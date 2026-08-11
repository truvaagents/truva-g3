package orchestration

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/truvaagents/truva-g3/core/conformance"
)

func TestRedisTaskConsumerConformance(t *testing.T) {
	conformance.RunTaskDeliveryProfileConformance(t, conformance.TaskDeliveryAtMostOnce, func(t *testing.T) conformance.TaskDeliveryFixture {
		mr, client := setupRedis(t)
		consumer, err := NewRedisTaskConsumer(client, conformance.QueueName)
		if err != nil {
			t.Fatalf("NewRedisTaskConsumer: %v", err)
		}
		dispatcher, err := NewRedisTaskDispatcher(client)
		if err != nil {
			t.Fatalf("NewRedisTaskDispatcher: %v", err)
		}
		return conformance.TaskDeliveryFixture{
			Consumer: consumer, Dispatcher: dispatcher,
			Cleanup: func() {
				_ = client.Close()
				mr.Close()
			},
			DeadLetterContains: func(ctx context.Context, queueName, taskID, reason string) (bool, error) {
				entries, err := client.LRange(ctx, dlqKeyPrefix+queueName, 0, -1).Result()
				if err != nil {
					return false, err
				}
				for _, raw := range entries {
					var entry deadLetterEntry
					if err := json.Unmarshal([]byte(raw), &entry); err != nil {
						return false, err
					}
					if entry.Task != nil && entry.Task.ID == taskID && entry.Reason == reason {
						return true, nil
					}
				}
				return false, nil
			},
		}
	})
}
