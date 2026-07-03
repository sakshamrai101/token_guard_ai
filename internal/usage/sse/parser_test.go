package sse

import (
	"os"
	"strings"
	"testing"
)

func TestParserFixture(t *testing.T) {
	raw, err := os.ReadFile("../testdata/openai_stream.sse")
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}

	p := NewParser()
	var events []Event
	events = append(events, p.Feed(raw)...)

	if len(events) < 3 {
		t.Fatalf("events = %d, want at least 3", len(events))
	}
	if events[0].Data == "" || events[0].Done {
		t.Fatalf("first event = %+v, want data chunk", events[0])
	}
	if !strings.Contains(events[1].Data, "total_tokens") {
		t.Fatalf("usage event = %q", events[1].Data)
	}
	if !events[len(events)-1].Done {
		t.Fatalf("last event = %+v, want Done", events[len(events)-1])
	}
}

func TestParserCommentsAndBlankLines(t *testing.T) {
	p := NewParser()
	events := p.Feed([]byte(": ping\n\ndata: hello\n\n"))
	if len(events) != 1 {
		t.Fatalf("events = %d, want 1", len(events))
	}
	if events[0].Data != "hello" {
		t.Fatalf("data = %q, want hello", events[0].Data)
	}
}

func TestParserDoneEvent(t *testing.T) {
	p := NewParser()
	events := p.Feed([]byte("data: [DONE]\n\n"))
	if len(events) != 1 {
		t.Fatalf("events = %d, want 1", len(events))
	}
	if !events[0].Done {
		t.Fatal("expected Done event")
	}
}

func TestParserPartialLinesAcrossFeeds(t *testing.T) {
	p := NewParser()
	var events []Event
	events = append(events, p.Feed([]byte("da"))...)
	events = append(events, p.Feed([]byte("ta: partial\n"))...)
	events = append(events, p.Feed([]byte("\n"))...)

	if len(events) != 1 {
		t.Fatalf("events = %d, want 1", len(events))
	}
	if events[0].Data != "partial" {
		t.Fatalf("data = %q, want partial", events[0].Data)
	}
}

func TestParserMultiLineDataField(t *testing.T) {
	p := NewParser()
	input := "data: line1\n" +
		"data: line2\n" +
		"\n"
	events := p.Feed([]byte(input))
	if len(events) != 1 {
		t.Fatalf("events = %d, want 1", len(events))
	}
	if events[0].Data != "line1\nline2" {
		t.Fatalf("data = %q, want line1\\nline2", events[0].Data)
	}
}

func TestParserFlushTrailingLine(t *testing.T) {
	p := NewParser()
	_ = p.Feed([]byte("data: tail"))
	events := p.Flush()
	if len(events) != 1 {
		t.Fatalf("events = %d, want 1", len(events))
	}
	if events[0].Data != "tail" {
		t.Fatalf("data = %q, want tail", events[0].Data)
	}
}
