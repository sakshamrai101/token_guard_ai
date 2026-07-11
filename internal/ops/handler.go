package ops

import (
	"context"
	"embed"
	"html/template"
	"net/http"
	"time"

	"github.com/saksham/token-guard-ai/internal/budget"
	"github.com/saksham/token-guard-ai/internal/store"
)

//go:embed templates/ops.html
var templateFS embed.FS

const usageLimit = 50

type BucketSource interface {
	ListBuckets(ctx context.Context) ([]budget.BucketBalance, error)
	ListReservations(ctx context.Context) ([]budget.ReservationHold, error)
}

type UsageSource interface {
	ListUsage(ctx context.Context, bucketID string, limit int) ([]store.UsageEvent, error)
}

type Handler struct {
	apiKey  string
	buckets BucketSource
	usage   UsageSource
	tmpl    *template.Template
}

type pageData struct {
	Buckets      []budget.BucketBalance
	Reservations []budget.ReservationHold
	Events       []store.UsageEvent
	GeneratedAt  time.Time
	Error        string
}

func NewHandler(apiKey string, buckets BucketSource, usage UsageSource) *Handler {
	tmpl := template.Must(template.ParseFS(templateFS, "templates/ops.html"))
	return &Handler{
		apiKey:  apiKey,
		buckets: buckets,
		usage:   usage,
		tmpl:    tmpl,
	}
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !h.authorize(w, r) {
		return
	}

	data := pageData{GeneratedAt: time.Now().UTC()}
	if h.buckets != nil {
		if buckets, err := h.buckets.ListBuckets(r.Context()); err != nil {
			data.Error = "failed to load buckets"
		} else {
			data.Buckets = buckets
		}
		if holds, err := h.buckets.ListReservations(r.Context()); err != nil {
			if data.Error == "" {
				data.Error = "failed to load reservations"
			}
		} else {
			data.Reservations = holds
		}
	}
	if h.usage != nil {
		if events, err := h.usage.ListUsage(r.Context(), "", usageLimit); err != nil {
			if data.Error == "" {
				data.Error = "failed to load usage"
			}
		} else {
			data.Events = events
		}
	}
	if data.Buckets == nil {
		data.Buckets = []budget.BucketBalance{}
	}
	if data.Reservations == nil {
		data.Reservations = []budget.ReservationHold{}
	}
	if data.Events == nil {
		data.Events = []store.UsageEvent{}
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	_ = h.tmpl.Execute(w, data)
}

func (h *Handler) authorize(w http.ResponseWriter, r *http.Request) bool {
	if h.apiKey == "" {
		w.Header().Set("WWW-Authenticate", `Basic realm="TokenGuard Ops"`)
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return false
	}
	user, pass, ok := r.BasicAuth()
	if !ok || user != "admin" || pass != h.apiKey {
		w.Header().Set("WWW-Authenticate", `Basic realm="TokenGuard Ops"`)
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return false
	}
	return true
}
