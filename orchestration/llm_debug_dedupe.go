package orchestration

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
)

// DedupeLLMInteractions joins shadow agent_llm_call rows written by
// ai.InstrumentedAIClient to their typed partners written by orchestration-
// side recorders, and drops the agent_llm_call partner so each physical
// LLM call contributes exactly one row.
//
// Join key: (sha256(prompt), sha256(response), duration_ms, step_id,
// phase_number). All five fields must match for two rows to be treated as
// paired — both recorders read the same response from the same wrapped
// client with the same latency, so the tuple is a deterministic surrogate
// for "same physical call". Retries produce distinct rows (different
// response/duration), so they correctly survive as separate calls.
//
// The typed row is retained. SourceComponent is NOT lifted onto the
// retained row (would violate the "Recording Sites" invariant in
// orchestration/ARCHITECTURE.md that typed rows carry empty
// SourceComponent). Per-agent attribution for the retained row is derived
// from the execution store's StoredExecution.AgentName by callers.
//
// Orphan agent_llm_call rows — those with no typed partner (reflection
// job, knowledge-extraction hook, custom agent endpoints, step-scoped
// agent-side calls orchestration doesn't record) — survive untouched.
//
// See orchestration/bugs/BUG_LLM_INTERACTION_DOUBLE_RECORDING.md Layer 1.
func DedupeLLMInteractions(interactions []LLMInteraction) []LLMInteraction {
	if len(interactions) == 0 {
		return interactions
	}

	// Pass 1: build a set of join keys present on typed (non-agent_llm_call)
	// rows. Those rows are retained; agent_llm_call rows whose key matches
	// a typed row are dropped as duplicates.
	typedKeys := make(map[string]struct{}, len(interactions))
	for i := range interactions {
		in := &interactions[i]
		if in.Type == "agent_llm_call" {
			continue
		}
		typedKeys[llmInteractionJoinKey(in)] = struct{}{}
	}

	// Pass 2: retain every typed row; drop agent_llm_call rows whose join
	// key matches a typed row; retain orphan agent_llm_call rows.
	out := make([]LLMInteraction, 0, len(interactions))
	for i := range interactions {
		in := interactions[i]
		if in.Type == "agent_llm_call" {
			if _, paired := typedKeys[llmInteractionJoinKey(&in)]; paired {
				continue
			}
		}
		out = append(out, in)
	}
	return out
}

// llmInteractionJoinKey renders the canonical dedupe tuple as a single
// string. sha256 is used for prompt and response to keep the key compact
// and robust to embedded separators in the raw payloads.
func llmInteractionJoinKey(in *LLMInteraction) string {
	promptHash := sha256.Sum256([]byte(in.Prompt))
	respHash := sha256.Sum256([]byte(in.Response))
	return fmt.Sprintf("%s|%s|%d|%s|%d",
		hex.EncodeToString(promptHash[:]),
		hex.EncodeToString(respHash[:]),
		in.DurationMs,
		in.StepID,
		in.PhaseNumber,
	)
}
