package orchestration

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/truvaagents/truva-g3/telemetry"
)

// TestLLMDistiller_RecordDebugInteraction_StoreErrorLogged covers the error branch of
// recordDebugInteraction: a debug-store write failure is logged (best-effort, off the hot
// path) while the distillation still returns its output.
func TestLLMDistiller_RecordDebugInteraction_StoreErrorLogged(t *testing.T) {
	logger := &TestLogger{}
	mockAI := &countingAI{out: "DISTILLED OUTPUT"}
	config := ResultDistillConfig{
		Enabled: true, DistillThreshold: 10, PreFilterBudget: 32768, TargetSize: 4096,
		Model: "fast", ModelContextTokens: 150000, CompactionDeadline: 5 * time.Second,
	}
	distiller := NewLLMDistiller(mockAI, config, NewStructuralTrimmer(nil, nil), logger)
	distiller.SetLLMDebugStore(&mockLLMDebugStore{err: fmt.Errorf("store down")})

	// recordDebugInteraction only fires when a request_id is in baggage.
	ctx := telemetry.WithBaggage(context.Background(), "request_id", "req-1")
	out := distiller.ProcessForPrompt(ctx, strings.Repeat("x", 100), 4096,
		ResultProcessorContext{StepID: "s1", AgentName: "a", Instruction: "i"})
	distiller.Shutdown() // wait for the async debug-recording goroutine

	if out == "" {
		t.Fatal("distillation should still return output despite a debug-store error")
	}
	found := false
	for _, l := range logger.GetLogsByLevel("WARN") {
		if strings.Contains(l.Message, "Failed to record distillation debug interaction") {
			found = true
		}
	}
	if !found {
		t.Error("expected a WARN log when the debug store errors")
	}
}
