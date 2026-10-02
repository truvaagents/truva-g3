package providers

import (
	"strings"

	"github.com/truvaagents/truva-g3/ai/internal/openaimodel"
)

// ModelCapabilities describes which advanced portable features are safe to translate
// for a given provider alias and model family.
type ModelCapabilities struct {
	ProviderAlias             string
	ModelPrefix               string
	ExactModel                bool
	MatchFamilyBoundary       bool   // ExactModel takes precedence; otherwise match the name or a hyphen suffix.
	ReasoningStyle            string // "", "openai", "anthropic", "gemini", "deepseek"
	SupportsJSONMode          bool
	SupportsJSONSchema        bool
	SupportsHeaderPassthrough bool
}

var modelCapabilities = []ModelCapabilities{
	{ProviderAlias: "openai", ModelPrefix: "", ReasoningStyle: "", SupportsJSONMode: true, SupportsJSONSchema: true, SupportsHeaderPassthrough: true},
	{ProviderAlias: "openai", ModelPrefix: "gpt-5", ReasoningStyle: "openai", SupportsJSONMode: true, SupportsJSONSchema: true, SupportsHeaderPassthrough: true},
	{ProviderAlias: "openai", ModelPrefix: "gpt-6", MatchFamilyBoundary: true, ReasoningStyle: "openai", SupportsJSONMode: true, SupportsJSONSchema: true, SupportsHeaderPassthrough: true},
	{ProviderAlias: "openai", ModelPrefix: "o1", ReasoningStyle: "openai", SupportsJSONMode: true, SupportsJSONSchema: true, SupportsHeaderPassthrough: true},
	{ProviderAlias: "openai", ModelPrefix: "o3", ReasoningStyle: "openai", SupportsJSONMode: true, SupportsJSONSchema: true, SupportsHeaderPassthrough: true},
	{ProviderAlias: "openai", ModelPrefix: "o4", ReasoningStyle: "openai", SupportsJSONMode: true, SupportsJSONSchema: true, SupportsHeaderPassthrough: true},

	{ProviderAlias: "anthropic", ModelPrefix: "claude-", ReasoningStyle: "anthropic", SupportsJSONMode: false, SupportsJSONSchema: false, SupportsHeaderPassthrough: true},
	{ProviderAlias: "gemini", ModelPrefix: "gemini-", ReasoningStyle: "gemini", SupportsJSONMode: true, SupportsJSONSchema: true, SupportsHeaderPassthrough: true},

	{ProviderAlias: "openai.openrouter", ModelPrefix: "", SupportsHeaderPassthrough: true},
	// JSON-object support verified on required-parameter, ZDR routes; this
	// does not declare schema support or a model-family reasoning wire format.
	{ProviderAlias: "openai.openrouter", ModelPrefix: "deepseek/deepseek-v4.1-flash", ExactModel: true, SupportsJSONMode: true, SupportsHeaderPassthrough: true},
	{ProviderAlias: "openai.openrouter", ModelPrefix: "z-ai/glm-5.3-flash", ExactModel: true, SupportsJSONMode: true, SupportsHeaderPassthrough: true},
	{ProviderAlias: "openai.openrouter", ModelPrefix: "openai/gpt-5.6-luna", ExactModel: true, ReasoningStyle: "openai", SupportsJSONMode: true, SupportsJSONSchema: true, SupportsHeaderPassthrough: true},
	// OpenRouter endpoint metadata reviewed 2026-09-28. Required-parameter routing
	// filters endpoints without JSON/schema support; this is not a live inference certification.
	{ProviderAlias: "openai.openrouter", ModelPrefix: "openai/gpt-6-luna", ExactModel: true, ReasoningStyle: "openai", SupportsJSONMode: true, SupportsJSONSchema: true, SupportsHeaderPassthrough: true},

	{ProviderAlias: "openai.groq", ModelPrefix: "", ReasoningStyle: "", SupportsJSONMode: false, SupportsJSONSchema: false, SupportsHeaderPassthrough: true},
	{ProviderAlias: "openai.groq", ModelPrefix: "gpt-oss", ReasoningStyle: "", SupportsJSONMode: true, SupportsJSONSchema: true, SupportsHeaderPassthrough: true},
	// Groq serves the gpt-oss family under the canonical OpenAI-namespaced ID (openai/gpt-oss-120b).
	// The matcher uses strings.HasPrefix, so this second row is needed for the slashed form.
	{ProviderAlias: "openai.groq", ModelPrefix: "openai/gpt-oss", ReasoningStyle: "", SupportsJSONMode: true, SupportsJSONSchema: true, SupportsHeaderPassthrough: true},

	{ProviderAlias: "openai.deepseek", ModelPrefix: "", ReasoningStyle: "", SupportsJSONMode: false, SupportsJSONSchema: false, SupportsHeaderPassthrough: true},
	{ProviderAlias: "openai.deepseek", ModelPrefix: "deepseek-reasoner", ReasoningStyle: "deepseek", SupportsJSONMode: true, SupportsJSONSchema: false, SupportsHeaderPassthrough: true},
	{ProviderAlias: "openai.deepseek", ModelPrefix: "deepseek-chat", ReasoningStyle: "", SupportsJSONMode: true, SupportsJSONSchema: false, SupportsHeaderPassthrough: true},

	{ProviderAlias: "openai.xai", ModelPrefix: "", ReasoningStyle: "", SupportsJSONMode: false, SupportsJSONSchema: false, SupportsHeaderPassthrough: true},
	{ProviderAlias: "openai.mistral", ModelPrefix: "", ReasoningStyle: "", SupportsJSONMode: false, SupportsJSONSchema: false, SupportsHeaderPassthrough: true},
	{ProviderAlias: "openai.qwen", ModelPrefix: "", ReasoningStyle: "", SupportsJSONMode: false, SupportsJSONSchema: false, SupportsHeaderPassthrough: true},
	{ProviderAlias: "openai.together", ModelPrefix: "", ReasoningStyle: "", SupportsJSONMode: false, SupportsJSONSchema: false, SupportsHeaderPassthrough: true},
	{ProviderAlias: "openai.ollama", ModelPrefix: "", ReasoningStyle: "openai", SupportsJSONMode: false, SupportsJSONSchema: false, SupportsHeaderPassthrough: true},
}

// LookupModelCapabilities returns the most specific capability record for the provider alias
// and model prefix. Rows declare exact, family-boundary, or legacy prefix matching.
// An empty prefix is a provider catch-all. Without a matching row the returned
// record has only ProviderAlias set, which callers must treat conservatively.
func LookupModelCapabilities(providerAlias, model string) ModelCapabilities {
	best := ModelCapabilities{ProviderAlias: providerAlias}
	bestLen := -1

	for _, candidate := range modelCapabilities {
		if candidate.ProviderAlias != providerAlias {
			continue
		}

		prefixLower := strings.ToLower(candidate.ModelPrefix)
		if candidate.matchesModel(model) {
			if len(prefixLower) > bestLen {
				best = candidate
				bestLen = len(prefixLower)
			}
		}
	}

	return best
}

func (candidate ModelCapabilities) matchesModel(model string) bool {
	prefix := strings.ToLower(candidate.ModelPrefix)
	switch {
	case prefix == "":
		return true
	case candidate.ExactModel:
		return strings.ToLower(model) == prefix
	case candidate.MatchFamilyBoundary:
		return openaimodel.MatchesFamily(model, prefix)
	default:
		return strings.HasPrefix(strings.ToLower(model), prefix)
	}
}
