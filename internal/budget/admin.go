package budget

import (
	"context"
	"fmt"

	"github.com/redis/go-redis/v9"
)

func (c *Client) GetBalance(ctx context.Context, bucketID string) (int64, error) {
	val, err := c.rdb.Get(ctx, budgetKey(bucketID)).Int64()
	if err == redis.Nil {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("get balance: %w", err)
	}
	return val, nil
}

func (c *Client) SetBalance(ctx context.Context, bucketID string, balance int64) (int64, error) {
	result, err := c.rdb.EvalSha(ctx, c.setBudgetSHA, []string{budgetKey(bucketID)}, "set", balance).Int64()
	if err != nil {
		return 0, fmt.Errorf("set balance: %w", err)
	}
	return result, nil
}

func (c *Client) TopupBalance(ctx context.Context, bucketID string, amount int64) (int64, error) {
	result, err := c.rdb.EvalSha(ctx, c.setBudgetSHA, []string{budgetKey(bucketID)}, "topup", amount).Int64()
	if err != nil {
		return 0, fmt.Errorf("topup balance: %w", err)
	}
	return result, nil
}
