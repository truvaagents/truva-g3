package core

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"
)

const (
	defaultRedisPoolSize   = 10
	defaultRedisMinIdle    = 5
	defaultRedisMaxRetries = 3
	maxRedisPoolSize       = 10_000
	maxRedisTimeout        = 10 * time.Minute
)

// RedisMode identifies the topology used by a Redis or Valkey deployment.
type RedisMode string

const (
	RedisModeStandalone RedisMode = "standalone"
	RedisModeSentinel   RedisMode = "sentinel"
	RedisModeCluster    RedisMode = "cluster"
)

// LookupEnv resolves one environment variable without coupling configuration
// parsing to process-global state.
type LookupEnv func(string) (string, bool)

// RedisConnectionConfig describes one explicit Redis or Valkey topology.
// PoolSize and MinIdle are per node when Mode is RedisModeCluster.
type RedisConnectionConfig struct {
	Mode             RedisMode
	Addrs            []string
	MasterName       string
	Username         string
	Password         string
	SentinelUsername string
	SentinelPassword string
	DB               int
	TLSConfig        *tls.Config
	PoolSize         int
	MinIdle          int
	DialTimeout      time.Duration
	ReadTimeout      time.Duration
	WriteTimeout     time.Duration
	MaxRetries       int
}

// String returns a credential-free connection summary.
func (config RedisConnectionConfig) String() string {
	return fmt.Sprintf(
		"RedisConnectionConfig{Mode:%s Addrs:%d DB:%d TLS:%t PoolSize:%d MinIdle:%d}",
		config.Mode,
		len(config.Addrs),
		config.DB,
		config.TLSConfig != nil,
		config.PoolSize,
		config.MinIdle,
	)
}

// GoString keeps %#v diagnostics credential-free as well.
func (config RedisConnectionConfig) GoString() string { return config.String() }

// RedisStartupError preserves the underlying client error for errors.Is/As
// while keeping its observable message bounded and credential-free.
type RedisStartupError struct {
	Mode  RedisMode
	cause error
}

func (err *RedisStartupError) Error() string {
	return fmt.Sprintf("redis %s startup check failed", err.Mode)
}

func (err *RedisStartupError) Unwrap() error { return err.cause }

var structuredRedisConnectionVariables = []string{
	"TRUVAG3_REDIS_MODE",
	"TRUVAG3_REDIS_ADDRS",
	"TRUVAG3_REDIS_MASTER_NAME",
	"TRUVAG3_REDIS_USERNAME",
	"TRUVAG3_REDIS_PASSWORD",
	"TRUVAG3_REDIS_SENTINEL_USERNAME",
	"TRUVAG3_REDIS_SENTINEL_PASSWORD",
	"TRUVAG3_REDIS_TLS_ENABLED",
	"TRUVAG3_REDIS_TLS_SERVER_NAME",
	"TRUVAG3_REDIS_CA_FILE",
	"TRUVAG3_REDIS_DB",
}

// DefaultRedisConnectionConfig returns the local DB-0 standalone profile.
func DefaultRedisConnectionConfig() RedisConnectionConfig {
	return RedisConnectionConfig{
		Mode:         RedisModeStandalone,
		Addrs:        []string{"localhost:6379"},
		DB:           0,
		PoolSize:     defaultRedisPoolSize,
		MinIdle:      defaultRedisMinIdle,
		DialTimeout:  5 * time.Second,
		ReadTimeout:  defaultRedisReadTimeout,
		WriteTimeout: defaultRedisReadTimeout,
		MaxRetries:   defaultRedisMaxRetries,
	}
}

// NewRedisUniversalClient constructs and verifies a topology-aware Redis or
// Valkey client. The returned client is owned by the caller.
func NewRedisUniversalClient(config RedisConnectionConfig) (redis.UniversalClient, error) {
	return newRedisUniversalClient(config, true)
}

func newRedisUniversalClient(config RedisConnectionConfig, verify bool) (redis.UniversalClient, error) {
	normalized, err := normalizeRedisConnectionConfig(config)
	if err != nil {
		return nil, err
	}
	options := ApplyRedisUniversalDefaults(&redis.UniversalOptions{
		Addrs:            append([]string(nil), normalized.Addrs...),
		MasterName:       normalized.MasterName,
		Username:         normalized.Username,
		Password:         normalized.Password,
		SentinelUsername: normalized.SentinelUsername,
		SentinelPassword: normalized.SentinelPassword,
		DB:               normalized.DB,
		TLSConfig:        normalized.TLSConfig,
		PoolSize:         normalized.PoolSize,
		MinIdleConns:     normalized.MinIdle,
		DialTimeout:      normalized.DialTimeout,
		ReadTimeout:      normalized.ReadTimeout,
		WriteTimeout:     normalized.WriteTimeout,
		MaxRetries:       normalized.MaxRetries,
		IsClusterMode:    normalized.Mode == RedisModeCluster,
	})
	if !verify {
		// Type-only unit tests must not start background connection warm-up.
		options.MinIdleConns = 0
	}

	var client redis.UniversalClient
	switch normalized.Mode {
	case RedisModeStandalone:
		client = redis.NewClient(options.Simple())
	case RedisModeSentinel:
		client = redis.NewFailoverClient(options.Failover())
	case RedisModeCluster:
		client = redis.NewClusterClient(options.Cluster())
	default:
		return nil, fmt.Errorf("unsupported Redis mode: %w", ErrInvalidConfiguration)
	}
	if !verify {
		return client, nil
	}

	if err := CheckRedisStartup(client, normalized.DialTimeout); err != nil {
		_ = client.Close()
		return nil, &RedisStartupError{Mode: normalized.Mode, cause: err}
	}
	return client, nil
}

// CheckRedisStartup bounds a single startup PING without taking ownership of
// client. It never changes client options or closes the application-owned pool.
// If a client ignores context cancellation, its outstanding command remains
// subject to its socket deadline or the owner's Close. Callers must close clients
// they own when startup fails, including clients configured without I/O deadlines.
func CheckRedisStartup(client redis.UniversalClient, timeout time.Duration) error {
	if nilRedisUniversalClient(client) || timeout <= 0 {
		return fmt.Errorf("redis startup check requires a client and positive timeout: %w", ErrInvalidConfiguration)
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	// go-redis intentionally ignores command-context deadlines unless
	// ContextTimeoutEnabled is set. Framework-owned clients retain the existing
	// runtime setting, so enforce the startup-only deadline independently. A
	// buffered result lets the command finish even after this check has returned.
	completed := make(chan error, 1)
	go func() {
		completed <- client.Ping(ctx).Err()
	}()
	select {
	case err := <-completed:
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}

// ParseStandaloneRedisURL parses the standard DB-0 standalone URL shorthand.
func ParseStandaloneRedisURL(rawURL string) (RedisConnectionConfig, error) {
	options, err := redis.ParseURL(strings.TrimSpace(rawURL))
	if err != nil {
		return RedisConnectionConfig{}, fmt.Errorf("invalid Redis URL: %w", ErrInvalidConfiguration)
	}
	if options.DB != 0 {
		return RedisConnectionConfig{}, fmt.Errorf("REDIS_URL must select DB 0: %w", ErrInvalidConfiguration)
	}
	return normalizeRedisConnectionConfig(RedisConnectionConfig{
		Mode:         RedisModeStandalone,
		Addrs:        []string{options.Addr},
		Username:     options.Username,
		Password:     options.Password,
		DB:           options.DB,
		TLSConfig:    options.TLSConfig,
		PoolSize:     options.PoolSize,
		MinIdle:      redisURLMinIdle(rawURL, options.MinIdleConns),
		DialTimeout:  options.DialTimeout,
		ReadTimeout:  options.ReadTimeout,
		WriteTimeout: options.WriteTimeout,
		MaxRetries:   options.MaxRetries,
	})
}

func redisURLMinIdle(rawURL string, parsed int) int {
	parsedURL, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil {
		return defaultRedisMinIdle
	}
	values, present := parsedURL.Query()["min_idle_conns"]
	if !present || len(values) == 0 || strings.TrimSpace(values[len(values)-1]) == "" {
		return defaultRedisMinIdle
	}
	return parsed
}

// ResolveRedisConnectionConfig selects exactly one connection source. Explicit
// Go configuration is never merged with environment connection settings.
func ResolveRedisConnectionConfig(explicit *RedisConnectionConfig, lookup LookupEnv) (RedisConnectionConfig, error) {
	if explicit != nil {
		return normalizeRedisConnectionConfig(*explicit)
	}
	if lookup == nil {
		return RedisConnectionConfig{}, fmt.Errorf("redis environment lookup is required: %w", ErrInvalidConfiguration)
	}
	if _, present := nonEmptyEnvironment(lookup, "TRUVAG3_REDIS_URL"); present {
		return RedisConnectionConfig{}, fmt.Errorf("TRUVAG3_REDIS_URL is unsupported; use REDIS_URL: %w", ErrInvalidConfiguration)
	}

	rawURL, hasURL := nonEmptyEnvironment(lookup, "REDIS_URL")
	hasStructured := anyEnvironmentVariablePresent(lookup, structuredRedisConnectionVariables)
	if hasURL && hasStructured {
		return RedisConnectionConfig{}, fmt.Errorf("REDIS_URL and structured Redis topology configuration cannot be combined: %w", ErrInvalidConfiguration)
	}

	var config RedisConnectionConfig
	var err error
	switch {
	case hasURL:
		config, err = ParseStandaloneRedisURL(rawURL)
	case hasStructured:
		config, err = loadStructuredRedisConnectionConfig(lookup)
	default:
		config = DefaultRedisConnectionConfig()
	}
	if err != nil {
		return RedisConnectionConfig{}, err
	}
	return loadRedisOperationalConfig(config, lookup)
}

func normalizeRedisConnectionConfig(config RedisConnectionConfig) (RedisConnectionConfig, error) {
	if config.Mode == "" {
		config.Mode = RedisModeStandalone
	}
	config.MasterName = strings.TrimSpace(config.MasterName)
	config.Addrs = append([]string(nil), config.Addrs...)
	for index, address := range config.Addrs {
		address = strings.TrimSpace(address)
		if address == "" || strings.Contains(address, "://") || strings.Contains(address, "@") {
			return RedisConnectionConfig{}, fmt.Errorf("redis address %d is invalid: %w", index, ErrInvalidConfiguration)
		}
		config.Addrs[index] = address
	}

	switch config.Mode {
	case RedisModeStandalone:
		if len(config.Addrs) != 1 || config.MasterName != "" {
			return RedisConnectionConfig{}, fmt.Errorf("standalone Redis requires exactly one address and no Sentinel master: %w", ErrInvalidConfiguration)
		}
	case RedisModeSentinel:
		if len(config.Addrs) == 0 || config.MasterName == "" {
			return RedisConnectionConfig{}, fmt.Errorf("sentinel Redis requires at least one address and a master name: %w", ErrInvalidConfiguration)
		}
	case RedisModeCluster:
		if len(config.Addrs) == 0 || config.MasterName != "" || config.DB != 0 {
			return RedisConnectionConfig{}, fmt.Errorf("cluster Redis requires at least one seed address, DB 0, and no Sentinel master: %w", ErrInvalidConfiguration)
		}
	default:
		return RedisConnectionConfig{}, fmt.Errorf("unsupported Redis mode: %w", ErrInvalidConfiguration)
	}
	if config.DB != 0 {
		return RedisConnectionConfig{}, fmt.Errorf("redis connections require DB 0: %w", ErrInvalidConfiguration)
	}

	if config.PoolSize == 0 {
		config.PoolSize = defaultRedisPoolSize
	}
	if config.DialTimeout == 0 {
		config.DialTimeout = 5 * time.Second
	}
	if config.ReadTimeout == 0 {
		config.ReadTimeout = defaultRedisReadTimeout
	}
	if config.WriteTimeout == 0 {
		config.WriteTimeout = config.ReadTimeout
	}
	if config.MaxRetries == 0 {
		config.MaxRetries = defaultRedisMaxRetries
	}
	if config.PoolSize < 1 || config.PoolSize > maxRedisPoolSize {
		return RedisConnectionConfig{}, fmt.Errorf("redis pool size is outside the supported range: %w", ErrInvalidConfiguration)
	}
	if config.MinIdle < 0 || config.MinIdle > config.PoolSize {
		return RedisConnectionConfig{}, fmt.Errorf("redis minimum idle connections must not exceed pool size: %w", ErrInvalidConfiguration)
	}
	if !validRedisTimeout(config.DialTimeout, false) || !validRedisTimeout(config.ReadTimeout, true) || !validRedisTimeout(config.WriteTimeout, true) {
		return RedisConnectionConfig{}, fmt.Errorf("redis timeout is outside the supported range: %w", ErrInvalidConfiguration)
	}
	if config.MaxRetries < -1 || config.MaxRetries > 100 {
		return RedisConnectionConfig{}, fmt.Errorf("redis retry count is outside the supported range: %w", ErrInvalidConfiguration)
	}
	if config.TLSConfig != nil {
		config.TLSConfig = config.TLSConfig.Clone()
		if config.TLSConfig.MinVersion == 0 || config.TLSConfig.MinVersion < tls.VersionTLS12 {
			config.TLSConfig.MinVersion = tls.VersionTLS12
		}
	}
	return config, nil
}

func validRedisTimeout(value time.Duration, allowDisabled bool) bool {
	if allowDisabled && (value == -1 || value == -2) {
		return true
	}
	return value > 0 && value <= maxRedisTimeout
}

func loadStructuredRedisConnectionConfig(lookup LookupEnv) (RedisConnectionConfig, error) {
	modeValue, ok := nonEmptyEnvironment(lookup, "TRUVAG3_REDIS_MODE")
	if !ok {
		return RedisConnectionConfig{}, fmt.Errorf("TRUVAG3_REDIS_MODE is required for structured Redis configuration: %w", ErrInvalidConfiguration)
	}
	addressesValue, _ := nonEmptyEnvironment(lookup, "TRUVAG3_REDIS_ADDRS")
	addresses := splitRedisAddresses(addressesValue)
	config := DefaultRedisConnectionConfig()
	config.Mode = RedisMode(strings.ToLower(strings.TrimSpace(modeValue)))
	config.Addrs = addresses
	config.MasterName = environmentValue(lookup, "TRUVAG3_REDIS_MASTER_NAME")
	config.Username = rawEnvironmentValue(lookup, "TRUVAG3_REDIS_USERNAME")
	config.Password = rawEnvironmentValue(lookup, "TRUVAG3_REDIS_PASSWORD")
	config.SentinelUsername = rawEnvironmentValue(lookup, "TRUVAG3_REDIS_SENTINEL_USERNAME")
	config.SentinelPassword = rawEnvironmentValue(lookup, "TRUVAG3_REDIS_SENTINEL_PASSWORD")
	if value, present := nonEmptyEnvironment(lookup, "TRUVAG3_REDIS_DB"); present {
		database, err := strconv.Atoi(strings.TrimSpace(value))
		if err != nil {
			return RedisConnectionConfig{}, fmt.Errorf("TRUVAG3_REDIS_DB must be an integer: %w", ErrInvalidConfiguration)
		}
		config.DB = database
	}

	tlsEnabled := false
	if value, present := nonEmptyEnvironment(lookup, "TRUVAG3_REDIS_TLS_ENABLED"); present {
		parsed, err := strconv.ParseBool(strings.TrimSpace(value))
		if err != nil {
			return RedisConnectionConfig{}, fmt.Errorf("TRUVAG3_REDIS_TLS_ENABLED must be true or false: %w", ErrInvalidConfiguration)
		}
		tlsEnabled = parsed
	}
	serverName := environmentValue(lookup, "TRUVAG3_REDIS_TLS_SERVER_NAME")
	caFile := environmentValue(lookup, "TRUVAG3_REDIS_CA_FILE")
	if !tlsEnabled && (serverName != "" || caFile != "") {
		return RedisConnectionConfig{}, fmt.Errorf("redis TLS settings require TRUVAG3_REDIS_TLS_ENABLED=true: %w", ErrInvalidConfiguration)
	}
	if tlsEnabled {
		config.TLSConfig = &tls.Config{MinVersion: tls.VersionTLS12, ServerName: serverName}
		if caFile != "" {
			// #nosec G304 -- the operator explicitly supplies this startup-only CA
			// path; reading an arbitrary mounted trust bundle is the intended API.
			contents, err := os.ReadFile(caFile)
			if err != nil {
				return RedisConnectionConfig{}, fmt.Errorf("read Redis CA file: %w", ErrInvalidConfiguration)
			}
			pool, err := x509.SystemCertPool()
			if err != nil || pool == nil {
				pool = x509.NewCertPool()
			}
			if !pool.AppendCertsFromPEM(contents) {
				return RedisConnectionConfig{}, fmt.Errorf("redis CA file contains no certificates: %w", ErrInvalidConfiguration)
			}
			config.TLSConfig.RootCAs = pool
		}
	}
	return normalizeRedisConnectionConfig(config)
}

func loadRedisOperationalConfig(config RedisConnectionConfig, lookup LookupEnv) (RedisConnectionConfig, error) {
	intSettings := []struct {
		name  string
		apply func(int)
	}{
		{name: "TRUVAG3_REDIS_POOL_SIZE", apply: func(value int) { config.PoolSize = value }},
		{name: "TRUVAG3_REDIS_MIN_IDLE_CONNS", apply: func(value int) { config.MinIdle = value }},
		{name: "TRUVAG3_REDIS_MAX_RETRIES", apply: func(value int) { config.MaxRetries = value }},
	}
	for _, setting := range intSettings {
		if value, present := nonEmptyEnvironment(lookup, setting.name); present {
			parsed, err := strconv.Atoi(strings.TrimSpace(value))
			if err != nil {
				return RedisConnectionConfig{}, fmt.Errorf("%s must be an integer: %w", setting.name, ErrInvalidConfiguration)
			}
			setting.apply(parsed)
		}
	}
	durationSettings := []struct {
		name  string
		apply func(time.Duration)
	}{
		{name: "TRUVAG3_REDIS_DIAL_TIMEOUT", apply: func(value time.Duration) { config.DialTimeout = value }},
		{name: "TRUVAG3_REDIS_READ_TIMEOUT", apply: func(value time.Duration) { config.ReadTimeout = value }},
		{name: "TRUVAG3_REDIS_WRITE_TIMEOUT", apply: func(value time.Duration) { config.WriteTimeout = value }},
	}
	for _, setting := range durationSettings {
		if value, present := nonEmptyEnvironment(lookup, setting.name); present {
			parsed, err := time.ParseDuration(strings.TrimSpace(value))
			if err != nil {
				return RedisConnectionConfig{}, fmt.Errorf("%s must be a Go duration: %w", setting.name, ErrInvalidConfiguration)
			}
			setting.apply(parsed)
		}
	}
	return normalizeRedisConnectionConfig(config)
}

func splitRedisAddresses(value string) []string {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	parts := strings.Split(value, ",")
	addresses := make([]string, 0, len(parts))
	for _, part := range parts {
		addresses = append(addresses, strings.TrimSpace(part))
	}
	return addresses
}

func nonEmptyEnvironment(lookup LookupEnv, name string) (string, bool) {
	value, present := lookup(name)
	return value, present && strings.TrimSpace(value) != ""
}

func environmentValue(lookup LookupEnv, name string) string {
	value, _ := lookup(name)
	return strings.TrimSpace(value)
}

func rawEnvironmentValue(lookup LookupEnv, name string) string {
	value, _ := lookup(name)
	return value
}

func anyEnvironmentVariablePresent(lookup LookupEnv, names []string) bool {
	for _, name := range names {
		if value, present := lookup(name); present && value != "" {
			return true
		}
	}
	return false
}

func redisConnectionSourcePresent(lookup LookupEnv) bool {
	if lookup == nil {
		return false
	}
	if _, present := nonEmptyEnvironment(lookup, "REDIS_URL"); present {
		return true
	}
	if _, present := nonEmptyEnvironment(lookup, "TRUVAG3_REDIS_URL"); present {
		return true
	}
	return anyEnvironmentVariablePresent(lookup, structuredRedisConnectionVariables)
}

func redisConnectionForDiscovery(config DiscoveryConfig) (RedisConnectionConfig, error) {
	if config.RedisConnection != nil {
		return ResolveRedisConnectionConfig(config.RedisConnection, nil)
	}
	if strings.TrimSpace(config.RedisURL) != "" {
		return ParseStandaloneRedisURL(config.RedisURL)
	}
	return RedisConnectionConfig{}, fmt.Errorf("redis discovery connection is required: %w", ErrMissingConfiguration)
}
