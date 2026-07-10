package proxy

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/saksham/token-guard-ai/internal/budget"
	"github.com/saksham/token-guard-ai/internal/store"
)

type retryStubSettler struct {
	attempts int
	failures int
}

func (s *retryStubSettler) Settle(_ context.Context, _ string, _ int64) error {
	s.attempts++
	if s.attempts <= s.failures {
		return errors.New("redis error")
	}
	return nil
}

func TestSettleWithRetrySyncSuccess(t *testing.T) {
	settler := &retryStubSettler{failures: 2}
	metrics := &budget.Metrics{}
	usageStore := store.NewMemoryUsageStore()

	settleWithRetrySync(settlementParams{
		settler:     settler,
		metrics:     metrics,
		usageLogger: usageStore,
		ctx:         context.Background(),
		requestID:   "req",
		bucketID:    "b1",
		provider:    "api.openai.com",
		reserved:    200,
	}, 100, "settled")

	if settler.attempts != 3 {
		t.Fatalf("attempts = %d, want 3", settler.attempts)
	}
	if metrics.SettleSuccess.Load() != 1 {
		t.Fatalf("settle success = %d, want 1", metrics.SettleSuccess.Load())
	}
	if metrics.SettleRetry.Load() != 2 {
		t.Fatalf("settle retry = %d, want 2", metrics.SettleRetry.Load())
	}
	events, err := usageStore.ListUsage(context.Background(), "", 10)
	if err != nil {
		t.Fatalf("ListUsage: %v", err)
	}
	if len(events) != 1 || events[0].Outcome != "settled" || events[0].Actual != 100 {
		t.Fatalf("events = %+v", events)
	}
}

func TestSettleWithRetrySyncFailsAfterThreeAttempts(t *testing.T) {
	settler := &retryStubSettler{failures: 5}
	metrics := &budget.Metrics{}
	usageStore := store.NewMemoryUsageStore()

	settleWithRetrySync(settlementParams{
		settler:     settler,
		metrics:     metrics,
		usageLogger: usageStore,
		ctx:         context.Background(),
		requestID:   "req",
		reserved:    200,
	}, 100, "settled")

	if settler.attempts != 3 {
		t.Fatalf("attempts = %d, want 3", settler.attempts)
	}
	if metrics.SettleSuccess.Load() != 0 {
		t.Fatalf("settle success = %d, want 0", metrics.SettleSuccess.Load())
	}
	events, _ := usageStore.ListUsage(context.Background(), "", 10)
	if len(events) != 0 {
		t.Fatalf("should not log usage on failed settle, got %+v", events)
	}
}

func TestSettleWithRetryAsync(t *testing.T) {
	settler := &retryStubSettler{}
	metrics := &budget.Metrics{}

	settleWithRetryAsync(settlementParams{
		settler:   settler,
		metrics:   metrics,
		ctx:       context.Background(),
		requestID: "req",
		reserved:  100,
	}, 50, "settled")

	deadline := time.Now().Add(time.Second)
	for settler.attempts == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if settler.attempts != 1 {
		t.Fatalf("attempts = %d, want 1", settler.attempts)
	}
}
