package store

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
)

// PostgresUsageStore persists usage_events in Postgres.
type PostgresUsageStore struct {
	db *sql.DB
}

func OpenPostgres(databaseURL string) (*PostgresUsageStore, error) {
	db, err := sql.Open("pgx", databaseURL)
	if err != nil {
		return nil, fmt.Errorf("open postgres: %w", err)
	}
	db.SetMaxOpenConns(10)
	db.SetMaxIdleConns(5)
	db.SetConnMaxLifetime(30 * time.Minute)

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("ping postgres: %w", err)
	}

	s := &PostgresUsageStore{db: db}
	if err := s.EnsureSchema(ctx); err != nil {
		_ = db.Close()
		return nil, err
	}
	if err := s.EnsureOrgSchema(ctx); err != nil {
		_ = db.Close()
		return nil, err
	}
	return s, nil
}

func (s *PostgresUsageStore) EnsureSchema(ctx context.Context) error {
	const q = `
CREATE TABLE IF NOT EXISTS usage_events (
    id BIGSERIAL PRIMARY KEY,
    org_id TEXT NOT NULL DEFAULT 'default',
    bucket_id TEXT NOT NULL DEFAULT '',
    request_id TEXT NOT NULL,
    reserved BIGINT NOT NULL DEFAULT 0,
    actual BIGINT NOT NULL DEFAULT 0,
    outcome TEXT NOT NULL,
    provider TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS usage_events_bucket_created_idx
    ON usage_events (bucket_id, created_at DESC);
CREATE INDEX IF NOT EXISTS usage_events_org_created_idx
    ON usage_events (org_id, created_at DESC);
CREATE INDEX IF NOT EXISTS usage_events_request_id_idx
    ON usage_events (request_id);
`
	_, err := s.db.ExecContext(ctx, q)
	if err != nil {
		return fmt.Errorf("ensure usage_events schema: %w", err)
	}
	return nil
}

func (s *PostgresUsageStore) InsertUsage(ctx context.Context, e UsageEvent) error {
	if e.OrgID == "" {
		e.OrgID = DefaultOrgID
	}
	if e.CreatedAt.IsZero() {
		e.CreatedAt = time.Now().UTC()
	}
	_, err := s.db.ExecContext(ctx, `
INSERT INTO usage_events (org_id, bucket_id, request_id, reserved, actual, outcome, provider, created_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
`, e.OrgID, e.BucketID, e.RequestID, e.Reserved, e.Actual, e.Outcome, e.Provider, e.CreatedAt)
	if err != nil {
		return fmt.Errorf("insert usage_event: %w", err)
	}
	return nil
}

func (s *PostgresUsageStore) LogUsage(ctx context.Context, e UsageEvent) error {
	return s.InsertUsage(ctx, e)
}

func (s *PostgresUsageStore) ListUsage(ctx context.Context, bucketID string, limit int) ([]UsageEvent, error) {
	if limit <= 0 {
		limit = 50
	}
	var (
		rows *sql.Rows
		err  error
	)
	if bucketID == "" {
		rows, err = s.db.QueryContext(ctx, `
SELECT id, org_id, bucket_id, request_id, reserved, actual, outcome, provider, created_at
FROM usage_events
ORDER BY created_at DESC, id DESC
LIMIT $1
`, limit)
	} else {
		rows, err = s.db.QueryContext(ctx, `
SELECT id, org_id, bucket_id, request_id, reserved, actual, outcome, provider, created_at
FROM usage_events
WHERE bucket_id = $1
ORDER BY created_at DESC, id DESC
LIMIT $2
`, bucketID, limit)
	}
	if err != nil {
		return nil, fmt.Errorf("list usage_events: %w", err)
	}
	defer rows.Close()

	var out []UsageEvent
	for rows.Next() {
		var e UsageEvent
		if err := rows.Scan(
			&e.ID, &e.OrgID, &e.BucketID, &e.RequestID,
			&e.Reserved, &e.Actual, &e.Outcome, &e.Provider, &e.CreatedAt,
		); err != nil {
			return nil, fmt.Errorf("scan usage_event: %w", err)
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

func (s *PostgresUsageStore) Close() error {
	if s.db == nil {
		return nil
	}
	return s.db.Close()
}
