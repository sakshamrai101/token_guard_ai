package proxy

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/saksham/token-guard-ai/internal/budget"
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

	settleWithRetrySync(context.Background(), settler, metrics, "req", 100, 200, "settled", nil)

	if settler.attempts != 3 {
		t.Fatalf("attempts = %d, want 3", settler.attempts)
	}
	if metrics.SettleSuccess.Load() != 1 {
		t.Fatalf("settle success = %d, want 1", metrics.SettleSuccess.Load())
	}
	if metrics.SettleRetry.Load() != 2 {
		t.Fatalf("settle retry = %d, want 2", metrics.SettleRetry.Load())
	}
}

func TestSettleWithRetrySyncFailsAfterThreeAttempts(t *testing.T) {
	settler := &retryStubSettler{failures: 5}
	metrics := &budget.Metrics{}

	settleWithRetrySync(context.Background(), settler, metrics, "req", 100, 200, "settled", nil)

	if settler.attempts != 3 {
		t.Fatalf("attempts = %d, want 3", settler.attempts)
	}
	if metrics.SettleSuccess.Load() != 0 {
		t.Fatalf("settle success = %d, want 0", metrics.SettleSuccess.Load())
	}
}

func TestSettleWithRetryAsync(t *testing.T) {
	settler := &retryStubSettler{}
	metrics := &budget.Metrics{}

	settleWithRetryAsync(context.Background(), settler, metrics, "req", 50, 100, "settled", nil)

	deadline := time.Now().Add(time.Second)
	for settler.attempts == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if settler.attempts != 1 {
		t.Fatalf("attempts = %d, want 1", settler.attempts)
	}
}
