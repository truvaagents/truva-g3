/*
Package telemetry provides production-grade observability for the Truva-G3 framework.

Architecture Overview:

The telemetry package is designed with a three-layer architecture:

1. Simple API Layer - Developer-facing functions (Emit, Counter, Histogram, Gauge)
2. Registry Layer - Thread-safe global registry with lifecycle management
3. Provider Layer - OpenTelemetry integration for actual metric export

Thread Safety:

All public functions in this package are thread-safe and can be called
concurrently from multiple goroutines. The package uses several techniques
to ensure safety:
  - atomic.Value for lock-free reads of the global registry
  - sync.Once for one-time initialization
  - sync.Map for concurrent metric registration
  - sync.Pool for efficient label slice reuse

Performance Considerations:

The package is optimized for high-throughput metric emission:
  - Lock-free fast path for metric emission
  - Bounded cardinality to prevent memory explosion
  - Circuit breaker to protect against backend failures
  - Efficient baggage propagation with size limits
  - Pooled allocations for label slices

Design Principles:

1. Progressive Disclosure - Simple API with advanced features when needed
2. Fail-Safe - Telemetry failures never crash the application
3. Zero-Config - Works with sensible defaults out of the box
4. Production-Ready - Built-in protection against common issues

Usage:

Initialize once in main:

	telemetry.Initialize(telemetry.UseProfile(telemetry.ProfileDevelopment))
	defer telemetry.Shutdown(context.Background())

Then emit metrics from anywhere:

	telemetry.Counter("requests.total", "status", "success")
	telemetry.Histogram("latency.ms", 123.5, "endpoint", "/api")

For distributed tracing:

	ctx = telemetry.WithBaggage(ctx, "request_id", "abc123")
	telemetry.EmitWithContext(ctx, "payment.amount", 99.99)

Safety Features:

  - Cardinality Limiting: Prevents unbounded label combinations
  - Circuit Breaker: Stops sending metrics when backend is down
  - PII Redaction: Can filter sensitive data (when enabled)
  - Rate Limiting: Prevents error log spam
  - Graceful Degradation: Continues operating even with failures

Configuration Profiles:

The package includes three pre-configured profiles:
  - ProfileDevelopment: Full sampling, no limits, fast feedback
  - ProfileStaging: Moderate sampling, safety features enabled
  - ProfileProduction: Low sampling, strict limits, maximum safety
*/
package telemetry
