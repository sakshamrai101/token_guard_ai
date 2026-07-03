package budget

import (
	"context"
	"io"
	"strings"
	"testing"
	"time"
)

func TestReadAndRestoreBody(t *testing.T) {
	input := `{"model":"gpt-4o","max_tokens":512}`
	body, restored, err := ReadAndRestoreBody(io.NopCloser(strings.NewReader(input)))
	if err != nil {
		t.Fatalf("ReadAndRestoreBody: %v", err)
	}
	if string(body) != input {
		t.Fatalf("body = %q, want %q", body, input)
	}

	got, err := io.ReadAll(restored)
	if err != nil {
		t.Fatalf("read restored: %v", err)
	}
	if string(got) != input {
		t.Fatalf("restored = %q, want %q", got, input)
	}
}

func TestReadAndRestoreBodyNil(t *testing.T) {
	body, restored, err := ReadAndRestoreBody(nil)
	if err != nil {
		t.Fatalf("ReadAndRestoreBody(nil): %v", err)
	}
	if len(body) != 0 {
		t.Fatalf("body = %q, want empty", body)
	}
	got, _ := io.ReadAll(restored)
	if len(got) != 0 {
		t.Fatalf("restored = %q, want empty", got)
	}
}

func TestReadAndRestoreBodyReadError(t *testing.T) {
	_, _, err := ReadAndRestoreBody(io.NopCloser(&errReader{}))
	if err == nil {
		t.Fatal("expected read error")
	}
}

type errReader struct{}

func (e *errReader) Read([]byte) (int, error) { return 0, io.ErrUnexpectedEOF }
func (e *errReader) Close() error             { return nil }

func TestMetricsCounters(t *testing.T) {
	m := &Metrics{}
	m.IncAllowed()
	m.IncDenied()
	m.IncFailOpen()
	m.IncSettleSuccess()
	m.IncSettleRetry()
	m.IncMissingUsage()
	m.RecordReserve(2 * time.Millisecond)
	m.RecordReserve(4 * time.Millisecond)

	if m.BudgetCheckAllowed.Load() != 1 {
		t.Fatalf("allowed = %d, want 1", m.BudgetCheckAllowed.Load())
	}
	if m.BudgetCheckDenied.Load() != 1 {
		t.Fatalf("denied = %d, want 1", m.BudgetCheckDenied.Load())
	}
	if m.FailOpenTotal.Load() != 1 || m.BudgetCheckFailOpen.Load() != 1 {
		t.Fatalf("fail_open counters = %d/%d, want 1/1", m.FailOpenTotal.Load(), m.BudgetCheckFailOpen.Load())
	}
	if m.SettleSuccess.Load() != 1 {
		t.Fatalf("settle success = %d, want 1", m.SettleSuccess.Load())
	}
	if m.SettleRetry.Load() != 1 {
		t.Fatalf("settle retry = %d, want 1", m.SettleRetry.Load())
	}
	if m.MissingUsage.Load() != 1 {
		t.Fatalf("missing usage = %d, want 1", m.MissingUsage.Load())
	}
	if avg := m.ReserveAvgMs(); avg <= 0 {
		t.Fatalf("ReserveAvgMs = %f, want > 0", avg)
	}
}

func TestEstimateFromBodyZeroMaxTokensUsesDefault(t *testing.T) {
	cfg := EstimateConfig{DefaultEstimate: 4096, PromptBuffer: 512}
	got := EstimateFromBody([]byte(`{"max_tokens":0}`), cfg, nil)
	if got != 4096 {
		t.Fatalf("got %d, want default 4096 for zero max_tokens", got)
	}
}

func TestEstimateFromBodyNegativeMaxTokensUsesDefault(t *testing.T) {
	cfg := EstimateConfig{DefaultEstimate: 4096, PromptBuffer: 512}
	got := EstimateFromBody([]byte(`{"max_tokens":-1}`), cfg, nil)
	if got != 4096 {
		t.Fatalf("got %d, want default 4096 for negative max_tokens", got)
	}
}

func TestReleaseIdempotentWhenMissing(t *testing.T) {
	_, client := setupTestClient(t, 1000)
	if err := client.Release(context.Background(), "no-such-request"); err != nil {
		t.Fatalf("Release on missing reservation: %v", err)
	}
}

func TestSettleIdempotentWhenMissing(t *testing.T) {
	_, client := setupTestClient(t, 1000)
	if err := client.Settle(context.Background(), "no-such-request", 100); err != nil {
		t.Fatalf("Settle on missing reservation: %v", err)
	}
}

func TestSettleExactReservedDeletesReservation(t *testing.T) {
	_, client := setupTestClient(t, 5000)
	checker := NewRedisBudgetChecker(client, nil)

	_, err := checker.Reserve(context.Background(), "test-bucket", "req-exact", 1000)
	if err != nil {
		t.Fatalf("Reserve: %v", err)
	}
	if err := client.Settle(context.Background(), "req-exact", 1000); err != nil {
		t.Fatalf("Settle: %v", err)
	}

	bal, _ := client.rdb.Get(context.Background(), budgetKey("test-bucket")).Int64()
	if bal != 4000 {
		t.Fatalf("balance = %d, want 4000", bal)
	}
	exists, _ := client.rdb.Exists(context.Background(), reservationKey("req-exact")).Result()
	if exists != 0 {
		t.Fatal("reservation should be deleted after settle")
	}
}
