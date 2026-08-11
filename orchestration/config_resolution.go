package orchestration

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/truvaagents/truva-g3/core"
	"github.com/truvaagents/truva-g3/telemetry"
)

// EnvironmentMode controls whether and how ResolveOrchestratorConfig reads
// deployment environment variables.
type EnvironmentMode uint8

const (
	EnvironmentDisabled EnvironmentMode = iota
	EnvironmentCompatible
	EnvironmentStrict
)

// ConfigDiagnosticReason is a bounded classification. It deliberately never
// contains the rejected environment value.
type ConfigDiagnosticReason string

const (
	ConfigReasonEmptyValue                 ConfigDiagnosticReason = "empty_value"
	ConfigReasonInvalidBoolean             ConfigDiagnosticReason = "invalid_boolean"
	ConfigReasonInvalidInteger             ConfigDiagnosticReason = "invalid_integer"
	ConfigReasonInvalidFloat               ConfigDiagnosticReason = "invalid_float"
	ConfigReasonInvalidDuration            ConfigDiagnosticReason = "invalid_duration"
	ConfigReasonInvalidEnum                ConfigDiagnosticReason = "invalid_enum"
	ConfigReasonOutOfRange                 ConfigDiagnosticReason = "out_of_range"
	ConfigReasonInvalidJSON                ConfigDiagnosticReason = "invalid_json"
	ConfigReasonInvalidConfiguration       ConfigDiagnosticReason = "invalid_configuration"
	ConfigReasonTelemetryDependencyMissing ConfigDiagnosticReason = "telemetry_dependency_missing"
	ConfigReasonTelemetryBootstrapFailed   ConfigDiagnosticReason = "telemetry_bootstrap_failed"
)

// ConfigFallbackAction describes the bounded fallback taken for a diagnostic.
type ConfigFallbackAction string

const (
	ConfigActionIgnored   ConfigFallbackAction = "ignored"
	ConfigActionCoerced   ConfigFallbackAction = "coerced"
	ConfigActionDefaulted ConfigFallbackAction = "defaulted"
)

// ConfigDiagnostic reports a fallback without retaining raw values, secrets,
// prompt bodies, or provider credentials.
type ConfigDiagnostic struct {
	Variable string
	Reason   ConfigDiagnosticReason
	Action   ConfigFallbackAction
}

// ConfigResolution describes the selected configuration layers.
type ConfigResolution struct {
	Base        *OrchestratorConfig
	Environment EnvironmentMode
	LookupEnv   func(string) (string, bool)
	Options     []OrchestratorOption
}

// ConfigResolutionResult contains an independent validated configuration and
// any bounded compatible-mode fallbacks.
type ConfigResolutionResult struct {
	Config      *OrchestratorConfig
	Diagnostics []ConfigDiagnostic
}

// ConfigEnvironmentError identifies one invalid strict environment setting
// without exposing its raw value.
type ConfigEnvironmentError struct {
	Variable string
	Reason   ConfigDiagnosticReason
}

func (e *ConfigEnvironmentError) Error() string {
	return fmt.Sprintf("invalid environment configuration for %s: %s", e.Variable, e.Reason)
}

var ErrInvalidOrchestratorConfig = errors.New("invalid orchestrator configuration")
var ErrOrchestratorConstruction = errors.New("orchestrator construction failed")

const configFallbackMetric = "orchestration.config.fallback"

// NewDefaultOrchestratorConfig returns deterministic framework defaults and
// never reads process environment.
func NewDefaultOrchestratorConfig() *OrchestratorConfig {
	planMaxTokens := 15000
	synthesisTemp := float32(0.5)
	synthesisMaxTokens := 5000
	microMaxTokens := 2000

	return &OrchestratorConfig{
		RoutingMode:                      ModeAutonomous,
		SynthesisStrategy:                StrategyLLM,
		HistorySize:                      100,
		MetricsEnabled:                   true,
		ConversationTokenBudget:          48000,
		ConversationRecentTurnsPreserved: 4,
		ConversationSummaryCacheSize:     256,
		CacheEnabled:                     true,
		CacheTTL:                         5 * time.Minute,
		ExecutionOptions: ExecutionOptions{
			MaxConcurrency:            25,
			StepTimeout:               120 * time.Second,
			TotalTimeout:              600 * time.Second,
			RetryAttempts:             3,
			RetryDelay:                2 * time.Second,
			CircuitBreaker:            true,
			FailureThreshold:          5,
			RecoveryTimeout:           30 * time.Second,
			ValidationFeedbackEnabled: true,
			MaxValidationRetries:      2,
		},
		CapabilityProviderType:         "default",
		EnableTelemetry:                true,
		EnableFallback:                 true,
		EnableHybridResolution:         true,
		PlanParseRetryEnabled:          true,
		PlanParseMaxRetries:            2,
		PlanMaxTokens:                  planMaxTokens,
		SynthesisMaxTokens:             synthesisMaxTokens,
		SynthesisTemperature:           roundLegacyFloat(float64(synthesisTemp)),
		MicroResolutionMaxTokens:       microMaxTokens,
		legacyAIOptionBridge:           true,
		HallucinationValidationEnabled: true,
		HallucinationRetryEnabled:      true,
		HallucinationMaxRetries:        1,
		EnableTieredResolution:         true,
		TieredResolution: TieredCapabilityConfig{
			MinToolsForTiering: 20,
			SelectionMaxTokens: 2000,
			RetryEnabled:       true,
			MaxRetries:         2,
		},
		IterativePlanning: IterativePlanConfig{
			Enabled:             true,
			MaxPhases:           5,
			MaxTotalSteps:       200,
			PhaseTimeout:        180 * time.Second,
			MaxValidationRounds: defaultMaxValidationRounds,
		},
		SemanticRetry: SemanticRetryConfig{
			Enabled:                   true,
			MaxAttempts:               2,
			TriggerStatusCodes:        []int{400, 422},
			EnableForIndependentSteps: true,
		},
		LLMDebug: DefaultLLMDebugConfig(),
		HITL:     DefaultHITLConfig(),
		ResultTrim: ResultTrimConfig{
			Enabled:                      true,
			MaxResultBytes:               16384,
			MaxTotalPromptBytes:          32768,
			MaxMicroResolutionBytes:      65536,
			MaxAgentInputBytes:           0,
			SchemaGuidedMappingThreshold: 16384,
		},
		ContinuationResultMaxChars:            10000,
		ContinuationResultMaxTotalChars:       32768,
		ContinuationMaxEscalations:            8,
		ContinuationDigestArraySample:         defaultDigestSampleN,
		ContinuationDigestScalarMax:           defaultDigestScalarMax,
		ContinuationDigestMaxKeys:             defaultDigestMaxKeys,
		RemediationFailurePatternMinFailures:  2,
		RemediationFailurePatternSignatureLen: 120,
		RemediationFailurePatternDisplayLen:   80,
		ResultDistill: ResultDistillConfig{
			Enabled:                 true,
			DistillThreshold:        defaultDistillThreshold,
			PreFilterBudget:         defaultPreFilterBudget,
			TargetSize:              4096,
			Model:                   "fast",
			CacheTTL:                5 * time.Minute,
			CompactionDeadline:      45 * time.Second,
			ModelContextTokens:      defaultModelContextTokens,
			MapConcurrency:          8,
			MapReduceThresholdBytes: 0,
		},
		ExecutionStore:             DefaultExecutionStoreConfig(),
		stepRetryBackoff:           core.DefaultBackoffConfig(),
		executionStoreWriteTimeout: defaultExecutionStoreWriteTimeout,
	}
}

// ResolveOrchestratorConfig applies exactly the selected layers in order and
// returns a validated independent configuration.
func ResolveOrchestratorConfig(input ConfigResolution) (*ConfigResolutionResult, error) {
	if input.Environment != EnvironmentDisabled &&
		input.Environment != EnvironmentCompatible &&
		input.Environment != EnvironmentStrict {
		return nil, fmt.Errorf("%w: unknown environment mode", ErrInvalidOrchestratorConfig)
	}
	base := input.Base
	if base == nil {
		base = NewDefaultOrchestratorConfig()
	}
	config := cloneOrchestratorConfig(base)
	lookup := input.LookupEnv
	if lookup == nil {
		lookup = os.LookupEnv
	}

	resolver := configEnvResolver{mode: input.Environment, lookup: lookup}
	if input.Environment != EnvironmentDisabled {
		if err := resolver.apply(config, input.Environment == EnvironmentStrict); err != nil {
			return nil, err
		}
	}
	for _, option := range input.Options {
		if option != nil {
			option(config)
		}
	}
	normalizeOrchestratorConfig(config)
	if err := ValidateOrchestratorConfig(config); err != nil {
		return nil, err
	}
	return &ConfigResolutionResult{
		Config:      cloneOrchestratorConfig(config),
		Diagnostics: append([]ConfigDiagnostic(nil), resolver.diagnostics...),
	}, nil
}

// DefaultConfigWithDiagnostics preserves the legacy environment-enabled helper
// while making every fallback observable and the result total.
func DefaultConfigWithDiagnostics() ConfigResolutionResult {
	return defaultConfigWithDiagnostics(os.LookupEnv)
}

func defaultConfigWithDiagnostics(lookup func(string) (string, bool)) ConfigResolutionResult {
	result, err := ResolveOrchestratorConfig(ConfigResolution{
		Environment: EnvironmentCompatible,
		LookupEnv:   lookup,
	})
	if err == nil {
		return *result
	}

	// Compatible defaults are total. A cross-field failure falls back to pure
	// defaults and reports only a bounded diagnostic.
	defaults := NewDefaultOrchestratorConfig()
	return ConfigResolutionResult{
		Config: defaults,
		Diagnostics: []ConfigDiagnostic{{
			Variable: "orchestrator_config",
			Reason:   ConfigReasonInvalidConfiguration,
			Action:   ConfigActionDefaulted,
		}},
	}
}

// DefaultConfig returns the same total configuration as
// DefaultConfigWithDiagnostics and emits bounded fallback metrics.
func DefaultConfig() *OrchestratorConfig {
	result := DefaultConfigWithDiagnostics()
	emitConfigFallbackMetrics(result.Diagnostics)
	return result.Config
}

func emitConfigFallbackMetrics(diagnostics []ConfigDiagnostic) {
	for _, diagnostic := range diagnostics {
		telemetry.Counter(
			configFallbackMetric,
			"module", telemetry.ModuleOrchestration,
			"variable", diagnostic.Variable,
			"reason", string(diagnostic.Reason),
			"action", string(diagnostic.Action),
		)
	}
}

func normalizeOrchestratorConfig(config *OrchestratorConfig) {
	if config.executionStoreWriteTimeout == 0 {
		config.executionStoreWriteTimeout = defaultExecutionStoreWriteTimeout
	}
	if config.IterativePlanning.Enabled {
		config.PromptConfig.IterativePlanConfig = &config.IterativePlanning
	} else {
		config.PromptConfig.IterativePlanConfig = nil
	}
}

// ValidateOrchestratorConfig validates effective behavior after every selected
// layer has been applied.
func ValidateOrchestratorConfig(config *OrchestratorConfig) error {
	if config == nil {
		return fmt.Errorf("%w: config is nil", ErrInvalidOrchestratorConfig)
	}
	if err := validateSynthesisStrategy(config.SynthesisStrategy); err != nil {
		return err
	}
	if err := validatePromptInvariantConfig(config); err != nil {
		return err
	}
	positive := []struct {
		name  string
		value int
	}{
		{"history_size", config.HistorySize},
		{"execution.max_concurrency", config.ExecutionOptions.MaxConcurrency},
		{"execution.retry_attempts", config.ExecutionOptions.RetryAttempts},
		{"conversation_token_budget", config.ConversationTokenBudget},
		{"conversation_recent_turns_preserved", config.ConversationRecentTurnsPreserved},
		{"conversation_summary_cache_size", config.ConversationSummaryCacheSize},
		{"iterative.max_phases", config.IterativePlanning.MaxPhases},
		{"iterative.max_total_steps", config.IterativePlanning.MaxTotalSteps},
		{"iterative.max_validation_rounds", config.IterativePlanning.MaxValidationRounds},
		{"tiered.min_tools", config.TieredResolution.MinToolsForTiering},
		{"tiered.selection_max_tokens", config.TieredResolution.SelectionMaxTokens},
		{"result_trim.max_result_bytes", config.ResultTrim.MaxResultBytes},
		{"result_trim.max_total_prompt_bytes", config.ResultTrim.MaxTotalPromptBytes},
		{"result_trim.max_micro_resolution_bytes", config.ResultTrim.MaxMicroResolutionBytes},
		{"continuation.result_max_chars", config.ContinuationResultMaxChars},
		{"continuation.result_max_total_chars", config.ContinuationResultMaxTotalChars},
		{"continuation.digest_array_sample", config.ContinuationDigestArraySample},
		{"continuation.digest_scalar_max", config.ContinuationDigestScalarMax},
		{"continuation.digest_max_keys", config.ContinuationDigestMaxKeys},
		{"remediation.min_failures", config.RemediationFailurePatternMinFailures},
		{"remediation.signature_len", config.RemediationFailurePatternSignatureLen},
		{"remediation.display_len", config.RemediationFailurePatternDisplayLen},
	}
	for _, field := range positive {
		if field.value <= 0 {
			return fmt.Errorf("%w: %s must be greater than zero", ErrInvalidOrchestratorConfig, field.name)
		}
	}
	positiveDurations := []struct {
		name  string
		value time.Duration
	}{
		{"execution.step_timeout", config.ExecutionOptions.StepTimeout},
		{"execution.total_timeout", config.ExecutionOptions.TotalTimeout},
		{"iterative.phase_timeout", config.IterativePlanning.PhaseTimeout},
		{"step_retry.initial_delay", config.stepRetryBackoff.InitialDelay},
		{"step_retry.max_delay", config.stepRetryBackoff.MaxDelay},
		{"execution_store.write_timeout", config.executionStoreWriteTimeout},
	}
	for _, field := range positiveDurations {
		if field.value <= 0 {
			return fmt.Errorf("%w: %s must be greater than zero", ErrInvalidOrchestratorConfig, field.name)
		}
	}
	nonNegative := []struct {
		name  string
		value int
	}{
		{"plan_parse_max_retries", config.PlanParseMaxRetries},
		{"hallucination_max_retries", config.HallucinationMaxRetries},
		{"semantic_retry.max_attempts", config.SemanticRetry.MaxAttempts},
		{"tiered.max_retries", config.TieredResolution.MaxRetries},
		{"result_trim.max_agent_input_bytes", config.ResultTrim.MaxAgentInputBytes},
		{"result_trim.schema_mapping_threshold", config.ResultTrim.SchemaGuidedMappingThreshold},
		{"continuation.max_escalations", config.ContinuationMaxEscalations},
		{"result_distill.map_reduce_threshold", config.ResultDistill.MapReduceThresholdBytes},
	}
	for _, field := range nonNegative {
		if field.value < 0 {
			return fmt.Errorf("%w: %s must be non-negative", ErrInvalidOrchestratorConfig, field.name)
		}
	}
	if config.CapabilityProviderType != "default" && config.CapabilityProviderType != "service" {
		return fmt.Errorf("%w: unknown capability provider type", ErrInvalidOrchestratorConfig)
	}
	if config.CapabilityProviderType == "service" && config.CapabilityService.Endpoint == "" {
		return fmt.Errorf("%w: capability service endpoint is required", ErrInvalidOrchestratorConfig)
	}
	if _, err := NewDefaultPromptBuilder(&config.PromptConfig); err != nil {
		return fmt.Errorf("%w: prompt config: %v", ErrInvalidOrchestratorConfig, err)
	}
	return nil
}

func validatePromptInvariantConfig(config *OrchestratorConfig) error {
	if config == nil {
		return fmt.Errorf("%w: config is nil", ErrInvalidOrchestratorConfig)
	}
	if config.PlanAIOptions != nil && config.PlanAIOptions.SystemPrompt != nil &&
		containsRuntimeContextTag(*config.PlanAIOptions.SystemPrompt) {
		return fmt.Errorf("%w: plan AI system prompt contains reserved runtime_context tag", ErrInvalidOrchestratorConfig)
	}
	if containsRuntimeContextTag(config.PromptConfig.SystemInstructions) {
		return fmt.Errorf("%w: prompt system instructions contain reserved runtime_context tag", ErrInvalidOrchestratorConfig)
	}
	return nil
}

func validateSynthesisStrategy(strategy SynthesisStrategy) error {
	switch strategy {
	case StrategyLLM, StrategyTemplate, StrategySimple:
		return nil
	case StrategyCustom:
		return fmt.Errorf("%w: custom synthesis requires an explicit synthesizer contract", ErrInvalidOrchestratorConfig)
	default:
		return fmt.Errorf("%w: unknown synthesis strategy", ErrInvalidOrchestratorConfig)
	}
}

// OrchestratorConfigIdentity contains a sanitized behavior summary and stable
// fingerprint. It excludes prompt bodies, secrets, endpoints, and raw env data.
type OrchestratorConfigIdentity struct {
	Summary     map[string]interface{}
	Fingerprint string
	// CacheEligible is true only when every behavior-affecting value in the
	// projection has a deterministic identity. Callers must not use Fingerprint
	// as a cache dimension unless CacheEligible is true.
	CacheEligible bool
}

func ResolveOrchestratorConfigIdentity(config *OrchestratorConfig) (OrchestratorConfigIdentity, error) {
	if err := ValidateOrchestratorConfig(config); err != nil {
		return OrchestratorConfigIdentity{}, err
	}

	// Start from the exported JSON configuration so new behavior fields enter
	// the identity by default. Remove content-bearing or credential-bearing
	// values before the map can leave this function. Their bounded identities
	// below preserve change detection without exposing the values themselves.
	sanitized := cloneOrchestratorConfig(config)
	sanitized.CapabilityService.Endpoint = ""
	sanitized.PromptConfig = PromptConfig{}
	sanitizeAIOptionsForIdentity(sanitized.PlanAIOptions)
	sanitizeAIOptionsForIdentity(sanitized.SynthesisAIOptions)
	sanitizeAIOptionsForIdentity(sanitized.MicroResolutionAIOptions)
	sanitizeAIOptionsForIdentity(sanitized.TieredSelectionAIOptions)
	sanitizeAIOptionsForIdentity(sanitized.ErrorAnalysisAIOptions)
	sanitizeAIOptionsForIdentity(sanitized.ResultDistillAIOptions)

	encodedConfig, err := json.Marshal(sanitized)
	if err != nil {
		return OrchestratorConfigIdentity{}, fmt.Errorf("encode sanitized config identity: %w", err)
	}
	var summary map[string]interface{}
	if err := json.Unmarshal(encodedConfig, &summary); err != nil {
		return OrchestratorConfigIdentity{}, fmt.Errorf("decode sanitized config identity: %w", err)
	}

	summary["configuration_presence"] = map[string]interface{}{
		"capability_endpoint":        config.CapabilityService.Endpoint != "",
		"oauth_token":                config.OAuthToken != "",
		"propagated_header_count":    len(config.PropagatedHeaders),
		"step_callback":              config.ExecutionOptions.OnStepComplete != nil,
		"llm_debug_store":            config.LLMDebugStore != nil,
		"execution_store_backend":    config.ExecutionStoreBackend != nil,
		"capability_fallback":        config.CapabilityService.FallbackProvider != nil,
		"capability_circuit_breaker": config.CapabilityService.CircuitBreaker != nil,
	}
	summary["private_runtime_policy"] = map[string]interface{}{
		"step_retry_initial_delay_ms": config.stepRetryBackoff.InitialDelay.Milliseconds(),
		"step_retry_max_delay_ms":     config.stepRetryBackoff.MaxDelay.Milliseconds(),
		"execution_store_timeout_ms":  config.executionStoreWriteTimeout.Milliseconds(),
	}
	promptIdentity, promptStable := promptConfigContentIdentity(config.PromptConfig)
	planIdentity, planStable := aiOptionsContentIdentity(config.PlanAIOptions)
	synthesisIdentity, synthesisStable := aiOptionsContentIdentity(config.SynthesisAIOptions)
	microIdentity, microStable := aiOptionsContentIdentity(config.MicroResolutionAIOptions)
	tieredIdentity, tieredStable := aiOptionsContentIdentity(config.TieredSelectionAIOptions)
	errorIdentity, errorStable := aiOptionsContentIdentity(config.ErrorAnalysisAIOptions)
	distillIdentity, distillStable := aiOptionsContentIdentity(config.ResultDistillAIOptions)
	cacheEligible := promptStable && planStable && synthesisStable && microStable &&
		tieredStable && errorStable && distillStable
	summary["content_identity"] = map[string]interface{}{
		"prompt_config":               promptIdentity,
		"plan_ai_options":             planIdentity,
		"synthesis_ai_options":        synthesisIdentity,
		"micro_resolution_ai_options": microIdentity,
		"tiered_selection_ai_options": tieredIdentity,
		"error_analysis_ai_options":   errorIdentity,
		"result_distill_ai_options":   distillIdentity,
	}

	encoded, err := json.Marshal(summary)
	if err != nil {
		return OrchestratorConfigIdentity{}, fmt.Errorf("encode config identity: %w", err)
	}
	identity := OrchestratorConfigIdentity{Summary: summary, CacheEligible: cacheEligible}
	if cacheEligible {
		digest := sha256.Sum256(encoded)
		identity.Fingerprint = hex.EncodeToString(digest[:])
	}
	return identity, nil
}

func promptConfigContentIdentity(config PromptConfig) (string, bool) {
	// IterativePlanConfig is derived from OrchestratorConfig.IterativePlanning
	// during construction. The canonical field is already present in the
	// exported projection, so pointer population must not change the identity.
	config.IterativePlanConfig = nil
	return identityDigest(config)
}

func sanitizeAIOptionsForIdentity(options *AIOptionsOverride) {
	if options == nil {
		return
	}
	options.SystemPrompt = nil
	options.Extra = nil
	options.Headers = nil
}

func aiOptionsContentIdentity(options *AIOptionsOverride) (string, bool) {
	if options == nil {
		return "", true
	}
	content := struct {
		SystemPrompt *string
		Extra        map[string]interface{}
	}{
		SystemPrompt: options.SystemPrompt,
		Extra:        options.Extra,
	}
	return identityDigest(content)
}

func identityDigest(value interface{}) (string, bool) {
	encoded, err := json.Marshal(value)
	if err != nil {
		// A configuration that contains a non-JSON provider extension remains
		// usable; its identity is explicitly unstable rather than leaking the
		// value or failing otherwise valid orchestration construction.
		return "unstable", false
	}
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:]), true
}

func cloneOrchestratorConfig(source *OrchestratorConfig) *OrchestratorConfig {
	if source == nil {
		return nil
	}
	cloned := *source
	cloned.PlanAIOptions = cloneAIOptionsOverride(source.PlanAIOptions)
	cloned.SynthesisAIOptions = cloneAIOptionsOverride(source.SynthesisAIOptions)
	cloned.MicroResolutionAIOptions = cloneAIOptionsOverride(source.MicroResolutionAIOptions)
	cloned.TieredSelectionAIOptions = cloneAIOptionsOverride(source.TieredSelectionAIOptions)
	cloned.ErrorAnalysisAIOptions = cloneAIOptionsOverride(source.ErrorAnalysisAIOptions)
	cloned.ResultDistillAIOptions = cloneAIOptionsOverride(source.ResultDistillAIOptions)
	cloned.SemanticRetry.TriggerStatusCodes = append([]int(nil), source.SemanticRetry.TriggerStatusCodes...)
	cloned.ExcludedCapabilities = append([]string(nil), source.ExcludedCapabilities...)
	cloned.PropagatedHeaders = cloneStringMap(source.PropagatedHeaders)
	cloned.PromptConfig.AdditionalTypeRules = append([]TypeRule(nil), source.PromptConfig.AdditionalTypeRules...)
	for index := range cloned.PromptConfig.AdditionalTypeRules {
		cloned.PromptConfig.AdditionalTypeRules[index].TypeNames = append([]string(nil), source.PromptConfig.AdditionalTypeRules[index].TypeNames...)
	}
	cloned.PromptConfig.CustomInstructions = append([]string(nil), source.PromptConfig.CustomInstructions...)
	if source.PromptConfig.IncludeAntiPatterns != nil {
		value := *source.PromptConfig.IncludeAntiPatterns
		cloned.PromptConfig.IncludeAntiPatterns = &value
	}
	cloned.PromptConfig.IterativePlanConfig = nil
	cloned.HITL.SensitiveCapabilities = append([]string(nil), source.HITL.SensitiveCapabilities...)
	cloned.HITL.SensitiveAgents = append([]string(nil), source.HITL.SensitiveAgents...)
	cloned.HITL.StepSensitiveCapabilities = append([]string(nil), source.HITL.StepSensitiveCapabilities...)
	cloned.HITL.StepSensitiveAgents = append([]string(nil), source.HITL.StepSensitiveAgents...)
	cloned.ResultTrim.PreserveKeys = append([]string(nil), source.ResultTrim.PreserveKeys...)
	if source.IterativePlanning.Enabled {
		cloned.PromptConfig.IterativePlanConfig = &cloned.IterativePlanning
	}
	return &cloned
}

func cloneAIOptionsOverride(source *AIOptionsOverride) *AIOptionsOverride {
	if source == nil {
		return nil
	}
	cloned := *source
	if source.Model != nil {
		value := *source.Model
		cloned.Model = &value
	}
	if source.Temperature != nil {
		value := *source.Temperature
		cloned.Temperature = &value
	}
	if source.MaxTokens != nil {
		value := *source.MaxTokens
		cloned.MaxTokens = &value
	}
	if source.SystemPrompt != nil {
		value := *source.SystemPrompt
		cloned.SystemPrompt = &value
	}
	if source.ReasoningEffort != nil {
		value := *source.ReasoningEffort
		cloned.ReasoningEffort = &value
	}
	if source.ResponseFormat != nil {
		value := *source.ResponseFormat
		cloned.ResponseFormat = &value
	}
	if source.Extra != nil {
		cloned.Extra = make(map[string]interface{}, len(source.Extra))
		for key, value := range source.Extra {
			cloned.Extra[key] = value
		}
	}
	cloned.Headers = cloneStringMap(source.Headers)
	return &cloned
}

type configEnvResolver struct {
	mode        EnvironmentMode
	lookup      func(string) (string, bool)
	diagnostics []ConfigDiagnostic
}

func (r *configEnvResolver) diagnostic(variable string, reason ConfigDiagnosticReason, action ConfigFallbackAction) {
	r.diagnostics = append(r.diagnostics, ConfigDiagnostic{Variable: variable, Reason: reason, Action: action})
}

func (r *configEnvResolver) invalid(
	variable string,
	reason ConfigDiagnosticReason,
	action ConfigFallbackAction,
) error {
	if r.mode == EnvironmentStrict {
		return &ConfigEnvironmentError{Variable: variable, Reason: reason}
	}
	r.diagnostic(variable, reason, action)
	return nil
}

func (r *configEnvResolver) raw(variable string) (string, bool, error) {
	value, present := r.lookup(variable)
	if !present {
		return "", false, nil
	}
	if value != "" {
		return value, true, nil
	}
	if err := r.invalid(variable, ConfigReasonEmptyValue, ConfigActionIgnored); err != nil {
		return "", false, err
	}
	return "", false, nil
}

func (r *configEnvResolver) text(variable string) (string, bool, error) {
	return r.raw(variable)
}

func (r *configEnvResolver) boolean(variable string) (bool, bool, error) {
	raw, present, err := r.raw(variable)
	if err != nil || !present {
		return false, false, err
	}
	switch strings.ToLower(raw) {
	case "true":
		return true, true, nil
	case "false":
		return false, true, nil
	default:
		if err := r.invalid(variable, ConfigReasonInvalidBoolean, ConfigActionCoerced); err != nil {
			return false, false, err
		}
		// Preserve the legacy strings.ToLower(value) == "true" coercion.
		return false, true, nil
	}
}

func (r *configEnvResolver) integer(
	variable string,
	minimum int,
) (int, bool, error) {
	raw, present, err := r.raw(variable)
	if err != nil || !present {
		return 0, false, err
	}
	value, parseErr := strconv.Atoi(raw)
	if parseErr != nil {
		if err := r.invalid(variable, ConfigReasonInvalidInteger, ConfigActionDefaulted); err != nil {
			return 0, false, err
		}
		return 0, false, nil
	}
	if value < minimum {
		if err := r.invalid(variable, ConfigReasonOutOfRange, ConfigActionDefaulted); err != nil {
			return 0, false, err
		}
		return 0, false, nil
	}
	return value, true, nil
}

func (r *configEnvResolver) duration(
	variable string,
	positive bool,
) (time.Duration, bool, error) {
	raw, present, err := r.raw(variable)
	if err != nil || !present {
		return 0, false, err
	}
	value, parseErr := time.ParseDuration(raw)
	if parseErr != nil {
		if err := r.invalid(variable, ConfigReasonInvalidDuration, ConfigActionDefaulted); err != nil {
			return 0, false, err
		}
		return 0, false, nil
	}
	if positive && value <= 0 {
		if err := r.invalid(variable, ConfigReasonOutOfRange, ConfigActionDefaulted); err != nil {
			return 0, false, err
		}
		return 0, false, nil
	}
	return value, true, nil
}

func (r *configEnvResolver) floatRange(
	variable string,
	minimum float64,
	maximum float64,
) (float64, bool, error) {
	raw, present, err := r.raw(variable)
	if err != nil || !present {
		return 0, false, err
	}
	value, parseErr := strconv.ParseFloat(raw, 32)
	if parseErr != nil {
		if err := r.invalid(variable, ConfigReasonInvalidFloat, ConfigActionDefaulted); err != nil {
			return 0, false, err
		}
		return 0, false, nil
	}
	if math.IsNaN(value) || math.IsInf(value, 0) || value < minimum || value > maximum {
		if err := r.invalid(variable, ConfigReasonOutOfRange, ConfigActionDefaulted); err != nil {
			return 0, false, err
		}
		return 0, false, nil
	}
	return value, true, nil
}

func (r *configEnvResolver) aliasedText(
	standard string,
	framework string,
) (string, bool, error) {
	if _, present := r.lookup(standard); present {
		return r.text(standard)
	}
	return r.text(framework)
}

func (r *configEnvResolver) apply(config *OrchestratorConfig, includePrompt bool) error {
	setPositiveInt := func(variable string, target *int) error {
		value, set, err := r.integer(variable, 1)
		if err != nil {
			return err
		}
		if set {
			*target = value
		}
		return nil
	}
	setNonNegativeInt := func(variable string, target *int) error {
		value, set, err := r.integer(variable, 0)
		if err != nil {
			return err
		}
		if set {
			*target = value
		}
		return nil
	}
	setPositiveDuration := func(variable string, target *time.Duration) error {
		value, set, err := r.duration(variable, true)
		if err != nil {
			return err
		}
		if set {
			*target = value
		}
		return nil
	}
	setBoolean := func(variable string, target *bool) error {
		value, set, err := r.boolean(variable)
		if err != nil {
			return err
		}
		if set {
			*target = value
		}
		return nil
	}
	setText := func(variable string, target *string) error {
		value, set, err := r.text(variable)
		if err != nil {
			return err
		}
		if set {
			*target = value
		}
		return nil
	}

	for _, setting := range []struct {
		variable string
		target   *int
	}{
		{"TRUVAG3_EXECUTION_MAX_CONCURRENCY", &config.ExecutionOptions.MaxConcurrency},
		{"TRUVAG3_STEP_RETRY_MAX_ATTEMPTS", &config.ExecutionOptions.RetryAttempts},
		{"TRUVAG3_CONVERSATION_TOKEN_BUDGET", &config.ConversationTokenBudget},
		{"TRUVAG3_CONVERSATION_RECENT_TURNS_PRESERVED", &config.ConversationRecentTurnsPreserved},
		{"TRUVAG3_CONVERSATION_SUMMARY_CACHE_SIZE", &config.ConversationSummaryCacheSize},
	} {
		if err := setPositiveInt(setting.variable, setting.target); err != nil {
			return err
		}
	}
	if err := setPositiveDuration("TRUVAG3_EXECUTION_STEP_TIMEOUT", &config.ExecutionOptions.StepTimeout); err != nil {
		return err
	}
	if err := setPositiveDuration("TRUVAG3_ORCHESTRATION_TIMEOUT", &config.ExecutionOptions.TotalTimeout); err != nil {
		return err
	}
	if err := setPositiveDuration("TRUVAG3_STEP_RETRY_INITIAL_DELAY", &config.stepRetryBackoff.InitialDelay); err != nil {
		return err
	}
	if err := setPositiveDuration("TRUVAG3_STEP_RETRY_MAX_DELAY", &config.stepRetryBackoff.MaxDelay); err != nil {
		return err
	}
	if err := setPositiveDuration("TRUVAG3_EXECUTION_STORE_WRITE_TIMEOUT", &config.executionStoreWriteTimeout); err != nil {
		return err
	}

	serviceURL, set, err := r.aliasedText("CAPABILITY_SERVICE_URL", "TRUVAG3_CAPABILITY_SERVICE_URL")
	if err != nil {
		return err
	}
	if set {
		config.CapabilityProviderType = "service"
		config.CapabilityService.Endpoint = serviceURL
	}

	for _, setting := range []struct {
		variable string
		target   *bool
	}{
		{"TRUVAG3_PLAN_RETRY_ENABLED", &config.PlanParseRetryEnabled},
		{"TRUVAG3_PLAN_REFINEMENT_ENABLED", &config.PlanRefinementEnabled},
		{"TRUVAG3_HALLUCINATION_VALIDATION_ENABLED", &config.HallucinationValidationEnabled},
		{"TRUVAG3_HALLUCINATION_RETRY_ENABLED", &config.HallucinationRetryEnabled},
		{"TRUVAG3_SEMANTIC_RETRY_ENABLED", &config.SemanticRetry.Enabled},
		{"TRUVAG3_SEMANTIC_RETRY_INDEPENDENT_STEPS", &config.SemanticRetry.EnableForIndependentSteps},
		{"TRUVAG3_TIERED_RESOLUTION_ENABLED", &config.EnableTieredResolution},
		{"TRUVAG3_TIERED_SELECTION_RETRY_ENABLED", &config.TieredResolution.RetryEnabled},
		{"TRUVAG3_ITERATIVE_PLANNING_ENABLED", &config.IterativePlanning.Enabled},
		{"TRUVAG3_LLM_DEBUG_ENABLED", &config.LLMDebug.Enabled},
		{"TRUVAG3_HITL_ENABLED", &config.HITL.Enabled},
		{"TRUVAG3_HITL_REQUIRE_PLAN_APPROVAL", &config.HITL.RequirePlanApproval},
		{"TRUVAG3_RESULT_TRIM_ENABLED", &config.ResultTrim.Enabled},
		{"TRUVAG3_RESULT_DISTILL_ENABLED", &config.ResultDistill.Enabled},
		{"TRUVAG3_EXECUTION_DEBUG_STORE_ENABLED", &config.ExecutionStore.Enabled},
	} {
		if err := setBoolean(setting.variable, setting.target); err != nil {
			return err
		}
	}

	for _, setting := range []struct {
		variable string
		target   *int
	}{
		{"TRUVAG3_PLAN_RETRY_MAX", &config.PlanParseMaxRetries},
		{"TRUVAG3_HALLUCINATION_MAX_RETRIES", &config.HallucinationMaxRetries},
		{"TRUVAG3_SEMANTIC_RETRY_MAX_ATTEMPTS", &config.SemanticRetry.MaxAttempts},
		{"TRUVAG3_TIERED_SELECTION_RETRY_MAX", &config.TieredResolution.MaxRetries},
		{"TRUVAG3_LLM_DEBUG_REDIS_DB", &config.LLMDebug.RedisDB},
		{"TRUVAG3_HITL_ESCALATE_AFTER_RETRIES", &config.HITL.EscalateAfterRetries},
		{"TRUVAG3_RESULT_TRIM_SCHEMA_MAPPING_THRESHOLD", &config.ResultTrim.SchemaGuidedMappingThreshold},
		{"TRUVAG3_CONTINUATION_MAX_ESCALATIONS", &config.ContinuationMaxEscalations},
		{"TRUVAG3_RESULT_DISTILL_MAPREDUCE_THRESHOLD", &config.ResultDistill.MapReduceThresholdBytes},
	} {
		if err := setNonNegativeInt(setting.variable, setting.target); err != nil {
			return err
		}
	}

	for _, setting := range []struct {
		variable string
		target   *int
	}{
		{"TRUVAG3_TIERED_MIN_TOOLS", &config.TieredResolution.MinToolsForTiering},
		{"TRUVAG3_TIERED_SELECTION_MAX_TOKENS", &config.TieredResolution.SelectionMaxTokens},
		{"TRUVAG3_ITERATIVE_MAX_PHASES", &config.IterativePlanning.MaxPhases},
		{"TRUVAG3_ITERATIVE_MAX_TOTAL_STEPS", &config.IterativePlanning.MaxTotalSteps},
		{"TRUVAG3_ITERATIVE_MAX_VALIDATION_ROUNDS", &config.IterativePlanning.MaxValidationRounds},
		{"TRUVAG3_RESULT_TRIM_MAX_BYTES", &config.ResultTrim.MaxResultBytes},
		{"TRUVAG3_RESULT_TRIM_MAX_TOTAL_BYTES", &config.ResultTrim.MaxTotalPromptBytes},
		{"TRUVAG3_RESULT_TRIM_MAX_MICRO_BYTES", &config.ResultTrim.MaxMicroResolutionBytes},
		{"TRUVAG3_CONTINUATION_RESULT_MAX_CHARS", &config.ContinuationResultMaxChars},
		{"TRUVAG3_CONTINUATION_RESULT_MAX_TOTAL_CHARS", &config.ContinuationResultMaxTotalChars},
		{"TRUVAG3_CONTINUATION_DIGEST_ARRAY_SAMPLE", &config.ContinuationDigestArraySample},
		{"TRUVAG3_CONTINUATION_DIGEST_SCALAR_MAX", &config.ContinuationDigestScalarMax},
		{"TRUVAG3_CONTINUATION_DIGEST_MAX_KEYS", &config.ContinuationDigestMaxKeys},
		{"TRUVAG3_FAILURE_PATTERN_MIN_FAILURES", &config.RemediationFailurePatternMinFailures},
		{"TRUVAG3_FAILURE_PATTERN_SIGNATURE_LEN", &config.RemediationFailurePatternSignatureLen},
		{"TRUVAG3_FAILURE_PATTERN_DISPLAY_LEN", &config.RemediationFailurePatternDisplayLen},
		{"TRUVAG3_RESULT_DISTILL_THRESHOLD", &config.ResultDistill.DistillThreshold},
		{"TRUVAG3_RESULT_DISTILL_PREFILTER", &config.ResultDistill.PreFilterBudget},
		{"TRUVAG3_RESULT_DISTILL_TARGET", &config.ResultDistill.TargetSize},
		{"TRUVAG3_RESULT_DISTILL_CONTEXT_TOKENS", &config.ResultDistill.ModelContextTokens},
		{"TRUVAG3_RESULT_DISTILL_MAP_CONCURRENCY", &config.ResultDistill.MapConcurrency},
		{"TRUVAG3_EXECUTION_DEBUG_CONVERSATION_QUERY_LIMIT", &config.ExecutionStore.ConversationQueryLimit},
		{"TRUVAG3_EXECUTION_DEBUG_INDEX_SCAN_LIMIT", &config.ExecutionStore.ConversationIndexScanLimit},
	} {
		if err := setPositiveInt(setting.variable, setting.target); err != nil {
			return err
		}
	}
	for _, setting := range []struct {
		variable string
		target   *int
		options  **AIOptionsOverride
	}{
		{"TRUVAG3_PLAN_MAX_TOKENS", &config.PlanMaxTokens, &config.PlanAIOptions},
		{"TRUVAG3_SYNTHESIS_MAX_TOKENS", &config.SynthesisMaxTokens, &config.SynthesisAIOptions},
		{"TRUVAG3_MICRO_RESOLUTION_MAX_TOKENS", &config.MicroResolutionMaxTokens, &config.MicroResolutionAIOptions},
	} {
		value, set, err := r.integer(setting.variable, 1)
		if err != nil {
			return err
		}
		if set {
			*setting.target = value
			*setting.options = ensureAIOptionsOverride(*setting.options)
			(*setting.options).MaxTokens = IntPtr(value)
		}
	}
	// Fidelity-first: zero disables the optional agent-input byte guard.
	if err := setNonNegativeInt("TRUVAG3_RESULT_TRIM_MAX_AGENT_INPUT_BYTES", &config.ResultTrim.MaxAgentInputBytes); err != nil {
		return err
	}

	if err := setPositiveDuration("TRUVAG3_ITERATIVE_PHASE_TIMEOUT", &config.IterativePlanning.PhaseTimeout); err != nil {
		return err
	}
	for _, setting := range []struct {
		variable string
		target   *time.Duration
	}{
		{"TRUVAG3_LLM_DEBUG_TTL", &config.LLMDebug.TTL},
		{"TRUVAG3_LLM_DEBUG_ERROR_TTL", &config.LLMDebug.ErrorTTL},
		{"TRUVAG3_HITL_DEFAULT_TIMEOUT", &config.HITL.DefaultTimeout},
		{"TRUVAG3_RESULT_DISTILL_CACHE_TTL", &config.ResultDistill.CacheTTL},
		{"TRUVAG3_RESULT_DISTILL_DEADLINE", &config.ResultDistill.CompactionDeadline},
		{"TRUVAG3_EXECUTION_DEBUG_TTL", &config.ExecutionStore.TTL},
		{"TRUVAG3_EXECUTION_DEBUG_ERROR_TTL", &config.ExecutionStore.ErrorTTL},
	} {
		if err := setPositiveDuration(setting.variable, setting.target); err != nil {
			return err
		}
	}

	if value, set, err := r.floatRange("TRUVAG3_SYNTHESIS_TEMPERATURE", 0, 2); err != nil {
		return err
	} else if set {
		config.SynthesisTemperature = roundLegacyFloat(value)
		config.SynthesisAIOptions = ensureAIOptionsOverride(config.SynthesisAIOptions)
		config.SynthesisAIOptions.Temperature = Float32Ptr(float32(value))
	}

	for _, setting := range []struct {
		variable string
		target   *string
	}{
		{"TRUVAG3_PLAN_MODEL", &config.PlanModel},
		{"TRUVAG3_SYNTHESIS_MODEL", &config.SynthesisModel},
		{"TRUVAG3_MICRO_RESOLUTION_MODEL", &config.MicroResolutionModel},
		{"TRUVAG3_RESULT_DISTILL_MODEL", &config.ResultDistill.Model},
		{"TRUVAG3_EXECUTION_DEBUG_KEY_PREFIX", &config.ExecutionStore.KeyPrefix},
		{"TRUVAG3_AGENT_NAME", &config.Name},
		{"TRUVAG3_OAUTH_TOKEN", &config.OAuthToken},
	} {
		if err := setText(setting.variable, setting.target); err != nil {
			return err
		}
	}
	if config.PlanModel != "" {
		if _, present := r.lookup("TRUVAG3_PLAN_MODEL"); present {
			config.PlanAIOptions = ensureAIOptionsOverride(config.PlanAIOptions)
			config.PlanAIOptions.Model = StringPtr(config.PlanModel)
		}
	}
	if config.SynthesisModel != "" {
		if _, present := r.lookup("TRUVAG3_SYNTHESIS_MODEL"); present {
			config.SynthesisAIOptions = ensureAIOptionsOverride(config.SynthesisAIOptions)
			config.SynthesisAIOptions.Model = StringPtr(config.SynthesisModel)
		}
	}
	if config.MicroResolutionModel != "" {
		if _, present := r.lookup("TRUVAG3_MICRO_RESOLUTION_MODEL"); present {
			config.MicroResolutionAIOptions = ensureAIOptionsOverride(config.MicroResolutionAIOptions)
			config.MicroResolutionAIOptions.Model = StringPtr(config.MicroResolutionModel)
		}
	}

	if value, set, err := r.text("TRUVAG3_HITL_DEFAULT_ACTION"); err != nil {
		return err
	} else if set {
		switch strings.ToLower(value) {
		case "approve":
			config.HITL.DefaultAction = CommandApprove
		case "reject":
			config.HITL.DefaultAction = CommandReject
		case "abort":
			config.HITL.DefaultAction = CommandAbort
		default:
			if err := r.invalid("TRUVAG3_HITL_DEFAULT_ACTION", ConfigReasonInvalidEnum, ConfigActionDefaulted); err != nil {
				return err
			}
		}
	}

	for _, setting := range []struct {
		variable string
		target   *[]string
		trim     bool
	}{
		{"TRUVAG3_HITL_SENSITIVE_CAPABILITIES", &config.HITL.SensitiveCapabilities, false},
		{"TRUVAG3_HITL_SENSITIVE_AGENTS", &config.HITL.SensitiveAgents, false},
		{"TRUVAG3_HITL_STEP_SENSITIVE_CAPABILITIES", &config.HITL.StepSensitiveCapabilities, false},
		{"TRUVAG3_HITL_STEP_SENSITIVE_AGENTS", &config.HITL.StepSensitiveAgents, false},
		{"TRUVAG3_EXCLUDED_CAPABILITIES", &config.ExcludedCapabilities, true},
	} {
		value, set, err := r.text(setting.variable)
		if err != nil {
			return err
		}
		if !set {
			continue
		}
		parts := strings.Split(value, ",")
		if setting.trim {
			filtered := make([]string, 0, len(parts))
			for _, part := range parts {
				if trimmed := strings.TrimSpace(part); trimmed != "" {
					filtered = append(filtered, trimmed)
				}
			}
			parts = filtered
		}
		*setting.target = parts
	}

	if includePrompt {
		if err := applyPromptEnvironment(&config.PromptConfig, r, false); err != nil {
			return err
		}
	}
	return nil
}

func ensureAIOptionsOverride(options *AIOptionsOverride) *AIOptionsOverride {
	if options == nil {
		return &AIOptionsOverride{}
	}
	return options
}

func applyPromptEnvironment(
	config *PromptConfig,
	resolver *configEnvResolver,
	appendValues bool,
) error {
	if value, set, err := resolver.text("TRUVAG3_PROMPT_TEMPLATE_FILE"); err != nil {
		return err
	} else if set {
		config.TemplateFile = value
	}
	if value, set, err := resolver.text("TRUVAG3_PROMPT_DOMAIN"); err != nil {
		return err
	} else if set {
		config.Domain = value
	}

	if value, set, err := resolver.text("TRUVAG3_PROMPT_TYPE_RULES"); err != nil {
		return err
	} else if set {
		var rules []TypeRule
		if decodeErr := json.Unmarshal([]byte(value), &rules); decodeErr != nil {
			if resolver.mode == EnvironmentStrict || appendValues {
				return &ConfigEnvironmentError{Variable: "TRUVAG3_PROMPT_TYPE_RULES", Reason: ConfigReasonInvalidJSON}
			}
			resolver.diagnostic("TRUVAG3_PROMPT_TYPE_RULES", ConfigReasonInvalidJSON, ConfigActionDefaulted)
		} else {
			for _, rule := range rules {
				if validateErr := ValidateTypeRule(rule); validateErr != nil {
					if resolver.mode == EnvironmentStrict || appendValues {
						return &ConfigEnvironmentError{Variable: "TRUVAG3_PROMPT_TYPE_RULES", Reason: ConfigReasonInvalidConfiguration}
					}
					resolver.diagnostic("TRUVAG3_PROMPT_TYPE_RULES", ConfigReasonInvalidConfiguration, ConfigActionDefaulted)
					rules = nil
					break
				}
			}
			if appendValues {
				config.AdditionalTypeRules = append(config.AdditionalTypeRules, rules...)
			} else if rules != nil {
				config.AdditionalTypeRules = rules
			}
		}
	}

	if value, set, err := resolver.text("TRUVAG3_PROMPT_CUSTOM_INSTRUCTIONS"); err != nil {
		return err
	} else if set {
		var instructions []string
		if decodeErr := json.Unmarshal([]byte(value), &instructions); decodeErr != nil {
			if resolver.mode == EnvironmentStrict || appendValues {
				return &ConfigEnvironmentError{Variable: "TRUVAG3_PROMPT_CUSTOM_INSTRUCTIONS", Reason: ConfigReasonInvalidJSON}
			}
			resolver.diagnostic("TRUVAG3_PROMPT_CUSTOM_INSTRUCTIONS", ConfigReasonInvalidJSON, ConfigActionDefaulted)
		} else if appendValues {
			config.CustomInstructions = append(config.CustomInstructions, instructions...)
		} else {
			config.CustomInstructions = instructions
		}
	}
	return nil
}

// applyMapReduceThresholdEnv is retained for package compatibility and tests;
// it delegates to the same typed environment parser used by the resolver.
func applyMapReduceThresholdEnv(current int) int {
	config := NewDefaultOrchestratorConfig()
	config.ResultDistill.MapReduceThresholdBytes = current
	resolver := configEnvResolver{mode: EnvironmentCompatible, lookup: os.LookupEnv}
	if err := resolver.apply(config, false); err != nil {
		return current
	}
	return config.ResultDistill.MapReduceThresholdBytes
}
