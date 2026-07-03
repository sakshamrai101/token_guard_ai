package proxy

import (
	"context"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/saksham/token-guard-ai/internal/budget"
	"github.com/saksham/token-guard-ai/internal/usage"
)

type stubSettler struct {
	calls []settleCall
	err   error
}

type settleCall struct {
	requestID string
	actual    int64
}

func (s *stubSettler) Settle(_ context.Context, requestID string, actual int64) error {
	s.calls = append(s.calls, settleCall{requestID: requestID, actual: actual})
	return s.err
}

func testParams(ctx context.Context, settler BudgetSettler, requestID string, reserved int64) settlementParams {
	return settlementParams{
		settler:   settler,
		ctx:       ctx,
		requestID: requestID,
		reserved:  reserved,
	}
}

func TestSettlingReaderSettlesOnClose(t *testing.T) {
	settler := &stubSettler{}
	body := `{"choices":[],"usage":{"total_tokens":200}}`
	reader := newSettlingReader(
		io.NopCloser(strings.NewReader(body)),
		usage.NewOpenAIExtractor(),
		testParams(context.Background(), settler, "req-1", 612),
	)

	if _, err := io.ReadAll(reader); err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	if err := reader.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	if len(settler.calls) != 1 {
		t.Fatalf("settle calls = %d, want 1", len(settler.calls))
	}
	if settler.calls[0].requestID != "req-1" || settler.calls[0].actual != 200 {
		t.Fatalf("settle call = %+v, want req-1/200", settler.calls[0])
	}
}

func TestSettlingReaderForwardsBytes(t *testing.T) {
	want := `{"choices":[],"usage":{"total_tokens":42}}`
	reader := newSettlingReader(
		io.NopCloser(strings.NewReader(want)),
		usage.NewOpenAIExtractor(),
		testParams(context.Background(), &stubSettler{}, "req-2", 100),
	)

	got, err := io.ReadAll(reader)
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	if string(got) != want {
		t.Fatalf("forwarded body = %q, want %q", got, want)
	}
}

func TestSettlingReaderSettlesAtReservedWhenUsageMissing(t *testing.T) {
	settler := &stubSettler{}
	metrics := &budget.Metrics{}
	reader := newSettlingReader(
		io.NopCloser(strings.NewReader(`{"id":"no-usage"}`)),
		usage.NewOpenAIExtractor(),
		settlementParams{
			settler:   settler,
			metrics:   metrics,
			ctx:       context.Background(),
			requestID: "req-3",
			reserved:  612,
		},
	)

	_, _ = io.ReadAll(reader)
	_ = reader.Close()

	if len(settler.calls) != 1 {
		t.Fatalf("settle calls = %d, want 1", len(settler.calls))
	}
	if settler.calls[0].actual != 612 {
		t.Fatalf("actual = %d, want reserved 612", settler.calls[0].actual)
	}
	if metrics.MissingUsage.Load() != 1 {
		t.Fatalf("missing_usage = %d, want 1", metrics.MissingUsage.Load())
	}
}

func TestSettlingReaderSettlesOnce(t *testing.T) {
	settler := &stubSettler{}
	body := `{"usage":{"total_tokens":10}}`
	reader := newSettlingReader(
		io.NopCloser(strings.NewReader(body)),
		usage.NewOpenAIExtractor(),
		testParams(context.Background(), settler, "req-4", 100),
	)

	_, _ = io.ReadAll(reader)
	_ = reader.Close()
	_ = reader.Close()

	if len(settler.calls) != 1 {
		t.Fatalf("settle calls = %d, want 1 (sync.Once)", len(settler.calls))
	}
}

func TestSettlingReaderSettlesAtReservedOnDisconnect(t *testing.T) {
	settler := &stubSettler{}
	ctx, cancel := context.WithCancel(context.Background())
	pr, pw := io.Pipe()

	reader := newSettlingReader(
		pr,
		usage.NewOpenAIExtractor(),
		testParams(ctx, settler, "req-disc", 500),
	)

	go func() {
		_, _ = pw.Write([]byte(`{"choices":[{"message":{"content":"partial`))
		cancel()
		_ = pw.Close()
	}()

	_, _ = io.ReadAll(reader)
	_ = reader.Close()

	deadline := time.Now().Add(500 * time.Millisecond)
	for len(settler.calls) == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if len(settler.calls) != 1 {
		t.Fatalf("settle calls = %d, want 1 on disconnect", len(settler.calls))
	}
	if settler.calls[0].actual != 500 {
		t.Fatalf("actual = %d, want reserved 500", settler.calls[0].actual)
	}
}
