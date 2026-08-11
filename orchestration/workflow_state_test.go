package orchestration

import (
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/go-redis/redis/v8"
)

func TestRedisStateStoreClientConstructors(t *testing.T) {
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { _ = client.Close() })

	store, err := NewRedisStateStoreWithClient(client, 0)
	if err != nil {
		t.Fatalf("NewRedisStateStoreWithClient() error = %v", err)
	}
	if store.client != client || store.ttl != 24*time.Hour || store.keyPrefix != "workflow" {
		t.Fatalf("default client store = client %T, ttl %v, prefix %q", store.client, store.ttl, store.keyPrefix)
	}

	store, err = NewRedisStateStoreWithClientAndPrefix(client, time.Hour, " tenant:workflow: ")
	if err != nil {
		t.Fatalf("NewRedisStateStoreWithClientAndPrefix() error = %v", err)
	}
	if store.ttl != time.Hour || store.keyPrefix != "tenant:workflow" {
		t.Fatalf("explicit client store = ttl %v, prefix %q", store.ttl, store.keyPrefix)
	}

	if _, err := NewRedisStateStoreWithClient(nil, time.Hour); err == nil {
		t.Fatal("nil workflow-state Redis client was accepted")
	}
	if _, err := NewRedisStateStoreWithClientAndPrefix(client, time.Hour, " : "); err == nil {
		t.Fatal("empty workflow-state key prefix was accepted")
	}
}
