package orchestration

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/truvaagents/truva-g3/core"
	"github.com/truvaagents/truva-g3/telemetry"
)

// --- TestDeserializeStringValues ---

func TestDeserializeStringValues_NestedJSONStrings(t *testing.T) {
	// Simulates the double-serialization bug: a string value that is valid JSON
	input := map[string]interface{}{
		"id":   "abc-123",
		"data": `{"price":150.5,"currency":"USD"}`,
	}

	result := deserializeStringValues(input)
	m := result.(map[string]interface{})

	dataVal, ok := m["data"].(map[string]interface{})
	if !ok {
		t.Fatalf("Expected data to be deserialized to map, got %T", m["data"])
	}
	// Numbers inside re-parsed JSON strings decode via unmarshalPreservingNumbers (UseNumber),
	// so they are json.Number (large-ID preservation), not float64.
	if dataVal["price"] != json.Number("150.5") {
		t.Errorf("Expected price=json.Number(150.5), got %v (%T)", dataVal["price"], dataVal["price"])
	}
	if dataVal["currency"] != "USD" {
		t.Errorf("Expected currency=USD, got %v", dataVal["currency"])
	}
	if m["id"] != "abc-123" {
		t.Errorf("Expected id unchanged, got %v", m["id"])
	}
}

func TestDeserializeStringValues_PlainTextPassthrough(t *testing.T) {
	input := map[string]interface{}{
		"message": "Hello, world!",
		"count":   42.0,
	}

	result := deserializeStringValues(input)
	m := result.(map[string]interface{})

	if m["message"] != "Hello, world!" {
		t.Errorf("Expected plain string unchanged")
	}
	if m["count"] != 42.0 {
		t.Errorf("Expected number unchanged")
	}
}

func TestDeserializeStringValues_DeeplyNested(t *testing.T) {
	input := map[string]interface{}{
		"outer": map[string]interface{}{
			"inner": `{"nested_key":"nested_val"}`,
		},
	}

	result := deserializeStringValues(input)
	outer := result.(map[string]interface{})["outer"].(map[string]interface{})
	inner, ok := outer["inner"].(map[string]interface{})
	if !ok {
		t.Fatalf("Expected deeply nested JSON string to be deserialized")
	}
	if inner["nested_key"] != "nested_val" {
		t.Errorf("Expected nested_key=nested_val, got %v", inner["nested_key"])
	}
}

func TestDeserializeStringValues_ArrayStringValues(t *testing.T) {
	input := []interface{}{
		`{"a":1}`,
		"plain",
		42.0,
	}

	result := deserializeStringValues(input)
	arr := result.([]interface{})

	if _, ok := arr[0].(map[string]interface{}); !ok {
		t.Errorf("Expected first array element deserialized to map, got %T", arr[0])
	}
	if arr[1] != "plain" {
		t.Errorf("Expected plain string unchanged")
	}
	if arr[2] != 42.0 {
		t.Errorf("Expected number unchanged")
	}
}

func TestDeserializeStringValues_ShortStringsSkipped(t *testing.T) {
	input := map[string]interface{}{
		"empty": "",
		"one":   "a",
		"two":   "{}",  // len == 2, NOT > 2, remains string
		"three": "[1]", // len == 3, IS > 2, starts with '[', valid JSON → deserialized
	}

	result := deserializeStringValues(input)
	m := result.(map[string]interface{})

	// "{}" has len=2 which is NOT > 2, so should remain a string
	if _, ok := m["two"].(string); !ok {
		t.Errorf("Expected '{}' (len=2) to remain string, got %T", m["two"])
	}
	// "[1]" has len=3 which IS > 2 and starts with '[', so it gets deserialized
	if arr, ok := m["three"].([]interface{}); !ok {
		t.Errorf("Expected '[1]' (len=3) to be deserialized to array, got %T", m["three"])
	} else if len(arr) != 1 || arr[0] != json.Number("1") {
		// json.Number, not float64: re-parsed via unmarshalPreservingNumbers (UseNumber).
		t.Errorf("Expected deserialized [1] as json.Number, got %v", arr)
	}
}

func TestDeserializeStringValues_ExportedVersion(t *testing.T) {
	input := map[string]interface{}{
		"data": `{"key":"val"}`,
	}
	result := DeserializeStringValues(input)
	m := result.(map[string]interface{})
	if _, ok := m["data"].(map[string]interface{}); !ok {
		t.Error("Expected exported version to work identically to unexported")
	}
}

func TestDeserializeStringValues_NonMapNonArrayTypes(t *testing.T) {
	// Non-string, non-map, non-array types should pass through unchanged
	if got := deserializeStringValues(42.0); got != 42.0 {
		t.Errorf("Expected float64 passthrough, got %v", got)
	}
	if got := deserializeStringValues(true); got != true {
		t.Errorf("Expected bool passthrough, got %v", got)
	}
	if got := deserializeStringValues(nil); got != nil {
		t.Errorf("Expected nil passthrough, got %v", got)
	}
}

// --- TestTruncateResultBytes ---

func TestTruncateResultBytes_UnderLimit(t *testing.T) {
	input := "short response"
	result := truncateResultBytes(input, 100)
	if result != input {
		t.Errorf("Expected no truncation, got %q", result)
	}
}

func TestTruncateResultBytes_ExactLimit(t *testing.T) {
	input := "exact"
	result := truncateResultBytes(input, 5)
	if result != input {
		t.Errorf("Expected no truncation at exact limit, got %q", result)
	}
}

func TestTruncateResultBytes_OverLimit(t *testing.T) {
	input := "This is a long response that should be truncated for the synthesis prompt"
	result := truncateResultBytes(input, 30)

	if len(result) > 30 {
		t.Errorf("Expected result to be at most 30 bytes, got %d bytes: %q", len(result), result)
	}
}

func TestTruncateResultBytes_VerySmallLimit(t *testing.T) {
	input := "Hello, world!"
	result := truncateResultBytes(input, 5)

	// With maxBytes=5, the annotation is longer than 5, so bare truncation
	if len(result) > 5 {
		t.Errorf("Expected bare truncation at 5 bytes, got %d bytes: %q", len(result), result)
	}
	if result != "Hello" {
		t.Errorf("Expected first 5 bytes 'Hello', got %q", result)
	}
}

func TestTruncateResultBytes_ZeroLimit(t *testing.T) {
	result := truncateResultBytes("something", 0)
	if result != "" {
		t.Errorf("Expected empty string for maxBytes=0, got %q", result)
	}
}

func TestTruncateResultBytes_ContainsAnnotation(t *testing.T) {
	input := "A moderately long response that definitely needs trimming to fit within the byte limit here"
	result := truncateResultBytes(input, 60)

	if len(result) > 60 {
		t.Errorf("Expected result <= 60 bytes, got %d", len(result))
	}
	if !strings.Contains(result, "[trimmed:") {
		t.Errorf("Expected trimmed annotation in result, got %q", result)
	}
}

func TestTruncateResultBytes_UTF8Boundary(t *testing.T) {
	// Each 🌍 is 4 bytes (F0 9F 8C 8D). 20 emojis = 80 bytes + "end" = 83 bytes.
	// With maxBytes=50, the cut lands mid-emoji, exercising the UTF-8 backup loop.
	input := strings.Repeat("🌍", 20) + "end"
	result := truncateResultBytes(input, 50)

	if len(result) > 50 {
		t.Errorf("Expected result <= 50 bytes, got %d", len(result))
	}
	// The content before the annotation must be valid UTF-8 (no split multi-byte chars)
	if !utf8.ValidString(result) {
		t.Errorf("Result contains broken UTF-8: %q", result)
	}
}

// --- TestExtractKeywords ---

func TestExtractKeywords_BasicInstruction(t *testing.T) {
	keywords := extractKeywords("Get the current stock price for AAPL")

	if len(keywords) == 0 {
		t.Fatal("Expected non-empty keywords")
	}

	found := make(map[string]bool)
	for _, kw := range keywords {
		found[kw] = true
	}

	if !found["stock"] {
		t.Errorf("Expected 'stock' in keywords, got %v", keywords)
	}
	if !found["price"] {
		t.Errorf("Expected 'price' in keywords, got %v", keywords)
	}
}

func TestExtractKeywords_StopWordRemoval(t *testing.T) {
	keywords := extractKeywords("get the current price for this item")

	for _, kw := range keywords {
		if kw == "the" || kw == "for" || kw == "get" || kw == "this" {
			t.Errorf("Stop word %q should have been removed", kw)
		}
	}
}

func TestExtractKeywords_EmptyInput(t *testing.T) {
	keywords := extractKeywords("")
	if len(keywords) != 0 {
		t.Errorf("Expected empty keywords for empty input, got %v", keywords)
	}
}

func TestExtractKeywords_ShortWordsFiltered(t *testing.T) {
	keywords := extractKeywords("do it or go")

	// All words are <= 2 chars or stop words
	if len(keywords) != 0 {
		t.Errorf("Expected empty keywords for all-short-word input, got %v", keywords)
	}
}

func TestExtractKeywords_StemmedAndOriginalBothPresent(t *testing.T) {
	keywords := extractKeywords("financial analysis pricing")

	found := make(map[string]bool)
	for _, kw := range keywords {
		found[kw] = true
	}

	// "financial" → strip "al" suffix → "financi", both "financi" and "financial" added
	if !found["financi"] && !found["financial"] {
		t.Errorf("Expected stemmed form of 'financial' in keywords, got %v", keywords)
	}
}

// --- TestBasicStem ---

func TestBasicStem_CommonSuffixes(t *testing.T) {
	tests := []struct {
		input, expected string
	}{
		{"running", "runn"},       // "ing" suffix
		{"prices", "pric"},        // "es" suffix
		{"actively", "active"},    // "ly" suffix
		{"connection", "connect"}, // "ion" suffix
		{"beautiful", "beauti"},   // "ful" suffix
		{"items", "item"},         // "s" suffix
	}

	for _, tt := range tests {
		got := basicStem(tt.input)
		if got != tt.expected {
			t.Errorf("basicStem(%q) = %q, want %q", tt.input, got, tt.expected)
		}
	}
}

func TestBasicStem_TooShortAfterStrip(t *testing.T) {
	// "bed" → "ed" suffix would leave "b" (len 1 < 3), so skip. Returns unchanged.
	got := basicStem("bed")
	if got != "bed" {
		t.Errorf("basicStem(\"bed\") = %q, want \"bed\"", got)
	}
}

// --- TestBudgetAllocator ---

func TestBudgetAllocator_AllSmall(t *testing.T) {
	ba := NewBudgetAllocator(10000, 5000)
	sizes := []int{1000, 2000, 3000}
	budgets := ba.Allocate(sizes)

	for i, size := range sizes {
		if budgets[i] != size {
			t.Errorf("Result %d: expected budget=%d, got %d", i, size, budgets[i])
		}
	}
}

func TestBudgetAllocator_ProportionalDistribution(t *testing.T) {
	ba := NewBudgetAllocator(10000, 5000)
	sizes := []int{100, 20000, 30000}

	budgets := ba.Allocate(sizes)

	if budgets[0] != 100 {
		t.Errorf("Small result: expected 100, got %d", budgets[0])
	}
	total := budgets[0] + budgets[1] + budgets[2]
	if total > 10000 {
		t.Errorf("Total budget %d exceeds totalBudget 10000", total)
	}
	if budgets[1] <= 0 || budgets[2] <= 0 {
		t.Errorf("Expected positive budgets for oversized results, got %v", budgets)
	}
}

func TestBudgetAllocator_SmallResultsExceedBudget(t *testing.T) {
	ba := NewBudgetAllocator(1000, 500)
	sizes := []int{400, 400, 400, 20000}

	budgets := ba.Allocate(sizes)

	// Phase 9: the small class (400*3 = 1200) over-subscribes the total (1000), so small budgets
	// scale down to fit; the raw-oversized result (20000) still gets 0 — it cannot be funded when
	// the small class already saturates the total.
	if budgets[3] != 0 {
		t.Errorf("Expected 0 budget for oversized when small results exceed total, got %d", budgets[3])
	}
	small := budgets[0] + budgets[1] + budgets[2]
	if small > 1000 {
		t.Errorf("Expected scaled small budgets to fit total 1000, got sum %d", small)
	}
	if budgets[0] <= 0 {
		t.Errorf("Expected small budgets scaled down (never zeroed), got %v", budgets)
	}
}

// fakeDistillProcessor is a ResultProcessor that also implements EffectiveSizer, modelling the
// distiller's post-distill footprint (Phase 9) without any LLM.
type fakeDistillProcessor struct {
	threshold  int
	targetSize int
}

func (f fakeDistillProcessor) ProcessForPrompt(_ context.Context, result string, maxBytes int, _ ResultProcessorContext) string {
	if maxBytes <= 0 || len(result) <= maxBytes {
		return result
	}
	return result[:maxBytes]
}

func (f fakeDistillProcessor) EffectiveSize(raw int) int {
	if raw < f.threshold {
		return raw
	}
	if f.targetSize > 0 && f.targetSize < raw {
		return f.targetSize
	}
	return raw
}

// TestBudgetAllocator_SmallClassDownScaled verifies the Phase 9 small-class down-scaling: when the
// small class alone over-subscribes the total, budgets scale proportionally to fit and none is zeroed.
func TestBudgetAllocator_SmallClassDownScaled(t *testing.T) {
	ba := NewBudgetAllocator(1000, 5000) // perResultMax high → all results are "small"
	sizes := []int{500, 500, 500, 500}   // sum 2000 > total 1000

	budgets := ba.Allocate(sizes)

	total := 0
	for i, b := range budgets {
		if b <= 0 {
			t.Errorf("budget[%d]=%d, expected > 0 (scaled, never zeroed)", i, b)
		}
		total += b
	}
	if total > 1000 {
		t.Errorf("scaled total %d exceeds totalBudget 1000", total)
	}
	if budgets[0] != budgets[1] || budgets[1] != budgets[2] || budgets[2] != budgets[3] {
		t.Errorf("expected equal scaled budgets for equal sizes, got %v", budgets)
	}
}

// TestProcessMultipleForBudget_EffectiveSizeAvoidsStarvation is the Phase 9 regression test: with an
// EffectiveSizer processor, large distill-eligible results occupy ~TargetSize and are never starved
// to budget 0, even when smaller results would otherwise saturate the total.
func TestProcessMultipleForBudget_EffectiveSizeAvoidsStarvation(t *testing.T) {
	steps := []StepResult{
		{StepID: "a", Response: strings.Repeat("x", 1900)},
		{StepID: "b", Response: strings.Repeat("x", 1900)},
		{StepID: "c", Response: strings.Repeat("x", 60000)},
		{StepID: "d", Response: strings.Repeat("x", 90000)},
	}
	const total, perResult = 4000, 2000

	dp := fakeDistillProcessor{threshold: 2000, targetSize: 500}
	_, meta := ProcessMultipleForBudget(context.Background(), dp, steps, total, perResult, "")

	for _, id := range []string{"a", "b", "c", "d"} {
		if meta[id] == nil || meta[id].BudgetAllocated <= 0 {
			t.Errorf("step %s starved under effective-size allocation: %+v", id, meta[id])
		}
	}

	// Contrast: a plain processor (no EffectiveSizer) budgets large results at raw size, so the
	// largest is starved — the pre-Phase-9 behavior this fix addresses. EffectiveSizer must do
	// strictly better for the largest result.
	_, plainMeta := ProcessMultipleForBudget(context.Background(), &captureProcessor{}, steps, total, perResult, "")
	if plainMeta["d"].BudgetAllocated >= meta["d"].BudgetAllocated {
		t.Errorf("expected effective-size allocation to beat raw for largest result: plain d=%d, effective d=%d",
			plainMeta["d"].BudgetAllocated, meta["d"].BudgetAllocated)
	}
}

// TestLLMDistiller_EffectiveSize verifies the post-distill footprint formula.
func TestLLMDistiller_EffectiveSize(t *testing.T) {
	d := NewLLMDistiller(nil, ResultDistillConfig{DistillThreshold: 16384, TargetSize: 4096}, NewStructuralTrimmer(nil, nil), nil)
	cases := []struct{ raw, want int }{
		{100, 100},      // below threshold → raw
		{16383, 16383},  // just below → raw
		{16384, 4096},   // at threshold → TargetSize
		{1000000, 4096}, // far above → TargetSize
	}
	for _, c := range cases {
		if got := d.EffectiveSize(c.raw); got != c.want {
			t.Errorf("EffectiveSize(%d)=%d, want %d", c.raw, got, c.want)
		}
	}
	// TargetSize 0 → never shrinks.
	d2 := NewLLMDistiller(nil, ResultDistillConfig{DistillThreshold: 16384, TargetSize: 0}, NewStructuralTrimmer(nil, nil), nil)
	if got := d2.EffectiveSize(1000000); got != 1000000 {
		t.Errorf("EffectiveSize with TargetSize=0 = %d, want 1000000", got)
	}
}

// TestCachingProcessor_EffectiveSize verifies the cache wrapper forwards EffectiveSize to its inner.
func TestCachingProcessor_EffectiveSize(t *testing.T) {
	d := NewLLMDistiller(nil, ResultDistillConfig{DistillThreshold: 16384, TargetSize: 4096}, NewStructuralTrimmer(nil, nil), nil)
	cp := NewCachingProcessor(d, &core.MockDigestCache{}, time.Minute, 16384, "salt", nil)

	sizer, ok := cp.(EffectiveSizer)
	if !ok {
		t.Fatal("cachingProcessor should implement EffectiveSizer")
	}
	if got := sizer.EffectiveSize(1000000); got != 4096 {
		t.Errorf("forwarded EffectiveSize = %d, want 4096", got)
	}
}

// TestLLMDistiller_PromptIncludesUserGoal verifies the original user query is threaded into the
// distillation prompt as the primary relevance signal, and omitted when no query is in scope.
func TestLLMDistiller_PromptIncludesUserGoal(t *testing.T) {
	d := NewLLMDistiller(nil, ResultDistillConfig{}, NewStructuralTrimmer(nil, nil), nil)

	stepCtx := ResultProcessorContext{
		AgentName:     "devops-tool",
		Instruction:   "Retrieve the last 5 minutes of logs",
		OriginalQuery: "tell me if there are PII present in the logs",
	}
	prompt := d.buildDistillationPrompt("some data", 4096, stepCtx, 1.0)

	if !strings.Contains(prompt, "User goal: tell me if there are PII present in the logs") {
		t.Errorf("expected the user goal in the prompt, got:\n%s", prompt)
	}
	if !strings.Contains(prompt, "Downstream task: Retrieve the last 5 minutes of logs") {
		t.Error("expected the downstream task (step instruction) in the prompt")
	}
	// Instruction #3 lives in the system message (Phase 13 §2.9 split), not the per-call user prompt.
	if !strings.Contains(distillationSystemPrompt, "Select the units relevant to the downstream task") ||
		!strings.Contains(distillationSystemPrompt, "using the user goal as the broader") {
		t.Error("expected system-prompt instruction #3 to be task-primary with the user goal as broader intent (Phase 13)")
	}

	// When no query is in scope, the "User goal:" line is omitted (no empty label).
	noQuery := d.buildDistillationPrompt("data", 4096, ResultProcessorContext{Instruction: "x"}, 1.0)
	if strings.Contains(noQuery, "User goal:") {
		t.Error("expected no 'User goal:' line when OriginalQuery is empty")
	}
}

// TestLLMDistiller_PromptTaskPrimary verifies Phase 13: the downstream task LEADS the <context>
// block (before the user goal) and is echoed verbatim on the final dual-anchor line.
func TestLLMDistiller_PromptTaskPrimary(t *testing.T) {
	d := NewLLMDistiller(nil, ResultDistillConfig{}, NewStructuralTrimmer(nil, nil), nil)

	stepCtx := ResultProcessorContext{
		AgentName:     "prometheus-query-tool",
		Instruction:   "Query container memory usage for all pods to detect OOM pressure",
		OriginalQuery: "Perform a full cluster health check and send a Slack report",
	}
	prompt := d.buildDistillationPrompt("some data", 4096, stepCtx, 1.0)

	taskIdx := strings.Index(prompt, "Downstream task: Query container memory usage")
	goalIdx := strings.Index(prompt, "User goal: Perform a full cluster health check")
	if taskIdx < 0 || goalIdx < 0 {
		t.Fatalf("expected both the task and goal lines, got:\n%s", prompt)
	}
	if taskIdx > goalIdx {
		t.Error("expected the downstream task to LEAD the user goal in <context> (task-primary)")
	}
	// Dual-anchor: the final line echoes the task verbatim (quoted).
	wantAnchor := `Return the compacted result for "Query container memory usage for all pods to detect OOM pressure" (at most 4096 characters):`
	if !strings.Contains(prompt, wantAnchor) {
		t.Errorf("expected the final line to dual-anchor the task verbatim, got:\n%s", prompt)
	}
}

// TestLLMDistiller_PromptEmptyInstruction verifies Phase 13's empty-guard: an empty Instruction
// emits no dangling "Downstream task:" line and the final anchor falls back to a generic phrase.
func TestLLMDistiller_PromptEmptyInstruction(t *testing.T) {
	d := NewLLMDistiller(nil, ResultDistillConfig{}, NewStructuralTrimmer(nil, nil), nil)

	prompt := d.buildDistillationPrompt("data", 4096, ResultProcessorContext{AgentName: "tool"}, 1.0)

	if strings.Contains(prompt, "Downstream task:") {
		t.Errorf("expected no 'Downstream task:' line when Instruction is empty, got:\n%s", prompt)
	}
	if !strings.Contains(prompt, "Return the compacted result for the downstream task (at most 4096 characters):") {
		t.Errorf("expected the generic anchor when Instruction is empty, got:\n%s", prompt)
	}
}

// TestLLMDistiller_SystemPromptSplit verifies Phase 13 §2.9: identity + rules are dispatched as the
// system message, and the per-call user prompt carries only <context>/<data> + the dual-anchored
// final line (not the identity/instructions blocks).
func TestLLMDistiller_SystemPromptSplit(t *testing.T) {
	mockAI := &distillerMockAI{
		response: &core.AIResponse{Content: "distilled", Usage: core.TokenUsage{}},
	}
	config := ResultDistillConfig{Enabled: true, DistillThreshold: 10, PreFilterBudget: 500, TargetSize: 200}
	distiller := NewLLMDistiller(mockAI, config, NewStructuralTrimmer(nil, nil), nil)

	largeInput := `{"field1":"value1","field2":"value2","field3":"value3"}`
	distiller.ProcessForPrompt(context.Background(), largeInput, 500, ResultProcessorContext{
		StepID: "step-1", AgentName: "test-agent", Instruction: "summarize",
	})

	if mockAI.opts == nil || mockAI.opts.SystemPrompt != distillationSystemPrompt {
		t.Fatalf("expected the distillation system prompt to be dispatched as SystemPrompt")
	}
	// The user message carries context/data + the dual-anchor, not the identity/instructions blocks.
	if !strings.Contains(mockAI.prompt, "<context") || !strings.Contains(mockAI.prompt, "<data>") {
		t.Errorf("expected the user message to contain <context> and <data>, got:\n%s", mockAI.prompt)
	}
	if strings.Contains(mockAI.prompt, "<identity>") || strings.Contains(mockAI.prompt, "<instructions>") {
		t.Errorf("expected identity/instructions in the system message, not the user message:\n%s", mockAI.prompt)
	}
}

func TestBudgetAllocator_TotalNeverExceeded(t *testing.T) {
	ba := NewBudgetAllocator(5000, 3000)
	sizes := []int{100, 50000, 80000, 60000}

	budgets := ba.Allocate(sizes)

	total := 0
	for _, b := range budgets {
		total += b
	}
	if total > 5000 {
		t.Errorf("Total allocated %d exceeds totalBudget 5000 (budgets: %v)", total, budgets)
	}
}

func TestBudgetAllocator_MinFloorWithSufficientBudget(t *testing.T) {
	ba := NewBudgetAllocator(20000, 10000)
	sizes := []int{100, 10001}

	budgets := ba.Allocate(sizes)

	if budgets[1] < 512 {
		t.Errorf("Expected at least 512 floor, got %d", budgets[1])
	}
}

func TestBudgetAllocator_MinFloorWithInsufficientBudget(t *testing.T) {
	// Two oversized results sharing 600 bytes remaining.
	// First gets floor=512, second gets the rest (88).
	ba := NewBudgetAllocator(600, 5000)
	sizes := []int{0, 50000, 50000}

	budgets := ba.Allocate(sizes)

	// First oversized: proportional=300 < 512, remaining=600 >= 512 → floor to 512
	if budgets[1] != 512 {
		t.Errorf("Expected first oversized to get 512 floor, got %d", budgets[1])
	}
	// Second oversized: proportional=300 < 512, remaining-used=88 < 512 → gets remainder
	if budgets[2] != 88 {
		t.Errorf("Expected second oversized to get remaining 88, got %d", budgets[2])
	}
	total := budgets[0] + budgets[1] + budgets[2]
	if total > 600 {
		t.Errorf("Total %d exceeds totalBudget 600", total)
	}
}

func TestBudgetAllocator_PerResultMaxCap(t *testing.T) {
	// Single oversized result with proportional share exceeding perResultMax.
	ba := NewBudgetAllocator(10000, 3000)
	sizes := []int{0, 100000}

	budgets := ba.Allocate(sizes)

	if budgets[1] > 3000 {
		t.Errorf("Expected capped at perResultMax 3000, got %d", budgets[1])
	}
	if budgets[1] != 3000 {
		t.Errorf("Expected exactly 3000, got %d", budgets[1])
	}
}

func TestBudgetAllocator_OverflowClamp(t *testing.T) {
	// First oversized gets floor=512, second gets proportional=1800 capped to 1500,
	// but 512+1500=2012 > 2000, so second is clamped to 2000-512=1488.
	ba := NewBudgetAllocator(2000, 1500)
	sizes := []int{0, 10000, 90000}

	budgets := ba.Allocate(sizes)

	if budgets[1] != 512 {
		t.Errorf("Expected idx 1 = 512 (min floor), got %d", budgets[1])
	}
	if budgets[2] != 1488 {
		t.Errorf("Expected idx 2 = 1488 (overflow clamped), got %d", budgets[2])
	}
	total := budgets[0] + budgets[1] + budgets[2]
	if total > 2000 {
		t.Errorf("Total %d exceeds totalBudget 2000", total)
	}
}

// --- Phase 3 Redistribution Tests ---

func TestBudgetAllocator_RedistributionFromClamping(t *testing.T) {
	// Production case: sizes [596039, 32350], budget 32768, cap 16384.
	// Step-1 proportional=31082 → clamped to 16384 (saves 14698).
	// Step-2 proportional=1686 → with redistribution gets 1686+14698=16384.
	// Both should hit cap. Total = 32768 (zero waste).
	ba := NewBudgetAllocator(32768, 16384)
	sizes := []int{596039, 32350}

	budgets := ba.Allocate(sizes)

	if budgets[0] != 16384 {
		t.Errorf("Expected idx 0 = 16384 (capped), got %d", budgets[0])
	}
	if budgets[1] != 16384 {
		t.Errorf("Expected idx 1 = 16384 (after redistribution), got %d", budgets[1])
	}
	total := budgets[0] + budgets[1]
	if total != 32768 {
		t.Errorf("Expected total = 32768 (zero waste), got %d", total)
	}
}

func TestBudgetAllocator_RedistributionPartialCap(t *testing.T) {
	// 3 oversized [500000, 50000, 50000], budget 2500, cap 1000.
	// Proportional: idx0=2083, idx1=208, idx2=208.
	// Phase 2: idx0 clamped to 1000, idx1 gets 512 floor, idx2 gets remainder.
	// Phase 3: redistribute savings to idx1 and idx2 up to cap.
	ba := NewBudgetAllocator(2500, 1000)
	sizes := []int{500000, 50000, 50000}

	budgets := ba.Allocate(sizes)

	if budgets[0] != 1000 {
		t.Errorf("Expected idx 0 = 1000 (capped), got %d", budgets[0])
	}
	total := budgets[0] + budgets[1] + budgets[2]
	if total != 2500 {
		t.Errorf("Expected total = 2500, got %d", total)
	}
	if budgets[1] > 1000 || budgets[2] > 1000 {
		t.Errorf("No budget should exceed cap 1000: got [%d, %d, %d]", budgets[0], budgets[1], budgets[2])
	}
	// idx1 and idx2 should share remaining 1500 equally → 750 each
	if budgets[1] != 750 || budgets[2] != 750 {
		t.Errorf("Expected idx1=750, idx2=750 after redistribution, got [%d, %d]", budgets[1], budgets[2])
	}
}

func TestBudgetAllocator_RedistributionMultiPass(t *testing.T) {
	// 3 oversized [200000, 50000, 10000], budget 6000, cap 2000.
	// Phase 2: idx0 capped→2000, idx1 proportional→1153, idx2 floor→512. budgetUsed=3665.
	// Phase 3 pass 1: leftover=2335, eligible=[idx1(room=847), idx2(room=1488)].
	//   Equal share=1167 each. idx1 clamped to room=847→2000. idx2 gets 1167→1679.
	//   distributed=2014. budgetUsed=5679.
	// Phase 3 pass 2: leftover=321, eligible=[idx2(room=321)].
	//   idx2 gets 321→2000. budgetUsed=6000.
	// All three hit cap. Total = 6000 (zero waste).
	ba := NewBudgetAllocator(6000, 2000)
	sizes := []int{200000, 50000, 10000}

	budgets := ba.Allocate(sizes)

	if budgets[0] != 2000 {
		t.Errorf("Expected idx 0 = 2000 (capped in Phase 2), got %d", budgets[0])
	}
	if budgets[1] != 2000 {
		t.Errorf("Expected idx 1 = 2000 (capped in Phase 3 pass 1), got %d", budgets[1])
	}
	if budgets[2] != 2000 {
		t.Errorf("Expected idx 2 = 2000 (capped in Phase 3 pass 2), got %d", budgets[2])
	}
	total := budgets[0] + budgets[1] + budgets[2]
	if total != 6000 {
		t.Errorf("Expected total = 6000 (zero waste), got %d", total)
	}
}

func TestBudgetAllocator_RedistributionWithSmallResults(t *testing.T) {
	// Mixed [100, 20000, 30000], budget 10000, cap 5000.
	// idx0 is small (100 < 5000) → gets 100, remaining=9900.
	// Phase 2: idx1 proportional=3960, idx2 proportional=5940 → clamped to 5000.
	// Phase 3: 940 leftover → idx1 gets 940 → total idx1=4900.
	// Total = 100 + 4900 + 5000 = 10000.
	ba := NewBudgetAllocator(10000, 5000)
	sizes := []int{100, 20000, 30000}

	budgets := ba.Allocate(sizes)

	if budgets[0] != 100 {
		t.Errorf("Expected idx 0 = 100 (small passthrough), got %d", budgets[0])
	}
	if budgets[2] != 5000 {
		t.Errorf("Expected idx 2 = 5000 (capped), got %d", budgets[2])
	}
	if budgets[1] != 4900 {
		t.Errorf("Expected idx 1 = 4900 (after redistribution), got %d", budgets[1])
	}
	total := budgets[0] + budgets[1] + budgets[2]
	if total != 10000 {
		t.Errorf("Expected total = 10000 (zero waste), got %d", total)
	}
}

// --- TestProcessMultipleForBudget ---

func TestProcessMultipleForBudget_SmallResultsPassthrough(t *testing.T) {
	trimmer := NewStructuralTrimmer(nil, nil)

	steps := []StepResult{
		{StepID: "step-1", AgentName: "agent-a", Instruction: "Get data", Response: `{"key": "value"}`},
		{StepID: "step-2", AgentName: "agent-b", Instruction: "Get more", Response: `{"another": "field"}`},
	}

	results, _ := ProcessMultipleForBudget(context.Background(), trimmer, steps, 10000, 5000, "")

	if len(results) != 2 {
		t.Fatalf("Expected 2 results, got %d", len(results))
	}
	// Small responses should pass through unchanged
	if results[0] != `{"key": "value"}` {
		t.Errorf("Expected passthrough for small result 0, got %q", results[0])
	}
	if results[1] != `{"another": "field"}` {
		t.Errorf("Expected passthrough for small result 1, got %q", results[1])
	}
}

func TestProcessMultipleForBudget_BudgetConstraint(t *testing.T) {
	trimmer := NewStructuralTrimmer(nil, nil)

	// Build a large JSON response (~500 bytes each)
	largeVal := strings.Repeat("x", 200)
	largeResponse := fmt.Sprintf(`{"field_a":"%s","field_b":"%s"}`, largeVal, largeVal)

	steps := []StepResult{
		{StepID: "step-1", AgentName: "agent-a", Instruction: "analyze field_a", Response: largeResponse},
		{StepID: "step-2", AgentName: "agent-b", Instruction: "analyze field_b", Response: largeResponse},
	}

	// Total budget smaller than combined result sizes forces trimming
	totalBudget := 300
	results, _ := ProcessMultipleForBudget(context.Background(), trimmer, steps, totalBudget, 200, "")

	if len(results) != 2 {
		t.Fatalf("Expected 2 results, got %d", len(results))
	}
	// Each result should be trimmed to fit within allocated budget
	for i, r := range results {
		if r == "" {
			t.Errorf("Result %d is empty", i)
		}
		if r == largeResponse {
			t.Errorf("Result %d should have been trimmed but was unchanged", i)
		}
	}
}

// --- TestLLMDistiller ---

// distillerMockAI is a mock AI client for LLMDistiller tests
type distillerMockAI struct {
	response *core.AIResponse
	err      error
	called   bool
	prompt   string
	opts     *core.AIOptions
}

func (m *distillerMockAI) GenerateResponse(ctx context.Context, prompt string, opts *core.AIOptions) (*core.AIResponse, error) {
	m.called = true
	m.prompt = prompt
	m.opts = opts
	if m.err != nil {
		return nil, m.err
	}
	return m.response, nil
}

func (m *distillerMockAI) StreamResponse(ctx context.Context, prompt string, opts *core.AIOptions, callback func(chunk string)) (*core.AIResponse, error) {
	return m.GenerateResponse(ctx, prompt, opts)
}

func TestLLMDistiller_BelowThreshold(t *testing.T) {
	mockAI := &distillerMockAI{}
	trimmer := NewStructuralTrimmer(nil, nil)
	config := ResultDistillConfig{
		Enabled:          true,
		DistillThreshold: 1000,
		PreFilterBudget:  500,
		TargetSize:       200,
	}
	distiller := NewLLMDistiller(mockAI, config, trimmer, nil)

	result := distiller.ProcessForPrompt(context.Background(), "short input", 500, ResultProcessorContext{
		StepID: "step-1", AgentName: "test", Instruction: "test instruction",
	})

	if mockAI.called {
		t.Error("LLM should NOT be called when input is below threshold")
	}
	if result != "short input" {
		t.Errorf("Expected passthrough for small input, got %q", result)
	}
}

func TestLLMDistiller_TwoStagePipeline(t *testing.T) {
	mockAI := &distillerMockAI{
		response: &core.AIResponse{
			Content: `{"summary": "distilled data"}`,
			Usage:   core.TokenUsage{PromptTokens: 100, CompletionTokens: 50},
		},
	}
	trimmer := NewStructuralTrimmer(nil, nil)
	config := ResultDistillConfig{
		Enabled:          true,
		DistillThreshold: 10,
		PreFilterBudget:  500,
		TargetSize:       200,
	}
	distiller := NewLLMDistiller(mockAI, config, trimmer, nil)

	largeInput := `{"field1":"value1","field2":"value2","field3":"value3"}`
	stepCtx := ResultProcessorContext{
		StepID: "step-1", AgentName: "test-agent", Instruction: "summarize data",
	}
	result := distiller.ProcessForPrompt(context.Background(), largeInput, 500, stepCtx)

	if !mockAI.called {
		t.Error("LLM should be called when input exceeds threshold")
	}
	if result != `{"summary": "distilled data"}` {
		t.Errorf("Expected LLM response content, got %q", result)
	}
	// Verify prompt contains the instruction and data (stage 1 pre-filtered then sent to LLM)
	if !strings.Contains(mockAI.prompt, "summarize data") {
		t.Errorf("Expected prompt to contain instruction 'summarize data', got: %.200s", mockAI.prompt)
	}
	if !strings.Contains(mockAI.prompt, "test-agent") {
		t.Errorf("Expected prompt to contain agent name 'test-agent', got: %.200s", mockAI.prompt)
	}
}

func TestLLMDistiller_AIOptionsOverrideModelWinsOverConfigModel(t *testing.T) {
	mockAI := &distillerMockAI{
		response: &core.AIResponse{
			Content: `{"summary": "distilled data"}`,
		},
	}
	trimmer := NewStructuralTrimmer(nil, nil)
	config := ResultDistillConfig{
		Enabled:          true,
		DistillThreshold: 10,
		PreFilterBudget:  500,
		TargetSize:       200,
		Model:            "config-model",
	}
	distiller := NewLLMDistiller(mockAI, config, trimmer, nil)
	distiller.SetAIOptionsOverride(&AIOptionsOverride{
		Model: StringPtr("override-model"),
	})

	result := distiller.ProcessForPrompt(context.Background(), `{"field":"value"}`, 500, ResultProcessorContext{
		StepID: "step-1", AgentName: "test-agent", Instruction: "summarize data",
	})

	if result != `{"summary": "distilled data"}` {
		t.Fatalf("expected distiller to return LLM response, got %q", result)
	}
	if mockAI.opts == nil {
		t.Fatal("expected distiller to call AI client with options")
	}
	if mockAI.opts.Model != "override-model" {
		t.Fatalf("expected AI options override model to win, got %q", mockAI.opts.Model)
	}
}

func TestLLMDistiller_FallbackOnError(t *testing.T) {
	mockAI := &distillerMockAI{
		err: fmt.Errorf("LLM service unavailable"),
	}
	trimmer := NewStructuralTrimmer(nil, nil)
	config := ResultDistillConfig{
		Enabled:          true,
		DistillThreshold: 10,
		PreFilterBudget:  500,
		TargetSize:       200,
	}
	distiller := NewLLMDistiller(mockAI, config, trimmer, nil)

	largeInput := `{"field1":"value1","field2":"value2","field3":"value3"}`
	stepCtx := ResultProcessorContext{
		StepID: "step-1", AgentName: "test", Instruction: "test",
	}
	result := distiller.ProcessForPrompt(context.Background(), largeInput, 500, stepCtx)

	if !mockAI.called {
		t.Error("LLM should have been called")
	}
	// Should fall back to pre-filter result — verify it matches what the trimmer would produce
	expected := trimmer.ProcessForPrompt(context.Background(), largeInput, 500, stepCtx)
	if result != expected {
		t.Errorf("Expected fallback to match pre-filter output.\nGot:      %q\nExpected: %q", result, expected)
	}
}

func TestLLMDistiller_FallbackOnError_WithLogger(t *testing.T) {
	mockAI := &distillerMockAI{
		err: fmt.Errorf("LLM service unavailable"),
	}
	logger := &mockLogger{}
	trimmer := NewStructuralTrimmer(nil, nil)
	config := ResultDistillConfig{
		Enabled:          true,
		DistillThreshold: 10,
		PreFilterBudget:  500,
		TargetSize:       200,
	}
	distiller := NewLLMDistiller(mockAI, config, trimmer, logger)

	largeInput := `{"field1":"value1","field2":"value2","field3":"value3"}`
	distiller.ProcessForPrompt(context.Background(), largeInput, 500, ResultProcessorContext{
		StepID: "step-1", AgentName: "test", Instruction: "test",
	})

	// Logger should have recorded the warning
	foundWarn := false
	for _, msg := range logger.messages {
		if strings.Contains(msg, "distillation failed") {
			foundWarn = true
			break
		}
	}
	if !foundWarn {
		t.Errorf("Expected logger warning about distillation failure, got messages: %v", logger.messages)
	}
}

func TestLLMDistiller_Success_WithLogger(t *testing.T) {
	mockAI := &distillerMockAI{
		response: &core.AIResponse{
			Content: "distilled",
			Usage:   core.TokenUsage{PromptTokens: 50, CompletionTokens: 10},
		},
	}
	logger := &mockLogger{}
	trimmer := NewStructuralTrimmer(nil, nil)
	config := ResultDistillConfig{
		Enabled:          true,
		DistillThreshold: 10,
		PreFilterBudget:  500,
		TargetSize:       200,
	}
	distiller := NewLLMDistiller(mockAI, config, trimmer, logger)

	largeInput := `{"field1":"value1","field2":"value2","field3":"value3"}`
	distiller.ProcessForPrompt(context.Background(), largeInput, 500, ResultProcessorContext{
		StepID: "step-1", AgentName: "test", Instruction: "test",
	})

	foundDebug := false
	for _, msg := range logger.messages {
		if strings.Contains(msg, "distillation completed") {
			foundDebug = true
			break
		}
	}
	if !foundDebug {
		t.Errorf("Expected logger debug about distillation completed, got messages: %v", logger.messages)
	}
}

func TestLLMDistiller_MaxBytesLessThanTargetSize(t *testing.T) {
	mockAI := &distillerMockAI{
		response: &core.AIResponse{
			Content: `{"summary": "small"}`,
			Usage:   core.TokenUsage{PromptTokens: 50, CompletionTokens: 20},
		},
	}
	trimmer := NewStructuralTrimmer(nil, nil)
	config := ResultDistillConfig{
		Enabled:          true,
		DistillThreshold: 10,
		PreFilterBudget:  500,
		TargetSize:       500, // Larger than maxBytes
	}
	distiller := NewLLMDistiller(mockAI, config, trimmer, nil)

	largeInput := `{"field1":"value1","field2":"value2","field3":"value3"}`
	// 300 is below TargetSize (500) but above minDistillTargetSize (256), so it is honored.
	distiller.ProcessForPrompt(context.Background(), largeInput, 300, ResultProcessorContext{
		StepID: "step-1", AgentName: "test", Instruction: "summarize",
	})

	// Verify the prompt uses maxBytes (300) as target, not config.TargetSize (500)
	if !strings.Contains(mockAI.prompt, "at most 300 characters") {
		t.Errorf("Expected prompt to use maxBytes target '300', prompt: %.300s", mockAI.prompt)
	}
}

func TestLLMDistiller_CustomModel(t *testing.T) {
	mockAI := &distillerMockAI{
		response: &core.AIResponse{
			Content: "distilled",
			Usage:   core.TokenUsage{PromptTokens: 50, CompletionTokens: 10},
		},
	}
	trimmer := NewStructuralTrimmer(nil, nil)
	config := ResultDistillConfig{
		Enabled:          true,
		DistillThreshold: 10,
		PreFilterBudget:  500,
		TargetSize:       200,
		Model:            "gpt-4o-mini",
	}
	distiller := NewLLMDistiller(mockAI, config, trimmer, nil)

	largeInput := `{"field1":"value1","field2":"value2"}`
	distiller.ProcessForPrompt(context.Background(), largeInput, 500, ResultProcessorContext{
		StepID: "step-1", AgentName: "test", Instruction: "test",
	})

	if mockAI.opts == nil {
		t.Fatal("Expected options to be passed to AI client")
	}
	if mockAI.opts.Model != "gpt-4o-mini" {
		t.Errorf("Expected model 'gpt-4o-mini', got %q", mockAI.opts.Model)
	}
}

func TestLLMDistiller_BuildDistillationPrompt_WithCapability(t *testing.T) {
	mockAI := &distillerMockAI{
		response: &core.AIResponse{
			Content: "distilled",
			Usage:   core.TokenUsage{},
		},
	}
	trimmer := NewStructuralTrimmer(nil, nil)
	config := ResultDistillConfig{
		Enabled:          true,
		DistillThreshold: 10,
		PreFilterBudget:  500,
		TargetSize:       200,
	}
	distiller := NewLLMDistiller(mockAI, config, trimmer, nil)

	largeInput := `{"field1":"value1","field2":"value2"}`
	distiller.ProcessForPrompt(context.Background(), largeInput, 500, ResultProcessorContext{
		StepID:      "step-1",
		AgentName:   "finance-agent",
		Capability:  "basic_financials",
		Instruction: "get stock data",
	})

	// Verify capability appears in prompt
	if !strings.Contains(mockAI.prompt, "basic_financials") {
		t.Errorf("Expected prompt to contain capability 'basic_financials', prompt: %.300s", mockAI.prompt)
	}
}

func TestLLMDistiller_BuildDistillationPrompt_NoCapability(t *testing.T) {
	mockAI := &distillerMockAI{
		response: &core.AIResponse{
			Content: "distilled",
			Usage:   core.TokenUsage{},
		},
	}
	trimmer := NewStructuralTrimmer(nil, nil)
	config := ResultDistillConfig{
		Enabled:          true,
		DistillThreshold: 10,
		PreFilterBudget:  500,
		TargetSize:       200,
	}
	distiller := NewLLMDistiller(mockAI, config, trimmer, nil)

	largeInput := `{"field1":"value1","field2":"value2"}`
	distiller.ProcessForPrompt(context.Background(), largeInput, 500, ResultProcessorContext{
		StepID:      "step-1",
		AgentName:   "test-agent",
		Instruction: "test",
	})

	// Without capability, the <context> tag should carry no capability attribute.
	if strings.Contains(mockAI.prompt, "capability=") {
		t.Errorf("Expected no capability attribute when Capability is empty, prompt: %.300s", mockAI.prompt)
	}
}

// TestBuildDistillationPrompt guards the fmt.Sprintf argument ordering in the per-call USER
// message built by buildDistillationPrompt. After the Phase 13 §2.9 split, identity +
// instructions are the system message (distillationSystemPrompt) and the user-message layout is:
//
//	<context source/capability>(task, goal) -> <data>(result) -> final line(task anchor + maxBytes)
//
// Arg order: AgentName, capabilityAttr, downstreamTaskLine, userGoalLine, result, taskAnchor, maxBytes
func TestBuildDistillationPrompt(t *testing.T) {
	d := &LLMDistiller{}
	stepCtx := ResultProcessorContext{
		StepID:      "step-2",
		AgentName:   "stock-agent",
		Capability:  "get_stock_price",
		Instruction: "Fetch latest equity price for portfolio analysis",
	}
	// Use values that don't overlap with any other field to ensure positional checks are unambiguous
	result := `{"symbol":"AAPL","price":185.42,"exchange":"NASDAQ"}`
	maxBytes := 4096

	prompt := d.buildDistillationPrompt(result, maxBytes, stepCtx, 1.0)

	// --- 1. No fmt.Sprintf type-mismatch garbage ---
	if strings.Contains(prompt, "%!") {
		t.Fatalf("Prompt contains fmt.Sprintf type-mismatch indicator (%%!), "+
			"which means arguments are in the wrong order:\n%s", prompt)
	}

	// --- 2. Identity + instructions are NOT in the user message (they are the system message) ---
	if strings.Contains(prompt, "<identity>") || strings.Contains(prompt, "<instructions>") {
		t.Fatalf("user message must not contain identity/instructions (Phase 13 §2.9):\n%s", prompt)
	}

	// --- 3. All values are present ---
	required := map[string]string{
		"maxBytes":        "4096",
		"AgentName":       "stock-agent",
		"Capability":      "get_stock_price",
		"Instruction":     "Fetch latest equity price for portfolio analysis",
		"result/symbol":   "AAPL",
		"result/price":    "185.42",
		"result/exchange": "NASDAQ",
	}
	for label, val := range required {
		if !strings.Contains(prompt, val) {
			t.Fatalf("Expected %s value %q in prompt, got:\n%s", label, val, prompt)
		}
	}

	// --- 4. Positional correctness: <context> -> <data> -> final line(maxBytes) ---
	contextSection := strings.Index(prompt, "<context")
	dataSection := strings.Index(prompt, "<data>")
	if contextSection < 0 || dataSection < 0 {
		t.Fatalf("Could not locate <context>/<data> headers in prompt:\n%s", prompt)
	}
	if contextSection >= dataSection {
		t.Errorf("Section ordering violated: context@%d must precede data@%d", contextSection, dataSection)
	}

	agentIdx := strings.Index(prompt, "stock-agent")
	capIdx := strings.Index(prompt, "get_stock_price")
	instrIdx := strings.Index(prompt, "Fetch latest equity price for portfolio analysis") // first hit: in <context>
	resultIdx := strings.Index(prompt, "AAPL")
	maxBytesIdx := strings.LastIndex(prompt, "4096") // the budget lives on the final line

	// 4a. Source attrs (agent, capability) and the downstream task lead <context>, before <data>.
	if agentIdx < contextSection || agentIdx > dataSection {
		t.Errorf("AgentName should appear inside <context>: context@%d agent@%d data@%d", contextSection, agentIdx, dataSection)
	}
	if capIdx < agentIdx || capIdx > dataSection {
		t.Errorf("Capability should appear after AgentName and before <data>: agent@%d cap@%d data@%d", agentIdx, capIdx, dataSection)
	}
	if instrIdx < capIdx || instrIdx > dataSection {
		t.Errorf("Downstream task should lead <context> after the source attrs, before <data>: cap@%d instr@%d data@%d", capIdx, instrIdx, dataSection)
	}

	// 4b. Result data sits inside <data> (after the header).
	if resultIdx < dataSection {
		t.Errorf("Result data should appear after <data>: data@%d result@%d", dataSection, resultIdx)
	}

	// 4c. The byte budget is dual-anchored on the FINAL line (after <data>).
	if maxBytesIdx < dataSection {
		t.Errorf("maxBytes should appear on the final line after <data>: data@%d maxBytes@%d", dataSection, maxBytesIdx)
	}
}

// --- Phase 5: MicroResolver Source Data Trimming Tests ---

// TestMicroResolver_TrimSourceData_BelowThreshold verifies that source data
// below maxSourceDataBytes is passed through unchanged.
func TestMicroResolver_TrimSourceData_BelowThreshold(t *testing.T) {
	mr := NewMicroResolver(nil, nil)
	trimmer := NewStructuralTrimmer(nil, nil)
	mr.SetResultProcessor(trimmer, 16384)

	smallJSON := []byte(`{"lat": 48.85, "lon": 2.35}`)
	result := mr.trimSourceData(context.Background(), smallJSON, "weather", "hint", "step-1")

	if string(result) != string(smallJSON) {
		t.Errorf("Expected small JSON to pass through unchanged, got %s", string(result))
	}
}

// TestMicroResolver_TrimSourceData_LargeSourceTrimmed verifies that source data
// exceeding maxSourceDataBytes is trimmed by the ResultProcessor.
func TestMicroResolver_TrimSourceData_LargeSourceTrimmed(t *testing.T) {
	mr := NewMicroResolver(nil, nil)
	trimmer := NewStructuralTrimmer(nil, nil)
	maxBytes := 1024
	mr.SetResultProcessor(trimmer, maxBytes)

	// Build a large JSON object (>1KB)
	largeObj := make(map[string]interface{})
	for i := 0; i < 50; i++ {
		largeObj[fmt.Sprintf("field_%03d", i)] = strings.Repeat("x", 100)
	}
	largeJSON, _ := json.MarshalIndent(largeObj, "", "  ")
	if len(largeJSON) <= maxBytes {
		t.Fatalf("Test setup error: largeJSON should be > %d bytes, got %d", maxBytes, len(largeJSON))
	}

	result := mr.trimSourceData(context.Background(), largeJSON, "sentiment_analysis", "Need to extract: content", "step-3")

	if len(result) > maxBytes+200 { // Allow some overhead from trimmer annotation
		t.Errorf("Expected trimmed result ≤ %d bytes (with annotation), got %d bytes", maxBytes+200, len(result))
	}
	if len(result) >= len(largeJSON) {
		t.Errorf("Expected result to be smaller than original %d bytes, got %d bytes", len(largeJSON), len(result))
	}
}

// TestMicroResolver_TrimSourceData_NoProcessor verifies that without a ResultProcessor,
// source data is passed through unchanged regardless of size.
func TestMicroResolver_TrimSourceData_NoProcessor(t *testing.T) {
	mr := NewMicroResolver(nil, nil)
	// No SetResultProcessor call — resultProcessor is nil

	largeJSON := []byte(strings.Repeat(`{"key":"value"}`, 5000))
	result := mr.trimSourceData(context.Background(), largeJSON, "test", "hint", "step-1")

	if string(result) != string(largeJSON) {
		t.Errorf("Expected large JSON to pass through unchanged without processor")
	}
}

// TestMicroResolver_TrimSourceData_KeywordContext verifies that the trimmer
// receives meaningful keyword context from capability name and hint.
func TestMicroResolver_TrimSourceData_KeywordContext(t *testing.T) {
	// Use a custom processor that captures the instruction for verification
	var capturedInstruction string
	processor := &captureProcessor{
		processFunc: func(ctx context.Context, result string, maxBytes int, stepCtx ResultProcessorContext) string {
			capturedInstruction = stepCtx.Instruction
			// Return truncated result
			if len(result) > maxBytes {
				return result[:maxBytes]
			}
			return result
		},
	}

	mr := NewMicroResolver(nil, nil)
	mr.SetResultProcessor(processor, 4096) // Must exceed promptTemplateReserve (2048) + data size

	largeJSON := []byte(strings.Repeat("x", 5000)) // Exceeds effective budget (4096 - 2048 = 2048)
	mr.trimSourceData(context.Background(), largeJSON, "financial_analysis", "Need to extract values for required parameters: [data]", "step-2")

	if !strings.Contains(capturedInstruction, "financial_analysis") {
		t.Errorf("Expected instruction to contain capability name, got: %s", capturedInstruction)
	}
	if !strings.Contains(capturedInstruction, "data") {
		t.Errorf("Expected instruction to contain parameter hint, got: %s", capturedInstruction)
	}
}

// captureProcessor is a test helper that captures ProcessForPrompt calls.
type captureProcessor struct {
	processFunc func(ctx context.Context, result string, maxBytes int, stepCtx ResultProcessorContext) string
}

func (p *captureProcessor) ProcessForPrompt(ctx context.Context, result string, maxBytes int, stepCtx ResultProcessorContext) string {
	if p.processFunc != nil {
		return p.processFunc(ctx, result, maxBytes, stepCtx)
	}
	return result
}

// --- Phase 8: Agent Input Processor Tests ---

// newAgentInputGuard builds the opt-in byte-budget agent-input processor used by most tests.
func newAgentInputGuard(maxBytes int, logger core.Logger) AgentInputProcessor {
	return NewByteBudgetAgentInputProcessor(NewStructuralTrimmer(nil, nil), maxBytes, logger)
}

// errAgentInputProcessor always errors — models a transform (e.g. a redactor) that must fail closed.
type errAgentInputProcessor struct{}

func (errAgentInputProcessor) ProcessInput(context.Context, map[string]interface{}, ResultProcessorContext) (map[string]interface{}, error) {
	return nil, fmt.Errorf("redaction unavailable")
}

// TestAgentInput_Identity verifies the fidelity-first default passes everything through unchanged,
// even oversized values (the openclaw/PII regression guard).
func TestAgentInput_Identity(t *testing.T) {
	bigData := make(map[string]interface{})
	for i := 0; i < 100; i++ {
		bigData[fmt.Sprintf("field_%d", i)] = strings.Repeat("x", 500)
	}
	params := map[string]interface{}{"data": bigData, "symbol": "AMD"}
	stepCtx := ResultProcessorContext{StepID: "step-1", AgentName: "test", Instruction: "test"}

	result, err := identityAgentInputProcessor{}.ProcessInput(context.Background(), params, stepCtx)
	if err != nil {
		t.Fatalf("identity returned error: %v", err)
	}
	resultData, _ := json.Marshal(result["data"])
	originalData, _ := json.Marshal(bigData)
	if len(resultData) != len(originalData) {
		t.Errorf("Expected identity passthrough, got %d vs %d", len(resultData), len(originalData))
	}
	if result["symbol"] != "AMD" {
		t.Errorf("Expected symbol=AMD, got %v", result["symbol"])
	}
}

// TestAgentInput_ErrorPropagates verifies a transform error is surfaced (the executor fails closed).
func TestAgentInput_ErrorPropagates(t *testing.T) {
	_, err := errAgentInputProcessor{}.ProcessInput(context.Background(),
		map[string]interface{}{"a": 1}, ResultProcessorContext{})
	if err == nil {
		t.Fatal("expected error to propagate (fail-closed contract)")
	}
}

// TestAgentInput_ScalarsUntouched verifies scalar values are never trimmed regardless of budget.
func TestAgentInput_ScalarsUntouched(t *testing.T) {
	p := newAgentInputGuard(100, nil)
	params := map[string]interface{}{"symbol": "AMD", "count": 42.0, "active": true}
	stepCtx := ResultProcessorContext{StepID: "step-1", AgentName: "test", Instruction: "analyze stock"}
	result, _ := p.ProcessInput(context.Background(), params, stepCtx)

	if result["symbol"] != "AMD" {
		t.Errorf("Expected symbol=AMD, got %v", result["symbol"])
	}
	if result["count"] != 42.0 {
		t.Errorf("Expected count=42.0, got %v", result["count"])
	}
	if result["active"] != true {
		t.Errorf("Expected active=true, got %v", result["active"])
	}
}

// TestAgentInput_SmallObjectUntouched verifies complex objects below the budget pass through.
func TestAgentInput_SmallObjectUntouched(t *testing.T) {
	p := newAgentInputGuard(10000, nil)
	params := map[string]interface{}{
		"data": map[string]interface{}{"price": 150.5, "currency": "USD"},
	}
	stepCtx := ResultProcessorContext{StepID: "step-1", AgentName: "test", Instruction: "get price"}
	result, _ := p.ProcessInput(context.Background(), params, stepCtx)

	data, ok := result["data"].(map[string]interface{})
	if !ok {
		t.Fatalf("Expected data map, got %T", result["data"])
	}
	if data["price"] != 150.5 {
		t.Errorf("Expected price=150.5, got %v", data["price"])
	}
}

// TestAgentInput_LargeObjectTrimmed verifies complex objects exceeding the budget are trimmed.
func TestAgentInput_LargeObjectTrimmed(t *testing.T) {
	bigData := make(map[string]interface{})
	for i := 0; i < 100; i++ {
		bigData[fmt.Sprintf("field_%d", i)] = strings.Repeat("x", 500)
	}

	p := newAgentInputGuard(4096, nil)
	params := map[string]interface{}{"symbol": "AMD", "data": bigData}
	stepCtx := ResultProcessorContext{StepID: "step-1", AgentName: "stock-service", Instruction: "get stock financial metrics"}
	result, _ := p.ProcessInput(context.Background(), params, stepCtx)

	if result["symbol"] != "AMD" {
		t.Errorf("Expected symbol=AMD, got %v", result["symbol"])
	}
	trimmedData, err := json.Marshal(result["data"])
	if err != nil {
		t.Fatalf("Failed to marshal trimmed data: %v", err)
	}
	if len(trimmedData) > 5000 {
		t.Errorf("Expected trimmed data <= ~4096 bytes, got %d", len(trimmedData))
	}
	originalData, _ := json.Marshal(bigData)
	if len(trimmedData) >= len(originalData) {
		t.Errorf("Expected trimmed data (%d) to be smaller than original (%d)", len(trimmedData), len(originalData))
	}
}

// TestAgentInput_ZeroBudgetDisabled verifies a non-positive budget disables the guard (passthrough).
func TestAgentInput_ZeroBudgetDisabled(t *testing.T) {
	p := newAgentInputGuard(0, nil)
	bigData := make(map[string]interface{})
	for i := 0; i < 50; i++ {
		bigData[fmt.Sprintf("field_%d", i)] = strings.Repeat("x", 500)
	}
	params := map[string]interface{}{"data": bigData}
	stepCtx := ResultProcessorContext{StepID: "step-1", AgentName: "test", Instruction: "test"}
	result, _ := p.ProcessInput(context.Background(), params, stepCtx)

	resultData, _ := json.Marshal(result["data"])
	originalData, _ := json.Marshal(bigData)
	if len(resultData) != len(originalData) {
		t.Errorf("Expected unchanged data with zero budget, got %d vs %d", len(resultData), len(originalData))
	}
}

// TestAgentInput_MixedParams verifies scalar, small-object, and large-object params in one call.
func TestAgentInput_MixedParams(t *testing.T) {
	bigData := make(map[string]interface{})
	for i := 0; i < 100; i++ {
		bigData[fmt.Sprintf("field_%d", i)] = strings.Repeat("x", 500)
	}

	p := newAgentInputGuard(4096, nil)
	params := map[string]interface{}{
		"symbol":        "AMD",
		"active":        true,
		"price":         150.5,
		"small_context": map[string]interface{}{"note": "short"},
		"data":          bigData,
	}
	stepCtx := ResultProcessorContext{StepID: "step-2", AgentName: "research-agent", Instruction: "analyze financials"}
	result, _ := p.ProcessInput(context.Background(), params, stepCtx)

	if result["symbol"] != "AMD" {
		t.Errorf("Expected symbol=AMD, got %v", result["symbol"])
	}
	if result["active"] != true {
		t.Errorf("Expected active=true, got %v", result["active"])
	}
	if result["price"] != 150.5 {
		t.Errorf("Expected price=150.5, got %v", result["price"])
	}
	smallCtx, ok := result["small_context"].(map[string]interface{})
	if !ok {
		t.Fatalf("Expected small_context to be map, got %T", result["small_context"])
	}
	if smallCtx["note"] != "short" {
		t.Errorf("Expected small_context.note=short, got %v", smallCtx["note"])
	}
	trimmedData, _ := json.Marshal(result["data"])
	originalData, _ := json.Marshal(bigData)
	if len(trimmedData) >= len(originalData) {
		t.Errorf("Expected data to be trimmed: original=%d, trimmed=%d", len(originalData), len(trimmedData))
	}
}

// TestAgentInput_LargeArray verifies array parameter values (not just maps) are trimmed.
func TestAgentInput_LargeArray(t *testing.T) {
	bigArray := make([]interface{}, 100)
	for i := 0; i < 100; i++ {
		bigArray[i] = map[string]interface{}{
			"headline": fmt.Sprintf("Article %d: %s", i, strings.Repeat("x", 200)),
			"source":   "test",
		}
	}

	p := newAgentInputGuard(4096, nil)
	params := map[string]interface{}{"articles": bigArray}
	stepCtx := ResultProcessorContext{StepID: "step-2", AgentName: "sentiment-agent", Instruction: "analyze news sentiment"}
	result, _ := p.ProcessInput(context.Background(), params, stepCtx)

	trimmedJSON, _ := json.Marshal(result["articles"])
	originalJSON, _ := json.Marshal(bigArray)
	if len(trimmedJSON) >= len(originalJSON) {
		t.Errorf("Expected array to be trimmed: original=%d, trimmed=%d", len(originalJSON), len(trimmedJSON))
	}
}

// TestAgentInput_NilValue verifies nil parameter values (scalar) pass through unchanged.
func TestAgentInput_NilValue(t *testing.T) {
	p := newAgentInputGuard(100, nil)
	params := map[string]interface{}{"optional_field": nil, "name": "test"}
	stepCtx := ResultProcessorContext{StepID: "step-1", AgentName: "test", Instruction: "test"}
	result, _ := p.ProcessInput(context.Background(), params, stepCtx)

	if result["optional_field"] != nil {
		t.Errorf("Expected nil to pass through, got %v", result["optional_field"])
	}
	if result["name"] != "test" {
		t.Errorf("Expected name=test, got %v", result["name"])
	}
}

// TestAgentInput_ParseFailureFallback verifies that when the trimmer returns invalid JSON (even
// after annotation stripping), the original value is kept (fail open — never corrupt a tool input).
func TestAgentInput_ParseFailureFallback(t *testing.T) {
	processor := &captureProcessor{
		processFunc: func(ctx context.Context, result string, maxBytes int, stepCtx ResultProcessorContext) string {
			return "this is not valid json at all"
		},
	}
	p := NewByteBudgetAgentInputProcessor(processor, 100, nil)

	originalData := map[string]interface{}{"important": strings.Repeat("x", 200)}
	params := map[string]interface{}{"data": originalData}
	stepCtx := ResultProcessorContext{StepID: "step-1", AgentName: "test", Instruction: "test"}
	result, _ := p.ProcessInput(context.Background(), params, stepCtx)

	data, ok := result["data"].(map[string]interface{})
	if !ok {
		t.Fatalf("Expected original map preserved on parse failure, got %T", result["data"])
	}
	if data["important"] != originalData["important"] {
		t.Error("Expected original data preserved on parse failure")
	}
}

// TestAgentInput_ParseFailureWithLogger verifies the warn log on re-parse failure.
func TestAgentInput_ParseFailureWithLogger(t *testing.T) {
	processor := &captureProcessor{
		processFunc: func(ctx context.Context, result string, maxBytes int, stepCtx ResultProcessorContext) string {
			return "not json"
		},
	}
	logger := &mockLogger{warnFunc: func(msg string, fields map[string]interface{}) {}}
	p := NewByteBudgetAgentInputProcessor(processor, 100, logger)

	originalData := map[string]interface{}{"key": strings.Repeat("x", 200)}
	params := map[string]interface{}{"data": originalData}
	stepCtx := ResultProcessorContext{StepID: "step-1", AgentName: "test", Instruction: "test"}
	_, _ = p.ProcessInput(context.Background(), params, stepCtx)

	found := false
	for _, msg := range logger.messages {
		if strings.Contains(msg, "Failed to re-parse") {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("Expected warn about re-parse failure, got messages: %v", logger.messages)
	}
}

// TestAgentInput_SuccessWithLogger verifies the info log on a successful trim.
func TestAgentInput_SuccessWithLogger(t *testing.T) {
	logger := &mockLogger{infoFunc: func(msg string, fields map[string]interface{}) {}}
	p := NewByteBudgetAgentInputProcessor(NewStructuralTrimmer(nil, nil), 4096, logger)

	bigData := make(map[string]interface{})
	for i := 0; i < 100; i++ {
		bigData[fmt.Sprintf("field_%d", i)] = strings.Repeat("x", 500)
	}
	params := map[string]interface{}{"data": bigData}
	stepCtx := ResultProcessorContext{StepID: "step-1", AgentName: "stock-service", Instruction: "analyze"}
	_, _ = p.ProcessInput(context.Background(), params, stepCtx)

	found := false
	for _, msg := range logger.messages {
		if strings.Contains(msg, "Agent input parameter trimmed") {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("Expected info log about trimming, got messages: %v", logger.messages)
	}
}

// TestAgentInput_KeywordContext verifies the step instruction is passed to the trimmer as context.
func TestAgentInput_KeywordContext(t *testing.T) {
	var capturedInstruction string
	processor := &captureProcessor{
		processFunc: func(ctx context.Context, result string, maxBytes int, stepCtx ResultProcessorContext) string {
			capturedInstruction = stepCtx.Instruction
			if len(result) > maxBytes {
				return result[:maxBytes]
			}
			return result
		},
	}
	p := NewByteBudgetAgentInputProcessor(processor, 4096, nil)

	bigData := make(map[string]interface{})
	for i := 0; i < 100; i++ {
		bigData[fmt.Sprintf("field_%d", i)] = strings.Repeat("x", 500)
	}
	params := map[string]interface{}{"data": bigData}
	stepCtx := ResultProcessorContext{
		StepID:      "step-2",
		AgentName:   "research-agent",
		Instruction: "Analyze AMD financial metrics and provide investment thesis",
	}
	_, _ = p.ProcessInput(context.Background(), params, stepCtx)

	if capturedInstruction != stepCtx.Instruction {
		t.Errorf("Expected instruction=%q, got %q", stepCtx.Instruction, capturedInstruction)
	}
}

// TestAgentInput_AnnotationStripped verifies every disclosure form is stripped before re-parsing.
func TestAgentInput_AnnotationStripped(t *testing.T) {
	for _, annotation := range []string{
		"\n[trimmed: 1/10 fields kept, 9 dropped]",
		"\n[severely reduced: kept 12 of ~9000 bytes (0.13%); most content omitted]",
	} {
		processor := &captureProcessor{
			processFunc: func(ctx context.Context, result string, maxBytes int, stepCtx ResultProcessorContext) string {
				return `{"key":"value"}` + annotation
			},
		}
		p := NewByteBudgetAgentInputProcessor(processor, 100, nil)

		bigData := map[string]interface{}{"large": strings.Repeat("x", 200)}
		params := map[string]interface{}{"data": bigData}
		stepCtx := ResultProcessorContext{StepID: "step-1", AgentName: "test", Instruction: "test"}
		result, _ := p.ProcessInput(context.Background(), params, stepCtx)

		data, ok := result["data"].(map[string]interface{})
		if !ok {
			t.Fatalf("Expected parsed map after stripping %q, got %T: %v", annotation, result["data"], result["data"])
		}
		if data["key"] != "value" {
			t.Errorf("Expected key=value after stripping %q, got %v", annotation, data["key"])
		}
	}
}

// =============================================================================
// Phase 13: LLMDistiller debug store recording
// Uses mockLLMDebugStore from llm_call_recorder_adapter_test.go (same package).
// =============================================================================

// TestLLMDistiller_DebugStore_RecordsSuccessInteraction verifies that a successful
// distillation call records a result_distillation interaction in the debug store
// with Success=true and the correct StepID, requestID, and token counts.
func TestLLMDistiller_DebugStore_RecordsSuccessInteraction(t *testing.T) {
	debugStore := &mockLLMDebugStore{}
	mockAI := &distillerMockAI{
		response: &core.AIResponse{
			Content:  `{"summary": "distilled"}`,
			Model:    "claude-3-5-haiku",
			Provider: "anthropic",
			Usage:    core.TokenUsage{PromptTokens: 80, CompletionTokens: 20, TotalTokens: 100},
		},
	}
	trimmer := NewStructuralTrimmer(nil, nil)
	config := ResultDistillConfig{
		Enabled:          true,
		DistillThreshold: 10, // small so any input > 10 bytes triggers LLM
		PreFilterBudget:  500,
		TargetSize:       200,
	}
	distiller := NewLLMDistiller(mockAI, config, trimmer, nil)
	distiller.SetLLMDebugStore(debugStore)

	ctx := telemetry.WithBaggage(context.Background(), "request_id", "req-distill-success")
	stepCtx := ResultProcessorContext{
		StepID:      "step-2",
		AgentName:   "weather-tool",
		Instruction: "summarize weather data",
	}
	largeInput := `{"field1":"value1","field2":"value2","field3":"value3"}`

	result := distiller.ProcessForPrompt(ctx, largeInput, 500, stepCtx)
	distiller.Shutdown()

	if result != `{"summary": "distilled"}` {
		t.Errorf("unexpected result: %q", result)
	}
	if len(debugStore.interactions) != 1 {
		t.Fatalf("expected 1 debug interaction, got %d", len(debugStore.interactions))
	}

	i := debugStore.interactions[0]
	if i.Type != "result_distillation" {
		t.Errorf("expected type 'result_distillation', got %q", i.Type)
	}
	if !i.Success {
		t.Error("expected Success=true for successful distillation")
	}
	if i.StepID != "step-2" {
		t.Errorf("expected StepID='step-2', got %q", i.StepID)
	}
	if i.Model != "claude-3-5-haiku" {
		t.Errorf("expected Model='claude-3-5-haiku', got %q", i.Model)
	}
	if i.PromptTokens != 80 {
		t.Errorf("expected PromptTokens=80, got %d", i.PromptTokens)
	}
	if i.CompletionTokens != 20 {
		t.Errorf("expected CompletionTokens=20, got %d", i.CompletionTokens)
	}
	if debugStore.requestIDs[0] != "req-distill-success" {
		t.Errorf("expected requestID='req-distill-success', got %q", debugStore.requestIDs[0])
	}
}

// TestLLMDistiller_DebugStore_RecordsFailureInteraction verifies that when the LLM call
// fails, a result_distillation interaction with Success=false is recorded and the Error
// field is populated.
func TestLLMDistiller_DebugStore_RecordsFailureInteraction(t *testing.T) {
	debugStore := &mockLLMDebugStore{}
	mockAI := &distillerMockAI{
		err: fmt.Errorf("LLM service unavailable"),
	}
	trimmer := NewStructuralTrimmer(nil, nil)
	config := ResultDistillConfig{
		Enabled:          true,
		DistillThreshold: 10,
		PreFilterBudget:  500,
		TargetSize:       200,
	}
	distiller := NewLLMDistiller(mockAI, config, trimmer, nil)
	distiller.SetLLMDebugStore(debugStore)

	ctx := telemetry.WithBaggage(context.Background(), "request_id", "req-distill-fail")
	stepCtx := ResultProcessorContext{
		StepID:    "step-3",
		AgentName: "geo-tool",
	}
	largeInput := `{"field1":"value1","field2":"value2","field3":"value3"}`

	distiller.ProcessForPrompt(ctx, largeInput, 500, stepCtx) // falls back to structural trim
	distiller.Shutdown()

	if len(debugStore.interactions) != 1 {
		t.Fatalf("expected 1 debug interaction, got %d", len(debugStore.interactions))
	}

	i := debugStore.interactions[0]
	if i.Type != "result_distillation" {
		t.Errorf("expected type 'result_distillation', got %q", i.Type)
	}
	if i.Success {
		t.Error("expected Success=false for LLM failure")
	}
	if i.StepID != "step-3" {
		t.Errorf("expected StepID='step-3', got %q", i.StepID)
	}
	if i.Error == "" {
		t.Error("expected Error field to be populated on failure")
	}
	if debugStore.requestIDs[0] != "req-distill-fail" {
		t.Errorf("expected requestID='req-distill-fail', got %q", debugStore.requestIDs[0])
	}
}

// TestLLMDistiller_DebugStore_SkipsWhenRequestIDEmpty verifies that when the context
// has no baggage (empty requestID), the debug store is NOT called.
// This guards against recording interactions with no correlation key.
func TestLLMDistiller_DebugStore_SkipsWhenRequestIDEmpty(t *testing.T) {
	debugStore := &mockLLMDebugStore{}
	mockAI := &distillerMockAI{
		response: &core.AIResponse{Content: `{"k":"v"}`},
	}
	trimmer := NewStructuralTrimmer(nil, nil)
	config := ResultDistillConfig{
		Enabled:          true,
		DistillThreshold: 10,
		PreFilterBudget:  500,
		TargetSize:       200,
	}
	distiller := NewLLMDistiller(mockAI, config, trimmer, nil)
	distiller.SetLLMDebugStore(debugStore)

	// context.Background() has no baggage → requestID will be empty
	distiller.ProcessForPrompt(context.Background(), `{"field1":"val1","field2":"val2"}`, 500, ResultProcessorContext{
		StepID: "step-1", AgentName: "test-agent",
	})
	distiller.Shutdown()

	if len(debugStore.interactions) != 0 {
		t.Errorf("expected 0 debug interactions when requestID is empty, got %d", len(debugStore.interactions))
	}
}

// --- TestWithTrimMetadataCapture ---

func TestWithTrimMetadataCapture_NoOp(t *testing.T) {
	// captureTrimMetadata on a plain context must not panic and must not write anywhere.
	captureTrimMetadata(context.Background(), ResultTrimMetadata{
		OriginalBytes: 100, TrimmedBytes: 50, Method: "structural",
	})
	// Reaching here without panic is the assertion.
}

func TestWithTrimMetadataCapture_RoundTrip(t *testing.T) {
	ctx, meta := WithTrimMetadataCapture(context.Background())
	captureTrimMetadata(ctx, ResultTrimMetadata{
		OriginalBytes: 1024,
		TrimmedBytes:  512,
		Method:        "structural",
		FieldsKept:    3,
		FieldsDropped: 2,
		Keywords:      []string{"price", "name"},
	})

	if meta.Method != "structural" {
		t.Errorf("Expected Method='structural', got %q", meta.Method)
	}
	if meta.OriginalBytes != 1024 {
		t.Errorf("Expected OriginalBytes=1024, got %d", meta.OriginalBytes)
	}
	if meta.TrimmedBytes != 512 {
		t.Errorf("Expected TrimmedBytes=512, got %d", meta.TrimmedBytes)
	}
	if meta.FieldsKept != 3 {
		t.Errorf("Expected FieldsKept=3, got %d", meta.FieldsKept)
	}
	if meta.FieldsDropped != 2 {
		t.Errorf("Expected FieldsDropped=2, got %d", meta.FieldsDropped)
	}
	if len(meta.Keywords) != 2 {
		t.Errorf("Expected 2 keywords, got %d: %v", len(meta.Keywords), meta.Keywords)
	}
}

func TestWithTrimMetadataCapture_SecondWriteWins(t *testing.T) {
	// Second captureTrimMetadata call overwrites the first.
	ctx, meta := WithTrimMetadataCapture(context.Background())
	captureTrimMetadata(ctx, ResultTrimMetadata{Method: "structural", OriginalBytes: 100})
	captureTrimMetadata(ctx, ResultTrimMetadata{Method: "truncate", OriginalBytes: 200})

	if meta.Method != "truncate" {
		t.Errorf("Expected second capture to win, got Method=%q", meta.Method)
	}
	if meta.OriginalBytes != 200 {
		t.Errorf("Expected OriginalBytes=200, got %d", meta.OriginalBytes)
	}
}

// --- TestProcessMultipleForBudget_MetadataMap ---

func TestProcessMultipleForBudget_MetadataMap_PopulatedAndKeyed(t *testing.T) {
	trimmer := NewStructuralTrimmer(nil, nil)
	steps := []StepResult{
		{StepID: "step-1", AgentName: "agent-a", Instruction: "get price", Response: `{"price":42}`},
		{StepID: "step-2", AgentName: "agent-b", Instruction: "get name", Response: `{"name":"Widget"}`},
	}

	results, metaMap := ProcessMultipleForBudget(context.Background(), trimmer, steps, 10000, 5000, "")

	if len(results) != 2 {
		t.Fatalf("Expected 2 results, got %d", len(results))
	}
	if len(metaMap) != 2 {
		t.Fatalf("Expected metadata map length 2, got %d", len(metaMap))
	}
	for _, step := range steps {
		m, ok := metaMap[step.StepID]
		if !ok {
			t.Errorf("Expected metadata entry for StepID=%q", step.StepID)
			continue
		}
		if m.BudgetAllocated <= 0 {
			t.Errorf("Expected BudgetAllocated > 0 for step %q, got %d", step.StepID, m.BudgetAllocated)
		}
	}
}

func TestProcessMultipleForBudget_MetadataMap_SmallResultBudgetEqualsSize(t *testing.T) {
	// Small results (under per-result cap) get BudgetAllocated == their own byte size.
	trimmer := NewStructuralTrimmer(nil, nil)
	resp := `{"x":"y"}`
	steps := []StepResult{
		{StepID: "s1", AgentName: "a", Instruction: "get x", Response: resp},
	}

	_, metaMap := ProcessMultipleForBudget(context.Background(), trimmer, steps, 5000, 5000, "")

	m, ok := metaMap["s1"]
	if !ok {
		t.Fatal("Expected metadata entry for s1")
	}
	if m.BudgetAllocated != len(resp) {
		t.Errorf("Expected BudgetAllocated=%d, got %d", len(resp), m.BudgetAllocated)
	}
}

func TestProcessMultipleForBudget_MetadataMap_MethodPopulatedOnTrim(t *testing.T) {
	// When trimming is triggered, Method must be non-empty in the returned metadata.
	trimmer := NewStructuralTrimmer(nil, nil)
	large := `{"price":100,"name":"Widget","description":"A very fine product","id":"sku-001"}`
	steps := []StepResult{
		{StepID: "s1", AgentName: "a", Instruction: "get price", Response: large},
	}

	_, metaMap := ProcessMultipleForBudget(context.Background(), trimmer, steps, 20, 20, "")

	m, ok := metaMap["s1"]
	if !ok {
		t.Fatal("Expected metadata entry for s1")
	}
	if m.Method == "" {
		t.Error("Expected Method to be populated after trimming")
	}
}

// --- TestLLMDistiller_CapturesMetadata ---

func TestLLMDistiller_CapturesMetadataOnSuccess(t *testing.T) {
	distilledContent := `{"distilled":"value"}`
	mockAI := &distillerMockAI{
		response: &core.AIResponse{
			Content: distilledContent,
			Usage:   core.TokenUsage{PromptTokens: 50, CompletionTokens: 20},
		},
	}
	trimmer := NewStructuralTrimmer(nil, nil)
	config := ResultDistillConfig{
		Enabled: true, DistillThreshold: 10, PreFilterBudget: 500, TargetSize: 200,
	}
	distiller := NewLLMDistiller(mockAI, config, trimmer, nil)

	largeInput := `{"field1":"value1","field2":"value2"}`
	ctx, meta := WithTrimMetadataCapture(context.Background())
	result := distiller.ProcessForPrompt(ctx, largeInput, 500, ResultProcessorContext{
		StepID: "s1", AgentName: "test", Instruction: "summarize",
	})

	if result != distilledContent {
		t.Errorf("Expected distilled content, got %q", result)
	}
	if meta.Method != "distill" {
		t.Errorf("Expected Method='distill', got %q", meta.Method)
	}
	if meta.OriginalBytes != len(largeInput) {
		t.Errorf("Expected OriginalBytes=%d, got %d", len(largeInput), meta.OriginalBytes)
	}
	if meta.TrimmedBytes != len(distilledContent) {
		t.Errorf("Expected TrimmedBytes=%d, got %d", len(distilledContent), meta.TrimmedBytes)
	}
}

func TestLLMDistiller_ErrorPath_MetadataNotDistill(t *testing.T) {
	// On LLM failure the structural preFilter is invoked as fallback.
	// Metadata must NOT have Method="distill".
	// Use maxBytes=20 so the fallback trimmer actually trims (37 bytes > 20), which
	// means captureTrimMetadata is called with the structural method.
	mockAI := &distillerMockAI{err: fmt.Errorf("api error")}
	trimmer := NewStructuralTrimmer(nil, nil)
	config := ResultDistillConfig{
		Enabled: true, DistillThreshold: 10, PreFilterBudget: 500, TargetSize: 200,
	}
	distiller := NewLLMDistiller(mockAI, config, trimmer, nil)

	largeInput := `{"field1":"value1","field2":"value2"}`
	ctx, meta := WithTrimMetadataCapture(context.Background())
	distiller.ProcessForPrompt(ctx, largeInput, 20, ResultProcessorContext{
		StepID: "s1", AgentName: "test", Instruction: "summarize",
	})

	if meta.Method == "distill" {
		t.Error("Fallback path must not use Method='distill'")
	}
	if meta.Method == "" {
		t.Error("Expected metadata to be populated by structural fallback (input 37b > budget 20b)")
	}
}

// --- Phase 0: degenerate-trim detection ---

func TestDegenerateTrim(t *testing.T) {
	cases := []struct {
		name              string
		original, trimmed int
		wantDegenerate    bool
		wantRatio         float64
	}{
		{"zero original disables check", 0, 100, false, 1},
		{"negative original disables check", -5, 100, false, 1},
		{"well below threshold", 100000, 2000, true, 0.02},
		{"just below threshold", 1000, 49, true, 0.049},
		{"exactly at threshold is not degenerate", 1000, 50, false, 0.05},
		{"above threshold", 1000, 500, false, 0.5},
		{"full passthrough", 1000, 1000, false, 1.0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gotDeg, gotRatio := degenerateTrim(tc.original, tc.trimmed)
			if gotDeg != tc.wantDegenerate {
				t.Errorf("degenerateTrim(%d,%d) degenerate=%v, want %v",
					tc.original, tc.trimmed, gotDeg, tc.wantDegenerate)
			}
			if math.Abs(gotRatio-tc.wantRatio) > 1e-9 {
				t.Errorf("degenerateTrim(%d,%d) ratio=%v, want %v",
					tc.original, tc.trimmed, gotRatio, tc.wantRatio)
			}
		})
	}
}

// --- Phase 1: extractive distillation prompt ---

// TestBuildDistillationPrompt_Extractive verifies the Phase 1 prompt is extractive
// for evidence (verbatim units), abstractive for narrative, and explicitly guards
// against false-absence inferences — while staying domain-agnostic.
func TestBuildDistillationPrompt_Extractive(t *testing.T) {
	d := &LLMDistiller{}
	userMsg := d.buildDistillationPrompt(`{"a":1}`, 1024, ResultProcessorContext{
		AgentName:   "agent",
		Instruction: "find errors",
	}, 1.0)

	// The extractive rules live in the system message (Phase 13 §2.9 split).
	for _, want := range []string{"VERBATIM", "EVIDENCE", "NARRATIVE", "No matching entries found", "UNKNOWN"} {
		if !strings.Contains(distillationSystemPrompt, want) {
			t.Errorf("Expected the extractive system prompt to contain %q", want)
		}
	}

	// Framework defaults must not hardcode any domain vocabulary (no logs/SRE/K8s strings) — in
	// either the static system prompt or the per-call user message.
	for _, banned := range []string{"Kubernetes", "Loki", "SRE", "Slack"} {
		if strings.Contains(distillationSystemPrompt, banned) || strings.Contains(userMsg, banned) {
			t.Errorf("Prompt must stay domain-agnostic, found %q", banned)
		}
	}
}

// --- Phase 4: compaction deadline ---

// blockingMockAI blocks until the context is cancelled, then returns the context
// error — used to exercise the compaction deadline fail-open path.
type blockingMockAI struct{}

func (blockingMockAI) GenerateResponse(ctx context.Context, _ string, _ *core.AIOptions) (*core.AIResponse, error) {
	<-ctx.Done()
	return nil, ctx.Err()
}

func (blockingMockAI) StreamResponse(ctx context.Context, _ string, _ *core.AIOptions, _ func(string)) (*core.AIResponse, error) {
	<-ctx.Done()
	return nil, ctx.Err()
}

// TestLLMDistiller_CompactionDeadline_FailsOpen verifies that when the LLM call exceeds
// the compaction deadline, ProcessForPrompt returns promptly via the structural floor
// rather than blocking the synthesis hot path.
func TestLLMDistiller_CompactionDeadline_FailsOpen(t *testing.T) {
	trimmer := NewStructuralTrimmer(nil, nil)
	config := ResultDistillConfig{
		Enabled: true, DistillThreshold: 10, PreFilterBudget: 4096, TargetSize: 200,
		CompactionDeadline: 50 * time.Millisecond,
	}
	distiller := NewLLMDistiller(blockingMockAI{}, config, trimmer, nil)

	// Larger than the 100B budget so the structural fallback actually trims and records.
	input := `{"alpha":"` + strings.Repeat("x", 300) + `","beta":"value"}`
	ctx, meta := WithTrimMetadataCapture(context.Background())

	start := time.Now()
	out := distiller.ProcessForPrompt(ctx, input, 100, ResultProcessorContext{
		StepID: "s1", AgentName: "a", Instruction: "summarize",
	})
	elapsed := time.Since(start)

	if elapsed > 2*time.Second {
		t.Errorf("expected return near the %v deadline, took %v", config.CompactionDeadline, elapsed)
	}
	if out == "" {
		t.Error("expected non-empty structural fallback output on deadline timeout")
	}
	if meta.Method == "distill" {
		t.Errorf("deadline timeout must fall open to structural, not Method=distill (got %q)", meta.Method)
	}
}

// TestLLMDistiller_ZeroBudgetDoesNotZeroTarget reproduces the live incident
// (orch-1781973307736373861): when the BudgetAllocator hands an oversized result a budget
// of 0, the distiller must NOT build a "max 0 characters" prompt — it must floor the
// target so the model can still emit a useful compaction.
func TestLLMDistiller_ZeroBudgetDoesNotZeroTarget(t *testing.T) {
	mockAI := &distillerMockAI{response: &core.AIResponse{Content: "DISTILLED-LOG-LINES"}}
	config := ResultDistillConfig{
		Enabled: true, DistillThreshold: 10, PreFilterBudget: 4096, TargetSize: 4096,
	}
	distiller := NewLLMDistiller(mockAI, config, NewStructuralTrimmer(nil, nil), nil)

	largeInput := `{"streams":"` + strings.Repeat("x", 20000) + `"}`
	out := distiller.ProcessForPrompt(context.Background(), largeInput, 0, ResultProcessorContext{
		StepID: "s1", AgentName: "a", Instruction: "find errors",
	})

	if strings.Contains(mockAI.prompt, "at most 0 characters") {
		t.Errorf("budget=0 must not produce a 'max 0 characters' prompt")
	}
	// A non-positive budget is ignored, so the target stays the full configured TargetSize.
	if !strings.Contains(mockAI.prompt, "at most 4096 characters") {
		t.Errorf("expected the target to fall back to TargetSize=4096, prompt head: %.200s", mockAI.prompt)
	}
	// Phase 16 — the input pre-filters to a partial sample (~21%), so the framework appends the
	// partial-source disclosure to the distilled OUTPUT. The real distilled content is still present.
	if !strings.Contains(out, "DISTILLED-LOG-LINES") {
		t.Errorf("expected the real distilled output to be present, got %q", out)
	}
	if !strings.Contains(out, "partial source") {
		t.Errorf("expected the partial-source disclosure to be appended, got %q", out)
	}
}
