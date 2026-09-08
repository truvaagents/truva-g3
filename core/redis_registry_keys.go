package core

type registryKeys struct {
	keyspace RedisKeyspace
}

func (keys registryKeys) service(id string) string {
	return keys.keyspace.Tagged("registry", "", "service", id)
}

func (keys registryKeys) all() string {
	return keys.keyspace.Tagged("registry", "", "index", "all")
}

func (keys registryKeys) capability(name string) string {
	return keys.keyspace.Tagged("registry", "", "index", "capability", name)
}

func (keys registryKeys) name(name string) string {
	return keys.keyspace.Tagged("registry", "", "index", "name", name)
}

func (keys registryKeys) serviceType(kind ComponentType) string {
	return keys.keyspace.Tagged("registry", "", "index", "type", string(kind))
}
