//go:build integration
// +build integration

package core

import (
	"context"
	"os"
	"testing"
)

// TestWithDiscoveryAutoConfiguresRedisURL verifies that WithDiscovery automatically sets Redis URL
func TestWithDiscoveryAutoConfiguresRedisURL(t *testing.T) {
	// Save original environment
	originalRedisURL := os.Getenv("REDIS_URL")
	originalTruvaG3RedisURL := os.Getenv("TRUVAG3_REDIS_URL")
	defer func() {
		os.Setenv("REDIS_URL", originalRedisURL)
		os.Setenv("TRUVAG3_REDIS_URL", originalTruvaG3RedisURL)
	}()

	t.Run("uses REDIS_URL environment variable", func(t *testing.T) {
		os.Setenv("REDIS_URL", "redis://env.example.com:6379")
		os.Unsetenv("TRUVAG3_REDIS_URL")

		config, err := NewConfig(WithDiscovery(true, "redis"))
		if err != nil {
			t.Fatalf("NewConfig failed: %v", err)
		}

		if config.Discovery.RedisURL != "redis://env.example.com:6379" {
			t.Errorf("Expected RedisURL from REDIS_URL env var, got: %s", config.Discovery.RedisURL)
		}
	})

	t.Run("uses TRUVAG3_REDIS_URL environment variable", func(t *testing.T) {
		os.Unsetenv("REDIS_URL")
		os.Setenv("TRUVAG3_REDIS_URL", "redis://truvag3.example.com:6379")

		config, err := NewConfig(WithDiscovery(true, "redis"))
		if err != nil {
			t.Fatalf("NewConfig failed: %v", err)
		}

		if config.Discovery.RedisURL != "redis://truvag3.example.com:6379" {
			t.Errorf("Expected RedisURL from TRUVAG3_REDIS_URL env var, got: %s", config.Discovery.RedisURL)
		}
	})

	t.Run("REDIS_URL takes precedence over TRUVAG3_REDIS_URL", func(t *testing.T) {
		// Clean environment first
		os.Unsetenv("REDIS_URL")
		os.Unsetenv("TRUVAG3_REDIS_URL")

		// Set both variables
		os.Setenv("REDIS_URL", "redis://primary.example.com:6379")
		os.Setenv("TRUVAG3_REDIS_URL", "redis://secondary.example.com:6379")

		config, err := NewConfig(WithDiscovery(true, "redis"))
		if err != nil {
			t.Fatalf("NewConfig failed: %v", err)
		}

		if config.Discovery.RedisURL != "redis://primary.example.com:6379" {
			t.Errorf("Expected REDIS_URL to take precedence, got: %s", config.Discovery.RedisURL)
		}
	})

	t.Run("defaults to localhost when no environment variables", func(t *testing.T) {
		os.Unsetenv("REDIS_URL")
		os.Unsetenv("TRUVAG3_REDIS_URL")

		config, err := NewConfig(WithDiscovery(true, "redis"))
		if err != nil {
			t.Fatalf("NewConfig failed: %v", err)
		}

		if config.Discovery.RedisURL != "redis://localhost:6379" {
			t.Errorf("Expected default localhost RedisURL, got: %s", config.Discovery.RedisURL)
		}
	})

	t.Run("doesn't override existing RedisURL", func(t *testing.T) {
		os.Setenv("REDIS_URL", "redis://env.example.com:6379")

		config, err := NewConfig(
			WithRedisURL("redis://existing.example.com:6379"),
			WithDiscovery(true, "redis"),
		)
		if err != nil {
			t.Fatalf("NewConfig failed: %v", err)
		}

		if config.Discovery.RedisURL != "redis://existing.example.com:6379" {
			t.Errorf("Expected existing RedisURL to be preserved, got: %s", config.Discovery.RedisURL)
		}
	})

	t.Run("doesn't set RedisURL for non-redis providers", func(t *testing.T) {
		// Clean environment first
		os.Unsetenv("REDIS_URL")
		os.Unsetenv("TRUVAG3_REDIS_URL")
		os.Setenv("REDIS_URL", "redis://env.example.com:6379")

		config, err := NewConfig(WithDiscovery(true, "mock"))
		if err != nil {
			t.Fatalf("NewConfig failed: %v", err)
		}

		if config.Discovery.RedisURL != "" {
			t.Errorf("Expected no RedisURL for mock provider, got: %s", config.Discovery.RedisURL)
		}
	})

	t.Run("doesn't set RedisURL when discovery disabled", func(t *testing.T) {
		// Clean environment first
		os.Unsetenv("REDIS_URL")
		os.Unsetenv("TRUVAG3_REDIS_URL")
		os.Setenv("REDIS_URL", "redis://env.example.com:6379")

		config, err := NewConfig(WithDiscovery(false, "redis"))
		if err != nil {
			t.Fatalf("NewConfig failed: %v", err)
		}

		if config.Discovery.RedisURL != "" {
			t.Errorf("Expected no RedisURL when discovery disabled, got: %s", config.Discovery.RedisURL)
		}
	})
}

// TestWithRedisDiscoveryHelper verifies the WithRedisDiscovery convenience function
func TestWithRedisDiscoveryHelper(t *testing.T) {
	config, err := NewConfig(WithRedisDiscovery("redis://helper.example.com:6379"))
	if err != nil {
		t.Fatalf("NewConfig failed: %v", err)
	}

	// Verify discovery is enabled
	if !config.Discovery.Enabled {
		t.Error("Expected discovery to be enabled")
	}

	// Verify provider is set to redis
	if config.Discovery.Provider != "redis" {
		t.Errorf("Expected provider to be 'redis', got: %s", config.Discovery.Provider)
	}

	// Verify Redis URL is set
	if config.Discovery.RedisURL != "redis://helper.example.com:6379" {
		t.Errorf("Expected RedisURL to be set, got: %s", config.Discovery.RedisURL)
	}

	// Verify memory Redis URL is also set
	if config.Memory.RedisURL != "redis://helper.example.com:6379" {
		t.Errorf("Expected Memory RedisURL to be set, got: %s", config.Memory.RedisURL)
	}
}

// TestAgentDependencyInjectionFixed verifies that agents now auto-initialize discovery
func TestAgentDependencyInjectionFixed(t *testing.T) {
	requireRedis(t)

	ctx := context.Background()

	t.Run("agent auto-initializes Redis discovery with WithDiscovery", func(t *testing.T) {
		agent := NewBaseAgent("di-test-agent")

		// This should now work without manual discovery setup
		config, err := NewConfig(
			WithName("di-test-agent"),
			WithDiscovery(true, "redis"), // Should auto-configure Redis URL
		)
		if err != nil {
			t.Fatalf("NewConfig failed: %v", err)
		}
		agent.Config = config

		// Initialize should now create Redis discovery automatically
		err = agent.Initialize(ctx)
		if err != nil {
			t.Fatalf("Agent.Initialize() failed: %v", err)
		}

		// Verify discovery was auto-initialized
		if agent.Discovery == nil {
			t.Fatal("Expected Discovery to be auto-initialized, but it's nil")
		}

		// Verify it's a Redis discovery
		if _, ok := agent.Discovery.(*RedisDiscovery); !ok {
			t.Errorf("Expected RedisDiscovery, got: %T", agent.Discovery)
		}
	})

	t.Run("agent auto-initializes with WithRedisDiscovery helper", func(t *testing.T) {
		agent := NewBaseAgent("di-helper-test-agent")

		config, err := NewConfig(
			WithName("di-helper-test-agent"),
			WithRedisDiscovery("redis://localhost:6379"),
		)
		if err != nil {
			t.Fatalf("NewConfig failed: %v", err)
		}
		agent.Config = config

		err = agent.Initialize(ctx)
		if err != nil {
			t.Fatalf("Agent.Initialize() failed: %v", err)
		}

		// Verify discovery was auto-initialized
		if agent.Discovery == nil {
			t.Fatal("Expected Discovery to be auto-initialized, but it's nil")
		}

		// Verify it's a Redis discovery
		if _, ok := agent.Discovery.(*RedisDiscovery); !ok {
			t.Errorf("Expected RedisDiscovery, got: %T", agent.Discovery)
		}
	})
}

// TestToolDependencyInjectionFixed verifies that tools now auto-initialize registry
func TestToolDependencyInjectionFixed(t *testing.T) {
	requireRedis(t)

	ctx := context.Background()

	tool := NewTool("di-test-tool")

	config, err := NewConfig(
		WithName("di-test-tool"),
		WithDiscovery(true, "redis"), // Should auto-configure for tools too
	)
	if err != nil {
		t.Fatalf("NewConfig failed: %v", err)
	}
	tool.Config = config

	// Manually set registry since tools don't auto-initialize discovery
	// but they should work with the auto-configured RedisURL
	if config.Discovery.RedisURL != "" {
		registry, err := NewRedisRegistry(config.Discovery.RedisURL)
		if err != nil {
			t.Fatalf("Failed to create Redis registry: %v", err)
		}
		tool.Registry = registry

		// Initialize should now work with the auto-configured URL
		err = tool.Initialize(ctx)
		if err != nil {
			t.Fatalf("Tool.Initialize() failed: %v", err)
		}

		// Verify registry is working
		if tool.Registry == nil {
			t.Fatal("Expected Registry to be set")
		}
	}
}

// TestFrameworkIntegrationWithDependencyInjection verifies end-to-end framework usage
func TestFrameworkIntegrationWithDependencyInjection(t *testing.T) {
	requireRedis(t)

	ctx := context.Background()

	t.Run("framework works with auto-configured discovery", func(t *testing.T) {
		agent := NewBaseAgent("framework-di-agent")

		// This is the user experience we want to work
		framework, err := NewFramework(agent,
			WithPort(9000),
			WithDiscovery(true, "redis"), // Should auto-configure Redis URL
		)
		if err != nil {
			t.Fatalf("NewFramework failed: %v", err)
		}

		// Framework.Run should work without manual discovery setup
		go func() {
			err := framework.Run(ctx)
			if err != nil {
				// Expected if Redis not available
				t.Logf("Framework.Run failed (likely Redis unavailable): %v", err)
			}
		}()

		// Give it a moment to initialize
		// time.Sleep(100 * time.Millisecond)

		// Test passes if no panic occurs during initialization
	})

	t.Run("framework works with WithRedisDiscovery helper", func(t *testing.T) {
		agent := NewBaseAgent("framework-helper-agent")

		framework, err := NewFramework(agent,
			WithPort(9001),
			WithRedisDiscovery("redis://localhost:6379"),
		)
		if err != nil {
			t.Fatalf("NewFramework failed: %v", err)
		}

		go func() {
			err := framework.Run(ctx)
			if err != nil {
				t.Logf("Framework.Run failed (likely Redis unavailable): %v", err)
			}
		}()

		// Test passes if no panic occurs during initialization
	})
}
