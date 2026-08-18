package core

import (
	"context"
	"net"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

func TestApplyRedisClientDefaults(t *testing.T) {
	options := ApplyRedisClientDefaults(nil)

	require.Equal(t, DefaultRedisProtocol, options.Protocol)
	require.Equal(t, 5*time.Second, options.DialTimeout)
	require.Equal(t, 3*time.Second, options.ReadTimeout)
	require.Equal(t, 3*time.Second, options.WriteTimeout)
	require.Equal(t, 8*time.Millisecond, options.MinRetryBackoff)
	require.Equal(t, 512*time.Millisecond, options.MaxRetryBackoff)
	require.Equal(t, 5*time.Minute, options.ConnMaxIdleTime)
	require.Zero(t, options.ReadBufferSize)
	require.Zero(t, options.WriteBufferSize)
	require.Equal(t, 1, options.DialerRetries)
	require.Nil(t, options.Dialer)
}

func TestApplyRedisClientDefaultsPreservesExplicitConfiguration(t *testing.T) {
	dialer := func(context.Context, string, string) (net.Conn, error) { return nil, nil }
	options := &redis.Options{
		Protocol:           3,
		DialTimeout:        time.Second,
		ReadTimeout:        -1,
		WriteTimeout:       2 * time.Second,
		MinRetryBackoff:    -1,
		MaxRetryBackoff:    2 * time.Second,
		ConnMaxIdleTime:    -1,
		ReadBufferSize:     8192,
		WriteBufferSize:    16384,
		DialerRetries:      4,
		DialerRetryTimeout: 50 * time.Millisecond,
		Dialer:             dialer,
	}

	result := ApplyRedisClientDefaults(options)

	require.NotSame(t, options, result)
	require.Equal(t, 3, result.Protocol)
	require.Equal(t, time.Second, result.DialTimeout)
	require.Equal(t, time.Duration(-1), result.ReadTimeout)
	require.Equal(t, 2*time.Second, result.WriteTimeout)
	require.Equal(t, time.Duration(-1), result.MinRetryBackoff)
	require.Equal(t, 2*time.Second, result.MaxRetryBackoff)
	require.Equal(t, time.Duration(-1), result.ConnMaxIdleTime)
	require.Equal(t, 8192, result.ReadBufferSize)
	require.Equal(t, 16384, result.WriteBufferSize)
	require.Equal(t, 4, result.DialerRetries)
	require.Equal(t, 50*time.Millisecond, result.DialerRetryTimeout)
	require.NotNil(t, result.Dialer)
}

func TestApplyRedisClientDefaultsDoesNotMutateCaller(t *testing.T) {
	input := &redis.Options{Addr: "redis:6379"}

	result := ApplyRedisClientDefaults(input)

	require.NotSame(t, input, result)
	require.Equal(t, "redis:6379", result.Addr)
	require.Zero(t, input.Protocol)
	require.Zero(t, input.DialTimeout)
	require.Zero(t, input.ReadTimeout)
	require.Zero(t, input.WriteTimeout)
	require.Zero(t, input.DialerRetries)
	require.Nil(t, input.Dialer)
}

func TestApplyRedisClientDefaultsDelegatesTransportDefaultsToGoRedis(t *testing.T) {
	client := redis.NewClient(ApplyRedisClientDefaults(nil))
	t.Cleanup(func() {
		require.NoError(t, client.Close())
	})

	effective := client.Options()
	require.Equal(t, DefaultRedisProtocol, effective.Protocol)
	require.Equal(t, 32*1024, effective.ReadBufferSize)
	require.Equal(t, 32*1024, effective.WriteBufferSize)
	require.NotNil(t, effective.Dialer)
}

func TestApplyRedisClientDefaultsMakesWriteTimeoutFollowExplicitReadTimeout(t *testing.T) {
	options := ApplyRedisClientDefaults(&redis.Options{ReadTimeout: 7 * time.Second})

	require.Equal(t, 7*time.Second, options.ReadTimeout)
	require.Equal(t, 7*time.Second, options.WriteTimeout)
}
