package orchestration

import (
	"testing"

	"github.com/truvaagents/truva-g3/core"
	"github.com/truvaagents/truva-g3/core/conformance"
)

func TestRedisTaskConsumerConformance(t *testing.T) {
	conformance.RunTaskConsumerConformance(t, func(t *testing.T) (core.TaskConsumer, core.TaskDispatcher, func()) {
		mr, client := setupRedis(t)
		consumer, err := NewRedisTaskConsumer(client, conformance.QueueName)
		if err != nil {
			t.Fatalf("NewRedisTaskConsumer: %v", err)
		}
		dispatcher, err := NewRedisTaskDispatcher(client)
		if err != nil {
			t.Fatalf("NewRedisTaskDispatcher: %v", err)
		}
		return consumer, dispatcher, func() {
			client.Close()
			mr.Close()
		}
	})
}
