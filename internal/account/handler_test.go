package account

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/saksham/token-guard-ai/internal/budget"
	"github.com/saksham/token-guard-ai/internal/store"
)

func TestMeBucketsRequiresKey(t *testing.T) {
	h, _, _, _ := newAccountHarness(t)
	mux := http.NewServeMux()
	h.Register(mux.Handle)

	req := httptest.NewRequest(http.MethodGet, "/me/buckets", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
}

func TestMeBucketsEmptyOrg(t *testing.T) {
	h, orgs, _, _ := newAccountHarness(t)
	mux := http.NewServeMux()
	h.Register(mux.Handle)

	org, _ := orgs.CreateOrg(context.Background(), "Empty")
	key, _, _ := orgs.CreateAPIKey(context.Background(), org.ID)

	req := httptest.NewRequest(http.MethodGet, "/me/buckets", nil)
	req.Header.Set("X-TokenGuard-Key", key)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	var body struct {
		Buckets []budget.BucketBalance `json:"buckets"`
	}
	_ = json.NewDecoder(rec.Body).Decode(&body)
	if body.Buckets == nil || len(body.Buckets) != 0 {
		t.Fatalf("buckets = %+v, want empty list", body.Buckets)
	}
}

func TestMeBucketsOnlyOwnOrg(t *testing.T) {
	h, orgs, mr, _ := newAccountHarness(t)
	mux := http.NewServeMux()
	h.Register(mux.Handle)

	orgA, _ := orgs.CreateOrg(context.Background(), "A")
	orgB, _ := orgs.CreateOrg(context.Background(), "B")
	keyA, _, _ := orgs.CreateAPIKey(context.Background(), orgA.ID)
	mr.Set("budget:"+orgA.ID+":default", "1000")
	mr.Set("budget:"+orgB.ID+":secret", "99999")

	req := httptest.NewRequest(http.MethodGet, "/me/buckets", nil)
	req.Header.Set("X-TokenGuard-Key", keyA)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	var body struct {
		Buckets []budget.BucketBalance `json:"buckets"`
	}
	_ = json.NewDecoder(rec.Body).Decode(&body)
	if len(body.Buckets) != 1 || body.Buckets[0].BucketID != "default" || body.Buckets[0].Balance != 1000 {
		t.Fatalf("buckets = %+v", body.Buckets)
	}
	for _, b := range body.Buckets {
		if b.OrgID != orgA.ID {
			t.Fatalf("leaked org %q", b.OrgID)
		}
	}
}

func TestMeUsageScopedNewestFirstAndLimit(t *testing.T) {
	h, orgs, _, usageStore := newAccountHarness(t)
	mux := http.NewServeMux()
	h.Register(mux.Handle)

	orgA, _ := orgs.CreateOrg(context.Background(), "A")
	orgB, _ := orgs.CreateOrg(context.Background(), "B")
	keyA, _, _ := orgs.CreateAPIKey(context.Background(), orgA.ID)

	ctx := context.Background()
	_ = usageStore.InsertUsage(ctx, store.UsageEvent{OrgID: orgA.ID, BucketID: "b", RequestID: "old", Outcome: "settled", CreatedAt: time.Now().UTC().Add(-time.Minute)})
	_ = usageStore.InsertUsage(ctx, store.UsageEvent{OrgID: orgB.ID, BucketID: "b", RequestID: "other", Outcome: "settled"})
	_ = usageStore.InsertUsage(ctx, store.UsageEvent{OrgID: orgA.ID, BucketID: "b", RequestID: "new", Outcome: "settled"})

	req := httptest.NewRequest(http.MethodGet, "/me/usage?limit=1", nil)
	req.Header.Set("X-TokenGuard-Key", keyA)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	var body struct {
		Events []store.UsageEvent `json:"events"`
	}
	_ = json.NewDecoder(rec.Body).Decode(&body)
	if len(body.Events) != 1 || body.Events[0].RequestID != "new" {
		t.Fatalf("events = %+v", body.Events)
	}

	req2 := httptest.NewRequest(http.MethodGet, "/me/usage?limit=50", nil)
	req2.Header.Set("X-TokenGuard-Key", keyA)
	rec2 := httptest.NewRecorder()
	mux.ServeHTTP(rec2, req2)
	_ = json.NewDecoder(rec2.Body).Decode(&body)
	if len(body.Events) != 2 {
		t.Fatalf("len = %d, want 2", len(body.Events))
	}
	for _, e := range body.Events {
		if e.OrgID != orgA.ID {
			t.Fatalf("leaked usage org %q", e.OrgID)
		}
	}
}

func TestMeOrgMasksSlack(t *testing.T) {
	h, orgs, _, _ := newAccountHarness(t)
	mux := http.NewServeMux()
	h.Register(mux.Handle)

	org, _ := orgs.CreateOrg(context.Background(), "Acme")
	_, _ = orgs.UpdateOrgSlackWebhook(context.Background(), org.ID, "https://hooks.slack.com/services/T00/B00/SECRETTOKEN")
	_ = orgs.SetDefaultBucket(context.Background(), org.ID, "default")
	key, _, _ := orgs.CreateAPIKey(context.Background(), org.ID)

	req := httptest.NewRequest(http.MethodGet, "/me/org", nil)
	req.Header.Set("X-TokenGuard-Key", key)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	raw := rec.Body.String()
	if strings.Contains(raw, "SECRETTOKEN") {
		t.Fatalf("full webhook leaked: %s", raw)
	}
	var body map[string]any
	_ = json.Unmarshal([]byte(raw), &body)
	if body["org_id"] != org.ID || body["slack_webhook_url_set"] != true {
		t.Fatalf("body = %+v", body)
	}
}

func TestMeSlackUpdatesWebhook(t *testing.T) {
	h, orgs, _, _ := newAccountHarness(t)
	mux := http.NewServeMux()
	h.Register(mux.Handle)

	org, _ := orgs.CreateOrg(context.Background(), "Acme")
	key, _, _ := orgs.CreateAPIKey(context.Background(), org.ID)

	body := bytes.NewBufferString(`{"slack_webhook_url":"https://hooks.slack.com/services/T/B/x"}`)
	req := httptest.NewRequest(http.MethodPatch, "/me/slack", body)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-TokenGuard-Key", key)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	got, _ := orgs.GetOrg(context.Background(), org.ID)
	if got.SlackWebhookURL != "https://hooks.slack.com/services/T/B/x" {
		t.Fatalf("webhook = %q", got.SlackWebhookURL)
	}
}

func TestAccountViewRejectsInvalidKey(t *testing.T) {
	h, _, _, _ := newAccountHarness(t)
	mux := http.NewServeMux()
	h.Register(mux.Handle)

	form := url.Values{"tg_key": {"tg_bogus"}}
	req := httptest.NewRequest(http.MethodPost, "/account/view", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "invalid") {
		t.Fatalf("body = %s", rec.Body.String())
	}
}

func TestAccountViewRendersBalances(t *testing.T) {
	h, orgs, mr, usageStore := newAccountHarness(t)
	mux := http.NewServeMux()
	h.Register(mux.Handle)

	org, _ := orgs.CreateOrg(context.Background(), "Acme")
	_ = orgs.SetDefaultBucket(context.Background(), org.ID, "default")
	key, _, _ := orgs.CreateAPIKey(context.Background(), org.ID)
	mr.Set("budget:"+org.ID+":default", "42000")
	_ = usageStore.InsertUsage(context.Background(), store.UsageEvent{
		OrgID: org.ID, BucketID: "default", RequestID: "req-1", Actual: 10, Outcome: "settled",
	})

	form := url.Values{"tg_key": {key}}
	req := httptest.NewRequest(http.MethodPost, "/account/view", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	html := rec.Body.String()
	if !strings.Contains(html, "42000") || !strings.Contains(html, "req-1") {
		t.Fatalf("missing data in html: %s", html)
	}
	if !strings.Contains(html, org.ID) {
		t.Fatalf("missing org id")
	}
}

func newAccountHarness(t *testing.T) (*Handler, *store.MemoryOrgStore, *miniredis.Miniredis, *store.MemoryUsageStore) {
	t.Helper()
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	client, err := budget.NewClientFromRedis(rdb, 5*time.Minute)
	if err != nil {
		t.Fatalf("redis client: %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })

	orgs := store.NewMemoryOrgStore()
	usageStore := store.NewMemoryUsageStore()
	h := NewHandler(orgs, client, usageStore)
	return h, orgs, mr, usageStore
}
