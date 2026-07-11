package proxy

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/saksham/token-guard-ai/internal/store"
)

func TestAuthMiddlewareMissingKey(t *testing.T) {
	called := false
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	})
	mw := NewAuthMiddleware(store.NewMemoryOrgStore(), next)

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	rec := httptest.NewRecorder()
	mw.ServeHTTP(rec, req)

	if called {
		t.Fatal("next handler should not run without key")
	}
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
}

func TestAuthMiddlewareInvalidKey(t *testing.T) {
	called := false
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
	})
	mw := NewAuthMiddleware(store.NewMemoryOrgStore(), next)

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	req.Header.Set("X-TokenGuard-Key", "tg_invalid")
	rec := httptest.NewRecorder()
	mw.ServeHTTP(rec, req)

	if called {
		t.Fatal("next should not run")
	}
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
	var body map[string]any
	_ = json.NewDecoder(rec.Body).Decode(&body)
	if body["error"] == nil {
		t.Fatalf("body = %v", body)
	}
}

func TestAuthMiddlewareValidKeySetsOrgContext(t *testing.T) {
	orgs := store.NewMemoryOrgStore()
	org, err := orgs.CreateOrg(context.Background(), "Acme")
	if err != nil {
		t.Fatalf("CreateOrg: %v", err)
	}
	_, err = orgs.UpdateOrgSlackWebhook(context.Background(), org.ID, "https://hooks.slack.com/org")
	if err != nil {
		t.Fatalf("UpdateOrgSlackWebhook: %v", err)
	}
	raw, _, err := orgs.CreateAPIKey(context.Background(), org.ID)
	if err != nil {
		t.Fatalf("CreateAPIKey: %v", err)
	}

	var gotOrg, gotWebhook string
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotOrg = OrgIDFromContext(r.Context())
		gotWebhook = OrgWebhookFromContext(r.Context())
		w.WriteHeader(http.StatusOK)
	})
	mw := NewAuthMiddleware(orgs, next)

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{}`))
	req.Header.Set("X-TokenGuard-Key", raw)
	rec := httptest.NewRecorder()
	mw.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	if gotOrg != org.ID {
		t.Fatalf("org = %q, want %q", gotOrg, org.ID)
	}
	if gotWebhook != "https://hooks.slack.com/org" {
		t.Fatalf("webhook = %q", gotWebhook)
	}
}
