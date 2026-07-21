package signup

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/saksham/token-guard-ai/internal/billing"
	"github.com/saksham/token-guard-ai/internal/store"
)

type mockCheckout struct {
	url string
	err error
}

func (m *mockCheckout) StartPublicCheckout(_ context.Context, email, plan string) (string, error) {
	if m.err != nil {
		return "", m.err
	}
	return m.url, nil
}

type memSetup struct {
	mu   sync.Mutex
	keys map[string]string
	orgs map[string]string
}

func newMemSetup() *memSetup {
	return &memSetup{keys: map[string]string{}, orgs: map[string]string{}}
}

func (m *memSetup) Put(sessionID, orgID, key string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.keys[sessionID] = key
	m.orgs[sessionID] = orgID
}

func (m *memSetup) TakeSetupSecret(_ context.Context, sessionID string) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	v := m.keys[sessionID]
	delete(m.keys, sessionID)
	return v, nil
}

func (m *memSetup) SetupOrgID(_ context.Context, sessionID string) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.orgs[sessionID], nil
}

func TestSignupCheckoutReturnsURL(t *testing.T) {
	h := NewHandler(&mockCheckout{url: "https://checkout.stripe.com/pay/cs"}, store.NewMemoryOrgStore(), nil, "http://localhost:8080")
	req := httptest.NewRequest(http.MethodPost, "/signup/checkout", strings.NewReader(`{"email":"a@b.com","plan":"indie"}`))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	var resp map[string]string
	_ = json.NewDecoder(rec.Body).Decode(&resp)
	if resp["url"] != "https://checkout.stripe.com/pay/cs" {
		t.Fatalf("resp = %+v", resp)
	}
}

func TestSignupCheckoutBadPlan(t *testing.T) {
	h := NewHandler(&mockCheckout{err: billing.ErrInvalidPlan}, store.NewMemoryOrgStore(), nil, "http://localhost:8080")
	req := httptest.NewRequest(http.MethodPost, "/signup/checkout", strings.NewReader(`{"email":"a@b.com","plan":"nope"}`))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d", rec.Code)
	}
}

func TestSetupRevealsOnce(t *testing.T) {
	orgs := store.NewMemoryOrgStore()
	org, _ := orgs.CreateOrgWithEmail(context.Background(), "a@b.com", "a@b.com")
	setup := newMemSetup()
	setup.Put("cs_1", org.ID, "tg_secretrawkey000")
	h := NewHandler(nil, orgs, setup, "http://localhost:8080")

	req := httptest.NewRequest(http.MethodGet, "/setup?session_id=cs_1", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "tg_secretrawkey000") {
		t.Fatalf("missing key in body: %s", body)
	}

	req2 := httptest.NewRequest(http.MethodGet, "/setup?session_id=cs_1", nil)
	rec2 := httptest.NewRecorder()
	h.ServeHTTP(rec2, req2)
	if strings.Contains(rec2.Body.String(), "tg_secretrawkey000") {
		t.Fatal("key should not appear on second load")
	}
	if !strings.Contains(rec2.Body.String(), "already revealed") {
		t.Fatalf("expected expired message: %s", rec2.Body.String())
	}
}

func TestSetupSlackSavesWebhook(t *testing.T) {
	orgs := store.NewMemoryOrgStore()
	org, _ := orgs.CreateOrgWithEmail(context.Background(), "a@b.com", "a@b.com")
	setup := newMemSetup()
	setup.Put("cs_2", org.ID, "tg_x")
	// consume key so page is expired path, but org mapping remains
	_, _ = setup.TakeSetupSecret(context.Background(), "cs_2")

	h := NewHandler(nil, orgs, setup, "http://localhost:8080")
	form := strings.NewReader("session_id=cs_2&slack_webhook_url=https://hooks.slack.com/services/T/B/X")
	req := httptest.NewRequest(http.MethodPost, "/setup/slack", form)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("status = %d", rec.Code)
	}
	got, _ := orgs.GetOrg(context.Background(), org.ID)
	if got.SlackWebhookURL != "https://hooks.slack.com/services/T/B/X" {
		t.Fatalf("webhook = %q", got.SlackWebhookURL)
	}
}

func TestSignupPageOK(t *testing.T) {
	h := NewHandler(nil, nil, nil, "http://localhost:8080")
	req := httptest.NewRequest(http.MethodGet, "/signup", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "TokenGuard") {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
}
