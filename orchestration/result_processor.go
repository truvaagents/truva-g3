package orchestration

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// ResultProcessor transforms step results before they are embedded in prompts.
// Default implementation: StructuralTrimmer (see structural_trimmer.go).
// Developers implement this for domain-specific trimming.
type ResultProcessor interface {
	ProcessForPrompt(ctx context.Context, result string, maxBytes int, stepContext ResultProcessorContext) string
}

// ResultProcessorContext provides step metadata for context-aware trimming.
type ResultProcessorContext struct {
	StepID      string // "step-3"
	AgentName   string // Agent that produced this result
	Capability  string // Optional: not populated in default synthesis path (StepResult has no Capability field). Set by custom callers or when integrating with capability-aware executors.
	Instruction string // Step instruction — primary signal for query-conditioned trimming
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
	Enabled          bool `json:"enabled"`           // Default: false
	DistillThreshold int  `json:"distill_threshold"` // Min bytes to trigger distillation. Default: 32768
	PreFilterBudget  int  `json:"prefilter_budget"`  // StructuralTrimmer budget before LLM. Default: 32768
	TargetSize       int  `json:"target_size"`       // LLM output target size. Default: 4096
	// Model overrides the LLM model for distillation calls.
	// Empty string = use the AIClient's default model.
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
	// (result content + instruction + budget) and is fail-open (a nil cache disables it
	// with no overhead). Reuses the shared-memory digest cache pattern so scheduled and
	// repetitive runs — the worst offenders for redundant LLM cost — become cache hits.
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
	if ptr, ok := ctx.Value(trimMetadataKey{}).(*ResultTrimMetadata); ok {
		*ptr = meta
	}
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

	// Clamp: small results may have consumed the entire budget
	if remaining < 0 {
		remaining = 0
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
	totalBudget, perResultMax int,
) ([]string, map[string]*ResultTrimMetadata) {
	sizes := make([]int, len(steps))
	for i, s := range steps {
		sizes[i] = len(s.Response)
	}
	budgets := NewBudgetAllocator(totalBudget, perResultMax).Allocate(sizes)

	results := make([]string, len(steps))
	metaMap := make(map[string]*ResultTrimMetadata, len(steps))
	for i, step := range steps {
		trimCtx, meta := WithTrimMetadataCapture(ctx)
		results[i] = processor.ProcessForPrompt(trimCtx, step.Response, budgets[i], ResultProcessorContext{
			StepID: step.StepID, AgentName: step.AgentName, Instruction: step.Instruction,
		})
		meta.BudgetAllocated = budgets[i]
		metaMap[step.StepID] = meta
	}
	return results, metaMap
}
