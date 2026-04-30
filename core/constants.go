package core

import "time"

// Environment Variables - TruvaG3 Protocol
const (
	// Schema Discovery Protocol
	EnvValidatePayloads = "TRUVAG3_VALIDATE_PAYLOADS" // Enable Phase 3 schema validation

	// Service Discovery
	EnvRedisURL    = "REDIS_URL"                // Redis connection URL for discovery
	EnvNamespace   = "NAMESPACE"                // Kubernetes namespace for service isolation
	EnvServiceName = "TRUVAG3_K8S_SERVICE_NAME" // Service name in Kubernetes

	// Common Configuration
	EnvPort    = "PORT"     // HTTP server port
	EnvDevMode = "DEV_MODE" // Development mode flag
)

// Schema Discovery Constants
const (
	// SchemaEndpointSuffix is appended to capability endpoints to form schema endpoints
	// Example: /api/capabilities/weather + SchemaEndpointSuffix = /api/capabilities/weather/schema
	SchemaEndpointSuffix = "/schema"
)

// Redis Cache Defaults
const (
	// DefaultRedisPrefix is the default key prefix for schema cache entries in Redis
	// Format: <prefix><tool-name>:<capability-name>
	// Example: truvag3:schema:weather-service:current_weather
	DefaultRedisPrefix = "truvag3:schema:"

	// DefaultSchemaCacheTTL is the default TTL for cached schemas in Redis
	// Schemas rarely change, so 24 hours is a reasonable default
	DefaultSchemaCacheTTL = 24 * time.Hour
)
