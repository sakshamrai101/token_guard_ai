package proxy

import (
	"context"

	"github.com/saksham/token-guard-ai/internal/budget"
)

type budgetReserve interface {
	Reserve(ctx context.Context, orgID, bucketID, requestID string, estimate int64) (budget.ReserveResult, error)
}

type budgetCheckerBridge struct {
	inner budgetReserve
}

func NewBudgetCheckerBridge(inner budgetReserve) BudgetChecker {
	return budgetCheckerBridge{inner: inner}
}

func (b budgetCheckerBridge) Reserve(ctx context.Context, orgID, bucketID, requestID string, estimate int64) (PreCheckResult, error) {
	r, err := b.inner.Reserve(ctx, orgID, bucketID, requestID, estimate)
	return PreCheckResult{Allowed: r.Allowed, Reserved: r.Reserved}, err
}
