package telemetry

import (
	"strings"
	"testing"

	"github.com/truvaagents/truva-g3/core"
)

func TestLLMDebugAuthoritativeKeysShareRequestSlot(t *testing.T) {
	keyspace, err := core.NewRedisKeyspace("prod")
	if err != nil {
		t.Fatal(err)
	}
	keys := NewRedisLLMDebugKeys(keyspace)
	want := telemetryRedisHashTag(keys.Meta("request-1"))
	if want == "" {
		t.Fatal("LLM debug metadata key has no Redis hash tag")
	}
	for _, key := range []string{keys.Interactions("request-1"), keys.RetentionFloor("request-1")} {
		if got := telemetryRedisHashTag(key); got != want {
			t.Fatalf("key %q has hash tag %q, want %q", key, got, want)
		}
	}
	if got := telemetryRedisHashTag(keys.RecentIndex()); got != "" {
		t.Fatalf("repairable recent index unexpectedly pins hash tag %q", got)
	}
}

func telemetryRedisHashTag(key string) string {
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
