package orchestration

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/truvaagents/truva-g3/core"
)

// TieredTestAIClient is a mock AI client for tiered provider tests
type TieredTestAIClient struct {
	response     string
	err          error
	calls        []string
	lastOptions  *core.AIOptions
	model        string
	provider     string
	promptTokens int
	compTokens   int
}

func NewTieredTestAIClient() *TieredTestAIClient {
	return &TieredTestAIClient{
		model:        "test-model",
		provider:     "test-provider",
		promptTokens: 100,
		compTokens:   50,
	}
}

func (m *TieredTestAIClient) GenerateResponse(ctx context.Context, prompt string, options *core.AIOptions) (*core.AIResponse, error) {
	m.calls = append(m.calls, prompt)
	if options != nil {
		optsCopy := *options
		m.lastOptions = &optsCopy
	} else {
		m.lastOptions = nil
	}
	if m.err != nil {
		return nil, m.err
	}
	return &core.AIResponse{
		Content:  m.response,
		Model:    m.model,
		Provider: m.provider,
		Usage: core.TokenUsage{
			PromptTokens:     m.promptTokens,
			CompletionTokens: m.compTokens,
			TotalTokens:      m.promptTokens + m.compTokens,
		},
	}, nil
}

func (m *TieredTestAIClient) SetResponse(response string) {
	m.response = response
}

func (m *TieredTestAIClient) SetError(err error) {
	m.err = err
}

func (m *TieredTestAIClient) GetCalls() []string {
	return m.calls
}

func (m *TieredTestAIClient) LastOptions() *core.AIOptions {
	return m.lastOptions
}

// setupTestCatalog creates a catalog with the specified number of tools
func setupTestCatalog(toolCount int) *AgentCatalog {
	discovery := NewMockDiscovery()
	catalog := NewAgentCatalog(discovery)

	// Create capabilities
	caps := make([]core.Capability, toolCount)
	for i := 0; i < toolCount; i++ {
		caps[i] = core.Capability{
			Name:        fmt.Sprintf("capability_%d", i),
			Description: fmt.Sprintf("Test capability %d. This is a description.", i),
		}
	}

	// Register a test agent with all capabilities
	registration := &core.ServiceRegistration{
		ID:           "test-agent",
		Name:         "test-agent",
		Type:         core.ComponentTypeAgent,
		Description:  "Test agent",
		Address:      "localhost",
		Port:         8080,
		Capabilities: caps,
		Health:       core.HealthHealthy,
	}
	_ = discovery.Register(context.Background(), registration)
	_ = catalog.Refresh(context.Background())

	return catalog
}

// setupMultiAgentCatalog creates a catalog with multiple agents
func setupMultiAgentCatalog(agentCount, capsPerAgent int) *AgentCatalog {
	discovery := NewMockDiscovery()
	catalog := NewAgentCatalog(discovery)

	for a := 0; a < agentCount; a++ {
		caps := make([]core.Capability, capsPerAgent)
		for i := 0; i < capsPerAgent; i++ {
			caps[i] = core.Capability{
				Name:        fmt.Sprintf("capability_%d", i),
				Description: fmt.Sprintf("Capability %d of agent %d. This is useful.", i, a),
			}
		}

		registration := &core.ServiceRegistration{
			ID:           fmt.Sprintf("agent-%d", a),
			Name:         fmt.Sprintf("agent-%d", a),
			Type:         core.ComponentTypeAgent,
			Description:  fmt.Sprintf("Agent %d", a),
			Address:      "localhost",
			Port:         8080 + a,
			Capabilities: caps,
			Health:       core.HealthHealthy,
		}
		_ = discovery.Register(context.Background(), registration)
	}
	_ = catalog.Refresh(context.Background())

	return catalog
}

func TestTieredCapabilityProvider_BelowThreshold(t *testing.T) {
	// Setup: Create catalog with 15 tools (below default threshold of 20)
	catalog := setupTestCatalog(15)
	aiClient := NewTieredTestAIClient()

	provider := NewTieredCapabilityProvider(catalog, aiClient, nil)

	// Get capabilities
	capabilities, err := provider.GetCapabilities(context.Background(), "test request", nil)
	if err != nil {
		t.Fatalf("Failed to get capabilities: %v", err)
	}

	// Verify no LLM call was made (below threshold)
	if len(aiClient.GetCalls()) > 0 {
		t.Error("Expected no LLM calls below threshold, but calls were made")
	}

	// Verify full catalog returned
	if !strings.Contains(capabilities.FormattedInfo, "test-agent") {
		t.Error("Expected capabilities to contain test-agent")
	}
}

func TestTieredCapabilityProvider_AboveThreshold(t *testing.T) {
	// Setup: Create catalog with 30 tools (above default threshold of 20)
	catalog := setupTestCatalog(30)
	aiClient := NewTieredTestAIClient()

	// Mock AI client returns a selection of tools
	aiClient.SetResponse(`["test-agent/capability_0", "test-agent/capability_5"]`)

	provider := NewTieredCapabilityProvider(catalog, aiClient, nil)

	// Get capabilities
	capabilities, err := provider.GetCapabilities(context.Background(), "test request", nil)
	if err != nil {
		t.Fatalf("Failed to get capabilities: %v", err)
	}

	// Verify LLM call was made
	if len(aiClient.GetCalls()) == 0 {
		t.Error("Expected LLM call above threshold, but none was made")
	}

	// Verify only selected tools are in output
	if !strings.Contains(capabilities.FormattedInfo, "capability_0") {
		t.Error("Expected capabilities to contain capability_0")
	}
	if !strings.Contains(capabilities.FormattedInfo, "capability_5") {
		t.Error("Expected capabilities to contain capability_5")
	}
	// Verify non-selected tools are NOT in output
	if strings.Contains(capabilities.FormattedInfo, "capability_10") {
		t.Error("Expected capabilities to NOT contain capability_10 (not selected)")
	}
}

func TestTieredCapabilityProvider_HallucinationFiltering(t *testing.T) {
	// Setup: Create catalog with 25 tools
	catalog := setupTestCatalog(25)
	aiClient := NewTieredTestAIClient()

	// Mock AI client returns both real and fake tools
	aiClient.SetResponse(`["test-agent/capability_0", "fake-tool/fake_cap", "test-agent/capability_1"]`)

	provider := NewTieredCapabilityProvider(catalog, aiClient, nil)

	// Get capabilities
	capabilities, err := provider.GetCapabilities(context.Background(), "test request", nil)
	if err != nil {
		t.Fatalf("Failed to get capabilities: %v", err)
	}

	// Verify only valid tools returned
	if !strings.Contains(capabilities.FormattedInfo, "capability_0") {
		t.Error("Expected capabilities to contain capability_0")
	}
	if !strings.Contains(capabilities.FormattedInfo, "capability_1") {
		t.Error("Expected capabilities to contain capability_1")
	}
	// Verify fake tool is not included
	if strings.Contains(capabilities.FormattedInfo, "fake_cap") {
		t.Error("Expected fake tool to be filtered out")
	}
}

func TestTieredCapabilityProvider_FallbackOnError(t *testing.T) {
	// Setup: Create catalog with 25 tools
	catalog := setupTestCatalog(25)
	aiClient := NewTieredTestAIClient()

	// Mock AI client returns an error
	aiClient.SetError(errors.New("LLM service unavailable"))

	provider := NewTieredCapabilityProvider(catalog, aiClient, nil)

	// Get capabilities - should fall back to FormatForLLM
	capabilities, err := provider.GetCapabilities(context.Background(), "test request", nil)
	if err != nil {
		t.Fatalf("Expected graceful degradation, got error: %v", err)
	}

	// Verify full catalog returned as fallback
	if !strings.Contains(capabilities.FormattedInfo, "capability_0") {
		t.Error("Expected fallback to include all tools")
	}
	if !strings.Contains(capabilities.FormattedInfo, "capability_10") {
		t.Error("Expected fallback to include all tools")
	}
}

func TestTieredCapabilityProvider_EmptySelection(t *testing.T) {
	// Setup: Create catalog with 25 tools
	catalog := setupTestCatalog(25)
	aiClient := NewTieredTestAIClient()

	// Mock AI client returns empty array
	aiClient.SetResponse(`[]`)

	provider := NewTieredCapabilityProvider(catalog, aiClient, nil)

	// Get capabilities - should fall back since no valid tools
	capabilities, err := provider.GetCapabilities(context.Background(), "test request", nil)
	if err != nil {
		t.Fatalf("Expected graceful degradation on empty selection, got error: %v", err)
	}

	// Verify full catalog returned as fallback
	if !strings.Contains(capabilities.FormattedInfo, "capability_0") {
		t.Error("Expected fallback to include all tools")
	}
}

func TestTieredCapabilityProvider_AllHallucinations(t *testing.T) {
	// Setup: Create catalog with 25 tools
	catalog := setupTestCatalog(25)
	aiClient := NewTieredTestAIClient()

	// Mock AI client returns only fake tools
	aiClient.SetResponse(`["fake-tool/fake1", "fake-tool/fake2"]`)

	provider := NewTieredCapabilityProvider(catalog, aiClient, nil)

	// Get capabilities - should fall back since all selections are hallucinated
	capabilities, err := provider.GetCapabilities(context.Background(), "test request", nil)
	if err != nil {
		t.Fatalf("Expected graceful degradation when all selections hallucinated, got error: %v", err)
	}

	// Verify full catalog returned as fallback
	if !strings.Contains(capabilities.FormattedInfo, "capability_0") {
		t.Error("Expected fallback to include all tools")
	}
}

func TestTieredCapabilityProvider_CustomThreshold(t *testing.T) {
	// Setup: Create catalog with 15 tools
	catalog := setupTestCatalog(15)
	aiClient := NewTieredTestAIClient()

	// Set threshold to 10 so 15 tools will trigger tiering
	config := &TieredCapabilityConfig{
		MinToolsForTiering: 10,
	}

	aiClient.SetResponse(`["test-agent/capability_0"]`)

	provider := NewTieredCapabilityProvider(catalog, aiClient, config)

	// Get capabilities
	_, err := provider.GetCapabilities(context.Background(), "test request", nil)
	if err != nil {
		t.Fatalf("Failed to get capabilities: %v", err)
	}

	// Verify LLM call was made (custom threshold exceeded)
	if len(aiClient.GetCalls()) == 0 {
		t.Error("Expected LLM call with custom threshold, but none was made")
	}
}

func TestTieredCapabilityProvider_SetAIOptionsOverride_PropagatesToGenerateResponse(t *testing.T) {
	catalog := setupTestCatalog(30)
	aiClient := NewTieredTestAIClient()
	aiClient.SetResponse(`["test-agent/capability_0"]`)

	provider := NewTieredCapabilityProvider(catalog, aiClient, &TieredCapabilityConfig{
		MinToolsForTiering: 10,
	})
	provider.SetAIOptionsOverride(&AIOptionsOverride{
		Model:           StringPtr("smart"),
		Temperature:     Float32Ptr(0),
		MaxTokens:       IntPtr(2222),
		ReasoningEffort: StringPtr("none"),
		ResponseFormat:  StringPtr("json"),
	})

	_, err := provider.GetCapabilities(context.Background(), "test request", nil)
	if err != nil {
		t.Fatalf("GetCapabilities returned error: %v", err)
	}

	opts := aiClient.LastOptions()
	if opts == nil {
		t.Fatal("expected tiered selection to call AI client with options")
	}
	if opts.Model != "smart" || opts.Temperature != 0 || opts.MaxTokens != 2222 || opts.ReasoningEffort != "none" || opts.ResponseFormat != "json" {
		t.Fatalf("unexpected AI options propagated to tiered selection: %+v", *opts)
	}
}

func TestTieredCapabilityProvider_EnvVarThreshold(t *testing.T) {
	// Set environment variable
	os.Setenv("TRUVAG3_TIERED_MIN_TOOLS", "10")
	defer os.Unsetenv("TRUVAG3_TIERED_MIN_TOOLS")

	// Setup: Create catalog with 15 tools
	catalog := setupTestCatalog(15)
	aiClient := NewTieredTestAIClient()
	aiClient.SetResponse(`["test-agent/capability_0"]`)

	// Create provider without explicit config - should use env var
	provider := NewTieredCapabilityProvider(catalog, aiClient, nil)

	// Verify threshold was set from env var
	if provider.MinToolsForTiering != 10 {
		t.Errorf("Expected MinToolsForTiering=10 from env var, got %d", provider.MinToolsForTiering)
	}

	// Get capabilities
	_, err := provider.GetCapabilities(context.Background(), "test request", nil)
	if err != nil {
		t.Fatalf("Failed to get capabilities: %v", err)
	}

	// Verify LLM call was made (env var threshold exceeded)
	if len(aiClient.GetCalls()) == 0 {
		t.Error("Expected LLM call with env var threshold, but none was made")
	}
}

func TestTieredCapabilityProvider_ConfigPrecedence(t *testing.T) {
	// Set environment variable
	os.Setenv("TRUVAG3_TIERED_MIN_TOOLS", "10")
	defer os.Unsetenv("TRUVAG3_TIERED_MIN_TOOLS")

	// Create provider with explicit config that should override env var
	config := &TieredCapabilityConfig{
		MinToolsForTiering: 25,
	}

	catalog := setupTestCatalog(1)
	aiClient := NewTieredTestAIClient()

	provider := NewTieredCapabilityProvider(catalog, aiClient, config)

	// Verify explicit config takes precedence over env var
	if provider.MinToolsForTiering != 25 {
		t.Errorf("Expected explicit config (25) to override env var (10), got %d", provider.MinToolsForTiering)
	}
}

func TestTieredCapabilityProvider_StructuredPrompt(t *testing.T) {
	// Setup: Create catalog with 25 tools
	catalog := setupTestCatalog(25)
	aiClient := NewTieredTestAIClient()
	aiClient.SetResponse(`["test-agent/capability_0"]`)

	provider := NewTieredCapabilityProvider(catalog, aiClient, nil)

	// Get capabilities
	_, err := provider.GetCapabilities(context.Background(), "Get weather for NYC", nil)
	if err != nil {
		t.Fatalf("Failed to get capabilities: %v", err)
	}

	// Verify prompt structure follows Guided-Structured Templates
	calls := aiClient.GetCalls()
	if len(calls) == 0 {
		t.Fatal("Expected LLM call to be made")
	}

	prompt := calls[0]

	// Check for structured sections
	if !strings.Contains(prompt, "<identity>") {
		t.Error("Expected prompt to contain <identity> tag")
	}
	if !strings.Contains(prompt, "<available_tools>") {
		t.Error("Expected prompt to contain <available_tools> tag")
	}
	if !strings.Contains(prompt, "<user_request>") {
		t.Error("Expected prompt to contain <user_request> tag")
	}
	if !strings.Contains(prompt, "<selection_guide>") {
		t.Error("Expected prompt to contain <selection_guide> tag")
	}
	if !strings.Contains(prompt, "Get weather for NYC") {
		t.Error("Expected prompt to contain user request")
	}
}

func TestTieredCapabilityProvider_MultiAgent(t *testing.T) {
	// Setup: Create catalog with 3 agents, 10 capabilities each (30 total)
	catalog := setupMultiAgentCatalog(3, 10)
	aiClient := NewTieredTestAIClient()

	// Select tools from different agents
	aiClient.SetResponse(`["agent-0/capability_0", "agent-1/capability_5", "agent-2/capability_9"]`)

	provider := NewTieredCapabilityProvider(catalog, aiClient, nil)

	// Get capabilities
	capabilities, err := provider.GetCapabilities(context.Background(), "test request", nil)
	if err != nil {
		t.Fatalf("Failed to get capabilities: %v", err)
	}

	// Verify tools from all selected agents are present
	if !strings.Contains(capabilities.FormattedInfo, "agent-0") {
		t.Error("Expected capabilities to contain agent-0")
	}
	if !strings.Contains(capabilities.FormattedInfo, "agent-1") {
		t.Error("Expected capabilities to contain agent-1")
	}
	if !strings.Contains(capabilities.FormattedInfo, "agent-2") {
		t.Error("Expected capabilities to contain agent-2")
	}
}

func TestTieredCapabilityProvider_ParseToolSelection_MarkdownWrapped(t *testing.T) {
	catalog := setupTestCatalog(25)
	aiClient := NewTieredTestAIClient()

	// Test markdown-wrapped JSON response
	aiClient.SetResponse("```json\n[\"test-agent/capability_0\"]\n```")

	provider := NewTieredCapabilityProvider(catalog, aiClient, nil)

	capabilities, err := provider.GetCapabilities(context.Background(), "test request", nil)
	if err != nil {
		t.Fatalf("Failed to parse markdown-wrapped response: %v", err)
	}

	if !strings.Contains(capabilities.FormattedInfo, "capability_0") {
		t.Error("Expected capabilities to contain capability_0")
	}
}

func TestTieredCapabilityProvider_InvalidJSON(t *testing.T) {
	catalog := setupTestCatalog(25)
	aiClient := NewTieredTestAIClient()

	// Test invalid JSON response
	aiClient.SetResponse("not valid json")

	provider := NewTieredCapabilityProvider(catalog, aiClient, nil)

	// Should fall back gracefully
	capabilities, err := provider.GetCapabilities(context.Background(), "test request", nil)
	if err != nil {
		t.Fatalf("Expected graceful fallback on invalid JSON, got error: %v", err)
	}

	// Verify full catalog returned as fallback
	if !strings.Contains(capabilities.FormattedInfo, "capability_0") {
		t.Error("Expected fallback to include all tools")
	}
}

func TestTieredCapabilityProvider_SetLogger(t *testing.T) {
	catalog := setupTestCatalog(5)
	aiClient := NewTieredTestAIClient()

	provider := NewTieredCapabilityProvider(catalog, aiClient, nil)

	// Should not panic when setting logger
	provider.SetLogger(&core.NoOpLogger{})
}

func TestTieredCapabilityProvider_SetTelemetry(t *testing.T) {
	catalog := setupTestCatalog(5)
	aiClient := NewTieredTestAIClient()

	provider := NewTieredCapabilityProvider(catalog, aiClient, nil)

	// Should not panic when setting telemetry
	provider.SetTelemetry(&core.NoOpTelemetry{})
}

func TestTieredCapabilityProvider_Shutdown(t *testing.T) {
	catalog := setupTestCatalog(5)
	aiClient := NewTieredTestAIClient()

	provider := NewTieredCapabilityProvider(catalog, aiClient, nil)

	// Shutdown should complete without error
	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
	defer cancel()

	err := provider.Shutdown(ctx)
	if err != nil {
		t.Errorf("Expected clean shutdown, got error: %v", err)
	}
}

func TestTieredCapabilityProvider_ShutdownTimeout(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping shutdown-timeout test in short mode (uses real time.Sleep in mock debug store)")
	}
	catalog := setupTestCatalog(25)
	aiClient := NewTieredTestAIClient()
	aiClient.SetResponse(`["test-agent/capability_0"]`)

	// Create mock debug store that takes a long time
	mockStore := &mockSlowDebugStore{delay: 2 * time.Second}

	provider := NewTieredCapabilityProvider(catalog, aiClient, nil)
	provider.SetLLMDebugStore(mockStore)

	// Make a call to trigger debug recording
	_, _ = provider.GetCapabilities(context.Background(), "test", nil)

	// Shutdown with short timeout should timeout
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	err := provider.Shutdown(ctx)
	if err == nil {
		t.Error("Expected shutdown timeout error")
	}
}

// mockSlowDebugStore simulates a slow debug store for testing shutdown
type mockSlowDebugStore struct {
	delay time.Duration
}

func (m *mockSlowDebugStore) RecordInteraction(ctx context.Context, requestID string, interaction LLMInteraction) error {
	time.Sleep(m.delay)
	return nil
}

func (m *mockSlowDebugStore) GetRecord(ctx context.Context, requestID string) (*LLMDebugRecord, error) {
	return nil, nil
}

func (m *mockSlowDebugStore) SetMetadata(ctx context.Context, requestID string, key, value string) error {
	return nil
}

func (m *mockSlowDebugStore) ExtendTTL(ctx context.Context, requestID string, duration time.Duration) error {
	return nil
}

func (m *mockSlowDebugStore) ListRecent(ctx context.Context, limit int) ([]LLMDebugRecordSummary, error) {
	return nil, nil
}

// Test helper function for extractFirstSentences
func TestExtractFirstSentences(t *testing.T) {
	tests := []struct {
		text     string
		n        int
		expected string
	}{
		{"First. Second. Third.", 2, "First. Second."},
		{"Only one sentence", 2, "Only one sentence"},
		{"First! Second? Third.", 2, "First! Second?"},
		{"", 2, ""},
		{"First sentence. Second sentence. Third sentence. Fourth.", 3, "First sentence. Second sentence. Third sentence."},
		{"No periods here", 1, "No periods here"},
		{"  Spaces around.  ", 1, "Spaces around."},
	}

	for _, tc := range tests {
		result := extractFirstSentences(tc.text, tc.n)
		if result != tc.expected {
			t.Errorf("extractFirstSentences(%q, %d) = %q, expected %q",
				tc.text, tc.n, result, tc.expected)
		}
	}
}

// Test catalog extension methods
func TestAgentCatalog_GetCapabilitySummaries(t *testing.T) {
	catalog := setupTestCatalog(5)

	summaries := catalog.GetCapabilitySummaries()

	if len(summaries) != 5 {
		t.Errorf("Expected 5 summaries, got %d", len(summaries))
	}

	// Verify structure
	for _, s := range summaries {
		if s.AgentName == "" {
			t.Error("Expected AgentName to be set")
		}
		if s.CapabilityName == "" {
			t.Error("Expected CapabilityName to be set")
		}
		if s.Summary == "" {
			t.Error("Expected Summary to be generated")
		}
	}
}

func TestAgentCatalog_GetToolCount(t *testing.T) {
	catalog := setupTestCatalog(15)

	count := catalog.GetToolCount()
	if count != 15 {
		t.Errorf("Expected tool count 15, got %d", count)
	}
}

func TestAgentCatalog_FormatToolsForLLM(t *testing.T) {
	catalog := setupMultiAgentCatalog(3, 10) // 30 total tools

	// Select specific tools
	toolIDs := []string{"agent-0/capability_0", "agent-1/capability_5"}

	formatted := catalog.FormatToolsForLLM(toolIDs)

	// Verify selected tools are present
	if !strings.Contains(formatted, "capability_0") {
		t.Error("Expected formatted output to contain capability_0")
	}
	if !strings.Contains(formatted, "capability_5") {
		t.Error("Expected formatted output to contain capability_5")
	}

	// Verify non-selected tools are NOT present
	if strings.Contains(formatted, "capability_9") {
		t.Error("Expected formatted output to NOT contain capability_9")
	}
}

func TestAgentCatalog_FormatToolsForLLM_UnknownTools(t *testing.T) {
	catalog := setupTestCatalog(5)

	// Include an unknown tool
	toolIDs := []string{"test-agent/capability_0", "unknown-agent/unknown_cap"}

	formatted := catalog.FormatToolsForLLM(toolIDs)

	// Should include valid tool
	if !strings.Contains(formatted, "capability_0") {
		t.Error("Expected formatted output to contain capability_0")
	}

	// Should NOT include unknown tool (silently ignored)
	if strings.Contains(formatted, "unknown_cap") {
		t.Error("Expected unknown tool to be silently ignored")
	}
}

func TestEnhancedCapability_GetSummary(t *testing.T) {
	// Test with explicit Summary
	cap1 := EnhancedCapability{
		Name:        "test",
		Description: "This is a long description. With multiple sentences. And more text.",
		Summary:     "Custom summary",
	}
	if cap1.GetSummary() != "Custom summary" {
		t.Errorf("Expected custom summary, got %s", cap1.GetSummary())
	}

	// Test auto-generation from Description
	cap2 := EnhancedCapability{
		Name:        "test",
		Description: "First sentence. Second sentence. Third sentence.",
	}
	expected := "First sentence. Second sentence."
	if cap2.GetSummary() != expected {
		t.Errorf("Expected auto-generated summary %q, got %q", expected, cap2.GetSummary())
	}
}

// =============================================================================
// Circuit Breaker Integration Tests
// =============================================================================

// tieredTestCircuitBreaker implements core.CircuitBreaker for tiered provider tests
// (separate from mockCircuitBreaker in test_mocks.go to track additional fields)
type tieredTestCircuitBreaker struct {
	shouldOpen    bool // If true, Execute returns error
	executeCalled bool // Track if Execute was called
	callCount     int  // Number of Execute calls
}

func newTieredTestCircuitBreaker() *tieredTestCircuitBreaker {
	return &tieredTestCircuitBreaker{}
}

func (m *tieredTestCircuitBreaker) Execute(ctx context.Context, fn func() error) error {
	m.executeCalled = true
	m.callCount++

	if m.shouldOpen {
		return errors.New("circuit breaker is open")
	}

	// Execute the wrapped function
	return fn()
}

func (m *tieredTestCircuitBreaker) ExecuteWithTimeout(ctx context.Context, timeout time.Duration, fn func() error) error {
	return m.Execute(ctx, fn)
}

func (m *tieredTestCircuitBreaker) GetState() string {
	if m.shouldOpen {
		return "open"
	}
	return "closed"
}

func (m *tieredTestCircuitBreaker) GetMetrics() map[string]interface{} {
	return map[string]interface{}{
		"call_count": m.callCount,
	}
}

func (m *tieredTestCircuitBreaker) Reset() {
	m.callCount = 0
	m.shouldOpen = false
}

func (m *tieredTestCircuitBreaker) CanExecute() bool {
	return !m.shouldOpen
}

func TestTieredCapabilityProvider_CircuitBreakerIntegration(t *testing.T) {
	// Setup: Create catalog with 25 tools (above threshold)
	catalog := setupTestCatalog(25)
	aiClient := NewTieredTestAIClient()
	aiClient.SetResponse(`["test-agent/capability_0", "test-agent/capability_1"]`)

	cb := newTieredTestCircuitBreaker()
	provider := NewTieredCapabilityProvider(catalog, aiClient, nil)
	provider.SetCircuitBreaker(cb)

	// Get capabilities
	capabilities, err := provider.GetCapabilities(context.Background(), "test request", nil)
	if err != nil {
		t.Fatalf("Expected success, got error: %v", err)
	}

	// Verify circuit breaker was used
	if !cb.executeCalled {
		t.Error("Expected circuit breaker Execute to be called")
	}
	if cb.callCount != 1 {
		t.Errorf("Expected 1 circuit breaker call, got %d", cb.callCount)
	}

	// Verify capabilities were returned correctly
	if !strings.Contains(capabilities.FormattedInfo, "capability_0") {
		t.Error("Expected capabilities to contain capability_0")
	}
}

func TestTieredCapabilityProvider_CircuitBreakerOpen(t *testing.T) {
	// Setup: Create catalog with 25 tools (above threshold)
	catalog := setupTestCatalog(25)
	aiClient := NewTieredTestAIClient()
	aiClient.SetResponse(`["test-agent/capability_0"]`)

	cb := newTieredTestCircuitBreaker()
	cb.shouldOpen = true // Circuit is open - should reject

	provider := NewTieredCapabilityProvider(catalog, aiClient, nil)
	provider.SetCircuitBreaker(cb)

	// Get capabilities - should fall back gracefully when circuit is open
	capabilities, err := provider.GetCapabilities(context.Background(), "test request", nil)
	if err != nil {
		t.Fatalf("Expected graceful fallback, got error: %v", err)
	}

	// Verify fallback to FormatForLLM (all tools returned)
	if !strings.Contains(capabilities.FormattedInfo, "capability_0") {
		t.Error("Expected fallback to include capability_0")
	}
	if !strings.Contains(capabilities.FormattedInfo, "capability_10") {
		t.Error("Expected fallback to include capability_10 (all tools)")
	}
	if !strings.Contains(capabilities.FormattedInfo, "capability_20") {
		t.Error("Expected fallback to include capability_20 (all tools)")
	}
}

func TestTieredCapabilityProvider_WithoutCircuitBreaker(t *testing.T) {
	// Setup: Create catalog with 25 tools (above threshold)
	catalog := setupTestCatalog(25)
	aiClient := NewTieredTestAIClient()
	aiClient.SetResponse(`["test-agent/capability_0"]`)

	// Create provider WITHOUT circuit breaker
	provider := NewTieredCapabilityProvider(catalog, aiClient, nil)
	// Note: No SetCircuitBreaker call

	// Get capabilities - should work without circuit breaker
	capabilities, err := provider.GetCapabilities(context.Background(), "test request", nil)
	if err != nil {
		t.Fatalf("Expected success without circuit breaker, got error: %v", err)
	}

	// Verify LLM call was made directly
	if len(aiClient.GetCalls()) == 0 {
		t.Error("Expected LLM call to be made")
	}

	// Verify capabilities were returned
	if !strings.Contains(capabilities.FormattedInfo, "capability_0") {
		t.Error("Expected capabilities to contain capability_0")
	}
}

func TestTieredCapabilityProvider_CircuitBreakerErrorRecording(t *testing.T) {
	// Setup: Create catalog with 25 tools (above threshold)
	catalog := setupTestCatalog(25)
	aiClient := NewTieredTestAIClient()
	aiClient.SetResponse(`["test-agent/capability_0"]`)

	cb := newTieredTestCircuitBreaker()
	mockStore := &tieredTestDebugStore{interactions: make([]LLMInteraction, 0)}

	provider := NewTieredCapabilityProvider(catalog, aiClient, nil)
	provider.SetCircuitBreaker(cb)
	provider.SetLLMDebugStore(mockStore)

	// Get capabilities
	_, err := provider.GetCapabilities(context.Background(), "test request", nil)
	if err != nil {
		t.Fatalf("Expected success, got error: %v", err)
	}

	// Use Shutdown() to wait for async debug recording (uses internal WaitGroup)
	// This is cleaner than time.Sleep and ensures deterministic behavior
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := provider.Shutdown(ctx); err != nil {
		t.Fatalf("Shutdown failed: %v", err)
	}

	// Verify debug interaction was recorded
	if len(mockStore.interactions) == 0 {
		t.Error("Expected debug interaction to be recorded")
	}
	if len(mockStore.interactions) > 0 && mockStore.interactions[0].Type != "tiered_selection" {
		t.Errorf("Expected type 'tiered_selection', got %s", mockStore.interactions[0].Type)
	}
}

// =============================================================================
// Context Cancellation Tests
// =============================================================================

func TestTieredCapabilityProvider_ContextCancellation(t *testing.T) {
	// Setup: Create catalog with 25 tools (above threshold)
	catalog := setupTestCatalog(25)
	aiClient := NewTieredTestAIClient()
	aiClient.SetResponse(`["test-agent/capability_0"]`)

	provider := NewTieredCapabilityProvider(catalog, aiClient, nil)

	// Create a cancelled context
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // Cancel immediately

	// Get capabilities with cancelled context - should fall back gracefully
	capabilities, err := provider.GetCapabilities(ctx, "test request", nil)
	if err != nil {
		t.Fatalf("Expected graceful fallback on cancelled context, got error: %v", err)
	}

	// Verify fallback returns all tools
	if !strings.Contains(capabilities.FormattedInfo, "capability_0") {
		t.Error("Expected fallback to include all tools")
	}
	if !strings.Contains(capabilities.FormattedInfo, "capability_10") {
		t.Error("Expected fallback to include capability_10")
	}
}

func TestTieredCapabilityProvider_ContextCancellationNoLLMCall(t *testing.T) {
	// Setup: Create catalog with 25 tools (above threshold)
	catalog := setupTestCatalog(25)
	aiClient := NewTieredTestAIClient()
	aiClient.SetResponse(`["test-agent/capability_0"]`)

	provider := NewTieredCapabilityProvider(catalog, aiClient, nil)

	// Create a cancelled context
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // Cancel immediately

	// Get capabilities with cancelled context
	_, _ = provider.GetCapabilities(ctx, "test request", nil)

	// Verify NO LLM call was made (context was checked before expensive operation)
	if len(aiClient.GetCalls()) > 0 {
		t.Error("Expected no LLM call when context is cancelled before selectRelevantTools")
	}
}

func TestTieredCapabilityProvider_ContextTimeout(t *testing.T) {
	// Setup: Create catalog with 25 tools (above threshold)
	catalog := setupTestCatalog(25)

	// Create a slow AI client
	slowAIClient := &slowTestAIClient{
		delay:    500 * time.Millisecond,
		response: `["test-agent/capability_0"]`,
	}

	provider := NewTieredCapabilityProvider(catalog, slowAIClient, nil)

	// Create a context with very short timeout
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()

	// Get capabilities - context will timeout
	capabilities, err := provider.GetCapabilities(ctx, "test request", nil)
	if err != nil {
		t.Fatalf("Expected graceful fallback on timeout, got error: %v", err)
	}

	// Verify fallback returns all tools
	if !strings.Contains(capabilities.FormattedInfo, "capability_0") {
		t.Error("Expected fallback to include all tools on timeout")
	}
}

// slowTestAIClient simulates a slow AI client for testing timeout behavior
type slowTestAIClient struct {
	delay    time.Duration
	response string
}

func (s *slowTestAIClient) GenerateResponse(ctx context.Context, prompt string, options *core.AIOptions) (*core.AIResponse, error) {
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-time.After(s.delay):
		return &core.AIResponse{
			Content:  s.response,
			Model:    "test-model",
			Provider: "test-provider",
		}, nil
	}
}

// tieredTestDebugStore is a simple in-memory debug store for tiered provider testing
type tieredTestDebugStore struct {
	interactions []LLMInteraction
	mu           sync.Mutex
}

func (m *tieredTestDebugStore) RecordInteraction(ctx context.Context, requestID string, interaction LLMInteraction) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.interactions = append(m.interactions, interaction)
	return nil
}

func (m *tieredTestDebugStore) GetRecord(ctx context.Context, requestID string) (*LLMDebugRecord, error) {
	return nil, nil
}

func (m *tieredTestDebugStore) SetMetadata(ctx context.Context, requestID string, key, value string) error {
	return nil
}

func (m *tieredTestDebugStore) ExtendTTL(ctx context.Context, requestID string, duration time.Duration) error {
	return nil
}

func (m *tieredTestDebugStore) ListRecent(ctx context.Context, limit int) ([]LLMDebugRecordSummary, error) {
	return nil, nil
}

// =============================================================================
// Error Wrapping Tests
// =============================================================================

func TestTieredCapabilityProvider_ErrorWrapping(t *testing.T) {
	// Setup: Create catalog with 25 tools (above threshold)
	catalog := setupTestCatalog(25)
	aiClient := NewTieredTestAIClient()

	// Set error to simulate LLM failure
	testErr := errors.New("LLM service unavailable")
	aiClient.SetError(testErr)

	provider := NewTieredCapabilityProvider(catalog, aiClient, nil)

	// Get capabilities - should fall back gracefully
	capabilities, err := provider.GetCapabilities(context.Background(), "test request with some context", nil)
	if err != nil {
		t.Fatalf("Expected graceful fallback, got error: %v", err)
	}

	// Verify fallback worked
	if !strings.Contains(capabilities.FormattedInfo, "capability_0") {
		t.Error("Expected fallback to include all tools")
	}
}

func TestTieredCapabilityProvider_TruncateRequest(t *testing.T) {
	tests := []struct {
		input    string
		maxLen   int
		expected string
	}{
		{"short", 10, "short"},
		{"exactly10!", 10, "exactly10!"},
		{"this is a long request that should be truncated", 20, "this is a long reque..."},
		{"", 10, ""},
		{"test", 0, "..."},
	}

	for _, tc := range tests {
		result := truncateRequest(tc.input, tc.maxLen)
		if result != tc.expected {
			t.Errorf("truncateRequest(%q, %d) = %q, expected %q",
				tc.input, tc.maxLen, result, tc.expected)
		}
	}
}

// =============================================================================
// SetCircuitBreaker Method Tests
// =============================================================================

func TestTieredCapabilityProvider_SetCircuitBreaker(t *testing.T) {
	catalog := setupTestCatalog(5)
	aiClient := NewTieredTestAIClient()
	provider := NewTieredCapabilityProvider(catalog, aiClient, nil)

	// Initially no circuit breaker
	cb := newTieredTestCircuitBreaker()
	provider.SetCircuitBreaker(cb)

	// Verify circuit breaker was set (by checking it's used in a call)
	aiClient.SetResponse(`["test-agent/capability_0"]`)

	// Below threshold - circuit breaker won't be used
	// But we can verify setting nil doesn't crash
	provider.SetCircuitBreaker(nil)

	// Should not panic
	_, err := provider.GetCapabilities(context.Background(), "test", nil)
	if err != nil {
		t.Errorf("Unexpected error with nil circuit breaker: %v", err)
	}
}

// ============================================================================
// ORCH-014: CustomInstructions in Tiered Selection Tests
// ============================================================================

// --- 5a: writeCustomInstructions helper — direct tests ---

func TestWriteCustomInstructions_WithInstructions(t *testing.T) {
	instructions := []string{
		"Always check for reusable scripts before generating new ones",
		"Use project_key QA for all JIRA tickets",
	}

	var sb strings.Builder
	writeCustomInstructions(&sb, instructions)
	result := sb.String()

	if !strings.Contains(result, "<custom_instructions>") {
		t.Error("Missing opening <custom_instructions> tag")
	}
	if !strings.Contains(result, "</custom_instructions>") {
		t.Error("Missing closing </custom_instructions> tag")
	}
	if !strings.Contains(result, "1. Always check for reusable scripts before generating new ones") {
		t.Error("Missing first numbered instruction")
	}
	if !strings.Contains(result, "2. Use project_key QA for all JIRA tickets") {
		t.Error("Missing second numbered instruction")
	}
}

func TestWriteCustomInstructions_Empty(t *testing.T) {
	var sb strings.Builder
	writeCustomInstructions(&sb, nil)

	if sb.Len() != 0 {
		t.Errorf("Expected empty output for nil instructions, got: %q", sb.String())
	}
}

func TestWriteCustomInstructions_EmptySlice(t *testing.T) {
	var sb strings.Builder
	writeCustomInstructions(&sb, []string{})

	if sb.Len() != 0 {
		t.Errorf("Expected empty output for empty slice instructions, got: %q", sb.String())
	}
}

func TestWriteCustomInstructions_SingleInstruction(t *testing.T) {
	instructions := []string{"Always check for reusable scripts"}

	var sb strings.Builder
	writeCustomInstructions(&sb, instructions)
	result := sb.String()

	if !strings.Contains(result, "1. Always check for reusable scripts") {
		t.Error("Missing numbered instruction")
	}
	if strings.Contains(result, "2.") {
		t.Error("Unexpected second instruction numbering")
	}
}

// --- 5b: buildSelectionPrompt — CustomInstructions injection (Phase 1) ---

func TestBuildSelectionPromptIncludesCustomInstructions(t *testing.T) {
	catalog := setupTestCatalog(5)
	aiClient := NewTieredTestAIClient()
	provider := NewTieredCapabilityProvider(catalog, aiClient, nil)
	provider.SetCustomInstructions([]string{
		"Always check for reusable scripts before generating new ones",
		"Use project_key QA for all JIRA tickets",
	})

	summaries := catalog.GetCapabilitySummaries()
	prompt := provider.buildSelectionPrompt(context.Background(), summaries, "Test https://example.com")

	if !strings.Contains(prompt, "<custom_instructions>") {
		t.Error("Selection prompt missing <custom_instructions> section")
	}
	if !strings.Contains(prompt, "1. Always check for reusable scripts") {
		t.Error("Selection prompt missing first custom instruction")
	}
	if !strings.Contains(prompt, "2. Use project_key QA") {
		t.Error("Selection prompt missing second custom instruction")
	}
}

func TestBuildSelectionPromptOmitsEmptyCustomInstructions(t *testing.T) {
	catalog := setupTestCatalog(5)
	aiClient := NewTieredTestAIClient()
	provider := NewTieredCapabilityProvider(catalog, aiClient, nil)

	summaries := catalog.GetCapabilitySummaries()
	prompt := provider.buildSelectionPrompt(context.Background(), summaries, "Test https://example.com")

	if strings.Contains(prompt, "<custom_instructions>") {
		t.Error("Selection prompt should not contain <custom_instructions> when empty")
	}
}

func TestBuildSelectionPrompt_CustomInstructionsPlacement(t *testing.T) {
	catalog := setupTestCatalog(5)
	aiClient := NewTieredTestAIClient()
	provider := NewTieredCapabilityProvider(catalog, aiClient, nil)
	provider.SetCustomInstructions([]string{"Check scripts first"})

	summaries := catalog.GetCapabilitySummaries()
	prompt := provider.buildSelectionPrompt(context.Background(), summaries, "Test site")

	guideEnd := strings.Index(prompt, "</selection_guide>")
	customStart := strings.Index(prompt, "<custom_instructions>")
	toolsStart := strings.Index(prompt, "<available_tools>")

	if guideEnd == -1 || customStart == -1 || toolsStart == -1 {
		t.Fatal("Missing expected XML sections in prompt")
	}
	if customStart < guideEnd {
		t.Error("<custom_instructions> should appear AFTER </selection_guide>")
	}
	if customStart > toolsStart {
		t.Error("<custom_instructions> should appear BEFORE <available_tools>")
	}
}

// --- 5c: buildContinuationSelectionPrompt — CustomInstructions injection (Phase 2+) ---

func TestBuildContinuationSelectionPromptIncludesCustomInstructions(t *testing.T) {
	catalog := setupTestCatalog(5)
	aiClient := NewTieredTestAIClient()
	provider := NewTieredCapabilityProvider(catalog, aiClient, nil)
	provider.SetCustomInstructions([]string{
		"Always check for reusable scripts before generating new ones",
	})

	summaries := catalog.GetCapabilitySummaries()
	phaseContext := map[string]interface{}{
		PhaseContextKeyPriorToolsUsed: []string{"playwright-tool/explore_page"},
	}
	prompt := provider.buildContinuationSelectionPrompt(context.Background(), summaries, "Test site", phaseContext)

	if !strings.Contains(prompt, "<custom_instructions>") {
		t.Error("Continuation selection prompt missing <custom_instructions> section")
	}
	if !strings.Contains(prompt, "1. Always check for reusable scripts") {
		t.Error("Continuation selection prompt missing numbered instruction")
	}
}

func TestBuildContinuationSelectionPromptOmitsEmptyCustomInstructions(t *testing.T) {
	catalog := setupTestCatalog(5)
	aiClient := NewTieredTestAIClient()
	provider := NewTieredCapabilityProvider(catalog, aiClient, nil)

	summaries := catalog.GetCapabilitySummaries()
	phaseContext := map[string]interface{}{
		PhaseContextKeyPriorToolsUsed: []string{"playwright-tool/explore_page"},
	}
	prompt := provider.buildContinuationSelectionPrompt(context.Background(), summaries, "Test site", phaseContext)

	if strings.Contains(prompt, "<custom_instructions>") {
		t.Error("Continuation prompt should not contain <custom_instructions> when empty")
	}
}

func TestBuildContinuationSelectionPrompt_CustomInstructionsPlacement(t *testing.T) {
	catalog := setupTestCatalog(5)
	aiClient := NewTieredTestAIClient()
	provider := NewTieredCapabilityProvider(catalog, aiClient, nil)
	provider.SetCustomInstructions([]string{"Check scripts first"})

	summaries := catalog.GetCapabilitySummaries()
	phaseContext := map[string]interface{}{
		PhaseContextKeyPriorToolsUsed: []string{"tool-a/cap_1"},
	}
	prompt := provider.buildContinuationSelectionPrompt(context.Background(), summaries, "Test site", phaseContext)

	phaseEnd := strings.Index(prompt, "</phase_context>")
	customStart := strings.Index(prompt, "<custom_instructions>")
	toolsStart := strings.Index(prompt, "<available_tools>")

	if phaseEnd == -1 || customStart == -1 || toolsStart == -1 {
		t.Fatal("Missing expected XML sections in prompt")
	}
	if customStart < phaseEnd {
		t.Error("<custom_instructions> should appear AFTER </phase_context>")
	}
	if customStart > toolsStart {
		t.Error("<custom_instructions> should appear BEFORE <available_tools>")
	}
}

// --- 5c-conv: Conversation history injection tests ---

func TestBuildSelectionPrompt_IncludesConversationHistory(t *testing.T) {
	catalog := setupTestCatalog(25)
	aiClient := &mockAIClient{}
	provider := NewTieredCapabilityProvider(catalog, aiClient, nil)

	summaries := catalog.GetCapabilitySummaries()

	history := "User: What is KubeCon?\nAssistant: KubeCon is the CNCF flagship conference."
	ctx := core.WithPipelineEnrichments(context.Background(), map[string]interface{}{
		core.EnrichmentConversationHistory: history,
	})

	prompt := provider.buildSelectionPrompt(ctx, summaries, "Can you check from the official website?")

	if !strings.Contains(prompt, "<conversation_history>") {
		t.Error("Selection prompt should contain <conversation_history> when enrichments provide it")
	}
	if !strings.Contains(prompt, history) {
		t.Error("Selection prompt should contain the actual conversation history text")
	}

	// Verify placement: conversation_history after </available_tools>, before <user_request>
	toolsEnd := strings.Index(prompt, "</available_tools>")
	convStart := strings.Index(prompt, "<conversation_history>")
	requestStart := strings.Index(prompt, "<user_request>")
	if toolsEnd == -1 || convStart == -1 || requestStart == -1 {
		t.Fatal("Missing expected XML sections in prompt")
	}
	if convStart < toolsEnd {
		t.Error("<conversation_history> should appear AFTER </available_tools>")
	}
	if convStart > requestStart {
		t.Error("<conversation_history> should appear BEFORE <user_request>")
	}
}

func TestBuildSelectionPrompt_OmitsConversationHistoryWhenAbsent(t *testing.T) {
	catalog := setupTestCatalog(25)
	aiClient := &mockAIClient{}
	provider := NewTieredCapabilityProvider(catalog, aiClient, nil)

	summaries := catalog.GetCapabilitySummaries()

	// No enrichments in context
	prompt := provider.buildSelectionPrompt(context.Background(), summaries, "test request")

	if strings.Contains(prompt, "<conversation_history>") {
		t.Error("Selection prompt should NOT contain <conversation_history> when no enrichments")
	}
}

func TestBuildContinuationSelectionPrompt_IncludesConversationHistory(t *testing.T) {
	catalog := setupTestCatalog(25)
	aiClient := &mockAIClient{}
	provider := NewTieredCapabilityProvider(catalog, aiClient, nil)

	summaries := catalog.GetCapabilitySummaries()
	phaseContext := map[string]interface{}{
		PhaseContextKeyPhaseNumber:    2,
		PhaseContextKeyPriorToolsUsed: []string{"research-tool"},
	}

	history := "User: Find flights to Salt Lake City\nAssistant: Here are available flights..."
	ctx := core.WithPipelineEnrichments(context.Background(), map[string]interface{}{
		core.EnrichmentConversationHistory: history,
	})

	prompt := provider.buildContinuationSelectionPrompt(ctx, summaries, "Now book the cheapest one", phaseContext)

	if !strings.Contains(prompt, "<conversation_history>") {
		t.Error("Continuation prompt should contain <conversation_history> when enrichments provide it")
	}
	if !strings.Contains(prompt, history) {
		t.Error("Continuation prompt should contain the actual conversation history text")
	}

	// Verify placement: after </available_tools>, before <user_request>
	toolsEnd := strings.Index(prompt, "</available_tools>")
	convStart := strings.Index(prompt, "<conversation_history>")
	requestStart := strings.Index(prompt, "<user_request>")
	if toolsEnd == -1 || convStart == -1 || requestStart == -1 {
		t.Fatal("Missing expected XML sections in prompt")
	}
	if convStart < toolsEnd {
		t.Error("<conversation_history> should appear AFTER </available_tools>")
	}
	if convStart > requestStart {
		t.Error("<conversation_history> should appear BEFORE <user_request>")
	}
}

// --- 5d: Integration — CustomInstructions propagated through GetCapabilities ---

func TestTieredCapabilityProvider_CustomInstructionsInLLMPrompt(t *testing.T) {
	catalog := setupTestCatalog(25)
	aiClient := NewTieredTestAIClient()
	aiClient.SetResponse(`["test-agent/capability_0", "test-agent/capability_1"]`)

	provider := NewTieredCapabilityProvider(catalog, aiClient, nil)
	provider.SetCustomInstructions([]string{
		"Always check for reusable test scripts for the target hostname",
	})

	_, err := provider.GetCapabilities(context.Background(), "Test https://example.com", nil)
	if err != nil {
		t.Fatalf("GetCapabilities failed: %v", err)
	}

	calls := aiClient.GetCalls()
	if len(calls) == 0 {
		t.Fatal("Expected LLM call, got none")
	}
	prompt := calls[0]
	if !strings.Contains(prompt, "<custom_instructions>") {
		t.Error("LLM prompt missing <custom_instructions> section")
	}
	if !strings.Contains(prompt, "Always check for reusable test scripts") {
		t.Error("LLM prompt missing custom instruction text")
	}
}

func TestTieredCapabilityProvider_NoCustomInstructionsInLLMPrompt(t *testing.T) {
	catalog := setupTestCatalog(25)
	aiClient := NewTieredTestAIClient()
	aiClient.SetResponse(`["test-agent/capability_0"]`)

	provider := NewTieredCapabilityProvider(catalog, aiClient, nil)

	_, err := provider.GetCapabilities(context.Background(), "test request", nil)
	if err != nil {
		t.Fatalf("GetCapabilities failed: %v", err)
	}

	calls := aiClient.GetCalls()
	if len(calls) == 0 {
		t.Fatal("Expected LLM call, got none")
	}
	if strings.Contains(calls[0], "<custom_instructions>") {
		t.Error("LLM prompt should NOT contain <custom_instructions> when none configured")
	}
}

// --- 5e: Observability — custom_instructions_count in log output ---

func TestTieredCapabilityProvider_LogsCustomInstructionsCount(t *testing.T) {
	catalog := setupTestCatalog(25)
	aiClient := NewTieredTestAIClient()
	aiClient.SetResponse(`["test-agent/capability_0"]`)

	var capturedFields map[string]interface{}
	logger := &mockLogger{
		infoFunc: func(msg string, fields map[string]interface{}) {
			if msg == "Tier 1 tool selection complete" {
				capturedFields = fields
			}
		},
	}

	provider := NewTieredCapabilityProvider(catalog, aiClient, nil)
	provider.SetLogger(logger)
	provider.SetCustomInstructions([]string{
		"Always check for reusable scripts",
		"Use project_key QA for JIRA",
	})

	_, err := provider.GetCapabilities(context.Background(), "test request", nil)
	if err != nil {
		t.Fatalf("GetCapabilities failed: %v", err)
	}

	if capturedFields == nil {
		t.Fatal("Expected 'Tier 1 tool selection complete' log, got none")
	}
	count, ok := capturedFields["custom_instructions_count"]
	if !ok {
		t.Error("Log missing 'custom_instructions_count' field")
	} else if count != 2 {
		t.Errorf("Expected custom_instructions_count=2, got %v", count)
	}
}

func TestTieredCapabilityProvider_LogsZeroCustomInstructionsCount(t *testing.T) {
	catalog := setupTestCatalog(25)
	aiClient := NewTieredTestAIClient()
	aiClient.SetResponse(`["test-agent/capability_0"]`)

	var capturedFields map[string]interface{}
	logger := &mockLogger{
		infoFunc: func(msg string, fields map[string]interface{}) {
			if msg == "Tier 1 tool selection complete" {
				capturedFields = fields
			}
		},
	}

	provider := NewTieredCapabilityProvider(catalog, aiClient, nil)
	provider.SetLogger(logger)

	_, err := provider.GetCapabilities(context.Background(), "test request", nil)
	if err != nil {
		t.Fatalf("GetCapabilities failed: %v", err)
	}

	if capturedFields == nil {
		t.Fatal("Expected 'Tier 1 tool selection complete' log, got none")
	}
	count, ok := capturedFields["custom_instructions_count"]
	if !ok {
		t.Error("Log missing 'custom_instructions_count' field")
	} else if count != 0 {
		t.Errorf("Expected custom_instructions_count=0, got %v", count)
	}
}

// ============================================================================
// Tiered Selection Retry Tests
// ============================================================================

// sequentialAIClient returns different responses on successive calls.
// Used to test retry behavior where the first call fails but a subsequent one succeeds.
type sequentialAIClient struct {
	responses []string // responses to return in order; last one repeats
	errors    []error  // errors to return in order; nil = no error; last one repeats
	callIdx   int
	calls     []string
	model     string
	provider  string
}

func newSequentialAIClient(responses []string, errors []error) *sequentialAIClient {
	return &sequentialAIClient{
		responses: responses,
		errors:    errors,
		model:     "test-model",
		provider:  "test-provider",
	}
}

func (s *sequentialAIClient) GenerateResponse(ctx context.Context, prompt string, options *core.AIOptions) (*core.AIResponse, error) {
	s.calls = append(s.calls, prompt)
	idx := s.callIdx
	s.callIdx++

	// Pick error for this call
	var err error
	if len(s.errors) > 0 {
		if idx < len(s.errors) {
			err = s.errors[idx]
		} else {
			err = s.errors[len(s.errors)-1]
		}
	}
	if err != nil {
		return nil, err
	}

	// Pick response for this call
	var resp string
	if idx < len(s.responses) {
		resp = s.responses[idx]
	} else if len(s.responses) > 0 {
		resp = s.responses[len(s.responses)-1]
	}

	return &core.AIResponse{
		Content:  resp,
		Model:    s.model,
		Provider: s.provider,
		Usage: core.TokenUsage{
			PromptTokens:     100,
			CompletionTokens: 50,
			TotalTokens:      150,
		},
	}, nil
}

// --- 5a: Empty response detection tests ---

func TestTieredSelection_EmptyResponse_DetectedAndRetried(t *testing.T) {
	catalog := setupTestCatalog(30)
	aiClient := newSequentialAIClient(
		[]string{"", `["test-agent/capability_0"]`}, // empty first, valid second
		[]error{nil, nil},
	)

	config := &TieredCapabilityConfig{RetryEnabled: true, MaxRetries: 2}
	provider := NewTieredCapabilityProvider(catalog, aiClient, config)

	mockStore := &tieredTestDebugStore{interactions: make([]LLMInteraction, 0)}
	provider.SetLLMDebugStore(mockStore)

	_, err := provider.GetCapabilities(context.Background(), "test request", nil)
	if err != nil {
		t.Fatalf("Expected retry to succeed, got error: %v", err)
	}
	_ = provider.Shutdown(context.Background()) // Wait for async debug recordings

	// Verify 2 LLM calls were made (1 empty + 1 success)
	if len(aiClient.calls) != 2 {
		t.Errorf("Expected 2 LLM calls, got %d", len(aiClient.calls))
	}

	// Verify debug store has 2 interactions: first failed, second succeeded
	mockStore.mu.Lock()
	defer mockStore.mu.Unlock()
	if len(mockStore.interactions) < 2 {
		t.Fatalf("Expected at least 2 debug interactions, got %d", len(mockStore.interactions))
	}

	// Debug recording is async, so ordering is not guaranteed.
	foundEmpty := false
	for _, inter := range mockStore.interactions {
		if inter.Error == errTieredSelectionEmptyResponse {
			foundEmpty = true
			if inter.Success {
				t.Error("Empty response interaction should have Success=false")
			}
			break
		}
	}
	if !foundEmpty {
		t.Error("Expected a debug interaction with Error='empty_response'")
	}
	// Find the successful interaction
	found := false
	for _, inter := range mockStore.interactions {
		if inter.Success {
			found = true
			if inter.Attempt != 2 {
				t.Errorf("Successful interaction should be Attempt=2, got %d", inter.Attempt)
			}
			break
		}
	}
	if !found {
		t.Error("Expected a successful debug interaction after retry")
	}
}

func TestTieredSelection_WhitespaceResponse_DetectedAndRetried(t *testing.T) {
	catalog := setupTestCatalog(30)
	aiClient := newSequentialAIClient(
		[]string{"   \n\t  ", `["test-agent/capability_0"]`},
		[]error{nil, nil},
	)

	config := &TieredCapabilityConfig{RetryEnabled: true, MaxRetries: 2}
	provider := NewTieredCapabilityProvider(catalog, aiClient, config)

	_, err := provider.GetCapabilities(context.Background(), "test request", nil)
	if err != nil {
		t.Fatalf("Expected retry to succeed, got error: %v", err)
	}
	if len(aiClient.calls) != 2 {
		t.Errorf("Expected 2 LLM calls (whitespace detected as empty), got %d", len(aiClient.calls))
	}
}

func TestTieredSelection_EmptyResponse_RetryDisabled_ImmediateError(t *testing.T) {
	catalog := setupTestCatalog(30)
	aiClient := newSequentialAIClient(
		[]string{""},
		[]error{nil},
	)

	config := &TieredCapabilityConfig{RetryEnabled: false, MaxRetries: 0}
	provider := NewTieredCapabilityProvider(catalog, aiClient, config)

	// GetCapabilities should fall back (not error) since selectRelevantTools returns error
	// and GetCapabilities catches it with fallback
	_, err := provider.GetCapabilities(context.Background(), "test request", nil)
	if err != nil {
		t.Fatalf("Expected graceful fallback, got error: %v", err)
	}

	// Only 1 call — no retry
	if len(aiClient.calls) != 1 {
		t.Errorf("Expected exactly 1 LLM call (no retry), got %d", len(aiClient.calls))
	}
}

func TestTieredSelection_EmptyResponse_ExhaustsRetries(t *testing.T) {
	catalog := setupTestCatalog(30)
	// All 3 attempts return empty
	aiClient := newSequentialAIClient(
		[]string{"", "", ""},
		[]error{nil, nil, nil},
	)

	config := &TieredCapabilityConfig{RetryEnabled: true, MaxRetries: 2}
	provider := NewTieredCapabilityProvider(catalog, aiClient, config)

	mockStore := &tieredTestDebugStore{interactions: make([]LLMInteraction, 0)}
	provider.SetLLMDebugStore(mockStore)

	// Should fall back gracefully (GetCapabilities catches the error)
	_, err := provider.GetCapabilities(context.Background(), "test request", nil)
	if err != nil {
		t.Fatalf("Expected graceful fallback, got error: %v", err)
	}
	_ = provider.Shutdown(context.Background())

	// All 3 attempts should have been made
	if len(aiClient.calls) != 3 {
		t.Errorf("Expected 3 LLM calls, got %d", len(aiClient.calls))
	}

	// All debug interactions should be failures
	mockStore.mu.Lock()
	defer mockStore.mu.Unlock()
	for i, inter := range mockStore.interactions {
		if inter.Success {
			t.Errorf("Interaction %d should have Success=false", i)
		}
	}
}

// --- 5b: Parse failure retry tests ---

func TestTieredSelection_ParseFailure_Retried(t *testing.T) {
	catalog := setupTestCatalog(30)
	aiClient := newSequentialAIClient(
		[]string{"not valid json", `["test-agent/capability_0"]`},
		[]error{nil, nil},
	)

	config := &TieredCapabilityConfig{RetryEnabled: true, MaxRetries: 2}
	provider := NewTieredCapabilityProvider(catalog, aiClient, config)

	_, err := provider.GetCapabilities(context.Background(), "test request", nil)
	if err != nil {
		t.Fatalf("Expected retry to succeed after parse failure, got error: %v", err)
	}
	if len(aiClient.calls) != 2 {
		t.Errorf("Expected 2 LLM calls (parse fail + success), got %d", len(aiClient.calls))
	}
}

func TestTieredSelection_ParseFailure_SucceedsOnSecondAttempt(t *testing.T) {
	catalog := setupTestCatalog(30)
	aiClient := newSequentialAIClient(
		[]string{"{invalid", `["test-agent/capability_0", "test-agent/capability_5"]`},
		[]error{nil, nil},
	)

	config := &TieredCapabilityConfig{RetryEnabled: true, MaxRetries: 1}
	provider := NewTieredCapabilityProvider(catalog, aiClient, config)

	mockStore := &tieredTestDebugStore{interactions: make([]LLMInteraction, 0)}
	provider.SetLLMDebugStore(mockStore)

	caps, err := provider.GetCapabilities(context.Background(), "test request", nil)
	if err != nil {
		t.Fatalf("Expected success on second attempt, got error: %v", err)
	}
	_ = provider.Shutdown(context.Background()) // Wait for async debug recordings

	// Verify selected tools are present
	if !strings.Contains(caps.FormattedInfo, "capability_0") {
		t.Error("Expected capability_0 in output")
	}

	// First interaction should be failure, second success
	mockStore.mu.Lock()
	defer mockStore.mu.Unlock()
	if len(mockStore.interactions) < 2 {
		t.Fatalf("Expected at least 2 debug interactions, got %d", len(mockStore.interactions))
	}
	var sawFailure, sawSuccess bool
	for _, interaction := range mockStore.interactions {
		if interaction.Success {
			sawSuccess = true
		} else {
			sawFailure = true
		}
	}
	if !sawFailure {
		t.Error("Expected at least one failed debug interaction for the parse error attempt")
	}
	if !sawSuccess {
		t.Error("Expected at least one successful debug interaction for the retry attempt")
	}
}

func TestTieredSelection_AllRetriesFail_ReturnsError(t *testing.T) {
	catalog := setupTestCatalog(30)
	// All attempts return invalid JSON
	aiClient := newSequentialAIClient(
		[]string{"bad1", "bad2", "bad3"},
		[]error{nil, nil, nil},
	)

	config := &TieredCapabilityConfig{RetryEnabled: true, MaxRetries: 2}
	provider := NewTieredCapabilityProvider(catalog, aiClient, config)

	// Should fall back gracefully
	_, err := provider.GetCapabilities(context.Background(), "test request", nil)
	if err != nil {
		t.Fatalf("Expected graceful fallback, got error: %v", err)
	}

	// All 3 attempts should have been made
	if len(aiClient.calls) != 3 {
		t.Errorf("Expected 3 LLM calls, got %d", len(aiClient.calls))
	}
}

// --- 5c: Debug record accuracy tests ---

func TestTieredSelection_DebugRecord_EmptyResponse_SuccessFalse(t *testing.T) {
	catalog := setupTestCatalog(30)
	aiClient := newSequentialAIClient(
		[]string{"", `["test-agent/capability_0"]`},
		[]error{nil, nil},
	)

	config := &TieredCapabilityConfig{RetryEnabled: true, MaxRetries: 1}
	provider := NewTieredCapabilityProvider(catalog, aiClient, config)

	mockStore := &tieredTestDebugStore{interactions: make([]LLMInteraction, 0)}
	provider.SetLLMDebugStore(mockStore)

	_, _ = provider.GetCapabilities(context.Background(), "test request", nil)
	_ = provider.Shutdown(context.Background())

	mockStore.mu.Lock()
	defer mockStore.mu.Unlock()
	if len(mockStore.interactions) == 0 {
		t.Fatal("Expected debug interactions")
	}
	// Find the empty_response interaction (async ordering not guaranteed)
	found := false
	for _, inter := range mockStore.interactions {
		if inter.Error == errTieredSelectionEmptyResponse {
			found = true
			if inter.Success {
				t.Error("Empty response should record Success=false")
			}
			break
		}
	}
	if !found {
		t.Error("Expected a debug interaction with Error='empty_response'")
	}
}

func TestTieredSelection_DebugRecord_ParseFailure_SuccessFalse(t *testing.T) {
	catalog := setupTestCatalog(30)
	aiClient := newSequentialAIClient(
		[]string{"not json", `["test-agent/capability_0"]`},
		[]error{nil, nil},
	)

	config := &TieredCapabilityConfig{RetryEnabled: true, MaxRetries: 1}
	provider := NewTieredCapabilityProvider(catalog, aiClient, config)

	mockStore := &tieredTestDebugStore{interactions: make([]LLMInteraction, 0)}
	provider.SetLLMDebugStore(mockStore)

	_, _ = provider.GetCapabilities(context.Background(), "test request", nil)
	_ = provider.Shutdown(context.Background())

	mockStore.mu.Lock()
	defer mockStore.mu.Unlock()
	if len(mockStore.interactions) == 0 {
		t.Fatal("Expected debug interactions")
	}

	// Debug recording is async, so ordering is not guaranteed.
	found := false
	for _, inter := range mockStore.interactions {
		if inter.Error != "" {
			found = true
			if inter.Success {
				t.Error("Parse failure should record Success=false")
			}
			break
		}
	}
	if !found {
		t.Error("Expected a debug interaction with a parse error")
	}
}

func TestTieredSelection_DebugRecord_Success_Attempt1(t *testing.T) {
	catalog := setupTestCatalog(30)
	aiClient := NewTieredTestAIClient()
	aiClient.SetResponse(`["test-agent/capability_0"]`)

	config := &TieredCapabilityConfig{RetryEnabled: true, MaxRetries: 2}
	provider := NewTieredCapabilityProvider(catalog, aiClient, config)

	mockStore := &tieredTestDebugStore{interactions: make([]LLMInteraction, 0)}
	provider.SetLLMDebugStore(mockStore)

	_, err := provider.GetCapabilities(context.Background(), "test request", nil)
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}
	_ = provider.Shutdown(context.Background())

	mockStore.mu.Lock()
	defer mockStore.mu.Unlock()
	if len(mockStore.interactions) == 0 {
		t.Fatal("Expected debug interactions")
	}
	last := mockStore.interactions[len(mockStore.interactions)-1]
	if !last.Success {
		t.Error("Successful selection should record Success=true")
	}
	if last.Attempt != 1 {
		t.Errorf("First-attempt success should have Attempt=1, got %d", last.Attempt)
	}
}

func TestTieredSelection_DebugRecord_RetrySuccess_Attempt2(t *testing.T) {
	catalog := setupTestCatalog(30)
	aiClient := newSequentialAIClient(
		[]string{"", `["test-agent/capability_0"]`},
		[]error{nil, nil},
	)

	config := &TieredCapabilityConfig{RetryEnabled: true, MaxRetries: 2}
	provider := NewTieredCapabilityProvider(catalog, aiClient, config)

	mockStore := &tieredTestDebugStore{interactions: make([]LLMInteraction, 0)}
	provider.SetLLMDebugStore(mockStore)

	_, err := provider.GetCapabilities(context.Background(), "test request", nil)
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}
	_ = provider.Shutdown(context.Background())

	mockStore.mu.Lock()
	defer mockStore.mu.Unlock()
	// Find the successful interaction
	found := false
	for _, inter := range mockStore.interactions {
		if inter.Success {
			found = true
			if inter.Attempt != 2 {
				t.Errorf("Retry success should have Attempt=2, got %d", inter.Attempt)
			}
			break
		}
	}
	if !found {
		t.Error("Expected a successful debug interaction")
	}
}

// --- 5d: Config wiring tests ---

func TestTieredSelection_DefaultConfig_RetryEnabled(t *testing.T) {
	config := DefaultConfig()
	if !config.TieredResolution.RetryEnabled {
		t.Error("Default config should have TieredResolution.RetryEnabled=true")
	}
	if config.TieredResolution.MaxRetries != 2 {
		t.Errorf("Default config should have TieredResolution.MaxRetries=2, got %d", config.TieredResolution.MaxRetries)
	}
}

func TestTieredSelection_WithTieredSelectionRetry_Disables(t *testing.T) {
	config := DefaultConfig()
	opt := WithTieredSelectionRetry(false, 0)
	opt(config)

	if config.TieredResolution.RetryEnabled {
		t.Error("WithTieredSelectionRetry(false, 0) should disable retry")
	}
	if config.TieredResolution.MaxRetries != 0 {
		t.Errorf("WithTieredSelectionRetry(false, 0) should set MaxRetries=0, got %d", config.TieredResolution.MaxRetries)
	}
}

func TestTieredSelection_EnvVar_DisablesRetry(t *testing.T) {
	t.Setenv("TRUVAG3_TIERED_SELECTION_RETRY_ENABLED", "false")
	defer os.Unsetenv("TRUVAG3_TIERED_SELECTION_RETRY_ENABLED")

	config := DefaultConfig()
	if config.TieredResolution.RetryEnabled {
		t.Error("TRUVAG3_TIERED_SELECTION_RETRY_ENABLED=false should disable retry")
	}
}
