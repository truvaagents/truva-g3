package core_test

import (
	"testing"

	"github.com/truvaagents/truva-g3/core"
	"github.com/truvaagents/truva-g3/core/conformance"
)

func TestLegacyAIOptionsCloneConformance(t *testing.T) {
	conformance.RunLegacyAIOptionsCloneConformance(t, func(options *core.AIOptions) (*core.AIOptions, error) {
		return core.NewAIRequestFromLegacy("prompt", "conformance", options).LegacyOptions(), nil
	})
}
