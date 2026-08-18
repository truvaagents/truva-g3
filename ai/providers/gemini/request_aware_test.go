package gemini

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/truvaagents/truva-g3/ai/requestpolicy"
	"github.com/truvaagents/truva-g3/core"
)

func decodeGeminiPreparedBody(t *testing.T, body []byte) map[string]interface{} {
	t.Helper()
	var decoded map[string]interface{}
	if err := json.Unmarshal(body, &decoded); err != nil {
		t.Fatalf("decode prepared body: %v", err)
	}
	return decoded
}

func preparedGenerationConfig(t *testing.T, body map[string]interface{}) map[string]interface{} {
	t.Helper()
	config, ok := body["generationConfig"].(map[string]interface{})
	if !ok {
		t.Fatalf("generationConfig = %#v", body["generationConfig"])
	}
	return config
}

func TestPrepareInvocationPresenceAwareIntentAndStreamParity(t *testing.T) {
	client := NewClient("gemini-key", "", &core.NoOpLogger{})
	request := core.NewAIRequest("hello", "planning")
	request.Generation.Model = "gemini-3.5-flash"
	request.Generation.Temperature = core.SetAIParameter(float32(0))
	request.Generation.TopP = core.SetAIParameter(float32(0.4))
	request.Generation.TopK = core.SetAIParameter(32)
	request.Generation.MaxTokens = core.SetAIParameter(2048)
	request.Generation.SystemPrompt = core.SetAIParameter("")
	request.Generation.ReasoningEffort = core.SetAIParameter("high")
	request.Generation.ResponseFormat = core.SetAIParameter("json")

	buffered, err := client.prepareInvocation(t.Context(), request, false)
	if err != nil {
		t.Fatalf("prepare buffered invocation: %v", err)
	}
	streamed, err := client.prepareInvocation(t.Context(), request, true)
	if err != nil {
		t.Fatalf("prepare streamed invocation: %v", err)
	}
	bufferedBody := decodeGeminiPreparedBody(t, buffered.Request.Body)
	streamedBody := decodeGeminiPreparedBody(t, streamed.Request.Body)
	if !reflect.DeepEqual(bufferedBody, streamedBody) {
		t.Fatalf("buffered/stream body drift:\nbuffered=%#v\nstream=%#v", bufferedBody, streamedBody)
	}
	if bufferedBody["store"] != false {
		t.Fatalf("store invariant = %#v", bufferedBody["store"])
	}
	config := preparedGenerationConfig(t, bufferedBody)
	if config["temperature"] != float64(0) || config["topP"] != 0.4 ||
		config["topK"] != float64(32) || config["maxOutputTokens"] != float64(2048) ||
		config["responseMimeType"] != "application/json" {
		t.Fatalf("generation config = %#v", config)
	}
	thinking := config["thinkingConfig"].(map[string]interface{})
	if thinking["thinkingLevel"] != "high" {
		t.Fatalf("thinking config = %#v", thinking)
	}
	if bufferedBody["systemInstruction"].(map[string]interface{})["parts"] == nil {
		t.Fatalf("system instruction = %#v", bufferedBody["systemInstruction"])
	}

	report := buffered.Request.Report
	if report == nil || !report.Stable || len(report.Fingerprint) != 64 ||
		report.Provider != "gemini" || report.ProviderAlias != "gemini" ||
		report.Surface != "generate-content" || report.Operation != "generate" ||
		report.Purpose != "planning" || report.RequestedModel != "gemini-3.5-flash" ||
		report.ResolvedModel != "gemini-3.5-flash" {
		t.Fatalf("buffered report = %#v", report)
	}
	if report.EffectiveTemperature.Mode != core.AIParameterSet || report.EffectiveTemperature.Value != 0 ||
		report.EffectiveMaxTokens.Mode != core.AIParameterSet || report.EffectiveMaxTokens.Value != 2048 {
		t.Fatalf("effective generation report = %#v", report)
	}
	if streamed.Request.Report.Operation != "stream" || streamed.Request.Headers.Get("Accept") != "text/event-stream" {
		t.Fatalf("stream preparation = report %#v headers %#v", streamed.Request.Report, streamed.Request.Headers)
	}
}

func TestPrepareInvocationLatestModelSamplingCompatibility(t *testing.T) {
	request := core.NewAIRequest("hello", "")
	request.Generation.Model = "gemini-3.7-flash"
	request.Generation.Temperature = core.SetAIParameter(float32(0))
	request.Generation.TopP = core.SetAIParameter(float32(0.5))
	request.Generation.TopK = core.SetAIParameter(8)

	compatible := NewClient("key", "", &core.NoOpLogger{})
	invocation, err := compatible.prepareInvocation(t.Context(), request, false)
	if err != nil {
		t.Fatalf("compatible preparation: %v", err)
	}
	config := preparedGenerationConfig(t, decodeGeminiPreparedBody(t, invocation.Request.Body))
	for _, field := range []string{"temperature", "topP", "topK"} {
		if _, exists := config[field]; exists {
			t.Fatalf("forbidden field %q reached latest-model request: %#v", field, config)
		}
	}
	if invocation.Request.Report.EffectiveTemperature.Mode != core.AIParameterOmit || len(invocation.Request.Report.Adjustments) != 3 {
		t.Fatalf("compatible report = %#v", invocation.Request.Report)
	}

	strict := NewClient("key", "", &core.NoOpLogger{})
	engine, err := newRequestPolicyEngineWithIntegration(nil, nil, requestpolicy.CompatibilityStrict)
	if err != nil {
		t.Fatalf("create strict policy: %v", err)
	}
	strict.requestPolicy = engine
	strictInvocation, err := strict.prepareInvocation(t.Context(), request, false)
	if err == nil || strictInvocation == nil || strictInvocation.Request == nil || strictInvocation.Request.Report == nil {
		t.Fatalf("strict preparation = %#v, %v", strictInvocation, err)
	}
	var policyErr *requestpolicy.PolicyError
	if !errors.As(err, &policyErr) || policyErr.Stage != "compatibility" {
		t.Fatalf("strict error = %T %v", err, err)
	}
}

func TestCandidateCountModelRuleCoversExtrasAndPatches(t *testing.T) {
	compatible := NewClient("key", "", &core.NoOpLogger{})
	legacy := core.NewAIRequestFromLegacy("hello", "", &core.AIOptions{
		Model: "gemini-3.7-flash",
		Extra: map[string]interface{}{"candidate_count": 2},
	})
	invocation, err := compatible.prepareInvocation(t.Context(), legacy, false)
	if err != nil {
		t.Fatalf("compatible candidateCount preparation: %v", err)
	}
	config := preparedGenerationConfig(t, decodeGeminiPreparedBody(t, invocation.Request.Body))
	if _, exists := config["candidateCount"]; exists {
		t.Fatalf("candidateCount reached Gemini 3.x body: %#v", config)
	}
	if len(invocation.Request.Report.Adjustments) != 1 || invocation.Request.Report.Adjustments[0].Path != "/generationConfig/candidateCount" {
		t.Fatalf("candidateCount adjustment = %#v", invocation.Request.Report.Adjustments)
	}

	allowed := core.NewAIRequestFromLegacy("hello", "", &core.AIOptions{
		Model: "gemini-2.5-flash",
		Extra: map[string]interface{}{"candidateCount": 2},
	})
	allowedInvocation, err := compatible.prepareInvocation(t.Context(), allowed, false)
	if err != nil {
		t.Fatalf("2.5 candidateCount preparation: %v", err)
	}
	if got := preparedGenerationConfig(t, decodeGeminiPreparedBody(t, allowedInvocation.Request.Body))["candidateCount"]; got != float64(2) {
		t.Fatalf("2.5 candidateCount = %#v", got)
	}

	for _, path := range []string{"/generationConfig/candidateCount", "/generationConfig/candidate_count", "/candidate_count"} {
		patched := core.NewAIRequest("hello", "")
		patched.Generation.Model = "gemini-3.7-flash"
		patched.Patches = []core.AIProviderPatch{{
			Name: "reintroduce-candidates", Version: "1",
			Selector: core.AIProviderSelector{Provider: "gemini"},
			Set:      map[string]interface{}{path: 2},
		}}
		if _, err := compatible.prepareInvocation(t.Context(), patched, false); err == nil {
			t.Fatalf("patch %q reintroduced candidate count", path)
		}
	}
}

func TestGenerateContentPatchPathsRequireCanonicalProfileShape(t *testing.T) {
	client := NewClient("key", "", &core.NoOpLogger{})
	tests := []struct {
		name  string
		path  string
		value interface{}
	}{
		{name: "interactions parent", path: "/generation_config/temperature", value: 0.5},
		{name: "wrong top-p casing", path: "/generationConfig/top_p", value: 0.5},
		{name: "misplaced max tokens", path: "/max_output_tokens", value: 32},
		{name: "wrong system casing", path: "/system_instruction", value: "system"},
		{name: "wrong thinking-level casing", path: "/generationConfig/thinkingConfig/thinking_level", value: "high"},
		{name: "unapproved numeric budget", path: "/generationConfig/thinkingConfig/thinkingBudget", value: 64},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := core.NewAIRequest("hello", "")
			request.Generation.Model = "gemini-2.5-flash"
			request.Patches = []core.AIProviderPatch{{
				Name: "surface-path", Version: "1",
				Selector: core.AIProviderSelector{Provider: "gemini"},
				Set:      map[string]interface{}{test.path: test.value},
			}}
			if _, err := client.prepareInvocation(t.Context(), request, false); err == nil {
				t.Fatalf("noncanonical patch %q succeeded", test.path)
			}
		})
	}

	for _, path := range []string{
		"/generation_config/temperature",
		"/generationConfig/top_p",
		"/max_output_tokens",
		"/system_instruction",
		"/generationConfig/thinkingConfig/thinking_level",
		"/generationConfig/thinkingConfig/thinkingBudget",
	} {
		t.Run("remove "+path, func(t *testing.T) {
			request := core.NewAIRequest("hello", "")
			request.Generation.Model = "gemini-2.5-flash"
			request.Patches = []core.AIProviderPatch{{
				Name: "surface-remove", Version: "1",
				Selector: core.AIProviderSelector{Provider: "gemini"},
				Remove:   []string{path},
			}}
			if _, err := client.prepareInvocation(t.Context(), request, false); err == nil {
				t.Fatalf("noncanonical removal %q succeeded", path)
			}
		})
	}

	canonical := core.NewAIRequest("hello", "")
	canonical.Generation.Model = "gemini-2.5-flash"
	canonical.Patches = []core.AIProviderPatch{{
		Name: "surface-path", Version: "1",
		Selector: core.AIProviderSelector{Provider: "gemini"},
		Set:      map[string]interface{}{selectedWireProfile.generationPaths().TopP: 0.5},
	}}
	if _, err := client.prepareInvocation(t.Context(), canonical, false); err != nil {
		t.Fatalf("canonical GenerateContent patch failed: %v", err)
	}
}

func TestReasoningLevelsAndTokenLimitsFollowExactModelSnapshot(t *testing.T) {
	client := NewClient("key", "", &core.NoOpLogger{})
	tests := []struct {
		model  string
		level  string
		wantOK bool
	}{
		{model: "gemini-3.7-flash", level: "low", wantOK: true},
		{model: "gemini-3.7-flash", level: "minimal", wantOK: false},
		{model: "gemini-3.6-flash", level: "minimal", wantOK: true},
		{model: "gemini-3.1-pro-preview", level: "high", wantOK: true},
		{model: "gemini-3.1-flash-lite", level: "minimal", wantOK: true},
		{model: "gemini-2.5-pro", level: "high", wantOK: false},
		{model: "gemini-3.7-flash-lookalike", level: "high", wantOK: false},
		{model: "gemini-3.7-flash", level: "none", wantOK: false},
		{model: "gemini-3.7-flash", level: "xhigh", wantOK: false},
	}
	for _, test := range tests {
		t.Run(test.model+"/"+test.level, func(t *testing.T) {
			request := core.NewAIRequest("hello", "")
			request.Generation.Model = test.model
			request.Generation.ReasoningEffort = core.SetAIParameter(test.level)
			_, err := client.prepareInvocation(t.Context(), request, false)
			if (err == nil) != test.wantOK {
				t.Fatalf("prepare error = %v, wantOK=%t", err, test.wantOK)
			}
			if err != nil && !errors.Is(err, core.ErrAIRequestFeatureUnsupported) {
				t.Fatalf("reasoning error = %T %v", err, err)
			}
		})
	}

	tooLarge := core.NewAIRequest("hello", "")
	tooLarge.Generation.Model = "gemini-2.5-flash"
	tooLarge.Generation.MaxTokens = core.SetAIParameter(geminiOutputTokenLimit + 1)
	if _, err := client.prepareInvocation(t.Context(), tooLarge, false); err == nil || !strings.Contains(err.Error(), "model limit") {
		t.Fatalf("oversized max tokens error = %v", err)
	}
}

func TestProtectedStatelessnessAndTransportFieldsCannotBePatched(t *testing.T) {
	client := NewClient("key", "", &core.NoOpLogger{})
	for _, mutation := range []struct {
		path  string
		value interface{}
	}{
		{path: "/store", value: true},
		{path: "/Store", value: true},
		{path: "/contents", value: []interface{}{}},
		{path: "/background", value: true},
		{path: "/previous_interaction_id", value: "state"},
		{path: "/stream", value: true},
	} {
		t.Run(mutation.path, func(t *testing.T) {
			request := core.NewAIRequest("hello", "")
			request.Generation.Model = "gemini-2.5-flash"
			request.Patches = []core.AIProviderPatch{{
				Name: "structural", Version: "1",
				Selector: core.AIProviderSelector{Provider: "gemini"},
				Set:      map[string]interface{}{mutation.path: mutation.value},
			}}
			if _, err := client.prepareInvocation(t.Context(), request, false); err == nil {
				t.Fatalf("protected mutation %q succeeded", mutation.path)
			}
		})
	}

	legacy := core.NewAIRequestFromLegacy("hello", "", &core.AIOptions{
		Model:   "gemini-2.5-flash",
		Extra:   map[string]interface{}{"Store": true, "background": true, "previous_interaction_id": "state"},
		Headers: map[string]string{"x-goog-api-key": "wrong", "Authorization": "wrong"},
	})
	invocation, err := client.prepareInvocation(context.Background(), legacy, false)
	if err != nil {
		t.Fatalf("prepare legacy protected extras: %v", err)
	}
	body := decodeGeminiPreparedBody(t, invocation.Request.Body)
	if body["store"] != false || body["background"] != nil || body["previous_interaction_id"] != nil {
		t.Fatalf("protected extras reached body: %#v", body)
	}
	if invocation.Request.Headers.Get("x-goog-api-key") != "" || invocation.Request.Headers.Get("Authorization") != "" {
		t.Fatalf("credential headers reached prepared request: %#v", invocation.Request.Headers)
	}
}

func TestPortableGenerationIntentRejectsInvalidModesAndValues(t *testing.T) {
	client := NewClient("key", "", &core.NoOpLogger{})
	tests := []struct {
		name   string
		mutate func(*core.AIRequest)
		want   string
	}{
		{
			name: "invalid mode",
			mutate: func(request *core.AIRequest) {
				request.Generation.TopP = core.AIParameter[float32]{Mode: core.AIParameterMode(255)}
			},
			want: "invalid generation.top_p mode",
		},
		{
			name: "nonpositive max tokens",
			mutate: func(request *core.AIRequest) {
				request.Generation.MaxTokens = core.SetAIParameter(0)
			},
			want: "generation.max_tokens must be positive",
		},
		{
			name: "temperature above range",
			mutate: func(request *core.AIRequest) {
				request.Generation.Temperature = core.SetAIParameter(float32(2.1))
			},
			want: "temperature must be between",
		},
		{
			name: "top-p below range",
			mutate: func(request *core.AIRequest) {
				request.Generation.TopP = core.SetAIParameter(float32(-0.1))
			},
			want: "topP must be between",
		},
		{
			name: "top-k below range",
			mutate: func(request *core.AIRequest) {
				request.Generation.TopK = core.SetAIParameter(0)
			},
			want: "topK must be at least",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := core.NewAIRequest("hello", "")
			request.Generation.Model = "gemini-2.5-flash"
			test.mutate(request)
			invocation, err := client.prepareInvocation(t.Context(), request, false)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("prepare = %#v, %v; want %q", invocation, err, test.want)
			}
		})
	}
}

func TestLatestModelPrefilledTurnValidation(t *testing.T) {
	body := map[string]interface{}{
		"contents": []interface{}{
			map[string]interface{}{"role": "user", "parts": []interface{}{map[string]interface{}{"text": "hello"}}},
			map[string]interface{}{"role": "model", "parts": []interface{}{map[string]interface{}{"text": "prefix"}}},
		},
		"generationConfig": map[string]interface{}{"maxOutputTokens": 32},
		"store":            false,
	}
	document, err := requestpolicy.NewDocument(requestpolicy.DocumentConfig{
		Info: requestpolicy.RequestInfo{
			Provider: "gemini", ProviderAlias: "gemini", Surface: "generate-content",
			Operation: "generate", RequestedModel: "gemini-3.7-flash", ResolvedModel: "gemini-3.7-flash",
		},
		Body:           body,
		ProtectedPaths: []string{"/contents", "/store"},
	})
	if err != nil {
		t.Fatalf("create draft document: %v", err)
	}
	capabilities, ok := capabilitiesForModel("gemini-3.7-flash")
	if !ok {
		t.Fatal("missing gemini-3.7-flash capabilities")
	}
	draft := &geminiDraft{Document: document, profile: selectedWireProfile, capabilities: capabilities}
	if err := draft.Validate(); err == nil || !strings.Contains(err.Error(), "prefilled model turn") {
		t.Fatalf("prefilled validation error = %v", err)
	}
}
