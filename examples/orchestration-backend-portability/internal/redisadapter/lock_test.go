package redisadapter_test

import (
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/truvaagents/truva-g3/core"
	"github.com/truvaagents/truva-g3/examples/orchestration-backend-portability/internal/redisadapter"
	"github.com/truvaagents/truva-g3/orchestration/backendconformance"
)

func TestDistributedLockConformance(t *testing.T) {
	backendconformance.RunDistributedLockConformance(t, func(t *testing.T) backendconformance.LockFixture {
		server := miniredis.RunT(t)
		client := redis.NewClient(&redis.Options{Addr: server.Addr()})
		t.Cleanup(func() { _ = client.Close() })

		first, err := redisadapter.NewDistributedLock(client, "conformance")
		if err != nil {
			t.Fatal(err)
		}
		second, err := redisadapter.NewDistributedLock(client, "conformance")
		if err != nil {
			t.Fatal(err)
		}
		return backendconformance.LockFixture{
			Locks:   []core.DistributedLock{first, second},
			Advance: server.FastForward,
		}
	})
}

func TestNewDistributedLockRejectsInvalidConfiguration(t *testing.T) {
	client := redis.NewClient(&redis.Options{Addr: "127.0.0.1:1"})
	t.Cleanup(func() { _ = client.Close() })

	if _, err := redisadapter.NewDistributedLock(nil, "namespace"); err == nil {
		t.Fatal("nil Redis client was accepted")
	}
	if _, err := redisadapter.NewDistributedLock(client, "  "); err == nil {
		t.Fatal("empty namespace was accepted")
	}
}

func TestDistributedLockNamespacesAreIsolated(t *testing.T) {
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { _ = client.Close() })

	first, err := redisadapter.NewDistributedLock(client, "tenant-a")
	if err != nil {
		t.Fatal(err)
	}
	second, err := redisadapter.NewDistributedLock(client, "tenant-b")
	if err != nil {
		t.Fatal(err)
	}
	for name, lock := range map[string]core.DistributedLock{"tenant-a": first, "tenant-b": second} {
		acquired, err := lock.Acquire(t.Context(), "scheduler", time.Minute)
		if err != nil || !acquired {
			t.Fatalf("%s Acquire() = %v, %v; namespaces should not contend", name, acquired, err)
		}
	}
}
