package orchestration

import (
	"encoding/json"
	"errors"
	"io"
	"strings"

	"github.com/truvaagents/truva-g3/core"
)

// Call-local metadata for the built-in summarizer. Custom implementations keep
// the public core.EventSummarizer contract and do not fabricate this observation.
type summaryParseOutcome struct {
	Valid              map[string]core.StepSummary `json:"-"`
	Expected           int                         `json:"expected"`
	Accepted           int                         `json:"accepted"`
	Rejected           int                         `json:"rejected_entries"`
	EntitiesRejected   int                         `json:"rejected_entities"`
	CompleteArray      bool                        `json:"complete_array"`
	Truncated          bool                        `json:"truncated"`
	Outcome            string                      `json:"outcome"`
	EntityFields       summaryEntityCoverage       `json:"entity_fields"`
	BatchExcluded      int                         `json:"batch_excluded"`
	SummariesShortened int                         `json:"summaries_shortened"`
	batchExcludedIDs   map[string]bool
	entityFields       map[string]entityFieldObservation
}

// Only the derived prose is shortened. Raw completion evidence is retained by
// the debug recorder before parsing, and structured entities keep exact IDs.
func (result *summaryParseOutcome) boundSummaryProse(maxBytes int) {
	const marker = " [shortened]"
	for id, entry := range result.Valid {
		if len(entry.Summary) <= maxBytes {
			continue
		}
		entry.Summary = boundedSummaryResponse(entry.Summary, maxBytes-len(marker)) + marker
		result.Valid[id] = entry
		result.SummariesShortened++
	}
}

// Entity field coverage describes retained summaries, not entity grounding or
// backend writes. Custom summarizers have no Batch and therefore no observation.
type summaryEntityCoverage struct {
	Observed int `json:"observed"`
	Missing  int `json:"missing"`
	Empty    int `json:"empty"`
	Valid    int `json:"valid"`
	Invalid  int `json:"invalid"`
	Mixed    int `json:"mixed"`
	Accepted int `json:"accepted_entities"`
	Rejected int `json:"rejected_entities"`
}

type entityFieldObservation struct {
	Present    bool
	ValidArray bool
	Accepted   int
	Rejected   int
}

type memorySummaryCoverage struct {
	Source     string               `json:"source"`
	Eligible   int                  `json:"eligible"`
	Summarized int                  `json:"summarized"`
	Fallback   int                  `json:"fallback"`
	Batch      *summaryParseOutcome `json:"batch,omitempty"`
}

func newSummaryOutcome(batch []core.StepSummaryInput) summaryParseOutcome {
	return summaryParseOutcome{
		Valid: make(map[string]core.StepSummary), Expected: len(batch), Outcome: "fallback",
		entityFields: make(map[string]entityFieldObservation),
	}
}

func parseEventSummaries(content string, batch []core.StepSummaryInput) summaryParseOutcome {
	result := newSummaryOutcome(batch)
	expected := make(map[string]bool, len(batch))
	for _, step := range batch {
		expected[step.StepID] = true
	}
	content = strings.TrimSpace(content)
	missingFence := false
	if strings.HasPrefix(content, "```") {
		line, rest, found := strings.Cut(content, "\n")
		if !found || (strings.TrimSpace(line) != "```" && !strings.EqualFold(strings.TrimSpace(line), "```json")) {
			return result
		}
		content = strings.TrimSpace(rest)
		missingFence = !strings.HasSuffix(content, "```")
		if !missingFence {
			content = strings.TrimSpace(strings.TrimSuffix(content, "```"))
		}
	}
	if array, ok := completeSummaryArray(content); ok {
		content = array
	} else if !strings.HasPrefix(content, "[") {
		// Only a raw (or outer-fenced) array is a partial recovery boundary.
		return result
	}
	decoder := json.NewDecoder(strings.NewReader(content))
	opener, err := decoder.Token()
	if err != nil || opener != json.Delim('[') {
		return result
	}
	seen := make(map[string]bool)
	for decoder.More() {
		var raw json.RawMessage
		if err := decoder.Decode(&raw); err != nil {
			if truncatedSummaryJSON(err) {
				result.Truncated = true
				return finishSummaryOutcome(result)
			}
			return newSummaryOutcome(batch)
		}
		var fields map[string]json.RawMessage
		if json.Unmarshal(raw, &fields) != nil || fields == nil {
			result.Rejected++
			continue
		}
		var id, summary string
		if json.Unmarshal(fields["step_id"], &id) != nil || id == "" || !expected[id] {
			result.Rejected++
			continue
		}
		if seen[id] {
			delete(result.Valid, id)
			delete(result.entityFields, id)
			result.Rejected++
			continue
		}
		seen[id] = true
		if json.Unmarshal(fields["summary"], &summary) != nil || strings.TrimSpace(summary) == "" {
			result.Rejected++
			continue
		}
		entry := core.StepSummary{Summary: summary}
		entityJSON := fields["entities"]
		entities, observation := parseSummaryEntities(entityJSON)
		entry.Entities = entities
		result.entityFields[id] = observation
		// Preserve the legacy batch rejection/outcome semantics: null previously
		// decoded as a nil slice. Its shape failure is additive field coverage.
		if strings.TrimSpace(string(entityJSON)) != "null" {
			result.EntitiesRejected += observation.Rejected
		}
		result.Valid[id] = entry
	}
	if _, err := decoder.Token(); err != nil {
		if truncatedSummaryJSON(err) {
			result.Truncated = true
			return finishSummaryOutcome(result)
		}
		return newSummaryOutcome(batch)
	}
	var trailing json.RawMessage
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return newSummaryOutcome(batch)
	}
	result.CompleteArray = true
	result.Truncated = missingFence
	return finishSummaryOutcome(result)
}

// completeSummaryArray admits one complete array with optional plain prose on
// either side. Decode determines the boundary, including brackets in strings.
// Other JSON values/envelopes and ambiguous multiple arrays are not evidence.
// The returned substring is unchanged; this is not JSON or payload repair.
func completeSummaryArray(content string) (string, bool) {
	start := strings.IndexByte(content, '[')
	if start < 0 || !summaryEnvelopeProse(content[:start]) {
		return "", false
	}
	decoder := json.NewDecoder(strings.NewReader(content[start:]))
	var array json.RawMessage
	if decoder.Decode(&array) != nil {
		return "", false
	}
	end := start + int(decoder.InputOffset())
	if !summaryEnvelopeProse(content[end:]) {
		return "", false
	}
	return content[start:end], true
}

func summaryEnvelopeProse(s string) bool {
	s = strings.TrimSpace(s)
	return s == "" || (!strings.ContainsAny(s, "[]{}\"`") && !json.Valid([]byte(s)))
}

func parseSummaryEntities(raw json.RawMessage) ([]core.EntityRef, entityFieldObservation) {
	observation := entityFieldObservation{Present: len(raw) > 0}
	if !observation.Present {
		return nil, observation
	}
	var entries []json.RawMessage
	if json.Unmarshal(raw, &entries) != nil || entries == nil {
		observation.Rejected = 1
		return nil, observation
	}
	observation.ValidArray = true
	var entities []core.EntityRef
	for _, entry := range entries {
		var entity core.EntityRef
		if json.Unmarshal(entry, &entity) != nil || entity.Type == "" || entity.ID == "" {
			observation.Rejected++
			continue
		}
		entities = append(entities, entity)
		observation.Accepted++
	}
	return entities, observation
}

func truncatedSummaryJSON(err error) bool {
	// encoding/json implementations expose an unfinished value either as an
	// EOF or as this specific SyntaxError. Other syntax errors are not salvageable.
	var syntax *json.SyntaxError
	return errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) ||
		(errors.As(err, &syntax) && syntax.Error() == "unexpected end of JSON input")
}

func finishSummaryOutcome(result summaryParseOutcome) summaryParseOutcome {
	result.Accepted = len(result.Valid)
	result.EntityFields = summaryEntityCoverage{}
	for id := range result.Valid {
		observation := result.entityFields[id]
		result.EntityFields.Observed++
		result.EntityFields.Accepted += observation.Accepted
		result.EntityFields.Rejected += observation.Rejected
		switch {
		case !observation.Present:
			result.EntityFields.Missing++
		case !observation.ValidArray:
			result.EntityFields.Invalid++
		case observation.Rejected > 0 && observation.Accepted > 0:
			result.EntityFields.Mixed++
		case observation.Rejected > 0:
			result.EntityFields.Invalid++
		case observation.Accepted == 0:
			result.EntityFields.Empty++
		default:
			result.EntityFields.Valid++
		}
	}
	if result.Accepted > 0 {
		result.Outcome = "partial"
		if result.CompleteArray && !result.Truncated && result.Accepted == result.Expected && result.Rejected == 0 && result.EntitiesRejected == 0 {
			result.Outcome = "complete"
		}
	}
	return result
}
