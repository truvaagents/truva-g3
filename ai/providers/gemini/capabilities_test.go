package gemini

import (
	"strings"
	"testing"
)

func TestCapabilitySnapshotMatchesCoverageAndAliasInventory(t *testing.T) {
	covered := make(map[string]modelCoverageDecision)
	for _, decision := range modelCoverageSnapshot {
		if decision.ModelID == "" || decision.Surface != "generate-content" ||
			decision.LifecycleSource == "" || decision.CapabilitySource == "" ||
			decision.EvaluatedOn != "2026-08-17" {
			t.Fatalf("incomplete coverage decision: %#v", decision)
		}
		if strings.Contains(decision.ModelID, "latest") {
			t.Fatalf("floating model in coverage snapshot: %q", decision.ModelID)
		}
		if decision.Included {
			if _, duplicate := covered[decision.ModelID]; duplicate {
				t.Fatalf("duplicate included model %q", decision.ModelID)
			}
			if !lifecycleAllowsInclusion(decision.ShutdownOn, decision.DaysRemaining) {
				t.Fatalf("included model %q is inside exclusion threshold: %#v", decision.ModelID, decision)
			}
			if decision.ShutdownOn == "" && decision.DaysRemaining != -1 {
				t.Fatalf("included model %q has no date but days remaining = %d", decision.ModelID, decision.DaysRemaining)
			}
			covered[decision.ModelID] = decision
			continue
		}
		if lifecycleAllowsInclusion(decision.ShutdownOn, decision.DaysRemaining) || decision.ExclusionExplanation == "" {
			t.Fatalf("invalid excluded-model rationale: %#v", decision)
		}
	}

	capabilities := make(map[string]modelCapabilities)
	for _, capability := range capabilitySnapshot {
		if capability.ModelID == "" || strings.Contains(capability.ModelID, "latest") {
			t.Fatalf("invalid capability model ID %q", capability.ModelID)
		}
		if _, duplicate := capabilities[capability.ModelID]; duplicate {
			t.Fatalf("duplicate capability row %q", capability.ModelID)
		}
		if capability.Methods != methodGenerate|methodStream ||
			capability.Surfaces != surfaceGenerateContent ||
			capability.InputTokenLimit <= 0 || capability.OutputTokenLimit <= 0 {
			t.Fatalf("incomplete capability row: %#v", capability)
		}
		if capability.forbidsSampling() {
			if capability.Temperature.Supported || capability.TopP.Supported || capability.TopK.Supported {
				t.Fatalf("forbidden sampling has advertised capability: %#v", capability)
			}
		} else if !capability.Temperature.Supported || !capability.TopP.Supported || !capability.TopK.Supported {
			t.Fatalf("supported sampling row is incomplete: %#v", capability)
		}
		capabilities[capability.ModelID] = capability
	}
	if len(capabilities) != len(covered) {
		t.Fatalf("capability rows = %d, included coverage rows = %d", len(capabilities), len(covered))
	}
	for model := range covered {
		if _, ok := capabilities[model]; !ok {
			t.Fatalf("included model %q has no capability row", model)
		}
	}
	for alias, model := range modelAliases {
		if strings.Contains(model, "latest") {
			t.Fatalf("alias %q uses floating model %q", alias, model)
		}
		if _, ok := covered[model]; !ok {
			t.Fatalf("alias %q targets uncovered model %q", alias, model)
		}
	}
}

func TestLifecycleInclusionThresholdBoundaries(t *testing.T) {
	tests := []struct {
		name       string
		shutdownOn string
		days       int
		want       bool
	}{
		{name: "no announced date", days: -1, want: true},
		{name: "44 days", shutdownOn: "2026-09-30", days: 44, want: false},
		{name: "45 days", shutdownOn: "2026-10-01", days: 45, want: true},
		{name: "46 days", shutdownOn: "2026-10-02", days: 46, want: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := lifecycleAllowsInclusion(test.shutdownOn, test.days); got != test.want {
				t.Fatalf("lifecycleAllowsInclusion(%q, %d) = %t, want %t", test.shutdownOn, test.days, got, test.want)
			}
		})
	}
}

func TestCapabilityLookupUsesExactIDsAndConservativeUnknowns(t *testing.T) {
	known, ok := capabilitiesForModel("gemini-3.7-flash")
	if !ok || !known.ForbidTemperature || !known.ForbidCandidateCount || !known.ThinkingLevels.supports("low") || known.ThinkingLevels.supports("minimal") {
		t.Fatalf("gemini-3.7-flash capabilities = %#v", known)
	}
	for _, lookalike := range []string{"gemini-3.7-flash-extra", "prefix-gemini-3.7-flash", "GEMINI-3.7-FLASH"} {
		if _, ok := capabilitiesForModel(lookalike); ok {
			t.Fatalf("lookalike %q matched exact capability snapshot", lookalike)
		}
	}
	unknown := conservativeUnknownCapabilities("future-model")
	if unknown.ModelID != "future-model" || !unknown.forbidsSampling() ||
		!unknown.ForbidCandidateCount || unknown.ThinkingLevels != 0 ||
		unknown.OutputTokenLimit != 0 {
		t.Fatalf("unknown-model capabilities are not conservative: %#v", unknown)
	}
}

func TestGenerateContentThinkingLevelsMatchDatedModelSubsets(t *testing.T) {
	tests := []struct {
		model   string
		minimal bool
		low     bool
		medium  bool
		high    bool
	}{
		{model: "gemini-2.5-pro"},
		{model: "gemini-2.5-flash"},
		{model: "gemini-2.5-flash-lite"},
		{model: "gemini-3.1-pro-preview", low: true, medium: true, high: true},
		{model: "gemini-3.1-flash-lite", minimal: true, low: true, medium: true, high: true},
		{model: "gemini-3-flash-preview", minimal: true, low: true, medium: true, high: true},
		{model: "gemini-3.5-flash", minimal: true, low: true, medium: true, high: true},
		{model: "gemini-3.5-flash-lite", minimal: true, low: true, medium: true, high: true},
		{model: "gemini-3.6-flash", minimal: true, low: true, medium: true, high: true},
		{model: "gemini-3.7-flash", low: true, medium: true, high: true},
	}
	for _, test := range tests {
		t.Run(test.model, func(t *testing.T) {
			capabilities, ok := capabilitiesForModel(test.model)
			if !ok {
				t.Fatalf("missing capability row")
			}
			got := []bool{
				capabilities.ThinkingLevels.supports("minimal"),
				capabilities.ThinkingLevels.supports("low"),
				capabilities.ThinkingLevels.supports("medium"),
				capabilities.ThinkingLevels.supports("high"),
			}
			want := []bool{test.minimal, test.low, test.medium, test.high}
			for index := range want {
				if got[index] != want[index] {
					t.Fatalf("thinking support = %#v, want %#v", got, want)
				}
			}
		})
	}
}
