package ops

import (
	"context"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/saksham/token-guard-ai/internal/budget"
	"github.com/saksham/token-guard-ai/internal/store"
)

type stubData struct {
	buckets []budget.BucketBalance
	holds   []budget.ReservationHold
	events  []store.UsageEvent
}

func (s *stubData) ListBuckets(context.Context) ([]budget.BucketBalance, error) {
	return s.buckets, nil
}
func (s *stubData) ListReservations(context.Context) ([]budget.ReservationHold, error) {
	return s.holds, nil
}
func (s *stubData) ListUsage(context.Context, string, int) ([]store.UsageEvent, error) {
	return s.events, nil
}

func TestOpsUnauthorized(t *testing.T) {
	h := NewHandler("secret", &stubData{}, &stubData{})
	req := httptest.NewRequest(http.MethodGet, "/ops", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
	if !strings.Contains(rec.Header().Get("WWW-Authenticate"), "Basic") {
		t.Fatalf("WWW-Authenticate = %q", rec.Header().Get("WWW-Authenticate"))
	}
}

func TestOpsWrongPassword(t *testing.T) {
	h := NewHandler("secret", &stubData{}, &stubData{})
	req := httptest.NewRequest(http.MethodGet, "/ops", nil)
	req.SetBasicAuth("admin", "wrong")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
}

func TestOpsAuthorizedRendersSections(t *testing.T) {
	data := &stubData{
		buckets: []budget.BucketBalance{{OrgID: "org_a", BucketID: "b1", Balance: 1000}},
		holds: []budget.ReservationHold{{
			RequestID: "req-1", OrgID: "org_a", BucketID: "b1", Reserved: 50, CreatedAt: time.Unix(1700000000, 0).UTC(),
		}},
		events: []store.UsageEvent{{
			OrgID: "org_a", BucketID: "b1", RequestID: "req-0", Reserved: 100, Actual: 80,
			Outcome: "settled", Provider: "api.openai.com", CreatedAt: time.Unix(1700000001, 0).UTC(),
		}},
	}
	h := NewHandler("secret", data, data)
	req := httptest.NewRequest(http.MethodGet, "/ops", nil)
	req.Header.Set("Authorization", "Basic "+base64.StdEncoding.EncodeToString([]byte("admin:secret")))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	for _, want := range []string{
		"TokenGuard Ops",
		"Buckets",
		"Usage",
		"Reservations",
		"org_a",
		"b1",
		"1000",
		"req-1",
		"settled",
		"req-0",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("missing %q in body:\n%s", want, body)
		}
	}
	if ct := rec.Header().Get("Content-Type"); !strings.Contains(ct, "text/html") {
		t.Fatalf("Content-Type = %q", ct)
	}
}

func TestOpsDisabledWithoutAPIKey(t *testing.T) {
	h := NewHandler("", &stubData{}, &stubData{})
	req := httptest.NewRequest(http.MethodGet, "/ops", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d", rec.Code)
	}
}
