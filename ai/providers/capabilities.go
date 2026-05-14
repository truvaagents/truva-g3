package providers

import "strings"

// ModelCapabilities describes which advanced portable features are safe to translate
// for a given provider alias and model family.
type ModelCapabilities struct {
	ProviderAlias             string
	ModelPrefix               string
	ReasoningStyle            string // "", "openai", "anthropic", "gemini", "deepseek"
	SupportsJSONMode          bool
	SupportsJSONSchema        bool
	SupportsHeaderPassthrough bool
}

var modelCapabilities = []ModelCapabilities{
	{ProviderAlias: "openai", ModelPrefix: "", ReasoningStyle: "", SupportsJSONMode: true, SupportsJSONSchema: true, SupportsHeaderPassthrough: true},
	{ProviderAlias: "openai", ModelPrefix: "gpt-5", ReasoningStyle: "openai", SupportsJSONMode: true, SupportsJSONSchema: true, SupportsHeaderPassthrough: true},
	{ProviderAlias: "openai", ModelPrefix: "o1", ReasoningStyle: "openai", SupportsJSONMode: true, SupportsJSONSchema: true, SupportsHeaderPassthrough: true},
	{ProviderAlias: "openai", ModelPrefix: "o3", ReasoningStyle: "openai", SupportsJSONMode: true, SupportsJSONSchema: true, SupportsHeaderPassthrough: true},
	{ProviderAlias: "openai", ModelPrefix: "o4", ReasoningStyle: "openai", SupportsJSONMode: true, SupportsJSONSchema: true, SupportsHeaderPassthrough: true},

	{ProviderAlias: "anthropic", ModelPrefix: "claude-", ReasoningStyle: "anthropic", SupportsJSONMode: false, SupportsJSONSchema: false, SupportsHeaderPassthrough: true},
	{ProviderAlias: "gemini", ModelPrefix: "gemini-", ReasoningStyle: "gemini", SupportsJSONMode: true, SupportsJSONSchema: true, SupportsHeaderPassthrough: true},

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
// and model prefix. Unknown aliases and models return a zero-value capability record, which
// callers must treat conservatively.
func LookupModelCapabilities(providerAlias, model string) ModelCapabilities {
	best := ModelCapabilities{ProviderAlias: providerAlias}
	bestLen := -1
	modelLower := strings.ToLower(model)

	for _, candidate := range modelCapabilities {
		if candidate.ProviderAlias != providerAlias {
			continue
		}

		prefixLower := strings.ToLower(candidate.ModelPrefix)
		if prefixLower == "" || strings.HasPrefix(modelLower, prefixLower) {
			if len(prefixLower) > bestLen {
				best = candidate
				bestLen = len(prefixLower)
			}
		}
	}

	return best
}
