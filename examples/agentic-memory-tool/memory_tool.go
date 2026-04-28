package main

import (
	"log"
	"os"
	"time"

	"github.com/truvaagents/truva-g3/core"
)

// MemoryTool exposes the framework's shared memory interfaces as read-only
// HTTP capabilities. No AI client needed — this tool only reads from memory backends.
type MemoryTool struct {
	*core.BaseTool
	episodic    core.EpisodicMemory
	knowledge   core.SharedKnowledge          // may be nil (graceful degradation)
	coordinator core.InvestigationCoordinator // may be nil (graceful degradation)
	embedder    core.EmbeddingClient          // may be nil (needed for vector search)
	domain      string
}

// --- Request Types ---

// QueryEventsRequest represents the input for query_events.
type QueryEventsRequest struct {
	EntityType string `json:"entity_type,omitempty"` // Optional: filter by entity type
	EntityID   string `json:"entity_id,omitempty"`   // Optional: filter by entity ID
	AgentName  string `json:"agent_name,omitempty"`  // Optional: filter by recording agent
	ActionType string `json:"action_type,omitempty"` // Optional: filter by action type
	SinceHours int    `json:"since_hours,omitempty"` // Optional: lookback window in hours (default: 24)
	Limit      int    `json:"limit,omitempty"`       // Optional: max results (default: 20, max: 100)
}

// QueryKnowledgeRequest represents the input for query_knowledge.
type QueryKnowledgeRequest struct {
	Query     string `json:"query"`              // Required: natural language search query
	Namespace string `json:"namespace,omitempty"` // Optional: filter by namespace
	Limit     int    `json:"limit,omitempty"`     // Optional: max results (default: 5, max: 20)
}

// QueryInvestigationsRequest represents the input for query_investigations.
type QueryInvestigationsRequest struct {
	EntityID string `json:"entity_id,omitempty"` // Optional: check specific entity
}

// --- Response Types ---
// Field names must match OutputSummary declarations exactly.

// EventsResponse is the response for query_events.
type EventsResponse struct {
	Events     []EventSummary `json:"events"`
	TotalCount int            `json:"total_count"`
	Domain     string         `json:"domain"`
}

// EventSummary is a single event in the query_events response.
type EventSummary struct {
	EventID    string    `json:"event_id"`
	Timestamp  time.Time `json:"timestamp"`
	AgentName  string    `json:"agent_name"`
	ActionType string    `json:"action_type"`
	EntityType string    `json:"entity_type"`
	EntityID   string    `json:"entity_id"`
	Summary    string    `json:"summary"`
	Outcome    string    `json:"outcome"`
	Importance float64   `json:"importance"`
}

// KnowledgeResponse is the response for query_knowledge.
type KnowledgeResponse struct {
	Fragments  []KnowledgeFragment `json:"fragments"`
	TotalCount int                 `json:"total_count"`
	Domain     string              `json:"domain"`
}

// KnowledgeFragment is a single fragment in the query_knowledge response.
type KnowledgeFragment struct {
	Content      string   `json:"content"`
	Namespace    string   `json:"namespace"`
	Importance   float64  `json:"importance"`
	Confidence   float64  `json:"confidence"`
	SourceEvents []string `json:"source_events,omitempty"`
}

// InvestigationsResponse is the response for query_investigations.
type InvestigationsResponse struct {
	Investigations []Investigation `json:"investigations"`
	Domain         string          `json:"domain"`
}

// Investigation is a single active investigation in the query_investigations response.
type Investigation struct {
	EntityID string `json:"entity_id"`
	Holder   string `json:"holder"`
	Status   string `json:"status"` // "active"
}

// NewMemoryTool creates and initializes the tool with memory backends and capabilities.
func NewMemoryTool() *MemoryTool {
	domain := os.Getenv("TRUVAG3_AGENT_DOMAIN")
	if domain == "" {
		domain = "infrastructure"
	}

	tool := &MemoryTool{
		BaseTool: core.NewTool("agentic-memory-tool"),
		domain:   domain,
	}

	// Setup memory backends (Redis for episodic/coordinator, Qdrant for knowledge)
	if err := tool.setupBackends(); err != nil {
		log.Fatalf("Failed to setup memory backends: %v", err)
	}

	// Register all capabilities
	tool.registerCapabilities()
	return tool
}

func (t *MemoryTool) registerCapabilities() {
	// --- query_events ---
	t.RegisterCapability(core.Capability{
		Name: "query_events",
		Description: "Queries episodic memory for recent agent activity events by entity, agent, action type, and time range. " +
			"Use when the compact domain summary in <agent_memory> mentions activity worth investigating in detail. " +
			"Returns: event objects with event_id, timestamp, agent_name, action_type, entity details, summary, outcome, importance. " +
			"All parameters are optional — calling with no parameters returns the 20 most recent domain events.",
		InputTypes:  []string{"json"},
		OutputTypes: []string{"json"},
		Handler:     t.handleQueryEvents,

		InputSummary: &core.SchemaSummary{
			OptionalFields: []core.FieldHint{
				{Name: "entity_type", Type: "string", Example: "pod", Description: "Filter by entity type (pod, service, deployment)"},
				{Name: "entity_id", Type: "string", Example: "product-catalog-api", Description: "Filter by entity ID"},
				{Name: "agent_name", Type: "string", Example: "devops-chat-agent", Description: "Filter by recording agent name"},
				{Name: "action_type", Type: "string", Example: "create_issue", Description: "Filter by action type (create_issue, rollout_restart, etc.)"},
				{Name: "since_hours", Type: "integer", Example: "24", Description: "Events from the last N hours (default: 24)"},
				{Name: "limit", Type: "integer", Example: "20", Description: "Max events to return (default: 20, max: 100)"},
			},
		},

		OutputSummary: &core.SchemaSummary{
			RequiredFields: []core.FieldHint{
				{Name: "events", Type: "array", Description: "Array of event objects with event_id, timestamp, agent_name, action_type, entity_type, entity_id, summary, outcome, importance"},
				{Name: "total_count", Type: "integer", Example: "0", Description: "Number of events returned"},
				{Name: "domain", Type: "string", Example: "infrastructure", Description: "Domain scope of the query"},
			},
		},
	})

	// --- query_knowledge ---
	t.RegisterCapability(core.Capability{
		Name: "query_knowledge",
		Description: "Semantic search over institutional knowledge fragments derived from prior agent executions. " +
			"Use when investigating patterns, resolution strategies, or historical insights for a class of problem. " +
			"Returns: knowledge fragments with content, namespace, importance, confidence, and source event references. " +
			"Required: query (natural language search). Optional: namespace (incidents, runbooks, patterns), limit (default: 5, max: 20).",
		InputTypes:  []string{"json"},
		OutputTypes: []string{"json"},
		Handler:     t.handleQueryKnowledge,

		InputSummary: &core.SchemaSummary{
			RequiredFields: []core.FieldHint{
				{Name: "query", Type: "string", Example: "product-catalog-api latency remediation", Description: "Natural language search query"},
			},
			OptionalFields: []core.FieldHint{
				{Name: "namespace", Type: "string", Example: "incidents", Description: "Filter by knowledge namespace (incidents, runbooks, patterns)"},
				{Name: "limit", Type: "integer", Example: "5", Description: "Max results (default: 5, max: 20)"},
			},
		},

		OutputSummary: &core.SchemaSummary{
			RequiredFields: []core.FieldHint{
				{Name: "fragments", Type: "array", Description: "Array of knowledge fragments with content, namespace, importance, confidence, source_events"},
				{Name: "total_count", Type: "integer", Example: "0", Description: "Number of fragments returned"},
				{Name: "domain", Type: "string", Example: "infrastructure", Description: "Domain scope of the query"},
			},
		},
	})

	// --- query_investigations ---
	t.RegisterCapability(core.Capability{
		Name: "query_investigations",
		Description: "Lists active investigations for entities in the domain to prevent duplicate work across agents. " +
			"Use when checking if another agent is already handling an entity before starting a new investigation. " +
			"Returns: investigation objects with entity_id, holder (agent name), and status. " +
			"Optional: entity_id (check specific entity; omit to list all active investigations).",
		InputTypes:  []string{"json"},
		OutputTypes: []string{"json"},
		Handler:     t.handleQueryInvestigations,

		InputSummary: &core.SchemaSummary{
			OptionalFields: []core.FieldHint{
				{Name: "entity_id", Type: "string", Example: "product-catalog-api-78c468fc8b-q8v2s", Description: "Check investigation status for a specific entity"},
			},
		},

		OutputSummary: &core.SchemaSummary{
			RequiredFields: []core.FieldHint{
				{Name: "investigations", Type: "array", Description: "Array of active investigations with entity_id, holder (agent name), status"},
				{Name: "domain", Type: "string", Example: "infrastructure", Description: "Domain scope"},
			},
		},
	})
}
