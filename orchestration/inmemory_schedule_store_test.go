package orchestration

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/truvaagents/truva-g3/core"
	"github.com/truvaagents/truva-g3/core/conformance"
)

// ═══════════════════════════════════════════════════════════════════════════
// Constructor / interface satisfaction
// ═══════════════════════════════════════════════════════════════════════════

func TestInMemoryScheduleStore_ImplementsInterface(t *testing.T) {
	var _ core.ScheduleStore = NewInMemoryScheduleStore()
}

func TestInMemoryScheduleStoreConformance(t *testing.T) {
	conformance.RunScheduleStoreConformance(t, func(*testing.T) conformance.ScheduleStoreFixture {
		shared := NewInMemoryScheduleStore()
		return conformance.ScheduleStoreFixture{
			First: shared, Second: shared, Isolated: NewInMemoryScheduleStore(),
		}
	})
}

func TestInMemoryScheduleStore_NewIsEmpty(t *testing.T) {
	s := NewInMemoryScheduleStore()
	list, err := s.List(context.Background())
	require.NoError(t, err)
	assert.Empty(t, list)
}

// ═══════════════════════════════════════════════════════════════════════════
// Create
// ═══════════════════════════════════════════════════════════════════════════

func makeSchedule(id, target string, runAt time.Time) *core.Schedule {
	return &core.Schedule{
		ID:              id,
		TargetAgent:     target,
		RunAt:           runAt,
		Enabled:         true,
		MissedRunPolicy: core.MissedRunSkip,
		CreatedAt:       time.Now(),
		Input:           map[string]interface{}{"k": "v"},
	}
}

func TestInMemoryScheduleStore_Create_Success(t *testing.T) {
	s := NewInMemoryScheduleStore()
	err := s.Create(context.Background(), makeSchedule("sch-1", "agent-a", time.Now()))
	require.NoError(t, err)

	list, err := s.List(context.Background())
	require.NoError(t, err)
	assert.Len(t, list, 1)
}

func TestInMemoryScheduleStore_Create_Duplicate(t *testing.T) {
	s := NewInMemoryScheduleStore()
	_ = s.Create(context.Background(), makeSchedule("sch-1", "a", time.Now()))
	err := s.Create(context.Background(), makeSchedule("sch-1", "a", time.Now()))
	assert.ErrorIs(t, err, core.ErrScheduleAlreadyExists)
}

func TestInMemoryScheduleStore_Create_Nil(t *testing.T) {
	s := NewInMemoryScheduleStore()
	err := s.Create(context.Background(), nil)
	require.Error(t, err)
	assert.ErrorIs(t, err, errNilSchedule)
}

func TestInMemoryScheduleStore_Create_EmptyID(t *testing.T) {
	s := NewInMemoryScheduleStore()
	err := s.Create(context.Background(), &core.Schedule{TargetAgent: "a", RunAt: time.Now(), Enabled: true})
	require.Error(t, err)
	assert.ErrorIs(t, err, errEmptyScheduleID)
}

func TestInMemoryScheduleStore_Create_IsolatesInput(t *testing.T) {
	// Mutating the caller's input map after Create should NOT affect what
	// the store returns on subsequent Get calls.
	s := NewInMemoryScheduleStore()
	input := map[string]interface{}{"k": "v"}
	sch := makeSchedule("sch-1", "a", time.Now())
	sch.Input = input
	require.NoError(t, s.Create(context.Background(), sch))

	input["k"] = "mutated-after-create"

	got, err := s.Get(context.Background(), "sch-1")
	require.NoError(t, err)
	assert.Equal(t, "v", got.Input["k"], "stored input should not reflect caller mutations")
}

// ═══════════════════════════════════════════════════════════════════════════
// Get
// ═══════════════════════════════════════════════════════════════════════════

func TestInMemoryScheduleStore_Get_NotFound(t *testing.T) {
	s := NewInMemoryScheduleStore()
	_, err := s.Get(context.Background(), "missing")
	assert.ErrorIs(t, err, core.ErrScheduleNotFound)
}

func TestInMemoryScheduleStore_Get_ReturnsDeepCopy(t *testing.T) {
	s := NewInMemoryScheduleStore()
	_ = s.Create(context.Background(), makeSchedule("sch-1", "a", time.Now()))

	got1, _ := s.Get(context.Background(), "sch-1")
	got1.Input["k"] = "mutated"

	got2, _ := s.Get(context.Background(), "sch-1")
	assert.Equal(t, "v", got2.Input["k"], "mutating one Get result must not affect subsequent Gets")
}

func TestInMemoryScheduleStore_Get_WithLastRunAt(t *testing.T) {
	s := NewInMemoryScheduleStore()
	lastRun := time.Date(2026, 4, 1, 12, 0, 0, 0, time.UTC)
	sch := makeSchedule("sch-lr", "a", time.Now())
	sch.LastRunAt = &lastRun
	require.NoError(t, s.Create(context.Background(), sch))

	got, err := s.Get(context.Background(), "sch-lr")
	require.NoError(t, err)
	require.NotNil(t, got.LastRunAt)
	assert.True(t, got.LastRunAt.Equal(lastRun))

	// Mutating the copy's LastRunAt should not affect the stored schedule.
	newT := time.Date(2999, 1, 1, 0, 0, 0, 0, time.UTC)
	*got.LastRunAt = newT

	got2, _ := s.Get(context.Background(), "sch-lr")
	assert.True(t, got2.LastRunAt.Equal(lastRun),
		"LastRunAt pointer in a returned copy must be independent of the stored copy")
}

// ═══════════════════════════════════════════════════════════════════════════
// List
// ═══════════════════════════════════════════════════════════════════════════

func TestInMemoryScheduleStore_List_SortedByRunAt(t *testing.T) {
	s := NewInMemoryScheduleStore()
	base := time.Now()
	_ = s.Create(context.Background(), makeSchedule("s3", "a", base.Add(3*time.Hour)))
	_ = s.Create(context.Background(), makeSchedule("s1", "a", base.Add(1*time.Hour)))
	_ = s.Create(context.Background(), makeSchedule("s2", "a", base.Add(2*time.Hour)))

	list, err := s.List(context.Background())
	require.NoError(t, err)
	require.Len(t, list, 3)
	assert.Equal(t, "s1", list[0].ID)
	assert.Equal(t, "s2", list[1].ID)
	assert.Equal(t, "s3", list[2].ID)
}

// ═══════════════════════════════════════════════════════════════════════════
// Update
// ═══════════════════════════════════════════════════════════════════════════

func TestInMemoryScheduleStore_Update_Success(t *testing.T) {
	s := NewInMemoryScheduleStore()
	_ = s.Create(context.Background(), makeSchedule("sch-1", "agent-a", time.Now()))

	got, _ := s.Get(context.Background(), "sch-1")
	got.TargetAgent = "agent-b"
	got.Enabled = false
	require.NoError(t, s.Update(context.Background(), got))

	updated, _ := s.Get(context.Background(), "sch-1")
	assert.Equal(t, "agent-b", updated.TargetAgent)
	assert.False(t, updated.Enabled)
}

func TestInMemoryScheduleStore_Update_NotFound(t *testing.T) {
	s := NewInMemoryScheduleStore()
	err := s.Update(context.Background(), makeSchedule("missing", "a", time.Now()))
	assert.ErrorIs(t, err, core.ErrScheduleNotFound)
}

func TestInMemoryScheduleStore_Update_Nil(t *testing.T) {
	s := NewInMemoryScheduleStore()
	err := s.Update(context.Background(), nil)
	assert.ErrorIs(t, err, errNilSchedule)
}

func TestInMemoryScheduleStore_Update_EmptyID(t *testing.T) {
	s := NewInMemoryScheduleStore()
	err := s.Update(context.Background(), &core.Schedule{TargetAgent: "a"})
	assert.ErrorIs(t, err, errEmptyScheduleID)
}

// ═══════════════════════════════════════════════════════════════════════════
// Delete
// ═══════════════════════════════════════════════════════════════════════════

func TestInMemoryScheduleStore_Delete_Success(t *testing.T) {
	s := NewInMemoryScheduleStore()
	_ = s.Create(context.Background(), makeSchedule("sch-1", "a", time.Now()))
	require.NoError(t, s.Delete(context.Background(), "sch-1"))
	_, err := s.Get(context.Background(), "sch-1")
	assert.ErrorIs(t, err, core.ErrScheduleNotFound)
}

func TestInMemoryScheduleStore_Delete_NotFound(t *testing.T) {
	s := NewInMemoryScheduleStore()
	err := s.Delete(context.Background(), "missing")
	assert.ErrorIs(t, err, core.ErrScheduleNotFound)
}

// ═══════════════════════════════════════════════════════════════════════════
// GetDue
// ═══════════════════════════════════════════════════════════════════════════

func TestInMemoryScheduleStore_GetDue_OnlyDueAndEnabled(t *testing.T) {
	s := NewInMemoryScheduleStore()
	base := time.Now()

	// Past & enabled → due.
	_ = s.Create(context.Background(), makeSchedule("past-enabled", "a", base.Add(-1*time.Hour)))
	// Past & disabled → not due.
	pastDisabled := makeSchedule("past-disabled", "a", base.Add(-1*time.Hour))
	pastDisabled.Enabled = false
	_ = s.Create(context.Background(), pastDisabled)
	// Future & enabled → not due yet.
	_ = s.Create(context.Background(), makeSchedule("future", "a", base.Add(1*time.Hour)))
	// Exactly now & enabled → due (uses !After, so equal is included).
	_ = s.Create(context.Background(), makeSchedule("now", "a", base))

	due, err := s.GetDue(context.Background(), base)
	require.NoError(t, err)

	ids := make([]string, len(due))
	for i, d := range due {
		ids[i] = d.ID
	}
	assert.Contains(t, ids, "past-enabled")
	assert.Contains(t, ids, "now")
	assert.NotContains(t, ids, "past-disabled")
	assert.NotContains(t, ids, "future")
}

func TestInMemoryScheduleStore_GetDue_SortedByRunAt(t *testing.T) {
	s := NewInMemoryScheduleStore()
	base := time.Now()
	_ = s.Create(context.Background(), makeSchedule("s3", "a", base.Add(-1*time.Minute)))
	_ = s.Create(context.Background(), makeSchedule("s1", "a", base.Add(-3*time.Minute)))
	_ = s.Create(context.Background(), makeSchedule("s2", "a", base.Add(-2*time.Minute)))

	due, err := s.GetDue(context.Background(), base)
	require.NoError(t, err)
	require.Len(t, due, 3)
	assert.Equal(t, "s1", due[0].ID)
	assert.Equal(t, "s2", due[1].ID)
	assert.Equal(t, "s3", due[2].ID)
}

// ═══════════════════════════════════════════════════════════════════════════
// cloneSchedule helper
// ═══════════════════════════════════════════════════════════════════════════

func TestCloneSchedule_Nil(t *testing.T) {
	assert.Nil(t, cloneSchedule(nil))
}

func TestCloneSchedule_NilInputAndLastRunAt(t *testing.T) {
	src := &core.Schedule{ID: "s", Input: nil, LastRunAt: nil}
	cp := cloneSchedule(src)
	require.NotNil(t, cp)
	assert.Equal(t, "s", cp.ID)
	assert.Nil(t, cp.Input)
	assert.Nil(t, cp.LastRunAt)
}

// ═══════════════════════════════════════════════════════════════════════════
// Concurrency
// ═══════════════════════════════════════════════════════════════════════════

func TestInMemoryScheduleStore_ConcurrentAccess(t *testing.T) {
	// Sanity check that the store doesn't deadlock under concurrent mixed
	// operations. This test is deliberately loose — it only verifies that
	// the goroutines complete without hanging.
	//
	// Actual data-race detection depends on running the test suite with
	// `go test -race`. CI must include -race for this test to be meaningful.
	// Without -race, a regression introducing a data race would pass this
	// test silently.
	s := NewInMemoryScheduleStore()
	const n = 50

	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			id := fmt.Sprintf("sch-%d", i)
			_ = s.Create(context.Background(), makeSchedule(id, "a", time.Now()))
			_, _ = s.Get(context.Background(), id)
			_, _ = s.List(context.Background())
			_, _ = s.GetDue(context.Background(), time.Now())
			if i%2 == 0 {
				_ = s.Delete(context.Background(), id)
			}
		}(i)
	}
	wg.Wait()
}
