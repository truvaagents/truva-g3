package core

import (
	"encoding/base64"
	"fmt"
	"regexp"
	"strings"
)

const redisSchemaVersion = "v1"

var redisNamespacePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)

// RedisKeyspace builds DB-0 keys with a validated deployment namespace and a
// stable schema version. Adapters remain responsible for their subsystem and
// record identifiers.
type RedisKeyspace struct {
	deployment string
}

// NewRedisKeyspace validates a deployment namespace. An empty value selects
// the development-safe "default" namespace.
func NewRedisKeyspace(deployment string) (RedisKeyspace, error) {
	deployment = strings.TrimSpace(deployment)
	if deployment == "" {
		deployment = "default"
	}
	if !redisNamespacePattern.MatchString(deployment) {
		return RedisKeyspace{}, fmt.Errorf("invalid Redis deployment namespace: %w", ErrInvalidConfiguration)
	}
	return RedisKeyspace{deployment: deployment}, nil
}

// Deployment returns the validated deployment namespace.
func (keyspace RedisKeyspace) Deployment() string {
	if keyspace.deployment == "" {
		return "default"
	}
	return keyspace.deployment
}

// Plain builds a versioned key that is not assigned an explicit cluster hash
// tag.
func (keyspace RedisKeyspace) Plain(subsystem string, suffix ...string) string {
	subsystem = redisKeyspaceSegment(subsystem)
	parts := []string{"truvag3", redisSchemaVersion, keyspace.Deployment(), subsystem}
	parts = append(parts, suffix...)
	return strings.Join(parts, ":")
}

// Tagged builds a versioned key whose deployment, subsystem, and optional
// atomic scope form its cluster hash tag.
func (keyspace RedisKeyspace) Tagged(subsystem, scope string, suffix ...string) string {
	subsystem = redisKeyspaceSegment(subsystem)
	tag := keyspace.Deployment() + ":" + subsystem
	if scope != "" {
		tag += ":" + redisHashTagScope(scope)
	}
	parts := []string{
		"truvag3",
		redisSchemaVersion,
		keyspace.Deployment(),
		subsystem,
		"{" + tag + "}",
	}
	parts = append(parts, suffix...)
	return strings.Join(parts, ":")
}

func redisHashTagScope(scope string) string {
	return redisKeyspaceSegment(scope)
}

func redisKeyspaceSegment(segment string) string {
	if redisNamespacePattern.MatchString(segment) {
		return segment
	}
	return "~" + base64.RawURLEncoding.EncodeToString([]byte(segment))
}
