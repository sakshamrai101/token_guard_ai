package store

import (
	"context"
	"testing"
)

func TestGenerateAPIKeyFormat(t *testing.T) {
	raw, prefix, hash, err := GenerateAPIKey()
	if err != nil {
		t.Fatalf("GenerateAPIKey: %v", err)
	}
	if len(raw) < 10 || raw[:3] != "tg_" {
		t.Fatalf("raw = %q, want tg_ prefix", raw)
	}
	if prefix == "" || hash == "" {
		t.Fatal("expected prefix and hash")
	}
	if HashAPIKey(raw) != hash {
		t.Fatal("hash mismatch")
	}
	if HashAPIKey(raw) == raw {
		t.Fatal("hash must not equal raw key")
	}
}

func TestMemoryOrgStoreCreateKeyAndLookup(t *testing.T) {
	s := NewMemoryOrgStore()
	ctx := context.Background()

	org, err := s.CreateOrg(ctx, "Acme")
	if err != nil {
		t.Fatalf("CreateOrg: %v", err)
	}
	raw, key, err := s.CreateAPIKey(ctx, org.ID)
	if err != nil {
		t.Fatalf("CreateAPIKey: %v", err)
	}
	if raw[:3] != "tg_" {
		t.Fatalf("raw = %q", raw)
	}
	if key.KeyHash == raw {
		t.Fatal("stored hash must not be raw key")
	}

	auth, err := s.LookupAPIKey(ctx, raw)
	if err != nil {
		t.Fatalf("LookupAPIKey: %v", err)
	}
	if auth.OrgID != org.ID {
		t.Fatalf("org = %q, want %q", auth.OrgID, org.ID)
	}

	if _, err := s.LookupAPIKey(ctx, "tg_bogus"); err != ErrInvalidAPIKey {
		t.Fatalf("err = %v, want ErrInvalidAPIKey", err)
	}
	if _, err := s.LookupAPIKey(ctx, ""); err != ErrInvalidAPIKey {
		t.Fatalf("empty key err = %v", err)
	}
}
