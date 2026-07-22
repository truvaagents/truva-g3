package ai

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/truvaagents/truva-g3/core"
	"github.com/truvaagents/truva-g3/telemetry"
)

type phase5RequestClient struct {
	generate func(context.Context, *core.AIRequest) (*core.AIResult, error)
	stream   func(context.Context, *core.AIRequest, core.StreamCallback) (*core.AIResult, error)

	legacyCalls    atomic.Int64
	generateCalls  atomic.Int64
	streamCalls    atomic.Int64
	setLoggerCalls atomic.Int64
	setTelemCalls  atomic.Int64
}

type phase8FingerprintClient struct {
	phase5RequestClient
	fingerprint string
	stable      bool
}

func (client *phase8FingerprintClient) RequestFingerprint(context.Context, *core.AIRequest) (string, bool) {
	return client.fingerprint, client.stable
}

func (client *phase5RequestClient) GenerateResponse(
	context.Context,
	string,
	*core.AIOptions,
) (*core.AIResponse, error) {
	client.legacyCalls.Add(1)
	return &core.AIResponse{Content: "legacy"}, nil
}

func TestChainRequestFingerprintRequiresEveryOrderedEntry(t *testing.T) {
	first := &phase8FingerprintClient{fingerprint: "first-v1", stable: true}
	second := &phase8FingerprintClient{fingerprint: "second-v1", stable: true}
	chain, err := NewChain(ClientEntry("first", first), ClientEntry("second", second))
	if err != nil {
		t.Fatal(err)
	}
	request := core.NewAIRequest("prompt", "planning")

	fingerprint, stable := chain.RequestFingerprint(t.Context(), request)
	if !stable || len(fingerprint) != 64 {
		t.Fatalf("chain fingerprint = %q, stable = %t", fingerprint, stable)
	}
	second.fingerprint = "second-v2"
	changed, stable := chain.RequestFingerprint(t.Context(), request)
	if !stable || changed == fingerprint {
		t.Fatalf("entry change did not alter chain fingerprint: first=%q changed=%q", fingerprint, changed)
	}
	second.stable = false
	if _, stable := chain.RequestFingerprint(t.Context(), request); stable {
		t.Fatal("unstable failover entry must make the chain fingerprint unstable")
	}

	legacyChain, err := NewChain(ClientEntry("request", first), ClientEntry("legacy", &phase5LegacyClient{}))
	if err != nil {
		t.Fatal(err)
	}
	if _, stable := legacyChain.RequestFingerprint(t.Context(), request); stable {
		t.Fatal("entry without fingerprint capability must make the chain fingerprint unstable")
	}
	if _, stable := chain.RequestFingerprint(t.Context(), nil); stable {
		t.Fatal("nil chain request must be unstable")
	}
	if _, stable := (&ChainClient{}).RequestFingerprint(t.Context(), request); stable {
		t.Fatal("empty chain must be unstable")
	}
	invalid := core.NewAIRequest("prompt", "planning")
	invalid.Patches = []core.AIProviderPatch{{
		Name:     "invalid",
		Version:  "1",
		Selector: core.AIProviderSelector{AllProviders: true},
		Set:      map[string]interface{}{`/value`: new(int)},
	}}
	if _, stable := chain.RequestFingerprint(t.Context(), invalid); stable {
		t.Fatal("uncloneable chain request must be unstable")
	}
	first.fingerprint = ""
	if _, stable := chain.RequestFingerprint(t.Context(), request); stable {
		t.Fatal("empty entry fingerprint must make the chain fingerprint unstable")
	}
}

func TestChainRequestFingerprintMatchesSuccessfulExecutionReport(t *testing.T) {
	firstErr := errors.New("primary unavailable")
	first := &phase8FingerprintClient{fingerprint: "first-v1", stable: true}
	first.generate = func(context.Context, *core.AIRequest) (*core.AIResult, error) {
		return nil, firstErr
	}
	first.stream = func(context.Context, *core.AIRequest, core.StreamCallback) (*core.AIResult, error) {
		return nil, firstErr
	}
	second := &phase8FingerprintClient{fingerprint: "second-v1", stable: true}
	second.generate = func(context.Context, *core.AIRequest) (*core.AIResult, error) {
		return &core.AIResult{
			Response: &core.AIResponse{Content: "backup"},
			RequestReport: &core.AIRequestReport{
				Provider: "backup", Fingerprint: "second-v1", Stable: true,
			},
		}, nil
	}
	second.stream = func(_ context.Context, _ *core.AIRequest, callback core.StreamCallback) (*core.AIResult, error) {
		if err := callback(core.StreamChunk{Content: "backup", Delta: true}); err != nil {
			return nil, err
		}
		return &core.AIResult{
			Response: &core.AIResponse{Content: "backup"},
			RequestReport: &core.AIRequestReport{
				Provider: "backup", Fingerprint: "second-v1", Stable: true,
			},
		}, nil
	}
	chain, err := NewChain(ClientEntry("first", first), ClientEntry("second", second))
	if err != nil {
		t.Fatal(err)
	}
	request := core.NewAIRequest("prompt", "planning")
	want, stable := chain.RequestFingerprint(t.Context(), request)
	if !stable || want == "" {
		t.Fatalf("preflight fingerprint = %q, stable = %t", want, stable)
	}

	result, err := chain.Generate(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	if result.RequestReport == nil || !result.RequestReport.Stable || result.RequestReport.Fingerprint != want {
		t.Fatalf("generate report = %#v, want chain fingerprint %q", result.RequestReport, want)
	}

	result, err = chain.Stream(t.Context(), request, func(core.StreamChunk) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	if result.RequestReport == nil || !result.RequestReport.Stable || result.RequestReport.Fingerprint != want {
		t.Fatalf("stream report = %#v, want chain fingerprint %q", result.RequestReport, want)
	}

	second.generate = func(context.Context, *core.AIRequest) (*core.AIResult, error) {
		return &core.AIResult{
			Response:      &core.AIResponse{Content: "drifted"},
			RequestReport: &core.AIRequestReport{Fingerprint: "second-v2", Stable: true},
		}, nil
	}
	result, err = chain.Generate(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	if result.RequestReport == nil || result.RequestReport.Stable || result.RequestReport.Fingerprint != "" {
		t.Fatalf("drifted provider report must make the chain unstable: %#v", result.RequestReport)
	}
}

func (client *phase5RequestClient) Generate(
	ctx context.Context,
	request *core.AIRequest,
) (*core.AIResult, error) {
	client.generateCalls.Add(1)
	if client.generate != nil {
		return client.generate(ctx, request)
	}
	return &core.AIResult{Response: &core.AIResponse{Content: "request"}}, nil
}

func (client *phase5RequestClient) Stream(
	ctx context.Context,
	request *core.AIRequest,
	callback core.StreamCallback,
) (*core.AIResult, error) {
	client.streamCalls.Add(1)
	if client.stream != nil {
		return client.stream(ctx, request, callback)
	}
	if err := callback(core.StreamChunk{Content: "request", Delta: true}); err != nil {
		return nil, err
	}
	return &core.AIResult{Response: &core.AIResponse{Content: "request"}}, nil
}

func (client *phase5RequestClient) SetLogger(core.Logger) {
	client.setLoggerCalls.Add(1)
}

func (client *phase5RequestClient) SetTelemetry(core.Telemetry) {
	client.setTelemCalls.Add(1)
}

type phase5LegacyClient struct {
	calls          atomic.Int64
	setLoggerCalls atomic.Int64
	setTelemCalls  atomic.Int64
	response       *core.AIResponse
	err            error
}

func (client *phase5LegacyClient) GenerateResponse(
	context.Context,
	string,
	*core.AIOptions,
) (*core.AIResponse, error) {
	client.calls.Add(1)
	return client.response, client.err
}

func (client *phase5LegacyClient) SetLogger(core.Logger) {
	client.setLoggerCalls.Add(1)
}

func (client *phase5LegacyClient) SetTelemetry(core.Telemetry) {
	client.setTelemCalls.Add(1)
}

type phase5ValueClient struct{}

func (phase5ValueClient) GenerateResponse(
	context.Context,
	string,
	*core.AIOptions,
) (*core.AIResponse, error) {
	return &core.AIResponse{Content: "value client"}, nil
}

type phase5LegacyStreamingClient struct {
	phase5LegacyClient
	streamCalls atomic.Int64
	stream      func(context.Context, string, *core.AIOptions, core.StreamCallback) (*core.AIResponse, error)
}

func (client *phase5LegacyStreamingClient) StreamResponse(
	ctx context.Context,
	prompt string,
	options *core.AIOptions,
	callback core.StreamCallback,
) (*core.AIResponse, error) {
	client.streamCalls.Add(1)
	return client.stream(ctx, prompt, options, callback)
}

func (*phase5LegacyStreamingClient) SupportsStreaming() bool { return true }

type phase5RequestFactory struct {
	name  string
	build func(int, *AIConfig, ProviderIntegrationConfig) *phase5RequestClient

	mu           sync.Mutex
	configs      []*AIConfig
	integrations []ProviderIntegrationConfig
	clients      []*phase5RequestClient
}

func (factory *phase5RequestFactory) Name() string           { return factory.name }
func (*phase5RequestFactory) Description() string            { return "phase 5 request factory" }
func (*phase5RequestFactory) DetectEnvironment() (int, bool) { return 1, true }
func (*phase5RequestFactory) Create(*AIConfig) core.AIClient { return &phase5LegacyClient{} }
func (factory *phase5RequestFactory) CreateRequestClient(
	config *AIConfig,
	integration ProviderIntegrationConfig,
) (core.AIRequestClient, error) {
	factory.mu.Lock()
	defer factory.mu.Unlock()
	index := len(factory.clients)
	client := &phase5RequestClient{}
	if factory.build != nil {
		client = factory.build(index, config, integration)
	}
	factory.configs = append(factory.configs, config)
	factory.integrations = append(factory.integrations, integration)
	factory.clients = append(factory.clients, client)
	return client, nil
}

type phase5CredentialSource struct{ name string }

func (source *phase5CredentialSource) Credential(
	context.Context,
	CredentialRequest,
) (HeaderCredential, error) {
	return NewHeaderCredential("Authorization", "Bearer "+source.name), nil
}

type phase5EndpointResolver struct{ name string }

func (resolver *phase5EndpointResolver) ResolveEndpoint(
	context.Context,
	EndpointRequest,
) (ResolvedEndpoint, error) {
	return ResolvedEndpoint{
		URL:           &url.URL{Scheme: "https", Host: "gateway.example", Path: "/messages"},
		RouteIdentity: resolver.name,
	}, nil
}

type phase5TraceKey struct{}

type phase5Telemetry struct{}

func (*phase5Telemetry) StartSpan(ctx context.Context, name string) (context.Context, core.Span) {
	return context.WithValue(ctx, phase5TraceKey{}, name), &core.NoOpSpan{}
}

func (*phase5Telemetry) RecordMetric(string, float64, map[string]string) {}

type nilReturningChainTelemetry struct{}

func (nilReturningChainTelemetry) StartSpan(context.Context, string) (context.Context, core.Span) {
	return nil, nil
}
func (nilReturningChainTelemetry) RecordMetric(string, float64, map[string]string) {}

func TestChainStartRequestSpanIsNilSafe(t *testing.T) {
	for _, test := range []struct {
		name      string
		telemetry core.Telemetry
	}{
		{name: "nil telemetry"},
		{name: "nil-returning telemetry", telemetry: nilReturningChainTelemetry{}},
	} {
		t.Run(test.name, func(t *testing.T) {
			client := &ChainClient{telemetry: test.telemetry}
			ctx, span := client.startRequestSpan(nil, "ai.chain.nil-safe")
			if ctx == nil || span == nil {
				t.Fatalf("startRequestSpan returned context=%#v span=%#v", ctx, span)
			}
			span.SetAttribute("test", true)
			span.RecordError(errors.New("safe"))
			span.End()
		})
	}
}

type chainObservationLogStore struct {
	components []string
	levels     []string
	messages   []string
	fields     []map[string]interface{}
}

type chainObservationLogger struct {
	store     *chainObservationLogStore
	component string
}

func (logger *chainObservationLogger) WithComponent(component string) core.Logger {
	return &chainObservationLogger{store: logger.store, component: component}
}
func (logger *chainObservationLogger) record(level, message string, fields map[string]interface{}) {
	logger.store.components = append(logger.store.components, logger.component)
	logger.store.levels = append(logger.store.levels, level)
	logger.store.messages = append(logger.store.messages, message)
	logger.store.fields = append(logger.store.fields, fields)
}
func (logger *chainObservationLogger) Debug(message string, fields map[string]interface{}) {
	logger.record("DEBUG", message, fields)
}
func (logger *chainObservationLogger) Info(message string, fields map[string]interface{}) {
	logger.record("INFO", message, fields)
}
func (logger *chainObservationLogger) Warn(message string, fields map[string]interface{}) {
	logger.record("WARN", message, fields)
}
func (logger *chainObservationLogger) Error(message string, fields map[string]interface{}) {
	logger.record("ERROR", message, fields)
}
func (logger *chainObservationLogger) DebugWithContext(_ context.Context, message string, fields map[string]interface{}) {
	logger.record("DEBUG", message, fields)
}
func (logger *chainObservationLogger) InfoWithContext(_ context.Context, message string, fields map[string]interface{}) {
	logger.record("INFO", message, fields)
}
func (logger *chainObservationLogger) WarnWithContext(_ context.Context, message string, fields map[string]interface{}) {
	logger.record("WARN", message, fields)
}
func (logger *chainObservationLogger) ErrorWithContext(_ context.Context, message string, fields map[string]interface{}) {
	logger.record("ERROR", message, fields)
}

func findChainObservationLog(
	t *testing.T,
	store *chainObservationLogStore,
	operation string,
) (string, map[string]interface{}) {
	t.Helper()
	for index, fields := range store.fields {
		if fields["operation"] == operation {
			return store.levels[index], fields
		}
	}
	t.Fatalf("operation %q not found in logs: %#v", operation, store.fields)
	return "", nil
}

func requireChainSanitizedErrorLog(
	t *testing.T,
	store *chainObservationLogStore,
	operation string,
	wantLevel string,
	wantErrorType string,
) map[string]interface{} {
	t.Helper()
	level, fields := findChainObservationLog(t, store, operation)
	if level != wantLevel {
		t.Errorf("%s level = %q, want %q", operation, level, wantLevel)
	}
	if fields["error_type"] != wantErrorType {
		t.Errorf("%s error_type = %#v, want %q", operation, fields["error_type"], wantErrorType)
	}
	wantError := "AI provider request failed: " + wantErrorType
	if fields["error"] != wantError {
		t.Errorf("%s error = %#v, want %q", operation, fields["error"], wantError)
	}
	return fields
}

type chainObservationTelemetry struct {
	names []string
	spans []*chainObservationSpan
}

func (tracing *chainObservationTelemetry) StartSpan(ctx context.Context, name string) (context.Context, core.Span) {
	span := &chainObservationSpan{attributes: map[string]interface{}{}}
	tracing.names = append(tracing.names, name)
	tracing.spans = append(tracing.spans, span)
	return ctx, span
}
func (*chainObservationTelemetry) RecordMetric(string, float64, map[string]string) {}

type chainObservationSpan struct {
	attributes map[string]interface{}
	errors     []error
	ended      int
}

func (span *chainObservationSpan) End() { span.ended++ }
func (span *chainObservationSpan) SetAttribute(key string, value interface{}) {
	span.attributes[key] = value
}
func (span *chainObservationSpan) RecordError(err error) { span.errors = append(span.errors, err) }

func TestChainObservabilitySanitizesErrorsAndCorrelatesRequests(t *testing.T) {
	const (
		requestID    = "chain-observation-request"
		promptSecret = "chain-observation-prompt-secret"
		firstSecret  = "chain-first-error-secret"
		secondSecret = "chain-second-error-secret"
	)
	firstErr := errors.New(firstSecret)
	secondErr := errors.New(secondSecret)
	first := &phase5RequestClient{generate: func(context.Context, *core.AIRequest) (*core.AIResult, error) {
		return nil, firstErr
	}}
	second := &phase5RequestClient{generate: func(context.Context, *core.AIRequest) (*core.AIResult, error) {
		return nil, secondErr
	}}
	chain, err := NewChain(ClientEntry("first", first), ClientEntry("second", second))
	if err != nil {
		t.Fatalf("NewChain returned error: %v", err)
	}
	store := &chainObservationLogStore{}
	logger := &chainObservationLogger{store: store}
	tracing := &chainObservationTelemetry{}
	chain.SetLogger(logger)
	chain.SetTelemetry(tracing)
	ctx := telemetry.WithBaggage(context.Background(), "request_id", requestID)

	_, err = chain.Generate(ctx, core.NewAIRequest(promptSecret, "observation"))
	if !errors.Is(err, firstErr) || !errors.Is(err, secondErr) {
		t.Fatalf("Generate error = %v, want both original causes", err)
	}
	if !reflect.DeepEqual(tracing.names, []string{
		"ai.chain.generate",
		"ai.chain.provider_attempt",
		"ai.chain.provider_attempt",
	}) {
		t.Fatalf("span hierarchy = %#v", tracing.names)
	}
	for index, fields := range store.fields {
		if store.components[index] != "framework/ai" {
			t.Errorf("log %d component = %q", index, store.components[index])
		}
		if fields["operation"] == "" || fields["request_id"] != requestID {
			t.Errorf("log %d fields = %#v", index, fields)
		}
	}
	requireChainSanitizedErrorLog(t, store, "ai_chain_provider_failed", "WARN", "unknown")
	requireChainSanitizedErrorLog(t, store, "ai_chain_exhausted", "ERROR", "unknown")
	var observed strings.Builder
	fmt.Fprint(&observed, store.fields)
	for index, span := range tracing.spans {
		if span.ended != 1 {
			t.Errorf("span %d ended %d times", index, span.ended)
		}
		if span.attributes["request_id"] != requestID {
			t.Errorf("span %d request_id = %#v", index, span.attributes["request_id"])
		}
		fmt.Fprint(&observed, span.attributes)
		for _, spanErr := range span.errors {
			fmt.Fprint(&observed, spanErr.Error())
			if spanErr.Error() != "AI provider request failed: unknown" {
				t.Errorf("span %d error = %q", index, spanErr.Error())
			}
		}
	}
	for _, forbidden := range []string{promptSecret, firstSecret, secondSecret} {
		if strings.Contains(observed.String(), forbidden) {
			t.Fatalf("chain observations leaked %q: %s", forbidden, observed.String())
		}
	}
}

func TestChainFailureLogsUseSanitizedErrorContract(t *testing.T) {
	const requestID = "chain-error-log-request"
	ctx := telemetry.WithBaggage(context.Background(), "request_id", requestID)
	uncloneableRequest := func(secret string) *core.AIRequest {
		request := core.NewAIRequest("prompt", "observation")
		request.Patches = []core.AIProviderPatch{{
			Name:     secret,
			Version:  "1",
			Selector: core.AIProviderSelector{AllProviders: true},
			Set:      map[string]interface{}{`/` + secret: new(int)},
		}}
		return request
	}

	t.Run("request clone failures", func(t *testing.T) {
		for _, test := range []struct {
			name      string
			operation string
			spanName  string
			invoke    func(*ChainClient, *core.AIRequest) error
		}{
			{
				name:      "generate",
				operation: "ai_chain_request_error",
				spanName:  "ai.chain.generate",
				invoke: func(chain *ChainClient, request *core.AIRequest) error {
					_, err := chain.Generate(ctx, request)
					return err
				},
			},
			{
				name:      "stream",
				operation: "ai_chain_stream_request_error",
				spanName:  "ai.chain.stream",
				invoke: func(chain *ChainClient, request *core.AIRequest) error {
					_, err := chain.Stream(ctx, request, func(core.StreamChunk) error {
						return errors.New("callback called for an uncloneable request")
					})
					return err
				},
			},
		} {
			t.Run(test.name, func(t *testing.T) {
				secret := "chain-clone-" + test.name + "-secret"
				chain, err := NewChain(ClientEntry("first", &phase5RequestClient{}))
				if err != nil {
					t.Fatal(err)
				}
				store := &chainObservationLogStore{}
				tracing := &chainObservationTelemetry{}
				chain.SetLogger(&chainObservationLogger{store: store})
				chain.SetTelemetry(tracing)

				err = test.invoke(chain, uncloneableRequest(secret))
				if err == nil || !strings.Contains(err.Error(), secret) {
					t.Fatalf("caller error = %v, want original clone diagnostic", err)
				}
				fields := requireChainSanitizedErrorLog(t, store, test.operation, "ERROR", "invalid_request")
				if fields["request_id"] != requestID {
					t.Fatalf("clone failure fields = %#v", fields)
				}
				if !reflect.DeepEqual(tracing.names, []string{test.spanName}) || len(tracing.spans) != 1 ||
					len(tracing.spans[0].errors) != 1 || tracing.spans[0].ended != 1 {
					t.Fatalf("clone failure spans names=%#v spans=%#v", tracing.names, tracing.spans)
				}
				if tracing.spans[0].errors[0].Error() != "AI provider request failed: invalid_request" ||
					tracing.spans[0].attributes["ai.error_type"] != "invalid_request" {
					t.Fatalf("clone failure span = %#v", tracing.spans[0])
				}
				observed := fmt.Sprint(store.fields, tracing.spans[0].attributes, tracing.spans[0].errors)
				if strings.Contains(observed, secret) {
					t.Fatalf("clone failure observations leaked diagnostic: %s", observed)
				}
			})
		}
	})

	t.Run("non-retryable generate abort", func(t *testing.T) {
		const secret = "chain-abort-provider-secret"
		wantErr := &testProviderError{statusCode: 400, message: secret}
		first := &phase5RequestClient{generate: func(context.Context, *core.AIRequest) (*core.AIResult, error) {
			return nil, wantErr
		}}
		chain, err := NewChain(ClientEntry("first", first))
		if err != nil {
			t.Fatal(err)
		}
		store := &chainObservationLogStore{}
		chain.SetLogger(&chainObservationLogger{store: store})

		_, err = chain.Generate(ctx, core.NewAIRequest("prompt", "observation"))
		if !errors.Is(err, wantErr) {
			t.Fatalf("Generate error = %v, want preserved provider error", err)
		}
		fields := requireChainSanitizedErrorLog(t, store, "ai_chain_abort", "ERROR", "provider_client")
		if fields["request_id"] != requestID || strings.Contains(fmt.Sprint(fields), secret) {
			t.Fatalf("abort fields = %#v", fields)
		}
	})

	t.Run("transient provider failover", func(t *testing.T) {
		const secret = "chain-transient-provider-secret"
		wantErr := &testProviderError{statusCode: 503, message: secret, transient: true}
		first := &phase5RequestClient{generate: func(context.Context, *core.AIRequest) (*core.AIResult, error) {
			return nil, wantErr
		}}
		second := &phase5RequestClient{}
		chain, err := NewChain(ClientEntry("first", first), ClientEntry("second", second))
		if err != nil {
			t.Fatal(err)
		}
		store := &chainObservationLogStore{}
		chain.SetLogger(&chainObservationLogger{store: store})

		if _, err := chain.Generate(ctx, core.NewAIRequest("prompt", "observation")); err != nil {
			t.Fatalf("Generate returned error: %v", err)
		}
		for _, operation := range []string{"chain_failover", "ai_chain_provider_failed"} {
			fields := requireChainSanitizedErrorLog(t, store, operation, "WARN", "transport")
			if fields["request_id"] != requestID || strings.Contains(fmt.Sprint(fields), secret) {
				t.Fatalf("%s fields = %#v", operation, fields)
			}
		}
		_, providerFields := findChainObservationLog(t, store, "ai_chain_provider_failed")
		if providerFields["failover_reason"] != "upstream_5xx" || providerFields["failure_type"] != nil {
			t.Fatalf("provider failover classification = %#v", providerFields)
		}
	})

	t.Run("retryable provider failover", func(t *testing.T) {
		const secret = "chain-retryable-provider-secret"
		wantErr := &testProviderError{statusCode: 429, message: secret, retryable: true}
		first := &phase5RequestClient{generate: func(context.Context, *core.AIRequest) (*core.AIResult, error) {
			return nil, wantErr
		}}
		second := &phase5RequestClient{}
		chain, err := NewChain(ClientEntry("first", first), ClientEntry("second", second))
		if err != nil {
			t.Fatal(err)
		}
		store := &chainObservationLogStore{}
		chain.SetLogger(&chainObservationLogger{store: store})

		if _, err := chain.Generate(ctx, core.NewAIRequest("prompt", "observation")); err != nil {
			t.Fatalf("Generate returned error: %v", err)
		}
		for _, operation := range []string{"chain_failover_retryable", "ai_chain_provider_failed"} {
			fields := requireChainSanitizedErrorLog(t, store, operation, "WARN", "provider_rate_limit")
			if fields["request_id"] != requestID || strings.Contains(fmt.Sprint(fields), secret) {
				t.Fatalf("%s fields = %#v", operation, fields)
			}
		}
		_, providerFields := findChainObservationLog(t, store, "ai_chain_provider_failed")
		if providerFields["failover_reason"] != "rate_limit" || providerFields["failure_type"] != nil {
			t.Fatalf("retryable failover classification = %#v", providerFields)
		}
	})

	t.Run("stream abort and exhaustion", func(t *testing.T) {
		const (
			abortSecret      = "chain-stream-abort-secret"
			exhaustionSecret = "chain-stream-exhaustion-secret"
		)
		t.Run("abort", func(t *testing.T) {
			wantErr := &testProviderError{statusCode: 400, message: abortSecret}
			first := &phase5RequestClient{stream: func(context.Context, *core.AIRequest, core.StreamCallback) (*core.AIResult, error) {
				return nil, wantErr
			}}
			chain, err := NewChain(ClientEntry("first", first))
			if err != nil {
				t.Fatal(err)
			}
			store := &chainObservationLogStore{}
			chain.SetLogger(&chainObservationLogger{store: store})
			_, err = chain.Stream(ctx, core.NewAIRequest("prompt", "observation"), func(core.StreamChunk) error { return nil })
			if !errors.Is(err, wantErr) {
				t.Fatalf("Stream error = %v, want preserved provider error", err)
			}
			fields := requireChainSanitizedErrorLog(t, store, "ai_chain_stream_abort", "ERROR", "provider_client")
			if strings.Contains(fmt.Sprint(fields), abortSecret) {
				t.Fatalf("stream abort leaked provider error: %#v", fields)
			}
		})

		t.Run("exhaustion", func(t *testing.T) {
			firstErr := errors.New(exhaustionSecret + "-first")
			secondErr := errors.New(exhaustionSecret + "-second")
			first := &phase5RequestClient{stream: func(context.Context, *core.AIRequest, core.StreamCallback) (*core.AIResult, error) {
				return nil, firstErr
			}}
			second := &phase5RequestClient{stream: func(context.Context, *core.AIRequest, core.StreamCallback) (*core.AIResult, error) {
				return nil, secondErr
			}}
			chain, err := NewChain(ClientEntry("first", first), ClientEntry("second", second))
			if err != nil {
				t.Fatal(err)
			}
			store := &chainObservationLogStore{}
			chain.SetLogger(&chainObservationLogger{store: store})
			_, err = chain.Stream(ctx, core.NewAIRequest("prompt", "observation"), func(core.StreamChunk) error { return nil })
			if !errors.Is(err, firstErr) || !errors.Is(err, secondErr) {
				t.Fatalf("Stream error = %v, want both causes", err)
			}
			failoverFields := requireChainSanitizedErrorLog(t, store, "ai_chain_stream_failover", "WARN", "unknown")
			if failoverFields["failover_reason"] != "unknown" || failoverFields["failure_type"] != nil {
				t.Fatalf("stream failover fields = %#v", failoverFields)
			}
			exhaustedFields := requireChainSanitizedErrorLog(t, store, "ai_chain_stream_exhausted", "ERROR", "unknown")
			if exhaustedFields["request_id"] != requestID || strings.Contains(fmt.Sprint(store.fields), exhaustionSecret) {
				t.Fatalf("stream exhaustion logs = %#v", store.fields)
			}
		})
	})
}

func TestNewChain_Phase5ValidatesEntries(t *testing.T) {
	var typedNil *phase5LegacyClient
	tests := []struct {
		name    string
		entries []ChainEntry
		want    string
	}{
		{name: "empty", want: "at least one entry"},
		{
			name:    "duplicate names",
			entries: []ChainEntry{ClientEntry("same", &phase5LegacyClient{}), ClientEntry("same", &phase5LegacyClient{})},
			want:    "duplicate name",
		},
		{name: "empty name", entries: []ChainEntry{ClientEntry("", &phase5LegacyClient{})}, want: "name is empty"},
		{name: "whitespace name", entries: []ChainEntry{ClientEntry(" bad", &phase5LegacyClient{})}, want: "surrounding whitespace"},
		{name: "control name", entries: []ChainEntry{ClientEntry("bad\nname", &phase5LegacyClient{})}, want: "control characters"},
		{name: "oversized name", entries: []ChainEntry{ClientEntry(strings.Repeat("n", 257), &phase5LegacyClient{})}, want: "exceeds 256 bytes"},
		{name: "nil client", entries: []ChainEntry{ClientEntry("nil", nil)}, want: "nil client"},
		{name: "typed nil client", entries: []ChainEntry{ClientEntry("nil", typedNil)}, want: "nil client"},
		{name: "empty provider alias", entries: []ChainEntry{ProviderEntry("provider", "")}, want: "provider alias is empty"},
		{name: "empty legacy provider alias", entries: []ChainEntry{legacyProviderEntry("legacy", "")}, want: "provider alias is empty"},
		{name: "nil provider option", entries: []ChainEntry{ProviderEntry("provider", "missing", nil)}, want: "client option 0 is nil"},
		{name: "invalid entry kind", entries: []ChainEntry{{name: "invalid", kind: chainEntryKind(99)}}, want: "invalid kind"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := NewChain(test.entries...)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("NewChain error = %v, want substring %q", err, test.want)
			}
		})
	}

	if _, err := NewChain(ClientEntry("value", phase5ValueClient{})); err != nil {
		t.Fatalf("NewChain rejected value client: %v", err)
	}
}

func TestNewChain_Phase5ProviderEntryRequiresRequestCapableFactory(t *testing.T) {
	factory := &phase3LegacyFactory{name: "phase5-legacy-only", client: &phase3LegacyClient{}}
	installPhase3Factory(t, factory)

	_, err := NewChain(ProviderEntry("legacy-only", factory.name))
	if !errors.Is(err, core.ErrAIRequestFeatureUnsupported) {
		t.Fatalf("NewChain error = %v, want request capability error", err)
	}
}

func TestNewChain_Phase5MaterializesIndependentProviderEntries(t *testing.T) {
	firstFailure := errors.New("primary unavailable")
	factory := &phase5RequestFactory{name: "phase5-provider"}
	factory.build = func(index int, config *AIConfig, _ ProviderIntegrationConfig) *phase5RequestClient {
		if index == 0 {
			return &phase5RequestClient{generate: func(_ context.Context, request *core.AIRequest) (*core.AIResult, error) {
				request.Prompt = "mutated"
				request.Generation.Model = "mutated-model"
				request.Patches[0].Set["/nested"].(map[string]interface{})["value"] = "mutated"
				return &core.AIResult{RequestReport: &core.AIRequestReport{Provider: config.Provider, Stable: false}}, firstFailure
			}}
		}
		return &phase5RequestClient{generate: func(_ context.Context, request *core.AIRequest) (*core.AIResult, error) {
			if request.Prompt != "original" || request.Generation.Model != "portable" {
				return nil, fmt.Errorf("backup observed mutated request: %#v", request)
			}
			nested := request.Patches[0].Set["/nested"].(map[string]interface{})
			if nested["value"] != "original" {
				return nil, fmt.Errorf("backup observed mutated patch: %#v", nested)
			}
			return &core.AIResult{
				Response: &core.AIResponse{Content: config.Model, Provider: config.ProviderAlias, Model: config.Model},
				RequestReport: &core.AIRequestReport{
					Provider:      config.Provider,
					ProviderAlias: config.ProviderAlias,
					ResolvedModel: config.Model,
					Fingerprint:   "provider-fingerprint",
					Stable:        true,
					Adjustments:   []core.AIRequestAdjustment{{Source: "provider", Rule: "kept"}},
				},
			}, nil
		}}
	}
	installPhase3Factory(t, factory)

	primaryCredential := &phase5CredentialSource{name: "primary"}
	backupCredential := &phase5CredentialSource{name: "backup"}
	primaryResolver := &phase5EndpointResolver{name: "primary-route"}
	backupResolver := &phase5EndpointResolver{name: "backup-route"}
	primaryOptions := []ClientOption{
		WithProvider("wrong-provider"),
		WithProviderAlias("wrong.alias"),
		WithModel("primary-model"),
		WithCredentialSource(primaryCredential),
		WithEndpointResolver(primaryResolver),
		WithRequestRules(core.AIProviderPatch{
			Name: "primary-rule", Version: "1", Selector: core.AIProviderSelector{AllProviders: true}, Set: map[string]interface{}{"/primary": true},
		}),
	}
	primary := ProviderEntry("primary", factory.name, primaryOptions...)
	primaryOptions[0] = nil
	backup := ProviderEntry(
		"backup",
		factory.name,
		WithModel("backup-model"),
		WithCredentialSource(backupCredential),
		WithEndpointResolver(backupResolver),
	)

	chain, err := NewChain(primary, backup)
	if err != nil {
		t.Fatalf("NewChain returned error: %v", err)
	}
	request := core.NewAIRequest("original", "phase-5")
	request.Generation.Model = "portable"
	request.Patches = []core.AIProviderPatch{{
		Name: "request-rule", Selector: core.AIProviderSelector{AllProviders: true}, Set: map[string]interface{}{
			"/nested": map[string]interface{}{"value": "original"},
		},
	}}
	result, err := chain.Generate(t.Context(), request)
	if err != nil {
		t.Fatalf("Generate returned error: %v", err)
	}
	if result.Response == nil || result.Response.Content != "backup-model" {
		t.Fatalf("result = %#v", result)
	}
	if request.Prompt != "original" || request.Generation.Model != "portable" ||
		request.Patches[0].Set["/nested"].(map[string]interface{})["value"] != "original" {
		t.Fatalf("input request was mutated: %#v", request)
	}

	factory.mu.Lock()
	defer factory.mu.Unlock()
	if len(factory.configs) != 2 || len(factory.clients) != 2 {
		t.Fatalf("materialized configs=%d clients=%d", len(factory.configs), len(factory.clients))
	}
	if factory.clients[0].generateCalls.Load() != 1 || factory.clients[1].generateCalls.Load() != 1 {
		t.Fatalf("entry calls primary=%d backup=%d", factory.clients[0].generateCalls.Load(), factory.clients[1].generateCalls.Load())
	}
	for index, wantModel := range []string{"primary-model", "backup-model"} {
		if factory.configs[index].Provider != factory.name || factory.configs[index].ProviderAlias != factory.name || factory.configs[index].Model != wantModel {
			t.Fatalf("entry %d config = %#v", index, factory.configs[index])
		}
	}
	if factory.integrations[0].CredentialSource != primaryCredential || factory.integrations[1].CredentialSource != backupCredential ||
		factory.integrations[0].EndpointResolver != primaryResolver || factory.integrations[1].EndpointResolver != backupResolver ||
		len(factory.integrations[0].RequestRules) != 1 || len(factory.integrations[1].RequestRules) != 0 {
		t.Fatalf("entry integrations = %#v", factory.integrations)
	}
	if result.RequestReport.Provider != factory.name || result.RequestReport.Fingerprint != "" || result.RequestReport.Stable {
		t.Fatalf("chain without stable entry fingerprints exposed a cache-safe report: %#v", result.RequestReport)
	}
	adjustments := result.RequestReport.Adjustments
	if len(adjustments) != 2 || adjustments[0].Source != "provider" || adjustments[1].Source != "chain" ||
		adjustments[1].Rule != "backup" || adjustments[1].Reason != "chain attempt 2" {
		t.Fatalf("chain report adjustments = %#v", adjustments)
	}
}

func TestChainClient_Phase5PreservesFailureReportsAndJoinedCauses(t *testing.T) {
	firstErr := errors.New("first failure")
	secondErr := errors.New("second failure")
	first := &phase5RequestClient{generate: func(context.Context, *core.AIRequest) (*core.AIResult, error) {
		return nil, firstErr
	}}
	second := &phase5RequestClient{generate: func(context.Context, *core.AIRequest) (*core.AIResult, error) {
		return &core.AIResult{RequestReport: &core.AIRequestReport{
			Provider: "second-provider", Surface: "messages", Fingerprint: "kept", Stable: true,
		}}, secondErr
	}}
	chain, err := NewChain(ClientEntry("first", first), ClientEntry("second", second))
	if err != nil {
		t.Fatalf("NewChain returned error: %v", err)
	}

	result, err := chain.Generate(t.Context(), core.NewAIRequest("prompt", "phase-5-report"))
	if !errors.Is(err, firstErr) || !errors.Is(err, secondErr) {
		t.Fatalf("joined error does not preserve causes: %v", err)
	}
	if !strings.Contains(err.Error(), `entry "first" attempt 1`) || !strings.Contains(err.Error(), `entry "second" attempt 2`) {
		t.Fatalf("joined error lacks entry context: %v", err)
	}
	if result == nil || result.RequestReport == nil || result.RequestReport.Provider != "second-provider" ||
		result.RequestReport.Surface != "messages" || result.RequestReport.Fingerprint != "kept" || !result.RequestReport.Stable {
		t.Fatalf("failure report was not preserved: %#v", result)
	}
	last := result.RequestReport.Adjustments[len(result.RequestReport.Adjustments)-1]
	if last.Source != "chain" || last.Rule != "second" || last.Reason != "chain attempt 2" {
		t.Fatalf("failure report chain context = %#v", last)
	}
}

func TestChainClient_Phase5LegacyCapabilityRefusalFailsOverWithoutCallingLegacyClient(t *testing.T) {
	legacy := &phase5LegacyClient{response: &core.AIResponse{Content: "legacy"}}
	requestClient := &phase5RequestClient{generate: func(context.Context, *core.AIRequest) (*core.AIResult, error) {
		return &core.AIResult{Response: &core.AIResponse{Content: "request"}}, nil
	}}
	chain, err := NewChain(ClientEntry("legacy", legacy), ClientEntry("request", requestClient))
	if err != nil {
		t.Fatalf("NewChain returned error: %v", err)
	}
	request := core.NewAIRequest("prompt", "phase-5-capability")
	request.Patches = []core.AIProviderPatch{{Name: "advanced", Selector: core.AIProviderSelector{AllProviders: true}}}

	result, err := chain.Generate(t.Context(), request)
	if err != nil || result.Response == nil || result.Response.Content != "request" {
		t.Fatalf("Generate result=%#v err=%v", result, err)
	}
	if legacy.calls.Load() != 0 || requestClient.generateCalls.Load() != 1 {
		t.Fatalf("legacy calls=%d request calls=%d", legacy.calls.Load(), requestClient.generateCalls.Load())
	}
}

func TestChainClient_Phase5LegacyAdaptersUseRequestAwareLoop(t *testing.T) {
	requestClient := &phase5RequestClient{
		generate: func(context.Context, *core.AIRequest) (*core.AIResult, error) {
			return &core.AIResult{Response: &core.AIResponse{Content: "generated"}}, nil
		},
		stream: func(_ context.Context, _ *core.AIRequest, callback core.StreamCallback) (*core.AIResult, error) {
			if err := callback(core.StreamChunk{Content: "streamed", Delta: true}); err != nil {
				return nil, err
			}
			return &core.AIResult{Response: &core.AIResponse{Content: "streamed"}}, nil
		},
	}
	chain, err := NewChain(ClientEntry("request", requestClient))
	if err != nil {
		t.Fatalf("NewChain returned error: %v", err)
	}
	response, err := chain.GenerateResponse(t.Context(), "prompt", nil)
	if err != nil || response == nil || response.Content != "generated" {
		t.Fatalf("GenerateResponse response=%#v err=%v", response, err)
	}
	streamed, err := chain.StreamResponse(t.Context(), "prompt", nil, func(core.StreamChunk) error { return nil })
	if err != nil || streamed == nil || streamed.Content != "streamed" {
		t.Fatalf("StreamResponse response=%#v err=%v", streamed, err)
	}
	if requestClient.generateCalls.Load() != 1 || requestClient.streamCalls.Load() != 1 || requestClient.legacyCalls.Load() != 0 {
		t.Fatalf("request calls generate=%d stream=%d legacy=%d", requestClient.generateCalls.Load(), requestClient.streamCalls.Load(), requestClient.legacyCalls.Load())
	}
}

func TestNewChainClient_Phase5CompilesLegacyFactoriesIntoEntries(t *testing.T) {
	requestClient := &phase5RequestClient{generate: func(context.Context, *core.AIRequest) (*core.AIResult, error) {
		return &core.AIResult{Response: &core.AIResponse{Content: "request-aware"}}, nil
	}}
	factory := &phase3LegacyFactory{name: "phase5-legacy-factory", client: requestClient}
	installPhase3Factory(t, factory)

	chain, err := NewChainClient(WithProviderChain(factory.name))
	if err != nil {
		t.Fatalf("NewChainClient returned error: %v", err)
	}
	if len(chain.entries) != 1 || chain.entries[0].kind != chainLegacyProvider || chain.entries[0].name != factory.name {
		t.Fatalf("compiled entries = %#v", chain.entries)
	}
	response, err := chain.GenerateResponse(t.Context(), "prompt", nil)
	if err != nil || response == nil || response.Content != "request-aware" {
		t.Fatalf("GenerateResponse response=%#v err=%v", response, err)
	}
	if requestClient.generateCalls.Load() != 1 || requestClient.legacyCalls.Load() != 0 {
		t.Fatalf("request calls generate=%d legacy=%d", requestClient.generateCalls.Load(), requestClient.legacyCalls.Load())
	}
}

func TestNewChainClient_Phase5HandlesLegacyMaterializationFailure(t *testing.T) {
	wantErr := errors.New("legacy construction failed")
	factory := &phase3ValidatedFactory{name: "phase5-legacy-error", err: wantErr}
	installPhase3Factory(t, factory)

	chain, err := NewChainClient(WithProviderChain(factory.name))
	if chain != nil || err == nil || !strings.Contains(err.Error(), "no providers could be initialized") {
		t.Fatalf("NewChainClient chain=%#v error=%v", chain, err)
	}
	if factory.validatedCalls != 1 {
		t.Fatalf("validated factory calls = %d", factory.validatedCalls)
	}
}

func TestChainClient_Phase5CancellationStopsFailover(t *testing.T) {
	first := &phase5RequestClient{generate: func(context.Context, *core.AIRequest) (*core.AIResult, error) {
		return nil, context.Canceled
	}}
	second := &phase5RequestClient{}
	chain, err := NewChain(ClientEntry("first", first), ClientEntry("second", second))
	if err != nil {
		t.Fatalf("NewChain returned error: %v", err)
	}
	if _, err := chain.Generate(t.Context(), core.NewAIRequest("prompt", "cancel")); !errors.Is(err, context.Canceled) {
		t.Fatalf("Generate error = %v, want context cancellation", err)
	}
	if second.generateCalls.Load() != 0 {
		t.Fatalf("second entry called after cancellation: %d", second.generateCalls.Load())
	}
}

func TestChainClient_Phase5ValidatesLiveRequestPaths(t *testing.T) {
	empty := &ChainClient{}
	if _, err := empty.Generate(t.Context(), nil); err == nil || err.Error() != "AI chain request is nil" {
		t.Fatalf("Generate nil request error = %v", err)
	}
	if _, err := empty.Generate(t.Context(), core.NewAIRequest("prompt", "purpose")); err == nil || err.Error() != "AI chain has no entries" {
		t.Fatalf("Generate empty chain error = %v", err)
	}
	if _, err := empty.Stream(t.Context(), nil, func(core.StreamChunk) error { return nil }); err == nil || err.Error() != "AI chain stream request is nil" {
		t.Fatalf("Stream nil request error = %v", err)
	}
	if _, err := empty.Stream(t.Context(), core.NewAIRequest("prompt", "purpose"), nil); err == nil || err.Error() != "AI chain stream callback is nil" {
		t.Fatalf("Stream nil callback error = %v", err)
	}
	if _, err := empty.Stream(t.Context(), core.NewAIRequest("prompt", "purpose"), func(core.StreamChunk) error { return nil }); err == nil || err.Error() != "AI chain has no entries" {
		t.Fatalf("Stream empty chain error = %v", err)
	}

	client := &phase5RequestClient{}
	chain, err := NewChain(ClientEntry("client", client))
	if err != nil {
		t.Fatalf("NewChain returned error: %v", err)
	}
	cycle := map[string]interface{}{}
	cycle["self"] = cycle
	request := core.NewAIRequest("prompt", "purpose")
	request.Patches = []core.AIProviderPatch{{
		Name: "cycle", Set: map[string]interface{}{"/cycle": cycle},
	}}
	if _, err := chain.Generate(t.Context(), request); err == nil || !strings.Contains(err.Error(), "cyclic map") {
		t.Fatalf("Generate clone error = %v", err)
	}
	if _, err := chain.Stream(t.Context(), request, func(core.StreamChunk) error { return nil }); err == nil || !strings.Contains(err.Error(), "cyclic map") {
		t.Fatalf("Stream clone error = %v", err)
	}
	if client.generateCalls.Load() != 0 || client.streamCalls.Load() != 0 {
		t.Fatalf("client called after clone failure: generate=%d stream=%d", client.generateCalls.Load(), client.streamCalls.Load())
	}
}

func TestChainClient_Phase5RejectsNilSuccessResults(t *testing.T) {
	client := &phase5RequestClient{
		generate: func(context.Context, *core.AIRequest) (*core.AIResult, error) { return nil, nil },
		stream: func(context.Context, *core.AIRequest, core.StreamCallback) (*core.AIResult, error) {
			return nil, nil
		},
	}
	chain, err := NewChain(ClientEntry("nil-result", client))
	if err != nil {
		t.Fatalf("NewChain returned error: %v", err)
	}
	if _, err := chain.Generate(t.Context(), core.NewAIRequest("prompt", "purpose")); err == nil || !strings.Contains(err.Error(), "nil response without error") {
		t.Fatalf("Generate nil result error = %v", err)
	}
	if _, err := chain.Stream(t.Context(), core.NewAIRequest("prompt", "purpose"), func(core.StreamChunk) error { return nil }); err == nil || !strings.Contains(err.Error(), "nil response without error") {
		t.Fatalf("Stream nil result error = %v", err)
	}
}

func TestChainClient_Phase5StreamingCapabilityExhaustionAndAbort(t *testing.T) {
	t.Run("non-streaming entry fails over", func(t *testing.T) {
		legacy := &phase5LegacyClient{response: &core.AIResponse{Content: "legacy generation"}}
		backup := &phase5RequestClient{}
		chain, err := NewChain(ClientEntry("legacy", legacy), ClientEntry("backup", backup))
		if err != nil {
			t.Fatalf("NewChain returned error: %v", err)
		}
		result, err := chain.Stream(t.Context(), core.NewAIRequest("prompt", "purpose"), func(core.StreamChunk) error { return nil })
		if err != nil || result == nil || result.Response == nil || result.Response.Content != "request" {
			t.Fatalf("Stream result=%#v err=%v", result, err)
		}
		if legacy.calls.Load() != 0 || backup.streamCalls.Load() != 1 {
			t.Fatalf("legacy calls=%d backup stream calls=%d", legacy.calls.Load(), backup.streamCalls.Load())
		}
	})

	t.Run("exhaustion joins causes and keeps last report", func(t *testing.T) {
		firstErr := errors.New("first stream failure")
		secondErr := errors.New("second stream failure")
		first := &phase5RequestClient{stream: func(context.Context, *core.AIRequest, core.StreamCallback) (*core.AIResult, error) {
			return nil, firstErr
		}}
		second := &phase5RequestClient{stream: func(context.Context, *core.AIRequest, core.StreamCallback) (*core.AIResult, error) {
			return &core.AIResult{RequestReport: &core.AIRequestReport{Provider: "second", Fingerprint: "kept", Stable: true}}, secondErr
		}}
		chain, err := NewChain(ClientEntry("first", first), ClientEntry("second", second))
		if err != nil {
			t.Fatalf("NewChain returned error: %v", err)
		}
		result, err := chain.Stream(t.Context(), core.NewAIRequest("prompt", "purpose"), func(core.StreamChunk) error { return nil })
		if !errors.Is(err, firstErr) || !errors.Is(err, secondErr) {
			t.Fatalf("stream exhaustion error = %v", err)
		}
		if result == nil || result.RequestReport == nil || result.RequestReport.Provider != "second" ||
			result.RequestReport.Fingerprint != "kept" || !result.RequestReport.Stable {
			t.Fatalf("stream exhaustion result = %#v", result)
		}
		last := result.RequestReport.Adjustments[len(result.RequestReport.Adjustments)-1]
		if last.Rule != "second" || last.Reason != "chain attempt 2" {
			t.Fatalf("stream exhaustion annotation = %#v", last)
		}
	})

	t.Run("non-retryable provider error aborts", func(t *testing.T) {
		wantErr := &testProviderError{statusCode: 400, message: "bad request"}
		first := &phase5RequestClient{stream: func(context.Context, *core.AIRequest, core.StreamCallback) (*core.AIResult, error) {
			return nil, wantErr
		}}
		second := &phase5RequestClient{}
		chain, err := NewChain(ClientEntry("first", first), ClientEntry("second", second))
		if err != nil {
			t.Fatalf("NewChain returned error: %v", err)
		}
		if _, err := chain.Stream(t.Context(), core.NewAIRequest("prompt", "purpose"), func(core.StreamChunk) error { return nil }); !errors.Is(err, wantErr) {
			t.Fatalf("Stream error = %v", err)
		}
		if second.streamCalls.Load() != 0 {
			t.Fatalf("second entry called after non-retryable error: %d", second.streamCalls.Load())
		}
	})
}

func TestChainClient_Phase5PropagatesAttemptSpanContext(t *testing.T) {
	client := &phase5RequestClient{
		generate: func(ctx context.Context, _ *core.AIRequest) (*core.AIResult, error) {
			if got := ctx.Value(phase5TraceKey{}); got != "ai.chain.provider_attempt" {
				return nil, fmt.Errorf("generate context span = %v", got)
			}
			return &core.AIResult{Response: &core.AIResponse{Content: "generated"}}, nil
		},
		stream: func(ctx context.Context, _ *core.AIRequest, _ core.StreamCallback) (*core.AIResult, error) {
			if got := ctx.Value(phase5TraceKey{}); got != "ai.chain.stream_attempt" {
				return nil, fmt.Errorf("stream context span = %v", got)
			}
			return &core.AIResult{Response: &core.AIResponse{Content: "streamed"}}, nil
		},
	}
	chain, err := NewChain(ClientEntry("request", client))
	if err != nil {
		t.Fatalf("NewChain returned error: %v", err)
	}
	chain.SetTelemetry(&phase5Telemetry{})
	if _, err := chain.Generate(t.Context(), core.NewAIRequest("prompt", "purpose")); err != nil {
		t.Fatalf("Generate returned error: %v", err)
	}
	if _, err := chain.Stream(t.Context(), core.NewAIRequest("prompt", "purpose"), func(core.StreamChunk) error { return nil }); err != nil {
		t.Fatalf("Stream returned error: %v", err)
	}
}

func TestChainClient_Phase5DoesNotMutateInjectedClientsThroughSetLogger(t *testing.T) {
	factory := &phase5RequestFactory{
		name: "phase5-managed",
		build: func(int, *AIConfig, ProviderIntegrationConfig) *phase5RequestClient {
			return &phase5RequestClient{generate: func(context.Context, *core.AIRequest) (*core.AIResult, error) {
				return nil, errors.New("managed unavailable")
			}}
		},
	}
	installPhase3Factory(t, factory)
	injected := &phase5LegacyClient{response: &core.AIResponse{Content: "injected"}}
	chain, err := NewChain(
		ProviderEntry("managed", factory.name),
		ClientEntry("injected", injected),
	)
	if err != nil {
		t.Fatalf("NewChain returned error: %v", err)
	}
	chain.SetLogger(&core.NoOpLogger{})
	chain.SetTelemetry(&core.NoOpTelemetry{})
	if factory.clients[0].setLoggerCalls.Load() != 1 {
		t.Fatalf("managed SetLogger calls = %d", factory.clients[0].setLoggerCalls.Load())
	}
	if injected.setLoggerCalls.Load() != 0 {
		t.Fatalf("injected client was mutated through SetLogger: %d", injected.setLoggerCalls.Load())
	}
	if factory.clients[0].setTelemCalls.Load() != 1 {
		t.Fatalf("managed SetTelemetry calls = %d", factory.clients[0].setTelemCalls.Load())
	}
	if injected.setTelemCalls.Load() != 0 {
		t.Fatalf("injected client was mutated through SetTelemetry: %d", injected.setTelemCalls.Load())
	}
	result, err := chain.Generate(t.Context(), core.NewAIRequest("prompt", "mixed"))
	if err != nil || result.Response == nil || result.Response.Content != "injected" || injected.calls.Load() != 1 {
		t.Fatalf("mixed chain result=%#v err=%v injected_calls=%d", result, err, injected.calls.Load())
	}
}

func TestChainClient_Phase5StreamingFailoverBoundary(t *testing.T) {
	t.Run("fails over before first chunk", func(t *testing.T) {
		firstErr := errors.New("connect failed")
		first := &phase5RequestClient{stream: func(context.Context, *core.AIRequest, core.StreamCallback) (*core.AIResult, error) {
			return nil, firstErr
		}}
		second := &phase5RequestClient{stream: func(_ context.Context, request *core.AIRequest, callback core.StreamCallback) (*core.AIResult, error) {
			if request.Prompt != "prompt" {
				return nil, errors.New("request mutation leaked")
			}
			if err := callback(core.StreamChunk{Content: "backup", Delta: true}); err != nil {
				return nil, err
			}
			return &core.AIResult{Response: &core.AIResponse{Content: "backup"}}, nil
		}}
		chain, err := NewChain(ClientEntry("first", first), ClientEntry("second", second))
		if err != nil {
			t.Fatalf("NewChain returned error: %v", err)
		}
		var chunks []string
		result, err := chain.Stream(t.Context(), core.NewAIRequest("prompt", "stream"), func(chunk core.StreamChunk) error {
			chunks = append(chunks, chunk.Content)
			return nil
		})
		if err != nil || result.Response == nil || result.Response.Content != "backup" || !reflect.DeepEqual(chunks, []string{"backup"}) {
			t.Fatalf("Stream result=%#v chunks=%#v err=%v", result, chunks, err)
		}
		if first.streamCalls.Load() != 1 || second.streamCalls.Load() != 1 {
			t.Fatalf("stream calls first=%d second=%d", first.streamCalls.Load(), second.streamCalls.Load())
		}
	})

	t.Run("does not fail over after a chunk", func(t *testing.T) {
		streamErr := errors.New("stream interrupted")
		first := &phase5RequestClient{stream: func(_ context.Context, _ *core.AIRequest, callback core.StreamCallback) (*core.AIResult, error) {
			if err := callback(core.StreamChunk{Content: "visible", Delta: true}); err != nil {
				return nil, err
			}
			return &core.AIResult{
				Response:      &core.AIResponse{Content: "visible"},
				RequestReport: &core.AIRequestReport{Provider: "first", Stable: false},
			}, streamErr
		}}
		second := &phase5RequestClient{}
		chain, err := NewChain(ClientEntry("first", first), ClientEntry("second", second))
		if err != nil {
			t.Fatalf("NewChain returned error: %v", err)
		}
		result, err := chain.Stream(t.Context(), core.NewAIRequest("prompt", "stream"), func(core.StreamChunk) error { return nil })
		if !errors.Is(err, streamErr) || result == nil || result.Response == nil || result.Response.Content != "visible" {
			t.Fatalf("Stream result=%#v err=%v", result, err)
		}
		if second.streamCalls.Load() != 0 {
			t.Fatalf("second entry called after visible output: %d", second.streamCalls.Load())
		}
		last := result.RequestReport.Adjustments[len(result.RequestReport.Adjustments)-1]
		if last.Rule != "first" || last.Reason != "chain attempt 1" {
			t.Fatalf("partial report annotation = %#v", last)
		}
	})
}

func TestChainClient_Phase5LegacyStreamingCapabilityAndReport(t *testing.T) {
	legacy := &phase5LegacyStreamingClient{stream: func(
		_ context.Context,
		prompt string,
		options *core.AIOptions,
		callback core.StreamCallback,
	) (*core.AIResponse, error) {
		if prompt != "legacy prompt" || options == nil || options.Model != "legacy-model" {
			return nil, fmt.Errorf("legacy stream request prompt=%q options=%#v", prompt, options)
		}
		if err := callback(core.StreamChunk{Content: "legacy", Delta: true}); err != nil {
			return nil, err
		}
		return &core.AIResponse{Content: "legacy", Provider: "legacy-provider", Model: "legacy-model"}, nil
	}}
	chain, err := NewChain(ClientEntry("legacy-entry", legacy))
	if err != nil {
		t.Fatalf("NewChain returned error: %v", err)
	}
	request := core.NewAIRequestFromLegacy("legacy prompt", "legacy-purpose", &core.AIOptions{Model: "legacy-model"})
	result, err := chain.Stream(t.Context(), request, func(core.StreamChunk) error { return nil })
	if err != nil {
		t.Fatalf("Stream returned error: %v", err)
	}
	if legacy.streamCalls.Load() != 1 || result.RequestReport == nil || result.RequestReport.Provider != "legacy-provider" ||
		result.RequestReport.Operation != "stream" || result.RequestReport.Purpose != "legacy-purpose" || result.RequestReport.Stable {
		t.Fatalf("legacy stream result = %#v calls=%d", result, legacy.streamCalls.Load())
	}

	request = core.NewAIRequest("advanced", "advanced")
	request.Generation.TopP = core.SetAIParameter(float32(0.9))
	backup := &phase5RequestClient{}
	advancedChain, err := NewChain(ClientEntry("legacy", legacy), ClientEntry("backup", backup))
	if err != nil {
		t.Fatalf("NewChain returned error: %v", err)
	}
	if _, err := advancedChain.Stream(t.Context(), request, func(core.StreamChunk) error { return nil }); err != nil {
		t.Fatalf("advanced Stream returned error: %v", err)
	}
	if legacy.streamCalls.Load() != 1 || backup.streamCalls.Load() != 1 {
		t.Fatalf("legacy advanced calls=%d backup calls=%d", legacy.streamCalls.Load(), backup.streamCalls.Load())
	}
}

func TestChainClient_Phase5ConcurrentUse(t *testing.T) {
	client := &phase5RequestClient{generate: func(_ context.Context, request *core.AIRequest) (*core.AIResult, error) {
		return &core.AIResult{Response: &core.AIResponse{Content: request.Prompt}}, nil
	}}
	chain, err := NewChain(ClientEntry("shared", client))
	if err != nil {
		t.Fatalf("NewChain returned error: %v", err)
	}

	const workers = 32
	errorsFound := make(chan error, workers)
	var wait sync.WaitGroup
	for index := range workers {
		wait.Add(1)
		go func() {
			defer wait.Done()
			prompt := fmt.Sprintf("prompt-%d", index)
			result, err := chain.Generate(t.Context(), core.NewAIRequest(prompt, "race"))
			if err != nil {
				errorsFound <- err
				return
			}
			if result.Response == nil || result.Response.Content != prompt {
				errorsFound <- fmt.Errorf("prompt %q result %#v", prompt, result)
			}
		}()
	}
	wait.Wait()
	close(errorsFound)
	for err := range errorsFound {
		t.Error(err)
	}
	if client.generateCalls.Load() != workers {
		t.Fatalf("generate calls = %d, want %d", client.generateCalls.Load(), workers)
	}
}

func TestChainClient_SupportsStreaming_HonorsRequestAwareDecoratorCapability(t *testing.T) {
	nonStreaming := NewInstrumentedClient(&phase5LegacyClient{}, nil)
	chain, err := NewChain(ClientEntry("decorated", nonStreaming))
	if err != nil {
		t.Fatalf("NewChain returned error: %v", err)
	}
	if chain.SupportsStreaming() {
		t.Fatal("request-aware decorator over a non-streaming client reported streaming support")
	}

	streaming := NewInstrumentedClient(&phase5LegacyStreamingClient{}, nil)
	chain, err = NewChain(ClientEntry("decorated", streaming))
	if err != nil {
		t.Fatalf("NewChain returned error: %v", err)
	}
	if !chain.SupportsStreaming() {
		t.Fatal("request-aware decorator over a streaming client hid streaming support")
	}
}

var (
	_ core.AIRequestClient          = (*phase5RequestClient)(nil)
	_ core.StreamingAIRequestClient = (*phase5RequestClient)(nil)
	_ core.StreamingAIClient        = (*phase5LegacyStreamingClient)(nil)
	_ CredentialSource              = (*phase5CredentialSource)(nil)
	_ EndpointResolver              = (*phase5EndpointResolver)(nil)
)
