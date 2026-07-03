package usage

import (
	"os"
	"testing"
)

func TestAnthropicExtractFromJSONFixture(t *testing.T) {
	body, err := os.ReadFile("testdata/anthropic_completion.json")
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}

	ext := NewAnthropicExtractor()
	got, err := ext.ExtractFromJSON(body)
	if err != nil {
		t.Fatalf("ExtractFromJSON: %v", err)
	}
	if got.PromptTokens != 10 || got.CompletionTokens != 20 {
		t.Fatalf("usage = %+v, want input=10 output=20", got)
	}
	if got.Total() != 30 {
		t.Fatalf("Total() = %d, want 30 (input + output)", got.Total())
	}
}

func TestAnthropicExtractFromJSONInline(t *testing.T) {
	ext := NewAnthropicExtractor()
	body := []byte(`{"content":[],"usage":{"input_tokens":5,"output_tokens":15}}`)
	got, err := ext.ExtractFromJSON(body)
	if err != nil {
		t.Fatalf("ExtractFromJSON: %v", err)
	}
	if got.Total() != 20 {
		t.Fatalf("Total() = %d, want 20", got.Total())
	}
}

func TestAnthropicExtractFromJSONMissingUsage(t *testing.T) {
	ext := NewAnthropicExtractor()
	_, err := ext.ExtractFromJSON([]byte(`{"id":"msg-no-usage","type":"message"}`))
	if err == nil {
		t.Fatal("expected error when usage is missing")
	}
}

func TestAnthropicExtractFromJSONInvalidJSON(t *testing.T) {
	ext := NewAnthropicExtractor()
	_, err := ext.ExtractFromJSON([]byte(`{invalid`))
	if err == nil {
		t.Fatal("expected error for invalid JSON")
	}
}
