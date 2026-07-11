package main

import (
	"log/slog"
	"net/http"
	"os"

	"github.com/saksham/token-guard-ai/internal/admin"
	"github.com/saksham/token-guard-ai/internal/billing"
	"github.com/saksham/token-guard-ai/internal/budget"
	"github.com/saksham/token-guard-ai/internal/config"
	"github.com/saksham/token-guard-ai/internal/ops"
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
	var balances proxy.BalanceReader
	var extractor usage.UsageExtractor
	var streamExt usage.StreamExtractor
	var readiness proxy.ReadinessChecker
	var adminHandler *admin.Handler
	var opsHandler http.Handler
	var usageStore store.UsageStore
	var usageLogger store.UsageLogger
	var orgStore store.OrgStore
	var billingSvc *billing.Service
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

	billingCfg := billing.Config{
		SecretKey:     cfg.StripeSecretKey,
		WebhookSecret: cfg.StripeWebhookSecret,
		PriceIndie:    cfg.StripePriceIndie,
		PriceTeam:     cfg.StripePriceTeam,
		SuccessURL:    cfg.StripeSuccessURL,
		CancelURL:     cfg.StripeCancelURL,
	}
	if billingCfg.Enabled() && orgStore != nil {
		billingSvc = billing.NewService(billingCfg, billing.NewLiveStripeAPI(billingCfg.SecretKey), orgStore)
		logger.Info("stripe billing enabled")
	} else if cfg.StripeSecretKey != "" || cfg.StripeWebhookSecret != "" {
		logger.Warn("stripe env partially set; billing disabled until secret, webhook secret, and both price IDs are set with an org store")
	}

	needRedis := cfg.EnforcementMode != config.EnforcementOff || cfg.AdminAPIKey != "" || requireAuth
	if needRedis {
		redisClient, err := budget.NewClient(cfg)
		if err != nil {
			logger.Error("failed to connect to redis", "error", err)
			os.Exit(1)
		}
		defer redisClient.Close()

		alerter = alerter.WithDedupe(redisClient.WarningDedupe())

		redisStore := admin.NewRedisStore(redisClient)
		if cfg.AdminAPIKey != "" {
			var checkout admin.CheckoutStarter
			if billingSvc != nil {
				checkout = billingSvc
			}
			adminHandler = admin.NewHandlerWithBilling(redisStore, usageStore, orgStore, checkout, cfg.AdminAPIKey)
			opsHandler = ops.NewHandler(cfg.AdminAPIKey, redisStore, usageStore)
		}

		if cfg.EnforcementMode != config.EnforcementOff {
			budgetChecker := budget.NewRedisBudgetChecker(redisClient, metrics)
			checker = proxy.NewBudgetCheckerBridge(budgetChecker)
			releaser = redisClient
			settler = redisClient
			balances = redisClient
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
	if balances != nil {
		handler.WithBalances(balances)
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
	if billingSvc != nil {
		server.Handle("POST /billing/webhook", billing.NewWebhookHandler(billingSvc))
		logger.Info("stripe webhook mounted", "path", "/billing/webhook")
	}
	if opsHandler != nil {
		server.Handle("GET /ops", opsHandler)
		logger.Info("ops page mounted", "path", "/ops")
	}
	if err := server.ListenAndServe(); err != nil {
		logger.Error("server exited", "error", err)
		os.Exit(1)
	}
}
