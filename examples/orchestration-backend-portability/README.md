# Orchestration Backend Portability

This example shows how to run TruvaG3 orchestration across multiple backend
technologies without leaking provider-specific code into agents, workers,
schedulers, or executors.

It is a self-contained reference implementation: one setup command creates a
separate Kind cluster, installs PostgreSQL, NATS, and Redis, deploys every
application component, and verifies the result end to end. You do not need an
AI-provider key or any other TruvaG3 example.

> The PostgreSQL and NATS adapters in this folder are reference implementations,
> not production-supported framework providers. See
> [Production boundaries](#production-boundaries) before adopting them directly.

## Table of contents

- [Choose your path](#choose-your-path)
- [Quick start](#quick-start)
- [What backend portability means](#what-backend-portability-means)
- [Architecture at a glance](#architecture-at-a-glance)
- [Follow the two workflows](#follow-the-two-workflows)
- [Explore the running example](#explore-the-running-example)
- [Code tour](#code-tour)
- [How composition works](#how-composition-works)
- [Replace a provider](#replace-a-provider)
- [Setup commands](#setup-commands)
- [Cluster isolation and configuration](#cluster-isolation-and-configuration)
- [Troubleshooting](#troubleshooting)
- [Local development](#local-development)
- [Conformance scope](#conformance-scope)
- [What this example proves](#what-this-example-proves)
- [Production boundaries](#production-boundaries)

## Choose your path

You do not need to read this document from beginning to end.

| Your goal | Start here |
|---|---|
| Run the example and see it work | [Quick start](#quick-start) |
| Understand why PostgreSQL, NATS, and Redis are all present | [What backend portability means](#what-backend-portability-means) |
| Follow a request through the system | [Follow the two workflows](#follow-the-two-workflows) |
| Learn how to apply the pattern to an agent | [Code tour](#code-tour) and [How composition works](#how-composition-works) |
| Implement another backend | [Replace a provider](#replace-a-provider) |
| Diagnose a failed deployment | [Troubleshooting](#troubleshooting) |

## Quick start

### 1. Check the prerequisites

You need the following command-line tools:

| Tool | Why it is needed | Quick check |
|---|---|---|
| Go 1.27+ | Compiles the conformance tests | `go version` |
| Docker or Podman | Runs the Kind nodes and builds images | `docker info` or `podman info` |
| Kind | Creates the isolated Kubernetes cluster | `kind version` |
| kubectl | Deploys and inspects resources | `kubectl version --client` |
| curl | Exercises the HTTP flows | `curl --version` |
| jq | Verifies JSON responses and NATS state | `jq --version` |
| OpenSSL | Generates the local PostgreSQL password | `openssl version` |

If this is your first local TruvaG3 deployment, the repository's
[Getting Started guide](../../GETTING_STARTED.md) provides installation and
container-runtime background.

The setup helper automatically chooses a running Docker or Podman engine. To
select one explicitly, set `TRUVAG3_CONTAINER_RUNTIME=docker` or
`TRUVAG3_CONTAINER_RUNTIME=podman`.

### 2. Deploy the complete reference

From the repository root:

```bash
cd examples/orchestration-backend-portability
./setup.sh full-deploy
```

The command performs the full sequence for you:

1. Creates a dedicated Kind cluster named
   `truvag3-portability-$(whoami)`.
2. Installs the shared Redis and ingress resources in that cluster.
3. Installs PostgreSQL and NATS with JetStream.
4. Generates PostgreSQL credentials in a Kubernetes Secret.
5. Applies the database schema through a migration Job.
6. Builds and deploys the API, worker, scheduler, scheduled executor, and
   deterministic target agent.
7. Runs provider conformance tests and both live application workflows.

It does not change your default Kubernetes context or touch the normal
`truvag3-demo-$(whoami)` cluster.

### 3. Confirm success

A successful run reports all three outcomes:

```text
Provider conformance and mixed-composition proof passed
Live API -> NATS worker -> PostgreSQL proof passed
Scheduler proof passed with exactly one PostgreSQL task and target attempt 1
```

You can then inspect the deployment:

```bash
./setup.sh status
```

The default browser-accessible endpoints are:

```text
API:       http://portability-agent.localhost:18080
Scheduler: http://portability-scheduler.localhost:18080
Target:    http://portability-target.localhost:18080
```

### 4. Ask each backend for independent evidence

```bash
./setup.sh evidence
```

This prints SQL rows from PostgreSQL, stream and consumer state from NATS, and
service-registration and lock state from Redis. It exits with an error if the
observed data does not support the claimed architecture.

The failure scenarios are intentionally separate because they temporarily
stop workers and inject a target-agent failure:

```bash
./setup.sh failure-test
```

## What backend portability means

In plain language, the application asks for a behavior—not a technology.

For example, a worker needs to consume a task. It depends on TruvaG3's
`TaskConsumer` contract; it does not know that NATS JetStream delivers the task.
The composition code chooses NATS and hands the worker that contract.

This separation lets an application replace one capability without rewriting
unrelated application logic:

```text
application logic --> framework contract <-- provider adapter
```

The design deliberately uses several focused contracts instead of one generic
`DataStore` interface. A durable record, a notification, a work queue, and a
distributed lock have different consistency, retry, ordering, and failure
semantics. Keeping those differences visible is what makes the composition
portable.

### Provider selection in this example

| Capability | Provider | Main consumer |
|---|---|---|
| Workflow execution state | PostgreSQL | API and worker |
| Schedule definitions | PostgreSQL | Scheduler |
| Materialized scheduled-task state | PostgreSQL | Scheduler and scheduled executor |
| Checkpoint commands | NATS Core Pub/Sub | Conformance scenario |
| Task dispatch and consumption | NATS JetStream | API, worker, scheduler, and scheduled executor |
| Service registration and discovery | Redis | Scheduler, target agent, and scheduled executor |
| Scheduler leadership lock | Redis through the example-owned adapter | Scheduler replicas |
| Checkpoint persistence | Redis preset | Mixed-composition conformance scenario |

If some of those terms are new:

- A **contract** is a Go interface that describes required behavior.
- A **provider adapter** implements a contract using a particular technology.
- A **composition root** is the small part of the application that chooses and
  connects concrete providers.
- A **conformance suite** runs the same behavioral tests against any
  implementation of a contract.
- **JetStream** is NATS's persistent streaming and work-queue layer.

## Architecture at a glance

Everything runs in the `truvag3-examples` namespace of the dedicated cluster.
There is exactly one Redis deployment.

```text
                         APPLICATION ROLES

       API (2) ------------------------------ Worker (2)
          |                                      |
          | workflow state                       | workflow state
          v                                      v
                        PostgreSQL
          |                                      ^
          | dispatch                             | consume + acknowledge
          v                                      |
                      NATS JetStream


  Scheduler (2) ---------------------- Scheduled executor (2)
      |     |                                  |          |
      |     `-- dispatch --> NATS JetStream ---'          |
      |                                                    |
      | schedule/task state              target discovery  |
      v                                      via Redis     v
  PostgreSQL                                      Target agent (1)
      ^
      `---------------- completed task result -------------'

  Redis also provides scheduler leadership and service registration.
```

One application image runs in five modes through `TRUVAG3_MODE`:

| Mode | Responsibility | Framework shape |
|---|---|---|
| `api` | Accept work and dispatch it | `BaseAgent` with HTTP handlers |
| `worker` | Consume work and persist results | `BaseAgent` with a `core.Runnable` |
| `scheduler` | Materialize due schedules | `Tool` with the neutral scheduler runnable |
| `scheduled-executor` | Deliver scheduled tasks to agents | `BaseAgent` with an executor runnable |
| `target-agent` | Handle deterministic scheduled requests | Redis-registered `BaseAgent` |

The deterministic worker and target agent require no LLM. That keeps the test
repeatable and ensures it measures backend behavior rather than model behavior.

## Follow the two workflows

### Workflow 1: API to worker

```text
POST /tasks
     |
     v
API saves pending execution in PostgreSQL
     |
     v
API dispatches a task to NATS JetStream
     |
     v
Worker consumes the task
     |
     v
Worker stores the completed execution in PostgreSQL
     |
     v
Worker acknowledges the NATS task
```

The persistence-before-acknowledgement order is important. If PostgreSQL is
temporarily unavailable, the worker does not acknowledge the message, allowing
JetStream to deliver it again.

`./setup.sh live-test` submits a request, follows it to completion, and verifies
that the application result does not simply claim which providers were used.

### Workflow 2: Scheduler to target agent

```text
schedule_task request
     |
     v
Scheduler stores the schedule in PostgreSQL
     |
     v
One scheduler replica acquires the Redis leadership lock
     |
     v
Scheduler creates one deterministic task in PostgreSQL
     |
     v
Scheduler dispatches the task through NATS JetStream
     |
     v
Scheduled executor discovers the target agent through Redis
     |
     v
Target agent handles /api/v1/scheduled
     |
     v
Executor stores the result in PostgreSQL, then acknowledges NATS
```

Two scheduler replicas are deployed deliberately. The Redis lock and
deterministic task ID must still result in exactly one PostgreSQL task.

## Explore the running example

### Inspect the API's selected backends

```bash
curl -sS http://portability-agent.localhost:18080/backends | jq
```

The descriptor is derived from the validated composition. It is useful for
diagnostics, but it is not considered sufficient evidence by itself.

### Submit work manually

```bash
response="$(curl -sS -X POST \
  http://portability-agent.localhost:18080/tasks \
  -H 'Content-Type: application/json' \
  -d '{"location":"Chicago, IL"}')"

printf '%s\n' "$response" | jq
execution_id="$(printf '%s\n' "$response" | jq -r '.execution_id')"
curl -sS "http://portability-agent.localhost:18080/tasks/$execution_id" | jq
```

The first status read may still be `pending` or `running`; repeat the last
command until it becomes `completed`.

### Run the complete scheduler scenario

```bash
./setup.sh scheduler-test
```

This command creates a short one-shot schedule, derives its deterministic task
ID, waits for completion, and confirms directly in PostgreSQL that two
scheduler replicas produced one task.

### Open the cluster in FreeLens or another Kubernetes UI

Import the contents of:

```text
examples/orchestration-backend-portability/.state/kubeconfig
```

The kubeconfig is generated locally and ignored by Git.

## Code tour

The folder follows the same startup and lifecycle conventions as other TruvaG3
examples. Read only as deeply as your task requires.

### First visit

1. `main.go` selects the process mode and owns startup and shutdown.
2. `backends.go` chooses the concrete providers for each mode.
3. `agent.go` and `worker.go` show the simplest request flow.

### Application developer

4. `scheduler.go` shows a standard scheduler tool and scheduled-executor
   runnable using non-default backends.
5. `target_agent.go` shows the included deterministic scheduled endpoint.
6. `k8-deployment*.yaml` shows how the same image is configured for each role.

### Provider implementer

7. `internal/postgresadapter` implements the durable-state contracts.
8. `internal/natsadapter` implements command delivery and task transport.
9. `internal/redisadapter` implements the owner-safe scheduler lock.
10. `portability_test.go` runs live-provider conformance and mixed composition.
11. `migrations/001_create_orchestration_tables.sql` owns schema creation.
12. `scripts/` performs independent backend inspection.

Provider SDK imports are allowed only in `backends.go`, tests, and the internal
adapter packages. `dependency_direction_test.go` turns that architecture rule
into an executable check.

## How composition works

`backends.go` provides one explicit builder for each role:

```go
buildAPIBackends(ctx, config)
buildWorkerBackends(ctx, config)
buildSchedulerBackends(ctx, config)
buildExecutorBackends(ctx, config)
```

Each builder does six things:

1. Validates only the configuration needed by that role.
2. Opens only the provider connections needed by that role.
3. Constructs implementations of focused framework contracts.
4. Installs those implementations into `OrchestrationBackends`.
5. Validates the composition against that role's `BackendRequirements`.
6. Returns narrow dependencies plus an owner that closes resources.

This means the worker receives a `StateStore` and `TaskConsumer`, not a
PostgreSQL pool and NATS connection. The scheduler receives `ScheduleStore`,
`TaskStore`, `TaskDispatcher`, and `DistributedLock`. Neither component needs to
understand provider configuration.

This is also why there is no universal runtime object passed everywhere: a
process should not configure or connect to capabilities it does not use.

## Replace a provider

Start with one capability, not the entire backend set. For example, you could
replace PostgreSQL workflow state while leaving schedules and tasks unchanged.

1. Identify the framework contract for the capability.
2. Implement it in an application-owned adapter package.
3. Run the reusable conformance suite for that contract.
4. Change only the role builders that use the capability.
5. Update the composition-derived provider descriptor.
6. Add configuration, credentials, migrations, and deployment resources at the
   application boundary.
7. Add a direct evidence check against the new provider.
8. Re-run live and failure tests.

For an existing agent, keep its normal `core.NewFramework` startup and
registered runnables. Move provider construction into its composition root and
continue passing framework contracts into the application components.

Avoid creating a generic interface that combines durable state, notifications,
queues, and locks. That hides precisely the semantics a portable implementation
needs to preserve.

## Setup commands

Run `./setup.sh help` for the authoritative command list.

### Everyday commands

| Command | Use it when |
|---|---|
| `./setup.sh full-deploy` | Starting from no portability cluster |
| `./setup.sh deploy` | The portability cluster exists and code should be built and deployed |
| `./setup.sh rebuild` | You need a no-cache image rebuild |
| `./setup.sh rollout` | Only configuration changed; no image rebuild is needed |
| `./setup.sh status` | You want resource state and access URLs |
| `./setup.sh logs` | You want application and conformance logs |
| `./setup.sh forward` | Local ingress is unavailable and you need port forwards |

### Verification commands

| Command | What it verifies |
|---|---|
| `./setup.sh conformance-test` | PostgreSQL, NATS, Redis lock, and mixed-composition contracts |
| `./setup.sh live-test` | API → NATS → worker → PostgreSQL |
| `./setup.sh scheduler-test` | PostgreSQL → NATS → Redis discovery → target → PostgreSQL |
| `./setup.sh failure-test` | Redelivery, worker restart, target retry, and scheduler idempotency |
| `./setup.sh evidence` | Direct PostgreSQL, NATS, Redis, migration, secret, and single-Redis facts |

### Cleanup commands

| Command | What it removes |
|---|---|
| `./setup.sh cleanup` | Portability-owned resources; retains the cluster, shared Redis, and ingress |
| `./setup.sh cleanup-all` | Only the dedicated portability Kind cluster |

Calling `./setup.sh` without an argument displays help and does not deploy
anything.

## Cluster isolation and configuration

| Setting | Default |
|---|---|
| Kind cluster | `truvag3-portability-$(whoami)` |
| Namespace | `truvag3-examples` |
| Kubeconfig | `.state/kubeconfig` |
| HTTP ingress port | `18080` |
| HTTPS ingress mapping | `18443` |

The script refuses to use the normal `truvag3-demo-$(whoami)` cluster name. All
cluster-changing commands use the isolated kubeconfig.

`.env.example` documents every override. `setup.sh` creates a local `.env` if
one is absent. Common settings are:

| Variable | Purpose |
|---|---|
| `TRUVAG3_CLUSTER_NAME` | Choose another dedicated cluster name |
| `PORTABILITY_HTTP_PORT` | Change the host HTTP port before cluster creation |
| `PORTABILITY_HTTPS_PORT` | Change the host HTTPS mapping before cluster creation |
| `PORTABILITY_POSTGRES_PASSWORD` | Override the generated local password |
| `PORTABILITY_BACKEND_NAMESPACE` | Change logical backend isolation |

PostgreSQL credentials live in a Kubernetes Secret and are not printed or
stored in a ConfigMap. Application replicas do not create schema at startup;
the migration Job completes first.

## Troubleshooting

| Symptom | What to check | Suggested action |
|---|---|---|
| Docker is installed but not responding | `docker info` | Start Docker Desktop or select a running Podman engine |
| “Dedicated cluster does not exist” | `kind get clusters` | Run `./setup.sh full-deploy` |
| Port 18080 or 18443 is already in use | `lsof -nP -iTCP:18080 -sTCP:LISTEN` | Choose unused ports in `.env`, then recreate the cluster |
| A pod is not ready | `./setup.sh status` | Run `./setup.sh logs` and inspect the failing pod events in Kubernetes |
| `.localhost` URLs do not resolve | Browser/host DNS behavior | Run `./setup.sh forward` and use ports 18081–18083 |
| A verification command fails | The command's final error and workload logs | Run `./setup.sh evidence`, then `./setup.sh logs` |
| FreeLens cannot see the cluster | Imported kubeconfig or stopped container runtime | Re-import `.state/kubeconfig` and confirm Docker/Podman is running |
| You need a completely clean retry | Existing dedicated cluster state | Run `./setup.sh cleanup-all`, then `./setup.sh full-deploy` |

## Local development

Runnable examples intentionally live outside the framework's `go.work`. Use
`GOWORK=off` for local Go commands:

```bash
GOWORK=off go test ./...
GOWORK=off go test -race ./...
GOWORK=off go vet ./...
GOWORK=off go build ./...
```

Live provider tests require Kubernetes infrastructure and are run through
`setup.sh`. Ordinary `go test` skips them when the integration opt-in is absent.

Generated files are ignored:

- `.env` contains local overrides;
- `.build/` contains local binaries;
- `.state/` contains the isolated kubeconfig and runtime evidence;
- `coverage.out` contains local coverage data.

## Conformance scope

This table is the exact boundary of the portability claim. “Covered here”
means this folder runs the framework's reusable behavioral suite against the
listed adapter. A capability marked out of scope is not silently implied by the
end-to-end demonstration.

| Framework contract | Provider in this example | Coverage here | Reason or boundary |
|---|---|---|---|
| Workflow state | PostgreSQL | Reusable suite against live PostgreSQL | Alternative durable workflow implementation |
| Schedule store | PostgreSQL | Reusable suite against live PostgreSQL | Alternative durable schedule implementation |
| Task store | PostgreSQL | Reusable suite against live PostgreSQL | Alternative durable task implementation |
| Command store | NATS Core Pub/Sub | Reusable suite against live NATS | Alternative checkpoint-command transport |
| Task dispatcher and consumer | NATS JetStream | At-least-once delivery suite against live NATS | Alternative asynchronous task transport |
| Distributed lock | Redis through `internal/redisadapter` | Reusable suite against miniredis and live Redis | Retained Redis capability with example-owned, owner-safe release semantics |
| Checkpoint persistence | Framework Redis preset | Not rerun here | Retained provider implementation; mixed composition verifies coexistence, while its provider module owns conformance |
| Execution debug store | Not composed | Out of scope | No application role in this reference consumes it |
| LLM debug store | Not composed | Out of scope | The deterministic reference does not call an LLM |
| Legacy task queue | Not composed | Out of scope | The reference uses the split `TaskDispatcher` and `TaskConsumer` contracts |
| Skills contracts | Not composed | Out of scope | Skill publication and administration are unrelated to this proof |

## What this example proves

The automated checks establish that:

- PostgreSQL satisfies workflow, schedule, and task-store conformance;
- NATS Core satisfies command-store conformance;
- NATS JetStream satisfies dispatch, consumption, acknowledgement,
  deduplication, dead-letter, and abandoned-claim behavior;
- the example-owned Redis lock preserves leases across competing owners,
  expiration, stale release, and cancellation;
- PostgreSQL, NATS, and Redis can coexist in one validated capability
  composition;
- API/worker and scheduler/executor flows work with multiple replicas;
- queued work survives a complete worker shutdown and restart;
- transient target failure is retried;
- two scheduler replicas materialize one deterministic task;
- provider claims can be verified directly in the underlying systems.

For the implementation checklist and rationale, see
[REFERENCE_IMPLEMENTATION_PLAN.md](REFERENCE_IMPLEMENTATION_PLAN.md).

## Production boundaries

Passing this example proves the framework's public portability seam. It does not
make the included PostgreSQL and NATS adapters supported production providers.

The local stack uses single infrastructure replicas, ephemeral volumes, and
development-oriented connectivity. Production promotion would additionally
require:

- durable storage and migration policy;
- authentication, authorization, and TLS;
- high availability and disaster recovery;
- operational metrics, diagnostics, and runbooks;
- performance and capacity testing;
- compatibility and versioning commitments;
- a supported release and maintenance process.

Use this folder as a composition and conformance reference, then apply your
organization's production requirements to the provider implementations.
