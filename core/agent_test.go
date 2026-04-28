package core

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// TestHandleFunc tests the new HandleFunc method
func TestHandleFunc(t *testing.T) {
	tests := []struct {
		name        string
		setupFunc   func(*BaseAgent) error
		pattern     string
		handler     http.HandlerFunc
		wantErr     bool
		errContains string
	}{
		{
			name:    "successful registration",
			pattern: "/api/test",
			handler: func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusOK)
			},
			wantErr: false,
		},
		{
			name: "duplicate pattern registration",
			setupFunc: func(agent *BaseAgent) error {
				return agent.HandleFunc("/api/duplicate", func(w http.ResponseWriter, r *http.Request) {})
			},
			pattern: "/api/duplicate",
			handler: func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusOK)
			},
			wantErr:     true,
			errContains: "handler already registered",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			agent := NewBaseAgent("test-agent")

			// Run setup if provided
			if tt.setupFunc != nil {
				if err := tt.setupFunc(agent); err != nil {
					t.Fatalf("setup failed: %v", err)
				}
			}

			// Test HandleFunc
			err := agent.HandleFunc(tt.pattern, tt.handler)

			if tt.wantErr {
				if err == nil {
					t.Errorf("HandleFunc() expected error but got none")
				} else if tt.errContains != "" && !strings.Contains(err.Error(), tt.errContains) {
					t.Errorf("HandleFunc() error = %v, want error containing %v", err, tt.errContains)
				}
			} else {
				if err != nil {
					t.Errorf("HandleFunc() unexpected error = %v", err)
				}
			}
		})
	}
}

// TestHandleFuncAfterStart tests that HandleFunc fails after server starts
func TestHandleFuncAfterStart(t *testing.T) {
	agent := NewBaseAgent("test-agent")

	// Start server in background
	go func() {
		_ = agent.Start(context.Background(), 8092) // Use a specific port
	}()

	// Give server time to start
	time.Sleep(100 * time.Millisecond)

	// Try to register handler after start
	err := agent.HandleFunc("/api/late", func(w http.ResponseWriter, r *http.Request) {})

	if err == nil {
		t.Errorf("HandleFunc() should fail after server starts")
	} else if !strings.Contains(err.Error(), "server already started") {
		t.Errorf("HandleFunc() error = %v, want error containing 'server already started'", err)
	}

	// Clean up
	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
	defer cancel()
	_ = agent.Stop(ctx)
}

// TestHandleFuncIntegration tests that registered handlers actually work
func TestHandleFuncIntegration(t *testing.T) {
	agent := NewBaseAgent("test-agent")

	// Register a custom handler
	called := false
	err := agent.HandleFunc("/api/custom", func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("custom response"))
	})

	if err != nil {
		t.Fatalf("HandleFunc() failed: %v", err)
	}

	// Test the handler directly through the mux
	req := httptest.NewRequest("GET", "/api/custom", nil)
	rec := httptest.NewRecorder()

	agent.mux.ServeHTTP(rec, req)

	if !called {
		t.Errorf("Custom handler was not called")
	}

	if rec.Code != http.StatusOK {
		t.Errorf("Expected status %d, got %d", http.StatusOK, rec.Code)
	}

	if rec.Body.String() != "custom response" {
		t.Errorf("Expected body 'custom response', got '%s'", rec.Body.String())
	}
}

// TestHandleFuncThreadSafety tests concurrent handler registration
func TestHandleFuncThreadSafety(t *testing.T) {
	agent := NewBaseAgent("test-agent")

	// Try to register handlers concurrently
	errors := make(chan error, 10)

	for i := 0; i < 10; i++ {
		go func(n int) {
			pattern := fmt.Sprintf("/api/concurrent%d", n)
			err := agent.HandleFunc(pattern, func(w http.ResponseWriter, r *http.Request) {})
			errors <- err
		}(i)
	}

	// Collect results
	for i := 0; i < 10; i++ {
		if err := <-errors; err != nil {
			t.Errorf("Concurrent registration failed: %v", err)
		}
	}

	// Verify all patterns were registered
	if len(agent.registeredPatterns) < 10 {
		t.Errorf("Expected at least 10 registered patterns, got %d", len(agent.registeredPatterns))
	}
}

// TestCapabilityWithHandler tests the new Handler field in Capability
func TestCapabilityWithHandler(t *testing.T) {
	agent := NewBaseAgent("test-agent")

	// Test 1: Capability with custom handler
	customCalled := false
	customHandler := func(w http.ResponseWriter, r *http.Request) {
		customCalled = true
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("custom capability response"))
	}

	agent.RegisterCapability(Capability{
		Name:        "custom_cap",
		Description: "Custom capability",
		Handler:     customHandler,
		InputTypes:  []string{"string"},
		OutputTypes: []string{"string"},
	})

	// Verify endpoint was auto-generated
	if len(agent.Capabilities) != 1 {
		t.Errorf("Expected 1 capability, got %d", len(agent.Capabilities))
	}

	cap := agent.Capabilities[0]
	expectedEndpoint := "/api/capabilities/custom_cap"
	if cap.Endpoint != expectedEndpoint {
		t.Errorf("Expected endpoint %s, got %s", expectedEndpoint, cap.Endpoint)
	}

	// Test the custom handler is actually used
	req := httptest.NewRequest("POST", expectedEndpoint, strings.NewReader(`{"test": "data"}`))
	rec := httptest.NewRecorder()

	agent.mux.ServeHTTP(rec, req)

	if !customCalled {
		t.Errorf("Custom handler was not called")
	}

	if rec.Code != http.StatusOK {
		t.Errorf("Expected status %d, got %d", http.StatusOK, rec.Code)
	}

	if rec.Body.String() != "custom capability response" {
		t.Errorf("Expected custom response, got '%s'", rec.Body.String())
	}
}

// TestCapabilityBackwardCompatibility tests that capabilities without Handler still work
func TestCapabilityBackwardCompatibility(t *testing.T) {
	agent := NewBaseAgent("test-agent")

	// Register capability without handler (old style)
	agent.RegisterCapability(Capability{
		Name:        "generic_cap",
		Description: "Generic capability",
		Endpoint:    "/api/test",
		InputTypes:  []string{"number"},
		OutputTypes: []string{"number"},
	})

	// Verify capability was registered
	if len(agent.Capabilities) != 1 {
		t.Errorf("Expected 1 capability, got %d", len(agent.Capabilities))
	}

	cap := agent.Capabilities[0]
	if cap.Endpoint != "/api/test" {
		t.Errorf("Expected endpoint /api/test, got %s", cap.Endpoint)
	}

	// Test that generic handler is used
	req := httptest.NewRequest("POST", "/api/test", strings.NewReader(`{"value": 42}`))
	rec := httptest.NewRecorder()

	agent.mux.ServeHTTP(rec, req)

	// Generic handler returns 200 with JSON response
	if rec.Code != http.StatusOK {
		t.Errorf("Expected status %d, got %d", http.StatusOK, rec.Code)
	}

	// Check response contains capability name
	if !strings.Contains(rec.Body.String(), "generic_cap") {
		t.Errorf("Response should contain capability name, got: %s", rec.Body.String())
	}
}

// TestCapabilityWithExplicitEndpoint tests capability with both Handler and explicit Endpoint
func TestCapabilityWithExplicitEndpoint(t *testing.T) {
	agent := NewBaseAgent("test-agent")

	called := false
	handler := func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusCreated)
	}

	agent.RegisterCapability(Capability{
		Name:        "explicit_cap",
		Description: "Capability with explicit endpoint",
		Endpoint:    "/custom/endpoint",
		Handler:     handler,
	})

	// Verify explicit endpoint is preserved
	cap := agent.Capabilities[0]
	if cap.Endpoint != "/custom/endpoint" {
		t.Errorf("Expected endpoint /custom/endpoint, got %s", cap.Endpoint)
	}

	// Test handler at explicit endpoint
	req := httptest.NewRequest("GET", "/custom/endpoint", nil)
	rec := httptest.NewRecorder()

	agent.mux.ServeHTTP(rec, req)

	if !called {
		t.Errorf("Handler was not called at explicit endpoint")
	}

	if rec.Code != http.StatusCreated {
		t.Errorf("Expected status %d, got %d", http.StatusCreated, rec.Code)
	}
}

// TestCapabilityJSONSerialization tests that Handler field is excluded from JSON
func TestCapabilityJSONSerialization(t *testing.T) {
	cap := Capability{
		Name:        "test",
		Description: "Test capability",
		Endpoint:    "/api/test",
		InputTypes:  []string{"string"},
		OutputTypes: []string{"string"},
		Handler: func(w http.ResponseWriter, r *http.Request) {
			// This should not be serialized
		},
	}

	data, err := json.Marshal(cap)
	if err != nil {
		t.Fatalf("Failed to marshal capability: %v", err)
	}

	// Check that Handler is not in JSON
	jsonStr := string(data)
	if strings.Contains(jsonStr, "handler") || strings.Contains(jsonStr, "Handler") {
		t.Errorf("Handler field should not be in JSON: %s", jsonStr)
	}

	// Verify other fields are present
	var decoded map[string]interface{}
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("Failed to unmarshal: %v", err)
	}

	if decoded["name"] != "test" {
		t.Errorf("Name not preserved in JSON")
	}

	if decoded["endpoint"] != "/api/test" {
		t.Errorf("Endpoint not preserved in JSON")
	}
}

// TestMultipleCapabilitiesWithMixedHandlers tests mixing custom and generic handlers
func TestMultipleCapabilitiesWithMixedHandlers(t *testing.T) {
	agent := NewBaseAgent("test-agent")

	customCalled := false

	// Register capability with custom handler
	agent.RegisterCapability(Capability{
		Name: "custom",
		Handler: func(w http.ResponseWriter, r *http.Request) {
			customCalled = true
			w.WriteHeader(http.StatusOK)
		},
	})

	// Register capability without handler (generic)
	agent.RegisterCapability(Capability{
		Name: "generic",
	})

	// Verify both registered
	if len(agent.Capabilities) != 2 {
		t.Errorf("Expected 2 capabilities, got %d", len(agent.Capabilities))
	}

	// Test custom handler
	req1 := httptest.NewRequest("POST", "/api/capabilities/custom", strings.NewReader("{}"))
	rec1 := httptest.NewRecorder()
	agent.mux.ServeHTTP(rec1, req1)

	if !customCalled {
		t.Errorf("Custom handler not called")
	}

	// Test generic handler
	req2 := httptest.NewRequest("POST", "/api/capabilities/generic", strings.NewReader("{}"))
	rec2 := httptest.NewRecorder()
	agent.mux.ServeHTTP(rec2, req2)

	if rec2.Code != http.StatusOK {
		t.Errorf("Generic handler failed")
	}

	// Generic handler should return JSON with capability name
	if !strings.Contains(rec2.Body.String(), "generic") {
		t.Errorf("Generic handler response missing capability name")
	}
}
