package orchestration

import (
	"testing"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
	"github.com/truvaagents/truva-g3/core"
)

func mustRedisTestKeyspace(t *testing.T, deployment string) core.RedisKeyspace {
	t.Helper()
	keyspace, err := core.NewRedisKeyspace(deployment)
	require.NoError(t, err)
	return keyspace
}

func TestRedisExecutionStoreRequiresTypedKeyspaceForCustomNamespace(t *testing.T) {
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	config := DefaultExecutionStoreConfig()
	config.KeyPrefix = "old:raw:prefix"
	_, err := NewRedisExecutionDebugStoreWithClient(client, config)
	require.ErrorIs(t, err, core.ErrInvalidConfiguration)
	keyspace := mustRedisTestKeyspace(t, "isolated")
	store, err := NewRedisExecutionDebugStoreWithClient(client, config, WithExecutionDebugKeyspace(keyspace))
	require.NoError(t, err)
	require.Equal(t, keyspace.Tagged("execution-debug", "request", "record"), store.recordKey("request"))
	require.NoError(t, store.Close())
	require.NoError(t, client.Ping(t.Context()).Err(), "borrowed client must remain open")
}

func TestDirectRedisConstructorsRejectRemovedSettings(t *testing.T) {
	constructors := []struct {
		name     string
		settings []string
		create   func() error
	}{
		{"execution", []string{"TRUVAG3_EXECUTION_DEBUG_REDIS_DB", "TRUVAG3_EXECUTION_DEBUG_KEY_PREFIX"}, func() error {
			store, err := NewRedisExecutionDebugStore()
			if store != nil {
				_ = store.Close()
			}
			return err
		}},
		{"llm", []string{"TRUVAG3_LLM_DEBUG_REDIS_DB", "TRUVAG3_LLM_DEBUG_KEY_PREFIX"}, func() error {
			store, err := NewRedisLLMDebugStore()
			if store != nil {
				_ = store.Close()
			}
			return err
		}},
		{"checkpoint", []string{"TRUVAG3_HITL_REDIS_DB", "TRUVAG3_HITL_KEY_PREFIX"}, func() error {
			store, err := NewRedisCheckpointStore()
			if store != nil {
				_ = store.Close()
			}
			return err
		}},
		{"command", []string{"TRUVAG3_HITL_REDIS_DB", "TRUVAG3_HITL_KEY_PREFIX"}, func() error {
			store, err := NewRedisCommandStore()
			if store != nil {
				_ = store.Close()
			}
			return err
		}},
	}
	for _, constructor := range constructors {
		for _, name := range constructor.settings {
			t.Run(constructor.name+"/"+name, func(t *testing.T) {
				// No service is needed: reject obsolete configuration before dialing.
				t.Setenv(name, "unit-test-secret")
				err := constructor.create()
				require.ErrorIs(t, err, core.ErrInvalidConfiguration)
				require.NotContains(t, err.Error(), "unit-test-secret")
			})
		}
	}
}
