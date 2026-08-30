package main

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/nats-io/nats.go"
	"github.com/redis/go-redis/v9"
	"github.com/truvaagents/truva-g3/core"
	"github.com/truvaagents/truva-g3/examples/orchestration-backend-portability/internal/natsadapter"
	"github.com/truvaagents/truva-g3/examples/orchestration-backend-portability/internal/postgresadapter"
	"github.com/truvaagents/truva-g3/examples/orchestration-backend-portability/internal/redisadapter"
	"github.com/truvaagents/truva-g3/orchestration"
)

const (
	DefaultQueue      = "portable-weather"
	DefaultWorkflowID = "portable-weather-workflow"
)

// Config contains provider endpoints and the example's logical isolation.
// Each role validates only the fields that it actually consumes.
type Config struct {
	PostgresURL string
	NATSURL     string
	RedisURL    string
	Namespace   string
	Queue       string
	WorkflowID  string
	AckWait     time.Duration
}

type BackendSelection struct {
	Provider       string `json:"provider"`
	Implementation string `json:"implementation"`
}

// BackendDescriptor is derived at the composition root from validated
// contracts and the concrete adapters installed for them.
type BackendDescriptor struct {
	Validated            bool                        `json:"validated"`
	RequiredCapabilities []string                    `json:"required_capabilities"`
	SelectedBackends     map[string]BackendSelection `json:"selected_backends"`
}

type APIBackends struct {
	Workflow   orchestration.StateStore
	Dispatcher core.TaskDispatcher
	Queue      string
	WorkflowID string
	Descriptor BackendDescriptor
}

type WorkerBackends struct {
	Workflow   orchestration.StateStore
	Consumer   core.TaskConsumer
	Queue      string
	Descriptor BackendDescriptor
}

type SchedulerBackends struct {
	Schedules  core.ScheduleStore
	Tasks      core.TaskStore
	Dispatcher core.TaskDispatcher
	Lock       core.DistributedLock
	Descriptor BackendDescriptor
}

type ExecutorBackends struct {
	Tasks      core.TaskStore
	Consumer   core.TaskConsumer
	Discovery  serviceResolver
	Descriptor BackendDescriptor
}

// backendOwner closes only the connections opened by one role builder.
type backendOwner struct {
	once    sync.Once
	closers []func() error
	err     error
}

func (owner *backendOwner) add(closer func() error) {
	owner.closers = append(owner.closers, closer)
}

func (owner *backendOwner) Close() error {
	if owner == nil {
		return nil
	}
	owner.once.Do(func() {
		var closeErrors []error
		for index := len(owner.closers) - 1; index >= 0; index-- {
			if err := owner.closers[index](); err != nil {
				closeErrors = append(closeErrors, err)
			}
		}
		owner.err = errors.Join(closeErrors...)
	})
	return owner.err
}

func buildAPIBackends(ctx context.Context, config Config) (APIBackends, *backendOwner, error) {
	if err := validateRoleConfig("api", config, roleNeeds{Queue: true, WorkflowID: true}); err != nil {
		return APIBackends{}, nil, err
	}
	owner := &backendOwner{}
	built := false
	defer func() {
		if !built {
			_ = owner.Close()
		}
	}()
	workflow, err := openWorkflowStore(ctx, config, owner)
	if err != nil {
		return APIBackends{}, nil, err
	}
	transport, err := openTaskTransport(ctx, config, "api", owner)
	if err != nil {
		return APIBackends{}, nil, err
	}
	backends, err := orchestration.NewOrchestrationBackends(
		orchestration.WithWorkflowBackend(workflow),
		orchestration.WithTaskDispatcherBackend(transport),
	)
	if err != nil {
		return APIBackends{}, nil, err
	}
	requirements, err := orchestration.NewBackendRequirements(
		orchestration.BackendWorkflowState,
		orchestration.BackendTaskDispatcher,
	)
	if err != nil {
		return APIBackends{}, nil, err
	}
	descriptor, err := describeBackends(backends, requirements, map[orchestration.BackendCapability]providerBinding{
		orchestration.BackendWorkflowState:  {provider: "postgresql", implementation: workflow},
		orchestration.BackendTaskDispatcher: {provider: "nats-jetstream", implementation: transport},
	})
	if err != nil {
		return APIBackends{}, nil, err
	}
	built = true
	return APIBackends{
		Workflow: workflow, Dispatcher: transport, Queue: config.Queue,
		WorkflowID: config.WorkflowID, Descriptor: descriptor,
	}, owner, nil
}

func buildWorkerBackends(ctx context.Context, config Config) (WorkerBackends, *backendOwner, error) {
	if err := validateRoleConfig("worker", config, roleNeeds{Queue: true}); err != nil {
		return WorkerBackends{}, nil, err
	}
	owner := &backendOwner{}
	built := false
	defer func() {
		if !built {
			_ = owner.Close()
		}
	}()
	workflow, err := openWorkflowStore(ctx, config, owner)
	if err != nil {
		return WorkerBackends{}, nil, err
	}
	transport, err := openTaskTransport(ctx, config, "worker", owner)
	if err != nil {
		return WorkerBackends{}, nil, err
	}
	backends, err := orchestration.NewOrchestrationBackends(
		orchestration.WithWorkflowBackend(workflow),
		orchestration.WithTaskConsumerBackend(transport),
	)
	if err != nil {
		return WorkerBackends{}, nil, err
	}
	requirements, err := orchestration.NewBackendRequirements(
		orchestration.BackendWorkflowState,
		orchestration.BackendTaskConsumer,
	)
	if err != nil {
		return WorkerBackends{}, nil, err
	}
	descriptor, err := describeBackends(backends, requirements, map[orchestration.BackendCapability]providerBinding{
		orchestration.BackendWorkflowState: {provider: "postgresql", implementation: workflow},
		orchestration.BackendTaskConsumer:  {provider: "nats-jetstream", implementation: transport},
	})
	if err != nil {
		return WorkerBackends{}, nil, err
	}
	built = true
	return WorkerBackends{
		Workflow: workflow, Consumer: transport,
		Queue: config.Queue, Descriptor: descriptor,
	}, owner, nil
}

func buildSchedulerBackends(
	ctx context.Context,
	config Config,
) (SchedulerBackends, *backendOwner, error) {
	if err := validateRoleConfig("scheduler", config, roleNeeds{Redis: true}); err != nil {
		return SchedulerBackends{}, nil, err
	}
	owner := &backendOwner{}
	built := false
	defer func() {
		if !built {
			_ = owner.Close()
		}
	}()
	pool, err := openPostgres(ctx, config, owner)
	if err != nil {
		return SchedulerBackends{}, nil, err
	}
	schedules, err := postgresadapter.NewScheduleStore(pool, config.Namespace)
	if err != nil {
		return SchedulerBackends{}, nil, err
	}
	tasks, err := postgresadapter.NewTaskStore(pool, config.Namespace)
	if err != nil {
		return SchedulerBackends{}, nil, err
	}
	transport, err := openTaskTransport(ctx, config, "scheduler", owner)
	if err != nil {
		return SchedulerBackends{}, nil, err
	}
	redisClient, err := openRedis(ctx, config, owner)
	if err != nil {
		return SchedulerBackends{}, nil, err
	}
	lock, err := redisadapter.NewDistributedLock(redisClient, config.Namespace)
	if err != nil {
		return SchedulerBackends{}, nil, fmt.Errorf("scheduler lock: %w", err)
	}
	backends, err := orchestration.NewOrchestrationBackends(
		orchestration.WithScheduleBackend(schedules),
		orchestration.WithTaskBackend(tasks),
		orchestration.WithTaskDispatcherBackend(transport),
		orchestration.WithLockBackend(lock),
	)
	if err != nil {
		return SchedulerBackends{}, nil, err
	}
	requirements, err := orchestration.RequirementsForFeatures(nil, orchestration.BackendFeatureSchedulerProducer)
	if err != nil {
		return SchedulerBackends{}, nil, err
	}
	descriptor, err := describeBackends(backends, requirements, map[orchestration.BackendCapability]providerBinding{
		orchestration.BackendSchedules:      {provider: "postgresql", implementation: schedules},
		orchestration.BackendTasks:          {provider: "postgresql", implementation: tasks},
		orchestration.BackendTaskDispatcher: {provider: "nats-jetstream", implementation: transport},
		orchestration.BackendLock:           {provider: "redis", implementation: lock},
	})
	if err != nil {
		return SchedulerBackends{}, nil, err
	}
	built = true
	return SchedulerBackends{
		Schedules: schedules, Tasks: tasks, Dispatcher: transport, Lock: lock, Descriptor: descriptor,
	}, owner, nil
}

func buildExecutorBackends(ctx context.Context, config Config) (ExecutorBackends, *backendOwner, error) {
	if err := validateRoleConfig("scheduled-executor", config, roleNeeds{Redis: true}); err != nil {
		return ExecutorBackends{}, nil, err
	}
	owner := &backendOwner{}
	built := false
	defer func() {
		if !built {
			_ = owner.Close()
		}
	}()
	pool, err := openPostgres(ctx, config, owner)
	if err != nil {
		return ExecutorBackends{}, nil, err
	}
	tasks, err := postgresadapter.NewTaskStore(pool, config.Namespace)
	if err != nil {
		return ExecutorBackends{}, nil, err
	}
	transport, err := openTaskTransport(ctx, config, "scheduled-executor", owner)
	if err != nil {
		return ExecutorBackends{}, nil, err
	}
	discovery, err := core.NewRedisDiscovery(config.RedisURL)
	if err != nil {
		return ExecutorBackends{}, nil, fmt.Errorf("scheduled executor discovery: %w", err)
	}
	backends, err := orchestration.NewOrchestrationBackends(
		orchestration.WithTaskBackend(tasks),
		orchestration.WithTaskConsumerBackend(transport),
	)
	if err != nil {
		return ExecutorBackends{}, nil, err
	}
	requirements, err := orchestration.RequirementsForFeatures(
		nil,
		orchestration.BackendFeatureTaskStorage,
		orchestration.BackendFeatureScheduledWorker,
	)
	if err != nil {
		return ExecutorBackends{}, nil, err
	}
	descriptor, err := describeBackends(backends, requirements, map[orchestration.BackendCapability]providerBinding{
		orchestration.BackendTasks:        {provider: "postgresql", implementation: tasks},
		orchestration.BackendTaskConsumer: {provider: "nats-jetstream", implementation: transport},
	}, providerBinding{name: "discovery", provider: "redis", implementation: discovery})
	if err != nil {
		return ExecutorBackends{}, nil, err
	}
	built = true
	return ExecutorBackends{
		Tasks: tasks, Consumer: transport, Discovery: discovery, Descriptor: descriptor,
	}, owner, nil
}

type providerBinding struct {
	name           string
	provider       string
	implementation interface{}
}

func describeBackends(
	backends *orchestration.OrchestrationBackends,
	requirements orchestration.BackendRequirements,
	bindings map[orchestration.BackendCapability]providerBinding,
	extras ...providerBinding,
) (BackendDescriptor, error) {
	if err := backends.ValidateFor(requirements); err != nil {
		return BackendDescriptor{}, fmt.Errorf("validate role composition: %w", err)
	}
	capabilities := requirements.Capabilities()
	descriptor := BackendDescriptor{
		Validated:            true,
		RequiredCapabilities: make([]string, 0, len(capabilities)),
		SelectedBackends:     make(map[string]BackendSelection, len(capabilities)+len(extras)),
	}
	for _, capability := range capabilities {
		binding, ok := bindings[capability]
		if !ok || strings.TrimSpace(binding.provider) == "" || binding.implementation == nil {
			return BackendDescriptor{}, fmt.Errorf("describe role composition: capability %q has no provider binding", capability)
		}
		descriptor.RequiredCapabilities = append(descriptor.RequiredCapabilities, string(capability))
		descriptor.SelectedBackends[string(capability)] = selection(binding.provider, binding.implementation)
	}
	for _, binding := range extras {
		if strings.TrimSpace(binding.name) == "" || strings.TrimSpace(binding.provider) == "" || binding.implementation == nil {
			return BackendDescriptor{}, fmt.Errorf("describe role composition: invalid supplemental provider binding")
		}
		descriptor.SelectedBackends[binding.name] = selection(binding.provider, binding.implementation)
	}
	return descriptor, nil
}

func selection(provider string, implementation interface{}) BackendSelection {
	return BackendSelection{Provider: provider, Implementation: fmt.Sprintf("%T", implementation)}
}

type roleNeeds struct {
	Redis      bool
	Queue      bool
	WorkflowID bool
}

type configRequirement struct {
	name  string
	value string
}

func validateRoleConfig(role string, config Config, needs roleNeeds) error {
	required := []configRequirement{
		{name: "POSTGRES_URL", value: config.PostgresURL},
		{name: "NATS_URL", value: config.NATSURL},
		{name: "PORTABILITY_BACKEND_NAMESPACE", value: config.Namespace},
	}
	if needs.Redis {
		required = append(required, configRequirement{name: "REDIS_URL", value: config.RedisURL})
	}
	if needs.Queue {
		required = append(required, configRequirement{name: "PORTABILITY_QUEUE", value: config.Queue})
	}
	if needs.WorkflowID {
		required = append(required, configRequirement{name: "PORTABILITY_WORKFLOW_ID", value: config.WorkflowID})
	}
	for _, field := range required {
		if strings.TrimSpace(field.value) == "" {
			return fmt.Errorf("%s backends: %s is required", role, field.name)
		}
	}
	if config.AckWait <= 0 {
		return fmt.Errorf("%s backends: PORTABILITY_ACK_WAIT must be positive", role)
	}
	return nil
}

func openPostgres(ctx context.Context, config Config, owner *backendOwner) (*pgxpool.Pool, error) {
	pool, err := pgxpool.New(ctx, config.PostgresURL)
	if err != nil {
		return nil, fmt.Errorf("open PostgreSQL: %w", err)
	}
	owner.add(func() error { pool.Close(); return nil })
	if err := pool.Ping(ctx); err != nil {
		return nil, fmt.Errorf("ping PostgreSQL: %w", err)
	}
	return pool, nil
}

func openWorkflowStore(
	ctx context.Context,
	config Config,
	owner *backendOwner,
) (orchestration.StateStore, error) {
	pool, err := openPostgres(ctx, config, owner)
	if err != nil {
		return nil, err
	}
	store, err := postgresadapter.NewWorkflowStore(pool, config.Namespace)
	if err != nil {
		return nil, err
	}
	return store, nil
}

func openTaskTransport(
	ctx context.Context,
	config Config,
	role string,
	owner *backendOwner,
) (*natsadapter.TaskTransport, error) {
	connection, err := nats.Connect(
		config.NATSURL,
		nats.Name("truvag3-portability-"+role+"-"+config.Namespace),
		nats.Timeout(5*time.Second),
	)
	if err != nil {
		return nil, fmt.Errorf("connect to NATS for %s: %w", role, err)
	}
	owner.add(func() error { connection.Close(); return nil })
	transport, err := natsadapter.NewTaskTransport(
		ctx,
		connection,
		config.Namespace,
		natsadapter.WithAckWait(config.AckWait),
	)
	if err != nil {
		return nil, err
	}
	owner.add(transport.Close)
	return transport, nil
}

func openRedis(ctx context.Context, config Config, owner *backendOwner) (redis.UniversalClient, error) {
	options, err := redis.ParseURL(config.RedisURL)
	if err != nil {
		return nil, fmt.Errorf("parse Redis URL: %w", err)
	}
	client := redis.NewClient(core.ApplyRedisClientDefaults(options))
	owner.add(client.Close)
	if err := client.Ping(ctx).Err(); err != nil {
		return nil, fmt.Errorf("ping Redis: %w", err)
	}
	return client, nil
}
