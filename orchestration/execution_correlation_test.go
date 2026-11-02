package orchestration

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/truvaagents/truva-g3/core"
	"github.com/truvaagents/truva-g3/telemetry"
	"go.opentelemetry.io/otel/baggage"
)

func incomingExecutionContext(t *testing.T, ctx context.Context, call core.IncomingCall) context.Context {
	t.Helper()
	ctx, err := core.WithIncomingCall(ctx, call)
	if err != nil {
		t.Fatal(err)
	}
	return ctx
}

func TestExecutionCorrelationEntryPreservesRootAndIsolatesChild(t *testing.T) {
	for _, tc := range []struct {
		name, baggageRoot, incomingRoot, wantRoot string
		resume, wantParent                        bool
	}{
		{name: "coherent", baggageRoot: "root", incomingRoot: "root", wantRoot: "root", wantParent: true},
		{name: "header fallback", incomingRoot: "root", wantRoot: "root", wantParent: true},
		{name: "unknown root", wantRoot: "child", wantParent: true},
		{name: "root conflict", baggageRoot: "root", incomingRoot: "other-root", wantRoot: "root"},
		{name: "low level resume ignores approval", baggageRoot: "root", incomingRoot: "root", wantRoot: "root", resume: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := telemetry.WithBaggage(t.Context(), "request_id", "parent", "step_id", "step-18", "phase_number", "4", "plan_id", "parent-plan", "unrelated", "keep")
			if tc.baggageRoot != "" {
				ctx = telemetry.WithBaggage(ctx, "original_request_id", tc.baggageRoot)
			}
			ctx = core.WithPlanID(core.WithPhaseNumber(core.WithStepID(core.WithRequestID(ctx, "parent"), "step-18"), 4), "parent-plan")
			ctx = core.WithOriginalRequestID(ctx, "stale-core-root")
			ctx = incomingExecutionContext(t, ctx, core.IncomingCall{RequestID: "parent", StepID: "step-18", OriginalRequestID: tc.incomingRoot})
			if tc.resume {
				ctx = WithResumeMode(ctx, "checkpoint")
			}
			o := &AIOrchestrator{logger: &TestLogger{}}
			child, correlation := o.beginExecutionCorrelation(ctx, "child")
			if correlation.OriginalRequestID != tc.wantRoot || (correlation.Parent.RequestID != "") != tc.wantParent {
				t.Fatalf("correlation = %+v", correlation)
			}
			if tc.wantParent && correlation.Parent != (executionParent{RequestID: "parent", StepID: "step-18"}) {
				t.Fatalf("wrong immediate caller: %+v", correlation.Parent)
			}
			if _, present := core.IncomingCallFromContext(child); present {
				t.Fatal("incoming caller was not consumed")
			}
			bag := telemetry.GetBaggage(child)
			if core.GetRequestID(child) != "child" || GetRequestID(child) != "child" || core.GetOriginalRequestID(child) != tc.wantRoot || bag["request_id"] != "child" ||
				bag["original_request_id"] != tc.wantRoot || bag["unrelated"] != "keep" || core.GetStepID(child) != "" ||
				core.GetPhaseNumber(child) != 0 || core.GetPlanID(child) != "" || bag["phase_number"] != "" || bag["step_id"] != "" || bag["plan_id"] != "" {
				t.Fatalf("child retained caller identity: core=%q bag=%v", core.GetRequestID(child), bag)
			}
			if core.GetPhaseNumber(ctx) != 4 || core.GetOriginalRequestID(ctx) != "stale-core-root" || telemetry.GetBaggage(ctx)["request_id"] != "parent" {
				t.Fatal("caller context changed")
			}
		})
	}
}

func TestSaturatedManagedRootSurvivesDebugWritersAndDetachedHook(t *testing.T) {
	const requestID = "child"
	const mainTrace = "11111111111111111111111111111111"
	const backgroundTrace = "22222222222222222222222222222222"
	const payload = "exact *payload* 中文\n{\"value\":\"a<b\"}"
	rootID := strings.Repeat("r", core.MaxIncomingCallIDLength)
	for _, writer := range []string{"memory", "redis-orchestration", "redis-telemetry"} {
		t.Run(writer, func(t *testing.T) {
			caller := correlationCapacityContext(t, 18, 500)
			caller = telemetry.WithoutBaggageMember(caller, "original_request_id")
			caller = incomingExecutionContext(t, caller, core.IncomingCall{RequestID: "parent", StepID: "step-1", OriginalRequestID: rootID})
			o := &AIOrchestrator{logger: &core.NoOpLogger{}}
			ctx, correlation := o.beginExecutionCorrelation(caller, requestID)
			if correlation.OriginalRequestID != rootID || core.GetOriginalRequestID(ctx) != rootID {
				t.Fatal("managed entry lost the exact typed root")
			}
			if telemetry.GetBaggage(ctx)["original_request_id"] != "" {
				t.Fatal("fixture did not omit the root at baggage byte capacity")
			}
			var store LLMDebugStore = NewMemoryLLMDebugStore()
			recorder := NewLLMCallRecorderAdapter(store)
			if writer != "memory" {
				mr, redisStore := setupRedisLLMDebugTestStore(t)
				store = redisStore
				var err error
				recorder, err = telemetry.NewRedisLLMCallRecorderWithClient(redisStore.client, defaultRedisKeyspace())
				if err != nil {
					t.Fatal(err)
				}
				if err := redisStore.PreserveRetention(t.Context(), requestID, 14*24*time.Hour); err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() {
					for _, key := range []string{redisStore.metaKey(requestID), redisStore.interactionsKey(requestID)} {
						if got := mr.TTL(key); got != 14*24*time.Hour {
							t.Errorf("debug root writes changed %s TTL to %v", key, got)
						}
					}
				})
			}
			write := func(callCtx context.Context, traceID string, generic bool) error {
				if generic {
					return recorder.RecordLLMCall(callCtx, requestID, telemetry.LLMCallRecord{
						CallType: "agent_llm_call", TraceID: traceID, Prompt: payload, Response: payload,
						ReasoningContent: payload, Success: true,
					})
				}
				return store.RecordInteraction(callCtx, requestID, LLMInteraction{
					Type: "plan_generation", TraceID: traceID, Prompt: payload, Response: payload,
					ReasoningContent: payload, Success: true,
				})
			}
			if err := write(ctx, mainTrace, writer == "redis-telemetry"); err != nil {
				t.Fatal(err)
			}
			type observed struct {
				requestID, typedRoot, baggageRoot string
				err                               error
			}
			done := make(chan observed, 1)
			client := &mockAIClient{generateFunc: func(async context.Context, _ string, _ *core.AIOptions) (*core.AIResponse, error) {
				writeErr := write(async, backgroundTrace, writer != "redis-telemetry")
				done <- observed{requestID: core.GetRequestID(async), typedRoot: core.GetOriginalRequestID(async),
					baggageRoot: telemetry.GetBaggage(async)["original_request_id"], err: writeErr}
				return &core.AIResponse{Content: `[]`}, writeErr
			}}
			hook, err := NewKnowledgeExtractionHook(&core.MockSharedKnowledge{}, &core.MockEmbeddingClient{}, client, "test-agent", "test-domain")
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(hook.Close)
			response, err := hook.AfterSynthesis(ctx, &core.PipelineContext{Request: "request"}, payload)
			if err != nil || response != payload {
				t.Fatalf("hook changed response or failed: %q, %v", response, err)
			}
			select {
			case actual := <-done:
				if actual.err != nil || actual.requestID != requestID || actual.typedRoot != rootID || actual.baggageRoot != "" {
					t.Fatalf("detached context lost request-owned root: %+v", actual)
				}
			case <-time.After(3 * time.Second):
				t.Fatal("detached knowledge extraction did not finish")
			}
			hook.Close()
			record, err := store.GetRecord(t.Context(), requestID)
			if err != nil || record.RequestID != requestID || record.OriginalRequestID != rootID || record.TraceID != mainTrace || len(record.Interactions) != 2 {
				t.Fatalf("main/detached debug facts changed: %+v, %v", record, err)
			}
			for index, interaction := range record.Interactions {
				wantTrace := mainTrace
				if index > 0 {
					wantTrace = backgroundTrace
				}
				if interaction.TraceID != wantTrace || interaction.Prompt != payload || interaction.Response != payload || interaction.ReasoningContent != payload {
					t.Fatalf("call %d trace/payload changed: %+v", index, interaction)
				}
			}
		})
	}
}

func correlationCapacityContext(t *testing.T, members, valueSize int) context.Context {
	t.Helper()
	values := make([]baggage.Member, 0, members)
	for i := 0; i < members-2; i++ {
		member, err := baggage.NewMember(fmt.Sprintf("key%02d", i), strings.Repeat("x", valueSize))
		if err != nil {
			t.Fatal(err)
		}
		values = append(values, member)
	}
	for key, value := range map[string]string{"request_id": "parent", "original_request_id": "root"} {
		member, err := baggage.NewMember(key, value)
		if err != nil {
			t.Fatal(err)
		}
		values = append(values, member)
	}
	bag, err := baggage.New(values...)
	if err != nil {
		t.Fatal(err)
	}
	return baggage.ContextWithBaggage(t.Context(), bag)
}

func TestExecutionCorrelationAtBaggageCapacity(t *testing.T) {
	for _, tc := range []struct {
		name, id   string
		members    int
		valueSize  int
		wantAbsent bool
	}{
		{name: "replace at item limit", id: "child", members: 64, valueSize: 1},
		{name: "total size rejection", id: strings.Repeat("c", 512), members: 18, valueSize: 500, wantAbsent: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := correlationCapacityContext(t, tc.members, tc.valueSize)
			ctx = incomingExecutionContext(t, ctx, core.IncomingCall{RequestID: "parent", StepID: "step-1", OriginalRequestID: "root"})
			logger := &TestLogger{}
			o := &AIOrchestrator{logger: logger}
			ctx, correlation := o.beginExecutionCorrelation(ctx, tc.id)
			got := telemetry.GetBaggage(ctx)["request_id"]
			if tc.wantAbsent && got != "" || !tc.wantAbsent && got != tc.id {
				t.Fatalf("request baggage = %q", got)
			}
			if core.GetRequestID(ctx) != tc.id || correlation.RequestID != tc.id || correlation.OriginalRequestID != "root" {
				t.Fatal("typed identity lost at capacity")
			}
			if len(logger.GetLogsByOperation("execution_correlation")) == 0 && tc.wantAbsent {
				t.Fatal("exact-set rejection was not observed")
			}
		})
	}
}

type correlationRoundTripper func(*http.Request) (*http.Response, error)

func (fn correlationRoundTripper) RoundTrip(r *http.Request) (*http.Response, error) {
	return fn(r)
}

func TestManagedOutboundCorrelationFallbackAndOwnership(t *testing.T) {
	for _, managed := range []string{"matching", "unmanaged", "mismatched"} {
		t.Run(managed, func(t *testing.T) {
			ctx := WithRequestID(telemetry.WithBaggage(t.Context(), "request_id", "legacy", "original_request_id", "legacy-root"), "current")
			if managed != "unmanaged" {
				owner := "current"
				if managed == "mismatched" {
					owner = "different-execution"
				}
				ctx = withRequestCorrelation(ctx, requestCorrelation{RequestID: owner, OriginalRequestID: "typed-root"})
			}
			if managed == "matching" {
				ctx = telemetry.WithoutBaggageMember(ctx, "request_id")
				ctx = telemetry.WithoutBaggageMember(ctx, "original_request_id")
			}
			ctx = core.WithStepID(ctx, "step-2")
			var header http.Header
			e := &SmartExecutor{httpClient: &http.Client{Transport: correlationRoundTripper(func(r *http.Request) (*http.Response, error) {
				header = r.Header.Clone()
				return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"success":true,"data":{"note":"a<b *exact*"}}`)), Header: make(http.Header)}, nil
			})}}
			if _, _, err := e.callComponentWithBody(ctx, "http://component.test/call", []byte(`{"note":"unchanged"}`)); err != nil {
				t.Fatal(err)
			}
			wantID, wantRoot := "legacy", "legacy-root"
			if managed == "matching" {
				wantID, wantRoot = "current", "typed-root"
			}
			if header.Get("X-TruvaG3-Request-ID") != wantID || header.Get("X-TruvaG3-Original-Request-ID") != wantRoot || header.Get("X-TruvaG3-Step-ID") != "step-2" {
				t.Fatalf("outbound correlation = %v", header)
			}
		})
	}
}

func TestCheckpointCorrelationIsAuthoritativeAcrossResume(t *testing.T) {
	for _, parent := range []executionParent{{RequestID: "parent", StepID: "step-18"}, {}} {
		ctx := incomingExecutionContext(t, telemetry.WithBaggage(t.Context(), "original_request_id", "approval-root"), core.IncomingCall{RequestID: "approval", StepID: "approval-step"})
		cp := &ExecutionCheckpoint{CheckpointID: "checkpoint", RequestID: "suspended", OriginalRequestID: "root", ParentRequestID: parent.RequestID, ParentStepID: parent.StepID, Status: CheckpointStatusApproved}
		for i := 0; i < 2; i++ {
			resume, end, err := BuildResumeContext(ctx, cp)
			if err != nil {
				t.Fatal(err)
			}
			o := &AIOrchestrator{}
			resume, correlation := o.beginExecutionCorrelation(resume, fmt.Sprintf("resumed-%d", i))
			end()
			if correlation.OriginalRequestID != "root" || correlation.Parent != parent || telemetry.GetBaggage(resume)["original_request_id"] != "root" {
				t.Fatalf("approval replaced checkpoint correlation: %+v", correlation)
			}
			cp = &ExecutionCheckpoint{CheckpointID: "successor", RequestID: correlation.RequestID, Status: CheckpointStatusApproved}
			applyRunSnapshot(cp, executionRunSnapshot{Correlation: correlation})
		}
	}
}

func TestCheckpointCorrelationLegacyInvalidAndSaturated(t *testing.T) {
	for _, tc := range []struct {
		name, root, parent, step, wantRoot string
	}{
		{name: "legacy", wantRoot: "suspended"},
		{name: "invalid root", root: "bad\nroot", wantRoot: "suspended"},
		{name: "invalid parent", root: "root", parent: "bad\nparent", wantRoot: "root"},
		{name: "orphan step", root: "root", step: "step-1", wantRoot: "root"},
		{name: "self parent", root: "root", parent: "suspended", wantRoot: "root"},
		{name: "large root at capacity", root: strings.Repeat("r", 512), wantRoot: strings.Repeat("r", 512)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cp := &ExecutionCheckpoint{CheckpointID: "checkpoint", RequestID: "suspended", OriginalRequestID: tc.root, ParentRequestID: tc.parent, ParentStepID: tc.step, Status: CheckpointStatusApproved}
			ctx := correlationCapacityContext(t, 18, 500)
			resume, end, err := BuildResumeContext(ctx, cp)
			if err != nil {
				t.Fatal(err)
			}
			defer end()
			o := &AIOrchestrator{}
			_, correlation := o.beginExecutionCorrelation(resume, "resumed")
			if correlation.OriginalRequestID != tc.wantRoot || correlation.Parent != (executionParent{}) {
				t.Fatalf("invalid/legacy checkpoint identity = %+v", correlation)
			}
		})
	}
}

func TestRunSnapshotCorrelationAndOwnership(t *testing.T) {
	correlation := requestCorrelation{RequestID: "request", OriginalRequestID: "root", Parent: executionParent{RequestID: "parent", StepID: "step-1"}}
	state := &executionRunState{Correlation: correlation}
	if got := state.Snapshot(); got.Correlation != correlation || got.requiresPersistence() {
		t.Fatalf("empty-phase snapshot = %+v", got)
	}
	for _, owner := range []string{"request", "unrelated"} {
		checkpoint := &ExecutionCheckpoint{RequestID: owner, OriginalRequestID: "prior-root", ParentRequestID: "prior-parent"}
		applyRunSnapshot(checkpoint, state.Snapshot())
		if owner == "request" {
			if checkpoint.OriginalRequestID != "root" || checkpoint.ParentRequestID != "parent" || checkpoint.ParentStepID != "step-1" {
				t.Fatalf("snapshot lost correlation: %+v", checkpoint)
			}
		} else if checkpoint.OriginalRequestID != "prior-root" || checkpoint.ParentRequestID != "prior-parent" {
			t.Fatal("snapshot borrowed an unrelated execution")
		}
	}
	state.Phase.Result = &phaseLoopResult{CombinedResult: &ExecutionResult{PhaseCount: 2, Steps: []StepResult{{StepID: "step-2", Success: true}}}}
	if got := state.Snapshot(); got.Correlation != correlation || got.PhaseNumber != 2 || len(got.AccumulatedResults) != 1 {
		t.Fatalf("phase snapshot = %+v", got)
	}
}

func TestExecutionCorrelationSurvivesRecordedSnapshotsAndSummary(t *testing.T) {
	store := &terminalRecordStore{NoOpExecutionStore: NewNoOpExecutionStore()}
	o := NewAIOrchestrator(NewDefaultOrchestratorConfig(), nil, nil)
	o.SetExecutionStore(store)
	t.Cleanup(func() { shutdownRequiredHookFixture(t, o) })
	correlation := requestCorrelation{RequestID: "managed", OriginalRequestID: "root", Parent: executionParent{RequestID: "parent", StepID: "step-17"}}
	ctx := withRequestCorrelation(WithRequestID(t.Context(), "managed"), correlation)
	for _, id := range []string{"managed", "different-direct-execution"} {
		o.storeExecutionWithFinalResponseAsync(ctx, "request", id, nil, &ExecutionResult{Success: false}, "exact *response* 中文")
	}
	o.executionWg.Wait()
	store.mu.Lock()
	defer store.mu.Unlock()
	for _, record := range store.records {
		if record.RequestID == "managed" {
			encoded, err := json.Marshal(record)
			if err != nil {
				t.Fatal(err)
			}
			var restored StoredExecution
			if err := json.Unmarshal(encoded, &restored); err != nil {
				t.Fatal(err)
			}
			summary := executionSummaryFromStored(&restored)
			if summary.ParentRequestID != "parent" || summary.ParentStepID != "step-17" || restored.OriginalRequestID != "root" || restored.FinalResponse == nil || *restored.FinalResponse != "exact *response* 中文" {
				t.Fatalf("recorded facts changed: %+v", restored)
			}
		} else if record.ParentRequestID != "" || record.ParentStepID != "" {
			t.Fatal("unmanaged producer inherited a parent")
		}
	}
	if len(store.records) != 2 {
		t.Fatalf("stored %d snapshots", len(store.records))
	}
}

func TestRealPlanningDebugUsesChildIdentity(t *testing.T) {
	for _, saturated := range []bool{false, true} {
		t.Run(fmt.Sprintf("byte_saturation_%t", saturated), func(t *testing.T) {
			testRealPlanningDebugUsesChildIdentity(t, saturated)
		})
	}
}

func testRealPlanningDebugUsesChildIdentity(t *testing.T, saturated bool) {
	o := newAfterPlanningContractOrchestrator(t)
	t.Cleanup(func() { shutdownRequiredHookFixture(t, o) })
	client := &promptCapturingAIClient{responses: []string{`{"plan_id":"child-plan","terminal":true,"steps":[]}`}}
	o.aiClient = client
	debug := &mockLLMDebugStore{}
	o.SetLLMDebugStore(debug)
	logger := &TestLogger{}
	o.SetLogger(logger)
	ctx := telemetry.WithBaggage(t.Context(), "request_id", "parent", "original_request_id", "root", "phase_number", "4", "plan_id", "parent-plan", "step_id", "step-18")
	if saturated {
		ctx = correlationCapacityContext(t, 18, 500)
		o.config.RequestIDPrefix = strings.Repeat("c", 480)
	}
	ctx = incomingExecutionContext(t, ctx, core.IncomingCall{RequestID: "parent", StepID: "step-18", OriginalRequestID: "root"})
	state, err := o.beginRequestRun(ctx, requestRunInput{Request: "request"})
	if err != nil {
		t.Fatal(err)
	}
	defer state.Span.End()
	if saturated && telemetry.GetBaggage(state.Context)["request_id"] != "" {
		t.Fatal("fixture did not force exact request-ID baggage rejection")
	}
	plan, err := o.generateExecutionPlan(state.Context, "request", state.Correlation.RequestID)
	if err != nil {
		t.Fatal(err)
	}
	// Planning evidence is appended by recordDebugInteraction's debugWg,
	// independently of execution snapshot persistence's executionWg.
	o.debugWg.Wait()
	debug.mu.Lock()
	defer debug.mu.Unlock()
	if len(debug.interactions) == 0 || plan.PlanID != "child-plan" {
		t.Fatal("actual planning/debug path was not exercised")
	}
	for i, interaction := range debug.interactions {
		if debug.requestIDs[i] != state.Correlation.RequestID || interaction.PhaseNumber == 4 || interaction.Response != client.responses[0] {
			t.Fatalf("debug attributed to parent or response changed: %+v", interaction)
		}
		if bag := telemetry.GetBaggage(debug.contexts[i]); bag["phase_number"] == "4" || bag["plan_id"] == "parent-plan" || bag["step_id"] != "" {
			t.Fatalf("debug context retains parent: %v", bag)
		}
	}
	logs := logger.GetLogsByOperation("process_request")
	if len(logs) != 1 || logs[0].Fields["parent_request_id"] != "parent" || logs[0].Fields["parent_step_id"] != "step-18" {
		t.Fatalf("request-start parent evidence missing: %v", logs)
	}
	if !saturated && telemetry.GetBaggage(ctx)["plan_id"] != "parent-plan" {
		t.Fatal("caller context was changed")
	}
}

func TestExecutionParentRoundTripsWithoutAncestorRetentionPromotion(t *testing.T) {
	config := DefaultExecutionStoreConfig()
	config.TTL, config.ErrorTTL = time.Hour, 8*time.Hour
	for _, backend := range []string{"provider", "redis"} {
		t.Run(backend, func(t *testing.T) {
			var store ExecutionStore
			var ttl func(string) time.Duration
			if backend == "provider" {
				provider := newMockStorageProvider()
				impl := NewExecutionStoreWithProvider(provider, config, nil).(*executionStoreImpl)
				store = impl
				ttl = func(id string) time.Duration { return provider.ttls[impl.recordKey(id)] }
			} else {
				server, impl := newRedisExecutionConversationTestStore(t, config)
				store = impl
				ttl = func(id string) time.Duration { return server.TTL(impl.recordKey(id)) }
			}
			root := &StoredExecution{RequestID: "root", Result: &ExecutionResult{Success: true}, CreatedAt: time.Now()}
			parent := &StoredExecution{RequestID: "parent", Result: &ExecutionResult{Success: true}, CreatedAt: time.Now()}
			child := &StoredExecution{RequestID: "child", OriginalRequestID: "root", ParentRequestID: "parent", ParentStepID: "step-18", Result: &ExecutionResult{Success: false}, CreatedAt: time.Now()}
			for _, record := range []*StoredExecution{root, parent, child} {
				if err := store.Store(t.Context(), record); err != nil {
					t.Fatal(err)
				}
			}
			stored, err := store.Get(t.Context(), "child")
			if err != nil || stored == nil || stored.ParentRequestID != "parent" || stored.ParentStepID != "step-18" {
				t.Fatalf("round trip: stored=%+v err=%v", stored, err)
			}
			if ttl("root") != config.ErrorTTL || ttl("parent") != config.TTL {
				t.Fatalf("root TTL=%v parent TTL=%v; only existing root promotion should apply", ttl("root"), ttl("parent"))
			}
		})
	}
}

func TestRequestCorrelationValueCopiesAreIndependent(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
	defer cancel()
	correlation := requestCorrelation{RequestID: "child", Parent: executionParent{RequestID: "parent"}}
	ctx = withRequestCorrelation(ctx, correlation)
	correlation.Parent.RequestID = "mutated"
	got, ok := requestCorrelationFromContext(ctx)
	if !ok || got.Parent.RequestID != "parent" {
		t.Fatal("request correlation shared mutable state")
	}
	cancel()
	if ctx.Err() != context.Canceled {
		t.Fatal("correlation lost cancellation")
	}
}
