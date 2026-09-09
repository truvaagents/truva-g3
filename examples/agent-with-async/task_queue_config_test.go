package main

import (
	"testing"

	"github.com/truvaagents/truva-g3/core"
)

func TestAsyncTravelTaskQueueConfigIsSharedAcrossComponents(t *testing.T) {
	keyspace, err := core.NewRedisKeyspace("example")
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv(core.EnvServiceName, "async-travel-agent-api-service")
	apiConfig := asyncTravelTaskQueueConfig(keyspace)

	t.Setenv(core.EnvServiceName, "async-travel-agent-worker-service")
	workerConfig := asyncTravelTaskQueueConfig(keyspace)

	if apiConfig.QueueKey != workerConfig.QueueKey {
		t.Fatalf("API queue %q differs from worker queue %q", apiConfig.QueueKey, workerConfig.QueueKey)
	}
	wantQueue := keyspace.Plain("tasks", "queue", "async-travel-agent")
	if apiConfig.QueueKey != wantQueue {
		t.Fatalf("queue key = %q, want %q", apiConfig.QueueKey, wantQueue)
	}
	wantProcessing := keyspace.Plain("tasks", "processing", "async-travel-agent")
	if apiConfig.ProcessingKey != wantProcessing {
		t.Fatalf("processing key = %q, want %q", apiConfig.ProcessingKey, wantProcessing)
	}
}
