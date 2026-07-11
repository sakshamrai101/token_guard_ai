package store

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"time"
)

var (
	ErrNotFound      = errors.New("not found")
	ErrKeyRevoked    = errors.New("api key revoked")
	ErrInvalidAPIKey = errors.New("invalid api key")
)

type Org struct {
	ID               string    `json:"id"`
	Name             string    `json:"name"`
	Plan             string    `json:"plan"`
	SlackWebhookURL  string    `json:"slack_webhook_url,omitempty"`
	DefaultBucketID  string    `json:"default_bucket_id,omitempty"`
	CreatedAt        time.Time `json:"created_at"`
}

type APIKey struct {
	ID        string     `json:"id"`
	OrgID     string     `json:"org_id"`
	KeyHash   string     `json:"-"`
	KeyPrefix string     `json:"key_prefix"`
	RevokedAt *time.Time `json:"revoked_at,omitempty"`
	CreatedAt time.Time  `json:"created_at"`
}

// AuthResult is returned after a successful TokenGuard key lookup.
type AuthResult struct {
	OrgID           string
	Plan            string
	KeyID           string
	KeyPrefix       string
	SlackWebhookURL string
}

// OrgStore manages orgs, API keys, and bucket registry.
type OrgStore interface {
	CreateOrg(ctx context.Context, name string) (Org, error)
	ListOrgs(ctx context.Context) ([]Org, error)
	GetOrg(ctx context.Context, orgID string) (Org, error)
	UpdateOrgSlackWebhook(ctx context.Context, orgID, webhookURL string) (Org, error)
	CreateAPIKey(ctx context.Context, orgID string) (rawKey string, key APIKey, err error)
	LookupAPIKey(ctx context.Context, rawKey string) (AuthResult, error)
	UpsertBucket(ctx context.Context, orgID, bucketID string) error
}

func HashAPIKey(raw string) string {
	sum := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(sum[:])
}

func GenerateAPIKey() (raw string, prefix string, hash string, err error) {
	buf := make([]byte, 24)
	if _, err = rand.Read(buf); err != nil {
		return "", "", "", fmt.Errorf("generate api key: %w", err)
	}
	raw = "tg_" + hex.EncodeToString(buf)
	prefix = raw
	if len(prefix) > 11 {
		prefix = prefix[:11] // tg_ + 8 hex chars
	}
	hash = HashAPIKey(raw)
	return raw, prefix, hash, nil
}

func newID(prefix string) (string, error) {
	buf := make([]byte, 12)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return prefix + hex.EncodeToString(buf), nil
}
