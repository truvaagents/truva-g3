package openai

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"

	"github.com/truvaagents/truva-g3/ai"
	"github.com/truvaagents/truva-g3/ai/providerkit/openaiwire"
	"github.com/truvaagents/truva-g3/ai/providers"
	"github.com/truvaagents/truva-g3/ai/requestpolicy"
	"github.com/truvaagents/truva-g3/core"
)

type preparedRequest struct {
	Model              string
	Body               []byte
	Headers            http.Header
	Report             *core.AIRequestReport
	Codec              openaiwire.ProfiledCodec
	ProtectedConflicts []string
}

type requestSemantics struct {
	Request        *core.AIRequest
	Options        *core.AIOptions
	RequestedModel string
	SemanticModel  string
	ProviderAlias  string
	Surface        string
	Operation      string
	Purpose        string
	Capabilities   providers.ModelCapabilities
	ReasoningModel bool
}

func (s *requestSemantics) endpointRequest() ai.EndpointRequest {
	return ai.EndpointRequest{
		Provider:      "openai",
		ProviderAlias: s.ProviderAlias,
		Surface:       s.Surface,
		ResolvedModel: s.SemanticModel,
		Operation:     s.Operation,
		Purpose:       s.Purpose,
	}
}

type preparedInvocation struct {
	Request *preparedRequest
	Route   resolvedRoute
}

func newRequestPolicyEngine() *requestpolicy.Engine {
	engine, err := newRequestPolicyEngineWithIntegration(nil, nil, requestpolicy.CompatibilityCompatible)
	if err != nil {
		panic(fmt.Sprintf("invalid built-in OpenAI request policy: %v", err))
	}
	return engine
}

func newRequestPolicyEngineWithIntegration(
	appRules []core.AIProviderPatch,
	middleware []requestpolicy.RequestMiddleware,
	mode requestpolicy.CompatibilityMode,
) (*requestpolicy.Engine, error) {
	return requestpolicy.NewEngine(requestpolicy.Config{
		AppRules:   appRules,
		Middleware: middleware,
		Mode:       mode,
	})
}

func (c *Client) prepareInvocation(
	ctx context.Context,
	supplied *core.AIRequest,
	stream bool,
) (*preparedInvocation, error) {
	semantics, err := c.prepareSemantics(ctx, supplied, stream)
	if err != nil {
		return nil, err
	}
	route, err := c.resolveEndpoint(ctx, semantics.endpointRequest())
	if err != nil {
		return nil, err
	}
	profile, err := c.requestProfile(semantics, route)
	if err != nil {
		return nil, err
	}
	prepared, err := c.buildPolicyRequest(ctx, semantics, profile, stream)
	invocation := &preparedInvocation{Request: prepared, Route: route}
	if err != nil {
		return invocation, err
	}
	c.bindRoute(prepared, route)
	return invocation, nil
}

func (c *Client) prepareSemantics(
	ctx context.Context,
	supplied *core.AIRequest,
	stream bool,
) (*requestSemantics, error) {
	request, err := core.CloneAIRequest(supplied)
	if err != nil {
		return nil, fmt.Errorf("clone OpenAI AI request: %w", err)
	}
	options, err := providers.CloneAIOptions(request.LegacyOptions())
	if err != nil {
		return nil, fmt.Errorf("clone OpenAI legacy request options: %w", err)
	}
	options = c.ApplyDefaults(options)
	if request.Generation.Model != "" {
		options.Model = request.Generation.Model
	}
	if err := validatePortableIntent(request.Generation); err != nil {
		return nil, err
	}
	requestedModel := options.Model
	options.Model = ResolveModel(c.providerAlias, options.Model)

	providerAlias := c.getProviderName()
	caps := providers.LookupModelCapabilities(providerAlias, options.Model)
	if request.Generation.ReasoningEffort.Mode == core.AIParameterSet && caps.ReasoningStyle != "openai" {
		return nil, &core.AIRequestFeatureError{
			ClientType: "*openai.Client",
			Feature:    "generation.reasoning_effort",
		}
	}
	if request.Generation.ResponseFormat.Mode == core.AIParameterSet && !caps.SupportsJSONMode {
		return nil, &core.AIRequestFeatureError{
			ClientType: "*openai.Client",
			Feature:    "generation.response_format",
		}
	}

	reasoningEffort := options.ReasoningEffort
	if reasoningEffort == "" {
		reasoningEffort = c.ReasoningEffort
	}
	if reasoningEffort != "" && caps.ReasoningStyle != "openai" {
		providers.LogTranslationDegraded(ctx, c.Logger, providerAlias, options.Model, "reasoning_effort_stripped", "reasoning_effort")
		reasoningEffort = ""
	}
	options.ReasoningEffort = reasoningEffort
	if options.ResponseFormat != "" && !caps.SupportsJSONMode {
		providers.LogTranslationDegraded(ctx, c.Logger, providerAlias, options.Model, "response_format_stripped", "response_format")
		options.ResponseFormat = ""
	}

	clientDefaults, err := providers.CloneAIOptions(&core.AIOptions{
		Extra:   c.defaultExtra,
		Headers: c.defaultHeaders,
	})
	if err != nil {
		return nil, fmt.Errorf("clone OpenAI client request defaults: %w", err)
	}
	options.Extra = filterOpenAIExtraFields(
		ctx,
		c.Logger,
		providerAlias,
		options.Model,
		caps,
		providers.MergeAnyMaps(clientDefaults.Extra, options.Extra),
	)
	options.Headers = mergeOpenAIHeaders(clientDefaults.Headers, options.Headers)
	operation := "generate"
	if stream {
		operation = "stream"
	}
	return &requestSemantics{
		Request:        request,
		Options:        options,
		RequestedModel: requestedModel,
		SemanticModel:  options.Model,
		ProviderAlias:  providerAlias,
		Surface:        "chat-completions",
		Operation:      operation,
		Purpose:        request.Purpose,
		Capabilities:   caps,
		ReasoningModel: openaiwire.IsReasoningModel(options.Model),
	}, nil
}

func validatePortableIntent(generation core.AIGenerationOptions) error {
	parameters := []struct {
		name string
		mode core.AIParameterMode
	}{
		{name: "temperature", mode: generation.Temperature.Mode},
		{name: "top_p", mode: generation.TopP.Mode},
		{name: "top_k", mode: generation.TopK.Mode},
		{name: "max_tokens", mode: generation.MaxTokens.Mode},
		{name: "system_prompt", mode: generation.SystemPrompt.Mode},
		{name: "reasoning_effort", mode: generation.ReasoningEffort.Mode},
		{name: "response_format", mode: generation.ResponseFormat.Mode},
	}
	for _, parameter := range parameters {
		if parameter.mode != core.AIParameterInherit &&
			parameter.mode != core.AIParameterSet &&
			parameter.mode != core.AIParameterOmit {
			return fmt.Errorf("invalid generation.%s mode %d", parameter.name, parameter.mode)
		}
	}
	if generation.TopK.Mode == core.AIParameterSet {
		return &core.AIRequestFeatureError{ClientType: "*openai.Client", Feature: "generation.top_k"}
	}
	if generation.MaxTokens.Mode == core.AIParameterSet && generation.MaxTokens.Value <= 0 {
		return errors.New("generation.max_tokens must be positive")
	}
	return nil
}

func (c *Client) requestProfile(
	semantics *requestSemantics,
	_ resolvedRoute,
) (openaiwire.RequestProfile, error) {
	profile := openaiwire.RequestProfile{
		SemanticModel:   semantics.SemanticModel,
		WireModel:       semantics.SemanticModel,
		ModelField:      openaiwire.ModelFieldRequired,
		TokenLimit:      openaiwire.TokenLimitMaxTokens,
		ReasoningEffort: openaiwire.ReasoningEffortOmitted,
		Sampling:        openaiwire.SamplingOrdinary,
	}
	if semantics.ReasoningModel {
		profile.TokenLimit = openaiwire.TokenLimitMaxCompletionTokens
		profile.Sampling = openaiwire.SamplingReasoningRestricted
	}
	// The stock OpenAI Chat Completions surface supports the top-level field
	// spelling even when the framework does not claim reasoning capability for
	// an application-supplied model. This lets a scoped native policy make that
	// provider-model assertion without classifying the model as a reasoning
	// family or changing its token and sampling profile.
	if semantics.ProviderAlias == "openai" || semantics.Capabilities.ReasoningStyle == "openai" {
		profile.ReasoningEffort = openaiwire.ReasoningEffortTopLevel
		if semantics.ProviderAlias == "openai.ollama" {
			profile.ReasoningEffort = openaiwire.ReasoningEffortNestedObject
		}
	}
	return profile, profile.Validate()
}

func (c *Client) buildPolicyRequest(
	ctx context.Context,
	semantics *requestSemantics,
	profile openaiwire.RequestProfile,
	stream bool,
) (*preparedRequest, error) {
	request := semantics.Request
	options := semantics.Options

	wireOptions := *options
	wireOptions.Model = semantics.RequestedModel
	wireRequest := core.NewAIRequestFromLegacy(request.Prompt, request.Purpose, &wireOptions)
	wireRequest.Generation = request.Generation
	wireRequest.Patches = request.Patches
	codec, err := openaiwire.NewProfiledCodec(openaiwire.Config{
		SurfaceVersion:           openaiwire.DefaultSurfaceVersion,
		ReasoningTokenMultiplier: c.ReasoningTokenMultiplier,
		DefaultReasoningEffort:   options.ReasoningEffort,
	})
	if err != nil {
		return nil, fmt.Errorf("create OpenAI wire codec: %w", err)
	}
	draft, err := codec.BuildDraftWithProfile(wireRequest, profile, stream)
	if err != nil {
		return nil, err
	}
	if err := draft.BindIdentity("openai", semantics.ProviderAlias); err != nil {
		return nil, err
	}
	// Preserve the unresolved caller-facing model in selectors and reports.
	if draft.Info().RequestedModel != semantics.RequestedModel {
		return nil, errors.New("OpenAI wire requested-model invariant was not preserved")
	}
	prepared := &preparedRequest{
		Model:              options.Model,
		Codec:              codec,
		ProtectedConflicts: draft.ProtectedHeaderConflicts(),
	}
	if c.requestPolicy == nil {
		return prepared, errors.New("OpenAI request policy engine is not configured")
	}
	report, err := c.requestPolicy.Apply(ctx, draft, request.Patches)
	if report != nil {
		report.Adjustments = append(draft.Adjustments(), report.Adjustments...)
		prepared.Report = report
	}
	if err != nil {
		return prepared, err
	}
	prepared.Body, err = codec.Encode(draft)
	if err != nil {
		return prepared, err
	}

	headers := make(http.Header)
	headers.Set("Content-Type", "application/json")
	if c.credentialSource == nil && c.apiKey != "" {
		headers.Set("Authorization", "Bearer "+c.apiKey)
	}
	if stream {
		headers.Set("Accept", "text/event-stream")
	}
	providers.ApplyLegacyHeaders(headers, openAIProtectedHeaders(stream), draft.Headers(), nil)
	prepared.Headers = headers
	return prepared, nil
}

func mergeOpenAIHeaders(defaultHeaders, requestHeaders map[string]string) map[string]string {
	if len(defaultHeaders) == 0 && len(requestHeaders) == 0 {
		return nil
	}

	headers := make(http.Header)
	for _, source := range []map[string]string{defaultHeaders, requestHeaders} {
		keys := make([]string, 0, len(source))
		for name := range source {
			keys = append(keys, name)
		}
		sort.Strings(keys)
		for _, name := range keys {
			headers.Set(name, source[name])
		}
	}

	merged := make(map[string]string, len(headers))
	for name, values := range headers {
		if len(values) > 0 {
			merged[name] = values[len(values)-1]
		}
	}
	return merged
}

func openAIProtectedHeaders(stream bool) map[string]struct{} {
	protected := map[string]struct{}{
		"authorization": {},
		"content-type":  {},
	}
	if stream {
		protected["accept"] = struct{}{}
	}
	return protected
}

func (c *Client) recordRequestPreparation(ctx context.Context, span core.Span, prepared *preparedRequest) {
	if prepared == nil {
		return
	}
	if prepared.Report != nil {
		span.SetAttribute("ai.request.provider_alias", prepared.Report.ProviderAlias)
		span.SetAttribute("ai.request.surface", prepared.Report.Surface)
		span.SetAttribute("ai.request.operation", prepared.Report.Operation)
		span.SetAttribute("ai.request.policy_stable", prepared.Report.Stable)
		span.SetAttribute("ai.request.adjustment_count", len(prepared.Report.Adjustments))
		if prepared.Report.Fingerprint != "" {
			span.SetAttribute("ai.request.policy_fingerprint", prepared.Report.Fingerprint)
		}
	}
	if len(prepared.ProtectedConflicts) > 0 && c.Logger != nil {
		fields := map[string]interface{}{
			"operation":       "ai_request_policy",
			"provider":        c.getProviderName(),
			"model":           prepared.Model,
			"ignored_headers": strings.Join(prepared.ProtectedConflicts, ","),
			"migration":       "remove provider-managed names from WithHeaders and AIOptions.Headers",
		}
		providers.AddObservationRequestID(ctx, fields)
		c.Logger.WarnWithContext(ctx, "OpenAI legacy protected headers ignored", fields)
	}
}

func resultWithReport(prepared *preparedRequest, result *core.AIResult) *core.AIResult {
	if result == nil {
		if prepared == nil || prepared.Report == nil {
			return nil
		}
		result = &core.AIResult{}
	}
	if prepared != nil {
		result.RequestReport = prepared.Report
	}
	return result
}
