package core

import (
	"time"

	"github.com/redis/go-redis/v9"
)

const (
	// DefaultRedisProtocol keeps framework-owned clients on RESP2. Developers can
	// explicitly select RESP3 in options they own when their complete command and
	// provider surface has been validated against it.
	DefaultRedisProtocol = 2

	defaultRedisReadTimeout     = 3 * time.Second
	defaultRedisMinRetryBackoff = 8 * time.Millisecond
	defaultRedisMaxRetryBackoff = 512 * time.Millisecond
	defaultRedisConnMaxIdleTime = 5 * time.Minute
)

// ApplyRedisUniversalDefaults applies TruvaG3's stable Redis defaults to
// standalone, Sentinel, and cluster client options. The caller's value, address
// slice, and TLS configuration are not mutated.
func ApplyRedisUniversalDefaults(options *redis.UniversalOptions) *redis.UniversalOptions {
	if options == nil {
		options = &redis.UniversalOptions{}
	}
	resolved := *options
	resolved.Addrs = append([]string(nil), options.Addrs...)
	if options.TLSConfig != nil {
		resolved.TLSConfig = options.TLSConfig.Clone()
	}
	options = &resolved
	if options.Protocol == 0 {
		options.Protocol = DefaultRedisProtocol
	}
	if options.DialTimeout == 0 {
		options.DialTimeout = 5 * time.Second
	}
	if options.ReadTimeout == 0 {
		options.ReadTimeout = defaultRedisReadTimeout
	}
	if options.WriteTimeout == 0 {
		options.WriteTimeout = options.ReadTimeout
	}
	if options.MinRetryBackoff == 0 {
		options.MinRetryBackoff = defaultRedisMinRetryBackoff
	}
	if options.MaxRetryBackoff == 0 {
		options.MaxRetryBackoff = defaultRedisMaxRetryBackoff
	}
	if options.ConnMaxIdleTime == 0 {
		options.ConnMaxIdleTime = defaultRedisConnMaxIdleTime
	}
	if options.DialerRetries == 0 {
		// A value of one means one dial attempt, matching the pre-v9 client.
		options.DialerRetries = 1
	}
	return options
}

// ApplyRedisClientDefaults applies the universal defaults to standalone
// options while preserving standalone-only fields. It remains the compatibility
// wrapper for existing direct go-redis client construction.
func ApplyRedisClientDefaults(options *redis.Options) *redis.Options {
	if options == nil {
		options = &redis.Options{}
	}
	resolved := ApplyRedisUniversalDefaults(universalOptionsFromSimple(options)).Simple()

	// These fields do not have a UniversalOptions equivalent, or are not copied
	// back by UniversalOptions.Simple in the pinned go-redis version.
	resolved.Network = options.Network
	resolved.NodeAddress = options.NodeAddress
	resolved.DialerRetryBackoff = options.DialerRetryBackoff
	resolved.PipelineReadBufferSize = options.PipelineReadBufferSize
	resolved.PipelineWriteBufferSize = options.PipelineWriteBufferSize
	resolved.PipelinePoolSize = options.PipelinePoolSize
	resolved.Limiter = options.Limiter
	resolved.FailingTimeoutSeconds = options.FailingTimeoutSeconds
	return resolved
}

func universalOptionsFromSimple(options *redis.Options) *redis.UniversalOptions {
	return &redis.UniversalOptions{
		Addrs:                        []string{options.Addr},
		ClientName:                   options.ClientName,
		DB:                           options.DB,
		Dialer:                       options.Dialer,
		OnConnect:                    options.OnConnect,
		Protocol:                     options.Protocol,
		Username:                     options.Username,
		Password:                     options.Password,
		CredentialsProvider:          options.CredentialsProvider,
		CredentialsProviderContext:   options.CredentialsProviderContext,
		StreamingCredentialsProvider: options.StreamingCredentialsProvider,
		MaxRetries:                   options.MaxRetries,
		MinRetryBackoff:              options.MinRetryBackoff,
		MaxRetryBackoff:              options.MaxRetryBackoff,
		DialTimeout:                  options.DialTimeout,
		DialerRetries:                options.DialerRetries,
		DialerRetryTimeout:           options.DialerRetryTimeout,
		ReadTimeout:                  options.ReadTimeout,
		WriteTimeout:                 options.WriteTimeout,
		ContextTimeoutEnabled:        options.ContextTimeoutEnabled,
		ReadBufferSize:               options.ReadBufferSize,
		WriteBufferSize:              options.WriteBufferSize,
		PoolFIFO:                     options.PoolFIFO,
		PoolSize:                     options.PoolSize,
		MaxConcurrentDials:           options.MaxConcurrentDials,
		PoolTimeout:                  options.PoolTimeout,
		MinIdleConns:                 options.MinIdleConns,
		MaxIdleConns:                 options.MaxIdleConns,
		MaxActiveConns:               options.MaxActiveConns,
		ConnMaxIdleTime:              options.ConnMaxIdleTime,
		ConnMaxLifetime:              options.ConnMaxLifetime,
		ConnMaxLifetimeJitter:        options.ConnMaxLifetimeJitter,
		TLSConfig:                    options.TLSConfig,
		DisableIdentity:              options.DisableIdentity,
		// Deprecated fields are copied deliberately so this compatibility helper
		// remains lossless for callers using the pinned go-redis API.
		DisableIndentity:          options.DisableIndentity, //nolint:staticcheck
		IdentitySuffix:            options.IdentitySuffix,
		UnstableResp3:             options.UnstableResp3, //nolint:staticcheck
		PushNotificationProcessor: options.PushNotificationProcessor,
		AutoPipelineOptions:       options.AutoPipelineOptions,
		MaintNotificationsConfig:  options.MaintNotificationsConfig,
		ClientSideCacheConfig:     options.ClientSideCacheConfig,
		ClientSideCache:           options.ClientSideCache,
		ClientSideCacheStrategy:   options.ClientSideCacheStrategy,
	}
}
