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

// travelSkillConfig supplies optional defaults; configuration resolution still
// lets deployment settings disable skills or replace the complete binding list.
func travelSkillConfig() orchestration.SkillConfig {
	return orchestration.SkillConfig{
		Enabled: true,
		Bindings: []orchestration.SkillBinding{
			{
				Namespace: "travel", Name: "action-verification", Version: "published",
				Activation: orchestration.SkillActivationAlways,
			},
			{
				Namespace: "travel", Name: "devops-delegation", Version: "published",
				Activation: orchestration.SkillActivationAlways,
			},
			{
				Namespace: "travel", Name: "travel-search-preparation", Version: "published",
				Activation: orchestration.SkillActivationAuto,
			},
			{
				Namespace: "travel", Name: "currency-conversion", Version: "published",
				Activation: orchestration.SkillActivationAuto,
			},
			{
				Namespace: "travel", Name: "travel-readiness-assessment", Version: "published",
				Activation: orchestration.SkillActivationAuto,
			},
			{
				Namespace: "travel", Name: "weather-assessment", Version: "published",
				Activation: orchestration.SkillActivationAuto,
			},
		},
	}
}
