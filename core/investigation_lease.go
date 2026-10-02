package core

import (
	"context"
	"time"
)

// InvestigationLeaseRequest identifies one acquisition, distinct from an agent
// instance. Generate a fresh AcquisitionID for every execution segment. Retrying
// the same acquisition while live is idempotent and does not extend its TTL.
// AcquisitionID is private lifecycle state, not prompt or telemetry payload.
type InvestigationLeaseRequest struct {
	AgentName     string
	EntityID      string
	AcquisitionID string `json:"-"`
	TTL           time.Duration
}

// InvestigationLease is an exact ownership handle. ExpiresAt is informative;
// backend state, not the caller's clock, determines release ownership.
type InvestigationLease struct {
	AgentName     string
	EntityID      string
	AcquisitionID string `json:"-"`
	ExpiresAt     time.Time
}

// InvestigationReleaseOutcome reports an observed release, not just an accepted
// provider call. Absent includes an expired claim; unknown requires an error.
type InvestigationReleaseOutcome string

const (
	InvestigationReleased InvestigationReleaseOutcome = "released"
	InvestigationAbsent   InvestigationReleaseOutcome = "absent"
	InvestigationNotOwned InvestigationReleaseOutcome = "not_owned"
	InvestigationUnknown  InvestigationReleaseOutcome = "unknown"
)

// InvestigationLeaseCoordinator optionally extends InvestigationCoordinator
// with acquisition-safe ownership. It remains advisory: callers may fail open
// on returned errors, but cannot report an unobserved claim/release as success.
// Implementations contend with legacy claims on the same logical entity lock.
// Names alone cannot release token-owned claims. Implementations honor context
// cancellation; they own no background work or renewal goroutines.
type InvestigationLeaseCoordinator interface {
	// AcquireInvestigation returns a handle if acquired, or nil and the current
	// human-readable holder on contention. Zero TTL selects the provider default;
	// negative TTL or empty identities are invalid. Unknown backend outcomes
	// return an error; callers may retry with the same acquisition identity.
	AcquireInvestigation(ctx context.Context, request InvestigationLeaseRequest) (*InvestigationLease, string, error)
	// ReleaseInvestigationLease compares the full ownership handle atomically.
	// A stale handle cannot remove a newer claim by the same agent. Unknown
	// outcomes are returned as InvestigationUnknown plus a non-nil error.
	ReleaseInvestigationLease(ctx context.Context, lease InvestigationLease) (InvestigationReleaseOutcome, error)
}
