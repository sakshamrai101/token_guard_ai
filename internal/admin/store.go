package admin

import (
	"context"

	"github.com/saksham/token-guard-ai/internal/budget"
	"github.com/saksham/token-guard-ai/internal/store"
)

type Store interface {
	GetBalance(ctx context.Context, orgID, bucketID string) (int64, error)
	SetBalance(ctx context.Context, orgID, bucketID string, balance int64) (int64, error)
	Topup(ctx context.Context, orgID, bucketID string, amount int64) (int64, error)
	ListBuckets(ctx context.Context) ([]budget.BucketBalance, error)
	ListReservations(ctx context.Context) ([]budget.ReservationHold, error)
}

type UsageQuerier interface {
	ListUsage(ctx context.Context, bucketID string, limit int) ([]store.UsageEvent, error)
}
