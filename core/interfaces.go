package core

import (
	"context"
	"encoding/json"
	"sync"
	"time"
)

// Logger interface - minimal logging interface
type Logger interface {
	// Basic logging methods
	Info(msg string, fields map[string]interface{})
	Error(msg string, fields map[string]interface{})
	Warn(msg string, fields map[string]interface{})
	Debug(msg string, fields map[string]interface{})

	// Context-aware methods for distributed tracing and request correlation
	InfoWithContext(ctx context.Context, msg string, fields map[string]interface{})
	ErrorWithContext(ctx context.Context, msg string, fields map[string]interface{})
	WarnWithContext(ctx context.Context, msg string, fields map[string]interface{})
	DebugWithContext(ctx context.Context, msg string, fields map[string]interface{})
}

// ComponentAwareLogger extends Logger with component context support.
// This allows different parts of the application to have their own
// component identifier while sharing the same base configuration.
//
// ProductionLogger implements this interface. When a logger is
// component-aware, the component name appears in structured logs
// allowing filtering by component type:
//
//	kubectl logs ... | jq 'select(.component | startswith("agent/"))'
//	kubectl logs ... | jq 'select(.component == "framework/orchestration")'
//
// Component naming convention:
//   - "framework/core"          - Core framework (discovery, registry, config)
//   - "framework/orchestration" - Orchestration module
//   - "framework/ai"            - AI module
//   - "framework/resilience"    - Resilience patterns
//   - "framework/telemetry"     - Telemetry integration
//   - "agent/<name>"            - User agents (e.g., "agent/travel-research-orchestration")
//   - "tool/<name>"             - User tools (e.g., "tool/weather-service")
type ComponentAwareLogger interface {
	Logger
	WithComponent(component string) Logger
}

// Telemetry interface - optional telemetry support
type Telemetry interface {
	StartSpan(ctx context.Context, name string) (context.Context, Span)
	RecordMetric(name string, value float64, labels map[string]string)
}

// Span represents a telemetry span
type Span interface {
	End()
	SetAttribute(key string, value interface{})
	RecordError(err error)
}

// ProviderError represents a structured error from an AI provider or proxy.
// Implementations carry HTTP status, provider identity, and model info so that
// callers can make routing decisions without string-matching error messages.
//
// Retryability semantics — three orthogonal questions a chain client may ask:
//
//   - StatusCode():  Raw HTTP status (used by chain clients to apply
//     RFC-style retry rules: 4xx fail fast, 5xx retry, 429 retry).
//   - IsTransient(): Is this a proxy/infra hiccup the request never reached?
//     (e.g. Cloudflare returned an HTML 400 because it intercepted the
//     request before it ever hit the API.) When true, the chain may retry
//     on a different provider regardless of the status code.
//   - IsRetryable(): May this error category succeed on a different provider
//     even though the status code looks like a hard client error? Use this
//     for terminal-but-provider-specific failures: billing exhaustion,
//     account suspension, regional outages reported as 4xx, etc. When true,
//     the chain client treats the error as retryable instead of falling
//     into the fail-fast 4xx path. Providers set this from structured
//     metadata in the response body — never from string-matching at the
//     chain layer.
//
// IsTransient and IsRetryable are independent flags. A proxy 400 sets only
// IsTransient. A billing-exhausted 400 sets only IsRetryable. A real malformed
// request sets neither and is correctly classified as fail-fast 4xx.
type ProviderError interface {
	error
	StatusCode() int   // HTTP status (e.g., 400, 429, 500)
	Provider() string  // Provider name (e.g., "anthropic", "openai")
	Model() string     // Resolved model name (e.g., "claude-sonnet-4-5-20250929")
	IsTransient() bool // True for proxy/infra errors (Cloudflare, DNS, TLS)
	IsRetryable() bool // True for terminal-but-provider-specific errors (billing, account suspension)
}

// AIClient interface - optional AI support
type AIClient interface {
	GenerateResponse(ctx context.Context, prompt string, options *AIOptions) (*AIResponse, error)
}

// AIOptions for AI generation
type AIOptions struct {
	Model           string
	Temperature     float32
	MaxTokens       int
	SystemPrompt    string
	ReasoningEffort string                 // Semantic reasoning intent; provider translation decides exact wire format
	ResponseFormat  string                 // Portable values: "" or "json"; native formats belong in Extra
	Extra           map[string]interface{} // Provider-specific request body fields
	Headers         map[string]string      // Provider-specific request headers / beta flags
}

// AIResponse from AI client
type AIResponse struct {
	Content  string
	Model    string
	Provider string // Provider identifier (e.g., "openai", "openai.groq", "anthropic", "gemini", "bedrock")
	Usage    TokenUsage
}

// TokenUsage for AI responses
type TokenUsage struct {
	PromptTokens     int
	CompletionTokens int
	TotalTokens      int
}

// AggregatedTokenUsage accumulates token usage across multiple LLM calls
// within an orchestration request. Thread-safe for concurrent use.
type AggregatedTokenUsage struct {
	mu      sync.Mutex
	Total   TokenUsage
	ByPhase map[string]TokenUsage
}

// NewAggregatedTokenUsage creates a new accumulator.
func NewAggregatedTokenUsage() *AggregatedTokenUsage {
	return &AggregatedTokenUsage{
		ByPhase: make(map[string]TokenUsage),
	}
}

// Add records token usage for a named phase. Safe for concurrent use.
func (a *AggregatedTokenUsage) Add(phase string, usage TokenUsage) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.Total.PromptTokens += usage.PromptTokens
	a.Total.CompletionTokens += usage.CompletionTokens
	a.Total.TotalTokens += usage.TotalTokens
	existing := a.ByPhase[phase]
	existing.PromptTokens += usage.PromptTokens
	existing.CompletionTokens += usage.CompletionTokens
	existing.TotalTokens += usage.TotalTokens
	a.ByPhase[phase] = existing
}

// Snapshot returns a copy of the accumulated totals and per-phase breakdown.
func (a *AggregatedTokenUsage) Snapshot() (TokenUsage, map[string]TokenUsage) {
	a.mu.Lock()
	defer a.mu.Unlock()
	byPhase := make(map[string]TokenUsage, len(a.ByPhase))
	for k, v := range a.ByPhase {
		byPhase[k] = v
	}
	return a.Total, byPhase
}

// StreamChunk represents a single chunk in a streaming response
type StreamChunk struct {
	Content      string                 `json:"content,omitempty"`
	Delta        bool                   `json:"delta"`
	Index        int                    `json:"index"`
	FinishReason string                 `json:"finish_reason,omitempty"`
	Model        string                 `json:"model,omitempty"`
	Usage        *TokenUsage            `json:"usage,omitempty"`
	Metadata     map[string]interface{} `json:"metadata,omitempty"`
}

// StreamCallback is called for each chunk in a streaming response.
// Return an error to stop the stream early.
type StreamCallback func(chunk StreamChunk) error

// StreamingAIClient extends AIClient with streaming support
type StreamingAIClient interface {
	AIClient
	// StreamResponse generates a streaming response, calling callback for each chunk.
	// Returns the complete AIResponse after streaming finishes (for usage tracking).
	StreamResponse(ctx context.Context, prompt string, options *AIOptions, callback StreamCallback) (*AIResponse, error)
	// SupportsStreaming returns true if this client supports streaming responses.
	SupportsStreaming() bool
}

// Registry interface for tools (registration only)
type Registry interface {
	Register(ctx context.Context, info *ServiceInfo) error
	UpdateHealth(ctx context.Context, id string, status HealthStatus) error
	Unregister(ctx context.Context, id string) error
}

// Discovery interface for agents (registration + discovery)
type Discovery interface {
	Registry // Embed Registry
	Discover(ctx context.Context, filter DiscoveryFilter) ([]*ServiceInfo, error)
	// Backward compatibility methods
	FindService(ctx context.Context, serviceName string) ([]*ServiceInfo, error)
	FindByCapability(ctx context.Context, capability string) ([]*ServiceInfo, error)
}

// CapabilityExample provides example usage of a capability
type CapabilityExample struct {
	Description string          `json:"description"`
	Input       json.RawMessage `json:"input"`
	Output      json.RawMessage `json:"output"`
}

// ServiceRegistration is deprecated - use ServiceInfo from component.go instead
type ServiceRegistration = ServiceInfo

// HealthStatus for services
type HealthStatus string

const (
	HealthHealthy   HealthStatus = "healthy"
	HealthUnhealthy HealthStatus = "unhealthy"
	HealthUnknown   HealthStatus = "unknown"
)

// Memory is the interface for key-value state storage used by agents.
//
// Implementations MUST be safe for concurrent use by multiple goroutines.
// Get / Set / Delete / Exists may be called concurrently from request handlers
// and from orchestrator-driven parallel step dispatch. Implementations that
// violate this contract will eventually trigger Go's runtime concurrent-map-
// write fatal panic, which bypasses recover() and terminates the process.
//
// The only in-tree implementation is *MemoryStore (see core/memory_store.go) —
// mutex-guarded and TTL-aware, with optional periodic eviction via the
// MemoryStoreSweeper Runnable (see NewMemoryStoreSweeper). Callers may supply
// their own implementation (e.g. a Redis client) by setting BaseAgent.Memory
// before NewFramework is called.
type Memory interface {
	Get(ctx context.Context, key string) (string, error)
	Set(ctx context.Context, key string, value string, ttl time.Duration) error
	Delete(ctx context.Context, key string) error
	Exists(ctx context.Context, key string) (bool, error)
}

// --- Pipeline Hooks ---

// PipelineContext carries request data through pipeline hooks.
// Sequential execution contract: hooks run in order, no concurrent access.
type PipelineContext struct {
	Request     string
	Metadata    map[string]interface{}
	Enrichments map[string]interface{}
}

// Well-known enrichment keys for pipeline hooks.
const (
	EnrichmentConversationHistory  = "conversation_history"
	EnrichmentRAGContext           = "rag_context"
	EnrichmentActivityCoordination = "activity_coordination"
)

// PipelineHook is the base interface for all pipeline hooks.
// Implementors embed this and additionally implement one or more stage-specific
// interfaces (BeforePlanningHook, AfterPlanningHook, etc.).
type PipelineHook interface {
	Name() string
}

// BeforePlanningHook runs before the planning phase. Returning a non-nil
// PipelineShortCircuit skips the entire pipeline (e.g. semantic cache hit).
type BeforePlanningHook interface {
	PipelineHook
	BeforePlanning(ctx context.Context, pctx *PipelineContext) (*PipelineShortCircuit, error)
}

// PipelineShortCircuitKind identifies why an opt-in before-planning hook wants
// to stop the pipeline. Authoritative responses (for example, policy denials)
// do not depend on cache freshness. Cache responses are accepted only after
// orchestration verifies their stored variation dimensions.
type PipelineShortCircuitKind string

const (
	PipelineShortCircuitAuthoritative PipelineShortCircuitKind = "authoritative"
	PipelineShortCircuitCache         PipelineShortCircuitKind = "cache"
)

// PipelineGate exposes request-local cache policy to opt-in hooks. CacheVary
// returns a defensive copy on every call; mutating it cannot change framework
// enforcement or the view supplied to another hook.
type PipelineGate interface {
	CacheVary() map[string]string
	ResponseCacheReadDisabled() bool
}

// PipelineShortCircuitDecision adds provenance to the unchanged legacy
// PipelineShortCircuit payload. CachedAgainst must be the variation map stored
// with the cached entry. Echoing the gate's current values instead defeats the
// freshness check and violates this contract.
type PipelineShortCircuitDecision struct {
	ShortCircuit  *PipelineShortCircuit
	Kind          PipelineShortCircuitKind
	CachedAgainst map[string]string
}

// BeforePlanningDecisionHook is the opt-in, provenance-aware alternative to
// BeforePlanningHook. A hook may implement both; orchestration invokes only
// this method when it is available. Returning nil continues the pipeline.
type BeforePlanningDecisionHook interface {
	PipelineHook
	BeforePlanningDecision(
		ctx context.Context,
		pctx *PipelineContext,
		gate PipelineGate,
	) (*PipelineShortCircuitDecision, error)
}

// AfterPlanningHook runs after the planning phase. It may mutate the plan.
type AfterPlanningHook interface {
	PipelineHook
	AfterPlanning(ctx context.Context, pctx *PipelineContext, plan interface{}) (interface{}, error)
}

// AfterExecutionHook runs after tool execution completes.
type AfterExecutionHook interface {
	PipelineHook
	AfterExecution(ctx context.Context, pctx *PipelineContext, results interface{}) error
}

// AfterSynthesisHook runs after the synthesis phase. It may mutate the response.
type AfterSynthesisHook interface {
	PipelineHook
	AfterSynthesis(ctx context.Context, pctx *PipelineContext, response string) (string, error)
}

// PipelineShortCircuit allows a BeforePlanningHook to skip the entire pipeline
// and return a pre-computed response.
type PipelineShortCircuit struct {
	Response string
	Source   string
}

// --- Conversation Memory ---

// ConversationTurn represents a single turn in a conversation.
type ConversationTurn struct {
	Role      string                 `json:"role"`
	Content   string                 `json:"content"`
	Timestamp time.Time              `json:"timestamp"`
	Metadata  map[string]interface{} `json:"metadata,omitempty"`
}

// ConversationMemory provides session-scoped conversation history.
// The sessionID is passed per-call for flexibility.
type ConversationMemory interface {
	AddTurn(ctx context.Context, sessionID string, turn ConversationTurn) error
	GetHistory(ctx context.Context, sessionID string, maxTurns int) ([]ConversationTurn, error)
	Clear(ctx context.Context, sessionID string) error
}

// TokenCounter estimates token usage for prompt budgeting.
// Implementations may be heuristic or provider-backed.
//
// Callers must treat this as advisory sizing information and degrade gracefully
// when an implementation returns an error.
type TokenCounter interface {
	CountTokens(ctx context.Context, text string) (int, error)
}

// ConversationCompactor recursively compacts older conversation turns into a
// summary string suitable for prompt reuse on the request path.
//
// Implementations should be safe for request-path use and may fail open by
// returning an empty summary with a nil error if the caller is expected to
// degrade to non-compacted behavior.
type ConversationCompactor interface {
	Compact(ctx context.Context, priorSummary string, newTurns []ConversationTurn) (string, error)
}

// FullConversationMemory is an additive optional extension for conversation
// stores that can return the full append-only session history.
//
// Existing ConversationMemory implementations remain valid without this method.
// Callers must degrade gracefully when the optional interface is not present.
type FullConversationMemory interface {
	ConversationMemory
	GetFullHistory(ctx context.Context, sessionID string) ([]ConversationTurn, error)
}

// --- Semantic Memory ---

// MemoryResult represents a single result from a semantic memory search.
type MemoryResult struct {
	Content   string                 `json:"content"`
	Score     float64                `json:"score"`
	Metadata  map[string]interface{} `json:"metadata,omitempty"`
	Timestamp time.Time              `json:"timestamp"`
}

// SemanticMemory provides cross-session similarity-based memory.
type SemanticMemory interface {
	Store(ctx context.Context, namespace string, content string, metadata map[string]interface{}) error
	Search(ctx context.Context, namespace string, query string, topK int) ([]MemoryResult, error)
	Delete(ctx context.Context, namespace string, filter map[string]interface{}) error
}

// --- Embedding Client ---

// EmbeddingOptions configures embedding generation.
type EmbeddingOptions struct {
	Model      string
	Dimensions int
}

// EmbeddingResponse contains the result of an embedding generation call.
type EmbeddingResponse struct {
	Embeddings [][]float32
	Model      string
	Provider   string
	Usage      TokenUsage
}

// EmbeddingClient generates vector embeddings from text.
// Batch by default — callers pass one or more texts per call.
// Implementations should document any provider-imposed batch size limits.
type EmbeddingClient interface {
	GenerateEmbeddings(ctx context.Context, texts []string, options *EmbeddingOptions) (*EmbeddingResponse, error)
}

// --- Shared Agent Memory ---
//
// Cross-agent memory enables agents to share episodic events, coordinate
// investigations, accumulate knowledge, and learn from past executions.
//
// Four independent interfaces, each solving a distinct problem:
//   - EpisodicMemory: "What has any agent done about entity X?"
//   - InvestigationCoordinator: "Is someone else already investigating this?"
//   - SharedKnowledge: "What do we know about this class of problem?"
//   - MemoryReflector: "Synthesize higher-level patterns from raw events"
//
// Default implementations:
//   - Redis-compatible Streams: StreamEpisodicMemory, AtomicLockCoordinator (in core/)
//   - Qdrant: QdrantSharedKnowledge (in memory/ module — separate go.mod)
//   - In-memory: InMemoryEpisodicMemory, InMemoryInvestigationCoordinator (in core/)
//   - LLM: LLMMemoryReflector (in core/, uses AIClient interface)

// MemoryScope controls the visibility of events and knowledge fragments
// across agent domain boundaries.
type MemoryScope string

const (
	// ScopePrivate is visible only to the owning agent type within its domain.
	// Used for internal reasoning traces and failed intermediate steps.
	// Private fragments must NOT be stored in SharedKnowledge.
	ScopePrivate MemoryScope = "private"

	// ScopeSharedDomain is visible to all agents within the same domain.
	// Used for domain-specific events and knowledge (e.g., all infrastructure agents
	// can see each other's investigation results).
	ScopeSharedDomain MemoryScope = "shared_domain"

	// ScopeGlobal is visible to all agents across all domains.
	// Used for entity state changes and cross-domain correlations
	// (e.g., "service-X is down" is relevant to both infrastructure and commerce).
	ScopeGlobal MemoryScope = "global"
)

// EntityRef identifies an entity referenced by an event.
// Used in AgentEvent.Entities to support multi-entity indexing (one action → multiple entities).
type EntityRef struct {
	Type string `json:"type"` // "pod", "service", "deployment", "order"
	ID   string `json:"id"`   // "payment-service-pod-7x9k2"
}

// AgentEvent represents a structured record of something an agent did.
// The unit of episodic memory in cross-agent coordination.
// Each event is immutable once recorded — append-only by design.
type AgentEvent struct {
	EventID     string            `json:"event_id"`               // Unique ID (auto-generated by implementation)
	Timestamp   time.Time         `json:"timestamp"`              // When the event occurred
	AgentName   string            `json:"agent_name"`             // Agent type that performed the action (e.g., "event-driven-agent")
	AgentDomain string            `json:"agent_domain"`           // Domain the agent belongs to (e.g., "infrastructure")
	ActionType  string            `json:"action_type"`            // What was done (e.g., "pod_restart", "jira_created", "alert_fired")
	EntityType  string            `json:"entity_type"`            // Primary entity type (backward compat)
	EntityID    string            `json:"entity_id"`              // Primary entity ID (backward compat)
	Entities    []EntityRef       `json:"entities,omitempty"`     // All related entities — one event indexed under multiple entities
	Summary     string            `json:"summary"`                // Human-readable description of what happened
	Outcome     string            `json:"outcome"`                // Result: "success", "failure", "pending"
	Confidence  float64           `json:"confidence"`             // 0.0-1.0, how confident the agent is in the outcome
	TraceID     string            `json:"trace_id"`               // Links to distributed trace for debugging
	RequestID   string            `json:"request_id"`             // Links to the orchestration request that produced this event
	ParentEvent string            `json:"parent_event,omitempty"` // Parent request ID for nested delegation (see §0.2.3 of impl plan)
	Scope       MemoryScope       `json:"scope"`                  // Visibility scope
	Importance  float64           `json:"importance"`             // 1.0-10.0, heuristic or LLM-rated significance
	Metadata    map[string]string `json:"metadata,omitempty"`     // Flexible key-value pairs
}

// EventFilter defines query parameters for episodic memory retrieval.
// All fields are optional — zero values mean "no filter on this field."
type EventFilter struct {
	EntityType  string      // Filter by entity type (e.g., "pod")
	EntityID    string      // Filter by specific entity
	AgentName   string      // Filter by acting agent type
	AgentDomain string      // Filter by domain
	ActionTypes []string    // Filter by action type(s) — OR logic
	Since       time.Time   // Events after this time (zero = no lower bound)
	Until       time.Time   // Events before this time (zero = no upper bound)
	MinScope    MemoryScope // Minimum scope level to return (respects access control)
	Limit       int         // Max events to return (0 = implementation default)
}

// EpisodicMemory records and queries structured agent events.
// This is the cross-agent "what happened?" memory.
//
// Implementations must enforce scope-based visibility using the callerDomain parameter:
//   - ScopeGlobal events: visible to all callers
//   - ScopeSharedDomain events: visible only if callerDomain == event.AgentDomain
//   - ScopePrivate events: visible only if callerDomain == event.AgentDomain AND filter.AgentName == event.AgentName
//
// Default implementations:
//   - core.StreamEpisodicMemory (Redis-compatible Streams + sorted set indexes)
//   - core.InMemoryEpisodicMemory (pure Go, zero-infra fallback)
type EpisodicMemory interface {
	// RecordEvent appends a structured event to the shared event log.
	// The implementation assigns EventID if empty and sets Timestamp if zero.
	RecordEvent(ctx context.Context, event AgentEvent) error

	// QueryEvents retrieves events matching the filter criteria.
	// Results are ordered by timestamp descending (most recent first).
	QueryEvents(ctx context.Context, callerDomain string, filter EventFilter) ([]AgentEvent, error)

	// QueryEntityHistory returns all events for a specific entity, ordered chronologically.
	// Convenience method for the common "what happened to entity X?" query.
	QueryEntityHistory(ctx context.Context, callerDomain string, entityType, entityID string, since time.Time) ([]AgentEvent, error)

	// QueryRecentEvents returns the most recent events across all entities in a domain.
	// Provides situational awareness ("what happened recently?") without requiring
	// entity extraction from the query. Results ordered by timestamp descending.
	QueryRecentEvents(ctx context.Context, callerDomain string, since time.Time, limit int) ([]AgentEvent, error)

	// DeleteEvents removes events by ID. Used by compaction to clean up digested events.
	// Implementations should remove both the event data and all index references.
	// Returns nil if events don't exist (idempotent).
	DeleteEvents(ctx context.Context, eventIDs []string) error
}

// InvestigationCoordinator prevents duplicate work across agents.
// Uses optimistic locking (SETNX-style) to claim exclusive investigation of an entity.
//
// Default implementations:
//   - core.AtomicLockCoordinator (SET NX PX + Lua ownership check)
//   - core.InMemoryInvestigationCoordinator (sync.Mutex + map, for single-process)
type InvestigationCoordinator interface {
	// ClaimInvestigation attempts to claim exclusive investigation of an entity.
	// Returns (true, "", nil) if claimed successfully.
	// Returns (false, currentHolder, nil) if another agent already holds the claim.
	// The claim auto-expires after ttl to handle agent crashes.
	ClaimInvestigation(ctx context.Context, agentName, entityID string, ttl time.Duration) (claimed bool, currentHolder string, err error)

	// ReleaseInvestigation releases a previously claimed investigation.
	// Only the agent that holds the claim can release it (ownership check).
	// Returns nil if the claim was released or didn't exist.
	ReleaseInvestigation(ctx context.Context, agentName, entityID string) error

	// GetActiveInvestigations returns all currently claimed entities and their holders.
	// Returns a map of entityID → agentName.
	GetActiveInvestigations(ctx context.Context) (map[string]string, error)
}

// ActivitySignal represents a transient coordination signal indicating
// what an agent is currently working on. Signals expire via TTL.
// Not memory — signals are communication, not historical record.
type ActivitySignal struct {
	AgentName   string            `json:"agent_name"`
	AgentDomain string            `json:"agent_domain"`
	RequestID   string            `json:"request_id"`
	Query       string            `json:"query"`  // What the agent is working on (truncated)
	Status      string            `json:"status"` // "planning", "executing", "synthesizing", "completed"
	StartedAt   time.Time         `json:"started_at"`
	TTL         time.Duration     `json:"-"`                  // Not serialized — used for storage expiry
	Metadata    map[string]string `json:"metadata,omitempty"` // Extensible — developers add domain-specific fields
}

// ActivityCoordinator manages transient activity signals for real-time
// agent coordination. Signals are self-cleaning via TTL — no explicit
// cleanup needed for crash recovery. Agents announce what they're working on;
// other agents check before planning to coordinate.
//
// Default implementation: RedisActivityCoordinator (memory module).
// Backed by Redis SET with TTL — same infrastructure as InvestigationCoordinator.
type ActivityCoordinator interface {
	// AnnounceActivity publishes a signal indicating what the agent is working on.
	// The signal expires after TTL. Called at start of request processing.
	AnnounceActivity(ctx context.Context, signal ActivitySignal) error

	// UpdateStatus updates the status of an existing activity signal.
	// Used to track progress: "planning" → "executing" → "synthesizing" → "completed".
	UpdateStatus(ctx context.Context, requestID, status string) error

	// GetDomainActivities returns all active signals in a domain.
	// The caller's ActivityFilter decides which ones reach the LLM.
	GetDomainActivities(ctx context.Context, domain string) ([]ActivitySignal, error)

	// CompleteActivity removes the signal (or lets TTL expire).
	// Called when request processing finishes.
	CompleteActivity(ctx context.Context, requestID string) error
}

// KnowledgeFragment represents a piece of learned knowledge derived from events.
// Stored in SharedKnowledge backends (e.g., Qdrant) with vector embeddings
// for semantic similarity search.
type KnowledgeFragment struct {
	FragmentID   string            `json:"fragment_id"`             // Unique ID (UUID)
	Namespace    string            `json:"namespace"`               // Category: "incidents", "runbooks", "decisions"
	Content      string            `json:"content"`                 // Natural language knowledge statement
	Embedding    []float32         `json:"embedding,omitempty"`     // Vector embedding (populated by caller, not by SharedKnowledge)
	SourceEvents []string          `json:"source_events,omitempty"` // Event IDs this knowledge was derived from
	AgentDomain  string            `json:"agent_domain"`            // Domain that produced this knowledge
	Scope        MemoryScope       `json:"scope"`                   // Visibility scope (must NOT be ScopePrivate)
	Importance   float64           `json:"importance"`              // 1.0-10.0, significance rating
	CreatedAt    time.Time         `json:"created_at"`              // When the fragment was created
	LastAccessed time.Time         `json:"last_accessed"`           // Last time this fragment was returned in a search
	AccessCount  int               `json:"access_count"`            // Number of times retrieved
	Metadata     map[string]string `json:"metadata,omitempty"`      // Flexible key-value pairs
}

// RetrievalWeights controls the relative importance of recency, relevance,
// and importance in knowledge retrieval scoring.
// Weights should sum to ~1.0 for normalized scoring but this is not enforced.
type RetrievalWeights struct {
	Recency    float64 `json:"recency"`    // Weight for temporal proximity (higher = prefer recent)
	Relevance  float64 `json:"relevance"`  // Weight for semantic similarity (higher = prefer relevant)
	Importance float64 `json:"importance"` // Weight for significance rating (higher = prefer important)
}

// ScoredKnowledge is a KnowledgeFragment with its retrieval score breakdown.
// Returned by SharedKnowledge.SearchKnowledge.
type ScoredKnowledge struct {
	Fragment   KnowledgeFragment `json:"fragment"`
	Score      float64           `json:"score"`            // Combined weighted score
	Recency    float64           `json:"recency_score"`    // Recency component
	Relevance  float64           `json:"relevance_score"`  // Relevance component (cosine similarity)
	Importance float64           `json:"importance_score"` // Importance component
}

// SharedKnowledge stores and retrieves embedded knowledge fragments.
// This is the cross-agent "what do we know?" memory.
//
// Implementations must:
//   - Enforce scope filtering at query time using callerDomain
//   - Reject ScopePrivate fragments in StoreKnowledge
//   - Accept pre-embedded fragments (Embedding field populated by caller)
//   - Return (nil, nil) on infrastructure failure (fail-open)
//
// Default implementation: QdrantSharedKnowledge (in memory/ module).
// Upgrade path: pgvector, Weaviate, or any vector store via this interface.
type SharedKnowledge interface {
	// StoreKnowledge persists a knowledge fragment for semantic retrieval.
	// The fragment.Embedding field must be populated by the caller.
	// Returns error if fragment.Scope is ScopePrivate.
	StoreKnowledge(ctx context.Context, fragment KnowledgeFragment) error

	// SearchKnowledge performs semantic similarity search within a namespace.
	// Returns fragments ranked by the weighted retrieval score.
	// An empty namespace searches across all namespaces.
	// Returns (nil, nil) on infrastructure failure — never blocks the pipeline.
	SearchKnowledge(ctx context.Context, callerDomain string, namespace string,
		query string, topK int, weights RetrievalWeights) ([]ScoredKnowledge, error)

	// UpdateImportance adjusts the importance score of a knowledge fragment.
	// Used by compaction for importance decay on stale fragments.
	// Returns nil if fragment doesn't exist (idempotent).
	UpdateImportance(ctx context.Context, fragmentID string, newImportance float64) error
}

// CompactionConfig controls the memory compaction process.
type CompactionConfig struct {
	EventAgeThreshold   time.Duration // Events older than this get compacted into digests
	DigestWindow        time.Duration // Group events into digests of this size (e.g., 24h)
	ImportanceThreshold float64       // Knowledge fragments below this score get archived
	DryRun              bool          // Preview changes without applying
}

// MemoryReflector synthesizes higher-level knowledge from raw episodic events.
// This is the "institutional learning" mechanism — it distills patterns from
// accumulated events into reusable knowledge fragments.
//
// Not called per-request. Invoked by the application via CronJob or admin endpoint.
//
// Default implementation: LLMMemoryReflector (in core/, uses AIClient for synthesis).
type MemoryReflector interface {
	// Reflect examines recent events for an entity and generates knowledge fragments.
	// The returned fragments have Embedding = nil — the caller is responsible for
	// embedding them before storing in SharedKnowledge.
	Reflect(ctx context.Context, entityType, entityID string, since time.Time) ([]KnowledgeFragment, error)

	// Compact executes the memory compaction process.
	// Summarizes old events into digests, prunes low-importance knowledge.
	Compact(ctx context.Context, config CompactionConfig) error
}

// StepSummaryInput provides the data needed to generate an actionable summary for one execution step.
// Used by EventSummarizer to produce fact-based event descriptions that include tool-specific
// identifiers (ticket IDs, channel names, URLs) extracted from tool responses.
type StepSummaryInput struct {
	StepID      string                 `json:"step_id"`
	AgentName   string                 `json:"agent_name"`  // Tool/agent name (e.g., "jira-tool", "slack-tool")
	Capability  string                 `json:"capability"`  // Action type (e.g., "create_issue", "send_message")
	Instruction string                 `json:"instruction"` // Natural language instruction sent to tool
	Parameters  map[string]interface{} `json:"parameters,omitempty"`
	Response    string                 `json:"response"` // Tool response (truncated by caller)
	Success     bool                   `json:"success"`
}

// StepSummary is the per-step output produced by an EventSummarizer.
//
// Summary is a natural-language, one-sentence factual description of what
// the step did. Identical semantics to the legacy string return type that
// EventSummarizer.SummarizeSteps used to return.
//
// Entities is the LLM's identification of domain entities the step operated
// on. The framework does not define which entity types are valid — any
// domain-meaningful string is acceptable ("pod", "order", "flight",
// "patient"). Implementations that do not extract entities should return
// Entities as nil; consumers treat nil and empty slice identically.
//
// Entity extraction is performed as a side effect of summarization so callers
// do not pay for a separate LLM call over the same step data.
type StepSummary struct {
	Summary  string      `json:"summary"`
	Entities []EntityRef `json:"entities,omitempty"`
}

// EventSummarizer generates actionable, fact-based summaries for execution steps.
// Used by MemoryRecordHook to produce high-quality episodic event summaries that
// include tool-specific identifiers (ticket IDs, channel names, deployment names)
// AND — when the implementation chooses to — identify domain entities each step
// operated on.
//
// Default implementation: LLMEventSummarizer (orchestration module) — batched LLM call.
// Fallback: heuristic summary when nil or on error (fail-open).
//
// BREAKING CHANGE (pre-1.0): Return type changed from map[string]string to
// map[string]StepSummary. External implementers must update their method
// signature. The Summary field carries the same semantics as the legacy
// string return; the new Entities field is additive and may be left nil.
type EventSummarizer interface {
	// SummarizeSteps generates summaries for a batch of steps in a single call.
	// Returns a map of stepID -> StepSummary.
	// Implementations must be fail-open: return (empty map, nil) on infrastructure failure.
	SummarizeSteps(ctx context.Context, steps []StepSummaryInput) (map[string]StepSummary, error)
}

// ActivityCompactor compresses raw episodic events into a fixed-size digest
// suitable for LLM context injection. Used by MemoryEnrichmentHook to produce
// bounded-size domain activity summaries regardless of event volume.
//
// Default implementation: LLMActivityCompactor (orchestration module).
// Fallback: raw events when nil or on error (fail-open).
type ActivityCompactor interface {
	// CompactEvents takes raw events and produces a token-bounded digest string.
	// maxTokens controls the output size (approximate, 1 token ≈ 4 chars).
	// Implementations should be fail-open: on infrastructure failure, return ("", err).
	// The caller handles fallback — error is returned for observability/logging,
	// not for propagation to the user.
	CompactEvents(ctx context.Context, events []AgentEvent, maxTokens int) (string, error)

	// UpdateDigest incrementally updates an existing digest with new events.
	// previousDigest is the cached digest content. newEvents are events since last compaction.
	// Returns the updated digest. Caller falls back to previousDigest on error.
	UpdateDigest(ctx context.Context, previousDigest string, newEvents []AgentEvent, maxTokens int) (string, error)
}

// DigestCache stores and retrieves compacted domain activity digests.
// Implementations: RedisDigestCache (shared across instances), InMemoryDigestCache (per-instance).
type DigestCache interface {
	GetDigest(ctx context.Context, domain string) ([]byte, error)
	SetDigest(ctx context.Context, domain string, data []byte, ttl time.Duration) error
}

// SharedMemoryDeps holds cross-agent memory backends for the orchestration pipeline.
// Pass to orchestration.BuildMemoryHooks() to create the standard memory hook pipeline.
// Nil fields disable the corresponding feature (fail-open).
//
// This struct lives in core because both the memory module (returns it via ToDeps())
// and the orchestration module (reads it in BuildMemoryHooks) need to reference it.
type SharedMemoryDeps struct {
	// AgentName identifies this agent in memory events and coordination signals.
	AgentName string

	// AgentDomain groups agents for memory scoping. Agents in the same domain
	// see each other's events. Falls back to TRUVAG3_AGENT_DOMAIN if empty.
	AgentDomain string

	// Phase 1 — Episodic Memory + Coordination (requires Redis)
	Episodic    EpisodicMemory           // Required — minimum for memory to function
	Coordinator InvestigationCoordinator // Optional — nil disables investigation dedup

	// Phase 2 — Knowledge Extraction (requires Qdrant + embedding endpoint)
	Knowledge SharedKnowledge // Optional — nil disables knowledge search/extraction
	Embedder  EmbeddingClient // Optional — nil disables knowledge search/extraction

	// Activity Coordination — real-time agent signals
	ActivityCoordinator ActivityCoordinator // Optional — nil disables coordination signals

	// Digest Caching — reduces redundant LLM compaction calls
	DigestCache DigestCache // Optional — nil means full compaction every request

	// Background job coordination — prevents duplicate work across replicas.
	// Used by ReflectionJob and any future background jobs (scheduled compaction, HITL expiry).
	// Optional — nil means no locking (single-replica deployments).
	Lock DistributedLock
}

// Default no-op implementations

// NoOpLogger provides a no-op logger implementation
type NoOpLogger struct{}

func (n *NoOpLogger) Info(msg string, fields map[string]interface{})  {}
func (n *NoOpLogger) Error(msg string, fields map[string]interface{}) {}
func (n *NoOpLogger) Warn(msg string, fields map[string]interface{})  {}
func (n *NoOpLogger) Debug(msg string, fields map[string]interface{}) {}

func (n *NoOpLogger) InfoWithContext(ctx context.Context, msg string, fields map[string]interface{}) {
}
func (n *NoOpLogger) ErrorWithContext(ctx context.Context, msg string, fields map[string]interface{}) {
}
func (n *NoOpLogger) WarnWithContext(ctx context.Context, msg string, fields map[string]interface{}) {
}
func (n *NoOpLogger) DebugWithContext(ctx context.Context, msg string, fields map[string]interface{}) {
}

// NoOpTelemetry provides a no-op telemetry implementation
type NoOpTelemetry struct{}

func (n *NoOpTelemetry) StartSpan(ctx context.Context, name string) (context.Context, Span) {
	return ctx, &NoOpSpan{}
}

func (n *NoOpTelemetry) RecordMetric(name string, value float64, labels map[string]string) {}

// NoOpSpan provides a no-op span implementation
type NoOpSpan struct{}

func (n *NoOpSpan) End()                                       {}
func (n *NoOpSpan) SetAttribute(key string, value interface{}) {}
func (n *NoOpSpan) RecordError(err error)                      {}

// ─── User Memory ─────────────────────────────────────────────────────────────

// FactSource describes how a user fact was acquired.
type FactSource string

const (
	SourceExplicit   FactSource = "explicit"   // User directly stated it: "I'm vegetarian"
	SourceCorrection FactSource = "correction" // User corrected the agent: "No, I said aisle seat"
	SourceInferred   FactSource = "inferred"   // Extracted from conversation patterns
	SourceDerived    FactSource = "derived"    // Synthesized from multiple facts/sessions
)

// UserFact represents a learned piece of information about a specific user.
// Facts are private to the user and never visible to other users or agents
// outside the user's session context.
//
// This struct defines the interface boundary — what crosses between memory
// and orchestration. Implementation-specific fields (embeddings, internal
// indexes, supersession tracking) stay in the implementation.
type UserFact struct {
	FactID     string            `json:"fact_id"`
	UserID     string            `json:"user_id"`
	Namespace  string            `json:"namespace"` // Agent type scope: "travel", "devops", "universal"
	Category   string            `json:"category"`  // "preference", "identity", "constraint", "context", "summary", "relationship"
	Content    string            `json:"content"`   // Natural language: "Prefers window seats on flights over 4 hours"
	Source     FactSource        `json:"source"`
	Confidence float64           `json:"confidence"` // 0.0-1.0
	CreatedAt  time.Time         `json:"created_at"`
	UpdatedAt  time.Time         `json:"updated_at"`
	Metadata   map[string]string `json:"metadata,omitempty"`
}

const (
	// UserFactMetadataLifetimeKey stores the fact lifetime in UserFact.Metadata.
	UserFactMetadataLifetimeKey = "lifetime"

	// UserFactLifetimeDurable marks long-lived profile memory.
	UserFactLifetimeDurable = "durable"
	// UserFactLifetimeTransient marks short-lived task/session context.
	UserFactLifetimeTransient = "transient"
	// UserFactLifetimeSummary marks continuity-oriented summary memory.
	UserFactLifetimeSummary = "summary"
)

// EffectiveUserFactLifetime returns the effective lifetime for a fact.
// Metadata takes precedence when present; older facts without lifetime metadata
// fall back to category-based defaults for backward compatibility.
func EffectiveUserFactLifetime(fact UserFact) string {
	if fact.Metadata != nil {
		switch fact.Metadata[UserFactMetadataLifetimeKey] {
		case UserFactLifetimeDurable, UserFactLifetimeTransient, UserFactLifetimeSummary:
			return fact.Metadata[UserFactMetadataLifetimeKey]
		}
	}

	switch fact.Category {
	case "summary":
		return UserFactLifetimeSummary
	case "context":
		return UserFactLifetimeTransient
	case "identity", "preference", "constraint", "relationship":
		return UserFactLifetimeDurable
	default:
		return UserFactLifetimeDurable
	}
}

// UserMemory stores and retrieves per-user private facts.
// Implementations must enforce strict user isolation — a query for user A
// must never return facts belonging to user B, enforced at the storage level.
//
// This is the core interface that hooks depend on. Keep it minimal.
// Same method count as EpisodicMemory (4 methods).
type UserMemory interface {
	// Remember stores a fact about a user. Simple upsert by FactID.
	// Reconciliation (dedup, contradiction detection) is NOT this method's
	// concern — that logic lives in the extraction hook (orchestration module).
	Remember(ctx context.Context, userID string, fact UserFact) error

	// Recall retrieves facts relevant to the given query context.
	// Scoring strategy is implementation-defined (vector similarity, category
	// filtering, or hybrid). namespace="" searches all namespaces for the user.
	Recall(ctx context.Context, userID string, namespace string, queryContext string, limit int) ([]UserFact, error)

	// RecallByCategory retrieves facts in a specific category.
	RecallByCategory(ctx context.Context, userID string, namespace string, category string, limit int) ([]UserFact, error)

	// Forget deletes ALL facts for a user across all namespaces. GDPR Article 17.
	// Must be complete — no residual data in indexes, embeddings, or caches.
	Forget(ctx context.Context, userID string) error
}

// UserMemoryAdmin extends UserMemory with administrative operations for
// transparency UIs, GDPR data portability, and granular user control.
// Not all implementations need this — hooks depend only on UserMemory.
type UserMemoryAdmin interface {
	UserMemory

	// ListFacts returns all active facts for a user (for transparency UIs and GDPR export).
	// Returns (facts, totalCount, error).
	ListFacts(ctx context.Context, userID string, namespace string, offset int, limit int) ([]UserFact, int, error)

	// ForgetNamespace deletes all facts for a user in a specific namespace.
	ForgetNamespace(ctx context.Context, userID string, namespace string) error

	// ForgetFact deletes a single fact by ID (for user-driven memory management).
	ForgetFact(ctx context.Context, userID string, factID string) error
}

// UserMemoryDeps holds all dependencies needed by BuildUserMemoryHooks.
// Lives in core so that both memory (returns it via ToDeps()) and
// orchestration (reads it in BuildUserMemoryHooks) can reference it
// without importing each other. Same pattern as SharedMemoryDeps.
type UserMemoryDeps struct {
	UserMemory UserMemory      // Required
	Embedder   EmbeddingClient // Required for vector-backed backends; nil when using InMemoryUserMemory
	Namespace  string          // Agent type scope (e.g., "travel", "devops")
}

// Well-known enrichment key for user memory hooks.
const EnrichmentUserProfile = "user_profile"

// NoOpUserMemory is a no-op implementation for agents that don't use user memory.
// Satisfies both UserMemory and UserMemoryAdmin.
type NoOpUserMemory struct{}

func (n *NoOpUserMemory) Remember(ctx context.Context, userID string, fact UserFact) error {
	return nil
}

func (n *NoOpUserMemory) Recall(ctx context.Context, userID string, namespace string, queryContext string, limit int) ([]UserFact, error) {
	return nil, nil
}

func (n *NoOpUserMemory) RecallByCategory(ctx context.Context, userID string, namespace string, category string, limit int) ([]UserFact, error) {
	return nil, nil
}

func (n *NoOpUserMemory) Forget(ctx context.Context, userID string) error {
	return nil
}

func (n *NoOpUserMemory) ListFacts(ctx context.Context, userID string, namespace string, offset int, limit int) ([]UserFact, int, error) {
	return nil, 0, nil
}

func (n *NoOpUserMemory) ForgetNamespace(ctx context.Context, userID string, namespace string) error {
	return nil
}

func (n *NoOpUserMemory) ForgetFact(ctx context.Context, userID string, factID string) error {
	return nil
}

// DistributedLock provides mutual exclusion across multiple instances for efficiency
// (preventing duplicate work). If the lock fails or two holders overlap due to clock
// skew, the worst case is duplicate work — not data corruption.
//
// This is NOT a correctness lock — it does not provide fencing tokens for safe
// concurrent writes to external storage. For correctness guarantees, use
// consensus-backed systems (etcd, ZooKeeper). See: Martin Kleppmann,
// "How to do distributed locking" (2016) for the distinction.
//
// Implementations: RedisDistributedLock (memory module), NoOpDistributedLock (testing/single-replica).
type DistributedLock interface {
	// Acquire attempts to acquire the lock. Returns true if acquired, false if held by another.
	// The lock auto-expires after ttl to prevent deadlocks from crashed holders.
	Acquire(ctx context.Context, key string, ttl time.Duration) (acquired bool, err error)

	// Release explicitly releases the lock. Safe to call even if not held.
	Release(ctx context.Context, key string) error
}

// Runnable represents a long-running component whose lifecycle is tied to
// the framework's lifecycle. The component runs until ctx is cancelled.
//
// Implementations MUST:
//  1. Block in Start(ctx) until the component shuts down
//  2. Respond to ctx.Done() within the framework's drain timeout
//     (default 10 seconds, configurable via TRUVAG3_FRAMEWORK_RUNNABLE_DRAIN_TIMEOUT).
//     Implementations that ignore ctx will leak goroutines until process exit.
//  3. Return nil on graceful shutdown, error on startup or runtime failure
//  4. Never call Start more than once
//
// The framework cannot forcibly terminate a runnable that ignores ctx —
// Go provides no mechanism for this. Honoring ctx.Done() is the implementation's
// responsibility, not the framework's.
//
// This pattern is identical to sigs.k8s.io/controller-runtime/pkg/manager.Runnable —
// proven by 7+ years of production use across every Kubernetes operator.
//
// Implementations: ReflectionJob (memory module), and any future background job
// (scheduled compaction, HITL expiry processor, custom user jobs).
type Runnable interface {
	// Start runs the component until ctx is cancelled.
	// Returns nil on graceful shutdown, error on startup failure or runtime error.
	Start(ctx context.Context) error
}

// ============================================================================
// Global Registry Pattern for Telemetry Integration
// ============================================================================

// MetricsRegistry enables telemetry module to register itself with core.
// This avoids circular dependencies while enabling metrics emission from
// framework internals (discovery, cache, agent lifecycle).
//
// The telemetry module implements this interface via FrameworkMetricsRegistry
// and registers itself using SetMetricsRegistry() during initialization.
type MetricsRegistry interface {
	// === Existing methods (preserved for backward compatibility) ===

	// Counter increments a counter metric by 1
	// Example: Counter("discovery.registrations", "service_type", "agent")
	Counter(name string, labels ...string)

	// EmitWithContext emits a metric with context for trace correlation
	// This is the generic emission method - works for any metric type
	EmitWithContext(ctx context.Context, name string, value float64, labels ...string)

	// GetBaggage returns baggage from context for correlation
	GetBaggage(ctx context.Context) map[string]string

	// === New methods for explicit metric type semantics ===

	// Gauge sets a gauge metric to a specific value
	// Use for point-in-time measurements (active connections, queue size, etc.)
	// Example: Gauge("discovery.services.active", 5, "namespace", "default")
	Gauge(name string, value float64, labels ...string)

	// Histogram records a value in a histogram distribution
	// Use for latency, size distributions, etc.
	// Example: Histogram("discovery.lookup.duration_ms", 12.5, "service_type", "tool")
	Histogram(name string, value float64, labels ...string)
}

// Global registry - set by telemetry module when it initializes
var globalMetricsRegistry MetricsRegistry

// SetMetricsRegistry allows telemetry module to register itself
func SetMetricsRegistry(registry MetricsRegistry) {
	globalMetricsRegistry = registry

	// Enable metrics on all existing loggers
	enableMetricsOnExistingLoggers()
}

// GetGlobalMetricsRegistry returns the global metrics registry if available.
// Returns nil if telemetry module has not registered a metrics registry yet.
// This enables framework modules to emit metrics without creating circular dependencies.
//
// Usage pattern:
//
//	if registry := core.GetGlobalMetricsRegistry(); registry != nil {
//	    registry.EmitWithContext(ctx, "metric.name", value, labels...)
//	}
func GetGlobalMetricsRegistry() MetricsRegistry {
	return globalMetricsRegistry
}

// Track created loggers to enable metrics when telemetry becomes available
var createdLoggers []*ProductionLogger
var loggersMutex sync.RWMutex

func trackLogger(logger *ProductionLogger) {
	loggersMutex.Lock()
	defer loggersMutex.Unlock()

	createdLoggers = append(createdLoggers, logger)

	// If metrics already available, enable immediately
	if globalMetricsRegistry != nil {
		logger.EnableMetrics()
	}
}

func enableMetricsOnExistingLoggers() {
	loggersMutex.Lock()
	defer loggersMutex.Unlock()

	for _, logger := range createdLoggers {
		logger.EnableMetrics()
	}
}
