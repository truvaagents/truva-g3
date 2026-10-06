package orchestration

import (
	"context"
	"testing"
	"time"

	"github.com/truvaagents/truva-g3/core"
	"go.opentelemetry.io/otel"
)

func TestLegacyInvestigationOwnershipCorrelation(t *testing.T) {
	for _, requestID := range []string{"legacy-request", ""} {
		t.Run("request="+requestID, func(t *testing.T) {
			claims := 0
			coordinator := &core.MockInvestigationCoordinator{
				ClaimFn: func(_ context.Context, agent, entity string, ttl time.Duration) (bool, string, error) {
					claims++
					if agent != "agent" || entity != "doc-1" || ttl != 0 {
						t.Fatal("legacy claim semantics changed")
					}
					return true, "", nil
				},
			}
			hook, err := NewMemoryEnrichmentHook(&core.MockEpisodicMemory{}, coordinator, "agent", "documents")
			if err != nil {
				t.Fatal(err)
			}
			recorder := setupExecutorTestTracer(t)
			ctx, span := otel.Tracer("legacy-correlation-test").Start(WithRequestID(t.Context(), requestID), "legacy")
			_, err = hook.BeforePlanning(ctx, &core.PipelineContext{Request: "read", Metadata: map[string]interface{}{"entity_type": "document", "entity_id": "doc-1"}})
			span.End()
			if err != nil || claims != 1 {
				t.Fatalf("legacy hook behavior changed: claims=%d err=%v", claims, err)
			}
			found := 0
			for _, recorded := range recorder.Ended() {
				for _, event := range recorded.Events() {
					if event.Name != "memory.investigation.legacy_ownership" {
						continue
					}
					found++
					attrs := event.Attributes
					if len(attrs) != 2 || string(attrs[0].Key) != "request_id" || attrs[0].Value.AsString() != requestID || string(attrs[1].Key) != "ownership" || attrs[1].Value.AsString() != "agent_name_only" {
						t.Fatalf("incorrect correlated event: %+v", attrs)
					}
				}
			}
			if found != 1 {
				t.Fatalf("expected exactly one legacy event, got %d", found)
			}
		})
	}
}
