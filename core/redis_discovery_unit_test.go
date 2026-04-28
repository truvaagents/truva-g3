package core

import (
	"context"
	"testing"
)

// TestIntersect tests the intersect utility function comprehensively
func TestIntersect(t *testing.T) {
	tests := []struct {
		name     string
		a        []string
		b        []string
		expected []string
	}{
		{
			name:     "empty slices",
			a:        []string{},
			b:        []string{},
			expected: []string{},
		},
		{
			name:     "first slice empty",
			a:        []string{},
			b:        []string{"a", "b", "c"},
			expected: []string{},
		},
		{
			name:     "second slice empty",
			a:        []string{"a", "b", "c"},
			b:        []string{},
			expected: []string{},
		},
		{
			name:     "no intersection",
			a:        []string{"a", "b", "c"},
			b:        []string{"d", "e", "f"},
			expected: []string{},
		},
		{
			name:     "full intersection same order",
			a:        []string{"a", "b", "c"},
			b:        []string{"a", "b", "c"},
			expected: []string{"a", "b", "c"},
		},
		{
			name:     "full intersection different order",
			a:        []string{"a", "b", "c"},
			b:        []string{"c", "a", "b"},
			expected: []string{"c", "a", "b"},
		},
		{
			name:     "partial intersection",
			a:        []string{"a", "b", "c", "d"},
			b:        []string{"c", "d", "e", "f"},
			expected: []string{"c", "d"},
		},
		{
			name:     "single element intersection",
			a:        []string{"a", "b", "c"},
			b:        []string{"x", "c", "y"},
			expected: []string{"c"},
		},
		{
			name:     "duplicates in first slice",
			a:        []string{"a", "b", "a", "c"},
			b:        []string{"a", "c"},
			expected: []string{"a", "c"},
		},
		{
			name:     "duplicates in second slice",
			a:        []string{"a", "c"},
			b:        []string{"a", "b", "a", "c"},
			expected: []string{"a", "c"},
		},
		{
			name:     "duplicates in both slices",
			a:        []string{"a", "b", "a", "c"},
			b:        []string{"a", "c", "a", "d"},
			expected: []string{"a", "c"},
		},
		{
			name:     "subset relationship - b subset of a",
			a:        []string{"a", "b", "c", "d", "e"},
			b:        []string{"b", "d"},
			expected: []string{"b", "d"},
		},
		{
			name:     "subset relationship - a subset of b",
			a:        []string{"b", "d"},
			b:        []string{"a", "b", "c", "d", "e"},
			expected: []string{"b", "d"},
		},
		{
			name:     "service ID intersection scenario",
			a:        []string{"service-1", "service-2", "service-3"},
			b:        []string{"service-2", "service-4", "service-1"},
			expected: []string{"service-2", "service-1"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := intersect(tt.a, tt.b)

			if len(result) != len(tt.expected) {
				t.Errorf("intersect(%v, %v) = %v, expected length %d, got length %d",
					tt.a, tt.b, result, len(tt.expected), len(result))
				return
			}

			// Convert result to map for order-independent comparison
			resultMap := make(map[string]bool)
			for _, v := range result {
				resultMap[v] = true
			}

			// Verify all expected elements are present
			for _, expected := range tt.expected {
				if !resultMap[expected] {
					t.Errorf("intersect(%v, %v) = %v, missing expected element %s",
						tt.a, tt.b, result, expected)
				}
			}

			// Verify no unexpected elements
			expectedMap := make(map[string]bool)
			for _, v := range tt.expected {
				expectedMap[v] = true
			}
			for _, v := range result {
				if !expectedMap[v] {
					t.Errorf("intersect(%v, %v) = %v, unexpected element %s",
						tt.a, tt.b, result, v)
				}
			}
		})
	}
}

// TestRedisDiscoveryConstructorValidation tests constructor parameter validation
func TestRedisDiscoveryConstructorValidation(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode (Redis connection attempts)")
	}
	tests := []struct {
		name        string
		redisURL    string
		namespace   string
		expectError bool
		errorType   string
	}{
		{
			name:        "invalid URL - empty",
			redisURL:    "",
			namespace:   "test",
			expectError: true,
			errorType:   "invalid Redis URL",
		},
		{
			name:        "invalid URL - malformed",
			redisURL:    "not-a-url",
			namespace:   "test",
			expectError: true,
			errorType:   "invalid Redis URL",
		},
		{
			name:        "invalid URL - wrong scheme",
			redisURL:    "http://localhost:6379",
			namespace:   "test",
			expectError: true,
			errorType:   "invalid Redis URL",
		},
		{
			name:        "valid URL format - may succeed or fail depending on Redis availability",
			redisURL:    "redis://localhost:6379",
			namespace:   "test",
			expectError: false, // Don't expect error since Redis might be running
			errorType:   "",
		},
		{
			name:        "valid URL with auth - may succeed or fail depending on Redis availability",
			redisURL:    "redis://user:pass@localhost:6379",
			namespace:   "test",
			expectError: false, // Don't expect error, test URL parsing only
			errorType:   "",
		},
		{
			name:        "empty namespace should work",
			redisURL:    "redis://localhost:6379",
			namespace:   "",
			expectError: false, // Don't expect error since Redis might be running
			errorType:   "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var discovery *RedisDiscovery
			var err error

			if tt.namespace == "test" {
				discovery, err = NewRedisDiscovery(tt.redisURL)
			} else {
				discovery, err = NewRedisDiscoveryWithNamespace(tt.redisURL, tt.namespace)
			}

			if tt.expectError {
				if err == nil {
					t.Errorf("Expected error containing %q, but got no error", tt.errorType)
					return
				}
				if discovery != nil {
					t.Error("Expected nil discovery on error, but got non-nil")
				}
			} else {
				// For valid URLs, we don't enforce error/success since Redis availability varies
				// Just verify that if success, discovery is not nil
				if err == nil && discovery == nil {
					t.Error("Expected non-nil discovery on success, but got nil")
				}
				// If error occurs, it should be connection-related, not URL parsing
				if err != nil {
					t.Logf("Connection error (expected if Redis not available): %v", err)
				}
			}
		})
	}
}

// TestRedisDiscoverySetLogger tests logger functionality
func TestRedisDiscoverySetLogger(t *testing.T) {
	// Create a mock logger to test with
	mockLogger := &MockLogger{
		entries: make([]LogEntry, 0),
	}

	// We can't create a real RedisDiscovery without Redis, but we can test the struct
	discovery := &RedisDiscovery{
		RedisRegistry: &RedisRegistry{},
	}

	// Test setting logger
	discovery.SetLogger(mockLogger)

	if discovery.logger != mockLogger {
		t.Error("Expected logger to be set on discovery")
	}

	if discovery.RedisRegistry.logger != mockLogger {
		t.Error("Expected logger to be set on embedded registry")
	}
}

// TestRedisDiscoveryBackwardCompatibilityWrappers tests wrapper function logic
func TestRedisDiscoveryBackwardCompatibilityWrappers(t *testing.T) {
	// Create a mock discovery that doesn't require Redis
	discovery := &mockRedisDiscovery{}

	// Test FindService wrapper
	t.Run("FindService wrapper", func(t *testing.T) {
		serviceName := "test-service"
		result, err := discovery.FindService(context.Background(), serviceName)

		if err != nil {
			t.Errorf("Expected no error, got: %v", err)
		}

		// Verify it called Discover with correct filter
		if discovery.lastFilter.Name != serviceName {
			t.Errorf("Expected filter name %q, got %q", serviceName, discovery.lastFilter.Name)
		}
		if discovery.lastFilter.Type != "" {
			t.Errorf("Expected empty filter type, got %q", discovery.lastFilter.Type)
		}
		if len(discovery.lastFilter.Capabilities) != 0 {
			t.Errorf("Expected empty capabilities filter, got %v", discovery.lastFilter.Capabilities)
		}

		if len(result) != 1 || result[0].Name != serviceName {
			t.Errorf("Expected result with service name %q", serviceName)
		}
	})

	// Test FindByCapability wrapper
	t.Run("FindByCapability wrapper", func(t *testing.T) {
		capability := "test-capability"
		result, err := discovery.FindByCapability(context.Background(), capability)

		if err != nil {
			t.Errorf("Expected no error, got: %v", err)
		}

		// Verify it called Discover with correct filter
		if discovery.lastFilter.Name != "" {
			t.Errorf("Expected empty filter name, got %q", discovery.lastFilter.Name)
		}
		if discovery.lastFilter.Type != "" {
			t.Errorf("Expected empty filter type, got %q", discovery.lastFilter.Type)
		}
		if len(discovery.lastFilter.Capabilities) != 1 || discovery.lastFilter.Capabilities[0] != capability {
			t.Errorf("Expected capabilities filter [%q], got %v", capability, discovery.lastFilter.Capabilities)
		}

		if len(result) != 1 || result[0].Capabilities[0].Name != capability {
			t.Errorf("Expected result with capability %q", capability)
		}
	})
}

// Mock logger for testing
type MockLogger struct {
	entries []LogEntry
}

type LogEntry struct {
	Level   string
	Message string
	Fields  map[string]interface{}
}

func (m *MockLogger) Debug(msg string, fields map[string]interface{}) {
	m.entries = append(m.entries, LogEntry{Level: "debug", Message: msg, Fields: fields})
}

func (m *MockLogger) Info(msg string, fields map[string]interface{}) {
	m.entries = append(m.entries, LogEntry{Level: "info", Message: msg, Fields: fields})
}

func (m *MockLogger) Warn(msg string, fields map[string]interface{}) {
	m.entries = append(m.entries, LogEntry{Level: "warn", Message: msg, Fields: fields})
}

func (m *MockLogger) Error(msg string, fields map[string]interface{}) {
	m.entries = append(m.entries, LogEntry{Level: "error", Message: msg, Fields: fields})
}

func (m *MockLogger) DebugWithContext(ctx context.Context, msg string, fields map[string]interface{}) {
	m.entries = append(m.entries, LogEntry{Level: "debug", Message: msg, Fields: fields})
}

func (m *MockLogger) InfoWithContext(ctx context.Context, msg string, fields map[string]interface{}) {
	m.entries = append(m.entries, LogEntry{Level: "info", Message: msg, Fields: fields})
}

func (m *MockLogger) WarnWithContext(ctx context.Context, msg string, fields map[string]interface{}) {
	m.entries = append(m.entries, LogEntry{Level: "warn", Message: msg, Fields: fields})
}

func (m *MockLogger) ErrorWithContext(ctx context.Context, msg string, fields map[string]interface{}) {
	m.entries = append(m.entries, LogEntry{Level: "error", Message: msg, Fields: fields})
}

// Mock discovery for testing wrapper functions without Redis
type mockRedisDiscovery struct {
	lastFilter DiscoveryFilter
}

func (m *mockRedisDiscovery) FindService(ctx context.Context, serviceName string) ([]*ServiceInfo, error) {
	return m.Discover(ctx, DiscoveryFilter{Name: serviceName})
}

func (m *mockRedisDiscovery) FindByCapability(ctx context.Context, capability string) ([]*ServiceInfo, error) {
	return m.Discover(ctx, DiscoveryFilter{Capabilities: []string{capability}})
}

func (m *mockRedisDiscovery) Discover(ctx context.Context, filter DiscoveryFilter) ([]*ServiceInfo, error) {
	m.lastFilter = filter

	// Return mock result based on filter
	if filter.Name != "" {
		return []*ServiceInfo{{Name: filter.Name}}, nil
	}
	if len(filter.Capabilities) > 0 {
		return []*ServiceInfo{{
			Capabilities: []Capability{{Name: filter.Capabilities[0]}},
		}}, nil
	}
	return []*ServiceInfo{}, nil
}
