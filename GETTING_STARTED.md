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

> **Reference: author's local setup.** Not required — just a known-good baseline
> the maintainer develops against:
> - **Hardware:** MacBook Pro (M5 Max)
> - **Editor:** VS Code
> - **Coding agents:** [Claude Code](https://claude.com/claude-code) + [OpenAI Codex](https://github.com/openai/codex)
> - **Container runtime:** [OrbStack](https://orbstack.dev/)
> - **Kubernetes UI:** [Lens Desktop](https://k8slens.dev/) (see [Optional: Kubernetes UI](#optional-kubernetes-ui) below for the free alternative)

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

> **Not verified:** Windows installation steps have not been tested end-to-end by the maintainers. macOS and Linux are the supported platforms; the commands below are best-effort and may need adjustment. Please open an issue or PR if you hit problems.

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

### Optional: Kubernetes UI

A GUI helps with poking around the cluster — viewing pods, logs, events, port-forwarding — without typing `kubectl` every time. Pick one:

- **[Lens Desktop](https://k8slens.dev/)** — full-featured Kubernetes IDE. Free personal tier requires a Mirantis account; paid for commercial use beyond evaluation.
- **[Headlamp](https://headlamp.dev/)** — open-source (CNCF Sandbox), Microsoft-maintained. Like-for-like GUI alternative to Lens with no account required and no commercial-use restrictions. macOS: `brew install --cask headlamp`.
- **[k9s](https://k9scli.io/)** — terminal UI (vim-style keys), not a GUI, but extremely fast once you learn it. macOS: `brew install k9s`. Worth knowing even if you also run Lens or Headlamp.

---

## 2. Run the Examples First (Recommended)

The fastest way to understand TruvaG3 is to run a complete example end-to-end.
Two quickstarts are provided below — pick **travel** for a traditional
consumer-style demo (one AI key plus a couple of free public APIs), or
**devops** if you want the absolute minimum setup (one AI key, no other
credentials, no third-party APIs) or you're evaluating the framework against
an SRE/observability use case. Both share the same cluster and infrastructure,
so you can run them side-by-side.

> **Setup convention — always use `setup.sh`.** Every example (agent or
> tool) ships with a `setup.sh` that wires up Kind image-loading and
> Kubernetes deployment in one step. Don't `kubectl apply` manifests by
> hand — the script handles dependencies you'd otherwise miss.
>
> Two verbs cover the common cases:
>
> - **`./setup.sh full-deploy`** — cold start. Creates the Kind cluster,
>   rolls out shared infrastructure (Redis, OTEL Collector, Loki,
>   Prometheus, Jaeger, Grafana, ingress-nginx, registry-viewer), then
>   builds and deploys this example. Use on the **first** example you run.
> - **`./setup.sh deploy`** — cluster + infra already up. Skips the
>   cluster and infra phases, only builds and deploys this example. Use
>   for **every subsequent** agent or tool, including all the tools in
>   step 4 below.
>
> The full verb list and edge cases (`rollout` after `.env` edits,
> `rebuild` for forced rebuilds, `cleanup`/`clean` to remove) are in
> [Section 3 — Available Examples](#3-available-examples).

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
[**AI Providers Setup Guide**](docs/building/AI_PROVIDERS_SETUP_GUIDE.md). For this
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

Each tool uses `./setup.sh deploy` (not `full-deploy`) since the cluster
and shared infrastructure are already up from step 3 — see the setup
convention callout at the top of this section.

```bash
cd ../weather-tool-v2       && ./setup.sh deploy && cd -
cd ../geocoding-tool        && ./setup.sh deploy && cd -
cd ../currency-tool         && ./setup.sh deploy && cd -
cd ../country-info-tool     && ./setup.sh deploy && cd -
cd ../system-utilities-tool && ./setup.sh deploy && cd -
cd ../travel-advisory-tool  && ./setup.sh deploy && cd -
# (optional, see table below) cd ../news-tool          && ./setup.sh deploy && cd -
# (optional, see table below) cd ../flight-tool        && ./setup.sh deploy && cd -
# (optional, see table below) cd ../hotel-tool         && ./setup.sh deploy && cd -
# (optional, see table below) cd ../places-tool        && ./setup.sh deploy && cd -
```

The travel-chat-agent has no built-in travel knowledge — its LLM plans and
calls the tools below to answer questions. Each tool covers one slice of a
travel query; the more you deploy, the richer the answers the agent can
produce. Deploy the no-key tools at minimum; add the keyed tools to unlock
flights, hotels, local places, and headlines.

| Tool | What it gives the agent | API Key Required? | External Service | Path |
|------|-------------------------|-------------------|------------------|------|
| weather-tool-v2 | Current conditions and forecasts for any coordinate | No (free, no auth) | [Open-Meteo](https://open-meteo.com/) | [examples/weather-tool-v2/](examples/weather-tool-v2/) |
| geocoding-tool | City/landmark name → latitude/longitude (input for weather and places) | No (free, no auth) | [Nominatim / OpenStreetMap](https://nominatim.org/) | [examples/geocoding-tool/](examples/geocoding-tool/) |
| currency-tool | Currency conversion across 31 ECB currencies | No (free, no auth) | [Frankfurter](https://frankfurter.dev/) | [examples/currency-tool/](examples/currency-tool/) |
| country-info-tool | Country facts — capital, languages, currency code, region | No (free, no auth) | [RestCountries](https://restcountries.com/) | [examples/country-info-tool/](examples/country-info-tool/) |
| system-utilities-tool | Current time, timezone conversion, date math (e.g. "next Friday") | No (self-contained) | None — Go stdlib (timezone DB, date math) | [examples/system-utilities-tool/](examples/system-utilities-tool/) |
| travel-advisory-tool | Official US State Department safety advisories per country | No (free, no auth) | [Travel Advisories API](https://cadataapi.state.gov/) | [examples/travel-advisory-tool/](examples/travel-advisory-tool/) |
| news-tool | Destination news / current events | **Yes** — `GNEWS_API_KEY` | [GNews.io](https://gnews.io/) (free tier: 100 req/day) | [examples/news-tool/](examples/news-tool/) |
| flight-tool | Flight search and airport/city IATA-code lookup | **Yes** — `TRAVELPAYOUTS_TOKEN` | [Travelpayouts](https://www.travelpayouts.com/) (free signup) | [examples/flight-tool/](examples/flight-tool/) |
| hotel-tool | Hotel search by ISO country code + city name | **Yes** — `LITEAPI_KEY` | [LiteAPI](https://liteapi.travel/) | [examples/hotel-tool/](examples/hotel-tool/) |
| places-tool | Restaurants, attractions, and "nearby" search around coordinates | **Yes** — `FOURSQUARE_API_KEY` and/or `GEOAPIFY_API_KEY` | [Foursquare](https://location.foursquare.com/developer/) / [Geoapify](https://www.geoapify.com/places-api) | [examples/places-tool/](examples/places-tool/) |
| currency-global-tool | Currency conversion across 170+ currencies (richer alternative to currency-tool) | **Yes** — `CURRENCYBEACON_API_KEY` | [CurrencyBeacon](https://currencybeacon.com/) | [examples/currency-global-tool/](examples/currency-global-tool/) |

The first six tools (no-key) are deployed by the commands in step 4 above and
require no additional configuration. For each keyed tool, edit its `.env`
(e.g. `examples/flight-tool/.env`) and set the relevant key before running
`./setup.sh deploy`. The agent discovers whatever is running via Redis — you
can add or remove tools at any time and re-hit `/discover` to see the
updated catalog.

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

### Quick Start: DevOps Chat Agent

A minimum-setup alternative to the travel quickstart: needs **only an AI
provider key** and nothing else. The agent uses in-cluster `kubectl`,
Prometheus, Loki, and Jaeger to answer questions about the very cluster it
runs in — no third-party APIs to provision, no external credentials.

#### Step 1: Configure the AI provider key

Same provider table as the travel quickstart's Step 2. If you already set
`OPENAI_API_KEY` or `ANTHROPIC_API_KEY` for the travel agent, the same value
works here:

```bash
# From the truva-g3 repo root (clone first if you skipped the travel quickstart):
cd examples/devops-chat-agent
cp .env.example .env
# Edit .env and set OPENAI_API_KEY or ANTHROPIC_API_KEY
```

#### Step 2: Deploy the agent

```bash
./setup.sh full-deploy
```

If you already ran the travel quickstart, the cluster and infrastructure are
in place and `full-deploy` finishes in ~1–2 minutes (it skips the infra phase
and just builds and rolls out the devops agent itself — note that this
agent's `setup.sh` does **not** deploy `chat-ui`; that comes from Step 3
below or from the travel quickstart).

#### Step 3: Deploy the cluster-management tools (and chat-ui)

These six tools all talk to in-cluster services only — no external API keys.
The trailing `chat-ui` deploy makes the dashboard at http://chat.localhost
available; it's idempotent if the travel quickstart already deployed it:

```bash
cd ../devops-tool                && ./setup.sh deploy && cd -
cd ../devops-observability-tool  && ./setup.sh deploy && cd -
cd ../prometheus-query-tool      && ./setup.sh deploy && cd -
cd ../system-utilities-tool      && ./setup.sh deploy && cd -
cd ../scheduler-tool             && ./setup.sh deploy && cd -
cd ../agentic-memory-tool        && ./setup.sh deploy && cd -
cd ../chat-ui                    && ./setup.sh deploy && cd -   # frontend
```

| Tool | What it does | Talks to |
|------|--------------|----------|
| [devops-tool](examples/devops-tool/) | Flexible `kubectl` access (only `delete` blocked) | In-cluster ServiceAccount |
| [devops-observability-tool](examples/devops-observability-tool/) | Search logs and traces | Loki + Jaeger |
| [prometheus-query-tool](examples/prometheus-query-tool/) | Run PromQL queries | Prometheus |
| [system-utilities-tool](examples/system-utilities-tool/) | Time, date, timezone math | Go stdlib |
| [scheduler-tool](examples/scheduler-tool/) | Cron-style scheduled execution | In-cluster |
| [agentic-memory-tool](examples/agentic-memory-tool/) | Semantic memory recall | Redis + Qdrant |

#### Step 4: Test the agent

Open http://chat.localhost and click the **🛠️ DevOps Chat** card, or go
directly to http://chat.localhost/devops.html. The agent's API lives at
http://devops-chat-agent.localhost. Try:

- "What pods are unhealthy in the `truvag3-examples` namespace?"
- "Show me the last 10 errors from `travel-chat-agent` in Loki"
- "What's the memory usage trend for `qdrant` over the last hour?"

The agent discovers the deployed tools at runtime via the Redis registry,
asks the LLM to plan the right sequence of calls, and streams the answer
back via SSE.

### Access the Running System

All services are reachable via `*.localhost` ingress — no port-forwarding
required:

| Service | URL | Notes |
|---------|-----|-------|
| Chat dashboard | http://chat.localhost | **Launcher with cards** — your entry point (see below) |
| Travel Chat UI | http://chat.localhost/index.html | Direct link to the travel chat (the dashboard's "Travel Chat" card opens this) |
| DevOps Chat UI | http://chat.localhost/devops.html | Direct link to the devops chat (the dashboard's "DevOps Chat" card opens this) |
| Travel Agent API | http://travel-chat-agent.localhost | Direct API access (REST + SSE) |
| DevOps Agent API | http://devops-chat-agent.localhost | Direct API access (REST + SSE) |
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

### Test the Travel Agent

(For DevOps Chat Agent test queries, see Step 4 of the DevOps quickstart above.)

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

## 4. Build Your Own Components (with a coding agent)

TruvaG3 ships three fill-in-the-blanks scaffolds, each paired with a
**`PROMPT.md`** that drives a coding agent through the build. Pick the
scaffold that matches what you're building, copy it, and feed `PROMPT.md`
to your agent one step at a time.

### Pick a scaffold

| You want to build | Scaffold | Reference example |
|---|---|---|
| A capability provider that wraps an **external HTTP API** | [examples/my-tool/](examples/my-tool/) | [examples/stock-market-tool/](examples/stock-market-tool/) |
| A capability provider that wraps **local command execution** (kubectl, shell, npx, …) | [examples/my-tool/](examples/my-tool/) | [examples/devops-tool/](examples/devops-tool/) |
| A **streaming chat agent** (SSE, multi-tool orchestration, chat-ui frontend) | [examples/my-streaming-agent/](examples/my-streaming-agent/) | [examples/travel-chat-agent/](examples/travel-chat-agent/) or [examples/devops-chat-agent/](examples/devops-chat-agent/) |
| An **event-driven async agent** (webhooks, queues, scheduled triggers, optional HITL gating) | [examples/my-async-agent/](examples/my-async-agent/) | [examples/agent-with-async/](examples/agent-with-async/) |

> **`my-tool` covers two tool patterns.** Its `PROMPT.md` opens with a
> one-time choice — *external API facade* or *local command execution
> facade* — that selects the matching reference example. Subsequent steps
> are otherwise identical.

### How the workflow runs

Each `PROMPT.md` is structured as **12 self-contained steps**. You paste
each step into your coding agent, wait for it to finish, **review what it
produced**, and only then move on. The agent accumulates a `plan.md` along
the way — your record of API contracts, capability scope, and design
decisions.

The 12 steps follow a standard arc:

1. Explore (API docs / domain / tool dependencies)
2. Capture findings in `plan.md`
3. Read the relevant framework guide(s)
4. Study the reference example end-to-end
5. Implement
6–9. Vet against four framework guides chosen for your scaffold (one
   pass per guide; your `PROMPT.md` names which four — for tools they're
   the development guide, schema discovery, distributed tracing, and
   logging; for streaming and async agents the second guide is replaced
   by a shape-specific one such as async orchestration)
10. Allocate a port from the registry
11. Final pass against the reference for deviations
12. Deploy and verify (pod running, registered, traced, logged)

The paste-review-iterate loop is the point — don't queue all 12 prompts at
once. Course-correcting after each step is cheap; rolling back after
step 12 is not.

### Get started

```bash
# Pick a scaffold and copy it under a new name
cp -r examples/my-tool/ examples/<your-tool>/
cd examples/<your-tool>/

# Open PROMPT.md (in your editor, or in a pager) and start with Step 1
${EDITOR:-less} PROMPT.md
```

Same pattern for `my-streaming-agent` and `my-async-agent`.

### Tools vs agents at a glance

| Aspect | Tool | Agent |
|--------|------|-------|
| Role | Provides specific capabilities | Discovers and orchestrates |
| Discovery | Registers itself (passive) | Can discover others (active) |
| Base Type | `*core.BaseTool` | `*core.BaseAgent` |
| Constructor | `core.NewTool(name)` | `core.NewBaseAgent(name)` |

For an annotated minimal code example without leaving the doc tree, see
[examples/tool-example/](examples/tool-example/) and
[examples/agent-example/](examples/agent-example/) — the same patterns
your coding agent will study in Step 4.

### Framework guides referenced by the prompts

`PROMPT.md` will tell you which of these to read at Step 3 and which to
vet against in Steps 6–9:

- [docs/building/TOOL_DEVELOPMENT_GUIDE.md](docs/building/TOOL_DEVELOPMENT_GUIDE.md)
- [docs/building/AGENT_DEVELOPMENT_GUIDE.md](docs/building/AGENT_DEVELOPMENT_GUIDE.md)
- [docs/orchestration/ASYNC_ORCHESTRATION_GUIDE.md](docs/orchestration/ASYNC_ORCHESTRATION_GUIDE.md)
- [docs/building/TOOL_SCHEMA_DISCOVERY_GUIDE.md](docs/building/TOOL_SCHEMA_DISCOVERY_GUIDE.md)
- [docs/observability/DISTRIBUTED_TRACING_GUIDE.md](docs/observability/DISTRIBUTED_TRACING_GUIDE.md)
- [docs/observability/LOGGING_IMPLEMENTATION_GUIDE.md](docs/observability/LOGGING_IMPLEMENTATION_GUIDE.md)

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
- **[Agent Memory Guide](docs/memory-and-chat/AGENT_MEMORY_USER_GUIDE.md)** - Cross-agent shared memory, activity compaction, and real-time coordination
- **[Adding Context to Your Agent](docs/building/ADDING_CONTEXT_TO_YOUR_AGENT_GUIDE.md)** - Building custom pipeline hooks

### Resources

- [Full Documentation](README.md)
- [API Reference](docs/reference/API_REFERENCE.md)
- [Examples Directory](examples/README.md)
- [GitHub Issues](https://github.com/truvaagents/truva-g3/issues)

---

**Happy Building!** Start with the examples, then build your own tools and agents.
