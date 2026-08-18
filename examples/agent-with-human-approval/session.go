package main

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
	"github.com/truvaagents/truva-g3/core"
)

// Session represents a chat session with conversation history.
type Session struct {
	ID        string                 `json:"id"`
	UserID    string                 `json:"user_id"`
	Title     string                 `json:"title"`
	CreatedAt time.Time              `json:"created_at"`
	UpdatedAt time.Time              `json:"updated_at"`
	Messages  []Message              `json:"messages"`
	Metadata  map[string]interface{} `json:"metadata,omitempty"`
}

// SessionSummary is a lightweight view of a session for listing.
type SessionSummary struct {
	ID           string    `json:"id"`
	Title        string    `json:"title"`
	MessageCount int       `json:"message_count"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
	Preview      string    `json:"preview"`
}

// Message represents a chat message in a session.
type Message struct {
	ID        string                 `json:"id"`
	Role      string                 `json:"role"` // "user" or "assistant"
	Content   string                 `json:"content"`
	Timestamp time.Time              `json:"timestamp"`
	Metadata  map[string]interface{} `json:"metadata,omitempty"`
}

// SessionStore provides Redis-based session management.
// Uses Redis DB 2 (RedisDBSessions) to isolate from service registry (DB 0).
type SessionStore struct {
	client      *core.RedisClient
	ttl         time.Duration
	maxMessages int
	logger      core.Logger
}

// NewSessionStore creates a new Redis-backed session store.
// It uses Redis DB 2 (RedisDBSessions) with namespace "truvag3:sessions"
// to keep session data separate from the service registry (DB 0).
func NewSessionStore(redisURL string, ttl time.Duration, maxMessages int, logger core.Logger) (*SessionStore, error) {
	client, err := core.NewRedisClient(core.RedisClientOptions{
		RedisURL:  redisURL,
		DB:        core.RedisDBSessions, // DB 2 - separate from registry (DB 0)
		Namespace: "truvag3:sessions",
		Logger:    logger,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to create Redis client for sessions: %w", err)
	}

	store := &SessionStore{
		client:      client,
		ttl:         ttl,
		maxMessages: maxMessages,
		logger:      logger,
	}

	if logger != nil {
		logger.Info("Session store initialized", map[string]interface{}{
			"redis_db":     core.RedisDBSessions,
			"namespace":    "truvag3:sessions",
			"ttl":          ttl.String(),
			"max_messages": maxMessages,
		})
	}

	return store, nil
}

// Create creates a new session for the given user.
func (s *SessionStore) Create(userID string, metadata map[string]interface{}) *Session {
	now := time.Now()
	session := &Session{
		ID:        uuid.New().String(),
		UserID:    userID,
		CreatedAt: now,
		UpdatedAt: now,
		Messages:  make([]Message, 0),
		Metadata:  metadata,
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := s.saveSession(ctx, session); err != nil {
		if s.logger != nil {
			s.logger.Error("Failed to save new session", map[string]interface{}{
				"session_id": session.ID,
				"user_id":    userID,
				"error":      err.Error(),
			})
		}
		// Return session anyway - it exists in memory even if Redis save failed
		return session
	}

	// Add to user's session index (sorted by UpdatedAt)
	if userID != "" {
		indexKey := s.userIndexKey(userID)
		if err := s.client.ZAdd(ctx, indexKey, redis.Z{
			Score:  float64(now.UnixMilli()),
			Member: session.ID,
		}); err != nil {
			if s.logger != nil {
				s.logger.Error("Failed to add session to index", map[string]interface{}{
					"session_id": session.ID,
					"user_id":    userID,
					"error":      err.Error(),
				})
			}
		}
	}

	// Increment active session counter
	if _, err := s.client.Incr(ctx, "active_session_count"); err != nil {
		if s.logger != nil {
			s.logger.Warn("Failed to increment session counter", map[string]interface{}{
				"error": err.Error(),
			})
		}
	}

	return session
}

// Get retrieves a session by ID.
func (s *SessionStore) Get(sessionID string) *Session {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	session, err := s.loadSession(ctx, sessionID)
	if err != nil {
		if s.logger != nil {
			s.logger.Debug("Session not found or expired", map[string]interface{}{
				"session_id": sessionID,
				"error":      err.Error(),
			})
		}
		return nil
	}

	return session
}

// Delete removes a session.
func (s *SessionStore) Delete(sessionID string) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Load session to get userID for index cleanup
	session, err := s.loadSession(ctx, sessionID)
	if err == nil && session.UserID != "" {
		indexKey := s.userIndexKey(session.UserID)
		_ = s.client.ZRem(ctx, indexKey, sessionID)
	}

	// Decrement active session counter
	if _, err := s.client.IncrBy(ctx, "active_session_count", -1); err != nil {
		if s.logger != nil {
			s.logger.Warn("Failed to decrement session counter", map[string]interface{}{
				"error": err.Error(),
			})
		}
	}

	if err := s.client.Del(ctx, sessionID); err != nil {
		if s.logger != nil {
			s.logger.Error("Failed to delete session", map[string]interface{}{
				"session_id": sessionID,
				"error":      err.Error(),
			})
		}
	}
}

// AddMessage adds a message to a session.
func (s *SessionStore) AddMessage(sessionID string, msg Message) bool {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Load existing session
	session, err := s.loadSession(ctx, sessionID)
	if err != nil {
		if s.logger != nil {
			s.logger.Error("Failed to load session for adding message", map[string]interface{}{
				"session_id": sessionID,
				"error":      err.Error(),
			})
		}
		return false
	}

	// Set message ID if not provided
	if msg.ID == "" {
		msg.ID = uuid.New().String()
	}

	// Auto-generate title from first user message
	if session.Title == "" && msg.Role == "user" {
		title := msg.Content
		if len(title) > 60 {
			title = title[:57] + "..."
		}
		session.Title = title
	}

	// Add message
	session.Messages = append(session.Messages, msg)

	// Trim to max messages (sliding window)
	if len(session.Messages) > s.maxMessages {
		session.Messages = session.Messages[len(session.Messages)-s.maxMessages:]
	}

	session.UpdatedAt = time.Now()

	// Save back to Redis
	if err := s.saveSession(ctx, session); err != nil {
		if s.logger != nil {
			s.logger.Error("Failed to save session after adding message", map[string]interface{}{
				"session_id": sessionID,
				"error":      err.Error(),
			})
		}
		return false
	}

	// Update score in user's index (bump UpdatedAt)
	if session.UserID != "" {
		indexKey := s.userIndexKey(session.UserID)
		_ = s.client.ZAdd(ctx, indexKey, redis.Z{
			Score:  float64(session.UpdatedAt.UnixMilli()),
			Member: session.ID,
		})
	}

	return true
}

// GetMessages retrieves messages from a session.
func (s *SessionStore) GetMessages(sessionID string, limit int) []Message {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	session, err := s.loadSession(ctx, sessionID)
	if err != nil {
		return nil
	}

	messages := session.Messages
	if limit > 0 && len(messages) > limit {
		messages = messages[len(messages)-limit:]
	}

	// Return a copy to avoid race conditions
	result := make([]Message, len(messages))
	copy(result, messages)
	return result
}

// GetHistory retrieves the full conversation history for a session.
func (s *SessionStore) GetHistory(sessionID string) []Message {
	return s.GetMessages(sessionID, 0)
}

// List returns a paginated list of sessions for a user, ordered by most recent.
// Uses pipeline batch fetch and lazy cleanup of expired sessions.
func (s *SessionStore) List(userID string, offset, limit int) ([]SessionSummary, int, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if userID == "" {
		return nil, 0, fmt.Errorf("user_id is required")
	}

	indexKey := s.userIndexKey(userID)

	// Get total count for pagination metadata
	total, err := s.client.ZCard(ctx, indexKey)
	if err != nil {
		return nil, 0, fmt.Errorf("failed to get session count: %w", err)
	}

	if total == 0 {
		return []SessionSummary{}, 0, nil
	}

	// Get session IDs from user's sorted set (descending by score = most recent first)
	// Keep the legacy command for Redis-compatible providers without ZRANGE REV.
	//nolint:staticcheck // ZRevRange remains supported by go-redis/v9.
	ids, err := s.client.ZRevRange(ctx, indexKey, int64(offset), int64(offset+limit-1))
	if err != nil {
		return nil, 0, fmt.Errorf("failed to list sessions: %w", err)
	}

	if len(ids) == 0 {
		return []SessionSummary{}, int(total), nil
	}

	// Pipeline: batch-fetch all sessions in one round-trip
	pipe := s.client.Pipeline()
	cmds := make([]*redis.StringCmd, len(ids))
	for i, id := range ids {
		cmds[i] = pipe.Get(ctx, s.client.FormatKey(id))
	}
	_, _ = pipe.Exec(ctx) // Errors are per-command, checked below

	// Parse results, collect stale entries for lazy cleanup
	var results []SessionSummary
	var staleIDs []interface{}

	for i, cmd := range cmds {
		if cmd.Err() == redis.Nil || cmd.Err() != nil {
			staleIDs = append(staleIDs, ids[i])
			continue
		}

		var session Session
		if err := json.Unmarshal([]byte(cmd.Val()), &session); err != nil {
			staleIDs = append(staleIDs, ids[i])
			continue
		}

		results = append(results, toSummary(&session))
	}

	// Fire-and-forget: remove expired entries from index
	if len(staleIDs) > 0 {
		go func() {
			cleanCtx, cleanCancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cleanCancel()
			_ = s.client.ZRem(cleanCtx, indexKey, staleIDs...)
		}()
	}

	// Adjust total to account for stale entries we just found
	adjustedTotal := int(total) - len(staleIDs)
	if adjustedTotal < 0 {
		adjustedTotal = 0
	}

	return results, adjustedTotal, nil
}

// UpdateTitle sets or updates the title of a session.
func (s *SessionStore) UpdateTitle(sessionID, title string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	session, err := s.loadSession(ctx, sessionID)
	if err != nil {
		return fmt.Errorf("session not found: %w", err)
	}

	session.Title = title
	session.UpdatedAt = time.Now()

	if err := s.saveSession(ctx, session); err != nil {
		return fmt.Errorf("failed to save session: %w", err)
	}

	// Update score in user's index
	if session.UserID != "" {
		indexKey := s.userIndexKey(session.UserID)
		_ = s.client.ZAdd(ctx, indexKey, redis.Z{
			Score:  float64(session.UpdatedAt.UnixMilli()),
			Member: session.ID,
		})
	}

	return nil
}

// GetActiveSessionCount returns the count of active sessions from a Redis counter.
func (s *SessionStore) GetActiveSessionCount() int {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	val, err := s.client.Get(ctx, "active_session_count")
	if err != nil {
		return 0
	}

	var count int
	if _, err := fmt.Sscanf(val, "%d", &count); err != nil || count < 0 {
		return 0
	}
	return count
}

// GetTTL returns the session TTL duration.
func (s *SessionStore) GetTTL() time.Duration {
	return s.ttl
}

// Close closes the Redis connection.
func (s *SessionStore) Close() error {
	if s.client != nil {
		return s.client.Close()
	}
	return nil
}

// userIndexKey returns the Redis key for a user's session index.
func (s *SessionStore) userIndexKey(userID string) string {
	return fmt.Sprintf("index:%s", userID)
}

// toSummary converts a Session to a SessionSummary.
func toSummary(session *Session) SessionSummary {
	preview := ""
	for _, msg := range session.Messages {
		if msg.Role == "user" {
			preview = msg.Content
			if len(preview) > 100 {
				preview = preview[:97] + "..."
			}
			break
		}
	}

	return SessionSummary{
		ID:           session.ID,
		Title:        session.Title,
		MessageCount: len(session.Messages),
		CreatedAt:    session.CreatedAt,
		UpdatedAt:    session.UpdatedAt,
		Preview:      preview,
	}
}

// saveSession saves a session to Redis.
func (s *SessionStore) saveSession(ctx context.Context, session *Session) error {
	data, err := json.Marshal(session)
	if err != nil {
		return fmt.Errorf("failed to marshal session: %w", err)
	}

	// Key is just the session ID - namespace is handled by RedisClient
	if err := s.client.Set(ctx, session.ID, string(data), s.ttl); err != nil {
		return fmt.Errorf("failed to save session to Redis: %w", err)
	}

	return nil
}

// loadSession loads a session from Redis.
func (s *SessionStore) loadSession(ctx context.Context, sessionID string) (*Session, error) {
	data, err := s.client.Get(ctx, sessionID)
	if err != nil {
		return nil, fmt.Errorf("failed to load session from Redis: %w", err)
	}

	var session Session
	if err := json.Unmarshal([]byte(data), &session); err != nil {
		return nil, fmt.Errorf("failed to unmarshal session: %w", err)
	}

	return &session, nil
}
