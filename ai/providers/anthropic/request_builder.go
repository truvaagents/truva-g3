package anthropic

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"

	"github.com/truvaagents/truva-g3/ai/providers"
	"github.com/truvaagents/truva-g3/ai/requestpolicy"
	"github.com/truvaagents/truva-g3/core"
)

const samplingAdjustmentRule = "anthropic-adaptive-thinking-sampling-v1"

type preparedRequest struct {
	Model                string
	Body                 []byte
	Headers              http.Header
	Report               *core.AIRequestReport
	Adjustments          []core.AIRequestAdjustment
	SamplingPolicy       samplingPolicy
	RequestedTemperature float32
	TemperatureSent      bool
	LegacySamplingExtras []string
	ProtectedConflicts   []string
}

func (c *Client) prepareRequest(prompt string, supplied *core.AIOptions, stream bool) (*preparedRequest, error) {
	return c.prepareAIRequest(context.Background(), core.NewAIRequestFromLegacy(prompt, "", supplied), stream)
}

func (c *Client) prepareAIRequest(ctx context.Context, supplied *core.AIRequest, stream bool) (*preparedRequest, error) {
	request, err := core.CloneAIRequest(supplied)
	if err != nil {
		return nil, fmt.Errorf("clone Anthropic AI request: %w", err)
	}
	options, err := providers.CloneAIOptions(request.LegacyOptions())
	if err != nil {
		return nil, fmt.Errorf("clone Anthropic legacy request options: %w", err)
	}
	options = c.ApplyDefaults(options)
	if request.Generation.Model != "" {
		options.Model = request.Generation.Model
	}
	requestedModel := options.Model
	options.Model = resolveModel(options.Model)

	body := map[string]interface{}{
		"model":       options.Model,
		"messages":    []Message{{Role: "user", Content: request.Prompt}},
		"max_tokens":  options.MaxTokens,
		"temperature": options.Temperature,
	}
	if options.SystemPrompt != "" {
		body["system"] = options.SystemPrompt
	}
	if stream {
		body["stream"] = true
	}
	if options.ResponseFormat != "" {
		body["response_format"] = options.ResponseFormat
	}
	clientDefaults, err := providers.CloneAIOptions(&core.AIOptions{Extra: c.defaultExtra})
	if err != nil {
		return nil, fmt.Errorf("clone Anthropic client request extras: %w", err)
	}
	mergedExtras := providers.MergeAnyMaps(clientDefaults.Extra, options.Extra)
	for key, value := range mergedExtras {
		if _, structural := body[key]; !structural {
			body[key] = value
		}
	}

	explicit := make(map[string]struct{})
	if err := applyGenerationSets(body, request.Generation, explicit); err != nil {
		return nil, err
	}
	requestedTemperature := options.Temperature
	if request.Generation.Temperature.Mode == core.AIParameterSet {
		requestedTemperature = request.Generation.Temperature.Value
	}

	protectedHeaders := anthropicProtectedHeaders(stream)
	protectedConflicts := protectedHeaderConflicts(protectedHeaders, c.defaultHeaders, options.Headers)
	eligibleHeaders := eligibleLegacyHeaders(protectedHeaders, c.defaultHeaders, options.Headers)
	operation := "generate"
	if stream {
		operation = "stream"
	}
	document, err := requestpolicy.NewDocument(requestpolicy.DocumentConfig{
		Info: requestpolicy.RequestInfo{
			Provider:       "anthropic",
			ProviderAlias:  c.providerAlias,
			Surface:        "messages",
			Operation:      operation,
			Purpose:        request.Purpose,
			RequestedModel: requestedModel,
			ResolvedModel:  options.Model,
		},
		Body:                 body,
		Headers:              eligibleHeaders,
		ProtectedPaths:       []string{"/model", "/messages", "/stream"},
		ProtectedHeaders:     protectedHeaderNames(protectedHeaders),
		CaseInsensitivePaths: []string{"/temperature", "/top_p", "/top_k"},
	})
	if err != nil {
		return nil, fmt.Errorf("create Anthropic request draft: %w", err)
	}
	draft := &anthropicDraft{Document: document, explicit: explicit, stream: stream}
	portableAdjustments, err := applyGenerationOmits(draft, request.Generation)
	if err != nil {
		return nil, err
	}

	policy := samplingPolicyForModel(options.Model)
	prepared := &preparedRequest{
		Model:                options.Model,
		SamplingPolicy:       policy,
		RequestedTemperature: requestedTemperature,
		LegacySamplingExtras: samplingExtraPaths(mergedExtras),
		ProtectedConflicts:   protectedConflicts,
	}
	if c.requestPolicy == nil {
		return prepared, errors.New("anthropic request policy engine is not configured")
	}
	report, err := c.requestPolicy.Apply(ctx, draft, request.Patches)
	if report != nil {
		report.Adjustments = append(portableAdjustments, report.Adjustments...)
		prepared.Report = report
		prepared.Adjustments = report.Adjustments
	}
	_, prepared.TemperatureSent = draft.Get("/temperature")
	if err != nil {
		return prepared, err
	}

	encoded, err := json.Marshal(draft.Body())
	if err != nil {
		return prepared, fmt.Errorf("marshal Anthropic request: %w", err)
	}

	headers := make(http.Header)
	headers.Set("Content-Type", "application/json")
	if c.credentialSource == nil && c.apiKey != "" {
		headers.Set("x-api-key", c.apiKey)
	}
	headers.Set("anthropic-version", APIVersion)
	if stream {
		headers.Set("Accept", "text/event-stream")
	}
	providers.ApplyLegacyHeaders(headers, protectedHeaders, draft.Headers(), nil)
	prepared.Body = encoded
	prepared.Headers = headers
	return prepared, nil
}

func applyGenerationSets(body map[string]interface{}, generation core.AIGenerationOptions, explicit map[string]struct{}) error {
	if err := setGenerationParameter(body, "/temperature", "temperature", generation.Temperature.Mode, generation.Temperature.Value, explicit); err != nil {
		return err
	}
	if err := setGenerationParameter(body, "/top_p", "top_p", generation.TopP.Mode, generation.TopP.Value, explicit); err != nil {
		return err
	}
	if err := setGenerationParameter(body, "/top_k", "top_k", generation.TopK.Mode, generation.TopK.Value, explicit); err != nil {
		return err
	}
	if err := setGenerationParameter(body, "/max_tokens", "max_tokens", generation.MaxTokens.Mode, generation.MaxTokens.Value, explicit); err != nil {
		return err
	}
	if err := setGenerationParameter(body, "/system", "system", generation.SystemPrompt.Mode, generation.SystemPrompt.Value, explicit); err != nil {
		return err
	}
	if generation.ReasoningEffort.Mode == core.AIParameterSet {
		return &core.AIRequestFeatureError{ClientType: "*anthropic.Client", Feature: "generation.reasoning_effort"}
	}
	if generation.ReasoningEffort.Mode != core.AIParameterInherit && generation.ReasoningEffort.Mode != core.AIParameterOmit {
		return fmt.Errorf("invalid generation.reasoning_effort mode %d", generation.ReasoningEffort.Mode)
	}
	return setGenerationParameter(body, "/response_format", "response_format", generation.ResponseFormat.Mode, generation.ResponseFormat.Value, explicit)
}

func setGenerationParameter[T any](
	body map[string]interface{},
	path string,
	key string,
	mode core.AIParameterMode,
	value T,
	explicit map[string]struct{},
) error {
	switch mode {
	case core.AIParameterInherit, core.AIParameterOmit:
		return nil
	case core.AIParameterSet:
		if path == "/temperature" || path == "/top_p" || path == "/top_k" {
			for existing := range body {
				if strings.EqualFold(existing, key) {
					delete(body, existing)
				}
			}
		}
		body[key] = value
		explicit[path] = struct{}{}
		return nil
	default:
		return fmt.Errorf("invalid generation parameter mode %d for %s", mode, path)
	}
}

func applyGenerationOmits(draft *anthropicDraft, generation core.AIGenerationOptions) ([]core.AIRequestAdjustment, error) {
	parameters := []struct {
		path string
		mode core.AIParameterMode
	}{
		{path: "/temperature", mode: generation.Temperature.Mode},
		{path: "/top_p", mode: generation.TopP.Mode},
		{path: "/top_k", mode: generation.TopK.Mode},
		{path: "/max_tokens", mode: generation.MaxTokens.Mode},
		{path: "/system", mode: generation.SystemPrompt.Mode},
		{path: "/response_format", mode: generation.ResponseFormat.Mode},
	}
	adjustments := make([]core.AIRequestAdjustment, 0, len(parameters))
	for _, parameter := range parameters {
		if parameter.mode != core.AIParameterOmit {
			continue
		}
		_, existed := draft.Get(parameter.path)
		if err := draft.Remove(parameter.path); err != nil {
			return nil, fmt.Errorf("apply portable omit %s: %w", parameter.path, err)
		}
		if existed {
			adjustments = append(adjustments, core.AIRequestAdjustment{
				Source: "portable",
				Rule:   "generation-omit",
				Path:   parameter.path,
				Action: "remove",
				Reason: "explicit portable omit",
			})
		}
	}
	return adjustments, nil
}

func anthropicProtectedHeaders(stream bool) map[string]struct{} {
	protected := map[string]struct{}{
		"content-type":      {},
		"x-api-key":         {},
		"anthropic-version": {},
	}
	if stream {
		protected["accept"] = struct{}{}
	}
	return protected
}

func protectedHeaderNames(protected map[string]struct{}) []string {
	names := make([]string, 0, len(protected))
	for name := range protected {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func eligibleLegacyHeaders(protected map[string]struct{}, defaultHeaders, requestHeaders map[string]string) map[string]string {
	headers := make(http.Header)
	for _, source := range []map[string]string{defaultHeaders, requestHeaders} {
		for _, name := range sortedStringKeys(source) {
			if _, isProtected := protected[strings.ToLower(name)]; isProtected {
				continue
			}
			headers.Set(name, source[name])
		}
	}
	result := make(map[string]string, len(headers))
	for name, values := range headers {
		if len(values) > 0 {
			result[name] = values[len(values)-1]
		}
	}
	return result
}

func sortedStringKeys(source map[string]string) []string {
	keys := make([]string, 0, len(source))
	for key := range source {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func protectedHeaderConflicts(protected map[string]struct{}, sources ...map[string]string) []string {
	conflicts := make(map[string]struct{})
	for _, source := range sources {
		for name := range source {
			if _, isProtected := protected[strings.ToLower(name)]; isProtected {
				conflicts[http.CanonicalHeaderKey(name)] = struct{}{}
			}
		}
	}

	result := make([]string, 0, len(conflicts))
	for name := range conflicts {
		result = append(result, name)
	}
	sort.Strings(result)
	return result
}

func samplingExtraPaths(extras map[string]interface{}) []string {
	paths := make(map[string]struct{})
	for key := range extras {
		switch strings.ToLower(key) {
		case "temperature":
			paths["/temperature"] = struct{}{}
		case "top_p":
			paths["/top_p"] = struct{}{}
		case "top_k":
			paths["/top_k"] = struct{}{}
		}
	}

	result := make([]string, 0, len(paths))
	for path := range paths {
		result = append(result, path)
	}
	sort.Strings(result)
	return result
}

func (c *Client) recordRequestPreparation(ctx context.Context, span core.Span, prepared *preparedRequest) {
	if prepared == nil {
		return
	}

	temperatureSent := prepared.TemperatureSent
	span.SetAttribute("ai.sampling.policy", prepared.SamplingPolicy.String())
	span.SetAttribute("ai.temperature.requested", float64(prepared.RequestedTemperature))
	span.SetAttribute("ai.temperature.sent", temperatureSent)
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
	if paths := removedBuiltInSamplingPaths(prepared.Adjustments); len(paths) > 0 {
		parameters := make([]string, 0, len(paths))
		for _, path := range paths {
			parameters = append(parameters, strings.TrimPrefix(path, "/"))
		}
		span.SetAttribute("ai.parameters.omitted", strings.Join(parameters, ","))
		span.SetAttribute("ai.parameter_adjustment.reason", "model_sampling_parameters_deprecated")
	}

	if c.Logger != nil {
		fields := map[string]interface{}{
			"operation":        "ai_request_policy",
			"provider":         "anthropic",
			"model":            prepared.Model,
			"sampling_policy":  prepared.SamplingPolicy.String(),
			"temperature_sent": temperatureSent,
		}
		if paths := removedBuiltInSamplingPaths(prepared.Adjustments); len(paths) > 0 {
			fields["adjustment_rule"] = samplingAdjustmentRule
			fields["adjusted_paths"] = strings.Join(paths, ",")
		}
		fields["adjustment_count"] = len(prepared.Adjustments)
		c.Logger.DebugWithContext(ctx, "Anthropic request policy evaluated", fields)

		if prepared.SamplingPolicy == samplingOmitted && len(prepared.LegacySamplingExtras) > 0 {
			c.Logger.WarnWithContext(ctx, "Anthropic legacy sampling extras omitted for resolved model", map[string]interface{}{
				"operation":       "ai_request_policy",
				"provider":        "anthropic",
				"model":           prepared.Model,
				"adjustment_rule": samplingAdjustmentRule,
				"adjusted_paths":  strings.Join(prepared.LegacySamplingExtras, ","),
			})
		}
		if len(prepared.ProtectedConflicts) > 0 {
			c.Logger.WarnWithContext(ctx, "Anthropic legacy protected headers ignored", map[string]interface{}{
				"operation":       "ai_request_policy",
				"provider":        "anthropic",
				"model":           prepared.Model,
				"ignored_headers": strings.Join(prepared.ProtectedConflicts, ","),
				"migration":       "remove provider-managed names from WithHeaders and AIOptions.Headers",
			})
		}
	}
}

func adjustmentPaths(adjustments []core.AIRequestAdjustment) []string {
	paths := make([]string, 0, len(adjustments))
	for _, adjustment := range adjustments {
		paths = append(paths, adjustment.Path)
	}
	return paths
}

func removedBuiltInSamplingPaths(adjustments []core.AIRequestAdjustment) []string {
	paths := make([]string, 0, len(adjustments))
	for _, adjustment := range adjustments {
		if adjustment.Source != "built-in-rule" || adjustment.Action != "remove" || !strings.HasPrefix(adjustment.Rule, samplingAdjustmentRule) {
			continue
		}
		switch adjustment.Path {
		case "/temperature", "/top_p", "/top_k":
			paths = append(paths, adjustment.Path)
		}
	}
	return paths
}
