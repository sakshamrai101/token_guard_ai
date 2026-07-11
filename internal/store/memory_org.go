package store

import (
	"context"
	"sync"
	"time"
)

// MemoryOrgStore is an in-process OrgStore for tests and local multi-tenant without Postgres.
type MemoryOrgStore struct {
	mu      sync.Mutex
	orgs    map[string]Org
	keys    map[string]APIKey // keyed by hash
	buckets map[string]struct{}
}

func NewMemoryOrgStore() *MemoryOrgStore {
	return &MemoryOrgStore{
		orgs:    make(map[string]Org),
		keys:    make(map[string]APIKey),
		buckets: make(map[string]struct{}),
	}
}

func (s *MemoryOrgStore) CreateOrg(_ context.Context, name string) (Org, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	id, err := newID("org_")
	if err != nil {
		return Org{}, err
	}
	org := Org{
		ID:        id,
		Name:      name,
		Plan:      "trial",
		CreatedAt: time.Now().UTC(),
	}
	s.orgs[id] = org
	return org, nil
}

func (s *MemoryOrgStore) ListOrgs(_ context.Context) ([]Org, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Org, 0, len(s.orgs))
	for _, o := range s.orgs {
		out = append(out, o)
	}
	return out, nil
}

func (s *MemoryOrgStore) GetOrg(_ context.Context, orgID string) (Org, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	o, ok := s.orgs[orgID]
	if !ok {
		return Org{}, ErrNotFound
	}
	return o, nil
}

func (s *MemoryOrgStore) UpdateOrgSlackWebhook(_ context.Context, orgID, webhookURL string) (Org, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	o, ok := s.orgs[orgID]
	if !ok {
		return Org{}, ErrNotFound
	}
	o.SlackWebhookURL = webhookURL
	s.orgs[orgID] = o
	return o, nil
}

func (s *MemoryOrgStore) CreateAPIKey(_ context.Context, orgID string) (string, APIKey, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if _, ok := s.orgs[orgID]; !ok {
		return "", APIKey{}, ErrNotFound
	}
	raw, prefix, hash, err := GenerateAPIKey()
	if err != nil {
		return "", APIKey{}, err
	}
	id, err := newID("key_")
	if err != nil {
		return "", APIKey{}, err
	}
	key := APIKey{
		ID:        id,
		OrgID:     orgID,
		KeyHash:   hash,
		KeyPrefix: prefix,
		CreatedAt: time.Now().UTC(),
	}
	s.keys[hash] = key
	return raw, key, nil
}

func (s *MemoryOrgStore) LookupAPIKey(_ context.Context, rawKey string) (AuthResult, error) {
	if rawKey == "" || len(rawKey) < 4 || rawKey[:3] != "tg_" {
		return AuthResult{}, ErrInvalidAPIKey
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	key, ok := s.keys[HashAPIKey(rawKey)]
	if !ok {
		return AuthResult{}, ErrInvalidAPIKey
	}
	if key.RevokedAt != nil {
		return AuthResult{}, ErrKeyRevoked
	}
	org, ok := s.orgs[key.OrgID]
	if !ok {
		return AuthResult{}, ErrInvalidAPIKey
	}
	return AuthResult{
		OrgID:           key.OrgID,
		Plan:            org.Plan,
		KeyID:           key.ID,
		KeyPrefix:       key.KeyPrefix,
		SlackWebhookURL: org.SlackWebhookURL,
	}, nil
}

func (s *MemoryOrgStore) UpsertBucket(_ context.Context, orgID, bucketID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.buckets[orgID+":"+bucketID] = struct{}{}
	return nil
}
