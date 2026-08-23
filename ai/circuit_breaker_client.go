package ai

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"sync/atomic"

	"github.com/truvaagents/truva-g3/core"
)

// CircuitBreakerFactory constructs one independent breaker for a chain entry.
// entryName and providerAlias are stable, non-secret configuration identities.
// The factory must return a fresh breaker on every call and configure it with
// shouldCountFailure; sharing breaker state across entries is unsupported.
type CircuitBreakerFactory func(
	entryName string,
	providerAlias string,
	shouldCountFailure func(error) bool,
) (core.CircuitBreaker, error)

// NewCircuitBreakerClient decorates a caller-constructed AI client with a
// provider-neutral circuit breaker. It is the direct-constructor seam for
// clients that do not use NewClient, NewRequestClient, or ProviderEntry.
func NewCircuitBreakerClient(
	client core.AIClient,
	breaker core.CircuitBreaker,
) (core.AIClient, error) {
	if isNilAIClient(client) {
		return nil, fmt.Errorf("%w: AI client is nil", core.ErrInvalidConfiguration)
	}
	if isNilCircuitBreaker(breaker) {
		return nil, fmt.Errorf("%w: AI circuit breaker is nil", core.ErrInvalidConfiguration)
	}
	return &circuitBreakerAIClient{wrapped: client, breaker: breaker}, nil
}

// ShouldCountAICircuitBreakerFailure classifies provider-health failures for
// application-composed circuit breakers. Provider/network failures and
// timeouts count; caller cancellation, configuration/state/feature errors,
// ordinary 4xx responses, and post-output streaming failures do not.
func ShouldCountAICircuitBreakerFailure(err error) bool {
	if err == nil {
		return false
	}
	var ignored *aiCircuitBreakerIgnoredError
	if errors.As(err, &ignored) {
		return false
	}
	switch {
	case errors.Is(err, context.Canceled),
		errors.Is(err, core.ErrContextCanceled),
		errors.Is(err, core.ErrCircuitBreakerOpen),
		errors.Is(err, core.ErrStreamPartiallyCompleted),
		errors.Is(err, core.ErrAIRequestFeatureUnsupported),
		core.IsConfigurationError(err),
		core.IsNotFound(err),
		core.IsStateError(err):
		return false
	case errors.Is(err, context.DeadlineExceeded),
		errors.Is(err, core.ErrTimeout),
		errors.Is(err, core.ErrConnectionFailed),
		errors.Is(err, core.ErrRequestFailed):
		return true
	}

	var providerErr core.ProviderError
	if errors.As(err, &providerErr) {
		if providerErr.IsTransient() {
			return true
		}
		status := providerErr.StatusCode()
		if status >= 400 && status < 500 {
			return status == 408 || status == 429
		}
		return status == 0 || status >= 500
	}

	// Unstructured errors are conservatively treated as transport/provider
	// failures. Streaming callback failures are marked internally above.
	return true
}

type circuitBreakerAIClient struct {
	wrapped core.AIClient
	breaker core.CircuitBreaker
}

type aiCircuitBreakerIgnoredError struct {
	cause error
}

func (err *aiCircuitBreakerIgnoredError) Error() string { return "AI caller callback stopped stream" }
func (err *aiCircuitBreakerIgnoredError) Unwrap() error { return err.cause }

type aiCircuitBreakerExecutionError struct {
	cause error
}

func (err *aiCircuitBreakerExecutionError) Error() string { return "AI provider call failed" }
func (err *aiCircuitBreakerExecutionError) Unwrap() error { return err.cause }

func (client *circuitBreakerAIClient) GenerateResponse(
	ctx context.Context,
	prompt string,
	options *core.AIOptions,
) (*core.AIResponse, error) {
	result, err := client.Generate(ctx, core.NewAIRequestFromLegacy(prompt, "", options))
	if result != nil {
		return result.Response, err
	}
	return nil, err
}

func (client *circuitBreakerAIClient) Generate(
	ctx context.Context,
	request *core.AIRequest,
) (*core.AIResult, error) {
	if request == nil {
		return nil, errors.New("AI request is nil")
	}
	var (
		result    *core.AIResult
		callErr   error
		completed atomic.Bool
	)
	breakerErr := client.breaker.Execute(ctx, func() error {
		result, callErr = core.GenerateAI(ctx, client.wrapped, request)
		completed.Store(true)
		if callErr == nil {
			return nil
		}
		return &aiCircuitBreakerExecutionError{cause: callErr}
	})
	if !completed.Load() {
		return nil, breakerErr
	}
	return result, callErr
}

func (client *circuitBreakerAIClient) StreamResponse(
	ctx context.Context,
	prompt string,
	options *core.AIOptions,
	callback core.StreamCallback,
) (*core.AIResponse, error) {
	if callback == nil {
		callback = func(core.StreamChunk) error { return nil }
	}
	result, err := client.Stream(ctx, core.NewAIRequestFromLegacy(prompt, "", options), callback)
	if result != nil {
		return result.Response, err
	}
	return nil, err
}

func (client *circuitBreakerAIClient) Stream(
	ctx context.Context,
	request *core.AIRequest,
	callback core.StreamCallback,
) (*core.AIResult, error) {
	if request == nil {
		return nil, errors.New("AI request is nil")
	}
	if callback == nil {
		return nil, errors.New("AI stream callback is nil")
	}
	var (
		result         *core.AIResult
		callErr        error
		completed      atomic.Bool
		callbackFailed atomic.Bool
	)
	trackedCallback := func(chunk core.StreamChunk) error {
		err := callback(chunk)
		if err != nil {
			callbackFailed.Store(true)
		}
		return err
	}
	breakerErr := client.breaker.Execute(ctx, func() error {
		result, callErr = core.StreamAI(ctx, client.wrapped, request, trackedCallback)
		completed.Store(true)
		if callErr != nil && callbackFailed.Load() {
			return &aiCircuitBreakerIgnoredError{cause: callErr}
		}
		if callErr == nil {
			return nil
		}
		return &aiCircuitBreakerExecutionError{cause: callErr}
	})
	if !completed.Load() {
		return nil, breakerErr
	}
	return result, callErr
}

func (client *circuitBreakerAIClient) SupportsStreaming() bool {
	if _, ok := client.wrapped.(core.StreamingAIRequestClient); ok {
		return true
	}
	streaming, ok := client.wrapped.(core.StreamingAIClient)
	return ok && streaming.SupportsStreaming()
}

func (client *circuitBreakerAIClient) RequestFingerprint(
	ctx context.Context,
	request *core.AIRequest,
) (string, bool) {
	if fingerprinter, ok := client.wrapped.(core.AIRequestFingerprinter); ok {
		return fingerprinter.RequestFingerprint(ctx, request)
	}
	return legacyClientRequestFingerprint(client.wrapped, request)
}

func (client *circuitBreakerAIClient) SetLogger(logger core.Logger) {
	if configurable, ok := client.wrapped.(interface{ SetLogger(core.Logger) }); ok {
		configurable.SetLogger(logger)
	}
}

func (client *circuitBreakerAIClient) SetTelemetry(provider core.Telemetry) {
	if configurable, ok := client.wrapped.(interface{ SetTelemetry(core.Telemetry) }); ok {
		configurable.SetTelemetry(provider)
	}
}

func isNilAIClient(client core.AIClient) bool {
	return isNilInterface(client)
}

func isNilCircuitBreaker(breaker core.CircuitBreaker) bool {
	return isNilInterface(breaker)
}

func isNilInterface(value any) bool {
	if value == nil {
		return true
	}
	reflected := reflect.ValueOf(value)
	switch reflected.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return reflected.IsNil()
	default:
		return false
	}
}

var _ core.AIRequestClient = (*circuitBreakerAIClient)(nil)
var _ core.StreamingAIClient = (*circuitBreakerAIClient)(nil)
var _ core.StreamingAIRequestClient = (*circuitBreakerAIClient)(nil)
var _ core.AIRequestFingerprinter = (*circuitBreakerAIClient)(nil)
