package budget

import (
	"context"
	"testing"
)

func TestListBucketsAndReservations(t *testing.T) {
	_, client := setupTestClient(t, 5000)
	ctx := context.Background()

	checker := NewRedisBudgetChecker(client, nil)
	if _, err := checker.Reserve(ctx, "test-bucket", "req-list-1", 100); err != nil {
		t.Fatalf("Reserve: %v", err)
	}
	if _, err := client.SetBalance(ctx, "other-bucket", 200); err != nil {
		t.Fatalf("SetBalance: %v", err)
	}

	buckets, err := client.ListBuckets(ctx)
	if err != nil {
		t.Fatalf("ListBuckets: %v", err)
	}
	if len(buckets) < 2 {
		t.Fatalf("buckets = %+v, want at least 2", buckets)
	}

	holds, err := client.ListReservations(ctx)
	if err != nil {
		t.Fatalf("ListReservations: %v", err)
	}
	if len(holds) != 1 {
		t.Fatalf("holds = %+v, want 1", holds)
	}
	if holds[0].RequestID != "req-list-1" || holds[0].BucketID != "test-bucket" || holds[0].Reserved != 100 {
		t.Fatalf("hold = %+v", holds[0])
	}
}
