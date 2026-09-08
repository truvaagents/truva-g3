package orchestration

import (
	"fmt"
	"os"
	"strings"

	"github.com/redis/go-redis/v9"
	"github.com/truvaagents/truva-g3/core"
)

// newOwnedRedisUniversalClient is the single compatibility bridge used by
// direct Redis-owning orchestration constructors. Canonical application
// composition injects a client through redisprovider instead.
func newOwnedRedisUniversalClient(
	explicitURL string,
	compatibilityDB int,
	logger core.Logger,
) (redis.UniversalClient, core.RedisConnectionConfig, error) {
	var (
		resolution core.RedisConnectionResolution
		err        error
	)
	if strings.TrimSpace(explicitURL) != "" {
		connection, parseErr := core.ParseStandaloneRedisURLForCompatibility(explicitURL)
		if parseErr != nil {
			return nil, core.RedisConnectionConfig{}, fmt.Errorf("parse standalone Redis compatibility URL: %w", parseErr)
		}
		connection.DB = compatibilityDB
		resolution, err = core.ResolveRedisConnectionConfig(&connection, nil)
	} else {
		resolution, err = core.ResolveRedisConnectionConfig(nil, os.LookupEnv)
		if err == nil && compatibilityDB != 0 {
			resolution.Config.DB = compatibilityDB
			resolution, err = core.ResolveRedisConnectionConfig(&resolution.Config, nil)
		}
	}
	if err != nil {
		return nil, core.RedisConnectionConfig{}, fmt.Errorf("resolve Redis connection: %w", err)
	}
	if logger != nil {
		for _, diagnostic := range resolution.Diagnostics {
			logger.Warn("Redis configuration notice", map[string]interface{}{
				"operation":  "redis_configuration_notice",
				"diagnostic": diagnostic,
			})
		}
	}
	client, err := core.NewRedisUniversalClientForCompatibility(resolution.Config)
	if err != nil {
		return nil, core.RedisConnectionConfig{}, fmt.Errorf("initialize Redis client: %w", err)
	}
	return client, resolution.Config, nil
}

func orchestrationComponentLogger(logger core.Logger) core.Logger {
	if logger == nil {
		return &core.NoOpLogger{}
	}
	if componentAware, ok := logger.(core.ComponentAwareLogger); ok {
		return componentAware.WithComponent("framework/orchestration")
	}
	return logger
}

// rejectLegacyRedisPrefixForMode keeps the precursor raw-prefix schema on
// topologies where Redis does not enforce hash-slot co-location. Canonical
// cluster composition must use the typed RedisKeyspace options instead.
func rejectLegacyRedisPrefixForMode(mode core.RedisMode, legacyPrefix bool, adapter string) error {
	if legacyPrefix && mode == core.RedisModeCluster {
		return fmt.Errorf(
			"%s raw Redis key-prefix compatibility is unavailable in cluster mode; use the typed Redis keyspace option: %w",
			adapter,
			core.ErrInvalidConfiguration,
		)
	}
	return nil
}

func rejectLegacyRedisPrefixForClient(client redis.UniversalClient, legacyPrefix bool, adapter string) error {
	if _, cluster := client.(*redis.ClusterClient); cluster {
		return rejectLegacyRedisPrefixForMode(core.RedisModeCluster, legacyPrefix, adapter)
	}
	return nil
}
