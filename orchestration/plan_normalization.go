package orchestration

import (
	"fmt"
	"regexp"
	"slices"
	"sort"
	"strings"
)

// numericBracketSegment is an alias for the dot-numeric path syntax already
// understood by the executor. It is used only inside complete step macros.
var numericBracketSegment = regexp.MustCompile(`\[([0-9]+)\]`)

// normalizeGeneratedPlan creates an independent executable candidate. Caller
// plans, hook results, and approved checkpoints never pass through this seam.
// A rejected candidate is left unchanged for correction feedback and debugging.
func normalizeGeneratedPlan(plan *RoutingPlan, priorResults map[string]*StepResult) (*RoutingPlan, int, error) {
	if plan == nil {
		return nil, 0, fmt.Errorf("cannot normalize a nil plan")
	}
	copy := cloneGeneratedPlan(plan)
	samePhase := make(map[string]struct{}, len(copy.Steps))
	for _, step := range copy.Steps {
		if _, duplicate := samePhase[step.StepID]; duplicate {
			return nil, 0, fmt.Errorf("duplicate step ID %s", step.StepID)
		}
		samePhase[step.StepID] = struct{}{}
	}
	changes := 0
	for i := range copy.Steps {
		step := &copy.Steps[i]
		var parameters interface{}
		if step.Metadata != nil {
			if original, present := step.Metadata["parameters"]; present {
				var rewritten int
				parameters, rewritten = rewriteNumericBracketSegments(original)
				step.Metadata["parameters"] = parameters
				changes += rewritten
			}
		}
		references := collectReferencedStepIDs(parameters)
		referenceIDs := make([]string, 0, len(references))
		for ref := range references {
			referenceIDs = append(referenceIDs, ref)
		}
		sort.Strings(referenceIDs)
		for _, declared := range step.ImplicitDeps {
			if _, current := samePhase[declared]; current {
				return nil, 0, fmt.Errorf("step %s lists same-phase step %s in implicit_deps", step.StepID, declared)
			}
			if _, used := references[declared]; !used {
				return nil, 0, fmt.Errorf("step %s lists %s in implicit_deps without a matching parameter template", step.StepID, declared)
			}
		}
		prior := make([]string, 0, len(references))
		for _, ref := range referenceIDs {
			if ref == step.StepID {
				return nil, 0, fmt.Errorf("step %s references its own output", step.StepID)
			}
			if _, current := samePhase[ref]; current {
				continue // depends_on is still validated by the existing gauntlet
			}
			if priorResults[ref] == nil {
				return nil, 0, fmt.Errorf("step %s references unknown prior step %s", step.StepID, ref)
			}
			prior = append(prior, ref)
		}
		declared := append([]string(nil), step.ImplicitDeps...)
		sort.Strings(declared)
		if !slices.Equal(declared, prior) || len(step.ImplicitDeps) != len(prior) {
			step.ImplicitDeps = prior
			changes++
		} else if !slices.Equal(step.ImplicitDeps, prior) {
			step.ImplicitDeps = prior
			changes++
		}
	}
	return copy, changes, nil
}

func rewriteNumericBracketSegments(value interface{}) (interface{}, int) {
	switch typed := value.(type) {
	case string:
		changes := 0
		result := frameworkMacroPattern.ReplaceAllStringFunc(typed, func(token string) string {
			// A complete reserved token can contain several index segments.
			if !strings.HasPrefix(token, "{{step-") || !strings.HasSuffix(token, "}}") {
				return token
			}
			rewritten := numericBracketSegment.ReplaceAllString(token, ".$1")
			if rewritten != token {
				changes++
			}
			return rewritten
		})
		return result, changes
	case map[string]interface{}:
		changes := 0
		for key, item := range typed {
			var count int
			typed[key], count = rewriteNumericBracketSegments(item)
			changes += count
		}
		return typed, changes
	case []interface{}:
		changes := 0
		for index, item := range typed {
			var count int
			typed[index], count = rewriteNumericBracketSegments(item)
			changes += count
		}
		return typed, changes
	default:
		return value, 0
	}
}

func cloneGeneratedPlan(plan *RoutingPlan) *RoutingPlan {
	copy := *plan
	copy.Steps = append([]RoutingStep(nil), plan.Steps...)
	if plan.Terminal != nil {
		terminal := *plan.Terminal
		copy.Terminal = &terminal
	}
	if plan.NeedsUserInput != nil {
		clarification := *plan.NeedsUserInput
		clarification.MissingFields = append([]string(nil), clarification.MissingFields...)
		copy.NeedsUserInput = &clarification
	}
	for i := range copy.Steps {
		copy.Steps[i].DependsOn = append([]string(nil), plan.Steps[i].DependsOn...)
		copy.Steps[i].ImplicitDeps = append([]string(nil), plan.Steps[i].ImplicitDeps...)
		if plan.Steps[i].Metadata != nil {
			copy.Steps[i].Metadata = cloneGeneratedValue(plan.Steps[i].Metadata).(map[string]interface{})
		}
	}
	return &copy
}

func cloneGeneratedValue(value interface{}) interface{} {
	switch typed := value.(type) {
	case map[string]interface{}:
		copy := make(map[string]interface{}, len(typed))
		for key, item := range typed {
			copy[key] = cloneGeneratedValue(item)
		}
		return copy
	case []interface{}:
		copy := make([]interface{}, len(typed))
		for i, item := range typed {
			copy[i] = cloneGeneratedValue(item)
		}
		return copy
	default:
		return value
	}
}
