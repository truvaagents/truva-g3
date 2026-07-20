package anthropic

import (
	"testing"

	"github.com/truvaagents/truva-g3/ai/requestpolicy"
)

func TestSamplingPolicyForModel(t *testing.T) {
	tests := []struct {
		name  string
		model string
		want  samplingPolicy
	}{
		{name: "opus 4.7", model: "claude-opus-4-7", want: samplingOmitted},
		{name: "opus 4.7 pinned", model: "claude-opus-4-7-20260701", want: samplingOmitted},
		{name: "opus 4.8 case insensitive", model: " CLAUDE-OPUS-4-8-20260701 ", want: samplingOmitted},
		{name: "sonnet 5", model: "claude-sonnet-5", want: samplingOmitted},
		{name: "fable 5", model: "claude-fable-5-20260701", want: samplingOmitted},
		{name: "mythos 5", model: "claude-mythos-5", want: samplingOmitted},
		{name: "mythos preview", model: "claude-mythos-preview-20260701", want: samplingOmitted},
		{name: "sonnet 4.6", model: "claude-sonnet-4-6", want: samplingAllowed},
		{name: "haiku 4.5", model: "claude-haiku-4-5-20251001", want: samplingAllowed},
		{name: "future sonnet does not prefix collide", model: "claude-sonnet-50", want: samplingUnknown},
		{name: "custom model", model: "enterprise-claude", want: samplingUnknown},
		{name: "empty model", model: "", want: samplingUnknown},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := samplingPolicyForModel(test.model); got != test.want {
				t.Fatalf("samplingPolicyForModel(%q) = %s, want %s", test.model, got, test.want)
			}
		})
	}
}

func TestRequestPolicyEngine_RemovesCaseFoldedSamplingKeys(t *testing.T) {
	document, err := requestpolicy.NewDocument(requestpolicy.DocumentConfig{
		Info: requestpolicy.RequestInfo{
			Provider:      "anthropic",
			Surface:       "messages",
			ResolvedModel: "claude-sonnet-5",
		},
		Body: map[string]interface{}{
			"model":       "claude-sonnet-5",
			"messages":    []Message{{Role: "user", Content: "hello"}},
			"max_tokens":  100,
			"temperature": 0.7,
			"Top_P":       0.8,
			"TOP_K":       10,
			"metadata":    true,
		},
		CaseInsensitivePaths: []string{"/temperature", "/top_p", "/top_k"},
	})
	if err != nil {
		t.Fatalf("NewDocument returned error: %v", err)
	}
	draft := &anthropicDraft{Document: document}
	report, err := newRequestPolicyEngine().Apply(t.Context(), draft, nil)
	if err != nil {
		t.Fatalf("Apply returned error: %v", err)
	}
	for _, path := range []string{"/temperature", "/top_p", "/top_k"} {
		if _, exists := draft.Get(path); exists {
			t.Fatalf("sampling path %s was not removed", path)
		}
	}
	if _, exists := draft.Get("/metadata"); !exists {
		t.Fatal("unrelated metadata was removed")
	}
	if len(report.Adjustments) != 3 {
		t.Fatalf("adjustment count = %d, want 3", len(report.Adjustments))
	}
}

func TestRequestPolicyEngine_MatchesSamplingClassification(t *testing.T) {
	engine := newRequestPolicyEngine()
	tests := []struct {
		name  string
		model string
	}{
		{name: "exact restricted family", model: "claude-sonnet-5"},
		{name: "pinned restricted family", model: "claude-opus-4-7-20260701"},
		{name: "case and surrounding whitespace", model: " CLAUDE-MYTHOS-5-20260701 "},
		{name: "prefix boundary collision", model: "claude-sonnet-50"},
		{name: "known sampling model", model: "claude-haiku-4-5-20251001"},
		{name: "unknown custom model", model: "enterprise-claude"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			document, err := requestpolicy.NewDocument(requestpolicy.DocumentConfig{
				Info: requestpolicy.RequestInfo{
					Provider:      "anthropic",
					Surface:       "messages",
					ResolvedModel: test.model,
				},
				Body: map[string]interface{}{
					"model":       test.model,
					"messages":    []Message{{Role: "user", Content: "hello"}},
					"max_tokens":  100,
					"temperature": 0.7,
				},
			})
			if err != nil {
				t.Fatalf("NewDocument returned error: %v", err)
			}
			draft := &anthropicDraft{Document: document}
			report, err := engine.Apply(t.Context(), draft, nil)
			if err != nil {
				t.Fatalf("Apply returned error: %v", err)
			}

			_, temperaturePresent := draft.Get("/temperature")
			classifiedOmitted := samplingPolicyForModel(test.model) == samplingOmitted
			policyOmitted := !temperaturePresent
			if policyOmitted != classifiedOmitted {
				t.Fatalf(
					"sampling classification and request policy diverged for %q: classification omitted=%t, policy omitted=%t, adjustments=%#v",
					test.model,
					classifiedOmitted,
					policyOmitted,
					report.Adjustments,
				)
			}
		})
	}
}
