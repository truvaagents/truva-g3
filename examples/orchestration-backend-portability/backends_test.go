package main

import (
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/truvaagents/truva-g3/orchestration"
)

func TestRoleConfigValidationRequiresOnlyConsumedSettings(t *testing.T) {
	base := Config{
		PostgresURL: "postgres://database",
		NATSURL:     "nats://messaging",
		Namespace:   "test",
		AckWait:     time.Second,
	}

	api := base
	api.Queue = "api-work"
	api.WorkflowID = "api-workflow"
	if err := validateRoleConfig("api", api, roleNeeds{Queue: true, WorkflowID: true}); err != nil {
		t.Fatalf("API rejected an intentionally absent Redis URL: %v", err)
	}

	worker := base
	worker.Queue = "worker-work"
	if err := validateRoleConfig("worker", worker, roleNeeds{Queue: true}); err != nil {
		t.Fatalf("worker rejected intentionally absent Redis and workflow settings: %v", err)
	}

	scheduler := base
	scheduler.RedisURL = "redis://lock"
	if err := validateRoleConfig("scheduler", scheduler, roleNeeds{Redis: true}); err != nil {
		t.Fatalf("scheduler rejected intentionally absent API settings: %v", err)
	}
}

func TestDescribeBackendsUsesValidatedProviderBindings(t *testing.T) {
	workflow := newFakeWorkflowStore()
	dispatcher := &fakeDispatcher{}
	backends, err := orchestration.NewOrchestrationBackends(
		orchestration.WithWorkflowBackend(workflow),
		orchestration.WithTaskDispatcherBackend(dispatcher),
	)
	if err != nil {
		t.Fatal(err)
	}
	requirements, err := orchestration.NewBackendRequirements(
		orchestration.BackendWorkflowState,
		orchestration.BackendTaskDispatcher,
	)
	if err != nil {
		t.Fatal(err)
	}
	descriptor, err := describeBackends(backends, requirements, map[orchestration.BackendCapability]providerBinding{
		orchestration.BackendWorkflowState:  {provider: "postgresql", implementation: workflow},
		orchestration.BackendTaskDispatcher: {provider: "nats-jetstream", implementation: dispatcher},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !descriptor.Validated {
		t.Fatal("descriptor is not validated")
	}
	if got := descriptor.SelectedBackends[string(orchestration.BackendWorkflowState)].Provider; got != "postgresql" {
		t.Fatalf("workflow provider = %q", got)
	}
	if got := descriptor.SelectedBackends[string(orchestration.BackendTaskDispatcher)].Provider; got != "nats-jetstream" {
		t.Fatalf("dispatcher provider = %q", got)
	}
}

func TestDescribeBackendsRejectsMissingProviderBinding(t *testing.T) {
	workflow := newFakeWorkflowStore()
	backends, err := orchestration.NewOrchestrationBackends(orchestration.WithWorkflowBackend(workflow))
	if err != nil {
		t.Fatal(err)
	}
	requirements, err := orchestration.NewBackendRequirements(orchestration.BackendWorkflowState)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := describeBackends(backends, requirements, nil); err == nil {
		t.Fatal("missing provider binding was accepted")
	}
}

func TestBackendOwnerClosesOnceInReverseOrder(t *testing.T) {
	owner := &backendOwner{}
	var closed []string
	owner.add(func() error { closed = append(closed, "first"); return nil })
	wantErr := errors.New("close second")
	owner.add(func() error { closed = append(closed, "second"); return wantErr })

	if err := owner.Close(); !errors.Is(err, wantErr) {
		t.Fatalf("Close() error = %v", err)
	}
	if err := owner.Close(); !errors.Is(err, wantErr) {
		t.Fatalf("second Close() error = %v", err)
	}
	if want := []string{"second", "first"}; !reflect.DeepEqual(closed, want) {
		t.Fatalf("close order = %v, want %v", closed, want)
	}
}
