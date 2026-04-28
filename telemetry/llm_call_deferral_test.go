package telemetry

import (
	"context"
	"testing"
)

// TestLLMCallRecordingDeferral_RoundTrip verifies the marker flows
// through a nested-context chain and survives unrelated context.Values.
// This is the happy-path invariant: orchestration sets it, the wrapper
// reads it back.
func TestLLMCallRecordingDeferral_RoundTrip(t *testing.T) {
	ctx := context.Background()
	if IsLLMCallRecordingDeferred(ctx) {
		t.Fatal("plain context.Background() must not carry the marker")
	}

	ctx = WithLLMCallRecordingDeferred(ctx)
	if !IsLLMCallRecordingDeferred(ctx) {
		t.Fatal("context returned by WithLLMCallRecordingDeferred must report true")
	}

	// Survive unrelated context wrapping (mirrors real-world propagation
	// through middleware that layers its own values on the same chain).
	type unrelatedKey struct{}
	nested := context.WithValue(ctx, unrelatedKey{}, "something-else")
	if !IsLLMCallRecordingDeferred(nested) {
		t.Fatal("marker must persist through unrelated context.WithValue wrapping")
	}

	// Derived ctx created with context.WithCancel must still carry the marker.
	withCancel, cancel := context.WithCancel(ctx)
	defer cancel()
	if !IsLLMCallRecordingDeferred(withCancel) {
		t.Fatal("marker must persist through context.WithCancel")
	}
}

// TestLLMCallRecordingDeferral_AbsentMarker is the regression guard for
// the default case. Any code path that does not explicitly opt in must
// report false; this is the condition that keeps InstrumentedAIClient
// recording agent_llm_call for background LLM calls.
func TestLLMCallRecordingDeferral_AbsentMarker(t *testing.T) {
	cases := []struct {
		name string
		ctx  context.Context
	}{
		{"background", context.Background()},
		{"todo", context.TODO()},
		{"with unrelated value", context.WithValue(context.Background(), struct{ K string }{"k"}, "v")},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if IsLLMCallRecordingDeferred(tc.ctx) {
				t.Fatalf("%s context must not report deferred", tc.name)
			}
		})
	}
}

// TestLLMCallRecordingDeferral_KeyCannotBeSpoofed locks in the
// collision-safety property: no external package can flip the flag to
// `true` because the context key is an unexported struct type. A
// "spoofed" marker built from a different key type MUST be ignored.
// This matters because if the flag were spoofable, a third party could
// silently suppress LLMCallRecorder emissions the agent was supposed
// to capture.
func TestLLMCallRecordingDeferral_KeyCannotBeSpoofed(t *testing.T) {
	type lookalikeKey struct{}
	ctx := context.WithValue(context.Background(), lookalikeKey{}, true)
	if IsLLMCallRecordingDeferred(ctx) {
		t.Fatal("context value set with a different (lookalike) key type must not be readable as the deferral marker")
	}

	// Same nominal name, different package path is already impossible by
	// Go's type identity rules; the struct{} key's type identity IS the
	// fully qualified package path. Document that invariant with a
	// compile-time check: the key type is not exported, so no external
	// package can even reference it.
	//
	// (This is also the reason we don't just use a string-valued key —
	// strings can collide.)
}

// TestLLMCallRecordingDeferral_NonBoolValueIgnored covers the narrow
// case where a future refactor might store a non-bool under the same
// key. The getter's type assertion with `v, _ := ...(bool)` returns
// false for any non-bool value, which is the safe default.
func TestLLMCallRecordingDeferral_NonBoolValueIgnored(t *testing.T) {
	ctx := context.WithValue(context.Background(), llmCallRecordingDeferredKey{}, "not-a-bool")
	if IsLLMCallRecordingDeferred(ctx) {
		t.Fatal("non-bool value stored under the deferral key must be treated as absent")
	}
}
