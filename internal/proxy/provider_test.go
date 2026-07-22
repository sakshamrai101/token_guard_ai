package proxy

import (
	"context"
	"net/http"
	"net/url"
	"testing"

	"github.com/saksham/token-guard-ai/internal/config"
	"github.com/saksham/token-guard-ai/internal/usage"
)

func TestStripProviderPrefix(t *testing.T) {
	tests := []struct {
		path, prefix, wantRest string
		ok                     bool
	}{
		{"/openai/v1/chat/completions", "openai", "/v1/chat/completions", true},
		{"/anthropic/v1/messages", "anthropic", "/v1/messages", true},
		{"/openai", "openai", "/", true},
		{"/anthropic/", "anthropic", "/", true},
		{"/v1/chat/completions", "openai", "", false},
		{"/openaiish/v1", "openai", "", false},
		{"/admin/v1/buckets", "openai", "", false},
		{"/me/buckets", "anthropic", "", false},
	}
	for _, tc := range tests {
		rest, ok := stripProviderPrefix(tc.path, tc.prefix)
		if ok != tc.ok || rest != tc.wantRest {
			t.Fatalf("%s + %s: rest=%q ok=%v, want rest=%q ok=%v",
				tc.path, tc.prefix, rest, ok, tc.wantRest, tc.ok)
		}
	}
}

func TestResolveProviderRoutePrefixes(t *testing.T) {
	openaiURL, _ := url.Parse("https://api.openai.com")
	anthropicURL, _ := url.Parse("https://api.anthropic.com")
	legacyURL, _ := url.Parse("https://legacy.example")
	cfg := config.Config{
		UpstreamURL:           legacyURL.String(),
		UpstreamHost:          "legacy.example",
		OpenAIUpstreamURL:     openaiURL.String(),
		OpenAIUpstreamHost:    "api.openai.com",
		AnthropicUpstreamURL:  anthropicURL.String(),
		AnthropicUpstreamHost: "api.anthropic.com",
	}
	routes := newProviderRoutes(cfg, legacyURL, "legacy.example",
		usage.NewOpenAIExtractor(), usage.NewOpenAIStreamExtractor())

	openai := routes.resolve("/openai/v1/chat/completions")
	if openai.Kind != ProviderOpenAI || openai.ForwardPath != "/v1/chat/completions" {
		t.Fatalf("openai route = %+v", openai)
	}
	if openai.UpstreamHost != "api.openai.com" || openai.UpstreamURL.Host != "api.openai.com" {
		t.Fatalf("openai upstream = %s %v", openai.UpstreamHost, openai.UpstreamURL)
	}
	if openai.JSON == nil || openai.Stream == nil {
		t.Fatal("openai extractors missing")
	}

	anthropic := routes.resolve("/anthropic/v1/messages")
	if anthropic.Kind != ProviderAnthropic || anthropic.ForwardPath != "/v1/messages" {
		t.Fatalf("anthropic route = %+v", anthropic)
	}
	if anthropic.UpstreamHost != "api.anthropic.com" {
		t.Fatalf("host = %s", anthropic.UpstreamHost)
	}

	legacy := routes.resolve("/v1/chat/completions")
	if legacy.Kind != ProviderLegacy || legacy.ForwardPath != "/v1/chat/completions" {
		t.Fatalf("legacy = %+v", legacy)
	}
	if legacy.UpstreamHost != "legacy.example" {
		t.Fatalf("legacy host = %s", legacy.UpstreamHost)
	}
}

func TestReservedPathsStayLegacy(t *testing.T) {
	legacyURL, _ := url.Parse("https://api.openai.com")
	cfg := config.Config{
		UpstreamHost:          "api.openai.com",
		OpenAIUpstreamURL:     "https://api.openai.com",
		OpenAIUpstreamHost:    "api.openai.com",
		AnthropicUpstreamURL:  "https://api.anthropic.com",
		AnthropicUpstreamHost: "api.anthropic.com",
	}
	routes := newProviderRoutes(cfg, legacyURL, "api.openai.com",
		usage.NewOpenAIExtractor(), usage.NewOpenAIStreamExtractor())

	for _, path := range []string{
		"/admin/v1/buckets",
		"/me/buckets",
		"/account",
		"/ops",
		"/signup",
		"/setup",
		"/billing/webhook",
		"/healthz",
		"/readyz",
	} {
		r := routes.resolve(path)
		if r.Kind != ProviderLegacy || r.ForwardPath != path {
			t.Fatalf("%s resolved as %+v, want legacy unchanged", path, r)
		}
	}
}

func TestProviderContextRoundTrip(t *testing.T) {
	route := ProviderRoute{
		Kind:         ProviderOpenAI,
		UpstreamHost: "api.openai.com",
		ForwardPath:  "/v1/x",
	}
	ctx := withProviderRoute(context.Background(), route)
	got, ok := providerRouteFromContext(ctx)
	if !ok || got.Kind != ProviderOpenAI || got.ForwardPath != "/v1/x" {
		t.Fatalf("got %+v ok=%v", got, ok)
	}
}

func TestDirectorUsesProviderRoute(t *testing.T) {
	openaiMock, _ := url.Parse("http://openai-mock.test")
	legacy, _ := url.Parse("http://legacy-mock.test")
	cfg := config.Config{
		UpstreamURL:           legacy.String(),
		UpstreamHost:          "legacy-mock.test",
		OpenAIUpstreamURL:     openaiMock.String(),
		OpenAIUpstreamHost:    "api.openai.com",
		AnthropicUpstreamURL:  "http://anthropic-mock.test",
		AnthropicUpstreamHost: "api.anthropic.com",
	}
	h, err := NewHandler(cfg, nil, NewEnforcement(cfg, nil, nil), nil, nil, nil, nil, nil, nil, nil, nil)
	if err != nil {
		t.Fatalf("NewHandler: %v", err)
	}

	req, _ := http.NewRequest(http.MethodPost, "http://proxy/openai/v1/chat/completions", nil)
	route := h.providers.resolve(req.URL.Path)
	req.URL.Path = route.ForwardPath
	req = req.WithContext(withProviderRoute(req.Context(), route))
	h.director(req)

	if req.URL.Scheme != "http" || req.URL.Host != "openai-mock.test" {
		t.Fatalf("url = %s://%s", req.URL.Scheme, req.URL.Host)
	}
	if req.URL.Path != "/v1/chat/completions" {
		t.Fatalf("path = %s", req.URL.Path)
	}
	if req.Host != "api.openai.com" {
		t.Fatalf("Host = %s", req.Host)
	}
}
