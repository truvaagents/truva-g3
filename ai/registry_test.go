package ai

// IMPORTANT: Tests in this file manipulate the global provider registry directly.
// Do NOT use t.Parallel() in any test that reads or writes registry.providers,
// as concurrent access would cause race conditions and flaky tests.
// Each test saves and restores the registry state via defers.

import (
	"context"
	"testing"

	"github.com/truvaagents/truva-g3/core"
)

// MockProviderFactory implements ProviderFactory for testing
type MockProviderFactory struct {
	name        string
	description string
	priority    int
	available   bool
	createFunc  func(*AIConfig) core.AIClient
}

func (m *MockProviderFactory) Create(config *AIConfig) core.AIClient {
	if m.createFunc != nil {
		return m.createFunc(config)
	}
	return &mockRegistryAIClient{}
}

func (m *MockProviderFactory) DetectEnvironment() (int, bool) {
	return m.priority, m.available
}

func (m *MockProviderFactory) Name() string {
	return m.name
}

func (m *MockProviderFactory) Description() string {
	return m.description
}

// mockRegistryAIClient implements core.AIClient for testing registry
type mockRegistryAIClient struct{}

func (m *mockRegistryAIClient) GenerateResponse(ctx context.Context, prompt string, options *core.AIOptions) (*core.AIResponse, error) {
	return &core.AIResponse{
		Content: "mock response",
		Model:   "mock-model",
	}, nil
}

func TestRegister(t *testing.T) {
	// Clear registry for testing
	registry.mu.Lock()
	registry.providers = make(map[string]ProviderFactory)
	registry.mu.Unlock()

	tests := []struct {
		name      string
		factory   ProviderFactory
		wantError bool
	}{
		{
			name: "register new provider",
			factory: &MockProviderFactory{
				name:        "test-provider",
				description: "Test Provider",
			},
			wantError: false,
		},
		{
			name: "register duplicate provider",
			factory: &MockProviderFactory{
				name: "test-provider",
			},
			wantError: true,
		},
		{
			name:      "register nil factory",
			factory:   nil,
			wantError: true,
		},
		{
			name: "register empty name",
			factory: &MockProviderFactory{
				name:        "",
				description: "Empty name",
			},
			wantError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := Register(tt.factory)
			if (err != nil) != tt.wantError {
				t.Errorf("Register() error = %v, wantError %v", err, tt.wantError)
			}
		})
	}
}

func TestGetProvider(t *testing.T) {
	// Clear and setup registry
	registry.mu.Lock()
	registry.providers = make(map[string]ProviderFactory)
	testFactory := &MockProviderFactory{
		name:        "test-provider",
		description: "Test Provider",
	}
	registry.providers["test-provider"] = testFactory
	registry.mu.Unlock()

	tests := []struct {
		name         string
		providerName string
		wantExists   bool
	}{
		{
			name:         "get existing provider",
			providerName: "test-provider",
			wantExists:   true,
		},
		{
			name:         "get non-existent provider",
			providerName: "non-existent",
			wantExists:   false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			factory, exists := GetProvider(tt.providerName)
			if exists != tt.wantExists {
				t.Errorf("GetProvider() exists = %v, want %v", exists, tt.wantExists)
			}
			if exists && factory == nil {
				t.Error("GetProvider() returned nil factory for existing provider")
			}
		})
	}
}

func TestListProviders(t *testing.T) {
	// Clear and setup registry
	registry.mu.Lock()
	registry.providers = make(map[string]ProviderFactory)
	registry.providers["provider-a"] = &MockProviderFactory{name: "provider-a"}
	registry.providers["provider-b"] = &MockProviderFactory{name: "provider-b"}
	registry.providers["provider-c"] = &MockProviderFactory{name: "provider-c"}
	registry.mu.Unlock()

	providers := ListProviders()

	// Check count
	if len(providers) != 3 {
		t.Errorf("ListProviders() returned %d providers, want 3", len(providers))
	}

	// Check sorting
	expected := []string{"provider-a", "provider-b", "provider-c"}
	for i, p := range providers {
		if p != expected[i] {
			t.Errorf("ListProviders()[%d] = %s, want %s", i, p, expected[i])
		}
	}
}

func TestDetectBestProvider(t *testing.T) {
	// Clear and setup registry with providers of different priorities
	registry.mu.Lock()
	registry.providers = make(map[string]ProviderFactory)
	registry.providers["high-priority"] = &MockProviderFactory{
		name:      "high-priority",
		priority:  100,
		available: true,
	}
	registry.providers["medium-priority"] = &MockProviderFactory{
		name:      "medium-priority",
		priority:  50,
		available: true,
	}
	registry.providers["low-priority"] = &MockProviderFactory{
		name:      "low-priority",
		priority:  10,
		available: true,
	}
	registry.providers["unavailable"] = &MockProviderFactory{
		name:      "unavailable",
		priority:  200,
		available: false,
	}
	registry.mu.Unlock()

	provider, err := detectBestProvider(nil)
	if err != nil {
		t.Fatalf("detectBestProvider() error = %v", err)
	}

	if provider != "high-priority" {
		t.Errorf("detectBestProvider() = %s, want high-priority", provider)
	}
}

func TestDetectBestProviderNoAvailable(t *testing.T) {
	// Clear registry and add only unavailable providers
	registry.mu.Lock()
	registry.providers = make(map[string]ProviderFactory)
	registry.providers["unavailable1"] = &MockProviderFactory{
		name:      "unavailable1",
		priority:  100,
		available: false,
	}
	registry.providers["unavailable2"] = &MockProviderFactory{
		name:      "unavailable2",
		priority:  50,
		available: false,
	}
	registry.mu.Unlock()

	_, err := detectBestProvider(nil)
	if err == nil {
		t.Error("detectBestProvider() should return error when no providers available")
	}
}

// MockEnumeratingFactory implements both ProviderFactory and SubProviderEnumerator
type MockEnumeratingFactory struct {
	name        string
	description string
	aliases     []AliasAvailability
}

func (m *MockEnumeratingFactory) Create(config *AIConfig) core.AIClient {
	return &mockRegistryAIClient{}
}

func (m *MockEnumeratingFactory) DetectEnvironment() (int, bool) {
	if len(m.aliases) == 0 {
		return 0, false
	}
	return m.aliases[0].Priority, true
}

func (m *MockEnumeratingFactory) Name() string        { return m.name }
func (m *MockEnumeratingFactory) Description() string { return m.description }
func (m *MockEnumeratingFactory) DetectAvailableAliases() []AliasAvailability {
	return m.aliases
}

func TestDetectAvailableProviders(t *testing.T) {
	// Clear and setup registry with mix of simple and enumerating factories
	registry.mu.Lock()
	registry.providers = make(map[string]ProviderFactory)
	registry.providers["simple-provider"] = &MockProviderFactory{
		name:      "simple-provider",
		priority:  80,
		available: true,
	}
	registry.providers["enumerating-provider"] = &MockEnumeratingFactory{
		name: "enumerating-provider",
		aliases: []AliasAvailability{
			{Alias: "enumerating-provider", ProviderName: "enumerating-provider", Priority: 100},
			{Alias: "enumerating-provider.sub1", ProviderName: "enumerating-provider", Priority: 70},
		},
	}
	registry.providers["unavailable"] = &MockProviderFactory{
		name:      "unavailable",
		priority:  200,
		available: false,
	}
	registry.mu.Unlock()

	available := DetectAvailableProviders(nil)

	// Should have 3 entries: enumerating-provider(100), simple-provider(80), enumerating-provider.sub1(70)
	if len(available) != 3 {
		t.Fatalf("Expected 3 available aliases, got %d: %+v", len(available), available)
	}

	// Verify sorted by priority descending
	if available[0].Alias != "enumerating-provider" || available[0].Priority != 100 {
		t.Errorf("Expected first alias 'enumerating-provider' with priority 100, got %q with %d", available[0].Alias, available[0].Priority)
	}
	if available[1].Alias != "simple-provider" || available[1].Priority != 80 {
		t.Errorf("Expected second alias 'simple-provider' with priority 80, got %q with %d", available[1].Alias, available[1].Priority)
	}
	if available[2].Alias != "enumerating-provider.sub1" || available[2].Priority != 70 {
		t.Errorf("Expected third alias 'enumerating-provider.sub1' with priority 70, got %q with %d", available[2].Alias, available[2].Priority)
	}
}

func TestDetectAvailableProviders_Empty(t *testing.T) {
	// Clear registry — only unavailable providers
	registry.mu.Lock()
	registry.providers = make(map[string]ProviderFactory)
	registry.providers["unavailable"] = &MockProviderFactory{
		name:      "unavailable",
		priority:  100,
		available: false,
	}
	registry.mu.Unlock()

	available := DetectAvailableProviders(nil)
	if len(available) != 0 {
		t.Errorf("Expected empty slice, got %d entries: %+v", len(available), available)
	}
}

func TestDetectAvailableProvidersBuiltInPriorityContract(t *testing.T) {
	registry.mu.Lock()
	registry.providers = map[string]ProviderFactory{
		"openai": &MockEnumeratingFactory{
			name: "openai",
			aliases: []AliasAvailability{
				{Alias: "openai", ProviderName: "openai", Priority: 1000},
				{Alias: "openai.openrouter", ProviderName: "openai", Priority: 850},
			},
		},
		"anthropic": &MockProviderFactory{name: "anthropic", priority: 900, available: true},
		"gemini":    &MockProviderFactory{name: "gemini", priority: 800, available: true},
	}
	registry.mu.Unlock()

	available := DetectAvailableProviders(nil)
	want := []string{"openai", "anthropic", "openai.openrouter", "gemini"}
	if len(available) != len(want) {
		t.Fatalf("available = %#v", available)
	}
	for index, alias := range want {
		if available[index].Alias != alias {
			t.Fatalf("available[%d] = %q, want %q", index, available[index].Alias, alias)
		}
	}
}

func TestDetectBestProviderWithAlias(t *testing.T) {
	// Setup registry with an enumerating factory
	registry.mu.Lock()
	registry.providers = make(map[string]ProviderFactory)
	registry.providers["test-enum"] = &MockEnumeratingFactory{
		name: "test-enum",
		aliases: []AliasAvailability{
			{Alias: "test-enum.sub1", ProviderName: "test-enum", Priority: 90},
			{Alias: "test-enum", ProviderName: "test-enum", Priority: 100},
		},
	}
	registry.mu.Unlock()

	provider, alias, err := detectBestProviderWithAlias(nil)
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}
	if provider != "test-enum" {
		t.Errorf("Expected provider 'test-enum', got %q", provider)
	}
	if alias != "test-enum" {
		t.Errorf("Expected alias 'test-enum', got %q", alias)
	}
}

func TestGetProviderInfo(t *testing.T) {
	// Clear and setup registry
	registry.mu.Lock()
	registry.providers = make(map[string]ProviderFactory)
	registry.providers["available-high"] = &MockProviderFactory{
		name:        "available-high",
		description: "High priority available",
		priority:    100,
		available:   true,
	}
	registry.providers["available-low"] = &MockProviderFactory{
		name:        "available-low",
		description: "Low priority available",
		priority:    10,
		available:   true,
	}
	registry.providers["unavailable"] = &MockProviderFactory{
		name:        "unavailable",
		description: "Unavailable provider",
		priority:    50,
		available:   false,
	}
	registry.mu.Unlock()

	info := GetProviderInfo()

	// Check count
	if len(info) != 3 {
		t.Errorf("GetProviderInfo() returned %d providers, want 3", len(info))
	}

	// Check sorting by priority (highest first)
	if info[0].Name != "available-high" {
		t.Errorf("First provider should be available-high, got %s", info[0].Name)
	}

	// Check availability flags
	for _, p := range info {
		switch p.Name {
		case "available-high", "available-low":
			if !p.Available {
				t.Errorf("Provider %s should be available", p.Name)
			}
		case "unavailable":
			if p.Available {
				t.Errorf("Provider %s should not be available", p.Name)
			}
		}
	}
}
