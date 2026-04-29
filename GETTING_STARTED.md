# Getting Started with Truva-G3

**Build intelligent AI agents and tools in Go that can discover and coordinate with each other.**

Truva-G3 is a Kubernetes-native framework for building AI agents and tools. Components discover each other automatically through Redis and coordinate to accomplish complex tasks.

**Why Truva-G3?**
- Ultra-lightweight: 15-44MB containers, ~100ms startup
- AI-native: Built-in support for Groq, OpenAI, Anthropic, Gemini, and more
- Auto-discovery: Components find each other automatically via Redis
- Kubernetes-native: Designed for K8s with health checks, metrics, easy deployment
- Batteries included: HTTP server, routing, middleware built-in

---

## 1. Prerequisites

Truva-G3 is designed to run on Kubernetes. For local development, we use [Kind](https://kind.sigs.k8s.io/) (Kubernetes in Docker).

### Required Software

**macOS:**
```bash
# Go 1.21+ (auto-upgrades to required version when needed)
brew install go
go version  # Should show go1.21+

# Docker Desktop (or Podman - see below)
brew install --cask docker

# Kind and kubectl
brew install kind kubectl
```

**Linux (Ubuntu/Debian):**
```bash
# Go 1.21+
wget https://golang.org/dl/go1.21.0.linux-amd64.tar.gz
sudo tar -C /usr/local -xzf go1.21.0.linux-amd64.tar.gz
echo 'export PATH=$PATH:/usr/local/go/bin' >> ~/.bashrc
source ~/.bashrc

# Docker
sudo apt update && sudo apt install -y docker.io
sudo systemctl start docker && sudo systemctl enable docker
sudo usermod -aG docker $USER
# Log out and back in

# Kind
curl -Lo ./kind https://kind.sigs.k8s.io/download/v0.20.0/kind-linux-amd64
chmod +x ./kind && sudo mv ./kind /usr/local/bin/kind

# kubectl
curl -LO "https://dl.k8s.io/release/$(curl -L -s https://dl.k8s.io/release/stable.txt)/bin/linux/amd64/kubectl"
chmod +x kubectl && sudo mv kubectl /usr/local/bin/
```

**Windows:**
```powershell
# Install via Chocolatey or download installers
choco install golang docker-desktop kind kubernetes-cli
# Restart required for Docker Desktop
```

### Alternative: Podman (Drop-in Docker Replacement)

[Podman](https://podman.io/) is a free, open-source container engine that works as a drop-in replacement for Docker. Commands are nearly identical - just replace `docker` with `podman`.

- **No daemon required** - Podman runs containers directly without a background service
- **Rootless by default** - Enhanced security without requiring root privileges
- **Free for all users** - No licensing fees (unlike Docker Desktop for large teams)
- **OCI-compliant** - Uses the same container image formats as Docker

```bash
# macOS
brew install podman
podman machine init && podman machine start

# Linux (Ubuntu/Debian)
sudo apt install podman

# Optional: Alias docker to podman
alias docker=podman

# Kind works with Podman via:
KIND_EXPERIMENTAL_PROVIDER=podman kind create cluster
```

For multi-container setups, use [Podman Compose](https://github.com/containers/podman-compose) which works with existing `docker-compose.yml` files.

### Verify Your Setup

```bash
go version          # Should show go1.21+
docker --version    # Should show Docker version (or: podman --version)
kind --version      # Should show Kind version
kubectl version --client
```

---

## 2. Run the Examples First (Recommended)

The fastest way to understand Truva-G3 is to run a complete example. We recommend starting with the **travel-chat-agent** which demonstrates the full framework capabilities.

### Quick Start: Travel Chat Agent

The travel-chat-agent example includes a one-command setup that creates everything you need:

```bash
# Clone the repository
git clone https://github.com/truvaagents/truva-g3.git
cd truva-g3/examples/travel-chat-agent

# Configure your AI provider (interactive prompt)
../setup-api-keys.sh

# Deploy everything with one command
./setup.sh full-deploy
```

This single command:
1. Creates a Kind cluster with proper port mappings
2. Deploys infrastructure (Redis, Prometheus, Grafana, Jaeger)
3. Deploys required tools (weather, geocoding, currency, country-info)
4. Builds and deploys the travel-chat-agent
5. Sets up port forwarding for local access

### Access the Running System

After deployment completes:

| Service | URL | Description |
|---------|-----|-------------|
| Chat UI | http://localhost:8360 | Web interface for chatting |
| Agent API | http://localhost:8356 | Direct API access |
| Grafana | http://localhost:3000 | Metrics dashboards |
| Prometheus | http://localhost:9090 | Raw metrics |
| Jaeger | http://localhost:16686 | Distributed tracing |

### Test the Agent

**Via Chat UI:**
Open http://localhost:8360 and try:
- "What's the weather in Paris?"
- "Plan a trip to Tokyo"
- "What currency do they use in Japan?"

**Via curl:**
```bash
# Chat endpoint (SSE streaming)
curl -N http://localhost:8356/chat \
  -H "Content-Type: application/json" \
  -d '{"message": "What is the weather in London?", "session_id": "test-session"}'

# Discover registered tools
curl http://localhost:8356/discover

# Health check
curl http://localhost:8356/health
```

### Clean Up

```bash
# Remove just this deployment
./setup.sh cleanup

# Delete the entire Kind cluster
kind delete cluster --name truvag3
```

---

## 3. Available Examples

Truva-G3 provides reference examples demonstrating various framework patterns.

### Agents (Active Components - Can Discover and Orchestrate)

| Example | Port | Description | Setup |
|---------|------|-------------|-------|
| [travel-chat-agent](../examples/travel-chat-agent/) | 8356 | Real-time streaming chat with SSE | `./setup.sh full-deploy` |
| [agent-example](../examples/agent-example/) | 8350 | Research assistant with AI orchestration | `./setup.sh run-all` |
| [agent-with-async](../examples/agent-with-async/) | 8351 | Async task processing with DAG execution | `./setup.sh run-all` |
| [agent-with-orchestration](../examples/agent-with-orchestration/) | 8353 | Predefined DAGs and dynamic AI planning | `./setup.sh run-all` |
| [agent-with-resilience](../examples/agent-with-resilience/) | 8354 | Circuit breakers, retries, graceful degradation | `./setup.sh run-all` |
| [agent-with-telemetry](../examples/agent-with-telemetry/) | 8355 | Full OpenTelemetry integration | `./setup.sh run-all` |
| [agent-with-human-approval](../examples/agent-with-human-approval/) | 8352 | Human-in-the-loop approval workflows | `./setup.sh run-all` |

### Tools (Passive Components - Register Capabilities)

| Example | Port | Description | Setup |
|---------|------|-------------|-------|
| [weather-tool-v2](../examples/weather-tool-v2/) | 8339 | Weather using Open-Meteo API (free) | `./setup.sh run-all` |
| [geocoding-tool](../examples/geocoding-tool/) | 8335 | Location geocoding | `./setup.sh run-all` |
| [currency-tool](../examples/currency-tool/) | 8334 | Currency conversion | `./setup.sh run-all` |
| [country-info-tool](../examples/country-info-tool/) | 8333 | Country information | `./setup.sh run-all` |
| [news-tool](../examples/news-tool/) | 8337 | News search and retrieval | `./setup.sh run-all` |
| [stock-market-tool](../examples/stock-market-tool/) | 8338 | Stock prices and market data | `./setup.sh run-all` |
| [tool-example](../examples/tool-example/) | 8340 | Basic weather tool pattern | `./setup.sh run-all` |
| [grocery-tool](../examples/grocery-tool/) | 8336 | Mock grocery store (resilience testing) | `./setup.sh run-all` |

### UI Applications

| Example | Port | Description | Setup |
|---------|------|-------------|-------|
| [chat-ui](../examples/chat-ui/) | 8360 | Web interface for chat agents | `./setup.sh run-all` |
| [registry-viewer-app](../examples/registry-viewer-app/) | 8361 | Visualize services in Redis registry | `./setup.sh run-all` |

### Running Other Examples

Each example has a consistent setup pattern:

```bash
cd examples/<example-name>

# Option 1: Full local deployment (Kind cluster + infrastructure + component)
./setup.sh full-deploy   # Creates everything if needed

# Option 2: Deploy to existing cluster
./setup.sh deploy        # Deploy to current K8s context
./setup.sh forward       # Port-forward for local access

# Common commands
./setup.sh status        # Check deployment status
./setup.sh test          # Run test scenarios
./setup.sh cleanup       # Remove this deployment
./setup.sh logs          # View logs
```

---

## 4. Build Your Own Components

After exploring the examples, you're ready to build your own tools and agents.

### Understanding Tools vs Agents

| Aspect | Tool | Agent |
|--------|------|-------|
| Role | Provides specific capabilities | Discovers and orchestrates |
| Discovery | Registers itself (passive) | Can discover others (active) |
| Base Type | `*core.BaseTool` | `*core.BaseAgent` |
| Constructor | `core.NewTool(name)` | `core.NewBaseAgent(name)` |
| Example | Weather service, Calculator | Research assistant, Coordinator |

### Creating a Tool

Tools are focused components that provide specific capabilities. Here's the pattern:

**main.go:**
```go
package main

import (
	"context"
	"errors"
	"log"
	"os"
	"os/signal"
	"strconv"
	"syscall"

	"github.com/truvaagents/truva-g3/core"
)

func main() {
	tool := NewMyTool()

	port := 8080
	if p := os.Getenv("PORT"); p != "" {
		if n, err := strconv.Atoi(p); err == nil {
			port = n
		}
	}

	framework, err := core.NewFramework(tool,
		core.WithName("my-tool"),
		core.WithPort(port),
		core.WithNamespace(os.Getenv("NAMESPACE")),
		core.WithRedisURL(os.Getenv("REDIS_URL")),
		core.WithDiscovery(true, "redis"),
		core.WithCORS([]string{"*"}, true),
	)
	if err != nil {
		log.Fatalf("Failed to create framework: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)
	go func() { <-sigChan; cancel() }()

	if err := framework.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
		log.Fatalf("Framework error: %v", err)
	}
}
```

**tool.go:**
```go
package main

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/truvaagents/truva-g3/core"
)

// MyTool provides specific capabilities
type MyTool struct {
	*core.BaseTool
}

func NewMyTool() *MyTool {
	tool := &MyTool{
		BaseTool: core.NewTool("my-tool"),
	}
	tool.registerCapabilities()
	return tool
}

func (t *MyTool) registerCapabilities() {
	t.RegisterCapability(core.Capability{
		Name:        "my_capability",
		Description: "What this does. Required: input (string).",
		InputTypes:  []string{"json"},
		OutputTypes: []string{"json"},
		Handler:     t.handleMyCapability,
		InputSummary: &core.SchemaSummary{
			RequiredFields: []core.FieldHint{
				{Name: "input", Type: "string", Example: "hello", Description: "Input value"},
			},
		},
	})
}

func (t *MyTool) handleMyCapability(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Input string `json:"input"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid JSON", http.StatusBadRequest)
		return
	}

	response := map[string]interface{}{
		"result":    "Processed: " + req.Input,
		"timestamp": time.Now().Format(time.RFC3339),
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(response)
}
```

### Creating an Agent

Agents can discover and orchestrate other components:

**agent.go:**
```go
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/truvaagents/truva-g3/core"
)

// MyAgent discovers and coordinates tools
type MyAgent struct {
	*core.BaseAgent
	httpClient *http.Client
}

func NewMyAgent() *MyAgent {
	agent := &MyAgent{
		BaseAgent: core.NewBaseAgent("my-agent"),
		httpClient: &http.Client{
			Timeout: 30 * time.Second,
			Transport: &http.Transport{
				MaxIdleConns:        100,
				MaxIdleConnsPerHost: 10,
				IdleConnTimeout:     90 * time.Second,
			},
		},
	}
	agent.registerCapabilities()
	return agent
}

func (a *MyAgent) registerCapabilities() {
	a.RegisterCapability(core.Capability{
		Name:        "orchestrate",
		Description: "Orchestrates tools to accomplish tasks",
		InputTypes:  []string{"json"},
		OutputTypes: []string{"json"},
		Handler:     a.handleOrchestrate,
	})
}

func (a *MyAgent) handleOrchestrate(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	// Discover available tools using the agent's Discovery field
	tools, err := a.Discovery.Discover(ctx, core.DiscoveryFilter{
		Type: core.ComponentTypeTool,
	})
	if err != nil {
		http.Error(w, fmt.Sprintf("Discovery failed: %v", err), http.StatusServiceUnavailable)
		return
	}

	// Find and call a specific tool
	var targetTool *core.ServiceInfo
	for _, tool := range tools {
		if tool.Name == "my-tool" {
			targetTool = tool
			break
		}
	}

	if targetTool == nil {
		http.Error(w, "Required tool not found", http.StatusServiceUnavailable)
		return
	}

	// Call the tool
	result, err := a.callTool(ctx, targetTool, "my_capability", map[string]interface{}{
		"input": "test",
	})
	if err != nil {
		http.Error(w, fmt.Sprintf("Tool call failed: %v", err), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(result)
}

func (a *MyAgent) callTool(ctx context.Context, tool *core.ServiceInfo, capability string, data interface{}) (map[string]interface{}, error) {
	url := fmt.Sprintf("http://%s:%d/api/capabilities/%s", tool.Address, tool.Port, capability)
	jsonData, _ := json.Marshal(data)

	req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewBuffer(jsonData))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := a.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	var result map[string]interface{}
	json.NewDecoder(resp.Body).Decode(&result)
	return result, nil
}
```

### Kubernetes Deployment

Create a `k8-deployment.yaml` for your component:

```yaml
apiVersion: apps/v1
kind: Deployment
metadata:
  name: my-tool
  labels:
    app: my-tool
spec:
  replicas: 1
  selector:
    matchLabels:
      app: my-tool
  template:
    metadata:
      labels:
        app: my-tool
    spec:
      containers:
      - name: my-tool
        image: my-tool:latest
        imagePullPolicy: IfNotPresent
        ports:
        - containerPort: 8080
        env:
        - name: PORT
          value: "8080"
        - name: REDIS_URL
          value: "redis://redis:6379"
        - name: NAMESPACE
          valueFrom:
            fieldRef:
              fieldPath: metadata.namespace
        livenessProbe:
          httpGet:
            path: /health
            port: 8080
          initialDelaySeconds: 5
          periodSeconds: 10
        readinessProbe:
          httpGet:
            path: /health
            port: 8080
          initialDelaySeconds: 3
          periodSeconds: 5
---
apiVersion: v1
kind: Service
metadata:
  name: my-tool
spec:
  selector:
    app: my-tool
  ports:
  - port: 8080
    targetPort: 8080
```

---

## 5. Framework Options Reference

### Core Options

```go
framework, err := core.NewFramework(component,
	core.WithName("my-service"),           // Service name for discovery
	core.WithPort(8080),                   // HTTP server port
	core.WithNamespace("default"),         // K8s namespace for discovery
	core.WithRedisURL("redis://host:6379"), // Redis connection
	core.WithDiscovery(true, "redis"),     // Enable service discovery
	core.WithCORS([]string{"*"}, true),    // CORS configuration
	core.WithDevelopmentMode(true),        // Development helpers
)
```

### Resilience Options

```go
import "time"

framework, err := core.NewFramework(component,
	// Circuit breaker: opens after 5 failures, resets after 30 seconds
	core.WithCircuitBreaker(5, 30*time.Second),

	// Retry: up to 3 attempts with 100ms initial interval (exponential backoff)
	core.WithRetry(3, 100*time.Millisecond),
)
```

### AI Integration

```go
import (
	"github.com/truvaagents/truva-g3/ai"
	"github.com/truvaagents/truva-g3/core"
)

// Auto-configured AI client - detects from environment
aiClient, err := ai.NewClient()
if err != nil {
	log.Printf("AI not available: %v", err)
}

// Use in your agent
if aiClient != nil {
	response, err := aiClient.GenerateResponse(ctx, "Your prompt", &core.AIOptions{
		Temperature: 0.7,
		MaxTokens:   1000,
	})
}
```

### Telemetry Integration

```go
import "github.com/truvaagents/truva-g3/telemetry"

// Initialize telemetry
config := telemetry.UseProfile(telemetry.ProfileDevelopment)
config.ServiceName = "my-service"
config.Endpoint = os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT")

telemetry.Initialize(config)
defer telemetry.Shutdown(context.Background())

// Enable framework integration
telemetry.EnableFrameworkIntegration(nil)

// Add tracing middleware
framework, err := core.NewFramework(component,
	core.WithMiddleware(telemetry.TracingMiddleware("my-service")),
)
```

---

## 6. Environment Variables

### Core Configuration

```bash
# Required
REDIS_URL=redis://localhost:6379  # Redis connection for discovery
PORT=8080                         # HTTP server port

# Recommended
NAMESPACE=default                 # K8s namespace for service discovery
DEV_MODE=true                     # Enable development mode
```

### AI Providers (set one)

```bash
GROQ_API_KEY=gsk-...              # Groq (free tier available)
OPENAI_API_KEY=sk-...             # OpenAI
ANTHROPIC_API_KEY=sk-ant-...      # Anthropic
GEMINI_API_KEY=...                # Google Gemini
DEEPSEEK_API_KEY=...              # DeepSeek
```

### Logging

```bash
TRUVAG3_LOG_LEVEL=debug            # debug, info, warn, error
TRUVAG3_LOG_FORMAT=json            # json or text
```

### Telemetry

```bash
APP_ENV=development               # development, staging, production
OTEL_EXPORTER_OTLP_ENDPOINT=http://localhost:4318
```

---

## 7. Troubleshooting

### Enable Debug Logging

```bash
# Local development
export TRUVAG3_LOG_LEVEL=debug
go run .

# Kubernetes
kubectl set env deployment/my-service TRUVAG3_LOG_LEVEL=debug
kubectl logs -f deployment/my-service
```

### Common Issues

**"connection refused" to Redis:**
```bash
# Check Redis pod
kubectl get pods | grep redis

# Port-forward Redis
kubectl port-forward svc/redis 6379:6379 &

# Test connectivity
redis-cli ping  # Should return "PONG"
```

**Components can't discover each other:**
```bash
# Check Redis keys
kubectl port-forward svc/redis 6379:6379 &
redis-cli KEYS "*"

# Verify registration
redis-cli HGETALL "truvag3:services:my-tool"

# Check all components use same namespace
kubectl get pods -n default
```

**Port already in use:**
```bash
# Find existing port-forwards
ps aux | grep port-forward

# Kill them
pkill -f "kubectl port-forward"

# Or use different port
export PORT=8081
```

**Kind cluster issues:**
```bash
# Check cluster status
kind get clusters
kubectl cluster-info

# Restart cluster
kind delete cluster --name truvag3
./setup-kind-demo.sh setup
```

---

## 8. Next Steps

### Recommended Learning Path

1. **Run examples** - Start with [travel-chat-agent](../examples/travel-chat-agent/) to see everything working
2. **Explore patterns** - Study [agent-with-orchestration](../examples/agent-with-orchestration/) for DAG workflows
3. **Add observability** - Try [agent-with-telemetry](../examples/agent-with-telemetry/) for full monitoring
4. **Build resilience** - Learn from [agent-with-resilience](../examples/agent-with-resilience/)

### Explore Advanced Features

- **[AI Module](../ai/README.md)** - Multi-provider support with automatic failover
- **[Orchestration Module](../orchestration/README.md)** - DAG workflows and AI-generated plans
- **[Telemetry Module](../telemetry/README.md)** - OpenTelemetry integration
- **[Resilience Module](../resilience/README.md)** - Circuit breakers and graceful degradation
- **[Agent Memory Guide](AGENT_MEMORY_USER_GUIDE.md)** - Cross-agent shared memory, activity compaction, and real-time coordination
- **[Adding Context to Your Agent](ADDING_CONTEXT_TO_YOUR_AGENT_GUIDE.md)** - Building custom pipeline hooks

### Resources

- [Full Documentation](../README.md)
- [API Reference](API_REFERENCE.md)
- [Examples Directory](../examples/README.md)
- [GitHub Issues](https://github.com/truvaagents/truva-g3/issues)

---

**Happy Building!** Start with the examples, then build your own tools and agents.
