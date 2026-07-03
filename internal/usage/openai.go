package usage

import (
	"encoding/json"
	"fmt"
)

type openAIExtractor struct{}

func NewOpenAIExtractor() UsageExtractor {
	return openAIExtractor{}
}

type openAIResponse struct {
	Usage struct {
		PromptTokens     int64 `json:"prompt_tokens"`
		CompletionTokens int64 `json:"completion_tokens"`
		TotalTokens      int64 `json:"total_tokens"`
	} `json:"usage"`
}

func (openAIExtractor) ExtractFromJSON(body []byte) (Usage, error) {
	if len(body) == 0 {
		return Usage{}, fmt.Errorf("empty response body")
	}

	var resp openAIResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		return Usage{}, fmt.Errorf("parse openai response: %w", err)
	}

	if resp.Usage.PromptTokens == 0 && resp.Usage.CompletionTokens == 0 && resp.Usage.TotalTokens == 0 {
		return Usage{}, fmt.Errorf("usage field missing or empty")
	}

	return Usage{
		PromptTokens:     resp.Usage.PromptTokens,
		CompletionTokens: resp.Usage.CompletionTokens,
		TotalTokens:      resp.Usage.TotalTokens,
	}, nil
}
