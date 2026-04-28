package telemetry

import "time"

// Config configures the telemetry system
type Config struct {
	// Basic settings
	Enabled     bool
	ServiceName string
	ServiceType string // "tool" or "agent" - automatically inferred from component type
	Endpoint    string
	Provider    string // "otel", "prometheus", "statsd"

	// Cardinality control
	CardinalityLimit  int
	CardinalityLimits map[string]int // Per-label limits

	// Circuit breaker configuration
	CircuitBreaker CircuitConfig

	// PII redaction
	PIIRedaction bool
	PIIPatterns  []string
}

// Profile represents a pre-configured telemetry profile
type Profile string

const (
	ProfileDevelopment Profile = "development"
	ProfileStaging     Profile = "staging"
	ProfileProduction  Profile = "production"
)

// Profiles contains pre-configured telemetry profiles
var Profiles = map[Profile]Config{
	ProfileDevelopment: {
		Enabled:          true,
		Endpoint:         "localhost:4318",
		CardinalityLimit: 50000,
		CircuitBreaker: CircuitConfig{
			Enabled: false,
		},
		PIIRedaction: false,
	},
	ProfileStaging: {
		Enabled:          true,
		Endpoint:         "otel-collector.staging:4318",
		CardinalityLimit: 20000,
		CircuitBreaker: CircuitConfig{
			Enabled:      true,
			MaxFailures:  10,
			RecoveryTime: 15 * time.Second,
		},
		PIIRedaction: true,
	},
	ProfileProduction: {
		Enabled:          true,
		Endpoint:         "otel-collector.prod:4318", // Override with env var
		CardinalityLimit: 10000,
		CircuitBreaker: CircuitConfig{
			Enabled:      true,
			MaxFailures:  10,
			RecoveryTime: 30 * time.Second,
			HalfOpenMax:  5,
		},
		PIIRedaction: true,
		CardinalityLimits: map[string]int{
			"agent_id":   100,
			"capability": 50,
			"error_type": 50,
			"user_id":    100,
		},
	},
}

// UseProfile returns a configuration based on a profile name
func UseProfile(profile Profile) Config {
	if config, ok := Profiles[profile]; ok {
		return config
	}
	// Default to development profile
	return Profiles[ProfileDevelopment]
}

// WithOverrides applies overrides to a config
func (c Config) WithOverrides(overrides Config) Config {
	// Override non-zero values
	if overrides.Enabled {
		c.Enabled = overrides.Enabled
	}
	if overrides.ServiceName != "" {
		c.ServiceName = overrides.ServiceName
	}
	if overrides.ServiceType != "" {
		c.ServiceType = overrides.ServiceType
	}
	if overrides.Endpoint != "" {
		c.Endpoint = overrides.Endpoint
	}
	if overrides.Provider != "" {
		c.Provider = overrides.Provider
	}
	if overrides.CardinalityLimit > 0 {
		c.CardinalityLimit = overrides.CardinalityLimit
	}
	if overrides.CardinalityLimits != nil {
		c.CardinalityLimits = overrides.CardinalityLimits
	}
	if overrides.CircuitBreaker.Enabled {
		c.CircuitBreaker = overrides.CircuitBreaker
	}
	if overrides.PIIRedaction {
		c.PIIRedaction = overrides.PIIRedaction
	}
	if len(overrides.PIIPatterns) > 0 {
		c.PIIPatterns = overrides.PIIPatterns
	}

	return c
}
