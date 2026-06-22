package orchestration

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/truvaagents/truva-g3/core"
	"github.com/truvaagents/truva-g3/telemetry"
	"go.opentelemetry.io/otel/attribute"
)

const (
	// maxArrayInventoryItems caps the number of array items inventoried.
	// Even at ~80 bytes/item (smallest typical items), 500 items = 40 KB which
	// already fills most practical budgets. Prevents O(n log n) sort bloat for
	// very large arrays (10K+ items from paginated APIs).
	maxArrayInventoryItems = 500

	// minBackfillBudget is the minimum remaining budget (in bytes) after greedy
	// field selection to attempt backfilling a dropped string field. Below this
	// threshold, the truncated value would be too small to be useful to the LLM.
	minBackfillBudget = 512

	// minBackfillValueSize is the minimum JSON-serialized value budget (in bytes)
	// for a backfilled string field. Accounts for key overhead and wrapper nesting.
	// Below this, the truncated content is unlikely to contain meaningful data.
	minBackfillValueSize = 100

	// previewScoringLength is the maximum number of characters from a string
	// field's value to scan for keyword matches during scoring. Limits CPU cost
	// on large fields (e.g., 642KB stdout) while capturing enough context to
	// identify content relevance. 500 chars covers most JSON structure preambles
	// and log file headers where identifying keywords appear.
	previewScoringLength = 500

	// minBackfillRelevance is the minimum relevance score for a dropped field
	// to be considered for backfill. Fields below this threshold are likely
	// irrelevant to the user's query and including them may distract the
	// synthesis LLM (ACON, Oct 2025: compression improves accuracy when
	// irrelevant context is removed).
	//
	// scoreField base scores (no keyword signal):
	//   depth 0 scalar: 0.3 (isScalar) + 0.1 (depth-0) = 0.4 → passes (0.4 > 0.3)
	//   depth 1 scalar: 0.3 (isScalar) + 0.0             = 0.3 → excluded (≤ 0.3)
	//   depth 4 scalar: 0.3 (isScalar) - 0.1 (deep)      = 0.2 → excluded (≤ 0.3)
	//
	// Any keyword match (name: +1.5, path: +1.0, fuzzy: +0.3, content: +0.8)
	// pushes the score well above 0.3. This threshold filters nested fields
	// with zero keyword relevance — the most likely distractors.
	minBackfillRelevance = 0.3

	// maxInventoryDepth caps the recursion depth in buildFieldInventory and
	// buildArrayInventory. Prevents stack overflow on pathological inputs with
	// deeply nested JSON-in-strings (each deserializeStringValues unwrap adds
	// a nesting level). Real-world REST API JSON rarely exceeds 4-5 levels.
	maxInventoryDepth = 8
)

// StructuralTrimmer is the default ResultProcessor. Uses query-conditioned field selection.
// Research: ACON, CompactPrompt, SWE-Pruner.
type StructuralTrimmer struct {
	preserveKeys map[string]bool
	logger       core.Logger
}

// NewStructuralTrimmer creates a StructuralTrimmer with optional preserve keys.
func NewStructuralTrimmer(preserveKeys []string, logger core.Logger) *StructuralTrimmer {
	keySet := make(map[string]bool, len(preserveKeys))
	for _, k := range preserveKeys {
		keySet[strings.ToLower(k)] = true
	}
	return &StructuralTrimmer{preserveKeys: keySet, logger: logger}
}

type fieldEntry struct {
	path       string
	key        string
	value      interface{}
	size       int
	depth      int
	isScalar   bool
	relevance  float64
	arrayIndex int // -1 for non-array fields, 0+ for array items
	arrayTotal int // total items in parent array (for positional scoring)
}

// ProcessForPrompt trims a result to fit within maxBytes using query-conditioned field selection.
func (t *StructuralTrimmer) ProcessForPrompt(
	ctx context.Context, response string, maxBytes int, stepCtx ResultProcessorContext,
) string {
	if len(response) <= maxBytes {
		return response
	}

	var data interface{}
	if err := json.Unmarshal([]byte(response), &data); err != nil {
		result := t.trimPlainText(response, maxBytes, stepCtx.Instruction)
		degenerate, keptRatio := degenerateTrim(len(response), len(result))
		captureTrimMetadata(ctx, ResultTrimMetadata{
			OriginalBytes: len(response),
			TrimmedBytes:  len(result),
			Method:        "structural_text",
			Keywords:      extractKeywords(stepCtx.Instruction),
			Degenerate:    degenerate,
			KeptRatio:     keptRatio,
		})
		// Honest disclosure when the floor kept a non-representative fraction (no-op otherwise).
		return result + degenerateNote(len(response), len(result))
	}

	// Phase 5 Fix 2: Unwrap JSON-valued strings before inventory building so
	// buildFieldInventory's existing object/array recursion handles embedded
	// structures. Reuses deserializeStringValues (result_processor.go:83).
	data = deserializeStringValues(data)

	keywords := extractKeywords(stepCtx.Instruction)

	// Extract request_id from baggage for Jaeger correlation (DISTRIBUTED_TRACING_GUIDE.md Pattern 6)
	requestID := ""
	if bag := telemetry.GetBaggage(ctx); bag != nil {
		requestID = bag["request_id"]
	}

	telemetry.AddSpanEvent(ctx, "result_trim.structural",
		attribute.String("request_id", requestID),
		attribute.String("step_id", stepCtx.StepID),
		attribute.String("agent_name", stepCtx.AgentName),
		attribute.Int("original_bytes", len(response)),
		attribute.Int("budget_bytes", maxBytes),
		attribute.Int("keyword_count", len(keywords)),
	)

	if obj, ok := data.(map[string]interface{}); ok {
		result, fieldsKept, fieldsDropped, backfilledCount, thresholdSkipped, matchedPaths, degenerate, keptRatio := t.selectFieldsWithMeta(ctx, obj, maxBytes, len(response), keywords)
		captureTrimMetadata(ctx, ResultTrimMetadata{
			OriginalBytes:    len(response),
			TrimmedBytes:     len(result),
			Method:           "structural",
			FieldsKept:       fieldsKept,
			FieldsDropped:    fieldsDropped,
			BackfilledCount:  backfilledCount,
			ThresholdSkipped: thresholdSkipped,
			Keywords:         keywords,
			MatchedPaths:     matchedPaths,
			Degenerate:       degenerate,
			KeptRatio:        keptRatio,
		})
		if t.logger != nil {
			t.logger.DebugWithContext(ctx, "Structural trim completed (JSON object)", map[string]interface{}{
				"operation":      "result_trim",
				"request_id":     requestID,
				"step_id":        stepCtx.StepID,
				"agent_name":     stepCtx.AgentName,
				"original_bytes": len(response),
				"trimmed_bytes":  len(result),
				"keyword_count":  len(keywords),
			})
		}
		if registry := core.GetGlobalMetricsRegistry(); registry != nil {
			registry.Counter("orchestration.result_trim.triggered", "agent_name", stepCtx.AgentName)
			registry.Histogram("orchestration.result.original_size_bytes", float64(len(response)), "agent_name", stepCtx.AgentName)
			registry.Histogram("orchestration.result.trimmed_size_bytes", float64(len(result)), "agent_name", stepCtx.AgentName)
		}
		return result
	}

	if arr, ok := data.([]interface{}); ok {
		result, keptCount, totalCount := t.trimArray(arr, maxBytes)
		degenerate, keptRatio := degenerateTrim(len(response), len(result))
		captureTrimMetadata(ctx, ResultTrimMetadata{
			OriginalBytes: len(response),
			TrimmedBytes:  len(result),
			Method:        "structural_array",
			FieldsKept:    keptCount,
			FieldsDropped: totalCount - keptCount,
			Degenerate:    degenerate,
			KeptRatio:     keptRatio,
		})
		if t.logger != nil {
			t.logger.DebugWithContext(ctx, "Structural trim completed (JSON array)", map[string]interface{}{
				"operation":      "result_trim",
				"request_id":     requestID,
				"step_id":        stepCtx.StepID,
				"agent_name":     stepCtx.AgentName,
				"original_bytes": len(response),
				"trimmed_bytes":  len(result),
			})
		}
		return result + degenerateNote(len(response), len(result))
	}

	fallback := truncateResultBytes(response, maxBytes)
	degenerate, keptRatio := degenerateTrim(len(response), len(fallback))
	captureTrimMetadata(ctx, ResultTrimMetadata{
		OriginalBytes: len(response),
		TrimmedBytes:  len(fallback),
		Method:        "truncate",
		Degenerate:    degenerate,
		KeptRatio:     keptRatio,
	})
	return fallback + degenerateNote(len(response), len(fallback))
}

func (t *StructuralTrimmer) buildFieldInventory(obj map[string]interface{}, prefix string, depth int) []fieldEntry {
	if depth >= maxInventoryDepth {
		return nil
	}
	entries := make([]fieldEntry, 0, len(obj))
	for key, val := range obj {
		path := key
		if prefix != "" {
			path = prefix + "." + key
		}
		serialized, _ := json.Marshal(val)
		size := len(serialized) + len(key) + 4 // key + quotes + colon + comma

		entries = append(entries, fieldEntry{
			path: path, key: key, value: val,
			size: size, depth: depth, isScalar: isScalar(val),
			arrayIndex: -1,
		})

		// Recurse into large nested objects/arrays for deeper scoring
		if nested, ok := val.(map[string]interface{}); ok && size > 1024 {
			entries = append(entries, t.buildFieldInventory(nested, path, depth+1)...)
		} else if arr, ok := val.([]interface{}); ok && size > 1024 {
			entries = append(entries, t.buildArrayInventory(arr, path, depth+1)...)
		}
	}
	return entries
}

// buildArrayInventory decomposes a JSON array into individual selectable items.
// Each item competes in the same inventory as map fields, scored by position and content.
// Capped at maxArrayInventoryItems to prevent sort bloat on very large arrays.
func (t *StructuralTrimmer) buildArrayInventory(
	arr []interface{}, prefix string, depth int,
) []fieldEntry {
	if depth >= maxInventoryDepth {
		return nil
	}
	total := len(arr)
	if total > maxArrayInventoryItems {
		total = maxArrayInventoryItems
	}

	entries := make([]fieldEntry, 0, total)
	for i := 0; i < total; i++ {
		item := arr[i]
		path := fmt.Sprintf("%s[%d]", prefix, i)
		serialized, _ := json.Marshal(item)
		size := len(serialized) + 1 // item bytes + comma separator

		entries = append(entries, fieldEntry{
			path:       path,
			key:        fmt.Sprintf("%s[%d]", lastPathSegment(prefix), i),
			value:      item,
			size:       size,
			depth:      depth,
			isScalar:   isScalar(item),
			arrayIndex: i,
			arrayTotal: len(arr),
		})

		// Phase 5 Fix 1: Recurse into large object items — same pattern as
		// buildFieldInventory. Sub-fields compete with the atomic item entry;
		// ancestor/descendant checks in selectFieldsWithMeta prevent double-counting.
		if obj, ok := item.(map[string]interface{}); ok && size > 1024 {
			entries = append(entries, t.buildFieldInventory(obj, path, depth+1)...)
		}
	}
	return entries
}

// lastPathSegment returns the final segment of a dot-separated path.
// "data.news" → "news", "news" → "news".
func lastPathSegment(path string) string {
	if idx := strings.LastIndex(path, "."); idx >= 0 {
		return path[idx+1:]
	}
	return path
}

func (t *StructuralTrimmer) scoreField(entry fieldEntry, keywords []string) float64 {
	score := 0.0
	lowerKey := strings.ToLower(entry.key)
	lowerPath := strings.ToLower(entry.path)

	if t.preserveKeys[lowerKey] {
		score += 5.0
	}

	for _, kw := range keywords {
		if strings.Contains(lowerKey, kw) {
			score += 1.5
		} else if strings.Contains(lowerPath, kw) {
			score += 1.0
		} else {
			for _, part := range strings.Split(lowerKey, "_") {
				if len(part) >= 3 && len(kw) >= 3 && (strings.HasPrefix(part, kw[:3]) || strings.HasPrefix(kw, part[:3])) {
					score += 0.3
					break
				}
			}
		}
	}

	// Content-aware preview scoring: match keywords against the first
	// previewScoringLength characters of string field values. Catches cases
	// where field names are generic (e.g., "stdout", "output") but the value
	// contains query-relevant data. Weight (0.8) is lower than name match (1.5)
	// because content matches are probabilistic — a large JSON blob may contain
	// the keyword anywhere without being primarily about that topic.
	if entry.isScalar {
		if strVal, ok := entry.value.(string); ok && len(strVal) > 0 {
			previewLen := len(strVal)
			if previewLen > previewScoringLength {
				previewLen = previewScoringLength
			}
			lowerPreview := strings.ToLower(strVal[:previewLen])
			for _, kw := range keywords {
				if strings.Contains(lowerPreview, kw) {
					score += 0.8
					break // one content bonus per field, not per keyword
				}
			}
		}
	}

	// Array item: content-based keyword scoring + positional decay
	if entry.arrayIndex >= 0 {
		// Scan string values within object items for keyword matches.
		// E.g., {"title": "Q4 earnings report"} matches keyword "earn",
		// {"name": "Wireless Bluetooth Speaker"} matches keyword "bluetooth".
		if obj, ok := entry.value.(map[string]interface{}); ok {
			score += scoreObjectContent(obj, keywords)
		}
		// Positional decay: earlier items score higher (APIs return by relevance/recency).
		// Range: +0.5 (first item) to ~0.0 (last item).
		if entry.arrayTotal > 0 {
			score += 0.5 * (1.0 - float64(entry.arrayIndex)/float64(entry.arrayTotal))
		}
	}

	if entry.isScalar {
		score += 0.3
	}
	if entry.depth == 0 {
		score += 0.1
	}
	if entry.depth > 3 {
		score -= 0.1
	}
	return score
}

// scoreObjectContent scans string field values of a JSON object for keyword matches.
// Returns a bonus score based on the number of keyword hits in the object's content.
// Capped at 1.0 to avoid over-weighting content-rich items relative to key matches.
func scoreObjectContent(obj map[string]interface{}, keywords []string) float64 {
	if len(keywords) == 0 {
		return 0
	}
	hits := 0
	for _, val := range obj {
		s, ok := val.(string)
		if !ok {
			continue
		}
		lower := strings.ToLower(s)
		for _, kw := range keywords {
			if strings.Contains(lower, kw) {
				hits++
			}
		}
	}
	// Cap at 1.0 total content bonus
	bonus := float64(hits) * 0.2
	if bonus > 1.0 {
		bonus = 1.0
	}
	return bonus
}

// selectFieldsWithMeta returns the trimmed output plus selection counters, the matched
// paths, and the degenerate decision (degenerate, keptRatio) computed on the CONTENT
// length — the same basis used for the in-prompt "severely reduced" annotation — so the
// caller stamps metadata that agrees with what the synthesizing LLM is told.
func (t *StructuralTrimmer) selectFieldsWithMeta(ctx context.Context, obj map[string]interface{}, maxBytes, originalBytes int, keywords []string) (string, int, int, int, int, []string, bool, float64) {
	inventory := t.buildFieldInventory(obj, "", 0)
	for i := range inventory {
		inventory[i].relevance = t.scoreField(inventory[i], keywords)
	}

	// Whole-unit selection (Phase 2): sort by depth ASCENDING, then size DESCENDING —
	// never value-density.
	//
	// Semantic relevance to a natural-language query is the LLM's job now: distillation
	// is the primary path and this StructuralTrimmer is only the pre-filter that bounds
	// size for the LLM and the fail-open floor. A deterministic algorithm cannot decide
	// what a query means, so this sort makes no relevance cut. Instead:
	//   1. depth ascending — whole top-level fields and whole array items are considered
	//      before any of their nested leaves, so small-but-meaningful fields (a ticker
	//      symbol, a metric block) and whole records are kept as units rather than being
	//      crowded out by, or decomposed into, deep scaffolding;
	//   2. size descending within a depth — when the budget forces a descent below a
	//      unit that doesn't fit, substantive content (a 2 KB log line) is preferred over
	//      tiny scaffolding (a 15 B stream label). This is the exact inverse of the old
	//      value-density defect, which packed labels and dropped every log line.
	// Combined with the ancestor/descendant dedup below, a unit that fits is kept whole
	// (everything inside it); only a unit too big to fit is descended into.
	//
	// Relevance still informs the secondary backfill pass and key ordering, but never
	// the primary cut. Positional order is restored in reconstructHierarchy (array items
	// re-sorted by original index), so selection order does not affect output order.
	sort.Slice(inventory, func(i, j int) bool {
		if inventory[i].depth != inventory[j].depth {
			return inventory[i].depth < inventory[j].depth
		}
		if inventory[i].size != inventory[j].size {
			return inventory[i].size > inventory[j].size
		}
		// Stable tiebreakers for equal-size units: original array position, then path.
		if inventory[i].arrayIndex != inventory[j].arrayIndex {
			return inventory[i].arrayIndex < inventory[j].arrayIndex
		}
		return inventory[i].path < inventory[j].path
	})

	// Step 3 (§4.4.3 / RESEARCH §7.2): Budget-constrained greedy selection.
	// All inventoried fields at all depths compete equally.
	// Ancestor/descendant checks prevent double-counting:
	//   - If a parent is already selected, skip its children (included via parent).
	//   - If children are already selected, skip the parent (would re-include everything).
	selectedSet := make(map[string]bool)         // fast lookup for ancestor/descendant checks
	var selectedPaths []string                   // ordered list for annotation
	wrapperKeys := make(map[string]bool)         // intermediate wrapper paths already budgeted
	arraysWithWholeItem := make(map[string]bool) // arrays from which a whole item was kept
	budgetUsed := 2                              // "{}" root wrapper
	droppedCount := 0

	for _, entry := range inventory {
		// Ancestor check: skip if a parent path is already selected.
		if hasAncestor(entry.path, selectedSet) {
			continue
		}
		// Descendant check: skip if a child under this entry is already selected.
		if !entry.isScalar && hasDescendant(entry.path, selectedSet) {
			continue
		}
		// Phase 2 whole-unit guard: once whole items of an array have been kept, do not
		// also pack partial SUB-FIELDS of that array's other (unselected) items into the
		// leftover budget — that reintroduces the leaf scatter this phase removes (e.g.
		// keeping {"id":...} from 40 records instead of a few whole records). Whole items
		// of the array remain eligible; only their individual leaves are suppressed.
		if ap := containingArrayPath(entry.path); ap != "" && arraysWithWholeItem[ap] {
			droppedCount++
			continue
		}

		// Split once for both overhead calculation and wrapper marking.
		var parts []string
		if entry.depth > 0 {
			parts = strings.Split(entry.path, ".")
		}

		// Calculate wrapper overhead for nested fields.
		// When selecting "data.metric" (depth 1), the output needs the intermediate
		// "data":{} wrapper. Each new intermediate path costs len(key) + 5 bytes.
		overhead := 0
		if entry.depth > 0 {
			for i := 0; i < len(parts)-1; i++ {
				ancestorPath := strings.Join(parts[:i+1], ".")
				if !wrapperKeys[ancestorPath] && !selectedSet[ancestorPath] {
					overhead += len(parts[i]) + 5
				}
			}
		}

		if budgetUsed+entry.size+overhead <= maxBytes {
			selectedSet[entry.path] = true
			selectedPaths = append(selectedPaths, entry.path)
			budgetUsed += entry.size + overhead
			// Mark intermediate wrappers so subsequent siblings don't re-pay.
			if entry.depth > 0 {
				for i := 0; i < len(parts)-1; i++ {
					wrapperKeys[strings.Join(parts[:i+1], ".")] = true
				}
			}
			// Record that this array yielded a whole item, so its other items' leaves
			// are suppressed above (whole-unit guard).
			if entry.arrayIndex >= 0 {
				arraysWithWholeItem[arrayPathOf(entry.path)] = true
			}
		} else {
			droppedCount++
		}
	}

	// Phase 3.5: Multi-field greedy backfill — recover dropped string fields
	// into remaining budget, highest-relevance first. Only strings can be safely
	// truncated (objects/arrays would produce invalid JSON if cut mid-way).
	// Algorithm: fractional knapsack — provably optimal for value-density ordering.
	backfilledCount := 0
	thresholdSkipped := 0
	valueOverrides := make(map[string]interface{})
	remainingBudget := maxBytes - budgetUsed
	if remainingBudget > minBackfillBudget && droppedCount > 0 {
		// Collect all dropped string candidates
		type backfillCandidate struct {
			entry  *fieldEntry
			srcStr string
		}
		var candidates []backfillCandidate
		for i := range inventory {
			e := &inventory[i]
			if selectedSet[e.path] || hasAncestor(e.path, selectedSet) {
				continue
			}
			if !e.isScalar {
				continue
			}
			// Whole-unit guard (see greedy loop): don't backfill an array item's leaf
			// when whole items of that array were already kept.
			if ap := containingArrayPath(e.path); ap != "" && arraysWithWholeItem[ap] {
				continue
			}
			srcVal := navigateToValue(obj, e.path)
			if s, ok := srcVal.(string); ok {
				candidates = append(candidates, backfillCandidate{entry: e, srcStr: s})
			}
		}

		// Sort by relevance descending (highest-priority fields get budget first)
		sort.Slice(candidates, func(i, j int) bool {
			return candidates[i].entry.relevance > candidates[j].entry.relevance
		})

		// Iterate candidates, each taking what it needs from shrinking budget
		for i, cand := range candidates {
			if remainingBudget <= minBackfillBudget {
				break
			}

			// Skip candidates at or below minimum relevance threshold.
			// Since candidates are sorted by relevance descending, once we hit
			// one at/below threshold, all remaining candidates are also below — break.
			if cand.entry.relevance <= minBackfillRelevance {
				thresholdSkipped = len(candidates) - i
				break
			}

			// Calculate wrapper overhead for this field's nesting
			overhead := 0
			if cand.entry.depth > 0 {
				parts := strings.Split(cand.entry.path, ".")
				for pi := 0; pi < len(parts)-1; pi++ {
					ancestorPath := strings.Join(parts[:pi+1], ".")
					if !wrapperKeys[ancestorPath] && !selectedSet[ancestorPath] {
						overhead += len(parts[pi]) + 5
					}
				}
			}

			keyOverhead := len(cand.entry.key) + 4
			valueBudget := remainingBudget - overhead - keyOverhead
			if valueBudget <= minBackfillValueSize {
				continue
			}

			// Check if the field fits entirely (no truncation needed)
			fullSerialized, _ := json.Marshal(cand.srcStr)
			if len(fullSerialized)+overhead+keyOverhead <= remainingBudget {
				// Include at full size — no wasteful truncation
				valueOverrides[cand.entry.path] = cand.srcStr
				actualCost := len(fullSerialized) + overhead + keyOverhead
				selectedSet[cand.entry.path] = true
				selectedPaths = append(selectedPaths, cand.entry.path)
				remainingBudget -= actualCost
				budgetUsed += actualCost
				droppedCount--
				backfilledCount++

				if cand.entry.depth > 0 {
					parts := strings.Split(cand.entry.path, ".")
					for pi := 0; pi < len(parts)-1; pi++ {
						wrapperKeys[strings.Join(parts[:pi+1], ".")] = true
					}
				}
				continue
			}

			// Binary search for max raw string length whose JSON-serialized
			// size (including quotes) fits within valueBudget.
			lo, hi := 0, len(cand.srcStr)
			for lo < hi {
				mid := (lo + hi + 1) / 2
				candidate := truncateResultBytes(cand.srcStr, mid)
				serialized, _ := json.Marshal(candidate)
				if len(serialized) <= valueBudget {
					lo = mid
				} else {
					hi = mid - 1
				}
			}

			if lo > 0 {
				truncatedVal := truncateResultBytes(cand.srcStr, lo)
				valueOverrides[cand.entry.path] = truncatedVal
				selectedSet[cand.entry.path] = true
				selectedPaths = append(selectedPaths, cand.entry.path)

				serializedVal, _ := json.Marshal(truncatedVal)
				actualCost := len(serializedVal) + overhead + keyOverhead
				remainingBudget -= actualCost
				budgetUsed += actualCost
				droppedCount--
				backfilledCount++

				if cand.entry.depth > 0 {
					parts := strings.Split(cand.entry.path, ".")
					for pi := 0; pi < len(parts)-1; pi++ {
						wrapperKeys[strings.Join(parts[:pi+1], ".")] = true
					}
				}
			}
		}
	}

	// Note: No fallback needed. The size-descending primary sort keeps whole units
	// (largest substantive content first); the backfill pass below then recovers
	// dropped string fields into any leftover budget, highest-relevance first.

	// Step 4 (§4.4.3 / RESEARCH §7.2): Hierarchy reconstruction.
	// Selected fields at any depth are placed back into their original nesting.
	// Build path→relevance map for relevance-ordered serialization.
	// Only populated when there are multiple selected fields (single-field
	// output doesn't benefit from ordering).
	var fieldRelevance map[string]float64
	if len(selectedPaths) > 1 {
		fieldRelevance = make(map[string]float64, len(inventory))
		for _, e := range inventory {
			fieldRelevance[e.path] = e.relevance
		}
	}

	output, err := marshalOrdered(reconstructHierarchy(obj, selectedSet, valueOverrides), fieldRelevance)
	if err != nil {
		return truncateResultBytes(fmt.Sprintf("%v", obj), maxBytes), 0, 0, 0, 0, nil, false, 1
	}

	// Safety check: if wrapper overhead estimation was slightly off, batch-remove
	// fields using size estimates, then reconstruct once. Avoids O(n²)
	// re-serialization from per-iteration reconstructHierarchy + json.Marshal.
	if len(output) > maxBytes && len(selectedPaths) > 0 {
		// Build size lookup from inventory for removal estimates.
		sizeOf := make(map[string]int, len(inventory))
		for _, e := range inventory {
			sizeOf[e.path] = e.size
		}
		overshoot := len(output) - maxBytes
		for overshoot > 0 && len(selectedPaths) > 0 {
			removePath := selectedPaths[len(selectedPaths)-1]
			selectedPaths = selectedPaths[:len(selectedPaths)-1]
			delete(selectedSet, removePath)
			droppedCount++
			overshoot -= sizeOf[removePath]
		}
		output, _ = marshalOrdered(reconstructHierarchy(obj, selectedSet, valueOverrides), fieldRelevance)
		// Final guard: if size estimate was wrong, remove one more.
		if len(output) > maxBytes && len(selectedPaths) > 0 {
			removePath := selectedPaths[len(selectedPaths)-1]
			selectedPaths = selectedPaths[:len(selectedPaths)-1]
			delete(selectedSet, removePath)
			droppedCount++
			output, _ = marshalOrdered(reconstructHierarchy(obj, selectedSet, valueOverrides), fieldRelevance)
		}
	}

	candidateCount := len(selectedPaths) + droppedCount

	// Count array items in selection for annotation
	arrayItemCount := 0
	for _, p := range selectedPaths {
		if strings.Contains(p, "[") {
			arrayItemCount++
		}
	}

	if t.logger != nil {
		requestID := ""
		if bag := telemetry.GetBaggage(ctx); bag != nil {
			requestID = bag["request_id"]
		}
		t.logger.DebugWithContext(ctx, "Field selection completed", map[string]interface{}{
			"operation":         "result_trim.select_fields",
			"request_id":        requestID,
			"inventory_size":    len(inventory),
			"candidates_total":  candidateCount,
			"fields_kept":       len(selectedPaths),
			"fields_dropped":    droppedCount,
			"backfilled_count":  backfilledCount,
			"threshold_skipped": thresholdSkipped,
			"array_items_kept":  arrayItemCount,
			"budget_bytes":      maxBytes,
			"output_bytes":      len(output),
			"keyword_count":     len(keywords),
		})
	}

	var annotation string
	backfillNote := ""
	if backfilledCount > 0 {
		backfillNote = fmt.Sprintf(" (%d backfilled)", backfilledCount)
	}
	// Degenerate trim: so little of the source survived that the kept fields are
	// non-representative. Computed on the content length (output, excluding this
	// annotation) and returned so the caller's metadata flag matches this decision.
	// The honest disclosure replaces the neutral "[trimmed: …]" note, which implies
	// coverage and invites a false-negative inference (e.g. "no ERROR entries found")
	// about content that was never actually examined.
	degenerate, keptRatio := degenerateTrim(originalBytes, len(output))
	if degenerate {
		annotation = fmt.Sprintf(
			"\n[severely reduced: kept %d of ~%d bytes (%d/%d items%s); most content omitted — "+
				"treat anything NOT shown as UNKNOWN, do not infer it is absent]",
			len(output), originalBytes, len(selectedPaths), candidateCount, backfillNote)
	} else if arrayItemCount > 0 {
		annotation = fmt.Sprintf("\n[trimmed: %d/%d entries kept%s (%d array items), %d dropped]",
			len(selectedPaths), candidateCount, backfillNote, arrayItemCount, droppedCount)
	} else {
		annotation = fmt.Sprintf("\n[trimmed: %d/%d fields kept%s, %d dropped, matched: %s]",
			len(selectedPaths), candidateCount, backfillNote, droppedCount,
			truncateString(strings.Join(selectedPaths, ", "), 200))
	}

	if len(output)+len(annotation) <= maxBytes {
		return string(output) + annotation, len(selectedPaths), droppedCount, backfilledCount, thresholdSkipped, selectedPaths, degenerate, keptRatio
	}
	return string(output), len(selectedPaths), droppedCount, backfilledCount, thresholdSkipped, selectedPaths, degenerate, keptRatio
}

func (t *StructuralTrimmer) trimArray(arr []interface{}, maxBytes int) (string, int, int) {
	var result []interface{}
	budgetUsed := 2
	for _, item := range arr {
		serialized, _ := json.Marshal(item)
		if budgetUsed+len(serialized)+1 > maxBytes {
			break
		}
		result = append(result, item)
		budgetUsed += len(serialized) + 1
	}
	output, _ := json.Marshal(result)
	annotation := fmt.Sprintf("\n[trimmed: %d/%d items]", len(result), len(arr))
	if len(output)+len(annotation) <= maxBytes {
		return string(output) + annotation, len(result), len(arr)
	}
	return string(output), len(result), len(arr)
}

func (t *StructuralTrimmer) trimPlainText(text string, maxBytes int, instruction string) string {
	keywords := extractKeywords(instruction)
	if len(keywords) == 0 {
		return truncateResultBytes(text, maxBytes)
	}
	sentences := strings.FieldsFunc(text, func(r rune) bool { return r == '.' || r == '!' || r == '?' })
	if len(sentences) == 0 {
		return truncateResultBytes(text, maxBytes)
	}

	type scored struct {
		text  string
		score float64
		idx   int
	}
	items := make([]scored, 0, len(sentences))
	for i, s := range sentences {
		s = strings.TrimSpace(s)
		if len(s) == 0 {
			continue
		}
		sc := 0.0
		lower := strings.ToLower(s)
		for _, kw := range keywords {
			if strings.Contains(lower, kw) {
				sc += 1.0
			}
		}
		items = append(items, scored{text: s, score: sc, idx: i})
	}

	sort.Slice(items, func(i, j int) bool {
		if items[i].score != items[j].score {
			return items[i].score > items[j].score
		}
		return items[i].idx < items[j].idx
	})

	var selected []scored
	budgetUsed := 0
	for _, s := range items {
		if budgetUsed+len(s.text)+2 > maxBytes-50 {
			break
		}
		selected = append(selected, s)
		budgetUsed += len(s.text) + 2
	}

	sort.Slice(selected, func(i, j int) bool { return selected[i].idx < selected[j].idx })

	var sb strings.Builder
	for _, s := range selected {
		sb.WriteString(s.text)
		sb.WriteString(". ")
	}
	fmt.Fprintf(&sb, "\n[trimmed: %d/%d sentences]", len(selected), len(sentences))
	return sb.String()
}

func isScalar(v interface{}) bool {
	switch v.(type) {
	case string, float64, bool, nil:
		return true
	}
	return false
}

// hasAncestor reports whether any ancestor of path is in the selected set.
// An ancestor is any prefix up to a "." or "[" separator.
// Examples: "data.news[0]" has ancestors "data" (at ".") and "data.news" (at "[").
func hasAncestor(path string, selected map[string]bool) bool {
	for i := 0; i < len(path); i++ {
		if (path[i] == '.' || path[i] == '[') && selected[path[:i]] {
			return true
		}
	}
	return false
}

// arrayPathOf returns the path of the array that owns an array-item path.
// "records[3]" -> "records"; "streams[0].entries[2]" -> "streams[0].entries".
// Returns "" if the path is not an array item.
func arrayPathOf(itemPath string) string {
	openIdx := strings.LastIndex(itemPath, "[")
	if openIdx < 0 {
		return ""
	}
	return itemPath[:openIdx]
}

// containingArrayPath returns the path of the array whose item directly contains the
// given SUB-FIELD path, or "" if the path is not a sub-field of an array item.
// "records[3].id" -> "records"; "s[0].entries[2].line" -> "s[0].entries";
// "records[3]" -> "" (an item itself, not a sub-field); "data.metric" -> "" (no array).
func containingArrayPath(path string) string {
	closeIdx := strings.LastIndex(path, "]")
	// No array bracket, or the path ends at "]" (it is an item, not a sub-field of one).
	if closeIdx < 0 || closeIdx == len(path)-1 {
		return ""
	}
	// A sub-field of the array item ending at closeIdx must continue with ".suffix".
	if path[closeIdx+1] != '.' {
		return ""
	}
	openIdx := strings.LastIndex(path[:closeIdx], "[")
	if openIdx < 0 {
		return ""
	}
	return path[:openIdx]
}

// hasDescendant reports whether any path in selected is a child of prefix.
// Checks both "." children (map fields) and "[" children (array items).
// Examples: "data.news" has descendants "data.news.extra" and "data.news[0]".
func hasDescendant(prefix string, selected map[string]bool) bool {
	dotPrefix := prefix + "."
	bracketPrefix := prefix + "["
	for p := range selected {
		if strings.HasPrefix(p, dotPrefix) || strings.HasPrefix(p, bracketPrefix) {
			return true
		}
	}
	return false
}

// marshalOrdered serializes a map with keys ordered by relevance (descending).
// When fieldRelevance is nil, falls back to standard json.Marshal (alphabetical).
// Uses manual JSON construction to control key order — json.Marshal always alphabetizes.
//
// Research: Lost in the Middle (ICLR 2025) — LLMs attend most to data at
// the beginning and end of context. Placing highest-relevance fields first
// aligns data position with attention distribution.
func marshalOrdered(obj map[string]interface{}, fieldRelevance map[string]float64) ([]byte, error) {
	if fieldRelevance == nil || len(obj) <= 1 {
		return json.Marshal(obj)
	}

	// Sort top-level keys by maximum relevance of any descendant path
	keys := make([]string, 0, len(obj))
	for k := range obj {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		ri := maxRelevanceForKey(keys[i], fieldRelevance)
		rj := maxRelevanceForKey(keys[j], fieldRelevance)
		if ri != rj {
			return ri > rj
		}
		return keys[i] < keys[j] // stable tiebreaker
	})

	// Build ordered JSON manually
	var buf bytes.Buffer
	buf.WriteByte('{')
	for i, k := range keys {
		if i > 0 {
			buf.WriteByte(',')
		}
		keyBytes, _ := json.Marshal(k)
		buf.Write(keyBytes)
		buf.WriteByte(':')
		valBytes, err := json.Marshal(obj[k])
		if err != nil {
			return nil, err
		}
		buf.Write(valBytes)
	}
	buf.WriteByte('}')
	return buf.Bytes(), nil
}

// maxRelevanceForKey returns the highest relevance score among all paths
// that start with the given top-level key.
func maxRelevanceForKey(key string, fieldRelevance map[string]float64) float64 {
	best := 0.0
	prefix := key + "."
	for path, rel := range fieldRelevance {
		if path == key || strings.HasPrefix(path, prefix) {
			if rel > best {
				best = rel
			}
		}
	}
	return best
}

// reconstructHierarchy builds a map containing only the selected fields, preserving the
// original nesting structure. It walks each selected path through splitPath — the same
// grammar navigateToValue reads — so it handles arbitrary nesting, including arrays inside
// arrays (e.g. "streams[0].entries[2]" → {"streams":[{"entries":[...]}]}), not just one
// array level. Array elements become dense arrays sorted by original index (deterministic
// despite Go's random map iteration; the dense positions renumber the original indices).
func reconstructHierarchy(obj map[string]interface{}, selectedPaths map[string]bool, valueOverrides map[string]interface{}) map[string]interface{} {
	// reconNode is an interior node (object children by key and/or array elements by index)
	// or a leaf carrying a selected value. The ancestor/descendant dedup in selection means
	// a node is never both, so a leaf takes precedence when building output.
	type reconNode struct {
		leaf     bool
		value    interface{}
		children map[string]*reconNode // object fields
		elems    map[int]*reconNode    // sparse array indices (densified on build)
	}
	newNode := func() *reconNode {
		return &reconNode{children: map[string]*reconNode{}, elems: map[int]*reconNode{}}
	}
	root := newNode()

	for path := range selectedPaths {
		srcVal := navigateToValue(obj, path)
		if valueOverrides != nil {
			if override, ok := valueOverrides[path]; ok {
				srcVal = override
			}
		}
		segments := splitPath(path)
		cur := root
		for i, seg := range segments {
			var next *reconNode
			if seg.index >= 0 {
				if next = cur.elems[seg.index]; next == nil {
					next = newNode()
					cur.elems[seg.index] = next
				}
			} else {
				if next = cur.children[seg.key]; next == nil {
					next = newNode()
					cur.children[seg.key] = next
				}
			}
			if i == len(segments)-1 {
				next.leaf = true
				next.value = srcVal
			}
			cur = next
		}
	}

	// build converts a node into its JSON value: a leaf's value, a dense array (elements in
	// ascending original-index order), or an object.
	var build func(n *reconNode) interface{}
	build = func(n *reconNode) interface{} {
		if n.leaf {
			return n.value
		}
		if len(n.elems) > 0 {
			idxs := make([]int, 0, len(n.elems))
			for idx := range n.elems {
				idxs = append(idxs, idx)
			}
			sort.Ints(idxs)
			arr := make([]interface{}, 0, len(idxs))
			for _, idx := range idxs {
				arr = append(arr, build(n.elems[idx]))
			}
			return arr
		}
		m := make(map[string]interface{}, len(n.children))
		for k, child := range n.children {
			m[k] = build(child)
		}
		return m
	}

	// Top-level paths always begin with a map key, so the root is an object.
	result := make(map[string]interface{}, len(root.children))
	for k, child := range root.children {
		result[k] = build(child)
	}
	return result
}

// navigateToValue retrieves a value from a nested map/array structure using a path.
// Handles both dot-separated map paths ("data.metric") and array paths ("data.news[5]").
func navigateToValue(obj map[string]interface{}, path string) interface{} {
	var srcVal interface{} = obj
	for _, segment := range splitPath(path) {
		switch v := srcVal.(type) {
		case map[string]interface{}:
			srcVal = v[segment.key]
		case []interface{}:
			if segment.index >= 0 && segment.index < len(v) {
				srcVal = v[segment.index]
			} else {
				return nil
			}
		default:
			return nil
		}
	}
	return srcVal
}

// pathSegment represents one segment of a hierarchical path.
type pathSegment struct {
	key   string // map key (e.g., "data", "news")
	index int    // array index (-1 for map segments)
}

// splitPath breaks "data.news[5].headline" into segments:
// [{key:"data", index:-1}, {key:"news", index:-1}, {key:"", index:5}, {key:"headline", index:-1}]
func splitPath(path string) []pathSegment {
	var segments []pathSegment
	for _, part := range strings.Split(path, ".") {
		if bracketIdx := strings.Index(part, "["); bracketIdx >= 0 {
			// "news[5]" → map key "news" + array index 5
			mapKey := part[:bracketIdx]
			if mapKey != "" {
				segments = append(segments, pathSegment{key: mapKey, index: -1})
			}
			idxStr := part[bracketIdx+1 : len(part)-1] // strip "[" and "]"
			idx := 0
			for _, c := range idxStr {
				idx = idx*10 + int(c-'0')
			}
			segments = append(segments, pathSegment{index: idx})
		} else {
			segments = append(segments, pathSegment{key: part, index: -1})
		}
	}
	return segments
}
