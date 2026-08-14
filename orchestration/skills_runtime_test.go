package orchestration

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
)

type runtimeTestSkillRegistry struct {
	candidates []SkillCandidate
	err        error
	calls      int
	requests   []SkillCandidateRequest
}

func (registry *runtimeTestSkillRegistry) ListMetadata(context.Context, SkillMetadataFilter) ([]SkillMetadata, error) {
	return nil, nil
}

func (registry *runtimeTestSkillRegistry) ResolveCandidates(
	_ context.Context,
	requests []SkillCandidateRequest,
) ([]SkillCandidate, error) {
	registry.calls++
	registry.requests = append([]SkillCandidateRequest(nil), requests...)
	return cloneSkillCandidates(registry.candidates), registry.err
}

func (*runtimeTestSkillRegistry) GetManifest(context.Context, SkillVersionRef) (SkillManifest, error) {
	return SkillManifest{}, ErrSkillNotFound
}

func (*runtimeTestSkillRegistry) GetResource(context.Context, SkillResourceRef) (SkillResource, error) {
	return SkillResource{}, ErrSkillNotFound
}

func TestSkillRuntimePinsOneCanonicalBodyFreeCandidateBatch(t *testing.T) {
	bindings := []SkillBinding{
		{Namespace: "travel", Name: "weather", Version: "published", Activation: SkillActivationAuto},
		{Namespace: "operations", Name: "incident", Version: "2", Activation: SkillActivationExplicit},
	}
	incident := runtimeTestResolvedCandidate(bindings[1], 2, "b", "operations")
	weather := runtimeTestResolvedCandidate(bindings[0], 7, "a", "travel")
	registry := &runtimeTestSkillRegistry{candidates: []SkillCandidate{weather, incident}}
	runtime := newRuntimeForTest(t, bindings, registry, nil)

	ctx, err := WithTrustedSkillActivations(t.Context(), incident.Ref)
	if err != nil {
		t.Fatal(err)
	}
	ctx, err = WithTrustedSkillResourceRequests(ctx, SkillResourceRequest{Skill: incident.Ref, Name: "runbook"})
	if err != nil {
		t.Fatal(err)
	}
	state, cacheContext, err := runtime.PinCandidates(ctx)
	if err != nil {
		t.Fatalf("PinCandidates() error = %v", err)
	}
	if registry.calls != 1 {
		t.Fatalf("ResolveCandidates() calls = %d, want 1", registry.calls)
	}
	wantRequests := []SkillCandidateRequest{
		{Ref: incident.Ref, RequestedVersion: "2"},
		{Ref: weather.Ref, RequestedVersion: "published"},
	}
	if !reflect.DeepEqual(registry.requests, wantRequests) {
		t.Fatalf("ResolveCandidates() requests = %#v, want %#v", registry.requests, wantRequests)
	}
	if state.Pinned == nil || len(state.Pinned.Candidates) != 2 ||
		state.Pinned.Candidates[0].Ref != incident.Ref || state.Pinned.Candidates[1].Ref != weather.Ref {
		t.Fatalf("pinned candidates = %#v", state.Pinned)
	}
	if cacheContext.Fingerprint == "" || state.Pinned.CacheFingerprint != cacheContext.Fingerprint {
		t.Fatalf("cache context = %#v, snapshot = %#v", cacheContext, state.Pinned)
	}
	encoded := mustJSONForTest(t, state)
	for _, forbidden := range []string{"planning_instructions", "response_instructions", "resource content"} {
		if strings.Contains(encoded, forbidden) {
			t.Fatalf("body-free state contains %q: %s", forbidden, encoded)
		}
	}

	// The context carrier and returned state are defensive copies.
	state.Pinned.Candidates[0].Metadata.Description = "caller mutation"
	carried := withSkillExecutionState(t.Context(), state)
	loaded, ok := skillExecutionStateFromContext(carried)
	if !ok {
		t.Fatal("skill execution state missing from context")
	}
	loaded.Pinned.Candidates[0].Metadata.Description = "second mutation"
	again, _ := skillExecutionStateFromContext(carried)
	if again.Pinned.Candidates[0].Metadata.Description != "caller mutation" {
		t.Fatalf("context state was aliased: %#v", again.Pinned.Candidates[0].Metadata)
	}
}

func TestSkillRuntimeDomainCompatibilityModes(t *testing.T) {
	binding := SkillBinding{Namespace: "travel", Name: "weather", Version: "published", Activation: SkillActivationAlways}
	candidate := runtimeTestResolvedCandidate(binding, 1, "c", "finance")

	t.Run("warn retains optional candidate", func(t *testing.T) {
		registry := &runtimeTestSkillRegistry{candidates: []SkillCandidate{candidate}}
		runtime := newRuntimeForTest(t, []SkillBinding{binding}, registry, func(config *OrchestratorConfig) {
			config.PromptConfig.Domain = "travel"
			config.Skills.DomainCompatibilityMode = SkillDomainCompatibilityWarn
		})
		state, _, err := runtime.PinCandidates(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		if state.Pinned.Candidates[0].Status != SkillCandidateResolved ||
			state.Pinned.DomainOutcomes[0].Outcome != "mismatch_warned" {
			t.Fatalf("warn state = %#v", state.Pinned)
		}
	})

	t.Run("enforce omits optional candidate", func(t *testing.T) {
		registry := &runtimeTestSkillRegistry{candidates: []SkillCandidate{candidate}}
		runtime := newRuntimeForTest(t, []SkillBinding{binding}, registry, func(config *OrchestratorConfig) {
			config.PromptConfig.Domain = "travel"
			config.Skills.DomainCompatibilityMode = SkillDomainCompatibilityEnforce
		})
		state, _, err := runtime.PinCandidates(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		if state.Pinned.Candidates[0].Status != SkillCandidateUnavailable ||
			state.Pinned.Candidates[0].Resolved.Version != 0 ||
			state.Pinned.DomainOutcomes[0].Outcome != "mismatch_omitted" {
			t.Fatalf("enforced state = %#v", state.Pinned)
		}
	})

	t.Run("enforce fails required candidate", func(t *testing.T) {
		required := binding
		required.Required = true
		candidate := runtimeTestResolvedCandidate(required, 1, "d", "finance")
		registry := &runtimeTestSkillRegistry{candidates: []SkillCandidate{candidate}}
		runtime := newRuntimeForTest(t, []SkillBinding{required}, registry, func(config *OrchestratorConfig) {
			config.PromptConfig.Domain = "travel"
			config.Skills.DomainCompatibilityMode = SkillDomainCompatibilityEnforce
		})
		if _, _, err := runtime.PinCandidates(t.Context()); !errors.Is(err, ErrSkillUnavailable) {
			t.Fatalf("PinCandidates() error = %v, want ErrSkillUnavailable", err)
		}
	})
}

func TestSkillRuntimeCandidateFailuresRespectRequiredPolicy(t *testing.T) {
	optional := SkillBinding{Namespace: "travel", Name: "weather", Version: "published", Activation: SkillActivationAuto}

	t.Run("optional registry failure is body-free unavailable", func(t *testing.T) {
		registry := &runtimeTestSkillRegistry{err: errors.New("provider detail must not escape")}
		runtime := newRuntimeForTest(t, []SkillBinding{optional}, registry, nil)
		state, _, err := runtime.PinCandidates(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		if state.Pinned.Candidates[0].Status != SkillCandidateUnavailable ||
			state.Diagnostics[0].Code != "skill_registry_unavailable" {
			t.Fatalf("optional failure state = %#v", state)
		}
	})

	t.Run("required registry failure fails before hooks", func(t *testing.T) {
		required := optional
		required.Required = true
		registry := &runtimeTestSkillRegistry{err: errors.New("provider detail must not escape")}
		runtime := newRuntimeForTest(t, []SkillBinding{required}, registry, nil)
		_, _, err := runtime.PinCandidates(t.Context())
		if !errors.Is(err, ErrSkillUnavailable) || strings.Contains(err.Error(), "provider detail") {
			t.Fatalf("required failure error = %v", err)
		}
	})

	t.Run("required missing candidate fails with skill identity", func(t *testing.T) {
		required := optional
		required.Required = true
		registry := &runtimeTestSkillRegistry{candidates: []SkillCandidate{{
			Ref: required.Ref(), RequestedVersion: required.Version, Status: SkillCandidateNotFound,
		}}}
		runtime := newRuntimeForTest(t, []SkillBinding{required}, registry, nil)
		state, _, err := runtime.PinCandidates(t.Context())
		if !errors.Is(err, ErrSkillUnavailable) ||
			!hasRuntimeSkillDiagnostic(state.Diagnostics, "skill_required_candidate_unavailable") {
			t.Fatalf("required missing candidate state=%#v error=%v", state, err)
		}
	})

	t.Run("optional malformed provider result is omitted", func(t *testing.T) {
		candidate := runtimeTestResolvedCandidate(optional, 1, "e", "travel")
		candidate.Resolved.ManifestHash = "invalid"
		registry := &runtimeTestSkillRegistry{candidates: []SkillCandidate{candidate}}
		runtime := newRuntimeForTest(t, []SkillBinding{optional}, registry, nil)
		state, _, err := runtime.PinCandidates(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		if state.Pinned.Candidates[0].Status != SkillCandidateUnavailable ||
			state.Diagnostics[0].Code != "skill_candidate_result_invalid" {
			t.Fatalf("malformed optional state = %#v", state)
		}
	})
}

func TestSkillCandidateFingerprintCoversAnswerAffectingInputs(t *testing.T) {
	binding := SkillBinding{Namespace: "travel", Name: "weather", Version: "published", Activation: SkillActivationAuto}
	baseCandidate := runtimeTestResolvedCandidate(binding, 1, "f", "travel")
	baseRegistry := &runtimeTestSkillRegistry{candidates: []SkillCandidate{baseCandidate}}
	baseRuntime := newRuntimeForTest(t, []SkillBinding{binding}, baseRegistry, func(config *OrchestratorConfig) {
		config.PromptConfig.Domain = "travel"
		config.SkillActivationAIOptions = &AIOptionsOverride{Model: StringPtr("selector-a")}
		config.SkillResourceAIOptions = &AIOptionsOverride{Model: StringPtr("resolver-a")}
	})
	_, base, err := baseRuntime.PinCandidates(t.Context())
	if err != nil {
		t.Fatal(err)
	}

	tests := map[string]func(*OrchestratorConfig, *SkillCandidate, *context.Context){
		"published candidate": func(_ *OrchestratorConfig, candidate *SkillCandidate, _ *context.Context) {
			candidate.Resolved.Version++
			candidate.Metadata.PublishedVersion++
			candidate.Resolved.ManifestHash = "sha256:" + strings.Repeat("1", 64)
		},
		"runtime budget": func(config *OrchestratorConfig, _ *SkillCandidate, _ *context.Context) {
			config.Skills.Limits.TotalTokenBudget--
		},
		"selector model": func(config *OrchestratorConfig, _ *SkillCandidate, _ *context.Context) {
			config.SkillActivationAIOptions.Model = StringPtr("selector-b")
		},
		"runtime policy": func(config *OrchestratorConfig, _ *SkillCandidate, _ *context.Context) {
			config.Skills.RuntimePolicyID = "custom-policy/v2"
		},
		"trusted activation": func(_ *OrchestratorConfig, _ *SkillCandidate, ctx *context.Context) {
			updated, contextErr := WithTrustedSkillActivations(*ctx, binding.Ref())
			if contextErr != nil {
				t.Fatal(contextErr)
			}
			*ctx = updated
		},
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			candidate := baseCandidate
			configMutator := func(config *OrchestratorConfig) {
				config.PromptConfig.Domain = "travel"
				config.SkillActivationAIOptions = &AIOptionsOverride{Model: StringPtr("selector-a")}
				config.SkillResourceAIOptions = &AIOptionsOverride{Model: StringPtr("resolver-a")}
			}
			config := NewDefaultOrchestratorConfig()
			config.Skills.Enabled = true
			config.Skills.Bindings = []SkillBinding{binding}
			configMutator(config)
			ctx := context.Context(t.Context())
			mutate(config, &candidate, &ctx)
			registry := &runtimeTestSkillRegistry{candidates: []SkillCandidate{candidate}}
			config.SkillRegistry = registry
			runtime, runtimeErr := newSkillRuntime(config, registry, nil)
			if runtimeErr != nil {
				t.Fatal(runtimeErr)
			}
			_, got, pinErr := runtime.PinCandidates(ctx)
			if pinErr != nil {
				t.Fatal(pinErr)
			}
			if got.Fingerprint == base.Fingerprint {
				t.Fatalf("fingerprint did not change for %s", name)
			}
		})
	}
}

func TestSkillResponseCacheEligibilityRequiresAttestableSelectorIntent(t *testing.T) {
	binding := SkillBinding{Namespace: "travel", Name: "weather", Version: "published", Activation: SkillActivationAuto}
	registry := &runtimeTestSkillRegistry{candidates: []SkillCandidate{runtimeTestResolvedCandidate(binding, 1, "9", "travel")}}
	tests := []struct {
		name   string
		mutate func(*OrchestratorConfig)
		want   bool
	}{
		{name: "inherited models bypass", want: false},
		{name: "explicit selector models", mutate: func(config *OrchestratorConfig) {
			config.SkillActivationAIOptions = &AIOptionsOverride{Model: StringPtr("activation")}
			config.SkillResourceAIOptions = &AIOptionsOverride{Model: StringPtr("resource")}
		}, want: true},
		{name: "runtime policy attests inherited model", mutate: func(config *OrchestratorConfig) {
			config.Skills.RuntimePolicyID = "deployment/model-policy-v1"
		}, want: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			runtime := newRuntimeForTest(t, []SkillBinding{binding}, registry, test.mutate)
			_, cacheContext, err := runtime.PinCandidates(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			if cacheContext.ResponseCacheEligible != test.want {
				t.Fatalf("ResponseCacheEligible = %v, want %v", cacheContext.ResponseCacheEligible, test.want)
			}
		})
	}
}

func TestTrustedSkillContextCarriersValidateSortAndCopy(t *testing.T) {
	refs := []SkillRef{{Namespace: "z", Name: "last"}, {Namespace: "a", Name: "first"}}
	ctx, err := WithTrustedSkillActivations(t.Context(), refs...)
	if err != nil {
		t.Fatal(err)
	}
	refs[0].Name = "mutated"
	got := trustedSkillActivationsFromContext(ctx)
	if got[0] != (SkillRef{Namespace: "a", Name: "first"}) || got[1].Name != "last" {
		t.Fatalf("trusted activations = %#v", got)
	}
	if _, err := WithTrustedSkillActivations(t.Context(), got[0], got[0]); !errors.Is(err, ErrInvalidSkillPackage) {
		t.Fatalf("duplicate trusted activation error = %v", err)
	}
	if _, err := WithTrustedSkillActivations(t.Context(), make([]SkillRef, maxTrustedSkillActivations+1)...); !errors.Is(err, ErrSkillLimitExceeded) {
		t.Fatalf("oversized trusted activation error = %v", err)
	}
	if _, err := WithTrustedSkillResourceRequests(t.Context(),
		SkillResourceRequest{Skill: got[0], Name: "same"},
		SkillResourceRequest{Skill: got[0], Name: "same"},
	); !errors.Is(err, ErrInvalidSkillPackage) {
		t.Fatalf("duplicate trusted resource error = %v", err)
	}
	capabilities := []string{"weather", "route-planning", "weather"}
	capabilityCtx, err := WithSkillExpectedCapabilities(t.Context(), capabilities...)
	if err != nil {
		t.Fatal(err)
	}
	capabilities[0] = "mutated"
	gotCapabilities := skillExpectedCapabilitiesFromContext(capabilityCtx)
	if len(gotCapabilities) != 2 || gotCapabilities[0] != "route-planning" || gotCapabilities[1] != "weather" {
		t.Fatalf("expected capabilities = %#v", gotCapabilities)
	}
	if _, err := WithSkillExpectedCapabilities(t.Context(), make([]string, maxSkillExpectedCapabilities+1)...); !errors.Is(err, ErrSkillLimitExceeded) {
		t.Fatalf("oversized expected capabilities error = %v", err)
	}
}

func TestSkillExpectedCapabilitiesParticipateInCacheFingerprint(t *testing.T) {
	binding := SkillBinding{Namespace: "travel", Name: "weather", Version: "published", Activation: SkillActivationAlways}
	registry := &runtimeTestSkillRegistry{candidates: []SkillCandidate{runtimeTestResolvedCandidate(binding, 1, "9", "travel")}}
	runtime := newRuntimeForTest(t, []SkillBinding{binding}, registry, nil)
	_, plain, err := runtime.PinCandidates(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	ctx, err := WithSkillExpectedCapabilities(t.Context(), "forecasting")
	if err != nil {
		t.Fatal(err)
	}
	state, hinted, err := runtime.PinCandidates(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if hinted.Fingerprint == plain.Fingerprint || len(state.Pinned.ExpectedCapabilities) != 1 {
		t.Fatalf("fingerprints plain=%q hinted=%q snapshot=%#v", plain.Fingerprint, hinted.Fingerprint, state.Pinned)
	}
}

func TestSkillCanonicalFingerprintGoldenVectors(t *testing.T) {
	config := NewDefaultOrchestratorConfig().Skills
	bindings := []SkillBinding{
		{Namespace: "travel", Name: "weather", Version: "published", Activation: SkillActivationAuto},
		{Namespace: "operations", Name: "incident", Version: "2", Activation: SkillActivationAlways, Required: true},
	}
	bindingFingerprint, err := ComputeSkillBindingFingerprint(bindings)
	if err != nil {
		t.Fatal(err)
	}
	budgetFingerprint, err := ComputeSkillRuntimeBudgetFingerprint(config)
	if err != nil {
		t.Fatal(err)
	}
	const wantBindings = "sha256:4309cbf9361108430f09d5577479cf72c219f1aaac2a11e444f12150f60acf1d"
	const wantBudgets = "sha256:ba4d19f53016f85fe53dc3e33cc37884e05c94cef18c01e4df1ec01812fc84d4"
	if bindingFingerprint != wantBindings || budgetFingerprint != wantBudgets {
		t.Fatalf("golden fingerprints = bindings %q budgets %q", bindingFingerprint, budgetFingerprint)
	}
}

func TestSkillInputEncoderPolicyVersionContract(t *testing.T) {
	if skillInputEncoderPolicyVersion != "skill_input_json_v1" {
		t.Fatalf("skill input encoder policy version = %q", skillInputEncoderPolicyVersion)
	}
}

func newRuntimeForTest(
	t *testing.T,
	bindings []SkillBinding,
	registry SkillRegistry,
	mutate func(*OrchestratorConfig),
) *skillRuntime {
	t.Helper()
	config := NewDefaultOrchestratorConfig()
	config.Skills.Enabled = true
	config.Skills.Bindings = append([]SkillBinding(nil), bindings...)
	config.SkillRegistry = registry
	if mutate != nil {
		mutate(config)
	}
	runtime, err := newSkillRuntime(config, registry, nil)
	if err != nil {
		t.Fatalf("newSkillRuntime() error = %v", err)
	}
	return runtime
}

func runtimeTestResolvedCandidate(binding SkillBinding, version uint64, hashDigit, domain string) SkillCandidate {
	ref := binding.Ref()
	return SkillCandidate{
		Ref:              ref,
		RequestedVersion: binding.Version,
		Resolved: SkillVersionRef{
			Ref: ref, Version: version, ManifestHash: "sha256:" + strings.Repeat(hashDigit, 64),
		},
		Metadata: SkillMetadata{
			Ref: ref, DisplayName: binding.Name, Description: "Body-free trigger description.",
			Domains: []string{domain}, PublishedVersion: version, Status: SkillPublicationPublished,
		},
		Status: SkillCandidateResolved,
	}
}

func TestSkillCandidateDebugRecordsCaptureBoundedPinnedMetadata(t *testing.T) {
	binding := SkillBinding{
		Namespace: "travel", Name: "weather-assessment", Version: "published",
		Activation: SkillActivationAuto,
	}
	candidate := runtimeTestResolvedCandidate(binding, 3, "a", "travel")
	candidate.Metadata.DisplayName = "Weather Assessment"
	candidate.Metadata.Description = "Evaluates forecast conditions and travel disruption."

	records := skillCandidateDebugRecords([]SkillBinding{binding}, []SkillCandidate{candidate})
	if len(records) != 1 {
		t.Fatalf("records = %d, want 1", len(records))
	}
	if records[0].DisplayName != candidate.Metadata.DisplayName ||
		records[0].Description != candidate.Metadata.Description {
		t.Fatalf("debug metadata = %q, %q, want %q, %q",
			records[0].DisplayName, records[0].Description,
			candidate.Metadata.DisplayName, candidate.Metadata.Description)
	}
}

func mustJSONForTest(t *testing.T, value interface{}) string {
	t.Helper()
	encoded, err := jsonMarshalForTest(value)
	if err != nil {
		t.Fatal(err)
	}
	return string(encoded)
}

// Keep the test helper behind a variable so static analysis still sees direct
// encoding errors being handled without duplicating a large type assertion.
var jsonMarshalForTest = func(value interface{}) ([]byte, error) {
	return json.Marshal(value)
}
