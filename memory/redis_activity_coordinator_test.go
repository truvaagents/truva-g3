package memory

import (
	"context"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/truvaagents/truva-g3/core"
)

func newRedisActivityTestCoordinator(t *testing.T) (*miniredis.Miniredis, *redis.Client, *RedisActivityCoordinator) {
	t.Helper()
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	coordinator, err := NewRedisActivityCoordinator(client, "infra")
	if err != nil {
		t.Fatal(err)
	}
	return server, client, coordinator
}

func TestRedisActivityCoordinatorMaintainsDomainIndex(t *testing.T) {
	_, client, coordinator := newRedisActivityTestCoordinator(t)
	ctx := context.Background()
	signal := core.ActivitySignal{RequestID: "request-1", AgentDomain: "infra", Status: "running", TTL: time.Minute}
	if err := coordinator.AnnounceActivity(ctx, signal); err != nil {
		t.Fatal(err)
	}
	if !client.SIsMember(ctx, coordinator.signalIndexKey("infra"), signal.RequestID).Val() {
		t.Fatal("activity ID is absent from the domain index")
	}
	activities, err := coordinator.GetDomainActivities(ctx, "infra")
	if err != nil || len(activities) != 1 || activities[0].RequestID != signal.RequestID {
		t.Fatalf("activities = (%v, %v)", activities, err)
	}
	if err := coordinator.CompleteActivity(ctx, signal.RequestID); err != nil {
		t.Fatal(err)
	}
	if client.Exists(ctx, coordinator.signalKey(signal.RequestID)).Val() != 0 ||
		client.SIsMember(ctx, coordinator.signalIndexKey("infra"), signal.RequestID).Val() {
		t.Fatal("completion did not remove the signal aggregate")
	}
}

func TestRedisActivityCoordinatorPrunesExpiredAndFailsOpen(t *testing.T) {
	server, client, coordinator := newRedisActivityTestCoordinator(t)
	ctx := context.Background()
	if err := coordinator.AnnounceActivity(ctx, core.ActivitySignal{
		RequestID: "expired", AgentDomain: "infra", TTL: time.Second,
	}); err != nil {
		t.Fatal(err)
	}
	server.FastForward(2 * time.Second)
	activities, err := coordinator.GetDomainActivities(ctx, "infra")
	if err != nil || len(activities) != 0 {
		t.Fatalf("expired activities = (%v, %v)", activities, err)
	}
	if client.SIsMember(ctx, coordinator.signalIndexKey("infra"), "expired").Val() {
		t.Fatal("expired activity retained stale index membership")
	}
	_ = client.Close()
	if err := coordinator.AnnounceActivity(ctx, core.ActivitySignal{RequestID: "closed", TTL: time.Minute}); err != nil {
		t.Fatalf("announce did not fail open: %v", err)
	}
	if got, err := coordinator.GetDomainActivities(ctx, "infra"); err != nil || got != nil {
		t.Fatalf("read did not fail open: (%v, %v)", got, err)
	}
	if err := coordinator.CompleteActivity(ctx, "closed"); err != nil {
		t.Fatalf("complete did not fail open: %v", err)
	}
}

func TestRedisActivityScanHandlesCursorEdgeCasesAndPrunesStaleMembers(t *testing.T) {
	_, client, coordinator := newRedisActivityTestCoordinator(t)
	live := core.ActivitySignal{RequestID: "live", AgentDomain: "infra", Status: "running", TTL: time.Minute}
	if err := coordinator.AnnounceActivity(t.Context(), live); err != nil {
		t.Fatal(err)
	}
	if err := client.SAdd(t.Context(), coordinator.signalIndexKey("infra"), "stale").Err(); err != nil {
		t.Fatal(err)
	}
	client.AddHook(&scriptedMemorySScanHook{pages: []memoryScanPage{
		{members: []string{live.RequestID, "stale"}, cursor: 11},
		{members: nil, cursor: 22},
		{members: []string{live.RequestID}, cursor: 0},
	}})

	activities, err := coordinator.GetDomainActivities(t.Context(), "infra")
	if err != nil {
		t.Fatal(err)
	}
	if len(activities) != 1 || activities[0].RequestID != live.RequestID {
		t.Fatalf("activities = %v, want one deduplicated live signal", activities)
	}
	if client.SIsMember(t.Context(), coordinator.signalIndexKey("infra"), "stale").Val() {
		t.Fatal("stale activity member survived completed cursor iteration")
	}
}

func TestRedisActivityCoordinatorObservesDistinctFailOpenFailures(t *testing.T) {
	tests := []struct {
		name      string
		operation string
		run       func(*testing.T, *redis.Client, *RedisActivityCoordinator)
	}{
		{
			name:      "announcement pipeline",
			operation: "activity_announce",
			run: func(t *testing.T, client *redis.Client, coordinator *RedisActivityCoordinator) {
				client.AddHook(&memoryFailureHook{failPipeline: true})
				if err := coordinator.AnnounceActivity(t.Context(), core.ActivitySignal{RequestID: "pipeline", TTL: time.Minute}); err != nil {
					t.Fatalf("fail-open announce returned %v", err)
				}
			},
		},
		{
			name:      "TTL read",
			operation: "activity_status_ttl",
			run: func(t *testing.T, client *redis.Client, coordinator *RedisActivityCoordinator) {
				if err := coordinator.AnnounceActivity(t.Context(), core.ActivitySignal{RequestID: "ttl", TTL: time.Minute}); err != nil {
					t.Fatal(err)
				}
				client.AddHook(&memoryFailureHook{command: "ttl"})
				if err := coordinator.UpdateStatus(t.Context(), "ttl", "done"); err != nil {
					t.Fatalf("fail-open update returned %v", err)
				}
			},
		},
		{
			name:      "index scan",
			operation: "activity_index_scan",
			run: func(t *testing.T, client *redis.Client, coordinator *RedisActivityCoordinator) {
				client.AddHook(&memoryFailureHook{command: "sscan"})
				if got, err := coordinator.GetDomainActivities(t.Context(), "infra"); err != nil || got != nil {
					t.Fatalf("fail-open scan = (%v, %v)", got, err)
				}
			},
		},
		{
			name:      "hydration result",
			operation: "activity_signal_load",
			run: func(t *testing.T, client *redis.Client, coordinator *RedisActivityCoordinator) {
				if err := coordinator.AnnounceActivity(t.Context(), core.ActivitySignal{RequestID: "hydrate", TTL: time.Minute}); err != nil {
					t.Fatal(err)
				}
				client.AddHook(&memoryFailureHook{failPipelineResult: "get"})
				if got, err := coordinator.GetDomainActivities(t.Context(), "infra"); err != nil || got != nil {
					t.Fatalf("fail-open hydration = (%v, %v)", got, err)
				}
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := miniredis.RunT(t)
			client := redis.NewClient(&redis.Options{Addr: server.Addr()})
			t.Cleanup(func() { _ = client.Close() })
			logger := &memoryCaptureLogger{}
			coordinator, err := NewRedisActivityCoordinator(client, "infra", WithActivityCoordinatorLogger(logger))
			if err != nil {
				t.Fatal(err)
			}
			test.run(t, client, coordinator)
			fields := logger.fieldsForOperation(test.operation)
			if fields == nil {
				t.Fatalf("failure was not logged with operation %q", test.operation)
			}
			if fields["error"] != "redis activity coordination unavailable" {
				t.Fatalf("error = %#v, want bounded diagnostic", fields["error"])
			}
			if errorType := fields["error_type"]; errorType != "backend" && errorType != "serialization" {
				t.Fatalf("error_type = %#v, want bounded classification", errorType)
			}
		})
	}
}
