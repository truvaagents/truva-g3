package orchestration

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/truvaagents/truva-g3/core"
	"github.com/truvaagents/truva-g3/telemetry"
	"go.opentelemetry.io/otel/attribute"
)

// Compile-time interface compliance check.
var _ core.AfterSynthesisHook = (*UserMemoryExtractionHook)(nil)

// reconcileInput pairs an extracted candidate with its similarity-search neighbors.
// Used by the extraction hook to feed both the batched and per-candidate
// reconciliation paths from a single search pass.
type reconcileInput struct {
	candidate core.UserFact
	neighbors []core.UserFact
}

// extractionInput is a value snapshot of the inputs extractAndStore needs.
// Captured at the AfterSynthesis dispatch boundary so the detached async goroutine
// never retains a pointer to the live *core.PipelineContext (avoids data races
// with subsequent pipeline code that may read/mutate the same context).
type extractionInput struct {
	request   string
	response  string
	userID    string
	requestID string
}

// ─── Extraction Hook (AfterSynthesis) ────────────────────────────────────────

// UserMemoryExtractionHook extracts user facts from conversations and stores them.
// Owns the reconciliation pipeline (extract → search similar → classify → store).
// Implements core.AfterSynthesisHook. Passes the response through unmodified.
//
// This is the Layer 3 primitive (direct construction via NewUserMemoryExtractionHook).
// It defaults to synchronous execution — AfterSynthesis blocks until extraction,
// reconciliation, and storage complete, matching Go's "no surprise goroutines" idiom
// and ensuring errors are observable and completion can be asserted in tests. Opt in
// to asynchronous execution via WithAsynchronousUserExtraction() when the caller is
// willing to own the lifecycle (Close()).
//
// Note: the Layer 1 preset — orchestration.BuildUserMemoryHooks — makes the opposite
// default choice (async on) because its target audience is chat agents where response
// latency matters and the preset itself returns an io.Closer that the caller drains.
// The two defaults are intentional: opinionated at Layer 1, boring at Layer 3.
type UserMemoryExtractionHook struct {
	userMemory        core.UserMemory
	embedder          core.EmbeddingClient
	aiClient          core.AIClient
	namespace         string
	logger            core.Logger
	extractor         UserFactExtractor
	reconciler        UserFactReconciler
	persistencePolicy UserFactPersistencePolicy
	summaryModel      string         // from TRUVAG3_USER_MEMORY_EXTRACTION_MODEL
	summaryMaxRespLen int            // from TRUVAG3_USER_MEMORY_SUMMARY_MAX_RESPONSE_LEN (default: 500)
	maxSplitClauses   int            // from TRUVAG3_USER_MEMORY_MAX_SPLIT_CLAUSES (default: 3)
	debugStore        LLMDebugStore  // optional — set by factory duck-typing propagation
	debugWg           sync.WaitGroup // tracks in-flight debug recordings
	asynchronous      bool           // default false (sync); enable via WithAsynchronousUserExtraction
	extractionWg      sync.WaitGroup // tracks in-flight async extraction goroutines
}

// UserMemoryExtractionOption configures UserMemoryExtractionHook (Layer 3).
type UserMemoryExtractionOption func(*UserMemoryExtractionHook)

// WithUserExtractionPersistencePolicy overrides the default post-extraction
// store/drop policy for direct hook construction.
func WithUserExtractionPersistencePolicy(p UserFactPersistencePolicy) UserMemoryExtractionOption {
	return func(h *UserMemoryExtractionHook) {
		if p != nil {
			h.persistencePolicy = p
		}
	}
}

// WithAsynchronousUserExtraction enables non-blocking extraction at Layer 3.
// When set, AfterSynthesis spawns a detached goroutine (via context.WithoutCancel
// so the request's trace context is preserved while its cancellation is not) and
// returns immediately. Close() waits for in-flight extractions to complete.
//
// Default is synchronous: AfterSynthesis blocks until extraction completes,
// which is the idiomatic Go default (no surprise goroutines, errors observable,
// completion assertable in tests).
func WithAsynchronousUserExtraction() UserMemoryExtractionOption {
	return func(h *UserMemoryExtractionHook) {
		h.asynchronous = true
	}
}

// NewUserMemoryExtractionHook creates an extraction hook. Layer 3 constructor.
// Passing nil for extractor and/or reconciler uses the same framework defaults
// as BuildUserMemoryHooks, so callers can override one concern without
// reconstructing the entire stack.
func NewUserMemoryExtractionHook(
	userMem core.UserMemory,
	embedder core.EmbeddingClient,
	aiClient core.AIClient,
	namespace string,
	logger core.Logger,
	extractor UserFactExtractor,
	reconciler UserFactReconciler,
	opts ...UserMemoryExtractionOption,
) *UserMemoryExtractionHook {
	// Component-aware logger setup (per LOGGING_IMPLEMENTATION_GUIDE §Component-Aware)
	var wrappedLogger core.Logger = &core.NoOpLogger{}
	if logger != nil {
		if cal, ok := logger.(core.ComponentAwareLogger); ok {
			wrappedLogger = cal.WithComponent("framework/orchestration")
		} else {
			wrappedLogger = logger
		}
	}

	// Read env vars for numeric tuning once at construction (not per-request)
	summaryMaxRespLen := 500
	if v, _ := strconv.Atoi(os.Getenv("TRUVAG3_USER_MEMORY_SUMMARY_MAX_RESPONSE_LEN")); v > 0 {
		summaryMaxRespLen = v
	}
	maxSplitClauses := 3
	if v, _ := strconv.Atoi(os.Getenv("TRUVAG3_USER_MEMORY_MAX_SPLIT_CLAUSES")); v > 0 {
		maxSplitClauses = v
	}

	h := &UserMemoryExtractionHook{
		userMemory:        userMem,
		embedder:          embedder,
		aiClient:          aiClient,
		namespace:         namespace,
		logger:            wrappedLogger,
		extractor:         extractor,
		reconciler:        reconciler,
		persistencePolicy: NewDefaultUserFactPersistencePolicy(),
		summaryModel:      os.Getenv("TRUVAG3_USER_MEMORY_EXTRACTION_MODEL"),
		summaryMaxRespLen: summaryMaxRespLen,
		maxSplitClauses:   maxSplitClauses,
	}
	if h.extractor == nil {
		h.extractor = defaultUserFactExtractor(aiClient, wrappedLogger)
	}
	if h.reconciler == nil {
		h.reconciler = defaultUserFactReconciler(userMem, aiClient, wrappedLogger)
	}
	for _, opt := range opts {
		opt(h)
	}
	return h
}

func (h *UserMemoryExtractionHook) Name() string { return "user-memory-extraction" }

// SetLLMDebugStore enables debug recording. Called by factory duck-typing propagation.
func (h *UserMemoryExtractionHook) SetLLMDebugStore(store LLMDebugStore) {
	h.debugStore = store
}

// deferLLMRecordingIfWeWillRecord marks ctx so InstrumentedAIClient skips
// its own agent_llm_call emission when this hook will emit a typed
// user_memory_* record itself. Gated on debugStore presence to preserve
// the graceful-fallback invariant in orchestration/ARCHITECTURE.md.
// See orchestration/bugs/BUG_LLM_INTERACTION_DOUBLE_RECORDING.md.
func (h *UserMemoryExtractionHook) deferLLMRecordingIfWeWillRecord(ctx context.Context) context.Context {
	if h.debugStore == nil {
		return ctx
	}
	return telemetry.WithLLMCallRecordingDeferred(ctx)
}

// recordDebugInteraction asynchronously records a debug interaction.
func (h *UserMemoryExtractionHook) recordDebugInteraction(ctx context.Context, requestID string, interaction LLMInteraction) {
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

// AfterSynthesis extracts facts from the completed conversation and stores them.
// The pipeline runner auto-creates the parent span:
//
//	pipeline.hook.after_synthesis.user-memory-extraction
//
// The response is passed through unmodified — this hook only writes to memory.
//
// Execution mode:
//   - Synchronous (default): blocks until extraction, reconciliation, and storage
//     complete. Errors are logged (hook is fail-open) but extraction latency is
//     added to the response path.
//   - Asynchronous (opt-in via WithAsynchronousUserExtraction): spawns a detached
//     goroutine that inherits the request's trace context via context.WithoutCancel
//     but does not honour the request's cancellation. AfterSynthesis returns
//     immediately. Call Close() on shutdown to wait for in-flight goroutines.
func (h *UserMemoryExtractionHook) AfterSynthesis(ctx context.Context, pctx *core.PipelineContext, response string) (string, error) {
	userID, ok := pctx.Metadata["user_id"].(string)
	if !ok || userID == "" {
		return response, nil // No user context — skip, pass response through
	}

	// Snapshot the inputs as values before (possibly) crossing a goroutine
	// boundary — the detached goroutine must not retain *pctx.
	snap := extractionInput{
		request:   pctx.Request,
		response:  response,
		userID:    userID,
		requestID: GetRequestID(ctx),
	}

	if h.asynchronous {
		// Detach from the request's cancellation/deadline but preserve trace
		// context and baggage for observability correlation.
		bgCtx := context.WithoutCancel(ctx)
		h.extractionWg.Add(1)
		go func() {
			defer h.extractionWg.Done()
			h.extractAndStore(bgCtx, snap)
		}()
		return response, nil
	}

	h.extractAndStore(ctx, snap)
	return response, nil
}

// Close waits for in-flight asynchronous extractions (if any) and in-flight debug
// interaction recordings to complete. Always returns nil — matches io.Closer.
// Safe to call even when the hook runs synchronously (both WaitGroups are zero).
func (h *UserMemoryExtractionHook) Close() error {
	h.extractionWg.Wait()
	h.debugWg.Wait()
	return nil
}

// extractAndStore runs the extraction/reconciliation/store pipeline. Called
// synchronously by AfterSynthesis in sync mode, and from a detached goroutine
// in async mode. All errors are logged via the hook's logger; none propagate.
//
// Takes an immutable extractionInput snapshot rather than *core.PipelineContext
// so the async goroutine cannot race with subsequent pipeline code that touches
// the live context.
func (h *UserMemoryExtractionHook) extractAndStore(ctx context.Context, in extractionInput) {
	requestID := in.requestID
	userID := in.userID
	response := in.response
	startTime := time.Now()

	// Parent span for the entire extract → reconcile → store → summarize
	// pipeline. AfterSynthesis already wraps the call in a hook span, but in
	// async mode that span has ended before this goroutine runs — so we own
	// our own here. End at the bottom via defer.
	ctx, endExtractionSpan := telemetry.StartChildSpan(ctx, "user_memory.extraction",
		attribute.String("user_id", userID),
		attribute.String("namespace", h.namespace),
	)
	defer endExtractionSpan()

	// 1. Extract candidate facts via LLM
	// Wrapped in a closure so the span's End() is deferred — on panic the
	// span still closes cleanly. Same pattern is used for the reconciliation
	// batch and summary spans below.
	extractionStart := time.Now()
	var extractResult *ExtractResult
	var err error
	var extractionDuration time.Duration
	func() {
		llmCtx, endLLMSpan := telemetry.StartChildSpan(ctx, "user_memory.extraction.llm",
			attribute.String("user_id", userID),
			attribute.Int("request_chars", len(in.request)),
			attribute.Int("response_chars", len(response)),
		)
		defer endLLMSpan()
		llmCtx = telemetry.WithBaggage(llmCtx, "ai.purpose", "user_memory_extraction")
		telemetry.AddSpanEvent(llmCtx, "user_memory.extraction.llm_request",
			attribute.String("request_id", requestID),
			attribute.String("user_id", userID),
		)
		extractCallCtx := h.deferLLMRecordingIfWeWillRecord(llmCtx)
		extractResult, err = h.extractor.ExtractFacts(extractCallCtx, in.request, response, nil)
		extractionDuration = time.Since(extractionStart)
		if extractResult != nil && extractResult.Response != nil {
			telemetry.SetSpanAttributes(llmCtx,
				attribute.String("ai.model", extractResult.Response.Model),
				attribute.String("ai.provider", extractResult.Response.Provider),
				attribute.Int("ai.prompt_tokens", extractResult.Response.Usage.PromptTokens),
				attribute.Int("ai.completion_tokens", extractResult.Response.Usage.CompletionTokens),
				attribute.Int("ai.total_tokens", extractResult.Response.Usage.TotalTokens),
			)
		}
		telemetry.SetSpanAttributes(llmCtx, attribute.Int64("duration_ms", extractionDuration.Milliseconds()))
	}()
	if err != nil {
		h.recordDebugInteraction(ctx, requestID, LLMInteraction{
			Type: "user_memory_extraction", HookPhase: HookPhasePost, Category: "llm",
			Timestamp: extractionStart, DurationMs: extractionDuration.Milliseconds(),
			Prompt:  fmt.Sprintf("[request] %s\n[response] %s", truncateUTF8(in.request, 200), truncateUTF8(response, 300)),
			Success: false, Error: err.Error(),
		})
		telemetry.RecordSpanError(ctx, err)
		if h.logger != nil {
			h.logger.WarnWithContext(ctx, "User fact extraction failed", map[string]interface{}{
				"operation":  "user_memory_extraction",
				"request_id": requestID,
				"user_id":    userID,
				"error":      err.Error(),
				"error_type": "llm_unavailable",
			})
		}
		return // fail-open — response already passed through by the dispatcher
	}
	candidates := extractResult.Facts
	extractedCount := len(candidates)
	candidates = h.expandCandidatesForStorage(ctx, requestID, userID, candidates)
	extractRec := LLMInteraction{
		Type: "user_memory_extraction", HookPhase: HookPhasePost, Category: "llm",
		Timestamp: extractionStart, DurationMs: extractionDuration.Milliseconds(),
		Prompt:   fmt.Sprintf("[request] %s\n[response] %s", truncateUTF8(in.request, 200), truncateUTF8(response, 300)),
		Response: fmt.Sprintf("%d facts extracted, %d candidates after compaction", extractedCount, len(candidates)),
		Success:  true,
	}
	if extractResult.Response != nil {
		extractRec.Model = extractResult.Response.Model
		extractRec.Provider = extractResult.Response.Provider
		extractRec.PromptTokens = extractResult.Response.Usage.PromptTokens
		extractRec.CompletionTokens = extractResult.Response.Usage.CompletionTokens
		extractRec.TotalTokens = extractResult.Response.Usage.TotalTokens
	}
	h.recordDebugInteraction(ctx, requestID, extractRec)

	// 2. Reconciliation pipeline.
	//
	// Two paths:
	//
	//   (a) Batched (default for LLMUserFactReconciler): gather all candidates
	//       and their neighbors via per-candidate similarity search, then issue
	//       ONE LLM call that classifies every candidate. Collapses N LLM calls
	//       into 1, with no behavioral change vs the per-candidate path.
	//
	//   (b) Per-candidate fallback: used when the reconciler does not implement
	//       BatchUserFactReconciler, when ReconcileBatch returns an error
	//       (parse failure, length mismatch, LLM error), or when the batch
	//       returns fewer results than candidates. Preserves the original loop.
	//
	// The similarity search step is intentionally NOT batched: vector queries
	// are fast (~15-50ms each), and the original observed bottleneck was the
	// per-candidate LLM reconciliation, not the search.
	storedCount := 0

	// 2a. Per-candidate similarity search (always runs).
	// We deliberately do NOT create a span per candidate (would scale with the
	// user's memory size) — instead we aggregate count + total duration onto
	// the parent extraction span at the end.
	inputs := make([]reconcileInput, 0, len(candidates))
	var totalSearchDurMs int64
	var totalNeighborsFound int
	for _, candidate := range candidates {
		candidate = ensureFactLifetimeMetadata(candidate)
		candidate.Namespace = h.namespace
		decision, err := h.persistencePolicy.Evaluate(ctx, UserFactPersistenceInput{
			UserID:        userID,
			Namespace:     h.namespace,
			UserRequest:   in.request,
			AgentResponse: response,
			Fact:          candidate,
		})
		if err != nil {
			wrappedErr := &core.FrameworkError{
				Op:      "user_memory_persistence_policy",
				Kind:    "internal",
				Message: "user fact persistence policy evaluation failed",
				Err:     err,
			}
			h.recordDebugInteraction(ctx, requestID, LLMInteraction{
				Type:       "user_memory_persistence_policy",
				HookPhase:  HookPhasePost,
				Category:   "logic",
				Timestamp:  time.Now(),
				DurationMs: 0,
				Prompt:     candidate.Content,
				Success:    false,
				Error:      wrappedErr.Error(),
			})
			telemetry.RecordSpanError(ctx, wrappedErr)
			telemetry.AddSpanEvent(ctx, "user_memory.persistence_policy.error",
				attribute.String("request_id", requestID),
				attribute.String("user_id", userID),
				attribute.String("namespace", h.namespace),
				attribute.String("fact_category", candidate.Category),
				attribute.String("error_type", "policy_eval_error"),
			)
			if h.logger != nil {
				h.logger.WarnWithContext(ctx, "User fact persistence policy failed", map[string]interface{}{
					"operation":  "user_memory_persistence_policy",
					"request_id": requestID,
					"user_id":    userID,
					"error":      wrappedErr.Error(),
					"error_type": "policy_eval_error",
				})
			}
			continue
		}
		if !decision.Store {
			telemetry.AddSpanEvent(ctx, "user_memory.persistence_policy.drop",
				attribute.String("request_id", requestID),
				attribute.String("user_id", userID),
				attribute.String("namespace", h.namespace),
				attribute.String("fact_category", candidate.Category),
				attribute.String("decision", "drop"),
			)
			h.recordDebugInteraction(ctx, requestID, LLMInteraction{
				Type:       "user_memory_persistence_policy",
				HookPhase:  HookPhasePost,
				Category:   "logic",
				Timestamp:  time.Now(),
				DurationMs: 0,
				Prompt:     candidate.Content,
				Response:   "dropped",
				Success:    true,
			})
			continue
		}
		telemetry.AddSpanEvent(ctx, "user_memory.persistence_policy.keep",
			attribute.String("request_id", requestID),
			attribute.String("user_id", userID),
			attribute.String("namespace", h.namespace),
			attribute.String("fact_category", candidate.Category),
			attribute.String("decision", "keep"),
		)
		candidate = decision.Fact
		candidate = ensureFactLifetimeMetadata(candidate)
		if candidate.Namespace == "" {
			candidate.Namespace = h.namespace
		}

		searchStart := time.Now()
		existing, _ := h.userMemory.Recall(ctx, userID, h.namespace, candidate.Content, 5)
		searchDur := time.Since(searchStart)
		totalSearchDurMs += searchDur.Milliseconds()
		totalNeighborsFound += len(existing)
		h.recordDebugInteraction(ctx, requestID, LLMInteraction{
			Type: "user_memory_similarity_search", HookPhase: HookPhasePost, Category: "vector_db",
			Timestamp: searchStart, DurationMs: searchDur.Milliseconds(),
			Prompt:   truncateUTF8(candidate.Content, 200),
			Response: fmt.Sprintf("%d similar facts found", len(existing)),
			Success:  true,
		})

		inputs = append(inputs, reconcileInput{candidate: candidate, neighbors: existing})
	}
	// Aggregate similarity-search stats on the parent extraction span — gives
	// visibility into vector-DB load per request without one span per candidate.
	telemetry.SetSpanAttributes(ctx,
		attribute.Int("user_memory.search.candidate_count", len(inputs)),
		attribute.Int64("user_memory.search.total_duration_ms", totalSearchDurMs),
		attribute.Int("user_memory.search.total_neighbors_found", totalNeighborsFound),
	)

	// 2b. Reconcile (batched if supported, with fallback to per-candidate).
	results, batchUsed := h.reconcileAll(ctx, userID, requestID, inputs)
	skipCompactedWrites := h.markRedundantDurableWrites(ctx, requestID, userID, inputs, results)

	// 2c. Apply each result. Skipped=true means per-candidate reconcile
	// failed for that input; we skip those (matching the prior `continue`
	// semantics).
	for i := range results {
		result := &results[i]
		if result.Skipped {
			continue
		}
		if skipCompactedWrites[i] {
			continue
		}
		candidate := inputs[i].candidate
		existing := inputs[i].neighbors

		// Record per-candidate reconciliation interaction.
		// In batched mode, the actual LLM call is logged once via reconcileAll;
		// here we emit a per-candidate row with category "derived" so the
		// registry viewer can attribute each operation back to the batch
		// WITHOUT double-counting tokens (the "llm" rows are filtered for
		// token totals, "derived" rows are not).
		recType, recCategory := "user_memory_reconciliation", "llm"
		if len(existing) == 0 {
			recType, recCategory = "user_memory_reconciliation_skip", "logic"
		} else if batchUsed {
			recType, recCategory = "user_memory_reconciliation_batch_item", "derived"
		}
		recInteraction := LLMInteraction{
			Type: recType, HookPhase: HookPhasePost, Category: recCategory,
			Timestamp:  time.Now(),
			DurationMs: 0,
			Prompt:     fmt.Sprintf("[candidate] %s\n[existing] %d facts", candidate.Content, len(existing)),
			Response:   fmt.Sprintf("operation=%s target=%s", result.Operation, result.TargetFactID),
			Success:    true,
		}
		// Per-candidate path populates per-call token metadata; batched path
		// leaves it on the single batch interaction record (see reconcileAll).
		if !batchUsed && result.Response != nil {
			recInteraction.Model = result.Response.Model
			recInteraction.Provider = result.Response.Provider
			recInteraction.PromptTokens = result.Response.Usage.PromptTokens
			recInteraction.CompletionTokens = result.Response.Usage.CompletionTokens
			recInteraction.TotalTokens = result.Response.Usage.TotalTokens
		}
		h.recordDebugInteraction(ctx, requestID, recInteraction)

		telemetry.Counter("user_memory.reconciliation.operation",
			"module", telemetry.ModuleOrchestration, "operation", result.Operation)

		// 2d. Apply operation.
		switch result.Operation {
		case "ADD", "UPDATE":
			result.MergedFact = h.finalizeReconciledFact(ctx, requestID, userID, result.TargetFactID, existing, result.MergedFact)
			result.MergedFact = ensureFactLifetimeMetadata(result.MergedFact)
			rememberStart := time.Now()
			if err := h.userMemory.Remember(ctx, userID, result.MergedFact); err != nil {
				h.recordDebugInteraction(ctx, requestID, LLMInteraction{
					Type: "user_memory_remember", HookPhase: HookPhasePost, Category: "storage",
					Timestamp: rememberStart, DurationMs: time.Since(rememberStart).Milliseconds(),
					Prompt:  fmt.Sprintf("[%s] %s", result.MergedFact.Category, result.MergedFact.Content),
					Success: false, Error: err.Error(),
				})
				telemetry.RecordSpanError(ctx, err)
				if h.logger != nil {
					h.logger.WarnWithContext(ctx, "User fact store failed", map[string]interface{}{
						"operation":  "user_memory_remember",
						"request_id": requestID,
						"user_id":    userID,
						"error":      err.Error(),
						"error_type": "knowledge_store",
					})
				}
			} else {
				h.recordDebugInteraction(ctx, requestID, LLMInteraction{
					Type: "user_memory_remember", HookPhase: HookPhasePost, Category: "storage",
					Timestamp: rememberStart, DurationMs: time.Since(rememberStart).Milliseconds(),
					Prompt:   fmt.Sprintf("[%s] %s", result.MergedFact.Category, result.MergedFact.Content),
					Response: fmt.Sprintf("stored as %s (confidence: %.2f)", result.Operation, result.MergedFact.Confidence),
					Success:  true,
				})
				storedCount++
			}
		case "CONTRADICT":
			if result.TargetFactID != "" {
				if admin, ok := h.userMemory.(core.UserMemoryAdmin); ok {
					_ = admin.ForgetFact(ctx, userID, result.TargetFactID)
				}
			}
			result.MergedFact = h.finalizeReconciledFact(ctx, requestID, userID, result.TargetFactID, existing, result.MergedFact)
			result.MergedFact = ensureFactLifetimeMetadata(result.MergedFact)
			rememberStart := time.Now()
			if err := h.userMemory.Remember(ctx, userID, result.MergedFact); err != nil {
				h.recordDebugInteraction(ctx, requestID, LLMInteraction{
					Type: "user_memory_remember", HookPhase: HookPhasePost, Category: "storage",
					Timestamp: rememberStart, DurationMs: time.Since(rememberStart).Milliseconds(),
					Prompt:  fmt.Sprintf("[%s] %s", result.MergedFact.Category, result.MergedFact.Content),
					Success: false, Error: err.Error(),
				})
				telemetry.RecordSpanError(ctx, err)
				if h.logger != nil {
					h.logger.WarnWithContext(ctx, "User fact store failed", map[string]interface{}{
						"operation":  "user_memory_remember",
						"request_id": requestID,
						"user_id":    userID,
						"error":      err.Error(),
						"error_type": "knowledge_store",
					})
				}
			} else {
				h.recordDebugInteraction(ctx, requestID, LLMInteraction{
					Type: "user_memory_remember", HookPhase: HookPhasePost, Category: "storage",
					Timestamp: rememberStart, DurationMs: time.Since(rememberStart).Milliseconds(),
					Prompt:   fmt.Sprintf("[%s] %s", result.MergedFact.Category, result.MergedFact.Content),
					Response: fmt.Sprintf("stored as %s (confidence: %.2f)", result.Operation, result.MergedFact.Confidence),
					Success:  true,
				})
				storedCount++
			}
		case "DUPLICATE":
			// No action needed
		}
	}

	// 3. Generate session summary (category: "summary")
	if h.aiClient != nil && in.request != "" && response != "" {
		// Truncate response to bounded length for prompt token control.
		// h.summaryMaxRespLen read from TRUVAG3_USER_MEMORY_SUMMARY_MAX_RESPONSE_LEN at construction.
		truncatedResponse := response
		if len(truncatedResponse) > h.summaryMaxRespLen {
			truncatedResponse = truncateUTF8(truncatedResponse, h.summaryMaxRespLen)
		}

		summaryPrompt := fmt.Sprintf(summaryPromptTemplate, in.request, truncatedResponse)

		summaryOpts := &core.AIOptions{
			MaxTokens:   200,
			Temperature: 0.1,
		}
		if h.summaryModel != "" {
			summaryOpts.Model = h.summaryModel
		}

		summaryStart := time.Now()
		// Closure scopes the deferred span end so a panic inside the LLM call
		// still closes the span cleanly.
		var summaryResp *core.AIResponse
		var summaryErr error
		var summaryDuration time.Duration
		func() {
			summaryCtx, endSummarySpan := telemetry.StartChildSpan(ctx, "user_memory.summary",
				attribute.String("user_id", userID),
				attribute.Int("prompt_chars", len(summaryPrompt)),
				attribute.String("ai.model", summaryOpts.Model),
			)
			defer endSummarySpan()
			summaryCtx = telemetry.WithBaggage(summaryCtx, "ai.purpose", "user_memory_summary")
			telemetry.AddSpanEvent(summaryCtx, "user_memory.summary.llm_request",
				attribute.String("request_id", requestID),
				attribute.String("user_id", userID),
			)
			summaryCallCtx := h.deferLLMRecordingIfWeWillRecord(summaryCtx)
			summaryResp, summaryErr = h.aiClient.GenerateResponse(summaryCallCtx, summaryPrompt, summaryOpts)
			summaryDuration = time.Since(summaryStart)
			if summaryResp != nil {
				telemetry.SetSpanAttributes(summaryCtx,
					attribute.String("ai.model", summaryResp.Model),
					attribute.String("ai.provider", summaryResp.Provider),
					attribute.Int("ai.prompt_tokens", summaryResp.Usage.PromptTokens),
					attribute.Int("ai.completion_tokens", summaryResp.Usage.CompletionTokens),
					attribute.Int("ai.total_tokens", summaryResp.Usage.TotalTokens),
					attribute.Int("response_chars", len(summaryResp.Content)),
				)
			}
			telemetry.SetSpanAttributes(summaryCtx, attribute.Int64("duration_ms", summaryDuration.Milliseconds()))
			if summaryErr != nil {
				telemetry.RecordSpanError(summaryCtx, summaryErr)
			}
		}()
		if summaryErr != nil {
			h.recordDebugInteraction(ctx, requestID, LLMInteraction{
				Type: "user_memory_summary", HookPhase: HookPhasePost, Category: "llm",
				Timestamp: summaryStart, DurationMs: summaryDuration.Milliseconds(),
				Prompt: summaryPrompt, Success: false, Error: summaryErr.Error(),
			})
			// Fail-open — summary is nice-to-have, not critical
			telemetry.RecordSpanError(ctx, summaryErr)
			if h.logger != nil {
				h.logger.WarnWithContext(ctx, "Session summary generation failed", map[string]interface{}{
					"operation":  "user_memory_summary",
					"request_id": requestID,
					"user_id":    userID,
					"error":      summaryErr.Error(),
					"error_type": "llm_unavailable",
				})
			}
		} else {
			h.recordDebugInteraction(ctx, requestID, LLMInteraction{
				Type: "user_memory_summary", HookPhase: HookPhasePost, Category: "llm",
				Timestamp:        summaryStart,
				DurationMs:       summaryDuration.Milliseconds(),
				Prompt:           summaryPrompt,
				Response:         summaryResp.Content,
				Model:            summaryResp.Model,
				Provider:         summaryResp.Provider,
				PromptTokens:     summaryResp.Usage.PromptTokens,
				CompletionTokens: summaryResp.Usage.CompletionTokens,
				TotalTokens:      summaryResp.Usage.TotalTokens,
				Temperature:      float64(summaryOpts.Temperature),
				MaxTokens:        summaryOpts.MaxTokens,
				Success:          true,
			})
			type summaryResult struct {
				Content string `json:"content"`
			}
			var sr summaryResult
			if parseErr := json.Unmarshal([]byte(cleanLLMJSONResponse(summaryResp.Content)), &sr); parseErr != nil {
				// JSON parse failure — log, don't swallow silently
				telemetry.RecordSpanError(ctx, parseErr)
				if h.logger != nil {
					h.logger.WarnWithContext(ctx, "Session summary JSON parse failed", map[string]interface{}{
						"operation":  "user_memory_summary",
						"request_id": requestID,
						"user_id":    userID,
						"error":      parseErr.Error(),
						"error_type": "parse_failure",
					})
				}
			} else if sr.Content != "" {
				summaryFact := core.UserFact{
					FactID:     uuid.New().String(),
					Namespace:  h.namespace,
					Category:   "summary",
					Content:    sr.Content,
					Source:     core.SourceDerived,
					Confidence: 0.80,
					CreatedAt:  time.Now(),
					UpdatedAt:  time.Now(),
				}
				summaryFact = ensureFactLifetimeMetadata(summaryFact)
				summaryDecision, policyErr := h.persistencePolicy.Evaluate(ctx, UserFactPersistenceInput{
					UserID:        userID,
					Namespace:     h.namespace,
					UserRequest:   in.request,
					AgentResponse: response,
					Fact:          summaryFact,
				})
				if policyErr != nil {
					wrappedErr := &core.FrameworkError{
						Op:      "user_memory_summary_persistence_policy",
						Kind:    "internal",
						Message: "user memory summary persistence policy evaluation failed",
						Err:     policyErr,
					}
					h.recordDebugInteraction(ctx, requestID, LLMInteraction{
						Type:       "user_memory_summary_persistence_policy",
						HookPhase:  HookPhasePost,
						Category:   "logic",
						Timestamp:  time.Now(),
						DurationMs: 0,
						Prompt:     summaryFact.Content,
						Success:    false,
						Error:      wrappedErr.Error(),
					})
					telemetry.RecordSpanError(ctx, wrappedErr)
					telemetry.AddSpanEvent(ctx, "user_memory.persistence_policy.error",
						attribute.String("request_id", requestID),
						attribute.String("user_id", userID),
						attribute.String("namespace", h.namespace),
						attribute.String("fact_category", summaryFact.Category),
						attribute.String("error_type", "policy_eval_error"),
					)
					if h.logger != nil {
						h.logger.WarnWithContext(ctx, "User memory summary persistence policy failed", map[string]interface{}{
							"operation":  "user_memory_summary_persistence_policy",
							"request_id": requestID,
							"user_id":    userID,
							"error":      wrappedErr.Error(),
							"error_type": "policy_eval_error",
						})
					}
				} else if !summaryDecision.Store {
					telemetry.AddSpanEvent(ctx, "user_memory.persistence_policy.drop",
						attribute.String("request_id", requestID),
						attribute.String("user_id", userID),
						attribute.String("namespace", h.namespace),
						attribute.String("fact_category", summaryFact.Category),
						attribute.String("decision", "drop"),
					)
					h.recordDebugInteraction(ctx, requestID, LLMInteraction{
						Type:       "user_memory_summary_persistence_policy",
						HookPhase:  HookPhasePost,
						Category:   "logic",
						Timestamp:  time.Now(),
						DurationMs: 0,
						Prompt:     summaryFact.Content,
						Response:   "dropped",
						Success:    true,
					})
				} else {
					telemetry.AddSpanEvent(ctx, "user_memory.persistence_policy.keep",
						attribute.String("request_id", requestID),
						attribute.String("user_id", userID),
						attribute.String("namespace", h.namespace),
						attribute.String("fact_category", summaryFact.Category),
						attribute.String("decision", "keep"),
					)
					summaryFact = summaryDecision.Fact
					summaryFact = ensureFactLifetimeMetadata(summaryFact)
					if summaryFact.Namespace == "" {
						summaryFact.Namespace = h.namespace
					}
					if summaryFact.Category == "" {
						summaryFact.Category = "summary"
					}

					summaryRememberStart := time.Now()
					if storeErr := h.userMemory.Remember(ctx, userID, summaryFact); storeErr != nil {
						h.recordDebugInteraction(ctx, requestID, LLMInteraction{
							Type: "user_memory_summary_remember", HookPhase: HookPhasePost, Category: "storage",
							Timestamp: summaryRememberStart, DurationMs: time.Since(summaryRememberStart).Milliseconds(),
							Prompt: summaryFact.Content, Success: false, Error: storeErr.Error(),
						})
						telemetry.RecordSpanError(ctx, storeErr)
						if h.logger != nil {
							h.logger.WarnWithContext(ctx, "Session summary store failed", map[string]interface{}{
								"operation":  "user_memory_summary",
								"request_id": requestID,
								"user_id":    userID,
								"error":      storeErr.Error(),
								"error_type": "knowledge_store",
							})
						}
					} else {
						h.recordDebugInteraction(ctx, requestID, LLMInteraction{
							Type: "user_memory_summary_remember", HookPhase: HookPhasePost, Category: "storage",
							Timestamp: summaryRememberStart, DurationMs: time.Since(summaryRememberStart).Milliseconds(),
							Prompt: summaryFact.Content, Response: "stored", Success: true,
						})
						storedCount++
						telemetry.AddSpanEvent(ctx, "user_memory.summary.stored",
							attribute.String("request_id", requestID),
							attribute.String("user_id", userID),
						)
						telemetry.Counter("user_memory.summary.stored",
							"module", telemetry.ModuleOrchestration)
					}
				}
			}
		}
	}

	// 4. Log summary
	telemetry.AddSpanEvent(ctx, "user_memory.extraction.complete",
		attribute.String("request_id", requestID),
		attribute.String("user_id", userID),
		attribute.Int("candidates", len(candidates)),
		attribute.Int("stored", storedCount),
	)
	if h.logger != nil {
		h.logger.InfoWithContext(ctx, "User memory extraction complete", map[string]interface{}{
			"operation":        "user_memory_extraction",
			"request_id":       requestID,
			"user_id":          userID,
			"candidates_count": len(candidates),
			"stored_count":     storedCount,
			"duration_ms":      time.Since(startTime).Milliseconds(),
		})
	}

}

// reconcileAll runs reconciliation across all candidates, preferring the
// batched path when the configured reconciler implements BatchUserFactReconciler.
//
// Returns:
//   - results: slice of length len(inputs); entries with Skipped=true mean
//     reconciliation failed for that candidate and the caller must ignore it
//     (matches the prior `continue` semantics).
//   - batchUsed: true if the batched LLM call succeeded; false if the
//     per-candidate fallback ran (either because the reconciler does not
//     implement BatchUserFactReconciler or because the batched call failed
//     and we fell back).
//
// On batched success, exactly ONE debug interaction of type
// "user_memory_reconciliation_batch" is recorded with the full LLM metadata
// (model, provider, token usage). Per-candidate debug rows are emitted by the
// caller with category "derived" so the totals add up correctly in the
// registry viewer.
func (h *UserMemoryExtractionHook) reconcileAll(
	ctx context.Context,
	userID string,
	requestID string,
	inputs []reconcileInput,
) (results []ReconcileResult, batchUsed bool) {
	results = make([]ReconcileResult, len(inputs))

	// Try the batched path first if the reconciler supports it.
	if br, ok := h.reconciler.(BatchUserFactReconciler); ok && len(inputs) > 0 {
		candidates := make([]core.UserFact, len(inputs))
		neighbors := make([][]core.UserFact, len(inputs))
		for i, in := range inputs {
			candidates[i] = in.candidate
			neighbors[i] = in.neighbors
		}

		// Count how many candidates would actually trigger an LLM call (i.e.,
		// have at least one neighbor). If none, the batched path resolves
		// everything to ADD without any LLM invocation, and we DON'T need to
		// emit a "_batch" debug row at all.
		llmEligible := 0
		for _, n := range neighbors {
			if len(n) > 0 {
				llmEligible++
			}
		}

		batchStart := time.Now()
		// Closure scopes the deferred span end so a panic inside ReconcileBatch
		// still closes the span cleanly.
		var batchResults []ReconcileResult
		var err error
		var batchDuration time.Duration
		func() {
			batchCtx, endBatchSpan := telemetry.StartChildSpan(ctx, "user_memory.reconciliation.batch",
				attribute.String("user_id", userID),
				attribute.String("namespace", h.namespace),
				attribute.Int("candidate_count", len(inputs)),
				attribute.Int("llm_eligible_count", llmEligible),
			)
			defer endBatchSpan()
			batchCtx = telemetry.WithBaggage(batchCtx, "ai.purpose", "user_memory_reconciliation")
			batchCallCtx := h.deferLLMRecordingIfWeWillRecord(batchCtx)
			batchResults, err = br.ReconcileBatch(batchCallCtx, userID, h.namespace, candidates, neighbors)
			batchDuration = time.Since(batchStart)
			telemetry.SetSpanAttributes(batchCtx, attribute.Int64("duration_ms", batchDuration.Milliseconds()))
			if err == nil && len(batchResults) == len(inputs) {
				telemetry.SetSpanAttributes(batchCtx, attribute.Int("decisions_returned", len(batchResults)))
			}
			if err != nil {
				telemetry.RecordSpanError(batchCtx, err)
			}
		}()

		if err == nil && len(batchResults) == len(inputs) {
			// Success — record the single batched call (only if there was one).
			if llmEligible > 0 {
				rec := LLMInteraction{
					Type:       "user_memory_reconciliation_batch",
					HookPhase:  HookPhasePost,
					Category:   "llm",
					Timestamp:  batchStart,
					DurationMs: batchDuration.Milliseconds(),
					Prompt: fmt.Sprintf("[batch] %d candidates, %d eligible for LLM classification",
						len(inputs), llmEligible),
					Response: fmt.Sprintf("%d decisions returned", len(batchResults)),
					Success:  true,
				}
				// Every LLM-decided result shares the same Response pointer;
				// pick the first non-nil one for token attribution.
				for i := range batchResults {
					if batchResults[i].Response != nil {
						rec.Model = batchResults[i].Response.Model
						rec.Provider = batchResults[i].Response.Provider
						rec.PromptTokens = batchResults[i].Response.Usage.PromptTokens
						rec.CompletionTokens = batchResults[i].Response.Usage.CompletionTokens
						rec.TotalTokens = batchResults[i].Response.Usage.TotalTokens
						break
					}
				}
				h.recordDebugInteraction(ctx, requestID, rec)
				telemetry.Counter("user_memory.reconciliation.batch",
					"module", telemetry.ModuleOrchestration, "outcome", "success")
			}
			copy(results, batchResults)
			return results, true
		}

		// Batched path failed — record the failure and fall through.
		// Both the LLM-error and length-mismatch paths emit a "_batch"
		// debug row with Success=false so the registry viewer can anchor
		// the per-candidate fallback rows to the failed batch.
		if err != nil {
			h.recordDebugInteraction(ctx, requestID, LLMInteraction{
				Type:       "user_memory_reconciliation_batch",
				HookPhase:  HookPhasePost,
				Category:   "llm",
				Timestamp:  batchStart,
				DurationMs: batchDuration.Milliseconds(),
				Prompt:     fmt.Sprintf("[batch] %d candidates", len(inputs)),
				Success:    false,
				Error:      err.Error(),
			})
			telemetry.RecordSpanError(ctx, err)
			telemetry.Counter("user_memory.reconciliation.batch",
				"module", telemetry.ModuleOrchestration, "outcome", "fallback")
			if h.logger != nil {
				h.logger.WarnWithContext(ctx, "Batched user fact reconciliation failed, falling back to per-candidate", map[string]interface{}{
					"operation":  "user_memory_reconciliation_batch",
					"request_id": requestID,
					"user_id":    userID,
					"error":      err.Error(),
					"error_type": "batch_failure",
				})
			}
		} else {
			// Length mismatch (no error) — also fall back.
			mismatchErr := fmt.Sprintf("length mismatch: expected %d decisions, got %d", len(inputs), len(batchResults))
			h.recordDebugInteraction(ctx, requestID, LLMInteraction{
				Type:       "user_memory_reconciliation_batch",
				HookPhase:  HookPhasePost,
				Category:   "llm",
				Timestamp:  batchStart,
				DurationMs: batchDuration.Milliseconds(),
				Prompt:     fmt.Sprintf("[batch] %d candidates", len(inputs)),
				Success:    false,
				Error:      mismatchErr,
			})
			telemetry.Counter("user_memory.reconciliation.batch",
				"module", telemetry.ModuleOrchestration, "outcome", "fallback")
			if h.logger != nil {
				h.logger.WarnWithContext(ctx, "Batched reconciliation returned wrong count, falling back", map[string]interface{}{
					"operation":  "user_memory_reconciliation_batch",
					"request_id": requestID,
					"user_id":    userID,
					"expected":   len(inputs),
					"got":        len(batchResults),
					"error_type": "batch_length_mismatch",
				})
			}
		}
	}

	// Per-candidate fallback path (also used when the reconciler does not
	// implement the optional batch interface).
	reconcileCallCtx := h.deferLLMRecordingIfWeWillRecord(ctx)
	for i, in := range inputs {
		reconcileStart := time.Now()
		result, err := h.reconciler.Reconcile(reconcileCallCtx, userID, h.namespace, in.candidate, in.neighbors)
		reconcileDuration := time.Since(reconcileStart)
		if err != nil {
			h.recordDebugInteraction(ctx, requestID, LLMInteraction{
				Type:       "user_memory_reconciliation",
				HookPhase:  HookPhasePost,
				Category:   "llm",
				Timestamp:  reconcileStart,
				DurationMs: reconcileDuration.Milliseconds(),
				Prompt:     fmt.Sprintf("[candidate] %s\n[existing] %d facts", in.candidate.Content, len(in.neighbors)),
				Success:    false,
				Error:      err.Error(),
			})
			telemetry.RecordSpanError(ctx, err)
			if h.logger != nil {
				h.logger.WarnWithContext(ctx, "User fact reconciliation failed", map[string]interface{}{
					"operation":  "user_memory_reconciliation",
					"request_id": requestID,
					"user_id":    userID,
					"error":      err.Error(),
					"error_type": "parse_failure",
				})
			}
			results[i] = ReconcileResult{Skipped: true}
			continue
		}
		results[i] = result
	}
	return results, false
}

// ─── Helpers ─────────────────────────────────────────────────────────────────

// truncateUTF8 truncates a string to at most maxBytes bytes without cutting
// multi-byte UTF-8 characters in the middle. If the cut point falls within a
// multi-byte sequence, it backs up to the previous rune boundary.
func truncateUTF8(s string, maxBytes int) string {
	if len(s) <= maxBytes {
		return s
	}
	// Back up from maxBytes to the start of the last valid rune
	for maxBytes > 0 && maxBytes < len(s) {
		// Check if we're at a rune boundary (not a continuation byte 10xxxxxx)
		if s[maxBytes]&0xC0 != 0x80 {
			break
		}
		maxBytes--
	}
	return s[:maxBytes]
}

// cleanLLMJSONResponse strips markdown code fences from LLM JSON responses.
// Handles ```json, ```, and leading/trailing whitespace.
func cleanLLMJSONResponse(content string) string {
	content = strings.TrimSpace(content)
	content = strings.TrimPrefix(content, "```json")
	content = strings.TrimPrefix(content, "```")
	content = strings.TrimSuffix(content, "```")
	return strings.TrimSpace(content)
}

// ─── Default Implementations ─────────────────────────────────────────────────

type rawExtractedFact struct {
	Content    string  `json:"content"`
	Category   string  `json:"category"`
	Source     string  `json:"source"`
	Confidence float64 `json:"confidence"`
}

// DefaultUserFactExtractor uses an LLM to extract persistent user facts.
type DefaultUserFactExtractor struct {
	aiClient core.AIClient
	model    string
	logger   core.Logger
}

// DefaultUserFactPersistencePolicy conservatively drops clearly transient facts
// that should not persist across future conversations.
type DefaultUserFactPersistencePolicy struct{}

// NewDefaultUserFactExtractor creates the default LLM-based fact extractor.
func NewDefaultUserFactExtractor(aiClient core.AIClient, model string, logger core.Logger) *DefaultUserFactExtractor {
	return &DefaultUserFactExtractor{aiClient: aiClient, model: model, logger: logger}
}

// NewDefaultUserFactPersistencePolicy creates the default post-extraction
// persistence policy.
func NewDefaultUserFactPersistencePolicy() *DefaultUserFactPersistencePolicy {
	return &DefaultUserFactPersistencePolicy{}
}

// summaryPromptTemplate generates a one-sentence session summary.
// Tag names use user_memory_ prefix to avoid conflicts with reserved orchestration
// tags per docs/EFFECTIVE_PROMPTS_GUIDE.md §9.2.
const summaryPromptTemplate = `<user_memory_role>You are a session summarizer. You produce one-sentence summaries of user-assistant conversations.</user_memory_role>

<user_memory_summary_rules>
1. Summarize what was accomplished or discussed in one sentence
2. Use third person: "User planned..." or "User asked about..."
3. Be specific: include named entities, dates, quantities, and decisions made
4. Focus on outcomes and decisions, not process
5. Attribute named entities (places, people, dates, identifiers, quantities) to the user only when they appear in <user_memory_user_message> or the user explicitly accepted them. Entities that appear only in <user_memory_assistant_message> are assistant-suggested — describe them as such (e.g., "agent proposed X") rather than asserting the user chose them. The assistant may be echoing stored context the user has since moved past.
6. When the user's message uses a pronoun ("there", "that one", "same as before") with no named referent, write the summary around the user's stated action and omit the disputed entity rather than picking the assistant's guess.
</user_memory_summary_rules>

<user_memory_summary_example>
Input: User asked the agent to schedule a kickoff meeting with the finance team for next Tuesday at 2pm. Agent confirmed the meeting and sent invites.
Output: {"content": "User scheduled a kickoff meeting with the finance team for next Tuesday at 2pm; agent sent invites"}
</user_memory_summary_example>

<user_memory_summary_example_2>
Input:
  User: "Set it up for the team."
  Assistant: "I have configured the staging deployment pipeline for the platform team."
Output: {"content": "User asked the agent to set something up for the team; agent proposed configuring the staging deployment pipeline for the platform team"}
The user did not name what to set up; the staging pipeline appears only in the assistant response, so the summary attributes it to the agent rather than the user.
</user_memory_summary_example_2>

<user_memory_user_message>%s</user_memory_user_message>

<user_memory_assistant_message>%s</user_memory_assistant_message>

Return a single JSON object: {"content": "one sentence summary"}.
Return ONLY the JSON object.`

// extractionPromptTemplate is the persistence classification prompt.
// See Extraction Strategy §Persistence Classification in the proposal for full rationale.
// Tag names use user_memory_ prefix to avoid conflicts with reserved orchestration
// tags (<identity>, <instructions>, <example>, <user_request>) per
// docs/EFFECTIVE_PROMPTS_GUIDE.md §9.2.
const extractionPromptTemplate = `<user_memory_role>You are a user fact extractor. You store only long-lived personal information that will remain useful for personalization in future conversations, and you return it as structured JSON.</user_memory_role>

<user_memory_extraction_rules>
1. Extract only facts that will remain useful for personalization across future conversations: preferences, identity, constraints, relationships
2. Only extract facts stated or agreed to by the user in <user_memory_user_message>. Named entities (places, people, dates, quantities, identifiers) and decisions that appear only in <user_memory_assistant_message> count as user facts only after the user confirms them in a later turn (e.g., "yes, let's do that", "go ahead with that option") — until confirmed, treat them as assistant output. The assistant may be echoing stored context that the user has since moved away from.
3. When the user uses a pronoun or reference ("there", "that one", "same as before"), emit a fact only when the intended entity is named elsewhere in <user_memory_user_message>. Otherwise omit — entity resolution is the planner's job, not the extractor's.
4. Return no fact when information is one-time, current-task-only, or uncertain. Prefer omission over storing transient details.
5. Return no fact for one-time task parameters or one-off execution details such as "use the dark theme today" or "run it against the staging environment for this task"
6. Classify time-bounded plan-in-progress details such as active plans, projects, purchases, date windows, or open decisions ("I'm launching next week", "We are evaluating vendor A and vendor B", "starting the last Sunday of this month") as "context"
7. Use "constraint" only for durable hard requirements or limits such as dietary restrictions, accessibility needs, hard budget ceilings, regulatory rules, or must-avoid categories
8. Use "preference" for durable likes, soft defaults, and recurring choices such as preferred notification time, tool style, communication cadence, or standing decision heuristics
9. Split mixed statements into the smallest standalone facts possible — emit separate facts for identity, current context, preferences, and relationships
10. Use third person: "User prefers..."
11. Set confidence: 0.95 for explicit user statements, 0.70 for inferences from behavior
12. Return empty array [] when no persistent facts are present
</user_memory_extraction_rules>

<user_memory_extraction_example>
Input: "I'm vegetarian and the standup this week should be at 10am."
Output:
[
  {"content": "User is vegetarian", "category": "constraint", "source": "explicit", "confidence": 0.95}
]
The one-time standup scheduling produces no stored fact; only the durable dietary constraint remains.
</user_memory_extraction_example>

<user_memory_extraction_example_2>
Input: "We are launching the new analytics module next week, and I always prefer Monday kickoffs for cross-team work."
Output:
[
  {"content": "User prefers Monday kickoffs for cross-team work", "category": "preference", "source": "explicit", "confidence": 0.95},
  {"content": "User is launching the new analytics module next week", "category": "context", "source": "explicit", "confidence": 0.95}
]
The active launch is stored as context; the kickoff scheduling preference is durable.
</user_memory_extraction_example_2>

<user_memory_extraction_example_3>
Input:
  User: "I want to continue working on it tomorrow."
  Assistant: "Here is a checklist for finishing the authentication migration..."
Output:
[]
"it" is a pronoun with no antecedent in the user message. The authentication migration appears only in the assistant response, so it is not a user fact. Emit nothing.
</user_memory_extraction_example_3>

<user_memory_user_message>%s</user_memory_user_message>

<user_memory_assistant_message>%s</user_memory_assistant_message>

Return a JSON array of extracted facts. Each fact: {"content", "category", "source", "confidence"}.
Valid categories: identity, preference, constraint, relationship, context, summary.
Valid sources: explicit, inferred.
Return ONLY the JSON array.`

// ExtractFacts calls the LLM to extract persistent user facts from a conversation.
// Returns the extracted facts alongside the AIResponse metadata for debug recording.
func (e *DefaultUserFactExtractor) ExtractFacts(ctx context.Context, userRequest string, agentResponse string, corrections []string) (*ExtractResult, error) {
	if e.aiClient == nil {
		return &ExtractResult{}, nil // No AI client — skip extraction
	}

	prompt := fmt.Sprintf(extractionPromptTemplate, userRequest, agentResponse)

	opts := &core.AIOptions{
		MaxTokens:   7000,
		Temperature: 0.1, // Low temperature for structured extraction
	}
	if e.model != "" {
		opts.Model = e.model
	}

	resp, err := e.aiClient.GenerateResponse(ctx, prompt, opts)
	if err != nil {
		return nil, fmt.Errorf("user fact extraction LLM call failed: %w", err)
	}

	// Parse JSON response
	var extracted []rawExtractedFact
	content := cleanLLMJSONResponse(resp.Content)

	if err := json.Unmarshal([]byte(content), &extracted); err != nil {
		return nil, fmt.Errorf("user fact extraction: failed to parse LLM response as JSON: %w", err)
	}

	// Convert to core.UserFact
	now := time.Now()
	facts := make([]core.UserFact, 0, len(extracted))
	for _, ef := range extracted {
		fact, ok := normalizeExtractedFact(ef, now)
		if !ok {
			continue
		}
		facts = append(facts, fact)
	}
	return &ExtractResult{Facts: facts, Response: resp}, nil
}

func normalizeExtractedFact(ef rawExtractedFact, now time.Time) (core.UserFact, bool) {
	content := strings.Join(strings.Fields(strings.TrimSpace(ef.Content)), " ")
	if content == "" {
		return core.UserFact{}, false
	}

	category := strings.ToLower(strings.TrimSpace(ef.Category))
	switch category {
	case "identity", "preference", "constraint", "relationship", "context":
	case "summary":
		// Session summaries are produced by the dedicated summary step below.
		category = "context"
	default:
		category = "context"
	}

	if shouldDemoteFactToContext(category, content) {
		category = "context"
	}

	source := core.SourceExplicit
	switch strings.ToLower(strings.TrimSpace(ef.Source)) {
	case string(core.SourceExplicit):
		source = core.SourceExplicit
	case string(core.SourceInferred):
		source = core.SourceInferred
	}

	confidence := ef.Confidence
	if confidence <= 0 {
		confidence = 0.70
	}
	if confidence > 1 {
		confidence = 1
	}

	fact := core.UserFact{
		FactID:     uuid.New().String(),
		Content:    content,
		Category:   category,
		Source:     source,
		Confidence: confidence,
		CreatedAt:  now,
		UpdatedAt:  now,
	}
	return ensureFactLifetimeMetadata(fact), true
}

func (h *UserMemoryExtractionHook) expandCandidatesForStorage(ctx context.Context, requestID, userID string, candidates []core.UserFact) []core.UserFact {
	if len(candidates) == 0 {
		return candidates
	}

	expanded := make([]core.UserFact, 0, len(candidates))
	originalClauseCount := 0
	emittedClauseCount := 0
	durableCount := 0
	transientCount := 0
	splitOccurred := false

	for _, candidate := range candidates {
		parts := splitMixedExtractedFact(candidate, h.maxSplitClauses)
		originalClauseCount++
		emittedClauseCount += len(parts)
		if len(parts) > 1 {
			splitOccurred = true
		}
		for _, part := range compactDurableFactSet(parts) {
			switch core.EffectiveUserFactLifetime(part) {
			case core.UserFactLifetimeTransient:
				transientCount++
			case core.UserFactLifetimeDurable:
				durableCount++
			}
			expanded = append(expanded, part)
		}
	}

	if splitOccurred {
		telemetry.AddSpanEvent(ctx, "user_memory.compaction.split",
			attribute.String("request_id", requestID),
			attribute.String("user_id", userID),
			attribute.String("operation", "user_memory_compaction"),
			attribute.Int("original_clause_count", originalClauseCount),
			attribute.Int("emitted_clause_count", emittedClauseCount),
			attribute.Int("durable_count", durableCount),
			attribute.Int("transient_count", transientCount),
		)
	}

	return expanded
}

func (h *UserMemoryExtractionHook) finalizeReconciledFact(ctx context.Context, requestID, userID, targetFactID string, existing []core.UserFact, merged core.UserFact) core.UserFact {
	originalMerged := merged
	parts := compactDurableFactSet(splitMixedExtractedFact(merged, h.maxSplitClauses))
	if len(parts) == 0 {
		return ensureFactLifetimeMetadata(merged)
	}

	target := findFactByID(existing, targetFactID)
	merged = selectPreferredMergedFact(parts, target)
	if originalMerged.FactID != "" {
		merged.FactID = originalMerged.FactID
	}
	if merged.Namespace == "" {
		merged.Namespace = originalMerged.Namespace
	}
	if merged.Source == "" {
		merged.Source = originalMerged.Source
	}
	if merged.Confidence == 0 {
		merged.Confidence = originalMerged.Confidence
	}
	if merged.CreatedAt.IsZero() {
		merged.CreatedAt = originalMerged.CreatedAt
	}
	if merged.UpdatedAt.IsZero() {
		merged.UpdatedAt = originalMerged.UpdatedAt
	}
	if target != nil && isDurableCategory(target.Category) && isDurableCategory(merged.Category) {
		merged = preferCompactDurableMerge(*target, merged)
		telemetry.AddSpanEvent(ctx, "user_memory.compaction.merge",
			attribute.String("request_id", requestID),
			attribute.String("user_id", userID),
			attribute.String("operation", "user_memory_compaction"),
			attribute.Int("original_clause_count", len(parts)),
			attribute.Int("emitted_clause_count", 1),
			attribute.Int("durable_count", 1),
			attribute.Int("transient_count", max(0, len(parts)-1)),
		)
	}

	return ensureFactLifetimeMetadata(merged)
}

func (h *UserMemoryExtractionHook) markRedundantDurableWrites(ctx context.Context, requestID, userID string, inputs []reconcileInput, results []ReconcileResult) []bool {
	skip := make([]bool, len(results))
	finalized := make([]core.UserFact, len(results))

	for i := range results {
		if results[i].Skipped {
			continue
		}
		switch results[i].Operation {
		case "ADD", "UPDATE", "CONTRADICT":
			finalized[i] = ensureFactLifetimeMetadata(h.finalizeReconciledFact(ctx, requestID, userID, results[i].TargetFactID, inputs[i].neighbors, results[i].MergedFact))
			results[i].MergedFact = finalized[i]
		}
	}

	for i := range results {
		if skip[i] || finalized[i].FactID == "" || !isDurableCategory(finalized[i].Category) {
			continue
		}
		for j := i + 1; j < len(results); j++ {
			if skip[j] || finalized[j].FactID == "" || finalized[i].Category != finalized[j].Category || !isDurableCategory(finalized[j].Category) {
				continue
			}
			iKeys := normalizedClauseKeys(finalized[i].Content)
			jKeys := normalizedClauseKeys(finalized[j].Content)
			switch {
			case sameClauseSet(iKeys, jKeys):
				if shouldPreferLaterResult(results[i], results[j]) {
					skip[i] = true
				} else {
					skip[j] = true
				}
			case isSubset(iKeys, jKeys):
				skip[i] = true
			case isSubset(jKeys, iKeys):
				skip[j] = true
			}
		}
	}

	if count := countTrue(skip); count > 0 {
		telemetry.AddSpanEvent(ctx, "user_memory.compaction.deduped",
			attribute.String("request_id", requestID),
			attribute.String("user_id", userID),
			attribute.String("operation", "user_memory_compaction"),
			attribute.Int("emitted_clause_count", len(results)-count),
			attribute.Int("original_clause_count", len(results)),
			attribute.Int("durable_count", countDurableFacts(finalized)),
			attribute.Int("transient_count", countTransientFacts(finalized)),
		)
	}

	return skip
}

// Evaluate decides whether a candidate should continue to reconciliation/store.
func (p *DefaultUserFactPersistencePolicy) Evaluate(ctx context.Context, input UserFactPersistenceInput) (UserFactPersistenceDecision, error) {
	fact := input.Fact
	if shouldDropExtractedFact(strings.ToLower(fact.Content)) {
		return UserFactPersistenceDecision{Store: false}, nil
	}
	return UserFactPersistenceDecision{Fact: fact, Store: true}, nil
}

func shouldDemoteFactToContext(category string, content string) bool {
	if category == "context" || category == "identity" {
		return false
	}

	lower := strings.ToLower(content)
	if hasStrongTransientTaskSignal(lower) {
		return true
	}

	switch category {
	case "constraint":
		if hasDurableConstraintSignal(lower) {
			return false
		}
	case "preference":
		if hasDurablePreferenceSignal(lower) && !isOverstuffedFact(content) {
			return false
		}
	case "relationship":
		if hasDurableRelationshipSignal(lower) && !isOverstuffedFact(content) {
			return false
		}
	}

	return isClearlyTransientFact(lower, category, content)
}

func shouldDropExtractedFact(content string) bool {
	return isTransientFundingFact(content)
}

func isTransientFundingFact(content string) bool {
	transactionSignals := []string{
		"fund ", "pay ", "cover ", "selling ", "sell ", "using ", "use ", "redeem ",
		"redeeming ", "cash out ", "finance ", "financing ",
	}
	targetSignals := []string{
		"trip", "booking", "purchase", "order", "expense", "task", "request",
		"subscription", "invoice", "bill", "project", "renovation",
	}

	if !containsAnySignal(content, transactionSignals) {
		return false
	}
	if !containsAnySignal(content, targetSignals) {
		return false
	}
	if hasDurablePreferenceSignal(content) && !hasOneOffSpecificitySignal(content) && !hasTaskCouplingSignal(content) {
		return false
	}
	if !hasTaskCouplingSignal(content) && !hasOneOffSpecificitySignal(content) {
		return false
	}
	if !hasPlanInProgressSignal(content) && !hasExecutionScopedSignal(content) {
		return false
	}

	return true
}

func hasDurableConstraintSignal(content string) bool {
	signals := []string{
		"vegetarian", "vegan", "dietary", "diet", "egg", "eggs", "gelatin", "alcohol",
		"sober", "sobriety", "allerg", "allergic", "wheelchair", "accessible", "accessibility",
		"gluten", "halal", "kosher", "budget", "must avoid", "avoid ", "cannot eat", "do not consume",
	}
	for _, signal := range signals {
		if strings.Contains(content, signal) {
			return true
		}
	}
	return false
}

func hasDurablePreferenceSignal(content string) bool {
	signals := []string{
		"prefer ", "prefers ", "likes ", "enjoys ", "favorite ", "favourite ",
		"usually ", "typically ", "by default", "in general", "whenever possible",
		"tends to ", "default ",
	}
	return containsAnySignal(content, signals)
}

func hasDurableRelationshipSignal(content string) bool {
	signals := []string{
		"wife", "husband", "spouse", "partner", "son", "daughter", "child", "children",
		"mother", "father", "parent", "parents", "brother", "sister", "sibling",
		"family of", "lives with",
	}
	return containsAnySignal(content, signals)
}

func isClearlyTransientFact(content string, category string, rawContent string) bool {
	signalCount := 0
	if hasTemporalSpecificitySignal(content) {
		signalCount++
	}
	if hasTaskCouplingSignal(content) {
		signalCount++
	}
	if hasPlanInProgressSignal(content) {
		signalCount++
	}
	if hasExecutionScopedSignal(content) {
		signalCount++
	}

	compound := isOverstuffedFact(rawContent)

	switch category {
	case "constraint":
		return signalCount >= 2
	case "preference":
		if compound && signalCount >= 1 {
			return true
		}
		return signalCount >= 2 && !hasDurablePreferenceSignal(content)
	case "relationship":
		if compound && signalCount >= 1 {
			return true
		}
		return signalCount >= 2 && !hasDurableRelationshipSignal(content)
	default:
		return signalCount >= 1 || compound
	}
}

func hasStrongTransientTaskSignal(content string) bool {
	if !hasTaskCouplingSignal(content) {
		return false
	}
	return hasTemporalSpecificitySignal(content) || hasOneOffSpecificitySignal(content) || hasExecutionScopedSignal(content)
}

func hasTemporalSpecificitySignal(content string) bool {
	signals := []string{
		"this ", "current ", "currently", "upcoming ", "next week", "next month",
		"this month", "today", "tomorrow", "for now", "this year", "this year's",
	}
	return containsAnySignal(content, signals)
}

func hasOneOffSpecificitySignal(content string) bool {
	signals := []string{
		"this ", "current ", "upcoming ", "next week", "next month",
		"this month", "today", "tomorrow", "for now",
	}
	return containsAnySignal(content, signals)
}

func hasTaskCouplingSignal(content string) bool {
	signals := []string{
		"for this", "for the current", "for this request", "for this task", "for the task",
		"for this project", "for this purchase", "for this order",
	}
	return containsAnySignal(content, signals)
}

func hasPlanInProgressSignal(content string) bool {
	signals := []string{
		"planning", "plan ", "planned ", "starting", "considering", "evaluating",
		"choosing", "deciding", "itinerary",
	}
	return containsAnySignal(content, signals)
}

func hasExecutionScopedSignal(content string) bool {
	signals := []string{
		"book ", "buy ", "sell ", "redeem ", "pay ", "schedule ", "cancel ",
		"fund ", "finance ", "using ", "use ",
	}
	return containsAnySignal(content, signals)
}

func isOverstuffedFact(content string) bool {
	return len(content) > 220 || strings.Count(content, ",") >= 4 || strings.Count(strings.ToLower(content), " and ") >= 3
}

func containsAnySignal(content string, signals []string) bool {
	for _, signal := range signals {
		if strings.Contains(content, signal) {
			return true
		}
	}
	return false
}

func inferLifetime(category string, content string) string {
	lower := strings.ToLower(strings.TrimSpace(content))
	switch category {
	case "summary":
		return core.UserFactLifetimeSummary
	case "context":
		return core.UserFactLifetimeTransient
	case "identity":
		return core.UserFactLifetimeDurable
	case "preference", "constraint", "relationship":
		if shouldDemoteFactToContext(category, content) {
			return core.UserFactLifetimeTransient
		}
		return core.UserFactLifetimeDurable
	default:
		if hasStrongTransientTaskSignal(lower) || isClearlyTransientFact(lower, "context", content) {
			return core.UserFactLifetimeTransient
		}
		return core.UserFactLifetimeDurable
	}
}

func ensureFactLifetimeMetadata(fact core.UserFact) core.UserFact {
	if fact.Metadata == nil {
		fact.Metadata = map[string]string{}
	}
	lifetime := fact.Metadata[core.UserFactMetadataLifetimeKey]
	switch lifetime {
	case core.UserFactLifetimeDurable, core.UserFactLifetimeTransient, core.UserFactLifetimeSummary:
		fact.Metadata[core.UserFactMetadataLifetimeKey] = lifetime
		return fact
	default:
		lifetime = inferLifetime(fact.Category, fact.Content)
	}
	fact.Metadata[core.UserFactMetadataLifetimeKey] = lifetime
	return fact
}

func splitMixedExtractedFact(fact core.UserFact, maxClauses int) []core.UserFact {
	if !shouldSplitFact(fact) {
		return compactDurableFactSet([]core.UserFact{ensureFactLifetimeMetadata(fact)})
	}

	clauses := splitFactClauses(fact.Content, maxClauses)
	if len(clauses) <= 1 {
		return compactDurableFactSet([]core.UserFact{ensureFactLifetimeMetadata(fact)})
	}

	now := time.Now()
	split := make([]core.UserFact, 0, len(clauses))
	seen := map[string]struct{}{}
	for _, clause := range clauses {
		clauseCategory := fact.Category
		if fact.Category == "context" {
			clauseCategory = inferClauseCategory(clause)
		}
		normalized, ok := normalizeExtractedFact(rawExtractedFact{
			Content:    clause,
			Category:   clauseCategory,
			Source:     string(fact.Source),
			Confidence: fact.Confidence,
		}, now)
		if !ok {
			continue
		}
		normalized.Namespace = fact.Namespace
		normalized.UserID = fact.UserID
		key := normalized.Category + "|" + normalized.Content
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		split = append(split, normalized)
	}
	if len(split) == 0 {
		return compactDurableFactSet([]core.UserFact{ensureFactLifetimeMetadata(fact)})
	}
	return compactDurableFactSet(split)
}

func inferClauseCategory(content string) string {
	lower := strings.ToLower(strings.TrimSpace(content))
	switch {
	case hasDurableConstraintSignal(lower):
		return "constraint"
	case hasDurableRelationshipSignal(lower):
		return "relationship"
	case hasDurablePreferenceSignal(lower):
		return "preference"
	default:
		return "context"
	}
}

func splitFactClauses(content string, maxClauses int) []string {
	content = strings.TrimSpace(content)
	if content == "" {
		return nil
	}
	if maxClauses <= 0 {
		maxClauses = 3
	}

	clauses := []string{content}
	separators := []string{";", ". "}
	for _, separator := range separators {
		next := make([]string, 0, len(clauses))
		for _, clause := range clauses {
			parts := strings.Split(clause, separator)
			for _, part := range parts {
				part = strings.TrimSpace(strings.TrimSuffix(part, "."))
				if part != "" {
					next = append(next, part)
				}
			}
		}
		clauses = next
		if len(clauses) >= maxClauses {
			break
		}
	}

	if len(clauses) > maxClauses {
		clauses = clauses[:maxClauses]
	}
	return clauses
}

func compactDurableFactContent(content string) string {
	content = strings.Join(strings.Fields(strings.TrimSpace(content)), " ")
	if content == "" {
		return ""
	}

	clauses := splitFactClauses(content, 8)
	if len(clauses) == 0 {
		return content
	}

	seen := map[string]struct{}{}
	compacted := make([]string, 0, len(clauses))
	for _, clause := range clauses {
		clause = strings.Trim(strings.Join(strings.Fields(clause), " "), " ,.;")
		if clause == "" {
			continue
		}
		key := strings.ToLower(clause)
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		compacted = append(compacted, clause)
	}
	if len(compacted) == 0 {
		return content
	}
	return strings.Join(compacted, "; ")
}

func compactDurableFactSet(facts []core.UserFact) []core.UserFact {
	if len(facts) == 0 {
		return nil
	}

	compacted := make([]core.UserFact, 0, len(facts))
	seen := map[string]struct{}{}
	for _, fact := range facts {
		if isDurableCategory(fact.Category) && fact.Category != "identity" {
			fact.Content = compactDurableFactContent(fact.Content)
		}
		fact = ensureFactLifetimeMetadata(fact)
		key := fact.Category + "|" + strings.ToLower(strings.TrimSpace(fact.Content))
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		compacted = append(compacted, fact)
	}
	return compacted
}

func preferCompactDurableMerge(existing core.UserFact, merged core.UserFact) core.UserFact {
	existing = ensureFactLifetimeMetadata(existing)
	merged = ensureFactLifetimeMetadata(merged)
	if !isDurableCategory(existing.Category) || !isDurableCategory(merged.Category) || existing.Category != merged.Category {
		return merged
	}
	if merged.Category == "identity" {
		return merged
	}

	existing.Content = compactDurableFactContent(existing.Content)
	merged.Content = compactDurableFactContent(merged.Content)

	existingDisplay := durableDisplayClauses(existing.Content)
	mergedDisplay := durableDisplayClauses(merged.Content)
	existingClauses := normalizedClauseKeys(existing.Content)
	mergedClauses := normalizedClauseKeys(merged.Content)
	switch {
	case sameClauseSet(existingClauses, mergedClauses):
		if len(merged.Content) <= len(existing.Content) {
			return merged
		}
		return existing
	case isSubset(existingClauses, mergedClauses):
		return merged
	case isSubset(mergedClauses, existingClauses):
		return existing
	default:
		union := unionDisplayClauses(existingDisplay, mergedDisplay)
		if len(union) == 0 {
			return merged
		}
		merged.Content = strings.Join(union, "; ")
		return merged
	}
}

func shouldSplitFact(fact core.UserFact) bool {
	if fact.Category == "identity" {
		return false
	}
	if fact.Category != "preference" && fact.Category != "constraint" && fact.Category != "relationship" && fact.Category != "context" {
		return false
	}
	if len(fact.Content) > 180 || strings.Contains(fact.Content, ";") {
		return true
	}
	return clauseSeparatorCount(fact.Content) >= 2
}

func clauseSeparatorCount(content string) int {
	count := 0
	for _, sep := range []string{";", ". "} {
		count += strings.Count(content, sep)
	}
	return count
}

func isDurableCategory(category string) bool {
	switch category {
	case "identity", "preference", "constraint", "relationship":
		return true
	default:
		return false
	}
}

func durableDisplayClauses(content string) []string {
	clauses := splitFactClauses(content, 8)
	if len(clauses) == 0 {
		return nil
	}
	display := make([]string, 0, len(clauses))
	seen := map[string]struct{}{}
	for _, clause := range clauses {
		clause = strings.Trim(strings.Join(strings.Fields(clause), " "), " ,.;")
		if clause == "" {
			continue
		}
		key := normalizedClauseKey(clause)
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		display = append(display, clause)
	}
	return display
}

func normalizedClauseKey(clause string) string {
	return strings.ToLower(strings.Trim(strings.Join(strings.Fields(clause), " "), " ,.;"))
}

func normalizedClauseKeys(content string) []string {
	display := durableDisplayClauses(content)
	if len(display) == 0 {
		return nil
	}
	keys := make([]string, 0, len(display))
	for _, clause := range display {
		keys = append(keys, normalizedClauseKey(clause))
	}
	sort.Strings(keys)
	return keys
}

func sameClauseSet(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func isSubset(a, b []string) bool {
	if len(a) > len(b) {
		return false
	}
	bSet := make(map[string]struct{}, len(b))
	for _, clause := range b {
		bSet[clause] = struct{}{}
	}
	for _, clause := range a {
		if _, exists := bSet[clause]; !exists {
			return false
		}
	}
	return true
}

func unionDisplayClauses(a, b []string) []string {
	seen := map[string]struct{}{}
	union := make([]string, 0, len(a)+len(b))
	for _, clause := range append(a, b...) {
		key := normalizedClauseKey(clause)
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		union = append(union, clause)
	}
	return union
}

func shouldPreferLaterResult(current ReconcileResult, later ReconcileResult) bool {
	currentSpecific := current.TargetFactID != ""
	laterSpecific := later.TargetFactID != ""
	if currentSpecific != laterSpecific {
		return laterSpecific
	}
	return true
}

func countTrue(values []bool) int {
	count := 0
	for _, v := range values {
		if v {
			count++
		}
	}
	return count
}

func countDurableFacts(facts []core.UserFact) int {
	count := 0
	for _, fact := range facts {
		if isDurableCategory(fact.Category) {
			count++
		}
	}
	return count
}

func countTransientFacts(facts []core.UserFact) int {
	count := 0
	for _, fact := range facts {
		if core.EffectiveUserFactLifetime(fact) == core.UserFactLifetimeTransient {
			count++
		}
	}
	return count
}

func findFactByID(facts []core.UserFact, factID string) *core.UserFact {
	if factID == "" {
		return nil
	}
	for i := range facts {
		if facts[i].FactID == factID {
			return &facts[i]
		}
	}
	return nil
}

func selectPreferredMergedFact(parts []core.UserFact, target *core.UserFact) core.UserFact {
	if len(parts) == 0 {
		return core.UserFact{}
	}
	if target != nil && isDurableCategory(target.Category) {
		matchingDurable := make([]core.UserFact, 0, len(parts))
		for _, part := range parts {
			if part.Category == target.Category && isDurableCategory(part.Category) {
				matchingDurable = append(matchingDurable, part)
			}
		}
		if len(matchingDurable) > 0 {
			preferred := matchingDurable[0]
			if len(matchingDurable) > 1 {
				contents := make([]string, 0, len(matchingDurable))
				for _, part := range matchingDurable {
					contents = append(contents, part.Content)
				}
				preferred.Content = compactDurableFactContent(strings.Join(contents, "; "))
			}
			return preferred
		}
		for _, part := range parts {
			if isDurableCategory(part.Category) {
				return part
			}
		}
	}
	return parts[0]
}

// ─── LLM Reconciler ──────────────────────────────────────────────────────────

// LLMUserFactReconciler uses semantic search + LLM classification to resolve
// conflicts between new and existing user facts.
//
// The threshold field controls the minimum number of existing facts required to
// trigger an LLM reconciliation call. When Recall returns facts, they are already
// filtered by the vector backend's similarity scoring. The threshold is reserved
// for future use when Recall returns scored results that can be filtered client-side.
// Currently, if Recall returns any facts, the LLM is called. If Recall returns none
// (no similar facts), the reconciler short-circuits to ADD without an LLM call.
type LLMUserFactReconciler struct {
	userMemory core.UserMemory
	aiClient   core.AIClient
	model      string
	threshold  float64 // reserved for future client-side similarity filtering
	logger     core.Logger
}

// NewLLMUserFactReconciler creates the default LLM-based reconciler.
func NewLLMUserFactReconciler(
	userMem core.UserMemory,
	aiClient core.AIClient,
	model string,
	threshold float64,
	logger core.Logger,
) *LLMUserFactReconciler {
	return &LLMUserFactReconciler{
		userMemory: userMem,
		aiClient:   aiClient,
		model:      model,
		threshold:  threshold,
		logger:     logger,
	}
}

// reconciliationPromptTemplate classifies fact relationships.
// See Fact Reconciliation Pipeline §LLM Reconciliation Prompt in the proposal.
const reconciliationPromptTemplate = `<user_memory_role>You are a user memory reconciliation system. You classify the relationship between a new fact and existing facts about a user.</user_memory_role>

<user_memory_reconciliation_rules>
1. Compare the new fact against each existing fact
2. Return exactly one operation:
   - ADD: new fact has no semantic overlap with any existing fact
   - DUPLICATE: an existing fact already captures the same information
   - UPDATE: new fact adds detail to an existing fact (e.g., "likes cricket" → "likes cricket with friends on weekends")
   - CONTRADICT: new fact supersedes an existing fact (e.g., "prefers window seats" → "prefers aisle seats")
3. For UPDATE, provide the merged content combining old and new
4. For DUPLICATE/CONTRADICT, provide the target_fact_id of the existing fact
</user_memory_reconciliation_rules>

<user_memory_existing_facts>
%s</user_memory_existing_facts>

<user_memory_new_fact>%s (source: %s)</user_memory_new_fact>

Return a single JSON object: {"operation", "target_fact_id", "merged_content", "reasoning"}.
Return ONLY the JSON object.`

// Reconcile determines how a candidate fact relates to existing facts.
// If no existing facts → returns ADD without calling the LLM (cost optimization).
func (r *LLMUserFactReconciler) Reconcile(ctx context.Context, userID string, namespace string, candidate core.UserFact, existing []core.UserFact) (ReconcileResult, error) {
	// If no existing facts → ADD (skip LLM call)
	if len(existing) == 0 {
		return ReconcileResult{
			Operation:  "ADD",
			MergedFact: candidate,
		}, nil
	}

	if r.aiClient == nil {
		// No AI client — default to ADD
		return ReconcileResult{
			Operation:  "ADD",
			MergedFact: candidate,
		}, nil
	}

	// Build existing facts text for the prompt
	var existingText strings.Builder
	for _, fact := range existing {
		fmt.Fprintf(&existingText, "- [%s] %s (source: %s, confidence: %.2f)\n",
			fact.FactID, fact.Content, fact.Source, fact.Confidence)
	}

	prompt := fmt.Sprintf(reconciliationPromptTemplate,
		existingText.String(),
		candidate.Content,
		candidate.Source,
	)

	opts := &core.AIOptions{
		MaxTokens:   3500,
		Temperature: 0.1,
	}
	if r.model != "" {
		opts.Model = r.model
	}

	resp, err := r.aiClient.GenerateResponse(ctx, prompt, opts)
	if err != nil {
		return ReconcileResult{}, fmt.Errorf("reconciliation LLM call failed: %w", err)
	}

	// Parse JSON response
	type llmResult struct {
		Operation     string `json:"operation"`
		TargetFactID  string `json:"target_fact_id"`
		MergedContent string `json:"merged_content"`
		Reasoning     string `json:"reasoning"`
	}

	content := cleanLLMJSONResponse(resp.Content)

	var result llmResult
	if err := json.Unmarshal([]byte(content), &result); err != nil {
		return ReconcileResult{}, fmt.Errorf("reconciliation: failed to parse LLM response: %w", err)
	}

	// Build the final fact based on operation
	mergedFact := candidate
	switch result.Operation {
	case "UPDATE":
		if result.MergedContent != "" {
			mergedFact.Content = result.MergedContent
		}
		mergedFact.FactID = result.TargetFactID // Reuse existing fact's ID for upsert
		mergedFact.UpdatedAt = time.Now()
	case "CONTRADICT":
		mergedFact.FactID = uuid.New().String() // New fact gets new ID
	case "DUPLICATE":
		// Return existing fact's ID so caller can optionally touch its UpdatedAt
		if result.TargetFactID != "" {
			mergedFact.FactID = result.TargetFactID
		}
	case "ADD":
		mergedFact.FactID = uuid.New().String()
	default:
		// Unknown operation — treat as ADD
		result.Operation = "ADD"
		mergedFact.FactID = uuid.New().String()
	}

	return ReconcileResult{
		Operation:    result.Operation,
		TargetFactID: result.TargetFactID,
		MergedFact:   mergedFact,
		Response:     resp,
	}, nil
}

// Compile-time check: LLMUserFactReconciler implements the optional batch interface.
var _ BatchUserFactReconciler = (*LLMUserFactReconciler)(nil)

// batchedReconciliationPromptTemplate classifies multiple candidates in one LLM call.
// Each candidate is reconciled against ITS OWN neighbor list (independent decisions),
// and the model returns a JSON array with one decision per candidate, in order.
//
// Rationale: pre-batching, reconciliation issued one LLM call per candidate
// (~7 calls × ~700 tokens per turn was typical). Batching collapses that into
// a single call with negligible accuracy impact, since the per-candidate
// decisions are independent.
//
// Tag names use user_memory_ prefix to avoid conflicts with reserved
// orchestration tags per docs/EFFECTIVE_PROMPTS_GUIDE.md §9.2.
const batchedReconciliationPromptTemplate = `<user_memory_role>You are a user memory reconciliation system. You classify the relationship between each new candidate fact and the existing facts that are similar to it.</user_memory_role>

<user_memory_reconciliation_rules>
1. For EACH numbered candidate, decide ONE operation against ITS OWN neighbor list:
   - ADD: no semantic overlap with any of that candidate's neighbors
   - DUPLICATE: a neighbor already captures the same information
   - UPDATE: candidate adds detail to one of its neighbors (e.g., "likes cricket" → "likes cricket with friends on weekends")
   - CONTRADICT: candidate supersedes one of its neighbors (e.g., "prefers window seats" → "prefers aisle seats")
2. For UPDATE, provide merged_content combining old and new
3. For DUPLICATE/CONTRADICT/UPDATE, provide target_fact_id of the matched neighbor
4. Each candidate is independent — never compare candidates against each other
5. Return one decision per candidate, in the same order as the input
</user_memory_reconciliation_rules>

<user_memory_candidates>
%s</user_memory_candidates>

Return a JSON array with exactly %d objects, one per candidate, in the same order.
Each object: {"candidate": <1-based index>, "operation": "ADD|DUPLICATE|UPDATE|CONTRADICT", "target_fact_id": "...", "merged_content": "...", "reasoning": "..."}.
Return ONLY the JSON array.`

// ReconcileBatch classifies multiple candidates in a single LLM call.
//
// Behavior:
//   - candidates and neighbors must be aligned (len equal, neighbors[i] for candidates[i]).
//   - Candidates whose neighbors slice is empty are pre-resolved to ADD without
//     contributing to the LLM prompt. If every candidate has empty neighbors,
//     no LLM call is made at all.
//   - On any error or parse mismatch, returns the error so the caller can fall
//     back to per-candidate Reconcile. The returned slice on success has length
//     equal to len(candidates) and each entry corresponds to candidates[i].
//
// Every LLM-decided ReconcileResult shares the same *core.AIResponse pointer
// from the batched call, so debug recording can attribute model/usage metadata
// to any of them. Pre-resolved ADD entries (no neighbors) leave Response nil.
func (r *LLMUserFactReconciler) ReconcileBatch(
	ctx context.Context,
	userID string,
	namespace string,
	candidates []core.UserFact,
	neighbors [][]core.UserFact,
) ([]ReconcileResult, error) {
	if len(candidates) != len(neighbors) {
		return nil, fmt.Errorf("reconcile batch: candidates (%d) and neighbors (%d) length mismatch", len(candidates), len(neighbors))
	}
	if len(candidates) == 0 {
		return []ReconcileResult{}, nil
	}

	results := make([]ReconcileResult, len(candidates))

	// Pre-resolve candidates with no neighbors to ADD (no LLM call needed,
	// matches the per-candidate Reconcile short-circuit).
	pendingIdxs := make([]int, 0, len(candidates))
	for i, candidate := range candidates {
		if len(neighbors[i]) == 0 {
			results[i] = ReconcileResult{
				Operation:  "ADD",
				MergedFact: candidate,
			}
			continue
		}
		pendingIdxs = append(pendingIdxs, i)
	}

	// All ADD — nothing to send to the LLM.
	if len(pendingIdxs) == 0 {
		return results, nil
	}

	// If no AI client, default every pending to ADD (matches per-candidate behavior).
	if r.aiClient == nil {
		for _, origIdx := range pendingIdxs {
			results[origIdx] = ReconcileResult{
				Operation:  "ADD",
				MergedFact: candidates[origIdx],
			}
		}
		return results, nil
	}

	// Build the candidates body. Each candidate block is self-contained:
	// its number, content/source, and its own neighbors.
	var body strings.Builder
	for batchIdx, origIdx := range pendingIdxs {
		candidate := candidates[origIdx]
		fmt.Fprintf(&body, "%d. New fact: %q (source: %s)\n", batchIdx+1, candidate.Content, candidate.Source)
		body.WriteString("   Neighbors:\n")
		for _, n := range neighbors[origIdx] {
			fmt.Fprintf(&body, "   - [id=%s] %s (source: %s, confidence: %.2f)\n",
				n.FactID, n.Content, n.Source, n.Confidence)
		}
		body.WriteString("\n")
	}

	prompt := fmt.Sprintf(batchedReconciliationPromptTemplate, body.String(), len(pendingIdxs))

	// Token budget per batched candidate. Each batched decision has the same
	// JSON shape as a single Reconcile call (operation + target_fact_id +
	// merged_content + reasoning), and the per-candidate Reconcile path
	// budgets 3500 tokens. We give each batched candidate similar headroom
	// (default 2800) so UPDATE merged_content doesn't get truncated on longer
	// turns, with a floor of 3500 for tiny batches. Tunable via env var so
	// operators can dial it down for cost or up for verbose merges.
	perCandidateTokens := 2800
	if v, _ := strconv.Atoi(os.Getenv("TRUVAG3_USER_MEMORY_BATCH_TOKENS_PER_CANDIDATE")); v > 0 {
		perCandidateTokens = v
	}
	opts := &core.AIOptions{
		MaxTokens:   max(3500, perCandidateTokens*len(pendingIdxs)),
		Temperature: 0.1,
	}
	if r.model != "" {
		opts.Model = r.model
	}

	resp, err := r.aiClient.GenerateResponse(ctx, prompt, opts)
	if err != nil {
		return nil, fmt.Errorf("batched reconciliation LLM call failed: %w", err)
	}

	type llmDecision struct {
		Candidate     int    `json:"candidate"`
		Operation     string `json:"operation"`
		TargetFactID  string `json:"target_fact_id"`
		MergedContent string `json:"merged_content"`
		Reasoning     string `json:"reasoning"`
	}

	content := cleanLLMJSONResponse(resp.Content)

	var decisions []llmDecision
	if err := json.Unmarshal([]byte(content), &decisions); err != nil {
		return nil, fmt.Errorf("batched reconciliation: failed to parse LLM response as JSON array: %w", err)
	}

	if len(decisions) != len(pendingIdxs) {
		return nil, fmt.Errorf("batched reconciliation: expected %d decisions, got %d", len(pendingIdxs), len(decisions))
	}

	// Apply decisions in batch order. Decisions are expected in the same
	// order as pendingIdxs; we ignore the model-supplied "candidate" index
	// and rely on positional ordering for safety.
	for batchIdx, d := range decisions {
		origIdx := pendingIdxs[batchIdx]
		candidate := candidates[origIdx]
		mergedFact := candidate
		switch d.Operation {
		case "UPDATE":
			if d.MergedContent != "" {
				mergedFact.Content = d.MergedContent
			}
			mergedFact.FactID = d.TargetFactID
			mergedFact.UpdatedAt = time.Now()
		case "CONTRADICT":
			mergedFact.FactID = uuid.New().String()
		case "DUPLICATE":
			if d.TargetFactID != "" {
				mergedFact.FactID = d.TargetFactID
			}
		case "ADD":
			mergedFact.FactID = uuid.New().String()
		default:
			d.Operation = "ADD"
			mergedFact.FactID = uuid.New().String()
		}
		results[origIdx] = ReconcileResult{
			Operation:    d.Operation,
			TargetFactID: d.TargetFactID,
			MergedFact:   mergedFact,
			Response:     resp, // shared metadata across the batch
		}
	}

	return results, nil
}
