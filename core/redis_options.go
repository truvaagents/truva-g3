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

// ApplyRedisClientDefaults applies TruvaG3's stable Redis client defaults to
// zero-value fields. Explicit non-zero developer configuration is preserved,
// including Protocol 3, disabled timeouts and custom dialers.
//
// A shallow copy is returned so applying framework defaults does not mutate the
// caller's options. The dialer implementation, TCP keepalive and buffer sizing
// remain owned by go-redis/v9 unless the caller configures them explicitly.
func ApplyRedisClientDefaults(options *redis.Options) *redis.Options {
	if options == nil {
		options = &redis.Options{}
	}
	resolved := *options
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
