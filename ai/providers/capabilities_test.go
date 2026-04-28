package providers

import "testing"

func TestLookupModelCapabilities_LongestPrefixWins(t *testing.T) {
	caps := LookupModelCapabilities("openai.groq", "gpt-oss-120b")
	if !caps.SupportsJSONMode {
		t.Fatal("expected gpt-oss groq model to support JSON mode")
	}
}

func TestLookupModelCapabilities_UnknownAliasDefaultsConservative(t *testing.T) {
	caps := LookupModelCapabilities("openai.custom", "custom-model")
	if caps.ProviderAlias != "openai.custom" {
		t.Fatalf("expected provider alias to be preserved, got %q", caps.ProviderAlias)
	}
	if caps.ReasoningStyle != "" {
		t.Fatalf("expected unknown alias reasoning style to be empty, got %q", caps.ReasoningStyle)
	}
	if caps.SupportsJSONMode {
		t.Fatal("expected unknown alias to default to conservative JSON handling")
	}
}

func TestLookupModelCapabilities_DeepSeekReasonerMatch(t *testing.T) {
	caps := LookupModelCapabilities("openai.deepseek", "deepseek-reasoner")
	if caps.ReasoningStyle != "deepseek" {
		t.Fatalf("expected deepseek reasoning style, got %q", caps.ReasoningStyle)
	}
	if !caps.SupportsJSONMode {
		t.Fatal("expected deepseek-reasoner to support JSON mode")
	}
}

func TestLookupModelCapabilities_AnthropicClaudePrefixMatch(t *testing.T) {
	caps := LookupModelCapabilities("anthropic", "claude-sonnet-4-5-20250929")
	if caps.ReasoningStyle != "anthropic" {
		t.Fatalf("expected anthropic reasoning style, got %q", caps.ReasoningStyle)
	}
}

func TestLookupModelCapabilities_NativeOpenAICatchAll(t *testing.T) {
	caps := LookupModelCapabilities("openai", "gpt-4.1-2025-04-14")
	if caps.ProviderAlias != "openai" {
		t.Fatalf("expected provider alias openai, got %q", caps.ProviderAlias)
	}
	if caps.ModelPrefix != "" {
		t.Fatalf("expected catch-all model prefix, got %q", caps.ModelPrefix)
	}
	if !caps.SupportsJSONMode {
		t.Fatal("expected native openai catch-all to support JSON mode")
	}
}

func TestLookupModelCapabilities_GeminiPrefixMatch(t *testing.T) {
	caps := LookupModelCapabilities("gemini", "gemini-2.5-pro")
	if caps.ReasoningStyle != "gemini" {
		t.Fatalf("expected gemini reasoning style, got %q", caps.ReasoningStyle)
	}
	if !caps.SupportsJSONSchema {
		t.Fatal("expected gemini to support JSON schema")
	}
}

func TestLookupModelCapabilities_CaseInsensitiveMatching(t *testing.T) {
	caps := LookupModelCapabilities("openai.deepseek", "DEEPSEEK-REASONER")
	if caps.ReasoningStyle != "deepseek" {
		t.Fatalf("expected deepseek reasoning style for case-insensitive match, got %q", caps.ReasoningStyle)
	}
}
