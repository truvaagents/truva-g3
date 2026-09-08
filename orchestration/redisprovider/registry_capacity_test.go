//go:build integration

package redisprovider

import (
	"context"
	"fmt"
	"os"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/truvaagents/truva-g3/core"
	"github.com/truvaagents/truva-g3/orchestration/internal/redistest"
)

const (
	registryCapacityServices = 2_000
	registryCapacityDuration = 2 * time.Minute
)

type capacityLatencies struct {
	mu     sync.Mutex
	values []time.Duration
}

func (latencies *capacityLatencies) add(duration time.Duration) {
	latencies.mu.Lock()
	latencies.values = append(latencies.values, duration)
	latencies.mu.Unlock()
}

func (latencies *capacityLatencies) percentile(percentile float64) time.Duration {
	latencies.mu.Lock()
	values := append([]time.Duration(nil), latencies.values...)
	latencies.mu.Unlock()
	if len(values) == 0 {
		return 0
	}
	sort.Slice(values, func(left, right int) bool { return values[left] < values[right] })
	index := int(float64(len(values)-1) * percentile)
	return values[index]
}

func runRegistryCapacity(t *testing.T, cluster redistest.Cluster) {
	t.Helper()
	if strings.ToLower(strings.TrimSpace(os.Getenv("TRUVAG3_RUN_REDIS_CAPACITY_TESTS"))) != "true" {
		t.Skip("registry capacity gate requires an explicit manual opt-in")
	}
	if testing.Short() {
		t.Skip("registry capacity gate")
	}

	connection := core.DefaultRedisConnectionConfig()
	connection.Mode = core.RedisModeCluster
	connection.Addrs = cluster.SeedAddrs()
	connection.DB = 0
	client, err := core.NewRedisUniversalClient(connection)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close() })
	keyspace, err := core.NewRedisKeyspace(clusterNamespace("registry-capacity"))
	if err != nil {
		t.Fatal(err)
	}
	registry, err := core.NewRedisRegistryWithClient(client, keyspace, 30*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	discovery, err := core.NewRedisDiscoveryWithClient(client, keyspace, 30*time.Second)
	if err != nil {
		t.Fatal(err)
	}

	services := make([]*core.ServiceInfo, registryCapacityServices)
	for index := range services {
		capabilities := make([]core.Capability, 5)
		for capabilityIndex := range capabilities {
			capabilities[capabilityIndex] = core.Capability{Name: fmt.Sprintf("capacity-%d-%d", index, capabilityIndex)}
		}
		services[index] = &core.ServiceInfo{
			ID: fmt.Sprintf("capacity-service-%04d", index), Name: fmt.Sprintf("capacity-%04d", index),
			Type: core.ComponentTypeAgent, Address: "http://127.0.0.1", Port: 8000 + index,
			Capabilities: capabilities, Health: core.HealthHealthy,
		}
	}
	hotPrimary := clusterPrimaryForKey(t, client, keyspace.Tagged("registry", "", "index", "all"))
	metricCtx, metricCancel := context.WithTimeout(t.Context(), 10*time.Second)
	beforeMetrics, err := cluster.PrimaryMetrics(metricCtx)
	metricCancel()
	if err != nil {
		t.Fatalf("capture capacity baseline metrics: %v", err)
	}
	capacityStarted := time.Now()

	var registrationLatency capacityLatencies
	registerServices(t, registry, services, &registrationLatency)

	ctx, cancel := context.WithTimeout(t.Context(), registryCapacityDuration)
	defer cancel()
	var domainErrors atomic.Uint64
	var heartbeatLatency, filteredLatency, unfilteredLatency capacityLatencies
	var wait sync.WaitGroup

	wait.Add(1)
	go func() {
		defer wait.Done()
		ticker := time.NewTicker(7500 * time.Microsecond) // 133.3 heartbeats/second.
		defer ticker.Stop()
		index := 0
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				started := time.Now()
				if err := registry.UpdateHealth(ctx, services[index%len(services)].ID, core.HealthHealthy); err != nil && ctx.Err() == nil {
					domainErrors.Add(1)
				}
				heartbeatLatency.add(time.Since(started))
				index++
			}
		}
	}()

	wait.Add(1)
	go func() {
		defer wait.Done()
		ticker := time.NewTicker(10 * time.Millisecond) // 100 filtered discoveries/second.
		defer ticker.Stop()
		index := 0
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				started := time.Now()
				services, err := discovery.Discover(ctx, core.DiscoveryFilter{
					Capabilities: []string{fmt.Sprintf("capacity-%d-0", index%registryCapacityServices)},
				})
				if err != nil && ctx.Err() == nil {
					domainErrors.Add(1)
				} else if ctx.Err() == nil && len(services) != 1 {
					domainErrors.Add(1)
				}
				filteredLatency.add(time.Since(started))
				index++
			}
		}
	}()

	wait.Add(1)
	go func() {
		defer wait.Done()
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				started := time.Now()
				services, err := discovery.Discover(ctx, core.DiscoveryFilter{})
				if err != nil && ctx.Err() == nil {
					domainErrors.Add(1)
				} else if ctx.Err() == nil && len(services) != registryCapacityServices {
					domainErrors.Add(1)
				}
				unfilteredLatency.add(time.Since(started))
			}
		}
	}()

	<-ctx.Done()
	wait.Wait()
	capacityElapsed := time.Since(capacityStarted)
	metricCtx, metricCancel = context.WithTimeout(t.Context(), 10*time.Second)
	afterMetrics, err := cluster.PrimaryMetrics(metricCtx)
	metricCancel()
	if err != nil {
		t.Fatalf("capture capacity completion metrics: %v", err)
	}

	metrics := map[string]time.Duration{
		"registration_p50":        registrationLatency.percentile(0.50),
		"registration_p95":        registrationLatency.percentile(0.95),
		"registration_p99":        registrationLatency.percentile(0.99),
		"heartbeat_p50":           heartbeatLatency.percentile(0.50),
		"heartbeat_p95":           heartbeatLatency.percentile(0.95),
		"heartbeat_p99":           heartbeatLatency.percentile(0.99),
		"filtered_discover_p50":   filteredLatency.percentile(0.50),
		"filtered_discover_p95":   filteredLatency.percentile(0.95),
		"filtered_discover_p99":   filteredLatency.percentile(0.99),
		"unfiltered_discover_p50": unfilteredLatency.percentile(0.50),
		"unfiltered_discover_p95": unfilteredLatency.percentile(0.95),
		"unfiltered_discover_p99": unfilteredLatency.percentile(0.99),
	}
	hotBefore, beforeFound := primaryMetricByAddress(beforeMetrics, hotPrimary)
	hotAfter, afterFound := primaryMetricByAddress(afterMetrics, hotPrimary)
	if !beforeFound || !afterFound {
		t.Fatalf("registry hot-slot primary %q missing from INFO snapshots", hotPrimary)
	}
	cpuPercent := (hotAfter.CPUSeconds - hotBefore.CPUSeconds) / capacityElapsed.Seconds() * 100
	commandRate := float64(hotAfter.TotalCommands-hotBefore.TotalCommands) / capacityElapsed.Seconds()
	t.Logf(
		"registry capacity metrics: services=%d duration=%s errors=%d latency=%v hot_primary=%s cpu_percent=%.2f command_rate=%.2f instantaneous_ops=%d connected_clients=%d",
		registryCapacityServices, capacityElapsed.Round(time.Millisecond), domainErrors.Load(), metrics,
		hotPrimary, cpuPercent, commandRate, hotAfter.InstantaneousOps, hotAfter.ConnectedClients,
	)
	if errors := domainErrors.Load(); errors != 0 {
		t.Fatalf("registry capacity domain errors = %d", errors)
	}
	if got := metrics["heartbeat_p99"]; got > 100*time.Millisecond {
		t.Fatalf("heartbeat p99 = %s, want <= 100ms", got)
	}
	if got := metrics["registration_p99"]; got > 250*time.Millisecond {
		t.Fatalf("registration p99 = %s, want <= 250ms", got)
	}
	if got := metrics["filtered_discover_p99"]; got > 250*time.Millisecond {
		t.Fatalf("filtered discovery p99 = %s, want <= 250ms", got)
	}
	if got := metrics["unfiltered_discover_p99"]; got > 2*time.Second {
		t.Fatalf("unfiltered discovery p99 = %s, want <= 2s", got)
	}
}

func primaryMetricByAddress(metrics []redistest.PrimaryMetrics, address string) (redistest.PrimaryMetrics, bool) {
	for _, metric := range metrics {
		if metric.Addr == address {
			return metric, true
		}
	}
	return redistest.PrimaryMetrics{}, false
}

func registerServices(
	t *testing.T,
	registry *core.RedisRegistry,
	services []*core.ServiceInfo,
	latencies *capacityLatencies,
) {
	t.Helper()
	const workers = 32
	jobs := make(chan *core.ServiceInfo)
	errors := make(chan error, workers)
	var wait sync.WaitGroup
	for worker := 0; worker < workers; worker++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			for service := range jobs {
				started := time.Now()
				if err := registry.Register(t.Context(), service); err != nil {
					select {
					case errors <- err:
					default:
					}
				}
				latencies.add(time.Since(started))
			}
		}()
	}
	for _, service := range services {
		jobs <- service
	}
	close(jobs)
	wait.Wait()
	close(errors)
	for err := range errors {
		if err != nil {
			t.Fatalf("initial registry population: %v", err)
		}
	}
}
