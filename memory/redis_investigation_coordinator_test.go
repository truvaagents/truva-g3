package memory

import (
	"context"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

func newRedisInvestigationTestCoordinator(t *testing.T) (*miniredis.Miniredis, *redis.Client, *AtomicLockCoordinator) {
	t.Helper()
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	coordinator, err := NewAtomicLockCoordinator(
		WithCoordinatorRedisClient(client),
		WithCoordinatorDomain("infra"),
	)
	if err != nil {
		t.Fatal(err)
	}
	return server, client, coordinator
}

func TestAtomicLockCoordinatorMaintainsInvestigationIndex(t *testing.T) {
	_, client, coordinator := newRedisInvestigationTestCoordinator(t)
	ctx := context.Background()
	claimed, holder, err := coordinator.ClaimInvestigation(ctx, "agent-a", "entity-1", time.Minute)
	if err != nil || !claimed || holder != "" {
		t.Fatalf("first claim = (%v, %q, %v)", claimed, holder, err)
	}
	if !client.SIsMember(ctx, coordinator.investigationIndexKey(), "entity-1").Val() {
		t.Fatal("claimed entity is absent from the domain index")
	}
	claimed, holder, err = coordinator.ClaimInvestigation(ctx, "agent-b", "entity-1", time.Minute)
	if err != nil || claimed || holder != "agent-a" {
		t.Fatalf("contended claim = (%v, %q, %v)", claimed, holder, err)
	}
	if err := coordinator.ReleaseInvestigation(ctx, "agent-b", "entity-1"); err != nil {
		t.Fatal(err)
	}
	if err := coordinator.ReleaseInvestigation(ctx, "agent-a", "entity-1"); err != nil {
		t.Fatal(err)
	}
	if client.SIsMember(ctx, coordinator.investigationIndexKey(), "entity-1").Val() {
		t.Fatal("release retained stale index membership")
	}
}

func TestAtomicLockCoordinatorPrunesExpiredAndFailsOpen(t *testing.T) {
	server, client, coordinator := newRedisInvestigationTestCoordinator(t)
	ctx := context.Background()
	if claimed, _, err := coordinator.ClaimInvestigation(ctx, "agent", "expired", time.Second); err != nil || !claimed {
		t.Fatalf("claim = (%v, %v)", claimed, err)
	}
	server.FastForward(2 * time.Second)
	active, err := coordinator.GetActiveInvestigations(ctx)
	if err != nil || len(active) != 0 {
		t.Fatalf("active = (%v, %v)", active, err)
	}
	if client.SIsMember(ctx, coordinator.investigationIndexKey(), "expired").Val() {
		t.Fatal("expired claim retained stale index membership")
	}
	_ = client.Close()
	if claimed, holder, err := coordinator.ClaimInvestigation(ctx, "agent", "closed", time.Minute); err != nil || claimed || holder != "" {
		t.Fatalf("claim did not fail open: (%v, %q, %v)", claimed, holder, err)
	}
	if active, err := coordinator.GetActiveInvestigations(ctx); err != nil || active != nil {
		t.Fatalf("read did not fail open: (%v, %v)", active, err)
	}
	if err := coordinator.ReleaseInvestigation(ctx, "agent", "closed"); err != nil {
		t.Fatalf("release did not fail open: %v", err)
	}
}

func TestInvestigationScanHandlesCursorEdgeCasesAndPrunesStaleMembers(t *testing.T) {
	_, client, coordinator := newRedisInvestigationTestCoordinator(t)
	claimed, _, err := coordinator.ClaimInvestigation(t.Context(), "agent", "live", time.Minute)
	if err != nil || !claimed {
		t.Fatalf("claim = (%v, %v)", claimed, err)
	}
	if err := client.SAdd(t.Context(), coordinator.investigationIndexKey(), "stale").Err(); err != nil {
		t.Fatal(err)
	}
	client.AddHook(&scriptedMemorySScanHook{pages: []memoryScanPage{
		{members: []string{"live", "stale"}, cursor: 11},
		{members: nil, cursor: 22},
		{members: []string{"live"}, cursor: 0},
	}})

	active, err := coordinator.GetActiveInvestigations(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(active) != 1 || active["live"] != "agent" {
		t.Fatalf("active = %v, want one deduplicated live investigation", active)
	}
	if client.SIsMember(t.Context(), coordinator.investigationIndexKey(), "stale").Val() {
		t.Fatal("stale investigation member survived completed cursor iteration")
	}
}

func TestAtomicLockCoordinatorObservesDistinctFailOpenFailures(t *testing.T) {
	tests := []struct {
		name      string
		operation string
		run       func(*testing.T, *redis.Client, *AtomicLockCoordinator)
	}{
		{
			name:      "claim script",
			operation: "claim_investigation",
			run: func(t *testing.T, client *redis.Client, coordinator *AtomicLockCoordinator) {
				client.AddHook(&memoryFailureHook{command: "evalsha"})
				claimed, holder, err := coordinator.ClaimInvestigation(t.Context(), "agent", "script", time.Minute)
				if err != nil || claimed || holder != "" {
					t.Fatalf("fail-open claim = (%v, %q, %v)", claimed, holder, err)
				}
			},
		},
		{
			name:      "claim holder read",
			operation: "claim_holder_read",
			run: func(t *testing.T, client *redis.Client, coordinator *AtomicLockCoordinator) {
				if err := client.Set(t.Context(), coordinator.investigationKey("held"), "agent-a", time.Minute).Err(); err != nil {
					t.Fatal(err)
				}
				client.AddHook(&memoryFailureHook{command: "get"})
				claimed, holder, err := coordinator.ClaimInvestigation(t.Context(), "agent-b", "held", time.Minute)
				if err != nil || claimed || holder != "" {
					t.Fatalf("fail-open holder read = (%v, %q, %v)", claimed, holder, err)
				}
			},
		},
		{
			name:      "index scan",
			operation: "active_investigation_index_scan",
			run: func(t *testing.T, client *redis.Client, coordinator *AtomicLockCoordinator) {
				client.AddHook(&memoryFailureHook{command: "sscan"})
				if got, err := coordinator.GetActiveInvestigations(t.Context()); err != nil || got != nil {
					t.Fatalf("fail-open scan = (%v, %v)", got, err)
				}
			},
		},
		{
			name:      "hydration result",
			operation: "active_investigation_load",
			run: func(t *testing.T, client *redis.Client, coordinator *AtomicLockCoordinator) {
				if err := client.Set(t.Context(), coordinator.investigationKey("hydrate"), "agent", time.Minute).Err(); err != nil {
					t.Fatal(err)
				}
				if err := client.SAdd(t.Context(), coordinator.investigationIndexKey(), "hydrate").Err(); err != nil {
					t.Fatal(err)
				}
				client.AddHook(&memoryFailureHook{failPipelineResult: "get"})
				if got, err := coordinator.GetActiveInvestigations(t.Context()); err != nil || got != nil {
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
			coordinator, err := NewAtomicLockCoordinator(
				WithCoordinatorRedisClient(client),
				WithCoordinatorDomain("infra"),
				WithCoordinatorLogger(logger),
			)
			if err != nil {
				t.Fatal(err)
			}
			test.run(t, client, coordinator)
			fields := logger.fieldsForOperation(test.operation)
			if fields == nil {
				t.Fatalf("failure was not logged with operation %q", test.operation)
			}
			if fields["error"] != "redis investigation coordination unavailable" || fields["error_type"] != "backend" {
				t.Fatalf("failure log fields = %#v, want bounded backend diagnostic", fields)
			}
		})
	}
}
