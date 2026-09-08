package core

import (
	"fmt"
	"strings"

	"github.com/redis/go-redis/v9"
)

// Deprecated: use DB 0 with RedisKeyspace isolation. These numbered-DB
// constants exist only for the precursor compatibility window and are removed
// at the next major release.
const (
	RedisDBServiceDiscovery = 0
	RedisDBRateLimiting     = 1
	RedisDBSessions         = 2
	RedisDBCache            = 3
	RedisDBCircuitBreaker   = 4
	RedisDBMetrics          = 5
	RedisDBTelemetry        = 6
	RedisDBLLMDebug         = 7
	RedisDBExecutionDebug   = 8
	RedisDBReserved9        = 9
	RedisDBReserved10       = 10
	RedisDBReserved11       = 11
	RedisDBReserved12       = 12
	RedisDBReserved13       = 13
	RedisDBReserved14       = 14
	RedisDBReserved15       = 15
	RedisDBReservedStart    = 7
	RedisDBReservedEnd      = 15
)

// IsReservedDB reports whether a database number belonged to the deprecated
// framework-reserved range.
// Deprecated: canonical framework storage uses DB 0 plus RedisKeyspace.
func IsReservedDB(db int) bool {
	return db >= RedisDBReservedStart && db <= RedisDBReservedEnd
}

// GetRedisDBName returns the legacy human-readable logical database name.
// Deprecated: canonical framework storage uses DB 0 plus RedisKeyspace.
func GetRedisDBName(db int) string {
	switch db {
	case RedisDBServiceDiscovery:
		return "Service Discovery"
	case RedisDBRateLimiting:
		return "Rate Limiting"
	case RedisDBSessions:
		return "Sessions"
	case RedisDBCache:
		return "Cache"
	case RedisDBCircuitBreaker:
		return "Circuit Breaker"
	case RedisDBMetrics:
		return "Metrics"
	case RedisDBTelemetry:
		return "Telemetry"
	case RedisDBLLMDebug:
		return "LLM Debug"
	case RedisDBExecutionDebug:
		return "Execution Debug"
	default:
		if IsReservedDB(db) {
			return fmt.Sprintf("Reserved DB %d", db)
		}
		return fmt.Sprintf("DB %d", db)
	}
}

// ParseStandaloneRedisURLForCompatibility parses the deprecated numbered-DB
// standalone form. Canonical code should use ParseStandaloneRedisURL, whose
// schema is fixed to DB 0.
// Deprecated: use ParseStandaloneRedisURL and DB-0 RedisKeyspace isolation.
func ParseStandaloneRedisURLForCompatibility(rawURL string) (RedisConnectionConfig, error) {
	if !strings.Contains(strings.TrimSpace(rawURL), "://") {
		rawURL = "redis://" + strings.TrimSpace(rawURL)
	}
	return parseStandaloneRedisURL(rawURL, true)
}

// NewRedisUniversalClientForCompatibility constructs a client that may select
// a numbered standalone database. It exists only for deprecated adapters that
// have not yet moved to DB 0 plus RedisKeyspace isolation.
// Deprecated: use NewRedisUniversalClient with DB 0.
func NewRedisUniversalClientForCompatibility(config RedisConnectionConfig) (redis.UniversalClient, error) {
	return newRedisUniversalClientWithCompatibility(config, true, true)
}
