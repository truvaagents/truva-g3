package modelcatalog_test

import (
	"testing"

	"github.com/truvaagents/truva-g3/ai/providers/openai/modelcatalog"
)

func TestResolveUsesBuiltInAliasesOverridesAndPassThrough(t *testing.T) {
	if got := modelcatalog.Resolve("", "smart"); got != "o3" {
		t.Fatalf("default provider smart = %q", got)
	}
	if got := modelcatalog.Resolve("openai.groq", "default"); got != "openai/gpt-oss-120b" {
		t.Fatalf("Groq default = %q", got)
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
	if got := second["openai"]["smart"]; got != "o3" {
		t.Fatalf("nested catalog was shared: %q", got)
	}
	if _, exists := second["openai.groq"]; !exists {
		t.Fatal("top-level catalog was shared")
	}
	if got := modelcatalog.Resolve("openai", "smart"); got != "o3" {
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
