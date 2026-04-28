package orchestration

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ─── DerivePrecedenceAudit — self-gating + cheap fields ──────────────────────

func TestDerivePrecedenceAudit_NoEnrichments_ReturnsNil(t *testing.T) {
	// Interactions that don't carry <user_profile> or <conversation_history>
	// must not get an audit record — keeps the LLMInteraction lean for
	// hook LLM calls, micro-resolution, tiered selection, etc.
	i := LLMInteraction{
		Type:   "tiered_selection",
		Prompt: "<available_tools>...</available_tools>\n<user_request>hi</user_request>",
	}
	got := DerivePrecedenceAudit(context.Background(), i, nil)
	assert.Nil(t, got, "audit must be nil when prompt has no profile or history")
}

func TestDerivePrecedenceAudit_ProfileOnly_CheapFieldsOnly(t *testing.T) {
	i := LLMInteraction{
		Type: "plan_generation",
		Prompt: `<available_agents>x</available_agents>
<user_profile>
Context:
- User is planning a trip (explicit, recorded 3 days ago)
</user_profile>
<context_precedence>
When ... disagree about a named entity ...
</context_precedence>
<user_request>
travel there
</user_request>`,
	}
	got := DerivePrecedenceAudit(context.Background(), i, nil)
	require.NotNil(t, got)
	assert.True(t, got.DirectiveEmitted, "directive was in the prompt text")
	assert.True(t, got.ProfilePresent)
	assert.False(t, got.HistoryPresent)
	assert.Equal(t, PromptKindPlanning, got.PromptKind)
	assert.Nil(t, got.ProfileContextEntities, "no extractor configured — entity fields stay nil")
	assert.Empty(t, got.Compliance)
	assert.Empty(t, got.AuditorVersion)
}

func TestDerivePrecedenceAudit_HistoryOnly_DirectiveMissing(t *testing.T) {
	// Directive NOT present in prompt — simulates a regression where the
	// helper was accidentally removed. DirectiveEmitted should be false
	// so dashboards can alert.
	i := LLMInteraction{
		Type: "synthesis",
		Prompt: `<user_request>x</user_request>
<conversation_history>
User asked about Rome last turn.
</conversation_history>
<agent_responses>...</agent_responses>`,
	}
	got := DerivePrecedenceAudit(context.Background(), i, nil)
	require.NotNil(t, got)
	assert.False(t, got.DirectiveEmitted, "regression signal: prompt has history but no directive")
	assert.False(t, got.ProfilePresent)
	assert.True(t, got.HistoryPresent)
	assert.Equal(t, PromptKindSynthesis, got.PromptKind)
}

func TestDerivePrecedenceAudit_PromptKindMappings(t *testing.T) {
	cases := map[string]string{
		"plan_generation":                PromptKindPlanning,
		"continuation_plan_generation":   PromptKindContinuation,
		"continuation_plan_regeneration": PromptKindContinuation,
		"synthesis":                      PromptKindSynthesis,
		"synthesis_streaming":            PromptKindSynthesis,
		"agent_llm_call":                 "agent_llm_call", // unknown → pass-through
	}
	for inType, wantKind := range cases {
		t.Run(inType, func(t *testing.T) {
			got := promptKindForInteractionType(inType)
			assert.Equal(t, wantKind, got)
		})
	}
}

// ─── DerivePrecedenceAudit — entity extraction path ──────────────────────────

// stubEntityExtractor is a minimal PrecedenceEntityExtractor for tests.
// Returns per-section fixtures so compliance classification can be
// exercised deterministically.
type stubEntityExtractor struct {
	profileEntities      []string
	conversationEntities []string
	requestEntities      []string
	planEntities         []string
	version              string
	calls                []PrecedenceSection
}

func (e *stubEntityExtractor) ExtractEntities(_ context.Context, section PrecedenceSection, _ string) []string {
	e.calls = append(e.calls, section)
	switch section {
	case PrecedenceSectionProfileContext:
		return e.profileEntities
	case PrecedenceSectionConversation:
		return e.conversationEntities
	case PrecedenceSectionRequest:
		return e.requestEntities
	case PrecedenceSectionPlanResponse:
		return e.planEntities
	}
	return nil
}

func (e *stubEntityExtractor) Version() string {
	if e.version == "" {
		return "stub/v1"
	}
	return e.version
}

func TestDerivePrecedenceAudit_WithExtractor_PopulatesEntitiesAndVersion(t *testing.T) {
	extractor := &stubEntityExtractor{
		profileEntities:      []string{"Switzerland"},
		conversationEntities: []string{"Rome"},
		requestEntities:      nil,
		planEntities:         []string{"Rome"},
		version:              "stub/v2",
	}
	i := LLMInteraction{
		Type: "plan_generation",
		Prompt: `<user_profile>
Context:
- User is planning a trip to Switzerland (explicit, recorded 12 days ago)
</user_profile>

<conversation_history>
User asked about Rome last turn.
</conversation_history>

<context_precedence>
When ... trust the live turn ...
</context_precedence>

<user_request>
travel there
</user_request>`,
		Response: `{"plan_id":"italy","steps":[{"parameters":{"city":"Rome"}}]}`,
	}

	got := DerivePrecedenceAudit(context.Background(), i, extractor)
	require.NotNil(t, got)
	assert.Equal(t, []string{"Switzerland"}, got.ProfileContextEntities)
	assert.Equal(t, []string{"Rome"}, got.ConversationEntities)
	assert.Equal(t, []string{"Rome"}, got.PlanTargetEntities)
	assert.Equal(t, "stub/v2", got.AuditorVersion)
	assert.Contains(t, extractor.calls, PrecedenceSectionProfileContext)
	assert.Contains(t, extractor.calls, PrecedenceSectionConversation)
	assert.Contains(t, extractor.calls, PrecedenceSectionPlanResponse)
}

// ─── Compliance classification ───────────────────────────────────────────────

func TestDerivePrecedenceCompliance_CompliantWhenPlanMatchesLiveTurn(t *testing.T) {
	a := &PrecedenceAudit{
		ProfileContextEntities: []string{"Switzerland"},
		ConversationEntities:   []string{"Rome"},
		RequestEntities:        nil,
		PlanTargetEntities:     []string{"Rome"},
	}
	assert.Equal(t, PrecedenceComplianceCompliant, derivePrecedenceCompliance(a))
}

func TestDerivePrecedenceCompliance_AnchoredOnProfileWhenPlanPicksProfileOnlyEntity(t *testing.T) {
	// The canonical Switzerland-vs-Italy regression: conversation says
	// Rome, profile carries stale Switzerland, plan picks Switzerland.
	a := &PrecedenceAudit{
		ProfileContextEntities: []string{"Switzerland"},
		ConversationEntities:   []string{"Rome"},
		RequestEntities:        nil,
		PlanTargetEntities:     []string{"Switzerland"},
	}
	assert.Equal(t, PrecedenceComplianceAnchoredOnProfile, derivePrecedenceCompliance(a))
}

func TestDerivePrecedenceCompliance_InconclusiveWhenNoPlanEntities(t *testing.T) {
	a := &PrecedenceAudit{
		ProfileContextEntities: []string{"Switzerland"},
		ConversationEntities:   []string{"Rome"},
		PlanTargetEntities:     nil,
	}
	assert.Equal(t, PrecedenceComplianceInconclusive, derivePrecedenceCompliance(a))
}

func TestDerivePrecedenceCompliance_CaseInsensitiveEntityMatching(t *testing.T) {
	a := &PrecedenceAudit{
		ProfileContextEntities: []string{"SWITZERLAND"},
		ConversationEntities:   []string{"rome"},
		PlanTargetEntities:     []string{"Rome"},
	}
	assert.Equal(t, PrecedenceComplianceCompliant, derivePrecedenceCompliance(a))
}

func TestDerivePrecedenceCompliance_EntityInBothProfileAndLiveTurnCountsAsCompliant(t *testing.T) {
	// User re-confirmed Switzerland this turn. Profile still has it and
	// so does conversation_history. Plan picking Switzerland is correct,
	// not anchored-on-profile.
	a := &PrecedenceAudit{
		ProfileContextEntities: []string{"Switzerland"},
		ConversationEntities:   []string{"Switzerland"},
		PlanTargetEntities:     []string{"Switzerland"},
	}
	assert.Equal(t, PrecedenceComplianceCompliant, derivePrecedenceCompliance(a))
}

// ─── writeContextPrecedence — cheap telemetry path ──────────────────────────

// TestWriteContextPrecedence_PromptKindLabel documents the
// prompt_kind label set emitted on telemetry. If a new call site is
// added, extend this list in the same commit so dashboards and
// compliance queries cover it.
func TestWriteContextPrecedence_PromptKindLabel(t *testing.T) {
	expected := []string{
		PromptKindPlanning,
		PromptKindPlanningFallback,
		PromptKindContinuation,
		PromptKindSynthesis,
		PromptKindSynthesisOrchestrator,
	}
	// Every label is a non-empty stable identifier.
	seen := map[string]bool{}
	for _, kind := range expected {
		assert.NotEmpty(t, kind, "prompt kind constant must be non-empty")
		assert.False(t, seen[kind], "prompt kind %q collides with another", kind)
		seen[kind] = true
		// Labels should be safe for dashboards (lowercase + underscores only).
		assert.NotContains(t, kind, " ", "prompt kind %q contains spaces", kind)
		assert.Equal(t, strings.ToLower(kind), kind, "prompt kind %q should be lowercase", kind)
	}
}

// ─── PrecedenceEntityExtractor contract ─────────────────────────────────────

// TestPrecedenceEntityExtractor_InterfaceShape is a compile-time
// acknowledgement that the stub above implements the contract. Adding
// fields/methods to the interface would break this test, preventing
// silent API drift.
func TestPrecedenceEntityExtractor_InterfaceShape(t *testing.T) {
	var _ PrecedenceEntityExtractor = (*stubEntityExtractor)(nil)
}

// ─── Compliance edge cases ──────────────────────────────────────────────────

// TestDerivePrecedenceCompliance_EmptyProfileWithPlanEntities guards the
// "nothing to anchor on" case. With no profile Context entities, there is
// no way for the plan to leak profile-only state into the output, so the
// label must be Compliant regardless of whether the plan entities also
// appear in the live turn.
func TestDerivePrecedenceCompliance_EmptyProfileWithPlanEntities(t *testing.T) {
	a := &PrecedenceAudit{
		ProfileContextEntities: nil,
		ConversationEntities:   []string{"Rome"},
		PlanTargetEntities:     []string{"Rome", "Florence"}, // Florence not in live turn, but also not in profile
	}
	assert.Equal(t, PrecedenceComplianceCompliant, derivePrecedenceCompliance(a))
}

// TestDerivePrecedenceCompliance_EmptyLiveTurn_ProfileLeakDetected covers
// the pathological case where the live turn carries no entities
// (conversation and request both empty). A plan entity that matches a
// profile-only entry must still be flagged — this is exactly the stale-
// memory symptom the precedence rule is meant to catch.
func TestDerivePrecedenceCompliance_EmptyLiveTurn_ProfileLeakDetected(t *testing.T) {
	a := &PrecedenceAudit{
		ProfileContextEntities: []string{"Switzerland"},
		ConversationEntities:   nil,
		RequestEntities:        nil,
		PlanTargetEntities:     []string{"Switzerland"},
	}
	assert.Equal(t, PrecedenceComplianceAnchoredOnProfile, derivePrecedenceCompliance(a))
}

// TestDerivePrecedenceCompliance_EmptyLiveTurn_NoProfileMatch_StaysCompliant
// mirrors the above but with a plan entity that doesn't appear in profile
// either — no signal to flag.
func TestDerivePrecedenceCompliance_EmptyLiveTurn_NoProfileMatch_StaysCompliant(t *testing.T) {
	a := &PrecedenceAudit{
		ProfileContextEntities: []string{"Switzerland"},
		ConversationEntities:   nil,
		RequestEntities:        nil,
		PlanTargetEntities:     []string{"Japan"},
	}
	assert.Equal(t, PrecedenceComplianceCompliant, derivePrecedenceCompliance(a))
}

// ─── SetPrecedenceEntityExtractor — propagation ─────────────────────────────

// TestAIOrchestrator_SetPrecedenceEntityExtractor_PropagatesToSynthesizer
// pins the propagation behaviour. The common operator wiring is:
//
//	o.SetPrecedenceEntityExtractor(myExtractor)
//
// which must reach the synthesizer so synthesis interactions also get
// entity-level audit data. Dropping the propagation line would silently
// leave the synthesizer with nil extractor and no compliance signal on
// synthesis prompts — a regression that the trace can't easily reveal.
func TestAIOrchestrator_SetPrecedenceEntityExtractor_PropagatesToSynthesizer(t *testing.T) {
	synth := &AISynthesizer{}
	orch := &AIOrchestrator{synthesizer: synth}
	extractor := &stubEntityExtractor{version: "test/v1"}

	orch.SetPrecedenceEntityExtractor(extractor)

	assert.Same(t, extractor, orch.precedenceEntityExtractor, "orchestrator should hold the extractor")
	assert.Same(t, extractor, synth.precedenceEntityExtractor, "synthesizer should receive the same extractor via propagation")
}

// TestAIOrchestrator_SetPrecedenceEntityExtractor_NilSynthesizer_NoPanic
// guards the early-construction path (synthesizer not yet wired). The
// propagation must no-op rather than panic on nil synthesizer.
func TestAIOrchestrator_SetPrecedenceEntityExtractor_NilSynthesizer_NoPanic(t *testing.T) {
	orch := &AIOrchestrator{synthesizer: nil}
	extractor := &stubEntityExtractor{}

	assert.NotPanics(t, func() {
		orch.SetPrecedenceEntityExtractor(extractor)
	})
	assert.Same(t, extractor, orch.precedenceEntityExtractor)
}

// TestAIOrchestrator_SetPrecedenceEntityExtractor_NilClearsBothLayers
// documents the "disable entity extraction" path. Passing nil must clear
// the extractor on both the orchestrator and the synthesizer so the audit
// falls back to cheap-only mode.
func TestAIOrchestrator_SetPrecedenceEntityExtractor_NilClearsBothLayers(t *testing.T) {
	synth := &AISynthesizer{}
	orch := &AIOrchestrator{synthesizer: synth}
	orch.SetPrecedenceEntityExtractor(&stubEntityExtractor{})
	require.NotNil(t, orch.precedenceEntityExtractor)
	require.NotNil(t, synth.precedenceEntityExtractor)

	orch.SetPrecedenceEntityExtractor(nil)

	assert.Nil(t, orch.precedenceEntityExtractor)
	assert.Nil(t, synth.precedenceEntityExtractor)
}

// ─── recordDebugInteraction audit wiring (integration via fake store) ───────

// TestAIOrchestrator_RecordDebugInteraction_AttachesAuditForPlanningPrompt
// pins the central fact that recordDebugInteraction derives and attaches
// PrecedenceAudit for interactions whose prompt carries conflict-eligible
// enrichments. The derivation line is the glue between the audit helper
// and the LLMInteraction record — removing it silently disables the
// entire moderate-option observability surface.
func TestAIOrchestrator_RecordDebugInteraction_AttachesAuditForPlanningPrompt(t *testing.T) {
	store := &capturingDebugStore{}
	orch := &AIOrchestrator{debugStore: store}

	interaction := LLMInteraction{
		Type: "plan_generation",
		Prompt: `<user_profile>
Context:
- User is planning a trip (explicit, recorded 3 days ago)
</user_profile>
<context_precedence>...</context_precedence>
<user_request>go</user_request>`,
	}
	orch.recordDebugInteraction(context.Background(), "req-1", interaction)
	orch.debugWg.Wait() // deterministic: drain the async record goroutine

	recorded := store.getInteractions()
	require.Len(t, recorded, 1, "exactly one interaction should be recorded")
	require.NotNil(t, recorded[0].PrecedenceAudit, "audit must be populated for plan_generation with <user_profile>")
	assert.True(t, recorded[0].PrecedenceAudit.DirectiveEmitted)
	assert.True(t, recorded[0].PrecedenceAudit.ProfilePresent)
	assert.Equal(t, PromptKindPlanning, recorded[0].PrecedenceAudit.PromptKind)
}

// TestAIOrchestrator_RecordDebugInteraction_PreservesPreSetAudit guards
// the nil-check in the derivation call. If a caller pre-sets
// PrecedenceAudit (e.g., for tests, or a future code path that computes
// it earlier), the record path must not overwrite it.
func TestAIOrchestrator_RecordDebugInteraction_PreservesPreSetAudit(t *testing.T) {
	store := &capturingDebugStore{}
	orch := &AIOrchestrator{debugStore: store}

	preset := &PrecedenceAudit{
		DirectiveEmitted: true,
		ProfilePresent:   true,
		PromptKind:       "test_kind",
		AuditorVersion:   "preset/v0",
	}
	interaction := LLMInteraction{
		Type: "plan_generation",
		Prompt: `<user_profile>
Context:
- User is planning a trip
</user_profile>
<user_request>go</user_request>`,
		PrecedenceAudit: preset,
	}
	orch.recordDebugInteraction(context.Background(), "req-1", interaction)
	orch.debugWg.Wait()

	recorded := store.getInteractions()
	require.Len(t, recorded, 1)
	assert.Same(t, preset, recorded[0].PrecedenceAudit, "pre-set audit must survive the record path unchanged")
	assert.Equal(t, "preset/v0", recorded[0].PrecedenceAudit.AuditorVersion)
	assert.Equal(t, "test_kind", recorded[0].PrecedenceAudit.PromptKind)
}

// TestAIOrchestrator_RecordDebugInteraction_NoAuditForIrrelevantInteraction
// guards the self-gating: interactions whose prompt has no
// <user_profile> or <conversation_history> (hook LLM calls,
// micro-resolution, tiered selection, etc.) must NOT carry an audit —
// it keeps the record lean and the registry-viewer filter "has audit"
// meaningful.
func TestAIOrchestrator_RecordDebugInteraction_NoAuditForIrrelevantInteraction(t *testing.T) {
	store := &capturingDebugStore{}
	orch := &AIOrchestrator{debugStore: store}

	interaction := LLMInteraction{
		Type:   "tiered_selection",
		Prompt: "<available_tools>...</available_tools>\n<user_request>go</user_request>",
	}
	orch.recordDebugInteraction(context.Background(), "req-1", interaction)
	orch.debugWg.Wait()

	recorded := store.getInteractions()
	require.Len(t, recorded, 1)
	assert.Nil(t, recorded[0].PrecedenceAudit, "interactions without profile/history must not carry an audit")
}

// TestAIOrchestrator_RecordDebugInteraction_NoStoreSkipsDerivation is a
// small efficiency check: when the orchestrator is configured with no
// debug store, recordDebugInteraction early-returns before deriving an
// audit. Observing this through public API is awkward, so we verify by
// asserting that the call doesn't panic even with completely empty
// setup.
func TestAIOrchestrator_RecordDebugInteraction_NoStoreSkipsDerivation(t *testing.T) {
	orch := &AIOrchestrator{debugStore: nil}
	interaction := LLMInteraction{
		Type: "plan_generation",
		Prompt: `<user_profile>
Context:
- X
</user_profile>`,
	}
	assert.NotPanics(t, func() {
		orch.recordDebugInteraction(context.Background(), "req-1", interaction)
	})
}

// TestAISynthesizer_RecordDebugInteraction_AttachesAuditForSynthesisPrompt
// mirrors the orchestrator-side wiring test for the synthesizer path.
func TestAISynthesizer_RecordDebugInteraction_AttachesAuditForSynthesisPrompt(t *testing.T) {
	store := &capturingDebugStore{}
	synth := &AISynthesizer{debugStore: store}

	interaction := LLMInteraction{
		Type: "synthesis",
		Prompt: `<user_request>go</user_request>
<conversation_history>
User asked about Rome last turn.
</conversation_history>
<context_precedence>...</context_precedence>
<agent_responses>...</agent_responses>`,
	}
	synth.recordDebugInteraction(context.Background(), "req-1", interaction)
	synth.debugWg.Wait()

	recorded := store.getInteractions()
	require.Len(t, recorded, 1)
	require.NotNil(t, recorded[0].PrecedenceAudit, "synthesis interaction with <conversation_history> must carry an audit")
	assert.False(t, recorded[0].PrecedenceAudit.ProfilePresent)
	assert.True(t, recorded[0].PrecedenceAudit.HistoryPresent)
	assert.Equal(t, PromptKindSynthesis, recorded[0].PrecedenceAudit.PromptKind)
}
