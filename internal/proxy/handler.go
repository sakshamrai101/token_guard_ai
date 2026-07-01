package proxy

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"

	"github.com/saksham/token-guard-ai/internal/config"
)

type Handler struct {
	proxy        *httputil.ReverseProxy
	enforcement  *Enforcement
	upstreamURL  *url.URL
	upstreamHost string
	logger       *slog.Logger
}

func NewHandler(cfg config.Config, transport *http.Transport, enforcement *Enforcement, logger *slog.Logger) (*Handler, error) {
	upstream, err := url.Parse(cfg.UpstreamURL)
	if err != nil {
		return nil, err
	}
	if logger == nil {
		logger = slog.Default()
	}

	h := &Handler{
		enforcement:  enforcement,
		upstreamURL:  upstream,
		upstreamHost: cfg.UpstreamHost,
		logger:       logger,
	}

	rp := &httputil.ReverseProxy{
		Transport: transport,
		Director:  h.director,
		ModifyResponse: func(resp *http.Response) error {
			SanitizeResponseHeaders(resp.Header)
			return nil
		},
		ErrorHandler: h.errorHandler,
	}
	h.proxy = rp
	return h, nil
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	requestID := r.Header.Get("X-Request-Id")
	bucketID := r.Header.Get("X-Budget-Bucket-Id")

	result := h.enforcement.PreCheck(r.Context(), bucketID, requestID, 0)
	if result.FailOpen {
		h.logger.Warn("fail-open bypass", "request_id", requestID, "path", r.URL.Path)
	}
	if !result.Allowed {
		writeBudgetDenied(w)
		return
	}

	h.proxy.ServeHTTP(w, r)
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
	h.logger.Error("upstream proxy error",
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
