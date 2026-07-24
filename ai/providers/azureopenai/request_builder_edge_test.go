package azureopenai

import (
	"context"
	"errors"
	"io"
	"net/http"
	"reflect"
	"strings"
	"testing"

	"github.com/truvaagents/truva-g3/ai"
	"github.com/truvaagents/truva-g3/ai/providerkit/openaiwire"
	"github.com/truvaagents/truva-g3/ai/providers"
	"github.com/truvaagents/truva-g3/core"
	"github.com/truvaagents/truva-g3/telemetry"
)

func TestFilterAzureExtraFieldsCapabilityBoundaries(t *testing.T) {
	input := map[string]interface{}{
		"Reasoning":        map[string]interface{}{"effort": "high"},
		"REASONING_EFFORT": "low",
		"Response_Format":  map[string]interface{}{"type": "json_object"},
		"custom":           "preserved",
	}
	original := providers.MergeAnyMaps(nil, input)

	tests := []struct {
		name string
		caps providers.ModelCapabilities
		want map[string]interface{}
		logs int
	}{
		{
			name: "unsupported capabilities are removed case insensitively",
			want: map[string]interface{}{"custom": "preserved"},
			logs: 3,
		},
		{
			name: "supported portable fields remain while reasoning object is removed",
			caps: providers.ModelCapabilities{ReasoningStyle: "openai", SupportsJSONMode: true},
			want: map[string]interface{}{
				"REASONING_EFFORT": "low",
				"Response_Format":  map[string]interface{}{"type": "json_object"},
				"custom":           "preserved",
			},
			logs: 1,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			logger := newAzureObservationLogger()
			ctx := telemetry.WithBaggage(context.Background(), "request_id", "azure-filter-request")
			got := filterAzureExtraFields(ctx, logger, "azureopenai.v1", "gpt-test", test.caps, input)
			if !reflect.DeepEqual(got, test.want) {
				t.Fatalf("filtered extras = %#v, want %#v", got, test.want)
			}
			if !reflect.DeepEqual(input, original) {
				t.Fatalf("input extras mutated: got %#v, want %#v", input, original)
			}
			if len(*logger.entries) != test.logs {
				t.Fatalf("degradation logs = %d, want %d", len(*logger.entries), test.logs)
			}
			for _, entry := range *logger.entries {
				if entry.fields["request_id"] != "azure-filter-request" || entry.fields["status"] != "degraded" {
					t.Fatalf("degradation fields = %#v", entry.fields)
				}
			}
		})
	}

	if got := filterAzureExtraFields(t.Context(), nil, "azureopenai.v1", "gpt-test", providers.ModelCapabilities{}, nil); got != nil {
		t.Fatalf("nil extras = %#v", got)
	}
	if got := filterAzureExtraFields(t.Context(), nil, "azureopenai.v1", "gpt-test", providers.ModelCapabilities{}, map[string]interface{}{
		"reasoning": "remove",
	}); got != nil {
		t.Fatalf("fully filtered extras = %#v", got)
	}
}

func TestAzureHeaderPreparationIsCaseInsensitiveAndIsolated(t *testing.T) {
	defaults := map[string]string{
		"x-trace": "default",
		"X-Only":  "default-only",
	}
	request := map[string]string{
		"X-Trace":   "request",
		"x-request": "request-only",
		"API-KEY":   "must-be-ignored",
	}
	defaultsBefore := providers.MergeStringMaps(nil, defaults)
	requestBefore := providers.MergeStringMaps(nil, request)

	merged := mergeHeaders(defaults, request)
	if got := merged["X-Trace"]; got != "request" {
		t.Fatalf("merged X-Trace = %q", got)
	}
	if got := merged["X-Only"]; got != "default-only" {
		t.Fatalf("merged X-Only = %q", got)
	}
	if got := merged["X-Request"]; got != "request-only" {
		t.Fatalf("merged X-Request = %q", got)
	}

	filtered, conflicts := stripAPIKeyHeader(merged)
	if got := filtered["Api-Key"]; got != "" {
		t.Fatalf("filtered api-key = %q", got)
	}
	if !reflect.DeepEqual(conflicts, []string{"Api-Key"}) {
		t.Fatalf("api-key conflicts = %#v", conflicts)
	}
	if !reflect.DeepEqual(defaults, defaultsBefore) || !reflect.DeepEqual(request, requestBefore) {
		t.Fatalf("header inputs mutated: defaults=%#v request=%#v", defaults, request)
	}
	if got := mergeHeaders(nil, nil); got != nil {
		t.Fatalf("nil header merge = %#v", got)
	}
	if got, conflicts := stripAPIKeyHeader(nil); got != nil || conflicts != nil {
		t.Fatalf("nil api-key filter = %#v, %#v", got, conflicts)
	}
}

func TestAzureDraftProtectsAPIKeyForEveryPolicyOperation(t *testing.T) {
	codec, err := openaiwire.NewProfiledCodec(openaiwire.Config{SurfaceVersion: surfaceVersionV1})
	if err != nil {
		t.Fatal(err)
	}
	draft, err := codec.BuildDraftWithProfile(
		core.NewAIRequest("hello", "policy"),
		openaiwire.RequestProfile{
			SemanticModel: "gpt-4.1", WireModel: "deployment",
			ModelField: openaiwire.ModelFieldRequired, TokenLimit: openaiwire.TokenLimitMaxTokens,
			ReasoningEffort: openaiwire.ReasoningEffortOmitted, Sampling: openaiwire.SamplingOrdinary,
		},
		false,
	)
	if err != nil {
		t.Fatal(err)
	}
	protected := &azureDraft{Draft: draft}
	if err := protected.SetHeader("API-KEY", "policy-secret"); err == nil {
		t.Fatal("SetHeader accepted api-key")
	}
	if err := protected.RemoveHeader("Api-Key"); err == nil {
		t.Fatal("RemoveHeader accepted api-key")
	}
	if err := protected.SetHeader("X-Trace", "allowed"); err != nil {
		t.Fatalf("SetHeader(X-Trace): %v", err)
	}
	if err := protected.RemoveHeader("X-Trace"); err != nil {
		t.Fatalf("RemoveHeader(X-Trace): %v", err)
	}
	if err := draft.SetHeader("api-key", "bypass"); err != nil {
		t.Fatalf("underlying draft setup: %v", err)
	}
	if err := protected.Validate(); err == nil || !strings.Contains(err.Error(), "api-key header invariant") {
		t.Fatalf("Validate error = %v", err)
	}
}

func TestValidateAzurePortableIntentEdgeCases(t *testing.T) {
	tests := []struct {
		name       string
		generation core.AIGenerationOptions
		featureErr bool
	}{
		{
			name: "invalid mode",
			generation: core.AIGenerationOptions{
				Temperature: core.AIParameter[float32]{Mode: core.AIParameterMode(255)},
			},
		},
		{
			name: "top k unsupported",
			generation: core.AIGenerationOptions{
				TopK: core.SetAIParameter(10),
			},
			featureErr: true,
		},
		{
			name: "zero max tokens",
			generation: core.AIGenerationOptions{
				MaxTokens: core.SetAIParameter(0),
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := validatePortableIntent(test.generation)
			if err == nil {
				t.Fatal("invalid intent accepted")
			}
			if test.featureErr && !errors.Is(err, core.ErrAIRequestFeatureUnsupported) {
				t.Fatalf("error = %v", err)
			}
		})
	}
}

func TestAzurePrepareSemanticsReasoningCapabilityBoundary(t *testing.T) {
	logger := newAzureObservationLogger()
	client := mustClient(t, &ai.AIConfig{
		ProviderAlias: "azureopenai.v1", APIKey: "key", Model: "gpt-4.1",
		ReasoningEffort: "high", MaxRetries: 0, Logger: logger,
	}, ai.ProviderIntegrationConfig{EndpointResolver: &testResolver{}})
	*logger.entries = nil
	ctx := telemetry.WithBaggage(context.Background(), "request_id", "azure-reasoning-capability")
	semantics, err := client.prepareSemantics(ctx, core.NewAIRequest("hello", "legacy-reasoning"), false)
	if err != nil {
		t.Fatal(err)
	}
	if semantics.Options.ReasoningEffort != "" {
		t.Fatalf("unsupported default reasoning effort = %q", semantics.Options.ReasoningEffort)
	}
	if len(*logger.entries) != 1 {
		t.Fatalf("degradation entries = %#v", *logger.entries)
	}
	fields := (*logger.entries)[0].fields
	if fields["warning_type"] != "reasoning_effort_stripped" || fields["request_id"] != "azure-reasoning-capability" {
		t.Fatalf("degradation fields = %#v", fields)
	}

	explicit := core.NewAIRequest("hello", "explicit-reasoning")
	explicit.Generation.ReasoningEffort = core.SetAIParameter("high")
	if _, err := client.prepareSemantics(ctx, explicit, false); !errors.Is(err, core.ErrAIRequestFeatureUnsupported) {
		t.Fatalf("explicit unsupported reasoning error = %v", err)
	}

	reasoningClient := mustClient(t, &ai.AIConfig{
		ProviderAlias: "azureopenai.v1", APIKey: "key", Model: "o3",
		ReasoningEffort: "high", MaxRetries: 0,
	}, ai.ProviderIntegrationConfig{EndpointResolver: &testResolver{}})
	reasoningSemantics, err := reasoningClient.prepareSemantics(ctx, core.NewAIRequest("hello", "supported-reasoning"), false)
	if err != nil {
		t.Fatal(err)
	}
	if reasoningSemantics.Options.ReasoningEffort != "high" || !reasoningSemantics.ReasoningModel {
		t.Fatalf("supported reasoning semantics = %#v", reasoningSemantics)
	}
}

func TestAzureSurfaceProfileValidationMatrix(t *testing.T) {
	if _, err := surface(255).surfaceVersion(); err == nil {
		t.Fatal("invalid surface version accepted")
	}
	client := &Client{surface: surface(255)}
	if _, err := client.surfaceContract(resolvedRoute{}); err == nil {
		t.Fatal("invalid surface contract accepted")
	}
	classic := &Client{surface: surfaceClassic}
	if _, err := classic.surfaceContract(resolvedRoute{url: mustURL(t,
		"https://resource.openai.azure.com/openai/deployments/prod/chat/completions?api-version=a&api-version=b",
	)}); err == nil {
		t.Fatal("classic surface accepted multiple api-version values")
	}
	semantics := &requestSemantics{
		SemanticModel: "gpt-4.1", ProviderAlias: "azureopenai.v1",
		Capabilities: providers.ModelCapabilities{SupportsJSONMode: true},
	}
	v1 := &Client{surface: surfaceV1}
	if _, err := v1.requestProfile(semantics, resolvedRoute{}); err == nil {
		t.Fatal("request profile accepted an empty deployment")
	}
	invalid := &Client{surface: surface(255)}
	if _, err := invalid.requestProfile(semantics, resolvedRoute{deployment: "prod"}); err == nil {
		t.Fatal("request profile accepted an invalid surface")
	}
}

func TestAzureLegacyAdaptersAndStreamingCapability(t *testing.T) {
	resolver := &testResolver{resolved: ai.ResolvedEndpoint{
		URL:        mustURL(t, "https://resource.openai.azure.com/openai/v1/chat/completions"),
		Deployment: "prod-chat", RouteIdentity: "legacy-adapter-route-v1",
	}}
	transport := roundTripFunc(func(request *http.Request) (*http.Response, error) {
		if request.Header.Get("Accept") == "text/event-stream" {
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     http.Header{"Content-Type": {"text/event-stream"}},
				Body: io.NopCloser(strings.NewReader(
					"data: {\"model\":\"prod-chat\",\"choices\":[{\"delta\":{\"content\":\"streamed\"}}]}\n\ndata: [DONE]\n",
				)),
				Request: request,
			}, nil
		}
		return successResponse(request, "prod-chat", "generated"), nil
	})
	client := mustClient(t, &ai.AIConfig{
		ProviderAlias: "azureopenai.v1", APIKey: "key", Model: "gpt-4.1", MaxRetries: 0,
	}, ai.ProviderIntegrationConfig{
		EndpointResolver: resolver,
		HTTPClient:       &http.Client{Transport: transport},
	})
	if !client.SupportsStreaming() {
		t.Fatal("SupportsStreaming returned false")
	}
	generated, err := client.GenerateResponse(t.Context(), "hello", &core.AIOptions{Model: "gpt-4.1"})
	if err != nil || generated == nil || generated.Content != "generated" {
		t.Fatalf("GenerateResponse = %#v, %v", generated, err)
	}
	var chunks []string
	streamed, err := client.StreamResponse(t.Context(), "hello", &core.AIOptions{Model: "gpt-4.1"}, func(chunk core.StreamChunk) error {
		chunks = append(chunks, chunk.Content)
		return nil
	})
	if err != nil || streamed == nil || streamed.Content != "streamed" {
		t.Fatalf("StreamResponse = %#v, %v", streamed, err)
	}
	if len(chunks) != 1 || chunks[0] != "streamed" {
		t.Fatalf("stream chunks = %#v", chunks)
	}
}

func TestAzureLegacyAdaptersRetainErrorsAndAvoidTransport(t *testing.T) {
	resolverFailure := errors.New("resolver unavailable")
	resolver := &testResolver{err: resolverFailure}
	transportCalls := 0
	client := mustClient(t, &ai.AIConfig{
		ProviderAlias: "azureopenai.v1", APIKey: "key", Model: "gpt-4.1", MaxRetries: 0,
	}, ai.ProviderIntegrationConfig{
		EndpointResolver: resolver,
		HTTPClient: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			transportCalls++
			return nil, errors.New("unexpected transport")
		})},
	})
	response, err := client.GenerateResponse(t.Context(), "hello", &core.AIOptions{Model: "gpt-4.1"})
	if response != nil || !errors.Is(err, resolverFailure) {
		t.Fatalf("GenerateResponse = %#v, %v", response, err)
	}
	if fingerprint, stable := client.RequestFingerprint(t.Context(), core.NewAIRequest("hello", "fingerprint")); fingerprint != "" || stable {
		t.Fatalf("RequestFingerprint = %q, %t", fingerprint, stable)
	}
	streamed, err := client.StreamResponse(t.Context(), "hello", &core.AIOptions{Model: "gpt-4.1"}, nil)
	if streamed != nil || err == nil || !strings.Contains(err.Error(), "callback is nil") {
		t.Fatalf("StreamResponse = %#v, %v", streamed, err)
	}
	if transportCalls != 0 {
		t.Fatalf("transport calls = %d", transportCalls)
	}
}

func TestAzureStreamProviderErrorRetainsRequestReport(t *testing.T) {
	resolver := &testResolver{resolved: ai.ResolvedEndpoint{
		URL:        mustURL(t, "https://resource.openai.azure.com/openai/v1/chat/completions"),
		Deployment: "prod-chat", RouteIdentity: "stream-error-route-v1",
	}}
	client := mustClient(t, &ai.AIConfig{
		ProviderAlias: "azureopenai.v1", APIKey: "key", Model: "gpt-4.1", MaxRetries: 0,
	}, ai.ProviderIntegrationConfig{
		EndpointResolver: resolver,
		HTTPClient: &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
			return &http.Response{
				StatusCode: http.StatusBadRequest,
				Header:     http.Header{"Content-Type": {"application/json"}},
				Body:       io.NopCloser(strings.NewReader(`{"error":{"message":"bad stream"}}`)),
				Request:    request,
			}, nil
		})},
	})
	result, err := client.Stream(t.Context(), core.NewAIRequest("hello", "stream-error"), func(core.StreamChunk) error { return nil })
	if err == nil || result == nil || result.RequestReport == nil {
		t.Fatalf("Stream result = %#v, error = %v", result, err)
	}
	if result.RequestReport.Operation != "stream" || result.RequestReport.ResolvedModel != "gpt-4.1" {
		t.Fatalf("request report = %#v", result.RequestReport)
	}
}
