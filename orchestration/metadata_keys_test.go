package orchestration

import (
	"strings"
	"testing"

	"github.com/truvaagents/truva-g3/core"
)

func TestConversationIDCandidateFromMetadata(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		metadata  map[string]interface{}
		candidate conversationIDCandidate
	}{
		{
			name:     "absent",
			metadata: map[string]interface{}{},
		},
		{
			name: "valid",
			metadata: map[string]interface{}{
				MetadataConversationID: "conversation-1",
			},
			candidate: conversationIDCandidate{
				Present: true,
				Value:   "conversation-1",
			},
		},
		{
			name: "wrong type",
			metadata: map[string]interface{}{
				MetadataConversationID: 42,
			},
			candidate: conversationIDCandidate{
				Present: true,
				Reason:  core.ConversationIDValidationInvalidType,
			},
		},
		{
			name: "empty",
			metadata: map[string]interface{}{
				MetadataConversationID: "",
			},
			candidate: conversationIDCandidate{
				Present: true,
				Reason:  core.ConversationIDValidationEmpty,
			},
		},
		{
			name: "invalid character",
			metadata: map[string]interface{}{
				MetadataConversationID: "conversation 1",
			},
			candidate: conversationIDCandidate{
				Present: true,
				Reason:  core.ConversationIDValidationInvalidCharacter,
			},
		},
		{
			name: "too long",
			metadata: map[string]interface{}{
				MetadataConversationID: strings.Repeat("a", core.MaxConversationIDLength+1),
			},
			candidate: conversationIDCandidate{
				Present: true,
				Reason:  core.ConversationIDValidationTooLong,
			},
		},
		{
			name: "session ID is not promoted",
			metadata: map[string]interface{}{
				"session_id": "application-session",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			candidate := conversationIDCandidateFromMetadata(tt.metadata)
			if candidate != tt.candidate {
				t.Fatalf("candidate = %+v, want %+v", candidate, tt.candidate)
			}
			if value := conversationIDFromMetadata(tt.metadata); value != tt.candidate.Value {
				t.Fatalf("conversationIDFromMetadata() = %q, want %q", value, tt.candidate.Value)
			}
		})
	}
}
