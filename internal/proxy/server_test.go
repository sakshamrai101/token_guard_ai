package proxy

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/saksham/token-guard-ai/internal/admin"
	"github.com/saksham/token-guard-ai/internal/budget"
	"github.com/saksham/token-guard-ai/internal/config"
)

func TestAdminRoutesNotProxied(t *testing.T) {
	var proxied bool
	proxyHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		proxied = true
		w.WriteHeader(http.StatusOK)
	})

	adminHandler := admin.NewHandler(adminStubStore{}, nil, "secret")
	cfg := config.Config{}
	server := NewServer(cfg, proxyHandler, adminHandler, nil, nil)
	ts := httptest.NewServer(server)
	t.Cleanup(ts.Close)

	req, _ := http.NewRequest(http.MethodGet, ts.URL+"/admin/v1/buckets/b1", nil)
	req.Header.Set("Authorization", "Bearer secret")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	resp.Body.Close()

	if proxied {
		t.Fatal("admin request was handled by proxy catch-all")
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
}

func TestOpsRouteNotProxied(t *testing.T) {
	var proxied bool
	proxyHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		proxied = true
		w.WriteHeader(http.StatusOK)
	})
	cfg := config.Config{}
	server := NewServer(cfg, proxyHandler, nil, nil, nil)

	ops := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("<h1>TokenGuard Ops</h1><h2>Buckets</h2>"))
	})
	server.Handle("GET /ops", ops)

	ts := httptest.NewServer(server)
	t.Cleanup(ts.Close)

	resp, err := http.Get(ts.URL + "/ops")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	defer resp.Body.Close()
	if proxied {
		t.Fatal("/ops was handled by proxy catch-all")
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
}

type adminStubStore struct{}

func (adminStubStore) GetBalance(_ context.Context, _, _ string) (int64, error) {
	return 42, nil
}

func (adminStubStore) SetBalance(_ context.Context, _, _ string, balance int64) (int64, error) {
	return balance, nil
}

func (adminStubStore) Topup(_ context.Context, _, _ string, amount int64) (int64, error) {
	return amount, nil
}

func (adminStubStore) ListBuckets(_ context.Context) ([]budget.BucketBalance, error) {
	return nil, nil
}

func (adminStubStore) ListReservations(_ context.Context) ([]budget.ReservationHold, error) {
	return nil, nil
}
