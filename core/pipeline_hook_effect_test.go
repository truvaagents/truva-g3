package core

import (
	"context"
	"reflect"
	"testing"
)

type capturePipelineHookEffectReporter struct {
	effects []PipelineHookEffect
}

func (reporter *capturePipelineHookEffectReporter) ReportPipelineHookEffect(
	effect PipelineHookEffect,
) error {
	reporter.effects = append(reporter.effects, effect)
	return nil
}

func TestPipelineHookEffectReporterContextSeam(t *testing.T) {
	effect := PipelineHookEffect{
		EffectID: "retrieval",
		Name:     "Retrieved context",
		Status:   PipelineHookEffectSucceeded,
		Data:     []byte(`{"value":"exact"}`),
	}
	if err := ReportPipelineHookEffect(context.Background(), effect); err != nil {
		t.Fatalf("ReportPipelineHookEffect without reporter = %v", err)
	}

	reporter := &capturePipelineHookEffectReporter{}
	ctx := WithPipelineHookEffectReporter(t.Context(), reporter)
	if got, ok := PipelineHookEffectReporterFromContext(ctx); !ok || got != reporter {
		t.Fatalf("PipelineHookEffectReporterFromContext() = %#v, %v", got, ok)
	}
	if err := ReportPipelineHookEffect(ctx, effect); err != nil {
		t.Fatalf("ReportPipelineHookEffect() = %v", err)
	}
	if !reflect.DeepEqual(reporter.effects, []PipelineHookEffect{effect}) {
		t.Fatalf("reported effects = %#v", reporter.effects)
	}
}
