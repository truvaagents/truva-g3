package postgresadapter

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/truvaagents/truva-g3/orchestration"
)

func TestNewWorkflowStoreUsesSharedConfigurationValidation(t *testing.T) {
	pool := &pgxpool.Pool{}
	store, err := NewWorkflowStore(pool, "  tenant-a  ")
	if err != nil {
		t.Fatal(err)
	}
	if store.namespace != "tenant-a" {
		t.Fatalf("namespace = %q, want tenant-a", store.namespace)
	}
	if _, err := NewWorkflowStore(nil, "tenant-a"); err == nil {
		t.Fatal("nil pool was accepted")
	}
}

func TestValidateExecution(t *testing.T) {
	tests := []struct {
		name      string
		execution *orchestration.WorkflowExecution
		wantError string
	}{
		{name: "valid", execution: &orchestration.WorkflowExecution{ID: "execution-1", WorkflowID: "workflow-1"}},
		{name: "nil", wantError: "workflow execution is required"},
		{name: "missing execution ID", execution: &orchestration.WorkflowExecution{WorkflowID: "workflow-1"}, wantError: "workflow execution ID is required"},
		{name: "missing workflow ID", execution: &orchestration.WorkflowExecution{ID: "execution-1"}, wantError: "workflow ID is required"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := validateExecution(test.execution)
			if test.wantError == "" && err != nil {
				t.Fatalf("validateExecution() error = %v", err)
			}
			if test.wantError != "" && (err == nil || !strings.Contains(err.Error(), test.wantError)) {
				t.Fatalf("validateExecution() error = %v, want containing %q", err, test.wantError)
			}
		})
	}
}

func TestDecodeExecution(t *testing.T) {
	payload, err := json.Marshal(&orchestration.WorkflowExecution{
		ID: "execution-1", WorkflowID: "workflow-1", Status: orchestration.ExecutionRunning,
	})
	if err != nil {
		t.Fatal(err)
	}
	execution, err := decodeExecution(payload)
	if err != nil || execution.ID != "execution-1" || execution.Status != orchestration.ExecutionRunning {
		t.Fatalf("decodeExecution() = %#v, %v", execution, err)
	}
	if _, err := decodeExecution([]byte("{")); err == nil || !strings.Contains(err.Error(), "decode workflow execution") {
		t.Fatalf("invalid execution error = %v", err)
	}
}
