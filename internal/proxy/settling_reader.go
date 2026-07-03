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
	settler    BudgetSettler
	requestID  string
	reserved   int64
	ctx        context.Context
	logger     *slog.Logger
	once       sync.Once
}

func newSettlingReader(
	underlying io.ReadCloser,
	extractor usage.UsageExtractor,
	settler BudgetSettler,
	ctx context.Context,
	requestID string,
	reserved int64,
	logger *slog.Logger,
) io.ReadCloser {
	if logger == nil {
		logger = slog.Default()
	}
	return &settlingReader{
		underlying: underlying,
		extractor:  extractor,
		settler:    settler,
		ctx:        ctx,
		requestID:  requestID,
		reserved:   reserved,
		logger:     logger,
	}
}

func (r *settlingReader) Read(p []byte) (int, error) {
	n, err := r.underlying.Read(p)
	if n > 0 {
		_, _ = r.buf.Write(p[:n])
	}
	if err == io.EOF {
		r.settle()
	}
	return n, err
}

func (r *settlingReader) Close() error {
	err := r.underlying.Close()
	r.settle()
	return err
}

func (r *settlingReader) settle() {
	r.once.Do(func() {
		if r.settler == nil || r.extractor == nil || r.requestID == "" {
			return
		}

		u, err := r.extractor.ExtractFromJSON(r.buf.Bytes())
		if err != nil {
			r.logger.Warn("failed to extract usage from non-streaming response",
				"request_id", r.requestID,
				"error", err,
			)
			return
		}

		actual := u.Total()
		if err := r.settler.Settle(r.ctx, r.requestID, actual); err != nil {
			r.logger.Error("failed to settle budget",
				"request_id", r.requestID,
				"reserved", r.reserved,
				"actual", actual,
				"error", err,
			)
			return
		}

		r.logger.Info("budget settled",
			"request_id", r.requestID,
			"reserved", r.reserved,
			"actual", actual,
			"outcome", "settled",
		)
	})
}
