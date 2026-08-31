package main

import (
	"testing"
	"time"

	"github.com/truvaagents/truva-g3/orchestration"
)

// Tests for ORCH-022 Phase B: registry viewer's normalizeSteps helper.
// Proves the three branches (PhasePlans multi-phase, single-phase Plan, and
// legacy Checkpoint-fallback synthesis) and the buildUnifiedView integration.

func TestBuildUnifiedViewPreservesSkillExecutionDebug(t *testing.T) {
	previousMock := useMock
	useMock = true
	t.Cleanup(func() { useMock = previousMock })

	execution := &StoredExecution{
		RequestID: "request-with-skills",
		Skills: &orchestration.SkillExecutionDebug{
			BindingSource: "code",
			Candidates: []orchestration.SkillCandidateDebug{{
				Sequence: 1, DisplayName: "Weather Assessment",
				Description: "Evaluates forecast conditions and travel disruption.",
			}},
		},
	}

	view := buildUnifiedView(execution)
	if view == nil || view.Skills == nil || view.Skills.BindingSource != "code" ||
		len(view.Skills.Candidates) != 1 ||
		view.Skills.Candidates[0].DisplayName != "Weather Assessment" ||
		view.Skills.Candidates[0].Description != "Evaluates forecast conditions and travel disruption." {
		t.Fatalf("unified skills = %#v", view)
	}
}

func TestBuildUnifiedViewPreservesGovernedFinalResponse(t *testing.T) {
	previousMock := useMock
	useMock = true
	t.Cleanup(func() { useMock = previousMock })

	governed := "Governed order recovery outcome: recovered"
	view := buildUnifiedView(&StoredExecution{
		RequestID:           "request-with-governed-response",
		FinalResponse:       &governed,
		FinalResponseSource: "after_synthesis_hooks",
	})
	if view == nil || view.FinalResponse == nil || *view.FinalResponse != governed {
		t.Fatalf("final response = %#v", view)
	}
	if view.FinalResponseSource != "after_synthesis_hooks" {
		t.Errorf("final response source = %q", view.FinalResponseSource)
	}
}

func TestSummarizeExecutionDoesNotReplacePhaseLoopWithSmallerStepEnvelope(t *testing.T) {
	started := time.Date(2026, 8, 21, 3, 39, 57, 0, time.UTC)
	ended := started.Add(55 * time.Second)
	execution := &StoredExecution{
		RequestID: "duration-lower-bound",
		Result: &ExecutionResult{
			TotalDuration: int64(183 * time.Second),
			Steps: []StepResult{{
				StepID: "step-1", Success: true, StartTime: &started, EndTime: &ended,
			}},
		},
	}

	summary := summarizeExecution(execution)
	if summary.WallClockDurationMs != 183_000 {
		t.Fatalf("wall clock = %d, want phase-loop lower bound 183000", summary.WallClockDurationMs)
	}
	if summary.DurationSource != "phase_loop_fallback" {
		t.Fatalf("duration source = %q, want phase_loop_fallback", summary.DurationSource)
	}
}

func TestResumeObservationWindowExcludesRestoredStepsAndIncludesLaterResumePhases(t *testing.T) {
	historicalStart := time.Date(2026, 8, 27, 15, 34, 36, 0, time.UTC)
	historicalEnd := historicalStart.Add(50 * time.Second)
	resumeStart := historicalStart.Add(111 * time.Second)
	resumeEnd := resumeStart.Add(2 * time.Second)
	continuationStart := resumeEnd.Add(time.Second)
	continuationEnd := continuationStart.Add(4 * time.Second)
	execution := &StoredExecution{
		RequestID: "resume-duration",
		Metadata:  map[string]string{"resume_checkpoint_id": "cp-duration"},
		Result: &ExecutionResult{
			TotalDuration: int64(5 * time.Second),
			Steps: []StepResult{
				{
					StepID: "restored", Success: true, StartTime: &historicalStart, EndTime: &historicalEnd,
					Metadata: map[string]interface{}{"plan_source": "continuation_generation"},
				},
				{
					StepID: "resumed", Success: true, StartTime: &resumeStart, EndTime: &resumeEnd,
					Metadata: map[string]interface{}{"plan_source": "hitl_resume"},
				},
				{
					StepID: "continued", Success: true, StartTime: &continuationStart, EndTime: &continuationEnd,
					Metadata: map[string]interface{}{"plan_source": "continuation_generation"},
				},
			},
		},
	}

	start, end := executionObservationWindow(execution)
	if !start.Equal(resumeStart) || !end.Equal(continuationEnd) {
		t.Fatalf("resume window = %s to %s, want %s to %s", start, end, resumeStart, continuationEnd)
	}
	summary := summarizeExecution(execution)
	if summary.WallClockDurationMs != 7_000 || summary.DurationSource != "step_time_envelope" {
		t.Fatalf("resume duration = %d source=%q, want 7000 step_time_envelope", summary.WallClockDurationMs, summary.DurationSource)
	}
}

func TestNormalizeSteps_MultiPhasePostFix(t *testing.T) {
	t0 := time.Now()
	exec := &StoredExecution{
		PhasePlans: []*RoutingPlan{
			{PlanID: "p1", Steps: []RoutingStep{{StepID: "step-1"}, {StepID: "step-2"}}},
			{PlanID: "p1", Steps: []RoutingStep{{StepID: "step-3"}, {StepID: "step-4"}, {StepID: "step-5"}}},
		},
		Result: &ExecutionResult{
			Steps: []StepResult{
				{StepID: "step-1", Success: true, StartTime: &t0},
				{StepID: "step-2", Success: true},
				{StepID: "step-4", Success: true},
			},
		},
	}

	steps, results, breakpoints := normalizeSteps(exec)

	if len(steps) != 5 {
		t.Fatalf("steps length = %d, want 5 (concatenation of both phase plans)", len(steps))
	}
	wantIDs := []string{"step-1", "step-2", "step-3", "step-4", "step-5"}
	for i, s := range steps {
		if s.StepID != wantIDs[i] {
			t.Errorf("steps[%d].StepID = %q, want %q", i, s.StepID, wantIDs[i])
		}
	}
	if len(results) != 3 {
		t.Errorf("results length = %d, want 3", len(results))
	}
	if len(breakpoints) != 2 {
		t.Errorf("breakpoints length = %d, want 2 (one per phase)", len(breakpoints))
	}
	if breakpoints[0] != 0 || breakpoints[1] != 2 {
		t.Errorf("breakpoints = %v, want [0, 2]", breakpoints)
	}
}

func TestNormalizeSteps_SinglePhaseFallback(t *testing.T) {
	exec := &StoredExecution{
		PhasePlans: []*RoutingPlan{
			{PlanID: "p1", Steps: []RoutingStep{{StepID: "step-a"}, {StepID: "step-b"}}},
		},
		Plan: &RoutingPlan{Steps: []RoutingStep{{StepID: "step-a"}, {StepID: "step-b"}}},
		Result: &ExecutionResult{
			Steps: []StepResult{{StepID: "step-a", Success: true}},
		},
	}

	steps, results, breakpoints := normalizeSteps(exec)

	// len(PhasePlans) == 1 → helper falls through to Plan.Steps, not multi-phase branch
	if len(steps) != 2 {
		t.Fatalf("steps length = %d, want 2", len(steps))
	}
	if len(results) != 1 {
		t.Errorf("results length = %d, want 1", len(results))
	}
	if breakpoints != nil {
		t.Errorf("breakpoints = %v, want nil for single-phase", breakpoints)
	}
}

func TestNormalizeSteps_LegacyInterruptedRecord_FromCheckpoint(t *testing.T) {
	// Legacy pre-ORCH-022 shape: PhasePlans nil, Plan holds only the
	// continuation plan (step-3/4/5), Result.Steps empty. Checkpoint carries
	// step-1/2/4 in StepResults. normalizeSteps must synthesize step-1/2 as
	// nodes so the DAG has data to render.
	exec := &StoredExecution{
		Plan: &RoutingPlan{
			Steps: []RoutingStep{{StepID: "step-3"}, {StepID: "step-4"}, {StepID: "step-5"}},
		},
		Result: &ExecutionResult{Steps: []StepResult{}},
		Checkpoint: &HITLCheckpoint{
			StepResults: map[string]*StepResult{
				"step-1": {StepID: "step-1", AgentName: "devops-tool", Namespace: "truvag3-examples", Instruction: "get_pods", Capability: "get_pods", Success: true, Parameters: map[string]interface{}{"label_filter": "app=flight-tool"}},
				"step-2": {StepID: "step-2", AgentName: "devops-tool", Success: true},
				"step-4": {StepID: "step-4", AgentName: "jira-tool", Success: true},
			},
		},
	}

	steps, results, breakpoints := normalizeSteps(exec)

	if len(steps) != 5 {
		t.Fatalf("steps length = %d, want 5 (3 from Plan + synthesized step-1, step-2)", len(steps))
	}
	// Plan-side steps come first in walking order
	planIDs := []string{"step-3", "step-4", "step-5"}
	for i, id := range planIDs {
		if steps[i].StepID != id {
			t.Errorf("steps[%d].StepID = %q, want %q", i, steps[i].StepID, id)
		}
	}
	// Synthesized nodes carry Capability and Parameters from StepResult
	var step1Synth *RoutingStep
	for i := range steps {
		if steps[i].StepID == "step-1" {
			step1Synth = &steps[i]
			break
		}
	}
	if step1Synth == nil {
		t.Fatal("synthesized step-1 missing from steps")
	}
	if step1Synth.Capability != "get_pods" {
		t.Errorf("synthesized step-1 Capability = %q, want get_pods", step1Synth.Capability)
	}
	if step1Synth.Namespace != "truvag3-examples" {
		t.Errorf("synthesized step-1 Namespace = %q, want truvag3-examples", step1Synth.Namespace)
	}
	if step1Synth.Parameters == nil || step1Synth.Parameters["label_filter"] != "app=flight-tool" {
		t.Errorf("synthesized step-1 Parameters = %v, want label_filter populated", step1Synth.Parameters)
	}
	// DependsOn is unrecoverable from StepResult — must be empty/nil on synthesized nodes
	if len(step1Synth.DependsOn) != 0 {
		t.Errorf("synthesized step-1 DependsOn = %v, want empty (unrecoverable from StepResult)", step1Synth.DependsOn)
	}
	// Results map gets entries from Checkpoint.StepResults because Result.Steps was empty
	if len(results) != 3 {
		t.Errorf("results length = %d, want 3 (all Checkpoint entries folded in)", len(results))
	}
	if breakpoints != nil {
		t.Errorf("breakpoints = %v, want nil for legacy fallback", breakpoints)
	}
}

func TestNormalizeSteps_NilExecution(t *testing.T) {
	steps, results, breakpoints := normalizeSteps(nil)
	if steps != nil {
		t.Errorf("nil execution steps = %v, want nil", steps)
	}
	if results == nil {
		t.Error("results should be empty map, not nil (safe to index)")
	}
	if breakpoints != nil {
		t.Errorf("nil execution breakpoints = %v, want nil", breakpoints)
	}
}

func TestNormalizeSteps_ResultWinsOverCheckpoint(t *testing.T) {
	// When a step appears in BOTH Result.Steps and Checkpoint.StepResults,
	// Result.Steps is authoritative (runtime fields).
	exec := &StoredExecution{
		Plan: &RoutingPlan{Steps: []RoutingStep{{StepID: "step-1"}}},
		Result: &ExecutionResult{
			Steps: []StepResult{{StepID: "step-1", Success: true, Response: "from-result"}},
		},
		Checkpoint: &HITLCheckpoint{
			StepResults: map[string]*StepResult{
				"step-1": {StepID: "step-1", Success: false, Response: "from-checkpoint"},
			},
		},
	}

	_, results, _ := normalizeSteps(exec)

	sr, ok := results["step-1"]
	if !ok {
		t.Fatal("step-1 missing from results")
	}
	if resp, ok := sr.Response.(string); !ok || resp != "from-result" {
		t.Errorf("step-1 response = %v, want from-result (Result takes precedence)", sr.Response)
	}
}

func TestComputeDAG_InterruptedRecord_PostFix(t *testing.T) {
	// End-to-end against the reproducing shape (post-fix): 5 nodes, step-3
	// is the interrupted step (via Checkpoint.CurrentStep), step-5 is pending
	// (not yet run).
	exec := &StoredExecution{
		PhasePlans: []*RoutingPlan{
			{PlanID: "p1", Steps: []RoutingStep{{StepID: "step-1"}, {StepID: "step-2"}}},
			{PlanID: "p1", Steps: []RoutingStep{{StepID: "step-3"}, {StepID: "step-4"}, {StepID: "step-5", DependsOn: []string{"step-3"}}}},
		},
		Result: &ExecutionResult{
			Steps: []StepResult{
				{StepID: "step-1", Success: true, DurationMs: 21902},
				{StepID: "step-2", Success: true, DurationMs: 22401},
				{StepID: "step-4", Success: true, DurationMs: 1010},
			},
		},
		Interrupted: true,
		Checkpoint: &HITLCheckpoint{
			CurrentStep: &RoutingStep{StepID: "step-3"},
			StepResults: map[string]*StepResult{
				"step-1": {StepID: "step-1", Success: true},
				"step-2": {StepID: "step-2", Success: true},
				"step-4": {StepID: "step-4", Success: true},
			},
		},
	}

	dag := computeDAG(exec)

	// Count step nodes only; multi-phase DAGs also include phase-boundary nodes
	// for visual grouping.
	stepNodes := make([]DAGNode, 0, len(dag.Nodes))
	for _, n := range dag.Nodes {
		if n.NodeType == "" || n.NodeType == "step" {
			stepNodes = append(stepNodes, n)
		}
	}
	if len(stepNodes) != 5 {
		t.Fatalf("step-node count = %d, want 5 (dag.Nodes length = %d including boundaries)", len(stepNodes), len(dag.Nodes))
	}
	// Assert statuses
	wantStatus := map[string]string{
		"step-1": "completed",
		"step-2": "completed",
		"step-3": "blocked", // CurrentStep match → blocked (not pending)
		"step-4": "completed",
		"step-5": "pending",
	}
	for _, n := range stepNodes {
		if want, ok := wantStatus[n.ID]; ok && n.Status != want {
			t.Errorf("node %s status = %q, want %q", n.ID, n.Status, want)
		}
	}
}

func TestComputeDAG_InterruptedRecord_Legacy(t *testing.T) {
	// End-to-end against the pre-fix shape: PhasePlans empty, Plan holds only
	// the continuation (step-3/4/5), Result.Steps empty, Checkpoint carries
	// step-1/2/4 in StepResults. Viewer must still show 5 nodes via the
	// Checkpoint-fallback in normalizeSteps.
	exec := &StoredExecution{
		Plan: &RoutingPlan{
			Steps: []RoutingStep{{StepID: "step-3"}, {StepID: "step-4"}, {StepID: "step-5", DependsOn: []string{"step-3"}}},
		},
		Result:      &ExecutionResult{Steps: []StepResult{}},
		Interrupted: true,
		Checkpoint: &HITLCheckpoint{
			CurrentStep: &RoutingStep{StepID: "step-3"},
			StepResults: map[string]*StepResult{
				"step-1": {StepID: "step-1", AgentName: "devops-tool", Success: true},
				"step-2": {StepID: "step-2", AgentName: "devops-tool", Success: true},
				"step-4": {StepID: "step-4", AgentName: "jira-tool", Success: true},
			},
		},
	}

	dag := computeDAG(exec)

	if len(dag.Nodes) != 5 {
		t.Fatalf("dag.Nodes length = %d, want 5 via Checkpoint fallback", len(dag.Nodes))
	}
	// Verify step-1/step-2 synthesized and marked completed (from Checkpoint.StepResults)
	var step1, step3 *DAGNode
	for i := range dag.Nodes {
		switch dag.Nodes[i].ID {
		case "step-1":
			step1 = &dag.Nodes[i]
		case "step-3":
			step3 = &dag.Nodes[i]
		}
	}
	if step1 == nil {
		t.Fatal("step-1 missing from legacy-fallback DAG")
	}
	if step1.Status != "completed" {
		t.Errorf("synthesized step-1 status = %q, want completed", step1.Status)
	}
	if step3 == nil {
		t.Fatal("step-3 missing from legacy-fallback DAG")
	}
	if step3.Status != "blocked" {
		t.Errorf("step-3 status = %q, want blocked (Checkpoint.CurrentStep match)", step3.Status)
	}
}

func TestComputeDAG_LiveDurationAndPhaseAwareTopology(t *testing.T) {
	exec := &StoredExecution{
		PhasePlans: []*RoutingPlan{
			{PhaseNumber: 1, Steps: []RoutingStep{{StepID: "step-1"}, {StepID: "step-2"}}},
			{PhaseNumber: 2, Steps: []RoutingStep{{StepID: "step-3"}}},
			{PhaseNumber: 3, Steps: []RoutingStep{
				{StepID: "step-4"},
				{StepID: "step-5"},
				{StepID: "step-6", DependsOn: []string{"step-4", "step-5"}},
			}},
			{PhaseNumber: 4, Steps: []RoutingStep{
				{StepID: "step-7", ImplicitDeps: []string{"step-6"}},
				{StepID: "step-8", DependsOn: []string{"step-7"}},
				{StepID: "step-9", DependsOn: []string{"step-7", "step-8"}},
			}},
		},
		Result: &ExecutionResult{Success: true, Steps: []StepResult{
			{StepID: "step-1", Success: true},
			{StepID: "step-2", Success: true},
			{StepID: "step-3", Success: true},
			{StepID: "step-4", Success: true},
			{StepID: "step-5", Success: true},
			{StepID: "step-6", Success: true, Duration: 6_918_398_341},
			{StepID: "step-7", Success: true},
			{StepID: "step-8", Success: true},
			{StepID: "step-9", Success: true},
		}},
	}

	dag := computeDAG(exec)
	wantLevels := [][]string{
		{"step-1", "step-2"},
		{"phase_boundary_1"},
		{"step-3"},
		{"phase_boundary_2"},
		{"step-4", "step-5"},
		{"step-6"},
		{"phase_boundary_3"},
		{"step-7"},
		{"step-8"},
		{"step-9"},
	}
	if len(dag.Levels) != len(wantLevels) {
		t.Fatalf("levels = %v, want %v", dag.Levels, wantLevels)
	}
	for i := range wantLevels {
		if len(dag.Levels[i]) != len(wantLevels[i]) {
			t.Fatalf("level %d = %v, want %v", i, dag.Levels[i], wantLevels[i])
		}
		for j := range wantLevels[i] {
			if dag.Levels[i][j] != wantLevels[i][j] {
				t.Fatalf("level %d = %v, want %v", i, dag.Levels[i], wantLevels[i])
			}
		}
	}
	if dag.Statistics.MaxParallelism != 2 {
		t.Errorf("max_parallelism = %d, want 2", dag.Statistics.MaxParallelism)
	}
	if dag.Statistics.TotalNodes != len(dag.Nodes) || len(dag.Nodes) != 12 {
		t.Errorf("total_nodes = %d, node count = %d, want both 12", dag.Statistics.TotalNodes, len(dag.Nodes))
	}
	if dag.Statistics.Depth != 10 {
		t.Errorf("depth = %d, want 10 including three sequencing boundaries", dag.Statistics.Depth)
	}

	var step6 *DAGNode
	implicitEdgeFound := false
	for i := range dag.Nodes {
		if dag.Nodes[i].ID == "step-6" {
			step6 = &dag.Nodes[i]
		}
	}
	for _, edge := range dag.Edges {
		if edge.Source == "step-6" && edge.Target == "step-7" && edge.EdgeType == "implicit_dependency" {
			implicitEdgeFound = true
		}
	}
	if step6 == nil || step6.DurationMs != 6918 {
		t.Fatalf("step-6 = %#v, want duration_ms=6918 from live nanoseconds", step6)
	}
	if !implicitEdgeFound {
		t.Fatal("missing implicit_dependency edge step-6 -> step-7")
	}
}

func TestObservedDurationUsesElapsedEnvelopeNotDurationSum(t *testing.T) {
	start := time.Date(2026, 8, 21, 22, 0, 0, 0, time.UTC)
	end := start.Add(70_677 * time.Millisecond)
	if got := observedDurationMilliseconds(start, end); got != 70_677 {
		t.Fatalf("observed duration = %dms, want 70677ms", got)
	}
	if got := primaryExecutionDurationMs(ExecutionSummary{
		TotalDurationMs:     59_343,
		WallClockDurationMs: 70_677,
		LLMTotalDurationMs:  70_555,
	}); got != 70_677 {
		t.Fatalf("primary duration = %dms, want wall clock 70677ms", got)
	}
}

func TestBuildHITLLifecycleReconcilesSnapshotAndCurrentCheckpoint(t *testing.T) {
	initial := &StoredExecution{
		RequestID:         "initial-request",
		OriginalRequestID: "initial-request",
		Interrupted:       true,
		Checkpoint: &HITLCheckpoint{
			CheckpointID: "checkpoint-1",
			Status:       "pending",
		},
	}
	current := []HITLCheckpoint{{CheckpointID: "checkpoint-1", Status: "completed"}}
	family := []ExecutionSummary{{
		RequestID:         "resume-request",
		OriginalRequestID: "initial-request",
		TraceID:           "resume-trace",
		Success:           true,
		HITLCheckpointID:  "checkpoint-1",
		Metadata:          map[string]string{"resume_checkpoint_id": "checkpoint-1"},
	}}

	lifecycle := buildHITLLifecycle(initial, current, family)
	if lifecycle == nil {
		t.Fatal("lifecycle is nil")
	}
	if lifecycle.SnapshotStatus != "pending" || lifecycle.CurrentStatus != "completed" {
		t.Errorf("snapshot=%q current=%q", lifecycle.SnapshotStatus, lifecycle.CurrentStatus)
	}
	if lifecycle.CurrentStatusSource != "agent_checkpoint_api" || lifecycle.CurrentCheckpoint == nil {
		t.Errorf("current checkpoint source/payload = %#v", lifecycle)
	}
	if len(lifecycle.RelatedExecutions) != 1 || lifecycle.RelatedExecutions[0].Role != "resume" ||
		lifecycle.RelatedExecutions[0].RequestID != "resume-request" {
		t.Errorf("related executions = %#v", lifecycle.RelatedExecutions)
	}
}

func TestBuildHITLLifecycleMarksResumeAndLinksBackToInitial(t *testing.T) {
	resume := &StoredExecution{
		RequestID:         "resume-request",
		OriginalRequestID: "initial-request",
		Metadata:          map[string]string{"resume_checkpoint_id": "checkpoint-1"},
	}
	current := []HITLCheckpoint{{CheckpointID: "checkpoint-1", Status: "aborted"}}
	family := []ExecutionSummary{{
		RequestID:         "initial-request",
		OriginalRequestID: "initial-request",
		TraceID:           "initial-trace",
		Interrupted:       true,
		HITLCheckpointID:  "checkpoint-1",
	}, {
		RequestID:         "other-interruption",
		OriginalRequestID: "initial-request",
		Interrupted:       true,
		HITLCheckpointID:  "checkpoint-2",
	}}

	lifecycle := buildHITLLifecycle(resume, current, family)
	if lifecycle == nil || !lifecycle.IsResume || lifecycle.CheckpointID != "checkpoint-1" {
		t.Fatalf("resume lifecycle = %#v", lifecycle)
	}
	if lifecycle.InitialRequestID != "initial-request" || lifecycle.CurrentStatus != "aborted" {
		t.Errorf("resume lifecycle = %#v", lifecycle)
	}
	if len(lifecycle.RelatedExecutions) != 1 || lifecycle.RelatedExecutions[0].Role != "interrupted" ||
		lifecycle.RelatedExecutions[0].RequestID != "initial-request" {
		t.Errorf("related executions = %#v", lifecycle.RelatedExecutions)
	}
}
