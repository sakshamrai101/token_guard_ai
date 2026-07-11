package admin

import (
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/saksham/token-guard-ai/internal/budget"
	"github.com/saksham/token-guard-ai/internal/store"
)

const defaultRateLimit = 60

type bucketResponse struct {
	OrgID    string `json:"org_id"`
	BucketID string `json:"bucket_id"`
	Balance  int64  `json:"balance"`
}

type setBalanceRequest struct {
	Balance int64 `json:"balance"`
}

type topupRequest struct {
	Amount int64 `json:"amount"`
}

type createOrgRequest struct {
	Name string `json:"name"`
}

type patchOrgRequest struct {
	SlackWebhookURL *string `json:"slack_webhook_url"`
}

type createKeyResponse struct {
	Key     string       `json:"key"`
	APIKey  store.APIKey `json:"api_key"`
	Warning string       `json:"warning"`
}

type Handler struct {
	store  Store
	usage  UsageQuerier
	orgs   store.OrgStore
	apiKey string
	mux    *http.ServeMux
	limit  *rateLimiter
}

func NewHandler(budgetStore Store, usage UsageQuerier, apiKey string) *Handler {
	return NewHandlerWithOrgs(budgetStore, usage, nil, apiKey)
}

func NewHandlerWithOrgs(budgetStore Store, usage UsageQuerier, orgs store.OrgStore, apiKey string) *Handler {
	h := &Handler{
		store:  budgetStore,
		usage:  usage,
		orgs:   orgs,
		apiKey: apiKey,
		mux:    http.NewServeMux(),
		limit:  newRateLimiter(defaultRateLimit, time.Minute),
	}
	h.mux.HandleFunc("GET /admin/v1/buckets/{id}", h.handleGet)
	h.mux.HandleFunc("PUT /admin/v1/buckets/{id}", h.handlePut)
	h.mux.HandleFunc("POST /admin/v1/buckets/{id}/topup", h.handleTopup)
	h.mux.HandleFunc("GET /admin/v1/buckets", h.handleListBuckets)
	h.mux.HandleFunc("GET /admin/v1/usage", h.handleListUsage)
	h.mux.HandleFunc("GET /admin/v1/reservations", h.handleListReservations)
	h.mux.HandleFunc("POST /admin/v1/orgs", h.handleCreateOrg)
	h.mux.HandleFunc("GET /admin/v1/orgs", h.handleListOrgs)
	h.mux.HandleFunc("PATCH /admin/v1/orgs/{id}", h.handlePatchOrg)
	h.mux.HandleFunc("POST /admin/v1/orgs/{id}/keys", h.handleCreateKey)
	return h
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if !strings.HasPrefix(r.URL.Path, "/admin/") {
		http.NotFound(w, r)
		return
	}
	if !h.authenticate(w, r) {
		return
	}
	if !h.limit.allow(clientIP(r)) {
		writeJSON(w, http.StatusTooManyRequests, map[string]string{"error": "rate limit exceeded"})
		return
	}
	h.mux.ServeHTTP(w, r)
}

func (h *Handler) authenticate(w http.ResponseWriter, r *http.Request) bool {
	if h.apiKey == "" {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
		return false
	}
	auth := r.Header.Get("Authorization")
	if !strings.HasPrefix(auth, "Bearer ") {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
		return false
	}
	token := strings.TrimSpace(strings.TrimPrefix(auth, "Bearer "))
	if token != h.apiKey {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
		return false
	}
	return true
}

func orgIDFromRequest(r *http.Request) string {
	if v := r.URL.Query().Get("org_id"); v != "" {
		return v
	}
	return store.DefaultOrgID
}

func (h *Handler) handleGet(w http.ResponseWriter, r *http.Request) {
	bucketID := r.PathValue("id")
	if bucketID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "missing bucket id"})
		return
	}
	orgID := orgIDFromRequest(r)
	balance, err := h.store.GetBalance(r.Context(), orgID, bucketID)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to get balance"})
		return
	}
	writeJSON(w, http.StatusOK, bucketResponse{OrgID: orgID, BucketID: bucketID, Balance: balance})
}

func (h *Handler) handlePut(w http.ResponseWriter, r *http.Request) {
	bucketID := r.PathValue("id")
	if bucketID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "missing bucket id"})
		return
	}
	body, err := io.ReadAll(r.Body)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid body"})
		return
	}
	var req setBalanceRequest
	if err := json.Unmarshal(body, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid json"})
		return
	}
	if req.Balance < 0 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "balance must be non-negative"})
		return
	}
	orgID := orgIDFromRequest(r)
	balance, err := h.store.SetBalance(r.Context(), orgID, bucketID, req.Balance)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to set balance"})
		return
	}
	writeJSON(w, http.StatusOK, bucketResponse{OrgID: orgID, BucketID: bucketID, Balance: balance})
}

func (h *Handler) handleTopup(w http.ResponseWriter, r *http.Request) {
	bucketID := r.PathValue("id")
	if bucketID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "missing bucket id"})
		return
	}
	body, err := io.ReadAll(r.Body)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid body"})
		return
	}
	var req topupRequest
	if err := json.Unmarshal(body, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid json"})
		return
	}
	if req.Amount <= 0 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "amount must be positive"})
		return
	}
	orgID := orgIDFromRequest(r)
	balance, err := h.store.Topup(r.Context(), orgID, bucketID, req.Amount)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to topup"})
		return
	}
	writeJSON(w, http.StatusOK, bucketResponse{OrgID: orgID, BucketID: bucketID, Balance: balance})
}

func (h *Handler) handleListBuckets(w http.ResponseWriter, r *http.Request) {
	buckets, err := h.store.ListBuckets(r.Context())
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to list buckets"})
		return
	}
	if buckets == nil {
		buckets = []budget.BucketBalance{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"buckets": buckets})
}

func (h *Handler) handleListUsage(w http.ResponseWriter, r *http.Request) {
	if h.usage == nil {
		writeJSON(w, http.StatusOK, map[string]any{"events": []any{}})
		return
	}
	bucketID := r.URL.Query().Get("bucket_id")
	limit := 50
	if raw := r.URL.Query().Get("limit"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n <= 0 {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid limit"})
			return
		}
		limit = n
	}
	events, err := h.usage.ListUsage(r.Context(), bucketID, limit)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to list usage"})
		return
	}
	if events == nil {
		events = []store.UsageEvent{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"events": events})
}

func (h *Handler) handleListReservations(w http.ResponseWriter, r *http.Request) {
	holds, err := h.store.ListReservations(r.Context())
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to list reservations"})
		return
	}
	if holds == nil {
		holds = []budget.ReservationHold{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"reservations": holds})
}

func (h *Handler) handleCreateOrg(w http.ResponseWriter, r *http.Request) {
	if h.orgs == nil {
		writeJSON(w, http.StatusNotImplemented, map[string]string{"error": "org store not configured"})
		return
	}
	body, err := io.ReadAll(r.Body)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid body"})
		return
	}
	var req createOrgRequest
	if err := json.Unmarshal(body, &req); err != nil || strings.TrimSpace(req.Name) == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "name is required"})
		return
	}
	org, err := h.orgs.CreateOrg(r.Context(), strings.TrimSpace(req.Name))
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to create org"})
		return
	}
	writeJSON(w, http.StatusCreated, org)
}

func (h *Handler) handleListOrgs(w http.ResponseWriter, r *http.Request) {
	if h.orgs == nil {
		writeJSON(w, http.StatusOK, map[string]any{"orgs": []any{}})
		return
	}
	orgs, err := h.orgs.ListOrgs(r.Context())
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to list orgs"})
		return
	}
	if orgs == nil {
		orgs = []store.Org{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"orgs": orgs})
}

func (h *Handler) handlePatchOrg(w http.ResponseWriter, r *http.Request) {
	if h.orgs == nil {
		writeJSON(w, http.StatusNotImplemented, map[string]string{"error": "org store not configured"})
		return
	}
	orgID := r.PathValue("id")
	if orgID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "missing org id"})
		return
	}
	body, err := io.ReadAll(r.Body)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid body"})
		return
	}
	var req patchOrgRequest
	if err := json.Unmarshal(body, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid json"})
		return
	}
	if req.SlackWebhookURL == nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "slack_webhook_url is required"})
		return
	}
	org, err := h.orgs.UpdateOrgSlackWebhook(r.Context(), orgID, strings.TrimSpace(*req.SlackWebhookURL))
	if err == store.ErrNotFound {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "org not found"})
		return
	}
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to update org"})
		return
	}
	writeJSON(w, http.StatusOK, org)
}

func (h *Handler) handleCreateKey(w http.ResponseWriter, r *http.Request) {
	if h.orgs == nil {
		writeJSON(w, http.StatusNotImplemented, map[string]string{"error": "org store not configured"})
		return
	}
	orgID := r.PathValue("id")
	if orgID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "missing org id"})
		return
	}
	raw, key, err := h.orgs.CreateAPIKey(r.Context(), orgID)
	if err == store.ErrNotFound {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "org not found"})
		return
	}
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to create key"})
		return
	}
	key.KeyHash = "" // never return hash
	writeJSON(w, http.StatusCreated, createKeyResponse{
		Key:     raw,
		APIKey:  key,
		Warning: "store this key now; it will not be shown again",
	})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func clientIP(r *http.Request) string {
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		if ip := strings.TrimSpace(strings.Split(xff, ",")[0]); ip != "" {
			return ip
		}
	}
	host, _, _ := strings.Cut(r.RemoteAddr, ":")
	if host != "" {
		return host
	}
	return r.RemoteAddr
}

type rateLimiter struct {
	mu     sync.Mutex
	limit  int
	window time.Duration
	hits   map[string][]time.Time
}

func newRateLimiter(limit int, window time.Duration) *rateLimiter {
	return &rateLimiter{
		limit:  limit,
		window: window,
		hits:   make(map[string][]time.Time),
	}
}

func (rl *rateLimiter) allow(key string) bool {
	now := time.Now()
	cutoff := now.Add(-rl.window)

	rl.mu.Lock()
	defer rl.mu.Unlock()

	times := rl.hits[key]
	i := 0
	for _, ts := range times {
		if ts.After(cutoff) {
			times[i] = ts
			i++
		}
	}
	times = times[:i]

	if len(times) >= rl.limit {
		rl.hits[key] = times
		return false
	}

	rl.hits[key] = append(times, now)
	return true
}
