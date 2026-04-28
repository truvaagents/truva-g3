package orchestration

import (
	"context"
	"testing"

	"github.com/truvaagents/truva-g3/telemetry"
)

// TestAIOrchestrator_DeferLLMRecording_GatesOnDebugStore is the Batch A
// DoD guard for BUG_LLM_INTERACTION_DOUBLE_RECORDING.md Layer 2. The
// helper must NOT mark ctx when debugStore is nil — otherwise
// InstrumentedAIClient would skip its own agent_llm_call emission while
// recordDebugInteraction quietly no-ops, and the call would vanish from
// observability entirely. This regression is the direct manifestation
// of the graceful-fallback invariant in orchestration/ARCHITECTURE.md
// ("Never fails orchestration if debug store fails").
func TestAIOrchestrator_DeferLLMRecording_GatesOnDebugStore(t *testing.T) {
	t.Run("nil debug store returns ctx unchanged", func(t *testing.T) {
		o := &AIOrchestrator{} // debugStore left nil
		ctx := context.Background()
		out := o.deferLLMRecordingIfWeWillRecord(ctx)
		if telemetry.IsLLMCallRecordingDeferred(out) {
			t.Fatal("orchestrator with nil debugStore must NOT set the deferral marker; the wrapper would then skip recording and the call would disappear")
		}
	})

	t.Run("configured debug store marks ctx for deferral", func(t *testing.T) {
		o := &AIOrchestrator{debugStore: &mockDebugStoreForRefinement{}}
		ctx := context.Background()
		out := o.deferLLMRecordingIfWeWillRecord(ctx)
		if !telemetry.IsLLMCallRecordingDeferred(out) {
			t.Fatal("orchestrator with debugStore set must mark ctx so InstrumentedAIClient skips its agent_llm_call emission; otherwise we get the double-record bug")
		}
	})
}
