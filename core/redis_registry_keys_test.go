package core

import "testing"

func TestRegistryAtomicKeysShareDeploymentSlot(t *testing.T) {
	keyspace, err := NewRedisKeyspace("prod")
	if err != nil {
		t.Fatal(err)
	}
	keys := registryKeys{keyspace: keyspace}
	requireCoreSameRedisHashTag(t,
		keys.service("service-1"), keys.all(), keys.capability("weather"),
		keys.name("forecast"), keys.serviceType(ComponentTypeTool),
	)
}

func requireCoreSameRedisHashTag(t *testing.T, keys ...string) {
	t.Helper()
	want := testRedisHashTag(keys[0])
	if want == "" {
		t.Fatalf("key %q has no Redis hash tag", keys[0])
	}
	for _, key := range keys[1:] {
		if got := testRedisHashTag(key); got != want {
			t.Fatalf("key %q has hash tag %q, want %q", key, got, want)
		}
	}
}
