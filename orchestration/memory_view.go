package orchestration

import (
	"encoding/json"
	"strings"
	"time"

	"github.com/truvaagents/truva-g3/core"
)

// These are derived prompt views, not replacements for stored events. Section
// order is supplied by the hook; budgeting does not prioritize domain semantics.
type memoryViewRecord struct {
	event *core.AgentEvent
	text  string
}

type memoryViewSection struct {
	kind    string
	heading string
	records []memoryViewRecord
}

type memoryViewCounts struct {
	Kind       string `json:"section"`
	Admitted   int    `json:"admitted"`
	Omitted    int    `json:"omitted"`
	Shortened  int    `json:"shortened"`
	Duplicates int    `json:"duplicates"`
}

type memoryViewReport struct {
	ViewVersion string             `json:"view_version"`
	EventFormat string             `json:"event_format"`
	BudgetBytes int                `json:"budget_bytes"`
	UsedBytes   int                `json:"used_bytes"`
	Outcome     string             `json:"outcome"`
	Sections    []memoryViewCounts `json:"sections"`
}

// Detail is a request-local presentation, never a stored event or a compactor
// input. Each tuple carries its own provenance (no cross-record references).
const memoryDetailViewVersion = "event-detail/v2-record-facts"
const memoryDetailLegend = "Event tuple (" + memoryDetailViewVersion + "): [event_id, origin_request_id, timestamp, agent, action, outcome]. Each record is self-contained."

type memoryEventDetail struct {
	Event           [6]string `json:"event"`
	Summary         string    `json:"summary,omitempty"`
	EntityType      string    `json:"entity_type,omitempty"`
	EntityID        string    `json:"entity_id,omitempty"`
	EntitiesOmitted int       `json:"entities_omitted,omitempty"`
	Shortened       bool      `json:"shortened,omitempty"`
	SummarySource   string    `json:"summary_source"`
	FallbackReason  string    `json:"summary_fallback_reason,omitempty"`
	TimeBasis       string    `json:"event_time_basis"`
}

type memoryEventView struct {
	EventID         string           `json:"event_id,omitempty"`
	Origin          string           `json:"origin_request_id"`
	Time            string           `json:"timestamp,omitempty"`
	Agent           string           `json:"agent,omitempty"`
	Action          string           `json:"action,omitempty"`
	Summary         string           `json:"summary,omitempty"`
	Outcome         string           `json:"outcome,omitempty"`
	EntityType      string           `json:"entity_type,omitempty"`
	EntityID        string           `json:"entity_id,omitempty"`
	Entities        []core.EntityRef `json:"entities,omitempty"`
	EntitiesOmitted int              `json:"entities_omitted,omitempty"`
	Shortened       bool             `json:"shortened,omitempty"`
	SummarySource   string           `json:"summary_source"`
	FallbackReason  string           `json:"summary_fallback_reason,omitempty"`
	TimeBasis       string           `json:"event_time_basis"`
}

func eventMemoryView(event core.AgentEvent, detail bool) memoryEventView {
	origin := event.RequestID
	if origin == "" {
		origin = "unknown"
	}
	view := memoryEventView{
		EventID: event.EventID, Origin: origin, Agent: event.AgentName,
		Action: event.ActionType, Summary: event.Summary, Outcome: event.Outcome,
		EntityType: event.EntityType, EntityID: event.EntityID,
		SummarySource: memorySummarySource(event.Metadata), FallbackReason: event.Metadata["summary_fallback_reason"],
		TimeBasis: memoryTimeBasis(event.Metadata),
	}
	if !event.Timestamp.IsZero() {
		view.Time = event.Timestamp.Format(time.RFC3339)
	}
	if detail {
		// Keep singular identity when supplied; the complete multi-entity list
		// remains in stored events and compactor inputs, not repeated in detail.
		view.EntitiesOmitted = len(event.Entities)
	} else {
		view.Entities = event.Entities
	}
	return view
}

func eventMemorySection(kind, heading string, events []core.AgentEvent) memoryViewSection {
	section := memoryViewSection{kind: kind, heading: heading}
	for i := range events {
		section.records = append(section.records, memoryViewRecord{event: &events[i]})
	}
	return section
}

func textMemorySection(kind, heading, text string) memoryViewSection {
	return memoryViewSection{kind: kind, heading: heading, records: []memoryViewRecord{{text: text}}}
}

// encodeMemoryRecord shortens only the derived summary/text, at rune boundaries,
// and then re-encodes JSON. IDs and other provenance are either intact or omitted
// with the entire record. Its byte accounting includes JSON escaping.
func encodeMemoryRecord(record memoryViewRecord, limit int) (string, bool) {
	return encodeMemoryRecordFormat(record, limit, true)
}

func encodeMemoryRecordFormat(record memoryViewRecord, limit int, compact bool) (string, bool) {
	var summary string
	var encode func(string, bool) []byte
	if record.event != nil {
		source := eventMemoryView(*record.event, true)
		view := memoryEventDetail{
			Event:   [6]string{source.EventID, source.Origin, source.Time, source.Agent, source.Action, source.Outcome},
			Summary: source.Summary, EntityType: source.EntityType, EntityID: source.EntityID,
			EntitiesOmitted: source.EntitiesOmitted,
			SummarySource:   source.SummarySource, FallbackReason: source.FallbackReason, TimeBasis: source.TimeBasis,
		}
		summary = view.Summary
		encode = func(value string, shortened bool) []byte {
			if !compact {
				source.Summary, source.Shortened = value, shortened
				encoded, _ := json.Marshal(source)
				return encoded
			}
			view.Summary, view.Shortened = value, shortened
			encoded, _ := json.Marshal(view) // string/scalar-only presentation DTO
			return encoded
		}
	} else {
		summary = record.text
		encode = func(value string, shortened bool) []byte {
			encoded, _ := json.Marshal(struct {
				Text      string `json:"text"`
				Shortened bool   `json:"shortened,omitempty"`
			}{value, shortened})
			return encoded
		}
	}
	if full := encode(summary, false); len(full) <= limit {
		return string(full), false
	}
	if len(encode("", true)) > limit {
		return "", false
	}
	runes := []rune(summary)
	low, high := 0, len(runes)
	for low < high {
		mid := low + (high-low+1)/2
		if len(encode(string(runes[:mid]), true)) <= limit {
			low = mid
		} else {
			high = mid - 1
		}
	}
	return string(encode(string(runes[:low]), true)), true
}

// Select a presentation using metadata size only, not summary meaning. Sparse
// histories retain named fields when tuples would not repay the legend. Stored
// events, retrieval amounts and section order never participate in this choice.
func compactMemoryDetails(sections []memoryViewSection) bool {
	savings := 0
	seen := make(map[string]bool)
	for _, section := range sections {
		for _, record := range section.records {
			if record.event == nil {
				continue
			}
			event := *record.event
			if event.EventID != "" && seen[event.EventID] {
				continue
			}
			seen[event.EventID] = true
			event.Summary = "" // Summary encoding is identical in both forms.
			metadata := memoryViewRecord{event: &event}
			named, _ := encodeMemoryRecordFormat(metadata, int(^uint(0)>>1), false)
			compact, _ := encodeMemoryRecord(metadata, int(^uint(0)>>1))
			savings += len(named) - len(compact)
		}
	}
	return savings > len("\n")+len(memoryDetailLegend)
}

// renderMemoryView reserves scope/headings/counts before admitting records in
// the hook's existing order. Budgeting applies only to this hook's contribution.
func renderMemoryView(sections []memoryViewSection, limit int) (string, memoryViewReport) {
	report := memoryViewReport{ViewVersion: memoryDetailViewVersion, EventFormat: "named", BudgetBytes: limit, Outcome: "empty"}
	var nonempty []memoryViewSection
	reserved := len(historicalMemoryScopeNotice)
	compact := compactMemoryDetails(sections)
	if compact {
		report.EventFormat = "tuple"
	}
	for _, section := range sections {
		if len(section.records) == 0 {
			continue
		}
		nonempty = append(nonempty, section)
		count := len(section.records)
		maximum := memoryViewCounts{section.kind, count, count, count, count}
		marker, _ := json.Marshal(maximum)
		reserved += len("\n\n") + len(section.heading) + len("\n") + len(marker)
	}
	if len(nonempty) == 0 {
		return "", report
	}
	if compact {
		reserved += len("\n") + len(memoryDetailLegend)
	}
	if reserved > limit {
		report.Outcome = "budget_too_small"
		for _, section := range nonempty {
			report.Sections = append(report.Sections, memoryViewCounts{Kind: section.kind, Omitted: len(section.records)})
		}
		return "", report
	}
	remaining := limit - reserved
	seen := make(map[string]bool)
	var out strings.Builder
	out.WriteString(historicalMemoryScopeNotice)
	if compact {
		out.WriteByte('\n')
		out.WriteString(memoryDetailLegend)
	}
	report.Outcome = "complete"
	for _, section := range nonempty {
		counts := memoryViewCounts{Kind: section.kind}
		var lines strings.Builder
		for _, record := range section.records {
			id := ""
			if record.event != nil {
				id = record.event.EventID
			}
			if id != "" && seen[id] {
				counts.Duplicates++
				continue
			}
			line, shortened := encodeMemoryRecordFormat(record, remaining-len("\n- "), compact)
			if line == "" {
				counts.Omitted++
				report.Outcome = "partial"
				continue
			}
			lines.WriteString("\n- ")
			lines.WriteString(line)
			remaining -= len("\n- ") + len(line)
			counts.Admitted++
			if shortened {
				counts.Shortened++
				report.Outcome = "partial"
			}
			if id != "" {
				seen[id] = true
			}
		}
		marker, _ := json.Marshal(counts)
		out.WriteString("\n\n")
		out.WriteString(section.heading)
		out.WriteByte('\n')
		out.Write(marker)
		out.WriteString(lines.String())
		report.Sections = append(report.Sections, counts)
	}
	report.UsedBytes = out.Len()
	return out.String(), report
}
