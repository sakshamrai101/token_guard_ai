package store

import (
	"context"
	"strings"
	"sync"
	"time"
)

// MemoryOrgStore is an in-process OrgStore for tests and local multi-tenant without Postgres.
type MemoryOrgStore struct {
	mu      sync.Mutex
	orgs    map[string]Org
	byEmail map[string]string // email → org id
	keys    map[string]APIKey // keyed by hash
	buckets map[string]struct{}
}

func NewMemoryOrgStore() *MemoryOrgStore {
	return &MemoryOrgStore{
		orgs:    make(map[string]Org),
		byEmail: make(map[string]string),
		keys:    make(map[string]APIKey),
		buckets: make(map[string]struct{}),
	}
}

func (s *MemoryOrgStore) CreateOrg(ctx context.Context, name string) (Org, error) {
	return s.CreateOrgWithEmail(ctx, name, "")
}

func (s *MemoryOrgStore) CreateOrgWithEmail(_ context.Context, name, email string) (Org, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	email = strings.ToLower(strings.TrimSpace(email))
	if email != "" {
		if id, ok := s.byEmail[email]; ok {
			return s.orgs[id], nil
		}
	}

	id, err := newID("org_")
	if err != nil {
		return Org{}, err
	}
	org := Org{
		ID:        id,
		Name:      name,
		Email:     email,
		Plan:      "trial",
		CreatedAt: time.Now().UTC(),
	}
	s.orgs[id] = org
	if email != "" {
		s.byEmail[email] = id
	}
	return org, nil
}

func (s *MemoryOrgStore) FindOrgByEmail(_ context.Context, email string) (Org, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	email = strings.ToLower(strings.TrimSpace(email))
	id, ok := s.byEmail[email]
	if !ok {
		return Org{}, ErrNotFound
	}
	return s.orgs[id], nil
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

func (s *MemoryOrgStore) SetDefaultBucket(_ context.Context, orgID, bucketID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	o, ok := s.orgs[orgID]
	if !ok {
		return ErrNotFound
	}
	o.DefaultBucketID = bucketID
	s.orgs[orgID] = o
	return nil
}

func (s *MemoryOrgStore) ApplyCheckoutCompleted(_ context.Context, orgID, plan, customerID, subscriptionID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	o, ok := s.orgs[orgID]
	if !ok {
		return ErrNotFound
	}
	o.Plan = plan
	o.StripeCustomerID = customerID
	o.StripeSubscriptionID = subscriptionID
	s.orgs[orgID] = o
	return nil
}

func (s *MemoryOrgStore) DowngradeBySubscription(_ context.Context, subscriptionID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for id, o := range s.orgs {
		if o.StripeSubscriptionID == subscriptionID {
			o.Plan = "trial"
			o.StripeSubscriptionID = ""
			s.orgs[id] = o
			return nil
		}
	}
	return ErrNotFound
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
		DefaultBucketID: org.DefaultBucketID,
	}, nil
}

func (s *MemoryOrgStore) UpsertBucket(_ context.Context, orgID, bucketID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.buckets[orgID+":"+bucketID] = struct{}{}
	return nil
}
