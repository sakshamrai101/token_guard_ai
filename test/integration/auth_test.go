package integration

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
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

func TestMissingTokenGuardKeyReturns401(t *testing.T) {
	var upstreamCalls atomic.Int32
	ts, _, _ := newMultiTenantStack(t, &upstreamCalls)

	reqBody := []byte(`{"model":"gpt-4o","max_tokens":10,"messages":[{"role":"user","content":"hi"}]}`)
	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/v1/chat/completions", bytes.NewReader(reqBody))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Budget-Bucket-Id", "shared")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", resp.StatusCode)
	}
	if upstreamCalls.Load() != 0 {
		t.Fatalf("upstream called %d times, want 0", upstreamCalls.Load())
	}
}

func TestOrgsHaveIsolatedBudgets(t *testing.T) {
	var upstreamCalls atomic.Int32
	ts, mr, orgs := newMultiTenantStack(t, &upstreamCalls)

	orgA, _ := orgs.CreateOrg(context.Background(), "A")
	orgB, _ := orgs.CreateOrg(context.Background(), "B")
	keyA, _, _ := orgs.CreateAPIKey(context.Background(), orgA.ID)
	keyB, _, _ := orgs.CreateAPIKey(context.Background(), orgB.ID)

	mr.Set("budget:"+orgA.ID+":shared", "1000")
	mr.Set("budget:"+orgB.ID+":shared", "5000")

	doReq := func(key string, requestID string) {
		t.Helper()
		reqBody := []byte(`{"model":"gpt-4o","max_tokens":100,"messages":[{"role":"user","content":"hi"}]}`)
		req, _ := http.NewRequest(http.MethodPost, ts.URL+"/v1/chat/completions", bytes.NewReader(reqBody))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Budget-Bucket-Id", "shared")
		req.Header.Set("X-TokenGuard-Key", key)
		req.Header.Set("X-Request-Id", requestID)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("Do: %v", err)
		}
		_, _ = io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("status = %d, want 200", resp.StatusCode)
		}
	}

	doReq(keyA, "req-a")
	doReq(keyB, "req-b")

	balA, _ := strconv.ParseInt(mustGet(t, mr, "budget:"+orgA.ID+":shared"), 10, 64)
	balB, _ := strconv.ParseInt(mustGet(t, mr, "budget:"+orgB.ID+":shared"), 10, 64)
	// estimate = 100+512=612; settle to actual 200
	if balA != 800 {
		t.Fatalf("org A balance = %d, want 800", balA)
	}
	if balB != 4800 {
		t.Fatalf("org B balance = %d, want 4800", balB)
	}
}

func TestSettleUsageEventUsesOrgID(t *testing.T) {
	var upstreamCalls atomic.Int32
	ts, mr, orgs := newMultiTenantStack(t, &upstreamCalls)

	org, _ := orgs.CreateOrg(context.Background(), "Acme")
	key, _, _ := orgs.CreateAPIKey(context.Background(), org.ID)
	mr.Set("budget:"+org.ID+":b1", "5000")

	reqBody := []byte(`{"model":"gpt-4o","max_tokens":100,"messages":[{"role":"user","content":"hi"}]}`)
	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/v1/chat/completions", bytes.NewReader(reqBody))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Budget-Bucket-Id", "b1")
	req.Header.Set("X-TokenGuard-Key", key)
	req.Header.Set("X-Request-Id", "req-org-usage")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	_, _ = io.ReadAll(resp.Body)
	resp.Body.Close()

	dumpReq, _ := http.NewRequest(http.MethodGet, ts.URL+"/admin/v1/usage?bucket_id=b1", nil)
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
	if len(dump.Events) != 1 {
		t.Fatalf("events = %+v", dump.Events)
	}
	if dump.Events[0].OrgID != org.ID {
		t.Fatalf("org_id = %q, want %q", dump.Events[0].OrgID, org.ID)
	}
}

func newMultiTenantStack(t *testing.T, upstreamCalls *atomic.Int32) (*httptest.Server, *miniredis.Miniredis, *store.MemoryOrgStore) {
	t.Helper()

	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	client, err := budget.NewClientFromRedis(rdb, 5*time.Minute)
	if err != nil {
		t.Fatalf("NewClientFromRedis: %v", err)
	}

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-TokenGuard-Key") != "" {
			t.Errorf("X-TokenGuard-Key should not reach upstream")
		}
		upstreamCalls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"choices":[],"usage":{"prompt_tokens":50,"completion_tokens":150,"total_tokens":200}}`)
	}))

	cfg := config.Config{
		UpstreamURL:           upstream.URL,
		UpstreamHost:          "api.openai.com",
		EnforcementMode:       config.EnforcementEnforce,
		PreCheckTimeout:       50 * time.Millisecond,
		DefaultReservationEst: 4096,
		PromptTokenBuffer:     512,
		AdminAPIKey:           "test-admin-key",
	}

	orgs := store.NewMemoryOrgStore()
	usageStore := store.NewMemoryUsageStore()
	metrics := &budget.Metrics{}
	budgetChecker := budget.NewRedisBudgetChecker(client, metrics)
	handler, err := proxy.NewHandlerWithRegistry(
		cfg, proxy.NewTransport(cfg),
		proxy.NewEnforcement(cfg, proxy.NewBudgetCheckerBridge(budgetChecker), nil),
		client, client, usage.NewOpenAIExtractor(), usage.NewOpenAIStreamExtractor(),
		metrics, nil, usageStore, orgs, nil,
	)
	if err != nil {
		t.Fatalf("NewHandler: %v", err)
	}

	adminHandler := admin.NewHandlerWithOrgs(admin.NewRedisStore(client), usageStore, orgs, cfg.AdminAPIKey)
	llm := proxy.NewAuthMiddleware(orgs, handler)
	ts := httptest.NewServer(proxy.NewServer(cfg, llm, adminHandler, budget.NewReadiness(client), nil))

	t.Cleanup(func() {
		ts.Close()
		upstream.Close()
		client.Close()
		mr.Close()
	})
	return ts, mr, orgs
}

func mustGet(t *testing.T, mr *miniredis.Miniredis, key string) string {
	t.Helper()
	v, err := mr.Get(key)
	if err != nil {
		t.Fatalf("get %s: %v", key, err)
	}
	return v
}
