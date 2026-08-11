package redisprovider

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/truvaagents/truva-g3/core"
	"github.com/truvaagents/truva-g3/orchestration"
)

var namespacePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$`)

const (
	defaultWorkflowStateTTL    = 24 * time.Hour
	defaultTaskQueueRetryCount = 3
	defaultTaskQueueRetryDelay = 100 * time.Millisecond
)

type Options struct {
	namespace       string
	executionConfig orchestration.ExecutionStoreConfig
	expiryEnabled   bool
	expiryCallback  orchestration.ExpiryCallback
	expiryConfig    orchestration.ExpiryProcessorConfig
	expiryOptions   []orchestration.CheckpointExpiryProcessorOption
	workflowTTL     time.Duration
	taskRetryCount  int
	taskRetryDelay  time.Duration
	logger          core.Logger
}

type Option interface{ applyOptions(*Options) error }
type optionFunc func(*Options) error

func (option optionFunc) applyOptions(options *Options) error { return option(options) }

func NewOptions(options ...Option) (Options, error) {
	defaults := orchestration.NewDefaultOrchestratorConfig()
	configured := Options{
		executionConfig: defaults.ExecutionStore,
		workflowTTL:     defaultWorkflowStateTTL,
		taskRetryCount:  defaultTaskQueueRetryCount,
		taskRetryDelay:  defaultTaskQueueRetryDelay,
	}
	return ConfigureOptions(configured, options...)
}

// ConfigureOptions applies explicit code configuration to an existing option
// set. Use it after LoadOptionsFromEnvironment when code must win over
// deployment defaults.
func ConfigureOptions(configured Options, options ...Option) (Options, error) {
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
	if base.taskRetryCount <= 0 {
		base.taskRetryCount = defaultTaskQueueRetryCount
	}
	if base.taskRetryDelay <= 0 {
		base.taskRetryDelay = defaultTaskQueueRetryDelay
	}

	if raw, present := lookup("TRUVAG3_WORKFLOW_STATE_TTL"); present {
		value, err := time.ParseDuration(strings.TrimSpace(raw))
		if err != nil || value <= 0 {
			return Options{}, fmt.Errorf("redisprovider: TRUVAG3_WORKFLOW_STATE_TTL must be a positive duration")
		}
		base.workflowTTL = value
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
	return base, nil
}

func WithNamespace(namespace string) Option {
	return optionFunc(func(options *Options) error {
		if !namespacePattern.MatchString(namespace) {
			return fmt.Errorf("redisprovider: namespace must match %s", namespacePattern.String())
		}
		options.namespace = namespace
		return nil
	})
}

func WithExecutionStoreConfig(config orchestration.ExecutionStoreConfig) Option {
	return optionFunc(func(options *Options) error {
		options.executionConfig = config
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
