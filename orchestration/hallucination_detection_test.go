package orchestration

import (
	"context"
	"os"
	"sort"
	"strings"
	"testing"

	"github.com/truvaagents/truva-g3/core"
)

// =============================================================================
// Tests for extractAgentNamesFromToolIDs()
// =============================================================================

func TestExtractAgentNamesFromToolIDs(t *testing.T) {
	tests := []struct {
		name     string
		toolIDs  []string
		expected []string
	}{
		{
			name:     "single tool ID with agent/capability format",
			toolIDs:  []string{"weather-tool-v2/get_current_weather"},
			expected: []string{"weather-tool-v2"}, // Already lowercase
		},
		{
			name: "multiple tool IDs from same agent",
			toolIDs: []string{
				"weather-tool-v2/get_current_weather",
				"weather-tool-v2/get_forecast",
			},
			expected: []string{"weather-tool-v2"}, // Deduplicated
		},
		{
			name: "multiple tool IDs from different agents",
			toolIDs: []string{
				"weather-tool-v2/get_current_weather",
				"news-tool/get_headlines",
				"stock-market-tool/get_price",
			},
			expected: []string{"news-tool", "stock-market-tool", "weather-tool-v2"}, // Sorted, lowercase
		},
		{
			name:     "tool ID without slash (just agent name)",
			toolIDs:  []string{"simple-agent"},
			expected: []string{"simple-agent"},
		},
		{
			name:     "empty tool IDs",
			toolIDs:  []string{},
			expected: []string{},
		},
		{
			name:     "nil tool IDs",
			toolIDs:  nil,
			expected: []string{},
		},
		{
			name: "mixed formats",
			toolIDs: []string{
				"agent-with-slash/capability",
				"agent-without-slash",
			},
			expected: []string{"agent-with-slash", "agent-without-slash"},
		},
		{
			name: "mixed case tool IDs normalized to lowercase",
			toolIDs: []string{
				"Weather-Tool-V2/get_current_weather",
				"NEWS-TOOL/get_headlines",
			},
			expected: []string{"news-tool", "weather-tool-v2"}, // Normalized to lowercase
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := extractAgentNamesFromToolIDs(tt.toolIDs)

			// Sort both for comparison
			sort.Strings(result)
			sort.Strings(tt.expected)

			if len(result) != len(tt.expected) {
				t.Errorf("expected %d agents, got %d", len(tt.expected), len(result))
				t.Logf("expected: %v", tt.expected)
				t.Logf("got: %v", result)
				return
			}

			for i, agent := range tt.expected {
				if result[i] != agent {
					t.Errorf("expected agent %q at index %d, got %q", agent, i, result[i])
				}
			}
		})
	}
}

// =============================================================================
// Tests for CapabilityResult struct
// =============================================================================

func TestCapabilityResult_Struct(t *testing.T) {
	t.Run("struct fields accessible", func(t *testing.T) {
		result := &CapabilityResult{
			FormattedInfo: "test capability info",
			AgentNames:    []string{"agent-1", "agent-2"},
		}

		if result.FormattedInfo != "test capability info" {
			t.Errorf("expected FormattedInfo 'test capability info', got %q", result.FormattedInfo)
		}

		if len(result.AgentNames) != 2 {
			t.Errorf("expected 2 agent names, got %d", len(result.AgentNames))
		}

		if result.AgentNames[0] != "agent-1" {
			t.Error("expected agent-1 in agent names")
		}
	})

	t.Run("nil AgentNames slice", func(t *testing.T) {
		result := &CapabilityResult{
			FormattedInfo: "info",
			AgentNames:    nil,
		}

		// Should not panic when accessing nil slice
		if result.AgentNames != nil {
			t.Error("expected nil AgentNames")
		}
	})
}

// =============================================================================
// Tests for GetPublicAgentNames()
// =============================================================================

func TestAgentCatalog_GetPublicAgentNames(t *testing.T) {
	t.Run("excludes agents with only internal capabilities", func(t *testing.T) {
		catalog := &AgentCatalog{
			agents: map[string]*AgentInfo{
				"public-agent": {
					Registration: &core.ServiceInfo{
						ID:   "public-agent",
						Name: "Public-Agent", // Mixed case - should be normalized
					},
					Capabilities: []EnhancedCapability{
						{Name: "public_cap", Internal: false},
					},
				},
				"internal-only-agent": {
					Registration: &core.ServiceInfo{
						ID:   "internal-only-agent",
						Name: "Internal-Only-Agent",
					},
					Capabilities: []EnhancedCapability{
						{Name: "internal_cap", Internal: true},
					},
				},
				"mixed-agent": {
					Registration: &core.ServiceInfo{
						ID:   "mixed-agent",
						Name: "MIXED-AGENT", // All caps - should be normalized
					},
					Capabilities: []EnhancedCapability{
						{Name: "internal_cap", Internal: true},
						{Name: "public_cap", Internal: false},
					},
				},
			},
		}

		names := catalog.GetPublicAgentNames()

		// Should include public-agent and mixed-agent, but NOT internal-only-agent
		// All names should be lowercase (normalized)
		if len(names) != 2 {
			t.Errorf("expected 2 public agents, got %d: %v", len(names), names)
		}

		hasPublic := false
		hasMixed := false
		hasInternal := false
		for _, name := range names {
			// Names should be lowercase
			if name == "public-agent" {
				hasPublic = true
			}
			if name == "mixed-agent" {
				hasMixed = true
			}
			if name == "internal-only-agent" {
				hasInternal = true
			}
		}

		if !hasPublic {
			t.Error("expected public-agent (lowercase) to be included")
		}
		if !hasMixed {
			t.Error("expected mixed-agent (lowercase) to be included (has at least one public capability)")
		}
		if hasInternal {
			t.Error("expected internal-only-agent to be excluded")
		}
	})

	t.Run("empty catalog returns empty slice", func(t *testing.T) {
		catalog := &AgentCatalog{
			agents: map[string]*AgentInfo{},
		}

		names := catalog.GetPublicAgentNames()
		if len(names) != 0 {
			t.Errorf("expected empty slice, got %v", names)
		}
	})

	t.Run("skips agents with nil Registration", func(t *testing.T) {
		catalog := &AgentCatalog{
			agents: map[string]*AgentInfo{
				"nil-reg-agent": {
					Registration: nil, // Nil registration
					Capabilities: []EnhancedCapability{
						{Name: "public_cap", Internal: false},
					},
				},
				"valid-agent": {
					Registration: &core.ServiceInfo{
						ID:   "valid-agent",
						Name: "Valid-Agent", // Mixed case - should be normalized to lowercase
					},
					Capabilities: []EnhancedCapability{
						{Name: "public_cap", Internal: false},
					},
				},
			},
		}

		names := catalog.GetPublicAgentNames()
		if len(names) != 1 {
			t.Errorf("expected 1 agent, got %d: %v", len(names), names)
		}
		// Name should be lowercase
		if len(names) > 0 && names[0] != "valid-agent" {
			t.Errorf("expected valid-agent (lowercase), got %s", names[0])
		}
	})

	t.Run("normalizes names to lowercase", func(t *testing.T) {
		catalog := &AgentCatalog{
			agents: map[string]*AgentInfo{
				"uppercase-agent": {
					Registration: &core.ServiceInfo{
						ID:   "uppercase-agent",
						Name: "UPPERCASE-AGENT",
					},
					Capabilities: []EnhancedCapability{
						{Name: "cap", Internal: false},
					},
				},
				"mixedcase-agent": {
					Registration: &core.ServiceInfo{
						ID:   "mixedcase-agent",
						Name: "MixedCase-Agent",
					},
					Capabilities: []EnhancedCapability{
						{Name: "cap", Internal: false},
					},
				},
			},
		}

		names := catalog.GetPublicAgentNames()
		if len(names) != 2 {
			t.Errorf("expected 2 agents, got %d: %v", len(names), names)
		}

		// All names should be lowercase
		for _, name := range names {
			if name != strings.ToLower(name) {
				t.Errorf("expected lowercase name, got %q", name)
			}
		}
	})
}

// =============================================================================
// Tests for validatePlanAgainstAllowedAgents()
// =============================================================================

func TestValidatePlanAgainstAllowedAgents(t *testing.T) {
	// Create a minimal orchestrator for testing
	orchestrator := &AIOrchestrator{
		// logger is nil - tests graceful degradation
	}

	tests := []struct {
		name              string
		plan              *RoutingPlan
		allowedAgents     map[string]bool
		expectError       bool
		expectedHallAgent string // The hallucinated agent name returned
	}{
		{
			name: "valid plan - all agents allowed",
			plan: &RoutingPlan{
				PlanID: "test-1",
				Steps: []RoutingStep{
					{StepID: "step-1", AgentName: "weather-tool-v2"},
					{StepID: "step-2", AgentName: "news-tool"},
				},
			},
			allowedAgents: map[string]bool{
				"weather-tool-v2": true,
				"news-tool":       true,
			},
			expectError:       false,
			expectedHallAgent: "",
		},
		{
			name: "invalid plan - one hallucinated agent",
			plan: &RoutingPlan{
				PlanID: "test-2",
				Steps: []RoutingStep{
					{StepID: "step-1", AgentName: "weather-tool-v2"},
					{StepID: "step-2", AgentName: "time-tool-v1"}, // Hallucinated!
				},
			},
			allowedAgents: map[string]bool{
				"weather-tool-v2": true,
			},
			expectError:       true,
			expectedHallAgent: "time-tool-v1",
		},
		{
			name: "invalid plan - all agents hallucinated",
			plan: &RoutingPlan{
				PlanID: "test-3",
				Steps: []RoutingStep{
					{StepID: "step-1", AgentName: "fake-agent-1"},
					{StepID: "step-2", AgentName: "fake-agent-2"},
				},
			},
			allowedAgents: map[string]bool{
				"real-agent": true,
			},
			expectError:       true,
			expectedHallAgent: "fake-agent-1", // First hallucinated agent detected
		},
		{
			name: "empty allowed agents - skip validation (graceful degradation)",
			plan: &RoutingPlan{
				PlanID: "test-4",
				Steps: []RoutingStep{
					{StepID: "step-1", AgentName: "any-agent"},
				},
			},
			allowedAgents:     map[string]bool{}, // Empty!
			expectError:       false,             // Should skip validation
			expectedHallAgent: "",
		},
		{
			name:              "nil plan - return error",
			plan:              nil,
			allowedAgents:     map[string]bool{"agent": true},
			expectError:       true,
			expectedHallAgent: "",
		},
		{
			name: "empty plan - no steps to validate",
			plan: &RoutingPlan{
				PlanID: "test-5",
				Steps:  []RoutingStep{}, // Empty steps
			},
			allowedAgents: map[string]bool{
				"agent": true,
			},
			expectError:       false, // No steps = nothing to validate = success
			expectedHallAgent: "",
		},
		{
			name: "single step - valid",
			plan: &RoutingPlan{
				PlanID: "test-6",
				Steps: []RoutingStep{
					{StepID: "step-1", AgentName: "only-agent"},
				},
			},
			allowedAgents: map[string]bool{
				"only-agent": true,
			},
			expectError:       false,
			expectedHallAgent: "",
		},
		{
			name: "single step - hallucinated",
			plan: &RoutingPlan{
				PlanID: "test-7",
				Steps: []RoutingStep{
					{StepID: "step-1", AgentName: "invented-agent"},
				},
			},
			allowedAgents: map[string]bool{
				"real-agent": true,
			},
			expectError:       true,
			expectedHallAgent: "invented-agent",
		},
		{
			name: "case insensitivity - different case should match",
			plan: &RoutingPlan{
				PlanID: "test-8",
				Steps: []RoutingStep{
					{StepID: "step-1", AgentName: "Weather-Tool-V2"}, // Mixed case
				},
			},
			allowedAgents: map[string]bool{
				"weather-tool-v2": true, // Lowercase (normalized)
			},
			expectError:       false, // Should pass - case insensitive matching
			expectedHallAgent: "",
		},
		{
			name: "case insensitivity - uppercase agent in plan should match lowercase allowed",
			plan: &RoutingPlan{
				PlanID: "test-9",
				Steps: []RoutingStep{
					{StepID: "step-1", AgentName: "WEATHER-TOOL-V2"}, // ALL CAPS
				},
			},
			allowedAgents: map[string]bool{
				"weather-tool-v2": true, // Lowercase (normalized)
			},
			expectError:       false, // Should pass - case insensitive matching
			expectedHallAgent: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			hallAgent, err := orchestrator.validatePlanAgainstAllowedAgents(context.Background(), tt.plan, tt.allowedAgents)

			if tt.expectError {
				if err == nil {
					t.Error("expected error, got nil")
				}
				if hallAgent != tt.expectedHallAgent {
					t.Errorf("expected hallucinated agent %q, got %q", tt.expectedHallAgent, hallAgent)
				}
			} else {
				if err != nil {
					t.Errorf("unexpected error: %v", err)
				}
				if hallAgent != "" {
					t.Errorf("expected no hallucinated agent, got %q", hallAgent)
				}
			}
		})
	}
}

func TestValidatePlanAgainstAllowedAgents_WithLogger(t *testing.T) {
	// Create orchestrator with a mock logger to verify logging behavior
	mockLogger := &hallMockLogger{}
	orchestrator := &AIOrchestrator{
		logger: mockLogger,
	}

	t.Run("logs debug when skipping validation due to empty allowed agents", func(t *testing.T) {
		mockLogger.reset()

		plan := &RoutingPlan{
			Steps: []RoutingStep{{StepID: "s1", AgentName: "agent"}},
		}

		_, err := orchestrator.validatePlanAgainstAllowedAgents(context.Background(), plan, map[string]bool{})

		if err != nil {
			t.Errorf("unexpected error: %v", err)
		}

		if !mockLogger.hasDebugLog("Skipping hallucination validation") {
			t.Error("expected debug log about skipping validation")
		}
	})
}

// =============================================================================
// Tests for DefaultConfig() - Hallucination Retry Defaults
// =============================================================================

func TestDefaultConfig_HallucinationRetryDefaults(t *testing.T) {
	// Clear any environment variables that might affect the test
	_ = os.Unsetenv("TRUVAG3_HALLUCINATION_RETRY_ENABLED")
	_ = os.Unsetenv("TRUVAG3_HALLUCINATION_MAX_RETRIES")

	config := DefaultConfig()

	t.Run("HallucinationRetryEnabled defaults to true", func(t *testing.T) {
		if !config.HallucinationRetryEnabled {
			t.Error("expected HallucinationRetryEnabled to default to true")
		}
	})

	t.Run("HallucinationMaxRetries defaults to 1", func(t *testing.T) {
		if config.HallucinationMaxRetries != 1 {
			t.Errorf("expected HallucinationMaxRetries to default to 1, got %d", config.HallucinationMaxRetries)
		}
	})
}

// =============================================================================
// Tests for Environment Variable Parsing
// =============================================================================

func TestDefaultConfig_HallucinationEnvVars(t *testing.T) {
	// Save original env vars
	origEnabled := os.Getenv("TRUVAG3_HALLUCINATION_RETRY_ENABLED")
	origMaxRetries := os.Getenv("TRUVAG3_HALLUCINATION_MAX_RETRIES")
	defer func() {
		restoreEnv("TRUVAG3_HALLUCINATION_RETRY_ENABLED", origEnabled)
		restoreEnv("TRUVAG3_HALLUCINATION_MAX_RETRIES", origMaxRetries)
	}()

	tests := []struct {
		name            string
		envEnabled      string
		envMaxRetries   string
		expectedEnabled bool
		expectedRetries int
	}{
		{
			name:            "TRUVAG3_HALLUCINATION_RETRY_ENABLED=false disables retry",
			envEnabled:      "false",
			envMaxRetries:   "",
			expectedEnabled: false,
			expectedRetries: 1, // Default
		},
		{
			name:            "TRUVAG3_HALLUCINATION_RETRY_ENABLED=true enables retry",
			envEnabled:      "true",
			envMaxRetries:   "",
			expectedEnabled: true,
			expectedRetries: 1, // Default
		},
		{
			name:            "TRUVAG3_HALLUCINATION_RETRY_ENABLED case insensitive",
			envEnabled:      "TRUE",
			envMaxRetries:   "",
			expectedEnabled: true,
			expectedRetries: 1,
		},
		{
			name:            "TRUVAG3_HALLUCINATION_MAX_RETRIES=0 sets zero retries",
			envEnabled:      "",
			envMaxRetries:   "0",
			expectedEnabled: true, // Default
			expectedRetries: 0,
		},
		{
			name:            "TRUVAG3_HALLUCINATION_MAX_RETRIES=3 sets three retries",
			envEnabled:      "",
			envMaxRetries:   "3",
			expectedEnabled: true,
			expectedRetries: 3,
		},
		{
			name:            "both env vars set",
			envEnabled:      "false",
			envMaxRetries:   "5",
			expectedEnabled: false,
			expectedRetries: 5,
		},
		{
			name:            "invalid max retries value ignored",
			envEnabled:      "",
			envMaxRetries:   "invalid",
			expectedEnabled: true,
			expectedRetries: 1, // Default preserved
		},
		{
			name:            "negative max retries value ignored",
			envEnabled:      "",
			envMaxRetries:   "-1",
			expectedEnabled: true,
			expectedRetries: 1, // Default preserved (val >= 0 check fails)
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Set environment variables
			_ = os.Unsetenv("TRUVAG3_HALLUCINATION_RETRY_ENABLED")
			_ = os.Unsetenv("TRUVAG3_HALLUCINATION_MAX_RETRIES")

			if tt.envEnabled != "" {
				_ = os.Setenv("TRUVAG3_HALLUCINATION_RETRY_ENABLED", tt.envEnabled)
			}
			if tt.envMaxRetries != "" {
				_ = os.Setenv("TRUVAG3_HALLUCINATION_MAX_RETRIES", tt.envMaxRetries)
			}

			config := DefaultConfig()

			if config.HallucinationRetryEnabled != tt.expectedEnabled {
				t.Errorf("expected HallucinationRetryEnabled=%v, got %v",
					tt.expectedEnabled, config.HallucinationRetryEnabled)
			}

			if config.HallucinationMaxRetries != tt.expectedRetries {
				t.Errorf("expected HallucinationMaxRetries=%d, got %d",
					tt.expectedRetries, config.HallucinationMaxRetries)
			}
		})
	}
}

// =============================================================================
// Tests for WithHallucinationRetry() Option Function
// =============================================================================

func TestWithHallucinationRetry(t *testing.T) {
	tests := []struct {
		name            string
		enabled         bool
		maxRetries      int
		expectedEnabled bool
		expectedRetries int
	}{
		{
			name:            "enable with 1 retry",
			enabled:         true,
			maxRetries:      1,
			expectedEnabled: true,
			expectedRetries: 1,
		},
		{
			name:            "disable with 0 retries",
			enabled:         false,
			maxRetries:      0,
			expectedEnabled: false,
			expectedRetries: 0,
		},
		{
			name:            "enable with 5 retries",
			enabled:         true,
			maxRetries:      5,
			expectedEnabled: true,
			expectedRetries: 5,
		},
		{
			name:            "negative retries ignored (keeps previous value)",
			enabled:         true,
			maxRetries:      -1,
			expectedEnabled: true,
			expectedRetries: 1, // Default value preserved
		},
		{
			name:            "zero retries is valid",
			enabled:         true,
			maxRetries:      0,
			expectedEnabled: true,
			expectedRetries: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Start with default config
			config := DefaultConfig()

			// Apply the option
			opt := WithHallucinationRetry(tt.enabled, tt.maxRetries)
			opt(config)

			if config.HallucinationRetryEnabled != tt.expectedEnabled {
				t.Errorf("expected HallucinationRetryEnabled=%v, got %v",
					tt.expectedEnabled, config.HallucinationRetryEnabled)
			}

			if config.HallucinationMaxRetries != tt.expectedRetries {
				t.Errorf("expected HallucinationMaxRetries=%d, got %d",
					tt.expectedRetries, config.HallucinationMaxRetries)
			}
		})
	}
}

func TestWithHallucinationRetry_OverridesEnvVars(t *testing.T) {
	// Save original env vars
	origEnabled := os.Getenv("TRUVAG3_HALLUCINATION_RETRY_ENABLED")
	origMaxRetries := os.Getenv("TRUVAG3_HALLUCINATION_MAX_RETRIES")
	defer func() {
		restoreEnv("TRUVAG3_HALLUCINATION_RETRY_ENABLED", origEnabled)
		restoreEnv("TRUVAG3_HALLUCINATION_MAX_RETRIES", origMaxRetries)
	}()

	// Set env vars to one value
	_ = os.Setenv("TRUVAG3_HALLUCINATION_RETRY_ENABLED", "false")
	_ = os.Setenv("TRUVAG3_HALLUCINATION_MAX_RETRIES", "10")

	// Get default config (should have env var values)
	config := DefaultConfig()

	// Verify env vars were applied
	if config.HallucinationRetryEnabled != false {
		t.Fatal("test setup error: env var not applied")
	}
	if config.HallucinationMaxRetries != 10 {
		t.Fatal("test setup error: env var not applied")
	}

	// Apply option - should override
	opt := WithHallucinationRetry(true, 2)
	opt(config)

	if config.HallucinationRetryEnabled != true {
		t.Error("WithHallucinationRetry should override env var for enabled")
	}
	if config.HallucinationMaxRetries != 2 {
		t.Error("WithHallucinationRetry should override env var for max retries")
	}
}

// =============================================================================
// Tests for PlanningPromptResult struct
// =============================================================================

func TestPlanningPromptResult_Struct(t *testing.T) {
	t.Run("struct fields accessible", func(t *testing.T) {
		result := &PlanningPromptResult{
			Prompt: "test prompt",
			AllowedAgents: map[string]bool{
				"agent-1": true,
				"agent-2": true,
			},
		}

		if result.Prompt != "test prompt" {
			t.Errorf("expected prompt 'test prompt', got %q", result.Prompt)
		}

		if len(result.AllowedAgents) != 2 {
			t.Errorf("expected 2 allowed agents, got %d", len(result.AllowedAgents))
		}

		if !result.AllowedAgents["agent-1"] {
			t.Error("expected agent-1 in allowed agents")
		}
	})

	t.Run("nil AllowedAgents map", func(t *testing.T) {
		result := &PlanningPromptResult{
			Prompt:        "prompt",
			AllowedAgents: nil,
		}

		// Should not panic when accessing nil map
		if result.AllowedAgents != nil {
			t.Error("expected nil AllowedAgents")
		}
	})
}

// =============================================================================
// Helper Functions
// =============================================================================

// restoreEnv restores an environment variable to its original value
func restoreEnv(key, value string) {
	if value == "" {
		_ = os.Unsetenv(key)
	} else {
		_ = os.Setenv(key, value)
	}
}

// hallMockLogger implements core.Logger for hallucination detection tests
// Named differently to avoid conflict with MockLogger in prompt_builder_test.go
type hallMockLogger struct {
	debugLogs []string
	infoLogs  []string
	warnLogs  []string
	errorLogs []string
}

func (m *hallMockLogger) reset() {
	m.debugLogs = nil
	m.infoLogs = nil
	m.warnLogs = nil
	m.errorLogs = nil
}

func (m *hallMockLogger) Debug(msg string, fields map[string]interface{}) {
	m.debugLogs = append(m.debugLogs, msg)
}

func (m *hallMockLogger) Info(msg string, fields map[string]interface{}) {
	m.infoLogs = append(m.infoLogs, msg)
}

func (m *hallMockLogger) Warn(msg string, fields map[string]interface{}) {
	m.warnLogs = append(m.warnLogs, msg)
}

func (m *hallMockLogger) Error(msg string, fields map[string]interface{}) {
	m.errorLogs = append(m.errorLogs, msg)
}

func (m *hallMockLogger) DebugWithContext(ctx context.Context, msg string, fields map[string]interface{}) {
	m.debugLogs = append(m.debugLogs, msg)
}

func (m *hallMockLogger) InfoWithContext(ctx context.Context, msg string, fields map[string]interface{}) {
	m.infoLogs = append(m.infoLogs, msg)
}

func (m *hallMockLogger) WarnWithContext(ctx context.Context, msg string, fields map[string]interface{}) {
	m.warnLogs = append(m.warnLogs, msg)
}

func (m *hallMockLogger) ErrorWithContext(ctx context.Context, msg string, fields map[string]interface{}) {
	m.errorLogs = append(m.errorLogs, msg)
}

func (m *hallMockLogger) hasDebugLog(substr string) bool {
	for _, log := range m.debugLogs {
		if hallContainsSubstr(log, substr) {
			return true
		}
	}
	return false
}

// hallContainsSubstr checks if s contains substr (named to avoid conflict)
func hallContainsSubstr(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(substr) == 0 ||
		(len(s) > 0 && len(substr) > 0 && hallFindSubstr(s, substr) >= 0))
}

func hallFindSubstr(s, substr string) int {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return i
		}
	}
	return -1
}

// =============================================================================
// Tests for Tiered Selection Miss (agent exists in catalog but not in allowedAgents)
// =============================================================================

func TestValidatePlanAgainstAllowedAgents_TieredSelectionMiss(t *testing.T) {
	// Create a mock catalog with agents
	catalog := &AgentCatalog{
		agents: map[string]*AgentInfo{
			"weather-tool-v2": {
				Registration: &core.ServiceInfo{
					ID:   "weather-tool-v2",
					Name: "weather-tool-v2",
				},
				Capabilities: []EnhancedCapability{
					{Name: "get_current_weather", Description: "Gets weather"},
				},
			},
			"geocoding-tool": {
				Registration: &core.ServiceInfo{
					ID:   "geocoding-tool",
					Name: "geocoding-tool",
				},
				Capabilities: []EnhancedCapability{
					{Name: "geocode", Description: "Geocodes locations"},
				},
			},
		},
	}

	mockLogger := &hallMockLogger{}

	// Create orchestrator with catalog and logger
	orchestrator := &AIOrchestrator{
		catalog: catalog,
		logger:  mockLogger,
	}

	t.Run("agent in catalog but not in allowed list should be added", func(t *testing.T) {
		mockLogger.reset()

		plan := &RoutingPlan{
			PlanID: "test-tiered-miss",
			Steps: []RoutingStep{
				{StepID: "step-1", AgentName: "geocoding-tool"},  // In allowedAgents
				{StepID: "step-2", AgentName: "weather-tool-v2"}, // NOT in allowedAgents but IS in catalog
			},
		}

		// Only geocoding-tool is in allowed list (simulating tiered selection missing weather-tool-v2)
		allowedAgents := map[string]bool{
			"geocoding-tool": true,
			// weather-tool-v2 is NOT here - simulating tiered selection miss
		}

		// Validation should pass because weather-tool-v2 exists in catalog
		hallAgent, err := orchestrator.validatePlanAgainstAllowedAgents(context.Background(), plan, allowedAgents)

		if err != nil {
			t.Errorf("expected no error (tiered selection miss should be handled), got: %v", err)
		}
		if hallAgent != "" {
			t.Errorf("expected no hallucinated agent, got %q", hallAgent)
		}

		// Verify weather-tool-v2 was added to allowedAgents
		if !allowedAgents["weather-tool-v2"] {
			t.Error("expected weather-tool-v2 to be added to allowedAgents after catalog check")
		}

		// Verify warning was logged
		foundWarning := false
		for _, log := range mockLogger.warnLogs {
			if hallContainsSubstr(log, "Tiered selection missed a valid tool") {
				foundWarning = true
				break
			}
		}
		if !foundWarning {
			t.Error("expected warning about tiered selection miss to be logged")
		}
	})

	t.Run("agent not in catalog is flagged as hallucination", func(t *testing.T) {
		mockLogger.reset()

		plan := &RoutingPlan{
			PlanID: "test-true-hallucination",
			Steps: []RoutingStep{
				{StepID: "step-1", AgentName: "geocoding-tool"}, // In allowedAgents
				{StepID: "step-2", AgentName: "time-tool-v1"},   // NOT in allowedAgents AND NOT in catalog
			},
		}

		allowedAgents := map[string]bool{
			"geocoding-tool": true,
		}

		// Validation should fail because time-tool-v1 doesn't exist in catalog
		hallAgent, err := orchestrator.validatePlanAgainstAllowedAgents(context.Background(), plan, allowedAgents)

		if err == nil {
			t.Error("expected error for true hallucination (agent not in catalog)")
		}
		if hallAgent != "time-tool-v1" {
			t.Errorf("expected hallucinated agent 'time-tool-v1', got %q", hallAgent)
		}
	})

	t.Run("multiple agents - some tiered miss, some true hallucination", func(t *testing.T) {
		mockLogger.reset()

		plan := &RoutingPlan{
			PlanID: "test-mixed",
			Steps: []RoutingStep{
				{StepID: "step-1", AgentName: "geocoding-tool"},  // In allowedAgents ✓
				{StepID: "step-2", AgentName: "weather-tool-v2"}, // Tiered selection miss (in catalog) ✓
				{StepID: "step-3", AgentName: "time-tool-v1"},    // True hallucination (not in catalog) ✗
			},
		}

		allowedAgents := map[string]bool{
			"geocoding-tool": true,
		}

		// Validation should fail on time-tool-v1 (true hallucination)
		hallAgent, err := orchestrator.validatePlanAgainstAllowedAgents(context.Background(), plan, allowedAgents)

		if err == nil {
			t.Error("expected error for true hallucination")
		}
		if hallAgent != "time-tool-v1" {
			t.Errorf("expected hallucinated agent 'time-tool-v1', got %q", hallAgent)
		}

		// weather-tool-v2 should have been added before we hit time-tool-v1
		if !allowedAgents["weather-tool-v2"] {
			t.Error("expected weather-tool-v2 to be added before hitting true hallucination")
		}
	})

	t.Run("no catalog - treat as hallucination", func(t *testing.T) {
		// Orchestrator without catalog
		orchNoCatalog := &AIOrchestrator{
			catalog: nil,
			logger:  mockLogger,
		}

		mockLogger.reset()

		plan := &RoutingPlan{
			PlanID: "test-no-catalog",
			Steps: []RoutingStep{
				{StepID: "step-1", AgentName: "some-agent"},
			},
		}

		allowedAgents := map[string]bool{
			"other-agent": true,
		}

		// Without catalog, can't verify - treat as hallucination
		hallAgent, err := orchNoCatalog.validatePlanAgainstAllowedAgents(context.Background(), plan, allowedAgents)

		if err == nil {
			t.Error("expected error when catalog is nil")
		}
		if hallAgent != "some-agent" {
			t.Errorf("expected 'some-agent', got %q", hallAgent)
		}
	})
}

// =============================================================================
// Tests for extractHallucinationContext()
// =============================================================================

func TestExtractHallucinationContext(t *testing.T) {
	t.Run("extracts full context from plan step", func(t *testing.T) {
		plan := &RoutingPlan{
			PlanID: "test-plan",
			Steps: []RoutingStep{
				{
					StepID:      "step-1",
					AgentName:   "calculator",
					Instruction: "Multiply 100 by the stock price",
					Metadata: map[string]interface{}{
						"capability": "calculate",
					},
				},
			},
		}

		ctx := extractHallucinationContext(plan, "calculator")

		if ctx.AgentName != "calculator" {
			t.Errorf("expected AgentName 'calculator', got %q", ctx.AgentName)
		}
		if ctx.Instruction != "Multiply 100 by the stock price" {
			t.Errorf("expected Instruction 'Multiply 100 by the stock price', got %q", ctx.Instruction)
		}
		if ctx.Capability != "calculate" {
			t.Errorf("expected Capability 'calculate', got %q", ctx.Capability)
		}
	})

	t.Run("handles nil plan", func(t *testing.T) {
		ctx := extractHallucinationContext(nil, "calculator")

		if ctx.AgentName != "calculator" {
			t.Errorf("expected AgentName 'calculator', got %q", ctx.AgentName)
		}
		if ctx.Instruction != "" {
			t.Errorf("expected empty Instruction, got %q", ctx.Instruction)
		}
		if ctx.Capability != "" {
			t.Errorf("expected empty Capability, got %q", ctx.Capability)
		}
	})

	t.Run("handles plan with no matching step", func(t *testing.T) {
		plan := &RoutingPlan{
			PlanID: "test-plan",
			Steps: []RoutingStep{
				{
					StepID:      "step-1",
					AgentName:   "weather-tool",
					Instruction: "Get weather",
				},
			},
		}

		ctx := extractHallucinationContext(plan, "calculator")

		if ctx.AgentName != "calculator" {
			t.Errorf("expected AgentName 'calculator', got %q", ctx.AgentName)
		}
		if ctx.Instruction != "" {
			t.Errorf("expected empty Instruction for non-matching agent, got %q", ctx.Instruction)
		}
	})

	t.Run("handles step with nil metadata", func(t *testing.T) {
		plan := &RoutingPlan{
			PlanID: "test-plan",
			Steps: []RoutingStep{
				{
					StepID:      "step-1",
					AgentName:   "calculator",
					Instruction: "Calculate something",
					Metadata:    nil,
				},
			},
		}

		ctx := extractHallucinationContext(plan, "calculator")

		if ctx.AgentName != "calculator" {
			t.Errorf("expected AgentName 'calculator', got %q", ctx.AgentName)
		}
		if ctx.Instruction != "Calculate something" {
			t.Errorf("expected Instruction 'Calculate something', got %q", ctx.Instruction)
		}
		if ctx.Capability != "" {
			t.Errorf("expected empty Capability when metadata is nil, got %q", ctx.Capability)
		}
	})

	t.Run("handles metadata with non-string capability", func(t *testing.T) {
		plan := &RoutingPlan{
			PlanID: "test-plan",
			Steps: []RoutingStep{
				{
					StepID:      "step-1",
					AgentName:   "calculator",
					Instruction: "Calculate",
					Metadata: map[string]interface{}{
						"capability": 123, // Not a string
					},
				},
			},
		}

		ctx := extractHallucinationContext(plan, "calculator")

		if ctx.Capability != "" {
			t.Errorf("expected empty Capability for non-string metadata, got %q", ctx.Capability)
		}
	})

	t.Run("extracts first matching step when multiple exist", func(t *testing.T) {
		plan := &RoutingPlan{
			PlanID: "test-plan",
			Steps: []RoutingStep{
				{
					StepID:      "step-1",
					AgentName:   "calculator",
					Instruction: "First calculation",
					Metadata: map[string]interface{}{
						"capability": "add",
					},
				},
				{
					StepID:      "step-2",
					AgentName:   "calculator",
					Instruction: "Second calculation",
					Metadata: map[string]interface{}{
						"capability": "multiply",
					},
				},
			},
		}

		ctx := extractHallucinationContext(plan, "calculator")

		// Should extract from the first matching step
		if ctx.Instruction != "First calculation" {
			t.Errorf("expected Instruction 'First calculation', got %q", ctx.Instruction)
		}
		if ctx.Capability != "add" {
			t.Errorf("expected Capability 'add', got %q", ctx.Capability)
		}
	})

	t.Run("handles empty steps slice", func(t *testing.T) {
		plan := &RoutingPlan{
			PlanID: "test-plan",
			Steps:  []RoutingStep{},
		}

		ctx := extractHallucinationContext(plan, "calculator")

		if ctx.AgentName != "calculator" {
			t.Errorf("expected AgentName 'calculator', got %q", ctx.AgentName)
		}
		if ctx.Instruction != "" {
			t.Errorf("expected empty Instruction, got %q", ctx.Instruction)
		}
	})
}

// =============================================================================
// Tests for buildEnhancedRequestForRetry()
// =============================================================================

func TestBuildEnhancedRequestForRetry(t *testing.T) {
	t.Run("builds enhanced request with full context", func(t *testing.T) {
		hallCtx := &HallucinationContext{
			AgentName:   "calculator",
			Capability:  "calculate",
			Instruction: "Multiply 100 by the stock price",
		}

		result := buildEnhancedRequestForRetry("Get stock price times 100", hallCtx)

		// Should contain original request
		if !strings.Contains(result, "Get stock price times 100") {
			t.Error("expected result to contain original request")
		}

		// Should contain CAPABILITY_HINT marker
		if !strings.Contains(result, "[CAPABILITY_HINT:") {
			t.Error("expected result to contain [CAPABILITY_HINT:")
		}

		// Should contain instruction
		if !strings.Contains(result, "perform: Multiply 100 by the stock price") {
			t.Error("expected result to contain instruction")
		}

		// Should contain agent type
		if !strings.Contains(result, "agent type: calculator") {
			t.Error("expected result to contain agent type")
		}

		// Should contain capability
		if !strings.Contains(result, "capability: calculate") {
			t.Error("expected result to contain capability")
		}
	})

	t.Run("handles nil context", func(t *testing.T) {
		result := buildEnhancedRequestForRetry("original request", nil)

		if result != "original request" {
			t.Errorf("expected original request unchanged, got %q", result)
		}
	})

	t.Run("handles context with only agent name", func(t *testing.T) {
		hallCtx := &HallucinationContext{
			AgentName: "calculator",
		}

		result := buildEnhancedRequestForRetry("original request", hallCtx)

		if !strings.Contains(result, "agent type: calculator") {
			t.Error("expected result to contain agent type")
		}
		if !strings.Contains(result, "[CAPABILITY_HINT:") {
			t.Error("expected result to contain CAPABILITY_HINT")
		}
	})

	t.Run("handles context with instruction and capability but no agent name", func(t *testing.T) {
		hallCtx := &HallucinationContext{
			Instruction: "Do something",
			Capability:  "do_it",
		}

		result := buildEnhancedRequestForRetry("original", hallCtx)

		if !strings.Contains(result, "perform: Do something") {
			t.Error("expected result to contain instruction")
		}
		if !strings.Contains(result, "capability: do_it") {
			t.Error("expected result to contain capability")
		}
	})

	t.Run("handles empty context (all fields empty)", func(t *testing.T) {
		hallCtx := &HallucinationContext{}

		result := buildEnhancedRequestForRetry("original request", hallCtx)

		// With all empty fields, should return original request
		if result != "original request" {
			t.Errorf("expected original request unchanged for empty context, got %q", result)
		}
	})

	t.Run("preserves multiline original request", func(t *testing.T) {
		hallCtx := &HallucinationContext{
			AgentName: "calculator",
		}

		originalRequest := "Line 1\nLine 2\nLine 3"
		result := buildEnhancedRequestForRetry(originalRequest, hallCtx)

		if !strings.Contains(result, "Line 1\nLine 2\nLine 3") {
			t.Error("expected result to preserve multiline original request")
		}
	})

	t.Run("hint parts are joined with semicolon", func(t *testing.T) {
		hallCtx := &HallucinationContext{
			AgentName:   "calc",
			Capability:  "add",
			Instruction: "add numbers",
		}

		result := buildEnhancedRequestForRetry("test", hallCtx)

		// Check that parts are joined with "; "
		if !strings.Contains(result, "perform: add numbers; agent type: calc; capability: add") {
			t.Errorf("expected hint parts joined with '; ', got: %s", result)
		}
	})
}

// =============================================================================
// Tests for HallucinationContext struct
// =============================================================================

func TestHallucinationContext_Struct(t *testing.T) {
	t.Run("struct fields accessible", func(t *testing.T) {
		ctx := &HallucinationContext{
			AgentName:   "test-agent",
			Capability:  "test-cap",
			Instruction: "test-instr",
		}

		if ctx.AgentName != "test-agent" {
			t.Errorf("expected AgentName 'test-agent', got %q", ctx.AgentName)
		}
		if ctx.Capability != "test-cap" {
			t.Errorf("expected Capability 'test-cap', got %q", ctx.Capability)
		}
		if ctx.Instruction != "test-instr" {
			t.Errorf("expected Instruction 'test-instr', got %q", ctx.Instruction)
		}
	})

	t.Run("zero value is safe", func(t *testing.T) {
		ctx := HallucinationContext{}

		if ctx.AgentName != "" {
			t.Error("expected empty AgentName")
		}
		if ctx.Capability != "" {
			t.Error("expected empty Capability")
		}
		if ctx.Instruction != "" {
			t.Error("expected empty Instruction")
		}
	})
}
