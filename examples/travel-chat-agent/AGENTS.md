# AGENTS.md — `examples/travel-chat-agent/`

A streaming (SSE) chat agent that orchestrates travel tools via AI planning.
Inherits [../AGENTS.md](../AGENTS.md) and the repo-root
[AGENTS.md](../../AGENTS.md); this file takes precedence here.

## Deploying

This is the recommended **first** example to run from a cold start. Its
`setup.sh full-deploy` creates the cluster, deploys shared infra, and builds +
deploys **both this agent and the [chat-ui](../chat-ui/)** in one step.

```bash
cp .env.example .env           # then set ONE provider key (quick-start: OPENAI_API_KEY / ANTHROPIC_API_KEY / OPENROUTER_API_KEY / GROQ_API_KEY / GEMINI_API_KEY)
./setup.sh full-deploy         # cold start (~5–15 min first run)
```

Those four are the quick-start keys; this agent's `setup.sh` also accepts
DeepSeek, xAI, Mistral, Qwen, Together AI, and other OpenAI-compatible providers
— see `.env.example` and [../../GETTING_STARTED.md](../../GETTING_STARTED.md) §6.

Verbs: `full-deploy`, `deploy`, `rollout`, `rebuild`, `skills-check`, and
`skills-sync`, plus `cleanup` / `cleanup-all` to tear down (this script happens
to use `cleanup`; the verb isn't consistent across examples). Run
`./setup.sh help` for the full list.

## After editing

For this agent, `deploy`, `rollout`, and `rebuild` all finish with a
`kubectl rollout restart`, so pods reliably pick up changes. Pick by what changed:

- Changed only `.env` (e.g. swapped the API key/model) → **`./setup.sh rollout`**.
  Regenerates the Secret/ConfigMap from `.env`, re-applies the manifest, and
  restarts pods — **without** rebuilding the image. The right, lightest verb for
  config-only changes.
- Changed Go code → **`./setup.sh rebuild`** — forces a `--no-cache` image build,
  reloads it into Kind, and restarts. (`deploy` also rebuilds + restarts but uses
  Docker's cache; prefer `rebuild` when dependencies changed or a cached build
  looks stale.)

## This agent has no built-in knowledge — deploy its tools

It plans and calls discovered tools to answer. Without tools deployed it
responds but can't fetch live data. After `full-deploy`, deploy the tools
(each with `./setup.sh deploy`): `weather-tool-v2`, `geocoding-tool`,
`currency-tool`, `country-info-tool`, `system-utilities-tool`,
`travel-advisory-tool`, `scheduler-tool` (no-key); plus keyed tools (`news-tool`, `flight-tool`,
`hotel-tool`, `places-tool`) for richer answers. Full list +
required keys: [../../GETTING_STARTED.md](../../GETTING_STARTED.md) §2.

Verify discovery: `curl http://travel-chat-agent.localhost/discover`.

## Gotchas

- Health check is `/health` — the root `/` returns 404 **by design**; that is
  not a failed deploy.
- This agent is browser-facing, so it uses `core.WithCORSDefaults()`. If you see
  "Failed to connect to backend" in the UI, a CORS preflight is being rejected —
  see [../../GETTING_STARTED.md](../../GETTING_STARTED.md) §7.
- API: `POST /chat/session` to get a `session_id`, then `POST /chat/stream` (SSE).
