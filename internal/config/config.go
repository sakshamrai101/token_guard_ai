package config

import (
	"fmt"
	"os"
	"strconv"
	"time"
)

type EnforcementMode string

const (
	EnforcementOff     EnforcementMode = "off"
	EnforcementShadow  EnforcementMode = "shadow"
	EnforcementEnforce EnforcementMode = "enforce"
)

type Config struct {
	ListenAddr       string
	UpstreamURL      string
	UpstreamHost     string
	EnforcementMode  EnforcementMode
	MaxIdleConns     int
	MaxIdlePerHost   int
	IdleConnTimeout  time.Duration
	PreCheckTimeout  time.Duration
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

	return Config{
		ListenAddr:      getEnv("LISTEN_ADDR", ":8080"),
		UpstreamURL:     upstreamURL,
		UpstreamHost:    upstreamHost,
		EnforcementMode: mode,
		MaxIdleConns:    maxIdle,
		MaxIdlePerHost:  maxIdlePerHost,
		IdleConnTimeout: time.Duration(idleTimeoutSec) * time.Second,
		PreCheckTimeout: time.Duration(preCheckTimeoutMs) * time.Millisecond,
	}, nil
}

func getEnv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
