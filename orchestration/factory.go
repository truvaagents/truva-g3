package orchestration

import (
	"context"
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

	// Optional: Custom result processor for the SYNTHESIS prompt (Layer 3).
	// Trims step results before the final answer prompt; MAY be LLM-distilling.
	// If nil and ResultTrim.Enabled, the factory builds the distiller chain
	// (ResultDistill.Enabled + AIClient) or a StructuralTrimmer.
	ResultProcessor ResultProcessor

	// Optional: Custom DETERMINISTIC processor for resolver source data (micro- and
	// contextual re-resolution prompts). Must be LLM-free — those prompts already drive
	// their own resolution LLM call. If nil, defaults to StructuralTrimmer. (Phase 8)
	SourceResultProcessor ResultProcessor

	// Optional: Custom transform for tool/agent INPUT parameters before dispatch (Layer 3).
	// Operates on the resolved param map — redact, validate, enrich, or trim. If nil, defaults
	// to identity (fidelity-first: the downstream tool receives the full upstream output). The
	// built-in byte-budget guard is opt-in (set ResultTrim.MaxAgentInputBytes > 0). (Phase 8)
	AgentInputProcessor AgentInputProcessor

	// Optional: Custom CONTINUATION distiller (Layer 3, Phase 14). Summarizes a non-JSON completed-step
	// blob for the continuation (next-phase) planning prompt — distinct from the SYNTHESIS ResultProcessor
	// (Seam 1) and the resolution-source processor (Seam 2). A custom override is always honored; if nil,
	// the factory builds the default LLM distiller when ResultDistill.Enabled and an AIClient is present,
	// otherwise C is disabled and the continuation builder's deterministic digest/floor path is used.
	ContinuationDistiller ResultProcessor

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

// CreateOrchestrator is the compatibility constructor. A nil config preserves
// the legacy environment-enabled default layer; a non-nil config remains an
// explicit application-owned value. Legacy capability and telemetry bootstrap
// adapters are confined to this wrapper family.
func CreateOrchestrator(config *OrchestratorConfig, deps OrchestratorDependencies) (*AIOrchestrator, error) {
	validationStarted := time.Now()
	diagnostics := []ConfigDiagnostic(nil)
	if config == nil {
		resolved := DefaultConfigWithDiagnostics()
		config = resolved.Config
		diagnostics = resolved.Diagnostics
	} else {
		config = cloneOrchestratorConfig(config)
		normalizeOrchestratorConfig(config)
		if config.SynthesisStrategy == "" {
			config.SynthesisStrategy = StrategyLLM
		}
		if err := validateSynthesisStrategy(config.SynthesisStrategy); err != nil {
			recordSkillConfigValidation(deps.Telemetry, config, "startup", time.Since(validationStarted), err)
			return nil, err
		}
		if err := validatePromptInvariantConfig(config); err != nil {
			recordSkillConfigValidation(deps.Telemetry, config, "startup", time.Since(validationStarted), err)
			return nil, err
		}
		if err := validateSkillRuntimeConfig(config); err != nil {
			recordSkillConfigValidation(deps.Telemetry, config, "startup", time.Since(validationStarted), err)
			return nil, err
		}
	}
	recordSkillConfigValidation(deps.Telemetry, config, "startup", time.Since(validationStarted), nil)
	return createOrchestrator(config, deps, true, diagnostics)
}

// CreateResolvedOrchestrator constructs from a complete code-owned config. It
// performs no environment reads and never initializes telemetry.
func CreateResolvedOrchestrator(
	config *OrchestratorConfig,
	deps OrchestratorDependencies,
) (*AIOrchestrator, error) {
	validationStarted := time.Now()
	if config == nil {
		return nil, fmt.Errorf("%w: config is nil", ErrInvalidOrchestratorConfig)
	}
	resolved := cloneOrchestratorConfig(config)
	normalizeOrchestratorConfig(resolved)
	if err := ValidateOrchestratorConfig(resolved); err != nil {
		recordSkillConfigValidation(deps.Telemetry, resolved, "startup", time.Since(validationStarted), err)
		return nil, err
	}
	recordSkillConfigValidation(deps.Telemetry, resolved, "startup", time.Since(validationStarted), nil)
	return createOrchestrator(resolved, deps, false, nil)
}

// CreateOrchestratorFromEnvironment resolves all documented orchestration and
// prompt variables strictly, applies code options last, and then uses the
// environment-free canonical constructor.
func CreateOrchestratorFromEnvironment(
	deps OrchestratorDependencies,
	options ...OrchestratorOption,
) (*AIOrchestrator, error) {
	return createOrchestratorFromEnvironment(deps, os.LookupEnv, options...)
}

// createOrchestratorFromEnvironment keeps environment resolution deterministic
// and unit-testable without changing the public constructor contract.
func createOrchestratorFromEnvironment(
	deps OrchestratorDependencies,
	lookup func(string) (string, bool),
	options ...OrchestratorOption,
) (*AIOrchestrator, error) {
	validationStarted := time.Now()
	resolved, err := ResolveOrchestratorConfig(ConfigResolution{
		Environment: EnvironmentStrict,
		LookupEnv:   lookup,
		Options:     options,
	})
	if err != nil {
		recordSkillConfigResolutionFailure(deps.Telemetry, err, time.Since(validationStarted))
		return nil, err
	}
	return CreateResolvedOrchestrator(resolved.Config, deps)
}

func createOrchestrator(
	config *OrchestratorConfig,
	deps OrchestratorDependencies,
	compatibilityBootstrap bool,
	diagnostics []ConfigDiagnostic,
) (*AIOrchestrator, error) {

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
	emitConfigDiagnostics(factoryLogger, diagnostics)

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
	if config.CapabilityProviderType == "service" && config.CapabilityService.Endpoint == "" {
		if compatibilityBootstrap {
			endpoint := os.Getenv("CAPABILITY_SERVICE_URL")
			if endpoint == "" {
				endpoint = os.Getenv("TRUVAG3_CAPABILITY_SERVICE_URL")
			}
			config.CapabilityService.Endpoint = endpoint
		}
		if config.CapabilityService.Endpoint == "" {
			return nil, fmt.Errorf("capability service URL required: set CapabilityService.Endpoint in config or a documented compatibility environment variable")
		}
	}

	// Create orchestrator
	orchestrator := NewAIOrchestrator(config, deps.Discovery, deps.AIClient)
	if config.Skills.Enabled && len(config.Skills.Bindings) > 0 {
		contentCache, err := resolveSkillContentCache(config)
		if err != nil {
			return nil, fmt.Errorf("construct skill content cache: %w", err)
		}
		registry, err := NewImmutableCachedSkillRegistry(config.SkillRegistry, contentCache)
		if err != nil {
			return nil, fmt.Errorf("construct verified skill registry: %w", err)
		}
		runtime, err := newSkillRuntime(config, registry, deps.AIClient)
		if err != nil {
			return nil, fmt.Errorf("construct skill runtime: %w", err)
		}
		orchestrator.skillRuntime = runtime
		runtime.debugRecorder = func(ctx context.Context, interaction LLMInteraction) {
			orchestrator.recordDebugInteraction(ctx, GetRequestID(ctx), interaction)
		}
	}

	if deps.ConversationHistoryPreparer == nil {
		preparer, err := BuildConversationHistoryProcessor(config)
		if err != nil {
			return nil, fmt.Errorf("build conversation history processor: %w", err)
		}
		deps.ConversationHistoryPreparer = preparer
	}
	orchestrator.SetConversationHistoryPreparer(deps.ConversationHistoryPreparer)

	// Resolve telemetry without ever leaving the dependency nil. Canonical
	// construction is application-owned and never initializes a provider.
	if deps.Telemetry != nil {
		orchestrator.SetTelemetry(deps.Telemetry)
	} else if compatibilityBootstrap {
		if globalProvider := telemetry.GetTelemetryProvider(); globalProvider != nil {
			orchestrator.SetTelemetry(globalProvider)
		} else if config.EnableTelemetry {
			endpoint := os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT")
			if endpoint == "" {
				endpoint = os.Getenv("TRUVAG3_TELEMETRY_ENDPOINT")
			}
			if endpoint != "" {
				otelProvider, err := telemetry.NewOTelProvider("orchestrator", "orchestrator", endpoint)
				if err != nil {
					emitConstructionDiagnostic(factoryLogger, ConfigDiagnostic{
						Variable: "telemetry",
						Reason:   ConfigReasonTelemetryBootstrapFailed,
						Action:   ConfigActionDefaulted,
					})
				} else {
					orchestrator.SetTelemetry(otelProvider)
				}
			} else {
				emitConstructionDiagnostic(factoryLogger, ConfigDiagnostic{
					Variable: "telemetry",
					Reason:   ConfigReasonTelemetryDependencyMissing,
					Action:   ConfigActionDefaulted,
				})
			}
		}
	} else if config.EnableTelemetry {
		emitConstructionDiagnostic(factoryLogger, ConfigDiagnostic{
			Variable: "telemetry",
			Reason:   ConfigReasonTelemetryDependencyMissing,
			Action:   ConfigActionDefaulted,
		})
	}

	orchestrator.SetLogger(deps.Logger)
	orchestrator.synthesizer.SetStrategy(config.SynthesisStrategy)

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
				"status":        "fallback",
				"error_type":    "preparation",
				"error":         err.Error(),
				"template_file": config.PromptConfig.TemplateFile,
			})
			defaultBuilder, defaultErr := NewDefaultPromptBuilder(&config.PromptConfig)
			if defaultErr != nil {
				factoryLogger.Warn("Failed to create fallback DefaultPromptBuilder", map[string]interface{}{
					"operation":  "prompt_builder_initialization",
					"status":     "error",
					"reason":     "invalid_default_config",
					"error_type": "preparation",
					"error":      defaultErr.Error(),
				})
				return nil, fmt.Errorf("create fallback default prompt builder: %w", defaultErr)
			}
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
		defaultBuilder, err := NewDefaultPromptBuilder(&config.PromptConfig)
		if err != nil {
			factoryLogger.Warn("Failed to create DefaultPromptBuilder", map[string]interface{}{
				"operation":  "prompt_builder_initialization",
				"status":     "error",
				"reason":     "invalid_default_config",
				"error_type": "preparation",
				"error":      err.Error(),
			})
			return nil, fmt.Errorf("create default prompt builder: %w", err)
		}
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
			if !compatibilityBootstrap {
				config.LLMDebugStore = NewNoOpLLMDebugStore()
				factoryLogger.Warn("LLM debug enabled without an injected backend, using NoOp", map[string]interface{}{
					"operation": "llm_debug_store_initialization", "status": "fallback",
					"reason": "backend_dependency_missing",
				})
			} else {
				// Compatibility constructors retain the historical Redis default.
				store, err := NewRedisLLMDebugStore(
					WithDebugRedisDB(config.LLMDebug.RedisDB),
					WithDebugLogger(deps.Logger),
					WithDebugTTL(config.LLMDebug.TTL),
					WithDebugErrorTTL(config.LLMDebug.ErrorTTL),
				)
				if err != nil {
					// Resilient behavior - use NoOp store if Redis unavailable
					factoryLogger.Warn("Failed to initialize Redis LLM debug store, using NoOp", map[string]interface{}{
						"operation":  "llm_debug_store_initialization",
						"status":     "fallback",
						"error_type": "preparation",
						"error":      safeBackendInitializationError(err),
						"hint":       "Set REDIS_URL or TRUVAG3_REDIS_URL, or disable via TRUVAG3_LLM_DEBUG_ENABLED=false",
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
		config.ExecutionStore = normalizeExecutionStoreConfig(config.ExecutionStore)
		if config.ExecutionStoreBackend != nil {
			// Custom backend provided - use it
			orchestrator.SetExecutionStore(config.ExecutionStoreBackend)
			factoryLogger.Info("ExecutionStore configured with custom backend", map[string]interface{}{
				"operation": "execution_store_initialization",
				"ttl":       config.ExecutionStore.TTL.String(),
				"error_ttl": config.ExecutionStore.ErrorTTL.String(),
			})
		} else {
			if !compatibilityBootstrap {
				orchestrator.SetExecutionStore(NewNoOpExecutionStore())
				factoryLogger.Warn("Execution debug enabled without an injected backend, using NoOp", map[string]interface{}{
					"operation": "execution_debug_store_initialization", "status": "fallback",
					"reason": "backend_dependency_missing",
				})
			} else {
				// Compatibility constructors retain the historical Redis default.
				store, err := NewRedisExecutionDebugStoreWithConfig(
					config.ExecutionStore,
					WithExecutionDebugRedisDB(core.RedisDBExecutionDebug),
					WithExecutionDebugLogger(deps.Logger),
				)
				if err != nil {
					// Resilient behavior - use NoOp store if Redis unavailable
					factoryLogger.Warn("Failed to initialize Redis execution debug store, using NoOp", map[string]interface{}{
						"operation":  "execution_debug_store_initialization",
						"status":     "fallback",
						"error_type": "preparation",
						"error":      safeBackendInitializationError(err),
						"hint":       "Set REDIS_URL or TRUVAG3_REDIS_URL, or disable via TRUVAG3_EXECUTION_DEBUG_STORE_ENABLED=false",
					})
					orchestrator.SetExecutionStore(NewNoOpExecutionStore())
				} else {
					orchestrator.SetExecutionStore(store)
					factoryLogger.Info("Redis execution debug store initialized", map[string]interface{}{
						"operation":                     "execution_debug_store_initialization",
						"redis_db":                      core.RedisDBExecutionDebug,
						"key_prefix":                    config.ExecutionStore.KeyPrefix,
						"ttl":                           config.ExecutionStore.TTL.String(),
						"error_ttl":                     config.ExecutionStore.ErrorTTL.String(),
						"conversation_query_limit":      config.ExecutionStore.ConversationQueryLimit,
						"conversation_index_scan_limit": config.ExecutionStore.ConversationIndexScanLimit,
					})
				}
			}
		}
	}

	// Advisory, once per orchestrator, whenever ANY built-in distiller will exist (synthesis
	// and/or continuation — both fan-out surfaces): CompactionDeadline==0 is a DOCUMENTED
	// opt-out, so this is informational, never an error, and it deliberately does NOT live in
	// normalizeResultDistillConfig (which runs per distiller construction and would warn
	// twice). Distinct operation from the normalization warns so alerting on genuine
	// misconfiguration is not paged by a documented opt-out.
	if config.ResultDistill.Enabled && deps.AIClient != nil && config.ResultDistill.CompactionDeadline == 0 {
		factoryLogger.Warn("CompactionDeadline is disabled (0); map-reduce fan-out has no wall-clock bound",
			map[string]interface{}{"operation": "result_distill.config_advisory"})
	}

	// Seam 1 (Phase 8) — SYNTHESIS result processor. Gated by ResultTrim.Enabled (the prompt-trim
	// feature); the synthesizer/streaming path also checks Enabled at use-time, so a synthesis
	// processor is meaningless when trimming is off.
	if config.ResultTrim.Enabled {
		if deps.ResultProcessor != nil {
			orchestrator.SetResultProcessor(deps.ResultProcessor)
			factoryLogger.Info("Result trimming enabled with custom processor", map[string]interface{}{
				"operation": "result_trim_initialization", "processor": "custom",
				"max_result_bytes": config.ResultTrim.MaxResultBytes,
			})
		} else if config.ResultDistill.Enabled && deps.AIClient != nil {
			trimmer := NewStructuralTrimmer(config.ResultTrim.PreserveKeys, deps.Logger)
			// NewLLMDistiller normalizes the config at construction and distillKeySalt normalizes
			// identically before hashing, so cache keys can never diverge from routing behavior.
			distiller := NewLLMDistiller(deps.AIClient, config.ResultDistill, trimmer, deps.Logger)
			distiller.SetAIOptionsOverride(config.ResultDistillAIOptions)
			// Propagate debugStore so distillation LLM calls appear in the LLM Debug tab.
			if orchestrator.debugStore != nil {
				distiller.SetLLMDebugStore(orchestrator.debugStore)
			}
			// The EFFECTIVE (normalized) config: the cache's minBytes gate and the init log must
			// describe what the distiller actually runs, not raw knob values — a hand-built
			// config's zero threshold would otherwise cache every sub-16 KB passthrough while
			// the distiller runs the backfilled gate. One normalize call, no hand-copied
			// backfill rules (nil logger: normalization warns already fired inside
			// NewLLMDistiller above).
			effDistill := normalizeResultDistillConfig(config.ResultDistill, nil)
			// Wrap with a content-addressed cache when one is provided (fail-open: a nil
			// cache returns the bare distiller, so this is a no-op without a cache).
			processor := NewCachingProcessor(distiller, deps.DistillCache, config.ResultDistill.CacheTTL, effDistill.DistillThreshold, distillKeySalt(config.ResultDistill, config.ResultDistillAIOptions, config.ResultTrim.PreserveKeys), deps.Logger)
			orchestrator.SetResultProcessor(processor)
			factoryLogger.Info("Result processing: LLM distillation (two-stage)", map[string]interface{}{
				"operation":                 "result_trim_initialization",
				"distill_threshold":         effDistill.DistillThreshold,
				"prefilter_budget":          effDistill.PreFilterBudget,
				"target_size":               effDistill.TargetSize,
				"mapreduce_threshold_bytes": effDistill.MapReduceThresholdBytes,
				"cache_enabled":             deps.DistillCache != nil,
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

	// Seam 4 (Phase 14) — CONTINUATION distiller (C): summarizes a non-JSON step blob for the continuation
	// planner, kept OFF the synthesis chain (no Phase-8 coupling). A custom override is ALWAYS honored
	// (consistent with Seams 1-3 — never silently dropped, independent of ResultTrim/ResultDistill toggles);
	// otherwise the built-in LLM distiller default is installed only when distillation is enabled with an AI
	// client. No cache wrapper — C fires ~never, so a cache would mostly miss.
	if deps.ContinuationDistiller != nil {
		orchestrator.SetContinuationDistiller(deps.ContinuationDistiller)
		factoryLogger.Info("Continuation distiller (C) enabled with custom processor", map[string]interface{}{
			"operation": "continuation_distiller_initialization",
		})
	} else if config.ResultDistill.Enabled && deps.AIClient != nil {
		contDistiller := NewLLMDistiller(deps.AIClient, config.ResultDistill,
			NewStructuralTrimmer(config.ResultTrim.PreserveKeys, deps.Logger), deps.Logger)
		contDistiller.SetAIOptionsOverride(config.ResultDistillAIOptions)
		if orchestrator.debugStore != nil {
			contDistiller.SetLLMDebugStore(orchestrator.debugStore)
		}
		orchestrator.SetContinuationDistiller(contDistiller)
		factoryLogger.Info("Continuation distiller (C) enabled", map[string]interface{}{
			"operation": "continuation_distiller_initialization",
		})
	}

	// Seams 2 & 3 (Phase 8) are INDEPENDENT data-flow concerns, wired REGARDLESS of
	// ResultTrim.Enabled so a custom override (e.g. a fail-closed PII redactor) is never silently
	// dropped when prompt trimming is off. Only the built-in defaults remain conditional.

	// Seam 2 — RESOLUTION-SOURCE processor (micro- & contextual re-resolution prompts). Deterministic,
	// never the distiller. A custom override is always honored; the built-in StructuralTrimmer default
	// is installed only when result trimming is enabled (preserves the prior default behavior).
	if deps.SourceResultProcessor != nil {
		orchestrator.SetSourceResultProcessor(deps.SourceResultProcessor)
	} else if config.ResultTrim.Enabled {
		orchestrator.SetSourceResultProcessor(NewStructuralTrimmer(config.ResultTrim.PreserveKeys, deps.Logger))
	}

	// Seam 3 — AGENT-INPUT transform (tool→tool data flow). A custom transform is always honored; the
	// built-in byte-budget guard is opt-in via MaxAgentInputBytes > 0 (independent of Enabled).
	// Otherwise identity (fidelity-first: the downstream tool receives the full upstream output).
	switch {
	case deps.AgentInputProcessor != nil:
		orchestrator.SetAgentInputProcessor(deps.AgentInputProcessor)
	case config.ResultTrim.MaxAgentInputBytes > 0:
		orchestrator.SetAgentInputProcessor(NewByteBudgetAgentInputProcessor(
			NewStructuralTrimmer(config.ResultTrim.PreserveKeys, deps.Logger),
			config.ResultTrim.MaxAgentInputBytes, deps.Logger))
	default:
		orchestrator.SetAgentInputProcessor(identityAgentInputProcessor{})
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

func emitConfigDiagnostics(logger core.Logger, diagnostics []ConfigDiagnostic) {
	for _, diagnostic := range diagnostics {
		emitConstructionDiagnostic(logger, diagnostic)
	}
}

func emitConstructionDiagnostic(logger core.Logger, diagnostic ConfigDiagnostic) {
	emitConfigFallbackMetrics([]ConfigDiagnostic{diagnostic})
	if logger == nil {
		return
	}
	logger.Warn("Orchestrator construction used a safe fallback", map[string]interface{}{
		"operation": "orchestrator_construction_fallback",
		"status":    "fallback",
		"variable":  diagnostic.Variable,
		"reason":    string(diagnostic.Reason),
		"action":    string(diagnostic.Action),
	})
}

func safeBackendInitializationError(err error) string {
	if err == nil {
		return ""
	}
	return "backend initialization failed"
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

// WithSkills replaces the complete developer-owned skill configuration.
func WithSkills(config SkillConfig) OrchestratorOption {
	return func(c *OrchestratorConfig) {
		c.Skills = cloneSkillConfig(config)
	}
}

// WithSkillRegistry injects the authoritative provider-neutral runtime read
// dependency. Construction never creates a concrete provider.
func WithSkillRegistry(registry SkillRegistry) OrchestratorOption {
	return func(c *OrchestratorConfig) {
		c.SkillRegistry = registry
	}
}

// WithSkillContentCache injects optional storage for verified exact immutable
// bodies. It never replaces registry authority or integrity verification.
func WithSkillContentCache(cache SkillContentCache) OrchestratorOption {
	return func(c *OrchestratorConfig) {
		c.SkillContentCache = cache
	}
}

func WithSkillActivationAIOptions(options *AIOptionsOverride) OrchestratorOption {
	return func(c *OrchestratorConfig) {
		c.SkillActivationAIOptions = cloneAIOptionsOverride(options)
	}
}

func WithSkillResourceAIOptions(options *AIOptionsOverride) OrchestratorOption {
	return func(c *OrchestratorConfig) {
		c.SkillResourceAIOptions = cloneAIOptionsOverride(options)
	}
}

func WithSkillPromptGuidance(guidance SkillPromptGuidance) OrchestratorOption {
	return func(c *OrchestratorConfig) {
		c.SkillPromptGuidance = guidance
	}
}

// WithSkillActivationPolicy installs an optional deterministic refinement for
// unresolved auto candidates. Runtime validation still owns every decision.
func WithSkillActivationPolicy(policy SkillActivationPolicy) OrchestratorOption {
	return func(c *OrchestratorConfig) {
		c.SkillActivationPolicy = policy
	}
}

// WithSkillResolver replaces only the included activation model task. Binding,
// availability, integrity, and admission remain framework-owned.
func WithSkillResolver(resolver SkillResolver) OrchestratorOption {
	return func(c *OrchestratorConfig) {
		c.SkillResolver = resolver
	}
}

// WithSkillResourceResolver replaces only the included resource-selection
// model task. Runtime eligibility, exact reads, and admission remain fixed.
func WithSkillResourceResolver(resolver SkillResourceResolver) OrchestratorOption {
	return func(c *OrchestratorConfig) {
		c.SkillResourceResolver = resolver
	}
}

// WithSkillTokenCounter installs the advisory runtime counter used for skill
// prompt admission. Invalid counter output degrades to the framework heuristic
// and is recorded as a bounded execution diagnostic.
func WithSkillTokenCounter(counter core.TokenCounter) OrchestratorOption {
	return func(c *OrchestratorConfig) {
		c.SkillTokenCounter = counter
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

// WithStepRetryBackoff configures retry timing without requiring process-wide
// environment variables. Invalid non-positive durations are rejected later by
// canonical configuration validation.
func WithStepRetryBackoff(initialDelay, maxDelay time.Duration) OrchestratorOption {
	return func(c *OrchestratorConfig) {
		c.stepRetryBackoff.InitialDelay = initialDelay
		c.stepRetryBackoff.MaxDelay = maxDelay
	}
}

// WithExecutionStoreWriteTimeout overrides the per-write timeout used by the
// asynchronous execution-debug recorder. Zero selects the framework default;
// negative values are rejected by canonical configuration validation.
func WithExecutionStoreWriteTimeout(timeout time.Duration) OrchestratorOption {
	return func(c *OrchestratorConfig) {
		c.executionStoreWriteTimeout = timeout
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
	validationStarted := time.Now()
	resolved, err := ResolveOrchestratorConfig(ConfigResolution{
		Environment: EnvironmentCompatible,
		Options:     opts,
	})
	if err != nil {
		recordSkillConfigResolutionFailure(deps.Telemetry, err, time.Since(validationStarted))
		return nil, err
	}
	recordSkillConfigValidation(deps.Telemetry, resolved.Config, "startup", time.Since(validationStarted), nil)
	return createOrchestrator(resolved.Config, deps, true, resolved.Diagnostics)
}

// CreateSimpleOrchestrator creates an orchestrator with zero configuration
// This is perfect for developers who just want to get started quickly.
// It will:
// - Use the default capability provider (sends all capabilities to LLM)
// - Work with small to medium deployments (up to ~100 agents/tools)
// - Not require any external services
// - Use NoOpLogger by default (pass Logger in dependencies for logging)
func CreateSimpleOrchestrator(discovery core.Discovery, aiClient core.AIClient) *AIOrchestrator {
	orchestrator, err := CreateSimpleOrchestratorWithError(discovery, aiClient)
	if err != nil {
		failed := NewAIOrchestrator(NewDefaultOrchestratorConfig(), discovery, aiClient)
		failed.SetLogger(nil)
		failed.constructionErr = fmt.Errorf("%w: %v", ErrOrchestratorConstruction, err)
		emitConstructionDiagnostic(failed.logger, ConfigDiagnostic{
			Variable: "orchestrator",
			Reason:   ConfigReasonInvalidConfiguration,
			Action:   ConfigActionDefaulted,
		})
		return failed
	}
	return orchestrator
}

// CreateSimpleOrchestratorWithError is the error-returning compatibility
// convenience for callers that do not want deferred construction errors.
func CreateSimpleOrchestratorWithError(
	discovery core.Discovery,
	aiClient core.AIClient,
) (*AIOrchestrator, error) {
	return CreateOrchestrator(nil, OrchestratorDependencies{
		Discovery: discovery,
		AIClient:  aiClient,
	})
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

// WithResultPreserveKeys sets keys the trimmer should favor keeping during trimming (a scoring preference, not a guarantee).
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
	// NewLLMDistiller normalizes the config at construction and distillKeySalt normalizes
	// identically before hashing, so cache keys can never diverge from routing behavior.
	distiller := NewLLMDistiller(ai, cfg, trimmer, logger)
	// Advisory (matches the factory path; 0 is a documented opt-out, so informational only —
	// distinct operation from normalization warns so misconfiguration alerting is not paged
	// by a documented opt-out).
	if cfg.CompactionDeadline == 0 && logger != nil {
		logger.Warn("CompactionDeadline is disabled (0); map-reduce fan-out has no wall-clock bound",
			map[string]interface{}{"operation": "result_distill.config_advisory"})
	}
	// The cache's minBytes must be the EFFECTIVE (backfilled) threshold, not the raw value: a
	// minimal config's zero threshold would otherwise cache every sub-threshold passthrough
	// while the distiller runs the backfilled 16 KB gate. Derived via the normalizer — never a
	// hand-copied backfill rule that can drift from it.
	// This Layer-2 helper does not apply a per-phase AI options override, so the salt has
	// none. Callers using deps.ResultDistillAIOptions go through the factory path above.
	return NewCachingProcessor(distiller, cache, cfg.CacheTTL, normalizeResultDistillConfig(cfg, nil).DistillThreshold, distillKeySalt(cfg, nil, nil), logger)
}
