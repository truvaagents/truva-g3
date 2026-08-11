package orchestration

import (
	"context"
	"errors"
	"testing"

	"github.com/truvaagents/truva-g3/core"
	"github.com/truvaagents/truva-g3/telemetry"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
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

type decisionHook struct {
	name          string
	decisionCalls int
	legacyCalls   int
	decision      func(core.PipelineGate) (*core.PipelineShortCircuitDecision, error)
}

func (h *decisionHook) Name() string { return h.name }

func (h *decisionHook) BeforePlanningDecision(
	_ context.Context,
	_ *core.PipelineContext,
	gate core.PipelineGate,
) (*core.PipelineShortCircuitDecision, error) {
	h.decisionCalls++
	return h.decision(gate)
}

// BeforePlanning deliberately makes decisionHook implement both contracts so
// tests can prove the opt-in method wins and exactly one callback is invoked.
func (h *decisionHook) BeforePlanning(
	_ context.Context,
	_ *core.PipelineContext,
) (*core.PipelineShortCircuit, error) {
	h.legacyCalls++
	return &core.PipelineShortCircuit{Response: "legacy must not run"}, nil
}

// --- Tests: runBeforePlanningHooks ---

func TestRunBeforePlanningHooks_NoHooks(t *testing.T) {
	o := &AIOrchestrator{}
	pctx := &core.PipelineContext{Request: "test", Enrichments: map[string]interface{}{}}

	result, err := o.runBeforePlanningHooks(context.Background(), pctx, newPipelineGate(nil, false))
	if err != nil {
		t.Fatalf("runBeforePlanningHooks() error = %v", err)
	}
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

	result, err := o.runBeforePlanningHooks(context.Background(), pctx, newPipelineGate(nil, false))
	if err != nil {
		t.Fatalf("runBeforePlanningHooks() error = %v", err)
	}
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

	result, err := o.runBeforePlanningHooks(context.Background(), pctx, newPipelineGate(nil, false))
	if err != nil {
		t.Fatalf("runBeforePlanningHooks() error = %v", err)
	}
	if result == nil {
		t.Fatal("expected short-circuit, got nil")
	}
	if result.shortCircuit.Response != "cached answer" {
		t.Errorf("expected 'cached answer', got %q", result.shortCircuit.Response)
	}
	if result.shortCircuit.Source != "semantic_cache" {
		t.Errorf("expected source 'semantic_cache', got %q", result.shortCircuit.Source)
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

	result, err := o.runBeforePlanningHooks(context.Background(), pctx, newPipelineGate(nil, false))
	if err != nil {
		t.Fatalf("runBeforePlanningHooks() error = %v", err)
	}
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

func TestAcceptShortCircuit_CacheVariationContract(t *testing.T) {
	response := &core.PipelineShortCircuit{Response: "response"}
	tests := []struct {
		name       string
		expected   map[string]string
		disabled   bool
		candidate  *core.PipelineShortCircuitDecision
		wantAccept bool
		wantReason string
		wantError  error
	}{
		{
			name:       "missing payload",
			candidate:  &core.PipelineShortCircuitDecision{Kind: core.PipelineShortCircuitCache},
			wantReason: "missing_payload",
			wantError:  ErrInvalidPipelineShortCircuitDecision,
		},
		{
			name: "unknown kind",
			candidate: &core.PipelineShortCircuitDecision{
				ShortCircuit: response,
				Kind:         "future-kind",
			},
			wantReason: "unknown_kind",
			wantError:  ErrUnknownPipelineShortCircuitKind,
		},
		{
			name:     "authoritative ignores cache policy",
			expected: map[string]string{"synthetic": "current"},
			disabled: true,
			candidate: &core.PipelineShortCircuitDecision{
				ShortCircuit: response,
				Kind:         core.PipelineShortCircuitAuthoritative,
			},
			wantAccept: true,
			wantReason: "authoritative",
		},
		{
			name:     "cache reads disabled",
			disabled: true,
			candidate: &core.PipelineShortCircuitDecision{
				ShortCircuit: response,
				Kind:         core.PipelineShortCircuitCache,
			},
			wantReason: "cache_read_disabled",
		},
		{
			name:     "current dimension missing from stored entry",
			expected: map[string]string{"synthetic": "current"},
			candidate: &core.PipelineShortCircuitDecision{
				ShortCircuit: response,
				Kind:         core.PipelineShortCircuitCache,
			},
			wantReason: "cache_dimension_mismatch",
		},
		{
			name: "stored dimension missing from current request",
			candidate: &core.PipelineShortCircuitDecision{
				ShortCircuit:  response,
				Kind:          core.PipelineShortCircuitCache,
				CachedAgainst: map[string]string{"synthetic": "stored"},
			},
			wantReason: "cache_dimension_mismatch",
		},
		{
			name:     "dimension value mismatch",
			expected: map[string]string{"synthetic": "current"},
			candidate: &core.PipelineShortCircuitDecision{
				ShortCircuit:  response,
				Kind:          core.PipelineShortCircuitCache,
				CachedAgainst: map[string]string{"synthetic": "stored"},
			},
			wantReason: "cache_dimension_mismatch",
		},
		{
			name:     "matching dimension",
			expected: map[string]string{"synthetic": "same"},
			candidate: &core.PipelineShortCircuitDecision{
				ShortCircuit:  response,
				Kind:          core.PipelineShortCircuitCache,
				CachedAgainst: map[string]string{"synthetic": "same"},
			},
			wantAccept: true,
			wantReason: "cache_match",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			accepted, reason, err := acceptShortCircuit(
				test.expected,
				test.disabled,
				test.candidate,
				"synthetic",
			)
			if accepted != test.wantAccept || reason != test.wantReason || !errors.Is(err, test.wantError) {
				t.Fatalf("acceptShortCircuit() = (%t, %q, %v), want (%t, %q, %v)", accepted, reason, err, test.wantAccept, test.wantReason, test.wantError)
			}
		})
	}
}

func TestRunBeforePlanningHooks_OptInDecisionPrecedenceAndDefensiveGate(t *testing.T) {
	var firstView map[string]string
	first := &decisionHook{
		name: "first",
		decision: func(gate core.PipelineGate) (*core.PipelineShortCircuitDecision, error) {
			firstView = gate.CacheVary()
			firstView["synthetic"] = "mutated"
			return &core.PipelineShortCircuitDecision{
				ShortCircuit:  &core.PipelineShortCircuit{Response: "stale"},
				Kind:          core.PipelineShortCircuitCache,
				CachedAgainst: map[string]string{"synthetic": "stale"},
			}, nil
		},
	}
	second := &decisionHook{
		name: "second",
		decision: func(gate core.PipelineGate) (*core.PipelineShortCircuitDecision, error) {
			if got := gate.CacheVary()["synthetic"]; got != "current" {
				t.Fatalf("second hook gate value = %q, want current", got)
			}
			return &core.PipelineShortCircuitDecision{
				ShortCircuit: &core.PipelineShortCircuit{Response: "policy response"},
				Kind:         core.PipelineShortCircuitAuthoritative,
			}, nil
		},
	}
	orchestrator := &AIOrchestrator{pipelineHooks: []core.PipelineHook{first, second}}

	result, err := orchestrator.runBeforePlanningHooks(
		context.Background(),
		&core.PipelineContext{Enrichments: map[string]interface{}{}},
		newPipelineGate(map[string]string{"synthetic": "current"}, false),
		"synthetic",
	)
	if err != nil {
		t.Fatalf("runBeforePlanningHooks() error = %v", err)
	}
	if result == nil || result.shortCircuit.Response != "policy response" ||
		result.kind != core.PipelineShortCircuitAuthoritative {
		t.Fatalf("result = %+v", result)
	}
	if first.decisionCalls != 1 || first.legacyCalls != 0 ||
		second.decisionCalls != 1 || second.legacyCalls != 0 {
		t.Fatalf("hook calls: first decision/legacy=%d/%d second=%d/%d", first.decisionCalls, first.legacyCalls, second.decisionCalls, second.legacyCalls)
	}
	if firstView["synthetic"] != "mutated" {
		t.Fatal("test did not mutate its private gate view")
	}
}

func TestPipelineGateReportsCacheReadPolicyAndReturnsDefensiveViews(t *testing.T) {
	gate := newPipelineGate(map[string]string{"synthetic": "current"}, true)
	if !gate.ResponseCacheReadDisabled() {
		t.Fatal("pipeline gate did not report disabled response-cache reads")
	}
	first := gate.CacheVary()
	first["synthetic"] = "mutated"
	if got := gate.CacheVary()["synthetic"]; got != "current" {
		t.Fatalf("pipeline gate cache variation was mutated through a returned view: %q", got)
	}
}

func TestRunBeforePlanningHooks_InvalidOptInDecisionFailsContract(t *testing.T) {
	hook := &decisionHook{
		name: "invalid",
		decision: func(core.PipelineGate) (*core.PipelineShortCircuitDecision, error) {
			return &core.PipelineShortCircuitDecision{
				ShortCircuit: &core.PipelineShortCircuit{Response: "response"},
			}, nil
		},
	}
	orchestrator := &AIOrchestrator{pipelineHooks: []core.PipelineHook{hook}}

	result, err := orchestrator.runBeforePlanningHooks(
		context.Background(),
		&core.PipelineContext{},
		newPipelineGate(nil, false),
	)
	if result != nil || !errors.Is(err, ErrUnknownPipelineShortCircuitKind) {
		t.Fatalf("runBeforePlanningHooks() = (%+v, %v)", result, err)
	}
}

func TestRunBeforePlanningHooks_LegacyResponseRemainsAuthoritative(t *testing.T) {
	hook := &allStagesHook{
		name:         "legacy-policy",
		shortCircuit: &core.PipelineShortCircuit{Response: "denied"},
	}
	orchestrator := &AIOrchestrator{pipelineHooks: []core.PipelineHook{hook}}

	result, err := orchestrator.runBeforePlanningHooks(
		context.Background(),
		&core.PipelineContext{},
		newPipelineGate(map[string]string{"synthetic": "current"}, true),
		"synthetic",
	)
	if err != nil {
		t.Fatalf("runBeforePlanningHooks() error = %v", err)
	}
	if result == nil || result.shortCircuit.Response != "denied" ||
		result.kind != core.PipelineShortCircuitAuthoritative ||
		result.diagnostic != "legacy_authoritative" {
		t.Fatalf("legacy result = %+v", result)
	}
}

func TestRunBeforePlanningHooks_Enrichments(t *testing.T) {
	hook := &enrichmentHook{key: core.EnrichmentRAGContext, value: "retrieved docs"}

	o := &AIOrchestrator{
		pipelineHooks: []core.PipelineHook{hook},
	}
	pctx := &core.PipelineContext{Request: "test", Enrichments: map[string]interface{}{}}

	if _, err := o.runBeforePlanningHooks(context.Background(), pctx, newPipelineGate(nil, false)); err != nil {
		t.Fatalf("runBeforePlanningHooks() error = %v", err)
	}

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

func TestPlanningHelpers_DoNotInvokeLifecycleOwnedAfterPlanningHook(t *testing.T) {
	client := &promptCapturingAIClient{
		responses: []string{validPlanJSON(boolPtr(true))},
	}
	orchestrator := setupTestOrchestrator(t, client)
	if err := orchestrator.discovery.Register(context.Background(), &core.ServiceRegistration{
		ID:   "test-agent",
		Name: "test-agent",
	}); err != nil {
		t.Fatalf("register test agent: %v", err)
	}
	orchestrator.catalog.agents = map[string]*AgentInfo{
		"test-agent": {
			Registration: &core.ServiceRegistration{Name: "test-agent"},
			Capabilities: []EnhancedCapability{{Name: "test_capability", Endpoint: "/process"}},
		},
	}
	hook := &allStagesHook{
		name:         "dead-production-after-planning",
		planMutation: &RoutingPlan{PlanID: "must-not-be-used"},
	}
	orchestrator.pipelineHooks = []core.PipelineHook{hook}

	completedResults := map[string]*StepResult{
		"step-1": {StepID: "step-1", AgentName: "test-agent", Success: true, Response: `{"ok":true}`},
	}
	tests := []struct {
		name string
		run  func() error
	}{
		{
			name: "initial",
			run: func() error {
				_, err := orchestrator.generateExecutionPlan(context.Background(), "request", "request-initial")
				return err
			},
		},
		{
			name: "continuation",
			run: func() error {
				_, err := orchestrator.generateContinuationPlan(
					context.Background(), "request", "request-continuation",
					completedResults, []string{"step-1"}, "continue", 2,
				)
				return err
			},
		},
		{
			name: "regeneration",
			run: func() error {
				_, err := orchestrator.regenerateContinuationPlan(
					context.Background(), "request", "request-regeneration",
					completedResults, []string{"step-1"}, "continue", 2,
					errors.New("characterized validation failure"), boolPtr(true),
				)
				return err
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			before := hook.afterPlanCalls
			if err := test.run(); err != nil {
				t.Fatalf("planning path returned error: %v", err)
			}
			if hook.afterPlanCalls != before {
				t.Fatalf("AfterPlanning calls changed from %d to %d", before, hook.afterPlanCalls)
			}
		})
	}
}

func TestAfterPlanningHook_RunsOnceOnFinalAcceptedInitialPlan(t *testing.T) {
	client := &promptCapturingAIClient{responses: []string{
		`{"plan_id":"accepted-plan","original_request":"request","mode":"autonomous","steps":[],"terminal":true}`,
		"synthesized response",
	}}
	orchestrator := setupTestOrchestrator(t, client)
	hook := &allStagesHook{name: "live-after-planning"}
	orchestrator.pipelineHooks = []core.PipelineHook{hook}

	response, err := orchestrator.ProcessRequest(context.Background(), "request", nil)
	if err != nil {
		t.Fatalf("ProcessRequest() error = %v", err)
	}
	if response == nil || response.Response == "" {
		t.Fatalf("ProcessRequest() response = %#v", response)
	}
	if hook.afterPlanCalls != 1 {
		t.Fatalf("AfterPlanning calls = %d, want exactly 1", hook.afterPlanCalls)
	}
	if hook.beforePlanCalls != 1 || hook.afterExecCalls != 1 || hook.afterSynthCalls != 1 {
		t.Fatalf("pipeline lifecycle calls = %+v", hook)
	}
}

func TestValidatedAfterPlanningHooks_RejectInvalidMutationAndPreservePriorPlan(t *testing.T) {
	orchestrator := setupTestOrchestrator(t, NewMockAIClient())
	orchestrator.catalog.agents = map[string]*AgentInfo{
		"test-agent": {
			Registration: &core.ServiceRegistration{Name: "test-agent"},
			Capabilities: []EnhancedCapability{{Name: "test_capability", Endpoint: "/process"}},
		},
	}
	valid := &RoutingPlan{
		PlanID: "valid-plan",
		Steps: []RoutingStep{{
			StepID: "step-1", AgentName: "test-agent", Instruction: "run",
			Metadata: map[string]interface{}{"capability": "test_capability"},
		}},
	}
	hook := &allStagesHook{name: "invalid-mutation", planMutation: &RoutingPlan{
		PlanID: "invalid-plan",
		Steps:  []RoutingStep{{StepID: "step-1", AgentName: "missing-agent"}},
	}}
	orchestrator.pipelineHooks = []core.PipelineHook{hook}

	result := orchestrator.runValidatedAfterPlanningHooks(
		context.Background(),
		&core.PipelineContext{Request: "request", Enrichments: map[string]interface{}{}},
		valid, nil, nil, 1, "request-id",
	)
	if result != valid || result.PlanID != "valid-plan" {
		t.Fatalf("invalid hook mutation replaced prior plan: %#v", result)
	}
	if valid.Steps[0].AgentName != "test-agent" {
		t.Fatalf("prior plan was mutated in place: %#v", valid)
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
	result, err := o.runBeforePlanningHooks(context.Background(), pctx, newPipelineGate(nil, false))
	if err != nil {
		t.Fatalf("runBeforePlanningHooks() error = %v", err)
	}
	if result == nil || result.shortCircuit.Response != "cached" {
		t.Errorf("expected short-circuit with telemetry, got %+v", result)
	}
}

func TestPipelineShortCircuitKindLabelIsBounded(t *testing.T) {
	for _, test := range []struct {
		kind core.PipelineShortCircuitKind
		want string
	}{
		{kind: core.PipelineShortCircuitAuthoritative, want: "authoritative"},
		{kind: core.PipelineShortCircuitCache, want: "cache"},
		{kind: core.PipelineShortCircuitKind("hook-controlled-value"), want: "unknown"},
	} {
		if got := pipelineShortCircuitKindLabel(test.kind); got != test.want {
			t.Fatalf("pipelineShortCircuitKindLabel(%q) = %q, want %q", test.kind, got, test.want)
		}
	}
}

func TestAcceptedShortCircuitIsVisibleOnRequestTrace(t *testing.T) {
	recorder := tracetest.NewSpanRecorder()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder))
	t.Cleanup(func() { _ = provider.Shutdown(context.Background()) })
	ctx := telemetry.WithBaggage(t.Context(), "request_id", "request-short-circuit")
	ctx, span := provider.Tracer("pipeline-hook-test").Start(ctx, "request")
	hook := &decisionHook{
		name: "policy",
		decision: func(core.PipelineGate) (*core.PipelineShortCircuitDecision, error) {
			return &core.PipelineShortCircuitDecision{
				ShortCircuit: &core.PipelineShortCircuit{Response: "denied"},
				Kind:         core.PipelineShortCircuitAuthoritative,
			}, nil
		},
	}
	orchestrator := &AIOrchestrator{pipelineHooks: []core.PipelineHook{hook}}
	result, err := orchestrator.runBeforePlanningHooks(ctx, &core.PipelineContext{}, newPipelineGate(nil, false))
	span.End()
	if err != nil || result == nil {
		t.Fatalf("short-circuit result/error = %#v, %v", result, err)
	}

	ended := recorder.Ended()
	if len(ended) != 1 {
		t.Fatalf("ended spans = %d, want 1", len(ended))
	}
	attrs := map[string]string{}
	for _, attr := range ended[0].Attributes() {
		attrs[string(attr.Key)] = attr.Value.String()
	}
	if attrs["pipeline.short_circuit"] != "true" ||
		attrs["pipeline.short_circuit.kind"] != "authoritative" {
		t.Fatalf("request span attributes = %#v", attrs)
	}
	var decisions int
	for _, event := range ended[0].Events() {
		if event.Name == "pipeline.short_circuit.decision" {
			decisions++
			if len(event.Attributes) == 0 || event.Attributes[0].Key != "request_id" ||
				event.Attributes[0].Value.AsString() != "request-short-circuit" {
				t.Fatalf("short-circuit event correlation = %#v", event.Attributes)
			}
		}
	}
	if decisions != 1 {
		t.Fatalf("short-circuit decision events = %d, want 1", decisions)
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
