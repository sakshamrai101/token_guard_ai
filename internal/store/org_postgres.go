package store

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

func (s *PostgresUsageStore) EnsureOrgSchema(ctx context.Context) error {
	const q = `
CREATE TABLE IF NOT EXISTS orgs (
    id TEXT PRIMARY KEY,
    name TEXT NOT NULL,
    plan TEXT NOT NULL DEFAULT 'trial',
    slack_webhook_url TEXT NOT NULL DEFAULT '',
    default_bucket_id TEXT NOT NULL DEFAULT '',
    stripe_customer_id TEXT NOT NULL DEFAULT '',
    stripe_subscription_id TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE TABLE IF NOT EXISTS api_keys (
    id TEXT PRIMARY KEY,
    org_id TEXT NOT NULL REFERENCES orgs(id),
    key_hash TEXT NOT NULL UNIQUE,
    key_prefix TEXT NOT NULL,
    revoked_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS api_keys_org_id_idx ON api_keys (org_id);

CREATE TABLE IF NOT EXISTS buckets (
    org_id TEXT NOT NULL,
    bucket_id TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (org_id, bucket_id)
);
`
	if _, err := s.db.ExecContext(ctx, q); err != nil {
		return fmt.Errorf("ensure org schema: %w", err)
	}
	// Migrate existing installs that predate Stripe columns.
	alters := []string{
		`ALTER TABLE orgs ADD COLUMN IF NOT EXISTS stripe_customer_id TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE orgs ADD COLUMN IF NOT EXISTS stripe_subscription_id TEXT NOT NULL DEFAULT ''`,
		`CREATE INDEX IF NOT EXISTS orgs_stripe_subscription_id_idx ON orgs (stripe_subscription_id)`,
	}
	for _, stmt := range alters {
		if _, err := s.db.ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf("migrate org stripe columns: %w", err)
		}
	}
	return nil
}

func (s *PostgresUsageStore) CreateOrg(ctx context.Context, name string) (Org, error) {
	id, err := newID("org_")
	if err != nil {
		return Org{}, err
	}
	now := time.Now().UTC()
	_, err = s.db.ExecContext(ctx, `
INSERT INTO orgs (id, name, plan, created_at) VALUES ($1, $2, 'trial', $3)
`, id, name, now)
	if err != nil {
		return Org{}, fmt.Errorf("insert org: %w", err)
	}
	return Org{ID: id, Name: name, Plan: "trial", CreatedAt: now}, nil
}

func (s *PostgresUsageStore) ListOrgs(ctx context.Context) ([]Org, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT id, name, plan, slack_webhook_url, default_bucket_id,
       stripe_customer_id, stripe_subscription_id, created_at
FROM orgs ORDER BY created_at ASC
`)
	if err != nil {
		return nil, fmt.Errorf("list orgs: %w", err)
	}
	defer rows.Close()
	var out []Org
	for rows.Next() {
		var o Org
		if err := rows.Scan(&o.ID, &o.Name, &o.Plan, &o.SlackWebhookURL, &o.DefaultBucketID,
			&o.StripeCustomerID, &o.StripeSubscriptionID, &o.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, o)
	}
	return out, rows.Err()
}

func (s *PostgresUsageStore) GetOrg(ctx context.Context, orgID string) (Org, error) {
	var o Org
	err := s.db.QueryRowContext(ctx, `
SELECT id, name, plan, slack_webhook_url, default_bucket_id,
       stripe_customer_id, stripe_subscription_id, created_at
FROM orgs WHERE id = $1
`, orgID).Scan(&o.ID, &o.Name, &o.Plan, &o.SlackWebhookURL, &o.DefaultBucketID,
		&o.StripeCustomerID, &o.StripeSubscriptionID, &o.CreatedAt)
	if err == sql.ErrNoRows {
		return Org{}, ErrNotFound
	}
	if err != nil {
		return Org{}, err
	}
	return o, nil
}

func (s *PostgresUsageStore) UpdateOrgSlackWebhook(ctx context.Context, orgID, webhookURL string) (Org, error) {
	res, err := s.db.ExecContext(ctx, `
UPDATE orgs SET slack_webhook_url = $2 WHERE id = $1
`, orgID, webhookURL)
	if err != nil {
		return Org{}, fmt.Errorf("update org slack webhook: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return Org{}, ErrNotFound
	}
	return s.GetOrg(ctx, orgID)
}

func (s *PostgresUsageStore) ApplyCheckoutCompleted(ctx context.Context, orgID, plan, customerID, subscriptionID string) error {
	res, err := s.db.ExecContext(ctx, `
UPDATE orgs
SET plan = $2, stripe_customer_id = $3, stripe_subscription_id = $4
WHERE id = $1
`, orgID, plan, customerID, subscriptionID)
	if err != nil {
		return fmt.Errorf("apply checkout: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *PostgresUsageStore) DowngradeBySubscription(ctx context.Context, subscriptionID string) error {
	res, err := s.db.ExecContext(ctx, `
UPDATE orgs
SET plan = 'trial', stripe_subscription_id = ''
WHERE stripe_subscription_id = $1
`, subscriptionID)
	if err != nil {
		return fmt.Errorf("downgrade subscription: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *PostgresUsageStore) CreateAPIKey(ctx context.Context, orgID string) (string, APIKey, error) {
	if _, err := s.GetOrg(ctx, orgID); err != nil {
		return "", APIKey{}, err
	}
	raw, prefix, hash, err := GenerateAPIKey()
	if err != nil {
		return "", APIKey{}, err
	}
	id, err := newID("key_")
	if err != nil {
		return "", APIKey{}, err
	}
	now := time.Now().UTC()
	_, err = s.db.ExecContext(ctx, `
INSERT INTO api_keys (id, org_id, key_hash, key_prefix, created_at)
VALUES ($1, $2, $3, $4, $5)
`, id, orgID, hash, prefix, now)
	if err != nil {
		return "", APIKey{}, fmt.Errorf("insert api key: %w", err)
	}
	return raw, APIKey{
		ID:        id,
		OrgID:     orgID,
		KeyHash:   hash,
		KeyPrefix: prefix,
		CreatedAt: now,
	}, nil
}

func (s *PostgresUsageStore) LookupAPIKey(ctx context.Context, rawKey string) (AuthResult, error) {
	if rawKey == "" || len(rawKey) < 4 || rawKey[:3] != "tg_" {
		return AuthResult{}, ErrInvalidAPIKey
	}
	hash := HashAPIKey(rawKey)
	var (
		keyID, orgID, prefix, plan string
		revoked                    sql.NullTime
	)
	var slackURL string
	err := s.db.QueryRowContext(ctx, `
SELECT k.id, k.org_id, k.key_prefix, k.revoked_at, o.plan, o.slack_webhook_url
FROM api_keys k
JOIN orgs o ON o.id = k.org_id
WHERE k.key_hash = $1
`, hash).Scan(&keyID, &orgID, &prefix, &revoked, &plan, &slackURL)
	if err == sql.ErrNoRows {
		return AuthResult{}, ErrInvalidAPIKey
	}
	if err != nil {
		return AuthResult{}, err
	}
	if revoked.Valid {
		return AuthResult{}, ErrKeyRevoked
	}
	return AuthResult{
		OrgID:           orgID,
		Plan:            plan,
		KeyID:           keyID,
		KeyPrefix:       prefix,
		SlackWebhookURL: slackURL,
	}, nil
}

func (s *PostgresUsageStore) UpsertBucket(ctx context.Context, orgID, bucketID string) error {
	_, err := s.db.ExecContext(ctx, `
INSERT INTO buckets (org_id, bucket_id, created_at)
VALUES ($1, $2, NOW())
ON CONFLICT (org_id, bucket_id) DO NOTHING
`, orgID, bucketID)
	if err != nil {
		return fmt.Errorf("upsert bucket: %w", err)
	}
	return nil
}
