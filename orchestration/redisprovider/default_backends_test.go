package redisprovider

import (
	"errors"
	"testing"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/truvaagents/truva-g3/core"
	"github.com/truvaagents/truva-g3/orchestration"
)

func TestDefaultBackendsConstructsOnlySelectedRolesAndOwnsClients(t *testing.T) {
	server := miniredis.RunT(t)
	lookup := lookupValues(map[string]string{
		"REDIS_URL":                         "redis://" + server.Addr(),
		"TRUVAG3_SKILLS_REDIS_DB":           "4",
		"TRUVAG3_HITL_REDIS_DB":             "invalid-unused-value",
		"TRUVAG3_WORKFLOW_STATE_TTL":        "invalid-unused-value",
		"TRUVAG3_TASK_QUEUE_RETRY_ATTEMPTS": "invalid-unused-value",
	})

	owned, err := newDefaultBackends(
		&core.NoOpLogger{},
		lookup,
		WithDefaultBackendRoles(ClientRoleSkills),
		WithDefaultBackendProviderOptions(WithNamespace("layer-one")),
	)
	if err != nil {
		t.Fatal(err)
	}
	if owned.Backends().SkillRegistry() == nil || owned.Backends().SkillAdministrationStore() == nil {
		t.Fatal("skills role did not construct the complete skills capability group")
	}
	if owned.Backends().Execution() != nil || owned.Backends().Workflow() != nil || owned.Backends().Schedules() != nil {
		t.Fatal("skills-only convenience composition constructed unrelated capability groups")
	}
	if len(owned.clients.clients) != 1 {
		t.Fatalf("owned Redis clients = %d, want one", len(owned.clients.clients))
	}
	client, ok := owned.clients.ClientSet().Resolve(ClientRoleSkills).(*redis.Client)
	if !ok || client.Options().DB != 4 {
		t.Fatalf("skills client = %#v, want Redis DB 4", client)
	}
	if err := owned.Close(); err != nil {
		t.Fatal(err)
	}
	if err := owned.Close(); err != nil {
		t.Fatalf("second close was not idempotent: %v", err)
	}
}

func TestDefaultBackendsPreservesCodePrecedenceAndCapabilityOverrides(t *testing.T) {
	server := miniredis.RunT(t)
	custom := orchestration.NewMemoryLLMDebugStore()
	owned, err := newDefaultBackends(
		&core.NoOpLogger{},
		lookupValues(map[string]string{
			"REDIS_URL":                  "redis://environment.invalid:6379",
			"TRUVAG3_LLM_DEBUG_REDIS_DB": "3",
		}),
		WithDefaultBackendRoles(ClientRoleLLMDebug),
		WithDefaultBackendClientConfig(
			WithClientURL("redis://"+server.Addr()),
			WithRoleDatabase(ClientRoleLLMDebug, 6),
		),
		WithDefaultBackendOverrides(orchestration.WithLLMDebugBackend(custom)),
	)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = owned.Close() })
	client, ok := owned.clients.ClientSet().Resolve(ClientRoleLLMDebug).(*redis.Client)
	if !ok || client.Options().DB != 6 {
		t.Fatalf("LLM-debug client = %#v, want code-owned Redis DB 6", client)
	}
	if owned.Backends().LLMDebug() != custom {
		t.Fatal("provider-neutral backend override did not win")
	}
}

func TestDefaultBackendsRejectsInvalidConfiguration(t *testing.T) {
	tests := []struct {
		name    string
		lookup  func(string) (string, bool)
		options []DefaultBackendsOption
	}{
		{name: "nil environment lookup", lookup: nil},
		{name: "nil option", lookup: lookupValues(nil), options: []DefaultBackendsOption{nil}},
		{name: "empty roles", lookup: lookupValues(nil), options: []DefaultBackendsOption{WithDefaultBackendRoles()}},
		{name: "duplicate roles", lookup: lookupValues(nil), options: []DefaultBackendsOption{WithDefaultBackendRoles(ClientRoleSkills, ClientRoleSkills)}},
		{name: "unknown role", lookup: lookupValues(nil), options: []DefaultBackendsOption{WithDefaultBackendRoles(ClientRole("unknown"))}},
		{name: "invalid environment", lookup: lookupValues(map[string]string{"TRUVAG3_SKILLS_REDIS_DB": "invalid"})},
		{name: "invalid client override", lookup: lookupValues(nil), options: []DefaultBackendsOption{WithDefaultBackendClientConfig(nil)}},
		{name: "invalid provider override", lookup: lookupValues(nil), options: []DefaultBackendsOption{WithDefaultBackendProviderOptions(nil)}},
		{name: "invalid backend override", lookup: lookupValues(nil), options: []DefaultBackendsOption{WithDefaultBackendOverrides(nil)}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			owned, err := newDefaultBackends(&core.NoOpLogger{}, test.lookup, test.options...)
			if err == nil {
				if owned != nil {
					_ = owned.Close()
				}
				t.Fatal("invalid convenience configuration was accepted")
			}
		})
	}
}

func TestOwnedBackendsNilSafety(t *testing.T) {
	var owned *OwnedBackends
	if owned.Backends() != nil {
		t.Fatal("nil ownership handle returned a composition")
	}
	if err := owned.Close(); err != nil {
		t.Fatalf("nil ownership handle close error = %v", err)
	}
	owned = &OwnedBackends{clients: &OwnedClients{clients: []redis.UniversalClient{closeErrorClient{}}}}
	if err := owned.Close(); err == nil || !errors.Is(err, errCloseClient) {
		t.Fatalf("Close error = %v, want %v", err, errCloseClient)
	}
}

func lookupValues(values map[string]string) func(string) (string, bool) {
	return func(name string) (string, bool) {
		value, ok := values[name]
		return value, ok
	}
}

var errCloseClient = errors.New("close client")

type closeErrorClient struct {
	redis.UniversalClient
}

func (closeErrorClient) Close() error { return errCloseClient }
