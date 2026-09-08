package orchestration

import (
	"os"

	"github.com/truvaagents/truva-g3/core"
)

func defaultRedisKeyspace() core.RedisKeyspace {
	keyspace, err := core.NewRedisKeyspace("default")
	if err != nil {
		panic("orchestration: invalid built-in Redis keyspace: " + err.Error())
	}
	return keyspace
}

func redisKeyspaceFromEnvironment() (core.RedisKeyspace, error) {
	return core.NewRedisKeyspace(os.Getenv("TRUVAG3_REDIS_NAMESPACE"))
}
