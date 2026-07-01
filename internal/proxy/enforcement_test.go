package proxy

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/saksham/token-guard-ai/internal/config"
)

type stubChecker struct {
	result PreCheckResult
	err    error
	delay  time.Duration
}

func (s stubChecker) Reserve(ctx context.Context, _, _ string, _ int64) (PreCheckResult, error) {
	if s.delay > 0 {
		select {
		case <-time.After(s.delay):
		case <-ctx.Done():
			return PreCheckResult{}, ctx.Err()
		}
	}
	return s.result, s.err
}

func TestEnforcementOffSkipsCheck(t *testing.T) {
	e := NewEnforcement(config.Config{EnforcementMode: config.EnforcementOff}, stubChecker{
		result: PreCheckResult{Allowed: false},
	}, nil)

	result := e.PreCheck(context.Background(), "bucket", "req", 100)
	if !result.Allowed || result.FailOpen {
		t.Fatalf("off mode should allow without fail-open: %+v", result)
	}
}

func TestEnforcementFailOpenOnError(t *testing.T) {
	e := NewEnforcement(config.Config{
		EnforcementMode: config.EnforcementEnforce,
		PreCheckTimeout: 50 * time.Millisecond,
	}, stubChecker{err: errors.New("redis down")}, nil)

	result := e.PreCheck(context.Background(), "bucket", "req", 100)
	if !result.Allowed || !result.FailOpen {
		t.Fatalf("expected fail-open on checker error, got %+v", result)
	}
}

func TestEnforcementFailOpenOnTimeout(t *testing.T) {
	e := NewEnforcement(config.Config{
		EnforcementMode: config.EnforcementEnforce,
		PreCheckTimeout: 10 * time.Millisecond,
	}, stubChecker{delay: 100 * time.Millisecond}, nil)

	result := e.PreCheck(context.Background(), "bucket", "req", 100)
	if !result.Allowed || !result.FailOpen {
		t.Fatalf("expected fail-open on timeout, got %+v", result)
	}
}

func TestEnforcementDeniesWhenExhausted(t *testing.T) {
	e := NewEnforcement(config.Config{
		EnforcementMode: config.EnforcementEnforce,
		PreCheckTimeout: 50 * time.Millisecond,
	}, stubChecker{result: PreCheckResult{Allowed: false}}, nil)

	result := e.PreCheck(context.Background(), "bucket", "req", 100)
	if result.Allowed {
		t.Fatal("expected deny in enforce mode when budget exhausted")
	}
}

func TestEnforcementShadowAllowsWhenDenied(t *testing.T) {
	e := NewEnforcement(config.Config{
		EnforcementMode: config.EnforcementShadow,
		PreCheckTimeout: 50 * time.Millisecond,
	}, stubChecker{result: PreCheckResult{Allowed: false}}, nil)

	result := e.PreCheck(context.Background(), "bucket", "req", 100)
	if !result.Allowed {
		t.Fatal("shadow mode should forward even when check denies")
	}
}
