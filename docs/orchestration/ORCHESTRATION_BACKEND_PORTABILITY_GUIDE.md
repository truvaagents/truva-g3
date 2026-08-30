# Orchestration Backend Portability Guide

Backend portability lets you choose the technologies that store orchestration
state, deliver notifications and tasks, and coordinate distributed work.
Agents, workers, schedulers, and orchestrators depend on framework contracts,
so they do not need to know which database or message broker you selected.

If you only want the included Redis setup, this guide starts with that simple
path. If you need to replace one backend, it shows how to keep the rest of the
included Redis composition. If you are implementing a new provider, it follows
the complete path from a framework contract to conformance tests and a
mixed-provider deployment.

> **Working examples**
>
> The examples in this guide are grounded in shipped code:
>
> - [`examples/travel-chat-agent/skills.go`](https://github.com/truvaagents/truva-g3/blob/main/examples/travel-chat-agent/skills.go)
>   uses the one-call Redis composition for a single process role.
> - [`examples/orchestration-backend-portability/`](https://github.com/truvaagents/truva-g3/tree/main/examples/orchestration-backend-portability)
>   is the complete PostgreSQL, NATS, and Redis reference implementation.
> - [`orchestration/backends.go`](https://github.com/truvaagents/truva-g3/blob/main/orchestration/backends.go)
>   defines a composition API that does not expose provider-specific software
>   development kits (SDKs).
> - [`orchestration/redisprovider/`](https://github.com/truvaagents/truva-g3/tree/main/orchestration/redisprovider)
>   contains the included Redis backend composition.
>
> The PostgreSQL and NATS adapters under the portability example are
> architectural proof code. They are not production-supported framework
> providers, and their Go `internal` packages cannot be imported by another
> application.

## Table of contents

- [Choose your path](#choose-your-path)
- [1. What backend portability means](#1-what-backend-portability-means)
- [2. Build the right mental model](#2-build-the-right-mental-model)
- [3. Quick start: use the included Redis composition](#3-quick-start-use-the-included-redis-composition)
- [4. Understand the three composition layers](#4-understand-the-three-composition-layers)
- [5. Construct only what a process needs](#5-construct-only-what-a-process-needs)
- [6. Validate and wire a composition](#6-validate-and-wire-a-composition)
- [7. Replace one capability](#7-replace-one-capability)
- [8. Follow the mixed-provider reference implementation](#8-follow-the-mixed-provider-reference-implementation)
- [9. Implement a provider adapter](#9-implement-a-provider-adapter)
- [10. Prove behavior with conformance tests](#10-prove-behavior-with-conformance-tests)
- [11. Handle ownership and background work](#11-handle-ownership-and-background-work)
- [12. Plan a provider migration](#12-plan-a-provider-migration)
- [13. Run the local portability proof](#13-run-the-local-portability-proof)
- [14. Troubleshooting](#14-troubleshooting)
- [15. Production review checklist](#15-production-review-checklist)
- [16. Quick reference](#16-quick-reference)
- [17. FAQ](#17-faq)
- [18. See also](#18-see-also)

---

## Choose your path

| Your goal | Recommended path |
|---|---|
| Learn backend composition from first principles | Start with [What backend portability means](#1-what-backend-portability-means), then continue through Section 6 |
| Give an agent the included Redis-backed skill registry | [Quick start](#3-quick-start-use-the-included-redis-composition) |
| Use Redis but create only selected backend groups | [Construct only what a process needs](#5-construct-only-what-a-process-needs) |
| Wire enabled orchestrator storage automatically | [Wire orchestrator-owned dependencies](#wire-orchestrator-owned-dependencies) |
| Replace one Redis capability with an application adapter | [Replace one capability](#7-replace-one-capability) |
| Understand the PostgreSQL/NATS/Redis proof | [Follow the mixed-provider reference implementation](#8-follow-the-mixed-provider-reference-implementation) |
| Implement another database, queue, notification service, or lock | [Implement a provider adapter](#9-implement-a-provider-adapter) |
| Verify an adapter before using it | [Prove behavior with conformance tests](#10-prove-behavior-with-conformance-tests) |
| Move live data and traffic to another provider | [Plan a provider migration](#12-plan-a-provider-migration) |
| Review lifecycle, operations, and production readiness | Start with [Handle ownership and background work](#11-handle-ownership-and-background-work), then continue through Section 15 |

---

## 1. What backend portability means

In plain language, application logic asks for a behavior, and application
startup chooses the technology that supplies it.

For example, a worker needs to receive tasks and report whether they completed
or should be retried. It depends on `core.TaskConsumer` and receives a
`core.TaskHandle`; it does not need to know whether Redis Streams, NATS
JetStream, SQS, or another transport delivered the task.

```text
application logic ---> framework contract <--- provider adapter
     Worker             TaskConsumer          NATS JetStream
```

The provider choice belongs in the application's **composition root**: the
small part of startup code that opens connections, creates adapters, validates
the result, and passes narrow interfaces to runtime components.

### Portability does not mean one generic datastore

TruvaG3 deliberately keeps several focused contracts instead of introducing a
single create, read, update, and delete (CRUD) `DataStore` interface. These
concerns have different correctness requirements:

| Category | Examples | Important semantics |
|---|---|---|
| Durable state | Workflow executions, checkpoints, schedules, tasks, debug records, skills | Atomic updates, conflicts, queries, retention, cross-instance visibility |
| Notification | Human-in-the-loop (HITL) commands and change notifications | Subscription cancellation, reconnect behavior, duplicate delivery, durability expectations |
| Work transport | Task dispatch and consumption | Competing consumers, acknowledgement, redelivery, dead letters, ordering |
| Coordination | Locks, leases, idempotency claims, leader election | Ownership, expiry, safe release, stopping when correctness cannot be guaranteed |

Combining those behaviors behind CRUD would move provider-specific assumptions
into consumers. Keeping the contracts narrow makes their semantics visible and
testable.

A few distributed-systems terms are important here. **Competing consumers** are
multiple workers that share a work source while each delivery is handled by one
worker. A **dead-letter destination** retains work that has exhausted its normal
retries. A **lease** grants temporary ownership that expires unless it is
renewed. An **idempotent** operation can be repeated without applying its effect
more than once.

### What can change without changing runtime logic

A backend change may replace:

- one implementation, such as workflow state moving from Redis to PostgreSQL;
- a capability group, such as scheduled work moving to NATS JetStream;
- or a complete preset—a ready-to-use composition of adapters and defaults—
  composed from several technologies.

The agent, planner, executor, HITL controller, scheduler, and worker continue to
consume the same domain contracts.

Backend portability does **not** automatically:

- change service discovery along with orchestration storage;
- migrate existing durable records or queued work;
- switch a provider in the middle of a request;
- make every provider's consistency model equivalent; or
- turn the reference PostgreSQL and NATS adapters into supported providers.

---

## 2. Build the right mental model

Seven terms explain most of the API:

| Term | Plain-English meaning |
|---|---|
| **Contract** | A focused Go interface describing behavior required by the framework, such as `StateStore` or `TaskConsumer`. |
| **Provider adapter** | An implementation of one contract using a particular technology. |
| **Capability** | The name used to install and validate one independently optional contract in `OrchestrationBackends`. |
| **Feature** | A higher-level framework behavior translated into one or more required capabilities. |
| **Preset** | A ready-to-use composition of provider adapters and configuration defaults. It may use one or more technologies, and applications can replace individual capabilities. |
| **Provider-neutral** | Depending only on framework contracts, without exposing a provider's SDK types or implementation details. |
| **Composition root** | Application startup code that selects providers, owns their resources, validates capabilities, and passes narrow dependencies onward. |

The startup flow is:

```text
effective application features
             |
             v
      BackendRequirements
             |
             v
provider adapters --options--> OrchestrationBackends
                             |
                             +-- ValidateFor(requirements)
                             |
                             +-- getters return narrow contracts
                             |
                             `-- Runnables register with Framework
```

### Who owns what

| Responsibility | Owner |
|---|---|
| Define runtime contracts and capability validation | `core` and `orchestration` |
| Implement a contract for a technology | Provider or application adapter package |
| Choose providers and configuration | Application composition root |
| Open and close provider connections | The constructor or application that created them |
| Start and cancel background work | `core.Framework` through `core.Runnable` |
| Move durable data and cut traffic over | Deployment and migration process |

### Keep the aggregate at the boundary

`OrchestrationBackends` belongs at application startup. Use it there to inspect,
validate, and unpack dependencies. Runtime components should not receive the
whole composition. Pass a worker only its `TaskConsumer`, a workflow engine only
its `StateStore`, and a skill runtime only its `SkillRegistry`.

This preserves the dependency direction:

```text
application composition
        |
        +----> Redis preset
        +----> PostgreSQL adapter
        `----> NATS adapter
                    |
                    v
       orchestration/core contracts
```

Root orchestration logic does not import provider presets, and provider SDK
types do not appear in the domain contracts.

### Discovery is a separate decision

Changing an orchestration backend does not implicitly change
`core.Discovery`. The portability reference intentionally demonstrates this:
the scheduled executor uses Redis discovery to find its target agent while
PostgreSQL stores task state and NATS JetStream transports the task.

---

## 3. Quick start: use the included Redis composition

The simplest production path is `redisprovider.NewDefaultBackends`. It reads the
documented Redis environment configuration, creates owned clients, constructs
the selected Redis adapter groups, validates them, and returns one cleanup
handle.

### Prerequisites

Your process needs:

- a `core.Logger`;
- a reachable Redis or Valkey endpoint; and
- `REDIS_URL` or `TRUVAG3_REDIS_URL` when the endpoint is not the local
  compatibility default.

For local Redis, the compatibility default is `redis://localhost:6379` when no
URL variable is set.

### Example: create only the skill backends

The shipped travel, QA, DevOps, and event-driven agents use this pattern. The
following helper is the pattern from
[`examples/travel-chat-agent/skills.go`](https://github.com/truvaagents/truva-g3/blob/main/examples/travel-chat-agent/skills.go):

```go
package main

import (
    "fmt"
    "io"

    "github.com/truvaagents/truva-g3/core"
    "github.com/truvaagents/truva-g3/orchestration"
    "github.com/truvaagents/truva-g3/orchestration/redisprovider"
)

func newSkillRegistry(
    logger core.Logger,
) (orchestration.SkillRegistry, io.Closer, error) {
    // Construct only the Redis role that supplies the skill contracts.
    owned, err := redisprovider.NewDefaultBackends(
        logger,
        redisprovider.WithDefaultBackendRoles(redisprovider.ClientRoleSkills),
    )
    if err != nil {
        return nil, nil, fmt.Errorf("create default skill backends: %w", err)
    }
    return owned.Backends().SkillRegistry(), owned, nil
}
```

The caller keeps the returned closer alive for as long as the registry is in
use and closes it after framework execution stops:

```go
registry, registryOwner, err := newSkillRegistry(agent.Logger)
if err != nil {
    return err
}
defer registryOwner.Close()

// Pass only registry to agent/orchestrator configuration.
```

<details>
<summary>Complete runnable example</summary>

Save this as `main.go` in a Go module that depends on TruvaG3. It connects to a
running Redis or Valkey instance, constructs only the skill backends, prints the
selected registry implementation, and closes the owned Redis client.

```go
package main

import (
    "fmt"
    "log"

    "github.com/truvaagents/truva-g3/core"
    "github.com/truvaagents/truva-g3/orchestration/redisprovider"
)

func main() {
    owned, err := redisprovider.NewDefaultBackends(
        &core.NoOpLogger{},
        redisprovider.WithDefaultBackendRoles(
            redisprovider.ClientRoleSkills,
        ),
    )
    if err != nil {
        log.Fatalf("create Redis skill backends: %v", err)
    }
    defer func() {
        if err := owned.Close(); err != nil {
            log.Printf("close Redis skill backends: %v", err)
        }
    }()

    registry := owned.Backends().SkillRegistry()
    fmt.Printf("skill registry ready: %T\n", registry)
}
```

With Redis listening on port 6379, run:

```bash
REDIS_URL=redis://localhost:6379 go run .
```

</details>

### What the convenience constructor does

For this process it:

1. loads the Redis URL and role-specific database configuration;
2. creates only the `skills` client role;
3. builds the runtime and administration skill contracts for that role;
4. validates every capability promised by the role; and
5. returns `OwnedBackends`, whose `Close` method releases the clients that the
   constructor creates.

It does not start a goroutine, pass Redis into the skill runtime, or construct
unrelated workflow, scheduling, human-in-the-loop, or debug roles.

### Add logical isolation

Use a provider namespace when applications share the same Redis deployment:

```go
owned, err := redisprovider.NewDefaultBackends(
    logger,
    redisprovider.WithDefaultBackendRoles(redisprovider.ClientRoleSkills),
    redisprovider.WithDefaultBackendProviderOptions(
        redisprovider.WithNamespace("travel-chat-agent"),
    ),
)
```

The namespace becomes a provider-private key prefix. Runtime feature code still
sees only the skill contracts.

> **Ownership rule:** `NewDefaultBackends` owns and closes the Redis clients it
> creates. Lower-level constructors that receive an application-created client
> do not close that client.

---

## 4. Understand the three composition layers

You can move between convenience and control one concern at a time.

| Layer | Use it when | Main API |
|---|---|---|
| **Layer 1: convenience** | You want environment-driven Redis with deterministic cleanup | `redisprovider.NewDefaultBackends` |
| **Layer 2: provider composition** | You need explicit client roles, database routing, option precedence, or injected Redis clients | `NewOwnedClients`, `NewClientSet`, `NewOptions`, `redisprovider.NewOrchestrationBackends` |
| **Layer 3: direct composition** | You are mixing technologies or implementing adapters yourself | `orchestration.NewOrchestrationBackends` and `With*Backend` options |

Layer 1 delegates to Layer 2, which produces the same provider-neutral
composition used by Layer 3. Replacing one concern does not require rewriting
every other concern.

### Layer 1: environment-driven Redis

```go
owned, err := redisprovider.NewDefaultBackends(
    logger,
    redisprovider.WithDefaultBackendRoles(
        redisprovider.ClientRoleScheduling,
    ),
    redisprovider.WithDefaultBackendProviderOptions(
        redisprovider.WithNamespace("orders"),
    ),
)
if err != nil {
    return err
}
defer owned.Close()

backends := owned.Backends()
```

Explicit code options are applied after environment values, so code wins when
both configure the same provider setting.

### Layer 2: explicit Redis construction

Use this layer when application startup must show each configuration and
ownership step:

```go
// Load environment configuration over the compatibility defaults.
clientConfig := redisprovider.DefaultClientConfig()

clientConfig, err := redisprovider.LoadClientConfigFromEnvironment(
    clientConfig,
    os.LookupEnv,
)
if err != nil {
    return err
}

// Open only the clients this process needs. This handle owns them.
clients, err := redisprovider.NewOwnedClients(
    clientConfig,
    redisprovider.WithOwnedClientRoles(
        redisprovider.ClientRoleWorkflow,
        redisprovider.ClientRoleScheduling,
    ),
)
if err != nil {
    return err
}
defer clients.Close()

// Load provider behavior from the environment.
providerOptions, err := redisprovider.NewOptions()
if err != nil {
    return err
}
providerOptions, err = redisprovider.LoadOptionsFromEnvironment(
    providerOptions,
    os.LookupEnv,
)
if err != nil {
    return err
}

// Apply explicit code options last, so they override environment values.
providerOptions, err = redisprovider.ConfigureOptions(
    providerOptions,
    redisprovider.WithLogger(logger),
    redisprovider.WithNamespace("orders"),
)
if err != nil {
    return err
}

// Build the provider-neutral composition from the owned clients and options.
backends, err := redisprovider.NewOrchestrationBackends(
    clients.ClientSet(),
    providerOptions,
)
if err != nil {
    return err
}
```

The default role-to-database assignments preserve legacy behavior. They are
compatibility values, not recommendations for a new Redis deployment. Use the
documented role-specific environment variables or
`redisprovider.WithRoleDatabase` when your deployment needs a different
isolation plan.

If your application already owns Redis clients, construct a `ClientSet`
instead. A non-nil default client is an explicit fallback for every role:

```go
clientSet, err := redisprovider.NewClientSet(redisClient)
```

To make construction feature-aware, pass a nil default and name each supplied
role:

```go
clientSet, err := redisprovider.NewClientSet(
    nil,
    redisprovider.WithRoleClient(
        redisprovider.ClientRoleWorkflow,
        workflowRedisClient,
    ),
)
```

The preset does not close clients supplied through `NewClientSet`.

### Layer 3: direct neutral composition

This is the application-owned path used by the portability example:

```go
backends, err := orchestration.NewOrchestrationBackends(
    orchestration.WithWorkflowBackend(postgresWorkflow),
    orchestration.WithTaskDispatcherBackend(natsTransport),
)
if err != nil {
    return err
}
```

`postgresWorkflow` and `natsTransport` are still passed through framework
interfaces. Only the composition root needs their provider-specific
constructors and SDK clients.

---

## 5. Construct only what a process needs

A web API, worker, scheduler, and administrative service rarely require the
same backends. Create and validate requirements per process role.

### Redis client roles

The Redis preset groups related capabilities into these client roles:

| Redis role | Capabilities constructed |
|---|---|
| `ClientRoleExecution` | Execution debug records |
| `ClientRoleLLMDebug` | Large language model (LLM) debug records |
| `ClientRoleHITL` | Checkpoint persistence, expired-checkpoint source, and commands |
| `ClientRoleWorkflow` | Workflow execution state |
| `ClientRoleScheduling` | Schedules, tasks, legacy task queue, dispatcher, consumer, and distributed lock |
| `ClientRoleSkills` | Skill registry, revision reader, administration, revision deletion, and audit |

With no role restriction, `NewDefaultBackends` constructs the full
compatibility preset. Prefer `WithDefaultBackendRoles` for a process that needs
only selected groups.

Checkpoint expiry processing is an additional lifecycle choice. The HITL role
supplies persistence and the atomic expired-item source; adding
`redisprovider.WithCheckpointExpiry(...)` constructs the processor and adds it
to the composition's runnable set.

### Capabilities and features are different

Use `NewBackendRequirements` when the application role already knows the exact
contracts it consumes:

```go
requirements, err := orchestration.NewBackendRequirements(
    orchestration.BackendWorkflowState,
    orchestration.BackendTaskDispatcher,
)
```

Use `RequirementsForFeatures` when a framework feature has a canonical group:

```go
requirements, err := orchestration.RequirementsForFeatures(
    nil,
    orchestration.BackendFeatureSchedulerProducer,
)
```

The current feature mapping is:

| Feature | Required capabilities |
|---|---|
| `BackendFeatureCheckpointPersistence` | Checkpoints |
| `BackendFeatureCrossInstanceHITL` | Checkpoints and commands |
| `BackendFeatureCheckpointExpiry` | Checkpoints, expired-checkpoint source, and expiry processor |
| `BackendFeatureWorkflow` | Workflow state |
| `BackendFeatureSchedulerProducer` | Schedules, tasks, task dispatcher, and distributed lock |
| `BackendFeatureScheduledWorker` | Task consumer |
| `BackendFeatureTaskStorage` | Task store |
| `BackendFeatureTaskQueue` | Legacy task queue |
| `BackendFeatureDistributedLock` | Distributed lock |
| `BackendFeatureSkillsRuntime` | Runtime skill registry |
| `BackendFeatureSkillsAdministration` | Skill registry, revision reader, administration, deletion, and audit |

`BackendSkills` and `BackendSkillRegistry` name the same runtime registry
capability. Prefer `BackendSkillRegistry` in new code; `BackendSkills` remains
source-compatible, while the four administrative skill contracts retain their
own capability constants and validation.

When passed an effective `OrchestratorConfig`, `RequirementsForFeatures` also
derives enabled execution-debug and LLM-debug requirements. It requires the
runtime skill registry when skills are enabled and at least one binding is
present. You must name the HITL and scheduler features yourself because no
single config struct records whether they are turned on.

Unknown and duplicate capabilities or features fail construction. Requirement
ordering does not matter, and `Capabilities()` returns a sorted defensive copy.

---

## 6. Validate and wire a composition

Validation should happen before runtime components start. It turns an absent or
partially constructed required backend into a startup error rather than a
failure on the first request.

### Validate exact role requirements

The API in the portability reference needs PostgreSQL workflow state and a NATS
dispatcher. Its builder follows this sequence:

```go
backends, err := orchestration.NewOrchestrationBackends(
    orchestration.WithWorkflowBackend(workflow),
    orchestration.WithTaskDispatcherBackend(transport),
)
if err != nil {
    return err
}

requirements, err := orchestration.NewBackendRequirements(
    orchestration.BackendWorkflowState,
    orchestration.BackendTaskDispatcher,
)
if err != nil {
    return err
}
if err := backends.ValidateFor(requirements); err != nil {
    return err
}

api, err := NewAPI(APIBackends{
    Workflow:   backends.Workflow(),
    Dispatcher: backends.TaskDispatcher(),
    Queue:      queue,
    WorkflowID: workflowID,
    // Descriptor is populated by the real buildAPIBackends implementation.
})
if err != nil {
    return err
}
```

Here `queue` and `workflowID` come from the API role's validated application
configuration, not from either provider adapter.

The complete implementation is
[`buildAPIBackends`](https://github.com/truvaagents/truva-g3/blob/main/examples/orchestration-backend-portability/backends.go).
The actual builder also validates configuration, owns connections, and derives
a diagnostic provider descriptor.

`ValidateFor` reports the complete sorted set of missing required capabilities.
Unused nil capabilities are allowed, which is what lets the API omit schedules,
commands, locks, and skill storage.

For example, a composition missing both the schedule store and task dispatcher
returns this startup error:

```text
orchestration: missing required backend capabilities: schedules, task_dispatcher
```

### Wire orchestrator-owned dependencies

`WireOrchestratorBackends` fills three storage dependencies owned directly by
`OrchestratorConfig`:

- execution debug storage when enabled;
- LLM debug storage when enabled; and
- the runtime skill registry when skills are enabled and bound.

```go
config := orchestration.NewDefaultOrchestratorConfig()

// Apply application feature configuration before wiring.
// config.ExecutionStore.Enabled = ...
// config.LLMDebug.Enabled = ...
// config.Skills = ...

if err := orchestration.WireOrchestratorBackends(config, backends); err != nil {
    return err
}

orchestrator := orchestration.NewAIOrchestrator(
    config,
    discovery,
    aiClient,
)
```

Explicit dependencies already present in the config win. The helper validates
all still-missing enabled dependencies before mutating the config, so a failed
call does not leave it partially wired.

### Unpack skill administration safely

An ordinary agent should receive only `SkillRegistry`. A skill management API
needs all five control-plane contracts:

```go
dependencies, err := backends.SkillAdministrationDependencies()
if err != nil {
    return err
}

handler, err := orchestration.NewSkillAdminHandler(dependencies)
```

The helper validates the complete management feature before returning the
dependency struct.

### Register provider background work

Composition constructors do not start background goroutines. Register every
returned runnable before `Framework.Run`:

```go
for _, runnable := range backends.Runnables() {
    framework.RegisterRunnable(runnable)
}

return framework.Run(ctx)
```

Do not call a runnable's `Start` method directly. Framework cancellation owns
its lifecycle.

---

## 7. Replace one capability

You can retain a Redis preset and replace selected behavior with neutral
backend options.

`OrchestrationBackends.With` returns a clone with the requested overrides; it
does not mutate the original composition. Runtime components should receive the
final validated clone.

### Replace one capability at Layer 1

Suppose an application already has a PostgreSQL `StateStore` but wants the
included Redis scheduling stack:

```go
owned, err := redisprovider.NewDefaultBackends(
    logger,
    redisprovider.WithDefaultBackendRoles(
        redisprovider.ClientRoleScheduling,
    ),
    redisprovider.WithDefaultBackendProviderOptions(
        redisprovider.WithNamespace("orders"),
    ),
    redisprovider.WithDefaultBackendOverrides(
        orchestration.WithWorkflowBackend(postgresWorkflow),
    ),
)
if err != nil {
    return err
}
defer owned.Close() // Closes only Redis clients created by this call.

backends := owned.Backends()
requirements, err := orchestration.RequirementsForFeatures(
    nil,
    orchestration.BackendFeatureWorkflow,
    orchestration.BackendFeatureSchedulerProducer,
)
if err != nil {
    return err
}
if err := backends.ValidateFor(requirements); err != nil {
    return err
}
```

The application that created `postgresWorkflow` still owns its PostgreSQL
connection. The Redis ownership handle does not know about or close override
resources.

`WithDefaultBackendRoles` selects only the Redis groups to construct. An
override may add a capability outside those groups, as PostgreSQL workflow
state does here. Validate the application's complete feature set after adding
such capabilities. Because the Redis workflow role is not selected,
`TRUVAG3_WORKFLOW_REDIS_DB` and `TRUVAG3_WORKFLOW_STATE_TTL` are ignored by this
constructor. Use Layer 2 when you need more detailed Redis client routing or
Layer 3 when the application owns the complete mixed composition.

### Override a lower-level Redis preset

The mixed-composition conformance test uses the provider constructor's override
list directly:

```go
backends, err := redisprovider.NewOrchestrationBackends(
    clients,
    redisOptions,
    orchestration.WithWorkflowBackend(postgresWorkflow),
    orchestration.WithScheduleBackend(postgresSchedules),
    orchestration.WithTaskBackend(postgresTasks),
    orchestration.WithCommandBackend(natsCommands),
    orchestration.WithTaskDispatcherBackend(natsTasks),
    orchestration.WithTaskConsumerBackend(natsTasks),
)
```

This exact pattern is exercised in
[`TestMixedProviderComposition`](https://github.com/truvaagents/truva-g3/blob/main/examples/orchestration-backend-portability/portability_test.go).
The test validates the combined feature set, checks that each getter returns
the selected adapter, and performs PostgreSQL, NATS command, and NATS task
round trips through the provider-neutral composition.

### Treat dependent checkpoint options as a unit

Checkpoint expiry has three independently validated parts:

1. `CheckpointPersistence` stores checkpoint state;
2. `ExpiredCheckpointSource` atomically claims expired checkpoints; and
3. a `core.Runnable` performs provider-neutral expiry processing.

Replacing persistence or the expired-item source invalidates the old processor
because it may still point to the replaced dependency. Rebuild the processor
against the new dependencies and place it after them:

```go
// Build the replacement processor against the final dependencies.
processor, err := orchestration.NewCheckpointExpiryProcessor(
    checkpointStore,
    expiredSource,
    callback,
    expiryConfig,
)
if err != nil {
    return err
}

// Install dependency options before the processor that uses them.
backends, err = backends.With(
    orchestration.WithCheckpointPersistence(checkpointStore),
    orchestration.WithCheckpointExpiry(expiredSource),
    orchestration.WithCheckpointExpiryProcessor(processor),
)
```

Putting the processor before either dependency option is an error. The Redis
preset applies caller overrides first and creates its default processor only
when expiry is enabled and no replacement processor remains.

---

## 8. Follow the mixed-provider reference implementation

The self-contained reference under
[`examples/orchestration-backend-portability`](https://github.com/truvaagents/truva-g3/tree/main/examples/orchestration-backend-portability)
proves that orchestration capabilities can be selected independently outside
the framework modules.

### Provider selection

| Capability | Provider in the reference | Consumers |
|---|---|---|
| Workflow execution state | PostgreSQL | API and worker |
| Schedule definitions | PostgreSQL | Scheduler |
| Scheduled-task state | PostgreSQL | Scheduler and scheduled executor |
| Checkpoint commands | NATS Core publish/subscribe (Pub/Sub) | Provider conformance and mixed-composition tests |
| Task dispatch and consumption | NATS JetStream | API, worker, scheduler, and scheduled executor |
| Scheduler leadership lock | Redis through the example-owned adapter | Scheduler replicas |
| Service registration and discovery | Redis | Scheduler, target agent, and scheduled executor |
| Checkpoint persistence | Included Redis preset | Mixed-composition test |

Discovery is listed separately because it is not an
`OrchestrationBackends` capability.

### Role-specific composition roots

[`backends.go`](https://github.com/truvaagents/truva-g3/blob/main/examples/orchestration-backend-portability/backends.go)
contains one builder per process role:

| Builder | Required neutral contracts |
|---|---|
| `buildAPIBackends` | `StateStore`, `TaskDispatcher` |
| `buildWorkerBackends` | `StateStore`, `TaskConsumer` |
| `buildSchedulerBackends` | `ScheduleStore`, `TaskStore`, `TaskDispatcher`, `DistributedLock` |
| `buildExecutorBackends` | `TaskStore`, `TaskConsumer`, plus separate discovery |

Every builder:

1. validates only that role's configuration;
2. opens only the connections the role uses;
3. constructs implementations of focused framework contracts;
4. installs them into `OrchestrationBackends`;
5. validates role-specific `BackendRequirements`; and
6. returns narrow dependencies plus an owner for its explicit adapter
   connections.

The application roles never receive `pgxpool.Pool`, `nats.Conn`, or a Redis
client. Provider SDK types remain in the composition root and matching internal
adapter packages.

### Resource ownership pattern

The example's `backendOwner` records closers for its PostgreSQL, NATS, and
Redis-lock adapter resources as they open and releases them in reverse order.
Each role builder also closes the partially built adapter graph on an error:

```go
owner := &backendOwner{}
built := false
defer func() {
    if !built {
        _ = owner.Close()
    }
}()

// Open resources and owner.add(...) each closer.

built = true
return roleBackends, owner, nil
```

The main process defers `closeBackends(owner)` and then starts the framework.
This keeps cleanup correct when construction fails after opening only some
resources or when normal framework shutdown completes.

### Two live workflows

The first workflow proves a durable store and task transport can be replaced
together:

```text
POST /tasks
    |
    v
API saves pending workflow execution in PostgreSQL
    |
    v
API dispatches a task to NATS JetStream
    |
    v
Worker consumes the task
    |
    v
Worker writes the completed execution to PostgreSQL
    |
    v
Worker acknowledges the NATS task
```

The worker persists the terminal state before acknowledgement. If PostgreSQL
is temporarily unavailable, it leaves the NATS claim unsettled so the task can
be delivered again. A redelivery observes an already completed execution and
acknowledges it without repeating the work. See
[`worker.go`](https://github.com/truvaagents/truva-g3/blob/main/examples/orchestration-backend-portability/worker.go).

The second workflow proves scheduler composition across three technologies:

```text
schedule request
    |
    v
PostgreSQL schedule state
    |
    +-- Redis leadership lock --> one scheduler promotes the due item
    |
    v
PostgreSQL deterministic task + NATS JetStream dispatch
    |
    v
Scheduled executor consumes task
    |
    +-- Redis discovery --> target agent
    |
    v
PostgreSQL terminal task result, then NATS acknowledgement
```

Two scheduler replicas are intentional. The leadership lock and deterministic
task identifier must still yield one task record.

### Code map

| What you want to study | Reference file |
|---|---|
| Process selection and framework lifecycle | [`main.go`](https://github.com/truvaagents/truva-g3/blob/main/examples/orchestration-backend-portability/main.go) |
| Provider selection, validation, ownership, and descriptors | [`backends.go`](https://github.com/truvaagents/truva-g3/blob/main/examples/orchestration-backend-portability/backends.go) |
| Narrow API dependencies and HTTP flow | [`agent.go`](https://github.com/truvaagents/truva-g3/blob/main/examples/orchestration-backend-portability/agent.go) |
| Runnable worker and persist-before-ack behavior | [`worker.go`](https://github.com/truvaagents/truva-g3/blob/main/examples/orchestration-backend-portability/worker.go) |
| Scheduler and scheduled-executor wiring | [`scheduler.go`](https://github.com/truvaagents/truva-g3/blob/main/examples/orchestration-backend-portability/scheduler.go) |
| PostgreSQL workflow adapter | [`internal/postgresadapter/workflow_store.go`](https://github.com/truvaagents/truva-g3/blob/main/examples/orchestration-backend-portability/internal/postgresadapter/workflow_store.go) |
| PostgreSQL schedule and task adapters | [`internal/postgresadapter/scheduler_stores.go`](https://github.com/truvaagents/truva-g3/blob/main/examples/orchestration-backend-portability/internal/postgresadapter/scheduler_stores.go) |
| NATS task transport | [`internal/natsadapter/task_transport.go`](https://github.com/truvaagents/truva-g3/blob/main/examples/orchestration-backend-portability/internal/natsadapter/task_transport.go) |
| NATS command delivery | [`internal/natsadapter/command_store.go`](https://github.com/truvaagents/truva-g3/blob/main/examples/orchestration-backend-portability/internal/natsadapter/command_store.go) |
| Redis owner-safe distributed lock | [`internal/redisadapter/lock.go`](https://github.com/truvaagents/truva-g3/blob/main/examples/orchestration-backend-portability/internal/redisadapter/lock.go) |
| Database schema | [`migrations/001_create_orchestration_tables.sql`](https://github.com/truvaagents/truva-g3/blob/main/examples/orchestration-backend-portability/migrations/001_create_orchestration_tables.sql) |
| Live conformance and mixed-provider test | [`portability_test.go`](https://github.com/truvaagents/truva-g3/blob/main/examples/orchestration-backend-portability/portability_test.go) |
| Enforced provider-import direction | [`dependency_direction_test.go`](https://github.com/truvaagents/truva-g3/blob/main/examples/orchestration-backend-portability/dependency_direction_test.go) |
| Direct provider inspection | [`scripts/`](https://github.com/truvaagents/truva-g3/tree/main/examples/orchestration-backend-portability/scripts) |

### Diagnostic descriptors are example-owned

The reference derives a `BackendDescriptor` after validation and exposes it on
the API's `/backends` endpoint. It records required capabilities, provider
labels, and concrete implementation types. This is useful startup and support
information, but it is not proof by itself and runtime logic must not branch on
it.

`BackendDescriptor` is currently implemented by the example, not exported by
the framework composition API. The framework design records automatic provider
identity reporting as later operational work.

---

## 9. Implement a provider adapter

Start with one contract whose semantics your technology can satisfy. Do not
begin by creating a provider-wide CRUD abstraction.

### Step 1: identify the owning contract

Contracts owned by `core` include scheduling stores, task stores, legacy task
queues, task dispatch/consumption, and distributed locks. Orchestration-owned
contracts include workflow state, debug stores, checkpoint persistence and
expiry claims, commands, and skill storage.

Read the interface, its sentinel errors, its existing tests, and the matching
conformance suite before writing an adapter. A **sentinel error** is an exported
Go error value that callers identify with `errors.Is`.

### Step 2: keep provider code at the composition boundary

An application-owned provider layout can follow the reference:

```text
my-agent/
├── main.go
├── backends.go                 # provider selection and connection ownership
└── internal/
    ├── postgresadapter/        # durable-state contracts
    ├── natsadapter/            # notification/work contracts
    └── redisadapter/           # coordination contract
```

Keep SDK imports inside `backends.go`, tests, and adapter packages. Runtime
handlers and workers should import framework contracts only.

### Step 3: make interface satisfaction visible

Use compile-time assertions beside adapter types:

```go
var _ orchestration.StateStore = (*WorkflowStore)(nil)
var _ core.ScheduleStore = (*ScheduleStore)(nil)
var _ core.TaskStore = (*TaskStore)(nil)
var _ orchestration.CommandStore = (*CommandStore)(nil)
var _ core.DistributedLock = (*DistributedLock)(nil)
var _ core.TaskDispatcher = (*TaskTransport)(nil)
var _ core.TaskConsumer = (*TaskTransport)(nil)
var _ core.TaskHandle = (*taskHandle)(nil)
```

The current reference asserts every adapter contract shown above. In
particular, `TaskTransport` deliberately implements both dispatch and
consumption, while its returned handle independently satisfies
`core.TaskHandle`.

### Step 4: translate provider behavior inside the adapter

An adapter owns the translation between provider behavior and the public
contract:

- map missing records and conflicts to documented framework sentinel errors;
- honor context cancellation and deadlines;
- preserve required fields during serialization round trips;
- implement namespace isolation;
- define acknowledgement, redelivery, and dead-letter behavior;
- enforce lock ownership and safe release;
- keep credentials, provider errors, SDK values, table names, subjects, and
  transactions out of returned domain objects.

Some providers are **eventually consistent**: they may return older data for a
short time after a write. The adapter must hide this temporary staleness from
callers, or the contract must state how much staleness it allows. A preset may
provide stronger behavior, but it must not weaken the framework contract.

### Step 5: keep schemas and credentials outside the framework module

The portability example owns its PostgreSQL schema in a migration file and
runs a Kubernetes migration Job before application replicas start. Pods do not
create or mutate schema during normal startup.

Follow the same boundary for:

- database migrations;
- stream, queue, or consumer provisioning;
- credentials and Kubernetes Secrets;
- provider endpoints and transport tuning; and
- direct provider health checks.

### Step 6: compose and inject narrow dependencies

Once the adapter passes conformance, install it with its typed option, validate
the consuming role, and pass only its getter result onward:

```go
backends, err := orchestration.NewOrchestrationBackends(
    orchestration.WithScheduleBackend(scheduleStore),
    orchestration.WithTaskBackend(taskStore),
    orchestration.WithTaskDispatcherBackend(dispatcher),
    orchestration.WithLockBackend(lock),
)
if err != nil {
    return err
}

requirements, err := orchestration.RequirementsForFeatures(
    nil,
    orchestration.BackendFeatureSchedulerProducer,
)
if err != nil {
    return err
}
if err := backends.ValidateFor(requirements); err != nil {
    return err
}
```

No scheduler implementation change is required.

---

## 10. Prove behavior with conformance tests

Interface satisfaction proves method shape. Conformance tests prove observable
behavior.

Backend-related suites live beside the module that owns each contract:

| Package | Relevant reusable suites |
|---|---|
| `core/conformance` | `TaskStore`, `ScheduleStore`, legacy `TaskQueue`, `TaskConsumer`, and task delivery profiles |
| `orchestration/backendconformance` | Workflow state, execution debug, LLM debug, checkpoints, commands, distributed locks, and skills |

Runtime packages do not import these testing packages. Provider test packages
do.

### Example: PostgreSQL workflow conformance

The reference opens two adapters over the same namespace to prove cross-instance
visibility:

```go
func TestPostgresWorkflowConformance(t *testing.T) {
    requireIntegration(t)
    pool := newPostgresPool(t)

    backendconformance.RunWorkflowStateConformance(
        t,
        func(t *testing.T) backendconformance.WorkflowFixture {
            namespace := fixtureNamespace(t, "postgres-workflow")
            first, err := postgresadapter.NewWorkflowStore(pool, namespace)
            if err != nil {
                t.Fatal(err)
            }
            second, err := postgresadapter.NewWorkflowStore(pool, namespace)
            if err != nil {
                t.Fatal(err)
            }
            return backendconformance.WorkflowFixture{
                First:  first,
                Second: second,
            }
        },
    )
}
```

The complete test also prepares schema and registers namespace cleanup. See
[`TestPostgresWorkflowConformance`](https://github.com/truvaagents/truva-g3/blob/main/examples/orchestration-backend-portability/portability_test.go).

### Example: declare task delivery semantics

NATS JetStream advertises at-least-once delivery in the reference. This means a
task should not be silently lost, but a consumer may receive the same task more
than once:

```go
conformance.RunTaskDeliveryProfileConformance(
    t,
    conformance.TaskDeliveryAtLeastOnce,
    func(t *testing.T) conformance.TaskDeliveryFixture {
        // Return independently constructed dispatcher and consumer adapters,
        // cleanup, dead-letter inspection, and abandoned-claim recovery.
    },
)
```

The profile runs the universal `TaskConsumer` checks plus redelivery,
dead-letter, and abandoned-claim assertions appropriate to the declared
delivery model.

### What the reference runs

[`portability_test.go`](https://github.com/truvaagents/truva-g3/blob/main/examples/orchestration-backend-portability/portability_test.go)
runs these live-provider suites:

- PostgreSQL workflow state;
- PostgreSQL schedule store;
- PostgreSQL task store;
- NATS Core command delivery;
- NATS JetStream at-least-once task delivery;
- Redis distributed lock; and
- mixed Redis/PostgreSQL/NATS composition.

Ordinary `GOWORK=off go test ./...` runs unit tests and skips tests guarded by
`PORTABILITY_INTEGRATION=1`. The example's `./setup.sh conformance-test` supplies
the live cluster environment.

### Know the boundary of the proof

The reference does not rerun conformance for every framework capability.
Execution debug, LLM debug, the legacy task queue, and skill contracts are not
used by its live application roles. Redis checkpoint persistence participates
only in the mixed-composition proof; its provider module owns its normal
conformance coverage.

Passing this example proves the public composition boundary and the tested
adapter behavior. It does not certify the PostgreSQL and NATS code for a
particular production workload, scale, availability target, or support policy.

---

## 11. Handle ownership and background work

Portability includes lifecycle semantics. A composition that leaks connections
or starts hidden workers is not safely replaceable.

### Connection ownership rules

| Construction path | Who closes resources? |
|---|---|
| `redisprovider.NewDefaultBackends` | `OwnedBackends.Close` closes only the Redis clients it created. |
| `redisprovider.NewOwnedClients` | `OwnedClients.Close` closes the clients it created. |
| `redisprovider.NewClientSet` with application clients | The application closes those clients. |
| Direct PostgreSQL/NATS/custom construction | The application-owned composition root closes connections and adapter resources. |
| A backend supplied through `WithDefaultBackendOverrides` | The code that created the override remains its owner. |

Close resources after the framework has stopped using them. Make cleanup
idempotent, so repeated cleanup calls have no additional effect. If
construction returns an error, close every resource that was already opened.

### Runnable rules

A provider lifecycle component implements `core.Runnable`:

```go
type Runnable interface {
    Start(context.Context) error
}
```

It should block until cancellation and return after cleaning up its own
background activity. The application registers it with the framework; the
provider does not call `Start` itself.

`WithRunnables` is additive across composition clones. Capability-owned
checkpoint expiry occupies its own typed slot and is also returned from
`Runnables()`.

### Choose what happens on failure

Do not let the provider decide what happens on failure. Choose the behavior the
domain needs. **Fail open** means allowing the primary operation to continue
when a non-critical dependency fails, while recording the failure. **Fail
closed** means stopping, retrying, or returning an error rather than reporting
success when correctness cannot be guaranteed.

| Concern | Expected default behavior |
|---|---|
| Execution and LLM debug recording | Fail open with observability |
| Required skill content | Fail closed before planning when no verified content is available |
| HITL checkpoint persistence | Fail closed; never claim an interrupt was saved when it was not |
| Schedule promotion | Treat the scheduler cycle as failed and retry later |
| Task claims and state transitions | Fail closed to avoid lost or duplicated work |
| Distributed lock | Fail closed for leader-only work |

Notification and transport adapters must document their delivery semantics
rather than assuming that every provider is durable or exactly once.

---

## 12. Plan a provider migration

Changing constructors is only the code portion of a backend change. Durable
state and in-flight work—requests or tasks that have already started or been
claimed—need a migration and traffic cutover designed for that domain.

### Select providers at startup

A process uses one composition for its lifetime. Do not mutate the backing
provider of an in-flight request, HITL checkpoint, scheduler tick, or task
claim. Change providers through a controlled deployment rollout.

### Classify the data first

| Data or behavior | Typical migration treatment |
|---|---|
| Debug records | Often allowed to expire according to the old store's retention policy |
| Skills and schedules | Explicit export/import or carefully bounded dual write |
| Pending HITL checkpoints | Keep resumable until completed or migrate with preserved identity and state |
| Queued or leased tasks | Allow current work to finish, then cut over transport ownership |
| Locks and transient notifications | Recreate through controlled startup; do not copy as durable records |

A global dual-write switch—writing every change to both providers—is unsafe
because stores, consumers, notifications, and locks have different semantics.
If mirroring is appropriate, implement it as a wrapper around that particular
contract. This kind of wrapper is often called a decorator.

### Recommended rollout

1. Run the target adapter's conformance suite.
2. Test expected scale, timeouts, failure recovery, and provider quotas.
3. Add shadow reads—comparison reads whose results do not control application
   behavior—or another non-authoritative comparison where the contract permits.
4. Migrate required durable state.
5. Prevent old producers from creating new work, sometimes called fencing the
   producers.
6. Drain or transfer consumers and leases, which are temporary ownership
   claims.
7. Deploy the new startup composition.
8. Verify application behavior and the provider's native state.
9. Remove the old path only after the rollback window and ownership boundary
   are understood.

Prefer roll-forward corrections: fix the new deployment and continue from its
current state. Switching a configuration value back does not reverse records
already migrated or tasks already consumed.

---

## 13. Run the local portability proof

The reference uses only open-source infrastructure and creates a dedicated Kind
cluster. It does not require an AI-provider key or another TruvaG3 example.

### Prerequisites

You need Go 1.27+, Docker or Podman, Kind, kubectl, curl, jq, and OpenSSL. The
setup helper detects a running Docker or Podman engine; set
`TRUVAG3_CONTAINER_RUNTIME` when you need to select one explicitly.

### Deploy and verify

From the repository root:

```bash
cd examples/orchestration-backend-portability
./setup.sh full-deploy
```

The script creates `truvag3-portability-$(whoami)` by default, installs Redis,
PostgreSQL, NATS with JetStream, ingress, the migration Job, and all five
application modes. It then runs provider conformance and both live workflows.
It uses its own generated kubeconfig and does not change the normal shared
example cluster.

Useful commands are:

| Command | What it does |
|---|---|
| `./setup.sh status` | Shows the dedicated cluster's resources and access URLs |
| `./setup.sh conformance-test` | Runs PostgreSQL, NATS, Redis-lock, and mixed-composition tests |
| `./setup.sh live-test` | Verifies API → NATS → worker → PostgreSQL |
| `./setup.sh scheduler-test` | Verifies PostgreSQL → NATS → Redis discovery → target → PostgreSQL |
| `./setup.sh failure-test` | Exercises worker restart, redelivery, target retry, and scheduler idempotency scenarios |
| `./setup.sh evidence` | Inspects PostgreSQL, NATS, Redis, Kubernetes, and migration state directly |
| `./setup.sh logs` | Shows application and verification logs |
| `./setup.sh forward` | Starts fallback port forwards when local ingress is unavailable |
| `./setup.sh cleanup` | Removes portability resources but retains the dedicated cluster and shared infrastructure |
| `./setup.sh cleanup-all` | Deletes only the dedicated portability Kind cluster |

Run `./setup.sh help` for the authoritative command list before changing
cluster state.

### Inspect the selected composition

```bash
curl -sS http://portability-agent.localhost:18080/backends | jq
```

This endpoint exposes the example-owned descriptor derived from the API role's
validated requirements. Treat it as diagnostics, then use independent evidence
to verify provider behavior:

```bash
./setup.sh evidence
```

The evidence scripts query PostgreSQL rows, NATS stream and consumer state,
Redis registration and lock state, migration results, and Kubernetes
configuration. SQL inspection queries live under
[`scripts/sql/`](https://github.com/truvaagents/truva-g3/tree/main/examples/orchestration-backend-portability/scripts/sql).

### Run local Go checks

The example intentionally lives outside the framework's `go.work`. From its
directory use:

```bash
GOWORK=off go test ./...
GOWORK=off go test -race ./...
GOWORK=off go vet ./...
GOWORK=off go build ./...
```

Live integration tests need the dedicated cluster and are normally run through
the setup helper.

---

## 14. Troubleshooting

| Symptom | Likely cause | What to do |
|---|---|---|
| `missing required backend capabilities` at startup | The requirements include a contract that was not composed | Read the complete missing list, install the matching typed backend option, or remove a feature that is not enabled for this process |
| A process connects to more Redis roles than expected | `NewDefaultBackends` was called without `WithDefaultBackendRoles`, or `NewClientSet` received a non-nil default client | Select explicit default roles, or use a nil default with `WithRoleClient` entries |
| An explicit orchestrator store was replaced | Wiring happened before explicit configuration, or another path mutated the config | Apply effective configuration first; `WireOrchestratorBackends` preserves non-nil explicit dependencies |
| Checkpoint processor becomes nil after an override | Checkpoint persistence or the expired source changed | Construct a new processor against the final dependencies and install it after both dependency options |
| `checkpoint expiry processor option must follow checkpoint dependency options` | The option list installed the processor too early | Order persistence, expired source, then processor |
| Connections leak after a failed builder | Cleanup is owned only by the successful return path | Add an error-path defer like the reference `backendOwner` pattern |
| A task disappears or runs twice during failure | Provider acknowledgement or redelivery semantics do not match the contract | Run the matching delivery profile conformance suite and verify persist/ack ordering |
| Two scheduler replicas promote the same schedule | Lock ownership, expiry, or deterministic task creation is incorrect | Run distributed-lock conformance and the reference scheduler/failure tests |
| A custom adapter compiles but fails in a second replica | Cross-instance visibility or namespace isolation is missing | Construct independent instances in the conformance fixture and test both shared and isolated namespaces |
| FreeLens cannot see the reference cluster | The dedicated kubeconfig was not imported | Import `examples/orchestration-backend-portability/.state/kubeconfig` after `full-deploy` |
| Local `.localhost` ingress does not work | Host DNS or port mapping differs | Run `./setup.sh forward` and use the ports reported by the script |
| You cannot import a reference adapter | It lives below the example's Go `internal` directory | Implement or copy the pattern into your own application's adapter package; do not depend on proof code as a library |

---

## 15. Production review checklist

Before advertising or deploying a new composition, confirm all of the
following.

### Contract and dependency boundaries

- [ ] Every runtime dependency is a narrow framework contract.
- [ ] Provider SDK types appear only in provider packages, composition roots,
      and provider tests.
- [ ] Runtime code does not branch on provider names or descriptors.
- [ ] Discovery selection remains an explicit, separate decision.
- [ ] Compile-time interface assertions cover every adapter contract.

### Construction and lifecycle

- [ ] Each process constructs only the capabilities it consumes.
- [ ] Startup validates the effective `BackendRequirements` before serving.
- [ ] Partial-construction failures close every opened resource.
- [ ] Ownership of injected and constructor-created clients is documented.
- [ ] Cleanup is idempotent and occurs after framework execution stops.
- [ ] Every background component is registered as a `core.Runnable`.

### Behavior and operations

- [ ] Every advertised capability passes its reusable conformance suite.
- [ ] Delivery, ordering, acknowledgement, retry, dead-letter, consistency, and
      expiry semantics are documented.
- [ ] Provider errors map to framework sentinel errors without leaking secrets.
- [ ] Namespace isolation and multi-instance visibility are tested.
- [ ] Health, logs, metrics, and traces identify capability outcomes without
      exposing credentials.
- [ ] Direct provider inspection can corroborate application claims.

### Migration and recovery

- [ ] Durable data has an explicit migration or retention decision.
- [ ] Pending checkpoints and queued or leased work have a drain/cutover plan.
- [ ] Producer and consumer ownership changes are ordered.
- [ ] The rollback window and roll-forward plan are documented.
- [ ] Load, outage, restart, cancellation, and partial-provider failures have
      been tested at the expected production scale.

---

## 16. Quick reference

### Neutral construction and validation

```go
orchestration.NewOrchestrationBackends(options...)
backends.With(overrides...)
orchestration.NewBackendRequirements(capabilities...)
orchestration.RequirementsForFeatures(config, features...)
backends.ValidateFor(requirements)
orchestration.WireOrchestratorBackends(config, backends)
backends.SkillAdministrationDependencies()
```

### Typed capability options

Use the typed option to install a contract, then use the matching getter to pass
that narrow contract to runtime code.

**State, checkpoints, and commands**

| Option | What it installs | Matching getter |
|---|---|---|
| `WithExecutionBackend` | Execution debug record storage | `Execution()` |
| `WithLLMDebugBackend` | LLM interaction debug storage | `LLMDebug()` |
| `WithCheckpointPersistence` | Checkpoint storage | `Checkpoints()` |
| `WithCheckpointExpiry` | Atomic expired-checkpoint source | `CheckpointExpiry()` |
| `WithCheckpointExpiryProcessor` | Checkpoint expiry background processor | `CheckpointExpiryProcessor()` |
| `WithCommandBackend` | HITL checkpoint command delivery | `Commands()` |
| `WithWorkflowBackend` | Workflow execution state | `Workflow()` |

The checkpoint expiry processor is also included in `Runnables()` so the
framework can start and stop it.

**Scheduling, work transport, and coordination**

| Option | What it installs | Matching getter |
|---|---|---|
| `WithScheduleBackend` | Schedule definitions | `Schedules()` |
| `WithTaskBackend` | Durable task state | `Tasks()` |
| `WithTaskQueueBackend` | Legacy asynchronous task queue | `TaskQueue()` |
| `WithTaskDispatcherBackend` | Task dispatch | `TaskDispatcher()` |
| `WithTaskConsumerBackend` | Task delivery and settlement | `TaskConsumer()` |
| `WithLockBackend` | Distributed coordination lock | `Lock()` |

**Skills and lifecycle**

| Option | What it installs | Matching getter |
|---|---|---|
| `WithSkillRegistryBackend` | Runtime skill registry | `SkillRegistry()` |
| `WithSkillRevisionReader` | Skill revision reads | `SkillRevisionReader()` |
| `WithSkillAdministrationStore` | Skill administration writes | `SkillAdministrationStore()` |
| `WithSkillRevisionDeletionStore` | Skill revision deletion | `SkillRevisionDeletionStore()` |
| `WithSkillAuditSink` | Skill audit records | `SkillAuditSink()` |
| `WithRunnables` | Additional provider lifecycle components | `Runnables()` |

### Included Redis paths

```go
redisprovider.NewDefaultBackends(logger, options...)
redisprovider.WithDefaultBackendRoles(roles...)
redisprovider.WithDefaultBackendClientConfig(options...)
redisprovider.WithDefaultBackendProviderOptions(options...)
redisprovider.WithDefaultBackendOverrides(overrides...)

redisprovider.DefaultClientConfig()
redisprovider.LoadClientConfigFromEnvironment(config, lookup)
redisprovider.ConfigureClientConfig(config, options...)
redisprovider.WithRoleDatabase(role, database)
redisprovider.NewOwnedClients(config, options...)
redisprovider.WithOwnedClientRoles(roles...)
redisprovider.NewClientSet(defaultClient, roleOptions...)
redisprovider.WithRoleClient(role, client)

redisprovider.NewOptions(options...)
redisprovider.LoadOptionsFromEnvironment(options, lookup)
redisprovider.ConfigureOptions(options, overrides...)
redisprovider.WithNamespace(namespace)
redisprovider.WithLogger(logger)
redisprovider.WithCheckpointExpiry(config, callback, options...)
redisprovider.NewOrchestrationBackends(clients, options, overrides...)
```

### Conformance packages

```go
import "github.com/truvaagents/truva-g3/core/conformance"
import "github.com/truvaagents/truva-g3/orchestration/backendconformance"
```

Choose the package that owns the contract. Do not import either from runtime
application code.

---

## 17. FAQ

### Do I have to use `OrchestrationBackends`?

No. Direct interface injection remains valid. The composition value is useful
when a process wants one inspectable, validated startup boundary and the ability
to apply typed overrides.

### Is Redis still the default?

Yes. `orchestration/redisprovider` is the included preset and can supply the
current orchestration capability set. Role selection lets a process construct
only the groups it needs.

### Must one provider implement every capability?

No. A preset can be hybrid, and an application can compose each capability
independently. The reference uses PostgreSQL, NATS, and Redis together.

### Why are task storage and task transport separate?

`TaskStore` owns durable task state. `TaskDispatcher` and `TaskConsumer` own
delivery and settlement. A database and a work queue have different semantics
and can use different technologies.

### Why does the scheduler need both a task store and a dispatcher?

The scheduler materializes deterministic task state and then sends work to a
consumer. Persisted state supports lifecycle and idempotency; transport supports
competing consumers and acknowledgement.

### Does replacing orchestration storage replace Redis discovery?

No. Discovery is constructed separately. You may retain Redis discovery while
moving orchestration state or task transport elsewhere, as the reference does.

### Are the reference PostgreSQL and NATS adapters ready for production?

They are proof-oriented, example-owned implementations. They demonstrate
contract satisfaction, mixed composition, and failure behavior locally. A team
adopting them must perform its own security, scale, availability, schema,
upgrade, and support review.

### Can I import the reference adapters into my agent?

No. They intentionally live under the reference example's `internal`
directory. Use them as readable patterns for an adapter owned by your
application or provider module.

### Does switching the provider migrate my data?

No. Composition changes startup wiring. Durable records, pending checkpoints,
schedules, and queued work need explicit domain-specific migration and cutover
steps.

### How do I know an adapter is portable rather than merely compiling?

Run the reusable suite for every contract it advertises, add multi-instance and
provider-integration coverage, test failure recovery, and verify native provider
state independently.

---

## 18. See also

- [Orchestration API Reference](../reference/API_REFERENCE.md#orchestration-backend-composition) — exact exported composition and Redis preset APIs.
- [Scheduled Tasks Guide](SCHEDULED_TASKS_GUIDE.md) — scheduler architecture, delivery semantics, and task-provider extension.
- [Async Orchestration Guide](ASYNC_ORCHESTRATION_GUIDE.md) — legacy task queue and task-store composition for HTTP 202 workflows.
- [Human-in-the-Loop User Guide](HUMAN_IN_THE_LOOP_USER_GUIDE.md) — checkpoint persistence, command delivery, and expiry lifecycle.
- [Agent Skills Guide](AGENT_SKILLS_GUIDE.md) — runtime and administrative skill storage requirements.
- [Framework Design Principles](https://github.com/truvaagents/truva-g3/blob/main/FRAMEWORK_DESIGN_PRINCIPLES.md) — layered composition, ownership, interface-first design, and invariant boundaries.
- [Orchestration Architecture](https://github.com/truvaagents/truva-g3/blob/main/orchestration/ARCHITECTURE.md) — authoritative module architecture and provider dependency direction.
- [Backend Portability Design](https://github.com/truvaagents/truva-g3/blob/main/orchestration/notes/ORCHESTRATION_BACKEND_PORTABILITY_DESIGN.md) — rationale, accepted design constraints, implementation status, and later migration work.
- [Portability Reference README](https://github.com/truvaagents/truva-g3/blob/main/examples/orchestration-backend-portability/README.md) — dedicated-cluster setup, live workflows, commands, and proof boundaries.
