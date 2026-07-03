package integration

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/saksham/token-guard-ai/internal/budget"
	"github.com/saksham/token-guard-ai/internal/config"
	"github.com/saksham/token-guard-ai/internal/proxy"
	"github.com/saksham/token-guard-ai/internal/usage"
)

func newProviderBudgetTestStack(t *testing.T, upstreamHost string, mode config.EnforcementMode, bucketBalance int64, upstream http.Handler) *testStack {
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
		UpstreamHost:          upstreamHost,
		EnforcementMode:       mode,
		MaxIdleConns:          10,
		MaxIdlePerHost:        10,
		IdleConnTimeout:       90 * time.Second,
		PreCheckTimeout:       50 * time.Millisecond,
		DefaultReservationEst: 4096,
		PromptTokenBuffer:     512,
	}

	providers := usage.RegistryForHost(upstreamHost)
	metrics := &budget.Metrics{}
	budgetChecker := budget.NewRedisBudgetChecker(client, metrics)
	transport := proxy.NewTransport(cfg)
	enforcement := proxy.NewEnforcement(cfg, proxy.NewBudgetCheckerBridge(budgetChecker), nil)
	handler, err := proxy.NewHandler(cfg, transport, enforcement, client, client, providers.JSON, providers.Stream, metrics, nil, nil)
	if err != nil {
		t.Fatalf("NewHandler: %v", err)
	}
	readiness := budget.NewReadiness(client)
	proxyServer := proxy.NewServer(cfg, handler, nil, readiness, nil)

	ts := httptest.NewServer(proxyServer)
	t.Cleanup(func() {
		ts.Close()
		upstreamServer.Close()
		client.Close()
		mr.Close()
	})

	return &testStack{mr: mr, client: client, server: ts}
}

func TestAnthropicNonStreamingSettlesToActualUsage(t *testing.T) {
	stack := newProviderBudgetTestStack(t, "api.anthropic.com", config.EnforcementEnforce, 5000, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"content":[{"type":"text","text":"Hello"}],"usage":{"input_tokens":50,"output_tokens":150}}`)
	}))

	reqBody := []byte(`{"model":"claude-3-5-sonnet-20241022","max_tokens":100,"messages":[{"role":"user","content":"hi"}]}`)
	req, err := http.NewRequest(http.MethodPost, stack.server.URL+"/v1/messages", bytes.NewReader(reqBody))
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Budget-Bucket-Id", "test-bucket")
	req.Header.Set("X-Request-Id", "req-anthropic-settle")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	defer resp.Body.Close()
	if _, err := io.ReadAll(resp.Body); err != nil {
		t.Fatalf("read body: %v", err)
	}

	balStr, err := stack.mr.Get("budget:test-bucket")
	if err != nil {
		t.Fatalf("get balance: %v", err)
	}
	bal, err := strconv.ParseInt(balStr, 10, 64)
	if err != nil {
		t.Fatalf("parse balance: %v", err)
	}
	if bal != 4800 {
		t.Fatalf("balance = %d, want 4800 (5000 - 200 actual usage)", bal)
	}
	if stack.mr.Exists("reservation:req-anthropic-settle") {
		t.Fatal("reservation should be deleted after settle")
	}
}

func TestAnthropicStreamingSettlesToActualUsage(t *testing.T) {
	stack := newProviderBudgetTestStack(t, "api.anthropic.com", config.EnforcementEnforce, 5000, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "event: message_start\n")
		_, _ = io.WriteString(w, "data: {\"type\":\"message_start\",\"message\":{\"usage\":{\"input_tokens\":50,\"output_tokens\":0}}}\n\n")
		_, _ = io.WriteString(w, "event: content_block_delta\n")
		_, _ = io.WriteString(w, "data: {\"type\":\"content_block_delta\",\"delta\":{\"type\":\"text_delta\",\"text\":\"Hi\"}}\n\n")
		_, _ = io.WriteString(w, "event: message_stop\n")
		_, _ = io.WriteString(w, "data: {\"type\":\"message_stop\",\"message\":{\"usage\":{\"input_tokens\":50,\"output_tokens\":150}}}\n\n")
	}))

	reqBody := []byte(`{"model":"claude-3-5-sonnet-20241022","max_tokens":100,"stream":true,"messages":[{"role":"user","content":"hi"}]}`)
	req, err := http.NewRequest(http.MethodPost, stack.server.URL+"/v1/messages", bytes.NewReader(reqBody))
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Budget-Bucket-Id", "test-bucket")
	req.Header.Set("X-Request-Id", "req-anthropic-stream-settle")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	defer resp.Body.Close()

	if !strings.Contains(resp.Header.Get("Content-Type"), "text/event-stream") {
		t.Fatalf("content-type = %q, want event-stream", resp.Header.Get("Content-Type"))
	}
	if _, err := io.Copy(io.Discard, resp.Body); err != nil {
		t.Fatalf("read stream: %v", err)
	}

	waitForBalance(t, stack.mr, "budget:test-bucket", 4800, 2*time.Second)
	if stack.mr.Exists("reservation:req-anthropic-stream-settle") {
		t.Fatal("reservation should be deleted after stream settle")
	}
}
