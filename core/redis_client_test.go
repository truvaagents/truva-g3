package core

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestGetRedisDBName(t *testing.T) {
	tests := []struct {
		name     string
		db       int
		expected string
	}{
		// Named databases
		{"ServiceDiscovery", RedisDBServiceDiscovery, "Service Discovery"},
		{"RateLimiting", RedisDBRateLimiting, "Rate Limiting"},
		{"Sessions", RedisDBSessions, "Sessions"},
		{"Cache", RedisDBCache, "Cache"},
		{"CircuitBreaker", RedisDBCircuitBreaker, "Circuit Breaker"},
		{"Metrics", RedisDBMetrics, "Metrics"},
		{"Telemetry", RedisDBTelemetry, "Telemetry"},
		{"LLMDebug", RedisDBLLMDebug, "LLM Debug"},
		{"ExecutionDebug", RedisDBExecutionDebug, "Execution Debug"},

		// Reserved databases (9-15)
		{"Reserved9", RedisDBReserved9, "Reserved DB 9"},
		{"Reserved10", RedisDBReserved10, "Reserved DB 10"},
		{"Reserved15", RedisDBReserved15, "Reserved DB 15"},

		// Non-reserved, unnamed databases (outside 0-15 range)
		{"DB16", 16, "DB 16"},
		{"DB100", 100, "DB 100"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := GetRedisDBName(tt.db)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestIsReservedDB(t *testing.T) {
	tests := []struct {
		name     string
		db       int
		expected bool
	}{
		// Not reserved (application DBs 0-6)
		{"DB0", 0, false},
		{"DB6", 6, false},

		// Reserved (framework DBs 7-15)
		{"DB7", 7, true},
		{"DB8", 8, true},
		{"DB15", 15, true},

		// Not reserved (beyond standard range)
		{"DB16", 16, false},
		{"DB100", 100, false},
		{"NegativeDB", -1, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := IsReservedDB(tt.db)
			assert.Equal(t, tt.expected, result)
		})
	}
}
