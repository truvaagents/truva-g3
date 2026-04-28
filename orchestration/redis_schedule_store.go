// Package orchestration — RedisScheduleStore implements core.ScheduleStore.
//
// Storage model:
//
//	truvag3:schedules:data:{id}   — JSON string, the canonical schedule record
//	truvag3:schedules:due         — sorted set, score = RunAt.Unix(), member = id
//
// The sorted set is a time-ordered index: GetDue uses ZRangeByScore to
// fetch schedules due at or before "now" in O(log N + M) instead of
// scanning every schedule.
//
// Create, Update, and Delete keep both the data key and the sorted set
// in sync. Disabled schedules are removed from the sorted set (so they
// don't appear in GetDue) but their data key remains so Get/List still
// see them.

package orchestration

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/go-redis/redis/v8"
	"github.com/truvaagents/truva-g3/core"
)

// Compile-time check: RedisScheduleStore satisfies core.ScheduleStore.
var _ core.ScheduleStore = (*RedisScheduleStore)(nil)

const (
	defaultScheduleKeyPrefix = "truvag3:schedules"
	dataKeySuffix            = ":data:"
	dueKeySuffix             = ":due"
)

// RedisScheduleStoreConfig configures a RedisScheduleStore.
type RedisScheduleStoreConfig struct {
	// KeyPrefix is the Redis key namespace for this store.
	// Default: "truvag3:schedules"
	KeyPrefix string

	// Logger for operational logs. Defaults to core.NoOpLogger{} if nil.
	Logger core.Logger
}

// DefaultRedisScheduleStoreConfig returns a config with sensible defaults.
func DefaultRedisScheduleStoreConfig() *RedisScheduleStoreConfig {
	return &RedisScheduleStoreConfig{
		KeyPrefix: defaultScheduleKeyPrefix,
	}
}

// RedisScheduleStore is a Redis-backed implementation of core.ScheduleStore.
//
// Accepts redis.Cmdable (rather than the concrete *redis.Client) so tests
// can inject miniredis clients and production can use *redis.ClusterClient
// transparently — matching the pattern established by memory.RedisDistributedLock.
type RedisScheduleStore struct {
	client redis.Cmdable
	prefix string
	logger core.Logger
}

// NewRedisScheduleStore creates a new Redis-backed schedule store.
// Pass nil config to use defaults.
//
// Returns errNilRedisClient if client is nil — consistent with the error-
// return pattern in memory.NewRedisDistributedLock. The scheduler-tool's
// main.go should propagate this via log.Fatal during startup.
func NewRedisScheduleStore(client redis.Cmdable, config *RedisScheduleStoreConfig) (*RedisScheduleStore, error) {
	if client == nil {
		return nil, errNilRedisClient
	}
	if config == nil {
		config = DefaultRedisScheduleStoreConfig()
	}
	prefix := config.KeyPrefix
	if prefix == "" {
		prefix = defaultScheduleKeyPrefix
	}
	var logger core.Logger = &core.NoOpLogger{}
	if config.Logger != nil {
		logger = config.Logger
	}
	return &RedisScheduleStore{
		client: client,
		prefix: prefix,
		logger: logger,
	}, nil
}

// dataKey returns the Redis key for a schedule's JSON data.
func (s *RedisScheduleStore) dataKey(id string) string {
	return s.prefix + dataKeySuffix + id
}

// dueKey returns the Redis key for the due-index sorted set.
func (s *RedisScheduleStore) dueKey() string {
	return s.prefix + dueKeySuffix
}

// Create persists a new schedule.
//
// Uses SETNX on the data key so duplicate IDs return
// core.ErrScheduleAlreadyExists. If the schedule is Enabled, it is also
// added to the due-index sorted set with score = RunAt.Unix().
func (s *RedisScheduleStore) Create(ctx context.Context, schedule *core.Schedule) error {
	if schedule == nil {
		return errNilSchedule
	}
	if schedule.ID == "" {
		return errEmptyScheduleID
	}

	data, err := json.Marshal(schedule)
	if err != nil {
		return fmt.Errorf("scheduler: failed to marshal schedule: %w", err)
	}

	ok, err := s.client.SetNX(ctx, s.dataKey(schedule.ID), data, 0).Result()
	if err != nil {
		return fmt.Errorf("scheduler: failed to create schedule: %w", err)
	}
	if !ok {
		return core.ErrScheduleAlreadyExists
	}

	if schedule.Enabled {
		if err := s.addToDueIndex(ctx, schedule); err != nil {
			// Best-effort rollback of the data key so the store is
			// consistent. If the rollback itself fails we log and move on —
			// the schedule will still be visible via Get but won't be
			// picked up by GetDue until the next Update.
			_ = s.client.Del(ctx, s.dataKey(schedule.ID)).Err()
			return err
		}
	}

	s.logger.InfoWithContext(ctx, "Schedule created", map[string]interface{}{
		"schedule_id":  schedule.ID,
		"target_agent": schedule.TargetAgent,
		"enabled":      schedule.Enabled,
	})
	return nil
}

// Get retrieves a schedule by ID. Returns core.ErrScheduleNotFound if the
// schedule doesn't exist.
func (s *RedisScheduleStore) Get(ctx context.Context, id string) (*core.Schedule, error) {
	raw, err := s.client.Get(ctx, s.dataKey(id)).Bytes()
	if err != nil {
		if err == redis.Nil {
			return nil, core.ErrScheduleNotFound
		}
		return nil, fmt.Errorf("scheduler: failed to get schedule: %w", err)
	}
	var schedule core.Schedule
	if err := json.Unmarshal(raw, &schedule); err != nil {
		return nil, fmt.Errorf("scheduler: failed to unmarshal schedule: %w", err)
	}
	return &schedule, nil
}

// List returns all schedules under this store's prefix.
//
// Uses SCAN to iterate data keys, then MGET to batch-fetch their JSON.
// For large schedule counts (>10k), consider adding pagination.
func (s *RedisScheduleStore) List(ctx context.Context) ([]*core.Schedule, error) {
	pattern := s.prefix + dataKeySuffix + "*"
	var keys []string
	var cursor uint64

	for {
		batch, nextCursor, err := s.client.Scan(ctx, cursor, pattern, 100).Result()
		if err != nil {
			return nil, fmt.Errorf("scheduler: scan failed: %w", err)
		}
		keys = append(keys, batch...)
		cursor = nextCursor
		if cursor == 0 {
			break
		}
	}

	if len(keys) == 0 {
		return []*core.Schedule{}, nil
	}

	values, err := s.client.MGet(ctx, keys...).Result()
	if err != nil {
		return nil, fmt.Errorf("scheduler: mget failed: %w", err)
	}

	out := make([]*core.Schedule, 0, len(values))
	for _, v := range values {
		if v == nil {
			continue // Race: deleted between SCAN and MGET.
		}
		str, ok := v.(string)
		if !ok {
			continue
		}
		var schedule core.Schedule
		if err := json.Unmarshal([]byte(str), &schedule); err != nil {
			s.logger.WarnWithContext(ctx, "Skipped malformed schedule JSON", map[string]interface{}{
				"error": err.Error(),
			})
			continue
		}
		out = append(out, &schedule)
	}
	return out, nil
}

// Update persists changes to an existing schedule.
//
// Overwrites the data key and syncs the due-index sorted set: if the
// schedule is Enabled, adds/updates its entry with the new RunAt score;
// if disabled, removes it from the due index.
//
// Returns core.ErrScheduleNotFound if the schedule doesn't exist.
func (s *RedisScheduleStore) Update(ctx context.Context, schedule *core.Schedule) error {
	if schedule == nil {
		return errNilSchedule
	}
	if schedule.ID == "" {
		return errEmptyScheduleID
	}

	// Check existence first. We can't use SETXX because we'd lose the
	// pre-check's ErrScheduleNotFound signal if the key doesn't exist.
	exists, err := s.client.Exists(ctx, s.dataKey(schedule.ID)).Result()
	if err != nil {
		return fmt.Errorf("scheduler: exists check failed: %w", err)
	}
	if exists == 0 {
		return core.ErrScheduleNotFound
	}

	data, err := json.Marshal(schedule)
	if err != nil {
		return fmt.Errorf("scheduler: failed to marshal schedule: %w", err)
	}
	if err := s.client.Set(ctx, s.dataKey(schedule.ID), data, 0).Err(); err != nil {
		return fmt.Errorf("scheduler: failed to persist schedule update: %w", err)
	}

	if schedule.Enabled {
		if err := s.addToDueIndex(ctx, schedule); err != nil {
			return err
		}
	} else {
		if err := s.client.ZRem(ctx, s.dueKey(), schedule.ID).Err(); err != nil {
			return fmt.Errorf("scheduler: failed to remove from due index: %w", err)
		}
	}

	s.logger.InfoWithContext(ctx, "Schedule updated", map[string]interface{}{
		"schedule_id": schedule.ID,
		"enabled":     schedule.Enabled,
	})
	return nil
}

// Delete removes a schedule.
//
// Removes both the data key and the entry from the due index. Returns
// core.ErrScheduleNotFound if the data key didn't exist.
func (s *RedisScheduleStore) Delete(ctx context.Context, id string) error {
	deleted, err := s.client.Del(ctx, s.dataKey(id)).Result()
	if err != nil {
		return fmt.Errorf("scheduler: failed to delete schedule: %w", err)
	}
	// Remove from due index regardless — even if the data key is already
	// gone, a stale due-index entry could cause phantom fires.
	_ = s.client.ZRem(ctx, s.dueKey(), id).Err()

	if deleted == 0 {
		return core.ErrScheduleNotFound
	}
	s.logger.InfoWithContext(ctx, "Schedule deleted", map[string]interface{}{
		"schedule_id": id,
	})
	return nil
}

// GetDue returns all enabled schedules where RunAt <= now.
//
// Uses ZRangeByScore on the due-index sorted set to fetch IDs, then MGET
// to batch-fetch their JSON payloads.
//
// Defensive filter: even though disabled schedules shouldn't be in the
// due index, we check Enabled on each returned schedule so a partially-
// synced index doesn't fire disabled schedules.
func (s *RedisScheduleStore) GetDue(ctx context.Context, now time.Time) ([]*core.Schedule, error) {
	ids, err := s.client.ZRangeByScore(ctx, s.dueKey(), &redis.ZRangeBy{
		Min: "0",
		Max: strconv.FormatInt(now.Unix(), 10),
	}).Result()
	if err != nil {
		return nil, fmt.Errorf("scheduler: due-index query failed: %w", err)
	}
	if len(ids) == 0 {
		return []*core.Schedule{}, nil
	}

	// Build the full data keys for MGET.
	keys := make([]string, len(ids))
	for i, id := range ids {
		keys[i] = s.dataKey(id)
	}
	values, err := s.client.MGet(ctx, keys...).Result()
	if err != nil {
		return nil, fmt.Errorf("scheduler: mget failed: %w", err)
	}

	out := make([]*core.Schedule, 0, len(values))
	for _, v := range values {
		if v == nil {
			// Race: the schedule was deleted between ZRANGEBYSCORE and MGET.
			continue
		}
		str, ok := v.(string)
		if !ok {
			continue
		}
		var schedule core.Schedule
		if err := json.Unmarshal([]byte(str), &schedule); err != nil {
			s.logger.WarnWithContext(ctx, "Skipped malformed due schedule JSON", map[string]interface{}{
				"error": err.Error(),
			})
			continue
		}
		// Defensive: skip disabled schedules even if they're in the due index.
		if !schedule.Enabled {
			continue
		}
		out = append(out, &schedule)
	}
	return out, nil
}

// addToDueIndex adds (or updates) a schedule's entry in the due-index
// sorted set, with score = RunAt.Unix().
func (s *RedisScheduleStore) addToDueIndex(ctx context.Context, schedule *core.Schedule) error {
	err := s.client.ZAdd(ctx, s.dueKey(), &redis.Z{
		Score:  float64(schedule.RunAt.Unix()),
		Member: schedule.ID,
	}).Err()
	if err != nil {
		return fmt.Errorf("scheduler: failed to add to due index: %w", err)
	}
	return nil
}

// Package-private validation errors for the scheduling subsystem.
// Inlined from the former scheduler/errors.go during the Phase B fold-in.
var (
	errNilSchedule     = errors.New("scheduler: schedule cannot be nil")
	errEmptyScheduleID = errors.New("scheduler: schedule ID cannot be empty")
	errNilTask         = errors.New("scheduler: task cannot be nil")
	errEmptyTaskID     = errors.New("scheduler: task ID cannot be empty")
	errEmptyQueueName  = errors.New("scheduler: queue name cannot be empty")
	errNilRedisClient  = errors.New("scheduler: redis client is required")
	errQueueFull       = errors.New("scheduler: in-memory queue is full")
)
