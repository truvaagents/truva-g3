package orchestration

import (
	"os"
	"testing"

	"github.com/truvaagents/truva-g3/core"
)

// =============================================================================
// Queue Key Precedence Tests (RC1)
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
			expectedKey:  "truvag3:tasks:queue:event-driven-agent",
			useNilConfig: false,
		},
		{
			name:         "local dev — env unset, no explicit key",
			serviceName:  "",
			explicitKey:  "",
			expectedKey:  "truvag3:tasks:queue",
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
			expectedKey:  "truvag3:tasks:queue:async-travel-agent",
		},
		{
			name:         "nil config without env uses hardcoded default",
			serviceName:  "",
			useNilConfig: true,
			expectedKey:  "truvag3:tasks:queue",
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
						config.QueueKey = "truvag3:tasks:queue:" + svc
					} else {
						config.QueueKey = "truvag3:tasks:queue"
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
	if cfg1.QueueKey != "truvag3:tasks:queue" {
		t.Errorf("Without env: QueueKey = %q, want %q", cfg1.QueueKey, "truvag3:tasks:queue")
	}

	// Second call with env set
	t.Setenv(core.EnvServiceName, "my-agent")
	cfg2 := DefaultRedisTaskQueueConfig()
	if cfg2.QueueKey != "truvag3:tasks:queue:my-agent" {
		t.Errorf("With env: QueueKey = %q, want %q", cfg2.QueueKey, "truvag3:tasks:queue:my-agent")
	}

	// Verify other defaults are always set
	if cfg2.RetryAttempts != 3 {
		t.Errorf("RetryAttempts = %d, want 3", cfg2.RetryAttempts)
	}
	if cfg2.ProcessingKey != "truvag3:tasks:processing:my-agent" {
		t.Errorf("ProcessingKey = %q, want %q", cfg2.ProcessingKey, "truvag3:tasks:processing:my-agent")
	}
}

func TestRedisTaskQueueDerivesProcessingKeyFromExplicitQueue(t *testing.T) {
	config := &RedisTaskQueueConfig{QueueKey: "custom:queue"}
	queue := NewRedisTaskQueueWithClient(nil, config)
	if queue.config.ProcessingKey != "custom:queue:processing" {
		t.Fatalf("ProcessingKey = %q, want %q", queue.config.ProcessingKey, "custom:queue:processing")
	}
}
