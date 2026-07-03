package usage

import (
	"encoding/json"
	"fmt"
)

type anthropicExtractor struct{}

func NewAnthropicExtractor() UsageExtractor {
	return anthropicExtractor{}
}

type anthropicResponse struct {
	Usage struct {
		InputTokens  int64 `json:"input_tokens"`
		OutputTokens int64 `json:"output_tokens"`
	} `json:"usage"`
}

func (anthropicExtractor) ExtractFromJSON(body []byte) (Usage, error) {
	if len(body) == 0 {
		return Usage{}, fmt.Errorf("empty response body")
	}

	var resp anthropicResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		return Usage{}, fmt.Errorf("parse anthropic response: %w", err)
	}

	if resp.Usage.InputTokens == 0 && resp.Usage.OutputTokens == 0 {
		return Usage{}, fmt.Errorf("usage field missing or empty")
	}

	return Usage{
		PromptTokens:     resp.Usage.InputTokens,
		CompletionTokens: resp.Usage.OutputTokens,
	}, nil
}
