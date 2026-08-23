package modelcatalog_test

import (
	"testing"

	"github.com/truvaagents/truva-g3/ai/providers/openai/modelcatalog"
)

func TestResolveUsesBuiltInAliasesOverridesAndPassThrough(t *testing.T) {
	if got := modelcatalog.Resolve("", "default"); got != "gpt-5.6-terra" {
		t.Fatalf("default provider default = %q", got)
	}
	if got := modelcatalog.Resolve("", "fast"); got != "gpt-5.6-luna" {
		t.Fatalf("default provider fast = %q", got)
	}
	if got := modelcatalog.Resolve("", "smart"); got != "gpt-5.6-sol" {
		t.Fatalf("default provider smart = %q", got)
	}
	if got := modelcatalog.Resolve("", "code"); got != "gpt-5.6-sol" {
		t.Fatalf("default provider code = %q", got)
	}
	if got := modelcatalog.Resolve("", "premium"); got != "gpt-5.6-sol" {
		t.Fatalf("default provider premium = %q", got)
	}
	if got := modelcatalog.Resolve("openai.groq", "default"); got != "openai/gpt-oss-120b" {
		t.Fatalf("Groq default = %q", got)
	}
	if got := modelcatalog.Resolve("openai.together", "default"); got != "deepseek-ai/DeepSeek-V4-Flash-0731" {
		t.Fatalf("Together default = %q", got)
	}
	if got := modelcatalog.Resolve("openai.together", "fast"); got != "google/gemma-4-31B-it" {
		t.Fatalf("Together fast = %q", got)
	}
	if got := modelcatalog.Resolve("openai.together", "smart"); got != "moonshotai/Kimi-K3" {
		t.Fatalf("Together smart = %q", got)
	}
	if got := modelcatalog.Resolve("openai.together", "code"); got != "moonshotai/Kimi-K3" {
		t.Fatalf("Together code = %q", got)
	}
	if got := modelcatalog.Resolve("openai.openrouter", "default"); got != "openrouter/auto" {
		t.Fatalf("OpenRouter default = %q", got)
	}
	if got := modelcatalog.Resolve("openai.openrouter", "smart"); got != "openrouter/auto" {
		t.Fatalf("OpenRouter smart = %q", got)
	}
	if got := modelcatalog.Resolve("openai.openrouter", "fast"); got != "openai/gpt-5.6-luna" {
		t.Fatalf("OpenRouter fast = %q", got)
	}
	if got := modelcatalog.Resolve("openai.openrouter", "code"); got != "openrouter/pareto-code" {
		t.Fatalf("OpenRouter code = %q", got)
	}
	if got := modelcatalog.Resolve("openai.openrouter", "openai/gpt-5.6-sol"); got != "openai/gpt-5.6-sol" {
		t.Fatalf("OpenRouter concrete pass-through = %q", got)
	}
	if got := modelcatalog.Resolve("openai.openrouter", "liquid/lfm-2.5-2.6b:free"); got != "liquid/lfm-2.5-2.6b:free" {
		t.Fatalf("OpenRouter :free pass-through = %q", got)
	}
	t.Setenv("TRUVAG3_OPENROUTER_MODEL_DEFAULT", "openai/gpt-5.6-sol")
	if got := modelcatalog.Resolve("openai.openrouter", "default"); got != "openai/gpt-5.6-sol" {
		t.Fatalf("OpenRouter environment override = %q", got)
	}
	if got := modelcatalog.Resolve("openai", "deployment-name"); got != "deployment-name" {
		t.Fatalf("unknown model pass-through = %q", got)
	}
	t.Setenv("TRUVAG3_OPENAI_MODEL_SMART", "semantic-override")
	if got := modelcatalog.Resolve("openai", "smart"); got != "semantic-override" {
		t.Fatalf("environment override = %q", got)
	}
}

func TestDefaultAliasesReturnsDeepCopies(t *testing.T) {
	first := modelcatalog.DefaultAliases()
	second := modelcatalog.DefaultAliases()
	first["openai"]["smart"] = "mutated"
	delete(first, "openai.groq")
	if got := second["openai"]["smart"]; got != "gpt-5.6-sol" {
		t.Fatalf("nested catalog was shared: %q", got)
	}
	if _, exists := second["openai.groq"]; !exists {
		t.Fatal("top-level catalog was shared")
	}
	if got := modelcatalog.Resolve("openai", "smart"); got != "gpt-5.6-sol" {
		t.Fatalf("built-in catalog was mutated: %q", got)
	}
}

func TestResolveWithAliasesHonorsSuppliedCompatibilityCatalog(t *testing.T) {
	aliases := modelcatalog.DefaultAliases()
	aliases["openai"]["smart"] = "runtime-model"
	if got := modelcatalog.ResolveWithAliases(aliases, "openai", "smart"); got != "runtime-model" {
		t.Fatalf("supplied catalog mutation = %q", got)
	}
}
