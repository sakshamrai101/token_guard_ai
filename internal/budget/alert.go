package budget

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/redis/go-redis/v9"
)

// WarningDedupe claims a one-shot warning slot per org+bucket.
type WarningDedupe interface {
	// TryClaim returns true if this caller should send the warning.
	TryClaim(ctx context.Context, orgID, bucketID string) (bool, error)
}

type Alerter struct {
	webhookURL string
	client     *http.Client
	logger     *slog.Logger
	dedupe     WarningDedupe
}

func NewAlerter(webhookURL string, logger *slog.Logger) *Alerter {
	if logger == nil {
		logger = slog.Default()
	}
	return &Alerter{
		webhookURL: webhookURL,
		client:     &http.Client{Timeout: 5 * time.Second},
		logger:     logger,
	}
}

func (a *Alerter) WithDedupe(d WarningDedupe) *Alerter {
	a.dedupe = d
	return a
}

func (a *Alerter) resolveWebhook(orgWebhook string) string {
	if orgWebhook != "" {
		return orgWebhook
	}
	return a.webhookURL
}

// FailOpen keeps the Day-2 call site (global webhook only).
func (a *Alerter) FailOpen(ctx context.Context, requestID, bucketID, detail string) {
	a.FailOpenAt(ctx, "", "", requestID, bucketID, detail)
}

// FailOpenAt posts fail_open; orgWebhook preferred over global when set.
func (a *Alerter) FailOpenAt(ctx context.Context, orgWebhook, orgID, requestID, bucketID, detail string) {
	msg := fmt.Sprintf("CRITICAL fail_open: request_id=%s org_id=%s bucket_id=%s %s", requestID, orgID, bucketID, detail)
	a.logger.Error(msg)
	a.postSlack(ctx, a.resolveWebhook(orgWebhook), msg)
}

// BudgetDenied keeps the Day-2 call site (global webhook, no org_id).
func (a *Alerter) BudgetDenied(ctx context.Context, requestID, bucketID string, estimate int64) {
	a.BudgetExhausted(ctx, "", "", bucketID, requestID, estimate)
}

// BudgetExhausted posts budget_exhausted on reserve denied in enforce.
func (a *Alerter) BudgetExhausted(ctx context.Context, orgWebhook, orgID, bucketID, requestID string, estimate int64) {
	msg := fmt.Sprintf("WARN budget_exhausted: request_id=%s org_id=%s bucket_id=%s estimate=%d", requestID, orgID, bucketID, estimate)
	a.logger.Warn(msg)
	a.postSlack(ctx, a.resolveWebhook(orgWebhook), msg)
}

// ShouldWarn80 is true when remaining ≤ 20% of (remaining + actual).
func ShouldWarn80(remaining, actual int64) bool {
	if actual < 0 {
		actual = 0
	}
	base := remaining + actual
	if base <= 0 {
		return true
	}
	return remaining*5 <= base
}

// MaybeBudgetWarning80 posts budget_warning_80 once per org+bucket per dedupe TTL when under threshold.
func (a *Alerter) MaybeBudgetWarning80(ctx context.Context, orgWebhook, orgID, bucketID string, remaining, actual int64) {
	if !ShouldWarn80(remaining, actual) {
		return
	}
	if a.dedupe != nil {
		ok, err := a.dedupe.TryClaim(ctx, orgID, bucketID)
		if err != nil {
			a.logger.Error("warning dedupe failed; sending alert", "error", err, "org_id", orgID, "bucket_id", bucketID)
		} else if !ok {
			return
		}
	}
	msg := fmt.Sprintf("WARN budget_warning_80: org_id=%s bucket_id=%s remaining=%d actual=%d", orgID, bucketID, remaining, actual)
	a.logger.Warn(msg)
	a.postSlack(ctx, a.resolveWebhook(orgWebhook), msg)
}

func (a *Alerter) postSlack(ctx context.Context, webhookURL, text string) {
	if webhookURL == "" {
		return
	}
	payload, err := json.Marshal(map[string]string{"text": text})
	if err != nil {
		a.logger.Error("failed to marshal slack payload", "error", err)
		return
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, webhookURL, bytes.NewReader(payload))
	if err != nil {
		a.logger.Error("failed to create slack request", "error", err)
		return
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := a.client.Do(req)
	if err != nil {
		a.logger.Error("failed to post slack alert", "error", err)
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		a.logger.Error("slack webhook returned error", "status", resp.StatusCode)
	}
}

// MemoryWarningDedupe is an in-process dedupe for tests.
type MemoryWarningDedupe struct {
	mu  sync.Mutex
	ttl time.Duration
	m   map[string]time.Time
}

func NewMemoryWarningDedupe(ttl time.Duration) *MemoryWarningDedupe {
	if ttl <= 0 {
		ttl = time.Hour
	}
	return &MemoryWarningDedupe{ttl: ttl, m: make(map[string]time.Time)}
}

func (d *MemoryWarningDedupe) TryClaim(_ context.Context, orgID, bucketID string) (bool, error) {
	key := orgID + ":" + bucketID
	now := time.Now()
	d.mu.Lock()
	defer d.mu.Unlock()
	if exp, ok := d.m[key]; ok && now.Before(exp) {
		return false, nil
	}
	d.m[key] = now.Add(d.ttl)
	return true, nil
}

// RedisWarningDedupe uses SET NX EX for once-per-hour warnings.
type RedisWarningDedupe struct {
	rdb redis.Cmdable
	ttl time.Duration
}

func NewRedisWarningDedupe(rdb redis.Cmdable, ttl time.Duration) *RedisWarningDedupe {
	if ttl <= 0 {
		ttl = time.Hour
	}
	return &RedisWarningDedupe{rdb: rdb, ttl: ttl}
}

func warn80Key(orgID, bucketID string) string {
	return fmt.Sprintf("alert:warn80:%s:%s", orgID, bucketID)
}

func (d *RedisWarningDedupe) TryClaim(ctx context.Context, orgID, bucketID string) (bool, error) {
	ok, err := d.rdb.SetNX(ctx, warn80Key(orgID, bucketID), "1", d.ttl).Result()
	if err != nil {
		return false, err
	}
	return ok, nil
}
