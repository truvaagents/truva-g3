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
