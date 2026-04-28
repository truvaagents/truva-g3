package providers

import (
	"context"

	"github.com/truvaagents/truva-g3/core"
	"github.com/truvaagents/truva-g3/telemetry"
	"go.opentelemetry.io/otel/attribute"
)

func LogTranslationDegraded(ctx context.Context, logger core.Logger, providerAlias, model, warningType, capability string) {
	requestID := core.GetRequestID(ctx)

	telemetry.AddSpanEvent(ctx, "ai.translation.degraded",
		attribute.String("request_id", requestID),
		attribute.String("provider_alias", providerAlias),
		attribute.String("model", model),
		attribute.String("warning_type", warningType),
		attribute.String("capability", capability),
	)

	telemetry.Counter("ai.translation.warnings_total",
		"module", telemetry.ModuleAI,
		"provider_alias", providerAlias,
		"warning_type", warningType,
		"capability", capability,
	)

	if logger != nil {
		logger.WarnWithContext(ctx, "AI request degraded for unsupported provider capability", map[string]interface{}{
			"operation":      "ai_request",
			"request_id":     requestID,
			"provider_alias": providerAlias,
			"model":          model,
			"status":         "degraded",
			"warning_type":   warningType,
			"capability":     capability,
		})
	}
}
