package usage

import (
	"os"
	"testing"
)

func TestOpenAIExtractFromJSONFixture(t *testing.T) {
	body, err := os.ReadFile("testdata/openai_completion.json")
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}

	ext := NewOpenAIExtractor()
	got, err := ext.ExtractFromJSON(body)
	if err != nil {
		t.Fatalf("ExtractFromJSON: %v", err)
	}
	if got.PromptTokens != 10 || got.CompletionTokens != 20 || got.TotalTokens != 30 {
		t.Fatalf("usage = %+v, want prompt=10 completion=20 total=30", got)
	}
	if got.Total() != 30 {
		t.Fatalf("Total() = %d, want 30", got.Total())
	}
}

func TestOpenAIExtractFromJSONInline(t *testing.T) {
	ext := NewOpenAIExtractor()
	body := []byte(`{"choices":[],"usage":{"prompt_tokens":5,"completion_tokens":15,"total_tokens":20}}`)
	got, err := ext.ExtractFromJSON(body)
	if err != nil {
		t.Fatalf("ExtractFromJSON: %v", err)
	}
	if got.Total() != 20 {
		t.Fatalf("Total() = %d, want 20", got.Total())
	}
}

func TestOpenAIExtractFromJSONMissingUsage(t *testing.T) {
	ext := NewOpenAIExtractor()
	_, err := ext.ExtractFromJSON([]byte(`{"id":"chatcmpl-no-usage"}`))
	if err == nil {
		t.Fatal("expected error when usage is missing")
	}
}

func TestOpenAIExtractFromJSONInvalidJSON(t *testing.T) {
	ext := NewOpenAIExtractor()
	_, err := ext.ExtractFromJSON([]byte(`{invalid`))
	if err == nil {
		t.Fatal("expected error for invalid JSON")
	}
}

func TestOpenAIExtractTotalFromPromptAndCompletion(t *testing.T) {
	ext := NewOpenAIExtractor()
	body := []byte(`{"usage":{"prompt_tokens":7,"completion_tokens":3}}`)
	got, err := ext.ExtractFromJSON(body)
	if err != nil {
		t.Fatalf("ExtractFromJSON: %v", err)
	}
	if got.Total() != 10 {
		t.Fatalf("Total() = %d, want 10", got.Total())
	}
}
