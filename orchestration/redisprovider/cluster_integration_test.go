//go:build integration

package redisprovider

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
	"github.com/truvaagents/truva-g3/core"
	"github.com/truvaagents/truva-g3/core/conformance"
	"github.com/truvaagents/truva-g3/orchestration"
	"github.com/truvaagents/truva-g3/orchestration/backendconformance"
	"github.com/truvaagents/truva-g3/orchestration/internal/redistest"
	"github.com/truvaagents/truva-g3/telemetry"
)

var clusterFixtureSequence atomic.Uint64

type clusterProviderFixture struct {
	backends *orchestration.OrchestrationBackends
	clients  *OwnedClients
}

type redisCommandFaultHook struct {
	mu        sync.Mutex
	command   string
	key       string
	err       error
	enabled   bool
	remaining int
	after     bool
	attempts  int
}

type redisCommandBarrierHook struct {
	command string
	key     string
	started chan struct{}
	release chan struct{}
	once    sync.Once
}

func (*redisCommandBarrierHook) DialHook(next redis.DialHook) redis.DialHook { return next }

func (hook *redisCommandBarrierHook) ProcessHook(next redis.ProcessHook) redis.ProcessHook {
	return func(ctx context.Context, command redis.Cmder) error {
		if hook.matches(command) {
			hook.once.Do(func() { close(hook.started) })
			select {
			case <-hook.release:
			case <-ctx.Done():
				return ctx.Err()
			}
		}
		return next(ctx, command)
	}
}

func (hook *redisCommandBarrierHook) ProcessPipelineHook(next redis.ProcessPipelineHook) redis.ProcessPipelineHook {
	return next
}

func (hook *redisCommandBarrierHook) matches(command redis.Cmder) bool {
	args := command.Args()
	if command.Name() != hook.command || len(args) < 2 {
		return false
	}
	key, _ := args[1].(string)
	return key == hook.key
}

func (*redisCommandFaultHook) DialHook(next redis.DialHook) redis.DialHook { return next }

func (hook *redisCommandFaultHook) ProcessHook(next redis.ProcessHook) redis.ProcessHook {
	return func(ctx context.Context, command redis.Cmder) error {
		fail, after, injected := hook.match(command)
		if !fail {
			return next(ctx, command)
		}
		if !after {
			return injected
		}
		if err := next(ctx, command); err != nil {
			return err
		}
		return injected
	}
}

func (hook *redisCommandFaultHook) ProcessPipelineHook(next redis.ProcessPipelineHook) redis.ProcessPipelineHook {
	return func(ctx context.Context, commands []redis.Cmder) error {
		for _, command := range commands {
			fail, after, injected := hook.match(command)
			if !fail {
				continue
			}
			if !after {
				return injected
			}
			if err := next(ctx, commands); err != nil {
				return err
			}
			return injected
		}
		return next(ctx, commands)
	}
}

func (hook *redisCommandFaultHook) match(command redis.Cmder) (bool, bool, error) {
	hook.mu.Lock()
	defer hook.mu.Unlock()
	args := command.Args()
	keyMatches := hook.key == ""
	for _, argument := range args[1:] {
		if value, ok := argument.(string); ok && value == hook.key {
			keyMatches = true
			break
		}
	}
	fail := hook.enabled && command.Name() == hook.command && keyMatches && hook.remaining != 0
	if fail {
		if hook.remaining > 0 {
			hook.remaining--
		}
		hook.attempts++
	}
	return fail, hook.after, hook.err
}

func (hook *redisCommandFaultHook) configure(enabled bool, remaining int, after bool) {
	hook.mu.Lock()
	hook.enabled = enabled
	hook.remaining = remaining
	hook.after = after
	hook.mu.Unlock()
}

func (hook *redisCommandFaultHook) attemptCount() int {
	hook.mu.Lock()
	defer hook.mu.Unlock()
	return hook.attempts
}

func TestRedisProviderAgainstRealCluster(t *testing.T) {
	if testing.Short() {
		t.Skip("real Redis/Valkey cluster tests are excluded in short mode")
	}
	distribution, err := redistest.DistributionFromEnvironment()
	if err != nil {
		if strings.ToLower(strings.TrimSpace(os.Getenv(redistest.EnvRunClusterTests))) != "true" {
			t.Skipf("real Redis/Valkey cluster test; set %s=true", redistest.EnvRunClusterTests)
		}
		t.Fatal(err)
	}
	cluster := redistest.StartCluster(t, distribution)
	seeds := cluster.SeedAddrs()

	t.Run("provider-composition", func(t *testing.T) {
		fixture := newClusterProviderFixture(t, seeds, clusterNamespace("composition"))
		requireAllProviderCapabilities(t, fixture.backends)
	})

	t.Run("connection-failures", func(t *testing.T) {
		runClusterConnectionFailures(t, seeds)
	})

	t.Run("default-provider-restricted-composition", func(t *testing.T) {
		connection := core.DefaultRedisConnectionConfig()
		connection.Mode = core.RedisModeCluster
		connection.Addrs = append([]string(nil), seeds...)
		connection.DB = 0
		owned, err := NewDefaultBackends(
			&core.NoOpLogger{},
			WithDefaultBackendRoles(ClientRoleWorkflow),
			WithDefaultBackendClientConfig(WithConnectionConfig(connection)),
			WithDefaultBackendProviderOptions(WithDeployment(clusterNamespace("default-composition"))),
		)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			if err := owned.Close(); err != nil {
				t.Errorf("close default Redis backends: %v", err)
			}
		})
		if owned.Backends().Workflow() == nil {
			t.Fatal("restricted default composition omitted workflow state")
		}
		if owned.Backends().Execution() != nil || owned.Backends().Tasks() != nil || owned.Backends().SkillRegistry() != nil {
			t.Fatal("restricted default composition expanded beyond the requested workflow role")
		}
	})

	t.Run("workflow", func(t *testing.T) {
		backendconformance.RunWorkflowStateConformance(t, func(t *testing.T) backendconformance.WorkflowFixture {
			deployment := clusterNamespace("workflow")
			first := newClusterProviderFixture(t, seeds, deployment, ClientRoleWorkflow)
			second := newClusterProviderFixture(t, seeds, deployment, ClientRoleWorkflow)
			return backendconformance.WorkflowFixture{First: first.backends.Workflow(), Second: second.backends.Workflow()}
		})
	})

	t.Run("hitl", func(t *testing.T) {
		backendconformance.RunCheckpointConformance(t, func(t *testing.T) backendconformance.CheckpointFixture {
			deployment := clusterNamespace("checkpoint")
			first := newClusterProviderFixture(t, seeds, deployment, ClientRoleHITL)
			second := newClusterProviderFixture(t, seeds, deployment, ClientRoleHITL)
			return backendconformance.CheckpointFixture{
				Persistence: first.backends.Checkpoints(),
				Sources: []orchestration.ExpiredCheckpointSource{
					first.backends.CheckpointExpiry(), second.backends.CheckpointExpiry(),
				},
				Advance: func(duration time.Duration) { time.Sleep(duration + 100*time.Millisecond) },
			}
		})
		backendconformance.RunCommandStoreConformance(t, func(t *testing.T) backendconformance.CommandFixture {
			deployment := clusterNamespace("commands")
			publisher := newClusterProviderFixture(t, seeds[:1], deployment, ClientRoleHITL)
			subscriber := newClusterProviderFixture(t, seeds[1:2], deployment, ClientRoleHITL)
			return backendconformance.CommandFixture{
				Publisher: publisher.backends.Commands(), Subscriber: subscriber.backends.Commands(),
			}
		})
		runClusterCommandReconnect(t, seeds)
	})

	t.Run("debug-stores", func(t *testing.T) {
		backendconformance.RunExecutionStoreConformance(t, func(t *testing.T) backendconformance.ExecutionFixture {
			deployment := clusterNamespace("execution")
			first := newClusterProviderFixture(t, seeds, deployment, ClientRoleExecution)
			second := newClusterProviderFixture(t, seeds, deployment, ClientRoleExecution)
			isolated := newClusterProviderFixture(t, seeds, deployment+"-isolated", ClientRoleExecution)
			return backendconformance.ExecutionFixture{
				First: first.backends.Execution(), Second: second.backends.Execution(), Isolated: isolated.backends.Execution(),
			}
		})
		backendconformance.RunLLMDebugStoreConformance(t, func(t *testing.T) backendconformance.LLMDebugFixture {
			deployment := clusterNamespace("llm")
			first := newClusterProviderFixture(t, seeds, deployment, ClientRoleLLMDebug)
			second := newClusterProviderFixture(t, seeds, deployment, ClientRoleLLMDebug)
			isolated := newClusterProviderFixture(t, seeds, deployment+"-isolated", ClientRoleLLMDebug)
			return backendconformance.LLMDebugFixture{
				First: first.backends.LLMDebug(), Second: second.backends.LLMDebug(), Isolated: isolated.backends.LLMDebug(),
			}
		})
	})

	t.Run("cross-slot-debug-retention", func(t *testing.T) {
		runClusterDebugRetention(t, seeds)
	})

	t.Run("scheduling", func(t *testing.T) {
		t.Setenv(core.EnvServiceName, "")
		newSchedulingSet := func(t *testing.T) (*clusterProviderFixture, *clusterProviderFixture, *clusterProviderFixture) {
			deployment := clusterNamespace("scheduling")
			return newClusterProviderFixture(t, seeds, deployment, ClientRoleScheduling),
				newClusterProviderFixture(t, seeds, deployment, ClientRoleScheduling),
				newClusterProviderFixture(t, seeds, deployment+"-isolated", ClientRoleScheduling)
		}
		conformance.RunTaskStoreConformance(t, func(t *testing.T) conformance.TaskStoreFixture {
			first, second, isolated := newSchedulingSet(t)
			return conformance.TaskStoreFixture{First: first.backends.Tasks(), Second: second.backends.Tasks(), Isolated: isolated.backends.Tasks()}
		})
		conformance.RunScheduleStoreConformance(t, func(t *testing.T) conformance.ScheduleStoreFixture {
			first, second, isolated := newSchedulingSet(t)
			return conformance.ScheduleStoreFixture{First: first.backends.Schedules(), Second: second.backends.Schedules(), Isolated: isolated.backends.Schedules()}
		})
		conformance.RunTaskQueueConformance(t, func(t *testing.T) conformance.TaskQueueFixture {
			deployment := clusterTaskQueueDeployment(t, seeds)
			first := newClusterProviderFixture(t, seeds, deployment, ClientRoleScheduling)
			second := newClusterProviderFixture(t, seeds, deployment, ClientRoleScheduling)
			isolated := newClusterProviderFixture(t, seeds, deployment+"-isolated", ClientRoleScheduling)
			keyspace, err := core.NewRedisKeyspace(deployment)
			if err != nil {
				t.Fatal(err)
			}
			requireClusterKeysOnDifferentPrimaries(
				t,
				first.clients.ClientSet().Resolve(ClientRoleScheduling),
				keyspace.Plain("tasks", "queue"),
				keyspace.Plain("tasks", "processing"),
			)
			return conformance.TaskQueueFixture{First: first.backends.TaskQueue(), Second: second.backends.TaskQueue(), Isolated: isolated.backends.TaskQueue()}
		})
		backendconformance.RunDistributedLockConformance(t, func(t *testing.T) backendconformance.LockFixture {
			deployment := clusterNamespace("locks")
			first := newClusterProviderFixture(t, seeds, deployment, ClientRoleScheduling)
			second := newClusterProviderFixture(t, seeds, deployment, ClientRoleScheduling)
			return backendconformance.LockFixture{
				Locks:   []core.DistributedLock{first.backends.Lock(), second.backends.Lock()},
				Advance: func(duration time.Duration) { time.Sleep(duration + 100*time.Millisecond) },
			}
		})
		runClusterTaskIndexRepair(t, seeds)
		runClusterFanoutCancellation(t, seeds)
		runClusterTaskQueueFaults(t, seeds)
	})

	t.Run("skills", func(t *testing.T) {
		backendconformance.RunSkillConformance(t, func(t *testing.T) backendconformance.SkillFixture {
			deployment := clusterNamespace("skills")
			firstFixture := newClusterProviderFixture(t, seeds, deployment, ClientRoleSkills)
			secondFixture := newClusterProviderFixture(t, seeds, deployment, ClientRoleSkills)
			first := firstFixture.backends.SkillRegistry().(*SkillStore)
			second := secondFixture.backends.SkillRegistry().(*SkillStore)
			client := firstFixture.clients.ClientSet().Resolve(ClientRoleSkills)
			return backendconformance.SkillFixture{
				Registry: first, PeerRegistry: second, Revisions: first, PeerRevisions: second,
				Administration: first, Deletions: first,
				CorruptManifest: func(ref orchestration.SkillVersionRef) error {
					encoded, err := client.Get(t.Context(), first.manifestKey(ref.Ref, ref.Version)).Bytes()
					if err != nil {
						return err
					}
					var manifest orchestration.SkillManifest
					if err := json.Unmarshal(encoded, &manifest); err != nil {
						return err
					}
					manifest.PlanningInstructions[0] += " corrupted-sensitive-body-marker"
					encoded, err = json.Marshal(manifest)
					if err != nil {
						return err
					}
					return client.Set(t.Context(), first.manifestKey(ref.Ref, ref.Version), encoded, 0).Err()
				},
				CorruptResource: func(ref orchestration.SkillResourceRef) error {
					encoded, err := client.Get(t.Context(), first.resourceKey(ref.Skill.Ref, ref.Skill.Version, ref.Name)).Bytes()
					if err != nil {
						return err
					}
					var resource orchestration.SkillResource
					if err := json.Unmarshal(encoded, &resource); err != nil {
						return err
					}
					resource.Content += " corrupted-sensitive-body-marker"
					encoded, err = json.Marshal(resource)
					if err != nil {
						return err
					}
					return client.Set(t.Context(), first.resourceKey(ref.Skill.Ref, ref.Skill.Version, ref.Name), encoded, 0).Err()
				},
			}
		})
	})

	t.Run("server-slots-resharding-and-failover", func(t *testing.T) {
		runClusterTopologyTransitions(t, cluster)
	})

	t.Run("registry-capacity", func(t *testing.T) {
		runRegistryCapacity(t, cluster)
	})

	t.Run("discovery-cleanup-registration-race", func(t *testing.T) {
		runDiscoveryCleanupRegistrationRace(t, seeds)
	})

	// This deliberately removes a primary and its replica, so it must remain the
	// final subtest that uses the shared fixture.
	t.Run("unavailable-shard-partial-results", func(t *testing.T) {
		runClusterUnavailableShard(t, cluster)
	})
}

func runClusterTaskIndexRepair(t *testing.T, seeds []string) {
	t.Helper()
	deployment := clusterNamespace("task-index-repair")
	fixture := newClusterProviderFixture(t, seeds, deployment, ClientRoleScheduling)
	store, ok := fixture.backends.Tasks().(*orchestration.RedisTaskStore)
	if !ok {
		t.Fatal("cluster scheduling fixture did not create RedisTaskStore")
	}
	client := fixture.clients.ClientSet().Resolve(ClientRoleScheduling)
	keyspace, err := core.NewRedisKeyspace(deployment)
	if err != nil {
		t.Fatal(err)
	}
	prefix := keyspace.Plain("tasks")
	firstID := "repair-primary-0"
	secondID := ""
	for candidate := 1; candidate < 10_000; candidate++ {
		id := fmt.Sprintf("repair-primary-%d", candidate)
		if clusterKeysUseDifferentPrimaries(
			t,
			client,
			prefix+":task:"+firstID,
			prefix+":task:"+id,
		) {
			secondID = id
			break
		}
	}
	if secondID == "" {
		t.Fatal("could not select task records on different cluster primaries")
	}
	for _, id := range []string{firstID, secondID} {
		if err := store.Create(t.Context(), &core.Task{
			ID: id, Type: "cluster-repair", Status: core.TaskStatusQueued, CreatedAt: time.Now(),
		}); err != nil {
			t.Fatal(err)
		}
	}
	allKey := prefix + ":index:all"
	statusKey := prefix + ":index:status:" + string(core.TaskStatusQueued)
	if err := client.Del(t.Context(), allKey).Err(); err != nil {
		t.Fatal(err)
	}
	if err := client.Del(t.Context(), statusKey).Err(); err != nil {
		t.Fatal(err)
	}
	if err := store.ReconcileTaskIndexes(t.Context(), 10_000); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{firstID, secondID} {
		if !client.SIsMember(t.Context(), allKey, id).Val() ||
			!client.SIsMember(t.Context(), statusKey, id).Val() {
			t.Fatalf("cluster-wide maintenance scan did not repair task %q", id)
		}
	}
}

func runClusterConnectionFailures(t *testing.T, seeds []string) {
	t.Helper()
	assertStartupFailure := func(t *testing.T, connection core.RedisConnectionConfig, forbidden ...string) {
		t.Helper()
		client, err := core.NewRedisUniversalClient(connection)
		if client != nil {
			_ = client.Close()
		}
		if err == nil {
			t.Fatal("invalid cluster connection unexpectedly passed its startup check")
		}
		var startupErr *core.RedisStartupError
		if !errors.As(err, &startupErr) {
			t.Fatalf("startup error type = %T; want *core.RedisStartupError", err)
		}
		visible := err.Error()
		for _, value := range forbidden {
			if value != "" && strings.Contains(visible, value) {
				t.Fatalf("bounded startup error disclosed %q: %q", value, visible)
			}
		}
	}

	t.Run("authentication", func(t *testing.T) {
		connection := core.DefaultRedisConnectionConfig()
		connection.Mode = core.RedisModeCluster
		connection.Addrs = append([]string(nil), seeds...)
		connection.Username = "missing-cluster-user"
		connection.Password = "cluster-secret-marker"
		connection.DialTimeout = 2 * time.Second
		connection.ReadTimeout = 2 * time.Second
		connection.WriteTimeout = 2 * time.Second
		connection.MaxRetries = 0
		assertStartupFailure(t, connection, connection.Username, connection.Password, seeds[0])
	})

	t.Run("tls-against-plaintext", func(t *testing.T) {
		connection := core.DefaultRedisConnectionConfig()
		connection.Mode = core.RedisModeCluster
		connection.Addrs = append([]string(nil), seeds...)
		connection.TLSConfig = &tls.Config{MinVersion: tls.VersionTLS12, ServerName: "cluster.invalid"}
		connection.DialTimeout = 2 * time.Second
		connection.ReadTimeout = 2 * time.Second
		connection.WriteTimeout = 2 * time.Second
		connection.MaxRetries = 0
		assertStartupFailure(t, connection, "cluster.invalid", seeds[0])
	})
}

func runClusterFanoutCancellation(t *testing.T, seeds []string) {
	t.Helper()
	fixture := newClusterProviderFixture(t, seeds, clusterNamespace("fanout-cancel"), ClientRoleScheduling)
	store, ok := fixture.backends.Tasks().(*orchestration.RedisTaskStore)
	if !ok {
		t.Fatal("cluster scheduling fixture did not create RedisTaskStore")
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := store.ReconcileTaskIndexes(ctx, 100); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled cluster fanout = %v; want context.Canceled", err)
	}
}

func runClusterTaskQueueFaults(t *testing.T, seeds []string) {
	t.Helper()
	newQueue := func(t *testing.T, label string, attempts int) (*orchestration.RedisTaskQueue, redis.UniversalClient, *orchestration.RedisTaskQueueConfig, *redisCommandFaultHook) {
		t.Helper()
		connection := core.DefaultRedisConnectionConfig()
		connection.Mode = core.RedisModeCluster
		connection.Addrs = append([]string(nil), seeds...)
		client, err := core.NewRedisUniversalClient(connection)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = client.Close() })
		deployment := clusterTaskQueueDeployment(t, seeds)
		keyspace, err := core.NewRedisKeyspace(deployment + "-" + label)
		if err != nil {
			t.Fatal(err)
		}
		config := &orchestration.RedisTaskQueueConfig{
			QueueKey: keyspace.Plain("tasks", "queue"), ProcessingKey: keyspace.Plain("tasks", "processing"),
			RetryAttempts: attempts, RetryDelay: time.Nanosecond,
		}
		if !clusterKeysUseDifferentPrimaries(t, client, config.QueueKey, config.ProcessingKey) {
			// Preserve the explicit cross-shard condition if appending the label
			// happened to move the pair onto one primary.
			for suffix := 0; suffix < 10_000; suffix++ {
				config.ProcessingKey = keyspace.Plain("tasks", "processing", fmt.Sprint(suffix))
				if clusterKeysUseDifferentPrimaries(t, client, config.QueueKey, config.ProcessingKey) {
					break
				}
			}
		}
		requireClusterKeysOnDifferentPrimaries(t, client, config.QueueKey, config.ProcessingKey)
		hook := &redisCommandFaultHook{err: errors.New("injected Redis task-queue failure")}
		client.AddHook(hook)
		return orchestration.NewRedisTaskQueue(client, config), client, config, hook
	}

	t.Run("processing-handoff-recovery", func(t *testing.T) {
		queue, client, config, hook := newQueue(t, "handoff", 1)
		task := &core.Task{ID: "cluster-task-recovery", Type: "test"}
		if err := queue.Enqueue(t.Context(), task); err != nil {
			t.Fatal(err)
		}
		hook.command, hook.key = "lpush", config.ProcessingKey
		hook.configure(true, 1, false)
		if got, err := queue.Dequeue(t.Context(), time.Second); err == nil || got != nil {
			t.Fatalf("Dequeue after tracking failure = %#v, %v; want nil and error", got, err)
		}
		if queued, processing := client.LLen(t.Context(), config.QueueKey).Val(), client.LLen(t.Context(), config.ProcessingKey).Val(); queued != 1 || processing != 0 {
			t.Fatalf("recovered lengths = queue %d, processing %d; want 1, 0", queued, processing)
		}
		hook.configure(false, 0, false)
		recovered, err := queue.Dequeue(t.Context(), time.Second)
		if err != nil || recovered == nil || recovered.ID != task.ID {
			t.Fatalf("recovered Dequeue = %#v, %v", recovered, err)
		}
		if err := queue.Acknowledge(t.Context(), recovered.ID); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("reject-cleanup-allows-duplicate-without-loss", func(t *testing.T) {
		queue, client, config, hook := newQueue(t, "reject", 1)
		task := &core.Task{ID: "cluster-task-reject", Type: "test"}
		if err := queue.Enqueue(t.Context(), task); err != nil {
			t.Fatal(err)
		}
		if got, err := queue.Dequeue(t.Context(), time.Second); err != nil || got == nil {
			t.Fatalf("Dequeue = %#v, %v", got, err)
		}
		hook.command, hook.key = "lrem", config.ProcessingKey
		hook.configure(true, 1, false)
		if err := queue.Reject(t.Context(), task.ID, "retryable"); err == nil {
			t.Fatal("Reject succeeded despite injected cleanup failure")
		}
		if queued, processing := client.LLen(t.Context(), config.QueueKey).Val(), client.LLen(t.Context(), config.ProcessingKey).Val(); queued != 1 || processing != 1 {
			t.Fatalf("post-failure lengths = queue %d, processing %d; want 1, 1", queued, processing)
		}
		hook.configure(false, 0, false)
		if err := queue.Reject(t.Context(), task.ID, "retry cleanup"); err != nil {
			t.Fatal(err)
		}
		if queued, processing := client.LLen(t.Context(), config.QueueKey).Val(), client.LLen(t.Context(), config.ProcessingKey).Val(); queued != 2 || processing != 0 {
			t.Fatalf("post-retry lengths = queue %d, processing %d; want allowed duplicate 2, 0", queued, processing)
		}
	})

	t.Run("ambiguous-enqueue-allows-duplicate-without-loss", func(t *testing.T) {
		queue, client, config, hook := newQueue(t, "ambiguous", 2)
		hook.command, hook.key = "lpush", config.QueueKey
		hook.configure(true, 1, true)
		if err := queue.Enqueue(t.Context(), &core.Task{ID: "cluster-task-ambiguous", Type: "test"}); err != nil {
			t.Fatal(err)
		}
		if queued := client.LLen(t.Context(), config.QueueKey).Val(); queued != 2 {
			t.Fatalf("queue length after ambiguous retry = %d, want allowed duplicate 2", queued)
		}
	})
}

func runClusterDebugRetention(t *testing.T, seeds []string) {
	t.Helper()
	newClient := func(t *testing.T) redis.UniversalClient {
		connection := core.DefaultRedisConnectionConfig()
		connection.Mode = core.RedisModeCluster
		connection.Addrs = append([]string(nil), seeds...)
		client, err := core.NewRedisUniversalClient(connection)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = client.Close() })
		return client
	}

	backendconformance.RunExecutionLineageRetentionConformance(t, func(t *testing.T) backendconformance.ExecutionLineageRetentionFixture {
		const (
			shortTTL = 5 * time.Second
			longTTL  = 30 * time.Second
		)
		client := newClient(t)
		keyspace, err := core.NewRedisKeyspace(clusterNamespace("execution-retention"))
		if err != nil {
			t.Fatal(err)
		}
		keys := orchestration.NewRedisExecutionDebugKeys(keyspace)
		rootID := "retention-root"
		childID := differentSlotExecutionID(t, client, keys, rootID)
		config := orchestration.DefaultExecutionStoreConfig()
		config.TTL = shortTTL
		config.ErrorTTL = longTTL
		hook := &redisCommandFaultHook{
			command: "evalsha", key: keys.Trace("trace-" + rootID),
			err: errors.New("injected execution projection TTL failure"),
		}
		client.AddHook(hook)
		store, err := orchestration.NewRedisExecutionDebugStoreWithClient(
			client, config, orchestration.WithExecutionDebugKeyspace(keyspace),
		)
		if err != nil {
			t.Fatal(err)
		}
		return backendconformance.ExecutionLineageRetentionFixture{
			Store: store, RootID: rootID, ChildID: childID, ShortTTL: shortTTL, LongTTL: longTTL,
			InjectProjectionFailure: func() { hook.configure(true, 1, false) },
			ProjectionFailureCount:  hook.attemptCount,
			AssertMinimumRetention: func(t *testing.T, requestID string, minimum time.Duration) {
				requireRedisMinimumTTL(t, client, keys.Record(requestID), minimum)
				requireRedisMinimumTTL(t, client, keys.RetentionLink(requestID), minimum)
			},
		}
	})

	backendconformance.RunLLMDebugRetentionConformance(t, func(t *testing.T) backendconformance.LLMDebugRetentionFixture {
		const (
			shortTTL = time.Second
			longTTL  = 15 * time.Second
		)
		client := newClient(t)
		keyspace, err := core.NewRedisKeyspace(clusterNamespace("llm-retention"))
		if err != nil {
			t.Fatal(err)
		}
		keys := telemetry.NewRedisLLMDebugKeys(keyspace)
		hook := &redisCommandFaultHook{
			command: "zadd", key: keys.RecentIndex(), err: errors.New("injected LLM projection failure"),
		}
		client.AddHook(hook)
		store, err := orchestration.NewRedisLLMDebugStoreWithClient(
			client,
			orchestration.WithDebugKeyspace(keyspace),
			orchestration.WithDebugTTL(shortTTL),
			orchestration.WithDebugErrorTTL(shortTTL),
		)
		if err != nil {
			t.Fatal(err)
		}
		recorder, err := telemetry.NewRedisLLMCallRecorderWithClient(
			client,
			keyspace,
			telemetry.WithRecorderTTL(shortTTL),
			telemetry.WithRecorderErrorTTL(shortTTL),
		)
		if err != nil {
			t.Fatal(err)
		}
		requestID := "retention-llm"
		return backendconformance.LLMDebugRetentionFixture{
			Store: store, RequestID: requestID, ShortTTL: shortTTL, LongTTL: longTTL,
			Advance: func(duration time.Duration) { time.Sleep(duration + 100*time.Millisecond) },
			Write: func(ctx context.Context, requestID, prompt string) error {
				return recorder.RecordLLMCall(ctx, requestID, telemetry.LLMCallRecord{
					CallType: "cluster-retention", Timestamp: time.Now(), Prompt: prompt, Response: "ok", Success: true,
				})
			},
			SetProjectionFailure: func(enabled bool) {
				if enabled {
					if err := client.ZRem(t.Context(), keys.RecentIndex(), requestID).Err(); err != nil {
						t.Fatal(err)
					}
				}
				hook.configure(enabled, -1, false)
			},
			ProjectionFailureCount: hook.attemptCount,
			InteractionCount: func(t *testing.T, requestID string) int64 {
				count, err := client.LLen(t.Context(), keys.Interactions(requestID)).Result()
				if err != nil {
					t.Fatal(err)
				}
				return count
			},
			RecentIndexPresent: func(t *testing.T, requestID string) bool {
				_, err := client.ZScore(t.Context(), keys.RecentIndex(), requestID).Result()
				if errors.Is(err, redis.Nil) {
					return false
				}
				if err != nil {
					t.Fatal(err)
				}
				return true
			},
			AssertMinimumRetention: func(t *testing.T, requestID string, minimum time.Duration) {
				requireRedisMinimumTTL(t, client, keys.Meta(requestID), minimum)
				requireRedisMinimumTTL(t, client, keys.Interactions(requestID), minimum)
				requireRedisMinimumTTL(t, client, keys.RetentionFloor(requestID), minimum)
			},
		}
	})
}

func differentSlotExecutionID(
	t *testing.T,
	client redis.UniversalClient,
	keys orchestration.RedisExecutionDebugKeys,
	rootID string,
) string {
	t.Helper()
	rootSlot, err := client.ClusterKeySlot(t.Context(), keys.Record(rootID)).Result()
	if err != nil {
		t.Fatal(err)
	}
	for suffix := 0; suffix < 10_000; suffix++ {
		candidate := fmt.Sprintf("retention-child-%d", suffix)
		childSlot, slotErr := client.ClusterKeySlot(t.Context(), keys.Record(candidate)).Result()
		if slotErr != nil {
			t.Fatal(slotErr)
		}
		if childSlot != rootSlot {
			return candidate
		}
	}
	t.Fatal("could not select execution lineage records on different slots")
	return ""
}

func runClusterCommandReconnect(t *testing.T, seeds []string) {
	t.Helper()
	if len(seeds) < 2 {
		t.Fatal("cluster command reconnect test requires two distinct seed nodes")
	}
	deployment := clusterNamespace("command-reconnect")
	publisher := newClusterProviderFixture(t, seeds[:1], deployment, ClientRoleHITL)
	subscriber := newClusterProviderFixture(t, seeds[1:2], deployment, ClientRoleHITL)
	commands, cancel, err := subscriber.backends.Commands().SubscribeCommand(t.Context(), "checkpoint-reconnect")
	if err != nil {
		t.Fatal(err)
	}
	defer cancel()
	if err := publisher.backends.Commands().PublishCommand(t.Context(), &orchestration.Command{
		CheckpointID: "checkpoint-reconnect", Type: orchestration.CommandApprove,
	}); err != nil {
		t.Fatal(err)
	}
	requireClusterCommand(t, commands)

	clusterClient, ok := subscriber.clients.ClientSet().Resolve(ClientRoleHITL).(*redis.ClusterClient)
	if !ok {
		t.Fatal("subscriber fixture did not create a Redis cluster client")
	}
	ctx, stop := context.WithTimeout(t.Context(), 10*time.Second)
	defer stop()
	if err := clusterClient.ForEachShard(ctx, func(ctx context.Context, shard *redis.Client) error {
		return shard.ClientKillByFilter(ctx, "TYPE", "pubsub", "SKIPME", "no").Err()
	}); err != nil {
		t.Fatalf("terminate Pub/Sub connections: %v", err)
	}

	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		if err := publisher.backends.Commands().PublishCommand(ctx, &orchestration.Command{
			CheckpointID: "checkpoint-reconnect", Type: orchestration.CommandReject,
		}); err != nil && ctx.Err() == nil {
			continue
		}
		select {
		case command, open := <-commands:
			if !open {
				t.Fatal("subscription channel closed instead of reconnecting")
			}
			if command != nil && command.Type == orchestration.CommandReject {
				return
			}
		case <-ticker.C:
		case <-ctx.Done():
			t.Fatalf("Pub/Sub subscription did not reconnect: %v", ctx.Err())
		}
	}
}

func requireClusterCommand(t *testing.T, commands <-chan *orchestration.Command) {
	t.Helper()
	select {
	case command, open := <-commands:
		if !open || command == nil {
			t.Fatal("cluster command subscription closed before delivery")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for cluster command")
	}
}

func requireRedisMinimumTTL(t *testing.T, client redis.UniversalClient, key string, minimum time.Duration) {
	t.Helper()
	ttl, err := client.PTTL(t.Context(), key).Result()
	if err != nil || ttl < minimum {
		t.Fatalf("PTTL(%q) = %s, %v; want >= %s", key, ttl, err, minimum)
	}
}

func runClusterTopologyTransitions(t *testing.T, cluster redistest.Cluster) {
	t.Helper()
	connection := core.DefaultRedisConnectionConfig()
	connection.Mode = core.RedisModeCluster
	connection.Addrs = cluster.SeedAddrs()
	client, err := core.NewRedisUniversalClient(connection)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close() })
	keyspace, err := core.NewRedisKeyspace(clusterNamespace("topology"))
	if err != nil {
		t.Fatal(err)
	}

	executionKeys := orchestration.NewRedisExecutionDebugKeys(keyspace)
	llmKeys := telemetry.NewRedisLLMDebugKeys(keyspace)
	skillStore, err := NewSkillStore(client, WithSkillStoreKeyspace(keyspace))
	if err != nil {
		t.Fatal(err)
	}
	skillRef := orchestration.SkillRef{Namespace: "cluster", Name: "publication"}
	atomicGroups := [][]string{
		{
			keyspace.Tagged("registry", "", "service", "one"),
			keyspace.Tagged("registry", "", "index", "all"),
			keyspace.Tagged("registry", "", "index", "capability", "weather"),
			keyspace.Tagged("registry", "", "index", "name", "forecast"),
			keyspace.Tagged("registry", "", "index", "type", string(core.ComponentTypeTool)),
		},
		{
			keyspace.Tagged("hitl", "agent", "checkpoint", "one"),
			keyspace.Tagged("hitl", "agent", "pending"),
			keyspace.Tagged("hitl", "agent", "request", "request-1"),
			keyspace.Tagged("hitl", "agent", "claim", "one"),
		},
		{
			keyspace.Tagged("workflow", "workflow-1", "execution", "one"),
			keyspace.Tagged("workflow", "workflow-1", "executions"),
		},
		{executionKeys.Record("request-1"), executionKeys.RetentionLink("request-1")},
		{llmKeys.Meta("request-1"), llmKeys.Interactions("request-1"), llmKeys.RetentionFloor("request-1")},
		{
			skillStore.currentKey(skillRef), skillStore.nextVersionKey(skillRef),
			skillStore.candidateKey(skillRef, 1), skillStore.revisionKey(skillRef, 1),
			skillStore.manifestKey(skillRef, 1), skillStore.resourceKey(skillRef, 1, "guide.md"),
			skillStore.versionsKey(skillRef), skillStore.catalogKey(),
			skillStore.idempotencyKey(skillRef, "request-1"),
		},
	}
	for _, keys := range atomicGroups {
		firstSlot, err := client.ClusterKeySlot(t.Context(), keys[0]).Result()
		if err != nil {
			t.Fatal(err)
		}
		for _, key := range keys[1:] {
			slot, err := client.ClusterKeySlot(t.Context(), key).Result()
			if err != nil || slot != firstSlot {
				t.Fatalf("atomic keys resolved to slots %d and %d: %v", firstSlot, slot, err)
			}
		}
	}
	left := keyspace.Plain("topology", "left")
	right := keyspace.Plain("topology", "right")
	for suffix := 0; ; suffix++ {
		leftSlot, slotErr := client.ClusterKeySlot(t.Context(), left).Result()
		if slotErr != nil {
			t.Fatal(slotErr)
		}
		right = keyspace.Plain("topology", "right", fmt.Sprint(suffix))
		rightSlot, slotErr := client.ClusterKeySlot(t.Context(), right).Result()
		if slotErr != nil {
			t.Fatal(slotErr)
		}
		if leftSlot != rightSlot {
			break
		}
	}
	if err := client.MGet(t.Context(), left, right).Err(); err == nil || !strings.Contains(strings.ToUpper(err.Error()), "CROSSSLOT") {
		t.Fatalf("real cluster cross-slot MGET error = %v", err)
	}

	// Exercise a real request-correctness path through an exact, paused slot
	// migration. Redirect-disabled clients prove that the servers emitted ASK
	// during migration and MOVED after assignment; the provider client must hide
	// both topology details from the checkpoint claimant.
	deployment := clusterNamespace("topology-hitl")
	fixture := newClusterProviderFixture(t, cluster.SeedAddrs(), deployment, ClientRoleHITL)
	checkpointID := "topology-expired"
	if err := fixture.backends.Checkpoints().SaveCheckpoint(t.Context(), &orchestration.ExecutionCheckpoint{
		CheckpointID: checkpointID,
		RequestID:    "request-" + checkpointID,
		Status:       orchestration.CheckpointStatusPending,
		CreatedAt:    time.Now().Add(-time.Hour),
		ExpiresAt:    time.Now().Add(-time.Minute),
		RequestMode:  orchestration.RequestModeNonStreaming,
	}); err != nil {
		t.Fatal(err)
	}
	hitlKeyspace, err := core.NewRedisKeyspace(deployment)
	if err != nil {
		t.Fatal(err)
	}
	pendingKey := hitlKeyspace.Tagged("hitl", "cluster-agent", "pending")
	probeKey := hitlKeyspace.Tagged("hitl", "cluster-agent", "migration-probe")
	slot, err := client.ClusterKeySlot(t.Context(), pendingKey).Result()
	if err != nil {
		t.Fatal(err)
	}
	staleForAsk := redis.NewClusterClient(&redis.ClusterOptions{Addrs: cluster.SeedAddrs(), MaxRedirects: -1})
	staleForMoved := redis.NewClusterClient(&redis.ClusterOptions{Addrs: cluster.SeedAddrs(), MaxRedirects: -1})
	t.Cleanup(func() {
		_ = staleForAsk.Close()
		_ = staleForMoved.Close()
	})
	for _, stale := range []*redis.ClusterClient{staleForAsk, staleForMoved} {
		if err := stale.SCard(t.Context(), pendingKey).Err(); err != nil {
			t.Fatalf("prime redirect-disabled cluster slot cache: %v", err)
		}
	}
	migration, err := cluster.BeginSlotMigration(t.Context(), slot)
	if err != nil {
		t.Fatal(err)
	}
	if err := staleForAsk.Set(t.Context(), probeKey, "probe", time.Minute).Err(); err == nil ||
		!strings.Contains(strings.ToUpper(err.Error()), "ASK") {
		t.Fatalf("redirect-disabled command during slot migration = %v; want ASK", err)
	}

	claimStarted := make(chan struct{})
	claimRelease := make(chan struct{})
	fixture.clients.ClientSet().Resolve(ClientRoleHITL).AddHook(&redisCommandBarrierHook{
		command: "smembers", key: pendingKey, started: claimStarted, release: claimRelease,
	})
	claimResult := make(chan error, 1)
	go func() {
		claimCtx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
		defer cancel()
		claimed, claimErr := fixture.backends.CheckpointExpiry().ClaimExpiredCheckpoints(
			claimCtx,
			orchestration.ExpiredCheckpointClaimRequest{
				Before: time.Now(), Limit: 1, Owner: "topology-owner", Lease: time.Minute,
			},
		)
		if claimErr == nil && (len(claimed) != 1 || claimed[0].CheckpointID != checkpointID) {
			claimErr = fmt.Errorf("claimed checkpoints = %v; want %q", checkpointIDs(claimed), checkpointID)
		}
		claimResult <- claimErr
	}()
	select {
	case <-claimStarted:
	case <-time.After(5 * time.Second):
		t.Fatal("checkpoint claim did not reach the migrating slot")
	}
	if err := migration.Complete(t.Context()); err != nil {
		t.Fatal(err)
	}
	close(claimRelease)
	if err := <-claimResult; err != nil {
		t.Fatalf("checkpoint claim exposed a topology transition: %v", err)
	}
	if err := staleForMoved.SCard(t.Context(), pendingKey).Err(); err == nil ||
		!strings.Contains(strings.ToUpper(err.Error()), "MOVED") {
		t.Fatalf("redirect-disabled command after slot migration = %v; want MOVED", err)
	}
	if _, err := fixture.backends.Checkpoints().LoadCheckpoint(t.Context(), checkpointID); err != nil {
		t.Fatalf("provider client did not refresh migrated slot ownership: %v", err)
	}

	var operationErrors atomic.Uint64
	loadCtx, stopLoad := context.WithCancel(t.Context())
	var wait sync.WaitGroup
	for worker := 0; worker < 8; worker++ {
		worker := worker
		wait.Add(1)
		go func() {
			defer wait.Done()
			for sequence := 0; ; sequence++ {
				select {
				case <-loadCtx.Done():
					return
				default:
				}
				key := keyspace.Plain("topology", "load", fmt.Sprint(worker), fmt.Sprint(sequence%64))
				if err := client.Set(loadCtx, key, sequence, time.Minute).Err(); err != nil && loadCtx.Err() == nil {
					operationErrors.Add(1)
				}
				if err := client.Get(loadCtx, key).Err(); err != nil && loadCtx.Err() == nil {
					operationErrors.Add(1)
				}
			}
		}()
	}
	transitionCtx, cancel := context.WithTimeout(t.Context(), 45*time.Second)
	defer cancel()
	if err := cluster.TriggerPrimaryFailover(transitionCtx); err != nil {
		stopLoad()
		wait.Wait()
		t.Fatal(err)
	}
	stopLoad()
	wait.Wait()
	if errors := operationErrors.Load(); errors != 0 {
		t.Fatalf("domain-visible errors during reshard/failover = %d", errors)
	}
	if err := client.Set(t.Context(), keyspace.Plain("topology", "after-failover"), "ok", time.Minute).Err(); err != nil {
		t.Fatalf("client did not refresh topology after failover: %v", err)
	}
}

func runClusterUnavailableShard(t *testing.T, cluster redistest.Cluster) {
	t.Helper()
	connection := core.DefaultRedisConnectionConfig()
	connection.Mode = core.RedisModeCluster
	connection.Addrs = cluster.SeedAddrs()
	connection.DialTimeout = time.Second
	connection.ReadTimeout = time.Second
	connection.WriteTimeout = time.Second
	connection.MaxRetries = 1
	client, err := core.NewRedisUniversalClient(connection)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close() })
	keyspace, err := core.NewRedisKeyspace(clusterNamespace("unavailable-shard"))
	if err != nil {
		t.Fatal(err)
	}
	keys := orchestration.NewRedisExecutionDebugKeys(keyspace)
	indexPrimary := clusterPrimaryForKey(t, client, keys.RecentIndex())
	aliveID := executionIDForPrimary(t, client, keys, indexPrimary, true)
	unavailableID := executionIDForPrimary(t, client, keys, indexPrimary, false)
	store, err := orchestration.NewRedisExecutionDebugStoreWithClient(
		client,
		orchestration.DefaultExecutionStoreConfig(),
		orchestration.WithExecutionDebugKeyspace(keyspace),
	)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	for index, requestID := range []string{aliveID, unavailableID} {
		if err := store.Store(t.Context(), &orchestration.StoredExecution{
			RequestID: requestID, TraceID: "trace-" + requestID, OriginalRequest: "request",
			CreatedAt: now.Add(time.Duration(index) * time.Millisecond),
			Result:    &orchestration.ExecutionResult{Success: true},
		}); err != nil {
			t.Fatal(err)
		}
	}
	unavailableSlot, err := client.ClusterKeySlot(t.Context(), keys.Record(unavailableID)).Result()
	if err != nil {
		t.Fatal(err)
	}
	if err := cluster.DisableShardForSlot(t.Context(), unavailableSlot); err != nil {
		t.Fatal(err)
	}
	// Let the surviving primaries observe the failed nodes before testing the
	// partial-availability contract.
	time.Sleep(1500 * time.Millisecond)

	queryCtx, cancel := context.WithTimeout(t.Context(), 8*time.Second)
	defer cancel()
	summaries, err := store.ListRecent(queryCtx, 10)
	if err == nil {
		t.Fatalf("ListRecent returned ambiguous partial success: %#v", summaries)
	}
	if summaries != nil {
		t.Fatalf("ListRecent returned partial records with error: %#v, %v", summaries, err)
	}
	if _, err := store.Get(t.Context(), aliveID); err != nil {
		t.Fatalf("surviving shard became unavailable: %v", err)
	}
	if _, err := client.ZScore(t.Context(), keys.RecentIndex(), unavailableID).Result(); err != nil {
		t.Fatalf("transient shard failure pruned a live projection member: %v", err)
	}
}

func executionIDForPrimary(
	t *testing.T,
	client redis.UniversalClient,
	keys orchestration.RedisExecutionDebugKeys,
	primary string,
	wantSame bool,
) string {
	t.Helper()
	for suffix := 0; suffix < 100_000; suffix++ {
		candidate := fmt.Sprintf("availability-%t-%d", wantSame, suffix)
		same := clusterPrimaryForKey(t, client, keys.Record(candidate)) == primary
		if same == wantSame {
			return candidate
		}
	}
	t.Fatalf("could not select execution key with same-primary=%t", wantSame)
	return ""
}

func checkpointIDs(checkpoints []*orchestration.ExecutionCheckpoint) []string {
	ids := make([]string, 0, len(checkpoints))
	for _, checkpoint := range checkpoints {
		if checkpoint != nil {
			ids = append(ids, checkpoint.CheckpointID)
		}
	}
	return ids
}

func clusterTaskQueueDeployment(t *testing.T, seeds []string) string {
	t.Helper()
	connection := core.DefaultRedisConnectionConfig()
	connection.Mode = core.RedisModeCluster
	connection.Addrs = append([]string(nil), seeds...)
	client, err := core.NewRedisUniversalClient(connection)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close() })
	for attempt := 0; attempt < 128; attempt++ {
		deployment := clusterNamespace("queue-shards")
		keyspace, err := core.NewRedisKeyspace(deployment)
		if err != nil {
			t.Fatal(err)
		}
		if clusterKeysUseDifferentPrimaries(
			t,
			client,
			keyspace.Plain("tasks", "queue"),
			keyspace.Plain("tasks", "processing"),
		) {
			return deployment
		}
	}
	t.Fatal("could not select task queue keys on different cluster primaries")
	return ""
}

func requireClusterKeysOnDifferentPrimaries(t *testing.T, client redis.UniversalClient, keys ...string) {
	t.Helper()
	if !clusterKeysUseDifferentPrimaries(t, client, keys...) {
		t.Fatalf("keys do not resolve to different cluster primaries: %q", keys)
	}
}

func clusterKeysUseDifferentPrimaries(t *testing.T, client redis.UniversalClient, keys ...string) bool {
	t.Helper()
	slots, err := client.ClusterSlots(t.Context()).Result()
	if err != nil {
		t.Fatal(err)
	}
	primaries := make(map[string]struct{}, len(keys))
	for _, key := range keys {
		slot, err := client.ClusterKeySlot(t.Context(), key).Result()
		if err != nil {
			t.Fatal(err)
		}
		primary := ""
		for _, assignment := range slots {
			if int(slot) >= assignment.Start && int(slot) <= assignment.End && len(assignment.Nodes) > 0 {
				primary = assignment.Nodes[0].Addr
				break
			}
		}
		if primary == "" {
			t.Fatalf("no primary owns slot %d for key %q", slot, key)
		}
		primaries[primary] = struct{}{}
	}
	return len(primaries) == len(keys)
}

func clusterPrimaryForKey(t *testing.T, client redis.UniversalClient, key string) string {
	t.Helper()
	slots, err := client.ClusterSlots(t.Context()).Result()
	if err != nil {
		t.Fatal(err)
	}
	slot, err := client.ClusterKeySlot(t.Context(), key).Result()
	if err != nil {
		t.Fatal(err)
	}
	for _, assignment := range slots {
		if int(slot) >= assignment.Start && int(slot) <= assignment.End && len(assignment.Nodes) > 0 {
			return assignment.Nodes[0].Addr
		}
	}
	t.Fatalf("no primary owns slot %d for key %q", slot, key)
	return ""
}

func newClusterProviderFixture(t *testing.T, seeds []string, deployment string, roles ...ClientRole) *clusterProviderFixture {
	t.Helper()
	config, err := ConfigureClientConfig(
		DefaultClientConfig(),
		WithConnectionMode(core.RedisModeCluster),
		WithAddresses(seeds...),
		WithDatabase(0),
	)
	if err != nil {
		t.Fatal(err)
	}
	ownedOptions := []OwnedClientsOption(nil)
	if len(roles) > 0 {
		ownedOptions = append(ownedOptions, WithOwnedClientRoles(roles...))
	}
	clients, err := NewOwnedClients(config, ownedOptions...)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := clients.Close(); err != nil {
			t.Errorf("close Redis clients: %v", err)
		}
	})
	options, err := NewOptions(WithDeployment(deployment), WithAgentScope("cluster-agent"))
	if err != nil {
		t.Fatal(err)
	}
	backends, err := NewOrchestrationBackends(clients.ClientSet(), options)
	if err != nil {
		t.Fatal(err)
	}
	return &clusterProviderFixture{backends: backends, clients: clients}
}

func clusterNamespace(label string) string {
	return fmt.Sprintf("cluster-%s-%d", label, clusterFixtureSequence.Add(1))
}

func requireAllProviderCapabilities(t *testing.T, backends *orchestration.OrchestrationBackends) {
	t.Helper()
	if backends.Execution() == nil || backends.LLMDebug() == nil || backends.Checkpoints() == nil ||
		backends.CheckpointExpiry() == nil || backends.Commands() == nil || backends.Workflow() == nil ||
		backends.Schedules() == nil || backends.Tasks() == nil || backends.TaskQueue() == nil ||
		backends.TaskDispatcher() == nil || backends.TaskConsumer() == nil || backends.Lock() == nil ||
		backends.SkillRegistry() == nil {
		t.Fatal("real-cluster provider composition omitted one or more capabilities")
	}
}
