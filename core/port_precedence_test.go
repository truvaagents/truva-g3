package core

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"sync"
	"testing"
	"time"
)

// TestPortParameterPrecedence tests that explicit port parameters take precedence over config
func TestPortParameterPrecedence(t *testing.T) {
	t.Run("Tool_Parameter_Overrides_Config", func(t *testing.T) {
		tool := NewTool("test-tool")

		// Use dynamic ports to avoid conflicts with system services
		configPort := findAvailablePort(t)
		testPort := findAvailablePort(t)

		tool.Config = &Config{
			Port:    configPort, // Config specifies one port
			Address: "localhost",
		}

		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		// Start tool with explicit port parameter (should override config)
		serverReady := make(chan bool, 1)
		var startErr error
		var wg sync.WaitGroup
		wg.Add(1)

		go func() {
			defer wg.Done()
			serverReady <- true
			startErr = tool.Start(ctx, testPort) // Parameter should override config
		}()

		// Wait for server to be ready
		<-serverReady
		time.Sleep(100 * time.Millisecond)

		// Verify server is listening on testPort, NOT on config port
		verifyServerOnPort(t, testPort, true, "tool should bind to parameter port")
		verifyServerOnPort(t, configPort, false, "tool should NOT bind to config port")

		// Cleanup
		tool.Shutdown(ctx)
		cancel()
		wg.Wait()

		// Verify no start error (unless context canceled)
		if startErr != nil && startErr != http.ErrServerClosed {
			t.Errorf("Tool.Start() error = %v", startErr)
		}
	})

	t.Run("Agent_Parameter_Overrides_Config", func(t *testing.T) {
		agent := NewBaseAgent("test-agent")

		// Use dynamic ports to avoid conflicts with system services
		configPort := findAvailablePort(t)
		testPort := findAvailablePort(t)

		agent.Config = &Config{
			Port:    configPort, // Config specifies one port
			Address: "localhost",
		}

		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		// Initialize agent first
		err := agent.Initialize(ctx)
		if err != nil {
			t.Fatalf("Agent.Initialize() error = %v", err)
		}

		// Start agent with explicit port parameter (should override config)
		serverReady := make(chan bool, 1)
		var startErr error

		go func() {
			serverReady <- true
			startErr = agent.Start(ctx, testPort) // Parameter should override config
		}()

		// Wait for server to be ready
		<-serverReady
		time.Sleep(200 * time.Millisecond)

		// Verify server is listening on testPort, NOT on config port
		verifyServerOnPort(t, testPort, true, "agent should bind to parameter port")
		verifyServerOnPort(t, configPort, false, "agent should NOT bind to config port")

		// Cleanup - cancel context to stop the agent
		cancel()

		// Give agent time to shutdown gracefully
		time.Sleep(100 * time.Millisecond)

		// Verify start error is context cancellation (expected)
		if startErr != nil && startErr.Error() != "context canceled" && startErr != http.ErrServerClosed {
			t.Errorf("Agent.Start() unexpected error = %v", startErr)
		}
	})
}

// TestPortConfigFallback tests that config port is used when parameter is negative
func TestPortConfigFallback(t *testing.T) {
	t.Run("Tool_Uses_Config_When_Parameter_Negative", func(t *testing.T) {
		tool := NewTool("test-tool")
		configPort := findAvailablePort(t)
		tool.Config = &Config{
			Port:    configPort,
			Address: "localhost",
		}

		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		// Start tool with negative port (should use config)
		serverReady := make(chan bool, 1)
		var startErr error
		var wg sync.WaitGroup
		wg.Add(1)

		go func() {
			defer wg.Done()
			serverReady <- true
			startErr = tool.Start(ctx, -1) // Should fall back to config
		}()

		// Wait for server to be ready
		<-serverReady
		time.Sleep(100 * time.Millisecond)

		// Verify server is listening on config port
		verifyServerOnPort(t, configPort, true, "tool should use config port when parameter is -1")

		// Cleanup
		tool.Shutdown(ctx)
		cancel()
		wg.Wait()

		// Verify no start error (unless context canceled)
		if startErr != nil && startErr != http.ErrServerClosed {
			t.Errorf("Tool.Start() error = %v", startErr)
		}
	})

	t.Run("Agent_Uses_Config_When_Parameter_Negative", func(t *testing.T) {
		agent := NewBaseAgent("test-agent")
		configPort := findAvailablePort(t)
		agent.Config = &Config{
			Port:    configPort,
			Address: "localhost",
		}

		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		// Initialize agent first
		err := agent.Initialize(ctx)
		if err != nil {
			t.Fatalf("Agent.Initialize() error = %v", err)
		}

		// Start agent with negative port (should use config)
		serverReady := make(chan bool, 1)
		var startErr error

		go func() {
			serverReady <- true
			startErr = agent.Start(ctx, -1) // Should fall back to config
		}()

		// Wait for server to be ready
		<-serverReady
		time.Sleep(200 * time.Millisecond)

		// Verify server is listening on config port
		verifyServerOnPort(t, configPort, true, "agent should use config port when parameter is -1")

		// Cleanup - cancel context to stop the agent
		cancel()

		// Give agent time to shutdown gracefully
		time.Sleep(100 * time.Millisecond)

		// Verify start error is context cancellation (expected)
		if startErr != nil && startErr.Error() != "context canceled" && startErr != http.ErrServerClosed {
			t.Errorf("Agent.Start() unexpected error = %v", startErr)
		}
	})
}

// TestPortValidation tests port range validation
func TestPortValidation(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode (server startup tests)")
	}
	// Use dynamic port allocation to avoid conflicts in CI
	testCases := []struct {
		name        string
		port        int
		shouldError bool
	}{
		{"Valid_Port_Auto_Assignment_1", 0, false}, // Changed from hardcoded port
		{"Valid_Port_Auto_Assignment_2", 0, false}, // Changed from hardcoded port
		{"Valid_Port_65535", 65535, false},
		{"Valid_Port_Auto_Assignment", 0, false},
		{"Invalid_Port_With_Invalid_Config", -1, true}, // Will use invalid config port
		{"Invalid_Port_Too_Large", 65536, true},
		{"Invalid_Port_Way_Too_Large", 100000, true},
	}

	for _, tc := range testCases {
		t.Run("Tool_"+tc.name, func(t *testing.T) {
			tool := NewTool("test-tool")
			config := &Config{Address: "localhost"}

			// Special case: set invalid config port for the invalid config test
			if tc.name == "Invalid_Port_With_Invalid_Config" {
				config.Port = 70000 // Invalid port in config
			}

			tool.Config = config

			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()

			if tc.shouldError {
				// Test error case synchronously
				err := tool.Start(ctx, tc.port)
				if err == nil {
					t.Errorf("Tool.Start(%d) should have returned error", tc.port)
					tool.Shutdown(ctx)
				}
			} else {
				// Test success case asynchronously (since it blocks)
				startErr := make(chan error, 1)
				go func() {
					startErr <- tool.Start(ctx, tc.port)
				}()

				// Give server time to start
				time.Sleep(100 * time.Millisecond)

				// Verify success by attempting connection (or that it's listening)
				if tc.port > 0 {
					// For specific ports, verify connection works
					verifyServerOnPort(t, tc.port, true, fmt.Sprintf("port %d should be listening", tc.port))
				}

				// Cleanup
				tool.Shutdown(ctx)
				cancel()

				// Verify start completed without error (or with expected error)
				select {
				case err := <-startErr:
					if err != nil && err != http.ErrServerClosed && err.Error() != "context canceled" {
						t.Errorf("Tool.Start(%d) unexpected error = %v", tc.port, err)
					}
				case <-time.After(1 * time.Second):
					// Start is still blocking, which is expected for valid ports
				}
			}
		})

		t.Run("Agent_"+tc.name, func(t *testing.T) {
			agent := NewBaseAgent("test-agent")
			config := &Config{Address: "localhost"}

			// Special case: set invalid config port for the invalid config test
			if tc.name == "Invalid_Port_With_Invalid_Config" {
				config.Port = 70000 // Invalid port in config
			}

			agent.Config = config

			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()

			// Initialize first
			err := agent.Initialize(ctx)
			if err != nil {
				t.Fatalf("Agent.Initialize() error = %v", err)
			}

			if tc.shouldError {
				// Test error case synchronously
				err := agent.Start(ctx, tc.port)
				if err == nil {
					t.Errorf("Agent.Start(%d) should have returned error", tc.port)
				}
			} else {
				// Test success case asynchronously (since it blocks)
				startErr := make(chan error, 1)
				go func() {
					startErr <- agent.Start(ctx, tc.port)
				}()

				// Give server time to start
				time.Sleep(100 * time.Millisecond)

				// Verify success by attempting connection (or that it's listening)
				if tc.port > 0 {
					// For specific ports, verify connection works
					verifyServerOnPort(t, tc.port, true, fmt.Sprintf("agent port %d should be listening", tc.port))
				}

				// Cleanup - cancel context to stop agent
				cancel()

				// Verify start completed without error (or with expected error)
				select {
				case err := <-startErr:
					if err != nil && err != http.ErrServerClosed && err.Error() != "context canceled" {
						t.Errorf("Agent.Start(%d) unexpected error = %v", tc.port, err)
					}
				case <-time.After(1 * time.Second):
					// Start is still blocking, which is expected for valid ports
				}
			}
		})
	}
}

// TestBackwardCompatibility ensures existing behavior still works
func TestBackwardCompatibility(t *testing.T) {
	t.Run("Tool_Config_Port_Still_Works_Without_Parameter", func(t *testing.T) {
		tool := NewTool("test-tool")
		testPort := findAvailablePort(t)
		tool.Config = &Config{
			Port:    testPort,
			Address: "localhost",
		}

		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		// Start with -1 (should use config)
		serverReady := make(chan bool, 1)

		go func() {
			serverReady <- true
			tool.Start(ctx, -1)
		}()

		<-serverReady
		time.Sleep(200 * time.Millisecond)

		// Verify it's using the config port
		verifyServerOnPort(t, testPort, true, "backward compatibility: config port should work")

		// Cleanup
		tool.Shutdown(ctx)
		cancel()
	})
}

// Helper functions

// findAvailablePort finds an available port for testing
func findAvailablePort(t *testing.T) int {
	listener, err := net.Listen("tcp", ":0")
	if err != nil {
		t.Fatalf("Failed to find available port: %v", err)
	}
	defer listener.Close()

	return listener.Addr().(*net.TCPAddr).Port
}

// verifyServerOnPort checks if a server is listening on a specific port
func verifyServerOnPort(t *testing.T, port int, shouldBeListening bool, message string) {
	t.Helper()

	conn, err := net.DialTimeout("tcp", fmt.Sprintf("localhost:%d", port), 500*time.Millisecond)
	isListening := err == nil
	if conn != nil {
		conn.Close()
	}

	if shouldBeListening && !isListening {
		t.Errorf("%s - expected server on port %d but not found", message, port)
	}
	if !shouldBeListening && isListening {
		t.Errorf("%s - unexpected server found on port %d", message, port)
	}
}
