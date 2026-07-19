package anthropic

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"

	"github.com/truvaagents/truva-g3/ai/providers"
	"github.com/truvaagents/truva-g3/core"
)

const samplingAdjustmentRule = "anthropic-adaptive-thinking-sampling-v1"

type preparedRequest struct {
	Model                string
	Body                 []byte
	Headers              http.Header
	Adjustments          []requestAdjustment
	SamplingPolicy       samplingPolicy
	RequestedTemperature float32
	LegacySamplingExtras []string
	ProtectedConflicts   []string
}

type requestAdjustment struct {
	Rule   string
	Path   string
	Action string
	Reason string
}

func (c *Client) prepareRequest(prompt string, supplied *core.AIOptions, stream bool) (*preparedRequest, error) {
	options, err := providers.CloneAIOptions(supplied)
	if err != nil {
		return nil, fmt.Errorf("clone Anthropic request options: %w", err)
	}
	options = c.ApplyDefaults(options)
	options.Model = resolveModel(options.Model)

	body := map[string]interface{}{
		"model":       options.Model,
		"messages":    []Message{{Role: "user", Content: prompt}},
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
	mergedExtras := providers.MergeAnyMaps(c.defaultExtra, options.Extra)
	for key, value := range mergedExtras {
		if _, structural := body[key]; !structural {
			body[key] = value
		}
	}

	policy := samplingPolicyForModel(options.Model)
	var adjustments []requestAdjustment
	if policy == samplingOmitted {
		removedPaths := deleteKeyFold(body, "temperature", "top_p", "top_k")
		for _, path := range removedPaths {
			adjustments = append(adjustments, requestAdjustment{
				Rule:   samplingAdjustmentRule,
				Path:   path,
				Action: "remove",
				Reason: "resolved model rejects explicit sampling controls",
			})
		}
	}

	encoded, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("marshal Anthropic request: %w", err)
	}

	headers := make(http.Header)
	headers.Set("Content-Type", "application/json")
	headers.Set("x-api-key", c.apiKey)
	headers.Set("anthropic-version", APIVersion)
	protectedHeaders := anthropicProtectedHeaders(stream)
	if stream {
		headers.Set("Accept", "text/event-stream")
	}
	protectedConflicts := protectedHeaderConflicts(protectedHeaders, c.defaultHeaders, options.Headers)
	providers.ApplyLegacyHeaders(headers, protectedHeaders, c.defaultHeaders, options.Headers)

	return &preparedRequest{
		Model:                options.Model,
		Body:                 encoded,
		Headers:              headers,
		Adjustments:          adjustments,
		SamplingPolicy:       policy,
		RequestedTemperature: options.Temperature,
		LegacySamplingExtras: samplingExtraPaths(mergedExtras),
		ProtectedConflicts:   protectedConflicts,
	}, nil
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

	temperatureSent := prepared.SamplingPolicy != samplingOmitted
	span.SetAttribute("ai.sampling.policy", prepared.SamplingPolicy.String())
	span.SetAttribute("ai.temperature.requested", float64(prepared.RequestedTemperature))
	span.SetAttribute("ai.temperature.sent", temperatureSent)
	if len(prepared.Adjustments) > 0 {
		paths := adjustmentPaths(prepared.Adjustments)
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
		if len(prepared.Adjustments) > 0 {
			fields["adjustment_rule"] = samplingAdjustmentRule
			fields["adjusted_paths"] = strings.Join(adjustmentPaths(prepared.Adjustments), ",")
		}
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

func adjustmentPaths(adjustments []requestAdjustment) []string {
	paths := make([]string, 0, len(adjustments))
	for _, adjustment := range adjustments {
		paths = append(paths, adjustment.Path)
	}
	return paths
}
