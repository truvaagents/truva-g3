package orchestration

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/truvaagents/truva-g3/core"
)

// delegatedContinuationView is a derived prompt view only. In particular, it
// must never replace the response that parameter binding or recorders consume.
type delegatedContinuationView struct {
	body              string
	rendered          string
	meta              ResultTrimMetadata
	childrenShown     int
	childrenOmitted   int
	childCapabilities []string
}

// isRegisteredOrchestrator checks the full catalog, independently of the
// selector's current subset. Conflicting replica registrations fail closed to
// the ordinary digest rather than choosing a type by map iteration order.
func isRegisteredOrchestrator(catalog *AgentCatalog, result *StepResult) bool {
	if catalog == nil || result == nil || result.AgentName == "" || result.Capability == "" {
		return false
	}
	catalog.mu.RLock()
	defer catalog.mu.RUnlock()
	found := false
	for _, agent := range catalog.agents {
		if agent == nil || agent.Registration == nil || agent.Registration.Name != result.AgentName {
			continue
		}
		namespace, _ := agent.Registration.Metadata["namespace"].(string)
		if result.Namespace != "" && namespace != "" && namespace != result.Namespace {
			continue
		}
		for _, capability := range agent.Capabilities {
			if capability.Name != result.Capability {
				continue
			}
			if capability.Type != core.CapabilityOrchestrator {
				return false
			}
			found = true
		}
	}
	return found
}

// frameworkDelegatedEnvelope requires the framework's actual discriminating
// fields. A custom receipt (including success/data) keeps its generic rendering.
// Missing fields cannot become valid merely by unmarshalling to zero values.
func frameworkDelegatedEnvelope(response string) (map[string]interface{}, bool) {
	value, err := unmarshalPreservingNumbers([]byte(response))
	if err != nil {
		return nil, false
	}
	object, ok := value.(map[string]interface{})
	if !ok {
		return nil, false
	}
	if _, success := object["success"]; success {
		if _, data := object["data"]; data {
			return nil, false
		}
	}
	requestID, ok := object["request_id"].(string)
	if !ok || requestID == "" {
		return nil, false
	}
	if _, ok = object["response"].(string); !ok {
		return nil, false
	}
	mode, ok := object["routing_mode"].(string)
	if !ok || (mode != string(ModeAutonomous) && mode != string(ModeWorkflow)) {
		return nil, false
	}
	if clarification, exists := object["clarification"]; exists && clarification != nil {
		fields, ok := clarification.(map[string]interface{})
		if !ok {
			return nil, false
		}
		if _, ok := fields["question"].(string); !ok {
			return nil, false
		}
	}
	if steps, exists := object["steps"]; exists && steps != nil {
		if _, ok := steps.([]interface{}); !ok {
			return nil, false
		}
	}
	// Check optional framework fields against their actual wire types too.
	// Keep the UseNumber map for rendering so extension values do not pass
	// through float64 merely because the schema is being validated.
	var envelope OrchestratorResponse
	if err := json.Unmarshal([]byte(response), &envelope); err != nil {
		return nil, false
	}
	return object, true
}

// delegatedPreview retains readable text, including literal code and Unicode,
// instead of replacing the whole scalar by its length. This is a disclosed
// presentation cut; JSON marshaling below preserves the original field path.
func delegatedPreview(value string, maxBytes int) (string, bool) {
	if len(value) <= maxBytes {
		return value, false
	}
	const omitted = "… [omitted]"
	return cutToBytes(value, maxBytes-len(omitted)) + omitted, true
}

// delegatedChildFacts reports observations, not retry or effect policy. A
// skipped call was not dispatched, and a failed call may have partial effects.
func delegatedChildFacts(object map[string]interface{}, maxBytes int) (string, int, int, []string) {
	children, _ := object["steps"].([]interface{})
	var lines []string
	var capabilities []string
	valid := 0
	used := 0
	for _, value := range children {
		child, ok := value.(map[string]interface{})
		if !ok {
			continue
		}
		valid++
		agent, _ := child["agent_name"].(string)
		capability, _ := child["capability"].(string)
		state := "UNKNOWN"
		message, _ := child["response"].(string)
		if skipped, _ := child["skipped"].(bool); skipped {
			state = "SKIPPED"
			message, _ = child["skip_reason"].(string)
		} else if success, exists := child["success"].(bool); exists {
			state = "SUCCESS"
			if !success {
				state = "FAILED"
				if cause, _ := child["error"].(string); cause != "" {
					message = cause
				}
			}
		}
		line := fmt.Sprintf("    - %s/%s [%s]: %s\n", agent, capability, state, truncateRunes(firstLine(message), 200))
		if used+len(line) <= maxBytes {
			lines = append(lines, line)
			capabilities = append(capabilities, agent+"/"+capability)
			used += len(line)
		}
	}
	if valid == 0 {
		return "", 0, 0, nil
	}
	// Reserve disclosure inside the same allocation as the child facts. Remove
	// complete lines if necessary; a view never cuts a child identity mid-line.
	omitted := valid - len(lines)
	for {
		note := ""
		if omitted > 0 {
			note = fmt.Sprintf("    [%d child outcomes omitted; original results retained]\n", omitted)
		}
		if used+len(note) <= maxBytes || len(lines) == 0 {
			if len(lines) == 0 && len(note) > maxBytes {
				return "", 0, valid, nil
			}
			return strings.Join(lines, "") + note, len(lines), omitted, capabilities
		}
		last := len(lines) - 1
		used -= len(lines[last])
		lines = lines[:last]
		capabilities = capabilities[:last]
		omitted++
	}
}

// delegatedOutputFields removes only named framework accounting fields from a
// copied view. Metadata can contain application outputs, so it remains a
// generic structural digest, including its original nested paths.
func delegatedOutputFields(object map[string]interface{}) (map[string]interface{}, bool) {
	fields := make(map[string]interface{}, len(object))
	lost := false
	for key, value := range object {
		switch key {
		case "usage", "usage_by_phase", "execution_time":
			lost = true
		case "response", "clarification":
			// Added separately, with readable scalar previews.
		default:
			fields[key] = value
		}
	}
	if children, ok := fields["steps"].([]interface{}); ok {
		copied := make([]interface{}, len(children))
		for index, value := range children {
			child, ok := value.(map[string]interface{})
			if !ok {
				copied[index] = value
				continue
			}
			copy := make(map[string]interface{}, len(child))
			for key, field := range child {
				copy[key] = field
			}
			if metadata, ok := child["metadata"].(map[string]interface{}); ok {
				view := make(map[string]interface{}, len(metadata))
				for key, field := range metadata {
					if key == "result_trim" {
						lost = true
						continue
					}
					view[key] = field
				}
				copy["metadata"] = view
			}
			copied[index] = copy
		}
		fields["steps"] = copied
	}
	return fields, lost
}

// renderDelegatedContinuationBody gives response and clarification readable
// prefixes while retaining a generic structural digest of every other field.
// No application field names or receipt semantics are built into the framework.
func renderDelegatedContinuationBody(object map[string]interface{}, opts continuationDigestOpts, maxBytes int) delegatedContinuationView {
	previewBytes := maxBytes
	childBytes := maxBytes / 4
	fields, bookkeepingOmitted := delegatedOutputFields(object)
	for {
		lost := bookkeepingOmitted
		view := digestValue(fields, opts.sampleN, opts.scalarMax, opts.maxKeys, &lost).(map[string]interface{})
		response, _ := object["response"].(string)
		preview, shortened := delegatedPreview(response, previewBytes)
		view["response"] = preview
		lost = lost || shortened
		if raw, ok := object["clarification"].(map[string]interface{}); ok {
			remaining := make(map[string]interface{}, len(raw))
			for key, value := range raw {
				if key != "question" && key != "partial_progress" {
					remaining[key] = value
				}
			}
			clarification := digestValue(remaining, opts.sampleN, opts.scalarMax, opts.maxKeys, &lost).(map[string]interface{})
			for _, key := range []string{"question", "partial_progress"} {
				if text, ok := raw[key].(string); ok {
					clarification[key], shortened = delegatedPreview(text, previewBytes)
					lost = lost || shortened
				}
			}
			view["clarification"] = clarification
		} else if value, exists := object["clarification"]; exists {
			view["clarification"] = value
		}
		// These identity fields remain present even when a wide extension object
		// requires the generic sorted-key sampling fallback.
		view["request_id"] = object["request_id"]
		view["routing_mode"] = object["routing_mode"]
		encoded, _ := json.Marshal(view) // digestValue yields JSON-marshalable values.
		body := string(encoded)
		facts, shown, omitted, capabilities := delegatedChildFacts(object, childBytes)
		if facts != "" {
			body += "\n  Observed child outcomes (failed effects may be uncertain; skipped calls were not dispatched):\n" + facts
		}
		lost = lost || omitted > 0
		if len(body) <= maxBytes {
			return delegatedContinuationView{
				body: body, meta: ResultTrimMetadata{Method: "digest", TrimmedBytes: len(body), ContentLost: lost},
				childrenShown: shown, childrenOmitted: omitted, childCapabilities: capabilities,
			}
		}
		if previewBytes <= 1 && opts.sampleN <= 1 && opts.scalarMax <= 1 && opts.maxKeys <= 1 && childBytes == 0 {
			// A pathological allocation cannot hold even the minimal skeleton.
			// Preserve an explicit omission rather than emitting invalid JSON.
			body = "[delegated output omitted for budget; original fields remain referenceable]"
			return delegatedContinuationView{body: body, meta: ResultTrimMetadata{Method: "digest", TrimmedBytes: len(body), ContentLost: true}, childrenOmitted: lenChildren(object)}
		}
		previewBytes /= 2
		childBytes /= 2
		opts.sampleN = max(1, opts.sampleN/2)
		opts.scalarMax = max(1, opts.scalarMax/2)
		opts.maxKeys = max(1, opts.maxKeys/2)
	}
}

func lenChildren(object map[string]interface{}) int {
	children, _ := object["steps"].([]interface{})
	return len(children)
}

// Preserve an actual earlier processor's loss observation, without treating the
// unused generic digest's hypothetical cuts as losses of this new view.
func delegatedRecordedContentLost(result *StepResult) bool {
	switch metadata := result.Metadata["result_trim"].(type) {
	case *ResultTrimMetadata:
		return metadata != nil && metadata.ContentLost
	case ResultTrimMetadata:
		return metadata.ContentLost
	case map[string]interface{}:
		lost, _ := metadata["content_lost"].(bool)
		return lost
	default:
		return false
	}
}

// prepareDelegatedContinuationView prepares each emitted dynamic value once.
// Count the final encoded framing before recency fill. A custom encoder can
// expand a protected newest result; its actual bytes and overflow are disclosed
// by the continuation budget event, without damaging the encoded value.
func prepareDelegatedContinuationView(ctx context.Context, result *StepResult, catalog *AgentCatalog, opts continuationDigestOpts, maxBytes int) (*delegatedContinuationView, error) {
	if result == nil || !result.Success || !isRegisteredOrchestrator(catalog, result) {
		return nil, nil
	}
	object, ok := frameworkDelegatedEnvelope(result.Response)
	if !ok {
		return nil, nil
	}
	view := renderDelegatedContinuationBody(object, opts, maxBytes)
	view.meta.OriginalBytes = len(result.Response)
	view.meta.ContentLost = view.meta.ContentLost || delegatedRecordedContentLost(result)
	fields := []struct{ field, value string }{
		{promptFieldPriorResultStepID, result.StepID},
		{promptFieldPriorResultAgentName, result.AgentName},
		{promptFieldPriorResultInstruction, result.Instruction},
		{promptFieldPriorResultResponse, view.body},
	}
	prepared := make([]string, len(fields))
	for index, field := range fields {
		value, err := preparePromptValue(ctx, promptContinuationPlan, promptValuePriorResult, field.field, field.value)
		if err != nil {
			return nil, err
		}
		prepared[index] = value
	}
	view.rendered = fmt.Sprintf("Step %s (%s):\n  Task: %s\n  Result: %s\n\n", prepared[0], prepared[1], prepared[2], prepared[3])
	view.meta.TrimmedBytes = len(prepared[3])
	return &view, nil
}
