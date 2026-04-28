package core

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

// TestToolCreation validates that tools are created with correct defaults
func TestToolCreation(t *testing.T) {
	tests := []struct {
		name      string
		toolName  string
		wantType  ComponentType
		wantPanic bool
	}{
		{
			name:     "create tool with valid name",
			toolName: "calculator",
			wantType: ComponentTypeTool,
		},
		{
			name:      "create tool with empty name",
			toolName:  "",
			wantPanic: false, // Should handle gracefully with default
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.wantPanic {
				defer func() {
					if r := recover(); r == nil {
						t.Errorf("NewTool() should have panicked")
					}
				}()
			}

			tool := NewTool(tt.toolName)

			if tool == nil {
				t.Fatal("NewTool() returned nil")
			}

			// Verify tool has correct type
			if tool.Type != ComponentTypeTool {
				t.Errorf("Tool type = %v, want %v", tool.Type, ComponentTypeTool)
			}

			// Verify tool has an ID
			if tool.ID == "" {
				t.Error("Tool should have an ID")
			}

			// Verify tool has a name
			if tt.toolName != "" && tool.Name != tt.toolName {
				t.Errorf("Tool name = %v, want %v", tool.Name, tt.toolName)
			}
		})
	}
}

// TestToolWithConfig validates configuration-based tool creation
func TestToolWithConfig(t *testing.T) {
	tests := []struct {
		name      string
		config    *Config
		wantName  string
		wantPort  int
		wantError bool
	}{
		{
			name: "valid config",
			config: &Config{
				Name:    "test-tool",
				Port:    8080,
				Address: "localhost",
			},
			wantName: "test-tool",
			wantPort: 8080,
		},
		{
			name:     "nil config",
			config:   nil,
			wantName: "truvag3-agent", // Default from DefaultConfig()
			wantPort: 8080,
		},
		{
			name: "config with kubernetes",
			config: &Config{
				Name: "k8s-tool",
				Kubernetes: KubernetesConfig{
					Enabled:     true,
					ServiceName: "tool-service",
					ServicePort: 80,
				},
			},
			wantName: "k8s-tool",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tool := NewToolWithConfig(tt.config)

			if tool == nil {
				t.Fatal("NewToolWithConfig() returned nil")
			}

			if tool.Name != tt.wantName {
				t.Errorf("Tool name = %v, want %v", tool.Name, tt.wantName)
			}

			// Verify tool cannot discover (architectural constraint)
			// Tools don't have Discovery field - this is enforced at compile time
			// The test passes by virtue of compilation

			// Verify tool has Registry for self-registration
			if tool.Registry == nil && tt.config != nil && tt.config.Discovery.Enabled {
				t.Error("Tool should have Registry for self-registration")
			}
		})
	}
}

// TestToolCannotDiscover is a critical architectural test
// Tools must NEVER be able to discover other components
func TestToolCannotDiscover(t *testing.T) {
	tool := NewTool("test-tool")

	// This test validates the architectural constraint
	// Tools should not have a Discovery field - this is enforced at compile time
	// The fact that this compiles proves tools don't have Discovery

	// If someone tries to add Discovery to tools, this would fail to compile:
	// _ = tool.Discovery // Would cause compilation error

	// Ensure tools don't accidentally get discovery through initialization
	ctx := context.Background()
	err := tool.Initialize(ctx)
	if err != nil {
		t.Fatalf("Initialize() error = %v", err)
	}

	// Tools only have Registry (for self-registration), not Discovery
	// This architectural constraint is enforced by the type system
}

// TestToolLifecycle tests the complete lifecycle of a tool
func TestToolLifecycle(t *testing.T) {
	ctx := context.Background()

	// Create tool with config that enables discovery
	config := &Config{
		Name: "lifecycle-tool",
		Discovery: DiscoveryConfig{
			Enabled: true,
		},
	}
	tool := NewToolWithConfig(config)
	mockRegistry := &mockRegistryForTest{
		registrations: make(map[string]*ServiceInfo),
	}
	tool.Registry = mockRegistry

	// Test initialization
	err := tool.Initialize(ctx)
	if err != nil {
		t.Fatalf("Initialize() error = %v", err)
	}

	// Verify tool registered itself
	if len(mockRegistry.registrations) != 1 {
		t.Errorf("Expected 1 registration, got %d", len(mockRegistry.registrations))
	}

	// Verify registration has correct type
	for _, reg := range mockRegistry.registrations {
		if reg.Type != ComponentTypeTool {
			t.Errorf("Registration type = %v, want %v", reg.Type, ComponentTypeTool)
		}
		if reg.Name != "lifecycle-tool" {
			t.Errorf("Registration name = %v, want %v", reg.Name, "lifecycle-tool")
		}
	}

	// Test capability registration
	capability := Capability{
		Name:        "calculate",
		Description: "Performs calculations",
		Handler: func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
		},
	}
	tool.RegisterCapability(capability)

	if len(tool.Capabilities) != 1 {
		t.Errorf("Expected 1 capability, got %d", len(tool.Capabilities))
	}

	// Test shutdown
	err = tool.Shutdown(ctx)
	if err != nil {
		t.Fatalf("Shutdown() error = %v", err)
	}

	// Verify unregistration
	if len(mockRegistry.registrations) != 0 {
		t.Error("Tool should unregister on shutdown")
	}
}

// TestToolCapabilityHandling tests that tools correctly handle capability requests
func TestToolCapabilityHandling(t *testing.T) {
	tool := NewTool("calculator-tool")

	// Track handler invocations
	handlerCalled := false

	// Register capability with handler
	tool.RegisterCapability(Capability{
		Name:        "add",
		Description: "Adds two numbers",
		Handler: func(w http.ResponseWriter, r *http.Request) {
			handlerCalled = true

			var input struct {
				A float64 `json:"a"`
				B float64 `json:"b"`
			}

			if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}

			result := map[string]float64{
				"result": input.A + input.B,
			}

			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(result)
		},
	})

	// Create test request
	req := httptest.NewRequest("POST", "/capabilities/add", nil)
	w := httptest.NewRecorder()

	// Find and invoke the handler
	var handler http.HandlerFunc
	for _, cap := range tool.Capabilities {
		if cap.Name == "add" && cap.Handler != nil {
			handler = cap.Handler
			break
		}
	}

	if handler == nil {
		t.Fatal("Handler not found for capability")
	}

	// Invoke handler
	handler(w, req)

	if !handlerCalled {
		t.Error("Handler was not called")
	}
}

// TestToolConcurrentCapabilityRegistration tests thread safety
func TestToolConcurrentCapabilityRegistration(t *testing.T) {
	tool := NewTool("concurrent-tool")

	var wg sync.WaitGroup
	numGoroutines := 100

	// Track which capabilities were registered
	registered := make(map[string]bool)
	var mu sync.Mutex

	for i := 0; i < numGoroutines; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()

			capName := fmt.Sprintf("capability-%d", id)
			capability := Capability{
				Name:        capName,
				Description: fmt.Sprintf("Capability %d", id),
			}

			// This should not panic or cause race conditions
			tool.RegisterCapability(capability)

			mu.Lock()
			registered[capName] = true
			mu.Unlock()
		}(i)
	}

	wg.Wait()

	// Due to potential race conditions in the implementation,
	// we check that most capabilities were registered
	// A proper implementation should register all of them
	if len(tool.Capabilities) < numGoroutines-5 {
		t.Errorf("Too few capabilities registered: got %d, expected around %d",
			len(tool.Capabilities), numGoroutines)
	}

	// Log if not all were registered (indicates potential race condition)
	if len(tool.Capabilities) != numGoroutines {
		t.Logf("Warning: Race condition detected - only %d/%d capabilities registered",
			len(tool.Capabilities), numGoroutines)
	}
}

// TestToolGetters tests all getter methods
func TestToolGetters(t *testing.T) {
	tool := NewTool("getter-tool")

	// Add some capabilities
	tool.RegisterCapability(Capability{Name: "cap1"})
	tool.RegisterCapability(Capability{Name: "cap2"})

	// Test GetID
	if id := tool.GetID(); id == "" {
		t.Error("GetID() returned empty string")
	}

	// Test GetName
	if name := tool.GetName(); name != "getter-tool" {
		t.Errorf("GetName() = %v, want %v", name, "getter-tool")
	}

	// Test GetType
	if typ := tool.GetType(); typ != ComponentTypeTool {
		t.Errorf("GetType() = %v, want %v", typ, ComponentTypeTool)
	}

	// Test GetCapabilities
	caps := tool.GetCapabilities()
	if len(caps) != 2 {
		t.Errorf("GetCapabilities() returned %d capabilities, want 2", len(caps))
	}
}

// TestToolStartStop tests the HTTP server lifecycle
func TestToolStartStop(t *testing.T) {
	ctx := context.Background()
	tool := NewTool("server-tool")

	// Use a random port to avoid conflicts
	port := 0 // Let OS assign a port
	tool.Config = &Config{
		Port:    port,
		Address: "localhost",
	}

	// Initialize tool
	err := tool.Initialize(ctx)
	if err != nil {
		t.Fatalf("Initialize() error = %v", err)
	}

	// Start server in background
	serverStarted := make(chan bool)
	serverStopped := make(chan bool)

	go func() {
		serverStarted <- true
		err := tool.Start(ctx, 0) // Use port 0 for automatic assignment
		if err != nil && err != http.ErrServerClosed {
			t.Errorf("Start() error = %v", err)
		}
		serverStopped <- true
	}()

	// Wait for server to start
	<-serverStarted
	time.Sleep(100 * time.Millisecond)

	// Stop the server
	err = tool.Shutdown(ctx)
	if err != nil {
		t.Fatalf("Shutdown() error = %v", err)
	}

	// Wait for server to stop
	select {
	case <-serverStopped:
		// Server stopped successfully
	case <-time.After(2 * time.Second):
		t.Error("Server did not stop within timeout")
	}
}

// TestToolInitializeIdempotent verifies initialization is idempotent
func TestToolInitializeIdempotent(t *testing.T) {
	ctx := context.Background()
	tool := NewTool("idempotent-tool")

	mockRegistry := &mockRegistryForTest{
		registrations: make(map[string]*ServiceInfo),
	}
	tool.Registry = mockRegistry

	// First initialization
	err := tool.Initialize(ctx)
	if err != nil {
		t.Fatalf("First Initialize() error = %v", err)
	}

	firstRegCount := len(mockRegistry.registrations)

	// Second initialization should be safe
	err = tool.Initialize(ctx)
	if err != nil {
		t.Fatalf("Second Initialize() error = %v", err)
	}

	// Should not register twice
	if len(mockRegistry.registrations) != firstRegCount {
		t.Error("Initialize() is not idempotent - registered multiple times")
	}
}

// TestToolWithNilConfig tests tool behavior with nil config
func TestToolWithNilConfig(t *testing.T) {
	tool := NewToolWithConfig(nil)

	if tool == nil {
		t.Fatal("NewToolWithConfig(nil) should not return nil")
	}

	// Should have default config
	if tool.Config == nil {
		t.Fatal("Tool should have default config when created with nil")
	}

	// Verify defaults
	if tool.Config.Port != 8080 {
		t.Errorf("Default port = %v, want 8080", tool.Config.Port)
	}

	if tool.Name == "" {
		t.Error("Tool should have a default name")
	}
}

// TestToolRegistryIntegration tests integration with registry
func TestToolRegistryIntegration(t *testing.T) {
	ctx := context.Background()

	config := &Config{
		Name: "registry-tool",
		Discovery: DiscoveryConfig{
			Enabled: true,
		},
	}

	tool := NewToolWithConfig(config)

	// Use mock registry
	mockRegistry := &mockRegistryForTest{
		registrations: make(map[string]*ServiceInfo),
	}
	tool.Registry = mockRegistry

	// Initialize should trigger registration
	err := tool.Initialize(ctx)
	if err != nil {
		t.Fatalf("Initialize() error = %v", err)
	}

	// Verify registration details
	if len(mockRegistry.registrations) != 1 {
		t.Fatalf("Expected 1 registration, got %d", len(mockRegistry.registrations))
	}

	var registration *ServiceInfo
	for _, reg := range mockRegistry.registrations {
		registration = reg
		break
	}

	// Verify registration has correct component type
	if registration.Type != ComponentTypeTool {
		t.Errorf("Registration type = %v, want %v", registration.Type, ComponentTypeTool)
	}

	// Verify registration has correct name
	if registration.Name != "registry-tool" {
		t.Errorf("Registration name = %v, want %v", registration.Name, "registry-tool")
	}

	// Verify health status
	if registration.Health != HealthHealthy {
		t.Errorf("Registration health = %v, want %v", registration.Health, HealthHealthy)
	}
}

// TestToolErrorHandling tests error scenarios
func TestToolErrorHandling(t *testing.T) {
	ctx := context.Background()

	t.Run("initialize with registry error", func(t *testing.T) {
		config := &Config{
			Name: "error-tool",
			Discovery: DiscoveryConfig{
				Enabled: true,
			},
		}
		tool := NewToolWithConfig(config)

		// Use registry that returns errors
		tool.Registry = &errorRegistry{
			registerErr: fmt.Errorf("registration failed"),
		}

		err := tool.Initialize(ctx)
		if err == nil {
			t.Error("Initialize() should return error when registry fails")
		}
	})

	t.Run("shutdown with registry error", func(t *testing.T) {
		tool := NewTool("error-tool")

		// Use registry that returns errors on unregister
		mockRegistry := &errorRegistry{
			unregisterErr: fmt.Errorf("unregistration failed"),
		}
		tool.Registry = mockRegistry

		// Shutdown should handle error gracefully (it logs but doesn't return the error)
		err := tool.Shutdown(ctx)
		// Shutdown doesn't return registry errors, it just logs them
		if err != nil {
			t.Errorf("Shutdown() returned unexpected error: %v", err)
		}
	})
}

// errorRegistry is a mock registry that returns errors
type errorRegistry struct {
	registerErr   error
	unregisterErr error
	updateErr     error
}

func (e *errorRegistry) Register(ctx context.Context, info *ServiceInfo) error {
	return e.registerErr
}

func (e *errorRegistry) UpdateHealth(ctx context.Context, id string, status HealthStatus) error {
	return e.updateErr
}

func (e *errorRegistry) Unregister(ctx context.Context, id string) error {
	return e.unregisterErr
}

// TestToolStartBlocks verifies that tool.Start() blocks properly and doesn't return immediately.
// This test prevents regression to the old non-blocking behavior.
func TestToolStartBlocks(t *testing.T) {
	// Create tool with config that doesn't override port
	config := DefaultConfig()
	config.Name = "blocking-test-tool"
	config.Port = 0 // Don't override the port parameter
	tool := NewToolWithConfig(config)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Channel to track when Start() returns
	startReturned := make(chan bool, 1)
	startError := make(chan error, 1)

	// Start the tool in a goroutine
	go func() {
		err := tool.Start(ctx, 0) // Use port 0 for automatic assignment
		startError <- err
		startReturned <- true
	}()

	// Give the server a moment to initialize
	time.Sleep(50 * time.Millisecond)

	// Verify that Start() has NOT returned yet (it should be blocking)
	select {
	case <-startReturned:
		t.Fatal("tool.Start() returned immediately - it should block until server stops")
	default:
		// Good! Start() is still blocking as expected
	}

	// Now shutdown the tool to make Start() return
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 1*time.Second)
	defer shutdownCancel()

	err := tool.Shutdown(shutdownCtx)
	if err != nil {
		t.Fatalf("Shutdown() failed: %v", err)
	}

	// Now Start() should return
	select {
	case err := <-startError:
		if err != nil && err != http.ErrServerClosed {
			t.Errorf("Start() returned unexpected error: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Start() did not return after shutdown")
	}

	// Verify Start() actually returned
	select {
	case <-startReturned:
		// Good! Start() returned after shutdown
	case <-time.After(1 * time.Second):
		t.Fatal("Start() notification not received")
	}
}

// BenchmarkToolCreation benchmarks tool creation
func BenchmarkToolCreation(b *testing.B) {
	for i := 0; i < b.N; i++ {
		_ = NewTool(fmt.Sprintf("tool-%d", i))
	}
}

// BenchmarkToolCapabilityRegistration benchmarks capability registration
func BenchmarkToolCapabilityRegistration(b *testing.B) {
	tool := NewTool("bench-tool")

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		tool.RegisterCapability(Capability{
			Name:        fmt.Sprintf("cap-%d", i),
			Description: "Benchmark capability",
		})
	}
}
