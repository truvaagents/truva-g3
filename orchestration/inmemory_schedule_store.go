// Package orchestration — InMemoryScheduleStore for dev/test and single-instance
// deployments.
//
// Thread-safe via sync.RWMutex. All Get/List/GetDue results are deep copies,
// so callers cannot accidentally mutate stored state through a shared
// reference. Not suitable for multi-replica production — use
// RedisScheduleStore (or another persistent backend) there.

package orchestration

import (
	"context"
	"sort"
	"sync"
	"time"

	"github.com/truvaagents/truva-g3/core"
)

// Compile-time check: InMemoryScheduleStore satisfies core.ScheduleStore.
var _ core.ScheduleStore = (*InMemoryScheduleStore)(nil)

// InMemoryScheduleStore is an in-memory implementation of core.ScheduleStore.
// All operations are thread-safe.
type InMemoryScheduleStore struct {
	mu        sync.RWMutex
	schedules map[string]*core.Schedule
}

// NewInMemoryScheduleStore creates a new empty in-memory schedule store.
func NewInMemoryScheduleStore() *InMemoryScheduleStore {
	return &InMemoryScheduleStore{
		schedules: make(map[string]*core.Schedule),
	}
}

// Create persists a new schedule. Returns core.ErrScheduleAlreadyExists if
// a schedule with the same ID is already present.
func (s *InMemoryScheduleStore) Create(_ context.Context, schedule *core.Schedule) error {
	if schedule == nil {
		return errNilSchedule
	}
	if schedule.ID == "" {
		return errEmptyScheduleID
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if _, exists := s.schedules[schedule.ID]; exists {
		return core.ErrScheduleAlreadyExists
	}
	s.schedules[schedule.ID] = cloneSchedule(schedule)
	return nil
}

// Get retrieves a schedule by ID. Returns a deep copy to prevent callers
// from mutating stored state.
func (s *InMemoryScheduleStore) Get(_ context.Context, id string) (*core.Schedule, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	schedule, ok := s.schedules[id]
	if !ok {
		return nil, core.ErrScheduleNotFound
	}
	return cloneSchedule(schedule), nil
}

// List returns all schedules. Returns deep copies.
// Results are sorted by RunAt ascending for deterministic iteration.
func (s *InMemoryScheduleStore) List(_ context.Context) ([]*core.Schedule, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	out := make([]*core.Schedule, 0, len(s.schedules))
	for _, sched := range s.schedules {
		out = append(out, cloneSchedule(sched))
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].RunAt.Before(out[j].RunAt)
	})
	return out, nil
}

// Update persists changes to an existing schedule. Returns
// core.ErrScheduleNotFound if the schedule doesn't exist.
func (s *InMemoryScheduleStore) Update(_ context.Context, schedule *core.Schedule) error {
	if schedule == nil {
		return errNilSchedule
	}
	if schedule.ID == "" {
		return errEmptyScheduleID
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if _, ok := s.schedules[schedule.ID]; !ok {
		return core.ErrScheduleNotFound
	}
	s.schedules[schedule.ID] = cloneSchedule(schedule)
	return nil
}

// Delete removes a schedule. Returns core.ErrScheduleNotFound if the
// schedule doesn't exist.
func (s *InMemoryScheduleStore) Delete(_ context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if _, ok := s.schedules[id]; !ok {
		return core.ErrScheduleNotFound
	}
	delete(s.schedules, id)
	return nil
}

// GetDue returns all enabled schedules whose RunAt <= now. Results are deep
// copies, sorted by RunAt ascending.
//
// Returns an empty slice (never nil) when no schedules are due, matching
// the convention used by List() for consistency with callers that may
// compare against a fresh empty slice.
func (s *InMemoryScheduleStore) GetDue(_ context.Context, now time.Time) ([]*core.Schedule, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	out := make([]*core.Schedule, 0)
	for _, sched := range s.schedules {
		if sched.Enabled && !sched.RunAt.After(now) {
			out = append(out, cloneSchedule(sched))
		}
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].RunAt.Before(out[j].RunAt)
	})
	return out, nil
}

// cloneSchedule returns a shallow-per-top-level-field copy of a schedule so
// callers cannot mutate the top-level stored state through shared
// references. The Input map is copied one level deep (keys + top-level
// values), and LastRunAt is dereferenced.
//
// KNOWN LIMITATION: if Input contains nested maps or slices, those nested
// values are NOT deep-copied — mutating a nested value in a returned copy
// will also mutate the stored schedule's nested value. A full JSON-level
// deep copy would require marshal/unmarshal round-trips or reflection,
// both expensive.
//
// This limitation is acceptable because:
//   - The production backend is Redis, which naturally deep-copies via
//     its JSON round-trip in Get/List/GetDue.
//   - The in-memory store is only for dev/test and single-instance
//     deployments where callers control their own mutation discipline.
//   - Agents' schedule Input payloads are typically flat string→string or
//     string→primitive maps (e.g. "instruction" → "...", "service" →
//     "api-gateway"), so the nested-mutation case is rare in practice.
//
// If a future use case requires full deep copies, switch to json.Marshal +
// json.Unmarshal here at a small performance cost.
func cloneSchedule(s *core.Schedule) *core.Schedule {
	if s == nil {
		return nil
	}
	cp := *s
	if s.Input != nil {
		cp.Input = make(map[string]interface{}, len(s.Input))
		for k, v := range s.Input {
			cp.Input[k] = v
		}
	}
	if s.LastRunAt != nil {
		t := *s.LastRunAt
		cp.LastRunAt = &t
	}
	return &cp
}
