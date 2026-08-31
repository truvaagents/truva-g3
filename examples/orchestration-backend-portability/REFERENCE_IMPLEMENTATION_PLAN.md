# Orchestration Backend Portability Reference Implementation Plan

Status: Implementation complete; documentation ready for human sign-off
Last updated: 2026-08-28

## Table of Contents

- [1. Purpose](#1-purpose)
- [2. Authoritative Constraints](#2-authoritative-constraints)
- [3. Starting State](#3-starting-state)
- [4. Target Outcome](#4-target-outcome)
- [5. Non-Goals](#5-non-goals)
- [6. Target Architecture](#6-target-architecture)
- [7. Alignment with Existing Examples](#7-alignment-with-existing-examples)
- [8. Target Source Layout](#8-target-source-layout)
- [9. Backend Composition Design](#9-backend-composition-design)
- [10. Self-Contained Reference Scenario](#10-self-contained-reference-scenario)
- [11. Kubernetes and Cluster Design](#11-kubernetes-and-cluster-design)
- [12. Setup Script Interface](#12-setup-script-interface)
- [13. Configuration and Secrets](#13-configuration-and-secrets)
- [14. Database Migration](#14-database-migration)
- [15. Verification Strategy](#15-verification-strategy)
- [16. Documentation Plan](#16-documentation-plan)
- [17. Implementation Phases](#17-implementation-phases)
- [18. Completion Criteria](#18-completion-criteria)
- [19. Risks and Guardrails](#19-risks-and-guardrails)
- [20. Implementation Status](#20-implementation-status)

## 1. Purpose

Convert `examples/orchestration-backend-portability` from a Phase 4 proof harness
into a self-contained reference implementation that other TruvaG3 developers can
copy, understand, and adapt without learning a new example structure.

The reference must demonstrate that an application outside the framework modules
can compose capability-specific orchestration backends as follows:

| Capability | Provider |
|---|---|
| Workflow state | PostgreSQL |
| Schedule definitions | PostgreSQL |
| Materialized task state | PostgreSQL |
| Checkpoint commands | NATS Core Pub/Sub |
| Task dispatch and consumption | NATS JetStream |
| Service discovery and registration | Redis |
| Scheduler leadership lock | Redis |
| Retained checkpoint persistence | Redis |

Provider-specific construction must remain at the application composition root.
Agent, tool, scheduler, executor, and worker logic must consume framework
contracts rather than PostgreSQL, NATS, or Redis implementations.

## 2. Authoritative Constraints

Implementation decisions in this plan must conform to these repository sources:

- [Framework Design Principles](../../FRAMEWORK_DESIGN_PRINCIPLES.md)
- [Core Architecture](../../core/ARCHITECTURE.md)
- [Orchestration Architecture](../../orchestration/ARCHITECTURE.md)
- [Backend Portability Design](../../orchestration/notes/ORCHESTRATION_BACKEND_PORTABILITY_DESIGN.md)
- [Examples conventions](../AGENTS.md)
- [Portable Example Contract](../README.md#portable-example-contract)

The following rules are binding:

1. Capability-specific contracts remain separate; do not collapse state,
   messages, queues, and locks into a universal datastore interface.
2. Dependency direction remains example/application to framework. The framework
   must not import this example or its providers.
3. Concrete providers appear only at composition roots and inside provider
   adapter packages.
4. Background work runs as `core.Runnable` through the framework lifecycle.
5. Feature requirements and validation must match what each process constructs.
6. The default example must be runnable without sibling example directories.
7. Deployment must go through this example's `setup.sh`.
8. Standard TruvaG3 example naming, configuration, lifecycle, telemetry, and
   deployment conventions take precedence over proof-harness convenience.

## 3. Starting State

The original folder proved the public portability seam, but was not yet suitable
as a copyable reference implementation.

The starting limitations were:

- An application entrypoint under `cmd/portable-agent` rather than the
  standard top-level `main.go` layout used by examples.
- Application code under `internal/liveapp`, obscuring the normal example
  reading order.
- One `NewRuntime` constructing every backend for every process, including
  capabilities that a process does not use.
- Placeholder configuration such as `scheduler-unused`.
- A custom `PORTABILITY_MODE` differing from the established `TRUVAG3_MODE`
  convention.
- API and worker lifecycle code not consistently following the standard
  `core.NewFramework` plus `RegisterRunnable` pattern.
- Backend proof strings written into results by the same application being
  tested instead of being derived from composition and independently verified.
- Schema creation during process startup rather than through an explicit
  migration step.
- PostgreSQL credentials embedded in ConfigMaps.
- Kubernetes manifests using a proof-specific `k8s/` layout rather than the
  recognizable `k8-deployment*.yaml` example convention.
- A live proof requiring separately deployed sibling examples.
- A dedicated cluster without host ingress mappings.
- A second Redis existing solely for conformance, creating avoidable namespace and
  ownership confusion.
- Reproducible evidence tooling under ignored `.state` rather
  than as reviewed example tooling.

The implementation preserved the original provider adapters, conformance suites,
and verified behavior while simplifying the application structure.

## 4. Target Outcome

A developer familiar with `agent-with-async`, `scheduler-tool`, or
`scheduled-executor` should be able to understand the reference by reading:

1. `README.md` for the five-minute quick start and architecture;
2. `main.go` for standard startup and `TRUVAG3_MODE` selection;
3. `backends.go` for the only portability-specific composition;
4. the role file relevant to their application;
5. an internal provider adapter only when implementing another provider.

The default scenario must:

- create an isolated parallel Kind cluster;
- expose browser-accessible endpoints on non-conflicting host ports;
- deploy all required open-source infrastructure and application components;
- require no sibling examples and no AI-provider key;
- run with `./setup.sh full-deploy`;
- prove both API/worker and scheduler/executor paths;
- preserve provider conformance suites;
- support independent PostgreSQL, NATS, and Redis verification.

## 5. Non-Goals

This conversion will not:

- move PostgreSQL or NATS providers into framework modules;
- advertise the internal adapters as production-supported providers;
- replace Redis discovery or the scheduler lock in this phase;
- prove multi-region or managed-cloud provider behavior;
- turn this example into a universal application scaffold;
- require the real `travel-chat-agent` or any LLM for the default path;
- retain audit history as a primary concern during the refactor;
- modify unrelated existing examples to adopt these providers.

The real travel-agent path may remain as an optional repository-only integration
profile after the self-contained reference is complete.

## 6. Target Architecture

### 6.1 API and worker path

```text
HTTP task request
       |
       v
Reference API replicas
       |-- create/update workflow state --> PostgreSQL
       `-- dispatch task -----------------> NATS JetStream
                                               |
                                               v
                                      Reference worker replicas
                                               |
                                               |-- load/update workflow --> PostgreSQL
                                               `-- complete NATS claim
```

### 6.2 Scheduler and executor path

```text
schedule_task
       |
       v
Reference scheduler-tool replicas
       |-- schedule/task state ----------> PostgreSQL
       |-- leadership lock --------------> Redis
       `-- task dispatch ----------------> NATS JetStream
                                               |
                                               v
                                  Scheduled-executor replicas
                                               |-- task state --> PostgreSQL
                                               |-- discovery --> Redis
                                               |-- HTTP ------> Included target agent
                                               `-- Ack -------> NATS JetStream
```

### 6.3 Dependency direction

```text
agent/tool/worker/scheduler/executor logic
                    |
                    v
           framework contracts
                    ^
                    |
              backends.go
          /         |          \
 PostgreSQL      NATS         Redis
  adapters      adapters      provider
```

Only `backends.go` and the internal provider packages may import provider SDKs.

## 7. Alignment with Existing Examples

The reference will deliberately mirror existing examples rather than introduce
a separate application framework.

| Existing convention | Reference implementation |
|---|---|
| Top-level `main.go` | Startup and `TRUVAG3_MODE` selection |
| `core.NewBaseAgent` / `core.NewTool` | Standard component construction |
| `core.NewFramework` | HTTP, discovery, middleware, and lifecycle |
| `framework.RegisterRunnable` | Workers, scheduler, and executor |
| Telemetry before component work | Same startup ordering and shutdown pattern |
| `.env.example` | All user-facing configuration and defaults |
| `Dockerfile` and `Dockerfile.workspace` | Standard standalone/workspace builds |
| `k8-deployment*.yaml` | Recognizable split-role manifests |
| Shared setup helper | Source `../k8-deployment/setup-env-lib.sh` |
| Standard setup verbs | Deploy, rebuild, rollout, logs, status, cleanup |
| `TRUVAG3_MODE` | `api`, `worker`, `scheduler`, `scheduled-executor`, `target-agent` |

The scheduler startup sequence will match `scheduler-tool` except for backend
construction. The scheduled executor will match `scheduled-executor` except for
its task state and transport providers. The API/worker role split will match
`agent-with-async`.

## 8. Target Source Layout

```text
orchestration-backend-portability/
├── .env.example
├── Dockerfile
├── Dockerfile.workspace
├── README.md
├── REFERENCE_IMPLEMENTATION_PLAN.md
├── go.mod
├── go.sum
├── main.go
├── agent.go
├── handlers.go
├── worker.go
├── scheduler.go
├── target_agent.go
├── backends.go
├── backends_test.go
├── portability_test.go
├── internal/
│   ├── postgresadapter/
│   │   ├── workflow_store.go
│   │   └── scheduler_stores.go
│   └── natsadapter/
│       ├── command_store.go
│       └── task_transport.go
├── migrations/
│   └── 001_create_orchestration_tables.sql
├── scripts/
│   ├── collect-evidence.sh
│   ├── verify-evidence.sh
│   └── sql/
│       ├── inspect-workflows.sql
│       ├── inspect-schedules.sql
│       └── inspect-tasks.sql
├── k8-deployment-infra.yaml
├── k8-deployment.yaml
├── k8-deployment-api.yaml
├── k8-deployment-worker.yaml
├── k8-deployment-scheduler.yaml
├── k8-deployment-executor.yaml
├── k8-deployment-target.yaml
└── setup.sh
```

The scheduler tool and scheduled-executor runnable share `scheduler.go` because
they implement the same scheduled-task flow. The top-level reading order and
provider isolation remain recognizable.

## 9. Backend Composition Design

### 9.1 Role-specific builders

`backends.go` will expose explicit builders rather than a generic dependency
injection container:

```go
buildAPIBackends(ctx, config)
buildWorkerBackends(ctx, config)
buildSchedulerBackends(ctx, config)
buildExecutorBackends(ctx, config)
```

Each builder must:

1. identify its feature requirements;
2. open only the provider connections needed for those requirements;
3. build the minimal `OrchestrationBackends` composition;
4. override Redis defaults with PostgreSQL and NATS contracts;
5. call `ValidateFor` with that role's requirements;
6. expose a composition-derived descriptor;
7. return an owner/closer for all opened resources.

### 9.2 Narrow consumer dependencies

Role constructors must not receive a universal runtime object. They will accept
narrow dependencies, for example:

```go
type WorkerDeps struct {
    WorkflowState orchestration.StateStore
    Consumer      core.TaskConsumer
    Discovery     core.Discovery
    Logger        core.Logger
}
```

Equivalent narrow dependency structures will be used for the API, scheduler,
and scheduled executor.

### 9.3 Provider descriptor

Provider diagnostics must be derived from the constructed composition and
concrete adapter registration. Application results must not contain hard-coded
provider claims such as `"workflow_state": "postgresql"`.

The reference may expose a diagnostic endpoint for human inspection, but tests
must also query the underlying providers independently.

### 9.4 Dependency-direction enforcement

An executable test will ensure that application files do not import:

- `github.com/jackc/pgx`;
- `github.com/nats-io/nats.go`;
- `github.com/redis/go-redis`;
- internal provider adapter packages.

Allowed import locations will be limited to `backends.go` and provider adapter
packages.

## 10. Self-Contained Reference Scenario

### 10.1 Included deterministic target agent

The example will include a small `BaseAgent` target that:

- registers in Redis discovery through the standard framework;
- exposes the standard `/api/v1/scheduled` endpoint;
- returns a deterministic response containing the schedule ID, task ID, and
  supplied instruction;
- requires no external API or LLM key;
- runs through `core.NewFramework` like existing agents.

This target exists only to make the default scheduler proof self-contained. It
must not contain provider-specific orchestration code.

### 10.2 Default live behavior

The API/worker path will use deterministic application logic so it can prove
state and transport without external services. Redis discovery will be proven
through target registration and resolution in the scheduler path.

### 10.3 Optional real-agent profile

An optional future repository-only command may exercise `travel-chat-agent`:

```bash
./setup.sh external-travel-test
```

That command is intentionally deferred. If added later, it must be clearly
documented as optional and must not be part of `full-deploy`, ordinary CI, or
the portable example contract.

## 11. Kubernetes and Cluster Design

### 11.1 Dedicated parallel cluster

The current portability cluster may be deleted. The updated setup will recreate
the reference in an isolated Kind cluster:

| Setting | Default |
|---|---|
| Cluster | `truvag3-portability-$(whoami)` |
| Kubeconfig | `.state/kubeconfig` |
| Namespace | `truvag3-examples` |
| Host HTTP port | `18080` |
| Host HTTPS port | `18443` |

The normal `truvag3-demo-$(whoami)` cluster must remain protected. The setup
script must reject that name as a portability target.

### 11.2 Ingress access

The Kind node will use explicit mappings:

```yaml
extraPortMappings:
  - containerPort: 80
    hostPort: 18080
    protocol: TCP
  - containerPort: 443
    hostPort: 18443
    protocol: TCP
```

Default access URLs:

```text
http://portability-agent.localhost:18080
http://portability-scheduler.localhost:18080
http://portability-target.localhost:18080
```

The setup script must check port availability before cluster creation and
support explicit setup-time overrides. `./setup.sh status` must print the
effective URLs. `./setup.sh forward` may remain as a fallback.

### 11.3 One namespace and one Redis

All default reference components will run in `truvag3-examples` inside the
dedicated cluster. The shared examples Redis will provide discovery,
registration, locking, and retained Redis capability groups.

The conformance-only Redis deployment will be removed. Conformance tests will
use unique logical namespaces against the single Redis instance.

### 11.4 Infrastructure ownership

`k8-deployment-infra.yaml` will contain reference-owned PostgreSQL, NATS, and
migration resources. Shared Redis and ingress deployment will use the existing
shared example infrastructure flow wherever possible.

## 12. Setup Script Interface

The primary command set will follow established example conventions:

```bash
./setup.sh full-deploy
./setup.sh deploy
./setup.sh rebuild
./setup.sh rollout
./setup.sh status
./setup.sh logs
./setup.sh forward
./setup.sh cleanup
./setup.sh cleanup-all
./setup.sh help
```

Portability-specific commands will be additive:

```bash
./setup.sh conformance-test
./setup.sh live-test
./setup.sh scheduler-test
./setup.sh failure-test
./setup.sh evidence
```

The deferred `external-travel-test` profile is not part of the implemented
command set or the completion criteria.

Behavioral requirements:

- No-argument invocation must show help and must not deploy anything.
- `full-deploy` must create the cluster, shared infrastructure, reference
  infrastructure, migration, and all default workloads.
- `deploy` must build and deploy the complete reference to an existing cluster.
- `rebuild` must force a no-cache image rebuild and redeploy.
- `rollout` must update configuration and restart workloads without pretending
  to rebuild code.
- `cleanup` must remove only the reference namespace/resources.
- `cleanup-all` must delete only the validated portability cluster.
- Every cluster-changing operation must use the isolated kubeconfig.

## 13. Configuration and Secrets

Add a self-documenting `.env.example` using established variables where they
exist:

- `TRUVAG3_MODE`;
- `TRUVAG3_CLUSTER_NAME`;
- `REDIS_URL`;
- `POSTGRES_URL`;
- `NATS_URL`;
- `PORT`;
- `NAMESPACE`;
- `DEV_MODE`;
- `TRUVAG3_LOG_LEVEL`;
- `TRUVAG3_LOG_FORMAT`.

Example-specific settings without framework equivalents must be grouped and
clearly documented.

Non-secret endpoints belong in ConfigMaps. PostgreSQL credentials must be
generated or loaded through a Kubernetes Secret. No production-looking secret
or API key may be committed.

`setup.sh` must use the shared environment helper's normal `.env.example` to
`.env` behavior.

## 14. Database Migration

Move schema definition to:

```text
migrations/001_create_orchestration_tables.sql
```

The migration must create:

- workflow execution table and workflow/time index;
- schedule table and due-selection index;
- task table and status/time index;
- composite namespace keys required for logical isolation.

A Kubernetes migration Job will run after PostgreSQL becomes ready and before
application rollout. Application replicas must not perform schema creation on
startup.

Conformance tests may apply migrations during fixture setup when running outside
the Kubernetes deployment sequence.

## 15. Verification Strategy

### 15.1 Unit tests

- Configuration validation per role.
- Role-specific composition requirements.
- Narrow constructor validation.
- Worker and executor success/failure behavior.
- Provider descriptor derivation.
- Dependency-direction enforcement.

### 15.2 Provider conformance

Preserve and expand:

- PostgreSQL workflow conformance;
- PostgreSQL schedule conformance;
- PostgreSQL task conformance;
- NATS command conformance;
- NATS JetStream task delivery conformance;
- mixed-provider composition validation.

### 15.3 Live tests

`live-test` must independently verify:

1. API submission;
2. PostgreSQL workflow creation;
3. NATS stream sequence/delivery change;
4. worker completion;
5. PostgreSQL terminal state;
6. NATS acknowledgement with no pending claim.

`scheduler-test` must independently verify:

1. schedule creation in PostgreSQL;
2. Redis leader lock existence and positive TTL;
3. task materialization in PostgreSQL;
4. NATS delivery;
5. Redis discovery of the included target agent;
6. target receipt of matching schedule/task IDs;
7. PostgreSQL terminal task state;
8. NATS acknowledgement.

### 15.4 Failure tests

`failure-test` will cover at minimum:

- abandoned NATS claim redelivery;
- worker restart while work is pending;
- two scheduler replicas producing one deterministic task;
- transient target failure and retry behavior;
- graceful shutdown of registered runnables.

### 15.5 Go gates

All Go changes require the repository's complete gate set:

- `go vet`;
- `go build ./...`;
- `go test ./...` and relevant race tests;
- `goimports`;
- `golangci-lint run`;
- `gosec`;
- `govulncheck`.

## 16. Documentation Plan

Rewrite `README.md` in the normal examples style:

1. What the example demonstrates.
2. Five-minute quick start.
3. Components and data ownership.
4. Access URLs and commands.
5. Familiar source reading order.
6. API/worker flow.
7. Scheduler/executor flow.
8. `backends.go` walkthrough.
9. How to replace one provider.
10. How to adopt the pattern in an existing agent.
11. Verification and direct backend inspection.
12. Troubleshooting.
13. Production limitations.

The README will include a comparison showing that standard examples and the
reference use the same lifecycle, with backend construction as the principal
difference.

Documentation edits remain uncommitted until explicitly approved, per repository
instructions.

## 17. Implementation Phases

### Phase 0: Remove the current cluster and establish a baseline

Tasks:

- [x] Confirm the current context and exact dedicated cluster name.
- [x] Run the current `./setup.sh clean-all` against only the portability
      cluster.
- [x] Confirm the normal examples cluster remains present and unchanged.
- [x] Run existing local Go tests before structural changes.
- [x] Record the current proof behaviors that must survive the refactor.

Exit criteria:

- The old portability cluster is absent.
- The normal examples cluster is untouched.
- The code baseline is understood and locally testable.

### Phase 1: Align source structure and lifecycle conventions

Tasks:

- [x] Move the entrypoint to top-level `main.go`.
- [x] Replace `PORTABILITY_MODE` with `TRUVAG3_MODE`.
- [x] Split application responsibilities into recognizable top-level files.
- [x] Adopt `BaseAgent`, `BaseTool`, `NewFramework`, and `RegisterRunnable`
      patterns from existing examples.
- [x] Add `.env.example`.
- [x] Add `Dockerfile.workspace` and align Docker behavior.
- [x] Preserve unit tests during moves.

Exit criteria:

- The application follows standard example startup and shutdown patterns.
- No provider behavior has changed yet.
- Existing unit and conformance tests pass.

### Phase 2: Introduce role-specific composition

Tasks:

- [x] Replace the universal runtime with explicit role builders in
      `backends.go`.
- [x] Define narrow dependencies for each role.
- [x] Construct only required capability groups per role.
- [x] Validate each role against its own feature requirements.
- [x] Derive the provider descriptor from actual composition.
- [x] Remove hard-coded provider proof strings from business results.
- [x] Add dependency-direction tests.

Exit criteria:

- Application logic imports no provider SDK or adapter package.
- No unused backend configuration is required.
- Role-specific validation tests pass.

### Phase 3: Make the default scenario self-contained

Tasks:

- [x] Add the deterministic target agent.
- [x] Register the standard scheduled endpoint.
- [x] Remove mandatory geocoding, weather, and travel-agent prerequisites.
- [x] Simplify the default API/worker workload to deterministic processing.
- [x] Keep real travel-agent integration out of the default path and defer any
      optional profile.

Exit criteria:

- Default tests require no sibling example and no AI key.
- Target discovery and scheduled invocation are observable.
- Portable Example Contract is satisfied.

### Phase 4: Standardize Kubernetes and setup behavior

Tasks:

- [x] Replace proof-specific manifests with top-level
      `k8-deployment*.yaml` files.
- [x] Use one namespace and one Redis.
- [x] Add PostgreSQL and NATS infrastructure resources.
- [x] Add the migration Job.
- [x] Move credentials to a Secret.
- [x] Add unique Kind ingress port mappings.
- [x] Add port-collision checks and URL reporting.
- [x] Standardize setup verbs and isolated-kubeconfig safeguards.

Exit criteria:

- `full-deploy` creates a complete parallel cluster.
- Both clusters can run simultaneously without host-port conflict.
- All default URLs are reachable.
- No duplicate Redis or cross-namespace dependency exists.

### Phase 5: Complete live and failure verification

Tasks:

- [x] Update conformance deployment and tests.
- [x] Implement deterministic `live-test`.
- [x] Implement deterministic `scheduler-test`.
- [x] Implement `failure-test`.
- [x] Add direct SQL, NATS, and Redis inspection scripts.
- [x] Promote reproducible evidence tooling into `scripts/`.
- [x] Run all Go gates.

Exit criteria:

- Conformance, live, scheduler, and failure tests pass.
- Backend state is independently inspectable.
- Evidence tooling is version-controlled and reproducible.
- Full repository Go gates pass.

### Phase 6: Documentation and final review

Tasks:

- [x] Rewrite the README as the reference adoption guide.
- [x] Update the top-level examples catalog.
- [x] Verify deployment and test commands from a clean cluster; verify help and
      non-destructive inspection commands against the resulting cluster.
- [x] Verify all document links.
- [x] Review against authoritative architecture documents.
- [ ] Request explicit human sign-off for documentation changes.

Exit criteria:

- A new developer can deploy and understand the example from the README.
- The implementation and documentation describe the same architecture.
- No acceptance criterion remains speculative or unverified.

## 18. Completion Criteria

The reference implementation is complete only when all of the following are
true:

- [x] `./setup.sh full-deploy` creates the parallel cluster and complete
      self-contained reference.
- [x] The normal examples cluster remains untouched.
- [x] Browser access works through isolated port 18080; port 18443 is reserved
      for the dedicated cluster's HTTPS ingress mapping.
- [x] The default scenario requires no sibling examples or AI-provider key.
- [x] Only `backends.go` and internal adapters import provider implementations.
- [x] Each process opens only the connections and capabilities it needs.
- [x] No placeholder or `*-unused` configuration remains.
- [x] Background processes use `core.Runnable` and framework shutdown.
- [x] Provider diagnostics are composition-derived.
- [x] Provider claims are verified independently against PostgreSQL, NATS, and
      Redis.
- [x] Database schema is applied through an explicit migration.
- [x] Credentials are not stored in ConfigMaps.
- [x] One Redis serves the intentionally retained Redis capabilities.
- [x] Provider conformance tests pass.
- [x] API/worker live tests pass with two replicas where applicable.
- [x] Scheduler/executor live tests pass with two replicas and one materialized
      task.
- [x] Failure and redelivery tests pass.
- [x] The full Go quality and security gate set passes.
- [x] The README follows established examples conventions and is independently
      usable.
- [ ] Documentation changes receive explicit human sign-off before commit.

## 19. Risks and Guardrails

| Risk | Guardrail |
|---|---|
| Reference becomes another bespoke framework | Mirror existing example startup, files, setup verbs, and lifecycle |
| Business logic imports providers | Dependency-direction test and narrow constructors |
| Every process constructs every backend | Role-specific builders and feature requirements |
| Self-reported provider proof hides wiring mistakes | Derive descriptors and query backends independently |
| Parallel cluster collides with existing ingress | Dedicated host ports with preflight checks |
| Cleanup targets the wrong cluster | Isolated kubeconfig, exact-name validation, reject normal cluster |
| Sibling examples become hidden requirements | Deterministic included target agent and self-contained default |
| Reference is mistaken for supported providers | Internal packages and explicit production-gap documentation |
| Schema changes race during startup | Explicit migration Job |
| Secrets leak into manifests or evidence | Kubernetes Secret, redaction, credential-pattern checks |
| Refactor loses conformance coverage | Preserve suites before changing application structure |
| Documentation drifts from implementation | Verify commands and acceptance criteria during Phase 6 |

## 20. Implementation Status

| Phase | Status | Notes |
|---|---|---|
| Phase 0: Cluster removal and baseline | Complete | Old dedicated cluster removed; normal cluster remained intact |
| Phase 1: Source and lifecycle alignment | Complete | Local unit tests, vet, and build pass |
| Phase 2: Role-specific composition | Complete | Narrow builders and dependency-direction test verified |
| Phase 3: Self-contained scenario | Complete | Deterministic target and both default flows verified without sibling examples or AI keys |
| Phase 4: Kubernetes and setup standardization | Complete | Clean `full-deploy`, migration, 18080 ingress, and parallel-cluster isolation verified |
| Phase 5: Verification and evidence | Complete | Conformance, live, failure, direct evidence, and full Go gates pass |
| Phase 6: Documentation and final review | Ready for sign-off | README, catalog, commands, links, and architecture alignment reviewed; explicit human approval remains |

Update this table and the phase checklists as implementation progresses. A phase
must not be marked complete until its exit criteria have been verified.
