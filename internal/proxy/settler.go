package proxy

import "context"

type BudgetSettler interface {
	Settle(ctx context.Context, requestID string, actual int64) error
}
