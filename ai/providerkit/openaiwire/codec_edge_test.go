package openaiwire_test

import (
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/truvaagents/truva-g3/ai/providerkit/openaiwire"
	"github.com/truvaagents/truva-g3/core"
)

func TestBuildRequestBodyCompatibilityBridge(t *testing.T) {
	messages := []map[string]string{{"role": "user", "content": "hello"}}
	tests := []struct {
		name        string
		model       string
		maxTokens   int
		temperature float32
		stream      bool
		multiplier  int
		effort      string
		want        map[string]interface{}
		absent      []string
	}{
		{
			name:  "ordinary model",
			model: "gpt-4.1", maxTokens: 20, temperature: 0.25,
			want:   map[string]interface{}{"model": "gpt-4.1", "messages": messages, "max_tokens": 20, "temperature": float32(0.25)},
			absent: []string{"max_completion_tokens", "reasoning", "stream"},
		},
		{
			name:  "reasoning model uses default multiplier",
			model: "o3", maxTokens: 20, temperature: 0.25, multiplier: 0, effort: "high",
			want: map[string]interface{}{
				"model": "o3", "messages": messages, "max_completion_tokens": 100,
				"reasoning": map[string]interface{}{"effort": "high"},
			},
			absent: []string{"max_tokens", "temperature"},
		},
		{
			name:  "reasoning none retains sampling and streams",
			model: "gpt-5", maxTokens: 20, temperature: 0.25, stream: true, multiplier: 9, effort: "none",
			want: map[string]interface{}{
				"model": "gpt-5", "messages": messages, "max_completion_tokens": 20,
				"temperature": float32(0.25), "reasoning": map[string]interface{}{"effort": "none"},
				"stream": true, "stream_options": map[string]interface{}{"include_usage": true},
			},
			absent: []string{"max_tokens"},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := openaiwire.BuildRequestBody(
				test.model, messages, test.maxTokens, test.temperature,
				test.stream, test.multiplier, test.effort,
			)
			for key, want := range test.want {
				if !reflect.DeepEqual(got[key], want) {
					t.Fatalf("body[%q] = %#v, want %#v; body=%#v", key, got[key], want, got)
				}
			}
			for _, key := range test.absent {
				if _, present := got[key]; present {
					t.Fatalf("body unexpectedly contains %q: %#v", key, got)
				}
			}
		})
	}
}

func TestRequestProfileValidationMatrix(t *testing.T) {
	valid := openaiwire.RequestProfile{
		SemanticModel: "gpt-4.1", WireModel: "deployment",
		ModelField: openaiwire.ModelFieldRequired, TokenLimit: openaiwire.TokenLimitMaxTokens,
		TokenBudget:     openaiwire.TokenBudgetExact,
		ReasoningEffort: openaiwire.ReasoningEffortOmitted, Sampling: openaiwire.SamplingOrdinary,
	}
	if err := valid.Validate(); err != nil {
		t.Fatalf("valid profile rejected: %v", err)
	}

	tests := []struct {
		name   string
		mutate func(*openaiwire.RequestProfile)
	}{
		{name: "empty semantic model", mutate: func(profile *openaiwire.RequestProfile) { profile.SemanticModel = " " }},
		{name: "required wire model empty", mutate: func(profile *openaiwire.RequestProfile) { profile.WireModel = "" }},
		{name: "omitted wire model present", mutate: func(profile *openaiwire.RequestProfile) { profile.ModelField = openaiwire.ModelFieldOmitted }},
		{name: "invalid model field", mutate: func(profile *openaiwire.RequestProfile) { profile.ModelField = openaiwire.ModelFieldMode(255) }},
		{name: "invalid token field", mutate: func(profile *openaiwire.RequestProfile) { profile.TokenLimit = openaiwire.TokenLimitField(255) }},
		{name: "invalid token budget", mutate: func(profile *openaiwire.RequestProfile) { profile.TokenBudget = openaiwire.TokenBudgetPolicy(255) }},
		{name: "scaled budget with max tokens", mutate: func(profile *openaiwire.RequestProfile) {
			profile.TokenBudget = openaiwire.TokenBudgetScaleForReasoning
		}},
		{name: "reasoning style below range", mutate: func(profile *openaiwire.RequestProfile) { profile.ReasoningEffort = 0 }},
		{name: "reasoning style above range", mutate: func(profile *openaiwire.RequestProfile) {
			profile.ReasoningEffort = openaiwire.ReasoningEffortStyle(255)
		}},
		{name: "invalid sampling policy", mutate: func(profile *openaiwire.RequestProfile) { profile.Sampling = openaiwire.SamplingPolicy(255) }},
		{name: "restricted sampling with max tokens", mutate: func(profile *openaiwire.RequestProfile) { profile.Sampling = openaiwire.SamplingReasoningRestricted }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			profile := valid
			test.mutate(&profile)
			if err := profile.Validate(); err == nil {
				t.Fatalf("invalid profile accepted: %#v", profile)
			}
		})
	}
}

func TestCodecPublicIdentityAndExplicitIntent(t *testing.T) {
	codec, err := openaiwire.NewCodec("enterprise-chat-v7")
	if err != nil {
		t.Fatal(err)
	}
	if got := codec.SurfaceVersion(); got != "enterprise-chat-v7" {
		t.Fatalf("SurfaceVersion = %q", got)
	}
	request := core.NewAIRequestFromLegacy("hello", "identity", &core.AIOptions{Model: "legacy-model"})
	request.Generation.Model = "requested-model"
	request.Generation.Temperature = core.SetAIParameter(float32(0))
	draft, err := codec.BuildDraft(request, "gpt-4.1", false)
	if err != nil {
		t.Fatal(err)
	}
	if !draft.HasExplicitIntent("/temperature") || draft.HasExplicitIntent("/top_p") {
		t.Fatalf("explicit intent mismatch: temperature=%t top_p=%t", draft.HasExplicitIntent("/temperature"), draft.HasExplicitIntent("/top_p"))
	}
	if draft.Info().RequestedModel != "requested-model" {
		t.Fatalf("requested model = %q", draft.Info().RequestedModel)
	}
	if err := draft.BindIdentity(" ", "enterprise"); err == nil {
		t.Fatal("BindIdentity accepted an empty provider")
	}
	var nilDraft *openaiwire.Draft
	if err := nilDraft.BindIdentity("openai", "openai"); err == nil {
		t.Fatal("nil draft BindIdentity returned nil error")
	}
	if nilDraft.Adjustments() != nil || nilDraft.ProtectedHeaderConflicts() != nil {
		t.Fatal("nil draft accessors returned non-nil values")
	}
}

func TestCodecRejectsEveryInvalidPortableMode(t *testing.T) {
	invalidMode := core.AIParameterMode(255)
	tests := []struct {
		name   string
		mutate func(*core.AIRequest)
	}{
		{name: "temperature", mutate: func(request *core.AIRequest) { request.Generation.Temperature.Mode = invalidMode }},
		{name: "top p", mutate: func(request *core.AIRequest) { request.Generation.TopP.Mode = invalidMode }},
		{name: "top k", mutate: func(request *core.AIRequest) { request.Generation.TopK.Mode = invalidMode }},
		{name: "max tokens", mutate: func(request *core.AIRequest) { request.Generation.MaxTokens.Mode = invalidMode }},
		{name: "system prompt", mutate: func(request *core.AIRequest) { request.Generation.SystemPrompt.Mode = invalidMode }},
		{name: "reasoning effort", mutate: func(request *core.AIRequest) { request.Generation.ReasoningEffort.Mode = invalidMode }},
		{name: "response format", mutate: func(request *core.AIRequest) { request.Generation.ResponseFormat.Mode = invalidMode }},
	}
	codec := mustCodec(t)
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := core.NewAIRequest("hello", "invalid-mode")
			test.mutate(request)
			if _, err := codec.BuildDraft(request, "gpt-4.1", false); err == nil {
				t.Fatal("invalid portable mode accepted")
			}
		})
	}
}

func TestCodecRejectsNativeSpellingsInPortableResponseFormat(t *testing.T) {
	codec := mustCodec(t)
	for _, value := range []string{"json_object", "json_schema", "text", "yaml", "response-format-canary"} {
		t.Run(value, func(t *testing.T) {
			request := core.NewAIRequestFromLegacy("hello", "invalid-response-format", &core.AIOptions{
				ResponseFormat: value,
			})
			if _, err := codec.BuildDraft(request, "gpt-4.1", false); err == nil ||
				!strings.Contains(err.Error(), "unsupported portable response format") {
				t.Fatalf("BuildDraft() error = %v", err)
			} else if strings.Contains(err.Error(), value) {
				t.Fatalf("BuildDraft() exposed caller-controlled response format: %v", err)
			}
		})
	}
}

func TestCodecTopPSetClonesNilLegacyExtra(t *testing.T) {
	codec := mustCodec(t)
	request := core.NewAIRequestFromLegacy("hello", "top-p", &core.AIOptions{Model: "gpt-4.1"})
	request.Generation.TopP = core.SetAIParameter(float32(0.75))
	draft, err := codec.BuildDraft(request, "gpt-4.1", false)
	if err != nil {
		t.Fatal(err)
	}
	if got, present := draft.Get("/top_p"); !present || got != float32(0.75) {
		t.Fatalf("top_p = %#v, %t", got, present)
	}
	if request.LegacyOptions().Extra != nil {
		t.Fatalf("caller request extras mutated: %#v", request.LegacyOptions().Extra)
	}
}

func TestCodecDraftReasoningAndIntegerValidationMatrix(t *testing.T) {
	codec := mustProfiledCodec(t)
	base := openaiwire.RequestProfile{
		SemanticModel: "gpt-4.1", WireModel: "wire-model",
		ModelField: openaiwire.ModelFieldRequired, TokenLimit: openaiwire.TokenLimitMaxTokens,
		TokenBudget:     openaiwire.TokenBudgetExact,
		ReasoningEffort: openaiwire.ReasoningEffortOmitted, Sampling: openaiwire.SamplingOrdinary,
	}

	reasoningTests := []struct {
		name    string
		profile openaiwire.RequestProfile
		path    string
		value   interface{}
	}{
		{name: "omitted profile rejects nested", profile: base, path: "/reasoning", value: map[string]interface{}{"effort": "low"}},
		{name: "omitted profile rejects top level", profile: base, path: "/reasoning_effort", value: "low"},
		{name: "top level profile rejects nested", profile: func() openaiwire.RequestProfile {
			profile := base
			profile.ReasoningEffort = openaiwire.ReasoningEffortTopLevel
			return profile
		}(), path: "/reasoning", value: map[string]interface{}{"effort": "low"}},
		{name: "nested profile rejects top level", profile: func() openaiwire.RequestProfile {
			profile := base
			profile.ReasoningEffort = openaiwire.ReasoningEffortNestedObject
			return profile
		}(), path: "/reasoning_effort", value: "low"},
	}
	for _, test := range reasoningTests {
		t.Run(test.name, func(t *testing.T) {
			draft, err := codec.BuildDraftWithProfile(core.NewAIRequest("hello", "reasoning"), test.profile, false)
			if err != nil {
				t.Fatal(err)
			}
			if err := draft.Set(test.path, test.value); err != nil {
				t.Fatalf("draft.Set: %v", err)
			}
			if err := draft.Validate(); err == nil {
				t.Fatal("incompatible reasoning shape accepted")
			}
		})
	}

	validIntegers := []interface{}{int(1), int8(1), int16(1), int32(1), int64(1), uint(1), uint8(1), uint16(1), uint32(1), uint64(1), float64(1)}
	for _, value := range validIntegers {
		t.Run("valid "+reflect.TypeOf(value).String(), func(t *testing.T) {
			draft, err := codec.BuildDraftWithProfile(core.NewAIRequest("hello", "integer"), base, false)
			if err != nil {
				t.Fatal(err)
			}
			if err := draft.Set("/max_tokens", value); err != nil {
				t.Fatal(err)
			}
			if err := draft.Validate(); err != nil {
				t.Fatalf("valid integer %T(%v) rejected: %v", value, value, err)
			}
		})
	}
	invalidIntegers := []interface{}{0, -1, float64(1.5), "1", nil}
	for index, value := range invalidIntegers {
		t.Run("invalid "+string(rune('a'+index)), func(t *testing.T) {
			draft, err := codec.BuildDraftWithProfile(core.NewAIRequest("hello", "integer"), base, false)
			if err != nil {
				t.Fatal(err)
			}
			if err := draft.Set("/max_tokens", value); err != nil {
				t.Fatal(err)
			}
			if err := draft.Validate(); err == nil || !strings.Contains(err.Error(), "positive integer") {
				t.Fatalf("invalid integer %T(%v) validation error = %v", value, value, err)
			}
		})
	}
}

func TestCodecEncodeMarshalFailureAndInitialStreamReadFailure(t *testing.T) {
	codec := mustCodec(t)
	draft, err := codec.BuildDraft(core.NewAIRequest("hello", "marshal"), "gpt-4.1", false)
	if err != nil {
		t.Fatal(err)
	}
	if err := draft.Set("/unsupported_value", func() {}); err != nil {
		t.Fatal(err)
	}
	if _, err := codec.Encode(draft); err == nil {
		t.Fatal("Encode accepted an unmarshalable value")
	}

	readErr := errors.New("initial stream read failed")
	result, err := codec.DecodeStream(&failAfterDataReader{data: strings.NewReader(""), err: readErr}, func(core.StreamChunk) error { return nil })
	if result != nil || !errors.Is(err, readErr) || errors.Is(err, core.ErrStreamPartiallyCompleted) {
		t.Fatalf("DecodeStream result=%#v error=%v", result, err)
	}
}
