package orchestration

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"
)

// ResultProcessor transforms step results before they are embedded in prompts.
// Default implementation: StructuralTrimmer (see structural_trimmer.go).
// Developers implement this for domain-specific trimming.
//
// Contract for custom implementations (Phase 16 honesty pipeline):
//   - Whenever processing LOSES content (drops, truncation, sampling), call
//     captureTrimMetadata with ContentLost: true — downstream disclosure gating (the
//     distiller's partial-source note, coverage accounting, cache-envelope auditing) keys
//     exclusively on that signal; an implementation that skips it makes real loss read as
//     full coverage with no disclosure anywhere.
//   - Any trailing annotation appended to the output must be a single line, start with a
//     prefix registered in annotationPrefixes, and end with "]" — the agent-input guard
//     strips exactly that shape before re-parsing; an unregistered or multi-line note makes
//     the re-parse fail and the guard fail open with the untrimmed value.
type ResultProcessor interface {
	ProcessForPrompt(ctx context.Context, result string, maxBytes int, stepContext ResultProcessorContext) string
}

// EffectiveSizer lets a ResultProcessor report the size a raw result will occupy in the final prompt
// AFTER processing. A distilling processor compresses any result >= its threshold to ~TargetSize
// regardless of raw size, so allocating raw bytes mis-models cost (Phase 9). ProcessMultipleForBudget
// budgets against this when the processor implements it; processors that do not (e.g. the bare
// StructuralTrimmer) are budgeted at raw size — identical to pre-Phase-9 behaviour.
type EffectiveSizer interface {
	EffectiveSize(rawSize int) int
}

// ResultProcessorContext provides step metadata for context-aware trimming.
type ResultProcessorContext struct {
	StepID      string // "step-3"
	AgentName   string // Agent that produced this result
	Capability  string // Optional: not populated in default synthesis path (StepResult has no Capability field). Set by custom callers or when integrating with capability-aware executors.
	Instruction string // Step instruction — the immediate task this result was fetched for
	// OriginalQuery is the end-user's request for the whole run. It is the PRIMARY relevance
	// signal for an LLM distiller: a result is selected for what answers the user's goal, not just
	// the mechanical step instruction. Empty when the caller has no query in scope.
	OriginalQuery string
}

// ResultTrimConfig configures automatic result trimming for prompt construction.
type ResultTrimConfig struct {
	Enabled                      bool     `json:"enabled"`
	MaxResultBytes               int      `json:"max_result_bytes"`
	MaxTotalPromptBytes          int      `json:"max_total_prompt_bytes"`
	MaxMicroResolutionBytes      int      `json:"max_micro_resolution_bytes"`      // Phase 5: budget for micro-resolution source data
	MaxAgentInputBytes           int      `json:"max_agent_input_bytes"`           // Phase 8: per-parameter budget for agent/tool HTTP calls
	SchemaGuidedMappingThreshold int      `json:"schema_guided_mapping_threshold"` // Phase 10: source size for schema-guided mapping
	PreserveKeys                 []string `json:"preserve_keys,omitempty"`
}

// ResultDistillConfig configures opt-in LLM-based result distillation.
// Two-stage pipeline: structural pre-filter → LLM distill.
type ResultDistillConfig struct {
	Enabled          bool `json:"enabled"`           // DefaultConfig: true (default-on, opt-out)
	DistillThreshold int  `json:"distill_threshold"` // Min bytes to trigger distillation. DefaultConfig: 16384
	PreFilterBudget  int  `json:"prefilter_budget"`  // StructuralTrimmer budget before LLM. DefaultConfig: 131072 (128 KB)
	TargetSize       int  `json:"target_size"`       // LLM output target size. DefaultConfig: 4096
	// Model overrides the LLM model for distillation calls. DefaultConfig sets
	// this to the portable "fast" alias; an empty string = use the AIClient's
	// default model.
	//
	// When using a ChainClient (multi-provider failover), use a portable
	// alias ("fast", "default", "smart") instead of a concrete model name.
	// Concrete names are provider-specific and will break failover — the
	// ChainClient classifies 404 as a non-retryable client error and stops
	// immediately without trying the next provider.
	//
	// Env: TRUVAG3_RESULT_DISTILL_MODEL
	Model string `json:"model,omitempty"`
	// CacheTTL is how long a distillation result stays cached. The cache is keyed by
	// (result content + instruction + query + budget) plus a config salt (prompt
	// version, model, target size, pre-filter budget, AI-options override) and is
	// fail-open (a nil cache disables it with no overhead). Reuses the shared-memory
	// digest cache pattern so scheduled and repetitive runs — the worst offenders for
	// redundant LLM cost — become cache hits.
	//
	// Env: TRUVAG3_RESULT_DISTILL_CACHE_TTL
	CacheTTL time.Duration `json:"cache_ttl,omitempty"`
	// CompactionDeadline bounds the wall-clock time a single compaction may spend in the
	// synthesis hot path. On timeout the call fails open: the single-call path falls back
	// to the structural floor, and the map-reduce path returns the chunks that completed
	// plus an honest "partial" disclosure. Zero disables the deadline. Set it under the
	// HTTP gateway timeout that would otherwise cut a long synchronous request.
	//
	// Env: TRUVAG3_RESULT_DISTILL_DEADLINE
	CompactionDeadline time.Duration `json:"compaction_deadline,omitempty"`
	// ModelContextTokens is the usable context (in tokens) of the compaction model.
	// Results estimated above this are chunked and map-reduced instead of sent in one
	// call. Tied to the model tier, not a concrete model id, so it stays ChainClient-safe.
	//
	// Env: TRUVAG3_RESULT_DISTILL_CONTEXT_TOKENS
	ModelContextTokens int `json:"model_context_tokens,omitempty"`
	// MapConcurrency caps how many chunks are compacted concurrently in the map-reduce
	// path. <= 0 falls back to the default.
	//
	// Env: TRUVAG3_RESULT_DISTILL_MAP_CONCURRENCY
	MapConcurrency int `json:"map_concurrency,omitempty"`
}

// ResultTrimMetadata captures what happened during a single trim operation.
// Stored in StepResult.Metadata["result_trim"] for registry viewer visibility.
type ResultTrimMetadata struct {
	OriginalBytes    int      `json:"original_bytes"`
	TrimmedBytes     int      `json:"trimmed_bytes"`
	Method           string   `json:"method"` // "structural", "structural_array", "structural_text", "truncate", "distill"
	FieldsKept       int      `json:"fields_kept,omitempty"`
	FieldsDropped    int      `json:"fields_dropped,omitempty"`
	BackfilledCount  int      `json:"backfilled_count,omitempty"`  // Phase 4A: number of dropped fields recovered via backfill
	ThresholdSkipped int      `json:"threshold_skipped,omitempty"` // Phase 4D: candidates skipped due to low relevance
	Keywords         []string `json:"keywords,omitempty"`
	MatchedPaths     []string `json:"matched_paths,omitempty"`    // Fields selected by keyword match
	BudgetAllocated  int      `json:"budget_allocated,omitempty"` // Per-result budget from ProcessMultipleForBudget
	Degenerate       bool     `json:"degenerate,omitempty"`       // kept too little to be representative of the source
	KeptRatio        float64  `json:"kept_ratio,omitempty"`       // trimmed_bytes / original_bytes

	// Phase 16 — coverage accounting. Byte-based values are APPROXIMATE: the trimmer parses and
	// re-serializes JSON (key order, whitespace, annotations all shift byte counts), so a byte
	// ratio can never be exact. Pair with the exact unit counts above (FieldsKept/FieldsDropped).
	// "Final output bytes" is the existing TrimmedBytes field.
	SourceCoverageRatio float64 `json:"source_coverage_ratio,omitempty"` // ~ source represented / original (approximate)
	LLMInputBytes       int     `json:"llm_input_bytes,omitempty"`       // total data bytes sent across LLM calls; can exceed OriginalBytes once map-reduce wrappers replicate per chunk
	SegmentsAnalyzed    int     `json:"segments_analyzed,omitempty"`     // map-reduce N (single-call: 1)
	SegmentsTotal       int     `json:"segments_total,omitempty"`        // map-reduce M (single-call: 1)
	PartialCoverage     bool    `json:"partial_coverage,omitempty"`      // a deterministic pre-LLM drop occurred
	CombineTruncated    bool    `json:"combine_truncated,omitempty"`     // reduce output was deterministically truncated

	// ContentLost is the AUTHORITATIVE loss signal, set by every lossy trim operation
	// (field/item/sentence drops, threshold skips, value truncation, byte cuts). Byte ratios
	// and unit counts cannot prove loss in either direction — re-serialization shrinks bytes
	// without losing content, and a backfilled field can be value-truncated with zero fields
	// dropped — so disclosure gating keys on this flag, never on those proxies.
	// Deliberately NOT omitempty: an explicit false means "verified lossless", distinct from
	// the key being absent on legacy records — consumers (the registry viewer) render the
	// tri-state, which an omitted false would collapse into "unknown".
	ContentLost bool `json:"content_lost"`
}

// degenerateKeptRatio is the threshold below which a structural trim is treated
// as non-representative: so little of the source survived that the synthesizing
// LLM must not infer that anything NOT shown is absent. Distillation output is
// exempt — the LLM there selects content deliberately, so a low ratio is success,
// not loss (see result_distiller.go).
const degenerateKeptRatio = 0.05

// degenerateTrim reports whether a trim kept so little of the original that the
// result is non-representative, along with the kept ratio (trimmed/original).
// originalBytes <= 0 disables the check (returns false, 1) — used by callers
// that do not have a meaningful original size in scope.
func degenerateTrim(originalBytes, trimmedBytes int) (bool, float64) {
	if originalBytes <= 0 {
		return false, 1
	}
	r := float64(trimmedBytes) / float64(originalBytes)
	return r < degenerateKeptRatio, r
}

// degenerateNote returns the honest "severely reduced … UNKNOWN" disclosure when a
// deterministic-floor trim kept a non-representative fraction of the source, else "".
// Shared by the plain-text / array / truncate floors so a degenerate result can never
// reach synthesis behind a coverage-implying note. (LLM distillation is intentionally
// exempt — a low ratio there is successful compression, not lost content.)
func degenerateNote(originalBytes, trimmedBytes int) string {
	degenerate, ratio := degenerateTrim(originalBytes, trimmedBytes)
	if !degenerate {
		return ""
	}
	return fmt.Sprintf(
		"\n[severely reduced: kept %d of ~%d bytes (%.2f%%); most content omitted — "+
			"treat anything NOT shown as UNKNOWN, do not infer it is absent]",
		trimmedBytes, originalBytes, ratio*100)
}

// cutToBytes returns s shortened to at most n bytes without splitting a multi-byte
// UTF-8 rune (and without adding any annotation of its own).
func cutToBytes(s string, n int) string {
	if n <= 0 {
		return ""
	}
	if len(s) <= n {
		return s
	}
	cut := n
	for cut > 0 && s[cut] >= 0x80 && s[cut] < 0xC0 {
		cut--
	}
	return s[:cut]
}

// floorWithDisclosure appends the honest degenerate-trim disclosure to a deterministic
// floor body while keeping the whole string within maxBytes. The disclosure is
// load-bearing — it tells the synthesizer to treat omitted content as UNKNOWN rather
// than infer absence — so when the body already fills the budget the body is shortened
// to make room for the note, instead of the note being dropped (which would let a
// degenerate floor reach synthesis behind a coverage-implying silence).
//
// TWO no-op cases return the body unchanged: contentLost == false (a byte-degenerate ratio
// alone must never fire a loss claim — pure re-serialization shrink can dip below the cliff
// with nothing omitted; degeneracy means severe LOSS, so it requires the caller's explicit
// loss signal), and a non-degenerate body.
//
// reshrink (when non-nil) re-derives a smaller body that stays STRUCTURALLY VALID for the
// requested byte room — e.g. re-trimming a JSON array to fewer whole items — so re-parsing
// consumers (the agent-input guard) are never handed corrupted JSON. When nil, a UTF-8-safe
// byte cut is used, which suffices for plain-text / already-truncated floors consumed as text.
func floorWithDisclosure(body string, originalBytes, maxBytes int, contentLost bool, reshrink func(room int) string) string {
	// A severe-loss note may only fire when content was actually LOST: on a pure
	// re-serialization shrink (pretty→compact, escape unwrapping) the byte ratio alone can
	// dip below the cliff with nothing omitted, and the make-room path below must never get
	// the chance to cut real content to fit a note about a loss that didn't happen.
	if !contentLost {
		return body
	}
	// The body may already end with the structural trimmer's own annotation. Degeneracy and
	// the note's kept-bytes figure are computed against the bare content, and on the
	// degenerate path the severe note REPLACES the trimmer's note — one authoritative
	// annotation whose counts cannot contradict a sibling note.
	base := stripResultAnnotation(body)
	note := degenerateNote(originalBytes, len(base))
	if note == "" {
		return body
	}
	if len(base)+len(note) <= maxBytes {
		return base + note
	}
	// Make room for the note, then recompute it against the shortened body so the reported
	// byte count/ratio stay accurate; the recomputed note is never longer (fewer kept bytes →
	// fewer digits, smaller ratio), so the result stays within maxBytes. If the budget is too
	// small for even the note (pathological), the disclosure still wins — losing the UNKNOWN
	// signal is worse than a tiny overshoot.
	room := maxBytes - len(note)
	if room < 0 {
		room = 0
	}
	if reshrink != nil {
		body = reshrink(room)
	} else {
		body = cutToBytes(base, room)
	}
	return body + degenerateNote(originalBytes, len(body))
}

// annotationPrefixes is the single registry of every trailing annotation/disclosure form a
// ResultProcessor may append to a result. stripResultAnnotation (agent_input_processor.go)
// iterates THIS slice, so emitters and the stripper can never drift apart — a new disclosure
// form not listed here would defeat the agent-input guard's re-parse and ship the param
// untrimmed. SHAPE CONTRACT: every form is a SINGLE line beginning with a registered prefix
// and ending with "]" — the exact shape stripResultAnnotation peels; a multi-line note would
// survive the peel and break the re-parse (the registry test asserts this per emitter).
var annotationPrefixes = []string{
	"\n[trimmed:",
	"\n[severely reduced:",
	"\n" + partialDisclosureMarker, // the map-reduce partial note's const (result_mapreduce.go) — never a second literal
	"\n[partial source:",
	"\n[reduced without model analysis:",
	"\n[findings truncated:",
}

// sanitizeAnnotationText makes free text (e.g. JSON key names) safe to embed in a
// single-line annotation: a raw newline would break the registry's shape contract — the
// stripper peels only single-line notes, so a multi-line note defeats the re-parse and the
// agent-input guard fails open with the untrimmed param — and a mid-rune cut would embed
// invalid UTF-8 in the prompt.
func sanitizeAnnotationText(s string) string {
	s = strings.ReplaceAll(s, "\n", `\n`)
	s = strings.ReplaceAll(s, "\r", `\r`)
	return cutToBytes(s, 200)
}

// coveragePct renders an approximate coverage ratio as a display percentage, clamped so a
// partial source never displays as fully covered (or negative). Shared by the appended output
// disclosure and the distill prompt's secondary signal so the two can never disagree.
func coveragePct(coverage float64) int {
	pct := int(coverage*100 + 0.5)
	if pct >= 100 {
		pct = 99
	}
	if pct < 0 {
		pct = 0
	}
	return pct
}

// partialSourceDisclosure discloses that the distillation model saw only a fraction of the
// source — a deterministic pre-LLM structural drop. coverage is the approximate kept ratio.
// Only called when coverage < 1, so it never reports "~100%". (Phase 16)
func partialSourceDisclosure(coverage float64) string {
	return fmt.Sprintf("\n[partial source: the model received ~%d%% of the source by bytes; "+
		"omitted content was not analyzed and is UNKNOWN]", coveragePct(coverage))
}

// combineTruncationDisclosure discloses that all source segments may have been analyzed but
// some extracted findings were dropped to fit the budget (map-reduce reduce truncation). This
// is a different loss than a partial source, so it gets its own wording. (Phase 16)
func combineTruncationDisclosure() string {
	return "\n[findings truncated: not all extracted findings are shown; treat unlisted findings as UNKNOWN]"
}

// truncationDisclosure discloses a deterministic byte-truncation at a presentation seam where NO
// model analyzed the dropped tail — the synthesis byte-truncation fallback that fires when no result
// processor is configured. (Phase 16)
func truncationDisclosure() string {
	return "\n[reduced without model analysis: output truncated to fit the budget; omitted content is UNKNOWN — do not infer it is absent]"
}

// truncateBytesWithUnknown is the honest form of truncateResultBytes for presentation seams
// where a deterministic byte cut is the FINAL word on the result (no model will analyze the
// dropped tail and no richer disclosure follows): the note carries the UNKNOWN safeguard so an
// above-cliff cut can never reach synthesis behind a coverage-implying neutral note.
// truncateResultBytes itself stays neutral — several of its call sites feed content a model
// DID or WILL analyze.
func truncateBytesWithUnknown(s string, maxBytes int) string {
	if len(s) <= maxBytes {
		return s
	}
	const noteFmt = "\n[trimmed: %d → %d bytes; omitted content is UNKNOWN — do not infer it is absent]"
	note := fmt.Sprintf(noteFmt, len(s), maxBytes)
	body := cutToBytes(s, maxBytes-len(note))
	// Re-format with the ACTUAL kept length — an honest note must not overstate the body
	// (the first format reserved space; digits can only shrink, so body+note stays ≤ maxBytes).
	return body + fmt.Sprintf(noteFmt, len(s), len(body))
}

// partialSegmentsDisclosure discloses that only completed of total map-reduce segments were
// analyzed within the time budget. A named emitter (not an inline Sprintf at the call site) so
// the registry round-trip test renders the exact production form.
func partialSegmentsDisclosure(completed, total int) string {
	return fmt.Sprintf(
		"\n%s %d of %d segments analyzed within the time budget; treat the rest as UNKNOWN, not absent]",
		partialDisclosureMarker, completed, total)
}

// appendDisclosure appends note to out, keeping the pair within maxBytes by shortening OUT
// (UTF-8-safe, via cutToBytes) — never the note. The disclosure is load-bearing, so if the
// budget cannot fit even the note, the note still wins (a small overshoot beats losing the
// UNKNOWN signal). note is expected to be one of the annotationPrefixes forms. (Phase 16)
func appendDisclosure(out, note string, maxBytes int) string {
	if len(out)+len(note) <= maxBytes {
		return out + note
	}
	return cutToBytes(out, maxBytes-len(note)) + note
}

// trimMetadataKey is the context key for passing trim metadata out of ProcessForPrompt.
type trimMetadataKey struct{}

// WithTrimMetadataCapture returns a derived context and a pointer to a ResultTrimMetadata
// that will be populated by captureTrimMetadata when ProcessForPrompt runs.
// The pointer is safe to read after ProcessForPrompt returns.
func WithTrimMetadataCapture(ctx context.Context) (context.Context, *ResultTrimMetadata) {
	meta := &ResultTrimMetadata{}
	return context.WithValue(ctx, trimMetadataKey{}, meta), meta
}

// captureTrimMetadata writes metadata into the context slot created by WithTrimMetadataCapture.
// No-op if the context was not prepared with WithTrimMetadataCapture.
func captureTrimMetadata(ctx context.Context, meta ResultTrimMetadata) {
	// Enforce the ContentLost superset invariant structurally: every specific loss flag
	// implies the authoritative bit. Without this, a forgetful emitter (in-tree or a custom
	// ResultProcessor) could record "partial coverage" beside an explicit "verified lossless"
	// — the contradiction the viewer's chip logic and disclosure gating assume impossible.
	// Idempotent, so cache-envelope replay through this same choke point is unaffected.
	meta.ContentLost = meta.ContentLost || meta.Degenerate || meta.PartialCoverage || meta.CombineTruncated
	if ptr, ok := ctx.Value(trimMetadataKey{}).(*ResultTrimMetadata); ok {
		*ptr = meta
	}
}

// lossyTrimEvent reports whether a trim record warrants the result_trim telemetry: the
// authoritative ContentLost signal, or any byte change (a lossless re-serialization is still
// size accounting worth recording). The Go twin of the registry viewer's isLossyTrim — keep
// the two in step.
func lossyTrimEvent(meta *ResultTrimMetadata, originalSize, trimmedSize int) bool {
	return meta != nil && (meta.ContentLost || trimmedSize != originalSize)
}

// lossyByteCoverage returns kept/total for a KNOWN-lossy trim, clamped so it can never read
// as full coverage: escape/annotation inflation can push the raw ratio to >= 1 while content
// was dropped, and 0.99 is the "lossy but nearly full" display floor. Shared by the distiller
// and the map-reduce chunker so the sentinel and boundary cannot drift between them.
func lossyByteCoverage(kept, total int) float64 {
	if total <= 0 {
		return 0.99
	}
	r := float64(kept) / float64(total)
	if r >= 1 {
		return 0.99
	}
	if r < 0 {
		return 0
	}
	return r
}

// deserializeStringValues recursively finds string values containing valid JSON
// and deserializes them to prevent double-escaping on re-serialization.
// Fixes orch-1771044870070918471 (181K backslashes, 24% of body).
func deserializeStringValues(data interface{}) interface{} {
	switch v := data.(type) {
	case map[string]interface{}:
		for k, val := range v {
			v[k] = deserializeStringValues(val)
		}
		return v
	case []interface{}:
		for i, val := range v {
			v[i] = deserializeStringValues(val)
		}
		return v
	case string:
		if len(v) > 2 && (v[0] == '{' || v[0] == '[') {
			var parsed interface{}
			if json.Unmarshal([]byte(v), &parsed) == nil {
				return parsed
			}
		}
		return v
	default:
		return v
	}
}

// DeserializeStringValues is the exported version for edge-case use by tool handlers.
func DeserializeStringValues(data interface{}) interface{} {
	return deserializeStringValues(data)
}

// truncateResultBytes is the Phase 1 fallback — replaced by StructuralTrimmer in Phase 2.
func truncateResultBytes(response string, maxBytes int) string {
	if len(response) <= maxBytes {
		return response
	}
	if maxBytes <= 0 {
		return ""
	}
	annotation := fmt.Sprintf("\n[trimmed: %d → %d bytes]", len(response), maxBytes)
	if len(annotation) >= maxBytes {
		// Budget too small for annotation — bare truncation
		return response[:maxBytes]
	}
	cut := maxBytes - len(annotation)
	// Avoid splitting multi-byte UTF-8
	for cut > 0 && response[cut] >= 0x80 && response[cut] < 0xC0 {
		cut--
	}
	return response[:cut] + annotation
}

// --- Keyword Extraction (Phase 2) ---

var stopWords = map[string]bool{
	"the": true, "a": true, "an": true, "is": true, "are": true, "was": true,
	"were": true, "be": true, "been": true, "have": true, "has": true, "had": true,
	"do": true, "does": true, "did": true, "will": true, "would": true, "could": true,
	"should": true, "may": true, "might": true, "can": true, "to": true, "of": true,
	"in": true, "for": true, "on": true, "with": true, "at": true, "by": true,
	"from": true, "as": true, "and": true, "but": true, "or": true, "not": true,
	"this": true, "that": true, "these": true, "those": true, "it": true, "its": true,
	"i": true, "me": true, "my": true, "we": true, "our": true, "you": true, "your": true,
	"he": true, "him": true, "his": true, "she": true, "her": true, "they": true,
	"them": true, "their": true, "what": true, "which": true, "who": true, "how": true,
	"when": true, "where": true, "why": true, "get": true, "give": true, "make": true,
	"use": true, "find": true, "tell": true, "show": true, "provide": true, "about": true,
}

// extractKeywords tokenizes an instruction into lowercase keywords with basic stemming.
// Research: SWE-Pruner (2025) showed keyword matching ≈ learned models for relevance.
func extractKeywords(instruction string) []string {
	words := strings.FieldsFunc(strings.ToLower(instruction), func(r rune) bool {
		return (r < 'a' || r > 'z') && (r < '0' || r > '9') && r != '_'
	})
	seen := make(map[string]bool, len(words))
	keywords := make([]string, 0, len(words))
	for _, word := range words {
		if len(word) < 3 || stopWords[word] {
			continue
		}
		stemmed := basicStem(word)
		if !seen[stemmed] {
			seen[stemmed] = true
			keywords = append(keywords, stemmed)
		}
		if stemmed != word && !seen[word] {
			seen[word] = true
			keywords = append(keywords, word)
		}
	}
	return keywords
}

// basicStem applies simple suffix-stripping. Not a full Porter stemmer —
// just enough for keyword matching against JSON field names.
func basicStem(word string) string {
	suffixes := []struct{ suffix, replacement string }{
		{"ation", ""}, {"ness", ""}, {"ment", ""}, {"ings", ""},
		{"ting", ""}, {"ing", ""}, {"ies", "y"}, {"ous", ""},
		{"ive", ""}, {"ful", ""}, {"ity", ""}, {"ion", ""},
		{"ly", ""}, {"ed", ""}, {"er", ""}, {"es", ""}, {"al", ""}, {"s", ""},
	}
	for _, s := range suffixes {
		if strings.HasSuffix(word, s.suffix) && len(word)-len(s.suffix)+len(s.replacement) >= 3 {
			return word[:len(word)-len(s.suffix)] + s.replacement
		}
	}
	return word
}

// --- Budget Allocation (Phase 3) ---

// BudgetAllocator distributes a total byte budget across multiple results.
type BudgetAllocator struct {
	totalBudget  int
	perResultMax int
}

// NewBudgetAllocator creates a budget allocator with total and per-result limits.
func NewBudgetAllocator(totalBudget, perResultMax int) *BudgetAllocator {
	return &BudgetAllocator{totalBudget: totalBudget, perResultMax: perResultMax}
}

// Allocate distributes budget proportionally. Small results get full size;
// oversized results share the remainder proportionally, clamped to available budget.
// Savings from perResultMax clamping are redistributed equally among eligible results.
func (ba *BudgetAllocator) Allocate(sizes []int) []int {
	budgets := make([]int, len(sizes))
	remaining := ba.totalBudget
	var oversized []int

	for i, size := range sizes {
		if size <= ba.perResultMax {
			budgets[i] = size
			remaining -= size
		} else {
			oversized = append(oversized, i)
		}
	}

	// Phase 9: when the small class alone over-subscribes the total (common once distill-eligible
	// results are sized at ~TargetSize via EffectiveSizer), scale every small budget down
	// proportionally to fit — degrade fidelity uniformly rather than letting earlier results keep
	// full size while later ones (and all oversized ones) starve to 0. The distiller floors its own
	// targetSize (minDistillTargetSize), so a scaled-down budget is never emitted empty.
	if remaining < 0 {
		smallTotal := ba.totalBudget - remaining // = Σ small sizes (> totalBudget)
		if smallTotal > 0 && ba.totalBudget > 0 {
			for i, size := range sizes {
				if size <= ba.perResultMax {
					budgets[i] = int(float64(size) / float64(smallTotal) * float64(ba.totalBudget))
				}
			}
		}
		return budgets // total exhausted; any oversized results correctly get 0
	}

	if len(oversized) > 0 && remaining > 0 {
		totalOversized := 0
		for _, idx := range oversized {
			totalOversized += sizes[idx]
		}
		budgetUsed := 0
		for _, idx := range oversized {
			budget := int(float64(sizes[idx]) / float64(totalOversized) * float64(remaining))
			if budget < 512 && remaining-budgetUsed >= 512 {
				budget = 512
			} else if budget < 512 {
				budget = remaining - budgetUsed
			}
			if budget > ba.perResultMax {
				budget = ba.perResultMax
			}
			if budgetUsed+budget > remaining {
				budget = remaining - budgetUsed
			}
			budgets[idx] = budget
			budgetUsed += budget
		}

		// Phase 3: redistribute unused budget freed by perResultMax clamping.
		// Repeat until budget exhausted or all oversized results are at cap.
		for {
			leftover := remaining - budgetUsed
			if leftover <= 0 {
				break
			}

			// Find oversized results still below their cap
			var eligible []int
			for _, idx := range oversized {
				if budgets[idx] < ba.perResultMax {
					eligible = append(eligible, idx)
				}
			}
			if len(eligible) == 0 {
				break
			}

			// Distribute leftover equally among eligible results
			distributed := 0
			for _, idx := range eligible {
				share := leftover / len(eligible)
				if share <= 0 {
					share = 1
				}
				room := ba.perResultMax - budgets[idx]
				if share > room {
					share = room
				}
				if distributed+share > leftover {
					share = leftover - distributed
				}
				budgets[idx] += share
				distributed += share
			}
			budgetUsed += distributed

			if distributed == 0 {
				break
			}
		}
	}
	return budgets
}

// ProcessMultipleForBudget applies a ResultProcessor with proportional budget allocation.
// Returns processed strings and per-step trim metadata (keyed by StepID).
func ProcessMultipleForBudget(
	ctx context.Context, processor ResultProcessor, steps []StepResult,
	totalBudget, perResultMax int, originalQuery string,
) ([]string, map[string]*ResultTrimMetadata) {
	// Phase 9: allocate against the POST-process footprint. A result the processor will distill
	// occupies ~TargetSize in the prompt, not its raw size. Processors without EffectiveSizer (the
	// bare StructuralTrimmer) report raw size, so this is identical to pre-Phase-9 behaviour.
	sizer, _ := processor.(EffectiveSizer)
	sizes := make([]int, len(steps))
	for i, s := range steps {
		sizes[i] = len(s.Response)
		if sizer != nil {
			sizes[i] = sizer.EffectiveSize(sizes[i])
		}
	}
	budgets := NewBudgetAllocator(totalBudget, perResultMax).Allocate(sizes)

	results := make([]string, len(steps))
	metaMap := make(map[string]*ResultTrimMetadata, len(steps))
	for i, step := range steps {
		trimCtx, meta := WithTrimMetadataCapture(ctx)
		results[i] = processor.ProcessForPrompt(trimCtx, step.Response, budgets[i], ResultProcessorContext{
			StepID: step.StepID, AgentName: step.AgentName, Instruction: step.Instruction,
			OriginalQuery: originalQuery,
		})
		meta.BudgetAllocated = budgets[i]
		metaMap[step.StepID] = meta
	}
	return results, metaMap
}

// --- Phase 14: continuation decision digest -------------------------------------------------------
//
// The continuation planner needs the STRUCTURE of each completed step (so it can write
// {{step-X.field}} templates) far more than the raw values (which resolve from full memory at
// execution). buildDecisionDigest renders a structure-complete skeleton: every object key/path is
// kept (objects beyond a per-object cap are key-sampled), arrays are trimmed to a head sample plus a
// length sentinel (staying arrays), and long string values are elided. The output is always valid JSON.
// This is distinct from StructuralTrimmer, which
// relevance-ranks and DROPS whole fields to fit a byte budget.

const (
	// defaultDigestSampleN is the per-array head-sample size in a decision digest.
	defaultDigestSampleN = 3
	// defaultDigestScalarMax is the max length of a string value kept inline before it is elided.
	defaultDigestScalarMax = 200
	// defaultDigestMaxKeys caps the keys kept per object. Schema objects (a handful of fields) are kept
	// whole; map-shaped objects keyed by many dynamic IDs (metrics-by-instance, services-by-name) are
	// sampled to this many sorted keys plus a sentinel, so one wide object can't dominate the digest.
	defaultDigestMaxKeys = 50
)

// buildDecisionDigest renders the structure-complete skeleton described above. degenerate is true only
// for a non-JSON blob (no structure to digest): the caller substitutes the structural floor for the
// body and C escalates to distill it. Valid JSON is never degenerate — the skeleton keeps the structure
// (every key, up to the per-object cap), so the planner can address values via {{step-X.field}} (they
// resolve from full memory at execution); a narrative-heavy value merely being elided does NOT warrant a
// fast-model C call (measured: that over-triggered on 8/30 steps of a real run whose data flowed to
// synthesis, not plan templates).
// elided reports whether ANY sampling/eliding cut fired while rendering — the explicit loss
// signal ContentLost keys on. A byte-length comparison cannot stand in for it: omission
// sentinels ("…N more of M") can make a lossy digest LONGER than a small original.
func buildDecisionDigest(response string, sampleN, scalarMax, maxKeys int) (digest string, degenerate, elided bool) {
	// Decode with UseNumber so large integers (snowflake IDs, nanosecond timestamps, request IDs beyond
	// float64's 2^53 exact range) survive verbatim instead of being mangled into scientific notation.
	dec := json.NewDecoder(strings.NewReader(response))
	dec.UseNumber()
	var v interface{}
	if err := dec.Decode(&v); err != nil {
		return "", true, false // non-JSON / incomplete → caller substitutes the structural floor; C escalates.
	}
	if dec.More() {
		return "", true, false // trailing content after the JSON value → treat as a blob (not clean JSON).
	}
	var el bool
	out, err := json.Marshal(digestValue(v, sampleN, scalarMax, maxKeys, &el))
	if err != nil {
		return "", true, false // unreachable in practice (digestValue yields only marshalable types); errcheck guard.
	}
	return string(out), false, el
}

// digestValue recurses producing the skeleton. Objects keep all keys up to maxKeys, then sample (sorted)
// + a sentinel; arrays stay arrays (head sample + length sentinel) so the planner's {{step-X.field[i]}}
// model holds; long strings elide to a sentinel.
func digestValue(v interface{}, sampleN, scalarMax, maxKeys int, elided *bool) interface{} {
	switch t := v.(type) {
	case map[string]interface{}:
		if len(t) > maxKeys {
			// Map-shaped object (many dynamic-ID keys): keep maxKeys sorted keys + a sentinel, mirroring
			// array sampling, so one wide object can't dominate the digest or crowd out other steps.
			*elided = true
			keys := make([]string, 0, len(t))
			for k := range t {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			m := make(map[string]interface{}, maxKeys+1)
			for _, k := range keys[:maxKeys] {
				m[k] = digestValue(t[k], sampleN, scalarMax, maxKeys, elided)
			}
			m["__truncated_keys__"] = fmt.Sprintf("%d more of %d keys", len(t)-maxKeys, len(t))
			return m
		}
		m := make(map[string]interface{}, len(t))
		for k, child := range t {
			m[k] = digestValue(child, sampleN, scalarMax, maxKeys, elided)
		}
		return m
	case []interface{}:
		n := sampleN
		if n > len(t) {
			n = len(t)
		}
		sample := make([]interface{}, 0, n+1)
		for i := 0; i < n; i++ {
			sample = append(sample, digestValue(t[i], sampleN, scalarMax, maxKeys, elided))
		}
		if len(t) > n {
			*elided = true
			sample = append(sample, fmt.Sprintf("…%d more of %d", len(t)-n, len(t)))
		}
		return sample
	case string:
		if len(t) > scalarMax {
			// Parens, not <…>: json.Marshal HTML-escapes < and > to </>, which would make the
			// planner-facing sentinel noisy.
			*elided = true
			return fmt.Sprintf("…(%d chars)", len(t))
		}
		return t
	default:
		return t // numbers, bools, null — salient scalars kept as-is
	}
}

// isStructurallyDegenerate reports whether a digested step should escalate to the continuation
// distiller (C). C fires for a non-JSON blob only — the digest path records that as Method "truncate"
// (the structural-floor body). Valid-JSON steps keep their structure, so they never escalate.
func isStructurallyDegenerate(meta *ResultTrimMetadata) bool {
	return meta != nil && meta.Method == "truncate"
}

// continuationDigestOpts bundles the env-tunable knobs for rendering continuation digests (Phase 14).
// The builder resolves these from OrchestratorConfig (falling back to the digest defaults) and passes
// concrete values, keeping these functions pure and unit-testable.
type continuationDigestOpts struct {
	floorChars int // non-JSON floor-preview cap (ContinuationResultMaxChars)
	sampleN    int // array head-sample size (ContinuationDigestArraySample)
	scalarMax  int // string-elision threshold (ContinuationDigestScalarMax)
	maxKeys    int // per-object key cap (ContinuationDigestMaxKeys)
}

// renderContinuationDigests builds a decision digest for each completed step (Phase 14), index-aligned
// with steps. A valid-JSON step gets its structure-complete skeleton (Method "digest"). A non-JSON blob
// has no structure to digest, so it gets a structural-floor preview as the body (fail-open, never the
// empty string) and is marked Method "truncate" so isStructurallyDegenerate routes it to C.
func renderContinuationDigests(steps []StepResult, opts continuationDigestOpts) (bodies []string, meta []*ResultTrimMetadata) {
	bodies = make([]string, len(steps))
	meta = make([]*ResultTrimMetadata, len(steps))
	for i := range steps {
		resp := steps[i].Response
		digest, degenerate, elided := buildDecisionDigest(resp, opts.sampleN, opts.scalarMax, opts.maxKeys)
		method := "digest"
		lost := elided // the digester's explicit signal — never a byte-length proxy
		if degenerate {
			digest = truncateRunes(resp, opts.floorChars) // non-JSON floor preview (fail-open body)
			method = "truncate"
			// truncateRunes returns its input UNCHANGED when it fits and otherwise cuts and
			// appends an ellipsis — so inequality is the exact cut condition. A length
			// comparison would be wrong here: the ellipsis can make a cut preview as long as
			// (or longer than) its source at the boundary.
			lost = digest != resp
		}
		bodies[i] = digest
		meta[i] = &ResultTrimMetadata{
			Method: method, OriginalBytes: len(resp), TrimmedBytes: len(digest),
			ContentLost: lost,
		}
	}
	return bodies, meta
}
