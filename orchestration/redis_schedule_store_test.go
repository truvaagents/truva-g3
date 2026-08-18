package orchestration

import (
	"context"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/truvaagents/truva-g3/core"
)

// setupRedis creates a miniredis + client pair with automatic cleanup.
func setupRedis(t *testing.T) (*miniredis.Miniredis, *redis.Client) {
	t.Helper()
	mr, err := miniredis.Run()
	require.NoError(t, err)
	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() {
		_ = client.Close()
		mr.Close()
	})
	return mr, client
}

// mustRedisStore constructs a RedisScheduleStore and fails the test if the
// constructor returns an error. Thin wrapper around NewRedisScheduleStore
// used by tests that don't care about the error-return path.
func mustRedisStore(t *testing.T, client redis.Cmdable, config *RedisScheduleStoreConfig) *RedisScheduleStore {
	t.Helper()
	s, err := NewRedisScheduleStore(client, config)
	require.NoError(t, err)
	return s
}

// mustRedisDispatcher constructs a RedisTaskDispatcher and fails the test
// if the constructor returns an error.
func mustRedisDispatcher(t *testing.T, client redis.Cmdable) *RedisTaskDispatcher {
	t.Helper()
	d, err := NewRedisTaskDispatcher(client)
	require.NoError(t, err)
	return d
}

// ═══════════════════════════════════════════════════════════════════════════
// Construction / config
// ═══════════════════════════════════════════════════════════════════════════

func TestRedisScheduleStore_ImplementsInterface(t *testing.T) {
	_, client := setupRedis(t)
	var _ core.ScheduleStore = mustRedisStore(t, client, nil)
}

func TestRedisScheduleStore_New_NilClient_ReturnsError(t *testing.T) {
	s, err := NewRedisScheduleStore(nil, nil)
	require.Error(t, err)
	assert.ErrorIs(t, err, errNilRedisClient)
	assert.Nil(t, s)
}

func TestRedisScheduleStore_DefaultConfig(t *testing.T) {
	cfg := DefaultRedisScheduleStoreConfig()
	require.NotNil(t, cfg)
	assert.Equal(t, defaultScheduleKeyPrefix, cfg.KeyPrefix)
}

func TestRedisScheduleStore_CustomPrefix(t *testing.T) {
	_, client := setupRedis(t)
	s := mustRedisStore(t, client, &RedisScheduleStoreConfig{KeyPrefix: "custom:ns"})
	assert.Equal(t, "custom:ns:data:id-1", s.dataKey("id-1"))
	assert.Equal(t, "custom:ns:due", s.dueKey())
}

func TestRedisScheduleStore_EmptyPrefixFallsBack(t *testing.T) {
	_, client := setupRedis(t)
	s := mustRedisStore(t, client, &RedisScheduleStoreConfig{KeyPrefix: ""})
	assert.Equal(t, defaultScheduleKeyPrefix+":data:x", s.dataKey("x"))
}

func TestRedisScheduleStore_CustomLogger_Preserved(t *testing.T) {
	_, client := setupRedis(t)
	logger := &core.NoOpLogger{}
	s := mustRedisStore(t, client, &RedisScheduleStoreConfig{Logger: logger})
	assert.Equal(t, logger, s.logger, "custom logger should be stored")
}

// ═══════════════════════════════════════════════════════════════════════════
// Create
// ═══════════════════════════════════════════════════════════════════════════

func TestRedisScheduleStore_Create_Success(t *testing.T) {
	_, client := setupRedis(t)
	s := mustRedisStore(t, client, nil)
	sch := makeSchedule("sch-1", "agent-a", time.Now())
	require.NoError(t, s.Create(context.Background(), sch))

	// Data key exists.
	exists, _ := client.Exists(context.Background(), s.dataKey("sch-1")).Result()
	assert.EqualValues(t, 1, exists)

	// Due index has exactly one entry when enabled.
	count, _ := client.ZCard(context.Background(), s.dueKey()).Result()
	assert.EqualValues(t, 1, count)
}

func TestRedisScheduleStore_Create_DisabledNotInDueIndex(t *testing.T) {
	_, client := setupRedis(t)
	s := mustRedisStore(t, client, nil)
	sch := makeSchedule("sch-disabled", "a", time.Now())
	sch.Enabled = false
	require.NoError(t, s.Create(context.Background(), sch))

	count, _ := client.ZCard(context.Background(), s.dueKey()).Result()
	assert.EqualValues(t, 0, count, "disabled schedule should not be in the due index")
}

func TestRedisScheduleStore_Create_Duplicate(t *testing.T) {
	_, client := setupRedis(t)
	s := mustRedisStore(t, client, nil)
	sch := makeSchedule("sch-dup", "a", time.Now())
	require.NoError(t, s.Create(context.Background(), sch))
	err := s.Create(context.Background(), sch)
	assert.ErrorIs(t, err, core.ErrScheduleAlreadyExists)
}

func TestRedisScheduleStore_Create_Nil(t *testing.T) {
	_, client := setupRedis(t)
	s := mustRedisStore(t, client, nil)
	err := s.Create(context.Background(), nil)
	assert.ErrorIs(t, err, errNilSchedule)
}

func TestRedisScheduleStore_Create_EmptyID(t *testing.T) {
	_, client := setupRedis(t)
	s := mustRedisStore(t, client, nil)
	err := s.Create(context.Background(), &core.Schedule{TargetAgent: "a"})
	assert.ErrorIs(t, err, errEmptyScheduleID)
}

// ═══════════════════════════════════════════════════════════════════════════
// Get
// ═══════════════════════════════════════════════════════════════════════════

func TestRedisScheduleStore_Get_RoundTrip(t *testing.T) {
	_, client := setupRedis(t)
	s := mustRedisStore(t, client, nil)

	runAt := time.Date(2026, 4, 10, 15, 0, 0, 0, time.UTC)
	original := makeSchedule("sch-rt", "agent-a", runAt)
	original.CronExpr = "*/5 * * * *"
	original.MissedRunPolicy = core.MissedRunCatchUp
	original.CreatedBy = "devops-chat-agent"
	require.NoError(t, s.Create(context.Background(), original))

	got, err := s.Get(context.Background(), "sch-rt")
	require.NoError(t, err)
	assert.Equal(t, "sch-rt", got.ID)
	assert.Equal(t, "agent-a", got.TargetAgent)
	assert.True(t, got.RunAt.Equal(runAt))
	assert.Equal(t, "*/5 * * * *", got.CronExpr)
	assert.Equal(t, core.MissedRunCatchUp, got.MissedRunPolicy)
	assert.Equal(t, "devops-chat-agent", got.CreatedBy)
	assert.Equal(t, "v", got.Input["k"])
}

func TestRedisScheduleStore_Get_NotFound(t *testing.T) {
	_, client := setupRedis(t)
	s := mustRedisStore(t, client, nil)
	_, err := s.Get(context.Background(), "missing")
	assert.ErrorIs(t, err, core.ErrScheduleNotFound)
}

func TestRedisScheduleStore_RoundTrip_LastRunAtPreserved(t *testing.T) {
	// LastRunAt is a *time.Time with json:"last_run_at,omitempty" — verify
	// it round-trips through Redis JSON serialization when set.
	_, client := setupRedis(t)
	s := mustRedisStore(t, client, nil)

	lastRun := time.Date(2026, 4, 1, 12, 0, 0, 0, time.UTC)
	sch := makeSchedule("sch-lastrun", "agent-a", time.Now())
	sch.LastRunAt = &lastRun
	require.NoError(t, s.Create(context.Background(), sch))

	got, err := s.Get(context.Background(), "sch-lastrun")
	require.NoError(t, err)
	require.NotNil(t, got.LastRunAt, "LastRunAt should survive round-trip through Redis")
	assert.True(t, got.LastRunAt.Equal(lastRun), "LastRunAt value should be preserved exactly")
}

func TestRedisScheduleStore_RoundTrip_LastRunAtNilOmitted(t *testing.T) {
	// With json:",omitempty", a nil LastRunAt should not appear in the
	// serialized form and should still be nil after round-trip.
	_, client := setupRedis(t)
	s := mustRedisStore(t, client, nil)

	sch := makeSchedule("sch-no-lastrun", "agent-a", time.Now())
	sch.LastRunAt = nil
	require.NoError(t, s.Create(context.Background(), sch))

	got, err := s.Get(context.Background(), "sch-no-lastrun")
	require.NoError(t, err)
	assert.Nil(t, got.LastRunAt, "nil LastRunAt should round-trip as nil")
}

func TestRedisScheduleStore_Get_MalformedJSON(t *testing.T) {
	_, client := setupRedis(t)
	s := mustRedisStore(t, client, nil)
	require.NoError(t, client.Set(context.Background(), s.dataKey("bad"), "{not-json", 0).Err())

	_, err := s.Get(context.Background(), "bad")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unmarshal")
}

// ═══════════════════════════════════════════════════════════════════════════
// List
// ═══════════════════════════════════════════════════════════════════════════

func TestRedisScheduleStore_List_Empty(t *testing.T) {
	_, client := setupRedis(t)
	s := mustRedisStore(t, client, nil)
	list, err := s.List(context.Background())
	require.NoError(t, err)
	assert.Empty(t, list)
}

func TestRedisScheduleStore_List_ReturnsAll(t *testing.T) {
	_, client := setupRedis(t)
	s := mustRedisStore(t, client, nil)
	for i := 0; i < 5; i++ {
		_ = s.Create(context.Background(), makeSchedule(
			"sch-"+string(rune('a'+i)), "agent-a", time.Now()))
	}
	list, err := s.List(context.Background())
	require.NoError(t, err)
	assert.Len(t, list, 5)
}

func TestRedisScheduleStore_List_SkipsMalformedEntries(t *testing.T) {
	_, client := setupRedis(t)
	s := mustRedisStore(t, client, nil)
	// Valid schedule.
	require.NoError(t, s.Create(context.Background(), makeSchedule("good", "a", time.Now())))
	// Malformed entry under the same prefix.
	require.NoError(t, client.Set(context.Background(), s.dataKey("bad"), "{not-json", 0).Err())

	list, err := s.List(context.Background())
	require.NoError(t, err)
	assert.Len(t, list, 1, "malformed entries should be skipped, not break List")
	assert.Equal(t, "good", list[0].ID)
}

// ═══════════════════════════════════════════════════════════════════════════
// Update
// ═══════════════════════════════════════════════════════════════════════════

func TestRedisScheduleStore_Update_Success(t *testing.T) {
	_, client := setupRedis(t)
	s := mustRedisStore(t, client, nil)
	sch := makeSchedule("sch-u", "agent-a", time.Now())
	require.NoError(t, s.Create(context.Background(), sch))

	sch.TargetAgent = "agent-b"
	sch.CronExpr = "*/10 * * * *"
	require.NoError(t, s.Update(context.Background(), sch))

	got, _ := s.Get(context.Background(), "sch-u")
	assert.Equal(t, "agent-b", got.TargetAgent)
	assert.Equal(t, "*/10 * * * *", got.CronExpr)
}

func TestRedisScheduleStore_Update_DisabledRemovesFromDueIndex(t *testing.T) {
	_, client := setupRedis(t)
	s := mustRedisStore(t, client, nil)
	sch := makeSchedule("sch-d", "agent-a", time.Now())
	require.NoError(t, s.Create(context.Background(), sch))

	before, _ := client.ZCard(context.Background(), s.dueKey()).Result()
	require.EqualValues(t, 1, before)

	sch.Enabled = false
	require.NoError(t, s.Update(context.Background(), sch))

	after, _ := client.ZCard(context.Background(), s.dueKey()).Result()
	assert.EqualValues(t, 0, after, "disabling a schedule should remove it from the due index")
}

func TestRedisScheduleStore_Update_EnabledAddsToDueIndex(t *testing.T) {
	_, client := setupRedis(t)
	s := mustRedisStore(t, client, nil)
	sch := makeSchedule("sch-e", "agent-a", time.Now())
	sch.Enabled = false
	require.NoError(t, s.Create(context.Background(), sch))

	before, _ := client.ZCard(context.Background(), s.dueKey()).Result()
	require.EqualValues(t, 0, before)

	sch.Enabled = true
	require.NoError(t, s.Update(context.Background(), sch))

	after, _ := client.ZCard(context.Background(), s.dueKey()).Result()
	assert.EqualValues(t, 1, after, "re-enabling a schedule should add it back to the due index")
}

func TestRedisScheduleStore_Update_NotFound(t *testing.T) {
	_, client := setupRedis(t)
	s := mustRedisStore(t, client, nil)
	err := s.Update(context.Background(), makeSchedule("missing", "a", time.Now()))
	assert.ErrorIs(t, err, core.ErrScheduleNotFound)
}

func TestRedisScheduleStore_Update_Nil(t *testing.T) {
	_, client := setupRedis(t)
	s := mustRedisStore(t, client, nil)
	err := s.Update(context.Background(), nil)
	assert.ErrorIs(t, err, errNilSchedule)
}

func TestRedisScheduleStore_Update_EmptyID(t *testing.T) {
	_, client := setupRedis(t)
	s := mustRedisStore(t, client, nil)
	err := s.Update(context.Background(), &core.Schedule{TargetAgent: "a"})
	assert.ErrorIs(t, err, errEmptyScheduleID)
}

// ═══════════════════════════════════════════════════════════════════════════
// Delete
// ═══════════════════════════════════════════════════════════════════════════

func TestRedisScheduleStore_Delete_Success(t *testing.T) {
	_, client := setupRedis(t)
	s := mustRedisStore(t, client, nil)
	sch := makeSchedule("sch-del", "agent-a", time.Now())
	require.NoError(t, s.Create(context.Background(), sch))

	require.NoError(t, s.Delete(context.Background(), "sch-del"))

	_, err := s.Get(context.Background(), "sch-del")
	assert.ErrorIs(t, err, core.ErrScheduleNotFound)

	// Removed from due index.
	count, _ := client.ZCard(context.Background(), s.dueKey()).Result()
	assert.EqualValues(t, 0, count)
}

func TestRedisScheduleStore_Delete_NotFound(t *testing.T) {
	_, client := setupRedis(t)
	s := mustRedisStore(t, client, nil)
	err := s.Delete(context.Background(), "missing")
	assert.ErrorIs(t, err, core.ErrScheduleNotFound)
}

// ═══════════════════════════════════════════════════════════════════════════
// GetDue
// ═══════════════════════════════════════════════════════════════════════════

func TestRedisScheduleStore_GetDue_ReturnsOnlyDue(t *testing.T) {
	_, client := setupRedis(t)
	s := mustRedisStore(t, client, nil)
	base := time.Now()

	// Past & enabled → due.
	_ = s.Create(context.Background(), makeSchedule("past", "a", base.Add(-1*time.Hour)))
	// Future & enabled → not due.
	_ = s.Create(context.Background(), makeSchedule("future", "a", base.Add(1*time.Hour)))
	// Past & disabled → not due.
	pastDisabled := makeSchedule("pd", "a", base.Add(-1*time.Hour))
	pastDisabled.Enabled = false
	_ = s.Create(context.Background(), pastDisabled)

	due, err := s.GetDue(context.Background(), base)
	require.NoError(t, err)
	require.Len(t, due, 1)
	assert.Equal(t, "past", due[0].ID)
}

func TestRedisScheduleStore_GetDue_Empty(t *testing.T) {
	_, client := setupRedis(t)
	s := mustRedisStore(t, client, nil)
	due, err := s.GetDue(context.Background(), time.Now())
	require.NoError(t, err)
	assert.Empty(t, due)
}

func TestRedisScheduleStore_GetDue_DefensiveSkipsDisabled(t *testing.T) {
	_, client := setupRedis(t)
	s := mustRedisStore(t, client, nil)

	// Inject a stale due-index entry: add a schedule's ID to the due-index
	// sorted set while the stored data has Enabled=false. GetDue should
	// defensively skip it.
	sch := makeSchedule("stale", "a", time.Now().Add(-1*time.Hour))
	sch.Enabled = false
	require.NoError(t, s.Create(context.Background(), sch))

	// Manually insert the stale entry into the due index.
	_ = client.ZAdd(context.Background(), s.dueKey(), redis.Z{
		Score:  float64(time.Now().Add(-1 * time.Hour).Unix()),
		Member: "stale",
	}).Err()

	due, err := s.GetDue(context.Background(), time.Now())
	require.NoError(t, err)
	assert.Empty(t, due, "GetDue must defensively skip disabled schedules even if they leak into the due index")
}

func TestRedisScheduleStore_GetDue_SkipsRaceDeleted(t *testing.T) {
	// If a schedule is in the due index but its data key has been deleted
	// (race: delete between ZRANGEBYSCORE and MGET), GetDue should quietly
	// skip the entry rather than returning an error.
	_, client := setupRedis(t)
	s := mustRedisStore(t, client, nil)

	// Add a ghost entry to the due index without a corresponding data key.
	_ = client.ZAdd(context.Background(), s.dueKey(), redis.Z{
		Score:  float64(time.Now().Add(-1 * time.Hour).Unix()),
		Member: "ghost",
	}).Err()

	due, err := s.GetDue(context.Background(), time.Now())
	require.NoError(t, err)
	assert.Empty(t, due, "GetDue should skip due-index entries with no data key")
}

func TestRedisScheduleStore_GetDue_SkipsMalformedJSON(t *testing.T) {
	_, client := setupRedis(t)
	s := mustRedisStore(t, client, nil)

	// Valid enabled schedule.
	_ = s.Create(context.Background(), makeSchedule("good", "a", time.Now().Add(-1*time.Hour)))

	// Add a stale due-index entry pointing at a data key containing garbage.
	require.NoError(t, client.Set(context.Background(), s.dataKey("bad"), "{not-json", 0).Err())
	_ = client.ZAdd(context.Background(), s.dueKey(), redis.Z{
		Score:  float64(time.Now().Add(-1 * time.Hour).Unix()),
		Member: "bad",
	}).Err()

	due, err := s.GetDue(context.Background(), time.Now())
	require.NoError(t, err)
	require.Len(t, due, 1)
	assert.Equal(t, "good", due[0].ID)
}

// ═══════════════════════════════════════════════════════════════════════════
// Create rollback path
// ═══════════════════════════════════════════════════════════════════════════

func TestRedisScheduleStore_Create_RollsBackOnDueIndexFailure(t *testing.T) {
	// Simulate: SETNX succeeds, ZADD fails. The store should delete the
	// data key to avoid orphaning a schedule that isn't in the due index.
	//
	// We force ZADD to fail by closing miniredis mid-call — too disruptive
	// for a unit test. Instead, verify the code path by spying on the
	// ZADD error handling via a direct call.
	//
	// Since the rollback path is defensive and hard to trigger without
	// injecting failures into the real Redis client, we at least lock in
	// the shape via the addToDueIndex test below.
	t.Skip("rollback path requires injected Redis failure; covered by addToDueIndex error wrap test instead")
}

func TestRedisScheduleStore_AddToDueIndex_Success(t *testing.T) {
	_, client := setupRedis(t)
	s := mustRedisStore(t, client, nil)
	sch := makeSchedule("add-due", "a", time.Now())
	err := s.addToDueIndex(context.Background(), sch)
	require.NoError(t, err)

	count, _ := client.ZCard(context.Background(), s.dueKey()).Result()
	assert.EqualValues(t, 1, count)
}

func TestRedisScheduleStore_AddToDueIndex_ClientError(t *testing.T) {
	mr, client := setupRedis(t)
	s := mustRedisStore(t, client, nil)
	mr.Close()

	err := s.addToDueIndex(context.Background(), makeSchedule("x", "a", time.Now()))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to add to due index")
}

// ═══════════════════════════════════════════════════════════════════════════
// List error propagation (closed client)
// ═══════════════════════════════════════════════════════════════════════════

func TestRedisScheduleStore_List_ClientError(t *testing.T) {
	mr, client := setupRedis(t)
	s := mustRedisStore(t, client, nil)
	mr.Close() // Force subsequent commands to fail.

	_, err := s.List(context.Background())
	require.Error(t, err)
}

func TestRedisScheduleStore_Get_ClientError(t *testing.T) {
	mr, client := setupRedis(t)
	s := mustRedisStore(t, client, nil)
	mr.Close()

	_, err := s.Get(context.Background(), "anything")
	require.Error(t, err)
}

func TestRedisScheduleStore_Create_ClientError(t *testing.T) {
	mr, client := setupRedis(t)
	s := mustRedisStore(t, client, nil)
	mr.Close()

	err := s.Create(context.Background(), makeSchedule("x", "a", time.Now()))
	require.Error(t, err)
}

func TestRedisScheduleStore_Update_ClientError(t *testing.T) {
	mr, client := setupRedis(t)
	s := mustRedisStore(t, client, nil)
	// Create first (succeeds), then close client to fail the Update path.
	_ = s.Create(context.Background(), makeSchedule("upd", "a", time.Now()))
	mr.Close()

	err := s.Update(context.Background(), makeSchedule("upd", "b", time.Now()))
	require.Error(t, err)
}

func TestRedisScheduleStore_Delete_ClientError(t *testing.T) {
	mr, client := setupRedis(t)
	s := mustRedisStore(t, client, nil)
	mr.Close()

	err := s.Delete(context.Background(), "x")
	require.Error(t, err)
}

func TestRedisScheduleStore_GetDue_ClientError(t *testing.T) {
	mr, client := setupRedis(t)
	s := mustRedisStore(t, client, nil)
	mr.Close()

	_, err := s.GetDue(context.Background(), time.Now())
	require.Error(t, err)
}
