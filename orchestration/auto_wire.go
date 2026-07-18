// Package orchestration provides intelligent parameter binding for multi-step workflows.
// This file implements auto-wiring: automatic parameter resolution based on name matching
// and semantic aliases, without requiring LLM involvement.
package orchestration

import (
	"encoding/json"
	"strconv"
	"strings"
	"time"

	"github.com/truvaagents/truva-g3/core"
)

// autoWireMatch holds the result of a single parameter match during auto-wiring.
type autoWireMatch struct {
	Value     interface{}
	MatchType string // "exact", "case_insensitive", "alias", "nested_extraction"
	SourceKey string // The key in source data that matched
}

// SemanticAliases is intentionally empty to maintain framework agnosticism.
// The framework relies on LLM micro-resolution for intelligent parameter binding
// rather than domain-specific hardcoded mappings.
//
// This design ensures:
// 1. Framework remains use-case agnostic (no weather, currency, stock assumptions)
// 2. Parameter binding is flexible and handles novel domains automatically
// 3. Tools self-describe their schemas; the LLM interprets them
//
// Auto-wiring still supports:
// - Exact name matching
// - Case-insensitive matching
// - Nested object extraction
// - Type coercion
//
// For semantic understanding (e.g., "latitude" → "lat"), use HybridResolver
// which falls back to LLM micro-resolution.
var SemanticAliases = map[string][]string{}

// AutoWirer handles automatic parameter resolution from source data
type AutoWirer struct {
	logger core.Logger
}

// NewAutoWirer creates a new auto-wirer instance
func NewAutoWirer(logger core.Logger) *AutoWirer {
	return &AutoWirer{logger: logger}
}

// SetLogger sets the logger for the auto-wirer
// The component is always set to "framework/orchestration" to ensure proper log attribution
// regardless of which agent or tool is using the orchestration module.
func (w *AutoWirer) SetLogger(logger core.Logger) {
	if logger == nil {
		w.logger = nil
	} else {
		if cal, ok := logger.(core.ComponentAwareLogger); ok {
			w.logger = cal.WithComponent("framework/orchestration")
		} else {
			w.logger = logger
		}
	}
}

// AutoWireParameters automatically maps source data to target parameters.
// Returns an AutoWireResult containing the successfully wired parameters,
// per-parameter resolution metadata, unmapped parameter names, and timing.
func (w *AutoWirer) AutoWireParameters(
	sourceData map[string]interface{},
	targetParams []Parameter,
) *AutoWireResult {
	startTime := time.Now()
	params := make(map[string]interface{})
	var resolved []ParameterResolution
	var unmapped []string

	for _, param := range targetParams {
		match, found := w.findMatchingValueWithMetadata(sourceData, param.Name, param.Type)
		if found {
			params[param.Name] = match.Value
			resolved = append(resolved, ParameterResolution{
				Name:      param.Name,
				Layer:     "auto_wire",
				MatchType: match.MatchType,
				SourceKey: match.SourceKey,
				Value:     match.Value,
			})
			if w.logger != nil {
				w.logger.Debug("Auto-wired parameter", map[string]interface{}{
					"param_name":  param.Name,
					"param_type":  param.Type,
					"match_type":  match.MatchType,
					"source_key":  match.SourceKey,
					"value":       match.Value,
					"source_keys": getMapKeys(sourceData),
				})
			}
		} else {
			unmapped = append(unmapped, param.Name)
		}
	}

	return &AutoWireResult{
		Parameters: params,
		Resolved:   resolved,
		Unmapped:   unmapped,
		DurationUs: time.Since(startTime).Microseconds(),
	}
}

// AutoWireFromMultipleSources merges data from multiple source results and performs auto-wiring
func (w *AutoWirer) AutoWireFromMultipleSources(
	sourceResults map[string]string, // stepID -> JSON response
	targetParams []Parameter,
) *AutoWireResult {
	// Merge all source data
	mergedData := make(map[string]interface{})
	for stepID, response := range sourceResults {
		if response == "" {
			continue
		}
		var parsed map[string]interface{}
		if err := json.Unmarshal([]byte(response), &parsed); err != nil {
			if w.logger != nil {
				w.logger.Warn("Failed to parse source response for auto-wiring", map[string]interface{}{
					"step_id": stepID,
					"error":   err.Error(),
				})
			}
			continue
		}
		// Merge into combined data (later steps override earlier for same keys)
		for k, v := range parsed {
			mergedData[k] = v
		}
	}

	return w.AutoWireParameters(mergedData, targetParams)
}

// findMatchingValueWithMetadata searches for a value matching the parameter name in the source data.
// Returns match metadata including the match strategy used and the source key.
func (w *AutoWirer) findMatchingValueWithMetadata(data map[string]interface{}, paramName, paramType string) (*autoWireMatch, bool) {
	// Strategy 1: Exact name match
	if val, ok := data[paramName]; ok {
		extracted := extractNestedValue(val, paramType)
		matchType := "exact"
		// Detect if extractNestedValue performed a map→string extraction.
		// Cannot use extracted != val because maps/slices are not comparable with == / !=
		// and would cause a runtime panic. extractNestedValue only transforms in one case:
		// map[string]interface{} → string (extracts "code", "id", "value", or "name" field).
		if _, wasMap := val.(map[string]interface{}); wasMap {
			if _, isStr := extracted.(string); isStr {
				matchType = "nested_extraction"
			}
		}
		return &autoWireMatch{
			Value:     coerceType(extracted, paramType),
			MatchType: matchType,
			SourceKey: paramName,
		}, true
	}

	// Strategy 2: Case-insensitive match
	paramLower := strings.ToLower(paramName)
	for key, val := range data {
		if strings.ToLower(key) == paramLower {
			extracted := extractNestedValue(val, paramType)
			return &autoWireMatch{
				Value:     coerceType(extracted, paramType),
				MatchType: "case_insensitive",
				SourceKey: key,
			}, true
		}
	}

	// Strategy 3: Semantic alias match
	aliases := getAliases(paramName)
	for _, alias := range aliases {
		// Check exact alias match
		if val, ok := data[alias]; ok {
			extracted := extractNestedValue(val, paramType)
			return &autoWireMatch{
				Value:     coerceType(extracted, paramType),
				MatchType: "alias",
				SourceKey: alias,
			}, true
		}
		// Check case-insensitive alias match
		for key, val := range data {
			if strings.EqualFold(key, alias) {
				extracted := extractNestedValue(val, paramType)
				return &autoWireMatch{
					Value:     coerceType(extracted, paramType),
					MatchType: "alias",
					SourceKey: key,
				}, true
			}
		}
	}

	// Strategy 4: Search in nested "data" or "response" wrappers
	if dataWrapper, ok := data["data"].(map[string]interface{}); ok {
		if match, found := w.findMatchingValueWithMetadata(dataWrapper, paramName, paramType); found {
			return match, true
		}
	}
	if responseWrapper, ok := data["response"].(map[string]interface{}); ok {
		if match, found := w.findMatchingValueWithMetadata(responseWrapper, paramName, paramType); found {
			return match, true
		}
	}

	return nil, false
}

// extractNestedValue extracts the most appropriate value from nested objects.
// When the source value is a map (object) and target type is string, this function
// attempts to extract common nested fields like "code", "id", "name" that typically
// represent the canonical string identifier.
//
// Common patterns handled:
//   - currency: {"code": "EUR", "name": "Euro", "symbol": "€"} -> extracts "EUR"
//   - country: {"code": "FR", "name": "France"} -> extracts "FR"
//
// This enables proper parameter binding when tools return structured objects but
// downstream tools expect simple string values.
func extractNestedValue(val interface{}, targetType string) interface{} {
	// Only apply nested extraction when target is string and source is a map
	if strings.ToLower(targetType) != "string" {
		return val
	}

	mapVal, isMap := val.(map[string]interface{})
	if !isMap {
		return val
	}

	// Priority order for extracting string identifier from nested objects:
	// 1. "code" - most common for currency, country codes (ISO standards)
	// 2. "id" - common identifier field
	// 3. "value" - generic value field
	// 4. "name" - fallback to name if no code/id exists
	extractionPriority := []string{"code", "id", "value", "name"}

	for _, field := range extractionPriority {
		if nestedVal, exists := mapVal[field]; exists {
			// Only extract if the nested value is a string
			if strVal, isStr := nestedVal.(string); isStr {
				return strVal
			}
		}
	}

	// No suitable nested field found - return original value
	return val
}

// getAliases returns all known aliases for a parameter name
func getAliases(paramName string) []string {
	paramLower := strings.ToLower(paramName)

	// Check if paramName is a canonical name
	if aliases, ok := SemanticAliases[paramLower]; ok {
		return aliases
	}

	// Check if paramName is an alias of a canonical name
	for canonical, aliases := range SemanticAliases {
		for _, alias := range aliases {
			if strings.EqualFold(paramName, alias) {
				// Return all aliases for this canonical name
				return append(SemanticAliases[canonical], canonical)
			}
		}
	}

	return []string{}
}

// coerceType converts a value to the target type
func coerceType(val interface{}, targetType string) interface{} {
	switch strings.ToLower(targetType) {
	case "number", "float", "float64", "double":
		return toFloat64(val)
	case "integer", "int", "int64":
		return toInt64(val)
	case "string":
		return toString(val)
	case "boolean", "bool":
		return toBool(val)
	default:
		return val
	}
}

// toFloat64 converts various types to float64
func toFloat64(val interface{}) float64 {
	switch v := val.(type) {
	case float64:
		return v
	case float32:
		return float64(v)
	case int:
		return float64(v)
	case int32:
		return float64(v)
	case int64:
		return float64(v)
	case string:
		f, err := strconv.ParseFloat(v, 64)
		if err != nil {
			return 0
		}
		return f
	case json.Number:
		f, err := v.Float64()
		if err != nil {
			return 0
		}
		return f
	default:
		return 0
	}
}

// toInt64 converts various types to int64
func toInt64(val interface{}) int64 {
	switch v := val.(type) {
	case int64:
		return v
	case int:
		return int64(v)
	case int32:
		return int64(v)
	case float64:
		return int64(v)
	case float32:
		return int64(v)
	case string:
		i, err := strconv.ParseInt(v, 10, 64)
		if err != nil {
			return 0
		}
		return i
	case json.Number:
		i, err := v.Int64()
		if err != nil {
			return 0
		}
		return i
	default:
		return 0
	}
}

// toString converts various types to string
func toString(val interface{}) string {
	if val == nil {
		return ""
	}
	switch v := val.(type) {
	case string:
		return v
	case []byte:
		return string(v)
	default:
		b, err := json.Marshal(v)
		if err != nil {
			return ""
		}
		// Remove quotes from simple values
		s := string(b)
		if s == "null" {
			return ""
		}
		if len(s) >= 2 && s[0] == '"' && s[len(s)-1] == '"' {
			return s[1 : len(s)-1]
		}
		return s
	}
}

// toBool converts various types to bool
func toBool(val interface{}) bool {
	switch v := val.(type) {
	case bool:
		return v
	case string:
		lower := strings.ToLower(v)
		return lower == "true" || lower == "1" || lower == "yes" || lower == "on"
	case int:
		return v != 0
	case int64:
		return v != 0
	case float64:
		return v != 0
	case json.Number:
		// Numbers decoded via unmarshalPreservingNumbers (P17.5) arrive as json.Number;
		// without this case a numeric truthy flag (enabled:1) silently coerces to false.
		f, err := v.Float64()
		return err == nil && f != 0
	default:
		return false
	}
}

// === Standalone functions for simpler usage ===

// AutoWireParameters is a convenience function that creates a temporary AutoWirer
// and performs auto-wiring without requiring an instance
func AutoWireParameters(
	sourceData map[string]interface{},
	targetParams []Parameter,
) *AutoWireResult {
	wirer := &AutoWirer{logger: nil}
	return wirer.AutoWireParameters(sourceData, targetParams)
}
