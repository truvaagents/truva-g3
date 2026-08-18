package memory

import (
	"os"
	"testing"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/truvaagents/truva-g3/core"
)

func newTestRedisClient(t *testing.T) *redis.Client {
	t.Helper()
	mr := miniredis.RunT(t)
	return redis.NewClient(&redis.Options{Addr: mr.Addr()})
}

func TestNewSharedBackends_Default(t *testing.T) {
	client := newTestRedisClient(t)
	sb, err := NewSharedBackends(client, &core.NoOpLogger{})
	require.NoError(t, err)
	require.NotNil(t, sb)
	defer sb.Close()

	deps := sb.ToDeps()
	assert.NotNil(t, deps.Episodic, "episodic should always be created")
	assert.NotNil(t, deps.Coordinator, "coordinator should be created")
	assert.NotNil(t, deps.ActivityCoordinator, "activity coordinator should be created")
	assert.NotNil(t, deps.DigestCache, "digest cache should be created")
	assert.Equal(t, "default", deps.AgentDomain)
	// Phase 2 disabled — no embedding client
	assert.Nil(t, deps.Knowledge)
	assert.Nil(t, deps.Embedder)
}

func TestNewSharedBackends_NilClient(t *testing.T) {
	_, err := NewSharedBackends(nil, &core.NoOpLogger{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "redis client is required")
}

func TestNewSharedBackends_WithDomain(t *testing.T) {
	client := newTestRedisClient(t)
	sb, err := NewSharedBackends(client, &core.NoOpLogger{},
		WithDomain("infrastructure"),
	)
	require.NoError(t, err)
	defer sb.Close()

	assert.Equal(t, "infrastructure", sb.ToDeps().AgentDomain)
}

func TestNewSharedBackends_WithAgentName(t *testing.T) {
	client := newTestRedisClient(t)
	sb, err := NewSharedBackends(client, &core.NoOpLogger{},
		WithAgentName("devops-chat-agent"),
	)
	require.NoError(t, err)
	defer sb.Close()

	assert.Equal(t, "devops-chat-agent", sb.ToDeps().AgentName)
}

func TestNewSharedBackends_DomainFromEnvVar(t *testing.T) {
	os.Setenv("TRUVAG3_AGENT_DOMAIN", "from-env")
	defer os.Unsetenv("TRUVAG3_AGENT_DOMAIN")

	client := newTestRedisClient(t)
	sb, err := NewSharedBackends(client, &core.NoOpLogger{})
	require.NoError(t, err)
	defer sb.Close()

	assert.Equal(t, "from-env", sb.ToDeps().AgentDomain)
}

func TestNewSharedBackends_ExplicitOptionOverridesEnvVar(t *testing.T) {
	os.Setenv("TRUVAG3_AGENT_DOMAIN", "from-env")
	defer os.Unsetenv("TRUVAG3_AGENT_DOMAIN")

	client := newTestRedisClient(t)
	sb, err := NewSharedBackends(client, &core.NoOpLogger{},
		WithDomain("from-option"),
	)
	require.NoError(t, err)
	defer sb.Close()

	assert.Equal(t, "from-option", sb.ToDeps().AgentDomain, "explicit option should override env var")
}

func TestNewSharedBackends_AgentNameFromEnvVar(t *testing.T) {
	os.Setenv("TRUVAG3_AGENT_NAME", "env-agent")
	defer os.Unsetenv("TRUVAG3_AGENT_NAME")

	client := newTestRedisClient(t)
	sb, err := NewSharedBackends(client, &core.NoOpLogger{})
	require.NoError(t, err)
	defer sb.Close()

	assert.Equal(t, "env-agent", sb.ToDeps().AgentName)
}

func TestNewSharedBackends_WithKnowledgeDisabled(t *testing.T) {
	client := newTestRedisClient(t)
	sb, err := NewSharedBackends(client, &core.NoOpLogger{},
		WithKnowledgeDisabled(),
		WithEmbeddingClient(&core.NoOpEmbeddingClient{}),
	)
	require.NoError(t, err)
	defer sb.Close()

	deps := sb.ToDeps()
	assert.Nil(t, deps.Knowledge, "knowledge should be nil when disabled")
	assert.Nil(t, deps.Embedder, "embedder should be nil when knowledge disabled")
}

func TestNewSharedBackends_Phase2RequiresEmbedder(t *testing.T) {
	client := newTestRedisClient(t)
	// No WithEmbeddingClient — Phase 2 should be disabled
	sb, err := NewSharedBackends(client, &core.NoOpLogger{})
	require.NoError(t, err)
	defer sb.Close()

	deps := sb.ToDeps()
	assert.Nil(t, deps.Knowledge)
	assert.Nil(t, deps.Embedder)
}

func TestNewSharedBackends_InvalidOption(t *testing.T) {
	client := newTestRedisClient(t)
	_, err := NewSharedBackends(client, &core.NoOpLogger{},
		WithDomain(""), // empty domain should fail
	)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "domain cannot be empty")
}

func TestNewSharedBackends_ToDepsNilSafe(t *testing.T) {
	var sb *SharedBackends
	assert.Nil(t, sb.ToDeps())
}

func TestNewSharedBackends_CloseNilSafe(t *testing.T) {
	var sb *SharedBackends
	sb.Close() // should not panic
}

func TestNewSharedBackends_NilLogger(t *testing.T) {
	client := newTestRedisClient(t)
	sb, err := NewSharedBackends(client, nil) // nil logger should default to NoOp
	require.NoError(t, err)
	defer sb.Close()
	assert.NotNil(t, sb.ToDeps().Episodic)
}
