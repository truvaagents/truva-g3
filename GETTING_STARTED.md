# Getting Started with TruvaG3

**Build intelligent AI agents and tools in Go that can discover and coordinate with each other.**

TruvaG3 is a Kubernetes-native framework for building AI agents and tools. Components discover each other automatically through Redis and coordinate to accomplish complex tasks.

**Why TruvaG3?**
- Ultra-lightweight: 15-44MB containers, ~100ms startup
- AI-native: Built-in support for Groq, OpenAI, Anthropic, Gemini, and more
- Auto-discovery: Components find each other automatically via Redis
- Kubernetes-native: Designed for K8s with health checks, metrics, easy deployment
- Batteries included: HTTP server, routing, middleware built-in

---

## 1. Prerequisites

TruvaG3 is designed to run on Kubernetes. For local development, we use [Kind](https://kind.sigs.k8s.io/) (Kubernetes in Docker).

### Required Software

> **Go version**: the framework's `go.mod` declares `go 1.26.2`, so building
> from source needs Go 1.26+. With Go's toolchain auto-upgrade (default since
> Go 1.21), an older Go install will fetch 1.26.2 on first build — but some
> corporate environments disable auto-upgrade, so installing a current Go
> directly is the simplest path.

**macOS:**
```bash
# Go (latest stable; needs ≥1.26 to build the framework)
brew install go
go version

# Docker Desktop (or Podman - see below)
brew install --cask docker

# Kind and kubectl
brew install kind kubectl
```

**Linux (Ubuntu/Debian):**
```bash
# Go (substitute the latest 1.26+ release for $GO_VERSION)
GO_VERSION=1.26.2
wget "https://go.dev/dl/go${GO_VERSION}.linux-amd64.tar.gz"
sudo rm -rf /usr/local/go
sudo tar -C /usr/local -xzf "go${GO_VERSION}.linux-amd64.tar.gz"
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
go version          # Should show go1.26+ (or earlier with toolchain auto-upgrade)
docker --version    # Should show Docker version (or: podman --version)
kind --version      # Should show Kind version
kubectl version --client
```

> **Linux/Windows note**: the deployment exposes services via `*.localhost`
> ingress hostnames. macOS resolves `*.localhost` automatically; on most Linux
> distros and Windows you may need to add the hostnames you'll use to your
> hosts file (e.g. `chat.localhost`, `travel-chat-agent.localhost`,
> `grafana.localhost`, `prometheus.localhost`, `jaeger.localhost` →
> `127.0.0.1`).

---

## 2. Run the Examples First (Recommended)

The fastest way to understand TruvaG3 is to run a complete example end-to-end.
We recommend starting with the **travel-chat-agent** — it exercises the full
framework: discovery, AI orchestration, multi-tool coordination, observability,
and a browser UI.

### Quick Start: Travel Chat Agent

#### Step 1: Clone the repository

```bash
git clone https://github.com/truvaagents/truva-g3.git
cd truva-g3/examples/travel-chat-agent
```

#### Step 2: Configure an AI provider API key (required)

> 🔑 **You must set at least one AI provider API key. Without one, the agent
> will start, but every chat request will fail at the LLM call.** The agent
> uses the LLM both to plan tool calls and to synthesize the final answer —
> there is no offline fallback.

```bash
cp .env.example .env
```

Open `.env` and set **one** of the following:

| Provider | Variable | Free tier? |
|----------|----------|------------|
| OpenAI | `OPENAI_API_KEY=sk-...` | No |
| Anthropic | `ANTHROPIC_API_KEY=sk-ant-...` | No |
| Groq | `GROQ_API_KEY=gsk-...` | **Yes** — quick to start |
| Google Gemini | `GEMINI_API_KEY=...` | Yes (limited) |

For multi-provider failover, custom model aliases, or other providers
(DeepSeek, Bedrock, Ollama, etc.), see the
[**AI Providers Setup Guide**](docs/AI_PROVIDERS_SETUP_GUIDE.md). For this
quick-start, one key is enough.

#### Step 3: Deploy the agent

```bash
# Cluster + infrastructure + agent + chat-ui in one command
./setup.sh full-deploy
```

The first run takes **~5–15 minutes**. `full-deploy` creates a Kind cluster
named `truvag3-demo-$(whoami)`, deploys the shared infrastructure (Redis,
OTEL Collector, Loki, Prometheus, Jaeger, Grafana, Swagger UI, Registry
Viewer, ingress-nginx), builds and loads the agent + chat-ui Docker images,
and waits for the rollouts to complete.

> **Forgot Step 2?** `setup.sh` auto-creates `.env` from `.env.example` on
> first run, but the placeholder keys won't authenticate. Edit `.env` after
> the deploy and run `./setup.sh rollout` to pick up the real values.

#### Step 4: Deploy the tools the agent will call

The agent has no built-in capabilities — it discovers tools via Redis at
runtime and asks the LLM to plan a sequence of tool calls. Without tools
deployed, the agent will respond but can't fetch live data.

```bash
cd ../weather-tool-v2       && ./setup.sh deploy && cd -
cd ../geocoding-tool        && ./setup.sh deploy && cd -
cd ../currency-tool         && ./setup.sh deploy && cd -
cd ../country-info-tool     && ./setup.sh deploy && cd -
cd ../system-utilities-tool && ./setup.sh deploy && cd -
# (optional, see table below) cd ../news-tool && ./setup.sh deploy && cd -
```

The travel-chat-agent orchestrates these tools to fulfil user queries:

| Tool | API Key Required? | External Service | Path |
|------|-------------------|------------------|------|
| weather-tool-v2 | No (free, no auth) | [Open-Meteo](https://open-meteo.com/) | [examples/weather-tool-v2/](examples/weather-tool-v2/) |
| geocoding-tool | No (free, no auth) | [Nominatim / OpenStreetMap](https://nominatim.org/) | [examples/geocoding-tool/](examples/geocoding-tool/) |
| currency-tool | No (free, no auth) | [Frankfurter](https://frankfurter.dev/) | [examples/currency-tool/](examples/currency-tool/) |
| country-info-tool | No (free, no auth) | [RestCountries](https://restcountries.com/) | [examples/country-info-tool/](examples/country-info-tool/) |
| system-utilities-tool | No (self-contained) | None — Go stdlib (timezone DB, date math) | [examples/system-utilities-tool/](examples/system-utilities-tool/) |
| news-tool | **Yes** — `GNEWS_API_KEY` | [GNews.io](https://gnews.io/) (free tier: 100 req/day) | [examples/news-tool/](examples/news-tool/) |

The first five are deployed in step 4 above and require no additional
configuration. For `news-tool`, edit `examples/news-tool/.env` and set
`GNEWS_API_KEY=...` before running `./setup.sh deploy`.

> **Why `system-utilities-tool`?** The agent has no built-in clock — without
> this tool it can't answer time-aware queries ("what time is it in Tokyo?",
> "if I leave New York at 9am, what time is that in London?"). The tool
> exposes `get_current_time`, `convert_timezone`, `list_timezones`, and
> `date_arithmetic` (plus a few non-travel capabilities the LLM ignores when
> they're not relevant to the query).

After all tools are running, verify the agent can see them:

```bash
curl http://travel-chat-agent.localhost/discover  # lists discovered tools
```

### Access the Running System

All services are reachable via `*.localhost` ingress — no port-forwarding
required:

| Service | URL | Notes |
|---------|-----|-------|
| Chat dashboard | http://chat.localhost | **Launcher with cards** — your entry point (see below) |
| Travel Chat UI | http://chat.localhost/index.html | Direct link to the travel chat (the dashboard's "Travel Chat" card opens this) |
| Agent API | http://travel-chat-agent.localhost | Direct API access (REST + SSE) |
| Grafana | http://grafana.localhost | admin / admin |
| Prometheus | http://prometheus.localhost | Raw metrics |
| Jaeger | http://jaeger.localhost | Distributed tracing |
| Swagger UI | http://swagger.localhost | Interactive OpenAPI explorer |
| Registry Viewer | http://registry.localhost | Real-time view of services registered in Redis |

> **`http://chat.localhost` is a launcher, not the chat itself.** The page
> shows cards for each available chat surface (Travel Chat, DevOps Chat,
> HITL Plan/Step Approval) plus links to Grafana, Prometheus, Jaeger, and
> the Registry Viewer. To talk to the travel agent, click the **🌍 Travel
> Chat** card (it opens `index.html` in a new tab) or visit
> `http://chat.localhost/index.html` directly.

### Test the Agent

**Via the Chat UI:**

1. Open http://chat.localhost in your browser.
2. Click the **🌍 Travel Chat** card (or go directly to http://chat.localhost/index.html).
3. Try queries like:

   - "What's the weather in Paris?"
   - "Plan a trip to Tokyo"
   - "What currency do they use in Japan?"

**Via curl (SSE streaming):**

```bash
# 1. Create a session
SESSION=$(curl -sS -X POST http://travel-chat-agent.localhost/chat/session | jq -r .session_id)

# 2. Stream a chat response
curl -N -X POST http://travel-chat-agent.localhost/chat/stream \
  -H "Content-Type: application/json" \
  -d "{\"session_id\":\"$SESSION\",\"message\":\"What is the weather in London?\"}"

# Health check
curl http://travel-chat-agent.localhost/health
```

### Clean Up

```bash
# Remove just this deployment (keep the cluster + infra for other examples)
./setup.sh cleanup

# Or delete the whole Kind cluster
kind delete cluster --name "truvag3-demo-$(whoami)"
```

---

## 3. Available Examples

TruvaG3 ships ~50 reference examples. Below is a curated subset; see
[examples/README.md](examples/README.md) for the full list.

> **Setup convention**: every example has a `setup.sh`. From a cold start
> (fresh clone, no cluster yet) use `full-deploy`. Once the cluster +
> infrastructure are already up from your first example, subsequent examples
> only need `deploy` (faster — skips cluster creation and infra rollout).

### Agents (active components — discover and orchestrate)

| Example | Description |
|---------|-------------|
| [travel-chat-agent](examples/travel-chat-agent/) | Real-time streaming chat with SSE, multi-tool coordination, web UI |
| [agent-example](examples/agent-example/) | Research assistant with AI orchestration |
| [agent-with-async](examples/agent-with-async/) | Async task processing with DAG execution |
| [agent-with-orchestration](examples/agent-with-orchestration/) | Predefined DAGs and dynamic AI planning |
| [agent-with-resilience](examples/agent-with-resilience/) | Circuit breakers, retries, graceful degradation |
| [agent-with-telemetry](examples/agent-with-telemetry/) | Full OpenTelemetry integration |
| [agent-with-human-approval](examples/agent-with-human-approval/) | Human-in-the-loop approval workflows |

### Tools (passive components — register capabilities)

| Example | Description |
|---------|-------------|
| [weather-tool-v2](examples/weather-tool-v2/) | Weather using Open-Meteo API (no key) |
| [geocoding-tool](examples/geocoding-tool/) | Forward + reverse geocoding |
| [currency-tool](examples/currency-tool/) | Currency conversion |
| [country-info-tool](examples/country-info-tool/) | Country information |
| [news-tool](examples/news-tool/) | News search and retrieval |
| [stock-market-tool](examples/stock-market-tool/) | Stock prices and market data |
| [tool-example](examples/tool-example/) | Minimal passive-tool template |
| [grocery-tool](examples/grocery-tool/) | Mock grocery store (resilience testing) |

Tools are reached by agents over ClusterIP within the cluster — they're not
typically exposed via ingress to the host.

### UI Applications

| Example | URL | Description |
|---------|-----|-------------|
| [chat-ui](examples/chat-ui/) | http://chat.localhost | Web interface for chat agents |
| [registry-viewer-app](examples/registry-viewer-app/) | (ClusterIP) | Visualize services in the Redis registry |

Neither app has its own `full-deploy` — both are deployed automatically as
part of any agent's `full-deploy`:

- **chat-ui** is built and deployed alongside the agent (e.g.
  `travel-chat-agent`'s setup.sh handles both images).
- **registry-viewer-app** is deployed by `truvag3_setup_infra` (the shared
  infrastructure setup), so it comes up with Redis, Grafana, etc. on the
  first agent's `full-deploy`.

To rebuild either standalone:

```bash
cd ../chat-ui && ./setup.sh deploy
cd ../registry-viewer-app && ./setup.sh rebuild   # rebuild only re-rolls the image
```

### Running Other Examples

```bash
cd examples/<example-name>

# Configure API keys / config (auto-bootstrapped from .env.example if missing)
cp .env.example .env            # if you want to edit before deploying

# Full local deployment from scratch — cluster + infra + this example
./setup.sh full-deploy

# Deploy to an existing cluster (cluster + infra already up)
./setup.sh deploy

# Common commands (run `./setup.sh` with no args to see the full list per example)
./setup.sh logs          # Follow logs
./setup.sh rollout       # Restart the deployment to pick up .env / config changes
./setup.sh rebuild       # Rebuild Docker image with --no-cache and redeploy
./setup.sh status        # Check deployment status (most examples)

# Removing a deployment — verb varies:
./setup.sh cleanup       # Used by agents (travel-chat-agent, devops-chat-agent, etc.)
./setup.sh clean         # Used by tools (currency-tool, weather-tool-v2, etc.)
```

> **Heads-up: `deploy` vs `rollout` after editing `.env`.** Both commands
> regenerate the Kubernetes Secret and ConfigMap from `.env`. The difference
> is whether the running pods are forced to restart afterwards:
>
> - **Agent examples** (e.g. `travel-chat-agent`) — `deploy` issues an
>   explicit `kubectl rollout restart`, so new pods pick up the new env
>   values automatically.
> - **Tool examples** (e.g. `currency-tool`, `weather-tool-v2`) — `deploy`
>   only `kubectl apply`s the manifest. If the manifest didn't change and
>   the image tag is still `:latest`, Kubernetes will not roll the
>   deployment, and the running pods keep their old env values.
>
> If you only edited `.env` (no code change), use **`./setup.sh rollout`** —
> it works consistently across both agents and tools by regenerating the
> Secret/ConfigMap and forcing a pod restart.

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
		core.WithCORS([]string{"*"}, true), // server-to-server only; if a browser will call this, use WithCORSDefaults() instead — see Section 5
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
	core.WithName("my-service"),            // Service name for discovery
	core.WithPort(8080),                    // HTTP server port
	core.WithNamespace("default"),          // K8s namespace for discovery
	core.WithRedisURL("redis://host:6379"), // Redis connection
	core.WithDiscovery(true, "redis"),      // Enable service discovery
	core.WithCORS([]string{"*"}, true),     // CORS — see "CORS choice" below
	core.WithDevelopmentMode(true),         // Development helpers
)
```

### CORS choice — `WithCORS` vs `WithCORSDefaults`

Two helpers, intended for different audiences:

| Helper | Allowed Headers | When to use |
|--------|-----------------|-------------|
| `core.WithCORS(origins, credentials)` | `Content-Type`, `Authorization` only | Server-to-server (agent ↔ tool, internal API) |
| `core.WithCORSDefaults()` | `*` (any header) — also sets origins `*`, credentials, all methods | Anything called from a **browser** (chat UIs, dashboards, SSE streams sending custom headers) |

If a browser sends `Accept`, `X-Requested-With`, or any custom header (most
SSE/streaming UIs do), the strict default of `WithCORS(...)` will reject the
preflight and the browser will fail with `Failed to connect to backend`.

For browser-facing components, prefer:

```go
core.WithCORSDefaults(), // browser UI needs more than Content-Type + Authorization
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

**Browser shows "Failed to connect to backend" when using a UI (e.g. http://chat.localhost):**

The browser's CORS preflight is being rejected. The agent is alive (its `/health` returns 200) but its CORS configuration only allows `Content-Type, Authorization` while the UI sends `Accept` / `X-User-ID` / etc.

Switch the agent's framework option from `core.WithCORS(...)` to `core.WithCORSDefaults()` (see Section 5), then `./setup.sh rebuild` to redeploy. Verify with:

```bash
curl -i -X OPTIONS http://travel-chat-agent.localhost/chat/stream \
  -H "Origin: http://chat.localhost" \
  -H "Access-Control-Request-Method: POST" \
  -H "Access-Control-Request-Headers: Content-Type, Accept, X-User-ID"
# Expect: Access-Control-Allow-Headers: *
```

**`.env` not loaded / missing API key:**

`setup.sh` auto-creates `.env` from `.env.example` if missing, but the placeholder keys won't authenticate. After editing `.env`:

```bash
./setup.sh rollout      # restart deployment to pick up new ConfigMap/Secret values
```

**Pods stuck in `ImagePullBackOff` / `ErrImageNeverPull`:**

Kind doesn't have access to local Docker images unless they're explicitly loaded. The deploy scripts handle this, but if a manual `kubectl apply` was used:

```bash
kind load docker-image my-tool:latest --name "truvag3-demo-$(whoami)"
kubectl rollout restart deployment/my-tool -n truvag3-examples
```

**Ingress URLs don't resolve (`*.localhost` returns NXDOMAIN):**

Common on Linux/Windows. Add the hostnames you use to your hosts file:

```
127.0.0.1 chat.localhost travel-chat-agent.localhost grafana.localhost prometheus.localhost jaeger.localhost swagger.localhost
```

macOS resolves `*.localhost` automatically.

**"connection refused" to Redis:**

```bash
# Check Redis pod (in the truvag3-examples namespace)
kubectl get pods -n truvag3-examples -l app=redis

# Port-forward Redis for local debugging
kubectl port-forward -n truvag3-examples svc/redis 6379:6379 &

# Test connectivity
redis-cli ping  # Should return "PONG"
```

**Components can't discover each other:**

```bash
# Check the registry
kubectl port-forward -n truvag3-examples svc/redis 6379:6379 &
redis-cli KEYS "truvag3:*"

# Verify a specific service is registered
redis-cli HGETALL "truvag3:services:my-tool"

# Check all components use the same namespace
kubectl get pods -n truvag3-examples
```

**Kind cluster issues:**

```bash
# Check cluster status
kind get clusters
kubectl cluster-info

# Recreate the cluster from scratch
kind delete cluster --name "truvag3-demo-$(whoami)"
cd examples/travel-chat-agent
./setup.sh full-deploy
```

---

## 8. Next Steps

### Recommended Learning Path

1. **Run examples** - Start with [travel-chat-agent](examples/travel-chat-agent/) to see everything working
2. **Explore patterns** - Study [agent-with-orchestration](examples/agent-with-orchestration/) for DAG workflows
3. **Add observability** - Try [agent-with-telemetry](examples/agent-with-telemetry/) for full monitoring
4. **Build resilience** - Learn from [agent-with-resilience](examples/agent-with-resilience/)

### Explore Advanced Features

- **[AI Module](ai/README.md)** - Multi-provider support with automatic failover
- **[Orchestration Module](orchestration/README.md)** - DAG workflows and AI-generated plans
- **[Telemetry Module](telemetry/README.md)** - OpenTelemetry integration
- **[Resilience Module](resilience/README.md)** - Circuit breakers and graceful degradation
- **[Agent Memory Guide](docs/AGENT_MEMORY_USER_GUIDE.md)** - Cross-agent shared memory, activity compaction, and real-time coordination
- **[Adding Context to Your Agent](docs/ADDING_CONTEXT_TO_YOUR_AGENT_GUIDE.md)** - Building custom pipeline hooks

### Resources

- [Full Documentation](README.md)
- [API Reference](docs/API_REFERENCE.md)
- [Examples Directory](examples/README.md)
- [GitHub Issues](https://github.com/truvaagents/truva-g3/issues)

---

**Happy Building!** Start with the examples, then build your own tools and agents.
