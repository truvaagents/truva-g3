package orchestration

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/truvaagents/truva-g3/core"
)

// Stored facts and derived views are separate: this code neither edits step
// payloads nor infers a domain cause from an error message.
func memoryErrorCode(raw json.RawMessage) string {
	var err struct {
		Code string `json:"code"`
	}
	if json.Unmarshal(raw, &err) != nil {
		return ""
	}
	return err.Code
}

const defaultRecordFailureCauseMaxBytes = 256

func failedMemorySummary(action string, entity Entity, step StepResult, maxCauseBytes int) (string, bool) {
	description := action + " failed"
	if entity.Type != "" && entity.ID != "" {
		description += " on " + entity.Type + "/" + entity.ID
	}
	if step.AgentName != "" {
		description += " via " + step.AgentName
	}
	cause := memoryErrorCode(step.ToolError)
	if cause == "" {
		cause = step.Error
	}
	if cause == "" {
		cause = "cause unknown"
	}
	shortened := len(cause) > maxCauseBytes
	if shortened {
		cause = boundedSummaryResponse(cause, maxCauseBytes) + " [shortened]"
	}
	return description + ": " + cause, shortened
}

func memorySummaryMetadata(summaries map[string]core.StepSummary, id string, summarizer core.EventSummarizer, reason string) map[string]string {
	metadata := map[string]string{"summary_source": "heuristic"}
	if summary, ok := summaries[id]; ok && summary.Summary != "" {
		metadata["summary_source"] = "custom"
		if _, builtin := summarizer.(*LLMEventSummarizer); builtin {
			metadata["summary_source"] = "model"
		}
	} else {
		metadata["summary_fallback_reason"] = reason
	}
	return metadata
}

func mergeMemoryEntities(request, step []Entity) []Entity {
	var entities []Entity
	seen := make(map[Entity]bool)
	for _, source := range [][]Entity{request, step} {
		for _, entity := range source {
			if entity.Type == "" || entity.ID == "" || seen[entity] {
				continue
			}
			seen[entity] = true
			entities = append(entities, entity)
		}
	}
	return entities
}

// Occurrence time changes query-window membership, not ingestion-based retention.
// The fallback basis explicitly distinguishes a recording timestamp from an
// observed completion. No historic event is rewritten.
func memoryEventTime(step StepResult, recordedAt time.Time, metadata map[string]string) time.Time {
	metadata["recorded_at"] = recordedAt.Format(time.RFC3339Nano)
	switch {
	case step.EndTime.After(recordedAt):
		metadata["event_time_fallback_reason"] = "end_in_future"
	case !step.EndTime.IsZero() && !step.StartTime.IsZero() && step.EndTime.Before(step.StartTime):
		metadata["event_time_fallback_reason"] = "end_before_start"
	case !step.EndTime.IsZero():
		metadata["event_time_basis"] = "step_end"
		return step.EndTime
	}
	if !step.StartTime.IsZero() && !step.StartTime.After(recordedAt) {
		metadata["event_time_basis"] = "step_start"
		return step.StartTime
	}
	metadata["event_time_basis"] = "recorded_at_unknown"
	if metadata["event_time_fallback_reason"] == "" {
		metadata["event_time_fallback_reason"] = "occurrence_unavailable"
	}
	return recordedAt
}

type memoryFailureView struct {
	Error            string `json:"error,omitempty"`
	Code             string `json:"error_code,omitempty"`
	ToolError        string `json:"tool_error,omitempty"`
	ErrorShortened   bool   `json:"error_shortened,omitempty"`
	ToolErrorOmitted bool   `json:"tool_error_omitted,omitempty"`
	CodeOmitted      bool   `json:"error_code_omitted,omitempty"`
}

// Encoded failure data shares the existing response byte allowance. Error text
// and structured bytes are untrusted strings, so they cannot close XML framing.
// Omission is explicit; original fields remain in the execution record.
func boundedMemoryFailure(step core.StepSummaryInput, budget int) string {
	view := memoryFailureView{Error: step.Error, Code: memoryErrorCode(step.ToolError), ToolError: string(step.ToolError)}
	encode := func() string {
		data, _ := json.Marshal(view) // scalar-only derived view
		return string(data)
	}
	if full := encode(); len(full) <= budget {
		return full
	}
	view.ToolErrorOmitted, view.ToolError = view.ToolError != "", ""
	view.ErrorShortened = view.Error != ""
	text := []rune(view.Error)
	view.Error = ""
	if len(encode()) > budget && view.Code != "" {
		view.Code, view.CodeOmitted = "", true
	}
	if len(encode()) > budget {
		return "" // Caller emits an omission marker outside the payload.
	}
	low, high := 0, len(text)
	for low < high {
		mid := low + (high-low+1)/2
		view.Error = string(text[:mid])
		if len(encode()) <= budget {
			low = mid
		} else {
			high = mid - 1
		}
	}
	view.Error = string(text[:low])
	view.ErrorShortened = low < len(text)
	return encode()
}

func memoryFallbackReason(reason string) string {
	if reason == "complete" || reason == "partial" {
		return "missing_summary"
	}
	return reason
}

func formatSummaryFailure(step core.StepSummaryInput, budget int) (string, int) {
	if step.Success || (step.Error == "" && len(step.ToolError) == 0) {
		return "", budget
	}
	view := boundedMemoryFailure(step, budget)
	if view == "" {
		return "  <failure omitted=\"true\"/>\n", budget
	}
	return fmt.Sprintf("  <failure>%s</failure>\n", view), budget - len(view)
}

func memorySummarySource(metadata map[string]string) string {
	switch source := metadata["summary_source"]; source {
	case "model", "heuristic", "custom":
		return source
	default:
		return "unknown"
	}
}

func memoryTimeBasis(metadata map[string]string) string {
	switch basis := metadata["event_time_basis"]; basis {
	case "step_end", "step_start", "recorded_at_unknown":
		return basis
	default:
		return "unknown"
	}
}

func boundedSummaryResponse(response string, budget int) string {
	if len(response) <= budget {
		return response
	}
	return strings.ToValidUTF8(response[:max(0, budget)], "")
}
