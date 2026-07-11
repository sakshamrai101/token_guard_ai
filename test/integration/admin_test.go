package integration

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/saksham/token-guard-ai/internal/admin"
	"github.com/saksham/token-guard-ai/internal/budget"
	"github.com/saksham/token-guard-ai/internal/config"
	"github.com/saksham/token-guard-ai/internal/proxy"
)

func newAdminTestServer(t *testing.T, bucketBalance int64) (*httptest.Server, *miniredis.Miniredis) {
	t.Helper()

	mr := miniredis.RunT(t)
	if bucketBalance > 0 {
		mr.Set("budget:default:ops-bucket", strconv.FormatInt(bucketBalance, 10))
	}

	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	client, err := budget.NewClientFromRedis(rdb, 5*time.Minute)
	if err != nil {
		t.Fatalf("NewClientFromRedis: %v", err)
	}

	cfg := config.Config{
		UpstreamURL:     "http://upstream.invalid",
		UpstreamHost:    "api.openai.com",
		EnforcementMode: config.EnforcementOff,
		AdminAPIKey:     "test-admin-key",
	}

	proxyHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTeapot)
	})

	adminHandler := admin.NewHandler(admin.NewRedisStore(client), nil, cfg.AdminAPIKey)
	server := proxy.NewServer(cfg, proxyHandler, adminHandler, budget.NewReadiness(client), nil)
	ts := httptest.NewServer(server)

	t.Cleanup(func() {
		ts.Close()
		client.Close()
		mr.Close()
	})

	return ts, mr
}

func TestAdminAPIGetSetTopup(t *testing.T) {
	ts, mr := newAdminTestServer(t, 1000)
	auth := "Bearer test-admin-key"

	getReq, _ := http.NewRequest(http.MethodGet, ts.URL+"/admin/v1/buckets/ops-bucket", nil)
	getReq.Header.Set("Authorization", auth)
	getResp, err := http.DefaultClient.Do(getReq)
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer getResp.Body.Close()
	if getResp.StatusCode != http.StatusOK {
		t.Fatalf("GET status = %d, want 200", getResp.StatusCode)
	}
	var got struct {
		BucketID string `json:"bucket_id"`
		Balance  int64  `json:"balance"`
	}
	if err := json.NewDecoder(getResp.Body).Decode(&got); err != nil {
		t.Fatalf("decode GET: %v", err)
	}
	if got.Balance != 1000 {
		t.Fatalf("GET balance = %d, want 1000", got.Balance)
	}

	putReq, _ := http.NewRequest(http.MethodPut, ts.URL+"/admin/v1/buckets/ops-bucket", bytes.NewReader([]byte(`{"balance":5000}`)))
	putReq.Header.Set("Authorization", auth)
	putReq.Header.Set("Content-Type", "application/json")
	putResp, err := http.DefaultClient.Do(putReq)
	if err != nil {
		t.Fatalf("PUT: %v", err)
	}
	putResp.Body.Close()
	if putResp.StatusCode != http.StatusOK {
		t.Fatalf("PUT status = %d, want 200", putResp.StatusCode)
	}

	topupReq, _ := http.NewRequest(http.MethodPost, ts.URL+"/admin/v1/buckets/ops-bucket/topup", bytes.NewReader([]byte(`{"amount":500}`)))
	topupReq.Header.Set("Authorization", auth)
	topupReq.Header.Set("Content-Type", "application/json")
	topupResp, err := http.DefaultClient.Do(topupReq)
	if err != nil {
		t.Fatalf("POST topup: %v", err)
	}
	defer topupResp.Body.Close()
	if topupResp.StatusCode != http.StatusOK {
		t.Fatalf("POST topup status = %d, want 200", topupResp.StatusCode)
	}
	var topupGot struct {
		Balance int64 `json:"balance"`
	}
	_ = json.NewDecoder(topupResp.Body).Decode(&topupGot)
	if topupGot.Balance != 5500 {
		t.Fatalf("topup balance = %d, want 5500", topupGot.Balance)
	}

	balStr, err := mr.Get("budget:default:ops-bucket")
	if err != nil {
		t.Fatalf("redis get: %v", err)
	}
	bal, _ := strconv.ParseInt(balStr, 10, 64)
	if bal != 5500 {
		t.Fatalf("redis balance = %d, want 5500", bal)
	}
}

func TestAdminAPIUnauthorized(t *testing.T) {
	ts, _ := newAdminTestServer(t, 1000)

	req, _ := http.NewRequest(http.MethodGet, ts.URL+"/admin/v1/buckets/ops-bucket", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", resp.StatusCode)
	}
}

func TestAdminAPIDoesNotProxyToUpstream(t *testing.T) {
	ts, _ := newAdminTestServer(t, 0)

	req, _ := http.NewRequest(http.MethodGet, ts.URL+"/admin/v1/buckets/new-bucket", nil)
	req.Header.Set("Authorization", "Bearer test-admin-key")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode == http.StatusTeapot {
		t.Fatalf("admin request was proxied upstream")
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d body = %s, want 200", resp.StatusCode, body)
	}
}
