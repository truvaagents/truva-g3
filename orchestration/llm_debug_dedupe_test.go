package orchestration

import "testing"

// TestDedupeLLMInteractions_DropsPairedShadow locks in the core Layer 1
// contract: an agent_llm_call row with the same prompt/response/duration/
// step_id/phase_number as a typed row is dropped, typed row is retained.
// See orchestration/bugs/BUG_LLM_INTERACTION_DOUBLE_RECORDING.md Layer 1.
func TestDedupeLLMInteractions_DropsPairedShadow(t *testing.T) {
	paired := []LLMInteraction{
		{
			Type:             "plan_generation",
			Prompt:           "plan this",
			Response:         "plan A",
			DurationMs:       142,
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
			PhaseNumber:      1,
			PromptTokens:     100,
			CompletionTokens: 50,
		},
	}
	out := DedupeLLMInteractions(paired)
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

// TestDedupeLLMInteractions_RetainsOrphanAgentCall: an agent_llm_call row
// with NO typed partner (reflection job, knowledge-extraction hook,
// custom agent endpoint, step-scoped call orchestration doesn't record)
// survives untouched.
func TestDedupeLLMInteractions_RetainsOrphanAgentCall(t *testing.T) {
	orphan := []LLMInteraction{
		{
			Type:            "agent_llm_call",
			SourceComponent: "reflection-worker",
			Prompt:          "reflect on this",
			Response:        "reflected",
			DurationMs:      300,
		},
	}
	out := DedupeLLMInteractions(orphan)
	if len(out) != 1 {
		t.Fatalf("orphan agent_llm_call must survive; got %d rows", len(out))
	}
	if out[0].SourceComponent != "reflection-worker" {
		t.Errorf("orphan row must preserve SourceComponent; got %q", out[0].SourceComponent)
	}
}

// TestDedupeLLMInteractions_NonWrappingAgentUnchanged: the travel-chat-
// agent regression surface. Agents that never wrap core.AIClient emit
// typed rows only. Dedupe must be a no-op.
func TestDedupeLLMInteractions_NonWrappingAgentUnchanged(t *testing.T) {
	typed := []LLMInteraction{
		{Type: "plan_generation", Prompt: "p1", Response: "r1", DurationMs: 100, PhaseNumber: 1},
		{Type: "synthesis_streaming", Prompt: "p2", Response: "r2", DurationMs: 200, PhaseNumber: 2},
	}
	out := DedupeLLMInteractions(typed)
	if len(out) != 2 {
		t.Fatalf("non-wrapping agent trace must be unchanged; got %d rows", len(out))
	}
}

// TestDedupeLLMInteractions_RetriesAreDistinctCalls: two rows with
// identical prompt but different response/duration are NOT treated as a
// pair. This matters for ChainClient failover and planner retries where
// the same prompt is sent twice with different results.
func TestDedupeLLMInteractions_RetriesAreDistinctCalls(t *testing.T) {
	retries := []LLMInteraction{
		{Type: "plan_generation", Prompt: "p", Response: "first-attempt", DurationMs: 100, PhaseNumber: 1, Attempt: 1},
		{Type: "plan_generation", Prompt: "p", Response: "second-attempt", DurationMs: 120, PhaseNumber: 1, Attempt: 2},
		{Type: "agent_llm_call", Prompt: "p", Response: "first-attempt", DurationMs: 100, PhaseNumber: 1},
		{Type: "agent_llm_call", Prompt: "p", Response: "second-attempt", DurationMs: 120, PhaseNumber: 1},
	}
	out := DedupeLLMInteractions(retries)
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
// step-scoped agent-call rendering surface (dag.js step-node attachments).
// agent_llm_call rows that have NO typed partner with matching key must
// survive — they belong to the parent step.
func TestDedupeLLMInteractions_StepScopedAgentCallSurvives(t *testing.T) {
	mixed := []LLMInteraction{
		{Type: "plan_generation", Prompt: "pp", Response: "rr", DurationMs: 50, PhaseNumber: 1},
		{Type: "agent_llm_call", SourceComponent: "my-agent", StepID: "step-3", Prompt: "agent-local-prompt", Response: "agent-local-response", DurationMs: 75, PhaseNumber: 1},
	}
	out := DedupeLLMInteractions(mixed)
	if len(out) != 2 {
		t.Fatalf("step-scoped agent_llm_call with distinct payload must survive alongside typed row; got %d rows", len(out))
	}
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
// executions with no LLM calls (cached responses, HITL-only flows).
func TestDedupeLLMInteractions_EmptyInputIsNoOp(t *testing.T) {
	if got := DedupeLLMInteractions(nil); len(got) != 0 {
		t.Fatalf("nil input must return empty/nil slice; got %d rows", len(got))
	}
	if got := DedupeLLMInteractions([]LLMInteraction{}); len(got) != 0 {
		t.Fatalf("empty input must return empty; got %d rows", len(got))
	}
}

// TestLLMInteractionJoinKey_DeterministicForIdenticalFields: the hash
// inputs must produce identical keys for rows with identical join-field
// values. This is the cornerstone invariant — if it fails, dedupe cannot
// pair shadow rows with their typed partners.
func TestLLMInteractionJoinKey_DeterministicForIdenticalFields(t *testing.T) {
	a := &LLMInteraction{Type: "t", Prompt: "p", Response: "r", DurationMs: 10, StepID: "s", PhaseNumber: 2}
	b := &LLMInteraction{Type: "different", Prompt: "p", Response: "r", DurationMs: 10, StepID: "s", PhaseNumber: 2}
	// Type is not part of the join key — keys must match even when type differs.
	if llmInteractionJoinKey(a) != llmInteractionJoinKey(b) {
		t.Fatal("join key must be type-independent: typed+agent_llm_call pair must hash identically")
	}
}

// TestLLMInteractionJoinKey_DistinctOnAnyJoinFieldChange: each of the
// five join-key fields must actually participate. If any one is
// accidentally excluded, genuine distinct calls would be treated as
// paired and dropped.
func TestLLMInteractionJoinKey_DistinctOnAnyJoinFieldChange(t *testing.T) {
	base := &LLMInteraction{Prompt: "p", Response: "r", DurationMs: 10, StepID: "s", PhaseNumber: 2}
	variants := []struct {
		name string
		mut  func(*LLMInteraction)
	}{
		{"prompt differs", func(i *LLMInteraction) { i.Prompt = "other" }},
		{"response differs", func(i *LLMInteraction) { i.Response = "other" }},
		{"duration differs", func(i *LLMInteraction) { i.DurationMs = 11 }},
		{"step_id differs", func(i *LLMInteraction) { i.StepID = "other" }},
		{"phase_number differs", func(i *LLMInteraction) { i.PhaseNumber = 3 }},
	}
	baseKey := llmInteractionJoinKey(base)
	for _, v := range variants {
		t.Run(v.name, func(t *testing.T) {
			variant := *base
			v.mut(&variant)
			if llmInteractionJoinKey(&variant) == baseKey {
				t.Fatalf("join key must change when %s", v.name)
			}
		})
	}
}
