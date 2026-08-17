package main

import (
	"fmt"
	"io"
	"os"

	"github.com/truvaagents/truva-g3/core"
	"github.com/truvaagents/truva-g3/orchestration"
	"github.com/truvaagents/truva-g3/orchestration/redisprovider"
)

// newSkillRegistry keeps the agent-facing configuration provider-neutral: the
// process composition root selects Redis, while InitializeOrchestrator receives
// only the SkillRegistry contract.
func newSkillRegistry(logger core.Logger) (orchestration.SkillRegistry, io.Closer, error) {
	clientConfig, err := redisprovider.LoadClientConfigFromEnvironment(
		redisprovider.DefaultClientConfig(), os.LookupEnv,
	)
	if err != nil {
		return nil, nil, fmt.Errorf("resolve skill backend configuration: %w", err)
	}
	clients, err := redisprovider.NewOwnedClients(clientConfig)
	if err != nil {
		return nil, nil, fmt.Errorf("create skill backend clients: %w", err)
	}
	store, err := redisprovider.NewSkillStore(
		clients.ClientSet().Resolve(redisprovider.ClientRoleSkills),
		redisprovider.WithSkillStoreLogger(logger),
	)
	if err != nil {
		_ = clients.Close()
		return nil, nil, fmt.Errorf("create skill registry: %w", err)
	}
	return store, clients, nil
}
