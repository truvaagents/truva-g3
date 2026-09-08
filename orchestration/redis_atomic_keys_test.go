package orchestration

import (
	"strings"
	"testing"

	"github.com/truvaagents/truva-g3/core"
)

func TestRedisAtomicGroupsShareHashSlots(t *testing.T) {
	keyspace, err := core.NewRedisKeyspace("prod")
	if err != nil {
		t.Fatal(err)
	}

	t.Run("execution retention", func(t *testing.T) {
		keys := NewRedisExecutionDebugKeys(keyspace)
		requireOrchestrationSameRedisHashTag(t, keys.Record("request-1"), keys.RetentionLink("request-1"))
	})
	t.Run("workflow", func(t *testing.T) {
		store := &RedisStateStore{keyspace: keyspace}
		requireOrchestrationSameRedisHashTag(t,
			store.executionKey("workflow-1", "execution-1"), store.indexKey("workflow-1"),
		)
	})
	t.Run("HITL", func(t *testing.T) {
		keys := newHITLKeys(keyspace.Tagged("hitl", "travel-agent"))
		requireOrchestrationSameRedisHashTag(t,
			keys.checkpoint("checkpoint-1"), keys.pending(), keys.request("request-1"),
			keys.claim("checkpoint-1"), keys.command("checkpoint-1"),
		)
	})
	t.Run("schedules", func(t *testing.T) {
		store := &RedisScheduleStore{keyspace: keyspace}
		requireOrchestrationSameRedisHashTag(t, store.dataKey("schedule-1"), store.allKey(), store.dueKey())
	})
}

func requireOrchestrationSameRedisHashTag(t *testing.T, keys ...string) {
	t.Helper()
	want := orchestrationRedisHashTag(keys[0])
	if want == "" {
		t.Fatalf("key %q has no Redis hash tag", keys[0])
	}
	for _, key := range keys[1:] {
		if got := orchestrationRedisHashTag(key); got != want {
			t.Fatalf("key %q has hash tag %q, want %q", key, got, want)
		}
	}
}

func orchestrationRedisHashTag(key string) string {
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
