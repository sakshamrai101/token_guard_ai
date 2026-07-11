package budget

import (
	"context"
	"fmt"
	"time"
)

type RedisBudgetChecker struct {
	client  *Client
	metrics *Metrics
}

func NewRedisBudgetChecker(client *Client, metrics *Metrics) *RedisBudgetChecker {
	if metrics == nil {
		metrics = &Metrics{}
	}
	return &RedisBudgetChecker{client: client, metrics: metrics}
}

func (c *RedisBudgetChecker) Reserve(ctx context.Context, orgID, bucketID, requestID string, estimate int64) (ReserveResult, error) {
	start := time.Now()
	defer func() {
		c.metrics.RecordReserve(time.Since(start))
	}()

	if bucketID == "" {
		return ReserveResult{Allowed: true}, fmt.Errorf("empty bucket_id")
	}
	if requestID == "" {
		return ReserveResult{}, fmt.Errorf("empty request_id")
	}
	if orgID == "" {
		orgID = "default"
	}

	ttlSec := int64(c.client.ttl.Seconds())
	res, err := c.client.rdb.EvalSha(ctx, c.client.reserveSHA, []string{
		budgetKey(orgID, bucketID),
		reservationKey(requestID),
	}, estimate, ttlSec, scopedBucketID(orgID, bucketID)).Result()
	if err != nil {
		return ReserveResult{}, err
	}

	vals, ok := res.([]interface{})
	if !ok || len(vals) < 3 {
		return ReserveResult{}, fmt.Errorf("unexpected reserve_budget result: %v", res)
	}

	allowed := toInt64(vals[0]) == 1
	reserved := toInt64(vals[1])
	_ = toInt64(vals[2])

	if allowed {
		c.metrics.IncAllowed()
		return ReserveResult{Allowed: true, Reserved: reserved}, nil
	}

	c.metrics.IncDenied()
	return ReserveResult{Allowed: false}, nil
}

func toInt64(v interface{}) int64 {
	switch n := v.(type) {
	case int64:
		return n
	case int:
		return int64(n)
	default:
		return 0
	}
}
