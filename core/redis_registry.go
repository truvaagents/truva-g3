package core

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"math/big"
	"strings"
	"sync"
	"time"

	"github.com/go-redis/redis/v8"
)

// HeartbeatStats tracks heartbeat statistics for periodic summaries
type HeartbeatStats struct {
	SuccessCount  int64
	FailureCount  int64
	LastSuccess   time.Time
	LastFailure   time.Time
	StartedAt     time.Time
	LastSummaryAt time.Time // Track when we last logged summary
}

// RegistryUpdateCallback is invoked when background retry successfully reconnects to Redis.
//
// This callback enables parent components (BaseTool, BaseAgent) to atomically update their
// registry/discovery references in a thread-safe manner. The callback receives the new
// registry instance and should:
//  1. Acquire appropriate locks (e.g., mu.Lock())
//  2. Stop old heartbeat if one exists
//  3. Update the registry/discovery field
//  4. Release locks
//
// Example usage in BaseTool:
//
//	onSuccess := func(newRegistry Registry) error {
//	    t.mu.Lock()
//	    defer t.mu.Unlock()
//	    if oldReg, ok := t.Registry.(*RedisRegistry); ok && oldReg != nil {
//	        oldReg.StopHeartbeat(ctx, t.ID)
//	    }
//	    t.Registry = newRegistry
//	    return nil
//	}
//
// The callback must return nil on success, or an error if the update failed.
// Note: For agents, the registry will be *RedisDiscovery (implements Discovery interface).
//
//	For tools, the registry will be *RedisRegistry (implements Registry interface).
type RegistryUpdateCallback func(newRegistry Registry) error

// registryRetryState maintains state for background retry attempts.
//
// This internal structure tracks the service information, current retry interval,
// and success callback for a background reconnection attempt. It's used by
// registryRetryManager to manage exponential backoff and service registration.
//
// Fields:
//   - serviceInfo: Information about the service to register (ID, Name, Type, etc.)
//   - currentInterval: Current retry interval (doubles on each failure, caps at 5 minutes)
//   - onSuccess: Callback to invoke when reconnection succeeds
//   - ttl: Registry TTL for service key expiration (0 = default 30s, min 5s)
//   - heartbeatInterval: Heartbeat refresh interval (0 = ttl/2, min 2s, must be < TTL)
type registryRetryState struct {
	serviceInfo       *ServiceInfo
	currentInterval   time.Duration
	onSuccess         RegistryUpdateCallback
	ttl               time.Duration // Registry TTL (0 = default 30s)
	heartbeatInterval time.Duration // Heartbeat interval (0 = ttl/2)
}

// RedisRegistry provides Redis-based service registration (implements Registry interface)
type RedisRegistry struct {
	client    *redis.Client
	namespace string
	ttl       time.Duration
	logger    Logger // Optional logger for better observability

	// Self-healing state management (internal enhancement)
	registrationState map[string]*ServiceInfo
	stateMutex        sync.RWMutex

	// Heartbeat tracking for periodic summaries
	heartbeatStats map[string]*HeartbeatStats
	heartbeatMutex sync.RWMutex

	// Heartbeat cancel functions for cleanup
	heartbeats   map[string]context.CancelFunc
	heartbeatsMu sync.RWMutex
}

// NewRedisRegistry creates a new Redis registry client
func NewRedisRegistry(redisURL string) (*RedisRegistry, error) {
	return NewRedisRegistryWithOptions(redisURL, "truvag3", 0)
}

// NewRedisRegistryWithNamespace creates a new Redis registry client with custom namespace
func NewRedisRegistryWithNamespace(redisURL, namespace string) (*RedisRegistry, error) {
	return NewRedisRegistryWithOptions(redisURL, namespace, 0)
}

// NewRedisRegistryWithOptions creates a new Redis registry client with custom namespace and TTL.
// If ttl is 0 or negative, defaults to 30 seconds. Minimum TTL is 5 seconds.
func NewRedisRegistryWithOptions(redisURL, namespace string, ttl time.Duration) (*RedisRegistry, error) {
	opt, err := redis.ParseURL(redisURL)
	if err != nil {
		return nil, fmt.Errorf("invalid Redis URL: %w", ErrInvalidConfiguration)
	}

	// Enhanced: Production-grade connection settings (internal enhancement)
	opt.PoolSize = 10                            // Handle 10 concurrent operations
	opt.MinIdleConns = 5                         // Keep 5 connections warm
	opt.MaxRetries = 3                           // Retry failed operations 3 times
	opt.MinRetryBackoff = time.Millisecond * 100 // Start with 100ms delay
	opt.MaxRetryBackoff = time.Second * 1        // Cap delay at 1 second
	opt.DialTimeout = time.Second * 5            // 5s to establish connection
	opt.ReadTimeout = time.Second * 5            // 5s for read operations
	opt.WriteTimeout = time.Second * 5           // 5s for write operations
	opt.PoolTimeout = time.Second * 10           // 10s to get connection from pool

	client := redis.NewClient(opt)

	// Enhanced: Connection verification with retry (reduced to ~10s total)
	// 3 attempts × 3s timeout + (2s + 2s) backoff = ~13 seconds
	for i := 0; i < 3; i++ {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		err = client.Ping(ctx).Err()
		cancel()

		if err == nil {
			break
		}

		if i < 2 { // Fixed backoff for faster startup
			time.Sleep(2 * time.Second)
		}
	}

	if err != nil {
		return nil, fmt.Errorf("failed to connect to Redis after retries: %w", ErrConnectionFailed)
	}

	// Apply TTL floor clamps
	const minTTL = 5 * time.Second

	if ttl <= 0 {
		ttl = 30 * time.Second // default
	} else if ttl < minTTL {
		ttl = minTTL // floor — prevent sub-second or absurdly low values
	}

	registry := &RedisRegistry{
		client:            client,
		namespace:         namespace,
		ttl:               ttl,
		registrationState: make(map[string]*ServiceInfo), // Enhanced: state storage
		stateMutex:        sync.RWMutex{},                // Enhanced: thread safety
		heartbeatStats:    make(map[string]*HeartbeatStats),
		heartbeatMutex:    sync.RWMutex{},
		heartbeats:        make(map[string]context.CancelFunc), // Track cancel functions
		heartbeatsMu:      sync.RWMutex{},
	}

	// Note: Logger will be set later via SetLogger method if needed
	// TTL clamp logging happens at the call site after SetLogger().

	return registry, nil
}

// TTL returns the effective TTL used for Redis key expiration.
func (r *RedisRegistry) TTL() time.Duration { return r.ttl }

// Register registers a service with the registry (implements Registry interface)
func (r *RedisRegistry) Register(ctx context.Context, info *ServiceInfo) error {
	start := time.Now()

	if r.logger != nil {
		r.logger.InfoWithContext(ctx, "Registering service", map[string]interface{}{
			"service_id":         info.ID,
			"service_name":       info.Name,
			"service_type":       info.Type,
			"capabilities_count": len(info.Capabilities),
			"address":            info.Address,
			"port":               info.Port,
			"ttl":                r.ttl.String(),
		})
	}

	// Store registration state for potential recovery (internal enhancement)
	r.storeRegistrationState(info)

	// Use atomic transactions (Issue #1 fix)
	pipe := r.client.TxPipeline()

	// Store main service data
	key := fmt.Sprintf("%s:services:%s", r.namespace, info.ID)
	data, err := json.Marshal(info)
	if err != nil {
		// Emit framework metrics for marshal failure
		if registry := GetGlobalMetricsRegistry(); registry != nil {
			registry.Counter("discovery.registrations",
				"service_type", string(info.Type),
				"namespace", r.namespace,
				"status", "error",
				"error_type", "marshal",
			)
		}

		if r.logger != nil {
			r.logger.ErrorWithContext(ctx, "Failed to marshal service info", map[string]interface{}{
				"error":        err,
				"error_type":   fmt.Sprintf("%T", err),
				"service_id":   info.ID,
				"service_name": info.Name,
			})
		}
		return fmt.Errorf("failed to marshal service info for %s: %w", info.ID, err)
	}
	pipe.Set(ctx, key, data, r.ttl)

	// Add to all indexes atomically
	for _, capability := range info.Capabilities {
		capKey := fmt.Sprintf("%s:capabilities:%s", r.namespace, capability.Name)
		pipe.SAdd(ctx, capKey, info.ID)
		pipe.Expire(ctx, capKey, r.ttl*2)
	}

	nameKey := fmt.Sprintf("%s:names:%s", r.namespace, info.Name)
	pipe.SAdd(ctx, nameKey, info.ID)
	pipe.Expire(ctx, nameKey, r.ttl*2)

	typeKey := fmt.Sprintf("%s:types:%s", r.namespace, info.Type)
	pipe.SAdd(ctx, typeKey, info.ID)
	pipe.Expire(ctx, typeKey, r.ttl*2)

	// Execute all operations atomically
	_, err = pipe.Exec(ctx)
	if err != nil {
		// Emit framework metrics for failed registration
		if registry := GetGlobalMetricsRegistry(); registry != nil {
			duration := float64(time.Since(start).Milliseconds())
			registry.Counter("discovery.registrations",
				"service_type", string(info.Type),
				"namespace", r.namespace,
				"status", "error",
				"error_type", "redis_exec",
			)
			registry.Histogram("discovery.registration.duration_ms", duration,
				"service_type", string(info.Type),
				"namespace", r.namespace,
			)
		}

		if r.logger != nil {
			r.logger.ErrorWithContext(ctx, "Failed to register service atomically", map[string]interface{}{
				"error":        err,
				"error_type":   fmt.Sprintf("%T", err),
				"service_id":   info.ID,
				"service_name": info.Name,
			})
		}
		return fmt.Errorf("failed to register service atomically: %w", err)
	}

	// Emit framework metrics for successful registration
	if registry := GetGlobalMetricsRegistry(); registry != nil {
		duration := float64(time.Since(start).Milliseconds())
		registry.Counter("discovery.registrations",
			"service_type", string(info.Type),
			"namespace", r.namespace,
			"status", "success",
		)
		registry.Histogram("discovery.registration.duration_ms", duration,
			"service_type", string(info.Type),
			"namespace", r.namespace,
		)
	}

	if r.logger != nil {
		r.logger.InfoWithContext(ctx, "Service registered successfully", map[string]interface{}{
			"service_id":         info.ID,
			"service_name":       info.Name,
			"service_type":       info.Type,
			"capabilities_count": len(info.Capabilities),
		})
	}

	return nil
}

// UpdateHealth updates service health status
func (r *RedisRegistry) UpdateHealth(ctx context.Context, serviceID string, status HealthStatus) error {
	start := time.Now()

	if r.logger != nil {
		r.logger.DebugWithContext(ctx, "Updating service health", map[string]interface{}{
			"service_id": serviceID,
			"status":     status,
		})
	}

	key := fmt.Sprintf("%s:services:%s", r.namespace, serviceID)

	// Get current registration
	data, err := r.client.Get(ctx, key).Result()
	if err != nil {
		if err == redis.Nil {
			// Emit framework metrics for service not found
			if registry := GetGlobalMetricsRegistry(); registry != nil {
				registry.Counter("discovery.health_checks",
					"namespace", r.namespace,
					"status", "not_found",
				)
			}

			if r.logger != nil {
				r.logger.WarnWithContext(ctx, "Service not found for health update", map[string]interface{}{
					"service_id": serviceID,
					"key":        key,
				})
			}
			return fmt.Errorf("service %s: %w", serviceID, ErrServiceNotFound)
		}

		// Emit framework metrics for Redis error
		if registry := GetGlobalMetricsRegistry(); registry != nil {
			registry.Counter("discovery.health_checks",
				"namespace", r.namespace,
				"status", "error",
				"error_type", "redis_get",
			)
		}

		if r.logger != nil {
			r.logger.ErrorWithContext(ctx, "Failed to get service for health update", map[string]interface{}{
				"error":      err,
				"error_type": fmt.Sprintf("%T", err),
				"service_id": serviceID,
				"key":        key,
			})
		}
		return fmt.Errorf("failed to get service %s: %w", serviceID, err)
	}

	var info ServiceInfo
	if err := json.Unmarshal([]byte(data), &info); err != nil {
		// Emit framework metrics for unmarshal failure
		if registry := GetGlobalMetricsRegistry(); registry != nil {
			registry.Counter("discovery.health_checks",
				"namespace", r.namespace,
				"status", "error",
				"error_type", "unmarshal",
			)
		}

		if r.logger != nil {
			r.logger.ErrorWithContext(ctx, "Failed to unmarshal service data for health update", map[string]interface{}{
				"error":      err,
				"error_type": fmt.Sprintf("%T", err),
				"service_id": serviceID,
				"key":        key,
				"data_size":  len(data),
			})
		}
		return fmt.Errorf("failed to unmarshal service data for %s: %w", serviceID, err)
	}

	// Update health and timestamp
	previousHealth := info.Health
	info.Health = status
	info.LastSeen = time.Now()

	// Marshal updated data
	updatedData, err := json.Marshal(info)
	if err != nil {
		// Emit framework metrics for marshal failure
		if registry := GetGlobalMetricsRegistry(); registry != nil {
			registry.Counter("discovery.health_checks",
				"namespace", r.namespace,
				"status", "error",
				"error_type", "marshal",
			)
		}

		if r.logger != nil {
			r.logger.ErrorWithContext(ctx, "Failed to marshal health data", map[string]interface{}{
				"error":      err,
				"error_type": fmt.Sprintf("%T", err),
				"service_id": serviceID,
				"status":     status,
			})
		}
		return fmt.Errorf("failed to marshal health data for %s: %w", serviceID, err)
	}

	// Update with TTL
	if err := r.client.Set(ctx, key, updatedData, r.ttl).Err(); err != nil {
		// Emit framework metrics for Redis SET failure
		if registry := GetGlobalMetricsRegistry(); registry != nil {
			registry.Counter("discovery.health_checks",
				"namespace", r.namespace,
				"status", "error",
				"error_type", "redis_set",
			)
		}

		if r.logger != nil {
			r.logger.ErrorWithContext(ctx, "Failed to store health update", map[string]interface{}{
				"error":      err,
				"error_type": fmt.Sprintf("%T", err),
				"service_id": serviceID,
				"key":        key,
				"status":     status,
				"ttl":        r.ttl.String(),
			})
		}
		return fmt.Errorf("failed to update health for %s: %w", serviceID, err)
	}

	// Refresh index set TTLs to prevent healthy services from disappearing
	// This fixes the critical bug where services become undiscoverable after 60s
	// even when they're healthy and sending heartbeats
	r.refreshIndexSetTTLs(ctx, &info)

	// Emit framework metrics for successful health update (heartbeat)
	if registry := GetGlobalMetricsRegistry(); registry != nil {
		duration := float64(time.Since(start).Milliseconds())
		registry.Counter("discovery.health_checks",
			"namespace", r.namespace,
			"status", "success",
			"health_status", string(status),
		)
		registry.Histogram("discovery.health_check.duration_ms", duration,
			"namespace", r.namespace,
		)
		// Record timestamp of last successful health check for freshness monitoring
		// This allows operators to see "time since last health check" in dashboards
		registry.Gauge("discovery.last_health_check_timestamp", float64(time.Now().Unix()),
			"namespace", r.namespace,
			"service_name", info.Name,
		)
	}

	if r.logger != nil {
		r.logger.DebugWithContext(ctx, "Service health updated", map[string]interface{}{
			"service_id":      serviceID,
			"previous_status": previousHealth,
			"new_status":      status,
			"last_seen":       info.LastSeen,
			"ttl":             r.ttl.String(),
		})
	}

	return nil
}

// Unregister removes a service from the registry
func (r *RedisRegistry) Unregister(ctx context.Context, serviceID string) error {
	start := time.Now()

	if r.logger != nil {
		r.logger.InfoWithContext(ctx, "Unregistering service", map[string]interface{}{
			"service_id": serviceID,
		})
	}

	key := fmt.Sprintf("%s:services:%s", r.namespace, serviceID)

	// Get service data to find capabilities
	data, err := r.client.Get(ctx, key).Result()
	if err == nil {
		var info ServiceInfo
		if err := json.Unmarshal([]byte(data), &info); err == nil {
			if r.logger != nil {
				r.logger.DebugWithContext(ctx, "Removing service from indexes", map[string]interface{}{
					"service_id":         serviceID,
					"service_name":       info.Name,
					"service_type":       info.Type,
					"capabilities_count": len(info.Capabilities),
				})
			}

			// Remove from capability indexes
			for _, capability := range info.Capabilities {
				capKey := fmt.Sprintf("%s:capabilities:%s", r.namespace, capability.Name)
				if err := r.client.SRem(ctx, capKey, serviceID).Err(); err != nil && r.logger != nil {
					r.logger.WarnWithContext(ctx, "Failed to remove from capability index", map[string]interface{}{
						"capability":     capability.Name,
						"capability_key": capKey,
						"service_id":     serviceID,
						"error":          err,
						"error_type":     fmt.Sprintf("%T", err),
					})
				}
			}
			// Remove from name index
			nameKey := fmt.Sprintf("%s:names:%s", r.namespace, info.Name)
			if err := r.client.SRem(ctx, nameKey, serviceID).Err(); err != nil && r.logger != nil {
				r.logger.WarnWithContext(ctx, "Failed to remove from name index", map[string]interface{}{
					"name_key":   nameKey,
					"service_id": serviceID,
					"error":      err,
					"error_type": fmt.Sprintf("%T", err),
				})
			}
			// Remove from type index
			typeKey := fmt.Sprintf("%s:types:%s", r.namespace, info.Type)
			if err := r.client.SRem(ctx, typeKey, serviceID).Err(); err != nil && r.logger != nil {
				r.logger.WarnWithContext(ctx, "Failed to remove from type index", map[string]interface{}{
					"type_key":   typeKey,
					"service_id": serviceID,
					"error":      err,
					"error_type": fmt.Sprintf("%T", err),
				})
			}
		} else {
			if r.logger != nil {
				r.logger.WarnWithContext(ctx, "Failed to unmarshal service data for unregistration", map[string]interface{}{
					"error":      err,
					"error_type": fmt.Sprintf("%T", err),
					"service_id": serviceID,
					"key":        key,
					"data_size":  len(data),
				})
			}
		}
	} else if err != redis.Nil && r.logger != nil {
		r.logger.WarnWithContext(ctx, "Failed to get service data for unregistration", map[string]interface{}{
			"error":      err,
			"error_type": fmt.Sprintf("%T", err),
			"service_id": serviceID,
			"key":        key,
		})
	}

	// Delete service key
	if err := r.client.Del(ctx, key).Err(); err != nil {
		// Emit framework metrics for failed unregistration
		if registry := GetGlobalMetricsRegistry(); registry != nil {
			duration := float64(time.Since(start).Milliseconds())
			registry.Counter("discovery.unregistrations",
				"namespace", r.namespace,
				"status", "error",
			)
			registry.Histogram("discovery.unregistration.duration_ms", duration,
				"namespace", r.namespace,
			)
		}

		if r.logger != nil {
			r.logger.ErrorWithContext(ctx, "Failed to delete service key", map[string]interface{}{
				"error":      err,
				"error_type": fmt.Sprintf("%T", err),
				"service_id": serviceID,
				"key":        key,
			})
		}
		return fmt.Errorf("failed to unregister service %s: %w", serviceID, err)
	}

	// Clean up registration state (prevents memory leaks)
	r.stateMutex.Lock()
	delete(r.registrationState, serviceID)
	r.stateMutex.Unlock()

	// Emit framework metrics for successful unregistration
	if registry := GetGlobalMetricsRegistry(); registry != nil {
		duration := float64(time.Since(start).Milliseconds())
		registry.Counter("discovery.unregistrations",
			"namespace", r.namespace,
			"status", "success",
		)
		registry.Histogram("discovery.unregistration.duration_ms", duration,
			"namespace", r.namespace,
		)
	}

	if r.logger != nil {
		r.logger.InfoWithContext(ctx, "Service unregistered successfully", map[string]interface{}{
			"service_id": serviceID,
			"key":        key,
		})
	}

	return nil
}

// refreshIndexSetTTLs extends TTL for all index sets this service belongs to
// This prevents healthy services from becoming undiscoverable when index sets expire
// before the service keys. Called during heartbeat to keep index sets alive.
func (r *RedisRegistry) refreshIndexSetTTLs(ctx context.Context, info *ServiceInfo) {
	if r.logger != nil {
		r.logger.DebugWithContext(ctx, "Refreshing index set TTLs", map[string]interface{}{
			"service_id":         info.ID,
			"service_name":       info.Name,
			"service_type":       info.Type,
			"capabilities_count": len(info.Capabilities),
			"ttl":                (r.ttl * 2).String(),
		})
	}

	// Refresh capability indexes
	for _, capability := range info.Capabilities {
		capKey := fmt.Sprintf("%s:capabilities:%s", r.namespace, capability.Name)
		if err := r.client.Expire(ctx, capKey, r.ttl*2).Err(); err != nil {
			if r.logger != nil {
				r.logger.DebugWithContext(ctx, "Failed to refresh capability index TTL", map[string]interface{}{
					"capability":     capability.Name,
					"capability_key": capKey,
					"error":          err,
					"error_type":     fmt.Sprintf("%T", err),
				})
			}
			// Continue with other indexes even if one fails
		}
	}

	// Refresh name index
	nameKey := fmt.Sprintf("%s:names:%s", r.namespace, info.Name)
	if err := r.client.Expire(ctx, nameKey, r.ttl*2).Err(); err != nil {
		if r.logger != nil {
			r.logger.DebugWithContext(ctx, "Failed to refresh name index TTL", map[string]interface{}{
				"name":       info.Name,
				"name_key":   nameKey,
				"error":      err,
				"error_type": fmt.Sprintf("%T", err),
			})
		}
	}

	// Refresh type index
	typeKey := fmt.Sprintf("%s:types:%s", r.namespace, info.Type)
	if err := r.client.Expire(ctx, typeKey, r.ttl*2).Err(); err != nil {
		if r.logger != nil {
			r.logger.DebugWithContext(ctx, "Failed to refresh type index TTL", map[string]interface{}{
				"type":       info.Type,
				"type_key":   typeKey,
				"error":      err,
				"error_type": fmt.Sprintf("%T", err),
			})
		}
	}

	if r.logger != nil {
		r.logger.DebugWithContext(ctx, "Index set TTL refresh completed", map[string]interface{}{
			"service_id":   info.ID,
			"service_name": info.Name,
			"type":         info.Type,
		})
	}
}

// SetLogger sets the logger for the registry client
// The logger is wrapped with component "framework/core" to identify logs from this module
func (r *RedisRegistry) SetLogger(logger Logger) {
	if logger != nil {
		if cal, ok := logger.(ComponentAwareLogger); ok {
			r.logger = cal.WithComponent("framework/core")
		} else {
			r.logger = logger
		}
	} else {
		r.logger = nil
	}
}

// storeRegistrationState stores service info for potential re-registration (internal helper)
func (r *RedisRegistry) storeRegistrationState(serviceInfo *ServiceInfo) {
	r.stateMutex.Lock()
	defer r.stateMutex.Unlock()

	// Store minimal copy for re-registration
	r.registrationState[serviceInfo.ID] = &ServiceInfo{
		ID:           serviceInfo.ID,
		Name:         serviceInfo.Name,
		Type:         serviceInfo.Type,
		Address:      serviceInfo.Address,
		Port:         serviceInfo.Port,
		Capabilities: append([]Capability{}, serviceInfo.Capabilities...),
		Health:       HealthHealthy,
		Metadata:     copyMetadata(serviceInfo.Metadata),
	}
}

// getStoredRegistrationState retrieves stored service info for re-registration (internal helper)
func (r *RedisRegistry) getStoredRegistrationState(serviceID string) *ServiceInfo {
	r.stateMutex.RLock()
	defer r.stateMutex.RUnlock()

	if info, exists := r.registrationState[serviceID]; exists {
		// Return copy to prevent external modification
		return &ServiceInfo{
			ID:           info.ID,
			Name:         info.Name,
			Type:         info.Type,
			Address:      info.Address,
			Port:         info.Port,
			Capabilities: append([]Capability{}, info.Capabilities...),
			Health:       HealthHealthy,
			Metadata:     copyMetadata(info.Metadata),
		}
	}
	return nil
}

// copyMetadata creates a deep copy of metadata map (internal helper)
func copyMetadata(metadata map[string]interface{}) map[string]interface{} {
	if metadata == nil {
		return nil
	}
	result := make(map[string]interface{})
	for k, v := range metadata {
		result[k] = v
	}
	return result
}

// isServiceNotFoundError checks if error indicates service not found (internal helper)
func (r *RedisRegistry) isServiceNotFoundError(err error) bool {
	if err == nil {
		return false
	}

	// Handle UpdateHealth errors
	if err == redis.Nil {
		return true
	}

	// Handle atomic registration errors
	errorStr := err.Error()
	return strings.Contains(errorStr, "service not found") ||
		strings.Contains(errorStr, "key does not exist")
}

// maintainRegistration provides intelligent heartbeat with self-healing (internal helper)
func (r *RedisRegistry) maintainRegistration(ctx context.Context, serviceID string) {
	// Try lightweight health update first (normal operation)
	err := r.UpdateHealth(ctx, serviceID, HealthHealthy)

	// Update stats and capture values for logging (under lock for thread safety)
	var failureCount int64
	var lastSuccessTime time.Time

	r.heartbeatMutex.Lock()
	stats := r.heartbeatStats[serviceID]
	if stats != nil {
		if err == nil {
			stats.SuccessCount++
			stats.LastSuccess = time.Now()
		} else {
			stats.FailureCount++
			stats.LastFailure = time.Now()
		}
		// Capture values under lock for safe logging later
		failureCount = stats.FailureCount
		lastSuccessTime = stats.LastSuccess
	}
	r.heartbeatMutex.Unlock()

	if err != nil && r.isServiceNotFoundError(err) {
		// Service expired from Redis - attempt re-registration
		if serviceInfo := r.getStoredRegistrationState(serviceID); serviceInfo != nil {
			// Use jittered backoff to prevent thundering herd during recovery
			jitterMs, _ := rand.Int(rand.Reader, big.NewInt(1000))
			jitter := time.Duration(jitterMs.Int64()) * time.Millisecond
			time.Sleep(jitter)

			if regErr := r.Register(ctx, serviceInfo); regErr != nil {
				if r.logger != nil {
					r.logger.ErrorWithContext(ctx, "Failed to re-register service during recovery", map[string]interface{}{
						"service_id":                serviceID,
						"error":                     regErr,
						"will_retry_next_heartbeat": true,
						"total_failures":            failureCount,
					})
				}
			} else {
				if r.logger != nil {
					downtime := time.Duration(0)
					if !lastSuccessTime.IsZero() {
						downtime = time.Since(lastSuccessTime)
					}
					r.logger.InfoWithContext(ctx, "Successfully re-registered service after Redis recovery", map[string]interface{}{
						"service_id":        serviceID,
						"downtime_seconds":  int(downtime.Seconds()),
						"missed_heartbeats": int(downtime.Seconds() / (r.ttl.Seconds() / 2)),
					})
				}
				// Reset failure count after successful recovery
				r.heartbeatMutex.Lock()
				if stats := r.heartbeatStats[serviceID]; stats != nil {
					stats.FailureCount = 0
					stats.LastSuccess = time.Now()
				}
				r.heartbeatMutex.Unlock()
			}
		}
	} else if err != nil && r.logger != nil {
		r.logger.ErrorWithContext(ctx, "Failed to send heartbeat", map[string]interface{}{
			"service_id":     serviceID,
			"error":          err.Error(),
			"total_failures": failureCount,
		})
	}
}

// checkAndLogPeriodicSummary logs heartbeat health every 5 minutes
func (r *RedisRegistry) checkAndLogPeriodicSummary(serviceID string) {
	// Check if it's time to log (read LastSummaryAt under lock)
	r.heartbeatMutex.RLock()
	stats := r.heartbeatStats[serviceID]
	if stats == nil || r.logger == nil {
		r.heartbeatMutex.RUnlock()
		return
	}
	shouldLog := time.Since(stats.LastSummaryAt) >= 5*time.Minute
	r.heartbeatMutex.RUnlock()

	// Log summary if needed
	if shouldLog {
		r.logHeartbeatSummary(serviceID, false)

		// Update LastSummaryAt (re-check stats exists under lock)
		r.heartbeatMutex.Lock()
		if stats := r.heartbeatStats[serviceID]; stats != nil {
			stats.LastSummaryAt = time.Now()
		}
		r.heartbeatMutex.Unlock()
	}
}

// logHeartbeatSummary logs heartbeat statistics
func (r *RedisRegistry) logHeartbeatSummary(serviceID string, isFinal bool) {
	// Capture consistent snapshot of stats under RLock
	var snapshot struct {
		successCount int64
		failureCount int64
		lastSuccess  time.Time
		lastFailure  time.Time
		startedAt    time.Time
	}

	r.heartbeatMutex.RLock()
	stats := r.heartbeatStats[serviceID]
	if stats == nil {
		r.heartbeatMutex.RUnlock()
		return
	}
	// Copy all fields under lock for consistent snapshot
	snapshot.successCount = stats.SuccessCount
	snapshot.failureCount = stats.FailureCount
	snapshot.lastSuccess = stats.LastSuccess
	snapshot.lastFailure = stats.LastFailure
	snapshot.startedAt = stats.StartedAt
	r.heartbeatMutex.RUnlock()

	if r.logger == nil {
		return
	}

	// Get service info for better context (not protected by heartbeatMutex)
	serviceInfo := r.getStoredRegistrationState(serviceID)
	serviceName := "unknown"
	serviceType := "unknown"
	if serviceInfo != nil {
		serviceName = serviceInfo.Name
		serviceType = string(serviceInfo.Type)
	}

	// Calculate metrics from consistent snapshot
	uptime := time.Since(snapshot.startedAt)
	total := snapshot.successCount + snapshot.failureCount
	successRate := float64(0)
	if total > 0 {
		successRate = float64(snapshot.successCount) / float64(total) * 100
	}

	logData := map[string]interface{}{
		"service_id":     serviceID,
		"service_name":   serviceName,
		"service_type":   serviceType,
		"success_count":  snapshot.successCount,
		"failure_count":  snapshot.failureCount,
		"success_rate":   fmt.Sprintf("%.2f%%", successRate),
		"uptime_minutes": int(uptime.Minutes()),
	}

	// Only add timestamps if they're valid
	if !snapshot.lastSuccess.IsZero() {
		logData["time_since_last_success_sec"] = int(time.Since(snapshot.lastSuccess).Seconds())
	}

	if snapshot.failureCount > 0 && !snapshot.lastFailure.IsZero() {
		logData["time_since_last_failure_sec"] = int(time.Since(snapshot.lastFailure).Seconds())
	}

	if isFinal {
		r.logger.Info("Heartbeat final summary (service shutting down)", logData)
	} else {
		r.logger.Info("Heartbeat health summary", logData)
	}
}

// StopHeartbeat gracefully stops the heartbeat goroutine for a specific service.
//
// This method cancels the heartbeat context and removes the service from the
// heartbeat tracking map. It's typically called during:
//   - Service shutdown
//   - Registry replacement (when switching to a new registry instance)
//   - Manual service de-registration
//
// The method is thread-safe and safe to call multiple times for the same service.
// If the service doesn't have an active heartbeat, this is a no-op.
//
// Parameters:
//   - ctx: Context for the operation (currently unused but kept for consistency)
//   - serviceID: Unique identifier of the service whose heartbeat should stop
//
// Example usage:
//
//	registry.StopHeartbeat(ctx, "my-service-123")
func (r *RedisRegistry) StopHeartbeat(ctx context.Context, serviceID string) {
	r.heartbeatsMu.Lock()
	defer r.heartbeatsMu.Unlock()

	if cancel, exists := r.heartbeats[serviceID]; exists {
		cancel() // Cancel the context, stopping the goroutine
		delete(r.heartbeats, serviceID)

		if r.logger != nil {
			r.logger.InfoWithContext(ctx, "Stopped heartbeat", map[string]interface{}{
				"service_id": serviceID,
			})
		}
	}
}

// StartHeartbeat starts a heartbeat goroutine to keep registration alive (enhanced for self-healing).
// If heartbeatInterval is 0, defaults to ttl/2. Minimum is 2 seconds.
// If heartbeatInterval >= TTL, falls back to ttl/2 to prevent expiry.
func (r *RedisRegistry) StartHeartbeat(ctx context.Context, serviceID string, heartbeatInterval time.Duration) {
	// Initialize heartbeat stats
	r.heartbeatMutex.Lock()
	r.heartbeatStats[serviceID] = &HeartbeatStats{
		StartedAt:     time.Now(),
		LastSummaryAt: time.Now(),
	}
	r.heartbeatMutex.Unlock()

	// Create cancellable context for this heartbeat.
	// #nosec G118 -- cancel is retained in r.heartbeats and invoked by StopHeartbeat.
	hbCtx, cancel := context.WithCancel(ctx)

	// Store cancel function for StopHeartbeat
	r.heartbeatsMu.Lock()
	if existingCancel, ok := r.heartbeats[serviceID]; ok {
		existingCancel()
	}
	r.heartbeats[serviceID] = cancel
	r.heartbeatsMu.Unlock()

	// Apply heartbeat interval floor clamps
	const minHeartbeat = 2 * time.Second

	baseInterval := heartbeatInterval
	if baseInterval <= 0 {
		baseInterval = r.ttl / 2 // default
	} else if baseInterval < minHeartbeat {
		baseInterval = minHeartbeat
	}
	// Safety: heartbeat must be shorter than TTL, otherwise the key
	// expires before the next renewal and the tool vanishes
	if baseInterval >= r.ttl {
		baseInterval = r.ttl / 2
	}

	// Log effective heartbeat configuration
	if r.logger != nil {
		logFields := map[string]interface{}{
			"service_id":         serviceID,
			"heartbeat_interval": baseInterval.String(),
			"ttl":                r.ttl.String(),
		}
		if heartbeatInterval > 0 && baseInterval != heartbeatInterval {
			logFields["requested_interval"] = heartbeatInterval.String()
			logFields["reason"] = "clamped"
			r.logger.Warn("Heartbeat interval adjusted", logFields)
		} else {
			r.logger.Info("Heartbeat started", logFields)
		}
	}

	maxJitter := int64(baseInterval.Milliseconds() / 4)
	jitterMs, _ := rand.Int(rand.Reader, big.NewInt(maxJitter))
	jitter := time.Duration(jitterMs.Int64()) * time.Millisecond
	interval := baseInterval + jitter

	ticker := time.NewTicker(interval)
	go func() {
		defer ticker.Stop()
		for {
			select {
			case <-hbCtx.Done():
				// Log final stats on shutdown
				r.logHeartbeatSummary(serviceID, true)
				// Clean up stats
				r.heartbeatMutex.Lock()
				delete(r.heartbeatStats, serviceID)
				r.heartbeatMutex.Unlock()
				return
			case <-ticker.C:
				// Use enhanced maintenance logic with self-healing
				r.maintainRegistration(hbCtx, serviceID)
				// Check if it's time for periodic summary (every 5 minutes)
				r.checkAndLogPeriodicSummary(serviceID)
			}
		}
	}()
}

// StartRegistryRetry initiates background reconnection attempts to Redis.
//
// This function is the entry point for the retry mechanism, enabling services to
// recover from initial Redis connection failures without manual intervention. It
// launches a background goroutine (registryRetryManager) that periodically attempts
// to reconnect and register the service.
//
// This is a package-level function that doesn't require an existing registry instance,
// making it suitable for use during initialization when the registry creation fails.
//
// Key behaviors:
//   - Non-blocking: Returns immediately, retry happens in background
//   - Exponential backoff: Retry interval doubles on each failure (caps at 5 minutes)
//   - Type-aware: Creates RedisDiscovery for agents, RedisRegistry for tools
//   - Self-terminating: Stops automatically on success or context cancellation
//
// Parameters:
//   - ctx: Context for cancellation (when cancelled, retry stops gracefully)
//   - redisURL: Redis connection string (e.g., "redis://localhost:6379")
//   - serviceInfo: Service details for registration (must include Type field)
//   - retryInterval: Initial retry interval (e.g., 30s). Will grow exponentially.
//   - logger: Logger for retry status messages (can be nil)
//   - onSuccess: Callback invoked when retry succeeds (for updating parent's registry ref)
//   - ttl: Registry TTL for service key expiration (0 = default 30s, min 5s)
//   - heartbeatInterval: Heartbeat refresh interval (0 = ttl/2, min 2s, must be < TTL)
//
// Example usage in BaseTool initialization:
//
//	if _, err := NewRedisRegistry(redisURL); err != nil {
//	    StartRegistryRetry(ctx, redisURL, serviceInfo, 30*time.Second, logger,
//	        func(newRegistry Registry) error {
//	            t.mu.Lock()
//	            defer t.mu.Unlock()
//	            t.Registry = newRegistry
//	            return nil
//	        },
//	        cfg.Discovery.TTL,
//	        cfg.Discovery.HeartbeatInterval,
//	    )
//	}
//
// Thread safety: Safe to call from multiple goroutines. Each invocation creates
// an independent retry manager.
func StartRegistryRetry(
	ctx context.Context,
	redisURL string,
	serviceInfo *ServiceInfo,
	retryInterval time.Duration,
	logger Logger,
	onSuccess RegistryUpdateCallback,
	ttl time.Duration,
	heartbeatInterval time.Duration,
) {
	state := &registryRetryState{
		serviceInfo:       serviceInfo,
		currentInterval:   retryInterval,
		onSuccess:         onSuccess,
		ttl:               ttl,
		heartbeatInterval: heartbeatInterval,
	}

	go registryRetryManager(ctx, redisURL, state, logger)
}

// registryRetryManager is the internal goroutine that handles periodic reconnection attempts.
//
// This function implements the core retry logic with exponential backoff. It runs in a
// background goroutine launched by StartRegistryRetry and continues until either:
//  1. Reconnection succeeds and service is registered
//  2. Context is cancelled (e.g., service shutdown)
//
// Retry algorithm:
//   - Attempts reconnection at each timer tick
//   - On failure: Doubles retry interval (e.g., 30s → 60s → 120s → 240s → 300s cap)
//   - On success: Registers service, starts heartbeat, invokes callback, and terminates
//
// Type handling:
//   - ComponentTypeAgent: Creates *RedisDiscovery (implements Discovery interface)
//   - ComponentTypeTool: Creates *RedisRegistry (implements Registry interface)
//   - This ensures the callback receives the correct type for each component
//
// Error handling:
//   - Connection failures trigger backoff and continue retry
//   - Registration failures trigger continue (no backoff increase)
//   - Callback failures are logged but don't prevent heartbeat from running
//
// Logging:
//   - Info: Startup, successful registration
//   - Warn: Connection failures with retry info
//   - Error: Registration failures, callback failures
//   - Debug: Each retry attempt
//
// Parameters:
//   - ctx: Cancellation context (stops retry when cancelled)
//   - redisURL: Redis connection string
//   - state: Retry state including service info, current interval, and callback
//   - logger: Logger for status messages (can be nil)
//
// Internal use only - called by StartRegistryRetry.
func registryRetryManager(
	ctx context.Context,
	redisURL string,
	state *registryRetryState,
	logger Logger,
) {
	ticker := time.NewTicker(state.currentInterval)
	defer ticker.Stop()

	if logger != nil {
		logger.InfoWithContext(ctx, "Background Redis retry started", map[string]interface{}{
			"service_id":     state.serviceInfo.ID,
			"retry_interval": state.currentInterval,
		})
	}

	attempt := 0
	for {
		select {
		case <-ctx.Done():
			if logger != nil {
				logger.InfoWithContext(ctx, "Redis retry manager shutting down", map[string]interface{}{
					"service_id": state.serviceInfo.ID,
				})
			}
			return

		case <-ticker.C:
			attempt++

			if logger != nil {
				logger.DebugWithContext(ctx, "Attempting Redis reconnection", map[string]interface{}{
					"service_id": state.serviceInfo.ID,
					"attempt":    attempt,
				})
			}

			// Handle agents and tools separately due to different interface requirements
			// Agents need RedisDiscovery (implements Discovery interface)
			// Tools need RedisRegistry (implements Registry interface)
			if state.serviceInfo.Type == ComponentTypeAgent {
				// Create RedisDiscovery for agents
				discovery, discoveryErr := NewRedisDiscoveryWithOptions(redisURL, "truvag3", state.ttl)
				if discoveryErr != nil {
					if logger != nil {
						logger.WarnWithContext(ctx, "Redis reconnection failed", map[string]interface{}{
							"service_id": state.serviceInfo.ID,
							"attempt":    attempt,
							"error":      discoveryErr.Error(),
						})
					}

					// Simple exponential backoff (double interval, cap at 5 minutes)
					state.currentInterval = state.currentInterval * 2
					if state.currentInterval > 5*time.Minute {
						state.currentInterval = 5 * time.Minute
					}
					ticker.Reset(state.currentInterval)

					continue
				}

				// Success! Register service
				discovery.SetLogger(logger)
				regErr := discovery.Register(ctx, state.serviceInfo)
				if regErr != nil {
					if logger != nil {
						logger.ErrorWithContext(ctx, "Failed to register after reconnection", map[string]interface{}{
							"service_id": state.serviceInfo.ID,
							"error":      regErr.Error(),
						})
					}
					continue
				}

				// Start heartbeat
				discovery.StartHeartbeat(ctx, state.serviceInfo.ID, state.heartbeatInterval)

				if logger != nil {
					logger.InfoWithContext(ctx, "Successfully registered after background retry", map[string]interface{}{
						"service_id": state.serviceInfo.ID,
						"attempt":    attempt,
					})
				}

				// Call success callback with discovery (implements Registry interface)
				if state.onSuccess != nil {
					if err := state.onSuccess(discovery); err != nil {
						if logger != nil {
							logger.ErrorWithContext(ctx, "Failed to update registry reference", map[string]interface{}{
								"service_id": state.serviceInfo.ID,
								"error":      err.Error(),
							})
						}
					}
				}

				return // Success - terminate goroutine

			} else {
				// Create RedisRegistry for tools
				registry, registryErr := NewRedisRegistryWithOptions(redisURL, "truvag3", state.ttl)
				if registryErr != nil {
					if logger != nil {
						logger.WarnWithContext(ctx, "Redis reconnection failed", map[string]interface{}{
							"service_id": state.serviceInfo.ID,
							"attempt":    attempt,
							"error":      registryErr.Error(),
						})
					}

					// Simple exponential backoff (double interval, cap at 5 minutes)
					state.currentInterval = state.currentInterval * 2
					if state.currentInterval > 5*time.Minute {
						state.currentInterval = 5 * time.Minute
					}
					ticker.Reset(state.currentInterval)

					continue
				}

				// Success! Register service
				registry.SetLogger(logger)
				regErr := registry.Register(ctx, state.serviceInfo)
				if regErr != nil {
					if logger != nil {
						logger.ErrorWithContext(ctx, "Failed to register after reconnection", map[string]interface{}{
							"service_id": state.serviceInfo.ID,
							"error":      regErr.Error(),
						})
					}
					continue
				}

				// Start heartbeat
				registry.StartHeartbeat(ctx, state.serviceInfo.ID, state.heartbeatInterval)

				if logger != nil {
					logger.InfoWithContext(ctx, "Successfully registered after background retry", map[string]interface{}{
						"service_id": state.serviceInfo.ID,
						"attempt":    attempt,
					})
				}

				// Call success callback with registry
				if state.onSuccess != nil {
					if err := state.onSuccess(registry); err != nil {
						if logger != nil {
							logger.ErrorWithContext(ctx, "Failed to update registry reference", map[string]interface{}{
								"service_id": state.serviceInfo.ID,
								"error":      err.Error(),
							})
						}
					}
				}

				return // Success - terminate goroutine
			}
		}
	}
}
