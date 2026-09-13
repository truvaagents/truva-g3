package orchestration_test

import (
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/truvaagents/truva-g3/core"
	"github.com/truvaagents/truva-g3/orchestration"
	"github.com/truvaagents/truva-g3/orchestration/backendconformance"
)

func TestRedisCheckpointResumeConformance(t *testing.T) {
	backendconformance.RunCheckpointResumeConformance(t, func(t *testing.T) backendconformance.CheckpointResumeFixture {
		server := miniredis.RunT(t)
		now := time.Now().UTC().Truncate(time.Millisecond)
		server.SetTime(now)
		keyspace, err := core.NewRedisKeyspace("resume-conformance")
		if err != nil {
			t.Fatal(err)
		}
		adapters := make([]orchestration.CheckpointResumePersistence, 2)
		var persistence *orchestration.RedisCheckpointStore
		for i := range adapters {
			client := redis.NewClient(&redis.Options{Addr: server.Addr()})
			t.Cleanup(func() { _ = client.Close() })
			store, err := orchestration.NewRedisCheckpointStoreWithClient(client,
				orchestration.WithCheckpointKeyspace(keyspace, "agent"), orchestration.WithCheckpointTTL(time.Hour))
			if err != nil {
				t.Fatal(err)
			}
			adapters[i] = store
			persistence = store
		}
		return backendconformance.CheckpointResumeFixture{
			Persistence: persistence, Resumers: adapters,
			Advance: func(duration time.Duration) {
				now = now.Add(duration)
				server.SetTime(now)
				server.FastForward(duration)
			},
			RemainingTTL: func(id string) time.Duration {
				return server.TTL(keyspace.Tagged("hitl", "agent") + ":checkpoint:" + id)
			},
		}
	})
}
