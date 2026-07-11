package integration

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
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

func TestSettleWritesUsageEventAndDumpReturnsIt(t *testing.T) {
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
		_, _ = io.WriteString(w, `{"choices":[],"usage":{"prompt_tokens":50,"completion_tokens":150,"total_tokens":200}}`)
	}))
	t.Cleanup(upstream.Close)

	cfg := config.Config{
		UpstreamURL:           upstream.URL,
		UpstreamHost:          "api.openai.com",
		EnforcementMode:       config.EnforcementEnforce,
		MaxIdleConns:          10,
		MaxIdlePerHost:        10,
		IdleConnTimeout:       90 * time.Second,
		PreCheckTimeout:       50 * time.Millisecond,
		DefaultReservationEst: 4096,
		PromptTokenBuffer:     512,
		AdminAPIKey:           "test-admin-key",
	}

	usageStore := store.NewMemoryUsageStore()
	metrics := &budget.Metrics{}
	budgetChecker := budget.NewRedisBudgetChecker(client, metrics)
	transport := proxy.NewTransport(cfg)
	enforcement := proxy.NewEnforcement(cfg, proxy.NewBudgetCheckerBridge(budgetChecker), nil)
	handler, err := proxy.NewHandler(
		cfg, transport, enforcement, client, client,
		usage.NewOpenAIExtractor(), usage.NewOpenAIStreamExtractor(),
		metrics, nil, usageStore, nil,
	)
	if err != nil {
		t.Fatalf("NewHandler: %v", err)
	}

	adminHandler := admin.NewHandler(admin.NewRedisStore(client), usageStore, cfg.AdminAPIKey)
	ts := httptest.NewServer(proxy.NewServer(cfg, handler, adminHandler, budget.NewReadiness(client), nil))
	t.Cleanup(ts.Close)

	reqBody := []byte(`{"model":"gpt-4o","max_tokens":100,"messages":[{"role":"user","content":"hi"}]}`)
	req, err := http.NewRequest(http.MethodPost, ts.URL+"/v1/chat/completions", bytes.NewReader(reqBody))
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Budget-Bucket-Id", "test-bucket")
	req.Header.Set("X-Request-Id", "req-usage-dump")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	_, _ = io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}

	dumpReq, _ := http.NewRequest(http.MethodGet, ts.URL+"/admin/v1/usage?bucket_id=test-bucket&limit=10", nil)
	dumpReq.Header.Set("Authorization", "Bearer test-admin-key")
	dumpResp, err := http.DefaultClient.Do(dumpReq)
	if err != nil {
		t.Fatalf("dump Do: %v", err)
	}
	defer dumpResp.Body.Close()
	if dumpResp.StatusCode != http.StatusOK {
		t.Fatalf("dump status = %d, want 200", dumpResp.StatusCode)
	}

	var dump struct {
		Events []store.UsageEvent `json:"events"`
	}
	if err := json.NewDecoder(dumpResp.Body).Decode(&dump); err != nil {
		t.Fatalf("decode dump: %v", err)
	}
	if len(dump.Events) != 1 {
		t.Fatalf("events = %+v, want 1", dump.Events)
	}
	ev := dump.Events[0]
	if ev.RequestID != "req-usage-dump" || ev.BucketID != "test-bucket" || ev.Actual != 200 || ev.Outcome != "settled" {
		t.Fatalf("event = %+v", ev)
	}
	if ev.OrgID != store.DefaultOrgID {
		t.Fatalf("org_id = %q, want %q", ev.OrgID, store.DefaultOrgID)
	}

	listReq, _ := http.NewRequest(http.MethodGet, ts.URL+"/admin/v1/buckets", nil)
	listReq.Header.Set("Authorization", "Bearer test-admin-key")
	listResp, err := http.DefaultClient.Do(listReq)
	if err != nil {
		t.Fatalf("list buckets: %v", err)
	}
	defer listResp.Body.Close()
	var buckets struct {
		Buckets []budget.BucketBalance `json:"buckets"`
	}
	_ = json.NewDecoder(listResp.Body).Decode(&buckets)
	if len(buckets.Buckets) == 0 {
		t.Fatal("expected at least one bucket in list")
	}

	balStr, _ := mr.Get("budget:default:test-bucket")
	bal, _ := strconv.ParseInt(balStr, 10, 64)
	if bal != 4800 {
		t.Fatalf("balance = %d, want 4800", bal)
	}
}

func TestReleaseWritesUsageEvent(t *testing.T) {
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
		w.WriteHeader(http.StatusBadRequest)
		_, _ = io.WriteString(w, `{"error":"bad"}`)
	}))
	t.Cleanup(upstream.Close)

	cfg := config.Config{
		UpstreamURL:           upstream.URL,
		UpstreamHost:          "api.openai.com",
		EnforcementMode:       config.EnforcementEnforce,
		PreCheckTimeout:       50 * time.Millisecond,
		DefaultReservationEst: 4096,
		PromptTokenBuffer:     0,
		AdminAPIKey:           "test-admin-key",
	}

	usageStore := store.NewMemoryUsageStore()
	metrics := &budget.Metrics{}
	budgetChecker := budget.NewRedisBudgetChecker(client, metrics)
	handler, err := proxy.NewHandler(
		cfg, proxy.NewTransport(cfg),
		proxy.NewEnforcement(cfg, proxy.NewBudgetCheckerBridge(budgetChecker), nil),
		client, client, usage.NewOpenAIExtractor(), usage.NewOpenAIStreamExtractor(),
		metrics, nil, usageStore, nil,
	)
	if err != nil {
		t.Fatalf("NewHandler: %v", err)
	}
	adminHandler := admin.NewHandler(admin.NewRedisStore(client), usageStore, cfg.AdminAPIKey)
	ts := httptest.NewServer(proxy.NewServer(cfg, handler, adminHandler, budget.NewReadiness(client), nil))
	t.Cleanup(ts.Close)

	reqBody := []byte(`{"model":"gpt-4o","max_tokens":100,"messages":[{"role":"user","content":"hi"}]}`)
	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/v1/chat/completions", bytes.NewReader(reqBody))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Budget-Bucket-Id", "test-bucket")
	req.Header.Set("X-Request-Id", "req-release-usage")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	resp.Body.Close()

	dumpReq, _ := http.NewRequest(http.MethodGet, ts.URL+"/admin/v1/usage?bucket_id=test-bucket", nil)
	dumpReq.Header.Set("Authorization", "Bearer test-admin-key")
	dumpResp, err := http.DefaultClient.Do(dumpReq)
	if err != nil {
		t.Fatalf("dump: %v", err)
	}
	defer dumpResp.Body.Close()

	var dump struct {
		Events []store.UsageEvent `json:"events"`
	}
	_ = json.NewDecoder(dumpResp.Body).Decode(&dump)
	if len(dump.Events) != 1 || dump.Events[0].Outcome != "released" {
		t.Fatalf("events = %+v, want released", dump.Events)
	}
}
