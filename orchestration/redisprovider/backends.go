package redisprovider

import (
	"fmt"

	"github.com/truvaagents/truva-g3/core"
	"github.com/truvaagents/truva-g3/orchestration"
)

func NewOrchestrationBackends(
	clients *ClientSet,
	options Options,
	overrides ...orchestration.OrchestrationBackendOption,
) (*orchestration.OrchestrationBackends, error) {
	if clients == nil {
		return nil, fmt.Errorf("redisprovider: client set is required")
	}
	backendOptions := make([]orchestration.OrchestrationBackendOption, 0, 19)

	if client := clients.Resolve(ClientRoleExecution); client != nil {
		executionOptions := []orchestration.RedisExecutionDebugStoreOption{
			orchestration.WithExecutionDebugKeyspace(options.keyspace),
		}
		if options.logger != nil {
			executionOptions = append(executionOptions, orchestration.WithExecutionDebugLogger(options.logger))
		}
		store, err := orchestration.NewRedisExecutionDebugStoreWithClient(client, options.executionConfig, executionOptions...)
		if err != nil {
			return nil, fmt.Errorf("redisprovider: execution backend: %w", err)
		}
		backendOptions = append(backendOptions, orchestration.WithExecutionBackend(store))
	}

	if client := clients.Resolve(ClientRoleLLMDebug); client != nil {
		llmOptions := []orchestration.RedisLLMDebugStoreOption{
			orchestration.WithDebugTTL(options.llmDebugTTL),
			orchestration.WithDebugErrorTTL(options.llmDebugErrorTTL),
			orchestration.WithDebugKeyspace(options.keyspace),
		}
		if options.logger != nil {
			llmOptions = append(llmOptions, orchestration.WithDebugLogger(options.logger))
		}
		store, err := orchestration.NewRedisLLMDebugStoreWithClient(client, llmOptions...)
		if err != nil {
			return nil, fmt.Errorf("redisprovider: LLM debug backend: %w", err)
		}
		backendOptions = append(backendOptions, orchestration.WithLLMDebugBackend(store))
	}

	if client := clients.Resolve(ClientRoleHITL); client != nil {
		checkpointOptions := []orchestration.RedisCheckpointStoreOption{
			orchestration.WithCheckpointTTL(options.checkpointTTL),
			orchestration.WithCheckpointKeyspace(options.keyspace, options.agentScope),
		}
		commandOptions := []orchestration.RedisCommandStoreOption{
			orchestration.WithCommandStoreKeyspace(options.keyspace, options.agentScope),
		}
		if options.logger != nil {
			checkpointOptions = append(checkpointOptions, orchestration.WithCheckpointStoreLogger(options.logger))
			commandOptions = append(commandOptions, orchestration.WithCommandStoreLogger(options.logger))
		}
		checkpoints, err := orchestration.NewRedisCheckpointStoreWithClient(client, checkpointOptions...)
		if err != nil {
			return nil, fmt.Errorf("redisprovider: checkpoint backend: %w", err)
		}
		commands, err := orchestration.NewRedisCommandStoreWithClient(client, commandOptions...)
		if err != nil {
			return nil, fmt.Errorf("redisprovider: command backend: %w", err)
		}
		backendOptions = append(backendOptions,
			orchestration.WithCheckpointPersistence(checkpoints),
			orchestration.WithCheckpointExpiry(checkpoints),
			orchestration.WithCommandBackend(commands),
		)
	}

	if client := clients.Resolve(ClientRoleWorkflow); client != nil {
		workflow, err := orchestration.NewRedisStateStoreWithClient(client, options.keyspace, options.workflowTTL)
		if err != nil {
			return nil, fmt.Errorf("redisprovider: workflow backend: %w", err)
		}
		backendOptions = append(backendOptions, orchestration.WithWorkflowBackend(workflow))
	}

	if client := clients.Resolve(ClientRoleScheduling); client != nil {
		scheduleConfig := orchestration.DefaultRedisScheduleStoreConfig()
		scheduleConfig.Keyspace = &options.keyspace
		scheduleConfig.MaxSchedules = options.scheduleMax
		if options.logger != nil {
			scheduleConfig.Logger = options.logger
		}
		taskPrefix := options.keyspace.Plain("tasks")
		schedules, err := orchestration.NewRedisScheduleStore(client, scheduleConfig)
		if err != nil {
			return nil, fmt.Errorf("redisprovider: schedule backend: %w", err)
		}
		dispatcher, err := orchestration.NewRedisTaskDispatcherWithPrefix(client, taskPrefix)
		if err != nil {
			return nil, fmt.Errorf("redisprovider: task dispatcher backend: %w", err)
		}
		consumer, err := orchestration.NewRedisTaskConsumerWithPrefix(client, orchestration.ScheduledExecutorQueue, taskPrefix)
		if err != nil {
			return nil, fmt.Errorf("redisprovider: task consumer backend: %w", err)
		}
		taskConfig := orchestration.DefaultRedisTaskStoreConfig()
		taskConfig.KeyPrefix = taskPrefix
		taskConfig.Logger = options.logger
		tasks := orchestration.NewRedisTaskStore(client, &taskConfig)
		queueSuffix := []string{"queue"}
		processingSuffix := []string{"processing"}
		if options.taskQueueScope != "" {
			queueSuffix = append(queueSuffix, options.taskQueueScope)
			processingSuffix = append(processingSuffix, options.taskQueueScope)
		}
		queueConfig := orchestration.RedisTaskQueueConfig{
			QueueKey:      options.keyspace.Plain("tasks", queueSuffix...),
			ProcessingKey: options.keyspace.Plain("tasks", processingSuffix...),
			RetryAttempts: options.taskRetryCount,
			RetryDelay:    options.taskRetryDelay,
			Logger:        options.logger,
		}
		queue := orchestration.NewRedisTaskQueue(client, &queueConfig)
		lockPrefix := options.keyspace.Plain("locks")
		lock, err := newRedisDistributedLock(client, lockPrefix, componentLogger(options.logger))
		if err != nil {
			return nil, fmt.Errorf("redisprovider: distributed lock backend: %w", err)
		}
		backendOptions = append(backendOptions,
			orchestration.WithScheduleBackend(schedules),
			orchestration.WithTaskBackend(tasks),
			orchestration.WithTaskQueueBackend(queue),
			orchestration.WithTaskDispatcherBackend(dispatcher),
			orchestration.WithTaskConsumerBackend(consumer),
			orchestration.WithLockBackend(lock),
		)
	}

	if client := clients.Resolve(ClientRoleSkills); client != nil {
		skillOptions := []SkillStoreOption{
			WithSkillStoreKeyspace(options.keyspace),
		}
		if options.logger != nil {
			skillOptions = append(skillOptions, WithSkillStoreLogger(componentLogger(options.logger)))
		}
		store, err := NewSkillStore(client, skillOptions...)
		if err != nil {
			return nil, fmt.Errorf("redisprovider: skill backend: %w", core.RedactSensitiveError(err))
		}
		backendOptions = append(backendOptions,
			orchestration.WithSkillRegistryBackend(store),
			orchestration.WithSkillRevisionReader(store),
			orchestration.WithSkillAdministrationStore(store),
			orchestration.WithSkillRevisionDeletionStore(store),
			orchestration.WithSkillAuditSink(store),
		)
	}

	backends, err := orchestration.NewOrchestrationBackends(backendOptions...)
	if err != nil {
		return nil, err
	}
	backends, err = backends.With(overrides...)
	if err != nil {
		return nil, err
	}
	if taskStore, ok := backends.Tasks().(*orchestration.RedisTaskStore); ok {
		reconciler, err := orchestration.NewTaskIndexReconciler(
			taskStore,
			options.taskIndexInterval,
			options.taskIndexMaxIDs,
		)
		if err != nil {
			return nil, fmt.Errorf("redisprovider: task index reconciler: %w", err)
		}
		backends, err = backends.With(orchestration.WithRunnables(reconciler))
		if err != nil {
			return nil, err
		}
	}
	// Compose the provider default only after caller overrides have established
	// the final checkpoint dependencies. A caller-supplied processor wins, and
	// the neutral composition rejects a processor placed before dependency
	// overrides instead of silently substituting this default.
	if options.expiryEnabled && backends.CheckpointExpiryProcessor() == nil {
		backends, err = withCheckpointExpiryProcessor(backends, options)
		if err != nil {
			return nil, err
		}
	}
	return backends, nil
}

var _ core.Runnable = (*orchestration.CheckpointExpiryProcessor)(nil)

func componentLogger(logger core.Logger) core.Logger {
	if componentAware, ok := logger.(core.ComponentAwareLogger); ok {
		return componentAware.WithComponent("framework/orchestration")
	}
	return logger
}

func withCheckpointExpiryProcessor(
	backends *orchestration.OrchestrationBackends,
	options Options,
) (*orchestration.OrchestrationBackends, error) {
	expiryOptions := make([]orchestration.CheckpointExpiryProcessorOption, 0, len(options.expiryOptions)+1)
	if options.logger != nil {
		expiryOptions = append(expiryOptions, orchestration.WithCheckpointExpiryLogger(options.logger))
	}
	expiryOptions = append(expiryOptions, options.expiryOptions...)
	processor, err := orchestration.NewCheckpointExpiryProcessor(
		backends.Checkpoints(), backends.CheckpointExpiry(), options.expiryCallback, options.expiryConfig, expiryOptions...,
	)
	if err != nil {
		return nil, fmt.Errorf("redisprovider: checkpoint expiry processor: %w", err)
	}
	configured, err := backends.With(orchestration.WithCheckpointExpiryProcessor(processor))
	if err != nil {
		return nil, fmt.Errorf("redisprovider: checkpoint expiry processor composition: %w", err)
	}
	return configured, nil
}
