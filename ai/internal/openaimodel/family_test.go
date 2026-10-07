package openaimodel

import "testing"

func TestMatchesFamily(t *testing.T) {
	for _, tc := range []struct {
		model, family string
		want          bool
	}{
		{"nova", "nova", true},
		{" NOVA-FAST-2026-09-26 ", " Nova ", true},
		{"nova2", "nova", false},
		{"noval", "nova", false},
		{"vendor/nova-fast", "nova", false},
		{"nova", "", false},
		{"-suffix", " ", false},
		{"", "nova", false},
	} {
		if got := MatchesFamily(tc.model, tc.family); got != tc.want {
			t.Errorf("MatchesFamily(%q, %q) = %v, want %v", tc.model, tc.family, got, tc.want)
		}
	}
}

func TestGPT6FamilyBoundaries(t *testing.T) {
	for _, tc := range []struct {
		model        string
		family, none bool
	}{
		{"gpt-6-astra", true, false},
		{"gpt-6-sol", true, true},
		{"gpt-6.1-sol", true, false},
		{"gpt-6.1-sol-pro", true, false},
		{"gpt-6.1-sol-custom", true, false},
		{"gpt-6-sol-pro", true, true},
		{" GPT-6.1-SOL-2026-09-29 ", true, false},
		{"gpt-6.1-solstice", false, false},
		{"gpt-6.10-sol", false, false},
		{"gpt-6.1-unknown", false, false},
		{"openai/gpt-6.1-sol", false, false},
		{" GPT-6-LUNA ", true, true},
		{"gpt-6-luna-2026-09-01", true, true},
		{"gpt-6-astra-2026-09-01", true, false},
		{"gpt-6", true, false},
		{"gpt-6-future", true, false},
		{"gpt-6-solstice", true, false},
		{"gpt-60", false, false},
		{"gpt-6ish", false, false},
		{"openai/gpt-6-sol", false, false},
		{"gpt-5.6-terra", false, false},
		{"", false, false},
	} {
		t.Run(tc.model, func(t *testing.T) {
			if IsGPT6(tc.model) != tc.family || SupportsGPT6NoReasoning(tc.model) != tc.none {
				t.Fatalf("incorrect family contract for %q", tc.model)
			}
		})
	}
}
