package redisprovider

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/truvaagents/truva-g3/core"
	"github.com/truvaagents/truva-g3/orchestration"
)

const (
	defaultWorkflowStateTTL    = 24 * time.Hour
	defaultTaskQueueRetryCount = 3
	defaultTaskQueueRetryDelay = 100 * time.Millisecond
	defaultTaskIndexInterval   = time.Minute
	defaultTaskIndexMaxIDs     = 1000
	defaultScheduleMax         = 10_000
)

type Options struct {
	keyspace          core.RedisKeyspace
	agentScope        string
	executionConfig   orchestration.ExecutionStoreConfig
	llmDebugTTL       time.Duration
	llmDebugErrorTTL  time.Duration
	checkpointTTL     time.Duration
	expiryEnabled     bool
	expiryCallback    orchestration.ExpiryCallback
	expiryConfig      orchestration.ExpiryProcessorConfig
	expiryOptions     []orchestration.CheckpointExpiryProcessorOption
	workflowTTL       time.Duration
	taskQueueScope    string
	taskRetryCount    int
	taskRetryDelay    time.Duration
	taskIndexInterval time.Duration
	taskIndexMaxIDs   int
	scheduleMax       int
	logger            core.Logger
}

type Option interface{ applyOptions(*Options) error }
type optionFunc func(*Options) error

func (option optionFunc) applyOptions(options *Options) error { return option(options) }

func NewOptions(options ...Option) (Options, error) {
	defaults := orchestration.NewDefaultOrchestratorConfig()
	keyspace, err := core.NewRedisKeyspace("default")
	if err != nil {
		return Options{}, err
	}
	configured := Options{
		keyspace:          keyspace,
		executionConfig:   defaults.ExecutionStore,
		llmDebugTTL:       defaults.LLMDebug.TTL,
		llmDebugErrorTTL:  defaults.LLMDebug.ErrorTTL,
		checkpointTTL:     defaults.HITL.CheckpointTTL,
		workflowTTL:       defaultWorkflowStateTTL,
		taskRetryCount:    defaultTaskQueueRetryCount,
		taskRetryDelay:    defaultTaskQueueRetryDelay,
		taskIndexInterval: defaultTaskIndexInterval,
		taskIndexMaxIDs:   defaultTaskIndexMaxIDs,
		scheduleMax:       defaultScheduleMax,
	}
	return ConfigureOptions(configured, options...)
}

// ConfigureOptions applies explicit code configuration to an existing option
// set. Use it after LoadOptionsFromEnvironment when code must win over
// deployment defaults.
func ConfigureOptions(configured Options, options ...Option) (Options, error) {
	if configured.keyspace.Deployment() == "" {
		keyspace, err := core.NewRedisKeyspace("default")
		if err != nil {
			return Options{}, err
		}
		configured.keyspace = keyspace
	}
	for index, option := range options {
		if option == nil {
			return Options{}, fmt.Errorf("redisprovider: option %d is nil", index)
		}
		if err := option.applyOptions(&configured); err != nil {
			return Options{}, err
		}
	}
	return configured, nil
}

// LoadOptionsFromEnvironment applies Redis-preset operational limits without
// constructing clients or backends. It is strict and never includes a rejected
// raw value in its errors.
func LoadOptionsFromEnvironment(base Options, lookup func(string) (string, bool)) (Options, error) {
	if lookup == nil {
		return Options{}, fmt.Errorf("redisprovider: environment lookup is required")
	}
	if base.workflowTTL <= 0 {
		base.workflowTTL = defaultWorkflowStateTTL
	}
	defaults := orchestration.NewDefaultOrchestratorConfig()
	if base.llmDebugTTL <= 0 {
		base.llmDebugTTL = defaults.LLMDebug.TTL
	}
	if base.llmDebugErrorTTL <= 0 {
		base.llmDebugErrorTTL = defaults.LLMDebug.ErrorTTL
	}
	if base.checkpointTTL <= 0 {
		base.checkpointTTL = defaults.HITL.CheckpointTTL
	}
	if base.taskRetryCount <= 0 {
		base.taskRetryCount = defaultTaskQueueRetryCount
	}
	if base.taskRetryDelay <= 0 {
		base.taskRetryDelay = defaultTaskQueueRetryDelay
	}
	if base.taskIndexInterval <= 0 {
		base.taskIndexInterval = defaultTaskIndexInterval
	}
	if base.taskIndexMaxIDs <= 0 {
		base.taskIndexMaxIDs = defaultTaskIndexMaxIDs
	}
	if base.scheduleMax <= 0 {
		base.scheduleMax = defaultScheduleMax
	}
	if deployment, present := lookup("TRUVAG3_REDIS_NAMESPACE"); present {
		configured, err := ConfigureOptions(base, WithDeployment(deployment))
		if err != nil {
			return Options{}, err
		}
		base = configured
	}
	if agent, present := lookup("TRUVAG3_AGENT_NAME"); present && strings.TrimSpace(agent) != "" {
		configured, err := ConfigureOptions(base, WithAgentScope(agent))
		if err != nil {
			return Options{}, err
		}
		base = configured
	}
	if service, present := lookup(core.EnvServiceName); present {
		configured, err := ConfigureOptions(base, WithTaskQueueScope(service))
		if err != nil {
			return Options{}, err
		}
		base = configured
	}

	if raw, present := lookup("TRUVAG3_WORKFLOW_STATE_TTL"); present {
		value, err := time.ParseDuration(strings.TrimSpace(raw))
		if err != nil || value <= 0 {
			return Options{}, fmt.Errorf("redisprovider: TRUVAG3_WORKFLOW_STATE_TTL must be a positive duration")
		}
		base.workflowTTL = value
	}
	for _, setting := range []struct {
		name   string
		target *time.Duration
	}{
		{name: "TRUVAG3_LLM_DEBUG_TTL", target: &base.llmDebugTTL},
		{name: "TRUVAG3_LLM_DEBUG_ERROR_TTL", target: &base.llmDebugErrorTTL},
		{name: "TRUVAG3_HITL_CHECKPOINT_TTL", target: &base.checkpointTTL},
	} {
		if raw, present := lookup(setting.name); present {
			value, err := time.ParseDuration(strings.TrimSpace(raw))
			if err != nil || value <= 0 {
				return Options{}, fmt.Errorf("redisprovider: %s must be a positive duration", setting.name)
			}
			*setting.target = value
		}
	}
	if raw, present := lookup("TRUVAG3_TASK_QUEUE_RETRY_ATTEMPTS"); present {
		value, err := strconv.Atoi(strings.TrimSpace(raw))
		if err != nil || value <= 0 {
			return Options{}, fmt.Errorf("redisprovider: TRUVAG3_TASK_QUEUE_RETRY_ATTEMPTS must be a positive integer")
		}
		base.taskRetryCount = value
	}
	if raw, present := lookup("TRUVAG3_TASK_QUEUE_RETRY_DELAY"); present {
		value, err := time.ParseDuration(strings.TrimSpace(raw))
		if err != nil || value <= 0 {
			return Options{}, fmt.Errorf("redisprovider: TRUVAG3_TASK_QUEUE_RETRY_DELAY must be a positive duration")
		}
		base.taskRetryDelay = value
	}
	if raw, present := lookup("TRUVAG3_TASK_INDEX_RECONCILE_INTERVAL"); present {
		value, err := time.ParseDuration(strings.TrimSpace(raw))
		if err != nil || value <= 0 {
			return Options{}, fmt.Errorf("redisprovider: TRUVAG3_TASK_INDEX_RECONCILE_INTERVAL must be a positive duration")
		}
		base.taskIndexInterval = value
	}
	if raw, present := lookup("TRUVAG3_TASK_INDEX_RECONCILE_MAX_IDS"); present {
		value, err := strconv.Atoi(strings.TrimSpace(raw))
		if err != nil || value <= 0 {
			return Options{}, fmt.Errorf("redisprovider: TRUVAG3_TASK_INDEX_RECONCILE_MAX_IDS must be a positive integer")
		}
		base.taskIndexMaxIDs = value
	}
	if raw, present := lookup("TRUVAG3_SCHEDULER_MAX_SCHEDULES"); present {
		value, err := strconv.Atoi(strings.TrimSpace(raw))
		if err != nil || value <= 0 {
			return Options{}, fmt.Errorf("redisprovider: TRUVAG3_SCHEDULER_MAX_SCHEDULES must be a positive integer")
		}
		base.scheduleMax = value
	}
	return base, nil
}

// WithDeployment selects the validated deployment segment used by every
// versioned Redis key produced by the provider.
func WithDeployment(deployment string) Option {
	return optionFunc(func(options *Options) error {
		keyspace, err := core.NewRedisKeyspace(deployment)
		if err != nil {
			return fmt.Errorf("redisprovider: deployment namespace: %w", err)
		}
		options.keyspace = keyspace
		return nil
	})
}

// WithNamespace is retained as a deprecated precursor alias.
// Deprecated: use WithDeployment.
func WithNamespace(namespace string) Option { return WithDeployment(namespace) }

// WithAgentScope isolates HITL coordination for one agent while preserving a
// cluster-safe encoded hash tag.
func WithAgentScope(agent string) Option {
	return optionFunc(func(options *Options) error {
		agent = strings.TrimSpace(agent)
		if agent == "" {
			return fmt.Errorf("redisprovider: agent scope is required")
		}
		options.agentScope = agent
		return nil
	})
}

func WithExecutionStoreConfig(config orchestration.ExecutionStoreConfig) Option {
	return optionFunc(func(options *Options) error {
		options.executionConfig = config
		return nil
	})
}

// WithLLMDebugRetention configures successful and failed LLM debug record
// retention without exposing Redis-specific settings through OrchestratorConfig.
func WithLLMDebugRetention(ttl, errorTTL time.Duration) Option {
	return optionFunc(func(options *Options) error {
		if ttl <= 0 || errorTTL <= 0 {
			return fmt.Errorf("redisprovider: LLM debug TTLs must be positive")
		}
		options.llmDebugTTL = ttl
		options.llmDebugErrorTTL = errorTTL
		return nil
	})
}

// WithCheckpointTTL configures Redis checkpoint retention.
func WithCheckpointTTL(ttl time.Duration) Option {
	return optionFunc(func(options *Options) error {
		if ttl <= 0 {
			return fmt.Errorf("redisprovider: checkpoint TTL must be positive")
		}
		options.checkpointTTL = ttl
		return nil
	})
}

// WithLogger supplies the application-owned logger to every logging-capable
// backend assembled by this preset. Individual backends retain ownership of
// their component attribution and nil-safe defaults.
func WithLogger(logger core.Logger) Option {
	return optionFunc(func(options *Options) error {
		options.logger = logger
		return nil
	})
}

func WithWorkflowStateTTL(ttl time.Duration) Option {
	return optionFunc(func(options *Options) error {
		if ttl <= 0 {
			return fmt.Errorf("redisprovider: workflow state TTL must be positive")
		}
		options.workflowTTL = ttl
		return nil
	})
}

func WithTaskQueueRetryPolicy(attempts int, delay time.Duration) Option {
	return optionFunc(func(options *Options) error {
		if attempts <= 0 || delay <= 0 {
			return fmt.Errorf("redisprovider: task queue retry attempts and delay must be positive")
		}
		options.taskRetryCount = attempts
		options.taskRetryDelay = delay
		return nil
	})
}

// WithTaskQueueScope isolates the scheduler delivery and in-flight queues for
// one service. An empty value explicitly selects the shared scheduling queue.
func WithTaskQueueScope(service string) Option {
	return optionFunc(func(options *Options) error {
		options.taskQueueScope = strings.TrimSpace(service)
		return nil
	})
}

func WithTaskIndexReconciliation(interval time.Duration, maxIDs int) Option {
	return optionFunc(func(options *Options) error {
		if interval <= 0 || maxIDs <= 0 {
			return fmt.Errorf("redisprovider: task index reconcile interval and maximum must be positive")
		}
		options.taskIndexInterval = interval
		options.taskIndexMaxIDs = maxIDs
		return nil
	})
}

// WithMaxSchedules bounds the control-plane schedule catalog. Code options are
// applied after environment configuration by NewDefaultBackends.
func WithMaxSchedules(maximum int) Option {
	return optionFunc(func(options *Options) error {
		if maximum <= 0 {
			return fmt.Errorf("redisprovider: maximum schedules must be positive")
		}
		options.scheduleMax = maximum
		return nil
	})
}

// WithCheckpointExpiry adds the provider-neutral expiry processor to the
// returned runnable set. The Redis store supplies only atomic claims.
func WithCheckpointExpiry(
	config orchestration.ExpiryProcessorConfig,
	callback orchestration.ExpiryCallback,
	processorOptions ...orchestration.CheckpointExpiryProcessorOption,
) Option {
	return optionFunc(func(options *Options) error {
		options.expiryEnabled = true
		options.expiryConfig = config
		options.expiryCallback = callback
		options.expiryOptions = append([]orchestration.CheckpointExpiryProcessorOption(nil), processorOptions...)
		return nil
	})
}
