package orchestration

import (
	"context"
	"errors"
	"reflect"
	"sync"
	"testing"

	"github.com/truvaagents/truva-g3/core"
	"github.com/truvaagents/truva-g3/telemetry"
	"go.opentelemetry.io/otel/codes"
)

type resumeLogCapture struct {
	core.NoOpLogger
	mu       sync.Mutex
	fields   []map[string]interface{}
	contexts []context.Context
}

func (l *resumeLogCapture) capture(ctx context.Context, fields map[string]interface{}) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.fields = append(l.fields, fields)
	l.contexts = append(l.contexts, ctx)
}
func (l *resumeLogCapture) InfoWithContext(ctx context.Context, _ string, fields map[string]interface{}) {
	l.capture(ctx, fields)
}
func (l *resumeLogCapture) WarnWithContext(ctx context.Context, _ string, fields map[string]interface{}) {
	l.capture(ctx, fields)
}
func (l *resumeLogCapture) ErrorWithContext(ctx context.Context, _ string, fields map[string]interface{}) {
	l.capture(ctx, fields)
}

func TestResumeCoordinatorObservationBoundaries(t *testing.T) {
	for _, mode := range []string{"success", "execution failure", "release failure", "finalization failure", "rejected", "invalid input"} {
		t.Run(mode, func(t *testing.T) {
			recorder := setupHITLResumeTestTracer(t)
			logs := &resumeLogCapture{}
			store := &resumeStoreStub{}
			failure := errors.New("application-owned-secret-body")
			switch mode {
			case "rejected":
				store.claim = func(context.Context, ResumeClaimRequest) (*CheckpointResumeClaim, error) {
					return nil, &ErrCheckpointResumeInProgress{CheckpointID: "parent"}
				}
			case "release failure":
				store.release = func(context.Context, string, string) error { return errors.New("backend body") }
			case "finalization failure":
				store.finalize = func(context.Context, ResumeFinalization) error { return errors.New("backend body") }
			}
			r := newResumeTestCoordinator(t, store, func(ctx context.Context, _ *ExecutionCheckpoint) (*ExecutionResult, error) {
				reportResumeRequestID(ctx, "actual-execution")
				if mode == "execution failure" || mode == "release failure" {
					return nil, failure
				}
				return &ExecutionResult{Success: true}, nil
			}, WithResumeLogger(logs))
			type counter struct {
				name   string
				labels map[string]string
			}
			var metrics []counter
			r.counter = func(name string, labels ...string) {
				values := map[string]string{}
				for i := 0; i < len(labels); i += 2 {
					values[labels[i]] = labels[i+1]
				}
				metrics = append(metrics, counter{name, values})
			}
			id := "parent"
			if mode == "invalid input" {
				id = ""
			}
			_, _ = r.ResumeExecution(t.Context(), id)
			preClaim := mode == "rejected" || mode == "invalid input"
			wantMetricCount := 2
			if preClaim {
				wantMetricCount = 1
			}
			if mode == "success" || mode == "finalization failure" {
				wantMetricCount = 3
			}
			if len(metrics) != wantMetricCount {
				t.Fatalf("metrics = %+v", metrics)
			}
			for _, metric := range metrics {
				allowed := map[string]bool{"module": true, "outcome": true}
				if metric.name == MetricResumeOutcome {
					allowed["stage"] = true
				}
				if metric.name == MetricResumeRenewal {
					allowed["phase"] = true
				}
				for key := range metric.labels {
					if !allowed[key] {
						t.Fatalf("unexpected metric label: %+v", metric)
					}
				}
				if metric.labels["module"] != telemetry.ModuleOrchestration {
					t.Fatal("missing module label")
				}
			}
			spans := recorder.Ended()
			if preClaim {
				if len(spans) != 0 || len(logs.fields) != 1 {
					t.Fatal("rejected call invented an execution boundary")
				}
				if metrics[0].name != MetricResumeAttempt {
					t.Fatal("rejection counted a terminal outcome")
				}
				return
			}
			if len(spans) != 1 {
				t.Fatalf("resume spans = %d", len(spans))
			}
			var events []string
			for _, event := range spans[0].Events() {
				if event.Name == "exception" {
					continue
				}
				events = append(events, event.Name)
				if event.Name != "hitl.trace_link_created" && (len(event.Attributes) == 0 || string(event.Attributes[0].Key) != "request_id") {
					t.Fatalf("request_id is not first: %+v", event)
				}
			}
			endEvent, outcome, stage := "hitl.resume.failed", "failed", "execute"
			if mode == "success" {
				endEvent, outcome, stage = "hitl.resume.completed", "completed", "finalize"
			}
			if mode == "release failure" {
				stage = "release"
			}
			if mode == "finalization failure" {
				stage = "finalize"
			}
			if !reflect.DeepEqual(events, []string{"hitl.trace_link_created", "hitl.resume.claimed", "hitl.resume.started", endEvent}) {
				t.Fatalf("event order = %v", events)
			}
			last := metrics[len(metrics)-1]
			if last.name != MetricResumeOutcome || last.labels["outcome"] != outcome || last.labels["stage"] != stage {
				t.Fatalf("terminal metric = %+v", last)
			}
			if (spans[0].Status().Code == codes.Error) != (mode != "success") {
				t.Fatalf("span status = %v", spans[0].Status())
			}
			log := logs.fields[len(logs.fields)-1]
			ctx := logs.contexts[len(logs.contexts)-1]
			if log["request_id"] != "actual-execution" || telemetry.GetBaggage(ctx)["request_id"] != "actual-execution" || log["original_request_id"] != "root-request" || log["outcome"] != outcome || log["stage"] != stage {
				t.Fatalf("terminal correlation = %+v", log)
			}
			if _, ok := log["duration_ms"].(int64); !ok {
				t.Fatal("duration is missing or not numeric")
			}
			if log["error"] == failure.Error() {
				t.Fatal("raw application body copied into lifecycle diagnostics")
			}
			bag := telemetry.GetBaggage(ctx)
			if bag["attempt_id"] != "" || bag["owner"] != "" {
				t.Fatal("ownership leaked into baggage")
			}
		})
	}
}
