package core

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestRedisKeyspaceBuildsVersionedPlainAndTaggedKeys(t *testing.T) {
	keyspace, err := NewRedisKeyspace("prod")
	require.NoError(t, err)

	require.Equal(t, "truvag3:v1:prod:workflow:maintenance:cursor", keyspace.Plain("workflow", "maintenance", "cursor"))
	require.Equal(
		t,
		"truvag3:v1:prod:workflow:{prod:workflow:wf-1}:execution:exec-1",
		keyspace.Tagged("workflow", "wf-1", "execution", "exec-1"),
	)
}

func TestRedisKeyspaceEncodesUnsafeHashTagScope(t *testing.T) {
	keyspace, err := NewRedisKeyspace("prod")
	require.NoError(t, err)

	first := keyspace.Tagged("workflow", "name}:{registry:prod}", "execution", "one")
	second := keyspace.Tagged("workflow", "name}:{registry:prod}", "executions")
	plain := keyspace.Plain("workflow", "maintenance", "cursor")
	tag := testRedisHashTag(first)

	require.Equal(t, "prod:workflow:~bmFtZX06e3JlZ2lzdHJ5OnByb2R9", tag)
	require.Equal(t, tag, testRedisHashTag(second))
	require.NotEmpty(t, tag)
	require.NotContains(t, tag, "}")
	require.NotContains(t, first, "{registry:prod}")
	require.True(t, strings.HasPrefix(first, "truvag3:v1:prod:workflow:"))
	require.True(t, strings.HasPrefix(plain, "truvag3:v1:prod:workflow:"))
}

func TestRedisKeyspaceEncodesUnsafeSubsystem(t *testing.T) {
	keyspace, err := NewRedisKeyspace("prod")
	require.NoError(t, err)

	unsafeSubsystem := "workflow}:{forced-slot}"
	tagged := keyspace.Tagged(unsafeSubsystem, "workflow-1", "execution")
	plain := keyspace.Plain(unsafeSubsystem, "index")
	encoded := redisKeyspaceSegment(unsafeSubsystem)

	require.True(t, strings.HasPrefix(tagged, "truvag3:v1:prod:"+encoded+":"))
	require.True(t, strings.HasPrefix(plain, "truvag3:v1:prod:"+encoded+":"))
	require.Equal(t, "prod:"+encoded+":workflow-1", testRedisHashTag(tagged))
	require.NotContains(t, tagged, "{forced-slot}")
}

func TestRedisKeyspaceValidatesDeployment(t *testing.T) {
	for _, invalid := range []string{"bad namespace", "{prod}", strings.Repeat("a", 129)} {
		_, err := NewRedisKeyspace(invalid)
		require.ErrorIs(t, err, ErrInvalidConfiguration)
	}
	keyspace, err := NewRedisKeyspace(" ")
	require.NoError(t, err)
	require.Equal(t, "default", keyspace.Deployment())
	require.Equal(t, "default", (RedisKeyspace{}).Deployment())
}

func testRedisHashTag(key string) string {
	open := strings.IndexByte(key, '{')
	if open < 0 {
		return ""
	}
	tail := key[open+1:]
	close := strings.IndexByte(tail, '}')
	if close <= 0 {
		return ""
	}
	return tail[:close]
}
