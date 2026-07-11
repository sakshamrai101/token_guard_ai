package budget

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// BucketBalance is a Redis budget key and its remaining balance.
type BucketBalance struct {
	OrgID    string `json:"org_id"`
	BucketID string `json:"bucket_id"`
	Balance  int64  `json:"balance"`
}

// ReservationHold is an unsettled reservation.
type ReservationHold struct {
	RequestID string    `json:"request_id"`
	OrgID     string    `json:"org_id"`
	BucketID  string    `json:"bucket_id"`
	Reserved  int64     `json:"reserved"`
	CreatedAt time.Time `json:"created_at,omitempty"`
}

func (c *Client) ListBuckets(ctx context.Context) ([]BucketBalance, error) {
	var (
		cursor uint64
		out    []BucketBalance
	)
	for {
		keys, next, err := c.rdb.Scan(ctx, cursor, "budget:*", 100).Result()
		if err != nil {
			return nil, fmt.Errorf("scan budget keys: %w", err)
		}
		for _, key := range keys {
			rest := strings.TrimPrefix(key, "budget:")
			orgID, bucketID := ParseScopedBucket(rest)
			bal, err := c.rdb.Get(ctx, key).Int64()
			if err != nil {
				continue
			}
			out = append(out, BucketBalance{OrgID: orgID, BucketID: bucketID, Balance: bal})
		}
		cursor = next
		if cursor == 0 {
			break
		}
	}
	return out, nil
}

func (c *Client) ListReservations(ctx context.Context) ([]ReservationHold, error) {
	var (
		cursor uint64
		out    []ReservationHold
	)
	for {
		keys, next, err := c.rdb.Scan(ctx, cursor, "reservation:*", 100).Result()
		if err != nil {
			return nil, fmt.Errorf("scan reservation keys: %w", err)
		}
		for _, key := range keys {
			vals, err := c.rdb.HGetAll(ctx, key).Result()
			if err != nil || len(vals) == 0 {
				continue
			}
			reserved, _ := strconv.ParseInt(vals["reserved"], 10, 64)
			orgID, bucketID := ParseScopedBucket(vals["bucket_id"])
			hold := ReservationHold{
				RequestID: strings.TrimPrefix(key, "reservation:"),
				OrgID:     orgID,
				BucketID:  bucketID,
				Reserved:  reserved,
			}
			if ts, err := strconv.ParseInt(vals["created_at"], 10, 64); err == nil && ts > 0 {
				hold.CreatedAt = time.Unix(ts, 0).UTC()
			}
			out = append(out, hold)
		}
		cursor = next
		if cursor == 0 {
			break
		}
	}
	return out, nil
}
