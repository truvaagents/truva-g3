package orchestration

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"time"

	"github.com/truvaagents/truva-g3/core"
	"github.com/truvaagents/truva-g3/telemetry"
	"go.opentelemetry.io/otel/attribute"
)

type pipelineEnrichmentValueSnapshot struct {
	present bool
	data    json.RawMessage
	err     string
}

type pipelineEnrichmentEffectData struct {
	Key       string          `json:"key"`
	Operation string          `json:"operation"`
	Before    json.RawMessage `json:"before,omitempty"`
	After     json.RawMessage `json:"after,omitempty"`
}

func snapshotPipelineEnrichments(pctx *core.PipelineContext) map[string]pipelineEnrichmentValueSnapshot {
	if pctx == nil || len(pctx.Enrichments) == 0 {
		return nil
	}
	snapshot := make(map[string]pipelineEnrichmentValueSnapshot, len(pctx.Enrichments))
	for key, value := range pctx.Enrichments {
		encoded, err := json.Marshal(value)
		entry := pipelineEnrichmentValueSnapshot{present: true, data: encoded}
		if err != nil {
			entry.err = err.Error()
			entry.data = nil
		}
		snapshot[key] = entry
	}
	return snapshot
}

func reportPipelineEnrichmentChanges(
	ctx context.Context,
	before map[string]pipelineEnrichmentValueSnapshot,
	pctx *core.PipelineContext,
) {
	after := snapshotPipelineEnrichments(pctx)
	keys := make(map[string]struct{}, len(before)+len(after))
	for key := range before {
		keys[key] = struct{}{}
	}
	for key := range after {
		keys[key] = struct{}{}
	}
	orderedKeys := make([]string, 0, len(keys))
	for key := range keys {
		orderedKeys = append(orderedKeys, key)
	}
	sort.Strings(orderedKeys)

	for _, key := range orderedKeys {
		prior := before[key]
		current := after[key]
		if prior.present == current.present && prior.err == current.err && bytes.Equal(prior.data, current.data) {
			continue
		}
		operation := "updated"
		switch {
		case !prior.present && current.present:
			operation = "added"
		case prior.present && !current.present:
			operation = "removed"
		}
		data, marshalErr := json.Marshal(pipelineEnrichmentEffectData{
			Key: key, Operation: operation, Before: prior.data, After: current.data,
		})
		captureErr := prior.err
		if captureErr == "" {
			captureErr = current.err
		}
		if captureErr == "" && marshalErr != nil {
			captureErr = marshalErr.Error()
		}
		status := core.PipelineHookEffectSucceeded
		summary := operation + " planning enrichment " + key
		if captureErr != "" {
			status = core.PipelineHookEffectFailed
			summary = "Could not capture planning enrichment " + key
			data = nil
		}
		_ = core.ReportPipelineHookEffect(ctx, core.PipelineHookEffect{
			EffectID: "planning_enrichment:" + key,
			Name:     "Planning enrichment",
			Status:   status,
			Summary:  summary,
			Data:     data,
			Error:    captureErr,
		})
	}
}

func reportStructuredPipelineHookEffect(
	ctx context.Context,
	effectID string,
	name string,
	status core.PipelineHookEffectStatus,
	summary string,
	data interface{},
	effectErr error,
	startedAt time.Time,
) {
	if _, enabled := core.PipelineHookEffectReporterFromContext(ctx); !enabled {
		return
	}
	var encoded json.RawMessage
	if data != nil {
		value, err := json.Marshal(data)
		if err != nil {
			status = core.PipelineHookEffectFailed
			effectErr = err
			summary = "Could not serialize hook effect evidence"
		} else {
			encoded = value
		}
	}
	errorText := ""
	if effectErr != nil {
		errorText = effectErr.Error()
	}
	_ = core.ReportPipelineHookEffect(ctx, core.PipelineHookEffect{
		EffectID:      effectID,
		SchemaVersion: 1,
		Name:          name,
		Status:        status,
		Summary:       summary,
		Data:          encoded,
		Error:         errorText,
		StartedAt:     startedAt,
		Duration:      time.Since(startedAt),
	})
}

// recordPipelineHookEffectSpanError marks an ordinary trace as failed without
// copying provider-owned error text into it. Exact errors remain available in
// the opt-in execution-debug effect that accompanies this observation.
func recordPipelineHookEffectSpanError(ctx context.Context, errorType string) {
	telemetry.RecordSpanError(ctx, fmt.Errorf("pipeline hook effect failed: %s", errorType))
	telemetry.SetSpanAttributes(ctx,
		attribute.String("error_type", errorType),
		attribute.String("pipeline.hook.effect.error_type", errorType),
	)
}
