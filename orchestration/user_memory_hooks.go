package orchestration

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/truvaagents/truva-g3/core"
	"github.com/truvaagents/truva-g3/telemetry"
	"go.opentelemetry.io/otel/attribute"
)

// Compile-time interface compliance checks.
var _ core.BeforePlanningHook = (*UserMemoryEnrichmentHook)(nil)

// ─── Enrichment Hook (BeforePlanning) ────────────────────────────────────────

// UserMemoryEnrichmentHook injects <user_profile> context into the planning prompt.
// Implements core.BeforePlanningHook. Never short-circuits the pipeline.
//
// Reads user_id from PipelineContext.Metadata["user_id"]. If absent, skips silently
// (agents without user context — e.g., event-driven-agent — are unaffected).
type UserMemoryEnrichmentHook struct {
	userMemory        core.UserMemory
	namespace         string
	logger            core.Logger
	maxFacts          int              // from TRUVAG3_USER_MEMORY_MAX_FACTS_IN_PROMPT (default: 15)
	maxIdentityFacts  int              // from TRUVAG3_USER_MEMORY_MAX_IDENTITY_FACTS (default: 5)
	maxDurableFacts   int              // from TRUVAG3_USER_MEMORY_MAX_DURABLE_FACTS_IN_PROMPT (default: 8)
	maxTransientFacts int              // from TRUVAG3_USER_MEMORY_MAX_TRANSIENT_FACTS_IN_PROMPT (default: 4)
	maxSummaryFacts   int              // from TRUVAG3_USER_MEMORY_MAX_SUMMARY_FACTS_IN_PROMPT (default: 3)
	maxStableFacts    int              // from TRUVAG3_USER_MEMORY_MAX_STABLE_FACTS_PER_CATEGORY (default: 2)
	maxUniversalFacts int              // from TRUVAG3_USER_MEMORY_MAX_UNIVERSAL_FACTS (default: 5)
	minConfidence     float64          // from TRUVAG3_USER_MEMORY_MIN_CONFIDENCE (default: 0.3)
	normalizeUserID   UserIDNormalizer // canonicalizes user_id before recall (default: identity)
	debugStore        LLMDebugStore    // optional — set by factory duck-typing propagation
	debugWg           sync.WaitGroup   // tracks in-flight debug recordings
}

// UserMemoryEnrichmentOption configures UserMemoryEnrichmentHook (Layer 3).
type UserMemoryEnrichmentOption func(*UserMemoryEnrichmentHook)

// NewUserMemoryEnrichmentHook creates an enrichment hook. Layer 3 constructor.
func NewUserMemoryEnrichmentHook(
	userMem core.UserMemory,
	namespace string,
	logger core.Logger,
	opts ...UserMemoryEnrichmentOption,
) *UserMemoryEnrichmentHook {
	// Component-aware logger setup (per LOGGING_IMPLEMENTATION_GUIDE §Component-Aware)
	var wrappedLogger core.Logger = &core.NoOpLogger{}
	if logger != nil {
		if cal, ok := logger.(core.ComponentAwareLogger); ok {
			wrappedLogger = cal.WithComponent("framework/orchestration")
		} else {
			wrappedLogger = logger
		}
	}

	h := &UserMemoryEnrichmentHook{
		userMemory:        userMem,
		namespace:         namespace,
		logger:            wrappedLogger,
		maxFacts:          15,
		maxIdentityFacts:  5,
		maxDurableFacts:   8,
		maxTransientFacts: 4,
		maxSummaryFacts:   3,
		maxStableFacts:    2,
		maxUniversalFacts: 5,
		minConfidence:     0.3,
	}

	// Read env vars for numeric tuning (per FRAMEWORK_DESIGN_PRINCIPLES §Externalize Hardcoded Limits)
	if v, _ := strconv.Atoi(os.Getenv("TRUVAG3_USER_MEMORY_MAX_FACTS_IN_PROMPT")); v > 0 {
		h.maxFacts = v
	}
	if v, _ := strconv.Atoi(os.Getenv("TRUVAG3_USER_MEMORY_MAX_IDENTITY_FACTS")); v > 0 {
		h.maxIdentityFacts = v
	}
	if v := positiveIntFromEnv("TRUVAG3_USER_MEMORY_MAX_DURABLE_FACTS_IN_PROMPT"); v > 0 {
		h.maxDurableFacts = v
	}
	if v := positiveIntFromEnv("TRUVAG3_USER_MEMORY_MAX_TRANSIENT_FACTS_IN_PROMPT", "TRUVAG3_USER_MEMORY_MAX_CONTEXT_FACTS"); v > 0 {
		h.maxTransientFacts = v
	}
	if v := positiveIntFromEnv("TRUVAG3_USER_MEMORY_MAX_SUMMARY_FACTS_IN_PROMPT", "TRUVAG3_USER_MEMORY_MAX_SUMMARY_FACTS"); v > 0 {
		h.maxSummaryFacts = v
	}
	if v, _ := strconv.Atoi(os.Getenv("TRUVAG3_USER_MEMORY_MAX_STABLE_FACTS_PER_CATEGORY")); v > 0 {
		h.maxStableFacts = v
	}
	if v, _ := strconv.Atoi(os.Getenv("TRUVAG3_USER_MEMORY_MAX_UNIVERSAL_FACTS")); v > 0 {
		h.maxUniversalFacts = v
	}
	if v, _ := strconv.ParseFloat(os.Getenv("TRUVAG3_USER_MEMORY_MIN_CONFIDENCE"), 64); v > 0 {
		h.minConfidence = v
	}

	for _, opt := range opts {
		opt(h)
	}

	// Default to identity when no normalizer was supplied (no-op, case-sensitive).
	if h.normalizeUserID == nil {
		h.normalizeUserID = IdentityUserIDNormalizer
	}
	return h
}

// WithUserMemoryEnrichmentUserIDNormalizer sets the function that canonicalizes
// user_id before recall. Pair it with the matching extraction-hook option so
// reads and writes land in the same bucket. Nil is ignored (keeps the default).
func WithUserMemoryEnrichmentUserIDNormalizer(n UserIDNormalizer) UserMemoryEnrichmentOption {
	return func(h *UserMemoryEnrichmentHook) {
		if n != nil {
			h.normalizeUserID = n
		}
	}
}

func (h *UserMemoryEnrichmentHook) Name() string { return "user-memory-enrichment" }

func positiveIntFromEnv(keys ...string) int {
	for _, key := range keys {
		if v, _ := strconv.Atoi(os.Getenv(key)); v > 0 {
			return v
		}
	}
	return 0
}

// SetLLMDebugStore enables debug recording. Called by factory duck-typing propagation
// (factory.go:345-354) — no factory changes needed.
func (h *UserMemoryEnrichmentHook) SetLLMDebugStore(store LLMDebugStore) {
	h.debugStore = store
}

// recordDebugInteraction asynchronously records a debug interaction.
// Same pattern as LLMEventSummarizer.recordDebugInteraction — async, fail-open, WaitGroup-tracked.
func (h *UserMemoryEnrichmentHook) recordDebugInteraction(ctx context.Context, requestID string, interaction LLMInteraction) {
	if h.debugStore == nil {
		return
	}
	h.debugWg.Add(1)
	go func() {
		defer h.debugWg.Done()
		recordCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 1*time.Second)
		defer cancel()
		if err := h.debugStore.RecordInteraction(recordCtx, requestID, interaction); err != nil {
			if h.logger != nil {
				h.logger.Warn("Failed to record user memory debug interaction", map[string]interface{}{
					"operation":  "user_memory_debug_recording",
					"request_id": requestID,
					"type":       interaction.Type,
					"error":      err.Error(),
				})
			}
		}
	}()
}

// BeforePlanning reads user facts and injects <user_profile> into enrichments.
// Returns (nil, nil) to continue the pipeline — never short-circuits.
// The pipeline runner auto-creates the parent span:
//
//	pipeline.hook.before_planning.user-memory-enrichment
func (h *UserMemoryEnrichmentHook) BeforePlanning(ctx context.Context, pctx *core.PipelineContext) (*core.PipelineShortCircuit, error) {
	userID, ok := pctx.Metadata["user_id"].(string)
	if !ok || userID == "" {
		return nil, nil // No user context — skip silently
	}
	// Canonicalize before any recall so reads match the form used at storage.
	// normalizeUserID is constructor-guaranteed non-nil (defaults to identity).
	userID = h.normalizeUserID(userID)
	if userID == "" {
		return nil, nil // Normalized away (e.g. whitespace-only) — nothing to recall
	}

	requestID := GetRequestID(ctx)
	startTime := time.Now()

	// 1. Recall identity facts (always included regardless of query relevance)
	identityStart := time.Now()
	identityFacts := func() []core.UserFact {
		spanCtx, end := telemetry.StartChildSpan(ctx, "user_memory.recall.identity",
			attribute.String("user_id", userID),
			attribute.String("namespace", "universal"),
			attribute.String("category", "identity"),
		)
		defer end()
		facts, _ := h.userMemory.RecallByCategory(spanCtx, userID, "universal", "identity", h.maxIdentityFacts)
		telemetry.SetSpanAttributes(spanCtx,
			attribute.Int("facts_recalled", len(facts)),
			attribute.Int64("duration_ms", time.Since(identityStart).Milliseconds()),
		)
		return facts
	}()
	h.recordDebugInteraction(ctx, requestID, LLMInteraction{
		Type:       "user_memory_recall_identity",
		HookPhase:  HookPhasePre,
		Category:   "vector_db",
		Timestamp:  identityStart,
		DurationMs: time.Since(identityStart).Milliseconds(),
		Response:   fmt.Sprintf("%d facts recalled", len(identityFacts)),
		Success:    true,
	})

	// 2. Recall recent session summaries (always included for cross-session continuity)
	summaryStart := time.Now()
	summaryFacts := func() []core.UserFact {
		spanCtx, end := telemetry.StartChildSpan(ctx, "user_memory.recall.summary",
			attribute.String("user_id", userID),
			attribute.String("namespace", h.namespace),
			attribute.String("category", "summary"),
		)
		defer end()
		facts, _ := h.userMemory.RecallByCategory(spanCtx, userID, h.namespace, "summary", h.maxSummaryFacts)
		telemetry.SetSpanAttributes(spanCtx,
			attribute.Int("facts_recalled", len(facts)),
			attribute.Int64("duration_ms", time.Since(summaryStart).Milliseconds()),
		)
		return facts
	}()
	h.recordDebugInteraction(ctx, requestID, LLMInteraction{
		Type:       "user_memory_recall_summary",
		HookPhase:  HookPhasePre,
		Category:   "vector_db",
		Timestamp:  summaryStart,
		DurationMs: time.Since(summaryStart).Milliseconds(),
		Response:   fmt.Sprintf("%d facts recalled", len(summaryFacts)),
		Success:    true,
	})

	// 3. Recall stable namespace facts (preferences, constraints, relationships)
	stableStart := time.Now()
	stableFacts := func() []core.UserFact {
		spanCtx, end := telemetry.StartChildSpan(ctx, "user_memory.recall.stable_namespace",
			attribute.String("user_id", userID),
			attribute.String("namespace", h.namespace),
		)
		defer end()
		facts := h.recallStableNamespaceFacts(spanCtx, userID)
		telemetry.SetSpanAttributes(spanCtx,
			attribute.Int("facts_recalled", len(facts)),
			attribute.Int64("duration_ms", time.Since(stableStart).Milliseconds()),
		)
		return facts
	}()
	h.recordDebugInteraction(ctx, requestID, LLMInteraction{
		Type:       "user_memory_recall_stable_namespace",
		HookPhase:  HookPhasePre,
		Category:   "vector_db",
		Timestamp:  stableStart,
		DurationMs: time.Since(stableStart).Milliseconds(),
		Response:   fmt.Sprintf("%d stable facts recalled from namespace %s", len(stableFacts), h.namespace),
		Success:    true,
	})

	// 4. Recall namespace-specific facts relevant to the request
	queryStart := time.Now()
	queryFacts := func() []core.UserFact {
		spanCtx, end := telemetry.StartChildSpan(ctx, "user_memory.recall.query",
			attribute.String("user_id", userID),
			attribute.String("namespace", h.namespace),
			attribute.Int("query_chars", len(pctx.Request)),
		)
		defer end()
		facts, _ := h.userMemory.Recall(spanCtx, userID, h.namespace, pctx.Request, h.maxFacts)
		telemetry.SetSpanAttributes(spanCtx,
			attribute.Int("facts_recalled", len(facts)),
			attribute.Int64("duration_ms", time.Since(queryStart).Milliseconds()),
		)
		return facts
	}()
	h.recordDebugInteraction(ctx, requestID, LLMInteraction{
		Type:       "user_memory_recall_query",
		HookPhase:  HookPhasePre,
		Category:   "vector_db",
		Timestamp:  queryStart,
		DurationMs: time.Since(queryStart).Milliseconds(),
		Prompt:     truncateUTF8(pctx.Request, 200),
		Response:   fmt.Sprintf("%d facts recalled from namespace %s", len(queryFacts), h.namespace),
		Success:    true,
	})

	// 5. Recall universal facts (preferences, constraints — not identity, already fetched)
	universalStart := time.Now()
	universalFacts := func() []core.UserFact {
		spanCtx, end := telemetry.StartChildSpan(ctx, "user_memory.recall.universal",
			attribute.String("user_id", userID),
			attribute.String("namespace", "universal"),
		)
		defer end()
		facts, _ := h.userMemory.Recall(spanCtx, userID, "universal", pctx.Request, h.maxUniversalFacts)
		telemetry.SetSpanAttributes(spanCtx,
			attribute.Int("facts_recalled", len(facts)),
			attribute.Int64("duration_ms", time.Since(universalStart).Milliseconds()),
		)
		return facts
	}()
	h.recordDebugInteraction(ctx, requestID, LLMInteraction{
		Type:       "user_memory_recall_universal",
		HookPhase:  HookPhasePre,
		Category:   "vector_db",
		Timestamp:  universalStart,
		DurationMs: time.Since(universalStart).Milliseconds(),
		Response:   fmt.Sprintf("%d universal facts recalled", len(universalFacts)),
		Success:    true,
	})

	// 6. Deduplicate and filter by confidence.
	facts := deduplicateAndFilter(identityFacts, stableFacts, summaryFacts, queryFacts, universalFacts, h.minConfidence)
	facts = selectFactsForPrompt(facts, pctx.Request, h.maxDurableFacts, h.maxTransientFacts, h.maxSummaryFacts, h.maxFacts)

	if len(facts) == 0 {
		return nil, nil // No facts to inject
	}

	// 7. Format into <user_profile> XML
	profile := formatUserProfile(facts)

	// 8. Inject into enrichments (map[string]interface{})
	if pctx.Enrichments == nil {
		pctx.Enrichments = make(map[string]interface{})
	}
	pctx.Enrichments[core.EnrichmentUserProfile] = profile

	// 9. Record enrichment injection + log + span event
	h.recordDebugInteraction(ctx, requestID, LLMInteraction{
		Type:       "user_memory_enrichment_injected",
		HookPhase:  HookPhasePre,
		Category:   "logic",
		Timestamp:  time.Now(),
		DurationMs: 0,
		Response:   fmt.Sprintf("%d facts injected, %d chars", len(facts), len(profile)),
		Success:    true,
	})
	telemetry.AddSpanEvent(ctx, "user_memory.enrichment.injected",
		attribute.String("request_id", requestID),
		attribute.String("user_id", userID),
		attribute.Int("facts_count", len(facts)),
		attribute.Int("profile_chars", len(profile)),
	)
	if h.logger != nil {
		h.logger.InfoWithContext(ctx, "User memory enrichment injected", map[string]interface{}{
			"operation":   "user_memory_enrichment",
			"request_id":  requestID,
			"user_id":     userID,
			"namespace":   h.namespace,
			"facts_count": len(facts),
			"duration_ms": time.Since(startTime).Milliseconds(),
		})
	}

	return nil, nil // Never short-circuits
}

// ─── Helpers ─────────────────────────────────────────────────────────────────

func (h *UserMemoryEnrichmentHook) recallStableNamespaceFacts(ctx context.Context, userID string) []core.UserFact {
	if h.namespace == "" || h.namespace == "universal" || h.maxStableFacts <= 0 {
		return nil
	}

	stableCategories := []string{"constraint", "preference", "relationship"}
	var stableFacts []core.UserFact
	for _, category := range stableCategories {
		facts, _ := h.userMemory.RecallByCategory(ctx, userID, h.namespace, category, h.maxStableFacts)
		stableFacts = append(stableFacts, facts...)
	}

	return stableFacts
}

// deduplicateAndFilter merges fact lists, removes duplicates by FactID,
// and filters out facts below the confidence threshold.
// Priority ordering: identity > stable namespace facts > summaries > query-specific > universal.
func deduplicateAndFilter(identityFacts, stableFacts, summaryFacts, queryFacts, universalFacts []core.UserFact, minConfidence float64) []core.UserFact {
	seen := make(map[string]bool)
	var result []core.UserFact

	appendFacts := func(facts []core.UserFact) {
		for _, fact := range facts {
			if !seen[fact.FactID] && fact.Confidence >= minConfidence {
				seen[fact.FactID] = true
				result = append(result, fact)
			}
		}
	}

	appendFacts(identityFacts)
	appendFacts(stableFacts)
	appendFacts(summaryFacts)
	appendFacts(queryFacts)
	appendFacts(universalFacts)
	return result
}

func selectFactsForPrompt(facts []core.UserFact, request string, maxDurableFacts, maxTransientFacts, maxSummaryFacts, maxTotal int) []core.UserFact {
	if len(facts) == 0 {
		return nil
	}

	relevantToRequest := func(content string) bool {
		return isFactRelevantToRequest(content, request)
	}
	seenContent := make(map[string]bool)
	var selected []core.UserFact
	var durableFacts []core.UserFact
	var transientFacts []core.UserFact
	var summaryFacts []core.UserFact
	var otherFacts []core.UserFact

	appendFact := func(dst *[]core.UserFact, fact core.UserFact) bool {
		normalizedContent := normalizeFactContent(fact.Content)
		if normalizedContent == "" || seenContent[normalizedContent] {
			return false
		}
		seenContent[normalizedContent] = true
		*dst = append(*dst, fact)
		return true
	}

	for _, fact := range facts {
		switch core.EffectiveUserFactLifetime(fact) {
		case core.UserFactLifetimeDurable:
			appendFact(&durableFacts, fact)
		case core.UserFactLifetimeTransient:
			if relevantToRequest(fact.Content) {
				appendFact(&transientFacts, fact)
			}
		case core.UserFactLifetimeSummary:
			if relevantToRequest(fact.Content) {
				appendFact(&summaryFacts, fact)
			}
		default:
			if relevantToRequest(fact.Content) {
				appendFact(&otherFacts, fact)
			}
		}
	}

	if maxTotal <= 0 {
		selected = append(selected, durableFacts...)
		selected = append(selected, transientFacts...)
		selected = append(selected, summaryFacts...)
		selected = append(selected, otherFacts...)
		return selected
	}

	durableBudget := minInt(maxDurableFacts, maxTotal)
	transientBudget := minInt(maxTransientFacts, maxTotal)
	summaryBudget := minInt(maxSummaryFacts, maxTotal)

	durableTake := minInt(len(durableFacts), durableBudget)
	selected = append(selected, durableFacts[:durableTake]...)

	remaining := maxTotal - len(selected)
	if remaining <= 0 {
		return selected
	}

	transientTake := minInt(len(transientFacts), minInt(transientBudget, remaining))
	selected = append(selected, transientFacts[:transientTake]...)

	remaining = maxTotal - len(selected)
	if remaining <= 0 {
		return selected
	}

	summaryTake := minInt(len(summaryFacts), minInt(summaryBudget, remaining))
	selected = append(selected, summaryFacts[:summaryTake]...)

	remaining = maxTotal - len(selected)
	if remaining <= 0 {
		return selected
	}

	if transientTake < transientBudget {
		extraTransient := minInt(len(transientFacts)-transientTake, remaining)
		selected = append(selected, transientFacts[transientTake:transientTake+extraTransient]...)
		remaining = maxTotal - len(selected)
	}
	if remaining <= 0 {
		return selected
	}

	if summaryTake < summaryBudget {
		extraSummary := minInt(len(summaryFacts)-summaryTake, remaining)
		selected = append(selected, summaryFacts[summaryTake:summaryTake+extraSummary]...)
		remaining = maxTotal - len(selected)
	}
	if remaining <= 0 {
		return selected
	}

	if durableTake < len(durableFacts) {
		extraDurable := minInt(len(durableFacts)-durableTake, remaining)
		selected = append(selected, durableFacts[durableTake:durableTake+extraDurable]...)
		remaining = maxTotal - len(selected)
	}
	if remaining <= 0 {
		return selected
	}

	selected = append(selected, otherFacts[:minInt(len(otherFacts), remaining)]...)
	return selected
}

func minInt(a int, b int) int {
	if a < b {
		return a
	}
	return b
}

func isFactRelevantToRequest(content string, request string) bool {
	requestTokens := tokenizeMemoryText(request)
	if len(requestTokens) == 0 {
		return false
	}

	contentLower := strings.ToLower(content)
	for token := range requestTokens {
		if strings.Contains(contentLower, token) {
			return true
		}
	}
	return false
}

func normalizeFactContent(content string) string {
	return strings.Join(strings.Fields(strings.ToLower(content)), " ")
}

func tokenizeMemoryText(text string) map[string]struct{} {
	stopWords := map[string]struct{}{
		"a": {}, "about": {}, "after": {}, "all": {}, "and": {}, "are": {}, "assistant": {}, "at": {},
		"be": {}, "been": {}, "before": {}, "by": {}, "can": {}, "current": {}, "destination": {},
		"did": {}, "discussed": {}, "for": {}, "from": {}, "give": {}, "home": {}, "i": {}, "in": {},
		"is": {}, "it": {}, "its": {}, "like": {}, "month": {}, "my": {}, "next": {}, "of": {}, "on": {},
		"or": {}, "our": {}, "planned": {}, "planning": {}, "prefers": {}, "requested": {}, "starting": {},
		"tell": {}, "that": {}, "the": {}, "their": {}, "them": {}, "these": {}, "this": {}, "to": {},
		"travel": {}, "trip": {}, "user": {}, "was": {}, "week": {}, "with": {}, "would": {}, "your": {},
		"family": {}, "friendly": {}, "airport": {}, "budget": {}, "options": {},
	}

	tokens := make(map[string]struct{})
	lower := strings.ToLower(text)
	for _, token := range strings.FieldsFunc(lower, func(r rune) bool {
		return (r < 'a' || r > 'z') && (r < '0' || r > '9')
	}) {
		if len(token) < 3 {
			continue
		}
		if _, skip := stopWords[token]; skip {
			continue
		}
		tokens[token] = struct{}{}
	}
	return tokens
}

// formatUserProfile renders facts into the <user_profile> XML tag,
// grouped by category. Durable facts carry a confidence label; transient
// and summary facts carry a recency label instead — their truthfulness
// decays with time independently of the confidence they were extracted at,
// and a stale "high confidence" label misleads the planner when the
// conversation has since moved on.
//
// Production callers use this entry point; it reads the current wall clock.
// Deterministic tests should use formatUserProfileAt with a fixed `now`.
func formatUserProfile(facts []core.UserFact) string {
	return formatUserProfileAt(facts, time.Now())
}

// formatUserProfileAt is the testable form of formatUserProfile: the caller
// supplies the reference time used for recency labels on transient and
// summary facts. Extracted so tests can assert exact age phrasing without
// races against the wall clock between fact construction and rendering.
func formatUserProfileAt(facts []core.UserFact, now time.Time) string {
	if len(facts) == 0 {
		return ""
	}

	// Group by category
	grouped := make(map[string][]core.UserFact)
	categoryOrder := []string{"identity", "constraint", "preference", "relationship", "context", "summary"}
	for _, fact := range facts {
		grouped[fact.Category] = append(grouped[fact.Category], fact)
	}

	var sb strings.Builder
	sb.WriteString("<user_profile>\n")

	for _, category := range categoryOrder {
		catFacts, ok := grouped[category]
		if !ok || len(catFacts) == 0 {
			continue
		}
		// Capitalize category name for display
		displayName := category
		if len(displayName) > 0 {
			displayName = strings.ToUpper(displayName[:1]) + displayName[1:]
		}
		sb.WriteString(displayName + ":\n")
		for _, fact := range catFacts {
			fmt.Fprintf(&sb, "- %s (%s, %s)\n", fact.Content, fact.Source, factProvenanceLabel(fact, now))
		}
		sb.WriteString("\n")
	}

	sb.WriteString("</user_profile>")
	return sb.String()
}

// factProvenanceLabel returns the trailing qualifier for a fact rendered in
// <user_profile>. Durable profile facts (identity, preference, constraint,
// relationship) get a confidence band; transient and summary facts get a
// recency phrase because their current truth depends on time, not on how
// strongly the user stated them once.
func factProvenanceLabel(fact core.UserFact, now time.Time) string {
	switch core.EffectiveUserFactLifetime(fact) {
	case core.UserFactLifetimeTransient, core.UserFactLifetimeSummary:
		return "recorded " + humanizeFactAge(fact.UpdatedAt, now)
	}
	switch {
	case fact.Confidence < 0.7:
		return "low confidence"
	case fact.Confidence < 0.9:
		return "medium confidence"
	default:
		return "high confidence"
	}
}

// humanizeFactAge renders the age of a fact in human terms. The output
// is coarse on purpose — the planner only needs "is this fresh or stale"
// to decide whether to trust it against the live conversation, not a
// precise duration.
func humanizeFactAge(updatedAt time.Time, now time.Time) string {
	if updatedAt.IsZero() {
		return "earlier"
	}
	diff := now.Sub(updatedAt)
	if diff < 0 {
		return "today"
	}
	switch {
	case diff < 24*time.Hour:
		return "today"
	case diff < 48*time.Hour:
		return "yesterday"
	case diff < 7*24*time.Hour:
		return fmt.Sprintf("%d days ago", int(diff/(24*time.Hour)))
	case diff < 30*24*time.Hour:
		weeks := int(diff / (7 * 24 * time.Hour))
		if weeks == 1 {
			return "1 week ago"
		}
		return fmt.Sprintf("%d weeks ago", weeks)
	default:
		months := int(diff / (30 * 24 * time.Hour))
		if months <= 1 {
			return "over a month ago"
		}
		return fmt.Sprintf("over %d months ago", months)
	}
}
