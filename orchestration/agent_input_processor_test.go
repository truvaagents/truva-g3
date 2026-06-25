package orchestration

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/truvaagents/truva-g3/telemetry"
)

// TestByteBudgetAgentInput_PassthroughGuards covers the nil-trimmer and zero-budget guards in
// byteBudgetAgentInputProcessor.ProcessInput — both must pass parameters through unchanged.
func TestByteBudgetAgentInput_PassthroughGuards(t *testing.T) {
	params := map[string]interface{}{
		"obj": map[string]interface{}{"big": strings.Repeat("z", 1000)},
	}
	stepCtx := ResultProcessorContext{StepID: "s1", AgentName: "a"}

	t.Run("nil trimmer is passthrough", func(t *testing.T) {
		p := NewByteBudgetAgentInputProcessor(nil, 100, nil)
		out, err := p.ProcessInput(context.Background(), params, stepCtx)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(out) != len(params) {
			t.Errorf("expected passthrough, got %d keys want %d", len(out), len(params))
		}
		// The oversized value must reach the output untouched (no trimming on the guard path).
		if obj, ok := out["obj"].(map[string]interface{}); !ok || len(obj["big"].(string)) != 1000 {
			t.Errorf("expected the oversized value to pass through unchanged, got %v", out["obj"])
		}
	})

	t.Run("zero budget is passthrough", func(t *testing.T) {
		p := NewByteBudgetAgentInputProcessor(NewStructuralTrimmer(nil, nil), 0, nil)
		out, err := p.ProcessInput(context.Background(), params, stepCtx)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(out) != len(params) {
			t.Errorf("expected passthrough, got %d keys want %d", len(out), len(params))
		}
	})
}

// TestByteBudgetAgentInput_TrimsOversizedParam exercises the full trim path: an oversized
// complex parameter is structurally trimmed back to valid JSON while scalars pass through
// untouched. Run with a request_id in baggage so the telemetry-correlation branch is covered.
func TestByteBudgetAgentInput_TrimsOversizedParam(t *testing.T) {
	big := make([]interface{}, 200)
	for i := range big {
		big[i] = map[string]interface{}{"k": fmt.Sprintf("value-%03d-with-padding", i)}
	}
	params := map[string]interface{}{
		"records": big,
		"scalar":  "left-alone",
	}
	p := NewByteBudgetAgentInputProcessor(NewStructuralTrimmer(nil, nil), 256, &TestLogger{})

	ctx := telemetry.WithBaggage(context.Background(), "request_id", "req-1")
	out, err := p.ProcessInput(ctx, params, ResultProcessorContext{StepID: "s1", AgentName: "a", Instruction: "find"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Scalar passes through untouched.
	if out["scalar"] != "left-alone" {
		t.Errorf("scalar param must pass through unchanged, got %v", out["scalar"])
	}

	// The oversized param remains valid JSON and is never larger than the original.
	trimmed, marshalErr := json.Marshal(out["records"])
	if marshalErr != nil {
		t.Fatalf("trimmed param is not valid JSON: %v", marshalErr)
	}
	orig, _ := json.Marshal(big)
	// Strictly smaller: the oversized param was actually trimmed (not silently left as-is via the
	// fail-open path). The structural trimmer keeps whole records up to the 256-byte budget.
	if len(trimmed) >= len(orig) {
		t.Errorf("expected the oversized param to be trimmed: trimmed=%d orig=%d", len(trimmed), len(orig))
	}
}
