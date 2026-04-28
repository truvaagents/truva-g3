package memory

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestInMemoryDigestCache_SetAndGet(t *testing.T) {
	cache, err := NewInMemoryDigestCache()
	require.NoError(t, err)
	ctx := context.Background()

	require.NoError(t, cache.SetDigest(ctx, "infra", []byte(`{"content":"digest"}`), 5*time.Minute))

	data, err := cache.GetDigest(ctx, "infra")
	require.NoError(t, err)
	assert.Equal(t, `{"content":"digest"}`, string(data))
}

func TestInMemoryDigestCache_GetMiss(t *testing.T) {
	cache, _ := NewInMemoryDigestCache()
	ctx := context.Background()

	data, err := cache.GetDigest(ctx, "infra")
	assert.NoError(t, err)
	assert.Nil(t, data)
}

func TestInMemoryDigestCache_TTLExpiry(t *testing.T) {
	cache, _ := NewInMemoryDigestCache()
	ctx := context.Background()

	require.NoError(t, cache.SetDigest(ctx, "infra", []byte("digest"), 1*time.Millisecond))
	time.Sleep(5 * time.Millisecond)

	data, err := cache.GetDigest(ctx, "infra")
	assert.NoError(t, err)
	assert.Nil(t, data, "should return nil after TTL expiry")
}

func TestInMemoryDigestCache_DomainIsolation(t *testing.T) {
	cache, _ := NewInMemoryDigestCache()
	ctx := context.Background()

	require.NoError(t, cache.SetDigest(ctx, "infra", []byte("infra-digest"), 5*time.Minute))
	require.NoError(t, cache.SetDigest(ctx, "commerce", []byte("commerce-digest"), 5*time.Minute))

	d1, _ := cache.GetDigest(ctx, "infra")
	d2, _ := cache.GetDigest(ctx, "commerce")
	d3, _ := cache.GetDigest(ctx, "security")

	assert.Equal(t, "infra-digest", string(d1))
	assert.Equal(t, "commerce-digest", string(d2))
	assert.Nil(t, d3)
}

func TestInMemoryDigestCache_Overwrite(t *testing.T) {
	cache, _ := NewInMemoryDigestCache()
	ctx := context.Background()

	require.NoError(t, cache.SetDigest(ctx, "infra", []byte("old"), 5*time.Minute))
	require.NoError(t, cache.SetDigest(ctx, "infra", []byte("new"), 5*time.Minute))

	data, _ := cache.GetDigest(ctx, "infra")
	assert.Equal(t, "new", string(data))
}

func TestInMemoryDigestCache_TTLRefreshOnOverwrite(t *testing.T) {
	cache, _ := NewInMemoryDigestCache()
	ctx := context.Background()

	require.NoError(t, cache.SetDigest(ctx, "infra", []byte("v1"), 50*time.Millisecond))
	time.Sleep(30 * time.Millisecond)

	// Overwrite before expiry — should reset TTL
	require.NoError(t, cache.SetDigest(ctx, "infra", []byte("v2"), 50*time.Millisecond))
	time.Sleep(30 * time.Millisecond)

	// v1's TTL would have expired, but v2's hasn't
	data, _ := cache.GetDigest(ctx, "infra")
	assert.Equal(t, "v2", string(data))
}
