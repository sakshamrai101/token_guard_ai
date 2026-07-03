package proxy

import (
	"context"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/saksham/token-guard-ai/internal/usage"
)

type asyncStubSettler struct {
	mu    sync.Mutex
	calls []settleCall
	done  chan struct{}
}

func newAsyncStubSettler() *asyncStubSettler {
	return &asyncStubSettler{done: make(chan struct{}, 1)}
}

func (s *asyncStubSettler) Settle(_ context.Context, requestID string, actual int64) error {
	s.mu.Lock()
	s.calls = append(s.calls, settleCall{requestID: requestID, actual: actual})
	s.mu.Unlock()
	select {
	case s.done <- struct{}{}:
	default:
	}
	return nil
}

func (s *asyncStubSettler) waitSettle(t *testing.T, timeout time.Duration) settleCall {
	t.Helper()
	select {
	case <-s.done:
	case <-time.After(timeout):
		t.Fatal("timed out waiting for async settle")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.calls) == 0 {
		t.Fatal("expected settle call")
	}
	return s.calls[0]
}

func TestStreamTapForwardsBytesAndSettlesAsync(t *testing.T) {
	stream := "" +
		"data: {\"choices\":[{\"delta\":{\"content\":\"Hi\"}}]}\n\n" +
		"data: {\"choices\":[],\"usage\":{\"total_tokens\":42}}\n\n" +
		"data: [DONE]\n\n"

	settler := newAsyncStubSettler()
	tap := newStreamTap(
		io.NopCloser(strings.NewReader(stream)),
		usage.NewOpenAIStreamExtractor(),
		settler,
		context.Background(),
		"req-stream",
		100,
		nil,
	)

	got, err := io.ReadAll(tap)
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	if string(got) != stream {
		t.Fatalf("forwarded stream length = %d, want %d", len(got), len(stream))
	}
	if err := tap.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	call := settler.waitSettle(t, time.Second)
	if call.requestID != "req-stream" || call.actual != 42 {
		t.Fatalf("settle call = %+v, want req-stream/42", call)
	}
}

func TestStreamTapSettlesOnce(t *testing.T) {
	stream := "data: {\"usage\":{\"total_tokens\":10}}\n\ndata: [DONE]\n\n"
	settler := newAsyncStubSettler()
	tap := newStreamTap(
		io.NopCloser(strings.NewReader(stream)),
		usage.NewOpenAIStreamExtractor(),
		settler,
		context.Background(),
		"req-once",
		50,
		nil,
	)

	_, _ = io.ReadAll(tap)
	_ = tap.Close()
	_ = tap.Close()

	settler.waitSettle(t, time.Second)
	settler.mu.Lock()
	defer settler.mu.Unlock()
	if len(settler.calls) != 1 {
		t.Fatalf("settle calls = %d, want 1", len(settler.calls))
	}
}

func TestStreamTapPartialReads(t *testing.T) {
	pr, pw := io.Pipe()
	settler := newAsyncStubSettler()
	tap := newStreamTap(
		pr,
		usage.NewOpenAIStreamExtractor(),
		settler,
		context.Background(),
		"req-partial",
		50,
		nil,
	)

	go func() {
		_, _ = pw.Write([]byte("data: {\"usage\":{\"total_to"))
		_, _ = pw.Write([]byte("kens\":7}}\n\ndata: [DONE]\n\n"))
		_ = pw.Close()
	}()

	_, _ = io.ReadAll(tap)
	_ = tap.Close()

	call := settler.waitSettle(t, time.Second)
	if call.actual != 7 {
		t.Fatalf("actual = %d, want 7", call.actual)
	}
}

func TestStreamTapNoSettleWithoutUsage(t *testing.T) {
	stream := "data: {\"choices\":[{\"delta\":{\"content\":\"x\"}}]}\n\ndata: [DONE]\n\n"
	settler := newAsyncStubSettler()
	tap := newStreamTap(
		io.NopCloser(strings.NewReader(stream)),
		usage.NewOpenAIStreamExtractor(),
		settler,
		context.Background(),
		"req-nousage",
		50,
		nil,
	)

	_, _ = io.ReadAll(tap)
	_ = tap.Close()

	select {
	case <-settler.done:
		t.Fatal("expected no settle without usage")
	case <-time.After(100 * time.Millisecond):
	}
}
