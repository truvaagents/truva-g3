package orchestration

import (
	"context"
	"testing"

	"github.com/truvaagents/truva-g3/telemetry"
)

// UserMemoryExtractionHook still delegates to extractor and reconciler
// interfaces that do not use aiInvocation. Its local deferral helper remains
// necessary until those nested contracts become request-aware.
func TestUserMemoryExtractionHook_DeferLLMRecording_GatesOnDebugStore(t *testing.T) {
	t.Run("nil debug store returns context unchanged", func(t *testing.T) {
		hook := &UserMemoryExtractionHook{}
		if telemetry.IsLLMCallRecordingDeferred(hook.deferLLMRecordingIfWeWillRecord(context.Background())) {
			t.Fatal("nil debug store must not defer wrapper recording")
		}
	})

	t.Run("configured debug store marks context", func(t *testing.T) {
		hook := &UserMemoryExtractionHook{debugStore: &mockDebugStoreForRefinement{}}
		if !telemetry.IsLLMCallRecordingDeferred(hook.deferLLMRecordingIfWeWillRecord(context.Background())) {
			t.Fatal("configured debug store must defer wrapper recording")
		}
	})
}
