package guidetests

import (
	"strings"
	"testing"

	"github.com/truvaagents/truva-g3/ai/providerkit/openaiwire"
	"github.com/truvaagents/truva-g3/core"
)

// TestCustomProviderGuideProfile compiles and validates the reusable-codec
// construction shown in CUSTOM_AI_PROVIDER_GUIDE.md. Keep this test aligned
// with that guide so its required profile fields cannot silently drift.
func TestCustomProviderGuideProfile(t *testing.T) {
	codec, err := openaiwire.NewProfiledCodec(openaiwire.Config{
		SurfaceVersion:   "acme-chat-completions-v1",
		MaxSSEEventBytes: 64,
	})
	if err != nil {
		t.Fatal(err)
	}
	profile := openaiwire.RequestProfile{
		SemanticModel:   "acme-reasoning-model",
		WireModel:       "acme-reasoning-model",
		ModelField:      openaiwire.ModelFieldRequired,
		TokenLimit:      openaiwire.TokenLimitMaxCompletionTokens,
		TokenBudget:     openaiwire.TokenBudgetExact,
		ReasoningEffort: openaiwire.ReasoningEffortTopLevel,
		Sampling:        openaiwire.SamplingOrdinary,
	}
	if err := profile.Validate(); err != nil {
		t.Fatalf("guide request profile is invalid: %v", err)
	}
	request := core.NewAIRequestFromLegacy("hello", "guide", &core.AIOptions{MaxTokens: 32})
	if _, err := codec.BuildDraftWithProfile(request, profile, false); err != nil {
		t.Fatalf("guide request profile cannot build a draft: %v", err)
	}

	oversizedEvent := `data: {"choices":[{"delta":{"content":"` +
		strings.Repeat("x", 128) + `"}}]}` + "\n\n" + "data: [DONE]\n\n"
	if _, err := codec.DecodeStream(strings.NewReader(oversizedEvent), func(core.StreamChunk) error { return nil }); err == nil ||
		!strings.Contains(err.Error(), "exceeds configured byte limit") {
		t.Fatalf("guide MaxSSEEventBytes setting was not enforced: %v", err)
	}
}
