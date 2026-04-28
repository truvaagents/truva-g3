package core

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestBaseTool_Initialize_AutoInjectsRegistry tests the new auto-injection functionality
func TestBaseTool_Initialize_AutoInjectsRegistry(t *testing.T) {
	tests := []struct {
		name                 string
		config               *Config
		presetRegistry       Registry
		expectedRegistryNil  bool
		expectedRegistryType string
	}{
		{
			name: "auto_initializes_redis_registry",
			config: &Config{
				Discovery: DiscoveryConfig{
					Enabled:  true,
					Provider: "redis",
					RedisURL: "redis://localhost:6379",
				},
			},
			presetRegistry:       nil,
			expectedRegistryNil:  false,
			expectedRegistryType: "*core.RedisRegistry",
		},
		{
			name: "auto_initializes_mock_registry_in_development",
			config: &Config{
				Discovery: DiscoveryConfig{
					Enabled:  true,
					Provider: "redis",
					RedisURL: "redis://localhost:6379",
				},
				Development: DevelopmentConfig{
					MockDiscovery: true,
				},
			},
			presetRegistry:       nil,
			expectedRegistryNil:  false,
			expectedRegistryType: "*core.MockDiscovery",
		},
		{
			name: "preserves_existing_registry",
			config: &Config{
				Discovery: DiscoveryConfig{
					Enabled:  true,
					Provider: "redis",
					RedisURL: "redis://localhost:6379",
				},
			},
			presetRegistry:       NewMockDiscovery(),
			expectedRegistryNil:  false,
			expectedRegistryType: "*core.MockDiscovery",
		},
		{
			name: "no_initialization_when_discovery_disabled",
			config: &Config{
				Discovery: DiscoveryConfig{
					Enabled:  false,
					Provider: "redis",
					RedisURL: "redis://localhost:6379",
				},
			},
			presetRegistry:       nil,
			expectedRegistryNil:  true,
			expectedRegistryType: "",
		},
		{
			name:                 "no_initialization_when_no_config",
			config:               nil,
			presetRegistry:       nil,
			expectedRegistryNil:  true,
			expectedRegistryType: "",
		},
		{
			name: "no_initialization_when_no_redis_url",
			config: &Config{
				Discovery: DiscoveryConfig{
					Enabled:  true,
					Provider: "redis",
					RedisURL: "", // Empty URL
				},
			},
			presetRegistry:       nil,
			expectedRegistryNil:  true,
			expectedRegistryType: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Skip Redis tests when Redis is not available (but allow mock tests)
			if tt.name == "auto_initializes_redis_registry" {
				requireRedis(t)
			}

			// Create tool with test configuration
			tool := &BaseTool{
				ID:       "test-tool",
				Name:     "test-tool",
				Type:     ComponentTypeTool,
				Logger:   &NoOpLogger{},
				Registry: tt.presetRegistry,
				Config:   tt.config,
			}

			// Initialize tool
			ctx := context.Background()
			err := tool.Initialize(ctx)
			assert.NoError(t, err, "Initialize should not fail")

			// Verify registry state
			if tt.expectedRegistryNil {
				assert.Nil(t, tool.Registry, "Registry should be nil")
			} else {
				require.NotNil(t, tool.Registry, "Registry should be initialized")
				assert.Equal(t, tt.expectedRegistryType, getTypeName(tool.Registry),
					"Registry should be of expected type")
			}
		})
	}
}

// TestBaseTool_Initialize_RegistrationWithAutoInjectedRegistry tests registration functionality
func TestBaseTool_Initialize_RegistrationWithAutoInjectedRegistry(t *testing.T) {
	t.Run("registers_with_auto_injected_mock_registry", func(t *testing.T) {
		// Create tool with mock registry configuration
		tool := &BaseTool{
			ID:   "test-tool",
			Name: "test-tool",
			Type: ComponentTypeTool,
			Capabilities: []Capability{
				{Name: "test_capability", Description: "Test capability"},
			},
			Logger: &NoOpLogger{},
			Config: &Config{
				Discovery: DiscoveryConfig{
					Enabled:  true,
					Provider: "redis",
					RedisURL: "redis://localhost:6379",
				},
				Development: DevelopmentConfig{
					MockDiscovery: true,
				},
				Port:    8080,
				Address: "localhost",
			},
		}

		// Initialize tool
		ctx := context.Background()
		err := tool.Initialize(ctx)
		assert.NoError(t, err, "Initialize should succeed")

		// Verify registry was auto-injected
		require.NotNil(t, tool.Registry, "Registry should be auto-injected")
		mockRegistry := tool.Registry.(*MockDiscovery)

		// Verify tool was registered
		services, err := mockRegistry.Discover(ctx, DiscoveryFilter{})
		require.NoError(t, err, "Discovery should work")
		assert.Len(t, services, 1, "Tool should be registered")

		registeredTool := services[0]
		assert.Equal(t, "test-tool", registeredTool.ID)
		assert.Equal(t, "test-tool", registeredTool.Name)
		assert.Equal(t, ComponentTypeTool, registeredTool.Type)
		assert.Len(t, registeredTool.Capabilities, 1)
		assert.Equal(t, "test_capability", registeredTool.Capabilities[0].Name)
	})
}

// TestBaseTool_Initialize_GracefulDegradation tests error handling
func TestBaseTool_Initialize_GracefulDegradation(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode (Redis connection timeout)")
	}
	t.Run("graceful_degradation_on_redis_failure", func(t *testing.T) {
		// Create tool with invalid Redis URL
		tool := &BaseTool{
			ID:     "test-tool",
			Name:   "test-tool",
			Type:   ComponentTypeTool,
			Logger: &NoOpLogger{},
			Config: &Config{
				Discovery: DiscoveryConfig{
					Enabled:  true,
					Provider: "redis",
					RedisURL: "redis://invalid-host:6379", // Invalid host
				},
			},
		}

		// Initialize should succeed despite Redis failure
		ctx := context.Background()
		err := tool.Initialize(ctx)
		assert.NoError(t, err, "Initialize should succeed even with Redis failure")

		// Registry should remain nil due to connection failure
		assert.Nil(t, tool.Registry, "Registry should be nil due to connection failure")
	})
}

// TestBaseTool_Initialize_ConfigurationPrecedence tests configuration scenarios
func TestBaseTool_Initialize_ConfigurationPrecedence(t *testing.T) {
	t.Run("mock_discovery_takes_precedence_over_redis", func(t *testing.T) {
		tool := &BaseTool{
			ID:     "test-tool",
			Name:   "test-tool",
			Type:   ComponentTypeTool,
			Logger: &NoOpLogger{},
			Config: &Config{
				Discovery: DiscoveryConfig{
					Enabled:  true,
					Provider: "redis",
					RedisURL: "redis://localhost:6379",
				},
				Development: DevelopmentConfig{
					MockDiscovery: true, // This should take precedence
				},
			},
		}

		ctx := context.Background()
		err := tool.Initialize(ctx)
		assert.NoError(t, err)

		// Should use MockDiscovery, not RedisRegistry
		require.NotNil(t, tool.Registry)
		assert.Equal(t, "*core.MockDiscovery", getTypeName(tool.Registry))
	})
}

// TestBaseTool_Initialize_BackwardCompatibility ensures existing behavior still works
func TestBaseTool_Initialize_BackwardCompatibility(t *testing.T) {
	t.Run("manual_registry_setup_still_works", func(t *testing.T) {
		// Create tool with manually set registry (old way)
		mockRegistry := NewMockDiscovery()
		tool := &BaseTool{
			ID:       "test-tool",
			Name:     "test-tool",
			Type:     ComponentTypeTool,
			Logger:   &NoOpLogger{},
			Registry: mockRegistry, // Manually set
			Config: &Config{
				Discovery: DiscoveryConfig{
					Enabled: true,
				},
				Port:    8080,
				Address: "localhost",
			},
		}

		ctx := context.Background()
		err := tool.Initialize(ctx)
		assert.NoError(t, err)

		// Should preserve manually set registry
		assert.Same(t, mockRegistry, tool.Registry, "Should preserve manually set registry")
	})
}

// Helper function to get type name for assertions
func getTypeName(obj interface{}) string {
	if obj == nil {
		return "<nil>"
	}
	return getTypeNameImpl(obj)
}

func getTypeNameImpl(obj interface{}) string {
	switch obj.(type) {
	case *RedisRegistry:
		return "*core.RedisRegistry"
	case *MockDiscovery:
		return "*core.MockDiscovery"
	default:
		return "unknown"
	}
}
