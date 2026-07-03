package usage

import "testing"

func TestRegistryForHostOpenAI(t *testing.T) {
	reg := RegistryForHost("api.openai.com")
	if _, ok := reg.JSON.(openAIExtractor); !ok {
		t.Fatalf("JSON extractor type = %T, want openAIExtractor", reg.JSON)
	}
	if _, ok := reg.Stream.(*openAIStreamExtractor); !ok {
		t.Fatalf("Stream extractor type = %T, want openAIStreamExtractor", reg.Stream)
	}
}

func TestRegistryForHostAnthropic(t *testing.T) {
	reg := RegistryForHost("api.anthropic.com")
	if _, ok := reg.JSON.(anthropicExtractor); !ok {
		t.Fatalf("JSON extractor type = %T, want anthropicExtractor", reg.JSON)
	}
	if _, ok := reg.Stream.(*anthropicStreamExtractor); !ok {
		t.Fatalf("Stream extractor type = %T, want anthropicStreamExtractor", reg.Stream)
	}
}

func TestRegistryForHostDefault(t *testing.T) {
	reg := RegistryForHost("custom.provider.test")
	if _, ok := reg.JSON.(openAIExtractor); !ok {
		t.Fatalf("unknown host should default to OpenAI JSON, got %T", reg.JSON)
	}
}
