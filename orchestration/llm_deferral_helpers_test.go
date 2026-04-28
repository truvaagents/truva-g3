package orchestration

import (
	"context"
	"testing"

	"github.com/truvaagents/truva-g3/telemetry"
)

// TestLLMDeferralHelpers_GateOnDebugStore covers the graceful-fallback
// invariant across every recorder that owns a debugStore field and a
// deferLLMRecordingIfWeWillRecord helper. Each helper must:
//   - return ctx unchanged when debugStore is nil (wrapper stays the
//     authoritative recorder to avoid silent record loss), AND
//   - return ctx + marker when debugStore is wired (InstrumentedAIClient
//     skips its own agent_llm_call emission so exactly one record is
//     written).
//
// This single sub-test matrix covers Batches C–G of
// BUG_LLM_INTERACTION_DOUBLE_RECORDING.md. AIOrchestrator and PlanRefiner
// are covered by orchestrator_llm_deferral_test.go and
// plan_refinement_test.go respectively.
func TestLLMDeferralHelpers_GateOnDebugStore(t *testing.T) {
	fakeStore := &mockDebugStoreForRefinement{}

	cases := []struct {
		name    string
		withNil func() context.Context
		withSet func() context.Context
	}{
		{
			name: "AISynthesizer",
			withNil: func() context.Context {
				s := &AISynthesizer{}
				return s.deferLLMRecordingIfWeWillRecord(context.Background())
			},
			withSet: func() context.Context {
				s := &AISynthesizer{debugStore: fakeStore}
				return s.deferLLMRecordingIfWeWillRecord(context.Background())
			},
		},
		{
			name: "TieredCapabilityProvider",
			withNil: func() context.Context {
				p := &TieredCapabilityProvider{}
				return p.deferLLMRecordingIfWeWillRecord(context.Background())
			},
			withSet: func() context.Context {
				p := &TieredCapabilityProvider{debugStore: fakeStore}
				return p.deferLLMRecordingIfWeWillRecord(context.Background())
			},
		},
		{
			name: "ErrorAnalyzer",
			withNil: func() context.Context {
				e := &ErrorAnalyzer{}
				return e.deferLLMRecordingIfWeWillRecord(context.Background())
			},
			withSet: func() context.Context {
				e := &ErrorAnalyzer{debugStore: fakeStore}
				return e.deferLLMRecordingIfWeWillRecord(context.Background())
			},
		},
		{
			name: "MicroResolver",
			withNil: func() context.Context {
				m := &MicroResolver{}
				return m.deferLLMRecordingIfWeWillRecord(context.Background())
			},
			withSet: func() context.Context {
				m := &MicroResolver{debugStore: fakeStore}
				return m.deferLLMRecordingIfWeWillRecord(context.Background())
			},
		},
		{
			name: "ContextualReResolver",
			withNil: func() context.Context {
				r := &ContextualReResolver{}
				return r.deferLLMRecordingIfWeWillRecord(context.Background())
			},
			withSet: func() context.Context {
				r := &ContextualReResolver{debugStore: fakeStore}
				return r.deferLLMRecordingIfWeWillRecord(context.Background())
			},
		},
		{
			name: "LLMEventSummarizer",
			withNil: func() context.Context {
				s := &LLMEventSummarizer{}
				return s.deferLLMRecordingIfWeWillRecord(context.Background())
			},
			withSet: func() context.Context {
				s := &LLMEventSummarizer{debugStore: fakeStore}
				return s.deferLLMRecordingIfWeWillRecord(context.Background())
			},
		},
		{
			name: "LLMDistiller",
			withNil: func() context.Context {
				d := &LLMDistiller{}
				return d.deferLLMRecordingIfWeWillRecord(context.Background())
			},
			withSet: func() context.Context {
				d := &LLMDistiller{debugStore: fakeStore}
				return d.deferLLMRecordingIfWeWillRecord(context.Background())
			},
		},
		{
			name: "LLMConversationCompactor",
			withNil: func() context.Context {
				c := &LLMConversationCompactor{}
				return c.deferLLMRecordingIfWeWillRecord(context.Background())
			},
			withSet: func() context.Context {
				c := &LLMConversationCompactor{debugStore: fakeStore}
				return c.deferLLMRecordingIfWeWillRecord(context.Background())
			},
		},
		{
			name: "UserMemoryExtractionHook",
			withNil: func() context.Context {
				h := &UserMemoryExtractionHook{}
				return h.deferLLMRecordingIfWeWillRecord(context.Background())
			},
			withSet: func() context.Context {
				h := &UserMemoryExtractionHook{debugStore: fakeStore}
				return h.deferLLMRecordingIfWeWillRecord(context.Background())
			},
		},
		{
			name: "LLMActivityCompactor",
			withNil: func() context.Context {
				c := &LLMActivityCompactor{}
				return c.deferLLMRecordingIfWeWillRecord(context.Background())
			},
			withSet: func() context.Context {
				c := &LLMActivityCompactor{debugStore: fakeStore}
				return c.deferLLMRecordingIfWeWillRecord(context.Background())
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name+"/nil debugStore must NOT mark ctx", func(t *testing.T) {
			if telemetry.IsLLMCallRecordingDeferred(tc.withNil()) {
				t.Fatal("with nil debugStore the helper must return ctx unchanged; otherwise InstrumentedAIClient would skip recording and the call would vanish")
			}
		})
		t.Run(tc.name+"/set debugStore must mark ctx", func(t *testing.T) {
			if !telemetry.IsLLMCallRecordingDeferred(tc.withSet()) {
				t.Fatal("with debugStore set the helper must mark ctx so InstrumentedAIClient skips its agent_llm_call emission; otherwise we get the double-record bug")
			}
		})
	}
}
