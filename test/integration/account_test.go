package integration

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
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

func TestAccountMeOrgIsolation(t *testing.T) {
	ts, mr, orgs, usageStore := newAccountStack(t)

	orgA, _ := orgs.CreateOrg(context.Background(), "A")
	orgB, _ := orgs.CreateOrg(context.Background(), "B")
	keyA, _, _ := orgs.CreateAPIKey(context.Background(), orgA.ID)
	keyB, _, _ := orgs.CreateAPIKey(context.Background(), orgB.ID)

	mr.Set("budget:"+orgA.ID+":default", "111")
	mr.Set("budget:"+orgB.ID+":default", "222")
	_ = usageStore.InsertUsage(context.Background(), store.UsageEvent{
		OrgID: orgA.ID, BucketID: "default", RequestID: "a-only", Outcome: "settled",
	})
	_ = usageStore.InsertUsage(context.Background(), store.UsageEvent{
		OrgID: orgB.ID, BucketID: "default", RequestID: "b-only", Outcome: "settled",
	})

	// Org A must not see org B balances or usage.
	req, _ := http.NewRequest(http.MethodGet, ts.URL+"/me/buckets", nil)
	req.Header.Set("X-TokenGuard-Key", keyA)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("buckets: %v", err)
	}
	defer resp.Body.Close()
	var buckets struct {
		Buckets []budget.BucketBalance `json:"buckets"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&buckets)
	if len(buckets.Buckets) != 1 || buckets.Buckets[0].Balance != 111 {
		t.Fatalf("org A buckets = %+v", buckets.Buckets)
	}

	ureq, _ := http.NewRequest(http.MethodGet, ts.URL+"/me/usage", nil)
	ureq.Header.Set("X-TokenGuard-Key", keyA)
	uresp, err := http.DefaultClient.Do(ureq)
	if err != nil {
		t.Fatalf("usage: %v", err)
	}
	defer uresp.Body.Close()
	var usageBody struct {
		Events []store.UsageEvent `json:"events"`
	}
	_ = json.NewDecoder(uresp.Body).Decode(&usageBody)
	if len(usageBody.Events) != 1 || usageBody.Events[0].RequestID != "a-only" {
		t.Fatalf("org A usage = %+v", usageBody.Events)
	}

	// Cross-check org B independently.
	breq, _ := http.NewRequest(http.MethodGet, ts.URL+"/me/buckets", nil)
	breq.Header.Set("X-TokenGuard-Key", keyB)
	bresp, _ := http.DefaultClient.Do(breq)
	defer bresp.Body.Close()
	_ = json.NewDecoder(bresp.Body).Decode(&buckets)
	if len(buckets.Buckets) != 1 || buckets.Buckets[0].Balance != 222 {
		t.Fatalf("org B buckets = %+v", buckets.Buckets)
	}
}

func TestOpsStillRequiresAdminBasicAuth(t *testing.T) {
	ts, _, _, _ := newAccountStack(t)

	resp, err := http.Get(ts.URL + "/ops")
	if err != nil {
		t.Fatalf("ops: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", resp.StatusCode)
	}

	req, _ := http.NewRequest(http.MethodGet, ts.URL+"/ops", nil)
	req.SetBasicAuth("admin", "test-admin-key")
	okResp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("ops auth: %v", err)
	}
	defer okResp.Body.Close()
	body, _ := io.ReadAll(okResp.Body)
	if okResp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", okResp.StatusCode)
	}
	if !strings.Contains(string(body), "TokenGuard Ops") {
		t.Fatalf("unexpected ops body")
	}
}

func newAccountStack(t *testing.T) (*httptest.Server, *miniredis.Miniredis, *store.MemoryOrgStore, *store.MemoryUsageStore) {
	t.Helper()

	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	client, err := budget.NewClientFromRedis(rdb, 5*time.Minute)
	if err != nil {
		t.Fatalf("NewClientFromRedis: %v", err)
	}

	var upstreamCalls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamCalls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"choices":[],"usage":{"total_tokens":1}}`)
	}))

	cfg := config.Config{
		UpstreamURL:           upstream.URL,
		UpstreamHost:          "api.openai.com",
		EnforcementMode:       config.EnforcementEnforce,
		PreCheckTimeout:       50 * time.Millisecond,
		DefaultReservationEst: 4096,
		PromptTokenBuffer:     0,
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

	redisStore := admin.NewRedisStore(client)
	adminHandler := admin.NewHandlerWithOrgs(redisStore, usageStore, orgs, cfg.AdminAPIKey)
	opsHandler := ops.NewHandler(cfg.AdminAPIKey, redisStore, usageStore)
	llm := proxy.NewAuthMiddleware(orgs, handler)
	server := proxy.NewServer(cfg, llm, adminHandler, budget.NewReadiness(client), nil)
	server.Handle("GET /ops", opsHandler)
	account.NewHandler(orgs, client, usageStore).Register(server.Handle)
	ts := httptest.NewServer(server)

	t.Cleanup(func() {
		ts.Close()
		upstream.Close()
		client.Close()
		mr.Close()
	})
	return ts, mr, orgs, usageStore
}
