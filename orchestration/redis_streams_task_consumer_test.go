package orchestration

import (
	"testing"

	"github.com/truvaagents/truva-g3/core"
	"github.com/truvaagents/truva-g3/core/conformance"
)

func TestRedisStreamsTaskConsumerConformance(t *testing.T) {
	conformance.RunTaskConsumerConformance(t, func(t *testing.T) (core.TaskConsumer, core.TaskDispatcher, func()) {
		mr, client := setupRedis(t)
		groupName := "conformance-group"
		consumer, err := NewRedisStreamsTaskConsumer(client, conformance.QueueName, groupName)
		if err != nil {
			t.Fatalf("NewRedisStreamsTaskConsumer: %v", err)
		}
		dispatcher, err := NewRedisStreamsTaskDispatcher(client, conformance.QueueName)
		if err != nil {
			t.Fatalf("NewRedisStreamsTaskDispatcher: %v", err)
		}
		return consumer, dispatcher, func() {
			client.Close()
			mr.Close()
		}
	})
}
