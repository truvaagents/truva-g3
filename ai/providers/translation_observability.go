package providers

import (
	"context"

	"github.com/truvaagents/truva-g3/core"
	"github.com/truvaagents/truva-g3/telemetry"
	"go.opentelemetry.io/otel/attribute"
)

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
