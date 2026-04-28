package core

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/go-redis/redis/v8"
)

// RedisDiscovery provides Redis-based service discovery (implements Discovery interface)
// It embeds RedisRegistry and adds discovery capabilities
type RedisDiscovery struct {
	*RedisRegistry        // Embed for registration capabilities
	logger         Logger // Optional logger for discovery operations
}

// NewRedisDiscovery creates a new Redis discovery client
func NewRedisDiscovery(redisURL string) (*RedisDiscovery, error) {
	return NewRedisDiscoveryWithOptions(redisURL, "truvag3", 0)
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

	return &RedisDiscovery{
		RedisRegistry: registry,
	}, nil
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
			"filter_type":         filter.Type,
			"filter_name":         filter.Name,
			"filter_capabilities": filter.Capabilities,
			"has_metadata_filter": len(filter.Metadata) > 0,
		})
	}

	var services []*ServiceInfo
	var serviceIDs []string

	// Filter by type if specified
	if filter.Type != "" {
		typeKey := fmt.Sprintf("%s:types:%s", d.namespace, filter.Type)
		if d.logger != nil {
			d.logger.DebugWithContext(ctx, "Filtering services by type", map[string]interface{}{
				"type":     filter.Type,
				"type_key": typeKey,
			})
		}

		ids, err := d.client.SMembers(ctx, typeKey).Result()
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
					"error":      err,
					"error_type": fmt.Sprintf("%T", err),
					"type":       filter.Type,
					"type_key":   typeKey,
				})
			}
			return nil, fmt.Errorf("failed to find services by type %s: %w", filter.Type, err)
		}
		serviceIDs = append(serviceIDs, ids...)

		if d.logger != nil {
			d.logger.DebugWithContext(ctx, "Found services by type", map[string]interface{}{
				"type":           filter.Type,
				"services_count": len(ids),
			})
		}
	}

	// Filter by name if specified
	if filter.Name != "" {
		nameKey := fmt.Sprintf("%s:names:%s", d.namespace, filter.Name)
		if d.logger != nil {
			d.logger.DebugWithContext(ctx, "Filtering services by name", map[string]interface{}{
				"name":     filter.Name,
				"name_key": nameKey,
			})
		}

		ids, err := d.client.SMembers(ctx, nameKey).Result()
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
					"error":      err,
					"error_type": fmt.Sprintf("%T", err),
					"name":       filter.Name,
					"name_key":   nameKey,
				})
			}
			return nil, fmt.Errorf("failed to find services by name %s: %w", filter.Name, err)
		}

		if filter.Type != "" {
			// Intersect with type filter
			beforeCount := len(serviceIDs)
			serviceIDs = intersect(serviceIDs, ids)
			if d.logger != nil {
				d.logger.DebugWithContext(ctx, "Applied name filter intersection", map[string]interface{}{
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
				"capabilities":       filter.Capabilities,
				"capabilities_count": len(filter.Capabilities),
			})
		}

		var capIDs []string
		for _, capability := range filter.Capabilities {
			capKey := fmt.Sprintf("%s:capabilities:%s", d.namespace, capability)
			ids, err := d.client.SMembers(ctx, capKey).Result()
			if err != nil && err != redis.Nil {
				if d.logger != nil {
					d.logger.WarnWithContext(ctx, "Failed to find services by capability", map[string]interface{}{
						"error":          err,
						"error_type":     fmt.Sprintf("%T", err),
						"capability":     capability,
						"capability_key": capKey,
					})
				}
				continue
			}
			capIDs = append(capIDs, ids...)

			if d.logger != nil {
				d.logger.DebugWithContext(ctx, "Found services by capability", map[string]interface{}{
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
				"namespace": d.namespace,
			})
		}

		// Get all service keys
		pattern := fmt.Sprintf("%s:services:*", d.namespace)
		keys, err := d.client.Keys(ctx, pattern).Result()
		if err != nil {
			if d.logger != nil {
				d.logger.ErrorWithContext(ctx, "Failed to list all services", map[string]interface{}{
					"error":      err,
					"error_type": fmt.Sprintf("%T", err),
					"pattern":    pattern,
					"namespace":  d.namespace,
				})
			}
			return nil, fmt.Errorf("failed to list all services: %w", err)
		}

		for _, key := range keys {
			// Extract service ID from key
			serviceID := key[len(fmt.Sprintf("%s:services:", d.namespace)):]
			serviceIDs = append(serviceIDs, serviceID)
		}

		if d.logger != nil {
			d.logger.DebugWithContext(ctx, "Found all services", map[string]interface{}{
				"total_services": len(serviceIDs),
				"pattern":        pattern,
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
			"unique_services":     len(uniqueIDs),
			"has_metadata_filter": len(filter.Metadata) > 0,
		})
	}

	skippedExpired := 0
	skippedMalformed := 0
	skippedMetadata := 0

	for _, id := range uniqueIDs {
		key := fmt.Sprintf("%s:services:%s", d.namespace, id)
		data, err := d.client.Get(ctx, key).Result()
		if err != nil {
			if err == redis.Nil {
				// Service expired or deleted, skip
				skippedExpired++
				if d.logger != nil {
					d.logger.DebugWithContext(ctx, "Service expired or deleted", map[string]interface{}{
						"service_id": id,
						"key":        key,
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
					"error":      err,
					"error_type": fmt.Sprintf("%T", err),
					"service_id": id,
					"key":        key,
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
					"error":      err,
					"error_type": fmt.Sprintf("%T", err),
					"service_id": id,
					"key":        key,
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
						"service_id":   id,
						"service_name": info.Name,
						"metadata":     info.Metadata,
						"filter":       filter.Metadata,
					})
				}
				continue
			}
		}

		services = append(services, &info)
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
