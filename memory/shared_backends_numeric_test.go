package memory

import (
	"context"
	"net"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
	"github.com/truvaagents/truva-g3/core"
)

func TestSharedBackendNumericConfiguration(t *testing.T) {
	for _, key := range []string{"TRUVAG3_SHARED_MEMORY_STREAM_MAXLEN", "TRUVAG3_SHARED_MEMORY_INVESTIGATION_TTL"} {
		for _, value := range []string{"", "private-invalid-input", "0", "-1", "99999999999999999999999999"} {
			t.Run(key+"/"+value, func(t *testing.T) {
				t.Setenv(key, value)
				client := redis.NewClient(&redis.Options{Dialer: func(context.Context, string, string) (net.Conn, error) {
					t.Error("invalid numeric configuration attempted network I/O")
					return nil, context.Canceled
				}})
				t.Cleanup(func() { _ = client.Close() })
				backends, err := NewSharedBackends(client, nil, WithKnowledgeDisabled())
				require.ErrorIs(t, err, core.ErrInvalidConfiguration)
				require.Contains(t, err.Error(), key)
				require.NotContains(t, err.Error(), "private-invalid-input")
				require.Nil(t, backends)
			})
		}
	}
	t.Run("effective values", func(t *testing.T) {
		t.Setenv("TRUVAG3_SHARED_MEMORY_STREAM_MAXLEN", "12345")
		t.Setenv("TRUVAG3_SHARED_MEMORY_INVESTIGATION_TTL", "1m2.5s")
		client := newTestRedisClient(t)
		t.Cleanup(func() { _ = client.Close() })
		backends, err := NewSharedBackends(client, nil, WithKnowledgeDisabled())
		require.NoError(t, err)
		t.Cleanup(backends.Close)
		require.Equal(t, int64(12345), backends.deps.Episodic.(*StreamEpisodicMemory).streamMaxLen)
		require.Equal(t, 62500*time.Millisecond, backends.deps.Coordinator.(*AtomicLockCoordinator).defaultTTL)
		// Lower-layer constructors remain environment-free.
		episodic, err := NewStreamEpisodicMemory(WithEpisodicRedisClient(client))
		require.NoError(t, err)
		require.Equal(t, int64(100000), episodic.streamMaxLen)
		coordinator, err := NewAtomicLockCoordinator(WithCoordinatorRedisClient(client))
		require.NoError(t, err)
		require.Equal(t, 30*time.Minute, coordinator.defaultTTL)
	})
}
