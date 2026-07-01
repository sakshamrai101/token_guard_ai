package integration

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/saksham/token-guard-ai/internal/budget"
	"github.com/saksham/token-guard-ai/internal/config"
	"github.com/saksham/token-guard-ai/internal/proxy"
)

type testStack struct {
	mr     *miniredis.Miniredis
	client *budget.Client
	server *httptest.Server
}

func newBudgetTestStack(t *testing.T, mode config.EnforcementMode, bucketBalance int64, upstream http.Handler) *testStack {
	t.Helper()

	mr := miniredis.RunT(t)
	if bucketBalance > 0 {
		mr.Set("budget:test-bucket", strconv.FormatInt(bucketBalance, 10))
	}

	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	client, err := budget.NewClientFromRedis(rdb, 5*time.Minute)
	if err != nil {
		t.Fatalf("NewClientFromRedis: %v", err)
	}

	upstreamServer := httptest.NewServer(upstream)

	cfg := config.Config{
		UpstreamURL:           upstreamServer.URL,
		UpstreamHost:          "mock.openai.test",
		EnforcementMode:       mode,
		MaxIdleConns:          10,
		MaxIdlePerHost:        10,
		IdleConnTimeout:       90 * time.Second,
		PreCheckTimeout:       50 * time.Millisecond,
		DefaultReservationEst: 4096,
		PromptTokenBuffer:     512,
	}

	metrics := &budget.Metrics{}
	budgetChecker := budget.NewRedisBudgetChecker(client, metrics)
	transport := proxy.NewTransport(cfg)
	enforcement := proxy.NewEnforcement(cfg, proxy.NewBudgetCheckerBridge(budgetChecker), nil)
	handler, err := proxy.NewHandler(cfg, transport, enforcement, client, metrics, nil, nil)
	if err != nil {
		t.Fatalf("NewHandler: %v", err)
	}
	readiness := budget.NewReadiness(client)
	proxyServer := proxy.NewServer(cfg, handler, readiness, nil)

	ts := httptest.NewServer(proxyServer)
	t.Cleanup(func() {
		ts.Close()
		upstreamServer.Close()
		client.Close()
		mr.Close()
	})

	return &testStack{mr: mr, client: client, server: ts}
}

func TestEnforceModeReturns429WhenBudgetExhausted(t *testing.T) {
	var upstreamCalls atomic.Int32
	stack := newBudgetTestStack(t, config.EnforcementEnforce, 100, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamCalls.Add(1)
		w.WriteHeader(http.StatusOK)
	}))

	reqBody := []byte(`{"model":"gpt-4o","max_tokens":1024,"messages":[{"role":"user","content":"hi"}]}`)
	req, err := http.NewRequest(http.MethodPost, stack.server.URL+"/v1/chat/completions", bytes.NewReader(reqBody))
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Budget-Bucket-Id", "test-bucket")
	req.Header.Set("X-Request-Id", "req-deny")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want 429", resp.StatusCode)
	}
	if upstreamCalls.Load() != 0 {
		t.Fatalf("upstream called %d times, want 0", upstreamCalls.Load())
	}
	if got := resp.Header.Get("Retry-After"); got != "60" {
		t.Errorf("Retry-After = %q, want 60", got)
	}
}

func TestFailOpenWhenRedisDown(t *testing.T) {
	var upstreamCalls atomic.Int32
	stack := newBudgetTestStack(t, config.EnforcementEnforce, 10000, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamCalls.Add(1)
		_, _ = io.WriteString(w, `{"ok":true}`)
	}))

	stack.mr.Close()

	reqBody := []byte(`{"model":"gpt-4o","max_tokens":100,"messages":[{"role":"user","content":"hi"}]}`)
	req, err := http.NewRequest(http.MethodPost, stack.server.URL+"/v1/chat/completions", bytes.NewReader(reqBody))
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Budget-Bucket-Id", "test-bucket")
	req.Header.Set("X-Request-Id", "req-failopen")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200 (fail-open forward)", resp.StatusCode)
	}
	if upstreamCalls.Load() != 1 {
		t.Fatalf("upstream called %d times, want 1", upstreamCalls.Load())
	}
}

func TestReadyz503WhenRedisUnreachable(t *testing.T) {
	stack := newBudgetTestStack(t, config.EnforcementEnforce, 1000, http.NotFoundHandler())
	stack.mr.Close()

	resp, err := http.Get(stack.server.URL + "/readyz")
	if err != nil {
		t.Fatalf("GET /readyz: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("/readyz status = %d, want 503", resp.StatusCode)
	}
}

func TestReleaseBudgetOnUpstream4xx(t *testing.T) {
	stack := newBudgetTestStack(t, config.EnforcementEnforce, 5000, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = io.WriteString(w, `{"error":"bad request"}`)
	}))

	reqBody := []byte(`{"model":"gpt-4o","max_tokens":100,"messages":[{"role":"user","content":"hi"}]}`)
	req, err := http.NewRequest(http.MethodPost, stack.server.URL+"/v1/chat/completions", bytes.NewReader(reqBody))
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Budget-Bucket-Id", "test-bucket")
	req.Header.Set("X-Request-Id", "req-release")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	resp.Body.Close()

	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", resp.StatusCode)
	}

	balStr, err := stack.mr.Get("budget:test-bucket")
	if err != nil {
		t.Fatalf("get balance: %v", err)
	}
	bal, err := strconv.ParseInt(balStr, 10, 64)
	if err != nil {
		t.Fatalf("parse balance: %v", err)
	}
	if bal != 5000 {
		t.Fatalf("balance after release = %d, want 5000 (full refund)", bal)
	}
}

func TestSuccessful200HoldsReservationUntilTTL(t *testing.T) {
	stack := newBudgetTestStack(t, config.EnforcementEnforce, 5000, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"id":"ok"}`)
	}))

	reqBody := []byte(`{"model":"gpt-4o","max_tokens":100,"messages":[{"role":"user","content":"hi"}]}`)
	req, err := http.NewRequest(http.MethodPost, stack.server.URL+"/v1/chat/completions", bytes.NewReader(reqBody))
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Budget-Bucket-Id", "test-bucket")
	req.Header.Set("X-Request-Id", "req-hold")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	resp.Body.Close()

	// estimate = 100 + 512 = 612 held, not settled on 200
	balStr, err := stack.mr.Get("budget:test-bucket")
	if err != nil {
		t.Fatalf("get balance: %v", err)
	}
	bal, err := strconv.ParseInt(balStr, 10, 64)
	if err != nil {
		t.Fatalf("parse balance: %v", err)
	}
	if bal != 4388 {
		t.Fatalf("balance = %d, want 4388 (hold active, no settle on 200)", bal)
	}
	if !stack.mr.Exists("reservation:req-hold") {
		t.Fatal("reservation should still exist after 200 response")
	}
}

func TestShadowModeForwardsWhenBudgetDenied(t *testing.T) {
	var upstreamCalls atomic.Int32
	stack := newBudgetTestStack(t, config.EnforcementShadow, 100, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamCalls.Add(1)
		_, _ = io.WriteString(w, `{"ok":true}`)
	}))

	reqBody := []byte(`{"model":"gpt-4o","max_tokens":1024,"messages":[{"role":"user","content":"hi"}]}`)
	req, err := http.NewRequest(http.MethodPost, stack.server.URL+"/v1/chat/completions", bytes.NewReader(reqBody))
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Budget-Bucket-Id", "test-bucket")
	req.Header.Set("X-Request-Id", "req-shadow")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200 in shadow mode", resp.StatusCode)
	}
	if upstreamCalls.Load() != 1 {
		t.Fatalf("upstream called %d times, want 1", upstreamCalls.Load())
	}
}

func TestReserveUsesParsedEstimate(t *testing.T) {
	stack := newBudgetTestStack(t, config.EnforcementEnforce, 5000, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"ok":true}`)
	}))

	// max_tokens=200 + buffer 512 = 712
	reqBody := []byte(`{"model":"gpt-4o","max_tokens":200,"messages":[{"role":"user","content":"hi"}]}`)
	req, err := http.NewRequest(http.MethodPost, stack.server.URL+"/v1/chat/completions", bytes.NewReader(reqBody))
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Budget-Bucket-Id", "test-bucket")
	req.Header.Set("X-Request-Id", "req-est")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	resp.Body.Close()

	balStr, err := stack.mr.Get("budget:test-bucket")
	if err != nil {
		t.Fatalf("get balance: %v", err)
	}
	bal, err := strconv.ParseInt(balStr, 10, 64)
	if err != nil {
		t.Fatalf("parse balance: %v", err)
	}
	if bal != 4288 {
		t.Fatalf("balance = %d, want 4288 (5000 - 712 estimate hold)", bal)
	}
}

func TestFailOpenMissingBucketForwards(t *testing.T) {
	var upstreamCalls atomic.Int32
	stack := newBudgetTestStack(t, config.EnforcementEnforce, 10000, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamCalls.Add(1)
		_, _ = io.WriteString(w, `{"ok":true}`)
	}))

	req, err := http.NewRequest(http.MethodPost, stack.server.URL+"/v1/chat/completions", bytes.NewReader([]byte(`{}`)))
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Request-Id", "req-missing-bucket")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200 fail-open", resp.StatusCode)
	}
	if upstreamCalls.Load() != 1 {
		t.Fatalf("upstream called %d times, want 1", upstreamCalls.Load())
	}

	balStr, err := stack.mr.Get("budget:test-bucket")
	if err != nil {
		t.Fatalf("get balance: %v", err)
	}
	bal, _ := strconv.ParseInt(balStr, 10, 64)
	if bal != 10000 {
		t.Fatalf("balance = %d, want unchanged 10000 when fail-open", bal)
	}
}

func TestReleaseBudgetOnUpstream5xx(t *testing.T) {
	stack := newBudgetTestStack(t, config.EnforcementEnforce, 5000, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = io.WriteString(w, `{"error":"upstream failure"}`)
	}))

	reqBody := []byte(`{"model":"gpt-4o","max_tokens":100,"messages":[{"role":"user","content":"hi"}]}`)
	req, err := http.NewRequest(http.MethodPost, stack.server.URL+"/v1/chat/completions", bytes.NewReader(reqBody))
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Budget-Bucket-Id", "test-bucket")
	req.Header.Set("X-Request-Id", "req-5xx")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	resp.Body.Close()

	if resp.StatusCode != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", resp.StatusCode)
	}

	balStr, err := stack.mr.Get("budget:test-bucket")
	if err != nil {
		t.Fatalf("get balance: %v", err)
	}
	bal, _ := strconv.ParseInt(balStr, 10, 64)
	if bal != 5000 {
		t.Fatalf("balance after 5xx release = %d, want 5000", bal)
	}
}
