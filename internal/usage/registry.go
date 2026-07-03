package usage

// Registry holds provider-specific usage extractors for JSON and SSE responses.
type Registry struct {
	JSON   UsageExtractor
	Stream StreamExtractor
}

// RegistryForHost selects extractors by upstream hostname.
// api.anthropic.com → Anthropic; all other hosts default to OpenAI.
func RegistryForHost(host string) Registry {
	switch host {
	case "api.anthropic.com":
		return Registry{
			JSON:   NewAnthropicExtractor(),
			Stream: NewAnthropicStreamExtractor(),
		}
	default:
		return Registry{
			JSON:   NewOpenAIExtractor(),
			Stream: NewOpenAIStreamExtractor(),
		}
	}
}
