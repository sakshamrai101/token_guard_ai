package usage

import (
	"os"
	"testing"

	"github.com/saksham/token-guard-ai/internal/usage/sse"
)

func TestAnthropicStreamExtractorFromFixture(t *testing.T) {
	raw, err := os.ReadFile("testdata/anthropic_stream.sse")
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}

	ext := NewAnthropicStreamExtractor()
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
	if got.PromptTokens != 10 || got.CompletionTokens != 20 {
		t.Fatalf("usage = %+v, want input=10 output=20", got)
	}
	if got.Total() != 30 {
		t.Fatalf("Total() = %d, want 30", got.Total())
	}
}

func TestAnthropicStreamExtractorIgnoresContentDelta(t *testing.T) {
	ext := NewAnthropicStreamExtractor()
	ev := sse.Event{Data: `{"type":"content_block_delta","delta":{"type":"text_delta","text":"hi"}}`}
	_, ok := ext.ExtractFromEvent(ev)
	if ok {
		t.Fatal("expected no usage from content_block_delta")
	}
}

func TestAnthropicStreamExtractorMessageDeltaNoCompleteUsage(t *testing.T) {
	ext := NewAnthropicStreamExtractor()
	ev := sse.Event{Data: `{"type":"message_delta","usage":{"output_tokens":5}}`}
	_, ok := ext.ExtractFromEvent(ev)
	if ok {
		t.Fatal("expected no usage from partial message_delta (wait for message_stop)")
	}
}

func TestAnthropicStreamExtractorMessageStop(t *testing.T) {
	ext := NewAnthropicStreamExtractor()
	ev := sse.Event{Data: `{"type":"message_stop","message":{"usage":{"input_tokens":7,"output_tokens":3}}}`}
	got, ok := ext.ExtractFromEvent(ev)
	if !ok {
		t.Fatal("expected usage from message_stop")
	}
	if got.Total() != 10 {
		t.Fatalf("Total() = %d, want 10", got.Total())
	}
}

func TestAnthropicStreamExtractorMessageStopTopLevelUsage(t *testing.T) {
	ext := NewAnthropicStreamExtractor()
	ev := sse.Event{Data: `{"type":"message_stop","usage":{"input_tokens":4,"output_tokens":6}}`}
	got, ok := ext.ExtractFromEvent(ev)
	if !ok {
		t.Fatal("expected usage from message_stop top-level usage")
	}
	if got.Total() != 10 {
		t.Fatalf("Total() = %d, want 10", got.Total())
	}
}

func TestAnthropicStreamExtractorIgnoresDone(t *testing.T) {
	ext := NewAnthropicStreamExtractor()
	_, ok := ext.ExtractFromEvent(sse.Event{Done: true})
	if ok {
		t.Fatal("expected no usage from Done event")
	}
}
