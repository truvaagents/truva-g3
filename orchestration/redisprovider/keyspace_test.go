package redisprovider

import (
	"strings"
	"testing"
)

func TestRedisHashTag(t *testing.T) {
	tests := []struct {
		name string
		key  string
		want string
	}{
		{name: "tagged", key: "truvag3:v1:prod:hitl:{travel-agent}:checkpoint:cp-1", want: "travel-agent"},
		{name: "first complete tag wins", key: "prefix:{first}:suffix:{second}", want: "first"},
		{name: "plain key", key: "truvag3:v1:prod:registry:index:all"},
		{name: "empty tag", key: "prefix:{}:suffix"},
		{name: "unterminated tag", key: "prefix:{tag"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := redisHashTag(test.key); got != test.want {
				t.Fatalf("redisHashTag(%q) = %q, want %q", test.key, got, test.want)
			}
		})
	}
}

func requireSameRedisHashTag(t *testing.T, keys ...string) {
	t.Helper()
	if len(keys) == 0 {
		t.Fatal("at least one Redis key is required")
	}
	want := redisHashTag(keys[0])
	if want == "" {
		t.Fatalf("Redis key %q has no non-empty hash tag", keys[0])
	}
	for _, key := range keys[1:] {
		if got := redisHashTag(key); got != want {
			t.Fatalf("Redis key %q has hash tag %q, want %q", key, got, want)
		}
	}
}

func redisHashTag(key string) string {
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
