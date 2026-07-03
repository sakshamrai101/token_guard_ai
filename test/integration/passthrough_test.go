package integration

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/saksham/token-guard-ai/internal/config"
	"github.com/saksham/token-guard-ai/internal/proxy"
)

func TestTransparentPassthrough(t *testing.T) {
	const wantBody = `{"id":"chatcmpl-test","choices":[{"message":{"content":"hello"}}]}`
	const wantPath = "/v1/chat/completions"

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("method = %s, want POST", r.Method)
		}
		if r.URL.Path != wantPath {
			t.Errorf("path = %s, want %s", r.URL.Path, wantPath)
		}
		if r.URL.RawQuery != "stream=false" {
			t.Errorf("query = %s, want stream=false", r.URL.RawQuery)
		}
		if got := r.Host; got != "mock.openai.test" {
			t.Errorf("Host = %q, want mock.openai.test", got)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer sk-test" {
			t.Errorf("Authorization = %q, want Bearer sk-test", got)
		}
		if got := r.Header.Get("X-Budget-Bucket-Id"); got != "" {
			t.Errorf("internal header X-Budget-Bucket-Id should not reach upstream, got %q", got)
		}

		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("read body: %v", err)
		}
		if !bytes.Contains(body, []byte(`"model":"gpt-4o"`)) {
			t.Errorf("body = %s, expected model field", string(body))
		}

		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Transfer-Encoding", "chunked")
		_, _ = io.WriteString(w, wantBody)
	}))
	defer upstream.Close()

	cfg := config.Config{
		UpstreamURL:           upstream.URL,
		UpstreamHost:          "mock.openai.test",
		EnforcementMode:       config.EnforcementOff,
		MaxIdleConns:          10,
		MaxIdlePerHost:        10,
		IdleConnTimeout:       90 * time.Second,
		PreCheckTimeout:       50 * time.Millisecond,
		DefaultReservationEst: 4096,
		PromptTokenBuffer:     512,
	}

	transport := proxy.NewTransport(cfg)
	enforcement := proxy.NewEnforcement(cfg, nil, nil)
	handler, err := proxy.NewHandler(cfg, transport, enforcement, nil, nil, nil, nil, nil, nil, nil)
	if err != nil {
		t.Fatalf("NewHandler: %v", err)
	}
	server := proxy.NewServer(cfg, handler, nil, nil, nil)

	proxyServer := httptest.NewServer(server)
	defer proxyServer.Close()

	reqBody := []byte(`{"model":"gpt-4o","messages":[{"role":"user","content":"hi"}]}`)
	req, err := http.NewRequest(http.MethodPost, proxyServer.URL+wantPath+"?stream=false", bytes.NewReader(reqBody))
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	req.Header.Set("Authorization", "Bearer sk-test")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Budget-Bucket-Id", "bucket-123")
	req.Header.Set("X-Request-Id", "req-abc")
	req.Header.Set("Connection", "keep-alive")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	if got := resp.Header.Get("Transfer-Encoding"); got != "" {
		t.Errorf("hop-by-hop Transfer-Encoding should be stripped from response, got %q", got)
	}

	gotBody, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read response: %v", err)
	}
	if string(gotBody) != wantBody {
		t.Errorf("body = %s, want %s", gotBody, wantBody)
	}
}

func TestHealthEndpoints(t *testing.T) {
	cfg := config.Config{
		UpstreamURL:     "http://example.com",
		EnforcementMode: config.EnforcementOff,
	}
	server := proxy.NewServer(cfg, http.NotFoundHandler(), nil, nil, nil)
	ts := httptest.NewServer(server)
	defer ts.Close()

	for _, path := range []string{"/healthz", "/readyz"} {
		resp, err := http.Get(ts.URL + path)
		if err != nil {
			t.Fatalf("GET %s: %v", path, err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Errorf("%s status = %d, want 200", path, resp.StatusCode)
		}
	}
}

func TestReadyzNotReady(t *testing.T) {
	cfg := config.Config{
		UpstreamURL:     "http://example.com",
		EnforcementMode: config.EnforcementOff,
	}
	server := proxy.NewServer(cfg, http.NotFoundHandler(), nil, stubNotReady{}, nil)
	ts := httptest.NewServer(server)
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/readyz")
	if err != nil {
		t.Fatalf("GET /readyz: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("/readyz status = %d, want 503", resp.StatusCode)
	}
}

type stubNotReady struct{}

func (stubNotReady) Ready() bool { return false }
