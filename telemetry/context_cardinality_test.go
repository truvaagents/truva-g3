package telemetry

import (
	"context"
	"strings"
	"testing"

	"go.opentelemetry.io/otel/propagation"
)

var reservedCorrelationKeys = []string{
	"request_id",
	"original_request_id",
	"conversation_id",
	"checkpoint_id",
	"user_id",
	"session_id",
	"trace_id",
	"span_id",
	"plan_id",
	"step_id",
	"pass_id",
	"investigation_owner",
	"provider_request_id",
}

func TestReservedCorrelationBaggageIsNeverMetricEligible(t *testing.T) {
	t.Run("legacy baggage", func(t *testing.T) {
		pairs := make([]string, 0, len(reservedCorrelationKeys)*2+2)
		for _, key := range reservedCorrelationKeys {
			pairs = append(pairs, key, "correlation-value")
		}
		pairs = append(pairs, "bounded_dimension", "stable")
		ctx := WithBaggage(context.Background(), pairs...)
		assertReservedCorrelationContract(t, ctx)
	})

	t.Run("exact baggage explicit eligibility cannot override deny list", func(t *testing.T) {
		ctx := context.Background()
		for _, key := range reservedCorrelationKeys {
			var err error
			ctx, err = WithBaggageExact(
				ctx,
				key,
				"correlation-value",
				WithMetricLabelEligibility(true),
			)
			if err != nil {
				t.Fatalf("WithBaggageExact(%q): %v", key, err)
			}
		}
		var err error
		ctx, err = WithBaggageExact(
			ctx,
			"bounded_dimension",
			"stable",
			WithMetricLabelEligibility(true),
		)
		if err != nil {
			t.Fatal(err)
		}
		assertReservedCorrelationContract(t, ctx)
	})

	t.Run("incoming W3C baggage", func(t *testing.T) {
		members := make([]string, 0, len(reservedCorrelationKeys)+1)
		for _, key := range reservedCorrelationKeys {
			members = append(members, key+"=correlation-value;truvag3_metric_label=true")
		}
		members = append(members, "bounded_dimension=stable;truvag3_metric_label=true")
		carrier := propagation.MapCarrier{"baggage": strings.Join(members, ",")}
		ctx := (propagation.Baggage{}).Extract(context.Background(), carrier)
		assertReservedCorrelationContract(t, ctx)
	})
}

func assertReservedCorrelationContract(t *testing.T, ctx context.Context) {
	t.Helper()
	bag := GetBaggage(ctx)
	labels := appendBaggageToLabels(ctx, nil)
	for _, key := range reservedCorrelationKeys {
		if bag[key] != "correlation-value" {
			t.Errorf("baggage %q = %q, want preserved correlation value", key, bag[key])
		}
		if labelsContainKey(labels, key) {
			t.Errorf("reserved correlation key %q reached metric labels: %v", key, labels)
		}
	}
	if bag["bounded_dimension"] != "stable" || !labelsContainKey(labels, "bounded_dimension") {
		t.Errorf("bounded application baggage lost existing behavior: baggage=%v labels=%v", bag, labels)
	}

	span := &commonAttributeCapture{}
	SetCommonAttrsOn(ctx, span)
	if bag["request_id"] != "" && span.attributes["request_id"] != "correlation-value" {
		t.Errorf("request_id common span attribute = %#v", span.attributes["request_id"])
	}
}
