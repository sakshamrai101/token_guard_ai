package budget

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
)

type EstimateConfig struct {
	DefaultEstimate int64
	PromptBuffer    int64
}

type chatCompletionRequest struct {
	MaxTokens *int64 `json:"max_tokens"` // OpenAI chat completions and Anthropic Messages API
}

func EstimateFromBody(body []byte, cfg EstimateConfig, logger *slog.Logger) int64 {
	if len(body) == 0 {
		return cfg.DefaultEstimate
	}

	var req chatCompletionRequest
	if err := json.Unmarshal(body, &req); err != nil {
		if logger != nil {
			logger.Warn("failed to parse request body for estimate, using default", "error", err)
		}
		return cfg.DefaultEstimate
	}

	if req.MaxTokens == nil || *req.MaxTokens <= 0 {
		return cfg.DefaultEstimate
	}

	return *req.MaxTokens + cfg.PromptBuffer
}

func ReadAndRestoreBody(r io.ReadCloser) ([]byte, io.ReadCloser, error) {
	if r == nil {
		return nil, io.NopCloser(bytes.NewReader(nil)), nil
	}
	body, err := io.ReadAll(r)
	if err != nil {
		return nil, r, err
	}
	_ = r.Close()
	return body, io.NopCloser(bytes.NewReader(body)), nil
}
