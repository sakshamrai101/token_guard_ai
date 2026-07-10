package store

import (
	"context"
	"testing"
)

func TestMemoryUsageStoreInsertAndList(t *testing.T) {
	s := NewMemoryUsageStore()
	ctx := context.Background()

	err := s.InsertUsage(ctx, UsageEvent{
		BucketID:  "b1",
		RequestID: "req-1",
		Reserved:  100,
		Actual:    80,
		Outcome:   "settled",
		Provider:  "api.openai.com",
	})
	if err != nil {
		t.Fatalf("InsertUsage: %v", err)
	}
	_ = s.InsertUsage(ctx, UsageEvent{
		BucketID:  "b2",
		RequestID: "req-2",
		Reserved:  50,
		Actual:    50,
		Outcome:   "released",
		Provider:  "api.openai.com",
	})

	all, err := s.ListUsage(ctx, "", 10)
	if err != nil {
		t.Fatalf("ListUsage: %v", err)
	}
	if len(all) != 2 {
		t.Fatalf("len = %d, want 2", len(all))
	}
	if all[0].RequestID != "req-2" {
		t.Fatalf("newest first: got %s, want req-2", all[0].RequestID)
	}
	if all[0].OrgID != DefaultOrgID {
		t.Fatalf("org_id = %q, want %q", all[0].OrgID, DefaultOrgID)
	}

	filtered, err := s.ListUsage(ctx, "b1", 10)
	if err != nil {
		t.Fatalf("ListUsage filter: %v", err)
	}
	if len(filtered) != 1 || filtered[0].RequestID != "req-1" {
		t.Fatalf("filtered = %+v, want req-1 only", filtered)
	}
}

func TestMemoryUsageStoreRespectsLimit(t *testing.T) {
	s := NewMemoryUsageStore()
	ctx := context.Background()
	for i := 0; i < 5; i++ {
		_ = s.InsertUsage(ctx, UsageEvent{RequestID: "r", BucketID: "b", Outcome: "settled"})
	}
	got, err := s.ListUsage(ctx, "", 2)
	if err != nil {
		t.Fatalf("ListUsage: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("len = %d, want 2", len(got))
	}
}
