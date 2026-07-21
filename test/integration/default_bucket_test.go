package integration

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/saksham/token-guard-ai/internal/budget"
	"github.com/saksham/token-guard-ai/internal/config"
	"github.com/saksham/token-guard-ai/internal/proxy"
	"github.com/saksham/token-guard-ai/internal/store"
	"github.com/saksham/token-guard-ai/internal/usage"
)

func TestProxyUsesDefaultBucketWhenHeaderAbsent(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	client, err := budget.NewClientFromRedis(rdb, 5*time.Minute)
	if err != nil {
		t.Fatalf("client: %v", err)
	}
	t.Cleanup(func() { client.Close(); mr.Close() })

	orgs := store.NewMemoryOrgStore()
	org, _ := orgs.CreateOrgWithEmail(context.Background(), "a@b.com", "a@b.com")
	_ = orgs.SetDefaultBucket(context.Background(), org.ID, "default")
	raw, _, _ := orgs.CreateAPIKey(context.Background(), org.ID)
	mr.Set("budget:"+org.ID+":default", "5000")

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"choices":[],"usage":{"prompt_tokens":10,"completion_tokens":20,"total_tokens":30}}`)
	}))
	t.Cleanup(upstream.Close)

	cfg := config.Config{
		UpstreamURL:           upstream.URL,
		UpstreamHost:          "api.openai.com",
		EnforcementMode:       config.EnforcementEnforce,
		PreCheckTimeout:       50 * time.Millisecond,
		DefaultReservationEst: 4096,
		PromptTokenBuffer:     0,
	}
	metrics := &budget.Metrics{}
	checker := budget.NewRedisBudgetChecker(client, metrics)
	handler, err := proxy.NewHandlerWithRegistry(
		cfg, proxy.NewTransport(cfg),
		proxy.NewEnforcement(cfg, proxy.NewBudgetCheckerBridge(checker), nil),
		client, client, usage.NewOpenAIExtractor(), nil,
		metrics, nil, store.NewMemoryUsageStore(), orgs, nil,
	)
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	llm := proxy.NewAuthMiddleware(orgs, handler)
	ts := httptest.NewServer(llm)
	t.Cleanup(ts.Close)

	body := []byte(`{"model":"gpt-4o","max_tokens":100,"messages":[{"role":"user","content":"hi"}]}`)
	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/v1/chat/completions", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-TokenGuard-Key", raw)
	// intentionally no X-Budget-Bucket-Id
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	_, _ = io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}

	balStr, err := mr.Get("budget:" + org.ID + ":default")
	if err != nil {
		t.Fatalf("get balance: %v", err)
	}
	bal, _ := strconv.ParseInt(balStr, 10, 64)
	if bal != 4970 { // 5000 - 30 actual
		t.Fatalf("balance = %d, want 4970", bal)
	}
}
