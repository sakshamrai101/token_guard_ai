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
}

func (b bridgeStub) Reserve(_ context.Context, _, _ string, _ int64) (budget.ReserveResult, error) {
	return b.result, b.err
}

func TestBudgetCheckerBridgeMapsResult(t *testing.T) {
	inner := bridgeStub{result: budget.ReserveResult{Allowed: true, Reserved: 2048}}
	bridge := NewBudgetCheckerBridge(inner)

	got, err := bridge.Reserve(context.Background(), "b", "r", 100)
	if err != nil {
		t.Fatalf("Reserve: %v", err)
	}
	if !got.Allowed || got.Reserved != 2048 {
		t.Fatalf("result = %+v, want allowed with reserved=2048", got)
	}
}

func TestBudgetCheckerBridgePropagatesError(t *testing.T) {
	inner := bridgeStub{err: errors.New("boom")}
	bridge := NewBudgetCheckerBridge(inner)

	_, err := bridge.Reserve(context.Background(), "b", "r", 100)
	if err == nil {
		t.Fatal("expected error propagation")
	}
}
