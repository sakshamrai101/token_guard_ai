package proxy

import (
	"context"
	"log/slog"
	"time"
)

func settleWithRetry(ctx context.Context, settler BudgetSettler, requestID string, actual, reserved int64, logger *slog.Logger) {
	if settler == nil || requestID == "" {
		return
	}
	if logger == nil {
		logger = slog.Default()
	}

	go func() {
		for attempt := 0; attempt < 3; attempt++ {
			if err := settler.Settle(ctx, requestID, actual); err == nil {
				logger.Info("budget settled",
					"request_id", requestID,
					"reserved", reserved,
					"actual", actual,
					"outcome", "settled",
				)
				return
			}
			time.Sleep(time.Duration(attempt+1) * 50 * time.Millisecond)
		}
		logger.Error("settle failed after retries",
			"request_id", requestID,
			"reserved", reserved,
			"actual", actual,
		)
	}()
}
