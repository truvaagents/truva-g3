package redisprovider

import (
	"context"
	"encoding/json"
	"strings"
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

type overrideExecutionStore struct{}

func (*overrideExecutionStore) Store(context.Context, *orchestration.StoredExecution) error {
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
		WithWorkflowStateTTL(72*time.Hour),
		WithTaskQueueRetryPolicy(7, 500*time.Millisecond),
	)
	if err != nil {
		t.Fatal(err)
	}
	if configured.executionConfig != executionConfig || configured.workflowTTL != 72*time.Hour || configured.taskRetryCount != 7 ||
		configured.taskRetryDelay != 500*time.Millisecond {
		t.Fatalf("code precedence = %#v", configured)
	}

	for variable, value := range map[string]string{
		"TRUVAG3_WORKFLOW_STATE_TTL":        "0s",
		"TRUVAG3_TASK_QUEUE_RETRY_ATTEMPTS": "0",
		"TRUVAG3_TASK_QUEUE_RETRY_DELAY":    "invalid",
	} {
		t.Run(variable, func(t *testing.T) {
			if _, err := LoadOptionsFromEnvironment(base, func(name string) (string, bool) {
				return value, name == variable
			}); err == nil {
				t.Fatalf("invalid %s was accepted", variable)
			}
		})
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
