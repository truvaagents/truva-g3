package orchestration

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/redis/go-redis/v9"
	"github.com/truvaagents/truva-g3/core"
	"github.com/truvaagents/truva-g3/telemetry"
	"go.opentelemetry.io/otel/attribute"
)

// =============================================================================
// RedisCommandStore - Production Implementation
// =============================================================================
//
// RedisCommandStore implements CommandStore using Redis Pub/Sub.
// This is the framework's default implementation for production use.
// It enables distributed command delivery across multiple instances.
//
// Per FRAMEWORK_DESIGN_PRINCIPLES.md:
// - Interface-First Design: Implements CommandStore interface
// - Production-First Architecture: Redis-backed for distributed systems
// - Intelligent Configuration: Environment-aware (REDIS_URL)
//
// Key format:
//   - Command channel: {prefix}:command:{checkpoint_id}
//
// Usage:
//
//	store, err := NewRedisCommandStore(
//	    WithCommandStoreRedisURL("redis://localhost:6379"),
//	    WithCommandStoreLogger(logger),
//	)
//
// =============================================================================

// RedisCommandStore implements CommandStore using Redis Pub/Sub.
type RedisCommandStore struct {
	client     redis.UniversalClient
	ownsClient bool
	keyPrefix  string
	keys       hitlKeys

	// Optional dependencies (injected per framework patterns)
	logger    core.Logger    // Defaults to NoOp
	telemetry core.Telemetry // Defaults to NoOp

	// Subscription management
	subscriptions map[string]context.CancelFunc
	subMu         sync.RWMutex
	closeOnce     sync.Once
	closeErr      error
}

// redisCommandStoreConfig holds configuration for the command store
type redisCommandStoreConfig struct {
	redisURL          string
	keyPrefix         string
	logger            core.Logger
	telemetry         core.Telemetry
	keyPrefixExplicit bool
}

// RedisCommandStoreOption configures the command store
type RedisCommandStoreOption func(*redisCommandStoreConfig)

// WithCommandStoreRedisURL sets the Redis connection URL
func WithCommandStoreRedisURL(url string) RedisCommandStoreOption {
	return func(c *redisCommandStoreConfig) {
		c.redisURL = url
	}
}

// WithCommandStoreKeyspace selects the canonical versioned DB-0 HITL keyspace.
func WithCommandStoreKeyspace(keyspace core.RedisKeyspace, agentScope string) RedisCommandStoreOption {
	return func(c *redisCommandStoreConfig) {
		c.keyPrefix = keyspace.Tagged("hitl", agentScope)
		c.keyPrefixExplicit = true
	}
}

// WithCommandStoreLogger sets the logger for the command store
func WithCommandStoreLogger(logger core.Logger) RedisCommandStoreOption {
	return func(c *redisCommandStoreConfig) {
		if logger == nil {
			return
		}
		// Use ComponentAwareLogger for component-based log segregation (per LOGGING_IMPLEMENTATION_GUIDE.md)
		if cal, ok := logger.(core.ComponentAwareLogger); ok {
			c.logger = cal.WithComponent("framework/orchestration")
		} else {
			c.logger = logger
		}
	}
}

// WithCommandStoreTelemetry sets the telemetry provider for the command store
func WithCommandStoreTelemetry(t core.Telemetry) RedisCommandStoreOption {
	return func(c *redisCommandStoreConfig) {
		c.telemetry = t
	}
}

// NewRedisCommandStore creates a new Redis-backed command store.
// Returns concrete type per Go idiom "return structs, accept interfaces".
//
// Configuration priority:
//  1. Explicit option (e.g., WithCommandStoreRedisURL)
//  2. Shared Redis connection environment (REDIS_URL or structured topology)
//  3. Default value
func NewRedisCommandStore(opts ...RedisCommandStoreOption) (*RedisCommandStore, error) {
	identity := resolveRedisCheckpointIdentity()
	// Initialize config with defaults
	config := &redisCommandStoreConfig{
		redisURL:  "",
		keyPrefix: identity.keyPrefix,
		logger:    &core.NoOpLogger{},
	}

	// Apply options
	for _, opt := range opts {
		if opt != nil {
			opt(config)
		}
	}
	if identity.validationErr != nil && !config.keyPrefixExplicit {
		return nil, identity.validationErr
	}

	client, _, err := newOwnedRedisUniversalClient(config.redisURL, "TRUVAG3_HITL_REDIS_DB", "TRUVAG3_HITL_KEY_PREFIX")
	if err != nil {
		return nil, fmt.Errorf("initialize Redis command store: %w", err)
	}

	return &RedisCommandStore{
		client:        client,
		ownsClient:    true,
		keyPrefix:     config.keyPrefix,
		keys:          newHITLKeys(config.keyPrefix),
		logger:        config.logger,
		telemetry:     config.telemetry,
		subscriptions: make(map[string]context.CancelFunc),
	}, nil
}

// NewRedisCommandStoreWithClient creates a command store using an
// application-owned client. Close cancels subscriptions but leaves the client
// open.
func NewRedisCommandStoreWithClient(client redis.UniversalClient, opts ...RedisCommandStoreOption) (*RedisCommandStore, error) {
	if client == nil {
		return nil, fmt.Errorf("redis command client is required")
	}
	identity := resolveRedisCheckpointIdentity()
	config := &redisCommandStoreConfig{
		keyPrefix: identity.keyPrefix,
		logger:    &core.NoOpLogger{},
	}
	for _, opt := range opts {
		if opt != nil {
			opt(config)
		}
	}
	if identity.validationErr != nil && !config.keyPrefixExplicit {
		return nil, identity.validationErr
	}
	return &RedisCommandStore{
		client: client, keyPrefix: config.keyPrefix, keys: newHITLKeys(config.keyPrefix), logger: config.logger,
		telemetry: config.telemetry, subscriptions: make(map[string]context.CancelFunc),
	}, nil
}

// -----------------------------------------------------------------------------
// CommandStore Implementation
// -----------------------------------------------------------------------------

// PublishCommand publishes a command for a waiting handler to receive.
// Uses Redis Pub/Sub for cross-instance delivery.
func (s *RedisCommandStore) PublishCommand(ctx context.Context, command *Command) error {
	channel := s.keys.command(command.CheckpointID)

	// Set timestamp if not set
	if command.Timestamp.IsZero() {
		command.Timestamp = time.Now()
	}

	data, err := json.Marshal(command)
	if err != nil {
		return fmt.Errorf("failed to marshal command: %w", err)
	}

	// Publish to Redis channel
	if err := s.client.Publish(ctx, channel, data).Err(); err != nil {
		if s.logger != nil {
			s.logger.ErrorWithContext(ctx, "Failed to publish command", map[string]interface{}{
				"request_id":    core.GetRequestID(ctx),
				"operation":     "hitl_command_publish",
				"checkpoint_id": command.CheckpointID,
				"command_type":  command.Type,
				"error":         "redis command publication failed",
				"error_type":    "backend_operation",
			})
		}
		return fmt.Errorf("failed to publish command to Redis: %w (check Redis connection configuration and connectivity)", err)
	}

	// Add span event for tracing visibility
	telemetry.AddSpanEvent(ctx, "hitl.command.published",
		attribute.String("request_id", core.GetRequestID(ctx)),
		attribute.String("checkpoint_id", command.CheckpointID),
		attribute.String("command_type", string(command.Type)),
	)

	// Record metric (Phase 4 - Metrics Integration)
	RecordCommandPublished(command.Type)

	if s.logger != nil {
		s.logger.DebugWithContext(ctx, "Command published", map[string]interface{}{
			"request_id":    core.GetRequestID(ctx),
			"operation":     "hitl_command_publish_complete",
			"checkpoint_id": command.CheckpointID,
			"command_type":  command.Type,
		})
	}

	return nil
}

// SubscribeCommand subscribes to commands for a specific checkpoint.
// Returns a channel that receives commands. Call the cancel func when done.
func (s *RedisCommandStore) SubscribeCommand(ctx context.Context, checkpointID string) (<-chan *Command, func(), error) {
	channel := s.keys.command(checkpointID)

	// Create subscription context
	subCtx, cancel := context.WithCancel(ctx)

	// Subscribe to Redis channel
	pubsub := s.client.Subscribe(subCtx, channel)

	// Wait for subscription confirmation
	_, err := pubsub.Receive(subCtx)
	if err != nil {
		cancel()
		return nil, nil, fmt.Errorf("failed to subscribe to command channel: %w (check Redis connection configuration and connectivity)", err)
	}

	// Create command channel
	cmdChan := make(chan *Command, 1)

	// Track subscription for cleanup
	s.subMu.Lock()
	s.subscriptions[checkpointID] = cancel
	s.subMu.Unlock()

	// Start goroutine to receive messages
	go func() {
		defer func() {
			_ = pubsub.Close() // Error intentionally ignored in cleanup
			close(cmdChan)

			s.subMu.Lock()
			delete(s.subscriptions, checkpointID)
			s.subMu.Unlock()
		}()

		ch := pubsub.Channel()
		for {
			select {
			case <-subCtx.Done():
				return
			case msg, ok := <-ch:
				if !ok {
					return
				}

				var cmd Command
				if err := json.Unmarshal([]byte(msg.Payload), &cmd); err != nil {
					if s.logger != nil {
						s.logger.WarnWithContext(ctx, "Failed to unmarshal command", map[string]interface{}{
							"request_id":    core.GetRequestID(ctx),
							"operation":     "hitl_command_receive",
							"checkpoint_id": checkpointID,
							"error":         "redis command decoding failed",
							"error_type":    "decode",
						})
					}
					continue
				}

				// Add span event
				telemetry.AddSpanEvent(ctx, "hitl.command.received",
					attribute.String("request_id", core.GetRequestID(ctx)),
					attribute.String("checkpoint_id", checkpointID),
					attribute.String("command_type", string(cmd.Type)),
				)

				select {
				case cmdChan <- &cmd:
					// Command delivered
				case <-subCtx.Done():
					return
				}
			}
		}
	}()

	if s.logger != nil {
		s.logger.DebugWithContext(ctx, "Subscribed to command channel", map[string]interface{}{
			"request_id":    core.GetRequestID(ctx),
			"operation":     "hitl_command_subscribe",
			"checkpoint_id": checkpointID,
		})
	}

	// Return cleanup function
	cleanup := func() {
		cancel()
	}

	return cmdChan, cleanup, nil
}

// Close closes the Redis connection and cancels all subscriptions.
func (s *RedisCommandStore) Close() error {
	// Cancel all subscriptions
	s.subMu.Lock()
	for _, cancel := range s.subscriptions {
		cancel()
	}
	s.subscriptions = make(map[string]context.CancelFunc)
	s.subMu.Unlock()

	if !s.ownsClient {
		return nil
	}
	s.closeOnce.Do(func() { s.closeErr = s.client.Close() })
	return s.closeErr
}

// Compile-time interface compliance check
var _ CommandStore = (*RedisCommandStore)(nil)
