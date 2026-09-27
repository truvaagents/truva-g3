// Package openaimodel holds side-effect-free OpenAI model-family recognition
// shared by capability metadata and wire-profile selection.
package openaimodel

import "strings"

// IsGPT6 recognizes the GPT-6 family, including suffixed model snapshots,
// without matching unrelated names such as gpt-60 or vendor/gpt-6-sol.
func IsGPT6(model string) bool {
	return MatchesFamily(model, "gpt-6")
}

// SupportsGPT6NoReasoning identifies GPT-6 variants documented to accept none.
// Unrecognized variants inherit only the common active-reasoning contract.
func SupportsGPT6NoReasoning(model string) bool {
	return MatchesFamily(model, "gpt-6-sol") || MatchesFamily(model, "gpt-6-luna")
}

// MatchesFamily matches a name exactly or at a hyphen-delimited family boundary.
// Names are case-insensitive and surrounding whitespace is ignored. An empty
// family never matches; capability-table catch-all rows are handled separately.
func MatchesFamily(model, family string) bool {
	normalized := strings.ToLower(strings.TrimSpace(model))
	family = strings.ToLower(strings.TrimSpace(family))
	if family == "" {
		return false
	}
	return normalized == family || strings.HasPrefix(normalized, family+"-")
}
