package providers

import (
	"context"

	"github.com/truvaagents/truva-g3/core"
	"github.com/truvaagents/truva-g3/telemetry"
	"go.opentelemetry.io/otel/attribute"
)

// LogInheritedEffortOmitted reports normal adaptation of a client default to a
// model without portable reasoning support. The adapter also retains its request
// adjustment. Explicit/legacy request degradation continues through the separate
// warning helper below; normal adaptation emits no degraded event or metric.
func LogInheritedEffortOmitted(ctx context.Context, logger core.Logger, providerAlias, model string) {
	if logger == nil {
		return
	}
	fields := map[string]interface{}{
		"operation": "ai_request", "provider_alias": providerAlias, "model": model,
		"status": "adapted", "capability": "reasoning_effort", "source": "client-default",
		"reason": "unsupported_model_capability",
	}
	AddObservationRequestID(ctx, fields)
	logger.DebugWithContext(ctx, "Inapplicable AI client reasoning default omitted", fields)
}

func LogTranslationDegraded(ctx context.Context, logger core.Logger, providerAlias, model, warningType, capability string) {
	fields := map[string]interface{}{
		"operation":      "ai_request",
		"provider_alias": providerAlias,
		"model":          model,
		"status":         "degraded",
		"warning_type":   warningType,
		"capability":     capability,
	}
	AddObservationRequestID(ctx, fields)
	requestID, _ := fields["request_id"].(string)

	telemetry.AddSpanEvent(ctx, "ai.translation.degraded",
		attribute.String("request_id", requestID),
		attribute.String("provider_alias", providerAlias),
		attribute.String("model", model),
		attribute.String("warning_type", warningType),
		attribute.String("capability", capability),
	)

	telemetry.Counter("ai.translation.warnings_total",
		"module", telemetry.ModuleAI,
		"status", "degraded",
	)

	if logger != nil {
		logger.WarnWithContext(ctx, "AI request degraded for unsupported provider capability", fields)
	}
}
