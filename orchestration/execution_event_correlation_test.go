package orchestration

import (
	"context"
	"testing"
	"time"

	"github.com/truvaagents/truva-g3/core"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

func TestExecutionLifecycleEventsCarryRequestID(t *testing.T) {
	recorder := tracetest.NewSpanRecorder()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder))
	t.Cleanup(func() { _ = provider.Shutdown(context.Background()) })
	ctx, span := provider.Tracer("correlation-test").Start(WithRequestID(t.Context(), "request"), "request")
	defer span.End()
	resources := &executionResources{timeout: time.Second}
	ctx = context.WithValue(ctx, executionResourcesKey{}, resources)
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	defer resources.close(ctx, time.Second, nil)
	updates := make(chan struct{}, 8)
	activity := &core.MockActivityCoordinator{AnnounceActivityFn: func(context.Context, core.ActivitySignal) error {
		select {
		case updates <- struct{}{}:
		default:
		}
		return nil
	}}
	if err := resources.announceActivity(ctx, activity, core.ActivitySignal{RequestID: "request", TTL: 60 * time.Millisecond}); err != nil {
		t.Fatal(err)
	}
	if acquired, _, err := resources.acquire(ctx, &resourceLeaseProvider{}, "agent", "entity"); err != nil || !acquired {
		t.Fatalf("acquire: %t, %v", acquired, err)
	}
	for range 2 { // Initial announcement and at least one renewal.
		select {
		case <-updates:
		case <-time.After(time.Second):
			t.Fatal("activity renewal did not arrive")
		}
	}
	cancel()
	resources.close(ctx, time.Second, nil)
	holder := &executionLifecycle{observation: ExecutionLifecycleObservation{State: RequestLifecycleRunning, Revision: 1}}
	state := &executionRunState{Context: context.WithValue(ctx, executionLifecycleKey{}, holder)}
	(&AIOrchestrator{}).settleRequestLifecycle(state, nil, context.Canceled)
	span.End()
	wanted := map[string]bool{
		"orchestrator.execution.lifecycle": false, "memory.investigation.cleanup": false,
		"activity.renewal.submitted": false, "activity.cleanup.submitted": false,
	}
	for _, recorded := range recorder.Ended() {
		for _, event := range recorded.Events() {
			if _, found := wanted[event.Name]; !found {
				continue
			}
			if len(event.Attributes) == 0 || string(event.Attributes[0].Key) != "request_id" || event.Attributes[0].Value.AsString() != "request" {
				t.Fatalf("event %s lost first-attribute request correlation: %+v", event.Name, event.Attributes)
			}
			wanted[event.Name] = true
		}
	}
	for event, found := range wanted {
		if !found {
			t.Errorf("missing lifecycle event %s", event)
		}
	}
}
