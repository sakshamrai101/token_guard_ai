package proxy

import (
	"net/http"
	"testing"
)

func TestSanitizeRequestHeaders(t *testing.T) {
	h := http.Header{
		"Host":                {"client.example.com"},
		"Authorization":       {"Bearer sk-test"},
		"Connection":          {"keep-alive, X-Foo"},
		"Keep-Alive":          {"timeout=5"},
		"Transfer-Encoding":   {"chunked"},
		"X-Budget-Bucket-Id":  {"bucket-123"},
		"X-Request-Id":        {"req-456"},
		"Content-Type":        {"application/json"},
		"X-Foo":               {"bar"},
	}

	SanitizeRequestHeaders(h, "api.openai.com")

	if got := h.Get("Host"); got != "api.openai.com" {
		t.Errorf("Host = %q, want api.openai.com", got)
	}
	if got := h.Get("Authorization"); got != "Bearer sk-test" {
		t.Errorf("Authorization should be preserved, got %q", got)
	}
	if got := h.Get("Content-Type"); got != "application/json" {
		t.Errorf("Content-Type should be preserved, got %q", got)
	}

	for _, name := range []string{
		"Connection", "Keep-Alive", "Transfer-Encoding",
		"X-Budget-Bucket-Id", "X-Request-Id",
	} {
		if h.Get(name) != "" {
			t.Errorf("header %q should be stripped, got %q", name, h.Get(name))
		}
	}
	// Connection: keep-alive, X-Foo lists X-Foo as hop-by-hop per RFC 7230.
	if h.Get("X-Foo") != "" {
		t.Errorf("X-Foo listed in Connection should be stripped, got %q", h.Get("X-Foo"))
	}
}

func TestSanitizeResponseHeaders(t *testing.T) {
	h := http.Header{
		"Content-Type":      {"application/json"},
		"Transfer-Encoding": {"chunked"},
		"Connection":        {"close"},
	}

	SanitizeResponseHeaders(h)

	if got := h.Get("Content-Type"); got != "application/json" {
		t.Errorf("Content-Type should be preserved, got %q", got)
	}
	for _, name := range []string{"Transfer-Encoding", "Connection"} {
		if h.Get(name) != "" {
			t.Errorf("header %q should be stripped, got %q", name, h.Get(name))
		}
	}
}
