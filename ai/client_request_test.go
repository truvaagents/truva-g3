package ai

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/truvaagents/truva-g3/ai/requestpolicy"
	"github.com/truvaagents/truva-g3/core"
)

type phase3RequestClient struct{}

func (*phase3RequestClient) GenerateResponse(context.Context, string, *core.AIOptions) (*core.AIResponse, error) {
	return &core.AIResponse{Content: "legacy"}, nil
}

func (*phase3RequestClient) Generate(context.Context, *core.AIRequest) (*core.AIResult, error) {
	return &core.AIResult{Response: &core.AIResponse{Content: "request"}}, nil
}

type phase3LegacyClient struct{}

func (*phase3LegacyClient) GenerateResponse(context.Context, string, *core.AIOptions) (*core.AIResponse, error) {
	return &core.AIResponse{Content: "legacy"}, nil
}

type phase3ValidatedFactory struct {
	name           string
	client         core.AIClient
	err            error
	createCalls    int
	validatedCalls int
	config         *AIConfig
}

func (f *phase3ValidatedFactory) Name() string                 { return f.name }
func (*phase3ValidatedFactory) Description() string            { return "phase 3 validated factory" }
func (*phase3ValidatedFactory) DetectEnvironment() (int, bool) { return 1, true }
func (f *phase3ValidatedFactory) Create(*AIConfig) core.AIClient {
	f.createCalls++
	return &phase3LegacyClient{}
}
func (f *phase3ValidatedFactory) CreateValidated(config *AIConfig) (core.AIClient, error) {
	f.validatedCalls++
	f.config = config
	return f.client, f.err
}

type phase3RequestFactory struct {
	name         string
	client       core.AIRequestClient
	err          error
	createCalls  int
	requestCalls int
	config       *AIConfig
	integration  ProviderIntegrationConfig
}

func (f *phase3RequestFactory) Name() string                 { return f.name }
func (*phase3RequestFactory) Description() string            { return "phase 3 request factory" }
func (*phase3RequestFactory) DetectEnvironment() (int, bool) { return 1, true }
func (f *phase3RequestFactory) Create(*AIConfig) core.AIClient {
	f.createCalls++
	return &phase3LegacyClient{}
}
func (f *phase3RequestFactory) CreateRequestClient(
	config *AIConfig,
	integration ProviderIntegrationConfig,
) (core.AIRequestClient, error) {
	f.requestCalls++
	f.config = config
	f.integration = integration
	return f.client, f.err
}

type phase3LegacyFactory struct {
	name   string
	client core.AIClient
	calls  int
}

func (f *phase3LegacyFactory) Name() string                 { return f.name }
func (*phase3LegacyFactory) Description() string            { return "phase 3 legacy factory" }
func (*phase3LegacyFactory) DetectEnvironment() (int, bool) { return 1, true }
func (f *phase3LegacyFactory) Create(*AIConfig) core.AIClient {
	f.calls++
	return f.client
}

type phase3Middleware struct {
	name    string
	version string
}

func (m *phase3Middleware) Name() string    { return m.name }
func (m *phase3Middleware) Version() string { return m.version }
func (*phase3Middleware) Apply(context.Context, requestpolicy.RequestEditor) error {
	return nil
}

func installPhase3Factory(t *testing.T, factory ProviderFactory) {
	t.Helper()
	original := registry
	registry = &ProviderRegistry{providers: map[string]ProviderFactory{factory.Name(): factory}}
	t.Cleanup(func() { registry = original })
}

func requireFactoryInstrumentedClient(t *testing.T, client core.AIClient, wrapped core.AIClient) *InstrumentedAIClient {
	t.Helper()
	instrumented, ok := client.(*InstrumentedAIClient)
	if !ok {
		t.Fatalf("factory client type = %T, want *InstrumentedAIClient", client)
	}
	if instrumented.wrapped != wrapped || !instrumented.factoryManaged {
		t.Fatalf("factory instrumentation = wrapped %T, managed %v", instrumented.wrapped, instrumented.factoryManaged)
	}
	return instrumented
}

func TestNewClient_PrefersValidatedFactory(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		factory := &phase3ValidatedFactory{
			name:   "validated",
			client: &phase3LegacyClient{},
		}
		installPhase3Factory(t, factory)

		client, err := NewClient(WithProvider(factory.name))
		if err != nil {
			t.Fatalf("NewClient returned error: %v", err)
		}
		requireFactoryInstrumentedClient(t, client, factory.client)
		if factory.validatedCalls != 1 || factory.createCalls != 0 {
			t.Fatalf("validated factory dispatch = client %T, validated calls %d, legacy calls %d", client, factory.validatedCalls, factory.createCalls)
		}
	})

	t.Run("error", func(t *testing.T) {
		wantErr := errors.New("invalid provider configuration")
		factory := &phase3ValidatedFactory{name: "validated-error", err: wantErr}
		installPhase3Factory(t, factory)

		if _, err := NewClient(WithProvider(factory.name)); !errors.Is(err, wantErr) {
			t.Fatalf("NewClient error = %v, want %v", err, wantErr)
		}
		if factory.validatedCalls != 1 || factory.createCalls != 0 {
			t.Fatalf("factory calls = validated %d, legacy %d", factory.validatedCalls, factory.createCalls)
		}
	})

	t.Run("nil client", func(t *testing.T) {
		factory := &phase3ValidatedFactory{name: "validated-nil"}
		installPhase3Factory(t, factory)

		if _, err := NewClient(WithProvider(factory.name)); err == nil || !strings.Contains(err.Error(), "nil client") {
			t.Fatalf("NewClient error = %v, want nil-client error", err)
		}
	})
}

func TestNewRequestClient_RequestFactoryReceivesSnapshots(t *testing.T) {
	t.Setenv("TRUVAG3_AI_RETRY_ATTEMPTS", "7")
	factory := &phase3RequestFactory{
		name:   "request-aware",
		client: &phase3RequestClient{},
	}
	installPhase3Factory(t, factory)

	headerSource := map[string]string{"X-Policy": "original"}
	headerOption := WithHeaders(headerSource)
	ruleValue := map[string]interface{}{"tenant": "original"}
	ruleOption := WithRequestRules(core.AIProviderPatch{
		Name:     "tenant-policy",
		Version:  "3",
		Selector: core.AIProviderSelector{AllProviders: true},
		Set:      map[string]interface{}{`/metadata`: ruleValue},
	})
	middleware := &phase3Middleware{name: "tenant-middleware", version: "4"}
	middlewareSource := []requestpolicy.RequestMiddleware{middleware}
	middlewareOption := WithRequestMiddleware(middlewareSource...)

	// New options snapshot captured collections when the option is created.
	headerSource["X-Policy"] = "caller-mutated-before-apply"
	ruleValue["tenant"] = "caller-mutated-before-apply"
	middlewareSource[0] = &phase3Middleware{name: "replacement", version: "1"}

	extraNested := map[string]interface{}{"value": "original"}
	client, err := NewRequestClient(
		WithProvider(factory.name),
		WithMaxRetries(2),
		headerOption,
		AIOption(func(config *AIConfig) {
			config.Extra = map[string]interface{}{"nested": extraNested}
		}),
		ruleOption,
		middlewareOption,
		WithCompatibilityMode(requestpolicy.CompatibilityStrict),
	)
	if err != nil {
		t.Fatalf("NewRequestClient returned error: %v", err)
	}
	requireFactoryInstrumentedClient(t, client, factory.client)
	if factory.requestCalls != 1 || factory.createCalls != 0 {
		t.Fatalf("request factory dispatch = client %T, request calls %d, legacy calls %d", client, factory.requestCalls, factory.createCalls)
	}
	extraNested["value"] = "caller-mutated-after-construction"

	if factory.config.MaxRetries != 2 {
		t.Fatalf("explicit retry option lost precedence: %d", factory.config.MaxRetries)
	}
	if factory.config.Headers["X-Policy"] != "original" {
		t.Fatalf("header option retained caller map: %#v", factory.config.Headers)
	}
	gotExtra := factory.config.Extra["nested"].(map[string]interface{})["value"]
	if gotExtra != "original" {
		t.Fatalf("factory config retained caller Extra: %#v", gotExtra)
	}
	if factory.integration.CompatibilityMode != requestpolicy.CompatibilityStrict {
		t.Fatalf("compatibility mode = %d", factory.integration.CompatibilityMode)
	}
	if len(factory.integration.RequestRules) != 1 {
		t.Fatalf("request rules = %#v", factory.integration.RequestRules)
	}
	gotRule := factory.integration.RequestRules[0].Set[`/metadata`].(map[string]interface{})["tenant"]
	if gotRule != "original" {
		t.Fatalf("request rule retained caller value: %#v", gotRule)
	}
	if len(factory.integration.RequestMiddleware) != 1 || factory.integration.RequestMiddleware[0] != middleware {
		t.Fatalf("middleware declaration order/snapshot changed: %#v", factory.integration.RequestMiddleware)
	}
}

func TestNewRequestClient_LegacyFactoryFallback(t *testing.T) {
	t.Run("request-capable client with zero integration", func(t *testing.T) {
		factory := &phase3LegacyFactory{name: "legacy-request", client: &phase3RequestClient{}}
		installPhase3Factory(t, factory)

		client, err := NewRequestClient(WithProvider(factory.name))
		if err != nil {
			t.Fatalf("NewRequestClient returned error: %v", err)
		}
		requireFactoryInstrumentedClient(t, client, factory.client)
		if factory.calls != 1 {
			t.Fatalf("legacy fallback = client %T, calls %d", client, factory.calls)
		}
	})

	t.Run("integration is not silently discarded", func(t *testing.T) {
		factory := &phase3LegacyFactory{name: "legacy-integration", client: &phase3RequestClient{}}
		installPhase3Factory(t, factory)

		_, err := NewRequestClient(
			WithProvider(factory.name),
			WithRequestRules(core.AIProviderPatch{
				Name:     "rule",
				Version:  "1",
				Selector: core.AIProviderSelector{AllProviders: true},
			}),
		)
		if !errors.Is(err, core.ErrAIRequestFeatureUnsupported) {
			t.Fatalf("NewRequestClient error = %v, want unsupported feature", err)
		}
		if factory.calls != 0 {
			t.Fatalf("legacy factory was invoked before refusing integration: %d", factory.calls)
		}
	})

	t.Run("legacy-only client is refused", func(t *testing.T) {
		factory := &phase3LegacyFactory{name: "legacy-only", client: &phase3LegacyClient{}}
		installPhase3Factory(t, factory)

		if _, err := NewRequestClient(WithProvider(factory.name)); !errors.Is(err, core.ErrAIRequestFeatureUnsupported) {
			t.Fatalf("NewRequestClient error = %v, want unsupported feature", err)
		}
		if factory.calls != 1 {
			t.Fatalf("legacy factory calls = %d, want 1", factory.calls)
		}
	})
}

func TestNewRequestClient_ValidationAndFactoryFailures(t *testing.T) {
	if _, err := NewRequestClient(nil); err == nil || !strings.Contains(err.Error(), "option 0 is nil") {
		t.Fatalf("nil ClientOption error = %v", err)
	}
	var nilLegacy AIOption
	if _, err := NewRequestClient(nilLegacy); err == nil || !strings.Contains(err.Error(), "AI option is nil") {
		t.Fatalf("nil AIOption error = %v", err)
	}
	var nilAdvanced clientOptionFunc
	if _, err := NewRequestClient(nilAdvanced); err == nil || !strings.Contains(err.Error(), "client option is nil") {
		t.Fatalf("nil advanced option error = %v", err)
	}
	if _, err := NewRequestClient(WithRequestRules(core.AIProviderPatch{
		Selector: core.AIProviderSelector{AllProviders: true},
	})); err == nil || !strings.Contains(err.Error(), "rule name is required") {
		t.Fatalf("invalid request rule error = %v", err)
	}
	if _, err := NewRequestClient(WithCompatibilityMode(requestpolicy.CompatibilityMode(99))); err == nil || !strings.Contains(err.Error(), "compatibility mode") {
		t.Fatalf("invalid compatibility mode error = %v", err)
	}

	t.Run("invalid middleware", func(t *testing.T) {
		factory := &phase3RequestFactory{name: "middleware-validation", client: &phase3RequestClient{}}
		installPhase3Factory(t, factory)
		if _, err := NewRequestClient(WithProvider(factory.name), WithRequestMiddleware(nil)); err == nil || !strings.Contains(err.Error(), "middleware 0 is nil") {
			t.Fatalf("nil middleware error = %v", err)
		}
		if factory.requestCalls != 0 {
			t.Fatalf("factory called before middleware validation: %d", factory.requestCalls)
		}
	})

	t.Run("request factory error", func(t *testing.T) {
		wantErr := errors.New("request construction failed")
		factory := &phase3RequestFactory{name: "request-error", err: wantErr}
		installPhase3Factory(t, factory)
		if _, err := NewRequestClient(WithProvider(factory.name)); !errors.Is(err, wantErr) {
			t.Fatalf("NewRequestClient error = %v, want %v", err, wantErr)
		}
	})

	t.Run("request factory nil client", func(t *testing.T) {
		factory := &phase3RequestFactory{name: "request-nil"}
		installPhase3Factory(t, factory)
		if _, err := NewRequestClient(WithProvider(factory.name)); err == nil || !strings.Contains(err.Error(), "nil request client") {
			t.Fatalf("NewRequestClient error = %v, want nil-client error", err)
		}
	})

	t.Run("legacy factory nil client", func(t *testing.T) {
		factory := &phase3LegacyFactory{name: "legacy-nil"}
		installPhase3Factory(t, factory)
		if _, err := NewRequestClient(WithProvider(factory.name)); err == nil || !strings.Contains(err.Error(), "nil client") {
			t.Fatalf("NewRequestClient error = %v, want nil-client error", err)
		}
	})
}

func TestProviderConstructors_Phase6InstrumentationComposition(t *testing.T) {
	t.Run("NewClient installs logical instrumentation", func(t *testing.T) {
		raw := &mockAIClientForInstr{generateResp: &core.AIResponse{
			Content:  "legacy",
			Model:    "gpt-4o-mini",
			Provider: "openai",
			Usage:    core.TokenUsage{PromptTokens: 1_000_000, CompletionTokens: 1_000_000},
		}}
		factory := &phase3LegacyFactory{name: "phase6-logical-instrumentation", client: raw}
		installPhase3Factory(t, factory)
		tracing := &phase6InstrumentedTelemetry{}

		client, err := NewClient(WithProvider(factory.name), WithTelemetry(tracing))
		if err != nil {
			t.Fatalf("NewClient() error = %v", err)
		}
		result, err := core.GenerateAI(t.Context(), client, core.NewAIRequest("prompt", "instrumentation"))
		if err != nil {
			t.Fatalf("GenerateAI() error = %v", err)
		}
		if result.Response != raw.generateResp {
			t.Fatalf("normalized response = %#v", result.Response)
		}
		if len(tracing.names) != 1 || tracing.names[0] != "ai.generate" || len(tracing.spans) != 1 {
			t.Fatalf("common telemetry = names %#v, spans %d", tracing.names, len(tracing.spans))
		}
		if tracing.spans[0].attributes["ai.prompt_tokens"] != 1_000_000 ||
			tracing.spans[0].attributes["ai.completion_tokens"] != 1_000_000 {
			t.Fatalf("common usage attributes = %#v", tracing.spans[0].attributes)
		}
	})

	t.Run("explicit instrumentation collapses factory layer", func(t *testing.T) {
		raw := &mockAIClientForInstr{generateResp: &core.AIResponse{
			Model: "gpt-4.1",
			Usage: core.TokenUsage{PromptTokens: 1_000_000},
		}}
		factory := &phase3LegacyFactory{name: "phase6-collapse", client: raw}
		installPhase3Factory(t, factory)
		tracing := &phase6InstrumentedTelemetry{}
		managed, err := NewClient(WithProvider(factory.name), WithTelemetry(tracing))
		if err != nil {
			t.Fatalf("NewClient() error = %v", err)
		}
		instrumented := NewInstrumentedClient(managed, nil)
		if instrumented.wrapped != raw {
			t.Fatalf("nested factory instrumentation was not collapsed: %T", instrumented.wrapped)
		}
		result, err := instrumented.Generate(t.Context(), core.NewAIRequest("prompt", "instrumentation"))
		if err != nil {
			t.Fatalf("Generate() error = %v", err)
		}
		if result == nil || result.Response != raw.generateResp || raw.callCount != 1 {
			t.Fatalf("collapsed result = %#v, provider calls %d", result, raw.callCount)
		}
		if len(tracing.names) != 1 || tracing.names[0] != "ai.generate" {
			t.Fatalf("collapsed logical spans = %#v", tracing.names)
		}
	})
}

func TestWithHeaders_SnapshotsInputAtOptionCreation(t *testing.T) {
	headers := map[string]string{"X-Test": "original"}
	option := WithHeaders(headers)
	headers["X-Test"] = "caller-mutated"

	config := &AIConfig{}
	option(config)
	if config.Headers["X-Test"] != "original" {
		t.Fatalf("WithHeaders retained caller map: %#v", config.Headers)
	}
}
