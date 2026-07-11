package billing

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strconv"
	"testing"
	"time"
)

func TestParsePlan(t *testing.T) {
	if p, err := ParsePlan("indie"); err != nil || p != PlanIndie {
		t.Fatalf("got %q %v", p, err)
	}
	if p, err := ParsePlan("team"); err != nil || p != PlanTeam {
		t.Fatalf("got %q %v", p, err)
	}
	if _, err := ParsePlan("trial"); err == nil {
		t.Fatal("trial is not a checkout plan")
	}
	if _, err := ParsePlan("bogus"); err == nil {
		t.Fatal("expected error")
	}
}

func TestVerifySignatureValid(t *testing.T) {
	secret := "whsec_test_secret"
	payload := []byte(`{"id":"evt_1","type":"checkout.session.completed"}`)
	ts := time.Now().Unix()
	sig := signPayload(secret, ts, payload)
	header := fmt.Sprintf("t=%d,v1=%s", ts, sig)

	if err := VerifySignature(payload, header, secret, 5*time.Minute); err != nil {
		t.Fatalf("VerifySignature: %v", err)
	}
}

func TestVerifySignatureInvalid(t *testing.T) {
	payload := []byte(`{"id":"evt_1"}`)
	header := fmt.Sprintf("t=%d,v1=%s", time.Now().Unix(), "deadbeef")
	if err := VerifySignature(payload, header, "whsec_test", 5*time.Minute); err == nil {
		t.Fatal("expected error")
	}
}

func TestPriceIDForPlan(t *testing.T) {
	c := Config{
		PriceIndie: "price_indie",
		PriceTeam:  "price_team",
	}
	id, err := c.PriceIDForPlan(PlanIndie)
	if err != nil || id != "price_indie" {
		t.Fatalf("got %q %v", id, err)
	}
	id, err = c.PriceIDForPlan(PlanTeam)
	if err != nil || id != "price_team" {
		t.Fatalf("got %q %v", id, err)
	}
}

func signPayload(secret string, ts int64, payload []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write([]byte(strconv.FormatInt(ts, 10)))
	_, _ = mac.Write([]byte("."))
	_, _ = mac.Write(payload)
	return hex.EncodeToString(mac.Sum(nil))
}
