//go:build integration

package redisprovider

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
	"github.com/truvaagents/truva-g3/core"
	"github.com/truvaagents/truva-g3/orchestration"
)

const envRunManagedRedisSmokeTests = "TRUVAG3_RUN_MANAGED_REDIS_SMOKE_TESTS"

// TestRedisProviderAgainstManagedCluster is an environment-gated smoke harness
// for a selected managed Redis or Valkey provider. Maintainers opt in manually
// with the integration build tag, provider endpoints, and credentials; neither
// connection data nor underlying startup errors are logged.
func TestRedisProviderAgainstManagedCluster(t *testing.T) {
	if testing.Short() {
		t.Skip("managed Redis/Valkey tests are excluded in short mode")
	}
	if strings.ToLower(strings.TrimSpace(os.Getenv(envRunManagedRedisSmokeTests))) != "true" {
		t.Skipf("managed cluster smoke test; set %s=true", envRunManagedRedisSmokeTests)
	}
	resolution, err := core.ResolveRedisConnectionConfig(nil, os.LookupEnv)
	if err != nil {
		t.Fatal(err)
	}
	if resolution.Config.Mode != core.RedisModeCluster || resolution.Config.DB != 0 {
		t.Fatal("managed smoke test requires cluster mode and DB 0")
	}
	deployment := strings.TrimSpace(os.Getenv("TRUVAG3_REDIS_NAMESPACE"))
	if deployment == "" || deployment == "default" {
		t.Fatal("managed smoke test requires a non-default TRUVAG3_REDIS_NAMESPACE")
	}
	keyspace, err := core.NewRedisKeyspace(deployment)
	if err != nil {
		t.Fatal(err)
	}
	config, err := ConfigureClientConfig(DefaultClientConfig(), WithConnectionConfig(resolution.Config))
	if err != nil {
		t.Fatal(err)
	}
	clients, err := NewOwnedClients(
		config,
		WithOwnedClientRoles(ClientRoleExecution, ClientRoleLLMDebug, ClientRoleHITL, ClientRoleWorkflow),
	)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := clients.Close(); err != nil {
			t.Errorf("close managed Redis clients: %v", err)
		}
	})
	clusterClient, ok := clients.ClientSet().Resolve(ClientRoleWorkflow).(*redis.ClusterClient)
	if !ok {
		t.Fatal("managed cluster configuration did not create a cluster-aware client")
	}
	if shards, err := clusterClient.ClusterShards(t.Context()).Result(); err != nil || len(shards) == 0 {
		t.Fatalf("managed cluster shard discovery failed: assignments=%d error=%v", len(shards), err)
	}

	const smokeTTL = 2 * time.Minute
	executionConfig := orchestration.DefaultExecutionStoreConfig()
	executionConfig.TTL = smokeTTL
	executionConfig.ErrorTTL = smokeTTL
	identifier := fmt.Sprintf("managed-smoke-%d", time.Now().UnixNano())
	options, err := NewOptions(
		WithDeployment(deployment),
		WithAgentScope(identifier),
		WithExecutionStoreConfig(executionConfig),
		WithLLMDebugRetention(smokeTTL, smokeTTL),
		WithCheckpointTTL(smokeTTL),
		WithWorkflowStateTTL(smokeTTL),
	)
	if err != nil {
		t.Fatal(err)
	}
	backends, err := NewOrchestrationBackends(clients.ClientSet(), options)
	if err != nil {
		t.Fatal(err)
	}
	if backends.Execution() == nil || backends.LLMDebug() == nil || backends.Checkpoints() == nil ||
		backends.CheckpointExpiry() == nil || backends.Workflow() == nil {
		t.Fatal("managed provider composition omitted a requested capability")
	}

	execution := &orchestration.StoredExecution{
		RequestID: identifier, TraceID: "trace-" + identifier, OriginalRequest: "managed smoke",
		CreatedAt: time.Now(), Result: &orchestration.ExecutionResult{Success: true},
	}
	if err := backends.Execution().Store(t.Context(), execution); err != nil {
		t.Fatal(err)
	}
	if loaded, err := backends.Execution().Get(t.Context(), identifier); err != nil || loaded.RequestID != identifier {
		t.Fatalf("managed execution round trip = %#v, %v", loaded, err)
	}
	if err := backends.LLMDebug().RecordInteraction(t.Context(), identifier, orchestration.LLMInteraction{
		Type: "managed-smoke", Timestamp: time.Now(), Prompt: "ping", Response: "pong", Success: true,
	}); err != nil {
		t.Fatal(err)
	}
	workflow := &orchestration.WorkflowExecution{
		ID: identifier, WorkflowID: identifier, Status: orchestration.ExecutionPending,
		Steps: make(map[string]*orchestration.StepExecution),
	}
	if err := backends.Workflow().SaveExecution(t.Context(), workflow); err != nil {
		t.Fatal(err)
	}
	if loaded, err := backends.Workflow().GetExecution(t.Context(), identifier, identifier); err != nil || loaded.ID != identifier {
		t.Fatalf("managed workflow round trip = %#v, %v", loaded, err)
	}
	checkpoint := &orchestration.ExecutionCheckpoint{
		CheckpointID: identifier, RequestID: identifier, Status: orchestration.CheckpointStatusPending,
		CreatedAt: time.Now().Add(-time.Minute), ExpiresAt: time.Now().Add(-time.Second),
		RequestMode: orchestration.RequestModeNonStreaming,
	}
	if err := backends.Checkpoints().SaveCheckpoint(t.Context(), checkpoint); err != nil {
		t.Fatal(err)
	}
	claimCtx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	claimed, err := backends.CheckpointExpiry().ClaimExpiredCheckpoints(claimCtx, orchestration.ExpiredCheckpointClaimRequest{
		Before: time.Now(), Limit: 1, Owner: identifier, Lease: time.Minute,
	})
	if err != nil || len(claimed) != 1 || claimed[0].CheckpointID != identifier {
		t.Fatalf("managed checkpoint claim = %#v, %v", claimed, err)
	}

	// Keep the namespace reference live so future schema additions cannot turn
	// the deployment validation above into dead setup.
	if keyspace.Deployment() != deployment {
		t.Fatal("managed smoke namespace changed during composition")
	}
}
