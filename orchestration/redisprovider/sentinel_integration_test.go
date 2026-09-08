//go:build integration

package redisprovider

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/truvaagents/truva-g3/core"
	"github.com/truvaagents/truva-g3/orchestration"
	"github.com/truvaagents/truva-g3/orchestration/internal/redistest"
)

func TestRedisProviderAgainstRealSentinel(t *testing.T) {
	if testing.Short() {
		t.Skip("real Sentinel tests are excluded in short mode")
	}
	fixture := redistest.StartSentinel(t)
	connection := core.DefaultRedisConnectionConfig()
	connection.Mode = core.RedisModeSentinel
	connection.Addrs = fixture.Addrs()
	connection.MasterName = fixture.MasterName()
	connection.DB = 0

	config, err := ConfigureClientConfig(DefaultClientConfig(), WithConnectionConfig(connection))
	if err != nil {
		t.Fatal(err)
	}
	clients, err := NewOwnedClients(config)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = clients.Close() })
	options, err := NewOptions(WithDeployment(clusterNamespace("sentinel")), WithAgentScope("sentinel-agent"))
	if err != nil {
		t.Fatal(err)
	}
	backends, err := NewOrchestrationBackends(clients.ClientSet(), options)
	if err != nil {
		t.Fatal(err)
	}
	requireAllProviderCapabilities(t, backends)

	record := &orchestration.StoredExecution{
		RequestID: "sentinel-before-failover", TraceID: "sentinel-trace",
		OriginalRequest: "verify Sentinel topology refresh", CreatedAt: time.Now(),
		Result: &orchestration.ExecutionResult{Success: true},
	}
	if err := backends.Execution().Store(t.Context(), record); err != nil {
		t.Fatal(err)
	}
	client := clients.ClientSet().Resolve(ClientRoleExecution)
	if err := client.Do(t.Context(), "WAIT", 1, 5000).Err(); err != nil {
		t.Fatalf("wait for Sentinel replica: %v", err)
	}

	transitionCtx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	if err := fixture.TriggerFailover(transitionCtx); err != nil {
		t.Fatal(err)
	}
	var lastErr error
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		loaded, loadErr := backends.Execution().Get(t.Context(), record.RequestID)
		if loadErr == nil && loaded != nil && loaded.RequestID == record.RequestID {
			return
		}
		lastErr = loadErr
		time.Sleep(100 * time.Millisecond)
	}
	if lastErr != nil && strings.Contains(strings.ToLower(lastErr.Error()), "password") {
		t.Fatalf("Sentinel failure exposed a credential-bearing error: %v", lastErr)
	}
	t.Fatalf("provider did not recover after Sentinel failover: %v", lastErr)
}
