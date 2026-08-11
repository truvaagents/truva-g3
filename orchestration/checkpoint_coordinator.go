package orchestration

import (
	"context"
	"errors"
	"fmt"

	"github.com/truvaagents/truva-g3/telemetry"
	"go.opentelemetry.io/otel/attribute"
)

var (
	ErrCheckpointEnrichmentUnsupported = errors.New("checkpoint enrichment unsupported")
	ErrCheckpointAuthoritativeSave     = errors.New("authoritative checkpoint save failed")
)

type checkpointEnrichmentRequiredKey struct{}

func withCheckpointEnrichmentRequired(ctx context.Context) context.Context {
	return context.WithValue(ctx, checkpointEnrichmentRequiredKey{}, true)
}

func checkpointEnrichmentRequired(ctx context.Context) bool {
	required, _ := ctx.Value(checkpointEnrichmentRequiredKey{}).(bool)
	return required
}

// executionRunSnapshot contains immutable lifecycle facts needed at a durable
// suspension boundary. It deliberately contains no provider clients or prompt
// and resource bodies.
type executionRunSnapshot struct {
	PhaseNumber        int
	AccumulatedResults map[string]*StepResult
	ExecutedStepIDs    []string
	ContinuationNote   string
}

func newExecutionRunSnapshot(
	phaseNumber int,
	results map[string]*StepResult,
	executedStepIDs []string,
	continuationNote string,
) executionRunSnapshot {
	clonedResults := make(map[string]*StepResult, len(results))
	for key, value := range results {
		if value == nil {
			continue
		}
		copy := *value
		copy.Parameters = cloneInterfaceMap(value.Parameters)
		if value.Metadata != nil {
			copy.Metadata = cloneInterfaceMap(value.Metadata)
		}
		clonedResults[key] = &copy
	}
	return executionRunSnapshot{
		PhaseNumber:        phaseNumber,
		AccumulatedResults: clonedResults,
		ExecutedStepIDs:    append([]string(nil), executedStepIDs...),
		ContinuationNote:   continuationNote,
	}
}

func (s executionRunSnapshot) requiresPersistence() bool {
	return s.PhaseNumber > 1 || len(s.AccumulatedResults) > 0 ||
		len(s.ExecutedStepIDs) > 0 || s.ContinuationNote != ""
}

type checkpointCoordinator struct {
	controller InterruptController
}

func (c checkpointCoordinator) saveAuthoritative(
	ctx context.Context,
	checkpoint *ExecutionCheckpoint,
	snapshot executionRunSnapshot,
) error {
	if checkpoint == nil {
		return nil
	}

	applyRunSnapshot(checkpoint, snapshot)
	enricher, ok := c.controller.(CheckpointEnricher)
	if !ok {
		if checkpoint.Status != CheckpointStatusPreparing && !snapshot.requiresPersistence() {
			return nil
		}
		return ErrCheckpointEnrichmentUnsupported
	}

	previousStatus := checkpoint.Status
	if checkpoint.Status == CheckpointStatusPreparing {
		checkpoint.Status = CheckpointStatusPending
	}
	if err := enricher.SaveEnrichedCheckpoint(ctx, checkpoint); err != nil {
		checkpoint.Status = previousStatus
		return fmt.Errorf("%w", ErrCheckpointAuthoritativeSave)
	}
	return nil
}

func applyRunSnapshot(checkpoint *ExecutionCheckpoint, snapshot executionRunSnapshot) {
	checkpoint.PhaseNumber = snapshot.PhaseNumber
	checkpoint.AccumulatedResults = make(map[string]*StepResult, len(snapshot.AccumulatedResults))
	if checkpoint.StepResults == nil {
		checkpoint.StepResults = make(map[string]*StepResult)
	}
	for key, value := range snapshot.AccumulatedResults {
		if value == nil {
			continue
		}
		copy := *value
		checkpoint.AccumulatedResults[key] = &copy
		if _, exists := checkpoint.StepResults[key]; !exists {
			stepCopy := copy
			checkpoint.StepResults[key] = &stepCopy
		}
	}
	checkpoint.ExecutedStepIDs = append([]string(nil), snapshot.ExecutedStepIDs...)
	checkpoint.ContinuationNote = snapshot.ContinuationNote
	rebuildCheckpointCompletedSteps(checkpoint)
}

func (o *AIOrchestrator) saveAuthoritativeCheckpoint(
	ctx context.Context,
	checkpoint *ExecutionCheckpoint,
	snapshot executionRunSnapshot,
	site string,
) error {
	coordinator := checkpointCoordinator{controller: o.interruptController}
	if err := coordinator.saveAuthoritative(ctx, checkpoint, snapshot); err != nil {
		reason := checkpointFailureReason(err)
		errorType := checkpointFailureErrorType(err)
		telemetry.Counter("orchestration.checkpoint.enrichment",
			"module", telemetry.ModuleOrchestration,
			"site", site,
			"status", "error",
		)
		telemetry.AddSpanEvent(ctx, "hitl.enriched_checkpoint.save_failed",
			attribute.String("request_id", GetRequestID(ctx)),
			attribute.String("checkpoint_id", checkpoint.CheckpointID),
			attribute.String("original_request_id", checkpoint.OriginalRequestID),
			attribute.String("reason", reason),
			attribute.String("error_type", errorType),
			attribute.String("site", site),
		)
		if o.logger != nil {
			o.logger.WarnWithContext(ctx, "Failed to save authoritative checkpoint", map[string]interface{}{
				"operation":           "checkpoint_enrichment",
				"request_id":          GetRequestID(ctx),
				"original_request_id": checkpoint.OriginalRequestID,
				"checkpoint_id":       checkpoint.CheckpointID,
				"site":                site,
				"status":              "error",
				"reason":              reason,
				"error_type":          errorType,
				"error":               err.Error(),
			})
		}
		return err
	}
	telemetry.AddSpanEvent(ctx, "hitl.enriched_checkpoint.saved",
		attribute.String("request_id", GetRequestID(ctx)),
		attribute.String("checkpoint_id", checkpoint.CheckpointID),
		attribute.String("original_request_id", checkpoint.OriginalRequestID),
		attribute.Int("step_results_count", len(checkpoint.StepResults)),
		attribute.String("site", site),
	)
	return nil
}

func checkpointFailureReason(err error) string {
	switch {
	case errors.Is(err, ErrCheckpointEnrichmentUnsupported):
		return "unsupported"
	case errors.Is(err, ErrCheckpointAuthoritativeSave):
		return "save_failed"
	default:
		return "unknown"
	}
}

func checkpointFailureErrorType(err error) string {
	switch {
	case errors.Is(err, ErrCheckpointEnrichmentUnsupported):
		return "preparation"
	case errors.Is(err, ErrCheckpointAuthoritativeSave):
		return "store_write"
	default:
		return "unknown"
	}
}

func cloneInterfaceMap(source map[string]interface{}) map[string]interface{} {
	if source == nil {
		return nil
	}
	cloned := make(map[string]interface{}, len(source))
	for key, value := range source {
		cloned[key] = cloneInterfaceValue(value)
	}
	return cloned
}

func cloneInterfaceValue(value interface{}) interface{} {
	switch typed := value.(type) {
	case map[string]interface{}:
		return cloneInterfaceMap(typed)
	case []interface{}:
		cloned := make([]interface{}, len(typed))
		for index, item := range typed {
			cloned[index] = cloneInterfaceValue(item)
		}
		return cloned
	case map[string]string:
		return cloneStringMap(typed)
	case []string:
		return append([]string(nil), typed...)
	case []int:
		return append([]int(nil), typed...)
	case []float64:
		return append([]float64(nil), typed...)
	case []bool:
		return append([]bool(nil), typed...)
	default:
		// Structs and opaque leaves are treated as immutable values. Framework
		// lifecycle snapshots never mutate values supplied by feature extensions.
		return value
	}
}
