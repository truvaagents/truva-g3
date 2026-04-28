package ai

import (
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/truvaagents/truva-g3/core"
	"github.com/truvaagents/truva-g3/telemetry"
)

// ProviderFactory defines the interface for AI provider factories
type ProviderFactory interface {
	// Create creates a new AI client instance with the given configuration
	Create(config *AIConfig) core.AIClient

	// DetectEnvironment checks if this provider can be used with current environment
	// Returns priority (higher = preferred) and availability
	DetectEnvironment() (priority int, available bool)

	// Name returns the provider's name
	Name() string

	// Description returns a human-readable description
	Description() string
}

// ProviderRegistry manages registered AI providers
type ProviderRegistry struct {
	mu        sync.RWMutex
	providers map[string]ProviderFactory
}

// Global registry instance
var registry = &ProviderRegistry{
	providers: make(map[string]ProviderFactory),
}

// Register registers a new AI provider factory
// This is typically called from init() functions in provider packages
func Register(factory ProviderFactory) error {
	if factory == nil {
		return fmt.Errorf("factory cannot be nil")
	}

	name := factory.Name()
	if name == "" {
		return fmt.Errorf("factory.Name() cannot be empty")
	}

	registry.mu.Lock()
	defer registry.mu.Unlock()

	if _, exists := registry.providers[name]; exists {
		return fmt.Errorf("provider '%s' already registered", name)
	}

	registry.providers[name] = factory
	return nil
}

// MustRegister registers a provider and panics on error
// Use this in init() functions where errors cannot be handled
func MustRegister(factory ProviderFactory) {
	if err := Register(factory); err != nil {
		panic(fmt.Sprintf("failed to register provider: %v", err))
	}
}

// GetProvider retrieves a registered provider by name
func GetProvider(name string) (ProviderFactory, bool) {
	registry.mu.RLock()
	defer registry.mu.RUnlock()

	factory, exists := registry.providers[name]
	return factory, exists
}

// ListProviders returns all registered provider names
func ListProviders() []string {
	registry.mu.RLock()
	defer registry.mu.RUnlock()

	names := make([]string, 0, len(registry.providers))
	for name := range registry.providers {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// GetProviderInfo returns information about all registered providers
func GetProviderInfo() []ProviderInfo {
	registry.mu.RLock()
	defer registry.mu.RUnlock()

	info := make([]ProviderInfo, 0, len(registry.providers))
	for name, factory := range registry.providers {
		priority, available := factory.DetectEnvironment()
		info = append(info, ProviderInfo{
			Name:        name,
			Description: factory.Description(),
			Available:   available,
			Priority:    priority,
		})
	}

	// Sort by priority (highest first), then by name
	sort.Slice(info, func(i, j int) bool {
		if info[i].Priority != info[j].Priority {
			return info[i].Priority > info[j].Priority
		}
		return info[i].Name < info[j].Name
	})

	return info
}

// ProviderInfo contains information about a registered provider
type ProviderInfo struct {
	Name        string
	Description string
	Available   bool
	Priority    int
}

// SubProviderEnumerator is an optional interface for provider factories
// that manage multiple sub-providers under a single registration.
// Example: The OpenAI factory manages groq, deepseek, xai etc.
// Factories that don't implement this interface fall back to DetectEnvironment().
type SubProviderEnumerator interface {
	DetectAvailableAliases() []AliasAvailability
}

// AliasAvailability represents a detected provider alias with its priority
type AliasAvailability struct {
	Alias        string // Full alias: "openai.groq", "anthropic"
	ProviderName string // Base factory name for registry lookup: "openai", "anthropic"
	Priority     int    // Detection priority (higher = tried first)
}

// DetectAvailableProviders discovers all available provider aliases from the registry.
// It iterates all registered factories, using SubProviderEnumerator for factories
// that manage multiple sub-providers (e.g., OpenAI factory handles groq, deepseek, etc.)
// and falling back to DetectEnvironment() for simple factories.
// Returns aliases sorted by priority (highest first), with unavailable providers excluded.
func DetectAvailableProviders(logger core.Logger) []AliasAvailability {
	startTime := time.Now()

	registry.mu.RLock()
	defer registry.mu.RUnlock()

	if logger != nil {
		logger.Info("Starting provider alias detection", map[string]interface{}{
			"operation":            "ai_provider_alias_detection",
			"registered_providers": len(registry.providers),
		})
	}

	var available []AliasAvailability

	for name, factory := range registry.providers {
		// Check if factory supports sub-provider enumeration (optional interface pattern)
		if enumerator, ok := factory.(SubProviderEnumerator); ok {
			aliases := enumerator.DetectAvailableAliases()
			available = append(available, aliases...)

			if logger != nil {
				aliasNames := make([]string, len(aliases))
				for i, a := range aliases {
					aliasNames[i] = a.Alias
				}
				logger.Debug("Sub-provider enumeration", map[string]interface{}{
					"operation":        "ai_provider_alias_detection",
					"factory":          name,
					"detected_aliases": aliasNames,
					"count":            len(aliases),
				})
			}
		} else {
			// Simple factory — use DetectEnvironment() and wrap as single alias
			priority, isAvailable := factory.DetectEnvironment()
			if isAvailable {
				available = append(available, AliasAvailability{
					Alias:        name,
					ProviderName: name,
					Priority:     priority,
				})

				if logger != nil {
					logger.Debug("Provider detected via environment", map[string]interface{}{
						"operation": "ai_provider_alias_detection",
						"provider":  name,
						"priority":  priority,
					})
				}
			}
		}
	}

	// Sort by priority (highest first), then by alias name for deterministic ordering
	sort.Slice(available, func(i, j int) bool {
		if available[i].Priority != available[j].Priority {
			return available[i].Priority > available[j].Priority
		}
		return available[i].Alias < available[j].Alias
	})

	detectionDuration := time.Since(startTime)

	// Record telemetry
	telemetry.Histogram("ai.provider.detection.duration_ms",
		float64(detectionDuration.Milliseconds()),
		"module", telemetry.ModuleAI,
		"mode", "alias_enumeration",
	)
	telemetry.Counter("ai.provider.detection",
		"module", telemetry.ModuleAI,
		"mode", "alias_enumeration",
		"count", fmt.Sprintf("%d", len(available)),
	)

	if logger != nil {
		aliasNames := make([]string, len(available))
		for i, a := range available {
			aliasNames[i] = a.Alias
		}
		logger.Info("Provider alias detection complete", map[string]interface{}{
			"operation":        "ai_provider_alias_detection",
			"detected_count":   len(available),
			"detected_aliases": aliasNames,
			"duration_ms":      detectionDuration.Milliseconds(),
		})
	}

	return available
}

// detectBestProviderWithAlias finds the best available provider and returns both
// the provider name and the full alias. Used by client.go to pass the alias
// to the factory, avoiding duplicate auto-detection in resolveCredentials().
// This is the single selection entry point — all selection telemetry and error
// logging is centralized here.
func detectBestProviderWithAlias(logger core.Logger) (providerName, alias string, err error) {
	available := DetectAvailableProviders(logger)

	if len(available) == 0 {
		telemetry.Counter("ai.provider.detection",
			"module", telemetry.ModuleAI,
			"status", "no_providers",
		)

		if logger != nil {
			logger.Error("No AI providers detected in environment", map[string]interface{}{
				"operation":         "ai_provider_detection",
				"checked_providers": len(registry.providers),
				"environment_check": "failed",
				"suggestion":        "Set API keys (OPENAI_API_KEY, ANTHROPIC_API_KEY, etc.)",
			})
		}
		return "", "", fmt.Errorf("no provider detected in environment")
	}

	selected := available[0]

	telemetry.Counter("ai.provider.selected",
		"module", telemetry.ModuleAI,
		"provider", selected.ProviderName,
		"alias", selected.Alias,
	)

	if logger != nil {
		alternatives := make([]string, 0, len(available)-1)
		for _, a := range available[1:] {
			alternatives = append(alternatives, a.Alias)
		}
		logger.Info("AI provider selected", map[string]interface{}{
			"operation":             "ai_provider_selection",
			"selected_provider":     selected.ProviderName,
			"selected_alias":        selected.Alias,
			"selection_priority":    selected.Priority,
			"total_candidates":      len(available),
			"alternative_providers": alternatives,
		})
	}

	return selected.ProviderName, selected.Alias, nil
}
