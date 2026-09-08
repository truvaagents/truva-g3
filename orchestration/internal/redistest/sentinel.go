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
	EnvRunSentinelTests = "TRUVAG3_RUN_REDIS_SENTINEL_TESTS"
	EnvSentinelBasePort = "TRUVAG3_TEST_REDIS_SENTINEL_BASE_PORT"
	testSentinelMaster  = "truvag3-primary"
)

// Sentinel is an owned primary, replica, and three-Sentinel test topology.
type Sentinel interface {
	Addrs() []string
	MasterName() string
	TriggerFailover(context.Context) error
	Close(context.Context) error
}

type dockerSentinel struct {
	definition    dockerDistribution
	masterPort    int
	replicaPort   int
	sentinelPorts []int
	master        string
	replica       string
	sentinels     []string
	closeOnce     sync.Once
	closeErr      error
}

// StartSentinel starts one Redis primary, one replica, and three Sentinel
// processes. It skips unless EnvRunSentinelTests is true.
func StartSentinel(t testing.TB) Sentinel {
	t.Helper()
	if strings.ToLower(strings.TrimSpace(os.Getenv(EnvRunSentinelTests))) != "true" {
		t.Skipf("real Redis Sentinel test; set %s=true", EnvRunSentinelTests)
	}
	if runtime.GOOS != "linux" {
		t.Skip("real Sentinel fixture requires Linux Docker host networking")
	}
	if _, err := exec.LookPath("docker"); err != nil {
		t.Fatalf("Docker is required for Sentinel integration tests: %v", err)
	}
	basePort := sentinelBasePort(t)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	fixture, err := startDockerSentinel(ctx, basePort)
	if err != nil {
		t.Fatalf("start Redis Sentinel fixture: %v", err)
	}
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cleanupCancel()
		if err := fixture.Close(cleanupCtx); err != nil {
			t.Errorf("close Redis Sentinel fixture: %v", err)
		}
	})
	return fixture
}

func sentinelBasePort(t testing.TB) int {
	t.Helper()
	const defaultBasePort = 18000
	raw := strings.TrimSpace(os.Getenv(EnvSentinelBasePort))
	if raw == "" {
		return defaultBasePort
	}
	port, err := strconv.Atoi(raw)
	if err != nil || port < 1024 || port > 60000 {
		t.Fatalf("%s must be an integer from 1024 through 60000", EnvSentinelBasePort)
	}
	return port
}

func startDockerSentinel(ctx context.Context, basePort int) (*dockerSentinel, error) {
	definition := dockerDistributions[RedisOSS]
	suffix := fmt.Sprintf("%d-%d", os.Getpid(), time.Now().UnixNano())
	fixture := &dockerSentinel{
		definition: definition, masterPort: basePort, replicaPort: basePort + 1,
		sentinelPorts: []int{basePort + 2, basePort + 3, basePort + 4},
		master:        fmt.Sprintf("truvag3-sentinel-%s-primary", suffix),
		replica:       fmt.Sprintf("truvag3-sentinel-%s-replica", suffix),
	}
	serverArgs := func(name string, port int, extra ...string) []string {
		args := []string{
			"run", "-d", "--rm", "--network", "host", "--name", name,
			definition.image, definition.server,
			"--port", strconv.Itoa(port), "--bind", "127.0.0.1", "--protected-mode", "no",
			"--appendonly", "no", "--save", "",
		}
		return append(args, extra...)
	}
	if output, err := runCommand(ctx, "docker", serverArgs(fixture.master, fixture.masterPort)...); err != nil {
		return nil, fmt.Errorf("start Sentinel primary (%s): %w", boundedOutput(output), err)
	}
	if output, err := runCommand(ctx, "docker", serverArgs(
		fixture.replica, fixture.replicaPort,
		"--replicaof", "127.0.0.1", strconv.Itoa(fixture.masterPort),
	)...); err != nil {
		_ = fixture.Close(context.Background())
		return nil, fmt.Errorf("start Sentinel replica (%s): %w", boundedOutput(output), err)
	}
	if err := waitUntil(ctx, func() bool {
		output, commandErr := runCommand(ctx, "docker", "exec", fixture.replica, definition.cli, "-p", strconv.Itoa(fixture.replicaPort), "ROLE")
		return commandErr == nil && strings.HasPrefix(strings.TrimSpace(output), "slave")
	}); err != nil {
		_ = fixture.Close(context.Background())
		return nil, fmt.Errorf("replica did not attach to primary: %w", err)
	}

	for index, port := range fixture.sentinelPorts {
		name := fmt.Sprintf("truvag3-sentinel-%s-monitor-%d", suffix, index)
		configuration := fmt.Sprintf(
			"port %d\nbind 127.0.0.1\nprotected-mode no\nsentinel monitor %s 127.0.0.1 %d 2\nsentinel down-after-milliseconds %s 1000\nsentinel failover-timeout %s 5000\nsentinel parallel-syncs %s 1\n",
			port, testSentinelMaster, fixture.masterPort, testSentinelMaster, testSentinelMaster, testSentinelMaster,
		)
		command := "printf '%s' '" + configuration + "' > /tmp/sentinel.conf && exec redis-server /tmp/sentinel.conf --sentinel"
		if output, err := runCommand(
			ctx, "docker", "run", "-d", "--rm", "--network", "host", "--name", name,
			definition.image, "sh", "-c", command,
		); err != nil {
			_ = fixture.Close(context.Background())
			return nil, fmt.Errorf("start Sentinel monitor %d (%s): %w", index, boundedOutput(output), err)
		}
		fixture.sentinels = append(fixture.sentinels, name)
	}
	if err := fixture.waitForMaster(ctx, fixture.masterPort); err != nil {
		_ = fixture.Close(context.Background())
		return nil, err
	}
	return fixture, nil
}

func (fixture *dockerSentinel) Addrs() []string {
	addresses := make([]string, len(fixture.sentinelPorts))
	for index, port := range fixture.sentinelPorts {
		addresses[index] = fmt.Sprintf("127.0.0.1:%d", port)
	}
	return addresses
}

func (*dockerSentinel) MasterName() string { return testSentinelMaster }

func (fixture *dockerSentinel) waitForMaster(ctx context.Context, wantPort int) error {
	if err := waitUntil(ctx, func() bool {
		for index, sentinel := range fixture.sentinels {
			output, commandErr := runCommand(
				ctx, "docker", "exec", sentinel, fixture.definition.cli,
				"-p", strconv.Itoa(fixture.sentinelPorts[index]),
				"SENTINEL", "get-master-addr-by-name", testSentinelMaster,
			)
			if commandErr == nil && strings.Contains(output, strconv.Itoa(wantPort)) {
				return true
			}
		}
		return false
	}); err != nil {
		return fmt.Errorf("Sentinel did not report primary port %d: %w", wantPort, err)
	}
	return nil
}

func (fixture *dockerSentinel) TriggerFailover(ctx context.Context) error {
	if output, err := runCommand(ctx, "docker", "rm", "-f", fixture.master); err != nil {
		return fmt.Errorf("stop Sentinel primary (%s): %w", boundedOutput(output), err)
	}
	return fixture.waitForMaster(ctx, fixture.replicaPort)
}

func (fixture *dockerSentinel) Close(ctx context.Context) error {
	fixture.closeOnce.Do(func() {
		containers := append([]string{fixture.master, fixture.replica}, fixture.sentinels...)
		for index := len(containers) - 1; index >= 0; index-- {
			if output, err := runCommand(ctx, "docker", "rm", "-f", containers[index]); err != nil && !strings.Contains(output, "No such container") && fixture.closeErr == nil {
				fixture.closeErr = fmt.Errorf("remove Sentinel container: %w", err)
			}
		}
	})
	return fixture.closeErr
}
