package billing

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/saksham/token-guard-ai/internal/store"
)

const (
	PlanTrial = "trial"
	PlanIndie = "indie"
	PlanTeam  = "team"

	EventCheckoutCompleted   = "checkout.session.completed"
	EventSubscriptionDeleted = "customer.subscription.deleted"
)

var (
	ErrInvalidPlan      = errors.New("invalid plan")
	ErrInvalidSignature = errors.New("invalid stripe signature")
	ErrMissingConfig    = errors.New("stripe billing not configured")
)

type Config struct {
	SecretKey     string
	WebhookSecret string
	PriceIndie    string
	PriceTeam     string
	SuccessURL    string
	CancelURL     string
}

func (c Config) Enabled() bool {
	return c.SecretKey != "" && c.WebhookSecret != "" && c.PriceIndie != "" && c.PriceTeam != ""
}

func (c Config) PriceIDForPlan(plan string) (string, error) {
	switch plan {
	case PlanIndie, PlanTrial:
		// Trial Checkout uses Indie price ID with metadata plan=trial (can be $0 / trial period in Stripe).
		if c.PriceIndie == "" {
			return "", ErrMissingConfig
		}
		return c.PriceIndie, nil
	case PlanTeam:
		if c.PriceTeam == "" {
			return "", ErrMissingConfig
		}
		return c.PriceTeam, nil
	default:
		return "", ErrInvalidPlan
	}
}

func ParsePlan(plan string) (string, error) {
	p := strings.ToLower(strings.TrimSpace(plan))
	switch p {
	case PlanIndie, PlanTeam:
		return p, nil
	default:
		return "", fmt.Errorf("%w: must be indie or team", ErrInvalidPlan)
	}
}

// CreateCheckoutParams is passed to the Stripe API adapter.
type CreateCheckoutParams struct {
	OrgID         string
	Email         string
	Plan          string
	PriceID       string
	CustomerID    string
	CustomerEmail string
	SuccessURL    string
	CancelURL     string
}

type CheckoutSession struct {
	ID  string
	URL string
}

// StripeAPI creates Checkout Sessions (mocked in tests).
type StripeAPI interface {
	CreateCheckoutSession(ctx context.Context, p CreateCheckoutParams) (CheckoutSession, error)
}

// OrgBilling persists Stripe plan state on orgs (admin checkout path).
type OrgBilling interface {
	GetOrg(ctx context.Context, orgID string) (store.Org, error)
	ApplyCheckoutCompleted(ctx context.Context, orgID, plan, customerID, subscriptionID string) error
	DowngradeBySubscription(ctx context.Context, subscriptionID string) error
}

type Service struct {
	cfg         Config
	api         StripeAPI
	orgs        OrgBilling
	provisioner *Provisioner
}

func NewService(cfg Config, api StripeAPI, orgs OrgBilling) *Service {
	return &Service{cfg: cfg, api: api, orgs: orgs}
}

func (s *Service) WithProvisioner(p *Provisioner) *Service {
	s.provisioner = p
	return s
}

func (s *Service) StartCheckout(ctx context.Context, orgID, plan string) (string, error) {
	plan, err := ParsePlan(plan)
	if err != nil {
		return "", err
	}
	priceID, err := s.cfg.PriceIDForPlan(plan)
	if err != nil {
		return "", err
	}
	if s.cfg.SuccessURL == "" || s.cfg.CancelURL == "" {
		return "", fmt.Errorf("%w: success/cancel URL required", ErrMissingConfig)
	}
	if s.api == nil {
		return "", ErrMissingConfig
	}
	org, err := s.orgs.GetOrg(ctx, orgID)
	if err != nil {
		return "", err
	}
	session, err := s.api.CreateCheckoutSession(ctx, CreateCheckoutParams{
		OrgID:      org.ID,
		Plan:       plan,
		PriceID:    priceID,
		CustomerID: org.StripeCustomerID,
		SuccessURL: s.cfg.SuccessURL,
		CancelURL:  s.cfg.CancelURL,
	})
	if err != nil {
		return "", err
	}
	if session.URL == "" {
		return "", errors.New("stripe checkout session missing url")
	}
	return session.URL, nil
}

// StartPublicCheckout creates Checkout for self-serve signup (email + plan metadata).
func (s *Service) StartPublicCheckout(ctx context.Context, email, plan string) (string, error) {
	email = strings.ToLower(strings.TrimSpace(email))
	if email == "" || !strings.Contains(email, "@") {
		return "", errors.New("valid email required")
	}
	plan, err := ParseSignupPlan(plan)
	if err != nil {
		return "", err
	}
	priceID, err := s.cfg.PriceIDForPlan(plan)
	if err != nil {
		return "", err
	}
	if s.cfg.SuccessURL == "" || s.cfg.CancelURL == "" {
		return "", fmt.Errorf("%w: success/cancel URL required", ErrMissingConfig)
	}
	if s.api == nil {
		return "", ErrMissingConfig
	}
	session, err := s.api.CreateCheckoutSession(ctx, CreateCheckoutParams{
		Email:         email,
		Plan:          plan,
		PriceID:       priceID,
		CustomerEmail: email,
		SuccessURL:    s.cfg.SuccessURL,
		CancelURL:     s.cfg.CancelURL,
	})
	if err != nil {
		return "", err
	}
	if session.URL == "" {
		return "", errors.New("stripe checkout session missing url")
	}
	return session.URL, nil
}

type Event struct {
	ID   string `json:"id"`
	Type string `json:"type"`
	Data struct {
		Object json.RawMessage `json:"object"`
	} `json:"data"`
}

type checkoutSessionObject struct {
	ID               string            `json:"id"`
	Customer         json.RawMessage   `json:"customer"`
	Subscription     json.RawMessage   `json:"subscription"`
	Metadata         map[string]string `json:"metadata"`
	CustomerDetails  *struct {
		Email string `json:"email"`
	} `json:"customer_details"`
	CustomerEmail string `json:"customer_email"`
}

type subscriptionObject struct {
	ID string `json:"id"`
}

func (s *Service) HandleWebhook(ctx context.Context, payload []byte, signatureHeader string) error {
	if s.cfg.WebhookSecret == "" {
		return ErrMissingConfig
	}
	if err := VerifySignature(payload, signatureHeader, s.cfg.WebhookSecret, 5*time.Minute); err != nil {
		return err
	}
	var ev Event
	if err := json.Unmarshal(payload, &ev); err != nil {
		return fmt.Errorf("invalid event json: %w", err)
	}
	return s.dispatch(ctx, ev)
}

func (s *Service) dispatch(ctx context.Context, ev Event) error {
	switch ev.Type {
	case EventCheckoutCompleted:
		var obj checkoutSessionObject
		if err := json.Unmarshal(ev.Data.Object, &obj); err != nil {
			return fmt.Errorf("parse checkout session: %w", err)
		}
		customerID := extractCustomerID(obj.Customer)
		subscriptionID := extractSubscriptionID(obj.Subscription)
		plan := ""
		orgID := ""
		if obj.Metadata != nil {
			plan = obj.Metadata["plan"]
			orgID = obj.Metadata["org_id"]
		}

		// Admin path: org_id in metadata
		if orgID != "" {
			if plan == "" {
				return errors.New("checkout session missing plan metadata")
			}
			if _, err := ParsePlan(plan); err != nil {
				return err
			}
			return s.orgs.ApplyCheckoutCompleted(ctx, orgID, plan, customerID, subscriptionID)
		}

		// Self-serve path: email in metadata or customer_details
		email := ""
		if obj.Metadata != nil {
			email = obj.Metadata["email"]
		}
		if email == "" && obj.CustomerDetails != nil {
			email = obj.CustomerDetails.Email
		}
		if email == "" {
			email = obj.CustomerEmail
		}
		if email == "" || plan == "" {
			return errors.New("checkout session missing email/plan for provisioning")
		}
		if s.provisioner == nil {
			return errors.New("signup provisioner not configured")
		}
		_, err := s.provisioner.ProvisionSignup(ctx, obj.ID, email, plan, customerID, subscriptionID)
		return err

	case EventSubscriptionDeleted:
		var obj subscriptionObject
		if err := json.Unmarshal(ev.Data.Object, &obj); err != nil {
			return fmt.Errorf("parse subscription: %w", err)
		}
		if obj.ID == "" {
			return errors.New("subscription id missing")
		}
		err := s.orgs.DowngradeBySubscription(ctx, obj.ID)
		if errors.Is(err, store.ErrNotFound) {
			return nil
		}
		return err
	default:
		return nil
	}
}

// VerifySignature validates Stripe-Signature (t=,v1=) HMAC.
func VerifySignature(payload []byte, header, secret string, tolerance time.Duration) error {
	if header == "" || secret == "" {
		return ErrInvalidSignature
	}
	var timestamp int64
	var signatures []string
	for _, part := range strings.Split(header, ",") {
		kv := strings.SplitN(strings.TrimSpace(part), "=", 2)
		if len(kv) != 2 {
			continue
		}
		switch kv[0] {
		case "t":
			ts, err := strconv.ParseInt(kv[1], 10, 64)
			if err != nil {
				return ErrInvalidSignature
			}
			timestamp = ts
		case "v1":
			signatures = append(signatures, kv[1])
		}
	}
	if timestamp == 0 || len(signatures) == 0 {
		return ErrInvalidSignature
	}
	if tolerance > 0 {
		now := time.Now().Unix()
		if timestamp < now-int64(tolerance.Seconds()) || timestamp > now+int64(tolerance.Seconds()) {
			return ErrInvalidSignature
		}
	}
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write([]byte(strconv.FormatInt(timestamp, 10)))
	_, _ = mac.Write([]byte("."))
	_, _ = mac.Write(payload)
	expected := hex.EncodeToString(mac.Sum(nil))
	for _, sig := range signatures {
		if hmac.Equal([]byte(expected), []byte(sig)) {
			return nil
		}
	}
	return ErrInvalidSignature
}

func NewWebhookHandler(svc *Service) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		payload, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
		if err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		err = svc.HandleWebhook(r.Context(), payload, r.Header.Get("Stripe-Signature"))
		if errors.Is(err, ErrInvalidSignature) {
			http.Error(w, "invalid signature", http.StatusBadRequest)
			return
		}
		if err != nil {
			http.Error(w, "webhook error", http.StatusBadRequest)
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, `{"received":true}`)
	})
}
