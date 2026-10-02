package memory

import (
	"context"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
	"github.com/truvaagents/truva-g3/core"
)

type leaseTestProvider interface {
	core.InvestigationCoordinator
	core.InvestigationLeaseCoordinator
}

func TestInvestigationLeaseProviderParity(t *testing.T) {
	for _, backend := range []string{"memory", "redis"} {
		t.Run(backend, func(t *testing.T) {
			var provider leaseTestProvider
			var expire func()
			if backend == "redis" {
				server, _, coordinator := newRedisInvestigationTestCoordinator(t)
				provider = coordinator
				expire = func() { server.FastForward(time.Hour) }
			} else {
				coordinator := NewInMemoryInvestigationCoordinator()
				provider = coordinator
				expire = func() {
					coordinator.mu.Lock()
					defer coordinator.mu.Unlock()
					for id, claim := range coordinator.claims {
						claim.expiresAt = time.Now().Add(-time.Second)
						coordinator.claims[id] = claim
					}
				}
			}
			ctx := t.Context()
			request := core.InvestigationLeaseRequest{AgentName: "agent", EntityID: "document", AcquisitionID: "first-segment"}
			lease, holder, err := provider.AcquireInvestigation(ctx, request)
			if err != nil || lease == nil || holder != "" || time.Until(lease.ExpiresAt) < 29*time.Minute {
				t.Fatalf("default acquisition: %v, %s, %v", lease, holder, err)
			}
			repeated, _, err := provider.AcquireInvestigation(ctx, request)
			if err != nil || repeated == nil || repeated.AcquisitionID != lease.AcquisitionID || repeated.ExpiresAt.Sub(lease.ExpiresAt) > time.Second {
				t.Fatal("idempotent acquisition changed identity/lifetime")
			}
			active, err := provider.GetActiveInvestigations(ctx)
			if err != nil || active["document"] != "agent" {
				t.Fatalf("holder visibility: %v, %v", active, err)
			}
			if err := provider.ReleaseInvestigation(ctx, "agent", "document"); err != nil {
				t.Fatal(err)
			}
			if got, holder, err := provider.ClaimInvestigation(ctx, "legacy", "document", time.Minute); err != nil || got || holder != "agent" {
				t.Fatal("legacy writer bypassed token ownership")
			}
			request.AcquisitionID = "second-segment"
			if got, holder, err := provider.AcquireInvestigation(ctx, request); err != nil || got != nil || holder != "agent" {
				t.Fatal("same-agent contention was treated as ownership")
			}
			expire()
			newer, _, err := provider.AcquireInvestigation(ctx, request)
			if err != nil || newer == nil {
				t.Fatal(err)
			}
			if outcome, err := provider.ReleaseInvestigationLease(ctx, *lease); err != nil || outcome != core.InvestigationNotOwned {
				t.Fatalf("late cleanup: %s, %v", outcome, err)
			}
			if outcome, err := provider.ReleaseInvestigationLease(ctx, *newer); err != nil || outcome != core.InvestigationReleased {
				t.Fatalf("owned cleanup: %s, %v", outcome, err)
			}
			if outcome, err := provider.ReleaseInvestigationLease(ctx, *newer); err != nil || outcome != core.InvestigationAbsent {
				t.Fatalf("repeat cleanup: %s, %v", outcome, err)
			}
			if ok, _, err := provider.ClaimInvestigation(ctx, "legacy", "document", time.Minute); err != nil || !ok {
				t.Fatal("legacy acquisition failed")
			}
			if got, holder, err := provider.AcquireInvestigation(ctx, request); err != nil || got != nil || holder != "legacy" {
				t.Fatal("token writer bypassed legacy claim")
			}
			if outcome, err := provider.ReleaseInvestigationLease(ctx, *newer); err != nil || outcome != core.InvestigationNotOwned {
				t.Fatal("token cleanup deleted legacy owner")
			}
			for _, invalid := range []core.InvestigationLeaseRequest{{}, {AgentName: "a", EntityID: "b", AcquisitionID: "c", TTL: -1}} {
				if _, _, err := provider.AcquireInvestigation(ctx, invalid); err == nil {
					t.Fatal("invalid acquisition accepted")
				}
			}
			if result, err := provider.ReleaseInvestigationLease(ctx, core.InvestigationLease{}); err == nil || result != core.InvestigationUnknown {
				t.Fatal("invalid release accepted")
			}
			cancelled, cancel := context.WithCancel(ctx)
			cancel()
			if _, _, err := provider.AcquireInvestigation(cancelled, request); err == nil {
				t.Fatal("cancelled acquisition accepted")
			}
			if result, err := provider.ReleaseInvestigationLease(cancelled, *newer); err == nil || result != core.InvestigationUnknown {
				t.Fatal("cancelled release reported success")
			}
		})
	}
}

func TestInvestigationLeaseOldBinaryReleaseCannotDeleteNewClaim(t *testing.T) {
	_, client, coordinator := newRedisInvestigationTestCoordinator(t)
	lease, _, err := coordinator.AcquireInvestigation(t.Context(), core.InvestigationLeaseRequest{AgentName: "agent", EntityID: "entity", AcquisitionID: "segment"})
	if err != nil {
		t.Fatal(err)
	}
	oldRelease := redis.NewScript(`if redis.call('GET', KEYS[1]) == ARGV[1] then redis.call('DEL', KEYS[1]); return 1 end; return 0`)
	if err := oldRelease.Run(t.Context(), client, []string{coordinator.investigationKey("entity")}, "agent").Err(); err == nil {
		t.Fatal("old release should fail closed on hash ownership")
	}
	if outcome, err := coordinator.ReleaseInvestigationLease(t.Context(), *lease); err != nil || outcome != core.InvestigationReleased {
		t.Fatalf("old release removed new lease: %s, %v", outcome, err)
	}
	_ = client.Close()
	if _, _, err := coordinator.AcquireInvestigation(t.Context(), core.InvestigationLeaseRequest{AgentName: "agent", EntityID: "entity", AcquisitionID: "segment"}); err == nil {
		t.Fatal("backend outage reported as observed contention")
	}
	if outcome, err := coordinator.ReleaseInvestigationLease(t.Context(), *lease); err == nil || outcome != core.InvestigationUnknown {
		t.Fatal("backend outage reported successful cleanup")
	}
}
