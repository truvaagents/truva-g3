package orchestration

import (
	"context"
	"fmt"
	"io"
	"os"
	"strconv"

	"github.com/truvaagents/truva-g3/core"
)

// ─── Interfaces for behavioural plugs ────────────────────────────────────────

// UserFactExtractor extracts candidate user facts from a conversation.
// Default: DefaultUserFactExtractor (LLM-based persistence classification).
type UserFactExtractor interface {
	ExtractFacts(ctx context.Context, userRequest string, agentResponse string, corrections []string) (*ExtractResult, error)
}

// ExtractResult wraps extracted facts with optional LLM metadata.
// Non-LLM extractors (e.g., regex-based) return Response: nil.
type ExtractResult struct {
	Facts    []core.UserFact  // Extracted candidate facts
	Response *core.AIResponse // LLM response metadata (nil for non-LLM extractors)
}

// UserFactPersistenceInput is the boundary object for post-extraction
// persistence decisions. It lets developers override store/drop behavior
// without replacing extraction or reconciliation.
type UserFactPersistenceInput struct {
	UserID        string
	Namespace     string
	UserRequest   string
	AgentResponse string
	Fact          core.UserFact
}

// UserFactPersistenceDecision tells the extraction hook whether a candidate
// should proceed to reconciliation/storage, and allows the policy to rewrite
// the candidate before that happens.
type UserFactPersistenceDecision struct {
	Fact  core.UserFact
	Store bool
}

// UserFactPersistencePolicy decides whether an extracted fact should be stored.
// Default: DefaultUserFactPersistencePolicy (conservative drop of transient facts).
type UserFactPersistencePolicy interface {
	Evaluate(ctx context.Context, input UserFactPersistenceInput) (UserFactPersistenceDecision, error)
}

// UserFactReconciler resolves conflicts between new and existing user facts.
// Default: LLMUserFactReconciler (embed → search → LLM classify).
type UserFactReconciler interface {
	Reconcile(ctx context.Context, userID string, namespace string, candidate core.UserFact, existing []core.UserFact) (ReconcileResult, error)
}

// BatchUserFactReconciler is an optional extension to UserFactReconciler.
// Reconcilers that implement it can classify all candidates from a single
// turn in a single LLM call, amortizing per-call overhead and dramatically
// reducing post-execution latency on turns that produce multiple candidates.
//
// The extraction hook detects this interface via a type assertion and uses
// the batched path when available. Reconcilers that do not implement it
// continue to work via the per-candidate Reconcile loop.
//
// Contract:
//   - candidates[i] is reconciled against neighbors[i] (same length, aligned).
//   - The returned slice MUST have len == len(candidates), with results[i]
//     applying to candidates[i]. A length mismatch indicates a parse failure
//     and the caller will fall back to per-candidate Reconcile.
//   - Implementations are free to short-circuit (e.g., skip the LLM call
//     entirely when every candidate has zero neighbors).
type BatchUserFactReconciler interface {
	UserFactReconciler
	ReconcileBatch(ctx context.Context, userID string, namespace string, candidates []core.UserFact, neighbors [][]core.UserFact) ([]ReconcileResult, error)
}

// ReconcileResult tells the extraction hook what to do with a candidate fact.
type ReconcileResult struct {
	Operation    string           // "ADD", "UPDATE", "DUPLICATE", "CONTRADICT"
	TargetFactID string           // existing fact ID for UPDATE/DUPLICATE/CONTRADICT
	MergedFact   core.UserFact    // final fact to store (for ADD and UPDATE)
	Response     *core.AIResponse // LLM response metadata (nil when no LLM call — e.g., ADD with no existing facts)
	Skipped      bool             // true when reconciliation failed for this candidate and the caller should ignore it
}

// ─── Builder ─────────────────────────────────────────────────────────────────

// userMemoryHookConfig holds behavioural overrides for BuildUserMemoryHooks.
//
// Defaults: asynchronousExtraction=true so AfterSynthesis does not delay the
// user's response — this is the chat-agent UX the Layer 1 preset is opinionated
// about. Opt out via WithSynchronousExtraction() when tests or downstream steps
// need to observe extraction completion before continuing. Layer 3
// (NewUserMemoryExtractionHook) is synchronous by default — see its godoc.
type userMemoryHookConfig struct {
	extractor             UserFactExtractor
	reconciler            UserFactReconciler
	persistencePolicy     UserFactPersistencePolicy
	retrievalWeights      core.RetrievalWeights
	synchronousExtraction bool
	userIDNormalizer      UserIDNormalizer
}

// BuildUserMemoryHooksOption configures behavioural plugs.
// Numeric tuning uses env vars (see Configuration Split section in proposal).
type BuildUserMemoryHooksOption func(*userMemoryHookConfig) error

func defaultUserFactExtractor(aiClient core.AIClient, logger core.Logger) UserFactExtractor {
	model := os.Getenv("TRUVAG3_USER_MEMORY_EXTRACTION_MODEL")
	return NewDefaultUserFactExtractor(aiClient, model, logger)
}

func defaultUserFactReconciler(userMem core.UserMemory, aiClient core.AIClient, logger core.Logger) UserFactReconciler {
	model := os.Getenv("TRUVAG3_USER_MEMORY_RECONCILIATION_MODEL")
	if model == "" {
		model = os.Getenv("TRUVAG3_USER_MEMORY_EXTRACTION_MODEL")
	}
	threshold := 0.75
	if v, _ := strconv.ParseFloat(os.Getenv("TRUVAG3_USER_MEMORY_RECONCILIATION_THRESHOLD"), 64); v > 0 {
		threshold = v
	}
	return NewLLMUserFactReconciler(userMem, aiClient, model, threshold, logger)
}

// WithUserFactExtractor overrides the default extraction prompt/logic.
func WithUserFactExtractor(e UserFactExtractor) BuildUserMemoryHooksOption {
	return func(c *userMemoryHookConfig) error {
		if e == nil {
			return fmt.Errorf("user fact extractor cannot be nil")
		}
		c.extractor = e
		return nil
	}
}

// WithUserFactReconciler overrides the default dedup/contradiction strategy.
func WithUserFactReconciler(r UserFactReconciler) BuildUserMemoryHooksOption {
	return func(c *userMemoryHookConfig) error {
		if r == nil {
			return fmt.Errorf("user fact reconciler cannot be nil")
		}
		c.reconciler = r
		return nil
	}
}

// WithUserFactPersistencePolicy overrides the default post-extraction
// store/drop decision without requiring a custom extractor or backend.
func WithUserFactPersistencePolicy(p UserFactPersistencePolicy) BuildUserMemoryHooksOption {
	return func(c *userMemoryHookConfig) error {
		if p == nil {
			return fmt.Errorf("user fact persistence policy cannot be nil")
		}
		c.persistencePolicy = p
		return nil
	}
}

// WithUserIDNormalizer canonicalizes user_id before it is used as the user
// memory isolation key. The same normalizer is applied to both recall
// (enrichment) and storage (extraction), so reads and writes always agree.
//
// Default (no option) is identity — case-sensitive, verbatim isolation. Pass
// NormalizeUserIDLowercaseTrim to fold casing/whitespace variants of the same
// id into one bucket. Normalization changes the identity key, so it is a
// deliberate code-level policy rather than a per-deployment env toggle.
func WithUserIDNormalizer(n UserIDNormalizer) BuildUserMemoryHooksOption {
	return func(c *userMemoryHookConfig) error {
		if n == nil {
			return fmt.Errorf("user id normalizer cannot be nil")
		}
		c.userIDNormalizer = n
		return nil
	}
}

// WithUserMemoryRetrievalWeights tunes the scoring weights for Recall.
func WithUserMemoryRetrievalWeights(w core.RetrievalWeights) BuildUserMemoryHooksOption {
	return func(c *userMemoryHookConfig) error {
		c.retrievalWeights = w
		return nil
	}
}

// WithSynchronousExtraction makes AfterSynthesis block until extraction,
// reconciliation, and storage complete — opting out of the Layer 1 default
// (asynchronous) for this preset.
//
// Default (async):
//   - AfterSynthesis spawns a detached goroutine and returns immediately.
//   - The goroutine uses context.WithoutCancel on the request context: trace
//     parent and baggage are preserved (spans still correlate in Jaeger) but
//     the request's cancellation/deadline is dropped so extraction outlives
//     the HTTP request.
//   - Errors are logged but never surface to the caller (fail-open).
//   - The io.Closer returned by BuildUserMemoryHooks drains in-flight work.
//
// Use synchronous mode when:
//   - Tests need to assert on stored facts after the response returns.
//   - A subsequent pipeline step depends on freshly-extracted facts being queryable.
//   - You need extraction errors to surface synchronously.
//
// This option controls the Layer 1 preset only. Layer 3 construction via
// NewUserMemoryExtractionHook is synchronous by default — opt in with
// WithAsynchronousUserExtraction().
func WithSynchronousExtraction() BuildUserMemoryHooksOption {
	return func(c *userMemoryHookConfig) error {
		c.synchronousExtraction = true
		return nil
	}
}

// BuildUserMemoryHooks creates the user memory pipeline hooks from a deps struct.
// Returns hooks in correct order: [enrichment, extraction].
//
// Numeric tuning is read from env vars:
//
//	TRUVAG3_USER_MEMORY_MAX_FACTS_IN_PROMPT (default: 15)
//	TRUVAG3_USER_MEMORY_MAX_IDENTITY_FACTS (default: 5)
//	TRUVAG3_USER_MEMORY_MAX_SUMMARY_FACTS (default: 3)
//	TRUVAG3_USER_MEMORY_MAX_UNIVERSAL_FACTS (default: 5)
//	TRUVAG3_USER_MEMORY_MIN_CONFIDENCE (default: 0.3)
//	TRUVAG3_USER_MEMORY_RECONCILIATION_THRESHOLD (default: 0.75)
//	TRUVAG3_USER_MEMORY_EXTRACTION_MODEL (default: "")
//	TRUVAG3_USER_MEMORY_RECONCILIATION_MODEL (default: extraction model)
//	TRUVAG3_USER_MEMORY_SUMMARY_MAX_RESPONSE_LEN (default: 500)
//
// Behavioural plugs are options:
//
//	WithUserFactExtractor — custom extraction logic
//	WithUserFactReconciler — custom dedup/contradiction strategy
//	WithUserFactPersistencePolicy — custom post-extraction store/drop policy
//	WithUserMemoryRetrievalWeights — scoring weight tuning
//	WithSynchronousExtraction — block AfterSynthesis on extraction completion
//	                             (default is async in this Layer 1 preset)
//
// Separate from BuildMemoryHooks — user memory is a composable primitive,
// not bundled into the shared memory pipeline.
//
// Returns ([]core.PipelineHook, io.Closer). The closer drains any in-flight
// asynchronous extraction goroutines at shutdown. Call closer.Close() from
// the agent's shutdown path — e.g., after the framework's Runnable drain and
// before process exit. The closer is always non-nil (no-op when deps are
// missing or when synchronous mode is selected) so defer closer.Close() is
// always safe.
func BuildUserMemoryHooks(
	deps *core.UserMemoryDeps,
	aiClient core.AIClient,
	logger core.Logger,
	opts ...BuildUserMemoryHooksOption,
) ([]core.PipelineHook, io.Closer) {
	if deps == nil || deps.UserMemory == nil {
		return nil, noopCloser{}
	}

	// Apply behavioural options
	cfg := &userMemoryHookConfig{
		retrievalWeights: core.RetrievalWeights{Recency: 0.20, Relevance: 0.50, Importance: 0.30},
	}
	for _, opt := range opts {
		if err := opt(cfg); err != nil {
			if logger != nil {
				logger.Error("invalid user memory hook option", map[string]interface{}{
					"operation": "build_user_memory_hooks",
					"error":     err.Error(),
				})
			}
		}
	}

	// Default extractor: LLM-based persistence classification
	if cfg.extractor == nil {
		cfg.extractor = defaultUserFactExtractor(aiClient, logger)
	}

	// Default reconciler: embed → search → LLM classify
	if cfg.reconciler == nil {
		cfg.reconciler = defaultUserFactReconciler(deps.UserMemory, aiClient, logger)
	}

	if cfg.persistencePolicy == nil {
		cfg.persistencePolicy = NewDefaultUserFactPersistencePolicy()
	}

	// Pass the same normalizer to both hooks so recall and storage stay in
	// lockstep. When nil, each hook independently defaults to identity, which is
	// still identical across the two.
	enrichHook := NewUserMemoryEnrichmentHook(deps.UserMemory, deps.Namespace, logger,
		WithUserMemoryEnrichmentUserIDNormalizer(cfg.userIDNormalizer),
	)
	extractOpts := []UserMemoryExtractionOption{
		WithUserExtractionPersistencePolicy(cfg.persistencePolicy),
		WithUserExtractionUserIDNormalizer(cfg.userIDNormalizer),
	}
	// Layer 1 preset defaults to async. Developers can opt out via
	// WithSynchronousExtraction() — if the flag is not set, enable async.
	if !cfg.synchronousExtraction {
		extractOpts = append(extractOpts, WithAsynchronousUserExtraction())
	}
	extractHook := NewUserMemoryExtractionHook(deps.UserMemory, deps.Embedder, aiClient, deps.Namespace, logger,
		cfg.extractor, cfg.reconciler, extractOpts...,
	)

	// extractHook satisfies io.Closer — return it directly as the lifecycle
	// surface. Close() drains in-flight async extractions; it's a no-op in sync
	// mode (WaitGroups are zero) so defer closer.Close() is always safe.
	return []core.PipelineHook{enrichHook, extractHook}, extractHook
}

// noopCloser implements io.Closer with a no-op Close() — returned from
// BuildUserMemoryHooks when deps are missing so the caller can always
// defer closer.Close() without a nil-check.
type noopCloser struct{}

func (noopCloser) Close() error { return nil }
