package main_test

import (
	"context"
	"fmt"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/nats-io/nats.go"
	"github.com/redis/go-redis/v9"
	"github.com/truvaagents/truva-g3/core"
	"github.com/truvaagents/truva-g3/core/conformance"
	"github.com/truvaagents/truva-g3/examples/orchestration-backend-portability/internal/natsadapter"
	"github.com/truvaagents/truva-g3/examples/orchestration-backend-portability/internal/postgresadapter"
	"github.com/truvaagents/truva-g3/examples/orchestration-backend-portability/internal/redisadapter"
	"github.com/truvaagents/truva-g3/orchestration"
	"github.com/truvaagents/truva-g3/orchestration/backendconformance"
	"github.com/truvaagents/truva-g3/orchestration/redisprovider"
)

const integrationFlag = "PORTABILITY_INTEGRATION"

var fixtureSequence atomic.Uint64

func TestPostgresWorkflowConformance(t *testing.T) {
	requireIntegration(t)
	pool := newPostgresPool(t)
	if err := postgresadapter.EnsureSchema(t.Context(), pool); err != nil {
		t.Fatal(err)
	}

	backendconformance.RunWorkflowStateConformance(t, func(t *testing.T) backendconformance.WorkflowFixture {
		namespace := fixtureNamespace(t, "postgres-workflow")
		first, err := postgresadapter.NewWorkflowStore(pool, namespace)
		if err != nil {
			t.Fatal(err)
		}
		second, err := postgresadapter.NewWorkflowStore(pool, namespace)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			cleanupContext, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if err := first.DeleteNamespace(cleanupContext); err != nil {
				t.Errorf("clean PostgreSQL fixture: %v", err)
			}
		})
		return backendconformance.WorkflowFixture{First: first, Second: second}
	})
}

func TestPostgresScheduleStoreConformance(t *testing.T) {
	requireIntegration(t)
	pool := newPostgresPool(t)
	if err := postgresadapter.EnsureSchema(t.Context(), pool); err != nil {
		t.Fatal(err)
	}

	conformance.RunScheduleStoreConformance(t, func(t *testing.T) conformance.ScheduleStoreFixture {
		namespace := fixtureNamespace(t, "postgres-schedules")
		isolatedNamespace := fixtureNamespace(t, "postgres-schedules-isolated")
		first, err := postgresadapter.NewScheduleStore(pool, namespace)
		if err != nil {
			t.Fatal(err)
		}
		second, err := postgresadapter.NewScheduleStore(pool, namespace)
		if err != nil {
			t.Fatal(err)
		}
		isolated, err := postgresadapter.NewScheduleStore(pool, isolatedNamespace)
		if err != nil {
			t.Fatal(err)
		}
		return conformance.ScheduleStoreFixture{
			First:    first,
			Second:   second,
			Isolated: isolated,
			Cleanup: func() {
				cleanupContext, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				if err := first.DeleteNamespace(cleanupContext); err != nil {
					t.Errorf("clean PostgreSQL schedule fixture: %v", err)
				}
				if err := isolated.DeleteNamespace(cleanupContext); err != nil {
					t.Errorf("clean isolated PostgreSQL schedule fixture: %v", err)
				}
			},
		}
	})
}

func TestPostgresTaskStoreConformance(t *testing.T) {
	requireIntegration(t)
	pool := newPostgresPool(t)
	if err := postgresadapter.EnsureSchema(t.Context(), pool); err != nil {
		t.Fatal(err)
	}

	conformance.RunTaskStoreConformance(t, func(t *testing.T) conformance.TaskStoreFixture {
		namespace := fixtureNamespace(t, "postgres-tasks")
		isolatedNamespace := fixtureNamespace(t, "postgres-tasks-isolated")
		first, err := postgresadapter.NewTaskStore(pool, namespace)
		if err != nil {
			t.Fatal(err)
		}
		second, err := postgresadapter.NewTaskStore(pool, namespace)
		if err != nil {
			t.Fatal(err)
		}
		isolated, err := postgresadapter.NewTaskStore(pool, isolatedNamespace)
		if err != nil {
			t.Fatal(err)
		}
		return conformance.TaskStoreFixture{
			First:    first,
			Second:   second,
			Isolated: isolated,
			Cleanup: func() {
				cleanupContext, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				if err := first.DeleteNamespace(cleanupContext); err != nil {
					t.Errorf("clean PostgreSQL task fixture: %v", err)
				}
				if err := isolated.DeleteNamespace(cleanupContext); err != nil {
					t.Errorf("clean isolated PostgreSQL task fixture: %v", err)
				}
			},
		}
	})
}

func TestNATSCommandConformance(t *testing.T) {
	requireIntegration(t)
	backendconformance.RunCommandStoreConformance(t, func(t *testing.T) backendconformance.CommandFixture {
		namespace := fixtureNamespace(t, "nats-commands")
		publisherConnection := newNATSConnection(t)
		subscriberConnection := newNATSConnection(t)
		publisher, err := natsadapter.NewCommandStore(publisherConnection, namespace)
		if err != nil {
			t.Fatal(err)
		}
		subscriber, err := natsadapter.NewCommandStore(subscriberConnection, namespace)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			_ = publisher.Close()
			_ = subscriber.Close()
			publisherConnection.Close()
			subscriberConnection.Close()
		})
		return backendconformance.CommandFixture{Publisher: publisher, Subscriber: subscriber}
	})
}

func TestNATSJetStreamTaskDeliveryConformance(t *testing.T) {
	requireIntegration(t)
	conformance.RunTaskDeliveryProfileConformance(
		t,
		conformance.TaskDeliveryAtLeastOnce,
		func(t *testing.T) conformance.TaskDeliveryFixture {
			namespace := fixtureNamespace(t, "nats-tasks")
			dispatcherConnection := newNATSConnection(t)
			consumerConnection := newNATSConnection(t)
			dispatcher, err := natsadapter.NewTaskTransport(t.Context(), dispatcherConnection, namespace)
			if err != nil {
				t.Fatal(err)
			}
			consumer, err := natsadapter.NewTaskTransport(t.Context(), consumerConnection, namespace)
			if err != nil {
				t.Fatal(err)
			}
			cleanup := func() {
				cleanupContext, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				if err := consumer.DeleteStream(cleanupContext); err != nil {
					t.Errorf("clean NATS fixture: %v", err)
				}
				_ = dispatcher.Close()
				_ = consumer.Close()
				dispatcherConnection.Close()
				consumerConnection.Close()
			}
			return conformance.TaskDeliveryFixture{
				Consumer:           consumer,
				Dispatcher:         dispatcher,
				Cleanup:            cleanup,
				DeadLetterContains: consumer.DeadLetterContains,
				RecoverAbandoned:   consumer.RecoverAbandoned,
			}
		},
	)
}

func TestRedisDistributedLockConformance(t *testing.T) {
	requireIntegration(t)
	backendconformance.RunDistributedLockConformance(t, func(t *testing.T) backendconformance.LockFixture {
		client := newRedisClient(t)
		namespace := fixtureNamespace(t, "redis-lock")
		first, err := redisadapter.NewDistributedLock(client, namespace)
		if err != nil {
			t.Fatal(err)
		}
		second, err := redisadapter.NewDistributedLock(client, namespace)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			cleanupContext, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			for _, key := range []string{"shared", "expiring", "cancelled"} {
				_ = first.Release(cleanupContext, key)
				_ = second.Release(cleanupContext, key)
			}
		})
		return backendconformance.LockFixture{
			Locks:   []core.DistributedLock{first, second},
			Advance: time.Sleep,
		}
	})
}

func TestMixedProviderComposition(t *testing.T) {
	requireIntegration(t)
	ctx := t.Context()

	pool := newPostgresPool(t)
	if err := postgresadapter.EnsureSchema(ctx, pool); err != nil {
		t.Fatal(err)
	}
	workflow, err := postgresadapter.NewWorkflowStore(pool, fixtureNamespace(t, "mixed-workflow"))
	if err != nil {
		t.Fatal(err)
	}
	schedulerNamespace := fixtureNamespace(t, "mixed-scheduler")
	schedules, err := postgresadapter.NewScheduleStore(pool, schedulerNamespace)
	if err != nil {
		t.Fatal(err)
	}
	taskStore, err := postgresadapter.NewTaskStore(pool, schedulerNamespace)
	if err != nil {
		t.Fatal(err)
	}

	natsConnection := newNATSConnection(t)
	commands, err := natsadapter.NewCommandStore(natsConnection, fixtureNamespace(t, "mixed-commands"))
	if err != nil {
		t.Fatal(err)
	}
	tasks, err := natsadapter.NewTaskTransport(ctx, natsConnection, fixtureNamespace(t, "mixed-tasks"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanupContext, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = workflow.DeleteNamespace(cleanupContext)
		_ = schedules.DeleteNamespace(cleanupContext)
		_ = taskStore.DeleteNamespace(cleanupContext)
		_ = tasks.DeleteStream(cleanupContext)
		_ = tasks.Close()
		_ = commands.Close()
	})

	redisClient := newRedisClient(t)
	clients, err := redisprovider.NewClientSet(redisClient)
	if err != nil {
		t.Fatal(err)
	}
	redisOptions, err := redisprovider.NewOptions(redisprovider.WithNamespace("phase4-mixed"))
	if err != nil {
		t.Fatal(err)
	}
	backends, err := redisprovider.NewOrchestrationBackends(
		clients,
		redisOptions,
		orchestration.WithWorkflowBackend(workflow),
		orchestration.WithScheduleBackend(schedules),
		orchestration.WithTaskBackend(taskStore),
		orchestration.WithCommandBackend(commands),
		orchestration.WithTaskDispatcherBackend(tasks),
		orchestration.WithTaskConsumerBackend(tasks),
	)
	if err != nil {
		t.Fatal(err)
	}

	requirements, err := orchestration.RequirementsForFeatures(
		nil,
		orchestration.BackendFeatureWorkflow,
		orchestration.BackendFeatureCrossInstanceHITL,
		orchestration.BackendFeatureSchedulerProducer,
		orchestration.BackendFeatureScheduledWorker,
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := backends.ValidateFor(requirements); err != nil {
		t.Fatalf("mixed provider composition did not satisfy enabled features: %v", err)
	}
	if backends.Workflow() != workflow || backends.Schedules() != schedules ||
		backends.Tasks() != taskStore || backends.Commands() != commands ||
		backends.TaskDispatcher() != tasks || backends.TaskConsumer() != tasks {
		t.Fatal("capability override did not preserve the selected PostgreSQL/NATS adapters")
	}
	if backends.Checkpoints() == nil || backends.Lock() == nil {
		t.Fatal("mixed composition discarded required Redis capabilities")
	}

	execution := &orchestration.WorkflowExecution{
		ID:         "mixed-execution",
		WorkflowID: "mixed-workflow",
		Status:     orchestration.ExecutionPending,
		Steps:      make(map[string]*orchestration.StepExecution),
	}
	if err := backends.Workflow().SaveExecution(ctx, execution); err != nil {
		t.Fatalf("PostgreSQL workflow override: %v", err)
	}
	if loaded, err := backends.Workflow().GetExecution(ctx, execution.ID); err != nil || loaded.ID != execution.ID {
		t.Fatalf("PostgreSQL workflow round trip: execution=%#v err=%v", loaded, err)
	}

	commandChannel, cancelCommand, err := backends.Commands().SubscribeCommand(ctx, "mixed-checkpoint")
	if err != nil {
		t.Fatal(err)
	}
	defer cancelCommand()
	if err := backends.Commands().PublishCommand(ctx, &orchestration.Command{
		CheckpointID: "mixed-checkpoint",
		Type:         orchestration.CommandApprove,
	}); err != nil {
		t.Fatalf("NATS command override: %v", err)
	}
	select {
	case command := <-commandChannel:
		if command == nil || command.Type != orchestration.CommandApprove {
			t.Fatalf("NATS command round trip: %#v", command)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("NATS command override did not deliver across the composition boundary")
	}

	task := core.NewTask("mixed-task", core.ScheduledTaskType, map[string]interface{}{"proof": "phase4"})
	if err := backends.TaskDispatcher().Dispatch(ctx, orchestration.ScheduledExecutorQueue, task); err != nil {
		t.Fatalf("NATS task dispatcher override: %v", err)
	}
	handle, err := backends.TaskConsumer().Consume(ctx, orchestration.ScheduledExecutorQueue)
	if err != nil || handle == nil || handle.Task().ID != task.ID {
		t.Fatalf("NATS task consumer override: handle=%#v err=%v", handle, err)
	}
	if err := handle.Ack(ctx); err != nil {
		t.Fatalf("NATS task acknowledgement: %v", err)
	}
}

func requireIntegration(t *testing.T) {
	t.Helper()
	if os.Getenv(integrationFlag) != "1" {
		t.Skipf("set %s=1 or run ./setup.sh full-deploy", integrationFlag)
	}
}

func newPostgresPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	url := requiredEnvironment(t, "POSTGRES_URL")
	pool, err := pgxpool.New(t.Context(), url)
	if err != nil {
		t.Fatalf("configure PostgreSQL pool: %v", err)
	}
	t.Cleanup(pool.Close)
	if err := pool.Ping(t.Context()); err != nil {
		t.Fatalf("connect to PostgreSQL: %v", err)
	}
	return pool
}

func newNATSConnection(t *testing.T) *nats.Conn {
	t.Helper()
	connection, err := nats.Connect(
		requiredEnvironment(t, "NATS_URL"),
		nats.Name("truvag3-portability-conformance"),
		nats.Timeout(5*time.Second),
		nats.NoReconnect(),
	)
	if err != nil {
		t.Fatalf("connect to NATS: %v", err)
	}
	return connection
}

func newRedisClient(t *testing.T) *redis.Client {
	t.Helper()
	options, err := redis.ParseURL(requiredEnvironment(t, "REDIS_URL"))
	if err != nil {
		t.Fatalf("configure Redis client: %v", err)
	}
	client := redis.NewClient(options)
	t.Cleanup(func() { _ = client.Close() })
	if err := client.Ping(t.Context()).Err(); err != nil {
		t.Fatalf("connect to Redis: %v", err)
	}
	return client
}

func requiredEnvironment(t *testing.T, name string) string {
	t.Helper()
	value := os.Getenv(name)
	if value == "" {
		t.Fatalf("%s is required when %s=1", name, integrationFlag)
	}
	return value
}

func fixtureNamespace(t *testing.T, prefix string) string {
	t.Helper()
	return fmt.Sprintf("%s-%d-%d", prefix, time.Now().UnixNano(), fixtureSequence.Add(1))
}
