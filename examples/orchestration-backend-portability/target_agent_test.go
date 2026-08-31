package main

import (
	"strings"
	"testing"
)

func TestDeterministicTargetResponseContainsScheduleTaskAndInstruction(t *testing.T) {
	target := newDeterministicTargetOrchestrator()
	response, err := target.ProcessRequest(t.Context(), "produce a deterministic result", map[string]interface{}{
		"schedule_id": "schedule-123",
		"task_id":     "task-456",
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, value := range []string{"schedule-123", "task-456", "produce a deterministic result"} {
		if !strings.Contains(response.Response, value) {
			t.Fatalf("response %q does not contain %q", response.Response, value)
		}
	}
	if response.Metadata["schedule_id"] != "schedule-123" || response.Metadata["task_id"] != "task-456" {
		t.Fatalf("metadata = %#v", response.Metadata)
	}
	if metrics := target.GetMetrics(); metrics.TotalRequests != 1 || metrics.SuccessfulRequests != 1 {
		t.Fatalf("metrics = %#v", metrics)
	}
}

func TestModeIdentityAcceptsTargetAgent(t *testing.T) {
	service, componentType, err := modeIdentity("target-agent")
	if err != nil {
		t.Fatal(err)
	}
	if service != portableTargetService || componentType == "" {
		t.Fatalf("identity = (%q, %q)", service, componentType)
	}
}

func TestDeterministicTargetCanInjectTransientFailure(t *testing.T) {
	target := newDeterministicTargetOrchestrator(1)
	metadata := map[string]interface{}{"task_id": "task-1"}

	if _, err := target.ProcessRequest(t.Context(), "retry me", metadata); err == nil {
		t.Fatal("expected the configured first attempt to fail")
	}
	response, err := target.ProcessRequest(t.Context(), "retry me", metadata)
	if err != nil {
		t.Fatalf("second attempt should recover: %v", err)
	}
	if response == nil || !strings.Contains(response.Response, "retry me") {
		t.Fatalf("unexpected recovered response: %#v", response)
	}
}
