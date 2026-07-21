package signup

import (
	"context"
	"embed"
	"encoding/json"
	"errors"
	"html/template"
	"io"
	"net/http"
	"strings"

	"github.com/saksham/token-guard-ai/internal/billing"
	"github.com/saksham/token-guard-ai/internal/store"
)

//go:embed templates/*.html
var templateFS embed.FS

type CheckoutStarter interface {
	StartPublicCheckout(ctx context.Context, email, plan string) (url string, err error)
}

type SetupSecrets interface {
	TakeSetupSecret(ctx context.Context, sessionID string) (string, error)
	SetupOrgID(ctx context.Context, sessionID string) (string, error)
}

type Handler struct {
	checkout   CheckoutStarter
	orgs       store.OrgStore
	setup      SetupSecrets
	publicBase string
	tmpl       *template.Template
}

func NewHandler(checkout CheckoutStarter, orgs store.OrgStore, setup SetupSecrets, publicBase string) *Handler {
	tmpl := template.Must(template.ParseFS(templateFS, "templates/*.html"))
	return &Handler{
		checkout:   checkout,
		orgs:       orgs,
		setup:      setup,
		publicBase: strings.TrimRight(publicBase, "/"),
		tmpl:       tmpl,
	}
}

func (h *Handler) Mount(mux *http.ServeMux) {
	mux.HandleFunc("GET /signup", h.handleSignupPage)
	mux.HandleFunc("POST /signup/checkout", h.handleCheckout)
	mux.HandleFunc("GET /setup", h.handleSetupPage)
	mux.HandleFunc("POST /setup/slack", h.handleSetupSlack)
}

// Register mounts public signup/setup routes on the proxy server mux.
func (h *Handler) Register(register func(pattern string, handler http.Handler)) {
	register("GET /signup", http.HandlerFunc(h.handleSignupPage))
	register("POST /signup/checkout", http.HandlerFunc(h.handleCheckout))
	register("GET /setup", http.HandlerFunc(h.handleSetupPage))
	register("POST /setup/slack", http.HandlerFunc(h.handleSetupSlack))
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	mux := http.NewServeMux()
	h.Mount(mux)
	mux.ServeHTTP(w, r)
}

func (h *Handler) handleSignupPage(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_ = h.tmpl.ExecuteTemplate(w, "signup.html", map[string]any{
		"Canceled": r.URL.Query().Get("canceled") == "1",
	})
}

type checkoutBody struct {
	Email string `json:"email"`
	Plan  string `json:"plan"`
}

func (h *Handler) handleCheckout(w http.ResponseWriter, r *http.Request) {
	if h.checkout == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "billing not configured"})
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<16))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid body"})
		return
	}
	var req checkoutBody
	if err := json.Unmarshal(body, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid json"})
		return
	}
	url, err := h.checkout.StartPublicCheckout(r.Context(), req.Email, req.Plan)
	if errors.Is(err, billing.ErrInvalidPlan) || errors.Is(err, billing.ErrMissingConfig) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"url": url})
}

type setupPageData struct {
	PublicBase string
	Key        string
	SessionID  string
	OrgID      string
	Revealed   bool
	Expired    bool
	SlackSaved bool
}

func (h *Handler) handleSetupPage(w http.ResponseWriter, r *http.Request) {
	sessionID := strings.TrimSpace(r.URL.Query().Get("session_id"))
	data := setupPageData{
		PublicBase: h.publicBase,
		SessionID:  sessionID,
		SlackSaved: r.URL.Query().Get("slack") == "1",
	}
	if sessionID == "" {
		data.Expired = true
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_ = h.tmpl.ExecuteTemplate(w, "setup.html", data)
		return
	}
	if h.setup != nil {
		key, err := h.setup.TakeSetupSecret(r.Context(), sessionID)
		if err != nil {
			http.Error(w, "setup error", http.StatusInternalServerError)
			return
		}
		if key == "" {
			data.Expired = true
		} else {
			data.Key = key
			data.Revealed = true
		}
		orgID, _ := h.setup.SetupOrgID(r.Context(), sessionID)
		data.OrgID = orgID
	} else {
		data.Expired = true
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_ = h.tmpl.ExecuteTemplate(w, "setup.html", data)
}

func (h *Handler) handleSetupSlack(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	sessionID := strings.TrimSpace(r.FormValue("session_id"))
	webhook := strings.TrimSpace(r.FormValue("slack_webhook_url"))
	if sessionID == "" || h.setup == nil || h.orgs == nil {
		http.Redirect(w, r, "/setup?session_id="+sessionID, http.StatusSeeOther)
		return
	}
	orgID, err := h.setup.SetupOrgID(r.Context(), sessionID)
	if err != nil || orgID == "" {
		http.Redirect(w, r, "/setup?session_id="+sessionID, http.StatusSeeOther)
		return
	}
	if _, err := h.orgs.UpdateOrgSlackWebhook(r.Context(), orgID, webhook); err != nil {
		http.Redirect(w, r, "/setup?session_id="+sessionID, http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, "/setup?session_id="+sessionID+"&slack=1", http.StatusSeeOther)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
