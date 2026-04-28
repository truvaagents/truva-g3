// Package orchestration provides intelligent parameter binding for multi-step workflows.
//
// # LLM-First Parameter Resolution Design
//
// This file implements hybrid resolution where LLM micro-resolution is the PRIMARY
// approach for semantic parameter binding. Auto-wiring is now limited to trivial
// matching only (exact names, case-insensitive) to avoid unnecessary LLM calls for
// obvious cases.
//
// Design principles:
//  1. Framework agnosticism: No domain-specific heuristics (weather, currency, etc.)
//  2. LLM-powered semantics: All semantic understanding (e.g., "latitude" → "lat",
//     "country" → "EUR") is delegated to LLM micro-resolution
//  3. Cost optimization: Only use auto-wiring for trivial cases where names match exactly
//
// What auto-wiring handles (no LLM needed):
//   - Exact name match: "lat" → "lat"
//   - Case-insensitive match: "LAT" → "lat"
//   - Nested extraction: {code: "EUR"} → "EUR"
//   - Type coercion: "35.6" → 35.6
//
// What LLM micro-resolution handles:
//   - Semantic equivalence: "latitude" → "lat"
//   - Domain inference: "France" → "EUR" (currency)
//   - Complex mappings: Any case where names don't match
//
// This ensures the framework handles novel domains automatically without hardcoded
// mappings, while still avoiding wasteful LLM calls for trivial parameter binding.
package orchestration

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/truvaagents/truva-g3/core"
)

// HybridResolver combines auto-wiring and micro-resolution for parameter binding.
// It first attempts to auto-wire parameters by name matching, then falls back to
// LLM-based micro-resolution for any parameters that couldn't be matched.
type HybridResolver struct {
	autoWirer     *AutoWirer
	microResolver *MicroResolver
	logger        core.Logger

	// Configuration
	enableMicroResolution bool // Whether to use LLM fallback (default: true)
}

// HybridResolverOption configures a HybridResolver
type HybridResolverOption func(*HybridResolver)

// WithMicroResolution enables or disables LLM-based micro-resolution fallback
func WithMicroResolution(enabled bool) HybridResolverOption {
	return func(h *HybridResolver) {
		h.enableMicroResolution = enabled
	}
}

// NewHybridResolver creates a new hybrid resolver
func NewHybridResolver(aiClient core.AIClient, logger core.Logger, opts ...HybridResolverOption) *HybridResolver {
	h := &HybridResolver{
		autoWirer:             NewAutoWirer(logger),
		logger:                logger,
		enableMicroResolution: true, // Default: enable LLM fallback
	}

	// Create micro-resolver if AI client is provided
	if aiClient != nil {
		h.microResolver = NewMicroResolver(aiClient, logger)
	}

	// Apply options
	for _, opt := range opts {
		opt(h)
	}

	return h
}

// SetMicroResolverAIOptions propagates the per-phase AI options override to the internal MicroResolver.
func (h *HybridResolver) SetMicroResolverAIOptions(opts *AIOptionsOverride) {
	if h.microResolver != nil {
		h.microResolver.SetAIOptionsOverride(opts)
	}
}

// Deprecated compatibility helpers kept while tests/examples migrate.
func (h *HybridResolver) SetMicroResolutionModel(model string) {
	if h.microResolver != nil {
		h.microResolver.SetModel(model)
	}
}

func (h *HybridResolver) SetMicroResolutionMaxTokens(maxTokens int) {
	if h.microResolver != nil {
		h.microResolver.SetMaxTokens(maxTokens)
	}
}

// ResolveParameters resolves parameters from dependency results to target capability.
// This is the main entry point for parameter resolution in the executor.
//
// Resolution strategy (LLM-first design):
//  1. Auto-wire trivial matches (exact name, case-insensitive) - no LLM cost
//  2. If required parameters remain unmapped → use LLM micro-resolution
//  3. LLM handles all semantic understanding (e.g., "latitude" → "lat")
//
// The stepID parameter associates any LLM calls (micro_resolution) with the
// execution step for DAG visualization. Pass empty string if not step-specific.
//
// The stepInstruction parameter provides context about what this step is trying to do.
// This is critical for ordinal resolution - when the instruction mentions "first",
// "second", "third" etc., the LLM needs this context to extract the correct item
// from the source data. Pass empty string if not available.
//
// This ensures domain-agnostic behavior while avoiding unnecessary LLM calls
// for trivial parameter mappings.
func (h *HybridResolver) ResolveParameters(
	ctx context.Context,
	dependencyResults map[string]*StepResult,
	targetCapability *EnhancedCapability,
	stepID string,
	stepInstruction string,
) (*HybridResolutionResult, error) {
	// Collect source data from all dependencies
	sourceData := h.collectSourceData(dependencyResults)

	// Collect dependency step IDs for metadata
	depStepIDs := make([]string, 0, len(dependencyResults))
	for id := range dependencyResults {
		depStepIDs = append(depStepIDs, id)
	}

	if len(sourceData) == 0 {
		h.logDebug("No source data available for parameter resolution", map[string]interface{}{
			"capability": targetCapability.Name,
		})
		return &HybridResolutionResult{
			Metadata: &ResolutionMetadata{
				Parameters:         []ParameterResolution{},
				SourceDataKeyCount: 0,
				DependencyStepIDs:  depStepIDs,
			},
		}, nil
	}

	// Phase 1: Try auto-wiring (fast, no LLM cost)
	autoResult := h.autoWirer.AutoWireParameters(sourceData, targetCapability.Parameters)

	h.logDebug("Auto-wiring result", map[string]interface{}{
		"capability":  targetCapability.Name,
		"wired_count": len(autoResult.Parameters),
		"unmapped":    autoResult.Unmapped,
		"source_keys": getMapKeys(sourceData),
		"duration_us": autoResult.DurationUs,
	})

	// Build base metadata from auto-wiring
	metadata := &ResolutionMetadata{
		Parameters:           autoResult.Resolved,
		AutoWiredCount:       len(autoResult.Resolved),
		AutoWiringDurationUs: autoResult.DurationUs,
		SourceDataKeyCount:   len(sourceData),
		DependencyStepIDs:    depStepIDs,
	}

	// Phase 2: If all required parameters resolved, we're done!
	if len(autoResult.Unmapped) == 0 {
		h.logInfo("All parameters auto-wired successfully", map[string]interface{}{
			"capability":  targetCapability.Name,
			"params":      autoResult.Parameters,
			"duration_us": autoResult.DurationUs,
		})
		return &HybridResolutionResult{
			Parameters: autoResult.Parameters,
			Metadata:   metadata,
		}, nil
	}

	// Check if all unmapped are optional
	allUnmappedOptional := true
	for _, paramName := range autoResult.Unmapped {
		for _, param := range targetCapability.Parameters {
			if param.Name == paramName && param.Required {
				allUnmappedOptional = false
				break
			}
		}
	}

	if allUnmappedOptional {
		h.logInfo("All required parameters auto-wired, optional params unmapped", map[string]interface{}{
			"capability":        targetCapability.Name,
			"params":            autoResult.Parameters,
			"optional_unmapped": autoResult.Unmapped,
		})
		return &HybridResolutionResult{
			Parameters: autoResult.Parameters,
			Metadata:   metadata,
		}, nil
	}

	// Phase 3: Use micro-resolution for remaining required parameters
	if !h.enableMicroResolution || h.microResolver == nil {
		h.logWarn("Micro-resolution disabled or unavailable, returning partial results", map[string]interface{}{
			"capability": targetCapability.Name,
			"params":     autoResult.Parameters,
			"unmapped":   autoResult.Unmapped,
		})
		return &HybridResolutionResult{
			Parameters: autoResult.Parameters,
			Metadata:   metadata,
		}, nil
	}

	h.logInfo("Using micro-resolution for unmapped parameters", map[string]interface{}{
		"capability":  targetCapability.Name,
		"unmapped":    autoResult.Unmapped,
		"instruction": stepInstruction,
	})

	// Build hint with step instruction context for ordinal resolution
	var hint string
	if stepInstruction != "" {
		hint = fmt.Sprintf(`STEP INSTRUCTION: %s

Need to extract values for required parameters: %v

IMPORTANT: If the step instruction mentions ordinal references like "first", "second", "third", or "Nth", extract the corresponding item from the source data in that position. For example:
- "first AI chip company" → extract the 1st company mentioned
- "second stock" → extract the 2nd stock mentioned
- "third result" → extract the 3rd item

Pay close attention to which specific item the instruction is referring to.`, stepInstruction, autoResult.Unmapped)
	} else {
		hint = fmt.Sprintf("Need to extract values for required parameters: %v", autoResult.Unmapped)
	}

	microStartTime := time.Now()
	resolved, err := h.microResolver.ResolveParameters(ctx, sourceData, targetCapability, hint, stepID)
	microDurationMs := time.Since(microStartTime).Milliseconds()

	if err != nil {
		// Micro-resolution failed, return what we have
		metadata.MicroResolutionDurationMs = microDurationMs
		h.logWarn("Micro-resolution failed, using partial auto-wired results", map[string]interface{}{
			"error":       err.Error(),
			"capability":  targetCapability.Name,
			"params":      autoResult.Parameters,
			"llm_call_ms": microDurationMs,
		})
		return &HybridResolutionResult{
			Parameters: autoResult.Parameters,
			Metadata:   metadata,
		}, nil
	}

	// Track micro-resolution timing
	metadata.MicroResolutionDurationMs = microDurationMs

	// Merge results (auto-wired takes priority to avoid overwriting with LLM guesses)
	params := autoResult.Parameters
	for k, v := range resolved {
		if _, exists := params[k]; !exists {
			params[k] = v
			metadata.Parameters = append(metadata.Parameters, ParameterResolution{
				Name:      k,
				Layer:     "micro_resolution",
				MatchType: "semantic",
				Value:     v,
			})
			metadata.MicroResolvedCount++
		}
	}

	h.logInfo("Hybrid resolution completed", map[string]interface{}{
		"capability":       targetCapability.Name,
		"final_params":     params,
		"auto_wired":       metadata.AutoWiredCount,
		"micro_resolved":   metadata.MicroResolvedCount,
		"auto_wire_us":     metadata.AutoWiringDurationUs,
		"micro_resolve_ms": metadata.MicroResolutionDurationMs,
	})

	return &HybridResolutionResult{
		Parameters: params,
		Metadata:   metadata,
	}, nil
}

// collectSourceData merges all dependency results into a single map
func (h *HybridResolver) collectSourceData(dependencyResults map[string]*StepResult) map[string]interface{} {
	sourceData := make(map[string]interface{})

	for stepID, result := range dependencyResults {
		if result == nil || result.Response == "" {
			continue
		}
		if !result.Success {
			h.logDebug("Skipping failed step in source data", map[string]interface{}{
				"step_id": stepID,
			})
			continue
		}

		var parsed map[string]interface{}
		if err := json.Unmarshal([]byte(result.Response), &parsed); err != nil {
			h.logWarn("Failed to parse step response for parameter resolution", map[string]interface{}{
				"step_id": stepID,
				"error":   err.Error(),
			})
			continue
		}

		// Fix double-serialization before merging (see LARGE_RESULT_DATA_MANAGEMENT.md §4.6)
		if deserialized, ok := deserializeStringValues(parsed).(map[string]interface{}); ok {
			parsed = deserialized
		}
		// Merge into sourceData (later steps may override earlier for same keys)
		for k, v := range parsed {
			sourceData[k] = v
		}
	}

	return sourceData
}

// SetLogger sets the logger for the hybrid resolver and its sub-components
// The component is always set to "framework/orchestration" to ensure proper log attribution
// regardless of which agent or tool is using the orchestration module.
func (h *HybridResolver) SetLogger(logger core.Logger) {
	if logger == nil {
		h.logger = nil
	} else {
		if cal, ok := logger.(core.ComponentAwareLogger); ok {
			h.logger = cal.WithComponent("framework/orchestration")
		} else {
			h.logger = logger
		}
	}
	// Propagate to sub-components (they will apply their own WithComponent)
	if h.autoWirer != nil {
		h.autoWirer.SetLogger(logger)
	}
	if h.microResolver != nil {
		h.microResolver.SetLogger(logger)
	}
}

// SetMicroResolverResultProcessor propagates the ResultProcessor to the MicroResolver
// for trimming source data in micro-resolution prompts (Phase 5).
func (h *HybridResolver) SetMicroResolverResultProcessor(processor ResultProcessor, maxBytes int) {
	if h.microResolver != nil {
		h.microResolver.SetResultProcessor(processor, maxBytes)
	}
}

// SetTelemetry propagates telemetry to the MicroResolver for distributed tracing.
// When set, micro-resolution LLM calls produce distinct spans in Jaeger
// (micro_resolver.value_extraction / micro_resolver.schema_mapping).
func (h *HybridResolver) SetTelemetry(t core.Telemetry) {
	if h.microResolver != nil {
		h.microResolver.SetTelemetry(t)
	}
}

// SetMicroResolverSchemaMappingThreshold propagates the schema-guided mapping threshold
// to the MicroResolver for Phase 10 large-data resolution.
func (h *HybridResolver) SetMicroResolverSchemaMappingThreshold(threshold int) {
	if h.microResolver != nil {
		h.microResolver.SetSchemaMappingThreshold(threshold)
	}
}

// ResolveSemanticValue resolves a single value semantically using LLM inference.
// This is used when template substitution fails (path doesn't exist) but we need to
// infer the intended value from available data.
// Example: template "{{step-2.response.data.country.currency}}" fails because
// geocoding returns country:"France" (string), not country:{currency:"EUR"}.
// This method can infer "EUR" from "France" using LLM.
//
// The stepID parameter associates any LLM calls with the execution step for
// DAG visualization. Pass empty string if not step-specific.
func (h *HybridResolver) ResolveSemanticValue(
	ctx context.Context,
	sourceData map[string]interface{},
	paramName string,
	paramHint string,
	expectedType string,
	stepID string,
) (interface{}, error) {
	if h.microResolver == nil || !h.enableMicroResolution {
		return nil, fmt.Errorf("micro-resolution not available")
	}

	// Create a minimal capability for single-value extraction
	targetCap := &EnhancedCapability{
		Name: "extract_value",
		Parameters: []Parameter{
			{
				Name:        paramName,
				Type:        expectedType,
				Required:    true,
				Description: paramHint,
			},
		},
	}

	h.logInfo("Semantic value resolution starting", map[string]interface{}{
		"param_name":  paramName,
		"param_hint":  paramHint,
		"source_keys": getMapKeys(sourceData),
	})

	resolved, err := h.microResolver.ResolveParameters(ctx, sourceData, targetCap, paramHint, stepID)
	if err != nil {
		h.logWarn("Semantic value resolution failed", map[string]interface{}{
			"error":      err.Error(),
			"param_name": paramName,
		})
		return nil, err
	}

	if val, ok := resolved[paramName]; ok {
		h.logInfo("Semantic value resolved successfully", map[string]interface{}{
			"param_name":     paramName,
			"resolved_value": val,
		})
		return val, nil
	}

	return nil, fmt.Errorf("parameter %s not found in micro-resolution result", paramName)
}

// Logging helpers
func (h *HybridResolver) logDebug(msg string, fields map[string]interface{}) {
	if h.logger != nil {
		h.logger.Debug(msg, fields)
	}
}

func (h *HybridResolver) logInfo(msg string, fields map[string]interface{}) {
	if h.logger != nil {
		h.logger.Info(msg, fields)
	}
}

func (h *HybridResolver) logWarn(msg string, fields map[string]interface{}) {
	if h.logger != nil {
		h.logger.Warn(msg, fields)
	}
}
