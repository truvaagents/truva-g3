package anthropic

import (
	"reflect"
	"testing"
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

func TestDeleteKeyFold(t *testing.T) {
	body := map[string]interface{}{
		"temperature": 0.7,
		"Top_P":       0.8,
		"TOP_K":       10,
		"metadata":    true,
	}

	removed := deleteKeyFold(body, "temperature", "top_p", "top_k", "missing")

	if len(body) != 1 || body["metadata"] != true {
		t.Fatalf("deleteKeyFold left unexpected body: %#v", body)
	}
	wantRemoved := []string{"/temperature", "/top_p", "/top_k"}
	if !reflect.DeepEqual(removed, wantRemoved) {
		t.Fatalf("removed paths = %#v, want %#v", removed, wantRemoved)
	}
}
