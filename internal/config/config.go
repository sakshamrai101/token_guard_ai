package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

type EnforcementMode string

const (
	EnforcementOff     EnforcementMode = "off"
	EnforcementShadow  EnforcementMode = "shadow"
	EnforcementEnforce EnforcementMode = "enforce"
)

type Config struct {
	ListenAddr            string
	UpstreamURL           string
	UpstreamHost          string
	OpenAIUpstreamURL     string
	OpenAIUpstreamHost    string
	AnthropicUpstreamURL  string
	AnthropicUpstreamHost string
	EnforcementMode       EnforcementMode
	MaxIdleConns          int
	MaxIdlePerHost        int
	IdleConnTimeout       time.Duration
	PreCheckTimeout       time.Duration

	RedisURL              string
	RedisPoolSize         int
	RedisMinIdleConns     int
	RedisCommandTimeout   time.Duration
	ReservationTTL        time.Duration
	DefaultReservationEst int64
	PromptTokenBuffer     int64
	SlackWebhookURL       string
	AdminAPIKey           string
	DatabaseURL           string

	StripeSecretKey     string
	StripeWebhookSecret string
	StripePriceIndie    string
	StripePriceTeam     string
	StripeSuccessURL    string
	StripeCancelURL     string

	PublicBaseURL     string
	TrialBudgetTokens int64
}

func Load() (Config, error) {
	mode := EnforcementMode(getEnv("ENFORCEMENT_MODE", string(EnforcementOff)))
	switch mode {
	case EnforcementOff, EnforcementShadow, EnforcementEnforce:
	default:
		return Config{}, fmt.Errorf("invalid ENFORCEMENT_MODE %q: must be off, shadow, or enforce", mode)
	}

	upstreamURL := getEnv("UPSTREAM_URL", "https://api.openai.com")
	upstreamHost := getEnv("UPSTREAM_HOST", "api.openai.com")
	openaiUpstreamURL := getEnv("OPENAI_UPSTREAM_URL", "https://api.openai.com")
	openaiUpstreamHost := getEnv("OPENAI_UPSTREAM_HOST", "api.openai.com")
	anthropicUpstreamURL := getEnv("ANTHROPIC_UPSTREAM_URL", "https://api.anthropic.com")
	anthropicUpstreamHost := getEnv("ANTHROPIC_UPSTREAM_HOST", "api.anthropic.com")

	maxIdle, err := strconv.Atoi(getEnv("HTTP_MAX_IDLE_CONNS", "100"))
	if err != nil {
		return Config{}, fmt.Errorf("HTTP_MAX_IDLE_CONNS: %w", err)
	}
	maxIdlePerHost, err := strconv.Atoi(getEnv("HTTP_MAX_IDLE_CONNS_PER_HOST", "100"))
	if err != nil {
		return Config{}, fmt.Errorf("HTTP_MAX_IDLE_CONNS_PER_HOST: %w", err)
	}
	idleTimeoutSec, err := strconv.Atoi(getEnv("HTTP_IDLE_CONN_TIMEOUT_SEC", "90"))
	if err != nil {
		return Config{}, fmt.Errorf("HTTP_IDLE_CONN_TIMEOUT_SEC: %w", err)
	}
	preCheckTimeoutMs, err := strconv.Atoi(getEnv("PRECHECK_TIMEOUT_MS", "50"))
	if err != nil {
		return Config{}, fmt.Errorf("PRECHECK_TIMEOUT_MS: %w", err)
	}

	redisURL := getEnv("REDIS_URL", "redis://localhost:6379")
	redisPoolSize, err := strconv.Atoi(getEnv("REDIS_POOL_SIZE", "10"))
	if err != nil {
		return Config{}, fmt.Errorf("REDIS_POOL_SIZE: %w", err)
	}
	redisMinIdle, err := strconv.Atoi(getEnv("REDIS_MIN_IDLE_CONNS", "10"))
	if err != nil {
		return Config{}, fmt.Errorf("REDIS_MIN_IDLE_CONNS: %w", err)
	}
	redisTimeoutMs, err := strconv.Atoi(getEnv("REDIS_COMMAND_TIMEOUT_MS", "50"))
	if err != nil {
		return Config{}, fmt.Errorf("REDIS_COMMAND_TIMEOUT_MS: %w", err)
	}
	reservationTTLSec, err := strconv.Atoi(getEnv("RESERVATION_TTL_SEC", "300"))
	if err != nil {
		return Config{}, fmt.Errorf("RESERVATION_TTL_SEC: %w", err)
	}
	defaultEst, err := strconv.ParseInt(getEnv("DEFAULT_RESERVATION_ESTIMATE", "4096"), 10, 64)
	if err != nil {
		return Config{}, fmt.Errorf("DEFAULT_RESERVATION_ESTIMATE: %w", err)
	}
	promptBuffer, err := strconv.ParseInt(getEnv("PROMPT_TOKEN_BUFFER", "512"), 10, 64)
	if err != nil {
		return Config{}, fmt.Errorf("PROMPT_TOKEN_BUFFER: %w", err)
	}
	trialBudget, err := strconv.ParseInt(getEnv("TRIAL_BUDGET_TOKENS", "200000"), 10, 64)
	if err != nil {
		return Config{}, fmt.Errorf("TRIAL_BUDGET_TOKENS: %w", err)
	}

	if mode != EnforcementOff && redisURL == "" {
		return Config{}, fmt.Errorf("REDIS_URL is required when ENFORCEMENT_MODE is not off")
	}

	publicBase := strings.TrimRight(getEnv("PUBLIC_BASE_URL", "http://localhost:8080"), "/")
	defaultSuccess := publicBase + "/setup?session_id={CHECKOUT_SESSION_ID}"
	defaultCancel := publicBase + "/signup?canceled=1"

	return Config{
		ListenAddr:            getEnv("LISTEN_ADDR", ":8080"),
		UpstreamURL:           upstreamURL,
		UpstreamHost:          upstreamHost,
		OpenAIUpstreamURL:     openaiUpstreamURL,
		OpenAIUpstreamHost:    openaiUpstreamHost,
		AnthropicUpstreamURL:  anthropicUpstreamURL,
		AnthropicUpstreamHost: anthropicUpstreamHost,
		EnforcementMode:       mode,
		MaxIdleConns:          maxIdle,
		MaxIdlePerHost:        maxIdlePerHost,
		IdleConnTimeout:       time.Duration(idleTimeoutSec) * time.Second,
		PreCheckTimeout:       time.Duration(preCheckTimeoutMs) * time.Millisecond,
		RedisURL:              redisURL,
		RedisPoolSize:         redisPoolSize,
		RedisMinIdleConns:     redisMinIdle,
		RedisCommandTimeout:   time.Duration(redisTimeoutMs) * time.Millisecond,
		ReservationTTL:        time.Duration(reservationTTLSec) * time.Second,
		DefaultReservationEst: defaultEst,
		PromptTokenBuffer:     promptBuffer,
		SlackWebhookURL:       getEnv("SLACK_WEBHOOK_URL", ""),
		AdminAPIKey:           getEnv("ADMIN_API_KEY", ""),
		DatabaseURL:           getEnv("DATABASE_URL", ""),
		StripeSecretKey:       getEnv("STRIPE_SECRET_KEY", ""),
		StripeWebhookSecret:   getEnv("STRIPE_WEBHOOK_SECRET", ""),
		StripePriceIndie:      getEnv("STRIPE_PRICE_INDIE", ""),
		StripePriceTeam:       getEnv("STRIPE_PRICE_TEAM", ""),
		StripeSuccessURL:      getEnv("STRIPE_SUCCESS_URL", defaultSuccess),
		StripeCancelURL:       getEnv("STRIPE_CANCEL_URL", defaultCancel),
		PublicBaseURL:         publicBase,
		TrialBudgetTokens:     trialBudget,
	}, nil
}

func getEnv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
