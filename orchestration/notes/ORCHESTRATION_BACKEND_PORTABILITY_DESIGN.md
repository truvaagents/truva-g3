# Orchestration Backend Portability and Provider Composition

**Status:** Phases 0–4 delivered; the alternative-provider proof lives outside
the framework modules under `examples/orchestration-backend-portability`, while
production provider promotion and migration tooling remain later work

**Date:** 2026-07-31

**Last updated:** 2026-08-28

**Area:** `orchestration/` — durable state, messaging, task transport,
coordination, caching, and provider composition

**Related framework code:** `execution_store.go`, `llm_debug_store.go`,
`hitl_interfaces.go`, `workflow_state.go`, `scheduler.go`, `task_worker.go`,
`scheduler_backends.go`, and the skill contracts implemented from
`AGENT_SKILLS_ARCHITECTURE_PLAN.md`

> **Design decision:** Orchestration must be portable across backend
> technologies without reducing every concern to one generic key-value store.
> Each orchestration behavior continues to depend on its narrow domain
> interface. An inspectable `OrchestrationBackends` composition value makes those
> interfaces easy to construct, inspect, override, and pass through application
> composition. Redis remains the included default composition. A future AWS
> composition may use DynamoDB for durable state, SQS for task transport, and
> DynamoDB Streams or EventBridge for notifications. Provider selection and
> concrete clients remain outside runtime orchestration logic. The bounded
> composition foundation—typed options/getters, capability validation,
> lifecycle ownership,
> Redis preset, overrides, and conformance—is implemented before Skills V1 so
> skills do not add another one-off backend path. A proof-only PostgreSQL/NATS
> mixed composition now verifies the public extension boundary without making
> those adapters framework providers. Production provider promotion, migration
> tooling, and operational cutover remain independent later work.

## Table of contents

- [1. Why this note belongs under `orchestration/notes`](#1-why-this-note-belongs-under-orchestrationnotes)
- [2. Problem](#2-problem)
- [3. Goals](#3-goals)
- [4. Non-goals](#4-non-goals)
- [5. Current framework contracts](#5-current-framework-contracts)
- [6. Proposed composition model](#6-proposed-composition-model)
  - [6.1 Inspectable, source-compatible backend composition](#61-inspectable-source-compatible-backend-composition)
  - [6.2 Feature-aware validation](#62-feature-aware-validation)
  - [6.3 Provider presets](#63-provider-presets)
  - [6.4 Provider selection](#64-provider-selection)
- [7. Provider capability categories](#7-provider-capability-categories)
  - [7.1 Durable state](#71-durable-state)
  - [7.2 Notification and subscription](#72-notification-and-subscription)
  - [7.3 Work transport](#73-work-transport)
  - [7.4 Coordination](#74-coordination)
- [8. Reference compositions](#8-reference-compositions)
  - [8.1 Included Redis composition](#81-included-redis-composition)
  - [8.2 Future AWS composition](#82-future-aws-composition)
  - [8.3 Mixed custom composition](#83-mixed-custom-composition)
- [9. Contract refinements required before portability claims](#9-contract-refinements-required-before-portability-claims)
  - [9.1 Workflow state construction](#91-workflow-state-construction)
  - [9.2 Redis-specific neutral configuration](#92-redis-specific-neutral-configuration)
  - [9.3 Factory-owned Redis construction](#93-factory-owned-redis-construction)
  - [9.4 HITL checkpoint lifecycle](#94-hitl-checkpoint-lifecycle)
  - [9.5 Low-level execution `StorageProvider`](#95-low-level-execution-storageprovider)
  - [9.6 Background work](#96-background-work)
- [10. Conformance testing](#10-conformance-testing)
  - [10.1 Common requirements](#101-common-requirements)
  - [10.2 Contract-specific requirements](#102-contract-specific-requirements)
- [11. Failure and consistency policy](#11-failure-and-consistency-policy)
- [12. Backend identity and observability](#12-backend-identity-and-observability)
- [13. Switching and migration semantics](#13-switching-and-migration-semantics)
  - [13.1 Startup pinning](#131-startup-pinning)
  - [13.2 Data migration](#132-data-migration)
  - [13.3 Rollout](#133-rollout)
- [14. Package and dependency direction](#14-package-and-dependency-direction)
- [15. Incremental implementation plan](#15-incremental-implementation-plan)
  - [Phase 0 — Contract audit and characterization](#phase-0--contract-audit-and-characterization)
  - [Phase 1 — Close abstraction leaks](#phase-1--close-abstraction-leaks)
  - [Phase 2 — Source-compatible backend composition](#phase-2--source-compatible-backend-composition)
  - [Phase 3 — Redis preset and parity](#phase-3--redis-preset-and-parity)
  - [Phase 4 — Alternative provider proof](#phase-4--alternative-provider-proof)
  - [Phase 5 — Migration tooling and operational guidance](#phase-5--migration-tooling-and-operational-guidance)
- [16. Acceptance criteria](#16-acceptance-criteria)
- [17. Decisions retained for later implementation](#17-decisions-retained-for-later-implementation)
- [18. References](#18-references)

---

## 1. Why this note belongs under `orchestration/notes`

This design belongs under `orchestration/notes`, not `core/notes`, because the
problem is the composition of orchestration-owned behaviors:

- execution and LLM-debug persistence;
- HITL checkpointing and command delivery;
- workflow state;
- schedules, task state, dispatch, and consumption;
- distributed coordination used by orchestration components;
- skill registry persistence and change notification.

`core` defines some shared contracts used by those behaviors, including
`ScheduleStore`, `TaskStore`, `TaskQueue`, `TaskDispatcher`, `TaskConsumer`,
`DistributedLock`, and `Runnable`. This activity may refine those contracts only
when a provider-neutral behavior is genuinely shared across modules. It must not
move orchestration-specific configuration, provider selection, or backend
factories into `core`.

Service discovery is a separate concern. `core.Discovery` remains a
service/capability registry and is not a source of datastore clients or general
persistence operations.

## 2. Problem

The framework already has useful provider-neutral interfaces, but the complete
backend experience is inconsistent:

- some components accept domain interfaces directly;
- some accept a concrete Redis client;
- some construct Redis internally from environment variables;
- some provider-neutral configuration structs contain Redis-specific fields;
- one legacy workflow-state constructor accepts `core.Discovery` but does not
  obtain storage through it;
- no single inspectable composition object shows which backend satisfies each
  required behavior.

Consequently, replacing Redis is possible component by component, but it is not
yet an easy, auditable deployment decision. A team adopting DynamoDB must find
and construct every persistence, queue, notification, and coordination adapter
independently.

The problem is not the absence of one universal datastore interface. Durable
records, subscriptions, work queues, and distributed locks have different
correctness requirements. Hiding them behind CRUD would make the framework less
portable by forcing provider-specific behavior into consumers.

## 3. Goals

The refactor must:

- preserve narrow domain interfaces as the runtime dependency boundary;
- make the full backend composition visible in one interface-typed value;
- retain Redis as the simple included default;
- allow a provider or platform composition to use more than one technology;
- permit individual backend overrides without abandoning a provider preset;
- keep provider names, clients, tables, keys, queues, streams, SDK types, and
  transactions out of planner, executor, HITL, scheduler, and skill logic;
- make startup validation aware of enabled framework features rather than
  requiring every backend in every process;
- provide conformance suites for behavior, consistency, error, and lifecycle
  semantics;
- support deliberate migration and rollback of infrastructure configuration
  without changing in-flight request semantics;
- preserve the current module dependency direction.

## 4. Non-goals

This activity does not:

- make every backend implement one generic `DataStore` or `StorageProvider`;
- require one physical database for every orchestration concern;
- make `core.Discovery` expose Redis or other provider clients;
- change the discovery provider when the orchestration backend changes;
- require DynamoDB, SQS, EventBridge, or any other cloud service;
- dynamically switch a process to another backend in the middle of a request;
- force applications to use a bundle instead of direct interface injection;
- require an alternate provider or migration tooling before Skills V1;
- move orchestration ownership into `core`;
- preserve provider-specific key, table, queue, or transaction designs as public
  compatibility contracts.

## 5. Current framework contracts

The existing contracts are the starting point, not something to replace with a
larger interface.

| Concern | Runtime contract | Current included implementation pattern |
|---|---|---|
| Execution records | `ExecutionStore`; optional `ConversationExecutionLister` | Provider-backed implementation plus direct Redis default |
| LLM debug records | `LLMDebugStore` | Injected store or direct Redis default |
| HITL state | Current `CheckpointStore`; prerequisite adds narrow `CheckpointPersistence` while retaining a compatibility interface | Redis implementation constructed independently and currently combines expiry lifecycle |
| HITL command delivery | `CommandStore` | Redis Pub/Sub implementation |
| Workflow state | `StateStore` | Legacy Redis implementation |
| Schedule definitions | `core.ScheduleStore` | Redis and in-memory implementations |
| Task state | `core.TaskStore` | Redis implementation |
| Legacy task queue | `core.TaskQueue` | Redis queue and in-flight settlement lists |
| Scheduled dispatch | `core.TaskDispatcher` | Redis list/stream and in-memory implementations |
| Scheduled consumption | `core.TaskConsumer` | Redis list/stream and in-memory implementations |
| Distributed coordination | `core.DistributedLock` | Redis and no-op/in-memory implementations |
| Skills | `SkillRegistry`, `SkillContentStore`, `SkillAdministration`, and `SkillAuditSink` | Redis default; semantic in-memory reference remains test-only inside the conformance suite |

Two lessons follow:

1. The stable abstraction should be each domain contract, because it expresses
   the behavior orchestration actually needs.
2. The missing layer is composition and provider conformance, not a universal
   CRUD API.

## 6. Proposed composition model

### 6.1 Inspectable, source-compatible backend composition

Introduce an interface-typed composition value whose aggregate fields remain
private. Public construction options and read-only getters keep every component
inspectable and replaceable without making the aggregate's field count a source-
compatibility contract:

```go
type BackendCapability string

type OrchestrationBackends struct {
    // Package-private fields, each typed as a public framework interface.
}

type OrchestrationBackendOption interface {
    applyBackend(*OrchestrationBackends) error
}

func NewOrchestrationBackends(
    options ...OrchestrationBackendOption,
) (*OrchestrationBackends, error)

func WithExecutionBackend(ExecutionStore) OrchestrationBackendOption
func WithCheckpointPersistence(CheckpointPersistence) OrchestrationBackendOption
func WithCheckpointExpiry(ExpiredCheckpointSource) OrchestrationBackendOption
func WithCheckpointExpiryProcessor(core.Runnable) OrchestrationBackendOption
func WithRunnables(...core.Runnable) OrchestrationBackendOption
// One typed option and read-only getter exists for every capability.

func (b *OrchestrationBackends) Execution() ExecutionStore
func (b *OrchestrationBackends) Runnables() []core.Runnable
func (b *OrchestrationBackends) With(
    overrides ...OrchestrationBackendOption,
) (*OrchestrationBackends, error)

func WireOrchestratorBackends(
    config *OrchestratorConfig,
    backends *OrchestrationBackends,
) error
func (b *OrchestrationBackends) SkillAdministrationDependencies() (
    SkillAdminHandlerDependencies,
    error,
)
```

The pre-Skills foundation shipped options, getters, and capability constants
for existing contracts. Skills V1 then added typed skill options/getters and a
source-compatible `BackendSkills` runtime-registry capability. The control-plane
reader, publication, deletion, and audit contracts each have their own
capability constant. The addition neither changes an exported struct literal
nor widens an interface that applications must implement.

The value is deliberately inspectable and replaceable:

- every option accepts and every getter returns a public framework interface;
- private aggregate fields prevent external positional literals from making
  future additive capabilities a breaking change;
- no runtime component receives the entire bundle when it needs only one field;
- applications can inspect any capability and replace one through `With`, which
  returns a defensive clone;
- a provider may leave unused capabilities nil;
- constructors do not start workers or background goroutines;
- application/framework lifecycle continues to use `core.Runnable`.

`WithRunnables` is additive across `With` calls. Capability-owned lifecycle
components use their own typed slot, so adding an unrelated reaper cannot
silently replace checkpoint expiry. Replacing checkpoint persistence or its
expired-source contract invalidates a previously composed expiry processor. In
one option list, a processor supplied before either dependency is rejected;
dependency options must come first. The Redis preset applies all caller
overrides before constructing any missing default processor, so an explicitly
supplied replacement wins without silent substitution.

The prerequisite foundation adds `CheckpointPersistence` as the persistence-
only subset of the existing public `CheckpointStore`. Compatibility adapters
retain the latter while expiry scanning becomes a separately exposed runnable.

The composition value does not become a god object inside `AIOrchestrator`. It
exists at the composition boundary and is unpacked into the existing narrow
constructors and options. `WireOrchestratorBackends` fills only enabled,
otherwise-unset orchestrator dependencies, while
`SkillAdministrationDependencies` validates and unpacks the full management
contract. Explicit dependencies retain precedence.

Applications register each returned runnable from the cloned `Runnables()`
slice with `Framework.RegisterRunnable` before `Framework.Run`; they do not call
`Start` directly. Runnable order is not a startup-dependency contract. A
provider that requires ordered startup encapsulates it within one runnable.

### 6.2 Feature-aware validation

Not every process requires every capability. Validation must be based on the
enabled feature set:

```go
type BackendRequirements struct {
    required map[BackendCapability]struct{}
}

func NewBackendRequirements(
    capabilities ...BackendCapability,
) (BackendRequirements, error)

func (b *OrchestrationBackends) ValidateFor(
    requirements BackendRequirements,
) error
```

Each shipped capability has a named `BackendCapability` constant backed by one
predicate registry. Unknown or duplicate constants are rejected; ordering is
irrelevant. `BackendSkills` remains the source-compatible runtime-registry name;
skill control-plane contracts validate independently.
Examples:

- an ordinary chat agent requires only the skill registry, while the complete
  administration feature additionally requires revision reads, publication,
  deletion, and audit;
- a scheduler producer sets schedules, tasks, dispatcher, and lock;
- a scheduled consumer sets consumer and tasks;
- local HITL may set checkpoints only; cross-instance wake-up also sets
  commands;
- disabled debug storage requires neither debug backend.

Canonical checkpoint expiry requires three independently validated
capabilities: checkpoint persistence, an expired-checkpoint source, and the
application-owned expiry processor runnable. Possessing a source without a
registered processor does not satisfy the feature.

Validation is fail-fast for required capabilities and silent for unused nil
values. A pure `RequirementsForFeatures` helper derives the explicit capability
set from effective configuration. Individual consumers retain their existing
fail-open/fail-closed policy.

### 6.3 Provider presets

Providers expose constructors that return the neutral composition value:

The included Redis provider exposes all three framework composition layers.
Layer 1 owns its clients, resolves documented environment configuration,
applies explicit code options last, validates every capability created for the
selected roles, and returns an explicit cleanup handle:

```go
ownedBackends, err := redisprovider.NewDefaultBackends(
    logger,
    redisprovider.WithDefaultBackendRoles(redisprovider.ClientRoleSkills),
)
if err != nil {
    return err
}
defer ownedBackends.Close()

skillRegistry := ownedBackends.Backends().SkillRegistry()
```

`WithDefaultBackendClientConfig`, `WithDefaultBackendProviderOptions`, and
`WithDefaultBackendOverrides` expose the existing Layer-2 configuration and
per-capability replacement seams without introducing a parallel implementation.
The returned `OwnedBackends` closes only clients created by this convenience
path; it does not start runnables or pass the aggregate into runtime behavior.

Layer 2 keeps configuration loading, ownership, and provider construction
separately callable for applications that need to control those boundaries.
Layer 3 remains direct construction of individual domain adapters and the
neutral composition value.

Growth-prone provider inputs follow the same compatibility rule as the neutral
composition value. The Redis preset uses a private-state `ClientSet` constructed
from an optional explicit default client plus typed `ClientRole` overrides, and a private-
state `Options` value constructed through provider option functions. A later
capability adds a role/option constant rather than a public client, database, or
settings field.

```go
redisClients, err := redisprovider.NewClientSet(redisClient)
if err != nil {
    return err
}
redisOptions, err := redisprovider.NewOptions(
    redisprovider.WithNamespace("truvag3"),
    redisprovider.WithLLMDebugRetention(24*time.Hour, 7*24*time.Hour),
    redisprovider.WithCheckpointTTL(24*time.Hour),
)
if err != nil {
    return err
}
redisBackends, err := redisprovider.NewOrchestrationBackends(
    redisClients,
    redisOptions,
)
```

```go
awsBackends, err := awsprovider.NewOrchestrationBackends(
    dynamoClient,
    sqsClient,
    eventClient,
    awsprovider.WithEnvironment("production"),
)
```

An application may keep a preset while overriding one behavior:

```go
redisClients, err := redisprovider.NewClientSet(redisClient)
if err != nil {
    return err
}
redisOptions, err := redisprovider.NewOptions()
if err != nil {
    return err
}
backends, err := redisprovider.NewOrchestrationBackends(
    redisClients,
    redisOptions,
)
if err != nil {
    return err
}

backends, err = backends.With(
    orchestration.WithExecutionBackend(customExecutionStore),
    orchestration.WithTaskConsumerBackend(customTaskConsumer),
)
if err != nil {
    return err
}
```

This is the framework's “no cliff” rule applied to infrastructure composition.

### 6.4 Provider selection

Provider selection belongs in application/integration composition, not in
runtime orchestration code. A deployment may use code, configuration, or an
inspectable preset factory, but the selected constructor returns the same
`OrchestrationBackends` type.

The base orchestration package must never contain logic such as:

```go
switch config.BackendProvider {
case "redis":
    // Redis behavior in planner/executor
case "aws":
    // AWS behavior in planner/executor
}
```

If a later convenience package supports an environment-selected provider, that
package explicitly imports the supported provider presets and performs only
composition. Explicit programmatic configuration must override environment
defaults. The provider factory must not leak into `AIOrchestrator` or `core`.

## 7. Provider capability categories

A provider preset may combine technologies because orchestration needs several
different categories of behavior.

### 7.1 Durable state

Durable stores retain records across process restarts and provide their required
query and concurrency semantics:

- executions and conversation correlation;
- LLM debug records;
- HITL checkpoints;
- workflow executions;
- schedules and task state;
- skill catalog, immutable revisions, bindings, and tombstones.

Suitable technologies include Redis with persistence, DynamoDB, PostgreSQL, and
other databases that can satisfy the relevant domain contract.

### 7.2 Notification and subscription

Notification backends wake waiting consumers or invalidate mutable caches:

- HITL command delivery;
- skill update/deletion notifications;
- optional cache invalidation events.

Suitable implementations include Redis Pub/Sub or Streams, DynamoDB Streams,
EventBridge, Kafka, NATS, and database polling. Notifications are not assumed to
be durable unless the interface explicitly requires durable delivery.

### 7.3 Work transport

Task dispatch and consumption require more than record storage:

- competing-consumer semantics;
- claim/lease behavior;
- acknowledgement and negative acknowledgement;
- redelivery and dead-letter handling;
- ordering guarantees defined by the selected implementation.

Suitable technologies include Redis lists/Streams, SQS, NATS JetStream, Kafka,
and database-backed queues.

### 7.4 Coordination

Coordination includes distributed locks, leases, idempotency claims, and leader
election. Implementations must define ownership, expiry, and release semantics
without exposing provider transactions to consumers.

## 8. Reference compositions

### 8.1 Included Redis composition

Redis remains the included default because the framework already uses it and it
can satisfy the complete initial contract:

```text
Redis durable records       -> execution, LLM debug, HITL, workflow, schedules,
                               tasks, and skills
Redis Pub/Sub or Streams    -> HITL and skill change notifications
Redis lists or Streams      -> task dispatch and consumption
Redis conditional commands -> locks and idempotency
```

The preset may use one physical Redis deployment with provider-private logical
isolation. The public composition API does not expose database numbers, key prefixes,
Lua scripts, transactions, or Pub/Sub channel names.

Discovery may use the same Redis deployment, but discovery is constructed and
injected independently. The orchestration preset does not obtain its client from
`core.Discovery`.

### 8.2 Future AWS composition

A natural AWS composition is:

```text
DynamoDB                  -> durable orchestration state
SQS                       -> task dispatch and consumption
DynamoDB Streams or
EventBridge               -> HITL and skill change notifications
DynamoDB conditional writes
                           -> locks, leases, idempotency, and atomic state changes
```

The DynamoDB portion can implement:

- `ExecutionStore` and conversation lookup;
- `LLMDebugStore`;
- `CheckpointPersistence` and, when expiry processing is enabled,
  `ExpiredCheckpointSource`;
- `StateStore`;
- `core.ScheduleStore`;
- `core.TaskStore`;
- `core.DistributedLock`;
- the skill registry, administration, deletion, binding, and source-version
  contracts.

SQS is a better natural fit than DynamoDB polling for `TaskDispatcher` and
`TaskConsumer`. Streams/EventBridge can implement provider-specific event
delivery behind `CommandStore` and `SkillChangeSource`.

This composition is illustrative, not a dependency or commitment. Another AWS
provider may choose different services while satisfying the same contracts.

### 8.3 Mixed custom composition

Provider neutrality also permits a mixed deployment:

```text
PostgreSQL -> durable records
NATS       -> task transport and notifications
Redis      -> distributed locks and cache
```

The framework does not require every capability in `OrchestrationBackends` to come
from the same constructor or vendor.

## 9. Contract refinements required before portability claims

This activity should audit and correct framework contracts that currently leak
Redis or combine unrelated responsibilities.

### 9.1 Workflow state construction

Deprecate the constructor shape:

```go
NewRedisStateStore(discovery core.Discovery)
```

`StateStore` is already the correct consumer contract. Concrete state stores
must receive their own provider client/configuration. `core.Discovery` must not
be accepted as an implied datastore.

### 9.2 Redis-specific neutral configuration

Move fields such as Redis database numbers out of neutral orchestration feature
configuration. Separate:

- provider-neutral behavior: enabled state, retention intent, query bounds,
  failure policy;
- Redis adapter configuration: URL/client, database, key prefix, connection
  pooling, scripts, and notification channels;
- AWS adapter configuration: table names, indexes, queues, streams, consistency,
  and client options.

The Redis preset owns new retention and namespace configuration. Legacy
`LLMDebugConfig.RedisDB`, `ExecutionStoreConfig.KeyPrefix`, and
`HITLConfig.KeyPrefix` fields remain deprecated only so existing compatibility
factory callers continue to compile; new composition code must not depend on
them.

### 9.3 Factory-owned Redis construction

Explicitly injected stores must always win. The long-term composition should
construct the Redis default in the Redis preset rather than inside planner or
orchestrator factories. Compatibility wrappers may preserve current
zero-configuration behavior during migration, but they should delegate to the
same preset and remain visibly replaceable.

### 9.4 HITL checkpoint lifecycle

`CheckpointStore` currently combines persistence with expiry processing and
lifecycle methods. Refine it toward narrow capabilities:

```go
type CheckpointPersistence interface {
    SaveCheckpoint(...)
    LoadCheckpoint(...)
    UpdateCheckpointStatus(...)
    ListPendingCheckpoints(...)
    DeleteCheckpoint(...)
}

type ExpiredCheckpointClaimRequest struct {
    Before time.Time
    Limit  int
    Owner  string
    Lease  time.Duration
}

type ExpiredCheckpointSource interface {
    ClaimExpiredCheckpoints(
        context.Context,
        ExpiredCheckpointClaimRequest,
    ) ([]*ExecutionCheckpoint, error)
    ReleaseExpiredCheckpointClaim(
        context.Context,
        checkpointID string,
        owner string,
    ) error
}
```

A provider-neutral expiry processor consumes `ExpiredCheckpointSource` and
`CheckpointPersistence` and implements only the existing `core.Runnable`
lifecycle contract (`Start(context.Context) error`; cancellation is shutdown).
Do not introduce a second exported lifecycle interface with `Stop` or
`Shutdown` methods. `ClaimExpiredCheckpoints` must atomically discover and
claim eligible checkpoints for `Owner` until the lease expires; only that owner
may release the claim. Providers remain free to use TTL events, streams,
queries, or polling internally. Ordinary checkpoint persistence does not
require this source; composition validation requires it only when canonical
expiry processing is enabled, together with the processor runnable itself.

The existing broad `CheckpointStore` lifecycle methods remain behind a
deprecated compatibility adapter during migration. The included Redis adapter
must expose its existing owner-checked claim behavior through the same narrow
contract rather than giving the canonical processor provider-specific types.

### 9.5 Low-level execution `StorageProvider`

`ExecutionStore` is the stable domain contract. The existing low-level
`StorageProvider` can remain a convenience for stores that fit its operations,
but a provider is always allowed to implement `ExecutionStore` directly.
Provider portability must not require DynamoDB, SQL, or object stores to emulate
Redis sorted-set primitives when a native implementation can satisfy the
domain behavior more directly.

### 9.6 Background work

Provider adapters must not start unmanaged goroutines. Reapers, pollers, stream
consumers, expiry processors, and cache invalidators implement `core.Runnable`
and are registered by composition. The composition value exposes companion
runnables without starting them:

```go
backends, err := awsprovider.NewOrchestrationBackends(...)
for _, runnable := range backends.Runnables() {
    framework.RegisterRunnable(runnable)
}
```

## 10. Conformance testing

There is no useful universal datastore conformance test. Each domain interface
needs its own reusable suite.

### 10.1 Common requirements

Every suite verifies:

- documented not-found and conflict errors;
- concurrency and idempotency semantics;
- ordering and pagination behavior;
- retention/expiry intent and provider tolerances;
- context cancellation and timeout handling;
- multi-instance visibility;
- serialization compatibility at the domain boundary;
- lifecycle ownership and clean shutdown;
- absence of provider types in returned domain objects.

### 10.2 Contract-specific requirements

- `ExecutionStore`: trace lookup, recent ordering, metadata updates,
  conversation lookup, and retention extension.
- `LLMDebugStore`: append/update behavior, recent ordering, TTL extension, and
  concurrent interaction recording.
- `CheckpointPersistence`: state transitions, pending queries, resume
  consistency, and concurrent status-update/resume attempts.
- `ExpiredCheckpointSource`: atomic claim, owner-only release, lease expiry,
  retry after abandoned claims, cancellation, and multi-instance exclusion.
- `CommandStore`: subscription cancellation, delivery semantics, duplicate
  handling, and cross-instance visibility.
- `ScheduleStore`: due-time queries, update conflicts, enable/disable behavior,
  and concurrent schedulers.
- `TaskStore`: deterministic IDs, idempotent create, state transitions, and
  retention.
- legacy `TaskQueue`: FIFO round trips, cross-instance acknowledgement,
  rejection/redelivery, namespace isolation, and cancellation.
- `TaskDispatcher`/`TaskConsumer`: claim, acknowledgement, negative
  acknowledgement, redelivery, crash recovery, and dead-letter behavior.
- `DistributedLock`: ownership, expiry, renewal if supported, and safe release.
- skills: immutable versions, atomic publication, source tokens, guarded
  deletion, tombstones, namespace isolation, and on-demand resource reads.

The included Redis implementation and every advertised provider preset must
pass the suites for the capabilities it exposes.

## 11. Failure and consistency policy

Backend portability requires explicit domain policies instead of relying on a
provider's accidental defaults.

| Concern | Default failure posture |
|---|---|
| Execution/LLM debug recording | Fail-open with observability |
| Optional skill | Fail-open according to binding policy and verified cache |
| Required skill | Fail-closed before planning when no verified content exists |
| HITL checkpoint persistence | Fail-closed; do not claim an interrupt was saved |
| HITL command delivery | Follow declared delivery semantics |
| Schedule promotion | Fail-closed for the tick; retry later |
| Task claim/state transition | Fail-closed to avoid duplicate or lost work |
| Distributed lock | Fail-closed for leader-only work |

Provider presets may offer stronger consistency, but they must not weaken the
contract observed by orchestration. Eventual consistency must be accounted for
inside the adapter or documented as an allowed domain tolerance.

## 12. Backend identity and observability

Observability may report a provider-neutral backend identity for diagnostics:

```go
type BackendDescriptor struct {
    Name         string
    Capabilities []string
    Instance     string // sanitized logical identity, never credentials
}
```

Runtime logic must not branch on this descriptor. It is for startup logs,
health, metrics, and support diagnostics only.

**Implementation status:** `BackendDescriptor` and automatic startup reporting
remain Phase 5 work. Phases 0–3 provide inspectable typed getters and validated
capabilities, but do not yet publish a sanitized provider identity through
health or telemetry.

Recommended telemetry includes:

- backend operation, capability, outcome, and latency;
- conflict, throttling, timeout, and unavailable classifications;
- queue claim/redelivery/dead-letter counts;
- notification lag and reconnects;
- cache source-version age;
- provider preset and enabled capability list, without secrets.

## 13. Switching and migration semantics

“Easy to switch” means composition and domain behavior remain stable. It does
not mean durable state moves automatically or that a backend changes during an
in-flight execution.

### 13.1 Startup pinning

A process selects one backend composition during startup. All operations for an
individual request, HITL checkpoint, schedule tick, or task claim use that
composition for the process lifetime. Provider changes occur through a
controlled deployment rollout.

### 13.2 Data migration

Different stores need different migration policies:

- debug data may be allowed to age out in the old store;
- skills and schedules require explicit export/import or dual-read migration;
- pending HITL checkpoints must remain resumable until completed or migrated;
- queued or leased tasks require drain/cutover procedures;
- locks and transient notifications are not copied as durable records.

Migration adapters are domain-specific decorators, not one global dual-write
switch. For example, a mirrored `ExecutionStore` may be safe while mirroring a
task consumer or distributed lock is not.

### 13.3 Rollout

A provider change should support:

1. conformance and load testing;
2. shadow reads or non-authoritative comparison where safe;
3. bounded dual writes only for contracts that define them safely;
4. migration of required durable state;
5. controlled producer cutover;
6. consumer drain and cutover;
7. verification and removal of the old path.

The framework favors roll-forward corrections. Infrastructure rollback remains
an operational escape hatch, but the framework must not pretend that switching
a configuration value reverses already migrated or consumed state.

## 14. Package and dependency direction

Target dependency direction:

```text
application / integration composition
        |                 |
        v                 v
orchestration provider   orchestration provider
preset: Redis            preset: AWS/custom
        \                 /
         v               v
        orchestration domain interfaces
                   |
                   v
                 core
```

Rules:

- root orchestration logic imports no provider preset;
- provider presets import orchestration/core contracts and their SDKs;
- `core` imports neither orchestration nor provider presets;
- provider clients never appear in domain interfaces;
- provider-specific options stay with the provider constructor;
- in-memory implementations remain available for conformance tests and
  single-process development.

The Redis preset accepts an inspectable `ClientSet` with private aggregate
state, not one mandatory shared client, because existing orchestration concerns
use different logical-database defaults. A constructor-supplied default client
is the explicit fallback; typed role options preserve existing isolation. The
owned-client helper reproduces current URL/database defaults and accepts
`WithOwnedClientRoles` to construct only the roles a process needs. The preset
never closes application-supplied clients. The historical HITL, workflow, and
scheduling database assignments are parity defaults rather than recommendations
for new deployments. Later roles and logical databases are added through
constants and typed options, not public struct fields.

Whether concrete presets are subpackages in the existing module or separate Go
modules should be decided during implementation based on dependency weight and
release compatibility. That packaging decision must not change the domain
interfaces or composition model.

## 15. Incremental implementation plan

Phases 0-4 are delivered by the bounded pre-Skills foundation, the ground-truth
completion work, and the executable alternative-provider proof recorded in this
repository. The formerly cited standalone implementation-plan file is not part
of the repository; this section and the executable conformance suites are the
maintained plan and acceptance source. Phase 5 remains independent follow-up
work and does not block Skills V1.

### Phase 0 — Contract audit and characterization

- Inventory framework-owned backend interfaces and concrete Redis construction.
- Characterize current behavior with reusable tests before changing wiring.
- Record required, optional, fail-open, and fail-closed capabilities.
- Confirm that discovery is not used as storage by any new path.

**Exit:** Existing behavior is captured without changing providers.

### Phase 1 — Close abstraction leaks

- Deprecate `NewRedisStateStore(discovery core.Discovery)`.
- Separate Redis adapter configuration from neutral feature configuration.
- Split HITL persistence from expiry lifecycle.
- Ensure explicit injected stores always take precedence over Redis defaults.
- Convert provider background work to `core.Runnable` where needed.

**Exit:** Every orchestration consumer depends on a domain interface rather than
provider construction.

### Phase 2 — Source-compatible backend composition

- Add private-field `OrchestrationBackends`, typed options/getters,
  `BackendCapability`, and `BackendRequirements`.
- Add feature-aware validation.
- Keep feature requirements closed over real constructor dependencies: the
  scheduler producer requires schedules, tasks, dispatcher, and lock; checkpoint
  expiry requires persistence, source, and processor.
- Add direct wiring helpers that unpack the bundle into existing constructors.
- Exercise the preset from in-tree skill-enabled agents and the skills
  administration host rather than constructing `SkillStore` directly.
- Preserve individual dependency-injection paths.

**Exit:** One visible composition object describes the backend capabilities of a
process without becoming a runtime god object.

### Phase 3 — Redis preset and parity

- Build the included Redis preset from existing implementations.
- Provide a Layer-1 owned Redis composition that delegates to the separately
  callable configuration, client-ownership, and preset constructors.
- Centralize Redis client ownership and provider-specific options in composition.
- Allow owned clients and preset construction to be restricted to the roles a
  process actually enables.
- Return companion `core.Runnable` components rather than starting them.
- Apply LLM-debug and checkpoint retention through provider-owned options.
- Run reusable checkpoint, command, workflow, schedule, task-store, legacy
  task-queue, task-delivery, lock, debug-store, and skills conformance suites.
- Preserve compatibility convenience paths during deprecation.

**Exit:** Existing Redis deployments retain behavior through the new composition
layer.

### Phase 4 — Alternative provider proof

- Implement a minimal alternative provider for durable state first.
- Implement task transport and notification adapters separately.
- Demonstrate capability-level override and a mixed-provider composition.
- Validate a DynamoDB/SQS/Streams-or-EventBridge composition if AWS is selected
  for the reference proof.

**Delivered reference proof:**
`examples/orchestration-backend-portability` implements internal PostgreSQL
workflow-state, schedule, and task-store adapters, a core-NATS command adapter,
and a NATS JetStream at-least-once task transport. Their reusable conformance
suites and a mixed Redis/PostgreSQL/NATS composition run as a Job in an isolated
local Kind cluster. The adapters import only public framework contracts and
remain outside the framework modules; they are executable architectural
evidence rather than advertised production providers. AWS was not selected for
this local, open-source reference proof. The same example deploys two live
cross-process paths. A two-replica API dispatches work through NATS to a
two-replica worker, which uses retained Redis discovery to invoke geocoding and
weather tools and commits the result to PostgreSQL. Separately, two scheduler
replicas use PostgreSQL schedule/task state and a retained Redis leader lock to
promote due work into NATS JetStream; two scheduled-executor replicas consume
it, Redis-discover `travel-chat-agent`, invoke its standard scheduled endpoint,
and persist the terminal task and agent response in PostgreSQL. The `live-test`
and `scheduler-test` commands inspect provider identity and check both complete
cross-process paths.

**Exit:** Backend replacement requires provider adapters and composition changes,
not planner, executor, HITL, scheduler, or skill behavior changes.

### Phase 5 — Migration tooling and operational guidance

- Define export/import formats for required durable domain records.
- Add safe per-contract mirroring or shadow-read decorators where appropriate.
- Document drain/cutover procedures for HITL and task queues.
- Add health, diagnostics, and provider identity reporting.

**Exit:** Operators can move between supported compositions with an explicit,
observable data and traffic cutover plan.

## 16. Acceptance criteria

The design is complete when:

1. Runtime orchestration contains no provider-name branches.
2. `core.Discovery` is not used to obtain datastore clients or operations.
3. Every backend dependency is represented by a narrow domain interface.
4. `OrchestrationBackends` is interface-typed, inspectable through getters,
   configurable through typed options, optional, and used only at composition
   boundaries; its aggregate fields are not public API.
5. A process validates only the capabilities required by its enabled features.
6. Redis remains available as the included default preset.
7. The common Redis path can resolve configuration, construct selected roles,
   validate them, and expose deterministic cleanup through one transparent
   convenience constructor.
8. An application can replace one backend capability without abandoning the rest of
   a preset.
9. A non-Redis durable implementation can be injected without changing
   orchestration behavior.
10. Task transport and notification technologies can differ from the durable
   database.
11. Provider-specific clients and configuration do not enter neutral feature
    configuration or domain objects.
12. Provider background work participates in the framework `Runnable` lifecycle.
13. Each advertised provider passes the conformance suites for the capabilities
    it exposes.
14. Provider switching occurs through controlled startup composition and
    migration, never by mutating the backend of an in-flight request.
15. Skills V1 adds its registry capability through this composition model and
    does not require completion of an alternative provider or migration tooling.

## 17. Decisions retained for later implementation

- Domain interfaces remain the primary abstraction; no universal datastore is
  introduced.
- `OrchestrationBackends` is a convenience composition value, not a new
  mandatory runtime dependency or an exported-field aggregate.
- Redis is the included default composition.
- A provider preset may be hybrid and use several technologies.
- DynamoDB/SQS/Streams-or-EventBridge is the leading AWS reference composition,
  not a required architecture.
- Discovery backend selection remains independent.
- Provider-specific configuration and SDKs remain outside root orchestration
  behavior.
- The composition foundation for existing stores is a prerequisite for skills;
  supported alternate-provider promotion and operational migration remain
  incremental follow-up.

## 18. References

- [Framework design principles](../../FRAMEWORK_DESIGN_PRINCIPLES.md)
- [Orchestration architecture](../ARCHITECTURE.md)
- [Agent skills architecture plan](AGENT_SKILLS_ARCHITECTURE_PLAN.md)
