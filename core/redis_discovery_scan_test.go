package core

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

type registryScanPage struct {
	members []string
	cursor  uint64
}

type scriptedRegistrySScanHook struct {
	pages []registryScanPage
	next  int
}

func (*scriptedRegistrySScanHook) DialHook(next redis.DialHook) redis.DialHook { return next }

func (hook *scriptedRegistrySScanHook) ProcessHook(next redis.ProcessHook) redis.ProcessHook {
	return func(ctx context.Context, command redis.Cmder) error {
		if command.Name() != "sscan" || hook.next >= len(hook.pages) {
			return next(ctx, command)
		}
		page := hook.pages[hook.next]
		hook.next++
		scan, ok := command.(*redis.ScanCmd)
		if !ok {
			return fmt.Errorf("SSCAN command has type %T", command)
		}
		scan.SetVal(page.members, page.cursor)
		return nil
	}
}

func (*scriptedRegistrySScanHook) ProcessPipelineHook(next redis.ProcessPipelineHook) redis.ProcessPipelineHook {
	return next
}

func TestRedisDiscoveryScanHandlesCursorEdgeCasesAndPrunesStaleMembers(t *testing.T) {
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	keyspace, err := NewRedisKeyspace("registry-scan-test")
	if err != nil {
		t.Fatal(err)
	}
	discovery, err := NewRedisDiscoveryWithClient(client, keyspace, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	live := &ServiceInfo{ID: "live", Name: "live", Type: ComponentTypeTool}
	if err := discovery.Register(t.Context(), live); err != nil {
		t.Fatal(err)
	}
	if err := client.SAdd(t.Context(), discovery.keys.all(), "stale").Err(); err != nil {
		t.Fatal(err)
	}
	client.AddHook(&scriptedRegistrySScanHook{pages: []registryScanPage{
		{members: []string{live.ID, "stale"}, cursor: 11},
		{members: nil, cursor: 22},
		{members: []string{live.ID}, cursor: 0},
	}})

	services, err := discovery.Discover(t.Context(), DiscoveryFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(services) != 1 || services[0].ID != live.ID {
		t.Fatalf("services = %v, want one deduplicated live service", services)
	}
	if client.SIsMember(t.Context(), discovery.keys.all(), "stale").Val() {
		t.Fatal("stale registry member survived completed cursor iteration")
	}
}
