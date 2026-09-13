package orchestration

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/redis/go-redis/v9"
)

type resumeWatchFailureClient struct {
	redis.UniversalClient
	cause error
	calls int
}

func (c *resumeWatchFailureClient) Watch(context.Context, func(*redis.Tx) error, ...string) error {
	c.calls++
	return c.cause
}

func TestCheckpointTransactionRetriesOnlyOptimisticContention(t *testing.T) {
	for _, tt := range []struct {
		cause error
		calls int
	}{
		{redis.TxFailedErr, checkpointTransactionAttempts},
		{errors.New("backend unavailable"), 1},
		{context.Canceled, 1},
	} {
		client := &resumeWatchFailureClient{cause: tt.cause}
		store, err := NewRedisCheckpointStoreWithClient(client)
		if err != nil {
			t.Fatal(err)
		}
		err = store.checkpointTransaction(t.Context(), []string{"checkpoint"}, func(*redis.Tx) error {
			t.Fatal("failed watch invoked transaction body")
			return nil
		})
		if client.calls != tt.calls {
			t.Fatalf("calls=%d want=%d", client.calls, tt.calls)
		}
		var exhausted *ErrCheckpointCASExhausted
		if errors.Is(tt.cause, redis.TxFailedErr) {
			if !errors.As(err, &exhausted) {
				t.Fatalf("contention=%v", err)
			}
		} else if !errors.Is(err, tt.cause) {
			t.Fatalf("cause replaced: %v", err)
		}
	}
}

type resumeWebhookTransport func(*http.Request) (*http.Response, error)

func (f resumeWebhookTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}

func TestResumeOwnershipDoesNotExpandWebhookProtocol(t *testing.T) {
	checkpoint := &ExecutionCheckpoint{
		CheckpointID: "checkpoint", RequestID: "request", Status: CheckpointStatusPending,
		ParentCheckpointID: "parent", ParentResumeAttemptID: "internal-attempt",
		ResumeState: &CheckpointResumeState{Owner: "internal-owner"},
		Decision:    &InterruptDecision{Metadata: map[string]interface{}{"owner": "application-owner"}},
	}
	handler := NewWebhookInterruptHandler("http://callback.invalid/hitl", nil)
	calls := 0
	handler.httpClient = &http.Client{Transport: resumeWebhookTransport(func(request *http.Request) (*http.Response, error) {
		calls++
		body, err := io.ReadAll(request.Body)
		if err != nil {
			t.Fatal(err)
		}
		var payload map[string]interface{}
		if err := json.Unmarshal(body, &payload); err != nil {
			t.Fatal(err)
		}
		if payload["checkpoint_id"] != "checkpoint" || payload["request_id"] != "request" {
			t.Fatalf("payload=%s", body)
		}
		if strings.Contains(string(body), "internal-") || !strings.Contains(string(body), "application-owner") {
			t.Fatalf("payload mapping=%s", body)
		}
		for _, field := range []string{"resume_state", "parent_resume_attempt_id", "parent_checkpoint_id"} {
			if _, exists := payload[field]; exists {
				t.Fatalf("webhook gained %s", field)
			}
		}
		return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(""))}, nil
	})}
	if err := handler.doNotify(t.Context(), checkpoint); err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatalf("webhook calls=%d", calls)
	}
}
