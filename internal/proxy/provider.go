package proxy

import (
	"context"
	"net/url"
	"strings"

	"github.com/saksham/token-guard-ai/internal/config"
	"github.com/saksham/token-guard-ai/internal/usage"
)

type ProviderKind string

const (
	ProviderOpenAI    ProviderKind = "openai"
	ProviderAnthropic ProviderKind = "anthropic"
	ProviderLegacy    ProviderKind = "legacy"
)

const providerRouteCtxKey ctxKey = "provider_route"

// ProviderRoute is the per-request upstream + extractors selected from the path.
type ProviderRoute struct {
	Kind         ProviderKind
	UpstreamURL  *url.URL
	UpstreamHost string
	ForwardPath  string
	JSON         usage.UsageExtractor
	Stream       usage.StreamExtractor
}

type providerRoutes struct {
	openaiURL       *url.URL
	openaiHost      string
	anthropicURL    *url.URL
	anthropicHost   string
	legacyURL       *url.URL
	legacyHost      string
	openaiJSON      usage.UsageExtractor
	openaiStream    usage.StreamExtractor
	anthropicJSON   usage.UsageExtractor
	anthropicStream usage.StreamExtractor
	legacyJSON      usage.UsageExtractor
	legacyStream    usage.StreamExtractor
}

func newProviderRoutes(
	cfg config.Config,
	legacyURL *url.URL,
	legacyHost string,
	legacyJSON usage.UsageExtractor,
	legacyStream usage.StreamExtractor,
) *providerRoutes {
	openaiURL, _ := url.Parse(cfg.OpenAIUpstreamURL)
	if openaiURL == nil || cfg.OpenAIUpstreamURL == "" {
		openaiURL, _ = url.Parse("https://api.openai.com")
	}
	anthropicURL, _ := url.Parse(cfg.AnthropicUpstreamURL)
	if anthropicURL == nil || cfg.AnthropicUpstreamURL == "" {
		anthropicURL, _ = url.Parse("https://api.anthropic.com")
	}
	openaiHost := cfg.OpenAIUpstreamHost
	if openaiHost == "" {
		openaiHost = "api.openai.com"
	}
	anthropicHost := cfg.AnthropicUpstreamHost
	if anthropicHost == "" {
		anthropicHost = "api.anthropic.com"
	}
	openaiReg := usage.RegistryForHost("api.openai.com")
	anthropicReg := usage.RegistryForHost("api.anthropic.com")
	return &providerRoutes{
		openaiURL:       openaiURL,
		openaiHost:      openaiHost,
		anthropicURL:    anthropicURL,
		anthropicHost:   anthropicHost,
		legacyURL:       legacyURL,
		legacyHost:      legacyHost,
		openaiJSON:      openaiReg.JSON,
		openaiStream:    openaiReg.Stream,
		anthropicJSON:   anthropicReg.JSON,
		anthropicStream: anthropicReg.Stream,
		legacyJSON:      legacyJSON,
		legacyStream:    legacyStream,
	}
}

func (p *providerRoutes) resolve(path string) ProviderRoute {
	if rest, ok := stripProviderPrefix(path, "openai"); ok {
		return ProviderRoute{
			Kind:         ProviderOpenAI,
			UpstreamURL:  p.openaiURL,
			UpstreamHost: p.openaiHost,
			ForwardPath:  rest,
			JSON:         p.openaiJSON,
			Stream:       p.openaiStream,
		}
	}
	if rest, ok := stripProviderPrefix(path, "anthropic"); ok {
		return ProviderRoute{
			Kind:         ProviderAnthropic,
			UpstreamURL:  p.anthropicURL,
			UpstreamHost: p.anthropicHost,
			ForwardPath:  rest,
			JSON:         p.anthropicJSON,
			Stream:       p.anthropicStream,
		}
	}
	return ProviderRoute{
		Kind:         ProviderLegacy,
		UpstreamURL:  p.legacyURL,
		UpstreamHost: p.legacyHost,
		ForwardPath:  path,
		JSON:         p.legacyJSON,
		Stream:       p.legacyStream,
	}
}

func stripProviderPrefix(path, prefix string) (rest string, ok bool) {
	base := "/" + prefix
	if path == base || path == base+"/" {
		return "/", true
	}
	if strings.HasPrefix(path, base+"/") {
		rest = path[len(base):]
		if rest == "" {
			rest = "/"
		}
		return rest, true
	}
	return "", false
}

func withProviderRoute(ctx context.Context, route ProviderRoute) context.Context {
	return context.WithValue(ctx, providerRouteCtxKey, route)
}

func providerRouteFromContext(ctx context.Context) (ProviderRoute, bool) {
	v, ok := ctx.Value(providerRouteCtxKey).(ProviderRoute)
	return v, ok
}
