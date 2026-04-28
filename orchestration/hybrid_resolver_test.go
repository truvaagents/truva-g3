package orchestration

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/truvaagents/truva-g3/core"
)

// mockAIClient implements core.AIClient for testing HybridResolver
type mockAIClient struct {
	generateFunc func(ctx context.Context, prompt string, opts *core.AIOptions) (*core.AIResponse, error)
	calls        []string
}

func (m *mockAIClient) GenerateResponse(ctx context.Context, prompt string, opts *core.AIOptions) (*core.AIResponse, error) {
	m.calls = append(m.calls, prompt)
	if m.generateFunc != nil {
		return m.generateFunc(ctx, prompt, opts)
	}
	return &core.AIResponse{Content: "{}"}, nil
}

// Helper to create a mock AI client that returns specific JSON
func newMockAIClientWithResponse(response map[string]interface{}) *mockAIClient {
	return &mockAIClient{
		generateFunc: func(ctx context.Context, prompt string, opts *core.AIOptions) (*core.AIResponse, error) {
			jsonBytes, _ := json.Marshal(response)
			return &core.AIResponse{Content: string(jsonBytes)}, nil
		},
	}
}

// TestHybridResolver_AllParamsAutoWired tests the case where all parameters
// are successfully matched by auto-wiring, so no LLM call is needed.
func TestHybridResolver_AllParamsAutoWired(t *testing.T) {
	aiClient := &mockAIClient{}
	resolver := NewHybridResolver(aiClient, nil)

	// Source data has exact name matches
	depResults := map[string]*StepResult{
		"step-1": {
			StepID:   "step-1",
			Success:  true,
			Response: `{"lat": 48.85, "lon": 2.35}`,
		},
	}

	targetCap := &EnhancedCapability{
		Name: "get_weather",
		Parameters: []Parameter{
			{Name: "lat", Type: "number", Required: true},
			{Name: "lon", Type: "number", Required: true},
		},
	}

	ctx := context.Background()
	result, err := resolver.ResolveParameters(ctx, depResults, targetCap, "test-step", "")

	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}

	params := result.Parameters

	// Should have resolved both params
	if len(params) != 2 {
		t.Errorf("Expected 2 params, got %d: %v", len(params), params)
	}

	if params["lat"].(float64) != 48.85 {
		t.Errorf("Expected lat=48.85, got %v", params["lat"])
	}

	if params["lon"].(float64) != 2.35 {
		t.Errorf("Expected lon=2.35, got %v", params["lon"])
	}

	// No LLM calls should have been made
	if len(aiClient.calls) != 0 {
		t.Errorf("Expected no AI calls (all auto-wired), got %d calls", len(aiClient.calls))
	}

	// Verify resolution metadata
	if result.Metadata == nil {
		t.Fatal("Expected resolution metadata, got nil")
	}
	if result.Metadata.AutoWiredCount != 2 {
		t.Errorf("Expected 2 auto-wired, got %d", result.Metadata.AutoWiredCount)
	}
	if result.Metadata.MicroResolvedCount != 0 {
		t.Errorf("Expected 0 micro-resolved, got %d", result.Metadata.MicroResolvedCount)
	}
}

// TestHybridResolver_OptionalParamsUnmapped tests that optional unmapped params
// don't trigger micro-resolution - only required params do.
func TestHybridResolver_OptionalParamsUnmapped(t *testing.T) {
	aiClient := &mockAIClient{}
	resolver := NewHybridResolver(aiClient, nil)

	depResults := map[string]*StepResult{
		"step-1": {
			StepID:   "step-1",
			Success:  true,
			Response: `{"lat": 48.85, "lon": 2.35}`,
		},
	}

	targetCap := &EnhancedCapability{
		Name: "get_weather",
		Parameters: []Parameter{
			{Name: "lat", Type: "number", Required: true},
			{Name: "lon", Type: "number", Required: true},
			{Name: "unit", Type: "string", Required: false}, // Optional, not in source
		},
	}

	ctx := context.Background()
	result, err := resolver.ResolveParameters(ctx, depResults, targetCap, "test-step", "")

	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}

	params := result.Parameters

	// Should have resolved required params only
	if len(params) != 2 {
		t.Errorf("Expected 2 params (required only), got %d: %v", len(params), params)
	}

	// No LLM calls - optional params don't trigger micro-resolution
	if len(aiClient.calls) != 0 {
		t.Errorf("Expected no AI calls (optional params don't trigger LLM), got %d calls", len(aiClient.calls))
	}
}

// TestHybridResolver_RequiredParamsMissingTriggersMicroResolution tests that
// unmapped required params trigger the LLM micro-resolution fallback.
func TestHybridResolver_RequiredParamsMissingTriggersMicroResolution(t *testing.T) {
	// Mock AI client returns the missing params
	aiClient := newMockAIClientWithResponse(map[string]interface{}{
		"lat": 48.85,
		"lon": 2.35,
	})
	resolver := NewHybridResolver(aiClient, nil)

	// Source has different names - auto-wiring won't match
	depResults := map[string]*StepResult{
		"step-1": {
			StepID:   "step-1",
			Success:  true,
			Response: `{"latitude": 48.85, "longitude": 2.35}`,
		},
	}

	targetCap := &EnhancedCapability{
		Name: "get_weather",
		Parameters: []Parameter{
			{Name: "lat", Type: "number", Required: true},
			{Name: "lon", Type: "number", Required: true},
		},
	}

	ctx := context.Background()
	result, err := resolver.ResolveParameters(ctx, depResults, targetCap, "test-step", "")

	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}

	params := result.Parameters

	// Should have resolved params via micro-resolution
	if len(params) != 2 {
		t.Errorf("Expected 2 params from micro-resolution, got %d: %v", len(params), params)
	}

	// LLM should have been called (micro-resolution)
	if len(aiClient.calls) == 0 {
		t.Error("Expected AI call for micro-resolution, got none")
	}

	// Verify micro-resolution metadata
	if result.Metadata == nil {
		t.Fatal("Expected resolution metadata, got nil")
	}
	if result.Metadata.MicroResolvedCount != 2 {
		t.Errorf("Expected 2 micro-resolved, got %d", result.Metadata.MicroResolvedCount)
	}
}

// TestHybridResolver_MicroResolutionDisabled tests that when micro-resolution
// is disabled, only auto-wired params are returned.
func TestHybridResolver_MicroResolutionDisabled(t *testing.T) {
	aiClient := &mockAIClient{}
	resolver := NewHybridResolver(aiClient, nil, WithMicroResolution(false))

	// Source has different names - auto-wiring won't match
	depResults := map[string]*StepResult{
		"step-1": {
			StepID:   "step-1",
			Success:  true,
			Response: `{"latitude": 48.85, "longitude": 2.35}`,
		},
	}

	targetCap := &EnhancedCapability{
		Name: "get_weather",
		Parameters: []Parameter{
			{Name: "lat", Type: "number", Required: true},
			{Name: "lon", Type: "number", Required: true},
		},
	}

	ctx := context.Background()
	result, err := resolver.ResolveParameters(ctx, depResults, targetCap, "test-step", "")

	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}

	params := result.Parameters

	// Should return empty - nothing auto-wired, micro-resolution disabled
	if len(params) != 0 {
		t.Errorf("Expected 0 params (micro-resolution disabled), got %d: %v", len(params), params)
	}

	// No LLM calls since disabled
	if len(aiClient.calls) != 0 {
		t.Errorf("Expected no AI calls (micro-resolution disabled), got %d calls", len(aiClient.calls))
	}
}

// TestHybridResolver_EmptyDependencyResults tests handling of no dependency data.
func TestHybridResolver_EmptyDependencyResults(t *testing.T) {
	aiClient := &mockAIClient{}
	resolver := NewHybridResolver(aiClient, nil)

	depResults := map[string]*StepResult{}

	targetCap := &EnhancedCapability{
		Name: "get_weather",
		Parameters: []Parameter{
			{Name: "lat", Type: "number", Required: true},
		},
	}

	ctx := context.Background()
	result, err := resolver.ResolveParameters(ctx, depResults, targetCap, "test-step", "")

	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}

	// Should return empty params - no source data available
	if result.Parameters != nil {
		t.Errorf("Expected nil params for empty dependencies, got %v", result.Parameters)
	}

	// Metadata should still exist with zero counts
	if result.Metadata == nil {
		t.Fatal("Expected metadata even with empty dependencies")
	}
	if result.Metadata.SourceDataKeyCount != 0 {
		t.Errorf("Expected 0 source data keys, got %d", result.Metadata.SourceDataKeyCount)
	}
}

// TestHybridResolver_FailedStepsSkipped tests that failed dependency steps
// are not included in source data.
func TestHybridResolver_FailedStepsSkipped(t *testing.T) {
	aiClient := &mockAIClient{}
	resolver := NewHybridResolver(aiClient, nil)

	depResults := map[string]*StepResult{
		"step-1": {
			StepID:   "step-1",
			Success:  false, // Failed step
			Response: `{"lat": 48.85, "lon": 2.35}`,
		},
		"step-2": {
			StepID:   "step-2",
			Success:  true,
			Response: `{"city": "Paris"}`,
		},
	}

	targetCap := &EnhancedCapability{
		Name: "get_weather",
		Parameters: []Parameter{
			{Name: "lat", Type: "number", Required: true},
			{Name: "city", Type: "string", Required: false},
		},
	}

	ctx := context.Background()
	result, err := resolver.ResolveParameters(ctx, depResults, targetCap, "test-step", "")

	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}

	params := result.Parameters

	// Should only have city from step-2 (step-1 failed, so lat not available)
	if params["city"] != "Paris" {
		t.Errorf("Expected city=Paris from step-2, got %v", params["city"])
	}

	// lat should NOT be present (from failed step)
	if _, hasLat := params["lat"]; hasLat {
		t.Error("Expected lat to be missing (from failed step), but it was present")
	}
}

// TestHybridResolver_MultipleDependencies tests merging data from multiple steps.
func TestHybridResolver_MultipleDependencies(t *testing.T) {
	aiClient := &mockAIClient{}
	resolver := NewHybridResolver(aiClient, nil)

	depResults := map[string]*StepResult{
		"geocoding": {
			StepID:   "geocoding",
			Success:  true,
			Response: `{"lat": 48.85, "lon": 2.35}`,
		},
		"country-info": {
			StepID:   "country-info",
			Success:  true,
			Response: `{"currency": "EUR", "population": 67000000}`,
		},
	}

	targetCap := &EnhancedCapability{
		Name: "convert_currency",
		Parameters: []Parameter{
			{Name: "lat", Type: "number", Required: true},
			{Name: "currency", Type: "string", Required: true},
		},
	}

	ctx := context.Background()
	result, err := resolver.ResolveParameters(ctx, depResults, targetCap, "test-step", "")

	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}

	params := result.Parameters

	// Should have merged data from both steps
	if params["lat"].(float64) != 48.85 {
		t.Errorf("Expected lat=48.85 from geocoding, got %v", params["lat"])
	}

	if params["currency"].(string) != "EUR" {
		t.Errorf("Expected currency=EUR from country-info, got %v", params["currency"])
	}
}

// TestHybridResolver_TypeCoercion tests that type coercion works during auto-wiring.
func TestHybridResolver_TypeCoercion(t *testing.T) {
	aiClient := &mockAIClient{}
	resolver := NewHybridResolver(aiClient, nil)

	// Source has string values
	depResults := map[string]*StepResult{
		"step-1": {
			StepID:   "step-1",
			Success:  true,
			Response: `{"lat": "48.85", "lon": "2.35"}`,
		},
	}

	targetCap := &EnhancedCapability{
		Name: "get_weather",
		Parameters: []Parameter{
			{Name: "lat", Type: "number", Required: true},
			{Name: "lon", Type: "number", Required: true},
		},
	}

	ctx := context.Background()
	result, err := resolver.ResolveParameters(ctx, depResults, targetCap, "test-step", "")

	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}

	params := result.Parameters

	// Should have coerced strings to numbers
	if params["lat"].(float64) != 48.85 {
		t.Errorf("Expected lat=48.85 (coerced), got %v", params["lat"])
	}

	if params["lon"].(float64) != 2.35 {
		t.Errorf("Expected lon=2.35 (coerced), got %v", params["lon"])
	}

	// No LLM calls needed - auto-wiring with coercion
	if len(aiClient.calls) != 0 {
		t.Errorf("Expected no AI calls (auto-wiring with coercion), got %d calls", len(aiClient.calls))
	}
}

// TestHybridResolver_NestedObjectExtraction tests extraction from nested objects.
func TestHybridResolver_NestedObjectExtraction(t *testing.T) {
	aiClient := &mockAIClient{}
	resolver := NewHybridResolver(aiClient, nil)

	// Source has nested currency object
	depResults := map[string]*StepResult{
		"step-1": {
			StepID:   "step-1",
			Success:  true,
			Response: `{"currency": {"code": "EUR", "name": "Euro"}}`,
		},
	}

	targetCap := &EnhancedCapability{
		Name: "convert",
		Parameters: []Parameter{
			{Name: "currency", Type: "string", Required: true}, // Expects string, not object
		},
	}

	ctx := context.Background()
	result, err := resolver.ResolveParameters(ctx, depResults, targetCap, "test-step", "")

	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}

	params := result.Parameters

	// Should extract "code" from nested object
	if params["currency"].(string) != "EUR" {
		t.Errorf("Expected currency=EUR (extracted from nested), got %v", params["currency"])
	}
}

// TestHybridResolver_CaseInsensitiveMatch tests case-insensitive name matching.
func TestHybridResolver_CaseInsensitiveMatch(t *testing.T) {
	aiClient := &mockAIClient{}
	resolver := NewHybridResolver(aiClient, nil)

	depResults := map[string]*StepResult{
		"step-1": {
			StepID:   "step-1",
			Success:  true,
			Response: `{"LAT": 48.85, "LON": 2.35}`,
		},
	}

	targetCap := &EnhancedCapability{
		Name: "get_weather",
		Parameters: []Parameter{
			{Name: "lat", Type: "number", Required: true},
			{Name: "lon", Type: "number", Required: true},
		},
	}

	ctx := context.Background()
	result, err := resolver.ResolveParameters(ctx, depResults, targetCap, "test-step", "")

	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}

	params := result.Parameters

	if params["lat"].(float64) != 48.85 {
		t.Errorf("Expected lat=48.85 (case-insensitive), got %v", params["lat"])
	}

	// No LLM calls - case-insensitive is handled by auto-wiring
	if len(aiClient.calls) != 0 {
		t.Errorf("Expected no AI calls (case-insensitive auto-wire), got %d calls", len(aiClient.calls))
	}
}

// TestHybridResolver_AutoWiredPriorityOverMicroResolution tests that auto-wired
// values are not overwritten by micro-resolution results.
func TestHybridResolver_AutoWiredPriorityOverMicroResolution(t *testing.T) {
	// Mock returns different values than source
	aiClient := newMockAIClientWithResponse(map[string]interface{}{
		"lat":  99.99, // Different from source
		"city": "Tokyo",
	})
	resolver := NewHybridResolver(aiClient, nil)

	depResults := map[string]*StepResult{
		"step-1": {
			StepID:   "step-1",
			Success:  true,
			Response: `{"lat": 48.85}`, // Has lat but not city
		},
	}

	targetCap := &EnhancedCapability{
		Name: "get_weather",
		Parameters: []Parameter{
			{Name: "lat", Type: "number", Required: true},
			{Name: "city", Type: "string", Required: true}, // Not in source
		},
	}

	ctx := context.Background()
	result, err := resolver.ResolveParameters(ctx, depResults, targetCap, "test-step", "")

	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}

	params := result.Parameters

	// lat should be from auto-wiring (48.85), not micro-resolution (99.99)
	if params["lat"].(float64) != 48.85 {
		t.Errorf("Expected lat=48.85 (auto-wired priority), got %v", params["lat"])
	}

	// city should be from micro-resolution
	if params["city"].(string) != "Tokyo" {
		t.Errorf("Expected city=Tokyo (from micro-resolution), got %v", params["city"])
	}

	// Verify metadata tracks both layers
	if result.Metadata.AutoWiredCount != 1 {
		t.Errorf("Expected 1 auto-wired (lat), got %d", result.Metadata.AutoWiredCount)
	}
	if result.Metadata.MicroResolvedCount != 1 {
		t.Errorf("Expected 1 micro-resolved (city), got %d", result.Metadata.MicroResolvedCount)
	}
}

// TestHybridResolver_NilAIClient tests behavior when no AI client is provided.
func TestHybridResolver_NilAIClient(t *testing.T) {
	// Create resolver without AI client
	resolver := NewHybridResolver(nil, nil)

	depResults := map[string]*StepResult{
		"step-1": {
			StepID:   "step-1",
			Success:  true,
			Response: `{"latitude": 48.85}`, // Won't auto-wire to "lat"
		},
	}

	targetCap := &EnhancedCapability{
		Name: "get_weather",
		Parameters: []Parameter{
			{Name: "lat", Type: "number", Required: true},
		},
	}

	ctx := context.Background()
	result, err := resolver.ResolveParameters(ctx, depResults, targetCap, "test-step", "")

	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}

	params := result.Parameters

	// Should return empty - can't auto-wire, no AI client for micro-resolution
	if len(params) != 0 {
		t.Errorf("Expected 0 params (no AI client), got %d: %v", len(params), params)
	}
}

// TestHybridResolver_SetLogger tests logger propagation to sub-components.
func TestHybridResolver_SetLogger(t *testing.T) {
	aiClient := &mockAIClient{}
	resolver := NewHybridResolver(aiClient, nil)

	logger := &mockLogger{}
	resolver.SetLogger(logger)

	// Trigger some logging via resolution
	depResults := map[string]*StepResult{
		"step-1": {
			StepID:   "step-1",
			Success:  true,
			Response: `{"lat": 48.85}`,
		},
	}

	targetCap := &EnhancedCapability{
		Name: "test",
		Parameters: []Parameter{
			{Name: "lat", Type: "number", Required: true},
		},
	}

	ctx := context.Background()
	_, _ = resolver.ResolveParameters(ctx, depResults, targetCap, "test-step", "")

	// Should have logged something
	if len(logger.messages) == 0 {
		t.Error("Expected logger to receive messages, got none")
	}
}

// TestHybridResolver_MetadataParametersPerEntry tests that Metadata.Parameters
// contains correct per-parameter ParameterResolution entries with Layer and MatchType.
func TestHybridResolver_MetadataParametersPerEntry(t *testing.T) {
	aiClient := &mockAIClient{}
	resolver := NewHybridResolver(aiClient, nil)

	depResults := map[string]*StepResult{
		"step-1": {
			StepID:   "step-1",
			Success:  true,
			Response: `{"lat": 48.85, "LON": 2.35}`,
		},
	}

	targetCap := &EnhancedCapability{
		Name: "get_weather",
		Parameters: []Parameter{
			{Name: "lat", Type: "number", Required: true},
			{Name: "lon", Type: "number", Required: true},
		},
	}

	ctx := context.Background()
	result, err := resolver.ResolveParameters(ctx, depResults, targetCap, "test-step", "")

	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}

	if result.Metadata == nil {
		t.Fatal("Expected metadata, got nil")
	}

	// Should have 2 parameter resolution entries
	if len(result.Metadata.Parameters) != 2 {
		t.Fatalf("Expected 2 parameter entries, got %d", len(result.Metadata.Parameters))
	}

	// Build lookup by name
	paramByName := make(map[string]ParameterResolution)
	for _, p := range result.Metadata.Parameters {
		paramByName[p.Name] = p
	}

	// "lat" should be exact match
	latR, ok := paramByName["lat"]
	if !ok {
		t.Fatal("Expected parameter entry for 'lat'")
	}
	if latR.Layer != "auto_wire" {
		t.Errorf("lat: expected Layer=\"auto_wire\", got %q", latR.Layer)
	}
	if latR.MatchType != "exact" {
		t.Errorf("lat: expected MatchType=\"exact\", got %q", latR.MatchType)
	}
	if latR.SourceKey != "lat" {
		t.Errorf("lat: expected SourceKey=\"lat\", got %q", latR.SourceKey)
	}

	// "lon" should be case_insensitive match from "LON"
	lonR, ok := paramByName["lon"]
	if !ok {
		t.Fatal("Expected parameter entry for 'lon'")
	}
	if lonR.Layer != "auto_wire" {
		t.Errorf("lon: expected Layer=\"auto_wire\", got %q", lonR.Layer)
	}
	if lonR.MatchType != "case_insensitive" {
		t.Errorf("lon: expected MatchType=\"case_insensitive\", got %q", lonR.MatchType)
	}
	if lonR.SourceKey != "LON" {
		t.Errorf("lon: expected SourceKey=\"LON\", got %q", lonR.SourceKey)
	}
}

// TestHybridResolver_MetadataAutoWiringDuration tests that AutoWiringDurationUs is tracked.
func TestHybridResolver_MetadataAutoWiringDuration(t *testing.T) {
	aiClient := &mockAIClient{}
	resolver := NewHybridResolver(aiClient, nil)

	depResults := map[string]*StepResult{
		"step-1": {
			StepID:   "step-1",
			Success:  true,
			Response: `{"lat": 48.85}`,
		},
	}

	targetCap := &EnhancedCapability{
		Name: "test",
		Parameters: []Parameter{
			{Name: "lat", Type: "number", Required: true},
		},
	}

	ctx := context.Background()
	result, err := resolver.ResolveParameters(ctx, depResults, targetCap, "test-step", "")

	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}

	if result.Metadata == nil {
		t.Fatal("Expected metadata, got nil")
	}

	// Auto-wiring duration should be non-negative
	if result.Metadata.AutoWiringDurationUs < 0 {
		t.Errorf("Expected AutoWiringDurationUs >= 0, got %d", result.Metadata.AutoWiringDurationUs)
	}
}

// TestHybridResolver_MetadataMicroResolutionDuration tests that MicroResolutionDurationMs
// is tracked on successful micro-resolution.
func TestHybridResolver_MetadataMicroResolutionDuration(t *testing.T) {
	aiClient := newMockAIClientWithResponse(map[string]interface{}{
		"lat": 48.85,
	})
	resolver := NewHybridResolver(aiClient, nil)

	// Source has mismatched name so micro-resolution is triggered
	depResults := map[string]*StepResult{
		"step-1": {
			StepID:   "step-1",
			Success:  true,
			Response: `{"latitude": 48.85}`,
		},
	}

	targetCap := &EnhancedCapability{
		Name: "test",
		Parameters: []Parameter{
			{Name: "lat", Type: "number", Required: true},
		},
	}

	ctx := context.Background()
	result, err := resolver.ResolveParameters(ctx, depResults, targetCap, "test-step", "")

	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}

	if result.Metadata == nil {
		t.Fatal("Expected metadata, got nil")
	}

	// Micro-resolution was used, so duration should be non-negative
	if result.Metadata.MicroResolutionDurationMs < 0 {
		t.Errorf("Expected MicroResolutionDurationMs >= 0, got %d", result.Metadata.MicroResolutionDurationMs)
	}

	// AI client was called
	if len(aiClient.calls) == 0 {
		t.Error("Expected AI call for micro-resolution")
	}
}

// TestHybridResolver_MetadataMicroResolutionDurationOnFailure tests that
// MicroResolutionDurationMs is recorded even when micro-resolution fails.
// This validates the bug fix where the failure path was missing timing recording.
func TestHybridResolver_MetadataMicroResolutionDurationOnFailure(t *testing.T) {
	// Mock AI client that returns an error
	aiClient := &mockAIClient{
		generateFunc: func(ctx context.Context, prompt string, opts *core.AIOptions) (*core.AIResponse, error) {
			return nil, fmt.Errorf("LLM service unavailable")
		},
	}
	resolver := NewHybridResolver(aiClient, nil)

	// Source has mismatched name so micro-resolution is triggered
	depResults := map[string]*StepResult{
		"step-1": {
			StepID:   "step-1",
			Success:  true,
			Response: `{"latitude": 48.85}`,
		},
	}

	targetCap := &EnhancedCapability{
		Name: "test",
		Parameters: []Parameter{
			{Name: "lat", Type: "number", Required: true},
		},
	}

	ctx := context.Background()
	result, err := resolver.ResolveParameters(ctx, depResults, targetCap, "test-step", "")

	// Should NOT return error - gracefully degrades
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}

	if result.Metadata == nil {
		t.Fatal("Expected metadata, got nil")
	}

	// MicroResolutionDurationMs should still be recorded even on failure
	if result.Metadata.MicroResolutionDurationMs < 0 {
		t.Errorf("Expected MicroResolutionDurationMs >= 0 even on failure, got %d",
			result.Metadata.MicroResolutionDurationMs)
	}

	// Should have 0 micro-resolved since it failed
	if result.Metadata.MicroResolvedCount != 0 {
		t.Errorf("Expected 0 micro-resolved on failure, got %d", result.Metadata.MicroResolvedCount)
	}
}

// TestHybridResolver_MetadataDependencyStepIDs tests that DependencyStepIDs
// tracks all input dependency step IDs.
func TestHybridResolver_MetadataDependencyStepIDs(t *testing.T) {
	aiClient := &mockAIClient{}
	resolver := NewHybridResolver(aiClient, nil)

	depResults := map[string]*StepResult{
		"geocoding": {
			StepID:   "geocoding",
			Success:  true,
			Response: `{"lat": 48.85}`,
		},
		"country-info": {
			StepID:   "country-info",
			Success:  true,
			Response: `{"currency": "EUR"}`,
		},
	}

	targetCap := &EnhancedCapability{
		Name: "test",
		Parameters: []Parameter{
			{Name: "lat", Type: "number", Required: true},
			{Name: "currency", Type: "string", Required: true},
		},
	}

	ctx := context.Background()
	result, err := resolver.ResolveParameters(ctx, depResults, targetCap, "test-step", "")

	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}

	if result.Metadata == nil {
		t.Fatal("Expected metadata, got nil")
	}

	// Should have 2 dependency step IDs
	if len(result.Metadata.DependencyStepIDs) != 2 {
		t.Fatalf("Expected 2 dependency step IDs, got %d: %v",
			len(result.Metadata.DependencyStepIDs), result.Metadata.DependencyStepIDs)
	}

	// Check both IDs are present (order may vary due to map iteration)
	stepIDSet := make(map[string]bool)
	for _, id := range result.Metadata.DependencyStepIDs {
		stepIDSet[id] = true
	}
	if !stepIDSet["geocoding"] {
		t.Error("Expected 'geocoding' in DependencyStepIDs")
	}
	if !stepIDSet["country-info"] {
		t.Error("Expected 'country-info' in DependencyStepIDs")
	}
}

// TestHybridResolver_MetadataSourceDataKeyCount tests that SourceDataKeyCount
// reflects the number of unique keys across all dependency results.
func TestHybridResolver_MetadataSourceDataKeyCount(t *testing.T) {
	aiClient := &mockAIClient{}
	resolver := NewHybridResolver(aiClient, nil)

	depResults := map[string]*StepResult{
		"step-1": {
			StepID:   "step-1",
			Success:  true,
			Response: `{"lat": 48.85, "lon": 2.35, "city": "Paris"}`,
		},
	}

	targetCap := &EnhancedCapability{
		Name: "test",
		Parameters: []Parameter{
			{Name: "lat", Type: "number", Required: true},
		},
	}

	ctx := context.Background()
	result, err := resolver.ResolveParameters(ctx, depResults, targetCap, "test-step", "")

	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}

	if result.Metadata == nil {
		t.Fatal("Expected metadata, got nil")
	}

	// Source has 3 keys: lat, lon, city
	if result.Metadata.SourceDataKeyCount != 3 {
		t.Errorf("Expected SourceDataKeyCount=3, got %d", result.Metadata.SourceDataKeyCount)
	}
}

// TestHybridResolver_EmptySourceDataMetadata tests that empty source data returns
// a non-nil empty Parameters slice (for clean JSON serialization as [] not null).
func TestHybridResolver_EmptySourceDataMetadata(t *testing.T) {
	aiClient := &mockAIClient{}
	resolver := NewHybridResolver(aiClient, nil)

	depResults := map[string]*StepResult{}

	targetCap := &EnhancedCapability{
		Name: "test",
		Parameters: []Parameter{
			{Name: "lat", Type: "number", Required: true},
		},
	}

	ctx := context.Background()
	result, err := resolver.ResolveParameters(ctx, depResults, targetCap, "test-step", "")

	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}

	if result.Metadata == nil {
		t.Fatal("Expected metadata, got nil")
	}

	// Parameters should be non-nil empty slice (JSON [] not null)
	if result.Metadata.Parameters == nil {
		t.Error("Expected Metadata.Parameters to be non-nil empty slice, got nil")
	}
	if len(result.Metadata.Parameters) != 0 {
		t.Errorf("Expected 0 parameter entries, got %d", len(result.Metadata.Parameters))
	}

	// Counts should all be 0
	if result.Metadata.AutoWiredCount != 0 {
		t.Errorf("Expected AutoWiredCount=0, got %d", result.Metadata.AutoWiredCount)
	}
	if result.Metadata.MicroResolvedCount != 0 {
		t.Errorf("Expected MicroResolvedCount=0, got %d", result.Metadata.MicroResolvedCount)
	}
}

// TestHybridResolver_MixedLayerParameterEntries tests that when both auto-wire
// and micro-resolution contribute parameters, each entry has the correct Layer.
func TestHybridResolver_MixedLayerParameterEntries(t *testing.T) {
	// Mock AI returns "city" via micro-resolution
	aiClient := newMockAIClientWithResponse(map[string]interface{}{
		"city": "Tokyo",
	})
	resolver := NewHybridResolver(aiClient, nil)

	depResults := map[string]*StepResult{
		"step-1": {
			StepID:   "step-1",
			Success:  true,
			Response: `{"lat": 48.85}`, // Only "lat" - "city" must come from LLM
		},
	}

	targetCap := &EnhancedCapability{
		Name: "test",
		Parameters: []Parameter{
			{Name: "lat", Type: "number", Required: true},
			{Name: "city", Type: "string", Required: true},
		},
	}

	ctx := context.Background()
	result, err := resolver.ResolveParameters(ctx, depResults, targetCap, "test-step", "")

	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}

	if result.Metadata == nil {
		t.Fatal("Expected metadata, got nil")
	}

	// Should have 2 parameter entries: one auto_wire, one micro_resolution
	if len(result.Metadata.Parameters) != 2 {
		t.Fatalf("Expected 2 parameter entries, got %d", len(result.Metadata.Parameters))
	}

	paramByName := make(map[string]ParameterResolution)
	for _, p := range result.Metadata.Parameters {
		paramByName[p.Name] = p
	}

	// "lat" should be auto_wire
	latR, ok := paramByName["lat"]
	if !ok {
		t.Fatal("Expected parameter entry for 'lat'")
	}
	if latR.Layer != "auto_wire" {
		t.Errorf("lat: expected Layer=\"auto_wire\", got %q", latR.Layer)
	}

	// "city" should be micro_resolution
	cityR, ok := paramByName["city"]
	if !ok {
		t.Fatal("Expected parameter entry for 'city'")
	}
	if cityR.Layer != "micro_resolution" {
		t.Errorf("city: expected Layer=\"micro_resolution\", got %q", cityR.Layer)
	}
	if cityR.MatchType != "semantic" {
		t.Errorf("city: expected MatchType=\"semantic\", got %q", cityR.MatchType)
	}
}

// TestHybridResolver_DependencyStepIDsWithEmptyResults tests that DependencyStepIDs
// includes step IDs even when no source data is collected (empty dependencies).
func TestHybridResolver_DependencyStepIDsWithEmptyResults(t *testing.T) {
	aiClient := &mockAIClient{}
	resolver := NewHybridResolver(aiClient, nil)

	// All steps have empty responses
	depResults := map[string]*StepResult{
		"step-a": {
			StepID:   "step-a",
			Success:  true,
			Response: "",
		},
		"step-b": {
			StepID:   "step-b",
			Success:  true,
			Response: "",
		},
	}

	targetCap := &EnhancedCapability{
		Name: "test",
		Parameters: []Parameter{
			{Name: "x", Type: "string", Required: true},
		},
	}

	ctx := context.Background()
	result, err := resolver.ResolveParameters(ctx, depResults, targetCap, "test-step", "")

	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}

	if result.Metadata == nil {
		t.Fatal("Expected metadata, got nil")
	}

	// Even though no data was collected, dependency step IDs should be present
	if len(result.Metadata.DependencyStepIDs) != 2 {
		t.Errorf("Expected 2 dependency step IDs, got %d", len(result.Metadata.DependencyStepIDs))
	}

	// Source data key count should be 0 (no data parsed)
	if result.Metadata.SourceDataKeyCount != 0 {
		t.Errorf("Expected SourceDataKeyCount=0, got %d", result.Metadata.SourceDataKeyCount)
	}
}

// TestHybridResolver_InvalidJSONResponse tests handling of invalid JSON in step response.
func TestHybridResolver_InvalidJSONResponse(t *testing.T) {
	aiClient := &mockAIClient{}
	logger := &mockLogger{}
	resolver := NewHybridResolver(aiClient, logger)

	depResults := map[string]*StepResult{
		"step-1": {
			StepID:   "step-1",
			Success:  true,
			Response: `not valid json`,
		},
		"step-2": {
			StepID:   "step-2",
			Success:  true,
			Response: `{"lat": 48.85}`,
		},
	}

	targetCap := &EnhancedCapability{
		Name: "test",
		Parameters: []Parameter{
			{Name: "lat", Type: "number", Required: true},
		},
	}

	ctx := context.Background()
	result, err := resolver.ResolveParameters(ctx, depResults, targetCap, "test-step", "")

	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}

	params := result.Parameters

	// Should still get lat from step-2
	if params["lat"].(float64) != 48.85 {
		t.Errorf("Expected lat=48.85 from step-2, got %v", params["lat"])
	}

	// Should have logged a warning about invalid JSON
	hasWarning := false
	for _, msg := range logger.messages {
		if msg == "Failed to parse step response for parameter resolution" {
			hasWarning = true
			break
		}
	}
	if !hasWarning {
		t.Error("Expected warning about invalid JSON, got none")
	}
}

// =============================================================================
// Model Override Propagation Tests
// =============================================================================

// TestHybridResolver_SetMicroResolutionModel_PropagatesToMicroResolver verifies
// that SetMicroResolutionModel propagates the model to the inner MicroResolver.
func TestHybridResolver_SetMicroResolutionModel_PropagatesToMicroResolver(t *testing.T) {
	t.Run("propagates model to MicroResolver", func(t *testing.T) {
		aiClient := &mockAIClient{}
		resolver := NewHybridResolver(aiClient, nil)

		resolver.SetMicroResolutionModel("fast")

		if resolver.microResolver.model != "fast" {
			t.Errorf("Expected microResolver.model='fast', got %q", resolver.microResolver.model)
		}
	})

	t.Run("empty model clears MicroResolver model", func(t *testing.T) {
		aiClient := &mockAIClient{}
		resolver := NewHybridResolver(aiClient, nil)

		resolver.SetMicroResolutionModel("fast")
		resolver.SetMicroResolutionModel("")

		if resolver.microResolver.model != "" {
			t.Errorf("Expected microResolver.model='', got %q", resolver.microResolver.model)
		}
	})

	t.Run("nil microResolver does not panic", func(t *testing.T) {
		// Create resolver without AI client — microResolver will be nil
		resolver := NewHybridResolver(nil, nil)

		// Should not panic
		resolver.SetMicroResolutionModel("fast")
	})
}

func TestHybridResolver_SetMicroResolutionMaxTokens_PropagatesToMicroResolver(t *testing.T) {
	t.Run("propagates maxTokens to MicroResolver", func(t *testing.T) {
		aiClient := &mockAIClient{}
		resolver := NewHybridResolver(aiClient, nil)

		resolver.SetMicroResolutionMaxTokens(4000)

		if resolver.microResolver.maxTokens != 4000 {
			t.Errorf("Expected microResolver.maxTokens=4000, got %d", resolver.microResolver.maxTokens)
		}
	})

	t.Run("zero maxTokens ignored", func(t *testing.T) {
		aiClient := &mockAIClient{}
		resolver := NewHybridResolver(aiClient, nil)

		resolver.SetMicroResolutionMaxTokens(0)

		// Should remain at default (2000)
		if resolver.microResolver.maxTokens != 2000 {
			t.Errorf("Expected microResolver.maxTokens=2000 (default), got %d", resolver.microResolver.maxTokens)
		}
	})

	t.Run("nil microResolver does not panic", func(t *testing.T) {
		resolver := NewHybridResolver(nil, nil)

		// Should not panic
		resolver.SetMicroResolutionMaxTokens(4000)
	})
}
