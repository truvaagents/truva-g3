package ai

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/truvaagents/truva-g3/core"
)

// mockFactory is a test factory for client testing
type mockFactory struct {
	name      string
	priority  int
	available bool
	client    core.AIClient
	createErr error
}

type timeoutDefaultFactory struct {
	*mockFactory
	defaultTimeout time.Duration
	lastConfig     *AIConfig
}

func (factory *timeoutDefaultFactory) Create(config *AIConfig) core.AIClient {
	factory.lastConfig = config
	return factory.client
}

func (factory *timeoutDefaultFactory) DefaultRequestTimeout() time.Duration {
	return factory.defaultTimeout
}

func (f *mockFactory) Name() string {
	return f.name
}

func (f *mockFactory) Description() string {
	return "Mock provider for testing"
}

func (f *mockFactory) Priority() int {
	return f.priority
}

func (f *mockFactory) Create(config *AIConfig) core.AIClient {
	if f.createErr != nil {
		return &errorClient{err: f.createErr}
	}
	return f.client
}

func (f *mockFactory) DetectEnvironment() (int, bool) {
	return f.priority, f.available
}

// mockAIClient is a test implementation of core.AIClient
type mockAIClient struct {
	generateFunc func(ctx context.Context, prompt string, options *core.AIOptions) (*core.AIResponse, error)
}

func (c *mockAIClient) GenerateResponse(ctx context.Context, prompt string, options *core.AIOptions) (*core.AIResponse, error) {
	if c.generateFunc != nil {
		return c.generateFunc(ctx, prompt, options)
	}
	return &core.AIResponse{
		Content: "mock response",
		Model:   "mock-model",
		Usage:   core.TokenUsage{PromptTokens: 10, CompletionTokens: 20, TotalTokens: 30},
	}, nil
}

// errorClient for testing error cases
type errorClient struct {
	err error
}

func (e *errorClient) GenerateResponse(ctx context.Context, prompt string, options *core.AIOptions) (*core.AIResponse, error) {
	return nil, e.err
}

func TestNewClient(t *testing.T) {
	// Save original registry
	originalRegistry := registry
	defer func() { registry = originalRegistry }()

	tests := []struct {
		name     string
		options  []AIOption
		setup    func()
		wantErr  bool
		errMsg   string
		validate func(*testing.T, core.AIClient)
	}{
		{
			name: "auto-detect with available provider",
			setup: func() {
				registry = &ProviderRegistry{
					providers: make(map[string]ProviderFactory),
				}
				registry.providers["mock1"] = &mockFactory{
					name:      "mock1",
					priority:  100,
					available: true,
					client:    &mockAIClient{},
				}
			},
			wantErr: false,
		},
		{
			name: "auto-detect with no available providers",
			setup: func() {
				registry = &ProviderRegistry{
					providers: make(map[string]ProviderFactory),
				}
				registry.providers["mock1"] = &mockFactory{
					name:      "mock1",
					priority:  100,
					available: false,
				}
			},
			wantErr: true,
			errMsg:  "no AI provider available",
		},
		{
			name: "explicit provider selection",
			options: []AIOption{
				WithProvider("mock2"),
			},
			setup: func() {
				registry = &ProviderRegistry{
					providers: make(map[string]ProviderFactory),
				}
				registry.providers["mock2"] = &mockFactory{
					name:   "mock2",
					client: &mockAIClient{},
				}
			},
			wantErr: false,
		},
		{
			name: "unknown provider",
			options: []AIOption{
				WithProvider("unknown"),
			},
			setup: func() {
				registry = &ProviderRegistry{
					providers: make(map[string]ProviderFactory),
				}
			},
			wantErr: true,
			errMsg:  "provider 'unknown' not registered",
		},
		{
			name: "provider with custom config",
			options: []AIOption{
				WithProvider("mock3"),
				WithAPIKey("test-key"),
				WithBaseURL("https://test.com"),
				WithModel("test-model"),
				WithTemperature(0.7),
				WithMaxTokens(1000),
			},
			setup: func() {
				registry = &ProviderRegistry{
					providers: make(map[string]ProviderFactory),
				}
				registry.providers["mock3"] = &mockFactory{
					name: "mock3",
					client: &mockAIClient{
						generateFunc: func(ctx context.Context, prompt string, options *core.AIOptions) (*core.AIResponse, error) {
							// Verify config was passed through
							if options.Model != "test-model" {
								t.Errorf("expected model test-model, got %s", options.Model)
							}
							return &core.AIResponse{Content: "configured"}, nil
						},
					},
				}
			},
			wantErr: false,
			validate: func(t *testing.T, client core.AIClient) {
				resp, err := client.GenerateResponse(context.Background(), "test", &core.AIOptions{
					Model: "test-model",
				})
				if err != nil {
					t.Errorf("unexpected error: %v", err)
				}
				if resp.Content != "configured" {
					t.Errorf("expected configured response, got %s", resp.Content)
				}
			},
		},
		{
			name: "auto-detect chooses highest priority",
			setup: func() {
				registry = &ProviderRegistry{
					providers: make(map[string]ProviderFactory),
				}
				registry.providers["low"] = &mockFactory{
					name:      "low",
					priority:  50,
					available: true,
					client: &mockAIClient{
						generateFunc: func(ctx context.Context, prompt string, options *core.AIOptions) (*core.AIResponse, error) {
							return &core.AIResponse{Content: "low priority"}, nil
						},
					},
				}
				registry.providers["high"] = &mockFactory{
					name:      "high",
					priority:  150,
					available: true,
					client: &mockAIClient{
						generateFunc: func(ctx context.Context, prompt string, options *core.AIOptions) (*core.AIResponse, error) {
							return &core.AIResponse{Content: "high priority"}, nil
						},
					},
				}
			},
			wantErr: false,
			validate: func(t *testing.T, client core.AIClient) {
				resp, err := client.GenerateResponse(context.Background(), "test", nil)
				if err != nil {
					t.Errorf("unexpected error: %v", err)
				}
				if resp.Content != "high priority" {
					t.Errorf("expected high priority provider, got %s", resp.Content)
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.setup != nil {
				tt.setup()
			}

			client, err := NewClient(tt.options...)

			if tt.wantErr {
				if err == nil {
					t.Errorf("expected error, got nil")
				} else if tt.errMsg != "" && !strings.Contains(err.Error(), tt.errMsg) {
					t.Errorf("expected error containing %q, got %q", tt.errMsg, err.Error())
				}
				return
			}

			if err != nil {
				t.Errorf("unexpected error: %v", err)
				return
			}

			if client == nil {
				t.Error("expected client, got nil")
				return
			}

			if tt.validate != nil {
				tt.validate(t, client)
			}
		})
	}
}

func TestWithOptions(t *testing.T) {
	config := &AIConfig{}

	// Test WithProvider
	WithProvider("test-provider")(config)
	if config.Provider != "test-provider" {
		t.Errorf("expected provider test-provider, got %s", config.Provider)
	}

	// Test WithAPIKey
	WithAPIKey("test-key")(config)
	if config.APIKey != "test-key" {
		t.Errorf("expected API key test-key, got %s", config.APIKey)
	}

	// Test WithBaseURL
	WithBaseURL("https://test.com")(config)
	if config.BaseURL != "https://test.com" {
		t.Errorf("expected base URL https://test.com, got %s", config.BaseURL)
	}

	// Test WithModel
	WithModel("test-model")(config)
	if config.Model != "test-model" {
		t.Errorf("expected model test-model, got %s", config.Model)
	}

	// Test WithTemperature
	WithTemperature(0.8)(config)
	if config.Temperature != 0.8 {
		t.Errorf("expected temperature 0.8, got %f", config.Temperature)
	}

	// Test WithMaxTokens
	WithMaxTokens(2000)(config)
	if config.MaxTokens != 2000 {
		t.Errorf("expected max tokens 2000, got %d", config.MaxTokens)
	}

	// Test WithRegion
	WithRegion("us-west-2")(config)
	if config.Extra["region"] != "us-west-2" {
		t.Errorf("expected region us-west-2, got %v", config.Extra["region"])
	}

	// Test WithAWSCredentials
	WithAWSCredentials("access", "secret", "token")(config)
	if config.Extra["aws_access_key_id"] != "access" {
		t.Errorf("expected access key 'access', got %v", config.Extra["aws_access_key_id"])
	}
	if config.Extra["aws_secret_access_key"] != "secret" {
		t.Errorf("expected secret key 'secret', got %v", config.Extra["aws_secret_access_key"])
	}
	if config.Extra["aws_session_token"] != "token" {
		t.Errorf("expected session token 'token', got %v", config.Extra["aws_session_token"])
	}
}

func TestAutoDetectProvider(t *testing.T) {
	// Save original registry
	originalRegistry := registry
	defer func() { registry = originalRegistry }()

	tests := []struct {
		name          string
		factories     []ProviderFactory
		expectedName  string
		expectedError string
	}{
		{
			name: "single available provider",
			factories: []ProviderFactory{
				&mockFactory{name: "provider1", priority: 100, available: true},
			},
			expectedName: "provider1",
		},
		{
			name: "multiple providers, highest priority wins",
			factories: []ProviderFactory{
				&mockFactory{name: "provider1", priority: 50, available: true},
				&mockFactory{name: "provider2", priority: 100, available: true},
				&mockFactory{name: "provider3", priority: 75, available: true},
			},
			expectedName: "provider2",
		},
		{
			name: "only unavailable providers",
			factories: []ProviderFactory{
				&mockFactory{name: "provider1", priority: 100, available: false},
				&mockFactory{name: "provider2", priority: 200, available: false},
			},
			expectedError: "no provider detected in environment",
		},
		{
			name:          "no providers registered",
			factories:     []ProviderFactory{},
			expectedError: "no provider detected in environment",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Setup registry with test factories
			registry = &ProviderRegistry{
				providers: make(map[string]ProviderFactory),
			}
			for _, f := range tt.factories {
				registry.providers[f.Name()] = f
			}

			providerName, err := detectBestProvider(nil)

			if tt.expectedError != "" {
				if err == nil {
					t.Errorf("expected error %q, got nil", tt.expectedError)
				} else if err.Error() != tt.expectedError {
					t.Errorf("expected error %q, got %q", tt.expectedError, err.Error())
				}
				return
			}

			if err != nil {
				t.Errorf("unexpected error: %v", err)
				return
			}

			if providerName != tt.expectedName {
				t.Errorf("expected provider %s, got %s", tt.expectedName, providerName)
			}
		})
	}
}

// --- resolveMaxRetries: precedence chain unit tests ---
//
// resolveMaxRetries is the small helper that decides what MaxRetries value
// NewClient should put on the AIConfig before handing it to the factory. It
// implements the framework's Configuration Precedence rule for this single
// field: explicit option > env var > default. Each test isolates one branch
// of that precedence chain.

func TestResolveMaxRetries_ExplicitOptionWins(t *testing.T) {
	// When the option set MaxRetries to anything other than the unset
	// sentinel, resolveMaxRetries must return that value verbatim — even
	// when an env var is also set, even when the explicit value is 0.
	t.Setenv("TRUVAG3_AI_RETRY_ATTEMPTS", "7")

	if got := resolveMaxRetries(5); got != 5 {
		t.Errorf("explicit 5 should win over env var: got %d, want 5", got)
	}
	if got := resolveMaxRetries(0); got != 0 {
		t.Errorf("explicit 0 must be honored as 'no retries': got %d, want 0", got)
	}
	if got := resolveMaxRetries(1); got != 1 {
		t.Errorf("explicit 1 should be returned as-is: got %d, want 1", got)
	}
}

func TestResolveMaxRetries_EnvVarUsedWhenSentinel(t *testing.T) {
	t.Setenv("TRUVAG3_AI_RETRY_ATTEMPTS", "5")
	if got := resolveMaxRetries(maxRetriesUnset); got != 5 {
		t.Errorf("env var should fill in the sentinel: got %d, want 5", got)
	}
}

func TestResolveMaxRetries_EnvVarZeroIsRejected(t *testing.T) {
	// Per FRAMEWORK_DESIGN_PRINCIPLES §3.5 rule 3: env var values for
	// numeric limits are guarded with val > 0. Zero is rejected and the
	// resolver falls back to the supplied default. To disable retries
	// entirely, operators must use WithMaxRetries(0) programmatically
	// (single client) or rely on the chain client's default of 0.
	t.Setenv("TRUVAG3_AI_RETRY_ATTEMPTS", "0")
	if got := resolveMaxRetries(maxRetriesUnset); got != defaultMaxRetries {
		t.Errorf("env var 0 must be rejected by single-client resolver: got %d, want %d", got, defaultMaxRetries)
	}
}

func TestResolveMaxRetriesWithDefault_ChainFallbackIsZero(t *testing.T) {
	// The chain client uses resolveMaxRetriesWithDefault with fallback=0
	// because chain failover is the retry layer. Verify that branch directly.
	t.Setenv("TRUVAG3_AI_RETRY_ATTEMPTS", "")
	if got := resolveMaxRetriesWithDefault(maxRetriesUnset, 0); got != 0 {
		t.Errorf("chain default fallback: got %d, want 0", got)
	}
}

func TestResolveMaxRetriesWithDefault_EnvVarOverridesChainDefault(t *testing.T) {
	// When the chain default is 0 but the operator sets TRUVAG3_AI_RETRY_ATTEMPTS=2,
	// the env var must override the chain default (precedence: env var > fallback).
	t.Setenv("TRUVAG3_AI_RETRY_ATTEMPTS", "2")
	if got := resolveMaxRetriesWithDefault(maxRetriesUnset, 0); got != 2 {
		t.Errorf("env var should override chain default 0: got %d, want 2", got)
	}
}

func TestResolveMaxRetriesWithDefault_ExplicitOptionBeatsEnvVarAndFallback(t *testing.T) {
	// Explicit option (any value, including 0) wins over both env var and
	// the supplied fallback. This is the path the chain client uses when
	// WithChainMaxRetries(n) is called by the operator.
	t.Setenv("TRUVAG3_AI_RETRY_ATTEMPTS", "2")
	if got := resolveMaxRetriesWithDefault(5, 0); got != 5 {
		t.Errorf("explicit 5 should beat env var and fallback: got %d, want 5", got)
	}
	if got := resolveMaxRetriesWithDefault(0, 99); got != 0 {
		t.Errorf("explicit 0 must be honored even when fallback is 99: got %d, want 0", got)
	}
}

func TestResolveMaxRetries_EnvVarNegativeIgnored(t *testing.T) {
	// Negative values are nonsense and must be ignored, falling back to
	// the framework default. We don't error — env vars are operator-set
	// and we want the framework to keep working with sensible defaults.
	t.Setenv("TRUVAG3_AI_RETRY_ATTEMPTS", "-3")
	if got := resolveMaxRetries(maxRetriesUnset); got != defaultMaxRetries {
		t.Errorf("negative env var must fall back to default: got %d, want %d", got, defaultMaxRetries)
	}
}

func TestResolveMaxRetries_EnvVarInvalidIgnored(t *testing.T) {
	// Non-integer env var means a typo or copy-paste error. Fall back to
	// default rather than crashing.
	t.Setenv("TRUVAG3_AI_RETRY_ATTEMPTS", "five")
	if got := resolveMaxRetries(maxRetriesUnset); got != defaultMaxRetries {
		t.Errorf("invalid env var must fall back to default: got %d, want %d", got, defaultMaxRetries)
	}
}

func TestResolveMaxRetries_NoEnvVarFallsBackToDefault(t *testing.T) {
	t.Setenv("TRUVAG3_AI_RETRY_ATTEMPTS", "")
	if got := resolveMaxRetries(maxRetriesUnset); got != defaultMaxRetries {
		t.Errorf("unset env var should fall back to default: got %d, want %d", got, defaultMaxRetries)
	}
}

// recordingFactory captures the *AIConfig handed to it by NewClient so tests
// can assert on the resolved MaxRetries value (and any other field) without
// having to inspect the returned client. The captured config is the exact
// pointer NewClient passed in, so its MaxRetries reflects whatever the
// resolveMaxRetries() precedence chain decided.
type recordingFactory struct {
	name        string
	priority    int
	available   bool
	lastConfig  *AIConfig
	createCalls int
}

func (f *recordingFactory) Name() string                   { return f.name }
func (f *recordingFactory) Description() string            { return "Recording mock for retry-precedence tests" }
func (f *recordingFactory) Priority() int                  { return f.priority }
func (f *recordingFactory) DetectEnvironment() (int, bool) { return f.priority, f.available }
func (f *recordingFactory) Create(config *AIConfig) core.AIClient {
	f.lastConfig = config
	f.createCalls++
	return &mockAIClient{}
}

// withRecordingFactory installs a recording factory as the only registered
// provider for the duration of the test, restoring the original registry on
// cleanup. Returns the factory so the test can read lastConfig after calling
// NewClient.
func withRecordingFactory(t *testing.T) *recordingFactory {
	t.Helper()
	originalRegistry := registry
	t.Cleanup(func() { registry = originalRegistry })

	factory := &recordingFactory{
		name:      "mock-recording",
		priority:  1000,
		available: true,
	}
	registry = &ProviderRegistry{
		providers: map[string]ProviderFactory{
			"mock-recording": factory,
		},
	}
	return factory
}

// TestNewClient_DefaultMaxRetriesWhenNoEnvVar verifies the end-to-end happy
// path: a vanilla NewClient() call still gets MaxRetries=3, matching the
// historical default. This is the regression guard for any deployment that
// hasn't set the env var and isn't passing WithMaxRetries.
func TestNewClient_DefaultMaxRetriesWhenNoEnvVar(t *testing.T) {
	t.Setenv("TRUVAG3_AI_RETRY_ATTEMPTS", "")
	factory := withRecordingFactory(t)

	if _, err := NewClient(WithProvider("mock-recording")); err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	if factory.lastConfig == nil {
		t.Fatal("factory was not invoked")
	}
	if factory.lastConfig.MaxRetries != defaultMaxRetries {
		t.Errorf("expected default MaxRetries %d, got %d", defaultMaxRetries, factory.lastConfig.MaxRetries)
	}
}

// TestNewClient_EnvVarOverridesDefault verifies that setting
// TRUVAG3_AI_RETRY_ATTEMPTS without passing WithMaxRetries reaches the factory.
func TestNewClient_EnvVarOverridesDefault(t *testing.T) {
	t.Setenv("TRUVAG3_AI_RETRY_ATTEMPTS", "1")
	factory := withRecordingFactory(t)

	if _, err := NewClient(WithProvider("mock-recording")); err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	if factory.lastConfig.MaxRetries != 1 {
		t.Errorf("expected env-resolved MaxRetries 1, got %d", factory.lastConfig.MaxRetries)
	}
}

// TestNewClient_ExplicitOptionBeatsEnvVar verifies that WithMaxRetries(n)
// wins over TRUVAG3_AI_RETRY_ATTEMPTS — the explicit option is the highest-
// precedence layer.
func TestNewClient_ExplicitOptionBeatsEnvVar(t *testing.T) {
	t.Setenv("TRUVAG3_AI_RETRY_ATTEMPTS", "1")
	factory := withRecordingFactory(t)

	if _, err := NewClient(WithProvider("mock-recording"), WithMaxRetries(5)); err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	if factory.lastConfig.MaxRetries != 5 {
		t.Errorf("explicit WithMaxRetries should win over env var: got %d, want 5", factory.lastConfig.MaxRetries)
	}
}

// TestNewClient_ExplicitZeroDisablesRetries verifies the cost-sensitive
// escape hatch: WithMaxRetries(0) means "no retries", not "use default".
// This is the test that catches the silently-ignored-zero bug.
func TestNewClient_ExplicitZeroDisablesRetries(t *testing.T) {
	t.Setenv("TRUVAG3_AI_RETRY_ATTEMPTS", "")
	factory := withRecordingFactory(t)

	if _, err := NewClient(WithProvider("mock-recording"), WithMaxRetries(0)); err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	if factory.lastConfig.MaxRetries != 0 {
		t.Errorf("WithMaxRetries(0) must be honored: got %d, want 0", factory.lastConfig.MaxRetries)
	}
}

func TestNewClientProviderRequestTimeoutDefault(t *testing.T) {
	originalRegistry := registry
	defer func() { registry = originalRegistry }()

	factory := &timeoutDefaultFactory{
		mockFactory:    &mockFactory{name: "provider-timeout", client: &mockAIClient{}},
		defaultTimeout: 60 * time.Minute,
	}
	registry = &ProviderRegistry{providers: map[string]ProviderFactory{
		factory.Name(): factory,
	}}

	if _, err := NewClient(WithProvider(factory.Name())); err != nil {
		t.Fatal(err)
	}
	if factory.lastConfig == nil || factory.lastConfig.Timeout != 60*time.Minute {
		t.Fatalf("provider timeout config = %#v, want 60m", factory.lastConfig)
	}

	if _, err := NewClient(
		WithProvider(factory.Name()),
		WithTimeout(2*time.Minute),
	); err != nil {
		t.Fatal(err)
	}
	if factory.lastConfig.Timeout != 2*time.Minute {
		t.Fatalf("explicit timeout = %s, want 2m", factory.lastConfig.Timeout)
	}

	for _, timeout := range []time.Duration{0, -time.Second} {
		if _, err := NewClient(
			WithProvider(factory.Name()),
			WithTimeout(timeout),
		); err != nil {
			t.Fatal(err)
		}
		if factory.lastConfig.Timeout != 60*time.Minute {
			t.Fatalf("non-positive timeout %s resolved to %s, want provider default 60m", timeout, factory.lastConfig.Timeout)
		}
	}

	factory.defaultTimeout = 0
	if _, err := NewClient(WithProvider(factory.Name())); err == nil ||
		!strings.Contains(err.Error(), "invalid default request timeout") {
		t.Fatalf("invalid provider timeout error = %v", err)
	}
	if _, err := NewClient(
		WithProvider(factory.Name()),
		WithTimeout(time.Minute),
	); err != nil {
		t.Fatalf("explicit timeout should bypass invalid unused provider default: %v", err)
	}
}

func TestNewClientAutoDetectionRetainsFrameworkRequestTimeout(t *testing.T) {
	originalRegistry := registry
	defer func() { registry = originalRegistry }()

	factory := &timeoutDefaultFactory{
		mockFactory: &mockFactory{
			name: "auto-provider-timeout", priority: 100, available: true, client: &mockAIClient{},
		},
		defaultTimeout: 60 * time.Minute,
	}
	registry = &ProviderRegistry{providers: map[string]ProviderFactory{
		factory.Name(): factory,
	}}

	if _, err := NewClient(); err != nil {
		t.Fatal(err)
	}
	if factory.lastConfig == nil || factory.lastConfig.Timeout != defaultRequestTimeout {
		t.Fatalf("auto-detected timeout config = %#v, want %s", factory.lastConfig, defaultRequestTimeout)
	}

	factory.defaultTimeout = 0
	if _, err := NewClient(); err != nil {
		t.Fatalf("auto-detection should not evaluate the unused provider timeout default: %v", err)
	}
}

func TestNewClientRetainsFrameworkRequestTimeoutDefault(t *testing.T) {
	factory := withRecordingFactory(t)
	if _, err := NewClient(WithProvider(factory.Name())); err != nil {
		t.Fatal(err)
	}
	if factory.lastConfig == nil || factory.lastConfig.Timeout != defaultRequestTimeout {
		t.Fatalf("framework timeout config = %#v, want %s", factory.lastConfig, defaultRequestTimeout)
	}
}

func TestNewChainClientUsesFailoverSafeRequestTimeoutUnlessOverridden(t *testing.T) {
	originalRegistry := registry
	defer func() { registry = originalRegistry }()

	factory := &timeoutDefaultFactory{
		mockFactory: &mockFactory{
			name: "chain-provider-timeout", client: &mockAIClient{},
		},
		defaultTimeout: 60 * time.Minute,
	}
	registry = &ProviderRegistry{providers: map[string]ProviderFactory{
		factory.Name(): factory,
	}}

	if _, err := NewChainClient(WithProviderChain(factory.Name())); err != nil {
		t.Fatal(err)
	}
	if factory.lastConfig == nil || factory.lastConfig.Timeout != defaultRequestTimeout {
		t.Fatalf("provider chain timeout config = %#v, want %s", factory.lastConfig, defaultRequestTimeout)
	}

	if _, err := NewChainClient(
		WithProviderChain(factory.Name()),
		WithChainTimeout(2*time.Minute),
	); err != nil {
		t.Fatal(err)
	}
	if factory.lastConfig.Timeout != 2*time.Minute {
		t.Fatalf("explicit chain timeout = %s, want 2m", factory.lastConfig.Timeout)
	}
}
