package openai

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"

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
	Codec              openaiwire.Codec
	ProtectedConflicts []string
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

func (c *Client) prepareAIRequest(
	ctx context.Context,
	supplied *core.AIRequest,
	stream bool,
) (*preparedRequest, error) {
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

	wireOptions := *options
	wireOptions.Model = requestedModel
	wireRequest := core.NewAIRequestFromLegacy(request.Prompt, request.Purpose, &wireOptions)
	wireRequest.Generation = request.Generation
	wireRequest.Patches = request.Patches
	codec, err := openaiwire.NewConfiguredCodec(openaiwire.Config{
		SurfaceVersion:           openaiwire.DefaultSurfaceVersion,
		ReasoningTokenMultiplier: c.ReasoningTokenMultiplier,
		DefaultReasoningEffort:   reasoningEffort,
		ForceReasoningObject:     providerAlias == "openai.ollama",
	})
	if err != nil {
		return nil, fmt.Errorf("create OpenAI wire codec: %w", err)
	}
	draft, err := codec.BuildDraft(wireRequest, options.Model, stream)
	if err != nil {
		return nil, err
	}
	if err := draft.BindIdentity("openai", providerAlias); err != nil {
		return nil, err
	}
	// Preserve the unresolved caller-facing model in selectors and reports.
	if draft.Info().RequestedModel != requestedModel {
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
