// Package orchestration — scheduler backend bundle factories.
//
// SchedulerBackends bundles the primitives needed by both the producer side
// (scheduler-tool) and the consumer side (scheduled-executor). It is returned
// by Layer 1 convenience factories (NewRedisSchedulerBackends,
// NewInMemorySchedulerBackends) for the common case where an application
// wants all backends from the same source.
//
// The lock is intentionally NOT part of this bundle. Applications wire the
// lock separately from the memory peer module or from core.
//
// No separate DeadLetterSink field: dead-letter persistence is carried by
// the consumer's TaskHandle.Nack method. The consumer owns both the
// "remove from queue" and "persist to DLQ" operations.

package orchestration

import (
	"github.com/redis/go-redis/v9"
	"github.com/truvaagents/truva-g3/core"
)

// SchedulerBackends bundles vendor-specific reference implementations of
// the core interfaces needed by the producer side (Scheduler + capability
// handlers) AND the consumer side (scheduled-executor). Three fields, all
// interface-typed so the struct is vendor-neutral at the type level even
// though the constructors wire Redis by default.
type SchedulerBackends struct {
	// Producer-side (used by scheduler-tool)
	ScheduleStore  core.ScheduleStore
	TaskDispatcher core.TaskDispatcher

	// Consumer-side (used by scheduled-executor)
	TaskConsumer core.TaskConsumer
}

const (
	// ScheduledExecutorQueue is the canonical logical queue name shared by
	// both sides. The producer (Scheduler) dispatches to it; the consumer
	// (scheduled-executor) reads from it. The transport implementation
	// translates this into a transport-specific identifier (Redis list key,
	// Redis stream key, Postgres queue_name column, etc.).
	ScheduledExecutorQueue = "scheduled-executor"
)

// NewRedisSchedulerBackends wires the default BRPOP-based consumer
// (at-most-once). This is the framework's default. For at-least-once
// semantics, use NewRedisStreamsSchedulerBackends instead and register
// the returned reaper Runnable alongside the worker.
//
// Accepts redis.Cmdable so both *redis.Client and *redis.ClusterClient work.
func NewRedisSchedulerBackends(client redis.Cmdable) (*SchedulerBackends, error) {
	store, err := NewRedisScheduleStore(client, nil)
	if err != nil {
		return nil, err
	}
	dispatcher, err := NewRedisTaskDispatcher(client)
	if err != nil {
		return nil, err
	}
	consumer, err := NewRedisTaskConsumer(client, ScheduledExecutorQueue)
	if err != nil {
		return nil, err
	}
	return &SchedulerBackends{
		ScheduleStore:  store,
		TaskDispatcher: dispatcher,
		TaskConsumer:   consumer,
	}, nil
}

// NewRedisStreamsSchedulerBackends wires the Redis Streams-based consumer
// (at-least-once) plus its companion reaper. The reaper MUST be registered
// alongside the worker as a Runnable — without it, crashed executor
// replicas leak claimed tasks indefinitely.
//
// Usage in main.go:
//
//	backends, reaper, err := orchestration.NewRedisStreamsSchedulerBackends(client)
//	framework.RegisterRunnable(worker)
//	framework.RegisterRunnable(reaper)
func NewRedisStreamsSchedulerBackends(client redis.Cmdable) (*SchedulerBackends, core.Runnable, error) {
	store, err := NewRedisScheduleStore(client, nil)
	if err != nil {
		return nil, nil, err
	}
	dispatcher, err := NewRedisStreamsTaskDispatcher(client, ScheduledExecutorQueue)
	if err != nil {
		return nil, nil, err
	}
	groupName := "scheduled-executor-group"
	consumer, err := NewRedisStreamsTaskConsumer(client, ScheduledExecutorQueue, groupName)
	if err != nil {
		return nil, nil, err
	}
	reaper := NewRedisStreamsReaper(client, ScheduledExecutorQueue, groupName)
	return &SchedulerBackends{
		ScheduleStore:  store,
		TaskDispatcher: dispatcher,
		TaskConsumer:   consumer,
	}, reaper, nil
}

// NewInMemorySchedulerBackends returns a SchedulerBackends populated with
// in-memory implementations suitable for dev, unit tests, and
// single-instance deployments.
func NewInMemorySchedulerBackends() *SchedulerBackends {
	dispatcher := NewInMemoryTaskDispatcher()
	consumer := NewInMemoryTaskConsumerFromDispatcher(dispatcher)
	return &SchedulerBackends{
		ScheduleStore:  NewInMemoryScheduleStore(),
		TaskDispatcher: dispatcher,
		TaskConsumer:   consumer,
	}
}
