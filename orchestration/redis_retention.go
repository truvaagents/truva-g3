package orchestration

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"github.com/redis/go-redis/v9"
)

var extendRedisMinimumTTLScript = redis.NewScript(`
local current = redis.call("PTTL", KEYS[1])
local requested = tonumber(ARGV[1])
if current == -2 or current == -1 or current >= requested then
	return current
end
redis.call("PEXPIRE", KEYS[1], requested)
return current
`)

var extendRedisKeysMinimumTTLScript = redis.NewScript(`
local primary = redis.call("PTTL", KEYS[1])
if primary == -2 then
	return primary
end
local requested = tonumber(ARGV[1])
for index = 1, #KEYS do
	local current = redis.call("PTTL", KEYS[index])
	if current >= 0 and current < requested then
		redis.call("PEXPIRE", KEYS[index], requested)
	end
end
return primary
`)

var setRedisValueWithMinimumTTLScript = redis.NewScript(`
local previous = redis.call("PTTL", KEYS[1])
redis.call("SET", KEYS[1], ARGV[1])
if previous == -1 then
	return previous
end
local requested = tonumber(ARGV[2])
local selected = requested
if previous >= 0 and previous > selected then
	selected = previous
end
redis.call("PEXPIRE", KEYS[1], selected)
return previous
`)

var setRedisValuesWithMinimumTTLScript = redis.NewScript(`
local requested = tonumber(ARGV[#ARGV])
local previous_ttls = {}
local entries = {}
for index = 1, #KEYS do
	previous_ttls[index] = redis.call("PTTL", KEYS[index])
	table.insert(entries, KEYS[index])
	table.insert(entries, ARGV[index])
end
redis.call("MSET", unpack(entries))
for index = 1, #KEYS do
	local previous = previous_ttls[index]
	if previous ~= -1 then
		local selected = requested
		if previous >= 0 and previous > selected then
			selected = previous
		end
		redis.call("PEXPIRE", KEYS[index], selected)
	end
end
return #KEYS
`)

func positiveTTLMilliseconds(ttl time.Duration) (int64, error) {
	if ttl <= 0 {
		return 0, fmt.Errorf("duration must be positive")
	}
	milliseconds := ttl.Milliseconds()
	if milliseconds <= 0 {
		milliseconds = 1
	}
	return milliseconds, nil
}

// extendRedisKeyMinimumTTL atomically keeps an existing Redis key for at least
// minTTL from now. Missing keys are not created and persistent keys remain
// persistent. The boolean reports whether the key existed.
func extendRedisKeyMinimumTTL(
	ctx context.Context,
	client redis.UniversalClient,
	key string,
	minTTL time.Duration,
) (bool, error) {
	milliseconds, err := positiveTTLMilliseconds(minTTL)
	if err != nil {
		return false, err
	}
	previous, err := extendRedisMinimumTTLScript.Run(
		ctx,
		client,
		[]string{key},
		strconv.FormatInt(milliseconds, 10),
	).Int64()
	if err != nil {
		return false, err
	}
	return previous != redisPTTLKeyMissing, nil
}

// extendRedisKeysMinimumTTL atomically keeps an existing primary key and any
// present related keys for at least minTTL. A missing primary key causes a
// no-op for the whole key set. Missing related keys are never created and
// persistent keys remain persistent.
func extendRedisKeysMinimumTTL(
	ctx context.Context,
	client redis.UniversalClient,
	keys []string,
	minTTL time.Duration,
) (bool, error) {
	if len(keys) == 0 {
		return false, fmt.Errorf("at least one retention key is required")
	}
	milliseconds, err := positiveTTLMilliseconds(minTTL)
	if err != nil {
		return false, err
	}
	previous, err := extendRedisKeysMinimumTTLScript.Run(
		ctx,
		client,
		keys,
		strconv.FormatInt(milliseconds, 10),
	).Int64()
	if err != nil {
		return false, err
	}
	return previous != redisPTTLKeyMissing, nil
}

// setRedisValueWithMinimumTTL atomically writes a value and preserves the
// larger of the key's previous remaining TTL and minTTL. A missing key is
// created with minTTL; a previously persistent key remains persistent.
func setRedisValueWithMinimumTTL(
	ctx context.Context,
	client redis.UniversalClient,
	key string,
	value interface{},
	minTTL time.Duration,
) error {
	milliseconds, err := positiveTTLMilliseconds(minTTL)
	if err != nil {
		return err
	}
	return setRedisValueWithMinimumTTLScript.Run(
		ctx,
		client,
		[]string{key},
		value,
		strconv.FormatInt(milliseconds, 10),
	).Err()
}

// setRedisValuesWithMinimumTTL atomically writes related values while
// preserving each key's larger previous remaining TTL. Missing keys are
// created with minTTL and previously persistent keys remain persistent.
func setRedisValuesWithMinimumTTL(
	ctx context.Context,
	client redis.UniversalClient,
	keys []string,
	values []interface{},
	minTTL time.Duration,
) error {
	if len(keys) == 0 {
		return fmt.Errorf("at least one retention key is required")
	}
	if len(keys) != len(values) {
		return fmt.Errorf("retention key and value counts must match")
	}
	milliseconds, err := positiveTTLMilliseconds(minTTL)
	if err != nil {
		return err
	}
	args := make([]interface{}, 0, len(values)+1)
	args = append(args, values...)
	args = append(args, strconv.FormatInt(milliseconds, 10))
	return setRedisValuesWithMinimumTTLScript.Run(
		ctx,
		client,
		keys,
		args...,
	).Err()
}
