//go:build bedrock
// +build bedrock

package bedrock

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"reflect"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime"
	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime/types"
	"github.com/truvaagents/truva-g3/ai"
	"github.com/truvaagents/truva-g3/ai/requestpolicy"
	"github.com/truvaagents/truva-g3/core"
)

func TestDraftPortableIntentPolicyAndSDKTranslation(t *testing.T) {
	extra := map[string]interface{}{
		"top_k":  20,
		"vendor": map[string]interface{}{"mode": "safe"},
	}
	request := core.NewAIRequestFromLegacy("secret-prompt", "bedrock-draft", &core.AIOptions{
		Model:        "alias",
		Temperature:  0.7,
		MaxTokens:    200,
		SystemPrompt: "legacy system",
		Extra:        extra,
	})
	request.Generation.Temperature = core.SetAIParameter(float32(0))
	request.Generation.TopP = core.SetAIParameter(float32(0.8))
	request.Generation.SystemPrompt = core.OmitAIParameter[string]()
	request.Generation.TopK = core.OmitAIParameter[int]()
	draft, err := NewDraft(ModelClaude3Sonnet, request)
	if err != nil {
		t.Fatalf("NewDraft returned error: %v", err)
	}
	engine, err := requestpolicy.NewEngine(requestpolicy.Config{
		Mode: requestpolicy.CompatibilityCompatible,
		AppRules: []core.AIProviderPatch{{
			Name:    "bedrock-controls",
			Version: "1",
			Selector: core.AIProviderSelector{
				Provider: "bedrock",
				Surface:  "converse",
			},
			Set: map[string]interface{}{
				"/inference_config/stop_sequences":             []string{"END"},
				"/additional_model_request_fields/vendor/mode": "strict",
			},
		}},
	})
	if err != nil {
		t.Fatalf("NewEngine returned error: %v", err)
	}
	report, err := engine.Apply(t.Context(), draft, nil)
	if err != nil {
		t.Fatalf("Apply returned error: %v", err)
	}
	if report == nil || !report.Stable || report.Surface != "converse" || report.Purpose != "bedrock-draft" {
		t.Fatalf("report = %#v", report)
	}
	if text := fmt.Sprintf("%#v", report); strings.Contains(text, "secret-prompt") || strings.Contains(text, "legacy system") {
		t.Fatalf("report contains request content: %s", text)
	}
	input, err := draft.SDKInput()
	if err != nil {
		t.Fatalf("SDKInput returned error: %v", err)
	}
	if aws.ToString(input.ModelId) != ModelClaude3Sonnet || len(input.Messages) != 1 {
		t.Fatalf("SDK input structural fields = %#v", input)
	}
	if input.System != nil {
		t.Fatalf("system = %#v, want omitted", input.System)
	}
	if got := aws.ToFloat32(input.InferenceConfig.Temperature); got != 0 {
		t.Fatalf("temperature = %v", got)
	}
	if got := aws.ToFloat32(input.InferenceConfig.TopP); got != 0.8 {
		t.Fatalf("top_p = %v", got)
	}
	if !reflect.DeepEqual(input.InferenceConfig.StopSequences, []string{"END"}) {
		t.Fatalf("stop sequences = %#v", input.InferenceConfig.StopSequences)
	}
	encodedAdditional, err := input.AdditionalModelRequestFields.MarshalSmithyDocument()
	if err != nil {
		t.Fatalf("marshal additional fields: %v", err)
	}
	var additional map[string]interface{}
	if err := json.Unmarshal(encodedAdditional, &additional); err != nil {
		t.Fatalf("decode additional fields: %v", err)
	}
	if _, exists := additional["top_k"]; exists {
		t.Fatal("portable top_k omit did not remove the legacy additional field")
	}
	if got := additional["vendor"].(map[string]interface{})["mode"]; got != "strict" {
		t.Fatalf("vendor mode = %#v", got)
	}

	extra["vendor"].(map[string]interface{})["mode"] = "mutated"
	secondInput, err := draft.SDKInput()
	if err != nil {
		t.Fatalf("second SDKInput returned error: %v", err)
	}
	secondAdditional, _ := secondInput.AdditionalModelRequestFields.MarshalSmithyDocument()
	if strings.Contains(string(secondAdditional), "mutated") {
		t.Fatalf("draft changed after caller mutation: %s", secondAdditional)
	}
}

func TestDraftRejectsHTTPHeadersAndProtectedSDKFields(t *testing.T) {
	request := core.NewAIRequestFromLegacy("hello", "", &core.AIOptions{Model: "model", MaxTokens: 100})
	draft, err := NewDraft("model", request)
	if err != nil {
		t.Fatalf("NewDraft returned error: %v", err)
	}
	for name, patch := range map[string]core.AIProviderPatch{
		"header": {
			Name: "header", Version: "1",
			Selector:   core.AIProviderSelector{AllProviders: true},
			SetHeaders: map[string]string{"X-Test": "value"},
		},
		"model": {
			Name: "model", Version: "1",
			Selector: core.AIProviderSelector{AllProviders: true},
			Set:      map[string]interface{}{"/model": "other"},
		},
	} {
		t.Run(name, func(t *testing.T) {
			engine, err := requestpolicy.NewEngine(requestpolicy.Config{
				Mode:     requestpolicy.CompatibilityCompatible,
				AppRules: []core.AIProviderPatch{patch},
			})
			if err != nil {
				t.Fatalf("NewEngine returned error: %v", err)
			}
			_, err = engine.Apply(t.Context(), draft, nil)
			if err == nil {
				t.Fatal("expected policy failure")
			}
		})
	}
}

func TestDraftSyncAndStreamTranslationParity(t *testing.T) {
	request := core.NewAIRequestFromLegacy("hello", "parity", &core.AIOptions{
		Model:       "model",
		Temperature: 0.4,
		MaxTokens:   120,
		Extra:       map[string]interface{}{"vendor": true},
	})
	syncDraft, err := NewDraft("model", request)
	if err != nil {
		t.Fatal(err)
	}
	streamDraft, err := NewStreamDraft("model", request)
	if err != nil {
		t.Fatal(err)
	}
	syncInput, err := syncDraft.SDKInput()
	if err != nil {
		t.Fatal(err)
	}
	streamInput, err := streamDraft.SDKStreamInput()
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(syncInput.Messages, streamInput.Messages) ||
		!reflect.DeepEqual(syncInput.System, streamInput.System) ||
		!reflect.DeepEqual(syncInput.InferenceConfig, streamInput.InferenceConfig) {
		t.Fatalf("sync/stream SDK inputs differ:\nsync=%#v\nstream=%#v", syncInput, streamInput)
	}
}

func TestDraftRejectsUnsupportedPortableFeature(t *testing.T) {
	request := core.NewAIRequest("hello", "")
	request.Generation.TopK = core.SetAIParameter(5)
	_, err := NewDraft("model", request)
	if !errors.Is(err, core.ErrAIRequestFeatureUnsupported) {
		t.Fatalf("NewDraft error = %v", err)
	}
}

func TestClientGenerateUsesPolicyTranslatedSDKInput(t *testing.T) {
	runtime := &fakeRuntimeClient{}
	client := newClientWithRuntime(runtime, "us-test-1", &core.NoOpLogger{})
	client.DefaultModel = "model"
	client.DefaultMaxTokens = 100
	engine, err := newRequestPolicyEngineWithIntegration([]core.AIProviderPatch{{
		Name:     "sampling",
		Version:  "1",
		Selector: core.AIProviderSelector{Provider: "bedrock", Surface: "converse"},
		Set:      map[string]interface{}{"/inference_config/top_p": 0.6},
	}}, nil, requestpolicy.CompatibilityCompatible)
	if err != nil {
		t.Fatal(err)
	}
	client.requestPolicy = engine
	request := core.NewAIRequest("hello", "client-test")
	result, err := client.Generate(t.Context(), request)
	if err != nil {
		t.Fatalf("Generate returned error: %v", err)
	}
	if runtime.converseInput == nil || aws.ToFloat32(runtime.converseInput.InferenceConfig.TopP) != 0.6 {
		t.Fatalf("captured Converse input = %#v", runtime.converseInput)
	}
	if result.Response.Content != "ok" || result.Response.Usage.TotalTokens != 5 {
		t.Fatalf("response = %#v", result.Response)
	}
	if result.UsageDetails == nil || result.UsageDetails.CachedInputTokens != 1 ||
		result.UsageDetails.Counters["cache_write_input_tokens"] != 2 {
		t.Fatalf("usage details = %#v", result.UsageDetails)
	}
	if result.RequestReport == nil || !result.RequestReport.Stable || result.RequestReport.Purpose != "client-test" {
		t.Fatalf("report = %#v", result.RequestReport)
	}
}

func TestClientRequestFingerprintUsesPreparedConversePolicy(t *testing.T) {
	client := newClientWithRuntime(&fakeRuntimeClient{}, "us-test-1", &core.NoOpLogger{})
	client.DefaultModel = "model"
	client.DefaultMaxTokens = 100
	request := core.NewAIRequest("secret prompt", "planning")

	fingerprint, stable := client.RequestFingerprint(t.Context(), request)
	if !stable || len(fingerprint) != 64 {
		t.Fatalf("fingerprint = %q, stable = %t", fingerprint, stable)
	}
	request.Purpose = "synthesis"
	changed, stable := client.RequestFingerprint(t.Context(), request)
	if !stable || changed == fingerprint {
		t.Fatalf("purpose did not change fingerprint: first=%q changed=%q stable=%t", fingerprint, changed, stable)
	}
	if fingerprint, stable := client.RequestFingerprint(t.Context(), nil); stable || fingerprint != "" {
		t.Fatalf("nil request fingerprint = %q, %t", fingerprint, stable)
	}
}

func TestClientStreamUsesPolicyTranslatedSDKInput(t *testing.T) {
	stream := newFakeEventStream(
		&types.ConverseStreamOutputMemberContentBlockDelta{Value: types.ContentBlockDeltaEvent{
			ContentBlockIndex: aws.Int32(0),
			Delta:             &types.ContentBlockDeltaMemberText{Value: "hello"},
		}},
		&types.ConverseStreamOutputMemberMetadata{Value: types.ConverseStreamMetadataEvent{
			Usage: &types.TokenUsage{
				InputTokens: aws.Int32(2), OutputTokens: aws.Int32(1), TotalTokens: aws.Int32(3),
				CacheReadInputTokens: aws.Int32(1), CacheWriteInputTokens: aws.Int32(2),
			},
		}},
		&types.ConverseStreamOutputMemberMessageStop{Value: types.MessageStopEvent{
			StopReason: types.StopReasonEndTurn,
		}},
	)
	runtime := &fakeRuntimeClient{stream: stream}
	client := newClientWithRuntime(runtime, "us-test-1", &core.NoOpLogger{})
	client.DefaultModel = "model"
	client.DefaultMaxTokens = 100
	engine, err := newRequestPolicyEngineWithIntegration([]core.AIProviderPatch{{
		Name:     "sampling",
		Version:  "1",
		Selector: core.AIProviderSelector{Provider: "bedrock", Surface: "converse", Operation: "stream"},
		Set:      map[string]interface{}{"/inference_config/top_p": 0.5},
	}}, nil, requestpolicy.CompatibilityCompatible)
	if err != nil {
		t.Fatal(err)
	}
	client.requestPolicy = engine
	var chunks []core.StreamChunk
	result, err := client.Stream(t.Context(), core.NewAIRequest("hello", "stream-test"), func(chunk core.StreamChunk) error {
		chunks = append(chunks, chunk)
		return nil
	})
	if err != nil {
		t.Fatalf("Stream returned error: %v", err)
	}
	if runtime.streamInput == nil || aws.ToFloat32(runtime.streamInput.InferenceConfig.TopP) != 0.5 {
		t.Fatalf("captured ConverseStream input = %#v", runtime.streamInput)
	}
	if result.Response.Content != "hello" || result.Response.Usage.TotalTokens != 3 {
		t.Fatalf("response = %#v", result.Response)
	}
	if result.UsageDetails == nil || result.UsageDetails.CachedInputTokens != 1 ||
		result.UsageDetails.Counters["cache_write_input_tokens"] != 2 {
		t.Fatalf("usage details = %#v", result.UsageDetails)
	}
	if len(chunks) != 2 || !chunks[0].Delta || chunks[1].FinishReason != string(types.StopReasonEndTurn) {
		t.Fatalf("chunks = %#v", chunks)
	}
	if !stream.closed {
		t.Fatal("event stream was not closed")
	}
	if result.RequestReport == nil || result.RequestReport.Operation != "stream" ||
		result.RequestReport.Purpose != "stream-test" {
		t.Fatalf("report = %#v", result.RequestReport)
	}
}

func TestNormalizeUsageDetails(t *testing.T) {
	tests := []struct {
		name            string
		usage           *types.TokenUsage
		wantTotal       int
		wantDetails     bool
		wantCachedInput int64
		wantCacheWrite  int64
	}{
		{name: "nil usage"},
		{
			name: "standard usage",
			usage: &types.TokenUsage{
				InputTokens: aws.Int32(2), OutputTokens: aws.Int32(3), TotalTokens: aws.Int32(5),
			},
			wantTotal: 5,
		},
		{
			name: "cache details",
			usage: &types.TokenUsage{
				InputTokens: aws.Int32(4), OutputTokens: aws.Int32(2), TotalTokens: aws.Int32(6),
				CacheReadInputTokens: aws.Int32(3), CacheWriteInputTokens: aws.Int32(1),
			},
			wantTotal: 6, wantDetails: true, wantCachedInput: 3, wantCacheWrite: 1,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			usage, details := normalizeUsage(test.usage)
			if usage.TotalTokens != test.wantTotal {
				t.Fatalf("total tokens = %d, want %d", usage.TotalTokens, test.wantTotal)
			}
			if (details != nil) != test.wantDetails {
				t.Fatalf("usage details = %#v, want present %t", details, test.wantDetails)
			}
			if details != nil && (details.CachedInputTokens != test.wantCachedInput ||
				details.Counters["cache_write_input_tokens"] != test.wantCacheWrite) {
				t.Fatalf("usage details = %#v", details)
			}
		})
	}
}

func TestFactoryCreatesRequestClientAndRejectsHTTPIntegrations(t *testing.T) {
	config := &ai.AIConfig{
		Model: "model",
		Extra: map[string]interface{}{
			"region":                "us-test-1",
			"aws_access_key_id":     "access",
			"aws_secret_access_key": "secret",
		},
	}
	factory := &Factory{}
	client, err := factory.CreateRequestClient(config, ai.ProviderIntegrationConfig{
		CompatibilityMode: requestpolicy.CompatibilityCompatible,
		RequestRules: []core.AIProviderPatch{{
			Name: "rule", Version: "1", Selector: core.AIProviderSelector{AllProviders: true},
			Set: map[string]interface{}{"/inference_config/top_p": 0.4},
		}},
	})
	if err != nil {
		t.Fatalf("CreateRequestClient returned error: %v", err)
	}
	if _, ok := client.(*Client); !ok {
		t.Fatalf("client type = %T", client)
	}
	_, err = factory.CreateRequestClient(config, ai.ProviderIntegrationConfig{
		CompatibilityMode: requestpolicy.CompatibilityCompatible,
		HTTPClient:        &http.Client{},
	})
	if !errors.Is(err, core.ErrAIRequestFeatureUnsupported) {
		t.Fatalf("HTTP integration error = %v", err)
	}
	configWithHeaders := *config
	configWithHeaders.Headers = map[string]string{"X-Test": "value"}
	_, err = factory.CreateRequestClient(&configWithHeaders, ai.ProviderIntegrationConfig{
		CompatibilityMode: requestpolicy.CompatibilityCompatible,
	})
	if !errors.Is(err, core.ErrAIRequestFeatureUnsupported) {
		t.Fatalf("header integration error = %v", err)
	}
}

func TestBedrockConfigurationValidation(t *testing.T) {
	if _, err := bedrockRegion(map[string]interface{}{"region": 42}); err == nil {
		t.Fatal("expected typed region validation error")
	}
	if _, _, err := optionalString(map[string]interface{}{"key": 42}, "key"); err == nil {
		t.Fatal("expected optional string validation error")
	}
	_, err := loadAWSConfig(t.Context(), "us-test-1", map[string]interface{}{
		"aws_access_key_id": "access",
	})
	if err == nil {
		t.Fatal("expected incomplete static credential validation error")
	}
}

type fakeRuntimeClient struct {
	converseInput *bedrockruntime.ConverseInput
	streamInput   *bedrockruntime.ConverseStreamInput
	stream        converseEventStream
}

type fakeEventStream struct {
	events chan types.ConverseStreamOutput
	err    error
	closed bool
}

func newFakeEventStream(events ...types.ConverseStreamOutput) *fakeEventStream {
	stream := &fakeEventStream{events: make(chan types.ConverseStreamOutput, len(events))}
	for _, event := range events {
		stream.events <- event
	}
	close(stream.events)
	return stream
}

func (stream *fakeEventStream) Events() <-chan types.ConverseStreamOutput { return stream.events }
func (stream *fakeEventStream) Close() error {
	stream.closed = true
	return stream.err
}
func (stream *fakeEventStream) Err() error { return stream.err }

func (client *fakeRuntimeClient) Converse(
	_ context.Context,
	input *bedrockruntime.ConverseInput,
	_ ...func(*bedrockruntime.Options),
) (*bedrockruntime.ConverseOutput, error) {
	client.converseInput = input
	return &bedrockruntime.ConverseOutput{
		Output: &types.ConverseOutputMemberMessage{Value: types.Message{
			Role: types.ConversationRoleAssistant,
			Content: []types.ContentBlock{
				&types.ContentBlockMemberText{Value: "ok"},
			},
		}},
		Usage: &types.TokenUsage{
			InputTokens: aws.Int32(2), OutputTokens: aws.Int32(3),
			TotalTokens: aws.Int32(5), CacheReadInputTokens: aws.Int32(1),
			CacheWriteInputTokens: aws.Int32(2),
		},
		StopReason: types.StopReasonEndTurn,
	}, nil
}

func (client *fakeRuntimeClient) ConverseStream(
	_ context.Context,
	input *bedrockruntime.ConverseStreamInput,
	_ ...func(*bedrockruntime.Options),
) (converseEventStream, error) {
	client.streamInput = input
	if client.stream == nil {
		return nil, errors.New("not implemented")
	}
	return client.stream, nil
}

func (client *fakeRuntimeClient) InvokeModel(
	context.Context,
	*bedrockruntime.InvokeModelInput,
	...func(*bedrockruntime.Options),
) (*bedrockruntime.InvokeModelOutput, error) {
	return nil, errors.New("not implemented")
}
