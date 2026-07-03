package main

import (
	"log/slog"
	"os"

	"github.com/saksham/token-guard-ai/internal/budget"
	"github.com/saksham/token-guard-ai/internal/config"
	"github.com/saksham/token-guard-ai/internal/proxy"
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

	if cfg.EnforcementMode != config.EnforcementOff {
		redisClient, err := budget.NewClient(cfg)
		if err != nil {
			logger.Error("failed to connect to redis", "error", err)
			os.Exit(1)
		}
		defer redisClient.Close()

		budgetChecker := budget.NewRedisBudgetChecker(redisClient, metrics)
		checker = proxy.NewBudgetCheckerBridge(budgetChecker)
		releaser = redisClient
		settler = redisClient
		providers := usage.RegistryForHost(cfg.UpstreamHost)
		extractor = providers.JSON
		streamExt = providers.Stream
		readiness = budget.NewReadiness(redisClient)
	}

	transport := proxy.NewTransport(cfg)
	enforcement := proxy.NewEnforcement(cfg, checker, logger)
	handler, err := proxy.NewHandler(cfg, transport, enforcement, releaser, settler, extractor, streamExt, metrics, alerter, logger)
	if err != nil {
		logger.Error("failed to create proxy handler", "error", err)
		os.Exit(1)
	}

	server := proxy.NewServer(cfg, handler, readiness, logger)
	if err := server.ListenAndServe(); err != nil {
		logger.Error("server exited", "error", err)
		os.Exit(1)
	}
}
