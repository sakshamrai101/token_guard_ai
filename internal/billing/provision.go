package billing

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/saksham/token-guard-ai/internal/store"
)

const DefaultBucket = store.DefaultBucketName

// BudgetSeeder sets Redis budget balances (Lua set_budget).
type BudgetSeeder interface {
	SetBalance(ctx context.Context, orgID, bucketID string, balance int64) (int64, error)
}

// SetupSecrets stores one-time setup keys.
type SetupSecrets interface {
	PutSetupSecret(ctx context.Context, sessionID, orgID, rawKey string) error
}

// Provisioner creates org + key + default bucket after self-serve Checkout.
type Provisioner struct {
	orgs        store.OrgStore
	budgets     BudgetSeeder
	setup       SetupSecrets
	trialTokens int64
}

func NewProvisioner(orgs store.OrgStore, budgets BudgetSeeder, setup SetupSecrets, trialTokens int64) *Provisioner {
	if trialTokens <= 0 {
		trialTokens = 200000
	}
	return &Provisioner{orgs: orgs, budgets: budgets, setup: setup, trialTokens: trialTokens}
}

// ProvisionSignupResult is returned after provisioning (for tests).
type ProvisionSignupResult struct {
	OrgID  string
	RawKey string
}

func (p *Provisioner) ProvisionSignup(ctx context.Context, sessionID, email, plan, customerID, subscriptionID string) (ProvisionSignupResult, error) {
	email = strings.ToLower(strings.TrimSpace(email))
	if email == "" {
		return ProvisionSignupResult{}, errors.New("email required for signup provision")
	}
	plan, err := ParseSignupPlan(plan)
	if err != nil {
		return ProvisionSignupResult{}, err
	}
	if sessionID == "" {
		return ProvisionSignupResult{}, errors.New("checkout session id required")
	}

	org, err := p.orgs.FindOrgByEmail(ctx, email)
	if errors.Is(err, store.ErrNotFound) {
		org, err = p.orgs.CreateOrgWithEmail(ctx, email, email)
	}
	if err != nil {
		return ProvisionSignupResult{}, fmt.Errorf("resolve org: %w", err)
	}

	if err := p.orgs.ApplyCheckoutCompleted(ctx, org.ID, plan, customerID, subscriptionID); err != nil {
		return ProvisionSignupResult{}, err
	}
	if err := p.orgs.SetDefaultBucket(ctx, org.ID, DefaultBucket); err != nil {
		return ProvisionSignupResult{}, err
	}
	if err := p.orgs.UpsertBucket(ctx, org.ID, DefaultBucket); err != nil {
		return ProvisionSignupResult{}, err
	}
	if p.budgets != nil {
		if _, err := p.budgets.SetBalance(ctx, org.ID, DefaultBucket, p.trialTokens); err != nil {
			return ProvisionSignupResult{}, fmt.Errorf("seed trial budget: %w", err)
		}
	}

	rawKey, _, err := p.orgs.CreateAPIKey(ctx, org.ID)
	if err != nil {
		return ProvisionSignupResult{}, fmt.Errorf("mint api key: %w", err)
	}
	if p.setup != nil {
		if err := p.setup.PutSetupSecret(ctx, sessionID, org.ID, rawKey); err != nil {
			return ProvisionSignupResult{}, err
		}
	}
	return ProvisionSignupResult{OrgID: org.ID, RawKey: rawKey}, nil
}

// ParseSignupPlan accepts trial|indie|team.
func ParseSignupPlan(plan string) (string, error) {
	p := strings.ToLower(strings.TrimSpace(plan))
	switch p {
	case PlanTrial, PlanIndie, PlanTeam:
		return p, nil
	default:
		return "", fmt.Errorf("%w: must be trial, indie, or team", ErrInvalidPlan)
	}
}

func extractCustomerID(raw json.RawMessage) string {
	if len(raw) == 0 || string(raw) == "null" {
		return ""
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s
	}
	var obj struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(raw, &obj); err == nil {
		return obj.ID
	}
	return ""
}

func extractSubscriptionID(raw json.RawMessage) string {
	return extractCustomerID(raw)
}
