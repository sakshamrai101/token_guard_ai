package admin

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/saksham/token-guard-ai/internal/billing"
	"github.com/saksham/token-guard-ai/internal/budget"
	"github.com/saksham/token-guard-ai/internal/store"
)

type stubStore struct {
	balances     map[string]int64 // key = org:bucket
	buckets      []budget.BucketBalance
	reservations []budget.ReservationHold
}

func balKey(orgID, bucketID string) string {
	if orgID == "" {
		orgID = store.DefaultOrgID
	}
	return orgID + ":" + bucketID
}

func (s *stubStore) GetBalance(_ context.Context, orgID, bucketID string) (int64, error) {
	if s.balances == nil {
		return 0, nil
	}
	return s.balances[balKey(orgID, bucketID)], nil
}

func (s *stubStore) SetBalance(_ context.Context, orgID, bucketID string, balance int64) (int64, error) {
	if s.balances == nil {
		s.balances = make(map[string]int64)
	}
	s.balances[balKey(orgID, bucketID)] = balance
	return balance, nil
}

func (s *stubStore) Topup(_ context.Context, orgID, bucketID string, amount int64) (int64, error) {
	if s.balances == nil {
		s.balances = make(map[string]int64)
	}
	k := balKey(orgID, bucketID)
	s.balances[k] += amount
	return s.balances[k], nil
}

func (s *stubStore) ListBuckets(_ context.Context) ([]budget.BucketBalance, error) {
	return s.buckets, nil
}

func (s *stubStore) ListReservations(_ context.Context) ([]budget.ReservationHold, error) {
	return s.reservations, nil
}

func doAdmin(t *testing.T, h *Handler, method, path, apiKey, body string) *httptest.ResponseRecorder {
	t.Helper()
	var reader *strings.Reader
	if body != "" {
		reader = strings.NewReader(body)
	} else {
		reader = strings.NewReader("")
	}
	req := httptest.NewRequest(method, path, reader)
	if apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+apiKey)
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestAdminGetBucketUnauthorizedWithoutKey(t *testing.T) {
	h := NewHandler(&stubStore{balances: map[string]int64{"default:b1": 100}}, nil, "")
	rec := doAdmin(t, h, http.MethodGet, "/admin/v1/buckets/b1", "secret", "")
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
}

func TestAdminGetBucketUnauthorizedBadToken(t *testing.T) {
	h := NewHandler(&stubStore{balances: map[string]int64{"default:b1": 100}}, nil, "secret")
	rec := doAdmin(t, h, http.MethodGet, "/admin/v1/buckets/b1", "wrong", "")
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
}

func TestAdminGetBucket(t *testing.T) {
	h := NewHandler(&stubStore{balances: map[string]int64{"default:b1": 1000}}, nil, "secret")
	rec := doAdmin(t, h, http.MethodGet, "/admin/v1/buckets/b1", "secret", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var resp bucketResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.BucketID != "b1" || resp.Balance != 1000 || resp.OrgID != "default" {
		t.Fatalf("resp = %+v, want default/b1/1000", resp)
	}
}

func TestAdminPutBucket(t *testing.T) {
	st := &stubStore{}
	h := NewHandler(st, nil, "secret")
	rec := doAdmin(t, h, http.MethodPut, "/admin/v1/buckets/b1", "secret", `{"balance":5000}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var resp bucketResponse
	_ = json.NewDecoder(rec.Body).Decode(&resp)
	if resp.Balance != 5000 {
		t.Fatalf("balance = %d, want 5000", resp.Balance)
	}
	if st.balances["default:b1"] != 5000 {
		t.Fatalf("store balance = %d, want 5000", st.balances["default:b1"])
	}
}

func TestAdminPutBucketRejectsNegative(t *testing.T) {
	h := NewHandler(&stubStore{}, nil, "secret")
	rec := doAdmin(t, h, http.MethodPut, "/admin/v1/buckets/b1", "secret", `{"balance":-1}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

func TestAdminTopupBucket(t *testing.T) {
	st := &stubStore{balances: map[string]int64{"default:b1": 1000}}
	h := NewHandler(st, nil, "secret")
	rec := doAdmin(t, h, http.MethodPost, "/admin/v1/buckets/b1/topup", "secret", `{"amount":250}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var resp bucketResponse
	_ = json.NewDecoder(rec.Body).Decode(&resp)
	if resp.Balance != 1250 {
		t.Fatalf("balance = %d, want 1250", resp.Balance)
	}
}

func TestAdminTopupRejectsNonPositiveAmount(t *testing.T) {
	h := NewHandler(&stubStore{balances: map[string]int64{"default:b1": 1000}}, nil, "secret")
	rec := doAdmin(t, h, http.MethodPost, "/admin/v1/buckets/b1/topup", "secret", `{"amount":0}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

func TestAdminRateLimit(t *testing.T) {
	h := NewHandler(&stubStore{balances: map[string]int64{"default:b1": 1}}, nil, "secret")
	h.limit = newRateLimiter(2, time.Minute)
	for i := 0; i < 2; i++ {
		rec := doAdmin(t, h, http.MethodGet, "/admin/v1/buckets/b1", "secret", "")
		if rec.Code != http.StatusOK {
			t.Fatalf("request %d status = %d, want 200", i, rec.Code)
		}
	}
	rec := doAdmin(t, h, http.MethodGet, "/admin/v1/buckets/b1", "secret", "")
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want 429", rec.Code)
	}
}

func TestAdminListBuckets(t *testing.T) {
	h := NewHandler(&stubStore{
		buckets: []budget.BucketBalance{{OrgID: "default", BucketID: "b1", Balance: 100}},
	}, nil, "secret")
	rec := doAdmin(t, h, http.MethodGet, "/admin/v1/buckets", "secret", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var resp struct {
		Buckets []budget.BucketBalance `json:"buckets"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(resp.Buckets) != 1 || resp.Buckets[0].BucketID != "b1" {
		t.Fatalf("buckets = %+v", resp.Buckets)
	}
}

func TestAdminListUsage(t *testing.T) {
	usageStore := store.NewMemoryUsageStore()
	_ = usageStore.InsertUsage(context.Background(), store.UsageEvent{
		BucketID:  "b1",
		RequestID: "req-1",
		Reserved:  100,
		Actual:    80,
		Outcome:   "settled",
		Provider:  "api.openai.com",
	})
	h := NewHandler(&stubStore{}, usageStore, "secret")
	rec := doAdmin(t, h, http.MethodGet, "/admin/v1/usage?bucket_id=b1&limit=10", "secret", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 body=%s", rec.Code, rec.Body.String())
	}
	var resp struct {
		Events []store.UsageEvent `json:"events"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(resp.Events) != 1 || resp.Events[0].RequestID != "req-1" {
		t.Fatalf("events = %+v", resp.Events)
	}
}

func TestAdminListReservations(t *testing.T) {
	h := NewHandler(&stubStore{
		reservations: []budget.ReservationHold{{RequestID: "r1", OrgID: "default", BucketID: "b1", Reserved: 50}},
	}, nil, "secret")
	rec := doAdmin(t, h, http.MethodGet, "/admin/v1/reservations", "secret", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var resp struct {
		Reservations []budget.ReservationHold `json:"reservations"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(resp.Reservations) != 1 || resp.Reservations[0].RequestID != "r1" {
		t.Fatalf("reservations = %+v", resp.Reservations)
	}
}

func TestAdminCreateOrgAndKey(t *testing.T) {
	orgs := store.NewMemoryOrgStore()
	h := NewHandlerWithOrgs(&stubStore{}, nil, orgs, "secret")

	rec := doAdmin(t, h, http.MethodPost, "/admin/v1/orgs", "secret", `{"name":"Acme"}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create org status = %d body=%s", rec.Code, rec.Body.String())
	}
	var org store.Org
	if err := json.NewDecoder(rec.Body).Decode(&org); err != nil {
		t.Fatalf("decode org: %v", err)
	}
	if org.ID == "" || org.Name != "Acme" {
		t.Fatalf("org = %+v", org)
	}

	rec = doAdmin(t, h, http.MethodPost, "/admin/v1/orgs/"+org.ID+"/keys", "secret", "")
	if rec.Code != http.StatusCreated {
		t.Fatalf("create key status = %d body=%s", rec.Code, rec.Body.String())
	}
	var keyResp createKeyResponse
	if err := json.NewDecoder(rec.Body).Decode(&keyResp); err != nil {
		t.Fatalf("decode key: %v", err)
	}
	if !strings.HasPrefix(keyResp.Key, "tg_") {
		t.Fatalf("key = %q", keyResp.Key)
	}
	if keyResp.APIKey.KeyHash != "" {
		t.Fatal("key hash must not be returned")
	}

	auth, err := orgs.LookupAPIKey(context.Background(), keyResp.Key)
	if err != nil || auth.OrgID != org.ID {
		t.Fatalf("lookup = %+v err=%v", auth, err)
	}
}

func TestAdminPatchOrgSlackWebhook(t *testing.T) {
	orgs := store.NewMemoryOrgStore()
	h := NewHandlerWithOrgs(&stubStore{}, nil, orgs, "secret")

	rec := doAdmin(t, h, http.MethodPost, "/admin/v1/orgs", "secret", `{"name":"Acme"}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create org status = %d", rec.Code)
	}
	var org store.Org
	_ = json.NewDecoder(rec.Body).Decode(&org)

	rec = doAdmin(t, h, http.MethodPatch, "/admin/v1/orgs/"+org.ID, "secret",
		`{"slack_webhook_url":"https://hooks.slack.com/services/T/B/X"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("patch status = %d body=%s", rec.Code, rec.Body.String())
	}
	var updated store.Org
	_ = json.NewDecoder(rec.Body).Decode(&updated)
	if updated.SlackWebhookURL != "https://hooks.slack.com/services/T/B/X" {
		t.Fatalf("webhook = %q", updated.SlackWebhookURL)
	}

	got, err := orgs.GetOrg(context.Background(), org.ID)
	if err != nil || got.SlackWebhookURL != updated.SlackWebhookURL {
		t.Fatalf("store org = %+v err=%v", got, err)
	}
}

type mockCheckout struct {
	url    string
	err    error
	orgID  string
	plan   string
}

func (m *mockCheckout) StartCheckout(_ context.Context, orgID, plan string) (string, error) {
	m.orgID = orgID
	m.plan = plan
	return m.url, m.err
}

func TestAdminCheckoutReturnsURL(t *testing.T) {
	orgs := store.NewMemoryOrgStore()
	org, _ := orgs.CreateOrg(context.Background(), "Acme")
	checkout := &mockCheckout{url: "https://checkout.stripe.com/c/pay/cs_test"}
	h := NewHandlerWithBilling(&stubStore{}, nil, orgs, checkout, "secret")

	rec := doAdmin(t, h, http.MethodPost, "/admin/v1/orgs/"+org.ID+"/checkout", "secret", `{"plan":"indie"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	var resp map[string]string
	_ = json.NewDecoder(rec.Body).Decode(&resp)
	if resp["url"] != checkout.url {
		t.Fatalf("resp = %+v", resp)
	}
	if checkout.orgID != org.ID || checkout.plan != "indie" {
		t.Fatalf("checkout args = %s/%s", checkout.orgID, checkout.plan)
	}
}

func TestAdminCheckoutUnknownOrg(t *testing.T) {
	h := NewHandlerWithBilling(&stubStore{}, nil, store.NewMemoryOrgStore(), &mockCheckout{err: store.ErrNotFound}, "secret")
	rec := doAdmin(t, h, http.MethodPost, "/admin/v1/orgs/missing/checkout", "secret", `{"plan":"indie"}`)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d", rec.Code)
	}
}

func TestAdminCheckoutBadPlan(t *testing.T) {
	orgs := store.NewMemoryOrgStore()
	org, _ := orgs.CreateOrg(context.Background(), "Acme")
	h := NewHandlerWithBilling(&stubStore{}, nil, orgs, &mockCheckout{err: billing.ErrInvalidPlan}, "secret")
	rec := doAdmin(t, h, http.MethodPost, "/admin/v1/orgs/"+org.ID+"/checkout", "secret", `{"plan":"trial"}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d", rec.Code)
	}
}
