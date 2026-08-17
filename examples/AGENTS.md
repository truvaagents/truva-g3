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

## Agent-owned skills

Git is the source of truth for skills used by an example. The runtime skills
store is derived state: a cold start must be able to recreate it without a
developer using the management API by hand.

When adding or changing skills in an example:

1. Store each complete, publishable package at
   `skills/packages/<namespace>/<skill-name>.json`. The two path components are
   the namespace and name sent to the Skills API, so use valid lowercase slugs
   and do not place drafts, fixtures, symlinks, or unrelated files under
   `skills/packages/`. The shared helper ignores Finder-created `.DS_Store`
   files only; every other unexpected file remains a validation failure.
2. Bind the same namespace and name explicitly in the agent's Go configuration
   or supported environment configuration. Adding a package without a runtime
   binding is incomplete; package publication does not implicitly grant an
   agent access to that skill.
3. Do not add one shell command per package. A skill-enabled setup script must
   call `truvag3_prepare_agent_skills "$SCRIPT_DIR/skills/packages"` from its
   deployment paths and expose strict wrappers around
   `truvag3_sync_agent_skills` and `truvag3_check_agent_skills` from
   `k8-deployment/setup-env-lib.sh`.
4. Attempt skill synchronization after shared infrastructure is ready and
   before the agent workload is created or restarted. Wire the best-effort
   `prepare` helper into every state-changing deployment mode the example
   supports, including `full-deploy`, `deploy`, `rebuild`, `rollout`, and
   split/embedded variants where present. A validation, routing, or management
   API failure must produce a visible warning but must not block agent
   deployment.
5. Expose `skills-sync` and `skills-check` through the skill-enabled agent's
   `setup.sh`. Normal setup remains automatic; these commands are for explicit
   repair and read-only drift checks.
6. Never write raw backend keys and never publish packages from an agent replica
   during process startup. Setup automation writes packages through the
   provider-neutral Skills HTTP API; agent processes only bind and read them.
7. Do not automatically delete stored skills when a file disappears from Git.
   Deletion is an explicit administrative action. Removing the runtime binding
   prevents the agent from using an old package.
8. After editing a package, run the setup helper tests, use
   `./setup.sh skills-sync` to update the runtime copy, and finish with
   `./setup.sh skills-check` to confirm that Git and the published package
   match.

`skills-sync` and `skills-check` are deliberately strict and return a non-zero
status on drift or failure, so operators and CI can rely on them. Automatic
deployment synchronization is deliberately best-effort. Set the setup-only
`TRUVAG3_SKIP_SKILLS_SYNC=true` when the management API is intentionally
unreachable from the setup host; explicit `skills-sync` and `skills-check`
commands ignore this switch and remain strict.

The management API writer and every agent runtime reader must use the same
logical skills datastore. With the included Redis implementation, that includes
the same Redis deployment and `TRUVAG3_SKILLS_REDIS_DB`. If this setting changes
after shared infrastructure is running, redeploy the infrastructure management
host before synchronizing or rolling out agents; an agent rollout cannot update
another workload's ConfigMap.

If an example has no `skills/packages/` directory, the shared directory helper
returns successfully without contacting the Skills API. This keeps the helper
safe for reusable agent scaffolds while making the presence of that directory
the opt-in signal for agent-owned skill setup.

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
