# AGENTS.md

Guidance for AI coding assistants (Codex, Cursor, Copilot, Gemini CLI, Claude Code,
Windsurf, Aider, Zed, and others) working in the **TruvaG3** repository.

> This is the cross-tool source of truth. `CLAUDE.md` imports this file via
> `@AGENTS.md`. Nested `AGENTS.md` files exist in subdirectories (e.g.
> `examples/`, `examples/chat-ui/`) — **the file closest to what you're editing
> wins**, so always read the nearest one before acting.

## What this project is

TruvaG3 is a Kubernetes-native Go framework for building AI agents and tools that
discover and coordinate with each other via Redis. The repo contains the framework
(`core/`, `ai/`, `orchestration/`, `telemetry/`, `resilience/`) and ~50 runnable
reference examples under `examples/`.

The single best human-facing onboarding doc is [GETTING_STARTED.md](GETTING_STARTED.md).
When you need setup/run detail beyond this file, read that — don't improvise.

## The golden rule: run examples via their `setup.sh`

**Every runnable example ships a `setup.sh`. Use it. Do NOT hand-run the image
build (`docker build`/`podman build`), `kind load`, or `kubectl apply` to deploy
an example.** (A few directories under
`examples/` are shared infrastructure, not runnable examples — see
[examples/AGENTS.md](examples/AGENTS.md).) The script wires up
Kind image-loading, namespace creation, manifest application, and rollout waits
in the correct order — steps that are easy to miss and silently break discovery.

Standard verbs (most agent/tool examples):

| Command | Use when |
|---|---|
| `./setup.sh full-deploy` | **Cold start** — first example you run. Creates the Kind cluster, deploys shared infra (Redis, OTEL Collector, Loki, Prometheus, Jaeger, Grafana, Swagger UI, `registry-viewer-app`, ingress-nginx), then builds + deploys this example. |
| `./setup.sh deploy` | Cluster + infra already up — build + deploy just this example. Use for **every subsequent** example. |
| `./setup.sh rollout` | You edited only `.env`/config (no code change). Regenerates Secret/ConfigMap and restarts pods. |
| `./setup.sh rebuild` | You changed code/assets — forces a `--no-cache` image rebuild and redeploy. (A `--build` flag on `rollout` is silently ignored — use `rebuild`.) |
| `./setup.sh logs` / `status` | Follow logs / check deployment. |
| `./setup.sh <cleanup-verb>` | Remove this deployment. **The verb varies** — it's one of `clean` / `cleanup` / `clean-all` / `cleanup-all`, and it does **not** track agent-vs-tool. Confirm via `./setup.sh help`. |

**Verbs are not uniform across examples.** Some examples (notably
[examples/chat-ui/](examples/chat-ui/)) have a different verb set, and for some,
**no args triggers a real deployment** rather than a help screen. To inspect an
example's actual commands, run **`./setup.sh help`** (every example supports it)
or read its nested `AGENTS.md` — never run `./setup.sh` with no args just to see
what it does.

An **agent** deploy that calls an LLM needs an AI provider key in `.env`
(`cp .env.example .env`). The common quick-start keys are `OPENAI_API_KEY` /
`ANTHROPIC_API_KEY` / `GROQ_API_KEY` / `GEMINI_API_KEY` (set any one), but the
setup scripts accept more providers (DeepSeek, xAI, Mistral, Qwen, Together AI,
and other OpenAI-compatible endpoints) — see [GETTING_STARTED.md](GETTING_STARTED.md)
§6 and each example's `.env.example`. **Tools and static UIs don't need an AI
provider key** (chat-ui has no `.env` at all; a tool may instead need its own
service API key, e.g. `GNEWS_API_KEY`). Agents discover tools at runtime via
Redis, so an agent needs its tools deployed too — see
[GETTING_STARTED.md](GETTING_STARTED.md) §2.

## Working on the framework (Go) — pre-commit gates

For any change that touches Go, run the full gate set and require all to pass
before considering the change done: `go vet`, `go build ./...`, `go test ./...`,
`goimports`, `golangci-lint run`, `gosec`, `govulncheck`. Partial passes are not
acceptable. See [CONTRIBUTING.md](CONTRIBUTING.md).

This is a Go workspace (`go.work`); the framework requires **Go 1.26+**.

## Conventions

- **Container runtime: Docker or Podman.** `setup.sh` auto-detects the engine
  (prefers Docker when its daemon is up, otherwise Podman) and points `kind` at the
  same one. To pin one explicitly, set `TRUVAG3_CONTAINER_RUNTIME=docker|podman` —
  it overrides the auto-detection (honored even if that engine isn't running). On Podman,
  macOS needs a *rootful* machine and `alias docker=podman` won't reach the scripts —
  see [PODMAN_TROUBLESHOOTING.md](docs/reference/PODMAN_TROUBLESHOOTING.md).
- Browser-facing components (chat UIs, dashboards, SSE) need `core.WithCORSDefaults()`,
  not `core.WithCORS(...)` — the strict default rejects browser preflights. See
  [GETTING_STARTED.md](GETTING_STARTED.md) §5.
- Examples are designed to be **portable** — an example must run without depending on
  sibling example directories (it may rely only on the shared `examples/k8-deployment/`
  helper). Don't add cross-example imports. See [examples/README.md](examples/README.md).
- Services are exposed via `*.localhost` ingress (e.g. `chat.localhost`,
  `travel-chat-agent.localhost`). No port-forwarding needed on macOS.

## Docs changes

Treat `*.md` edits as needing explicit human sign-off before commit — propose the
change and stop at "ready to commit" rather than committing docs automatically.
