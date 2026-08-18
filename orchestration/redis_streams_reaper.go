// Package orchestration — RedisStreamsReaper reclaims stuck pending entries.
//
// This Runnable companion to RedisStreamsTaskConsumer runs XAUTOCLAIM on a
// ticker to reclaim messages whose consumer has been unresponsive for longer
// than the visibility timeout. Without this Runnable, a crashed executor
// replica holds its claimed tasks forever.
//
// Register this alongside the worker in the executor's main.go:
//
//	framework.RegisterRunnable(reaper)

package orchestration

import (
	"context"
	"time"

	"github.com/redis/go-redis/v9"
	"github.com/truvaagents/truva-g3/core"
)

const (
	defaultReapInterval      = 30 * time.Second
	defaultVisibilityTimeout = 5 * time.Minute
)

var _ core.Runnable = (*RedisStreamsReaper)(nil)

// RedisStreamsReaper is a core.Runnable that periodically reclaims stuck
// pending entries from the Redis Stream consumer group.
type RedisStreamsReaper struct {
	client            redis.Cmdable
	streamKey         string
	groupName         string
	consumerName      string
	reapInterval      time.Duration
	visibilityTimeout time.Duration
	logger            core.Logger
}

// NewRedisStreamsReaper creates a reaper for the given stream and group.
func NewRedisStreamsReaper(client redis.Cmdable, queueName, groupName string) *RedisStreamsReaper {
	return &RedisStreamsReaper{
		client:            client,
		streamKey:         taskStreamKeyPrefix + queueName,
		groupName:         groupName,
		consumerName:      consumerName(),
		reapInterval:      defaultReapInterval,
		visibilityTimeout: defaultVisibilityTimeout,
	}
}

// Start implements core.Runnable. Runs until ctx is cancelled.
func (r *RedisStreamsReaper) Start(ctx context.Context) error {
	ticker := time.NewTicker(r.reapInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			r.reap(ctx)
		}
	}
}

func (r *RedisStreamsReaper) reap(ctx context.Context) {
	minIdleTime := r.visibilityTimeout
	// XAUTOCLAIM transfers ownership of idle pending entries to this consumer.
	_, _, err := r.client.XAutoClaim(ctx, &redis.XAutoClaimArgs{
		Stream:   r.streamKey,
		Group:    r.groupName,
		Consumer: r.consumerName,
		MinIdle:  minIdleTime,
		Start:    "0-0",
		Count:    100,
	}).Result()
	if err != nil && r.logger != nil {
		r.logger.Warn("Reaper: XAUTOCLAIM failed, will retry next tick", map[string]interface{}{
			"operation":  "reaper_xautoclaim",
			"stream_key": r.streamKey,
			"group":      r.groupName,
			"error":      err.Error(),
		})
	}
}
