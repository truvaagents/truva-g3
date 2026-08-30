package postgresadapter

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/truvaagents/truva-g3/core"
)

func TestValidateStoreConfig(t *testing.T) {
	pool := &pgxpool.Pool{}
	tests := []struct {
		name      string
		pool      *pgxpool.Pool
		namespace string
		want      string
		wantError string
	}{
		{name: "valid and trimmed", pool: pool, namespace: "  tenant-a  ", want: "tenant-a"},
		{name: "missing pool", namespace: "tenant-a", wantError: "pool is required"},
		{name: "missing namespace", pool: pool, namespace: "  ", wantError: "namespace is required"},
		{name: "namespace too long", pool: pool, namespace: strings.Repeat("a", 129), wantError: "exceeds 128 characters"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := validateStoreConfig(test.pool, test.namespace)
			if test.wantError != "" {
				if err == nil || !strings.Contains(err.Error(), test.wantError) {
					t.Fatalf("validateStoreConfig() error = %v, want containing %q", err, test.wantError)
				}
				return
			}
			if err != nil || got != test.want {
				t.Fatalf("validateStoreConfig() = %q, %v; want %q, nil", got, err, test.want)
			}
		})
	}
}

func TestValidateSchedule(t *testing.T) {
	valid := &core.Schedule{ID: "schedule-1", TargetAgent: "agent", RunAt: time.Now()}
	tests := []struct {
		name      string
		schedule  *core.Schedule
		wantError string
	}{
		{name: "valid", schedule: valid},
		{name: "nil", wantError: "schedule is required"},
		{name: "missing ID", schedule: &core.Schedule{TargetAgent: "agent", RunAt: time.Now()}, wantError: "schedule ID is required"},
		{name: "missing target", schedule: &core.Schedule{ID: "schedule-1", RunAt: time.Now()}, wantError: "target agent is required"},
		{name: "missing run time", schedule: &core.Schedule{ID: "schedule-1", TargetAgent: "agent"}, wantError: "run time is required"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := validateSchedule(test.schedule)
			if test.wantError == "" && err != nil {
				t.Fatalf("validateSchedule() error = %v", err)
			}
			if test.wantError != "" && (err == nil || !strings.Contains(err.Error(), test.wantError)) {
				t.Fatalf("validateSchedule() error = %v, want containing %q", err, test.wantError)
			}
		})
	}
}

func TestValidateTaskAndRequireID(t *testing.T) {
	if err := validateTask(&core.Task{ID: "task-1"}); err != nil {
		t.Fatalf("valid task rejected: %v", err)
	}
	if err := validateTask(nil); err == nil || !strings.Contains(err.Error(), "task is required") {
		t.Fatalf("nil task error = %v", err)
	}
	if err := validateTask(&core.Task{ID: "  "}); err == nil || !strings.Contains(err.Error(), "task ID is required") {
		t.Fatalf("empty task ID error = %v", err)
	}
	if got, err := requireID("task", "  task-1  "); err != nil || got != "task-1" {
		t.Fatalf("requireID() = %q, %v", got, err)
	}
	if _, err := requireID("task", "  "); err == nil || !strings.Contains(err.Error(), "task ID is required") {
		t.Fatalf("empty requireID error = %v", err)
	}
}

func TestDecodeScheduleAndTask(t *testing.T) {
	runAt := time.Date(2026, time.August, 28, 12, 0, 0, 0, time.UTC)
	schedulePayload, err := json.Marshal(&core.Schedule{ID: "schedule-1", TargetAgent: "agent", RunAt: runAt})
	if err != nil {
		t.Fatal(err)
	}
	schedule, err := decodeSchedule(schedulePayload)
	if err != nil || schedule.ID != "schedule-1" || !schedule.RunAt.Equal(runAt) {
		t.Fatalf("decodeSchedule() = %#v, %v", schedule, err)
	}
	if _, err := decodeSchedule([]byte("{")); err == nil || !strings.Contains(err.Error(), "decode schedule") {
		t.Fatalf("invalid schedule error = %v", err)
	}

	taskPayload, err := json.Marshal(&core.Task{ID: "task-1", Status: core.TaskStatusQueued})
	if err != nil {
		t.Fatal(err)
	}
	task, err := decodeTask(taskPayload)
	if err != nil || task.ID != "task-1" || task.Status != core.TaskStatusQueued {
		t.Fatalf("decodeTask() = %#v, %v", task, err)
	}
	if _, err := decodeTask([]byte("{")); err == nil || !strings.Contains(err.Error(), "decode task") {
		t.Fatalf("invalid task error = %v", err)
	}
}

func TestScanSchedules(t *testing.T) {
	first, _ := json.Marshal(&core.Schedule{ID: "first"})
	second, _ := json.Marshal(&core.Schedule{ID: "second"})

	t.Run("success", func(t *testing.T) {
		rows := &fakeScheduleRows{payloads: [][]byte{first, second}}
		schedules, err := scanSchedules(rows)
		if err != nil || len(schedules) != 2 || schedules[0].ID != "first" || schedules[1].ID != "second" {
			t.Fatalf("scanSchedules() = %#v, %v", schedules, err)
		}
	})
	t.Run("scan error", func(t *testing.T) {
		rows := &fakeScheduleRows{payloads: [][]byte{first}, scanErr: errors.New("scan failed")}
		if _, err := scanSchedules(rows); err == nil || !strings.Contains(err.Error(), "scan schedule") {
			t.Fatalf("scanSchedules() error = %v", err)
		}
	})
	t.Run("decode error", func(t *testing.T) {
		rows := &fakeScheduleRows{payloads: [][]byte{[]byte("{")}}
		if _, err := scanSchedules(rows); err == nil || !strings.Contains(err.Error(), "decode schedule") {
			t.Fatalf("scanSchedules() error = %v", err)
		}
	})
	t.Run("iteration error", func(t *testing.T) {
		rows := &fakeScheduleRows{iterationErr: errors.New("iteration failed")}
		if _, err := scanSchedules(rows); err == nil || !strings.Contains(err.Error(), "iterate schedules") {
			t.Fatalf("scanSchedules() error = %v", err)
		}
	})
}

type fakeScheduleRows struct {
	payloads     [][]byte
	index        int
	scanErr      error
	iterationErr error
}

func (rows *fakeScheduleRows) Next() bool {
	if rows.index >= len(rows.payloads) {
		return false
	}
	rows.index++
	return true
}

func (rows *fakeScheduleRows) Scan(dest ...any) error {
	if rows.scanErr != nil {
		return rows.scanErr
	}
	target, ok := dest[0].(*[]byte)
	if !ok {
		return errors.New("unexpected scan destination")
	}
	*target = rows.payloads[rows.index-1]
	return nil
}

func (rows *fakeScheduleRows) Err() error {
	return rows.iterationErr
}
