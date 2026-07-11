package proxy

import (
	"context"
	"log/slog"
	"time"

	"github.com/saksham/token-guard-ai/internal/store"
)

func settleWithRetrySync(p settlementParams, actual int64, outcome string) {
	if p.settler == nil || p.requestID == "" {
		return
	}
	if p.logger == nil {
		p.logger = slog.Default()
	}
	ctx := p.ctx
	if ctx == nil {
		ctx = context.Background()
	}

	for attempt := 0; attempt < 3; attempt++ {
		if err := p.settler.Settle(ctx, p.requestID, actual); err == nil {
			if p.metrics != nil {
				p.metrics.IncSettleSuccess()
			}
			p.logger.Info("budget settled",
				"request_id", p.requestID,
				"reserved", p.reserved,
				"actual", actual,
				"outcome", outcome,
			)
			logUsageEvent(ctx, p.usageLogger, p, actual, outcome)
			maybeWarn80(ctx, p, actual)
			return
		}
		if attempt < 2 {
			if p.metrics != nil {
				p.metrics.IncSettleRetry()
			}
			time.Sleep(time.Duration(attempt+1) * 50 * time.Millisecond)
		}
	}
	p.logger.Error("settle failed after retries",
		"request_id", p.requestID,
		"reserved", p.reserved,
		"actual", actual,
		"outcome", outcome,
	)
}

func settleWithRetryAsync(p settlementParams, actual int64, outcome string) {
	p.ctx = context.WithoutCancel(p.ctx)
	go settleWithRetrySync(p, actual, outcome)
}

func logUsageEvent(ctx context.Context, logger store.UsageLogger, p settlementParams, actual int64, outcome string) {
	if logger == nil {
		return
	}
	orgID := p.orgID
	if orgID == "" {
		orgID = store.DefaultOrgID
	}
	if err := logger.LogUsage(ctx, store.UsageEvent{
		OrgID:     orgID,
		BucketID:  p.bucketID,
		RequestID: p.requestID,
		Reserved:  p.reserved,
		Actual:    actual,
		Outcome:   outcome,
		Provider:  p.provider,
	}); err != nil && p.logger != nil {
		p.logger.Error("failed to log usage event",
			"request_id", p.requestID,
			"error", err,
		)
	}
}

func maybeWarn80(ctx context.Context, p settlementParams, actual int64) {
	if p.alerter == nil || p.balances == nil || p.bucketID == "" {
		return
	}
	orgID := p.orgID
	if orgID == "" {
		orgID = store.DefaultOrgID
	}
	remaining, err := p.balances.GetBalance(ctx, orgID, p.bucketID)
	if err != nil {
		if p.logger != nil {
			p.logger.Error("failed to read balance for 80% warning",
				"request_id", p.requestID,
				"error", err,
			)
		}
		return
	}
	p.alerter.MaybeBudgetWarning80(ctx, p.orgWebhook, orgID, p.bucketID, remaining, actual)
}
