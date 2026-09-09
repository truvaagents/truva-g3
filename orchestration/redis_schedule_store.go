// Package orchestration — RedisScheduleStore implements core.ScheduleStore.
//
// Storage model:
//
//	truvag3:v1:<deployment>:{schedules:<deployment>}:data:<id> — canonical record
//	truvag3:v1:<deployment>:{schedules:<deployment>}:index:all — bounded catalog
//	truvag3:v1:<deployment>:{schedules:<deployment>}:index:due — due-time index
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
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"
	"github.com/truvaagents/truva-g3/core"
)

// Compile-time check: RedisScheduleStore satisfies core.ScheduleStore.
var _ core.ScheduleStore = (*RedisScheduleStore)(nil)

const (
	defaultMaxSchedules = 10_000
)

// RedisScheduleStoreConfig configures a RedisScheduleStore.
type RedisScheduleStoreConfig struct {
	// Keyspace selects the versioned, tagged DB-0 schedule schema.
	Keyspace *core.RedisKeyspace

	// MaxSchedules bounds the control-plane catalog enumerated by List.
	MaxSchedules int

	// Logger for operational logs. Defaults to core.NoOpLogger{} if nil.
	Logger core.Logger
}

// DefaultRedisScheduleStoreConfig returns a config with sensible defaults.
func DefaultRedisScheduleStoreConfig() *RedisScheduleStoreConfig {
	return &RedisScheduleStoreConfig{
		MaxSchedules: defaultMaxSchedules,
	}
}

// RedisScheduleStore is a Redis-backed implementation of core.ScheduleStore.
//
// Accepts redis.UniversalClient (rather than the concrete *redis.Client) so tests
// can inject miniredis clients and production can use *redis.ClusterClient
// transparently — matching the pattern established by memory.RedisDistributedLock.
type RedisScheduleStore struct {
	client       redis.UniversalClient
	keyspace     core.RedisKeyspace
	maxSchedules int
	logger       core.Logger
}

// NewRedisScheduleStore creates a new Redis-backed schedule store.
// Pass nil config to use defaults.
//
// Returns errNilRedisClient if client is nil — consistent with the error-
// return pattern in memory.NewRedisDistributedLock. The scheduler-tool's
// main.go should propagate this via log.Fatal during startup.
func NewRedisScheduleStore(client redis.UniversalClient, config *RedisScheduleStoreConfig) (*RedisScheduleStore, error) {
	if client == nil {
		return nil, errNilRedisClient
	}
	configProvided := config != nil
	if config == nil {
		config = DefaultRedisScheduleStoreConfig()
	}
	explicitMaxSchedules := config.MaxSchedules
	if !configProvided {
		explicitMaxSchedules = 0
	}
	maxSchedules, err := resolveMaxSchedules(explicitMaxSchedules)
	if err != nil {
		return nil, err
	}
	keyspace := defaultRedisKeyspace()
	if config.Keyspace != nil {
		keyspace = *config.Keyspace
	}
	var logger core.Logger = &core.NoOpLogger{}
	if config.Logger != nil {
		logger = config.Logger
	}
	logger = orchestrationComponentLogger(logger)
	return &RedisScheduleStore{
		client: client, keyspace: keyspace,
		maxSchedules: maxSchedules, logger: logger,
	}, nil
}

func resolveMaxSchedules(explicit int) (int, error) {
	if explicit > 0 {
		return explicit, nil
	}
	if raw, ok := os.LookupEnv("TRUVAG3_SCHEDULER_MAX_SCHEDULES"); ok {
		value, err := strconv.Atoi(strings.TrimSpace(raw))
		if err != nil || value <= 0 {
			return 0, fmt.Errorf("TRUVAG3_SCHEDULER_MAX_SCHEDULES must be a positive integer: %w", core.ErrInvalidConfiguration)
		}
		return value, nil
	}
	return defaultMaxSchedules, nil
}

// dataKey returns the Redis key for a schedule's JSON data.
func (s *RedisScheduleStore) dataKey(id string) string {
	return s.keyspace.Tagged("schedules", "", "data", id)
}

// dueKey returns the Redis key for the due-index sorted set.
func (s *RedisScheduleStore) dueKey() string {
	return s.keyspace.Tagged("schedules", "", "index", "due")
}

func (s *RedisScheduleStore) allKey() string {
	return s.keyspace.Tagged("schedules", "", "index", "all")
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

	err = s.watchSchedule(ctx, []string{s.dataKey(schedule.ID), s.allKey()}, func(tx *redis.Tx) error {
		exists, err := tx.Exists(ctx, s.dataKey(schedule.ID)).Result()
		if err != nil {
			return err
		}
		if exists != 0 {
			return core.ErrScheduleAlreadyExists
		}
		count, err := tx.SCard(ctx, s.allKey()).Result()
		if err != nil {
			return err
		}
		if count >= int64(s.maxSchedules) {
			return core.ErrCapacityExceeded
		}
		_, err = tx.TxPipelined(ctx, func(pipe redis.Pipeliner) error {
			pipe.Set(ctx, s.dataKey(schedule.ID), data, 0)
			pipe.SAdd(ctx, s.allKey(), schedule.ID)
			if schedule.Enabled {
				pipe.ZAdd(ctx, s.dueKey(), redis.Z{Score: float64(schedule.RunAt.Unix()), Member: schedule.ID})
			} else {
				pipe.ZRem(ctx, s.dueKey(), schedule.ID)
			}
			return nil
		})
		return err
	})
	if err != nil {
		return fmt.Errorf("scheduler: failed to create schedule: %w", err)
	}

	s.logger.InfoWithContext(ctx, "Schedule created", map[string]interface{}{
		"operation":    "schedule_create",
		"request_id":   core.GetRequestID(ctx),
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
func (s *RedisScheduleStore) List(ctx context.Context) ([]*core.Schedule, error) {
	ids, err := s.client.SMembers(ctx, s.allKey()).Result()
	if err != nil {
		return nil, fmt.Errorf("scheduler: list index failed: %w", err)
	}
	return s.loadSchedules(ctx, ids, false)
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

	data, err := json.Marshal(schedule)
	if err != nil {
		return fmt.Errorf("scheduler: failed to marshal schedule: %w", err)
	}
	err = s.watchSchedule(ctx, []string{s.dataKey(schedule.ID), s.allKey()}, func(tx *redis.Tx) error {
		exists, err := tx.Exists(ctx, s.dataKey(schedule.ID)).Result()
		if err != nil {
			return err
		}
		if exists == 0 {
			return core.ErrScheduleNotFound
		}
		_, err = tx.TxPipelined(ctx, func(pipe redis.Pipeliner) error {
			pipe.Set(ctx, s.dataKey(schedule.ID), data, 0)
			pipe.SAdd(ctx, s.allKey(), schedule.ID)
			if schedule.Enabled {
				pipe.ZAdd(ctx, s.dueKey(), redis.Z{Score: float64(schedule.RunAt.Unix()), Member: schedule.ID})
			} else {
				pipe.ZRem(ctx, s.dueKey(), schedule.ID)
			}
			return nil
		})
		return err
	})
	if err != nil {
		return fmt.Errorf("scheduler: failed to persist schedule update: %w", err)
	}

	s.logger.InfoWithContext(ctx, "Schedule updated", map[string]interface{}{
		"operation":   "schedule_update",
		"request_id":  core.GetRequestID(ctx),
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
	err := s.watchSchedule(ctx, []string{s.dataKey(id), s.allKey()}, func(tx *redis.Tx) error {
		exists, err := tx.Exists(ctx, s.dataKey(id)).Result()
		if err != nil {
			return err
		}
		if exists == 0 {
			return core.ErrScheduleNotFound
		}
		_, err = tx.TxPipelined(ctx, func(pipe redis.Pipeliner) error {
			pipe.Del(ctx, s.dataKey(id))
			pipe.SRem(ctx, s.allKey(), id)
			pipe.ZRem(ctx, s.dueKey(), id)
			return nil
		})
		return err
	})
	if err != nil {
		return fmt.Errorf("scheduler: failed to delete schedule: %w", err)
	}
	s.logger.InfoWithContext(ctx, "Schedule deleted", map[string]interface{}{
		"operation":   "schedule_delete",
		"request_id":  core.GetRequestID(ctx),
		"schedule_id": id,
	})
	return nil
}

// GetDue returns all enabled schedules where RunAt <= now.
//
// Uses ZRangeByScore on the due-index sorted set to fetch IDs, then a pipeline
// of single-key GETs to hydrate their JSON payloads.
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

	return s.loadSchedules(ctx, ids, true)
}

func (s *RedisScheduleStore) watchSchedule(
	ctx context.Context,
	keys []string,
	operation func(*redis.Tx) error,
) error {
	for attempt := 0; attempt < 3; attempt++ {
		err := s.client.Watch(ctx, operation, keys...)
		if !errors.Is(err, redis.TxFailedErr) {
			return err
		}
	}
	return redis.TxFailedErr
}

func (s *RedisScheduleStore) loadSchedules(
	ctx context.Context,
	ids []string,
	dueOnly bool,
) ([]*core.Schedule, error) {
	if len(ids) == 0 {
		return []*core.Schedule{}, nil
	}
	pipe := s.client.Pipeline()
	commands := make([]*redis.StringCmd, len(ids))
	for i, id := range ids {
		commands[i] = pipe.Get(ctx, s.dataKey(id))
	}
	_, execErr := pipe.Exec(ctx)
	if execErr != nil && !errors.Is(execErr, redis.Nil) {
		return nil, fmt.Errorf("scheduler: load schedules failed: %w", execErr)
	}
	out := make([]*core.Schedule, 0, len(ids))
	stale := make([]interface{}, 0)
	for i, command := range commands {
		raw, err := command.Bytes()
		if errors.Is(err, redis.Nil) {
			stale = append(stale, ids[i])
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("scheduler: load schedule %q failed: %w", ids[i], err)
		}
		var schedule core.Schedule
		if err := json.Unmarshal(raw, &schedule); err != nil {
			if s.logger != nil {
				s.logger.WarnWithContext(ctx, "Skipped malformed schedule JSON", map[string]interface{}{
					"operation":   "schedule_load",
					"request_id":  core.GetRequestID(ctx),
					"schedule_id": ids[i],
					"error":       "stored schedule is malformed",
					"error_type":  "unmarshal",
				})
			}
			continue
		}
		if dueOnly && !schedule.Enabled {
			_ = s.client.ZRem(ctx, s.dueKey(), ids[i]).Err()
			continue
		}
		out = append(out, &schedule)
	}
	if len(stale) > 0 {
		cleanup := s.client.Pipeline()
		cleanup.SRem(ctx, s.allKey(), stale...)
		cleanup.ZRem(ctx, s.dueKey(), stale...)
		if _, err := cleanup.Exec(ctx); err != nil && s.logger != nil {
			s.logger.WarnWithContext(ctx, "Failed to prune stale schedule indexes", map[string]interface{}{
				"operation":   "schedule_index_cleanup",
				"request_id":  core.GetRequestID(ctx),
				"error":       "redis schedule index cleanup failed",
				"error_type":  "index_write",
				"stale_count": len(stale),
			})
		}
	}
	return out, nil
}

func (s *RedisScheduleStore) addToDueIndex(ctx context.Context, schedule *core.Schedule) error {
	if err := s.client.ZAdd(ctx, s.dueKey(), redis.Z{
		Score: float64(schedule.RunAt.Unix()), Member: schedule.ID,
	}).Err(); err != nil {
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
