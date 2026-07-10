package proxy

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"

	"github.com/saksham/token-guard-ai/internal/usage"
)

const reservedContextKey ctxKey = "reserved"

func isEventStream(h http.Header) bool {
	return strings.Contains(h.Get("Content-Type"), "text/event-stream")
}

type settlingReader struct {
	underlying io.ReadCloser
	buf        bytes.Buffer
	extractor  usage.UsageExtractor
	params     settlementParams
	once       sync.Once
}

func newSettlingReader(
	underlying io.ReadCloser,
	extractor usage.UsageExtractor,
	params settlementParams,
) io.ReadCloser {
	if params.logger == nil {
		params.logger = slog.Default()
	}
	return &settlingReader{
		underlying: underlying,
		extractor:  extractor,
		params:     params,
	}
}

func (r *settlingReader) Read(p []byte) (int, error) {
	if err := r.params.ctx.Err(); err != nil {
		r.finalize("disconnected", r.params.reserved, false)
		return 0, err
	}

	n, err := r.underlying.Read(p)
	if n > 0 {
		_, _ = r.buf.Write(p[:n])
	}
	if err == io.EOF {
		r.finalizeFromBody()
	}
	return n, err
}

func (r *settlingReader) Close() error {
	err := r.underlying.Close()
	r.finalizeFromBody()
	return err
}

func (r *settlingReader) finalizeFromBody() {
	if r.extractor == nil {
		return
	}

	u, err := r.extractor.ExtractFromJSON(r.buf.Bytes())
	if err != nil {
		r.params.logger.Warn("failed to extract usage from non-streaming response",
			"request_id", r.params.requestID,
			"error", err,
		)
		r.finalize("missing_usage", r.params.reserved, true)
		return
	}
	r.finalize("settled", u.Total(), false)
}

func (r *settlingReader) finalize(outcome string, actual int64, countMissing bool) {
	r.once.Do(func() {
		if countMissing && r.params.metrics != nil {
			r.params.metrics.IncMissingUsage()
		}
		p := r.params
		p.ctx = context.WithoutCancel(r.params.ctx)
		settleWithRetrySync(p, actual, outcome)
	})
}
