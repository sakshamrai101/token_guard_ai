package integration

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/saksham/token-guard-ai/internal/budget"
	"github.com/saksham/token-guard-ai/internal/config"
	"github.com/saksham/token-guard-ai/internal/proxy"
	"github.com/saksham/token-guard-ai/internal/usage"
)

func TestConcurrentRequestsCannotOverspendWithSettlement(t *testing.T) {
	responseBody := `{"choices":[],"usage":{"prompt_tokens":50,"completion_tokens":150,"total_tokens":200}}`

	mr := miniredis.RunT(t)
	mr.Set("budget:default:test-bucket", "5000")

	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	client, err := budget.NewClientFromRedis(rdb, 5*time.Minute)
	if err != nil {
		t.Fatalf("NewClientFromRedis: %v", err)
	}
	t.Cleanup(func() {
		client.Close()
		mr.Close()
	})

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, responseBody)
	}))
	t.Cleanup(upstream.Close)

	cfg := config.Config{
		UpstreamURL:           upstream.URL,
		UpstreamHost:          "mock.openai.test",
		EnforcementMode:       config.EnforcementEnforce,
		MaxIdleConns:          10,
		MaxIdlePerHost:        10,
		IdleConnTimeout:       90 * time.Second,
		PreCheckTimeout:       50 * time.Millisecond,
		DefaultReservationEst: 4096,
		PromptTokenBuffer:     0,
	}

	metrics := &budget.Metrics{}
	budgetChecker := budget.NewRedisBudgetChecker(client, metrics)
	transport := proxy.NewTransport(cfg)
	enforcement := proxy.NewEnforcement(cfg, proxy.NewBudgetCheckerBridge(budgetChecker), nil)
	handler, err := proxy.NewHandler(cfg, transport, enforcement, client, client, usage.NewOpenAIExtractor(), usage.NewOpenAIStreamExtractor(), metrics, nil, nil, nil)
	if err != nil {
		t.Fatalf("NewHandler: %v", err)
	}
	proxyServer := proxy.NewServer(cfg, handler, nil, budget.NewReadiness(client), nil)
	ts := httptest.NewServer(proxyServer)
	t.Cleanup(ts.Close)

	// max_tokens=200, buffer=0 → estimate=200 per request
	reqBody := []byte(`{"model":"gpt-4o","max_tokens":200,"messages":[{"role":"user","content":"hi"}]}`)

	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			req, err := http.NewRequest(http.MethodPost, ts.URL+"/v1/chat/completions", bytes.NewReader(reqBody))
			if err != nil {
				t.Errorf("NewRequest: %v", err)
				return
			}
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("X-Budget-Bucket-Id", "test-bucket")
			req.Header.Set("X-Request-Id", fmt.Sprintf("req-concurrent-%d", i))

			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Errorf("Do: %v", err)
				return
			}
			if _, err := io.ReadAll(resp.Body); err != nil {
				t.Errorf("read body: %v", err)
			}
			resp.Body.Close()
			if resp.StatusCode != http.StatusOK {
				t.Errorf("status = %d, want 200", resp.StatusCode)
			}
		}(i)
	}
	wg.Wait()

	waitForBalance(t, mr, "budget:default:test-bucket", 1000, 3*time.Second)

	denyBody := []byte(`{"model":"gpt-4o","max_tokens":1024,"messages":[{"role":"user","content":"hi"}]}`)
	denyReq, err := http.NewRequest(http.MethodPost, ts.URL+"/v1/chat/completions", bytes.NewReader(denyBody))
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	denyReq.Header.Set("Content-Type", "application/json")
	denyReq.Header.Set("X-Budget-Bucket-Id", "test-bucket")
	denyReq.Header.Set("X-Request-Id", "req-deny-after-concurrent")

	denyResp, err := http.DefaultClient.Do(denyReq)
	if err != nil {
		t.Fatalf("Do deny: %v", err)
	}
	denyResp.Body.Close()

	if denyResp.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want 429 after budget nearly exhausted", denyResp.StatusCode)
	}
}

func TestGoroutineLeakOnAbortedStreams(t *testing.T) {
	mr := miniredis.RunT(t)
	mr.Set("budget:default:test-bucket", "10000000")
	rdb := redis.NewClient(&redis.Options{
		Addr:         mr.Addr(),
		PoolSize:     4,
		MinIdleConns: 0,
	})
	client, err := budget.NewClientFromRedis(rdb, 5*time.Minute)
	if err != nil {
		t.Fatalf("NewClientFromRedis: %v", err)
	}
	t.Cleanup(func() {
		_ = client.Close()
		mr.Close()
	})

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher, _ := w.(http.Flusher)
		for i := 0; i < 20; i++ {
			select {
			case <-r.Context().Done():
				return
			default:
			}
			_, _ = fmt.Fprintf(w, "data: {\"choices\":[{\"delta\":{\"content\":\"%d\"}}]}\n\n", i)
			if flusher != nil {
				flusher.Flush()
			}
			select {
			case <-r.Context().Done():
				return
			case <-time.After(1 * time.Millisecond):
			}
		}
		_, _ = io.WriteString(w, "data: [DONE]\n\n")
	}))
	t.Cleanup(upstream.Close)

	cfg := config.Config{
		UpstreamURL:           upstream.URL,
		UpstreamHost:          "mock.openai.test",
		EnforcementMode:       config.EnforcementEnforce,
		PreCheckTimeout:       50 * time.Millisecond,
		DefaultReservationEst: 4096,
		PromptTokenBuffer:     0,
	}
	metrics := &budget.Metrics{}
	budgetChecker := budget.NewRedisBudgetChecker(client, metrics)
	handler, err := proxy.NewHandler(cfg, proxy.NewTransport(cfg), proxy.NewEnforcement(cfg, proxy.NewBudgetCheckerBridge(budgetChecker), nil), client, client, usage.NewOpenAIExtractor(), usage.NewOpenAIStreamExtractor(), metrics, nil, nil, nil)
	if err != nil {
		t.Fatalf("NewHandler: %v", err)
	}
	ts := httptest.NewServer(proxy.NewServer(cfg, handler, nil, budget.NewReadiness(client), nil))
	t.Cleanup(ts.Close)

	abortClient := &http.Client{
		Transport: &http.Transport{DisableKeepAlives: true},
		Timeout:   5 * time.Second,
	}

	runAbortedStreams := func(n int) {
		sem := make(chan struct{}, 50)
		var wg sync.WaitGroup
		for i := 0; i < n; i++ {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				sem <- struct{}{}
				defer func() { <-sem }()

				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()

				reqBody := []byte(`{"model":"gpt-4o","max_tokens":10,"stream":true,"messages":[{"role":"user","content":"hi"}]}`)
				req, err := http.NewRequestWithContext(ctx, http.MethodPost, ts.URL+"/v1/chat/completions", bytes.NewReader(reqBody))
				if err != nil {
					return
				}
				req.Header.Set("Content-Type", "application/json")
				req.Header.Set("X-Budget-Bucket-Id", "test-bucket")
				req.Header.Set("X-Request-Id", fmt.Sprintf("req-abort-%d", i))

				resp, err := abortClient.Do(req)
				if err != nil {
					return
				}
				buf := make([]byte, 16)
				_, _ = resp.Body.Read(buf)
				cancel()
				resp.Body.Close()
			}(i)
		}
		wg.Wait()
	}

	baseline := runtime.NumGoroutine()

	runAbortedStreams(1000)
	abortClient.CloseIdleConnections()

	settleDeadline := time.Now().Add(5 * time.Second)
	for metrics.SettleSuccess.Load() < 900 && time.Now().Before(settleDeadline) {
		time.Sleep(50 * time.Millisecond)
	}
	if metrics.SettleSuccess.Load() < 900 {
		t.Fatalf("settle success = %d, want at least 900", metrics.SettleSuccess.Load())
	}
	// Async settle retries run up to ~300ms; let stragglers finish before closing Redis.
	time.Sleep(400 * time.Millisecond)

	// Release miniredis connection-handler goroutines before counting; pool growth is
	// test infrastructure noise, not proxy leaks.
	if err := client.Close(); err != nil {
		t.Fatalf("client.Close: %v", err)
	}
	abortClient.CloseIdleConnections()
	ts.Close()
	upstream.Close()

	runtime.GC()

	deadline := time.Now().Add(3 * time.Second)
	const tolerance = 25
	for time.Now().Before(deadline) {
		runtime.GC()
		time.Sleep(200 * time.Millisecond)
		after := runtime.NumGoroutine()
		if after <= baseline+tolerance {
			return
		}
	}
	t.Fatalf("goroutines = %d, baseline = %d, delta %d exceeds tolerance %d", runtime.NumGoroutine(), baseline, runtime.NumGoroutine()-baseline, tolerance)
}
