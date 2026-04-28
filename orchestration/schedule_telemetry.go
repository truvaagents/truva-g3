// Package orchestration — telemetry helpers for scheduling.
//
// Mirrors task_telemetry.go pattern: centralized Emit* functions that wrap
// the telemetry global singleton API. Nil-safe by construction — the
// telemetry package's global API is a no-op when not initialized, so these
// functions never panic regardless of setup state.
//
// Metric namespace:
//   truvag3.schedules.*  — schedule lifecycle (created, deleted, active count)
//   truvag3.scheduler.*  — tick loop and promotion internals
//
// Keeping these in orchestration (not in the telemetry module) follows
// telemetry/ARCHITECTURE.md §4: module-specific metrics live in the module
// that owns them. The telemetry module defines contracts; it does not define
// domain-specific metrics.

package orchestration

import (
	"context"
	"time"

	"github.com/truvaagents/truva-g3/core"
	"github.com/truvaagents/truva-g3/telemetry"
	"go.opentelemetry.io/otel/attribute"
)

// ═══════════════════════════════════════════════════════════════════════════
// Schedule Lifecycle (emitted by capability handlers)
// ═══════════════════════════════════════════════════════════════════════════

// EmitScheduleCreated emits a metric when a new schedule is created.
//
//	Counter: truvag3.schedules.created (target_agent, schedule_type)
//	  schedule_type is "cron" or "one_shot"
func EmitScheduleCreated(ctx context.Context, schedule *core.Schedule) {
	if schedule == nil {
		return
	}
	scheduleType := "one_shot"
	if schedule.CronExpr != "" {
		scheduleType = "cron"
	}
	telemetry.Counter("truvag3.schedules.created",
		"target_agent", schedule.TargetAgent,
		"schedule_type", scheduleType,
	)
	telemetry.AddSpanEvent(ctx, "schedule.created",
		attribute.String("schedule_id", schedule.ID),
		attribute.String("target_agent", schedule.TargetAgent),
		attribute.String("schedule_type", scheduleType),
	)
}

// EmitScheduleDeleted emits a metric when a schedule is removed — either
// explicitly via cancel_schedule, or implicitly when a one-shot schedule is
// consumed after firing.
//
//	Counter: truvag3.schedules.deleted
func EmitScheduleDeleted(ctx context.Context, scheduleID string) {
	telemetry.Counter("truvag3.schedules.deleted")
	telemetry.AddSpanEvent(ctx, "schedule.deleted",
		attribute.String("schedule_id", scheduleID),
	)
}

// EmitActiveSchedules emits the current count of active (enabled) schedules.
// Intended for periodic emission by operator tooling or a future background
// job — the Scheduler itself does not call this on every tick.
//
//	Gauge: truvag3.schedules.active
func EmitActiveSchedules(_ context.Context, count int) {
	telemetry.Gauge("truvag3.schedules.active", float64(count))
}

// ═══════════════════════════════════════════════════════════════════════════
// Scheduler Tick Loop
// ═══════════════════════════════════════════════════════════════════════════

// EmitSchedulerTick emits metrics for a single Scheduler tick cycle.
//
//	Counter:   truvag3.scheduler.ticks (outcome: "leader" | "standby" | "error")
//	Histogram: truvag3.scheduler.tick_duration_ms (only for "leader" ticks,
//	           to keep the histogram meaningful — standby/error ticks are
//	           near-instant)
func EmitSchedulerTick(ctx context.Context, outcome string, durationMs float64) {
	telemetry.Counter("truvag3.scheduler.ticks",
		"outcome", outcome,
	)
	if outcome == "leader" {
		telemetry.Histogram("truvag3.scheduler.tick_duration_ms", durationMs)
	}
	telemetry.AddSpanEvent(ctx, "scheduler.tick",
		attribute.String("outcome", outcome),
		attribute.Float64("duration_ms", durationMs),
	)
}

// EmitTaskPromoted emits a metric when a schedule successfully fires and
// a task is dispatched to the target queue.
//
//	Counter: truvag3.scheduler.tasks_promoted (target_agent, schedule_type)
func EmitTaskPromoted(ctx context.Context, schedule *core.Schedule) {
	if schedule == nil {
		return
	}
	scheduleType := "one_shot"
	if schedule.CronExpr != "" {
		scheduleType = "cron"
	}
	telemetry.Counter("truvag3.scheduler.tasks_promoted",
		"target_agent", schedule.TargetAgent,
		"schedule_type", scheduleType,
	)
	telemetry.AddSpanEvent(ctx, "scheduler.task_promoted",
		attribute.String("schedule_id", schedule.ID),
		attribute.String("target_agent", schedule.TargetAgent),
		attribute.String("schedule_type", scheduleType),
	)
}

// EmitTaskDeduplicated emits a metric when TaskStore.Create returns
// ErrTaskAlreadyExists, meaning another tick (or another leader during
// failover) already created this task. This is a safe skip — not an error —
// but spikes in this metric indicate leader churn worth investigating.
//
//	Counter: truvag3.scheduler.tasks_deduplicated
func EmitTaskDeduplicated(ctx context.Context, scheduleID string) {
	telemetry.Counter("truvag3.scheduler.tasks_deduplicated")
	telemetry.AddSpanEvent(ctx, "scheduler.task_deduplicated",
		attribute.String("schedule_id", scheduleID),
	)
}

// EmitMissedRunGap emits a histogram of how far behind a schedule was when
// it was fired — i.e., the gap between the stored RunAt and the time the
// Scheduler actually processed it. High p95 values indicate promotion lag.
//
//	Histogram: truvag3.scheduler.missed_gap_seconds (policy: "skip" | "catchup")
func EmitMissedRunGap(ctx context.Context, gap time.Duration, policy core.MissedRunPolicy) {
	telemetry.Histogram("truvag3.scheduler.missed_gap_seconds",
		gap.Seconds(),
		"policy", string(policy),
	)
	telemetry.AddSpanEvent(ctx, "scheduler.missed_gap",
		attribute.Float64("gap_seconds", gap.Seconds()),
		attribute.String("policy", string(policy)),
	)
}
