package openaiwire_test

import (
	"math"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/truvaagents/truva-g3/ai/providerkit/openaiwire"
	"github.com/truvaagents/truva-g3/core"
)

func TestLegacyBudgetOverflowSaturates(t *testing.T) {
	for _, stream := range []bool{false, true} {
		for _, multiplier := range []int{0, 1, 2, 3, 5} {
			effective := multiplier
			if effective == 0 {
				effective = openaiwire.DefaultReasoningTokenMultiplier
			}
			for _, budget := range []int{100, math.MaxInt / effective, math.MaxInt} {
				want := math.MaxInt
				if budget <= math.MaxInt/effective {
					want = budget * effective
				}
				body := openaiwire.BuildRequestBody("gpt-6.1-sol", nil, budget, 0, stream, multiplier, "low")
				require.Equal(t, want, body["max_completion_tokens"])
				for _, effort := range []string{"", "none"} {
					body = openaiwire.BuildRequestBody("gpt-6.1-sol", nil, budget, 0, stream, multiplier, effort)
					require.Equal(t, budget, body["max_completion_tokens"])
				}
				body = openaiwire.BuildRequestBody("ordinary-model", nil, budget, 0, stream, multiplier, "low")
				require.Equal(t, budget, body["max_tokens"])
			}
		}
	}
}

func TestReasoningBudgetFingerprintAppliesWithoutTokenCount(t *testing.T) {
	for _, effort := range []string{"low", "", "none"} {
		for _, omit := range []bool{false, true} {
			identities := make(map[string]bool)
			for _, multiplier := range []int{1, 3, 5} {
				codec, err := openaiwire.NewProfiledCodec(openaiwire.Config{SurfaceVersion: openaiwire.DefaultSurfaceVersion, ReasoningTokenMultiplier: multiplier, DefaultReasoningEffort: effort})
				require.NoError(t, err)
				request := core.NewAIRequest("synthetic", "cache-preflight")
				if omit {
					request.Generation.ReasoningEffort = core.OmitAIParameter[string]()
				}
				draft, err := codec.BuildDraftWithProfile(request, openaiwire.RequestProfile{
					SemanticModel: "gpt-6.1-sol", WireModel: "gpt-6.1-sol", ModelField: openaiwire.ModelFieldRequired,
					TokenLimit: openaiwire.TokenLimitMaxCompletionTokens, TokenBudget: openaiwire.TokenBudgetScaleForReasoning,
					ReasoningEffort: openaiwire.ReasoningEffortTopLevel, Sampling: openaiwire.SamplingReasoningRestricted,
				}, false)
				require.NoError(t, err)
				identities[draft.PolicyFingerprintIdentity()] = true
			}
			want := 1
			if effort == "low" && !omit {
				want = 3
			}
			require.Len(t, identities, want)
		}
	}
}

func TestModelDefaultBudgetIdentityAndOverflow(t *testing.T) {
	profile := openaiwire.RequestProfile{
		SemanticModel: "reviewed", WireModel: "reviewed", ModelField: openaiwire.ModelFieldRequired,
		TokenLimit: openaiwire.TokenLimitMaxCompletionTokens, TokenBudget: openaiwire.TokenBudgetScaleForReasoning,
		ReasoningEffort: openaiwire.ReasoningEffortTopLevel, Sampling: openaiwire.SamplingReasoningRestricted,
		ReasoningActiveByDefault: true,
	}
	identities := make(map[string]bool)
	for _, multiplier := range []int{1, 3, 5} {
		codec, err := openaiwire.NewProfiledCodec(openaiwire.Config{SurfaceVersion: openaiwire.DefaultSurfaceVersion, ReasoningTokenMultiplier: multiplier})
		require.NoError(t, err)
		request := core.NewAIRequest("unchanged", "preflight")
		draft, err := codec.BuildDraftWithProfile(request, profile, false)
		require.NoError(t, err)
		identity := draft.PolicyFingerprintIdentity()
		identities[identity] = true
		request.Generation.MaxTokens = core.SetAIParameter(512)
		draft, err = codec.BuildDraftWithProfile(request, profile, false)
		require.NoError(t, err)
		require.Equal(t, identity, draft.PolicyFingerprintIdentity())
		request.Generation.MaxTokens = core.SetAIParameter(math.MaxInt)
		_, err = codec.BuildDraftWithProfile(request, profile, false)
		if multiplier == 1 {
			require.NoError(t, err)
		} else {
			require.ErrorContains(t, err, "overflows")
		}
		request.Generation.ReasoningEffort = core.OmitAIParameter[string]()
		_, err = codec.BuildDraftWithProfile(request, profile, false)
		require.NoError(t, err)
	}
	require.Len(t, identities, 3)
}

func TestReviewedHeadroomLimit(t *testing.T) {
	profile := openaiwire.RequestProfile{
		SemanticModel: "reviewed", WireModel: "reviewed", ModelField: openaiwire.ModelFieldRequired,
		TokenLimit: openaiwire.TokenLimitMaxCompletionTokens, TokenBudget: openaiwire.TokenBudgetScaleForReasoning,
		ReasoningEffort: openaiwire.ReasoningEffortTopLevel, Sampling: openaiwire.SamplingReasoningRestricted,
		ReasoningActiveByDefault: true, ReasoningHeadroomLimit: 128000,
	}
	codec, err := openaiwire.NewProfiledCodec(openaiwire.Config{SurfaceVersion: openaiwire.DefaultSurfaceVersion, ReasoningTokenMultiplier: math.MaxInt})
	require.NoError(t, err)
	request := core.NewAIRequest("unchanged", "bounded-headroom")
	preflight, err := codec.BuildDraftWithProfile(request, profile, false)
	require.NoError(t, err)
	for _, base := range []int{1, 50000, 128000} {
		request.Generation.MaxTokens = core.SetAIParameter(base)
		draft, err := codec.BuildDraftWithProfile(request, profile, false)
		require.NoError(t, err, "a reviewed limit should bound even overflowing derived headroom")
		value, present := draft.Get("/max_completion_tokens")
		require.True(t, present)
		require.Equal(t, 128000, value)
		require.Equal(t, preflight.PolicyFingerprintIdentity(), draft.PolicyFingerprintIdentity())
		require.True(t, draft.HasExplicitIntent("/max_completion_tokens"), "bounding headroom must preserve base intent")
	}
	profile.ReasoningHeadroomLimit = 64000
	request.Generation.MaxTokens = core.AIParameter[int]{}
	changed, err := codec.BuildDraftWithProfile(request, profile, false)
	require.NoError(t, err)
	require.NotEqual(t, preflight.PolicyFingerprintIdentity(), changed.PolicyFingerprintIdentity())
	profile.ReasoningHeadroomLimit = 0
	unknown, err := codec.BuildDraftWithProfile(request, profile, false)
	require.NoError(t, err)
	require.NotEqual(t, preflight.PolicyFingerprintIdentity(), unknown.PolicyFingerprintIdentity())
	profile.ReasoningHeadroomLimit = -1
	_, err = codec.BuildDraftWithProfile(request, profile, false)
	require.ErrorContains(t, err, "reasoning-headroom limit must be nonnegative")

	// No adapter capacity guard here: a base already over the limit stays
	// visible to the provider, as do absent/none/omitted/exact-budget semantics.
	codec, err = openaiwire.NewProfiledCodec(openaiwire.Config{SurfaceVersion: openaiwire.DefaultSurfaceVersion, ReasoningTokenMultiplier: 3})
	require.NoError(t, err)
	profile.ReasoningHeadroomLimit = 128000
	request.Generation.MaxTokens = core.SetAIParameter(128001)
	draft, err := codec.BuildDraftWithProfile(request, profile, false)
	require.NoError(t, err)
	value, _ := draft.Get("/max_completion_tokens")
	require.Equal(t, 384003, value)
	request.Generation.MaxTokens = core.SetAIParameter(50000)
	for _, mode := range []string{"none", "omit", "exact"} {
		request.Generation.ReasoningEffort = core.SetAIParameter("none")
		switch mode {
		case "omit":
			request.Generation.ReasoningEffort = core.OmitAIParameter[string]()
		case "exact":
			request.Generation.ReasoningEffort = core.AIParameter[string]{}
			profile.TokenBudget = openaiwire.TokenBudgetExact
		}
		draft, err = codec.BuildDraftWithProfile(request, profile, false)
		require.NoError(t, err)
		value, _ = draft.Get("/max_completion_tokens")
		require.Equal(t, 50000, value)
		require.NotContains(t, draft.PolicyFingerprintIdentity(), "reasoning-headroom-ceiling")
	}
}
