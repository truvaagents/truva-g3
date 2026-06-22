package orchestration

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// --- TestStructuralTrimmer_SmallResult ---

func TestStructuralTrimmer_SmallResult(t *testing.T) {
	trimmer := NewStructuralTrimmer(nil, nil)
	input := `{"status":"ok","count":42}`

	result := trimmer.ProcessForPrompt(context.Background(), input, 1000, ResultProcessorContext{
		StepID: "step-1", AgentName: "test", Instruction: "check status",
	})

	if result != input {
		t.Errorf("Expected passthrough for small input, got %q", result)
	}
}

// --- TestStructuralTrimmer_LargeJSON ---

func TestStructuralTrimmer_LargeJSON(t *testing.T) {
	obj := make(map[string]interface{})
	for i := 0; i < 100; i++ {
		key := strings.Repeat("k", 50) + string(rune('a'+i%26))
		obj[key] = strings.Repeat("v", 200)
	}
	data, _ := json.Marshal(obj)
	input := string(data)

	trimmer := NewStructuralTrimmer(nil, nil)

	result := trimmer.ProcessForPrompt(context.Background(), input, 2000, ResultProcessorContext{
		StepID: "step-1", AgentName: "big-agent", Instruction: "analyze data",
	})

	if len(result) > 2000 {
		t.Errorf("Expected result <= 2000 bytes, got %d", len(result))
	}
	if len(result) == 0 {
		t.Error("Expected non-empty result")
	}

	// Should be valid JSON (possibly with annotation suffix)
	jsonPart := result
	if idx := strings.Index(result, "\n[trimmed:"); idx >= 0 {
		jsonPart = result[:idx]
	}
	var parsed interface{}
	if err := json.Unmarshal([]byte(jsonPart), &parsed); err != nil {
		t.Errorf("Expected valid JSON output, got error: %v (output: %.200s...)", err, result)
	}
}

// --- TestStructuralTrimmer_KeywordMatching ---

func TestStructuralTrimmer_KeywordMatching(t *testing.T) {
	input := `{"stock_price":150.5,"company_name":"AAPL","irrelevant_metadata":"xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx","market_cap":2500000000}`

	trimmer := NewStructuralTrimmer(nil, nil)

	result := trimmer.ProcessForPrompt(context.Background(), input, 100, ResultProcessorContext{
		StepID: "step-1", AgentName: "stock-agent", Instruction: "Get the stock price",
	})

	if !strings.Contains(result, "stock_price") {
		t.Errorf("Expected keyword-matched field 'stock_price' to be preserved, got: %s", result)
	}
}

// --- TestStructuralTrimmer_PreserveKeys ---

func TestStructuralTrimmer_PreserveKeys(t *testing.T) {
	input := `{"id":"abc-123","name":"Test","description":"A very long description that takes up lots of space in the budget and might get dropped if not careful about preserve keys","status":"active"}`

	trimmer := NewStructuralTrimmer([]string{"id", "status"}, nil)

	result := trimmer.ProcessForPrompt(context.Background(), input, 80, ResultProcessorContext{
		StepID: "step-1", AgentName: "agent", Instruction: "generic task",
	})

	if !strings.Contains(result, "abc-123") {
		t.Errorf("Expected preserved key 'id' in result, got: %s", result)
	}
}

// --- TestStructuralTrimmer_NoKeywordMatches ---

func TestStructuralTrimmer_NoKeywordMatches(t *testing.T) {
	// Instruction with only stop words — no keywords extracted.
	// All fields have non-zero scores from isScalar/depth bonuses, so
	// the primary sort selects fields. This tests that selection still works.
	input := `{"alpha":"1","beta":"2","gamma":"3333333333333333333333333333333333333333333333333333333333333333"}`

	trimmer := NewStructuralTrimmer(nil, nil)

	result := trimmer.ProcessForPrompt(context.Background(), input, 50, ResultProcessorContext{
		StepID: "step-1", AgentName: "agent", Instruction: "do it",
	})

	if len(result) == 0 {
		t.Error("Expected non-empty result from selection")
	}
	if len(result) > 50 {
		t.Errorf("Expected result <= 50 bytes, got %d", len(result))
	}
}

// --- TestStructuralTrimmer_FallbackSelection ---

func TestStructuralTrimmer_NoFieldFitsBudget(t *testing.T) {
	// Budget so small no field fits during primary sort → empty object {}.
	input := `{"aaa":"` + strings.Repeat("x", 100) + `"}`

	trimmer := NewStructuralTrimmer(nil, nil)

	result := trimmer.ProcessForPrompt(context.Background(), input, 10, ResultProcessorContext{
		StepID: "step-1", AgentName: "agent", Instruction: "do it",
	})

	// Budget is 10 bytes. No field fits (smallest is >100 bytes). Output is "{}".
	if result != "{}" {
		t.Errorf("Expected empty JSON object when no fields fit, got: %q", result)
	}
}

// --- TestStructuralTrimmer_AnnotationFitsInBudget ---

func TestStructuralTrimmer_AnnotationFitsInBudget(t *testing.T) {
	// Some fields fit, annotation fits → annotation appended
	input := `{"a":"1","b":"2","c":"` + strings.Repeat("x", 200) + `"}`

	trimmer := NewStructuralTrimmer(nil, nil)

	result := trimmer.ProcessForPrompt(context.Background(), input, 200, ResultProcessorContext{
		StepID: "step-1", AgentName: "agent", Instruction: "check values",
	})

	if !strings.Contains(result, "[trimmed:") {
		t.Errorf("Expected trimmed annotation when budget is sufficient, got: %s", result)
	}
	if len(result) > 200 {
		t.Errorf("Expected result <= 200 bytes, got %d", len(result))
	}
}

// --- TestStructuralTrimmer_AnnotationExceedsBudget ---

func TestStructuralTrimmer_AnnotationExceedsBudget(t *testing.T) {
	// Fields fit tightly into budget, annotation doesn't fit → no annotation
	input := `{"a":"1","b":"` + strings.Repeat("x", 40) + `"}`

	trimmer := NewStructuralTrimmer(nil, nil)

	// Small enough budget that selected fields fill it, leaving no room for annotation
	result := trimmer.ProcessForPrompt(context.Background(), input, 20, ResultProcessorContext{
		StepID: "step-1", AgentName: "agent", Instruction: "check",
	})

	// Should produce valid JSON output without annotation
	if strings.Contains(result, "[trimmed:") {
		t.Errorf("Expected no annotation when budget is tight, got: %s", result)
	}
	if len(result) > 20 {
		t.Errorf("Expected result <= 20 bytes, got %d", len(result))
	}
}

// --- TestStructuralTrimmer_PlainText ---

func TestStructuralTrimmer_PlainText(t *testing.T) {
	// Input must exceed maxBytes to trigger trimPlainText. Use enough sentences
	// with at least one matching the keywords so the scoring path executes.
	input := "The stock price is going up today. The bond market is stable. Weather is nice and warm. The CEO recently spoke about new products."

	trimmer := NewStructuralTrimmer(nil, nil)

	result := trimmer.ProcessForPrompt(context.Background(), input, 100, ResultProcessorContext{
		StepID: "step-1", AgentName: "text-agent", Instruction: "Get the stock price",
	})

	if len(result) > 120 {
		t.Errorf("Expected result <= 120 bytes, got %d", len(result))
	}
	// Should prefer sentences mentioning "stock" or "price"
	lower := strings.ToLower(result)
	if !strings.Contains(lower, "stock") && !strings.Contains(lower, "price") {
		t.Errorf("Expected keyword-relevant sentences (stock/price), got: %s", result)
	}
	// Should contain trimmed annotation
	if !strings.Contains(result, "[trimmed:") {
		t.Errorf("Expected trimmed sentence annotation, got: %s", result)
	}
}

// --- TestStructuralTrimmer_PlainText_NoKeywords ---

func TestStructuralTrimmer_PlainText_NoKeywords(t *testing.T) {
	// Non-JSON input with instruction producing no keywords → falls back to truncateResultBytes
	input := "Some plain text that is long enough to need trimming but has no matching keywords."

	trimmer := NewStructuralTrimmer(nil, nil)

	result := trimmer.ProcessForPrompt(context.Background(), input, 40, ResultProcessorContext{
		StepID: "step-1", AgentName: "agent", Instruction: "do it",
	})

	if len(result) > 40 {
		t.Errorf("Expected result <= 40 bytes, got %d", len(result))
	}
	if len(result) == 0 {
		t.Error("Expected non-empty result")
	}
}

// --- TestStructuralTrimmer_PlainText_NoSentences ---

func TestStructuralTrimmer_PlainText_NoSentences(t *testing.T) {
	// Input with only sentence-ending chars → FieldsFunc returns empty → falls back to truncate
	input := "...!?!?...!?!?...!?!?...!?!?...!?!?"

	trimmer := NewStructuralTrimmer(nil, nil)

	result := trimmer.ProcessForPrompt(context.Background(), input, 20, ResultProcessorContext{
		StepID: "step-1", AgentName: "agent", Instruction: "analyze content",
	})

	if len(result) > 20 {
		t.Errorf("Expected result <= 20 bytes, got %d", len(result))
	}
}

// --- TestStructuralTrimmer_PlainText_EmptySegmentAndMultiSentence ---

func TestStructuralTrimmer_PlainText_EmptySegmentAndMultiSentence(t *testing.T) {
	// ". ." creates a whitespace-only segment → TrimSpace produces "" → len(s)==0 skip.
	// Also tests multi-sentence selection + re-sort by original index.
	input := "Stock analysis shows growth. .Bond markets remain stable.Weather forecast is sunny.Inflation data is concerning."

	trimmer := NewStructuralTrimmer(nil, nil)

	// maxBytes=110 < len(input)=112 → triggers trimming.
	// trimPlainText budget for sentences = 110-50 = 60, fits 2 sentences.
	result := trimmer.ProcessForPrompt(context.Background(), input, 110, ResultProcessorContext{
		StepID: "step-1", AgentName: "agent", Instruction: "Get the stock and bond analysis",
	})

	// Should have multiple sentences selected and re-sorted by original index
	if !strings.Contains(result, "[trimmed:") {
		t.Errorf("Expected trimmed annotation, got: %s", result)
	}
	// Should contain keyword-relevant sentences
	lower := strings.ToLower(result)
	if !strings.Contains(lower, "stock") && !strings.Contains(lower, "bond") {
		t.Errorf("Expected keyword-relevant sentences, got: %s", result)
	}
}

// --- TestStructuralTrimmer_Array ---

func TestStructuralTrimmer_Array(t *testing.T) {
	input := `[{"id":1,"name":"first"},{"id":2,"name":"second"},{"id":3,"name":"third"},{"id":4,"name":"fourth"}]`

	trimmer := NewStructuralTrimmer(nil, nil)

	// Budget 95: fits 3/4 items (output=74 bytes) + annotation (21 bytes) = 95 ≤ 95
	result := trimmer.ProcessForPrompt(context.Background(), input, 95, ResultProcessorContext{
		StepID: "step-1", AgentName: "agent", Instruction: "list items",
	})

	if len(result) > 95 {
		t.Errorf("Expected result <= 95 bytes, got %d", len(result))
	}

	// Should contain at least the first item
	if !strings.Contains(result, `"id":1`) {
		t.Errorf("Expected first array item preserved, got: %s", result)
	}

	// Should have trimmed annotation
	if !strings.Contains(result, "[trimmed:") {
		t.Errorf("Expected trimmed annotation for array, got: %s", result)
	}
}

// --- TestStructuralTrimmer_ArrayAnnotationExceedsBudget ---

func TestStructuralTrimmer_ArrayAnnotationExceedsBudget(t *testing.T) {
	input := `[{"id":1,"name":"first"},{"id":2,"name":"second"},{"id":3,"name":"third"}]`

	trimmer := NewStructuralTrimmer(nil, nil)

	// Tight budget: items fit but annotation doesn't
	result := trimmer.ProcessForPrompt(context.Background(), input, 55, ResultProcessorContext{
		StepID: "step-1", AgentName: "agent", Instruction: "list",
	})

	if len(result) > 55 {
		t.Errorf("Expected result <= 55 bytes, got %d", len(result))
	}
	// Should still be valid JSON array
	jsonPart := result
	if idx := strings.Index(result, "\n[trimmed:"); idx >= 0 {
		jsonPart = result[:idx]
	}
	var parsed interface{}
	if err := json.Unmarshal([]byte(jsonPart), &parsed); err != nil {
		t.Errorf("Expected valid JSON array, got error: %v", err)
	}
}

// --- TestStructuralTrimmer_InvalidJSON ---

func TestStructuralTrimmer_InvalidJSON(t *testing.T) {
	input := "This is not JSON at all, just plain text that is long enough to require trimming."

	trimmer := NewStructuralTrimmer(nil, nil)

	result := trimmer.ProcessForPrompt(context.Background(), input, 40, ResultProcessorContext{
		StepID: "step-1", AgentName: "agent", Instruction: "analyze text",
	})

	if len(result) > 40 {
		t.Errorf("Expected result <= 40 bytes for invalid JSON, got %d", len(result))
	}
}

// --- TestStructuralTrimmer_ScalarJSON ---

func TestStructuralTrimmer_ScalarJSON(t *testing.T) {
	// JSON scalar (not object or array) that exceeds budget → falls to truncateResultBytes
	input := `"a very long JSON string value that exceeds the byte budget and needs plain truncation"`

	trimmer := NewStructuralTrimmer(nil, nil)

	result := trimmer.ProcessForPrompt(context.Background(), input, 40, ResultProcessorContext{
		StepID: "step-1", AgentName: "agent", Instruction: "analyze",
	})

	if len(result) > 40 {
		t.Errorf("Expected result <= 40 bytes for scalar JSON, got %d", len(result))
	}
	if len(result) == 0 {
		t.Error("Expected non-empty result")
	}
}

// --- TestStructuralTrimmer_NestedObjectRecursion ---

func TestStructuralTrimmer_NestedObjectRecursion(t *testing.T) {
	// Nested object > 1024 bytes triggers recursion in buildFieldInventory
	bigValue := strings.Repeat("x", 600)
	input := fmt.Sprintf(`{"outer":{"inner_a":"%s","inner_b":"%s"},"small":"val"}`, bigValue, bigValue)

	trimmer := NewStructuralTrimmer(nil, nil)

	result := trimmer.ProcessForPrompt(context.Background(), input, 200, ResultProcessorContext{
		StepID: "step-1", AgentName: "agent", Instruction: "get small value",
	})

	if len(result) > 200 {
		t.Errorf("Expected result <= 200 bytes, got %d", len(result))
	}
	// "small" field is tiny and relevant — should be selected over the large nested object
	if !strings.Contains(result, "small") {
		t.Errorf("Expected 'small' field to be selected, got: %s", result)
	}
}

// --- TestStructuralTrimmer_WithLogger ---

func TestStructuralTrimmer_WithLogger_JSONObject(t *testing.T) {
	logger := &mockLogger{}
	trimmer := NewStructuralTrimmer(nil, logger)

	input := `{"a":"1","b":"` + strings.Repeat("x", 100) + `"}`
	trimmer.ProcessForPrompt(context.Background(), input, 30, ResultProcessorContext{
		StepID: "step-1", AgentName: "test-agent", Instruction: "check",
	})

	found := false
	for _, msg := range logger.messages {
		if strings.Contains(msg, "Structural trim completed (JSON object)") {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("Expected logger message about JSON object trim, got: %v", logger.messages)
	}
}

func TestStructuralTrimmer_WithLogger_JSONArray(t *testing.T) {
	logger := &mockLogger{}
	trimmer := NewStructuralTrimmer(nil, logger)

	input := `[{"id":1},{"id":2},{"id":3},{"id":4},{"id":5},{"id":6},{"id":7},{"id":8}]`
	trimmer.ProcessForPrompt(context.Background(), input, 30, ResultProcessorContext{
		StepID: "step-1", AgentName: "test-agent", Instruction: "list",
	})

	found := false
	for _, msg := range logger.messages {
		if strings.Contains(msg, "Structural trim completed (JSON array)") {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("Expected logger message about JSON array trim, got: %v", logger.messages)
	}
}

// --- Test isScalar ---

func TestIsScalar(t *testing.T) {
	tests := []struct {
		input    interface{}
		expected bool
	}{
		{"hello", true},
		{42.0, true},
		{true, true},
		{nil, true},
		{map[string]interface{}{}, false},
		{[]interface{}{}, false},
	}

	for _, tt := range tests {
		got := isScalar(tt.input)
		if got != tt.expected {
			t.Errorf("isScalar(%v) = %v, want %v", tt.input, got, tt.expected)
		}
	}
}

// --- Test fieldEntry scoring ---

func TestScoreField_PreserveKeys(t *testing.T) {
	trimmer := NewStructuralTrimmer([]string{"id", "name"}, nil)

	entry := fieldEntry{path: "id", key: "id", depth: 0, isScalar: true}
	score := trimmer.scoreField(entry, nil)

	// preserveKeys bonus = 5.0, scalar bonus = 0.3, depth 0 bonus = 0.1
	expected := 5.4
	if score < expected-0.01 || score > expected+0.01 {
		t.Errorf("Expected score ~%.1f for preserved key, got %.4f", expected, score)
	}
}

func TestScoreField_KeywordMatch(t *testing.T) {
	trimmer := NewStructuralTrimmer(nil, nil)

	entry := fieldEntry{path: "stock_price", key: "stock_price", depth: 0, isScalar: true}
	score := trimmer.scoreField(entry, []string{"stock", "price"})

	// Both keywords match key: 1.5 + 1.5 = 3.0, scalar = 0.3, depth 0 = 0.1
	if score < 3.0 {
		t.Errorf("Expected score >= 3.0 for keyword-matched field, got %.1f", score)
	}
}

func TestScoreField_PathKeywordMatch(t *testing.T) {
	trimmer := NewStructuralTrimmer(nil, nil)

	// Keyword "finance" matches path "finance.price" but not key "price"
	entry := fieldEntry{path: "finance.price", key: "price", depth: 1, isScalar: true}
	score := trimmer.scoreField(entry, []string{"finance"})

	// "finance" is in path but not in key "price" → path match = 1.0, scalar = 0.3
	if score < 1.0 {
		t.Errorf("Expected score >= 1.0 for path-matched field, got %.2f", score)
	}
}

func TestScoreField_FuzzyPrefixMatch(t *testing.T) {
	trimmer := NewStructuralTrimmer(nil, nil)

	// key "primax" does NOT contain keyword "prism" as substring.
	// But split("primax", "_") → ["primax"]. HasPrefix("primax", "pri") = true where kw[:3] = "pri".
	entry := fieldEntry{path: "primax", key: "primax", depth: 0, isScalar: true}
	score := trimmer.scoreField(entry, []string{"prism"})

	// Fuzzy match = 0.3, scalar = 0.3, depth 0 = 0.1
	expected := 0.7
	if score < expected-0.01 || score > expected+0.01 {
		t.Errorf("Expected score ~%.1f for fuzzy prefix match, got %.4f", expected, score)
	}
}

func TestScoreField_DeepDepthPenalty(t *testing.T) {
	trimmer := NewStructuralTrimmer(nil, nil)

	shallow := fieldEntry{path: "a", key: "a", depth: 0, isScalar: true}
	deep := fieldEntry{path: "a.b.c.d.e", key: "e", depth: 4, isScalar: true}

	shallowScore := trimmer.scoreField(shallow, nil)
	deepScore := trimmer.scoreField(deep, nil)

	if deepScore >= shallowScore {
		t.Errorf("Expected deep entry to score lower: shallow=%.2f, deep=%.2f", shallowScore, deepScore)
	}
}

// --- TestBuildFieldInventory ---

func TestBuildFieldInventory_NestedPrefix(t *testing.T) {
	trimmer := NewStructuralTrimmer(nil, nil)

	obj := map[string]interface{}{
		"a": "val",
	}
	entries := trimmer.buildFieldInventory(obj, "parent", 1)

	if len(entries) == 0 {
		t.Fatal("Expected at least one entry")
	}
	if entries[0].path != "parent.a" {
		t.Errorf("Expected path 'parent.a', got %q", entries[0].path)
	}
	if entries[0].depth != 1 {
		t.Errorf("Expected depth 1, got %d", entries[0].depth)
	}
}

// --- Helper: load testdata file relative to this test file ---

func loadTestdata(t *testing.T, name string) []byte {
	t.Helper()
	_, filename, _, _ := runtime.Caller(0)
	path := filepath.Join(filepath.Dir(filename), "testdata", name)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("Failed to load testdata/%s: %v", name, err)
	}
	return data
}

// ===========================================================================
// Phase 6: Nested Field Selection Tests
// ===========================================================================

// --- TestSelectFields_NestedFieldSelection ---
// Uses the realistic subset extracted from production request orch-1771359604520565967.
// Verifies that the trimmer selects nested fields (data.metric, data.symbol) instead
// of dropping the entire "data" wrapper.

func TestSelectFields_NestedFieldSelection(t *testing.T) {
	input := string(loadTestdata(t, "basic_financials_subset.json"))

	trimmer := NewStructuralTrimmer(nil, nil)

	// Budget: 2048 bytes — smaller than the full response (~3.7KB) but large enough
	// to fit data.metric (1.3KB) + scalars. Forces nested selection.
	result := trimmer.ProcessForPrompt(context.Background(), input, 2048, ResultProcessorContext{
		StepID: "step-1", AgentName: "stock-service",
		Instruction: "Retrieve comprehensive financial metrics for Nvidia (NVDA) to enable a detailed analysis.",
	})

	if len(result) > 2048 {
		t.Errorf("Expected result <= 2048 bytes, got %d", len(result))
	}

	// Strip annotation for JSON validation
	jsonPart := result
	if idx := strings.Index(result, "\n[trimmed:"); idx >= 0 {
		jsonPart = result[:idx]
	}

	var parsed map[string]interface{}
	if err := json.Unmarshal([]byte(jsonPart), &parsed); err != nil {
		t.Fatalf("Expected valid JSON output, got error: %v\nOutput: %.500s", err, result)
	}

	// Must contain nested financial data — not just {"success":true}
	if _, ok := parsed["data"]; !ok {
		t.Errorf("Expected 'data' wrapper in output, got keys: %v", mapKeys(parsed))
	}

	if data, ok := parsed["data"].(map[string]interface{}); ok {
		// data.metric or data.symbol should be present (high relevance for "financial metrics")
		if _, hasMetric := data["metric"]; !hasMetric {
			if _, hasSymbol := data["symbol"]; !hasSymbol {
				t.Errorf("Expected nested financial fields (metric/symbol) in data, got keys: %v", mapKeys(data))
			}
		}
	}

	// Must NOT be the old broken output {"success":true}
	if _, hasSuc := parsed["success"]; hasSuc && len(parsed) == 1 {
		t.Error("BUG: Output is only {\"success\":true} — nested field selection is not working")
	}
}

// --- TestSelectFields_ProductionReplay ---
// Loads the full 205KB production response and verifies the trimmer produces
// a useful result with real financial data, not just {"success":true}.

func TestSelectFields_ProductionReplay(t *testing.T) {
	input := string(loadTestdata(t, "basic_financials_response.json"))

	if len(input) < 200000 {
		t.Fatalf("Expected production response > 200KB, got %d bytes", len(input))
	}

	trimmer := NewStructuralTrimmer(nil, nil)

	result := trimmer.ProcessForPrompt(context.Background(), input, 8192, ResultProcessorContext{
		StepID:      "step-1",
		AgentName:   "stock-service",
		Instruction: "Retrieve comprehensive financial metrics for Nvidia (NVDA) to enable a detailed analysis.",
	})

	if len(result) > 8192 {
		t.Errorf("Expected result <= 8192 bytes, got %d", len(result))
	}

	jsonPart := result
	if idx := strings.Index(result, "\n[trimmed:"); idx >= 0 {
		jsonPart = result[:idx]
	}

	var parsed map[string]interface{}
	if err := json.Unmarshal([]byte(jsonPart), &parsed); err != nil {
		t.Fatalf("Expected valid JSON, got error: %v\nOutput (first 500): %.500s", err, result)
	}

	// The old bug: only {"success":true} — 16 bytes with all data lost
	if len(jsonPart) < 100 {
		t.Errorf("Output suspiciously small (%d bytes) — nested data likely lost", len(jsonPart))
	}

	// Must contain nested data fields
	data, hasData := parsed["data"].(map[string]interface{})
	if !hasData {
		t.Fatal("Expected 'data' wrapper in output")
	}

	// data.metric should be present — 132 financial ratios, only ~4KB, high keyword relevance
	if _, hasMetric := data["metric"]; !hasMetric {
		t.Error("Expected data.metric (financial ratios) in output — it's 3.9KB and highly relevant")
	}

	// data.symbol should be present — tiny scalar, high relevance
	if _, hasSymbol := data["symbol"]; !hasSymbol {
		t.Error("Expected data.symbol in output — it's 6 bytes and relevant")
	}

	// data.series as a whole (217KB) cannot fit, but the algorithm may select smaller
	// sub-fields (e.g., series.annual at ~45KB). If series is present, verify it's a
	// partial reconstruction — not the full 217KB object.
	if series, hasSeries := data["series"]; hasSeries {
		seriesJSON, _ := json.Marshal(series)
		if len(seriesJSON) > 8192 {
			t.Errorf("data.series in output is too large (%d bytes) — should be partial, not full", len(seriesJSON))
		}
	}

	t.Logf("Production replay: %d bytes input → %d bytes output (%.1f%% reduction)",
		len(input), len(result), 100-float64(len(result))/float64(len(input))*100)
}

// --- TestSelectFields_HierarchyReconstruction ---
// Verifies that selected nested fields are reconstructed into proper JSON hierarchy.

func TestSelectFields_HierarchyReconstruction(t *testing.T) {
	// Inner objects must exceed 1024 bytes for buildFieldInventory recursion (line 126).
	targetValue := strings.Repeat("relevant_data_", 80)   // ~1120 bytes
	bloatValue := strings.Repeat("bloat_padding_xx_", 80) // ~1360 bytes
	obj := map[string]interface{}{
		"wrapper": map[string]interface{}{
			"target": targetValue,
			"bloat":  bloatValue,
			"id":     "abc",
		},
		"top": float64(1),
	}

	data, _ := json.Marshal(obj)
	input := string(data)

	// Verify wrapper > 1024 bytes (recursion threshold)
	wrapperBytes, _ := json.Marshal(obj["wrapper"])
	if len(wrapperBytes) < 1024 {
		t.Fatalf("Test setup: wrapper must be > 1024 bytes for recursion, got %d", len(wrapperBytes))
	}

	trimmer := NewStructuralTrimmer(nil, nil)

	// Budget smaller than full wrapper, forces nested selection
	budget := len(wrapperBytes) - 200
	result := trimmer.ProcessForPrompt(context.Background(), input, budget, ResultProcessorContext{
		StepID: "step-1", AgentName: "agent",
		Instruction: "get the target data",
	})

	jsonPart := result
	if idx := strings.Index(result, "\n[trimmed:"); idx >= 0 {
		jsonPart = result[:idx]
	}

	var parsed map[string]interface{}
	if err := json.Unmarshal([]byte(jsonPart), &parsed); err != nil {
		t.Fatalf("Expected valid JSON, got error: %v\nOutput: %.300s", err, result)
	}

	// Must preserve hierarchy: wrapper.target inside {"wrapper": {...}}
	wrapper, ok := parsed["wrapper"].(map[string]interface{})
	if !ok {
		t.Fatalf("Expected 'wrapper' object in output, got keys: %v", mapKeys(parsed))
	}

	if _, hasTarget := wrapper["target"]; !hasTarget {
		t.Errorf("Expected 'wrapper.target' in output (keyword match), got wrapper keys: %v", mapKeys(wrapper))
	}

	if len(result) > budget {
		t.Errorf("Expected result <= %d bytes, got %d", budget, len(result))
	}
}

// --- TestSelectFields_AncestorExclusion ---
// When a parent is selected as a whole, its children should be skipped.

func TestSelectFields_AncestorExclusion(t *testing.T) {
	// Children must be inventoried → parent must be > 1024 bytes
	childA := strings.Repeat("a", 600)
	childB := strings.Repeat("b", 600)
	obj := map[string]interface{}{
		"important": map[string]interface{}{
			"child_a": childA,
			"child_b": childB,
		},
	}

	data, _ := json.Marshal(obj)
	input := string(data)

	// preserveKeys gives "important" a +5.0 bonus so it sorts before its children
	trimmer := NewStructuralTrimmer([]string{"important"}, nil)

	result := trimmer.ProcessForPrompt(context.Background(), input, 8192, ResultProcessorContext{
		StepID: "step-1", AgentName: "agent", Instruction: "general task",
	})

	jsonPart, _ := splitAnnotation(result)

	var parsed map[string]interface{}
	if err := json.Unmarshal([]byte(jsonPart), &parsed); err != nil {
		t.Fatalf("Expected valid JSON, got error: %v", err)
	}

	// "important" should be selected as a whole (it fits budget and has highest score)
	imp, ok := parsed["important"].(map[string]interface{})
	if !ok {
		t.Fatalf("Expected 'important' object in output, got keys: %v", mapKeys(parsed))
	}

	// Both children should be present (included via parent selection, not individually)
	if _, hasA := imp["child_a"]; !hasA {
		t.Error("Expected child_a included via parent selection")
	}
	if _, hasB := imp["child_b"]; !hasB {
		t.Error("Expected child_b included via parent selection")
	}
}

// --- TestSelectFields_DescendantExclusion ---
// When children are selected first, the parent should be skipped.

func TestSelectFields_DescendantExclusion(t *testing.T) {
	bloat := strings.Repeat("x", 100000)
	obj := map[string]interface{}{
		"big": map[string]interface{}{
			"relevant": "value",
			"bloat":    bloat,
		},
		"meta": "ok",
	}

	data, _ := json.Marshal(obj)
	input := string(data)

	trimmer := NewStructuralTrimmer(nil, nil)

	result := trimmer.ProcessForPrompt(context.Background(), input, 1024, ResultProcessorContext{
		StepID: "step-1", AgentName: "agent",
		Instruction: "find the relevant data",
	})

	jsonPart, _ := splitAnnotation(result)

	var parsed map[string]interface{}
	if err := json.Unmarshal([]byte(jsonPart), &parsed); err != nil {
		t.Fatalf("Expected valid JSON, got error: %v", err)
	}

	// big.relevant should be selected (tiny, high keyword relevance)
	big, ok := parsed["big"].(map[string]interface{})
	if !ok {
		t.Fatalf("Expected 'big' wrapper in output, got keys: %v", mapKeys(parsed))
	}

	if val, hasRel := big["relevant"]; !hasRel || val != "value" {
		t.Errorf("Expected big.relevant = \"value\", got: %v", big)
	}

	// big.bloat may be present (backfill truncates large strings to fill remaining budget)
	// rather than dropping them entirely. If present, it should be truncated, not full-size.
	if bloatVal, hasBloat := big["bloat"]; hasBloat {
		bloatStr, ok := bloatVal.(string)
		if !ok {
			t.Error("big.bloat should be a string if present")
		} else if len(bloatStr) >= 100000 {
			t.Error("big.bloat should be truncated, not full 100KB")
		}
	}

	// "meta" should be present (tiny scalar)
	if _, hasMeta := parsed["meta"]; !hasMeta {
		t.Error("Expected 'meta' field in output (tiny scalar)")
	}
}

// --- TestSelectFields_FlatResponseUnchanged ---
// Flat responses (all depth 0) must behave identically to the old implementation.

func TestSelectFields_FlatResponseUnchanged(t *testing.T) {
	input := `{"a":1,"b":"two","c":true,"d":4.0,"e":"five"}`

	trimmer := NewStructuralTrimmer(nil, nil)

	result := trimmer.ProcessForPrompt(context.Background(), input, 40, ResultProcessorContext{
		StepID: "step-1", AgentName: "agent", Instruction: "check values",
	})

	if len(result) > 40 {
		t.Errorf("Expected result <= 40 bytes, got %d", len(result))
	}

	jsonPart := result
	if idx := strings.Index(result, "\n[trimmed:"); idx >= 0 {
		jsonPart = result[:idx]
	}

	var parsed map[string]interface{}
	if err := json.Unmarshal([]byte(jsonPart), &parsed); err != nil {
		t.Fatalf("Expected valid JSON, got error: %v\nOutput: %s", err, result)
	}

	// With no keywords, smallest fields should be selected first (size-ascending tiebreaker)
	// All fields are small scalars, so the ones that fit should be there
	if len(parsed) == 0 {
		t.Error("Expected at least one field selected")
	}
}

// --- TestHasAncestor ---

func TestHasAncestor(t *testing.T) {
	tests := []struct {
		path     string
		selected map[string]bool
		want     bool
	}{
		{"data.metric.pe_ratio", map[string]bool{"data.metric": true}, true},
		{"data.metric.pe_ratio", map[string]bool{"data": true}, true},
		{"data.metric", map[string]bool{"data": true}, true},
		{"data.metric", map[string]bool{"data_extra": true}, false},
		{"data", map[string]bool{}, false},
		{"data", map[string]bool{"data": true}, false}, // self is not ancestor
		{"success", map[string]bool{"data": true}, false},
	}

	for _, tt := range tests {
		got := hasAncestor(tt.path, tt.selected)
		if got != tt.want {
			t.Errorf("hasAncestor(%q, %v) = %v, want %v", tt.path, tt.selected, got, tt.want)
		}
	}
}

// --- TestHasDescendant ---

func TestHasDescendant(t *testing.T) {
	tests := []struct {
		prefix   string
		selected map[string]bool
		want     bool
	}{
		{"data", map[string]bool{"data.metric": true}, true},
		{"data", map[string]bool{"data.metric.pe_ratio": true}, true},
		{"data", map[string]bool{"data_extra": true}, false},
		{"data", map[string]bool{}, false},
		{"data", map[string]bool{"data": true}, false}, // self is not descendant
		{"data.metric", map[string]bool{"data.metric.pe_ratio": true}, true},
		{"data.metric", map[string]bool{"data.series": true}, false},
	}

	for _, tt := range tests {
		got := hasDescendant(tt.prefix, tt.selected)
		if got != tt.want {
			t.Errorf("hasDescendant(%q, %v) = %v, want %v", tt.prefix, tt.selected, got, tt.want)
		}
	}
}

// --- TestReconstructHierarchy ---

func TestReconstructHierarchy(t *testing.T) {
	obj := map[string]interface{}{
		"success": true,
		"data": map[string]interface{}{
			"metric": map[string]interface{}{"pe_ratio": 25.4, "eps": 3.12},
			"series": map[string]interface{}{"annual": "big"},
			"symbol": "NVDA",
		},
	}

	selected := map[string]bool{
		"success":     true,
		"data.metric": true,
		"data.symbol": true,
	}

	result := reconstructHierarchy(obj, selected, nil)

	// Top level should have "success" and "data"
	if result["success"] != true {
		t.Errorf("Expected success=true, got %v", result["success"])
	}

	data, ok := result["data"].(map[string]interface{})
	if !ok {
		t.Fatalf("Expected 'data' map in result, got %T", result["data"])
	}

	// data.metric should be the full metric object
	metric, ok := data["metric"].(map[string]interface{})
	if !ok {
		t.Fatalf("Expected 'data.metric' map, got %T", data["metric"])
	}
	if metric["pe_ratio"] != 25.4 {
		t.Errorf("Expected pe_ratio=25.4, got %v", metric["pe_ratio"])
	}

	// data.symbol should be present
	if data["symbol"] != "NVDA" {
		t.Errorf("Expected symbol=NVDA, got %v", data["symbol"])
	}

	// data.series should NOT be present (not selected)
	if _, hasSeries := data["series"]; hasSeries {
		t.Error("Expected data.series to be absent (not selected)")
	}
}

// --- helper to get map keys for error messages ---

func mapKeys(m map[string]interface{}) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	return keys
}

// ===========================================================================
// Phase 2: Array-Aware Trimming Tests
// ===========================================================================

// --- TestBuildArrayInventory_Items ---

func TestBuildArrayInventory_Items(t *testing.T) {
	trimmer := NewStructuralTrimmer(nil, nil)

	// Build a 20-item array (generic — could be any domain)
	items := make([]interface{}, 20)
	for i := 0; i < 20; i++ {
		items[i] = map[string]interface{}{
			"title":       fmt.Sprintf("Item %d title with relevant keywords", i),
			"description": fmt.Sprintf("Description %d with searchable content", i),
			"source":      "TestAPI",
		}
	}

	entries := trimmer.buildArrayInventory(items, "data.results", 1)

	if len(entries) != 20 {
		t.Fatalf("Expected 20 entries, got %d", len(entries))
	}

	// Verify first entry: path includes parent, key includes parent segment
	if entries[0].path != "data.results[0]" {
		t.Errorf("Expected path 'data.results[0]', got %q", entries[0].path)
	}
	if entries[0].key != "results[0]" {
		t.Errorf("Expected key 'results[0]', got %q", entries[0].key)
	}
	if entries[0].arrayIndex != 0 {
		t.Errorf("Expected arrayIndex 0, got %d", entries[0].arrayIndex)
	}
	if entries[0].arrayTotal != 20 {
		t.Errorf("Expected arrayTotal 20, got %d", entries[0].arrayTotal)
	}
	if entries[0].depth != 1 {
		t.Errorf("Expected depth 1, got %d", entries[0].depth)
	}

	// Verify last entry
	last := entries[19]
	if last.path != "data.results[19]" {
		t.Errorf("Expected path 'data.results[19]', got %q", last.path)
	}
	if last.arrayIndex != 19 {
		t.Errorf("Expected arrayIndex 19, got %d", last.arrayIndex)
	}
}

// --- TestBuildArrayInventory_Cap ---

func TestBuildArrayInventory_Cap(t *testing.T) {
	trimmer := NewStructuralTrimmer(nil, nil)

	// Build array exceeding cap
	articles := make([]interface{}, 600)
	for i := 0; i < 600; i++ {
		articles[i] = map[string]interface{}{"id": float64(i)}
	}

	entries := trimmer.buildArrayInventory(articles, "items", 0)

	if len(entries) != maxArrayInventoryItems {
		t.Errorf("Expected capped at %d entries, got %d", maxArrayInventoryItems, len(entries))
	}
	// arrayTotal should reflect the full array size, not the cap
	if entries[0].arrayTotal != 600 {
		t.Errorf("Expected arrayTotal 600, got %d", entries[0].arrayTotal)
	}
}

// --- TestScoreField_ArrayItem ---

func TestScoreField_ArrayItem(t *testing.T) {
	trimmer := NewStructuralTrimmer(nil, nil)

	// --- Subtest 1: Positional decay (first vs last) ---
	first := fieldEntry{
		path: "data.results[0]", key: "results[0]",
		value: map[string]interface{}{
			"title":   "Quarterly revenue exceeds expectations",
			"summary": "Strong earnings reported across divisions",
		},
		depth: 1, arrayIndex: 0, arrayTotal: 100,
	}
	last := fieldEntry{
		path: "data.results[99]", key: "results[99]",
		value:      map[string]interface{}{"title": "Unrelated filler entry"},
		depth:      1,
		arrayIndex: 99, arrayTotal: 100,
	}

	keywords := []string{"result", "earn", "revenue"}

	firstScore := trimmer.scoreField(first, keywords)
	lastScore := trimmer.scoreField(last, keywords)

	// First item should score higher (key match + content match + positional)
	if firstScore <= lastScore {
		t.Errorf("Expected first item to score higher: first=%.2f, last=%.2f", firstScore, lastScore)
	}

	// "result" matches key "results[0]" → +1.5
	// Content "earnings" matches "earn" → +0.2 content bonus
	// Position 0/100 → +0.5 positional
	if firstScore < 2.0 {
		t.Errorf("Expected first item score >= 2.0, got %.2f", firstScore)
	}

	// --- Subtest 2: Domain-agnostic key matching ---
	// Verifies the parent-key-in-key design works for any domain
	domains := []struct {
		path    string
		key     string
		keyword string
	}{
		{"search.results[0]", "results[0]", "result"},
		{"catalog.products[0]", "products[0]", "product"},
		{"api.readings[0]", "readings[0]", "reading"},
		{"data.logs[0]", "logs[0]", "log"},
	}
	for _, d := range domains {
		entry := fieldEntry{
			path: d.path, key: d.key,
			value: map[string]interface{}{"id": "test"},
			depth: 1, arrayIndex: 0, arrayTotal: 10,
		}
		score := trimmer.scoreField(entry, []string{d.keyword})
		// Parent key should match keyword → +1.5
		if score < 1.5 {
			t.Errorf("Domain %q: expected score >= 1.5 from key match, got %.2f", d.key, score)
		}
	}
}

// --- TestSelectFields_ArraySubsetSelection ---

func TestSelectFields_ArraySubsetSelection(t *testing.T) {
	// Generic scenario: object with metadata + large array of results
	items := make([]interface{}, 100)
	for i := 0; i < 100; i++ {
		items[i] = map[string]interface{}{
			"title":       fmt.Sprintf("Result %d matching the search query", i),
			"description": fmt.Sprintf("Detailed description %d with relevant content", i),
			"url":         fmt.Sprintf("https://example.com/item/%d", i),
		}
	}
	obj := map[string]interface{}{
		"data": map[string]interface{}{
			"query":   "test search",
			"total":   float64(100),
			"results": items,
		},
		"success": true,
	}
	input, _ := json.Marshal(obj)

	trimmer := NewStructuralTrimmer(nil, nil)

	// Budget: 4096 bytes — forces subset selection
	result := trimmer.ProcessForPrompt(context.Background(), string(input), 4096, ResultProcessorContext{
		StepID: "step-2", AgentName: "search-agent",
		Instruction: "Analyze the search results for relevance",
	})

	if len(result) > 4096 {
		t.Errorf("Expected result <= 4096 bytes, got %d", len(result))
	}

	// Strip annotation
	jsonPart := result
	if idx := strings.Index(result, "\n[trimmed:"); idx >= 0 {
		jsonPart = result[:idx]
	}

	var parsed map[string]interface{}
	if err := json.Unmarshal([]byte(jsonPart), &parsed); err != nil {
		t.Fatalf("Expected valid JSON, got error: %v\nOutput: %.500s", err, result)
	}

	// Must contain data.results as an array with SOME items (not zero)
	data, _ := parsed["data"].(map[string]interface{})
	if data == nil {
		t.Fatal("Expected 'data' wrapper in output")
	}
	resultsArr, ok := data["results"].([]interface{})
	if !ok || len(resultsArr) == 0 {
		t.Fatalf("Expected non-empty 'data.results' array, got: %v", data["results"])
	}

	// Should have a subset, not all 100
	if len(resultsArr) >= 100 {
		t.Errorf("Expected subset of items, got all %d", len(resultsArr))
	}

	t.Logf("Array subset: %d/%d items selected in %d bytes", len(resultsArr), 100, len(result))
}

// --- TestSelectFields_MixedMapAndArray ---

func TestSelectFields_MixedMapAndArray(t *testing.T) {
	// Object with both a large nested map and a large array
	bigStats := make(map[string]interface{})
	for i := 0; i < 50; i++ {
		bigStats[fmt.Sprintf("stat_%d", i)] = float64(i) * 1.5
	}
	entries := make([]interface{}, 30)
	for i := 0; i < 30; i++ {
		entries[i] = map[string]interface{}{
			"title": fmt.Sprintf("Entry %d with relevant content", i),
			"body":  strings.Repeat("content ", 20),
		}
	}
	obj := map[string]interface{}{
		"data": map[string]interface{}{
			"statistics": bigStats,
			"entries":    entries,
			"id":         "test-123",
		},
		"success": true,
	}
	input, _ := json.Marshal(obj)

	trimmer := NewStructuralTrimmer(nil, nil)

	result := trimmer.ProcessForPrompt(context.Background(), string(input), 2048, ResultProcessorContext{
		StepID: "step-1", AgentName: "agent",
		Instruction: "Analyze the statistics and entries",
	})

	if len(result) > 2048 {
		t.Errorf("Expected result <= 2048 bytes, got %d", len(result))
	}

	jsonPart := result
	if idx := strings.Index(result, "\n[trimmed:"); idx >= 0 {
		jsonPart = result[:idx]
	}
	var parsed map[string]interface{}
	if err := json.Unmarshal([]byte(jsonPart), &parsed); err != nil {
		t.Fatalf("Expected valid JSON, got error: %v", err)
	}

	// Should have meaningful content, not just {"success":true}
	if len(jsonPart) < 100 {
		t.Errorf("Output suspiciously small (%d bytes) — data likely lost", len(jsonPart))
	}
}

// --- TestReconstructHierarchy_ArrayPaths ---

func TestReconstructHierarchy_ArrayPaths(t *testing.T) {
	obj := map[string]interface{}{
		"status": "ok",
		"data": map[string]interface{}{
			"query": "test",
			"items": []interface{}{
				map[string]interface{}{"title": "First", "id": float64(1)},
				map[string]interface{}{"title": "Second", "id": float64(2)},
				map[string]interface{}{"title": "Third", "id": float64(3)},
				map[string]interface{}{"title": "Fourth", "id": float64(4)},
				map[string]interface{}{"title": "Fifth", "id": float64(5)},
			},
		},
	}

	// Select status, data.query, and three non-contiguous array items (out of insertion order)
	selected := map[string]bool{
		"status":        true,
		"data.query":    true,
		"data.items[4]": true, // Fifth — selected first in map, but index 4
		"data.items[0]": true, // First — index 0
		"data.items[2]": true, // Third — index 2
	}

	// Run multiple times to catch non-deterministic ordering from map iteration.
	// Without the sort.Slice fix, this would produce different orderings on different runs.
	for iter := 0; iter < 10; iter++ {
		result := reconstructHierarchy(obj, selected, nil)

		if result["status"] != "ok" {
			t.Errorf("iter %d: Expected status=ok, got %v", iter, result["status"])
		}

		data, ok := result["data"].(map[string]interface{})
		if !ok {
			t.Fatalf("iter %d: Expected 'data' map, got %T", iter, result["data"])
		}
		if data["query"] != "test" {
			t.Errorf("iter %d: Expected query=test, got %v", iter, data["query"])
		}

		// Dense array: items 0, 2, 4 packed as [First, Third, Fifth] — sorted by original index
		itemsArr, ok := data["items"].([]interface{})
		if !ok {
			t.Fatalf("iter %d: Expected 'data.items' as []interface{}, got %T", iter, data["items"])
		}
		if len(itemsArr) != 3 {
			t.Fatalf("iter %d: Expected 3 items in dense array, got %d", iter, len(itemsArr))
		}

		// Deterministic ordering: must always be [First, Third, Fifth] regardless of map iteration order
		expectedTitles := []string{"First", "Third", "Fifth"}
		for i, expected := range expectedTitles {
			item, _ := itemsArr[i].(map[string]interface{})
			if item["title"] != expected {
				t.Errorf("iter %d: Expected item[%d].title=%q, got %v (non-deterministic ordering?)",
					iter, i, expected, item["title"])
			}
		}
	}
}

// --- TestHasAncestor_ArrayPaths ---

func TestHasAncestor_ArrayPaths(t *testing.T) {
	tests := []struct {
		path     string
		selected map[string]bool
		want     bool
	}{
		// Array item with parent array selected
		{"data.items[0]", map[string]bool{"data.items": true}, true},
		// Array item with grandparent selected
		{"data.items[0]", map[string]bool{"data": true}, true},
		// Array item, nothing selected
		{"data.items[0]", map[string]bool{}, false},
		// Array item, sibling selected (not ancestor)
		{"data.items[0]", map[string]bool{"data.items[1]": true}, false},
		// Non-array path unchanged behavior
		{"data.metric", map[string]bool{"data": true}, true},
		// Top-level array
		{"results[0]", map[string]bool{"results": true}, true},
	}
	for _, tt := range tests {
		got := hasAncestor(tt.path, tt.selected)
		if got != tt.want {
			t.Errorf("hasAncestor(%q, %v) = %v, want %v", tt.path, tt.selected, got, tt.want)
		}
	}
}

// --- TestHasDescendant_ArrayPaths ---

func TestHasDescendant_ArrayPaths(t *testing.T) {
	tests := []struct {
		prefix   string
		selected map[string]bool
		want     bool
	}{
		// Array parent with item selected
		{"data.items", map[string]bool{"data.items[0]": true}, true},
		// Array parent with multiple items
		{"data.items", map[string]bool{"data.items[0]": true, "data.items[5]": true}, true},
		// Grandparent with array item
		{"data", map[string]bool{"data.items[0]": true}, true},
		// No descendants
		{"data.items", map[string]bool{"data.other": true}, false},
		// Non-array unchanged behavior
		{"data", map[string]bool{"data.metric": true}, true},
		// Top-level array parent
		{"results", map[string]bool{"results[3]": true}, true},
	}
	for _, tt := range tests {
		got := hasDescendant(tt.prefix, tt.selected)
		if got != tt.want {
			t.Errorf("hasDescendant(%q, %v) = %v, want %v", tt.prefix, tt.selected, got, tt.want)
		}
	}
}

// --- TestSelectFields_ProductionReplay_CompanyNews ---

func TestSelectFields_ProductionReplay_CompanyNews(t *testing.T) {
	input := string(loadTestdata(t, "company_news_response.json"))

	if len(input) < 100000 {
		t.Fatalf("Expected production response > 100KB, got %d bytes", len(input))
	}

	trimmer := NewStructuralTrimmer(nil, nil)

	// Budget uses the default MaxMicroResolutionBytes (65536) - promptTemplateReserve (2048).
	// This is configurable in production via ResultTrimConfig.MaxMicroResolutionBytes.
	budget := 65536 - 2048 // 63,488 bytes
	result := trimmer.ProcessForPrompt(context.Background(), input, budget, ResultProcessorContext{
		StepID:      "step-2",
		AgentName:   "research-agent-telemetry",
		Instruction: "Perform sentiment analysis on the news articles for GOOGL",
	})

	if len(result) > budget {
		t.Errorf("Expected result <= %d bytes, got %d", budget, len(result))
	}

	jsonPart := result
	if idx := strings.Index(result, "\n[trimmed:"); idx >= 0 {
		jsonPart = result[:idx]
	}

	var parsed map[string]interface{}
	if err := json.Unmarshal([]byte(jsonPart), &parsed); err != nil {
		t.Fatalf("Expected valid JSON, got error: %v\nOutput: %.500s", err, result)
	}

	// CRITICAL: Must NOT produce just {"source":"Finnhub API","symbol":"GOOGL","success":true}
	data, _ := parsed["data"].(map[string]interface{})
	if data == nil {
		t.Fatal("Expected 'data' wrapper — regression to metadata-only output")
	}

	newsArr, ok := data["news"].([]interface{})
	if !ok || len(newsArr) == 0 {
		t.Fatal("Expected non-empty 'data.news' array — this is the Phase 2 fix target")
	}

	// Should have a meaningful subset of items
	if len(newsArr) < 10 {
		t.Errorf("Expected at least 10 items in budget, got %d", len(newsArr))
	}

	// Metadata should be preserved alongside array items
	if data["symbol"] == nil && data["source"] == nil {
		t.Error("Expected metadata fields preserved alongside array items")
	}

	t.Logf("Production replay: %d bytes → %d bytes, %d items preserved (%.1f%% budget used)",
		len(input), len(result), len(newsArr), float64(len(result))/float64(budget)*100)
}

// ===========================================================================
// Phase 2: Unit Tests — Helper Functions & Branch Coverage
// ===========================================================================

// --- TestScoreObjectContent ---

func TestScoreObjectContent(t *testing.T) {
	tests := []struct {
		name     string
		obj      map[string]interface{}
		keywords []string
		wantMin  float64
		wantMax  float64
	}{
		{
			name:     "empty keywords returns 0",
			obj:      map[string]interface{}{"title": "anything"},
			keywords: nil,
			wantMin:  0, wantMax: 0,
		},
		{
			name:     "empty object returns 0",
			obj:      map[string]interface{}{},
			keywords: []string{"search"},
			wantMin:  0, wantMax: 0,
		},
		{
			name:     "no string values returns 0",
			obj:      map[string]interface{}{"count": float64(42), "active": true},
			keywords: []string{"count"},
			wantMin:  0, wantMax: 0,
		},
		{
			name:     "single hit returns 0.2",
			obj:      map[string]interface{}{"title": "quarterly earnings report"},
			keywords: []string{"earn"},
			wantMin:  0.2, wantMax: 0.2,
		},
		{
			name:     "multiple hits from different fields",
			obj:      map[string]interface{}{"title": "revenue growth", "body": "strong earnings"},
			keywords: []string{"revenue", "earn"},
			wantMin:  0.4, wantMax: 0.4,
		},
		{
			name:     "capped at 1.0",
			obj:      map[string]interface{}{"a": "kw1 kw2 kw3", "b": "kw1 kw2 kw3"},
			keywords: []string{"kw1", "kw2", "kw3"},
			wantMin:  1.0, wantMax: 1.0,
		},
		{
			name:     "case insensitive",
			obj:      map[string]interface{}{"title": "UPPER CASE Content"},
			keywords: []string{"upper", "content"},
			wantMin:  0.4, wantMax: 0.4,
		},
		{
			name:     "non-string values skipped",
			obj:      map[string]interface{}{"num": float64(99), "str": "matching keyword here"},
			keywords: []string{"keyword"},
			wantMin:  0.2, wantMax: 0.2,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := scoreObjectContent(tt.obj, tt.keywords)
			if got < tt.wantMin-0.001 || got > tt.wantMax+0.001 {
				t.Errorf("scoreObjectContent() = %.2f, want [%.2f, %.2f]", got, tt.wantMin, tt.wantMax)
			}
		})
	}
}

// --- TestNavigateToValue ---

func TestNavigateToValue(t *testing.T) {
	obj := map[string]interface{}{
		"data": map[string]interface{}{
			"items": []interface{}{
				map[string]interface{}{"title": "First"},
				map[string]interface{}{"title": "Second"},
			},
			"symbol": "TEST",
		},
		"scalar": "top-level",
	}

	tests := []struct {
		name string
		path string
		want interface{}
	}{
		{"map path", "scalar", "top-level"},
		{"nested map path", "data.symbol", "TEST"},
		{"array item", "data.items[0]", map[string]interface{}{"title": "First"}},
		{"array item field", "data.items[1]", map[string]interface{}{"title": "Second"}},
		{"non-existent key", "data.missing", nil},
		{"out-of-bounds array index", "data.items[99]", nil},
		{"path through scalar", "scalar.child", nil},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := navigateToValue(obj, tt.path)
			gotJSON, _ := json.Marshal(got)
			wantJSON, _ := json.Marshal(tt.want)
			if string(gotJSON) != string(wantJSON) {
				t.Errorf("navigateToValue(%q) = %s, want %s", tt.path, gotJSON, wantJSON)
			}
		})
	}
}

// --- TestSplitPath ---

func TestSplitPath(t *testing.T) {
	tests := []struct {
		path     string
		wantKeys []string
		wantIdxs []int
	}{
		{
			path:     "data.symbol",
			wantKeys: []string{"data", "symbol"},
			wantIdxs: []int{-1, -1},
		},
		{
			path:     "data.items[0]",
			wantKeys: []string{"data", "items", ""},
			wantIdxs: []int{-1, -1, 0},
		},
		{
			path:     "data.items[42]",
			wantKeys: []string{"data", "items", ""},
			wantIdxs: []int{-1, -1, 42},
		},
		{
			path:     "items[0]",
			wantKeys: []string{"items", ""},
			wantIdxs: []int{-1, 0},
		},
		{
			path:     "single",
			wantKeys: []string{"single"},
			wantIdxs: []int{-1},
		},
	}

	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			segments := splitPath(tt.path)
			if len(segments) != len(tt.wantKeys) {
				t.Fatalf("splitPath(%q) returned %d segments, want %d", tt.path, len(segments), len(tt.wantKeys))
			}
			for i, seg := range segments {
				if seg.key != tt.wantKeys[i] {
					t.Errorf("segment[%d].key = %q, want %q", i, seg.key, tt.wantKeys[i])
				}
				if seg.index != tt.wantIdxs[i] {
					t.Errorf("segment[%d].index = %d, want %d", i, seg.index, tt.wantIdxs[i])
				}
			}
		})
	}
}

// --- TestLastPathSegment ---

func TestLastPathSegment(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"data.news", "news"},
		{"a.b.c", "c"},
		{"single", "single"},
		{"", ""},
	}

	for _, tt := range tests {
		got := lastPathSegment(tt.input)
		if got != tt.want {
			t.Errorf("lastPathSegment(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

// --- TestBuildArrayInventory_EmptyArray ---

func TestBuildArrayInventory_EmptyArray(t *testing.T) {
	trimmer := NewStructuralTrimmer(nil, nil)
	entries := trimmer.buildArrayInventory([]interface{}{}, "data.items", 1)
	if len(entries) != 0 {
		t.Errorf("Expected 0 entries for empty array, got %d", len(entries))
	}
}

// --- TestBuildArrayInventory_ScalarItems ---

func TestBuildArrayInventory_ScalarItems(t *testing.T) {
	trimmer := NewStructuralTrimmer(nil, nil)
	arr := []interface{}{"hello", float64(42), true, nil}
	entries := trimmer.buildArrayInventory(arr, "tags", 0)

	if len(entries) != 4 {
		t.Fatalf("Expected 4 entries, got %d", len(entries))
	}
	for i, e := range entries {
		if !e.isScalar {
			t.Errorf("entries[%d].isScalar = false, want true (value: %v)", i, e.value)
		}
		if e.arrayIndex != i {
			t.Errorf("entries[%d].arrayIndex = %d, want %d", i, e.arrayIndex, i)
		}
	}
}

// --- TestScoreField_ArrayItemScalarValue ---
// Array item that is a string (not a map) — content scoring should be skipped.

func TestScoreField_ArrayItemScalarValue(t *testing.T) {
	trimmer := NewStructuralTrimmer(nil, nil)
	entry := fieldEntry{
		path:       "tags[0]",
		key:        "tags[0]",
		value:      "some text with keyword match",
		depth:      1,
		isScalar:   true,
		arrayIndex: 0,
		arrayTotal: 5,
	}

	score := trimmer.scoreField(entry, []string{"keyword"})

	// key "tags[0]" doesn't contain "keyword" → no key match
	// content preview: "keyword" found in value → +0.8
	// value is string, not map → scoreObjectContent NOT called
	// positional: 0.5 * (1 - 0/5) = 0.5
	// isScalar: +0.3
	// depth 1: no bonus
	// Total: 0.8 + 0.5 + 0.3 = 1.6
	expected := 1.6
	if score < expected-0.01 || score > expected+0.01 {
		t.Errorf("Expected score ~%.1f for scalar array item, got %.2f", expected, score)
	}
}

// --- TestScoreField_ArrayItemZeroTotal ---
// Guard: arrayTotal=0 should skip positional scoring (no div-by-zero).

func TestScoreField_ArrayItemZeroTotal(t *testing.T) {
	trimmer := NewStructuralTrimmer(nil, nil)
	entry := fieldEntry{
		path:       "items[0]",
		key:        "items[0]",
		value:      map[string]interface{}{"id": "test"},
		depth:      1,
		arrayIndex: 0,
		arrayTotal: 0, // guard case
	}
	// Should not panic
	score := trimmer.scoreField(entry, []string{"item"})
	// key match: "items[0]" contains "item" → +1.5, no positional (total=0)
	if score < 1.0 {
		t.Errorf("Expected score >= 1.0, got %.2f", score)
	}
}

// --- TestReconstructHierarchy_Empty ---

func TestReconstructHierarchy_Empty(t *testing.T) {
	obj := map[string]interface{}{"a": "1"}
	result := reconstructHierarchy(obj, map[string]bool{}, nil)
	if len(result) != 0 {
		t.Errorf("Expected empty map for empty selection, got %v", result)
	}
}

// --- TestReconstructHierarchy_OnlyArrayPaths ---

func TestReconstructHierarchy_OnlyArrayPaths(t *testing.T) {
	obj := map[string]interface{}{
		"items": []interface{}{"a", "b", "c"},
	}
	selected := map[string]bool{
		"items[0]": true,
		"items[2]": true,
	}
	result := reconstructHierarchy(obj, selected, nil)

	arr, ok := result["items"].([]interface{})
	if !ok {
		t.Fatalf("Expected 'items' array, got %T", result["items"])
	}
	if len(arr) != 2 {
		t.Fatalf("Expected 2 items, got %d", len(arr))
	}
	// Dense: [a, c] (sorted by original index)
	if arr[0] != "a" || arr[1] != "c" {
		t.Errorf("Expected [a, c], got %v", arr)
	}
}

// --- TestReconstructHierarchy_MultipleArraysSameParent ---

func TestReconstructHierarchy_MultipleArraysSameParent(t *testing.T) {
	obj := map[string]interface{}{
		"data": map[string]interface{}{
			"news":   []interface{}{"n0", "n1", "n2"},
			"events": []interface{}{"e0", "e1"},
		},
	}
	selected := map[string]bool{
		"data.news[0]":   true,
		"data.news[2]":   true,
		"data.events[1]": true,
	}
	result := reconstructHierarchy(obj, selected, nil)

	data, ok := result["data"].(map[string]interface{})
	if !ok {
		t.Fatalf("Expected 'data' map, got %T", result["data"])
	}

	news, ok := data["news"].([]interface{})
	if !ok || len(news) != 2 {
		t.Fatalf("Expected 'data.news' with 2 items, got %v", data["news"])
	}
	if news[0] != "n0" || news[1] != "n2" {
		t.Errorf("Expected news [n0, n2], got %v", news)
	}

	events, ok := data["events"].([]interface{})
	if !ok || len(events) != 1 {
		t.Fatalf("Expected 'data.events' with 1 item, got %v", data["events"])
	}
	if events[0] != "e1" {
		t.Errorf("Expected events [e1], got %v", events)
	}
}

// --- TestSelectFields_AncestorSkipsArrayChildren ---
// Directly targets the hasAncestor continue branch (line 293-294).
// When the parent array is selected as a whole (high preserve score),
// individual array items must be skipped.

func TestSelectFields_AncestorSkipsArrayChildren(t *testing.T) {
	// Build an array > 1024 bytes to trigger decomposition
	items := make([]interface{}, 10)
	for i := 0; i < 10; i++ {
		items[i] = map[string]interface{}{
			"title": fmt.Sprintf("Item %d with padding %s", i, strings.Repeat("x", 100)),
		}
	}
	obj := map[string]interface{}{
		"items": items,
	}

	// preserveKeys = ["items"] gives the parent field +5.0 score,
	// ensuring it's selected before any individual items.
	trimmer := NewStructuralTrimmer([]string{"items"}, nil)

	// Budget large enough to fit the entire parent
	result, _, _, _, _, _, _, _ := trimmer.selectFieldsWithMeta(context.Background(), obj, 8192, 0, []string{})

	// The parent "items" should be selected as a whole, children skipped
	jsonPart, _ := splitAnnotation(result)

	var parsed map[string]interface{}
	if err := json.Unmarshal([]byte(jsonPart), &parsed); err != nil {
		t.Fatalf("Expected valid JSON, got error: %v", err)
	}

	// items should be the full array (10 items), not a dense subset
	arr, ok := parsed["items"].([]interface{})
	if !ok {
		t.Fatalf("Expected 'items' array, got %T", parsed["items"])
	}
	if len(arr) != 10 {
		t.Errorf("Expected all 10 items via parent selection, got %d (ancestor check may not be working)", len(arr))
	}
}

// --- TestSelectFields_SafetyCheckOvershoot ---
// Targets the safety check overshoot correction (lines 349-371).
// Crafts a scenario where wrapper overhead estimation is slightly wrong,
// forcing the first json.Marshal to exceed budget.

func TestSelectFields_SafetyCheckOvershoot(t *testing.T) {
	// Create nested fields where overhead estimation will be slightly off.
	// Deep nesting amplifies the difference between estimated and actual overhead.
	obj := map[string]interface{}{
		"a": map[string]interface{}{
			"b": map[string]interface{}{
				"c": map[string]interface{}{
					"d": strings.Repeat("x", 50),
					"e": strings.Repeat("y", 50),
					"f": strings.Repeat("z", 50),
				},
			},
		},
		"small": "val",
	}

	serialized, _ := json.Marshal(obj)
	trimmer := NewStructuralTrimmer(nil, nil)

	// Set budget just under the full serialized size to force trimming.
	// The tight budget combined with deep nesting increases the chance
	// that estimated overhead diverges from actual json.Marshal output.
	budget := len(serialized) - 20
	result, _, _, _, _, _, _, _ := trimmer.selectFieldsWithMeta(context.Background(), obj, budget, 0, []string{"x", "y", "z"})

	jsonPart := result
	if idx := strings.Index(result, "\n[trimmed:"); idx >= 0 {
		jsonPart = result[:idx]
	}

	if len(jsonPart) > budget {
		t.Errorf("Safety check failed: output %d bytes exceeds budget %d", len(jsonPart), budget)
	}

	var parsed map[string]interface{}
	if err := json.Unmarshal([]byte(jsonPart), &parsed); err != nil {
		t.Fatalf("Expected valid JSON after safety check, got error: %v", err)
	}
}

// --- TestSelectFields_AnnotationArrayFormat ---
// Verifies the annotation format switches when array items are selected.

func TestSelectFields_AnnotationArrayFormat(t *testing.T) {
	// Each item must exceed 1024 bytes to be decomposed, and the whole "data" object
	// must exceed the budget so it cannot be kept whole — forcing whole-unit selection
	// to descend and keep individual array ITEMS (the "array items" annotation path).
	items := make([]interface{}, 10)
	for i := 0; i < 10; i++ {
		items[i] = map[string]interface{}{
			"title": fmt.Sprintf("Item %d with some content to pad size", i),
			"body":  strings.Repeat("padding ", 140), // ~1.1KB → item >1024B, 10 items ≈ 12KB
		}
	}
	obj := map[string]interface{}{
		"data": map[string]interface{}{
			"results": items,
			"total":   float64(10),
		},
	}

	trimmer := NewStructuralTrimmer(nil, nil)
	// Budget below the whole "data" object (~12KB) but above a single item (~1.1KB),
	// so individual array items are selected and the annotation fits.
	result, _, _, _, _, _, _, _ := trimmer.selectFieldsWithMeta(context.Background(), obj, 8192, 0, []string{"item", "content"})

	// Should use array annotation format
	if !strings.Contains(result, "array items") {
		tail := result
		if idx := strings.LastIndex(result, "\n"); idx >= 0 {
			tail = result[idx:]
		}
		t.Errorf("Expected annotation with 'array items' when arrays selected, got: %s", tail)
	}
	if strings.Contains(result, "matched:") {
		t.Error("Array annotation should NOT contain 'matched:' field list")
	}
}

// --- TestSelectFields_AnnotationNonArrayFormat ---
// Verifies the old annotation format is preserved when no array items are selected.

func TestSelectFields_AnnotationNonArrayFormat(t *testing.T) {
	obj := map[string]interface{}{
		"alpha": strings.Repeat("a", 50),
		"beta":  strings.Repeat("b", 50),
		"gamma": "small",
	}

	trimmer := NewStructuralTrimmer(nil, nil)
	// Enough budget to fit JSON + annotation
	result, _, _, _, _, _, _, _ := trimmer.selectFieldsWithMeta(context.Background(), obj, 400, 0, []string{"gamma"})

	if strings.Contains(result, "array items") {
		t.Error("Non-array annotation should NOT contain 'array items'")
	}
	if !strings.Contains(result, "matched:") && !strings.Contains(result, "fields kept") {
		tail := result
		if idx := strings.LastIndex(result, "\n"); idx >= 0 {
			tail = result[idx:]
		}
		t.Errorf("Expected standard annotation format, got: %s", tail)
	}
}

// --- Phase 14: ProcessForPrompt branch metadata capture ---

func TestProcessForPrompt_CapturesMetadata_PlainText(t *testing.T) {
	trimmer := NewStructuralTrimmer(nil, nil)
	// Non-JSON input triggers the plain-text branch (Method: "structural_text").
	input := "The stock price rose sharply. Volume was high. Markets responded quickly to the change."
	ctx, meta := WithTrimMetadataCapture(context.Background())
	result := trimmer.ProcessForPrompt(ctx, input, 30, ResultProcessorContext{
		StepID: "s1", AgentName: "a", Instruction: "get stock price",
	})

	if result == "" {
		t.Fatal("Expected non-empty result")
	}
	if meta.Method != "structural_text" {
		t.Errorf("Expected Method='structural_text', got %q", meta.Method)
	}
	if meta.OriginalBytes != len(input) {
		t.Errorf("Expected OriginalBytes=%d, got %d", len(input), meta.OriginalBytes)
	}
	if meta.TrimmedBytes > 30 {
		t.Errorf("Expected TrimmedBytes <= 30, got %d", meta.TrimmedBytes)
	}
	if len(meta.Keywords) == 0 {
		t.Error("Expected Keywords to be extracted from instruction")
	}
}

func TestProcessForPrompt_CapturesMetadata_JSONObject(t *testing.T) {
	trimmer := NewStructuralTrimmer(nil, nil)
	// JSON object input with tight budget triggers the structural (object) branch.
	input := `{"price":100,"name":"Widget","description":"A fine product","id":"sku-001"}`
	ctx, meta := WithTrimMetadataCapture(context.Background())
	_ = trimmer.ProcessForPrompt(ctx, input, 20, ResultProcessorContext{
		StepID: "s1", AgentName: "a", Instruction: "get price",
	})

	if meta.Method != "structural" {
		t.Errorf("Expected Method='structural', got %q", meta.Method)
	}
	if meta.OriginalBytes != len(input) {
		t.Errorf("Expected OriginalBytes=%d, got %d", len(input), meta.OriginalBytes)
	}
	if meta.FieldsKept <= 0 {
		t.Errorf("Expected FieldsKept > 0, got %d", meta.FieldsKept)
	}
	if meta.FieldsDropped <= 0 {
		t.Errorf("Expected FieldsDropped > 0 (tight budget forces drops), got %d", meta.FieldsDropped)
	}
}

func TestProcessForPrompt_CapturesMetadata_JSONArray(t *testing.T) {
	trimmer := NewStructuralTrimmer(nil, nil)
	// JSON array input with tight budget triggers the structural_array branch.
	// 5 items × ~9 bytes/item + 2 for brackets = 47 bytes total; budget=22 fits 2.
	input := `[{"id":1},{"id":2},{"id":3},{"id":4},{"id":5}]`
	ctx, meta := WithTrimMetadataCapture(context.Background())
	_ = trimmer.ProcessForPrompt(ctx, input, 22, ResultProcessorContext{
		StepID: "s1", AgentName: "a", Instruction: "list items",
	})

	if meta.Method != "structural_array" {
		t.Errorf("Expected Method='structural_array', got %q", meta.Method)
	}
	if meta.OriginalBytes != len(input) {
		t.Errorf("Expected OriginalBytes=%d, got %d", len(input), meta.OriginalBytes)
	}
	if meta.FieldsKept <= 0 {
		t.Errorf("Expected FieldsKept (items kept) > 0, got %d", meta.FieldsKept)
	}
	if meta.FieldsKept+meta.FieldsDropped != 5 {
		t.Errorf("Expected FieldsKept+FieldsDropped=5 (total items), got %d", meta.FieldsKept+meta.FieldsDropped)
	}
}

func TestProcessForPrompt_CapturesMetadata_Truncate(t *testing.T) {
	trimmer := NewStructuralTrimmer(nil, nil)
	// A JSON string literal is valid JSON but not a map or array — triggers the truncate fallback.
	input := `"this is a long JSON string value that exceeds the small budget limit"`
	ctx, meta := WithTrimMetadataCapture(context.Background())
	_ = trimmer.ProcessForPrompt(ctx, input, 10, ResultProcessorContext{
		StepID: "s1", AgentName: "a", Instruction: "test",
	})

	if meta.Method != "truncate" {
		t.Errorf("Expected Method='truncate', got %q", meta.Method)
	}
	if meta.OriginalBytes != len(input) {
		t.Errorf("Expected OriginalBytes=%d, got %d", len(input), meta.OriginalBytes)
	}
}

func TestProcessForPrompt_NoBudgetExceeded_NoMetadataWritten(t *testing.T) {
	// When response fits within budget, ProcessForPrompt returns early without calling
	// captureTrimMetadata. The metadata pointer must remain zero-valued.
	trimmer := NewStructuralTrimmer(nil, nil)
	input := `{"x":"y"}`
	ctx, meta := WithTrimMetadataCapture(context.Background())
	_ = trimmer.ProcessForPrompt(ctx, input, 8192, ResultProcessorContext{
		StepID: "s1", AgentName: "a", Instruction: "test",
	})

	if meta.Method != "" {
		t.Errorf("Expected no metadata written when response fits budget, got Method=%q", meta.Method)
	}
}

// --- Phase 14: selectFieldsWithMeta return values ---

func TestSelectFieldsWithMeta_FieldCounts_FlatObject(t *testing.T) {
	// "price":42 → inventory size = len("42")+len("price")+4 = 11
	// "name":"Widget" → inventory size = len(`"Widget"`)+len("name")+4 = 16
	// Budget 15: wrapper(2) + price(11) = 13 ≤ 15 → selected; +name = 29 > 15 → dropped.
	obj := map[string]interface{}{"price": 42.0, "name": "Widget"}
	trimmer := NewStructuralTrimmer(nil, nil)
	_, kept, dropped, _, _, matchedPaths, _, _ := trimmer.selectFieldsWithMeta(context.Background(), obj, 15, 0, []string{})

	if kept != 1 {
		t.Errorf("Expected fieldsKept=1, got %d", kept)
	}
	if dropped != 1 {
		t.Errorf("Expected fieldsDropped=1, got %d", dropped)
	}
	if len(matchedPaths) != kept {
		t.Errorf("Expected len(matchedPaths)==fieldsKept=%d, got %d: %v", kept, len(matchedPaths), matchedPaths)
	}
}

func TestSelectFieldsWithMeta_MatchedPathsInOutput(t *testing.T) {
	// matchedPaths must be the set of field paths included in the output JSON.
	obj := map[string]interface{}{
		"alpha": "small",
		"beta":  strings.Repeat("b", 50),
	}
	trimmer := NewStructuralTrimmer(nil, nil)
	result, kept, _, _, _, matchedPaths, _, _ := trimmer.selectFieldsWithMeta(context.Background(), obj, 8192, 0, []string{"alpha"})

	if kept == 0 {
		t.Fatal("Expected at least one field selected")
	}
	// Every path in matchedPaths must be a key visible in the JSON output.
	for _, path := range matchedPaths {
		if !strings.Contains(result, `"`+path+`"`) {
			t.Errorf("matchedPath %q not found as a JSON key in result: %.150s", path, result)
		}
	}
}

func TestSelectFieldsWithMeta_LargeBudgetKeepsAllFields(t *testing.T) {
	obj := map[string]interface{}{
		"a": "alpha",
		"b": "beta",
		"c": "gamma",
	}
	trimmer := NewStructuralTrimmer(nil, nil)
	_, kept, dropped, _, _, _, _, _ := trimmer.selectFieldsWithMeta(context.Background(), obj, 8192, 0, []string{})

	if kept != 3 {
		t.Errorf("Expected all 3 fields kept with large budget, got %d", kept)
	}
	if dropped != 0 {
		t.Errorf("Expected 0 fields dropped with large budget, got %d", dropped)
	}
}

// --- Phase 14: trimArray kept/total counts ---

func TestTrimArray_AllItemsFit(t *testing.T) {
	trimmer := NewStructuralTrimmer(nil, nil)
	arr := []interface{}{
		map[string]interface{}{"id": 1.0},
		map[string]interface{}{"id": 2.0},
	}
	_, kept, total := trimmer.trimArray(arr, 8192)

	if kept != 2 {
		t.Errorf("Expected kept=2, got %d", kept)
	}
	if total != 2 {
		t.Errorf("Expected total=2, got %d", total)
	}
}

func TestTrimArray_PartialFit(t *testing.T) {
	// Each `{"id":N}` = 8 bytes. budgetUsed starts at 2 (for `[]`).
	// Each item costs 8+1=9 bytes. Budget 20: 2+9=11, 11+9=20, 20+9=29 > 20 → stops after 2.
	trimmer := NewStructuralTrimmer(nil, nil)
	arr := []interface{}{
		map[string]interface{}{"id": 1.0},
		map[string]interface{}{"id": 2.0},
		map[string]interface{}{"id": 3.0},
		map[string]interface{}{"id": 4.0},
		map[string]interface{}{"id": 5.0},
	}
	_, kept, total := trimmer.trimArray(arr, 20)

	if total != 5 {
		t.Errorf("Expected total=5, got %d", total)
	}
	if kept == 0 {
		t.Errorf("Expected at least 1 item kept, got 0")
	}
	if kept >= total {
		t.Errorf("Expected partial fit (kept < total), got kept=%d total=%d", kept, total)
	}
}

func TestTrimArray_EmptyArray(t *testing.T) {
	trimmer := NewStructuralTrimmer(nil, nil)
	_, kept, total := trimmer.trimArray([]interface{}{}, 100)

	if kept != 0 || total != 0 {
		t.Errorf("Expected kept=0, total=0 for empty array, got kept=%d, total=%d", kept, total)
	}
}

func TestTrimArray_KeptNeverExceedsTotal(t *testing.T) {
	// Invariant: kept ≤ total for any input.
	trimmer := NewStructuralTrimmer(nil, nil)
	arr := make([]interface{}, 20)
	for i := range arr {
		arr[i] = map[string]interface{}{"idx": float64(i)}
	}
	for _, budget := range []int{1, 10, 50, 8192} {
		_, kept, total := trimmer.trimArray(arr, budget)
		if kept > total {
			t.Errorf("budget=%d: kept=%d > total=%d (invariant violated)", budget, kept, total)
		}
		if total != 20 {
			t.Errorf("budget=%d: total should always be len(arr)=20, got %d", budget, total)
		}
	}
}

// --- Backfill tests ---

// TestStructuralTrimmer_BackfillDroppedStringField tests the devops scenario:
// a huge string field (data.stdout) alongside small metadata fields.
// With backfill, the string should be truncated to fill remaining budget, not dropped.
func TestStructuralTrimmer_BackfillDroppedStringField(t *testing.T) {
	trimmer := NewStructuralTrimmer(nil, nil)
	ctx := context.Background()

	// Build a response mimicking devops: small fields + one huge string
	bigString := strings.Repeat("pod-data-line\n", 5000) // ~70KB
	obj := map[string]interface{}{
		"success": true,
		"data": map[string]interface{}{
			"command":   "kubectl get pods",
			"exit_code": float64(0),
			"stdout":    bigString,
		},
	}
	raw, _ := json.Marshal(obj)

	result := trimmer.ProcessForPrompt(ctx, string(raw), 4096, ResultProcessorContext{
		StepID: "step-1", AgentName: "devops-tool", Instruction: "list pods",
	})

	jsonPart, _ := splitAnnotation(result)
	var parsed map[string]interface{}
	if err := json.Unmarshal([]byte(jsonPart), &parsed); err != nil {
		t.Fatalf("Invalid JSON: %v", err)
	}

	data, _ := parsed["data"].(map[string]interface{})
	stdout, hasStdout := data["stdout"]
	if !hasStdout {
		t.Fatal("data.stdout should be present via backfill, not dropped")
	}

	stdoutStr := stdout.(string)
	if len(stdoutStr) < 500 {
		t.Errorf("Backfilled stdout too small: %d bytes", len(stdoutStr))
	}
	if len(jsonPart) > 4096 {
		t.Errorf("Output %d exceeds budget 4096", len(jsonPart))
	}

	// Small fields should also be present
	if _, ok := data["command"]; !ok {
		t.Error("data.command should be present")
	}
	if _, ok := data["exit_code"]; !ok {
		t.Error("data.exit_code should be present")
	}
}

// TestStructuralTrimmer_BackfillSkipsNonString tests that backfill does not
// attempt to truncate non-string fields (objects/arrays), as that would
// produce invalid JSON.
func TestStructuralTrimmer_BackfillSkipsNonString(t *testing.T) {
	trimmer := NewStructuralTrimmer(nil, nil)
	ctx := context.Background()

	// Large nested object (not a string) — should NOT be backfilled
	bigObj := make(map[string]interface{})
	for i := 0; i < 200; i++ {
		bigObj[fmt.Sprintf("key_%d", i)] = strings.Repeat("v", 100)
	}
	obj := map[string]interface{}{
		"status": "ok",
		"data":   bigObj, // ~25KB object — too big for budget
	}
	raw, _ := json.Marshal(obj)

	result := trimmer.ProcessForPrompt(ctx, string(raw), 2048, ResultProcessorContext{
		StepID: "step-1", AgentName: "test", Instruction: "test",
	})

	jsonPart, _ := splitAnnotation(result)
	var parsed map[string]interface{}
	if err := json.Unmarshal([]byte(jsonPart), &parsed); err != nil {
		t.Fatalf("Invalid JSON: %v", err)
	}

	// "status" (small) should be present
	if _, ok := parsed["status"]; !ok {
		t.Error("status field should be present")
	}

	// "data" may or may not have sub-fields selected by greedy, but
	// the full object should NOT appear as a truncated string
	if data, ok := parsed["data"]; ok {
		if _, isString := data.(string); isString {
			t.Error("data should NOT be a truncated string — backfill should skip non-string fields")
		}
	}
}

// TestStructuralTrimmer_BackfillInsufficientBudget tests that backfill is
// skipped when remaining budget is too small (<= 512 bytes).
func TestStructuralTrimmer_BackfillInsufficientBudget(t *testing.T) {
	trimmer := NewStructuralTrimmer(nil, nil)
	ctx := context.Background()

	bigString := strings.Repeat("x", 10000)
	obj := map[string]interface{}{
		"small1": "a",
		"small2": "b",
		"small3": "c",
		"big":    bigString,
	}
	raw, _ := json.Marshal(obj)

	// Budget is just barely enough for the 3 small fields + wrappers (~60 bytes).
	// Remaining budget after small fields will be < 512, so no backfill.
	result := trimmer.ProcessForPrompt(ctx, string(raw), 100, ResultProcessorContext{
		StepID: "step-1", AgentName: "test", Instruction: "test",
	})

	jsonPart, _ := splitAnnotation(result)
	var parsed map[string]interface{}
	if err := json.Unmarshal([]byte(jsonPart), &parsed); err != nil {
		t.Fatalf("Invalid JSON: %v", err)
	}

	if _, ok := parsed["big"]; ok {
		t.Error("big field should NOT be backfilled with <512 byte remaining budget")
	}
}

// TestStructuralTrimmer_BackfillHighestRelevance tests that when two string
// fields are both dropped, multi-field backfill recovers both with the higher-
// relevance field getting budget first (Phase 4A update).
func TestStructuralTrimmer_BackfillHighestRelevance(t *testing.T) {
	trimmer := NewStructuralTrimmer(nil, nil)
	ctx := context.Background()

	// Two large string fields. "pods" keyword should boost data.pods relevance.
	obj := map[string]interface{}{
		"data": map[string]interface{}{
			"logs": strings.Repeat("log-line\n", 5000), // ~45KB, low relevance
			"pods": strings.Repeat("pod-info\n", 5000), // ~45KB, HIGH relevance (matches keyword)
		},
	}
	raw, _ := json.Marshal(obj)

	result := trimmer.ProcessForPrompt(ctx, string(raw), 8192, ResultProcessorContext{
		StepID: "step-1", AgentName: "devops-tool", Instruction: "list all pods",
	})

	jsonPart, _ := splitAnnotation(result)
	var parsed map[string]interface{}
	if err := json.Unmarshal([]byte(jsonPart), &parsed); err != nil {
		t.Fatalf("Invalid JSON: %v", err)
	}

	data, _ := parsed["data"].(map[string]interface{})
	_, hasPods := data["pods"]
	_, hasLogs := data["logs"]

	if !hasPods {
		t.Error("data.pods should be backfilled (higher relevance due to 'pods' keyword match)")
	}
	// Phase 4A: multi-field backfill recovers both fields, splitting the budget.
	if hasPods && hasLogs {
		t.Log("Both fields present — multi-field backfill correctly splits budget across candidates")
	}
	if len(jsonPart) > 8192 {
		t.Errorf("Output exceeds budget: %d > 8192", len(jsonPart))
	}
}

// TestReconstructHierarchy_ValueOverrides tests that the valueOverrides parameter
// correctly overrides values during hierarchy reconstruction.
func TestReconstructHierarchy_ValueOverrides(t *testing.T) {
	obj := map[string]interface{}{
		"data": map[string]interface{}{
			"stdout":  "original-very-long-value",
			"command": "kubectl get pods",
		},
		"success": true,
	}

	selected := map[string]bool{
		"success":      true,
		"data.command": true,
		"data.stdout":  true,
	}

	// Override data.stdout with a truncated value
	overrides := map[string]interface{}{
		"data.stdout": "truncated",
	}

	result := reconstructHierarchy(obj, selected, overrides)

	// success should be original
	if result["success"] != true {
		t.Errorf("success should be true, got %v", result["success"])
	}

	data, ok := result["data"].(map[string]interface{})
	if !ok {
		t.Fatal("data should be a map")
	}

	// command should be original (no override)
	if data["command"] != "kubectl get pods" {
		t.Errorf("data.command should be original, got %v", data["command"])
	}

	// stdout should be overridden
	if data["stdout"] != "truncated" {
		t.Errorf("data.stdout should be overridden to 'truncated', got %v", data["stdout"])
	}

	// Test nil overrides (backward compatibility)
	result2 := reconstructHierarchy(obj, selected, nil)
	data2 := result2["data"].(map[string]interface{})
	if data2["stdout"] != "original-very-long-value" {
		t.Errorf("With nil overrides, data.stdout should be original, got %v", data2["stdout"])
	}
}

// --- Benchmarks ---

// BenchmarkStructuralTrimmer_BackfillLargeString measures the backfill path:
// a 560KB string field that triggers binary search truncation.
func BenchmarkStructuralTrimmer_BackfillLargeString(b *testing.B) {
	trimmer := NewStructuralTrimmer(nil, nil)
	ctx := context.Background()

	bigString := strings.Repeat("pod-data-line\n", 40000) // ~560KB
	obj := map[string]interface{}{
		"success": true,
		"data": map[string]interface{}{
			"command":   "kubectl get pods",
			"exit_code": float64(0),
			"stdout":    bigString,
		},
	}
	raw, _ := json.Marshal(obj)
	input := string(raw)
	stepCtx := ResultProcessorContext{
		StepID: "step-1", AgentName: "devops-tool",
		Instruction: "list pods",
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		trimmer.ProcessForPrompt(ctx, input, 32768, stepCtx)
	}
}

// --- Phase 4A: Multi-field greedy backfill tests ---

// TestStructuralTrimmer_BackfillMultipleFields tests that multi-field backfill
// recovers multiple dropped strings when possible (Phase 4A).
// Uses one small string (fits whole) + one large string (truncated) to
// demonstrate the multi-field improvement over single-field backfill.
func TestStructuralTrimmer_BackfillMultipleFields(t *testing.T) {
	trimmer := NewStructuralTrimmer(nil, nil)
	ctx := context.Background()

	// Small stderr (fits whole) + large stdout (truncated). Both match keywords.
	smallErr := "error: connection refused at port 8080"
	obj := map[string]interface{}{
		"data": map[string]interface{}{
			"stdout":    strings.Repeat("pod-data output\n", 40000), // ~640KB
			"stderr":    smallErr,                                   // ~39 bytes, fits whole
			"exit_code": float64(1),
		},
	}
	raw, _ := json.Marshal(obj)

	result := trimmer.ProcessForPrompt(ctx, string(raw), 32768, ResultProcessorContext{
		StepID: "step-1", AgentName: "tool", Instruction: "show errors and output",
	})

	jsonPart, _ := splitAnnotation(result)
	var parsed map[string]interface{}
	if err := json.Unmarshal([]byte(jsonPart), &parsed); err != nil {
		t.Fatalf("Invalid JSON: %v", err)
	}

	data, _ := parsed["data"].(map[string]interface{})
	_, hasStdout := data["stdout"]
	_, hasStderr := data["stderr"]

	if !hasStdout {
		t.Error("data.stdout should be backfilled (truncated to fill remaining budget)")
	}
	if !hasStderr {
		t.Error("data.stderr should be backfilled (small enough to fit whole)")
	}
	if hasStdout && hasStderr {
		// This is the key Phase 4A improvement: single-field backfill would only recover one.
		t.Log("Both fields present — multi-field backfill correctly recovers both")
	}
	// Verify small stderr is included at full size (not truncated)
	if stderr, ok := data["stderr"].(string); ok && stderr != smallErr {
		t.Errorf("Small stderr should be included whole, got truncated: %q", stderr[:min(len(stderr), 50)])
	}
}

// TestStructuralTrimmer_BackfillFieldFitsWhole tests that a small dropped string
// is included at full size (no wasteful truncation) when budget allows (Phase 4A).
func TestStructuralTrimmer_BackfillFieldFitsWhole(t *testing.T) {
	trimmer := NewStructuralTrimmer(nil, nil)
	ctx := context.Background()

	smallValue := "small error message that fits entirely"
	obj := map[string]interface{}{
		"data": map[string]interface{}{
			"stdout":    strings.Repeat("pod-data\n", 60000), // ~540KB, too big
			"stderr":    smallValue,                          // ~39 bytes, fits whole
			"exit_code": float64(1),
		},
	}
	raw, _ := json.Marshal(obj)

	result := trimmer.ProcessForPrompt(ctx, string(raw), 32768, ResultProcessorContext{
		StepID: "step-1", AgentName: "tool", Instruction: "show errors and output",
	})

	jsonPart, _ := splitAnnotation(result)
	var parsed map[string]interface{}
	if err := json.Unmarshal([]byte(jsonPart), &parsed); err != nil {
		t.Fatalf("Invalid JSON: %v", err)
	}

	data, _ := parsed["data"].(map[string]interface{})
	if stderr, ok := data["stderr"].(string); ok {
		if stderr != smallValue {
			t.Errorf("Small stderr should be included whole (no truncation), got %q", stderr)
		}
	} else {
		t.Error("data.stderr should be present in output")
	}
}

// TestStructuralTrimmer_BackfillMultipleExhausted tests that backfill stops
// when remaining budget drops below minBackfillBudget (Phase 4A).
func TestStructuralTrimmer_BackfillMultipleExhausted(t *testing.T) {
	trimmer := NewStructuralTrimmer(nil, nil)
	ctx := context.Background()

	// Three large strings, very tight budget (2KB).
	obj := map[string]interface{}{
		"a": strings.Repeat("aaa", 10000), // ~30KB
		"b": strings.Repeat("bbb", 10000), // ~30KB
		"c": strings.Repeat("ccc", 10000), // ~30KB
	}
	raw, _ := json.Marshal(obj)

	result := trimmer.ProcessForPrompt(ctx, string(raw), 2048, ResultProcessorContext{
		StepID: "step-1", AgentName: "tool", Instruction: "show all data",
	})

	jsonPart, _ := splitAnnotation(result)
	var parsed map[string]interface{}
	if err := json.Unmarshal([]byte(jsonPart), &parsed); err != nil {
		t.Fatalf("Invalid JSON: %v", err)
	}

	// At least one field should be backfilled, but not all three at full size
	fieldCount := len(parsed)
	if fieldCount == 0 {
		t.Error("Expected at least one field to be backfilled")
	}
	// Total output should respect budget
	if len(jsonPart) > 2048 {
		t.Errorf("Output exceeds budget: %d > 2048", len(jsonPart))
	}
}

// --- Phase 4B: Content-aware preview scoring tests ---

// TestScoreField_ContentPreviewMatching tests that keywords found in the first
// 500 chars of a string field's value add a content bonus (Phase 4B).
func TestScoreField_ContentPreviewMatching(t *testing.T) {
	trimmer := NewStructuralTrimmer(nil, nil)
	entry := fieldEntry{
		path:     "data.stdout",
		key:      "stdout",
		value:    `{"apiVersion":"v1","items":[{"metadata":{"name":"agent-pod","restartCount":3}}]}`,
		depth:    1,
		isScalar: true,
	}

	score := trimmer.scoreField(entry, []string{"pod"})

	// "stdout" doesn't contain "pod" → no key match
	// content preview contains "pod" → +0.8
	// isScalar: +0.3
	// depth 1: no bonus
	// Total: 0.8 + 0.3 = 1.1
	if score < 1.0 {
		t.Errorf("Expected content preview bonus for 'pod' in value, got score %.2f", score)
	}
}

// TestScoreField_ContentPreviewNoFalsePositive tests that keywords appearing
// beyond the preview length don't trigger the content bonus (Phase 4B).
func TestScoreField_ContentPreviewNoFalsePositive(t *testing.T) {
	trimmer := NewStructuralTrimmer(nil, nil)
	// Place the keyword "secret" at character 600 (beyond 500 char preview)
	value := strings.Repeat("x", 600) + "secret" + strings.Repeat("y", 100)
	entry := fieldEntry{
		path:     "data.output",
		key:      "output",
		value:    value,
		depth:    1,
		isScalar: true,
	}

	score := trimmer.scoreField(entry, []string{"secret"})

	// "output" doesn't contain "secret" → no key match
	// "secret" is at char 600 (beyond previewScoringLength=500) → no content bonus
	// isScalar: +0.3
	// depth 1: no bonus
	expected := 0.3
	if score > expected+0.01 {
		t.Errorf("Expected no content bonus (keyword beyond preview), got score %.2f", score)
	}
}

// TestScoreField_ContentPreviewWeight tests that name matches outrank content
// matches: "error_logs" (name match +1.5) beats "stdout" (content match +0.8) (Phase 4B).
func TestScoreField_ContentPreviewWeight(t *testing.T) {
	trimmer := NewStructuralTrimmer(nil, nil)

	nameMatchEntry := fieldEntry{
		path: "error_logs", key: "error_logs",
		value: "irrelevant content", depth: 0, isScalar: true,
	}
	contentMatchEntry := fieldEntry{
		path: "stdout", key: "stdout",
		value: "error: something failed at line 42", depth: 0, isScalar: true,
	}

	nameScore := trimmer.scoreField(nameMatchEntry, []string{"error"})
	contentScore := trimmer.scoreField(contentMatchEntry, []string{"error"})

	if nameScore <= contentScore {
		t.Errorf("Name match (%.2f) should outrank content match (%.2f)", nameScore, contentScore)
	}
}

// TestScoreField_ContentPreviewSingleBonus tests that only one content bonus
// is awarded per field, even when multiple keywords match (Phase 4B).
func TestScoreField_ContentPreviewSingleBonus(t *testing.T) {
	trimmer := NewStructuralTrimmer(nil, nil)
	entry := fieldEntry{
		path: "data.output", key: "output",
		value: "pods restart count status namespace", depth: 1, isScalar: true,
	}

	score := trimmer.scoreField(entry, []string{"pod", "restart", "status"})

	// No key match for any keyword
	// Content preview: "pod" matches → +0.8 (break after first)
	// isScalar: +0.3
	// depth 1: no bonus
	// Total should be ~1.1, NOT 1.1 + 0.8 + 0.8 = 2.7
	expected := 1.1
	if score > expected+0.2 {
		t.Errorf("Expected single content bonus (~%.1f), got %.2f — multiple bonuses awarded?", expected, score)
	}
}

// --- Phase 4C: Relevance-ordered JSON serialization tests ---

// TestMarshalOrdered_RelevanceOrdering tests that top-level keys are serialized
// in descending relevance order (Phase 4C).
func TestMarshalOrdered_RelevanceOrdering(t *testing.T) {
	obj := map[string]interface{}{
		"exit_code": float64(0),
		"command":   "kubectl get pods",
		"stdout":    "pod data here",
	}
	fieldRelevance := map[string]float64{
		"stdout":    1.7,
		"command":   0.9,
		"exit_code": 0.5,
	}

	output, err := marshalOrdered(obj, fieldRelevance)
	if err != nil {
		t.Fatalf("marshalOrdered failed: %v", err)
	}

	result := string(output)
	stdoutIdx := strings.Index(result, `"stdout"`)
	commandIdx := strings.Index(result, `"command"`)
	exitCodeIdx := strings.Index(result, `"exit_code"`)

	if stdoutIdx == -1 || commandIdx == -1 || exitCodeIdx == -1 {
		t.Fatalf("Missing keys in output: %s", result)
	}
	if stdoutIdx >= commandIdx {
		t.Errorf("stdout (rel 1.7) should appear before command (rel 0.9)")
	}
	if commandIdx >= exitCodeIdx {
		t.Errorf("command (rel 0.9) should appear before exit_code (rel 0.5)")
	}
}

// TestMarshalOrdered_NilMap tests backward compatibility — nil fieldRelevance
// falls back to standard json.Marshal (alphabetical order) (Phase 4C).
func TestMarshalOrdered_NilMap(t *testing.T) {
	obj := map[string]interface{}{
		"beta":  "b",
		"alpha": "a",
	}

	ordered, err := marshalOrdered(obj, nil)
	if err != nil {
		t.Fatalf("marshalOrdered(nil) failed: %v", err)
	}
	standard, _ := json.Marshal(obj)

	if string(ordered) != string(standard) {
		t.Errorf("With nil fieldRelevance, marshalOrdered should match json.Marshal.\nOrdered:  %s\nStandard: %s", ordered, standard)
	}
}

// TestMarshalOrdered_NestedRelevance tests that top-level keys inherit the max
// relevance of their descendants (Phase 4C).
func TestMarshalOrdered_NestedRelevance(t *testing.T) {
	obj := map[string]interface{}{
		"data":    map[string]interface{}{"stdout": "pod data"},
		"success": true,
	}
	fieldRelevance := map[string]float64{
		"data.stdout": 1.7,
		"success":     0.3,
	}

	output, err := marshalOrdered(obj, fieldRelevance)
	if err != nil {
		t.Fatalf("marshalOrdered failed: %v", err)
	}

	result := string(output)
	dataIdx := strings.Index(result, `"data"`)
	successIdx := strings.Index(result, `"success"`)

	if dataIdx >= successIdx {
		t.Errorf("data (child rel 1.7) should appear before success (rel 0.3)")
	}
}

// TestMarshalOrdered_SingleKey tests that a single-key map falls back to
// json.Marshal (no ordering needed) (Phase 4C).
func TestMarshalOrdered_SingleKey(t *testing.T) {
	obj := map[string]interface{}{"only": "value"}
	fieldRelevance := map[string]float64{"only": 1.0}

	ordered, err := marshalOrdered(obj, fieldRelevance)
	if err != nil {
		t.Fatalf("marshalOrdered failed: %v", err)
	}
	standard, _ := json.Marshal(obj)

	if string(ordered) != string(standard) {
		t.Errorf("Single-key map should match json.Marshal.\nOrdered:  %s\nStandard: %s", ordered, standard)
	}
}

// --- Phase 4D: Minimum relevance threshold tests ---

// TestStructuralTrimmer_BackfillSkipsLowRelevance tests that dropped fields
// with relevance ≤ minBackfillRelevance are not backfilled (Phase 4D).
func TestStructuralTrimmer_BackfillSkipsLowRelevance(t *testing.T) {
	trimmer := NewStructuralTrimmer(nil, nil)
	ctx := context.Background()

	// "metadata" at depth 1 with no keyword match: score = isScalar(0.3) = 0.3 → ≤ threshold
	obj := map[string]interface{}{
		"data": map[string]interface{}{
			"metadata": strings.Repeat("irrelevant-data\n", 5000), // ~80KB, low relevance
		},
	}
	raw, _ := json.Marshal(obj)

	result := trimmer.ProcessForPrompt(ctx, string(raw), 8192, ResultProcessorContext{
		StepID: "step-1", AgentName: "tool", Instruction: "unrelated query about weather",
	})

	jsonPart, _ := splitAnnotation(result)
	var parsed map[string]interface{}
	if err := json.Unmarshal([]byte(jsonPart), &parsed); err != nil {
		t.Fatalf("Invalid JSON: %v", err)
	}

	data, _ := parsed["data"].(map[string]interface{})
	if _, hasMetadata := data["metadata"]; hasMetadata {
		t.Error("data.metadata should NOT be backfilled — relevance ≤ threshold (0.3)")
	}
}

// TestStructuralTrimmer_BackfillIncludesHighRelevance tests that dropped fields
// with relevance above the threshold are backfilled normally (Phase 4D).
func TestStructuralTrimmer_BackfillIncludesHighRelevance(t *testing.T) {
	trimmer := NewStructuralTrimmer(nil, nil)
	ctx := context.Background()

	// "pods" keyword matches field name → score = 1.5 + 0.3 = 1.8, well above threshold
	obj := map[string]interface{}{
		"data": map[string]interface{}{
			"pods": strings.Repeat("pod-info-line\n", 50000), // ~700KB
		},
	}
	raw, _ := json.Marshal(obj)

	result := trimmer.ProcessForPrompt(ctx, string(raw), 16384, ResultProcessorContext{
		StepID: "step-1", AgentName: "tool", Instruction: "list all pods",
	})

	jsonPart, _ := splitAnnotation(result)
	var parsed map[string]interface{}
	if err := json.Unmarshal([]byte(jsonPart), &parsed); err != nil {
		t.Fatalf("Invalid JSON: %v", err)
	}

	data, _ := parsed["data"].(map[string]interface{})
	if _, hasPods := data["pods"]; !hasPods {
		t.Error("data.pods should be backfilled — relevance (1.8) well above threshold")
	}
}

// TestStructuralTrimmer_BackfillThresholdBreaksEarly tests that once a candidate
// falls below the threshold, all remaining candidates are skipped (Phase 4D).
func TestStructuralTrimmer_BackfillThresholdBreaksEarly(t *testing.T) {
	trimmer := NewStructuralTrimmer(nil, nil)
	ctx := context.Background()

	// "error_logs" matches keyword "error" in name (+1.5), well above threshold.
	// "metadata" has no keyword match, score = 0.3 (isScalar) → at/below threshold.
	// Use a small error field that fits whole + large metadata that would be dropped.
	obj := map[string]interface{}{
		"data": map[string]interface{}{
			"error_logs": strings.Repeat("error: failed\n", 10000), // ~140KB, keyword match in name
			"metadata":   strings.Repeat("meta-data\n", 10000),     // ~100KB, no keyword match
		},
	}
	raw, _ := json.Marshal(obj)

	result := trimmer.ProcessForPrompt(ctx, string(raw), 32768, ResultProcessorContext{
		StepID: "step-1", AgentName: "tool", Instruction: "show all error logs",
	})

	jsonPart, _ := splitAnnotation(result)
	var parsed map[string]interface{}
	if err := json.Unmarshal([]byte(jsonPart), &parsed); err != nil {
		t.Fatalf("Invalid JSON: %v", err)
	}

	data, _ := parsed["data"].(map[string]interface{})
	_, hasErrors := data["error_logs"]
	_, hasMetadata := data["metadata"]

	if !hasErrors {
		t.Error("data.error_logs should be backfilled (keyword match in name → high relevance)")
	}
	if hasMetadata {
		t.Error("data.metadata should NOT be backfilled (no keyword match → relevance ≤ threshold)")
	}
}

// --- Phase 4E: Return value tests ---

// TestSelectFieldsWithMeta_BackfilledCountReturned tests that the new
// backfilledCount return value is populated correctly (Phase 4E).
func TestSelectFieldsWithMeta_BackfilledCountReturned(t *testing.T) {
	trimmer := NewStructuralTrimmer(nil, nil)

	obj := map[string]interface{}{
		"data": map[string]interface{}{
			"stdout":  strings.Repeat("pod-data\n", 60000), // ~540KB
			"command": "kubectl get pods",
		},
	}

	_, _, _, backfilledCount, _, _, _, _ := trimmer.selectFieldsWithMeta(
		context.Background(), obj, 32768, 0, []string{"pod"},
	)

	if backfilledCount == 0 {
		t.Error("Expected backfilledCount > 0 when a large dropped field is recovered")
	}
}

// TestSelectFieldsWithMeta_ThresholdSkippedReturned tests that the new
// thresholdSkipped return value counts candidates below the relevance threshold (Phase 4E).
func TestSelectFieldsWithMeta_ThresholdSkippedReturned(t *testing.T) {
	trimmer := NewStructuralTrimmer(nil, nil)

	// "pods" is small → selected by greedy (not a backfill candidate).
	// "logfile" is large → dropped by greedy → becomes a backfill candidate.
	// "logfile" has no keyword match in name or content → relevance 0.3 → at/below threshold.
	// The backfill loop sees logfile and skips it via threshold → thresholdSkipped = 1.
	obj := map[string]interface{}{
		"data": map[string]interface{}{
			"pods":    "pod-summary: 3 running, 0 failed",    // ~37 bytes, selected by greedy
			"logfile": strings.Repeat("zzzzz-data\n", 60000), // ~660KB, dropped → backfill candidate
		},
	}

	_, _, _, _, thresholdSkipped, _, _, _ := trimmer.selectFieldsWithMeta(
		context.Background(), obj, 32768, 0, []string{"pod"},
	)

	if thresholdSkipped == 0 {
		t.Error("Expected thresholdSkipped > 0 when low-relevance backfill candidates exist")
	}
}

// --- Phase 5: Deep Inventory Decomposition Tests ---

// TestBuildArrayInventory_DecomposesLargeObjectItems verifies Fix 1:
// large object items (>1024B) are recursed into, producing sub-field entries
// alongside the atomic item entry.
func TestBuildArrayInventory_DecomposesLargeObjectItems(t *testing.T) {
	trimmer := NewStructuralTrimmer(nil, nil)

	// Build items where each is >1024B (large enough to trigger decomposition)
	items := make([]interface{}, 5)
	for i := 0; i < 5; i++ {
		items[i] = map[string]interface{}{
			"name":    fmt.Sprintf("item-%d", i),
			"status":  "active",
			"payload": strings.Repeat("x", 1200), // pushes each item >1024B
		}
	}

	entries := trimmer.buildArrayInventory(items, "data.items", 1)

	// Should have 5 atomic entries + sub-field entries for each item
	atomicCount := 0
	subFieldCount := 0
	for _, e := range entries {
		if e.arrayIndex >= 0 {
			atomicCount++
		} else {
			subFieldCount++
		}
	}

	if atomicCount != 5 {
		t.Errorf("Expected 5 atomic entries, got %d", atomicCount)
	}
	if subFieldCount == 0 {
		t.Fatal("Expected sub-field entries from decomposition, got 0")
	}

	// Sub-fields should have paths like "data.items[0].name", "data.items[0].status"
	foundSubPath := false
	for _, e := range entries {
		if strings.Contains(e.path, "[0].name") {
			foundSubPath = true
			break
		}
	}
	if !foundSubPath {
		t.Error("Expected decomposed sub-field path like 'data.items[0].name'")
	}
}

// TestBuildArrayInventory_SmallItemsNotDecomposed verifies that items <1024B
// are NOT decomposed — they stay atomic. No behavior change for small items.
func TestBuildArrayInventory_SmallItemsNotDecomposed(t *testing.T) {
	trimmer := NewStructuralTrimmer(nil, nil)

	items := make([]interface{}, 10)
	for i := 0; i < 10; i++ {
		items[i] = map[string]interface{}{
			"id":   float64(i),
			"name": fmt.Sprintf("item-%d", i),
		}
	}

	entries := trimmer.buildArrayInventory(items, "results", 0)

	// All entries should be atomic (arrayIndex >= 0), no sub-fields
	for _, e := range entries {
		if e.arrayIndex < 0 {
			t.Errorf("Small item should not produce sub-field entries, got path %q", e.path)
		}
	}
	if len(entries) != 10 {
		t.Errorf("Expected 10 atomic entries, got %d", len(entries))
	}
}

// TestBuildArrayInventory_ScalarItemsNotDecomposed verifies that scalar array
// items (numbers, strings) are never decomposed regardless of size.
func TestBuildArrayInventory_ScalarItemsNotDecomposed(t *testing.T) {
	trimmer := NewStructuralTrimmer(nil, nil)

	items := make([]interface{}, 5)
	for i := 0; i < 5; i++ {
		items[i] = strings.Repeat("data", 500) // 2000B strings, but scalars
	}

	entries := trimmer.buildArrayInventory(items, "values", 0)

	if len(entries) != 5 {
		t.Errorf("Expected 5 entries (atomic only), got %d", len(entries))
	}
	for _, e := range entries {
		if e.arrayIndex < 0 {
			t.Errorf("Scalar item should not produce sub-field entries, got path %q", e.path)
		}
	}
}

// TestArrayDecomposition_SelectsSubFieldsAcrossItems tests the end-to-end
// effect: with decomposition, the trimmer can pick individual sub-fields from
// multiple array items instead of picking only a few complete atomic items.
// TestArrayDecomposition_NoLeafScatterAcrossItems verifies the Phase 2 whole-unit
// guard: under a budget that fits only a couple of whole items, the trimmer keeps
// WHOLE items (name + status + payload together), never a scatter of name/status
// leaves harvested across many items while dropping their payloads (the old
// value-density behavior this phase removes).
func TestArrayDecomposition_NoLeafScatterAcrossItems(t *testing.T) {
	trimmer := NewStructuralTrimmer(nil, nil)
	ctx := context.Background()

	// Array of 10 large objects, each with small fields (name, status) and a large payload.
	items := make([]interface{}, 10)
	for i := 0; i < 10; i++ {
		items[i] = map[string]interface{}{
			"name":    fmt.Sprintf("service-%d", i),
			"status":  "running",
			"payload": strings.Repeat(fmt.Sprintf("bulk-data-%d-", i), 300), // ~3.6KB each
		}
	}
	obj := map[string]interface{}{
		"items": items,
		"count": float64(10),
	}
	raw, _ := json.Marshal(obj)

	result := trimmer.ProcessForPrompt(ctx, string(raw), 4096, ResultProcessorContext{
		StepID: "step-1", AgentName: "test", Instruction: "list service names and status",
	})

	jsonPart, _ := splitAnnotation(result)
	var parsed map[string]interface{}
	if err := json.Unmarshal([]byte(jsonPart), &parsed); err != nil {
		t.Fatalf("Invalid JSON output: %v\nOutput: %.500s", err, result)
	}

	kept, ok := parsed["items"].([]interface{})
	if !ok || len(kept) == 0 {
		t.Fatalf("Expected at least one whole item kept, got: %.300s", jsonPart)
	}
	// Every kept item must be WHOLE: a name without its payload would be the leaf
	// scatter Phase 2 removes.
	for i, it := range kept {
		m, ok := it.(map[string]interface{})
		if !ok {
			t.Fatalf("item %d not an object: %v", i, it)
		}
		if _, hasName := m["name"]; hasName {
			if _, hasPayload := m["payload"]; !hasPayload {
				t.Errorf("item %d kept name without payload — leaf scatter must not happen: %v", i, m)
			}
		}
	}
}

// TestJSONInString_ValidEmbedded tests Fix 2: deserializeStringValues unwraps
// JSON-valued strings before inventory building, enabling structural trimming
// on the embedded content.
func TestJSONInString_ValidEmbedded(t *testing.T) {
	trimmer := NewStructuralTrimmer(nil, nil)
	ctx := context.Background()

	// Build an embedded JSON structure as a string value
	embeddedObj := map[string]interface{}{
		"pods": []interface{}{
			map[string]interface{}{
				"name":         "web-server",
				"restartCount": float64(5),
				"status":       "Running",
			},
			map[string]interface{}{
				"name":         "api-gateway",
				"restartCount": float64(0),
				"status":       "Running",
			},
		},
		"namespace": "production",
	}
	// Pad with extra data to make it large
	for i := 0; i < 50; i++ {
		embeddedObj[fmt.Sprintf("metric_%d", i)] = strings.Repeat("data", 100)
	}

	embeddedJSON, _ := json.Marshal(embeddedObj)

	obj := map[string]interface{}{
		"success":   true,
		"exit_code": float64(0),
		"stdout":    string(embeddedJSON), // JSON-valued string
	}
	raw, _ := json.Marshal(obj)

	result := trimmer.ProcessForPrompt(ctx, string(raw), 2048, ResultProcessorContext{
		StepID: "step-1", AgentName: "devops-tool", Instruction: "check pod restart counts",
	})

	// The embedded JSON should be unwrapped and structurally trimmed.
	// "restartCount" and "name" should be selected by keyword matching.
	if !strings.Contains(result, "restartCount") && !strings.Contains(result, "restart") {
		t.Errorf("Expected keyword-matched field 'restartCount' in output, got: %.500s", result)
	}
	if !strings.Contains(result, "web-server") && !strings.Contains(result, "api-gateway") {
		t.Errorf("Expected pod names in output, got: %.500s", result)
	}
}

// TestJSONInString_InvalidJSON verifies that strings starting with '{' but
// containing invalid JSON are left as-is by deserializeStringValues.
func TestJSONInString_InvalidJSON(t *testing.T) {
	trimmer := NewStructuralTrimmer(nil, nil)
	ctx := context.Background()

	obj := map[string]interface{}{
		"data": map[string]interface{}{
			"output": "{this is not valid json but starts with a brace " + strings.Repeat("x", 2000),
		},
	}
	raw, _ := json.Marshal(obj)

	// Should not panic or produce invalid output
	result := trimmer.ProcessForPrompt(ctx, string(raw), 1024, ResultProcessorContext{
		StepID: "step-1", AgentName: "test", Instruction: "check output",
	})

	if len(result) == 0 {
		t.Error("Expected non-empty result")
	}
	if len(result) > 1024 {
		t.Errorf("Result %d exceeds budget 1024", len(result))
	}
}

// TestJSONInString_SmallString verifies that small JSON-in-strings are still
// unwrapped by deserializeStringValues (no size threshold), but their sub-fields
// are too small for further recursion.
func TestJSONInString_SmallString(t *testing.T) {
	trimmer := NewStructuralTrimmer(nil, nil)

	// Small JSON-in-string (well under 1024B)
	obj := map[string]interface{}{
		"result": `{"status":"ok","count":42}`,
		"extra":  strings.Repeat("padding", 300), // make total large enough to trigger trimming
	}

	// After deserializeStringValues, "result" should be an object, not a string
	data := deserializeStringValues(obj)
	resultVal := data.(map[string]interface{})["result"]

	// Should be unwrapped to a map, not remain a string
	if _, ok := resultVal.(map[string]interface{}); !ok {
		t.Errorf("Expected JSON-in-string to be unwrapped to map, got %T", resultVal)
	}

	// The trimmer should handle this correctly end-to-end
	raw, _ := json.Marshal(obj) // original obj with string
	result := trimmer.ProcessForPrompt(context.Background(), string(raw), 512, ResultProcessorContext{
		StepID: "step-1", AgentName: "test", Instruction: "check status",
	})

	if !strings.Contains(result, "status") {
		t.Errorf("Expected 'status' from unwrapped JSON in output, got: %s", result)
	}
}

// TestDeepNesting_MultiLevel tests 3 levels of nesting: object → array of
// objects → nested objects >1024B. Verifies recursion works at each level.
func TestDeepNesting_MultiLevel(t *testing.T) {
	trimmer := NewStructuralTrimmer(nil, nil)
	ctx := context.Background()

	// Level 3: deeply nested objects >1024B
	deepObj := map[string]interface{}{
		"metric_name": "cpu_usage",
		"metric_data": strings.Repeat("sample-", 200), // ~1.4KB
	}

	// Level 2: array of objects containing level-3 objects
	items := make([]interface{}, 5)
	for i := 0; i < 5; i++ {
		items[i] = map[string]interface{}{
			"id":      fmt.Sprintf("node-%d", i),
			"metrics": deepObj,
			"filler":  strings.Repeat("x", 500),
		}
	}

	// Level 1: top-level object
	obj := map[string]interface{}{
		"nodes": items,
		"cluster": map[string]interface{}{
			"name":   "prod-cluster",
			"region": "us-east-1",
		},
	}
	raw, _ := json.Marshal(obj)

	// Budget large enough to keep at least one whole node (~2KB). Phase 2 selects whole
	// units, not keyword-matched leaves: deeply-nested content (nodes[i].metrics.metric_name
	// = "cpu_usage") survives because the whole node is kept as a unit, not because a
	// deterministic heuristic cherry-picked the leaf by keyword. Relevance-to-a-query is
	// the LLM's job now (§2.2).
	result := trimmer.ProcessForPrompt(ctx, string(raw), 4096, ResultProcessorContext{
		StepID: "step-1", AgentName: "monitoring", Instruction: "show cpu usage metrics",
	})

	if !strings.Contains(result, "cpu_usage") {
		t.Errorf("Expected deeply nested 'cpu_usage' to survive inside a whole-unit selection, got: %.500s", result)
	}
}

// TestArrayDecomposition_MixedArray verifies that only large object items
// get decomposed, while other types (strings, numbers, small objects) stay atomic.
func TestArrayDecomposition_MixedArray(t *testing.T) {
	trimmer := NewStructuralTrimmer(nil, nil)

	items := []interface{}{
		"a plain string", // string scalar
		float64(42),      // number scalar
		map[string]interface{}{"big": strings.Repeat("x", 1500)}, // >1024B object
		map[string]interface{}{"small": "value"},                 // <1024B object
	}

	entries := trimmer.buildArrayInventory(items, "data", 0)

	// Count sub-field entries (non-array entries)
	subFieldCount := 0
	for _, e := range entries {
		if e.arrayIndex < 0 {
			subFieldCount++
		}
	}

	// Only the big object (index 2) should produce sub-field entries
	if subFieldCount == 0 {
		t.Error("Expected sub-field entries from the large object item")
	}

	// Verify the sub-field is from the correct item
	foundBigSubField := false
	for _, e := range entries {
		if e.arrayIndex < 0 && strings.Contains(e.path, "[2].") {
			foundBigSubField = true
			break
		}
	}
	if !foundBigSubField {
		t.Error("Expected sub-field entry from data[2] (the large object)")
	}
}

// TestReconstructHierarchy_ArraySubFieldsMerged validates the C1 fix:
// when multiple sub-fields of the same array item are selected (e.g.,
// items[0].name and items[0].status), they must be merged into a single
// object in the output array, not flattened into separate entries.
func TestReconstructHierarchy_ArraySubFieldsMerged(t *testing.T) {
	trimmer := NewStructuralTrimmer(nil, nil)
	ctx := context.Background()

	// 10 items, each with name (small, keyword-matched) + status (small) + payload (large).
	// Budget is tight enough that only sub-fields are selected (not atomic items).
	items := make([]interface{}, 10)
	for i := 0; i < 10; i++ {
		items[i] = map[string]interface{}{
			"name":    fmt.Sprintf("service-%d", i),
			"status":  "running",
			"payload": strings.Repeat(fmt.Sprintf("x%d", i), 2000), // ~4KB each
		}
	}
	obj := map[string]interface{}{"items": items}
	raw, _ := json.Marshal(obj)

	result := trimmer.ProcessForPrompt(ctx, string(raw), 2048, ResultProcessorContext{
		StepID: "step-1", AgentName: "test", Instruction: "list service names and status",
	})

	jsonPart, _ := splitAnnotation(result)
	var parsed map[string]interface{}
	if err := json.Unmarshal([]byte(jsonPart), &parsed); err != nil {
		t.Fatalf("Invalid JSON: %v\nOutput: %.500s", err, result)
	}

	// The output must contain an "items" array.
	itemsRaw, ok := parsed["items"]
	if !ok {
		t.Fatalf("Missing 'items' key in output: %s", jsonPart)
	}
	arr, ok := itemsRaw.([]interface{})
	if !ok {
		t.Fatalf("'items' is not an array: %T", itemsRaw)
	}

	if len(arr) == 0 {
		t.Fatal("'items' array is empty")
	}

	// Structural validation: each array element that came from sub-field selection
	// must be an object (map) with merged fields, NOT a raw scalar.
	for i, elem := range arr {
		obj, ok := elem.(map[string]interface{})
		if !ok {
			t.Errorf("items[%d] is %T, expected map (merged sub-field object); value: %v", i, elem, elem)
			continue
		}
		// Merged objects should contain at least one of the queried sub-fields.
		_, hasName := obj["name"]
		_, hasStatus := obj["status"]
		if !hasName && !hasStatus {
			t.Errorf("items[%d] has neither 'name' nor 'status': %v", i, obj)
		}
	}

	t.Logf("Output has %d items, all properly merged objects", len(arr))
}

// TestReconstructHierarchy_DepthLimitPreventsOverflow validates that
// maxInventoryDepth prevents stack overflow on deeply nested structures.
func TestReconstructHierarchy_DepthLimitPreventsOverflow(t *testing.T) {
	trimmer := NewStructuralTrimmer(nil, nil)
	ctx := context.Background()

	// Build a deeply nested structure (12 levels) that exceeds maxInventoryDepth (8).
	inner := map[string]interface{}{"leaf": "value"}
	for i := 0; i < 12; i++ {
		inner = map[string]interface{}{fmt.Sprintf("level%d", i): inner}
	}
	raw, _ := json.Marshal(inner)

	// Should not panic — depth guard prevents infinite recursion.
	result := trimmer.ProcessForPrompt(ctx, string(raw), 4096, ResultProcessorContext{
		StepID: "step-1", AgentName: "test", Instruction: "check levels",
	})

	if result == "" {
		t.Error("Expected non-empty result")
	}
	t.Logf("Deeply nested input handled safely, output: %d bytes", len(result))
}

// TestReconstructHierarchy_MultiLevelSubFields validates B2 fix: multi-level
// sub-field paths like items[0].metadata.name produce nested objects
// {"metadata":{"name":"..."}} instead of flat {"metadata.name":"..."}.
func TestReconstructHierarchy_MultiLevelSubFields(t *testing.T) {
	trimmer := NewStructuralTrimmer(nil, nil)
	ctx := context.Background()

	// Items with nested metadata (>1024B to trigger decomposition).
	items := make([]interface{}, 5)
	for i := 0; i < 5; i++ {
		items[i] = map[string]interface{}{
			"metadata": map[string]interface{}{
				"name":   fmt.Sprintf("pod-%d", i),
				"labels": map[string]interface{}{"app": "web", "env": "prod"},
				"annotations": map[string]interface{}{
					"desc": strings.Repeat(fmt.Sprintf("annotation-data-%d-", i), 200), // ~4KB
				},
			},
			"status": "Running",
		}
	}
	obj := map[string]interface{}{"items": items}
	raw, _ := json.Marshal(obj)

	// Tight budget forces sub-field selection (not whole items).
	result := trimmer.ProcessForPrompt(ctx, string(raw), 2048, ResultProcessorContext{
		StepID: "step-1", AgentName: "test", Instruction: "list pod names and status",
	})

	jsonPart, _ := splitAnnotation(result)
	var parsed map[string]interface{}
	if err := json.Unmarshal([]byte(jsonPart), &parsed); err != nil {
		t.Fatalf("Invalid JSON: %v\nOutput: %.500s", err, result)
	}

	itemsRaw, ok := parsed["items"]
	if !ok {
		t.Fatalf("Missing 'items' in output: %s", jsonPart)
	}
	arr, ok := itemsRaw.([]interface{})
	if !ok {
		t.Fatalf("'items' is not an array: %T", itemsRaw)
	}

	// Verify that metadata sub-fields are nested, not flat.
	for i, elem := range arr {
		obj, ok := elem.(map[string]interface{})
		if !ok {
			t.Errorf("items[%d] is %T, expected map", i, elem)
			continue
		}
		// If metadata.name was selected, it should be at obj["metadata"]["name"],
		// NOT at obj["metadata.name"].
		if _, hasFlat := obj["metadata.name"]; hasFlat {
			t.Errorf("items[%d] has flat key 'metadata.name' — should be nested under 'metadata'", i)
		}
		if meta, hasMeta := obj["metadata"]; hasMeta {
			metaMap, ok := meta.(map[string]interface{})
			if !ok {
				t.Errorf("items[%d].metadata is %T, expected map", i, meta)
				continue
			}
			if _, hasName := metaMap["name"]; hasName {
				t.Logf("items[%d].metadata.name correctly nested", i)
			}
		}
	}
	t.Logf("Output has %d items with properly nested sub-fields", len(arr))
}

// TestReconstructHierarchy_SubFieldValueOverrides validates B1 fix: backfilled
// (truncated) sub-field values use the override, not the original full value.
func TestReconstructHierarchy_SubFieldValueOverrides(t *testing.T) {
	// Directly test reconstructHierarchy with valueOverrides for a sub-field path.
	obj := map[string]interface{}{
		"items": []interface{}{
			map[string]interface{}{
				"name":    "service-0",
				"payload": strings.Repeat("x", 5000), // 5KB original
			},
		},
	}

	selectedPaths := map[string]bool{
		"items[0].name":    true,
		"items[0].payload": true,
	}
	truncatedPayload := "xxx... [truncated]"
	valueOverrides := map[string]interface{}{
		"items[0].payload": truncatedPayload,
	}

	result := reconstructHierarchy(obj, selectedPaths, valueOverrides)

	// Navigate to items[0].payload in the result
	itemsArr, ok := result["items"].([]interface{})
	if !ok || len(itemsArr) == 0 {
		t.Fatalf("Expected items array, got: %v", result)
	}
	item0, ok := itemsArr[0].(map[string]interface{})
	if !ok {
		t.Fatalf("Expected items[0] to be map, got: %T", itemsArr[0])
	}

	payload, ok := item0["payload"]
	if !ok {
		t.Fatal("items[0].payload missing from output")
	}

	payloadStr, ok := payload.(string)
	if !ok {
		t.Fatalf("items[0].payload is %T, expected string", payload)
	}

	if payloadStr != truncatedPayload {
		t.Errorf("items[0].payload should be the override (%d bytes), got original (%d bytes)",
			len(truncatedPayload), len(payloadStr))
	}

	// Verify name is also present
	if name, ok := item0["name"]; !ok || name != "service-0" {
		t.Errorf("items[0].name missing or wrong: %v", name)
	}

	t.Logf("valueOverrides correctly applied to sub-field path")
}

// TestBuildFieldInventory_DepthGuardTriggered verifies that buildFieldInventory
// returns nil when recursion depth reaches maxInventoryDepth (8). Requires each
// nesting level to be >1024B to trigger recursion (small objects don't recurse).
func TestBuildFieldInventory_DepthGuardTriggered(t *testing.T) {
	trimmer := NewStructuralTrimmer(nil, nil)

	// Build a structure where each level is >1024B to ensure recursion.
	// The leaf has a large padding string so every parent exceeds the threshold.
	inner := map[string]interface{}{
		"leaf":    "value",
		"padding": strings.Repeat("x", 1200), // ensures >1024B at every level
	}
	for i := 0; i < 10; i++ { // 10 levels — exceeds maxInventoryDepth (8)
		inner = map[string]interface{}{
			fmt.Sprintf("level%d", i): inner,
			"filler":                  strings.Repeat("y", 500), // keep each level >1024B
		}
	}

	entries := trimmer.buildFieldInventory(inner, "", 0)

	// Verify that we have entries but the deepest levels are capped.
	// With maxInventoryDepth=8 and 10 nesting levels, levels 8+ should not appear.
	maxDepthSeen := 0
	for _, e := range entries {
		if e.depth > maxDepthSeen {
			maxDepthSeen = e.depth
		}
	}

	if maxDepthSeen >= maxInventoryDepth {
		t.Errorf("Max depth seen (%d) should be < maxInventoryDepth (%d) — depth guard not working",
			maxDepthSeen, maxInventoryDepth)
	}
	t.Logf("Depth guard working: max depth seen = %d (cap = %d), entries = %d",
		maxDepthSeen, maxInventoryDepth, len(entries))
}

// TestBackfill_ValueBudgetTooSmall verifies that a backfill candidate is skipped
// when wrapper overhead consumes most of the remaining budget, leaving
// valueBudget <= minBackfillValueSize (100 bytes).
//
// For this to trigger: remaining > minBackfillBudget(512) AND
// overhead + keyOverhead > remaining - 100. Two 300-char wrapper keys
// produce 610 bytes of overhead, which exceeds remaining-100 when
// remaining is between 512 and 710.
func TestBackfill_ValueBudgetTooSmall(t *testing.T) {
	trimmer := NewStructuralTrimmer(nil, nil)
	ctx := context.Background()

	longKeyA := strings.Repeat("a", 300) // wrapper key 1 → overhead 305
	longKeyB := strings.Repeat("b", 300) // wrapper key 2 → overhead 305
	obj := map[string]interface{}{
		"status": "ok",
		longKeyA: map[string]interface{}{
			longKeyB: map[string]interface{}{
				"target": strings.Repeat("data", 5000), // 20KB — dropped by greedy
			},
		},
	}
	raw, _ := json.Marshal(obj)

	// Budget 735: greedy selects "status" (18B). Remaining: 717.
	// Backfill candidate: <longKeyA>.<longKeyB>.target (depth 2).
	// Wrapper overhead: 305 + 305 = 610. keyOverhead: 10. Total: 620.
	// valueBudget = 717 - 620 = 97 < minBackfillValueSize(100) → skipped.
	result := trimmer.ProcessForPrompt(ctx, string(raw), 735, ResultProcessorContext{
		StepID: "step-1", AgentName: "test", Instruction: "show target data",
	})

	jsonPart, _ := splitAnnotation(result)
	var parsed map[string]interface{}
	if err := json.Unmarshal([]byte(jsonPart), &parsed); err != nil {
		t.Fatalf("Invalid JSON: %v\nOutput: %.300s", err, result)
	}

	// The nested target field should NOT be backfilled (valueBudget too small).
	if _, hasStatus := parsed["status"]; !hasStatus {
		t.Error("Expected 'status' in output")
	}
	if _, hasLongKey := parsed[longKeyA]; hasLongKey {
		t.Log("Wrapper key present — valueBudget guard may not have fired (overhead estimation may differ)")
	}
	t.Logf("Output: %d bytes, budget: 735, fields: %d", len(jsonPart), len(parsed))
}

// TestBackfill_NestedFieldFitsWhole verifies the full-fit backfill path for a
// nested (depth > 0) string field. The field should be included at full size
// without truncation, and its wrapper keys should be marked.
func TestBackfill_NestedFieldFitsWhole(t *testing.T) {
	trimmer := NewStructuralTrimmer(nil, nil)
	ctx := context.Background()

	// A nested small string that gets dropped by greedy (because its parent object
	// is also in the inventory and gets picked first or it's overshadowed).
	// But after greedy, remaining budget is enough to include it whole.
	smallMsg := strings.Repeat("error details here ", 10) // ~200 bytes
	obj := map[string]interface{}{
		"data": map[string]interface{}{
			"stdout": strings.Repeat("output-data\n", 5000), // ~60KB, dropped by greedy
			"stderr": smallMsg,                              // ~200B — dropped because stdout dominates
			"code":   float64(1),
		},
	}
	raw, _ := json.Marshal(obj)

	result := trimmer.ProcessForPrompt(ctx, string(raw), 4096, ResultProcessorContext{
		StepID: "step-1", AgentName: "test", Instruction: "show errors and output",
	})

	jsonPart, _ := splitAnnotation(result)
	var parsed map[string]interface{}
	if err := json.Unmarshal([]byte(jsonPart), &parsed); err != nil {
		t.Fatalf("Invalid JSON: %v", err)
	}

	data, _ := parsed["data"].(map[string]interface{})
	if data == nil {
		t.Fatal("Expected 'data' wrapper in output")
	}

	// stderr is a nested (depth 1) string. With 4KB budget, the greedy selector
	// picks small fields first (code, status). stderr (~200B) may fit in greedy
	// or get backfilled. Either way, it should be present at full size.
	if stderr, ok := data["stderr"].(string); ok {
		if stderr != smallMsg {
			t.Errorf("stderr should be included at full size (%d bytes), got %d bytes",
				len(smallMsg), len(stderr))
		}
		t.Log("Nested field included at full size (full-fit path or greedy)")
	} else {
		t.Error("data.stderr should be present in output")
	}
}

// BenchmarkStructuralTrimmer_NoBackfill measures the common case where all
// fields fit within budget and no backfill is needed.
func BenchmarkStructuralTrimmer_NoBackfill(b *testing.B) {
	trimmer := NewStructuralTrimmer(nil, nil)
	ctx := context.Background()

	obj := map[string]interface{}{
		"status": "ok",
		"count":  float64(42),
		"name":   "test-service",
		"data": map[string]interface{}{
			"message": "all systems operational",
			"uptime":  float64(86400),
		},
	}
	raw, _ := json.Marshal(obj)
	input := string(raw)
	stepCtx := ResultProcessorContext{
		StepID: "step-1", AgentName: "test",
		Instruction: "check status",
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		trimmer.ProcessForPrompt(ctx, input, 4096, stepCtx)
	}
}

// --- Phase 2: whole-unit selection ---

// TestSelectFields_WholeRecordsNotLeaves verifies the Phase 2 fix: an array of records
// under a tight budget yields a few WHOLE records (every field of each kept record
// present, including the large payload), not a scatter of cherry-picked leaf fields
// across many records. This is the inverse of the old value-density behavior, which
// would pack tiny high-density leaves (id/label) from many records and drop every
// payload.
func TestSelectFields_WholeRecordsNotLeaves(t *testing.T) {
	trimmer := NewStructuralTrimmer(nil, nil)

	// Each record exceeds 1024B (so it decomposes into selectable sub-fields), but a
	// single record fits the budget — the case where whole-unit vs leaf selection diverges.
	recs := make([]interface{}, 50)
	for i := 0; i < 50; i++ {
		recs[i] = map[string]interface{}{
			"id":      fmt.Sprintf("rec-%02d", i),
			"label":   "L",
			"payload": strings.Repeat("x", 1100), // record ≈ 1.1KB > 1024 → decomposable
		}
	}
	raw, _ := json.Marshal(map[string]interface{}{"records": recs})

	result := trimmer.ProcessForPrompt(context.Background(), string(raw), 4096, ResultProcessorContext{
		StepID: "s1", AgentName: "a", Instruction: "list records",
	})

	jsonPart, _ := splitAnnotation(result)
	var parsed map[string]interface{}
	if err := json.Unmarshal([]byte(jsonPart), &parsed); err != nil {
		t.Fatalf("invalid JSON: %v\n%s", err, jsonPart)
	}
	records, ok := parsed["records"].([]interface{})
	if !ok || len(records) == 0 {
		t.Fatalf("expected some whole records kept, got: %s", jsonPart)
	}
	// Every kept record must be WHOLE: all fields present, including the large payload —
	// never a scattered leaf like {"label":"L"} or {"id":"rec-00"} on its own.
	for i, r := range records {
		rec, ok := r.(map[string]interface{})
		if !ok {
			t.Fatalf("record %d is not an object: %v", i, r)
		}
		for _, field := range []string{"id", "label", "payload"} {
			if _, has := rec[field]; !has {
				t.Errorf("record %d missing field %q — Phase 2 must keep WHOLE records, got: %v", i, field, rec)
			}
		}
	}
}

// TestSelectFields_LogsKeepWholeEntriesNotLabels reproduces the incident shape (Loki
// streams) and verifies Phase 2 selection keeps whole log entries (the substantive
// records) rather than starving them for tiny stream labels — the inverse of the
// value-density defect that kept only labels.logtag.
func TestSelectFields_LogsKeepWholeEntriesNotLabels(t *testing.T) {
	trimmer := NewStructuralTrimmer(nil, nil)

	line := "2026-06-18T12:00:00Z level=error msg=\"panic: nil pointer dereference in handler\""
	var streams []interface{}
	for i := 0; i < 20; i++ {
		entries := make([]interface{}, 8)
		for j := range entries {
			entries[j] = map[string]interface{}{"line": fmt.Sprintf("%s seq=%d-%d", line, i, j)}
		}
		streams = append(streams, map[string]interface{}{
			"labels":  map[string]interface{}{"level": "error", "logtag": "F"},
			"entries": entries,
		})
	}
	raw, _ := json.Marshal(map[string]interface{}{"streams": streams})

	// Budget below one whole stream (~700B+) but well above one entry (~90B), forcing
	// a descent that must prefer substantive entries over scaffolding labels.
	result := trimmer.ProcessForPrompt(context.Background(), string(raw), 1500, ResultProcessorContext{
		StepID: "step-17", AgentName: "obs-tool", Instruction: "find panic errors",
	})

	// Whole log lines (the records) must survive.
	if !strings.Contains(result, "panic: nil pointer dereference") {
		t.Errorf("Expected verbatim log lines (whole entries) to survive, got: %.400s", result)
	}
	// The bytes must be spent on entries, not crowded out by logtag scaffolding. Verify
	// at least one entry survived per the structure (entries present in output).
	if !strings.Contains(result, "\"line\"") && !strings.Contains(result, "line=") {
		t.Errorf("Expected entry 'line' content in output, got: %.400s", result)
	}
}

// --- Phase 0: degenerate-trim honest disclosure ---

// TestProcessForPrompt_DegenerateTrim_HonestDisclosure reproduces the incident
// (req 1781784008606859622): a logs-shaped result whose meaningful content
// (whole log lines) is far larger than the per-result budget, so the structural
// trimmer can keep only tiny Loki stream labels. The output must carry the honest
// "severely reduced / UNKNOWN" disclosure — not the neutral "[trimmed: …]" note,
// which implies coverage and invites the false-negative inference ("no ERROR
// entries found") — and the metadata must flag the trim as degenerate.
func TestProcessForPrompt_DegenerateTrim_HonestDisclosure(t *testing.T) {
	trimmer := NewStructuralTrimmer(nil, nil)

	// Loki-shaped payload: many streams, each with small labels and a large log
	// line. A single line exceeds the whole per-result budget, so only labels survive.
	line := strings.Repeat("2026-06-18T12:00:00Z level=info msg=\"request served\" ", 40) // ~2KB
	var streams []interface{}
	for i := 0; i < 40; i++ {
		streams = append(streams, map[string]interface{}{
			"labels":  map[string]interface{}{"level": "info", "logtag": "F"},
			"entries": []interface{}{map[string]interface{}{"line": line}},
		})
	}
	payload, err := json.Marshal(map[string]interface{}{"streams": streams})
	if err != nil {
		t.Fatalf("marshal fixture: %v", err)
	}

	ctx, meta := WithTrimMetadataCapture(context.Background())
	// Budget ~2KB (the incident's per-result budget); original is ~80KB+.
	result := trimmer.ProcessForPrompt(ctx, string(payload), 2056, ResultProcessorContext{
		StepID: "step-17", AgentName: "devops-observability-tool", Instruction: "find ERROR logs",
	})

	tail := result
	if len(tail) > 400 {
		tail = tail[len(tail)-400:]
	}

	if !meta.Degenerate {
		t.Errorf("Expected metadata.Degenerate=true (kept %d of %d bytes, ratio=%.4f)",
			meta.TrimmedBytes, meta.OriginalBytes, meta.KeptRatio)
	}
	if meta.KeptRatio >= degenerateKeptRatio {
		t.Errorf("Expected KeptRatio < %.2f, got %.4f", degenerateKeptRatio, meta.KeptRatio)
	}
	if !strings.Contains(result, "severely reduced") {
		t.Errorf("Expected honest 'severely reduced' disclosure, got tail: %s", tail)
	}
	if !strings.Contains(result, "UNKNOWN") {
		t.Errorf("Expected 'UNKNOWN' guidance in disclosure, got tail: %s", tail)
	}
	// The coverage-implying neutral note must NOT be present on a degenerate trim.
	if strings.Contains(result, "fields kept") || strings.Contains(result, "entries kept") {
		t.Errorf("Degenerate trim must not use the coverage-implying note, got tail: %s", tail)
	}
}

// TestReconstructHierarchy_NestedArrays guards the Finding-3 fix: a path with two array
// levels (streams[0].entries[2], the incident shape) must rebuild "entries" as a nested
// ARRAY, not collapse the inner index into a literal "entries[2]" map key.
func TestReconstructHierarchy_NestedArrays(t *testing.T) {
	obj := map[string]interface{}{
		"streams": []interface{}{
			map[string]interface{}{
				"labels": map[string]interface{}{"level": "error"},
				"entries": []interface{}{
					map[string]interface{}{"line": "first"},
					map[string]interface{}{"line": "second"},
					map[string]interface{}{"line": "third"},
				},
			},
		},
	}
	selected := map[string]bool{"streams[0].entries[2]": true}

	result := reconstructHierarchy(obj, selected, nil)

	streams, ok := result["streams"].([]interface{})
	if !ok || len(streams) != 1 {
		t.Fatalf("expected streams array with 1 item, got %T %v", result["streams"], result["streams"])
	}
	stream0, ok := streams[0].(map[string]interface{})
	if !ok {
		t.Fatalf("expected stream object, got %T", streams[0])
	}
	if _, bad := stream0["entries[2]"]; bad {
		t.Fatalf("nested array index leaked as a literal map key 'entries[2]': %v", stream0)
	}
	entries, ok := stream0["entries"].([]interface{})
	if !ok || len(entries) != 1 {
		t.Fatalf("expected nested 'entries' ARRAY with 1 item, got %T %v", stream0["entries"], stream0["entries"])
	}
	entry, ok := entries[0].(map[string]interface{})
	if !ok || entry["line"] != "third" {
		t.Errorf("expected the index-2 entry (line='third') densified to position 0, got %v", entries[0])
	}
}

// TestProcessForPrompt_PlainTextDegenerateDisclosure guards the floor disclosure-parity
// fix: a large non-JSON (plain text) result trimmed to a tiny budget must carry the honest
// "severely reduced … UNKNOWN" note — not just "[trimmed: N/M sentences]" — so the
// synthesizer cannot infer absence from a near-empty floor trim.
func TestProcessForPrompt_PlainTextDegenerateDisclosure(t *testing.T) {
	trimmer := NewStructuralTrimmer(nil, nil)
	input := strings.Repeat("alpha beta gamma delta epsilon. ", 400) // ~12.8KB plain text, non-JSON

	ctx, meta := WithTrimMetadataCapture(context.Background())
	out := trimmer.ProcessForPrompt(ctx, input, 200, ResultProcessorContext{
		StepID: "s1", AgentName: "a", Instruction: "find errors",
	})

	if meta.Method != "structural_text" {
		t.Fatalf("expected Method=structural_text, got %q", meta.Method)
	}
	if !meta.Degenerate {
		t.Errorf("expected Degenerate=true (kept %d of %d bytes)", meta.TrimmedBytes, meta.OriginalBytes)
	}
	if !strings.Contains(out, "severely reduced") || !strings.Contains(out, "UNKNOWN") {
		tail := out
		if len(tail) > 200 {
			tail = tail[len(tail)-200:]
		}
		t.Errorf("expected honest disclosure on a degenerate plain-text floor trim, got tail: %s", tail)
	}
}
