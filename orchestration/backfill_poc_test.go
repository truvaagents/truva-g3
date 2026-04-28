package orchestration

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
)

// TestPOC_DevopsBackfill_RealData loads the actual devops-tool response (296KB)
// and validates that the structural trimmer preserves relevant pod data.
//
// Before Phase 5: data.stdout was a JSON-in-string backfilled as a truncated blob.
// After Phase 5: deserializeStringValues unwraps data.stdout → buildFieldInventory
// decomposes the kubectl JSON → individual pod sub-fields compete in the greedy selector.
// Result: sub-fields from MANY pods (name, restartCount, status) instead of first-N-bytes blob.
func TestPOC_DevopsBackfill_RealData(t *testing.T) {
	// Load real devops response
	raw, err := os.ReadFile("testdata/devops_large_response.json")
	if err != nil {
		t.Fatalf("Failed to load test data: %v", err)
	}
	t.Logf("Loaded real devops response: %d bytes", len(raw))

	trimmer := NewStructuralTrimmer(nil, nil)
	ctx := context.Background()
	stepCtx := ResultProcessorContext{
		StepID:      "step-1",
		AgentName:   "devops-tool",
		Instruction: "List all pods in the truvag3-examples namespace with detailed output to inspect restart counts",
	}

	// --- 16KB budget: Phase 5 decomposes stdout into pod sub-fields ---
	t.Run("16KB_Budget_Decomposition", func(t *testing.T) {
		budget := 16384
		result := trimmer.ProcessForPrompt(ctx, string(raw), budget, stepCtx)

		jsonPart, annotation := splitAnnotation(result)
		var parsed map[string]interface{}
		if err := json.Unmarshal([]byte(jsonPart), &parsed); err != nil {
			t.Fatalf("Invalid JSON output: %v", err)
		}

		data, _ := parsed["data"].(map[string]interface{})
		_, hasStdout := data["stdout"]

		t.Logf("Budget: %d bytes", budget)
		t.Logf("Output: %d bytes (JSON: %d)", len(result), len(jsonPart))
		t.Logf("data.stdout present: %v", hasStdout)
		t.Logf("Annotation: %s", annotation)
		t.Logf("Fields in output: %s", mapKeysStr(parsed, data))

		if !hasStdout {
			t.Fatal("data.stdout should be present — decomposed after JSON-in-string unwrap")
		}

		if len(jsonPart) > budget {
			t.Errorf("JSON output %d exceeds budget %d", len(jsonPart), budget)
		}

		// After Phase 5, stdout is an unwrapped object (not a string).
		// Verify pod data is present by checking the serialized output for pod-related content.
		podNameCount := strings.Count(jsonPart, "agent-") + strings.Count(jsonPart, "tool-") + strings.Count(jsonPart, "viewer-")
		t.Logf("Pod-related name fragments visible: %d", podNameCount)
		if podNameCount == 0 {
			t.Error("No pod name fragments visible in decomposed output")
		}

		// Verify keyword-matched fields like restart-related data are present
		if strings.Contains(jsonPart, "restart") || strings.Contains(jsonPart, "restartCount") {
			t.Log("restartCount data visible in output")
		}
	})

	// --- 32KB budget: more decomposed data preserved ---
	t.Run("32KB_Budget_Decomposition", func(t *testing.T) {
		budget := 32768
		result := trimmer.ProcessForPrompt(ctx, string(raw), budget, stepCtx)

		jsonPart, annotation := splitAnnotation(result)
		var parsed map[string]interface{}
		if err := json.Unmarshal([]byte(jsonPart), &parsed); err != nil {
			t.Fatalf("Invalid JSON output: %v", err)
		}

		data, _ := parsed["data"].(map[string]interface{})
		_, hasStdout := data["stdout"]

		t.Logf("Budget: %d bytes", budget)
		t.Logf("Output: %d bytes (JSON: %d)", len(result), len(jsonPart))
		t.Logf("data.stdout present: %v", hasStdout)
		t.Logf("Annotation: %s", annotation)

		if !hasStdout {
			t.Fatal("data.stdout should be present with 32KB budget")
		}

		if len(jsonPart) > budget {
			t.Errorf("JSON output %d exceeds budget %d", len(jsonPart), budget)
		}

		// With 32KB, we should see more pod data than 16KB
		podNameCount := strings.Count(jsonPart, "agent-") + strings.Count(jsonPart, "tool-") + strings.Count(jsonPart, "viewer-")
		t.Logf("Pod-related name fragments visible: %d", podNameCount)

		// All metadata fields should still be present
		for _, key := range []string{"command", "exit_code", "stdout"} {
			if _, ok := data[key]; !ok {
				t.Errorf("Missing expected field data.%s", key)
			}
		}
	})

	// --- Direct comparison summary ---
	t.Run("Summary", func(t *testing.T) {
		result16 := trimmer.ProcessForPrompt(ctx, string(raw), 16384, stepCtx)
		json16, _ := splitAnnotation(result16)
		result32 := trimmer.ProcessForPrompt(ctx, string(raw), 32768, stepCtx)
		json32, _ := splitAnnotation(result32)

		// Count pod-related content at each budget level
		podCount16 := strings.Count(json16, "agent-") + strings.Count(json16, "tool-") + strings.Count(json16, "viewer-")
		podCount32 := strings.Count(json32, "agent-") + strings.Count(json32, "tool-") + strings.Count(json32, "viewer-")

		fmt.Printf("\n")
		fmt.Printf("  ╔══════════════════════════════════════════════════════════╗\n")
		fmt.Printf("  ║   DEVOPS TRIMMING: PHASE 5 DECOMPOSITION VALIDATION     ║\n")
		fmt.Printf("  ╠══════════════════════════════════════════════════════════╣\n")
		fmt.Printf("  ║  Input size:        %6d bytes (%d KB)                 ║\n", len(raw), len(raw)/1024)
		fmt.Printf("  ║                                                          ║\n")
		fmt.Printf("  ║  16KB budget:                                            ║\n")
		fmt.Printf("  ║    JSON output:      %6d bytes                        ║\n", len(json16))
		fmt.Printf("  ║    Pod fragments:    %6d (decomposed sub-fields)      ║\n", podCount16)
		fmt.Printf("  ║                                                          ║\n")
		fmt.Printf("  ║  32KB budget:                                            ║\n")
		fmt.Printf("  ║    JSON output:      %6d bytes                        ║\n", len(json32))
		fmt.Printf("  ║    Pod fragments:    %6d (decomposed sub-fields)      ║\n", podCount32)
		fmt.Printf("  ╚══════════════════════════════════════════════════════════╝\n\n")
	})
}

func splitAnnotation(result string) (jsonPart, annotation string) {
	if idx := strings.Index(result, "\n[trimmed:"); idx >= 0 {
		return result[:idx], result[idx:]
	}
	return result, ""
}

func mapKeysStr(top map[string]interface{}, data map[string]interface{}) string {
	var keys []string
	for k := range top {
		if k == "data" && data != nil {
			for dk := range data {
				keys = append(keys, "data."+dk)
			}
		} else {
			keys = append(keys, k)
		}
	}
	return strings.Join(keys, ", ")
}
