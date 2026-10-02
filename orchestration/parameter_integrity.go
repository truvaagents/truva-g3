package orchestration

import (
	"context"
	"fmt"
	"reflect"
	"strings"

	"github.com/truvaagents/truva-g3/telemetry"
	"go.opentelemetry.io/otel/attribute"
)

// cloneParameterMap isolates execution and dispatch state from admitted plans
// and checkpoints without a JSON round trip that changes numeric types.
func cloneParameterMap(value map[string]interface{}) map[string]interface{} {
	if value == nil {
		return nil
	}
	return cloneGeneratedValue(value).(map[string]interface{})
}

func missingParameterCapability(capability *EnhancedCapability, params map[string]interface{}) *EnhancedCapability {
	if capability == nil {
		return nil
	}
	copy := *capability
	copy.Parameters = nil
	for _, param := range capability.Parameters {
		if _, exists := params[param.Name]; !exists && param.Required {
			copy.Parameters = append(copy.Parameters, param)
		}
	}
	if len(copy.Parameters) == 0 {
		return nil
	}
	return &copy
}

// parameterBindingEvidence records original references separately from inference.
func parameterBindingEvidence(original, resolved map[string]interface{}) map[string]interface{} {
	evidence := make(map[string]interface{})
	for key, value := range original {
		if !paramsContainUnresolvedFrameworkMacro(map[string]interface{}{key: value}) {
			continue
		}
		method := "exact"
		if paramsContainUnresolvedFrameworkMacro(map[string]interface{}{key: resolved[key]}) {
			method = "unresolved"
		}
		evidence[key] = map[string]interface{}{"method": method, "reference": cloneGeneratedValue(value), "value": cloneGeneratedValue(resolved[key])}
	}
	return evidence
}

// Exact and approved values cannot use semantic nested extraction to repair a
// known shape mismatch. Inferred/auto-wired maps still use the existing resolver.
func validateExactBindingShapes(params map[string]interface{}, capability *EnhancedCapability, metadata map[string]interface{}, approved bool) error {
	if capability == nil {
		return nil
	}
	bindings, _ := metadata["bindings"].(map[string]interface{})
	for _, p := range capability.Parameters {
		binding, _ := bindings[p.Name].(map[string]interface{})
		if !approved && binding["method"] != "exact" {
			continue
		}
		if err := validateContainerShape(p.Name, params[p.Name], p.Type); err != nil {
			return err
		}
	}
	return nil
}

func (e *SmartExecutor) resolveScopedTemplateValue(ctx context.Context, value interface{}, name string, sources map[string]map[string]interface{}, stepID string) interface{} {
	switch v := value.(type) {
	case map[string]interface{}:
		copy := cloneParameterMap(v)
		for key, item := range copy {
			copy[key] = e.resolveScopedTemplateValue(ctx, item, name+"."+key, sources, stepID)
		}
		return copy
	case []interface{}:
		copy := make([]interface{}, len(v))
		for i, item := range v {
			copy[i] = e.resolveScopedTemplateValue(ctx, item, fmt.Sprintf("%s.%d", name, i), sources, stepID)
		}
		return copy
	case string:
		result := v
		for _, match := range stepOutputTemplatePattern.FindAllStringSubmatch(v, -1) {
			if !strings.HasPrefix(match[1], "step-") {
				continue
			}
			data, id, found := findStepDataCaseInsensitive(sources, match[1])
			if !found || len(data) == 0 {
				continue
			}
			// buildStepContext admits only successful, non-skipped execution
			// results. Application payload fields cannot override that status.
			hint := fmt.Sprintf("Infer the value intended by %s for parameter %s using only this referenced source. Return null if unavailable.", match[0], name)
			resolved, err := e.hybridResolver.ResolveSemanticValue(ctx, map[string]interface{}{id: data}, name, hint, "string", stepID)
			if err != nil || resolved == nil {
				continue
			}
			requestID := ""
			if baggage := telemetry.GetBaggage(ctx); baggage != nil {
				requestID = baggage["request_id"]
			}
			telemetry.AddSpanEvent(ctx, "semantic_template_resolution", attribute.String("request_id", requestID), attribute.String("step_id", stepID), attribute.String("param_name", name), attribute.String("source_step", id), attribute.String("original_template", match[0]), attribute.String("binding_method", "inferred"))
			result = strings.Replace(result, match[0], fmt.Sprint(resolved), 1)
		}
		return result
	default:
		return value
	}
}

// validParameterCorrection protects exact references and HITL-approved inputs,
// and rejects no-op or still-unresolved corrections before another dispatch.
func validParameterCorrection(previous, corrected, protected map[string]interface{}) bool {
	if corrected == nil || reflect.DeepEqual(previous, corrected) || paramsContainUnresolvedFrameworkMacro(corrected) {
		return false
	}
	for key, value := range protected {
		if candidate, exists := corrected[key]; !exists || !reflect.DeepEqual(candidate, value) {
			return false
		}
	}
	return true
}
