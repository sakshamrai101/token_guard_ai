package proxy

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/saksham/token-guard-ai/internal/budget"
	"github.com/saksham/token-guard-ai/internal/config"
)

type stubBudgetChecker struct {
	result   PreCheckResult
	err      error
	lastEst  int64
	lastReq  string
	lastBuck string
}

func (s *stubBudgetChecker) Reserve(_ context.Context, bucketID, requestID string, estimate int64) (PreCheckResult, error) {
	s.lastBuck = bucketID
	s.lastReq = requestID
	s.lastEst = estimate
	return s.result, s.err
}

type stubReleaser struct {
	calls []string
	err   error
}

func (s *stubReleaser) Release(_ context.Context, requestID string) error {
	s.calls = append(s.calls, requestID)
	return s.err
}

func testHandler(t *testing.T, mode config.EnforcementMode, checker BudgetChecker, releaser BudgetReleaser, metrics *budget.Metrics) (*Handler, *httptest.Server) {
	t.Helper()
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		w.Header().Set("X-Seen-Body", string(body))
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	t.Cleanup(upstream.Close)

	cfg := config.Config{
		UpstreamURL:           upstream.URL,
		UpstreamHost:          "mock.openai.test",
		EnforcementMode:       mode,
		DefaultReservationEst: 4096,
		PromptTokenBuffer:     512,
		PreCheckTimeout:       50 * time.Millisecond,
	}
	enforcement := NewEnforcement(cfg, checker, nil)
	handler, err := NewHandler(cfg, NewTransport(cfg), enforcement, releaser, metrics, nil, nil)
	if err != nil {
		t.Fatalf("NewHandler: %v", err)
	}
	return handler, httptest.NewServer(handler)
}

func TestHandlerBudgetDeniedReturns429(t *testing.T) {
	checker := &stubBudgetChecker{result: PreCheckResult{Allowed: false}}
	_, server := testHandler(t, config.EnforcementEnforce, checker, nil, nil)
	defer server.Close()

	reqBody := `{"model":"gpt-4o","max_tokens":1024}`
	req, _ := http.NewRequest(http.MethodPost, server.URL+"/v1/chat/completions", strings.NewReader(reqBody))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Budget-Bucket-Id", "bucket-1")
	req.Header.Set("X-Request-Id", "req-deny")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want 429", resp.StatusCode)
	}
	if checker.lastEst != 1536 {
		t.Fatalf("estimate passed to checker = %d, want 1536", checker.lastEst)
	}
}

func TestHandlerFailOpenMissingBucket(t *testing.T) {
	metrics := &budget.Metrics{}
	checker := &stubBudgetChecker{result: PreCheckResult{Allowed: false}}
	_, server := testHandler(t, config.EnforcementEnforce, checker, nil, metrics)
	defer server.Close()

	req, _ := http.NewRequest(http.MethodPost, server.URL+"/v1/chat/completions", strings.NewReader(`{}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Request-Id", "req-nobucket")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200 fail-open", resp.StatusCode)
	}
	if checker.lastReq != "" {
		t.Fatal("checker should not be called when bucket id is missing")
	}
	if metrics.FailOpenTotal.Load() != 1 {
		t.Fatalf("fail_open_total = %d, want 1", metrics.FailOpenTotal.Load())
	}
}

func TestHandlerGeneratesRequestID(t *testing.T) {
	checker := &stubBudgetChecker{result: PreCheckResult{Allowed: true}}
	_, server := testHandler(t, config.EnforcementEnforce, checker, nil, nil)
	defer server.Close()

	req, _ := http.NewRequest(http.MethodPost, server.URL+"/v1/chat/completions", strings.NewReader(`{}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Budget-Bucket-Id", "bucket-1")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	resp.Body.Close()

	if checker.lastReq == "" {
		t.Fatal("expected generated request id to reach checker")
	}
	if strings.Count(checker.lastReq, "-") != 4 {
		t.Fatalf("request id %q does not look like a UUID", checker.lastReq)
	}
}

func TestHandlerRestoresBodyForUpstream(t *testing.T) {
	checker := &stubBudgetChecker{result: PreCheckResult{Allowed: true}}
	_, server := testHandler(t, config.EnforcementOff, checker, nil, nil)
	defer server.Close()

	reqBody := `{"model":"gpt-4o","max_tokens":256,"messages":[{"role":"user","content":"hi"}]}`
	req, _ := http.NewRequest(http.MethodPost, server.URL+"/v1/chat/completions", strings.NewReader(reqBody))
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	defer resp.Body.Close()

	if got := resp.Header.Get("X-Seen-Body"); got != reqBody {
		t.Fatalf("upstream body = %q, want original body forwarded", got)
	}
}

func TestHandlerModifyResponseReleasesOnUpstreamError(t *testing.T) {
	releaser := &stubReleaser{}
	cfg := config.Config{
		UpstreamURL:     "http://example.com",
		UpstreamHost:    "example.com",
		EnforcementMode: config.EnforcementEnforce,
	}
	handler, err := NewHandler(cfg, NewTransport(cfg), NewEnforcement(cfg, nil, nil), releaser, nil, nil, nil)
	if err != nil {
		t.Fatalf("NewHandler: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	req = req.WithContext(context.WithValue(req.Context(), requestIDContextKey, "req-release"))
	resp := &http.Response{
		StatusCode: http.StatusInternalServerError,
		Header:     make(http.Header),
		Request:    req,
	}

	if err := handler.modifyResponse(resp); err != nil {
		t.Fatalf("modifyResponse: %v", err)
	}
	if len(releaser.calls) != 1 || releaser.calls[0] != "req-release" {
		t.Fatalf("release calls = %v, want [req-release]", releaser.calls)
	}
}

func TestHandlerModifyResponseSkipsReleaseOn200(t *testing.T) {
	releaser := &stubReleaser{}
	cfg := config.Config{UpstreamURL: "http://example.com", UpstreamHost: "example.com"}
	handler, err := NewHandler(cfg, NewTransport(cfg), NewEnforcement(cfg, nil, nil), releaser, nil, nil, nil)
	if err != nil {
		t.Fatalf("NewHandler: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/", nil)
	req = req.WithContext(context.WithValue(req.Context(), requestIDContextKey, "req-ok"))
	resp := &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Request: req}

	if err := handler.modifyResponse(resp); err != nil {
		t.Fatalf("modifyResponse: %v", err)
	}
	if len(releaser.calls) != 0 {
		t.Fatalf("release should not be called on 200, got %v", releaser.calls)
	}
}

func TestHandlerFailOpenIncrementsMetrics(t *testing.T) {
	metrics := &budget.Metrics{}
	checker := &stubBudgetChecker{err: errors.New("redis down")}
	_, server := testHandler(t, config.EnforcementEnforce, checker, nil, metrics)
	defer server.Close()

	req, _ := http.NewRequest(http.MethodPost, server.URL+"/v1/chat/completions", strings.NewReader(`{}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Budget-Bucket-Id", "bucket-1")
	req.Header.Set("X-Request-Id", "req-fo")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200 fail-open", resp.StatusCode)
	}
	if metrics.FailOpenTotal.Load() != 1 {
		t.Fatalf("fail_open_total = %d, want 1", metrics.FailOpenTotal.Load())
	}
}

func TestWriteBudgetDenied(t *testing.T) {
	rr := httptest.NewRecorder()
	writeBudgetDenied(rr)

	if rr.Code != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want 429", rr.Code)
	}
	if got := rr.Header().Get("Retry-After"); got != "60" {
		t.Fatalf("Retry-After = %q, want 60", got)
	}
	if !strings.Contains(rr.Body.String(), "budget_exceeded") {
		t.Fatalf("body = %q, want budget_exceeded error", rr.Body.String())
	}
}
