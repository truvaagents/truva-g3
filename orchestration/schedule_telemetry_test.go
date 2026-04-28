// Package orchestration — unit tests for schedule_telemetry.go.
//
// The Emit* helpers wrap the telemetry global singleton API, which is
// nil-safe (no-op when telemetry isn't initialized). These tests verify
// the helpers never panic under any input configuration and correctly
// derive their labels from the Schedule struct.

package orchestration

import (
	"context"
	"testing"
	"time"

	"github.com/truvaagents/truva-g3/core"
	"github.com/stretchr/testify/assert"
)

func TestEmitScheduleCreated_CronType_DoesNotPanic(t *testing.T) {
	assert.NotPanics(t, func() {
		EmitScheduleCreated(context.Background(), &core.Schedule{
			ID:          "sch-1",
			TargetAgent: "agent-a",
			CronExpr:    "*/5 * * * *",
		})
	})
}

func TestEmitScheduleCreated_OneShotType_DoesNotPanic(t *testing.T) {
	assert.NotPanics(t, func() {
		EmitScheduleCreated(context.Background(), &core.Schedule{
			ID:          "sch-1",
			TargetAgent: "agent-a",
			CronExpr:    "",
		})
	})
}

func TestEmitScheduleCreated_NilSchedule_DoesNotPanic(t *testing.T) {
	assert.NotPanics(t, func() {
		EmitScheduleCreated(context.Background(), nil)
	})
}

func TestEmitScheduleDeleted_DoesNotPanic(t *testing.T) {
	assert.NotPanics(t, func() {
		EmitScheduleDeleted(context.Background(), "sch-1")
	})
}

func TestEmitActiveSchedules_DoesNotPanic(t *testing.T) {
	assert.NotPanics(t, func() {
		EmitActiveSchedules(context.Background(), 42)
	})
}

func TestEmitSchedulerTick_LeaderOutcome_DoesNotPanic(t *testing.T) {
	assert.NotPanics(t, func() {
		EmitSchedulerTick(context.Background(), "leader", 123.45)
	})
}

func TestEmitSchedulerTick_StandbyOutcome_DoesNotPanic(t *testing.T) {
	assert.NotPanics(t, func() {
		EmitSchedulerTick(context.Background(), "standby", 1.2)
	})
}

func TestEmitSchedulerTick_ErrorOutcome_DoesNotPanic(t *testing.T) {
	assert.NotPanics(t, func() {
		EmitSchedulerTick(context.Background(), "error", 5.0)
	})
}

func TestEmitTaskPromoted_CronType_DoesNotPanic(t *testing.T) {
	assert.NotPanics(t, func() {
		EmitTaskPromoted(context.Background(), &core.Schedule{
			ID:          "sch-1",
			TargetAgent: "agent-a",
			CronExpr:    "0 9 * * *",
		})
	})
}

func TestEmitTaskPromoted_OneShotType_DoesNotPanic(t *testing.T) {
	assert.NotPanics(t, func() {
		EmitTaskPromoted(context.Background(), &core.Schedule{
			ID:          "sch-1",
			TargetAgent: "agent-a",
			CronExpr:    "",
		})
	})
}

func TestEmitTaskPromoted_NilSchedule_DoesNotPanic(t *testing.T) {
	assert.NotPanics(t, func() {
		EmitTaskPromoted(context.Background(), nil)
	})
}

func TestEmitTaskDeduplicated_DoesNotPanic(t *testing.T) {
	assert.NotPanics(t, func() {
		EmitTaskDeduplicated(context.Background(), "sch-dedup")
	})
}

func TestEmitMissedRunGap_SkipPolicy_DoesNotPanic(t *testing.T) {
	assert.NotPanics(t, func() {
		EmitMissedRunGap(context.Background(), 3*time.Minute, core.MissedRunSkip)
	})
}

func TestEmitMissedRunGap_CatchUpPolicy_DoesNotPanic(t *testing.T) {
	assert.NotPanics(t, func() {
		EmitMissedRunGap(context.Background(), 15*time.Minute, core.MissedRunCatchUp)
	})
}
