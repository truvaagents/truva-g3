package orchestration

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
)

const (
	// ResumeStateVersion identifies the closed typed state contract below.
	ResumeStateVersion = 1
	// MaxResumeStateBytes bounds the JSON representation accepted by capture
	// and restore, including operational state and body-free diagnostics.
	MaxResumeStateBytes = 256 * 1024
)

var (
	ErrInvalidResumeState     = errors.New("invalid execution resume state")
	ErrResumeStateUnavailable = errors.New("execution resume state unavailable")
)

// ResumeState preserves feature-owned, body-free request state across an
// application-managed durable boundary. It contains no context, callbacks,
// credentials, clients, instruction bodies or resource bodies. Plan, completed
// steps and phase state remain composed through WithPlanOverride and
// WithCompletedSteps; applications persist their own ownership and deadlines.
//
// A nil SkillState records a genuinely skill-free initialized request. Restore
// preserves that fact even if the resuming deployment has new skill bindings.
// This value is trusted application persistence data, never user metadata or
// authorization. It does not confer HITL approval or execution ownership.
type ResumeState struct {
	Version           int                  `json:"version"`
	SkillState        *SkillExecutionState `json:"skill_state,omitempty"`
	SkillCacheContext *SkillCacheContext   `json:"skill_cache_context,omitempty"`
}

type resumeStateCaptureKey struct{}
type resumeStateCapture struct {
	skills bool
	holder *skillExecutionStateHolder
}

// Bind capture to this initialized request's holder, rather than inheriting a
// different orchestrator's holder or mistaking pre-initialization for skill-free.
func withResumeStateCapture(ctx context.Context, skills bool) context.Context {
	capture := &resumeStateCapture{skills: skills}
	if skills {
		capture.holder, _ = skillExecutionHolderFromContext(ctx)
	}
	return context.WithValue(ctx, resumeStateCaptureKey{}, capture)
}

// CaptureResumeState snapshots the actual initialized execution context passed
// to a pipeline hook. The incoming host context, before ProcessRequest creates
// feature state, cannot be captured. A step callback without a context parameter
// should retain a snapshot captured at its preceding final plan boundary.
// The returned state is isolated from later request updates and caller mutation.
func CaptureResumeState(ctx context.Context) (ResumeState, error) {
	if ctx == nil {
		return ResumeState{}, ErrResumeStateUnavailable
	}
	capture, _ := ctx.Value(resumeStateCaptureKey{}).(*resumeStateCapture)
	if capture == nil || capture.skills && capture.holder == nil {
		return ResumeState{}, ErrResumeStateUnavailable
	}
	state := ResumeState{Version: ResumeStateVersion}
	if capture.skills {
		skills, cache := capture.holder.Snapshot()
		state.SkillState = &skills
		state.SkillCacheContext = &cache
	}
	return cloneValidatedResumeState(state)
}

// WithResumeState prepares a fresh host context for ordinary continuation.
// It installs no resume-mode flag, approval status, claim or HITL lineage.
// ProcessRequest still validates current runtime limits, binding revocation and
// exact immutable skill revisions through the existing ResumeCandidates path.
// Applications must separately validate their durable owner/claim/deadline and
// reconcile admitted incomplete effects before restoring plan/completed steps.
func WithResumeState(ctx context.Context, state ResumeState) (context.Context, error) {
	if ctx == nil {
		return nil, ErrInvalidResumeState
	}
	if capture, _ := ctx.Value(resumeStateCaptureKey{}).(*resumeStateCapture); capture != nil {
		return nil, ErrInvalidResumeState
	}
	cloned, err := cloneValidatedResumeState(state)
	if err != nil {
		return nil, err
	}
	// Explicitly shadow inherited feature state in both directions. In
	// particular a skill-free checkpoint marker must not disable restored pins.
	ctx = context.WithValue(ctx, skillExecutionHolderContextKey{}, (*skillExecutionStateHolder)(nil))
	ctx = withSkillExecutionState(ctx, SkillExecutionState{})
	if cloned.SkillState == nil {
		ctx = withCheckpointSkillState(ctx, SkillExecutionState{}, nil)
		return withSkillFreeCheckpointResume(ctx), nil
	}
	ctx = context.WithValue(ctx, skillFreeCheckpointResumeContextKey{}, false)
	return withCheckpointSkillState(ctx, *cloned.SkillState, cloned.SkillCacheContext), nil
}

func cloneValidatedResumeState(state ResumeState) (ResumeState, error) {
	if state.Version != ResumeStateVersion {
		return ResumeState{}, ErrInvalidResumeState
	}
	raw, err := json.Marshal(state)
	if err != nil {
		return ResumeState{}, ErrInvalidResumeState
	}
	if len(raw) > MaxResumeStateBytes {
		return ResumeState{}, fmt.Errorf("%w: %w", ErrInvalidResumeState, ErrSkillLimitExceeded)
	}
	var cloned ResumeState
	if json.Unmarshal(raw, &cloned) != nil {
		return ResumeState{}, ErrInvalidResumeState
	}
	if cloned.SkillState == nil {
		if cloned.SkillCacheContext != nil {
			return ResumeState{}, ErrInvalidResumeState
		}
		return cloned, nil
	}
	skills, cache := cloned.SkillState, cloned.SkillCacheContext
	if skills.Pinned == nil || cache == nil || !validSkillSHA256(cache.Fingerprint) || cache.Fingerprint != skills.Pinned.CacheFingerprint {
		return ResumeState{}, fmt.Errorf("%w: %w", ErrInvalidResumeState, ErrSkillIntegrity)
	}
	// The byte envelope bounds all arrays/text. Reuse existing structural
	// identity/hash/version validation without applying a different deployment's
	// limits here; the actual runtime revalidates its own limits on continuation.
	validator := skillRuntime{config: SkillConfig{Limits: SkillRuntimeLimits{MaxBindings: MaxResumeStateBytes, MaxActiveSkills: MaxResumeStateBytes}}}
	if validator.validateResumedSkillState(*skills) != nil {
		return ResumeState{}, fmt.Errorf("%w: %w", ErrInvalidResumeState, ErrSkillIntegrity)
	}
	return cloned, nil
}
