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
	BucketID string `json:"bucket_id"`
	Balance  int64  `json:"balance"`
}

type setBalanceRequest struct {
	Balance int64 `json:"balance"`
}

type topupRequest struct {
	Amount int64 `json:"amount"`
}

type Handler struct {
	store  Store
	usage  UsageQuerier
	apiKey string
	mux    *http.ServeMux
	limit  *rateLimiter
}

func NewHandler(store Store, usage UsageQuerier, apiKey string) *Handler {
	h := &Handler{
		store:  store,
		usage:  usage,
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

func (h *Handler) handleGet(w http.ResponseWriter, r *http.Request) {
	bucketID := r.PathValue("id")
	if bucketID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "missing bucket id"})
		return
	}

	balance, err := h.store.GetBalance(r.Context(), bucketID)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to get balance"})
		return
	}
	writeJSON(w, http.StatusOK, bucketResponse{BucketID: bucketID, Balance: balance})
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

	balance, err := h.store.SetBalance(r.Context(), bucketID, req.Balance)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to set balance"})
		return
	}
	writeJSON(w, http.StatusOK, bucketResponse{BucketID: bucketID, Balance: balance})
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

	balance, err := h.store.Topup(r.Context(), bucketID, req.Amount)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to topup"})
		return
	}
	writeJSON(w, http.StatusOK, bucketResponse{BucketID: bucketID, Balance: balance})
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
