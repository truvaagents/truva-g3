package orchestration

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/truvaagents/truva-g3/core"
	"github.com/truvaagents/truva-g3/telemetry"
	"go.opentelemetry.io/otel/attribute"
)

// =============================================================================
// WebhookInterruptHandler - Reference Implementation
// =============================================================================
//
// WebhookInterruptHandler implements InterruptHandler using webhooks.
// This is the framework's reference implementation.
//
// Per ARCHITECTURE.md Three-Layer Resilience, it supports optional circuit breaker injection.
// Per DISTRIBUTED_TRACING_GUIDE.md, it uses TracedHTTPClient for trace propagation.
// Per FRAMEWORK_DESIGN_PRINCIPLES.md, uses interface-first design for CommandStore.
//
// Usage:
//
//	// Production usage (requires RedisCommandStore for distributed command delivery)
//	cmdStore, _ := NewRedisCommandStore(WithCommandStoreRedisURL(redisURL))
//	handler := NewWebhookInterruptHandler(
//	    "https://my-service/hitl/webhook",
//	    cmdStore,
//	    WithHandlerCircuitBreaker(cb),
//	    WithHandlerLogger(logger),
//	)
//
// =============================================================================

// WebhookInterruptHandler implements InterruptHandler using webhooks.
type WebhookInterruptHandler struct {
	webhookURL   string
	httpClient   *http.Client
	commandStore CommandStore // For distributed command delivery (interface per FRAMEWORK_DESIGN_PRINCIPLES.md)

	// Optional dependencies (injected per framework patterns)
	circuitBreaker core.CircuitBreaker // Layer 2: Optional, injected
	logger         core.Logger         // Optional, defaults to NoOp
	telemetry      core.Telemetry      // Optional, defaults to NoOp
}

// NewWebhookInterruptHandler creates a handler with required config.
// Returns concrete type per Go idiom "return structs, accept interfaces".
// Uses TracedHTTPClient by default for distributed tracing (per DISTRIBUTED_TRACING_GUIDE.md).
//
// CommandStore is required for distributed command delivery. Use NewRedisCommandStore
// to create a production-ready store backed by Redis Pub/Sub.
func NewWebhookInterruptHandler(webhookURL string, commandStore CommandStore, opts ...WebhookHandlerOption) *WebhookInterruptHandler {
	h := &WebhookInterruptHandler{
		webhookURL:   webhookURL,
		commandStore: commandStore,
		// Use TracedHTTPClient for trace context propagation (per DISTRIBUTED_TRACING_GUIDE.md)
		// Config matches gold standard from agent-with-orchestration/research_agent.go
		httpClient: telemetry.NewTracedHTTPClientWithTransport(&http.Transport{
			MaxIdleConns:        100,
			MaxIdleConnsPerHost: 10,
			IdleConnTimeout:     90 * time.Second,
			DisableKeepAlives:   false, // Gold standard config
			ForceAttemptHTTP2:   true,  // Gold standard config
		}),
		logger: &core.NoOpLogger{}, // Safe default per framework
	}
	h.httpClient.Timeout = 30 * time.Second

	for _, opt := range opts {
		opt(h)
	}
	return h
}

// -----------------------------------------------------------------------------
// InterruptHandler Implementation
// -----------------------------------------------------------------------------

// NotifyInterrupt sends webhook notification about a pending interrupt.
// Uses Three-Layer Resilience: Layer 2 circuit breaker if injected, else Layer 1 retry.
func (h *WebhookInterruptHandler) NotifyInterrupt(ctx context.Context, checkpoint *ExecutionCheckpoint) error {
	// Layer 2: Use injected circuit breaker if provided
	if h.circuitBreaker != nil {
		return h.circuitBreaker.Execute(ctx, func() error {
			return h.doNotify(ctx, checkpoint)
		})
	}

	// Layer 1: Built-in simple resilience (3 retries with backoff)
	return h.doNotifyWithRetry(ctx, checkpoint)
}

// doNotify sends the webhook notification.
func (h *WebhookInterruptHandler) doNotify(ctx context.Context, checkpoint *ExecutionCheckpoint) error {
	// Start timing for webhook duration metric (Phase 4 - Metrics Integration)
	startTime := time.Now()

	// Build webhook payload
	payload := &WebhookPayload{
		Type:           "interrupt",
		CheckpointID:   checkpoint.CheckpointID,
		RequestID:      checkpoint.RequestID,
		InterruptPoint: string(checkpoint.InterruptPoint),
		Decision:       checkpoint.Decision,
		Plan:           checkpoint.Plan,
		CurrentStep:    checkpoint.CurrentStep,
		CreatedAt:      checkpoint.CreatedAt,
		ExpiresAt:      checkpoint.ExpiresAt,
	}

	data, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("failed to marshal webhook payload: %w", err)
	}

	// CRITICAL: Use NewRequestWithContext to propagate trace (per DISTRIBUTED_TRACING_GUIDE.md)
	// #nosec G704 -- webhookURL is an operator-configured callback endpoint.
	req, err := http.NewRequestWithContext(ctx, "POST", h.webhookURL, bytes.NewReader(data))
	if err != nil {
		return fmt.Errorf("failed to create webhook request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-TruvaG3-Event", "hitl.interrupt")
	req.Header.Set("X-TruvaG3-Checkpoint-ID", checkpoint.CheckpointID)
	if checkpoint.RequestID != "" {
		req.Header.Set("X-TruvaG3-Request-ID", checkpoint.RequestID)
	}

	// TracedHTTPClient automatically adds traceparent header.
	// #nosec G704 -- webhookURL is an operator-configured callback endpoint.
	resp, err := h.httpClient.Do(req)
	if err != nil {
		// Record webhook failure metrics (Phase 4 - Metrics Integration)
		duration := time.Since(startTime).Seconds()
		RecordWebhookDuration(duration, false)
		RecordWebhookSent(false, 0)
		return fmt.Errorf("failed to send webhook to %s: %w (check TRUVAG3_HITL_WEBHOOK_URL and endpoint availability)", h.webhookURL, err)
	}
	defer func() { _ = resp.Body.Close() }() // Error intentionally ignored in cleanup

	// Record webhook duration (Phase 4 - Metrics Integration)
	duration := time.Since(startTime).Seconds()
	success := resp.StatusCode < 400
	RecordWebhookDuration(duration, success)
	RecordWebhookSent(success, resp.StatusCode)

	if resp.StatusCode >= 400 {
		return fmt.Errorf("webhook returned error status %d from %s", resp.StatusCode, h.webhookURL)
	}

	// Add span event for successful notification
	telemetry.AddSpanEvent(ctx, "hitl.webhook.sent",
		attribute.String("checkpoint_id", checkpoint.CheckpointID),
		attribute.String("webhook_url", h.webhookURL),
		attribute.Int("status_code", resp.StatusCode),
	)

	if h.logger != nil {
		h.logger.DebugWithContext(ctx, "Webhook notification sent", map[string]interface{}{
			"operation":     "hitl_webhook_notify",
			"checkpoint_id": checkpoint.CheckpointID,
			"webhook_url":   h.webhookURL,
			"status_code":   resp.StatusCode,
		})
	}

	return nil
}

// doNotifyWithRetry sends webhook with Layer 1 built-in retry.
func (h *WebhookInterruptHandler) doNotifyWithRetry(ctx context.Context, checkpoint *ExecutionCheckpoint) error {
	var lastErr error
	backoff := 50 * time.Millisecond

	for attempt := 1; attempt <= 3; attempt++ {
		if err := h.doNotify(ctx, checkpoint); err != nil {
			lastErr = err
			if h.logger != nil {
				h.logger.WarnWithContext(ctx, "Webhook notification failed, retrying", map[string]interface{}{
					"operation":     "hitl_webhook_retry",
					"checkpoint_id": checkpoint.CheckpointID,
					"attempt":       attempt,
					"error":         err.Error(),
				})
			}
			time.Sleep(backoff)
			backoff *= 2 // Exponential backoff
			continue
		}
		return nil
	}

	return fmt.Errorf("webhook notification failed after 3 retries: %w", lastErr)
}

// WaitForCommand blocks until human responds or timeout.
// Uses CommandStore interface for distributed command delivery.
func (h *WebhookInterruptHandler) WaitForCommand(ctx context.Context, checkpointID string, timeout time.Duration) (*Command, error) {
	// Subscribe to commands for this checkpoint via CommandStore
	cmdChan, cleanup, err := h.commandStore.SubscribeCommand(ctx, checkpointID)
	if err != nil {
		return nil, fmt.Errorf("failed to subscribe to commands: %w", err)
	}
	defer cleanup()

	// Wait for command or timeout
	select {
	case cmd := <-cmdChan:
		return cmd, nil
	case <-time.After(timeout):
		return nil, &ErrCheckpointExpired{CheckpointID: checkpointID}
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// SubmitCommand processes a command submitted via external channel.
// Uses CommandStore interface for distributed command delivery.
func (h *WebhookInterruptHandler) SubmitCommand(ctx context.Context, command *Command) error {
	// Set timestamp if not set
	if command.Timestamp.IsZero() {
		command.Timestamp = time.Now()
	}

	// Publish command via CommandStore (works across instances with Redis)
	if err := h.commandStore.PublishCommand(ctx, command); err != nil {
		if h.logger != nil {
			h.logger.WarnWithContext(ctx, "Failed to publish command", map[string]interface{}{
				"operation":     "hitl_command_submit",
				"checkpoint_id": command.CheckpointID,
				"command_type":  command.Type,
				"error":         err.Error(),
			})
		}
		return fmt.Errorf("failed to publish command: %w", err)
	}

	// Add span event
	telemetry.AddSpanEvent(ctx, "hitl.command.submitted",
		attribute.String("checkpoint_id", command.CheckpointID),
		attribute.String("command_type", string(command.Type)),
	)

	if h.logger != nil {
		h.logger.InfoWithContext(ctx, "Command submitted", map[string]interface{}{
			"operation":     "hitl_command_submit",
			"checkpoint_id": command.CheckpointID,
			"command_type":  command.Type,
			"user_id":       command.UserID,
		})
	}

	return nil
}

// -----------------------------------------------------------------------------
// Webhook Payload Types
// -----------------------------------------------------------------------------

// WebhookPayload is the payload sent to webhook endpoints.
type WebhookPayload struct {
	Type           string             `json:"type"`
	CheckpointID   string             `json:"checkpoint_id"`
	RequestID      string             `json:"request_id,omitempty"`
	InterruptPoint string             `json:"interrupt_point"`
	Decision       *InterruptDecision `json:"decision"`
	Plan           *RoutingPlan       `json:"plan,omitempty"`
	CurrentStep    *RoutingStep       `json:"current_step,omitempty"`
	CreatedAt      time.Time          `json:"created_at"`
	ExpiresAt      time.Time          `json:"expires_at"`
}

// =============================================================================
// NoOpInterruptHandler - For Testing
// =============================================================================

// NoOpInterruptHandler is a handler that does nothing.
// Useful for testing or when HITL notifications are disabled.
type NoOpInterruptHandler struct{}

// NewNoOpInterruptHandler creates a no-op handler.
func NewNoOpInterruptHandler() *NoOpInterruptHandler {
	return &NoOpInterruptHandler{}
}

// NotifyInterrupt does nothing
func (h *NoOpInterruptHandler) NotifyInterrupt(ctx context.Context, checkpoint *ExecutionCheckpoint) error {
	return nil
}

// WaitForCommand immediately returns timeout
func (h *NoOpInterruptHandler) WaitForCommand(ctx context.Context, checkpointID string, timeout time.Duration) (*Command, error) {
	return nil, &ErrCheckpointExpired{CheckpointID: checkpointID}
}

// SubmitCommand does nothing
func (h *NoOpInterruptHandler) SubmitCommand(ctx context.Context, command *Command) error {
	return nil
}

// Compile-time interface compliance checks
var (
	_ InterruptHandler = (*WebhookInterruptHandler)(nil)
	_ InterruptHandler = (*NoOpInterruptHandler)(nil)
)
