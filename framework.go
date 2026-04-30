// Package framework provides the main entry point for the TruvaG3 framework
// This is a monolithic package that includes all TruvaG3 capabilities
//
// Import paths:
//   - github.com/truvaagents/truva-g3 - Main framework package
//   - github.com/truvaagents/truva-g3/core - Core agent framework
//   - github.com/truvaagents/truva-g3/ai - AI capabilities
//   - github.com/truvaagents/truva-g3/orchestration - Multi-agent orchestration
//   - github.com/truvaagents/truva-g3/telemetry - Observability
//   - github.com/truvaagents/truva-g3/resilience - Resilience patterns
package framework

import (
	"context"

	"github.com/truvaagents/truva-g3/core"
)

// Re-export core types for backward compatibility
type (
	// Core agent types
	Agent      = core.Agent
	BaseAgent  = core.BaseAgent
	Capability = core.Capability

	// Configuration types
	Config            = core.Config
	Option            = core.Option
	HTTPConfig        = core.HTTPConfig
	CORSConfig        = core.CORSConfig
	DiscoveryConfig   = core.DiscoveryConfig
	AIConfig          = core.AIConfig
	TelemetryConfig   = core.TelemetryConfig
	MemoryConfig      = core.MemoryConfig
	ResilienceConfig  = core.ResilienceConfig
	LoggingConfig     = core.LoggingConfig
	DevelopmentConfig = core.DevelopmentConfig
	KubernetesConfig  = core.KubernetesConfig

	// Interfaces
	Logger    = core.Logger
	Discovery = core.Discovery
	Memory    = core.Memory
	Telemetry = core.Telemetry
	AIClient  = core.AIClient

	// Service types
	ServiceRegistration = core.ServiceRegistration
	HealthStatus        = core.HealthStatus

	// AI types
	AIOptions  = core.AIOptions
	AIResponse = core.AIResponse
	TokenUsage = core.TokenUsage

	// Telemetry types
	Span = core.Span
)

// Re-export constants
const (
	HealthHealthy   = core.HealthHealthy
	HealthUnhealthy = core.HealthUnhealthy
	HealthUnknown   = core.HealthUnknown
)

// Re-export core functions
var (
	NewBaseAgent           = core.NewBaseAgent
	NewBaseAgentWithConfig = core.NewBaseAgentWithConfig
	NewFramework           = core.NewFramework
	NewRedisDiscovery      = core.NewRedisDiscovery
	NewMockDiscovery       = core.NewMockDiscovery
	NewMemoryStore         = core.NewMemoryStore
	NewMemoryStoreSweeper  = core.NewMemoryStoreSweeper
	NewConfig              = core.NewConfig
	DefaultConfig          = core.DefaultConfig

	// Configuration options
	WithName                  = core.WithName
	WithPort                  = core.WithPort
	WithAddress               = core.WithAddress
	WithNamespace             = core.WithNamespace
	WithCORS                  = core.WithCORS
	WithCORSDefaults          = core.WithCORSDefaults
	WithRedisURL              = core.WithRedisURL
	WithDiscovery             = core.WithDiscovery
	WithDiscoveryCacheEnabled = core.WithDiscoveryCacheEnabled
	WithOpenAIAPIKey          = core.WithOpenAIAPIKey
	WithAI                    = core.WithAI
	WithAIModel               = core.WithAIModel
	WithTelemetry             = core.WithTelemetry
	WithEnableMetrics         = core.WithEnableMetrics
	WithEnableTracing         = core.WithEnableTracing
	WithOTELEndpoint          = core.WithOTELEndpoint
	WithLogLevel              = core.WithLogLevel
	WithLogFormat             = core.WithLogFormat
	WithCircuitBreaker        = core.WithCircuitBreaker
	WithRetry                 = core.WithRetry
	WithKubernetes            = core.WithKubernetes
	WithConfigFile            = core.WithConfigFile
	WithDevelopmentMode       = core.WithDevelopmentMode
	WithMockAI                = core.WithMockAI
	WithMockDiscovery         = core.WithMockDiscovery
)

// RunAgent provides a simplified way to run an agent
// DEPRECATED: Use NewFramework with options instead
func RunAgent(agent Agent, port int) error {
	framework, err := core.NewFramework(agent, core.WithPort(port))
	if err != nil {
		return err
	}
	ctx := context.Background()
	return framework.Run(ctx)
}
