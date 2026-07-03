package admin

import (
	"context"
)

type Store interface {
	GetBalance(ctx context.Context, bucketID string) (int64, error)
	SetBalance(ctx context.Context, bucketID string, balance int64) (int64, error)
	Topup(ctx context.Context, bucketID string, amount int64) (int64, error)
}
