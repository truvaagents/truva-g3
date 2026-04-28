package memory

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/go-redis/redis/v8"
	"github.com/google/uuid"
	"github.com/truvaagents/truva-g3/core"
	"github.com/truvaagents/truva-g3/telemetry"
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
// Returns error if the option value is invalid (fail-fast per CORE_DESIGN_PRINCIPLES).
type EpisodicMemoryOption func(*StreamEpisodicMemory) error

// WithEpisodicRedisClient sets the Redis client for episodic memory.
func WithEpisodicRedisClient(client *redis.Client) EpisodicMemoryOption {
	return func(v *StreamEpisodicMemory) error {
		if client == nil {
			return fmt.Errorf("redis client cannot be nil")
		}
		v.client = client
		return nil
	}
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
		v.logger = logger
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
	client       *redis.Client
	domain       string
	streamMaxLen int64
	eventTTL     time.Duration // TTL for individual event hash keys
	logger       core.Logger
}

// NewStreamEpisodicMemory creates a new Stream-backed episodic memory.
// Uses WithXXX option functions per FRAMEWORK_DESIGN_PRINCIPLES §Configuration.
func NewStreamEpisodicMemory(opts ...EpisodicMemoryOption) (*StreamEpisodicMemory, error) {
	v := &StreamEpisodicMemory{
		domain:       "default",
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
		v.logger.WarnWithContext(ctx, "Failed to marshal event", map[string]interface{}{
			"request_id": core.GetRequestID(ctx),
			"operation":  "record_event",
			"event_id":   event.EventID,
			"error":      err.Error(),
			"error_type": "marshal",
		})
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
		Stream:       streamKey,
		MaxLenApprox: v.streamMaxLen,
		Values:       map[string]interface{}{"event_id": event.EventID, "entity_id": primaryEntityID},
	})

	// 3. Index by all entities (one event, multiple sorted set entries)
	if len(event.Entities) > 0 {
		for _, entity := range event.Entities {
			if entity.Type != "" && entity.ID != "" {
				entityKey := v.entityIndexKey(entity.Type, entity.ID)
				pipe.ZAdd(ctx, entityKey, &redis.Z{Score: score, Member: event.EventID})
			}
		}
	} else if event.EntityType != "" && event.EntityID != "" {
		// Backward compat: singular fields
		entityKey := v.entityIndexKey(event.EntityType, event.EntityID)
		pipe.ZAdd(ctx, entityKey, &redis.Z{Score: score, Member: event.EventID})
	}

	// 4. Index by agent
	agentKey := v.agentIndexKey(event.AgentName)
	pipe.ZAdd(ctx, agentKey, &redis.Z{Score: score, Member: event.EventID})

	// 5. Dual-write: ScopeGlobal events also go to the global stream
	if event.Scope == core.ScopeGlobal {
		globalStreamKey := "truvag3:memory:global:events:stream"
		pipe.XAdd(ctx, &redis.XAddArgs{
			Stream:       globalStreamKey,
			MaxLenApprox: v.streamMaxLen,
			Values:       map[string]interface{}{"event_id": event.EventID, "domain": event.AgentDomain},
		})
	}

	_, err = pipe.Exec(ctx)
	if err != nil {
		v.logger.WarnWithContext(ctx, "Failed to record event", map[string]interface{}{
			"request_id": core.GetRequestID(ctx),
			"operation":  "record_event",
			"event_id":   event.EventID,
			"error":      err.Error(),
			"error_type": "stream_write",
		})
		telemetry.RecordSpanError(ctx, err)
		return nil // Fail-open
	}

	return nil
}

// QueryEvents retrieves events matching the filter criteria.
// Enforces scope-based visibility using callerDomain.
func (v *StreamEpisodicMemory) QueryEvents(ctx context.Context, callerDomain string, filter core.EventFilter) ([]core.AgentEvent, error) {
	limit := filter.Limit
	if limit <= 0 {
		limit = 50 // Default limit
	}

	// Determine which index to query
	var eventIDs []string
	var err error

	switch {
	case filter.EntityType != "" && filter.EntityID != "":
		eventIDs, err = v.queryByEntity(ctx, filter.EntityType, filter.EntityID, filter.Since, filter.Until, limit*2) // Over-fetch for post-filter
	case filter.AgentName != "":
		eventIDs, err = v.queryByAgent(ctx, filter.AgentName, filter.Since, filter.Until, limit*2)
	default:
		// Scan the domain stream for recent events
		eventIDs, err = v.queryByStream(ctx, filter.Since, limit*2)
	}

	if err != nil {
		v.logger.WarnWithContext(ctx, "Failed to query event index", map[string]interface{}{
			"request_id":    core.GetRequestID(ctx),
			"operation":     "query_events",
			"caller_domain": callerDomain,
			"error":         err.Error(),
			"error_type":    "index_read",
		})
		telemetry.RecordSpanError(ctx, err)
		return nil, nil // Fail-open
	}

	// Also include global events (cross-domain visibility for ScopeGlobal)
	// Only needed when the query isn't already filtered to a specific entity/agent
	// (entity/agent queries naturally include global events via the domain indexes)
	if filter.EntityType == "" && filter.EntityID == "" && filter.AgentName == "" {
		globalIDs, globalErr := v.queryGlobalStream(ctx, filter.Since, limit)
		if globalErr == nil {
			// Prepend global IDs (they may overlap with domain IDs — deduped below)
			eventIDs = append(globalIDs, eventIDs...)
		}
	}

	// Fetch event details and apply scope filtering
	seen := make(map[string]bool)
	var results []core.AgentEvent
	for _, id := range eventIDs {
		// Normalize cross-domain refs for dedup
		dedupKey := id
		if strings.HasPrefix(id, "xdomain:") {
			parts := strings.SplitN(id[8:], ":", 2)
			if len(parts) == 2 {
				dedupKey = parts[1]
			}
		}
		if seen[dedupKey] {
			continue // Dedup between domain and global streams
		}
		seen[dedupKey] = true
		event, fetchErr := v.fetchEvent(ctx, id)
		if fetchErr != nil || event == nil {
			continue
		}
		if !v.isVisible(event, callerDomain, filter.AgentName) {
			continue
		}
		if len(filter.ActionTypes) > 0 && !containsString(filter.ActionTypes, event.ActionType) {
			continue
		}
		if filter.AgentDomain != "" && event.AgentDomain != filter.AgentDomain {
			continue
		}
		results = append(results, *event)
		if len(results) >= limit {
			break
		}
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
		v.logger.WarnWithContext(ctx, "Failed to delete events", map[string]interface{}{
			"request_id":  core.GetRequestID(ctx),
			"operation":   "delete_events",
			"event_count": len(eventIDs),
			"error":       err.Error(),
			"error_type":  "stream_delete",
		})
		return nil // Fail-open
	}

	return nil
}

// --- Key schema helpers ---

func (v *StreamEpisodicMemory) domainStreamKey() string {
	return fmt.Sprintf("truvag3:memory:%s:events:stream", v.domain)
}

func (v *StreamEpisodicMemory) entityIndexKey(entityType, entityID string) string {
	return fmt.Sprintf("truvag3:memory:%s:entity:%s:%s", v.domain, entityType, entityID)
}

func (v *StreamEpisodicMemory) agentIndexKey(agentName string) string {
	return fmt.Sprintf("truvag3:memory:%s:agent:%s", v.domain, agentName)
}

func (v *StreamEpisodicMemory) eventKey(eventID string) string {
	return fmt.Sprintf("truvag3:memory:%s:event:%s", v.domain, eventID)
}

// --- Query helpers ---

func (v *StreamEpisodicMemory) queryByEntity(ctx context.Context, entityType, entityID string, since, until time.Time, limit int) ([]string, error) {
	key := v.entityIndexKey(entityType, entityID)
	return v.queryZSet(ctx, key, since, until, limit)
}

func (v *StreamEpisodicMemory) queryByAgent(ctx context.Context, agentName string, since, until time.Time, limit int) ([]string, error) {
	key := v.agentIndexKey(agentName)
	return v.queryZSet(ctx, key, since, until, limit)
}

func (v *StreamEpisodicMemory) queryZSet(ctx context.Context, key string, since, until time.Time, limit int) ([]string, error) {
	min := "-inf"
	max := "+inf"
	if !since.IsZero() {
		min = strconv.FormatFloat(float64(since.UnixMilli()), 'f', 0, 64)
	}
	if !until.IsZero() {
		max = strconv.FormatFloat(float64(until.UnixMilli()), 'f', 0, 64)
	}

	// Reverse range to get most recent first
	results, err := v.client.ZRevRangeByScore(ctx, key, &redis.ZRangeBy{
		Min:   min,
		Max:   max,
		Count: int64(limit),
	}).Result()
	if err != nil {
		return nil, err
	}
	return results, nil
}

func (v *StreamEpisodicMemory) queryByStream(ctx context.Context, since time.Time, limit int) ([]string, error) {
	start := "-"
	if !since.IsZero() {
		start = fmt.Sprintf("%d-0", since.UnixMilli())
	}

	msgs, err := v.client.XRevRangeN(ctx, v.domainStreamKey(), "+", start, int64(limit)).Result()
	if err != nil {
		return nil, err
	}

	var ids []string
	for _, msg := range msgs {
		if id, ok := msg.Values["event_id"].(string); ok {
			ids = append(ids, id)
		}
	}
	return ids, nil
}

func (v *StreamEpisodicMemory) queryGlobalStream(ctx context.Context, since time.Time, limit int) ([]string, error) {
	start := "-"
	if !since.IsZero() {
		start = fmt.Sprintf("%d-0", since.UnixMilli())
	}

	msgs, err := v.client.XRevRangeN(ctx, "truvag3:memory:global:events:stream", "+", start, int64(limit)).Result()
	if err != nil {
		return nil, err
	}

	var ids []string
	for _, msg := range msgs {
		id, _ := msg.Values["event_id"].(string)
		domain, _ := msg.Values["domain"].(string)
		if id == "" {
			continue
		}
		// For events from other domains, we need to look up their event hash
		// under the originating domain's key prefix. Store a prefixed lookup key
		// that fetchEventCrossDomain can resolve.
		if domain != "" && domain != v.domain {
			// Use a synthetic key that fetchEvent can detect and resolve
			ids = append(ids, "xdomain:"+domain+":"+id)
		} else {
			ids = append(ids, id)
		}
	}
	return ids, nil
}

func (v *StreamEpisodicMemory) fetchEvent(ctx context.Context, eventID string) (*core.AgentEvent, error) {
	// Handle cross-domain event references from the global stream
	key := v.eventKey(eventID)
	if strings.HasPrefix(eventID, "xdomain:") {
		parts := strings.SplitN(eventID[8:], ":", 2) // "xdomain:{domain}:{id}"
		if len(parts) == 2 {
			key = fmt.Sprintf("truvag3:memory:%s:event:%s", parts[0], parts[1])
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
// Returns error if the option value is invalid (fail-fast per CORE_DESIGN_PRINCIPLES).
type InvestigationCoordinatorOption func(*AtomicLockCoordinator) error

// WithCoordinatorRedisClient sets the Redis client for investigation coordination.
func WithCoordinatorRedisClient(client *redis.Client) InvestigationCoordinatorOption {
	return func(v *AtomicLockCoordinator) error {
		if client == nil {
			return fmt.Errorf("redis client cannot be nil")
		}
		v.client = client
		return nil
	}
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
		v.logger = logger
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
	client     *redis.Client
	domain     string
	defaultTTL time.Duration
	logger     core.Logger
}

// NewAtomicLockCoordinator creates a new Stream-backed investigation coordinator.
func NewAtomicLockCoordinator(opts ...InvestigationCoordinatorOption) (*AtomicLockCoordinator, error) {
	v := &AtomicLockCoordinator{
		domain:     "default",
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

	// SET key agentName NX PX ttl — atomic claim
	ok, err := v.client.SetNX(ctx, key, agentName, ttl).Result()
	if err != nil {
		v.logger.WarnWithContext(ctx, "Failed to claim investigation", map[string]interface{}{
			"request_id": core.GetRequestID(ctx),
			"operation":  "claim_investigation",
			"entity_id":  entityID,
			"error":      err.Error(),
			"error_type": "claim",
		})
		return false, "", nil // Fail-open: can't claim, but don't error
	}

	if ok {
		return true, "", nil // Successfully claimed
	}

	// Already claimed — find out by whom
	holder, err := v.client.Get(ctx, key).Result()
	if err != nil {
		return false, "", nil // Can't determine holder
	}
	return false, holder, nil
}

// ReleaseInvestigation releases a previously claimed investigation.
// Uses a Lua script to atomically check ownership before deleting.
func (v *AtomicLockCoordinator) ReleaseInvestigation(ctx context.Context, agentName, entityID string) error {
	key := v.investigationKey(entityID)

	// Lua script: only delete if current value matches agentName (ownership check)
	script := redis.NewScript(`
		if redis.call("GET", KEYS[1]) == ARGV[1] then
			return redis.call("DEL", KEYS[1])
		end
		return 0
	`)

	_, err := script.Run(ctx, v.client, []string{key}, agentName).Result()
	if err != nil && err != redis.Nil {
		v.logger.WarnWithContext(ctx, "Failed to release investigation", map[string]interface{}{
			"request_id": core.GetRequestID(ctx),
			"operation":  "release_investigation",
			"entity_id":  entityID,
			"error":      err.Error(),
			"error_type": "release",
		})
	}
	return nil // Fail-open
}

// GetActiveInvestigations returns all currently claimed entities and their holders.
func (v *AtomicLockCoordinator) GetActiveInvestigations(ctx context.Context) (map[string]string, error) {
	pattern := v.investigationKey("*")
	result := make(map[string]string)

	iter := v.client.Scan(ctx, 0, pattern, 100).Iterator()
	for iter.Next(ctx) {
		key := iter.Val()
		holder, err := v.client.Get(ctx, key).Result()
		if err != nil {
			continue // Key may have expired between scan and get
		}
		// Extract entity ID from key: truvag3:memory:{domain}:investigating:{entityID}
		entityID := extractEntityIDFromKey(key, v.domain)
		if entityID != "" {
			result[entityID] = holder
		}
	}
	if err := iter.Err(); err != nil {
		v.logger.WarnWithContext(ctx, "Failed to scan active investigations", map[string]interface{}{
			"request_id": core.GetRequestID(ctx),
			"operation":  "get_active_investigations",
			"error":      err.Error(),
			"error_type": "scan",
		})
		return nil, nil // Fail-open
	}

	return result, nil
}

func (v *AtomicLockCoordinator) investigationKey(entityID string) string {
	return fmt.Sprintf("truvag3:memory:%s:investigating:%s", v.domain, entityID)
}

// extractEntityIDFromKey parses "truvag3:memory:{domain}:investigating:{entityID}" → entityID
func extractEntityIDFromKey(key, domain string) string {
	prefix := fmt.Sprintf("truvag3:memory:%s:investigating:", domain)
	if strings.HasPrefix(key, prefix) {
		return key[len(prefix):]
	}
	return ""
}
