package main

import (
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/truvaagents/truva-g3/core"
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
	scheduler.Redis = core.RedisConnectionConfig{
		Mode: core.RedisModeStandalone, Addrs: []string{"redis:6379"},
	}
	if err := validateRoleConfig("scheduler", scheduler, roleNeeds{Redis: true}); err != nil {
		t.Fatalf("scheduler rejected intentionally absent API settings: %v", err)
	}
}

func TestRoleConfigAcceptsEveryRedisTopology(t *testing.T) {
	base := Config{
		PostgresURL: "postgres://database", NATSURL: "nats://messaging",
		Namespace: "test", AckWait: time.Second,
	}
	for name, connection := range map[string]core.RedisConnectionConfig{
		"standalone": {Mode: core.RedisModeStandalone, Addrs: []string{"redis:6379"}},
		"sentinel": {
			Mode: core.RedisModeSentinel, Addrs: []string{"sentinel:26379"}, MasterName: "primary",
		},
		"cluster": {Mode: core.RedisModeCluster, Addrs: []string{"node-0:6379", "node-1:6379"}},
	} {
		t.Run(name, func(t *testing.T) {
			config := base
			config.Redis = connection
			if err := validateRoleConfig("scheduler", config, roleNeeds{Redis: true}); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestRedisKeyspaceIsIndependentFromProviderBackendNamespace(t *testing.T) {
	keyspace, err := resolvePortabilityRedisKeyspace(func(string) (string, bool) {
		return "", false
	})
	if err != nil {
		t.Fatal(err)
	}
	if keyspace.Deployment() != "default" {
		t.Fatalf("default Redis deployment = %q, want default", keyspace.Deployment())
	}
	if keyspace.Deployment() == defaultBackendNamespace {
		t.Fatalf("Redis deployment reused provider backend namespace %q", defaultBackendNamespace)
	}

	keyspace, err = resolvePortabilityRedisKeyspace(func(name string) (string, bool) {
		if name == "TRUVAG3_REDIS_NAMESPACE" {
			return "redis-deployment", true
		}
		return "", false
	})
	if err != nil {
		t.Fatal(err)
	}
	if keyspace.Deployment() != "redis-deployment" {
		t.Fatalf("resolved Redis deployment = %q", keyspace.Deployment())
	}
}

func TestRedisKeyspaceRejectsInvalidEnvironmentValue(t *testing.T) {
	_, err := resolvePortabilityRedisKeyspace(func(string) (string, bool) {
		return "invalid{namespace", true
	})
	if !errors.Is(err, core.ErrInvalidConfiguration) {
		t.Fatalf("keyspace error = %v, want ErrInvalidConfiguration", err)
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
