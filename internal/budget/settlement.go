package budget

import (
	"context"
	"fmt"
	"time"
)

func (c *Client) Release(ctx context.Context, requestID string) error {
	if requestID == "" {
		return nil
	}
	_, err := c.rdb.EvalSha(ctx, c.releaseSHA, []string{
		reservationKey(requestID),
	}).Result()
	if err != nil {
		return fmt.Errorf("release_budget: %w", err)
	}
	return nil
}

func (c *Client) Settle(ctx context.Context, requestID string, actual int64) error {
	if requestID == "" {
		return nil
	}
	_, err := c.rdb.EvalSha(ctx, c.settleSHA, []string{
		reservationKey(requestID),
	}, actual).Result()
	if err != nil {
		return fmt.Errorf("settle_budget: %w", err)
	}
	return nil
}

type Readiness struct {
	client *Client
}

func NewReadiness(client *Client) *Readiness {
	return &Readiness{client: client}
}

func (r *Readiness) Ready() bool {
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	return r.client.Ready(ctx)
}
