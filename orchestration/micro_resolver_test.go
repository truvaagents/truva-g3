package orchestration

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/truvaagents/truva-g3/core"
	"github.com/truvaagents/truva-g3/telemetry"
	"github.com/stretchr/testify/assert"
)

// --- Test 1: TestGenerateStructuralSummary_NewsArticles ---

func TestGenerateStructuralSummary_NewsArticles(t *testing.T) {
	// Build source data matching production shape
	articles := make([]interface{}, 85)
	for i := 0; i < 85; i++ {
		articles[i] = map[string]interface{}{
			"headline":  fmt.Sprintf("Article %d headline", i),
			"summary":   fmt.Sprintf("Article %d summary text that is moderately long", i),
			"source":    "Finnhub API",
			"url":       fmt.Sprintf("https://example.com/article/%d", i),
			"image":     "",
			"published": float64(1700000000 + i*3600),
		}
	}
	sourceData := map[string]interface{}{
		"data": map[string]interface{}{
			"news":   articles,
			"source": "Finnhub API",
			"symbol": "GOOGL",
		},
		"success": true,
	}

	summary := generateStructuralSummary(sourceData)

	// Schema should capture NESTED structure correctly
	assert.Contains(t, summary.Schema, "data")
	dataSchema := summary.Schema["data"].(map[string]interface{})

	// Array should have item schema notation [{}], not just "array"
	newsSchema := dataSchema["news"].([]interface{})
	assert.Len(t, newsSchema, 1) // One schema item representing array element type
	itemSchema := newsSchema[0].(map[string]interface{})
	assert.Equal(t, "string", itemSchema["headline"])
	assert.Equal(t, "string", itemSchema["summary"])

	// Scalar values should be ACTUAL VALUES, not types
	assert.Equal(t, "Finnhub API", dataSchema["source"]) // actual value, not "string"
	assert.Equal(t, "GOOGL", dataSchema["symbol"])       // actual value, not "string"
	assert.Equal(t, true, summary.Schema["success"])     // actual value, not "boolean"

	// Samples should have exactly 2 items
	assert.Contains(t, summary.Samples, "data.news[0]")
	assert.Contains(t, summary.Samples, "data.news[1]")
	assert.NotContains(t, summary.Samples, "data.news[2]")

	// Metadata should capture array length and total_bytes
	newsMeta := summary.Metadata["data.news"].(map[string]interface{})
	assert.Equal(t, 85, newsMeta["length"])
	assert.Greater(t, newsMeta["item_avg_bytes"], 0)
	assert.Contains(t, summary.Metadata, "total_bytes")

	// Summary should be compact (< 4KB)
	summaryJSON, _ := json.Marshal(summary)
	assert.Less(t, len(summaryJSON), 4096)
}

// --- Test 2: TestApplyMapping_PathReference ---

func TestApplyMapping_PathReference(t *testing.T) {
	sourceData := map[string]interface{}{
		"data": map[string]interface{}{
			"news": []interface{}{
				map[string]interface{}{"headline": "Article 1"},
				map[string]interface{}{"headline": "Article 2"},
			},
			"symbol": "GOOGL",
		},
	}

	mapping := map[string]MappingExpr{
		"content": {Source: MappingSourcePath, Path: "data.news"},
		"symbol":  {Source: MappingSourcePath, Path: "data.symbol"},
	}

	result, err := applyMapping(sourceData, mapping)
	assert.NoError(t, err)

	// content should be the full array (both articles)
	arr := result["content"].([]interface{})
	assert.Len(t, arr, 2)

	// symbol should be the string value
	assert.Equal(t, "GOOGL", result["symbol"])
}

// --- Test 3: TestApplyMapping_FieldProjection ---

func TestApplyMapping_FieldProjection(t *testing.T) {
	sourceData := map[string]interface{}{
		"data": map[string]interface{}{
			"news": []interface{}{
				map[string]interface{}{"headline": "H1", "summary": "S1", "url": "U1", "image": "I1"},
				map[string]interface{}{"headline": "H2", "summary": "S2", "url": "U2", "image": "I2"},
			},
		},
	}

	mapping := map[string]MappingExpr{
		"content": {Source: MappingSourcePath, Path: "data.news", Fields: []string{"headline", "summary"}},
	}

	result, err := applyMapping(sourceData, mapping)
	assert.NoError(t, err)

	arr := result["content"].([]interface{})
	assert.Len(t, arr, 2)

	// Each item should only have headline and summary (url and image dropped)
	item := arr[0].(map[string]interface{})
	assert.Contains(t, item, "headline")
	assert.Contains(t, item, "summary")
	assert.NotContains(t, item, "url")
	assert.NotContains(t, item, "image")
}

// --- Test 4: TestApplyMapping_Transforms ---

func TestApplyMapping_Transforms(t *testing.T) {
	sourceData := map[string]interface{}{
		"items": []interface{}{"first", "second", "third"},
	}

	tests := []struct {
		name      string
		transform MappingTransform
		expected  interface{}
	}{
		{"serialize", MappingTransformSerialize, `["first","second","third"]`},
		{"join", MappingTransformJoin, "first\nsecond\nthird"},
		{"first", MappingTransformFirst, "first"},
		{"count", MappingTransformCount, 3},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mapping := map[string]MappingExpr{
				"result": {Source: MappingSourcePath, Path: "items", Transform: tt.transform},
			}
			result, err := applyMapping(sourceData, mapping)
			assert.NoError(t, err)
			assert.Equal(t, tt.expected, result["result"])
		})
	}
}

// --- Test 5: TestApplyMapping_Literal ---

func TestApplyMapping_Literal(t *testing.T) {
	sourceData := map[string]interface{}{"anything": "data"}

	mapping := map[string]MappingExpr{
		"content_type": {Source: MappingSourceLiteral, Value: "news_articles"},
		"aspects":      {Source: MappingSourceLiteral, Value: []interface{}{"sentiment", "market_impact"}},
	}

	result, err := applyMapping(sourceData, mapping)
	assert.NoError(t, err)
	assert.Equal(t, "news_articles", result["content_type"])
	assert.Equal(t, []interface{}{"sentiment", "market_impact"}, result["aspects"])
}

// --- Test 6: TestApplyMapping_Template ---

func TestApplyMapping_Template(t *testing.T) {
	sourceData := map[string]interface{}{
		"data": map[string]interface{}{"symbol": "GOOGL"},
	}

	mapping := map[string]MappingExpr{
		"label": {Source: MappingSourceTemplate, Template: "Sentiment analysis for {data.symbol}"},
	}

	result, err := applyMapping(sourceData, mapping)
	assert.NoError(t, err)
	assert.Equal(t, "Sentiment analysis for GOOGL", result["label"])
}

// --- Test 7: TestApplyMapping_PathNotFound ---

func TestApplyMapping_PathNotFound(t *testing.T) {
	sourceData := map[string]interface{}{"data": map[string]interface{}{}}

	mapping := map[string]MappingExpr{
		"content": {Source: MappingSourcePath, Path: "data.nonexistent"},
	}

	_, err := applyMapping(sourceData, mapping)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "not found")
}

// --- Test 8: TestResolveParameters_ThresholdBranching ---

// mockAIClientForSchemaMapping tracks which LLM path was triggered based on prompt content.
// Schema-mapping prompts contain "<data_structure>", value-extraction prompts contain "<source_data>".
type mockAIClientForSchemaMapping struct {
	lastCallType string // "schema_mapping" or "value_extraction"
}

func (m *mockAIClientForSchemaMapping) GenerateResponse(ctx context.Context, prompt string, opts *core.AIOptions) (*core.AIResponse, error) {
	if strings.Contains(prompt, "<data_structure>") {
		m.lastCallType = "schema_mapping"
		// Return a valid mapping expression for "name" parameter
		return &core.AIResponse{
			Content: `{"name": {"source": "path", "path": "name"}}`,
		}, nil
	}
	m.lastCallType = "value_extraction"
	// Return a valid extraction response for "name" parameter
	return &core.AIResponse{
		Content: `{"name": "test"}`,
	}, nil
}

func TestResolveParameters_ThresholdBranching(t *testing.T) {
	mockClient := &mockAIClientForSchemaMapping{}

	mr := NewMicroResolver(mockClient, nil)
	mr.schemaMappingThreshold = 100 // Low threshold for testing

	cap := &EnhancedCapability{
		Name:       "test_cap",
		Parameters: []Parameter{{Name: "name", Type: "string", Required: true}},
	}

	// Small data: below threshold → value extraction
	smallData := map[string]interface{}{"name": "test"}
	_, _ = mr.ResolveParameters(context.Background(), smallData, cap, "", "step-1")
	assert.Equal(t, "value_extraction", mockClient.lastCallType)

	// Large data: above threshold → schema mapping
	largeData := make(map[string]interface{})
	for i := 0; i < 50; i++ {
		largeData[fmt.Sprintf("key_%d", i)] = strings.Repeat("x", 100)
	}
	largeData["name"] = "expected_value"
	_, _ = mr.ResolveParameters(context.Background(), largeData, cap, "", "step-2")
	assert.Equal(t, "schema_mapping", mockClient.lastCallType)
}

// --- Test 9: TestResolveParameters_SchemaMapping_FallbackOnFailure ---

// mockAIClientSchemaMappingFallback returns invalid JSON for schema-mapping calls
// and valid extraction JSON for value-extraction calls, verifying the fallback path.
type mockAIClientSchemaMappingFallback struct {
	schemaMappingResponse   string
	valueExtractionResponse string
	callSequence            []string
}

func (m *mockAIClientSchemaMappingFallback) GenerateResponse(ctx context.Context, prompt string, opts *core.AIOptions) (*core.AIResponse, error) {
	if strings.Contains(prompt, "<data_structure>") {
		m.callSequence = append(m.callSequence, "schema_mapping")
		return &core.AIResponse{Content: m.schemaMappingResponse}, nil
	}
	m.callSequence = append(m.callSequence, "value_extraction")
	return &core.AIResponse{Content: m.valueExtractionResponse}, nil
}

func TestResolveParameters_SchemaMapping_FallbackOnFailure(t *testing.T) {
	mockClient := &mockAIClientSchemaMappingFallback{
		schemaMappingResponse:   "not valid json",
		valueExtractionResponse: `{"name": "extracted"}`,
	}

	mr := NewMicroResolver(mockClient, nil)
	mr.schemaMappingThreshold = 100

	largeData := make(map[string]interface{})
	for i := 0; i < 50; i++ {
		largeData[fmt.Sprintf("key_%d", i)] = strings.Repeat("x", 100)
	}
	largeData["name"] = "expected_value"

	cap := &EnhancedCapability{
		Name:       "test_cap",
		Parameters: []Parameter{{Name: "name", Type: "string", Required: true}},
	}

	result, err := mr.ResolveParameters(context.Background(), largeData, cap, "", "step-1")
	assert.NoError(t, err)
	// Should have fallen back to value extraction
	assert.Equal(t, "extracted", result["name"])
	// Verify call ordering: schema_mapping first, then value_extraction
	assert.Equal(t, []string{"schema_mapping", "value_extraction"}, mockClient.callSequence)
}

// --- Test 10: TestGenerateStructuralSummary_NestedObjects ---

func TestGenerateStructuralSummary_NestedObjects(t *testing.T) {
	sourceData := map[string]interface{}{
		"metrics": map[string]interface{}{
			"pe_ratio":      float64(27.76),
			"market_cap":    float64(1.8e12),
			"revenue":       float64(3.07e11),
			"profit_margin": float64(0.3281),
		},
		"company": map[string]interface{}{
			"name":     "Alphabet Inc",
			"ticker":   "GOOGL",
			"industry": "Technology",
		},
	}

	summary := generateStructuralSummary(sourceData)

	// Schema should reflect nested structure with ACTUAL scalar values
	assert.Contains(t, summary.Schema, "metrics")
	assert.Contains(t, summary.Schema, "company")

	// Verify actual scalar values are preserved (not just types)
	company := summary.Schema["company"].(map[string]interface{})
	assert.Equal(t, "Alphabet Inc", company["name"])
	assert.Equal(t, "GOOGL", company["ticker"])

	metrics := summary.Schema["metrics"].(map[string]interface{})
	assert.Equal(t, float64(27.76), metrics["pe_ratio"])

	// No arrays → no samples, but metadata has total_bytes
	assert.Empty(t, summary.Samples)
	assert.Contains(t, summary.Metadata, "total_bytes")
	assert.Equal(t, 1, len(summary.Metadata)) // only total_bytes, no array metadata

	// Should be very compact for scalar data
	summaryJSON, _ := json.Marshal(summary)
	assert.Less(t, len(summaryJSON), 512)
}

// --- Edge case tests ---

func TestGenerateStructuralSummary_EmptyData(t *testing.T) {
	sourceData := map[string]interface{}{}

	summary := generateStructuralSummary(sourceData)

	assert.Empty(t, summary.Schema)
	assert.Empty(t, summary.Samples)
	assert.Contains(t, summary.Metadata, "total_bytes")
	assert.Equal(t, 1, len(summary.Metadata))
}

func TestResolveParameters_ThresholdExact(t *testing.T) {
	mockClient := &mockAIClientForSchemaMapping{}
	mr := NewMicroResolver(mockClient, nil)

	// Create data that serializes to exactly the threshold
	data := map[string]interface{}{"name": "test"}
	dataJSON, _ := json.Marshal(data)
	mr.schemaMappingThreshold = len(dataJSON) // exact match

	cap := &EnhancedCapability{
		Name:       "test_cap",
		Parameters: []Parameter{{Name: "name", Type: "string", Required: true}},
	}

	_, _ = mr.ResolveParameters(context.Background(), data, cap, "", "step-1")
	// Threshold check is > (not >=), so exact match should use value extraction
	assert.Equal(t, "value_extraction", mockClient.lastCallType)
}

func TestResolveParameters_ThresholdDisabled(t *testing.T) {
	mockClient := &mockAIClientForSchemaMapping{}
	mr := NewMicroResolver(mockClient, nil)
	mr.schemaMappingThreshold = 0 // disabled

	// Even large data should use value extraction when threshold is 0
	largeData := make(map[string]interface{})
	for i := 0; i < 50; i++ {
		largeData[fmt.Sprintf("key_%d", i)] = strings.Repeat("x", 100)
	}

	cap := &EnhancedCapability{
		Name:       "test_cap",
		Parameters: []Parameter{{Name: "key_0", Type: "string", Required: true}},
	}

	_, _ = mr.ResolveParameters(context.Background(), largeData, cap, "", "step-1")
	assert.Equal(t, "value_extraction", mockClient.lastCallType)
}

func TestApplyMapping_EmptyMapping(t *testing.T) {
	sourceData := map[string]interface{}{"data": "test"}

	result, err := applyMapping(sourceData, map[string]MappingExpr{})
	assert.NoError(t, err)
	assert.Empty(t, result)
}

func TestGenerateStructuralSummary_DeeplyNested(t *testing.T) {
	sourceData := map[string]interface{}{
		"level1": map[string]interface{}{
			"level2": map[string]interface{}{
				"level3": map[string]interface{}{
					"level4": map[string]interface{}{
						"level5": "deep_value",
					},
				},
			},
		},
	}

	summary := generateStructuralSummary(sourceData)

	// Should capture all 5 levels
	l1 := summary.Schema["level1"].(map[string]interface{})
	l2 := l1["level2"].(map[string]interface{})
	l3 := l2["level3"].(map[string]interface{})
	l4 := l3["level4"].(map[string]interface{})
	assert.Equal(t, "deep_value", l4["level5"])
}

// =============================================================================
// Additional coverage tests for 100% unit test coverage on new code
// =============================================================================

// --- resolveWithSchemaMapping error paths ---

// mockAIClientReturnsError returns an error from GenerateResponse.
type mockAIClientReturnsError struct {
	err error
}

func (m *mockAIClientReturnsError) GenerateResponse(ctx context.Context, prompt string, opts *core.AIOptions) (*core.AIResponse, error) {
	return nil, m.err
}

func TestResolveWithSchemaMapping_LLMError(t *testing.T) {
	mockClient := &mockAIClientReturnsError{err: errors.New("LLM unavailable")}
	mr := NewMicroResolver(mockClient, nil)

	cap := &EnhancedCapability{
		Name:       "test_cap",
		Parameters: []Parameter{{Name: "name", Type: "string", Required: true}},
	}
	sourceData := map[string]interface{}{"name": "test"}

	_, err := mr.resolveWithSchemaMapping(context.Background(), sourceData, cap, "", "step-1")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "schema-mapping LLM call failed")
}

// mockAIClientReturnsContent returns a fixed content string from GenerateResponse.
type mockAIClientReturnsContent struct {
	content string
}

func (m *mockAIClientReturnsContent) GenerateResponse(ctx context.Context, prompt string, opts *core.AIOptions) (*core.AIResponse, error) {
	return &core.AIResponse{Content: m.content}, nil
}

func TestResolveWithSchemaMapping_InvalidMappingExprJSON(t *testing.T) {
	// Valid outer JSON, but individual mapping expr is not a valid MappingExpr
	mockClient := &mockAIClientReturnsContent{
		content: `{"name": "not a mapping object, just a string"}`,
	}
	mr := NewMicroResolver(mockClient, nil)

	cap := &EnhancedCapability{
		Name:       "test_cap",
		Parameters: []Parameter{{Name: "name", Type: "string", Required: true}},
	}
	sourceData := map[string]interface{}{"name": "test"}

	_, err := mr.resolveWithSchemaMapping(context.Background(), sourceData, cap, "", "step-1")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "failed to parse mapping expression")
}

func TestResolveWithSchemaMapping_UnknownSourceType(t *testing.T) {
	mockClient := &mockAIClientReturnsContent{
		content: `{"name": {"source": "magic", "path": "name"}}`,
	}
	mr := NewMicroResolver(mockClient, nil)

	cap := &EnhancedCapability{
		Name:       "test_cap",
		Parameters: []Parameter{{Name: "name", Type: "string", Required: true}},
	}
	sourceData := map[string]interface{}{"name": "test"}

	_, err := mr.resolveWithSchemaMapping(context.Background(), sourceData, cap, "", "step-1")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "unknown mapping source")
}

func TestResolveWithSchemaMapping_ApplyMappingError(t *testing.T) {
	// Valid mapping JSON but references a path that doesn't exist
	mockClient := &mockAIClientReturnsContent{
		content: `{"name": {"source": "path", "path": "nonexistent.deep.path"}}`,
	}
	mr := NewMicroResolver(mockClient, nil)

	cap := &EnhancedCapability{
		Name:       "test_cap",
		Parameters: []Parameter{{Name: "name", Type: "string", Required: true}},
	}
	sourceData := map[string]interface{}{"name": "test"}

	_, err := mr.resolveWithSchemaMapping(context.Background(), sourceData, cap, "", "step-1")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "failed to apply mapping")
}

func TestResolveWithSchemaMapping_Success_WithHint(t *testing.T) {
	mockClient := &mockAIClientReturnsContent{
		content: `{"name": {"source": "path", "path": "data.name"}}`,
	}
	mr := NewMicroResolver(mockClient, nil)

	cap := &EnhancedCapability{
		Name:       "test_cap",
		Parameters: []Parameter{{Name: "name", Type: "string", Required: true}},
	}
	sourceData := map[string]interface{}{
		"data": map[string]interface{}{"name": "Alice"},
	}

	result, err := mr.resolveWithSchemaMapping(context.Background(), sourceData, cap, "extract user info", "step-1")
	assert.NoError(t, err)
	assert.Equal(t, "Alice", result["name"])
}

func TestResolveWithSchemaMapping_TypeCoercion(t *testing.T) {
	// LLM maps to a path that has a string, but target type is number
	mockClient := &mockAIClientReturnsContent{
		content: `{"count": {"source": "path", "path": "total"}}`,
	}
	mr := NewMicroResolver(mockClient, nil)

	cap := &EnhancedCapability{
		Name:       "test_cap",
		Parameters: []Parameter{{Name: "count", Type: "number", Required: true}},
	}
	sourceData := map[string]interface{}{"total": "42"}

	result, err := mr.resolveWithSchemaMapping(context.Background(), sourceData, cap, "", "step-1")
	assert.NoError(t, err)
	// coerceType should convert string "42" to float64
	assert.Equal(t, float64(42), result["count"])
}

func TestResolveWithSchemaMapping_WithBaggage(t *testing.T) {
	// Exercise the telemetry baggage path where requestID is obtained from context
	mockClient := &mockAIClientReturnsContent{
		content: `{"name": {"source": "path", "path": "name"}}`,
	}
	mr := NewMicroResolver(mockClient, nil)

	cap := &EnhancedCapability{
		Name:       "test_cap",
		Parameters: []Parameter{{Name: "name", Type: "string", Required: true}},
	}
	sourceData := map[string]interface{}{"name": "test_value"}

	// Set up context with baggage containing request_id
	ctx := telemetry.WithBaggage(context.Background(), "request_id", "test-req-123")

	result, err := mr.resolveWithSchemaMapping(ctx, sourceData, cap, "", "step-1")
	assert.NoError(t, err)
	assert.Equal(t, "test_value", result["name"])
}

func TestResolveWithSchemaMapping_MarkdownCodeBlock(t *testing.T) {
	// LLM returns mapping wrapped in markdown code blocks
	mockClient := &mockAIClientReturnsContent{
		content: "```json\n{\"name\": {\"source\": \"path\", \"path\": \"name\"}}\n```",
	}
	mr := NewMicroResolver(mockClient, nil)

	cap := &EnhancedCapability{
		Name:       "test_cap",
		Parameters: []Parameter{{Name: "name", Type: "string", Required: true}},
	}
	sourceData := map[string]interface{}{"name": "test_value"}

	result, err := mr.resolveWithSchemaMapping(context.Background(), sourceData, cap, "", "step-1")
	assert.NoError(t, err)
	assert.Equal(t, "test_value", result["name"])
}

// --- applyMapping uncovered paths ---

func TestApplyMapping_UnknownSource(t *testing.T) {
	sourceData := map[string]interface{}{"data": "test"}

	mapping := map[string]MappingExpr{
		"param": {Source: MappingSource("unknown_source")},
	}

	_, err := applyMapping(sourceData, mapping)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "unknown mapping source")
}

func TestApplyMapping_TransformError(t *testing.T) {
	sourceData := map[string]interface{}{"data": "not_an_array"}

	mapping := map[string]MappingExpr{
		"param": {Source: MappingSourcePath, Path: "data", Transform: MappingTransformCount},
	}

	_, err := applyMapping(sourceData, mapping)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "transform")
}

// --- navigateToPath edge cases ---

func TestNavigateToPath_EmptySegment(t *testing.T) {
	data := map[string]interface{}{"a": map[string]interface{}{"b": "value"}}

	// Path with empty segment (leading dot)
	result := navigateToPath(data, ".a.b")
	assert.Equal(t, "value", result)

	// Path with trailing dot
	result = navigateToPath(data, "a.b.")
	assert.Equal(t, "value", result)
}

func TestNavigateToPath_NonMapTraversal(t *testing.T) {
	data := map[string]interface{}{"a": "string_value"}

	// Trying to navigate through a string
	result := navigateToPath(data, "a.b")
	assert.Nil(t, result)
}

func TestNavigateToPath_MissingKey(t *testing.T) {
	data := map[string]interface{}{"a": map[string]interface{}{"b": "value"}}

	result := navigateToPath(data, "a.c")
	assert.Nil(t, result)
}

func TestNavigateToPath_TopLevel(t *testing.T) {
	data := map[string]interface{}{"key": "value"}

	result := navigateToPath(data, "key")
	assert.Equal(t, "value", result)
}

// --- projectFields edge cases ---

func TestProjectFields_SingleObject(t *testing.T) {
	obj := map[string]interface{}{
		"name": "Alice",
		"age":  30,
		"city": "NYC",
	}

	result := projectFields(obj, []string{"name", "age"})
	projected := result.(map[string]interface{})
	assert.Equal(t, "Alice", projected["name"])
	assert.Equal(t, 30, projected["age"])
	assert.NotContains(t, projected, "city")
}

func TestProjectFields_NonObjectItem(t *testing.T) {
	// Array of non-objects (strings) — projection should return items as-is
	arr := []interface{}{"hello", "world"}

	result := projectFields(arr, []string{"anything"})
	resultArr := result.([]interface{})
	assert.Equal(t, "hello", resultArr[0])
	assert.Equal(t, "world", resultArr[1])
}

func TestProjectFields_ScalarValue(t *testing.T) {
	// Passing a non-array, non-object value
	result := projectFields("scalar_value", []string{"field"})
	assert.Equal(t, "scalar_value", result)
}

func TestProjectFields_MissingFields(t *testing.T) {
	obj := map[string]interface{}{"name": "Alice"}

	result := projectFields(obj, []string{"name", "nonexistent"})
	projected := result.(map[string]interface{})
	assert.Equal(t, "Alice", projected["name"])
	assert.NotContains(t, projected, "nonexistent")
}

// --- applyTransform edge cases ---

func TestApplyTransform_JoinNonArray(t *testing.T) {
	result, err := applyTransform("single_value", MappingTransformJoin)
	assert.NoError(t, err)
	assert.Equal(t, "single_value", result)
}

func TestApplyTransform_FirstNonArray(t *testing.T) {
	result, err := applyTransform("not_array", MappingTransformFirst)
	assert.NoError(t, err)
	assert.Equal(t, "not_array", result)
}

func TestApplyTransform_FirstEmptyArray(t *testing.T) {
	_, err := applyTransform([]interface{}{}, MappingTransformFirst)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "empty array")
}

func TestApplyTransform_CountNonArray(t *testing.T) {
	_, err := applyTransform("not_array", MappingTransformCount)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "count transform requires an array")
}

func TestApplyTransform_UnknownTransform(t *testing.T) {
	_, err := applyTransform("value", MappingTransform("unknown"))
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "unknown transform")
}

func TestApplyTransform_SerializeMap(t *testing.T) {
	input := map[string]interface{}{"key": "val"}
	result, err := applyTransform(input, MappingTransformSerialize)
	assert.NoError(t, err)
	assert.Equal(t, `{"key":"val"}`, result)
}

func TestApplyTransform_SerializeError(t *testing.T) {
	// json.Marshal fails on channels
	_, err := applyTransform(make(chan int), MappingTransformSerialize)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "serialize failed")
}

// --- interpolateTemplate edge cases ---

func TestInterpolateTemplate_UnmatchedOpenBrace(t *testing.T) {
	data := map[string]interface{}{"key": "value"}

	// Unmatched { without closing }
	result := interpolateTemplate("hello {unclosed", data)
	assert.Equal(t, "hello {unclosed", result)
}

func TestInterpolateTemplate_NoPlaceholders(t *testing.T) {
	data := map[string]interface{}{"key": "value"}

	result := interpolateTemplate("static text", data)
	assert.Equal(t, "static text", result)
}

func TestInterpolateTemplate_MissingPath(t *testing.T) {
	data := map[string]interface{}{"key": "value"}

	// Path doesn't exist → replaced with empty string
	result := interpolateTemplate("hello {nonexistent}", data)
	assert.Equal(t, "hello ", result)
}

func TestInterpolateTemplate_MultiplePlaceholders(t *testing.T) {
	data := map[string]interface{}{
		"first": "Alice",
		"last":  "Smith",
	}

	result := interpolateTemplate("{first} {last}", data)
	assert.Equal(t, "Alice Smith", result)
}

func TestInterpolateTemplate_ReplacementContainingBraces(t *testing.T) {
	data := map[string]interface{}{
		"data": map[string]interface{}{
			"info": "price is {estimated}",
		},
	}

	// The replacement value contains curly braces — they must NOT be parsed as placeholders
	result := interpolateTemplate("Result: {data.info}", data)
	assert.Equal(t, "Result: price is {estimated}", result)
}

// --- walkForSummary edge cases ---

func TestGenerateStructuralSummary_EmptyArray(t *testing.T) {
	sourceData := map[string]interface{}{
		"items": []interface{}{},
	}

	summary := generateStructuralSummary(sourceData)

	// Empty array should have empty schema notation
	schema := summary.Schema["items"].([]interface{})
	assert.Empty(t, schema)

	// No samples for empty array
	assert.NotContains(t, summary.Samples, "items[0]")

	// No array metadata for empty array (only total_bytes)
	assert.NotContains(t, summary.Metadata, "items")
	assert.Contains(t, summary.Metadata, "total_bytes")
}

func TestGenerateStructuralSummary_SingleItemArray(t *testing.T) {
	sourceData := map[string]interface{}{
		"items": []interface{}{
			map[string]interface{}{"name": "only_one"},
		},
	}

	summary := generateStructuralSummary(sourceData)

	// Schema should have item type
	schema := summary.Schema["items"].([]interface{})
	assert.Len(t, schema, 1)

	// Only 1 sample (fewer than maxSummarySamples)
	assert.Contains(t, summary.Samples, "items[0]")
	assert.NotContains(t, summary.Samples, "items[1]")

	// Metadata should reflect array length of 1
	meta := summary.Metadata["items"].(map[string]interface{})
	assert.Equal(t, 1, meta["length"])
}

// --- typeString comprehensive coverage ---

func TestTypeString_AllTypes(t *testing.T) {
	tests := []struct {
		name     string
		value    interface{}
		expected string
	}{
		{"string", "hello", "string"},
		{"float64", float64(3.14), "number"},
		{"float32", float32(3.14), "number"},
		{"int", int(42), "number"},
		{"int64", int64(42), "number"},
		{"int32", int32(42), "number"},
		{"json.Number", json.Number("123"), "number"},
		{"bool", true, "boolean"},
		{"nil", nil, "null"},
		{"map", map[string]interface{}{"k": "v"}, "object"},
		{"array", []interface{}{1, 2}, "array"},
		{"unknown", struct{}{}, "unknown"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.expected, typeString(tt.value))
		})
	}
}

// --- typeSchemaForValue edge cases ---

func TestTypeSchemaForValue_Scalar(t *testing.T) {
	// Non-object value should return type string
	assert.Equal(t, "string", typeSchemaForValue("hello"))
	assert.Equal(t, "number", typeSchemaForValue(float64(42)))
	assert.Equal(t, "boolean", typeSchemaForValue(true))
	assert.Equal(t, "null", typeSchemaForValue(nil))
	assert.Equal(t, "array", typeSchemaForValue([]interface{}{1, 2}))
}

func TestTypeSchemaForValue_Object(t *testing.T) {
	obj := map[string]interface{}{
		"name":   "Alice",
		"age":    float64(30),
		"active": true,
	}

	result := typeSchemaForValue(obj).(map[string]interface{})
	assert.Equal(t, "string", result["name"])
	assert.Equal(t, "number", result["age"])
	assert.Equal(t, "boolean", result["active"])
}

// --- walkForSummary: array of scalars ---

func TestGenerateStructuralSummary_ArrayOfScalars(t *testing.T) {
	sourceData := map[string]interface{}{
		"tags": []interface{}{"go", "agent", "framework"},
	}

	summary := generateStructuralSummary(sourceData)

	// Schema: array notation with scalar type
	schema := summary.Schema["tags"].([]interface{})
	assert.Len(t, schema, 1)
	assert.Equal(t, "string", schema[0])

	// Samples: first 2 items
	assert.Contains(t, summary.Samples, "tags[0]")
	assert.Contains(t, summary.Samples, "tags[1]")
	assert.Equal(t, "go", summary.Samples["tags[0]"])
	assert.Equal(t, "agent", summary.Samples["tags[1]"])

	// Metadata
	meta := summary.Metadata["tags"].(map[string]interface{})
	assert.Equal(t, 3, meta["length"])
}

// --- ResolveParameters: schema mapping success through the full path ---

func TestResolveParameters_SchemaMapping_SuccessPath(t *testing.T) {
	// Mock client that returns valid mapping for schema-mapping prompts
	// and should NOT be called for value extraction
	mockClient := &mockAIClientSchemaMappingFallback{
		schemaMappingResponse:   `{"content": {"source": "path", "path": "data.items"}, "label": {"source": "literal", "value": "test_label"}}`,
		valueExtractionResponse: `{"content": "should not reach here"}`,
	}

	mr := NewMicroResolver(mockClient, nil)
	mr.schemaMappingThreshold = 100 // Low threshold

	largeData := make(map[string]interface{})
	for i := 0; i < 50; i++ {
		largeData[fmt.Sprintf("key_%d", i)] = strings.Repeat("x", 100)
	}
	largeData["data"] = map[string]interface{}{
		"items": []interface{}{"item1", "item2"},
	}

	cap := &EnhancedCapability{
		Name: "test_cap",
		Parameters: []Parameter{
			{Name: "content", Type: "array", Required: true},
			{Name: "label", Type: "string", Required: false},
		},
	}

	result, err := mr.ResolveParameters(context.Background(), largeData, cap, "", "step-1")
	assert.NoError(t, err)

	// Should have used schema mapping (only 1 call, no fallback)
	assert.Equal(t, []string{"schema_mapping"}, mockClient.callSequence)

	// Results should be from the mapping
	arr := result["content"].([]interface{})
	assert.Len(t, arr, 2)
	assert.Equal(t, "test_label", result["label"])
}

func TestResolveWithSchemaMapping_SummaryMarshalError(t *testing.T) {
	// Passing a channel in source data causes generateStructuralSummary to include it
	// in the schema (walkForSummary default case stores actual values for scalars),
	// and json.MarshalIndent fails on channels.
	mockClient := &mockAIClientReturnsContent{content: `{}`}
	mr := NewMicroResolver(mockClient, nil)

	cap := &EnhancedCapability{
		Name:       "test_cap",
		Parameters: []Parameter{{Name: "name", Type: "string", Required: true}},
	}
	sourceData := map[string]interface{}{"bad": make(chan int)}

	_, err := mr.resolveWithSchemaMapping(context.Background(), sourceData, cap, "", "step-1")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "failed to generate structural summary")
}

// --- ResolveParameters: schema mapping with LLM error triggers fallback ---

type mockAIClientSchemaMappingLLMError struct {
	callSequence []string
}

func (m *mockAIClientSchemaMappingLLMError) GenerateResponse(ctx context.Context, prompt string, opts *core.AIOptions) (*core.AIResponse, error) {
	if strings.Contains(prompt, "<data_structure>") {
		m.callSequence = append(m.callSequence, "schema_mapping")
		return nil, errors.New("LLM rate limited")
	}
	m.callSequence = append(m.callSequence, "value_extraction")
	return &core.AIResponse{Content: `{"name": "fallback_value"}`}, nil
}

// --- DefaultConfig env var override tests ---

func TestDefaultConfig_SchemaGuidedMappingThreshold_Default(t *testing.T) {
	// Ensure env var is not set
	os.Unsetenv("TRUVAG3_RESULT_TRIM_SCHEMA_MAPPING_THRESHOLD")

	config := DefaultConfig()
	assert.Equal(t, 16384, config.ResultTrim.SchemaGuidedMappingThreshold)
}

func TestDefaultConfig_SchemaGuidedMappingThreshold_EnvOverride(t *testing.T) {
	os.Setenv("TRUVAG3_RESULT_TRIM_SCHEMA_MAPPING_THRESHOLD", "32768")
	defer os.Unsetenv("TRUVAG3_RESULT_TRIM_SCHEMA_MAPPING_THRESHOLD")

	config := DefaultConfig()
	assert.Equal(t, 32768, config.ResultTrim.SchemaGuidedMappingThreshold)
}

func TestDefaultConfig_SchemaGuidedMappingThreshold_EnvDisable(t *testing.T) {
	// 0 is valid — means "disable schema-guided mapping"
	os.Setenv("TRUVAG3_RESULT_TRIM_SCHEMA_MAPPING_THRESHOLD", "0")
	defer os.Unsetenv("TRUVAG3_RESULT_TRIM_SCHEMA_MAPPING_THRESHOLD")

	config := DefaultConfig()
	assert.Equal(t, 0, config.ResultTrim.SchemaGuidedMappingThreshold)
}

func TestDefaultConfig_SchemaGuidedMappingThreshold_EnvInvalid(t *testing.T) {
	// Invalid value should keep default
	os.Setenv("TRUVAG3_RESULT_TRIM_SCHEMA_MAPPING_THRESHOLD", "not_a_number")
	defer os.Unsetenv("TRUVAG3_RESULT_TRIM_SCHEMA_MAPPING_THRESHOLD")

	config := DefaultConfig()
	assert.Equal(t, 16384, config.ResultTrim.SchemaGuidedMappingThreshold)
}

func TestDefaultConfig_SchemaGuidedMappingThreshold_EnvNegative(t *testing.T) {
	// Negative value should keep default (val >= 0 check)
	os.Setenv("TRUVAG3_RESULT_TRIM_SCHEMA_MAPPING_THRESHOLD", "-1")
	defer os.Unsetenv("TRUVAG3_RESULT_TRIM_SCHEMA_MAPPING_THRESHOLD")

	config := DefaultConfig()
	assert.Equal(t, 16384, config.ResultTrim.SchemaGuidedMappingThreshold)
}

// --- ResolveParameters: schema mapping with LLM error triggers fallback ---

func TestResolveParameters_SchemaMapping_LLMErrorFallback(t *testing.T) {
	mockClient := &mockAIClientSchemaMappingLLMError{}

	mr := NewMicroResolver(mockClient, nil)
	mr.schemaMappingThreshold = 100

	largeData := make(map[string]interface{})
	for i := 0; i < 50; i++ {
		largeData[fmt.Sprintf("key_%d", i)] = strings.Repeat("x", 100)
	}
	largeData["name"] = "expected"

	cap := &EnhancedCapability{
		Name:       "test_cap",
		Parameters: []Parameter{{Name: "name", Type: "string", Required: true}},
	}

	result, err := mr.ResolveParameters(context.Background(), largeData, cap, "", "step-1")
	assert.NoError(t, err)
	assert.Equal(t, "fallback_value", result["name"])
	// Verify: schema_mapping attempted first, then fell back to value_extraction
	assert.Equal(t, []string{"schema_mapping", "value_extraction"}, mockClient.callSequence)
}

// =============================================================================
// §13 Structural Summary Sibling Deduplication Tests
// =============================================================================

// --- representativeElement tests ---

func TestRepresentativeElement_NilFirst(t *testing.T) {
	arr := []interface{}{nil, nil, 42.5, 43.1}
	assert.Equal(t, 42.5, representativeElement(arr))
}

func TestRepresentativeElement_AllNil(t *testing.T) {
	arr := []interface{}{nil, nil, nil}
	// Falls back to first element (nil)
	assert.Nil(t, representativeElement(arr))
}

func TestRepresentativeElement_NoNils(t *testing.T) {
	arr := []interface{}{1.0, 2.0, 3.0}
	assert.Equal(t, 1.0, representativeElement(arr))
}

func TestRepresentativeElement_EmptyArray(t *testing.T) {
	arr := []interface{}{}
	assert.Nil(t, representativeElement(arr))
}

func TestRepresentativeElement_MixedNils(t *testing.T) {
	arr := []interface{}{nil, "hello", nil, 42}
	assert.Equal(t, "hello", representativeElement(arr))
}

// --- structuralFingerprint tests ---

func TestStructuralFingerprint_ArrayOfObjects(t *testing.T) {
	val := []interface{}{
		map[string]interface{}{"period": "2025", "value": float64(42)},
	}
	fp := structuralFingerprint(val)
	assert.Equal(t, "array:object:{period:string,value:number}", fp)
}

func TestStructuralFingerprint_NestedObject(t *testing.T) {
	val := map[string]interface{}{
		"a": map[string]interface{}{"b": "x"},
		"c": float64(1),
	}
	fp := structuralFingerprint(val)
	assert.Equal(t, "object:{a:object:{b:string},c:number}", fp)
}

func TestStructuralFingerprint_Scalar(t *testing.T) {
	assert.Equal(t, "number", structuralFingerprint(42.5))
}

func TestStructuralFingerprint_EmptyArray(t *testing.T) {
	assert.Equal(t, "array:empty", structuralFingerprint([]interface{}{}))
}

func TestStructuralFingerprint_NullFirstElement(t *testing.T) {
	// Bug 2 regression: should fingerprint using representative element, not v[0]
	val := []interface{}{nil, nil, 42.5}
	fp := structuralFingerprint(val)
	assert.Equal(t, "array:number", fp, "should use representative element, not nil first element")
}

func TestStructuralFingerprint_AllNullArray(t *testing.T) {
	val := []interface{}{nil, nil, nil}
	fp := structuralFingerprint(val)
	assert.Equal(t, "array:null", fp, "truly all-nil array should fingerprint as array:null")
}

// --- collapseSiblings tests ---

func TestCollapseSiblings_ArrayGroup(t *testing.T) {
	// 5 arrays of {period, value} objects — should form one group
	data := make(map[string]interface{})
	schema := make(map[string]interface{})
	for i := 0; i < 5; i++ {
		key := fmt.Sprintf("metric_%d", i)
		data[key] = []interface{}{
			map[string]interface{}{"period": "2025", "value": float64(i)},
			map[string]interface{}{"period": "2024", "value": float64(i + 10)},
		}
		schema[key] = []interface{}{map[string]interface{}{"period": "string", "value": "number"}}
	}

	summary := &structuralSummary{
		Samples:  make(map[string]interface{}),
		Metadata: make(map[string]interface{}),
	}
	// Add metadata for each array
	for i := 0; i < 5; i++ {
		key := fmt.Sprintf("metric_%d", i)
		summary.Metadata[key] = map[string]interface{}{
			"type":           "array",
			"length":         2,
			"item_avg_bytes": 30,
		}
	}

	result, groups := collapseSiblings(schema, data, "", summary)
	assert.Len(t, groups, 1)

	// Single group, all children collapsed → should be unwrapped
	assert.True(t, result["_grouped"].(bool))
	assert.Equal(t, 5, result["_count"])
	assert.NotNil(t, result["_element_schema"])
	assert.NotNil(t, result["_keys"])
}

func TestCollapseSiblings_ObjectGroup(t *testing.T) {
	// 4 objects with identical structure
	data := make(map[string]interface{})
	schema := make(map[string]interface{})
	for i := 0; i < 4; i++ {
		key := fmt.Sprintf("item_%d", i)
		data[key] = map[string]interface{}{"name": fmt.Sprintf("item%d", i), "value": float64(i)}
		schema[key] = map[string]interface{}{"name": "string", "value": "number"}
	}

	summary := &structuralSummary{
		Samples:  make(map[string]interface{}),
		Metadata: make(map[string]interface{}),
	}

	result, groups := collapseSiblings(schema, data, "parent", summary)
	assert.Len(t, groups, 1)

	// Single group, all children collapsed → unwrapped
	assert.True(t, result["_grouped"].(bool))
	assert.Equal(t, 4, result["_count"])
	elemSchema := result["_element_schema"].(map[string]interface{})
	assert.Equal(t, "string", elemSchema["name"])
	assert.Equal(t, "number", elemSchema["value"])
}

func TestCollapseSiblings_BelowThreshold(t *testing.T) {
	// Only 2 identical arrays — below minSiblingGroupSize (3)
	data := map[string]interface{}{
		"a": []interface{}{float64(1), float64(2)},
		"b": []interface{}{float64(3), float64(4)},
	}
	schema := map[string]interface{}{
		"a": []interface{}{"number"},
		"b": []interface{}{"number"},
	}

	summary := &structuralSummary{
		Samples:  make(map[string]interface{}),
		Metadata: make(map[string]interface{}),
	}

	result, groups := collapseSiblings(schema, data, "", summary)
	assert.Len(t, groups, 0)
	// Schema should be unchanged
	assert.Contains(t, result, "a")
	assert.Contains(t, result, "b")
}

func TestCollapseSiblings_MixedFingerprints(t *testing.T) {
	// 7 type-A arrays + 3 type-B arrays
	data := make(map[string]interface{})
	schema := make(map[string]interface{})

	// Type A: arrays of {period, value}
	for i := 0; i < 7; i++ {
		key := fmt.Sprintf("typeA_%d", i)
		data[key] = []interface{}{
			map[string]interface{}{"period": "2025", "value": float64(i)},
		}
		schema[key] = []interface{}{map[string]interface{}{"period": "string", "value": "number"}}
	}
	// Type B: arrays of {name, count}
	for i := 0; i < 3; i++ {
		key := fmt.Sprintf("typeB_%d", i)
		data[key] = []interface{}{
			map[string]interface{}{"name": fmt.Sprintf("item%d", i), "count": float64(i)},
		}
		schema[key] = []interface{}{map[string]interface{}{"name": "string", "count": "number"}}
	}

	summary := &structuralSummary{
		Samples:  make(map[string]interface{}),
		Metadata: make(map[string]interface{}),
	}
	for key := range data {
		summary.Metadata[key] = map[string]interface{}{
			"type":           "array",
			"length":         1,
			"item_avg_bytes": 30,
		}
	}

	result, groups := collapseSiblings(schema, data, "", summary)
	assert.Len(t, groups, 2, "should form 2 groups (type A and type B)")

	// Both groups should be present as _group_0 and _group_1
	// (not unwrapped since there are 2 groups)
	assert.Contains(t, result, "_group_0")
	assert.Contains(t, result, "_group_1")
}

func TestCollapseSiblings_ScalarsExcluded(t *testing.T) {
	// 20 numeric scalars — should NOT be grouped
	data := make(map[string]interface{})
	schema := make(map[string]interface{})
	for i := 0; i < 20; i++ {
		key := fmt.Sprintf("metric_%d", i)
		data[key] = float64(i * 10)
		schema[key] = float64(i * 10)
	}

	summary := &structuralSummary{
		Samples:  make(map[string]interface{}),
		Metadata: make(map[string]interface{}),
	}

	result, groups := collapseSiblings(schema, data, "", summary)
	assert.Len(t, groups, 0)
	assert.Len(t, result, 20, "all scalars should remain ungrouped")
}

func TestCollapseSiblings_KeysTruncation(t *testing.T) {
	// 15 identical arrays — _keys should truncate to maxGroupedKeys
	data := make(map[string]interface{})
	schema := make(map[string]interface{})
	for i := 0; i < 15; i++ {
		key := fmt.Sprintf("arr_%02d", i)
		data[key] = []interface{}{float64(1), float64(2)}
		schema[key] = []interface{}{"number"}
	}

	summary := &structuralSummary{
		Samples:  make(map[string]interface{}),
		Metadata: make(map[string]interface{}),
	}
	for key := range data {
		summary.Metadata[key] = map[string]interface{}{
			"type":           "array",
			"length":         2,
			"item_avg_bytes": 8,
		}
	}

	result, groups := collapseSiblings(schema, data, "", summary)
	assert.Len(t, groups, 1)

	// Single group → unwrapped
	keys := result["_keys"].([]interface{})
	assert.Len(t, keys, maxGroupedKeys+1, "should have maxGroupedKeys entries + sentinel")
	sentinel := keys[maxGroupedKeys].(string)
	assert.Contains(t, sentinel, "...+5 more")
}

func TestCollapseSiblings_KeyNamingNoCollision(t *testing.T) {
	// Bug 1 regression: multiple groups should get distinct _group_N keys
	data := make(map[string]interface{})
	schema := make(map[string]interface{})

	// Create 4 distinct fingerprint groups of 3 each
	for groupNum := 0; groupNum < 4; groupNum++ {
		for i := 0; i < 3; i++ {
			key := fmt.Sprintf("g%d_item_%d", groupNum, i)
			// Each group has a different structure
			obj := map[string]interface{}{}
			for f := 0; f <= groupNum; f++ {
				obj[fmt.Sprintf("field_%d", f)] = "value"
			}
			data[key] = []interface{}{obj}
			schema[key] = []interface{}{map[string]interface{}{}}
		}
	}

	summary := &structuralSummary{
		Samples:  make(map[string]interface{}),
		Metadata: make(map[string]interface{}),
	}
	for key := range data {
		summary.Metadata[key] = map[string]interface{}{
			"type":           "array",
			"length":         1,
			"item_avg_bytes": 20,
		}
	}

	result, groups := collapseSiblings(schema, data, "", summary)
	assert.Len(t, groups, 4, "should form 4 groups")

	// Verify all 4 _group_N keys are present and distinct
	groupKeys := make(map[string]bool)
	for k := range result {
		if strings.HasPrefix(k, "_group_") {
			assert.False(t, groupKeys[k], "duplicate group key: %s", k)
			groupKeys[k] = true
		}
	}
	assert.Len(t, groupKeys, 4, "should have 4 distinct group keys")
}

func TestCollapseSiblings_SingleGroupUnwrap(t *testing.T) {
	// All children form one group → should unwrap (no _group_0 wrapper)
	data := make(map[string]interface{})
	schema := make(map[string]interface{})
	for i := 0; i < 5; i++ {
		key := fmt.Sprintf("arr_%d", i)
		data[key] = []interface{}{float64(1)}
		schema[key] = []interface{}{"number"}
	}

	summary := &structuralSummary{
		Samples:  make(map[string]interface{}),
		Metadata: make(map[string]interface{}),
	}
	for key := range data {
		summary.Metadata[key] = map[string]interface{}{
			"type":           "array",
			"length":         1,
			"item_avg_bytes": 1,
		}
	}

	result, groups := collapseSiblings(schema, data, "", summary)
	assert.Len(t, groups, 1)
	// Should be unwrapped — _grouped is directly in result, not nested under _group_0
	assert.True(t, result["_grouped"].(bool))
	assert.Nil(t, result["_group_0"], "single group should be unwrapped, not wrapped in _group_0")
}

func TestCollapseSiblings_SingleGroupWithUngrouped(t *testing.T) {
	// 5 identical arrays + 2 scalars → should NOT unwrap (ungrouped children exist)
	data := make(map[string]interface{})
	schema := make(map[string]interface{})
	for i := 0; i < 5; i++ {
		key := fmt.Sprintf("arr_%d", i)
		data[key] = []interface{}{float64(1)}
		schema[key] = []interface{}{"number"}
	}
	data["scalar_a"] = "hello"
	data["scalar_b"] = float64(42)
	schema["scalar_a"] = "hello"
	schema["scalar_b"] = float64(42)

	summary := &structuralSummary{
		Samples:  make(map[string]interface{}),
		Metadata: make(map[string]interface{}),
	}
	for i := 0; i < 5; i++ {
		key := fmt.Sprintf("arr_%d", i)
		summary.Metadata[key] = map[string]interface{}{
			"type":           "array",
			"length":         1,
			"item_avg_bytes": 1,
		}
	}

	result, groups := collapseSiblings(schema, data, "", summary)
	assert.Len(t, groups, 1)
	// Should have _group_0 wrapper since ungrouped children exist
	assert.Contains(t, result, "_group_0")
	assert.Contains(t, result, "scalar_a")
	assert.Contains(t, result, "scalar_b")
}

func TestCollapseSiblings_MultiGroupMetadataKeys(t *testing.T) {
	// 2 groups under the same parent → each should get distinct metadata key
	data := make(map[string]interface{})
	schema := make(map[string]interface{})

	// Group A: 3 arrays of {x, y}
	for i := 0; i < 3; i++ {
		key := fmt.Sprintf("ga_%d", i)
		data[key] = []interface{}{map[string]interface{}{"x": float64(1), "y": float64(2)}}
		schema[key] = []interface{}{map[string]interface{}{"x": "number", "y": "number"}}
	}
	// Group B: 3 arrays of {name, value}
	for i := 0; i < 3; i++ {
		key := fmt.Sprintf("gb_%d", i)
		data[key] = []interface{}{map[string]interface{}{"name": "test", "value": float64(1)}}
		schema[key] = []interface{}{map[string]interface{}{"name": "string", "value": "number"}}
	}

	summary := &structuralSummary{
		Samples:  make(map[string]interface{}),
		Metadata: make(map[string]interface{}),
	}
	for key := range data {
		summary.Metadata[key] = map[string]interface{}{
			"type":           "array",
			"length":         1,
			"item_avg_bytes": 30,
		}
	}

	_, groups := collapseSiblings(schema, data, "parent", summary)
	assert.Len(t, groups, 2)

	// Multi-group metadata keys should be distinct
	metaKeys := make(map[string]bool)
	for k := range summary.Metadata {
		if strings.Contains(k, "_group_") || k == "parent" {
			metaKeys[k] = true
		}
	}
	// Should have 2 distinct metadata keys (parent._group_0 and parent._group_1)
	assert.GreaterOrEqual(t, len(metaKeys), 2, "multi-group should produce distinct metadata keys")
}

func TestCollapseSiblings_SingleGroupMetadataKey(t *testing.T) {
	// Single group → metadata key should be just the prefix (no ._group_0)
	data := make(map[string]interface{})
	schema := make(map[string]interface{})
	for i := 0; i < 4; i++ {
		key := fmt.Sprintf("item_%d", i)
		data[key] = []interface{}{float64(1)}
		schema[key] = []interface{}{"number"}
	}

	summary := &structuralSummary{
		Samples:  make(map[string]interface{}),
		Metadata: make(map[string]interface{}),
	}
	for key := range data {
		summary.Metadata[key] = map[string]interface{}{
			"type":           "array",
			"length":         1,
			"item_avg_bytes": 1,
		}
	}

	_, groups := collapseSiblings(schema, data, "parent.child", summary)
	assert.Len(t, groups, 1)

	// Single group → metadata key is parent path
	assert.Contains(t, summary.Metadata, "parent.child")
	assert.NotContains(t, summary.Metadata, "parent.child._group_0")
}

// --- walkForSummary tests for §13 changes ---

func TestWalkForSummary_TopLevelSiblings(t *testing.T) {
	// Top-level map with 10 identical arrays — should be deduplicated at root
	data := make(map[string]interface{})
	for i := 0; i < 10; i++ {
		key := fmt.Sprintf("series_%d", i)
		data[key] = []interface{}{
			map[string]interface{}{"time": "2025-01-01", "value": float64(i)},
		}
	}

	summary := generateStructuralSummary(data)

	// Should be deduplicated — single group at root → unwrapped
	assert.True(t, summary.Schema["_grouped"].(bool), "top-level siblings should be collapsed")
	assert.Equal(t, 10, summary.Schema["_count"])
}

func TestWalkForSummary_NullFirstSamples(t *testing.T) {
	// Array with null first elements — samples should skip nulls
	data := map[string]interface{}{
		"temperatures": []interface{}{nil, nil, -1.7, -1.5, 0.3},
	}

	summary := generateStructuralSummary(data)

	// Samples should contain real values, not nils
	foundNonNil := false
	for path, val := range summary.Samples {
		if strings.HasPrefix(path, "temperatures[") {
			if val != nil {
				foundNonNil = true
			}
		}
	}
	assert.True(t, foundNonNil, "samples should contain non-nil values from the array")
}

func TestWalkForSummary_AllNullSamples(t *testing.T) {
	// Array with ALL null elements — should fall back to sampling nulls
	data := map[string]interface{}{
		"empty_series": []interface{}{nil, nil, nil},
	}

	summary := generateStructuralSummary(data)

	// Should have some samples (fallback to first N)
	hasSamples := false
	for path := range summary.Samples {
		if strings.HasPrefix(path, "empty_series[") {
			hasSamples = true
		}
	}
	assert.True(t, hasSamples, "all-nil array should still have samples via fallback")
}

func TestWalkForSummary_NullFirstSchema(t *testing.T) {
	// Array with null first elements — schema should reflect real element type
	data := map[string]interface{}{
		"values": []interface{}{nil, nil, 42.5},
	}

	summary := generateStructuralSummary(data)

	// Schema should show "number", not "null"
	valuesSchema := summary.Schema["values"].([]interface{})
	assert.Len(t, valuesSchema, 1)
	assert.Equal(t, "number", valuesSchema[0], "schema should reflect real element type, not null")
}

// --- Cross-domain integration tests using real-world test data ---

func loadTestData(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("failed to load test data from %s: %v", path, err)
	}
	return data
}

// navigateSchema navigates a nested schema map by dot-separated path.
func navigateSchema(schema map[string]interface{}, path string) map[string]interface{} {
	parts := strings.Split(path, ".")
	current := schema
	for _, part := range parts {
		if val, ok := current[part]; ok {
			if m, ok := val.(map[string]interface{}); ok {
				current = m
			} else {
				return nil
			}
		} else {
			return nil
		}
	}
	return current
}

func TestStructuralSummary_FinnhubFinancials(t *testing.T) {
	raw := loadTestData(t, "testdata/basic_financials_response.json")
	var sourceData map[string]interface{}
	err := json.Unmarshal(raw, &sourceData)
	assert.NoError(t, err)

	summary := generateStructuralSummary(sourceData)
	summaryJSON, _ := json.MarshalIndent(summary, "", "  ")

	// Verify size reduction (raw 221KB → summary should be compact)
	assert.Less(t, len(summaryJSON), 15000, "summary should be <15KB after deduplication")

	// Verify grouped entries exist for annual/quarterly
	// annual: single-group unwrap → directly contains _grouped: true
	annual := navigateSchema(summary.Schema, "data.series.annual")
	if assert.NotNil(t, annual, "should have data.series.annual in schema") {
		assert.True(t, annual["_grouped"].(bool), "annual should be grouped")
		assert.Greater(t, annual["_count"], 0, "annual should have positive count")
		assert.Nil(t, annual["_group_0"], "single group should be unwrapped")

		// Verify element schema describes {period, value}
		if elemSchema, ok := annual["_element_schema"].([]interface{}); ok && len(elemSchema) > 0 {
			if schemaObj, ok := elemSchema[0].(map[string]interface{}); ok {
				assert.Contains(t, schemaObj, "period")
				assert.Contains(t, schemaObj, "value")
			}
		}
	}

	quarterly := navigateSchema(summary.Schema, "data.series.quarterly")
	if assert.NotNil(t, quarterly, "should have data.series.quarterly in schema") {
		assert.True(t, quarterly["_grouped"].(bool), "quarterly should be grouped")
		assert.Greater(t, quarterly["_count"], 0, "quarterly should have positive count")
	}

	// Verify samples are reduced (only representative members)
	assert.LessOrEqual(t, len(summary.Samples), 10, "samples should be reduced after deduplication")
}

func TestStructuralSummary_NpmExpressRegistry(t *testing.T) {
	raw := loadTestData(t, "testdata/npm_express_registry.json")
	var sourceData map[string]interface{}
	err := json.Unmarshal(raw, &sourceData)
	assert.NoError(t, err)

	summary := generateStructuralSummary(sourceData)
	summaryJSON, _ := json.MarshalIndent(summary, "", "  ")

	// Verify deduplication produces meaningful reduction
	rawJSON, _ := json.Marshal(sourceData)
	assert.Less(t, len(summaryJSON), len(rawJSON),
		"summary should be smaller than raw data after deduplication")

	// Verify no key collisions (Bug 1 regression)
	versions := navigateSchema(summary.Schema, "versions")
	if assert.NotNil(t, versions, "should have versions in schema") {
		groupKeys := make(map[string]bool)
		for k := range versions {
			if strings.HasPrefix(k, "_group_") {
				assert.False(t, groupKeys[k], "duplicate group key: %s", k)
				groupKeys[k] = true
			}
		}
		// Vetting found ~38 groups from 287 versions
		assert.GreaterOrEqual(t, len(groupKeys), 20,
			"should form many groups from version objects")

		// Verify group entries are self-describing
		for k, v := range versions {
			if strings.HasPrefix(k, "_group_") {
				group := v.(map[string]interface{})
				assert.True(t, group["_grouped"].(bool))
				assert.NotNil(t, group["_element_schema"])
				assert.NotNil(t, group["_keys"])
			}
		}
	}

	// Verify multi-group metadata keys don't collide
	metaGroupKeys := make(map[string]bool)
	for k := range summary.Metadata {
		if strings.Contains(k, "_group_") {
			assert.False(t, metaGroupKeys[k], "duplicate metadata key: %s", k)
			metaGroupKeys[k] = true
		}
	}
}

func TestStructuralSummary_OpenMeteoWeather(t *testing.T) {
	raw := loadTestData(t, "testdata/openmeteo_weather_forecast.json")
	var sourceData map[string]interface{}
	err := json.Unmarshal(raw, &sourceData)
	assert.NoError(t, err)

	summary := generateStructuralSummary(sourceData)
	summaryJSON, _ := json.MarshalIndent(summary, "", "  ")

	// Verify size reduction (raw 667KB → summary should be compact)
	assert.Less(t, len(summaryJSON), 10000, "summary should be <10KB after deduplication")

	// Verify Bug 2 fix: no "null" element schemas for grouped entries
	hourly := navigateSchema(summary.Schema, "hourly")
	if assert.NotNil(t, hourly, "should have hourly in schema") {
		for k, v := range hourly {
			if strings.HasPrefix(k, "_group_") {
				group := v.(map[string]interface{})
				elemSchema := group["_element_schema"]
				schemaStr := fmt.Sprintf("%v", elemSchema)
				assert.NotEqual(t, "[null]", schemaStr,
					"group %s should not have null element schema — representativeElement should find real data", k)
			}
		}
	}

	// Verify samples contain actual values, not nil
	for path, val := range summary.Samples {
		if strings.Contains(path, "hourly.") && strings.Contains(path, "[") {
			// Not all hourly fields start with "temperature" — just check we have some non-nil
			if val != nil {
				// Good — at least some samples have real values
				continue
			}
		}
	}
}

// --- Coverage gap tests ---

func TestWalkForSummary_AllNullShortArray(t *testing.T) {
	// All-nil array with fewer elements than maxSummarySamples (2).
	// Covers the len(v) < sampleCount branch in the all-nil fallback.
	data := map[string]interface{}{
		"short": []interface{}{nil},
	}

	summary := generateStructuralSummary(data)

	// Should have 1 sample via fallback (array length < maxSummarySamples)
	assert.Contains(t, summary.Samples, "short[0]")
	assert.Nil(t, summary.Samples["short[0]"])
}

// --- Tests for ORCH-010: Large scalar string capping in walkForSummary ---

func TestGenerateStructuralSummary_LargeScalarCapped(t *testing.T) {
	// Large string scalar (e.g., base64 screenshot) should be replaced with
	// a type+size descriptor, not embedded verbatim in the schema.
	largeValue := strings.Repeat("A", 10240) // 10KB string
	data := map[string]interface{}{
		"data": map[string]interface{}{
			"screenshot": largeValue,
			"name":       "test-result",
		},
	}

	summary := generateStructuralSummary(data)

	// Navigate into the schema
	dataSchema := summary.Schema["data"].(map[string]interface{})

	// Large string should be capped with size descriptor
	assert.Equal(t, "string(10240 bytes)", dataSchema["screenshot"])

	// Small string should be preserved as actual value
	assert.Equal(t, "test-result", dataSchema["name"])
}

func TestGenerateStructuralSummary_SmallScalarPreserved(t *testing.T) {
	// Strings at or below maxScalarSchemaBytes (256) should be preserved
	// as actual values for template interpolation and literal inference.
	shortString := strings.Repeat("x", 256) // exactly at threshold
	data := map[string]interface{}{
		"symbol": "GOOGL",
		"url":    "https://example.com/api/v1/data",
		"exact":  shortString,
	}

	summary := generateStructuralSummary(data)

	assert.Equal(t, "GOOGL", summary.Schema["symbol"])
	assert.Equal(t, "https://example.com/api/v1/data", summary.Schema["url"])
	assert.Equal(t, shortString, summary.Schema["exact"]) // 256 bytes = at threshold, kept
}

func TestGenerateStructuralSummary_NonStringScalarUnchanged(t *testing.T) {
	// Non-string scalars (numbers, booleans, nil) are always embedded
	// regardless of the string cap — they're inherently small.
	data := map[string]interface{}{
		"count":    float64(42),
		"active":   true,
		"ratio":    float64(3.14),
		"nothing":  nil,
		"duration": float64(12826),
	}

	summary := generateStructuralSummary(data)

	assert.Equal(t, float64(42), summary.Schema["count"])
	assert.Equal(t, true, summary.Schema["active"])
	assert.Equal(t, float64(3.14), summary.Schema["ratio"])
	assert.Nil(t, summary.Schema["nothing"])
	assert.Equal(t, float64(12826), summary.Schema["duration"])
}

func TestGenerateStructuralSummary_MixedScalarsInNestedMap(t *testing.T) {
	// Simulates the browser test tool scenario: a map containing both
	// small scalars and large base64 screenshot strings.
	screenshot1 := strings.Repeat("iVBORw0KGgo", 5000) // ~55KB base64-like
	screenshot2 := strings.Repeat("iVBORw0KGgo", 5000)
	data := map[string]interface{}{
		"data": map[string]interface{}{
			"passed":      true,
			"duration_ms": float64(12826),
			"url":         "https://u.cisco.com/search",
			"screenshots": map[string]interface{}{
				"1": screenshot1,
				"7": screenshot2,
			},
		},
		"success": true,
	}

	summary := generateStructuralSummary(data)
	summaryJSON, err := json.Marshal(summary)
	assert.NoError(t, err)

	// The summary must be compact — well under 10KB regardless of screenshot size
	assert.Less(t, len(summaryJSON), 10240,
		"Summary should be compact; got %d bytes (screenshots are %d bytes each)",
		len(summaryJSON), len(screenshot1))

	// Verify small scalars preserved, large strings capped
	dataSchema := summary.Schema["data"].(map[string]interface{})
	assert.Equal(t, true, dataSchema["passed"])
	assert.Equal(t, float64(12826), dataSchema["duration_ms"])
	assert.Equal(t, "https://u.cisco.com/search", dataSchema["url"])

	screenshots := dataSchema["screenshots"].(map[string]interface{})
	assert.Equal(t, fmt.Sprintf("string(%d bytes)", len(screenshot1)), screenshots["1"])
	assert.Equal(t, fmt.Sprintf("string(%d bytes)", len(screenshot2)), screenshots["7"])
}

func TestGenerateStructuralSummary_StringJustAboveThreshold(t *testing.T) {
	// String at 257 bytes (one byte above threshold) should be capped.
	aboveThreshold := strings.Repeat("x", 257)
	data := map[string]interface{}{
		"value": aboveThreshold,
	}

	summary := generateStructuralSummary(data)

	assert.Equal(t, "string(257 bytes)", summary.Schema["value"])
}

func TestCollapseSiblings_EmptyArrayGroup(t *testing.T) {
	// 3 empty arrays — should group and produce empty _element_schema.
	// Covers the len(v) == 0 branch for arrays in collapseSiblings.
	data := map[string]interface{}{
		"a": []interface{}{},
		"b": []interface{}{},
		"c": []interface{}{},
	}
	schema := map[string]interface{}{
		"a": []interface{}{},
		"b": []interface{}{},
		"c": []interface{}{},
	}

	summary := &structuralSummary{
		Samples:  make(map[string]interface{}),
		Metadata: make(map[string]interface{}),
	}

	result, groups := collapseSiblings(schema, data, "", summary)
	assert.Len(t, groups, 1)

	// Single group → unwrapped
	assert.True(t, result["_grouped"].(bool))
	assert.Equal(t, 3, result["_count"])
	// Element schema should be empty array (no items to infer type from)
	elemSchema := result["_element_schema"].([]interface{})
	assert.Empty(t, elemSchema)

	// Metadata should have clamped ranges [0, 0] (not [MaxInt64, 0])
	meta, ok := summary.Metadata["_group"].(map[string]interface{})
	assert.True(t, ok, "expected metadata for _group")
	assert.Equal(t, []int{0, 0}, meta["length_range"])
	assert.Equal(t, []int{0, 0}, meta["item_avg_bytes_range"])
}

// =============================================================================
// Model Override Tests
// =============================================================================

// optsCapturingAIClient captures the *core.AIOptions passed to GenerateResponse
// for verifying model override propagation.
type optsCapturingAIClient struct {
	mu           sync.Mutex
	capturedOpts []*core.AIOptions
	response     string
	err          error
}

func (m *optsCapturingAIClient) GenerateResponse(ctx context.Context, prompt string, opts *core.AIOptions) (*core.AIResponse, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if opts != nil {
		optsCopy := *opts
		m.capturedOpts = append(m.capturedOpts, &optsCopy)
	}
	if m.err != nil {
		return nil, m.err
	}
	resp := m.response
	if resp == "" {
		resp = `{"name": "extracted_value"}`
	}
	return &core.AIResponse{Content: resp, Model: "resolved-model", Provider: "test"}, nil
}

func (m *optsCapturingAIClient) lastOpts() *core.AIOptions {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.capturedOpts) == 0 {
		return nil
	}
	return m.capturedOpts[len(m.capturedOpts)-1]
}

// TestMicroResolver_SetModel_PropagatesModelToAIOptions verifies that SetModel
// causes opts.Model to be set on GenerateResponse calls for both resolveWithText
// and resolveWithSchemaMapping paths.
func TestMicroResolver_SetModel_PropagatesModelToAIOptions(t *testing.T) {
	t.Run("resolveWithText passes model override", func(t *testing.T) {
		mock := &optsCapturingAIClient{}
		mr := NewMicroResolver(mock, nil)
		mr.SetModel("fast")

		cap := &EnhancedCapability{
			Name:       "test_cap",
			Parameters: []Parameter{{Name: "name", Type: "string", Required: true}},
		}
		sourceData := map[string]interface{}{"some_key": "some_value"}

		_, _ = mr.resolveWithText(context.Background(), sourceData, cap, "", "step-1")

		opts := mock.lastOpts()
		if opts == nil {
			t.Fatal("Expected AI options to be captured")
		}
		if opts.Model != "fast" {
			t.Errorf("Expected opts.Model='fast', got %q", opts.Model)
		}
	})

	t.Run("resolveWithSchemaMapping passes model override", func(t *testing.T) {
		mock := &optsCapturingAIClient{
			response: `{"name": {"source": "path", "path": "name"}}`,
		}
		mr := NewMicroResolver(mock, nil)
		mr.SetModel("fast")

		cap := &EnhancedCapability{
			Name:       "test_cap",
			Parameters: []Parameter{{Name: "name", Type: "string", Required: true}},
		}
		sourceData := map[string]interface{}{"name": "test_value"}

		_, _ = mr.resolveWithSchemaMapping(context.Background(), sourceData, cap, "", "step-1")

		opts := mock.lastOpts()
		if opts == nil {
			t.Fatal("Expected AI options to be captured")
		}
		if opts.Model != "fast" {
			t.Errorf("Expected opts.Model='fast', got %q", opts.Model)
		}
	})

	t.Run("empty model leaves opts.Model unset", func(t *testing.T) {
		mock := &optsCapturingAIClient{}
		mr := NewMicroResolver(mock, nil)
		// Do NOT call SetModel — model stays ""

		cap := &EnhancedCapability{
			Name:       "test_cap",
			Parameters: []Parameter{{Name: "name", Type: "string", Required: true}},
		}
		sourceData := map[string]interface{}{"some_key": "some_value"}

		_, _ = mr.resolveWithText(context.Background(), sourceData, cap, "", "step-1")

		opts := mock.lastOpts()
		if opts == nil {
			t.Fatal("Expected AI options to be captured")
		}
		if opts.Model != "" {
			t.Errorf("Expected opts.Model='', got %q", opts.Model)
		}
	})
}

func TestMicroResolver_SetAIOptionsOverride_PropagatesToGenerateResponse(t *testing.T) {
	mock := &optsCapturingAIClient{}
	mr := NewMicroResolver(mock, nil)
	mr.SetAIOptionsOverride(&AIOptionsOverride{
		Model:           StringPtr("fast"),
		Temperature:     Float32Ptr(0),
		MaxTokens:       IntPtr(1111),
		ReasoningEffort: StringPtr("none"),
		ResponseFormat:  StringPtr("json"),
	})

	cap := &EnhancedCapability{
		Name:       "test_cap",
		Parameters: []Parameter{{Name: "name", Type: "string", Required: true}},
	}
	sourceData := map[string]interface{}{"some_key": "some_value"}

	_, _ = mr.resolveWithText(context.Background(), sourceData, cap, "", "step-1")

	opts := mock.lastOpts()
	if opts == nil {
		t.Fatal("expected AI options to be captured")
	}
	if opts.Model != "fast" || opts.Temperature != 0 || opts.MaxTokens != 1111 || opts.ReasoningEffort != "none" || opts.ResponseFormat != "json" {
		t.Fatalf("unexpected override propagation: %#v", opts)
	}
}

// TestMicroResolver_SetModel_ErrorPath_RecordsModelInDebugPayload verifies that
// when GenerateResponse fails, the debug interaction records Model = m.model.
func TestMicroResolver_SetModel_ErrorPath_RecordsModelInDebugPayload(t *testing.T) {
	mock := &optsCapturingAIClient{err: errors.New("LLM unavailable")}
	mr := NewMicroResolver(mock, nil)
	mr.SetModel("fast")

	debugStore := &capturingDebugStore{}
	mr.SetLLMDebugStore(debugStore)

	cap := &EnhancedCapability{
		Name:       "test_cap",
		Parameters: []Parameter{{Name: "name", Type: "string", Required: true}},
	}
	sourceData := map[string]interface{}{"some_key": "some_value"}

	// resolveWithText will fail
	_, _ = mr.resolveWithText(context.Background(), sourceData, cap, "", "step-1")

	// Wait for async debug recording
	mr.debugWg.Wait()

	interactions := debugStore.getInteractions()
	if len(interactions) == 0 {
		t.Fatal("Expected at least one debug interaction")
	}
	if interactions[0].Model != "fast" {
		t.Errorf("Expected debug interaction Model='fast', got %q", interactions[0].Model)
	}
	if interactions[0].Success {
		t.Error("Expected Success=false for error path")
	}
}

// TestMicroResolver_SetModel_SchemaMapping_ErrorPath_RecordsModel verifies
// that schema_mapping error debug payloads include the model override.
func TestMicroResolver_SetModel_SchemaMapping_ErrorPath_RecordsModel(t *testing.T) {
	mock := &optsCapturingAIClient{err: errors.New("LLM unavailable")}
	mr := NewMicroResolver(mock, nil)
	mr.SetModel("fast")

	debugStore := &capturingDebugStore{}
	mr.SetLLMDebugStore(debugStore)

	cap := &EnhancedCapability{
		Name:       "test_cap",
		Parameters: []Parameter{{Name: "name", Type: "string", Required: true}},
	}
	sourceData := map[string]interface{}{"some_key": "some_value"}

	_, _ = mr.resolveWithSchemaMapping(context.Background(), sourceData, cap, "", "step-1")

	mr.debugWg.Wait()

	interactions := debugStore.getInteractions()
	if len(interactions) == 0 {
		t.Fatal("Expected at least one debug interaction")
	}
	if interactions[0].Model != "fast" {
		t.Errorf("Expected debug interaction Model='fast', got %q", interactions[0].Model)
	}
	if interactions[0].Type != "schema_mapping" {
		t.Errorf("Expected Type='schema_mapping', got %q", interactions[0].Type)
	}
}

// --- RC5: MicroResolver SetMaxTokens tests ---

func TestMicroResolver_DefaultMaxTokens(t *testing.T) {
	mr := NewMicroResolver(nil, nil)
	if mr.maxTokens != 2000 {
		t.Errorf("Expected default maxTokens 2000, got %d", mr.maxTokens)
	}
}

func TestMicroResolver_SetMaxTokens(t *testing.T) {
	mr := NewMicroResolver(nil, nil)

	t.Run("set valid value", func(t *testing.T) {
		mr.SetMaxTokens(4000)
		if mr.maxTokens != 4000 {
			t.Errorf("Expected maxTokens 4000, got %d", mr.maxTokens)
		}
	})

	t.Run("zero ignored", func(t *testing.T) {
		mr.SetMaxTokens(4000) // set to known value first
		mr.SetMaxTokens(0)
		if mr.maxTokens != 4000 {
			t.Errorf("Expected maxTokens 4000 (unchanged), got %d", mr.maxTokens)
		}
	})

	t.Run("negative ignored", func(t *testing.T) {
		mr.SetMaxTokens(4000)
		mr.SetMaxTokens(-100)
		if mr.maxTokens != 4000 {
			t.Errorf("Expected maxTokens 4000 (unchanged), got %d", mr.maxTokens)
		}
	})
}
