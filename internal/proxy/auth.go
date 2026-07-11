package proxy

import (
	"context"
	"encoding/json"
	"net/http"

	"github.com/saksham/token-guard-ai/internal/store"
)

const (
	orgIDContextKey      ctxKey = "org_id"
	orgPlanContextKey    ctxKey = "org_plan"
	orgWebhookContextKey ctxKey = "org_webhook"
)

// KeyLookup resolves a TokenGuard API key to an org.
type KeyLookup interface {
	LookupAPIKey(ctx context.Context, rawKey string) (store.AuthResult, error)
}

// AuthMiddleware requires X-TokenGuard-Key on LLM proxy requests.
type AuthMiddleware struct {
	keys KeyLookup
	next http.Handler
}

func NewAuthMiddleware(keys KeyLookup, next http.Handler) *AuthMiddleware {
	return &AuthMiddleware{keys: keys, next: next}
}

func (m *AuthMiddleware) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	raw := r.Header.Get("X-TokenGuard-Key")
	if raw == "" {
		writeAuthError(w, http.StatusUnauthorized, "missing X-TokenGuard-Key")
		return
	}
	auth, err := m.keys.LookupAPIKey(r.Context(), raw)
	if err != nil {
		writeAuthError(w, http.StatusUnauthorized, "invalid X-TokenGuard-Key")
		return
	}
	ctx := context.WithValue(r.Context(), orgIDContextKey, auth.OrgID)
	ctx = context.WithValue(ctx, orgPlanContextKey, auth.Plan)
	ctx = context.WithValue(ctx, orgWebhookContextKey, auth.SlackWebhookURL)
	m.next.ServeHTTP(w, r.WithContext(ctx))
}

func OrgIDFromContext(ctx context.Context) string {
	if v, ok := ctx.Value(orgIDContextKey).(string); ok && v != "" {
		return v
	}
	return store.DefaultOrgID
}

func OrgWebhookFromContext(ctx context.Context) string {
	if v, ok := ctx.Value(orgWebhookContextKey).(string); ok {
		return v
	}
	return ""
}

func writeAuthError(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"error": map[string]string{
			"message": msg,
			"type":    "unauthorized",
		},
	})
}
