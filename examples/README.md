# TruvaG3 Examples

This directory contains standalone examples for the TruvaG3 framework. The
examples are intended to be useful in two modes:

1. As a catalog inside this repository, so developers can discover working
   patterns and compare implementations.
2. As portable starters, so a single example can be copied into its own
   repository together with `k8-deployment/`.

Individual example READMEs are intentionally self-contained. Do not assume that
a copied example still has access to sibling examples or this top-level catalog.

## Table of Contents

- [Portable Example Contract](#portable-example-contract)
- [Quick Start](#quick-start)
- [Skills in Examples](#skills-in-examples)
- [Example Catalog](#example-catalog)
- [Infrastructure and UIs](#infrastructure-and-uis)
- [Learning Paths](#learning-paths)
- [Ports](#ports)
- [API Keys](#api-keys)
- [Development Notes](#development-notes)

## Portable Example Contract

Each example should be able to move into its own repository when copied with the
shared deployment helper folder:

```text
new-repo/
  k8-deployment/
  your-example/
    README.md
    setup.sh
    .env.example
    Dockerfile
    k8-deployment.yaml
    go.mod
    *.go
```

The sibling layout matters because the setup scripts source shared helpers from
`../k8-deployment/setup-env-lib.sh`.

For an example to be considered portable:

- Its README explains how to run it without relying on other example
  directories.
- Its setup script can create or reuse the Kind cluster and shared
  infrastructure.
- Its `.env.example` documents required and optional configuration.
- Its Docker and Kubernetes files build from the example itself or the portable
  bundle.
- Links in the example README point only to files that would exist in the
  copied bundle, or clearly identify optional repository-only references.

## Quick Start

Use `travel-chat-agent` when you want a full end-to-end demo with an agent, UI,
tool discovery, AI integration, and observability:

```bash
cd examples/travel-chat-agent
cp .env.example .env
# Edit .env and set at least one AI provider key if required by the example.
./setup.sh full-deploy
```

Most examples expose a similar setup interface:

```bash
./setup.sh full-deploy   # cluster + infrastructure + example deployment
./setup.sh deploy        # rebuild/redeploy this example
./setup.sh forward       # optional local port-forward fallback
./setup.sh logs          # view example logs, where supported
./setup.sh clean         # remove this example's resources
./setup.sh clean-all     # delete the local Kind cluster, where supported
```

The first `full-deploy` creates the Kind cluster and shared infrastructure. Later
examples can reuse the same cluster.

## Skills in Examples

Some agents use skills: reusable instructions that help the orchestrator apply
domain-specific procedures without hardcoding discovered tool or capability
names. Their setup is automatic. Developers running an example do not need to
create skill records manually.

Git stores the desired packages with the agent:

```text
examples/<agent>/
  skills/
    packages/
      <namespace>/
        <skill-name>.json
```

The directory path supplies the skill identity. For example:

```text
skills/packages/travel/weather-assessment.json
                └─┬──┘ └────────┬────────┘
              namespace       skill name
```

During a cold start or normal deployment, setup follows this order:

```text
create or reuse cluster
          │
          ▼
deploy shared infrastructure and the Skills API host
          │
          ▼
validate and synchronize every agent-owned package
          │
          ▼
create or restart the agent workload
          │
          ▼
agent binds and reads the published skills during execution
```

`full-deploy`, `deploy`, `rebuild`, and `rollout` perform synchronization before
the skill-enabled agent starts or restarts. Unchanged content is skipped, so
repeated setup commands do not create unnecessary versions. Changed content is
published as a new version and verified by reading it back. Automatic
synchronization is best-effort: an invalid package or an unavailable management
API produces a clear warning, but the agent deployment continues. The agent can
use an existing published revision if one is available. A request that needs a
missing required skill can still fail until the published state is repaired.
Finder-created `.DS_Store` files are ignored; other unexpected files still
produce a warning during automatic setup and fail the strict maintenance
commands.

This means deleting and recreating the local Kind cluster does not lose the
example definition. The runtime data store is derived state; the next
`full-deploy` recreates the published packages from Git.

Skill-enabled examples also provide two operational commands:

```bash
./setup.sh skills-check   # read-only: compare published packages with Git
./setup.sh skills-sync    # reconcile packages without rebuilding the agent
```

Use `TRUVAG3_SKILLS_API_URL` to point these commands at a different deployment
of the provider-neutral Skills management API. The local examples default to
`http://registry.localhost/api/v1/skills`. Registry Viewer provides the local
management screen and lets developers inspect published versions, while the
orchestration module owns validation and storage contracts.

The explicit `skills-sync` and `skills-check` commands are strict: drift,
invalid content, or an unavailable API returns a non-zero status for operators
and CI. On a headless or remote setup host that cannot route to the management
API, set `TRUVAG3_SKIP_SKILLS_SYNC=true` to skip only the automatic deployment
attempt. The explicit maintenance commands ignore this setup-only switch.
Automatic setup briefly retries an ingress `404` that can occur while a new
route is converging. Strict commands fail fast on `404`, and the read-only
`skills-check` uses a shorter retry budget for other temporary failures.

The management API and the agent must also address the same logical datastore.
For the included Redis implementation, keep `TRUVAG3_SKILLS_REDIS_DB` consistent
between Registry Viewer and every skill-enabled agent. Changing the value in an
agent `.env` and running only `rollout` updates the agent reader, not Registry
Viewer's writer. Run an infrastructure-capable example's `./setup.sh infra`
with the new value before `skills-sync` and the agent rollout.

When a coding agent adds a new skill, it must add the package under the standard
directory and add an explicit runtime binding for the same namespace and name.
No per-package setup command is needed: the shared setup helper discovers the
new file automatically. The complete coding-assistant rules are in
[`examples/AGENTS.md`](AGENTS.md#agent-owned-skills).

## Example Catalog

### Core Patterns

| Example | Type | What It Demonstrates | README |
|---|---|---|---|
| [tool-example](tool-example/) | Tool | Minimal passive tool pattern, capability registration, schema endpoint | Yes |
| [agent-example](agent-example/) | Agent | Active coordination, service discovery, AI-assisted orchestration | Yes |
| [my-tool](my-tool/) | Tool | Small custom tool starter | Yes |

### Agent Patterns

| Example | Type | What It Demonstrates | README |
|---|---|---|---|
| [agent-with-async](agent-with-async/) | Agent | Async task submission, polling, cancellation | Yes |
| [my-async-agent](my-async-agent/) | Agent | Compact async agent starter | Yes |
| [my-streaming-agent](my-streaming-agent/) | Agent | Streaming response agent pattern | Yes |
| [agent-with-human-approval](agent-with-human-approval/) | Agent | Human-in-the-loop checkpoints and resume flow | Yes |
| [agent-with-orchestration](agent-with-orchestration/) | Agent | Multi-step orchestration and workflow execution | Yes |
| [agent-with-resilience](agent-with-resilience/) | Agent | Circuit breakers, retry behavior, resilience testing | Yes |
| [agent-with-telemetry](agent-with-telemetry/) | Agent | Metrics, logs, traces, telemetry setup | Yes |
| [event-driven-agent](event-driven-agent/) | Agent | Event triggers, webhooks, queue-style processing | Yes |
| [scheduled-executor](scheduled-executor/) | Agent | Scheduled workflow execution | Yes |

### Full Application Agents

| Example | Type | What It Demonstrates | README |
|---|---|---|---|
| [travel-chat-agent](travel-chat-agent/) | Agent/App | Travel assistant with chat UI integration and multiple travel tools | Yes |
| [devops-chat-agent](devops-chat-agent/) | Agent/App | DevOps assistant that uses operational tools | Yes |
| [qa-agent](qa-agent/) | Agent/App | Question-answering flow with orchestration and memory | Yes |
| [github-pr-review-agent](github-pr-review-agent/) | Agent/App | **WIP — untested end-to-end.** GitHub PR review workflow | Yes |

### Travel and Location Tools

| Example | Type | What It Demonstrates | README |
|---|---|---|---|
| [weather-tool-v2](weather-tool-v2/) | Tool | Weather forecast and current conditions | Yes |
| [geocoding-tool](geocoding-tool/) | Tool | Forward and reverse geocoding | Yes |
| [places-tool](places-tool/) | Tool | Place search and location enrichment | Yes |
| [flight-tool](flight-tool/) | Tool | Flight search mock/API pattern | Yes |
| [hotel-tool](hotel-tool/) | Tool | Hotel search mock/API pattern | Yes |
| [travel-advisory-tool](travel-advisory-tool/) | Tool | Country travel advisories | Yes |

### Finance and Public Data Tools

| Example | Type | What It Demonstrates | README |
|---|---|---|---|
| [currency-tool](currency-tool/) | Tool | Currency conversion | Yes |
| [currency-global-tool](currency-global-tool/) | Tool | Broad global currency conversion | Yes |
| [stock-market-tool](stock-market-tool/) | Tool | Stock quote and market data access | Yes |
| [economic-data-tool](economic-data-tool/) | Tool | Economic data access | Pending |
| [fiscal-data-tool](fiscal-data-tool/) | Tool | Fiscal data access | Pending |
| [demographics-tool](demographics-tool/) | Tool | Demographic data access | Pending |
| [country-info-tool](country-info-tool/) | Tool | Country profile data from a bundled offline dataset (`go:embed`) | Yes |
| [world-health-tool](world-health-tool/) | Tool | World health indicators | Yes |

### Research, Medical, and Knowledge Tools

| Example | Type | What It Demonstrates | README |
|---|---|---|---|
| [arxiv-tool](arxiv-tool/) | Tool | arXiv paper search | Yes |
| [semantic-scholar-tool](semantic-scholar-tool/) | Tool | Academic paper and citation lookup | Yes |
| [pubmed-tool](pubmed-tool/) | Tool | PubMed literature search | Yes |
| [clinical-trials-tool](clinical-trials-tool/) | Tool | ClinicalTrials.gov search | Yes |
| [openfda-tool](openfda-tool/) | Tool | FDA drug and device safety APIs | Yes |
| [web-search-tool](web-search-tool/) | Tool | Web search capability wrapper | Yes |
| [news-tool](news-tool/) | Tool | News search capability wrapper | Yes |

### DevOps and System Tools

| Example | Type | What It Demonstrates | README |
|---|---|---|---|
| [devops-tool](devops-tool/) | Tool | Kubernetes and operational actions | Yes |
| [devops-observability-tool](devops-observability-tool/) | Tool | Observability investigation helpers | Yes |
| [prometheus-query-tool](prometheus-query-tool/) | Tool | Prometheus query capabilities | Yes |
| [system-utilities-tool](system-utilities-tool/) | Tool | Time, command, ID, and browser utility capabilities | Yes |
| [playwright-tool](playwright-tool/) | Tool | Browser automation capability wrapper | Yes |
| [openclaw-tool](openclaw-tool/) | Tool | Contained autonomous OpenClaw agent — `run_task` plus 11 typed data/code/security capabilities | Yes |

### Collaboration and Productivity Tools

| Example | Type | What It Demonstrates | README |
|---|---|---|---|
| [github-tool](github-tool/) | Tool | **WIP — untested end-to-end.** GitHub REST API wrapper for PR review flows | Yes |
| [jira-tool](jira-tool/) | Tool | Jira issue lookup and search | Yes |
| [slack-tool](slack-tool/) | Tool | Slack channel, message search, and send actions | Yes |
| [slack-gateway](slack-gateway/) | Gateway | Slack gateway integration | Pending |
| [confluence-tool](confluence-tool/) | Tool | Confluence content access | Pending |
| [scheduler-tool](scheduler-tool/) | Tool | Schedule creation and management | Yes |
| [agentic-memory-tool](agentic-memory-tool/) | Tool | Memory-oriented tool pattern | Yes |

### Support and UI Examples

| Example | Type | What It Demonstrates | README |
|---|---|---|---|
| [chat-ui](chat-ui/) | UI | Browser chat interface for agents | Yes |
| [registry-viewer-app](registry-viewer-app/) | UI | Registry, execution, and debugging viewer | Yes |
| [mock-services](mock-services/) | Support | Placeholder/support area for mock APIs used by examples | Yes |
| [k8-deployment](k8-deployment/) | Infrastructure | Shared Kind, Redis, observability, and setup helpers | Yes |

### Framework Validation Examples

| Example | Type | What It Demonstrates | README |
|---|---|---|---|
| [orchestration-backend-portability](orchestration-backend-portability/) | Integration | Role-specific PostgreSQL, NATS, and Redis composition with self-contained Kind verification | Yes |

## Infrastructure and UIs

The `k8-deployment/` folder contains shared local Kubernetes infrastructure used
by the examples:

- Kind cluster setup helpers
- Redis service discovery support
- Prometheus, Grafana, Jaeger, and OpenTelemetry components
- NGINX Ingress support for `*.localhost` routes
- Shared shell functions used by example `setup.sh` scripts

Common local URLs after a full deployment:

| Service | URL |
|---|---|
| Chat UI | `http://chat.localhost` |
| Registry Viewer | `http://registry.localhost` |
| Grafana | `http://grafana.localhost` |
| Prometheus | `http://prometheus.localhost` |
| Jaeger | `http://jaeger.localhost` |

Agents and UIs generally expose Ingress routes. Tools are usually internal
ClusterIP services and are accessed directly only through a port-forward or a
tool-specific test command.

## Learning Paths

### First Principles

1. [tool-example](tool-example/) - learn how a passive tool registers
   capabilities.
2. [agent-example](agent-example/) - learn how an agent discovers and calls
   tools.
3. [travel-chat-agent](travel-chat-agent/) - see a complete user-facing agent
   application.

### Agent Capabilities

1. [agent-with-async](agent-with-async/) - async task lifecycle.
2. [agent-with-orchestration](agent-with-orchestration/) - multi-step workflows.
3. [agent-with-human-approval](agent-with-human-approval/) - approval gates.
4. [agent-with-resilience](agent-with-resilience/) - circuit breakers and retry
   behavior.
5. [agent-with-telemetry](agent-with-telemetry/) - production observability.

### Domain Tooling

1. Travel: [weather-tool-v2](weather-tool-v2/),
   [geocoding-tool](geocoding-tool/), [places-tool](places-tool/),
   [flight-tool](flight-tool/), [hotel-tool](hotel-tool/),
   [travel-advisory-tool](travel-advisory-tool/).
2. Research and health: [arxiv-tool](arxiv-tool/),
   [semantic-scholar-tool](semantic-scholar-tool/), [pubmed-tool](pubmed-tool/),
   [clinical-trials-tool](clinical-trials-tool/), [openfda-tool](openfda-tool/),
   [world-health-tool](world-health-tool/).
3. Operations: [devops-tool](devops-tool/),
   [devops-observability-tool](devops-observability-tool/),
   [prometheus-query-tool](prometheus-query-tool/),
   [system-utilities-tool](system-utilities-tool/).

## Ports

The current example port allocation is:

| Example | Port | Type |
|---|---:|---|
| country-info-tool | 8333 | tool |
| currency-tool | 8334 | tool |
| geocoding-tool | 8335 | tool |
| grocery-tool | 8336 | tool |
| news-tool | 8337 | tool |
| stock-market-tool | 8338 | tool |
| weather-tool-v2 | 8339 | tool |
| tool-example | 8340 | tool |
| web-search-tool | 8341 | tool |
| flight-tool | 8342 | tool |
| hotel-tool | 8343 | tool |
| places-tool | 8344 | tool |
| travel-advisory-tool | 8345 | tool |
| currency-global-tool | 8346 | tool |
| devops-tool | 8347 | tool |
| system-utilities-tool | 8348 | tool |
| playwright-tool | 8349 | tool |
| agent-example | 8350 | agent |
| agent-with-async | 8351 | agent |
| agent-with-human-approval | 8352 | agent |
| agent-with-orchestration | 8353 | agent |
| agent-with-resilience | 8354 | agent |
| agent-with-telemetry | 8355 | agent |
| travel-chat-agent | 8356 | agent |
| devops-chat-agent | 8357 | agent |
| qa-agent | 8358 | agent |
| chat-ui | 8360 | ui |
| registry-viewer-app | 8361 | ui |
| economic-data-tool | 8363 | tool |
| fiscal-data-tool | 8364 | tool |
| demographics-tool | 8365 | tool |
| jira-tool | 8366 | tool |
| clinical-trials-tool | 8367 | tool |
| world-health-tool | 8368 | tool |
| arxiv-tool | 8369 | tool |
| semantic-scholar-tool | 8370 | tool |
| prometheus-query-tool | 8371 | tool |
| event-driven-agent | 8372 | agent |
| slack-tool | 8373 | tool |
| openfda-tool | 8374 | tool |
| pubmed-tool | 8375 | tool |
| confluence-tool | 8376 | tool |
| agentic-memory-tool | 8377 | tool |
| devops-observability-tool | 8378 | tool |
| scheduler-tool | 8379 | tool |
| scheduled-executor | 8380 | agent |
| github-tool | 8381 | tool |
| github-pr-review-agent | 8382 | agent |
| my-tool | 8390 | tool |
| my-streaming-agent | 8391 | agent |
| my-async-agent | 8392 | agent |
| openclaw-tool | 8393 | tool |

Infrastructure defaults:

| Service | Port |
|---|---:|
| Grafana | 3000 |
| Prometheus | 9090 |
| Jaeger | 16686 |

## API Keys

Many examples run against public APIs or mock data. AI-enabled examples generally
need at least one AI provider key. Copy the example environment file before
deploying:

```bash
cd examples/<example-name>
cp .env.example .env
```

Common AI provider variables include:

```bash
OPENAI_API_KEY=...
GROQ_API_KEY=...
ANTHROPIC_API_KEY=...
DEEPSEEK_API_KEY=...
GOOGLE_AI_API_KEY=...
```

Tool-specific keys, such as GitHub, Slack, Jira, Confluence, weather, or search
provider credentials, are documented in the corresponding example README and
`.env.example`.

## Development Notes

When adding or updating examples:

- Keep the example portable with `k8-deployment/`.
- Keep the individual README self-contained.
- Avoid making an example README depend on sibling example directories.
- Keep ports aligned across `README.md`, `setup.sh`, and `k8-deployment.yaml`.
- Keep framework module versions aligned with the example `go.mod`.
- Prefer environment variables over hardcoded configuration.
- Use the shared setup library from `k8-deployment/setup-env-lib.sh` instead of
  duplicating cluster and infrastructure logic.

Known documentation follow-up work:

- Add READMEs for examples currently marked `Pending` in the catalog.
- Refresh individual example READMEs for stale ports, versions, and broken
  local links.
- Review WIP examples and decide when to promote them to fully validated
  examples.
