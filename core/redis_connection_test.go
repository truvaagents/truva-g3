package core

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

func TestRedisUniversalClientUsesExplicitTopologyMode(t *testing.T) {
	tests := []struct {
		name   string
		config RedisConnectionConfig
		assert func(*testing.T, redis.UniversalClient)
	}{
		{
			name: "standalone",
			config: RedisConnectionConfig{
				Mode: RedisModeStandalone, Addrs: []string{"standalone.example:6379"},
			},
			assert: func(t *testing.T, client redis.UniversalClient) {
				_, ok := client.(*redis.Client)
				require.True(t, ok)
			},
		},
		{
			name: "sentinel",
			config: RedisConnectionConfig{
				Mode: RedisModeSentinel, Addrs: []string{"sentinel.example:26379"}, MasterName: "mymaster",
			},
			assert: func(t *testing.T, client redis.UniversalClient) {
				_, ok := client.(*redis.Client)
				require.True(t, ok)
			},
		},
		{
			name: "cluster",
			config: RedisConnectionConfig{
				Mode: RedisModeCluster, Addrs: []string{"cluster.example:6379"},
			},
			assert: func(t *testing.T, client redis.UniversalClient) {
				_, ok := client.(*redis.ClusterClient)
				require.True(t, ok)
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			client, err := newRedisUniversalClient(test.config, false)
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, client.Close()) })
			test.assert(t, client)
		})
	}
}

func TestRedisUniversalClientVerifiesStartup(t *testing.T) {
	server := miniredis.RunT(t)
	client, err := NewRedisUniversalClient(RedisConnectionConfig{
		Mode: RedisModeStandalone, Addrs: []string{server.Addr()},
	})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, client.Close()) })
	require.NoError(t, client.Ping(t.Context()).Err())
}

func TestCanonicalRedisUniversalClientRequiresDBZero(t *testing.T) {
	server := miniredis.RunT(t)
	config := RedisConnectionConfig{
		Mode: RedisModeStandalone, Addrs: []string{server.Addr()}, DB: 2,
	}
	_, err := NewRedisUniversalClient(config)
	require.ErrorIs(t, err, ErrInvalidConfiguration)

	client, err := NewRedisUniversalClientForCompatibility(config)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, client.Close()) })
	require.NoError(t, client.Set(t.Context(), "compatibility-key", "value", 0).Err())
	require.True(t, server.DB(2).Exists("compatibility-key"))
}

func TestRedisUniversalClientBoundsStartupCheckIndependently(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	release := make(chan struct{})
	serverDone := make(chan struct{})
	go func() {
		defer close(serverDone)
		connection, acceptErr := listener.Accept()
		if acceptErr != nil {
			return
		}
		defer connection.Close()
		<-release
	}()
	t.Cleanup(func() {
		close(release)
		_ = listener.Close()
		<-serverDone
	})

	started := time.Now()
	_, err = NewRedisUniversalClient(RedisConnectionConfig{
		Mode:         RedisModeStandalone,
		Addrs:        []string{listener.Addr().String()},
		DialTimeout:  50 * time.Millisecond,
		ReadTimeout:  2 * time.Second,
		WriteTimeout: 2 * time.Second,
		MaxRetries:   -1,
	})
	elapsed := time.Since(started)
	require.Error(t, err)
	require.ErrorIs(t, err, context.DeadlineExceeded)
	require.Less(t, elapsed, time.Second, "startup check exceeded its independent deadline")
}

// Only PING and Close are relevant to startup; unexpected client operations
// fail through the embedded nil interface instead of needing a large mock.
type startupCheckClient struct {
	redis.UniversalClient
	cause  error
	pings  int
	closed bool
}

func (c *startupCheckClient) Ping(context.Context) *redis.StatusCmd {
	c.pings++
	return redis.NewStatusResult("PONG", c.cause)
}

func (c *startupCheckClient) Close() error {
	c.closed = true
	return nil
}

func TestCheckRedisStartupValidatesBeforePing(t *testing.T) {
	var typedNil *redis.Client
	for name, client := range map[string]redis.UniversalClient{"nil": nil, "typed nil": typedNil} {
		t.Run(name, func(t *testing.T) {
			require.ErrorIs(t, CheckRedisStartup(client, time.Second), ErrInvalidConfiguration)
		})
	}
	for _, timeout := range []time.Duration{0, -time.Second} {
		client := &startupCheckClient{}
		require.ErrorIs(t, CheckRedisStartup(client, timeout), ErrInvalidConfiguration)
		require.Zero(t, client.pings)
		require.False(t, client.closed)
	}
}

func TestCheckRedisStartupPreservesCauseAndBorrowedOwnership(t *testing.T) {
	cause := errors.New("injected startup failure")
	for name, want := range map[string]error{"success": nil, "failure": cause} {
		t.Run(name, func(t *testing.T) {
			client := &startupCheckClient{cause: want}
			err := CheckRedisStartup(client, time.Second)
			require.ErrorIs(t, err, want)
			require.Equal(t, 1, client.pings)
			require.False(t, client.closed)
		})
	}
}

func TestNormalizeRedisConnectionConfigValidatesTopology(t *testing.T) {
	tests := []RedisConnectionConfig{
		{Mode: RedisModeStandalone, Addrs: []string{"one:6379", "two:6379"}},
		{Mode: RedisModeStandalone, Addrs: []string{"one:6379"}, MasterName: "mymaster"},
		{Mode: RedisModeSentinel, Addrs: []string{"sentinel:26379"}},
		{Mode: RedisModeCluster, Addrs: []string{"cluster:6379"}, DB: 1},
		{Mode: RedisModeCluster, Addrs: []string{"cluster:6379"}, MasterName: "mymaster"},
		{Mode: RedisMode("automatic"), Addrs: []string{"redis:6379"}},
	}
	for _, config := range tests {
		_, err := normalizeRedisConnectionConfig(config)
		require.ErrorIs(t, err, ErrInvalidConfiguration, "%#v", config)
	}
}

func TestNormalizeRedisConnectionConfigClonesMutableInput(t *testing.T) {
	tlsConfig := &tls.Config{ServerName: "redis.example"}
	config := RedisConnectionConfig{
		Mode: RedisModeCluster, Addrs: []string{"cluster:6379"}, TLSConfig: tlsConfig,
	}
	resolved, err := normalizeRedisConnectionConfig(config)
	require.NoError(t, err)
	require.NotSame(t, tlsConfig, resolved.TLSConfig)
	require.Equal(t, uint16(tls.VersionTLS12), resolved.TLSConfig.MinVersion)
	config.Addrs[0] = "changed:6379"
	tlsConfig.ServerName = "changed.example"
	require.Equal(t, "cluster:6379", resolved.Addrs[0])
	require.Equal(t, "redis.example", resolved.TLSConfig.ServerName)
}

func TestResolveRedisConnectionConfigSources(t *testing.T) {
	t.Run("standard URL", func(t *testing.T) {
		resolution, err := ResolveRedisConnectionConfig(nil, mapRedisLookup(map[string]string{
			"REDIS_URL":                    "redis://redis.example:6379/0",
			"TRUVAG3_REDIS_POOL_SIZE":      "24",
			"TRUVAG3_REDIS_MIN_IDLE_CONNS": "6",
			"TRUVAG3_REDIS_DIAL_TIMEOUT":   "4s",
			"TRUVAG3_REDIS_READ_TIMEOUT":   "2s",
			"TRUVAG3_REDIS_WRITE_TIMEOUT":  "2500ms",
			"TRUVAG3_REDIS_MAX_RETRIES":    "5",
		}))
		require.NoError(t, err)
		require.Equal(t, RedisModeStandalone, resolution.Config.Mode)
		require.Equal(t, []string{"redis.example:6379"}, resolution.Config.Addrs)
		require.Equal(t, 24, resolution.Config.PoolSize)
		require.Equal(t, 6, resolution.Config.MinIdle)
		require.Equal(t, 4*time.Second, resolution.Config.DialTimeout)
		require.Equal(t, 5, resolution.Config.MaxRetries)
		require.Empty(t, resolution.Diagnostics)
	})

	t.Run("deprecated URL alias", func(t *testing.T) {
		resolution, err := ResolveRedisConnectionConfig(nil, mapRedisLookup(map[string]string{
			"TRUVAG3_REDIS_URL": "redis://legacy.example:6379/8",
		}))
		require.NoError(t, err)
		require.Equal(t, 8, resolution.Config.DB)
		require.Equal(t, []string{"TRUVAG3_REDIS_URL is deprecated; use REDIS_URL"}, resolution.Diagnostics)
	})

	t.Run("structured cluster", func(t *testing.T) {
		resolution, err := ResolveRedisConnectionConfig(nil, mapRedisLookup(map[string]string{
			"TRUVAG3_REDIS_MODE":  "cluster",
			"TRUVAG3_REDIS_ADDRS": "one:6379, two:6379",
			"TRUVAG3_REDIS_DB":    "0",
		}))
		require.NoError(t, err)
		require.Equal(t, RedisModeCluster, resolution.Config.Mode)
		require.Equal(t, []string{"one:6379", "two:6379"}, resolution.Config.Addrs)
	})

	t.Run("default", func(t *testing.T) {
		resolution, err := ResolveRedisConnectionConfig(nil, mapRedisLookup(nil))
		require.NoError(t, err)
		require.Equal(t, DefaultRedisConnectionConfig(), resolution.Config)
	})
}

func TestResolveRedisConnectionConfigPreservesStructuredCredentials(t *testing.T) {
	resolution, err := ResolveRedisConnectionConfig(nil, mapRedisLookup(map[string]string{
		"TRUVAG3_REDIS_MODE":              "sentinel",
		"TRUVAG3_REDIS_ADDRS":             "sentinel.example:26379",
		"TRUVAG3_REDIS_MASTER_NAME":       "mymaster",
		"TRUVAG3_REDIS_USERNAME":          "  data user  ",
		"TRUVAG3_REDIS_PASSWORD":          "  data secret  ",
		"TRUVAG3_REDIS_SENTINEL_USERNAME": "  sentinel user  ",
		"TRUVAG3_REDIS_SENTINEL_PASSWORD": "  sentinel secret  ",
	}))
	require.NoError(t, err)
	require.Equal(t, "  data user  ", resolution.Config.Username)
	require.Equal(t, "  data secret  ", resolution.Config.Password)
	require.Equal(t, "  sentinel user  ", resolution.Config.SentinelUsername)
	require.Equal(t, "  sentinel secret  ", resolution.Config.SentinelPassword)
}

func TestResolveRedisConnectionConfigRejectsInvalidSettings(t *testing.T) {
	for _, setting := range []struct{ name, value string }{
		{"TRUVAG3_REDIS_MODE", ""},
		{"TRUVAG3_REDIS_MODE", "automatic"},
		{"TRUVAG3_REDIS_ADDRS", ""},
		{"TRUVAG3_REDIS_ADDRS", "one:6379,,two:6379"},
		{"TRUVAG3_REDIS_DB", "not-a-number"},
		{"TRUVAG3_REDIS_DB", "1"},
		{"TRUVAG3_REDIS_TLS_ENABLED", "not-a-bool"},
		{"TRUVAG3_REDIS_POOL_SIZE", "not-a-number"},
		{"TRUVAG3_REDIS_POOL_SIZE", "-1"},
		{"TRUVAG3_REDIS_POOL_SIZE", "1000000"},
		{"TRUVAG3_REDIS_MIN_IDLE_CONNS", "not-a-number"},
		{"TRUVAG3_REDIS_MIN_IDLE_CONNS", "-1"},
		{"TRUVAG3_REDIS_MIN_IDLE_CONNS", "1000000"},
		{"TRUVAG3_REDIS_MAX_RETRIES", "not-a-number"},
		{"TRUVAG3_REDIS_MAX_RETRIES", "-2"},
		{"TRUVAG3_REDIS_MAX_RETRIES", "101"},
		{"TRUVAG3_REDIS_DIAL_TIMEOUT", "not-a-duration"},
		{"TRUVAG3_REDIS_DIAL_TIMEOUT", "-1s"},
		{"TRUVAG3_REDIS_READ_TIMEOUT", "not-a-duration"},
		{"TRUVAG3_REDIS_READ_TIMEOUT", "24h"},
		{"TRUVAG3_REDIS_WRITE_TIMEOUT", "not-a-duration"},
		{"TRUVAG3_REDIS_WRITE_TIMEOUT", "-1s"},
	} {
		t.Run(setting.name+"="+setting.value, func(t *testing.T) {
			environment := map[string]string{
				"TRUVAG3_REDIS_MODE":     "cluster",
				"TRUVAG3_REDIS_ADDRS":    "cluster.example:6379",
				"TRUVAG3_REDIS_PASSWORD": "unit-test-secret",
			}
			environment[setting.name] = setting.value
			_, err := ResolveRedisConnectionConfig(nil, mapRedisLookup(environment))
			require.ErrorIs(t, err, ErrInvalidConfiguration)
			require.NotContains(t, err.Error(), "unit-test-secret")
		})
	}
}

func TestResolveRedisConnectionConfigTLS(t *testing.T) {
	environment := map[string]string{
		"TRUVAG3_REDIS_MODE":            " CLUSTER ",
		"TRUVAG3_REDIS_ADDRS":           " cluster.example:6379 ",
		"TRUVAG3_REDIS_TLS_ENABLED":     "true",
		"TRUVAG3_REDIS_TLS_SERVER_NAME": " redis.example ",
	}
	resolution, err := ResolveRedisConnectionConfig(nil, mapRedisLookup(environment))
	require.NoError(t, err)
	require.Equal(t, RedisModeCluster, resolution.Config.Mode)
	require.Equal(t, []string{"cluster.example:6379"}, resolution.Config.Addrs)
	require.NotNil(t, resolution.Config.TLSConfig)
	require.Equal(t, uint16(tls.VersionTLS12), resolution.Config.TLSConfig.MinVersion)
	require.Equal(t, "redis.example", resolution.Config.TLSConfig.ServerName)
	require.False(t, resolution.Config.TLSConfig.InsecureSkipVerify)
	require.Nil(t, resolution.Config.TLSConfig.RootCAs, "no custom CA keeps system trust")

	environment["TRUVAG3_REDIS_TLS_ENABLED"] = "false"
	_, err = ResolveRedisConnectionConfig(nil, mapRedisLookup(environment))
	require.ErrorIs(t, err, ErrInvalidConfiguration, "TLS settings must not silently disable verification")
	delete(environment, "TRUVAG3_REDIS_TLS_SERVER_NAME")
	resolution, err = ResolveRedisConnectionConfig(nil, mapRedisLookup(environment))
	require.NoError(t, err)
	require.Nil(t, resolution.Config.TLSConfig)
}

func TestResolveRedisConnectionConfigRejectsUnreadableOrInvalidCA(t *testing.T) {
	invalidCA := filepath.Join(t.TempDir(), "invalid.pem")
	require.NoError(t, os.WriteFile(invalidCA, []byte("not a certificate"), 0o600))
	for name, caFile := range map[string]string{
		"missing": filepath.Join(t.TempDir(), "missing.pem"),
		"invalid": invalidCA,
	} {
		t.Run(name, func(t *testing.T) {
			_, err := ResolveRedisConnectionConfig(nil, mapRedisLookup(map[string]string{
				"TRUVAG3_REDIS_MODE":        "cluster",
				"TRUVAG3_REDIS_ADDRS":       "cluster.example:6379",
				"TRUVAG3_REDIS_TLS_ENABLED": "true",
				"TRUVAG3_REDIS_CA_FILE":     caFile,
			}))
			require.ErrorIs(t, err, ErrInvalidConfiguration)
			require.NotContains(t, err.Error(), caFile, "configuration errors must not expose operator paths")
		})
	}
}

func TestResolveRedisConnectionConfigPreservesExplicitZeroMinIdle(t *testing.T) {
	resolution, err := ResolveRedisConnectionConfig(nil, mapRedisLookup(map[string]string{
		"TRUVAG3_REDIS_MODE":           "cluster",
		"TRUVAG3_REDIS_ADDRS":          "cluster.example:6379",
		"TRUVAG3_REDIS_MIN_IDLE_CONNS": "0",
	}))
	require.NoError(t, err)
	require.Zero(t, resolution.Config.MinIdle)

	standardURL, err := ParseStandaloneRedisURL("redis://redis.example:6379")
	require.NoError(t, err)
	require.Equal(t, defaultRedisMinIdle, standardURL.MinIdle)
	zeroURL, err := ParseStandaloneRedisURL("redis://redis.example:6379?min_idle_conns=0")
	require.NoError(t, err)
	require.Zero(t, zeroURL.MinIdle)
}

func TestResolveRedisConnectionConfigRejectsMixedSourcesBeforeValues(t *testing.T) {
	const secretURL = "redis://operator:super-secret@host.example:6379/8"
	_, err := ResolveRedisConnectionConfig(nil, mapRedisLookup(map[string]string{
		"REDIS_URL":           secretURL,
		"TRUVAG3_REDIS_MODE":  "cluster",
		"TRUVAG3_REDIS_ADDRS": "cluster.example:6379",
	}))
	require.ErrorIs(t, err, ErrInvalidConfiguration)
	require.Contains(t, err.Error(), "cannot be combined")
	require.NotContains(t, err.Error(), "super-secret")
	require.NotContains(t, err.Error(), secretURL)
}

func TestParseStandaloneRedisURLRequiresDBZeroAndProtectsCredentials(t *testing.T) {
	_, err := ParseStandaloneRedisURL("redis://redis.example:6379/8")
	require.ErrorIs(t, err, ErrInvalidConfiguration)

	const malformed = "redis://operator:super-secret@%zz"
	_, err = ParseStandaloneRedisURL(malformed)
	require.ErrorIs(t, err, ErrInvalidConfiguration)
	require.NotContains(t, err.Error(), malformed)
	require.NotContains(t, err.Error(), "super-secret")
}

func TestResolveRedisConnectionConfigUsesExplicitConfigWithoutEnvironment(t *testing.T) {
	explicit := DefaultRedisConnectionConfig()
	explicit.Addrs = []string{"code.example:6379"}
	resolution, err := ResolveRedisConnectionConfig(&explicit, func(string) (string, bool) {
		panic("environment must not be read for explicit connection configuration")
	})
	require.NoError(t, err)
	require.Equal(t, "code.example:6379", resolution.Config.Addrs[0])
}

func TestRedisConnectionErrorsRetainExpectedIdentity(t *testing.T) {
	_, err := NewRedisUniversalClient(RedisConnectionConfig{
		Mode: RedisModeCluster, Addrs: []string{"cluster.example:6379"}, DB: 1,
	})
	require.True(t, errors.Is(err, ErrInvalidConfiguration))
}

func TestRedisStartupErrorIsBoundedAndPreservesCause(t *testing.T) {
	server := miniredis.RunT(t)
	server.SetError("ERR authentication failed password=top-secret")
	_, err := NewRedisUniversalClient(RedisConnectionConfig{
		Mode: RedisModeStandalone, Addrs: []string{server.Addr()},
	})
	require.Error(t, err)
	require.NotContains(t, err.Error(), "top-secret")
	require.NotContains(t, err.Error(), "password")
	var redisError redis.Error
	require.ErrorAs(t, err, &redisError)
	var startupError *RedisStartupError
	require.ErrorAs(t, err, &startupError)
}

func mapRedisLookup(values map[string]string) LookupEnv {
	return func(name string) (string, bool) {
		value, present := values[name]
		return value, present
	}
}

func TestStructuredRedisConnectionRejectsCredentialsInAddresses(t *testing.T) {
	_, err := ResolveRedisConnectionConfig(nil, mapRedisLookup(map[string]string{
		"TRUVAG3_REDIS_MODE":  "cluster",
		"TRUVAG3_REDIS_ADDRS": "operator:secret@cluster.example:6379",
	}))
	require.ErrorIs(t, err, ErrInvalidConfiguration)
	require.False(t, strings.Contains(err.Error(), "secret"))
}

func TestRedisConnectionConfigFormattingDoesNotExposeCredentials(t *testing.T) {
	config := RedisConnectionConfig{
		Mode:             RedisModeSentinel,
		Addrs:            []string{"sentinel.example:26379"},
		MasterName:       "mymaster",
		Username:         "operator",
		Password:         "data-secret",
		SentinelUsername: "sentinel-operator",
		SentinelPassword: "sentinel-secret",
	}
	for _, formatted := range []string{fmt.Sprintf("%v", config), fmt.Sprintf("%+v", config), fmt.Sprintf("%#v", config)} {
		require.NotContains(t, formatted, "data-secret")
		require.NotContains(t, formatted, "sentinel-secret")
		require.NotContains(t, formatted, "operator")
	}
}

func TestLogRedisConnectionDiagnosticsEmitsEachDiagnosticOnce(t *testing.T) {
	logger := &MockLogger{}
	logRedisConnectionDiagnostics(logger, []string{"deprecated alias", "deprecated alias", ""})
	require.Equal(t, []LogEntry{{
		Level:   "warn",
		Message: "Redis configuration notice",
		Fields: map[string]interface{}{
			"operation":  "redis_configuration_notice",
			"diagnostic": "deprecated alias",
		},
	}}, logger.entries)
}

type redisClientComponentLogger struct {
	NoOpLogger
	component string
}

func (logger *redisClientComponentLogger) WithComponent(component string) Logger {
	logger.component = component
	return logger
}

func TestRedisClientScopesComponentAwareLogger(t *testing.T) {
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	logger := &redisClientComponentLogger{}

	namespaced, err := NewRedisClientWithClient(client, "component-test", logger)
	require.NoError(t, err)
	require.Same(t, logger, namespaced.logger)
	require.Equal(t, "framework/core", logger.component)
}

func TestRedisClientHealthCheckLogsBoundedRequestCorrelatedFields(t *testing.T) {
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	logger := &MockLogger{}
	namespaced, err := NewRedisClientWithClient(client, "health-test", logger)
	require.NoError(t, err)
	ctx := WithRequestID(t.Context(), "health-request")

	require.NoError(t, namespaced.HealthCheck(ctx))
	require.NoError(t, client.Close())
	require.Error(t, namespaced.HealthCheck(ctx))

	require.NotEmpty(t, logger.entries)
	fields := logger.entries[len(logger.entries)-1].Fields
	require.Equal(t, "redis_health_check", fields["operation"])
	require.Equal(t, "health-request", fields["request_id"])
	require.Equal(t, "redis health check failed", fields["error"])
	require.Equal(t, "backend", fields["error_type"])
	require.NotContains(t, fmt.Sprint(fields), server.Addr())
}

func TestRegistryRetryConfigurationWarningIsBoundedAndCorrelated(t *testing.T) {
	logger := &MockLogger{}
	ctx := WithRequestID(t.Context(), "retry-request")
	StartRegistryRetry(
		ctx,
		"redis://user:secret@host.invalid:6379/8",
		&ServiceInfo{ID: "service-1"},
		time.Second,
		logger,
		nil,
		time.Minute,
		time.Second,
	)
	require.Len(t, logger.entries, 1)
	fields := logger.entries[0].Fields
	require.Equal(t, "redis_registry_retry_configuration", fields["operation"])
	require.Equal(t, "retry-request", fields["request_id"])
	require.Equal(t, "rejected", fields["status"])
	require.Equal(t, "invalid Redis retry configuration", fields["error"])
	require.Equal(t, "invalid_configuration", fields["error_type"])
	require.NotContains(t, fmt.Sprint(fields), "secret")
}

func TestConfigLoadsStructuredRedisConnection(t *testing.T) {
	clearRedisConnectionEnvironment(t)
	t.Setenv("TRUVAG3_REDIS_MODE", "cluster")
	t.Setenv("TRUVAG3_REDIS_ADDRS", "one.example:6379,two.example:6379")

	config, err := NewConfig(WithDiscovery(true, "redis"))
	require.NoError(t, err)
	require.Empty(t, config.Discovery.RedisURL)
	require.NotNil(t, config.Discovery.RedisConnection)
	require.Equal(t, RedisModeCluster, config.Discovery.RedisConnection.Mode)
	require.Equal(t, []string{"one.example:6379", "two.example:6379"}, config.Discovery.RedisConnection.Addrs)
}

func TestConfigRejectsMixedRedisConnectionSources(t *testing.T) {
	clearRedisConnectionEnvironment(t)
	t.Setenv("REDIS_URL", "redis://standalone.example:6379")
	t.Setenv("TRUVAG3_REDIS_MODE", "cluster")
	t.Setenv("TRUVAG3_REDIS_ADDRS", "cluster.example:6379")

	_, err := NewConfig(WithDiscovery(true, "redis"))
	require.ErrorIs(t, err, ErrInvalidConfiguration)
}

func TestExplicitRedisConnectionOverridesResolvedEnvironment(t *testing.T) {
	clearRedisConnectionEnvironment(t)
	t.Setenv("REDIS_URL", "redis://standalone.example:6379")
	t.Setenv("TRUVAG3_REDIS_MODE", "cluster")
	t.Setenv("TRUVAG3_REDIS_ADDRS", "environment.example:6379")

	config, err := NewConfig(WithRedisConnection(RedisConnectionConfig{
		Mode:       RedisModeSentinel,
		Addrs:      []string{"sentinel.example:26379"},
		MasterName: "primary",
	}))
	require.NoError(t, err)
	require.Empty(t, config.Discovery.RedisURL)
	require.Equal(t, RedisModeSentinel, config.Discovery.RedisConnection.Mode)
	require.Equal(t, []string{"sentinel.example:26379"}, config.Discovery.RedisConnection.Addrs)
}

func clearRedisConnectionEnvironment(t *testing.T) {
	t.Helper()
	names := append([]string{
		"REDIS_URL",
		"TRUVAG3_REDIS_URL",
		"TRUVAG3_REDIS_POOL_SIZE",
		"TRUVAG3_REDIS_MIN_IDLE_CONNS",
		"TRUVAG3_REDIS_DIAL_TIMEOUT",
		"TRUVAG3_REDIS_READ_TIMEOUT",
		"TRUVAG3_REDIS_WRITE_TIMEOUT",
		"TRUVAG3_REDIS_MAX_RETRIES",
	}, structuredRedisConnectionVariables...)
	for _, name := range names {
		t.Setenv(name, "")
	}
}
