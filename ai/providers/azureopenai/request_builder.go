package azureopenai

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
	"github.com/truvaagents/truva-g3/ai/providers/openai/modelcatalog"
	"github.com/truvaagents/truva-g3/core"
)

type preparedRequest struct {
	SemanticModel      string
	Body               []byte
	Headers            http.Header
	Report             *core.AIRequestReport
	ProtectedConflicts []string
}

type requestSemantics struct {
	Request         *core.AIRequest
	Options         *core.AIOptions
	RequestedModel  string
	SemanticModel   string
	ProviderAlias   string
	Surface         string
	Operation       string
	Purpose         string
	Capabilities    providers.ModelCapabilities
	ReasoningModel  bool
	APIKeyConflicts []string
}

func (s *requestSemantics) endpointRequest() ai.EndpointRequest {
	return ai.EndpointRequest{
		Provider:      providerName,
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

type azureDraft struct {
	*openaiwire.Draft
}

func (d *azureDraft) SetHeader(name, value string) error {
	if strings.EqualFold(name, "api-key") {
		return errors.New("header \"api-key\" is protected")
	}
	return d.Draft.SetHeader(name, value)
}

func (d *azureDraft) RemoveHeader(name string) error {
	if strings.EqualFold(name, "api-key") {
		return errors.New("header \"api-key\" is protected")
	}
	return d.Draft.RemoveHeader(name)
}

func (d *azureDraft) Validate() error {
	if _, present := d.Header("api-key"); present {
		return errors.New("azure OpenAI api-key header invariant was not preserved")
	}
	return d.Draft.Validate()
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
	bindRoute(prepared, route)
	return invocation, nil
}

func (c *Client) prepareSemantics(
	ctx context.Context,
	supplied *core.AIRequest,
	stream bool,
) (*requestSemantics, error) {
	request, err := core.CloneAIRequest(supplied)
	if err != nil {
		return nil, fmt.Errorf("clone Azure OpenAI AI request: %w", err)
	}
	options, err := providers.CloneAIOptions(request.LegacyOptions())
	if err != nil {
		return nil, fmt.Errorf("clone Azure OpenAI legacy request options: %w", err)
	}
	options = c.ApplyDefaults(options)
	if request.Generation.Model != "" {
		options.Model = request.Generation.Model
	}
	if err := validatePortableIntent(request.Generation); err != nil {
		return nil, err
	}
	requestedModel := options.Model
	options.Model = modelcatalog.Resolve("openai", options.Model)
	caps := providers.LookupModelCapabilities("openai", options.Model)
	if request.Generation.ReasoningEffort.Mode == core.AIParameterSet && caps.ReasoningStyle != "openai" {
		return nil, &core.AIRequestFeatureError{
			ClientType: "*azureopenai.Client", Feature: "generation.reasoning_effort",
		}
	}
	if request.Generation.ResponseFormat.Mode == core.AIParameterSet && !caps.SupportsJSONMode {
		return nil, &core.AIRequestFeatureError{
			ClientType: "*azureopenai.Client", Feature: "generation.response_format",
		}
	}
	reasoningEffort := options.ReasoningEffort
	if reasoningEffort == "" {
		reasoningEffort = c.defaultReasoningEffort
	}
	if reasoningEffort != "" && caps.ReasoningStyle != "openai" {
		providers.LogTranslationDegraded(ctx, c.Logger, c.providerAlias, options.Model, "reasoning_effort_stripped", "reasoning_effort")
		reasoningEffort = ""
	}
	options.ReasoningEffort = reasoningEffort
	if options.ResponseFormat != "" && !caps.SupportsJSONMode {
		providers.LogTranslationDegraded(ctx, c.Logger, c.providerAlias, options.Model, "response_format_stripped", "response_format")
		options.ResponseFormat = ""
	}
	clientDefaults, err := providers.CloneAIOptions(&core.AIOptions{
		Extra: c.defaultExtra, Headers: c.defaultHeaders,
	})
	if err != nil {
		return nil, fmt.Errorf("clone Azure OpenAI client request defaults: %w", err)
	}
	options.Extra = filterAzureExtraFields(
		ctx, c.Logger, c.providerAlias, options.Model, caps,
		providers.MergeAnyMaps(clientDefaults.Extra, options.Extra),
	)
	options.Headers = mergeHeaders(clientDefaults.Headers, options.Headers)
	var apiKeyConflicts []string
	options.Headers, apiKeyConflicts = stripAPIKeyHeader(options.Headers)
	operation := "generate"
	if stream {
		operation = "stream"
	}
	return &requestSemantics{
		Request: request, Options: options, RequestedModel: requestedModel,
		SemanticModel: options.Model, ProviderAlias: c.providerAlias,
		Surface: "chat-completions", Operation: operation, Purpose: request.Purpose,
		Capabilities: caps, ReasoningModel: openaiwire.IsReasoningModel(options.Model),
		APIKeyConflicts: apiKeyConflicts,
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
		return &core.AIRequestFeatureError{ClientType: "*azureopenai.Client", Feature: "generation.top_k"}
	}
	if generation.MaxTokens.Mode == core.AIParameterSet && generation.MaxTokens.Value <= 0 {
		return errors.New("generation.max_tokens must be positive")
	}
	return nil
}

func (c *Client) buildPolicyRequest(
	ctx context.Context,
	semantics *requestSemantics,
	profile openaiwire.RequestProfile,
	stream bool,
) (*preparedRequest, error) {
	wireOptions := *semantics.Options
	wireOptions.Model = semantics.RequestedModel
	wireRequest := core.NewAIRequestFromLegacy(semantics.Request.Prompt, semantics.Purpose, &wireOptions)
	wireRequest.Generation = semantics.Request.Generation
	wireRequest.Patches = semantics.Request.Patches
	draft, err := c.codec.BuildDraftWithProfile(wireRequest, profile, stream)
	if err != nil {
		return nil, err
	}
	if err := draft.BindIdentity(providerName, semantics.ProviderAlias); err != nil {
		return nil, err
	}
	if draft.Info().RequestedModel != semantics.RequestedModel {
		return nil, errors.New("azure OpenAI wire requested-model invariant was not preserved")
	}
	policyDraft := &azureDraft{Draft: draft}
	conflicts := append(draft.ProtectedHeaderConflicts(), semantics.APIKeyConflicts...)
	sort.Strings(conflicts)
	prepared := &preparedRequest{
		SemanticModel:      semantics.SemanticModel,
		ProtectedConflicts: conflicts,
	}
	if c.requestPolicy == nil {
		return prepared, errors.New("azure OpenAI request policy engine is not configured")
	}
	report, err := c.requestPolicy.Apply(ctx, policyDraft, semantics.Request.Patches)
	if report != nil {
		report.Adjustments = append(draft.Adjustments(), report.Adjustments...)
		prepared.Report = report
	}
	if err != nil {
		return prepared, err
	}
	prepared.Body, err = c.codec.Encode(draft)
	if err != nil {
		return prepared, err
	}
	headers := make(http.Header)
	headers.Set("Content-Type", "application/json")
	if stream {
		headers.Set("Accept", "text/event-stream")
	}
	providers.ApplyLegacyHeaders(headers, azureProtectedHeaders(stream), draft.Headers(), nil)
	prepared.Headers = headers
	return prepared, nil
}

func filterAzureExtraFields(
	ctx context.Context,
	logger core.Logger,
	providerAlias string,
	model string,
	caps providers.ModelCapabilities,
	extra map[string]interface{},
) map[string]interface{} {
	if len(extra) == 0 {
		return nil
	}
	filtered := make(map[string]interface{}, len(extra))
	for key, value := range extra {
		switch strings.ToLower(key) {
		case "reasoning":
			providers.LogTranslationDegraded(ctx, logger, providerAlias, model, "extra_reasoning_stripped", "reasoning")
			continue
		case "reasoning_effort":
			if caps.ReasoningStyle != "openai" {
				providers.LogTranslationDegraded(ctx, logger, providerAlias, model, "extra_reasoning_effort_stripped", "reasoning_effort")
				continue
			}
		case "response_format":
			if !caps.SupportsJSONMode {
				providers.LogTranslationDegraded(ctx, logger, providerAlias, model, "extra_response_format_stripped", "response_format")
				continue
			}
		}
		filtered[key] = value
	}
	if len(filtered) == 0 {
		return nil
	}
	return filtered
}

func mergeHeaders(defaultHeaders, requestHeaders map[string]string) map[string]string {
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

func stripAPIKeyHeader(headers map[string]string) (map[string]string, []string) {
	if len(headers) == 0 {
		return headers, nil
	}
	filtered := make(map[string]string, len(headers))
	conflicts := make([]string, 0, 1)
	for name, value := range headers {
		if strings.EqualFold(name, "api-key") {
			conflicts = append(conflicts, "Api-Key")
			continue
		}
		filtered[name] = value
	}
	return filtered, conflicts
}

func azureProtectedHeaders(stream bool) map[string]struct{} {
	protected := map[string]struct{}{
		"api-key": {}, "authorization": {}, "content-type": {},
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
			"operation": "ai_request_policy", "provider": providerName,
			"provider_alias":  c.providerAlias,
			"ignored_headers": strings.Join(prepared.ProtectedConflicts, ","),
			"migration":       "remove provider-managed names from WithHeaders and AIOptions.Headers",
		}
		providers.AddObservationRequestID(ctx, fields)
		c.Logger.WarnWithContext(ctx, "Azure OpenAI legacy protected headers ignored", fields)
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
