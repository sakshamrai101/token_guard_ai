package billing

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// LiveStripeAPI calls Stripe Checkout Sessions via HTTPS.
type LiveStripeAPI struct {
	secretKey  string
	httpClient *http.Client
	baseURL    string
}

func NewLiveStripeAPI(secretKey string) *LiveStripeAPI {
	return &LiveStripeAPI{
		secretKey:  secretKey,
		httpClient: &http.Client{Timeout: 15 * time.Second},
		baseURL:    "https://api.stripe.com",
	}
}

func (a *LiveStripeAPI) CreateCheckoutSession(ctx context.Context, p CreateCheckoutParams) (CheckoutSession, error) {
	form := url.Values{}
	form.Set("mode", "subscription")
	form.Set("success_url", p.SuccessURL)
	form.Set("cancel_url", p.CancelURL)
	form.Set("line_items[0][price]", p.PriceID)
	form.Set("line_items[0][quantity]", "1")
	form.Set("metadata[org_id]", p.OrgID)
	form.Set("metadata[plan]", p.Plan)
	form.Set("subscription_data[metadata][org_id]", p.OrgID)
	form.Set("subscription_data[metadata][plan]", p.Plan)
	if p.CustomerID != "" {
		form.Set("customer", p.CustomerID)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, a.baseURL+"/v1/checkout/sessions", strings.NewReader(form.Encode()))
	if err != nil {
		return CheckoutSession{}, err
	}
	req.Header.Set("Authorization", "Bearer "+a.secretKey)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := a.httpClient.Do(req)
	if err != nil {
		return CheckoutSession{}, fmt.Errorf("stripe request: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode >= 300 {
		msg := string(body)
		if len(msg) > 200 {
			msg = msg[:200] + "..."
		}
		return CheckoutSession{}, fmt.Errorf("stripe error status=%d body=%s", resp.StatusCode, msg)
	}
	var out struct {
		ID  string `json:"id"`
		URL string `json:"url"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return CheckoutSession{}, fmt.Errorf("decode stripe response: %w", err)
	}
	return CheckoutSession{ID: out.ID, URL: out.URL}, nil
}
