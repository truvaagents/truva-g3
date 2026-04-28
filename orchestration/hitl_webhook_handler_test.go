package orchestration

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

// =============================================================================
// Mock CommandStore for Testing
// =============================================================================

// mockCommandStore implements CommandStore for unit tests.
// This avoids requiring Redis for unit testing while production code uses RedisCommandStore.
type mockCommandStore struct {
	channels map[string]chan *Command
	mu       sync.RWMutex
}

func newMockCommandStore() *mockCommandStore {
	return &mockCommandStore{
		channels: make(map[string]chan *Command),
	}
}

func (s *mockCommandStore) PublishCommand(ctx context.Context, command *Command) error {
	if command.Timestamp.IsZero() {
		command.Timestamp = time.Now()
	}

	s.mu.RLock()
	ch, exists := s.channels[command.CheckpointID]
	s.mu.RUnlock()

	if exists {
		select {
		case ch <- command:
		default:
		}
	}
	return nil
}

func (s *mockCommandStore) SubscribeCommand(ctx context.Context, checkpointID string) (<-chan *Command, func(), error) {
	ch := make(chan *Command, 1)

	s.mu.Lock()
	s.channels[checkpointID] = ch
	s.mu.Unlock()

	cleanup := func() {
		s.mu.Lock()
		delete(s.channels, checkpointID)
		s.mu.Unlock()
	}

	return ch, cleanup, nil
}

func (s *mockCommandStore) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for id := range s.channels {
		delete(s.channels, id)
	}
	return nil
}

// Compile-time check
var _ CommandStore = (*mockCommandStore)(nil)

// =============================================================================
// WebhookInterruptHandler Tests
// =============================================================================

func TestNewWebhookInterruptHandler(t *testing.T) {
	store := newMockCommandStore()
	handler := NewWebhookInterruptHandler("https://example.com/webhook", store)

	if handler == nil {
		t.Fatal("Expected handler to be created")
	}

	if handler.webhookURL != "https://example.com/webhook" {
		t.Errorf("Expected webhook URL to be set, got %s", handler.webhookURL)
	}

	if handler.httpClient == nil {
		t.Error("Expected HTTP client to be initialized")
	}

	if handler.commandStore == nil {
		t.Error("Expected command store to be initialized")
	}
}

func TestWebhookInterruptHandler_NotifyInterrupt(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping webhook interrupt notification test in short mode (uses real httptest server + HTTP round-trips)")
	}
	tests := []struct {
		name           string
		serverResponse int
		serverDelay    time.Duration
		expectError    bool
	}{
		{
			name:           "successful notification",
			serverResponse: http.StatusOK,
			serverDelay:    0,
			expectError:    false,
		},
		{
			name:           "server returns 201",
			serverResponse: http.StatusCreated,
			serverDelay:    0,
			expectError:    false,
		},
		{
			name:           "server returns 204",
			serverResponse: http.StatusNoContent,
			serverDelay:    0,
			expectError:    false,
		},
		{
			name:           "server returns 400",
			serverResponse: http.StatusBadRequest,
			serverDelay:    0,
			expectError:    true,
		},
		{
			name:           "server returns 500",
			serverResponse: http.StatusInternalServerError,
			serverDelay:    0,
			expectError:    true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Create test server
			var receivedPayload WebhookPayload
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				// Verify request headers
				if r.Header.Get("Content-Type") != "application/json" {
					t.Error("Expected Content-Type application/json")
				}
				if r.Header.Get("X-TruvaG3-Event") != "hitl.interrupt" {
					t.Error("Expected X-TruvaG3-Event header")
				}

				// Parse payload
				_ = json.NewDecoder(r.Body).Decode(&receivedPayload)

				// Delay if specified
				if tt.serverDelay > 0 {
					time.Sleep(tt.serverDelay)
				}

				w.WriteHeader(tt.serverResponse)
			}))
			defer server.Close()

			// Create handler with mock command store
			store := newMockCommandStore()
			handler := NewWebhookInterruptHandler(server.URL, store)

			// Create checkpoint
			checkpoint := &ExecutionCheckpoint{
				CheckpointID:   "cp-test-123",
				RequestID:      "req-456",
				InterruptPoint: InterruptPointPlanGenerated,
				Decision: &InterruptDecision{
					ShouldInterrupt: true,
					Reason:          ReasonPlanApproval,
					Message:         "Test interrupt",
					Priority:        PriorityNormal,
				},
				CreatedAt: time.Now(),
				ExpiresAt: time.Now().Add(time.Hour),
			}

			// Send notification
			err := handler.NotifyInterrupt(context.Background(), checkpoint)

			// Check result
			if tt.expectError && err == nil {
				t.Error("Expected error but got none")
			}
			if !tt.expectError && err != nil {
				t.Errorf("Unexpected error: %v", err)
			}

			// Verify payload was sent correctly (for successful cases)
			if !tt.expectError {
				if receivedPayload.CheckpointID != "cp-test-123" {
					t.Errorf("Expected checkpoint_id cp-test-123, got %s", receivedPayload.CheckpointID)
				}
				if receivedPayload.RequestID != "req-456" {
					t.Errorf("Expected request_id req-456, got %s", receivedPayload.RequestID)
				}
				if receivedPayload.Type != "interrupt" {
					t.Errorf("Expected type interrupt, got %s", receivedPayload.Type)
				}
			}
		})
	}
}

func TestWebhookInterruptHandler_NotifyInterrupt_ServerUnreachable(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping webhook unreachable test in short mode (waits for HTTP timeout against refused connection)")
	}
	// Use an invalid URL that will fail
	store := newMockCommandStore()
	handler := NewWebhookInterruptHandler("http://localhost:99999", store)

	checkpoint := &ExecutionCheckpoint{
		CheckpointID:   "cp-test",
		InterruptPoint: InterruptPointPlanGenerated,
		Decision: &InterruptDecision{
			ShouldInterrupt: true,
			Reason:          ReasonPlanApproval,
		},
		CreatedAt: time.Now(),
		ExpiresAt: time.Now().Add(time.Hour),
	}

	err := handler.NotifyInterrupt(context.Background(), checkpoint)
	if err == nil {
		t.Error("Expected error for unreachable server")
	}
}

func TestWebhookInterruptHandler_NotifyInterrupt_ContextCancelled(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping webhook context-cancellation test in short mode (uses real httptest server with delayed response)")
	}
	// Create a server that delays response
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(5 * time.Second)
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	store := newMockCommandStore()
	handler := NewWebhookInterruptHandler(server.URL, store)

	checkpoint := &ExecutionCheckpoint{
		CheckpointID:   "cp-test",
		InterruptPoint: InterruptPointPlanGenerated,
		Decision: &InterruptDecision{
			ShouldInterrupt: true,
			Reason:          ReasonPlanApproval,
		},
		CreatedAt: time.Now(),
		ExpiresAt: time.Now().Add(time.Hour),
	}

	// Create cancelled context
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // Cancel immediately

	err := handler.NotifyInterrupt(ctx, checkpoint)
	if err == nil {
		t.Error("Expected error for cancelled context")
	}
}

func TestWebhookInterruptHandler_WaitForCommand(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping webhook wait-for-command test in short mode (uses real polling with timer-based timeout)")
	}
	store := newMockCommandStore()
	handler := NewWebhookInterruptHandler("https://example.com/webhook", store)

	// Test timeout scenario
	t.Run("timeout", func(t *testing.T) {
		ctx := context.Background()
		cmd, err := handler.WaitForCommand(ctx, "cp-123", 100*time.Millisecond)

		if cmd != nil {
			t.Error("Expected nil command on timeout")
		}
		if err == nil {
			t.Error("Expected error on timeout")
		}
		if !IsCheckpointExpired(err) {
			t.Errorf("Expected ErrCheckpointExpired, got %T", err)
		}
	})

	// Test context cancellation
	t.Run("context cancelled", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()

		cmd, err := handler.WaitForCommand(ctx, "cp-456", time.Minute)

		if cmd != nil {
			t.Error("Expected nil command on context cancel")
		}
		if err != context.Canceled {
			t.Errorf("Expected context.Canceled, got %v", err)
		}
	})

	// Test successful command delivery
	t.Run("command delivered", func(t *testing.T) {
		checkpointID := "cp-delivery-test"

		// Start waiting in goroutine
		var wg sync.WaitGroup
		var receivedCmd *Command
		var waitErr error

		wg.Add(1)
		go func() {
			defer wg.Done()
			receivedCmd, waitErr = handler.WaitForCommand(context.Background(), checkpointID, 5*time.Second)
		}()

		// Give time for the channel to be created
		time.Sleep(50 * time.Millisecond)

		// Submit command
		cmd := &Command{
			CheckpointID: checkpointID,
			Type:         CommandApprove,
			UserID:       "test-user",
		}
		err := handler.SubmitCommand(context.Background(), &Command{
			CheckpointID: checkpointID,
			Type:         CommandApprove,
			UserID:       "test-user",
		})
		if err != nil {
			t.Fatalf("Failed to submit command: %v", err)
		}

		// Wait for goroutine
		wg.Wait()

		if waitErr != nil {
			t.Errorf("Unexpected error: %v", waitErr)
		}
		if receivedCmd == nil {
			t.Fatal("Expected command to be received")
		}
		if receivedCmd.Type != cmd.Type {
			t.Errorf("Expected type %s, got %s", cmd.Type, receivedCmd.Type)
		}
	})
}

func TestWebhookInterruptHandler_SubmitCommand(t *testing.T) {
	store := newMockCommandStore()
	handler := NewWebhookInterruptHandler("https://example.com/webhook", store)

	t.Run("submit to non-waiting checkpoint", func(t *testing.T) {
		// Should not error even if no one is waiting
		cmd := &Command{
			CheckpointID: "cp-no-waiter",
			Type:         CommandApprove,
		}
		err := handler.SubmitCommand(context.Background(), cmd)
		if err != nil {
			t.Errorf("Unexpected error: %v", err)
		}
	})

	t.Run("timestamp is set", func(t *testing.T) {
		cmd := &Command{
			CheckpointID: "cp-timestamp-test",
			Type:         CommandApprove,
		}

		if !cmd.Timestamp.IsZero() {
			t.Error("Expected timestamp to be zero initially")
		}

		err := handler.SubmitCommand(context.Background(), cmd)
		if err != nil {
			t.Errorf("Unexpected error: %v", err)
		}

		if cmd.Timestamp.IsZero() {
			t.Error("Expected timestamp to be set")
		}
	})
}

func TestWebhookInterruptHandler_WebhookHeaders(t *testing.T) {
	var receivedHeaders http.Header
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedHeaders = r.Header
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	store := newMockCommandStore()
	handler := NewWebhookInterruptHandler(server.URL, store)

	checkpoint := &ExecutionCheckpoint{
		CheckpointID:   "cp-header-test",
		RequestID:      "req-header-test",
		InterruptPoint: InterruptPointBeforeStep,
		Decision: &InterruptDecision{
			ShouldInterrupt: true,
			Reason:          ReasonSensitiveOperation,
		},
		CreatedAt: time.Now(),
		ExpiresAt: time.Now().Add(time.Hour),
	}

	err := handler.NotifyInterrupt(context.Background(), checkpoint)
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}

	// Verify headers
	if receivedHeaders.Get("Content-Type") != "application/json" {
		t.Error("Expected Content-Type header")
	}
	if receivedHeaders.Get("X-TruvaG3-Event") != "hitl.interrupt" {
		t.Error("Expected X-TruvaG3-Event header")
	}
	if receivedHeaders.Get("X-TruvaG3-Checkpoint-ID") != "cp-header-test" {
		t.Errorf("Expected X-TruvaG3-Checkpoint-ID header, got %s", receivedHeaders.Get("X-TruvaG3-Checkpoint-ID"))
	}
	if receivedHeaders.Get("X-TruvaG3-Request-ID") != "req-header-test" {
		t.Errorf("Expected X-TruvaG3-Request-ID header, got %s", receivedHeaders.Get("X-TruvaG3-Request-ID"))
	}
}

func TestWebhookInterruptHandler_WithOptions(t *testing.T) {
	// Test with nil logger (should not panic)
	store := newMockCommandStore()
	handler := NewWebhookInterruptHandler(
		"https://example.com/webhook",
		store,
		WithHandlerLogger(nil),
	)
	if handler == nil {
		t.Fatal("Expected handler to be created")
	}
}

// =============================================================================
// NoOpInterruptHandler Tests
// =============================================================================

func TestNoOpInterruptHandler(t *testing.T) {
	handler := NewNoOpInterruptHandler()

	t.Run("NotifyInterrupt returns nil", func(t *testing.T) {
		err := handler.NotifyInterrupt(context.Background(), &ExecutionCheckpoint{})
		if err != nil {
			t.Errorf("Expected nil error, got %v", err)
		}
	})

	t.Run("WaitForCommand returns expired error", func(t *testing.T) {
		cmd, err := handler.WaitForCommand(context.Background(), "cp-123", time.Second)
		if cmd != nil {
			t.Error("Expected nil command")
		}
		if !IsCheckpointExpired(err) {
			t.Errorf("Expected ErrCheckpointExpired, got %T", err)
		}
	})

	t.Run("SubmitCommand returns nil", func(t *testing.T) {
		err := handler.SubmitCommand(context.Background(), &Command{})
		if err != nil {
			t.Errorf("Expected nil error, got %v", err)
		}
	})
}

// =============================================================================
// WebhookPayload Tests
// =============================================================================

func TestWebhookPayload_JSON(t *testing.T) {
	now := time.Now()
	payload := WebhookPayload{
		Type:           "interrupt",
		CheckpointID:   "cp-json-test",
		RequestID:      "req-json-test",
		InterruptPoint: string(InterruptPointPlanGenerated),
		Decision: &InterruptDecision{
			ShouldInterrupt: true,
			Reason:          ReasonPlanApproval,
			Message:         "Test message",
			Priority:        PriorityHigh,
		},
		CreatedAt: now,
		ExpiresAt: now.Add(time.Hour),
	}

	// Marshal
	data, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("Failed to marshal payload: %v", err)
	}

	// Unmarshal
	var decoded WebhookPayload
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("Failed to unmarshal payload: %v", err)
	}

	// Verify
	if decoded.CheckpointID != payload.CheckpointID {
		t.Errorf("Expected checkpoint_id %s, got %s", payload.CheckpointID, decoded.CheckpointID)
	}
	if decoded.RequestID != payload.RequestID {
		t.Errorf("Expected request_id %s, got %s", payload.RequestID, decoded.RequestID)
	}
	if decoded.Decision.Reason != payload.Decision.Reason {
		t.Errorf("Expected reason %s, got %s", payload.Decision.Reason, decoded.Decision.Reason)
	}
}
