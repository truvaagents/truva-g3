package redisprovider

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/truvaagents/truva-g3/core"
	"github.com/truvaagents/truva-g3/core/conformance"
	"github.com/truvaagents/truva-g3/orchestration"
	"github.com/truvaagents/truva-g3/orchestration/backendconformance"
)

func TestClientConfigInvalidURLDoesNotExposeCredentials(t *testing.T) {
	const rawURL = "redis://:super-secret@%zz"
	_, err := ConfigureClientConfig(DefaultClientConfig(), WithClientURL(rawURL))
	if err == nil {
		t.Fatal("invalid Redis URL was accepted")
	}
	if strings.Contains(err.Error(), rawURL) || strings.Contains(err.Error(), "super-secret") {
		t.Fatalf("Redis provider configuration error exposed credentials: %q", err)
	}
}

type componentCaptureLogger struct {
	core.NoOpLogger
	components []string
}

func (logger *componentCaptureLogger) WithComponent(component string) core.Logger {
	logger.components = append(logger.components, component)
	return logger
}

func TestRedisPresetPropagatesLoggerToLoggingCapableBackends(t *testing.T) {
	client := redis.NewClient(&redis.Options{Addr: "127.0.0.1:1"})
	t.Cleanup(func() { _ = client.Close() })
	clients, err := NewClientSet(client)
	if err != nil {
		t.Fatal(err)
	}
	logger := &componentCaptureLogger{}
	options, err := NewOptions(WithLogger(logger))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := NewOrchestrationBackends(clients, options); err != nil {
		t.Fatal(err)
	}
	if len(logger.components) != 9 {
		t.Fatalf("component logger applications = %d, want 9: %#v", len(logger.components), logger.components)
	}
	for _, component := range logger.components {
		if component != "framework/orchestration" {
			t.Fatalf("backend component = %q, want framework/orchestration", component)
		}
	}
}

func TestRedisPresetPreservesServiceScopedTaskQueueDefault(t *testing.T) {
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	t.Setenv(core.EnvServiceName, "travel-agent")
	clients, err := NewClientSet(nil, WithRoleClient(ClientRoleScheduling, client))
	if err != nil {
		t.Fatal(err)
	}
	options, err := NewOptions()
	if err != nil {
		t.Fatal(err)
	}
	backends, err := NewOrchestrationBackends(clients, options)
	if err != nil {
		t.Fatal(err)
	}
	if err := backends.TaskQueue().Enqueue(t.Context(), &core.Task{ID: "task-1"}); err != nil {
		t.Fatal(err)
	}
	items, err := server.List("truvag3:tasks:queue:travel-agent")
	if err != nil || len(items) != 1 {
		t.Fatalf("service-scoped task queue contents = %#v, %v", items, err)
	}
	if server.Exists("truvag3:tasks:queue") {
		t.Fatal("Redis preset used the shared task queue despite a service name")
	}
}

func TestRedisPresetTaskQueuePreservesInflightListRepresentation(t *testing.T) {
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	clients, err := NewClientSet(nil, WithRoleClient(ClientRoleScheduling, client))
	if err != nil {
		t.Fatal(err)
	}
	options, err := NewOptions(WithNamespace("settlement"))
	if err != nil {
		t.Fatal(err)
	}
	backends, err := NewOrchestrationBackends(clients, options)
	if err != nil {
		t.Fatal(err)
	}
	task := &core.Task{ID: "inflight-list", Status: core.TaskStatusQueued}
	if err := backends.TaskQueue().Enqueue(t.Context(), task); err != nil {
		t.Fatal(err)
	}
	dequeued, err := backends.TaskQueue().Dequeue(t.Context(), time.Second)
	if err != nil || dequeued == nil {
		t.Fatalf("Dequeue = %#v, %v", dequeued, err)
	}
	if got := server.Type("settlement:tasks:processing"); got != "list" {
		t.Fatalf("in-flight Redis type = %q, want list", got)
	}
	if err := backends.TaskQueue().Acknowledge(t.Context(), task.ID); err != nil {
		t.Fatal(err)
	}
}

func TestRedisPresetPreservesAgentScopedCheckpointPrefixDefault(t *testing.T) {
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	t.Setenv("TRUVAG3_HITL_KEY_PREFIX", "legacy:hitl")
	t.Setenv("TRUVAG3_AGENT_NAME", "travel-agent")
	clients, err := NewClientSet(nil, WithRoleClient(ClientRoleHITL, client))
	if err != nil {
		t.Fatal(err)
	}
	options, err := NewOptions()
	if err != nil {
		t.Fatal(err)
	}
	backends, err := NewOrchestrationBackends(clients, options)
	if err != nil {
		t.Fatal(err)
	}
	checkpoint := &orchestration.ExecutionCheckpoint{
		CheckpointID: "prefix-default", Status: orchestration.CheckpointStatusPending,
	}
	if err := backends.Checkpoints().SaveCheckpoint(t.Context(), checkpoint); err != nil {
		t.Fatal(err)
	}
	if !server.Exists("legacy:hitl:travel-agent:checkpoint:prefix-default") {
		t.Fatal("Redis preset did not preserve the agent-scoped checkpoint prefix")
	}
	if server.Exists("legacy:hitl:checkpoint:prefix-default") {
		t.Fatal("Redis preset wrote the checkpoint under the shared base prefix")
	}
}

func TestRedisPresetTaskDeliveryConformance(t *testing.T) {
	conformance.RunTaskDeliveryProfileConformance(t, conformance.TaskDeliveryAtMostOnce, func(t *testing.T) conformance.TaskDeliveryFixture {
		server := miniredis.RunT(t)
		client := redis.NewClient(&redis.Options{Addr: server.Addr()})
		clients, err := NewClientSet(client)
		if err != nil {
			t.Fatal(err)
		}
		options, err := NewOptions(WithNamespace("conformance"))
		if err != nil {
			t.Fatal(err)
		}
		backends, err := NewOrchestrationBackends(clients, options)
		if err != nil {
			t.Fatal(err)
		}
		return conformance.TaskDeliveryFixture{
			Consumer: backends.TaskConsumer(), Dispatcher: backends.TaskDispatcher(),
			Cleanup: func() { _ = client.Close() },
			DeadLetterContains: func(ctx context.Context, queueName, taskID, reason string) (bool, error) {
				entries, err := client.LRange(ctx, "conformance:tasks:dead:"+queueName, 0, -1).Result()
				if err != nil {
					return false, err
				}
				for _, raw := range entries {
					var entry struct {
						Task struct {
							ID string `json:"id"`
						} `json:"task"`
						Reason string `json:"reason"`
					}
					if err := json.Unmarshal([]byte(raw), &entry); err != nil {
						return false, err
					}
					if entry.Task.ID == taskID && entry.Reason == reason {
						return true, nil
					}
				}
				return false, nil
			},
		}
	})
}

func TestRedisPresetStorageConformance(t *testing.T) {
	newSchedulingFixture := func(t *testing.T) (*orchestration.OrchestrationBackends, *orchestration.OrchestrationBackends, *orchestration.OrchestrationBackends) {
		server := miniredis.RunT(t)
		build := func(namespace string) *orchestration.OrchestrationBackends {
			client := redis.NewClient(&redis.Options{Addr: server.Addr()})
			t.Cleanup(func() { _ = client.Close() })
			clients, err := NewClientSet(nil, WithRoleClient(ClientRoleScheduling, client))
			if err != nil {
				t.Fatal(err)
			}
			options, err := NewOptions(WithNamespace(namespace))
			if err != nil {
				t.Fatal(err)
			}
			backends, err := NewOrchestrationBackends(clients, options)
			if err != nil {
				t.Fatal(err)
			}
			return backends
		}
		return build("conformance"), build("conformance"), build("isolated")
	}

	t.Run("TaskStore", func(t *testing.T) {
		conformance.RunTaskStoreConformance(t, func(t *testing.T) conformance.TaskStoreFixture {
			first, second, isolated := newSchedulingFixture(t)
			return conformance.TaskStoreFixture{
				First: first.Tasks(), Second: second.Tasks(), Isolated: isolated.Tasks(),
			}
		})
	})
	t.Run("ScheduleStore", func(t *testing.T) {
		conformance.RunScheduleStoreConformance(t, func(t *testing.T) conformance.ScheduleStoreFixture {
			first, second, isolated := newSchedulingFixture(t)
			return conformance.ScheduleStoreFixture{
				First: first.Schedules(), Second: second.Schedules(), Isolated: isolated.Schedules(),
			}
		})
	})
	t.Run("TaskQueue", func(t *testing.T) {
		conformance.RunTaskQueueConformance(t, func(t *testing.T) conformance.TaskQueueFixture {
			first, second, isolated := newSchedulingFixture(t)
			return conformance.TaskQueueFixture{
				First: first.TaskQueue(), Second: second.TaskQueue(), Isolated: isolated.TaskQueue(),
			}
		})
	})
}

type overrideExecutionStore struct{}

func (*overrideExecutionStore) Store(context.Context, *orchestration.StoredExecution) error {
	return nil
}

type recordingCheckpointPersistence struct {
	updates chan orchestration.CheckpointStatus
}

func (*recordingCheckpointPersistence) SaveCheckpoint(context.Context, *orchestration.ExecutionCheckpoint) error {
	return nil
}
func (*recordingCheckpointPersistence) LoadCheckpoint(context.Context, string) (*orchestration.ExecutionCheckpoint, error) {
	return nil, nil
}
func (persistence *recordingCheckpointPersistence) UpdateCheckpointStatus(_ context.Context, _ string, status orchestration.CheckpointStatus) error {
	persistence.updates <- status
	return nil
}
func (*recordingCheckpointPersistence) ListPendingCheckpoints(context.Context, orchestration.CheckpointFilter) ([]*orchestration.ExecutionCheckpoint, error) {
	return nil, nil
}
func (*recordingCheckpointPersistence) DeleteCheckpoint(context.Context, string) error { return nil }

type recordingExpiredCheckpointSource struct {
	once sync.Once
}

type providerTestRunnable struct{}

func (*providerTestRunnable) Start(context.Context) error { return nil }

func (source *recordingExpiredCheckpointSource) ClaimExpiredCheckpoints(context.Context, orchestration.ExpiredCheckpointClaimRequest) ([]*orchestration.ExecutionCheckpoint, error) {
	var checkpoints []*orchestration.ExecutionCheckpoint
	source.once.Do(func() {
		checkpoints = []*orchestration.ExecutionCheckpoint{{
			CheckpointID: "override-checkpoint",
			Status:       orchestration.CheckpointStatusPending,
			RequestMode:  orchestration.RequestModeNonStreaming,
		}}
	})
	return checkpoints, nil
}
func (*recordingExpiredCheckpointSource) ReleaseExpiredCheckpointClaim(context.Context, string, string) error {
	return nil
}

func TestRedisCheckpointConformance(t *testing.T) {
	backendconformance.RunCheckpointConformance(t, func(t *testing.T) backendconformance.CheckpointFixture {
		server := miniredis.RunT(t)
		firstClient := redis.NewClient(&redis.Options{Addr: server.Addr()})
		secondClient := redis.NewClient(&redis.Options{Addr: server.Addr()})
		t.Cleanup(func() { _ = firstClient.Close(); _ = secondClient.Close() })
		first, err := orchestration.NewRedisCheckpointStoreWithClient(firstClient, orchestration.WithCheckpointKeyPrefix("conformance:hitl"))
		if err != nil {
			t.Fatal(err)
		}
		second, err := orchestration.NewRedisCheckpointStoreWithClient(secondClient, orchestration.WithCheckpointKeyPrefix("conformance:hitl"))
		if err != nil {
			t.Fatal(err)
		}
		return backendconformance.CheckpointFixture{
			Persistence: first,
			Sources:     []orchestration.ExpiredCheckpointSource{first, second},
			Advance:     server.FastForward,
		}
	})
}

func TestRedisDebugStoreConformance(t *testing.T) {
	server := miniredis.RunT(t)
	newClient := func(t *testing.T) *redis.Client {
		client := redis.NewClient(&redis.Options{Addr: server.Addr()})
		t.Cleanup(func() { _ = client.Close() })
		return client
	}
	backendconformance.RunExecutionStoreConformance(t, func(t *testing.T) backendconformance.ExecutionFixture {
		config := orchestration.NewDefaultOrchestratorConfig().ExecutionStore
		first, err := orchestration.NewRedisExecutionDebugStoreWithClient(newClient(t), config, orchestration.WithExecutionDebugKeyPrefix("conformance:execution"))
		if err != nil {
			t.Fatal(err)
		}
		second, err := orchestration.NewRedisExecutionDebugStoreWithClient(newClient(t), config, orchestration.WithExecutionDebugKeyPrefix("conformance:execution"))
		if err != nil {
			t.Fatal(err)
		}
		isolated, err := orchestration.NewRedisExecutionDebugStoreWithClient(newClient(t), config, orchestration.WithExecutionDebugKeyPrefix("isolated:execution"))
		if err != nil {
			t.Fatal(err)
		}
		return backendconformance.ExecutionFixture{First: first, Second: second, Isolated: isolated}
	})
	backendconformance.RunLLMDebugStoreConformance(t, func(t *testing.T) backendconformance.LLMDebugFixture {
		first, err := orchestration.NewRedisLLMDebugStoreWithClient(newClient(t), orchestration.WithDebugKeyPrefix("conformance:llm"))
		if err != nil {
			t.Fatal(err)
		}
		second, err := orchestration.NewRedisLLMDebugStoreWithClient(newClient(t), orchestration.WithDebugKeyPrefix("conformance:llm"))
		if err != nil {
			t.Fatal(err)
		}
		isolated, err := orchestration.NewRedisLLMDebugStoreWithClient(newClient(t), orchestration.WithDebugKeyPrefix("isolated:llm"))
		if err != nil {
			t.Fatal(err)
		}
		return backendconformance.LLMDebugFixture{First: first, Second: second, Isolated: isolated}
	})
}

func TestRedisCoordinationConformance(t *testing.T) {
	server := miniredis.RunT(t)
	newClient := func(t *testing.T) *redis.Client {
		client := redis.NewClient(&redis.Options{Addr: server.Addr()})
		t.Cleanup(func() { _ = client.Close() })
		return client
	}
	backendconformance.RunCommandStoreConformance(t, func(t *testing.T) backendconformance.CommandFixture {
		publisher, err := orchestration.NewRedisCommandStoreWithClient(newClient(t), orchestration.WithCommandStoreKeyPrefix("conformance:commands"))
		if err != nil {
			t.Fatal(err)
		}
		subscriber, err := orchestration.NewRedisCommandStoreWithClient(newClient(t), orchestration.WithCommandStoreKeyPrefix("conformance:commands"))
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = publisher.Close(); _ = subscriber.Close() })
		return backendconformance.CommandFixture{Publisher: publisher, Subscriber: subscriber}
	})
	backendconformance.RunWorkflowStateConformance(t, func(t *testing.T) backendconformance.WorkflowFixture {
		first, err := orchestration.NewRedisStateStoreWithClientAndPrefix(newClient(t), time.Hour, "conformance:workflow")
		if err != nil {
			t.Fatal(err)
		}
		second, err := orchestration.NewRedisStateStoreWithClientAndPrefix(newClient(t), time.Hour, "conformance:workflow")
		if err != nil {
			t.Fatal(err)
		}
		return backendconformance.WorkflowFixture{First: first, Second: second}
	})
}

func TestRedisDistributedLockConformance(t *testing.T) {
	backendconformance.RunDistributedLockConformance(t, func(t *testing.T) backendconformance.LockFixture {
		server := miniredis.RunT(t)
		newLock := func() core.DistributedLock {
			client := redis.NewClient(&redis.Options{Addr: server.Addr()})
			t.Cleanup(func() { _ = client.Close() })
			clients, err := NewClientSet(nil, WithRoleClient(ClientRoleScheduling, client))
			if err != nil {
				t.Fatal(err)
			}
			options, err := NewOptions(WithNamespace("conformance"))
			if err != nil {
				t.Fatal(err)
			}
			backends, err := NewOrchestrationBackends(clients, options)
			if err != nil {
				t.Fatal(err)
			}
			return backends.Lock()
		}
		return backendconformance.LockFixture{
			Locks: []core.DistributedLock{newLock(), newLock()}, Advance: server.FastForward,
		}
	})
}
func (*overrideExecutionStore) Get(context.Context, string) (*orchestration.StoredExecution, error) {
	return nil, nil
}
func (*overrideExecutionStore) GetByTraceID(context.Context, string) (*orchestration.StoredExecution, error) {
	return nil, nil
}
func (*overrideExecutionStore) SetMetadata(context.Context, string, string, string) error { return nil }
func (*overrideExecutionStore) ExtendTTL(context.Context, string, time.Duration) error    { return nil }
func (*overrideExecutionStore) ListRecent(context.Context, int) ([]orchestration.ExecutionSummary, error) {
	return nil, nil
}

func TestClientSetFallbackAndRoleOverride(t *testing.T) {
	server := miniredis.RunT(t)
	defaultClient := redis.NewClient(&redis.Options{Addr: server.Addr()})
	roleClient := redis.NewClient(&redis.Options{Addr: server.Addr(), DB: 1})
	t.Cleanup(func() { _ = defaultClient.Close(); _ = roleClient.Close() })
	clients, err := NewClientSet(defaultClient, WithRoleClient(ClientRoleHITL, roleClient))
	if err != nil {
		t.Fatal(err)
	}
	if clients.Resolve(ClientRoleExecution) != defaultClient || clients.Resolve(ClientRoleHITL) != roleClient {
		t.Fatal("client role resolution did not preserve explicit override and fallback")
	}
	if clients.Resolve(ClientRole("unknown")) != nil {
		t.Fatal("unknown client role unexpectedly resolved through the fallback")
	}
	var typedNil *redis.Client
	if _, err := NewClientSet(typedNil); err == nil {
		t.Fatal("typed-nil default client was accepted")
	}
	if _, err := NewClientSet(defaultClient, WithRoleClient(ClientRole("unknown"), roleClient)); err == nil {
		t.Fatal("unknown role override was accepted")
	}
}

func TestRedisPresetPopulatesCapabilitiesAndAppliesCallerOverrideLast(t *testing.T) {
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	clients, err := NewClientSet(client)
	if err != nil {
		t.Fatal(err)
	}
	options, err := NewOptions(WithNamespace("test-agent"))
	if err != nil {
		t.Fatal(err)
	}
	override := &overrideExecutionStore{}
	backends, err := NewOrchestrationBackends(clients, options, orchestration.WithExecutionBackend(override))
	if err != nil {
		t.Fatal(err)
	}
	if backends.Execution() != override {
		t.Fatal("caller override did not win over provider default")
	}
	if backends.LLMDebug() == nil || backends.Checkpoints() == nil || backends.CheckpointExpiry() == nil ||
		backends.Commands() == nil || backends.Workflow() == nil || backends.Schedules() == nil ||
		backends.Tasks() == nil || backends.TaskQueue() == nil || backends.TaskDispatcher() == nil ||
		backends.TaskConsumer() == nil || backends.Lock() == nil {
		t.Fatal("Redis preset did not populate all supported capabilities")
	}
	expiryRequirements, err := orchestration.RequirementsForFeatures(nil, orchestration.BackendFeatureCheckpointExpiry)
	if err != nil {
		t.Fatal(err)
	}
	if err := backends.ValidateFor(expiryRequirements); err == nil {
		t.Fatal("Redis preset advertised checkpoint expiry without an enabled processor")
	}

	providerDefaults, err := NewOrchestrationBackends(clients, options)
	if err != nil {
		t.Fatal(err)
	}
	for name, backend := range map[string]interface{}{
		"llm_debug":   providerDefaults.LLMDebug(),
		"execution":   providerDefaults.Execution(),
		"checkpoints": providerDefaults.Checkpoints(),
		"commands":    providerDefaults.Commands(),
	} {
		closer, ok := backend.(interface{ Close() error })
		if !ok {
			t.Fatalf("%s backend is not close-compatible", name)
		}
		if err := closer.Close(); err != nil {
			t.Fatalf("close %s backend: %v", name, err)
		}
	}
	if err := client.Ping(t.Context()).Err(); err != nil {
		t.Fatalf("adapter closed application-owned client: %v", err)
	}
}

func TestRedisPresetExposesExpiryLifecycleWithoutStartingIt(t *testing.T) {
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	clients, err := NewClientSet(nil, WithRoleClient(ClientRoleHITL, client))
	if err != nil {
		t.Fatal(err)
	}
	options, err := NewOptions(WithCheckpointExpiry(orchestration.ExpiryProcessorConfig{
		Enabled: true, ScanInterval: time.Hour, BatchSize: 10,
	}, nil, orchestration.WithCheckpointExpiryOwner("provider-conformance")))
	if err != nil {
		t.Fatal(err)
	}
	backends, err := NewOrchestrationBackends(clients, options)
	if err != nil {
		t.Fatal(err)
	}
	if len(backends.Runnables()) != 1 {
		t.Fatalf("runnables = %d, want one application-owned expiry processor", len(backends.Runnables()))
	}
	requirements, err := orchestration.RequirementsForFeatures(nil, orchestration.BackendFeatureCheckpointExpiry)
	if err != nil {
		t.Fatal(err)
	}
	if err := backends.ValidateFor(requirements); err != nil {
		t.Fatalf("enabled checkpoint expiry composition is incomplete: %v", err)
	}
	// Construction must not start the processor. A checkpoint saved immediately
	// afterward remains pending until the application starts the runnable.
	checkpoint := &orchestration.ExecutionCheckpoint{
		CheckpointID: "not-started", RequestID: "request", Status: orchestration.CheckpointStatusPending,
		CreatedAt: time.Now().Add(-time.Hour), ExpiresAt: time.Now().Add(-time.Minute),
	}
	if err := backends.Checkpoints().SaveCheckpoint(t.Context(), checkpoint); err != nil {
		t.Fatal(err)
	}
	loaded, err := backends.Checkpoints().LoadCheckpoint(t.Context(), checkpoint.CheckpointID)
	if err != nil || loaded.Status != orchestration.CheckpointStatusPending {
		t.Fatalf("checkpoint after construction = %#v, %v", loaded, err)
	}
}

func TestRedisPresetRebuildsExpiryProcessorAgainstCheckpointOverrides(t *testing.T) {
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	clients, err := NewClientSet(nil, WithRoleClient(ClientRoleHITL, client))
	if err != nil {
		t.Fatal(err)
	}
	options, err := NewOptions(WithCheckpointExpiry(orchestration.ExpiryProcessorConfig{
		Enabled: true, ScanInterval: time.Second, BatchSize: 1,
	}, nil, orchestration.WithCheckpointExpiryOwner("override-test")))
	if err != nil {
		t.Fatal(err)
	}
	persistence := &recordingCheckpointPersistence{updates: make(chan orchestration.CheckpointStatus, 1)}
	source := &recordingExpiredCheckpointSource{}
	backends, err := NewOrchestrationBackends(
		clients,
		options,
		orchestration.WithCheckpointPersistence(persistence),
		orchestration.WithCheckpointExpiry(source),
	)
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- backends.CheckpointExpiryProcessor().Start(ctx) }()
	select {
	case status := <-persistence.updates:
		if status != orchestration.CheckpointStatusExpiredRejected {
			t.Fatalf("expiry status = %q, want %q", status, orchestration.CheckpointStatusExpiredRejected)
		}
	case <-ctx.Done():
		t.Fatal("expiry processor did not use the overridden checkpoint dependencies")
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatalf("expiry processor shutdown: %v", err)
	}
}

func TestRedisPresetRejectsProcessorBeforeCheckpointDependencyOverride(t *testing.T) {
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	clients, err := NewClientSet(nil, WithRoleClient(ClientRoleHITL, client))
	if err != nil {
		t.Fatal(err)
	}
	options, err := NewOptions(WithCheckpointExpiry(orchestration.ExpiryProcessorConfig{
		Enabled: true, ScanInterval: time.Second, BatchSize: 1,
	}, nil))
	if err != nil {
		t.Fatal(err)
	}
	_, err = NewOrchestrationBackends(
		clients,
		options,
		orchestration.WithCheckpointExpiryProcessor(&providerTestRunnable{}),
		orchestration.WithCheckpointPersistence(&recordingCheckpointPersistence{}),
	)
	if err == nil {
		t.Fatal("processor before checkpoint dependency override was silently substituted")
	}
}

func TestClientConfigurationPrecedenceAndValidation(t *testing.T) {
	lookup := func(name string) (string, bool) {
		values := map[string]string{
			"REDIS_URL":                   "redis://standard:6379",
			"TRUVAG3_REDIS_URL":           "redis://alias:6379",
			"TRUVAG3_HITL_REDIS_DB":       "4",
			"TRUVAG3_SCHEDULING_REDIS_DB": "5",
			"TRUVAG3_SKILLS_REDIS_DB":     "9",
		}
		value, ok := values[name]
		return value, ok
	}
	fromEnvironment, err := LoadClientConfigFromEnvironment(DefaultClientConfig(), lookup)
	if err != nil {
		t.Fatal(err)
	}
	if fromEnvironment.url != "redis://standard:6379" || fromEnvironment.roleDB[ClientRoleHITL] != 4 ||
		fromEnvironment.roleDB[ClientRoleScheduling] != 5 || fromEnvironment.roleDB[ClientRoleSkills] != 9 {
		t.Fatalf("environment config = %#v", fromEnvironment)
	}
	configured, err := ConfigureClientConfig(fromEnvironment,
		WithClientURL("redis://code:6379"), WithRoleDatabase(ClientRoleHITL, 9),
	)
	if err != nil {
		t.Fatal(err)
	}
	if configured.url != "redis://code:6379" || configured.roleDB[ClientRoleHITL] != 9 {
		t.Fatalf("code overrides did not win: %#v", configured)
	}

	if _, err := ConfigureClientConfig(DefaultClientConfig(), WithClientURL("")); err == nil {
		t.Fatal("empty Redis URL was accepted")
	}
	if _, err := ConfigureClientConfig(DefaultClientConfig(), WithRoleDatabase(ClientRoleHITL, -1)); err == nil {
		t.Fatal("negative logical database was accepted")
	}
	if _, err := LoadClientConfigFromEnvironment(DefaultClientConfig(), func(name string) (string, bool) {
		return "not-an-integer", name == "TRUVAG3_HITL_REDIS_DB"
	}); err == nil {
		t.Fatal("malformed environment database was accepted")
	}
	if _, err := NewOptions(WithNamespace("invalid namespace")); err == nil {
		t.Fatal("invalid namespace was accepted")
	}
}

func TestOwnedClientsCanConstructOnlySelectedRoles(t *testing.T) {
	owned, err := NewOwnedClients(
		DefaultClientConfig(),
		WithOwnedClientRoles(ClientRoleSkills),
	)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = owned.Close() })
	if len(owned.clients) != 1 {
		t.Fatalf("owned Redis clients = %d, want one", len(owned.clients))
	}
	if owned.ClientSet().Resolve(ClientRoleSkills) == nil {
		t.Fatal("selected skills role was not constructed")
	}
	for _, role := range orderedClientRoles {
		if role != ClientRoleSkills && owned.ClientSet().Resolve(role) != nil {
			t.Fatalf("unselected role %q resolved through the client set", role)
		}
	}

	options, err := NewOptions()
	if err != nil {
		t.Fatal(err)
	}
	backends, err := NewOrchestrationBackends(owned.ClientSet(), options)
	if err != nil {
		t.Fatal(err)
	}
	if backends.SkillRegistry() == nil || backends.SkillAdministrationStore() == nil {
		t.Fatal("selected skills role did not construct the skills capability group")
	}
	if backends.Execution() != nil || backends.Workflow() != nil || backends.Schedules() != nil {
		t.Fatal("skills-only clients constructed unrelated backend capability groups")
	}
}

func TestOwnedClientRoleSelectionValidation(t *testing.T) {
	tests := []OwnedClientsOption{
		WithOwnedClientRoles(),
		WithOwnedClientRoles(ClientRole("unknown")),
		WithOwnedClientRoles(ClientRoleSkills, ClientRoleSkills),
	}
	for index, option := range tests {
		if _, err := NewOwnedClients(DefaultClientConfig(), option); err == nil {
			t.Errorf("invalid owned-client role option %d was accepted", index)
		}
	}
	if _, err := NewOwnedClients(DefaultClientConfig(), nil); err == nil {
		t.Fatal("nil owned-client option was accepted")
	}
}

func TestProviderOperationalOptionsEnvironmentAndCodePrecedence(t *testing.T) {
	base, err := NewOptions()
	if err != nil {
		t.Fatal(err)
	}
	fromEnvironment, err := LoadOptionsFromEnvironment(base, func(name string) (string, bool) {
		values := map[string]string{
			"TRUVAG3_WORKFLOW_STATE_TTL":        "48h",
			"TRUVAG3_TASK_QUEUE_RETRY_ATTEMPTS": "5",
			"TRUVAG3_TASK_QUEUE_RETRY_DELAY":    "250ms",
			"TRUVAG3_LLM_DEBUG_TTL":             "2h",
			"TRUVAG3_LLM_DEBUG_ERROR_TTL":       "96h",
			"TRUVAG3_HITL_CHECKPOINT_TTL":       "36h",
		}
		value, present := values[name]
		return value, present
	})
	if err != nil {
		t.Fatal(err)
	}
	executionConfig := orchestration.ExecutionStoreConfig{
		Enabled:   true,
		TTL:       30 * time.Minute,
		ErrorTTL:  10 * time.Minute,
		KeyPrefix: "custom:execution:",
	}
	configured, err := ConfigureOptions(fromEnvironment,
		WithExecutionStoreConfig(executionConfig),
		WithLLMDebugRetention(3*time.Hour, 120*time.Hour),
		WithCheckpointTTL(48*time.Hour),
		WithWorkflowStateTTL(72*time.Hour),
		WithTaskQueueRetryPolicy(7, 500*time.Millisecond),
	)
	if err != nil {
		t.Fatal(err)
	}
	if configured.executionConfig != executionConfig || configured.workflowTTL != 72*time.Hour || configured.taskRetryCount != 7 ||
		configured.taskRetryDelay != 500*time.Millisecond || configured.llmDebugTTL != 3*time.Hour ||
		configured.llmDebugErrorTTL != 120*time.Hour || configured.checkpointTTL != 48*time.Hour {
		t.Fatalf("code precedence = %#v", configured)
	}

	for variable, value := range map[string]string{
		"TRUVAG3_WORKFLOW_STATE_TTL":        "0s",
		"TRUVAG3_TASK_QUEUE_RETRY_ATTEMPTS": "0",
		"TRUVAG3_TASK_QUEUE_RETRY_DELAY":    "invalid",
		"TRUVAG3_LLM_DEBUG_TTL":             "0s",
		"TRUVAG3_LLM_DEBUG_ERROR_TTL":       "invalid",
		"TRUVAG3_HITL_CHECKPOINT_TTL":       "-1h",
	} {
		t.Run(variable, func(t *testing.T) {
			if _, err := LoadOptionsFromEnvironment(base, func(name string) (string, bool) {
				return value, name == variable
			}); err == nil {
				t.Fatalf("invalid %s was accepted", variable)
			}
		})
	}
	if _, err := NewOptions(WithLLMDebugRetention(0, time.Hour)); err == nil {
		t.Fatal("non-positive LLM debug retention was accepted")
	}
	if _, err := NewOptions(WithCheckpointTTL(0)); err == nil {
		t.Fatal("non-positive checkpoint retention was accepted")
	}
}

func TestRedisPresetAppliesDebugAndCheckpointRetention(t *testing.T) {
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	clients, err := NewClientSet(client)
	if err != nil {
		t.Fatal(err)
	}
	options, err := NewOptions(
		WithNamespace("retention"),
		WithLLMDebugRetention(2*time.Hour, 6*time.Hour),
		WithCheckpointTTL(4*time.Hour),
	)
	if err != nil {
		t.Fatal(err)
	}
	backends, err := NewOrchestrationBackends(clients, options)
	if err != nil {
		t.Fatal(err)
	}
	for id, interaction := range map[string]orchestration.LLMInteraction{
		"success": {Type: "test", Timestamp: time.Now(), Success: true},
		"failure": {Type: "test", Timestamp: time.Now(), Success: false},
	} {
		if err := backends.LLMDebug().RecordInteraction(t.Context(), id, interaction); err != nil {
			t.Fatal(err)
		}
	}
	if got := server.TTL("retention:llm:debug:success:meta"); got != 2*time.Hour {
		t.Fatalf("successful LLM debug TTL = %v, want %v", got, 2*time.Hour)
	}
	if got := server.TTL("retention:llm:debug:failure:meta"); got != 6*time.Hour {
		t.Fatalf("failed LLM debug TTL = %v, want %v", got, 6*time.Hour)
	}
	checkpoint := &orchestration.ExecutionCheckpoint{
		CheckpointID: "retained", Status: orchestration.CheckpointStatusPending,
	}
	if err := backends.Checkpoints().SaveCheckpoint(t.Context(), checkpoint); err != nil {
		t.Fatal(err)
	}
	if got := server.TTL("retention:hitl:checkpoint:retained"); got != 4*time.Hour {
		t.Fatalf("checkpoint TTL = %v, want %v", got, 4*time.Hour)
	}
}

func TestOwnedClientsPreserveRoleDatabasesAndCloseOnce(t *testing.T) {
	server := miniredis.RunT(t)
	config, err := ConfigureClientConfig(DefaultClientConfig(), WithClientURL("redis://"+server.Addr()))
	if err != nil {
		t.Fatal(err)
	}
	owned, err := NewOwnedClients(config)
	if err != nil {
		t.Fatal(err)
	}
	execution, ok := owned.ClientSet().Resolve(ClientRoleExecution).(*redis.Client)
	if !ok || execution.Options().DB != 8 {
		t.Fatalf("execution client = %#v", execution)
	}
	workflow, ok := owned.ClientSet().Resolve(ClientRoleWorkflow).(*redis.Client)
	if !ok || workflow.Options().DB != 0 {
		t.Fatalf("workflow client = %#v", workflow)
	}
	skills, ok := owned.ClientSet().Resolve(ClientRoleSkills).(*redis.Client)
	if !ok || skills.Options().DB != core.RedisDBReserved9 {
		t.Fatalf("skills client = %#v", skills)
	}
	if err := owned.Close(); err != nil {
		t.Fatal(err)
	}
	if err := owned.Close(); err != nil {
		t.Fatalf("second close was not idempotent: %v", err)
	}
}
