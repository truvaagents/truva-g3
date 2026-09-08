// Package redistest provides opt-in real Redis and Valkey topology fixtures.
// The cluster fixture follows the upstream requirement to use host networking
// rather than NAT/port remapping and therefore requires a Linux test host.
package redistest

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

const (
	EnvRunClusterTests = "TRUVAG3_RUN_REDIS_CLUSTER_TESTS"
	EnvDistribution    = "TRUVAG3_TEST_REDIS_DISTRIBUTION"
	EnvClusterBasePort = "TRUVAG3_TEST_REDIS_CLUSTER_BASE_PORT"
)

// Distribution identifies a server distribution covered by the portable
// cluster baseline.
type Distribution string

const (
	RedisOSS Distribution = "redis-oss"
	Valkey8  Distribution = "valkey-8"
	Valkey9  Distribution = "valkey-9"
)

// Valid reports whether the distribution belongs to the required cluster
// matrix.
func (distribution Distribution) Valid() bool {
	switch distribution {
	case RedisOSS, Valkey8, Valkey9:
		return true
	default:
		return false
	}
}

// DistributionFromEnvironment returns the single distribution selected by a
// matrix job. Keeping one distribution per process makes fixed host-network
// ports safe and keeps failures attributable to one server implementation.
func DistributionFromEnvironment() (Distribution, error) {
	distribution := Distribution(strings.TrimSpace(os.Getenv(EnvDistribution)))
	if !distribution.Valid() {
		return "", fmt.Errorf("%s must be one of %q, %q, or %q", EnvDistribution, RedisOSS, Valkey8, Valkey9)
	}
	return distribution, nil
}

// Cluster is an owned multi-primary test topology. SeedAddrs returns a stable
// snapshot suitable for constructing a cluster-aware client. Close must be
// idempotent.
type Cluster interface {
	SeedAddrs() []string
	BeginSlotMigration(context.Context, int64) (SlotMigration, error)
	DisableShardForSlot(context.Context, int64) error
	PrimaryMetrics(context.Context) ([]PrimaryMetrics, error)
	TriggerPrimaryFailover(context.Context) error
	Close(context.Context) error
}

// SlotMigration is a deliberately paused slot migration. BeginSlotMigration
// leaves the selected slot in MIGRATING/IMPORTING state so tests can prove the
// client handles ASK replies before Complete assigns the slot to its new owner.
type SlotMigration interface {
	Complete(context.Context) error
}

// PrimaryMetrics is a bounded snapshot of the server metrics used by the
// capacity acceptance test. It intentionally excludes configuration and
// command arguments so test logs cannot disclose provider credentials.
type PrimaryMetrics struct {
	Addr             string
	CPUSeconds       float64
	TotalCommands    uint64
	InstantaneousOps int64
	ConnectedClients int64
}

// ClusterStarter creates an isolated cluster fixture and waits until its
// cluster_state is ok before returning it.
type ClusterStarter interface {
	Start(context.Context, Distribution) (Cluster, error)
}

type dockerDistribution struct {
	image  string
	server string
	cli    string
}

var dockerDistributions = map[Distribution]dockerDistribution{
	RedisOSS: {image: "redis:8.8.0-alpine", server: "redis-server", cli: "redis-cli"},
	Valkey8:  {image: "valkey/valkey:8.1.9-alpine", server: "valkey-server", cli: "valkey-cli"},
	Valkey9:  {image: "valkey/valkey:9.1.1-alpine", server: "valkey-server", cli: "valkey-cli"},
}

type dockerCluster struct {
	distribution Distribution
	definition   dockerDistribution
	seedAddrs    []string
	containers   []string
	closeOnce    sync.Once
	closeErr     error
}

// StartCluster starts three primaries and three replicas with Docker host
// networking, waits for all slots to be covered, and registers idempotent test
// cleanup. It skips unless EnvRunClusterTests is true.
func StartCluster(t testing.TB, distribution Distribution) Cluster {
	t.Helper()
	if strings.ToLower(strings.TrimSpace(os.Getenv(EnvRunClusterTests))) != "true" {
		t.Skipf("real Redis/Valkey cluster test; set %s=true", EnvRunClusterTests)
	}
	if runtime.GOOS != "linux" {
		t.Skip("real cluster fixture requires Linux Docker host networking; run manually inside a Linux Docker test runner on macOS or Windows")
	}
	definition, ok := dockerDistributions[distribution]
	if !ok {
		t.Fatalf("unsupported Redis test distribution %q", distribution)
	}
	if _, err := exec.LookPath("docker"); err != nil {
		t.Fatalf("Docker is required for cluster integration tests: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	cluster, err := startDockerCluster(ctx, distribution, definition, clusterBasePort(t))
	if err != nil {
		t.Fatalf("start %s cluster: %v", distribution, err)
	}
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cleanupCancel()
		if err := cluster.Close(cleanupCtx); err != nil {
			t.Errorf("close %s cluster: %v", distribution, err)
		}
	})
	return cluster
}

func clusterBasePort(t testing.TB) int {
	t.Helper()
	const defaultBasePort = 17000
	raw := strings.TrimSpace(os.Getenv(EnvClusterBasePort))
	if raw == "" {
		return defaultBasePort
	}
	port, err := strconv.Atoi(raw)
	if err != nil || port < 1024 || port > 45000 {
		t.Fatalf("%s must be an integer from 1024 through 45000", EnvClusterBasePort)
	}
	return port
}

func startDockerCluster(
	ctx context.Context,
	distribution Distribution,
	definition dockerDistribution,
	basePort int,
) (*dockerCluster, error) {
	suffix := fmt.Sprintf("%d-%d", os.Getpid(), time.Now().UnixNano())
	cluster := &dockerCluster{distribution: distribution, definition: definition}
	for index := 0; index < 6; index++ {
		port := basePort + index
		busPort := basePort + 100 + index
		name := fmt.Sprintf("truvag3-%s-%s-%d", distribution, suffix, index)
		args := []string{
			"run", "-d", "--rm", "--network", "host", "--name", name,
			definition.image, definition.server,
			"--port", strconv.Itoa(port),
			"--bind", "127.0.0.1",
			"--protected-mode", "no",
			"--appendonly", "no",
			"--save", "",
			"--cluster-enabled", "yes",
			"--cluster-config-file", "nodes.conf",
			"--cluster-node-timeout", "1000",
			"--cluster-require-full-coverage", "no",
			"--cluster-port", strconv.Itoa(busPort),
			"--cluster-replica-validity-factor", "0",
			"--cluster-announce-ip", "127.0.0.1",
			"--cluster-announce-port", strconv.Itoa(port),
			"--cluster-announce-bus-port", strconv.Itoa(busPort),
		}
		if _, err := runCommand(ctx, "docker", args...); err != nil {
			_ = cluster.Close(context.Background())
			return nil, fmt.Errorf("start node %d: %w", index, err)
		}
		cluster.containers = append(cluster.containers, name)
		cluster.seedAddrs = append(cluster.seedAddrs, fmt.Sprintf("127.0.0.1:%d", port))
	}

	if err := cluster.waitForNodes(ctx); err != nil {
		_ = cluster.Close(context.Background())
		return nil, err
	}
	createArgs := []string{"exec", cluster.containers[0], definition.cli, "--cluster", "create"}
	createArgs = append(createArgs, cluster.seedAddrs...)
	createArgs = append(createArgs, "--cluster-replicas", "1", "--cluster-yes")
	if output, err := runCommand(ctx, "docker", createArgs...); err != nil {
		_ = cluster.Close(context.Background())
		return nil, fmt.Errorf("create cluster (%s): %w", boundedOutput(output), err)
	}
	if err := cluster.waitForHealthy(ctx); err != nil {
		_ = cluster.Close(context.Background())
		return nil, err
	}
	return cluster, nil
}

func (cluster *dockerCluster) waitForNodes(ctx context.Context) error {
	for index, container := range cluster.containers {
		port := strings.TrimPrefix(cluster.seedAddrs[index], "127.0.0.1:")
		if err := waitUntil(ctx, func() bool {
			output, err := runCommand(ctx, "docker", "exec", container, cluster.definition.cli, "-h", "127.0.0.1", "-p", port, "PING")
			return err == nil && strings.TrimSpace(output) == "PONG"
		}); err != nil {
			return fmt.Errorf("node %d did not become ready: %w", index, err)
		}
	}
	return nil
}

func (cluster *dockerCluster) waitForHealthy(ctx context.Context) error {
	if err := waitUntil(ctx, func() bool {
		for index, container := range cluster.containers {
			port := strings.TrimPrefix(cluster.seedAddrs[index], "127.0.0.1:")
			output, err := runCommand(
				ctx, "docker", "exec", container, cluster.definition.cli,
				"-h", "127.0.0.1", "-p", port, "CLUSTER", "INFO",
			)
			if err != nil || !strings.Contains(output, "cluster_state:ok") ||
				!strings.Contains(output, "cluster_slots_assigned:16384") ||
				!strings.Contains(output, "cluster_known_nodes:6") {
				return false
			}
		}
		return true
	}); err != nil {
		return fmt.Errorf("all cluster nodes did not become healthy: %w", err)
	}
	return nil
}

func waitUntil(ctx context.Context, ready func() bool) error {
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		if ready() {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

func runCommand(ctx context.Context, executable string, args ...string) (string, error) {
	if executable != "docker" {
		return "", fmt.Errorf("unsupported fixture executable %q", executable)
	}
	// #nosec G204 -- internal test fixtures construct every Docker argument;
	// no application or request input reaches this helper.
	command := exec.CommandContext(ctx, "docker", args...)
	output, err := command.CombinedOutput()
	return string(output), err
}

func boundedOutput(output string) string {
	const max = 2048
	output = strings.TrimSpace(output)
	if len(output) <= max {
		return output
	}
	return output[len(output)-max:]
}

func (cluster *dockerCluster) SeedAddrs() []string {
	return append([]string(nil), cluster.seedAddrs...)
}

type clusterNode struct {
	id        string
	addr      string
	flags     string
	primary   string
	port      string
	container string
	slots     []slotRange
}

type slotRange struct {
	first int64
	last  int64
}

func (cluster *dockerCluster) clusterNodes(ctx context.Context) ([]clusterNode, error) {
	output, err := runCommand(
		ctx,
		"docker", "exec", cluster.containers[0], cluster.definition.cli,
		"-h", "127.0.0.1", "-p", strings.TrimPrefix(cluster.seedAddrs[0], "127.0.0.1:"),
		"CLUSTER", "NODES",
	)
	if err != nil {
		return nil, fmt.Errorf("read cluster nodes: %w", err)
	}
	containerByPort := make(map[string]string, len(cluster.seedAddrs))
	for index, address := range cluster.seedAddrs {
		_, port, _ := strings.Cut(address, ":")
		containerByPort[port] = cluster.containers[index]
	}
	var nodes []clusterNode
	for _, line := range strings.Split(strings.TrimSpace(output), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 4 {
			continue
		}
		address := strings.Split(fields[1], "@")[0]
		_, port, found := strings.Cut(address, ":")
		if !found {
			continue
		}
		node := clusterNode{
			id: fields[0], addr: address, flags: fields[2], primary: fields[3],
			port: port, container: containerByPort[port],
		}
		for _, specification := range fields[8:] {
			if strings.HasPrefix(specification, "[") {
				continue
			}
			firstText, lastText, ranged := strings.Cut(specification, "-")
			first, parseErr := strconv.ParseInt(firstText, 10, 64)
			if parseErr != nil {
				continue
			}
			last := first
			if ranged {
				last, parseErr = strconv.ParseInt(lastText, 10, 64)
				if parseErr != nil {
					continue
				}
			}
			node.slots = append(node.slots, slotRange{first: first, last: last})
		}
		nodes = append(nodes, node)
	}
	return nodes, nil
}

func (node clusterNode) ownsSlot(slot int64) bool {
	for _, assigned := range node.slots {
		if assigned.first <= slot && slot <= assigned.last {
			return true
		}
	}
	return false
}

func (cluster *dockerCluster) runNodeCommand(ctx context.Context, node clusterNode, args ...string) (string, error) {
	command := []string{
		"exec", node.container, cluster.definition.cli,
		"-h", "127.0.0.1", "-p", node.port,
	}
	command = append(command, args...)
	return runCommand(ctx, "docker", command...)
}

type dockerSlotMigration struct {
	cluster *dockerCluster
	slot    int64
	source  clusterNode
	target  clusterNode
	once    sync.Once
	err     error
}

// BeginSlotMigration selects the exact caller-provided slot, then pauses after
// declaring its source MIGRATING and target IMPORTING. This makes the topology
// transition deterministic and exposes real ASK replies to the integration
// test rather than merely proving that an arbitrary reshard completed.
func (cluster *dockerCluster) BeginSlotMigration(ctx context.Context, slot int64) (SlotMigration, error) {
	if slot < 0 || slot > 16383 {
		return nil, fmt.Errorf("cluster slot must be from 0 through 16383")
	}
	nodes, err := cluster.clusterNodes(ctx)
	if err != nil {
		return nil, err
	}
	var source clusterNode
	var target clusterNode
	for _, node := range nodes {
		if !strings.Contains(node.flags, "master") || strings.Contains(node.flags, "fail") {
			continue
		}
		if node.ownsSlot(slot) {
			source = node
			continue
		}
		if target.container == "" {
			target = node
		}
	}
	if source.container == "" || target.container == "" {
		return nil, fmt.Errorf("slot migration requires a source owner and a distinct healthy primary")
	}
	slotText := strconv.FormatInt(slot, 10)
	if output, commandErr := cluster.runNodeCommand(
		ctx, target, "CLUSTER", "SETSLOT", slotText, "IMPORTING", source.id,
	); commandErr != nil {
		return nil, fmt.Errorf("mark slot importing (%s): %w", boundedOutput(output), commandErr)
	}
	if output, commandErr := cluster.runNodeCommand(
		ctx, source, "CLUSTER", "SETSLOT", slotText, "MIGRATING", target.id,
	); commandErr != nil {
		_, _ = cluster.runNodeCommand(context.Background(), target, "CLUSTER", "SETSLOT", slotText, "STABLE")
		return nil, fmt.Errorf("mark slot migrating (%s): %w", boundedOutput(output), commandErr)
	}
	return &dockerSlotMigration{cluster: cluster, slot: slot, source: source, target: target}, nil
}

// Complete moves every key in the selected slot and publishes its new owner to
// every node. MIGRATE uses the test fixture's loopback-only, unauthenticated
// endpoints; production connection details never pass through this path.
func (migration *dockerSlotMigration) Complete(ctx context.Context) error {
	migration.once.Do(func() {
		migration.err = migration.complete(ctx)
	})
	return migration.err
}

func (migration *dockerSlotMigration) complete(ctx context.Context) error {
	slotText := strconv.FormatInt(migration.slot, 10)
	for {
		output, err := migration.cluster.runNodeCommand(
			ctx, migration.source, "CLUSTER", "GETKEYSINSLOT", slotText, "100",
		)
		if err != nil {
			return fmt.Errorf("list keys in migrating slot (%s): %w", boundedOutput(output), err)
		}
		keys := strings.Fields(strings.TrimSpace(output))
		if len(keys) == 0 {
			break
		}
		for _, key := range keys {
			output, err = migration.cluster.runNodeCommand(
				ctx, migration.source,
				"MIGRATE", "127.0.0.1", migration.target.port, key, "0", "5000", "REPLACE",
			)
			if err != nil || strings.TrimSpace(output) != "OK" {
				return fmt.Errorf("migrate key in selected slot (%s): %w", boundedOutput(output), err)
			}
		}
	}

	nodes, err := migration.cluster.clusterNodes(ctx)
	if err != nil {
		return err
	}
	for _, node := range nodes {
		output, commandErr := migration.cluster.runNodeCommand(
			ctx, node, "CLUSTER", "SETSLOT", slotText, "NODE", migration.target.id,
		)
		if commandErr != nil {
			return fmt.Errorf("publish migrated slot owner (%s): %w", boundedOutput(output), commandErr)
		}
	}
	return migration.cluster.waitForHealthy(ctx)
}

// DisableShardForSlot stops both the primary that owns slot and its replica.
// Callers must run this destructive scenario last because the fixture is not
// expected to recover the disabled shard before cleanup.
func (cluster *dockerCluster) DisableShardForSlot(ctx context.Context, slot int64) error {
	nodes, err := cluster.clusterNodes(ctx)
	if err != nil {
		return err
	}
	var primary clusterNode
	for _, node := range nodes {
		if strings.Contains(node.flags, "master") && !strings.Contains(node.flags, "fail") && node.ownsSlot(slot) {
			primary = node
			break
		}
	}
	if primary.container == "" {
		return fmt.Errorf("no healthy primary owns slot %d", slot)
	}
	var targets []clusterNode
	targets = append(targets, primary)
	for _, node := range nodes {
		if strings.Contains(node.flags, "slave") && node.primary == primary.id {
			targets = append(targets, node)
			break
		}
	}
	if len(targets) != 2 {
		return fmt.Errorf("slot %d primary has no replica to disable", slot)
	}
	for _, target := range targets {
		if output, commandErr := runCommand(ctx, "docker", "stop", "--time", "0", target.container); commandErr != nil {
			return fmt.Errorf("stop slot %d shard node (%s): %w", slot, boundedOutput(output), commandErr)
		}
	}
	return nil
}

// PrimaryMetrics returns one INFO snapshot per healthy primary.
func (cluster *dockerCluster) PrimaryMetrics(ctx context.Context) ([]PrimaryMetrics, error) {
	nodes, err := cluster.clusterNodes(ctx)
	if err != nil {
		return nil, err
	}
	metrics := make([]PrimaryMetrics, 0, 3)
	for _, node := range nodes {
		if !strings.Contains(node.flags, "master") || strings.Contains(node.flags, "fail") {
			continue
		}
		output, commandErr := cluster.runNodeCommand(ctx, node, "INFO", "all")
		if commandErr != nil {
			return nil, fmt.Errorf("read primary INFO (%s): %w", boundedOutput(output), commandErr)
		}
		values := parseRedisInfo(output)
		userCPU, parseErr := strconv.ParseFloat(values["used_cpu_user"], 64)
		if parseErr != nil {
			return nil, fmt.Errorf("parse primary used_cpu_user")
		}
		systemCPU, parseErr := strconv.ParseFloat(values["used_cpu_sys"], 64)
		if parseErr != nil {
			return nil, fmt.Errorf("parse primary used_cpu_sys")
		}
		commands, parseErr := strconv.ParseUint(values["total_commands_processed"], 10, 64)
		if parseErr != nil {
			return nil, fmt.Errorf("parse primary total_commands_processed")
		}
		instantaneous, parseErr := strconv.ParseInt(values["instantaneous_ops_per_sec"], 10, 64)
		if parseErr != nil {
			return nil, fmt.Errorf("parse primary instantaneous_ops_per_sec")
		}
		connections, parseErr := strconv.ParseInt(values["connected_clients"], 10, 64)
		if parseErr != nil {
			return nil, fmt.Errorf("parse primary connected_clients")
		}
		metrics = append(metrics, PrimaryMetrics{
			Addr: node.addr, CPUSeconds: userCPU + systemCPU, TotalCommands: commands,
			InstantaneousOps: instantaneous, ConnectedClients: connections,
		})
	}
	return metrics, nil
}

func parseRedisInfo(output string) map[string]string {
	values := make(map[string]string)
	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, value, found := strings.Cut(line, ":")
		if found {
			values[key] = strings.TrimSpace(value)
		}
	}
	return values
}

// TriggerPrimaryFailover asks one replica to perform a coordinated manual
// failover and waits until it is promoted.
func (cluster *dockerCluster) TriggerPrimaryFailover(ctx context.Context) error {
	nodes, err := cluster.clusterNodes(ctx)
	if err != nil {
		return err
	}
	primaryIDs := make(map[string]struct{})
	for _, node := range nodes {
		if strings.Contains(node.flags, "master") && !strings.Contains(node.flags, "fail") {
			primaryIDs[node.id] = struct{}{}
		}
	}
	var replica clusterNode
	for _, node := range nodes {
		if _, ok := primaryIDs[node.primary]; ok && strings.Contains(node.flags, "slave") {
			replica = node
			break
		}
	}
	if replica.container == "" {
		return fmt.Errorf("no healthy replica found for failover")
	}
	if output, err := runCommand(
		ctx, "docker", "exec", replica.container, cluster.definition.cli,
		"-h", "127.0.0.1", "-p", replica.port, "CLUSTER", "FAILOVER",
	); err != nil {
		return fmt.Errorf("trigger cluster failover (%s): %w", boundedOutput(output), err)
	}
	if err := waitUntil(ctx, func() bool {
		output, commandErr := runCommand(
			ctx, "docker", "exec", replica.container, cluster.definition.cli,
			"-h", "127.0.0.1", "-p", replica.port, "ROLE",
		)
		return commandErr == nil && strings.HasPrefix(strings.TrimSpace(output), "master")
	}); err != nil {
		return fmt.Errorf("wait for cluster primary promotion: %w", err)
	}
	return cluster.waitForHealthy(ctx)
}

func (cluster *dockerCluster) Close(ctx context.Context) error {
	cluster.closeOnce.Do(func() {
		for index := len(cluster.containers) - 1; index >= 0; index-- {
			if output, err := runCommand(ctx, "docker", "rm", "-f", cluster.containers[index]); err != nil && !strings.Contains(output, "No such container") && cluster.closeErr == nil {
				cluster.closeErr = fmt.Errorf("remove cluster node: %w", err)
			}
		}
	})
	return cluster.closeErr
}
