package admin

import (
	"context"

	"github.com/saksham/token-guard-ai/internal/budget"
)

type RedisStore struct {
	client *budget.Client
}

func NewRedisStore(client *budget.Client) *RedisStore {
	return &RedisStore{client: client}
}

func (s *RedisStore) GetBalance(ctx context.Context, bucketID string) (int64, error) {
	return s.client.GetBalance(ctx, bucketID)
}

func (s *RedisStore) SetBalance(ctx context.Context, bucketID string, balance int64) (int64, error) {
	return s.client.SetBalance(ctx, bucketID, balance)
}

func (s *RedisStore) Topup(ctx context.Context, bucketID string, amount int64) (int64, error) {
	return s.client.TopupBalance(ctx, bucketID, amount)
}
