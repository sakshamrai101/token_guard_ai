package config

import (
	"os"
	"testing"
	"time"
)

func setenv(t *testing.T, key, val string) {
	t.Helper()
	old, ok := os.LookupEnv(key)
	if err := os.Setenv(key, val); err != nil {
		t.Fatalf("Setenv %s: %v", key, err)
	}
	t.Cleanup(func() {
		if ok {
			_ = os.Setenv(key, old)
		} else {
			_ = os.Unsetenv(key)
		}
	})
}

func unsetenv(t *testing.T, key string) {
	t.Helper()
	old, ok := os.LookupEnv(key)
	_ = os.Unsetenv(key)
	t.Cleanup(func() {
		if ok {
			_ = os.Setenv(key, old)
		}
	})
}

func TestLoadDefaults(t *testing.T) {
	unsetenv(t, "ENFORCEMENT_MODE")
	unsetenv(t, "REDIS_URL")
	unsetenv(t, "DEFAULT_RESERVATION_ESTIMATE")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.EnforcementMode != EnforcementOff {
		t.Fatalf("mode = %q, want off", cfg.EnforcementMode)
	}
	if cfg.RedisURL != "redis://localhost:6379" {
		t.Fatalf("RedisURL = %q", cfg.RedisURL)
	}
	if cfg.DefaultReservationEst != 4096 {
		t.Fatalf("DefaultReservationEst = %d, want 4096", cfg.DefaultReservationEst)
	}
	if cfg.PromptTokenBuffer != 512 {
		t.Fatalf("PromptTokenBuffer = %d, want 512", cfg.PromptTokenBuffer)
	}
	if cfg.RedisPoolSize != 10 || cfg.RedisMinIdleConns != 10 {
		t.Fatalf("redis pool = %d/%d, want 10/10", cfg.RedisPoolSize, cfg.RedisMinIdleConns)
	}
	if cfg.ReservationTTL != 300*time.Second {
		t.Fatalf("ReservationTTL = %v, want 300s", cfg.ReservationTTL)
	}
}

func TestLoadRedisConfigOverrides(t *testing.T) {
	setenv(t, "ENFORCEMENT_MODE", "enforce")
	setenv(t, "REDIS_URL", "redis://redis:6379/1")
	setenv(t, "REDIS_POOL_SIZE", "20")
	setenv(t, "REDIS_MIN_IDLE_CONNS", "15")
	setenv(t, "REDIS_COMMAND_TIMEOUT_MS", "100")
	setenv(t, "RESERVATION_TTL_SEC", "600")
	setenv(t, "DEFAULT_RESERVATION_ESTIMATE", "8192")
	setenv(t, "PROMPT_TOKEN_BUFFER", "256")
	setenv(t, "SLACK_WEBHOOK_URL", "https://hooks.slack.com/test")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.EnforcementMode != EnforcementEnforce {
		t.Fatalf("mode = %q, want enforce", cfg.EnforcementMode)
	}
	if cfg.RedisURL != "redis://redis:6379/1" {
		t.Fatalf("RedisURL = %q", cfg.RedisURL)
	}
	if cfg.RedisPoolSize != 20 || cfg.RedisMinIdleConns != 15 {
		t.Fatalf("pool = %d/%d", cfg.RedisPoolSize, cfg.RedisMinIdleConns)
	}
	if cfg.RedisCommandTimeout != 100*time.Millisecond {
		t.Fatalf("RedisCommandTimeout = %v", cfg.RedisCommandTimeout)
	}
	if cfg.ReservationTTL != 600*time.Second {
		t.Fatalf("ReservationTTL = %v", cfg.ReservationTTL)
	}
	if cfg.DefaultReservationEst != 8192 || cfg.PromptTokenBuffer != 256 {
		t.Fatalf("estimate config = %d/%d", cfg.DefaultReservationEst, cfg.PromptTokenBuffer)
	}
	if cfg.SlackWebhookURL != "https://hooks.slack.com/test" {
		t.Fatalf("SlackWebhookURL = %q", cfg.SlackWebhookURL)
	}
}

func TestLoadInvalidEnforcementMode(t *testing.T) {
	setenv(t, "ENFORCEMENT_MODE", "invalid")
	_, err := Load()
	if err == nil {
		t.Fatal("expected error for invalid ENFORCEMENT_MODE")
	}
}

func TestLoadStripeConfig(t *testing.T) {
	setenv(t, "STRIPE_SECRET_KEY", "sk_test_x")
	setenv(t, "STRIPE_WEBHOOK_SECRET", "whsec_x")
	setenv(t, "STRIPE_PRICE_INDIE", "price_indie")
	setenv(t, "STRIPE_PRICE_TEAM", "price_team")
	setenv(t, "STRIPE_SUCCESS_URL", "https://example.com/ok")
	setenv(t, "STRIPE_CANCEL_URL", "https://example.com/cancel")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.StripeSecretKey != "sk_test_x" || cfg.StripeWebhookSecret != "whsec_x" {
		t.Fatalf("stripe secrets = %q/%q", cfg.StripeSecretKey, cfg.StripeWebhookSecret)
	}
	if cfg.StripePriceIndie != "price_indie" || cfg.StripePriceTeam != "price_team" {
		t.Fatalf("prices = %q/%q", cfg.StripePriceIndie, cfg.StripePriceTeam)
	}
	if cfg.StripeSuccessURL != "https://example.com/ok" {
		t.Fatalf("success = %q", cfg.StripeSuccessURL)
	}
}
