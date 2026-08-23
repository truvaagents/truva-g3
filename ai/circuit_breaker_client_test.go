package ai

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

type recordingCircuitBreaker struct {
	mu            sync.Mutex
	open          bool
	openOnFailure bool
	executions    int
	observed      []error
	classifier    func(error) bool
}

func (breaker *recordingCircuitBreaker) Execute(ctx context.Context, fn func() error) error {
	return breaker.ExecuteWithTimeout(ctx, 0, fn)
}

func (breaker *recordingCircuitBreaker) ExecuteWithTimeout(
	_ context.Context,
	_ time.Duration,
	fn func() error,
) error {
	breaker.mu.Lock()
	if breaker.open {
		breaker.mu.Unlock()
		return fmt.Errorf("test breaker is open: %w", core.ErrCircuitBreakerOpen)
	}
	breaker.executions++
	breaker.mu.Unlock()

	err := fn()
	breaker.mu.Lock()
	breaker.observed = append(breaker.observed, err)
	if err != nil && breaker.openOnFailure && breaker.classifier != nil && breaker.classifier(err) {
		breaker.open = true
	}
	breaker.mu.Unlock()
	return err
}

func (breaker *recordingCircuitBreaker) GetState() string {
	breaker.mu.Lock()
	defer breaker.mu.Unlock()
	if breaker.open {
		return "open"
	}
	return "closed"
}

func (breaker *recordingCircuitBreaker) GetMetrics() map[string]interface{} {
	breaker.mu.Lock()
	defer breaker.mu.Unlock()
	return map[string]interface{}{"executions": breaker.executions}
}

func (breaker *recordingCircuitBreaker) Reset() {
	breaker.mu.Lock()
	breaker.open = false
	breaker.mu.Unlock()
}

func (breaker *recordingCircuitBreaker) CanExecute() bool {
	breaker.mu.Lock()
	defer breaker.mu.Unlock()
	return !breaker.open
}

func TestShouldCountAICircuitBreakerFailure(t *testing.T) {
	callbackErr := errors.New("application callback stopped")
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{name: "nil", err: nil, want: false},
		{name: "configuration", err: core.ErrInvalidConfiguration, want: false},
		{name: "unsupported feature", err: core.ErrAIRequestFeatureUnsupported, want: false},
		{name: "caller cancellation", err: context.Canceled, want: false},
		{name: "partial stream", err: errors.Join(core.ErrStreamPartiallyCompleted, errors.New("upstream reset")), want: false},
		{name: "callback", err: &aiCircuitBreakerIgnoredError{cause: callbackErr}, want: false},
		{name: "deadline", err: context.DeadlineExceeded, want: true},
		{name: "connection", err: core.ErrConnectionFailed, want: true},
		{name: "provider 400", err: &testProviderError{statusCode: 400}, want: false},
		{name: "provider auth", err: &testProviderError{statusCode: 401}, want: false},
		{name: "provider billing", err: &testProviderError{statusCode: 402, retryable: true}, want: false},
		{name: "provider timeout", err: &testProviderError{statusCode: 408}, want: true},
		{name: "provider rate limit", err: &testProviderError{statusCode: 429}, want: true},
		{name: "provider server", err: &testProviderError{statusCode: 503}, want: true},
		{name: "transient proxy", err: &testProviderError{statusCode: 400, transient: true}, want: true},
		{name: "network", err: errors.New("connection reset"), want: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := ShouldCountAICircuitBreakerFailure(test.err); got != test.want {
				t.Fatalf("ShouldCountAICircuitBreakerFailure(%v) = %t, want %t", test.err, got, test.want)
			}
		})
	}
}

func TestNewCircuitBreakerClientProtectsDirectBufferedAndStreamingCalls(t *testing.T) {
	callbackErr := errors.New("stop consuming")
	wrapped := &phase5RequestClient{
		generate: func(context.Context, *core.AIRequest) (*core.AIResult, error) {
			return &core.AIResult{Response: &core.AIResponse{Content: "generated"}}, nil
		},
		stream: func(
			_ context.Context,
			_ *core.AIRequest,
			callback core.StreamCallback,
		) (*core.AIResult, error) {
			if err := callback(core.StreamChunk{Content: "partial", Delta: true}); err != nil {
				return &core.AIResult{Response: &core.AIResponse{Content: "partial"}}, err
			}
			return nil, nil
		},
	}
	breaker := &recordingCircuitBreaker{
		classifier:    ShouldCountAICircuitBreakerFailure,
		openOnFailure: true,
	}
	protected, err := NewCircuitBreakerClient(wrapped, breaker)
	if err != nil {
		t.Fatal(err)
	}

	response, err := protected.GenerateResponse(t.Context(), "prompt", nil)
	if err != nil || response == nil || response.Content != "generated" {
		t.Fatalf("buffered result = %#v, error = %v", response, err)
	}
	streaming := protected.(core.StreamingAIClient)
	response, err = streaming.StreamResponse(
		t.Context(),
		"prompt",
		nil,
		func(core.StreamChunk) error { return callbackErr },
	)
	if !errors.Is(err, callbackErr) || response == nil || response.Content != "partial" {
		t.Fatalf("stream result = %#v, error = %v", response, err)
	}
	if breaker.GetState() != "closed" {
		t.Fatal("application callback error must not open provider-health circuit")
	}
	breaker.mu.Lock()
	defer breaker.mu.Unlock()
	if len(breaker.observed) != 2 {
		t.Fatalf("breaker observations = %d, want 2", len(breaker.observed))
	}
	var ignored *aiCircuitBreakerIgnoredError
	if !errors.As(breaker.observed[1], &ignored) {
		t.Fatalf("stream callback breaker observation = %T, want ignored marker", breaker.observed[1])
	}
}

func TestCircuitBreakerDecoratorForwardsCapabilitiesConfigurationAndFingerprint(t *testing.T) {
	wrapped := &phase8FingerprintClient{
		phase5RequestClient: phase5RequestClient{},
		fingerprint:         "request-aware-v1",
		stable:              true,
	}
	protected, err := NewCircuitBreakerClient(wrapped, &recordingCircuitBreaker{})
	if err != nil {
		t.Fatal(err)
	}
	decorator := protected.(*circuitBreakerAIClient)
	if !decorator.SupportsStreaming() {
		t.Fatal("request-aware streaming capability was not preserved")
	}
	decorator.SetLogger(&core.NoOpLogger{})
	decorator.SetTelemetry(&core.NoOpTelemetry{})
	if wrapped.setLoggerCalls.Load() != 1 || wrapped.setTelemCalls.Load() != 1 {
		t.Fatalf("configuration forwarding logger=%d telemetry=%d",
			wrapped.setLoggerCalls.Load(), wrapped.setTelemCalls.Load())
	}
	request := core.NewAIRequest("prompt", "fingerprint")
	if fingerprint, stable := decorator.RequestFingerprint(t.Context(), request); !stable || fingerprint != "request-aware-v1" {
		t.Fatalf("fingerprint=%q stable=%t", fingerprint, stable)
	}

	valueProtected, err := NewCircuitBreakerClient(phase5ValueClient{}, &recordingCircuitBreaker{})
	if err != nil {
		t.Fatal(err)
	}
	valueDecorator := valueProtected.(*circuitBreakerAIClient)
	if valueDecorator.SupportsStreaming() {
		t.Fatal("non-streaming value client reported streaming support")
	}
	valueDecorator.SetLogger(&core.NoOpLogger{})
	valueDecorator.SetTelemetry(&core.NoOpTelemetry{})
}

func TestCircuitBreakerDecoratorRejectsInvalidDirectRequestsBeforeExecution(t *testing.T) {
	breaker := &recordingCircuitBreaker{}
	protected, err := NewCircuitBreakerClient(&phase5RequestClient{}, breaker)
	if err != nil {
		t.Fatal(err)
	}
	requestClient := protected.(core.AIRequestClient)
	streaming := protected.(core.StreamingAIRequestClient)
	if _, err := requestClient.Generate(t.Context(), nil); err == nil {
		t.Fatal("nil request was accepted by Generate")
	}
	if _, err := streaming.Stream(t.Context(), core.NewAIRequest("prompt", "stream"), nil); err == nil {
		t.Fatal("nil callback was accepted by Stream")
	}
	if breaker.executions != 0 {
		t.Fatalf("invalid calls reached breaker: %d executions", breaker.executions)
	}
}

func TestCircuitBreakerDecoratorReturnsOpenErrorWithoutCallingProvider(t *testing.T) {
	wrapped := &phase5RequestClient{}
	protected, err := NewCircuitBreakerClient(wrapped, &recordingCircuitBreaker{open: true})
	if err != nil {
		t.Fatal(err)
	}
	request := core.NewAIRequest("prompt", "open-breaker")
	if _, err := protected.(core.AIRequestClient).Generate(t.Context(), request); !errors.Is(err, core.ErrCircuitBreakerOpen) {
		t.Fatalf("Generate error=%v, want circuit open", err)
	}
	if _, err := protected.(core.StreamingAIRequestClient).Stream(
		t.Context(), request, func(core.StreamChunk) error { return nil },
	); !errors.Is(err, core.ErrCircuitBreakerOpen) {
		t.Fatalf("Stream error=%v, want circuit open", err)
	}
	if wrapped.generateCalls.Load() != 0 || wrapped.streamCalls.Load() != 0 {
		t.Fatalf("open breaker reached provider: generate=%d stream=%d",
			wrapped.generateCalls.Load(), wrapped.streamCalls.Load())
	}
}

func TestCircuitBreakerDecoratorSupportsLegacyStreamingAndNilLegacyCallback(t *testing.T) {
	wrapped := &phase5LegacyStreamingClient{}
	wrapped.stream = func(
		_ context.Context,
		_ string,
		_ *core.AIOptions,
		callback core.StreamCallback,
	) (*core.AIResponse, error) {
		if err := callback(core.StreamChunk{Content: "legacy", Delta: true}); err != nil {
			return nil, err
		}
		return &core.AIResponse{Content: "legacy"}, nil
	}
	protected, err := NewCircuitBreakerClient(wrapped, &recordingCircuitBreaker{})
	if err != nil {
		t.Fatal(err)
	}
	streaming := protected.(core.StreamingAIClient)
	if !streaming.SupportsStreaming() {
		t.Fatal("legacy streaming capability was not preserved")
	}
	response, err := streaming.StreamResponse(t.Context(), "prompt", nil, nil)
	if err != nil || response == nil || response.Content != "legacy" {
		t.Fatalf("legacy stream response=%#v error=%v", response, err)
	}
}

func TestCircuitBreakerInternalErrorsAreSafeAndUnwrapCauses(t *testing.T) {
	cause := errors.New("provider-secret-canary")
	ignored := &aiCircuitBreakerIgnoredError{cause: cause}
	if ignored.Error() != "AI caller callback stopped stream" || !errors.Is(ignored, cause) {
		t.Fatalf("ignored error=%q unwrap=%t", ignored.Error(), errors.Is(ignored, cause))
	}
	execution := &aiCircuitBreakerExecutionError{cause: cause}
	if execution.Error() != "AI provider call failed" || !errors.Is(execution, cause) {
		t.Fatalf("execution error=%q unwrap=%t", execution.Error(), errors.Is(execution, cause))
	}
}

func TestNewCircuitBreakerClientRejectsNilDependencies(t *testing.T) {
	var typedNilClient *phase5RequestClient
	var typedNilBreaker *recordingCircuitBreaker
	if _, err := NewCircuitBreakerClient(nil, &recordingCircuitBreaker{}); !errors.Is(err, core.ErrInvalidConfiguration) {
		t.Fatalf("nil client error = %v", err)
	}
	if _, err := NewCircuitBreakerClient(typedNilClient, &recordingCircuitBreaker{}); !errors.Is(err, core.ErrInvalidConfiguration) {
		t.Fatalf("typed nil client error = %v", err)
	}
	if _, err := NewCircuitBreakerClient(&phase5RequestClient{}, typedNilBreaker); !errors.Is(err, core.ErrInvalidConfiguration) {
		t.Fatalf("typed nil breaker error = %v", err)
	}
}

func TestCircuitBreakerReceivesSafeErrorWhileCallerKeepsCause(t *testing.T) {
	upstreamErr := errors.New("provider body contains prompt-canary-secret")
	wrapped := &phase5RequestClient{
		generate: func(context.Context, *core.AIRequest) (*core.AIResult, error) {
			return nil, upstreamErr
		},
	}
	breaker := &recordingCircuitBreaker{classifier: ShouldCountAICircuitBreakerFailure}
	protected, err := NewCircuitBreakerClient(wrapped, breaker)
	if err != nil {
		t.Fatal(err)
	}
	requestClient := protected.(core.AIRequestClient)
	if _, err := requestClient.Generate(t.Context(), core.NewAIRequest("prompt", "test")); !errors.Is(err, upstreamErr) {
		t.Fatalf("caller error = %v, want original cause", err)
	}
	breaker.mu.Lock()
	defer breaker.mu.Unlock()
	if len(breaker.observed) != 1 {
		t.Fatalf("breaker observations = %d", len(breaker.observed))
	}
	if got := breaker.observed[0].Error(); got != "AI provider call failed" || strings.Contains(got, "canary") {
		t.Fatalf("breaker-visible error = %q", got)
	}
	if !errors.Is(breaker.observed[0], upstreamErr) {
		t.Fatal("safe breaker error must preserve classification through Unwrap")
	}
}

func TestNewClientInstallsCircuitBreakerInsideLogicalInstrumentation(t *testing.T) {
	withRecordingFactory(t)
	breaker := &recordingCircuitBreaker{open: true}
	client, err := NewClient(
		WithProvider("mock-recording"),
		WithCircuitBreaker(breaker),
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.GenerateResponse(t.Context(), "prompt", nil); !errors.Is(err, core.ErrCircuitBreakerOpen) {
		t.Fatalf("GenerateResponse error = %v, want circuit open", err)
	}
	instrumented, ok := client.(*InstrumentedAIClient)
	if !ok {
		t.Fatalf("client type = %T, want logical instrumentation", client)
	}
	if _, ok := instrumented.wrapped.(*circuitBreakerAIClient); !ok {
		t.Fatalf("instrumented wrapped type = %T, want circuit breaker decorator", instrumented.wrapped)
	}
}

func TestCircuitBreakerDecoratorPreservesLegacyFingerprint(t *testing.T) {
	request := core.NewAIRequestFromLegacy("prompt", "planning", &core.AIOptions{Model: "model-a"})
	legacy := &phase5LegacyClient{}
	want, stable := NewInstrumentedClient(legacy, nil).RequestFingerprint(t.Context(), request)
	if !stable || want == "" {
		t.Fatalf("unprotected fingerprint = %q, stable = %t", want, stable)
	}
	protected, err := NewCircuitBreakerClient(legacy, &recordingCircuitBreaker{})
	if err != nil {
		t.Fatal(err)
	}
	got, stable := NewInstrumentedClient(protected, nil).RequestFingerprint(t.Context(), request)
	if !stable || got != want {
		t.Fatalf("protected fingerprint = %q, stable = %t, want %q", got, stable, want)
	}
}

func TestNewChainClientCreatesIndependentCircuitBreakersPerEntry(t *testing.T) {
	withRecordingFactory(t)
	var (
		breakers []*recordingCircuitBreaker
		entries  []string
		aliases  []string
	)
	chain, err := NewChainClient(
		WithProviderChain("mock-recording.primary", "mock-recording.backup"),
		WithChainCircuitBreakerFactory(func(
			entryName string,
			providerAlias string,
			classifier func(error) bool,
		) (core.CircuitBreaker, error) {
			breaker := &recordingCircuitBreaker{classifier: classifier}
			breakers = append(breakers, breaker)
			entries = append(entries, entryName)
			aliases = append(aliases, providerAlias)
			return breaker, nil
		}),
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(breakers) != 2 || breakers[0] == breakers[1] {
		t.Fatalf("breakers = %#v, want two independent instances", breakers)
	}
	if fmt.Sprint(entries) != "[mock-recording.primary mock-recording.backup]" ||
		fmt.Sprint(aliases) != "[mock-recording.primary mock-recording.backup]" {
		t.Fatalf("factory identities: entries=%v aliases=%v", entries, aliases)
	}
	breakers[0].open = true
	response, err := chain.GenerateResponse(t.Context(), "prompt", nil)
	if err != nil || response == nil {
		t.Fatalf("chain response = %#v, error = %v", response, err)
	}
	if breakers[0].executions != 0 || breakers[1].executions != 1 {
		t.Fatalf("entry executions = [%d %d], want [0 1]", breakers[0].executions, breakers[1].executions)
	}
	if got := classifyFailoverReason(core.ErrCircuitBreakerOpen); got != "circuit_open" {
		t.Fatalf("circuit-open failover reason = %q", got)
	}
}

func TestNewChainClientRejectsCircuitBreakerFactoryFailure(t *testing.T) {
	withRecordingFactory(t)
	wantErr := errors.New("breaker configuration failed")
	_, err := NewChainClient(
		WithProviderChain("mock-recording"),
		WithChainCircuitBreakerFactory(func(
			string,
			string,
			func(error) bool,
		) (core.CircuitBreaker, error) {
			return nil, wantErr
		}),
	)
	if !errors.Is(err, wantErr) {
		t.Fatalf("factory error = %v", err)
	}

	_, err = NewChainClient(
		WithProviderChain("mock-recording"),
		WithChainCircuitBreakerFactory(func(
			string,
			string,
			func(error) bool,
		) (core.CircuitBreaker, error) {
			return nil, nil
		}),
	)
	if err == nil || !strings.Contains(err.Error(), "returned nil") {
		t.Fatalf("nil breaker error = %v", err)
	}
}

func TestChainClientCircuitBreakerCommentsDoNotClaimRetryOrFailoverIsABreaker(t *testing.T) {
	source, err := os.ReadFile("chain_client.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{
		"Each provider already has circuit breaker protection",
		"failover loop is the retry mechanism",
		"Neither layer is a circuit breaker",
	} {
		if strings.Contains(string(source), forbidden) {
			t.Fatalf("chain_client.go retains stale circuit-breaker claim %q", forbidden)
		}
	}
}
