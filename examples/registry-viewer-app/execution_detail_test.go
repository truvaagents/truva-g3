package main

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/truvaagents/truva-g3/core"
	"github.com/truvaagents/truva-g3/orchestration"
)

type detailSectionStore struct {
	orchestration.LLMDebugStore
	calls  int
	record *orchestration.LLMDebugRecord
	err    error
}

func (store *detailSectionStore) GetRecord(context.Context, string) (*orchestration.LLMDebugRecord, error) {
	store.calls++
	return store.record, store.err
}

func withDetailSectionStore(t *testing.T, store *detailSectionStore) {
	t.Helper()
	previousStore, previousMock := llmDebugStore, useMock
	llmDebugStore, useMock = store, false
	t.Cleanup(func() { llmDebugStore, useMock = previousStore, previousMock })
}

func TestExecutionOverviewDefersEvidenceWithoutChangingRecord(t *testing.T) {
	store := &detailSectionStore{err: fmt.Errorf("unexpected optional store read")}
	withDetailSectionStore(t, store)
	const payload = "EVIDENCE_ONLY_ON_DEMAND"
	step := RoutingStep{StepID: "step-1", AgentName: "test-tool", Instruction: "Inspect input", Metadata: map[string]interface{}{"capability": "inspect", "parameters": map[string]interface{}{"value": payload}}}
	result := StepResult{StepID: "step-1", AgentName: "test-tool", Success: true, Response: payload, Parameters: map[string]interface{}{"value": payload}, Dispatches: json.RawMessage(`[{"response":"` + payload + `"}]`), Metadata: map[string]interface{}{"value": payload}}
	plan := &RoutingPlan{Steps: []RoutingStep{step}}
	finalResponse := payload
	execution := &StoredExecution{
		RequestID: "request-1", ParentRequestID: "parent-1", ParentStepID: "step-parent",
		OriginalRequest: "Inspect the input", Plan: plan, PhasePlans: []*RoutingPlan{plan},
		Result:        &ExecutionResult{Success: true, Steps: []StepResult{result}},
		Checkpoint:    &HITLCheckpoint{Plan: plan, CurrentStep: &step, CurrentStepResult: &result, CompletedSteps: []StepResult{result}, StepResults: map[string]*StepResult{"step-1": &result}, ResolvedParameters: map[string]interface{}{"value": payload}},
		FinalResponse: &finalResponse,
		PipelineHooks: []orchestration.PipelineHookExecution{{HookName: "example", Error: payload}},
	}
	before, err := json.Marshal(execution)
	if err != nil {
		t.Fatal(err)
	}
	response := buildExecutionDetailSection(t.Context(), execution, "overview")
	encoded, err := json.Marshal(response)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), payload) || store.calls != 0 {
		t.Fatalf("overview eagerly read or included evidence: optionalReads=%d body=%s", store.calls, encoded)
	}
	if response["detail_level"] != "overview" || response["payloads_deferred"] != true || response["parent_request_id"] != "parent-1" {
		t.Fatalf("overview omitted its projection/relationship facts: %+v", response)
	}
	dag, ok := response["dag"].(*DAGResponse)
	if !ok || len(dag.Nodes) != 1 || dag.Nodes[0].Status != "completed" {
		t.Fatalf("overview lost the actual step outcome: %+v", response["dag"])
	}
	after, err := json.Marshal(execution)
	if err != nil || string(before) != string(after) {
		t.Fatal("read-side projection changed the original stored execution")
	}
}

func TestExecutionDetailSectionsOnlyLoadRequestedEvidence(t *testing.T) {
	for _, section := range []string{"overview", "steps", "skills", "raw", "llm", "pre", "post", "flow"} {
		t.Run(section, func(t *testing.T) {
			store := &detailSectionStore{record: &orchestration.LLMDebugRecord{Interactions: []orchestration.LLMInteraction{
				{Type: "before", HookPhase: "pre", Prompt: "PRE_PAYLOAD"},
				{Type: "plan_generation", Prompt: "PLAN_PAYLOAD"},
				{Type: "after", HookPhase: "post", Prompt: "POST_PAYLOAD"},
			}}}
			withDetailSectionStore(t, store)
			response := buildExecutionDetailSection(t.Context(), &StoredExecution{RequestID: "request-1"}, section)
			readsLLM := section == "llm" || section == "pre" || section == "post" || section == "flow"
			if readsLLM != (store.calls == 1) || store.calls > 1 {
				t.Fatalf("section=%s optionalReads=%d", section, store.calls)
			}
			if response["detail_level"] != section {
				t.Fatalf("wrong section identity: %+v", response)
			}
			if readsLLM {
				interactions := response["llm_interactions"].([]orchestration.LLMInteraction)
				if section == "pre" || section == "post" {
					if len(interactions) != 1 || interactions[0].HookPhase != section {
						t.Fatalf("section leaked unrequested interaction payloads: %+v", interactions)
					}
				} else if len(interactions) != 3 {
					t.Fatalf("requested complete flow lost interactions: %+v", interactions)
				}
			}
		})
	}
}

func TestExecutionDetailHTTPSectionValidationAndProjection(t *testing.T) {
	previousMock := useMock
	useMock = true
	t.Cleanup(func() { useMock = previousMock })
	requestID := "orch-1705312800123456789"
	for _, section := range []string{"overview", "steps", "raw", "unsupported"} {
		recorder := httptest.NewRecorder()
		handleExecution(recorder, httptest.NewRequest(http.MethodGet, "/api/executions/"+requestID+"/unified?section="+section, nil))
		if section == "unsupported" {
			if recorder.Code != http.StatusBadRequest {
				t.Fatalf("invalid section status=%d", recorder.Code)
			}
			continue
		}
		var response map[string]interface{}
		if recorder.Code != http.StatusOK || json.Unmarshal(recorder.Body.Bytes(), &response) != nil || response["detail_level"] != section {
			t.Fatalf("section=%s status=%d body=%s", section, recorder.Code, recorder.Body.String())
		}
		if section == "raw" && response["raw_execution_json"] == nil {
			t.Fatal("raw section did not preserve the original execution")
		}
	}
}

func TestExecutionOverviewPreviewKeepsUTF8Boundary(t *testing.T) {
	value := strings.Repeat("a", viewerOverviewTextBytes-1) + "é" + "rest"
	preview := executionOverviewPreview(value)
	if !utf8.ValidString(preview) || !strings.HasSuffix(preview, "…") || strings.Contains(preview, "rest") {
		t.Fatalf("invalid bounded overview preview %q", preview)
	}
}

func TestExecutionRawSectionPreservesStoredJSONFromOneRead(t *testing.T) {
	const original = "{\n  \"request_id\": \"raw-execution\", \"original_request\": \"Inspect a<b\", \"new_framework_field\": {\"identifier\": 9007199254740993}, \"result\": {\"steps\": [{\"step_id\": \"step-1\", \"response\": {\"identifier\": 9007199254740993}}]}\n}"
	for _, encoding := range []string{"legacy", "plain", "gzip"} {
		t.Run(encoding, func(t *testing.T) {
			client := setupLLMMetadataTest(t)
			previousKeys := viewerExecutionKeys
			keyspace, err := core.NewRedisKeyspace("exact-raw-section")
			if err != nil {
				t.Fatal(err)
			}
			viewerExecutionKeys = orchestration.NewRedisExecutionDebugKeys(keyspace)
			t.Cleanup(func() { viewerExecutionKeys = previousKeys })
			stored := []byte(original)
			if encoding == "plain" {
				stored = append([]byte{0}, stored...)
			} else if encoding == "gzip" {
				var compressed bytes.Buffer
				writer := gzip.NewWriter(&compressed)
				if _, err := writer.Write(stored); err != nil {
					t.Fatal(err)
				}
				if err := writer.Close(); err != nil {
					t.Fatal(err)
				}
				stored = append([]byte{1}, compressed.Bytes()...)
			}
			if err := client.Set(t.Context(), viewerExecutionKeys.Record("raw-execution"), stored, 0).Err(); err != nil {
				t.Fatal(err)
			}
			hook := &metadataReadCommands{}
			client.AddHook(hook)
			recorder := httptest.NewRecorder()
			handleExecution(recorder, httptest.NewRequest(http.MethodGet, "/api/executions/raw-execution/unified?section=raw", nil))
			var response struct {
				Raw string `json:"raw_execution_json"`
			}
			if recorder.Code != http.StatusOK || json.Unmarshal(recorder.Body.Bytes(), &response) != nil || response.Raw != original {
				t.Fatalf("raw section changed evidence: status=%d body=%s", recorder.Code, recorder.Body.String())
			}
			hook.mu.Lock()
			defer hook.mu.Unlock()
			reads := 0
			for _, command := range hook.commands {
				if command == "get" {
					reads++
				}
				if command == "lrange" || command == "hmget" {
					t.Fatalf("raw section loaded unrelated evidence: %s", command)
				}
			}
			if reads != 1 {
				t.Fatalf("raw/header did not share a single snapshot read: %d", reads)
			}
		})
	}
}

func TestExecutionFlowAndHITLKeepSnapshotAndCurrentStateDistinct(t *testing.T) {
	store := &detailSectionStore{}
	withDetailSectionStore(t, store)
	previousCache, previousClient, previousProtection := servicesCache.Load(), hitlHTTPClient, viewerProtection
	t.Cleanup(func() {
		servicesCache.Store(previousCache)
		hitlHTTPClient, viewerProtection = previousClient, previousProtection
	})
	viewerProtection = nil
	servicesCache.Store(&servicesCacheEntry{expires: time.Now().Add(time.Minute), services: []ServiceInfo{
		{Name: "owning-agent", Type: "agent", Address: "owner.invalid", Port: 8080},
		{Name: "unrelated-agent", Type: "agent", Address: "unrelated.invalid", Port: 8080},
	}})
	type markerKey struct{}
	ctx := context.WithValue(t.Context(), markerKey{}, "inherited-request-context")
	ownerCalls := 0
	hitlHTTPClient = &http.Client{Transport: protectedViewerRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		ownerCalls++
		if request.URL.Host != "owner.invalid:8080" || request.Context().Value(markerKey{}) != "inherited-request-context" {
			t.Fatalf("checkpoint query left its owner/request context: %s", request.URL)
		}
		body := `{"checkpoints":[]}` // The real list endpoint only returns pending checkpoints.
		switch request.URL.Path {
		case "/hitl/checkpoints":
			if request.URL.Query().Get("request_id") != "request-1" {
				t.Fatalf("unexpected request lookup: %s", request.URL)
			}
		case "/hitl/checkpoints/cp-1":
			body = `{"checkpoint_id":"cp-1","request_id":"request-1","status":"approved"}`
		default:
			t.Fatalf("unexpected checkpoint lookup: %s", request.URL)
		}
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(body))}, nil
	})}
	execution := &StoredExecution{RequestID: "request-1", AgentName: "owning-agent", Interrupted: true,
		Checkpoint: &HITLCheckpoint{CheckpointID: "cp-1", RequestID: "request-1", Status: "pending"},
		Metadata:   map[string]string{"plan_regeneration_events": `[{"phase_number":2}]`},
	}
	flow := buildExecutionDetailSection(ctx, execution, "flow")
	flowLifecycle, ok := flow["hitl_lifecycle"].(*HITLLifecycle)
	if !ok || flow["has_hitl_data"] != true || len(flow["hitl_checkpoints"].([]HITLCheckpoint)) != 1 || flowLifecycle.CurrentStatusSource != "stored_interruption_snapshot" || ownerCalls != 0 {
		t.Fatalf("flow lost the stored checkpoint or queried live state: %+v ownerCalls=%d", flow, ownerCalls)
	}
	if flow["metadata"].(map[string]string)["plan_regeneration_events"] != execution.Metadata["plan_regeneration_events"] {
		t.Fatal("explicit full flow lost execution metadata")
	}
	hitl := buildExecutionDetailSection(ctx, execution, "hitl")
	lifecycle, ok := hitl["hitl_lifecycle"].(*HITLLifecycle)
	if !ok || lifecycle.CurrentStatus != "approved" || lifecycle.CurrentStatusSource != "agent_checkpoint_api" || lifecycle.SnapshotStatus != "pending" || ownerCalls != 2 {
		t.Fatalf("checkpoint snapshot replaced current evidence: %+v calls=%d", lifecycle, ownerCalls)
	}
	if checkpoints := hitl["hitl_checkpoints"].([]HITLCheckpoint); len(checkpoints) != 1 || checkpoints[0].Status != "approved" {
		t.Fatalf("display list preferred stale snapshot: %+v", checkpoints)
	}
	steps := buildExecutionDetailSection(ctx, execution, "steps")
	if steps["metadata"].(map[string]string)["plan_regeneration_events"] == "" || store.calls != 1 {
		t.Fatal("step detail omitted metadata or an unrelated section read LLM bodies")
	}
}

func TestExecutionHITLSectionResolvesResumeAndKeepsUnavailableStateUnknown(t *testing.T) {
	for _, test := range []struct {
		name, pendingBody, exactBody, wantStatus string
		wantExact, wantWarning                   bool
	}{
		{name: "resolved resume", pendingBody: `{"checkpoints":[]}`, exactBody: `{"checkpoint_id":"cp-1","status":"completed"}`, wantStatus: "completed", wantExact: true},
		{name: "pending already loaded", pendingBody: `{"checkpoints":[{"checkpoint_id":"cp-1","status":"pending"}]}`, wantStatus: "pending"},
		{name: "wrong exact response", pendingBody: `{"checkpoints":[]}`, exactBody: `{"checkpoint_id":"another-checkpoint","status":"approved"}`, wantExact: true, wantWarning: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			withDetailSectionStore(t, &detailSectionStore{})
			previousCache, previousClient, previousProtection := servicesCache.Load(), hitlHTTPClient, viewerProtection
			t.Cleanup(func() {
				servicesCache.Store(previousCache)
				hitlHTTPClient, viewerProtection = previousClient, previousProtection
			})
			viewerProtection = nil
			servicesCache.Store(&servicesCacheEntry{expires: time.Now().Add(time.Minute), services: []ServiceInfo{{Name: "owning-agent", Type: "agent", Address: "owner.invalid", Port: 8080}}})
			exactCalls := 0
			hitlHTTPClient = &http.Client{Transport: protectedViewerRoundTripFunc(func(request *http.Request) (*http.Response, error) {
				if request.URL.Host != "owner.invalid:8080" {
					t.Fatalf("checkpoint query left its owner: %s", request.URL)
				}
				body := test.pendingBody
				if request.URL.Path == "/hitl/checkpoints/cp-1" {
					exactCalls++
					body = test.exactBody
				}
				return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(body))}, nil
			})}
			execution := &StoredExecution{RequestID: "resume-1", OriginalRequestID: "request-1", AgentName: "owning-agent", Metadata: map[string]string{"resume_checkpoint_id": "cp-1"}}
			response := buildExecutionDetailSection(t.Context(), execution, "hitl")
			lifecycle := response["hitl_lifecycle"].(*HITLLifecycle)
			warnings, _ := response["section_warnings"].([]string)
			if lifecycle.CurrentStatus != test.wantStatus || !lifecycle.IsResume || response["has_hitl_data"] != true || (exactCalls == 1) != test.wantExact || exactCalls > 1 || (len(warnings) > 0) != test.wantWarning {
				t.Fatalf("resume evidence mismatch: lifecycle=%+v exactCalls=%d warnings=%v", lifecycle, exactCalls, warnings)
			}
			if test.wantStatus == "" && lifecycle.CurrentStatusSource != "" {
				t.Fatalf("unavailable state reported as current: %+v", lifecycle)
			}
		})
	}
}

func TestExecutionOverviewPreservesAuthoritativeResumeTiming(t *testing.T) {
	store := &detailSectionStore{err: fmt.Errorf("unexpected optional store read")}
	withDetailSectionStore(t, store)
	start := time.Date(2026, 11, 6, 12, 0, 0, 0, time.UTC)
	end := start.Add(20 * time.Second)
	historicalStart, historicalEnd := start.Add(-time.Hour), start.Add(-59*time.Minute)
	execution := &StoredExecution{
		RequestID: "resume-1", Metadata: map[string]string{"resume_checkpoint_id": "cp-1"},
		OriginalRequest: strings.Repeat("a", viewerOverviewTextBytes+100),
		Result: &ExecutionResult{Success: true, Steps: []StepResult{
			{StepID: "restored", Success: true, StartTime: &historicalStart, EndTime: &historicalEnd},
			{StepID: "resumed", Success: true, StartTime: &start, EndTime: &end, Metadata: map[string]interface{}{"plan_source": "hitl_resume"}},
		}},
	}
	response := buildExecutionDetailSection(t.Context(), execution, "overview")
	if response["wall_clock_duration_ms"] != int64(20000) || response["duration_source"] != "step_time_envelope" || store.calls != 0 {
		t.Fatalf("overview lost resume timing or loaded optional evidence: %+v reads=%d", response, store.calls)
	}
	if response["original_request"] != executionOverviewPreview(execution.OriginalRequest) {
		t.Fatal("authoritative header bypassed the overview text bound")
	}
	if source := execution.Result.Steps[1].Metadata["plan_source"]; source != "hitl_resume" {
		t.Fatal("overview changed the original timing metadata")
	}
}

func TestExecutionSectionsKeepResumeNumberingLightweightAndTruthful(t *testing.T) {
	for _, test := range []struct {
		name       string
		resumeID   string
		checkpoint *HITLCheckpoint
		wantCount  *int
	}{
		{name: "ordinary execution"},
		{name: "resume checkpoint not loaded", resumeID: "cp-origin"},
		{name: "later interruption is not origin", resumeID: "cp-origin", checkpoint: &HITLCheckpoint{CheckpointID: "cp-later", CompletedSteps: []StepResult{{StepID: "prior"}, {StepID: "this-invocation"}}}},
		{name: "origin lacks completed evidence", resumeID: "cp-origin", checkpoint: &HITLCheckpoint{CheckpointID: "cp-origin"}},
		{name: "observed zero", resumeID: "cp-origin", checkpoint: &HITLCheckpoint{CheckpointID: "cp-origin", CompletedSteps: []StepResult{}}, wantCount: new(0)},
		{name: "recorded origin", resumeID: "cp-origin", checkpoint: &HITLCheckpoint{CheckpointID: "cp-origin", CompletedSteps: []StepResult{{StepID: "prior-a", Response: "PRIVATE_STEP_PAYLOAD"}, {StepID: "prior-b"}, {StepID: "prior-a"}, {StepID: ""}}}, wantCount: new(2)},
	} {
		for _, section := range []string{"overview", "steps", "flow"} {
			t.Run(test.name+"/"+section, func(t *testing.T) {
				store := &detailSectionStore{}
				withDetailSectionStore(t, store)
				execution := &StoredExecution{RequestID: "resume-1", Metadata: map[string]string{"resume_checkpoint_id": test.resumeID}, Checkpoint: test.checkpoint}
				response := buildExecutionDetailSection(t.Context(), execution, section)
				value, present := response["resume_step_numbering"]
				if test.resumeID == "" {
					if present {
						t.Fatal("ordinary execution gained resume numbering")
					}
					return
				}
				numbering, ok := value.(*executionResumeNumbering)
				if !ok || numbering.CheckpointID != test.resumeID || (numbering.CompletedStepCount == nil) != (test.wantCount == nil) {
					t.Fatalf("resume identity/known state lost: %+v", value)
				}
				if test.wantCount != nil && (*numbering.CompletedStepCount != *test.wantCount || numbering.DisplayStepOffset == nil || *numbering.DisplayStepOffset != *test.wantCount || numbering.Source != "stored_checkpoint") {
					t.Fatalf("wrong observed count/source: %+v", numbering)
				}
				encoded, err := json.Marshal(numbering)
				if err != nil || strings.Contains(string(encoded), "PRIVATE_STEP_PAYLOAD") || strings.Contains(string(encoded), "prior-a") {
					t.Fatalf("numbering retained checkpoint payload: %s err=%v", encoded, err)
				}
				if test.wantCount == nil && (!strings.Contains(string(encoded), `"completed_step_count":null`) || !strings.Contains(string(encoded), `"display_step_offset":null`) || numbering.Source != "") {
					t.Fatalf("unobserved count was claimed as known: %s", encoded)
				}
				if wantReads := map[string]int{"flow": 1}[section]; store.calls != wantReads {
					t.Fatalf("numbering added optional reads: got %d want %d", store.calls, wantReads)
				}
			})
		}
	}
}

func TestExecutionResumeNumberingExcludesRestoredStepsFromOffset(t *testing.T) {
	plan := func(ids ...string) *RoutingPlan {
		result := &RoutingPlan{}
		for _, id := range ids {
			result.Steps = append(result.Steps, RoutingStep{StepID: id})
		}
		return result
	}
	for _, test := range []struct {
		name       string
		plan       *RoutingPlan
		phasePlans []*RoutingPlan
		wantOffset int
	}{
		{name: "full interrupted plan", plan: plan("earlier-phase", "step-a", "step-b", "step-c"), wantOffset: 0},
		{name: "earlier phases absent", plan: plan("step-a", "step-b", "step-c"), wantOffset: 1},
		{name: "remaining steps only", plan: plan("step-b", "step-c"), wantOffset: 2},
		{name: "combined phases retain restored step", plan: plan("step-d"), phasePlans: []*RoutingPlan{plan("step-a", "step-b", "step-c"), plan("step-d")}, wantOffset: 1},
		{name: "duplicate rendered IDs", plan: plan("step-a", "step-a", "step-b"), wantOffset: 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			execution := &StoredExecution{
				RequestID: "resume", Metadata: map[string]string{"resume_checkpoint_id": "cp-origin"},
				Plan: test.plan, PhasePlans: test.phasePlans,
				Checkpoint: &HITLCheckpoint{CheckpointID: "cp-origin", CompletedSteps: []StepResult{{StepID: "earlier-phase"}, {StepID: "step-a"}, {StepID: "step-a"}, {StepID: ""}}},
			}
			fact := executionResumeStepNumbering(execution, nil)
			if fact.CompletedStepCount == nil || *fact.CompletedStepCount != 2 || fact.DisplayStepOffset == nil || *fact.DisplayStepOffset != test.wantOffset {
				t.Fatalf("completed count and display offset diverged incorrectly: %+v", fact)
			}
			if len(execution.Checkpoint.CompletedSteps) != 4 {
				t.Fatal("derived numbering modified checkpoint evidence")
			}
			for _, section := range []string{"overview", "steps"} {
				response := buildExecutionDetailSection(t.Context(), execution, section)
				projected := response["resume_step_numbering"].(*executionResumeNumbering)
				if projected.DisplayStepOffset == nil || *projected.DisplayStepOffset != test.wantOffset {
					t.Fatalf("%s lost numbering offset: %+v", section, projected)
				}
			}
		})
	}
}

func TestExecutionHITLReadSuppliesObservedResumeNumbering(t *testing.T) {
	withDetailSectionStore(t, &detailSectionStore{})
	previousCache, previousClient, previousProtection := servicesCache.Load(), hitlHTTPClient, viewerProtection
	t.Cleanup(func() {
		servicesCache.Store(previousCache)
		hitlHTTPClient, viewerProtection = previousClient, previousProtection
	})
	viewerProtection = nil
	servicesCache.Store(&servicesCacheEntry{expires: time.Now().Add(time.Minute), services: []ServiceInfo{{Name: "owning-agent", Type: "agent", Address: "owner.invalid", Port: 8080}}})
	calls := 0
	hitlHTTPClient = &http.Client{Transport: protectedViewerRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		calls++
		body := `{"checkpoints":[]}`
		if request.URL.Path == "/hitl/checkpoints/cp-origin" {
			body = `{"checkpoint_id":"cp-origin","status":"completed","completed_steps":[{"step_id":"prior-a"},{"step_id":"prior-b"},{"step_id":"prior-b"},{"step_id":"prior-c"}]}`
		} else if request.URL.Path != "/hitl/checkpoints" {
			t.Fatalf("unexpected checkpoint lookup: %s", request.URL)
		}
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(body))}, nil
	})}
	execution := &StoredExecution{RequestID: "resume-1", OriginalRequestID: "initial-1", AgentName: "owning-agent", Metadata: map[string]string{"resume_checkpoint_id": "cp-origin"}}
	response := buildExecutionDetailSection(t.Context(), execution, "hitl")
	numbering := response["resume_step_numbering"].(*executionResumeNumbering)
	if numbering.CheckpointID != "cp-origin" || numbering.CompletedStepCount == nil || *numbering.CompletedStepCount != 3 || numbering.DisplayStepOffset == nil || *numbering.DisplayStepOffset != 3 || numbering.Source != "agent_checkpoint_api" || calls != 3 {
		t.Fatalf("explicit HITL read lost the known resume offset: %+v calls=%d", numbering, calls)
	}
	if execution.Checkpoint != nil || len(execution.Metadata) != 1 {
		t.Fatal("derived numbering mutated the authoritative execution")
	}
}

func TestExecutionRichSectionsIncludeLoadedLLMTimingWithoutShrinkingPhaseDuration(t *testing.T) {
	for _, section := range []string{"llm", "pre", "post", "flow"} {
		for _, phaseDuration := range []time.Duration{40 * time.Second, 90 * time.Second} {
			t.Run(fmt.Sprintf("%s/%s", section, phaseDuration), func(t *testing.T) {
				start := time.Date(2026, 11, 6, 12, 0, 0, 0, time.UTC)
				stepStart, stepEnd := start.Add(30*time.Second), start.Add(40*time.Second)
				store := &detailSectionStore{record: &orchestration.LLMDebugRecord{Interactions: []orchestration.LLMInteraction{
					{Type: "plan_generation", Timestamp: start, DurationMs: 30000},
					{Type: "synthesis", Timestamp: stepEnd, DurationMs: 20000},
				}}}
				withDetailSectionStore(t, store)
				execution := &StoredExecution{RequestID: "request-1", Result: &ExecutionResult{Success: true, TotalDuration: int64(phaseDuration), Steps: []StepResult{{StepID: "step-1", Success: true, StartTime: &stepStart, EndTime: &stepEnd}}}}
				response := buildExecutionDetailSection(t.Context(), execution, section)
				wantElapsed, wantSource := int64(60000), "llm_and_step_time_envelope"
				if phaseDuration > 60*time.Second {
					wantElapsed, wantSource = phaseDuration.Milliseconds(), "phase_loop_fallback"
				}
				if response["wall_clock_duration_ms"] != wantElapsed || response["duration_source"] != wantSource || store.calls != 1 {
					t.Fatalf("rich timing mismatch: elapsed=%v source=%v reads=%d", response["wall_clock_duration_ms"], response["duration_source"], store.calls)
				}
			})
		}
	}
}

func TestPostExecutionSectionRetainsLatestSynthesisForResponseComparison(t *testing.T) {
	store := &detailSectionStore{record: &orchestration.LLMDebugRecord{Interactions: []orchestration.LLMInteraction{
		{Type: "synthesis", Response: "earlier output"},
		{Type: "plan_generation", Response: "unrelated plan"},
		{Type: "synthesis_streaming", Response: "unchanged answer"},
		{Type: "event_summarization", HookPhase: "post", Response: "recorded events"},
	}}}
	withDetailSectionStore(t, store)
	finalResponse := "unchanged answer"
	response := buildExecutionDetailSection(t.Context(), &StoredExecution{RequestID: "request-1", FinalResponse: &finalResponse}, "post")
	interactions := response["llm_interactions"].([]orchestration.LLMInteraction)
	if store.calls != 1 || len(interactions) != 2 || interactions[0].Type != "synthesis_streaming" || interactions[0].Response != finalResponse || interactions[1].HookPhase != "post" {
		t.Fatalf("post section lost its response-comparison baseline: %+v reads=%d", interactions, store.calls)
	}
}
