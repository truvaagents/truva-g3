package core

import (
	"context"
	"errors"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

func TestAgentDiscoveryAccessorObservesLateRecovery(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	address := listener.Addr().String()
	require.NoError(t, listener.Close())
	config := DefaultConfig()
	config.Discovery.Enabled = true
	config.Discovery.Provider = "redis"
	config.Discovery.RedisURL = "redis://" + address
	config.Discovery.RedisConnection = &RedisConnectionConfig{Mode: RedisModeStandalone, Addrs: []string{address}, DialTimeout: 50 * time.Millisecond, MaxRetries: -1}
	config.Discovery.RetryOnFailure = true
	config.Discovery.RetryInterval = 20 * time.Millisecond
	config.Development.MockDiscovery = false
	agent := NewBaseAgentWithConfig(config)
	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)
	require.NoError(t, agent.Initialize(ctx))
	require.Nil(t, agent.GetDiscovery())
	_, err = agent.Discover(ctx, DiscoveryFilter{})
	require.Error(t, err)

	server := miniredis.NewMiniRedis()
	require.NoError(t, server.StartAddr(address))
	t.Cleanup(server.Close)
	require.Eventually(t, func() bool { return agent.GetDiscovery() != nil }, 5*time.Second, time.Millisecond)
	discovery, ok := agent.GetDiscovery().(*RedisDiscovery)
	require.True(t, ok)
	t.Cleanup(func() { cancel(); _ = discovery.Close() })
	services, err := agent.Discover(ctx, DiscoveryFilter{})
	require.NoError(t, err)
	require.NotEmpty(t, services)
}

func TestAgentDiscoveryAccessorConcurrentReplacement(t *testing.T) {
	agent := NewBaseAgent("discovery-accessor")
	first, second := NewMockDiscovery(), NewMockDiscovery()
	done := make(chan struct{})
	go func() {
		defer close(done)
		for range 1000 {
			agent.mu.Lock()
			agent.Discovery = first
			agent.mu.Unlock()
			agent.mu.Lock()
			agent.Discovery = second
			agent.mu.Unlock()
		}
	}()
	for range 1000 {
		discovery := agent.GetDiscovery()
		require.True(t, discovery == nil || discovery == first || discovery == second)
		_, _ = agent.Discover(t.Context(), DiscoveryFilter{})
	}
	<-done
	require.Same(t, second, agent.GetDiscovery())
}

type registryRevivalHook struct {
	key    string
	revive func()
}

func (*registryRevivalHook) DialHook(next redis.DialHook) redis.DialHook { return next }
func (*registryRevivalHook) ProcessPipelineHook(next redis.ProcessPipelineHook) redis.ProcessPipelineHook {
	return next
}
func (h *registryRevivalHook) ProcessHook(next redis.ProcessHook) redis.ProcessHook {
	return func(ctx context.Context, cmd redis.Cmder) error {
		err := next(ctx, cmd)
		if cmd.Name() == "get" && cmd.Args()[1] == h.key && errors.Is(err, redis.Nil) && h.revive != nil {
			revive := h.revive
			h.revive = nil
			revive()
		}
		return err
	}
}

func TestDiscoveryCleanupPreservesConcurrentRegistration(t *testing.T) {
	for name, filter := range map[string]DiscoveryFilter{
		"all": {}, "type": {Type: ComponentTypeTool}, "name": {Name: "forecast"},
		"capability": {Capabilities: []string{"weather"}},
		"combined":   {Type: ComponentTypeTool, Name: "forecast", Capabilities: []string{"weather"}},
	} {
		t.Run(name, func(t *testing.T) {
			server := miniredis.RunT(t)
			reader := redis.NewClient(&redis.Options{Addr: server.Addr()})
			writer := redis.NewClient(&redis.Options{Addr: server.Addr()})
			t.Cleanup(func() { _ = reader.Close(); _ = writer.Close() })
			keys, err := NewRedisKeyspace("cleanup-race")
			require.NoError(t, err)
			discovery, err := NewRedisDiscoveryWithClient(reader, keys, time.Minute)
			require.NoError(t, err)
			registry, err := NewRedisRegistryWithClient(writer, keys, time.Minute)
			require.NoError(t, err)
			info := &ServiceInfo{ID: "same-id", Name: "forecast", Type: ComponentTypeTool, Capabilities: []Capability{{Name: "weather"}}}
			require.NoError(t, registry.Register(t.Context(), info))
			key := registry.keys.service(info.ID)
			require.NoError(t, writer.Del(t.Context(), key).Err())
			reader.AddHook(&registryRevivalHook{key: key, revive: func() { require.NoError(t, registry.Register(t.Context(), info)) }})
			_, err = discovery.Discover(t.Context(), filter)
			require.NoError(t, err)
			services, err := discovery.Discover(t.Context(), filter)
			require.NoError(t, err)
			require.Len(t, services, 1, "cleanup removed freshly restored index membership")
			require.NoError(t, writer.Del(t.Context(), key).Err())
			services, err = discovery.Discover(t.Context(), filter)
			require.NoError(t, err)
			require.Empty(t, services)
			for _, index := range []string{registry.keys.all(), registry.keys.name(info.Name), registry.keys.serviceType(info.Type), registry.keys.capability("weather")} {
				if name == "all" && index != registry.keys.all() {
					continue
				}
				if name == "type" && index != registry.keys.serviceType(info.Type) {
					continue
				}
				if name == "name" && index != registry.keys.name(info.Name) {
					continue
				}
				if name == "capability" && index != registry.keys.capability("weather") {
					continue
				}
				if name == "combined" && index == registry.keys.all() {
					continue
				}
				require.False(t, writer.SIsMember(t.Context(), index, info.ID).Val(), "genuinely stale member was not pruned")
			}
		})
	}
}

func TestExplicitRedisDeploymentOverridesInvalidNamespaceEnvironment(t *testing.T) {
	t.Setenv("TRUVAG3_REDIS_NAMESPACE", "{invalid}")
	cfg, err := NewConfig(WithRedisDeployment("explicit"))
	require.NoError(t, err)
	require.Equal(t, "explicit", cfg.Discovery.RedisKeyspace.Deployment())
	_, err = NewConfig()
	require.ErrorIs(t, err, ErrInvalidConfiguration)
	_, err = NewConfig(WithRedisConnection(DefaultRedisConnectionConfig()))
	require.ErrorIs(t, err, ErrInvalidConfiguration, "connection override must not clear namespace validation")
	_, err = NewConfig(WithRedisDeployment("{invalid-option}"))
	require.ErrorIs(t, err, ErrInvalidConfiguration)
}

type registryDiagnosticFailureClient struct {
	redis.UniversalClient
	cause error
}

type discoveryCleanupFailureClient struct {
	redis.UniversalClient
	cause error
}

func (c *discoveryCleanupFailureClient) EvalSha(ctx context.Context, _ string, _ []string, _ ...interface{}) *redis.Cmd {
	command := redis.NewCmd(ctx)
	command.SetErr(c.cause)
	return command
}

func TestDiscoveryCleanupFailureKeepsLiveResults(t *testing.T) {
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	keys, err := NewRedisKeyspace("cleanup-failure")
	require.NoError(t, err)
	discovery, err := NewRedisDiscoveryWithClient(
		&discoveryCleanupFailureClient{client, errors.New("redis://user:diagnostic-secret@private-host/0")},
		keys, time.Minute,
	)
	require.NoError(t, err)
	ctx := WithRequestID(t.Context(), "cleanup-request")
	require.NoError(t, discovery.Register(ctx, &ServiceInfo{ID: "live", Name: "live", Type: ComponentTypeTool}))
	require.NoError(t, client.SAdd(ctx, discovery.keys.all(), "expired").Err())
	logger := &MockLogger{}
	discovery.SetLogger(logger)

	services, err := discovery.Discover(ctx, DiscoveryFilter{})
	require.NoError(t, err, "advisory cleanup must not fail a successful lookup")
	require.Len(t, services, 1)
	require.Equal(t, "live", services[0].ID)
	require.True(t, client.SIsMember(ctx, discovery.keys.all(), "expired").Val())
	warnings := 0
	for _, entry := range logger.entries {
		if entry.Level != "warn" {
			continue
		}
		warnings++
		require.Equal(t, "discovery_index_cleanup", entry.Fields["operation"])
		require.Equal(t, "cleanup-request", entry.Fields["request_id"])
		require.Equal(t, "index_write", entry.Fields["error_type"])
		require.Equal(t, "redis discovery index cleanup failed", entry.Fields["error"])
	}
	require.Equal(t, 1, warnings)
}

func (c *registryDiagnosticFailureClient) Get(ctx context.Context, key string) *redis.StringCmd {
	return redis.NewStringResult("", c.cause)
}
func (c *registryDiagnosticFailureClient) SScan(ctx context.Context, key string, cursor uint64, match string, count int64) *redis.ScanCmd {
	cmd := redis.NewScanCmd(ctx, nil)
	cmd.SetErr(c.cause)
	return cmd
}
func (c *registryDiagnosticFailureClient) TxPipelined(context.Context, func(redis.Pipeliner) error) ([]redis.Cmder, error) {
	return nil, c.cause
}

func TestRegistryDiagnosticsAreBoundedAndCorrelated(t *testing.T) {
	cause := errors.New("redis://user:diagnostic-secret@private-host/0")
	for _, operation := range []string{"discover", "register", "heartbeat", "unregister"} {
		t.Run(operation, func(t *testing.T) {
			server := miniredis.RunT(t)
			client := redis.NewClient(&redis.Options{Addr: server.Addr()})
			t.Cleanup(func() { _ = client.Close() })
			keys, err := NewRedisKeyspace("diagnostics")
			require.NoError(t, err)
			discovery, err := NewRedisDiscoveryWithClient(&registryDiagnosticFailureClient{client, cause}, keys, time.Minute)
			require.NoError(t, err)
			logger := &MockLogger{}
			discovery.SetLogger(logger)
			ctx := WithRequestID(t.Context(), "diagnostic-request")
			switch operation {
			case "discover":
				_, err = discovery.Discover(ctx, DiscoveryFilter{})
			case "register":
				err = discovery.Register(ctx, &ServiceInfo{ID: "id", Name: "name", Type: ComponentTypeTool})
			case "heartbeat":
				err = discovery.UpdateHealth(ctx, "id", HealthHealthy)
			case "unregister":
				err = discovery.Unregister(ctx, "id")
			}
			require.ErrorIs(t, err, cause)
			require.NotEmpty(t, logger.entries)
			for _, entry := range logger.entries {
				require.Equal(t, "diagnostic-request", entry.Fields["request_id"])
				require.NotEmpty(t, entry.Fields["operation"])
				if entry.Fields["error"] != nil {
					require.NotEmpty(t, entry.Fields["error_type"])
				}
				for key, value := range entry.Fields {
					require.False(t, key == "key" || strings.HasSuffix(key, "_key"))
					if text, ok := value.(string); ok {
						require.NotContains(t, text, "diagnostic-secret")
						require.NotContains(t, text, "private-host")
					}
					_, rawError := value.(error)
					require.False(t, rawError, "raw backend error in diagnostic")
				}
			}
		})
	}
}
