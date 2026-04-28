package core

import (
	"os"
	"path/filepath"
	"testing"
)

// TestWithRedisDiscovery tests the WithRedisDiscovery config option
func TestWithRedisDiscovery(t *testing.T) {
	tests := []struct {
		name     string
		redisURL string
		expected struct {
			enabled  bool
			provider string
			redisURL string
		}
	}{
		{
			name:     "basic redis URL",
			redisURL: "redis://localhost:6379",
			expected: struct {
				enabled  bool
				provider string
				redisURL string
			}{
				enabled:  true,
				provider: "redis",
				redisURL: "redis://localhost:6379",
			},
		},
		{
			name:     "redis with auth",
			redisURL: "redis://user:pass@localhost:6379/0",
			expected: struct {
				enabled  bool
				provider string
				redisURL string
			}{
				enabled:  true,
				provider: "redis",
				redisURL: "redis://user:pass@localhost:6379/0",
			},
		},
		{
			name:     "empty redis URL",
			redisURL: "",
			expected: struct {
				enabled  bool
				provider string
				redisURL string
			}{
				enabled:  true,
				provider: "redis",
				redisURL: "",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			config := DefaultConfig()

			// Apply the WithRedisDiscovery option
			option := WithRedisDiscovery(tt.redisURL)
			err := option(config)
			if err != nil {
				t.Errorf("WithRedisDiscovery() error = %v", err)
			}

			// Verify discovery configuration
			if config.Discovery.Enabled != tt.expected.enabled {
				t.Errorf("Discovery.Enabled = %v, want %v", config.Discovery.Enabled, tt.expected.enabled)
			}
			if config.Discovery.Provider != tt.expected.provider {
				t.Errorf("Discovery.Provider = %q, want %q", config.Discovery.Provider, tt.expected.provider)
			}
			if config.Discovery.RedisURL != tt.expected.redisURL {
				t.Errorf("Discovery.RedisURL = %q, want %q", config.Discovery.RedisURL, tt.expected.redisURL)
			}
		})
	}
}

// TestWithLogger tests the WithLogger config option
func TestWithLogger(t *testing.T) {
	// Create a mock logger
	mockLogger := &MockLogger{
		entries: make([]LogEntry, 0),
	}

	config := DefaultConfig()

	// Initially should have no logger (nil)
	if config.logger != nil {
		t.Error("Initial config should have nil logger")
	}

	// Apply the WithLogger option
	option := WithLogger(mockLogger)
	err := option(config)
	if err != nil {
		t.Errorf("WithLogger() error = %v", err)
	}

	// Verify logger was set
	if config.logger != mockLogger {
		t.Error("Logger was not set correctly")
	}

	// Verify we can set nil logger
	nilOption := WithLogger(nil)
	err = nilOption(config)
	if err != nil {
		t.Errorf("WithLogger(nil) error = %v", err)
	}

	if config.logger != nil {
		t.Error("Logger should be nil after WithLogger(nil)")
	}
}

// TestLoadFromFile_MissingCoverage tests missing paths in LoadFromFile
func TestLoadFromFile_MissingCoverage(t *testing.T) {
	// Test with non-existent file
	t.Run("non-existent file", func(t *testing.T) {
		config := DefaultConfig()
		err := config.LoadFromFile("/path/to/non/existent/file.yaml")

		// Should return an error for non-existent file
		if err == nil {
			t.Error("LoadFromFile() should return error for non-existent file")
		}
	})

	// Test with directory instead of file
	t.Run("directory instead of file", func(t *testing.T) {
		config := DefaultConfig()
		tempDir := t.TempDir()

		err := config.LoadFromFile(tempDir)

		// Should return an error when trying to read a directory
		if err == nil {
			t.Error("LoadFromFile() should return error when path is a directory")
		}
	})

	// Test with YAML file (should return error as YAML not supported)
	t.Run("YAML file not supported", func(t *testing.T) {
		config := DefaultConfig()
		tempDir := t.TempDir()
		yamlFile := filepath.Join(tempDir, "config.yaml")

		// Create YAML file (content doesn't matter as YAML is not supported)
		yamlContent := `name: "test"`
		err := os.WriteFile(yamlFile, []byte(yamlContent), 0644)
		if err != nil {
			t.Fatalf("Failed to create test file: %v", err)
		}

		err = config.LoadFromFile(yamlFile)

		// Should return an error for YAML files
		if err == nil {
			t.Error("LoadFromFile() should return error for YAML files (not supported)")
		}
	})

	// Test with malformed JSON
	t.Run("malformed JSON", func(t *testing.T) {
		config := DefaultConfig()
		tempDir := t.TempDir()
		malformedFile := filepath.Join(tempDir, "malformed.json")

		// Create malformed JSON file
		malformedJSON := `{
  "name": "test",
  "port": invalid_value,
  "unclosed": {
}`
		err := os.WriteFile(malformedFile, []byte(malformedJSON), 0644)
		if err != nil {
			t.Fatalf("Failed to create test file: %v", err)
		}

		err = config.LoadFromFile(malformedFile)

		// Should return an error for malformed JSON
		if err == nil {
			t.Error("LoadFromFile() should return error for malformed JSON")
		}
	})

	// Test with valid JSON that has different structure
	t.Run("valid JSON with config values", func(t *testing.T) {
		config := DefaultConfig()
		tempDir := t.TempDir()
		configFile := filepath.Join(tempDir, "config.json")

		// Create valid JSON with actual config values
		validJSON := `{
  "name": "test-agent",
  "port": 8080,
  "address": "0.0.0.0",
  "namespace": "test-namespace",
  "ai": {
    "enabled": true,
    "model": "gpt-4"
  },
  "discovery": {
    "enabled": true,
    "provider": "memory"
  },
  "cors": {
    "enabled": true,
    "origins": ["https://example.com"]
  }
}`
		err := os.WriteFile(configFile, []byte(validJSON), 0644)
		if err != nil {
			t.Fatalf("Failed to create test file: %v", err)
		}

		err = config.LoadFromFile(configFile)
		if err != nil {
			t.Errorf("LoadFromFile() failed for valid JSON: %v", err)
		}

		// Verify some values were loaded
		if config.Name != "test-agent" {
			t.Errorf("Name = %q, want %q", config.Name, "test-agent")
		}
		if config.Port != 8080 {
			t.Errorf("Port = %d, want %d", config.Port, 8080)
		}
		if config.Address != "0.0.0.0" {
			t.Errorf("Address = %q, want %q", config.Address, "0.0.0.0")
		}
		if config.Namespace != "test-namespace" {
			t.Errorf("Namespace = %q, want %q", config.Namespace, "test-namespace")
		}
	})

	// Test with empty JSON file
	t.Run("empty JSON file", func(t *testing.T) {
		config := DefaultConfig()
		tempDir := t.TempDir()
		emptyFile := filepath.Join(tempDir, "empty.json")

		// Create empty file
		err := os.WriteFile(emptyFile, []byte(""), 0644)
		if err != nil {
			t.Fatalf("Failed to create test file: %v", err)
		}

		err = config.LoadFromFile(emptyFile)

		// Empty file should cause JSON parsing error
		if err == nil {
			t.Error("LoadFromFile() should return error for empty JSON file")
		}
	})

	// Test with minimal valid JSON
	t.Run("minimal valid JSON", func(t *testing.T) {
		config := DefaultConfig()
		tempDir := t.TempDir()
		minimalFile := filepath.Join(tempDir, "minimal.json")

		// Create minimal valid JSON (empty object)
		minimalJSON := `{}`
		err := os.WriteFile(minimalFile, []byte(minimalJSON), 0644)
		if err != nil {
			t.Fatalf("Failed to create test file: %v", err)
		}

		err = config.LoadFromFile(minimalFile)

		// Should not cause error (empty object is valid JSON)
		if err != nil {
			t.Errorf("LoadFromFile() failed for minimal JSON: %v", err)
		}
	})

	// Test with unsupported file extension
	t.Run("unsupported file extension", func(t *testing.T) {
		config := DefaultConfig()
		tempDir := t.TempDir()
		unsupportedFile := filepath.Join(tempDir, "config.toml")

		// Create file with unsupported extension
		tomlContent := `name = "test"`
		err := os.WriteFile(unsupportedFile, []byte(tomlContent), 0644)
		if err != nil {
			t.Fatalf("Failed to create test file: %v", err)
		}

		err = config.LoadFromFile(unsupportedFile)

		// Should return an error for unsupported extension
		if err == nil {
			t.Error("LoadFromFile() should return error for unsupported file extension")
		}
	})
}
