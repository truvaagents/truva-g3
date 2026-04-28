# Truva-G3 Framework Examples

Complete, production-ready examples demonstrating AI-enhanced distributed systems with the Truva-G3 framework. Optimized for local development with Kind and cloud deployment on any Kubernetes platform.

---

## Table of Contents

### Getting Started
- [1. Available Examples](#1-available-examples) - Browse all examples by complexity
- [2. Quick Start](#2-quick-start) - Get running in 5 minutes
- [3. Test the System](#3-test-the-system) - Verify your setup works
- [4. Individual Example Usage](#4-individual-example-usage) - Run examples locally
- [5. API Key Configuration](#5-api-key-configuration) - Set up AI providers

### Deployment
- [6. Kubernetes Deployment](#6-kubernetes-deployment) - Local (Kind) and cloud
- [7. Networking & Ingress](#7-networking--ingress) - How services are accessed
- [8. Detailed Example Features](#8-detailed-example-features) - What each example demonstrates
- [9. System Architecture](#9-system-architecture) - How components interact

### Operations
- [10. Monitoring & Observability](#10-monitoring--observability) - Metrics, logs, traces
- [11. Troubleshooting](#11-troubleshooting) - Common issues and solutions
- [12. Development Workflow](#12-development-workflow) - Build tools, agents, and workflows
- [13. Cleanup](#13-cleanup) - Remove deployed resources
- [14. Learning Progression](#14-learning-progression) - Structured learning paths
- [15. Building Your Own Examples](#15-building-your-own-examples) - Best practices and patterns

### Resources
- [16. Next Steps](#16-next-steps) - Your journey from learning to deployment
- [17. Documentation](#17-documentation) - Additional guides and references

---

## 1. Available Examples

### Quick Reference - Start Here

| Example | Pattern | Complexity | Best For | Time |
|---------|---------|------------|----------|------|
| **[tool-example](tool-example/)** | Tool (Passive) | ⭐ Beginner | Learning tool patterns, external APIs | 15 min |
| **[agent-example](agent-example/)** | Agent (Active) | ⭐⭐ Intermediate | Service discovery, coordination | 20 min |
| **[ai-tools-showcase](ai-tools-showcase/)** | Ready AI Tools | ⭐⭐ Intermediate | Adding AI to existing systems | 15 min |
| **[agent-example-enhanced](agent-example-enhanced/)** | AI Agent | ⭐⭐⭐ Advanced | AI-powered capabilities | 30 min |
| **[ai-agent-example](ai-agent-example/)** | AI-Native | ⭐⭐⭐⭐ Expert | AI-driven architecture | 45 min |
| **[ai-multi-provider](ai-multi-provider/)** | Resilient AI | ⭐⭐⭐⭐ Expert | Mission-critical AI systems | 60 min |
| **[orchestration-example](orchestration-example/)** | Orchestrator | ⭐⭐⭐⭐ Expert | Complex workflows | 45 min |
| **[workflow-example](workflow-example/)** | YAML Engine | ⭐⭐⭐ Advanced | Declarative workflows | 30 min |

### Framework Patterns (Optional)

| Example | Focus | Best For | Time |
|---------|-------|----------|------|
| **[telemetry](telemetry/)** | Monitoring | Production observability | 20 min |
| **[context_propagation](context_propagation/)** | Tracing | Distributed system debugging | 15 min |
| **[error_handling](error_handling/)** | Error Patterns | Framework consistency | 10 min |

### Where Should I Start?

**👋 New to Truva-G3?** → `tool-example` then `agent-example`

**🤖 Want AI features?** → `ai-tools-showcase` (4 ready tools) or `agent-example-enhanced`

**🏢 Enterprise workflows?** → `orchestration-example` or `workflow-example`

**⚡ Production AI?** → `ai-multi-provider` (reliability) + `telemetry` (monitoring)

**🔧 Framework dev?** → Start with core examples, then framework patterns

### Infrastructure
| Component | Purpose | Features |
|-----------|---------|----------|
| **[k8-deployment/](k8-deployment/)** | Kubernetes deployment configs | Redis, Prometheus, Grafana, Jaeger, OTEL |

## 2. Quick Start

### Prerequisites

**Required:**
- [Docker](https://docs.docker.com/get-docker/) (20.10+)
- [Kind](https://kind.sigs.k8s.io/docs/user/quick-start/#installation) (0.20+)
- [kubectl](https://kubernetes.io/docs/tasks/tools/) (1.28+)
- [Go](https://golang.org/dl/) (1.25+) - for building examples

**Verification:**
```bash
docker --version    # Docker 20.10+
kind --version      # kind 0.20+
kubectl version     # Client 1.28+
go version          # go1.25+
```

### One-Command Setup

Each agent example has a `full-deploy` command that sets up everything from scratch:

```bash
# Clone and setup
git clone https://github.com/truvaagents/truva-g3.git
cd truva-g3/examples/travel-chat-agent

# Configure your AI provider key
cp .env.example .env
# Edit .env and set at least one API key (OPENAI_API_KEY, GROQ_API_KEY, etc.)

# Deploy everything with one command
./setup.sh full-deploy
```

This single command creates a Kind cluster, deploys all infrastructure, builds and deploys the agent, and verifies all Ingress routes. All services are accessible via `*.localhost` — no port-forwarding needed.

```
Access services:
  Chat UI:       http://chat.localhost
  Chat API:      http://travel-chat-agent.localhost
  Grafana:       http://grafana.localhost (admin/admin)
  Prometheus:    http://prometheus.localhost
  Jaeger:        http://jaeger.localhost
  Registry:      http://registry.localhost
```

Subsequent agents reuse the existing cluster and infrastructure (~30 sec deploy):
```bash
cd ../devops-chat-agent
cp .env.example .env && vim .env
./setup.sh full-deploy    # Reuses cluster, deploys only this agent
```

## 3. Test the System

Once the demo is running, test the complete tool → agent orchestration:

```bash
# 1. Test weather tool directly
curl -X POST http://localhost:8339/api/capabilities/current_weather \
  -H "Content-Type: application/json" \
  -d '{"location":"New York","units":"metric"}'

# 2. Test agent service discovery
curl http://localhost:8350/api/capabilities/discover_tools

# 3. Test intelligent orchestration (agent + AI + tools)
curl -X POST http://localhost:8350/api/capabilities/research_topic \
  -H "Content-Type: application/json" \
  -d '{
    "topic": "current weather conditions in San Francisco",
    "ai_synthesis": true,
    "max_results": 5
  }'
```

**Expected Flow:**
1. Agent receives research request
2. Agent discovers available tools (weather tool)
3. Agent extracts location and calls weather tool
4. Agent uses AI to analyze and synthesize results
5. Agent returns intelligent summary

## 4. Individual Example Usage

### Running Examples Locally

Each example can run independently:

```bash
# Terminal 1: Start Redis (required for service discovery)
docker run -p 6379:6379 redis:7-alpine

# Terminal 2: Run any tool
cd tool-example
go run main.go
# Tool starts on http://localhost:8340

# Terminal 3: Run any agent
cd agent-example
go run main.go
# Agent starts on http://localhost:8350
```

### Building Examples

Each example builds independently:

```bash
# Build any example
cd <example-directory>
go mod tidy
go build -o example-binary .
./example-binary

# Or with Docker
docker build -t <example-name>:latest .
```

## 5. API Key Configuration

### Automated Setup (Recommended)

```bash
# Interactive setup for local and Kubernetes
./setup-api-keys.sh
```

### Manual Setup

Create `.env` file in `examples/` directory:

```bash
# AI Providers (at least one recommended)
OPENAI_API_KEY=sk-your-openai-key
GROQ_API_KEY=gsk-your-groq-key        # Free tier available
ANTHROPIC_API_KEY=sk-ant-your-key
DEEPSEEK_API_KEY=your-deepseek-key
GOOGLE_AI_API_KEY=your-gemini-key

# External APIs (optional)
WEATHER_API_KEY=your-weather-api-key
```

The framework auto-detects available providers and uses the best one available.

## 6. Kubernetes Deployment

### Local Development (Kind)

Every agent example provides a 1-click `full-deploy` command:

```bash
cd examples/travel-chat-agent
./setup.sh full-deploy    # Creates cluster + infra + deploys agent
```

**What `full-deploy` does:**
1. Creates a Kind cluster with NGINX Ingress Controller (ports 80/443 only)
2. Deploys shared infrastructure (Redis, Prometheus, Grafana, Jaeger, OTEL Collector)
3. Builds Docker image from local workspace modules
4. Deploys the agent with Secrets, ConfigMaps, and Ingress routes
5. Verifies all `*.localhost` routes are reachable

The first agent creates the cluster (~3 min). Subsequent agents reuse it (~30 sec).

### Cloud Deployment

The same Ingress resources work on EKS, GKE, and AKS. The only differences are:
1. Replace `*.localhost` hostnames with real DNS (`*.truvag3.example.com`)
2. The Ingress Controller is provisioned by the cloud (AWS ALB, GCP GLB) instead of our manifest
3. Add TLS via cert-manager

See [CLOUD_DEPLOYMENT_GUIDE.md](CLOUD_DEPLOYMENT_GUIDE.md) for platform-specific instructions.

## 7. Networking & Ingress

All services are accessed through an **NGINX Ingress Controller** — a single reverse proxy that routes traffic based on the `Host` header. This replaces the old pattern of per-service port-forwarding.

### Architecture

```
Browser (http://travel-chat-agent.localhost)
    │
    ▼ port 80
Kind Node (hostPort 80)
    │
    ▼
NGINX Ingress Controller (single pod in ingress-nginx namespace)
    │ reads Ingress resources from ALL namespaces
    │ matches Host header → routes to backend service
    │
    ├─ Host: travel-chat-agent.localhost  → svc/travel-chat-agent-service:80
    ├─ Host: devops-chat-agent.localhost  → svc/devops-chat-agent-service:80
    ├─ Host: hitl-agent.localhost         → svc/agent-with-human-approval-service:80
    ├─ Host: chat.localhost               → svc/chat-ui-service:80
    ├─ Host: grafana.localhost            → svc/grafana:80
    ├─ Host: jaeger.localhost             → svc/jaeger-query:80
    └─ ... (all services via *.localhost)
```

### Why `*.localhost` Works Without /etc/hosts

On macOS and Linux, `*.localhost` resolves to `127.0.0.1` automatically per [RFC 6761](https://tools.ietf.org/html/rfc6761). The browser sends the request to `127.0.0.1:80`, which hits the Kind node's hostPort, which is the NGINX Ingress Controller. The controller reads the `Host` header and routes accordingly.

### Service Access URLs

| Service | URL | Type |
|---------|-----|------|
| **travel-chat-agent** | http://travel-chat-agent.localhost | Agent |
| **devops-chat-agent** | http://devops-chat-agent.localhost | Agent |
| **agent-with-human-approval** | http://hitl-agent.localhost | Agent |
| **agent-with-async** | http://async-travel-agent.localhost | Agent |
| **agent-with-resilience** | http://resilience-agent.localhost | Agent |
| **event-driven-agent** | http://event-driven-agent.localhost | Agent |
| **qa-agent** | http://qa-agent.localhost | Agent |
| **Chat UI** | http://chat.localhost | UI |
| **Registry Viewer** | http://registry.localhost | UI |
| **Grafana** | http://grafana.localhost | Infra |
| **Prometheus** | http://prometheus.localhost | Infra |
| **Jaeger** | http://jaeger.localhost | Infra |
| **AlertManager** | http://alertmanager.localhost | Infra |

### Who Gets Ingress, Who Doesn't

| Component Type | Ingress? | Reason |
|---|---|---|
| **Agents** | Yes | User-facing APIs accessed from browser/curl |
| **UIs** (chat-ui, registry-viewer) | Yes | User-facing web apps |
| **Infra dashboards** (grafana, jaeger, prometheus) | Yes | Developer-facing dashboards |
| **Tools** (weather-tool, geocoding-tool, etc.) | No | Internal services called by agents within the cluster via ClusterIP |
| **Internal infra** (redis, otel-collector, loki) | No | Backend services, no browser access needed |

### How It's Implemented

Each agent's `k8-deployment.yaml` contains an Ingress resource alongside its Deployment and Service:

```yaml
apiVersion: networking.k8s.io/v1
kind: Ingress
metadata:
  name: my-agent-ingress
  annotations:
    # Large payloads for AI orchestration responses
    nginx.ingress.kubernetes.io/proxy-body-size: "50m"
    # SSE streaming support — keep connection open, disable buffering
    nginx.ingress.kubernetes.io/proxy-read-timeout: "3600"
    nginx.ingress.kubernetes.io/proxy-send-timeout: "3600"
    nginx.ingress.kubernetes.io/proxy-buffering: "off"
spec:
  ingressClassName: nginx
  rules:
  - host: my-agent.localhost
    http:
      paths:
      - path: /
        pathType: Prefix
        backend:
          service:
            name: my-agent-service
            port:
              number: 80
```

When `kubectl apply` creates this Ingress, the NGINX controller automatically picks it up and starts routing traffic. No restart or port-forward needed.

### Shared Setup Library

All setup scripts source a shared library ([k8-deployment/setup-env-lib.sh](k8-deployment/setup-env-lib.sh)) that provides:

| Function | Purpose |
|----------|---------|
| `truvag3_check_prerequisites` | Checks required tools (go, docker, kind, kubectl) |
| `truvag3_create_cluster` | Creates Kind cluster with Ingress Controller support (ports 80/443 only) |
| `truvag3_setup_infra` | Deploys shared infrastructure (idempotent — skips if already running) |
| `truvag3_verify_ingress` | Verifies `*.localhost` routes are reachable via HTTP |
| `truvag3_forward` / `truvag3_forward_all` | Fallback port-forwarding if Ingress is unavailable |
| `truvag3_load_env` | Loads environment variables from `.env` file |
| `truvag3_build_docker` | Builds Docker image with optional `--no-cache` |
| `truvag3_load_to_kind` | Loads Docker image into Kind cluster (auto-detects cluster name) |
| `truvag3_create_secret` | Creates K8s Secret with AI provider keys from environment |
| `truvag3_create_configmap` | Creates K8s ConfigMap with `TRUVAG3_*` variables from `.env` file |
| `truvag3_delete_cluster` | Deletes Kind cluster |

This keeps each agent's `setup.sh` thin — app-specific logic only, with common infrastructure delegated to the shared lib.

### Fallback: Port Forwarding

If Ingress is unavailable (e.g., non-Kind cluster without an ingress controller), every agent supports fallback port-forwarding:

```bash
./setup.sh forward        # Forward agent only
./setup.sh forward-all    # Forward agent + monitoring dashboards
```

## 8. Detailed Example Features

### Core Learning Examples

#### [tool-example](tool-example/) - Passive Tool Pattern
**What it demonstrates:**
- Multiple capability registration (`current_weather`, `forecast`, `analysis`)
- Auto-generated REST endpoints (`/api/capabilities/current_weather`)
- External API integration (Weather API) with error handling
- Caching patterns and request/response transformation
- Tool registration without discovery capabilities (passive pattern)

**Perfect for:** Understanding how to build focused, discoverable services that other agents can find and use

---

#### [agent-example](agent-example/) - Active Agent Pattern
**What it demonstrates:**
- Service discovery capabilities (finds and catalogs available tools)
- Tool orchestration and coordination logic
- AI provider auto-detection (OpenAI/Groq/Anthropic/Gemini)
- Basic AI-enhanced responses and intelligent routing
- Agent-to-tool communication patterns and data flow

**Perfect for:** Understanding coordination patterns and building service mesh-like architectures

---

### AI-Enhanced Examples

#### [agent-example-enhanced](agent-example-enhanced/) - AI-Everything Agent
**What it demonstrates:**
- 4 distinct capabilities ALL enhanced with AI intelligence
- Multiple AI provider support with automatic fallback mechanisms
- Enhanced data synthesis and contextual analysis
- Smart context awareness across different operations
- Production-ready AI error handling and graceful degradation

**Perfect for:** Building agents where every capability benefits from AI enhancement

---

#### [ai-agent-example](ai-agent-example/) - AI-First Architecture
**What it demonstrates:**
- AI drives EVERY decision from initial request to final response
- Intent recognition and dynamic execution planning
- AI-guided execution flow with no hardcoded business logic
- Continuous AI oversight and real-time adaptation
- Hard dependency on AI (demonstrates true AI-native design)

**Perfect for:** Building systems where AI IS the primary brain, not just a helper tool

---

#### [ai-multi-provider](ai-multi-provider/) - Production AI Resilience
**What it demonstrates:**
- Primary/fallback/secondary AI provider configuration
- Automatic provider failover and real-time health checking
- Hybrid deployment (can run as Tool, Agent, or both simultaneously)
- Provider-specific optimization (speed vs accuracy vs cost)
- Production-grade AI reliability patterns

**Perfect for:** Mission-critical systems that cannot afford AI downtime

---

#### [ai-tools-showcase](ai-tools-showcase/) - Ready-to-Deploy AI Tools
**What it demonstrates:**
- 4 production-ready AI tools you can use immediately:
  - **Translation Tool**: Professional multi-language translation
  - **Summarization Tool**: Intelligent document and content summarization
  - **Sentiment Analysis Tool**: Emotion, tone, and intent detection
  - **Code Review Tool**: AI-powered code quality and security analysis
- Composite deployment pattern (all tools hosted in single service)
- Individual tool deployment and scaling strategies

**Perfect for:** Adding professional AI capabilities to existing systems without building from scratch

---

### Advanced Orchestration

#### [orchestration-example](orchestration-example/) - Multi-Modal Orchestration
**What it demonstrates:**
- **Autonomous Mode**: AI analyzes incoming requests and dynamically determines routing
- **Workflow Mode**: Recipe-based execution with explicit dependencies and error handling
- **Hybrid Mode**: Intelligently combines AI decision-making with predefined workflows
- Multi-agent coordination patterns and complex scenario handling
- Advanced routing strategies and workflow adaptation

**Perfect for:** Enterprise systems requiring sophisticated workflow coordination

---

#### [workflow-example](workflow-example/) - Declarative YAML Workflows
**What it demonstrates:**
- YAML workflow definitions loaded dynamically from Kubernetes ConfigMaps
- Declarative step dependencies, parallel execution, and error handling
- Runtime workflow modification without service redeployment
- Built-in workflow templates and common patterns
- Optional AI enhancement for workflow optimization and adaptation

**Perfect for:** Business process automation and user-configurable workflows

---

### Framework Patterns

#### [telemetry](telemetry/) - Production Monitoring
- Comprehensive metrics emission (counters, histograms, gauges)
- Circuit breaker integration with telemetry
- Error tracking and success rate monitoring
- Development vs production telemetry profiles

#### [context_propagation](context_propagation/) - Distributed Tracing
- Request correlation across service boundaries
- OpenTelemetry integration and trace visualization
- User and tenant context tracking
- Performance monitoring across microservices
- **See Also:** [Distributed Tracing Guide](../docs/DISTRIBUTED_TRACING_GUIDE.md) for comprehensive tracing patterns

#### [error_handling](error_handling/) - Framework Error Consistency
- Structured error types and sentinel error patterns
- Retryable error detection and automatic retry logic
- Configuration error handling and validation
- Framework-wide error consistency and debugging

## 9. System Architecture

```
┌─────────────────┐    Service      ┌─────────────────┐
│     Agents      │    Discovery    │      Redis      │
│  (Active Logic) │◄────────────────┤   (Registry)    │
└─────────┬───────┘                 └─────────────────┘
          │                                   ▲
          │ Orchestrate                       │ Register
          │                                   │
          ▼                                   │
┌─────────────────┐                 ┌─────────┴───────┐
│     Tools       │                 │   Monitoring    │
│ (Capabilities)  │                 │ Prometheus/Otel │
└─────────┬───────┘                 └─────────────────┘
          │
          ▼
┌─────────────────┐
│   AI Providers  │
│ OpenAI/Groq/etc │
└─────────────────┘
```

**Component Roles:**
- **Tools**: Provide focused capabilities (weather, APIs, data processing)
- **Agents**: Coordinate workflows and orchestrate multiple tools
- **Redis**: Service discovery registry and caching layer
- **AI Providers**: External intelligence for analysis and decision making
- **Monitoring**: Full observability with metrics, logs, and traces

## 10. Monitoring & Observability

### Accessing Dashboards

With any agent deployed via `full-deploy`:
- **Grafana**: http://grafana.localhost (admin/admin)
- **Prometheus**: http://prometheus.localhost
- **Jaeger**: http://jaeger.localhost
- **Registry Viewer**: http://registry.localhost

### Key Metrics

```promql
# Service health
up{job=~"truvag3.*"}

# Request rates
rate(http_requests_total{truvag3_framework_type=~"tool|agent"}[5m])

# Error rates
rate(http_requests_total{status=~"5.."}[5m])

# Service discovery
rate(truvag3_discovery_requests_total[5m])
```

### Service Health Monitoring

#### Understanding Heartbeat Logs

When running examples, you'll see periodic heartbeat logs that indicate service health:

```bash
# Example output from a healthy service
INFO Started heartbeat for tool registration tool_id=weather-tool-7f8d9 tool_name=weather-tool interval_sec=15 ttl_sec=30
INFO Tool initialization completed id=weather-tool-7f8d9 name=weather-tool discovery_enabled=true
# ... after 5 minutes ...
INFO Heartbeat health summary service_id=weather-tool-7f8d9 service_name=weather-tool success_count=20 failure_count=0 success_rate=100.00% uptime_minutes=5
```

#### Monitoring Heartbeat Health

```bash
# View all heartbeat-related logs
kubectl logs deployment/<service-name> -n truvag3-examples | grep -E "(heartbeat|Heartbeat)"

# Watch health summaries in real-time
kubectl logs -f deployment/<service-name> -n truvag3-examples | grep "Heartbeat health summary"

# Check for heartbeat failures
kubectl logs deployment/<service-name> -n truvag3-examples | grep -E "(Failed to send heartbeat|failure_count)"
```

## 11. Troubleshooting

### Common Issues

**Services not discovering each other:**
```bash
# Check if heartbeats are running
kubectl logs deployment/<service-name> -n truvag3-examples --tail=100 | grep -E "(Started heartbeat|Heartbeat health)"

# Check Redis connectivity
kubectl port-forward -n truvag3-examples svc/redis 6379:6379
redis-cli ping

# Check service registrations
redis-cli KEYS "truvag3:services:*"

# Common issues and solutions:
# 1. "Failed to send heartbeat" with "connection refused"
#    -> Redis is not accessible. Check Redis deployment and service.

# 2. No "Started heartbeat" log
#    -> Service discovery might be disabled. Check REDIS_URL environment variable.

# 3. High failure_count in health summary
#    -> Intermittent network issues. Check pod networking and Redis stability.
```

**AI requests failing:**
```bash
# Verify API keys are set
kubectl get secret ai-provider-keys -n truvag3-examples -o yaml

# Check logs for AI errors
kubectl logs -f deployment/research-agent -n truvag3-examples
```

**Pods not starting:**
```bash
# Check pod status
kubectl get pods -n truvag3-examples

# Get detailed events
kubectl describe pod <pod-name> -n truvag3-examples
```

### Debug Commands

```bash
# Check cluster status
kubectl get pods -n truvag3-examples
kubectl get ingress -n truvag3-examples

# View logs for a specific agent
kubectl logs -n truvag3-examples -l app=travel-chat-agent -f

# Verify all ingress routes
cd examples/travel-chat-agent && ./setup.sh verify

# Clean restart
./setup.sh cleanup-all && ./setup.sh full-deploy
```

### Debugging & Troubleshooting

#### Enable Debug Logging

When things aren't working as expected, enable debug logs to see framework internals:

```bash
# For normal operations (recommended)
kubectl set env deployment/weather-tool TRUVAG3_LOG_LEVEL=info -n truvag3-examples

# For debugging heartbeat issues
kubectl set env deployment/weather-tool TRUVAG3_LOG_LEVEL=debug -n truvag3-examples

# Watch the detailed logs
kubectl logs -f deployment/weather-tool -n truvag3-examples

# View logs with timestamps
kubectl logs deployment/weather-tool -n truvag3-examples --timestamps=true
```

#### Supported Log Levels

| Level | Use Case | What Gets Logged |
|-------|----------|------------------|
| `debug` | Troubleshooting | Everything - Debug + Info + Warn + Error |
| `info` | Production (default) | Info + Warn + Error messages |
| `warn` | Production (minimal) | Warn + Error messages only |
| `error` | Production (critical only) | Error messages only |

**How Filtering Works:**
- Each level includes all higher severity levels
- Setting `error` minimizes log volume to only critical issues
- Setting `debug` shows maximum detail for troubleshooting

#### Quick Troubleshooting Guide

**"Service not registering in Redis"**
```bash
# Check Redis connectivity
kubectl exec -it deployment/weather-tool -n truvag3-examples -- sh -c 'nc -zv redis 6379'

# Enable debug to see registration attempts
kubectl set env deployment/weather-tool TRUVAG3_LOG_LEVEL=debug -n truvag3-examples
kubectl logs -f deployment/weather-tool -n truvag3-examples | grep -i redis
```

**"Agent can't discover tools"**
```bash
# Check what's registered in Redis
kubectl exec -it deployment/redis -n truvag3-examples -- redis-cli KEYS "truvag3:services:*"

# Enable debug on agent to see discovery attempts
kubectl set env deployment/research-agent TRUVAG3_LOG_LEVEL=debug -n truvag3-examples
kubectl logs -f deployment/research-agent -n truvag3-examples | grep -i discover
```

**"AI provider errors"**
```bash
# Check if API keys are set correctly
kubectl get secret external-api-keys -n truvag3-examples -o yaml

# Enable debug to see AI API calls
kubectl set env deployment/research-agent TRUVAG3_LOG_LEVEL=debug -n truvag3-examples
kubectl logs -f deployment/research-agent -n truvag3-examples | grep -i "ai\|openai\|anthropic"
```

For comprehensive logging configuration, see [Logging Configuration](../docs/API_REFERENCE.md#logging-configuration) in the API Reference.

## 12. Development Workflow

### 1. Pattern-Specific Development

#### Building Tools (Passive Components)
```bash
# Start with tool-example
cd tool-example && go run main.go

# Key patterns to implement:
# 1. Register capabilities with core.Capability{}
# 2. Implement handler functions
# 3. Use core.NewTool() for base functionality
# 4. Test auto-generated endpoints: /api/capabilities/<name>

# Create your own tool:
cp -r tool-example my-data-tool
# → Modify capabilities: data_analysis, data_transform, etc.
# → Update handlers for your domain
# → Tools are discovered automatically by agents
```

#### Building Agents (Active Coordinators)
```bash
# Start with agent-example
cd agent-example && go run main.go

# Key patterns to implement:
# 1. Use core.NewBaseAgent() for discovery powers
# 2. Implement service discovery with agent.DiscoverServices()
# 3. Coordinate tool calls based on requests
# 4. Add AI integration with ai.NewClient()

# Create your own agent:
cp -r agent-example my-workflow-agent
# → Modify coordination logic
# → Add domain-specific orchestration
# → Agents discover and coordinate tools automatically
```

#### AI-Enhanced Development
```bash
# For basic AI enhancement:
cd agent-example-enhanced

# For AI-native architecture:
cd ai-agent-example

# For production AI reliability:
cd ai-multi-provider

# Key AI patterns:
# - ai.NewClient() for auto-detection
# - Multiple provider support
# - Fallback mechanisms
# - AI-driven decision making
```

#### Critical: AI Telemetry Initialization Order

**For AI tracing and logging to work correctly**, you MUST initialize telemetry BEFORE creating your agent/AI client:

```go
func main() {
    // 1. Set component type FIRST
    core.SetCurrentComponentType(core.ComponentTypeAgent)

    // 2. Initialize telemetry BEFORE agent creation
    initTelemetry("my-agent")
    defer telemetry.Shutdown(context.Background())

    // 3. Create agent AFTER telemetry
    agent, err := NewMyAgent()  // AI client created here

    // 4. Framework auto-propagates logger to AI client
    framework, _ := core.NewFramework(agent)
}
```

**Why this matters:**
- If you create the AI client before telemetry, `telemetry.GetTelemetryProvider()` returns `nil`
- AI spans (`ai.generate_response`, `ai.http_attempt`) won't appear in Jaeger
- AI logs will be silent (using `NoOpLogger` instead of production logger)

**Framework-Driven Logger Propagation:**

The Framework automatically propagates the production logger to your AI client during component registration. You don't need to manually call `ai.WithLogger()` - just ensure telemetry is initialized before agent creation.

**Result: AI logs appear with trace IDs:**
```json
{
  "component": "framework/ai",
  "level": "DEBUG",
  "message": "AI HTTP request completed",
  "trace.trace_id": "5b54aa1e7925acb809e77479b5797f5d"
}
```

See [Distributed Tracing Guide](../docs/DISTRIBUTED_TRACING_GUIDE.md#ai-module-distributed-tracing) for complete AI telemetry setup.

### 2. Integration Testing
```bash
# Full system testing with Kind
cd examples/travel-chat-agent && ./setup.sh full-deploy

# Test specific patterns:
# Tools: curl -X POST http://localhost:8340/api/capabilities/<name>
# Agents: curl http://localhost:8350/api/capabilities/discover_tools
# AI: curl -X POST http://localhost:8350/api/capabilities/research_topic

# Debug specific components:
kubectl logs -f -l app.kubernetes.io/name=<component> -n truvag3-examples
```

### 3. Extending Examples

#### Adding New Capabilities
```bash
# In any tool:
tool.RegisterCapability(core.Capability{
    Name:        "your_capability",
    Description: "What it does",
    InputTypes:  []string{"json"},
    OutputTypes: []string{"json"},
    Handler:     yourHandlerFunction,
})
# → Automatically creates /api/capabilities/your_capability endpoint
```

#### Adding Orchestration Logic
```bash
# In any agent:
func (a *YourAgent) handleComplexWorkflow(w http.ResponseWriter, r *http.Request) {
    // 1. Discover available tools
    tools, _ := a.DiscoverServices()

    // 2. Use AI to plan execution (if available)
    if a.aiClient != nil {
        plan, _ := a.aiClient.CreateCompletion(context.Background(), &ai.CompletionRequest{
            Messages: []ai.Message{{Role: "user", Content: "How should I process this request?"}},
        })
    }

    // 3. Coordinate tool calls
    // 4. Synthesize results
}
```

### 4. Production Deployment

**Choose Your Deployment Pattern:**
- **Simple**: Use existing k8-deployment YAML files
- **Cloud-Specific**: Follow [CLOUD_DEPLOYMENT_GUIDE.md](CLOUD_DEPLOYMENT_GUIDE.md)
- **Custom**: Modify deployment configs for your infrastructure

**Key Production Considerations:**
```bash
# Resource limits based on example type:
# Tools: 200m CPU, 256Mi memory (lightweight)
# Agents: 500m CPU, 512Mi memory (coordination overhead)
# AI-Enhanced: 1000m CPU, 1Gi memory (AI processing)

# Scaling patterns:
# Tools: Scale horizontally based on request load
# Agents: Typically 1-3 replicas (coordination complexity)
# Multi-Provider: Scale based on AI API rate limits
```

## 13. Cleanup

To clean up deployed resources:

```bash
# Remove a specific agent
cd examples/travel-chat-agent
./setup.sh cleanup

# Delete the entire Kind cluster and all resources
./setup.sh cleanup-all
```

## 14. Learning Progression

### Beginner Path (30 minutes)
1. **Start Simple**: `cd travel-chat-agent && ./setup.sh full-deploy` → See everything working
2. **Core Concepts**: Study `tool-example` → Understand passive tools
3. **Coordination**: Study `agent-example` → Understand active agents
4. **Test Together**: Run tool + agent → See service discovery in action

### AI Integration Path (1 hour)
1. **Enhanced AI**: Study `agent-example-enhanced` → AI in every capability
2. **AI-Native**: Study `ai-agent-example` → AI-driven architecture
3. **Production AI**: Study `ai-multi-provider` → Provider resilience
4. **Ready Tools**: Study `ai-tools-showcase` → Use built-in AI capabilities

### Advanced Architecture Path (2 hours)
1. **Complex Flows**: Study `orchestration-example` → Multi-modal coordination
2. **Declarative**: Study `workflow-example` → YAML-driven workflows
3. **Observability**: Study `telemetry` + `context_propagation` → Production monitoring
4. **Reliability**: Study `error_handling` → Framework consistency

### Use Case Focused Learning

**"I want to add AI to my existing service"**
→ Start with `ai-tools-showcase` → See 4 ready-to-use AI tools

**"I want to build intelligent workflows"**
→ `agent-example` → `orchestration-example` → `workflow-example`

**"I want production-grade AI reliability"**
→ `ai-multi-provider` → `telemetry` → `error_handling`

**"I want intelligent error handling with AI-powered retry"**
→ `agent-with-orchestration` → Uses orchestration module for semantic retry
→ AI analyzes errors and corrects parameters automatically

**"I want to understand the framework patterns"**
→ `tool-example` → `agent-example` → `context_propagation`

### Error Handling Progression

| Level | Example | Error Handling Capability |
|-------|---------|--------------------------|
| Basic | `agent-example` | Fails on error (no retry) |
| Observability | `agent-with-telemetry` | Metrics + tracing (no intelligent retry) |
| **Intelligent** | `agent-with-orchestration` | **AI-powered retry with parameter correction** |
| Production | `agent-with-resilience` | Circuit breakers + intelligent retry |

> **Note**: For AI-powered error correction, use the `orchestration` module. See [orchestration/README.md](../orchestration/README.md#-when-to-use-the-orchestration-module).

## 15. Building Your Own Examples

Want to create your own tools and agents? Follow these battle-tested patterns learned from all existing examples.

### The Foundation: Workspace Independence

**Most Important Rule:** Every example must work standalone - no dependencies on framework source code.

**Why this matters:**
- Examples are production-ready templates users can copy
- Docker builds work without framework source
- Examples can be moved to separate GitHub repos
- Shows real-world usage patterns

**How it works:**
```go
// ✅ Every example's go.mod looks like this
module github.com/truvaagents/truva-g3/examples/your-example

go 1.25

require github.com/truvaagents/truva-g3/core v0.6.4  // Fetches from GitHub

// NO replace directives
// NO workspace references
```

**Testing standalone builds:**
```bash
# Copy example anywhere and it should build
cp -r examples/your-example /tmp/test
cd /tmp/test
go build .  # Should work immediately!
```

**Getting the latest framework version:**
```bash
# Check latest release at https://github.com/truvaagents/truva-g3/tags
# Or via command:
git tag --sort=-v:refname | head -1

# Update your example
cd examples/your-example
go get github.com/truvaagents/truva-g3/core@v0.6.4  # Use actual latest
go mod tidy
```

---

### File Structure

**Tools use 4 focused files:**
```
your-tool/
├── main.go              (100-170 lines)  → Lifecycle only
├── {domain}_tool.go     (150-300 lines)  → Component definition
├── handlers.go          (150-400 lines)  → HTTP layer
└── {domain}_data.go     (100-250 lines)  → Business logic
```

**Agents use 4 focused files:**
```
your-agent/
├── main.go              (150-200 lines)  → Lifecycle only
├── {domain}_agent.go    (250-350 lines)  → Agent definition
├── handlers.go          (350-450 lines)  → HTTP + coordination
└── orchestration.go     (500-1000 lines) → Complex workflows
```

**File responsibilities:**
- **main.go** - Configuration, framework setup, graceful shutdown. NO business logic.
- **{type}.go** - Struct definition, capability registration, types
- **handlers.go** - HTTP request/response handling
- **{logic}.go** - Business logic, API calls, orchestration

**Keep it focused:** Aim for <200 lines per file when possible (orchestration.go is the exception).

---

### Configuration Best Practices

**Always use environment variables** - never hardcode values:

```go
// ✅ GOOD - Environment-based
func main() {
    if err := validateConfig(); err != nil {  // Validate FIRST
        log.Fatalf("Configuration error: %v", err)
    }

    framework, _ := core.NewFramework(component,
        core.WithPort(getPortFromEnv()),         // From PORT env var
        core.WithRedisURL(os.Getenv("REDIS_URL")),  // Required
        core.WithNamespace(os.Getenv("NAMESPACE")), // Optional
    )
}

func validateConfig() error {
    redisURL := os.Getenv("REDIS_URL")
    if redisURL == "" {
        return fmt.Errorf("REDIS_URL environment variable required")
    }
    // Validate format, etc.
    return nil
}

// ❌ BAD - Hardcoded
core.WithPort(8080)  // Don't do this!
```

**Required files:**
- `.env.example` - Documents all environment variables with examples
- `validateConfig()` - Checks required config at startup

---

### Naming Conventions

**Be consistent** - it helps developers (and AI) understand your code:

| What | Tool Pattern | Agent Pattern | Example |
|------|-------------|---------------|---------|
| Struct | `{Domain}Tool` | `{Domain}Agent` | `WeatherTool`, `ResearchAgent` |
| Constructor | `New{Domain}Tool()` | `New{Domain}Agent()` | `NewWeatherTool()` |
| Service Name | `{domain}-service` | `{domain}-assistant` | `weather-service`, `research-assistant` |
| Port Range | 833X-834X, 836X+ | 835X | Tools: 8333-8349 + 8363+, Agents: 8350-8359, UIs: 8360-8362 |

**Port allocation:**

All examples use ports starting from **8333** to avoid conflicts with common development ports. When the primary tool range (8333-8349) fills up, new tools use ports starting from **8363** (after the UI block at 8360-8362).

| Example | Host Port | NodePort | Type |
|---------|-----------|----------|------|
| **Tools** | | | |
| country-info-tool | 8333 | 30333 | tool |
| currency-tool | 8334 | 30334 | tool |
| geocoding-tool | 8335 | 30335 | tool |
| grocery-tool | 8336 | 30336 | tool |
| news-tool | 8337 | 30337 | tool |
| stock-market-tool | 8338 | 30338 | tool |
| weather-tool-v2 | 8339 | 30339 | tool |
| tool-example | 8340 | 30340 | tool |
| web-search-tool | 8341 | 30341 | tool |
| flight-tool | 8342 | 30342 | tool |
| hotel-tool | 8343 | 30343 | tool |
| places-tool | 8344 | 30344 | tool |
| travel-advisory-tool | 8345 | 30345 | tool |
| currency-global-tool | 8346 | 30346 | tool |
| devops-tool | 8347 | 30347 | tool |
| system-utilities-tool | 8348 | 30348 | tool |
| economic-data-tool | 8363 | 30363 | tool |
| fiscal-data-tool | 8364 | 30364 | tool |
| demographics-tool | 8365 | 30365 | tool |
| jira-tool | 8366 | 30366 | tool |
| clinical-trials-tool | 8367 | 30367 | tool |
| world-health-tool | 8368 | 30368 | tool |
| arxiv-tool | 8369 | 30369 | tool |
| semantic-scholar-tool | 8370 | 30370 | tool |
| prometheus-query-tool | 8371 | 30371 | tool |
| slack-tool | 8373 | 30373 | tool |
| openfda-tool | 8374 | 30374 | tool |
| pubmed-tool | 8375 | 30375 | tool |
| confluence-tool | 8376 | 30376 | tool |
| playwright-tool | 8349 | 30349 | tool |
| agentic-memory-tool | 8377 | 30377 | tool |
| devops-observability-tool | 8378 | 30378 | tool |
| scheduler-tool | 8379 | 30379 | tool |
| scheduled-executor | 8380 | 30380 | agent |
| github-tool | 8381 | 30381 | tool |
| *Available* | 8383+ | 30383+ | |
| **Agents** | | | |
| agent-example | 8350 | 30350 | agent |
| agent-with-async | 8351 | 30351 | agent |
| agent-with-human-approval | 8352 | 30352 | agent |
| agent-with-orchestration | 8353 | 30353 | agent |
| agent-with-resilience | 8354 | 30354 | agent |
| agent-with-telemetry | 8355 | 30355 | agent |
| travel-chat-agent | 8356 | 30356 | agent |
| devops-chat-agent | 8357 | 30357 | agent |
| qa-agent | 8358 | 30358 | agent |
| event-driven-agent | 8372 | 30372 | agent |
| github-pr-review-agent | 8382 | 30382 | agent |
| *Available* | 8359, 8383+ | 30359, 30383+ | |
| **UI Apps** | | | |
| chat-ui | 8360 | 30360 | ui |
| registry-viewer-app | 8361 | 30361 | ui |
| agent-with-human-approval (UI) | 8362 | 30362 | ui |

**Infrastructure Ports (unchanged):**
| Service | Host Port | NodePort |
|---------|-----------|----------|
| Grafana | 3000 | 30030 |
| Prometheus | 9090 | 30090 |
| Jaeger | 16686 | 31686 |

---

### Capability Registration

**Always include Phase 2 field hints** for AI accuracy:

```go
tool.RegisterCapability(core.Capability{
    Name:        "current_weather",
    Description: "Gets current weather conditions for a location",
    InputTypes:  []string{"json"},
    OutputTypes: []string{"json"},
    Handler:     w.handleCurrentWeather,

    // IMPORTANT: Include field hints for AI payload generation
    InputSummary: &core.SchemaSummary{
        RequiredFields: []core.FieldHint{
            {
                Name:        "location",
                Type:        "string",
                Example:     "London",
                Description: "City name or coordinates",
            },
        },
        OptionalFields: []core.FieldHint{
            {
                Name:        "units",
                Type:        "string",
                Example:     "metric",
                Description: "metric or imperial",
            },
        },
    },
})
```

---

### Main Function Structure

**Every example follows this exact pattern:**

```go
func main() {
    // 1. Validate configuration (fail fast)
    if err := validateConfig(); err != nil {
        log.Fatalf("Configuration error: %v", err)
    }

    // 2. Create component
    component := NewYourComponent()

    // 3. Get port from environment
    port := 8080 // default
    if portStr := os.Getenv("PORT"); portStr != "" {
        if p, err := strconv.Atoi(portStr); err == nil {
            port = p
        }
    }

    // 4. Create framework
    framework, err := core.NewFramework(component,
        core.WithPort(port),
        core.WithRedisURL(os.Getenv("REDIS_URL")),
        core.WithDiscovery(true, "redis"),
        core.WithCORS([]string{"*"}, true),
    )

    // 5. Display startup info (with emojis!)
    log.Println("🚀 Service Starting...")
    log.Printf("🌐 Port: %d\n", port)

    // 6. Graceful shutdown (30s timeout)
    ctx, cancel := context.WithCancel(context.Background())
    defer cancel()

    sigChan := make(chan os.Signal, 1)
    signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)

    go func() {
        <-sigChan
        log.Println("\n⚠️  Shutting down gracefully...")
        shutdownCtx, shutdownCancel := context.WithTimeout(
            context.Background(), 30*time.Second)
        defer shutdownCancel()
        cancel()
        // ... shutdown logic
    }()

    // 7. Run framework
    if err := framework.Run(ctx); err != nil &&
       !errors.Is(err, context.Canceled) {
        log.Fatalf("Framework error: %v", err)
    }
}
```

---

### Emoji Logging

**Use emojis for visual clarity** (makes logs easier to scan):

```go
// Startup
log.Println("🌤️  Weather Tool Service Starting...")
log.Println("🤖 Research Agent Starting...")

// Success
log.Println("✅ Shutdown completed")

// Warning
log.Println("⚠️  Warning: API key not set - using mock data")

// Error
log.Println("❌ Configuration error")

// Info
log.Printf("🌐 Server Port: %d\n", port)
log.Println("📋 Registered endpoints...")
```

---

### Error Handling

**Graceful degradation** - warn for optional features, fail for required:

```go
// ✅ GOOD - Warn but continue for optional features
func NewWeatherTool() *WeatherTool {
    apiKey := os.Getenv("WEATHER_API_KEY")
    if apiKey == "" {
        log.Println("⚠️  Warning: WEATHER_API_KEY not set - using mock data")
    }
    // Continue - tool still works with mock data
}

// ✅ GOOD - Fail fast for required features
func validateConfig() error {
    redisURL := os.Getenv("REDIS_URL")
    if redisURL == "" {
        return fmt.Errorf("REDIS_URL environment variable required")
    }
    return nil
}
```

---

### Docker Best Practices

**Use multi-stage builds** for small images:

```dockerfile
# Stage 1: Build
FROM golang:1.25-alpine AS builder
WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY *.go ./
RUN CGO_ENABLED=0 GOOS=linux go build -o service .

# Stage 2: Runtime (tools: ~15-26MB, agents: ~24-44MB depending on telemetry)
FROM alpine:latest
RUN adduser -D -u 1001 appuser
WORKDIR /app
COPY --from=builder /app/service .
RUN chown appuser:appuser /app
USER appuser
EXPOSE 8080
CMD ["./service"]
```

---

### Kubernetes Patterns

**Every example needs:**

1. **ConfigMap** - Non-sensitive config
2. **Secret** - API keys and credentials
3. **Deployment** - 2 replicas for HA
4. **Service** - ClusterIP for internal access
5. **Health Probes** - Liveness + Readiness

**Resource limits:**
```yaml
resources:
  requests:
    cpu: "200m"      # Tools
    memory: "256Mi"
  limits:
    cpu: "500m"      # Adjust for agents/AI
    memory: "512Mi"
```

---

### Required Supporting Files

**Every example must have:**

- `README.md` - Comprehensive documentation
- `Dockerfile` - Container build
- `k8-deployment.yaml` - Kubernetes manifests
- `Makefile` - Automation (setup, deploy, test, clean)
- `.env.example` - Configuration template
- `.gitignore` - Exclude binaries, .env, etc.
- `go.mod` - With versioned framework dependency
- `go.sum` - Checksums

---

### Tool vs Agent Distinctions

**Tools (Passive):**
- Use `core.NewTool()`
- Can register but NOT discover
- Provide focused capabilities
- Lightweight (200m CPU, 256Mi RAM)

**Agents (Active):**
- Use `core.NewBaseAgent()`
- Can discover AND register
- Orchestrate multiple tools
- More resources (500m CPU, 512Mi RAM)

---

### Pre-Commit Checklist

Before committing your new example, verify:

**Foundation:**
- [ ] go.mod has versioned framework dependency (no `replace`)
- [ ] No go.work file in example directory
- [ ] Standalone build works: `cp -r . /tmp/test && cd /tmp/test && go build .`
- [ ] Docker build works without framework source

**Code:**
- [ ] 4 focused Go files (main, component, handlers, logic)
- [ ] validateConfig() runs first in main()
- [ ] All config from environment variables
- [ ] Phase 2 InputSummary on all capabilities
- [ ] Emoji logging for visual clarity

**Documentation:**
- [ ] README.md with quick start (<5 min)
- [ ] .env.example documents all variables
- [ ] Architecture diagram (ASCII art is fine)
- [ ] Troubleshooting section

**Deployment:**
- [ ] Makefile with setup/deploy/test/clean
- [ ] Multi-stage Dockerfile
- [ ] k8-deployment.yaml with 2 replicas
- [ ] ConfigMap + Secret pattern
- [ ] Resource limits configured

**Production:**
- [ ] Graceful degradation for optional features
- [ ] 30-second shutdown timeout
- [ ] Health probes configured
- [ ] Port from correct range (808X or 809X)

---

### AI Coding Assistant Tips

When working with AI assistants (Claude, Copilot, etc.), use these prompts:

```
"Create a new Truva-G3 tool following the 4-file pattern from tool-example"

"Add a capability to {domain}_tool.go with Phase 2 InputSummary"

"Implement the handler in handlers.go for the {capability} capability"

"Update main.go to validate the {CONFIG_VAR} environment variable"

"Add graceful degradation for missing {OPTIONAL_FEATURE}"
```

**Pro tip:** Mention the specific file name for more focused AI responses.

---

## 16. Next Steps

1. **🏃 Run Quick Start** - See the full system (5 min)
2. **🔍 Pick Your Path** - Choose beginner/AI/advanced based on your needs (15-120 min)
3. **🧪 Test Interactions** - Agent + Tool orchestration (10 min)
4. **🎨 Customize** - Copy and modify examples for your use case (30 min)
5. **🏗️ Build Your Own** - Follow patterns above to create new examples (1-2 hours)
6. **☸️ Deploy** - Production Kubernetes deployment (1 hour)
7. **🤖 Launch** - Your intelligent distributed system is live!

## 17. Documentation

- **[Shared Setup Library](k8-deployment/setup-env-lib.sh)** - Common functions for all setup scripts
- **[Infrastructure Setup](k8-deployment/setup-infrastructure.sh)** - Ingress Controller + monitoring stack
- **[Cloud Deployment](CLOUD_DEPLOYMENT_GUIDE.md)** - Production deployment guide
- **[Getting Started](../docs/GETTING_STARTED.md)** - Framework introduction and prerequisites
- **[Distributed Tracing Guide](../docs/DISTRIBUTED_TRACING_GUIDE.md)** - End-to-end request tracing and log correlation
- **[Individual Examples](.)** - Each example has its own README
- **[Framework Core](../core/)** - Core framework documentation
- **[AI Integration](../ai/)** - AI provider configuration

---

**Ready to build intelligent distributed systems?**

Start with `cd travel-chat-agent && ./setup.sh full-deploy` and explore the examples. Each demonstrates different patterns for building AI-enhanced, discoverable, and orchestrable systems. All services are accessible via `*.localhost` Ingress routes — no port-forwarding needed.
