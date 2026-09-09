// Package telemetry provides a write-only Redis-backed LLM call recorder for agents.
//
// This file implements telemetry.LLMCallRecorder by writing to the shared DB-0
// versioned Redis keyspace
// in the same format as orchestration.RedisLLMDebugStore.RecordInteraction. This
// allows agents to record LLM calls WITHOUT importing the orchestration module.
//
// Recorder contract:
// - Write-only: agents append interactions, registry-viewer reads them
// - Format-compatible: writes match orchestration.LLMInteraction JSON structure
// - Atomic: one Redis script appends data and preserves minimum retention
// - Resilient: Layer 1 retry with exponential backoff
package telemetry

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/redis/go-redis/v9"
	"github.com/truvaagents/truva-g3/core"
)

const (
	recorderConversationMetadataField = "meta:" + core.MetadataConversationID

	// Default TTLs — match orchestration defaults
	recorderDefaultTTL = 24 * time.Hour
	recorderErrorTTL   = 7 * 24 * time.Hour

	// Layer 1 resilience constants
	recorderMaxRetries     = 3
	recorderInitialBackoff = 100 * time.Millisecond
	recorderMaxBackoff     = 2 * time.Second
	recorderFailureWindow  = 30 * time.Second
	recorderMaxFailures    = 5
	recorderStartupTimeout = 5 * time.Second
)

func telemetryDefaultRedisKeyspace() core.RedisKeyspace {
	keyspace, err := core.NewRedisKeyspace("default")
	if err != nil {
		panic("telemetry: invalid built-in Redis keyspace: " + err.Error())
	}
	return keyspace
}

// recordLLMCallScript is the format-twin of orchestration's LLM interaction
// writer. Reading the prior TTL and applying the write in one script prevents
// an agent-side success record from shortening retention already promoted by
// the orchestrator. Missing keys receive the requested TTL; persistent and
// longer-lived keys retain their existing lifetime.
var recordLLMCallScript = redis.NewScript(`
local meta_ttl = redis.call("PTTL", KEYS[1])
local interaction_ttl = redis.call("PTTL", KEYS[2])
local floor_ttl = redis.call("PTTL", KEYS[3])
redis.call("RPUSH", KEYS[2], ARGV[1])
redis.call("HSETNX", KEYS[1], "created_at", ARGV[2])
redis.call("HSET", KEYS[1], "updated_at", ARGV[2])
redis.call("HSET", KEYS[1], "trace_id", ARGV[3])
redis.call("HSET", KEYS[1], "request_id", ARGV[4])
redis.call("HSET", KEYS[1], "original_request_id", ARGV[5])
if ARGV[6] ~= "" then
	redis.call("HSETNX", KEYS[1], ARGV[10], ARGV[6])
end
if ARGV[7] ~= "" then
	redis.call("HSETNX", KEYS[1], "source_component", ARGV[7])
end
if ARGV[8] ~= "" then
	redis.call("HSETNX", KEYS[1], "originating_agent", ARGV[8])
end
local requested = tonumber(ARGV[9])
if floor_ttl == -1 then
	requested = -1
elseif floor_ttl > requested then
	requested = floor_ttl
end
if requested == -1 then
	redis.call("PERSIST", KEYS[1])
	redis.call("PERSIST", KEYS[2])
else
	if meta_ttl == -2 or (meta_ttl >= 0 and meta_ttl < requested) then
		redis.call("PEXPIRE", KEYS[1], requested)
	end
	if interaction_ttl == -2 or (interaction_ttl >= 0 and interaction_ttl < requested) then
		redis.call("PEXPIRE", KEYS[2], requested)
	end
end
return 1
`)

// RedisLLMCallRecorder is a write-only Redis-backed implementation of LLMCallRecorder.
// It writes LLM call records to the shared versioned DB-0 keyspace in the same
// format as orchestration.RedisLLMDebugStore, enabling a common debug reader.
//
// Agents use this instead of importing the orchestration module directly.
type RedisLLMCallRecorder struct {
	client     redis.UniversalClient
	ownsClient bool
	logger     core.Logger
	keys       RedisLLMDebugKeys
	ttl        time.Duration
	errTTL     time.Duration

	// Layer 1 resilience state
	failureCount int
	failureMu    sync.Mutex
	lastFailure  time.Time
	closeOnce    sync.Once
	closeErr     error
}

// RecorderOption configures a RedisLLMCallRecorder.
type RecorderOption func(*recorderConfig)

type recorderConfig struct {
	redisURL         string
	logger           core.Logger
	keys             RedisLLMDebugKeys
	ttl              time.Duration
	errTTL           time.Duration
	keyspaceExplicit bool
}

type recorderStartupError struct {
	cause error
}

func (*recorderStartupError) Error() string {
	return "telemetry Redis startup check failed"
}

func (err *recorderStartupError) Unwrap() error { return err.cause }

// WithRecorderLogger sets the logger for recorder operations.
func WithRecorderLogger(logger core.Logger) RecorderOption {
	return func(c *recorderConfig) { c.logger = logger }
}

// WithRecorderRedisURL sets the Redis connection URL.
func WithRecorderRedisURL(url string) RecorderOption {
	return func(c *recorderConfig) { c.redisURL = url }
}

// WithRecorderTTL sets the TTL for successful debug records.
func WithRecorderTTL(ttl time.Duration) RecorderOption {
	return func(c *recorderConfig) { c.ttl = ttl }
}

// WithRecorderErrorTTL sets the TTL for error debug records.
func WithRecorderErrorTTL(ttl time.Duration) RecorderOption {
	return func(c *recorderConfig) { c.errTTL = ttl }
}

// WithRecorderKeyspace selects the canonical versioned DB-0 keyspace.
func WithRecorderKeyspace(keyspace core.RedisKeyspace) RecorderOption {
	return func(c *recorderConfig) {
		c.keys = NewRedisLLMDebugKeys(keyspace)
		c.keyspaceExplicit = true
	}
}

// NewRedisLLMCallRecorder creates an owning write-only Redis recorder for agent
// LLM calls. Connection resolution is shared with every other Redis adapter and
// therefore supports standalone, Sentinel, and cluster topology.
func NewRedisLLMCallRecorder(opts ...RecorderOption) (*RedisLLMCallRecorder, error) {
	for _, name := range []string{"TRUVAG3_LLM_DEBUG_REDIS_DB", "TRUVAG3_LLM_DEBUG_KEY_PREFIX"} {
		if value := os.Getenv(name); strings.TrimSpace(value) != "" {
			return nil, fmt.Errorf("%s is unsupported; use DB 0 with WithRecorderKeyspace: %w", name, core.ErrInvalidConfiguration)
		}
	}
	cfg := &recorderConfig{
		redisURL: "",
		logger:   &core.NoOpLogger{},
		keys:     NewRedisLLMDebugKeys(telemetryDefaultRedisKeyspace()),
		ttl:      recorderGetEnvDuration("TRUVAG3_LLM_DEBUG_TTL", recorderDefaultTTL),
		errTTL:   recorderGetEnvDuration("TRUVAG3_LLM_DEBUG_ERROR_TTL", recorderErrorTTL),
	}
	for _, opt := range opts {
		if opt != nil {
			opt(cfg)
		}
	}
	if !cfg.keyspaceExplicit {
		keyspace, err := core.NewRedisKeyspace(os.Getenv("TRUVAG3_REDIS_NAMESPACE"))
		if err != nil {
			return nil, fmt.Errorf("resolve Redis LLM recorder keyspace: %w", err)
		}
		cfg.keys = NewRedisLLMDebugKeys(keyspace)
	}
	normalizeRecorderConfig(cfg)

	connection, err := resolveRecorderRedisConnection(cfg.redisURL)
	if err != nil {
		return nil, fmt.Errorf("resolve Redis recorder connection: %w", err)
	}
	client, err := core.NewRedisUniversalClient(connection)
	if err != nil {
		return nil, fmt.Errorf("initialize Redis LLM recorder: %w", err)
	}

	if cfg.logger != nil {
		cfg.logger.Info("Redis LLM call recorder initialized", map[string]interface{}{
			"operation":  "llm_debug_recorder_initialize",
			"redis_mode": connection.Mode,
			"seed_count": len(connection.Addrs),
			"redis_db":   connection.DB,
			"ttl":        cfg.ttl.String(),
			"error_ttl":  cfg.errTTL.String(),
		})
	}

	return &RedisLLMCallRecorder{
		client:     client,
		ownsClient: true,
		logger:     cfg.logger,
		keys:       cfg.keys,
		ttl:        cfg.ttl,
		errTTL:     cfg.errTTL,
	}, nil
}

func resolveRecorderRedisConnection(explicitURL string) (core.RedisConnectionConfig, error) {
	if strings.TrimSpace(explicitURL) != "" {
		return core.ParseStandaloneRedisURL(explicitURL)
	}
	return core.ResolveRedisConnectionConfig(nil, os.LookupEnv)
}

// NewRedisLLMCallRecorderWithClient borrows client; the caller owns its lifetime.
func NewRedisLLMCallRecorderWithClient(
	client redis.UniversalClient,
	keyspace core.RedisKeyspace,
	opts ...RecorderOption,
) (*RedisLLMCallRecorder, error) {
	if nilRedisLLMRecorderClient(client) {
		return nil, fmt.Errorf("redis LLM call recorder client is required")
	}
	cfg := &recorderConfig{
		logger:           &core.NoOpLogger{},
		keys:             NewRedisLLMDebugKeys(keyspace),
		ttl:              recorderDefaultTTL,
		errTTL:           recorderErrorTTL,
		keyspaceExplicit: true,
	}
	for _, opt := range opts {
		if opt != nil {
			opt(cfg)
		}
	}
	normalizeRecorderConfig(cfg)
	if err := core.CheckRedisStartup(client, recorderStartupTimeout); err != nil {
		return nil, &recorderStartupError{cause: err}
	}
	return &RedisLLMCallRecorder{
		client: client,
		logger: cfg.logger,
		keys:   cfg.keys,
		ttl:    cfg.ttl,
		errTTL: cfg.errTTL,
	}, nil
}

func nilRedisLLMRecorderClient(client redis.UniversalClient) bool {
	if client == nil {
		return true
	}
	value := reflect.ValueOf(client)
	switch value.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return value.IsNil()
	default:
		return false
	}
}

func normalizeRecorderConfig(cfg *recorderConfig) {
	if cfg.logger == nil {
		cfg.logger = &core.NoOpLogger{}
	} else if componentAware, ok := cfg.logger.(core.ComponentAwareLogger); ok {
		cfg.logger = componentAware.WithComponent("framework/telemetry")
	}
	if cfg.ttl <= 0 {
		cfg.ttl = recorderDefaultTTL
	}
	if cfg.errTTL <= 0 {
		cfg.errTTL = recorderErrorTTL
	}
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

// RecordLLMCall appends an LLM call record to the versioned DB-0 keyspace.
// Uses the same atomic minimum-retention format as
// orchestration.RedisLLMDebugStore.RecordInteraction.
func (r *RedisLLMCallRecorder) RecordLLMCall(ctx context.Context, requestID string, record LLMCallRecord) error {
	if requestID == "" {
		return nil // Not called from orchestration — skip silently
	}
	if core.GetRequestID(ctx) == "" {
		ctx = core.WithRequestID(ctx, requestID)
	}

	var indexScore int64
	operation := func() error {
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
		indexScore = now.Unix()
		ttl := r.ttl
		if !record.Success {
			ttl = r.errTTL
		}
		ttlMilliseconds, err := recorderTTLMilliseconds(ttl)
		if err != nil {
			return err
		}

		if err := recordLLMCallScript.Run(
			ctx,
			r.client,
			[]string{
				r.keys.Meta(requestID),
				r.keys.Interactions(requestID),
				r.keys.RetentionFloor(requestID),
			},
			data,
			strconv.FormatInt(now.Unix(), 10),
			traceID,
			requestID,
			originalRequestID,
			conversationID,
			interaction.SourceComponent,
			originatingAgent,
			strconv.FormatInt(ttlMilliseconds, 10),
			recorderConversationMetadataField,
		).Err(); err != nil {
			return fmt.Errorf("write authoritative LLM debug record: %w", err)
		}
		return nil
	}

	if err := r.executeWithRetry(ctx, operation); err != nil {
		return err
	}
	indexStartedAt := time.Now()
	if err := retryRecorderIndexOnly(ctx, func() error {
		return r.client.ZAdd(ctx, r.keys.RecentIndex(), redis.Z{
			Score:  float64(indexScore),
			Member: requestID,
		}).Err()
	}); err != nil {
		if r.logger != nil {
			r.logger.WarnWithContext(ctx, "Failed to update LLM debug recent index", map[string]interface{}{
				"operation":     "llm_debug_recent_index",
				"request_id":    requestID,
				"error":         "redis LLM debug index update failed",
				"error_type":    "index_write",
				"failure_class": classifyRecorderRedisDiagnostic(err),
				"duration_ms":   time.Since(indexStartedAt).Milliseconds(),
			})
		}
	}
	return nil
}

func retryRecorderIndexOnly(ctx context.Context, operation func() error) error {
	var lastErr error
	backoff := recorderInitialBackoff
	for attempt := 1; attempt <= recorderMaxRetries; attempt++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := operation(); err == nil {
			return nil
		} else {
			lastErr = err
		}
		if attempt < recorderMaxRetries {
			timer := time.NewTimer(backoff)
			select {
			case <-ctx.Done():
				timer.Stop()
				return ctx.Err()
			case <-timer.C:
			}
			backoff *= 2
			if backoff > recorderMaxBackoff {
				backoff = recorderMaxBackoff
			}
		}
	}
	return lastErr
}

func classifyRecorderRedisDiagnostic(err error) string {
	switch {
	case err == nil:
		return "none"
	case errors.Is(err, context.Canceled):
		return "canceled"
	case errors.Is(err, context.DeadlineExceeded):
		return "timeout"
	default:
		var networkError net.Error
		if errors.As(err, &networkError) && networkError.Timeout() {
			return "timeout"
		}
		return "redis_backend_failure"
	}
}

func recorderTTLMilliseconds(ttl time.Duration) (int64, error) {
	if ttl <= 0 {
		return 0, fmt.Errorf("duration must be positive")
	}
	milliseconds := ttl.Milliseconds()
	if milliseconds <= 0 {
		milliseconds = 1
	}
	return milliseconds, nil
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

	conversationID := GetBaggage(ctx)[core.MetadataConversationID]
	if core.ValidateConversationID(conversationID) != core.ConversationIDValidationNone {
		return ""
	}
	return conversationID
}

// Close closes the Redis connection.
func (r *RedisLLMCallRecorder) Close() error {
	if !r.ownsClient {
		return nil
	}
	r.closeOnce.Do(func() { r.closeErr = r.client.Close() })
	return r.closeErr
}

// executeWithRetry implements Layer 1 built-in resilience.
func (r *RedisLLMCallRecorder) executeWithRetry(ctx context.Context, operation func() error) error {
	r.failureMu.Lock()
	if r.failureCount >= recorderMaxFailures && time.Since(r.lastFailure) < recorderFailureWindow {
		r.failureMu.Unlock()
		if r.logger != nil {
			r.logger.WarnWithContext(ctx, "LLM recorder: in cooldown period", map[string]interface{}{
				"operation":    "llm_debug_recorder_retry",
				"request_id":   core.GetRequestID(ctx),
				"status":       "cooldown",
				"failures":     r.failureCount,
				"cooldown_sec": recorderFailureWindow.Seconds(),
			})
		}
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
		if r.logger != nil {
			r.logger.WarnWithContext(ctx, "LLM recorder: operation failed, retrying", map[string]interface{}{
				"operation":     "llm_debug_recorder_retry",
				"request_id":    core.GetRequestID(ctx),
				"attempt":       attempt,
				"max":           recorderMaxRetries,
				"backoff":       backoff.String(),
				"error":         "redis LLM recorder operation failed",
				"error_type":    "backend",
				"failure_class": classifyRecorderRedisDiagnostic(err),
			})
		}

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
