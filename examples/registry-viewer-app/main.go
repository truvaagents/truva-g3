package main

import (
	"bytes"
	"compress/gzip"
	"context"
	"embed"
	"encoding/csv"
	"encoding/json"
	"flag"
	"fmt"
	"hash/fnv"
	"io"
	"io/fs"
	"log"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"sync/atomic"

	"github.com/go-redis/redis/v8"
	"github.com/truvaagents/truva-g3/core"
	"github.com/truvaagents/truva-g3/memory"
	"github.com/truvaagents/truva-g3/orchestration"
	"golang.org/x/sync/errgroup"
	"golang.org/x/sync/singleflight"
)

//go:embed static/*
var staticFiles embed.FS

// ServiceInfo represents a registered service in the registry
type ServiceInfo struct {
	ID           string                 `json:"id"`
	Name         string                 `json:"name"`
	Type         string                 `json:"type"` // "tool" or "agent"
	Description  string                 `json:"description"`
	Address      string                 `json:"address"`
	Port         int                    `json:"port"`
	Capabilities []Capability           `json:"capabilities"`
	Metadata     map[string]interface{} `json:"metadata"`
	Health       string                 `json:"health"`
	LastSeen     time.Time              `json:"last_seen"`
}

// Capability represents a service capability
type Capability struct {
	Name         string        `json:"name"`
	Description  string        `json:"description"`
	Version      string        `json:"version,omitempty"`
	Endpoint     string        `json:"endpoint,omitempty"`
	InputTypes   []string      `json:"input_types,omitempty"`
	OutputTypes  []string      `json:"output_types,omitempty"`
	InputSummary *InputSummary `json:"input_summary,omitempty"`
	Internal     bool          `json:"internal,omitempty"`
}

// InputSummary contains parameter information
type InputSummary struct {
	Required []ParamInfo `json:"required,omitempty"`
	Optional []ParamInfo `json:"optional,omitempty"`
}

// ParamInfo describes a parameter
type ParamInfo struct {
	Name        string `json:"name"`
	Type        string `json:"type"`
	Description string `json:"description,omitempty"`
	Example     string `json:"example,omitempty"`
}

// RegistryResponse is the API response format
type RegistryResponse struct {
	Services   []ServiceInfo `json:"services"`
	TotalCount int           `json:"totalCount"`
	ToolCount  int           `json:"toolCount"`
	AgentCount int           `json:"agentCount"`
	Timestamp  time.Time     `json:"timestamp"`
}

// nonNilSlice returns an empty slice for nil input. Use at API-response
// construction sites whenever a list field is about to be JSON-encoded —
// clients expect `[]` not `null` for a list-typed field, and normalizing
// at the server boundary means no downstream consumer (this viewer,
// Swagger UI, any future tool, or `curl | jq` pipelines) has to
// defensively handle the `null` case.
//
// Go's encoding/json serializes a nil slice as JSON `null`, which is
// technically valid JSON but is a wart for list-typed fields: JS code
// that calls `.sort()`, `.filter()`, `.forEach()`, or `.length` on the
// value throws a TypeError on `null`. Every empty-data path becomes a
// visible error in the UI unless every caller defensively null-checks,
// which couples every consumer to a wire-format quirk that's fixable at
// the source once.
func nonNilSlice[T any](s []T) []T {
	if s == nil {
		return []T{}
	}
	return s
}

// SwaggerURL is one entry in the Swagger UI `urls` array.
// The format is dictated by Swagger UI itself:
// https://swagger.io/docs/open-source-tools/swagger-ui/usage/configuration/
// Swagger UI renders a dropdown using `name` and fetches the spec from `url`.
type SwaggerURL struct {
	Name string `json:"name"`
	URL  string `json:"url"`
}

// ============================================================================
// LLM Debug Types - using orchestration package types
// ============================================================================

// LLMDebugListResponse is the API response for listing debug records
type LLMDebugListResponse struct {
	Records   []orchestration.LLMDebugRecordSummary `json:"records"`
	Total     int                                   `json:"total"`
	Timestamp time.Time                             `json:"timestamp"`
}

// ============================================================================
// HITL Checkpoint Types (local types for API responses)
// Note: These are kept local due to UI-specific fields and structural differences
// from framework types (typed strings vs plain strings, different field names)
// ============================================================================

// HITLCheckpoint represents an execution checkpoint awaiting human approval
type HITLCheckpoint struct {
	CheckpointID       string                 `json:"checkpoint_id"`
	RequestID          string                 `json:"request_id"`
	InterruptPoint     string                 `json:"interrupt_point"`
	Decision           *InterruptDecision     `json:"decision,omitempty"`
	Plan               *RoutingPlan           `json:"plan,omitempty"`
	CompletedSteps     []StepResult           `json:"completed_steps,omitempty"`
	CurrentStep        *RoutingStep           `json:"current_step,omitempty"`
	CurrentStepResult  *StepResult            `json:"current_step_result,omitempty"`
	StepResults        map[string]*StepResult `json:"step_results,omitempty"`
	ResolvedParameters map[string]interface{} `json:"resolved_parameters,omitempty"`
	OriginalRequest    string                 `json:"original_request"`
	UserContext        map[string]interface{} `json:"user_context,omitempty"`
	CreatedAt          time.Time              `json:"created_at"`
	ExpiresAt          time.Time              `json:"expires_at"`
	Status             string                 `json:"status"`
	AgentName          string                 `json:"agent_name,omitempty"`
	AgentAddress       string                 `json:"agent_address,omitempty"` // Direct HTTP address for command routing (populated by framework at creation)
	RequestMode        string                 `json:"request_mode,omitempty"`  // "streaming" or "non_streaming" — drives RC3 approve/reject guard
}

// InterruptDecision contains the decision context for an interrupt
type InterruptDecision struct {
	ShouldInterrupt bool                   `json:"should_interrupt"`
	Reason          string                 `json:"reason"`
	Message         string                 `json:"message"`
	Priority        string                 `json:"priority"`
	Timeout         int64                  `json:"timeout,omitempty"`
	DefaultAction   string                 `json:"default_action,omitempty"`
	Metadata        map[string]interface{} `json:"metadata,omitempty"`
}

// RoutingPlan represents an LLM-generated execution plan
type RoutingPlan struct {
	PlanID            string        `json:"plan_id,omitempty"`
	RequestID         string        `json:"request_id,omitempty"`
	OriginalRequest   string        `json:"original_request,omitempty"`
	Mode              string        `json:"mode,omitempty"`
	Steps             []RoutingStep `json:"steps"`
	SynthesisStrategy string        `json:"synthesis_strategy,omitempty"`
	Rationale         string        `json:"rationale,omitempty"`
	CreatedAt         *time.Time    `json:"created_at,omitempty"`
	Terminal          *bool         `json:"terminal,omitempty"`
	ContinuationNote  string        `json:"continuation_note,omitempty"`
	PhaseNumber       int           `json:"phase_number,omitempty"`
}

// RoutingStep represents a single step in an execution plan
type RoutingStep struct {
	StepID         string                 `json:"step_id"`
	Capability     string                 `json:"capability"`
	ServiceName    string                 `json:"service_name,omitempty"`
	AgentName      string                 `json:"agent_name,omitempty"`
	Namespace      string                 `json:"namespace,omitempty"`
	CapabilityName string                 `json:"capability_name,omitempty"`
	Instruction    string                 `json:"instruction,omitempty"`
	Parameters     map[string]interface{} `json:"parameters,omitempty"`
	DependsOn      []string               `json:"depends_on,omitempty"`
	Description    string                 `json:"description,omitempty"`
	ExpectedOutput string                 `json:"expected_output,omitempty"`
	Metadata       map[string]interface{} `json:"metadata,omitempty"`
}

// StepResult represents the result of executing a step
type StepResult struct {
	StepID       string                 `json:"step_id"`
	Capability   string                 `json:"capability"`
	ServiceName  string                 `json:"service_name,omitempty"`
	AgentName    string                 `json:"agent_name,omitempty"`
	Namespace    string                 `json:"namespace,omitempty"`
	Instruction  string                 `json:"instruction,omitempty"`
	Parameters   map[string]interface{} `json:"parameters,omitempty"`
	Success      bool                   `json:"success"`
	Response     interface{}            `json:"response,omitempty"`
	ResponseText string                 `json:"response_text,omitempty"`
	Error        string                 `json:"error,omitempty"`
	Duration     int64                  `json:"duration,omitempty"`
	DurationMs   int64                  `json:"duration_ms,omitempty"`
	Attempts     int                    `json:"attempts,omitempty"`
	StartTime    *time.Time             `json:"start_time,omitempty"`
	EndTime      *time.Time             `json:"end_time,omitempty"`
	Skipped      bool                   `json:"skipped,omitempty"`
	Metadata     map[string]interface{} `json:"metadata,omitempty"`
}

// HITLCheckpointSummary is a lightweight version for listing
type HITLCheckpointSummary struct {
	CheckpointID    string    `json:"checkpoint_id"`
	RequestID       string    `json:"request_id"`
	InterruptPoint  string    `json:"interrupt_point"`
	Reason          string    `json:"reason"`
	Priority        string    `json:"priority"`
	Message         string    `json:"message"`
	OriginalRequest string    `json:"original_request"`
	StepCount       int       `json:"step_count"`
	CompletedCount  int       `json:"completed_count"`
	CurrentStep     string    `json:"current_step,omitempty"`
	StepID          string    `json:"step_id,omitempty"`
	StepInstruction string    `json:"step_instruction,omitempty"`
	CreatedAt       time.Time `json:"created_at"`
	ExpiresAt       time.Time `json:"expires_at"`
	Status          string    `json:"status"`
	AgentName       string    `json:"agent_name,omitempty"`
	AgentAddress    string    `json:"agent_address,omitempty"` // Direct HTTP address for command routing
	RequestMode     string    `json:"request_mode,omitempty"`  // "streaming" or "non_streaming"
}

// HITLCheckpointListResponse is the API response for listing checkpoints
type HITLCheckpointListResponse struct {
	Checkpoints []HITLCheckpointSummary `json:"checkpoints"`
	Total       int                     `json:"total"`
	Timestamp   time.Time               `json:"timestamp"`
}

// ============================================================================
// Execution DAG Types (mirrors orchestration/execution_store.go)
// ============================================================================

// Redis key patterns for Execution DAG (mirrors orchestration/redis_execution_store.go)
const (
	executionKeyPrefix   = "truvag3:execution:debug:"
	executionIndexKey    = "truvag3:execution:debug:index"
	executionTracePrefix = "truvag3:execution:debug:trace:"
)

// StoredExecution contains everything needed for DAG visualization
type StoredExecution struct {
	RequestID         string            `json:"request_id"`
	OriginalRequestID string            `json:"original_request_id,omitempty"`
	TraceID           string            `json:"trace_id"`
	AgentName         string            `json:"agent_name,omitempty"`
	OriginalRequest   string            `json:"original_request"`
	Plan              *RoutingPlan      `json:"plan"`
	Result            *ExecutionResult  `json:"result"`
	Interrupted       bool              `json:"interrupted,omitempty"` // True if execution was interrupted for HITL
	Checkpoint        *HITLCheckpoint   `json:"checkpoint,omitempty"`  // Checkpoint data if interrupted
	CreatedAt         time.Time         `json:"created_at"`
	Metadata          map[string]string `json:"metadata,omitempty"`

	// Multi-phase execution data
	PhasePlans     []*RoutingPlan `json:"phase_plans,omitempty"`
	PhaseCount     int            `json:"phase_count,omitempty"`
	ForcedTerminal bool           `json:"forced_terminal,omitempty"`
}

// ExecutionResult represents the outcome of plan execution
type ExecutionResult struct {
	PlanID        string                 `json:"plan_id"`
	Steps         []StepResult           `json:"steps"`
	Success       bool                   `json:"success"`
	TotalDuration int64                  `json:"total_duration"` // nanoseconds
	Metadata      map[string]interface{} `json:"metadata,omitempty"`
}

// ExecutionSummary is a lightweight version for listing
type ExecutionSummary struct {
	RequestID          string            `json:"request_id"`
	OriginalRequestID  string            `json:"original_request_id,omitempty"`
	TraceID            string            `json:"trace_id"`
	AgentName          string            `json:"agent_name,omitempty"`
	OriginalRequest    string            `json:"original_request"`
	Success            bool              `json:"success"`
	Interrupted        bool              `json:"interrupted,omitempty"`
	StepCount          int               `json:"step_count"`
	FailedSteps        int               `json:"failed_steps"`
	TotalDurationMs    int64             `json:"total_duration_ms"`
	LLMTotalDurationMs int64             `json:"llm_total_duration_ms,omitempty"` // LLM call duration for total time calculation
	CreatedAt          time.Time         `json:"created_at"`
	Metadata           map[string]string `json:"metadata,omitempty"`
}

// ExecutionListResponse is the API response for listing executions
type ExecutionListResponse struct {
	Executions []ExecutionSummary `json:"executions"`
	Total      int                `json:"total"`
	HasMore    bool               `json:"has_more"`
	NextCursor string             `json:"next_cursor,omitempty"` // Score of last item; pass as ?cursor= for next page
	Timestamp  time.Time          `json:"timestamp"`
}

// DAGNode represents a node in the DAG visualization
type DAGNode struct {
	ID          string                 `json:"id"`
	Label       string                 `json:"label"`
	Instruction string                 `json:"instruction"`
	Status      string                 `json:"status"`
	DurationMs  int64                  `json:"duration_ms"`
	Level       int                    `json:"level"`
	NodeType    string                 `json:"node_type,omitempty"` // "step" (default) or "phase_boundary"
	Metadata    map[string]interface{} `json:"metadata,omitempty"`
}

// DAGEdge represents an edge in the DAG visualization
type DAGEdge struct {
	Source   string `json:"source"`
	Target   string `json:"target"`
	EdgeType string `json:"edge_type,omitempty"` // "dependency" (default) or "phase_transition"
}

// DAGStatistics contains computed statistics for the DAG
type DAGStatistics struct {
	TotalNodes     int `json:"total_nodes"`
	CompletedNodes int `json:"completed_nodes"`
	FailedNodes    int `json:"failed_nodes"`
	SkippedNodes   int `json:"skipped_nodes"`
	MaxParallelism int `json:"max_parallelism"`
	Depth          int `json:"depth"`
}

// DAGResponse is the computed DAG structure for visualization
type DAGResponse struct {
	Nodes      []DAGNode     `json:"nodes"`
	Edges      []DAGEdge     `json:"edges"`
	Levels     [][]string    `json:"levels"`
	Statistics DAGStatistics `json:"statistics"`
}

// ResolutionAnalyticsRecord represents a single step's resolution data for export
type ResolutionAnalyticsRecord struct {
	RequestID      string `json:"request_id"`
	StepID         string `json:"step_id"`
	Capability     string `json:"capability"`
	AgentName      string `json:"agent_name"`
	Success        bool   `json:"success"`
	AutoWiredCount int    `json:"auto_wired_count"`
	MicroResolved  int    `json:"micro_resolved_count"`
	SemanticRetry  int    `json:"semantic_retry_count"`
	UserProvided   int    `json:"user_provided_count"`
	AutoWireDurUs  int64  `json:"auto_wiring_duration_us"`
	MicroResolvMs  int64  `json:"micro_resolution_duration_ms"`
	SourceKeyCount int    `json:"source_data_key_count"`
	TotalParams    int    `json:"total_params"`
}

// ResolutionAnalyticsSummary provides aggregate statistics
type ResolutionAnalyticsSummary struct {
	TotalExecutions     int     `json:"total_executions"`
	TotalSteps          int     `json:"total_steps"`
	StepsWithResolution int     `json:"steps_with_resolution"`
	AvgAutoWirePercent  float64 `json:"avg_auto_wire_percent"`
	AvgMicroResolveMs   float64 `json:"avg_micro_resolution_ms"`
}

// ResolutionAnalyticsResponse is the JSON response for analytics export
type ResolutionAnalyticsResponse struct {
	Records    []ResolutionAnalyticsRecord `json:"records"`
	Summary    ResolutionAnalyticsSummary  `json:"summary"`
	ExportedAt time.Time                   `json:"exported_at"`
}

// UnifiedExecutionView combines all related data for a single request view
// This provides a "one-stop shop" for debugging and understanding request execution
type UnifiedExecutionView struct {
	// Core execution data
	RequestID         string           `json:"request_id"`
	OriginalRequestID string           `json:"original_request_id,omitempty"`
	TraceID           string           `json:"trace_id,omitempty"`
	AgentName         string           `json:"agent_name,omitempty"`
	OriginalRequest   string           `json:"original_request"`
	CreatedAt         time.Time        `json:"created_at"`
	Success           bool             `json:"success"`
	TotalDurationMs   int64            `json:"total_duration_ms"`
	Plan              *RoutingPlan     `json:"plan,omitempty"`
	Result            *ExecutionResult `json:"result,omitempty"`
	Interrupted       bool             `json:"interrupted,omitempty"` // True if execution was interrupted for HITL
	Checkpoint        *HITLCheckpoint  `json:"checkpoint,omitempty"`  // Checkpoint data if interrupted (includes completed_steps, step_results)

	// Multi-phase execution data
	PhasePlans     []*RoutingPlan `json:"phase_plans,omitempty"`
	PhaseCount     int            `json:"phase_count,omitempty"`
	ForcedTerminal bool           `json:"forced_terminal,omitempty"`

	// Computed DAG structure
	DAG *DAGResponse `json:"dag,omitempty"`

	// LLM interactions (from LLM Debug store)
	LLMInteractions []orchestration.LLMInteraction `json:"llm_interactions,omitempty"`
	LLMDebugSummary *LLMDebugSummary               `json:"llm_debug_summary,omitempty"`

	// HITL checkpoints (if any)
	HITLCheckpoints []HITLCheckpoint `json:"hitl_checkpoints,omitempty"`

	// Metadata for UI
	HasLLMData  bool `json:"has_llm_data"`
	HasHITLData bool `json:"has_hitl_data"`

	// ResumeSiblingRequestID is populated when this record is interrupted AND
	// a completed sibling execution exists under the same original_request_id
	// (written by the HITL resume path). The UI uses this to surface a banner
	// linking to the completed resume. Empty when no sibling exists or when
	// this record is not interrupted. (ORCH-022 Layer 4)
	ResumeSiblingRequestID string `json:"resume_sibling_request_id,omitempty"`
}

// LLMDebugSummary provides a summary of LLM interactions
type LLMDebugSummary struct {
	TotalCalls        int            `json:"total_calls"`
	TotalTokensIn     int            `json:"total_tokens_in"`
	TotalTokensOut    int            `json:"total_tokens_out"`
	TotalDurationMs   int64          `json:"total_duration_ms"`
	ProviderBreakdown map[string]int `json:"provider_breakdown"` // provider -> call count
}

// LLM-debug dedupe is delegated to orchestration.DedupeLLMInteractions
// (see orchestration/llm_debug_dedupe.go). Callers: handleExecution above
// and handleLLMDebugRecord below.

var (
	useMock   bool
	redisURL  string
	namespace string
	port      int
)

func init() {
	flag.BoolVar(&useMock, "mock", true, "Use mock data instead of Redis")
	flag.StringVar(&redisURL, "redis-url", "", "Redis/Valkey URL (required when -mock=false, or set REDIS_URL env var)")
	flag.StringVar(&namespace, "namespace", "truvag3", "Redis key namespace")
	flag.IntVar(&port, "port", 8100, "HTTP server port")
}

// getEnvOrDefault returns environment variable value or default
func getEnvOrDefault(key, defaultVal string) string {
	if val := os.Getenv(key); val != "" {
		return val
	}
	return defaultVal
}

// getEnvBool returns environment variable as bool
func getEnvBool(key string, defaultVal bool) bool {
	val := os.Getenv(key)
	if val == "" {
		return defaultVal
	}
	// Accept various truthy/falsy values
	switch strings.ToLower(val) {
	case "true", "1", "yes", "on":
		return true
	case "false", "0", "no", "off":
		return false
	}
	return defaultVal
}

// getEnvInt returns environment variable as int
func getEnvInt(key string, defaultVal int) int {
	val := os.Getenv(key)
	if val == "" {
		return defaultVal
	}
	if i, err := strconv.Atoi(val); err == nil {
		return i
	}
	return defaultVal
}

func main() {
	flag.Parse()

	// Environment variables override command-line flags
	// This allows K8s ConfigMaps/Secrets to configure the app
	if envRedisURL := os.Getenv("REDIS_URL"); envRedisURL != "" {
		redisURL = envRedisURL
	}
	if envNamespace := os.Getenv("REDIS_NAMESPACE"); envNamespace != "" {
		namespace = envNamespace
	}
	if envPort := getEnvInt("PORT", 0); envPort != 0 {
		port = envPort
	}
	// USE_MOCK env var: "false" or "0" disables mock mode
	mockEnv := os.Getenv("USE_MOCK")
	if mockEnv != "" {
		useMock = getEnvBool("USE_MOCK", useMock)
	}

	// Auto-disable mock when REDIS_URL is set and the operator hasn't asked
	// for mock explicitly. The flag's default is true so a bare `go run`
	// still works for UI-only exploration, but as soon as Redis is wired up
	// the viewer should show live data by default. Honor explicit -mock=true
	// (e.g., from the Dockerfile CMD) and USE_MOCK=true env.
	mockFlagExplicit := false
	flag.Visit(func(f *flag.Flag) {
		if f.Name == "mock" {
			mockFlagExplicit = true
		}
	})
	if useMock && redisURL != "" && !mockFlagExplicit && mockEnv == "" {
		useMock = false
		log.Printf("REDIS_URL is set and -mock/-USE_MOCK not explicitly provided — auto-disabling mock mode")
	}

	// Validate Redis URL is provided when not in mock mode
	if !useMock && redisURL == "" {
		log.Fatalf("REDIS_URL environment variable or -redis-url flag is required when not using mock mode")
	}

	mux := http.NewServeMux()

	// API endpoints — all wrapped with apiMiddleware (CORS + gzip + ETag)
	mux.HandleFunc("/api/services", apiMiddleware(handleServices))
	// /swagger-urls.json is the config feed for Swagger UI auto-discovery.
	// Swagger UI loads this at page start and uses it to populate its service dropdown,
	// so new tools/agents appear without editing any static config.
	mux.HandleFunc("/swagger-urls.json", apiMiddleware(handleSwaggerURLs))
	mux.HandleFunc("/api/health", apiMiddleware(handleHealth))
	mux.HandleFunc("/api/llm-debug", apiMiddleware(handleLLMDebugList))
	mux.HandleFunc("/api/llm-debug/", apiMiddleware(handleLLMDebugRecord))
	mux.HandleFunc("/api/hitl/checkpoints", apiMiddleware(handleHITLCheckpointList))
	mux.HandleFunc("/api/hitl/checkpoints/", apiMiddleware(handleHITLCheckpoint))
	mux.HandleFunc("/api/hitl/command", apiMiddleware(handleHITLCommand))
	mux.HandleFunc("/api/hitl/resume/", apiMiddleware(handleHITLResume))
	mux.HandleFunc("/api/executions", apiMiddleware(handleExecutionList))
	mux.HandleFunc("/api/executions/search", apiMiddleware(handleExecutionSearch))
	mux.HandleFunc("/api/executions/", apiMiddleware(handleExecution)) // Handles both /{id} and /{id}/dag
	mux.HandleFunc("/api/analytics/resolution", apiMiddleware(handleResolutionAnalytics))

	// Memory endpoints — all wrapped with apiMiddleware (CORS + gzip + ETag)
	mux.HandleFunc("/api/memory/domains", apiMiddleware(handleMemoryDomains))
	mux.HandleFunc("/api/memory/events", apiMiddleware(handleMemoryEvents))
	mux.HandleFunc("/api/memory/events/recent", apiMiddleware(handleMemoryEventsRecent))
	mux.HandleFunc("/api/memory/investigations", apiMiddleware(handleMemoryInvestigations))
	mux.HandleFunc("/api/memory/digest", apiMiddleware(handleMemoryDigest))
	mux.HandleFunc("/api/memory/activities", apiMiddleware(handleMemoryActivities))

	// Configure memory domains if requested. Actual client init is lazy
	// (see getMemoryDomain) so a Redis outage at startup recovers
	// automatically once Redis is reachable, without needing a pod restart.
	if domainEnv := os.Getenv("TRUVAG3_VIEWER_MEMORY_DOMAINS"); domainEnv != "" && !useMock {
		var domains []string
		for _, d := range strings.Split(domainEnv, ",") {
			d = strings.TrimSpace(d)
			if d != "" {
				domains = append(domains, d)
			}
		}
		if len(domains) > 0 {
			memoryDomainsList = domains
			memoryEnabled = true
			// Eagerly try init; on failure we stay enabled and retry on
			// first memory endpoint request. The factory's Ping gate
			// preserves the pre-refactor fail-fast-at-startup behavior:
			// if Redis is unreachable, every per-domain factory call
			// returns an error, initMemoryClients populates no domains,
			// and the lazy-retry path in getMemoryDomain takes over.
			initMemoryClients(getMemoryBackendFactory(), domains)
		}
	}

	// Static files - use fs.Sub to strip "static/" prefix from embedded FS
	staticContent, err := fs.Sub(staticFiles, "static")
	if err != nil {
		log.Fatalf("Failed to create static file server: %v", err)
	}
	mux.Handle("/", http.FileServer(http.FS(staticContent)))

	addr := fmt.Sprintf(":%d", port)
	log.Printf("Starting Registry Viewer on http://localhost%s", addr)
	log.Printf("Mode: %s", map[bool]string{true: "MOCK", false: "REDIS"}[useMock])
	if !useMock {
		log.Printf("Redis URL: %s", redisURL)
		log.Printf("Redis Namespace: %s", namespace)
	}

	if err := http.ListenAndServe(addr, mux); err != nil {
		log.Fatalf("Server failed: %v", err)
	}
}

// ─── HTTP Middleware ─────────────────────────────────────────────────────────

// withCORS sets CORS headers and handles OPTIONS preflight.
// Replaces 18 per-handler Access-Control-Allow-Origin assignments.
func withCORS(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next(w, r)
	}
}

// responseRecorder captures the response body and status code so middleware
// (ETag, gzip) can inspect/transform the response after the handler writes it.
type responseRecorder struct {
	http.ResponseWriter
	body       *bytes.Buffer
	statusCode int
	written    bool
}

func (r *responseRecorder) WriteHeader(code int) {
	r.statusCode = code
	r.written = true
}

func (r *responseRecorder) Write(b []byte) (int, error) {
	if !r.written {
		r.statusCode = http.StatusOK
		r.written = true
	}
	return r.body.Write(b)
}

// withETag computes an ETag from the response body and returns 304 Not Modified
// when the client sends a matching If-None-Match header. Applied to polled list
// endpoints so the 5s auto-refresh becomes near-zero-cost when data is unchanged.
func withETag(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		rec := &responseRecorder{ResponseWriter: w, body: &bytes.Buffer{}}
		next(rec, r)

		body := rec.body.Bytes()

		// Compute ETag from FNV-1a hash of the response body (fast, stdlib)
		h := fnv.New64a()
		h.Write(body)
		etag := fmt.Sprintf(`"%x"`, h.Sum64())

		// If client has this version, return 304
		if r.Header.Get("If-None-Match") == etag {
			w.WriteHeader(http.StatusNotModified)
			return
		}

		// Headers set by the handler are already on the underlying writer
		// (responseRecorder delegates Header() to the embedded ResponseWriter).
		w.Header().Set("ETag", etag)
		w.WriteHeader(rec.statusCode)
		w.Write(body)
	}
}

// gzipResponseWriter wraps an http.ResponseWriter to gzip the output.
type gzipResponseWriter struct {
	http.ResponseWriter
	Writer    io.Writer
	wroteBody bool
}

func (g *gzipResponseWriter) Write(b []byte) (int, error) {
	g.wroteBody = true
	return g.Writer.Write(b)
}

// withGzip compresses API responses with gzip when the client supports it.
// Applied after withETag so the ETag is computed on uncompressed content
// (consistent regardless of Accept-Encoding).
//
// Sets Content-Encoding eagerly (before calling handler) so the header
// is present before WriteHeader flushes them. On 304 (no body written),
// the gzip stream is discarded — the header is harmless since 304 has
// no body for the browser to decompress.
func withGzip(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.Header.Get("Accept-Encoding"), "gzip") {
			next(w, r)
			return
		}
		w.Header().Set("Content-Encoding", "gzip")
		w.Header().Del("Content-Length")
		gz := gzip.NewWriter(w)
		gzw := &gzipResponseWriter{ResponseWriter: w, Writer: gz}
		next(gzw, r)
		if gzw.wroteBody {
			gz.Close()
		} else {
			gz.Reset(io.Discard)
			gz.Close()
		}
	}
}

// apiMiddleware composes CORS + ETag + gzip for API endpoints.
// Ordering: CORS (outermost) → gzip → ETag → handler (innermost).
// ETag is innermost so it captures the uncompressed body for hashing.
// Gzip wraps ETag so the ETag-computed body is compressed for transfer.
func apiMiddleware(handler http.HandlerFunc) http.HandlerFunc {
	return withCORS(withGzip(withETag(handler)))
}

// ─── Handlers ────────────────────────────────────────────────────────────────

func handleHealth(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}

func handleServices(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	var services []ServiceInfo
	var err error

	if useMock {
		services = getMockServices()
	} else {
		services, err = getRedisServices()
		if err != nil {
			http.Error(w, fmt.Sprintf("Redis error: %v", err), http.StatusInternalServerError)
			return
		}
	}

	// Sort by type (agents first), then by name
	sort.Slice(services, func(i, j int) bool {
		if services[i].Type != services[j].Type {
			return services[i].Type == "agent"
		}
		return services[i].Name < services[j].Name
	})

	toolCount := 0
	agentCount := 0
	for _, s := range services {
		if s.Type == "tool" {
			toolCount++
		} else {
			agentCount++
		}
	}

	response := RegistryResponse{
		Services:   nonNilSlice(services),
		TotalCount: len(services),
		ToolCount:  toolCount,
		AgentCount: agentCount,
		Timestamp:  time.Now(),
	}

	json.NewEncoder(w).Encode(response)
}

// handleSwaggerURLs returns a Swagger UI compatible `urls` array for auto-discovery.
// Each tool/agent registered in Redis is exposed as an entry pointing at the
// swagger-ui nginx proxy route /svc/{service-name}/openapi.json. The same
// /svc/{service-name}/<path> prefix proxies "Try it out" requests back to the
// agent (see examples/k8-deployment/swagger-ui.yaml), which is why the nginx
// catch-all and the URL template here have to stay in lockstep.
//
// The K8s service name is extracted from ServiceInfo.Address (the authoritative
// cluster DNS name registered by the framework), not derived from ServiceInfo.Name,
// because some agents register with names that already include a "-service" suffix
// (e.g. "research-agent-telemetry-service").
func handleSwaggerURLs(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	var services []ServiceInfo
	var err error

	if useMock {
		services = getMockServices()
	} else {
		services, err = getRedisServices()
		if err != nil {
			http.Error(w, fmt.Sprintf("Redis error: %v", err), http.StatusInternalServerError)
			return
		}
	}

	// Deduplicate by service name — multiple pods of the same deployment register
	// separate ServiceInfo entries with distinct IDs, but they share a name and
	// a ClusterIP Service endpoint. Swagger UI expects unique names in the dropdown.
	seen := make(map[string]bool, len(services))
	urls := make([]SwaggerURL, 0, len(services))

	for _, svc := range services {
		if svc.Type != "tool" && svc.Type != "agent" {
			continue
		}
		if seen[svc.Name] {
			continue
		}
		// Extract the K8s Service DNS name from Address.
		// Expected format: "<service>.<namespace>.svc.cluster.local" — take the
		// first label. Fall back to the full address if it does not contain a dot.
		svcDNSName := svc.Address
		if idx := strings.Index(svcDNSName, "."); idx > 0 {
			svcDNSName = svcDNSName[:idx]
		}
		if svcDNSName == "" {
			continue
		}
		seen[svc.Name] = true
		urls = append(urls, SwaggerURL{
			Name: svc.Name,
			URL:  fmt.Sprintf("/svc/%s/openapi.json", svcDNSName),
		})
	}

	// Stable order — Swagger UI preserves the array order in its dropdown.
	sort.Slice(urls, func(i, j int) bool { return urls[i].Name < urls[j].Name })

	json.NewEncoder(w).Encode(urls)
}

// getMockServices returns mock service data for development
func getMockServices() []ServiceInfo {
	now := time.Now()
	return []ServiceInfo{
		{
			ID:          "weather-tool-abc123",
			Name:        "weather-tool",
			Type:        "tool",
			Description: "Provides current weather information for any location",
			Address:     "weather-tool-service.truvag3-examples",
			Port:        80,
			Capabilities: []Capability{
				{Name: "get-weather", Description: "Get current weather for a location", Version: "1.0.0"},
				{Name: "get-forecast", Description: "Get weather forecast", Version: "1.0.0"},
			},
			Metadata: map[string]interface{}{
				"provider": "openweathermap",
				"version":  "2.1.0",
			},
			Health:   "healthy",
			LastSeen: now.Add(-5 * time.Second),
		},
		{
			ID:          "geocoding-tool-def456",
			Name:        "geocoding-tool",
			Type:        "tool",
			Description: "Converts addresses to coordinates and vice versa",
			Address:     "geocoding-tool-service.truvag3-examples",
			Port:        80,
			Capabilities: []Capability{
				{Name: "geocode", Description: "Convert address to coordinates", Version: "1.0.0"},
				{Name: "reverse-geocode", Description: "Convert coordinates to address", Version: "1.0.0"},
			},
			Metadata: map[string]interface{}{
				"provider": "nominatim",
				"version":  "1.5.0",
			},
			Health:   "healthy",
			LastSeen: now.Add(-8 * time.Second),
		},
		{
			ID:          "stock-market-tool-ghi789",
			Name:        "stock-market-tool",
			Type:        "tool",
			Description: "Provides stock market data and quotes",
			Address:     "stock-market-tool-service.truvag3-examples",
			Port:        80,
			Capabilities: []Capability{
				{Name: "get-quote", Description: "Get stock quote", Version: "1.0.0"},
				{Name: "get-history", Description: "Get historical prices", Version: "1.0.0"},
			},
			Metadata: map[string]interface{}{
				"provider": "alphavantage",
				"version":  "1.2.0",
			},
			Health:   "healthy",
			LastSeen: now.Add(-12 * time.Second),
		},
		{
			ID:          "news-tool-jkl012",
			Name:        "news-tool",
			Type:        "tool",
			Description: "Fetches latest news articles",
			Address:     "news-tool-service.truvag3-examples",
			Port:        80,
			Capabilities: []Capability{
				{Name: "search-news", Description: "Search for news articles", Version: "1.0.0"},
				{Name: "get-headlines", Description: "Get top headlines", Version: "1.0.0"},
			},
			Metadata: map[string]interface{}{
				"provider": "newsapi",
				"version":  "1.0.0",
			},
			Health:   "unhealthy",
			LastSeen: now.Add(-45 * time.Second),
		},
		{
			ID:          "currency-tool-mno345",
			Name:        "currency-tool",
			Type:        "tool",
			Description: "Currency conversion and exchange rates",
			Address:     "currency-tool-service.truvag3-examples",
			Port:        80,
			Capabilities: []Capability{
				{Name: "convert", Description: "Convert between currencies", Version: "1.0.0"},
				{Name: "get-rates", Description: "Get exchange rates", Version: "1.0.0"},
			},
			Metadata: map[string]interface{}{
				"provider": "exchangerate-api",
				"version":  "1.1.0",
			},
			Health:   "healthy",
			LastSeen: now.Add(-3 * time.Second),
		},
		{
			ID:          "research-agent-pqr678",
			Name:        "research-agent",
			Type:        "agent",
			Description: "AI agent that performs research tasks using available tools",
			Address:     "research-agent-service.truvag3-examples",
			Port:        8090,
			Capabilities: []Capability{
				{Name: "research", Description: "Conduct research on a topic", Version: "1.0.0"},
				{Name: "summarize", Description: "Summarize information", Version: "1.0.0"},
			},
			Metadata: map[string]interface{}{
				"llm_provider": "openai",
				"model":        "gpt-4",
				"version":      "3.0.0",
			},
			Health:   "healthy",
			LastSeen: now.Add(-2 * time.Second),
		},
		{
			ID:          "travel-agent-stu901",
			Name:        "travel-agent",
			Type:        "agent",
			Description: "AI agent that helps plan travel itineraries",
			Address:     "travel-agent-service.truvag3-examples",
			Port:        8090,
			Capabilities: []Capability{
				{Name: "plan-trip", Description: "Plan a travel itinerary", Version: "1.0.0"},
				{Name: "find-flights", Description: "Search for flights", Version: "1.0.0"},
				{Name: "book-hotel", Description: "Find and book hotels", Version: "1.0.0"},
			},
			Metadata: map[string]interface{}{
				"llm_provider": "anthropic",
				"model":        "claude-3",
				"version":      "2.0.0",
			},
			Health:   "healthy",
			LastSeen: now.Add(-7 * time.Second),
		},
	}
}

// hitlHTTPClient is the HTTP client used for fan-out calls to agent /hitl/checkpoints endpoints.
// Uses a 10s timeout to avoid hanging on unresponsive agents.
var hitlHTTPClient = &http.Client{Timeout: 10 * time.Second}

// Discovery client singleton. The viewer talks to core.Discovery so the
// enumeration of registered services is not coupled to a specific backing
// store. Swap the concrete build below to point the viewer at any other
// core.Discovery implementation.
var (
	discoveryClient   core.Discovery
	discoveryClientMu sync.Mutex
)

func getDiscovery() (core.Discovery, error) {
	discoveryClientMu.Lock()
	defer discoveryClientMu.Unlock()

	if discoveryClient == nil {
		d, err := core.NewRedisDiscoveryWithNamespace(redisURL, namespace)
		if err != nil {
			return nil, fmt.Errorf("failed to create discovery client: %w", err)
		}
		discoveryClient = d
	}
	return discoveryClient, nil
}

// servicesCache caches the output of fetchServicesFromRedis with a short TTL
// and dedupes concurrent requests via singleflight. At our polling cadences
// (Registry view 15s, HITL 5s, DAG 30s) a 2s TTL is short enough that data
// never feels stale, yet long enough to collapse the burst of polls from
// multiple operators into a single Redis SCAN. The singleflight group also
// ensures that even a cache miss is only fetched once when multiple goroutines
// race through the fast path simultaneously.
const servicesCacheTTL = 2 * time.Second

type servicesCacheEntry struct {
	services []ServiceInfo
	expires  time.Time
}

var (
	servicesCache   atomic.Pointer[servicesCacheEntry]
	servicesCacheSF singleflight.Group
)

// getRedisServices returns the cached service list or refreshes it from Redis
// if the cached entry has expired. Concurrent callers that observe an expired
// entry deduplicate through singleflight so Redis is hit at most once per TTL.
//
// Returns a shallow copy of the cached outer slice so callers can freely sort
// or filter without mutating the shared cache entry (which would race with
// other concurrent readers). Inner references — Capabilities, Metadata — are
// still shared and must not be mutated by callers.
func getRedisServices() ([]ServiceInfo, error) {
	// Fast path — cache hit.
	if entry := servicesCache.Load(); entry != nil && time.Now().Before(entry.expires) {
		return cloneServicesSlice(entry.services), nil
	}

	// Slow path — one caller fetches, others wait for the same result.
	v, err, _ := servicesCacheSF.Do("services", func() (interface{}, error) {
		// Re-check inside the singleflight to avoid a redundant fetch when
		// another goroutine already populated the cache while we waited.
		if entry := servicesCache.Load(); entry != nil && time.Now().Before(entry.expires) {
			return entry.services, nil
		}
		services, err := fetchServicesFromRedis()
		if err != nil {
			return nil, err
		}
		servicesCache.Store(&servicesCacheEntry{
			services: services,
			expires:  time.Now().Add(servicesCacheTTL),
		})
		return services, nil
	})
	if err != nil {
		return nil, err
	}
	return cloneServicesSlice(v.([]ServiceInfo)), nil
}

// cloneServicesSlice returns a shallow copy of the outer slice. Inner
// references (Capabilities, Metadata) are not deep-copied — they're treated
// as read-only by all current callers.
//
// Always returns a non-nil slice so any downstream JSON encoding path
// (handler forgets to wrap with nonNilSlice, or a future handler is
// added) emits `[]` for the empty case instead of `null`. This is
// defense-in-depth with the nonNilSlice helper — both can forget without
// consequence; it takes both failing to produce the UI-breaking `null`
// wire value.
func cloneServicesSlice(src []ServiceInfo) []ServiceInfo {
	if len(src) == 0 {
		return []ServiceInfo{}
	}
	dst := make([]ServiceInfo, len(src))
	copy(dst, src)
	return dst
}

// fetchServicesFromRedis is the uncached implementation. Use getRedisServices
// in handlers — it adds the TTL cache + singleflight in front of this call.
//
// Enumeration is delegated to core.Discovery so the viewer is agnostic to the
// backing store. An empty DiscoveryFilter returns all registered services.
// Name retained for call-site compatibility; it no longer talks to Redis
// directly.
func fetchServicesFromRedis() ([]ServiceInfo, error) {
	discovery, err := getDiscovery()
	if err != nil {
		return nil, err
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	coreServices, err := discovery.Discover(ctx, core.DiscoveryFilter{})
	if err != nil {
		return nil, fmt.Errorf("discovery failed: %w", err)
	}

	services := make([]ServiceInfo, 0, len(coreServices))
	for _, cs := range coreServices {
		if cs == nil {
			continue
		}
		services = append(services, ServiceInfo{
			ID:           cs.ID,
			Name:         cs.Name,
			Type:         string(cs.Type),
			Description:  cs.Description,
			Address:      cs.Address,
			Port:         cs.Port,
			Capabilities: convertCapabilities(cs.Capabilities),
			Metadata:     cs.Metadata,
			Health:       string(cs.Health),
			LastSeen:     cs.LastSeen,
		})
	}
	return services, nil
}

// convertCapabilities projects []core.Capability onto the viewer's local
// []Capability shape. The two types overlap but are not identical — core-only
// fields (Type, Handler, OutputSummary, SchemaEndpoint) are dropped because
// the viewer does not surface them; viewer's Version has no counterpart in
// core.Capability and is left empty (omitempty in the JSON response).
func convertCapabilities(src []core.Capability) []Capability {
	if len(src) == 0 {
		return nil
	}
	dst := make([]Capability, len(src))
	for i, c := range src {
		dst[i] = Capability{
			Name:         c.Name,
			Description:  c.Description,
			Endpoint:     c.Endpoint,
			InputTypes:   c.InputTypes,
			OutputTypes:  c.OutputTypes,
			InputSummary: adaptInputSummary(c.InputSummary),
			Internal:     c.Internal,
		}
	}
	return dst
}

func adaptInputSummary(src *core.SchemaSummary) *InputSummary {
	if src == nil {
		return nil
	}
	return &InputSummary{
		Required: adaptFieldHints(src.RequiredFields),
		Optional: adaptFieldHints(src.OptionalFields),
	}
}

func adaptFieldHints(src []core.FieldHint) []ParamInfo {
	if len(src) == 0 {
		return nil
	}
	out := make([]ParamInfo, len(src))
	for i, h := range src {
		out[i] = ParamInfo{
			Name:        h.Name,
			Type:        h.Type,
			Description: h.Description,
			Example:     h.Example,
		}
	}
	return out
}

// ============================================================================
// LLM Debug Redis Client and Handlers
// ============================================================================

// LLM Debug Store singleton — uses framework's RedisLLMDebugStore.
// This ensures the viewer uses the same storage format and read logic as the orchestrator.
//
// Uses the same lazy-init-with-retry pattern as getRedisClient: if
// construction fails (Redis unreachable at startup), the next call retries
// instead of latching permanently via sync.Once. Once constructed, the
// underlying redis client inside the store handles reconnects transparently
// through go-redis's pool, so no per-call health check is needed here.
var (
	llmDebugStore   orchestration.LLMDebugStore
	llmDebugStoreMu sync.Mutex
)

func getLLMDebugStore() (orchestration.LLMDebugStore, error) {
	llmDebugStoreMu.Lock()
	defer llmDebugStoreMu.Unlock()

	if llmDebugStore == nil {
		store, err := orchestration.NewRedisLLMDebugStore(
			orchestration.WithDebugRedisURL(redisURL),
		)
		if err != nil {
			return nil, fmt.Errorf("failed to create LLM debug store: %w", err)
		}
		llmDebugStore = store
	}
	return llmDebugStore, nil
}

// Execution Debug Redis client singleton (uses DB 8).
// Same lazy-reconnect pattern as getRedisClient — see that function's
// docstring for rationale.
var (
	executionDebugClient   *redis.Client
	executionDebugClientMu sync.Mutex
)

func getExecutionDebugClient() (*redis.Client, error) {
	executionDebugClientMu.Lock()
	defer executionDebugClientMu.Unlock()

	if executionDebugClient == nil {
		opt, err := redis.ParseURL(redisURL)
		if err != nil {
			return nil, fmt.Errorf("invalid redis URL: %w", err)
		}
		opt.DB = core.RedisDBExecutionDebug // Use DB 8 for Execution Debug
		executionDebugClient = redis.NewClient(opt)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := executionDebugClient.Ping(ctx).Err(); err != nil {
		return nil, fmt.Errorf("redis connection failed (DB %d): %w", core.RedisDBExecutionDebug, err)
	}
	return executionDebugClient, nil
}

// handleLLMDebugList returns a list of recent LLM debug records
func handleLLMDebugList(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	// Parse limit parameter (default 50)
	limit := 50
	if limitStr := r.URL.Query().Get("limit"); limitStr != "" {
		if l, err := strconv.Atoi(limitStr); err == nil && l > 0 && l <= 100 {
			limit = l
		}
	}

	var records []orchestration.LLMDebugRecordSummary
	var err error

	if useMock {
		records = getMockLLMDebugSummaries()
	} else {
		store, storeErr := getLLMDebugStore()
		if storeErr != nil {
			http.Error(w, "LLM debug store unavailable", http.StatusServiceUnavailable)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
		defer cancel()
		records, err = store.ListRecent(ctx, limit)
		if err != nil {
			http.Error(w, fmt.Sprintf("Redis error: %v", err), http.StatusInternalServerError)
			return
		}
	}

	response := LLMDebugListResponse{
		Records:   nonNilSlice(records),
		Total:     len(records),
		Timestamp: time.Now(),
	}

	json.NewEncoder(w).Encode(response)
}

// handleLLMDebugRecord returns a specific LLM debug record by ID
func handleLLMDebugRecord(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	// Extract request ID from URL path: /api/llm-debug/{id}
	path := strings.TrimPrefix(r.URL.Path, "/api/llm-debug/")
	requestID := strings.TrimSpace(path)

	if requestID == "" {
		http.Error(w, "request_id is required", http.StatusBadRequest)
		return
	}

	var record *orchestration.LLMDebugRecord
	var err error

	if useMock {
		record = getMockLLMDebugRecord(requestID)
		if record == nil {
			http.Error(w, fmt.Sprintf("record not found: %s", requestID), http.StatusNotFound)
			return
		}
	} else {
		store, storeErr := getLLMDebugStore()
		if storeErr != nil {
			http.Error(w, "LLM debug store unavailable", http.StatusServiceUnavailable)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
		defer cancel()
		record, err = store.GetRecord(ctx, requestID)
		if err != nil {
			if strings.Contains(err.Error(), "not found") {
				http.Error(w, fmt.Sprintf("record not found: %s", requestID), http.StatusNotFound)
			} else {
				http.Error(w, fmt.Sprintf("Redis error: %v", err), http.StatusInternalServerError)
			}
			return
		}
	}

	// Apply Layer 1 dedupe so historical records (written before the
	// framework-side Layer 2 landed) render correctly in the standalone
	// LLM Debug view — same invariant as handleExecution above.
	// See orchestration/bugs/BUG_LLM_INTERACTION_DOUBLE_RECORDING.md.
	if record != nil {
		record.Interactions = orchestration.DedupeLLMInteractions(record.Interactions)
	}

	json.NewEncoder(w).Encode(record)
}

// ============================================================================
// LLM Debug Mock Data
// ============================================================================

// getMockLLMDebugSummaries returns mock summaries for development
func getMockLLMDebugSummaries() []orchestration.LLMDebugRecordSummary {
	now := time.Now()
	return []orchestration.LLMDebugRecordSummary{
		{
			RequestID:         "req-abc123",
			OriginalRequestID: "req-abc123", // Same as RequestID for initial request
			TraceID:           "369fecb4e3156c34e0950c61f1f99d62",
			CreatedAt:         now.Add(-5 * time.Minute),
			InteractionCount:  3,
			TotalTokens:       2847,
			HasErrors:         false,
		},
		{
			RequestID:         "req-def456",
			OriginalRequestID: "req-abc123", // Part of same HITL conversation (resume)
			TraceID:           "7a8b9c0d1e2f3a4b5c6d7e8f9a0b1c2d",
			CreatedAt:         now.Add(-15 * time.Minute),
			InteractionCount:  2,
			TotalTokens:       1523,
			HasErrors:         false,
			SourceComponents:  []string{"research-assistant"},
		},
		{
			RequestID:         "req-ghi789",
			OriginalRequestID: "req-ghi789", // Different conversation
			TraceID:           "1234567890abcdef1234567890abcdef",
			CreatedAt:         now.Add(-1 * time.Hour),
			InteractionCount:  4,
			TotalTokens:       4102,
			HasErrors:         true,
			SourceComponents:  []string{"travel-agent", "research-assistant"},
		},
	}
}

// ============================================================================
// HITL Checkpoint Handlers (HTTP fan-out to agents — no direct Redis access)
// ============================================================================

// handleHITLCheckpointList returns a list of pending HITL checkpoints
func handleHITLCheckpointList(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	var checkpoints []HITLCheckpointSummary
	var err error

	if useMock {
		checkpoints = getMockHITLCheckpointSummaries()
	} else {
		checkpoints, err = getAgentHITLCheckpointSummaries(r.Context())
		if err != nil {
			http.Error(w, fmt.Sprintf("error fetching checkpoints: %v", err), http.StatusInternalServerError)
			return
		}
	}

	if checkpoints == nil {
		checkpoints = []HITLCheckpointSummary{}
	}
	response := HITLCheckpointListResponse{
		Checkpoints: checkpoints,
		Total:       len(checkpoints),
		Timestamp:   time.Now(),
	}

	json.NewEncoder(w).Encode(response)
}

// handleHITLCheckpoint returns a specific HITL checkpoint by ID
func handleHITLCheckpoint(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	// Extract checkpoint ID from URL path: /api/hitl/checkpoints/{id}
	path := strings.TrimPrefix(r.URL.Path, "/api/hitl/checkpoints/")
	checkpointID := strings.TrimSpace(path)

	if checkpointID == "" {
		http.Error(w, "checkpoint_id is required", http.StatusBadRequest)
		return
	}

	var checkpoint *HITLCheckpoint
	var err error

	if useMock {
		checkpoint = getMockHITLCheckpoint(checkpointID)
		if checkpoint == nil {
			http.Error(w, fmt.Sprintf("checkpoint not found: %s", checkpointID), http.StatusNotFound)
			return
		}
	} else {
		checkpoint, err = getAgentHITLCheckpoint(r.Context(), checkpointID)
		if err != nil {
			if strings.Contains(err.Error(), "not found") {
				http.Error(w, fmt.Sprintf("checkpoint not found: %s", checkpointID), http.StatusNotFound)
			} else {
				http.Error(w, fmt.Sprintf("error fetching checkpoint: %v", err), http.StatusInternalServerError)
			}
			return
		}
	}

	json.NewEncoder(w).Encode(checkpoint)
}

// handleHITLCommand proxies an approve/reject command to the owning agent.
//
// Flow:
//  1. Parse the command request from the registry viewer UI
//  2. Load the checkpoint from Redis to get the agent name
//  3. Look up the agent's address from the service discovery registry
//  4. Forward the command to the agent's /hitl/command endpoint
//  5. Return the ResumeResult (includes should_resume flag)
func handleHITLCommand(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	if r.Method != http.MethodPost {
		http.Error(w, `{"error":"method not allowed, use POST"}`, http.StatusMethodNotAllowed)
		return
	}

	var req struct {
		CheckpointID string `json:"checkpoint_id"`
		CommandType  string `json:"command_type"` // "approve" or "reject"
		Feedback     string `json:"feedback,omitempty"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, fmt.Sprintf(`{"error":"invalid JSON: %s"}`, err.Error()), http.StatusBadRequest)
		return
	}
	if req.CheckpointID == "" || req.CommandType == "" {
		http.Error(w, `{"error":"checkpoint_id and command_type are required"}`, http.StatusBadRequest)
		return
	}

	// Load checkpoint via agent HTTP interface to determine which agent owns it.
	checkpoint, err := getAgentHITLCheckpoint(r.Context(), req.CheckpointID)
	if err != nil {
		if strings.Contains(err.Error(), "not found") {
			http.Error(w, fmt.Sprintf(`{"error":"checkpoint not found: %s"}`, req.CheckpointID), http.StatusNotFound)
		} else {
			http.Error(w, fmt.Sprintf(`{"error":"failed to load checkpoint: %s"}`, err.Error()), http.StatusInternalServerError)
		}
		return
	}

	// Use AgentAddress from checkpoint directly (populated by framework at creation time).
	// Fall back to service registry lookup for old checkpoints that predate RC3-Backend.
	agentAddr := checkpoint.AgentAddress
	if agentAddr == "" {
		if checkpoint.AgentName == "" {
			http.Error(w, `{"error":"cannot route command: checkpoint has no agent_address or agent_name"}`, http.StatusBadRequest)
			return
		}
		agentAddr, err = getAgentAddress(checkpoint.AgentName)
		if err != nil {
			http.Error(w, fmt.Sprintf(`{"error":"%s"}`, err.Error()), http.StatusBadGateway)
			return
		}
	}

	// Build the command payload using the field names expected by orchestration.Command
	agentCmd := map[string]interface{}{
		"checkpoint_id": req.CheckpointID,
		"type":          req.CommandType,
		"feedback":      req.Feedback,
	}
	body, _ := json.Marshal(agentCmd)

	resp, err := http.Post(agentAddr+"/hitl/command", "application/json", bytes.NewReader(body))
	if err != nil {
		http.Error(w, fmt.Sprintf(`{"error":"failed to reach agent %q: %s"}`, checkpoint.AgentName, err.Error()), http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()

	w.WriteHeader(resp.StatusCode)
	io.Copy(w, resp.Body)
}

// handleHITLResume proxies a resume request to the owning agent after approval.
//
// The registry viewer calls this after handleHITLCommand returns should_resume:true.
// Path: POST /api/hitl/resume/{checkpoint_id}
func handleHITLResume(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	if r.Method != http.MethodPost {
		http.Error(w, `{"error":"method not allowed, use POST"}`, http.StatusMethodNotAllowed)
		return
	}

	checkpointID := strings.TrimPrefix(r.URL.Path, "/api/hitl/resume/")
	if checkpointID == "" {
		http.Error(w, `{"error":"checkpoint_id is required in path"}`, http.StatusBadRequest)
		return
	}

	checkpoint, err := getAgentHITLCheckpoint(r.Context(), checkpointID)
	if err != nil {
		if strings.Contains(err.Error(), "not found") {
			http.Error(w, fmt.Sprintf(`{"error":"checkpoint not found: %s"}`, checkpointID), http.StatusNotFound)
		} else {
			http.Error(w, fmt.Sprintf(`{"error":"failed to load checkpoint: %s"}`, err.Error()), http.StatusInternalServerError)
		}
		return
	}

	// Use AgentAddress from checkpoint directly; fall back to service registry for old checkpoints.
	agentAddr := checkpoint.AgentAddress
	if agentAddr == "" {
		if checkpoint.AgentName == "" {
			http.Error(w, `{"error":"cannot route resume: checkpoint has no agent_address or agent_name"}`, http.StatusBadRequest)
			return
		}
		var lookupErr error
		agentAddr, lookupErr = getAgentAddress(checkpoint.AgentName)
		if lookupErr != nil {
			http.Error(w, fmt.Sprintf(`{"error":"%s"}`, lookupErr.Error()), http.StatusBadGateway)
			return
		}
	}

	resp, err := http.Post(agentAddr+"/hitl/resume/"+url.PathEscape(checkpointID), "application/json", http.NoBody)
	if err != nil {
		http.Error(w, fmt.Sprintf(`{"error":"failed to reach agent: %s"}`, err.Error()), http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()

	w.WriteHeader(resp.StatusCode)
	io.Copy(w, resp.Body)
}

// getAgentAddress looks up an agent's HTTP address from the service discovery registry.
func getAgentAddress(agentName string) (string, error) {
	services, err := getRedisServices()
	if err != nil {
		return "", fmt.Errorf("failed to query service registry: %w", err)
	}
	for _, svc := range services {
		if svc.Name == agentName {
			if svc.Address == "" || svc.Port == 0 {
				return "", fmt.Errorf("agent %q found in registry but has no address/port", agentName)
			}
			return fmt.Sprintf("http://%s:%d", svc.Address, svc.Port), nil
		}
	}
	return "", fmt.Errorf("agent %q not found in service registry — it may be offline or not yet registered", agentName)
}

// ============================================================================
// Execution DAG Handlers
// ============================================================================

// handleExecutionList returns a list of recent executions
func handleExecutionList(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	// Parse query parameters
	limitStr := r.URL.Query().Get("limit")
	limit := 50
	if limitStr != "" {
		if l, err := strconv.Atoi(limitStr); err == nil && l > 0 && l <= 200 {
			limit = l
		}
	}
	cursor := r.URL.Query().Get("cursor")

	var response ExecutionListResponse

	if useMock {
		summaries := getMockExecutionSummaries()
		response = ExecutionListResponse{
			Executions: nonNilSlice(summaries),
			Total:      len(summaries),
			HasMore:    false,
			Timestamp:  time.Now(),
		}
	} else {
		page, err := getRedisExecutionSummaries(limit, cursor)
		if err != nil {
			http.Error(w, fmt.Sprintf("Redis error: %v", err), http.StatusInternalServerError)
			return
		}
		response = ExecutionListResponse{
			Executions: nonNilSlice(page.Summaries),
			Total:      len(page.Summaries),
			HasMore:    page.HasMore,
			NextCursor: page.NextCursor,
			Timestamp:  time.Now(),
		}
	}

	json.NewEncoder(w).Encode(response)
}

// ExecutionSearchResponse is the API response for search results
type ExecutionSearchResponse struct {
	Executions []ExecutionSummary `json:"executions"`
	Query      string             `json:"query"`
	Total      int                `json:"total"`
	Timestamp  time.Time          `json:"timestamp"`
}

// handleExecutionSearch searches executions by original request content
func handleExecutionSearch(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	// Parse query parameters
	query := r.URL.Query().Get("q")
	if query == "" {
		http.Error(w, "query parameter 'q' is required", http.StatusBadRequest)
		return
	}

	limitStr := r.URL.Query().Get("limit")
	limit := 50
	if limitStr != "" {
		if l, err := strconv.Atoi(limitStr); err == nil && l > 0 && l <= 100 {
			limit = l
		}
	}

	var summaries []ExecutionSummary
	var err error

	if useMock {
		summaries = searchMockExecutions(query, limit)
	} else {
		summaries, err = searchRedisExecutions(query, limit)
		if err != nil {
			http.Error(w, fmt.Sprintf("Redis error: %v", err), http.StatusInternalServerError)
			return
		}
	}

	response := ExecutionSearchResponse{
		Executions: nonNilSlice(summaries),
		Query:      query,
		Total:      len(summaries),
		Timestamp:  time.Now(),
	}

	json.NewEncoder(w).Encode(response)
}

// searchMockExecutions searches mock executions by original request content
func searchMockExecutions(query string, limit int) []ExecutionSummary {
	allSummaries := getMockExecutionSummaries()
	queryLower := strings.ToLower(query)

	var results []ExecutionSummary
	for _, summary := range allSummaries {
		if strings.Contains(strings.ToLower(summary.OriginalRequest), queryLower) {
			results = append(results, summary)
			if len(results) >= limit {
				break
			}
		}
	}
	return results
}

// searchRedisExecutions searches Redis executions by original request content.
// Note: For production, consider using Redis Search or a dedicated search index.
func searchRedisExecutions(query string, limit int) ([]ExecutionSummary, error) {
	page, err := getRedisExecutionSummaries(1000, "") // Fetch more to search through
	if err != nil {
		return nil, err
	}

	queryLower := strings.ToLower(query)
	var results []ExecutionSummary
	for _, summary := range page.Summaries {
		if strings.Contains(strings.ToLower(summary.OriginalRequest), queryLower) {
			results = append(results, summary)
			if len(results) >= limit {
				break
			}
		}
	}
	return results, nil
}

// handleExecution handles GET /api/executions/{id}, /{id}/dag, and /{id}/unified
func handleExecution(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	// Parse URL path: /api/executions/{id}, /{id}/dag, or /{id}/unified
	path := strings.TrimPrefix(r.URL.Path, "/api/executions/")
	parts := strings.Split(path, "/")

	if len(parts) == 0 || parts[0] == "" {
		http.Error(w, "request_id is required", http.StatusBadRequest)
		return
	}

	requestID := parts[0]
	isDAGRequest := len(parts) > 1 && parts[1] == "dag"
	isUnifiedRequest := len(parts) > 1 && parts[1] == "unified"

	var execution *StoredExecution
	var err error

	if useMock {
		execution = getMockExecution(requestID)
		if execution == nil {
			http.Error(w, fmt.Sprintf("execution not found: %s", requestID), http.StatusNotFound)
			return
		}
	} else {
		execution, err = getRedisExecution(requestID)
		if err != nil {
			if strings.Contains(err.Error(), "not found") {
				http.Error(w, fmt.Sprintf("execution not found: %s", requestID), http.StatusNotFound)
			} else {
				http.Error(w, fmt.Sprintf("Redis error: %v", err), http.StatusInternalServerError)
			}
			return
		}
	}

	if isUnifiedRequest {
		// Return unified view combining execution, LLM debug, and HITL data
		unified := buildUnifiedView(execution)
		json.NewEncoder(w).Encode(unified)
	} else if isDAGRequest {
		// Return computed DAG structure
		dag := computeDAG(execution)
		json.NewEncoder(w).Encode(dag)
	} else {
		// Return full execution record
		json.NewEncoder(w).Encode(execution)
	}
}

// normalizeSteps produces a unified step list, a step_id → result map, and
// optional phase-boundary index markers for a stored execution. It merges
// fields across Plan / PhasePlans / Result / Checkpoint so callers see a
// single normalized shape regardless of which storage-era or record-type
// produced the execution.
//
// After the orchestrator's ORCH-022 Layer 1 fix, PhasePlans is authoritative
// for multi-phase records and Result.Steps covers the runtime status. This
// helper also folds Checkpoint.StepResults into the step list as a back-compat
// fallback for records persisted BEFORE the orchestrator fix landed — no new
// records need this path.
//
// Field-level merge:
//   - steps are populated from plan side (authoritative for definition fields:
//     instruction, capability, depends_on, parameters).
//   - results are populated from result/checkpoint side (authoritative for
//     runtime fields: success, duration, end_time, response).
//
// Two slots, not one — avoiding the "pick one source per step" model that
// loses information.
//
// phaseBreakpoints is non-nil only when PhasePlans has > 1 entry (multi-phase
// records). Legacy Checkpoint-synthesized nodes do not contribute phase
// boundaries — that information is unrecoverable from StepResult alone.
func normalizeSteps(execution *StoredExecution) (steps []RoutingStep, results map[string]*StepResult, phaseBreakpoints []int) {
	results = make(map[string]*StepResult)
	if execution == nil {
		return nil, results, nil
	}

	// 1. Plan-side nodes (authoritative for definition fields).
	if len(execution.PhasePlans) > 1 {
		for _, phasePlan := range execution.PhasePlans {
			if phasePlan == nil {
				continue
			}
			phaseBreakpoints = append(phaseBreakpoints, len(steps))
			steps = append(steps, phasePlan.Steps...)
		}
	} else if execution.Plan != nil {
		steps = append(steps, execution.Plan.Steps...)
	}

	seen := make(map[string]struct{}, len(steps))
	for _, s := range steps {
		seen[s.StepID] = struct{}{}
	}

	// 2. Result-side runtime map (authoritative for runtime fields).
	if execution.Result != nil {
		for i := range execution.Result.Steps {
			sr := &execution.Result.Steps[i]
			results[sr.StepID] = sr
		}
	}

	// 3. Checkpoint-side fallback for legacy interrupted records. Only fires
	//    when a step_id appears in Checkpoint.StepResults but not in the
	//    plan+result view. Synthesized nodes carry Capability/Parameters from
	//    StepResult (the viewer's RoutingStep has these fields; the framework's
	//    does not). DependsOn is unrecoverable from StepResult — leave empty.
	//    Edges for synthesized nodes are best-effort (legacy pre-fix records).
	if execution.Checkpoint != nil {
		for stepID, sr := range execution.Checkpoint.StepResults {
			if _, present := results[stepID]; !present {
				results[stepID] = sr
			}
			if _, present := seen[stepID]; !present && sr != nil {
				steps = append(steps, RoutingStep{
					StepID:      sr.StepID,
					AgentName:   sr.AgentName,
					Namespace:   sr.Namespace,
					Instruction: sr.Instruction,
					Capability:  sr.Capability,
					Parameters:  sr.Parameters,
				})
				seen[stepID] = struct{}{}
			}
		}
	}
	return steps, results, phaseBreakpoints
}

// computeDAG builds the DAG structure from a stored execution
func computeDAG(execution *StoredExecution) *DAGResponse {
	if execution == nil {
		return &DAGResponse{
			Nodes:  []DAGNode{},
			Edges:  []DAGEdge{},
			Levels: [][]string{},
		}
	}

	// ORCH-022: unified step source + result map via normalizeSteps. Handles
	// multi-phase records (via PhasePlans), single-phase records (via Plan),
	// and legacy interrupted records (via Checkpoint.StepResults fallback).
	allPlanSteps, stepResults, phaseBreakpoints := normalizeSteps(execution)

	if len(allPlanSteps) == 0 {
		return &DAGResponse{
			Nodes:  []DAGNode{},
			Edges:  []DAGEdge{},
			Levels: [][]string{},
		}
	}

	// Build adjacency list and in-degree map for topological sort
	inDegree := make(map[string]int)
	dependents := make(map[string][]string)

	for _, step := range allPlanSteps {
		if _, exists := inDegree[step.StepID]; !exists {
			inDegree[step.StepID] = 0
		}
		for _, dep := range step.DependsOn {
			inDegree[step.StepID]++
			dependents[dep] = append(dependents[dep], step.StepID)
		}
	}

	// Compute levels using BFS (Kahn's algorithm)
	levels := [][]string{}
	currentLevel := []string{}

	// Find initial nodes (no dependencies)
	for _, step := range allPlanSteps {
		if inDegree[step.StepID] == 0 {
			currentLevel = append(currentLevel, step.StepID)
		}
	}

	levelMap := make(map[string]int)
	for len(currentLevel) > 0 {
		levels = append(levels, currentLevel)
		levelIdx := len(levels) - 1
		for _, stepID := range currentLevel {
			levelMap[stepID] = levelIdx
		}

		nextLevel := []string{}
		for _, stepID := range currentLevel {
			for _, dep := range dependents[stepID] {
				inDegree[dep]--
				if inDegree[dep] == 0 {
					nextLevel = append(nextLevel, dep)
				}
			}
		}
		currentLevel = nextLevel
	}

	// Build nodes
	nodes := make([]DAGNode, 0, len(allPlanSteps))
	statistics := DAGStatistics{
		TotalNodes: len(allPlanSteps),
		Depth:      len(levels),
	}

	// Determine the current step blocked by HITL (if interrupted)
	var blockedStepID string
	if execution.Interrupted && execution.Checkpoint != nil && execution.Checkpoint.CurrentStep != nil {
		blockedStepID = execution.Checkpoint.CurrentStep.StepID
	}

	for _, step := range allPlanSteps {
		status := "pending"
		var durationMs int64

		if result, ok := stepResults[step.StepID]; ok {
			if result.Success {
				status = "completed"
				statistics.CompletedNodes++
			} else if step.StepID == blockedStepID {
				// Step is blocked by HITL approval, not failed
				status = "blocked"
			} else if result.Error != "" {
				status = "failed"
				statistics.FailedNodes++
			} else if result.Skipped {
				status = "skipped"
				statistics.SkippedNodes++
			}
			durationMs = result.DurationMs
		} else if step.StepID == blockedStepID {
			// Step has no result yet but is the blocked HITL step
			status = "blocked"
		}

		nodes = append(nodes, DAGNode{
			ID:          step.StepID,
			Label:       step.AgentName,
			Instruction: step.Instruction,
			Status:      status,
			DurationMs:  durationMs,
			Level:       levelMap[step.StepID],
		})
	}

	// Build edges
	edges := make([]DAGEdge, 0)
	for _, step := range allPlanSteps {
		for _, dep := range step.DependsOn {
			edges = append(edges, DAGEdge{
				Source: dep,
				Target: step.StepID,
			})
		}
	}

	// For multi-phase: add phase boundary nodes and edges
	if len(phaseBreakpoints) > 1 {
		for p := 0; p < len(phaseBreakpoints)-1; p++ {
			currentPhaseStart := phaseBreakpoints[p]
			nextPhaseStart := phaseBreakpoints[p+1]
			var currentPhaseEnd int
			if p+1 < len(phaseBreakpoints) {
				currentPhaseEnd = phaseBreakpoints[p+1]
			} else {
				currentPhaseEnd = len(allPlanSteps)
			}

			currentPhaseSteps := allPlanSteps[currentPhaseStart:currentPhaseEnd]
			var nextPhaseEnd int
			if p+2 < len(phaseBreakpoints) {
				nextPhaseEnd = phaseBreakpoints[p+2]
			} else {
				nextPhaseEnd = len(allPlanSteps)
			}
			nextPhaseSteps := allPlanSteps[nextPhaseStart:nextPhaseEnd]

			// Find leaf steps of current phase (no other step in this phase depends on them)
			dependedOnInPhase := make(map[string]bool)
			for _, s := range currentPhaseSteps {
				for _, dep := range s.DependsOn {
					dependedOnInPhase[dep] = true
				}
			}
			var leafSteps []string
			for _, s := range currentPhaseSteps {
				if !dependedOnInPhase[s.StepID] {
					leafSteps = append(leafSteps, s.StepID)
				}
			}

			// Find root steps of next phase (no depends_on)
			var rootNextSteps []string
			for _, s := range nextPhaseSteps {
				if len(s.DependsOn) == 0 {
					rootNextSteps = append(rootNextSteps, s.StepID)
				}
			}

			// Create phase boundary node
			boundaryID := fmt.Sprintf("phase_boundary_%d", p+1)
			note := ""
			if p < len(execution.PhasePlans) {
				note = execution.PhasePlans[p].ContinuationNote
			}

			nodes = append(nodes, DAGNode{
				ID:       boundaryID,
				Label:    fmt.Sprintf("Phase %d → %d", p+1, p+2),
				NodeType: "phase_boundary",
				Metadata: map[string]interface{}{
					"continuation_note": note,
					"phase_number":      p + 1,
				},
			})

			// Connect: leaf steps of Phase N → boundary → root steps of Phase N+1
			for _, stepID := range leafSteps {
				edges = append(edges, DAGEdge{
					Source:   stepID,
					Target:   boundaryID,
					EdgeType: "phase_transition",
				})
			}
			for _, stepID := range rootNextSteps {
				edges = append(edges, DAGEdge{
					Source:   boundaryID,
					Target:   stepID,
					EdgeType: "phase_transition",
				})
			}
		}
	}

	// Calculate max parallelism
	for _, level := range levels {
		if len(level) > statistics.MaxParallelism {
			statistics.MaxParallelism = len(level)
		}
	}

	return &DAGResponse{
		Nodes:      nodes,
		Edges:      edges,
		Levels:     levels,
		Statistics: statistics,
	}
}

// handleResolutionAnalytics handles GET /api/analytics/resolution
// Returns resolution layer tracking data for research analytics.
// Supports ?format=json (default) and ?format=csv for export.
// Supports ?limit=N to control how many executions to scan (default 100).
func handleResolutionAnalytics(w http.ResponseWriter, r *http.Request) {
	// Parse query parameters
	format := r.URL.Query().Get("format")
	if format == "" {
		format = "json"
	}
	limitStr := r.URL.Query().Get("limit")
	limit := 100
	if limitStr != "" {
		if l, err := strconv.Atoi(limitStr); err == nil && l > 0 {
			limit = l
		}
	}

	// Fetch execution summaries
	var summaries []ExecutionSummary

	if useMock {
		summaries = getMockExecutionSummaries()
	} else {
		page, err := getRedisExecutionSummaries(limit, "")
		if err != nil {
			http.Error(w, fmt.Sprintf("Redis error: %v", err), http.StatusInternalServerError)
			return
		}
		summaries = page.Summaries
	}

	// Collect resolution records from all executions
	var records []ResolutionAnalyticsRecord
	totalExecutions := 0

	if useMock {
		for _, summary := range summaries {
			execution := getMockExecution(summary.RequestID)
			if execution == nil || execution.Result == nil {
				continue
			}
			totalExecutions++
			records = append(records, extractResolutionRecords(execution)...)
		}
	} else {
		// Batch-fetch all executions via Redis pipeline (same pattern as getRedisExecutionSummaries)
		client, clientErr := getExecutionDebugClient()
		if clientErr != nil {
			http.Error(w, fmt.Sprintf("Redis error: %v", clientErr), http.StatusInternalServerError)
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()

		pipe := client.Pipeline()
		cmds := make([]*redis.StringCmd, len(summaries))
		for i, s := range summaries {
			cmds[i] = pipe.Get(ctx, executionKeyPrefix+s.RequestID)
		}
		if _, pipeErr := pipe.Exec(ctx); pipeErr != nil && pipeErr != redis.Nil {
			if ctx.Err() != nil {
				http.Error(w, fmt.Sprintf("Redis pipeline error: %v", pipeErr), http.StatusInternalServerError)
				return
			}
		}

		for i, summary := range summaries {
			data, err := cmds[i].Bytes()
			if err != nil {
				log.Printf("Warning: analytics skipping execution %s: %v", summary.RequestID, err)
				continue
			}
			execution, err := deserializeExecution(data)
			if err != nil {
				log.Printf("Warning: analytics skipping execution %s: %v", summary.RequestID, err)
				continue
			}
			if execution.Result == nil {
				continue
			}
			totalExecutions++
			records = append(records, extractResolutionRecords(execution)...)
		}
	}

	// Compute summary statistics
	analyticsSummary := computeResolutionSummary(records, totalExecutions)

	if format == "csv" {
		writeResolutionCSV(w, records)
		return
	}

	// Default: JSON response
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(ResolutionAnalyticsResponse{
		Records:    nonNilSlice(records),
		Summary:    analyticsSummary,
		ExportedAt: time.Now(),
	})
}

// extractResolutionRecords extracts per-step resolution data from a stored execution
func extractResolutionRecords(execution *StoredExecution) []ResolutionAnalyticsRecord {
	if execution.Result == nil {
		return nil
	}

	var records []ResolutionAnalyticsRecord
	for _, step := range execution.Result.Steps {
		record := ResolutionAnalyticsRecord{
			RequestID:  execution.RequestID,
			StepID:     step.StepID,
			Capability: step.Capability,
			AgentName:  step.AgentName,
			Success:    step.Success,
		}

		// Extract resolution metadata from step.Metadata["resolution"]
		if step.Metadata != nil {
			if resRaw, ok := step.Metadata["resolution"]; ok {
				if resMap, ok := resRaw.(map[string]interface{}); ok {
					record.AutoWiredCount = intFromMap(resMap, "auto_wired_count")
					record.MicroResolved = intFromMap(resMap, "micro_resolved_count")
					record.SemanticRetry = intFromMap(resMap, "semantic_retry_count")
					record.UserProvided = intFromMap(resMap, "user_provided_count")
					record.AutoWireDurUs = int64FromMap(resMap, "auto_wiring_duration_us")
					record.MicroResolvMs = int64FromMap(resMap, "micro_resolution_duration_ms")
					record.SourceKeyCount = intFromMap(resMap, "source_data_key_count")

					// Count total params from parameters array
					if params, ok := resMap["parameters"].([]interface{}); ok {
						record.TotalParams = len(params)
					}
				}
			}
		}

		// Fall back to counting parameters if no resolution metadata
		if record.TotalParams == 0 && len(step.Parameters) > 0 {
			record.TotalParams = len(step.Parameters)
		}

		records = append(records, record)
	}
	return records
}

// computeResolutionSummary computes aggregate statistics from resolution records
func computeResolutionSummary(records []ResolutionAnalyticsRecord, totalExecutions int) ResolutionAnalyticsSummary {
	summary := ResolutionAnalyticsSummary{
		TotalExecutions: totalExecutions,
		TotalSteps:      len(records),
	}

	var totalAutoWirePercent float64
	var autoWirePercentCount int
	var totalMicroResolveMs int64
	var microResolveCount int

	for _, r := range records {
		totalParams := r.AutoWiredCount + r.MicroResolved + r.SemanticRetry + r.UserProvided
		if totalParams > 0 {
			summary.StepsWithResolution++
			pct := float64(r.AutoWiredCount) * 100.0 / float64(totalParams)
			totalAutoWirePercent += pct
			autoWirePercentCount++
		}
		if r.MicroResolvMs > 0 {
			totalMicroResolveMs += r.MicroResolvMs
			microResolveCount++
		}
	}

	if autoWirePercentCount > 0 {
		summary.AvgAutoWirePercent = totalAutoWirePercent / float64(autoWirePercentCount)
	}
	if microResolveCount > 0 {
		summary.AvgMicroResolveMs = float64(totalMicroResolveMs) / float64(microResolveCount)
	}

	return summary
}

// writeResolutionCSV writes resolution analytics records as CSV
func writeResolutionCSV(w http.ResponseWriter, records []ResolutionAnalyticsRecord) {
	w.Header().Set("Content-Type", "text/csv")
	w.Header().Set("Content-Disposition", "attachment; filename=resolution_analytics.csv")

	writer := csv.NewWriter(w)
	defer writer.Flush()

	// Header row
	writer.Write([]string{
		"request_id", "step_id", "capability", "agent_name", "success",
		"auto_wired_count", "micro_resolved_count", "semantic_retry_count", "user_provided_count",
		"auto_wiring_duration_us", "micro_resolution_duration_ms",
		"source_data_key_count", "total_params",
	})

	for _, r := range records {
		writer.Write([]string{
			r.RequestID,
			r.StepID,
			r.Capability,
			r.AgentName,
			strconv.FormatBool(r.Success),
			strconv.Itoa(r.AutoWiredCount),
			strconv.Itoa(r.MicroResolved),
			strconv.Itoa(r.SemanticRetry),
			strconv.Itoa(r.UserProvided),
			strconv.FormatInt(r.AutoWireDurUs, 10),
			strconv.FormatInt(r.MicroResolvMs, 10),
			strconv.Itoa(r.SourceKeyCount),
			strconv.Itoa(r.TotalParams),
		})
	}
}

// intFromMap extracts an int from a map[string]interface{}, handling float64 (JSON default)
func intFromMap(m map[string]interface{}, key string) int {
	if v, ok := m[key]; ok {
		switch val := v.(type) {
		case float64:
			return int(val)
		case int:
			return val
		case int64:
			return int(val)
		}
	}
	return 0
}

// int64FromMap extracts an int64 from a map[string]interface{}, handling float64 (JSON default)
func int64FromMap(m map[string]interface{}, key string) int64 {
	if v, ok := m[key]; ok {
		switch val := v.(type) {
		case float64:
			return int64(val)
		case int64:
			return val
		case int:
			return int64(val)
		}
	}
	return 0
}

// buildUnifiedView combines execution data with LLM debug and HITL checkpoint data
func buildUnifiedView(execution *StoredExecution) *UnifiedExecutionView {
	if execution == nil {
		return nil
	}

	unified := &UnifiedExecutionView{
		RequestID:         execution.RequestID,
		OriginalRequestID: execution.OriginalRequestID,
		TraceID:           execution.TraceID,
		AgentName:         execution.AgentName,
		OriginalRequest:   execution.OriginalRequest,
		CreatedAt:         execution.CreatedAt,
		Plan:              execution.Plan,
		Result:            execution.Result,
		Interrupted:       execution.Interrupted,
		Checkpoint:        execution.Checkpoint,
		PhasePlans:        execution.PhasePlans,
		PhaseCount:        execution.PhaseCount,
		ForcedTerminal:    execution.ForcedTerminal,
	}

	// Compute success and duration from result
	if execution.Result != nil {
		unified.Success = execution.Result.Success
		unified.TotalDurationMs = execution.Result.TotalDuration / 1_000_000 // ns to ms
	}

	// Compute DAG structure
	unified.DAG = computeDAG(execution)

	// Fetch LLM debug data (non-blocking - errors are logged but don't fail the request)
	if !useMock {
		store, storeErr := getLLMDebugStore()
		var llmRecord *orchestration.LLMDebugRecord
		var err error
		if storeErr == nil {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			llmRecord, err = store.GetRecord(ctx, execution.RequestID)
		}
		if err == nil && llmRecord != nil {
			// Dedupe agent_llm_call shadows before building the summary so
			// historical records (written before the framework-side Layer 2
			// landed) and any still-unmigrated call sites produce correct
			// totals. Newly-written records from migrated code paths are
			// already single-recorded, so this is a no-op for them.
			// See orchestration/bugs/BUG_LLM_INTERACTION_DOUBLE_RECORDING.md.
			deduped := orchestration.DedupeLLMInteractions(llmRecord.Interactions)
			unified.LLMInteractions = deduped
			unified.HasLLMData = len(deduped) > 0

			// Build summary from the deduped view
			if len(deduped) > 0 {
				summary := &LLMDebugSummary{
					TotalCalls:        len(deduped),
					ProviderBreakdown: make(map[string]int),
				}
				for _, interaction := range deduped {
					summary.TotalTokensIn += interaction.PromptTokens
					summary.TotalTokensOut += interaction.CompletionTokens
					summary.TotalDurationMs += interaction.DurationMs
					provider := interaction.Provider
					if provider == "" {
						provider = "unknown"
					}
					summary.ProviderBreakdown[provider]++
				}
				unified.LLMDebugSummary = summary
			}
		} else if err != nil && !strings.Contains(err.Error(), "not found") {
			log.Printf("Warning: failed to fetch LLM debug data for %s: %v", execution.RequestID, err)
		}

		// Fetch HITL checkpoints by request ID
		checkpoints, err := getHITLCheckpointsByRequestID(execution.RequestID)
		if err == nil && len(checkpoints) > 0 {
			unified.HITLCheckpoints = checkpoints
			unified.HasHITLData = true
		} else if err != nil {
			log.Printf("Warning: failed to fetch HITL checkpoints for %s: %v", execution.RequestID, err)
		}

		// Also check by original_request_id if different (for resumed HITL conversations)
		if execution.OriginalRequestID != "" && execution.OriginalRequestID != execution.RequestID {
			moreCheckpoints, err := getHITLCheckpointsByRequestID(execution.OriginalRequestID)
			if err == nil && len(moreCheckpoints) > 0 {
				unified.HITLCheckpoints = append(unified.HITLCheckpoints, moreCheckpoints...)
				unified.HasHITLData = true
			}
		}
	}

	// ORCH-022 Layer 4: surface a pointer to a completed resume sibling when this
	// record is interrupted. The UI renders a banner linking to the resume.
	// Canonical "is interrupted" signal is execution.Interrupted; also flag
	// has-HITL-data when the stored record carries an embedded checkpoint.
	if execution.Checkpoint != nil {
		unified.HasHITLData = true
	}
	if execution.Interrupted && execution.OriginalRequestID != "" {
		if sibling := findCompletedResumeSibling(execution); sibling != "" {
			unified.ResumeSiblingRequestID = sibling
		}
	}

	return unified
}

// resumeSiblingSearchDepth bounds the findCompletedResumeSibling scan. Named
// constant so the limitation is discoverable by grep rather than buried as a
// magic number at the call site.
const resumeSiblingSearchDepth = 200

// findCompletedResumeSibling searches recent executions for a non-interrupted
// record sharing the same original_request_id. Returns the sibling's request_id
// or empty string when none exists.
//
// Scope and limits (Layer 4 UX banner, not load-bearing):
//   - Only fires when the current execution is interrupted AND has a non-empty
//     original_request_id (cheap early-out on the common case).
//   - Scans the most recent resumeSiblingSearchDepth executions. Older siblings
//     (e.g. an approval that pended for weeks) may not be found; the banner
//     is best-effort.
//   - No memoization or indexed lookup. Each unified-view load of an interrupted
//     record issues one Redis pipeline fetch. Acceptable at current scale; if
//     viewer load becomes a concern, add a short-TTL cache keyed by
//     original_request_id or push an explicit index.
func findCompletedResumeSibling(execution *StoredExecution) string {
	if useMock || execution == nil || execution.OriginalRequestID == "" {
		return ""
	}
	page, err := getRedisExecutionSummaries(resumeSiblingSearchDepth, "")
	if err != nil || page == nil {
		return ""
	}
	for _, s := range page.Summaries {
		if s.RequestID == execution.RequestID {
			continue
		}
		if s.OriginalRequestID != execution.OriginalRequestID {
			continue
		}
		if !s.Interrupted {
			return s.RequestID
		}
	}
	return ""
}

// getHITLCheckpointsByRequestID finds all HITL checkpoints for a given request ID.
// Delegates to the HTTP fan-out helper (RC3-Backend).
func getHITLCheckpointsByRequestID(requestID string) ([]HITLCheckpoint, error) {
	return getAgentHITLCheckpointsByRequestID(context.Background(), requestID)
}

// getRedisExecutionSummaries fetches recent execution summaries from Redis
// executionPage holds a page of summaries with cursor for pagination.
type executionPage struct {
	Summaries  []ExecutionSummary
	NextCursor string // Score of last item; empty if no more pages
	HasMore    bool
}

func getRedisExecutionSummaries(limit int, cursor string) (*executionPage, error) {
	client, err := getExecutionDebugClient() // Uses Redis DB 8 for Execution Debug
	if err != nil {
		return nil, err
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Cursor-based pagination: fetch limit+1 to detect if there's a next page.
	// Max score is the cursor (exclusive) or "+inf" for the first page.
	maxScore := "+inf"
	if cursor != "" {
		// Use "(" prefix for exclusive range — don't re-include the cursor item
		maxScore = "(" + cursor
	}

	// Fetch limit+1 to detect hasMore
	fetchCount := int64(limit + 1)
	zResults, err := client.ZRevRangeByScoreWithScores(ctx, executionIndexKey, &redis.ZRangeBy{
		Min:   "-inf",
		Max:   maxScore,
		Count: fetchCount,
	}).Result()
	if err != nil {
		return nil, fmt.Errorf("failed to list executions: %w", err)
	}

	// Determine pagination state
	hasMore := len(zResults) > limit
	if hasMore {
		zResults = zResults[:limit] // Trim the extra item
	}

	if len(zResults) == 0 {
		return &executionPage{}, nil
	}

	// Extract request IDs and the last score for cursor
	requestIDs := make([]string, len(zResults))
	var lastScore string
	for i, z := range zResults {
		requestIDs[i] = z.Member.(string)
		if i == len(zResults)-1 {
			lastScore = fmt.Sprintf("%f", z.Score)
		}
	}

	// Batch-fetch all executions via Redis pipeline (replaces N individual GET calls)
	pipe := client.Pipeline()
	cmds := make([]*redis.StringCmd, len(requestIDs))
	for i, id := range requestIDs {
		cmds[i] = pipe.Get(ctx, executionKeyPrefix+id)
	}
	// Pipeline Exec returns an error if ANY command fails — individual key misses
	// (redis.Nil) are checked per-command below, so we only fail on context errors.
	if _, pipeErr := pipe.Exec(ctx); pipeErr != nil && pipeErr != redis.Nil {
		if ctx.Err() != nil {
			return nil, fmt.Errorf("pipeline exec failed: %w", pipeErr)
		}
	}

	summaries := make([]ExecutionSummary, 0, len(requestIDs))
	for i, requestID := range requestIDs {
		data, err := cmds[i].Bytes()
		if err != nil {
			log.Printf("Warning: skipping execution %s: %v", requestID, err)
			continue
		}
		execution, err := deserializeExecution(data)
		if err != nil {
			log.Printf("Warning: skipping execution %s: %v", requestID, err)
			continue
		}

		summary := ExecutionSummary{
			RequestID:         execution.RequestID,
			OriginalRequestID: execution.OriginalRequestID,
			TraceID:           execution.TraceID,
			AgentName:         execution.AgentName,
			OriginalRequest:   execution.OriginalRequest,
			Interrupted:       execution.Interrupted,
			CreatedAt:         execution.CreatedAt,
			Metadata:          execution.Metadata,
		}

		if execution.Result != nil {
			summary.Success = execution.Result.Success
			summary.TotalDurationMs = execution.Result.TotalDuration / 1_000_000 // ns to ms
			summary.StepCount = len(execution.Result.Steps)
			for _, step := range execution.Result.Steps {
				if !step.Success {
					summary.FailedSteps++
				}
			}
		}

		summaries = append(summaries, summary)
	}

	// Batch-fetch LLM debug data for all executions to get LLM durations.
	// This is a separate store (different Redis DB), so a separate pipeline.
	store, storeErr := getLLMDebugStore()
	if storeErr == nil && len(summaries) > 0 {
		for i := range summaries {
			llmRecord, _ := store.GetRecord(ctx, summaries[i].RequestID)
			if llmRecord != nil && len(llmRecord.Interactions) > 0 {
				var llmTotalMs int64
				for _, interaction := range llmRecord.Interactions {
					llmTotalMs += interaction.DurationMs
				}
				summaries[i].LLMTotalDurationMs = llmTotalMs
			}
		}
	}

	page := &executionPage{Summaries: summaries, HasMore: hasMore}
	if hasMore {
		page.NextCursor = lastScore
	}
	return page, nil
}

// getRedisExecution fetches a single execution from Redis
func getRedisExecution(requestID string) (*StoredExecution, error) {
	client, err := getExecutionDebugClient() // Uses Redis DB 8 for Execution Debug
	if err != nil {
		return nil, err
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	key := executionKeyPrefix + requestID
	data, err := client.Get(ctx, key).Bytes()
	if err == redis.Nil {
		return nil, fmt.Errorf("execution not found: %s", requestID)
	}
	if err != nil {
		return nil, fmt.Errorf("redis get failed: %w", err)
	}

	return deserializeExecution(data)
}

// deserializeExecution deserializes an execution with optional gzip decompression
// Format: first byte is compression flag (0=raw, 1=gzip), rest is JSON
func deserializeExecution(data []byte) (*StoredExecution, error) {
	if len(data) == 0 {
		return nil, fmt.Errorf("empty data")
	}

	var jsonData []byte

	// Check compression flag (first byte)
	if data[0] == 1 {
		// Gzip compressed
		reader, err := gzip.NewReader(bytes.NewReader(data[1:]))
		if err != nil {
			return nil, fmt.Errorf("gzip reader failed: %w", err)
		}
		defer reader.Close()

		var buf bytes.Buffer
		if _, err := buf.ReadFrom(reader); err != nil {
			return nil, fmt.Errorf("gzip decompress failed: %w", err)
		}
		jsonData = buf.Bytes()
	} else if data[0] == 0 {
		// Raw JSON (skip flag byte)
		jsonData = data[1:]
	} else {
		// Legacy format (no flag byte, raw JSON)
		jsonData = data
	}

	var execution StoredExecution
	if err := json.Unmarshal(jsonData, &execution); err != nil {
		return nil, fmt.Errorf("json unmarshal failed: %w", err)
	}

	return &execution, nil
}

// getMockExecutionSummaries returns mock execution summaries for development
func getMockExecutionSummaries() []ExecutionSummary {
	now := time.Now()
	return []ExecutionSummary{
		{
			RequestID:         "orch-1705312800123456789",
			OriginalRequestID: "orch-1705312800123456789",
			TraceID:           "abc123def456",
			OriginalRequest:   "What's the weather in Tokyo and convert to Celsius?",
			Success:           true,
			StepCount:         2,
			FailedSteps:       0,
			TotalDurationMs:   2450,
			CreatedAt:         now.Add(-5 * time.Minute),
		},
		{
			RequestID:         "orch-1705312700987654321",
			OriginalRequestID: "orch-1705312700987654321",
			TraceID:           "xyz789abc012",
			OriginalRequest:   "Book a flight from NYC to London and check the weather",
			Success:           false,
			StepCount:         3,
			FailedSteps:       1,
			TotalDurationMs:   4230,
			CreatedAt:         now.Add(-15 * time.Minute),
		},
		{
			RequestID:         "orch-1705312600111222333",
			OriginalRequestID: "orch-1705312600111222333",
			TraceID:           "mno345pqr678",
			OriginalRequest:   "Get stock prices for AAPL, GOOGL, and MSFT",
			Success:           true,
			StepCount:         3,
			FailedSteps:       0,
			TotalDurationMs:   1850,
			CreatedAt:         now.Add(-30 * time.Minute),
		},
	}
}

// getMockExecution returns a mock execution for development
func getMockExecution(requestID string) *StoredExecution {
	now := time.Now()

	switch requestID {
	case "orch-1705312800123456789":
		startTime1 := now.Add(-5*time.Minute - 2450*time.Millisecond)
		endTime1 := startTime1.Add(1200 * time.Millisecond)
		startTime2 := endTime1.Add(50 * time.Millisecond)
		endTime2 := startTime2.Add(1200 * time.Millisecond)
		planCreatedAt := startTime1.Add(-100 * time.Millisecond)

		return &StoredExecution{
			RequestID:         requestID,
			OriginalRequestID: requestID,
			TraceID:           "abc123def456",
			OriginalRequest:   "What's the weather in Tokyo and convert to Celsius?",
			Plan: &RoutingPlan{
				PlanID:          requestID,
				OriginalRequest: "What's the weather in Tokyo and convert to Celsius?",
				Mode:            "autonomous",
				Steps: []RoutingStep{
					{
						StepID:      "step-1",
						AgentName:   "weather-tool",
						Capability:  "get_weather",
						Instruction: "Get current weather for Tokyo",
						DependsOn:   []string{},
					},
					{
						StepID:      "step-2",
						AgentName:   "unit-converter",
						Capability:  "convert_temperature",
						Instruction: "Convert temperature from Fahrenheit to Celsius",
						DependsOn:   []string{"step-1"},
					},
				},
				CreatedAt: &planCreatedAt,
			},
			Result: &ExecutionResult{
				PlanID:        requestID,
				Success:       true,
				TotalDuration: 2450000000, // 2.45 seconds in nanoseconds
				Steps: []StepResult{
					{
						StepID:     "step-1",
						AgentName:  "weather-tool",
						Capability: "get_weather",
						Success:    true,
						Response:   map[string]interface{}{"temp": 72, "unit": "F", "condition": "sunny"},
						DurationMs: 1200,
						StartTime:  &startTime1,
						EndTime:    &endTime1,
						Attempts:   1,
					},
					{
						StepID:     "step-2",
						AgentName:  "unit-converter",
						Capability: "convert_temperature",
						Success:    true,
						Response:   map[string]interface{}{"temp": 22.2, "unit": "C"},
						DurationMs: 1200,
						StartTime:  &startTime2,
						EndTime:    &endTime2,
						Attempts:   1,
					},
				},
			},
			CreatedAt: now.Add(-5 * time.Minute),
		}

	case "orch-1705312700987654321":
		startTime1 := now.Add(-15*time.Minute - 4230*time.Millisecond)
		endTime1 := startTime1.Add(1500 * time.Millisecond)
		startTime2 := endTime1.Add(30 * time.Millisecond)
		endTime2 := startTime2.Add(2200 * time.Millisecond)
		startTime3 := startTime1 // Parallel with step 1
		endTime3 := startTime3.Add(500 * time.Millisecond)
		planCreatedAt := startTime1.Add(-100 * time.Millisecond)

		return &StoredExecution{
			RequestID:         requestID,
			OriginalRequestID: requestID,
			TraceID:           "xyz789abc012",
			OriginalRequest:   "Book a flight from NYC to London and check the weather",
			Plan: &RoutingPlan{
				PlanID:          requestID,
				OriginalRequest: "Book a flight from NYC to London and check the weather",
				Mode:            "autonomous",
				Steps: []RoutingStep{
					{
						StepID:      "step-1",
						AgentName:   "flight-booking",
						Capability:  "search_flights",
						Instruction: "Search for flights from NYC to London",
						DependsOn:   []string{},
					},
					{
						StepID:      "step-2",
						AgentName:   "flight-booking",
						Capability:  "book_flight",
						Instruction: "Book the selected flight",
						DependsOn:   []string{"step-1"},
					},
					{
						StepID:      "step-3",
						AgentName:   "weather-tool",
						Capability:  "get_weather",
						Instruction: "Get weather forecast for London",
						DependsOn:   []string{},
					},
				},
				CreatedAt: &planCreatedAt,
			},
			Result: &ExecutionResult{
				PlanID:        requestID,
				Success:       false,
				TotalDuration: 4230000000,
				Steps: []StepResult{
					{
						StepID:     "step-1",
						AgentName:  "flight-booking",
						Capability: "search_flights",
						Success:    true,
						Response:   map[string]interface{}{"flights": []string{"BA178", "AA101"}, "prices": []int{450, 520}},
						DurationMs: 1500,
						StartTime:  &startTime1,
						EndTime:    &endTime1,
						Attempts:   1,
					},
					{
						StepID:     "step-2",
						AgentName:  "flight-booking",
						Capability: "book_flight",
						Success:    false,
						Error:      "Payment gateway timeout after 3 retries",
						DurationMs: 2200,
						StartTime:  &startTime2,
						EndTime:    &endTime2,
						Attempts:   3,
					},
					{
						StepID:     "step-3",
						AgentName:  "weather-tool",
						Capability: "get_weather",
						Success:    true,
						Response:   map[string]interface{}{"temp": 12, "unit": "C", "condition": "cloudy"},
						DurationMs: 500,
						StartTime:  &startTime3,
						EndTime:    &endTime3,
						Attempts:   1,
					},
				},
			},
			CreatedAt: now.Add(-15 * time.Minute),
		}

	default:
		return nil
	}
}

// ============================================================================
// HITL HTTP Fan-Out Helpers (RC3-Backend)
// Replace direct Redis access with HTTP delegation to each agent's /hitl/checkpoints endpoints.
// ============================================================================

// getAgentHITLCheckpointSummaries fans out GET /hitl/checkpoints to all registered
// agents and merges the results. Unavailable agents are skipped gracefully.
// Only agents (type="agent") are queried — tools do not have HITL endpoints.
//
// Results are deduplicated by CheckpointID — multiple agents can legitimately
// return the same checkpoint when they share a Redis pending index (e.g.,
// instance-scoped registrations under a shared keyPrefix). The merged view
// should reflect unique checkpoints regardless of how many sources reported
// them. Mirrors the dedup pattern in getAgentHITLCheckpointsByRequestID below.
func getAgentHITLCheckpointSummaries(ctx context.Context) ([]HITLCheckpointSummary, error) {
	services, err := getRedisServices()
	if err != nil {
		return nil, err
	}
	var all []HITLCheckpointSummary
	seen := make(map[string]bool)
	for _, svc := range services {
		if svc.Address == "" || svc.Port == 0 || svc.Type != "agent" {
			continue
		}
		agentURL := fmt.Sprintf("http://%s:%d", svc.Address, svc.Port)
		resp, err := hitlHTTPClient.Get(agentURL + "/hitl/checkpoints")
		if err != nil {
			log.Printf("Warning: could not reach agent %s for HITL checkpoints: %v", svc.Name, err)
			continue
		}
		var result struct {
			Checkpoints []HITLCheckpoint `json:"checkpoints"`
		}
		decodeErr := json.NewDecoder(resp.Body).Decode(&result)
		resp.Body.Close()
		if decodeErr != nil {
			log.Printf("Warning: failed to decode HITL checkpoints from agent %s: %v", svc.Name, decodeErr)
			continue
		}
		for _, cp := range result.Checkpoints {
			if seen[cp.CheckpointID] {
				continue
			}
			seen[cp.CheckpointID] = true
			all = append(all, toHITLCheckpointSummary(cp))
		}
	}
	// Sort by created_at descending (newest first)
	sort.Slice(all, func(i, j int) bool {
		return all[i].CreatedAt.After(all[j].CreatedAt)
	})
	return all, nil
}

// getAgentHITLCheckpoint fans out GET /hitl/checkpoints/{id} to all registered agents
// and returns the first successful response. Returns "not found" error if no agent has it.
// Only agents (type="agent") are queried — tools do not have HITL endpoints.
func getAgentHITLCheckpoint(ctx context.Context, checkpointID string) (*HITLCheckpoint, error) {
	services, err := getRedisServices()
	if err != nil {
		return nil, err
	}
	for _, svc := range services {
		if svc.Address == "" || svc.Port == 0 || svc.Type != "agent" {
			continue
		}
		agentURL := fmt.Sprintf("http://%s:%d", svc.Address, svc.Port)
		resp, err := hitlHTTPClient.Get(agentURL + "/hitl/checkpoints/" + url.PathEscape(checkpointID))
		if err != nil {
			continue
		}
		if resp.StatusCode == http.StatusNotFound {
			resp.Body.Close()
			continue
		}
		if resp.StatusCode != http.StatusOK {
			resp.Body.Close()
			continue
		}
		var cp HITLCheckpoint
		decodeErr := json.NewDecoder(resp.Body).Decode(&cp)
		resp.Body.Close()
		if decodeErr != nil {
			continue
		}
		return &cp, nil
	}
	return nil, fmt.Errorf("checkpoint not found: %s", checkpointID)
}

// getAgentHITLCheckpointsByRequestID fans out GET /hitl/checkpoints?request_id={id}
// to all registered agents and merges results, deduplicating by CheckpointID.
// Only agents (type="agent") are queried — tools do not have HITL endpoints.
//
// The fan-out is parallelized with errgroup + bounded concurrency (16) so
// wall-clock latency is O(per-call × ceil(N/16)) instead of O(N × per-call).
// Per-agent errors are swallowed (return nil from goroutine) to preserve the
// sequential code's "ignore failures, return whatever came back" semantics.
func getAgentHITLCheckpointsByRequestID(ctx context.Context, requestID string) ([]HITLCheckpoint, error) {
	services, err := getRedisServices()
	if err != nil {
		return nil, err
	}

	var (
		mu          sync.Mutex
		checkpoints []HITLCheckpoint
		seen        = make(map[string]bool)
		g           errgroup.Group
	)
	g.SetLimit(16) // Bounded concurrency — avoids saturating the viewer's socket pool at large agent counts.

	for _, svc := range services {
		if svc.Address == "" || svc.Port == 0 || svc.Type != "agent" {
			continue
		}
		g.Go(func() error {
			agentURL := fmt.Sprintf("http://%s:%d", svc.Address, svc.Port)
			resp, err := hitlHTTPClient.Get(agentURL + "/hitl/checkpoints?request_id=" + url.QueryEscape(requestID))
			if err != nil {
				return nil // swallow per-agent failures, same as the sequential implementation
			}
			defer resp.Body.Close()
			if resp.StatusCode != http.StatusOK {
				return nil
			}
			var result struct {
				Checkpoints []HITLCheckpoint `json:"checkpoints"`
			}
			if decodeErr := json.NewDecoder(resp.Body).Decode(&result); decodeErr != nil {
				return nil
			}
			if len(result.Checkpoints) == 0 {
				return nil
			}
			mu.Lock()
			for _, cp := range result.Checkpoints {
				if !seen[cp.CheckpointID] {
					seen[cp.CheckpointID] = true
					checkpoints = append(checkpoints, cp)
				}
			}
			mu.Unlock()
			return nil
		})
	}
	_ = g.Wait() // goroutines never return errors; Wait only blocks until all finish

	sort.Slice(checkpoints, func(i, j int) bool {
		return checkpoints[i].CreatedAt.After(checkpoints[j].CreatedAt)
	})
	return checkpoints, nil
}

// toHITLCheckpointSummary converts a full HITLCheckpoint to the lightweight summary type.
func toHITLCheckpointSummary(cp HITLCheckpoint) HITLCheckpointSummary {
	reason, priority, message := "", "", ""
	if cp.Decision != nil {
		reason = cp.Decision.Reason
		priority = cp.Decision.Priority
		message = cp.Decision.Message
	}
	currentStep, stepID, stepInstruction := "", "", ""
	if cp.CurrentStep != nil {
		currentStep = cp.CurrentStep.Capability
		stepID = cp.CurrentStep.StepID
		stepInstruction = cp.CurrentStep.Instruction
	}
	stepCount := 0
	if cp.Plan != nil {
		stepCount = len(cp.Plan.Steps)
	}
	return HITLCheckpointSummary{
		CheckpointID:    cp.CheckpointID,
		RequestID:       cp.RequestID,
		InterruptPoint:  cp.InterruptPoint,
		Reason:          reason,
		Priority:        priority,
		Message:         message,
		OriginalRequest: cp.OriginalRequest,
		StepCount:       stepCount,
		CompletedCount:  len(cp.StepResults),
		CurrentStep:     currentStep,
		StepID:          stepID,
		StepInstruction: stepInstruction,
		CreatedAt:       cp.CreatedAt,
		ExpiresAt:       cp.ExpiresAt,
		Status:          cp.Status,
		AgentName:       cp.AgentName,
		AgentAddress:    cp.AgentAddress,
		RequestMode:     cp.RequestMode,
	}
}

// ============================================================================
// HITL Checkpoint Mock Data
// ============================================================================

// getMockHITLCheckpointSummaries returns mock summaries for development
func getMockHITLCheckpointSummaries() []HITLCheckpointSummary {
	now := time.Now()
	return []HITLCheckpointSummary{
		{
			CheckpointID:    "cp-abc123-plan",
			RequestID:       "req-travel-001",
			InterruptPoint:  "plan_generated",
			Reason:          "plan_approval",
			Priority:        "normal",
			Message:         "Execution plan requires approval before proceeding",
			OriginalRequest: "What's the weather in Tokyo and book me a flight there?",
			StepCount:       3,
			CompletedCount:  0,
			CurrentStep:     "",
			CreatedAt:       now.Add(-2 * time.Minute),
			ExpiresAt:       now.Add(5 * time.Minute),
			Status:          "pending",
			AgentName:       "travel-agent",
		},
		{
			CheckpointID:    "cp-def456-step",
			RequestID:       "req-stock-002",
			InterruptPoint:  "before_step",
			Reason:          "sensitive_operation",
			Priority:        "high",
			Message:         "About to execute sensitive operation: stock_trade.execute_trade",
			OriginalRequest: "Buy 100 shares of AAPL",
			StepCount:       2,
			CompletedCount:  1,
			CurrentStep:     "stock_trade.execute_trade",
			CreatedAt:       now.Add(-5 * time.Minute),
			ExpiresAt:       now.Add(10 * time.Minute),
			Status:          "pending",
			AgentName:       "trading-bot",
		},
		{
			CheckpointID:    "cp-ghi789-error",
			RequestID:       "req-payment-003",
			InterruptPoint:  "on_error",
			Reason:          "escalation",
			Priority:        "critical",
			Message:         "Payment processing failed after 3 retries - human intervention required",
			OriginalRequest: "Process refund for order #12345",
			StepCount:       1,
			CompletedCount:  0,
			CurrentStep:     "payment.process_refund",
			CreatedAt:       now.Add(-10 * time.Minute),
			ExpiresAt:       now.Add(30 * time.Minute),
			Status:          "pending",
			AgentName:       "payment-service",
		},
	}
}

// getMockHITLCheckpoint returns a mock full checkpoint for development
func getMockHITLCheckpoint(checkpointID string) *HITLCheckpoint {
	now := time.Now()

	switch checkpointID {
	case "cp-abc123-plan":
		return &HITLCheckpoint{
			CheckpointID:   "cp-abc123-plan",
			RequestID:      "req-travel-001",
			InterruptPoint: "plan_generated",
			Decision: &InterruptDecision{
				ShouldInterrupt: true,
				Reason:          "plan_approval",
				Message:         "Execution plan requires approval before proceeding",
				Priority:        "normal",
				Timeout:         300000000000, // 5 minutes in nanoseconds
				DefaultAction:   "approve",
			},
			Plan: &RoutingPlan{
				RequestID: "req-travel-001",
				Steps: []RoutingStep{
					{
						StepID:         "step-1",
						Capability:     "weather-tool.get_weather",
						ServiceName:    "weather-tool",
						CapabilityName: "get_weather",
						Parameters:     map[string]interface{}{"location": "Tokyo, Japan"},
						DependsOn:      []string{},
						Description:    "Get current weather for Tokyo",
					},
					{
						StepID:         "step-2",
						Capability:     "flight-search.search_flights",
						ServiceName:    "flight-search",
						CapabilityName: "search_flights",
						Parameters:     map[string]interface{}{"destination": "Tokyo", "date": "2024-03-15"},
						DependsOn:      []string{},
						Description:    "Search for available flights to Tokyo",
					},
					{
						StepID:         "step-3",
						Capability:     "flight-booking.book_flight",
						ServiceName:    "flight-booking",
						CapabilityName: "book_flight",
						Parameters:     map[string]interface{}{"flight_id": "{{step-2.flights[0].id}}"},
						DependsOn:      []string{"step-2"},
						Description:    "Book the selected flight",
					},
				},
				SynthesisStrategy: "llm",
				Rationale:         "User wants weather info and flight booking. Steps 1 and 2 can run in parallel, step 3 depends on step 2 results.",
			},
			CompletedSteps:  []StepResult{},
			OriginalRequest: "What's the weather in Tokyo and book me a flight there?",
			UserContext: map[string]interface{}{
				"user_id":    "user-123",
				"session_id": "session-abc",
			},
			CreatedAt: now.Add(-2 * time.Minute),
			ExpiresAt: now.Add(5 * time.Minute),
			Status:    "pending",
			AgentName: "travel-agent",
		}

	case "cp-def456-step":
		return &HITLCheckpoint{
			CheckpointID:   "cp-def456-step",
			RequestID:      "req-stock-002",
			InterruptPoint: "before_step",
			Decision: &InterruptDecision{
				ShouldInterrupt: true,
				Reason:          "sensitive_operation",
				Message:         "About to execute sensitive operation: stock_trade.execute_trade",
				Priority:        "high",
				Metadata: map[string]interface{}{
					"capability":   "stock_trade.execute_trade",
					"risk_level":   "high",
					"amount_limit": 10000,
				},
			},
			Plan: &RoutingPlan{
				RequestID: "req-stock-002",
				Steps: []RoutingStep{
					{
						StepID:         "step-1",
						Capability:     "stock-market.get_quote",
						ServiceName:    "stock-market",
						CapabilityName: "get_quote",
						Parameters:     map[string]interface{}{"symbol": "AAPL"},
						DependsOn:      []string{},
						Description:    "Get current stock quote for AAPL",
					},
					{
						StepID:         "step-2",
						Capability:     "stock_trade.execute_trade",
						ServiceName:    "stock_trade",
						CapabilityName: "execute_trade",
						Parameters:     map[string]interface{}{"symbol": "AAPL", "quantity": 100, "action": "buy"},
						DependsOn:      []string{"step-1"},
						Description:    "Execute buy order for 100 shares of AAPL",
					},
				},
				SynthesisStrategy: "simple",
			},
			CompletedSteps: []StepResult{
				{
					StepID:     "step-1",
					Capability: "stock-market.get_quote",
					Success:    true,
					Response: map[string]interface{}{
						"symbol": "AAPL",
						"price":  178.50,
						"change": 2.35,
					},
					DurationMs: 234,
				},
			},
			CurrentStep: &RoutingStep{
				StepID:         "step-2",
				Capability:     "stock_trade.execute_trade",
				ServiceName:    "stock_trade",
				CapabilityName: "execute_trade",
				Parameters:     map[string]interface{}{"symbol": "AAPL", "quantity": 100, "action": "buy"},
				DependsOn:      []string{"step-1"},
				Description:    "Execute buy order for 100 shares of AAPL",
			},
			ResolvedParameters: map[string]interface{}{
				"symbol":   "AAPL",
				"quantity": 100,
				"action":   "buy",
				"price":    178.50,
				"total":    17850.00,
			},
			OriginalRequest: "Buy 100 shares of AAPL",
			UserContext: map[string]interface{}{
				"user_id":      "user-456",
				"account_type": "premium",
			},
			CreatedAt: now.Add(-5 * time.Minute),
			ExpiresAt: now.Add(10 * time.Minute),
			Status:    "pending",
			AgentName: "trading-bot",
		}

	case "cp-ghi789-error":
		return &HITLCheckpoint{
			CheckpointID:   "cp-ghi789-error",
			RequestID:      "req-payment-003",
			InterruptPoint: "on_error",
			Decision: &InterruptDecision{
				ShouldInterrupt: true,
				Reason:          "escalation",
				Message:         "Payment processing failed after 3 retries - human intervention required",
				Priority:        "critical",
				Metadata: map[string]interface{}{
					"retry_count":   3,
					"last_error":    "Payment gateway timeout",
					"order_id":      "#12345",
					"refund_amount": 99.99,
				},
			},
			Plan: &RoutingPlan{
				RequestID: "req-payment-003",
				Steps: []RoutingStep{
					{
						StepID:         "step-1",
						Capability:     "payment.process_refund",
						ServiceName:    "payment",
						CapabilityName: "process_refund",
						Parameters:     map[string]interface{}{"order_id": "#12345", "amount": 99.99},
						DependsOn:      []string{},
						Description:    "Process refund for the order",
					},
				},
				SynthesisStrategy: "simple",
			},
			CompletedSteps: []StepResult{},
			CurrentStep: &RoutingStep{
				StepID:         "step-1",
				Capability:     "payment.process_refund",
				ServiceName:    "payment",
				CapabilityName: "process_refund",
				Parameters:     map[string]interface{}{"order_id": "#12345", "amount": 99.99},
				DependsOn:      []string{},
				Description:    "Process refund for the order",
			},
			CurrentStepResult: &StepResult{
				StepID:     "step-1",
				Capability: "payment.process_refund",
				Success:    false,
				Error:      "Payment gateway timeout after 30s - gateway returned 504",
				DurationMs: 30234,
			},
			OriginalRequest: "Process refund for order #12345",
			UserContext: map[string]interface{}{
				"user_id":  "user-789",
				"order_id": "#12345",
				"customer": "John Doe",
				"email":    "john@example.com",
			},
			CreatedAt: now.Add(-10 * time.Minute),
			ExpiresAt: now.Add(30 * time.Minute),
			Status:    "pending",
			AgentName: "payment-service",
		}

	default:
		return nil
	}
}

// getMockLLMDebugRecord returns a mock full record for development
func getMockLLMDebugRecord(requestID string) *orchestration.LLMDebugRecord {
	now := time.Now()

	// Return different mock data based on request ID
	switch requestID {
	case "req-abc123":
		return &orchestration.LLMDebugRecord{
			RequestID:         "req-abc123",
			OriginalRequestID: "req-abc123", // Same as RequestID for initial request
			TraceID:           "369fecb4e3156c34e0950c61f1f99d62",
			CreatedAt:         now.Add(-5 * time.Minute),
			UpdatedAt:         now.Add(-4 * time.Minute),
			Interactions: []orchestration.LLMInteraction{
				{
					Type:             "plan_generation",
					Timestamp:        now.Add(-5 * time.Minute),
					DurationMs:       1247,
					Prompt:           "You are an intelligent orchestrator. Given the user request and available capabilities, create an execution plan.\n\nUser Request: \"What's the weather in Tokyo and convert 100 USD to JPY?\"\n\nAvailable Capabilities:\n- weather-tool.get-weather: Get current weather for a location\n- currency-tool.convert: Convert between currencies\n\nCreate a routing plan with the necessary steps.",
					SystemPrompt:     "You must respond with valid JSON only. Do not include any explanation.",
					Temperature:      0.3,
					MaxTokens:        2000,
					Model:            "gpt-4o-mini",
					Provider:         "openai",
					Response:         "{\"routing_plan\":{\"steps\":[{\"id\":\"step1\",\"capability\":\"weather-tool.get-weather\",\"parameters\":{\"location\":\"Tokyo\"}},{\"id\":\"step2\",\"capability\":\"currency-tool.convert\",\"parameters\":{\"from\":\"USD\",\"to\":\"JPY\",\"amount\":100}}]}}",
					PromptTokens:     247,
					CompletionTokens: 156,
					TotalTokens:      403,
					Success:          true,
					Attempt:          1,
				},
				{
					Type:             "micro_resolution",
					Timestamp:        now.Add(-4*time.Minute - 30*time.Second),
					DurationMs:       523,
					Prompt:           "Extract parameters for the \"get-weather\" function.\n\nAvailable data from previous step:\n{\"location\": \"Tokyo\"}\n\nReturn the extracted parameter values.",
					Temperature:      0.0,
					MaxTokens:        500,
					Model:            "gpt-4o-mini",
					Provider:         "openai",
					Response:         "{\"location\": \"Tokyo\", \"units\": \"metric\"}",
					PromptTokens:     89,
					CompletionTokens: 24,
					TotalTokens:      113,
					Success:          true,
					Attempt:          1,
				},
				{
					Type:             "synthesis",
					Timestamp:        now.Add(-4 * time.Minute),
					DurationMs:       1892,
					Prompt:           "User Request: What's the weather in Tokyo and convert 100 USD to JPY?\n\nAgent Responses:\n\nAgent: weather-tool\nTask: Get weather for Tokyo\nResponse:\n{\n  \"location\": \"Tokyo\",\n  \"temperature\": 22,\n  \"conditions\": \"Partly cloudy\",\n  \"humidity\": 65\n}\n\nAgent: currency-tool\nTask: Convert 100 USD to JPY\nResponse:\n{\n  \"from\": \"USD\",\n  \"to\": \"JPY\",\n  \"amount\": 100,\n  \"result\": 15234.50,\n  \"rate\": 152.345\n}\n\nSynthesize these responses into a helpful answer.",
					SystemPrompt:     "You are an AI that synthesizes multiple agent responses into coherent, helpful answers.",
					Temperature:      0.5,
					MaxTokens:        1500,
					Model:            "gpt-4o-mini",
					Provider:         "openai",
					Response:         "Based on the information gathered:\n\n**Weather in Tokyo:**\nThe current weather in Tokyo is partly cloudy with a temperature of 22°C (72°F). The humidity is at 65%.\n\n**Currency Conversion:**\n100 USD equals approximately 15,234.50 JPY at the current exchange rate of 152.345 JPY per USD.\n\nIs there anything else you'd like to know about Tokyo or currency conversions?",
					PromptTokens:     423,
					CompletionTokens: 108,
					TotalTokens:      531,
					Success:          true,
					Attempt:          1,
				},
			},
			Metadata: map[string]string{
				"user_id":    "user-123",
				"session_id": "session-abc",
			},
		}

	case "req-def456":
		return &orchestration.LLMDebugRecord{
			RequestID:         "req-def456",
			OriginalRequestID: "req-abc123", // Part of same HITL conversation (resume)
			TraceID:           "7a8b9c0d1e2f3a4b5c6d7e8f9a0b1c2d",
			CreatedAt:         now.Add(-15 * time.Minute),
			UpdatedAt:         now.Add(-14 * time.Minute),
			Interactions: []orchestration.LLMInteraction{
				{
					Type:             "plan_generation",
					Timestamp:        now.Add(-15 * time.Minute),
					DurationMs:       987,
					Prompt:           "You are an intelligent orchestrator. Given the user request and available capabilities, create an execution plan.\n\nUser Request: \"Get the latest news about AI\"\n\nAvailable Capabilities:\n- news-tool.search-news: Search for news articles\n- news-tool.get-headlines: Get top headlines\n\nCreate a routing plan.",
					SystemPrompt:     "You must respond with valid JSON only.",
					Temperature:      0.3,
					MaxTokens:        2000,
					Model:            "gpt-4o-mini",
					Provider:         "openai",
					Response:         "{\"routing_plan\":{\"steps\":[{\"id\":\"step1\",\"capability\":\"news-tool.search-news\",\"parameters\":{\"query\":\"AI artificial intelligence\",\"limit\":5}}]}}",
					PromptTokens:     198,
					CompletionTokens: 87,
					TotalTokens:      285,
					Success:          true,
					Attempt:          1,
				},
				{
					Type:             "synthesis",
					Timestamp:        now.Add(-14 * time.Minute),
					DurationMs:       1238,
					Prompt:           "User Request: Get the latest news about AI\n\nAgent Responses:\n\nAgent: news-tool\nTask: Search news about AI\nResponse:\n{\n  \"articles\": [\n    {\"title\": \"OpenAI Announces GPT-5\", \"source\": \"TechCrunch\"},\n    {\"title\": \"AI Regulation in EU\", \"source\": \"Reuters\"}\n  ]\n}\n\nSynthesize into a helpful response.",
					SystemPrompt:     "You are an AI that synthesizes multiple agent responses into coherent, helpful answers.",
					Temperature:      0.5,
					MaxTokens:        1500,
					Model:            "gpt-4o-mini",
					Provider:         "openai",
					Response:         "Here are the latest AI news headlines:\n\n1. **OpenAI Announces GPT-5** (TechCrunch)\n2. **AI Regulation in EU** (Reuters)\n\nWould you like more details on any of these articles?",
					PromptTokens:     312,
					CompletionTokens: 56,
					TotalTokens:      368,
					Success:          true,
					Attempt:          1,
				},
			},
		}

	case "req-ghi789":
		return &orchestration.LLMDebugRecord{
			RequestID:         "req-ghi789",
			OriginalRequestID: "req-ghi789", // Different conversation
			TraceID:           "1234567890abcdef1234567890abcdef",
			CreatedAt:         now.Add(-1 * time.Hour),
			UpdatedAt:         now.Add(-59 * time.Minute),
			Interactions: []orchestration.LLMInteraction{
				{
					Type:             "plan_generation",
					Timestamp:        now.Add(-1 * time.Hour),
					DurationMs:       1523,
					Prompt:           "You are an intelligent orchestrator. Given the user request and available capabilities, create an execution plan.\n\nUser Request: \"Book a flight from NYC to London\"\n\nAvailable Capabilities:\n- travel-agent.find-flights: Search for flights\n- travel-agent.book-hotel: Find and book hotels\n\nCreate a routing plan.",
					SystemPrompt:     "You must respond with valid JSON only.",
					Temperature:      0.3,
					MaxTokens:        2000,
					Model:            "gpt-4o-mini",
					Provider:         "openai",
					Response:         "{\"routing_plan\":{\"steps\":[{\"id\":\"step1\",\"capability\":\"travel-agent.find-flights\",\"parameters\":{\"from\":\"NYC\",\"to\":\"London\"}}]}}",
					PromptTokens:     234,
					CompletionTokens: 98,
					TotalTokens:      332,
					Success:          true,
					Attempt:          1,
				},
				{
					Type:             "micro_resolution",
					Timestamp:        now.Add(-59*time.Minute - 45*time.Second),
					DurationMs:       412,
					Prompt:           "Extract parameters for the \"find-flights\" function.\n\nAvailable data:\n{\"from\": \"NYC\", \"to\": \"London\"}\n\nRequired parameters: departure_airport (IATA code), arrival_airport (IATA code), date\n\nReturn extracted values.",
					Temperature:      0.0,
					MaxTokens:        500,
					Model:            "gpt-4o-mini",
					Provider:         "openai",
					Response:         "{\"departure_airport\": \"JFK\", \"arrival_airport\": \"LHR\"}",
					PromptTokens:     156,
					CompletionTokens: 32,
					TotalTokens:      188,
					Success:          true,
					Attempt:          1,
				},
				{
					Type:         "correction",
					Timestamp:    now.Add(-59*time.Minute - 30*time.Second),
					DurationMs:   623,
					Prompt:       "The API returned an error: \"Missing required parameter: date\"\n\nOriginal parameters: {\"departure_airport\": \"JFK\", \"arrival_airport\": \"LHR\"}\n\nPlease provide corrected parameters including the missing 'date' field.",
					Temperature:  0.2,
					MaxTokens:    500,
					Model:        "gpt-4o-mini",
					Provider:     "openai",
					Response:     "",
					PromptTokens: 178,
					TotalTokens:  178,
					Success:      false,
					Error:        "LLM API timeout after 30s",
					Attempt:      1,
				},
				{
					Type:             "correction",
					Timestamp:        now.Add(-59 * time.Minute),
					DurationMs:       534,
					Prompt:           "The API returned an error: \"Missing required parameter: date\"\n\nOriginal parameters: {\"departure_airport\": \"JFK\", \"arrival_airport\": \"LHR\"}\n\nPlease provide corrected parameters including the missing 'date' field.",
					Temperature:      0.2,
					MaxTokens:        500,
					Model:            "gpt-4o-mini",
					Provider:         "openai",
					Response:         "{\"departure_airport\": \"JFK\", \"arrival_airport\": \"LHR\", \"date\": \"2024-02-15\"}",
					PromptTokens:     178,
					CompletionTokens: 45,
					TotalTokens:      223,
					Success:          true,
					Attempt:          2,
				},
			},
			Metadata: map[string]string{
				"error_count": "1",
				"retried":     "true",
			},
		}

	default:
		return nil
	}
}

// =============================================================================
// Memory View — Types, Initialization, Handlers, Mock Data
// =============================================================================

// domainMemory holds the memory interface implementations for one agent domain.
type domainMemory struct {
	episodic    core.EpisodicMemory
	coordinator core.InvestigationCoordinator
	digest      core.DigestCache
	activity    core.ActivityCoordinator
}

type MemoryBackends struct {
	Episodic    core.EpisodicMemory
	Coordinator core.InvestigationCoordinator
	Digest      core.DigestCache
	Activity    core.ActivityCoordinator
}

// MemoryBackendFactory builds backends per-domain because the Redis-backed
// memory constructors bake the domain into their state (key prefixes,
// stream names), so each domain needs its own concrete instances.
type MemoryBackendFactory func(domain string) (MemoryBackends, error)

var (
	memoryDomains     map[string]*domainMemory
	memoryDomainsList []string // ordered list of configured domains
	memoryEnabled     bool

	// Package-level factory singleton. Both call sites (startup + lazy
	// retry) go through getMemoryBackendFactory() so they share ONE
	// factory and therefore ONE underlying *redis.Client — mirroring the
	// pre-refactor singleton behavior of getRedisClient().
	memoryBackendFactory   MemoryBackendFactory
	memoryBackendFactoryMu sync.Mutex
)

func getMemoryBackendFactory() MemoryBackendFactory {
	memoryBackendFactoryMu.Lock()
	defer memoryBackendFactoryMu.Unlock()
	if memoryBackendFactory == nil {
		memoryBackendFactory = newRedisMemoryBackendFactory(redisURL)
	}
	return memoryBackendFactory
}

// newRedisMemoryBackendFactory returns a MemoryBackendFactory that builds
// the Redis-backed implementations of all four memory interfaces for a
// given domain. Do not call directly — go through getMemoryBackendFactory()
// so the factory and its *redis.Client remain process-wide singletons.
//
// The factory's getClient() re-Pings on every call. This is non-negotiable:
// the four Redis memory constructors only check for a non-nil client, not
// connectivity. Without the Ping gate, initMemoryClients would succeed with
// a dead Redis, populate memoryDomains with live objects wrapping a dead
// connection, and the lazy-retry path in getMemoryDomain (which only fires
// when memoryDomains == nil) would never run again.
func newRedisMemoryBackendFactory(redisURL string) MemoryBackendFactory {
	var (
		mu     sync.Mutex
		client *redis.Client
	)
	getClient := func() (*redis.Client, error) {
		mu.Lock()
		defer mu.Unlock()
		if client == nil {
			opt, err := redis.ParseURL(redisURL)
			if err != nil {
				return nil, fmt.Errorf("invalid redis URL: %w", err)
			}
			opt.DB = 0
			client = redis.NewClient(opt)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		if err := client.Ping(ctx).Err(); err != nil {
			return nil, fmt.Errorf("redis connection failed: %w", err)
		}
		return client, nil
	}

	return func(domain string) (MemoryBackends, error) {
		c, err := getClient()
		if err != nil {
			return MemoryBackends{}, fmt.Errorf("redis client: %w", err)
		}
		episodic, err := memory.NewStreamEpisodicMemory(
			memory.WithEpisodicRedisClient(c),
			memory.WithEpisodicDomain(domain),
		)
		if err != nil {
			return MemoryBackends{}, fmt.Errorf("episodic: %w", err)
		}
		coordinator, err := memory.NewAtomicLockCoordinator(
			memory.WithCoordinatorRedisClient(c),
			memory.WithCoordinatorDomain(domain),
		)
		if err != nil {
			return MemoryBackends{}, fmt.Errorf("coordinator: %w", err)
		}
		digest, err := memory.NewRedisDigestCache(c, nil)
		if err != nil {
			return MemoryBackends{}, fmt.Errorf("digest: %w", err)
		}
		activity, err := memory.NewRedisActivityCoordinator(c, domain)
		if err != nil {
			return MemoryBackends{}, fmt.Errorf("activity: %w", err)
		}
		return MemoryBackends{
			Episodic:    episodic,
			Coordinator: coordinator,
			Digest:      digest,
			Activity:    activity,
		}, nil
	}
}

// initMemoryClients creates memory interface implementations for each
// configured domain via the supplied factory. Safe to call multiple times —
// e.g., when the first startup attempt failed because the backend wasn't
// ready and we retry on the first memory endpoint request (see
// getMemoryDomain).
func initMemoryClients(factory MemoryBackendFactory, domains []string) {
	built := make(map[string]*domainMemory)
	for _, domain := range domains {
		backends, err := factory(domain)
		if err != nil {
			log.Printf("[WARN] Memory: failed to init backends for domain %q: %v", domain, err)
			continue
		}
		built[domain] = &domainMemory{
			episodic:    backends.Episodic,
			coordinator: backends.Coordinator,
			digest:      backends.Digest,
			activity:    backends.Activity,
		}
		log.Printf("[INFO] Memory: initialized domain %q", domain)
	}
	// Only promote to the package-level map if we successfully built at
	// least one domain — otherwise keep memoryDomains nil so getMemoryDomain
	// will retry on the next request.
	if len(built) > 0 {
		memoryDomains = built
	}
}

// getMemoryDomain returns the domainMemory for a given domain, or nil.
// If memoryDomains wasn't initialized at startup (because Redis was
// unreachable), this retries the initialization now. Subsequent calls
// after a successful init are fast — no repeat work.
func getMemoryDomain(domain string) *domainMemory {
	if memoryDomains == nil && memoryEnabled && len(memoryDomainsList) > 0 {
		initMemoryClients(getMemoryBackendFactory(), memoryDomainsList)
	}
	if memoryDomains == nil {
		return nil
	}
	return memoryDomains[domain]
}

// parseSinceDuration parses "1h", "6h", "24h", "3d", "7d" into a time.Duration.
func parseSinceDuration(s string) time.Duration {
	if s == "" {
		return 24 * time.Hour
	}
	if strings.HasSuffix(s, "d") {
		days, err := strconv.Atoi(strings.TrimSuffix(s, "d"))
		if err == nil && days > 0 {
			return time.Duration(days) * 24 * time.Hour
		}
	}
	d, err := time.ParseDuration(s)
	if err != nil {
		return 24 * time.Hour
	}
	return d
}

// memoryEventJSON is the JSON response shape for an episodic event.
type memoryEventJSON struct {
	EventID     string            `json:"event_id"`
	Timestamp   time.Time         `json:"timestamp"`
	AgentName   string            `json:"agent_name"`
	AgentDomain string            `json:"agent_domain"`
	ActionType  string            `json:"action_type"`
	EntityType  string            `json:"entity_type"`
	EntityID    string            `json:"entity_id"`
	Entities    []core.EntityRef  `json:"entities,omitempty"`
	Summary     string            `json:"summary"`
	Outcome     string            `json:"outcome"`
	Importance  float64           `json:"importance"`
	Scope       string            `json:"scope"`
	RequestID   string            `json:"request_id,omitempty"`
	TraceID     string            `json:"trace_id,omitempty"`
	ParentEvent string            `json:"parent_event,omitempty"`
	Confidence  float64           `json:"confidence,omitempty"`
	Metadata    map[string]string `json:"metadata,omitempty"`
}

func agentEventToJSON(e core.AgentEvent) memoryEventJSON {
	return memoryEventJSON{
		EventID:     e.EventID,
		Timestamp:   e.Timestamp,
		AgentName:   e.AgentName,
		AgentDomain: e.AgentDomain,
		ActionType:  e.ActionType,
		EntityType:  e.EntityType,
		EntityID:    e.EntityID,
		Entities:    e.Entities,
		Summary:     e.Summary,
		Outcome:     e.Outcome,
		Importance:  e.Importance,
		Scope:       string(e.Scope),
		RequestID:   e.RequestID,
		TraceID:     e.TraceID,
		ParentEvent: e.ParentEvent,
		Confidence:  e.Confidence,
		Metadata:    e.Metadata,
	}
}

// --- Memory Handlers ---

func handleMemoryDomains(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	domains := memoryDomainsList
	if useMock {
		domains = []string{"infrastructure", "travel"}
	}

	backends := map[string]bool{
		"episodic":       memoryEnabled || useMock,
		"investigations": memoryEnabled || useMock,
		"digest":         memoryEnabled || useMock,
		"activities":     memoryEnabled || useMock,
		"knowledge":      false, // Phase 2
	}

	json.NewEncoder(w).Encode(map[string]interface{}{
		"domains":  domains,
		"backends": backends,
	})
}

func handleMemoryEvents(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	domain := r.URL.Query().Get("domain")
	if domain == "" {
		http.Error(w, `{"error":"missing_parameter","message":"domain is required"}`, http.StatusBadRequest)
		return
	}

	since := parseSinceDuration(r.URL.Query().Get("since"))
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	if limit <= 0 {
		limit = 50
	}
	if limit > 200 {
		limit = 200
	}

	if useMock {
		events := getMockMemoryEvents(domain)
		jsonEvents := make([]memoryEventJSON, len(events))
		for i, e := range events {
			jsonEvents[i] = agentEventToJSON(e)
		}
		json.NewEncoder(w).Encode(map[string]interface{}{
			"events":      jsonEvents,
			"total_count": len(jsonEvents),
			"domain":      domain,
			"timestamp":   time.Now().UTC(),
		})
		return
	}

	dm := getMemoryDomain(domain)
	if dm == nil {
		http.Error(w, fmt.Sprintf(`{"error":"unknown_domain","message":"Domain %q not configured. Available: %v"}`, domain, memoryDomainsList), http.StatusBadRequest)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()

	filter := core.EventFilter{
		EntityType: r.URL.Query().Get("entity_type"),
		EntityID:   r.URL.Query().Get("entity_id"),
		AgentName:  r.URL.Query().Get("agent_name"),
		Since:      time.Now().Add(-since),
		Limit:      limit,
	}
	if at := r.URL.Query().Get("action_type"); at != "" {
		filter.ActionTypes = []string{at}
	}

	events, err := dm.episodic.QueryEvents(ctx, domain, filter)
	if err != nil {
		log.Printf("[ERROR] Memory: QueryEvents failed for domain %q: %v", domain, err)
		http.Error(w, `{"error":"query_failed","message":"Failed to query episodic events"}`, http.StatusInternalServerError)
		return
	}

	jsonEvents := make([]memoryEventJSON, len(events))
	for i, e := range events {
		jsonEvents[i] = agentEventToJSON(e)
	}

	json.NewEncoder(w).Encode(map[string]interface{}{
		"events":      jsonEvents,
		"total_count": len(jsonEvents),
		"domain":      domain,
		"timestamp":   time.Now().UTC(),
	})
}

func handleMemoryEventsRecent(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	domain := r.URL.Query().Get("domain")
	if domain == "" {
		http.Error(w, `{"error":"missing_parameter","message":"domain is required"}`, http.StatusBadRequest)
		return
	}

	since := parseSinceDuration(r.URL.Query().Get("since"))
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	if limit <= 0 {
		limit = 20
	}
	if limit > 200 {
		limit = 200
	}

	if useMock {
		events := getMockMemoryEvents(domain)
		if len(events) > limit {
			events = events[:limit]
		}
		jsonEvents := make([]memoryEventJSON, len(events))
		for i, e := range events {
			jsonEvents[i] = agentEventToJSON(e)
		}
		json.NewEncoder(w).Encode(map[string]interface{}{
			"events":      jsonEvents,
			"total_count": len(jsonEvents),
			"domain":      domain,
			"timestamp":   time.Now().UTC(),
		})
		return
	}

	dm := getMemoryDomain(domain)
	if dm == nil {
		http.Error(w, fmt.Sprintf(`{"error":"unknown_domain","message":"Domain %q not configured"}`, domain), http.StatusBadRequest)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()

	events, err := dm.episodic.QueryRecentEvents(ctx, domain, time.Now().Add(-since), limit)
	if err != nil {
		log.Printf("[ERROR] Memory: QueryRecentEvents failed for domain %q: %v", domain, err)
		http.Error(w, `{"error":"query_failed","message":"Failed to query recent events"}`, http.StatusInternalServerError)
		return
	}

	jsonEvents := make([]memoryEventJSON, len(events))
	for i, e := range events {
		jsonEvents[i] = agentEventToJSON(e)
	}

	json.NewEncoder(w).Encode(map[string]interface{}{
		"events":      jsonEvents,
		"total_count": len(jsonEvents),
		"domain":      domain,
		"timestamp":   time.Now().UTC(),
	})
}

func handleMemoryInvestigations(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	domain := r.URL.Query().Get("domain")

	if useMock {
		investigations := getMockMemoryInvestigations(domain)
		json.NewEncoder(w).Encode(map[string]interface{}{
			"investigations": investigations,
			"total_count":    len(investigations),
		})
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	type investigation struct {
		EntityID string `json:"entity_id"`
		Holder   string `json:"holder"`
		Domain   string `json:"domain"`
	}
	var results []investigation

	domainsToQuery := memoryDomainsList
	if domain != "" {
		domainsToQuery = []string{domain}
	}

	for _, d := range domainsToQuery {
		dm := getMemoryDomain(d)
		if dm == nil {
			continue
		}
		claims, err := dm.coordinator.GetActiveInvestigations(ctx)
		if err != nil {
			log.Printf("[WARN] Memory: GetActiveInvestigations failed for domain %q: %v", d, err)
			continue
		}
		for entityID, holder := range claims {
			results = append(results, investigation{
				EntityID: entityID,
				Holder:   holder,
				Domain:   d,
			})
		}
	}

	if results == nil {
		results = []investigation{}
	}

	json.NewEncoder(w).Encode(map[string]interface{}{
		"investigations": results,
		"total_count":    len(results),
	})
}

func handleMemoryDigest(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	domain := r.URL.Query().Get("domain")
	if domain == "" {
		http.Error(w, `{"error":"missing_parameter","message":"domain is required"}`, http.StatusBadRequest)
		return
	}

	if useMock {
		json.NewEncoder(w).Encode(map[string]interface{}{
			"domain":    domain,
			"digest":    getMockMemoryDigest(domain),
			"available": true,
		})
		return
	}

	dm := getMemoryDomain(domain)
	if dm == nil {
		http.Error(w, fmt.Sprintf(`{"error":"unknown_domain","message":"Domain %q not configured"}`, domain), http.StatusBadRequest)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	data, err := dm.digest.GetDigest(ctx, domain)
	if err != nil {
		log.Printf("[WARN] Memory: GetDigest failed for domain %q: %v", domain, err)
		json.NewEncoder(w).Encode(map[string]interface{}{
			"domain":    domain,
			"digest":    "",
			"available": false,
			"message":   "Failed to read digest from cache",
		})
		return
	}

	if len(data) == 0 {
		json.NewEncoder(w).Encode(map[string]interface{}{
			"domain":    domain,
			"digest":    "",
			"available": false,
			"message":   "No digest available. Compaction may not be configured or no recent activity.",
		})
		return
	}

	// The digest cache stores JSON with "content", "last_event_ts", "generated_at" fields.
	// Extract the content for display and pass through generated_at for metadata.
	var digestData struct {
		Content     string `json:"content"`
		GeneratedAt string `json:"generated_at"`
		LastEventTS string `json:"last_event_ts"`
	}
	if err := json.Unmarshal(data, &digestData); err == nil && digestData.Content != "" {
		// Re-marshal the raw data with indentation for the raw view
		var rawPretty interface{}
		json.Unmarshal(data, &rawPretty)
		json.NewEncoder(w).Encode(map[string]interface{}{
			"domain":       domain,
			"digest":       digestData.Content,
			"generated_at": digestData.GeneratedAt,
			"raw":          rawPretty,
			"available":    true,
		})
	} else {
		// Fallback: treat raw bytes as plain text digest
		json.NewEncoder(w).Encode(map[string]interface{}{
			"domain":    domain,
			"digest":    string(data),
			"available": true,
		})
	}
}

func handleMemoryActivities(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	domain := r.URL.Query().Get("domain")
	if domain == "" {
		http.Error(w, `{"error":"missing_parameter","message":"domain is required"}`, http.StatusBadRequest)
		return
	}

	if useMock {
		activities := getMockMemoryActivities(domain)
		json.NewEncoder(w).Encode(map[string]interface{}{
			"activities":  activities,
			"domain":      domain,
			"total_count": len(activities),
		})
		return
	}

	dm := getMemoryDomain(domain)
	if dm == nil {
		http.Error(w, fmt.Sprintf(`{"error":"unknown_domain","message":"Domain %q not configured"}`, domain), http.StatusBadRequest)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	signals, err := dm.activity.GetDomainActivities(ctx, domain)
	if err != nil {
		log.Printf("[WARN] Memory: GetDomainActivities failed for domain %q: %v", domain, err)
		signals = nil
	}

	type activityJSON struct {
		AgentName   string            `json:"agent_name"`
		AgentDomain string            `json:"agent_domain"`
		RequestID   string            `json:"request_id"`
		Query       string            `json:"query"`
		Status      string            `json:"status"`
		StartedAt   time.Time         `json:"started_at"`
		Metadata    map[string]string `json:"metadata,omitempty"`
	}
	results := make([]activityJSON, 0, len(signals))
	for _, s := range signals {
		results = append(results, activityJSON{
			AgentName:   s.AgentName,
			AgentDomain: s.AgentDomain,
			RequestID:   s.RequestID,
			Query:       s.Query,
			Status:      s.Status,
			StartedAt:   s.StartedAt,
			Metadata:    s.Metadata,
		})
	}

	json.NewEncoder(w).Encode(map[string]interface{}{
		"activities":  results,
		"domain":      domain,
		"total_count": len(results),
	})
}

// --- Mock Data Generators ---

func getMockMemoryEvents(domain string) []core.AgentEvent {
	now := time.Now()
	return []core.AgentEvent{
		{
			EventID: "mock-evt-001", Timestamp: now.Add(-10 * time.Minute),
			AgentName: "devops-chat-agent", AgentDomain: domain,
			ActionType: "rollout_restart", EntityType: "service", EntityID: "product-catalog-api",
			Entities: []core.EntityRef{{Type: "service", ID: "product-catalog-api"}},
			Summary:  "Initiated rolling restart of deployment/product-catalog-api in truvag3-examples namespace via devops-tool.",
			Outcome:  "success", Importance: 8, Scope: core.ScopeSharedDomain, RequestID: "orch-mock-001",
		},
		{
			EventID: "mock-evt-002", Timestamp: now.Add(-25 * time.Minute),
			AgentName: "devops-chat-agent", AgentDomain: domain,
			ActionType: "query_metrics", EntityType: "pod", EntityID: "prometheus",
			Entities: []core.EntityRef{{Type: "pod", ID: "prometheus"}},
			Summary:  "Queried Prometheus metrics for pod restart count. Result: 21 restarts in last 48 hours.",
			Outcome:  "success", Importance: 6, Scope: core.ScopeSharedDomain, RequestID: "orch-mock-002",
		},
		{
			EventID: "mock-evt-003", Timestamp: now.Add(-1 * time.Hour),
			AgentName: "event-driven-agent", AgentDomain: domain,
			ActionType: "create_ticket", EntityType: "service", EntityID: "TruvaG3HighLatency-product-catalog-api",
			Entities: []core.EntityRef{{Type: "service", ID: "product-catalog-api"}, {Type: "pod", ID: "product-catalog-api-78c468fc8b-q8v2s"}},
			Summary:  "Created JIRA ticket DEVOPS-58 for TruvaG3HighLatency alert on product-catalog-api.",
			Outcome:  "success", Importance: 9, Scope: core.ScopeGlobal, RequestID: "orch-mock-003", TraceID: "trace-abc123",
		},
		{
			EventID: "mock-evt-004", Timestamp: now.Add(-2 * time.Hour),
			AgentName: "devops-chat-agent", AgentDomain: domain,
			ActionType: "describe_resource", EntityType: "pod", EntityID: "otel-collector",
			Summary: "Described otel-collector DaemonSet pod — 3 restarts, Running status, resource limits 200m/256Mi.",
			Outcome: "success", Importance: 5, Scope: core.ScopePrivate, RequestID: "orch-mock-004",
		},
		{
			EventID: "mock-evt-005", Timestamp: now.Add(-4 * time.Hour),
			AgentName: "devops-chat-agent", AgentDomain: domain,
			ActionType: "patch_resource", EntityType: "deployment", EntityID: "product-catalog-api",
			Entities: []core.EntityRef{{Type: "deployment", ID: "product-catalog-api"}},
			Summary:  "Patched memory limits from 80Mi to 256Mi on deployment/product-catalog-api.",
			Outcome:  "success", Importance: 9, Scope: core.ScopeSharedDomain, RequestID: "orch-mock-005",
		},
		{
			EventID: "mock-evt-006", Timestamp: now.Add(-8 * time.Hour),
			AgentName: "devops-chat-agent", AgentDomain: domain,
			ActionType: "query_logs", EntityType: "pod", EntityID: "product-catalog-api-57854f48df-wkp8j",
			Summary: "Queried logs for product-catalog-api pod — found GC pressure warnings and connection pool degradation.",
			Outcome: "failure", Importance: 7, Scope: core.ScopeSharedDomain, RequestID: "orch-mock-007",
		},
	}
}

func getMockMemoryInvestigations(domain string) []map[string]string {
	return []map[string]string{
		{"entity_id": "product-catalog-api", "holder": "devops-chat-agent", "domain": domain},
	}
}

func getMockMemoryDigest(domain string) string {
	return `# Domain Activity Summary (last 24 hours)

## Active Investigation: product-catalog-api High Latency

- Rolling restart of product-catalog-api completed (10 min ago)
- JIRA ticket DEVOPS-58 created for TruvaG3HighLatency alert
- Memory limits patched from 80Mi to 256Mi on deployment
- Prometheus shows 21 restarts for prometheus pod in 48h

## Monitoring

- otel-collector DaemonSet: 3 restarts, Running status
- Slack notification sent to #devops-alerts

## Tools Used

- devops-tool: kubectl operations (rollout restart, describe, patch)
- prometheus-query-tool: metric queries
- jira-tool: ticket creation
- slack-tool: notifications`
}

func getMockMemoryActivities(domain string) []map[string]interface{} {
	return []map[string]interface{}{
		{
			"agent_name":   "devops-chat-agent",
			"agent_domain": domain,
			"request_id":   "orch-mock-active-001",
			"query":        "Investigate high latency on product-catalog-api",
			"status":       "executing",
			"started_at":   time.Now().Add(-2 * time.Minute).UTC(),
		},
		{
			"agent_name":   "event-driven-agent",
			"agent_domain": domain,
			"request_id":   "orch-mock-active-002",
			"query":        "Check prometheus pod restart count",
			"status":       "synthesizing",
			"started_at":   time.Now().Add(-30 * time.Second).UTC(),
		},
	}
}
