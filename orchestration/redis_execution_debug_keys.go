package orchestration

import (
	"crypto/sha256"
	"fmt"

	"github.com/truvaagents/truva-g3/core"
)

// RedisExecutionDebugKeys owns the Redis key schema for execution-debug data.
// Request-local record and retention keys share a slot; repairable projections
// remain deployment-wide plain keys.
type RedisExecutionDebugKeys struct {
	keyspace     core.RedisKeyspace
	legacyPrefix string
}

func NewRedisExecutionDebugKeys(keyspace core.RedisKeyspace) RedisExecutionDebugKeys {
	return RedisExecutionDebugKeys{keyspace: keyspace}
}

func legacyRedisExecutionDebugKeys(prefix string) RedisExecutionDebugKeys {
	return RedisExecutionDebugKeys{legacyPrefix: normalizeExecutionKeyPrefix(prefix)}
}

func (keys RedisExecutionDebugKeys) Record(requestID string) string {
	if keys.legacyPrefix != "" {
		return keys.legacyPrefix + requestID
	}
	return keys.keyspace.Tagged("execution-debug", requestID, "record")
}

func (keys RedisExecutionDebugKeys) RetentionLink(requestID string) string {
	if keys.legacyPrefix != "" {
		return executionRetentionLinkKey(keys.legacyPrefix, requestID)
	}
	return keys.keyspace.Tagged("execution-debug", requestID, "retention")
}

func (keys RedisExecutionDebugKeys) RecentIndex() string {
	if keys.legacyPrefix != "" {
		return keys.legacyPrefix + "index"
	}
	return keys.keyspace.Plain("execution-debug", "index", "recent")
}

func (keys RedisExecutionDebugKeys) Trace(traceID string) string {
	if keys.legacyPrefix != "" {
		return keys.legacyPrefix + "trace:" + traceID
	}
	return keys.keyspace.Plain("execution-debug", "index", "trace", traceID)
}

func (keys RedisExecutionDebugKeys) Conversation(conversationID string) string {
	digest := sha256.Sum256([]byte(conversationID))
	encoded := fmt.Sprintf("%x", digest)
	if keys.legacyPrefix != "" {
		return keys.legacyPrefix + "conversation:" + encoded
	}
	return keys.keyspace.Plain("execution-debug", "index", "conversation", encoded)
}

func (keys RedisExecutionDebugKeys) PrefixForDiagnostics() string {
	if keys.legacyPrefix != "" {
		return keys.legacyPrefix
	}
	return keys.keyspace.Plain("execution-debug")
}
