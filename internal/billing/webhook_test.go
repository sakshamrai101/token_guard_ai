package billing

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
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

func TestServiceStartCheckout(t *testing.T) {
	orgs := store.NewMemoryOrgStore()
	org, _ := orgs.CreateOrg(context.Background(), "Acme")
	api := &mockStripe{url: "https://checkout.stripe.com/c/pay/cs_test"}
	svc := NewService(Config{
		PriceIndie:  "price_indie",
		PriceTeam:   "price_team",
		SuccessURL:  "http://localhost/ok",
		CancelURL:   "http://localhost/cancel",
		WebhookSecret: "whsec_x",
	}, api, orgs)

	url, err := svc.StartCheckout(context.Background(), org.ID, "indie")
	if err != nil {
		t.Fatalf("StartCheckout: %v", err)
	}
	if url != api.url {
		t.Fatalf("url = %q", url)
	}
	if api.got.OrgID != org.ID || api.got.Plan != PlanIndie || api.got.PriceID != "price_indie" {
		t.Fatalf("params = %+v", api.got)
	}
}

func TestServiceStartCheckoutUnknownOrg(t *testing.T) {
	svc := NewService(Config{PriceIndie: "p", SuccessURL: "s", CancelURL: "c"}, &mockStripe{url: "u"}, store.NewMemoryOrgStore())
	_, err := svc.StartCheckout(context.Background(), "missing", "indie")
	if !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("err = %v", err)
	}
}

func TestServiceStartCheckoutBadPlan(t *testing.T) {
	orgs := store.NewMemoryOrgStore()
	org, _ := orgs.CreateOrg(context.Background(), "Acme")
	svc := NewService(Config{PriceIndie: "p", SuccessURL: "s", CancelURL: "c"}, &mockStripe{url: "u"}, orgs)
	_, err := svc.StartCheckout(context.Background(), org.ID, "trial")
	if err == nil {
		t.Fatal("expected error")
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
    "customer":"cus_123",
    "subscription":"sub_123",
    "metadata":{"org_id":%q,"plan":"indie"}
  }}
}`, org.ID))
	ts := time.Now().Unix()
	sig := signHMAC(secret, ts, payload)
	header := fmt.Sprintf("t=%d,v1=%s", ts, sig)

	h := NewWebhookHandler(svc)
	req := httptest.NewRequest(http.MethodPost, "/billing/webhook", strings.NewReader(string(payload)))
	req.Header.Set("Stripe-Signature", header)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	got, err := orgs.GetOrg(context.Background(), org.ID)
	if err != nil {
		t.Fatalf("GetOrg: %v", err)
	}
	if got.Plan != "indie" || got.StripeCustomerID != "cus_123" || got.StripeSubscriptionID != "sub_123" {
		t.Fatalf("org = %+v", got)
	}
}

func TestWebhookInvalidSignature(t *testing.T) {
	orgs := store.NewMemoryOrgStore()
	svc := NewService(Config{WebhookSecret: "whsec_test"}, nil, orgs)
	h := NewWebhookHandler(svc)

	req := httptest.NewRequest(http.MethodPost, "/billing/webhook", strings.NewReader(`{}`))
	req.Header.Set("Stripe-Signature", "t=1,v1=nope")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

func TestWebhookSubscriptionDeletedDowngrades(t *testing.T) {
	orgs := store.NewMemoryOrgStore()
	org, _ := orgs.CreateOrg(context.Background(), "Acme")
	_ = orgs.ApplyCheckoutCompleted(context.Background(), org.ID, "team", "cus_1", "sub_abc")
	secret := "whsec_test"
	svc := NewService(Config{WebhookSecret: secret}, nil, orgs)

	payload := []byte(`{
  "id":"evt_2",
  "type":"customer.subscription.deleted",
  "data":{"object":{"id":"sub_abc"}}
}`)
	ts := time.Now().Unix()
	header := fmt.Sprintf("t=%d,v1=%s", ts, signHMAC(secret, ts, payload))

	h := NewWebhookHandler(svc)
	req := httptest.NewRequest(http.MethodPost, "/billing/webhook", io.NopCloser(strings.NewReader(string(payload))))
	req.Header.Set("Stripe-Signature", header)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	got, _ := orgs.GetOrg(context.Background(), org.ID)
	if got.Plan != "trial" {
		t.Fatalf("plan = %q, want trial", got.Plan)
	}
	if got.StripeSubscriptionID != "" {
		t.Fatalf("subscription id should be cleared, got %q", got.StripeSubscriptionID)
	}
}

func TestHandleEventJSONRoundTrip(t *testing.T) {
	var ev Event
	if err := json.Unmarshal([]byte(`{"type":"ping"}`), &ev); err != nil {
		t.Fatal(err)
	}
	if ev.Type != "ping" {
		t.Fatalf("type = %q", ev.Type)
	}
}

func signHMAC(secret string, ts int64, payload []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write([]byte(strconv.FormatInt(ts, 10)))
	_, _ = mac.Write([]byte("."))
	_, _ = mac.Write(payload)
	return hex.EncodeToString(mac.Sum(nil))
}
