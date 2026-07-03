package proxy

import (
	"context"
	"log/slog"

	"github.com/saksham/token-guard-ai/internal/budget"
)

type settlementParams struct {
	settler   BudgetSettler
	metrics   *budget.Metrics
	ctx       context.Context
	requestID string
	reserved  int64
	logger    *slog.Logger
}
