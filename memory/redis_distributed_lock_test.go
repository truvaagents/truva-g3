package memory

import (
	"context"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/go-redis/redis/v8"
	"github.com/truvaagents/truva-g3/core"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --- Constructor tests ---

func TestNewRedisDistributedLock_Success(t *testing.T) {
	client := newTestRedisClient(t)
	lock, err := NewRedisDistributedLock(client, &core.NoOpLogger{})
	require.NoError(t, err)
	require.NotNil(t, lock)
	assert.NotNil(t, lock.client)
	assert.NotNil(t, lock.logger)
}

func TestNewRedisDistributedLock_NilClient_Error(t *testing.T) {
	lock, err := NewRedisDistributedLock(nil, &core.NoOpLogger{})
	require.Error(t, err)
	assert.Nil(t, lock)
	assert.Contains(t, err.Error(), "redis client is required")
}

func TestNewRedisDistributedLock_NilLogger_DefaultsToNoOp(t *testing.T) {
	client := newTestRedisClient(t)
	lock, err := NewRedisDistributedLock(client, nil)
	require.NoError(t, err)
	require.NotNil(t, lock)
	assert.NotNil(t, lock.logger, "nil logger should be replaced with NoOp")
}

// --- Acquire tests ---

func TestAcquire_Success(t *testing.T) {
	client := newTestRedisClient(t)
	lock, _ := NewRedisDistributedLock(client, &core.NoOpLogger{})

	acquired, err := lock.Acquire(context.Background(), "test-key", 5*time.Second)
	require.NoError(t, err)
	assert.True(t, acquired)
}

func TestAcquire_HeldByAnother(t *testing.T) {
	client := newTestRedisClient(t)
	lock, _ := NewRedisDistributedLock(client, &core.NoOpLogger{})
	ctx := context.Background()

	// First acquire succeeds
	acquired1, err := lock.Acquire(ctx, "shared-key", 5*time.Second)
	require.NoError(t, err)
	require.True(t, acquired1)

	// Second acquire on the same key should fail (held)
	acquired2, err := lock.Acquire(ctx, "shared-key", 5*time.Second)
	require.NoError(t, err, "Acquire on held lock should NOT return an error")
	assert.False(t, acquired2, "second acquire should return false")
}

func TestAcquire_DifferentKeys_BothSucceed(t *testing.T) {
	client := newTestRedisClient(t)
	lock, _ := NewRedisDistributedLock(client, &core.NoOpLogger{})
	ctx := context.Background()

	a1, err := lock.Acquire(ctx, "key-a", 5*time.Second)
	require.NoError(t, err)
	assert.True(t, a1)

	a2, err := lock.Acquire(ctx, "key-b", 5*time.Second)
	require.NoError(t, err)
	assert.True(t, a2)
}

func TestAcquire_AfterTTL_CanAcquireAgain(t *testing.T) {
	// Use miniredis directly to control time (FastForward — miniredis doesn't expire on wall clock)
	mr := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	lock, _ := NewRedisDistributedLock(client, &core.NoOpLogger{})
	ctx := context.Background()

	// Acquire with TTL
	a1, err := lock.Acquire(ctx, "ttl-key", 1*time.Second)
	require.NoError(t, err)
	require.True(t, a1)

	// Fast-forward miniredis past the TTL
	mr.FastForward(2 * time.Second)

	// Should be able to acquire again
	a2, err := lock.Acquire(ctx, "ttl-key", 5*time.Second)
	require.NoError(t, err)
	assert.True(t, a2, "lock should be acquirable after TTL expiry")
}

// --- Release tests ---

func TestRelease_Success(t *testing.T) {
	client := newTestRedisClient(t)
	lock, _ := NewRedisDistributedLock(client, &core.NoOpLogger{})
	ctx := context.Background()

	_, err := lock.Acquire(ctx, "release-key", 5*time.Second)
	require.NoError(t, err)

	err = lock.Release(ctx, "release-key")
	assert.NoError(t, err)

	// After release, should be acquirable again
	a, err := lock.Acquire(ctx, "release-key", 5*time.Second)
	require.NoError(t, err)
	assert.True(t, a)
}

func TestRelease_NotHeld_NoError(t *testing.T) {
	client := newTestRedisClient(t)
	lock, _ := NewRedisDistributedLock(client, &core.NoOpLogger{})

	// Release without ever acquiring — should not return an error
	err := lock.Release(context.Background(), "never-held")
	assert.NoError(t, err, "Release on non-existent lock should be a no-op")
}

// --- Key format ---

func TestLockKey_FormatPrefix(t *testing.T) {
	client := newTestRedisClient(t)
	lock, _ := NewRedisDistributedLock(client, &core.NoOpLogger{})
	ctx := context.Background()

	// Acquire and verify the underlying Redis key uses the truvag3:lock: prefix
	_, err := lock.Acquire(ctx, "myjob:domain1", 5*time.Second)
	require.NoError(t, err)

	// Read the key directly via the client
	val, err := client.Get(ctx, "truvag3:lock:myjob:domain1").Result()
	require.NoError(t, err, "key should exist with truvag3:lock: prefix")
	assert.NotEmpty(t, val)
}

// --- Error path tests (closed Redis client) ---

func TestAcquire_RedisError_ReturnsError(t *testing.T) {
	// Create a Redis client pointing at a closed server
	mr := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	mr.Close() // close the server so subsequent ops fail

	lock, _ := NewRedisDistributedLock(client, &core.NoOpLogger{})

	acquired, err := lock.Acquire(context.Background(), "any-key", 5*time.Second)
	require.Error(t, err, "Acquire should return error when Redis is unreachable")
	assert.False(t, acquired)
}

func TestRelease_RedisError_ReturnsError(t *testing.T) {
	mr := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	mr.Close()

	lock, _ := NewRedisDistributedLock(client, &core.NoOpLogger{})

	err := lock.Release(context.Background(), "any-key")
	assert.Error(t, err, "Release should return error when Redis is unreachable")
}

// --- ComponentAwareLogger wrapping ---

type fakeComponentAwareLogger struct {
	*core.NoOpLogger
	wrappedComponent string
}

func (f *fakeComponentAwareLogger) WithComponent(component string) core.Logger {
	return &fakeComponentAwareLogger{wrappedComponent: component}
}

func TestNewRedisDistributedLock_WrapsComponentAwareLogger(t *testing.T) {
	client := newTestRedisClient(t)
	componentLogger := &fakeComponentAwareLogger{}

	lock, err := NewRedisDistributedLock(client, componentLogger)
	require.NoError(t, err)

	wrapped, ok := lock.logger.(*fakeComponentAwareLogger)
	require.True(t, ok, "logger should be wrapped via ComponentAwareLogger")
	assert.Equal(t, "framework/memory", wrapped.wrappedComponent)
}

// --- Compile-time interface check ---

func TestRedisDistributedLock_ImplementsInterface(t *testing.T) {
	var _ core.DistributedLock = (*RedisDistributedLock)(nil)
}
