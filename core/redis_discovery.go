package core

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
)

// RedisDiscovery provides Redis-based service discovery (implements Discovery interface)
// It embeds RedisRegistry and adds discovery capabilities
type RedisDiscovery struct {
	*RedisRegistry        // Embed for registration capabilities
	logger         Logger // Optional logger for discovery operations
}

// Registry records and indexes share a slot. Recheck existence in the same
// operation as pruning so re-registration cannot lose fresh index membership.
var pruneMissingRegistryService = redis.NewScript(`
if redis.call("EXISTS", KEYS[1]) == 0 then
    for index = 2, #KEYS do
        redis.call("SREM", KEYS[index], ARGV[1])
    end
end
return 1
`)

// NewRedisDiscovery creates a new Redis discovery client
func NewRedisDiscovery(redisURL string) (*RedisDiscovery, error) {
	return NewRedisDiscoveryWithOptions(redisURL, "default", 0)
}

// NewRedisDiscoveryWithNamespace creates a new Redis discovery client with custom namespace
func NewRedisDiscoveryWithNamespace(redisURL, namespace string) (*RedisDiscovery, error) {
	return NewRedisDiscoveryWithOptions(redisURL, namespace, 0)
}

// NewRedisDiscoveryWithOptions creates a new Redis discovery client with custom namespace and TTL.
// If ttl is 0 or negative, defaults to 30 seconds. Minimum TTL is 5 seconds.
// TTL clamping is handled by NewRedisRegistryWithOptions.
func NewRedisDiscoveryWithOptions(redisURL, namespace string, ttl time.Duration) (*RedisDiscovery, error) {
	registry, err := NewRedisRegistryWithOptions(redisURL, namespace, ttl)
	if err != nil {
		return nil, err
	}
	return &RedisDiscovery{RedisRegistry: registry}, nil
}

// NewRedisDiscoveryWithConnection creates owning discovery for an explicit
// standalone, Sentinel, or cluster connection profile.
func NewRedisDiscoveryWithConnection(profile RedisConnectionConfig, keyspace RedisKeyspace, ttl time.Duration) (*RedisDiscovery, error) {
	registry, err := NewRedisRegistryWithConnection(profile, keyspace, ttl)
	if err != nil {
		return nil, err
	}
	return &RedisDiscovery{RedisRegistry: registry}, nil
}

// NewRedisDiscoveryWithClient creates discovery around an application-owned
// standalone, Sentinel, or cluster client.
func NewRedisDiscoveryWithClient(client redis.UniversalClient, keyspace RedisKeyspace, ttl time.Duration) (*RedisDiscovery, error) {
	registry, err := NewRedisRegistryWithClient(client, keyspace, ttl)
	if err != nil {
		return nil, err
	}
	return &RedisDiscovery{RedisRegistry: registry}, nil
}

// SetLogger sets the logger for the discovery client
// The logger is wrapped with component "framework/core" to identify logs from this module
func (d *RedisDiscovery) SetLogger(logger Logger) {
	if logger != nil {
		if cal, ok := logger.(ComponentAwareLogger); ok {
			d.logger = cal.WithComponent("framework/core")
		} else {
			d.logger = logger
		}
	} else {
		d.logger = nil
	}
	// Also set logger for embedded registry (will apply its own WithComponent)
	if d.RedisRegistry != nil {
		d.RedisRegistry.SetLogger(logger)
	}
}

// Discover finds services based on filter criteria (implements Discovery interface)
func (d *RedisDiscovery) Discover(ctx context.Context, filter DiscoveryFilter) ([]*ServiceInfo, error) {
	start := time.Now()

	if d.logger != nil {
		d.logger.InfoWithContext(ctx, "Starting service discovery", map[string]interface{}{
			"request_id":          GetRequestID(ctx),
			"operation":           "discovery_lookup",
			"filter_type":         filter.Type,
			"filter_name":         filter.Name,
			"filter_capabilities": filter.Capabilities,
			"has_metadata_filter": len(filter.Metadata) > 0,
		})
	}

	var services []*ServiceInfo
	var serviceIDs []string
	var candidateIndexKeys []string

	// Filter by type if specified
	if filter.Type != "" {
		typeKey := d.keys.serviceType(filter.Type)
		if d.logger != nil {
			d.logger.DebugWithContext(ctx, "Filtering services by type", map[string]interface{}{
				"request_id": GetRequestID(ctx),
				"operation":  "discovery_lookup",
				"type":       filter.Type,
			})
		}

		ids, err := d.scanIndexMembers(ctx, typeKey)
		if err != nil && err != redis.Nil {
			// Emit framework metrics for discovery error
			if registry := GetGlobalMetricsRegistry(); registry != nil {
				duration := float64(time.Since(start).Milliseconds())
				registry.Counter("discovery.lookups",
					"namespace", d.namespace,
					"filter_type", string(filter.Type),
					"status", "error",
					"error_type", "type_lookup",
				)
				registry.Histogram("discovery.lookup.duration_ms", duration,
					"namespace", d.namespace,
					"filter_type", string(filter.Type),
				)
			}

			if d.logger != nil {
				d.logger.ErrorWithContext(ctx, "Failed to find services by type", map[string]interface{}{
					"request_id": GetRequestID(ctx),
					"operation":  "discovery_lookup",
					"error":      "redis discovery backend_operation failed",
					"error_type": "backend_operation",
					"type":       filter.Type,
				})
			}
			return nil, fmt.Errorf("failed to find services by type %s: %w", filter.Type, err)
		}
		serviceIDs = append(serviceIDs, ids...)
		candidateIndexKeys = append(candidateIndexKeys, typeKey)

		if d.logger != nil {
			d.logger.DebugWithContext(ctx, "Found services by type", map[string]interface{}{
				"request_id":     GetRequestID(ctx),
				"operation":      "discovery_lookup",
				"type":           filter.Type,
				"services_count": len(ids),
			})
		}
	}

	// Filter by name if specified
	if filter.Name != "" {
		nameKey := d.keys.name(filter.Name)
		if d.logger != nil {
			d.logger.DebugWithContext(ctx, "Filtering services by name", map[string]interface{}{
				"request_id": GetRequestID(ctx),
				"operation":  "discovery_lookup",
				"name":       filter.Name,
			})
		}

		ids, err := d.scanIndexMembers(ctx, nameKey)
		if err != nil && err != redis.Nil {
			// Emit framework metrics for name lookup error
			if registry := GetGlobalMetricsRegistry(); registry != nil {
				duration := float64(time.Since(start).Milliseconds())
				registry.Counter("discovery.lookups",
					"namespace", d.namespace,
					"filter_type", "name",
					"status", "error",
					"error_type", "name_lookup",
				)
				registry.Histogram("discovery.lookup.duration_ms", duration,
					"namespace", d.namespace,
					"filter_type", "name",
				)
			}

			if d.logger != nil {
				d.logger.ErrorWithContext(ctx, "Failed to find services by name", map[string]interface{}{
					"request_id": GetRequestID(ctx),
					"operation":  "discovery_lookup",
					"error":      "redis discovery backend_operation failed",
					"error_type": "backend_operation",
					"name":       filter.Name,
				})
			}
			return nil, fmt.Errorf("failed to find services by name %s: %w", filter.Name, err)
		}
		candidateIndexKeys = append(candidateIndexKeys, nameKey)

		if filter.Type != "" {
			// Intersect with type filter
			beforeCount := len(serviceIDs)
			serviceIDs = intersect(serviceIDs, ids)
			if d.logger != nil {
				d.logger.DebugWithContext(ctx, "Applied name filter intersection", map[string]interface{}{
					"request_id":          GetRequestID(ctx),
					"operation":           "discovery_lookup",
					"name":                filter.Name,
					"before_intersection": beforeCount,
					"after_intersection":  len(serviceIDs),
					"name_matches":        len(ids),
				})
			}
		} else {
			serviceIDs = append(serviceIDs, ids...)
			if d.logger != nil {
				d.logger.DebugWithContext(ctx, "Found services by name", map[string]interface{}{
					"request_id":     GetRequestID(ctx),
					"operation":      "discovery_lookup",
					"name":           filter.Name,
					"services_count": len(ids),
				})
			}
		}
	}

	// Filter by capabilities if specified
	if len(filter.Capabilities) > 0 {
		if d.logger != nil {
			d.logger.DebugWithContext(ctx, "Filtering services by capabilities", map[string]interface{}{
				"request_id":         GetRequestID(ctx),
				"operation":          "discovery_lookup",
				"capabilities":       filter.Capabilities,
				"capabilities_count": len(filter.Capabilities),
			})
		}

		var capIDs []string
		for _, capability := range filter.Capabilities {
			capKey := d.keys.capability(capability)
			ids, err := d.scanIndexMembers(ctx, capKey)
			if err != nil && err != redis.Nil {
				if d.logger != nil {
					d.logger.WarnWithContext(ctx, "Failed to find services by capability", map[string]interface{}{
						"request_id": GetRequestID(ctx),
						"operation":  "discovery_lookup",
						"error":      "redis discovery backend_operation failed",
						"error_type": "backend_operation",
						"capability": capability,
					})
				}
				continue
			}
			capIDs = append(capIDs, ids...)
			candidateIndexKeys = append(candidateIndexKeys, capKey)

			if d.logger != nil {
				d.logger.DebugWithContext(ctx, "Found services by capability", map[string]interface{}{
					"request_id":     GetRequestID(ctx),
					"operation":      "discovery_lookup",
					"capability":     capability,
					"services_count": len(ids),
				})
			}
		}

		if len(serviceIDs) > 0 {
			// Intersect with existing filters
			beforeCount := len(serviceIDs)
			serviceIDs = intersect(serviceIDs, capIDs)
			if d.logger != nil {
				d.logger.DebugWithContext(ctx, "Applied capability filter intersection", map[string]interface{}{
					"request_id":           GetRequestID(ctx),
					"operation":            "discovery_lookup",
					"before_intersection":  beforeCount,
					"after_intersection":   len(serviceIDs),
					"capability_matches":   len(capIDs),
					"capabilities_checked": len(filter.Capabilities),
				})
			}
		} else {
			serviceIDs = capIDs
			if d.logger != nil {
				d.logger.DebugWithContext(ctx, "Using capability filter as primary", map[string]interface{}{
					"request_id":           GetRequestID(ctx),
					"operation":            "discovery_lookup",
					"services_count":       len(capIDs),
					"capabilities_checked": len(filter.Capabilities),
				})
			}
		}
	}

	// If no filters specified, get all services
	if filter.Type == "" && filter.Name == "" && len(filter.Capabilities) == 0 {
		if d.logger != nil {
			d.logger.DebugWithContext(ctx, "No filters specified, getting all services", map[string]interface{}{
				"request_id": GetRequestID(ctx),
				"operation":  "discovery_lookup",
				"namespace":  d.namespace,
			})
		}

		ids, err := d.scanIndexMembers(ctx, d.keys.all())
		if err != nil {
			if d.logger != nil {
				d.logger.ErrorWithContext(ctx, "Failed to list all services", map[string]interface{}{
					"request_id": GetRequestID(ctx),
					"operation":  "discovery_lookup",
					"error":      "redis discovery backend_operation failed",
					"error_type": "backend_operation",
					"namespace":  d.namespace,
				})
			}
			return nil, fmt.Errorf("failed to list all services: %w", err)
		}
		serviceIDs = ids
		candidateIndexKeys = append(candidateIndexKeys, d.keys.all())

		if d.logger != nil {
			d.logger.DebugWithContext(ctx, "Found all services", map[string]interface{}{
				"request_id":     GetRequestID(ctx),
				"operation":      "discovery_lookup",
				"total_services": len(serviceIDs),
			})
		}
	}

	// Remove duplicates
	seen := make(map[string]bool)
	uniqueIDs := []string{}
	for _, id := range serviceIDs {
		if !seen[id] {
			seen[id] = true
			uniqueIDs = append(uniqueIDs, id)
		}
	}

	// Fetch service info for each ID
	if d.logger != nil {
		d.logger.DebugWithContext(ctx, "Fetching service details", map[string]interface{}{
			"request_id":          GetRequestID(ctx),
			"operation":           "discovery_lookup",
			"unique_services":     len(uniqueIDs),
			"has_metadata_filter": len(filter.Metadata) > 0,
		})
	}

	skippedExpired := 0
	skippedMalformed := 0
	skippedMetadata := 0
	staleIDs := make([]string, 0)

	for _, id := range uniqueIDs {
		key := d.keys.service(id)
		data, err := d.client.Get(ctx, key).Result()
		if err != nil {
			if err == redis.Nil {
				// Service expired or deleted, skip
				skippedExpired++
				staleIDs = append(staleIDs, id)
				if d.logger != nil {
					d.logger.DebugWithContext(ctx, "Service expired or deleted", map[string]interface{}{
						"request_id": GetRequestID(ctx),
						"operation":  "discovery_lookup",
						"service_id": id,
					})
				}
				continue
			}
			// Emit framework metrics for service get error
			if registry := GetGlobalMetricsRegistry(); registry != nil {
				duration := float64(time.Since(start).Milliseconds())
				filterType := string(filter.Type)
				if filterType == "" {
					filterType = "all"
				}
				registry.Counter("discovery.lookups",
					"namespace", d.namespace,
					"filter_type", filterType,
					"status", "error",
					"error_type", "service_get",
				)
				registry.Histogram("discovery.lookup.duration_ms", duration,
					"namespace", d.namespace,
					"filter_type", filterType,
				)
			}

			if d.logger != nil {
				d.logger.ErrorWithContext(ctx, "Failed to get service data", map[string]interface{}{
					"request_id": GetRequestID(ctx),
					"operation":  "discovery_lookup",
					"error":      "redis discovery backend_operation failed",
					"error_type": "backend_operation",
					"service_id": id,
				})
			}
			return nil, fmt.Errorf("failed to get service %s: %w", id, err)
		}

		var info ServiceInfo
		if err := json.Unmarshal([]byte(data), &info); err != nil {
			// Log malformed entries instead of silently skipping
			skippedMalformed++
			if d.logger != nil {
				d.logger.WarnWithContext(ctx, "Skipping malformed service entry", map[string]interface{}{
					"request_id": GetRequestID(ctx),
					"operation":  "discovery_lookup",
					"error":      "redis discovery decode failed",
					"error_type": "decode",
					"service_id": id,
					"data_size":  len(data),
				})
			}
			continue
		}

		// Apply metadata filter if specified
		if len(filter.Metadata) > 0 {
			match := true
			for k, v := range filter.Metadata {
				if info.Metadata[k] != v {
					match = false
					break
				}
			}
			if !match {
				skippedMetadata++
				if d.logger != nil {
					d.logger.DebugWithContext(ctx, "Service filtered out by metadata", map[string]interface{}{
						"request_id":   GetRequestID(ctx),
						"operation":    "discovery_lookup",
						"service_id":   id,
						"service_name": info.Name,
					})
				}
				continue
			}
		}

		services = append(services, &info)
	}
	if len(staleIDs) > 0 {
		for _, id := range staleIDs {
			keys := append([]string{d.keys.service(id)}, candidateIndexKeys...)
			if err := pruneMissingRegistryService.Run(ctx, d.client, keys, id).Err(); err != nil && d.logger != nil {
				d.logger.WarnWithContext(ctx, "Failed to prune stale discovery index entries", map[string]interface{}{
					"operation":   "discovery_index_cleanup",
					"request_id":  GetRequestID(ctx),
					"error":       "redis discovery index cleanup failed",
					"error_type":  "index_write",
					"stale_count": len(staleIDs),
				})
			}
		}
	}

	// Emit framework metrics for successful discovery
	if registry := GetGlobalMetricsRegistry(); registry != nil {
		duration := float64(time.Since(start).Milliseconds())
		filterType := string(filter.Type)
		if filterType == "" {
			filterType = "all"
		}
		registry.Counter("discovery.lookups",
			"namespace", d.namespace,
			"filter_type", filterType,
			"status", "success",
		)
		registry.Histogram("discovery.lookup.duration_ms", duration,
			"namespace", d.namespace,
			"filter_type", filterType,
		)
		registry.Gauge("discovery.services.found", float64(len(services)),
			"namespace", d.namespace,
			"filter_type", filterType,
		)
	}

	// Log discovery summary
	if d.logger != nil {
		d.logger.InfoWithContext(ctx, "Service discovery completed", map[string]interface{}{
			"request_id":          GetRequestID(ctx),
			"operation":           "discovery_lookup",
			"services_found":      len(services),
			"services_checked":    len(uniqueIDs),
			"skipped_expired":     skippedExpired,
			"skipped_malformed":   skippedMalformed,
			"skipped_metadata":    skippedMetadata,
			"filter_type":         filter.Type,
			"filter_name":         filter.Name,
			"filter_capabilities": filter.Capabilities,
		})
	}

	return services, nil
}

func (d *RedisDiscovery) scanIndexMembers(ctx context.Context, key string) ([]string, error) {
	const countHint int64 = 256
	seen := make(map[string]struct{})
	var cursor uint64
	for {
		batch, next, err := d.client.SScan(ctx, key, cursor, "", countHint).Result()
		if err != nil {
			return nil, err
		}
		for _, id := range batch {
			seen[id] = struct{}{}
		}
		cursor = next
		if cursor == 0 {
			break
		}
	}
	ids := make([]string, 0, len(seen))
	for id := range seen {
		ids = append(ids, id)
	}
	return ids, nil
}

// FindService finds services by name (backward compatibility)
func (d *RedisDiscovery) FindService(ctx context.Context, serviceName string) ([]*ServiceInfo, error) {
	return d.Discover(ctx, DiscoveryFilter{Name: serviceName})
}

// FindByCapability finds services by capability (backward compatibility)
func (d *RedisDiscovery) FindByCapability(ctx context.Context, capability string) ([]*ServiceInfo, error) {
	return d.Discover(ctx, DiscoveryFilter{Capabilities: []string{capability}})
}

// intersect returns the intersection of two string slices
func intersect(a, b []string) []string {
	set := make(map[string]bool)
	for _, v := range a {
		set[v] = true
	}

	seen := make(map[string]bool)
	var result []string
	for _, v := range b {
		if set[v] && !seen[v] {
			seen[v] = true
			result = append(result, v)
		}
	}
	return result
}
