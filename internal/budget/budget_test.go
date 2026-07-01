package budget

import (
	"context"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/saksham/token-guard-ai/internal/config"
)

func setupTestClient(t *testing.T, balance int64) (*miniredis.Miniredis, *Client) {
	t.Helper()
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	client, err := NewClientFromRedis(rdb, 5*time.Minute)
	if err != nil {
		t.Fatalf("NewClientFromRedis: %v", err)
	}
	if balance > 0 {
		mr.Set("budget:test-bucket", strconv.FormatInt(balance, 10))
	}
	return mr, client
}

func TestReserveBudgetAllowsAndDeducts(t *testing.T) {
	_, client := setupTestClient(t, 5000)
	checker := NewRedisBudgetChecker(client, nil)

	result, err := checker.Reserve(context.Background(), "test-bucket", "req-1", 1536)
	if err != nil {
		t.Fatalf("Reserve: %v", err)
	}
	if !result.Allowed || result.Reserved != 1536 {
		t.Fatalf("result = %+v, want allowed with reserved=1536", result)
	}

	bal, _ := client.rdb.Get(context.Background(), budgetKey("test-bucket")).Int64()
	if bal != 3464 {
		t.Fatalf("balance = %d, want 3464", bal)
	}
}

func TestReserveBudgetDeniesWhenInsufficient(t *testing.T) {
	_, client := setupTestClient(t, 100)
	checker := NewRedisBudgetChecker(client, nil)

	result, err := checker.Reserve(context.Background(), "test-bucket", "req-1", 500)
	if err != nil {
		t.Fatalf("Reserve: %v", err)
	}
	if result.Allowed {
		t.Fatal("expected deny when balance insufficient")
	}

	bal, _ := client.rdb.Get(context.Background(), budgetKey("test-bucket")).Int64()
	if bal != 100 {
		t.Fatalf("balance should be unchanged, got %d", bal)
	}
}

func TestReserveBudgetIdempotent(t *testing.T) {
	_, client := setupTestClient(t, 5000)
	checker := NewRedisBudgetChecker(client, nil)

	r1, err := checker.Reserve(context.Background(), "test-bucket", "req-dup", 1000)
	if err != nil {
		t.Fatalf("first Reserve: %v", err)
	}
	r2, err := checker.Reserve(context.Background(), "test-bucket", "req-dup", 1000)
	if err != nil {
		t.Fatalf("second Reserve: %v", err)
	}
	if !r1.Allowed || !r2.Allowed {
		t.Fatal("both reserves should be allowed")
	}
	if r1.Reserved != r2.Reserved {
		t.Fatalf("reserved amounts differ: %d vs %d", r1.Reserved, r2.Reserved)
	}

	bal, _ := client.rdb.Get(context.Background(), budgetKey("test-bucket")).Int64()
	if bal != 4000 {
		t.Fatalf("balance = %d, want 4000 (single hold)", bal)
	}
}

func TestReleaseBudgetRefundsHold(t *testing.T) {
	_, client := setupTestClient(t, 5000)
	checker := NewRedisBudgetChecker(client, nil)

	_, err := checker.Reserve(context.Background(), "test-bucket", "req-rel", 2000)
	if err != nil {
		t.Fatalf("Reserve: %v", err)
	}
	if err := client.Release(context.Background(), "req-rel"); err != nil {
		t.Fatalf("Release: %v", err)
	}

	bal, _ := client.rdb.Get(context.Background(), budgetKey("test-bucket")).Int64()
	if bal != 5000 {
		t.Fatalf("balance after release = %d, want 5000", bal)
	}
	exists, _ := client.rdb.Exists(context.Background(), reservationKey("req-rel")).Result()
	if exists != 0 {
		t.Fatal("reservation key should be deleted after release")
	}
}

func TestSettleBudgetReconcilesActual(t *testing.T) {
	_, client := setupTestClient(t, 5000)
	checker := NewRedisBudgetChecker(client, nil)

	_, err := checker.Reserve(context.Background(), "test-bucket", "req-set", 2000)
	if err != nil {
		t.Fatalf("Reserve: %v", err)
	}

	// actual > reserved: deduct extra 500
	if err := client.Settle(context.Background(), "req-set", 2500); err != nil {
		t.Fatalf("Settle: %v", err)
	}
	bal, _ := client.rdb.Get(context.Background(), budgetKey("test-bucket")).Int64()
	if bal != 2500 {
		t.Fatalf("balance after settle(actual>reserved) = %d, want 2500", bal)
	}

	// reserve again and settle with actual < reserved
	_, err = checker.Reserve(context.Background(), "test-bucket", "req-set2", 2000)
	if err != nil {
		t.Fatalf("Reserve: %v", err)
	}
	if err := client.Settle(context.Background(), "req-set2", 1500); err != nil {
		t.Fatalf("Settle: %v", err)
	}
	bal, _ = client.rdb.Get(context.Background(), budgetKey("test-bucket")).Int64()
	if bal != 1000 {
		t.Fatalf("balance after settle(actual<reserved) = %d, want 1000", bal)
	}
}

func TestConcurrentReserveNoOverspend(t *testing.T) {
	_, client := setupTestClient(t, 1000)
	checker := NewRedisBudgetChecker(client, nil)

	const (
		workers   = 20
		estimate  = 100
		maxAllows = 10
	)

	var allowed int64
	var wg sync.WaitGroup
	wg.Add(workers)
	for i := 0; i < workers; i++ {
		go func(i int) {
			defer wg.Done()
			result, err := checker.Reserve(context.Background(), "test-bucket", "req-"+strconv.Itoa(i), estimate)
			if err != nil {
				t.Errorf("Reserve: %v", err)
				return
			}
			if result.Allowed {
				allowed++
			}
		}(i)
	}
	wg.Wait()

	if allowed != maxAllows {
		t.Fatalf("allowed = %d, want %d", allowed, maxAllows)
	}

	bal, _ := client.rdb.Get(context.Background(), budgetKey("test-bucket")).Int64()
	if bal != 0 {
		t.Fatalf("balance = %d, want 0", bal)
	}
}

func TestEstimateFromBody(t *testing.T) {
	cfg := EstimateConfig{DefaultEstimate: 4096, PromptBuffer: 512}

	tests := []struct {
		name string
		body string
		want int64
	}{
		{"max_tokens present", `{"model":"gpt-4o","max_tokens":1024}`, 1536},
		{"missing max_tokens", `{"model":"gpt-4o"}`, 4096},
		{"invalid json", `{bad`, 4096},
		{"empty body", ``, 4096},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := EstimateFromBody([]byte(tc.body), cfg, nil)
			if got != tc.want {
				t.Fatalf("EstimateFromBody = %d, want %d", got, tc.want)
			}
		})
	}
}

func TestReadiness(t *testing.T) {
	mr, client := setupTestClient(t, 100)
	ready := NewReadiness(client)
	if !ready.Ready() {
		t.Fatal("expected ready when redis is up")
	}
	mr.Close()
	if ready.Ready() {
		t.Fatal("expected not ready when redis is down")
	}
}

func TestNewClientConfig(t *testing.T) {
	mr := miniredis.RunT(t)
	cfg := config.Config{
		RedisURL:              "redis://" + mr.Addr(),
		RedisPoolSize:         10,
		RedisMinIdleConns:     10,
		RedisCommandTimeout:   50 * time.Millisecond,
		ReservationTTL:        5 * time.Minute,
		DefaultReservationEst: 4096,
		PromptTokenBuffer:     512,
	}
	client, err := NewClient(cfg)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	defer client.Close()
	if client.reserveSHA == "" || client.releaseSHA == "" || client.settleSHA == "" {
		t.Fatal("script SHAs should be loaded")
	}
}
