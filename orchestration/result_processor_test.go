package orchestration

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
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
	if dataVal["price"] != 150.5 {
		t.Errorf("Expected price=150.5, got %v", dataVal["price"])
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
	} else if len(arr) != 1 || arr[0] != 1.0 {
		t.Errorf("Expected deserialized [1], got %v", arr)
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

	// remaining clamped to 0, oversized gets 0
	if budgets[3] != 0 {
		t.Errorf("Expected 0 budget for oversized when small results exceed total, got %d", budgets[3])
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

	results, _ := ProcessMultipleForBudget(context.Background(), trimmer, steps, 10000, 5000)

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
	results, _ := ProcessMultipleForBudget(context.Background(), trimmer, steps, totalBudget, 200)

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
	distiller.ProcessForPrompt(context.Background(), largeInput, 200, ResultProcessorContext{
		StepID: "step-1", AgentName: "test", Instruction: "summarize",
	})

	// Verify the prompt uses maxBytes (200) as target, not config.TargetSize (500)
	if !strings.Contains(mockAI.prompt, "200") {
		t.Errorf("Expected prompt to contain maxBytes target '200', prompt: %.300s", mockAI.prompt)
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

	// Without capability, prompt should not contain "(capability:"
	if strings.Contains(mockAI.prompt, "(capability:") {
		t.Errorf("Expected no capability line in prompt when Capability is empty, prompt: %.300s", mockAI.prompt)
	}
}

// TestBuildDistillationPrompt guards the fmt.Sprintf argument ordering in
// buildDistillationPrompt. After ORCH-007 Change 6, the argument order is:
//
//	maxBytes, AgentName, capabilityLine, Instruction, result
//
// The prompt layout is: <identity> -> <instructions>(maxBytes) -> <context>(Agent,Cap,Instr) -> <data>(result)
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

	prompt := d.buildDistillationPrompt(result, maxBytes, stepCtx)

	// --- 1. No fmt.Sprintf type-mismatch garbage ---
	if strings.Contains(prompt, "%!") {
		t.Fatalf("Prompt contains fmt.Sprintf type-mismatch indicator (%%!), "+
			"which means arguments are in the wrong order:\n%s", prompt)
	}

	// --- 2. All values are present ---
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

	// --- 3. Positional correctness ---
	// New layout: <instructions>(maxBytes) -> <context>(Agent,Cap,Instr) -> <data>(result)
	instructionsSection := strings.Index(prompt, "<instructions>")
	contextSection := strings.Index(prompt, "<context")
	dataSection := strings.Index(prompt, "<data>")

	if instructionsSection < 0 || contextSection < 0 || dataSection < 0 {
		t.Fatalf("Could not locate all section headers in prompt:\n%s", prompt)
	}

	maxBytesIdx := strings.Index(prompt, "4096")
	agentIdx := strings.Index(prompt, "stock-agent")
	capIdx := strings.Index(prompt, "get_stock_price")
	instrIdx := strings.Index(prompt, "Fetch latest equity price for portfolio analysis")
	resultIdx := strings.Index(prompt, "AAPL")

	// 3a. maxBytes must be inside <instructions> section (before <context>)
	if maxBytesIdx < instructionsSection || maxBytesIdx > contextSection {
		t.Errorf("maxBytes should appear between <instructions> and <context> sections\n"+
			"  instructions@%d, maxBytes@%d, context@%d", instructionsSection, maxBytesIdx, contextSection)
	}

	// 3b. AgentName and Capability must be inside <context> section (between context and data)
	if agentIdx < contextSection || agentIdx > dataSection {
		t.Errorf("AgentName should appear between <context> and <data> sections\n"+
			"  context@%d, AgentName@%d, data@%d", contextSection, agentIdx, dataSection)
	}
	if capIdx < agentIdx {
		t.Error("Capability should appear after AgentName (it's appended as a suffix)")
	}
	if capIdx > dataSection {
		t.Errorf("Capability should appear before <data> section\n"+
			"  Capability@%d, data@%d", capIdx, dataSection)
	}

	// 3c. Instruction must be inside <context> section (after capability, before data)
	if instrIdx < capIdx || instrIdx > dataSection {
		t.Errorf("Instruction should appear after Capability and before <data> section\n"+
			"  Capability@%d, Instruction@%d, data@%d", capIdx, instrIdx, dataSection)
	}

	// 3d. Result data must be inside <data> section (after dataSection header)
	if resultIdx < dataSection {
		t.Errorf("Result data should appear after <data> section header\n"+
			"  data@%d, result@%d", dataSection, resultIdx)
	}

	// 3e. Verify overall section ordering: Instructions < Context < Data
	if instructionsSection >= contextSection || contextSection >= dataSection {
		t.Errorf("Section ordering violated: instructions@%d < context@%d < data@%d",
			instructionsSection, contextSection, dataSection)
	}

	// 3f. AgentName < Capability < Instruction (within context section)
	if agentIdx >= capIdx || capIdx >= instrIdx {
		t.Errorf("Within <context>, expected: AgentName < Capability < Instruction\n"+
			"  AgentName@%d, Capability@%d, Instruction@%d", agentIdx, capIdx, instrIdx)
	}

	// 3g. Result data must come after instruction
	if resultIdx < instrIdx {
		t.Error("Result data should appear after Instruction — <data> section follows <context>")
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

// --- Phase 8: Agent Input Trimming Tests ---

// TestTrimAgentInputParams_ScalarsUntouched verifies that scalar parameter values
// are never trimmed regardless of budget.
func TestTrimAgentInputParams_ScalarsUntouched(t *testing.T) {
	executor := NewSmartExecutor(nil)
	executor.SetAgentInputTrimmer(NewStructuralTrimmer(nil, nil), 100)

	params := map[string]interface{}{
		"symbol": "AMD",
		"count":  42.0,
		"active": true,
	}
	step := RoutingStep{StepID: "step-1", AgentName: "test", Instruction: "analyze stock"}
	result := executor.trimAgentInputParams(context.Background(), params, step)

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

// TestTrimAgentInputParams_SmallObjectUntouched verifies that complex objects
// below the budget pass through unchanged.
func TestTrimAgentInputParams_SmallObjectUntouched(t *testing.T) {
	executor := NewSmartExecutor(nil)
	executor.SetAgentInputTrimmer(NewStructuralTrimmer(nil, nil), 10000)

	params := map[string]interface{}{
		"data": map[string]interface{}{"price": 150.5, "currency": "USD"},
	}
	step := RoutingStep{StepID: "step-1", AgentName: "test", Instruction: "get price"}
	result := executor.trimAgentInputParams(context.Background(), params, step)

	data, ok := result["data"].(map[string]interface{})
	if !ok {
		t.Fatalf("Expected data map, got %T", result["data"])
	}
	if data["price"] != 150.5 {
		t.Errorf("Expected price=150.5, got %v", data["price"])
	}
}

// TestTrimAgentInputParams_LargeObjectTrimmed verifies that complex objects
// exceeding the budget are structurally trimmed.
func TestTrimAgentInputParams_LargeObjectTrimmed(t *testing.T) {
	bigData := make(map[string]interface{})
	for i := 0; i < 100; i++ {
		bigData[fmt.Sprintf("field_%d", i)] = strings.Repeat("x", 500)
	}

	executor := NewSmartExecutor(nil)
	executor.SetAgentInputTrimmer(NewStructuralTrimmer(nil, nil), 4096)

	params := map[string]interface{}{
		"symbol": "AMD",
		"data":   bigData,
	}
	step := RoutingStep{StepID: "step-1", AgentName: "stock-service", Instruction: "get stock financial metrics"}
	result := executor.trimAgentInputParams(context.Background(), params, step)

	// Symbol (scalar) should be untouched
	if result["symbol"] != "AMD" {
		t.Errorf("Expected symbol=AMD, got %v", result["symbol"])
	}

	// Data should be trimmed — re-serialize to check size
	trimmedData, err := json.Marshal(result["data"])
	if err != nil {
		t.Fatalf("Failed to marshal trimmed data: %v", err)
	}
	if len(trimmedData) > 5000 { // budget + some tolerance for re-serialization
		t.Errorf("Expected trimmed data <= ~4096 bytes, got %d", len(trimmedData))
	}
	originalData, _ := json.Marshal(bigData)
	if len(trimmedData) >= len(originalData) {
		t.Errorf("Expected trimmed data (%d) to be smaller than original (%d)", len(trimmedData), len(originalData))
	}
}

// TestTrimAgentInputParams_NilProcessor verifies that without a processor,
// parameters pass through unchanged regardless of size.
func TestTrimAgentInputParams_NilProcessor(t *testing.T) {
	executor := NewSmartExecutor(nil)
	// No SetAgentInputTrimmer call — processor is nil

	bigData := make(map[string]interface{})
	for i := 0; i < 100; i++ {
		bigData[fmt.Sprintf("field_%d", i)] = strings.Repeat("x", 500)
	}
	params := map[string]interface{}{"data": bigData}
	step := RoutingStep{StepID: "step-1", AgentName: "test", Instruction: "test"}
	result := executor.trimAgentInputParams(context.Background(), params, step)

	// Should return the same map (no trimming)
	resultData, _ := json.Marshal(result["data"])
	originalData, _ := json.Marshal(bigData)
	if len(resultData) != len(originalData) {
		t.Errorf("Expected unchanged data without processor, got %d vs %d", len(resultData), len(originalData))
	}
}

// TestTrimAgentInputParams_ZeroBudget verifies early return when budget is zero.
func TestTrimAgentInputParams_ZeroBudget(t *testing.T) {
	executor := NewSmartExecutor(nil)
	executor.SetAgentInputTrimmer(NewStructuralTrimmer(nil, nil), 0)

	bigData := make(map[string]interface{})
	for i := 0; i < 50; i++ {
		bigData[fmt.Sprintf("field_%d", i)] = strings.Repeat("x", 500)
	}
	params := map[string]interface{}{"data": bigData}
	step := RoutingStep{StepID: "step-1", AgentName: "test", Instruction: "test"}
	result := executor.trimAgentInputParams(context.Background(), params, step)

	// Zero budget → early return, original map returned unchanged
	resultData, _ := json.Marshal(result["data"])
	originalData, _ := json.Marshal(bigData)
	if len(resultData) != len(originalData) {
		t.Errorf("Expected unchanged data with zero budget, got %d vs %d", len(resultData), len(originalData))
	}
}

// TestTrimAgentInputParams_MixedParams verifies that a mix of scalar, small object,
// and large object parameters are handled correctly in a single call.
func TestTrimAgentInputParams_MixedParams(t *testing.T) {
	bigData := make(map[string]interface{})
	for i := 0; i < 100; i++ {
		bigData[fmt.Sprintf("field_%d", i)] = strings.Repeat("x", 500)
	}

	executor := NewSmartExecutor(nil)
	executor.SetAgentInputTrimmer(NewStructuralTrimmer(nil, nil), 4096)

	params := map[string]interface{}{
		"symbol":        "AMD",                                   // scalar — untouched
		"active":        true,                                    // scalar — untouched
		"price":         150.5,                                   // scalar — untouched
		"small_context": map[string]interface{}{"note": "short"}, // small object — untouched
		"data":          bigData,                                 // large object — trimmed
	}
	step := RoutingStep{StepID: "step-2", AgentName: "research-agent", Instruction: "analyze financials"}
	result := executor.trimAgentInputParams(context.Background(), params, step)

	// Scalars preserved
	if result["symbol"] != "AMD" {
		t.Errorf("Expected symbol=AMD, got %v", result["symbol"])
	}
	if result["active"] != true {
		t.Errorf("Expected active=true, got %v", result["active"])
	}
	if result["price"] != 150.5 {
		t.Errorf("Expected price=150.5, got %v", result["price"])
	}

	// Small object preserved
	smallCtx, ok := result["small_context"].(map[string]interface{})
	if !ok {
		t.Fatalf("Expected small_context to be map, got %T", result["small_context"])
	}
	if smallCtx["note"] != "short" {
		t.Errorf("Expected small_context.note=short, got %v", smallCtx["note"])
	}

	// Large object trimmed
	trimmedData, _ := json.Marshal(result["data"])
	originalData, _ := json.Marshal(bigData)
	if len(trimmedData) >= len(originalData) {
		t.Errorf("Expected data to be trimmed: original=%d, trimmed=%d", len(originalData), len(trimmedData))
	}
}

// TestTrimAgentInputParams_LargeArray verifies that array parameter values
// (not just maps) are trimmed when they exceed the budget.
func TestTrimAgentInputParams_LargeArray(t *testing.T) {
	bigArray := make([]interface{}, 100)
	for i := 0; i < 100; i++ {
		bigArray[i] = map[string]interface{}{
			"headline": fmt.Sprintf("Article %d: %s", i, strings.Repeat("x", 200)),
			"source":   "test",
		}
	}

	executor := NewSmartExecutor(nil)
	executor.SetAgentInputTrimmer(NewStructuralTrimmer(nil, nil), 4096)

	params := map[string]interface{}{
		"articles": bigArray,
	}
	step := RoutingStep{StepID: "step-2", AgentName: "sentiment-agent", Instruction: "analyze news sentiment"}
	result := executor.trimAgentInputParams(context.Background(), params, step)

	// Array should be trimmed (fewer items or smaller representation)
	trimmedJSON, _ := json.Marshal(result["articles"])
	originalJSON, _ := json.Marshal(bigArray)
	if len(trimmedJSON) >= len(originalJSON) {
		t.Errorf("Expected array to be trimmed: original=%d, trimmed=%d", len(originalJSON), len(trimmedJSON))
	}
}

// TestTrimAgentInputParams_NilValue verifies that nil parameter values
// (scalar) are passed through unchanged.
func TestTrimAgentInputParams_NilValue(t *testing.T) {
	executor := NewSmartExecutor(nil)
	executor.SetAgentInputTrimmer(NewStructuralTrimmer(nil, nil), 100)

	params := map[string]interface{}{
		"optional_field": nil,
		"name":           "test",
	}
	step := RoutingStep{StepID: "step-1", AgentName: "test", Instruction: "test"}
	result := executor.trimAgentInputParams(context.Background(), params, step)

	if result["optional_field"] != nil {
		t.Errorf("Expected nil to pass through, got %v", result["optional_field"])
	}
	if result["name"] != "test" {
		t.Errorf("Expected name=test, got %v", result["name"])
	}
}

// TestTrimAgentInputParams_ParseFailureFallback verifies that when the processor
// returns invalid JSON (even after annotation stripping), the original value is kept.
func TestTrimAgentInputParams_ParseFailureFallback(t *testing.T) {
	// Mock processor that returns invalid JSON
	processor := &captureProcessor{
		processFunc: func(ctx context.Context, result string, maxBytes int, stepCtx ResultProcessorContext) string {
			return "this is not valid json at all"
		},
	}

	executor := NewSmartExecutor(nil)
	executor.SetAgentInputTrimmer(processor, 100)

	originalData := map[string]interface{}{"important": strings.Repeat("x", 200)}
	params := map[string]interface{}{"data": originalData}
	step := RoutingStep{StepID: "step-1", AgentName: "test", Instruction: "test"}
	result := executor.trimAgentInputParams(context.Background(), params, step)

	// Fallback: original value preserved (not corrupted)
	data, ok := result["data"].(map[string]interface{})
	if !ok {
		t.Fatalf("Expected original map preserved on parse failure, got %T", result["data"])
	}
	if data["important"] != originalData["important"] {
		t.Error("Expected original data preserved on parse failure")
	}
}

// TestTrimAgentInputParams_ParseFailureWithLogger verifies that the warn log
// is emitted when re-parsing trimmed data fails and a logger is configured.
func TestTrimAgentInputParams_ParseFailureWithLogger(t *testing.T) {
	// Mock processor that returns invalid JSON
	processor := &captureProcessor{
		processFunc: func(ctx context.Context, result string, maxBytes int, stepCtx ResultProcessorContext) string {
			return "not json"
		},
	}

	logger := &mockLogger{
		warnFunc: func(msg string, fields map[string]interface{}) {},
	}

	executor := NewSmartExecutor(nil)
	executor.SetAgentInputTrimmer(processor, 100)
	executor.SetLogger(logger)

	originalData := map[string]interface{}{"key": strings.Repeat("x", 200)}
	params := map[string]interface{}{"data": originalData}
	step := RoutingStep{StepID: "step-1", AgentName: "test", Instruction: "test"}
	executor.trimAgentInputParams(context.Background(), params, step)

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

// TestTrimAgentInputParams_SuccessWithLogger verifies that the info log
// is emitted when a parameter is successfully trimmed and a logger is configured.
func TestTrimAgentInputParams_SuccessWithLogger(t *testing.T) {
	logger := &mockLogger{
		infoFunc: func(msg string, fields map[string]interface{}) {},
	}

	executor := NewSmartExecutor(nil)
	executor.SetAgentInputTrimmer(NewStructuralTrimmer(nil, nil), 4096)
	executor.SetLogger(logger)

	bigData := make(map[string]interface{})
	for i := 0; i < 100; i++ {
		bigData[fmt.Sprintf("field_%d", i)] = strings.Repeat("x", 500)
	}
	params := map[string]interface{}{"data": bigData}
	step := RoutingStep{StepID: "step-1", AgentName: "stock-service", Instruction: "analyze"}
	executor.trimAgentInputParams(context.Background(), params, step)

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

// TestTrimAgentInputParams_KeywordContext verifies that the step instruction
// is passed to the processor as keyword context.
func TestTrimAgentInputParams_KeywordContext(t *testing.T) {
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

	executor := NewSmartExecutor(nil)
	executor.SetAgentInputTrimmer(processor, 4096)

	bigData := make(map[string]interface{})
	for i := 0; i < 100; i++ {
		bigData[fmt.Sprintf("field_%d", i)] = strings.Repeat("x", 500)
	}
	params := map[string]interface{}{"data": bigData}
	step := RoutingStep{
		StepID:      "step-2",
		AgentName:   "research-agent",
		Instruction: "Analyze AMD financial metrics and provide investment thesis",
	}
	executor.trimAgentInputParams(context.Background(), params, step)

	if capturedInstruction != step.Instruction {
		t.Errorf("Expected instruction=%q, got %q", step.Instruction, capturedInstruction)
	}
}

// TestTrimAgentInputParams_AnnotationStripped verifies that the trimmer's
// "[trimmed: ...]" annotation is stripped before re-parsing for HTTP serialization.
func TestTrimAgentInputParams_AnnotationStripped(t *testing.T) {
	// Use a mock processor that always appends an annotation
	processor := &captureProcessor{
		processFunc: func(ctx context.Context, result string, maxBytes int, stepCtx ResultProcessorContext) string {
			return `{"key":"value"}` + "\n[trimmed: 1/10 fields kept, 9 dropped]"
		},
	}

	executor := NewSmartExecutor(nil)
	executor.SetAgentInputTrimmer(processor, 100)

	// Create a param that exceeds budget to trigger trimming
	bigData := map[string]interface{}{"large": strings.Repeat("x", 200)}
	params := map[string]interface{}{"data": bigData}
	step := RoutingStep{StepID: "step-1", AgentName: "test", Instruction: "test"}
	result := executor.trimAgentInputParams(context.Background(), params, step)

	// The result should be a clean parsed object, not a string with annotation
	data, ok := result["data"].(map[string]interface{})
	if !ok {
		t.Fatalf("Expected parsed map after annotation stripping, got %T: %v", result["data"], result["data"])
	}
	if data["key"] != "value" {
		t.Errorf("Expected key=value, got %v", data["key"])
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

	results, metaMap := ProcessMultipleForBudget(context.Background(), trimmer, steps, 10000, 5000)

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

	_, metaMap := ProcessMultipleForBudget(context.Background(), trimmer, steps, 5000, 5000)

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

	_, metaMap := ProcessMultipleForBudget(context.Background(), trimmer, steps, 20, 20)

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
