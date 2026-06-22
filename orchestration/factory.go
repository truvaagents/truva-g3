package orchestration

import (
	"fmt"
	"os"
	"time"

	"github.com/truvaagents/truva-g3/core"
	"github.com/truvaagents/truva-g3/telemetry"
)

// OrchestratorDependencies holds optional dependencies for the orchestrator
// This follows the dependency injection pattern used by the UI module
type OrchestratorDependencies struct {
	// Required dependencies
	Discovery core.Discovery
	AIClient  core.AIClient

	// Optional dependencies (can be nil)
	CircuitBreaker core.CircuitBreaker // For sophisticated resilience patterns
	Logger         core.Logger         // For structured logging
	Telemetry      core.Telemetry      // For observability

	// Optional: Custom prompt building (Layer 3)
	// If nil, DefaultPromptBuilder is used based on config.PromptConfig
	PromptBuilder PromptBuilder

	// Optional: Enable LLM-based error analysis (Layer 3: Error Analysis)
	// When true, creates an ErrorAnalyzer that uses LLM to determine if errors
	// can be fixed with different parameters. This removes the need for tools
	// to set Retryable flags. See PARAMETER_BINDING_FIX.md for design rationale.
	EnableErrorAnalyzer bool

	// Optional: Custom result processor for domain-specific trimming.
	// If nil and ResultTrim.Enabled, uses default StructuralTrimmer.
	ResultProcessor ResultProcessor

	// Optional: cache for LLM distillation results, keyed by (result + instruction +
	// budget). When set, the distiller is wrapped so identical compactions (common in
	// scheduled/repetitive runs) become cache hits. Nil disables caching (fail-open).
	// Reuses the core.DigestCache contract — pass the same Redis-backed cache used for
	// activity digests, or a dedicated one. Keys are namespaced ("distill:") so they do
	// not collide with activity digests.
	DistillCache core.DigestCache

	// Optional: Pipeline hooks for context engineering.
	// Hooks run at each pipeline stage (before planning, after synthesis, etc.).
	PipelineHooks []core.PipelineHook

	// Optional: shared conversation-history preparer for metadata and hook ingress paths.
	// If nil, the factory auto-builds the default Tier 1 processor from config.
	ConversationHistoryPreparer ConversationHistoryPreparer

	// Optional: Activity coordinator for real-time agent coordination signals.
	ActivityCoordinator core.ActivityCoordinator
}

// CreateOrchestrator creates an orchestrator with proper module integration and dependency injection
func CreateOrchestrator(config *OrchestratorConfig, deps OrchestratorDependencies) (*AIOrchestrator, error) {
	if config == nil {
		config = DefaultConfig()
	}

	var factoryLogger core.Logger
	if deps.Logger != nil {
		// Apply component-specific logging for orchestration module
		if cal, ok := deps.Logger.(core.ComponentAwareLogger); ok {
			factoryLogger = cal.WithComponent("framework/orchestration")
		} else {
			factoryLogger = deps.Logger
		}
	} else {
		// Use NoOpLogger to avoid creating a parallel logging setup.
		// The framework's logging is configured centrally via core.NewFramework().
		// If you want orchestration logs, pass the agent's Logger in OrchestratorDependencies:
		//   deps := OrchestratorDependencies{Logger: agent.Logger, ...}
		// This follows the same pattern as core/agent.go which uses NoOpLogger as the default.
		factoryLogger = &core.NoOpLogger{}
	}
	deps.Logger = factoryLogger

	factoryLogger.Info("Creating orchestrator instance", map[string]interface{}{
		"operation":                "orchestrator_creation",
		"routing_mode":             string(config.RoutingMode),
		"capability_provider_type": config.CapabilityProviderType,
		"telemetry_enabled":        config.EnableTelemetry,
	})

	// Pass optional dependencies to service capability provider if configured
	if config.CapabilityProviderType == "service" {
		// Inject optional dependencies into service config
		config.CapabilityService.CircuitBreaker = deps.CircuitBreaker
		config.CapabilityService.Logger = deps.Logger
		config.CapabilityService.Telemetry = deps.Telemetry
	}

	// Create orchestrator
	orchestrator := NewAIOrchestrator(config, deps.Discovery, deps.AIClient)

	if deps.ConversationHistoryPreparer == nil {
		preparer, err := BuildConversationHistoryProcessor(config)
		if err != nil {
			return nil, fmt.Errorf("build conversation history processor: %w", err)
		}
		deps.ConversationHistoryPreparer = preparer
	}
	orchestrator.SetConversationHistoryPreparer(deps.ConversationHistoryPreparer)

	// Validate service configuration if using service provider
	if config.CapabilityProviderType == "service" && config.CapabilityService.Endpoint == "" {
		// Check if auto-configuration found it
		if endpoint := os.Getenv("TRUVAG3_CAPABILITY_SERVICE_URL"); endpoint == "" {
			return nil, fmt.Errorf("capability service URL required: set CapabilityService.Endpoint in config or TRUVAG3_CAPABILITY_SERVICE_URL environment variable")
		}
	}

	// Set up telemetry if provided or create one if enabled
	if deps.Telemetry != nil {
		orchestrator.SetTelemetry(deps.Telemetry)
	} else if config.EnableTelemetry {
		// Check for telemetry endpoint in environment
		endpoint := os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT")
		if endpoint == "" {
			endpoint = os.Getenv("TRUVAG3_TELEMETRY_ENDPOINT")
		}

		// Use the framework's telemetry module
		otelProvider, err := telemetry.NewOTelProvider("orchestrator", "orchestrator", endpoint)
		if err != nil {
			// Resilient runtime behavior - continue without telemetry
			if factoryLogger != nil {
				factoryLogger.Warn("Failed to initialize telemetry", map[string]interface{}{
					"operation": "telemetry_initialization",
					"error":     err.Error(),
					"endpoint":  endpoint,
				})
			}
		} else {
			orchestrator.SetTelemetry(otelProvider)
		}
	}

	orchestrator.SetLogger(deps.Logger)

	// Initialize prompt builder based on configuration
	// Priority: 1) Injected builder (Layer 3), 2) Template (Layer 2), 3) Default (Layer 1)
	if deps.PromptBuilder != nil {
		// Layer 3: Custom builder injected by application
		orchestrator.SetPromptBuilder(deps.PromptBuilder)
		factoryLogger.Info("Using custom PromptBuilder", map[string]interface{}{
			"operation":    "prompt_builder_initialization",
			"builder_type": "custom",
		})
	} else if config.PromptConfig.TemplateFile != "" || config.PromptConfig.Template != "" {
		// Layer 2: Template-based customization
		builder, err := NewTemplatePromptBuilder(&config.PromptConfig)
		if err != nil {
			// Graceful degradation to DefaultPromptBuilder
			factoryLogger.Warn("Failed to create TemplatePromptBuilder, using default", map[string]interface{}{
				"operation":     "prompt_builder_initialization",
				"error":         err.Error(),
				"template_file": config.PromptConfig.TemplateFile,
			})
			defaultBuilder, _ := NewDefaultPromptBuilder(&config.PromptConfig)
			defaultBuilder.SetLogger(deps.Logger)
			defaultBuilder.SetTelemetry(deps.Telemetry)
			orchestrator.SetPromptBuilder(defaultBuilder)
		} else {
			builder.SetLogger(deps.Logger)
			builder.SetTelemetry(deps.Telemetry)
			orchestrator.SetPromptBuilder(builder)
			factoryLogger.Info("Using TemplatePromptBuilder", map[string]interface{}{
				"operation":     "prompt_builder_initialization",
				"builder_type":  "template",
				"template_file": config.PromptConfig.TemplateFile,
			})
		}
	} else {
		// Layer 1: Default with optional type rule extensions
		defaultBuilder, _ := NewDefaultPromptBuilder(&config.PromptConfig)
		defaultBuilder.SetLogger(deps.Logger)
		defaultBuilder.SetTelemetry(deps.Telemetry)
		orchestrator.SetPromptBuilder(defaultBuilder)
		factoryLogger.Info("Using DefaultPromptBuilder", map[string]interface{}{
			"operation":        "prompt_builder_initialization",
			"builder_type":     "default",
			"additional_rules": len(config.PromptConfig.AdditionalTypeRules),
			"domain":           config.PromptConfig.Domain,
		})
	}

	// Initialize TieredCapabilityProvider if enabled (token optimization)
	// Priority: 1) Tiered (if enabled), 2) Service (if configured), 3) Default
	var tieredProvider *TieredCapabilityProvider
	if config.EnableTieredResolution {
		tieredProvider = NewTieredCapabilityProvider(
			orchestrator.catalog,
			deps.AIClient,
			&config.TieredResolution,
		)
		tieredProvider.SetLogger(deps.Logger)
		tieredProvider.SetTelemetry(deps.Telemetry)
		if deps.CircuitBreaker != nil {
			tieredProvider.SetCircuitBreaker(deps.CircuitBreaker)
		}
		tieredProvider.SetAIOptionsOverride(config.TieredSelectionAIOptions)

		// ORCH-014 fix: Inject CustomInstructions so tiered selection is aware of
		// domain-specific tool requirements not implied by the user query.
		if len(config.PromptConfig.CustomInstructions) > 0 {
			tieredProvider.SetCustomInstructions(config.PromptConfig.CustomInstructions)
			factoryLogger.Info("Tiered selection CustomInstructions configured", map[string]interface{}{
				"custom_instructions_count": len(config.PromptConfig.CustomInstructions),
				"operation":                 "factory_init",
			})
		}

		orchestrator.SetCapabilityProvider(tieredProvider)

		factoryLogger.Info("Using TieredCapabilityProvider for token optimization", map[string]interface{}{
			"operation": "capability_provider_initialization",
			"min_tools": config.TieredResolution.MinToolsForTiering,
			"enabled":   true,
		})
	} else {
		factoryLogger.Debug("TieredCapabilityProvider disabled, using default capability provider", map[string]interface{}{
			"operation": "capability_provider_initialization",
			"enabled":   false,
		})
	}

	// Configure LLM-based error analyzer if enabled (Layer 3: Error Analysis)
	// This removes the need for tools to set Retryable flags - the LLM decides
	if deps.EnableErrorAnalyzer && deps.AIClient != nil {
		errorAnalyzer := NewErrorAnalyzer(deps.AIClient, deps.Logger)
		errorAnalyzer.SetAIOptionsOverride(config.ErrorAnalysisAIOptions)
		orchestrator.SetErrorAnalyzer(errorAnalyzer)
		factoryLogger.Info("LLM error analyzer enabled", map[string]interface{}{
			"operation": "error_analyzer_initialization",
		})
	}

	// Initialize LLM Debug Store if enabled
	// This provides full payload visibility for debugging orchestration issues.
	// Disabled by default - enable via TRUVAG3_LLM_DEBUG_ENABLED=true or WithLLMDebug(true)
	if config.LLMDebug.Enabled {
		if config.LLMDebugStore == nil {
			// Auto-configure Redis store from environment
			store, err := NewRedisLLMDebugStore(
				WithDebugRedisDB(config.LLMDebug.RedisDB),
				WithDebugLogger(deps.Logger),
				WithDebugTTL(config.LLMDebug.TTL),
				WithDebugErrorTTL(config.LLMDebug.ErrorTTL),
			)
			if err != nil {
				// Resilient behavior - use NoOp store if Redis unavailable
				factoryLogger.Warn("Failed to initialize Redis LLM debug store, using NoOp", map[string]interface{}{
					"operation": "llm_debug_store_initialization",
					"error":     err.Error(),
					"hint":      "Set REDIS_URL or TRUVAG3_REDIS_URL, or disable via TRUVAG3_LLM_DEBUG_ENABLED=false",
				})
				config.LLMDebugStore = NewNoOpLLMDebugStore()
			} else {
				config.LLMDebugStore = store
				factoryLogger.Info("Redis LLM debug store initialized", map[string]interface{}{
					"operation": "llm_debug_store_initialization",
					"redis_db":  config.LLMDebug.RedisDB,
					"ttl":       config.LLMDebug.TTL.String(),
					"error_ttl": config.LLMDebug.ErrorTTL.String(),
				})
			}
		} else {
			factoryLogger.Info("Using custom LLM debug store", map[string]interface{}{
				"operation": "llm_debug_store_initialization",
			})
		}
		orchestrator.SetLLMDebugStore(config.LLMDebugStore)

		// Propagate LLM debug store to TieredCapabilityProvider for tiered_selection recording
		if tieredProvider != nil {
			tieredProvider.SetLLMDebugStore(config.LLMDebugStore)
		}
	}

	// Set up execution store if enabled
	// Auto-configures from environment (same pattern as LLM Debug Store).
	// Enable via TRUVAG3_EXECUTION_DEBUG_STORE_ENABLED=true
	if config.ExecutionStore.Enabled {
		if config.ExecutionStoreBackend != nil {
			// Custom backend provided - use it
			orchestrator.SetExecutionStore(config.ExecutionStoreBackend)
			factoryLogger.Info("ExecutionStore configured with custom backend", map[string]interface{}{
				"operation": "execution_store_initialization",
				"ttl":       config.ExecutionStore.TTL.String(),
				"error_ttl": config.ExecutionStore.ErrorTTL.String(),
			})
		} else {
			// Auto-configure Redis store from environment (same pattern as LLM Debug Store)
			store, err := NewRedisExecutionDebugStore(
				WithExecutionDebugRedisDB(core.RedisDBExecutionDebug),
				WithExecutionDebugLogger(deps.Logger),
				WithExecutionDebugTTL(config.ExecutionStore.TTL),
				WithExecutionDebugErrorTTL(config.ExecutionStore.ErrorTTL),
				WithExecutionDebugKeyPrefix(config.ExecutionStore.KeyPrefix),
			)
			if err != nil {
				// Resilient behavior - use NoOp store if Redis unavailable
				factoryLogger.Warn("Failed to initialize Redis execution debug store, using NoOp", map[string]interface{}{
					"operation": "execution_debug_store_initialization",
					"error":     err.Error(),
					"hint":      "Set REDIS_URL or TRUVAG3_REDIS_URL, or disable via TRUVAG3_EXECUTION_DEBUG_STORE_ENABLED=false",
				})
				orchestrator.SetExecutionStore(NewNoOpExecutionStore())
			} else {
				orchestrator.SetExecutionStore(store)
				factoryLogger.Info("Redis execution debug store initialized", map[string]interface{}{
					"operation":  "execution_debug_store_initialization",
					"redis_db":   core.RedisDBExecutionDebug,
					"key_prefix": config.ExecutionStore.KeyPrefix,
					"ttl":        config.ExecutionStore.TTL.String(),
					"error_ttl":  config.ExecutionStore.ErrorTTL.String(),
				})
			}
		}
	}

	// Configure result trimming (large data management)
	if config.ResultTrim.Enabled {
		if deps.ResultProcessor != nil {
			orchestrator.SetResultProcessor(deps.ResultProcessor)
			factoryLogger.Info("Result trimming enabled with custom processor", map[string]interface{}{
				"operation": "result_trim_initialization", "processor": "custom",
				"max_result_bytes": config.ResultTrim.MaxResultBytes,
			})
		} else if config.ResultDistill.Enabled && deps.AIClient != nil {
			trimmer := NewStructuralTrimmer(config.ResultTrim.PreserveKeys, deps.Logger)
			distiller := NewLLMDistiller(deps.AIClient, config.ResultDistill, trimmer, deps.Logger)
			distiller.SetAIOptionsOverride(config.ResultDistillAIOptions)
			// Propagate debugStore so distillation LLM calls appear in the LLM Debug tab.
			if orchestrator.debugStore != nil {
				distiller.SetLLMDebugStore(orchestrator.debugStore)
			}
			// Wrap with a content-addressed cache when one is provided (fail-open: a nil
			// cache returns the bare distiller, so this is a no-op without a cache).
			processor := NewCachingProcessor(distiller, deps.DistillCache, config.ResultDistill.CacheTTL, config.ResultDistill.DistillThreshold, distillKeySalt(config.ResultDistill, config.ResultDistillAIOptions), deps.Logger)
			orchestrator.SetResultProcessor(processor)
			factoryLogger.Info("Result processing: LLM distillation (two-stage)", map[string]interface{}{
				"operation":         "result_trim_initialization",
				"distill_threshold": config.ResultDistill.DistillThreshold,
				"prefilter_budget":  config.ResultDistill.PreFilterBudget,
				"target_size":       config.ResultDistill.TargetSize,
				"cache_enabled":     deps.DistillCache != nil,
			})
		} else {
			orchestrator.SetResultProcessor(NewStructuralTrimmer(config.ResultTrim.PreserveKeys, deps.Logger))
			factoryLogger.Info("Result processing: StructuralTrimmer", map[string]interface{}{
				"operation":              "result_trim_initialization",
				"max_result_bytes":       config.ResultTrim.MaxResultBytes,
				"max_total_prompt_bytes": config.ResultTrim.MaxTotalPromptBytes,
				"preserve_keys":          len(config.ResultTrim.PreserveKeys),
			})
		}
	}

	// Wire pipeline hooks for context engineering
	if len(deps.PipelineHooks) > 0 {
		orchestrator.pipelineHooks = deps.PipelineHooks
		factoryLogger.Info("Pipeline hooks configured", map[string]interface{}{
			"operation": "pipeline_hooks_initialization",
			"count":     len(deps.PipelineHooks),
		})

		// Propagate debug store and telemetry to hooks that need them.
		// This must happen AFTER hooks are assigned (SetLLMDebugStore/SetTelemetry
		// were called earlier when pipelineHooks was still empty).
		if deps.Logger != nil {
			for _, hook := range orchestrator.pipelineHooks {
				if loggerAware, ok := hook.(interface{ SetLogger(core.Logger) }); ok {
					loggerAware.SetLogger(deps.Logger)
				}
			}
		}
		if orchestrator.debugStore != nil {
			for _, hook := range orchestrator.pipelineHooks {
				if debuggable, ok := hook.(interface{ SetLLMDebugStore(LLMDebugStore) }); ok {
					debuggable.SetLLMDebugStore(orchestrator.debugStore)
				}
			}
		}
		if orchestrator.telemetry != nil {
			for _, hook := range orchestrator.pipelineHooks {
				if telemetryAware, ok := hook.(interface{ SetTelemetry(core.Telemetry) }); ok {
					telemetryAware.SetTelemetry(orchestrator.telemetry)
				}
			}
		}
	}

	// Wire activity coordinator if provided
	if deps.ActivityCoordinator != nil {
		orchestrator.activityCoordinator = deps.ActivityCoordinator
	}

	factoryLogger.Info("Orchestrator created successfully", map[string]interface{}{
		"operation":       "orchestrator_creation_complete",
		"success":         true,
		"error_analyzer":  deps.EnableErrorAnalyzer,
		"llm_debug":       config.LLMDebug.Enabled,
		"execution_store": config.ExecutionStore.Enabled,
		"result_trim":     config.ResultTrim.Enabled,
		"pipeline_hooks":  len(deps.PipelineHooks),
	})

	return orchestrator, nil
}

// OrchestratorOption is a function that configures the orchestrator
type OrchestratorOption func(*OrchestratorConfig)

// WithCapabilityProvider creates an option for setting capability provider
func WithCapabilityProvider(providerType string, serviceURL string) OrchestratorOption {
	return func(c *OrchestratorConfig) {
		c.CapabilityProviderType = providerType
		if providerType == "service" && serviceURL != "" {
			// Auto-configure related settings when intent is clear
			c.CapabilityService.Endpoint = serviceURL
			c.EnableFallback = true // Smart default for production
		}
	}
}

// WithTelemetry creates an option for enabling/disabling telemetry
func WithTelemetry(enabled bool) OrchestratorOption {
	return func(c *OrchestratorConfig) {
		c.EnableTelemetry = enabled
	}
}

// WithFallback creates an option for enabling/disabling fallback
func WithFallback(enabled bool) OrchestratorOption {
	return func(c *OrchestratorConfig) {
		c.EnableFallback = enabled
	}
}

// WithPlanParseRetry creates an option for configuring plan parse retry behavior.
// When enabled, the orchestrator will retry LLM plan generation if JSON parsing fails
// due to invalid syntax (e.g., arithmetic expressions, malformed JSON).
//
// Parameters:
//   - enabled: whether to retry on JSON parse failures
//   - maxRetries: maximum number of retry attempts (0 = no retries, default: 2)
func WithPlanParseRetry(enabled bool, maxRetries int) OrchestratorOption {
	return func(c *OrchestratorConfig) {
		c.PlanParseRetryEnabled = enabled
		if maxRetries >= 0 {
			c.PlanParseMaxRetries = maxRetries
		}
	}
}

func WithPlanAIOptions(opts *AIOptionsOverride) OrchestratorOption {
	return func(c *OrchestratorConfig) {
		c.PlanAIOptions = opts
	}
}

func WithSynthesisAIOptions(opts *AIOptionsOverride) OrchestratorOption {
	return func(c *OrchestratorConfig) {
		c.SynthesisAIOptions = opts
	}
}

func WithMicroResolutionAIOptions(opts *AIOptionsOverride) OrchestratorOption {
	return func(c *OrchestratorConfig) {
		c.MicroResolutionAIOptions = opts
	}
}

func WithTieredSelectionAIOptions(opts *AIOptionsOverride) OrchestratorOption {
	return func(c *OrchestratorConfig) {
		c.TieredSelectionAIOptions = opts
	}
}

func WithErrorAnalysisAIOptions(opts *AIOptionsOverride) OrchestratorOption {
	return func(c *OrchestratorConfig) {
		c.ErrorAnalysisAIOptions = opts
	}
}

func WithResultDistillAIOptions(opts *AIOptionsOverride) OrchestratorOption {
	return func(c *OrchestratorConfig) {
		c.ResultDistillAIOptions = opts
	}
}

// Deprecated compatibility helpers. Prefer the per-phase With*AIOptions variants.
func WithPlanMaxTokens(maxTokens int) OrchestratorOption {
	return func(c *OrchestratorConfig) {
		c.legacyAIOptionBridge = true
		if maxTokens > 0 {
			c.PlanMaxTokens = maxTokens
			if c.PlanAIOptions == nil {
				c.PlanAIOptions = &AIOptionsOverride{}
			}
			c.PlanAIOptions.MaxTokens = IntPtr(maxTokens)
		}
	}
}

func WithPlanModel(model string) OrchestratorOption {
	return func(c *OrchestratorConfig) {
		c.legacyAIOptionBridge = true
		c.PlanModel = model
		if c.PlanAIOptions == nil {
			c.PlanAIOptions = &AIOptionsOverride{}
		}
		c.PlanAIOptions.Model = StringPtr(model)
	}
}

func WithSynthesisMaxTokens(maxTokens int) OrchestratorOption {
	return func(c *OrchestratorConfig) {
		c.legacyAIOptionBridge = true
		if maxTokens > 0 {
			c.SynthesisMaxTokens = maxTokens
			if c.SynthesisAIOptions == nil {
				c.SynthesisAIOptions = &AIOptionsOverride{}
			}
			c.SynthesisAIOptions.MaxTokens = IntPtr(maxTokens)
		}
	}
}

func WithSynthesisTemperature(temp float64) OrchestratorOption {
	return func(c *OrchestratorConfig) {
		c.legacyAIOptionBridge = true
		if temp >= 0 && temp <= 2.0 {
			c.SynthesisTemperature = roundLegacyFloat(temp)
			if c.SynthesisAIOptions == nil {
				c.SynthesisAIOptions = &AIOptionsOverride{}
			}
			c.SynthesisAIOptions.Temperature = Float32Ptr(float32(temp))
		}
	}
}

func WithSynthesisModel(model string) OrchestratorOption {
	return func(c *OrchestratorConfig) {
		c.legacyAIOptionBridge = true
		c.SynthesisModel = model
		if c.SynthesisAIOptions == nil {
			c.SynthesisAIOptions = &AIOptionsOverride{}
		}
		c.SynthesisAIOptions.Model = StringPtr(model)
	}
}

func WithMicroResolutionModel(model string) OrchestratorOption {
	return func(c *OrchestratorConfig) {
		c.legacyAIOptionBridge = true
		c.MicroResolutionModel = model
		if c.MicroResolutionAIOptions == nil {
			c.MicroResolutionAIOptions = &AIOptionsOverride{}
		}
		c.MicroResolutionAIOptions.Model = StringPtr(model)
	}
}

func WithMicroResolutionMaxTokens(maxTokens int) OrchestratorOption {
	return func(c *OrchestratorConfig) {
		c.legacyAIOptionBridge = true
		if maxTokens > 0 {
			c.MicroResolutionMaxTokens = maxTokens
			if c.MicroResolutionAIOptions == nil {
				c.MicroResolutionAIOptions = &AIOptionsOverride{}
			}
			c.MicroResolutionAIOptions.MaxTokens = IntPtr(maxTokens)
		}
	}
}

// WithMaxConcurrency configures the max number of parallel step executions.
// Default: 5. Controls how many DAG steps can execute simultaneously.
func WithMaxConcurrency(max int) OrchestratorOption {
	return func(c *OrchestratorConfig) {
		if max > 0 {
			c.ExecutionOptions.MaxConcurrency = max
		}
	}
}

// WithStepTimeout configures the per-step execution timeout.
// Default: 120s. Each individual step must complete within this duration.
func WithStepTimeout(timeout time.Duration) OrchestratorOption {
	return func(c *OrchestratorConfig) {
		if timeout > 0 {
			c.ExecutionOptions.StepTimeout = timeout
		}
	}
}

// WithTotalTimeout configures the total HTTP client timeout for tool/agent calls.
// Default: 600s. Controls how long the executor waits for individual HTTP responses.
func WithTotalTimeout(timeout time.Duration) OrchestratorOption {
	return func(c *OrchestratorConfig) {
		if timeout > 0 {
			c.ExecutionOptions.TotalTimeout = timeout
		}
	}
}

// WithHallucinationRetry creates an option for configuring hallucination retry behavior.
// When enabled, the orchestrator will retry LLM plan generation if the LLM hallucinates
// agent names that were not in the allowed list provided in the prompt.
// See orchestration/bugs/BUG_LLM_HALLUCINATED_TOOL.md for detailed analysis.
//
// Parameters:
//   - enabled: whether to retry on hallucination detection
//   - maxRetries: maximum number of retry attempts (0 = no retries, default: 1)
func WithHallucinationRetry(enabled bool, maxRetries int) OrchestratorOption {
	return func(c *OrchestratorConfig) {
		c.HallucinationRetryEnabled = enabled
		if maxRetries >= 0 {
			c.HallucinationMaxRetries = maxRetries
		}
	}
}

// CreateOrchestratorWithOptions creates an orchestrator with option functions
func CreateOrchestratorWithOptions(deps OrchestratorDependencies, opts ...OrchestratorOption) (*AIOrchestrator, error) {
	config := DefaultConfig()

	// Apply all options
	for _, opt := range opts {
		opt(config)
	}

	return CreateOrchestrator(config, deps)
}

// CreateSimpleOrchestrator creates an orchestrator with zero configuration
// This is perfect for developers who just want to get started quickly.
// It will:
// - Use the default capability provider (sends all capabilities to LLM)
// - Work with small to medium deployments (up to ~100 agents/tools)
// - Not require any external services
// - Use NoOpLogger by default (pass Logger in dependencies for logging)
func CreateSimpleOrchestrator(discovery core.Discovery, aiClient core.AIClient) *AIOrchestrator {
	// Use proper dependency injection to ensure all framework features work
	deps := OrchestratorDependencies{
		Discovery: discovery,
		AIClient:  aiClient,
		// Logger, Telemetry, CircuitBreaker will be auto-created with smart defaults
	}

	orchestrator, err := CreateOrchestrator(nil, deps)
	if err != nil {
		// This should never happen with default config, but follow fail-safe principles
		return NewAIOrchestrator(nil, discovery, aiClient)
	}

	return orchestrator
}

// WithCircuitBreaker creates an option for injecting a circuit breaker
func WithCircuitBreaker(cb core.CircuitBreaker) func(*OrchestratorDependencies) {
	return func(d *OrchestratorDependencies) {
		d.CircuitBreaker = cb
	}
}

// WithLogger creates an option for injecting a logger
func WithLogger(logger core.Logger) func(*OrchestratorDependencies) {
	return func(d *OrchestratorDependencies) {
		d.Logger = logger
	}
}

// WithLLMDebug enables or disables LLM debug payload storage.
// When enabled without explicit store, auto-uses Redis from discovery if available.
// Precedence: explicit config > REDIS_URL > TRUVAG3_REDIS_URL > discovery Redis
func WithLLMDebug(enabled bool) OrchestratorOption {
	return func(c *OrchestratorConfig) {
		c.LLMDebug.Enabled = enabled
	}
}

// WithLLMDebugStore explicitly sets the debug store implementation.
// Use this when you want a custom backend (PostgreSQL, S3, etc.)
// Setting a store automatically enables LLM debug.
func WithLLMDebugStore(store LLMDebugStore) OrchestratorOption {
	return func(c *OrchestratorConfig) {
		c.LLMDebug.Enabled = true
		c.LLMDebugStore = store
	}
}

// WithLLMDebugTTL sets custom TTL for successful debug records.
func WithLLMDebugTTL(ttl time.Duration) OrchestratorOption {
	return func(c *OrchestratorConfig) {
		c.LLMDebug.TTL = ttl
	}
}

// WithLLMDebugErrorTTL sets custom TTL for error debug records.
func WithLLMDebugErrorTTL(ttl time.Duration) OrchestratorOption {
	return func(c *OrchestratorConfig) {
		c.LLMDebug.ErrorTTL = ttl
	}
}

// WithExecutionStore explicitly sets the execution store implementation.
// Use this to inject a StorageProvider-backed store for DAG visualization.
// Setting a store automatically enables execution storage (same pattern as WithLLMDebugStore).
//
// Example:
//
//	provider := NewRedisStorageProvider(redisClient) // Application code
//	store := orchestration.NewExecutionStoreWithProvider(provider, config, logger)
//	orchestrator, _ := orchestration.NewOrchestrator(deps, orchestration.WithExecutionStore(store))
func WithExecutionStore(store ExecutionStore) OrchestratorOption {
	return func(c *OrchestratorConfig) {
		c.ExecutionStore.Enabled = true // Auto-enable when store is provided
		c.ExecutionStoreBackend = store
	}
}

// WithExecutionStoreProvider is a convenience function that creates an ExecutionStore
// from a StorageProvider. The application provides the StorageProvider implementation.
func WithExecutionStoreProvider(provider StorageProvider, logger core.Logger) OrchestratorOption {
	return func(c *OrchestratorConfig) {
		c.ExecutionStore.Enabled = true // Auto-enable when provider is provided
		c.ExecutionStoreBackend = NewExecutionStoreWithProvider(provider, c.ExecutionStore, logger)
	}
}

// WithExecutionStoreTTL sets custom TTL for successful execution records.
func WithExecutionStoreTTL(ttl time.Duration) OrchestratorOption {
	return func(c *OrchestratorConfig) {
		c.ExecutionStore.TTL = ttl
	}
}

// WithExecutionStoreErrorTTL sets custom TTL for error execution records.
func WithExecutionStoreErrorTTL(ttl time.Duration) OrchestratorOption {
	return func(c *OrchestratorConfig) {
		c.ExecutionStore.ErrorTTL = ttl
	}
}

// WithTieredResolution enables tiered capability resolution for token optimization.
// This is recommended for deployments with 20+ tools.
// Both tiers use the AI client's default model for simplicity.
func WithTieredResolution(enabled bool) OrchestratorOption {
	return func(c *OrchestratorConfig) {
		c.EnableTieredResolution = enabled
	}
}

// WithTieredSelectionMaxTokens configures the maximum output tokens for tiered
// selection LLM calls. Default: 2000.
func WithTieredSelectionMaxTokens(maxTokens int) OrchestratorOption {
	return func(c *OrchestratorConfig) {
		if maxTokens > 0 {
			c.TieredResolution.SelectionMaxTokens = maxTokens
		}
	}
}

// WithTieredSelectionRetry configures retry behavior for tiered selection
// when the LLM returns an empty response or unparseable JSON.
// Mirrors WithPlanParseRetry pattern for consistency.
//
// Parameters:
//   - enabled: whether to retry on empty responses and parse failures
//   - maxRetries: maximum number of retry attempts (0 = no retries, default: 2)
func WithTieredSelectionRetry(enabled bool, maxRetries int) OrchestratorOption {
	return func(c *OrchestratorConfig) {
		c.TieredResolution.RetryEnabled = enabled
		if maxRetries >= 0 {
			c.TieredResolution.MaxRetries = maxRetries
		}
	}
}

// WithResultTrimming configures result trimming for prompt construction.
func WithResultTrimming(enabled bool, maxResultBytes int) OrchestratorOption {
	return func(c *OrchestratorConfig) {
		c.ResultTrim.Enabled = enabled
		if maxResultBytes > 0 {
			c.ResultTrim.MaxResultBytes = maxResultBytes
		}
	}
}

// WithResultPreserveKeys sets keys that should always be preserved during trimming.
func WithResultPreserveKeys(keys []string) OrchestratorOption {
	return func(c *OrchestratorConfig) {
		c.ResultTrim.PreserveKeys = keys
	}
}

// WithResultDistill configures LLM-based result distillation (two-stage pipeline).
// When enabled (requires ResultTrim.Enabled=true and an AIClient), oversized step results
// are first structurally pre-filtered, then extractively distilled by an LLM.
// distillThreshold is the minimum result size in bytes to trigger distillation; a value
// <= 0 leaves the configured default unchanged.
// Defaults (DefaultConfig): enabled=true, distillThreshold=16384. Opt out with this
// option or TRUVAG3_RESULT_DISTILL_ENABLED=false.
func WithResultDistill(enabled bool, distillThreshold int) OrchestratorOption {
	return func(c *OrchestratorConfig) {
		c.ResultDistill.Enabled = enabled
		if distillThreshold > 0 {
			c.ResultDistill.DistillThreshold = distillThreshold
		}
	}
}

// WithResultDistillModel sets the model override for LLM distillation calls.
// Use a portable alias ("fast" is recommended) for ChainClient compatibility.
//
// Portable aliases ("fast", "default", "smart") work across all providers.
// Concrete model names (e.g., "gpt-4o-mini") only work with single-provider
// AIClient — they will break ChainClient failover (404 is non-retryable).
//
// If empty, the default AIClient model is used.
func WithResultDistillModel(model string) OrchestratorOption {
	return func(c *OrchestratorConfig) {
		c.ResultDistill.Model = model
	}
}

// BuildDistillationEnabledResultProcessor constructs the Layer-2 LLM-first result
// processor directly: a StructuralTrimmer (pre-filter + fail-open floor) wrapped by the
// LLMDistiller, wrapped by a fail-open content cache. Mirrors
// BuildCompactionEnabledConversationHistoryPreparer.
//
// Use this when wiring a processor outside CreateOrchestrator (which auto-builds the
// same stack from config — Layer 1). A nil ai yields the bare StructuralTrimmer floor;
// a nil cache disables caching. For a fully custom backend, pass it via
// deps.ResultProcessor (Layer 3) instead.
func BuildDistillationEnabledResultProcessor(
	cfg ResultDistillConfig, ai core.AIClient, cache core.DigestCache, logger core.Logger,
) ResultProcessor {
	trimmer := NewStructuralTrimmer(nil, logger)
	if ai == nil {
		return trimmer // fail-open: no model → structural floor only
	}
	distiller := NewLLMDistiller(ai, cfg, trimmer, logger)
	// This Layer-2 helper does not apply a per-phase AI options override, so the salt has
	// none. Callers using deps.ResultDistillAIOptions go through the factory path above.
	return NewCachingProcessor(distiller, cache, cfg.CacheTTL, cfg.DistillThreshold, distillKeySalt(cfg, nil), logger)
}
