package integration

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/saksham/token-guard-ai/internal/account"
	"github.com/saksham/token-guard-ai/internal/admin"
	"github.com/saksham/token-guard-ai/internal/budget"
	"github.com/saksham/token-guard-ai/internal/config"
	"github.com/saksham/token-guard-ai/internal/ops"
	"github.com/saksham/token-guard-ai/internal/proxy"
	"github.com/saksham/token-guard-ai/internal/store"
	"github.com/saksham/token-guard-ai/internal/usage"
)

type multiRouteStack struct {
	mr           *miniredis.Miniredis
	client       *budget.Client
	server       *httptest.Server
	openaiHits   *atomic.Int32
	anthropicHits *atomic.Int32
	legacyHits   *atomic.Int32
	openaiPath   string
	anthropicPath string
	legacyPath   string
	openaiHost   string
	anthropicHost string
	legacyHost   string
}

func newMultiRouteStack(t *testing.T, balance int64) *multiRouteStack {
	t.Helper()

	mr := miniredis.RunT(t)
	if balance > 0 {
		mr.Set("budget:default:shared", strconv.FormatInt(balance, 10))
	}
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	client, err := budget.NewClientFromRedis(rdb, 5*time.Minute)
	if err != nil {
		t.Fatalf("redis: %v", err)
	}

	var openaiHits, anthropicHits, legacyHits atomic.Int32
	stack := &multiRouteStack{mr: mr, client: client, openaiHits: &openaiHits, anthropicHits: &anthropicHits, legacyHits: &legacyHits}

	openaiUp := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		openaiHits.Add(1)
		stack.openaiPath = r.URL.Path
		stack.openaiHost = r.Host
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"choices":[],"usage":{"prompt_tokens":10,"completion_tokens":40,"total_tokens":50}}`)
	}))
	anthropicUp := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		anthropicHits.Add(1)
		stack.anthropicPath = r.URL.Path
		stack.anthropicHost = r.Host
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"content":[{"type":"text","text":"hi"}],"usage":{"input_tokens":20,"output_tokens":30}}`)
	}))
	legacyUp := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		legacyHits.Add(1)
		stack.legacyPath = r.URL.Path
		stack.legacyHost = r.Host
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"choices":[],"usage":{"total_tokens":25}}`)
	}))

	cfg := config.Config{
		UpstreamURL:           legacyUp.URL,
		UpstreamHost:          "legacy.mock",
		OpenAIUpstreamURL:     openaiUp.URL,
		OpenAIUpstreamHost:    "api.openai.com",
		AnthropicUpstreamURL:  anthropicUp.URL,
		AnthropicUpstreamHost: "api.anthropic.com",
		EnforcementMode:       config.EnforcementEnforce,
		MaxIdleConns:          10,
		MaxIdlePerHost:        10,
		IdleConnTimeout:       90 * time.Second,
		PreCheckTimeout:       50 * time.Millisecond,
		DefaultReservationEst: 4096,
		PromptTokenBuffer:     0,
		AdminAPIKey:           "test-admin-key",
	}

	metrics := &budget.Metrics{}
	budgetChecker := budget.NewRedisBudgetChecker(client, metrics)
	legacyReg := usage.RegistryForHost(cfg.UpstreamHost)
	handler, err := proxy.NewHandler(
		cfg, proxy.NewTransport(cfg),
		proxy.NewEnforcement(cfg, proxy.NewBudgetCheckerBridge(budgetChecker), nil),
		client, client, legacyReg.JSON, legacyReg.Stream, metrics, nil, nil, nil,
	)
	if err != nil {
		t.Fatalf("NewHandler: %v", err)
	}

	orgs := store.NewMemoryOrgStore()
	usageStore := store.NewMemoryUsageStore()
	redisStore := admin.NewRedisStore(client)
	adminHandler := admin.NewHandlerWithOrgs(redisStore, usageStore, orgs, cfg.AdminAPIKey)
	opsHandler := ops.NewHandler(cfg.AdminAPIKey, redisStore, usageStore)
	server := proxy.NewServer(cfg, handler, adminHandler, budget.NewReadiness(client), nil)
	server.Handle("GET /ops", opsHandler)
	account.NewHandler(orgs, client, usageStore).Register(server.Handle)

	ts := httptest.NewServer(server)
	t.Cleanup(func() {
		ts.Close()
		openaiUp.Close()
		anthropicUp.Close()
		legacyUp.Close()
		client.Close()
		mr.Close()
	})
	stack.server = ts
	return stack
}

func TestMultiRouteOpenAISettles(t *testing.T) {
	stack := newMultiRouteStack(t, 5000)

	reqBody := []byte(`{"model":"gpt-4o","max_tokens":100,"messages":[{"role":"user","content":"hi"}]}`)
	req, _ := http.NewRequest(http.MethodPost, stack.server.URL+"/openai/v1/chat/completions", bytes.NewReader(reqBody))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Budget-Bucket-Id", "shared")
	req.Header.Set("X-Request-Id", "req-openai-multi")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	_, _ = io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	if stack.openaiHits.Load() != 1 {
		t.Fatalf("openai hits = %d", stack.openaiHits.Load())
	}
	if stack.openaiPath != "/v1/chat/completions" {
		t.Fatalf("forwarded path = %q", stack.openaiPath)
	}
	if stack.openaiHost != "api.openai.com" {
		t.Fatalf("Host = %q", stack.openaiHost)
	}
	if stack.anthropicHits.Load() != 0 || stack.legacyHits.Load() != 0 {
		t.Fatalf("wrong upstream hit")
	}
	// settle actual=50 → 4950
	bal := mustBalance(t, stack.mr, "budget:default:shared")
	if bal != 4950 {
		t.Fatalf("balance = %d, want 4950", bal)
	}
}

func TestMultiRouteAnthropicSettles(t *testing.T) {
	stack := newMultiRouteStack(t, 5000)

	reqBody := []byte(`{"model":"claude-3-5-sonnet-20241022","max_tokens":100,"messages":[{"role":"user","content":"hi"}]}`)
	req, _ := http.NewRequest(http.MethodPost, stack.server.URL+"/anthropic/v1/messages", bytes.NewReader(reqBody))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Budget-Bucket-Id", "shared")
	req.Header.Set("X-Request-Id", "req-anthropic-multi")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	_, _ = io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	if stack.anthropicHits.Load() != 1 {
		t.Fatalf("anthropic hits = %d", stack.anthropicHits.Load())
	}
	if stack.anthropicPath != "/v1/messages" {
		t.Fatalf("path = %q", stack.anthropicPath)
	}
	if stack.anthropicHost != "api.anthropic.com" {
		t.Fatalf("Host = %q", stack.anthropicHost)
	}
	// actual = 20+30 = 50 → 4950
	bal := mustBalance(t, stack.mr, "budget:default:shared")
	if bal != 4950 {
		t.Fatalf("balance = %d, want 4950", bal)
	}
}

func TestMultiRouteBothProvidersSameBucket(t *testing.T) {
	stack := newMultiRouteStack(t, 5000)

	doOpenAI := func(id string) {
		t.Helper()
		body := []byte(`{"model":"gpt-4o","max_tokens":100,"messages":[{"role":"user","content":"hi"}]}`)
		req, _ := http.NewRequest(http.MethodPost, stack.server.URL+"/openai/v1/chat/completions", bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Budget-Bucket-Id", "shared")
		req.Header.Set("X-Request-Id", id)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("openai: %v", err)
		}
		_, _ = io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("openai status = %d", resp.StatusCode)
		}
	}
	doAnthropic := func(id string) {
		t.Helper()
		body := []byte(`{"model":"claude-3-5-sonnet-20241022","max_tokens":100,"messages":[{"role":"user","content":"hi"}]}`)
		req, _ := http.NewRequest(http.MethodPost, stack.server.URL+"/anthropic/v1/messages", bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Budget-Bucket-Id", "shared")
		req.Header.Set("X-Request-Id", id)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("anthropic: %v", err)
		}
		_, _ = io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("anthropic status = %d", resp.StatusCode)
		}
	}

	doOpenAI("both-oai")
	doAnthropic("both-ant")
	// 5000 - 50 - 50 = 4900
	bal := mustBalance(t, stack.mr, "budget:default:shared")
	if bal != 4900 {
		t.Fatalf("balance = %d, want 4900", bal)
	}
	if stack.openaiHits.Load() != 1 || stack.anthropicHits.Load() != 1 {
		t.Fatalf("hits openai=%d anthropic=%d", stack.openaiHits.Load(), stack.anthropicHits.Load())
	}
}

func TestMultiRouteLegacyUsesUpstream(t *testing.T) {
	stack := newMultiRouteStack(t, 5000)

	body := []byte(`{"model":"gpt-4o","max_tokens":100,"messages":[{"role":"user","content":"hi"}]}`)
	req, _ := http.NewRequest(http.MethodPost, stack.server.URL+"/v1/chat/completions", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Budget-Bucket-Id", "shared")
	req.Header.Set("X-Request-Id", "req-legacy")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	_, _ = io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	if stack.legacyHits.Load() != 1 {
		t.Fatalf("legacy hits = %d", stack.legacyHits.Load())
	}
	if stack.openaiHits.Load() != 0 || stack.anthropicHits.Load() != 0 {
		t.Fatal("prefixed upstream should not be hit")
	}
	if stack.legacyPath != "/v1/chat/completions" {
		t.Fatalf("path = %q", stack.legacyPath)
	}
	if stack.legacyHost != "legacy.mock" {
		t.Fatalf("Host = %q", stack.legacyHost)
	}
	// settle 25 → 4975
	bal := mustBalance(t, stack.mr, "budget:default:shared")
	if bal != 4975 {
		t.Fatalf("balance = %d, want 4975", bal)
	}
}

func TestMultiRouteReservedAppRoutesNotProxied(t *testing.T) {
	stack := newMultiRouteStack(t, 5000)

	resp, err := http.Get(stack.server.URL + "/healthz")
	if err != nil {
		t.Fatalf("healthz: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(body), `"ok"`) {
		t.Fatalf("healthz = %d %s", resp.StatusCode, body)
	}
	if stack.openaiHits.Load()+stack.anthropicHits.Load()+stack.legacyHits.Load() != 0 {
		t.Fatal("healthz should not hit LLM upstream")
	}

	opsResp, err := http.Get(stack.server.URL + "/ops")
	if err != nil {
		t.Fatalf("ops: %v", err)
	}
	opsResp.Body.Close()
	if opsResp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("ops status = %d, want 401 without basic auth", opsResp.StatusCode)
	}

	meResp, err := http.Get(stack.server.URL + "/me/buckets")
	if err != nil {
		t.Fatalf("me: %v", err)
	}
	meResp.Body.Close()
	if meResp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("me status = %d, want 401", meResp.StatusCode)
	}
	if stack.legacyHits.Load() != 0 {
		t.Fatal("/me should not fall through to legacy upstream")
	}
}

func TestMultiRouteOpenAIStreamingSettles(t *testing.T) {
	mr := miniredis.RunT(t)
	mr.Set("budget:default:shared", "5000")
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	client, err := budget.NewClientFromRedis(rdb, 5*time.Minute)
	if err != nil {
		t.Fatalf("redis: %v", err)
	}

	openaiUp := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher, _ := w.(http.Flusher)
		_, _ = io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"content\":\"hi\"}}]}\n\n")
		if flusher != nil {
			flusher.Flush()
		}
		_, _ = io.WriteString(w, "data: {\"usage\":{\"prompt_tokens\":5,\"completion_tokens\":15,\"total_tokens\":20}}\n\n")
		_, _ = io.WriteString(w, "data: [DONE]\n\n")
	}))
	anthropicUp := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("anthropic should not be called")
	}))
	legacyUp := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("legacy should not be called")
	}))

	cfg := config.Config{
		UpstreamURL:           legacyUp.URL,
		UpstreamHost:          "legacy.mock",
		OpenAIUpstreamURL:     openaiUp.URL,
		OpenAIUpstreamHost:    "api.openai.com",
		AnthropicUpstreamURL:  anthropicUp.URL,
		AnthropicUpstreamHost: "api.anthropic.com",
		EnforcementMode:       config.EnforcementEnforce,
		PreCheckTimeout:       50 * time.Millisecond,
		DefaultReservationEst: 4096,
		PromptTokenBuffer:     0,
	}
	metrics := &budget.Metrics{}
	budgetChecker := budget.NewRedisBudgetChecker(client, metrics)
	handler, err := proxy.NewHandler(
		cfg, proxy.NewTransport(cfg),
		proxy.NewEnforcement(cfg, proxy.NewBudgetCheckerBridge(budgetChecker), nil),
		client, client, usage.NewOpenAIExtractor(), usage.NewOpenAIStreamExtractor(), metrics, nil, nil, nil,
	)
	if err != nil {
		t.Fatalf("NewHandler: %v", err)
	}
	ts := httptest.NewServer(proxy.NewServer(cfg, handler, nil, budget.NewReadiness(client), nil))
	t.Cleanup(func() {
		ts.Close()
		openaiUp.Close()
		anthropicUp.Close()
		legacyUp.Close()
		client.Close()
		mr.Close()
	})

	body := []byte(`{"model":"gpt-4o","stream":true,"max_tokens":100,"messages":[{"role":"user","content":"hi"}]}`)
	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/openai/v1/chat/completions", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Budget-Bucket-Id", "shared")
	req.Header.Set("X-Request-Id", "req-oai-stream")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	_, _ = io.ReadAll(resp.Body)
	resp.Body.Close()

	deadline := time.Now().Add(2 * time.Second)
	var bal int64
	for time.Now().Before(deadline) {
		bal = mustBalance(t, mr, "budget:default:shared")
		if bal == 4980 {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("balance = %d, want 4980 (stream settle 20)", bal)
}

func TestMultiRouteAnthropicStreamingSettles(t *testing.T) {
	mr := miniredis.RunT(t)
	mr.Set("budget:default:shared", "5000")
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	client, err := budget.NewClientFromRedis(rdb, 5*time.Minute)
	if err != nil {
		t.Fatalf("redis: %v", err)
	}

	anthropicUp := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "event: message_start\ndata: {\"type\":\"message_start\"}\n\n")
		_, _ = io.WriteString(w, "event: message_stop\ndata: {\"type\":\"message_stop\",\"usage\":{\"input_tokens\":8,\"output_tokens\":12}}\n\n")
	}))
	openaiUp := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("openai should not be called")
	}))
	legacyUp := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("legacy should not be called")
	}))

	cfg := config.Config{
		UpstreamURL:           legacyUp.URL,
		UpstreamHost:          "legacy.mock",
		OpenAIUpstreamURL:     openaiUp.URL,
		OpenAIUpstreamHost:    "api.openai.com",
		AnthropicUpstreamURL:  anthropicUp.URL,
		AnthropicUpstreamHost: "api.anthropic.com",
		EnforcementMode:       config.EnforcementEnforce,
		PreCheckTimeout:       50 * time.Millisecond,
		DefaultReservationEst: 4096,
		PromptTokenBuffer:     0,
	}
	metrics := &budget.Metrics{}
	budgetChecker := budget.NewRedisBudgetChecker(client, metrics)
	handler, err := proxy.NewHandler(
		cfg, proxy.NewTransport(cfg),
		proxy.NewEnforcement(cfg, proxy.NewBudgetCheckerBridge(budgetChecker), nil),
		client, client, usage.NewOpenAIExtractor(), usage.NewOpenAIStreamExtractor(), metrics, nil, nil, nil,
	)
	if err != nil {
		t.Fatalf("NewHandler: %v", err)
	}
	ts := httptest.NewServer(proxy.NewServer(cfg, handler, nil, budget.NewReadiness(client), nil))
	t.Cleanup(func() {
		ts.Close()
		openaiUp.Close()
		anthropicUp.Close()
		legacyUp.Close()
		client.Close()
		mr.Close()
	})

	body := []byte(`{"model":"claude-3-5-sonnet-20241022","stream":true,"max_tokens":100,"messages":[{"role":"user","content":"hi"}]}`)
	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/anthropic/v1/messages", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Budget-Bucket-Id", "shared")
	req.Header.Set("X-Request-Id", "req-ant-stream")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	_, _ = io.ReadAll(resp.Body)
	resp.Body.Close()

	deadline := time.Now().Add(2 * time.Second)
	var bal int64
	for time.Now().Before(deadline) {
		bal = mustBalance(t, mr, "budget:default:shared")
		if bal == 4980 { // 8+12=20
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("balance = %d, want 4980 (anthropic stream settle 20)", bal)
}

func mustBalance(t *testing.T, mr *miniredis.Miniredis, key string) int64 {
	t.Helper()
	s, err := mr.Get(key)
	if err != nil {
		t.Fatalf("get %s: %v", key, err)
	}
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	return n
}
