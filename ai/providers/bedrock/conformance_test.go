//go:build bedrock
// +build bedrock

package bedrock

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"net/url"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime"
	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime/types"
	smithydocument "github.com/aws/smithy-go/document"
	"github.com/truvaagents/truva-g3/ai"
	"github.com/truvaagents/truva-g3/ai/requestpolicy"
	"github.com/truvaagents/truva-g3/core"
)

type bedrockResolverContextKey struct{}

type bedrockTestResolver struct {
	endpoint ai.ResolvedEndpoint
	err      error
	requests []ai.EndpointRequest
	contexts []context.Context
}

type bedrockTestCredentialSource struct{}

type bedrockFallbackClient struct {
	fingerprint string
	calls       int
	streamCalls int
}

func (client *bedrockFallbackClient) GenerateResponse(
	context.Context,
	string,
	*core.AIOptions,
) (*core.AIResponse, error) {
	return &core.AIResponse{Content: "fallback", Model: "fallback", Provider: "fallback"}, nil
}

func (client *bedrockFallbackClient) Generate(
	_ context.Context,
	request *core.AIRequest,
) (*core.AIResult, error) {
	client.calls++
	return &core.AIResult{
		Response: &core.AIResponse{Content: "fallback", Model: "fallback", Provider: "fallback"},
		RequestReport: &core.AIRequestReport{
			Provider: "fallback", Purpose: request.Purpose,
			ResolvedModel: "fallback", Fingerprint: client.fingerprint, Stable: true,
		},
	}, nil
}

func (client *bedrockFallbackClient) Stream(
	_ context.Context,
	request *core.AIRequest,
	callback core.StreamCallback,
) (*core.AIResult, error) {
	client.streamCalls++
	if err := callback(core.StreamChunk{Content: "fallback", Delta: true}); err != nil {
		return nil, err
	}
	return &core.AIResult{
		Response: &core.AIResponse{Content: "fallback", Model: "fallback", Provider: "fallback"},
		RequestReport: &core.AIRequestReport{
			Provider: "fallback", Purpose: request.Purpose,
			ResolvedModel: "fallback", Fingerprint: client.fingerprint, Stable: true,
		},
	}, nil
}

func (client *bedrockFallbackClient) RequestFingerprint(
	context.Context,
	*core.AIRequest,
) (string, bool) {
	return client.fingerprint, client.fingerprint != ""
}

func (bedrockTestCredentialSource) Credential(context.Context, ai.CredentialRequest) (ai.HeaderCredential, error) {
	return ai.NewHeaderCredential("Authorization", "secret"), nil
}

type bedrockHTTPStatusWrapper struct {
	status int
	err    error
}

func (wrapper *bedrockHTTPStatusWrapper) Error() string       { return wrapper.err.Error() }
func (wrapper *bedrockHTTPStatusWrapper) Unwrap() error       { return wrapper.err }
func (wrapper *bedrockHTTPStatusWrapper) HTTPStatusCode() int { return wrapper.status }

func (resolver *bedrockTestResolver) ResolveEndpoint(
	ctx context.Context,
	request ai.EndpointRequest,
) (ai.ResolvedEndpoint, error) {
	resolver.contexts = append(resolver.contexts, ctx)
	resolver.requests = append(resolver.requests, request)
	if resolver.err != nil {
		return ai.ResolvedEndpoint{}, resolver.err
	}
	return resolver.endpoint, nil
}

func TestBedrockCurrentDirectDefaultDoesNotSelectGlobalProfile(t *testing.T) {
	runtime := &fakeRuntimeClient{}
	client := newClientWithRuntime(runtime, "us-east-1", &core.NoOpLogger{})

	result, err := client.Generate(t.Context(), core.NewAIRequest("hello", "default"))
	if err != nil {
		t.Fatalf("Generate returned error: %v", err)
	}
	if got := aws.ToString(runtime.converseInput.ModelId); got != ModelClaudeSonnet5 {
		t.Fatalf("ModelId = %q, want %q", got, ModelClaudeSonnet5)
	}
	if strings.HasPrefix(aws.ToString(runtime.converseInput.ModelId), "global.") {
		t.Fatalf("default silently selected a global inference profile: %q", aws.ToString(runtime.converseInput.ModelId))
	}
	if runtime.converseInput.InferenceConfig == nil || runtime.converseInput.InferenceConfig.Temperature != nil {
		t.Fatalf("default Sonnet 5 inference config = %#v", runtime.converseInput.InferenceConfig)
	}
	if result.RequestReport == nil || result.RequestReport.ResolvedModel != ModelClaudeSonnet5 {
		t.Fatalf("request report = %#v", result.RequestReport)
	}
	if client.requestTimeout != 60*time.Minute {
		t.Fatalf("request timeout = %s, want 60m", client.requestTimeout)
	}
}

func TestBedrockImplicitDefaultRegionGuardAllowsPerRequestModelSelection(t *testing.T) {
	t.Run("implicit default fails before SDK invocation", func(t *testing.T) {
		runtime := &fakeRuntimeClient{}
		client := newClientWithRuntime(runtime, "us-west-2", &core.NoOpLogger{})

		_, err := client.Generate(t.Context(), core.NewAIRequest("hello", "implicit-default"))
		if err == nil || !strings.Contains(err.Error(), "implicit default model") {
			t.Fatalf("Generate error = %v", err)
		}
		if runtime.converseInput != nil {
			t.Fatalf("runtime was called with %#v", runtime.converseInput)
		}
	})

	t.Run("request model bypasses unused implicit default", func(t *testing.T) {
		const requestModel = "global.anthropic.claude-sonnet-5-v1:0"
		runtime := &fakeRuntimeClient{}
		client := newClientWithRuntime(runtime, "us-west-2", &core.NoOpLogger{})
		request := core.NewAIRequest("hello", "request-model")
		request.Generation.Model = requestModel

		if _, err := client.Generate(t.Context(), request); err != nil {
			t.Fatalf("Generate returned error: %v", err)
		}
		if got := aws.ToString(runtime.converseInput.ModelId); got != requestModel {
			t.Fatalf("ModelId = %q, want %q", got, requestModel)
		}
	})

	t.Run("explicit configured model retains application ownership", func(t *testing.T) {
		runtime := &fakeRuntimeClient{}
		client := newClientWithRuntime(runtime, "us-west-2", &core.NoOpLogger{})
		if err := client.SetDefaultModel(ModelClaudeSonnet5); err != nil {
			t.Fatal(err)
		}

		if _, err := client.Generate(t.Context(), core.NewAIRequest("hello", "configured-model")); err != nil {
			t.Fatalf("Generate returned error: %v", err)
		}
		if got := aws.ToString(runtime.converseInput.ModelId); got != ModelClaudeSonnet5 {
			t.Fatalf("ModelId = %q, want %q", got, ModelClaudeSonnet5)
		}
	})
}

func TestBedrockImplicitRouteGuardPreservesStableFingerprintsAndFailoverCaching(t *testing.T) {
	runtime := &fakeRuntimeClient{}
	logger := &bedrockObservationLogger{}
	tracing := &bedrockObservationTelemetry{}
	client := newClientWithRuntime(runtime, "us-west-2", logger)
	client.SetLogger(logger)
	client.SetTelemetry(tracing)
	request := core.NewAIRequest("hello", "implicit-route")

	entryFingerprint, stable := client.RequestFingerprint(t.Context(), request)
	if !stable || entryFingerprint == "" {
		t.Fatalf("Bedrock fingerprint = %q, stable = %t", entryFingerprint, stable)
	}
	result, err := client.Generate(t.Context(), request)
	if err == nil {
		t.Fatal("expected implicit-route guard error")
	}
	var routeErr *routeResolutionError
	if !errors.As(err, &routeErr) {
		t.Fatalf("Generate error type = %T, want routeResolutionError: %v", err, err)
	}
	if result == nil || result.RequestReport == nil ||
		!result.RequestReport.Stable ||
		result.RequestReport.Fingerprint != entryFingerprint {
		t.Fatalf("guarded result report = %#v", result)
	}
	if runtime.converseInput != nil {
		t.Fatal("route guard invoked the Bedrock runtime")
	}
	routeLogFound := false
	for _, fields := range logger.fields {
		if fields["error_type"] == "route" {
			routeLogFound = true
			break
		}
	}
	if !routeLogFound {
		t.Fatalf("route guard logs = %#v", logger.fields)
	}
	if len(tracing.spans) != 1 ||
		tracing.spans[0].attributes["ai.error_type"] != "route" ||
		len(tracing.spans[0].errors) != 1 {
		t.Fatalf("route guard span = %#v", tracing.spans)
	}
	streamResult, err := client.Stream(
		t.Context(),
		request,
		func(core.StreamChunk) error { return nil },
	)
	if err == nil {
		t.Fatal("expected streaming implicit-route guard error")
	}
	if !errors.As(err, &routeErr) {
		t.Fatalf("Stream error type = %T, want routeResolutionError: %v", err, err)
	}
	if streamResult == nil || streamResult.RequestReport == nil ||
		!streamResult.RequestReport.Stable ||
		streamResult.RequestReport.Fingerprint == "" {
		t.Fatalf("streaming guarded result report = %#v", streamResult)
	}
	if runtime.streamInput != nil {
		t.Fatal("streaming route guard invoked the Bedrock runtime")
	}

	fallback := &bedrockFallbackClient{fingerprint: "fallback-policy-v1"}
	chain, err := ai.NewChain(
		ai.ClientEntry("bedrock", client),
		ai.ClientEntry("fallback", fallback),
	)
	if err != nil {
		t.Fatal(err)
	}
	chain.SetLogger(logger)
	chain.SetTelemetry(tracing)
	chainFingerprint, stable := chain.RequestFingerprint(t.Context(), request)
	if !stable || chainFingerprint == "" {
		t.Fatalf("chain fingerprint = %q, stable = %t", chainFingerprint, stable)
	}
	result, err = chain.Generate(t.Context(), request)
	if err != nil {
		t.Fatalf("chain Generate returned error: %v", err)
	}
	if fallback.calls != 1 {
		t.Fatalf("fallback calls = %d, want 1", fallback.calls)
	}
	if result.RequestReport == nil ||
		!result.RequestReport.Stable ||
		result.RequestReport.Fingerprint != chainFingerprint {
		t.Fatalf("chain result report = %#v, want fingerprint %q", result.RequestReport, chainFingerprint)
	}
	routeFailoverFound := false
	for _, fields := range logger.fields {
		if fields["operation"] == "ai_chain_provider_failed" {
			if fields["failover_reason"] != "route" {
				t.Fatalf("chain route failover fields = %#v", fields)
			}
			if fields["error_type"] != "route" {
				t.Fatalf("chain route error type = %#v, want route", fields["error_type"])
			}
			routeFailoverFound = true
		}
	}
	if !routeFailoverFound {
		t.Fatalf("chain route failover log missing: %#v", logger.fields)
	}
	routeSpanFound := false
	routeAttemptSpanFound := false
	for index, name := range tracing.names {
		switch name {
		case "ai.chain.generate":
			if got := tracing.spans[index].attributes["ai.chain.failover_reason"]; got != "route" {
				t.Fatalf("chain failover span reason = %#v, want route", got)
			}
			routeSpanFound = true
		case "ai.chain.provider_attempt":
			if tracing.spans[index].attributes["ai.chain.attempt_status"] != "failed" {
				continue
			}
			if got := tracing.spans[index].attributes["ai.error_type"]; got != "route" {
				t.Fatalf("chain attempt error type = %#v, want route", got)
			}
			routeAttemptSpanFound = true
		}
	}
	if !routeSpanFound {
		t.Fatalf("chain generate span missing: names=%#v", tracing.names)
	}
	if !routeAttemptSpanFound {
		t.Fatalf("failed chain attempt span missing: names=%#v", tracing.names)
	}

	result, err = chain.Stream(
		t.Context(),
		request,
		func(core.StreamChunk) error { return nil },
	)
	if err != nil {
		t.Fatalf("chain Stream returned error: %v", err)
	}
	if fallback.streamCalls != 1 {
		t.Fatalf("fallback stream calls = %d, want 1", fallback.streamCalls)
	}
	if result.RequestReport == nil ||
		!result.RequestReport.Stable ||
		result.RequestReport.Fingerprint != chainFingerprint {
		t.Fatalf("chain stream report = %#v, want fingerprint %q", result.RequestReport, chainFingerprint)
	}
	streamRouteFailoverFound := false
	for _, fields := range logger.fields {
		if fields["operation"] == "ai_chain_stream_failover" &&
			fields["failover_reason"] == "route" &&
			fields["error_type"] == "route" {
			streamRouteFailoverFound = true
			break
		}
	}
	if !streamRouteFailoverFound {
		t.Fatalf("chain stream route failover log missing: %#v", logger.fields)
	}
}

func TestBedrockDirectClientCanDeclareExplicitDefaultModel(t *testing.T) {
	runtime := &fakeRuntimeClient{}
	client := newClientWithRuntime(runtime, "us-west-2", &core.NoOpLogger{})
	if err := client.SetDefaultModel(ModelClaudeSonnet5); err != nil {
		t.Fatal(err)
	}
	if !client.defaultModelExplicit || client.DefaultModel != ModelClaudeSonnet5 {
		t.Fatalf("explicit default state = model %q explicit %t", client.DefaultModel, client.defaultModelExplicit)
	}
	if _, err := client.Generate(t.Context(), core.NewAIRequest("hello", "direct-explicit")); err != nil {
		t.Fatalf("Generate returned error: %v", err)
	}
	if aws.ToString(runtime.converseInput.ModelId) != ModelClaudeSonnet5 {
		t.Fatalf("ModelId = %q", aws.ToString(runtime.converseInput.ModelId))
	}
	if err := client.SetDefaultModel(" bad "); err == nil {
		t.Fatal("expected invalid explicit default model error")
	}
	var nilClient *Client
	if err := nilClient.SetDefaultModel("model"); err == nil {
		t.Fatal("expected nil client error")
	}
}

func TestBedrockResolverSeparatesSemanticAndWireModels(t *testing.T) {
	const (
		semanticModel = "semantic-claude-model"
		wireSecret    = "arn:aws:bedrock:us-east-1:123456789012:application-inference-profile/private-profile"
		routeIdentity = "bedrock-tenant-primary-v1"
		contextValue  = "resolver-context"
	)
	runtime := &fakeRuntimeClient{}
	logger := &bedrockObservationLogger{}
	tracing := &bedrockObservationTelemetry{}
	resolver := &bedrockTestResolver{endpoint: ai.ResolvedEndpoint{
		Deployment: wireSecret, RouteIdentity: routeIdentity,
	}}
	client := newClientWithRuntime(runtime, "us-east-1", logger)
	client.DefaultModel = semanticModel
	client.endpointResolver = resolver
	client.SetLogger(logger)
	client.SetTelemetry(tracing)
	engine, err := newRequestPolicyEngineWithIntegration([]core.AIProviderPatch{{
		Name: "semantic-selector", Version: "1",
		Selector: core.AIProviderSelector{
			Provider: "bedrock", Surface: "converse", Model: semanticModel,
		},
		Set: map[string]interface{}{`/inference_config/top_p`: 0.6},
	}}, nil, requestpolicy.CompatibilityCompatible)
	if err != nil {
		t.Fatal(err)
	}
	client.requestPolicy = engine
	ctx := context.WithValue(t.Context(), bedrockResolverContextKey{}, contextValue)

	result, err := client.Generate(ctx, core.NewAIRequest("secret prompt", "routing"))
	if err != nil {
		t.Fatalf("Generate returned error: %v", err)
	}
	if got := aws.ToString(runtime.converseInput.ModelId); got != wireSecret {
		t.Fatalf("wire ModelId = %q, want resolver deployment", got)
	}
	if got := aws.ToFloat32(runtime.converseInput.InferenceConfig.TopP); got != 0.6 {
		t.Fatalf("semantic policy did not apply: top_p = %v", got)
	}
	if result.Response.Model != semanticModel || result.RequestReport.ResolvedModel != semanticModel {
		t.Fatalf("semantic result/report = %#v / %#v", result.Response, result.RequestReport)
	}
	if len(resolver.requests) != 1 || resolver.requests[0].ResolvedModel != semanticModel ||
		resolver.requests[0].Operation != "generate" || resolver.requests[0].Purpose != "routing" ||
		resolver.contexts[0].Value(bedrockResolverContextKey{}) != contextValue {
		t.Fatalf("resolver invocation = %#v / %#v", resolver.requests, resolver.contexts)
	}
	if result.RequestReport == nil || strings.Contains(fmt.Sprintf("%#v", result.RequestReport), wireSecret) {
		t.Fatalf("request report leaked wire deployment: %#v", result.RequestReport)
	}
	observed := fmt.Sprint(logger.fields, tracing.spans[0].attributes, tracing.spans[0].errors)
	if strings.Contains(observed, wireSecret) || !strings.Contains(observed, routeIdentity) {
		t.Fatalf("route observations = %s", observed)
	}
}

func TestBedrockFingerprintBindsRouteIdentityNotWireDeployment(t *testing.T) {
	resolver := &bedrockTestResolver{endpoint: ai.ResolvedEndpoint{
		Deployment: "private-wire-one", RouteIdentity: "stable-route-v1",
	}}
	client := newClientWithRuntime(&fakeRuntimeClient{}, "us-east-1", &core.NoOpLogger{})
	client.DefaultModel = "semantic-model"
	client.endpointResolver = resolver
	request := core.NewAIRequest("secret", "fingerprint")

	first, stable := client.RequestFingerprint(t.Context(), request)
	if !stable || first == "" {
		t.Fatalf("first fingerprint = %q, stable=%t", first, stable)
	}
	resolver.endpoint.Deployment = "private-wire-two"
	second, stable := client.RequestFingerprint(t.Context(), request)
	if !stable || second != first {
		t.Fatalf("raw deployment affected fingerprint: first=%q second=%q stable=%t", first, second, stable)
	}
	resolver.endpoint.RouteIdentity = "stable-route-v2"
	third, stable := client.RequestFingerprint(t.Context(), request)
	if !stable || third == first {
		t.Fatalf("route identity did not affect fingerprint: first=%q third=%q stable=%t", first, third, stable)
	}
	resolver.err = errors.New("route lookup unavailable")
	if fingerprint, stable := client.RequestFingerprint(t.Context(), request); stable || fingerprint != "" {
		t.Fatalf("resolver failure fingerprint = %q, stable=%t", fingerprint, stable)
	}
}

func TestBedrockResolverRejectsNonSDKRouteFieldsBeforeRuntime(t *testing.T) {
	endpointURL, err := url.Parse("https://bedrock.example.invalid/model")
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name     string
		endpoint ai.ResolvedEndpoint
		want     string
	}{
		{name: "URL", endpoint: ai.ResolvedEndpoint{URL: endpointURL, Deployment: "model", RouteIdentity: "route"}, want: "must not contain a URL"},
		{name: "query", endpoint: ai.ResolvedEndpoint{Query: url.Values{"x": {"secret"}}, Deployment: "model", RouteIdentity: "route"}, want: "must not contain query"},
		{name: "credential scope", endpoint: ai.ResolvedEndpoint{CredentialScope: "secret", Deployment: "model", RouteIdentity: "route"}, want: "must not contain a credential scope"},
		{name: "empty deployment", endpoint: ai.ResolvedEndpoint{RouteIdentity: "route"}, want: "deployment is empty"},
		{name: "deployment whitespace", endpoint: ai.ResolvedEndpoint{Deployment: " model", RouteIdentity: "route"}, want: "surrounding whitespace"},
		{name: "empty identity", endpoint: ai.ResolvedEndpoint{Deployment: "model"}, want: "route identity is empty"},
		{name: "identity whitespace", endpoint: ai.ResolvedEndpoint{Deployment: "model", RouteIdentity: " route"}, want: "surrounding whitespace"},
		{name: "identity control", endpoint: ai.ResolvedEndpoint{Deployment: "model", RouteIdentity: "bad\nroute"}, want: "control characters"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			runtime := &fakeRuntimeClient{}
			client := newClientWithRuntime(runtime, "us-east-1", &core.NoOpLogger{})
			client.DefaultModel = "semantic-model"
			client.endpointResolver = &bedrockTestResolver{endpoint: test.endpoint}
			_, err := client.Generate(t.Context(), core.NewAIRequest("hello", "route-validation"))
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("Generate error = %v, want %q", err, test.want)
			}
			if runtime.converseInput != nil {
				t.Fatalf("runtime called with %#v", runtime.converseInput)
			}
		})
	}
}

func TestBedrockCurrentClaudeSamplingPolicy(t *testing.T) {
	omitAllModels := []string{
		"anthropic.claude-sonnet-5",
		"us.anthropic.claude-sonnet-5-v1:0",
		"arn:aws:bedrock:us-east-1:123456789012:custom-model/anthropic.claude-sonnet-5/model",
		"anthropic.claude-opus-4-7",
		"GLOBAL.ANTHROPIC.CLAUDE-OPUS-4-7.V1:0",
		"global.anthropic.claude-opus-4-8",
	}
	for _, model := range omitAllModels {
		t.Run(model, func(t *testing.T) {
			runtime := &fakeRuntimeClient{}
			client := newClientWithRuntime(runtime, "us-east-1", &core.NoOpLogger{})
			client.DefaultModel = model
			request := core.NewAIRequestFromLegacy("hello", "sampling", &core.AIOptions{
				Model: model, Temperature: 0.7, MaxTokens: 100,
				Extra: map[string]interface{}{
					"Top_K": 20, "Temperature": 0.4, "TOP_P": 0.8,
				},
			})
			result, err := client.Generate(t.Context(), request)
			if err != nil {
				t.Fatalf("Generate returned error: %v", err)
			}
			if runtime.converseInput.InferenceConfig.Temperature != nil ||
				runtime.converseInput.InferenceConfig.TopP != nil {
				t.Fatalf("sampling fields survived: %#v", runtime.converseInput.InferenceConfig)
			}
			if runtime.converseInput.AdditionalModelRequestFields != nil {
				encoded, err := runtime.converseInput.AdditionalModelRequestFields.MarshalSmithyDocument()
				if err != nil {
					t.Fatal(err)
				}
				normalized := strings.ToLower(string(encoded))
				for _, field := range []string{"top_k", "temperature", "top_p"} {
					if strings.Contains(normalized, field) {
						t.Fatalf("%s survived built-in policy: %s", field, encoded)
					}
				}
			}
			if result.RequestReport == nil || len(result.RequestReport.Adjustments) == 0 {
				t.Fatalf("sampling adjustment was not reported: %#v", result.RequestReport)
			}
		})
	}

	for _, model := range []string{
		legacyBedrockTestModel,
		"anthropic.claude-sonnet-50",
		"anthropic.claude-opus-4-70",
		"anthropic.claude-mythos-5",
	} {
		t.Run("unaffected "+model, func(t *testing.T) {
			runtime := &fakeRuntimeClient{}
			client := newClientWithRuntime(runtime, "us-east-1", &core.NoOpLogger{})
			client.DefaultModel = model
			_, err := client.Generate(t.Context(), core.NewAIRequest("hello", "ordinary-sampling"))
			if err != nil {
				t.Fatal(err)
			}
			if got := aws.ToFloat32(runtime.converseInput.InferenceConfig.Temperature); got != 0.7 {
				t.Fatalf("ordinary temperature = %v, want 0.7", got)
			}
		})
	}
}

func TestBedrockSamplingFamilyClassification(t *testing.T) {
	tests := []struct {
		model string
		want  bedrockSamplingPolicy
	}{
		{model: "anthropic.claude-opus-4-7", want: bedrockSamplingOmitAll},
		{model: "eu.anthropic.claude-opus-4-8-v1:0", want: bedrockSamplingOmitAll},
		{model: "arn:aws:bedrock:us-east-1:123:custom-model/anthropic.claude-sonnet-5/model", want: bedrockSamplingOmitAll},
		{model: "GLOBAL.ANTHROPIC.CLAUDE-FABLE-5.V1:0", want: bedrockSamplingFable5},
		{model: "anthropic.claude-opus-4-70", want: bedrockSamplingUnrestricted},
		{model: "prefixanthropic.claude-sonnet-5", want: bedrockSamplingUnrestricted},
		{model: "anthropic.claude-mythos-5", want: bedrockSamplingUnrestricted},
	}
	for _, test := range tests {
		t.Run(test.model, func(t *testing.T) {
			if got := bedrockSamplingPolicyForModel(test.model); got != test.want {
				t.Fatalf("sampling policy = %d, want %d", got, test.want)
			}
		})
	}
}

func TestBedrockCurrentClaudeSamplingValidation(t *testing.T) {
	for _, test := range []struct {
		name  string
		path  string
		value interface{}
	}{
		{name: "inference temperature", path: `/inference_config/temperature`, value: 0.5},
		{name: "additional temperature", path: `/additional_model_request_fields/Temperature`, value: 0.5},
		{name: "additional top p", path: `/additional_model_request_fields/TOP_P`, value: 0.5},
		{name: "additional top k", path: `/additional_model_request_fields/Top_K`, value: 20},
	} {
		t.Run("application cannot reintroduce invalid Sonnet "+test.name, func(t *testing.T) {
			runtime := &fakeRuntimeClient{}
			client := newClientWithRuntime(runtime, "us-east-1", &core.NoOpLogger{})
			client.DefaultModel = ModelClaudeSonnet5
			engine, err := newRequestPolicyEngineWithIntegration([]core.AIProviderPatch{{
				Name: "invalid-sampling", Version: "1",
				Selector: core.AIProviderSelector{Provider: "bedrock", Surface: "converse"},
				Set:      map[string]interface{}{test.path: test.value},
			}}, nil, requestpolicy.CompatibilityCompatible)
			if err != nil {
				t.Fatal(err)
			}
			client.requestPolicy = engine
			_, err = client.Generate(t.Context(), core.NewAIRequest("hello", "invalid-sampling"))
			if err == nil || !strings.Contains(err.Error(), "does not accept modified") {
				t.Fatalf("Generate error = %v", err)
			}
			if runtime.converseInput != nil {
				t.Fatal("runtime was called for invalid sampling")
			}
		})
	}

	t.Run("per-request patch cannot reintroduce non-canonical Sonnet sampling", func(t *testing.T) {
		runtime := &fakeRuntimeClient{}
		client := newClientWithRuntime(runtime, "us-east-1", &core.NoOpLogger{})
		client.DefaultModel = ModelClaudeSonnet5
		request := core.NewAIRequest("hello", "request-sampling")
		request.Patches = []core.AIProviderPatch{{
			Name: "request-invalid-sampling", Version: "1",
			Selector: core.AIProviderSelector{Provider: "bedrock", Surface: "converse"},
			Set:      map[string]interface{}{`/additional_model_request_fields/temperature`: 0.5},
		}}

		_, err := client.Generate(t.Context(), request)
		if err == nil || !strings.Contains(err.Error(), "does not accept modified") {
			t.Fatalf("Generate error = %v", err)
		}
		if runtime.converseInput != nil {
			t.Fatal("runtime was called for invalid sampling")
		}
	})

	t.Run("case-duplicate legacy sampling fails closed", func(t *testing.T) {
		runtime := &fakeRuntimeClient{}
		client := newClientWithRuntime(runtime, "us-east-1", &core.NoOpLogger{})
		client.DefaultModel = ModelClaudeSonnet5
		request := core.NewAIRequestFromLegacy("hello", "duplicate-sampling", &core.AIOptions{
			Model: ModelClaudeSonnet5,
			Extra: map[string]interface{}{
				"temperature": 1,
				"Temperature": 0.5,
			},
		})

		_, err := client.Generate(t.Context(), request)
		if err == nil || !strings.Contains(err.Error(), "case-insensitive temperature fields") {
			t.Fatalf("Generate error = %v", err)
		}
		if runtime.converseInput != nil {
			t.Fatal("runtime was called for ambiguous sampling")
		}
	})

	t.Run("strict mode rejects built-in adjustment to explicit intent", func(t *testing.T) {
		client := newClientWithRuntime(&fakeRuntimeClient{}, "us-east-1", &core.NoOpLogger{})
		client.DefaultModel = ModelClaudeSonnet5
		engine, err := newRequestPolicyEngineWithIntegration(nil, nil, requestpolicy.CompatibilityStrict)
		if err != nil {
			t.Fatal(err)
		}
		client.requestPolicy = engine
		request := core.NewAIRequest("hello", "strict")
		request.Generation.Temperature = core.SetAIParameter(float32(0.5))
		_, err = client.Generate(t.Context(), request)
		if err == nil || !strings.Contains(err.Error(), "compatibility") {
			t.Fatalf("strict Generate error = %v", err)
		}
	})

	for _, mode := range []requestpolicy.CompatibilityMode{
		requestpolicy.CompatibilityCompatible,
		requestpolicy.CompatibilityStrict,
	} {
		t.Run(fmt.Sprintf("Fable preserves documented portable sampling in mode %d", mode), func(t *testing.T) {
			runtime := &fakeRuntimeClient{}
			client := newClientWithRuntime(runtime, "us-east-1", &core.NoOpLogger{})
			client.DefaultModel = fableBedrockTestModel
			engine, err := newRequestPolicyEngineWithIntegration(nil, nil, mode)
			if err != nil {
				t.Fatal(err)
			}
			client.requestPolicy = engine
			request := core.NewAIRequestFromLegacy("hello", "fable", &core.AIOptions{
				Model:       fableBedrockTestModel,
				Temperature: 0.7,
				Extra:       map[string]interface{}{"Top_K": 20},
			})
			request.Generation.Temperature = core.SetAIParameter(bedrockFableTemperature)
			request.Generation.TopP = core.SetAIParameter(validFableTopP)
			_, err = client.Generate(t.Context(), request)
			if err != nil {
				t.Fatalf("Generate returned error: %v", err)
			}
			if got := aws.ToFloat32(runtime.converseInput.InferenceConfig.Temperature); got != bedrockFableTemperature {
				t.Fatalf("temperature = %v", got)
			}
			if got := aws.ToFloat32(runtime.converseInput.InferenceConfig.TopP); got != validFableTopP {
				t.Fatalf("top_p = %v", got)
			}
			if runtime.converseInput.AdditionalModelRequestFields != nil {
				encoded, err := runtime.converseInput.AdditionalModelRequestFields.MarshalSmithyDocument()
				if err != nil {
					t.Fatal(err)
				}
				if strings.Contains(strings.ToLower(string(encoded)), "top_k") {
					t.Fatalf("top_k survived Fable policy: %s", encoded)
				}
			}
		})
	}

	t.Run("Fable omits incompatible inherited temperature", func(t *testing.T) {
		runtime := &fakeRuntimeClient{}
		client := newClientWithRuntime(runtime, "us-east-1", &core.NoOpLogger{})
		client.DefaultModel = fableBedrockTestModel
		result, err := client.Generate(t.Context(), core.NewAIRequest("hello", "fable-default"))
		if err != nil {
			t.Fatalf("Generate returned error: %v", err)
		}
		if runtime.converseInput.InferenceConfig.Temperature != nil {
			t.Fatalf("inherited temperature survived: %#v", runtime.converseInput.InferenceConfig)
		}
		if result.RequestReport == nil || len(result.RequestReport.Adjustments) == 0 {
			t.Fatalf("inherited-temperature adjustment missing: %#v", result.RequestReport)
		}
		adjustment := result.RequestReport.Adjustments[0]
		wantRule := bedrockFableTemperaturePreparationRule + "@" +
			bedrockFableTemperatureRuleVersion
		if adjustment.Rule != wantRule ||
			adjustment.Path != "/inference_config/temperature" ||
			adjustment.Action != "remove" {
			t.Fatalf("inherited-temperature adjustment = %#v, want rule %q", adjustment, wantRule)
		}
	})

	for _, test := range []struct {
		name   string
		mutate func(*core.AIRequest)
		want   string
	}{
		{
			name: "temperature below one",
			mutate: func(request *core.AIRequest) {
				request.Generation.Temperature = core.SetAIParameter(float32(0.5))
			},
			want: "temperature must be 1 or omitted",
		},
		{
			name: "top_p below range",
			mutate: func(request *core.AIRequest) {
				request.Generation.TopP = core.SetAIParameter(float32(0.98))
			},
			want: "top_p must be at least 0.99",
		},
		{
			name: "top_p upper bound",
			mutate: func(request *core.AIRequest) {
				request.Generation.TopP = core.SetAIParameter(bedrockFableTopPMaximum)
			},
			want: "top_p must be at least 0.99",
		},
	} {
		t.Run("Fable rejects "+test.name, func(t *testing.T) {
			runtime := &fakeRuntimeClient{}
			client := newClientWithRuntime(runtime, "us-east-1", &core.NoOpLogger{})
			client.DefaultModel = fableBedrockTestModel
			request := core.NewAIRequest("hello", "fable-invalid")
			test.mutate(request)
			_, err := client.Generate(t.Context(), request)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("Generate error = %v, want %q", err, test.want)
			}
			if runtime.converseInput != nil {
				t.Fatal("runtime was called for invalid Fable sampling")
			}
		})
	}

	t.Run("application cannot reintroduce Fable top_k", func(t *testing.T) {
		runtime := &fakeRuntimeClient{}
		client := newClientWithRuntime(runtime, "us-east-1", &core.NoOpLogger{})
		client.DefaultModel = fableBedrockTestModel
		engine, err := newRequestPolicyEngineWithIntegration([]core.AIProviderPatch{{
			Name: "invalid-fable-top-k", Version: "1",
			Selector: core.AIProviderSelector{Provider: "bedrock", Surface: "converse"},
			Set:      map[string]interface{}{`/additional_model_request_fields/Top_K`: 20},
		}}, nil, requestpolicy.CompatibilityCompatible)
		if err != nil {
			t.Fatal(err)
		}
		client.requestPolicy = engine
		_, err = client.Generate(t.Context(), core.NewAIRequest("hello", "fable-top-k"))
		if err == nil || !strings.Contains(err.Error(), "does not accept top_k") {
			t.Fatalf("Generate error = %v", err)
		}
		if runtime.converseInput != nil {
			t.Fatal("runtime was called for invalid Fable top_k")
		}
	})

	for _, test := range []struct {
		name  string
		path  string
		value interface{}
		want  string
	}{
		{
			name: "temperature",
			path: `/additional_model_request_fields/Temperature`, value: 1.0,
			want: "must use inference_config",
		},
		{
			name: "top p",
			path: `/additional_model_request_fields/TOP_P`, value: 0.995,
			want: "must use inference_config",
		},
	} {
		t.Run("application cannot reintroduce invalid Fable "+test.name, func(t *testing.T) {
			runtime := &fakeRuntimeClient{}
			client := newClientWithRuntime(runtime, "us-east-1", &core.NoOpLogger{})
			client.DefaultModel = fableBedrockTestModel
			engine, err := newRequestPolicyEngineWithIntegration([]core.AIProviderPatch{{
				Name: "invalid-fable-sampling", Version: "1",
				Selector: core.AIProviderSelector{Provider: "bedrock", Surface: "converse"},
				Set:      map[string]interface{}{test.path: test.value},
			}}, nil, requestpolicy.CompatibilityCompatible)
			if err != nil {
				t.Fatal(err)
			}
			client.requestPolicy = engine

			_, err = client.Generate(t.Context(), core.NewAIRequest("hello", "fable-sampling"))
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("Generate error = %v, want %q", err, test.want)
			}
			if runtime.converseInput != nil {
				t.Fatal("runtime was called for invalid Fable sampling")
			}
		})
	}
}

func TestBedrockFableSamplingRequiresCanonicalConverseContainers(t *testing.T) {
	for _, defaultTemperature := range []float32{bedrockFableTemperature, 0.7} {
		t.Run(fmt.Sprintf("legacy wrong-container sampling rejects with inherited temperature %g", defaultTemperature), func(t *testing.T) {
			runtime := &fakeRuntimeClient{}
			client := newClientWithRuntime(runtime, "us-east-1", &core.NoOpLogger{})
			client.DefaultModel = fableBedrockTestModel
			client.DefaultTemperature = defaultTemperature
			request := core.NewAIRequestFromLegacy("hello", "fable-legacy", &core.AIOptions{
				Model: fableBedrockTestModel,
				Extra: map[string]interface{}{
					"Temperature": fableTemperatureJSON,
					"TOP_P":       validFableTopPJSON,
				},
			})

			result, err := client.Generate(t.Context(), request)
			if err == nil || !strings.Contains(err.Error(), "must use inference_config") {
				t.Fatalf("Generate error = %v", err)
			}
			if strings.Contains(err.Error(), "conflicts with") {
				t.Fatalf("legacy sampling was rejected as an inherited-state collision: %v", err)
			}
			if result == nil || result.RequestReport == nil {
				t.Fatalf("policy failure report = %#v", result)
			}
			if runtime.converseInput != nil {
				t.Fatal("runtime was called for wrong-container legacy sampling")
			}
		})
	}

	t.Run("legacy additional temperature overlaps portable set without migration", func(t *testing.T) {
		runtime := &fakeRuntimeClient{}
		client := newClientWithRuntime(runtime, "us-east-1", &core.NoOpLogger{})
		client.DefaultModel = fableBedrockTestModel
		request := core.NewAIRequestFromLegacy("hello", "fable-overlap", &core.AIOptions{
			Model: fableBedrockTestModel,
			Extra: map[string]interface{}{"Temperature": fableTemperatureJSON},
		})
		request.Generation.Temperature = core.SetAIParameter(bedrockFableTemperature)

		_, err := client.Generate(t.Context(), request)
		if err == nil || !strings.Contains(err.Error(), "must use inference_config") {
			t.Fatalf("Generate error = %v", err)
		}
		if strings.Contains(err.Error(), "conflicts with") {
			t.Fatalf("overlap was treated as a migration collision: %v", err)
		}
		if runtime.converseInput != nil {
			t.Fatal("runtime was called for wrong-container overlapping sampling")
		}
	})

	for _, channel := range []string{"application", "middleware", "per-request"} {
		t.Run(channel+" can remediate legacy sampling before final validation", func(t *testing.T) {
			runtime := &fakeRuntimeClient{}
			client := newClientWithRuntime(runtime, "us-east-1", &core.NoOpLogger{})
			client.DefaultModel = fableBedrockTestModel
			request := core.NewAIRequestFromLegacy("hello", "fable-remediation", &core.AIOptions{
				Model: fableBedrockTestModel,
				Extra: map[string]interface{}{
					"Temperature": fableTemperatureJSON,
					"TOP_P":       validFableTopPJSON,
					"vendor":      "preserved",
				},
			})
			patch := core.AIProviderPatch{
				Name: channel + "-fable-remediation", Version: "1",
				Selector: core.AIProviderSelector{Provider: "bedrock", Surface: "converse"},
				Set: map[string]interface{}{
					"/inference_config/temperature": fableTemperatureJSON,
					"/inference_config/top_p":       validFableTopPJSON,
				},
				Remove: []string{
					"/additional_model_request_fields/temperature",
					"/additional_model_request_fields/top_p",
				},
			}
			var appRules []core.AIProviderPatch
			var middleware []requestpolicy.RequestMiddleware
			switch channel {
			case "application":
				appRules = []core.AIProviderPatch{patch}
			case "middleware":
				middleware = []requestpolicy.RequestMiddleware{&bedrockSetMiddleware{
					values: patch.Set,
					remove: patch.Remove,
				}}
			case "per-request":
				request.Patches = []core.AIProviderPatch{patch}
			}
			engine, err := newRequestPolicyEngineWithIntegration(
				appRules,
				middleware,
				requestpolicy.CompatibilityCompatible,
			)
			if err != nil {
				t.Fatal(err)
			}
			client.requestPolicy = engine

			result, err := client.Generate(t.Context(), request)
			if err != nil {
				t.Fatalf("Generate returned error: %v", err)
			}
			if got := aws.ToFloat32(runtime.converseInput.InferenceConfig.Temperature); got != bedrockFableTemperature {
				t.Fatalf("temperature = %v", got)
			}
			if got := aws.ToFloat32(runtime.converseInput.InferenceConfig.TopP); got != validFableTopP {
				t.Fatalf("top_p = %v", got)
			}
			encoded, err := runtime.converseInput.AdditionalModelRequestFields.MarshalSmithyDocument()
			if err != nil {
				t.Fatal(err)
			}
			text := strings.ToLower(string(encoded))
			if strings.Contains(text, "temperature") || strings.Contains(text, "top_p") ||
				!strings.Contains(text, "vendor") {
				t.Fatalf("additional model request fields = %s", encoded)
			}
			removed := map[string]bool{}
			for _, adjustment := range result.RequestReport.Adjustments {
				if adjustment.Action == "remove" {
					removed[adjustment.Path] = true
				}
			}
			for _, path := range patch.Remove {
				if !removed[path] {
					t.Fatalf("remediation adjustment for %s missing: %#v", path, result.RequestReport.Adjustments)
				}
			}
		})
	}

	t.Run("JSON numbers in canonical inference config are accepted", func(t *testing.T) {
		runtime := &fakeRuntimeClient{}
		client := newClientWithRuntime(runtime, "us-east-1", &core.NoOpLogger{})
		client.DefaultModel = fableBedrockTestModel
		engine, err := newRequestPolicyEngineWithIntegration([]core.AIProviderPatch{{
			Name: "fable-json-number", Version: "1",
			Selector: core.AIProviderSelector{Provider: "bedrock", Surface: "converse"},
			Set: map[string]interface{}{
				"/inference_config/temperature": fableTemperatureJSON,
				"/inference_config/top_p":       validFableTopPJSON,
				"/inference_config/max_tokens":  json.Number("128"),
			},
		}}, nil, requestpolicy.CompatibilityCompatible)
		if err != nil {
			t.Fatal(err)
		}
		client.requestPolicy = engine

		if _, err := client.Generate(t.Context(), core.NewAIRequest("hello", "fable-json")); err != nil {
			t.Fatalf("Generate returned error: %v", err)
		}
		if aws.ToInt32(runtime.converseInput.InferenceConfig.MaxTokens) != 128 ||
			aws.ToFloat32(runtime.converseInput.InferenceConfig.Temperature) != bedrockFableTemperature ||
			aws.ToFloat32(runtime.converseInput.InferenceConfig.TopP) != validFableTopP {
			t.Fatalf("inference config = %#v", runtime.converseInput.InferenceConfig)
		}
	})

	for _, channel := range []string{"application", "middleware", "per-request"} {
		t.Run(channel+" cannot place valid Fable sampling in additional fields", func(t *testing.T) {
			runtime := &fakeRuntimeClient{}
			client := newClientWithRuntime(runtime, "us-east-1", &core.NoOpLogger{})
			client.DefaultModel = fableBedrockTestModel
			patch := core.AIProviderPatch{
				Name: channel + "-fable-additional", Version: "1",
				Selector: core.AIProviderSelector{Provider: "bedrock", Surface: "converse"},
				Set: map[string]interface{}{
					"/additional_model_request_fields/temperature": fableTemperatureJSON,
					"/additional_model_request_fields/TOP_P":       validFableTopPJSON,
				},
			}
			var appRules []core.AIProviderPatch
			var middleware []requestpolicy.RequestMiddleware
			request := core.NewAIRequest("hello", "fable-"+channel)
			switch channel {
			case "application":
				appRules = []core.AIProviderPatch{patch}
			case "middleware":
				middleware = []requestpolicy.RequestMiddleware{&bedrockSetMiddleware{values: patch.Set}}
			case "per-request":
				request.Patches = []core.AIProviderPatch{patch}
			}
			engine, err := newRequestPolicyEngineWithIntegration(
				appRules,
				middleware,
				requestpolicy.CompatibilityCompatible,
			)
			if err != nil {
				t.Fatal(err)
			}
			client.requestPolicy = engine

			_, err = client.Generate(t.Context(), request)
			if err == nil || !strings.Contains(err.Error(), "must use inference_config") {
				t.Fatalf("Generate error = %v", err)
			}
			if runtime.converseInput != nil {
				t.Fatal("runtime was called for non-canonical Fable sampling")
			}
		})
	}
}

func TestBedrockJSONNumberValidationErrorsRemainSpecific(t *testing.T) {
	if _, err := positiveInt32(json.Number("1.5")); err == nil ||
		!strings.Contains(err.Error(), "must be an integer") {
		t.Fatalf("fractional max_tokens error = %v", err)
	}
	if _, err := finiteFloat32(json.Number("1e999")); err == nil ||
		!strings.Contains(err.Error(), "invalid JSON number") {
		t.Fatalf("out-of-range JSON number error = %v", err)
	}
}

func TestBedrockAdditionalDocumentNormalizesJSONNumbers(t *testing.T) {
	t.Run("decoder numbers remain numeric recursively", func(t *testing.T) {
		runtime := &fakeRuntimeClient{}
		client := newClientWithRuntime(runtime, "us-east-1", &core.NoOpLogger{})
		client.DefaultModel = "meta.llama3-2-90b-instruct-v1:0"
		request := core.NewAIRequestFromLegacy("hello", "document-numbers", &core.AIOptions{
			Model: client.DefaultModel,
			Extra: map[string]interface{}{
				"top_k":         json.Number("20"),
				"large_integer": json.Number("9223372036854775807"),
				"nested": map[string]interface{}{
					"ratio": json.Number("0.25"),
				},
				"items":    []interface{}{json.Number("7"), "seven"},
				"unsigned": uint16(9),
			},
		})

		if _, err := client.Generate(t.Context(), request); err != nil {
			t.Fatalf("Generate returned error: %v", err)
		}
		encoded, err := runtime.converseInput.AdditionalModelRequestFields.MarshalSmithyDocument()
		if err != nil {
			t.Fatal(err)
		}
		decoder := json.NewDecoder(strings.NewReader(string(encoded)))
		decoder.UseNumber()
		var document map[string]interface{}
		if err := decoder.Decode(&document); err != nil {
			t.Fatalf("decode additional document %s: %v", encoded, err)
		}
		if got, ok := document["top_k"].(json.Number); !ok || got.String() != "20" {
			t.Fatalf("top_k = %#v (%T), want numeric 20", document["top_k"], document["top_k"])
		}
		if got, ok := document["large_integer"].(json.Number); !ok ||
			got.String() != "9223372036854775807" {
			t.Fatalf(
				"large_integer = %#v (%T), want exact unquoted int64",
				document["large_integer"],
				document["large_integer"],
			)
		}
		nested := document["nested"].(map[string]interface{})
		if got, ok := nested["ratio"].(json.Number); !ok || got.String() != "0.25" {
			t.Fatalf("nested ratio = %#v (%T)", nested["ratio"], nested["ratio"])
		}
		items := document["items"].([]interface{})
		if got, ok := items[0].(json.Number); !ok || got.String() != "7" {
			t.Fatalf("item zero = %#v (%T)", items[0], items[0])
		}
		if got, ok := document["unsigned"].(json.Number); !ok || got.String() != "9" {
			t.Fatalf("unsigned = %#v (%T)", document["unsigned"], document["unsigned"])
		}
	})

	t.Run("invalid decoder number fails before runtime", func(t *testing.T) {
		runtime := &fakeRuntimeClient{}
		client := newClientWithRuntime(runtime, "us-east-1", &core.NoOpLogger{})
		client.DefaultModel = legacyBedrockTestModel
		request := core.NewAIRequestFromLegacy("hello", "invalid-document-number", &core.AIOptions{
			Model: client.DefaultModel,
			Extra: map[string]interface{}{"top_k": json.Number("not-a-number")},
		})

		if fingerprint, stable := client.RequestFingerprint(t.Context(), request); stable || fingerprint != "" {
			t.Fatalf("invalid decoder number fingerprint = %q, stable=%t", fingerprint, stable)
		}
		_, err := client.Generate(t.Context(), request)
		if err == nil || !strings.Contains(err.Error(), "invalid JSON number") {
			t.Fatalf("Generate error = %v", err)
		}
		if runtime.converseInput != nil {
			t.Fatal("runtime was called for an invalid document number")
		}
	})

	t.Run("empty decoder number fails instead of becoming zero", func(t *testing.T) {
		runtime := &fakeRuntimeClient{}
		client := newClientWithRuntime(runtime, "us-east-1", &core.NoOpLogger{})
		client.DefaultModel = legacyBedrockTestModel
		request := core.NewAIRequestFromLegacy("hello", "empty-document-number", &core.AIOptions{
			Model: client.DefaultModel,
			Extra: map[string]interface{}{"top_k": json.Number("")},
		})

		if fingerprint, stable := client.RequestFingerprint(t.Context(), request); stable || fingerprint != "" {
			t.Fatalf("empty decoder number fingerprint = %q, stable=%t", fingerprint, stable)
		}
		_, err := client.Generate(t.Context(), request)
		if err == nil || !strings.Contains(err.Error(), "invalid empty JSON number") {
			t.Fatalf("Generate error = %v", err)
		}
		if runtime.converseInput != nil {
			t.Fatal("runtime was called for an empty document number")
		}
	})

	t.Run("typed structs are rejected explicitly", func(t *testing.T) {
		type typedAdditional struct {
			TopK json.Number `json:"top_k"`
		}
		runtime := &fakeRuntimeClient{}
		client := newClientWithRuntime(runtime, "us-east-1", &core.NoOpLogger{})
		client.DefaultModel = legacyBedrockTestModel
		request := core.NewAIRequestFromLegacy("hello", "typed-document", &core.AIOptions{
			Model: client.DefaultModel,
			Extra: map[string]interface{}{
				"typed": typedAdditional{TopK: json.Number("20")},
			},
		})

		if fingerprint, stable := client.RequestFingerprint(t.Context(), request); stable || fingerprint != "" {
			t.Fatalf("typed document fingerprint = %q, stable=%t", fingerprint, stable)
		}
		_, err := client.Generate(t.Context(), request)
		if err == nil || !strings.Contains(err.Error(), "unsupported document struct") {
			t.Fatalf("Generate error = %v", err)
		}
		if runtime.converseInput != nil {
			t.Fatal("runtime was called for a typed additional document")
		}
	})
}

func TestNormalizeBedrockDocumentValueHandlesContainerEdges(t *testing.T) {
	number := json.Number("3")
	pointer := &number
	normalized, err := normalizeBedrockDocumentValue(map[string]interface{}{
		"pointer":  pointer,
		"array":    [2]interface{}{json.Number("1"), uint8(2)},
		"nil_map":  map[string]interface{}(nil),
		"nil_list": []interface{}(nil),
	})
	if err != nil {
		t.Fatalf("normalize returned error: %v", err)
	}
	document := normalized.(map[string]interface{})
	if document["pointer"] != smithydocument.Number("3") {
		t.Fatalf("pointer = %#v", document["pointer"])
	}
	array := document["array"].([]interface{})
	if array[0] != smithydocument.Number("1") || array[1] != uint8(2) {
		t.Fatalf("array = %#v", array)
	}
	if document["nil_map"] != nil || document["nil_list"] != nil {
		t.Fatalf("nil containers = %#v", document)
	}
	if normalized, err := normalizeBedrockDocumentValue(nil); err != nil || normalized != nil {
		t.Fatalf("nil normalization = %#v, %v", normalized, err)
	}
	aliased := make([]interface{}, 2)
	aliased[0] = "head"
	aliased[1] = aliased[:1]
	normalizedAliased, err := normalizeBedrockDocumentValue(aliased)
	if err != nil {
		t.Fatalf("legal aliased subslice was rejected: %v", err)
	}
	aliasedDocument := normalizedAliased.([]interface{})
	if got := aliasedDocument[1].([]interface{}); len(got) != 1 || got[0] != "head" {
		t.Fatalf("normalized aliased subslice = %#v", normalizedAliased)
	}

	cyclicMap := map[string]interface{}{}
	cyclicMap["self"] = cyclicMap
	cyclicSlice := []interface{}{nil}
	cyclicSlice[0] = cyclicSlice
	var cyclicPointer interface{}
	cyclicPointer = &cyclicPointer
	for _, test := range []struct {
		name  string
		value interface{}
		want  string
	}{
		{name: "non-string map key", value: map[int]string{1: "one"}, want: "map key type"},
		{name: "cyclic map", value: cyclicMap, want: "cyclic map"},
		{name: "cyclic slice", value: cyclicSlice, want: "cyclic slice"},
		{name: "cyclic pointer", value: cyclicPointer, want: "cyclic pointer"},
		{name: "struct", value: struct{ Value int }{Value: 1}, want: "unsupported document struct"},
		{name: "uintptr", value: uintptr(1), want: "unsupported document value"},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, err := normalizeBedrockDocumentValue(test.value)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("normalize error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestBedrockInferenceNumericConversionAcceptsNamedAndUnsignedValues(t *testing.T) {
	type namedUint16 uint16
	type namedFloat32 float32

	runtime := &fakeRuntimeClient{}
	client := newClientWithRuntime(runtime, "us-east-1", &core.NoOpLogger{})
	client.DefaultModel = legacyBedrockTestModel
	engine, err := newRequestPolicyEngineWithIntegration([]core.AIProviderPatch{{
		Name: "typed-numbers", Version: "1",
		Selector: core.AIProviderSelector{Provider: "bedrock", Surface: "converse"},
		Set: map[string]interface{}{
			"/inference_config/max_tokens":  namedUint16(128),
			"/inference_config/temperature": namedFloat32(0.5),
		},
	}}, nil, requestpolicy.CompatibilityCompatible)
	if err != nil {
		t.Fatal(err)
	}
	client.requestPolicy = engine

	if _, err := client.Generate(t.Context(), core.NewAIRequest("hello", "typed-numbers")); err != nil {
		t.Fatalf("Generate returned error: %v", err)
	}
	if got := aws.ToInt32(runtime.converseInput.InferenceConfig.MaxTokens); got != 128 {
		t.Fatalf("max_tokens = %d", got)
	}
	if got := aws.ToFloat32(runtime.converseInput.InferenceConfig.Temperature); got != 0.5 {
		t.Fatalf("temperature = %v", got)
	}
}

func TestBedrockNumericConversionRejectsUnsupportedAndOutOfRangeValues(t *testing.T) {
	for _, test := range []struct {
		name  string
		value interface{}
		want  string
	}{
		{name: "nil", want: "unsupported type"},
		{name: "boolean", value: true, want: "unsupported type"},
		{name: "negative", value: int8(-1), want: "positive 32-bit"},
		{name: "unsigned overflow", value: uint64(math.MaxUint64), want: "64-bit integer"},
		{name: "positive overflow", value: int64(math.MaxInt32) + 1, want: "positive 32-bit"},
		{name: "fractional float", value: float32(1.5), want: "must be an integer"},
		{name: "uintptr", value: uintptr(1), want: "unsupported type"},
	} {
		t.Run("integer "+test.name, func(t *testing.T) {
			_, err := positiveInt32(test.value)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("positiveInt32 error = %v, want %q", err, test.want)
			}
		})
	}

	for _, test := range []struct {
		name  string
		value interface{}
		want  string
	}{
		{name: "nil", want: "unsupported type"},
		{name: "boolean", value: true, want: "unsupported type"},
		{name: "nan", value: math.NaN(), want: "finite 32-bit"},
		{name: "infinity", value: math.Inf(1), want: "finite 32-bit"},
		{name: "overflow", value: math.MaxFloat64, want: "finite 32-bit"},
		{name: "uintptr", value: uintptr(1), want: "unsupported type"},
	} {
		t.Run("float "+test.name, func(t *testing.T) {
			_, err := finiteFloat32(test.value)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("finiteFloat32 error = %v, want %q", err, test.want)
			}
		})
	}
	if value, err := finiteFloat32(int16(-2)); err != nil || value != -2 {
		t.Fatalf("signed finite value = %v, %v", value, err)
	}
	if value, err := finiteFloat32(uint32(2)); err != nil || value != 2 {
		t.Fatalf("unsigned finite value = %v, %v", value, err)
	}
}

func TestBedrockDraftRejectsDocumentedEmptyAndStopLimits(t *testing.T) {
	t.Run("empty system", func(t *testing.T) {
		request := core.NewAIRequest("hello", "validation")
		request.Generation.SystemPrompt = core.SetAIParameter("")
		_, err := NewDraft("model", request)
		if err == nil || !strings.Contains(err.Error(), "system must be a non-empty string") {
			t.Fatalf("NewDraft error = %v", err)
		}
	})

	for _, test := range []struct {
		name  string
		stops interface{}
		want  string
	}{
		{name: "empty entry", stops: []string{""}, want: "must be non-empty"},
		{name: "too many", stops: make([]string, maxStopSequences+1), want: "at most 2500"},
	} {
		t.Run(test.name, func(t *testing.T) {
			if values, ok := test.stops.([]string); ok && test.name == "too many" {
				for index := range values {
					values[index] = "stop"
				}
			}
			draft, err := NewDraft("model", core.NewAIRequest("hello", "validation"))
			if err != nil {
				t.Fatal(err)
			}
			engine, err := requestpolicy.NewEngine(requestpolicy.Config{
				Mode: requestpolicy.CompatibilityCompatible,
				AppRules: []core.AIProviderPatch{{
					Name: "stops", Version: "1",
					Selector: core.AIProviderSelector{Provider: "bedrock"},
					Set:      map[string]interface{}{`/inference_config/stop_sequences`: test.stops},
				}},
			})
			if err != nil {
				t.Fatal(err)
			}
			_, err = engine.Apply(t.Context(), draft, nil)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("Apply error = %v, want %q", err, test.want)
			}
		})
	}
}

type countingBedrockMiddleware struct{ calls int }

type bedrockSetMiddleware struct {
	values map[string]interface{}
	remove []string
}

func (*bedrockSetMiddleware) Name() string    { return "bedrock-set" }
func (*bedrockSetMiddleware) Version() string { return "1" }
func (middleware *bedrockSetMiddleware) Apply(
	_ context.Context,
	editor requestpolicy.RequestEditor,
) error {
	for path, value := range middleware.values {
		if err := editor.Set(path, value); err != nil {
			return err
		}
	}
	for _, path := range middleware.remove {
		if err := editor.Remove(path); err != nil {
			return err
		}
	}
	return nil
}

func (*countingBedrockMiddleware) Name() string    { return "counting-bedrock" }
func (*countingBedrockMiddleware) Version() string { return "1" }
func (middleware *countingBedrockMiddleware) Apply(context.Context, requestpolicy.RequestEditor) error {
	middleware.calls++
	return nil
}

func TestBedrockRetryOptionsHonorFrameworkMaxRetries(t *testing.T) {
	runtime := &fakeRuntimeClient{invokeOut: &bedrockruntime.InvokeModelOutput{Body: []byte(`{"embedding":[0.1]}`)}}
	client := newClientWithRuntime(runtime, "us-east-1", &core.NoOpLogger{})
	client.DefaultModel = legacyBedrockTestModel
	middleware := &countingBedrockMiddleware{}
	engine, err := newRequestPolicyEngineWithIntegration(nil, []requestpolicy.RequestMiddleware{middleware}, requestpolicy.CompatibilityCompatible)
	if err != nil {
		t.Fatal(err)
	}
	client.requestPolicy = engine

	client.MaxRetries = 0
	if _, err := client.Generate(t.Context(), core.NewAIRequest("hello", "retry-zero")); err != nil {
		t.Fatal(err)
	}
	if runtime.converseAttempts != 1 {
		t.Fatalf("Converse attempts = %d, want 1", runtime.converseAttempts)
	}

	client.MaxRetries = 4
	if _, err := client.Generate(t.Context(), core.NewAIRequest("hello", "retry-four")); err != nil {
		t.Fatal(err)
	}
	if runtime.converseAttempts != 5 || middleware.calls != 2 {
		t.Fatalf("Converse attempts=%d middleware calls=%d", runtime.converseAttempts, middleware.calls)
	}

	runtime.stream = newFakeEventStream()
	client.MaxRetries = 2
	if _, err := client.Stream(t.Context(), core.NewAIRequest("hello", "stream-retry"), func(core.StreamChunk) error { return nil }); err != nil {
		t.Fatal(err)
	}
	if runtime.streamAttempts != 3 {
		t.Fatalf("ConverseStream attempts = %d, want 3", runtime.streamAttempts)
	}
	if _, err := client.InvokeModel(t.Context(), "model", []byte(`{}`)); err != nil {
		t.Fatal(err)
	}
	if runtime.invokeAttempts != 3 {
		t.Fatalf("InvokeModel attempts = %d, want 3", runtime.invokeAttempts)
	}
}

func TestNormalizeBedrockErrors(t *testing.T) {
	secret := aws.String("provider-secret")
	tests := []struct {
		name      string
		err       error
		status    int
		retryable bool
	}{
		{name: "access denied", err: &types.AccessDeniedException{Message: secret}, status: http.StatusForbidden},
		{name: "conflict", err: &types.ConflictException{Message: secret}, status: http.StatusConflict},
		{name: "internal", err: &types.InternalServerException{Message: secret}, status: http.StatusInternalServerError},
		{name: "model error", err: &types.ModelErrorException{Message: secret, OriginalStatusCode: aws.Int32(502)}, status: 502, retryable: true},
		{name: "model not ready", err: &types.ModelNotReadyException{Message: secret}, status: http.StatusTooManyRequests, retryable: true},
		{name: "model stream", err: &types.ModelStreamErrorException{Message: secret, OriginalStatusCode: aws.Int32(500)}, status: 500, retryable: true},
		{name: "model timeout", err: &types.ModelTimeoutException{Message: secret}, status: http.StatusRequestTimeout, retryable: true},
		{name: "not found", err: &types.ResourceNotFoundException{Message: secret}, status: http.StatusNotFound},
		{name: "quota", err: &types.ServiceQuotaExceededException{Message: secret}, status: http.StatusBadRequest, retryable: true},
		{name: "unavailable", err: &types.ServiceUnavailableException{Message: secret}, status: http.StatusServiceUnavailable},
		{name: "throttling", err: &types.ThrottlingException{Message: secret}, status: http.StatusTooManyRequests},
		{name: "validation", err: &types.ValidationException{Message: secret}, status: http.StatusBadRequest},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			normalized := normalizeBedrockError(test.err, "semantic-model")
			if !errors.Is(normalized, test.err) {
				t.Fatalf("normalized error lost SDK identity: %v", normalized)
			}
			var providerErr core.ProviderError
			if !errors.As(normalized, &providerErr) {
				t.Fatalf("normalized error type = %T", normalized)
			}
			if providerErr.StatusCode() != test.status || providerErr.Provider() != "bedrock" ||
				providerErr.Model() != "semantic-model" || providerErr.IsRetryable() != test.retryable {
				t.Fatalf("provider error = status %d provider %q model %q transient=%t retryable=%t",
					providerErr.StatusCode(), providerErr.Provider(), providerErr.Model(),
					providerErr.IsTransient(), providerErr.IsRetryable())
			}
		})
	}

	unknown := errors.New("unknown transport")
	if got := normalizeBedrockError(unknown, "model"); got != unknown {
		t.Fatalf("unknown error changed identity: %v", got)
	}

	wrapped := &bedrockHTTPStatusWrapper{
		status: http.StatusBadGateway,
		err:    &types.InternalServerException{Message: secret},
	}
	var providerErr core.ProviderError
	if normalized := normalizeBedrockError(wrapped, "model"); !errors.As(normalized, &providerErr) || providerErr.StatusCode() != http.StatusBadGateway {
		t.Fatalf("response status was not preserved: %v", normalized)
	}
}

func TestBedrockTypedErrorsAreStructuredForCallersAndSanitizedForObservations(t *testing.T) {
	const secret = "validation-provider-secret"
	wantErr := &types.ValidationException{Message: aws.String(secret)}
	runtime := &fakeRuntimeClient{converseErr: wantErr}
	logger := &bedrockObservationLogger{}
	tracing := &bedrockObservationTelemetry{}
	client := newClientWithRuntime(runtime, "us-east-1", logger)
	client.DefaultModel = legacyBedrockTestModel
	client.SetLogger(logger)
	client.SetTelemetry(tracing)

	_, err := client.Generate(t.Context(), core.NewAIRequest("hello", "structured-error"))
	if !errors.Is(err, wantErr) {
		t.Fatalf("Generate error = %v", err)
	}
	var providerErr core.ProviderError
	if !errors.As(err, &providerErr) || providerErr.StatusCode() != http.StatusBadRequest || providerErr.IsRetryable() {
		t.Fatalf("provider error = %#v", providerErr)
	}
	if len(tracing.spans) != 1 || len(tracing.spans[0].errors) != 1 ||
		tracing.spans[0].errors[0].Error() != "AI provider request failed: provider_client" {
		t.Fatalf("span errors = %#v", tracing.spans)
	}
	observed := fmt.Sprint(logger.fields, tracing.spans[0].attributes, tracing.spans[0].errors)
	if strings.Contains(observed, secret) || !strings.Contains(observed, "error_type:provider_client") {
		t.Fatalf("structured error observations = %s", observed)
	}
}

func TestBedrockTitanV2EmbeddingConfigurationAndPayload(t *testing.T) {
	t.Run("default V2 payload", func(t *testing.T) {
		runtime := &fakeRuntimeClient{invokeOut: &bedrockruntime.InvokeModelOutput{Body: []byte(`{"embedding":[0.1,0.2]}`)}}
		client := newClientWithRuntime(runtime, "us-east-1", &core.NoOpLogger{})
		client.MaxRetries = 2
		embedding, err := client.GetEmbeddings(t.Context(), "hello")
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(embedding, []float32{0.1, 0.2}) ||
			aws.ToString(runtime.invokeInput.ModelId) != ModelTitanEmbedV2 || runtime.invokeAttempts != 3 {
			t.Fatalf("embedding=%#v input=%#v attempts=%d", embedding, runtime.invokeInput, runtime.invokeAttempts)
		}
		var payload map[string]interface{}
		if err := json.Unmarshal(runtime.invokeInput.Body, &payload); err != nil {
			t.Fatal(err)
		}
		if payload["inputText"] != "hello" || payload["dimensions"] != nil || payload["normalize"] != nil {
			t.Fatalf("default Titan V2 payload = %#v", payload)
		}
	})

	t.Run("configured V2 controls", func(t *testing.T) {
		configured, err := bedrockEmbeddingConfig(map[string]interface{}{
			"embedding_model":      "custom-titan-v2-route",
			"embedding_dimensions": 512,
			"embedding_normalize":  false,
		})
		if err != nil {
			t.Fatal(err)
		}
		runtime := &fakeRuntimeClient{invokeOut: &bedrockruntime.InvokeModelOutput{Body: []byte(`{"embedding":[0.3]}`)}}
		client := newClientWithRuntime(runtime, "us-east-1", &core.NoOpLogger{})
		client.embedding = configured
		if _, err := client.GetEmbeddings(t.Context(), "hello"); err != nil {
			t.Fatal(err)
		}
		var payload map[string]interface{}
		if err := json.Unmarshal(runtime.invokeInput.Body, &payload); err != nil {
			t.Fatal(err)
		}
		if aws.ToString(runtime.invokeInput.ModelId) != "custom-titan-v2-route" ||
			payload["dimensions"] != float64(512) || payload["normalize"] != false {
			t.Fatalf("configured Titan payload = model %q body %#v", aws.ToString(runtime.invokeInput.ModelId), payload)
		}
	})

	t.Run("V1 migration pin uses the V1 payload shape", func(t *testing.T) {
		const v1Route = "arn:aws:bedrock:us-east-1::foundation-model/" +
			ModelTitanEmbedV1 + ":2:8k"
		runtime := &fakeRuntimeClient{invokeOut: &bedrockruntime.InvokeModelOutput{Body: []byte(`{"embedding":[0.3]}`)}}
		client := newClientWithRuntime(runtime, "us-east-1", &core.NoOpLogger{})
		tracing := &bedrockObservationTelemetry{}
		client.SetTelemetry(tracing)
		if _, err := client.GetEmbeddings(
			t.Context(),
			"hello",
			WithEmbeddingModel(v1Route),
		); err != nil {
			t.Fatal(err)
		}
		var payload map[string]interface{}
		if err := json.Unmarshal(runtime.invokeInput.Body, &payload); err != nil {
			t.Fatal(err)
		}
		if aws.ToString(runtime.invokeInput.ModelId) != v1Route ||
			payload["inputText"] != "hello" ||
			payload["dimensions"] != nil ||
			payload["normalize"] != nil {
			t.Fatalf("V1 migration payload = model %q body %#v", aws.ToString(runtime.invokeInput.ModelId), payload)
		}
		if len(tracing.spans) != 2 ||
			tracing.spans[0].attributes["ai.model"] != titanEmbeddingV1SemanticModel ||
			tracing.spans[1].attributes["ai.model"] != titanEmbeddingV1SemanticModel ||
			strings.Contains(fmt.Sprint(tracing.spans[0].attributes, tracing.spans[1].attributes), v1Route) {
			t.Fatalf(
				"V1 embedding span identity = %#v %#v",
				tracing.spans[0].attributes,
				tracing.spans[1].attributes,
			)
		}
	})

	t.Run("per-call V1 pin clears inherited V2-only client defaults", func(t *testing.T) {
		configured, err := bedrockEmbeddingConfig(map[string]interface{}{
			"embedding_model":      ModelTitanEmbedV2,
			"embedding_dimensions": 512,
			"embedding_normalize":  false,
		})
		if err != nil {
			t.Fatal(err)
		}
		runtime := &fakeRuntimeClient{invokeOut: &bedrockruntime.InvokeModelOutput{Body: []byte(`{"embedding":[0.3]}`)}}
		client := newClientWithRuntime(runtime, "us-east-1", &core.NoOpLogger{})
		client.embedding = configured

		if _, err := client.GetEmbeddings(
			t.Context(),
			"hello",
			WithEmbeddingModel(ModelTitanEmbedV1),
		); err != nil {
			t.Fatal(err)
		}
		var payload map[string]interface{}
		if err := json.Unmarshal(runtime.invokeInput.Body, &payload); err != nil {
			t.Fatal(err)
		}
		if payload["dimensions"] != nil || payload["normalize"] != nil {
			t.Fatalf("inherited V2 controls survived per-call V1 pin: %#v", payload)
		}
		if client.embedding.dimensions != 512 ||
			client.embedding.normalize == nil ||
			*client.embedding.normalize {
			t.Fatalf("per-call V1 pin mutated client defaults: %#v", client.embedding)
		}
	})

	t.Run("zero dimensions remains an explicit omission for V1", func(t *testing.T) {
		runtime := &fakeRuntimeClient{invokeOut: &bedrockruntime.InvokeModelOutput{Body: []byte(`{"embedding":[0.3]}`)}}
		client := newClientWithRuntime(runtime, "us-east-1", &core.NoOpLogger{})

		if _, err := client.GetEmbeddings(
			t.Context(),
			"hello",
			WithEmbeddingModel(ModelTitanEmbedV1),
			WithEmbeddingDimensions(0),
		); err != nil {
			t.Fatal(err)
		}
		var payload map[string]interface{}
		if err := json.Unmarshal(runtime.invokeInput.Body, &payload); err != nil {
			t.Fatal(err)
		}
		if payload["dimensions"] != nil || payload["normalize"] != nil {
			t.Fatalf("V1 omission payload = %#v", payload)
		}
	})

	t.Run("normalization can be explicitly omitted for V2", func(t *testing.T) {
		configured, err := bedrockEmbeddingConfig(map[string]interface{}{
			"embedding_model":      ModelTitanEmbedV2,
			"embedding_dimensions": 512,
			"embedding_normalize":  false,
		})
		if err != nil {
			t.Fatal(err)
		}
		runtime := &fakeRuntimeClient{invokeOut: &bedrockruntime.InvokeModelOutput{Body: []byte(`{"embedding":[0.3]}`)}}
		client := newClientWithRuntime(runtime, "us-east-1", &core.NoOpLogger{})
		client.embedding = configured

		if _, err := client.GetEmbeddings(
			t.Context(),
			"hello",
			WithoutEmbeddingNormalization(),
		); err != nil {
			t.Fatal(err)
		}
		var payload map[string]interface{}
		if err := json.Unmarshal(runtime.invokeInput.Body, &payload); err != nil {
			t.Fatal(err)
		}
		if payload["dimensions"] != float64(512) || payload["normalize"] != nil {
			t.Fatalf("normalization omission payload = %#v", payload)
		}
		if client.embedding.normalize == nil || *client.embedding.normalize {
			t.Fatalf("per-call omission mutated client defaults: %#v", client.embedding)
		}
	})

	t.Run("per-call controls override without mutating client defaults", func(t *testing.T) {
		runtime := &fakeRuntimeClient{invokeOut: &bedrockruntime.InvokeModelOutput{Body: []byte(`{"embedding":[0.4]}`)}}
		client := newClientWithRuntime(runtime, "us-east-1", &core.NoOpLogger{})
		tracing := &bedrockObservationTelemetry{}
		client.SetTelemetry(tracing)
		if _, err := client.GetEmbeddings(
			t.Context(),
			"hello",
			WithEmbeddingModel("custom-titan-v2-route"),
			WithEmbeddingDimensions(256),
			WithEmbeddingNormalization(true),
		); err != nil {
			t.Fatal(err)
		}
		var payload map[string]interface{}
		if err := json.Unmarshal(runtime.invokeInput.Body, &payload); err != nil {
			t.Fatal(err)
		}
		if aws.ToString(runtime.invokeInput.ModelId) != "custom-titan-v2-route" ||
			payload["dimensions"] != float64(256) || payload["normalize"] != true {
			t.Fatalf("per-call Titan payload = model %q body %#v", aws.ToString(runtime.invokeInput.ModelId), payload)
		}
		if client.embedding.model != ModelTitanEmbedV2 || client.embedding.dimensions != 0 || client.embedding.normalize != nil {
			t.Fatalf("per-call options mutated client defaults: %#v", client.embedding)
		}
		if len(tracing.spans) != 2 || tracing.spans[0].attributes["ai.model"] != titanEmbeddingV2SemanticModel ||
			strings.Contains(fmt.Sprint(tracing.spans[0].attributes, tracing.spans[1].attributes), "custom-titan-v2-route") {
			t.Fatalf("embedding span identity = %#v", tracing.spans)
		}
	})

	for _, test := range []struct {
		name  string
		extra map[string]interface{}
	}{
		{name: "empty model", extra: map[string]interface{}{"embedding_model": ""}},
		{name: "whitespace model", extra: map[string]interface{}{"embedding_model": " titan "}},
		{name: "model type", extra: map[string]interface{}{"embedding_model": 7}},
		{name: "dimensions type", extra: map[string]interface{}{"embedding_dimensions": 512.0}},
		{name: "dimensions value", extra: map[string]interface{}{"embedding_dimensions": 768}},
		{name: "normalize type", extra: map[string]interface{}{"embedding_normalize": "false"}},
		{
			name: "V1 dimensions",
			extra: map[string]interface{}{
				"embedding_model":      ModelTitanEmbedV1,
				"embedding_dimensions": 512,
			},
		},
		{
			name: "V1 normalization",
			extra: map[string]interface{}{
				"embedding_model":     ModelTitanEmbedV1,
				"embedding_normalize": true,
			},
		},
		{
			name: "versioned V1 dimensions",
			extra: map[string]interface{}{
				"embedding_model":      ModelTitanEmbedV1 + ":2:8k",
				"embedding_dimensions": 512,
			},
		},
		{
			name: "V1 foundation ARN normalization",
			extra: map[string]interface{}{
				"embedding_model": "arn:aws:bedrock:us-east-1::foundation-model/" +
					ModelTitanEmbedV1,
				"embedding_normalize": false,
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, err := bedrockEmbeddingConfig(test.extra); err == nil {
				t.Fatal("expected embedding configuration error")
			}
		})
	}

	t.Run("empty text fails before InvokeModel", func(t *testing.T) {
		runtime := &fakeRuntimeClient{}
		client := newClientWithRuntime(runtime, "us-east-1", &core.NoOpLogger{})
		_, err := client.GetEmbeddings(t.Context(), "")
		if err == nil || runtime.invokeInput != nil {
			t.Fatalf("GetEmbeddings error=%v input=%#v", err, runtime.invokeInput)
		}
	})

	t.Run("invalid per-call options fail before InvokeModel", func(t *testing.T) {
		for _, options := range [][]EmbeddingOption{
			{nil},
			{WithEmbeddingModel(" titan ")},
			{WithEmbeddingDimensions(768)},
			{
				WithEmbeddingModel(ModelTitanEmbedV1),
				WithEmbeddingDimensions(512),
			},
			{
				WithEmbeddingModel(ModelTitanEmbedV1),
				WithEmbeddingNormalization(true),
			},
			{
				WithEmbeddingModel(ModelTitanEmbedV1),
				WithEmbeddingNormalization(false),
			},
			{
				WithEmbeddingModel(ModelTitanEmbedV1 + ":2:8k"),
				WithEmbeddingDimensions(512),
			},
		} {
			runtime := &fakeRuntimeClient{}
			client := newClientWithRuntime(runtime, "us-east-1", &core.NoOpLogger{})
			if _, err := client.GetEmbeddings(t.Context(), "hello", options...); err == nil || runtime.invokeInput != nil {
				t.Fatalf("GetEmbeddings options=%#v error=%v input=%#v", options, err, runtime.invokeInput)
			}
		}
	})
}

func TestBedrockInvokeModelRejectsInvalidModelIDBeforeRuntime(t *testing.T) {
	for _, modelID := range []string{"", " model ", strings.Repeat("m", maxWireModelBytes+1), "model\n"} {
		runtime := &fakeRuntimeClient{}
		client := newClientWithRuntime(runtime, "us-east-1", &core.NoOpLogger{})
		if _, err := client.InvokeModel(t.Context(), modelID, []byte(`{}`)); err == nil || runtime.invokeInput != nil {
			t.Fatalf("InvokeModel modelID=%q error=%v input=%#v", modelID, err, runtime.invokeInput)
		}
	}
}

func TestBedrockInvokeModelErrorDoesNotExposeWireModelAsSemanticMetadata(t *testing.T) {
	const wireModel = "arn:aws:bedrock:us-east-1:123456789012:application-inference-profile/private-profile"
	runtimeErr := &types.ValidationException{Message: aws.String("provider detail")}
	runtime := &fakeRuntimeClient{invokeErr: runtimeErr}
	tracing := &bedrockObservationTelemetry{}
	client := newClientWithRuntime(runtime, "us-east-1", &core.NoOpLogger{})
	client.SetTelemetry(tracing)

	_, err := client.InvokeModel(t.Context(), wireModel, []byte(`{}`))
	var providerErr core.ProviderError
	if !errors.As(err, &providerErr) || providerErr.Model() != "" || !errors.Is(err, runtimeErr) {
		t.Fatalf("InvokeModel error = %v provider error = %#v", err, providerErr)
	}
	if len(tracing.spans) != 1 || strings.Contains(fmt.Sprint(tracing.spans[0].attributes), wireModel) {
		t.Fatalf("InvokeModel span exposed wire model: %#v", tracing.spans)
	}
}

func TestBedrockStreamIgnoresReasoningDeltasButPreservesText(t *testing.T) {
	const reasoningSecret = "private reasoning content"
	stream := newFakeEventStream(
		&types.ConverseStreamOutputMemberContentBlockDelta{Value: types.ContentBlockDeltaEvent{
			Delta: &types.ContentBlockDeltaMemberReasoningContent{Value: &types.ReasoningContentBlockDeltaMemberText{Value: reasoningSecret}},
		}},
		&types.ConverseStreamOutputMemberContentBlockDelta{Value: types.ContentBlockDeltaEvent{
			Delta: &types.ContentBlockDeltaMemberText{Value: "answer"},
		}},
		&types.ConverseStreamOutputMemberMessageStop{Value: types.MessageStopEvent{StopReason: types.StopReasonEndTurn}},
	)
	runtime := &fakeRuntimeClient{stream: stream}
	client := newClientWithRuntime(runtime, "us-east-1", &core.NoOpLogger{})
	client.DefaultModel = legacyBedrockTestModel
	var chunks []core.StreamChunk
	result, err := client.Stream(t.Context(), core.NewAIRequest("hello", "reasoning"), func(chunk core.StreamChunk) error {
		chunks = append(chunks, chunk)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Response.Content != "answer" || strings.Contains(result.Response.Content, reasoningSecret) {
		t.Fatalf("stream response = %#v", result.Response)
	}
	if len(chunks) != 2 || chunks[0].Content != "answer" || chunks[1].FinishReason != string(types.StopReasonEndTurn) {
		t.Fatalf("chunks = %#v", chunks)
	}
}

var _ ai.EndpointResolver = (*bedrockTestResolver)(nil)
var _ ai.CredentialSource = bedrockTestCredentialSource{}
