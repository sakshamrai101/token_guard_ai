package store

import (
	"context"
	"sync"
	"time"
)

// MemoryUsageStore is an in-process UsageStore for tests and when DATABASE_URL is unset.
type MemoryUsageStore struct {
	mu     sync.Mutex
	nextID int64
	events []UsageEvent
}

func NewMemoryUsageStore() *MemoryUsageStore {
	return &MemoryUsageStore{}
}

func (s *MemoryUsageStore) InsertUsage(_ context.Context, e UsageEvent) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.nextID++
	e.ID = s.nextID
	if e.OrgID == "" {
		e.OrgID = DefaultOrgID
	}
	if e.CreatedAt.IsZero() {
		e.CreatedAt = time.Now().UTC()
	}
	s.events = append(s.events, e)
	return nil
}

func (s *MemoryUsageStore) LogUsage(ctx context.Context, e UsageEvent) error {
	return s.InsertUsage(ctx, e)
}

func (s *MemoryUsageStore) ListUsage(_ context.Context, bucketID string, limit int) ([]UsageEvent, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if limit <= 0 {
		limit = 50
	}
	var out []UsageEvent
	for i := len(s.events) - 1; i >= 0 && len(out) < limit; i-- {
		e := s.events[i]
		if bucketID != "" && e.BucketID != bucketID {
			continue
		}
		out = append(out, e)
	}
	return out, nil
}

func (s *MemoryUsageStore) ListUsageByOrg(_ context.Context, orgID string, limit int) ([]UsageEvent, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if limit <= 0 {
		limit = 50
	}
	var out []UsageEvent
	for i := len(s.events) - 1; i >= 0 && len(out) < limit; i-- {
		e := s.events[i]
		if e.OrgID != orgID {
			continue
		}
		out = append(out, e)
	}
	return out, nil
}

func (s *MemoryUsageStore) Close() error { return nil }
