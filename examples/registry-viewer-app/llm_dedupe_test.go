package main

import (
	"testing"

	"github.com/truvaagents/truva-g3/orchestration"
)

// TestDedupeLLMInteractions_DropsPairedShadow covers the core Layer 1
// contract: an agent_llm_call row with the same prompt/response/duration/
// step_id/phase_number as a typed row is dropped, typed row is retained.
// This is the pair produced on every orchestration-initiated LLM call when
// the agent wraps core.AIClient with ai.InstrumentedAIClient.
// See orchestration/bugs/BUG_LLM_INTERACTION_DOUBLE_RECORDING.md Layer 1.
func TestDedupeLLMInteractions_DropsPairedShadow(t *testing.T) {
	paired := []orchestration.LLMInteraction{
		{
			Type:             "plan_generation",
			Prompt:           "plan this",
			Response:         "plan A",
			DurationMs:       142,
			StepID:           "",
			PhaseNumber:      1,
			PromptTokens:     100,
			CompletionTokens: 50,
		},
		{
			Type:             "agent_llm_call",
			SourceComponent:  "devops-chat-agent",
			Prompt:           "plan this",
			Response:         "plan A",
			DurationMs:       142,
			StepID:           "",
			PhaseNumber:      1,
			PromptTokens:     100,
			CompletionTokens: 50,
		},
	}
	out := orchestration.DedupeLLMInteractions(paired)
	if len(out) != 1 {
		t.Fatalf("expected 1 retained row (typed), got %d", len(out))
	}
	if out[0].Type != "plan_generation" {
		t.Fatalf("expected typed row retained, got type=%q", out[0].Type)
	}
	if out[0].SourceComponent != "" {
		t.Errorf("SourceComponent must remain empty on the typed row — wire-format invariant violated: got %q", out[0].SourceComponent)
	}
}

// TestDedupeLLMInteractions_RetainsOrphanAgentCall verifies the Layer 3
// contract: an agent_llm_call row with NO typed partner (reflection job,
// knowledge-extraction hook, custom agent endpoint, step-scoped call
// orchestration doesn't record) survives untouched.
func TestDedupeLLMInteractions_RetainsOrphanAgentCall(t *testing.T) {
	orphan := []orchestration.LLMInteraction{
		{
			Type:            "agent_llm_call",
			SourceComponent: "reflection-worker",
			Prompt:          "reflect on this",
			Response:        "reflected",
			DurationMs:      300,
			StepID:          "",
			PhaseNumber:     0,
		},
	}
	out := orchestration.DedupeLLMInteractions(orphan)
	if len(out) != 1 {
		t.Fatalf("orphan agent_llm_call must survive; got %d rows", len(out))
	}
	if out[0].SourceComponent != "reflection-worker" {
		t.Errorf("orphan row must preserve SourceComponent; got %q", out[0].SourceComponent)
	}
}

// TestDedupeLLMInteractions_NonWrappingAgentUnchanged is the travel-chat-
// agent regression surface: agents that never wrap core.AIClient emit
// typed rows only. Dedupe must be a no-op.
func TestDedupeLLMInteractions_NonWrappingAgentUnchanged(t *testing.T) {
	typed := []orchestration.LLMInteraction{
		{Type: "plan_generation", Prompt: "p1", Response: "r1", DurationMs: 100, PhaseNumber: 1},
		{Type: "synthesis_streaming", Prompt: "p2", Response: "r2", DurationMs: 200, PhaseNumber: 2},
	}
	out := orchestration.DedupeLLMInteractions(typed)
	if len(out) != 2 {
		t.Fatalf("non-wrapping agent trace must be unchanged; got %d rows", len(out))
	}
}

// TestDedupeLLMInteractions_RetriesAreDistinctCalls verifies the join rule
// correctly treats retries as separate calls: two rows with identical
// prompt but different response/duration are NOT treated as a pair. This
// matters for ChainClient failover and planner retries where the same
// prompt is sent twice with different results.
func TestDedupeLLMInteractions_RetriesAreDistinctCalls(t *testing.T) {
	retries := []orchestration.LLMInteraction{
		{Type: "plan_generation", Prompt: "p", Response: "first-attempt", DurationMs: 100, PhaseNumber: 1, Attempt: 1},
		{Type: "plan_generation", Prompt: "p", Response: "second-attempt", DurationMs: 120, PhaseNumber: 1, Attempt: 2},
		{Type: "agent_llm_call", Prompt: "p", Response: "first-attempt", DurationMs: 100, PhaseNumber: 1},
		{Type: "agent_llm_call", Prompt: "p", Response: "second-attempt", DurationMs: 120, PhaseNumber: 1},
	}
	out := orchestration.DedupeLLMInteractions(retries)
	if len(out) != 2 {
		t.Fatalf("both typed retry rows must survive as distinct calls; got %d rows", len(out))
	}
	for _, r := range out {
		if r.Type == "agent_llm_call" {
			t.Errorf("paired agent_llm_call shadow must be dropped per retry; got survivor %+v", r)
		}
	}
}

// TestDedupeLLMInteractions_StepScopedAgentCallSurvives locks in the
// step-scoped agent-call rendering surface (dag.js:1449-1475 filter on
// type=='agent_llm_call' && step_id && source_component). These rows do
// correspond to DAG steps — they must not be lumped with typed rows or
// dropped when a typed row with different payload happens to share keys.
func TestDedupeLLMInteractions_StepScopedAgentCallSurvives(t *testing.T) {
	mixed := []orchestration.LLMInteraction{
		{Type: "plan_generation", Prompt: "pp", Response: "rr", DurationMs: 50, PhaseNumber: 1},
		{Type: "agent_llm_call", SourceComponent: "my-agent", StepID: "step-3", Prompt: "agent-local-prompt", Response: "agent-local-response", DurationMs: 75, PhaseNumber: 1},
	}
	out := orchestration.DedupeLLMInteractions(mixed)
	if len(out) != 2 {
		t.Fatalf("step-scoped agent_llm_call with distinct payload must survive alongside typed row; got %d rows", len(out))
	}
	// Confirm the step-scoped row is preserved with full metadata.
	var foundStepScoped bool
	for _, r := range out {
		if r.Type == "agent_llm_call" && r.StepID == "step-3" && r.SourceComponent == "my-agent" {
			foundStepScoped = true
		}
	}
	if !foundStepScoped {
		t.Fatal("step-scoped agent_llm_call row was incorrectly dropped")
	}
}

// TestDedupeLLMInteractions_EmptyInputIsNoOp keeps the helper safe for
// executions with no LLM calls (e.g., cached responses or HITL-only flows).
func TestDedupeLLMInteractions_EmptyInputIsNoOp(t *testing.T) {
	if got := orchestration.DedupeLLMInteractions(nil); len(got) != 0 {
		t.Fatalf("nil input must return empty/nil slice; got %d rows", len(got))
	}
	if got := orchestration.DedupeLLMInteractions([]orchestration.LLMInteraction{}); len(got) != 0 {
		t.Fatalf("empty input must return empty; got %d rows", len(got))
	}
}
