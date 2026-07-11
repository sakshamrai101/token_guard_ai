package proxy

import (
	"context"
	"log/slog"

	"github.com/saksham/token-guard-ai/internal/budget"
	"github.com/saksham/token-guard-ai/internal/store"
)

type settlementParams struct {
	settler     BudgetSettler
	metrics     *budget.Metrics
	usageLogger store.UsageLogger
	ctx         context.Context
	requestID   string
	orgID       string
	bucketID    string
	provider    string
	reserved    int64
	logger      *slog.Logger
}
