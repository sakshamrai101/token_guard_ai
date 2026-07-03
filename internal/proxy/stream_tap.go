package proxy

import (
	"context"
	"io"
	"log/slog"
	"sync"

	"github.com/saksham/token-guard-ai/internal/usage"
	"github.com/saksham/token-guard-ai/internal/usage/sse"
)

type streamTap struct {
	underlying   io.ReadCloser
	parser       *sse.Parser
	streamExt    usage.StreamExtractor
	settler      BudgetSettler
	requestID    string
	reserved     int64
	ctx          context.Context
	logger       *slog.Logger
	once         sync.Once
	lastUsage    usage.Usage
	hasUsage     bool
	sawDone      bool
}

func newStreamTap(
	underlying io.ReadCloser,
	streamExt usage.StreamExtractor,
	settler BudgetSettler,
	ctx context.Context,
	requestID string,
	reserved int64,
	logger *slog.Logger,
) io.ReadCloser {
	if logger == nil {
		logger = slog.Default()
	}
	return &streamTap{
		underlying: underlying,
		parser:     sse.NewParser(),
		streamExt:  streamExt,
		settler:    settler,
		ctx:        ctx,
		requestID:  requestID,
		reserved:   reserved,
		logger:     logger,
	}
}

func (t *streamTap) Read(p []byte) (int, error) {
	n, err := t.underlying.Read(p)
	if n > 0 {
		t.processEvents(t.parser.Feed(p[:n]))
	}
	if err == io.EOF {
		t.finish()
	}
	return n, err
}

func (t *streamTap) Close() error {
	err := t.underlying.Close()
	t.finish()
	return err
}

func (t *streamTap) processEvents(events []sse.Event) {
	for _, ev := range events {
		if ev.Done {
			t.sawDone = true
			continue
		}
		if u, ok := t.streamExt.ExtractFromEvent(ev); ok {
			t.lastUsage = u
			t.hasUsage = true
		}
	}
}

func (t *streamTap) finish() {
	t.once.Do(func() {
		t.processEvents(t.parser.Flush())
		if !t.hasUsage {
			t.logger.Warn("stream ended without usage metadata",
				"request_id", t.requestID,
				"saw_done", t.sawDone,
			)
			return
		}
		settleCtx := context.WithoutCancel(t.ctx)
		settleWithRetry(settleCtx, t.settler, t.requestID, t.lastUsage.Total(), t.reserved, t.logger)
	})
}
