package budget

import (
	"context"
	"testing"
)

func TestSetBalance(t *testing.T) {
	_, client := setupTestClient(t, 1000)

	balance, err := client.SetBalance(context.Background(), "test-bucket", 5000)
	if err != nil {
		t.Fatalf("SetBalance: %v", err)
	}
	if balance != 5000 {
		t.Fatalf("balance = %d, want 5000", balance)
	}

	got, err := client.GetBalance(context.Background(), "test-bucket")
	if err != nil {
		t.Fatalf("GetBalance: %v", err)
	}
	if got != 5000 {
		t.Fatalf("stored balance = %d, want 5000", got)
	}
}

func TestTopupBalance(t *testing.T) {
	_, client := setupTestClient(t, 1000)

	balance, err := client.TopupBalance(context.Background(), "test-bucket", 250)
	if err != nil {
		t.Fatalf("TopupBalance: %v", err)
	}
	if balance != 1250 {
		t.Fatalf("balance = %d, want 1250", balance)
	}
}

func TestGetBalanceMissingBucketReturnsZero(t *testing.T) {
	_, client := setupTestClient(t, 0)

	balance, err := client.GetBalance(context.Background(), "missing-bucket")
	if err != nil {
		t.Fatalf("GetBalance: %v", err)
	}
	if balance != 0 {
		t.Fatalf("balance = %d, want 0", balance)
	}
}

func TestTopupBalanceRejectsZeroAmount(t *testing.T) {
	_, client := setupTestClient(t, 1000)

	_, err := client.TopupBalance(context.Background(), "test-bucket", 0)
	if err == nil {
		t.Fatal("expected error for zero topup amount")
	}
}
