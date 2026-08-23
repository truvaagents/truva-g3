# Getting Started with TruvaG3

**Build intelligent AI agents and tools in Go that can discover and coordinate with each other.**

TruvaG3 is a Kubernetes-native framework for building AI agents and tools. Components discover each other automatically through Redis and coordinate to accomplish complex tasks.

**Why TruvaG3?**
- Ultra-lightweight: 15-44MB containers, ~100ms startup
- AI-native: Built-in support for OpenAI, Anthropic, OpenRouter, Gemini, Groq, and more
- Auto-discovery: Components find each other automatically via Redis
- Kubernetes-native: Designed for K8s with health checks, metrics, easy deployment
- Batteries included: HTTP server, routing, middleware built-in
- Reusable skills: Versioned developer-bound guidance loaded progressively by orchestration

---

## 1. Prerequisites

> **Reference: author's local setup.** Not required — just a known-good baseline
> the maintainer develops against:
> - **Hardware:** MacBook Pro (M5 Max)
> - **Editor:** VS Code
> - **Coding agents:** [Claude Code](https://claude.com/claude-code) + [OpenAI Codex](https://github.com/openai/codex)
> - **Container runtime:** [OrbStack](https://orbstack.dev/)
> - **Kubernetes UI:** [Lens Desktop](https://k8slens.dev/) (see [Optional: Kubernetes UI](#optional-kubernetes-ui) below for the free alternative)
> - **AI models:** Each agent's `.env.example` ships the author's defaults
>   (e.g. `gpt-5.6-terra`, `claude-sonnet-5`, `gpt-oss-120b`). During
>   development, frontier non-reasoning models have been consistently more
>   effective for this framework's workloads than dedicated reasoning
>   models, which tend to add latency without clearly improving outcomes
>   here. Treat these as a starting point; override per-agent in `.env`.

TruvaG3 is designed to run on Kubernetes. For local development, we use [Kind](https://kind.sigs.k8s.io/) (Kubernetes in Docker).

Agent Skills are optional and disabled by default. The travel, DevOps, QA, and
event-driven agents demonstrate them. Their setup scripts discover packages at
`skills/packages/<namespace>/<name>.json`, validate and conditionally publish
them through the Registry Viewer management API, and then start agents whose
code explicitly binds those skills. Use `./setup.sh skills-check` for a
read-only comparison with Git or `./setup.sh skills-sync` to reconstruct an
empty or drifted runtime registry without rebuilding or restarting the agent.
Automatic synchronization during deployment is best-effort: it warns and lets
the agent deploy if the management API is unavailable. The explicit commands
remain strict. Set `TRUVAG3_SKIP_SKILLS_SYNC=true` when the setup host
intentionally cannot reach the API.
Open `http://registry.localhost/` and select **Skills** to inspect packages, or
open an execution and select its **Skills** tab to inspect body-free runtime
decisions. See the [Agent Skills Guide](docs/orchestration/AGENT_SKILLS_GUIDE.md)
before enabling skills in your own agent.

### Required Software

> **Go version**: the framework's `go.mod` declares `go 1.26.6`, so building
> from source needs Go 1.26+. With Go's toolchain auto-upgrade (default since
> Go 1.21), an older Go install will fetch 1.26.6 on first build — but some
> controlled environments disable auto-upgrade, so installing a current Go
> directly is the simplest path.

**macOS:**
```bash
# Go (latest stable; needs ≥1.26 to build the framework)
brew install go
go version

# Docker Desktop (or Podman - see below)
brew install --cask docker

# Kind, kubectl, and jq (used by skill package verification)
brew install kind kubectl jq
```

**Linux (Ubuntu/Debian):**
```bash
# Go (substitute the latest 1.26+ release for $GO_VERSION)
GO_VERSION=1.26.6
wget "https://go.dev/dl/go${GO_VERSION}.linux-amd64.tar.gz"
sudo rm -rf /usr/local/go
sudo tar -C /usr/local -xzf "go${GO_VERSION}.linux-amd64.tar.gz"
echo 'export PATH=$PATH:/usr/local/go/bin' >> ~/.bashrc
source ~/.bashrc

# Docker and jq (used by skill package verification)
sudo apt update && sudo apt install -y docker.io jq
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

**Windows (via WSL2 + Ubuntu 24.04 — supported path):**

The setup scripts under `examples/<name>/setup.sh` are bash and rely on Linux toolchains (kind, kubectl, Unix paths). The recommended Windows path is to run them inside WSL2 with Ubuntu 24.04, using Docker Desktop on the Windows host for the container runtime.

From PowerShell (run once):

```powershell
wsl --install -d Ubuntu-24.04
winget install Docker.DockerDesktop
```

Start Docker Desktop and enable **Settings → Resources → WSL Integration** for `Ubuntu-24.04`. Then restart WSL so the integration takes effect:

```powershell
wsl --set-default Ubuntu-24.04
wsl --shutdown
```

Open the Ubuntu shell and verify the container runtime is coming from Docker Desktop (not a stray WSL-native engine):

```bash
docker info --format '{{.OperatingSystem}} {{.ServerVersion}}'
# expected: Docker Desktop <version>
```

If this prints `Docker Engine` or `Ubuntu` instead of `Docker Desktop`, a WSL-native Docker installation is shadowing the integration — an unsupported configuration. Do not install Docker Engine inside Ubuntu; rely on Docker Desktop's WSL integration for the runtime.

Then follow the **Linux** prerequisites above (Go, kind, kubectl); `docker` itself already works in the Ubuntu shell via the WSL integration.

> **Paste Linux commands into the Ubuntu shell — not into PowerShell.** PowerShell aliases `curl` to `Invoke-WebRequest` and pre-evaluates `$(...)`, which breaks the `kubectl` install line (and similar). Use PowerShell only for `wsl --install` and `winget install Docker.DockerDesktop`; everything after that runs in Ubuntu. See [WINDOWS_TROUBLESHOOTING — Linux commands fail when run from PowerShell](docs/reference/WINDOWS_TROUBLESHOOTING.md#linux-commands-fail-when-run-from-powershell).

Clone the repo into the WSL filesystem (`~/truva-g3`) rather than `/mnt/c/...` — the WSL ext4 is noticeably faster and avoids permission and CRLF line-ending issues. The same goes for `.env` files: create and edit them inside Ubuntu, not in a Windows editor copied across — Windows line endings will fail `setup.sh` with `$'\r': command not found`. See [WINDOWS_TROUBLESHOOTING — CRLF line-ending errors](docs/reference/WINDOWS_TROUBLESHOOTING.md#crlf-line-ending-errors).

> **Native Windows (without WSL) is not supported.** The setup scripts are bash; running them under PowerShell or cmd is fragile. For Windows-specific issues (clock drift breaking service discovery, single-node ingress restart deadlock, CRLF line-ending errors), see [Windows + WSL2 Troubleshooting](docs/reference/WINDOWS_TROUBLESHOOTING.md).

### Alternative: Podman (Drop-in Docker Replacement)

[Podman](https://podman.io/) is a free, open-source container engine that works as a drop-in replacement for Docker. Commands are nearly identical - just replace `docker` with `podman`.

- **No daemon required** - Podman runs containers directly without a background service
- **Rootless by default** - Enhanced security without requiring root privileges
- **Free for all users** - No licensing fees (unlike Docker Desktop for large teams)
- **OCI-compliant** - Uses the same container image formats as Docker

```bash
# macOS — note: kind needs a ROOTFUL podman machine
brew install podman
podman machine init --rootful && podman machine start

# Linux (Ubuntu/Debian)
sudo apt install podman

# The setup scripts auto-detect the running runtime (preferring Docker if its
# daemon is up). To force podman — e.g. when Docker is also installed — set this.
# An `alias docker=podman` will NOT work: setup.sh runs non-interactively.
export TRUVAG3_CONTAINER_RUNTIME=podman

# Kind works with Podman via:
KIND_EXPERIMENTAL_PROVIDER=podman kind create cluster
```

> **On Podman, especially beyond the quickstart?** macOS needs a rootful
> machine, `alias docker=podman` won't reach `setup.sh`, the machine often
> needs sizing up for the full example catalog, and there's a known gvproxy
> ingress quirk. These (with upstream issue links) are collected in
> [Podman Troubleshooting](docs/reference/PODMAN_TROUBLESHOOTING.md) — the
> quickstart's ~7 tools work on the defaults.

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

### Optional: Ollama (for persistent agentic memory)

Both chat agents wire **per-user memory** — facts that come up in conversation (preferences, recurring questions, prior destinations, common clusters) are embedded and recalled in future sessions so the user doesn't have to repeat themselves. The embedding step uses an OpenAI-compatible endpoint, configured via `TRUVAG3_EMBEDDING_BASE_URL` in each agent's `.env.example` and defaulted to a local [Ollama](https://ollama.ai/) running the `nomic-embed-text` model.

**Skip this if you don't want persistent memory.** Both agents boot and run fine without Ollama — the framework doesn't panic when the embedding endpoint is unreachable; chats, planning, and tool calls all continue to work. You just don't get cross-session personalization.

To enable it:

1. Install Ollama from https://ollama.ai/.
2. Pull the embedding model:
   ```bash
   ollama pull nomic-embed-text
   ```
3. Make sure Ollama is running (default: `http://localhost:11434`).

The shipped `.env.example` files point at `http://host.docker.internal:11434/v1`, which resolves to your host from inside the Kind cluster on Docker Desktop (macOS/Windows). On Linux without Docker Desktop you may need to point `TRUVAG3_EMBEDDING_BASE_URL` at your host's reachable IP, or run Ollama inside the cluster.

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

#### Step 2: Configure an AI provider (required)

> 🔑 **You must configure at least one AI provider. Without one, the agent
> will start, but every chat request will fail at the LLM call.** The agent
> uses the LLM both to plan tool calls and to synthesize the final answer.
> The fastest path is a cloud API key (table below — Groq has a free tier).
> If you'd rather stay fully local, point the agent at a [local Ollama
> instance](docs/building/AI_PROVIDERS_SETUP_GUIDE.md#scenario-1-local-development-with-ollama)
> instead — no API key needed, but you'll need to install Ollama and pull a
> model that fits your machine.

```bash
cp .env.example .env
```

Open `.env` and set **one** of the following:

| Provider | Variable | Free tier? |
|----------|----------|------------|
| OpenAI | `OPENAI_API_KEY=sk-...` | No |
| Anthropic | `ANTHROPIC_API_KEY=sk-ant-...` | No |
| OpenRouter | `OPENROUTER_API_KEY=...` | Route-dependent; no built-in `free` alias |
| Groq | `GROQ_API_KEY=gsk-...` | **Yes** — quick to start |
| Google Gemini | `GOOGLE_API_KEY=...` | Yes (limited) |

For Gemini, use a current Google AI Studio **auth key**. The provider also
accepts `GEMINI_API_KEY`, but `GOOGLE_API_KEY` wins when both are set. Google
already rejects unrestricted Standard keys and will reject every Standard key
in September 2026; see the
[Gemini API-key guide](https://ai.google.dev/gemini-api/docs/api-key).

OpenRouter auto-detects immediately after Anthropic. Its framework default is
the `openrouter/auto` router; set
`TRUVAG3_OPENROUTER_MODEL_DEFAULT=openai/gpt-5.6-sol` to pin that concrete model.
The framework does not currently advertise a `free` alias because tested free
routes failed under its mandatory privacy constraints. Exact `:free` model IDs
remain experimental and are subject to OpenRouter's
[current free-model and account limits](https://openrouter.ai/docs/faq).

Adding `OPENROUTER_API_KEY` can change an unpinned `ai.NewClient()` or
`ai.NewRequestClient()` selection: when neither OpenAI nor Anthropic is
available, OpenRouter is selected ahead of Gemini and the remaining providers.
Pin it when provider identity must not depend on which keys happen to exist:

```go
client, err := ai.NewClient(
    ai.WithProviderAlias("openai.openrouter"),
    ai.WithModel("default"),
)
```

For multi-provider failover, custom model aliases, or other providers
(DeepSeek, Bedrock, Ollama, etc.), see the
[**AI Providers Setup Guide**](docs/building/AI_PROVIDERS_SETUP_GUIDE.md). For this
quick-start, one provider is enough.

> 💾 **Optional: persistent user memory.** The travel agent wires per-user memory (preferences, past destinations) via an embedding endpoint, pre-configured in `.env.example` for a local Ollama. Install Ollama and pull `nomic-embed-text` to enable it — see [Optional: Ollama (for persistent agentic memory)](#optional-ollama-for-persistent-agentic-memory). The agent still works without Ollama; cross-session memory just won't persist.

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
cd ../scheduler-tool        && ./setup.sh deploy && cd -
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
| country-info-tool | Country facts — capital, languages, currency code, region | No (free, no auth) | Bundled dataset + [apicountries.com](https://apicountries.com/) | [examples/country-info-tool/](examples/country-info-tool/) |
| system-utilities-tool | Current time, timezone conversion, date math (e.g. "next Friday") | No (self-contained) | None — Go stdlib (timezone DB, date math) | [examples/system-utilities-tool/](examples/system-utilities-tool/) |
| travel-advisory-tool | Official US State Department safety advisories per country | No (free, no auth) | [Travel Advisories API](https://cadataapi.state.gov/) | [examples/travel-advisory-tool/](examples/travel-advisory-tool/) |
| scheduler-tool | Schedule delayed and recurring (cron-style) tasks for the agent to run later | No (self-contained) | None — in-cluster | [examples/scheduler-tool/](examples/scheduler-tool/) |
| news-tool | Destination news / current events | **Yes** — `GNEWS_API_KEY` | [GNews.io](https://gnews.io/) (free tier: 100 req/day) | [examples/news-tool/](examples/news-tool/) |
| flight-tool | Flight search and airport/city IATA-code lookup | **Yes** — `TRAVELPAYOUTS_TOKEN` | [Travelpayouts](https://www.travelpayouts.com/) (free signup) | [examples/flight-tool/](examples/flight-tool/) |
| hotel-tool | Hotel search by ISO country code + city name | **Yes** — `LITEAPI_KEY` | [LiteAPI](https://liteapi.travel/) | [examples/hotel-tool/](examples/hotel-tool/) |
| places-tool | Restaurants, attractions, and "nearby" search around coordinates | **Yes** — `FOURSQUARE_API_KEY` and/or `GEOAPIFY_API_KEY` | [Foursquare](https://location.foursquare.com/developer/) / [Geoapify](https://www.geoapify.com/places-api) | [examples/places-tool/](examples/places-tool/) |
| currency-global-tool | Currency conversion across 170+ currencies (richer alternative to currency-tool) | **Yes** — `CURRENCYBEACON_API_KEY` | [CurrencyBeacon](https://currencybeacon.com/) | [examples/currency-global-tool/](examples/currency-global-tool/) |

The first seven tools (no-key) are deployed by the commands in step 4 above and
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

#### Step 1: Configure an AI provider

Same provider table as the travel quickstart's Step 2. If you already set a
provider key such as `OPENAI_API_KEY`, `ANTHROPIC_API_KEY`, or
`OPENROUTER_API_KEY` for the travel agent, the same value works here:

```bash
# From the truva-g3 repo root (clone first if you skipped the travel quickstart):
cd examples/devops-chat-agent
cp .env.example .env
# Edit .env and set one provider key, for example OPENROUTER_API_KEY
```

> 💾 **Optional: persistent agentic memory.** The devops agent (like the travel agent) wires per-user memory and also uses the `agentic-memory-tool` deployed in Step 3 for semantic recall. Both rely on an embedding endpoint, pre-configured in `.env.example` for a local Ollama. Install Ollama and pull `nomic-embed-text` to enable it — see [Optional: Ollama (for persistent agentic memory)](#optional-ollama-for-persistent-agentic-memory). The agent works without Ollama; the tool still deploys but semantic recall isn't available.

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

These six tools (plus `scheduled-executor`, the internal coordinator that
drains `scheduler-tool`'s queue and dispatches due tasks to target agents)
all talk to in-cluster services only — no external API keys. The trailing
`chat-ui` deploy makes the dashboard at http://chat.localhost available;
it's idempotent if the travel quickstart already deployed it:

```bash
cd ../devops-tool                && ./setup.sh deploy && cd -
cd ../devops-observability-tool  && ./setup.sh deploy && cd -
cd ../prometheus-query-tool      && ./setup.sh deploy && cd -
cd ../system-utilities-tool      && ./setup.sh deploy && cd -
cd ../scheduler-tool             && ./setup.sh deploy && cd -
cd ../scheduled-executor         && ./setup.sh deploy && cd -   # consumer side of scheduler-tool
cd ../agentic-memory-tool        && ./setup.sh deploy && cd -
cd ../chat-ui                    && ./setup.sh deploy && cd -   # frontend
```

| Tool | What it does | Talks to |
|------|--------------|----------|
| [devops-tool](examples/devops-tool/) | Flexible `kubectl` access (only `delete` blocked) | In-cluster ServiceAccount |
| [devops-observability-tool](examples/devops-observability-tool/) | Search logs and traces | Loki + Jaeger |
| [prometheus-query-tool](examples/prometheus-query-tool/) | Run PromQL queries | Prometheus |
| [system-utilities-tool](examples/system-utilities-tool/) | Time, date, timezone math | Go stdlib |
| [scheduler-tool](examples/scheduler-tool/) | Cron-style scheduled execution (producer — creates schedules, promotes due tasks) | In-cluster |
| [scheduled-executor](examples/scheduled-executor/) | Consumer side of `scheduler-tool` — drains the queue and HTTP-dispatches due tasks to target agents (internal coordinator, not LLM-facing) | Redis + target agents |
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

# Health check — use /health, not the root.
# The agent root returns 404 by design; that's not a failed deployment.
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

	// Provider packages are opt-in and register themselves from init.
	// Import every provider this binary may select or auto-detect.
	_ "github.com/truvaagents/truva-g3/ai/providers/anthropic"
	_ "github.com/truvaagents/truva-g3/ai/providers/gemini"
	_ "github.com/truvaagents/truva-g3/ai/providers/openai"
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
OPENROUTER_API_KEY=...            # OpenRouter (priority 850)
GOOGLE_API_KEY=...                # Google Gemini; preferred over GEMINI_API_KEY
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

**Ingress hostnames intermittently time out on Podman (`curl` returns `000`) while pods are healthy:**

This is Podman's gvproxy host→VM port forwarder degrading, not a cluster
problem — see [Podman Troubleshooting — Ingress hostnames intermittently time
out (gvproxy)](docs/reference/PODMAN_TROUBLESHOOTING.md#ingress-hostnames-intermittently-time-out-gvproxy)
for the one-time machine refresh that restores it.

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

**Service discovery flickers — tools register and disappear, registry TTLs seem broken:**

The discovery layer uses Redis TTLs (default 30s) to track healthy services. If the system clock on your dev machine, your WSL distro, or the Redis pod drift apart by more than the TTL, healthy services look expired and re-register repeatedly. Diagnose:

```bash
date -u                                                     # host UTC
kubectl exec -n truvag3-examples deploy/redis -- date -u    # pod UTC
```

If they differ by minutes, the WSL clock is most likely the culprit. Fix:

```bash
sudo systemctl stop systemd-timesyncd          # don't let it snap back
sudo date -u -s '<current Windows UTC>'
kubectl rollout restart deployment/travel-chat-agent -n truvag3-examples
# (also restart the affected tools so they re-register against the corrected clock)
```

See [Windows + WSL2 Troubleshooting — Service discovery flickers](docs/reference/WINDOWS_TROUBLESHOOTING.md#service-discovery-flickers-clock-skew) for the full diagnosis and tool-restart list.

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

**`kubectl rollout restart deployment/ingress-nginx-controller -n ingress-nginx` hangs forever:**

ingress-nginx uses `hostPort: 80, 443` to expose the cluster on `*.localhost`. On a single-node kind cluster the new pod can't schedule because the old pod still owns those ports (`0/1 nodes are available: 1 node(s) didn't have free ports`). Bypass the rolling strategy:

```bash
kubectl delete pod -n ingress-nginx -l app.kubernetes.io/component=controller
kubectl rollout status deployment/ingress-nginx-controller -n ingress-nginx --timeout=120s
```

You'll have ~10–30s of ingress downtime while the new pod schedules.

---

## 8. Next Steps

### Recommended Learning Path

1. **Run examples** - Start with [travel-chat-agent](examples/travel-chat-agent/) to see everything working
2. **Explore patterns** - Study [agent-with-orchestration](examples/agent-with-orchestration/) for DAG workflows
3. **Add observability** - Try [agent-with-telemetry](examples/agent-with-telemetry/) for full monitoring
4. **Build resilience** - Learn from [agent-with-resilience](examples/agent-with-resilience/)

### Explore Advanced Features

- **[AI Module](ai/README.md)** - Multi-provider support with automatic failover
- **[AI Providers Setup Guide](docs/building/AI_PROVIDERS_SETUP_GUIDE.md)** - Provider aliases, model selection, failover, and deployment configuration
- **[Custom AI Providers and Enterprise Integration](docs/building/CUSTOM_AI_PROVIDER_GUIDE.md)** - Request-aware policy, Azure OpenAI, Google-hosted models, dynamic credentials, routing, and custom adapters
- **[AI Provider Change Playbook](docs/building/AI_PROVIDER_CHANGE_PLAYBOOK.md)** - Safe responses to provider model, parameter, authentication, and endpoint changes
- **[Orchestration Module](orchestration/README.md)** - DAG workflows and AI-generated plans
- **[Agent Skills Guide](docs/orchestration/AGENT_SKILLS_GUIDE.md)** - Authoring, binding, progressive disclosure, management, and operations
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
