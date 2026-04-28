// Package orchestration — Scheduler component for delayed and recurring tasks.
//
// The Scheduler is a background component that promotes due schedules into
// the existing task queue system. It implements core.Runnable so the
// framework manages its lifecycle via framework.RegisterRunnable().
//
// This file imports only core, telemetry, and the cron-expression parser.
// All vendor-specific implementations of ScheduleStore, TaskDispatcher, and
// DistributedLock live in peer modules (scheduler/, memory/) or in the
// application's own package.
//
// Architecture:
//
//   tick loop (every TickInterval):
//     1. Acquire distributed lock — if not leader, skip this tick
//     2. ScheduleStore.GetDue(now) — fetch schedules ready to fire
//     3. For each due schedule:
//        a. Fire once or fire-catchup (depending on MissedRunPolicy)
//        b. Advance RunAt (recurring) or delete schedule (one-shot)
//     4. Emit telemetry for this tick
//
// Idempotency:
//   Task IDs are deterministic: "{schedule.ID}:{fireTime.Unix()}".
//   TaskStore.Create wraps core.ErrTaskAlreadyExists on duplicate — the
//   Scheduler detects this via errors.Is() and safely skips dispatch.
//   This makes promotion idempotent against leader failover, clock drift,
//   and brief split-brain windows.

package orchestration

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/google/uuid"
	"github.com/truvaagents/truva-g3/core"
	"github.com/truvaagents/truva-g3/telemetry"
	"github.com/robfig/cron/v3"
)

// Default timing for the Scheduler tick loop and leader lock TTL.
//
// These can be overridden via environment variables:
//   - TRUVAG3_SCHEDULER_TICK_INTERVAL
//   - TRUVAG3_SCHEDULER_LOCK_TTL
//
// Or via the SchedulerDeps fields (TickInterval, LockTTL), which take
// precedence over env vars per the framework's config precedence rules.
const (
	defaultSchedulerTickInterval = 5 * time.Second
	defaultSchedulerLockTTL      = 30 * time.Second
	schedulerLockKey             = "truvag3:scheduler"
)

// Environment variable names for Scheduler numeric tuning.
// Behavioural plugs (store, dispatcher, lock) are configured via SchedulerDeps.
const (
	envSchedulerTickInterval = "TRUVAG3_SCHEDULER_TICK_INTERVAL"
	envSchedulerLockTTL      = "TRUVAG3_SCHEDULER_LOCK_TTL"
)

// SchedulerDeps is the dependency injection struct for NewScheduler.
//
// Per FRAMEWORK_DESIGN_PRINCIPLES.md §4 (Composition over Bundling), every
// dependency is exposed explicitly — no hidden bundling. The application
// is responsible for wiring concrete implementations (typically from the
// scheduler/ and memory/ peer modules, or from the application's own package
// for custom backends).
type SchedulerDeps struct {
	// ScheduleStore persists schedule definitions. Required.
	ScheduleStore core.ScheduleStore

	// TaskDispatcher routes materialized tasks to their target queue. Required.
	TaskDispatcher core.TaskDispatcher

	// TaskStore is used for idempotent task creation via deterministic IDs.
	// Reuses the existing core.TaskStore interface. Required.
	TaskStore core.TaskStore

	// Lock provides distributed leader election so only one Scheduler instance
	// promotes schedules at any given time. Reuses the existing
	// core.DistributedLock interface — implementations include
	// memory.RedisDistributedLock (production) and core.NoOpDistributedLock
	// (single-instance dev). Required.
	Lock core.DistributedLock

	// Logger for structured logs. Defaults to core.NoOpLogger{} if nil.
	Logger core.Logger

	// TickInterval controls how often the Scheduler checks for due schedules.
	// Default: read from TRUVAG3_SCHEDULER_TICK_INTERVAL env var, then
	// defaultSchedulerTickInterval (5s).
	TickInterval time.Duration

	// LockTTL controls how long the distributed lock is held per acquisition.
	// Must be > TickInterval. Default: read from TRUVAG3_SCHEDULER_LOCK_TTL env
	// var, then defaultSchedulerLockTTL (30s).
	LockTTL time.Duration
}

// Scheduler is a background component that promotes due schedules into the
// task queue system. Implements core.Runnable — registered with the framework
// via framework.RegisterRunnable(scheduler).
type Scheduler struct {
	deps       SchedulerDeps
	cronParser cron.Parser
	instanceID string
}

// Compile-time check: Scheduler implements core.Runnable.
var _ core.Runnable = (*Scheduler)(nil)

// NewScheduler validates dependencies and returns a ready-to-run Scheduler.
// Returns an error if any required dependency is nil or if timing values
// from env vars are malformed.
//
// The Scheduler does not start running until Start(ctx) is called, typically
// via framework.RegisterRunnable() + framework.Run().
func NewScheduler(deps SchedulerDeps) (*Scheduler, error) {
	if deps.ScheduleStore == nil {
		return nil, fmt.Errorf("scheduler: ScheduleStore is required")
	}
	if deps.TaskDispatcher == nil {
		return nil, fmt.Errorf("scheduler: TaskDispatcher is required")
	}
	if deps.TaskStore == nil {
		return nil, fmt.Errorf("scheduler: TaskStore is required")
	}
	if deps.Lock == nil {
		return nil, fmt.Errorf("scheduler: Lock is required")
	}

	// Apply defaults for optional fields.
	if deps.Logger == nil {
		deps.Logger = &core.NoOpLogger{}
	}
	if deps.TickInterval <= 0 {
		deps.TickInterval = resolveDurationEnv(envSchedulerTickInterval, defaultSchedulerTickInterval)
	}
	if deps.LockTTL <= 0 {
		deps.LockTTL = resolveDurationEnv(envSchedulerLockTTL, defaultSchedulerLockTTL)
	}
	if deps.LockTTL <= deps.TickInterval {
		return nil, fmt.Errorf("scheduler: LockTTL (%s) must be greater than TickInterval (%s)",
			deps.LockTTL, deps.TickInterval)
	}

	// Standard 5-field cron syntax: minute | hour | day-of-month | month | day-of-week
	cronParser := cron.NewParser(cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow)

	// Instance ID for structured logs so operators can identify which pod
	// is running this Scheduler. core.DistributedLock does not accept a
	// value parameter — the lock implementation owns its own identity — so
	// this ID is purely for observability.
	instanceID := buildInstanceID()

	return &Scheduler{
		deps:       deps,
		cronParser: cronParser,
		instanceID: instanceID,
	}, nil
}

// Start implements core.Runnable. Blocks until ctx is cancelled.
//
// Per FRAMEWORK_DESIGN_PRINCIPLES.md §3.4, the Runnable contract is a single
// blocking Start(ctx) — no companion Stop() method, ctx cancellation drives
// shutdown.
func (s *Scheduler) Start(ctx context.Context) error {
	s.deps.Logger.Info("Scheduler started", map[string]interface{}{
		"operation":     "scheduler_start",
		"instance_id":   s.instanceID,
		"tick_interval": s.deps.TickInterval.String(),
		"lock_ttl":      s.deps.LockTTL.String(),
	})

	ticker := time.NewTicker(s.deps.TickInterval)
	defer ticker.Stop()

	// Release the lock on shutdown so the next instance can acquire quickly
	// instead of waiting for TTL to expire. Use a short background context
	// because the outer ctx is already cancelled.
	defer func() {
		releaseCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		if err := s.deps.Lock.Release(releaseCtx, schedulerLockKey); err != nil {
			s.deps.Logger.Warn("Scheduler lock release failed", map[string]interface{}{
				"operation":   "scheduler_stop",
				"instance_id": s.instanceID,
				"error":       err.Error(),
				"error_type":  "lock_release",
			})
		}
	}()

	for {
		select {
		case <-ctx.Done():
			s.deps.Logger.Info("Scheduler shutting down", map[string]interface{}{
				"operation":   "scheduler_stop",
				"instance_id": s.instanceID,
			})
			return nil
		case <-ticker.C:
			// Wrap the tick in a root span so fireOnce's downstream
			// GetTraceContext sees a valid trace, enabling both (a) the
			// pre-existing EmitSchedulerTick / EmitTaskPromoted span events
			// to actually record, and (b) task.TraceID / task.ParentSpanID
			// population for the executor's linked-span creation.
			tickCtx, endTickSpan := telemetry.StartLinkedSpan(
				ctx,
				"scheduler.tick",
				"", // no parent — this IS the root of the scheduled-execution trace
				"",
				map[string]string{
					"scheduler.instance_id": s.instanceID,
				},
			)
			s.tick(tickCtx)
			endTickSpan()
		}
	}
}

// tick runs one Scheduler cycle. Errors are logged and contained — a tick
// error never crashes the loop (per framework error-handling principle §2).
func (s *Scheduler) tick(ctx context.Context) {
	start := time.Now()

	acquired, err := s.deps.Lock.Acquire(ctx, schedulerLockKey, s.deps.LockTTL)
	if err != nil {
		s.deps.Logger.Warn("Scheduler lock acquire failed", map[string]interface{}{
			"operation":   "scheduler_tick",
			"instance_id": s.instanceID,
			"error":       err.Error(),
			"error_type":  "lock_acquire",
		})
		EmitSchedulerTick(ctx, "error", float64(time.Since(start).Milliseconds()))
		return
	}
	if !acquired {
		EmitSchedulerTick(ctx, "standby", float64(time.Since(start).Milliseconds()))
		return
	}

	due, err := s.deps.ScheduleStore.GetDue(ctx, time.Now())
	if err != nil {
		s.deps.Logger.Error("Scheduler GetDue failed", map[string]interface{}{
			"operation":   "scheduler_tick",
			"instance_id": s.instanceID,
			"error":       err.Error(),
			"error_type":  "schedule_store_read",
		})
		EmitSchedulerTick(ctx, "error", float64(time.Since(start).Milliseconds()))
		return
	}

	for _, schedule := range due {
		if schedule.MissedRunPolicy == core.MissedRunCatchUp {
			s.fireCatchUp(ctx, schedule)
		} else {
			s.fireOnce(ctx, schedule, schedule.RunAt)
		}
		s.advanceOrDelete(ctx, schedule)
	}

	EmitSchedulerTick(ctx, "leader", float64(time.Since(start).Milliseconds()))
}

// fireOnce materializes and dispatches a single task for the given schedule
// and fire time.
//
// The task ID is deterministic: "{schedule.ID}:{fireTime.Unix()}" — which
// makes TaskStore.Create idempotent. If a previous Scheduler leader already
// created this task (e.g., mid-failover), Create returns a wrapped
// core.ErrTaskAlreadyExists and we safely skip dispatch.
func (s *Scheduler) fireOnce(ctx context.Context, schedule *core.Schedule, fireTime time.Time) {
	taskID := fmt.Sprintf("%s:%d", schedule.ID, fireTime.Unix())

	// Shallow-copy the input map so downstream handlers cannot mutate the
	// schedule's persisted payload via a shared reference. Safe against any
	// TaskDispatcher implementation (in-memory channels, Redis LPUSH, etc.)
	// regardless of whether it serializes immediately.
	var taskInput map[string]interface{}
	if schedule.Input != nil {
		taskInput = make(map[string]interface{}, len(schedule.Input))
		for k, v := range schedule.Input {
			taskInput[k] = v
		}
	}

	// Capture the current tick span's trace context so the executor can
	// create a LINKED span (DISTRIBUTED_TRACING_GUIDE §16). Requires the
	// caller (Start loop) to have wrapped this tick in a root span.
	tc := telemetry.GetTraceContext(ctx)

	task := &core.Task{
		ID:           taskID,
		Type:         core.ScheduledTaskType,
		Status:       core.TaskStatusQueued,
		Input:        taskInput,
		ScheduleID:   schedule.ID,
		TargetAgent:  schedule.TargetAgent,
		TraceID:      tc.TraceID,
		ParentSpanID: tc.SpanID,
		CreatedAt:    time.Now(),
	}

	if err := s.deps.TaskStore.Create(ctx, task); err != nil {
		if errors.Is(err, core.ErrTaskAlreadyExists) {
			// Idempotent skip: another tick (or another leader during failover)
			// already created this exact task. Do not dispatch.
			EmitTaskDeduplicated(ctx, schedule.ID)
			return
		}
		s.deps.Logger.Error("Scheduler TaskStore.Create failed", map[string]interface{}{
			"operation":   "scheduler_fire",
			"instance_id": s.instanceID,
			"schedule_id": schedule.ID,
			"task_id":     taskID,
			"error":       err.Error(),
			"error_type":  "task_store_write",
		})
		return
	}

	// Dispatch to the fixed scheduled-executor queue, not per-agent.
	// The executor reads task.TargetAgent to route the HTTP POST.
	if err := s.deps.TaskDispatcher.Dispatch(ctx, ScheduledExecutorQueue, task); err != nil {
		s.deps.Logger.Error("Scheduler TaskDispatcher.Dispatch failed", map[string]interface{}{
			"operation":    "scheduler_fire",
			"instance_id":  s.instanceID,
			"schedule_id":  schedule.ID,
			"task_id":      taskID,
			"target_agent": schedule.TargetAgent,
			"error":        err.Error(),
			"error_type":   "task_dispatch",
		})
		return
	}

	EmitTaskPromoted(ctx, schedule)
}

// fireCatchUp fires one task per missed cron interval from schedule.RunAt
// up to the current time. Used when the Scheduler was down for a period and
// the schedule's MissedRunPolicy is MissedRunCatchUp.
//
// Each fired task has a distinct deterministic ID, so re-running catchup
// after a crash safely deduplicates.
func (s *Scheduler) fireCatchUp(ctx context.Context, schedule *core.Schedule) {
	if schedule.CronExpr == "" {
		// One-shot schedules don't support catchup semantics — fire once at
		// the stored RunAt and return.
		s.fireOnce(ctx, schedule, schedule.RunAt)
		return
	}

	cursor := schedule.RunAt
	now := time.Now()
	missedCount := 0
	for cursor.Before(now) {
		s.fireOnce(ctx, schedule, cursor)
		next, err := s.nextCron(schedule.CronExpr, cursor)
		if err != nil {
			s.deps.Logger.Error("Scheduler nextCron failed in catchup", map[string]interface{}{
				"operation":   "scheduler_fire",
				"instance_id": s.instanceID,
				"schedule_id": schedule.ID,
				"cron_expr":   schedule.CronExpr,
				"error":       err.Error(),
				"error_type":  "cron_parse",
			})
			break
		}
		cursor = next
		missedCount++
	}

	if missedCount > 0 {
		EmitMissedRunGap(ctx, time.Since(schedule.RunAt), core.MissedRunCatchUp)
	}
}

// advanceOrDelete updates recurring schedules (computing the next RunAt) or
// deletes one-shot schedules (which have been consumed by firing).
func (s *Scheduler) advanceOrDelete(ctx context.Context, schedule *core.Schedule) {
	if schedule.CronExpr == "" {
		// One-shot: consumed. Delete.
		if err := s.deps.ScheduleStore.Delete(ctx, schedule.ID); err != nil && !errors.Is(err, core.ErrScheduleNotFound) {
			s.deps.Logger.Warn("Scheduler one-shot delete failed", map[string]interface{}{
				"operation":   "scheduler_advance",
				"instance_id": s.instanceID,
				"schedule_id": schedule.ID,
				"error":       err.Error(),
				"error_type":  "schedule_store_write",
			})
			return
		}
		EmitScheduleDeleted(ctx, schedule.ID)
		return
	}

	// Recurring: compute next fire time from cron, update in place.
	now := time.Now()
	next, err := s.nextCron(schedule.CronExpr, now)
	if err != nil {
		s.deps.Logger.Error("Scheduler nextCron failed", map[string]interface{}{
			"operation":   "scheduler_advance",
			"instance_id": s.instanceID,
			"schedule_id": schedule.ID,
			"cron_expr":   schedule.CronExpr,
			"error":       err.Error(),
			"error_type":  "cron_parse",
		})
		return
	}

	schedule.RunAt = next
	schedule.LastRunAt = &now
	if err := s.deps.ScheduleStore.Update(ctx, schedule); err != nil {
		s.deps.Logger.Warn("Scheduler recurring update failed", map[string]interface{}{
			"operation":   "scheduler_advance",
			"instance_id": s.instanceID,
			"schedule_id": schedule.ID,
			"error":       err.Error(),
			"error_type":  "schedule_store_write",
		})
	}
}

// nextCron computes the next cron fire time strictly after the given
// reference time. Uses github.com/robfig/cron/v3 with standard 5-field syntax.
func (s *Scheduler) nextCron(cronExpr string, after time.Time) (time.Time, error) {
	sched, err := s.cronParser.Parse(cronExpr)
	if err != nil {
		return time.Time{}, fmt.Errorf("parse cron expression %q: %w", cronExpr, err)
	}
	next := sched.Next(after)
	if next.IsZero() {
		return time.Time{}, fmt.Errorf("cron expression %q has no next fire time after %s", cronExpr, after)
	}
	return next, nil
}

// resolveDurationEnv reads a Go-format duration ("5s", "30s", "2m") from an
// environment variable, falling back to a default if unset or malformed.
// Malformed values fall through to the default — startup is never blocked —
// matching the framework's fail-safe-defaults principle.
//
// Bare integers (e.g., "5") are intentionally rejected: they would silently
// become a different magnitude depending on the implied unit and mask
// operator typos like dropping the "m" from "5m".
func resolveDurationEnv(envVar string, fallback time.Duration) time.Duration {
	raw := os.Getenv(envVar)
	if raw == "" {
		return fallback
	}
	if d, err := time.ParseDuration(raw); err == nil && d > 0 {
		return d
	}
	return fallback
}

// buildInstanceID constructs a stable identifier for this Scheduler
// instance, used in structured logs so operators can identify which pod
// is running this Scheduler. It is NOT passed to the distributed lock —
// core.DistributedLock.Acquire takes no value parameter.
//
// Prefers TRUVAG3_K8S_SERVICE_NAME + hostname (K8s-aware, human-readable),
// falling back to a UUID for non-K8s deployments.
func buildInstanceID() string {
	service := os.Getenv(core.EnvServiceName)
	host, _ := os.Hostname()
	if service != "" && host != "" {
		return fmt.Sprintf("%s/%s", service, host)
	}
	if host != "" {
		return host
	}
	return uuid.New().String()
}
