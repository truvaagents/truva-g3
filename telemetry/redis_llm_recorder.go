// Package telemetry provides a write-only Redis-backed LLM call recorder for agents.
//
// This file implements telemetry.LLMCallRecorder by writing directly to Redis DB 7
// in the same format as orchestration.RedisLLMDebugStore.RecordInteraction. This
// allows agents to record LLM calls WITHOUT importing the orchestration module.
//
// Design: Phase 8 of AGENT_LLM_DEBUG_CAPTURE_DESIGN.md
// - Write-only: agents append interactions, registry-viewer reads them
// - Format-compatible: writes match orchestration.LLMInteraction JSON structure
// - Atomic: uses RPUSH (no read-modify-write, safe for concurrent writes)
// - Resilient: Layer 1 retry with exponential backoff
package telemetry

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"sync"
	"time"

	"github.com/go-redis/redis/v8"
	"github.com/truvaagents/truva-g3/core"
)

const (
	// Redis key patterns — must match orchestration/redis_llm_debug_store.go
	recorderKeyPrefix   = "truvag3:llm:debug:"
	recorderIndexKey    = "truvag3:llm:debug:index"
	recorderMetaSuffix  = ":meta"
	recorderInterSuffix = ":interactions"

	// Default TTLs — match orchestration defaults
	recorderDefaultTTL = 24 * time.Hour
	recorderErrorTTL   = 7 * 24 * time.Hour

	// Layer 1 resilience constants
	recorderMaxRetries     = 3
	recorderInitialBackoff = 100 * time.Millisecond
	recorderMaxBackoff     = 2 * time.Second
	recorderFailureWindow  = 30 * time.Second
	recorderMaxFailures    = 5
)

// RedisLLMCallRecorder is a write-only Redis-backed implementation of LLMCallRecorder.
// It writes LLM call records to Redis DB 7 in the same format as the orchestration
// module's RedisLLMDebugStore, enabling the registry-viewer to display agent LLM calls
// alongside orchestrator LLM calls.
//
// Agents use this instead of importing the orchestration module directly.
type RedisLLMCallRecorder struct {
	client *redis.Client
	logger core.Logger
	ttl    time.Duration
	errTTL time.Duration

	// Layer 1 resilience state
	failureCount int
	failureMu    sync.Mutex
	lastFailure  time.Time
}

// RecorderOption configures a RedisLLMCallRecorder.
type RecorderOption func(*recorderConfig)

type recorderConfig struct {
	redisURL string
	redisDB  int
	logger   core.Logger
	ttl      time.Duration
	errTTL   time.Duration
}

// WithRecorderLogger sets the logger for recorder operations.
func WithRecorderLogger(logger core.Logger) RecorderOption {
	return func(c *recorderConfig) { c.logger = logger }
}

// WithRecorderRedisURL sets the Redis connection URL.
func WithRecorderRedisURL(url string) RecorderOption {
	return func(c *recorderConfig) { c.redisURL = url }
}

// WithRecorderRedisDB sets the Redis database number (default: 7).
func WithRecorderRedisDB(db int) RecorderOption {
	return func(c *recorderConfig) { c.redisDB = db }
}

// WithRecorderTTL sets the TTL for successful debug records.
func WithRecorderTTL(ttl time.Duration) RecorderOption {
	return func(c *recorderConfig) { c.ttl = ttl }
}

// WithRecorderErrorTTL sets the TTL for error debug records.
func WithRecorderErrorTTL(ttl time.Duration) RecorderOption {
	return func(c *recorderConfig) { c.errTTL = ttl }
}

// NewRedisLLMCallRecorder creates a write-only Redis recorder for agent LLM calls.
// Environment variable precedence: explicit options > REDIS_URL > TRUVAG3_REDIS_URL > localhost:6379
func NewRedisLLMCallRecorder(opts ...RecorderOption) (*RedisLLMCallRecorder, error) {
	cfg := &recorderConfig{
		redisURL: recorderGetRedisURL(),
		redisDB:  recorderGetEnvInt("TRUVAG3_LLM_DEBUG_REDIS_DB", core.RedisDBLLMDebug),
		logger:   &core.NoOpLogger{},
		ttl:      recorderGetEnvDuration("TRUVAG3_LLM_DEBUG_TTL", recorderDefaultTTL),
		errTTL:   recorderGetEnvDuration("TRUVAG3_LLM_DEBUG_ERROR_TTL", recorderErrorTTL),
	}
	for _, opt := range opts {
		opt(cfg)
	}

	redisOpt, err := redis.ParseURL(cfg.redisURL)
	if err != nil {
		redisOpt = &redis.Options{Addr: cfg.redisURL}
	}
	redisOpt.DB = cfg.redisDB
	redisOpt.PoolSize = 10    // Match core.RedisRegistry connection pool pattern
	redisOpt.MinIdleConns = 5 // Keep connections warm for async recording bursts

	client := redis.NewClient(redisOpt)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := client.Ping(ctx).Err(); err != nil {
		return nil, fmt.Errorf("redis connection failed at %s (DB %d): %w\n"+
			"Hint: Check REDIS_URL or TRUVAG3_REDIS_URL environment variables",
			cfg.redisURL, cfg.redisDB, err)
	}

	cfg.logger.Info("Redis LLM call recorder initialized", map[string]interface{}{
		"redis_db":  cfg.redisDB,
		"ttl":       cfg.ttl.String(),
		"error_ttl": cfg.errTTL.String(),
	})

	return &RedisLLMCallRecorder{
		client: client,
		logger: cfg.logger,
		ttl:    cfg.ttl,
		errTTL: cfg.errTTL,
	}, nil
}

// llmInteractionJSON matches the JSON structure of orchestration.LLMInteraction.
// This is an internal serialization type — agents write this format, the registry-viewer
// (which uses orchestration.RedisLLMDebugStore.GetRecord) reads it.
type llmInteractionJSON struct {
	Type             string    `json:"type"`
	SourceComponent  string    `json:"source_component,omitempty"`
	CallDescription  string    `json:"call_description,omitempty"`
	StepID           string    `json:"step_id,omitempty"`
	Timestamp        time.Time `json:"timestamp"`
	DurationMs       int64     `json:"duration_ms"`
	Prompt           string    `json:"prompt"`
	SystemPrompt     string    `json:"system_prompt,omitempty"`
	Temperature      float64   `json:"temperature"`
	MaxTokens        int       `json:"max_tokens"`
	Model            string    `json:"model,omitempty"`
	Provider         string    `json:"provider,omitempty"`
	Response         string    `json:"response"`
	PromptTokens     int       `json:"prompt_tokens"`
	CompletionTokens int       `json:"completion_tokens"`
	TotalTokens      int       `json:"total_tokens"`
	Success          bool      `json:"success"`
	Error            string    `json:"error,omitempty"`
	Attempt          int       `json:"attempt"`
	PhaseNumber      int       `json:"phase_number,omitempty"`
}

// RecordLLMCall appends an LLM call record to Redis DB 7.
// Uses the same atomic pipeline format as orchestration.RedisLLMDebugStore.RecordInteraction.
func (r *RedisLLMCallRecorder) RecordLLMCall(ctx context.Context, requestID string, record LLMCallRecord) error {
	if requestID == "" {
		return nil // Not called from orchestration — skip silently
	}

	operation := func() error {
		metaKey := recorderKeyPrefix + requestID + recorderMetaSuffix
		interKey := recorderKeyPrefix + requestID + recorderInterSuffix

		// Convert telemetry.LLMCallRecord → orchestration.LLMInteraction JSON format
		interaction := llmInteractionJSON{
			Type:             record.CallType,
			SourceComponent:  record.SourceComponent,
			CallDescription:  record.Description,
			StepID:           record.StepID,
			Timestamp:        record.Timestamp,
			DurationMs:       record.DurationMs,
			Prompt:           record.Prompt,
			SystemPrompt:     record.SystemPrompt,
			Temperature:      record.Temperature,
			MaxTokens:        record.MaxTokens,
			Model:            record.Model,
			Provider:         record.Provider,
			Response:         record.Response,
			PromptTokens:     record.PromptTokens,
			CompletionTokens: record.CompletionTokens,
			TotalTokens:      record.TotalTokens,
			Success:          record.Success,
			Error:            record.Error,
			Attempt:          1, // Agent-side calls don't have retry visibility
			PhaseNumber:      record.PhaseNumber,
		}

		data, err := json.Marshal(interaction)
		if err != nil {
			return fmt.Errorf("serialization failed: %w", err)
		}

		// Extract trace context from baggage (matches orchestration store pattern)
		traceID := GetTraceContext(ctx).TraceID
		originalRequestID := requestID
		conversationID := recorderConversationIDFromContext(ctx)
		// originatingAgent mirrors the orchestration store's field (see
		// orchestration/redis_llm_debug_store.go). Sourced from the same
		// "agent_name" baggage key the orchestrator stamps from o.config.Name.
		// HSetNX below ensures first-writer-wins so the format-twin invariant
		// holds even when both writers target the same record.
		// See orchestration/ARCHITECTURE.md "LLM Debug Payload Store" — Alternative Writer.
		originatingAgent := ""
		if bag := GetBaggage(ctx); bag != nil {
			if origID := bag["original_request_id"]; origID != "" {
				originalRequestID = origID
			}
			originatingAgent = bag["agent_name"]
		}

		now := time.Now()
		ttl := r.ttl
		if !record.Success {
			ttl = r.errTTL
		}

		// Prevent TTL downgrade
		if existingTTL, err := r.client.TTL(ctx, metaKey).Result(); err == nil && existingTTL > ttl {
			ttl = existingTTL
		}

		// Atomic pipeline — matches orchestration.RedisLLMDebugStore.RecordInteraction exactly
		pipe := r.client.Pipeline()
		pipe.RPush(ctx, interKey, data)
		pipe.HSetNX(ctx, metaKey, "created_at", strconv.FormatInt(now.Unix(), 10))
		pipe.HSet(ctx, metaKey, "updated_at", strconv.FormatInt(now.Unix(), 10))
		pipe.HSet(ctx, metaKey, "trace_id", traceID)
		pipe.HSet(ctx, metaKey, "request_id", requestID)
		pipe.HSet(ctx, metaKey, "original_request_id", originalRequestID)
		if conversationID != "" {
			pipe.HSetNX(ctx, metaKey, "meta:conversation_id", conversationID)
		}
		if interaction.SourceComponent != "" {
			pipe.HSetNX(ctx, metaKey, "source_component", interaction.SourceComponent)
		}
		if originatingAgent != "" {
			pipe.HSetNX(ctx, metaKey, "originating_agent", originatingAgent)
		}
		pipe.Expire(ctx, metaKey, ttl)
		pipe.Expire(ctx, interKey, ttl)
		pipe.ZAdd(ctx, recorderIndexKey, &redis.Z{
			Score:  float64(now.Unix()),
			Member: requestID,
		})
		_, err = pipe.Exec(ctx)
		if err != nil {
			return fmt.Errorf("redis pipeline failed: %w", err)
		}
		return nil
	}

	return r.executeWithRetry(ctx, operation)
}

func recorderConversationIDFromContext(ctx context.Context) string {
	coreCandidate := core.GetConversationIDCandidate(ctx)
	if coreCandidate.Present {
		if coreCandidate.RejectionReason != core.ConversationIDValidationNone ||
			core.ValidateConversationID(coreCandidate.Value) != core.ConversationIDValidationNone {
			return ""
		}
		return coreCandidate.Value
	}

	conversationID := GetBaggage(ctx)["conversation_id"]
	if core.ValidateConversationID(conversationID) != core.ConversationIDValidationNone {
		return ""
	}
	return conversationID
}

// Close closes the Redis connection.
func (r *RedisLLMCallRecorder) Close() error {
	return r.client.Close()
}

// executeWithRetry implements Layer 1 built-in resilience.
func (r *RedisLLMCallRecorder) executeWithRetry(ctx context.Context, operation func() error) error {
	r.failureMu.Lock()
	if r.failureCount >= recorderMaxFailures && time.Since(r.lastFailure) < recorderFailureWindow {
		r.failureMu.Unlock()
		r.logger.Warn("LLM recorder: in cooldown period", map[string]interface{}{
			"failures":     r.failureCount,
			"cooldown_sec": recorderFailureWindow.Seconds(),
		})
		return fmt.Errorf("recorder in cooldown after %d failures", r.failureCount)
	}
	r.failureMu.Unlock()

	var lastErr error
	backoff := recorderInitialBackoff

	for attempt := 1; attempt <= recorderMaxRetries; attempt++ {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		err := operation()
		if err == nil {
			r.failureMu.Lock()
			r.failureCount = 0
			r.failureMu.Unlock()
			return nil
		}

		lastErr = err
		r.logger.Warn("LLM recorder: operation failed, retrying", map[string]interface{}{
			"attempt": attempt,
			"max":     recorderMaxRetries,
			"backoff": backoff.String(),
			"error":   err.Error(),
		})

		if attempt < recorderMaxRetries {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(backoff):
			}
			backoff *= 2
			if backoff > recorderMaxBackoff {
				backoff = recorderMaxBackoff
			}
		}
	}

	r.failureMu.Lock()
	r.failureCount++
	r.lastFailure = time.Now()
	r.failureMu.Unlock()

	return fmt.Errorf("recorder failed after %d attempts: %w", recorderMaxRetries, lastErr)
}

// Verify interface compliance
var _ LLMCallRecorder = (*RedisLLMCallRecorder)(nil)

// Environment variable helpers (duplicated from orchestration to avoid import)

func recorderGetRedisURL() string {
	if url := os.Getenv("REDIS_URL"); url != "" {
		return url
	}
	if url := os.Getenv("TRUVAG3_REDIS_URL"); url != "" {
		return url
	}
	return "localhost:6379"
}

func recorderGetEnvInt(key string, defaultVal int) int {
	if val := os.Getenv(key); val != "" {
		if result, err := strconv.Atoi(val); err == nil {
			return result
		}
	}
	return defaultVal
}

func recorderGetEnvDuration(key string, defaultVal time.Duration) time.Duration {
	if val := os.Getenv(key); val != "" {
		if result, err := time.ParseDuration(val); err == nil {
			return result
		}
	}
	return defaultVal
}
