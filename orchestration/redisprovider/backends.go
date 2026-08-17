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
		executionOptions := []orchestration.RedisExecutionDebugStoreOption{}
		if options.logger != nil {
			executionOptions = append(executionOptions, orchestration.WithExecutionDebugLogger(componentLogger(options.logger)))
		}
		if options.namespace != "" {
			executionOptions = append(executionOptions, orchestration.WithExecutionDebugKeyPrefix(options.namespace+":execution:debug"))
		}
		store, err := orchestration.NewRedisExecutionDebugStoreWithClient(client, options.executionConfig, executionOptions...)
		if err != nil {
			return nil, fmt.Errorf("redisprovider: execution backend: %w", err)
		}
		backendOptions = append(backendOptions, orchestration.WithExecutionBackend(store))
	}

	if client := clients.Resolve(ClientRoleLLMDebug); client != nil {
		llmOptions := []orchestration.RedisLLMDebugStoreOption{}
		if options.logger != nil {
			llmOptions = append(llmOptions, orchestration.WithDebugLogger(componentLogger(options.logger)))
		}
		if options.namespace != "" {
			llmOptions = append(llmOptions, orchestration.WithDebugKeyPrefix(options.namespace+":llm:debug"))
		}
		store, err := orchestration.NewRedisLLMDebugStoreWithClient(client, llmOptions...)
		if err != nil {
			return nil, fmt.Errorf("redisprovider: LLM debug backend: %w", err)
		}
		backendOptions = append(backendOptions, orchestration.WithLLMDebugBackend(store))
	}

	if client := clients.Resolve(ClientRoleHITL); client != nil {
		checkpointOptions := []orchestration.RedisCheckpointStoreOption{}
		commandOptions := []orchestration.RedisCommandStoreOption{}
		if options.logger != nil {
			checkpointOptions = append(checkpointOptions, orchestration.WithCheckpointStoreLogger(options.logger))
			commandOptions = append(commandOptions, orchestration.WithCommandStoreLogger(options.logger))
		}
		if options.namespace != "" {
			checkpointOptions = append(checkpointOptions, orchestration.WithCheckpointKeyPrefix(options.namespace+":hitl"))
			commandOptions = append(commandOptions, orchestration.WithCommandStoreKeyPrefix(options.namespace+":hitl"))
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
		if options.expiryEnabled {
			expiryOptions := make([]orchestration.CheckpointExpiryProcessorOption, 0, len(options.expiryOptions)+1)
			if options.logger != nil {
				expiryOptions = append(expiryOptions, orchestration.WithCheckpointExpiryLogger(options.logger))
			}
			expiryOptions = append(expiryOptions, options.expiryOptions...)
			processor, err := orchestration.NewCheckpointExpiryProcessor(
				checkpoints, checkpoints, options.expiryCallback, options.expiryConfig, expiryOptions...,
			)
			if err != nil {
				return nil, fmt.Errorf("redisprovider: checkpoint expiry processor: %w", err)
			}
			backendOptions = append(backendOptions, orchestration.WithRunnables(processor))
		}
	}

	if client := clients.Resolve(ClientRoleWorkflow); client != nil {
		workflowPrefix := "workflow"
		if options.namespace != "" {
			workflowPrefix = options.namespace + ":workflow"
		}
		workflow, err := orchestration.NewRedisStateStoreWithClientAndPrefix(client, options.workflowTTL, workflowPrefix)
		if err != nil {
			return nil, fmt.Errorf("redisprovider: workflow backend: %w", err)
		}
		backendOptions = append(backendOptions, orchestration.WithWorkflowBackend(workflow))
	}

	if client := clients.Resolve(ClientRoleScheduling); client != nil {
		scheduleConfig := orchestration.DefaultRedisScheduleStoreConfig()
		if options.logger != nil {
			scheduleConfig.Logger = componentLogger(options.logger)
		}
		taskPrefix := "truvag3:tasks"
		if options.namespace != "" {
			scheduleConfig.KeyPrefix = options.namespace + ":schedules"
			taskPrefix = options.namespace + ":tasks"
		}
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
		tasks := orchestration.NewRedisTaskStoreWithClient(client, &taskConfig)
		queueConfig := orchestration.DefaultRedisTaskQueueConfig()
		queueConfig.Logger = options.logger
		if options.namespace != "" {
			queueConfig.QueueKey = taskPrefix + ":queue"
			queueConfig.ProcessingKey = taskPrefix + ":processing"
		}
		queueConfig.RetryAttempts = options.taskRetryCount
		queueConfig.RetryDelay = options.taskRetryDelay
		queue := orchestration.NewRedisTaskQueueWithClient(client, &queueConfig)
		lockPrefix := defaultLockKeyPrefix
		if options.namespace != "" {
			lockPrefix = options.namespace + ":locks"
		}
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
		skillOptions := []SkillStoreOption{}
		if options.namespace != "" {
			skillOptions = append(skillOptions, WithSkillStoreKeyPrefix(options.namespace+":skills"))
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
	return backends.With(overrides...)
}

var _ core.Runnable = (*orchestration.CheckpointExpiryProcessor)(nil)

func componentLogger(logger core.Logger) core.Logger {
	if componentAware, ok := logger.(core.ComponentAwareLogger); ok {
		return componentAware.WithComponent("framework/orchestration")
	}
	return logger
}
