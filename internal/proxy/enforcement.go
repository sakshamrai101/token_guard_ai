package proxy

import (
	"context"
	"log/slog"
	"time"

	"github.com/saksham/token-guard-ai/internal/config"
)

type PreCheckResult struct {
	Allowed  bool
	FailOpen bool
	Reserved int64
}


type BudgetChecker interface {
	Reserve(ctx context.Context, bucketID, requestID string, estimate int64) (PreCheckResult, error)
}


type noopChecker struct{}

func (noopChecker) Reserve(_ context.Context, _, _ string, _ int64) (PreCheckResult, error) {
	return PreCheckResult{Allowed: true}, nil
}

type Enforcement struct {
	mode    config.EnforcementMode
	checker BudgetChecker
	timeout time.Duration
	logger  *slog.Logger
}

func NewEnforcement(cfg config.Config, checker BudgetChecker, logger *slog.Logger) *Enforcement {
	if checker == nil {
		checker = noopChecker{}
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &Enforcement{
		mode:    cfg.EnforcementMode,
		checker: checker,
		timeout: cfg.PreCheckTimeout,
		logger:  logger,
	}
}

func (e *Enforcement) PreCheck(ctx context.Context, bucketID, requestID string, estimate int64) PreCheckResult {
	if e.mode == config.EnforcementOff {
		return PreCheckResult{Allowed: true}
	}

	checkCtx, cancel := context.WithTimeout(ctx, e.timeout)
	defer cancel()

	result, err := e.checker.Reserve(checkCtx, bucketID, requestID, estimate)
	if err != nil {
		e.logger.Warn("budget pre-check failed, fail-open forward",
			"request_id", requestID,
			"bucket_id", bucketID,
			"error", err,
		)
		return PreCheckResult{Allowed: true, FailOpen: true}
	}

	if checkCtx.Err() == context.DeadlineExceeded {
		e.logger.Warn("budget pre-check timed out, fail-open forward",
			"request_id", requestID,
			"bucket_id", bucketID,
		)
		return PreCheckResult{Allowed: true, FailOpen: true}
	}

	if !result.Allowed && e.mode == config.EnforcementEnforce {
		return PreCheckResult{Allowed: false, Reserved: result.Reserved}
	}

	return PreCheckResult{Allowed: true, FailOpen: result.FailOpen, Reserved: result.Reserved}
}
