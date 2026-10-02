package memory

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
	"github.com/truvaagents/truva-g3/core"
)

// Compile-time interface compliance checks.
var (
	_ core.EpisodicMemory           = (*StreamEpisodicMemory)(nil)
	_ core.InvestigationCoordinator = (*AtomicLockCoordinator)(nil)
)

// --- Stream-Based Episodic Memory (Redis/Valkey Streams) ---

// EpisodicMemoryConfig configures StreamEpisodicMemory.
type EpisodicMemoryConfig struct {
	StreamMaxLen int64  // Approximate max length per domain stream (default: 100000)
	Domain       string // Agent domain for key prefixing (e.g., "infrastructure")
}

// EpisodicMemoryOption configures StreamEpisodicMemory using the WithXXX pattern.
// Returns error if the option value is invalid (fail-fast per core/ARCHITECTURE.md).
type EpisodicMemoryOption func(*StreamEpisodicMemory) error

// WithEpisodicRedisClient sets the Redis client for episodic memory.
func WithEpisodicRedisClient(client redis.UniversalClient) EpisodicMemoryOption {
	return func(v *StreamEpisodicMemory) error {
		if client == nil {
			return fmt.Errorf("redis client cannot be nil")
		}
		v.client = client
		return nil
	}
}

// WithEpisodicRedisUniversalClient is the topology-explicit spelling for
// injecting an application-owned standalone, Sentinel, or cluster client.
func WithEpisodicRedisUniversalClient(client redis.UniversalClient) EpisodicMemoryOption {
	return WithEpisodicRedisClient(client)
}

// WithEpisodicDomain sets the agent domain for key prefixing.
func WithEpisodicDomain(domain string) EpisodicMemoryOption {
	return func(v *StreamEpisodicMemory) error {
		if domain == "" {
			return fmt.Errorf("domain cannot be empty")
		}
		v.domain = domain
		return nil
	}
}

// WithEpisodicKeyspace sets the deployment-scoped Redis keyspace.
func WithEpisodicKeyspace(keyspace core.RedisKeyspace) EpisodicMemoryOption {
	return func(v *StreamEpisodicMemory) error {
		v.keyspace = keyspace
		return nil
	}
}

// WithEpisodicStreamMaxLen sets the approximate max stream length per domain.
func WithEpisodicStreamMaxLen(maxLen int64) EpisodicMemoryOption {
	return func(v *StreamEpisodicMemory) error {
		if maxLen <= 0 {
			return fmt.Errorf("stream max length must be positive, got %d", maxLen)
		}
		v.streamMaxLen = maxLen
		return nil
	}
}

// WithEpisodicEventTTL sets the TTL for individual event hash keys.
func WithEpisodicEventTTL(ttl time.Duration) EpisodicMemoryOption {
	return func(v *StreamEpisodicMemory) error {
		if ttl <= 0 {
			return fmt.Errorf("event TTL must be positive, got %v", ttl)
		}
		v.eventTTL = ttl
		return nil
	}
}

// WithEpisodicLogger sets the logger for episodic memory operations.
func WithEpisodicLogger(logger core.Logger) EpisodicMemoryOption {
	return func(v *StreamEpisodicMemory) error {
		if logger == nil {
			return fmt.Errorf("logger cannot be nil: use &core.NoOpLogger{} to disable logging")
		}
		if cal, ok := logger.(core.ComponentAwareLogger); ok {
			v.logger = cal.WithComponent("framework/memory")
		} else {
			v.logger = logger
		}
		return nil
	}
}

// StreamEpisodicMemory implements EpisodicMemory using Redis-compatible Streams
// and sorted set indexes. Works with Redis, Valkey, or any compatible server.
// Uses domain-prefixed keys for isolation (§0.6.5) and dual-writes ScopeGlobal
// events to both domain and global streams.
//
// Key schema:
//
//	truvag3:memory:{domain}:events:stream              — per-domain event stream
//	truvag3:memory:{domain}:entity:{type}:{id}         — sorted set (score=unix timestamp)
//	truvag3:memory:{domain}:agent:{name}               — sorted set
//	truvag3:memory:{domain}:event:{event_id}            — hash (event details)
//	truvag3:memory:global:events:stream                 — cross-domain global events
type StreamEpisodicMemory struct {
	client       redis.UniversalClient
	domain       string
	keyspace     core.RedisKeyspace
	streamMaxLen int64
	eventTTL     time.Duration // TTL for individual event hash keys
	logger       core.Logger
}

// NewStreamEpisodicMemory creates a new Stream-backed episodic memory.
// Uses WithXXX option functions per FRAMEWORK_DESIGN_PRINCIPLES §Configuration.
func NewStreamEpisodicMemory(opts ...EpisodicMemoryOption) (*StreamEpisodicMemory, error) {
	v := &StreamEpisodicMemory{
		domain:       "default",
		keyspace:     defaultRedisKeyspace(),
		streamMaxLen: 100000,
		eventTTL:     60 * 24 * time.Hour,
		logger:       &core.NoOpLogger{},
	}
	for _, opt := range opts {
		if err := opt(v); err != nil {
			return nil, fmt.Errorf("invalid episodic memory option: %w", err)
		}
	}
	if v.client == nil {
		return nil, fmt.Errorf("redis client is required: use WithEpisodicRedisClient()")
	}
	return v, nil
}

// RecordEvent appends a structured event to the domain event stream.
// Also writes to the global stream if Scope is ScopeGlobal.
func (v *StreamEpisodicMemory) RecordEvent(ctx context.Context, event core.AgentEvent) error {
	// Assign ID and timestamp if not set
	if event.EventID == "" {
		event.EventID = uuid.New().String()
	}
	if event.Timestamp.IsZero() {
		event.Timestamp = time.Now()
	}

	// Serialize event to JSON for the hash
	eventJSON, err := json.Marshal(event)
	if err != nil {
		if v.logger != nil {
			v.logger.WarnWithContext(ctx, "Failed to marshal event", map[string]interface{}{
				"request_id": core.GetRequestID(ctx),
				"operation":  "record_event",
				"event_id":   event.EventID,
				"error":      "memory event serialization failed",
				"error_type": "marshal",
			})
		}
		return nil // Fail-open: don't block the pipeline
	}

	score := float64(event.Timestamp.UnixMilli())
	pipe := v.client.Pipeline()

	// 1. Store event details as a hash
	eventKey := v.eventKey(event.EventID)
	pipe.Set(ctx, eventKey, string(eventJSON), v.eventTTL)

	// 2. Append to domain stream (approximate MAXLEN for efficiency)
	// Use primary entity for stream entry (backward compat)
	primaryEntityID := event.EntityID
	if len(event.Entities) > 0 {
		primaryEntityID = event.Entities[0].ID
	}
	streamKey := v.domainStreamKey()
	pipe.XAdd(ctx, &redis.XAddArgs{
		Stream: streamKey,
		MaxLen: v.streamMaxLen,
		Approx: true,
		Values: map[string]interface{}{"event_id": event.EventID, "entity_id": primaryEntityID},
	})

	// 3. Index by all entities (one event, multiple sorted set entries)
	if len(event.Entities) > 0 {
		for _, entity := range event.Entities {
			if entity.Type != "" && entity.ID != "" {
				entityKey := v.entityIndexKey(entity.Type, entity.ID)
				pipe.ZAdd(ctx, entityKey, redis.Z{Score: score, Member: event.EventID})
			}
		}
	} else if event.EntityType != "" && event.EntityID != "" {
		// Backward compat: singular fields
		entityKey := v.entityIndexKey(event.EntityType, event.EntityID)
		pipe.ZAdd(ctx, entityKey, redis.Z{Score: score, Member: event.EventID})
	}

	// 4. Index by agent
	agentKey := v.agentIndexKey(event.AgentName)
	pipe.ZAdd(ctx, agentKey, redis.Z{Score: score, Member: event.EventID})

	// 5. Dual-write: ScopeGlobal events also go to the global stream
	if event.Scope == core.ScopeGlobal {
		globalStreamKey := v.globalStreamKey()
		pipe.XAdd(ctx, &redis.XAddArgs{
			Stream: globalStreamKey,
			MaxLen: v.streamMaxLen,
			Approx: true,
			Values: map[string]interface{}{"event_id": event.EventID, "domain": event.AgentDomain},
		})
	}

	_, err = pipe.Exec(ctx)
	if err != nil {
		if v.logger != nil {
			v.logger.WarnWithContext(ctx, "Failed to record event", map[string]interface{}{
				"request_id": core.GetRequestID(ctx),
				"operation":  "record_event",
				"event_id":   event.EventID,
				"error":      "memory event write failed",
				"error_type": "stream_write",
			})
		}
		return nil // Fail-open
	}

	return nil
}

// QueryEvents retrieves events matching the filter criteria.
// Enforces scope-based visibility using callerDomain.
func (v *StreamEpisodicMemory) QueryEvents(ctx context.Context, callerDomain string, filter core.EventFilter) ([]core.AgentEvent, error) {
	results, err := v.queryEventsNewestFirst(ctx, callerDomain, filter)
	if err != nil {
		if v.logger != nil {
			v.logger.WarnWithContext(ctx, "Episodic query incomplete, omitting results", map[string]interface{}{
				"request_id":    core.GetRequestID(ctx),
				"operation":     "query_events",
				"caller_domain": callerDomain,
				"error_type":    "episodic_read",
			})
		}
		return nil, nil // Legacy fail-open contract; nil is not proof of read health.
	}
	return results, nil
}

// QueryEntityHistory returns all events for a specific entity, ordered chronologically.
func (v *StreamEpisodicMemory) QueryEntityHistory(ctx context.Context, callerDomain string, entityType, entityID string, since time.Time) ([]core.AgentEvent, error) {
	events, err := v.QueryEvents(ctx, callerDomain, core.EventFilter{
		EntityType: entityType,
		EntityID:   entityID,
		Since:      since,
		Limit:      50,
	})
	if err != nil || len(events) <= 1 {
		return events, err
	}
	// QueryEvents returns most-recent-first; reverse for chronological order
	for i, j := 0, len(events)-1; i < j; i, j = i+1, j-1 {
		events[i], events[j] = events[j], events[i]
	}
	return events, nil
}

// QueryRecentEvents returns the most recent events across all entities in a domain.
// Provides situational awareness without requiring entity extraction from the query.
// Results ordered by timestamp descending (most recent first).
func (v *StreamEpisodicMemory) QueryRecentEvents(ctx context.Context, domain string, since time.Time, limit int) ([]core.AgentEvent, error) {
	if limit <= 0 {
		limit = 10
	}
	return v.QueryEvents(ctx, domain, core.EventFilter{
		Since: since,
		Limit: limit,
	})
}

// DeleteEvents removes events by ID, including the event hash and all sorted set index references.
// Idempotent — returns nil if events don't exist.
func (v *StreamEpisodicMemory) DeleteEvents(ctx context.Context, eventIDs []string) error {
	if len(eventIDs) == 0 {
		return nil
	}

	pipe := v.client.Pipeline()

	for _, id := range eventIDs {
		// First fetch the event to know which indexes to clean up
		event, err := v.fetchEvent(ctx, id)
		if err != nil || event == nil {
			continue // Already gone or expired
		}

		// Delete event hash
		pipe.Del(ctx, v.eventKey(id))

		// Remove from all entity indexes
		if len(event.Entities) > 0 {
			for _, entity := range event.Entities {
				if entity.Type != "" && entity.ID != "" {
					pipe.ZRem(ctx, v.entityIndexKey(entity.Type, entity.ID), id)
				}
			}
		} else if event.EntityType != "" && event.EntityID != "" {
			// Backward compat: singular fields
			pipe.ZRem(ctx, v.entityIndexKey(event.EntityType, event.EntityID), id)
		}

		// Remove from agent index
		pipe.ZRem(ctx, v.agentIndexKey(event.AgentName), id)
	}

	_, err := pipe.Exec(ctx)
	if err != nil {
		if v.logger != nil {
			v.logger.WarnWithContext(ctx, "Failed to delete events", map[string]interface{}{
				"request_id":  core.GetRequestID(ctx),
				"operation":   "delete_events",
				"event_count": len(eventIDs),
				"error":       "memory event deletion failed",
				"error_type":  "stream_delete",
			})
		}
		return nil // Fail-open
	}

	return nil
}

// --- Key schema helpers ---

func (v *StreamEpisodicMemory) domainStreamKey() string {
	return v.keyspace.Plain("memory", v.domain, "events", "stream")
}

func (v *StreamEpisodicMemory) globalStreamKey() string {
	return v.keyspace.Plain("memory", "global", "events", "stream")
}

func (v *StreamEpisodicMemory) entityIndexKey(entityType, entityID string) string {
	return v.keyspace.Plain("memory", v.domain, "entity", entityType, entityID)
}

func (v *StreamEpisodicMemory) agentIndexKey(agentName string) string {
	return v.keyspace.Plain("memory", v.domain, "agent", agentName)
}

func (v *StreamEpisodicMemory) eventKey(eventID string) string {
	return v.keyspace.Plain("memory", v.domain, "event", eventID)
}

// --- Query helpers ---

func (v *StreamEpisodicMemory) fetchEvent(ctx context.Context, eventID string) (*core.AgentEvent, error) {
	// Handle cross-domain event references from the global stream
	key := v.eventKey(eventID)
	if strings.HasPrefix(eventID, "xdomain:") {
		parts := strings.SplitN(eventID[8:], ":", 2) // "xdomain:{domain}:{id}"
		if len(parts) == 2 {
			key = v.keyspace.Plain("memory", parts[0], "event", parts[1])
			_ = parts[1] // Normalize for dedup (eventID used via key lookup)
		}
	}

	data, err := v.client.Get(ctx, key).Result()
	if err == redis.Nil {
		return nil, nil // Expired or missing
	}
	if err != nil {
		return nil, err
	}

	var event core.AgentEvent
	if err := json.Unmarshal([]byte(data), &event); err != nil {
		return nil, err
	}
	return &event, nil
}

// isVisible checks if an event is visible to the caller based on scope rules.
//
// Note on ScopePrivate: the callerAgent parameter should ideally be the caller's own
// agent name (identity), not a query filter. Currently, hooks call QueryEvents without
// AgentName in the filter, so ScopePrivate events are invisible in standard hook queries
// (which is the correct behavior — private events are for the owning agent's own use).
// Direct API callers who set AgentName in the filter are declaring "show me this agent's events"
// which includes private events only if the name matches — acceptable for admin/debug queries.
func (v *StreamEpisodicMemory) isVisible(event *core.AgentEvent, callerDomain, callerAgent string) bool {
	switch event.Scope {
	case core.ScopeGlobal:
		return true
	case core.ScopeSharedDomain:
		return callerDomain == event.AgentDomain
	case core.ScopePrivate:
		return callerDomain == event.AgentDomain && callerAgent != "" && callerAgent == event.AgentName
	default:
		return true // Unknown scope — default visible (fail-open)
	}
}

func containsString(slice []string, s string) bool {
	for _, item := range slice {
		if item == s {
			return true
		}
	}
	return false
}

// --- Atomic Lock Investigation Coordinator (Redis-compatible SET NX) ---

// InvestigationCoordinatorOption configures AtomicLockCoordinator.
// Returns error if the option value is invalid (fail-fast per core/ARCHITECTURE.md).
type InvestigationCoordinatorOption func(*AtomicLockCoordinator) error

// WithCoordinatorRedisClient sets the Redis client for investigation coordination.
func WithCoordinatorRedisClient(client redis.UniversalClient) InvestigationCoordinatorOption {
	return func(v *AtomicLockCoordinator) error {
		if client == nil {
			return fmt.Errorf("redis client cannot be nil")
		}
		v.client = client
		return nil
	}
}

// WithCoordinatorRedisUniversalClient is the topology-explicit spelling for
// injecting an application-owned standalone, Sentinel, or cluster client.
func WithCoordinatorRedisUniversalClient(client redis.UniversalClient) InvestigationCoordinatorOption {
	return WithCoordinatorRedisClient(client)
}

// WithCoordinatorDomain sets the agent domain for key prefixing.
func WithCoordinatorDomain(domain string) InvestigationCoordinatorOption {
	return func(v *AtomicLockCoordinator) error {
		if domain == "" {
			return fmt.Errorf("domain cannot be empty")
		}
		v.domain = domain
		return nil
	}
}

// WithCoordinatorKeyspace sets the deployment-scoped Redis keyspace.
func WithCoordinatorKeyspace(keyspace core.RedisKeyspace) InvestigationCoordinatorOption {
	return func(v *AtomicLockCoordinator) error {
		v.keyspace = keyspace
		return nil
	}
}

// WithInvestigationTTL sets the default TTL for investigation claims.
func WithInvestigationTTL(ttl time.Duration) InvestigationCoordinatorOption {
	return func(v *AtomicLockCoordinator) error {
		if ttl <= 0 {
			return fmt.Errorf("investigation TTL must be positive, got %v", ttl)
		}
		v.defaultTTL = ttl
		return nil
	}
}

// WithCoordinatorLogger sets the logger for coordination operations.
func WithCoordinatorLogger(logger core.Logger) InvestigationCoordinatorOption {
	return func(v *AtomicLockCoordinator) error {
		if logger == nil {
			return fmt.Errorf("logger cannot be nil: use &core.NoOpLogger{} to disable logging")
		}
		if cal, ok := logger.(core.ComponentAwareLogger); ok {
			v.logger = cal.WithComponent("framework/memory")
		} else {
			v.logger = logger
		}
		return nil
	}
}

// AtomicLockCoordinator implements InvestigationCoordinator using
// Redis-compatible SET NX PX for atomic claim/release with TTL.
// Works with Redis, Valkey, or any compatible server.
//
// Key schema:
//
//	truvag3:memory:{domain}:investigating:{entity_id} — String (value=agentName, with TTL)
type AtomicLockCoordinator struct {
	client     redis.UniversalClient
	domain     string
	keyspace   core.RedisKeyspace
	defaultTTL time.Duration
	logger     core.Logger
}

var claimInvestigationScript = redis.NewScript(`
if redis.call("SET", KEYS[1], ARGV[1], "NX", "PX", ARGV[2]) then
	redis.call("SADD", KEYS[2], ARGV[3])
	return 1
end
return 0
`)

var releaseInvestigationScript = redis.NewScript(`
if redis.call("TYPE", KEYS[1]).ok == 'string' and redis.call("GET", KEYS[1]) == ARGV[1] then
	redis.call("DEL", KEYS[1])
	redis.call("SREM", KEYS[2], ARGV[2])
	return 1
end
return 0
`)

// NewAtomicLockCoordinator creates a new Stream-backed investigation coordinator.
func NewAtomicLockCoordinator(opts ...InvestigationCoordinatorOption) (*AtomicLockCoordinator, error) {
	v := &AtomicLockCoordinator{
		domain:     "default",
		keyspace:   defaultRedisKeyspace(),
		defaultTTL: 30 * time.Minute,
		logger:     &core.NoOpLogger{},
	}
	for _, opt := range opts {
		if err := opt(v); err != nil {
			return nil, fmt.Errorf("invalid coordinator option: %w", err)
		}
	}
	if v.client == nil {
		return nil, fmt.Errorf("redis client is required: use WithCoordinatorRedisClient()")
	}
	return v, nil
}

// ClaimInvestigation attempts to claim exclusive investigation of an entity.
// Uses SET NX PX for atomic claim with TTL.
func (v *AtomicLockCoordinator) ClaimInvestigation(ctx context.Context, agentName, entityID string, ttl time.Duration) (bool, string, error) {
	if ttl <= 0 {
		ttl = v.defaultTTL
	}
	key := v.investigationKey(entityID)
	milliseconds := ttl.Milliseconds()
	if milliseconds <= 0 {
		milliseconds = 1
	}
	ok, err := claimInvestigationScript.Run(
		ctx,
		v.client,
		[]string{key, v.investigationIndexKey()},
		agentName,
		strconv.FormatInt(milliseconds, 10),
		entityID,
	).Int64()
	if err != nil {
		v.observeRuntimeFailure(ctx, "claim_investigation", entityID, err)
		return false, "", nil // Fail-open: can't claim, but don't error
	}

	if ok == 1 {
		return true, "", nil // Successfully claimed
	}

	// Already claimed — find out by whom
	holder, err := v.client.Get(ctx, key).Result()
	if err != nil && strings.HasPrefix(err.Error(), "WRONGTYPE") {
		holder, err = investigationHolderScript.Run(ctx, v.client, []string{key}).Text()
	}
	if err != nil {
		v.observeRuntimeFailure(ctx, "claim_holder_read", entityID, err)
		return false, "", nil // Can't determine holder
	}
	return false, holder, nil
}

// ReleaseInvestigation releases a previously claimed investigation.
// Uses a Lua script to atomically check ownership before deleting.
func (v *AtomicLockCoordinator) ReleaseInvestigation(ctx context.Context, agentName, entityID string) error {
	key := v.investigationKey(entityID)

	_, err := releaseInvestigationScript.Run(
		ctx,
		v.client,
		[]string{key, v.investigationIndexKey()},
		agentName,
		entityID,
	).Result()
	if err != nil && err != redis.Nil {
		v.observeRuntimeFailure(ctx, "release_investigation", entityID, err)
	}
	return nil // Fail-open
}

// GetActiveInvestigations returns all currently claimed entities and their holders.
func (v *AtomicLockCoordinator) GetActiveInvestigations(ctx context.Context) (map[string]string, error) {
	ids, err := v.scanInvestigationIDs(ctx)
	if err != nil {
		v.observeRuntimeFailure(ctx, "active_investigation_index_scan", "", err)
		return nil, nil // Fail-open
	}
	pipe := v.client.Pipeline()
	commands := make([]*redis.StringCmd, len(ids))
	for i, id := range ids {
		commands[i] = pipe.Get(ctx, v.investigationKey(id))
	}
	_, execErr := pipe.Exec(ctx)
	if execErr != nil && execErr != redis.Nil && !strings.HasPrefix(execErr.Error(), "WRONGTYPE") {
		v.observeRuntimeFailure(ctx, "active_investigation_load", "", execErr)
		return nil, nil
	}
	result := make(map[string]string, len(ids))
	stale := make([]interface{}, 0)
	for i, command := range commands {
		holder, err := command.Result()
		if err != nil && strings.HasPrefix(err.Error(), "WRONGTYPE") {
			holder, err = investigationHolderScript.Run(ctx, v.client, []string{v.investigationKey(ids[i])}).Text()
		}
		if err == redis.Nil {
			stale = append(stale, ids[i])
			continue
		}
		if err != nil {
			v.observeRuntimeFailure(ctx, "active_investigation_load", ids[i], err)
			return nil, nil
		}
		result[ids[i]] = holder
	}
	if len(stale) > 0 {
		if err := v.client.SRem(ctx, v.investigationIndexKey(), stale...).Err(); err != nil {
			v.observeRuntimeFailure(ctx, "active_investigation_index_cleanup", "", err)
		}
	}
	return result, nil
}

func (v *AtomicLockCoordinator) observeRuntimeFailure(ctx context.Context, operation, entityID string, _ error) {
	if v.logger == nil {
		return
	}
	v.logger.WarnWithContext(ctx, "Redis investigation coordination failed open", map[string]interface{}{
		"request_id": core.GetRequestID(ctx),
		"operation":  operation,
		"entity_id":  entityID,
		"error":      "redis investigation coordination unavailable",
		"error_type": "backend",
	})
}

func (v *AtomicLockCoordinator) investigationKey(entityID string) string {
	return v.keyspace.Tagged("memory", v.domain, "investigation", entityID)
}

func (v *AtomicLockCoordinator) investigationIndexKey() string {
	return v.keyspace.Tagged("memory", v.domain, "investigations")
}

func (v *AtomicLockCoordinator) scanInvestigationIDs(ctx context.Context) ([]string, error) {
	seen := make(map[string]struct{})
	var cursor uint64
	for {
		ids, next, err := v.client.SScan(ctx, v.investigationIndexKey(), cursor, "", 256).Result()
		if err != nil {
			return nil, err
		}
		for _, id := range ids {
			seen[id] = struct{}{}
		}
		cursor = next
		if cursor == 0 {
			break
		}
	}
	ids := make([]string, 0, len(seen))
	for id := range seen {
		ids = append(ids, id)
	}
	return ids, nil
}
