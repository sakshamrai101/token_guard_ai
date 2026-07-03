package usage

import (
	"os"
	"testing"

	"github.com/saksham/token-guard-ai/internal/usage/sse"
)

func TestOpenAIStreamExtractorFromFixture(t *testing.T) {
	raw, err := os.ReadFile("testdata/openai_stream.sse")
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}

	ext := NewOpenAIStreamExtractor()
	p := sse.NewParser()

	var got Usage
	var found bool
	for _, ev := range p.Feed(raw) {
		if u, ok := ext.ExtractFromEvent(ev); ok {
			got = u
			found = true
		}
	}

	if !found {
		t.Fatal("expected usage from stream fixture")
	}
	if got.Total() != 30 {
		t.Fatalf("Total() = %d, want 30", got.Total())
	}
}

func TestOpenAIStreamExtractorIgnoresChunksWithoutUsage(t *testing.T) {
	ext := NewOpenAIStreamExtractor()
	ev := sse.Event{Data: `{"choices":[{"delta":{"content":"hi"}}]}`}
	_, ok := ext.ExtractFromEvent(ev)
	if ok {
		t.Fatal("expected no usage from delta-only chunk")
	}
}

func TestOpenAIStreamExtractorIgnoresDone(t *testing.T) {
	ext := NewOpenAIStreamExtractor()
	_, ok := ext.ExtractFromEvent(sse.Event{Done: true})
	if ok {
		t.Fatal("expected no usage from Done event")
	}
}

func TestOpenAIStreamExtractorUsageChunk(t *testing.T) {
	ext := NewOpenAIStreamExtractor()
	ev := sse.Event{Data: `{"choices":[],"usage":{"prompt_tokens":5,"completion_tokens":15,"total_tokens":20}}`}
	got, ok := ext.ExtractFromEvent(ev)
	if !ok {
		t.Fatal("expected usage")
	}
	if got.Total() != 20 {
		t.Fatalf("Total() = %d, want 20", got.Total())
	}
}
