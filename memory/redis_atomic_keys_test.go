package memory

import (
	"strings"
	"testing"

	"github.com/truvaagents/truva-g3/core"
)

func TestMemoryAtomicGroupsShareHashSlots(t *testing.T) {
	keyspace, err := core.NewRedisKeyspace("prod")
	if err != nil {
		t.Fatal(err)
	}

	activity := &RedisActivityCoordinator{domain: "infrastructure", keyspace: keyspace}
	requireMemorySameRedisHashTag(t,
		activity.signalKey("request-1"), activity.signalIndexKey("infrastructure"),
	)

	investigation := &AtomicLockCoordinator{domain: "infrastructure", keyspace: keyspace}
	requireMemorySameRedisHashTag(t,
		investigation.investigationKey("host-1"), investigation.investigationIndexKey(),
	)
}

func requireMemorySameRedisHashTag(t *testing.T, keys ...string) {
	t.Helper()
	want := memoryRedisHashTag(keys[0])
	if want == "" {
		t.Fatalf("key %q has no Redis hash tag", keys[0])
	}
	for _, key := range keys[1:] {
		if got := memoryRedisHashTag(key); got != want {
			t.Fatalf("key %q has hash tag %q, want %q", key, got, want)
		}
	}
}

func memoryRedisHashTag(key string) string {
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
