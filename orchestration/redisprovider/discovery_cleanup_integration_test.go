//go:build integration

package redisprovider

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
	"github.com/truvaagents/truva-g3/core"
)

type discoveryRevivalHook struct {
	key    string
	revive func()
}

func (*discoveryRevivalHook) DialHook(next redis.DialHook) redis.DialHook { return next }
func (*discoveryRevivalHook) ProcessPipelineHook(next redis.ProcessPipelineHook) redis.ProcessPipelineHook {
	return next
}
func (h *discoveryRevivalHook) ProcessHook(next redis.ProcessHook) redis.ProcessHook {
	return func(ctx context.Context, cmd redis.Cmder) error {
		err := next(ctx, cmd)
		if cmd.Name() == "get" && cmd.Args()[1] == h.key && errors.Is(err, redis.Nil) && h.revive != nil {
			revive := h.revive
			h.revive = nil
			revive()
		}
		return err
	}
}

func runDiscoveryCleanupRegistrationRace(t *testing.T, seeds []string) {
	t.Helper()
	config := core.DefaultRedisConnectionConfig()
	config.Mode, config.Addrs = core.RedisModeCluster, seeds
	reader, err := core.NewRedisUniversalClient(config)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reader.Close() })
	writer, err := core.NewRedisUniversalClient(config)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = writer.Close() })
	keys, err := core.NewRedisKeyspace(clusterNamespace("cleanup-race"))
	if err != nil {
		t.Fatal(err)
	}
	discovery, err := core.NewRedisDiscoveryWithClient(reader, keys, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	registry, err := core.NewRedisRegistryWithClient(writer, keys, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	info := &core.ServiceInfo{ID: "same-id", Name: "forecast", Type: core.ComponentTypeTool, Capabilities: []core.Capability{{Name: "weather"}}}
	for _, filter := range []core.DiscoveryFilter{{}, {Type: info.Type, Name: info.Name, Capabilities: []string{"weather"}}} {
		if err := registry.Register(t.Context(), info); err != nil {
			t.Fatal(err)
		}
		key := keys.Tagged("registry", "", "service", info.ID)
		if err := writer.Del(t.Context(), key).Err(); err != nil {
			t.Fatal(err)
		}
		reader.AddHook(&discoveryRevivalHook{key: key, revive: func() {
			if err := registry.Register(t.Context(), info); err != nil {
				t.Fatal(err)
			}
		}})
		if _, err := discovery.Discover(t.Context(), filter); err != nil {
			t.Fatal(err)
		}
		found, err := discovery.Discover(t.Context(), filter)
		if err != nil || len(found) != 1 {
			t.Fatalf("fresh registration lost after cleanup: count=%d error=%v", len(found), err)
		}
	}
}
