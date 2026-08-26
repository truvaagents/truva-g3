package core

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"reflect"
	"runtime/debug"
	"sync"
	"time"

	"github.com/google/uuid"
)

// Agent interface - agents have full discovery capabilities
type Agent interface {
	Component
	Start(ctx context.Context, port int) error
	RegisterCapability(cap Capability)
	Discover(ctx context.Context, filter DiscoveryFilter) ([]*ServiceInfo, error)
}

// HTTPComponent represents a component that can be run with HTTP server.
// Both Tools and Agents implement this interface, allowing the Framework
// to work with both types of components uniformly.
type HTTPComponent interface {
	Component
	Start(ctx context.Context, port int) error
	RegisterCapability(cap Capability)
}

// FieldHint provides basic field information for AI-powered payload generation.
// Part of Phase 2: Field-Hint-Based generation in the 3-tier schema architecture.
type FieldHint struct {
	Name        string `json:"name"`                  // Field name (e.g., "location")
	Type        string `json:"type"`                  // JSON type: "string", "number", "boolean", "object", "array"
	Example     string `json:"example,omitempty"`     // Example value (e.g., "London")
	Description string `json:"description,omitempty"` // Human-readable description
}

// SchemaSummary provides compact schema hints for the registry.
// Part of Phase 2: Field-Hint-Based generation in the 3-tier schema architecture.
// This summary is included in discovery responses to help AI generate accurate payloads.
type SchemaSummary struct {
	RequiredFields []FieldHint `json:"required,omitempty"` // Fields that must be provided
	OptionalFields []FieldHint `json:"optional,omitempty"` // Fields that are optional
}

// CapabilityType indicates the nature of a capability for executor behavior decisions.
type CapabilityType string

const (
	// CapabilityTool — calls external APIs, stateless, safe to retry.
	CapabilityTool CapabilityType = "tool"
	// CapabilityReasoning — LLM analysis only, no side effects.
	CapabilityReasoning CapabilityType = "reasoning"
	// CapabilityOrchestrator — nested DAG with own planning, execution, HITL.
	// Not safe to retry. Response includes steps[], usage, usage_by_phase.
	CapabilityOrchestrator CapabilityType = "orchestrator"
)

// Capability represents a capability that an agent provides.
// Supports 3-tier schema architecture:
// - Tier 1 (Phase 1): Description field for AI-based payload generation
// - Tier 2 (Phase 2): InputSummary/OutputSummary for field-hint-based generation
// - Tier 3 (Phase 3): Full JSON Schema available at SchemaEndpoint for validation
type Capability struct {
	Name        string           `json:"name"`
	Description string           `json:"description"`
	Endpoint    string           `json:"endpoint"`
	Type        CapabilityType   `json:"type,omitempty"` // defaults to "tool" if empty
	InputTypes  []string         `json:"input_types"`
	OutputTypes []string         `json:"output_types"`
	Handler     http.HandlerFunc `json:"-"` // Optional custom handler, excluded from JSON

	// Phase 2: Compact schema summaries (optional, ~200-300 bytes overhead)
	// These provide structured hints to AI for better payload generation accuracy
	InputSummary  *SchemaSummary `json:"input_summary,omitempty"`  // Field hints for input payloads
	OutputSummary *SchemaSummary `json:"output_summary,omitempty"` // Field hints for output responses

	// Phase 3: Full schema endpoint (auto-generated if InputSummary provided)
	// Format: /api/capabilities/{name}/schema
	// Returns complete JSON Schema v7 for validation
	SchemaEndpoint string `json:"schema_endpoint,omitempty"`

	// Internal marks capabilities that should not be exposed to LLM planning.
	// Internal capabilities are still callable via HTTP but are excluded from
	// the service catalog used for AI orchestration decisions.
	// Use cases: orchestration endpoints, admin endpoints, deprecated capabilities.
	Internal bool `json:"internal,omitempty"`
}

// BaseAgent provides the core agent functionality
// Agents are active components that can discover and orchestrate both tools and agents
type BaseAgent struct {
	// Core fields (always available)
	ID           string
	Name         string
	Type         ComponentType
	Capabilities []Capability
	Logger       Logger
	Discovery    Discovery // Agents get full discovery powers
	Memory       Memory

	// Optional fields (set by modules)
	Telemetry          Telemetry
	AI                 AIClient
	SchemaCache        SchemaCache        // Optional - for Phase 3 schema validation caching
	ConversationMemory ConversationMemory // Optional - session-scoped conversation history
	SemanticMemory     SemanticMemory     // Optional - cross-session similarity search

	// Configuration
	Config *Config

	// HTTP server
	server *http.Server
	mux    *http.ServeMux

	// Handler registration tracking
	registeredPatterns map[string]bool // Track registered patterns to prevent duplicates
	serverStarted      bool            // Track if server has started
	mu                 sync.RWMutex    // Protect concurrent access
}

// NewBaseAgent creates a new base agent with minimal dependencies
func NewBaseAgent(name string) *BaseAgent {
	config := DefaultConfig()
	config.Name = name
	return NewBaseAgentWithConfig(config)
}

// NewBaseAgentWithConfig creates a new base agent with configuration
func NewBaseAgentWithConfig(config *Config) *BaseAgent {
	if config == nil {
		config = DefaultConfig()
	}

	// Ensure name is set
	if config.Name == "" {
		config.Name = "truvag3-agent"
	}

	// Generate ID if not set
	if config.ID == "" {
		config.ID = fmt.Sprintf("%s-%s", config.Name, uuid.New().String()[:8])
	}

	// Track component type for automatic telemetry inference
	SetCurrentComponentType(ComponentTypeAgent)

	return &BaseAgent{
		ID:                 config.ID,
		Name:               config.Name,
		Type:               ComponentTypeAgent,
		Capabilities:       []Capability{},
		Logger:             &NoOpLogger{},    // Will be initialized based on config
		Memory:             NewMemoryStore(), // Mutex-guarded, TTL-aware. Override before NewFramework for custom impls; register a sweeper Runnable via framework.RegisterRunnable(core.NewMemoryStoreSweeper(...)) for periodic eviction.
		Telemetry:          &NoOpTelemetry{}, // Will be initialized based on config
		Config:             config,
		mux:                http.NewServeMux(),
		registeredPatterns: make(map[string]bool),
		serverStarted:      false,
	}
}

// Initialize initializes the agent
func (b *BaseAgent) Initialize(ctx context.Context) error {
	initStart := time.Now()

	b.Logger.Info("Starting agent initialization", map[string]interface{}{
		"id":                b.ID,
		"name":              b.Name,
		"type":              b.Type,
		"config_provided":   b.Config != nil,
		"discovery_enabled": b.Config != nil && b.Config.Discovery.Enabled,
		"namespace":         getNamespaceFromConfig(b.Config),
	})

	// Initialize components based on config
	if b.Config != nil {
		// Initialize discovery if configured
		if b.Config.Discovery.Enabled && b.Discovery == nil {
			b.Logger.Info("Initializing service discovery", map[string]interface{}{
				"provider":  b.Config.Discovery.Provider,
				"mock_mode": b.Config.Development.MockDiscovery,
				"redis_url": b.Config.Discovery.RedisURL != "",
			})

			if b.Config.Development.MockDiscovery {
				// Use mock discovery for development
				b.Discovery = NewMockDiscovery()
				b.Logger.Info("Using mock discovery for development", map[string]interface{}{
					"provider": "mock",
					"reason":   "development_mode",
				})
			} else if b.Config.Discovery.Provider == "redis" && b.Config.Discovery.RedisURL != "" {
				// Initialize Redis discovery with configured TTL
				if discovery, err := NewRedisDiscoveryWithOptions(b.Config.Discovery.RedisURL, "truvag3", b.Config.Discovery.TTL); err == nil {
					// Set logger for better observability
					discovery.SetLogger(b.Logger)
					b.mu.Lock()
					b.Discovery = discovery
					b.mu.Unlock()
					b.Logger.Info("Redis discovery initialized successfully", map[string]interface{}{
						"provider":      "redis",
						"redis_url":     b.Config.Discovery.RedisURL,
						"effective_ttl": discovery.TTL().String(),
						"requested_ttl": b.Config.Discovery.TTL.String(),
					})
				} else {
					// Enhance existing error logging with dependency context
					b.Logger.Error("Failed to initialize Redis discovery", map[string]interface{}{
						"error":         err,
						"error_type":    fmt.Sprintf("%T", err),
						"redis_url":     b.Config.Discovery.RedisURL,
						"impact":        "agent_will_run_without_discovery",
						"retry_enabled": b.Config.Discovery.RetryOnFailure,
					})

					// Start background retry if enabled
					if b.Config.Discovery.RetryOnFailure {
						address, port := ResolveServiceAddress(b.Config, b.Logger)

						serviceInfo := &ServiceInfo{
							ID:           b.ID,
							Name:         b.Name,
							Type:         ComponentTypeAgent,
							Capabilities: b.Capabilities,
							Address:      address,
							Port:         port,
							Metadata:     BuildServiceMetadata(b.Config),
						}

						// Define callback to update discovery reference
						onSuccess := func(newRegistry Registry) error {
							b.mu.Lock()
							defer b.mu.Unlock()

							// Stop old heartbeat if exists
							if oldDiscovery, ok := b.Discovery.(*RedisDiscovery); ok && oldDiscovery != nil {
								oldDiscovery.StopHeartbeat(ctx, b.ID)
							}

							// Update to new discovery
							b.Discovery = newRegistry.(Discovery)
							b.Logger.Info("Discovery reference updated", map[string]interface{}{
								"agent_id": b.ID,
							})
							return nil
						}

						// Start background retry manager
						StartRegistryRetry(
							ctx,
							b.Config.Discovery.RedisURL,
							serviceInfo,
							b.Config.Discovery.RetryInterval,
							b.Logger,
							onSuccess,
							b.Config.Discovery.TTL,
							b.Config.Discovery.HeartbeatInterval,
						)

						b.Logger.Info("Background discovery retry started", map[string]interface{}{
							"agent_id": b.ID,
						})
					}
				}
			}
		}

	}

	if b.Discovery != nil {
		address, port := ResolveServiceAddress(b.Config, b.Logger)

		b.Logger.Info("Attempting service registration", map[string]interface{}{
			"service_id":         b.ID,
			"service_name":       b.Name,
			"resolved_address":   address,
			"resolved_port":      port,
			"capabilities_count": len(b.Capabilities),
			"namespace":          getNamespaceFromConfig(b.Config),
		})

		capabilities := make([]string, len(b.Capabilities))
		for i, cap := range b.Capabilities {
			capabilities[i] = cap.Name
		}

		registration := &ServiceInfo{
			ID:           b.ID,
			Name:         b.Name,
			Type:         b.Type,
			Address:      address,
			Port:         port,
			Capabilities: b.Capabilities,
			Health:       HealthHealthy,
			LastSeen:     time.Now(),
			Metadata:     BuildServiceMetadata(b.Config),
		}

		if err := b.Discovery.Register(ctx, registration); err != nil {
			b.Logger.Error("Failed to register with discovery", map[string]interface{}{
				"error":      err,
				"error_type": fmt.Sprintf("%T", err),
				"agent_id":   b.ID,
				"agent_name": b.Name,
			})
			// Continue anyway - graceful degradation
		} else {
			// Start heartbeat to keep registration alive (Redis-specific)
			if redisDiscovery, ok := b.Discovery.(*RedisDiscovery); ok {
				redisDiscovery.StartHeartbeat(ctx, b.ID, b.Config.Discovery.HeartbeatInterval)
				b.Logger.Info("Started heartbeat for agent registration", map[string]interface{}{
					"agent_id":             b.ID,
					"agent_name":           b.Name,
					"ttl_sec":              int(redisDiscovery.ttl.Seconds()),
					"heartbeat_configured": b.Config.Discovery.HeartbeatInterval.String(),
				})
			}
		}
	} else {
		b.Logger.Warn("Agent running without service discovery", map[string]interface{}{
			"reason":        "discovery_not_configured",
			"impact":        "agent_not_discoverable",
			"manual_config": "required_for_service_mesh",
		})
	}

	// Emit framework metrics for agent initialization
	if registry := GetGlobalMetricsRegistry(); registry != nil {
		duration := float64(time.Since(initStart).Milliseconds())
		registry.Counter("agent.lifecycle",
			"agent_name", b.Name,
			"event", "initialized",
			"discovery_enabled", fmt.Sprintf("%t", b.Discovery != nil),
		)
		registry.Histogram("agent.initialization.duration_ms", duration,
			"agent_name", b.Name,
		)
		registry.Gauge("agent.capabilities.count", float64(len(b.Capabilities)),
			"agent_name", b.Name,
		)
	}

	b.Logger.Info("Agent initialization completed", map[string]interface{}{
		"id":                 b.ID,
		"name":               b.Name,
		"discovery_enabled":  b.Discovery != nil,
		"capabilities_count": len(b.Capabilities),
	})

	return nil
}

// getNamespaceFromConfig safely extracts namespace from config for logging
func getNamespaceFromConfig(config *Config) string {
	if config == nil {
		return ""
	}
	return config.Namespace
}

// GetID returns the agent ID
func (b *BaseAgent) GetID() string {
	return b.ID
}

// GetLogger returns the agent's logger.
// Implements the loggerHaver interface used by getComponentLogger so that
// types embedding *BaseAgent (e.g., *DevOpsChatAgent) inherit access to the logger.
// Without this method, getComponentLogger's `component.(*BaseAgent)` type assertion
// fails for embedded types and framework lifecycle logs are silently dropped.
func (b *BaseAgent) GetLogger() Logger {
	return b.Logger
}

// GetName returns the agent name
func (b *BaseAgent) GetName() string {
	return b.Name
}

// GetCapabilities returns the agent capabilities
func (b *BaseAgent) GetCapabilities() []Capability {
	return b.Capabilities
}

// GetType returns ComponentTypeAgent
func (b *BaseAgent) GetType() ComponentType {
	return b.Type
}

// Discover allows agents to discover both tools and other agents
func (b *BaseAgent) Discover(ctx context.Context, filter DiscoveryFilter) ([]*ServiceInfo, error) {
	if b.Discovery == nil {
		return nil, fmt.Errorf("discovery not configured for agent %s", b.Name)
	}
	return b.Discovery.Discover(ctx, filter)
}

// HandleFunc registers a custom HTTP handler for the given pattern.
// This method must be called before Start() is invoked.
// It returns an error if:
//   - The server has already been started
//   - The pattern has already been registered
//
// Example:
//
//	agent := core.NewBaseAgent("my-agent")
//	err := agent.HandleFunc("/api/custom", myHandler)
//	if err != nil {
//	    log.Fatal(err)
//	}
func (b *BaseAgent) HandleFunc(pattern string, handler http.HandlerFunc) error {
	b.mu.Lock()
	defer b.mu.Unlock()

	// Check if server has already started
	if b.serverStarted {
		// Keep the exact error message for backward compatibility with tests
		return fmt.Errorf("cannot register handler for pattern %s: server already started", pattern)
	}

	// Check for duplicate pattern registration
	if b.registeredPatterns[pattern] {
		// Keep the exact error message for backward compatibility with tests
		return fmt.Errorf("handler already registered for pattern: %s", pattern)
	}

	// Register the handler
	b.mux.HandleFunc(pattern, handler)
	b.registeredPatterns[pattern] = true

	// Log the registration
	b.Logger.Info("Registered custom handler", map[string]interface{}{
		"pattern": pattern,
	})

	return nil
}

// RegisterCapability registers a new capability with optional custom handler.
// If cap.Handler is provided, it will be used instead of the generic handler.
// If cap.Endpoint is empty, it will be auto-generated as /api/capabilities/{name}.
func (b *BaseAgent) RegisterCapability(cap Capability) {
	b.mu.Lock()
	defer b.mu.Unlock()

	// Auto-generate endpoint if not provided
	endpoint := cap.Endpoint
	if endpoint == "" {
		endpoint = fmt.Sprintf("/api/capabilities/%s", cap.Name)
	}

	// Update the capability's endpoint for consistency
	cap.Endpoint = endpoint

	// Phase 3: Auto-generate schema endpoint if InputSummary is provided
	// This enables on-demand schema fetching for validation
	if cap.InputSummary != nil {
		schemaEndpoint := fmt.Sprintf("%s/schema", endpoint)
		cap.SchemaEndpoint = schemaEndpoint

		// Register schema endpoint handler
		b.mux.HandleFunc(schemaEndpoint, b.handleSchemaRequest(cap))
		b.registeredPatterns[schemaEndpoint] = true

		b.Logger.Debug("Registered schema endpoint", map[string]interface{}{
			"capability":      cap.Name,
			"schema_endpoint": schemaEndpoint,
		})
	}

	// Append to capabilities list
	b.Capabilities = append(b.Capabilities, cap)

	// Register HTTP endpoint for the capability
	if cap.Handler != nil {
		// Use custom handler if provided (no automatic telemetry/logging)
		b.mux.HandleFunc(endpoint, cap.Handler)
	} else {
		// Use generic handler with telemetry and logging
		b.mux.HandleFunc(endpoint, b.handleCapabilityRequest(cap))
	}

	// Track this pattern internally
	b.registeredPatterns[endpoint] = true

	b.Logger.Info("Registered capability", map[string]interface{}{
		"name":           cap.Name,
		"endpoint":       endpoint,
		"custom_handler": cap.Handler != nil,
		"has_schema":     cap.InputSummary != nil,
	})
}

// handleCapabilityRequest creates an HTTP handler for a capability
func (b *BaseAgent) handleCapabilityRequest(cap Capability) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		capStart := time.Now()

		// Start telemetry span if available
		if b.Telemetry != nil {
			var span Span
			_, span = b.Telemetry.StartSpan(ctx, fmt.Sprintf("capability.%s", cap.Name))
			defer span.End()
			span.SetAttribute("capability.name", cap.Name)
		}

		// Log request
		b.Logger.Info("Handling capability request", map[string]interface{}{
			"capability": cap.Name,
			"method":     r.Method,
		})

		// Parse request
		var input map[string]interface{}
		if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
			// Emit framework metrics for capability error
			if registry := GetGlobalMetricsRegistry(); registry != nil {
				duration := float64(time.Since(capStart).Milliseconds())
				registry.Counter("agent.capability.executions",
					"agent_name", b.Name,
					"capability", cap.Name,
					"status", "error",
					"error_type", "parse_request",
				)
				registry.Histogram("agent.capability.duration_ms", duration,
					"agent_name", b.Name,
					"capability", cap.Name,
				)
			}

			b.Logger.Error("Failed to parse request", map[string]interface{}{
				"error":      err,
				"error_type": fmt.Sprintf("%T", err),
				"path":       r.URL.Path,
				"method":     r.Method,
			})
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}

		// TODO: Actual capability implementation would go here
		// This is where tool-specific logic would be implemented

		// Emit framework metrics for successful capability execution
		if registry := GetGlobalMetricsRegistry(); registry != nil {
			duration := float64(time.Since(capStart).Milliseconds())
			registry.Counter("agent.capability.executions",
				"agent_name", b.Name,
				"capability", cap.Name,
				"status", "success",
			)
			registry.Histogram("agent.capability.duration_ms", duration,
				"agent_name", b.Name,
				"capability", cap.Name,
			)
		}

		// Return response
		response := map[string]interface{}{
			"capability": cap.Name,
			"status":     "success",
			"result":     "Tool capability executed successfully",
		}

		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(response); err != nil {
			// Log error but response is already partially written
			if b.Logger != nil {
				b.Logger.Error("Failed to encode response", map[string]interface{}{
					"error":              err,
					"error_type":         fmt.Sprintf("%T", err),
					"agent_id":           b.ID,
					"request_method":     r.Method,
					"request_path":       r.URL.Path,
					"request_remote":     r.RemoteAddr,
					"capabilities_count": len(b.Capabilities),
					"user_agent":         r.Header.Get("User-Agent"),
					"content_length":     r.ContentLength,
				})
			}
		}
	}
}

// handleSchemaRequest creates an HTTP handler for schema endpoints.
// Part of Phase 3: Returns full JSON Schema v7 generated from InputSummary.
// This enables agents to fetch schemas on-demand for payload validation.
func (b *BaseAgent) handleSchemaRequest(cap Capability) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// Only support GET requests for schemas
		if r.Method != http.MethodGet {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}

		// Generate JSON Schema from InputSummary
		schema := b.generateJSONSchema(cap)

		// Return schema as JSON
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(schema); err != nil {
			b.Logger.Error("Failed to encode schema", map[string]interface{}{
				"error":      err,
				"capability": cap.Name,
			})
			http.Error(w, "Internal server error", http.StatusInternalServerError)
			return
		}

		b.Logger.Debug("Schema request served", map[string]interface{}{
			"capability": cap.Name,
			"client":     r.RemoteAddr,
		})
	}
}

// generateJSONSchema generates a JSON Schema v7 document from a Capability's InputSummary.
// Part of Phase 3: Converts compact field hints into full JSON Schema for validation.
func (b *BaseAgent) generateJSONSchema(cap Capability) map[string]interface{} {
	schema := map[string]interface{}{
		"$schema":     "http://json-schema.org/draft-07/schema#",
		"type":        "object",
		"title":       cap.Name,
		"description": cap.Description,
	}

	// If no InputSummary, return minimal schema
	if cap.InputSummary == nil {
		return schema
	}

	// Build properties from field hints
	properties := make(map[string]interface{})
	required := []string{}

	// Add required fields
	for _, field := range cap.InputSummary.RequiredFields {
		properties[field.Name] = b.fieldHintToJSONSchema(field)
		required = append(required, field.Name)
	}

	// Add optional fields
	for _, field := range cap.InputSummary.OptionalFields {
		properties[field.Name] = b.fieldHintToJSONSchema(field)
	}

	schema["properties"] = properties
	if len(required) > 0 {
		schema["required"] = required
	}
	schema["additionalProperties"] = false

	return schema
}

// fieldHintToJSONSchema converts a FieldHint to a JSON Schema property definition.
func (b *BaseAgent) fieldHintToJSONSchema(field FieldHint) map[string]interface{} {
	prop := map[string]interface{}{
		"type": field.Type,
	}

	if field.Description != "" {
		prop["description"] = field.Description
	}

	if field.Example != "" {
		prop["examples"] = []string{field.Example}
	}

	return prop
}

// Start starts the HTTP server for the agent
func (b *BaseAgent) Start(ctx context.Context, port int) error {
	b.mu.Lock()

	// Check if already started
	if b.serverStarted {
		b.mu.Unlock()
		return fmt.Errorf("server already started")
	}

	// Apply configuration precedence: explicit parameter > config > default
	// Only use Config.Port if no explicit port provided (port < 0)
	if port < 0 && b.Config != nil && b.Config.Port >= 0 {
		port = b.Config.Port
	}

	// Validate port range (0 is allowed for automatic assignment)
	if port < 0 || port > 65535 {
		b.mu.Unlock()
		b.Logger.Error("Invalid port specified", map[string]interface{}{
			"requested_port": port,
			"valid_range":    "0-65535",
			"port_zero_note": "0_enables_automatic_assignment",
		})
		return fmt.Errorf("invalid port %d: must be between 0-65535 (0 for automatic assignment)", port)
	}

	addr := fmt.Sprintf("%s:%d", b.Config.Address, port)
	if b.Config.Address == "" {
		addr = fmt.Sprintf(":%d", port)
	}

	b.Logger.Info("Configuring HTTP server", map[string]interface{}{
		"port":                 port,
		"cors_enabled":         b.Config.HTTP.CORS.Enabled,
		"health_check_enabled": b.Config.HTTP.EnableHealthCheck,
		"read_timeout":         b.Config.HTTP.ReadTimeout.String(),
		"write_timeout":        b.Config.HTTP.WriteTimeout.String(),
		"registered_endpoints": len(b.registeredPatterns),
	})

	// Add health endpoint if enabled
	if b.Config.HTTP.EnableHealthCheck {
		healthPath := b.Config.HTTP.HealthCheckPath
		// Check if health path is already registered (shouldn't be, but be safe)
		if !b.registeredPatterns[healthPath] {
			b.mux.HandleFunc(healthPath, func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusOK)
				if err := json.NewEncoder(w).Encode(map[string]string{
					"status": "healthy",
					"agent":  b.Name,
					"id":     b.ID,
				}); err != nil {
					// Log error but response is already partially written
					if b.Logger != nil {
						b.Logger.Error("Failed to encode health response", map[string]interface{}{
							"error":              err,
							"error_type":         fmt.Sprintf("%T", err),
							"agent_id":           b.ID,
							"request_method":     r.Method,
							"request_path":       r.URL.Path,
							"request_remote":     r.RemoteAddr,
							"capabilities_count": len(b.Capabilities),
							"user_agent":         r.Header.Get("User-Agent"),
							"content_length":     r.ContentLength,
						})
					}
				}
			})
			b.registeredPatterns[healthPath] = true
		}
	}

	// Add capabilities listing endpoint
	capabilitiesPath := "/api/capabilities"
	if !b.registeredPatterns[capabilitiesPath] {
		b.mux.HandleFunc(capabilitiesPath, func(w http.ResponseWriter, r *http.Request) {
			ApplyCORS(w, r, &b.Config.HTTP.CORS)
			w.Header().Set("Content-Type", "application/json")
			if err := json.NewEncoder(w).Encode(b.Capabilities); err != nil {
				// Log error but response is already partially written
				if b.Logger != nil {
					b.Logger.Error("Failed to encode capabilities", map[string]interface{}{
						"error":              err,
						"error_type":         fmt.Sprintf("%T", err),
						"agent_id":           b.ID,
						"request_method":     r.Method,
						"request_path":       r.URL.Path,
						"request_remote":     r.RemoteAddr,
						"capabilities_count": len(b.Capabilities),
						"user_agent":         r.Header.Get("User-Agent"),
						"content_length":     r.ContentLength,
					})
				}
			}
		})
		b.registeredPatterns[capabilitiesPath] = true
	}

	// Add OpenAPI spec endpoint — opt-in, disabled by default.
	// See HTTPConfig.EnableOpenAPI godoc for the security rationale:
	// the endpoint reveals every capability and its full schema to any
	// caller that can reach the pod, so it should never be enabled in
	// production without deliberate intent.
	if b.Config != nil && b.Config.HTTP.EnableOpenAPI {
		openapiPath := "/openapi.json"
		if !b.registeredPatterns[openapiPath] {
			b.mux.HandleFunc(openapiPath, openAPIHandler(b.Name, b.Type, func() []Capability {
				return b.Capabilities
			}))
			b.registeredPatterns[openapiPath] = true
		}
	}

	if len(b.registeredPatterns) > 0 {
		endpoints := make([]string, 0, len(b.registeredPatterns))
		for pattern := range b.registeredPatterns {
			endpoints = append(endpoints, pattern)
		}
		b.Logger.Info("HTTP endpoints registered", map[string]interface{}{
			"endpoints":    endpoints,
			"total_count":  len(endpoints),
			"capabilities": len(b.Capabilities),
		})
	}

	// Create handler with middleware stack
	// Order (outermost to innermost): CORS -> User Middleware -> Logging -> Recovery -> Handler
	// User middleware (e.g., TracingMiddleware) is placed after CORS to avoid tracing preflight requests,
	// and before logging so traces can capture the full request lifecycle.
	var handler http.Handler = b.mux

	// Always wrap with panic recovery middleware (innermost - catches panics from handler)
	handler = RecoveryMiddleware(b.Logger)(handler)

	// Add request/response logging middleware
	handler = LoggingMiddleware(b.Logger, b.Config.Development.Enabled)(handler)

	// Apply user-provided middleware (e.g., telemetry.TracingMiddleware)
	// These are applied in reverse order so the first middleware in the slice is outermost
	for i := len(b.Config.HTTP.Middleware) - 1; i >= 0; i-- {
		handler = b.Config.HTTP.Middleware[i](handler)
	}

	// Add CORS middleware if enabled (outermost - handles preflight requests)
	if b.Config.HTTP.CORS.Enabled {
		handler = CORSMiddleware(&b.Config.HTTP.CORS)(handler)
	}

	b.server = &http.Server{
		Addr:              addr,
		Handler:           handler,
		ReadTimeout:       b.Config.HTTP.ReadTimeout,
		ReadHeaderTimeout: b.Config.HTTP.ReadHeaderTimeout,
		WriteTimeout:      b.Config.HTTP.WriteTimeout,
		IdleTimeout:       b.Config.HTTP.IdleTimeout,
		MaxHeaderBytes:    b.Config.HTTP.MaxHeaderBytes,
	}

	if b.Discovery != nil {
		address, registrationPort := ResolveServiceAddress(b.Config, b.Logger)
		b.Logger.Info("Updating service registration with server details", map[string]interface{}{
			"service_id":           b.ID,
			"registration_address": address,
			"registration_port":    registrationPort,
			"server_port":          port,
		})
	}

	// Mark server as started (before actually starting to prevent race conditions)
	b.serverStarted = true
	b.mu.Unlock() // Unlock before blocking ListenAndServe call

	b.Logger.Info("Starting HTTP server", map[string]interface{}{
		"address":           addr,
		"cors":              b.Config.HTTP.CORS.Enabled,
		"capabilities":      len(b.Capabilities),
		"discovery_enabled": b.Discovery != nil,
	})

	if err := b.server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		b.Logger.Error("HTTP server failed to start", map[string]interface{}{
			"error":      err.Error(),
			"error_type": fmt.Sprintf("%T", err),
			"address":    addr,
			"port":       port,
		})
		return err
	}

	return nil
}

// Stop stops the HTTP server
func (b *BaseAgent) Stop(ctx context.Context) error {
	shutdownStart := time.Now()

	b.mu.Lock()
	defer b.mu.Unlock()

	if b.server != nil {
		// Use configured shutdown timeout or context deadline
		shutdownCtx := ctx
		if b.Config != nil && b.Config.HTTP.ShutdownTimeout > 0 {
			var cancel context.CancelFunc
			shutdownCtx, cancel = context.WithTimeout(ctx, b.Config.HTTP.ShutdownTimeout)
			defer cancel()
		}

		// Unregister from discovery if available
		if b.Discovery != nil && b.Config.Discovery.Enabled {
			if err := b.Discovery.Unregister(shutdownCtx, b.ID); err != nil {
				b.Logger.Error("Failed to unregister from discovery", map[string]interface{}{
					"error":      err,                    // Preserve full error object
					"error_type": fmt.Sprintf("%T", err), // Log error type for debugging
					"agent_id":   b.ID,
					"operation":  "unregister",
				})
			}
		}

		// Reset server state
		b.serverStarted = false

		// Perform actual shutdown
		err := b.server.Shutdown(shutdownCtx)

		// Emit framework metrics after shutdown completes (captures actual duration)
		if registry := GetGlobalMetricsRegistry(); registry != nil {
			duration := float64(time.Since(shutdownStart).Milliseconds())
			status := "success"
			if err != nil {
				status = "error"
			}
			registry.Counter("agent.lifecycle",
				"agent_name", b.Name,
				"event", "shutdown",
				"status", status,
			)
			registry.Histogram("agent.shutdown.duration_ms", duration,
				"agent_name", b.Name,
				"status", status,
			)
		}

		return err
	}

	// Emit shutdown metric even if server was nil
	if registry := GetGlobalMetricsRegistry(); registry != nil {
		registry.Counter("agent.lifecycle",
			"agent_name", b.Name,
			"event", "shutdown",
		)
	}

	return nil
}

// RecoveryMiddleware creates a middleware that recovers from panics in HTTP
// handlers via defer/recover.
//
// Limitation: Go's runtime fatal errors (concurrent map writes, certain
// stack-overflow paths) bypass recover() and terminate the process. This
// middleware cannot catch them; a panic of that class will still bring down
// the pod. If you see a "fatal error: ..." log line followed by kubelet
// restarts, the underlying bug must be fixed at the source.
func RecoveryMiddleware(logger Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			defer func() {
				if err := recover(); err != nil {
					// Log the panic with stack trace using structured logging
					stackTrace := debug.Stack()
					if logger != nil {
						logger.ErrorWithContext(r.Context(), "HTTP handler panic recovered", map[string]interface{}{
							"panic":      err,
							"error_type": fmt.Sprintf("%T", err),
							"path":       r.URL.Path,
							"method":     r.Method,
							"stack":      string(stackTrace),
							"user_agent": r.UserAgent(),
							"remote_ip":  r.RemoteAddr,
						})
					}

					// Return Internal Server Error to client
					http.Error(w, "Internal Server Error", http.StatusInternalServerError)
				}
			}()
			next.ServeHTTP(w, r)
		})
	}
}

// Framework provides a simple way to run components (both Tools and Agents)
type Framework struct {
	component HTTPComponent
	config    *Config
	runnables []Runnable // Background jobs started by Run() and stopped on ctx cancel
}

// applyConfigToComponent applies configuration to a component.
// It handles both direct BaseAgent/BaseTool instances and types that embed them.
// When the logger implements ComponentAwareLogger, it creates component-specific
// loggers with names like "agent/<name>" or "tool/<name>" for easy log filtering.
func applyConfigToComponent(component HTTPComponent, config *Config) {
	// First try direct type assertion
	switch base := component.(type) {
	case *BaseAgent:
		base.Config = config
		base.Name = config.Name
		// Apply service-based ID for Kubernetes deployments to ensure
		// multiple pod replicas share the same service discovery entry
		if config.ID != "" {
			base.ID = config.ID
		} else if config.Kubernetes.Enabled && config.Kubernetes.ServiceName != "" {
			// In Kubernetes with service mesh, use stable service-based ID
			// This ensures all pod replicas register as one service entry
			base.ID = config.Name
		}
		// else: keep the UUID-based ID from NewBaseAgent (for non-K8s deployments)

		// Create component-specific logger for agent log filtering
		base.Logger = createComponentLogger(config.logger, "agent/"+base.ID)
		// Propagate logger to AI client if it exists
		if base.AI != nil {
			if loggable, ok := base.AI.(interface{ SetLogger(Logger) }); ok {
				loggable.SetLogger(base.Logger)
			}
		}
		return

	case *BaseTool:
		base.Config = config
		base.Name = config.Name
		// Apply service-based ID for Kubernetes deployments
		if config.ID != "" {
			base.ID = config.ID
		} else if config.Kubernetes.Enabled && config.Kubernetes.ServiceName != "" {
			// In Kubernetes with service mesh, use stable service-based ID
			base.ID = config.Name
		}
		// else: keep the UUID-based ID from NewBaseTool (for non-K8s deployments)

		// Create component-specific logger for tool log filtering
		base.Logger = createComponentLogger(config.logger, "tool/"+base.ID)
		// Propagate logger to AI client if it exists
		if base.AI != nil {
			if loggable, ok := base.AI.(interface{ SetLogger(Logger) }); ok {
				loggable.SetLogger(base.Logger)
			}
		}
		return
	}

	// If direct assertion failed, use reflection to find embedded BaseAgent or BaseTool
	v := reflect.ValueOf(component)
	if v.Kind() != reflect.Pointer {
		return // Component must be a pointer
	}

	v = v.Elem()
	if v.Kind() != reflect.Struct {
		return // Must be a struct
	}

	// Iterate through fields to find embedded BaseAgent or BaseTool
	for i := 0; i < v.NumField(); i++ {
		field := v.Field(i)
		fieldType := field.Type()

		// Check if this field is *BaseAgent
		if fieldType == reflect.TypeOf((*BaseAgent)(nil)) && field.CanInterface() {
			if base, ok := field.Interface().(*BaseAgent); ok && base != nil {
				base.Config = config
				base.Name = config.Name
				// Apply service-based ID for Kubernetes deployments
				if config.ID != "" {
					base.ID = config.ID
				} else if config.Kubernetes.Enabled && config.Kubernetes.ServiceName != "" {
					// In Kubernetes with service mesh, use stable service-based ID
					base.ID = config.Name
				}
				// else: keep the UUID-based ID from NewBaseAgent

				// Create component-specific logger for agent log filtering
				base.Logger = createComponentLogger(config.logger, "agent/"+base.ID)
				// Propagate logger to AI client if it exists
				if base.AI != nil {
					if loggable, ok := base.AI.(interface{ SetLogger(Logger) }); ok {
						loggable.SetLogger(base.Logger)
					}
				}
				return
			}
		}

		// Check if this field is *BaseTool
		if fieldType == reflect.TypeOf((*BaseTool)(nil)) && field.CanInterface() {
			if base, ok := field.Interface().(*BaseTool); ok && base != nil {
				base.Config = config
				base.Name = config.Name
				// Apply service-based ID for Kubernetes deployments
				if config.ID != "" {
					base.ID = config.ID
				} else if config.Kubernetes.Enabled && config.Kubernetes.ServiceName != "" {
					// In Kubernetes with service mesh, use stable service-based ID
					base.ID = config.Name
				}
				// else: keep the UUID-based ID from NewBaseTool

				// Create component-specific logger for tool log filtering
				base.Logger = createComponentLogger(config.logger, "tool/"+base.ID)
				// Propagate logger to AI client if it exists
				if base.AI != nil {
					if loggable, ok := base.AI.(interface{ SetLogger(Logger) }); ok {
						loggable.SetLogger(base.Logger)
					}
				}
				return
			}
		}
	}
}

// createComponentLogger creates a component-specific logger if the logger supports it.
// Falls back to the original logger if ComponentAwareLogger interface is not implemented.
func createComponentLogger(logger Logger, componentName string) Logger {
	if cal, ok := logger.(ComponentAwareLogger); ok {
		return cal.WithComponent(componentName)
	}
	return logger
}

// NewFramework creates a new framework instance with options.
// It accepts any HTTPComponent (Tool or Agent) and provides uniform initialization and execution.
func NewFramework(component HTTPComponent, opts ...Option) (*Framework, error) {
	// Create configuration with options
	config, err := NewConfig(opts...)
	if err != nil {
		return nil, fmt.Errorf("failed to create config: %w", err)
	}

	// Update config for BaseAgent or BaseTool
	// This supports both direct instances and types that embed BaseAgent/BaseTool
	applyConfigToComponent(component, config)

	return &Framework{
		component: component,
		config:    config,
	}, nil
}

// RegisterRunnable registers a long-running component to be started alongside the framework.
// All registered runnables are started in goroutines when Run(ctx) is called and shut
// down gracefully when ctx is cancelled (typically by signal handler).
//
// Runnables are reusable for any background job: reflection, scheduled compaction,
// HITL expiry processing, custom user jobs. Each runnable implements the Runnable
// interface (single Start(ctx) method).
//
// Must be called before Run(). Calling RegisterRunnable after Run starts is undefined behaviour.
//
// Naming follows the framework's post-construction registration convention:
// RegisterCapability for capabilities, RegisterRunnable for background jobs.
// This is distinct from constructor options (WithDiscovery, WithName) which
// configure the framework at construction time.
func (f *Framework) RegisterRunnable(r Runnable) {
	f.runnables = append(f.runnables, r)

	// Lifecycle log — registration happens before Run, so no ctx available yet.
	// Use basic Info() per LOGGING_IMPLEMENTATION_GUIDE §5 (no request context).
	if logger := getComponentLogger(f.component); logger != nil {
		logger.Info("Runnable registered with framework", map[string]interface{}{
			"operation":     "framework_register_runnable",
			"runnable_type": fmt.Sprintf("%T", r),
			"total_count":   len(f.runnables),
		})
	}
}

// findEmbeddedBaseAgent returns the *BaseAgent embedded in component (or
// component itself if it IS a *BaseAgent). Returns nil if component is neither
// a *BaseAgent nor a struct that embeds *BaseAgent.
//
// Mirrors the reflection logic in applyConfigToComponent (above) — extracted
// so AutoRegisterMemorySweeper, which faces the same lookup problem, doesn't
// silently no-op for the in-tree agent examples (research_agent.go etc. all
// wrap *core.BaseAgent in a custom struct).
//
// Scope: only walks the top level of component's fields. Doubly-nested
// embeddings (X embeds Y, Y embeds *BaseAgent) are not detected. This
// matches applyConfigToComponent's behavior and is sufficient for the
// in-tree agent examples (research_agent.go etc., all single-level).
// If a future agent uses deeper nesting, both this helper and
// applyConfigToComponent would need to be updated together.
//
// Note: applyConfigToComponent could be refactored to use this helper too;
// that refactor is out of scope here.
func findEmbeddedBaseAgent(component interface{}) *BaseAgent {
	if component == nil {
		return nil
	}
	// Direct *BaseAgent — fast path for the test-style construction.
	if base, ok := component.(*BaseAgent); ok {
		return base
	}
	// Reflection walk for embedded *BaseAgent — matches the production
	// pattern used by the agent examples.
	v := reflect.ValueOf(component)
	if v.Kind() != reflect.Pointer {
		return nil
	}
	v = v.Elem()
	if v.Kind() != reflect.Struct {
		return nil
	}
	baseAgentType := reflect.TypeOf((*BaseAgent)(nil))
	for i := 0; i < v.NumField(); i++ {
		field := v.Field(i)
		if field.Type() == baseAgentType && field.CanInterface() {
			if base, ok := field.Interface().(*BaseAgent); ok && base != nil {
				return base
			}
		}
	}
	return nil
}

// AutoRegisterMemorySweeper inspects the wrapped component for a *BaseAgent
// (direct or embedded) and, if its Memory is the constructor-default
// *MemoryStore, registers a MemoryStoreSweeper Runnable using
// Config.Memory.CleanupInterval.
//
// No-op when:
//   - the wrapped component is neither *BaseAgent nor a struct embedding
//     *BaseAgent (tools own their cache lifecycle)
//   - agent.Memory is not a *MemoryStore (caller injected a custom impl)
//   - Config.Memory.CleanupInterval <= 0
//
// Tools own their cache lifecycle and should call f.RegisterRunnable directly
// for their own cache fields rather than using this helper.
//
// Idiomatic agent usage:
//
//	framework, _ := core.NewFramework(agent, opts...)
//	framework.AutoRegisterMemorySweeper()
//	framework.Run(ctx)
func (f *Framework) AutoRegisterMemorySweeper() {
	agent := findEmbeddedBaseAgent(f.component)
	if agent == nil {
		return
	}
	ms, ok := agent.Memory.(*MemoryStore)
	if !ok || ms == nil {
		return
	}
	if f.config.Memory.CleanupInterval <= 0 {
		return
	}
	// Pass agent.Logger so sweep-pass logs share the agent's component scope
	// (component-aware wrapping happens inside NewMemoryStoreSweeper).
	sweeper, err := NewMemoryStoreSweeper(
		ms,
		f.config.Memory.CleanupInterval,
		agent.Logger,
	)
	if err != nil {
		// Fail-open per orchestration/ARCHITECTURE.md §"Fail-Safe Defaults":
		// log and continue without a sweeper rather than failing framework
		// construction. Memory will still be hidden-on-expiry-read; only the
		// underlying-map eviction is missing.
		if agent.Logger != nil {
			agent.Logger.Error("Failed to create memory store sweeper; eviction disabled", map[string]interface{}{
				"operation": "auto_register_memory_sweeper",
				"error":     err.Error(),
			})
		}
		return
	}
	f.RegisterRunnable(sweeper)
}

// defaultRunnableDrainTimeout is the default time to wait for runnables to exit
// after ctx is cancelled. Override via TRUVAG3_FRAMEWORK_RUNNABLE_DRAIN_TIMEOUT env var.
const defaultRunnableDrainTimeout = 10 * time.Second

// Run initializes and starts the component (Tool or Agent) along with any registered runnables.
// Runnables run in parallel goroutines and shut down gracefully when ctx is cancelled.
// Run blocks until both the HTTP server and all runnables have exited (or drain timeout).
func (f *Framework) Run(ctx context.Context) error {
	// Initialize component
	if err := f.component.Initialize(ctx); err != nil {
		return fmt.Errorf("failed to initialize component: %w", err)
	}

	// Resolve drain timeout from env var (per "Externalize Hardcoded Limits" rule)
	drainTimeout := defaultRunnableDrainTimeout
	if v := os.Getenv("TRUVAG3_FRAMEWORK_RUNNABLE_DRAIN_TIMEOUT"); v != "" {
		if d, err := time.ParseDuration(v); err == nil && d > 0 {
			drainTimeout = d
		}
	}

	logger := getComponentLogger(f.component) // resolved once, used throughout

	// Lifecycle log: starting runnables (before HTTP server takes over)
	if logger != nil && len(f.runnables) > 0 {
		logger.InfoWithContext(ctx, "Starting registered runnables", map[string]interface{}{
			"operation":     "framework_runnable_start",
			"count":         len(f.runnables),
			"drain_timeout": drainTimeout.String(),
		})
	}

	// Start all registered runnables in background goroutines
	var wg sync.WaitGroup
	for _, r := range f.runnables {
		wg.Add(1)
		go func(r Runnable) {
			defer wg.Done()
			runnableType := fmt.Sprintf("%T", r)
			err := r.Start(ctx)
			// Pattern 1: logger nil check
			if logger == nil {
				return
			}
			switch {
			case err == nil || errors.Is(err, context.Canceled):
				// Clean exit — INFO level (lifecycle event)
				logger.InfoWithContext(ctx, "Runnable exited cleanly", map[string]interface{}{
					"operation":     "framework_runnable_exit",
					"runnable_type": runnableType,
				})
			default:
				// Unexpected error — ERROR level with error_type
				logger.ErrorWithContext(ctx, "Runnable exited with error", map[string]interface{}{
					"operation":     "framework_runnable_exit",
					"runnable_type": runnableType,
					"error":         err.Error(),
					"error_type":    "runnable_exit",
				})
			}
		}(r)
	}

	// Start HTTP server (blocks until ctx cancelled)
	err := f.component.Start(ctx, f.config.Port)

	// Lifecycle log: HTTP server stopped, draining runnables
	if logger != nil && len(f.runnables) > 0 {
		logger.InfoWithContext(ctx, "Draining registered runnables", map[string]interface{}{
			"operation":     "framework_runnable_drain",
			"count":         len(f.runnables),
			"drain_timeout": drainTimeout.String(),
		})
	}

	// Wait for runnables to finish gracefully (with bounded timeout)
	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()
	select {
	case <-done:
		// All runnables exited cleanly
		if logger != nil && len(f.runnables) > 0 {
			logger.InfoWithContext(ctx, "All runnables drained cleanly", map[string]interface{}{
				"operation": "framework_runnable_drain",
				"count":     len(f.runnables),
			})
		}
	case <-time.After(drainTimeout):
		// Timeout — log but don't block shutdown further.
		// Note: any remaining goroutines from buggy runnables will be reaped
		// by the OS on process exit. The framework cannot forcibly terminate
		// goroutines that ignore ctx — Go provides no mechanism for this.
		if logger != nil {
			logger.WarnWithContext(ctx, "Runnables did not exit within drain timeout", map[string]interface{}{
				"operation":     "framework_runnable_drain",
				"drain_timeout": drainTimeout.String(),
				"error_type":    "runnable_drain_timeout",
			})
		}
	}

	return err
}

// getComponentLogger extracts the Logger from an HTTPComponent if available.
// Returns nil if the component doesn't expose a logger (e.g., custom HTTPComponent impl).
func getComponentLogger(component HTTPComponent) Logger {
	if component == nil {
		return nil
	}
	if base, ok := component.(*BaseAgent); ok {
		return base.Logger
	}
	if base, ok := component.(*BaseTool); ok {
		return base.Logger
	}
	// Try reflection-style access via embedded BaseAgent/BaseTool
	type loggerHaver interface {
		GetLogger() Logger
	}
	if lh, ok := component.(loggerHaver); ok {
		return lh.GetLogger()
	}
	return nil
}
