package admin

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

type stubStore struct {
	balances map[string]int64
}

func (s *stubStore) GetBalance(_ context.Context, bucketID string) (int64, error) {
	if s.balances == nil {
		return 0, nil
	}
	return s.balances[bucketID], nil
}

func (s *stubStore) SetBalance(_ context.Context, bucketID string, balance int64) (int64, error) {
	if s.balances == nil {
		s.balances = make(map[string]int64)
	}
	s.balances[bucketID] = balance
	return balance, nil
}

func (s *stubStore) Topup(_ context.Context, bucketID string, amount int64) (int64, error) {
	if s.balances == nil {
		s.balances = make(map[string]int64)
	}
	s.balances[bucketID] += amount
	return s.balances[bucketID], nil
}

func doAdmin(t *testing.T, h *Handler, method, path, apiKey, body string) *httptest.ResponseRecorder {
	t.Helper()
	var reader *strings.Reader
	if body != "" {
		reader = strings.NewReader(body)
	} else {
		reader = strings.NewReader("")
	}
	req := httptest.NewRequest(method, path, reader)
	if apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+apiKey)
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestAdminGetBucketUnauthorizedWithoutKey(t *testing.T) {
	h := NewHandler(&stubStore{balances: map[string]int64{"b1": 100}}, "")
	rec := doAdmin(t, h, http.MethodGet, "/admin/v1/buckets/b1", "secret", "")
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
}

func TestAdminGetBucketUnauthorizedBadToken(t *testing.T) {
	h := NewHandler(&stubStore{balances: map[string]int64{"b1": 100}}, "secret")
	rec := doAdmin(t, h, http.MethodGet, "/admin/v1/buckets/b1", "wrong", "")
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
}

func TestAdminGetBucket(t *testing.T) {
	h := NewHandler(&stubStore{balances: map[string]int64{"b1": 1000}}, "secret")
	rec := doAdmin(t, h, http.MethodGet, "/admin/v1/buckets/b1", "secret", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var resp bucketResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.BucketID != "b1" || resp.Balance != 1000 {
		t.Fatalf("resp = %+v, want b1/1000", resp)
	}
}

func TestAdminPutBucket(t *testing.T) {
	store := &stubStore{}
	h := NewHandler(store, "secret")
	rec := doAdmin(t, h, http.MethodPut, "/admin/v1/buckets/b1", "secret", `{"balance":5000}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var resp bucketResponse
	_ = json.NewDecoder(rec.Body).Decode(&resp)
	if resp.Balance != 5000 {
		t.Fatalf("balance = %d, want 5000", resp.Balance)
	}
	if store.balances["b1"] != 5000 {
		t.Fatalf("store balance = %d, want 5000", store.balances["b1"])
	}
}

func TestAdminPutBucketRejectsNegative(t *testing.T) {
	h := NewHandler(&stubStore{}, "secret")
	rec := doAdmin(t, h, http.MethodPut, "/admin/v1/buckets/b1", "secret", `{"balance":-1}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

func TestAdminTopupBucket(t *testing.T) {
	store := &stubStore{balances: map[string]int64{"b1": 1000}}
	h := NewHandler(store, "secret")
	rec := doAdmin(t, h, http.MethodPost, "/admin/v1/buckets/b1/topup", "secret", `{"amount":250}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var resp bucketResponse
	_ = json.NewDecoder(rec.Body).Decode(&resp)
	if resp.Balance != 1250 {
		t.Fatalf("balance = %d, want 1250", resp.Balance)
	}
}

func TestAdminTopupRejectsNonPositiveAmount(t *testing.T) {
	h := NewHandler(&stubStore{balances: map[string]int64{"b1": 1000}}, "secret")
	rec := doAdmin(t, h, http.MethodPost, "/admin/v1/buckets/b1/topup", "secret", `{"amount":0}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

func TestAdminRateLimit(t *testing.T) {
	h := NewHandler(&stubStore{balances: map[string]int64{"b1": 1}}, "secret")
	h.limit = newRateLimiter(2, time.Minute)
	for i := 0; i < 2; i++ {
		rec := doAdmin(t, h, http.MethodGet, "/admin/v1/buckets/b1", "secret", "")
		if rec.Code != http.StatusOK {
			t.Fatalf("request %d status = %d, want 200", i, rec.Code)
		}
	}
	rec := doAdmin(t, h, http.MethodGet, "/admin/v1/buckets/b1", "secret", "")
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want 429", rec.Code)
	}
}
