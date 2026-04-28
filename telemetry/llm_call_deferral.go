package telemetry

import "context"

// WithLLMCallRecordingDeferred marks ctx so that InstrumentedAIClient
// skips its own LLMCallRecorder emission on the next GenerateResponse /
// StreamResponse call. The caller takes responsibility for recording
// the call itself after the wrapped client returns.
//
// Used by any code path that emits its own record for the same LLM
// call and wants to avoid a double-write from the wrapper. The
// telemetry module has no opinion about who the caller is or why they
// defer — it only provides the suppression mechanism. Calls without
// the marker continue to be recorded by the wrapper as agent_llm_call.
//
// Callers must only set the marker when they can actually record the
// call themselves. Setting the marker without a working downstream
// recorder silently loses the record — honour the graceful-fallback
// contract by guarding the marker on the downstream recorder's
// availability before wrapping ctx.
func WithLLMCallRecordingDeferred(ctx context.Context) context.Context {
	return context.WithValue(ctx, llmCallRecordingDeferredKey{}, true)
}

// IsLLMCallRecordingDeferred reports whether ctx carries the deferral
// marker. Consulted by InstrumentedAIClient; not intended for general
// callers to inspect.
func IsLLMCallRecordingDeferred(ctx context.Context) bool {
	v, _ := ctx.Value(llmCallRecordingDeferredKey{}).(bool)
	return v
}

// llmCallRecordingDeferredKey is the unexported context key used to
// store the deferral flag. Using a dedicated unexported struct type
// prevents external packages from setting or reading the flag with a
// colliding key value — only this package can reach it.
type llmCallRecordingDeferredKey struct{}
