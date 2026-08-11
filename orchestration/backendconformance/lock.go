package backendconformance

import (
	"context"
	"testing"
	"time"

	"github.com/truvaagents/truva-g3/core"
)

type LockFixture struct {
	Locks   []core.DistributedLock
	Advance func(time.Duration)
}

type LockFactory func(*testing.T) LockFixture

// RunDistributedLockConformance proves the efficiency-lock guarantees exposed
// by core.DistributedLock, including owner-safe release across instances.
func RunDistributedLockConformance(t *testing.T, factory LockFactory) {
	t.Helper()
	t.Run("competing owners and owner safe release", func(t *testing.T) {
		fixture := factory(t)
		if len(fixture.Locks) < 2 {
			t.Fatal("two independently constructed locks are required")
		}
		first, second := fixture.Locks[0], fixture.Locks[1]
		acquired, err := first.Acquire(t.Context(), "shared", time.Minute)
		if err != nil || !acquired {
			t.Fatalf("first acquire = %v, %v", acquired, err)
		}
		acquired, err = second.Acquire(t.Context(), "shared", time.Minute)
		if err != nil || acquired {
			t.Fatalf("competing acquire = %v, %v", acquired, err)
		}
		if err := second.Release(t.Context(), "shared"); err != nil {
			t.Fatal(err)
		}
		acquired, err = second.Acquire(t.Context(), "shared", time.Minute)
		if err != nil || acquired {
			t.Fatalf("non-owner release removed the lock: acquire = %v, %v", acquired, err)
		}
		if err := first.Release(t.Context(), "shared"); err != nil {
			t.Fatal(err)
		}
		acquired, err = second.Acquire(t.Context(), "shared", time.Minute)
		if err != nil || !acquired {
			t.Fatalf("acquire after owner release = %v, %v", acquired, err)
		}
	})

	t.Run("expired lease is retryable and stale release is safe", func(t *testing.T) {
		fixture := factory(t)
		if len(fixture.Locks) < 2 || fixture.Advance == nil {
			t.Fatal("two locks and deterministic time advancement are required")
		}
		first, second := fixture.Locks[0], fixture.Locks[1]
		acquired, err := first.Acquire(t.Context(), "expiring", time.Second)
		if err != nil || !acquired {
			t.Fatalf("first acquire = %v, %v", acquired, err)
		}
		fixture.Advance(2 * time.Second)
		acquired, err = second.Acquire(t.Context(), "expiring", time.Minute)
		if err != nil || !acquired {
			t.Fatalf("acquire after expiry = %v, %v", acquired, err)
		}
		if err := first.Release(t.Context(), "expiring"); err != nil {
			t.Fatal(err)
		}
		probe, err := first.Acquire(t.Context(), "expiring", time.Minute)
		if err != nil || probe {
			t.Fatalf("stale release removed the new owner: acquire = %v, %v", probe, err)
		}
	})

	t.Run("cancellation", func(t *testing.T) {
		fixture := factory(t)
		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		if _, err := fixture.Locks[0].Acquire(ctx, "cancelled", time.Second); err == nil {
			t.Fatal("cancelled acquire returned nil error")
		}
	})
}
