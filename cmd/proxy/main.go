package main

import (
	"log/slog"
	"net/http"
	"os"

	"github.com/saksham/token-guard-ai/internal/admin"
	"github.com/saksham/token-guard-ai/internal/budget"
	"github.com/saksham/token-guard-ai/internal/config"
	"github.com/saksham/token-guard-ai/internal/proxy"
	"github.com/saksham/token-guard-ai/internal/store"
	"github.com/saksham/token-guard-ai/internal/usage"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))

	cfg, err := config.Load()
	if err != nil {
		logger.Error("failed to load config", "error", err)
		os.Exit(1)
	}

	metrics := &budget.Metrics{}
	alerter := budget.NewAlerter(cfg.SlackWebhookURL, logger)

	var checker proxy.BudgetChecker
	var releaser proxy.BudgetReleaser
	var settler proxy.BudgetSettler
	var extractor usage.UsageExtractor
	var streamExt usage.StreamExtractor
	var readiness proxy.ReadinessChecker
	var adminHandler *admin.Handler
	var usageStore store.UsageStore
	var usageLogger store.UsageLogger
	var orgStore store.OrgStore
	requireAuth := false

	if cfg.DatabaseURL != "" {
		pg, err := store.OpenPostgres(cfg.DatabaseURL)
		if err != nil {
			logger.Error("failed to connect to postgres", "error", err)
			os.Exit(1)
		}
		defer pg.Close()
		usageStore = pg
		usageLogger = pg
		orgStore = pg
		requireAuth = true
		logger.Info("usage store connected", "backend", "postgres", "multi_tenant", true)
	} else {
		mem := store.NewMemoryUsageStore()
		usageStore = mem
		usageLogger = mem
		logger.Info("usage store connected", "backend", "memory", "multi_tenant", false)
	}

	needRedis := cfg.EnforcementMode != config.EnforcementOff || cfg.AdminAPIKey != "" || requireAuth
	if needRedis {
		redisClient, err := budget.NewClient(cfg)
		if err != nil {
			logger.Error("failed to connect to redis", "error", err)
			os.Exit(1)
		}
		defer redisClient.Close()

		if cfg.AdminAPIKey != "" {
			adminHandler = admin.NewHandlerWithOrgs(admin.NewRedisStore(redisClient), usageStore, orgStore, cfg.AdminAPIKey)
		}

		if cfg.EnforcementMode != config.EnforcementOff {
			budgetChecker := budget.NewRedisBudgetChecker(redisClient, metrics)
			checker = proxy.NewBudgetCheckerBridge(budgetChecker)
			releaser = redisClient
			settler = redisClient
			providers := usage.RegistryForHost(cfg.UpstreamHost)
			extractor = providers.JSON
			streamExt = providers.Stream
			readiness = budget.NewReadiness(redisClient)
		}
	}

	transport := proxy.NewTransport(cfg)
	enforcement := proxy.NewEnforcement(cfg, checker, logger)
	handler, err := proxy.NewHandlerWithRegistry(cfg, transport, enforcement, releaser, settler, extractor, streamExt, metrics, alerter, usageLogger, orgStore, logger)
	if err != nil {
		logger.Error("failed to create proxy handler", "error", err)
		os.Exit(1)
	}

	var llm http.Handler = handler
	if requireAuth {
		if orgStore == nil {
			logger.Error("multi-tenant auth required but org store is nil")
			os.Exit(1)
		}
		llm = proxy.NewAuthMiddleware(orgStore, handler)
		logger.Info("token guard auth enabled", "header", "X-TokenGuard-Key")
	}

	server := proxy.NewServer(cfg, llm, adminHandler, readiness, logger)
	if err := server.ListenAndServe(); err != nil {
		logger.Error("server exited", "error", err)
		os.Exit(1)
	}
}
