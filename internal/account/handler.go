package account

import (
	"context"
	"embed"
	"encoding/json"
	"html/template"
	"net/http"
	"strconv"
	"strings"

	"github.com/saksham/token-guard-ai/internal/budget"
	"github.com/saksham/token-guard-ai/internal/proxy"
	"github.com/saksham/token-guard-ai/internal/store"
)

//go:embed templates/*.html
var templateFS embed.FS

const defaultUsageLimit = 50
const maxUsageLimit = 200

type BucketSource interface {
	ListBuckets(ctx context.Context) ([]budget.BucketBalance, error)
}

type UsageSource interface {
	ListUsageByOrg(ctx context.Context, orgID string, limit int) ([]store.UsageEvent, error)
}

type OrgSource interface {
	LookupAPIKey(ctx context.Context, rawKey string) (store.AuthResult, error)
	GetOrg(ctx context.Context, orgID string) (store.Org, error)
	UpdateOrgSlackWebhook(ctx context.Context, orgID, webhookURL string) (store.Org, error)
}

type Handler struct {
	orgs    OrgSource
	buckets BucketSource
	usage   UsageSource
	tmpl    *template.Template
}

func NewHandler(orgs OrgSource, buckets BucketSource, usage UsageSource) *Handler {
	tmpl := template.Must(template.ParseFS(templateFS, "templates/*.html"))
	return &Handler{orgs: orgs, buckets: buckets, usage: usage, tmpl: tmpl}
}

// Register mounts customer /me and /account routes (not proxied upstream).
func (h *Handler) Register(register func(pattern string, handler http.Handler)) {
	auth := func(next http.Handler) http.Handler {
		return proxy.NewAuthMiddleware(h.orgs, next)
	}
	register("GET /me/buckets", auth(http.HandlerFunc(h.handleMeBuckets)))
	register("GET /me/usage", auth(http.HandlerFunc(h.handleMeUsage)))
	register("GET /me/org", auth(http.HandlerFunc(h.handleMeOrg)))
	register("PATCH /me/slack", auth(http.HandlerFunc(h.handleMeSlack)))
	register("GET /account", http.HandlerFunc(h.handleAccountGet))
	register("POST /account/view", http.HandlerFunc(h.handleAccountView))
	register("POST /account/slack", http.HandlerFunc(h.handleAccountSlack))
}

func (h *Handler) handleMeBuckets(w http.ResponseWriter, r *http.Request) {
	orgID := proxy.OrgIDFromContext(r.Context())
	buckets, err := h.orgBuckets(r.Context(), orgID)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "failed to list buckets")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"buckets": buckets})
}

func (h *Handler) handleMeUsage(w http.ResponseWriter, r *http.Request) {
	orgID := proxy.OrgIDFromContext(r.Context())
	limit := parseLimit(r.URL.Query().Get("limit"), defaultUsageLimit)
	events, err := h.usage.ListUsageByOrg(r.Context(), orgID, limit)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "failed to list usage")
		return
	}
	if events == nil {
		events = []store.UsageEvent{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"events": events})
}

func (h *Handler) handleMeOrg(w http.ResponseWriter, r *http.Request) {
	orgID := proxy.OrgIDFromContext(r.Context())
	org, err := h.orgs.GetOrg(r.Context(), orgID)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "failed to load org")
		return
	}
	writeJSON(w, http.StatusOK, orgResponse(org))
}

func (h *Handler) handleMeSlack(w http.ResponseWriter, r *http.Request) {
	orgID := proxy.OrgIDFromContext(r.Context())
	var body struct {
		SlackWebhookURL string `json:"slack_webhook_url"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	org, err := h.orgs.UpdateOrgSlackWebhook(r.Context(), orgID, strings.TrimSpace(body.SlackWebhookURL))
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "failed to update slack webhook")
		return
	}
	writeJSON(w, http.StatusOK, orgResponse(org))
}

type accountPageData struct {
	KeySubmitted string
	Org          *accountOrgView
	Buckets      []budget.BucketBalance
	Events       []store.UsageEvent
	Error        string
	SlackOK      bool
}

type accountOrgView struct {
	OrgID            string
	Plan             string
	DefaultBucketID  string
	SlackWebhookSet  bool
	SlackWebhookMask string
}

func (h *Handler) handleAccountGet(w http.ResponseWriter, r *http.Request) {
	h.renderAccount(w, accountPageData{})
}

func (h *Handler) handleAccountView(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		h.renderAccount(w, accountPageData{Error: "invalid form"})
		return
	}
	rawKey := strings.TrimSpace(r.FormValue("tg_key"))
	data := h.loadAccountView(r.Context(), rawKey, "")
	h.renderAccount(w, data)
}

func (h *Handler) handleAccountSlack(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		h.renderAccount(w, accountPageData{Error: "invalid form"})
		return
	}
	rawKey := strings.TrimSpace(r.FormValue("tg_key"))
	webhook := strings.TrimSpace(r.FormValue("slack_webhook_url"))
	auth, err := h.orgs.LookupAPIKey(r.Context(), rawKey)
	if err != nil || rawKey == "" {
		h.renderAccount(w, accountPageData{Error: "invalid or missing TokenGuard key", KeySubmitted: rawKey})
		return
	}
	if _, err := h.orgs.UpdateOrgSlackWebhook(r.Context(), auth.OrgID, webhook); err != nil {
		data := h.loadAccountView(r.Context(), rawKey, "failed to update Slack webhook")
		h.renderAccount(w, data)
		return
	}
	data := h.loadAccountView(r.Context(), rawKey, "")
	data.SlackOK = true
	h.renderAccount(w, data)
}

func (h *Handler) loadAccountView(ctx context.Context, rawKey, loadErr string) accountPageData {
	data := accountPageData{KeySubmitted: rawKey, Error: loadErr}
	if rawKey == "" {
		data.Error = "paste your TokenGuard key to view balances"
		return data
	}
	auth, err := h.orgs.LookupAPIKey(ctx, rawKey)
	if err != nil {
		data.Error = "invalid TokenGuard key"
		return data
	}
	org, err := h.orgs.GetOrg(ctx, auth.OrgID)
	if err != nil {
		data.Error = "failed to load org"
		return data
	}
	view := accountOrgView{
		OrgID:            org.ID,
		Plan:             org.Plan,
		DefaultBucketID:  org.DefaultBucketID,
		SlackWebhookSet:  org.SlackWebhookURL != "",
		SlackWebhookMask: maskWebhook(org.SlackWebhookURL),
	}
	if view.DefaultBucketID == "" {
		view.DefaultBucketID = store.DefaultBucketName
	}
	data.Org = &view

	buckets, err := h.orgBuckets(ctx, org.ID)
	if err != nil {
		data.Error = "failed to load buckets"
	} else {
		data.Buckets = buckets
	}
	events, err := h.usage.ListUsageByOrg(ctx, org.ID, defaultUsageLimit)
	if err != nil {
		if data.Error == "" {
			data.Error = "failed to load usage"
		}
	} else {
		data.Events = events
	}
	if data.Buckets == nil {
		data.Buckets = []budget.BucketBalance{}
	}
	if data.Events == nil {
		data.Events = []store.UsageEvent{}
	}
	return data
}

func (h *Handler) renderAccount(w http.ResponseWriter, data accountPageData) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	status := http.StatusOK
	if data.Error != "" && data.Org == nil && data.KeySubmitted != "" {
		status = http.StatusUnauthorized
	}
	w.WriteHeader(status)
	_ = h.tmpl.ExecuteTemplate(w, "account.html", data)
}

func (h *Handler) orgBuckets(ctx context.Context, orgID string) ([]budget.BucketBalance, error) {
	all, err := h.buckets.ListBuckets(ctx)
	if err != nil {
		return nil, err
	}
	var out []budget.BucketBalance
	for _, b := range all {
		if b.OrgID == orgID {
			out = append(out, b)
		}
	}
	if out == nil {
		out = []budget.BucketBalance{}
	}
	return out, nil
}

func orgResponse(org store.Org) map[string]any {
	defaultBucket := org.DefaultBucketID
	if defaultBucket == "" {
		defaultBucket = store.DefaultBucketName
	}
	return map[string]any{
		"org_id":                   org.ID,
		"plan":                     org.Plan,
		"default_bucket_id":        defaultBucket,
		"slack_webhook_url_set":    org.SlackWebhookURL != "",
		"slack_webhook_url_masked": maskWebhook(org.SlackWebhookURL),
	}
}

func maskWebhook(url string) string {
	url = strings.TrimSpace(url)
	if url == "" {
		return ""
	}
	if len(url) <= 40 {
		return url[:min(12, len(url))] + "…"
	}
	return url[:36] + "…"
}

func parseLimit(raw string, fallback int) int {
	if raw == "" {
		return fallback
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n <= 0 {
		return fallback
	}
	if n > maxUsageLimit {
		return maxUsageLimit
	}
	return n
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeJSONError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]any{
		"error": map[string]string{"message": msg, "type": http.StatusText(status)},
	})
}
