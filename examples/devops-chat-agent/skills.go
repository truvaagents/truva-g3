package main

import (
	"fmt"
	"io"

	"github.com/truvaagents/truva-g3/core"
	"github.com/truvaagents/truva-g3/orchestration"
	"github.com/truvaagents/truva-g3/orchestration/redisprovider"
)

// newSkillRegistry keeps the agent-facing configuration provider-neutral: the
// process composition root selects Redis, while InitializeOrchestrator receives
// only the SkillRegistry contract.
func newSkillRegistry(logger core.Logger) (orchestration.SkillRegistry, io.Closer, error) {
	owned, err := redisprovider.NewDefaultBackends(
		logger,
		redisprovider.WithDefaultBackendRoles(redisprovider.ClientRoleSkills),
	)
	if err != nil {
		return nil, nil, fmt.Errorf("create default skill backends: %w", err)
	}
	return owned.Backends().SkillRegistry(), owned, nil
}
