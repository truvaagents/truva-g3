package ai

import (
	"fmt"
	"os"
	"time"

	"github.com/truvaagents/truva-g3/core"
)

const requestTimeoutEnv = "TRUVAG3_AI_TIMEOUT"

// Resolve only overrides here: provider and managed-chain defaults are applied
// later. Invalid present environment values fail even when code takes precedence.
func resolveRequestTimeoutOverride(value time.Duration) (time.Duration, error) {
	var configured time.Duration
	if raw, present := os.LookupEnv(requestTimeoutEnv); present {
		parsed, err := time.ParseDuration(raw)
		if err != nil || parsed <= 0 {
			return 0, fmt.Errorf("%w: %s must be a positive Go duration", core.ErrInvalidConfiguration, requestTimeoutEnv)
		}
		configured = parsed
	}
	if value > 0 {
		return value, nil
	}
	return configured, nil
}
