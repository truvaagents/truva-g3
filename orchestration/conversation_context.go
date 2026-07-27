package orchestration

import (
	"context"

	"github.com/truvaagents/truva-g3/core"
	"github.com/truvaagents/truva-g3/telemetry"
)

const (
	conversationIDSourceMetadata     = "metadata"
	conversationIDSourceBaggage      = "baggage"
	conversationIDSourceBaggageExact = "baggage_exact"

	conversationIDRejectionMetric = "orchestration.conversation_id.rejected.total"
)

type ingressConversationCandidate struct {
	present bool
	value   string
	source  string
	reason  core.ConversationIDValidationReason
}

// resolveConversationContext is the single orchestration-ingress authority for
// selecting, validating, and promoting a conversation ID. Correlation failure
// is fail-open: business execution continues with the identity fully scrubbed.
func (o *AIOrchestrator) resolveConversationContext(
	ctx context.Context,
	metadata map[string]interface{},
) (context.Context, string, map[string]interface{}) {
	if ctx == nil {
		ctx = context.Background()
	}

	metadataCandidate := conversationIDCandidateFromMetadata(metadata)
	coreCandidate := core.GetConversationIDCandidate(ctx)
	baggageValues := telemetry.GetBaggage(ctx)
	baggageValue, baggagePresent := baggageValues[MetadataConversationID]

	requestMetadata := cloneMetadata(metadata)
	delete(requestMetadata, MetadataConversationID)

	ctx = core.WithoutConversationID(ctx)
	ctx = telemetry.WithoutBaggageMember(ctx, MetadataConversationID)

	selected := ingressConversationCandidate{}
	switch {
	case metadataCandidate.Present:
		selected = ingressConversationCandidate{
			present: true,
			value:   metadataCandidate.Value,
			source:  conversationIDSourceMetadata,
			reason:  metadataCandidate.Reason,
		}
	case coreCandidate.Present:
		selected = ingressConversationCandidate{
			present: true,
			value:   coreCandidate.Value,
			source:  boundedCoreConversationSource(coreCandidate.Source),
			reason:  coreCandidate.RejectionReason,
		}
	case baggagePresent:
		selected = ingressConversationCandidate{
			present: true,
			value:   baggageValue,
			source:  conversationIDSourceBaggage,
		}
	}

	if !selected.present {
		return ctx, "", requestMetadata
	}
	if selected.reason != core.ConversationIDValidationNone {
		o.emitConversationIDRejection(selected.source, string(selected.reason))
		return ctx, "", requestMetadata
	}
	if reason := core.ValidateConversationID(selected.value); reason != core.ConversationIDValidationNone {
		o.emitConversationIDRejection(selected.source, string(reason))
		return ctx, "", requestMetadata
	}

	baggageCtx, err := telemetry.WithBaggageExact(
		ctx,
		MetadataConversationID,
		selected.value,
		telemetry.WithMetricLabelEligibility(false),
	)
	if err != nil {
		o.emitConversationIDRejection(
			conversationIDSourceBaggageExact,
			conversationIDBaggageRejectionReason(err),
		)
		return ctx, "", requestMetadata
	}

	if requestMetadata == nil {
		requestMetadata = make(map[string]interface{})
	}
	requestMetadata[MetadataConversationID] = selected.value
	return core.WithConversationID(baggageCtx, selected.value), selected.value, requestMetadata
}

func boundedCoreConversationSource(source core.ConversationIDCandidateSource) string {
	if source == core.ConversationIDSourceCoreHeader {
		return string(core.ConversationIDSourceCoreHeader)
	}
	return string(core.ConversationIDSourceCoreContext)
}

func (o *AIOrchestrator) emitConversationIDRejection(source, reason string) {
	if o == nil || o.telemetry == nil {
		return
	}
	o.telemetry.RecordMetric(conversationIDRejectionMetric, 1, map[string]string{
		"source": source,
		"reason": reason,
		"module": telemetry.ModuleOrchestration,
	})
}

func conversationIDBaggageRejectionReason(err error) string {
	reason, ok := telemetry.BaggageExactErrorReasonOf(err)
	if !ok {
		return "invalid_baggage_key"
	}
	switch reason {
	case telemetry.BaggageExactInvalidUTF8:
		return string(core.ConversationIDValidationInvalidUTF8)
	case telemetry.BaggageExactKeyTooLong, telemetry.BaggageExactValueTooLong:
		return string(core.ConversationIDValidationTooLong)
	case telemetry.BaggageExactItemLimit:
		return "item_limit"
	case telemetry.BaggageExactTotalSize:
		return "total_size"
	default:
		return "invalid_baggage_key"
	}
}

func cloneMetadata(metadata map[string]interface{}) map[string]interface{} {
	if metadata == nil {
		return nil
	}
	cloned := make(map[string]interface{}, len(metadata))
	for key, value := range metadata {
		cloned[key] = value
	}
	return cloned
}

func mergeMetadata(
	base map[string]interface{},
	overlay map[string]interface{},
) map[string]interface{} {
	if len(overlay) == 0 {
		return base
	}
	if base == nil {
		base = make(map[string]interface{}, len(overlay))
	}
	for key, value := range overlay {
		base[key] = value
	}
	return base
}

func withCheckpointMetadata(
	ctx context.Context,
	requestMetadata map[string]interface{},
	conversationID string,
) context.Context {
	checkpointMetadata := cloneMetadata(GetMetadata(ctx))
	checkpointMetadata = mergeMetadata(checkpointMetadata, requestMetadata)
	delete(checkpointMetadata, MetadataConversationID)
	if conversationID != "" {
		if checkpointMetadata == nil {
			checkpointMetadata = make(map[string]interface{})
		}
		checkpointMetadata[MetadataConversationID] = conversationID
	}
	if len(checkpointMetadata) == 0 {
		return ctx
	}
	return WithMetadata(ctx, checkpointMetadata)
}
