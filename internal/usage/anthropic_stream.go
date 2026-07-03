package usage

import (
	"encoding/json"

	"github.com/saksham/token-guard-ai/internal/usage/sse"
)

type anthropicStreamExtractor struct{}

func NewAnthropicStreamExtractor() StreamExtractor {
	return &anthropicStreamExtractor{}
}

type anthropicStreamEvent struct {
	Type string `json:"type"`
	Usage *struct {
		InputTokens  int64 `json:"input_tokens"`
		OutputTokens int64 `json:"output_tokens"`
	} `json:"usage"`
	Message *struct {
		Usage struct {
			InputTokens  int64 `json:"input_tokens"`
			OutputTokens int64 `json:"output_tokens"`
		} `json:"usage"`
	} `json:"message"`
}

func (e *anthropicStreamExtractor) ExtractFromEvent(ev sse.Event) (Usage, bool) {
	if ev.Done || ev.Data == "" {
		return Usage{}, false
	}

	var event anthropicStreamEvent
	if err := json.Unmarshal([]byte(ev.Data), &event); err != nil {
		return Usage{}, false
	}

	switch event.Type {
	case "message_stop":
		return anthropicUsageFromEvent(event)
	default:
		return Usage{}, false
	}
}

func anthropicUsageFromEvent(event anthropicStreamEvent) (Usage, bool) {
	if event.Message != nil {
		in := event.Message.Usage.InputTokens
		out := event.Message.Usage.OutputTokens
		if in > 0 || out > 0 {
			return Usage{PromptTokens: in, CompletionTokens: out}, true
		}
	}
	if event.Usage != nil {
		in := event.Usage.InputTokens
		out := event.Usage.OutputTokens
		if in > 0 || out > 0 {
			return Usage{PromptTokens: in, CompletionTokens: out}, true
		}
	}
	return Usage{}, false
}
