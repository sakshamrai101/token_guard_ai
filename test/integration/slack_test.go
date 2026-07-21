package integration

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/saksham/token-guard-ai/internal/admin"
	"github.com/saksham/token-guard-ai/internal/budget"
	"github.com/saksham/token-guard-ai/internal/config"
	"github.com/saksham/token-guard-ai/internal/proxy"
	"github.com/saksham/token-guard-ai/internal/store"
	"github.com/saksham/token-guard-ai/internal/usage"
)

func TestSlackBudgetExhaustedFiresOnce(t *testing.T) {
	var slackHits atomic.Int32
	var slackBody string
	slack := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		slackHits.Add(1)
		b, _ := io.ReadAll(r.Body)
		slackBody = string(b)
		w.WriteHeader(http.StatusOK)
	}))
	defer slack.Close()

	ts, mr, orgs := newSlackStack(t, "", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("upstream should not be called when exhausted")
	}))

	org, _ := orgs.CreateOrg(context.Background(), "Acme")
	_, _ = orgs.UpdateOrgSlackWebhook(context.Background(), org.ID, slack.URL)
	key, _, _ := orgs.CreateAPIKey(context.Background(), org.ID)
	mr.Set("budget:"+org.ID+":b1", "10")

	reqBody := []byte(`{"model":"gpt-4o","max_tokens":100,"messages":[{"role":"user","content":"hi"}]}`)
	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/v1/chat/completions", bytes.NewReader(reqBody))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Budget-Bucket-Id", "b1")
	req.Header.Set("X-TokenGuard-Key", key)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want 429", resp.StatusCode)
	}
	if slackHits.Load() != 1 {
		t.Fatalf("slack hits = %d, want 1", slackHits.Load())
	}
	if !strings.Contains(slackBody, "budget_exhausted") || !strings.Contains(slackBody, org.ID) {
		t.Fatalf("slack body = %s", slackBody)
	}
}

func TestSlackFailOpenFiresOnce(t *testing.T) {
	var slackHits atomic.Int32
	var slackBody string
	slack := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		slackHits.Add(1)
		b, _ := io.ReadAll(r.Body)
		slackBody = string(b)
		w.WriteHeader(http.StatusOK)
	}))
	defer slack.Close()

	var upstreamHits atomic.Int32
	ts, mr, orgs := newSlackStack(t, "", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamHits.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"choices":[],"usage":{"total_tokens":10}}`)
	}))

	org, _ := orgs.CreateOrg(context.Background(), "Acme")
	_, _ = orgs.UpdateOrgSlackWebhook(context.Background(), org.ID, slack.URL)
	key, _, _ := orgs.CreateAPIKey(context.Background(), org.ID)
	_ = orgs.SetDefaultBucket(context.Background(), org.ID, "default")
	mr.Set("budget:"+org.ID+":default", "100000")

	// Close Redis so pre-check fails open (missing bucket no longer fail-opens when default exists).
	mr.Close()

	reqBody := []byte(`{"model":"gpt-4o","max_tokens":10,"messages":[{"role":"user","content":"hi"}]}`)
	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/v1/chat/completions", bytes.NewReader(reqBody))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-TokenGuard-Key", key)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	_, _ = io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	if upstreamHits.Load() != 1 {
		t.Fatalf("upstream hits = %d", upstreamHits.Load())
	}
	if slackHits.Load() != 1 {
		t.Fatalf("slack hits = %d, want 1", slackHits.Load())
	}
	if !strings.Contains(slackBody, "fail_open") {
		t.Fatalf("slack body = %s", slackBody)
	}
}

func TestSlackWarning80AndDedupe(t *testing.T) {
	var slackHits atomic.Int32
	var lastBody string
	slack := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		slackHits.Add(1)
		b, _ := io.ReadAll(r.Body)
		lastBody = string(b)
		w.WriteHeader(http.StatusOK)
	}))
	defer slack.Close()

	ts, mr, orgs := newSlackStack(t, "", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"choices":[],"usage":{"prompt_tokens":50,"completion_tokens":150,"total_tokens":200}}`)
	}))

	org, _ := orgs.CreateOrg(context.Background(), "Acme")
	_, _ = orgs.UpdateOrgSlackWebhook(context.Background(), org.ID, slack.URL)
	key, _, _ := orgs.CreateAPIKey(context.Background(), org.ID)

	// estimate=100 (buffer 0). Start 250 → reserve ok → settle actual 200 → remaining 50 ≤ 20% of 250 → warn.
	mr.Set("budget:"+org.ID+":b1", "250")

	doChat := func(requestID string) {
		t.Helper()
		reqBody := []byte(`{"model":"gpt-4o","max_tokens":100,"messages":[{"role":"user","content":"hi"}]}`)
		req, _ := http.NewRequest(http.MethodPost, ts.URL+"/v1/chat/completions", bytes.NewReader(reqBody))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Budget-Bucket-Id", "b1")
		req.Header.Set("X-TokenGuard-Key", key)
		req.Header.Set("X-Request-Id", requestID)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("Do: %v", err)
		}
		_, _ = io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("status = %d", resp.StatusCode)
		}
	}

	doChat("req-warn-1")
	deadline := time.Now().Add(2 * time.Second)
	for slackHits.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if slackHits.Load() != 1 {
		t.Fatalf("after first settle hits = %d, want 1 body=%s", slackHits.Load(), lastBody)
	}
	if !strings.Contains(lastBody, "budget_warning_80") {
		t.Fatalf("body = %s", lastBody)
	}

	mr.Set("budget:"+org.ID+":b1", "250")
	doChat("req-warn-2")
	time.Sleep(200 * time.Millisecond)
	if slackHits.Load() != 1 {
		t.Fatalf("dedupe failed: hits = %d, want 1", slackHits.Load())
	}
}

func TestSlackOrgWebhookPreferredOverGlobal(t *testing.T) {
	var globalHits, orgHits atomic.Int32
	global := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		globalHits.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer global.Close()
	orgHook := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		orgHits.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer orgHook.Close()

	ts, mr, orgs := newSlackStack(t, global.URL, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("upstream should not be called")
	}))

	org, _ := orgs.CreateOrg(context.Background(), "Acme")
	_, _ = orgs.UpdateOrgSlackWebhook(context.Background(), org.ID, orgHook.URL)
	key, _, _ := orgs.CreateAPIKey(context.Background(), org.ID)
	mr.Set("budget:"+org.ID+":b1", "10")

	reqBody := []byte(`{"model":"gpt-4o","max_tokens":100,"messages":[{"role":"user","content":"hi"}]}`)
	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/v1/chat/completions", bytes.NewReader(reqBody))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Budget-Bucket-Id", "b1")
	req.Header.Set("X-TokenGuard-Key", key)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	resp.Body.Close()

	if orgHits.Load() != 1 {
		t.Fatalf("org hits = %d, want 1", orgHits.Load())
	}
	if globalHits.Load() != 0 {
		t.Fatalf("global hits = %d, want 0", globalHits.Load())
	}
}

func newSlackStack(t *testing.T, globalWebhook string, upstream http.Handler) (*httptest.Server, *miniredis.Miniredis, *store.MemoryOrgStore) {
	t.Helper()

	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	client, err := budget.NewClientFromRedis(rdb, 5*time.Minute)
	if err != nil {
		t.Fatalf("NewClientFromRedis: %v", err)
	}

	up := httptest.NewServer(upstream)
	cfg := config.Config{
		UpstreamURL:           up.URL,
		UpstreamHost:          "api.openai.com",
		EnforcementMode:       config.EnforcementEnforce,
		PreCheckTimeout:       50 * time.Millisecond,
		DefaultReservationEst: 4096,
		PromptTokenBuffer:     0, // keep estimates = max_tokens for alert threshold math
		AdminAPIKey:           "test-admin-key",
		SlackWebhookURL:       globalWebhook,
	}

	orgs := store.NewMemoryOrgStore()
	usageStore := store.NewMemoryUsageStore()
	metrics := &budget.Metrics{}
	alerter := budget.NewAlerter(cfg.SlackWebhookURL, nil).WithDedupe(client.WarningDedupe())
	budgetChecker := budget.NewRedisBudgetChecker(client, metrics)
	handler, err := proxy.NewHandlerWithRegistry(
		cfg, proxy.NewTransport(cfg),
		proxy.NewEnforcement(cfg, proxy.NewBudgetCheckerBridge(budgetChecker), nil),
		client, client, usage.NewOpenAIExtractor(), usage.NewOpenAIStreamExtractor(),
		metrics, alerter, usageStore, orgs, nil,
	)
	if err != nil {
		t.Fatalf("NewHandler: %v", err)
	}
	handler.WithBalances(client)

	adminHandler := admin.NewHandlerWithOrgs(admin.NewRedisStore(client), usageStore, orgs, cfg.AdminAPIKey)
	llm := proxy.NewAuthMiddleware(orgs, handler)
	ts := httptest.NewServer(proxy.NewServer(cfg, llm, adminHandler, budget.NewReadiness(client), nil))

	t.Cleanup(func() {
		ts.Close()
		up.Close()
		client.Close()
		mr.Close()
	})
	return ts, mr, orgs
}
