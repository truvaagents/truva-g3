package orchestration

import (
	"testing"
	"time"

	"github.com/truvaagents/truva-g3/core"
)

type countingHookJSON struct{ calls *int }

func (v countingHookJSON) MarshalJSON() ([]byte, error) {
	*v.calls++
	return []byte(`"application-value"`), nil
}

func TestDisabledHookCaptureDoesNotMarshalEnrichments(t *testing.T) {
	calls := 0
	o := &AIOrchestrator{pipelineHooks: []core.PipelineHook{&allStagesHook{name: "capture-test"}}}
	pctx := &core.PipelineContext{Enrichments: map[string]interface{}{"application": countingHookJSON{&calls}}}
	_, err := o.runBeforePlanningHooks(t.Context(), pctx, newPipelineGate(nil, false))
	if err != nil {
		t.Fatal(err)
	}
	if calls != 0 {
		t.Fatalf("disabled hook capture invoked application MarshalJSON %d times", calls)
	}
}

type captureEffectReporter struct{ effects []core.PipelineHookEffect }

func (r *captureEffectReporter) ReportPipelineHookEffect(effect core.PipelineHookEffect) error {
	r.effects = append(r.effects, effect)
	return nil
}

func TestStructuredHookEffectRequiresReporterBeforeSerialization(t *testing.T) {
	calls := 0
	value := countingHookJSON{&calls}
	reportStructuredPipelineHookEffect(t.Context(), "effect", "effect", core.PipelineHookEffectSucceeded, "", value, nil, time.Now())
	if calls != 0 {
		t.Fatalf("absent reporter invoked MarshalJSON %d times", calls)
	}
	reporter := &captureEffectReporter{}
	ctx := core.WithPipelineHookEffectReporter(t.Context(), reporter)
	reportStructuredPipelineHookEffect(ctx, "effect", "effect", core.PipelineHookEffectSucceeded, "", value, nil, time.Now())
	if calls != 1 || len(reporter.effects) != 1 || string(reporter.effects[0].Data) != `"application-value"` {
		t.Fatalf("enabled capture lost exact evidence: calls=%d effects=%+v", calls, reporter.effects)
	}
}
