package openaiwire_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"reflect"
	"strings"
	"testing"

	"github.com/truvaagents/truva-g3/ai/providerkit/openaiwire"
	"github.com/truvaagents/truva-g3/ai/requestpolicy"
	"github.com/truvaagents/truva-g3/core"
)

func TestCodecBuildDraftPortableIntentAndIsolation(t *testing.T) {
	codec := mustCodec(t)
	extra := map[string]interface{}{
		"top_p":       0.9,
		"custom":      map[string]interface{}{"enabled": true},
		"temperature": 0.8,
		"MODEL":       "bypass",
		"Stream":      true,
	}
	headers := map[string]string{
		"X-Tenant":      "tenant-a",
		"Authorization": "caller-secret",
	}
	request := core.NewAIRequestFromLegacy("hello", "test", &core.AIOptions{
		Model:        "smart",
		MaxTokens:    100,
		SystemPrompt: "legacy system",
		Extra:        extra,
		Headers:      headers,
	})
	request.Generation.Temperature = core.SetAIParameter(float32(0))
	request.Generation.TopP = core.OmitAIParameter[float32]()
	request.Generation.SystemPrompt = core.OmitAIParameter[string]()

	draft, err := codec.BuildDraft(request, "gpt-4.1", false)
	if err != nil {
		t.Fatalf("BuildDraft returned error: %v", err)
	}
	if err := draft.BindIdentity("enterprise-openai", "enterprise-openai.us"); err != nil {
		t.Fatalf("BindIdentity returned error: %v", err)
	}
	encoded, err := codec.Encode(draft)
	if err != nil {
		t.Fatalf("Encode returned error: %v", err)
	}
	var body map[string]interface{}
	if err := json.Unmarshal(encoded, &body); err != nil {
		t.Fatalf("decode encoded body: %v", err)
	}
	if got := body["temperature"]; got != float64(0) {
		t.Fatalf("temperature = %#v, want explicit zero", got)
	}
	if _, exists := body["top_p"]; exists {
		t.Fatal("top_p must be omitted")
	}
	if _, exists := body["MODEL"]; exists {
		t.Fatal("case-variant structural model field bypassed protection")
	}
	if _, exists := body["Stream"]; exists {
		t.Fatal("case-variant stream field bypassed protection")
	}
	messages := body["messages"].([]interface{})
	if len(messages) != 1 {
		t.Fatalf("messages = %#v, want only user message", messages)
	}
	if got := draft.Info().Provider; got != "enterprise-openai" {
		t.Fatalf("provider identity = %q", got)
	}
	if _, exists := draft.Header("Authorization"); exists {
		t.Fatal("protected Authorization header reached the logical draft")
	}
	if got, exists := draft.Header("X-Tenant"); !exists || got != "tenant-a" {
		t.Fatalf("eligible header = %q, %v", got, exists)
	}
	if got := draft.ProtectedHeaderConflicts(); !reflect.DeepEqual(got, []string{"Authorization"}) {
		t.Fatalf("protected conflicts = %#v", got)
	}

	extra["custom"].(map[string]interface{})["enabled"] = false
	headers["X-Tenant"] = "mutated"
	encodedAfterMutation, err := codec.Encode(draft)
	if err != nil {
		t.Fatalf("Encode after caller mutation returned error: %v", err)
	}
	if !bytes.Equal(encoded, encodedAfterMutation) {
		t.Fatalf("draft changed after caller mutation:\nfirst: %s\nafter: %s", encoded, encodedAfterMutation)
	}

	adjustmentPaths := make([]string, 0, len(draft.Adjustments()))
	for _, adjustment := range draft.Adjustments() {
		adjustmentPaths = append(adjustmentPaths, adjustment.Path)
	}
	if !reflect.DeepEqual(adjustmentPaths, []string{"/messages", "/top_p"}) {
		t.Fatalf("portable adjustments = %#v", draft.Adjustments())
	}
}

func TestCodecPolicyApplicationAndProtectedFields(t *testing.T) {
	codec := mustCodec(t)
	request := core.NewAIRequestFromLegacy("hello", "policy-test", &core.AIOptions{
		Model:     "gpt-4.1",
		MaxTokens: 100,
	})
	draft, err := codec.BuildDraft(request, "gpt-4.1", false)
	if err != nil {
		t.Fatalf("BuildDraft returned error: %v", err)
	}
	if err := draft.BindIdentity("openai", "openai"); err != nil {
		t.Fatalf("BindIdentity returned error: %v", err)
	}
	engine, err := requestpolicy.NewEngine(requestpolicy.Config{
		Mode: requestpolicy.CompatibilityCompatible,
		AppRules: []core.AIProviderPatch{{
			Name:    "tenant-controls",
			Version: "1",
			Selector: core.AIProviderSelector{
				Provider: "openai",
				Surface:  "chat-completions",
			},
			Set:        map[string]interface{}{"/top_p": 0.25},
			SetHeaders: map[string]string{"X-Policy": "applied"},
		}},
	})
	if err != nil {
		t.Fatalf("NewEngine returned error: %v", err)
	}
	report, err := engine.Apply(t.Context(), draft, nil)
	if err != nil {
		t.Fatalf("Apply returned error: %v", err)
	}
	if report == nil || !report.Stable || report.Provider != "openai" || len(report.Adjustments) != 2 {
		t.Fatalf("request report = %#v", report)
	}
	if value, exists := draft.Get("/top_p"); !exists || value != 0.25 {
		t.Fatalf("top_p = %#v, %v", value, exists)
	}

	protectedEngine, err := requestpolicy.NewEngine(requestpolicy.Config{
		Mode: requestpolicy.CompatibilityCompatible,
		AppRules: []core.AIProviderPatch{{
			Name:     "invalid",
			Version:  "1",
			Selector: core.AIProviderSelector{AllProviders: true},
			Set:      map[string]interface{}{"/model": "other"},
		}},
	})
	if err != nil {
		t.Fatalf("NewEngine for protected test returned error: %v", err)
	}
	_, err = protectedEngine.Apply(t.Context(), draft, nil)
	if err == nil || !strings.Contains(err.Error(), "protected") {
		t.Fatalf("protected model mutation error = %v", err)
	}
}

func TestCodecSyncAndStreamDraftParity(t *testing.T) {
	codec := mustCodec(t)
	request := core.NewAIRequestFromLegacy("hello", "parity", &core.AIOptions{
		Model:       "gpt-4.1",
		Temperature: 0.4,
		MaxTokens:   200,
		Extra:       map[string]interface{}{"seed": 42},
		Headers:     map[string]string{"X-Test": "value"},
	})
	syncDraft, err := codec.BuildDraft(request, "gpt-4.1", false)
	if err != nil {
		t.Fatalf("sync BuildDraft returned error: %v", err)
	}
	streamDraft, err := codec.BuildDraft(request, "gpt-4.1", true)
	if err != nil {
		t.Fatalf("stream BuildDraft returned error: %v", err)
	}
	syncBody := cloneBody(t, syncDraft.Body())
	streamBody := cloneBody(t, streamDraft.Body())
	delete(streamBody, "stream")
	delete(streamBody, "stream_options")
	if !reflect.DeepEqual(syncBody, streamBody) {
		t.Fatalf("sync/stream semantic bodies differ:\nsync: %#v\nstream: %#v", syncBody, streamBody)
	}
	if !reflect.DeepEqual(syncDraft.Headers(), streamDraft.Headers()) {
		t.Fatalf("sync/stream headers differ: %#v vs %#v", syncDraft.Headers(), streamDraft.Headers())
	}
}

func TestCodecPresenceAwareMaxTokensMatchesLegacyReasoningBudget(t *testing.T) {
	tests := []struct {
		name            string
		model           string
		reasoningEffort string
		wantPath        string
		wantTokens      int
	}{
		{
			name:       "active reasoning applies multiplier",
			model:      "gpt-5",
			wantPath:   "max_completion_tokens",
			wantTokens: 1000 * openaiwire.DefaultReasoningTokenMultiplier,
		},
		{
			name:            "disabled reasoning keeps raw budget",
			model:           "gpt-5",
			reasoningEffort: "none",
			wantPath:        "max_completion_tokens",
			wantTokens:      1000,
		},
		{
			name:       "standard model keeps raw budget",
			model:      "gpt-4.1",
			wantPath:   "max_tokens",
			wantTokens: 1000,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			codec := mustCodec(t)
			legacyRequest := core.NewAIRequestFromLegacy("hello", "legacy", &core.AIOptions{
				MaxTokens:       1000,
				ReasoningEffort: test.reasoningEffort,
			})
			presenceRequest := core.NewAIRequest("hello", "presence-aware")
			presenceRequest.Generation.MaxTokens = core.SetAIParameter(1000)
			if test.reasoningEffort != "" {
				presenceRequest.Generation.ReasoningEffort = core.SetAIParameter(test.reasoningEffort)
			}

			legacyDraft, err := codec.BuildDraft(legacyRequest, test.model, false)
			if err != nil {
				t.Fatalf("legacy BuildDraft returned error: %v", err)
			}
			presenceDraft, err := codec.BuildDraft(presenceRequest, test.model, false)
			if err != nil {
				t.Fatalf("presence-aware BuildDraft returned error: %v", err)
			}

			legacyTokens := legacyDraft.Body()[test.wantPath]
			presenceTokens := presenceDraft.Body()[test.wantPath]
			if legacyTokens != test.wantTokens || presenceTokens != test.wantTokens {
				t.Fatalf(
					"%s budgets: legacy=%#v presence-aware=%#v, want %d",
					test.wantPath,
					legacyTokens,
					presenceTokens,
					test.wantTokens,
				)
			}
		})
	}
}

func TestCodecClassifiesTemperatureWithActiveReasoningAsUnsupported(t *testing.T) {
	codec := mustCodec(t)
	request := core.NewAIRequest("hello", "reasoning-temperature")
	request.Generation.Temperature = core.SetAIParameter(float32(0.5))

	_, err := codec.BuildDraft(request, "gpt-5", false)
	if !errors.Is(err, core.ErrAIRequestFeatureUnsupported) {
		t.Fatalf("BuildDraft error = %v, want unsupported feature", err)
	}
	var featureErr *core.AIRequestFeatureError
	if !errors.As(err, &featureErr) || featureErr.Feature != "generation.temperature" {
		t.Fatalf("BuildDraft feature error = %#v", featureErr)
	}
}

func TestCodecConfiguredReasoningObjectForCompatibleStandardModel(t *testing.T) {
	codec, err := openaiwire.NewConfiguredCodec(openaiwire.Config{
		SurfaceVersion:         "ollama-chat-completions-v1",
		DefaultReasoningEffort: "high",
		ForceReasoningObject:   true,
	})
	if err != nil {
		t.Fatalf("NewConfiguredCodec returned error: %v", err)
	}
	draft, err := codec.BuildDraft(core.NewAIRequest("hello", "ollama"), "gemma4:31b", false)
	if err != nil {
		t.Fatalf("BuildDraft returned error: %v", err)
	}
	if got := draft.Body()["reasoning"]; !reflect.DeepEqual(got, map[string]interface{}{"effort": "high"}) {
		t.Fatalf("reasoning = %#v", got)
	}
}

func TestCodecDecodeNormalizesUsageDetails(t *testing.T) {
	codec := mustCodec(t)
	result, err := codec.Decode(strings.NewReader(`{
		"model":"gpt-5",
		"choices":[{"message":{"content":"","reasoning_content":"answer"},"finish_reason":"stop"}],
		"usage":{
			"prompt_tokens":10,"completion_tokens":6,"total_tokens":16,
			"prompt_tokens_details":{"cached_tokens":4,"audio_tokens":2},
			"completion_tokens_details":{"reasoning_tokens":3,"audio_tokens":1}
		}
	}`))
	if err != nil {
		t.Fatalf("Decode returned error: %v", err)
	}
	if result.Response.Content != "answer" || result.Response.Provider != "" {
		t.Fatalf("response = %#v", result.Response)
	}
	wantDetails := &core.AIUsageDetails{
		CachedInputTokens: 4,
		ReasoningTokens:   3,
		AudioInputTokens:  2,
		AudioOutputTokens: 1,
	}
	if !reflect.DeepEqual(result.UsageDetails, wantDetails) {
		t.Fatalf("usage details = %#v, want %#v", result.UsageDetails, wantDetails)
	}
}

func TestCodecDecodeStreamNormalizesContentUsageAndFinish(t *testing.T) {
	codec := mustCodec(t)
	stream := strings.Join([]string{
		`data: {"model":"gpt-5","choices":[{"delta":{"reasoning_content":"think "}}]}`,
		`data: malformed`,
		`data: {"model":"gpt-5","choices":[{"delta":{"content":"answer"},"finish_reason":"stop"}]}`,
		`data: {"model":"gpt-5","choices":[],"usage":{"prompt_tokens":2,"completion_tokens":3,"total_tokens":5}}`,
		`data: [DONE]`,
	}, "\n\n")
	var chunks []core.StreamChunk
	result, err := codec.DecodeStream(strings.NewReader(stream), func(chunk core.StreamChunk) error {
		chunks = append(chunks, chunk)
		return nil
	})
	if err != nil {
		t.Fatalf("DecodeStream returned error: %v", err)
	}
	if result.Response.Content != "think answer" || result.Response.Usage.TotalTokens != 5 {
		t.Fatalf("stream result = %#v", result.Response)
	}
	if len(chunks) != 3 || chunks[2].Delta || chunks[2].FinishReason != "stop" {
		t.Fatalf("stream chunks = %#v", chunks)
	}
}

func TestCodecRejectsUnsupportedPortableTopK(t *testing.T) {
	codec := mustCodec(t)
	request := core.NewAIRequest("hello", "")
	request.Generation.TopK = core.SetAIParameter(10)
	_, err := codec.BuildDraft(request, "gpt-4.1", false)
	if !errors.Is(err, core.ErrAIRequestFeatureUnsupported) {
		t.Fatalf("BuildDraft error = %v", err)
	}
}

func TestCodecConstructionValidationAndEncodeGuards(t *testing.T) {
	if _, err := openaiwire.NewCodec("  "); err == nil {
		t.Fatal("NewCodec accepted an empty surface version")
	}
	codec := mustCodec(t)
	if _, err := codec.BuildDraft(nil, "gpt-4.1", false); err == nil {
		t.Fatal("BuildDraft accepted a nil request")
	}
	if _, err := codec.BuildDraft(core.NewAIRequest("hello", ""), "  ", false); err == nil {
		t.Fatal("BuildDraft accepted an empty resolved model")
	}
	if _, err := codec.Encode(nil); err == nil {
		t.Fatal("Encode accepted a nil draft")
	}

	otherCodec, err := openaiwire.NewCodec("other-chat-completions-v1")
	if err != nil {
		t.Fatalf("NewCodec returned error: %v", err)
	}
	draft, err := codec.BuildDraft(core.NewAIRequest("hello", ""), "gpt-4.1", false)
	if err != nil {
		t.Fatalf("BuildDraft returned error: %v", err)
	}
	if _, err := otherCodec.Encode(draft); err == nil || !strings.Contains(err.Error(), "different codec surface") {
		t.Fatalf("wrong-surface Encode error = %v", err)
	}
}

func TestCodecDraftValidationRejectsTokenConflictsAndInvalidBudgets(t *testing.T) {
	codec := mustCodec(t)

	conflicting, err := codec.BuildDraft(core.NewAIRequestFromLegacy("hello", "", &core.AIOptions{
		MaxTokens: 100,
	}), "gpt-4.1", false)
	if err != nil {
		t.Fatalf("BuildDraft returned error: %v", err)
	}
	if err := conflicting.Set("/max_completion_tokens", 100); err != nil {
		t.Fatalf("set conflicting token budget: %v", err)
	}
	if err := conflicting.Validate(); err == nil || !strings.Contains(err.Error(), "cannot both be set") {
		t.Fatalf("conflicting token validation error = %v", err)
	}

	invalid, err := codec.BuildDraft(core.NewAIRequest("hello", ""), "gpt-4.1", false)
	if err != nil {
		t.Fatalf("BuildDraft returned error: %v", err)
	}
	if err := invalid.Set("/max_tokens", 0); err != nil {
		t.Fatalf("set invalid token budget: %v", err)
	}
	if err := invalid.Validate(); err == nil || !strings.Contains(err.Error(), "positive integer") {
		t.Fatalf("invalid token validation error = %v", err)
	}
}

func TestCodecDecodeRejectsInvalidInputs(t *testing.T) {
	codec := mustCodec(t)
	if _, err := codec.Decode(nil); err == nil {
		t.Fatal("Decode accepted a nil reader")
	}

	tests := []struct {
		name string
		body string
	}{
		{name: "malformed JSON", body: `{invalid`},
		{name: "empty choices", body: `{"choices":[]}`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := codec.Decode(strings.NewReader(test.body)); err == nil {
				t.Fatalf("Decode(%q) returned nil error", test.body)
			}
		})
	}
}

func TestCodecDecodeStreamInputStopAndPartialReadContracts(t *testing.T) {
	codec := mustCodec(t)
	callback := func(core.StreamChunk) error { return nil }
	if _, err := codec.DecodeStream(nil, callback); err == nil {
		t.Fatal("DecodeStream accepted a nil reader")
	}
	if _, err := codec.DecodeStream(strings.NewReader(""), nil); err == nil {
		t.Fatal("DecodeStream accepted a nil callback")
	}

	t.Run("callback stops after current chunk", func(t *testing.T) {
		stream := strings.Join([]string{
			`data: {"model":"gpt-4.1","choices":[{"delta":{"content":"first"}}]}`,
			`data: {"model":"gpt-4.1","choices":[{"delta":{"content":"second"}}]}`,
			`data: [DONE]`,
		}, "\n\n")
		calls := 0
		result, err := codec.DecodeStream(strings.NewReader(stream), func(core.StreamChunk) error {
			calls++
			return errors.New("stop")
		})
		if err != nil {
			t.Fatalf("DecodeStream returned error after callback stop: %v", err)
		}
		if calls != 1 || result.Response.Content != "first" {
			t.Fatalf("callback-stop result=%#v calls=%d", result.Response, calls)
		}
	})

	t.Run("read failure after content returns partial sentinel", func(t *testing.T) {
		reader := &failAfterDataReader{
			data: strings.NewReader(`data: {"model":"gpt-4.1","choices":[{"delta":{"content":"partial"}}]}` + "\n\n"),
			err:  errors.New("stream read failed"),
		}
		result, err := codec.DecodeStream(reader, callback)
		if !errors.Is(err, core.ErrStreamPartiallyCompleted) {
			t.Fatalf("DecodeStream error = %v, want partial sentinel", err)
		}
		if result == nil || result.Response.Content != "partial" {
			t.Fatalf("partial stream result = %#v", result)
		}
	})

	t.Run("read failure before content preserves cause", func(t *testing.T) {
		wantErr := errors.New("stream read failed")
		reader := &failAfterDataReader{data: strings.NewReader(""), err: wantErr}
		result, err := codec.DecodeStream(reader, callback)
		if result != nil || !errors.Is(err, wantErr) {
			t.Fatalf("DecodeStream result=%#v error=%v", result, err)
		}
	})
}

func mustCodec(t *testing.T) openaiwire.Codec {
	t.Helper()
	codec, err := openaiwire.NewCodec(openaiwire.DefaultSurfaceVersion)
	if err != nil {
		t.Fatalf("NewCodec returned error: %v", err)
	}
	return codec
}

func cloneBody(t *testing.T, body map[string]interface{}) map[string]interface{} {
	t.Helper()
	encoded, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal body: %v", err)
	}
	var clone map[string]interface{}
	if err := json.Unmarshal(encoded, &clone); err != nil {
		t.Fatalf("unmarshal body: %v", err)
	}
	return clone
}

type failAfterDataReader struct {
	data   *strings.Reader
	err    error
	failed bool
}

func (reader *failAfterDataReader) Read(destination []byte) (int, error) {
	if reader.data.Len() > 0 {
		return reader.data.Read(destination)
	}
	if !reader.failed {
		reader.failed = true
		return 0, reader.err
	}
	return 0, io.EOF
}
