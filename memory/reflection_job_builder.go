package memory

import (
	"os"
	"strconv"

	"github.com/truvaagents/truva-g3/core"
)

// BuildReflectionJob creates a fully-configured ReflectionJob from SharedMemoryDeps.
//
// Layer 1 convenience function — auto-wires reflector, distributed lock, and all
// dependencies from the deps struct. Returns nil (not an error) when Phase 2
// backends are unavailable — reflection requires Knowledge + Embedder.
//
// Configuration precedence (per FRAMEWORK_DESIGN_PRINCIPLES §3):
//  1. Explicit ReflectionJobOption arguments (highest)
//  2. TRUVAG3_REFLECTION_* env vars (read by NewReflectionJob)
//  3. Sensible defaults (24h interval, 7-day threshold, 5 min events)
//
// Same composable pattern as BuildMemoryHooks — pass deps + ai client + logger,
// optionally pass behavioural options for customisation.
//
// Lifecycle is managed by the framework via the core.Runnable interface —
// register the returned job with Framework.RegisterRunnable and the framework
// starts it on Run(ctx) and stops it when ctx is cancelled.
//
// Layer 1 (most developers):
//
//	if job, _ := memory.BuildReflectionJob(backends.ToDeps(), agent.AI, agent.Logger); job != nil {
//	    framework.RegisterRunnable(job)
//	}
//
// Layer 2 (custom options):
//
//	if job, _ := memory.BuildReflectionJob(backends.ToDeps(), agent.AI, agent.Logger,
//	    memory.WithReflectionTelemetry(telemetry.GetTelemetryProvider()),
//	); job != nil {
//	    framework.RegisterRunnable(job)
//	}
//
// Layer 2b (custom backend — etcd lock instead of Redis):
//
//	deps := backends.ToDeps()
//	deps.Lock = myEtcdLock
//	if job, _ := memory.BuildReflectionJob(deps, agent.AI, agent.Logger); job != nil {
//	    framework.RegisterRunnable(job)
//	}
//
// Layer 3 (full manual control): use NewReflectionJob directly.
func BuildReflectionJob(
	deps *core.SharedMemoryDeps,
	aiClient core.AIClient,
	logger core.Logger,
	opts ...ReflectionJobOption,
) (*ReflectionJob, error) {
	// Phase 2 required — return nil (not error) when backends are unavailable.
	// Same fail-open pattern as BuildMemoryHooks returning empty hooks list.
	if deps == nil || deps.Episodic == nil || deps.Knowledge == nil || deps.Embedder == nil {
		return nil, nil
	}
	if aiClient == nil {
		return nil, nil
	}

	// Create reflector internally — same pattern as BuildMemoryHooks creating compactor.
	// The framework composes; the developer doesn't see the reflector unless they need
	// Layer 3 control.
	//
	// Propagate TRUVAG3_REFLECTION_MIN_EVENTS to the reflector so the job and reflector
	// agree on the threshold. Without this, the job uses the env var to discover entities
	// but the reflector applies its own default (5) and silently skips entities below it.
	//
	// Propagate TRUVAG3_REFLECTION_MODEL so operators can route reflection LLM calls to a
	// cheaper/faster model (e.g. "fast" alias → Haiku/Llama-8B) without affecting other
	// agent LLM calls. Empty/unset = use the AIClient's default model selection.
	var reflectorOpts []ReflectorOption
	if v := os.Getenv("TRUVAG3_REFLECTION_MIN_EVENTS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			reflectorOpts = append(reflectorOpts, WithReflectorMinEvents(n))
		}
	}
	if v := os.Getenv("TRUVAG3_REFLECTION_MODEL"); v != "" {
		reflectorOpts = append(reflectorOpts, WithReflectorModel(v))
	}
	reflector, err := NewLLMMemoryReflector(aiClient, deps.Episodic, deps.AgentDomain, logger, reflectorOpts...)
	if err != nil {
		return nil, err
	}

	// Auto-wire lock from deps if available — prepended so explicit opts override.
	// Layer 2 developers can replace the lock without losing other auto-wiring.
	if deps.Lock != nil {
		opts = append([]ReflectionJobOption{WithReflectionLock(deps.Lock)}, opts...)
	}

	return NewReflectionJob(
		reflector,
		deps.Episodic,
		deps.Knowledge,
		deps.Embedder,
		deps.AgentDomain,
		logger,
		opts...,
	)
}
