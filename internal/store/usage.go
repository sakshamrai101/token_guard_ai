package store

import (
	"context"
	"time"
)

const DefaultOrgID = "default"

// UsageEvent is a durable record of a settle or release.
type UsageEvent struct {
	ID        int64     `json:"id"`
	OrgID     string    `json:"org_id"`
	BucketID  string    `json:"bucket_id"`
	RequestID string    `json:"request_id"`
	Reserved  int64     `json:"reserved"`
	Actual    int64     `json:"actual"`
	Outcome   string    `json:"outcome"`
	Provider  string    `json:"provider"`
	CreatedAt time.Time `json:"created_at"`
}

// UsageStore persists and queries usage events.
type UsageStore interface {
	InsertUsage(ctx context.Context, e UsageEvent) error
	ListUsage(ctx context.Context, bucketID string, limit int) ([]UsageEvent, error)
	ListUsageByOrg(ctx context.Context, orgID string, limit int) ([]UsageEvent, error)
	Close() error
}

// UsageLogger is the hot-path write interface used by the proxy.
type UsageLogger interface {
	LogUsage(ctx context.Context, e UsageEvent) error
}
