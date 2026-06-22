package orchestration

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/truvaagents/truva-g3/core"
)

// stringContains is a helper for checking if a string contains a substring
func stringContains(s, substr string) bool {
	return strings.Contains(s, substr)
}

// TestCreateSimpleOrchestrator tests the zero-configuration orchestrator creation
func TestCreateSimpleOrchestrator(t *testing.T) {
	discovery := NewMockDiscovery()
	aiClient := NewMockAIClient()

	orchestrator := CreateSimpleOrchestrator(discovery, aiClient)

	if orchestrator == nil {
		t.Fatal("Expected orchestrator, got nil")
	}

	// Should use default configuration
	if orchestrator.config == nil {
		t.Error("Expected default config to be set")
	}

	// Should use DefaultCapabilityProvider
	if orchestrator.capabilityProvider == nil {
		t.Error("Expected capability provider to be set")
	}

	// Test that it can process requests
	ctx := context.Background()
	response, err := orchestrator.ProcessRequest(ctx, "test request", nil)
	if err != nil && response == nil {
		// Either error or response is acceptable for mock
		t.Logf("ProcessRequest returned: %v", err)
	}
}

// TestCreateOrchestrator tests orchestrator creation with configuration
func TestCreateOrchestrator(t *testing.T) {
	tests := []struct {
		name                 string
		config               *OrchestratorConfig
		deps                 OrchestratorDependencies
		envVars              map[string]string
		expectError          bool
		expectedProviderType string
	}{
		{
			name:   "nil config uses defaults",
			config: nil,
			deps: OrchestratorDependencies{
				Discovery: NewMockDiscovery(),
				AIClient:  NewMockAIClient(),
			},
			expectError:          false,
			expectedProviderType: "default",
		},
		{
			name: "explicit service provider config",
			config: &OrchestratorConfig{
				CapabilityProviderType: "service",
				CapabilityService: ServiceCapabilityConfig{
					Endpoint: "http://test-service:8080",
				},
			},
			deps: OrchestratorDependencies{
				Discovery: NewMockDiscovery(),
				AIClient:  NewMockAIClient(),
			},
			expectError:          false,
			expectedProviderType: "service",
		},
		{
			name: "service provider without endpoint fails",
			config: &OrchestratorConfig{
				CapabilityProviderType: "service",
				// No endpoint specified
			},
			deps: OrchestratorDependencies{
				Discovery: NewMockDiscovery(),
				AIClient:  NewMockAIClient(),
			},
			expectError: true,
		},
		{
			name:   "auto-configuration from environment",
			config: nil,
			deps: OrchestratorDependencies{
				Discovery: NewMockDiscovery(),
				AIClient:  NewMockAIClient(),
			},
			envVars: map[string]string{
				"TRUVAG3_CAPABILITY_SERVICE_URL": "http://env-service:8080",
			},
			expectError:          false,
			expectedProviderType: "service",
		},
		{
			name:   "with optional dependencies",
			config: DefaultConfig(),
			deps: OrchestratorDependencies{
				Discovery:      NewMockDiscovery(),
				AIClient:       NewMockAIClient(),
				CircuitBreaker: &mockCircuitBreaker{},
				Logger:         &mockLogger{},
				Telemetry:      &mockTelemetry{},
			},
			expectError:          false,
			expectedProviderType: "default",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Set environment variables
			for k, v := range tt.envVars {
				oldVal := os.Getenv(k)
				_ = os.Setenv(k, v)
				defer func(key, val string) { _ = os.Setenv(key, val) }(k, oldVal)
			}

			orchestrator, err := CreateOrchestrator(tt.config, tt.deps)

			if tt.expectError {
				if err == nil {
					t.Error("Expected error, got nil")
				}
				return
			}

			if err != nil {
				t.Errorf("Unexpected error: %v", err)
				return
			}

			if orchestrator == nil {
				t.Fatal("Expected orchestrator, got nil")
			}

			// Check provider type if specified
			if tt.expectedProviderType != "" {
				actualType := orchestrator.config.CapabilityProviderType
				if actualType != tt.expectedProviderType {
					t.Errorf("Expected provider type %s, got %s", tt.expectedProviderType, actualType)
				}
			}
		})
	}
}

// TestCreateOrchestratorWithOptions tests the options-based factory
func TestCreateOrchestratorWithOptions(t *testing.T) {
	deps := OrchestratorDependencies{
		Discovery: NewMockDiscovery(),
		AIClient:  NewMockAIClient(),
	}

	tests := []struct {
		name        string
		options     []OrchestratorOption
		checkConfig func(*testing.T, *OrchestratorConfig)
	}{
		{
			name: "with capability provider option",
			options: []OrchestratorOption{
				WithCapabilityProvider("service", "http://option-service:8080"),
			},
			checkConfig: func(t *testing.T, config *OrchestratorConfig) {
				if config.CapabilityProviderType != "service" {
					t.Errorf("Expected service provider, got %s", config.CapabilityProviderType)
				}
				if config.CapabilityService.Endpoint != "http://option-service:8080" {
					t.Errorf("Expected endpoint http://option-service:8080, got %s", config.CapabilityService.Endpoint)
				}
			},
		},
		{
			name: "with telemetry option",
			options: []OrchestratorOption{
				WithTelemetry(true),
			},
			checkConfig: func(t *testing.T, config *OrchestratorConfig) {
				if !config.EnableTelemetry {
					t.Error("Expected telemetry to be enabled")
				}
			},
		},
		{
			name: "with fallback option",
			options: []OrchestratorOption{
				WithFallback(false),
			},
			checkConfig: func(t *testing.T, config *OrchestratorConfig) {
				if config.EnableFallback {
					t.Error("Expected fallback to be disabled")
				}
			},
		},
		{
			name: "with multiple options",
			options: []OrchestratorOption{
				WithCapabilityProvider("service", "http://multi-service:8080"),
				WithTelemetry(true),
				WithFallback(true),
			},
			checkConfig: func(t *testing.T, config *OrchestratorConfig) {
				if config.CapabilityProviderType != "service" {
					t.Errorf("Expected service provider, got %s", config.CapabilityProviderType)
				}
				if !config.EnableTelemetry {
					t.Error("Expected telemetry to be enabled")
				}
				if !config.EnableFallback {
					t.Error("Expected fallback to be enabled")
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			orchestrator, err := CreateOrchestratorWithOptions(deps, tt.options...)
			if err != nil {
				t.Fatalf("Unexpected error: %v", err)
			}

			if orchestrator == nil {
				t.Fatal("Expected orchestrator, got nil")
			}

			tt.checkConfig(t, orchestrator.config)
		})
	}
}

// TestOrchestratorOption functions test individual option functions
func TestOrchestratorOptions(t *testing.T) {
	t.Run("WithCapabilityProvider", func(t *testing.T) {
		config := DefaultConfig()
		opt := WithCapabilityProvider("service", "http://test:8080")
		opt(config)

		if config.CapabilityProviderType != "service" {
			t.Errorf("Expected service, got %s", config.CapabilityProviderType)
		}
		if config.CapabilityService.Endpoint != "http://test:8080" {
			t.Errorf("Expected http://test:8080, got %s", config.CapabilityService.Endpoint)
		}
	})

	t.Run("WithTelemetry", func(t *testing.T) {
		config := DefaultConfig()

		// Test enabling
		WithTelemetry(true)(config)
		if !config.EnableTelemetry {
			t.Error("Expected telemetry to be enabled")
		}

		// Test disabling
		WithTelemetry(false)(config)
		if config.EnableTelemetry {
			t.Error("Expected telemetry to be disabled")
		}
	})

	t.Run("WithFallback", func(t *testing.T) {
		config := DefaultConfig()

		// Test enabling
		WithFallback(true)(config)
		if !config.EnableFallback {
			t.Error("Expected fallback to be enabled")
		}

		// Test disabling
		WithFallback(false)(config)
		if config.EnableFallback {
			t.Error("Expected fallback to be disabled")
		}
	})

	t.Run("WithPerPhaseAIOptions", func(t *testing.T) {
		config := DefaultConfig()
		planOpts := &AIOptionsOverride{Model: StringPtr("smart")}
		synthesisOpts := &AIOptionsOverride{Temperature: Float32Ptr(0)}
		tieredOpts := &AIOptionsOverride{MaxTokens: IntPtr(777)}
		errorOpts := &AIOptionsOverride{Model: StringPtr("fast")}
		distillOpts := &AIOptionsOverride{Model: StringPtr("default")}

		WithPlanAIOptions(planOpts)(config)
		WithSynthesisAIOptions(synthesisOpts)(config)
		WithTieredSelectionAIOptions(tieredOpts)(config)
		WithErrorAnalysisAIOptions(errorOpts)(config)
		WithResultDistillAIOptions(distillOpts)(config)

		if config.PlanAIOptions != planOpts || config.SynthesisAIOptions != synthesisOpts || config.TieredSelectionAIOptions != tieredOpts || config.ErrorAnalysisAIOptions != errorOpts || config.ResultDistillAIOptions != distillOpts {
			t.Fatal("expected With*AIOptions setters to store the provided overrides")
		}
	})
}

func TestCreateOrchestrator_AppliesLegacyAIOptionFields(t *testing.T) {
	config := DefaultConfig()
	config.PlanAIOptions = nil
	config.SynthesisAIOptions = nil
	config.MicroResolutionAIOptions = nil
	config.PlanMaxTokens = 4321
	config.SynthesisTemperature = 0
	config.SynthesisMaxTokens = 1234
	config.MicroResolutionMaxTokens = 987

	orchestrator, err := CreateOrchestrator(config, OrchestratorDependencies{
		Discovery: NewMockDiscovery(),
		AIClient:  NewMockAIClient(),
	})
	if err != nil {
		t.Fatalf("CreateOrchestrator failed: %v", err)
	}

	if got := orchestrator.config.PlanAIOptions; got == nil || got.MaxTokens == nil || *got.MaxTokens != 4321 {
		t.Fatalf("expected legacy plan max tokens to be applied, got %#v", got)
	}
	if got := orchestrator.config.SynthesisAIOptions; got == nil || got.MaxTokens == nil || *got.MaxTokens != 1234 || got.Temperature == nil || *got.Temperature != 0 {
		t.Fatalf("expected legacy synthesis settings to be applied, got %#v", got)
	}
	if got := orchestrator.config.MicroResolutionAIOptions; got == nil || got.MaxTokens == nil || *got.MaxTokens != 987 {
		t.Fatalf("expected legacy micro-resolution settings to be applied, got %#v", got)
	}
}

func TestCreateOrchestrator_WiresPerPhaseAIOptions(t *testing.T) {
	config := DefaultConfig()
	config.EnableTieredResolution = true
	config.TieredSelectionAIOptions = &AIOptionsOverride{Model: StringPtr("tiered-model")}
	config.ErrorAnalysisAIOptions = &AIOptionsOverride{Model: StringPtr("error-model")}
	config.ResultTrim.Enabled = true
	config.ResultDistill.Enabled = true
	config.ResultDistillAIOptions = &AIOptionsOverride{Model: StringPtr("distill-model")}

	orchestrator, err := CreateOrchestrator(config, OrchestratorDependencies{
		Discovery:           NewMockDiscovery(),
		AIClient:            NewMockAIClient(),
		Logger:              &mockLogger{},
		EnableErrorAnalyzer: true,
	})
	if err != nil {
		t.Fatalf("CreateOrchestrator failed: %v", err)
	}

	tiered, ok := orchestrator.capabilityProvider.(*TieredCapabilityProvider)
	if !ok {
		t.Fatal("expected TieredCapabilityProvider")
	}
	if tiered.aiOptionsOverride != config.TieredSelectionAIOptions {
		t.Fatal("expected tiered selection override to be wired to TieredCapabilityProvider")
	}

	if orchestrator.executor.errorAnalyzer == nil {
		t.Fatal("expected error analyzer to be wired")
	}
	if orchestrator.executor.errorAnalyzer.aiOptionsOverride != config.ErrorAnalysisAIOptions {
		t.Fatal("expected error analysis override to be wired to ErrorAnalyzer")
	}

	distiller, ok := orchestrator.resultProcessor.(*LLMDistiller)
	if !ok {
		t.Fatalf("expected LLMDistiller result processor, got %T", orchestrator.resultProcessor)
	}
	if distiller.aiOptionsOverride != config.ResultDistillAIOptions {
		t.Fatal("expected result distill override to be wired to LLMDistiller")
	}
}

// TestDefaultConfig tests the default configuration
func TestDefaultConfig(t *testing.T) {
	// Clear any environment variables that might affect the test
	oldVal := os.Getenv("TRUVAG3_CAPABILITY_SERVICE_URL")
	_ = os.Unsetenv("TRUVAG3_CAPABILITY_SERVICE_URL")
	defer func() { _ = os.Setenv("TRUVAG3_CAPABILITY_SERVICE_URL", oldVal) }()

	config := DefaultConfig()

	// Check defaults
	if config.RoutingMode != ModeAutonomous {
		t.Errorf("Expected ModeAutonomous, got %s", config.RoutingMode)
	}
	if config.SynthesisStrategy != StrategyLLM {
		t.Errorf("Expected StrategyLLM, got %s", config.SynthesisStrategy)
	}
	if config.CapabilityProviderType != "default" {
		t.Errorf("Expected default provider, got %s", config.CapabilityProviderType)
	}
	if !config.EnableTelemetry {
		t.Error("Expected telemetry to be enabled by default")
	}
	if !config.EnableFallback {
		t.Error("Expected fallback to be enabled by default")
	}
	if config.HistorySize != 100 {
		t.Errorf("Expected history size 100, got %d", config.HistorySize)
	}
	if !config.CacheEnabled {
		t.Error("Expected cache to be enabled by default")
	}
	if config.CacheTTL != 5*time.Minute {
		t.Errorf("Expected cache TTL 5m, got %v", config.CacheTTL)
	}

	// Check plan parse retry defaults
	if !config.PlanParseRetryEnabled {
		t.Error("Expected PlanParseRetryEnabled to be true by default")
	}
	if config.PlanParseMaxRetries != 2 {
		t.Errorf("Expected PlanParseMaxRetries 2, got %d", config.PlanParseMaxRetries)
	}

	// Check execution options
	if config.ExecutionOptions.MaxConcurrency != 25 {
		t.Errorf("Expected max concurrency 25, got %d", config.ExecutionOptions.MaxConcurrency)
	}
	if config.ExecutionOptions.StepTimeout != 120*time.Second {
		t.Errorf("Expected step timeout 120s, got %v", config.ExecutionOptions.StepTimeout)
	}
}

// TestDefaultConfig_StepRetryMaxAttempts_Default guards that DefaultConfig reports
// ExecutionOptions.RetryAttempts=3 when the env var is unset. This is the mirror of
// TestSmartExecutor_DefaultMaxAttempts at the config surface — DefaultConfig drives
// the orchestrator's SetMaxAttempts call, so a silent revert here would override the
// executor default on any orchestrator that wires config through the factory.
func TestDefaultConfig_StepRetryMaxAttempts_Default(t *testing.T) {
	oldVal := os.Getenv("TRUVAG3_STEP_RETRY_MAX_ATTEMPTS")
	defer func() { _ = os.Setenv("TRUVAG3_STEP_RETRY_MAX_ATTEMPTS", oldVal) }()
	_ = os.Unsetenv("TRUVAG3_STEP_RETRY_MAX_ATTEMPTS")

	config := DefaultConfig()
	if config.ExecutionOptions.RetryAttempts != 3 {
		t.Errorf("DefaultConfig.ExecutionOptions.RetryAttempts = %d, want 3 (initial + 2 retries)",
			config.ExecutionOptions.RetryAttempts)
	}
}

// TestDefaultConfig_StepRetryMaxAttempts_EnvVar verifies that TRUVAG3_STEP_RETRY_MAX_ATTEMPTS
// flows through DefaultConfig. This is the second read site (the executor's NewSmartExecutor
// has its own read); both are required because DefaultConfig's SetMaxAttempts call would
// otherwise clobber the executor-side env value.
func TestDefaultConfig_StepRetryMaxAttempts_EnvVar(t *testing.T) {
	tests := []struct {
		name         string
		envValue     string
		wantAttempts int // 0 means expect default (3)
	}{
		{"valid integer overrides default", "5", 5},
		{"minimum value of 1", "1", 1},
		{"zero is rejected (keeps default)", "0", 0},
		{"negative is rejected (keeps default)", "-1", 0},
		{"non-numeric is rejected (keeps default)", "many", 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			oldVal := os.Getenv("TRUVAG3_STEP_RETRY_MAX_ATTEMPTS")
			defer func() { _ = os.Setenv("TRUVAG3_STEP_RETRY_MAX_ATTEMPTS", oldVal) }()
			_ = os.Setenv("TRUVAG3_STEP_RETRY_MAX_ATTEMPTS", tt.envValue)

			config := DefaultConfig()

			expected := tt.wantAttempts
			if expected == 0 {
				expected = 3
			}
			if config.ExecutionOptions.RetryAttempts != expected {
				t.Errorf("RetryAttempts = %d, want %d", config.ExecutionOptions.RetryAttempts, expected)
			}
		})
	}
}

// TestDefaultConfig_EnvironmentAutoConfiguration tests auto-configuration from environment
func TestDefaultConfig_EnvironmentAutoConfiguration(t *testing.T) {
	// Set environment variable
	_ = os.Setenv("TRUVAG3_CAPABILITY_SERVICE_URL", "http://auto-config:9090")
	defer func() { _ = os.Unsetenv("TRUVAG3_CAPABILITY_SERVICE_URL") }()

	config := DefaultConfig()

	// Should auto-configure to service provider
	if config.CapabilityProviderType != "service" {
		t.Errorf("Expected service provider from env, got %s", config.CapabilityProviderType)
	}
	if config.CapabilityService.Endpoint != "http://auto-config:9090" {
		t.Errorf("Expected endpoint from env, got %s", config.CapabilityService.Endpoint)
	}
}

func TestDefaultConfig_OAuthToken_FromEnvironment(t *testing.T) {
	t.Setenv("TRUVAG3_OAUTH_TOKEN", "test-token-abc123")

	config := DefaultConfig()
	if config.OAuthToken != "test-token-abc123" {
		t.Errorf("Expected OAuthToken from env, got %q", config.OAuthToken)
	}
}

func TestDefaultConfig_OAuthToken_EmptyWhenUnset(t *testing.T) {
	t.Setenv("TRUVAG3_OAUTH_TOKEN", "")

	config := DefaultConfig()
	if config.OAuthToken != "" {
		t.Errorf("Expected empty OAuthToken when unset, got %q", config.OAuthToken)
	}
}

func TestDefaultConfig_OAuthToken_NotInJSON(t *testing.T) {
	t.Setenv("TRUVAG3_OAUTH_TOKEN", "secret-token")

	config := DefaultConfig()
	data, _ := json.Marshal(config)
	if strings.Contains(string(data), "secret-token") {
		t.Error("OAuthToken should not appear in JSON output (json:\"-\" tag)")
	}
}

func TestDefaultConfig_PropagatedHeaders_NotInJSON(t *testing.T) {
	config := DefaultConfig()
	config.PropagatedHeaders = map[string]string{
		"X-Secret-Tenant": "tenant-42",
		"X-Internal-Key":  "internal-value",
	}
	data, _ := json.Marshal(config)
	if strings.Contains(string(data), "tenant-42") {
		t.Error("PropagatedHeaders values should not appear in JSON output (json:\"-\" tag)")
	}
	if strings.Contains(string(data), "X-Secret-Tenant") {
		t.Error("PropagatedHeaders keys should not appear in JSON output (json:\"-\" tag)")
	}
}

// TestDependencyInjection tests that dependencies are properly injected
func TestDependencyInjection(t *testing.T) {
	// Create mock dependencies
	mockCB := &mockCircuitBreaker{
		executeFunc: func(ctx context.Context, fn func() error) error {
			return fn()
		},
	}

	mockLog := &mockLogger{
		debugFunc: func(msg string, fields map[string]interface{}) {
			// Logger called
		},
	}

	deps := OrchestratorDependencies{
		Discovery:      NewMockDiscovery(),
		AIClient:       NewMockAIClient(),
		CircuitBreaker: mockCB,
		Logger:         mockLog,
	}

	// Configure for service provider (disable tiered to test service provider injection)
	config := DefaultConfig()
	config.EnableTieredResolution = false // Disable to test ServiceCapabilityProvider
	config.CapabilityProviderType = "service"
	config.CapabilityService.Endpoint = "http://test:8080"

	orchestrator, err := CreateOrchestrator(config, deps)
	if err != nil {
		t.Fatalf("Failed to create orchestrator: %v", err)
	}

	// Verify dependencies were injected into ServiceCapabilityProvider
	serviceProvider, ok := orchestrator.capabilityProvider.(*ServiceCapabilityProvider)
	if !ok {
		t.Fatal("Expected ServiceCapabilityProvider")
	}

	if serviceProvider.circuitBreaker == nil {
		t.Error("Expected circuit breaker to be injected")
	}
	if serviceProvider.logger == nil {
		t.Error("Expected logger to be injected")
	}
}

// TestFactoryErrorCases tests error handling in factory functions
func TestFactoryErrorCases(t *testing.T) {
	t.Run("service provider without endpoint", func(t *testing.T) {
		deps := OrchestratorDependencies{
			Discovery: NewMockDiscovery(),
			AIClient:  NewMockAIClient(),
		}

		config := &OrchestratorConfig{
			CapabilityProviderType: "service",
			// No endpoint specified
		}

		_, err := CreateOrchestrator(config, deps)
		if err == nil {
			t.Error("Expected error for service provider without endpoint")
		}
		if err != nil && !stringContains(err.Error(), "capability service URL required") {
			t.Errorf("Expected error about missing URL, got: %v", err)
		}
	})
}

// Test helper for dependency functions
func TestWithCircuitBreaker(t *testing.T) {
	cb := &mockCircuitBreaker{}
	deps := &OrchestratorDependencies{
		Discovery: NewMockDiscovery(),
		AIClient:  NewMockAIClient(),
	}

	withCB := WithCircuitBreaker(cb)
	withCB(deps)

	if deps.CircuitBreaker != cb {
		t.Error("Expected circuit breaker to be set")
	}
}

func TestWithLogger(t *testing.T) {
	logger := &mockLogger{}
	deps := &OrchestratorDependencies{
		Discovery: NewMockDiscovery(),
		AIClient:  NewMockAIClient(),
	}

	withLogger := WithLogger(logger)
	withLogger(deps)

	if deps.Logger != logger {
		t.Error("Expected logger to be set")
	}
}

// Mocks are now in test_mocks.go to avoid duplication

// =============================================================================
// PromptBuilder Factory Integration Tests
// =============================================================================

// TestCreateOrchestrator_PromptBuilder_Layer1_Default tests that factory creates
// DefaultPromptBuilder when no template or custom builder is provided
func TestCreateOrchestrator_PromptBuilder_Layer1_Default(t *testing.T) {
	deps := OrchestratorDependencies{
		Discovery: NewMockDiscovery(),
		AIClient:  NewMockAIClient(),
		Logger:    &mockLogger{},
	}

	config := DefaultConfig()
	// No template file, no template string, no custom builder
	// Should use DefaultPromptBuilder (Layer 1)

	orchestrator, err := CreateOrchestrator(config, deps)
	if err != nil {
		t.Fatalf("Failed to create orchestrator: %v", err)
	}

	if orchestrator.promptBuilder == nil {
		t.Fatal("Expected promptBuilder to be set")
	}

	// Verify it's a DefaultPromptBuilder
	_, ok := orchestrator.promptBuilder.(*DefaultPromptBuilder)
	if !ok {
		t.Errorf("Expected DefaultPromptBuilder, got %T", orchestrator.promptBuilder)
	}
}

// TestCreateOrchestrator_PromptBuilder_Layer1_WithTypeRules tests DefaultPromptBuilder
// with additional type rules
func TestCreateOrchestrator_PromptBuilder_Layer1_WithTypeRules(t *testing.T) {
	deps := OrchestratorDependencies{
		Discovery: NewMockDiscovery(),
		AIClient:  NewMockAIClient(),
		Logger:    &mockLogger{},
	}

	config := DefaultConfig()
	config.PromptConfig = PromptConfig{
		Domain: "healthcare",
		AdditionalTypeRules: []TypeRule{
			{
				TypeNames: []string{"patient_id"},
				JsonType:  "JSON strings",
				Example:   `"P12345"`,
			},
		},
	}

	orchestrator, err := CreateOrchestrator(config, deps)
	if err != nil {
		t.Fatalf("Failed to create orchestrator: %v", err)
	}

	if orchestrator.promptBuilder == nil {
		t.Fatal("Expected promptBuilder to be set")
	}

	// Verify it's a DefaultPromptBuilder with additional rules
	builder, ok := orchestrator.promptBuilder.(*DefaultPromptBuilder)
	if !ok {
		t.Fatalf("Expected DefaultPromptBuilder, got %T", orchestrator.promptBuilder)
	}

	// Should have default rules + 1 additional rule
	rules := builder.GetTypeRules()
	if len(rules) < 7 { // 6 default + 1 additional
		t.Errorf("Expected at least 7 type rules, got %d", len(rules))
	}

	// Verify domain is set
	cfg := builder.GetConfig()
	if cfg.Domain != "healthcare" {
		t.Errorf("Expected domain 'healthcare', got '%s'", cfg.Domain)
	}
}

// TestCreateOrchestrator_PromptBuilder_Layer2_Template tests that factory creates
// TemplatePromptBuilder when template is provided
func TestCreateOrchestrator_PromptBuilder_Layer2_Template(t *testing.T) {
	deps := OrchestratorDependencies{
		Discovery: NewMockDiscovery(),
		AIClient:  NewMockAIClient(),
		Logger:    &mockLogger{},
	}

	config := DefaultConfig()
	config.PromptConfig = PromptConfig{
		Template: `You are orchestrating: {{.Request}}
Available: {{.CapabilityInfo}}
{{.TypeRules}}`,
	}

	orchestrator, err := CreateOrchestrator(config, deps)
	if err != nil {
		t.Fatalf("Failed to create orchestrator: %v", err)
	}

	if orchestrator.promptBuilder == nil {
		t.Fatal("Expected promptBuilder to be set")
	}

	// Verify it's a TemplatePromptBuilder
	_, ok := orchestrator.promptBuilder.(*TemplatePromptBuilder)
	if !ok {
		t.Errorf("Expected TemplatePromptBuilder, got %T", orchestrator.promptBuilder)
	}
}

// TestCreateOrchestrator_PromptBuilder_Layer2_TemplateFile tests template file loading
func TestCreateOrchestrator_PromptBuilder_Layer2_TemplateFile(t *testing.T) {
	// Create a temporary template file
	tmpFile, err := os.CreateTemp("", "prompt-template-*.tmpl")
	if err != nil {
		t.Fatalf("Failed to create temp file: %v", err)
	}
	defer func() { _ = os.Remove(tmpFile.Name()) }()

	templateContent := `You are an AI orchestrator.
Request: {{.Request}}
Capabilities: {{.CapabilityInfo}}
{{.TypeRules}}`
	if _, err := tmpFile.WriteString(templateContent); err != nil {
		t.Fatalf("Failed to write template: %v", err)
	}
	_ = tmpFile.Close()

	deps := OrchestratorDependencies{
		Discovery: NewMockDiscovery(),
		AIClient:  NewMockAIClient(),
		Logger:    &mockLogger{},
	}

	config := DefaultConfig()
	config.PromptConfig = PromptConfig{
		TemplateFile: tmpFile.Name(),
	}

	orchestrator, err := CreateOrchestrator(config, deps)
	if err != nil {
		t.Fatalf("Failed to create orchestrator: %v", err)
	}

	if orchestrator.promptBuilder == nil {
		t.Fatal("Expected promptBuilder to be set")
	}

	// Verify it's a TemplatePromptBuilder
	builder, ok := orchestrator.promptBuilder.(*TemplatePromptBuilder)
	if !ok {
		t.Errorf("Expected TemplatePromptBuilder, got %T", orchestrator.promptBuilder)
	}

	// Verify fallback is also initialized
	if builder.GetFallback() == nil {
		t.Error("Expected fallback builder to be set")
	}
}

// TestCreateOrchestrator_PromptBuilder_Layer3_Custom tests custom builder injection
func TestCreateOrchestrator_PromptBuilder_Layer3_Custom(t *testing.T) {
	customBuilder := &mockPromptBuilder{
		buildFunc: func(ctx context.Context, input PromptInput) (string, error) {
			return "Custom prompt: " + input.Request, nil
		},
	}

	deps := OrchestratorDependencies{
		Discovery:     NewMockDiscovery(),
		AIClient:      NewMockAIClient(),
		Logger:        &mockLogger{},
		PromptBuilder: customBuilder,
	}

	config := DefaultConfig()
	// Even if template is set, custom builder takes precedence
	config.PromptConfig = PromptConfig{
		Template: "This should be ignored",
	}

	orchestrator, err := CreateOrchestrator(config, deps)
	if err != nil {
		t.Fatalf("Failed to create orchestrator: %v", err)
	}

	if orchestrator.promptBuilder == nil {
		t.Fatal("Expected promptBuilder to be set")
	}

	// Verify it's our custom builder (Layer 3 takes precedence)
	if orchestrator.promptBuilder != customBuilder {
		t.Errorf("Expected custom builder, got %T", orchestrator.promptBuilder)
	}
}

// TestCreateOrchestrator_PromptBuilder_GracefulDegradation tests fallback when
// template fails to load
func TestCreateOrchestrator_PromptBuilder_GracefulDegradation(t *testing.T) {
	deps := OrchestratorDependencies{
		Discovery: NewMockDiscovery(),
		AIClient:  NewMockAIClient(),
		Logger:    &mockLogger{},
	}

	config := DefaultConfig()
	config.PromptConfig = PromptConfig{
		TemplateFile: "/nonexistent/path/template.tmpl", // File doesn't exist
	}

	// Should gracefully degrade to DefaultPromptBuilder, not error
	orchestrator, err := CreateOrchestrator(config, deps)
	if err != nil {
		t.Fatalf("Expected graceful degradation, got error: %v", err)
	}

	if orchestrator.promptBuilder == nil {
		t.Fatal("Expected promptBuilder to be set (fallback)")
	}

	// Should fall back to DefaultPromptBuilder
	_, ok := orchestrator.promptBuilder.(*DefaultPromptBuilder)
	if !ok {
		t.Errorf("Expected DefaultPromptBuilder fallback, got %T", orchestrator.promptBuilder)
	}
}

// TestCreateOrchestrator_PromptBuilder_InvalidTemplate tests graceful degradation
// when template has syntax errors
func TestCreateOrchestrator_PromptBuilder_InvalidTemplate(t *testing.T) {
	deps := OrchestratorDependencies{
		Discovery: NewMockDiscovery(),
		AIClient:  NewMockAIClient(),
		Logger:    &mockLogger{},
	}

	config := DefaultConfig()
	config.PromptConfig = PromptConfig{
		Template: "{{.Invalid syntax here", // Invalid template
	}

	// Should gracefully degrade to DefaultPromptBuilder
	orchestrator, err := CreateOrchestrator(config, deps)
	if err != nil {
		t.Fatalf("Expected graceful degradation, got error: %v", err)
	}

	if orchestrator.promptBuilder == nil {
		t.Fatal("Expected promptBuilder to be set (fallback)")
	}

	// Should fall back to DefaultPromptBuilder
	_, ok := orchestrator.promptBuilder.(*DefaultPromptBuilder)
	if !ok {
		t.Errorf("Expected DefaultPromptBuilder fallback, got %T", orchestrator.promptBuilder)
	}
}

// TestCreateOrchestrator_PromptBuilder_DependencyInjection tests that logger and
// telemetry are properly injected into PromptBuilder
func TestCreateOrchestrator_PromptBuilder_DependencyInjection(t *testing.T) {
	mockLog := &mockLogger{}
	mockTel := &mockTelemetry{}

	deps := OrchestratorDependencies{
		Discovery: NewMockDiscovery(),
		AIClient:  NewMockAIClient(),
		Logger:    mockLog,
		Telemetry: mockTel,
	}

	config := DefaultConfig()
	config.PromptConfig = PromptConfig{
		Domain: "finance",
	}

	orchestrator, err := CreateOrchestrator(config, deps)
	if err != nil {
		t.Fatalf("Failed to create orchestrator: %v", err)
	}

	// Build a prompt to verify dependencies work
	builder, ok := orchestrator.promptBuilder.(*DefaultPromptBuilder)
	if !ok {
		t.Fatalf("Expected DefaultPromptBuilder, got %T", orchestrator.promptBuilder)
	}

	// The builder should have logger set (we can verify by building a prompt)
	prompt, err := builder.BuildPlanningPrompt(context.Background(), PromptInput{
		CapabilityInfo: "Test capabilities",
		Request:        "Test request",
	})
	if err != nil {
		t.Errorf("BuildPlanningPrompt failed: %v", err)
	}

	// Prompt should include finance domain section
	if !stringContains(prompt, "FINANCE DOMAIN") {
		t.Error("Expected finance domain section in prompt")
	}
}

// mockPromptBuilder implements PromptBuilder for testing
type mockPromptBuilder struct {
	buildFunc func(ctx context.Context, input PromptInput) (string, error)
}

func (m *mockPromptBuilder) BuildPlanningPrompt(ctx context.Context, input PromptInput) (string, error) {
	if m.buildFunc != nil {
		return m.buildFunc(ctx, input)
	}
	return "mock prompt", nil
}

// =============================================================================
// Plan Parse Retry Configuration Tests
// =============================================================================

// TestDefaultConfig_PlanParseRetry_EnvironmentConfiguration tests env var loading
func TestDefaultConfig_PlanParseRetry_EnvironmentConfiguration(t *testing.T) {
	// Save and clear any existing env vars
	oldEnabled := os.Getenv("TRUVAG3_PLAN_RETRY_ENABLED")
	oldMax := os.Getenv("TRUVAG3_PLAN_RETRY_MAX")
	defer func() {
		if oldEnabled != "" {
			_ = os.Setenv("TRUVAG3_PLAN_RETRY_ENABLED", oldEnabled)
		} else {
			_ = os.Unsetenv("TRUVAG3_PLAN_RETRY_ENABLED")
		}
		if oldMax != "" {
			_ = os.Setenv("TRUVAG3_PLAN_RETRY_MAX", oldMax)
		} else {
			_ = os.Unsetenv("TRUVAG3_PLAN_RETRY_MAX")
		}
	}()

	t.Run("disable retry via env", func(t *testing.T) {
		_ = os.Setenv("TRUVAG3_PLAN_RETRY_ENABLED", "false")
		_ = os.Unsetenv("TRUVAG3_PLAN_RETRY_MAX")

		config := DefaultConfig()

		if config.PlanParseRetryEnabled {
			t.Error("Expected PlanParseRetryEnabled to be false from env")
		}
	})

	t.Run("enable retry via env", func(t *testing.T) {
		_ = os.Setenv("TRUVAG3_PLAN_RETRY_ENABLED", "true")
		_ = os.Unsetenv("TRUVAG3_PLAN_RETRY_MAX")

		config := DefaultConfig()

		if !config.PlanParseRetryEnabled {
			t.Error("Expected PlanParseRetryEnabled to be true from env")
		}
	})

	t.Run("set max retries via env", func(t *testing.T) {
		_ = os.Unsetenv("TRUVAG3_PLAN_RETRY_ENABLED")
		_ = os.Setenv("TRUVAG3_PLAN_RETRY_MAX", "5")

		config := DefaultConfig()

		if config.PlanParseMaxRetries != 5 {
			t.Errorf("Expected PlanParseMaxRetries 5, got %d", config.PlanParseMaxRetries)
		}
	})

	t.Run("invalid max retries ignored", func(t *testing.T) {
		_ = os.Unsetenv("TRUVAG3_PLAN_RETRY_ENABLED")
		_ = os.Setenv("TRUVAG3_PLAN_RETRY_MAX", "invalid")

		config := DefaultConfig()

		// Should keep default value of 2
		if config.PlanParseMaxRetries != 2 {
			t.Errorf("Expected PlanParseMaxRetries 2 (default), got %d", config.PlanParseMaxRetries)
		}
	})

	t.Run("negative max retries ignored", func(t *testing.T) {
		_ = os.Unsetenv("TRUVAG3_PLAN_RETRY_ENABLED")
		_ = os.Setenv("TRUVAG3_PLAN_RETRY_MAX", "-1")

		config := DefaultConfig()

		// Should keep default value of 2 (negative values are invalid)
		if config.PlanParseMaxRetries != 2 {
			t.Errorf("Expected PlanParseMaxRetries 2 (default), got %d", config.PlanParseMaxRetries)
		}
	})

	t.Run("zero max retries is valid", func(t *testing.T) {
		_ = os.Unsetenv("TRUVAG3_PLAN_RETRY_ENABLED")
		_ = os.Setenv("TRUVAG3_PLAN_RETRY_MAX", "0")

		config := DefaultConfig()

		// Zero is valid (means no retries)
		if config.PlanParseMaxRetries != 0 {
			t.Errorf("Expected PlanParseMaxRetries 0, got %d", config.PlanParseMaxRetries)
		}
	})
}

// TestWithPlanParseRetry tests the functional option
func TestWithPlanParseRetry(t *testing.T) {
	t.Run("enable with max retries", func(t *testing.T) {
		config := DefaultConfig()
		opt := WithPlanParseRetry(true, 5)
		opt(config)

		if !config.PlanParseRetryEnabled {
			t.Error("Expected PlanParseRetryEnabled to be true")
		}
		if config.PlanParseMaxRetries != 5 {
			t.Errorf("Expected PlanParseMaxRetries 5, got %d", config.PlanParseMaxRetries)
		}
	})

	t.Run("disable retry", func(t *testing.T) {
		config := DefaultConfig()
		opt := WithPlanParseRetry(false, 0)
		opt(config)

		if config.PlanParseRetryEnabled {
			t.Error("Expected PlanParseRetryEnabled to be false")
		}
		if config.PlanParseMaxRetries != 0 {
			t.Errorf("Expected PlanParseMaxRetries 0, got %d", config.PlanParseMaxRetries)
		}
	})

	t.Run("negative max retries ignored", func(t *testing.T) {
		config := DefaultConfig()
		// Start with known values
		config.PlanParseMaxRetries = 3

		opt := WithPlanParseRetry(true, -1)
		opt(config)

		// Should not change the max retries when negative
		if config.PlanParseMaxRetries != 3 {
			t.Errorf("Expected PlanParseMaxRetries to remain 3, got %d", config.PlanParseMaxRetries)
		}
	})
}

// TestCreateOrchestratorWithOptions_PlanParseRetry tests orchestrator creation with retry options
func TestCreateOrchestratorWithOptions_PlanParseRetry(t *testing.T) {
	deps := OrchestratorDependencies{
		Discovery: NewMockDiscovery(),
		AIClient:  NewMockAIClient(),
	}

	orchestrator, err := CreateOrchestratorWithOptions(deps, WithPlanParseRetry(true, 3))
	if err != nil {
		t.Fatalf("Failed to create orchestrator: %v", err)
	}

	if !orchestrator.config.PlanParseRetryEnabled {
		t.Error("Expected PlanParseRetryEnabled to be true")
	}
	if orchestrator.config.PlanParseMaxRetries != 3 {
		t.Errorf("Expected PlanParseMaxRetries 3, got %d", orchestrator.config.PlanParseMaxRetries)
	}
}

// =============================================================================
// LLM Max Tokens Configuration Tests
// =============================================================================

// TestDefaultConfig_MaxTokens_Defaults tests default values for PlanMaxTokens, SynthesisMaxTokens, and SynthesisTemperature
func TestDefaultConfig_MaxTokens_Defaults(t *testing.T) {
	// Clear any env vars that could interfere
	_ = os.Unsetenv("TRUVAG3_PLAN_MAX_TOKENS")
	_ = os.Unsetenv("TRUVAG3_SYNTHESIS_MAX_TOKENS")
	_ = os.Unsetenv("TRUVAG3_SYNTHESIS_TEMPERATURE")

	config := DefaultConfig()

	if config.PlanMaxTokens != 15000 {
		t.Errorf("Expected PlanMaxTokens default 15000, got %d", config.PlanMaxTokens)
	}
	if config.SynthesisMaxTokens != 5000 {
		t.Errorf("Expected SynthesisMaxTokens default 5000, got %d", config.SynthesisMaxTokens)
	}
	if config.SynthesisTemperature != 0.5 {
		t.Errorf("Expected SynthesisTemperature default 0.5, got %f", config.SynthesisTemperature)
	}
}

// TestDefaultConfig_MaxTokens_EnvironmentConfiguration tests env var loading for max tokens
func TestDefaultConfig_MaxTokens_EnvironmentConfiguration(t *testing.T) {
	// Save and clear any existing env vars
	oldPlan := os.Getenv("TRUVAG3_PLAN_MAX_TOKENS")
	oldSynthesis := os.Getenv("TRUVAG3_SYNTHESIS_MAX_TOKENS")
	defer func() {
		if oldPlan != "" {
			_ = os.Setenv("TRUVAG3_PLAN_MAX_TOKENS", oldPlan)
		} else {
			_ = os.Unsetenv("TRUVAG3_PLAN_MAX_TOKENS")
		}
		if oldSynthesis != "" {
			_ = os.Setenv("TRUVAG3_SYNTHESIS_MAX_TOKENS", oldSynthesis)
		} else {
			_ = os.Unsetenv("TRUVAG3_SYNTHESIS_MAX_TOKENS")
		}
	}()

	t.Run("override PlanMaxTokens via env", func(t *testing.T) {
		_ = os.Setenv("TRUVAG3_PLAN_MAX_TOKENS", "5000")
		_ = os.Unsetenv("TRUVAG3_SYNTHESIS_MAX_TOKENS")

		config := DefaultConfig()

		if config.PlanMaxTokens != 5000 {
			t.Errorf("Expected PlanMaxTokens 5000, got %d", config.PlanMaxTokens)
		}
		// SynthesisMaxTokens should remain default
		if config.SynthesisMaxTokens != 5000 {
			t.Errorf("Expected SynthesisMaxTokens 5000 (default), got %d", config.SynthesisMaxTokens)
		}
	})

	t.Run("override SynthesisMaxTokens via env", func(t *testing.T) {
		_ = os.Unsetenv("TRUVAG3_PLAN_MAX_TOKENS")
		_ = os.Setenv("TRUVAG3_SYNTHESIS_MAX_TOKENS", "3000")

		config := DefaultConfig()

		// PlanMaxTokens should remain default
		if config.PlanMaxTokens != 15000 {
			t.Errorf("Expected PlanMaxTokens 15000 (default), got %d", config.PlanMaxTokens)
		}
		if config.SynthesisMaxTokens != 3000 {
			t.Errorf("Expected SynthesisMaxTokens 3000, got %d", config.SynthesisMaxTokens)
		}
	})

	t.Run("invalid PlanMaxTokens keeps default", func(t *testing.T) {
		_ = os.Setenv("TRUVAG3_PLAN_MAX_TOKENS", "invalid")
		_ = os.Unsetenv("TRUVAG3_SYNTHESIS_MAX_TOKENS")

		config := DefaultConfig()

		if config.PlanMaxTokens != 15000 {
			t.Errorf("Expected PlanMaxTokens 15000 (default), got %d", config.PlanMaxTokens)
		}
	})

	t.Run("negative PlanMaxTokens keeps default", func(t *testing.T) {
		_ = os.Setenv("TRUVAG3_PLAN_MAX_TOKENS", "-1")
		_ = os.Unsetenv("TRUVAG3_SYNTHESIS_MAX_TOKENS")

		config := DefaultConfig()

		if config.PlanMaxTokens != 15000 {
			t.Errorf("Expected PlanMaxTokens 15000 (default), got %d", config.PlanMaxTokens)
		}
	})

	t.Run("zero PlanMaxTokens keeps default", func(t *testing.T) {
		_ = os.Setenv("TRUVAG3_PLAN_MAX_TOKENS", "0")
		_ = os.Unsetenv("TRUVAG3_SYNTHESIS_MAX_TOKENS")

		config := DefaultConfig()

		// val > 0 guard should reject zero
		if config.PlanMaxTokens != 15000 {
			t.Errorf("Expected PlanMaxTokens 15000 (default), got %d", config.PlanMaxTokens)
		}
	})
}

// TestWithPlanMaxTokens tests the functional option
func TestWithPlanMaxTokens(t *testing.T) {
	t.Run("set valid max tokens", func(t *testing.T) {
		config := DefaultConfig()
		opt := WithPlanMaxTokens(5000)
		opt(config)

		if config.PlanMaxTokens != 5000 {
			t.Errorf("Expected PlanMaxTokens 5000, got %d", config.PlanMaxTokens)
		}
	})

	t.Run("zero max tokens ignored", func(t *testing.T) {
		config := DefaultConfig()
		opt := WithPlanMaxTokens(0)
		opt(config)

		if config.PlanMaxTokens != 15000 {
			t.Errorf("Expected PlanMaxTokens to remain 15000, got %d", config.PlanMaxTokens)
		}
	})

	t.Run("negative max tokens ignored", func(t *testing.T) {
		config := DefaultConfig()
		opt := WithPlanMaxTokens(-1)
		opt(config)

		if config.PlanMaxTokens != 15000 {
			t.Errorf("Expected PlanMaxTokens to remain 15000, got %d", config.PlanMaxTokens)
		}
	})
}

// TestWithSynthesisMaxTokens tests the functional option
func TestWithSynthesisMaxTokens(t *testing.T) {
	t.Run("set valid max tokens", func(t *testing.T) {
		config := DefaultConfig()
		opt := WithSynthesisMaxTokens(3000)
		opt(config)

		if config.SynthesisMaxTokens != 3000 {
			t.Errorf("Expected SynthesisMaxTokens 3000, got %d", config.SynthesisMaxTokens)
		}
	})

	t.Run("zero max tokens ignored", func(t *testing.T) {
		config := DefaultConfig()
		opt := WithSynthesisMaxTokens(0)
		opt(config)

		if config.SynthesisMaxTokens != 5000 {
			t.Errorf("Expected SynthesisMaxTokens to remain 25000, got %d", config.SynthesisMaxTokens)
		}
	})

	t.Run("negative max tokens ignored", func(t *testing.T) {
		config := DefaultConfig()
		opt := WithSynthesisMaxTokens(-1)
		opt(config)

		if config.SynthesisMaxTokens != 5000 {
			t.Errorf("Expected SynthesisMaxTokens to remain 25000, got %d", config.SynthesisMaxTokens)
		}
	})
}

// TestDefaultConfig_TieredSelectionMaxTokens_Default tests default value for SelectionMaxTokens
func TestDefaultConfig_TieredSelectionMaxTokens_Default(t *testing.T) {
	_ = os.Unsetenv("TRUVAG3_TIERED_SELECTION_MAX_TOKENS")

	config := DefaultConfig()

	if config.TieredResolution.SelectionMaxTokens != 2000 {
		t.Errorf("Expected SelectionMaxTokens default 2000, got %d", config.TieredResolution.SelectionMaxTokens)
	}
}

// TestDefaultConfig_TieredSelectionMaxTokens_EnvironmentConfiguration tests env var loading
func TestDefaultConfig_TieredSelectionMaxTokens_EnvironmentConfiguration(t *testing.T) {
	oldVal := os.Getenv("TRUVAG3_TIERED_SELECTION_MAX_TOKENS")
	defer func() {
		if oldVal != "" {
			_ = os.Setenv("TRUVAG3_TIERED_SELECTION_MAX_TOKENS", oldVal)
		} else {
			_ = os.Unsetenv("TRUVAG3_TIERED_SELECTION_MAX_TOKENS")
		}
	}()

	t.Run("override SelectionMaxTokens via env", func(t *testing.T) {
		_ = os.Setenv("TRUVAG3_TIERED_SELECTION_MAX_TOKENS", "3000")

		config := DefaultConfig()

		if config.TieredResolution.SelectionMaxTokens != 3000 {
			t.Errorf("Expected SelectionMaxTokens 3000, got %d", config.TieredResolution.SelectionMaxTokens)
		}
	})

	t.Run("invalid SelectionMaxTokens keeps default", func(t *testing.T) {
		_ = os.Setenv("TRUVAG3_TIERED_SELECTION_MAX_TOKENS", "abc")

		config := DefaultConfig()

		if config.TieredResolution.SelectionMaxTokens != 2000 {
			t.Errorf("Expected SelectionMaxTokens 2000 (default), got %d", config.TieredResolution.SelectionMaxTokens)
		}
	})

	t.Run("negative SelectionMaxTokens keeps default", func(t *testing.T) {
		_ = os.Setenv("TRUVAG3_TIERED_SELECTION_MAX_TOKENS", "-1")

		config := DefaultConfig()

		if config.TieredResolution.SelectionMaxTokens != 2000 {
			t.Errorf("Expected SelectionMaxTokens 2000 (default), got %d", config.TieredResolution.SelectionMaxTokens)
		}
	})

	t.Run("zero SelectionMaxTokens keeps default", func(t *testing.T) {
		_ = os.Setenv("TRUVAG3_TIERED_SELECTION_MAX_TOKENS", "0")

		config := DefaultConfig()

		if config.TieredResolution.SelectionMaxTokens != 2000 {
			t.Errorf("Expected SelectionMaxTokens 2000 (default), got %d", config.TieredResolution.SelectionMaxTokens)
		}
	})
}

// TestWithTieredSelectionMaxTokens tests the functional option
func TestWithTieredSelectionMaxTokens(t *testing.T) {
	t.Run("set valid max tokens", func(t *testing.T) {
		config := DefaultConfig()
		opt := WithTieredSelectionMaxTokens(3000)
		opt(config)

		if config.TieredResolution.SelectionMaxTokens != 3000 {
			t.Errorf("Expected SelectionMaxTokens 3000, got %d", config.TieredResolution.SelectionMaxTokens)
		}
	})

	t.Run("zero max tokens ignored", func(t *testing.T) {
		config := DefaultConfig()
		opt := WithTieredSelectionMaxTokens(0)
		opt(config)

		if config.TieredResolution.SelectionMaxTokens != 2000 {
			t.Errorf("Expected SelectionMaxTokens to remain 2000, got %d", config.TieredResolution.SelectionMaxTokens)
		}
	})

	t.Run("negative max tokens ignored", func(t *testing.T) {
		config := DefaultConfig()
		opt := WithTieredSelectionMaxTokens(-1)
		opt(config)

		if config.TieredResolution.SelectionMaxTokens != 2000 {
			t.Errorf("Expected SelectionMaxTokens to remain 2000, got %d", config.TieredResolution.SelectionMaxTokens)
		}
	})
}

// TestWithTieredSelectionMaxTokens_OptionOverridesEnv tests that functional options take precedence over env vars
func TestWithTieredSelectionMaxTokens_OptionOverridesEnv(t *testing.T) {
	oldVal := os.Getenv("TRUVAG3_TIERED_SELECTION_MAX_TOKENS")
	defer func() {
		if oldVal != "" {
			_ = os.Setenv("TRUVAG3_TIERED_SELECTION_MAX_TOKENS", oldVal)
		} else {
			_ = os.Unsetenv("TRUVAG3_TIERED_SELECTION_MAX_TOKENS")
		}
	}()

	// Set env var to 3000
	_ = os.Setenv("TRUVAG3_TIERED_SELECTION_MAX_TOKENS", "3000")

	deps := OrchestratorDependencies{
		Discovery: NewMockDiscovery(),
		AIClient:  NewMockAIClient(),
	}

	// Functional option should override env var
	orchestrator, err := CreateOrchestratorWithOptions(deps, WithTieredSelectionMaxTokens(5000))
	if err != nil {
		t.Fatalf("Failed to create orchestrator: %v", err)
	}

	if orchestrator.config.TieredResolution.SelectionMaxTokens != 5000 {
		t.Errorf("Expected SelectionMaxTokens 5000 (option override), got %d", orchestrator.config.TieredResolution.SelectionMaxTokens)
	}
}

// TestDefaultConfig_MaxConcurrency_Default tests default value for MaxConcurrency
func TestDefaultConfig_MaxConcurrency_Default(t *testing.T) {
	_ = os.Unsetenv("TRUVAG3_EXECUTION_MAX_CONCURRENCY")

	config := DefaultConfig()

	if config.ExecutionOptions.MaxConcurrency != 25 {
		t.Errorf("Expected MaxConcurrency default 25, got %d", config.ExecutionOptions.MaxConcurrency)
	}
}

// TestDefaultConfig_MaxConcurrency_EnvironmentConfiguration tests env var loading
func TestDefaultConfig_MaxConcurrency_EnvironmentConfiguration(t *testing.T) {
	oldVal := os.Getenv("TRUVAG3_EXECUTION_MAX_CONCURRENCY")
	defer func() {
		if oldVal != "" {
			_ = os.Setenv("TRUVAG3_EXECUTION_MAX_CONCURRENCY", oldVal)
		} else {
			_ = os.Unsetenv("TRUVAG3_EXECUTION_MAX_CONCURRENCY")
		}
	}()

	t.Run("override MaxConcurrency via env", func(t *testing.T) {
		_ = os.Setenv("TRUVAG3_EXECUTION_MAX_CONCURRENCY", "10")

		config := DefaultConfig()

		if config.ExecutionOptions.MaxConcurrency != 10 {
			t.Errorf("Expected MaxConcurrency 10, got %d", config.ExecutionOptions.MaxConcurrency)
		}
	})

	t.Run("invalid MaxConcurrency keeps default", func(t *testing.T) {
		_ = os.Setenv("TRUVAG3_EXECUTION_MAX_CONCURRENCY", "abc")

		config := DefaultConfig()

		if config.ExecutionOptions.MaxConcurrency != 25 {
			t.Errorf("Expected MaxConcurrency 25 (default), got %d", config.ExecutionOptions.MaxConcurrency)
		}
	})

	t.Run("negative MaxConcurrency keeps default", func(t *testing.T) {
		_ = os.Setenv("TRUVAG3_EXECUTION_MAX_CONCURRENCY", "-1")

		config := DefaultConfig()

		if config.ExecutionOptions.MaxConcurrency != 25 {
			t.Errorf("Expected MaxConcurrency 25 (default), got %d", config.ExecutionOptions.MaxConcurrency)
		}
	})

	t.Run("zero MaxConcurrency keeps default", func(t *testing.T) {
		_ = os.Setenv("TRUVAG3_EXECUTION_MAX_CONCURRENCY", "0")

		config := DefaultConfig()

		if config.ExecutionOptions.MaxConcurrency != 25 {
			t.Errorf("Expected MaxConcurrency 25 (default), got %d", config.ExecutionOptions.MaxConcurrency)
		}
	})
}

// TestWithMaxConcurrency tests the functional option
func TestWithMaxConcurrency(t *testing.T) {
	t.Run("set valid max concurrency", func(t *testing.T) {
		config := DefaultConfig()
		opt := WithMaxConcurrency(10)
		opt(config)

		if config.ExecutionOptions.MaxConcurrency != 10 {
			t.Errorf("Expected MaxConcurrency 10, got %d", config.ExecutionOptions.MaxConcurrency)
		}
	})

	t.Run("zero max concurrency ignored", func(t *testing.T) {
		config := DefaultConfig()
		opt := WithMaxConcurrency(0)
		opt(config)

		if config.ExecutionOptions.MaxConcurrency != 25 {
			t.Errorf("Expected MaxConcurrency to remain 25, got %d", config.ExecutionOptions.MaxConcurrency)
		}
	})

	t.Run("negative max concurrency ignored", func(t *testing.T) {
		config := DefaultConfig()
		opt := WithMaxConcurrency(-1)
		opt(config)

		if config.ExecutionOptions.MaxConcurrency != 25 {
			t.Errorf("Expected MaxConcurrency to remain 25, got %d", config.ExecutionOptions.MaxConcurrency)
		}
	})
}

// TestWithMaxConcurrency_OptionOverridesEnv tests that functional options take precedence over env vars
func TestWithMaxConcurrency_OptionOverridesEnv(t *testing.T) {
	oldVal := os.Getenv("TRUVAG3_EXECUTION_MAX_CONCURRENCY")
	defer func() {
		if oldVal != "" {
			_ = os.Setenv("TRUVAG3_EXECUTION_MAX_CONCURRENCY", oldVal)
		} else {
			_ = os.Unsetenv("TRUVAG3_EXECUTION_MAX_CONCURRENCY")
		}
	}()

	_ = os.Setenv("TRUVAG3_EXECUTION_MAX_CONCURRENCY", "10")

	deps := OrchestratorDependencies{
		Discovery: NewMockDiscovery(),
		AIClient:  NewMockAIClient(),
	}

	orchestrator, err := CreateOrchestratorWithOptions(deps, WithMaxConcurrency(20))
	if err != nil {
		t.Fatalf("Failed to create orchestrator: %v", err)
	}

	if orchestrator.config.ExecutionOptions.MaxConcurrency != 20 {
		t.Errorf("Expected MaxConcurrency 20 (option override), got %d", orchestrator.config.ExecutionOptions.MaxConcurrency)
	}
}

// TestDefaultConfig_StepTimeout_Default tests default value for StepTimeout
func TestDefaultConfig_StepTimeout_Default(t *testing.T) {
	_ = os.Unsetenv("TRUVAG3_EXECUTION_STEP_TIMEOUT")

	config := DefaultConfig()

	if config.ExecutionOptions.StepTimeout != 120*time.Second {
		t.Errorf("Expected StepTimeout default 120s, got %v", config.ExecutionOptions.StepTimeout)
	}
}

// TestDefaultConfig_StepTimeout_EnvironmentConfiguration tests env var loading
func TestDefaultConfig_StepTimeout_EnvironmentConfiguration(t *testing.T) {
	oldVal := os.Getenv("TRUVAG3_EXECUTION_STEP_TIMEOUT")
	defer func() {
		if oldVal != "" {
			_ = os.Setenv("TRUVAG3_EXECUTION_STEP_TIMEOUT", oldVal)
		} else {
			_ = os.Unsetenv("TRUVAG3_EXECUTION_STEP_TIMEOUT")
		}
	}()

	t.Run("override StepTimeout via env", func(t *testing.T) {
		_ = os.Setenv("TRUVAG3_EXECUTION_STEP_TIMEOUT", "60s")

		config := DefaultConfig()

		if config.ExecutionOptions.StepTimeout != 60*time.Second {
			t.Errorf("Expected StepTimeout 60s, got %v", config.ExecutionOptions.StepTimeout)
		}
	})

	t.Run("invalid StepTimeout keeps default", func(t *testing.T) {
		_ = os.Setenv("TRUVAG3_EXECUTION_STEP_TIMEOUT", "abc")

		config := DefaultConfig()

		if config.ExecutionOptions.StepTimeout != 120*time.Second {
			t.Errorf("Expected StepTimeout 120s (default), got %v", config.ExecutionOptions.StepTimeout)
		}
	})

	t.Run("zero StepTimeout keeps default", func(t *testing.T) {
		_ = os.Setenv("TRUVAG3_EXECUTION_STEP_TIMEOUT", "0s")

		config := DefaultConfig()

		if config.ExecutionOptions.StepTimeout != 120*time.Second {
			t.Errorf("Expected StepTimeout 120s (default), got %v", config.ExecutionOptions.StepTimeout)
		}
	})
}

// TestWithStepTimeout tests the functional option
func TestWithStepTimeout(t *testing.T) {
	t.Run("set valid step timeout", func(t *testing.T) {
		config := DefaultConfig()
		opt := WithStepTimeout(60 * time.Second)
		opt(config)

		if config.ExecutionOptions.StepTimeout != 60*time.Second {
			t.Errorf("Expected StepTimeout 60s, got %v", config.ExecutionOptions.StepTimeout)
		}
	})

	t.Run("zero step timeout ignored", func(t *testing.T) {
		config := DefaultConfig()
		opt := WithStepTimeout(0)
		opt(config)

		if config.ExecutionOptions.StepTimeout != 120*time.Second {
			t.Errorf("Expected StepTimeout to remain 120s, got %v", config.ExecutionOptions.StepTimeout)
		}
	})

	t.Run("negative step timeout ignored", func(t *testing.T) {
		config := DefaultConfig()
		opt := WithStepTimeout(-1 * time.Second)
		opt(config)

		if config.ExecutionOptions.StepTimeout != 120*time.Second {
			t.Errorf("Expected StepTimeout to remain 120s, got %v", config.ExecutionOptions.StepTimeout)
		}
	})
}

// TestWithStepTimeout_OptionOverridesEnv tests that functional options take precedence over env vars
func TestWithStepTimeout_OptionOverridesEnv(t *testing.T) {
	oldVal := os.Getenv("TRUVAG3_EXECUTION_STEP_TIMEOUT")
	defer func() {
		if oldVal != "" {
			_ = os.Setenv("TRUVAG3_EXECUTION_STEP_TIMEOUT", oldVal)
		} else {
			_ = os.Unsetenv("TRUVAG3_EXECUTION_STEP_TIMEOUT")
		}
	}()

	_ = os.Setenv("TRUVAG3_EXECUTION_STEP_TIMEOUT", "60s")

	deps := OrchestratorDependencies{
		Discovery: NewMockDiscovery(),
		AIClient:  NewMockAIClient(),
	}

	orchestrator, err := CreateOrchestratorWithOptions(deps, WithStepTimeout(90*time.Second))
	if err != nil {
		t.Fatalf("Failed to create orchestrator: %v", err)
	}

	if orchestrator.config.ExecutionOptions.StepTimeout != 90*time.Second {
		t.Errorf("Expected StepTimeout 90s (option override), got %v", orchestrator.config.ExecutionOptions.StepTimeout)
	}
}

// TestDefaultConfig_TotalTimeout_Default tests default value for TotalTimeout
func TestDefaultConfig_TotalTimeout_Default(t *testing.T) {
	_ = os.Unsetenv("TRUVAG3_ORCHESTRATION_TIMEOUT")

	config := DefaultConfig()

	if config.ExecutionOptions.TotalTimeout != 600*time.Second {
		t.Errorf("Expected TotalTimeout default 600s, got %v", config.ExecutionOptions.TotalTimeout)
	}
}

// TestDefaultConfig_TotalTimeout_EnvironmentConfiguration tests env var loading
func TestDefaultConfig_TotalTimeout_EnvironmentConfiguration(t *testing.T) {
	oldVal := os.Getenv("TRUVAG3_ORCHESTRATION_TIMEOUT")
	defer func() {
		if oldVal != "" {
			_ = os.Setenv("TRUVAG3_ORCHESTRATION_TIMEOUT", oldVal)
		} else {
			_ = os.Unsetenv("TRUVAG3_ORCHESTRATION_TIMEOUT")
		}
	}()

	t.Run("override TotalTimeout via env", func(t *testing.T) {
		_ = os.Setenv("TRUVAG3_ORCHESTRATION_TIMEOUT", "5m")

		config := DefaultConfig()

		if config.ExecutionOptions.TotalTimeout != 5*time.Minute {
			t.Errorf("Expected TotalTimeout 5m, got %v", config.ExecutionOptions.TotalTimeout)
		}
	})

	t.Run("invalid TotalTimeout keeps default", func(t *testing.T) {
		_ = os.Setenv("TRUVAG3_ORCHESTRATION_TIMEOUT", "abc")

		config := DefaultConfig()

		if config.ExecutionOptions.TotalTimeout != 600*time.Second {
			t.Errorf("Expected TotalTimeout 600s (default), got %v", config.ExecutionOptions.TotalTimeout)
		}
	})

	t.Run("zero TotalTimeout keeps default", func(t *testing.T) {
		_ = os.Setenv("TRUVAG3_ORCHESTRATION_TIMEOUT", "0s")

		config := DefaultConfig()

		if config.ExecutionOptions.TotalTimeout != 600*time.Second {
			t.Errorf("Expected TotalTimeout 600s (default), got %v", config.ExecutionOptions.TotalTimeout)
		}
	})
}

// TestWithTotalTimeout tests the functional option
func TestWithTotalTimeout(t *testing.T) {
	t.Run("set valid total timeout", func(t *testing.T) {
		config := DefaultConfig()
		opt := WithTotalTimeout(5 * time.Minute)
		opt(config)

		if config.ExecutionOptions.TotalTimeout != 5*time.Minute {
			t.Errorf("Expected TotalTimeout 5m, got %v", config.ExecutionOptions.TotalTimeout)
		}
	})

	t.Run("zero total timeout ignored", func(t *testing.T) {
		config := DefaultConfig()
		opt := WithTotalTimeout(0)
		opt(config)

		if config.ExecutionOptions.TotalTimeout != 600*time.Second {
			t.Errorf("Expected TotalTimeout to remain 600s, got %v", config.ExecutionOptions.TotalTimeout)
		}
	})

	t.Run("negative total timeout ignored", func(t *testing.T) {
		config := DefaultConfig()
		opt := WithTotalTimeout(-1 * time.Second)
		opt(config)

		if config.ExecutionOptions.TotalTimeout != 600*time.Second {
			t.Errorf("Expected TotalTimeout to remain 600s, got %v", config.ExecutionOptions.TotalTimeout)
		}
	})
}

// TestWithTotalTimeout_OptionOverridesEnv tests that functional options take precedence over env vars
func TestWithTotalTimeout_OptionOverridesEnv(t *testing.T) {
	oldVal := os.Getenv("TRUVAG3_ORCHESTRATION_TIMEOUT")
	defer func() {
		if oldVal != "" {
			_ = os.Setenv("TRUVAG3_ORCHESTRATION_TIMEOUT", oldVal)
		} else {
			_ = os.Unsetenv("TRUVAG3_ORCHESTRATION_TIMEOUT")
		}
	}()

	_ = os.Setenv("TRUVAG3_ORCHESTRATION_TIMEOUT", "5m")

	deps := OrchestratorDependencies{
		Discovery: NewMockDiscovery(),
		AIClient:  NewMockAIClient(),
	}

	orchestrator, err := CreateOrchestratorWithOptions(deps, WithTotalTimeout(10*time.Minute))
	if err != nil {
		t.Fatalf("Failed to create orchestrator: %v", err)
	}

	if orchestrator.config.ExecutionOptions.TotalTimeout != 10*time.Minute {
		t.Errorf("Expected TotalTimeout 10m (option override), got %v", orchestrator.config.ExecutionOptions.TotalTimeout)
	}
}

// TestDefaultConfig_SynthesisTemperature_EnvironmentConfiguration tests env var loading for synthesis temperature
func TestDefaultConfig_SynthesisTemperature_EnvironmentConfiguration(t *testing.T) {
	oldTemp := os.Getenv("TRUVAG3_SYNTHESIS_TEMPERATURE")
	defer func() {
		if oldTemp != "" {
			_ = os.Setenv("TRUVAG3_SYNTHESIS_TEMPERATURE", oldTemp)
		} else {
			_ = os.Unsetenv("TRUVAG3_SYNTHESIS_TEMPERATURE")
		}
	}()

	t.Run("override SynthesisTemperature via env", func(t *testing.T) {
		_ = os.Setenv("TRUVAG3_SYNTHESIS_TEMPERATURE", "0.7")

		config := DefaultConfig()

		if config.SynthesisTemperature != 0.7 {
			t.Errorf("Expected SynthesisTemperature 0.7, got %f", config.SynthesisTemperature)
		}
	})

	t.Run("invalid SynthesisTemperature keeps default", func(t *testing.T) {
		_ = os.Setenv("TRUVAG3_SYNTHESIS_TEMPERATURE", "invalid")

		config := DefaultConfig()

		if config.SynthesisTemperature != 0.5 {
			t.Errorf("Expected SynthesisTemperature 0.5 (default), got %f", config.SynthesisTemperature)
		}
	})

	t.Run("negative SynthesisTemperature keeps default", func(t *testing.T) {
		_ = os.Setenv("TRUVAG3_SYNTHESIS_TEMPERATURE", "-0.1")

		config := DefaultConfig()

		if config.SynthesisTemperature != 0.5 {
			t.Errorf("Expected SynthesisTemperature 0.5 (default), got %f", config.SynthesisTemperature)
		}
	})

	t.Run("out-of-range SynthesisTemperature keeps default", func(t *testing.T) {
		_ = os.Setenv("TRUVAG3_SYNTHESIS_TEMPERATURE", "2.5")

		config := DefaultConfig()

		if config.SynthesisTemperature != 0.5 {
			t.Errorf("Expected SynthesisTemperature 0.5 (default), got %f", config.SynthesisTemperature)
		}
	})

	t.Run("zero SynthesisTemperature accepted", func(t *testing.T) {
		_ = os.Setenv("TRUVAG3_SYNTHESIS_TEMPERATURE", "0")

		config := DefaultConfig()

		if config.SynthesisTemperature != 0.0 {
			t.Errorf("Expected SynthesisTemperature 0.0, got %f", config.SynthesisTemperature)
		}
	})
}

// TestWithSynthesisTemperature tests the functional option
func TestWithSynthesisTemperature(t *testing.T) {
	t.Run("set valid temperature", func(t *testing.T) {
		config := DefaultConfig()
		opt := WithSynthesisTemperature(0.7)
		opt(config)

		if config.SynthesisTemperature != 0.7 {
			t.Errorf("Expected SynthesisTemperature 0.7, got %f", config.SynthesisTemperature)
		}
	})

	t.Run("set zero temperature accepted", func(t *testing.T) {
		config := DefaultConfig()
		opt := WithSynthesisTemperature(0.0)
		opt(config)

		if config.SynthesisTemperature != 0.0 {
			t.Errorf("Expected SynthesisTemperature 0.0, got %f", config.SynthesisTemperature)
		}
	})

	t.Run("set max boundary temperature", func(t *testing.T) {
		config := DefaultConfig()
		opt := WithSynthesisTemperature(2.0)
		opt(config)

		if config.SynthesisTemperature != 2.0 {
			t.Errorf("Expected SynthesisTemperature 2.0, got %f", config.SynthesisTemperature)
		}
	})

	t.Run("out-of-range temperature ignored", func(t *testing.T) {
		config := DefaultConfig()
		opt := WithSynthesisTemperature(2.5)
		opt(config)

		if config.SynthesisTemperature != 0.5 {
			t.Errorf("Expected SynthesisTemperature to remain 0.5, got %f", config.SynthesisTemperature)
		}
	})

	t.Run("negative temperature ignored", func(t *testing.T) {
		config := DefaultConfig()
		opt := WithSynthesisTemperature(-0.1)
		opt(config)

		if config.SynthesisTemperature != 0.5 {
			t.Errorf("Expected SynthesisTemperature to remain 0.5, got %f", config.SynthesisTemperature)
		}
	})
}

// TestWithMaxTokens_OptionOverridesEnv tests that functional options take precedence over env vars
func TestWithMaxTokens_OptionOverridesEnv(t *testing.T) {
	oldPlan := os.Getenv("TRUVAG3_PLAN_MAX_TOKENS")
	defer func() {
		if oldPlan != "" {
			_ = os.Setenv("TRUVAG3_PLAN_MAX_TOKENS", oldPlan)
		} else {
			_ = os.Unsetenv("TRUVAG3_PLAN_MAX_TOKENS")
		}
	}()

	// Set env var to 5000
	_ = os.Setenv("TRUVAG3_PLAN_MAX_TOKENS", "5000")

	deps := OrchestratorDependencies{
		Discovery: NewMockDiscovery(),
		AIClient:  NewMockAIClient(),
	}

	// Functional option should override env var
	orchestrator, err := CreateOrchestratorWithOptions(deps, WithPlanMaxTokens(8000))
	if err != nil {
		t.Fatalf("Failed to create orchestrator: %v", err)
	}

	if orchestrator.config.PlanMaxTokens != 8000 {
		t.Errorf("Expected PlanMaxTokens 8000 (option override), got %d", orchestrator.config.PlanMaxTokens)
	}
}

// TestWithResultDistill tests the functional option for LLM distillation
func TestWithResultDistill(t *testing.T) {
	t.Run("set valid config", func(t *testing.T) {
		config := DefaultConfig()
		opt := WithResultDistill(true, 16384)
		opt(config)

		if !config.ResultDistill.Enabled {
			t.Error("Expected ResultDistill.Enabled=true")
		}
		if config.ResultDistill.DistillThreshold != 16384 {
			t.Errorf("Expected DistillThreshold 16384, got %d", config.ResultDistill.DistillThreshold)
		}
	})

	t.Run("zero threshold keeps default", func(t *testing.T) {
		config := DefaultConfig()
		opt := WithResultDistill(true, 0)
		opt(config)

		if !config.ResultDistill.Enabled {
			t.Error("Expected ResultDistill.Enabled=true")
		}
		// Zero threshold leaves the DefaultConfig value untouched (16384 since Phase 1).
		if config.ResultDistill.DistillThreshold != 16384 {
			t.Errorf("Expected DistillThreshold to remain 16384, got %d", config.ResultDistill.DistillThreshold)
		}
	})

	t.Run("negative threshold keeps default", func(t *testing.T) {
		config := DefaultConfig()
		opt := WithResultDistill(true, -1)
		opt(config)

		// Negative threshold leaves the DefaultConfig value untouched (16384 since Phase 1).
		if config.ResultDistill.DistillThreshold != 16384 {
			t.Errorf("Expected DistillThreshold to remain 16384, got %d", config.ResultDistill.DistillThreshold)
		}
	})

	t.Run("disable distillation", func(t *testing.T) {
		config := DefaultConfig()
		config.ResultDistill.Enabled = true // pre-enable
		opt := WithResultDistill(false, 0)
		opt(config)

		if config.ResultDistill.Enabled {
			t.Error("Expected ResultDistill.Enabled=false")
		}
	})
}

// TestWithResultDistillModel tests the model override option.
// Uses portable alias "fast" (not a concrete model name) because concrete
// names break ChainClient failover — see MICRO_RESOLUTION_MODEL_OVERRIDE.md.
func TestWithResultDistillModel(t *testing.T) {
	t.Run("set model with portable alias", func(t *testing.T) {
		config := DefaultConfig()
		opt := WithResultDistillModel("fast")
		opt(config)

		if config.ResultDistill.Model != "fast" {
			t.Errorf("Expected Model 'fast', got '%s'", config.ResultDistill.Model)
		}
	})

	t.Run("empty model clears override", func(t *testing.T) {
		config := DefaultConfig()
		config.ResultDistill.Model = "some-model"
		opt := WithResultDistillModel("")
		opt(config)

		if config.ResultDistill.Model != "" {
			t.Errorf("Expected empty Model, got '%s'", config.ResultDistill.Model)
		}
	})
}

// TestWithPlanModel tests the plan generation model override option.
func TestWithPlanModel(t *testing.T) {
	t.Run("set model with portable alias", func(t *testing.T) {
		config := DefaultConfig()
		opt := WithPlanModel("smart")
		opt(config)

		if config.PlanModel != "smart" {
			t.Errorf("Expected PlanModel 'smart', got '%s'", config.PlanModel)
		}
	})

	t.Run("empty model clears override", func(t *testing.T) {
		config := DefaultConfig()
		config.PlanModel = "some-model"
		opt := WithPlanModel("")
		opt(config)

		if config.PlanModel != "" {
			t.Errorf("Expected empty PlanModel, got '%s'", config.PlanModel)
		}
	})
}

// TestWithSynthesisModel tests the synthesis model override option.
func TestWithSynthesisModel(t *testing.T) {
	t.Run("set model with portable alias", func(t *testing.T) {
		config := DefaultConfig()
		opt := WithSynthesisModel("default")
		opt(config)

		if config.SynthesisModel != "default" {
			t.Errorf("Expected SynthesisModel 'default', got '%s'", config.SynthesisModel)
		}
	})

	t.Run("empty model clears override", func(t *testing.T) {
		config := DefaultConfig()
		config.SynthesisModel = "some-model"
		opt := WithSynthesisModel("")
		opt(config)

		if config.SynthesisModel != "" {
			t.Errorf("Expected empty SynthesisModel, got '%s'", config.SynthesisModel)
		}
	})
}

// TestWithMicroResolutionModel tests the micro-resolution model override option.
func TestWithMicroResolutionModel(t *testing.T) {
	t.Run("set model with portable alias", func(t *testing.T) {
		config := DefaultConfig()
		opt := WithMicroResolutionModel("fast")
		opt(config)

		if config.MicroResolutionModel != "fast" {
			t.Errorf("Expected MicroResolutionModel 'fast', got '%s'", config.MicroResolutionModel)
		}
	})

	t.Run("empty model clears override", func(t *testing.T) {
		config := DefaultConfig()
		config.MicroResolutionModel = "some-model"
		opt := WithMicroResolutionModel("")
		opt(config)

		if config.MicroResolutionModel != "" {
			t.Errorf("Expected empty MicroResolutionModel, got '%s'", config.MicroResolutionModel)
		}
	})
}

// =============================================================================
// Config-to-Synthesizer Propagation Tests
// =============================================================================

// TestNewAIOrchestrator_PropagatesSynthesisConfig verifies that NewAIOrchestrator
// propagates SynthesisTemperature and SynthesisMaxTokens from config to the synthesizer.
// This is the critical propagation chain: OrchestratorConfig → synthesizer fields.
func TestNewAIOrchestrator_PropagatesSynthesisConfig(t *testing.T) {
	t.Run("default config propagates defaults", func(t *testing.T) {
		config := DefaultConfig()
		orchestrator := NewAIOrchestrator(config, NewMockDiscovery(), NewMockAIClient())

		if orchestrator.synthesizer.synthesisTemperature != 0.5 {
			t.Errorf("Expected synthesizer temperature=0.5, got %f",
				orchestrator.synthesizer.synthesisTemperature)
		}
		if orchestrator.synthesizer.synthesisMaxTokens != 5000 {
			t.Errorf("Expected synthesizer maxTokens=5000, got %d",
				orchestrator.synthesizer.synthesisMaxTokens)
		}
	})

	t.Run("custom config propagates to synthesizer", func(t *testing.T) {
		config := DefaultConfig()
		config.SynthesisTemperature = 0.9
		config.SynthesisMaxTokens = 3000
		orchestrator := NewAIOrchestrator(config, NewMockDiscovery(), NewMockAIClient())

		if orchestrator.synthesizer.synthesisTemperature != 0.9 {
			t.Errorf("Expected synthesizer temperature=0.9, got %f",
				orchestrator.synthesizer.synthesisTemperature)
		}
		if orchestrator.synthesizer.synthesisMaxTokens != 3000 {
			t.Errorf("Expected synthesizer maxTokens=3000, got %d",
				orchestrator.synthesizer.synthesisMaxTokens)
		}
	})

	t.Run("zero temperature propagates correctly", func(t *testing.T) {
		config := DefaultConfig()
		config.SynthesisTemperature = 0.0
		orchestrator := NewAIOrchestrator(config, NewMockDiscovery(), NewMockAIClient())

		if orchestrator.synthesizer.synthesisTemperature != 0.0 {
			t.Errorf("Expected synthesizer temperature=0.0, got %f",
				orchestrator.synthesizer.synthesisTemperature)
		}
	})

	t.Run("streaming config matches non-streaming synthesizer", func(t *testing.T) {
		config := DefaultConfig()
		config.SynthesisTemperature = 0.7
		config.SynthesisMaxTokens = 8000
		orchestrator := NewAIOrchestrator(config, NewMockDiscovery(), NewMockAIClient())

		// Streaming path uses o.config directly, non-streaming uses o.synthesizer fields.
		// Both must agree.
		if orchestrator.config.SynthesisTemperature != orchestrator.synthesizer.synthesisTemperature {
			t.Errorf("Config temperature (%f) != synthesizer temperature (%f)",
				orchestrator.config.SynthesisTemperature,
				orchestrator.synthesizer.synthesisTemperature)
		}
		if orchestrator.config.SynthesisMaxTokens != orchestrator.synthesizer.synthesisMaxTokens {
			t.Errorf("Config maxTokens (%d) != synthesizer maxTokens (%d)",
				orchestrator.config.SynthesisMaxTokens,
				orchestrator.synthesizer.synthesisMaxTokens)
		}
	})
}

// TestCreateOrchestrator_PropagatesSynthesisConfig verifies the full factory path
// (CreateOrchestrator → NewAIOrchestrator) propagates synthesis parameters.
func TestCreateOrchestrator_PropagatesSynthesisConfig(t *testing.T) {
	deps := OrchestratorDependencies{
		Discovery: NewMockDiscovery(),
		AIClient:  NewMockAIClient(),
	}

	config := DefaultConfig()
	config.SynthesisTemperature = 0.8
	config.SynthesisMaxTokens = 6000

	orchestrator, err := CreateOrchestrator(config, deps)
	if err != nil {
		t.Fatalf("Failed to create orchestrator: %v", err)
	}

	if orchestrator.synthesizer.synthesisTemperature != 0.8 {
		t.Errorf("Expected synthesizer temperature=0.8, got %f",
			orchestrator.synthesizer.synthesisTemperature)
	}
	if orchestrator.synthesizer.synthesisMaxTokens != 6000 {
		t.Errorf("Expected synthesizer maxTokens=6000, got %d",
			orchestrator.synthesizer.synthesisMaxTokens)
	}
}

// TestWithSynthesisTemperature_OptionOverridesEnv tests that functional options
// take precedence over environment variables (same pattern as TestWithMaxTokens_OptionOverridesEnv).
func TestWithSynthesisTemperature_OptionOverridesEnv(t *testing.T) {
	oldTemp := os.Getenv("TRUVAG3_SYNTHESIS_TEMPERATURE")
	defer func() {
		if oldTemp != "" {
			_ = os.Setenv("TRUVAG3_SYNTHESIS_TEMPERATURE", oldTemp)
		} else {
			_ = os.Unsetenv("TRUVAG3_SYNTHESIS_TEMPERATURE")
		}
	}()

	// Set env var to 0.9
	_ = os.Setenv("TRUVAG3_SYNTHESIS_TEMPERATURE", "0.9")

	deps := OrchestratorDependencies{
		Discovery: NewMockDiscovery(),
		AIClient:  NewMockAIClient(),
	}

	// Functional option should override env var
	orchestrator, err := CreateOrchestratorWithOptions(deps, WithSynthesisTemperature(0.3))
	if err != nil {
		t.Fatalf("Failed to create orchestrator: %v", err)
	}

	if orchestrator.config.SynthesisTemperature != 0.3 {
		t.Errorf("Expected SynthesisTemperature 0.3 (option override), got %f",
			orchestrator.config.SynthesisTemperature)
	}
	// Also verify it propagated to synthesizer
	if orchestrator.synthesizer.synthesisTemperature != 0.3 {
		t.Errorf("Expected synthesizer temperature 0.3 (propagated), got %f",
			orchestrator.synthesizer.synthesisTemperature)
	}
}

// =============================================================================
// Model Override Environment Variable Tests
// =============================================================================

// TestDefaultConfig_ModelOverrides_EnvironmentConfiguration tests env var loading
// for model override fields (PlanModel, SynthesisModel, MicroResolutionModel,
// ResultDistill.Model).
func TestDefaultConfig_ModelOverrides_EnvironmentConfiguration(t *testing.T) {
	// Save and restore all model env vars
	envVars := []string{
		"TRUVAG3_PLAN_MODEL",
		"TRUVAG3_SYNTHESIS_MODEL",
		"TRUVAG3_MICRO_RESOLUTION_MODEL",
		"TRUVAG3_RESULT_DISTILL_MODEL",
	}
	saved := make(map[string]string)
	for _, v := range envVars {
		saved[v] = os.Getenv(v)
	}
	defer func() {
		for _, v := range envVars {
			if saved[v] != "" {
				_ = os.Setenv(v, saved[v])
			} else {
				_ = os.Unsetenv(v)
			}
		}
	}()

	clearModelEnvVars := func() {
		for _, v := range envVars {
			_ = os.Unsetenv(v)
		}
	}

	t.Run("TRUVAG3_PLAN_MODEL sets PlanModel", func(t *testing.T) {
		clearModelEnvVars()
		_ = os.Setenv("TRUVAG3_PLAN_MODEL", "smart")
		config := DefaultConfig()
		if config.PlanModel != "smart" {
			t.Errorf("Expected PlanModel 'smart', got %q", config.PlanModel)
		}
	})

	t.Run("TRUVAG3_SYNTHESIS_MODEL sets SynthesisModel", func(t *testing.T) {
		clearModelEnvVars()
		_ = os.Setenv("TRUVAG3_SYNTHESIS_MODEL", "default")
		config := DefaultConfig()
		if config.SynthesisModel != "default" {
			t.Errorf("Expected SynthesisModel 'default', got %q", config.SynthesisModel)
		}
	})

	t.Run("TRUVAG3_MICRO_RESOLUTION_MODEL sets MicroResolutionModel", func(t *testing.T) {
		clearModelEnvVars()
		_ = os.Setenv("TRUVAG3_MICRO_RESOLUTION_MODEL", "fast")
		config := DefaultConfig()
		if config.MicroResolutionModel != "fast" {
			t.Errorf("Expected MicroResolutionModel 'fast', got %q", config.MicroResolutionModel)
		}
	})

	t.Run("TRUVAG3_RESULT_DISTILL_MODEL sets ResultDistill.Model", func(t *testing.T) {
		clearModelEnvVars()
		_ = os.Setenv("TRUVAG3_RESULT_DISTILL_MODEL", "fast")
		config := DefaultConfig()
		if config.ResultDistill.Model != "fast" {
			t.Errorf("Expected ResultDistill.Model 'fast', got %q", config.ResultDistill.Model)
		}
	})

	t.Run("unset env vars leave models empty", func(t *testing.T) {
		clearModelEnvVars()
		config := DefaultConfig()
		if config.PlanModel != "" {
			t.Errorf("Expected empty PlanModel, got %q", config.PlanModel)
		}
		if config.SynthesisModel != "" {
			t.Errorf("Expected empty SynthesisModel, got %q", config.SynthesisModel)
		}
		if config.MicroResolutionModel != "" {
			t.Errorf("Expected empty MicroResolutionModel, got %q", config.MicroResolutionModel)
		}
	})

	t.Run("all four env vars set simultaneously", func(t *testing.T) {
		clearModelEnvVars()
		_ = os.Setenv("TRUVAG3_PLAN_MODEL", "smart")
		_ = os.Setenv("TRUVAG3_SYNTHESIS_MODEL", "default")
		_ = os.Setenv("TRUVAG3_MICRO_RESOLUTION_MODEL", "fast")
		_ = os.Setenv("TRUVAG3_RESULT_DISTILL_MODEL", "fast")
		config := DefaultConfig()
		if config.PlanModel != "smart" {
			t.Errorf("Expected PlanModel 'smart', got %q", config.PlanModel)
		}
		if config.SynthesisModel != "default" {
			t.Errorf("Expected SynthesisModel 'default', got %q", config.SynthesisModel)
		}
		if config.MicroResolutionModel != "fast" {
			t.Errorf("Expected MicroResolutionModel 'fast', got %q", config.MicroResolutionModel)
		}
		if config.ResultDistill.Model != "fast" {
			t.Errorf("Expected ResultDistill.Model 'fast', got %q", config.ResultDistill.Model)
		}
	})
}

// TestWithModelOverrides_OptionOverridesEnv tests that With*Model() functional
// options take precedence over TRUVAG3_*_MODEL environment variables.
func TestWithModelOverrides_OptionOverridesEnv(t *testing.T) {
	envVars := []string{
		"TRUVAG3_PLAN_MODEL",
		"TRUVAG3_SYNTHESIS_MODEL",
		"TRUVAG3_MICRO_RESOLUTION_MODEL",
	}
	saved := make(map[string]string)
	for _, v := range envVars {
		saved[v] = os.Getenv(v)
	}
	defer func() {
		for _, v := range envVars {
			if saved[v] != "" {
				_ = os.Setenv(v, saved[v])
			} else {
				_ = os.Unsetenv(v)
			}
		}
	}()

	_ = os.Setenv("TRUVAG3_PLAN_MODEL", "default")
	_ = os.Setenv("TRUVAG3_SYNTHESIS_MODEL", "default")
	_ = os.Setenv("TRUVAG3_MICRO_RESOLUTION_MODEL", "default")

	deps := OrchestratorDependencies{
		Discovery: NewMockDiscovery(),
		AIClient:  NewMockAIClient(),
	}

	orchestrator, err := CreateOrchestratorWithOptions(deps,
		WithPlanModel("smart"),
		WithSynthesisModel("fast"),
		WithMicroResolutionModel("fast"),
	)
	if err != nil {
		t.Fatalf("Failed to create orchestrator: %v", err)
	}

	if orchestrator.config.PlanModel != "smart" {
		t.Errorf("Expected PlanModel 'smart' (option override), got %q", orchestrator.config.PlanModel)
	}
	if orchestrator.config.SynthesisModel != "fast" {
		t.Errorf("Expected SynthesisModel 'fast' (option override), got %q", orchestrator.config.SynthesisModel)
	}
	if orchestrator.config.MicroResolutionModel != "fast" {
		t.Errorf("Expected MicroResolutionModel 'fast' (option override), got %q", orchestrator.config.MicroResolutionModel)
	}
}

// =============================================================================
// Model Override Propagation Tests
// =============================================================================

// TestNewAIOrchestrator_PropagatesModelOverrides verifies that NewAIOrchestrator
// propagates model overrides from config to internal components.
func TestNewAIOrchestrator_PropagatesModelOverrides(t *testing.T) {
	t.Run("SynthesisModel propagates to synthesizer", func(t *testing.T) {
		config := DefaultConfig()
		config.SynthesisModel = "fast"
		orchestrator := NewAIOrchestrator(config, NewMockDiscovery(), NewMockAIClient())

		if orchestrator.synthesizer.model != "fast" {
			t.Errorf("Expected synthesizer.model='fast', got %q", orchestrator.synthesizer.model)
		}
	})

	t.Run("empty SynthesisModel leaves synthesizer model empty", func(t *testing.T) {
		config := DefaultConfig()
		orchestrator := NewAIOrchestrator(config, NewMockDiscovery(), NewMockAIClient())

		if orchestrator.synthesizer.model != "" {
			t.Errorf("Expected synthesizer.model='', got %q", orchestrator.synthesizer.model)
		}
	})

	t.Run("PlanModel stored in config for planAIOptions access", func(t *testing.T) {
		config := DefaultConfig()
		config.PlanModel = "smart"
		orchestrator := NewAIOrchestrator(config, NewMockDiscovery(), NewMockAIClient())

		if orchestrator.config.PlanModel != "smart" {
			t.Errorf("Expected config.PlanModel='smart', got %q", orchestrator.config.PlanModel)
		}
	})
}

// TestWithSynthesisMaxTokens_OptionOverridesEnv tests that functional options
// take precedence over environment variables for synthesis max tokens.
func TestWithSynthesisMaxTokens_OptionOverridesEnv(t *testing.T) {
	oldTokens := os.Getenv("TRUVAG3_SYNTHESIS_MAX_TOKENS")
	defer func() {
		if oldTokens != "" {
			_ = os.Setenv("TRUVAG3_SYNTHESIS_MAX_TOKENS", oldTokens)
		} else {
			_ = os.Unsetenv("TRUVAG3_SYNTHESIS_MAX_TOKENS")
		}
	}()

	// Set env var to 9000
	_ = os.Setenv("TRUVAG3_SYNTHESIS_MAX_TOKENS", "9000")

	deps := OrchestratorDependencies{
		Discovery: NewMockDiscovery(),
		AIClient:  NewMockAIClient(),
	}

	// Functional option should override env var
	orchestrator, err := CreateOrchestratorWithOptions(deps, WithSynthesisMaxTokens(2000))
	if err != nil {
		t.Fatalf("Failed to create orchestrator: %v", err)
	}

	if orchestrator.config.SynthesisMaxTokens != 2000 {
		t.Errorf("Expected SynthesisMaxTokens 2000 (option override), got %d",
			orchestrator.config.SynthesisMaxTokens)
	}
	if orchestrator.synthesizer.synthesisMaxTokens != 2000 {
		t.Errorf("Expected synthesizer maxTokens 2000 (propagated), got %d",
			orchestrator.synthesizer.synthesisMaxTokens)
	}
}

// --- RC5: MicroResolutionMaxTokens tests ---

func TestDefaultConfig_MicroResolutionMaxTokens_Default(t *testing.T) {
	_ = os.Unsetenv("TRUVAG3_MICRO_RESOLUTION_MAX_TOKENS")

	config := DefaultConfig()

	if config.MicroResolutionMaxTokens != 2000 {
		t.Errorf("Expected MicroResolutionMaxTokens default 2000, got %d", config.MicroResolutionMaxTokens)
	}
}

func TestDefaultConfig_MicroResolutionMaxTokens_EnvironmentConfiguration(t *testing.T) {
	old := os.Getenv("TRUVAG3_MICRO_RESOLUTION_MAX_TOKENS")
	defer func() {
		if old != "" {
			_ = os.Setenv("TRUVAG3_MICRO_RESOLUTION_MAX_TOKENS", old)
		} else {
			_ = os.Unsetenv("TRUVAG3_MICRO_RESOLUTION_MAX_TOKENS")
		}
	}()

	t.Run("override via env", func(t *testing.T) {
		_ = os.Setenv("TRUVAG3_MICRO_RESOLUTION_MAX_TOKENS", "4000")
		config := DefaultConfig()
		if config.MicroResolutionMaxTokens != 4000 {
			t.Errorf("Expected MicroResolutionMaxTokens 4000, got %d", config.MicroResolutionMaxTokens)
		}
	})

	t.Run("invalid env keeps default", func(t *testing.T) {
		_ = os.Setenv("TRUVAG3_MICRO_RESOLUTION_MAX_TOKENS", "invalid")
		config := DefaultConfig()
		if config.MicroResolutionMaxTokens != 2000 {
			t.Errorf("Expected MicroResolutionMaxTokens 2000 (default), got %d", config.MicroResolutionMaxTokens)
		}
	})

	t.Run("negative env keeps default", func(t *testing.T) {
		_ = os.Setenv("TRUVAG3_MICRO_RESOLUTION_MAX_TOKENS", "-1")
		config := DefaultConfig()
		if config.MicroResolutionMaxTokens != 2000 {
			t.Errorf("Expected MicroResolutionMaxTokens 2000 (default), got %d", config.MicroResolutionMaxTokens)
		}
	})

	t.Run("zero env keeps default", func(t *testing.T) {
		_ = os.Setenv("TRUVAG3_MICRO_RESOLUTION_MAX_TOKENS", "0")
		config := DefaultConfig()
		if config.MicroResolutionMaxTokens != 2000 {
			t.Errorf("Expected MicroResolutionMaxTokens 2000 (default), got %d", config.MicroResolutionMaxTokens)
		}
	})
}

func TestWithMicroResolutionMaxTokens(t *testing.T) {
	t.Run("set valid max tokens", func(t *testing.T) {
		config := DefaultConfig()
		opt := WithMicroResolutionMaxTokens(4000)
		opt(config)
		if config.MicroResolutionMaxTokens != 4000 {
			t.Errorf("Expected MicroResolutionMaxTokens 4000, got %d", config.MicroResolutionMaxTokens)
		}
	})

	t.Run("zero max tokens ignored", func(t *testing.T) {
		config := DefaultConfig()
		opt := WithMicroResolutionMaxTokens(0)
		opt(config)
		if config.MicroResolutionMaxTokens != 2000 {
			t.Errorf("Expected MicroResolutionMaxTokens 2000 (unchanged), got %d", config.MicroResolutionMaxTokens)
		}
	})

	t.Run("negative max tokens ignored", func(t *testing.T) {
		config := DefaultConfig()
		opt := WithMicroResolutionMaxTokens(-100)
		opt(config)
		if config.MicroResolutionMaxTokens != 2000 {
			t.Errorf("Expected MicroResolutionMaxTokens 2000 (unchanged), got %d", config.MicroResolutionMaxTokens)
		}
	})
}

func TestWithMicroResolutionMaxTokens_OptionOverridesEnv(t *testing.T) {
	old := os.Getenv("TRUVAG3_MICRO_RESOLUTION_MAX_TOKENS")
	defer func() {
		if old != "" {
			_ = os.Setenv("TRUVAG3_MICRO_RESOLUTION_MAX_TOKENS", old)
		} else {
			_ = os.Unsetenv("TRUVAG3_MICRO_RESOLUTION_MAX_TOKENS")
		}
	}()

	_ = os.Setenv("TRUVAG3_MICRO_RESOLUTION_MAX_TOKENS", "3000")

	deps := OrchestratorDependencies{
		Discovery: NewMockDiscovery(),
		AIClient:  NewMockAIClient(),
	}

	orchestrator, err := CreateOrchestratorWithOptions(deps, WithMicroResolutionMaxTokens(5000))
	if err != nil {
		t.Fatalf("Failed to create orchestrator: %v", err)
	}

	if orchestrator.config.MicroResolutionMaxTokens != 5000 {
		t.Errorf("Expected MicroResolutionMaxTokens 5000 (option override), got %d",
			orchestrator.config.MicroResolutionMaxTokens)
	}
}

// ============================================================================
// ORCH-014: Factory wiring of CustomInstructions to TieredCapabilityProvider
// ============================================================================

func TestCreateOrchestrator_TieredProvider_CustomInstructionsWired(t *testing.T) {
	deps := OrchestratorDependencies{
		Discovery: NewMockDiscovery(),
		AIClient:  NewMockAIClient(),
		Logger:    &mockLogger{},
	}

	config := DefaultConfig()
	config.EnableTieredResolution = true
	config.PromptConfig.CustomInstructions = []string{
		"Always check for reusable scripts",
		"Use project_key QA for JIRA",
	}

	orchestrator, err := CreateOrchestrator(config, deps)
	if err != nil {
		t.Fatalf("Failed to create orchestrator: %v", err)
	}

	tiered, ok := orchestrator.capabilityProvider.(*TieredCapabilityProvider)
	if !ok {
		t.Fatal("Expected TieredCapabilityProvider")
	}
	if len(tiered.customInstructions) != 2 {
		t.Errorf("Expected 2 CustomInstructions, got %d", len(tiered.customInstructions))
	}
	if tiered.customInstructions[0] != "Always check for reusable scripts" {
		t.Errorf("Unexpected first instruction: %s", tiered.customInstructions[0])
	}
}

func TestCreateOrchestrator_TieredProvider_NoCustomInstructions(t *testing.T) {
	deps := OrchestratorDependencies{
		Discovery: NewMockDiscovery(),
		AIClient:  NewMockAIClient(),
		Logger:    &mockLogger{},
	}

	config := DefaultConfig()
	config.EnableTieredResolution = true
	// No CustomInstructions set

	orchestrator, err := CreateOrchestrator(config, deps)
	if err != nil {
		t.Fatalf("Failed to create orchestrator: %v", err)
	}

	tiered, ok := orchestrator.capabilityProvider.(*TieredCapabilityProvider)
	if !ok {
		t.Fatal("Expected TieredCapabilityProvider")
	}
	if len(tiered.customInstructions) != 0 {
		t.Errorf("Expected 0 CustomInstructions, got %d", len(tiered.customInstructions))
	}
}

// --- Phase 1: LLM distillation is the default result-processing path ---

// TestCreateOrchestrator_DistillationDefaultOn verifies the Phase 1 default flip:
// distillation is on by default, so with an AIClient present the wired processor is
// the LLMDistiller; with no AIClient it falls open to the StructuralTrimmer floor;
// and an injected custom processor (Layer 3) still wins over both.
func TestCreateOrchestrator_DistillationDefaultOn(t *testing.T) {
	t.Run("default config enables distillation", func(t *testing.T) {
		if !DefaultConfig().ResultDistill.Enabled {
			t.Error("Expected ResultDistill.Enabled=true by default (Phase 1)")
		}
	})

	t.Run("AIClient present wires LLMDistiller", func(t *testing.T) {
		deps := OrchestratorDependencies{
			Discovery: NewMockDiscovery(),
			AIClient:  NewMockAIClient(),
		}
		orchestrator, err := CreateOrchestrator(DefaultConfig(), deps)
		if err != nil {
			t.Fatalf("CreateOrchestrator: %v", err)
		}
		if _, ok := orchestrator.resultProcessor.(*LLMDistiller); !ok {
			t.Errorf("Expected *LLMDistiller, got %T", orchestrator.resultProcessor)
		}
	})

	t.Run("no AIClient falls open to StructuralTrimmer", func(t *testing.T) {
		deps := OrchestratorDependencies{
			Discovery: NewMockDiscovery(),
			AIClient:  nil,
		}
		orchestrator, err := CreateOrchestrator(DefaultConfig(), deps)
		if err != nil {
			t.Fatalf("CreateOrchestrator: %v", err)
		}
		if _, ok := orchestrator.resultProcessor.(*StructuralTrimmer); !ok {
			t.Errorf("Expected *StructuralTrimmer floor when AIClient is nil, got %T", orchestrator.resultProcessor)
		}
	})

	t.Run("custom ResultProcessor wins (Layer 3)", func(t *testing.T) {
		custom := NewStructuralTrimmer([]string{"sentinel"}, nil)
		deps := OrchestratorDependencies{
			Discovery:       NewMockDiscovery(),
			AIClient:        NewMockAIClient(),
			ResultProcessor: custom,
		}
		orchestrator, err := CreateOrchestrator(DefaultConfig(), deps)
		if err != nil {
			t.Fatalf("CreateOrchestrator: %v", err)
		}
		if orchestrator.resultProcessor != custom {
			t.Errorf("Expected the injected custom processor to win, got %T", orchestrator.resultProcessor)
		}
	})

	t.Run("DistillCache wraps the distiller in a cache (Phase 3)", func(t *testing.T) {
		deps := OrchestratorDependencies{
			Discovery:    NewMockDiscovery(),
			AIClient:     NewMockAIClient(),
			DistillCache: &core.MockDigestCache{},
		}
		orchestrator, err := CreateOrchestrator(DefaultConfig(), deps)
		if err != nil {
			t.Fatalf("CreateOrchestrator: %v", err)
		}
		if _, ok := orchestrator.resultProcessor.(*cachingProcessor); !ok {
			t.Errorf("Expected *cachingProcessor when DistillCache is provided, got %T", orchestrator.resultProcessor)
		}
	})
}

// TestBuildDistillationEnabledResultProcessor covers the Layer-2 constructor's wiring.
func TestBuildDistillationEnabledResultProcessor(t *testing.T) {
	cfg := DefaultConfig().ResultDistill

	t.Run("AI + cache yields a caching distiller", func(t *testing.T) {
		p := BuildDistillationEnabledResultProcessor(cfg, NewMockAIClient(), &core.MockDigestCache{}, nil)
		if _, ok := p.(*cachingProcessor); !ok {
			t.Errorf("expected *cachingProcessor, got %T", p)
		}
	})

	t.Run("AI, no cache yields a bare distiller", func(t *testing.T) {
		p := BuildDistillationEnabledResultProcessor(cfg, NewMockAIClient(), nil, nil)
		if _, ok := p.(*LLMDistiller); !ok {
			t.Errorf("expected *LLMDistiller, got %T", p)
		}
	})

	t.Run("nil AI falls open to the StructuralTrimmer floor", func(t *testing.T) {
		p := BuildDistillationEnabledResultProcessor(cfg, nil, nil, nil)
		if _, ok := p.(*StructuralTrimmer); !ok {
			t.Errorf("expected *StructuralTrimmer floor, got %T", p)
		}
	})
}
