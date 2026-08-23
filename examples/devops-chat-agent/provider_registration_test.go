package main

import (
	"reflect"
	"testing"

	"github.com/truvaagents/truva-g3/ai"
)

func TestAutoDetectedProviderOrderIncludesGemini(t *testing.T) {
	for _, key := range []string{
		"OPENAI_API_KEY",
		"ANTHROPIC_API_KEY",
		"OPENROUTER_API_KEY",
		"GEMINI_API_KEY",
		"GOOGLE_API_KEY",
		"GROQ_API_KEY",
		"DEEPSEEK_API_KEY",
		"XAI_API_KEY",
		"MISTRAL_API_KEY",
		"QWEN_API_KEY",
		"TOGETHER_API_KEY",
		"OLLAMA_BASE_URL",
	} {
		t.Setenv(key, "")
	}
	t.Setenv("OPENAI_API_KEY", "test-openai")
	t.Setenv("ANTHROPIC_API_KEY", "test-anthropic")
	t.Setenv("OPENROUTER_API_KEY", "test-openrouter")
	t.Setenv("GEMINI_API_KEY", "test-gemini")
	t.Setenv("GROQ_API_KEY", "test-groq")

	available := ai.DetectAvailableProviders(nil)
	got := make([]string, len(available))
	for index, provider := range available {
		got[index] = provider.Alias
	}
	want := []string{
		"openai",
		"anthropic",
		"openai.openrouter",
		"gemini",
		"openai.groq",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("auto-detected providers = %v, want %v", got, want)
	}
}
