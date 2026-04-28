package core

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestResolveServiceAddressForAgent tests the address determination logic used by agents.
func TestResolveServiceAddressForAgent(t *testing.T) {
	tests := []struct {
		name           string
		config         *Config
		expectedAddr   string
		expectedPort   int
		expectContains string // For cases where exact match isn't predictable
	}{
		{
			name: "config with explicit address and port",
			config: &Config{
				Address: "192.168.1.100",
				Port:    8080,
			},
			expectedAddr: "192.168.1.100",
			expectedPort: 8080,
		},
		{
			name: "config with localhost",
			config: &Config{
				Address: "localhost",
				Port:    9090,
			},
			expectedAddr: "localhost",
			expectedPort: 9090,
		},
		{
			name: "config with 0.0.0.0 address",
			config: &Config{
				Address: "0.0.0.0",
				Port:    3000,
			},
			expectedAddr: "0.0.0.0",
			expectedPort: 3000,
		},
		{
			name: "config with kubernetes environment",
			config: &Config{
				Address:   "0.0.0.0",
				Port:      8080,
				Namespace: "test-namespace",
				Kubernetes: KubernetesConfig{
					Enabled: true,
				},
			},
			expectedAddr: "0.0.0.0",
			expectedPort: 8080,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			agent := &BaseAgent{
				Config: tt.config,
				Logger: &MockLogger{entries: make([]LogEntry, 0)},
			}

			addr, port := ResolveServiceAddress(agent.Config, agent.Logger)

			if tt.expectContains != "" {
				if !strings.Contains(addr, tt.expectContains) {
					t.Errorf("ResolveServiceAddress() address = %q, want to contain %q", addr, tt.expectContains)
				}
			} else {
				if addr != tt.expectedAddr {
					t.Errorf("ResolveServiceAddress() address = %q, want %q", addr, tt.expectedAddr)
				}
			}

			if port != tt.expectedPort {
				t.Errorf("ResolveServiceAddress() port = %d, want %d", port, tt.expectedPort)
			}
		})
	}
}

// TestBaseAgent_Initialize tests the Initialize method with different configurations
func TestBaseAgent_Initialize(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode (Redis connection attempts)")
	}
	ctx := context.Background()

	t.Run("initialize with nil config", func(t *testing.T) {
		agent := &BaseAgent{
			ID:     "test-agent",
			Name:   "Test Agent",
			Config: nil,
			Logger: &MockLogger{entries: make([]LogEntry, 0)},
		}

		err := agent.Initialize(ctx)
		if err != nil {
			t.Errorf("Initialize() with nil config should not error, got: %v", err)
		}

		// Should have no discovery or memory initialized
		if agent.Discovery != nil {
			t.Error("Discovery should be nil with nil config")
		}
		if agent.Memory != nil {
			t.Error("Memory should be nil with nil config")
		}
	})

	t.Run("initialize with mock discovery enabled", func(t *testing.T) {
		config := DefaultConfig()
		config.Discovery.Enabled = true
		config.Development.MockDiscovery = true

		agent := &BaseAgent{
			ID:     "test-agent",
			Name:   "Test Agent",
			Config: config,
			Logger: &MockLogger{entries: make([]LogEntry, 0)},
		}

		err := agent.Initialize(ctx)
		if err != nil {
			t.Errorf("Initialize() with mock discovery should not error, got: %v", err)
		}

		// Should have mock discovery initialized
		if agent.Discovery == nil {
			t.Error("Discovery should be initialized with mock discovery enabled")
		}
	})

	t.Run("initialize with discovery disabled", func(t *testing.T) {
		config := DefaultConfig()
		config.Discovery.Enabled = false

		agent := &BaseAgent{
			ID:     "test-agent",
			Name:   "Test Agent",
			Config: config,
			Logger: &MockLogger{entries: make([]LogEntry, 0)},
		}

		err := agent.Initialize(ctx)
		if err != nil {
			t.Errorf("Initialize() with discovery disabled should not error, got: %v", err)
		}

		// Should have no discovery
		if agent.Discovery != nil {
			t.Error("Discovery should be nil with discovery disabled")
		}
	})

	t.Run("initialize with redis discovery (will fail without redis)", func(t *testing.T) {
		config := DefaultConfig()
		config.Discovery.Enabled = true
		config.Discovery.Provider = "redis"
		config.Discovery.RedisURL = "redis://localhost:6379"
		config.Development.MockDiscovery = false

		agent := &BaseAgent{
			ID:     "test-agent",
			Name:   "Test Agent",
			Config: config,
			Logger: &MockLogger{entries: make([]LogEntry, 0)},
		}

		err := agent.Initialize(ctx)
		if err != nil {
			t.Errorf("Initialize() should not error even if Redis discovery fails, got: %v", err)
		}

		// Redis discovery will fail, so Discovery should remain nil
		// This is expected behavior - agent continues without discovery
		if agent.Discovery != nil {
			t.Log("Note: Redis discovery succeeded (Redis might be running)")
		}
	})

	t.Run("initialize with existing discovery", func(t *testing.T) {
		config := DefaultConfig()
		config.Discovery.Enabled = true
		config.Development.MockDiscovery = true

		existingDiscovery := NewMockDiscovery()
		agent := &BaseAgent{
			ID:        "test-agent",
			Name:      "Test Agent",
			Config:    config,
			Logger:    &MockLogger{entries: make([]LogEntry, 0)},
			Discovery: existingDiscovery,
		}

		err := agent.Initialize(ctx)
		if err != nil {
			t.Errorf("Initialize() should not error, got: %v", err)
		}

		// Should keep existing discovery
		if agent.Discovery != existingDiscovery {
			t.Error("Should keep existing discovery instance")
		}
	})
}

// TestRecoveryMiddleware tests the panic recovery middleware
func TestRecoveryMiddleware(t *testing.T) {
	t.Run("middleware with logger handles panic", func(t *testing.T) {
		mockLogger := &MockLogger{entries: make([]LogEntry, 0)}

		// Create a handler that panics
		panicHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			panic("test panic")
		})

		// Wrap with recovery middleware
		recoveryMiddleware := RecoveryMiddleware(mockLogger)
		recoveredHandler := recoveryMiddleware(panicHandler)

		// Test the middleware
		req := httptest.NewRequest("GET", "/test", nil)
		recorder := httptest.NewRecorder()

		// Should not panic
		recoveredHandler.ServeHTTP(recorder, req)

		// Should return 500
		if recorder.Code != http.StatusInternalServerError {
			t.Errorf("Expected status %d, got %d", http.StatusInternalServerError, recorder.Code)
		}

		// Should log the panic
		if len(mockLogger.entries) == 0 {
			t.Error("Expected panic to be logged")
		} else {
			entry := mockLogger.entries[0]
			if entry.Level != "error" {
				t.Errorf("Expected error log level, got %s", entry.Level)
			}
			if !strings.Contains(entry.Message, "panic recovered") {
				t.Errorf("Expected panic recovery message, got %s", entry.Message)
			}
		}
	})

	t.Run("middleware without logger handles panic", func(t *testing.T) {
		// Create a handler that panics
		panicHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			panic("test panic without logger")
		})

		// Wrap with recovery middleware (no logger)
		recoveryMiddleware := RecoveryMiddleware(nil)
		recoveredHandler := recoveryMiddleware(panicHandler)

		// Test the middleware
		req := httptest.NewRequest("POST", "/api/test", nil)
		recorder := httptest.NewRecorder()

		// Should not panic
		recoveredHandler.ServeHTTP(recorder, req)

		// Should return 500
		if recorder.Code != http.StatusInternalServerError {
			t.Errorf("Expected status %d, got %d", http.StatusInternalServerError, recorder.Code)
		}

		// Should have error message in body
		if !strings.Contains(recorder.Body.String(), "Internal Server Error") {
			t.Error("Expected error message in response body")
		}
	})

	t.Run("middleware allows normal requests", func(t *testing.T) {
		mockLogger := &MockLogger{entries: make([]LogEntry, 0)}

		// Create a normal handler
		normalHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
			w.Write([]byte("success"))
		})

		// Wrap with recovery middleware
		recoveryMiddleware := RecoveryMiddleware(mockLogger)
		recoveredHandler := recoveryMiddleware(normalHandler)

		// Test the middleware
		req := httptest.NewRequest("GET", "/normal", nil)
		recorder := httptest.NewRecorder()

		recoveredHandler.ServeHTTP(recorder, req)

		// Should return 200
		if recorder.Code != http.StatusOK {
			t.Errorf("Expected status %d, got %d", http.StatusOK, recorder.Code)
		}

		// Should have success message
		if recorder.Body.String() != "success" {
			t.Errorf("Expected 'success', got %s", recorder.Body.String())
		}

		// Should not log anything
		if len(mockLogger.entries) != 0 {
			t.Error("Expected no log entries for normal request")
		}
	})

	t.Run("middleware captures different panic types", func(t *testing.T) {
		mockLogger := &MockLogger{entries: make([]LogEntry, 0)}
		recoveryMiddleware := RecoveryMiddleware(mockLogger)

		tests := []struct {
			name      string
			panicVal  interface{}
			expectLog bool
		}{
			{"string panic", "string error", true},
			{"error panic", fmt.Errorf("error panic"), true},
			{"int panic", 42, true},
			{"nil panic", nil, true},
		}

		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				// Reset logger
				mockLogger.entries = make([]LogEntry, 0)

				panicHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					panic(tt.panicVal)
				})

				recoveredHandler := recoveryMiddleware(panicHandler)
				req := httptest.NewRequest("GET", "/panic", nil)
				recorder := httptest.NewRecorder()

				recoveredHandler.ServeHTTP(recorder, req)

				if recorder.Code != http.StatusInternalServerError {
					t.Errorf("Expected status %d, got %d", http.StatusInternalServerError, recorder.Code)
				}

				if tt.expectLog && len(mockLogger.entries) == 0 {
					t.Error("Expected panic to be logged")
				}
			})
		}
	})
}

// NOTE: Framework.Run tests removed because Framework interface structure
// is different in current codebase. Focus on BaseAgent methods that exist.
