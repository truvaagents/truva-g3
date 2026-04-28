package main

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/truvaagents/truva-g3/core"
	"github.com/truvaagents/truva-g3/orchestration"
)

// HITLInfrastructure holds all HITL-related components.
type HITLInfrastructure struct {
	CheckpointStore *orchestration.RedisCheckpointStore
	CommandStore    *orchestration.RedisCommandStore
	Controller      *orchestration.DefaultInterruptController
	Policy          *orchestration.RuleBasedPolicy
}

// SetupHITL initializes HITL infrastructure.
// NOTE: The orchestrator's config.HITL is loaded from environment variables
// by orchestration.DefaultConfig(). This function creates the runtime components.
func SetupHITL(logger core.Logger, hitlConfig orchestration.HITLConfig) (*HITLInfrastructure, error) {
	redisURL := os.Getenv("REDIS_URL")
	if redisURL == "" {
		return nil, fmt.Errorf("REDIS_URL environment variable is required")
	}

	// 1. Create checkpoint store (Redis DB 6)
	checkpointStore, err := orchestration.NewRedisCheckpointStore(
		orchestration.WithCheckpointRedisURL(redisURL),
		orchestration.WithCheckpointStoreLogger(logger),
	)
	if err != nil {
		return nil, fmt.Errorf("checkpoint store: %w", err)
	}

	// 1a. Set expiry callback for handling expired checkpoints
	// For Scenario 1 (streaming + implicit_deny): just log, UI detects via polling
	// For Scenarios 2a/3 (apply_default + approve): would auto-resume (deferred)
	if err := checkpointStore.SetExpiryCallback(func(ctx context.Context, cp *orchestration.ExecutionCheckpoint, action orchestration.CommandType) {
		if action == "" {
			// Scenario 1: implicit_deny - user saw dialog but didn't respond
			// No action needed here - UI will detect "expired" status via polling
			logger.InfoWithContext(ctx, "Checkpoint expired (implicit deny)", map[string]interface{}{
				"operation":       "hitl_expiry_callback",
				"checkpoint_id":   cp.CheckpointID,
				"request_id":      cp.RequestID,
				"request_mode":    string(cp.RequestMode),
				"interrupt_point": string(cp.InterruptPoint),
			})
			return
		}

		// Scenarios 2a/2b/3: apply_default was configured
		// For now, just log - auto-resume implementation is deferred
		logger.InfoWithContext(ctx, "Checkpoint expired with action", map[string]interface{}{
			"operation":     "hitl_expiry_callback",
			"checkpoint_id": cp.CheckpointID,
			"request_id":    cp.RequestID,
			"request_mode":  string(cp.RequestMode),
			"action":        string(action),
			"status":        string(cp.Status),
		})
		// TODO (Phase 8.2): Implement auto-resume for action="approve"
		// TODO (Phase 8.2): Implement result storage for action="reject"
	}); err != nil {
		return nil, fmt.Errorf("set expiry callback: %w", err)
	}

	// 1b. Start expiry processor to handle checkpoint timeouts
	// Uses background context since this runs for the lifetime of the application
	checkpointStore.StartExpiryProcessor(context.Background(), orchestration.ExpiryProcessorConfig{
		Enabled:      true,
		ScanInterval: 10 * time.Second,
		BatchSize:    100,
	})

	// 2. Create command store (Redis Pub/Sub)
	commandStore, err := orchestration.NewRedisCommandStore(
		orchestration.WithCommandStoreRedisURL(redisURL),
		orchestration.WithCommandStoreLogger(logger),
	)
	if err != nil {
		return nil, fmt.Errorf("command store: %w", err)
	}

	// 3. Create webhook handler for checkpoint notifications
	// Use TRUVAG3_HITL_WEBHOOK_URL for K8s, or construct from PORT for local dev
	webhookURL := os.Getenv("TRUVAG3_HITL_WEBHOOK_URL")
	if webhookURL == "" {
		port := os.Getenv("PORT")
		if port == "" {
			return nil, fmt.Errorf("TRUVAG3_HITL_WEBHOOK_URL or PORT environment variable is required")
		}
		webhookURL = fmt.Sprintf("http://localhost:%s/internal/hitl-webhook", port)
	}
	webhookHandler := orchestration.NewWebhookInterruptHandler(
		webhookURL,
		commandStore,
		orchestration.WithHandlerLogger(logger),
	)

	// 4. Create policy from HITL config (rule-based decisions)
	// This uses the config loaded from environment variables
	policy := orchestration.NewRuleBasedPolicy(
		hitlConfig,
		orchestration.WithPolicyLogger(logger),
	)

	// 5. Create controller (note: policy is required as first argument)
	controller := orchestration.NewInterruptController(
		policy,
		checkpointStore,
		webhookHandler,
		orchestration.WithControllerLogger(logger),
		orchestration.WithControllerCommandStore(commandStore),
	)

	return &HITLInfrastructure{
		CheckpointStore: checkpointStore,
		CommandStore:    commandStore,
		Controller:      controller,
		Policy:          policy,
	}, nil
}

// Close closes all HITL infrastructure connections.
func (h *HITLInfrastructure) Close() error {
	if h.CheckpointStore != nil {
		// Stop expiry processor first (graceful shutdown with 5s timeout)
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = h.CheckpointStore.StopExpiryProcessor(ctx)
		h.CheckpointStore.Close()
	}
	if h.CommandStore != nil {
		h.CommandStore.Close()
	}
	return nil
}
