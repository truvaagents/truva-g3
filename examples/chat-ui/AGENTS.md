# AGENTS.md — `examples/chat-ui/`

The web frontend for the TruvaG3 chat agents. Inherits
[../AGENTS.md](../AGENTS.md) and the repo-root
[AGENTS.md](../../AGENTS.md); this file takes precedence here.

## Stack: vanilla JS, no framework, no build step

This is **plain HTML + CSS + JavaScript** — deliberately **no framework and no
build system**. Each page is a self-contained file (HTML, CSS, and JS inlined).
Do not introduce React/Vue/Svelte, npm, a bundler, or a `package.json` here —
that would defeat the design goal of a zero-dependency, directly-servable UI.

The only external libraries are loaded from CDN at runtime:
- **marked.js** — Markdown rendering of agent responses
- **KaTeX** — math rendering

Communication with agents is via **Server-Sent Events (SSE)** (`POST /chat/stream`).
Session state lives in browser `localStorage`.

Pages:
- `index.html` — Travel chat (talks to `travel-chat-agent`)
- `devops.html` — DevOps chat (talks to `devops-chat-agent`)
- `hitl.html` — Human-in-the-loop approvals (talks to `agent-with-human-approval`)
- `dashboard.html` / `welcome.html` — launcher / onboarding

## Deploying — verbs differ from the agent/tool examples

⚠️ **chat-ui's `setup.sh` has a DIFFERENT verb set.** There is **no
`full-deploy`** and cleanup is **`clean`**, not `cleanup`. The script also
**requires an existing Kind cluster** — run an agent's `full-deploy`
(e.g. `travel-chat-agent`) first to create the cluster and shared infra.

| Command | Effect |
|---|---|
| `./setup.sh` (no args) | Full deploy into the existing cluster: build → load → deploy |
| `./setup.sh deploy` | Apply manifest to the existing cluster |
| `./setup.sh rebuild` | `--no-cache` rebuild + reload + redeploy + rollout restart |
| `./setup.sh build` | Build the Docker image only |
| `./setup.sh logs` / `status` | Follow logs / check status |
| `./setup.sh forward` / `stop-forward` | Port-forward `localhost:8360` ↔ service :80 |
| `./setup.sh clean` | Remove the deployment (**not** `cleanup`) |

**After editing any `.html`/CSS/JS, run `./setup.sh rebuild`.** The assets are
baked into the image at build time (nginx serves them), and `rebuild` is the
only verb that forces a `--no-cache` build **and** a `kubectl rollout restart`.
A bare `deploy` only re-applies the manifest (no image rebuild, no restart), so
your edits won't appear. (chat-ui has no `rollout` verb — that's agent/tool-only.)

Note: chat-ui is normally built and deployed **automatically** as part of
`travel-chat-agent`'s `full-deploy`, so you often don't deploy it standalone —
use the standalone verbs above only when iterating on the UI itself.

Served at `http://chat.localhost` (a launcher with cards), with the travel chat
at `/index.html` and devops chat at `/devops.html`.
