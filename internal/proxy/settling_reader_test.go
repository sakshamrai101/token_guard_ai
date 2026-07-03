package proxy

import (
	"context"
	"io"
	"strings"
	"testing"

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

func TestSettlingReaderSettlesOnClose(t *testing.T) {
	settler := &stubSettler{}
	body := `{"choices":[],"usage":{"total_tokens":200}}`
	reader := newSettlingReader(
		io.NopCloser(strings.NewReader(body)),
		usage.NewOpenAIExtractor(),
		settler,
		context.Background(),
		"req-1",
		612,
		nil,
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
		&stubSettler{},
		context.Background(),
		"req-2",
		100,
		nil,
	)

	got, err := io.ReadAll(reader)
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	if string(got) != want {
		t.Fatalf("forwarded body = %q, want %q", got, want)
	}
}

func TestSettlingReaderSkipsSettleWithoutUsage(t *testing.T) {
	settler := &stubSettler{}
	reader := newSettlingReader(
		io.NopCloser(strings.NewReader(`{"id":"no-usage"}`)),
		usage.NewOpenAIExtractor(),
		settler,
		context.Background(),
		"req-3",
		612,
		nil,
	)

	_, _ = io.ReadAll(reader)
	_ = reader.Close()

	if len(settler.calls) != 0 {
		t.Fatalf("settle calls = %d, want 0 when usage missing", len(settler.calls))
	}
}

func TestSettlingReaderSettlesOnce(t *testing.T) {
	settler := &stubSettler{}
	body := `{"usage":{"total_tokens":10}}`
	reader := newSettlingReader(
		io.NopCloser(strings.NewReader(body)),
		usage.NewOpenAIExtractor(),
		settler,
		context.Background(),
		"req-4",
		100,
		nil,
	)

	_, _ = io.ReadAll(reader)
	_ = reader.Close()
	_ = reader.Close()

	if len(settler.calls) != 1 {
		t.Fatalf("settle calls = %d, want 1 (sync.Once)", len(settler.calls))
	}
}
