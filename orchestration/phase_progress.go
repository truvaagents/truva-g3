package orchestration

import "sort"

// blockedPhaseRoots identifies a dependency-only phase by its actual failed
// ancestors, ignoring newly generated skipped step IDs. Any executed work or
// incomplete/cyclic evidence makes the phase ineligible for fixed-point stopping.
// This is not a tool-output comparison: repeated asynchronous polling is work.
func blockedPhaseRoots(steps []StepResult, results map[string]*StepResult) []string {
	if len(steps) == 0 {
		return nil
	}
	for _, step := range steps {
		if !isDependencySkip(&step) {
			return nil
		}
	}
	roots := make(map[string]bool)
	visiting, visited := make(map[string]bool), make(map[string]bool)
	var visit func(string) bool
	visit = func(id string) bool {
		if visiting[id] {
			return false
		}
		if visited[id] {
			return true
		}
		result := results[id]
		if result == nil || result.Success {
			return false
		}
		if !isDependencySkip(result) {
			if result.Skipped {
				return false
			}
			roots[id] = true
			visited[id] = true
			return true
		}
		deps := failedDependencyIDs(result)
		if len(deps) == 0 {
			return false
		}
		visiting[id] = true
		for _, dep := range deps {
			if !visit(dep) {
				return false
			}
		}
		delete(visiting, id)
		visited[id] = true
		return true
	}
	for _, step := range steps {
		if !visit(step.StepID) {
			return nil
		}
	}
	ids := make([]string, 0, len(roots))
	for id := range roots {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

func isDependencySkip(result *StepResult) bool {
	if result == nil || result.Success || !result.Skipped || result.Attempts != 0 {
		return false
	}
	reason, _ := result.Metadata[metaKeyBlockingReason].(string)
	return reason == blockingReasonExplicit || reason == blockingReasonTemplate
}

func failedDependencyIDs(result *StepResult) []string {
	var ids []string
	switch values := result.Metadata[metaKeyAllFailedDependencies].(type) {
	case []string:
		ids = values
	case []interface{}:
		for _, value := range values {
			id, ok := value.(string)
			if !ok || id == "" {
				return nil
			}
			ids = append(ids, id)
		}
	}
	if len(ids) == 0 {
		if id, _ := result.Metadata[metaKeyFailedDependency].(string); id != "" {
			ids = []string{id}
		}
	}
	return ids
}
