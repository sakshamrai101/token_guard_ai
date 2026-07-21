package billing

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/saksham/token-guard-ai/internal/store"
)

type mockStripe struct {
	url string
	err error
	got CreateCheckoutParams
}

func (m *mockStripe) CreateCheckoutSession(_ context.Context, p CreateCheckoutParams) (CheckoutSession, error) {
	m.got = p
	if m.err != nil {
		return CheckoutSession{}, m.err
	}
	return CheckoutSession{URL: m.url, ID: "cs_test"}, nil
}

type memSetup struct {
	mu   sync.Mutex
	keys map[string]string
	orgs map[string]string
}

func newMemSetup() *memSetup {
	return &memSetup{keys: map[string]string{}, orgs: map[string]string{}}
}

func (m *memSetup) PutSetupSecret(_ context.Context, sessionID, orgID, rawKey string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.keys[sessionID] = rawKey
	m.orgs[sessionID] = orgID
	return nil
}

type memBudget struct {
	mu   sync.Mutex
	bals map[string]int64
}

func newMemBudget() *memBudget {
	return &memBudget{bals: map[string]int64{}}
}

func (m *memBudget) SetBalance(_ context.Context, orgID, bucketID string, balance int64) (int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.bals[orgID+":"+bucketID] = balance
	return balance, nil
}

func signHMAC(secret string, ts int64, payload []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write([]byte(strconv.FormatInt(ts, 10)))
	_, _ = mac.Write([]byte("."))
	_, _ = mac.Write(payload)
	return hex.EncodeToString(mac.Sum(nil))
}

func TestServiceStartCheckout(t *testing.T) {
	orgs := store.NewMemoryOrgStore()
	org, _ := orgs.CreateOrg(context.Background(), "Acme")
	api := &mockStripe{url: "https://checkout.stripe.com/c/pay/cs_test"}
	svc := NewService(Config{
		PriceIndie:    "price_indie",
		PriceTeam:     "price_team",
		SuccessURL:    "http://localhost/ok",
		CancelURL:     "http://localhost/cancel",
		WebhookSecret: "whsec_x",
	}, api, orgs)

	url, err := svc.StartCheckout(context.Background(), org.ID, "indie")
	if err != nil {
		t.Fatalf("StartCheckout: %v", err)
	}
	if url != api.url {
		t.Fatalf("url = %q", url)
	}
	if api.got.OrgID != org.ID || api.got.Plan != PlanIndie {
		t.Fatalf("params = %+v", api.got)
	}
}

func TestServiceStartPublicCheckout(t *testing.T) {
	api := &mockStripe{url: "https://checkout.stripe.com/c/pay/cs_pub"}
	svc := NewService(Config{
		PriceIndie: "price_indie",
		PriceTeam:  "price_team",
		SuccessURL: "http://localhost/setup?session_id={CHECKOUT_SESSION_ID}",
		CancelURL:  "http://localhost/signup",
	}, api, store.NewMemoryOrgStore())

	url, err := svc.StartPublicCheckout(context.Background(), "a@ex.com", "trial")
	if err != nil {
		t.Fatalf("StartPublicCheckout: %v", err)
	}
	if url != api.url || api.got.Email != "a@ex.com" || api.got.Plan != PlanTrial {
		t.Fatalf("url=%q params=%+v", url, api.got)
	}
}

func TestServiceStartCheckoutUnknownOrg(t *testing.T) {
	svc := NewService(Config{PriceIndie: "p", SuccessURL: "s", CancelURL: "c"}, &mockStripe{url: "u"}, store.NewMemoryOrgStore())
	_, err := svc.StartCheckout(context.Background(), "missing", "indie")
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestServiceStartCheckoutBadPlan(t *testing.T) {
	orgs := store.NewMemoryOrgStore()
	org, _ := orgs.CreateOrg(context.Background(), "Acme")
	svc := NewService(Config{PriceIndie: "p", SuccessURL: "s", CancelURL: "c"}, &mockStripe{url: "u"}, orgs)
	_, err := svc.StartCheckout(context.Background(), org.ID, "trial")
	if err == nil {
		t.Fatal("expected error for admin trial")
	}
}

func TestWebhookCheckoutCompletedUpdatesPlan(t *testing.T) {
	orgs := store.NewMemoryOrgStore()
	org, _ := orgs.CreateOrg(context.Background(), "Acme")
	secret := "whsec_test"
	svc := NewService(Config{WebhookSecret: secret}, nil, orgs)

	payload := []byte(fmt.Sprintf(`{
  "id":"evt_1",
  "type":"checkout.session.completed",
  "data":{"object":{
    "id":"cs_admin",
    "customer":"cus_123",
    "subscription":"sub_123",
    "metadata":{"org_id":%q,"plan":"indie"}
  }}
}`, org.ID))
	ts := time.Now().Unix()
	req := httptest.NewRequest(http.MethodPost, "/billing/webhook", strings.NewReader(string(payload)))
	req.Header.Set("Stripe-Signature", fmt.Sprintf("t=%d,v1=%s", ts, signHMAC(secret, ts, payload)))
	rec := httptest.NewRecorder()
	NewWebhookHandler(svc).ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	got, _ := orgs.GetOrg(context.Background(), org.ID)
	if got.Plan != "indie" {
		t.Fatalf("org = %+v", got)
	}
}

func TestWebhookSignupProvisionsOrgKeyAndSeed(t *testing.T) {
	orgs := store.NewMemoryOrgStore()
	setup := newMemSetup()
	bals := newMemBudget()
	secret := "whsec_test"
	svc := NewService(Config{WebhookSecret: secret}, nil, orgs).WithProvisioner(
		NewProvisioner(orgs, bals, setup, 200000),
	)

	payload := []byte(`{
  "id":"evt_signup",
  "type":"checkout.session.completed",
  "data":{"object":{
    "id":"cs_signup_1",
    "customer":"cus_x",
    "subscription":"sub_x",
    "metadata":{"email":"user@example.com","plan":"trial"},
    "customer_details":{"email":"user@example.com"}
  }}
}`)
	ts := time.Now().Unix()
	req := httptest.NewRequest(http.MethodPost, "/billing/webhook", strings.NewReader(string(payload)))
	req.Header.Set("Stripe-Signature", fmt.Sprintf("t=%d,v1=%s", ts, signHMAC(secret, ts, payload)))
	rec := httptest.NewRecorder()
	NewWebhookHandler(svc).ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}

	org, err := orgs.FindOrgByEmail(context.Background(), "user@example.com")
	if err != nil {
		t.Fatalf("FindOrgByEmail: %v", err)
	}
	if org.DefaultBucketID != "default" || org.Plan != "trial" {
		t.Fatalf("org = %+v", org)
	}
	if bals.bals[org.ID+":default"] != 200000 {
		t.Fatalf("balance = %v", bals.bals)
	}
	if setup.orgs["cs_signup_1"] != org.ID {
		t.Fatalf("setup org = %q", setup.orgs["cs_signup_1"])
	}
	if k := setup.keys["cs_signup_1"]; k == "" || !strings.HasPrefix(k, "tg_") {
		t.Fatalf("setup key = %q", k)
	}
}

func TestWebhookInvalidSignature(t *testing.T) {
	svc := NewService(Config{WebhookSecret: "whsec_test"}, nil, store.NewMemoryOrgStore())
	req := httptest.NewRequest(http.MethodPost, "/billing/webhook", strings.NewReader(`{}`))
	req.Header.Set("Stripe-Signature", "t=1,v1=nope")
	rec := httptest.NewRecorder()
	NewWebhookHandler(svc).ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d", rec.Code)
	}
}

func TestWebhookSubscriptionDeletedDowngrades(t *testing.T) {
	orgs := store.NewMemoryOrgStore()
	org, _ := orgs.CreateOrg(context.Background(), "Acme")
	_ = orgs.ApplyCheckoutCompleted(context.Background(), org.ID, "team", "cus_1", "sub_abc")
	secret := "whsec_test"
	svc := NewService(Config{WebhookSecret: secret}, nil, orgs)

	payload := []byte(`{"id":"evt_2","type":"customer.subscription.deleted","data":{"object":{"id":"sub_abc"}}}`)
	ts := time.Now().Unix()
	req := httptest.NewRequest(http.MethodPost, "/billing/webhook", strings.NewReader(string(payload)))
	req.Header.Set("Stripe-Signature", fmt.Sprintf("t=%d,v1=%s", ts, signHMAC(secret, ts, payload)))
	rec := httptest.NewRecorder()
	NewWebhookHandler(svc).ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	got, _ := orgs.GetOrg(context.Background(), org.ID)
	if got.Plan != "trial" {
		t.Fatalf("plan = %q", got.Plan)
	}
}
