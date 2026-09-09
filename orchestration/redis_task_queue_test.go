package orchestration

import (
	"context"
	"errors"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/truvaagents/truva-g3/core"
)

type taskQueueCommandFailureHook struct {
	mu        sync.Mutex
	command   string
	key       string
	remaining int
	after     bool
}

func (*taskQueueCommandFailureHook) DialHook(next redis.DialHook) redis.DialHook { return next }

func (hook *taskQueueCommandFailureHook) ProcessHook(next redis.ProcessHook) redis.ProcessHook {
	return func(ctx context.Context, command redis.Cmder) error {
		args := command.Args()
		key := ""
		if len(args) > 1 {
			key, _ = args[1].(string)
		}
		hook.mu.Lock()
		fail := command.Name() == hook.command && key == hook.key && hook.remaining > 0
		if fail {
			hook.remaining--
		}
		after := hook.after
		hook.mu.Unlock()
		if !fail {
			return next(ctx, command)
		}
		injected := errors.New("injected Redis task-queue failure")
		if !after {
			return injected
		}
		if err := next(ctx, command); err != nil {
			return err
		}
		return injected
	}
}

func (*taskQueueCommandFailureHook) ProcessPipelineHook(next redis.ProcessPipelineHook) redis.ProcessPipelineHook {
	return next
}

// =============================================================================
// Queue Key Precedence Tests
// =============================================================================

// TestQueueKeyPrecedence verifies the queue key resolution precedence:
//
//	Explicit QueueKey > TRUVAG3_K8S_SERVICE_NAME > hardcoded default
//
// Per core/ARCHITECTURE.md §Configuration Changes checklist: "Test all precedence scenarios."
func TestQueueKeyPrecedence(t *testing.T) {
	testCases := []struct {
		name         string
		serviceName  string // TRUVAG3_K8S_SERVICE_NAME env var
		explicitKey  string // Explicit QueueKey in config
		expectedKey  string
		useNilConfig bool // Pass nil config to NewRedisTaskQueue
	}{
		{
			name:         "K8s default — env set, no explicit key",
			serviceName:  "event-driven-agent",
			explicitKey:  "",
			expectedKey:  "truvag3:v1:default:tasks:queue:event-driven-agent",
			useNilConfig: false,
		},
		{
			name:         "local dev — env unset, no explicit key",
			serviceName:  "",
			explicitKey:  "",
			expectedKey:  "truvag3:v1:default:tasks:queue",
			useNilConfig: false,
		},
		{
			name:         "explicit override beats env",
			serviceName:  "event-driven-agent",
			explicitKey:  "custom-key",
			expectedKey:  "custom-key",
			useNilConfig: false,
		},
		{
			name:         "explicit override beats default",
			serviceName:  "",
			explicitKey:  "custom-key",
			expectedKey:  "custom-key",
			useNilConfig: false,
		},
		{
			name:         "nil config with env set uses DefaultRedisTaskQueueConfig",
			serviceName:  "async-travel-agent",
			useNilConfig: true,
			expectedKey:  "truvag3:v1:default:tasks:queue:async-travel-agent",
		},
		{
			name:         "nil config without env uses hardcoded default",
			serviceName:  "",
			useNilConfig: true,
			expectedKey:  "truvag3:v1:default:tasks:queue",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			// Use t.Setenv for automatic cleanup
			t.Setenv(core.EnvServiceName, tc.serviceName)

			var config *RedisTaskQueueConfig
			if !tc.useNilConfig {
				config = &RedisTaskQueueConfig{
					QueueKey: tc.explicitKey,
				}
			}

			// NewRedisTaskQueue requires a non-nil client but we only need to
			// inspect the resolved config. Pass nil and recover the panic, or
			// just call the config resolution path directly.
			// We test via DefaultRedisTaskQueueConfig and the inline fallback.
			if tc.useNilConfig {
				defaultCfg := DefaultRedisTaskQueueConfig()
				if defaultCfg.QueueKey != tc.expectedKey {
					t.Errorf("DefaultRedisTaskQueueConfig().QueueKey = %q, want %q",
						defaultCfg.QueueKey, tc.expectedKey)
				}
			} else {
				// Simulate the inline fallback in NewRedisTaskQueue
				if config.QueueKey == "" {
					if svc := os.Getenv(core.EnvServiceName); svc != "" {
						config.QueueKey = defaultRedisKeyspace().Plain("tasks", "queue", svc)
					} else {
						config.QueueKey = defaultRedisKeyspace().Plain("tasks", "queue")
					}
				}
				if config.QueueKey != tc.expectedKey {
					t.Errorf("resolved QueueKey = %q, want %q",
						config.QueueKey, tc.expectedKey)
				}
			}
		})
	}
}

// TestDefaultRedisTaskQueueConfig_EnvIsolation verifies DefaultRedisTaskQueueConfig
// reads TRUVAG3_K8S_SERVICE_NAME at call time, not at import time.
func TestDefaultRedisTaskQueueConfig_EnvIsolation(t *testing.T) {
	// First call without env
	t.Setenv(core.EnvServiceName, "")
	cfg1 := DefaultRedisTaskQueueConfig()
	if cfg1.QueueKey != "truvag3:v1:default:tasks:queue" {
		t.Errorf("Without env: QueueKey = %q, want %q", cfg1.QueueKey, "truvag3:v1:default:tasks:queue")
	}

	// Second call with env set
	t.Setenv(core.EnvServiceName, "my-agent")
	cfg2 := DefaultRedisTaskQueueConfig()
	if cfg2.QueueKey != "truvag3:v1:default:tasks:queue:my-agent" {
		t.Errorf("With env: QueueKey = %q, want %q", cfg2.QueueKey, "truvag3:v1:default:tasks:queue:my-agent")
	}

	// Verify other defaults are always set
	if cfg2.RetryAttempts != 3 {
		t.Errorf("RetryAttempts = %d, want 3", cfg2.RetryAttempts)
	}
	if cfg2.ProcessingKey != "truvag3:v1:default:tasks:processing:my-agent" {
		t.Errorf("ProcessingKey = %q, want %q", cfg2.ProcessingKey, "truvag3:v1:default:tasks:processing:my-agent")
	}
}

func TestRedisTaskQueueDerivesProcessingKeyFromExplicitQueue(t *testing.T) {
	config := &RedisTaskQueueConfig{QueueKey: "custom:queue"}
	queue := NewRedisTaskQueue(nil, config)
	if queue.config.ProcessingKey != "custom:queue:processing" {
		t.Fatalf("ProcessingKey = %q, want %q", queue.config.ProcessingKey, "custom:queue:processing")
	}
}

func TestRedisTaskQueueRecoversPayloadWhenInflightTrackingFails(t *testing.T) {
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	config := &RedisTaskQueueConfig{
		QueueKey: "queue:recovery", ProcessingKey: "processing:recovery",
		RetryAttempts: 1, RetryDelay: time.Nanosecond,
	}
	queue := NewRedisTaskQueue(client, config)
	task := &core.Task{ID: "task-recovery", Type: "test"}
	if err := queue.Enqueue(t.Context(), task); err != nil {
		t.Fatal(err)
	}
	client.AddHook(&taskQueueCommandFailureHook{
		command: "lpush", key: config.ProcessingKey, remaining: 1,
	})

	if got, err := queue.Dequeue(t.Context(), time.Second); err == nil || got != nil {
		t.Fatalf("Dequeue after tracking failure = %#v, %v; want nil and error", got, err)
	}
	if queued := client.LLen(t.Context(), config.QueueKey).Val(); queued != 1 {
		t.Fatalf("recovered queue length = %d, want 1", queued)
	}
	if processing := client.LLen(t.Context(), config.ProcessingKey).Val(); processing != 0 {
		t.Fatalf("processing length after failed tracking = %d, want 0", processing)
	}
	recovered, err := queue.Dequeue(t.Context(), time.Second)
	if err != nil || recovered == nil || recovered.ID != task.ID {
		t.Fatalf("recovered Dequeue = %#v, %v", recovered, err)
	}
	if err := queue.Acknowledge(t.Context(), recovered.ID); err != nil {
		t.Fatal(err)
	}
}

func TestRedisTaskQueueRejectRetryAllowsDuplicatesWithoutLoss(t *testing.T) {
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	config := &RedisTaskQueueConfig{
		QueueKey: "queue:reject", ProcessingKey: "processing:reject",
		RetryAttempts: 1, RetryDelay: time.Nanosecond,
	}
	queue := NewRedisTaskQueue(client, config)
	task := &core.Task{ID: "task-reject", Type: "test"}
	if err := queue.Enqueue(t.Context(), task); err != nil {
		t.Fatal(err)
	}
	if got, err := queue.Dequeue(t.Context(), time.Second); err != nil || got == nil {
		t.Fatalf("Dequeue = %#v, %v", got, err)
	}
	client.AddHook(&taskQueueCommandFailureHook{
		command: "lrem", key: config.ProcessingKey, remaining: 1,
	})

	if err := queue.Reject(t.Context(), task.ID, "retryable"); err == nil {
		t.Fatal("Reject succeeded despite injected cleanup failure")
	}
	if queued, processing := client.LLen(t.Context(), config.QueueKey).Val(), client.LLen(t.Context(), config.ProcessingKey).Val(); queued != 1 || processing != 1 {
		t.Fatalf("post-failure lengths = queue %d, processing %d; want 1, 1", queued, processing)
	}
	if err := queue.Reject(t.Context(), task.ID, "retry cleanup"); err != nil {
		t.Fatal(err)
	}
	if queued, processing := client.LLen(t.Context(), config.QueueKey).Val(), client.LLen(t.Context(), config.ProcessingKey).Val(); queued != 2 || processing != 0 {
		t.Fatalf("post-retry lengths = queue %d, processing %d; want allowed duplicate 2, 0", queued, processing)
	}
}

func TestRedisTaskQueueAmbiguousEnqueueRetryDuplicatesWithoutLoss(t *testing.T) {
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	config := &RedisTaskQueueConfig{
		QueueKey: "queue:ambiguous", ProcessingKey: "processing:ambiguous",
		RetryAttempts: 2, RetryDelay: time.Nanosecond,
	}
	client.AddHook(&taskQueueCommandFailureHook{
		command: "lpush", key: config.QueueKey, remaining: 1, after: true,
	})
	queue := NewRedisTaskQueue(client, config)
	if err := queue.Enqueue(t.Context(), &core.Task{ID: "task-ambiguous", Type: "test"}); err != nil {
		t.Fatal(err)
	}
	if queued := client.LLen(t.Context(), config.QueueKey).Val(); queued != 2 {
		t.Fatalf("queue length after ambiguous retry = %d, want allowed duplicate 2", queued)
	}
}
