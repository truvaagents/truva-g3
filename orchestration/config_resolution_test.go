package orchestration

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/truvaagents/truva-g3/core"
)

func lookupFromMap(values map[string]string) func(string) (string, bool) {
	return func(key string) (string, bool) {
		value, present := values[key]
		return value, present
	}
}

func TestNewDefaultOrchestratorConfig_IsPureAndIndependent(t *testing.T) {
	t.Setenv("TRUVAG3_EXECUTION_MAX_CONCURRENCY", "99")
	t.Setenv("TRUVAG3_PLAN_MODEL", "environment-model")

	first := NewDefaultOrchestratorConfig()
	second := NewDefaultOrchestratorConfig()
	if first.ExecutionOptions.MaxConcurrency != 25 || first.PlanModel != "" || first.PlanAIOptions != nil {
		t.Fatalf("pure defaults were environment-dependent: %+v", first)
	}

	first.SemanticRetry.TriggerStatusCodes[0] = 999
	first.ResultTrim.PreserveKeys = append(first.ResultTrim.PreserveKeys, "mutated")
	if second.SemanticRetry.TriggerStatusCodes[0] != 400 || len(second.ResultTrim.PreserveKeys) != 0 {
		t.Fatal("default configurations share mutable state")
	}
}

func TestResolveOrchestratorConfig_LayerPrecedenceAndIndependence(t *testing.T) {
	base := NewDefaultOrchestratorConfig()
	base.PropagatedHeaders = map[string]string{"X-Test": "base"}
	base.ExcludedCapabilities = []string{"base-capability"}

	resolved, err := ResolveOrchestratorConfig(ConfigResolution{
		Base:        base,
		Environment: EnvironmentCompatible,
		LookupEnv: lookupFromMap(map[string]string{
			"TRUVAG3_EXECUTION_MAX_CONCURRENCY": "6",
			"TRUVAG3_EXCLUDED_CAPABILITIES":     "env-one, env-two",
		}),
		Options: []OrchestratorOption{WithMaxConcurrency(11)},
	})
	if err != nil {
		t.Fatalf("ResolveOrchestratorConfig() error = %v", err)
	}
	if resolved.Config.ExecutionOptions.MaxConcurrency != 11 {
		t.Fatalf("max concurrency = %d, want code option 11", resolved.Config.ExecutionOptions.MaxConcurrency)
	}
	if !reflect.DeepEqual(resolved.Config.ExcludedCapabilities, []string{"env-one", "env-two"}) {
		t.Fatalf("excluded capabilities = %#v", resolved.Config.ExcludedCapabilities)
	}
	resolved.Config.PropagatedHeaders["X-Test"] = "resolved"
	resolved.Config.ExcludedCapabilities[0] = "resolved"
	if base.PropagatedHeaders["X-Test"] != "base" || base.ExcludedCapabilities[0] != "base-capability" {
		t.Fatal("resolver mutated or aliased the base configuration")
	}
}

func TestResolveOrchestratorConfig_StepRetryBackoffIsCanonical(t *testing.T) {
	resolved, err := ResolveOrchestratorConfig(ConfigResolution{
		Environment: EnvironmentStrict,
		LookupEnv: lookupFromMap(map[string]string{
			"TRUVAG3_STEP_RETRY_INITIAL_DELAY": "750ms",
			"TRUVAG3_STEP_RETRY_MAX_DELAY":     "12s",
		}),
		Options: []OrchestratorOption{WithStepRetryBackoff(time.Second, 15*time.Second)},
	})
	if err != nil {
		t.Fatalf("ResolveOrchestratorConfig() error = %v", err)
	}
	if got := resolved.Config.stepRetryBackoff; got.InitialDelay != time.Second || got.MaxDelay != 15*time.Second {
		t.Fatalf("step retry backoff = %+v", got)
	}

	orchestrator, err := CreateResolvedOrchestrator(resolved.Config, OrchestratorDependencies{
		Discovery: NewMockDiscovery(),
		AIClient:  NewMockAIClient(),
	})
	if err != nil {
		t.Fatalf("CreateResolvedOrchestrator() error = %v", err)
	}
	if got := orchestrator.executor.stepRetryBackoff; got.InitialDelay != time.Second || got.MaxDelay != 15*time.Second {
		t.Fatalf("executor step retry backoff = %+v", got)
	}
}

func TestCreateOrchestratorFromEnvironment_UsesStrictEnvironmentThenCodeOptions(t *testing.T) {
	t.Setenv("TRUVAG3_EXECUTION_MAX_CONCURRENCY", "7")
	deps := OrchestratorDependencies{
		Discovery: NewMockDiscovery(),
		AIClient:  NewMockAIClient(),
	}

	orchestrator, err := CreateOrchestratorFromEnvironment(deps, WithMaxConcurrency(9))
	if err != nil {
		t.Fatalf("CreateOrchestratorFromEnvironment() error = %v", err)
	}
	if got := orchestrator.config.ExecutionOptions.MaxConcurrency; got != 9 {
		t.Fatalf("max concurrency = %d, want code-owned override 9", got)
	}

	t.Setenv("TRUVAG3_EXECUTION_MAX_CONCURRENCY", "invalid")
	if _, err := CreateOrchestratorFromEnvironment(deps); err == nil {
		t.Fatal("strict environment constructor accepted an invalid value")
	} else {
		var environmentErr *ConfigEnvironmentError
		if !errors.As(err, &environmentErr) || environmentErr.Variable != "TRUVAG3_EXECUTION_MAX_CONCURRENCY" {
			t.Fatalf("CreateOrchestratorFromEnvironment() error = %v", err)
		}
	}
}

func TestFailedSimpleOrchestrator_GuardsPublicExecutionEntrypoints(t *testing.T) {
	want := fmt.Errorf("%w: test", ErrOrchestratorConstruction)
	orchestrator := NewAIOrchestrator(NewDefaultOrchestratorConfig(), NewMockDiscovery(), NewMockAIClient())
	orchestrator.constructionErr = want
	logger := &TestLogger{}
	orchestrator.SetLogger(logger)

	if err := orchestrator.Start(context.Background()); !errors.Is(err, ErrOrchestratorConstruction) {
		t.Fatalf("Start() error = %v", err)
	}
	if _, err := orchestrator.ProcessRequest(context.Background(), "test", nil); !errors.Is(err, ErrOrchestratorConstruction) {
		t.Fatalf("ProcessRequest() error = %v", err)
	}
	if _, err := orchestrator.ProcessRequestStreaming(context.Background(), "test", nil, func(core.StreamChunk) error { return nil }); !errors.Is(err, ErrOrchestratorConstruction) {
		t.Fatalf("ProcessRequestStreaming() error = %v", err)
	}
	if _, err := orchestrator.ExecutePlan(context.Background(), &RoutingPlan{}); !errors.Is(err, ErrOrchestratorConstruction) {
		t.Fatalf("ExecutePlan() error = %v", err)
	}
	if _, err := orchestrator.ExecutePlanWithSynthesis(context.Background(), &RoutingPlan{}, "test"); !errors.Is(err, ErrOrchestratorConstruction) {
		t.Fatalf("ExecutePlanWithSynthesis() error = %v", err)
	}
	logs := logger.GetLogsByLevel("ERROR")
	if len(logs) != 5 {
		t.Fatalf("construction rejection logs = %d, want one per public operation", len(logs))
	}
	for _, entry := range logs {
		if entry.Fields["status"] != "rejected" || entry.Fields["error_type"] != "preparation" ||
			entry.Fields["error"] != "orchestrator construction failed" {
			t.Fatalf("construction rejection log = %#v", entry)
		}
	}
}

func TestResolveOrchestratorConfig_CompatibleFallbacksAreDiagnosed(t *testing.T) {
	resolved, err := ResolveOrchestratorConfig(ConfigResolution{
		Environment: EnvironmentCompatible,
		LookupEnv: lookupFromMap(map[string]string{
			"TRUVAG3_PLAN_RETRY_ENABLED":                "not-a-bool",
			"TRUVAG3_EXECUTION_MAX_CONCURRENCY":         "not-an-int",
			"TRUVAG3_PLAN_MODEL":                        "",
			"TRUVAG3_RESULT_TRIM_MAX_AGENT_INPUT_BYTES": "0",
		}),
	})
	if err != nil {
		t.Fatalf("ResolveOrchestratorConfig() error = %v", err)
	}
	if resolved.Config.PlanParseRetryEnabled {
		t.Fatal("compatible invalid boolean did not preserve legacy false coercion")
	}
	if resolved.Config.ExecutionOptions.MaxConcurrency != 25 || resolved.Config.ResultTrim.MaxAgentInputBytes != 0 {
		t.Fatalf("compatible values = concurrency %d, agent-input %d", resolved.Config.ExecutionOptions.MaxConcurrency, resolved.Config.ResultTrim.MaxAgentInputBytes)
	}
	want := []ConfigDiagnostic{
		{Variable: "TRUVAG3_EXECUTION_MAX_CONCURRENCY", Reason: ConfigReasonInvalidInteger, Action: ConfigActionDefaulted},
		{Variable: "TRUVAG3_PLAN_RETRY_ENABLED", Reason: ConfigReasonInvalidBoolean, Action: ConfigActionCoerced},
		{Variable: "TRUVAG3_PLAN_MODEL", Reason: ConfigReasonEmptyValue, Action: ConfigActionIgnored},
	}
	for _, expected := range want {
		found := false
		for _, diagnostic := range resolved.Diagnostics {
			if diagnostic == expected {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("missing diagnostic %+v in %+v", expected, resolved.Diagnostics)
		}
	}
}

func TestResolveOrchestratorConfig_StrictRejectsPresentInvalidValues(t *testing.T) {
	tests := []struct {
		name     string
		variable string
		value    string
		reason   ConfigDiagnosticReason
	}{
		{name: "empty", variable: "TRUVAG3_PLAN_MODEL", value: "", reason: ConfigReasonEmptyValue},
		{name: "boolean", variable: "TRUVAG3_PLAN_RETRY_ENABLED", value: "yes", reason: ConfigReasonInvalidBoolean},
		{name: "integer", variable: "TRUVAG3_EXECUTION_MAX_CONCURRENCY", value: "many", reason: ConfigReasonInvalidInteger},
		{name: "boundary", variable: "TRUVAG3_EXECUTION_MAX_CONCURRENCY", value: "0", reason: ConfigReasonOutOfRange},
		{name: "duration", variable: "TRUVAG3_EXECUTION_STEP_TIMEOUT", value: "later", reason: ConfigReasonInvalidDuration},
		{name: "non-finite float", variable: "TRUVAG3_SYNTHESIS_TEMPERATURE", value: "NaN", reason: ConfigReasonOutOfRange},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := ResolveOrchestratorConfig(ConfigResolution{
				Environment: EnvironmentStrict,
				LookupEnv:   lookupFromMap(map[string]string{test.variable: test.value}),
			})
			var environmentErr *ConfigEnvironmentError
			if !errors.As(err, &environmentErr) || environmentErr.Variable != test.variable || environmentErr.Reason != test.reason {
				t.Fatalf("ResolveOrchestratorConfig() error = %v", err)
			}
			if strings.Contains(err.Error(), test.value) && test.value != "" {
				t.Fatalf("strict error exposed raw value: %v", err)
			}
		})
	}
}

func TestResolveOrchestratorConfig_RejectsUnknownEnvironmentMode(t *testing.T) {
	_, err := ResolveOrchestratorConfig(ConfigResolution{Environment: EnvironmentMode(255)})
	if !errors.Is(err, ErrInvalidOrchestratorConfig) {
		t.Fatalf("ResolveOrchestratorConfig() error = %v", err)
	}
}

func TestResolveOrchestratorConfig_StandardAliasPrecedence(t *testing.T) {
	resolved, err := ResolveOrchestratorConfig(ConfigResolution{
		Environment: EnvironmentCompatible,
		LookupEnv: lookupFromMap(map[string]string{
			"CAPABILITY_SERVICE_URL":         "http://standard",
			"TRUVAG3_CAPABILITY_SERVICE_URL": "http://framework",
		}),
	})
	if err != nil {
		t.Fatalf("ResolveOrchestratorConfig() error = %v", err)
	}
	if resolved.Config.CapabilityService.Endpoint != "http://standard" {
		t.Fatalf("endpoint = %q, want standard alias", resolved.Config.CapabilityService.Endpoint)
	}
}

func TestResolveOrchestratorConfig_StrictIncludesPromptEnvironment(t *testing.T) {
	resolved, err := ResolveOrchestratorConfig(ConfigResolution{
		Environment: EnvironmentStrict,
		LookupEnv: lookupFromMap(map[string]string{
			"TRUVAG3_PROMPT_DOMAIN":              "finance",
			"TRUVAG3_PROMPT_CUSTOM_INSTRUCTIONS": `["use exact amounts"]`,
		}),
	})
	if err != nil {
		t.Fatalf("ResolveOrchestratorConfig() error = %v", err)
	}
	if resolved.Config.PromptConfig.Domain != "finance" ||
		!reflect.DeepEqual(resolved.Config.PromptConfig.CustomInstructions, []string{"use exact amounts"}) {
		t.Fatalf("prompt config = %+v", resolved.Config.PromptConfig)
	}

	_, err = ResolveOrchestratorConfig(ConfigResolution{
		Environment: EnvironmentStrict,
		LookupEnv: lookupFromMap(map[string]string{
			"TRUVAG3_PROMPT_TYPE_RULES": "not-json",
		}),
	})
	var environmentErr *ConfigEnvironmentError
	if !errors.As(err, &environmentErr) || environmentErr.Variable != "TRUVAG3_PROMPT_TYPE_RULES" {
		t.Fatalf("invalid prompt JSON error = %v", err)
	}
}

func TestDefaultConfigWithDiagnostics_IsTotalForInvalidEnvironment(t *testing.T) {
	result := defaultConfigWithDiagnostics(lookupFromMap(map[string]string{
		"TRUVAG3_EXECUTION_MAX_CONCURRENCY": "invalid",
		"TRUVAG3_HITL_DEFAULT_ACTION":       "unknown",
	}))
	if result.Config == nil {
		t.Fatal("DefaultConfigWithDiagnostics returned nil config")
	}
	if err := ValidateOrchestratorConfig(result.Config); err != nil {
		t.Fatalf("compatible result is invalid: %v", err)
	}
	if len(result.Diagnostics) != 2 {
		t.Fatalf("diagnostics = %+v, want 2", result.Diagnostics)
	}
}

func TestResolveOrchestratorConfigIdentity_IsStableAndSanitized(t *testing.T) {
	config := NewDefaultOrchestratorConfig()
	config.OAuthToken = "secret-token"
	config.PropagatedHeaders = map[string]string{"Authorization": "secret-header"}
	config.CapabilityService.Endpoint = "https://user:password@example.invalid"
	config.PromptConfig.Template = "private prompt body"
	config.PromptConfig.SystemInstructions = "private instructions"

	first, err := ResolveOrchestratorConfigIdentity(config)
	if err != nil {
		t.Fatalf("ResolveOrchestratorConfigIdentity() error = %v", err)
	}
	second, err := ResolveOrchestratorConfigIdentity(cloneOrchestratorConfig(config))
	if err != nil {
		t.Fatalf("ResolveOrchestratorConfigIdentity(clone) error = %v", err)
	}
	if first.Fingerprint == "" || first.Fingerprint != second.Fingerprint {
		t.Fatalf("fingerprints = %q / %q", first.Fingerprint, second.Fingerprint)
	}
	if !first.CacheEligible || !second.CacheEligible {
		t.Fatal("deterministic configuration was marked cache-ineligible")
	}
	identityJSON, err := json.Marshal(first)
	if err != nil {
		t.Fatalf("marshal identity: %v", err)
	}
	encoded := string(identityJSON)
	for _, prohibited := range []string{"secret-token", "secret-header", "password", "private prompt body", "private instructions"} {
		if strings.Contains(encoded, prohibited) {
			t.Fatalf("identity exposed prohibited content %q", prohibited)
		}
	}

	config.ExecutionOptions.MaxConcurrency++
	changed, err := ResolveOrchestratorConfigIdentity(config)
	if err != nil {
		t.Fatalf("changed identity error = %v", err)
	}
	if changed.Fingerprint == first.Fingerprint {
		t.Fatal("behavior change did not alter fingerprint")
	}

	config = NewDefaultOrchestratorConfig()
	baseline, err := ResolveOrchestratorConfigIdentity(config)
	if err != nil {
		t.Fatalf("baseline identity error = %v", err)
	}
	config.TieredResolution.SelectionMaxTokens++
	tieredChange, err := ResolveOrchestratorConfigIdentity(config)
	if err != nil {
		t.Fatalf("tiered identity error = %v", err)
	}
	if tieredChange.Fingerprint == baseline.Fingerprint {
		t.Fatal("automatically projected behavior field did not alter fingerprint")
	}

	config = NewDefaultOrchestratorConfig()
	config.PromptConfig.Template = "first private prompt"
	firstPrompt, err := ResolveOrchestratorConfigIdentity(config)
	if err != nil {
		t.Fatalf("first prompt identity error = %v", err)
	}
	config.PromptConfig.Template = "second private prompt"
	secondPrompt, err := ResolveOrchestratorConfigIdentity(config)
	if err != nil {
		t.Fatalf("second prompt identity error = %v", err)
	}
	if firstPrompt.Fingerprint == secondPrompt.Fingerprint {
		t.Fatal("prompt policy change did not alter fingerprint")
	}
	for _, identity := range []OrchestratorConfigIdentity{firstPrompt, secondPrompt} {
		encodedIdentity, marshalErr := json.Marshal(identity)
		if marshalErr != nil {
			t.Fatalf("marshal prompt identity: %v", marshalErr)
		}
		if strings.Contains(string(encodedIdentity), "private prompt") {
			t.Fatal("prompt identity exposed prompt content")
		}
	}
}

func TestResolveOrchestratorConfigIdentity_NonDeterministicExtensionDisablesCacheReuse(t *testing.T) {
	config := NewDefaultOrchestratorConfig()
	config.PlanAIOptions = &AIOptionsOverride{Extra: map[string]interface{}{
		"callback": func() {},
	}}

	identity, err := ResolveOrchestratorConfigIdentity(config)
	if err != nil {
		t.Fatalf("ResolveOrchestratorConfigIdentity() error = %v", err)
	}
	if identity.CacheEligible {
		t.Fatal("non-deterministic provider extension was marked cache-eligible")
	}
	if identity.Fingerprint != "" {
		t.Fatalf("cache-ineligible fingerprint = %q, want empty", identity.Fingerprint)
	}
	encoded, err := json.Marshal(identity)
	if err != nil {
		t.Fatalf("marshal identity: %v", err)
	}
	if strings.Contains(string(encoded), `"callback"`) {
		t.Fatal("identity exposed a provider-extension key")
	}
}

func TestResolveOrchestratorConfig_ExecutionStoreWriteTimeoutPrecedence(t *testing.T) {
	lookup := lookupFromMap(map[string]string{
		"TRUVAG3_EXECUTION_STORE_WRITE_TIMEOUT": "7s",
	})
	result, err := ResolveOrchestratorConfig(ConfigResolution{
		Environment: EnvironmentStrict,
		LookupEnv:   lookup,
		Options:     []OrchestratorOption{WithExecutionStoreWriteTimeout(9 * time.Second)},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Config.executionStoreWriteTimeout != 9*time.Second {
		t.Fatalf("execution store timeout = %v, want code override 9s", result.Config.executionStoreWriteTimeout)
	}

	if _, err := ResolveOrchestratorConfig(ConfigResolution{
		Environment: EnvironmentStrict,
		LookupEnv: lookupFromMap(map[string]string{
			"TRUVAG3_EXECUTION_STORE_WRITE_TIMEOUT": "invalid",
		}),
	}); err == nil {
		t.Fatal("strict resolver accepted invalid execution-store timeout")
	}
}

func TestCreateResolvedOrchestrator_TelemetryIsInjectedOrNoOp(t *testing.T) {
	config := NewDefaultOrchestratorConfig()
	logger := &mockLogger{}
	var diagnosticReason string
	var diagnosticStatus string
	logger.warnFunc = func(_ string, fields map[string]interface{}) {
		if fields["operation"] == "orchestrator_construction_fallback" {
			diagnosticReason, _ = fields["reason"].(string)
			diagnosticStatus, _ = fields["status"].(string)
		}
	}

	orchestrator, err := CreateResolvedOrchestrator(config, OrchestratorDependencies{
		Discovery: NewMockDiscovery(),
		AIClient:  NewMockAIClient(),
		Logger:    logger,
	})
	if err != nil {
		t.Fatalf("CreateResolvedOrchestrator() error = %v", err)
	}
	if _, ok := orchestrator.telemetry.(*core.NoOpTelemetry); !ok {
		t.Fatalf("telemetry = %T, want NoOp", orchestrator.telemetry)
	}
	if diagnosticReason != string(ConfigReasonTelemetryDependencyMissing) {
		t.Fatalf("diagnostic reason = %q", diagnosticReason)
	}
	if diagnosticStatus != "fallback" {
		t.Fatalf("diagnostic status = %q", diagnosticStatus)
	}

	injected := &mockTelemetry{}
	orchestrator, err = CreateResolvedOrchestrator(config, OrchestratorDependencies{
		Discovery: NewMockDiscovery(),
		AIClient:  NewMockAIClient(),
		Telemetry: injected,
	})
	if err != nil {
		t.Fatalf("CreateResolvedOrchestrator(injected) error = %v", err)
	}
	if orchestrator.telemetry != injected {
		t.Fatal("canonical constructor did not preserve injected telemetry")
	}
}

func TestConstructors_RejectUnknownSynthesisStrategy(t *testing.T) {
	config := NewDefaultOrchestratorConfig()
	config.SynthesisStrategy = "unknown"
	deps := OrchestratorDependencies{Discovery: NewMockDiscovery(), AIClient: NewMockAIClient()}
	for name, construct := range map[string]func() (*AIOrchestrator, error){
		"canonical":     func() (*AIOrchestrator, error) { return CreateResolvedOrchestrator(config, deps) },
		"compatibility": func() (*AIOrchestrator, error) { return CreateOrchestrator(config, deps) },
	} {
		t.Run(name, func(t *testing.T) {
			orchestrator, err := construct()
			if orchestrator != nil || !errors.Is(err, ErrInvalidOrchestratorConfig) {
				t.Fatalf("constructor = (%v, %v)", orchestrator, err)
			}
		})
	}
}
