# TruvaG3 Agent Framework - Capabilities Guide

## Overview

The TruvaG3 Agent Framework enables autonomous agents to discover, communicate, and collaborate in Kubernetes environments. This guide details the core capabilities including service discovery, inter-agent communication, distributed tracing, and orchestration features.

## Table of Contents

- [Agent Discovery in Kubernetes](#agent-discovery-in-kubernetes)
- [Inter-Agent Communication](#inter-agent-communication)
- [Observability & Telemetry](#observability--telemetry)
- [Resilience & Fault Tolerance](#resilience--fault-tolerance)
- [Orchestration & Routing](#orchestration--routing)

## Agent Discovery in Kubernetes

### Service Registration

The framework uses **Redis-based service discovery** with Kubernetes-aware features to enable agents to find and connect with each other dynamically.

#### Registration Process
- Agents automatically register with Redis upon startup using namespace-scoped keys
- Registration includes Kubernetes service metadata:
  - `ServiceName`: The K8s service name
  - `Namespace`: The K8s namespace
  - `ServiceEndpoint`: Full service endpoint
- Heartbeat-based health monitoring with configurable TTL (default: 60 seconds)
- Capability-based indexing for efficient agent discovery

**Key Files:**
- `pkg/discovery/redis.go:169-247` - Registration implementation
- `pkg/discovery/interfaces.go:27-47` - AgentRegistration structure

#### Kubernetes Service Resolution

Agents can be discovered through multiple mechanisms:

1. **Service-level discovery** (recommended for K8s):
   - Format: `<service-name>.<namespace>.svc.cluster.local:port`
   - Enables load balancing and service mesh integration
   - Supports both ClusterIP and NodePort services

2. **Pod-level discovery**:
   - Direct pod-to-pod communication
   - Useful for stateful agents or debugging

**Configuration Example:**
```go
framework.RunAgent(agent,
    framework.WithAgentName("calculator-agent"),
    framework.WithServiceName("calculator-service"),
    framework.WithNamespace("agents"),
    framework.WithRedisURL("redis://redis.default.svc.cluster.local:6379"),
)
```

### Discovery Mechanisms

#### 1. Capability-Based Discovery
Find agents based on what they can do:
```go
agents, err := discovery.FindCapability(ctx, "calculate")
```
- Returns all agents with the specified capability
- Supports fallback to local cache when Redis is unavailable

#### 2. Direct Agent Lookup
Find a specific agent by ID:
```go
agent, err := discovery.FindAgent(ctx, "calculator-agent-xyz")
```

#### 3. Full Catalog Synchronization
Download complete agent registry for intelligent routing:
```go
catalog := discovery.GetCatalogForLLM()
```
- Returns LLM-optimized catalog with descriptions and examples
- Periodic background synchronization (configurable interval)
- Used by autonomous routing mode

#### 4. Local Caching Strategy
- In-memory cache of agent registrations
- Automatic cache refresh with exponential backoff
- Circuit breaker pattern for Redis failures
- Optional persistence to disk for recovery

**Key Files:**
- `pkg/discovery/redis.go:250-301` - Capability-based discovery
- `pkg/discovery/redis.go:767-841` - Catalog management

## Inter-Agent Communication

### Natural Language Interface

Agents communicate using natural language instructions, eliminating the need for rigid API contracts.

#### Communication Pattern
```go
// Agent A asks Agent B to perform a calculation
response := agent.AskAgent("calculator-agent", "What is 25 multiplied by 4?")
```

**Features:**
- Plain English instructions
- No predefined message formats
- Agents interpret requests based on their capabilities
- Support for complex, multi-step instructions

**Key Files:**
- `pkg/communication/interfaces.go:9-21` - Communication interfaces
- `agent.go:82-102` - AskAgent implementation

### Communication Flow

```
┌─────────┐     ┌───────────┐     ┌─────────┐     ┌─────────┐
│ Agent A │────>│ Discovery │────>│   HTTP  │────>│ Agent B │
│         │     │  Service  │     │   Call  │     │         │
│         │<────│           │<────│         │<────│         │
└─────────┘     └───────────┘     └─────────┘     └─────────┘
     │                                   │              │
     └───────── Correlation IDs ─────────┴──────────────┘
```

### Transport Layer

**HTTP-based communication with:**
- RESTful endpoints for inter-agent calls
- Automatic retry with exponential backoff
- Configurable timeouts per request
- Health check endpoints for K8s probes

**Headers propagated automatically:**
- `X-Correlation-ID`: Request correlation
- `X-Request-ID`: Unique request identifier
- `X-User-ID`: User context (if applicable)
- `X-Session-ID`: Session tracking

## Observability & Telemetry

### OpenTelemetry (OTEL) Integration

#### Distributed Tracing

**Automatic instrumentation for:**
- All HTTP requests/responses
- Agent capability invocations
- Database operations (Redis)
- External service calls

**Trace Context Propagation:**
```go
// Automatically creates child spans
ctx, span := tracer.Start(ctx, "Agent.ProcessRequest",
    trace.WithAttributes(
        attribute.String("agent.id", agentID),
        attribute.String("capability", capability),
    ),
)
defer span.End()
```

**Key Files:**
- `pkg/telemetry/correlation.go:38-86` - Correlation middleware
- `pkg/telemetry/interfaces.go` - Telemetry interfaces

#### Span Attributes

Each span automatically includes:
- `correlation.id`: Request correlation ID
- `request.id`: Unique request identifier
- `agent.id`: Agent performing the operation
- `capability.name`: Capability being executed
- `trace.id` & `span.id`: OTEL trace context

### Structured Logging

#### Context-Aware Logging

All logs are automatically enriched with:
```json
{
  "timestamp": "2024-01-15T10:30:45Z",
  "level": "INFO",
  "message": "Agent registered successfully",
  "correlation_id": "abc-123-def",
  "trace_id": "1234567890abcdef",
  "span_id": "abcdef1234",
  "agent_id": "calculator-agent",
  "namespace": "agents"
}
```

**Log Levels:**
- `DEBUG`: Detailed diagnostic information
- `INFO`: General operational messages
- `WARN`: Warning conditions
- `ERROR`: Error conditions requiring attention

**Key Files:**
- `pkg/telemetry/correlation.go:155-181` - Log enrichment
- `pkg/logger/simple.go` - Logger implementation

### Metrics Collection

**Automatic metrics for:**
- Request latency (p50, p95, p99)
- Request success/failure rates
- Agent availability
- Discovery cache hit rates
- Circuit breaker state

### LLM Debug Payload Store (Orchestration)

For debugging orchestration issues, the framework provides complete LLM payload capture:

**Features:**
- Complete prompt and response storage (no truncation like Jaeger)
- 6 recording sites: `plan_generation`, `correction`, `synthesis`, `synthesis_streaming`, `micro_resolution`, `semantic_retry`
- Request correlation via trace ID
- Provider and model tracking
- Configurable TTL (24h success, 7 days errors)

**Configuration:**
```bash
export TRUVAG3_LLM_DEBUG_ENABLED=true
```

**Key Files:**
- `orchestration/llm_debug_store.go` - Interface and types
- `orchestration/redis_llm_debug_store.go` - Redis implementation

## Resilience & Fault Tolerance

### Circuit Breaker Pattern

Prevents cascade failures when services are unavailable:

```go
// Configuration
discovery.ConfigureCache(
    enabled: true,
    cbThreshold: 5,        // Open after 5 failures
    cbCooldown: 2*Minute,   // Recovery period
)
```

**States:**
- `CLOSED`: Normal operation
- `OPEN`: Failures exceeded threshold, using cache
- `HALF-OPEN`: Testing recovery

**Key Files:**
- `pkg/discovery/redis.go:556-565` - Circuit breaker implementation

### Caching & Fallback

#### Multi-Layer Caching
1. **In-memory cache**: Fast access to agent registry
2. **Persistent snapshots**: Disk-based recovery
3. **TTL-based expiration**: Automatic cleanup

#### Fallback Strategies
- Use cached data when Redis unavailable
- Graceful degradation of features
- Automatic recovery when services restore

### Health Monitoring

#### Heartbeat System
- Agents send periodic heartbeats (configurable interval)
- Automatic cleanup of dead agents after TTL expiry
- Health status exposed for Kubernetes probes

**Health Check Endpoint:**
```http
GET /health
{
  "status": "healthy",
  "agent_id": "calculator-agent",
  "uptime": 3600,
  "last_heartbeat": "2024-01-15T10:30:45Z"
}
```

**Key Files:**
- `pkg/discovery/redis.go:439-485` - Heartbeat refresh

## Orchestration & Routing

### Routing Modes

#### 1. Autonomous Mode
LLM-based dynamic routing:
```go
router := routing.NewAutonomousRouter(llmClient)
plan, err := router.Route(ctx, "Calculate my taxes and send report")
```
- Uses agent catalog to understand capabilities
- Dynamically creates execution plans
- Adapts to available agents

#### 2. Workflow Mode
Predefined execution patterns:
```yaml
name: financial_analysis
triggers:
  patterns: ["analyze stock*", "market report*"]
steps:
  - name: get_market_data
    agent: market-data-agent
    instruction: "Fetch latest market data for {symbol}"
  - name: analyze_trends
    agent: technical-analysis-agent
    instruction: "Analyze trends from market data"
    depends_on: [get_market_data]
```

#### 3. Hybrid Mode
- Attempts workflow matching first
- Falls back to autonomous routing
- Best of both worlds

**Key Files:**
- `pkg/routing/interfaces.go:11-17` - Router modes
- `pkg/orchestration/interfaces.go:13-16` - Orchestrator interface

### Execution Planning

#### Routing Plans
```go
type RoutingPlan struct {
    ID               string
    Steps            []RoutingStep
    EstimatedDuration time.Duration
    Confidence       float64
}
```

**Features:**
- Multi-step execution with dependencies
- Parallel execution for independent steps
- Configurable timeouts and retry policies
- Step-level success/failure handling

### Response Synthesis

#### Synthesis Strategies

1. **LLM-based**: Intelligent combination of responses
2. **Template-based**: Structured response formatting
3. **Simple**: Concatenation with formatting
4. **Custom**: User-defined synthesis logic

```go
synthesizer := orchestration.NewSynthesizer(
    orchestration.WithStrategy(orchestration.StrategyLLM),
)
response := synthesizer.Synthesize(ctx, request, executionResults)
```

**Key Files:**
- `pkg/orchestration/interfaces.go:75-81` - Synthesizer interface
- `pkg/orchestration/interfaces.go:84-98` - Synthesis strategies

### Intelligent Error Recovery

The orchestration module includes a **four-layer parameter resolution system** that handles errors intelligently:

| Layer | Strategy | Cost |
|-------|----------|------|
| **Layer 1: Auto-Wiring** | Exact/case-insensitive name matching, type coercion | Free |
| **Layer 2: Micro-Resolution** | LLM extracts parameters via function calling | 1 LLM call |
| **Layer 3: Error Analysis** | LLM analyzes tool errors and suggests corrections | 1 LLM call |
| **Layer 4: Semantic Retry** | LLM computes parameters from full execution context | 1 LLM call |

**Semantic Retry (Layer 4)** is particularly powerful—when standard error analysis says "cannot fix", it uses the user's original query plus all source data from previous steps to compute the correct values. This enables automatic recovery from errors that would otherwise require human intervention.

**Example:**
```
User: "Sell 100 Tesla shares and convert proceeds to EUR"
Step 1: Returns {price: 468.285}
Step 2: Fails with "amount: 0" (MicroResolver couldn't compute this)

→ Semantic Retry computes: 100 × 468.285 = 46828.5
→ Retries with corrected parameters → SUCCESS
```

**Configuration:**
```bash
# Enable/disable semantic retry (default: true)
export TRUVAG3_SEMANTIC_RETRY_ENABLED=true

# Maximum retry attempts (default: 2)
export TRUVAG3_SEMANTIC_RETRY_MAX_ATTEMPTS=2
```

**Key Files:**
- `orchestration/contextual_re_resolver.go` - Semantic retry implementation
- `orchestration/executor.go` - Integration with execution loop

### Tiered Capability Resolution (Token Optimization)

For deployments with 20+ tools, the orchestration module provides **tiered capability resolution** to reduce LLM token usage by 50-75%.

**How it works:**
1. **Tier 1**: Send lightweight tool summaries (~50-100 tokens each) to LLM for tool selection
2. **Tier 2**: Fetch full schemas only for selected tools (no LLM call)
3. **Tier 3**: Generate execution plan with focused context

**Benefits:**
- 50-75% token reduction for medium-scale deployments (20-100 tools)
- Improved accuracy with smaller context (research-backed)
- No external dependencies (unlike RAG-based solutions)
- Graceful fallback on selection errors

**Configuration:**
```bash
# Enabled by default
export TRUVAG3_TIERED_RESOLUTION_ENABLED=true

# Minimum tools to trigger tiering (default: 20)
export TRUVAG3_TIERED_MIN_TOOLS=20
```

```go
config := orchestration.DefaultConfig()
config.EnableTieredResolution = true  // Default
config.TieredResolution = orchestration.TieredCapabilityConfig{
    MinToolsForTiering: 20,
}
```

**Key Files:**
- `orchestration/tiered_capability_provider.go` - Tiered resolution implementation
- `orchestration/catalog.go` - Capability summaries

### Orchestrator Persona Customization

Define your orchestrator's behavioral context using `SystemInstructions`:

```go
config.PromptConfig = orchestration.PromptConfig{
    SystemInstructions: `You are a travel planning assistant.
Always check weather before recommending outdoor activities.`,
    Domain: "travel",
}
```

This is similar to LangChain's `system_prompt` and AutoGen's `system_message`. When set, your persona becomes the primary identity and the orchestrator role becomes functional.

**Key Files:**
- `orchestration/prompt_builder.go` - PromptConfig structure
- `orchestration/default_prompt_builder.go` - Persona integration

## Configuration Examples

### Basic Agent Setup
```go
package main

import (
    framework "github.com/truvaagents/truva-g3"
)

type MyAgent struct {
    *framework.BaseAgent
}

func (a *MyAgent) ProcessRequest(ctx context.Context, request string) (string, error) {
    // Agent logic here
    return "Response", nil
}

func main() {
    agent := &MyAgent{
        BaseAgent: &framework.BaseAgent{},
    }
    
    framework.RunAgent(agent,
        framework.WithAgentName("my-agent"),
        framework.WithRedisURL("redis://redis:6379"),
        framework.WithOTELEndpoint("http://jaeger:4317"),
        framework.WithNamespace("agents"),
    )
}
```

### Kubernetes Deployment
```yaml
apiVersion: apps/v1
kind: Deployment
metadata:
  name: my-agent
  namespace: agents
spec:
  replicas: 3
  selector:
    matchLabels:
      app: my-agent
  template:
    metadata:
      labels:
        app: my-agent
    spec:
      containers:
      - name: agent
        image: myregistry/my-agent:latest
        env:
        - name: REDIS_URL
          value: "redis://redis.default.svc.cluster.local:6379"
        - name: OTEL_EXPORTER_OTLP_ENDPOINT
          value: "http://otel-collector.observability:4317"
        - name: TRUVAG3_K8S_NAMESPACE
          valueFrom:
            fieldRef:
              fieldPath: metadata.namespace
---
apiVersion: v1
kind: Service
metadata:
  name: my-agent
  namespace: agents
spec:
  selector:
    app: my-agent
  ports:
  - port: 8080
    targetPort: 8080
```

## Best Practices

### 1. Service Discovery
- Use service-level registration in production
- Configure appropriate TTLs based on workload
- Enable caching for resilience

### 2. Communication
- Keep instructions clear and concise
- Include context in requests when needed
- Handle timeouts gracefully

### 3. Observability
- Always propagate correlation IDs
- Use structured logging consistently
- Set up alerts on key metrics

### 4. Resilience
- Configure circuit breakers appropriately
- Enable persistent caching for critical agents
- Implement health checks

### 5. Orchestration
- Start with workflow mode for predictable patterns
- Use autonomous mode for flexible requirements
- Monitor routing confidence scores

## Troubleshooting

### Common Issues

1. **Agents not discovering each other**
   - Check Redis connectivity
   - Verify namespace configuration
   - Ensure heartbeats are being sent

2. **High latency in communication**
   - Check network policies in K8s
   - Review timeout configurations
   - Monitor OTEL traces for bottlenecks

3. **Circuit breaker frequently opening**
   - Adjust threshold based on traffic
   - Increase cooldown period
   - Check Redis performance

4. **Missing traces**
   - Verify OTEL endpoint configuration
   - Check collector status
   - Ensure context propagation

## Performance Considerations

### Scaling Guidelines

- **Discovery Service**: Redis can handle 100K+ ops/sec
- **Agent Registration**: O(1) complexity
- **Capability Lookup**: O(n) where n = agents with capability
- **Catalog Sync**: Batch operation, schedule during low traffic

### Resource Requirements

**Minimum per agent:**
- CPU: 100m
- Memory: 128Mi
- Storage: 10Mi (if persistence enabled)

**Recommended for production:**
- CPU: 500m-1000m
- Memory: 512Mi-1Gi
- Storage: 100Mi (with logging)

## Security Considerations

### Network Security
- Use NetworkPolicies to restrict agent communication
- Enable mTLS for service mesh integration
- Rotate Redis passwords regularly

### Data Protection
- Sanitize sensitive data in logs
- Use encryption for Redis (TLS)
- Implement RBAC for agent operations

## Future Enhancements

### Planned Features
- GraphQL API for complex queries
- WebSocket support for real-time updates
- Native service mesh integration
- Multi-cluster federation
- Advanced workflow orchestration

### Community Roadmap
- Plugin system for custom capabilities
- UI for agent monitoring
- Automated testing framework
- Performance benchmarking suite

## References

- [TruvaG3 Framework Documentation](https://github.com/truvaagents/truva-g3)
- [OpenTelemetry Documentation](https://opentelemetry.io/docs/)
- [Kubernetes Service Discovery](https://kubernetes.io/docs/concepts/services-networking/service/)
- [Redis Documentation](https://redis.io/documentation)