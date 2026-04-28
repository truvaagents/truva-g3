package ai

import (
	"fmt"
	"testing"
)

// TestMustRegister tests the panic-based registration function
func TestMustRegister(t *testing.T) {
	// Clear registry for testing
	registry.mu.Lock()
	registry.providers = make(map[string]ProviderFactory)
	registry.mu.Unlock()

	tests := []struct {
		name        string
		factory     ProviderFactory
		shouldPanic bool
	}{
		{
			name: "successful registration",
			factory: &MockProviderFactory{
				name:        "test-provider",
				description: "Test Provider",
			},
			shouldPanic: false,
		},
		{
			name:        "nil factory should panic",
			factory:     nil,
			shouldPanic: true,
		},
		{
			name: "empty name should panic",
			factory: &MockProviderFactory{
				name:        "",
				description: "Empty name",
			},
			shouldPanic: true,
		},
		{
			name: "duplicate provider should panic",
			factory: &MockProviderFactory{
				name:        "test-provider", // Same as first test
				description: "Duplicate",
			},
			shouldPanic: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			defer func() {
				r := recover()
				if tt.shouldPanic && r == nil {
					t.Error("Expected MustRegister to panic but it didn't")
				}
				if !tt.shouldPanic && r != nil {
					t.Errorf("Expected MustRegister not to panic but it did: %v", r)
				}
			}()

			MustRegister(tt.factory)
		})
	}
}

// TestRegistryThreadSafety tests concurrent access to registry
func TestRegistryThreadSafety(t *testing.T) {
	// Clear registry for testing
	registry.mu.Lock()
	registry.providers = make(map[string]ProviderFactory)
	registry.mu.Unlock()

	// Register a test provider
	testFactory := &MockProviderFactory{
		name:        "thread-safe-provider",
		description: "Thread Safe Provider",
		priority:    50,
		available:   true,
	}
	err := Register(testFactory)
	if err != nil {
		t.Fatalf("Failed to register test provider: %v", err)
	}

	// Test concurrent reads
	done := make(chan bool)
	errors := make(chan error, 10)

	// Start multiple goroutines doing read operations
	for i := 0; i < 10; i++ {
		go func() {
			defer func() { done <- true }()

			// Test GetProvider
			if _, exists := GetProvider("thread-safe-provider"); !exists {
				errors <- fmt.Errorf("GetProvider failed to find provider")
				return
			}

			// Test ListProviders
			providers := ListProviders()
			if len(providers) == 0 {
				errors <- fmt.Errorf("ListProviders returned empty list")
				return
			}

			// Test GetProviderInfo
			info := GetProviderInfo()
			if len(info) == 0 {
				errors <- fmt.Errorf("GetProviderInfo returned empty list")
				return
			}

			// Test detectBestProvider
			if _, err := detectBestProvider(nil); err != nil {
				errors <- fmt.Errorf("detectBestProvider failed: %v", err)
				return
			}
		}()
	}

	// Wait for all goroutines to complete
	for i := 0; i < 10; i++ {
		<-done
	}

	// Check for errors
	close(errors)
	for err := range errors {
		t.Error(err)
	}
}

// TestDetectBestProviderEdgeCases tests edge cases for provider detection
func TestDetectBestProviderEdgeCases(t *testing.T) {
	tests := []struct {
		name      string
		providers map[string]*MockProviderFactory
		wantName  string
		wantError bool
	}{
		{
			name:      "empty registry",
			providers: map[string]*MockProviderFactory{},
			wantError: true,
		},
		{
			name: "same priority providers",
			providers: map[string]*MockProviderFactory{
				"provider-a": {
					name:      "provider-a",
					priority:  100,
					available: true,
				},
				"provider-b": {
					name:      "provider-b",
					priority:  100, // Same priority
					available: true,
				},
			},
			wantName:  "", // Either provider-a or provider-b is acceptable with same priority
			wantError: false,
		},
		{
			name: "negative priority provider",
			providers: map[string]*MockProviderFactory{
				"negative-provider": {
					name:      "negative-provider",
					priority:  -10,
					available: true,
				},
			},
			wantName:  "negative-provider",
			wantError: false,
		},
		{
			name: "mixed available and unavailable with highest unavailable",
			providers: map[string]*MockProviderFactory{
				"unavailable-high": {
					name:      "unavailable-high",
					priority:  1000,
					available: false,
				},
				"available-low": {
					name:      "available-low",
					priority:  10,
					available: true,
				},
			},
			wantName:  "available-low",
			wantError: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Clear and setup registry
			registry.mu.Lock()
			registry.providers = make(map[string]ProviderFactory)
			for name, factory := range tt.providers {
				registry.providers[name] = factory
			}
			registry.mu.Unlock()

			providerName, err := detectBestProvider(nil)

			if tt.wantError {
				if err == nil {
					t.Error("Expected error but got none")
				}
			} else {
				if err != nil {
					t.Errorf("Unexpected error: %v", err)
				}
				if tt.wantName != "" && providerName != tt.wantName {
					t.Errorf("detectBestProvider() = %s, want %s", providerName, tt.wantName)
				} else if tt.wantName == "" {
					// For same priority test, either provider is acceptable
					if providerName != "provider-a" && providerName != "provider-b" {
						t.Errorf("detectBestProvider() = %s, want either provider-a or provider-b", providerName)
					}
				}
			}
		})
	}
}

// TestGetProviderInfoSorting tests the sorting behavior of GetProviderInfo
func TestGetProviderInfoSorting(t *testing.T) {
	// Clear and setup registry with providers having same priority
	registry.mu.Lock()
	registry.providers = make(map[string]ProviderFactory)
	registry.providers["z-provider"] = &MockProviderFactory{
		name:        "z-provider",
		description: "Z Provider",
		priority:    100,
		available:   true,
	}
	registry.providers["a-provider"] = &MockProviderFactory{
		name:        "a-provider",
		description: "A Provider",
		priority:    100, // Same priority as z-provider
		available:   true,
	}
	registry.providers["m-provider"] = &MockProviderFactory{
		name:        "m-provider",
		description: "M Provider",
		priority:    50, // Lower priority
		available:   true,
	}
	registry.mu.Unlock()

	info := GetProviderInfo()

	if len(info) != 3 {
		t.Fatalf("Expected 3 providers, got %d", len(info))
	}

	// Should be sorted by priority (high to low), then by name (a to z)
	expected := []string{"a-provider", "z-provider", "m-provider"}
	for i, expectedName := range expected {
		if info[i].Name != expectedName {
			t.Errorf("Position %d: expected %s, got %s", i, expectedName, info[i].Name)
		}
	}

	// Verify priority values
	if info[0].Priority != 100 || info[1].Priority != 100 || info[2].Priority != 50 {
		t.Error("Priority sorting not working correctly")
	}
}
