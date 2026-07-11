package proxy

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"

	"github.com/google/uuid"
	"github.com/saksham/token-guard-ai/internal/budget"
	"github.com/saksham/token-guard-ai/internal/config"
	"github.com/saksham/token-guard-ai/internal/store"
	"github.com/saksham/token-guard-ai/internal/usage"
)

type ctxKey string

const requestIDContextKey ctxKey = "request_id"
const bucketIDContextKey ctxKey = "bucket_id"

type BudgetReleaser interface {
	Release(ctx context.Context, requestID string) error
}

type Handler struct {
	cfg           config.Config
	proxy         *httputil.ReverseProxy
	enforcement   *Enforcement
	releaser      BudgetReleaser
	settler       BudgetSettler
	extractor     usage.UsageExtractor
	streamExt     usage.StreamExtractor
	estimateCfg   budget.EstimateConfig
	metrics       *budget.Metrics
	alerter       *budget.Alerter
	usageLogger   store.UsageLogger
	bucketReg     store.OrgStore
	upstreamURL   *url.URL
	upstreamHost  string
	logger        *slog.Logger
}

func NewHandler(
	cfg config.Config,
	transport *http.Transport,
	enforcement *Enforcement,
	releaser BudgetReleaser,
	settler BudgetSettler,
	extractor usage.UsageExtractor,
	streamExt usage.StreamExtractor,
	metrics *budget.Metrics,
	alerter *budget.Alerter,
	usageLogger store.UsageLogger,
	logger *slog.Logger,
) (*Handler, error) {
	return NewHandlerWithRegistry(cfg, transport, enforcement, releaser, settler, extractor, streamExt, metrics, alerter, usageLogger, nil, logger)
}

func NewHandlerWithRegistry(
	cfg config.Config,
	transport *http.Transport,
	enforcement *Enforcement,
	releaser BudgetReleaser,
	settler BudgetSettler,
	extractor usage.UsageExtractor,
	streamExt usage.StreamExtractor,
	metrics *budget.Metrics,
	alerter *budget.Alerter,
	usageLogger store.UsageLogger,
	bucketReg store.OrgStore,
	logger *slog.Logger,
) (*Handler, error) {
	upstream, err := url.Parse(cfg.UpstreamURL)
	if err != nil {
		return nil, err
	}
	if logger == nil {
		logger = slog.Default()
	}
	if metrics == nil {
		metrics = &budget.Metrics{}
	}
	if alerter == nil {
		alerter = budget.NewAlerter("", logger)
	}

	h := &Handler{
		cfg:          cfg,
		enforcement:  enforcement,
		releaser:     releaser,
		settler:      settler,
		extractor:    extractor,
		streamExt:    streamExt,
		estimateCfg: budget.EstimateConfig{
			DefaultEstimate: cfg.DefaultReservationEst,
			PromptBuffer:    cfg.PromptTokenBuffer,
		},
		metrics:      metrics,
		alerter:      alerter,
		usageLogger:  usageLogger,
		bucketReg:    bucketReg,
		upstreamURL:  upstream,
		upstreamHost: cfg.UpstreamHost,
		logger:       logger,
	}

	rp := &httputil.ReverseProxy{
		Transport:      transport,
		Director:       h.director,
		ModifyResponse: h.modifyResponse,
		ErrorHandler:   h.errorHandler,
	}
	h.proxy = rp
	return h, nil
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	requestID := r.Header.Get("X-Request-Id")
	if requestID == "" {
		requestID = uuid.New().String()
		r.Header.Set("X-Request-Id", requestID)
	}
	bucketID := r.Header.Get("X-Budget-Bucket-Id")
	orgID := OrgIDFromContext(r.Context())

	body, restored, err := budget.ReadAndRestoreBody(r.Body)
	if err != nil {
		h.logger.Error("failed to read request body", "request_id", requestID, "error", err)
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	r.Body = restored

	estimate := budget.EstimateFromBody(body, h.estimateCfg, h.logger)

	var result PreCheckResult
	var outcome string

	if bucketID == "" && h.cfg.EnforcementMode != config.EnforcementOff {
		h.logger.Warn("missing bucket id, fail-open forward", "request_id", requestID)
		h.metrics.IncFailOpen()
		h.alerter.FailOpen(r.Context(), requestID, bucketID, "missing bucket_id")
		result = PreCheckResult{Allowed: true, FailOpen: true}
		outcome = "fail_open"
	} else {
		result = h.enforcement.PreCheck(r.Context(), orgID, bucketID, requestID, estimate)
		switch {
		case result.FailOpen:
			h.metrics.IncFailOpen()
			h.alerter.FailOpen(r.Context(), requestID, bucketID, "redis error or timeout")
			outcome = "fail_open"
		case !result.Allowed:
			h.alerter.BudgetDenied(r.Context(), requestID, bucketID, estimate)
			outcome = "denied"
		default:
			outcome = "allowed"
			if h.bucketReg != nil && bucketID != "" && !result.FailOpen {
				_ = h.bucketReg.UpsertBucket(r.Context(), orgID, bucketID)
			}
		}
	}

	h.logger.Info("budget check",
		"request_id", requestID,
		"org_id", orgID,
		"bucket_id", bucketID,
		"reserved", result.Reserved,
		"estimate", estimate,
		"mode", h.cfg.EnforcementMode,
		"outcome", outcome,
	)

	if result.FailOpen {
		h.logger.Warn("fail-open bypass", "request_id", requestID, "path", r.URL.Path)
	}
	if !result.Allowed {
		writeBudgetDenied(w)
		return
	}

	ctx := context.WithValue(r.Context(), requestIDContextKey, requestID)
	ctx = context.WithValue(ctx, reservedContextKey, result.Reserved)
	ctx = context.WithValue(ctx, bucketIDContextKey, bucketID)
	ctx = context.WithValue(ctx, orgIDContextKey, orgID)
	h.proxy.ServeHTTP(w, r.WithContext(ctx))
}

func (h *Handler) modifyResponse(resp *http.Response) error {
	SanitizeResponseHeaders(resp.Header)

	requestID, _ := resp.Request.Context().Value(requestIDContextKey).(string)
	bucketID, _ := resp.Request.Context().Value(bucketIDContextKey).(string)
	reserved, _ := resp.Request.Context().Value(reservedContextKey).(int64)
	orgID := OrgIDFromContext(resp.Request.Context())

	if resp.StatusCode >= 400 {
		if h.releaser != nil && requestID != "" {
			if err := h.releaser.Release(resp.Request.Context(), requestID); err != nil {
				h.logger.Error("failed to release budget on upstream error",
					"request_id", requestID,
					"status", resp.StatusCode,
					"error", err,
				)
			} else {
				h.logRelease(resp.Request.Context(), orgID, requestID, bucketID, reserved)
			}
		}
		return nil
	}

	if resp.StatusCode == 200 && h.settler != nil && requestID != "" {
		params := settlementParams{
			settler:     h.settler,
			metrics:     h.metrics,
			usageLogger: h.usageLogger,
			ctx:         resp.Request.Context(),
			requestID:   requestID,
			orgID:       orgID,
			bucketID:    bucketID,
			provider:    h.upstreamHost,
			reserved:    reserved,
			logger:      h.logger,
		}
		if isEventStream(resp.Header) && h.streamExt != nil {
			resp.Body = newStreamTap(resp.Body, h.streamExt, params)
		} else if h.extractor != nil && !isEventStream(resp.Header) {
			resp.Body = newSettlingReader(resp.Body, h.extractor, params)
		}
	}

	return nil
}

func (h *Handler) logRelease(ctx context.Context, orgID, requestID, bucketID string, reserved int64) {
	if h.usageLogger == nil {
		return
	}
	if orgID == "" {
		orgID = store.DefaultOrgID
	}
	if err := h.usageLogger.LogUsage(ctx, store.UsageEvent{
		OrgID:     orgID,
		BucketID:  bucketID,
		RequestID: requestID,
		Reserved:  reserved,
		Actual:    0,
		Outcome:   "released",
		Provider:  h.upstreamHost,
	}); err != nil {
		h.logger.Error("failed to log release usage event",
			"request_id", requestID,
			"error", err,
		)
	}
}

func (h *Handler) director(req *http.Request) {
	req.URL.Scheme = h.upstreamURL.Scheme
	req.URL.Host = h.upstreamURL.Host
	if h.upstreamURL.Path != "" && h.upstreamURL.Path != "/" {
		req.URL.Path = singleJoiningSlash(h.upstreamURL.Path, req.URL.Path)
	}
	req.Host = h.upstreamHost

	SanitizeRequestHeaders(req.Header, h.upstreamHost)
}

func (h *Handler) errorHandler(w http.ResponseWriter, r *http.Request, err error) {
	requestID, _ := r.Context().Value(requestIDContextKey).(string)
	bucketID, _ := r.Context().Value(bucketIDContextKey).(string)
	reserved, _ := r.Context().Value(reservedContextKey).(int64)
	orgID := OrgIDFromContext(r.Context())
	if requestID != "" && h.releaser != nil {
		if relErr := h.releaser.Release(r.Context(), requestID); relErr != nil {
			h.logger.Error("failed to release budget on upstream transport error",
				"request_id", requestID,
				"error", relErr,
			)
		} else {
			h.logRelease(r.Context(), orgID, requestID, bucketID, reserved)
		}
	}

	h.logger.Error("upstream proxy error",
		"request_id", requestID,
		"method", r.Method,
		"path", r.URL.Path,
		"error", err,
	)
	http.Error(w, "upstream unavailable", http.StatusBadGateway)
}

func writeBudgetDenied(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Retry-After", "60")
	w.WriteHeader(http.StatusTooManyRequests)
	_, _ = io.WriteString(w, `{"error":{"message":"budget exhausted","type":"budget_exceeded"}}`)
}

func singleJoiningSlash(a, b string) string {
	aslash := strings.HasSuffix(a, "/")
	bslash := strings.HasPrefix(b, "/")
	switch {
	case aslash && bslash:
		return a + b[1:]
	case !aslash && !bslash:
		return a + "/" + b
	}
	return a + b
}
