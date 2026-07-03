package proxy

import (
	"context"
	"log/slog"
	"time"

	"github.com/saksham/token-guard-ai/internal/budget"
)

func settleWithRetrySync(
	ctx context.Context,
	settler BudgetSettler,
	metrics *budget.Metrics,
	requestID string,
	actual, reserved int64,
	outcome string,
	logger *slog.Logger,
) {
	if settler == nil || requestID == "" {
		return
	}
	if logger == nil {
		logger = slog.Default()
	}

	for attempt := 0; attempt < 3; attempt++ {
		if err := settler.Settle(ctx, requestID, actual); err == nil {
			if metrics != nil {
				metrics.IncSettleSuccess()
			}
			logger.Info("budget settled",
				"request_id", requestID,
				"reserved", reserved,
				"actual", actual,
				"outcome", outcome,
			)
			return
		}
		if attempt < 2 {
			if metrics != nil {
				metrics.IncSettleRetry()
			}
			time.Sleep(time.Duration(attempt+1) * 50 * time.Millisecond)
		}
	}
	logger.Error("settle failed after retries",
		"request_id", requestID,
		"reserved", reserved,
		"actual", actual,
		"outcome", outcome,
	)
}

func settleWithRetryAsync(
	ctx context.Context,
	settler BudgetSettler,
	metrics *budget.Metrics,
	requestID string,
	actual, reserved int64,
	outcome string,
	logger *slog.Logger,
) {
	go settleWithRetrySync(context.WithoutCancel(ctx), settler, metrics, requestID, actual, reserved, outcome, logger)
}
