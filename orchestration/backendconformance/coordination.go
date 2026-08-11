package backendconformance

import (
	"testing"
	"time"

	"github.com/truvaagents/truva-g3/orchestration"
)

type CommandFixture struct {
	Publisher  orchestration.CommandStore
	Subscriber orchestration.CommandStore
}

func RunCommandStoreConformance(t *testing.T, factory func(*testing.T) CommandFixture) {
	t.Helper()
	fixture := factory(t)
	commands, cancel, err := fixture.Subscriber.SubscribeCommand(t.Context(), "checkpoint-1")
	if err != nil {
		t.Fatal(err)
	}
	defer cancel()
	want := &orchestration.Command{CheckpointID: "checkpoint-1", Type: orchestration.CommandApprove}
	if err := fixture.Publisher.PublishCommand(t.Context(), want); err != nil {
		t.Fatal(err)
	}
	select {
	case got := <-commands:
		if got == nil || got.CheckpointID != want.CheckpointID || got.Type != want.Type {
			t.Fatalf("command = %#v", got)
		}
	case <-time.After(time.Second):
		t.Fatal("cross-instance command was not delivered")
	}
	cancel()
	select {
	case _, open := <-commands:
		if open {
			t.Fatal("subscription channel remained open after cancellation")
		}
	case <-time.After(time.Second):
		t.Fatal("subscription did not stop after cancellation")
	}
}

type WorkflowFixture struct {
	First  orchestration.StateStore
	Second orchestration.StateStore
}

func RunWorkflowStateConformance(t *testing.T, factory func(*testing.T) WorkflowFixture) {
	t.Helper()
	fixture := factory(t)
	execution := &orchestration.WorkflowExecution{
		ID: "workflow-execution-1", WorkflowID: "workflow-1",
		Status: orchestration.ExecutionPending, Steps: make(map[string]*orchestration.StepExecution),
	}
	if err := fixture.First.SaveExecution(t.Context(), execution); err != nil {
		t.Fatal(err)
	}
	loaded, err := fixture.Second.GetExecution(t.Context(), execution.ID)
	if err != nil || loaded.ID != execution.ID {
		t.Fatalf("cross-instance workflow = %#v, %v", loaded, err)
	}
	execution.Status = orchestration.ExecutionRunning
	if err := fixture.Second.UpdateExecution(t.Context(), execution); err != nil {
		t.Fatal(err)
	}
	step := &orchestration.StepExecution{StepID: "step-1", Status: orchestration.StepCompleted, Attempts: 1}
	if err := fixture.First.UpdateStepExecution(t.Context(), execution.ID, step); err != nil {
		t.Fatal(err)
	}
	loaded, err = fixture.Second.GetExecution(t.Context(), execution.ID)
	if err != nil || loaded.Status != orchestration.ExecutionRunning || loaded.Steps[step.StepID] == nil ||
		loaded.Steps[step.StepID].Status != orchestration.StepCompleted {
		t.Fatalf("updated workflow = %#v, %v", loaded, err)
	}
	listed, err := fixture.Second.ListExecutions(t.Context(), execution.WorkflowID)
	if err != nil || len(listed) != 1 || listed[0].ID != execution.ID {
		t.Fatalf("workflow list = %#v, %v", listed, err)
	}
}
