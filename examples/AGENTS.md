# AGENTS.md — `examples/`

Applies to everything under `examples/`. Inherits the repo-root
[AGENTS.md](../AGENTS.md); a more specific `AGENTS.md` inside an individual
example directory overrides this one.

## Always deploy via `setup.sh`

Most subdirectories here are **self-contained, runnable examples**, each with its
own `setup.sh`, `k8-deployment.yaml`, and a `Dockerfile` (a few use
`Dockerfile.workspace`), plus a `.env.example` when the
example takes configuration — static UIs like `chat-ui` have none. A few
directories are **shared/support infrastructure, not directly runnable** and have
no top-level `setup.sh` — e.g. `k8-deployment/` (the shared deploy helper sourced
by every example), `mock-services/` (shared mock backends that paired examples
deploy as test dependencies — `grocery-store-api` for `agent-with-resilience` /
`grocery-tool`, `product-catalog-api` for `event-driven-agent` / `my-async-agent`;
see [mock-services/AGENTS.md](mock-services/AGENTS.md)), and `slack-gateway/`
(a guide only). To run a runnable example, `cd` into it and
use **its** `setup.sh` — never hand-run the image build
(`docker build`/`podman build`), `kind load`, or `kubectl apply` to deploy. The script orders the
build/load/deploy/rollout steps correctly and handles dependencies you'd
otherwise miss (Kind image loading, namespace, rollout waits).

Typical flow:

```bash
cd examples/<name>
cp .env.example .env          # set one AI provider key if it's an agent
./setup.sh full-deploy        # FIRST example only (creates cluster + shared infra)
# for every later example:
./setup.sh deploy             # cluster + infra already up
```

After editing files:
- changed only `.env`/config → `./setup.sh rollout`
- changed code/assets → `./setup.sh rebuild` (forces `--no-cache` rebuild;
  `rollout` will NOT pick up a new image)

**Verbs differ per example — don't assume.** The cleanup verb in particular
varies: `clean`, `cleanup`, `clean-all`, and `cleanup-all` all appear, and the
choice does **not** track agent-vs-tool (some agents use `clean`). Some examples
(e.g. [chat-ui/](chat-ui/)) have a different verb set entirely with no
`full-deploy` — and for chat-ui, running `./setup.sh` **with no args triggers a
real deployment**, not a help screen. So always run **`./setup.sh help`** (every
example supports it) to see that example's real commands, or read its nested
`AGENTS.md`. Never run `./setup.sh` with no args just to inspect it.

## Agents need their tools

An agent has no built-in capabilities — it discovers tools at runtime via Redis
and asks the LLM to plan calls. After deploying an agent, deploy the tools it
needs (`./setup.sh deploy` in each tool dir). See
[../GETTING_STARTED.md](../GETTING_STARTED.md) §2 for the per-quickstart tool lists.

## Portability contract

An example must run without depending on sibling example directories — it may
rely only on the shared `examples/k8-deployment/` helper (sourced by the setup
scripts). Don't introduce cross-example imports or links to files that wouldn't
exist in a copied single-example bundle. See [README.md](README.md).

## Building a new tool or agent

The scaffolds `my-tool/`, `my-streaming-agent/`, and `my-async-agent/` each ship
a `PROMPT.md` that drives a coding agent through a 12-step build. Copy the
scaffold, then follow `PROMPT.md` one step at a time — review after each step,
don't batch all 12. See [../GETTING_STARTED.md](../GETTING_STARTED.md) §4.
