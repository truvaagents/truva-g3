package core

import (
	"context"
	"crypto/tls"
	"net"
	"reflect"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
	"github.com/redis/go-redis/v9/auth"
	"github.com/redis/go-redis/v9/maintnotifications"
	"github.com/redis/go-redis/v9/push"
	"github.com/stretchr/testify/require"
)

type redisOptionsTestStreamingCredentials struct{}

func (*redisOptionsTestStreamingCredentials) Subscribe(auth.CredentialsListener) (auth.Credentials, auth.UnsubscribeFunc, error) {
	return auth.NewBasicCredentials("stream-user", "stream-secret"), func() error { return nil }, nil
}

type redisOptionsTestLimiter struct{}

func (*redisOptionsTestLimiter) Allow() error       { return nil }
func (*redisOptionsTestLimiter) ReportResult(error) {}

func TestApplyRedisClientDefaults(t *testing.T) {
	options := ApplyRedisClientDefaults(nil)

	require.Equal(t, DefaultRedisProtocol, options.Protocol)
	require.Equal(t, 5*time.Second, options.DialTimeout)
	require.Equal(t, 3*time.Second, options.ReadTimeout)
	require.Equal(t, 3*time.Second, options.WriteTimeout)
	require.Equal(t, 8*time.Millisecond, options.MinRetryBackoff)
	require.Equal(t, 512*time.Millisecond, options.MaxRetryBackoff)
	require.Equal(t, 5*time.Minute, options.ConnMaxIdleTime)
	require.Zero(t, options.ReadBufferSize)
	require.Zero(t, options.WriteBufferSize)
	require.Equal(t, 1, options.DialerRetries)
	require.Nil(t, options.Dialer)
}

func TestApplyRedisClientDefaultsPreservesExplicitConfiguration(t *testing.T) {
	dialer := func(context.Context, string, string) (net.Conn, error) { return nil, nil }
	options := &redis.Options{
		Protocol:           3,
		DialTimeout:        time.Second,
		ReadTimeout:        -1,
		WriteTimeout:       2 * time.Second,
		MinRetryBackoff:    -1,
		MaxRetryBackoff:    2 * time.Second,
		ConnMaxIdleTime:    -1,
		ReadBufferSize:     8192,
		WriteBufferSize:    16384,
		DialerRetries:      4,
		DialerRetryTimeout: 50 * time.Millisecond,
		Dialer:             dialer,
	}

	result := ApplyRedisClientDefaults(options)

	require.NotSame(t, options, result)
	require.Equal(t, 3, result.Protocol)
	require.Equal(t, time.Second, result.DialTimeout)
	require.Equal(t, time.Duration(-1), result.ReadTimeout)
	require.Equal(t, 2*time.Second, result.WriteTimeout)
	require.Equal(t, time.Duration(-1), result.MinRetryBackoff)
	require.Equal(t, 2*time.Second, result.MaxRetryBackoff)
	require.Equal(t, time.Duration(-1), result.ConnMaxIdleTime)
	require.Equal(t, 8192, result.ReadBufferSize)
	require.Equal(t, 16384, result.WriteBufferSize)
	require.Equal(t, 4, result.DialerRetries)
	require.Equal(t, 50*time.Millisecond, result.DialerRetryTimeout)
	require.NotNil(t, result.Dialer)
}

func TestApplyRedisClientDefaultsDoesNotMutateCaller(t *testing.T) {
	input := &redis.Options{Addr: "redis:6379"}

	result := ApplyRedisClientDefaults(input)

	require.NotSame(t, input, result)
	require.Equal(t, "redis:6379", result.Addr)
	require.Zero(t, input.Protocol)
	require.Zero(t, input.DialTimeout)
	require.Zero(t, input.ReadTimeout)
	require.Zero(t, input.WriteTimeout)
	require.Zero(t, input.DialerRetries)
	require.Nil(t, input.Dialer)
}

func TestApplyRedisClientDefaultsDelegatesTransportDefaultsToGoRedis(t *testing.T) {
	client := redis.NewClient(ApplyRedisClientDefaults(nil))
	t.Cleanup(func() {
		require.NoError(t, client.Close())
	})

	effective := client.Options()
	require.Equal(t, DefaultRedisProtocol, effective.Protocol)
	require.Equal(t, 32*1024, effective.ReadBufferSize)
	require.Equal(t, 32*1024, effective.WriteBufferSize)
	require.NotNil(t, effective.Dialer)
}

func TestApplyRedisClientDefaultsMakesWriteTimeoutFollowExplicitReadTimeout(t *testing.T) {
	options := ApplyRedisClientDefaults(&redis.Options{ReadTimeout: 7 * time.Second})

	require.Equal(t, 7*time.Second, options.ReadTimeout)
	require.Equal(t, 7*time.Second, options.WriteTimeout)
}

func TestApplyRedisUniversalDefaultsClonesMutableConfiguration(t *testing.T) {
	tlsConfig := &tls.Config{ServerName: "redis.example"}
	input := &redis.UniversalOptions{
		Addrs:     []string{"one:6379", "two:6379"},
		TLSConfig: tlsConfig,
	}

	result := ApplyRedisUniversalDefaults(input)

	require.NotSame(t, input, result)
	require.NotSame(t, tlsConfig, result.TLSConfig)
	input.Addrs[0] = "changed:6379"
	tlsConfig.ServerName = "changed.example"
	require.Equal(t, "one:6379", result.Addrs[0])
	require.Equal(t, "redis.example", result.TLSConfig.ServerName)
	require.Equal(t, DefaultRedisProtocol, result.Protocol)
	require.Equal(t, defaultRedisReadTimeout, result.ReadTimeout)
}

func TestApplyRedisClientDefaultsMatchesUniversalCommonFields(t *testing.T) {
	dialer := func(context.Context, string, string) (net.Conn, error) { return nil, nil }
	onConnect := func(context.Context, *redis.Conn) error { return nil }
	credentialsProvider := func() (string, string) { return "provider-user", "provider-secret" }
	credentialsProviderContext := func(context.Context) (string, string, error) {
		return "context-user", "context-secret", nil
	}
	dialerRetryBackoff := func(int) time.Duration { return 17 * time.Millisecond }
	streamingCredentials := &redisOptionsTestStreamingCredentials{}
	autoPipeline := &redis.AutoPipelineOptions{MaxBatchSize: 19}
	maintenance := &maintnotifications.Config{Mode: maintnotifications.ModeEnabled}
	processor := push.NewProcessor()
	cacheConfig := &redis.ClientSideCacheConfig{MaxEntries: 23}
	cache := redis.NewLocalCache(redis.CacheConfig{MaxEntries: 29})
	limiter := &redisOptionsTestLimiter{}
	input := &redis.Options{
		Network:                      "tcp4",
		Addr:                         "redis.example:6379",
		NodeAddress:                  "node.example:6379",
		ClientName:                   "truvag3-test",
		Dialer:                       dialer,
		OnConnect:                    onConnect,
		Protocol:                     3,
		Username:                     "operator",
		Password:                     "secret",
		CredentialsProvider:          credentialsProvider,
		CredentialsProviderContext:   credentialsProviderContext,
		StreamingCredentialsProvider: streamingCredentials,
		DB:                           4,
		MaxRetries:                   7,
		MinRetryBackoff:              11 * time.Millisecond,
		MaxRetryBackoff:              13 * time.Millisecond,
		DialTimeout:                  2 * time.Second,
		DialerRetries:                5,
		DialerRetryTimeout:           31 * time.Millisecond,
		DialerRetryBackoff:           dialerRetryBackoff,
		ReadTimeout:                  4 * time.Second,
		WriteTimeout:                 5 * time.Second,
		ContextTimeoutEnabled:        true,
		ReadBufferSize:               4096,
		WriteBufferSize:              8192,
		PipelineReadBufferSize:       16384,
		PipelineWriteBufferSize:      32768,
		PipelinePoolSize:             3,
		AutoPipelineOptions:          autoPipeline,
		PoolFIFO:                     true,
		PoolSize:                     21,
		MaxConcurrentDials:           8,
		PoolTimeout:                  6 * time.Second,
		MinIdleConns:                 3,
		MaxIdleConns:                 9,
		MaxActiveConns:               34,
		ConnMaxIdleTime:              time.Minute,
		ConnMaxLifetime:              2 * time.Minute,
		ConnMaxLifetimeJitter:        3 * time.Second,
		TLSConfig:                    &tls.Config{MinVersion: tls.VersionTLS13, ServerName: "redis.example"},
		Limiter:                      limiter,
		DisableIndentity:             true, //nolint:staticcheck // compatibility parity is intentional
		DisableIdentity:              true,
		IdentitySuffix:               "-parity",
		UnstableResp3:                true, //nolint:staticcheck // compatibility parity is intentional
		PushNotificationProcessor:    processor,
		FailingTimeoutSeconds:        37,
		MaintNotificationsConfig:     maintenance,
		ClientSideCacheConfig:        cacheConfig,
		ClientSideCache:              cache,
		ClientSideCacheStrategy:      redis.CSCStrategy(41),
	}

	wrapper := ApplyRedisClientDefaults(input)
	universal := ApplyRedisUniversalDefaults(universalOptionsFromSimple(input)).Simple()

	fields := []string{
		"Network", "Addr", "NodeAddress", "ClientName", "Dialer", "OnConnect",
		"Protocol", "Username", "Password", "CredentialsProvider",
		"CredentialsProviderContext", "StreamingCredentialsProvider", "DB",
		"MaxRetries", "MinRetryBackoff", "MaxRetryBackoff", "DialTimeout",
		"DialerRetries", "DialerRetryTimeout", "DialerRetryBackoff", "ReadTimeout",
		"WriteTimeout", "ContextTimeoutEnabled", "ReadBufferSize", "WriteBufferSize",
		"PipelineReadBufferSize", "PipelineWriteBufferSize", "PipelinePoolSize",
		"AutoPipelineOptions", "PoolFIFO", "PoolSize", "MaxConcurrentDials",
		"PoolTimeout", "MinIdleConns", "MaxIdleConns", "MaxActiveConns",
		"ConnMaxIdleTime", "ConnMaxLifetime", "ConnMaxLifetimeJitter", "TLSConfig",
		"Limiter", "DisableIndentity", "DisableIdentity", "IdentitySuffix",
		"UnstableResp3", "PushNotificationProcessor", "FailingTimeoutSeconds",
		"MaintNotificationsConfig", "ClientSideCacheConfig", "ClientSideCache",
		"ClientSideCacheStrategy",
	}
	covered := make(map[string]struct{}, len(fields))
	for _, field := range fields {
		covered[field] = struct{}{}
		requireRedisOptionFieldPreserved(t, field, input, wrapper)
	}

	// Force an explicit review whenever the pinned provider adds an option.
	optionType := reflect.TypeOf(redis.Options{})
	for index := 0; index < optionType.NumField(); index++ {
		field := optionType.Field(index)
		if field.IsExported() {
			require.Contains(t, covered, field.Name, "redis.Options field %q lacks parity coverage", field.Name)
		}
	}

	commonFields := []string{
		"Addr", "ClientName", "DB", "Protocol", "Username", "Password",
		"MaxRetries", "MinRetryBackoff", "MaxRetryBackoff", "DialTimeout",
		"DialerRetries", "DialerRetryTimeout", "ReadTimeout", "WriteTimeout",
		"ContextTimeoutEnabled", "ReadBufferSize", "WriteBufferSize", "PoolFIFO",
		"PoolSize", "MaxConcurrentDials", "PoolTimeout", "MinIdleConns",
		"MaxIdleConns", "MaxActiveConns", "ConnMaxIdleTime", "ConnMaxLifetime",
		"ConnMaxLifetimeJitter", "TLSConfig", "DisableIdentity", "IdentitySuffix",
		"AutoPipelineOptions", "PushNotificationProcessor", "MaintNotificationsConfig",
		"ClientSideCacheConfig", "ClientSideCache", "ClientSideCacheStrategy",
	}
	for _, field := range commonFields {
		requireRedisOptionFieldsEqual(t, field, universal, wrapper)
	}
}

func requireRedisOptionFieldPreserved(t *testing.T, field string, input, output *redis.Options) {
	t.Helper()
	want := reflect.ValueOf(input).Elem().FieldByName(field)
	got := reflect.ValueOf(output).Elem().FieldByName(field)
	if want.Kind() == reflect.Func {
		require.Equal(t, want.Pointer(), got.Pointer(), "redis.Options.%s", field)
		return
	}
	require.True(t, reflect.DeepEqual(want.Interface(), got.Interface()), "redis.Options.%s was not preserved", field)
}

func requireRedisOptionFieldsEqual(t *testing.T, field string, first, second *redis.Options) {
	t.Helper()
	left := reflect.ValueOf(first).Elem().FieldByName(field)
	right := reflect.ValueOf(second).Elem().FieldByName(field)
	if left.Kind() == reflect.Func {
		require.Equal(t, left.Pointer(), right.Pointer(), "redis.Options.%s", field)
		return
	}
	require.True(t, reflect.DeepEqual(left.Interface(), right.Interface()), "redis.Options.%s parity mismatch", field)
}
