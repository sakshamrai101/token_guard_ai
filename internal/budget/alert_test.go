package budget

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestAlerterFailOpenPostsOnce(t *testing.T) {
	var hits atomic.Int32
	var body string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		b, _ := io.ReadAll(r.Body)
		body = string(b)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	a := NewAlerter(srv.URL, nil)
	a.FailOpenAt(context.Background(), "", "org1", "req-1", "b1", "redis timeout")

	if hits.Load() != 1 {
		t.Fatalf("hits = %d, want 1", hits.Load())
	}
	if !strings.Contains(body, "fail_open") || !strings.Contains(body, "CRITICAL") ||
		!strings.Contains(body, "org1") || !strings.Contains(body, "b1") || !strings.Contains(body, "req-1") {
		t.Fatalf("body = %s", body)
	}
}

func TestAlerterBudgetExhaustedPostsOnce(t *testing.T) {
	var hits atomic.Int32
	var body string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		b, _ := io.ReadAll(r.Body)
		body = string(b)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	a := NewAlerter(srv.URL, nil)
	a.BudgetExhausted(context.Background(), "", "org1", "b1", "req-1", 1000)

	if hits.Load() != 1 {
		t.Fatalf("hits = %d, want 1", hits.Load())
	}
	if !strings.Contains(body, "budget_exhausted") || !strings.Contains(body, "org1") ||
		!strings.Contains(body, "b1") || !strings.Contains(body, "req-1") {
		t.Fatalf("body = %s", body)
	}
}

func TestAlerterBudgetWarning80PostsOnce(t *testing.T) {
	var hits atomic.Int32
	var body string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		b, _ := io.ReadAll(r.Body)
		body = string(b)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	a := NewAlerter(srv.URL, nil).WithDedupe(NewMemoryWarningDedupe(time.Hour))
	a.MaybeBudgetWarning80(context.Background(), "", "org1", "b1", 100, 900)

	if hits.Load() != 1 {
		t.Fatalf("hits = %d, want 1", hits.Load())
	}
	if !strings.Contains(body, "budget_warning_80") || !strings.Contains(body, "org1") || !strings.Contains(body, "b1") {
		t.Fatalf("body = %s", body)
	}
}

func TestAlerterWarningDedupeWithinTTL(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	a := NewAlerter(srv.URL, nil).WithDedupe(NewMemoryWarningDedupe(time.Hour))
	a.MaybeBudgetWarning80(context.Background(), "", "org1", "b1", 100, 900)
	a.MaybeBudgetWarning80(context.Background(), "", "org1", "b1", 50, 50)

	if hits.Load() != 1 {
		t.Fatalf("hits = %d, want 1 (deduped)", hits.Load())
	}
}

func TestAlerterOrgWebhookPreferredOverGlobal(t *testing.T) {
	var globalHits, orgHits atomic.Int32
	global := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		globalHits.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer global.Close()
	org := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		orgHits.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer org.Close()

	a := NewAlerter(global.URL, nil)
	a.BudgetExhausted(context.Background(), org.URL, "org1", "b1", "req-1", 10)
	a.FailOpenAt(context.Background(), org.URL, "org1", "req-2", "b1", "detail")

	if orgHits.Load() != 2 {
		t.Fatalf("org hits = %d, want 2", orgHits.Load())
	}
	if globalHits.Load() != 0 {
		t.Fatalf("global hits = %d, want 0", globalHits.Load())
	}
}

func TestAlerterFallsBackToGlobalWhenOrgWebhookEmpty(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	a := NewAlerter(srv.URL, nil)
	a.BudgetExhausted(context.Background(), "", "org1", "b1", "req-1", 10)
	if hits.Load() != 1 {
		t.Fatalf("hits = %d, want 1", hits.Load())
	}
}

func TestShouldWarn80(t *testing.T) {
	cases := []struct {
		remaining, actual int64
		want              bool
	}{
		{200, 800, true},
		{199, 801, true},
		{201, 799, false},
		{0, 100, true},
		{800, 200, false},
		{0, 0, true},
	}
	for _, tc := range cases {
		got := ShouldWarn80(tc.remaining, tc.actual)
		if got != tc.want {
			t.Fatalf("ShouldWarn80(%d,%d)=%v want %v", tc.remaining, tc.actual, got, tc.want)
		}
	}
}

func TestBudgetDeniedStillPosts(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		b, _ := io.ReadAll(r.Body)
		var payload map[string]string
		_ = json.Unmarshal(b, &payload)
		if !strings.Contains(payload["text"], "budget_exhausted") {
			t.Errorf("text = %s", payload["text"])
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	a := NewAlerter(srv.URL, nil)
	a.BudgetDenied(context.Background(), "req-1", "b1", 100)
	if hits.Load() != 1 {
		t.Fatalf("hits = %d", hits.Load())
	}
}

func TestWarningSkippedWhenAboveThreshold(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	a := NewAlerter(srv.URL, nil).WithDedupe(NewMemoryWarningDedupe(time.Hour))
	a.MaybeBudgetWarning80(context.Background(), "", "org1", "b1", 800, 200)
	if hits.Load() != 0 {
		t.Fatalf("hits = %d, want 0", hits.Load())
	}
}
