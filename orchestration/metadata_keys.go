package orchestration

import "github.com/truvaagents/truva-g3/core"

const (
	MetadataConversationTurns = "conversation_turns"
	MetadataConversationID    = "conversation_id"
)

type conversationIDCandidate struct {
	Present bool
	Value   string
	Reason  core.ConversationIDValidationReason
}

func conversationIDCandidateFromMetadata(metadata map[string]interface{}) conversationIDCandidate {
	raw, present := metadata[MetadataConversationID]
	if !present {
		return conversationIDCandidate{}
	}
	value, ok := raw.(string)
	if !ok {
		return conversationIDCandidate{
			Present: true,
			Reason:  core.ConversationIDValidationInvalidType,
		}
	}
	if reason := core.ValidateConversationID(value); reason != core.ConversationIDValidationNone {
		return conversationIDCandidate{
			Present: true,
			Reason:  reason,
		}
	}
	return conversationIDCandidate{
		Present: true,
		Value:   value,
	}
}

func conversationIDFromMetadata(metadata map[string]interface{}) string {
	return conversationIDCandidateFromMetadata(metadata).Value
}
