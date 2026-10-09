package ai

import (
	"fmt"
	"os"
	"strconv"

	"github.com/truvaagents/truva-g3/ai/providerkit/openaiwire"
	"github.com/truvaagents/truva-g3/core"
)

const reasoningMultiplierEnv = "TRUVAG3_AI_REASONING_TOKEN_MULTIPLIER"

// ResolveReasoningTokenMultiplier resolves code > environment > default. Zero
// in code is unset; an explicitly invalid environment value fails even when a
// code option would supersede it. Direct provider factories use this too.
func ResolveReasoningTokenMultiplier(value int) (int, error) {
	resolved := openaiwire.DefaultReasoningTokenMultiplier
	if raw, present := os.LookupEnv(reasoningMultiplierEnv); present {
		n, err := strconv.Atoi(raw)
		if err != nil || n <= 0 {
			return 0, fmt.Errorf("%w: %s must be a positive integer", core.ErrInvalidConfiguration, reasoningMultiplierEnv)
		}
		resolved = n
	}
	if value < 0 {
		return 0, fmt.Errorf("%w: reasoning token multiplier must be positive or zero (unset)", core.ErrInvalidConfiguration)
	}
	if value > 0 {
		resolved = value
	}
	return resolved, nil
}
