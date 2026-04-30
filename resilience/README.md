# TruvaG3 Resilience Module

Bulletproof your agents with production-ready fault tolerance patterns.

## Table of Contents

1. [What Does This Module Do?](#1-what-does-this-module-do)
2. [Quick Start](#2-quick-start)
3. [How It Works](#3-how-it-works)
4. [Advanced Configuration](#4-advanced-configuration)
5. [Real-World Examples](#5-real-world-examples)
6. [Monitoring & Observability](#6-monitoring--observability)
7. [Testing Your Resilience](#7-testing-your-resilience)
8. [Common Patterns & Best Practices](#8-common-patterns--best-practices)
9. [Performance Tips](#9-performance-tips)
10. [Debugging Guide](#10-debugging-guide)
11. [Production Checklist](#11-production-checklist)
12. [Pro Tips](#12-pro-tips)
13. [New Production Features](#13-new-production-features)
14. [Summary](#14-summary---what-youve-learned)

## 1. What Does This Module Do?

Think of this module as the **safety equipment for your agents** - like airbags and seatbelts in a car. When things go wrong (and they will), this module ensures your system stays up and running instead of crashing.

It provides battle-tested patterns to handle failures gracefully:

1. **Circuit Breaker** - Stops cascading failures before they bring down your system
2. **Retry Logic** - Automatically retries failed operations with smart backoff
3. **Sliding Window Metrics** - Tracks success/failure rates in real-time

### Real-World Analogy: The Smart Power Strip

Imagine a smart power strip that:
- **Detects problems**: If a device keeps shorting, it cuts power (circuit breaker opens)
- **Tests recovery**: After a cooldown, it allows limited power to test if the problem is fixed (half-open state)
- **Restores service**: If tests succeed, full power resumes (circuit closes)
- **Retries intelligently**: If your phone doesn't charge, it tries again with increasing delays

That's exactly how this module protects your agent communications!

## 2. Quick Start

### Installation

```go
import "github.com/truvaagents/truva-g3/resilience"
```

### Basic Circuit Breaker

Let me show you the simplest way to protect your code from failures. It's like adding a safety valve to a pipe - when pressure gets too high, it automatically shuts off to prevent damage.

```go
// Create a circuit breaker with default production settings
config := resilience.DefaultConfig()
config.Name = "payment-service"
cb := resilience.NewCircuitBreaker(config)

// Wrap any fallible operation
err := cb.Execute(ctx, func() error {
    return callPaymentService()
})

if err != nil {
    if errors.Is(err, core.ErrCircuitBreakerOpen) {
        // Circuit is open - service is down
        return fallbackPaymentMethod()
    }
    // Handle other errors
}
```

### Smart Retry with Backoff

Sometimes a simple retry is all you need. But doing it wrong can make things worse! Here's how to retry intelligently:

```go
// Configure retry behavior
retryConfig := &resilience.RetryConfig{
    MaxAttempts:   3,
    InitialDelay:  100 * time.Millisecond,
    MaxDelay:      5 * time.Second,
    BackoffFactor: 2.0,      // Double delay each time
    JitterEnabled: true,      // Prevent thundering herd
}

// Retry with exponential backoff
err := resilience.Retry(ctx, retryConfig, func() error {
    return fetchDataFromAPI()
})
```

### Combining Circuit Breaker + Retry

Here's where the magic happens - combining both patterns gives you maximum resilience:

```go
// Best of both worlds: retry with circuit breaker protection
err := resilience.RetryWithCircuitBreaker(
    ctx,
    retryConfig,
    cb,
    func() error {
        return riskyOperation()
    },
)
// Note: This helper function internally checks circuit state and records
// success/failure automatically. You don't need to call cb.Execute() yourself.

## 3. How It Works

### The Circuit Breaker State Machine

Understanding the state machine is crucial. Think of it like a traffic light system:

```
        ┌──────────────┐
        │    CLOSED    │ ← Normal operation
        │ (All good!)  │   All requests pass through
        └──────┬───────┘
               │
               │ Error rate > 50%
               │ (configurable)
               ▼
        ┌──────────────┐
        │     OPEN     │ ← Protection mode
        │ (Oh no!)     │   All requests fail fast
        └──────┬───────┘
               │
               │ After 30 seconds
               │ (sleep window)
               ▼
        ┌──────────────┐
        │  HALF-OPEN   │ ← Testing recovery
        │ (Maybe ok?)  │   Limited requests to test
        └──────┬───────┘
               │
               │ Success rate > 60%
               │ (configurable)
               ▼
        Back to CLOSED
```

### Why Three States?

Each state serves a specific purpose in protecting your system:

#### Closed State - "Everything is fine"
```javascript
Request comes in → Execute normally → Track success/failure
If error rate < 50%: Stay closed
If error rate >= 50%: Open the circuit!
```

#### Open State - "Houston, we have a problem"
```javascript
Request comes in → Immediately fail → Don't even try
Why? To give the failing service time to recover
After 30 seconds → Move to half-open
```

#### Half-Open State - "Is it safe yet?"
```javascript
Allow 5 test requests through
If 60% succeed → Close the circuit (all good!)
If not → Back to open (still broken)
```

### Smart Error Classification

Not all errors are equal! The circuit breaker is smart about what counts as a failure:

```go
// These DON'T trip the circuit (user/programming errors):
✅ Configuration errors (user's fault)
✅ Not found errors (404s)
✅ Invalid state errors (programming bugs)
✅ Context cancellation (client gave up)

// These DO trip the circuit (infrastructure problems):
❌ Network timeouts
❌ Connection refused
❌ 500 Internal Server Errors
❌ Database connection failures
```

Why this matters: You don't want to open your circuit because of bad user input. You want to open it when the infrastructure is failing!

### The Sliding Window - Real-Time Metrics

Instead of counting all failures forever, we use a sliding window. Think of it like a conveyor belt of metrics:

```
Time:  [====|====|====|====|====|====|====|====|====|====]
        ↑                                               ↑
     10 min ago                                      Now

Each segment tracks successes/failures
Old segments automatically expire
Gives accurate, real-time error rates
```

This means if your service had problems an hour ago but is fine now, the circuit won't stay open unnecessarily.

## 4. Advanced Configuration

### Production-Ready Circuit Breaker

Here's a fully configured circuit breaker with all the knobs you might need:

```go
config := &resilience.CircuitBreakerConfig{
    // Identity
    Name: "user-service",

    // When to open (choose one approach)
    ErrorThreshold:   0.5,      // Open at 50% error rate
    VolumeThreshold:  10,       // Need 10+ requests to evaluate

    // Recovery timing
    SleepWindow:      30 * time.Second,  // Wait before half-open
    HalfOpenRequests: 5,                  // Test requests in half-open
    SuccessThreshold: 0.6,                // 60% success to close

    // Metrics window
    WindowSize:   60 * time.Second,      // Look at last minute
    BucketCount:  10,                     // 10 buckets of 6 seconds each

    // Customize what counts as failure
    ErrorClassifier: func(err error) bool {
        // Only infrastructure errors count
        // You can customize this based on your needs
        return resilience.DefaultErrorClassifier(err)
    },

    // Observability
    Logger:  myLogger,
    Metrics: prometheusCollector,
}

cb := resilience.NewCircuitBreaker(config)
```

### Retry Strategies

Different operations need different retry strategies. Here are three common patterns:

```go
// Conservative retry (for critical operations)
conservative := &resilience.RetryConfig{
    MaxAttempts:   2,
    InitialDelay:  1 * time.Second,
    MaxDelay:      5 * time.Second,
    BackoffFactor: 2.0,
    JitterEnabled: false,  // Predictable timing
}

// Aggressive retry (for eventual consistency)
aggressive := &resilience.RetryConfig{
    MaxAttempts:   5,
    InitialDelay:  50 * time.Millisecond,
    MaxDelay:      10 * time.Second,
    BackoffFactor: 3.0,
    JitterEnabled: true,  // Prevent thundering herd
}

// No retry (for user-facing operations)
noRetry := &resilience.RetryConfig{
    MaxAttempts: 1,
}
```

## 5. Real-World Examples

### Example 1: Protecting External API Calls

Here's a complete example of protecting weather API calls with fallback to cache:

```go
// Define the Weather type
type Weather struct {
    Temperature float64
    Humidity    float64
    Description string
}

type WeatherService struct {
    cb    *resilience.CircuitBreaker
    rc    *resilience.RetryConfig
    cache map[string]*Weather // Simple cache
}

func NewWeatherService() *WeatherService {
    // Circuit breaker for the weather API
    config := resilience.DefaultConfig()
    config.Name = "weather-api"
    config.ErrorThreshold = 0.3    // Open at 30% errors
    config.SleepWindow = 1 * time.Minute

    return &WeatherService{
        cb:    resilience.NewCircuitBreaker(config),
        rc:    resilience.DefaultRetryConfig(),
        cache: make(map[string]*Weather),
    }
}

func (w *WeatherService) GetWeather(city string) (*Weather, error) {
    var result *Weather

    err := resilience.RetryWithCircuitBreaker(
        context.Background(),
        w.rc,
        w.cb,
        func() error {
            resp, err := http.Get(fmt.Sprintf("https://api.weather.com/%s", city))
            if err != nil {
                return err
            }
            defer resp.Body.Close()

            // 5xx errors are retryable
            if resp.StatusCode >= 500 {
                return fmt.Errorf("server error: %d", resp.StatusCode)
            }

            // 4xx errors are not (don't waste retries on bad requests)
            if resp.StatusCode >= 400 {
                return fmt.Errorf("client error: %d", resp.StatusCode)
            }

            result = &Weather{} // Create result
            err = json.NewDecoder(resp.Body).Decode(&result)
            if err == nil {
                // Cache successful result
                w.cache[city] = result
            }
            return err
        },
    )

    if err != nil {
        // Fallback to cached data
        if cached, ok := w.cache[city]; ok {
            return cached, nil
        }
        return nil, err
    }

    return result, nil
}
```

### Example 2: Database Connection Pool

Protecting your database from overload:

```go
type ResilientDB struct {
    pool     *sql.DB
    cb       *resilience.CircuitBreaker
    replica  *sql.DB // Read replica for fallback
}

func NewResilientDB(dsn, replicaDSN string) (*ResilientDB, error) {
    pool, err := sql.Open("postgres", dsn)
    if err != nil {
        return nil, err
    }

    replica, err := sql.Open("postgres", replicaDSN)
    if err != nil {
        return nil, err
    }

    // Configure circuit breaker for database
    config := resilience.DefaultConfig()
    config.Name = "postgres-main"
    config.ErrorThreshold = 0.2     // Very sensitive - open at 20% errors
    config.VolumeThreshold = 5       // Quick detection
    config.SleepWindow = 10 * time.Second  // Fast recovery attempts

    return &ResilientDB{
        pool:    pool,
        cb:      resilience.NewCircuitBreaker(config),
        replica: replica,
    }, nil
}

func (db *ResilientDB) Query(ctx context.Context, query string, args ...interface{}) (*sql.Rows, error) {
    var rows *sql.Rows

    err := db.cb.Execute(ctx, func() error {
        var err error
        rows, err = db.pool.QueryContext(ctx, query, args...)
        return err
    })

    if err != nil {
        if errors.Is(err, core.ErrCircuitBreakerOpen) {
            // Database is down - use read replica
            return db.replica.QueryContext(ctx, query, args...)
        }
        return nil, err
    }

    return rows, nil
}
```

### Example 3: Microservice Communication

Orchestrating multiple services with independent circuit breakers:

```go
// Define the Order type
type Order struct {
    Items   []string
    Payment PaymentInfo
}

type PaymentInfo struct {
    Method string
    Amount float64
}

type OrderService struct {
    inventoryCB *resilience.CircuitBreaker
    paymentCB   *resilience.CircuitBreaker
    shippingCB  *resilience.CircuitBreaker
    retry       *resilience.RetryConfig
}

func NewOrderService() *OrderService {
    // Each service gets its own circuit breaker
    inventoryConfig := resilience.DefaultConfig()
    inventoryConfig.Name = "inventory-service"

    paymentConfig := resilience.DefaultConfig()
    paymentConfig.Name = "payment-service"
    paymentConfig.ErrorThreshold = 0.3 // More sensitive for payments

    shippingConfig := resilience.DefaultConfig()
    shippingConfig.Name = "shipping-service"

    return &OrderService{
        inventoryCB: resilience.NewCircuitBreaker(inventoryConfig),
        paymentCB:   resilience.NewCircuitBreaker(paymentConfig),
        shippingCB:  resilience.NewCircuitBreaker(shippingConfig),
        retry:       resilience.DefaultRetryConfig(),
    }
}

func (s *OrderService) ProcessOrder(ctx context.Context, order Order) error {
    // Step 1: Check inventory (critical - must succeed)
    err := resilience.RetryWithCircuitBreaker(
        ctx, s.retry, s.inventoryCB,
        func() error {
            return s.checkInventory(order.Items)
        },
    )
    if err != nil {
        return fmt.Errorf("inventory check failed: %w", err)
    }

    // Step 2: Process payment (critical - must succeed)
    err = resilience.RetryWithCircuitBreaker(
        ctx, s.retry, s.paymentCB,
        func() error {
            return s.processPayment(order.Payment)
        },
    )
    if err != nil {
        // Rollback inventory
        s.releaseInventory(order.Items)
        return fmt.Errorf("payment failed: %w", err)
    }

    // Step 3: Schedule shipping (can be async/eventual)
    go func() {
        resilience.RetryWithCircuitBreaker(
            context.Background(),
            &resilience.RetryConfig{
                MaxAttempts: 10,  // Keep trying
                MaxDelay: 1 * time.Hour,
            },
            s.shippingCB,
            func() error {
                return s.scheduleShipping(order)
            },
        )
    }()

    return nil
}

// Helper methods (implement based on your actual services)
func (s *OrderService) checkInventory(items []string) error {
    // Implementation here
    return nil
}

func (s *OrderService) processPayment(payment PaymentInfo) error {
    // Implementation here
    return nil
}

func (s *OrderService) releaseInventory(items []string) {
    // Implementation here
}

func (s *OrderService) scheduleShipping(order Order) error {
    // Implementation here
    return nil
}
```

## 6. Monitoring & Observability

### Built-in Metrics Interface

The circuit breaker provides a metrics interface you can implement for any monitoring system:

```go
type MyMetricsCollector struct {
    // Your metrics backend here
}

func (m *MyMetricsCollector) RecordSuccess(name string) {
    // Record success metric
}

func (m *MyMetricsCollector) RecordFailure(name string, errorType string) {
    // Record failure with error classification
}

func (m *MyMetricsCollector) RecordStateChange(name string, from, to string) {
    // Record state transitions for alerting
}

func (m *MyMetricsCollector) RecordRejection(name string) {
    // Record when requests are rejected (circuit open)
}

// Use it
config.Metrics = &MyMetricsCollector{}
```

### Logging Integration

The circuit breaker integrates with the TruvaG3 logging system:

```go
// The circuit breaker uses core.Logger interface
config.Logger = yourLogger // Any implementation of core.Logger

// You'll see logs like:
// INFO: Circuit breaker state changed {"name": "payment-service", "from": "closed", "to": "open"}
// WARN: Circuit breaker rejecting requests {"name": "payment-service", "state": "open"}
// INFO: Circuit breaker recovery attempt {"name": "payment-service", "state": "half-open"}
```

### Dashboard Queries

Here are some useful queries for monitoring your circuit breakers:

```sql
-- Circuit breaker state changes
SELECT
    name,
    from_state,
    to_state,
    count(*) as transitions,
    max(timestamp) as last_transition
FROM circuit_breaker_events
WHERE timestamp > NOW() - INTERVAL '1 hour'
GROUP BY name, from_state, to_state;

-- Error rates by service
SELECT
    service_name,
    SUM(CASE WHEN status = 'success' THEN 1 ELSE 0 END) as successes,
    SUM(CASE WHEN status = 'failure' THEN 1 ELSE 0 END) as failures,
    ROUND(100.0 * SUM(CASE WHEN status = 'failure' THEN 1 ELSE 0 END) / COUNT(*), 2) as error_rate
FROM service_calls
WHERE timestamp > NOW() - INTERVAL '5 minutes'
GROUP BY service_name
ORDER BY error_rate DESC;

-- Services in non-closed state (potential issues)
SELECT
    name,
    state,
    last_state_change,
    error_rate,
    total_requests
FROM circuit_breaker_status
WHERE state != 'closed'
ORDER BY last_state_change DESC;
```

## 7. Testing Your Resilience

### Unit Testing Circuit Breakers

Here's how to test that your circuit breaker opens correctly:

```go
func TestCircuitBreakerOpensOnFailures(t *testing.T) {
    config := resilience.DefaultConfig()
    config.ErrorThreshold = 0.5
    config.VolumeThreshold = 4
    cb := resilience.NewCircuitBreaker(config)

    // Simulate failures
    for i := 0; i < 5; i++ {
        cb.Execute(context.Background(), func() error {
            return errors.New("failure")
        })
    }

    // Circuit should be open
    err := cb.Execute(context.Background(), func() error {
        return nil
    })

    assert.ErrorIs(t, err, core.ErrCircuitBreakerOpen)
}

func TestCircuitBreakerRecovery(t *testing.T) {
    config := resilience.DefaultConfig()
    config.SleepWindow = 100 * time.Millisecond // Short for testing
    cb := resilience.NewCircuitBreaker(config)

    // Open the circuit
    for i := 0; i < 10; i++ {
        cb.Execute(context.Background(), func() error {
            return errors.New("failure")
        })
    }

    // Wait for half-open
    time.Sleep(150 * time.Millisecond)

    // Should allow limited requests in half-open
    err := cb.Execute(context.Background(), func() error {
        return nil // Success
    })

    assert.NoError(t, err)
}
```

### Integration Testing

Test your resilient services with a flaky backend:

```go
func TestResilientServiceIntegration(t *testing.T) {
    // Start a flaky test server
    callCount := 0
    server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        callCount++
        if callCount < 3 {
            // Fail first 2 attempts
            w.WriteHeader(500)
            return
        }
        w.WriteHeader(200)
        json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
    }))
    defer server.Close()

    // Create resilient client with retry
    retryConfig := &resilience.RetryConfig{
        MaxAttempts:  3,
        InitialDelay: 10 * time.Millisecond,
    }

    var result map[string]string
    err := resilience.Retry(context.Background(), retryConfig, func() error {
        resp, err := http.Get(server.URL)
        if err != nil {
            return err
        }
        defer resp.Body.Close()

        if resp.StatusCode >= 500 {
            return fmt.Errorf("server error: %d", resp.StatusCode)
        }

        return json.NewDecoder(resp.Body).Decode(&result)
    })

    assert.NoError(t, err)
    assert.Equal(t, "ok", result["status"])
    assert.Equal(t, 3, callCount) // Verify it retried
}
```

### Chaos Testing

Inject failures to test your resilience in production-like conditions:

```go
import (
    "math/rand"
    "net/http"
    "encoding/json"
    "github.com/truvaagents/truva-g3/resilience"
)

// Chaos middleware for testing
type ChaosMiddleware struct {
    failureRate float64
    cb          *resilience.CircuitBreaker
}

func (c *ChaosMiddleware) Handle(next http.Handler) http.Handler {
    return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        err := c.cb.Execute(r.Context(), func() error {
            // Randomly inject failures
            if rand.Float64() < c.failureRate {
                return errors.New("chaos injection")
            }

            // Otherwise proceed normally
            next.ServeHTTP(w, r)
            return nil
        })

        if err != nil {
            w.WriteHeader(503)
            json.NewEncoder(w).Encode(map[string]string{
                "error": "service unavailable",
                "circuit_state": c.cb.GetState(),
            })
        }
    })
}

// Use in tests or staging
func TestWithChaos(t *testing.T) {
    cb := resilience.NewCircuitBreaker(resilience.DefaultConfig())
    chaos := &ChaosMiddleware{
        failureRate: 0.3, // 30% failure rate
        cb:          cb,
    }

    handler := chaos.Handle(yourHandler)
    // Run your tests with chaos
}
```

## 8. Common Patterns & Best Practices

### Pattern 1: Service Degradation

When things go wrong, degrade gracefully instead of failing completely:

```go
type SearchService struct {
    primaryCB *resilience.CircuitBreaker
    backupCB  *resilience.CircuitBreaker
    cache     map[string][]Result
}

type Result struct {
    ID    string
    Title string
    Score float64
}

func (s *SearchService) Search(ctx context.Context, query string) ([]Result, error) {
    // Try primary search service
    var results []Result
    err := s.primaryCB.Execute(ctx, func() error {
        var err error
        results, err = s.primarySearch(query)
        if err == nil {
            // Cache successful results
            s.cache[query] = results
        }
        return err
    })

    if err == nil {
        return results, nil
    }

    // If circuit is open, degrade gracefully
    if errors.Is(err, core.ErrCircuitBreakerOpen) {
        // Try backup service
        err = s.backupCB.Execute(ctx, func() error {
            var err error
            results, err = s.backupSearch(query)
            return err
        })

        if err == nil {
            return results, nil
        }

        // Last resort: return cached results
        if cached, ok := s.cache[query]; ok {
            return cached, nil
        }
    }

    return nil, err
}

func (s *SearchService) primarySearch(query string) ([]Result, error) {
    // Implementation
    return nil, nil
}

func (s *SearchService) backupSearch(query string) ([]Result, error) {
    // Implementation
    return nil, nil
}
```

### Pattern 2: Bulkhead Isolation

Isolate different operations to prevent one failure from affecting everything:

```go
// Isolate different operations with separate circuit breakers
type APIGateway struct {
    readCB   *resilience.CircuitBreaker  // For GET requests
    writeCB  *resilience.CircuitBreaker  // For POST/PUT/DELETE
    adminCB  *resilience.CircuitBreaker  // For admin operations
}

func NewAPIGateway() *APIGateway {
    // Different configs for different operations
    readConfig := resilience.DefaultConfig()
    readConfig.Name = "api-read"
    readConfig.ErrorThreshold = 0.5  // Tolerate more errors for reads

    writeConfig := resilience.DefaultConfig()
    writeConfig.Name = "api-write"
    writeConfig.ErrorThreshold = 0.2  // Very sensitive for writes

    adminConfig := resilience.DefaultConfig()
    adminConfig.Name = "api-admin"
    adminConfig.ErrorThreshold = 0.1  // Ultra sensitive for admin

    return &APIGateway{
        readCB:  resilience.NewCircuitBreaker(readConfig),
        writeCB: resilience.NewCircuitBreaker(writeConfig),
        adminCB: resilience.NewCircuitBreaker(adminConfig),
    }
}

func (g *APIGateway) HandleRequest(r *http.Request) error {
    var cb *resilience.CircuitBreaker

    // Choose circuit breaker based on operation
    switch r.Method {
    case "GET":
        cb = g.readCB
    case "POST", "PUT", "DELETE":
        cb = g.writeCB
    default:
        cb = g.adminCB
    }

    return cb.Execute(r.Context(), func() error {
        return g.forwardRequest(r)
    })
}

func (g *APIGateway) forwardRequest(r *http.Request) error {
    // Implementation
    return nil
}
```

### Pattern 3: Cascading Timeouts

Properly manage timeouts in a chain of service calls:

```go
type Service struct {
    serviceACB *resilience.CircuitBreaker
    serviceBCB *resilience.CircuitBreaker
    serviceCCB *resilience.CircuitBreaker
}

func (s *Service) CallChain(ctx context.Context) error {
    // Total timeout for entire chain
    ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
    defer cancel()

    // Service A gets 40% of total time
    ctxA, cancelA := context.WithTimeout(ctx, 4*time.Second)
    defer cancelA()

    err := s.serviceACB.Execute(ctxA, func() error {
        return s.callServiceA(ctxA)
    })
    if err != nil {
        return fmt.Errorf("service A failed: %w", err)
    }

    // Service B gets 30% of remaining time
    ctxB, cancelB := context.WithTimeout(ctx, 3*time.Second)
    defer cancelB()

    err = s.serviceBCB.Execute(ctxB, func() error {
        return s.callServiceB(ctxB)
    })
    if err != nil {
        return fmt.Errorf("service B failed: %w", err)
    }

    // Service C gets the rest
    return s.serviceCCB.Execute(ctx, func() error {
        return s.callServiceC(ctx)
    })
}

func (s *Service) callServiceA(ctx context.Context) error {
    // Implementation
    return nil
}

func (s *Service) callServiceB(ctx context.Context) error {
    // Implementation
    return nil
}

func (s *Service) callServiceC(ctx context.Context) error {
    // Implementation
    return nil
}
```

## 9. Performance Tips

### 1. **Tune Your Thresholds**

Different services need different sensitivity:

```go
// For stable, reliable services
config.ErrorThreshold = 0.5    // 50% errors
config.VolumeThreshold = 20    // Need good sample size

// For flaky or less critical services
config.ErrorThreshold = 0.8    // 80% errors (more tolerant)
config.VolumeThreshold = 5     // Quick detection

// For critical services (payment, auth)
config.ErrorThreshold = 0.2    // 20% errors (very sensitive)
config.VolumeThreshold = 10    // Balanced sample size
```

### 2. **Use Force Controls in Emergencies**

Sometimes you need manual override:

```go
// Emergency controls
cb.ForceOpen()   // Immediately stop all traffic
cb.ForceClosed() // Override and allow all traffic
cb.Reset()       // Clear state and start fresh

// Use case: Deployment or maintenance
func performMaintenance(cb *resilience.CircuitBreaker) {
    // Force open during maintenance
    cb.ForceOpen()
    defer cb.Reset() // Reset after maintenance

    // Do maintenance work
}
```

### 3. **Optimize Window Size**

The sliding window size affects accuracy vs memory:

```go
// For high-traffic services (1000+ req/sec)
config.WindowSize = 10 * time.Second  // Short window
config.BucketCount = 10               // Fine granularity

// For low-traffic services (<10 req/sec)
config.WindowSize = 5 * time.Minute   // Longer window
config.BucketCount = 5                // Coarse granularity

// For bursty traffic
config.WindowSize = 30 * time.Second  // Medium window
config.BucketCount = 6                // 5-second buckets
```

## 10. Debugging Guide

### When Circuit Won't Close

If your circuit stays open longer than expected:

```go
// Check the metrics (GetMetrics returns map[string]interface{})
metrics := cb.GetMetrics()
fmt.Printf("Current state: %v\n", metrics["state"])
fmt.Printf("Error rate: %.2f%%\n", metrics["error_rate"].(float64) * 100)
fmt.Printf("Total requests: %v\n", metrics["total"])

// Check half-open success rate if in half-open state
if metrics["state"] == "half-open" {
    // These fields are only present in half-open state
    if successes, ok := metrics["half_open_successes"]; ok {
        failures := metrics["half_open_failures"].(int32)
        successCount := successes.(int32)
        total := successCount + failures
        if total > 0 {
            rate := float64(successCount) / float64(total)
            fmt.Printf("Half-open success rate: %.2f%%\n", rate * 100)
            fmt.Printf("Required rate: %.2f%%\n", config.SuccessThreshold * 100)
        }
    }
}

// Force close if needed (with proper authorization)
if manualOverrideAuthorized {
    cb.ForceClosed()
}
```

### When Circuit Opens Too Often

If your circuit is too sensitive:

```go
// Option 1: Adjust sensitivity
config.ErrorThreshold = 0.7     // Increase from 0.5
config.VolumeThreshold = 30     // Increase from 10

// Option 2: Use custom error classifier
config.ErrorClassifier = func(err error) bool {
    // Ignore specific errors that shouldn't count
    if strings.Contains(err.Error(), "rate limit") {
        return false  // Don't count rate limits
    }
    if strings.Contains(err.Error(), "cancelled") {
        return false  // Don't count cancellations
    }
    // Use default classification for everything else
    return resilience.DefaultErrorClassifier(err)
}

// Option 3: Increase sleep window for stability
config.SleepWindow = 1 * time.Minute  // Increase from 30s
```

### Tracking Problem Services

Set up monitoring to identify problematic services:

```go
import (
    "time"
    "fmt"
    "log"
)

// Add detailed logging
config.Logger = yourLogger // Will log all state changes

// Monitor metrics programmatically
go func() {
    ticker := time.NewTicker(1 * time.Minute)
    defer ticker.Stop()
    for range ticker.C {
        metrics := cb.GetMetrics()
        if errorRate, ok := metrics["error_rate"].(float64); ok && errorRate > 0.3 {
            alert.Send(fmt.Sprintf("High error rate for %s: %.2f%%",
                config.Name, errorRate * 100))
        }
    }
}()

// Track state changes
prevState := cb.GetState()
go func() {
    ticker := time.NewTicker(5 * time.Second)
    defer ticker.Stop()
    for range ticker.C {
        currentState := cb.GetState()
        if currentState != prevState {
            log.Printf("Circuit %s changed: %s -> %s",
                config.Name, prevState, currentState)
            prevState = currentState
        }
    }
}()
```

## 11. Production Checklist

Before deploying to production, make sure you've:

- [ ] **Configure error thresholds** based on service SLA
- [ ] **Set appropriate timeouts** for your use case
- [ ] **Add monitoring** for circuit state changes
- [ ] **Test failure scenarios** in staging
- [ ] **Document fallback strategies** for when circuits open
- [ ] **Configure alerts** for prolonged open states
- [ ] **Review retry strategies** to avoid overwhelming services
- [ ] **Set up dashboards** for error rates and circuit states
- [ ] **Train your team** on manual override procedures
- [ ] **Document** which services use which circuit breakers

## 12. Pro Tips

### Tip 1: Different Configs for Different Environments

Use environment-aware configuration:

```go
func getCircuitBreakerConfig() *resilience.CircuitBreakerConfig {
    base := resilience.DefaultConfig()

    env := os.Getenv("APP_ENV")
    if env == "" {
        env = "development"
    }

    switch env {
    case "development":
        base.ErrorThreshold = 0.8      // More tolerant
        base.SleepWindow = 5 * time.Second  // Quick recovery
        // Use your own logger implementation here
        // base.Logger = myDevLogger
    case "staging":
        base.ErrorThreshold = 0.6
        base.SleepWindow = 15 * time.Second
    case "production":
        base.ErrorThreshold = 0.5      // Strict
        base.SleepWindow = 30 * time.Second  // Careful recovery
        // Use production logger and metrics
    }

    return base
}
```

### Tip 2: Service-Specific Strategies

Tailor your resilience to each service's characteristics:

```go
// Payment service: Conservative
paymentCB := resilience.NewCircuitBreaker(&resilience.CircuitBreakerConfig{
    Name: "payment-service",
    ErrorThreshold: 0.3,  // Very sensitive
    SleepWindow: 1 * time.Minute,  // Long recovery
    HalfOpenRequests: 1,  // Single test request
})

// Search service: Aggressive
searchCB := resilience.NewCircuitBreaker(&resilience.CircuitBreakerConfig{
    Name: "search-service",
    ErrorThreshold: 0.7,  // Tolerant
    SleepWindow: 5 * time.Second,  // Quick recovery
    HalfOpenRequests: 10,  // Multiple test requests
})

// Analytics service: Relaxed
analyticsCB := resilience.NewCircuitBreaker(&resilience.CircuitBreakerConfig{
    Name: "analytics-service",
    ErrorThreshold: 0.9,  // Very tolerant
    SleepWindow: 1 * time.Second,  // Instant recovery
    HalfOpenRequests: 20,  // Many test requests
})
```

### Tip 3: Combine with Other Patterns

Layer your defenses for maximum resilience:

```go
import (
    "golang.org/x/time/rate"
    "github.com/truvaagents/truva-g3/resilience"
)

// Rate limiter + Circuit breaker + Retry
type ResilientClient struct {
    rateLimiter    *rate.Limiter
    circuitBreaker *resilience.CircuitBreaker
    retryConfig    *resilience.RetryConfig
}

func (c *ResilientClient) Call(ctx context.Context, request interface{}) error {
    // First: Check rate limit
    if !c.rateLimiter.Allow() {
        return errors.New("rate limited")
    }

    // Second: Apply circuit breaker with retry
    return resilience.RetryWithCircuitBreaker(
        ctx,
        c.retryConfig,
        c.circuitBreaker,
        func() error {
            // Third: Actual call with timeout
            ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
            defer cancel()
            return c.doCall(ctx, request)
        },
    )
}

func (c *ResilientClient) doCall(ctx context.Context, request interface{}) error {
    // Your actual implementation
    return nil
}
```

## 13. New Production Features

### Enhanced Circuit Breaker Capabilities

Our circuit breaker has been battle-tested and enhanced with production learnings:

- **Configurable Thresholds**: Fine-tune failure detection with custom error rates and request volume thresholds
- **Advanced Error Categorization**: Smart classification of errors (network, timeout, server, client)
- **Production Logging**: Comprehensive logging with structured fields for observability
- **Metrics Integration**: Built-in metrics for monitoring circuit breaker state transitions

### Improved Retry Mechanisms

Smart retry that learns from failures:

- **Exponential Backoff with Jitter**: Prevents thundering herd problems in distributed systems
- **Context-Aware Cancellation**: Respects context deadlines and cancellations
- **Configurable Retry Policies**: Custom retry strategies per operation type
- **Smart Delay Capping**: Maximum delay limits to prevent infinite waits

### Panic Recovery System

Because panics happen in production - and when they do, they shouldn't take down your service:

#### The Problem with Panics in Goroutines

When you execute code in a circuit breaker, it runs in a goroutine for timeout support. Without proper recovery:
- **Goroutine dies silently** - No crash, but the operation never completes
- **Deadlocks without timeout** - The code waits forever for a response that never comes
- **Misleading errors with timeout** - You see "timeout" errors when the real issue was a panic
- **Resource leaks** - Execution tokens and metrics are never cleaned up

#### The Solution: Automatic Panic Recovery

The circuit breaker automatically recovers from ALL panics and converts them to proper errors:

```go
// This won't crash your service or cause deadlocks
err := cb.Execute(ctx, func() error {
    // Even if this panics...
    data := someRiskyOperation()
    if data == nil {
        panic("unexpected nil data")  // Converted to error
    }
    return processData(data)
})

if err != nil {
    // You'll get: "panic in circuit breaker: unexpected nil data\nStack:..."
    // Not: deadlock or "context deadline exceeded"
    log.Printf("Operation failed: %v", err)
}
```

#### Features of Panic Recovery:

- **Automatic Recovery**: All panics are caught and converted to errors
- **Preserves Stack Traces**: Full stack trace included for debugging
- **Proper Cleanup**: Tokens released, metrics updated, state managed correctly
- **Type-Aware**: Handles string, error, nil, and any other panic types
- **No Deadlocks**: Returns immediately with error instead of hanging
- **Metrics Integration**: Panics count as failures for circuit breaker decisions

#### Real-World Example:

```go
// Third-party libraries or database drivers might panic
err := cb.Execute(ctx, func() error {
    // This sketchy third-party library sometimes panics
    result := sketchy.DoSomething()

    // Or nil pointer dereferences in your code
    return result.Process() // If result is nil, this panics
})

// Instead of crashing, you get a proper error with stack trace
if err != nil {
    if strings.Contains(err.Error(), "panic") {
        // Log the panic for investigation
        logger.Error("Caught panic in operation", map[string]interface{}{
            "error": err.Error(),
            "operation": "sketchy-operation",
        })
        // Use fallback logic
        return handleWithFallback()
    }
    return err
}
```

This feature has been thoroughly tested with:
- Different panic types (string, error, nil, custom types)
- Concurrent panic scenarios
- Panics during state transitions
- Half-open state panics
- No goroutine leaks verified

### Production Logging Enhancements

All resilience components now include production-grade logging:

```go
// Example: Circuit breaker logs
// State changes
logger.Warn("Circuit breaker state changed", map[string]interface{}{
    "name": "payment-service",
    "from_state": "closed",
    "to_state": "open",
    "error_rate": 0.65,
    "consecutive_failures": 10,
})

// Retry attempts
logger.Info("Retrying operation", map[string]interface{}{
    "operation": "fetch-user-data",
    "attempt": 3,
    "delay_ms": 1500,
    "error": "connection timeout",
})

// Panic recovery
logger.Error("Recovered from panic", map[string]interface{}{
    "service": "order-processor",
    "panic": "nil pointer dereference",
    "stack_trace": "...",
})
```

## 14. Summary - What You've Learned

### This Module Gives You Three Superpowers

#### 1. **Circuit Breaker** - The Guardian
Your first line of defense against cascading failures:
- Prevents cascading failures before they spread
- Three intelligent states (closed, open, half-open)
- Self-healing with automatic recovery testing
- Smart error classification (infrastructure vs user errors)

#### 2. **Retry Logic** - The Persistent Helper
Never give up (but know when to stop):
- Exponential backoff prevents overwhelming failed services
- Jitter prevents thundering herd problems
- Context-aware cancellation respects deadlines
- Maximum delay caps prevent infinite waits

#### 3. **Sliding Window** - The Analyst
Real-time metrics that adapt to your traffic:
- Real-time success/failure tracking
- Time-skew protection (old failures don't haunt you)
- Configurable granularity for different traffic patterns
- Automatic bucket rotation for memory efficiency

### Quick Decision Guide

**Use Circuit Breaker when:**
- Calling external services (APIs, databases)
- Protecting critical resources
- Preventing cascade failures
- Need fast failure detection

**Use Retry when:**
- Handling transient failures (network blips)
- Eventually consistent systems
- Idempotent operations
- Need to tolerate temporary issues

**Use Both when:**
- Mission-critical operations
- Payment processing
- High-availability requirements
- Production distributed systems

### The Power of Resilience

Remember: **It's not about preventing failures, it's about handling them gracefully.**

Your services WILL fail. The network WILL have issues. Databases WILL go down. Third-party APIs WILL be flaky. But with this module, your system keeps running anyway. Your users stay happy. Your pages stay up. Your business keeps operating.

That's the power of resilience - turning inevitable failures into minor inconveniences instead of major disasters.

---

**🎉 Congratulations!** You now have production-grade resilience patterns at your fingertips. Your agents can handle anything the internet throws at them. Go forth and build bulletproof systems!