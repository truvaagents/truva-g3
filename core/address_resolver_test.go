package core

import (
	"context"
	"testing"
)

func TestResolveServiceAddress(t *testing.T) {
	tests := []struct {
		name            string
		config          *Config
		expectedAddress string
		expectedPort    int
		description     string
	}{
		{
			name: "kubernetes_with_service",
			config: &Config{
				Address: "localhost",
				Port:    8080,
				Kubernetes: KubernetesConfig{
					Enabled:      true,
					ServiceName:  "my-service",
					ServicePort:  80,
					PodNamespace: "production",
				},
			},
			expectedAddress: "my-service.production.svc.cluster.local",
			expectedPort:    80,
			description:     "Should use Kubernetes Service DNS when in K8s with service name",
		},
		{
			name: "kubernetes_default_namespace",
			config: &Config{
				Address: "localhost",
				Port:    8080,
				Kubernetes: KubernetesConfig{
					Enabled:      true,
					ServiceName:  "my-service",
					ServicePort:  443,
					PodNamespace: "", // Empty namespace
				},
			},
			expectedAddress: "my-service.default.svc.cluster.local",
			expectedPort:    443,
			description:     "Should use 'default' namespace when namespace is empty",
		},
		{
			name: "kubernetes_without_service_name",
			config: &Config{
				Address: "192.168.1.100",
				Port:    9090,
				Kubernetes: KubernetesConfig{
					Enabled:      true,
					ServiceName:  "", // No service name
					ServicePort:  80,
					PodNamespace: "production",
				},
			},
			expectedAddress: "192.168.1.100",
			expectedPort:    9090,
			description:     "Should fall back to regular address when no K8s service name",
		},
		{
			name: "non_kubernetes_environment",
			config: &Config{
				Address: "0.0.0.0",
				Port:    3000,
				Kubernetes: KubernetesConfig{
					Enabled: false, // Not in Kubernetes
				},
			},
			expectedAddress: "0.0.0.0",
			expectedPort:    3000,
			description:     "Should use configured address when not in Kubernetes",
		},
		{
			name: "default_address_fallback",
			config: &Config{
				Address: "", // Empty address
				Port:    5000,
				Kubernetes: KubernetesConfig{
					Enabled: false,
				},
			},
			expectedAddress: "localhost",
			expectedPort:    5000,
			description:     "Should default to localhost when address is empty",
		},
		{
			name:            "nil_config",
			config:          nil,
			expectedAddress: "localhost",
			expectedPort:    8080,
			description:     "Should handle nil config gracefully",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			address, port := ResolveServiceAddress(tt.config, nil)

			if address != tt.expectedAddress {
				t.Errorf("%s: address = %v, want %v", tt.description, address, tt.expectedAddress)
			}

			if port != tt.expectedPort {
				t.Errorf("%s: port = %v, want %v", tt.description, port, tt.expectedPort)
			}
		})
	}
}

func TestBuildServiceMetadata(t *testing.T) {
	tests := []struct {
		name     string
		config   *Config
		expected map[string]interface{}
	}{
		{
			name: "kubernetes_metadata",
			config: &Config{
				Namespace: "truvag3",
				Port:      8080,
				Kubernetes: KubernetesConfig{
					Enabled:      true,
					PodName:      "my-pod-abc123",
					PodNamespace: "production",
					ServiceName:  "my-service",
					ServicePort:  80,
					PodIP:        "10.1.2.3",
					NodeName:     "node-1",
				},
			},
			expected: map[string]interface{}{
				"namespace":      "truvag3",
				"pod_name":       "my-pod-abc123",
				"pod_namespace":  "production",
				"service_name":   "my-service",
				"container_port": "8080",
				"service_port":   "80",
				"pod_ip":         "10.1.2.3",
				"node_name":      "node-1",
			},
		},
		{
			name: "non_kubernetes",
			config: &Config{
				Namespace: "local",
				Kubernetes: KubernetesConfig{
					Enabled: false,
				},
			},
			expected: map[string]interface{}{
				"namespace": "local",
			},
		},
		{
			name:     "nil_config",
			config:   nil,
			expected: map[string]interface{}{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			metadata := BuildServiceMetadata(tt.config)

			if len(metadata) != len(tt.expected) {
				t.Errorf("metadata length = %v, want %v", len(metadata), len(tt.expected))
			}

			for key, expectedValue := range tt.expected {
				if actualValue, exists := metadata[key]; !exists {
					t.Errorf("missing metadata key: %s", key)
				} else if actualValue != expectedValue {
					t.Errorf("metadata[%s] = %v, want %v", key, actualValue, expectedValue)
				}
			}
		})
	}
}

func TestToolWithKubernetesConfig(t *testing.T) {
	// Test that tools properly use the shared resolver
	config := &Config{
		Name:    "test-tool",
		Address: "localhost",
		Port:    8080,
		Discovery: DiscoveryConfig{
			Enabled: true,
		},
		Kubernetes: KubernetesConfig{
			Enabled:      true,
			ServiceName:  "test-tool-service",
			ServicePort:  80,
			PodNamespace: "test-namespace",
		},
	}

	tool := NewToolWithConfig(config)

	// Verify tool has config
	if tool.Config == nil {
		t.Fatal("Tool should have config")
	}

	// Mock registry to capture registration
	mockRegistry := &mockRegistryForTest{
		registrations: make(map[string]*ServiceInfo),
	}
	tool.Registry = mockRegistry

	// Initialize tool
	ctx := context.Background()
	err := tool.Initialize(ctx)
	if err != nil {
		t.Fatalf("Failed to initialize tool: %v", err)
	}

	// Check registration used K8s Service DNS
	if len(mockRegistry.registrations) != 1 {
		t.Fatalf("Expected 1 registration, got %d", len(mockRegistry.registrations))
	}

	var registration *ServiceInfo
	for _, reg := range mockRegistry.registrations {
		registration = reg
		break
	}

	expectedAddress := "test-tool-service.test-namespace.svc.cluster.local"
	if registration.Address != expectedAddress {
		t.Errorf("Tool registration address = %v, want %v", registration.Address, expectedAddress)
	}

	if registration.Port != 80 {
		t.Errorf("Tool registration port = %v, want %v", registration.Port, 80)
	}

	if registration.Type != ComponentTypeTool {
		t.Errorf("Tool registration type = %v, want %v", registration.Type, ComponentTypeTool)
	}
}

func TestAgentWithKubernetesConfig(t *testing.T) {
	// Test that agents properly use the shared resolver
	config := &Config{
		Name:    "test-agent",
		Address: "localhost",
		Port:    8090,
		Discovery: DiscoveryConfig{
			Enabled: true,
		},
		Kubernetes: KubernetesConfig{
			Enabled:      true,
			ServiceName:  "test-agent-service",
			ServicePort:  443,
			PodNamespace: "prod",
		},
	}

	agent := NewBaseAgentWithConfig(config)

	// Mock discovery
	mockDiscovery := NewMockDiscovery()
	agent.Discovery = mockDiscovery

	// Initialize agent
	ctx := context.Background()
	err := agent.Initialize(ctx)
	if err != nil {
		t.Fatalf("Failed to initialize agent: %v", err)
	}

	// Check registration used K8s Service DNS
	services, _ := mockDiscovery.Discover(ctx, DiscoveryFilter{
		Name: "test-agent",
	})

	if len(services) != 1 {
		t.Fatalf("Expected 1 registration, got %d", len(services))
	}

	registration := services[0]

	expectedAddress := "test-agent-service.prod.svc.cluster.local"
	if registration.Address != expectedAddress {
		t.Errorf("Agent registration address = %v, want %v", registration.Address, expectedAddress)
	}

	if registration.Port != 443 {
		t.Errorf("Agent registration port = %v, want %v", registration.Port, 443)
	}

	if registration.Type != ComponentTypeAgent {
		t.Errorf("Agent registration type = %v, want %v", registration.Type, ComponentTypeAgent)
	}
}

// mockRegistryForTest is a simple mock for testing
type mockRegistryForTest struct {
	registrations map[string]*ServiceInfo
}

func (m *mockRegistryForTest) Register(ctx context.Context, info *ServiceInfo) error {
	m.registrations[info.ID] = info
	return nil
}

func (m *mockRegistryForTest) UpdateHealth(ctx context.Context, id string, status HealthStatus) error {
	if reg, exists := m.registrations[id]; exists {
		reg.Health = status
	}
	return nil
}

func (m *mockRegistryForTest) Unregister(ctx context.Context, id string) error {
	delete(m.registrations, id)
	return nil
}
