package proxy

import (
	"context"
	"errors"
	"testing"

	"github.com/saksham/token-guard-ai/internal/budget"
)

type bridgeStub struct {
	result budget.ReserveResult
	err    error
	orgID  string
}

func (b *bridgeStub) Reserve(_ context.Context, orgID, _, _ string, _ int64) (budget.ReserveResult, error) {
	b.orgID = orgID
	return b.result, b.err
}

func TestBudgetCheckerBridgeMapsResult(t *testing.T) {
	inner := &bridgeStub{result: budget.ReserveResult{Allowed: true, Reserved: 2048}}
	bridge := NewBudgetCheckerBridge(inner)

	got, err := bridge.Reserve(context.Background(), "org-a", "b", "r", 100)
	if err != nil {
		t.Fatalf("Reserve: %v", err)
	}
	if !got.Allowed || got.Reserved != 2048 {
		t.Fatalf("result = %+v, want allowed with reserved=2048", got)
	}
	if inner.orgID != "org-a" {
		t.Fatalf("orgID = %q, want org-a", inner.orgID)
	}
}

func TestBudgetCheckerBridgePropagatesError(t *testing.T) {
	inner := &bridgeStub{err: errors.New("boom")}
	bridge := NewBudgetCheckerBridge(inner)

	_, err := bridge.Reserve(context.Background(), "default", "b", "r", 100)
	if err == nil {
		t.Fatal("expected error propagation")
	}
}
