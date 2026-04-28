package orchestration

import (
	"context"
	"fmt"

	"github.com/truvaagents/truva-g3/core"
)

// Compile-time interface compliance check.
var _ core.BeforePlanningHook = (*ConversationHistoryHook)(nil)

// ConversationHistoryHook is an adapter that reads conversation turns from
// memory and delegates preparation to the shared conversation-history preparer.
type ConversationHistoryHook struct {
	memory    core.ConversationMemory
	sessionID string
	maxTurns  int
	logger    core.Logger
	preparer  ConversationHistoryPreparer
}

// ConversationHistoryOption configures ConversationHistoryHook.
type ConversationHistoryOption func(*ConversationHistoryHook) error

type conversationHistoryPathAwarePreparer interface {
	prepareFromTextWithPath(ctx context.Context, sessionKey string, formatted string, path string) (string, HistoryPreparationResult, error)
	prepareFromTurnsWithPath(ctx context.Context, sessionKey string, turns []core.ConversationTurn, path string) (string, HistoryPreparationResult, error)
}

// WithHistoryMaxTurns sets the maximum number of turns read from ConversationMemory
// when the memory backend does not implement core.FullConversationMemory.
func WithHistoryMaxTurns(maxTurns int) ConversationHistoryOption {
	return func(h *ConversationHistoryHook) error {
		if maxTurns <= 0 {
			return fmt.Errorf("maxTurns must be positive, got %d", maxTurns)
		}
		h.maxTurns = maxTurns
		return nil
	}
}

// WithHistoryLogger sets the logger used by the hook.
func WithHistoryLogger(logger core.Logger) ConversationHistoryOption {
	return func(h *ConversationHistoryHook) error {
		if logger == nil {
			return fmt.Errorf("logger cannot be nil: use &core.NoOpLogger{} to disable logging")
		}
		h.logger = logger
		return nil
	}
}

// WithConversationHistoryPreparer injects the shared preparer used by both the
// metadata path and the hook path.
func WithConversationHistoryPreparer(preparer ConversationHistoryPreparer) ConversationHistoryOption {
	return func(h *ConversationHistoryHook) error {
		if preparer == nil {
			return fmt.Errorf("conversation history preparer cannot be nil")
		}
		h.preparer = preparer
		return nil
	}
}

// NewConversationHistoryHook creates a hook that adapts memory-backed history
// into the shared conversation-history preparation path.
func NewConversationHistoryHook(
	memory core.ConversationMemory,
	sessionID string,
	opts ...ConversationHistoryOption,
) (*ConversationHistoryHook, error) {
	h := &ConversationHistoryHook{
		memory:    memory,
		sessionID: sessionID,
		maxTurns:  20,
		logger:    &core.NoOpLogger{},
	}
	for _, opt := range opts {
		if err := opt(h); err != nil {
			return nil, fmt.Errorf("invalid conversation history option: %w", err)
		}
	}
	if h.preparer == nil {
		return nil, fmt.Errorf("conversation history preparer is required: inject via WithConversationHistoryPreparer(...)")
	}
	return h, nil
}

// Name returns the hook name for telemetry spans.
func (h *ConversationHistoryHook) Name() string { return "conversation-history" }

// SetLogger updates the hook logger and forwards it to the preparer when supported.
func (h *ConversationHistoryHook) SetLogger(logger core.Logger) {
	if logger == nil {
		h.logger = &core.NoOpLogger{}
	} else if cal, ok := logger.(core.ComponentAwareLogger); ok {
		h.logger = cal.WithComponent("framework/orchestration")
	} else {
		h.logger = logger
	}
	if aware, ok := h.preparer.(interface{ SetLogger(core.Logger) }); ok {
		aware.SetLogger(logger)
	}
}

// SetTelemetry forwards telemetry to the preparer when supported.
func (h *ConversationHistoryHook) SetTelemetry(telemetry core.Telemetry) {
	if aware, ok := h.preparer.(interface{ SetTelemetry(core.Telemetry) }); ok {
		aware.SetTelemetry(telemetry)
	}
}

// SetLLMDebugStore forwards the debug store to the preparer when supported.
func (h *ConversationHistoryHook) SetLLMDebugStore(store LLMDebugStore) {
	if aware, ok := h.preparer.(interface{ SetLLMDebugStore(LLMDebugStore) }); ok {
		aware.SetLLMDebugStore(store)
	}
}

// BeforePlanning reads conversation history and injects the prepared value into enrichments.
func (h *ConversationHistoryHook) BeforePlanning(ctx context.Context, pctx *core.PipelineContext) (*core.PipelineShortCircuit, error) {
	if h.memory == nil || h.preparer == nil || h.sessionID == "" {
		return nil, nil
	}

	requestID := requestIDFromBaggage(ctx)
	originalRequestID := originalRequestIDFromBaggage(ctx)

	var (
		prepared string
		err      error
	)

	if fullMemory, ok := h.memory.(core.FullConversationMemory); ok {
		turns, getErr := fullMemory.GetFullHistory(ctx, h.sessionID)
		if getErr != nil {
			h.logger.WarnWithContext(ctx, "Failed to retrieve full conversation history, skipping", map[string]interface{}{
				"operation":           "conversation_history",
				"request_id":          requestID,
				"original_request_id": originalRequestID,
				"session_id":          h.sessionID,
				"error":               getErr.Error(),
				"error_type":          "session_read",
			})
			return nil, nil
		}
		if len(turns) > 0 {
			if pathAware, ok := h.preparer.(conversationHistoryPathAwarePreparer); ok {
				prepared, _, err = pathAware.prepareFromTurnsWithPath(ctx, h.sessionID, turns, "hook")
			} else {
				prepared, _, err = h.preparer.PrepareFromTurns(ctx, h.sessionID, turns)
			}
		} else {
			turns, getErr = h.memory.GetHistory(ctx, h.sessionID, h.maxTurns)
			if getErr != nil {
				h.logger.WarnWithContext(ctx, "Failed to retrieve conversation history, skipping", map[string]interface{}{
					"operation":           "conversation_history",
					"request_id":          requestID,
					"original_request_id": originalRequestID,
					"session_id":          h.sessionID,
					"error":               getErr.Error(),
					"error_type":          "session_read",
				})
				return nil, nil
			}
			if len(turns) == 0 {
				return nil, nil
			}
			if pathAware, ok := h.preparer.(conversationHistoryPathAwarePreparer); ok {
				prepared, _, err = pathAware.prepareFromTextWithPath(ctx, h.sessionID, formatConversationTurns(turns), "hook")
			} else {
				prepared, _, err = h.preparer.PrepareFromText(ctx, h.sessionID, formatConversationTurns(turns))
			}
		}
	} else {
		turns, getErr := h.memory.GetHistory(ctx, h.sessionID, h.maxTurns)
		if getErr != nil {
			h.logger.WarnWithContext(ctx, "Failed to retrieve conversation history, skipping", map[string]interface{}{
				"operation":           "conversation_history",
				"request_id":          requestID,
				"original_request_id": originalRequestID,
				"session_id":          h.sessionID,
				"error":               getErr.Error(),
				"error_type":          "session_read",
			})
			return nil, nil
		}
		if len(turns) == 0 {
			return nil, nil
		}
		if pathAware, ok := h.preparer.(conversationHistoryPathAwarePreparer); ok {
			prepared, _, err = pathAware.prepareFromTextWithPath(ctx, h.sessionID, formatConversationTurns(turns), "hook")
		} else {
			prepared, _, err = h.preparer.PrepareFromText(ctx, h.sessionID, formatConversationTurns(turns))
		}
	}

	if err != nil {
		h.logger.WarnWithContext(ctx, "Failed to prepare conversation history, skipping", map[string]interface{}{
			"operation":           "conversation_history",
			"request_id":          requestID,
			"original_request_id": originalRequestID,
			"session_id":          h.sessionID,
			"error":               err.Error(),
			"error_type":          "preparation",
		})
		return nil, nil
	}
	if prepared == "" {
		return nil, nil
	}

	if pctx.Enrichments == nil {
		pctx.Enrichments = make(map[string]interface{})
	}
	pctx.Enrichments[core.EnrichmentConversationHistory] = prepared
	return nil, nil
}
