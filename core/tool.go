package core

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sync"

	"github.com/google/uuid"
)

// generateID generates a unique ID for components
func generateID() string {
	return uuid.New().String()[:8]
}

// Tool interface - tools have NO discovery methods (compile-time safety)
type Tool interface {
	Component
	Start(ctx context.Context, port int) error
	RegisterCapability(cap Capability)
	// Tools cannot discover other components
}

// BaseTool provides the core tool functionality
// Tools are passive components that only respond to requests
type BaseTool struct {
	ID           string
	Name         string
	Type         ComponentType
	Capabilities []Capability
	capMutex     sync.RWMutex // Protects capabilities slice

	// Dependencies (all modules can enhance tools)
	Registry  Registry // Can register only - no discovery
	Logger    Logger
	Telemetry Telemetry // Telemetry still works
	AI        AIClient  // Can be AI-enhanced

	// Note: BaseTool intentionally does not provide a Memory field. Response
	// caching is a tool-local optimization with tool-specific TTL/eviction
	// semantics; tools that want it should add their own cache field (e.g.
	// `cache *core.MemoryStore`) opted in explicitly.

	// Configuration
	Config *Config // Configuration for K8s support and more

	// HTTP server
	server   *http.Server
	serverMu sync.RWMutex
	mux      *http.ServeMux

	// Handler registration tracking (same as BaseAgent for consistency)
	registeredPatterns map[string]bool // Track registered patterns to prevent duplicates

	// Mutex for thread-safe Registry access during background retry
	mu sync.RWMutex
}

// NewTool creates a new tool with default implementations
func NewTool(name string) *BaseTool {
	config := DefaultConfig()
	config.Name = name
	return NewToolWithConfig(config)
}

// NewToolWithConfig creates a new tool with custom configuration
func NewToolWithConfig(config *Config) *BaseTool {
	if config == nil {
		config = DefaultConfig()
	}

	// Ensure name is set
	if config.Name == "" {
		config.Name = "truvag3-tool"
	}

	// Generate ID if not set
	if config.ID == "" {
		config.ID = fmt.Sprintf("%s-%s", config.Name, generateID())
	}

	// Track component type for automatic telemetry inference
	SetCurrentComponentType(ComponentTypeTool)

	return &BaseTool{
		ID:                 config.ID,
		Name:               config.Name,
		Type:               ComponentTypeTool,
		Logger:             &NoOpLogger{},
		Telemetry:          &NoOpTelemetry{},
		Config:             config,
		mux:                http.NewServeMux(),
		registeredPatterns: make(map[string]bool), // Initialize pattern tracking
	}
}

// Initialize initializes the tool
func (t *BaseTool) Initialize(ctx context.Context) error {
	t.Logger.Info("Starting tool initialization", map[string]interface{}{
		"id":                t.ID,
		"name":              t.Name,
		"type":              t.Type,
		"config_provided":   t.Config != nil,
		"discovery_enabled": t.Config != nil && t.Config.Discovery.Enabled,
		"namespace":         getNamespaceFromConfig(t.Config),
	})

	// Initialize components based on config (following BaseAgent pattern)
	if t.Config != nil {
		// Initialize registry if configured
		if t.Config.Discovery.Enabled && t.Registry == nil {
			t.Logger.Info("Initializing service registry", map[string]interface{}{
				"provider":  t.Config.Discovery.Provider,
				"mock_mode": t.Config.Development.MockDiscovery,
				"redis_url": t.Config.Discovery.RedisURL != "",
			})

			if t.Config.Development.MockDiscovery {
				// Use mock registry for development
				t.Registry = NewMockDiscovery()
				t.Logger.Info("Using mock registry for development", map[string]interface{}{
					"provider": "mock",
					"reason":   "development_mode",
				})
			} else if t.Config.Discovery.Provider == "redis" && t.Config.Discovery.RedisURL != "" {
				// Initialize Redis registry with configured TTL
				if registry, err := NewRedisRegistryWithOptions(t.Config.Discovery.RedisURL, "truvag3", t.Config.Discovery.TTL); err == nil {
					// Set logger for better observability
					registry.SetLogger(t.Logger)
					t.mu.Lock()
					t.Registry = registry
					t.mu.Unlock()
					t.Logger.Info("Redis registry initialized successfully", map[string]interface{}{
						"provider":      "redis",
						"redis_url":     t.Config.Discovery.RedisURL,
						"effective_ttl": registry.TTL().String(),
						"requested_ttl": t.Config.Discovery.TTL.String(),
					})
				} else {
					// Enhance existing error logging with dependency context
					t.Logger.Error("Failed to initialize Redis registry", map[string]interface{}{
						"error":         err,
						"error_type":    fmt.Sprintf("%T", err),
						"redis_url":     t.Config.Discovery.RedisURL,
						"impact":        "tool_will_run_without_registry",
						"retry_enabled": t.Config.Discovery.RetryOnFailure,
					})

					// Start background retry if enabled
					if t.Config.Discovery.RetryOnFailure {
						address, port := ResolveServiceAddress(t.Config, t.Logger)

						serviceInfo := &ServiceInfo{
							ID:           t.ID,
							Name:         t.Name,
							Type:         ComponentTypeTool,
							Capabilities: t.GetCapabilities(),
							Address:      address,
							Port:         port,
							Metadata:     BuildServiceMetadata(t.Config),
						}

						// Define callback to update registry reference
						onSuccess := func(newRegistry Registry) error {
							t.mu.Lock()
							defer t.mu.Unlock()

							// Stop old heartbeat if exists
							if oldRegistry, ok := t.Registry.(*RedisRegistry); ok && oldRegistry != nil {
								oldRegistry.StopHeartbeat(ctx, t.ID)
							}

							// Update to new registry
							t.Registry = newRegistry
							t.Logger.Info("Registry reference updated", map[string]interface{}{
								"tool_id": t.ID,
							})
							return nil
						}

						// Start background retry manager
						StartRegistryRetry(
							ctx,
							t.Config.Discovery.RedisURL,
							serviceInfo,
							t.Config.Discovery.RetryInterval,
							t.Logger,
							onSuccess,
							t.Config.Discovery.TTL,
							t.Config.Discovery.HeartbeatInterval,
						)

						t.Logger.Info("Background registry retry started", map[string]interface{}{
							"tool_id": t.ID,
						})
					}
				}
			}
		}
	}

	if t.Registry != nil {
		address, port := ResolveServiceAddress(t.Config, t.Logger)

		t.Logger.Info("Attempting service registration", map[string]interface{}{
			"service_id":         t.ID,
			"service_name":       t.Name,
			"resolved_address":   address,
			"resolved_port":      port,
			"capabilities_count": len(t.Capabilities),
			"namespace":          getNamespaceFromConfig(t.Config),
		})

		info := &ServiceInfo{
			ID:           t.ID,
			Name:         t.Name,
			Type:         t.Type,
			Address:      address,
			Port:         port,
			Capabilities: t.Capabilities,
			Health:       HealthHealthy,
			Metadata:     BuildServiceMetadata(t.Config),
		}
		if err := t.Registry.Register(ctx, info); err != nil {
			return fmt.Errorf("failed to register tool: %w", err)
		}

		// Start heartbeat to keep registration alive (Redis-specific)
		if redisRegistry, ok := t.Registry.(*RedisRegistry); ok {
			redisRegistry.StartHeartbeat(ctx, t.ID, t.Config.Discovery.HeartbeatInterval)
			t.Logger.Info("Started heartbeat for tool registration", map[string]interface{}{
				"tool_id":              t.ID,
				"tool_name":            t.Name,
				"ttl_sec":              int(redisRegistry.ttl.Seconds()),
				"heartbeat_configured": t.Config.Discovery.HeartbeatInterval.String(),
			})
		}
	} else {
		t.Logger.Warn("Tool running without service registry", map[string]interface{}{
			"reason":        "registry_not_configured",
			"impact":        "tool_not_discoverable",
			"manual_config": "required_for_service_mesh",
		})
	}

	t.Logger.Info("Tool initialization completed", map[string]interface{}{
		"id":                 t.ID,
		"name":               t.Name,
		"discovery_enabled":  t.Registry != nil,
		"capabilities_count": len(t.Capabilities),
	})

	return nil
}

// GetID returns the tool's unique identifier
func (t *BaseTool) GetID() string {
	return t.ID
}

// GetLogger returns the tool's logger.
// Implements the loggerHaver interface used by getComponentLogger so that
// types embedding *BaseTool inherit access to the logger via interface satisfaction.
func (t *BaseTool) GetLogger() Logger {
	return t.Logger
}

// GetName returns the tool's name
func (t *BaseTool) GetName() string {
	return t.Name
}

// GetCapabilities returns the tool's capabilities
func (t *BaseTool) GetCapabilities() []Capability {
	t.capMutex.RLock()
	defer t.capMutex.RUnlock()

	// Return a copy to prevent external modification
	caps := make([]Capability, len(t.Capabilities))
	copy(caps, t.Capabilities)
	return caps
}

// GetType returns ComponentTypeTool
func (t *BaseTool) GetType() ComponentType {
	return t.Type
}

// RegisterCapability registers a new capability for the tool.
// Follows the same pattern as BaseAgent for consistency.
// If cap.Handler is provided, it will be used instead of the generic handler.
// If cap.Endpoint is empty, it will be auto-generated as /api/capabilities/{name}.
func (t *BaseTool) RegisterCapability(cap Capability) {
	t.capMutex.Lock()
	defer t.capMutex.Unlock()

	// Auto-generate endpoint if not provided (same as Agent)
	if cap.Endpoint == "" {
		cap.Endpoint = fmt.Sprintf("/api/capabilities/%s", cap.Name)
	}

	// Phase 3: Auto-generate schema endpoint if InputSummary is provided
	// This enables on-demand schema fetching for validation
	if cap.InputSummary != nil {
		schemaEndpoint := fmt.Sprintf("%s/schema", cap.Endpoint)
		cap.SchemaEndpoint = schemaEndpoint

		// Register schema endpoint handler
		t.mux.HandleFunc(schemaEndpoint, t.handleSchemaRequest(cap))
		t.registeredPatterns[schemaEndpoint] = true

		t.Logger.Debug("Registered schema endpoint", map[string]interface{}{
			"capability":      cap.Name,
			"schema_endpoint": schemaEndpoint,
		})
	}

	t.Capabilities = append(t.Capabilities, cap)

	// Register HTTP endpoint (same pattern as Agent)
	if cap.Handler != nil {
		// Use custom handler if provided
		t.mux.HandleFunc(cap.Endpoint, cap.Handler)
	} else {
		// Use generic handler with telemetry and logging
		t.mux.HandleFunc(cap.Endpoint, t.handleCapabilityRequest(cap))
	}

	// Track this pattern to prevent duplicates
	t.registeredPatterns[cap.Endpoint] = true

	t.Logger.Info("Registered capability", map[string]interface{}{
		"name":           cap.Name,
		"endpoint":       cap.Endpoint,
		"custom_handler": cap.Handler != nil,
		"has_schema":     cap.InputSummary != nil,
	})
}

// handleCapabilityRequest creates an HTTP handler for a capability.
// This provides a generic handler for capabilities without custom handlers.
func (t *BaseTool) handleCapabilityRequest(cap Capability) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()

		// Start telemetry span if available
		if t.Telemetry != nil {
			var span Span
			_, span = t.Telemetry.StartSpan(ctx, fmt.Sprintf("capability.%s", cap.Name))
			defer span.End()
			span.SetAttribute("capability.name", cap.Name)
			span.SetAttribute("component.type", "tool")
		}

		// Log request
		t.Logger.Info("Handling capability request", map[string]interface{}{
			"capability": cap.Name,
			"method":     r.Method,
			"tool":       t.Name,
		})

		// Since tools are passive and this is a generic handler,
		// we return capability information
		response := map[string]interface{}{
			"capability":   cap.Name,
			"description":  cap.Description,
			"input_types":  cap.InputTypes,
			"output_types": cap.OutputTypes,
			"message":      "This capability is registered but has no custom handler implementation",
		}

		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(response); err != nil {
			// Log error but response is already partially written
			t.Logger.Error("Failed to encode response", map[string]interface{}{
				"error":      err,
				"error_type": fmt.Sprintf("%T", err),
				"tool_id":    t.ID,
				// 🔥 ADD: Request context for troubleshooting
				"request_method":     r.Method,
				"request_path":       r.URL.Path,
				"request_remote":     r.RemoteAddr,
				"capabilities_count": len(t.Capabilities),
				"user_agent":         r.Header.Get("User-Agent"),
				"content_length":     r.ContentLength,
			})
		}
	}
}

// handleSchemaRequest creates an HTTP handler for schema endpoints.
// Part of Phase 3: Returns full JSON Schema v7 generated from InputSummary.
// This enables agents to fetch schemas on-demand for payload validation.
func (t *BaseTool) handleSchemaRequest(cap Capability) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// Only support GET requests for schemas
		if r.Method != http.MethodGet {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}

		// Generate JSON Schema from InputSummary
		schema := t.generateJSONSchema(cap)

		// Return schema as JSON
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(schema); err != nil {
			t.Logger.Error("Failed to encode schema", map[string]interface{}{
				"error":      err,
				"capability": cap.Name,
			})
			http.Error(w, "Internal server error", http.StatusInternalServerError)
			return
		}

		t.Logger.Debug("Schema request served", map[string]interface{}{
			"capability": cap.Name,
			"client":     r.RemoteAddr,
		})
	}
}

// generateJSONSchema generates a JSON Schema v7 document from a Capability's InputSummary.
// Part of Phase 3: Converts compact field hints into full JSON Schema for validation.
func (t *BaseTool) generateJSONSchema(cap Capability) map[string]interface{} {
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
		properties[field.Name] = t.fieldHintToJSONSchema(field)
		required = append(required, field.Name)
	}

	// Add optional fields
	for _, field := range cap.InputSummary.OptionalFields {
		properties[field.Name] = t.fieldHintToJSONSchema(field)
	}

	schema["properties"] = properties
	if len(required) > 0 {
		schema["required"] = required
	}
	schema["additionalProperties"] = false

	return schema
}

// fieldHintToJSONSchema converts a FieldHint to a JSON Schema property definition.
func (t *BaseTool) fieldHintToJSONSchema(field FieldHint) map[string]interface{} {
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

// setupStandardEndpoints adds standard endpoints like /api/capabilities and /health
func (t *BaseTool) setupStandardEndpoints() {
	// Add capabilities listing endpoint (same as Agent)
	capabilitiesPath := "/api/capabilities"
	if !t.registeredPatterns[capabilitiesPath] {
		t.mux.HandleFunc(capabilitiesPath, func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			if err := json.NewEncoder(w).Encode(t.Capabilities); err != nil {
				// Log error but response is already partially written
				t.Logger.Error("Failed to encode capabilities", map[string]interface{}{
					"error":              err,
					"error_type":         fmt.Sprintf("%T", err),
					"tool_id":            t.ID,
					"request_method":     r.Method,
					"request_path":       r.URL.Path,
					"request_remote":     r.RemoteAddr,
					"capabilities_count": len(t.Capabilities),
					"user_agent":         r.Header.Get("User-Agent"),
					"content_length":     r.ContentLength,
				})
			}
		})
		t.registeredPatterns[capabilitiesPath] = true
	}

	// Add health endpoint if enabled (same as Agent)
	if t.Config != nil && t.Config.HTTP.EnableHealthCheck {
		healthPath := t.Config.HTTP.HealthCheckPath
		if healthPath == "" {
			healthPath = "/health"
		}
		if !t.registeredPatterns[healthPath] {
			t.mux.HandleFunc(healthPath, func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusOK)
				if err := json.NewEncoder(w).Encode(map[string]string{
					"status": "healthy",
					"type":   "tool",
					"name":   t.Name,
					"id":     t.ID,
				}); err != nil {
					// Log error but response is already partially written
					t.Logger.Error("Failed to encode health response", map[string]interface{}{
						"error":              err,
						"error_type":         fmt.Sprintf("%T", err),
						"tool_id":            t.ID,
						"request_method":     r.Method,
						"request_path":       r.URL.Path,
						"request_remote":     r.RemoteAddr,
						"capabilities_count": len(t.Capabilities),
						"user_agent":         r.Header.Get("User-Agent"),
						"content_length":     r.ContentLength,
					})
				}
			})
			t.registeredPatterns[healthPath] = true
		}
	}

	// Add OpenAPI spec endpoint — opt-in, disabled by default.
	// See HTTPConfig.EnableOpenAPI godoc for the security rationale:
	// the endpoint reveals every capability and its full schema to any
	// caller that can reach the pod, so it should never be enabled in
	// production without deliberate intent.
	if t.Config != nil && t.Config.HTTP.EnableOpenAPI {
		openapiPath := "/openapi.json"
		if !t.registeredPatterns[openapiPath] {
			t.mux.HandleFunc(openapiPath, openAPIHandler(t.Name, t.Type, func() []Capability {
				return t.Capabilities
			}))
			t.registeredPatterns[openapiPath] = true
		}
	}
}

// Start starts the HTTP server for the tool
func (t *BaseTool) Start(ctx context.Context, port int) error {
	// Apply configuration precedence: explicit parameter > config > default
	// Only use Config.Port if no explicit port provided (port < 0)
	if port < 0 && t.Config != nil && t.Config.Port >= 0 {
		port = t.Config.Port
	}

	// Validate port range (0 is allowed for automatic assignment)
	if port < 0 || port > 65535 {
		t.Logger.Error("Invalid port specified", map[string]interface{}{
			"requested_port": port,
			"valid_range":    "0-65535",
			"port_zero_note": "0_enables_automatic_assignment",
		})
		return fmt.Errorf("invalid port %d: must be between 0-65535 (0 for automatic assignment)", port)
	}

	addr := fmt.Sprintf(":%d", port)

	// Use default timeouts if config is not provided
	if t.Config == nil {
		t.Config = DefaultConfig()
	}

	// Setup standard endpoints (/api/capabilities, /health)
	t.setupStandardEndpoints()

	t.Logger.Info("Configuring HTTP server", map[string]interface{}{
		"port":                 port,
		"cors_enabled":         t.Config.HTTP.CORS.Enabled,
		"health_check_enabled": t.Config.HTTP.EnableHealthCheck,
		"read_timeout":         t.Config.HTTP.ReadTimeout.String(),
		"write_timeout":        t.Config.HTTP.WriteTimeout.String(),
		"registered_endpoints": len(t.registeredPatterns),
	})

	if len(t.registeredPatterns) > 0 {
		endpoints := make([]string, 0, len(t.registeredPatterns))
		for pattern := range t.registeredPatterns {
			endpoints = append(endpoints, pattern)
		}
		t.Logger.Info("HTTP endpoints registered", map[string]interface{}{
			"endpoints":    endpoints,
			"total_count":  len(endpoints),
			"capabilities": len(t.Capabilities),
		})
	}

	// Create handler with middleware stack
	// Order (innermost to outermost): Handler -> Recovery -> Logging -> CORS -> Custom Middleware
	var handler http.Handler = t.mux

	// Always wrap with panic recovery middleware (innermost - catches panics from handler)
	handler = RecoveryMiddleware(t.Logger)(handler)

	// Add request/response logging middleware
	handler = LoggingMiddleware(t.Logger, t.Config.Development.Enabled)(handler)

	// Add CORS middleware if enabled
	if t.Config.HTTP.CORS.Enabled {
		handler = CORSMiddleware(&t.Config.HTTP.CORS)(handler)
	}

	// Apply custom middleware (outermost - applied last, executed first)
	// This enables application-level injection of telemetry middleware (e.g., tracing)
	// without core importing telemetry - following framework design principles.
	// Middleware is applied in reverse order so first middleware in the list is outermost.
	for i := len(t.Config.HTTP.Middleware) - 1; i >= 0; i-- {
		handler = t.Config.HTTP.Middleware[i](handler)
	}

	if len(t.Config.HTTP.Middleware) > 0 {
		t.Logger.Info("Custom middleware applied", map[string]interface{}{
			"middleware_count": len(t.Config.HTTP.Middleware),
		})
	}

	server := &http.Server{
		Addr:              addr,
		Handler:           handler,
		ReadTimeout:       t.Config.HTTP.ReadTimeout,
		ReadHeaderTimeout: t.Config.HTTP.ReadHeaderTimeout,
		WriteTimeout:      t.Config.HTTP.WriteTimeout,
		IdleTimeout:       t.Config.HTTP.IdleTimeout,
		MaxHeaderBytes:    t.Config.HTTP.MaxHeaderBytes,
	}
	t.setServer(server)
	defer t.clearServer(server)

	if t.Registry != nil {
		// Use the shared resolver for proper K8s support
		address, registrationPort := ResolveServiceAddress(t.Config, t.Logger)
		t.Logger.Info("Updating service registration with server details", map[string]interface{}{
			"service_id":           t.ID,
			"registration_address": address,
			"registration_port":    registrationPort,
			"server_port":          port,
		})

		info := &ServiceInfo{
			ID:           t.ID,
			Name:         t.Name,
			Type:         t.Type,
			Address:      address,
			Port:         registrationPort,
			Capabilities: t.Capabilities,
			Health:       HealthHealthy,
			Metadata:     BuildServiceMetadata(t.Config),
		}
		if err := t.Registry.Register(ctx, info); err != nil {
			t.Logger.Error("Failed to update registration", map[string]interface{}{
				"error": err.Error(),
			})
		}
	}

	t.Logger.Info("Starting HTTP server", map[string]interface{}{
		"address":          addr,
		"cors":             t.Config.HTTP.CORS.Enabled,
		"capabilities":     len(t.Capabilities),
		"registry_enabled": t.Registry != nil,
	})

	if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		t.Logger.Error("HTTP server failed to start", map[string]interface{}{
			"error":      err.Error(),
			"error_type": fmt.Sprintf("%T", err),
			"address":    addr,
			"port":       port,
		})
		return err
	}

	return nil
}

// Shutdown gracefully shuts down the tool
func (t *BaseTool) Shutdown(ctx context.Context) error {
	t.Logger.Info("Shutting down tool", map[string]interface{}{
		"name": t.Name,
	})

	// Unregister from registry
	if t.Registry != nil {
		if err := t.Registry.Unregister(ctx, t.ID); err != nil {
			t.Logger.Error("Failed to unregister", map[string]interface{}{
				"error": err.Error(),
			})
		}
	}

	// Shutdown HTTP server
	if server := t.getServer(); server != nil {
		return server.Shutdown(ctx)
	}

	return nil
}

func (t *BaseTool) setServer(server *http.Server) {
	t.serverMu.Lock()
	defer t.serverMu.Unlock()
	t.server = server
}

func (t *BaseTool) getServer() *http.Server {
	t.serverMu.RLock()
	defer t.serverMu.RUnlock()
	return t.server
}

func (t *BaseTool) clearServer(server *http.Server) {
	t.serverMu.Lock()
	defer t.serverMu.Unlock()
	if t.server == server {
		t.server = nil
	}
}
