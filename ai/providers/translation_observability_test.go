package providers

import (
	"context"
	"testing"

	"github.com/truvaagents/truva-g3/telemetry"
)

func TestLogTranslationDegradedCarriesBoundedRequestMetadata(t *testing.T) {
	logger := &mockLogger{}
	ctx := telemetry.WithBaggage(context.Background(), "request_id", "translation-request")
	LogTranslationDegraded(
		ctx,
		logger,
		"azureopenai.v1",
		"private-semantic-model",
		"reasoning_effort_stripped",
		"reasoning_effort",
	)
	if len(logger.warnCalls) != 1 {
		t.Fatalf("warning calls = %d", len(logger.warnCalls))
	}
	fields := logger.warnCalls[0]
	want := map[string]interface{}{
		"operation":      "ai_request",
		"provider_alias": "azureopenai.v1",
		"model":          "private-semantic-model",
		"status":         "degraded",
		"warning_type":   "reasoning_effort_stripped",
		"capability":     "reasoning_effort",
		"request_id":     "translation-request",
	}
	for key, value := range want {
		if fields[key] != value {
			t.Fatalf("fields[%q] = %#v, want %#v; fields=%#v", key, fields[key], value, fields)
		}
	}
}

func TestLogTranslationDegradedToleratesNilContextAndLogger(t *testing.T) {
	LogTranslationDegraded(nil, nil, "openai", "model", "unsupported", "feature")
}
