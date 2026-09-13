package orchestration

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/truvaagents/truva-g3/core"
)

func TestResumeCoordinatorConfiguration(t *testing.T) {
	defaults := DefaultResumeCoordinatorRuntimeConfig()
	if defaults.ClaimLease != 30*time.Second || defaults.CleanupTimeout != 5*time.Second {
		t.Fatal("unexpected defaults")
	}
	for _, config := range []ResumeCoordinatorRuntimeConfig{
		{}, {ClaimLease: 2 * time.Second, CleanupTimeout: time.Second},
		{ClaimLease: 25 * time.Hour, CleanupTimeout: time.Second},
		{ClaimLease: 30 * time.Second}, {ClaimLease: 30 * time.Second, CleanupTimeout: 11 * time.Second},
		{ClaimLease: time.Hour, CleanupTimeout: 61 * time.Second},
	} {
		if config.Validate() == nil {
			t.Fatalf("invalid config accepted: %+v", config)
		}
	}
	for _, config := range []ResumeCoordinatorRuntimeConfig{defaults, {ClaimLease: 3 * time.Second, CleanupTimeout: time.Second}, {ClaimLease: 24 * time.Hour, CleanupTimeout: time.Minute}} {
		if err := config.Validate(); err != nil {
			t.Fatal(err)
		}
	}
	for _, test := range []struct {
		lease, cleanup string
		invalid        bool
	}{
		{"60s", "10s", false}, {"0", "1s", true}, {"3s", "2s", true}, {"nonsense", "1s", true}, {"30s", "", true},
	} {
		got, err := LoadResumeCoordinatorRuntimeConfigFromEnvironment(defaults, func(name string) (string, bool) {
			if name == "TRUVAG3_HITL_RESUME_CLAIM_LEASE" {
				return test.lease, true
			}
			if name == "TRUVAG3_HITL_RESUME_CLEANUP_TIMEOUT" {
				return test.cleanup, true
			}
			return "", false
		})
		if (err != nil) != test.invalid {
			t.Fatalf("load %v: %+v/%v", test, got, err)
		}
	}
	if _, err := LoadResumeCoordinatorRuntimeConfigFromEnvironment(defaults, nil); err == nil {
		t.Fatal("nil lookup accepted")
	}
	got, err := LoadResumeCoordinatorRuntimeConfigFromEnvironment(defaults, func(string) (string, bool) { return "", false })
	if err != nil || got != defaults {
		t.Fatalf("absent environment changed base: %+v/%v", got, err)
	}
}

func TestResumeCoordinatorRejectsInvalidConstructionAndCallInputs(t *testing.T) {
	store := &resumeStoreStub{}
	execute := ResumeExecutorFunc(func(context.Context, *ExecutionCheckpoint) (*ExecutionResult, error) {
		t.Fatal("invalid call executed")
		return nil, nil
	})
	config := DefaultResumeCoordinatorRuntimeConfig()
	for _, missing := range []CheckpointResumePersistence{nil, (*resumeStoreStub)(nil)} {
		if _, err := NewResumeCoordinator(missing, execute, config); err == nil {
			t.Fatal("nil store accepted")
		}
	}
	for _, missing := range []ResumeExecutor{nil, ResumeExecutorFunc(nil)} {
		if _, err := NewResumeCoordinator(store, missing, config); err == nil {
			t.Fatal("nil executor accepted")
		}
	}
	for _, option := range []ResumeCoordinatorOption{nil, WithResumeLogger(nil), WithResumeLogger((*core.NoOpLogger)(nil)), WithResumeOwner(""), WithResumeOwner(" padded"), WithResumeOwner(strings.Repeat("x", 129))} {
		if _, err := NewResumeCoordinator(store, execute, config, option); err == nil {
			t.Fatal("invalid explicit option accepted")
		}
	}
	t.Setenv("TRUVAG3_HITL_RESUME_CLAIM_LEASE", "invalid")
	r, err := NewResumeCoordinator(store, execute, config)
	if err != nil {
		t.Fatalf("constructor consulted environment: %v", err)
	}
	for _, id := range []string{"", " padded", strings.Repeat("x", 257)} {
		_, err := r.ResumeExecution(t.Context(), id)
		var invalid *ErrInvalidResumeRequest
		if !errors.As(err, &invalid) {
			t.Fatalf("invalid ID result = %v", err)
		}
	}
	for _, missing := range []ResumeExecutor{nil, ResumeExecutorFunc(nil)} {
		_, err := r.ResumeWithExecutor(t.Context(), "parent", missing)
		var invalid *ErrInvalidResumeRequest
		if !errors.As(err, &invalid) {
			t.Fatalf("invalid per-call executor result = %v", err)
		}
	}
	if len(store.operations()) != 0 {
		t.Fatal("invalid input touched persistence")
	}
	other, err := NewResumeCoordinator(store, execute, config)
	if err != nil || other.owner != r.owner {
		t.Fatal("owner was not process-scoped")
	}
}
