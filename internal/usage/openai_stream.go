package usage

import "github.com/saksham/token-guard-ai/internal/usage/sse"

type StreamExtractor interface {
	ExtractFromEvent(ev sse.Event) (Usage, bool)
}

type openAIStreamExtractor struct {
	json UsageExtractor
}

func NewOpenAIStreamExtractor() StreamExtractor {
	return &openAIStreamExtractor{json: NewOpenAIExtractor()}
}

func (e *openAIStreamExtractor) ExtractFromEvent(ev sse.Event) (Usage, bool) {
	if ev.Done || ev.Data == "" {
		return Usage{}, false
	}

	u, err := e.json.ExtractFromJSON([]byte(ev.Data))
	if err != nil {
		return Usage{}, false
	}
	if u.Total() <= 0 {
		return Usage{}, false
	}
	return u, true
}
