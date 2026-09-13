package orchestration

import (
	"errors"
	"fmt"
	"time"
)

// ResumeCoordinatorRuntimeConfig controls attempt ownership, not human approval.
type ResumeCoordinatorRuntimeConfig struct {
	ClaimLease     time.Duration
	CleanupTimeout time.Duration
}

func DefaultResumeCoordinatorRuntimeConfig() ResumeCoordinatorRuntimeConfig {
	return ResumeCoordinatorRuntimeConfig{ClaimLease: 30 * time.Second, CleanupTimeout: 5 * time.Second}
}

func (c ResumeCoordinatorRuntimeConfig) Validate() error {
	if err := validateResumeLease(c.ClaimLease); err != nil {
		return err
	}
	if c.CleanupTimeout <= 0 || c.CleanupTimeout > time.Minute || c.CleanupTimeout > c.ClaimLease/3 {
		return errors.New("orchestration: resume cleanup timeout must be positive, at most 1m, and at most one third of the claim lease")
	}
	return nil
}

// LoadResumeCoordinatorRuntimeConfigFromEnvironment is explicit and lookup-driven;
// constructing a coordinator never reads environment variables.
func LoadResumeCoordinatorRuntimeConfigFromEnvironment(base ResumeCoordinatorRuntimeConfig, lookup func(string) (string, bool)) (ResumeCoordinatorRuntimeConfig, error) {
	if lookup == nil {
		return base, errors.New("orchestration: resume environment lookup cannot be nil")
	}
	for _, setting := range []struct {
		name        string
		destination *time.Duration
	}{
		{"TRUVAG3_HITL_RESUME_CLAIM_LEASE", &base.ClaimLease},
		{"TRUVAG3_HITL_RESUME_CLEANUP_TIMEOUT", &base.CleanupTimeout},
	} {
		if value, found := lookup(setting.name); found {
			duration, err := time.ParseDuration(value)
			if err != nil {
				return base, fmt.Errorf("orchestration: %s must be a duration", setting.name)
			}
			*setting.destination = duration
		}
	}
	return base, base.Validate()
}
