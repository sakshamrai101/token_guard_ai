package proxy

import (
	"io"
	"log/slog"
	"sync"

	"github.com/saksham/token-guard-ai/internal/usage"
	"github.com/saksham/token-guard-ai/internal/usage/sse"
)

type streamTap struct {
	underlying io.ReadCloser
	parser     *sse.Parser
	streamExt  usage.StreamExtractor
	params     settlementParams
	once       sync.Once
	lastUsage  usage.Usage
	hasUsage   bool
	sawDone    bool
}

func newStreamTap(
	underlying io.ReadCloser,
	streamExt usage.StreamExtractor,
	params settlementParams,
) io.ReadCloser {
	if params.logger == nil {
		params.logger = slog.Default()
	}
	return &streamTap{
		underlying: underlying,
		parser:     sse.NewParser(),
		streamExt:  streamExt,
		params:     params,
	}
}

func (t *streamTap) Read(p []byte) (int, error) {
	if err := t.params.ctx.Err(); err != nil {
		t.triggerSettle("disconnected", t.params.reserved)
		return 0, err
	}

	n, err := t.underlying.Read(p)
	if n > 0 {
		t.processEvents(t.parser.Feed(p[:n]))
	}
	if err == io.EOF {
		t.finishAfterStream()
	}
	return n, err
}

func (t *streamTap) Close() error {
	err := t.underlying.Close()
	t.finishAfterStream()
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

func (t *streamTap) finishAfterStream() {
	t.once.Do(func() {
		t.processEvents(t.parser.Flush())
		if t.hasUsage {
			t.runSettle("settled", t.lastUsage.Total())
			return
		}
		if t.params.metrics != nil {
			t.params.metrics.IncMissingUsage()
		}
		t.params.logger.Warn("stream ended without usage metadata",
			"request_id", t.params.requestID,
			"saw_done", t.sawDone,
		)
		t.runSettle("missing_usage", t.params.reserved)
	})
}

func (t *streamTap) triggerSettle(outcome string, actual int64) {
	t.once.Do(func() {
		t.runSettle(outcome, actual)
	})
}

func (t *streamTap) runSettle(outcome string, actual int64) {
	settleWithRetryAsync(t.params, actual, outcome)
}
