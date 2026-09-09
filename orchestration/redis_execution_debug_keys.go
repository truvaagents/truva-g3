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
	keyspace core.RedisKeyspace
}

func NewRedisExecutionDebugKeys(keyspace core.RedisKeyspace) RedisExecutionDebugKeys {
	return RedisExecutionDebugKeys{keyspace: keyspace}
}

func (keys RedisExecutionDebugKeys) Record(requestID string) string {
	return keys.keyspace.Tagged("execution-debug", requestID, "record")
}

func (keys RedisExecutionDebugKeys) RetentionLink(requestID string) string {
	return keys.keyspace.Tagged("execution-debug", requestID, "retention")
}

func (keys RedisExecutionDebugKeys) RecentIndex() string {
	return keys.keyspace.Plain("execution-debug", "index", "recent")
}

func (keys RedisExecutionDebugKeys) Trace(traceID string) string {
	return keys.keyspace.Plain("execution-debug", "index", "trace", traceID)
}

func (keys RedisExecutionDebugKeys) Conversation(conversationID string) string {
	digest := sha256.Sum256([]byte(conversationID))
	encoded := fmt.Sprintf("%x", digest)
	return keys.keyspace.Plain("execution-debug", "index", "conversation", encoded)
}

func (keys RedisExecutionDebugKeys) PrefixForDiagnostics() string {
	return keys.keyspace.Plain("execution-debug")
}
