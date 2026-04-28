package core

import (
	"context"
	"net"
	"testing"
	"time"
)

// requireRedis checks if Redis is available and skips the test if not.
// This provides consistent Redis availability checking across tests.
func requireRedis(t *testing.T) {
	t.Helper()

	if testing.Short() {
		t.Skip("Skipping Redis test in short mode")
	}

	if !isRedisReachable() {
		t.Skip("Redis not available at localhost:6379 (connection refused)")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	discovery, err := NewRedisDiscovery("redis://localhost:6379")
	if err != nil {
		t.Skipf("Redis not available: %v", err)
	}

	if discovery != nil {
		testInfo := &ServiceInfo{
			ID:   "redis-test-" + time.Now().Format("20060102-150405"),
			Name: "redis-availability-test",
			Type: ComponentTypeTool,
		}

		err = discovery.Register(ctx, testInfo)
		if err != nil {
			t.Skipf("Redis not responsive: %v", err)
		}

		_ = discovery.Unregister(ctx, testInfo.ID)
	}
}

// isRedisReachable performs a quick TCP connection check.
func isRedisReachable() bool {
	conn, err := net.DialTimeout("tcp", "localhost:6379", 1*time.Second)
	if err != nil {
		return false
	}
	_ = conn.Close()
	return true
}
