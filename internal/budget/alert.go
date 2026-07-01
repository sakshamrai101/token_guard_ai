package budget

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"time"
)

type Alerter struct {
	webhookURL string
	client     *http.Client
	logger     *slog.Logger
}

func NewAlerter(webhookURL string, logger *slog.Logger) *Alerter {
	if logger == nil {
		logger = slog.Default()
	}
	return &Alerter{
		webhookURL: webhookURL,
		client:     &http.Client{Timeout: 5 * time.Second},
		logger:     logger,
	}
}

func (a *Alerter) FailOpen(ctx context.Context, requestID, bucketID, detail string) {
	msg := fmt.Sprintf("CRITICAL: budget enforcement fail-open — request_id=%s bucket_id=%s %s", requestID, bucketID, detail)
	a.logger.Error(msg)
	a.postSlack(ctx, msg)
}

func (a *Alerter) BudgetDenied(ctx context.Context, requestID, bucketID string, estimate int64) {
	msg := fmt.Sprintf("WARN: budget denied — request_id=%s bucket_id=%s estimate=%d", requestID, bucketID, estimate)
	a.logger.Warn(msg)
	a.postSlack(ctx, msg)
}

func (a *Alerter) postSlack(ctx context.Context, text string) {
	if a.webhookURL == "" {
		return
	}
	payload, err := json.Marshal(map[string]string{"text": text})
	if err != nil {
		a.logger.Error("failed to marshal slack payload", "error", err)
		return
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, a.webhookURL, bytes.NewReader(payload))
	if err != nil {
		a.logger.Error("failed to create slack request", "error", err)
		return
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := a.client.Do(req)
	if err != nil {
		a.logger.Error("failed to post slack alert", "error", err)
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		a.logger.Error("slack webhook returned error", "status", resp.StatusCode)
	}
}
