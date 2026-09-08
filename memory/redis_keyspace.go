package memory

import "github.com/truvaagents/truva-g3/core"

func defaultRedisKeyspace() core.RedisKeyspace {
	keyspace, err := core.NewRedisKeyspace("default")
	if err != nil {
		panic("memory: invalid built-in Redis keyspace: " + err.Error())
	}
	return keyspace
}
