package core

import (
	"context"
	"errors"
	"math"
	"reflect"
	"strings"
	"testing"
)

type requestContextKey struct{}

type legacyRequestClient struct {
	response *AIResponse
	err      error
	calls    int
	ctx      context.Context
	prompt   string
	options  *AIOptions
}

func (c *legacyRequestClient) GenerateResponse(ctx context.Context, prompt string, options *AIOptions) (*AIResponse, error) {
	c.calls++
	c.ctx = ctx
	c.prompt = prompt
	c.options = options
	return c.response, c.err
}

type advancedRequestClient struct {
	legacyRequestClient
	result  *AIResult
	err     error
	request *AIRequest
}

func (c *advancedRequestClient) Generate(ctx context.Context, request *AIRequest) (*AIResult, error) {
	c.ctx = ctx
	c.request = request
	return c.result, c.err
}

func TestAIParameterConstructors(t *testing.T) {
	inherit := InheritAIParameter[int]()
	if inherit.Mode != AIParameterInherit || inherit.Value != 0 {
		t.Fatalf("unexpected inherited parameter: %#v", inherit)
	}

	set := SetAIParameter(0)
	if set.Mode != AIParameterSet || set.Value != 0 {
		t.Fatalf("explicit zero was not preserved: %#v", set)
	}

	omit := OmitAIParameter[string]()
	if omit.Mode != AIParameterOmit || omit.Value != "" {
		t.Fatalf("unexpected omitted parameter: %#v", omit)
	}
}

func TestNewAIRequest(t *testing.T) {
	request := NewAIRequest("prompt", "planning")
	if request.Prompt != "prompt" || request.Purpose != "planning" {
		t.Fatalf("unexpected request: %#v", request)
	}
	if request.LegacyOptions() != nil {
		t.Fatal("new request unexpectedly contains legacy options")
	}
}

func TestNewAIRequestFromLegacy_IsolatesCallerOptions(t *testing.T) {
	cycle := map[string]interface{}{}
	cycle["self"] = cycle
	sliceCycle := make([]interface{}, 1)
	sliceCycle[0] = sliceCycle
	array := [1][]string{{"original"}}
	original := &AIOptions{
		Model:   "legacy-model",
		Headers: map[string]string{"X-Test": "original"},
		Extra: map[string]interface{}{
			"nested": map[string]interface{}{
				"items": []interface{}{map[string]interface{}{"value": "original"}},
			},
			"labels":      map[string]string{"environment": "original"},
			"names":       []string{"original"},
			"typed":       []map[string]string{{"value": "original"}},
			"array":       array,
			"nil":         nil,
			"nil-map":     map[string]string(nil),
			"nil-slice":   []string(nil),
			"cycle":       cycle,
			"slice-cycle": sliceCycle,
		},
	}

	request := NewAIRequestFromLegacy("prompt", "purpose", original)
	original.Model = "mutated"
	original.Headers["X-Test"] = "mutated"
	original.Extra["nested"].(map[string]interface{})["items"].([]interface{})[0].(map[string]interface{})["value"] = "mutated"
	original.Extra["labels"].(map[string]string)["environment"] = "mutated"
	original.Extra["names"].([]string)[0] = "mutated"
	original.Extra["typed"].([]map[string]string)[0]["value"] = "mutated"
	array[0][0] = "mutated"
	cycle["caller-only"] = true
	sliceCycle[0].([]interface{})[0] = "caller-only"

	first := request.LegacyOptions()
	if first.Model != "legacy-model" || first.Headers["X-Test"] != "original" {
		t.Fatalf("legacy scalar snapshot changed: %#v", first)
	}
	nested := first.Extra["nested"].(map[string]interface{})
	if got := nested["items"].([]interface{})[0].(map[string]interface{})["value"]; got != "original" {
		t.Fatalf("nested legacy value changed: %v", got)
	}
	if got := first.Extra["labels"].(map[string]string)["environment"]; got != "original" {
		t.Fatalf("nested string map changed: %v", got)
	}
	if got := first.Extra["names"].([]string)[0]; got != "original" {
		t.Fatalf("nested string slice changed: %v", got)
	}
	if got := first.Extra["typed"].([]map[string]string)[0]["value"]; got != "original" {
		t.Fatalf("typed nested legacy value changed: %v", got)
	}
	if got := first.Extra["array"].([1][]string)[0][0]; got != "original" {
		t.Fatalf("array-backed legacy value changed: %v", got)
	}
	if first.Extra["nil"] != nil || first.Extra["nil-map"].(map[string]string) != nil || first.Extra["nil-slice"].([]string) != nil {
		t.Fatal("typed nil legacy values were not preserved")
	}
	clonedCycle := first.Extra["cycle"].(map[string]interface{})
	if _, exists := clonedCycle["caller-only"]; exists {
		t.Fatal("caller mutation reached cyclic legacy snapshot")
	}
	clonedCycle["copy-only"] = true
	if _, exists := clonedCycle["self"].(map[string]interface{})["copy-only"]; !exists {
		t.Fatal("cycle was not preserved within legacy snapshot")
	}
	clonedSliceCycle := first.Extra["slice-cycle"].([]interface{})
	clonedSliceCycle[0].([]interface{})[0] = "copy-only"
	if got := clonedSliceCycle[0]; got != "copy-only" {
		t.Fatalf("slice cycle was not preserved within legacy snapshot: %v", got)
	}

	first.Headers["X-Test"] = "first-copy"
	first.Extra["names"].([]string)[0] = "first-copy"
	second := request.LegacyOptions()
	if second.Headers["X-Test"] != "original" || second.Extra["names"].([]string)[0] != "original" {
		t.Fatal("mutating one LegacyOptions result changed a later result")
	}
}

func TestAIRequest_LegacyOptionsNilReceiver(t *testing.T) {
	var request *AIRequest
	if request.LegacyOptions() != nil {
		t.Fatal("nil request returned legacy options")
	}
}

func TestCloneAIRequest_IsolatesPatchesAndLegacyOptions(t *testing.T) {
	original := NewAIRequestFromLegacy("prompt", "planning", &AIOptions{
		Headers: map[string]string{"X-Legacy": "original"},
	})
	original.Generation.Model = "requested-model"
	original.Patches = []AIProviderPatch{{
		Name:          "test-patch",
		Remove:        []string{"/old"},
		SetHeaders:    map[string]string{"X-Patch": "original"},
		RemoveHeaders: []string{"X-Old"},
		Set: map[string]interface{}{
			"/nested": map[string]interface{}{
				"items": []interface{}{map[string]interface{}{"value": "original"}},
			},
			"/typed": map[string][]string{"names": {"original"}},
		},
	}}

	clone, err := CloneAIRequest(original)
	if err != nil {
		t.Fatalf("CloneAIRequest returned error: %v", err)
	}
	clone.Prompt = "clone"
	clone.Patches[0].Remove[0] = "/clone"
	clone.Patches[0].SetHeaders["X-Patch"] = "clone"
	clone.Patches[0].RemoveHeaders[0] = "X-Clone"
	clone.Patches[0].Set["/nested"].(map[string]interface{})["items"].([]interface{})[0].(map[string]interface{})["value"] = "clone"
	clone.Patches[0].Set["/typed"].(map[string][]string)["names"][0] = "clone"
	legacy := clone.LegacyOptions()
	legacy.Headers["X-Legacy"] = "clone"

	if original.Prompt != "prompt" || original.Patches[0].Remove[0] != "/old" {
		t.Fatal("clone mutation changed original request")
	}
	if original.Patches[0].SetHeaders["X-Patch"] != "original" || original.Patches[0].RemoveHeaders[0] != "X-Old" {
		t.Fatal("clone mutation changed original patch headers")
	}
	originalNested := original.Patches[0].Set["/nested"].(map[string]interface{})
	if got := originalNested["items"].([]interface{})[0].(map[string]interface{})["value"]; got != "original" {
		t.Fatalf("clone mutation changed nested patch value: %v", got)
	}
	if got := original.Patches[0].Set["/typed"].(map[string][]string)["names"][0]; got != "original" {
		t.Fatalf("clone mutation changed typed patch value: %v", got)
	}
	if got := original.LegacyOptions().Headers["X-Legacy"]; got != "original" {
		t.Fatalf("clone mutation changed legacy snapshot: %v", got)
	}
}

func TestCloneAIRequest_RejectsInvalidInput(t *testing.T) {
	if _, err := CloneAIRequest(nil); err == nil || err.Error() != "AI request is nil" {
		t.Fatalf("unexpected nil request error: %v", err)
	}

	cycle := map[string]interface{}{}
	cycle["self"] = cycle
	request := NewAIRequest("prompt", "purpose")
	request.Patches = []AIProviderPatch{{Name: "cycle", Set: map[string]interface{}{"/cycle": cycle}}}
	if _, err := CloneAIRequest(request); err == nil || !strings.Contains(err.Error(), "cyclic map") {
		t.Fatalf("expected cyclic patch error, got %v", err)
	}

	request.Patches = []AIProviderPatch{{Name: "function", Set: map[string]interface{}{"/function": func() {}}}}
	if _, err := CloneAIRequest(request); err == nil || !strings.Contains(err.Error(), "unsupported JSON-compatible value") {
		t.Fatalf("expected unsupported patch value error, got %v", err)
	}

	request.Patches = []AIProviderPatch{{Name: "map-key", Set: map[string]interface{}{"/map": map[int]string{1: "one"}}}}
	if _, err := CloneAIRequest(request); err == nil || !strings.Contains(err.Error(), "unsupported map key type") {
		t.Fatalf("expected unsupported map key error, got %v", err)
	}

	sliceCycle := make([]interface{}, 1)
	sliceCycle[0] = sliceCycle
	request.Patches = []AIProviderPatch{{Name: "slice-cycle", Set: map[string]interface{}{"/slice": sliceCycle}}}
	if _, err := CloneAIRequest(request); err == nil || !strings.Contains(err.Error(), "cyclic slice") {
		t.Fatalf("expected cyclic slice error, got %v", err)
	}

	request.Patches = []AIProviderPatch{{Name: "nan", Set: map[string]interface{}{"/number": math.NaN()}}}
	if _, err := CloneAIRequest(request); err == nil || !strings.Contains(err.Error(), "non-finite") {
		t.Fatalf("expected non-finite number error, got %v", err)
	}
}

func TestCloneAIRequest_ClonesJSONCompatibleValueShapes(t *testing.T) {
	type label string
	bytes := []byte{1, 2, 3}
	request := NewAIRequest("prompt", "purpose")
	request.Patches = []AIProviderPatch{{
		Name: "shapes",
		Set: map[string]interface{}{
			"/array":     [2]int{1, 2},
			"/bytes":     bytes,
			"/items":     []interface{}{nil},
			"/label":     label("value"),
			"/nil":       nil,
			"/nil-map":   map[string]interface{}(nil),
			"/nil-slice": []interface{}(nil),
			"/uintptr":   uintptr(3),
		},
	}}

	clone, err := CloneAIRequest(request)
	if err != nil {
		t.Fatalf("CloneAIRequest returned error: %v", err)
	}
	if !reflect.DeepEqual(clone.Patches[0].Set, request.Patches[0].Set) {
		t.Fatalf("cloned shapes changed values:\n got: %#v\nwant: %#v", clone.Patches[0].Set, request.Patches[0].Set)
	}
	clone.Patches[0].Set["/bytes"].([]byte)[0] = 9
	if bytes[0] != 1 {
		t.Fatal("cloned byte slice aliases original")
	}

	empty, err := CloneAIRequest(NewAIRequest("prompt", "purpose"))
	if err != nil || empty.Patches != nil {
		t.Fatalf("unexpected empty request clone: clone=%#v err=%v", empty, err)
	}

	request.Patches = []AIProviderPatch{{Name: "empty"}}
	emptyPatch, err := CloneAIRequest(request)
	if err != nil || len(emptyPatch.Patches) != 1 || emptyPatch.Patches[0].Set != nil {
		t.Fatalf("unexpected empty patch clone: clone=%#v err=%v", emptyPatch, err)
	}
}

func TestGenerateAI_UsesAdvancedCapability(t *testing.T) {
	wantResult := &AIResult{Response: &AIResponse{Content: "advanced"}}
	wantErr := errors.New("advanced error")
	client := &advancedRequestClient{result: wantResult, err: wantErr}
	request := NewAIRequest("prompt", "planning")
	ctx := context.WithValue(context.Background(), requestContextKey{}, "trace-value")

	result, err := GenerateAI(ctx, client, request)
	if result != wantResult || !errors.Is(err, wantErr) {
		t.Fatalf("unexpected advanced result: result=%#v err=%v", result, err)
	}
	if client.request != request {
		t.Fatal("advanced client did not receive the request")
	}
	if got := client.ctx.Value(requestContextKey{}); got != "trace-value" {
		t.Fatalf("advanced client did not receive propagated context: %v", got)
	}
	if client.calls != 0 {
		t.Fatal("legacy method was called for request-capable client")
	}
}

func TestGenerateAI_LegacyFallback(t *testing.T) {
	client := &legacyRequestClient{response: &AIResponse{
		Content:  "response",
		Model:    "resolved-model",
		Provider: "provider",
	}}
	request := NewAIRequestFromLegacy("prompt", "planning", &AIOptions{
		Model:       "legacy-model",
		Temperature: 0.4,
		Extra:       map[string]interface{}{"legacy": true},
		Headers:     map[string]string{"X-Legacy": "yes"},
	})
	request.Generation = AIGenerationOptions{
		Model:           "new-model",
		Temperature:     SetAIParameter(float32(0.2)),
		MaxTokens:       SetAIParameter(2048),
		SystemPrompt:    SetAIParameter("system"),
		ReasoningEffort: SetAIParameter("high"),
		ResponseFormat:  SetAIParameter("json"),
	}

	ctx := context.WithValue(context.Background(), requestContextKey{}, "trace-value")
	result, err := GenerateAI(ctx, client, request)
	if err != nil {
		t.Fatalf("GenerateAI returned error: %v", err)
	}
	if client.calls != 1 || client.prompt != "prompt" {
		t.Fatalf("unexpected legacy call: calls=%d prompt=%q", client.calls, client.prompt)
	}
	if got := client.ctx.Value(requestContextKey{}); got != "trace-value" {
		t.Fatalf("legacy client did not receive propagated context: %v", got)
	}
	wantOptions := &AIOptions{
		Model:           "new-model",
		Temperature:     0.2,
		MaxTokens:       2048,
		SystemPrompt:    "system",
		ReasoningEffort: "high",
		ResponseFormat:  "json",
		Extra:           map[string]interface{}{"legacy": true},
		Headers:         map[string]string{"X-Legacy": "yes"},
	}
	if !reflect.DeepEqual(client.options, wantOptions) {
		t.Fatalf("legacy options mismatch:\n got: %#v\nwant: %#v", client.options, wantOptions)
	}
	if result.Response != client.response {
		t.Fatal("fallback result did not preserve response")
	}
	wantReport := &AIRequestReport{
		Provider:      "provider",
		ResolvedModel: "resolved-model",
		Purpose:       "planning",
		Stable:        false,
	}
	if !reflect.DeepEqual(result.RequestReport, wantReport) {
		t.Fatalf("fallback report mismatch:\n got: %#v\nwant: %#v", result.RequestReport, wantReport)
	}

	client.options.Extra["legacy"] = false
	client.options.Headers["X-Legacy"] = "changed"
	legacy := request.LegacyOptions()
	if legacy.Extra["legacy"] != true || legacy.Headers["X-Legacy"] != "yes" {
		t.Fatal("legacy client mutated the request snapshot")
	}
}

func TestGenerateAI_LegacyFallbackWithoutOptionsPassesNil(t *testing.T) {
	client := &legacyRequestClient{response: &AIResponse{Content: "response"}}
	if _, err := GenerateAI(context.Background(), client, NewAIRequest("prompt", "purpose")); err != nil {
		t.Fatalf("GenerateAI returned error: %v", err)
	}
	if client.options != nil {
		t.Fatalf("legacy client received unexpected options: %#v", client.options)
	}
}

func TestGenerateAI_LegacyFallbackAllocatesOptionsForPortableModel(t *testing.T) {
	client := &legacyRequestClient{response: &AIResponse{Content: "response"}}
	request := NewAIRequest("prompt", "purpose")
	request.Generation.Model = "model"

	if _, err := GenerateAI(context.Background(), client, request); err != nil {
		t.Fatalf("GenerateAI returned error: %v", err)
	}
	if client.options == nil || client.options.Model != "model" {
		t.Fatalf("legacy client did not receive portable model: %#v", client.options)
	}
}

func TestGenerateAI_RejectsUnsupportedLegacyFeaturesBeforeCall(t *testing.T) {
	tests := []struct {
		name    string
		feature string
		mutate  func(*AIRequest)
	}{
		{name: "patches", feature: "provider_patches", mutate: func(r *AIRequest) {
			r.Patches = []AIProviderPatch{{Name: "patch"}}
		}},
		{name: "temperature omit", feature: "generation.temperature", mutate: func(r *AIRequest) {
			r.Generation.Temperature = OmitAIParameter[float32]()
		}},
		{name: "temperature explicit zero", feature: "generation.temperature", mutate: func(r *AIRequest) {
			r.Generation.Temperature = SetAIParameter(float32(0))
		}},
		{name: "top p set", feature: "generation.top_p", mutate: func(r *AIRequest) {
			r.Generation.TopP = SetAIParameter(float32(0.9))
		}},
		{name: "top k omit", feature: "generation.top_k", mutate: func(r *AIRequest) {
			r.Generation.TopK = OmitAIParameter[int]()
		}},
		{name: "max tokens explicit zero", feature: "generation.max_tokens", mutate: func(r *AIRequest) {
			r.Generation.MaxTokens = SetAIParameter(0)
		}},
		{name: "system prompt empty", feature: "generation.system_prompt", mutate: func(r *AIRequest) {
			r.Generation.SystemPrompt = SetAIParameter("")
		}},
		{name: "reasoning effort omit", feature: "generation.reasoning_effort", mutate: func(r *AIRequest) {
			r.Generation.ReasoningEffort = OmitAIParameter[string]()
		}},
		{name: "response format empty", feature: "generation.response_format", mutate: func(r *AIRequest) {
			r.Generation.ResponseFormat = SetAIParameter("")
		}},
		{name: "invalid mode", feature: "generation.temperature.mode", mutate: func(r *AIRequest) {
			r.Generation.Temperature = AIParameter[float32]{Mode: AIParameterMode(99)}
		}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			client := &legacyRequestClient{response: &AIResponse{Content: "response"}}
			request := NewAIRequest("prompt", "purpose")
			test.mutate(request)

			result, err := GenerateAI(context.Background(), client, request)
			if result != nil {
				t.Fatalf("unexpected result: %#v", result)
			}
			if !errors.Is(err, ErrAIRequestFeatureUnsupported) {
				t.Fatalf("expected unsupported feature error, got %v", err)
			}
			var featureErr *AIRequestFeatureError
			if !errors.As(err, &featureErr) || featureErr.Feature != test.feature {
				t.Fatalf("unexpected feature error: %#v", featureErr)
			}
			if client.calls != 0 {
				t.Fatalf("legacy client called %d times", client.calls)
			}
		})
	}
}

func TestGenerateAI_ValidatesInputsAndLegacyResults(t *testing.T) {
	request := NewAIRequest("prompt", "purpose")
	if _, err := GenerateAI(context.Background(), nil, request); err == nil || err.Error() != "AI client is nil" {
		t.Fatalf("unexpected nil client error: %v", err)
	}

	client := &legacyRequestClient{}
	if _, err := GenerateAI(context.Background(), client, nil); err == nil || err.Error() != "AI request is nil" {
		t.Fatalf("unexpected nil request error: %v", err)
	}

	wantErr := errors.New("legacy failure")
	client.err = wantErr
	if _, err := GenerateAI(context.Background(), client, request); !errors.Is(err, wantErr) {
		t.Fatalf("legacy error was not preserved: %v", err)
	}

	client.err = nil
	if _, err := GenerateAI(context.Background(), client, request); err == nil || err.Error() != "AI client returned a nil response without error" {
		t.Fatalf("unexpected nil response error: %v", err)
	}
}

func TestAIRequestFeatureError(t *testing.T) {
	err := &AIRequestFeatureError{ClientType: "*core.testClient", Feature: "generation.top_p"}
	want := "*core.testClient does not support AI request feature \"generation.top_p\""
	if err.Error() != want {
		t.Fatalf("unexpected error text: %q", err.Error())
	}
	if !errors.Is(err, ErrAIRequestFeatureUnsupported) || errors.Is(err, ErrAIOperationFailed) {
		t.Fatal("feature error sentinel matching is incorrect")
	}
}
