package main

import (
	"testing"

	"github.com/truvaagents/truva-g3/core"
)

func TestAsyncTravelTaskQueueConfigIsSharedAcrossComponents(t *testing.T) {
	t.Setenv(core.EnvServiceName, "async-travel-agent-api-service")
	apiConfig := asyncTravelTaskQueueConfig()

	t.Setenv(core.EnvServiceName, "async-travel-agent-worker-service")
	workerConfig := asyncTravelTaskQueueConfig()

	if apiConfig.QueueKey != workerConfig.QueueKey {
		t.Fatalf("API queue %q differs from worker queue %q", apiConfig.QueueKey, workerConfig.QueueKey)
	}
	if apiConfig.QueueKey != asyncTravelTaskQueueKey {
		t.Fatalf("queue key = %q, want %q", apiConfig.QueueKey, asyncTravelTaskQueueKey)
	}
	if apiConfig.ProcessingKey != asyncTravelTaskQueueKey+":processing" {
		t.Fatalf("processing key = %q, want %q", apiConfig.ProcessingKey, asyncTravelTaskQueueKey+":processing")
	}
}
