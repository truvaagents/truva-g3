package orchestration

import (
	"context"
	"errors"
	"testing"

	"github.com/truvaagents/truva-g3/core"
	"github.com/truvaagents/truva-g3/telemetry"
)

type stubConversationHistoryPreparer struct {
	lastConversationID string
	lastTurns          []core.ConversationTurn
	lastText           string
	prepared           string
}

func (s *stubConversationHistoryPreparer) PrepareFromText(ctx context.Context, sessionKey string, formatted string) (string, HistoryPreparationResult, error) {
	s.lastConversationID = sessionKey
	s.lastText = formatted
	return s.prepared, HistoryPreparationResult{Path: "metadata_text"}, nil
}

func (s *stubConversationHistoryPreparer) PrepareFromTurns(ctx context.Context, sessionKey string, turns []core.ConversationTurn) (string, HistoryPreparationResult, error) {
	s.lastConversationID = sessionKey
	s.lastTurns = turns
	return s.prepared, HistoryPreparationResult{Path: "metadata_turns"}, nil
}

// --- Test hook implementations ---

// allStagesHook implements all four hook stages for comprehensive testing.
type allStagesHook struct {
	name            string
	beforePlanErr   error
	shortCircuit    *core.PipelineShortCircuit
	afterPlanErr    error
	afterExecErr    error
	afterSynthErr   error
	beforePlanCalls int
	afterPlanCalls  int
	afterExecCalls  int
	afterSynthCalls int
	// mutation tracking
	planMutation     interface{}
	responseMutation string
}

func (h *allStagesHook) Name() string { return h.name }

func (h *allStagesHook) BeforePlanning(ctx context.Context, pctx *core.PipelineContext) (*core.PipelineShortCircuit, error) {
	h.beforePlanCalls++
	if h.beforePlanErr != nil {
		return nil, h.beforePlanErr
	}
	return h.shortCircuit, nil
}

func (h *allStagesHook) AfterPlanning(ctx context.Context, pctx *core.PipelineContext, plan interface{}) (interface{}, error) {
	h.afterPlanCalls++
	if h.afterPlanErr != nil {
		return plan, h.afterPlanErr
	}
	if h.planMutation != nil {
		return h.planMutation, nil
	}
	return plan, nil
}

func (h *allStagesHook) AfterExecution(ctx context.Context, pctx *core.PipelineContext, results interface{}) error {
	h.afterExecCalls++
	return h.afterExecErr
}

func (h *allStagesHook) AfterSynthesis(ctx context.Context, pctx *core.PipelineContext, response string) (string, error) {
	h.afterSynthCalls++
	if h.afterSynthErr != nil {
		return response, h.afterSynthErr
	}
	if h.responseMutation != "" {
		return h.responseMutation, nil
	}
	return response, nil
}

// enrichmentHook injects enrichments into PipelineContext.
type enrichmentHook struct {
	key   string
	value interface{}
}

func (h *enrichmentHook) Name() string { return "enrichment-" + h.key }

func (h *enrichmentHook) BeforePlanning(ctx context.Context, pctx *core.PipelineContext) (*core.PipelineShortCircuit, error) {
	pctx.Enrichments[h.key] = h.value
	return nil, nil
}

// beforeOnlyHook only implements BeforePlanningHook (not other stages).
type beforeOnlyHook struct {
	name   string
	called bool
}

func (h *beforeOnlyHook) Name() string { return h.name }
func (h *beforeOnlyHook) BeforePlanning(ctx context.Context, pctx *core.PipelineContext) (*core.PipelineShortCircuit, error) {
	h.called = true
	return nil, nil
}

// --- Tests: runBeforePlanningHooks ---

func TestRunBeforePlanningHooks_NoHooks(t *testing.T) {
	o := &AIOrchestrator{}
	pctx := &core.PipelineContext{Request: "test", Enrichments: map[string]interface{}{}}

	result := o.runBeforePlanningHooks(context.Background(), pctx)
	if result != nil {
		t.Errorf("expected nil, got %+v", result)
	}
}

func TestRunBeforePlanningHooks_TypeFiltering(t *testing.T) {
	// beforeOnlyHook implements BeforePlanningHook — should be called.
	// An allStagesHook that also implements BeforePlanningHook — should also be called.
	before := &beforeOnlyHook{name: "before-only"}
	all := &allStagesHook{name: "all-stages"}

	o := &AIOrchestrator{
		pipelineHooks: []core.PipelineHook{before, all},
	}
	pctx := &core.PipelineContext{Request: "test", Enrichments: map[string]interface{}{}}

	result := o.runBeforePlanningHooks(context.Background(), pctx)
	if result != nil {
		t.Errorf("expected nil (no short-circuit), got %+v", result)
	}
	if !before.called {
		t.Error("beforeOnlyHook was not called")
	}
	if all.beforePlanCalls != 1 {
		t.Errorf("allStagesHook.BeforePlanning: expected 1 call, got %d", all.beforePlanCalls)
	}
}

func TestRunBeforePlanningHooks_ShortCircuit(t *testing.T) {
	hook1 := &allStagesHook{
		name:         "cache-hit",
		shortCircuit: &core.PipelineShortCircuit{Response: "cached answer", Source: "semantic_cache"},
	}
	hook2 := &allStagesHook{name: "should-not-run"}

	o := &AIOrchestrator{
		pipelineHooks: []core.PipelineHook{hook1, hook2},
	}
	pctx := &core.PipelineContext{Request: "test", Enrichments: map[string]interface{}{}}

	result := o.runBeforePlanningHooks(context.Background(), pctx)
	if result == nil {
		t.Fatal("expected short-circuit, got nil")
	}
	if result.Response != "cached answer" {
		t.Errorf("expected 'cached answer', got %q", result.Response)
	}
	if result.Source != "semantic_cache" {
		t.Errorf("expected source 'semantic_cache', got %q", result.Source)
	}
	// Second hook should NOT have been called
	if hook2.beforePlanCalls != 0 {
		t.Errorf("hook2 should not have been called, got %d calls", hook2.beforePlanCalls)
	}
}

func TestRunBeforePlanningHooks_ErrorSkipsHook(t *testing.T) {
	failHook := &allStagesHook{name: "fail", beforePlanErr: errors.New("redis down")}
	okHook := &allStagesHook{name: "ok"}

	logger := &hookTestLogger{}
	o := &AIOrchestrator{
		pipelineHooks: []core.PipelineHook{failHook, okHook},
		logger:        logger,
	}
	pctx := &core.PipelineContext{Request: "test", Enrichments: map[string]interface{}{}}

	result := o.runBeforePlanningHooks(context.Background(), pctx)
	if result != nil {
		t.Errorf("expected nil (error skipped), got %+v", result)
	}
	// Both hooks should have been attempted
	if failHook.beforePlanCalls != 1 {
		t.Errorf("failHook: expected 1 call, got %d", failHook.beforePlanCalls)
	}
	if okHook.beforePlanCalls != 1 {
		t.Errorf("okHook: expected 1 call, got %d", okHook.beforePlanCalls)
	}
	// Logger should have been called with warning
	if logger.warnCount != 1 {
		t.Errorf("expected 1 warning logged, got %d", logger.warnCount)
	}
}

func TestRunBeforePlanningHooks_Enrichments(t *testing.T) {
	hook := &enrichmentHook{key: core.EnrichmentRAGContext, value: "retrieved docs"}

	o := &AIOrchestrator{
		pipelineHooks: []core.PipelineHook{hook},
	}
	pctx := &core.PipelineContext{Request: "test", Enrichments: map[string]interface{}{}}

	o.runBeforePlanningHooks(context.Background(), pctx)

	if pctx.Enrichments[core.EnrichmentRAGContext] != "retrieved docs" {
		t.Errorf("expected enrichment 'retrieved docs', got %v", pctx.Enrichments[core.EnrichmentRAGContext])
	}
}

func TestPrepareKnownEnrichments_PreparesConversationTurns(t *testing.T) {
	preparer := &stubConversationHistoryPreparer{prepared: "prepared history"}
	enrichments := map[string]interface{}{}
	metadata := map[string]interface{}{
		MetadataConversationTurns: []core.ConversationTurn{
			{Role: "user", Content: "hello"},
		},
		MetadataConversationID: "conversation-123",
	}

	prepareKnownEnrichments(context.Background(), metadata, enrichments, preparer)

	if enrichments[core.EnrichmentConversationHistory] != "prepared history" {
		t.Fatalf("expected prepared conversation history, got %v", enrichments[core.EnrichmentConversationHistory])
	}
	if preparer.lastConversationID != "conversation-123" {
		t.Fatalf("expected conversation ID to be forwarded, got %q", preparer.lastConversationID)
	}
	if len(preparer.lastTurns) != 1 {
		t.Fatalf("expected turns to be forwarded, got %d", len(preparer.lastTurns))
	}
}

func TestPrepareKnownEnrichments_PreparesConversationTextWithCanonicalID(t *testing.T) {
	preparer := &stubConversationHistoryPreparer{prepared: "prepared text"}
	enrichments := map[string]interface{}{}
	metadata := map[string]interface{}{
		MetadataConversationID:             "conversation-text-1",
		core.EnrichmentConversationHistory: "User: hello\nAssistant: hi",
	}

	prepareKnownEnrichments(context.Background(), metadata, enrichments, preparer)

	if enrichments[core.EnrichmentConversationHistory] != "prepared text" {
		t.Fatalf("expected prepared conversation history, got %v", enrichments[core.EnrichmentConversationHistory])
	}
	if preparer.lastConversationID != "conversation-text-1" {
		t.Fatalf("expected canonical ID to be forwarded, got %q", preparer.lastConversationID)
	}
	if preparer.lastText != "User: hello\nAssistant: hi" {
		t.Fatalf("expected formatted history to be forwarded, got %q", preparer.lastText)
	}
}

func TestPrepareKnownEnrichments_DoesNotUseInvalidConversationID(t *testing.T) {
	preparer := &stubConversationHistoryPreparer{prepared: "prepared text"}
	enrichments := map[string]interface{}{}
	metadata := map[string]interface{}{
		MetadataConversationID:             "invalid conversation",
		core.EnrichmentConversationHistory: "User: hello",
	}

	prepareKnownEnrichments(context.Background(), metadata, enrichments, preparer)

	if preparer.lastConversationID != "" {
		t.Fatalf("expected invalid conversation ID to be omitted, got %q", preparer.lastConversationID)
	}
}

func TestPrepareKnownEnrichments_KeepsConversationCacheKeysSeparate(t *testing.T) {
	preparer := &stubConversationHistoryPreparer{prepared: "prepared text"}

	for _, conversationID := range []string{"conversation-a", "conversation-b"} {
		prepareKnownEnrichments(
			context.Background(),
			map[string]interface{}{
				MetadataConversationID:             conversationID,
				core.EnrichmentConversationHistory: "User: hello",
			},
			map[string]interface{}{},
			preparer,
		)
		if preparer.lastConversationID != conversationID {
			t.Fatalf("cache key = %q, want %q", preparer.lastConversationID, conversationID)
		}
	}
}

func TestMaxConversationIDLengthMatchesBaggageValueLimit(t *testing.T) {
	if core.MaxConversationIDLength != telemetry.MaxBaggageValueLength {
		t.Fatalf(
			"core.MaxConversationIDLength = %d, telemetry.MaxBaggageValueLength = %d",
			core.MaxConversationIDLength,
			telemetry.MaxBaggageValueLength,
		)
	}
}

func TestPrepareKnownEnrichments_FallsBackToLegacyConversationText(t *testing.T) {
	enrichments := map[string]interface{}{}
	metadata := map[string]interface{}{
		core.EnrichmentConversationHistory: "legacy formatted history",
	}

	prepareKnownEnrichments(context.Background(), metadata, enrichments, nil)

	if enrichments[core.EnrichmentConversationHistory] != "legacy formatted history" {
		t.Fatalf("expected legacy conversation history to be preserved, got %v", enrichments[core.EnrichmentConversationHistory])
	}
}

func TestPrepareKnownEnrichments_DecodesJSONRoundTrippedConversationTurns(t *testing.T) {
	preparer := &stubConversationHistoryPreparer{prepared: "prepared from decoded turns"}
	enrichments := map[string]interface{}{}
	metadata := map[string]interface{}{
		MetadataConversationTurns: []interface{}{
			map[string]interface{}{
				"role":    "user",
				"content": "hello after resume",
			},
			map[string]interface{}{
				"role":    "assistant",
				"content": "welcome back",
			},
		},
		MetadataConversationID: "conversation-resume-1",
	}

	prepareKnownEnrichments(context.Background(), metadata, enrichments, preparer)

	if enrichments[core.EnrichmentConversationHistory] != "prepared from decoded turns" {
		t.Fatalf("expected decoded conversation turns to be prepared, got %v", enrichments[core.EnrichmentConversationHistory])
	}
	if preparer.lastConversationID != "conversation-resume-1" {
		t.Fatalf("expected conversation ID to survive resume decoding, got %q", preparer.lastConversationID)
	}
	if len(preparer.lastTurns) != 2 {
		t.Fatalf("expected 2 decoded turns, got %d", len(preparer.lastTurns))
	}
	if preparer.lastTurns[0].Content != "hello after resume" {
		t.Fatalf("expected first decoded turn content to match, got %q", preparer.lastTurns[0].Content)
	}
}

func TestPrepareKnownEnrichments_CopiesRAGContext(t *testing.T) {
	enrichments := map[string]interface{}{}
	metadata := map[string]interface{}{
		core.EnrichmentRAGContext: "docs",
	}

	prepareKnownEnrichments(context.Background(), metadata, enrichments, nil)

	if enrichments[core.EnrichmentRAGContext] != "docs" {
		t.Fatalf("expected RAG context to be copied, got %v", enrichments[core.EnrichmentRAGContext])
	}
}

// --- Tests: runAfterPlanningHooks ---

func TestRunAfterPlanningHooks_MutationChaining(t *testing.T) {
	hook1 := &allStagesHook{name: "h1", planMutation: "plan-v2"}
	hook2 := &allStagesHook{name: "h2", planMutation: "plan-v3"}

	o := &AIOrchestrator{
		pipelineHooks: []core.PipelineHook{hook1, hook2},
	}
	pctx := &core.PipelineContext{Request: "test", Enrichments: map[string]interface{}{}}

	result := o.runAfterPlanningHooks(context.Background(), pctx, "plan-v1")

	if result != "plan-v3" {
		t.Errorf("expected 'plan-v3' after chaining, got %v", result)
	}
	if hook1.afterPlanCalls != 1 || hook2.afterPlanCalls != 1 {
		t.Errorf("expected each hook called once, got h1=%d h2=%d", hook1.afterPlanCalls, hook2.afterPlanCalls)
	}
}

func TestRunAfterPlanningHooks_ErrorPreservesOriginalPlan(t *testing.T) {
	hook := &allStagesHook{name: "fail", afterPlanErr: errors.New("oops")}

	logger := &hookTestLogger{}
	o := &AIOrchestrator{
		pipelineHooks: []core.PipelineHook{hook},
		logger:        logger,
	}
	pctx := &core.PipelineContext{Request: "test", Enrichments: map[string]interface{}{}}

	result := o.runAfterPlanningHooks(context.Background(), pctx, "original-plan")

	if result != "original-plan" {
		t.Errorf("expected 'original-plan' preserved on error, got %v", result)
	}
	if logger.warnCount != 1 {
		t.Errorf("expected 1 warning, got %d", logger.warnCount)
	}
}

func TestRunAfterPlanningHooks_SkipsNonMatchingHooks(t *testing.T) {
	// beforeOnlyHook does NOT implement AfterPlanningHook
	before := &beforeOnlyHook{name: "before-only"}
	all := &allStagesHook{name: "all"}

	o := &AIOrchestrator{
		pipelineHooks: []core.PipelineHook{before, all},
	}
	pctx := &core.PipelineContext{Request: "test", Enrichments: map[string]interface{}{}}

	o.runAfterPlanningHooks(context.Background(), pctx, "plan")

	if all.afterPlanCalls != 1 {
		t.Errorf("expected 1 call on allStagesHook, got %d", all.afterPlanCalls)
	}
}

// --- Tests: runAfterExecutionHooks ---

func TestRunAfterExecutionHooks_Called(t *testing.T) {
	hook := &allStagesHook{name: "exec-hook"}

	o := &AIOrchestrator{
		pipelineHooks: []core.PipelineHook{hook},
	}
	pctx := &core.PipelineContext{Request: "test", Enrichments: map[string]interface{}{}}

	o.runAfterExecutionHooks(context.Background(), pctx, "results")

	if hook.afterExecCalls != 1 {
		t.Errorf("expected 1 call, got %d", hook.afterExecCalls)
	}
}

func TestRunAfterExecutionHooks_ErrorLogged(t *testing.T) {
	hook := &allStagesHook{name: "fail-exec", afterExecErr: errors.New("exec error")}

	logger := &hookTestLogger{}
	o := &AIOrchestrator{
		pipelineHooks: []core.PipelineHook{hook},
		logger:        logger,
	}
	pctx := &core.PipelineContext{Request: "test", Enrichments: map[string]interface{}{}}

	// Should not panic — errors are logged and skipped
	o.runAfterExecutionHooks(context.Background(), pctx, "results")

	if logger.warnCount != 1 {
		t.Errorf("expected 1 warning, got %d", logger.warnCount)
	}
}

// --- Tests: runAfterSynthesisHooks ---

func TestRunAfterSynthesisHooks_MutationChaining(t *testing.T) {
	hook1 := &allStagesHook{name: "h1", responseMutation: "response-v2"}
	hook2 := &allStagesHook{name: "h2", responseMutation: "response-v3"}

	o := &AIOrchestrator{
		pipelineHooks: []core.PipelineHook{hook1, hook2},
	}
	pctx := &core.PipelineContext{Request: "test", Enrichments: map[string]interface{}{}}

	result := o.runAfterSynthesisHooks(context.Background(), pctx, "response-v1")

	if result != "response-v3" {
		t.Errorf("expected 'response-v3', got %q", result)
	}
}

func TestRunAfterSynthesisHooks_ErrorPreservesResponse(t *testing.T) {
	hook := &allStagesHook{name: "fail-synth", afterSynthErr: errors.New("synth error")}

	logger := &hookTestLogger{}
	o := &AIOrchestrator{
		pipelineHooks: []core.PipelineHook{hook},
		logger:        logger,
	}
	pctx := &core.PipelineContext{Request: "test", Enrichments: map[string]interface{}{}}

	result := o.runAfterSynthesisHooks(context.Background(), pctx, "original-response")

	if result != "original-response" {
		t.Errorf("expected 'original-response' preserved on error, got %q", result)
	}
	if logger.warnCount != 1 {
		t.Errorf("expected 1 warning, got %d", logger.warnCount)
	}
}

func TestRunAfterSynthesisHooks_NoHooksReturnsOriginal(t *testing.T) {
	o := &AIOrchestrator{}
	pctx := &core.PipelineContext{Request: "test", Enrichments: map[string]interface{}{}}

	result := o.runAfterSynthesisHooks(context.Background(), pctx, "original")

	if result != "original" {
		t.Errorf("expected 'original', got %q", result)
	}
}

// --- Tests: telemetry integration ---

func TestRunBeforePlanningHooks_WithTelemetry(t *testing.T) {
	hook := &allStagesHook{
		name:         "cache",
		shortCircuit: &core.PipelineShortCircuit{Response: "cached", Source: "test"},
	}

	tel := &core.NoOpTelemetry{}
	o := &AIOrchestrator{
		pipelineHooks: []core.PipelineHook{hook},
		telemetry:     tel,
	}
	pctx := &core.PipelineContext{Request: "test", Enrichments: map[string]interface{}{}}

	// Should not panic with telemetry set
	result := o.runBeforePlanningHooks(context.Background(), pctx)
	if result == nil || result.Response != "cached" {
		t.Errorf("expected short-circuit with telemetry, got %+v", result)
	}
}

// --- Helper: test logger ---

type hookTestLogger struct {
	core.NoOpLogger
	warnCount int
}

func (l *hookTestLogger) WarnWithContext(ctx context.Context, msg string, fields map[string]interface{}) {
	l.warnCount++
}
