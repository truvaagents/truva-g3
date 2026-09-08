package telemetry

import "github.com/truvaagents/truva-g3/core"

// RedisLLMDebugKeys owns the shared Redis schema used by telemetry writers and
// orchestration readers. Request-local keys share one cluster slot; the recent
// index is a repairable deployment-wide projection.
type RedisLLMDebugKeys struct {
	keyspace core.RedisKeyspace
}

func NewRedisLLMDebugKeys(keyspace core.RedisKeyspace) RedisLLMDebugKeys {
	return RedisLLMDebugKeys{keyspace: keyspace}
}

func (keys RedisLLMDebugKeys) Meta(requestID string) string {
	return keys.keyspace.Tagged("llm-debug", requestID, "meta")
}

func (keys RedisLLMDebugKeys) Interactions(requestID string) string {
	return keys.keyspace.Tagged("llm-debug", requestID, "interactions")
}

func (keys RedisLLMDebugKeys) RetentionFloor(requestID string) string {
	return keys.keyspace.Tagged("llm-debug", requestID, "retention-floor")
}

func (keys RedisLLMDebugKeys) RecentIndex() string {
	return keys.keyspace.Plain("llm-debug", "index", "recent")
}
