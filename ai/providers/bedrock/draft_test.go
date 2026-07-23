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
	"github.com/truvaagents/truva-g3/telemetry"
)

const legacyBedrockTestModel = "anthropic.claude-3-haiku-20240307-v1:0"

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
	draft, err := NewDraft(legacyBedrockTestModel, request)
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
	if aws.ToString(input.ModelId) != legacyBedrockTestModel || len(input.Messages) != 1 {
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

func TestFactoryCreatesRequestClientWithSDKResolverAndRejectsHTTPIntegrations(t *testing.T) {
	config := &ai.AIConfig{
		Model: "model",
		Extra: map[string]interface{}{
			"region":                "us-test-1",
			"aws_access_key_id":     "access",
			"aws_secret_access_key": "secret",
		},
	}
	factory := &Factory{}
	resolver := &bedrockTestResolver{endpoint: ai.ResolvedEndpoint{
		Deployment: "wire-model", RouteIdentity: "factory-route-v1",
	}}
	client, err := factory.CreateRequestClient(config, ai.ProviderIntegrationConfig{
		CompatibilityMode: requestpolicy.CompatibilityCompatible,
		EndpointResolver:  resolver,
		RequestRules: []core.AIProviderPatch{{
			Name: "rule", Version: "1", Selector: core.AIProviderSelector{AllProviders: true},
			Set: map[string]interface{}{"/inference_config/top_p": 0.4},
		}},
	})
	if err != nil {
		t.Fatalf("CreateRequestClient returned error: %v", err)
	}
	bedrockClient, ok := client.(*Client)
	if !ok {
		t.Fatalf("client type = %T", client)
	}
	if bedrockClient.endpointResolver != resolver {
		t.Fatalf("endpoint resolver = %T, want configured resolver", bedrockClient.endpointResolver)
	}
	if bedrockClient.requestTimeout != defaultBedrockRequestTimeout {
		t.Fatalf("request timeout = %s, want %s", bedrockClient.requestTimeout, defaultBedrockRequestTimeout)
	}
	_, err = factory.CreateRequestClient(config, ai.ProviderIntegrationConfig{
		CompatibilityMode: requestpolicy.CompatibilityCompatible,
		HTTPClient:        &http.Client{},
	})
	if !errors.Is(err, core.ErrAIRequestFeatureUnsupported) {
		t.Fatalf("HTTP integration error = %v", err)
	}
	_, err = factory.CreateRequestClient(config, ai.ProviderIntegrationConfig{
		CompatibilityMode: requestpolicy.CompatibilityCompatible,
		CredentialSource:  bedrockTestCredentialSource{},
	})
	if !errors.Is(err, core.ErrAIRequestFeatureUnsupported) {
		t.Fatalf("credential integration error = %v", err)
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

func TestValidateImplicitBedrockDefault(t *testing.T) {
	tests := []struct {
		name                string
		region              string
		model               string
		hasEndpointResolver bool
		wantError           bool
	}{
		{name: "default supported region", region: "us-east-1"},
		{name: "unsupported implicit region", region: "us-west-2", wantError: true},
		{name: "explicit model", region: "us-west-2", model: "global.anthropic.claude-sonnet-5-v1:0"},
		{name: "endpoint resolver", region: "us-west-2", hasEndpointResolver: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := validateImplicitBedrockDefault(test.region, test.model, test.hasEndpointResolver)
			if test.wantError {
				if err == nil {
					t.Fatal("expected implicit-default region error")
				}
				for _, want := range []string{
					ModelClaudeSonnet5,
					test.region,
					"ai.WithModel",
					"ai.WithEndpointResolver",
				} {
					if !strings.Contains(err.Error(), want) {
						t.Fatalf("error %q does not contain %q", err, want)
					}
				}
				return
			}
			if err != nil {
				t.Fatalf("validateImplicitBedrockDefault returned error: %v", err)
			}
		})
	}
}

type fakeRuntimeClient struct {
	converseInput    *bedrockruntime.ConverseInput
	converseOut      *bedrockruntime.ConverseOutput
	converseErr      error
	converseAttempts int
	streamInput      *bedrockruntime.ConverseStreamInput
	stream           converseEventStream
	streamErr        error
	streamAttempts   int
	onConverseStream func()
	invokeInput      *bedrockruntime.InvokeModelInput
	invokeOut        *bedrockruntime.InvokeModelOutput
	invokeErr        error
	invokeNil        bool
	invokeAttempts   int
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
	options ...func(*bedrockruntime.Options),
) (*bedrockruntime.ConverseOutput, error) {
	client.converseInput = input
	client.converseAttempts = retryMaxAttempts(options)
	if client.converseErr != nil {
		return nil, client.converseErr
	}
	if client.converseOut != nil {
		return client.converseOut, nil
	}
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

type bedrockObservationLogger struct {
	component string
	fields    []map[string]interface{}
}

func (logger *bedrockObservationLogger) WithComponent(component string) core.Logger {
	logger.component = component
	return logger
}
func (*bedrockObservationLogger) Debug(string, map[string]interface{}) {}
func (*bedrockObservationLogger) Info(string, map[string]interface{})  {}
func (*bedrockObservationLogger) Warn(string, map[string]interface{})  {}
func (*bedrockObservationLogger) Error(string, map[string]interface{}) {}
func (*bedrockObservationLogger) DebugWithContext(context.Context, string, map[string]interface{}) {
}
func (logger *bedrockObservationLogger) InfoWithContext(_ context.Context, _ string, fields map[string]interface{}) {
	logger.fields = append(logger.fields, fields)
}
func (*bedrockObservationLogger) WarnWithContext(context.Context, string, map[string]interface{}) {
}
func (logger *bedrockObservationLogger) ErrorWithContext(_ context.Context, _ string, fields map[string]interface{}) {
	logger.fields = append(logger.fields, fields)
}

type bedrockObservationTelemetry struct {
	names   []string
	parents []string
	spans   []*bedrockObservationSpan
}

type bedrockObservationSpanContextKey struct{}

func (tracing *bedrockObservationTelemetry) StartSpan(ctx context.Context, name string) (context.Context, core.Span) {
	span := &bedrockObservationSpan{attributes: map[string]interface{}{}}
	tracing.names = append(tracing.names, name)
	parent, _ := ctx.Value(bedrockObservationSpanContextKey{}).(string)
	tracing.parents = append(tracing.parents, parent)
	tracing.spans = append(tracing.spans, span)
	return context.WithValue(ctx, bedrockObservationSpanContextKey{}, name), span
}
func (*bedrockObservationTelemetry) RecordMetric(string, float64, map[string]string) {}

type bedrockObservationSpan struct {
	attributes map[string]interface{}
	errors     []error
	ended      int
}

func (span *bedrockObservationSpan) End() { span.ended++ }
func (span *bedrockObservationSpan) SetAttribute(key string, value interface{}) {
	span.attributes[key] = value
}
func (span *bedrockObservationSpan) RecordError(err error) { span.errors = append(span.errors, err) }

func TestBedrockObservationContract(t *testing.T) {
	const (
		requestID      = "bedrock-observation-request"
		promptSecret   = "bedrock-observation-prompt-secret"
		responseSecret = "bedrock-observation-response-secret"
		providerSecret = "bedrock-observation-provider-secret"
	)

	t.Run("success excludes request and response content", func(t *testing.T) {
		runtime := &fakeRuntimeClient{converseOut: &bedrockruntime.ConverseOutput{
			Output: &types.ConverseOutputMemberMessage{Value: types.Message{
				Role: types.ConversationRoleAssistant,
				Content: []types.ContentBlock{
					&types.ContentBlockMemberText{Value: responseSecret},
				},
			}},
			Usage: &types.TokenUsage{InputTokens: aws.Int32(1), OutputTokens: aws.Int32(1), TotalTokens: aws.Int32(2)},
		}}
		logger := &bedrockObservationLogger{}
		tracing := &bedrockObservationTelemetry{}
		client := newClientWithRuntime(runtime, "us-test-1", logger)
		client.SetLogger(logger)
		client.SetTelemetry(tracing)
		client.DefaultModel = "semantic-bedrock-model"
		ctx := telemetry.WithBaggage(context.Background(), "request_id", requestID)

		result, err := client.Generate(ctx, core.NewAIRequest(promptSecret, "observation"))
		if err != nil {
			t.Fatalf("Generate returned error: %v", err)
		}
		if result == nil || result.Response == nil || result.Response.Content != responseSecret {
			t.Fatalf("result = %#v", result)
		}
		if logger.component != "framework/ai" {
			t.Fatalf("logger component = %q", logger.component)
		}
		if len(tracing.names) != 1 || tracing.names[0] != "ai.generate_response" {
			t.Fatalf("span hierarchy = %#v", tracing.names)
		}

		observed := fmt.Sprint(logger.fields, tracing.spans[0].attributes)
		for _, fields := range logger.fields {
			if fields["operation"] == "" || fields["request_id"] != requestID {
				t.Errorf("log fields = %#v", fields)
			}
		}
		for _, forbidden := range []string{promptSecret, responseSecret} {
			if strings.Contains(observed, forbidden) {
				t.Fatalf("observations leaked %q: %s", forbidden, observed)
			}
		}
	})

	t.Run("error preserves identity and sanitizes observations", func(t *testing.T) {
		wantErr := errors.New(providerSecret)
		runtime := &fakeRuntimeClient{converseErr: wantErr}
		logger := &bedrockObservationLogger{}
		tracing := &bedrockObservationTelemetry{}
		client := newClientWithRuntime(runtime, "us-test-1", logger)
		client.SetLogger(logger)
		client.SetTelemetry(tracing)
		client.DefaultModel = "semantic-bedrock-model"

		_, err := client.Generate(context.Background(), core.NewAIRequest("prompt", "observation"))
		if !errors.Is(err, wantErr) {
			t.Fatalf("Generate error = %v, want preserved identity", err)
		}
		if len(tracing.spans) != 1 || len(tracing.spans[0].errors) != 1 {
			t.Fatalf("error spans = %#v", tracing.spans)
		}
		if tracing.spans[0].errors[0].Error() != "AI provider request failed: transport" {
			t.Fatalf("span error = %v", tracing.spans[0].errors[0])
		}
		observed := fmt.Sprint(logger.fields, tracing.spans[0].attributes, tracing.spans[0].errors[0])
		if strings.Contains(observed, providerSecret) {
			t.Fatalf("observations leaked provider error: %s", observed)
		}
	})

	t.Run("stream cancellation records a sanitized span error", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		stream := newFakeEventStream(&types.ConverseStreamOutputMemberMetadata{
			Value: types.ConverseStreamMetadataEvent{},
		})
		runtime := &fakeRuntimeClient{stream: stream, onConverseStream: cancel}
		logger := &bedrockObservationLogger{}
		tracing := &bedrockObservationTelemetry{}
		client := newClientWithRuntime(runtime, "us-test-1", logger)
		client.SetLogger(logger)
		client.SetTelemetry(tracing)

		_, err := client.Stream(ctx, core.NewAIRequest("prompt", "observation"), func(core.StreamChunk) error { return nil })
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Stream error = %v, want context cancellation", err)
		}
		if len(tracing.spans) != 1 || len(tracing.spans[0].errors) != 1 || tracing.spans[0].ended != 1 {
			t.Fatalf("stream cancellation spans = %#v", tracing.spans)
		}
		if tracing.spans[0].errors[0].Error() != "AI provider request failed: cancelled" {
			t.Fatalf("stream cancellation span error = %v", tracing.spans[0].errors[0])
		}
		observed := fmt.Sprint(logger.fields, tracing.spans[0].attributes, tracing.spans[0].errors)
		if !strings.Contains(observed, "error_type:cancelled") || strings.Contains(observed, context.Canceled.Error()) {
			t.Fatalf("stream cancellation observations = %s", observed)
		}
	})

	t.Run("event stream error preserves identity and sanitizes observations", func(t *testing.T) {
		const streamSecret = "bedrock-event-stream-provider-secret"
		wantErr := errors.New(streamSecret)
		stream := newFakeEventStream()
		stream.err = wantErr
		runtime := &fakeRuntimeClient{stream: stream}
		logger := &bedrockObservationLogger{}
		tracing := &bedrockObservationTelemetry{}
		client := newClientWithRuntime(runtime, "us-test-1", logger)
		client.SetLogger(logger)
		client.SetTelemetry(tracing)

		_, err := client.Stream(context.Background(), core.NewAIRequest("prompt", "observation"), func(core.StreamChunk) error { return nil })
		if !errors.Is(err, wantErr) {
			t.Fatalf("Stream error = %v, want preserved event-stream error", err)
		}
		if len(tracing.spans) != 1 || len(tracing.spans[0].errors) != 1 || tracing.spans[0].ended != 1 {
			t.Fatalf("event stream error spans = %#v", tracing.spans)
		}
		if tracing.spans[0].errors[0].Error() != "AI provider request failed: transport" {
			t.Fatalf("event stream span error = %v", tracing.spans[0].errors[0])
		}
		observed := fmt.Sprint(logger.fields, tracing.spans[0].attributes, tracing.spans[0].errors)
		if strings.Contains(observed, streamSecret) {
			t.Fatalf("event stream observations leaked provider error: %s", observed)
		}
	})

	t.Run("embedding decode failure has nested spans and sanitized observations", func(t *testing.T) {
		const (
			requestID    = "bedrock-embedding-request"
			responseBody = "bedrock-embedding-response-secret"
		)
		runtime := &fakeRuntimeClient{invokeOut: &bedrockruntime.InvokeModelOutput{Body: []byte(responseBody)}}
		logger := &bedrockObservationLogger{}
		tracing := &bedrockObservationTelemetry{}
		client := newClientWithRuntime(runtime, "us-test-1", logger)
		client.SetLogger(logger)
		client.SetTelemetry(tracing)
		ctx := telemetry.WithBaggage(context.Background(), "request_id", requestID)

		_, err := client.GetEmbeddings(ctx, "embedding input")
		if err == nil || !strings.Contains(err.Error(), "parse Bedrock embed response") {
			t.Fatalf("GetEmbeddings error = %v", err)
		}
		if !reflect.DeepEqual(tracing.names, []string{"ai.get_embeddings", "ai.invoke_model"}) ||
			!reflect.DeepEqual(tracing.parents, []string{"", "ai.get_embeddings"}) {
			t.Fatalf("embedding span hierarchy names=%#v parents=%#v", tracing.names, tracing.parents)
		}
		if len(tracing.spans[0].errors) != 1 || tracing.spans[0].errors[0].Error() != "AI provider request failed: decode" {
			t.Fatalf("embedding span error = %#v", tracing.spans[0].errors)
		}
		if len(tracing.spans[1].errors) != 0 {
			t.Fatalf("successful invoke span errors = %#v", tracing.spans[1].errors)
		}
		for index, span := range tracing.spans {
			if span.ended != 1 {
				t.Errorf("embedding span %d ended %d times", index, span.ended)
			}
		}
		if tracing.spans[0].attributes["request_id"] != requestID ||
			tracing.spans[0].attributes["ai.model"] != titanEmbeddingSemanticModel ||
			tracing.spans[0].attributes["ai.text_length"] != len("embedding input") ||
			tracing.spans[0].attributes["ai.input_length"] != nil {
			t.Fatalf("embedding span attributes = %#v", tracing.spans[0].attributes)
		}
		observed := fmt.Sprint(logger.fields, tracing.spans[0].attributes, tracing.spans[0].errors)
		if !strings.Contains(observed, "error_type:decode") || strings.Contains(observed, responseBody) {
			t.Fatalf("embedding decode observations = %s", observed)
		}
	})

	t.Run("embedding invocation error marks both owning spans", func(t *testing.T) {
		const invokeSecret = "bedrock-embedding-invoke-secret"
		wantErr := errors.New(invokeSecret)
		runtime := &fakeRuntimeClient{invokeErr: wantErr}
		logger := &bedrockObservationLogger{}
		tracing := &bedrockObservationTelemetry{}
		client := newClientWithRuntime(runtime, "us-test-1", logger)
		client.SetLogger(logger)
		client.SetTelemetry(tracing)

		_, err := client.GetEmbeddings(context.Background(), "embedding input")
		if !errors.Is(err, wantErr) {
			t.Fatalf("GetEmbeddings error = %v, want preserved invocation error", err)
		}
		if !reflect.DeepEqual(tracing.names, []string{"ai.get_embeddings", "ai.invoke_model"}) {
			t.Fatalf("embedding invocation spans = %#v", tracing.names)
		}
		for index, span := range tracing.spans {
			if len(span.errors) != 1 || span.errors[0].Error() != "AI provider request failed: transport" || span.ended != 1 {
				t.Errorf("embedding invocation span %d = %#v", index, span)
			}
		}
		observed := fmt.Sprint(logger.fields, tracing.spans[0].attributes, tracing.spans[0].errors, tracing.spans[1].errors)
		if strings.Contains(observed, invokeSecret) {
			t.Fatalf("embedding invocation observations leaked provider error: %s", observed)
		}
	})

	t.Run("nil invocation output stays decode on both embedding spans", func(t *testing.T) {
		runtime := &fakeRuntimeClient{invokeNil: true}
		logger := &bedrockObservationLogger{}
		tracing := &bedrockObservationTelemetry{}
		client := newClientWithRuntime(runtime, "us-test-1", logger)
		client.SetLogger(logger)
		client.SetTelemetry(tracing)

		_, err := client.GetEmbeddings(context.Background(), "embedding input")
		if !errors.Is(err, errInvokeModelOutputNil) {
			t.Fatalf("GetEmbeddings error = %v, want nil-output sentinel", err)
		}
		if !reflect.DeepEqual(tracing.names, []string{"ai.get_embeddings", "ai.invoke_model"}) {
			t.Fatalf("nil-output embedding spans = %#v", tracing.names)
		}
		for index, span := range tracing.spans {
			if len(span.errors) != 1 || span.errors[0].Error() != "AI provider request failed: decode" ||
				span.attributes["ai.error_type"] != "decode" || span.ended != 1 {
				t.Errorf("nil-output embedding span %d = %#v", index, span)
			}
		}
		observed := fmt.Sprint(logger.fields, tracing.spans[0].attributes, tracing.spans[0].errors, tracing.spans[1].errors)
		if strings.Contains(observed, errInvokeModelOutputNil.Error()) {
			t.Fatalf("nil-output observations leaked raw diagnostic: %s", observed)
		}
	})
}

func (client *fakeRuntimeClient) ConverseStream(
	_ context.Context,
	input *bedrockruntime.ConverseStreamInput,
	options ...func(*bedrockruntime.Options),
) (converseEventStream, error) {
	client.streamInput = input
	client.streamAttempts = retryMaxAttempts(options)
	if client.onConverseStream != nil {
		client.onConverseStream()
	}
	if client.streamErr != nil {
		return nil, client.streamErr
	}
	if client.stream == nil {
		return nil, errors.New("not implemented")
	}
	return client.stream, nil
}

func (client *fakeRuntimeClient) InvokeModel(
	_ context.Context,
	input *bedrockruntime.InvokeModelInput,
	options ...func(*bedrockruntime.Options),
) (*bedrockruntime.InvokeModelOutput, error) {
	client.invokeInput = input
	client.invokeAttempts = retryMaxAttempts(options)
	if client.invokeErr != nil {
		return nil, client.invokeErr
	}
	if client.invokeNil {
		return nil, nil
	}
	if client.invokeOut != nil {
		return client.invokeOut, nil
	}
	return nil, errors.New("not implemented")
}

func retryMaxAttempts(options []func(*bedrockruntime.Options)) int {
	resolved := &bedrockruntime.Options{}
	for _, option := range options {
		option(resolved)
	}
	return resolved.RetryMaxAttempts
}
