package core

import "context"

// ObservedEpisodicReader is an optional read-health extension to EpisodicMemory.
// Implementations return an error when a backend/index/record read fails instead
// of converting that failure into a successful empty result. The owning caller
// retains optional-memory fail-open policy. Nil error does not promise an atomic
// snapshot, unlimited history, or visibility beyond the filter/retention window.
// Ordering and visibility follow EpisodicMemory.QueryEvents.
type ObservedEpisodicReader interface {
	QueryEventsObserved(ctx context.Context, callerDomain string, filter EventFilter) ([]AgentEvent, error)
}
