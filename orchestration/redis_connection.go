package orchestration

import (
	"fmt"
	"os"
	"strings"

	"github.com/redis/go-redis/v9"
	"github.com/truvaagents/truva-g3/core"
)

// newOwnedRedisUniversalClient resolves a DB-0 connection for direct owning
// constructors. Injected constructors leave client lifetime to the application.
func newOwnedRedisUniversalClient(explicitURL string, removedSettings ...string) (redis.UniversalClient, core.RedisConnectionConfig, error) {
	for _, name := range removedSettings {
		if value := os.Getenv(name); strings.TrimSpace(value) != "" {
			return nil, core.RedisConnectionConfig{}, fmt.Errorf("%s is unsupported; use DB 0 with RedisKeyspace: %w", name, core.ErrInvalidConfiguration)
		}
	}
	var connection core.RedisConnectionConfig
	var err error
	if strings.TrimSpace(explicitURL) != "" {
		connection, err = core.ParseStandaloneRedisURL(explicitURL)
	} else {
		connection, err = core.ResolveRedisConnectionConfig(nil, os.LookupEnv)
	}
	if err != nil {
		return nil, core.RedisConnectionConfig{}, fmt.Errorf("resolve Redis connection: %w", err)
	}
	client, err := core.NewRedisUniversalClient(connection)
	if err != nil {
		return nil, core.RedisConnectionConfig{}, fmt.Errorf("initialize Redis client: %w", err)
	}
	return client, connection, nil
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
