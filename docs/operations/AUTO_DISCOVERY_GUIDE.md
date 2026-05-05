# Auto-Discovery Guide

If you've ever wired up a microservice mesh and asked "how does my agent find the weather tool without a hardcoded URL?", you're in the right place. This guide explains TruvaG3's auto-discovery — how components register, how agents find them, what keeps stale entries out, and what to do when something looks off.

Auto-discovery is what lets you deploy a new tool to your cluster and have existing agents pick it up without changing a single line of agent code. It's the connective tissue that turns a pile of independent services into a working agent system.

> **Working Example**
>
> Everything in this guide comes from fully working, in-tree implementations:
> - **Tool that registers** (passive, can't discover): [`examples/weather-tool-v2/`](../../examples/weather-tool-v2/)
> - **Agent that discovers** (active, registers + finds): [`examples/travel-chat-agent/`](../../examples/travel-chat-agent/)
> - **Single-process tool+agent demo**: [`examples/agent-example/`](../../examples/agent-example/)
> - **Live registry inspection UI**: [`examples/registry-viewer-app/`](../../examples/registry-viewer-app/)
> - **Default infrastructure** (Redis/Valkey): [`examples/k8-deployment/`](../../examples/k8-deployment/)
> - **Core interfaces**: [`core/interfaces.go`](../../core/interfaces.go) (search for `Registry`, `Discovery`)
> - **Default backend**: [`core/redis_registry.go`](../../core/redis_registry.go), [`core/redis_discovery.go`](../../core/redis_discovery.go)

## Table of Contents

- [What is Auto-Discovery?](#what-is-auto-discovery)
- [Quick Start](#quick-start)
- [Architecture](#architecture)
  - [The Tool/Agent Split](#the-toolagent-split)
  - [End-to-End Flow](#end-to-end-flow)
- [How Components Register](#how-components-register)
  - [The Registration Record (`ServiceInfo`)](#the-registration-record-serviceinfo)
  - [What the Registry Stores: Four Key Types](#what-the-registry-stores-four-key-types)
  - [Atomic Registration](#atomic-registration)
  - [Address Resolution: Pod IP vs Service DNS](#address-resolution-pod-ip-vs-service-dns)
- [How Agents Discover Tools and Other Agents](#how-agents-discover-tools-and-other-agents)
  - [`FindByCapability` — the workhorse](#findbycapability--the-workhorse)
  - [`FindService` — when you want a specific component](#findservice--when-you-want-a-specific-component)
  - [`Discover` with a filter — multi-criteria](#discover-with-a-filter--multi-criteria)
  - [Which method should I pick?](#which-method-should-i-pick)
- [Heartbeat and TTL Management](#heartbeat-and-ttl-management)
  - [The Two-Layer Lease](#the-two-layer-lease)
  - [Heartbeat Interval](#heartbeat-interval)
  - [What the Heartbeat Actually Does](#what-the-heartbeat-actually-does)
- [Multi-Replica Behaviour](#multi-replica-behaviour)
  - [Each Replica Gets a Unique ID](#each-replica-gets-a-unique-id)
  - [Last-Writer-Wins on Shared Indexes](#last-writer-wins-on-shared-indexes)
  - [What Happens When One Replica Crashes](#what-happens-when-one-replica-crashes)
- [Resilience](#resilience)
  - [Self-Healing Recovery (Runtime Outages)](#self-healing-recovery-runtime-outages)
  - [Background Retry (Cold-Start Race)](#background-retry-cold-start-race)
  - [Which Mechanism Fires When?](#which-mechanism-fires-when)
- [Configuration](#configuration)
  - [Environment Variables](#environment-variables)
  - [Programmatic Options](#programmatic-options)
- [Observability](#observability)
  - [Inspecting the Registry](#inspecting-the-registry)
  - [Expected Anomalies](#expected-anomalies)
- [Troubleshooting](#troubleshooting)
- [Extending: Pluggable Backends](#extending-pluggable-backends)
  - [The Interface Contract](#the-interface-contract)
  - [What an Implementation Must Provide](#what-an-implementation-must-provide)
  - [Wiring a Custom Backend](#wiring-a-custom-backend)
  - [Test Path: `MockDiscovery`](#test-path-mockdiscovery)
- [FAQ](#faq)
- [See Also](#see-also)

---

## What is Auto-Discovery?

Auto-discovery is how TruvaG3 components find each other at runtime without hardcoded addresses. Two things happen automatically:

1. **Registration**: When a tool or agent starts up, the framework publishes a record to a shared service registry — its name, address, port, and the capabilities it provides.
2. **Discovery**: When an agent needs to call a capability (say, `current_weather`), it queries the registry and gets back a list of healthy components that can do it.

The framework ships with a Redis/Valkey-backed registry as the default, but the registry is pluggable behind the `core.Discovery` interface. You can swap in etcd, Consul, an in-memory mock for tests, or anything else that satisfies the contract.

**Two roles, enforced at compile time:**

- **Tools** are *passive*. They implement `core.Registry` (Register, UpdateHealth, Unregister). They can't discover other components — the type system literally won't let them. This keeps tools simple and prevents them from quietly growing into orchestrators.
- **Agents** are *active*. They implement `core.Discovery`, which embeds Registry plus three lookup methods. Agents both publish themselves *and* find others.

That separation is enforced at compile time, not by convention. See [`FRAMEWORK_DESIGN_PRINCIPLES.md`](../../FRAMEWORK_DESIGN_PRINCIPLES.md) §"Compile-Time Architectural Enforcement" for why.

## Quick Start

The minimum to get auto-discovery working: a Redis instance, two environment variables, and one option function.

**1. Run a Redis (or Valkey) somewhere reachable from your service.** For local development, the in-tree infrastructure does this for you:

```bash
cd examples/k8-deployment
./setup-infrastructure.sh   # provisions Redis + observability stack into a kind cluster
```

**2. Tool side — register and start serving.** This is the entire wiring needed for a passive tool:

```go
package main

import (
    "context"
    "os"
    "github.com/truvaagents/truva-g3/core"
)

func main() {
    tool := core.NewTool("weather-tool")
    tool.RegisterCapability(core.Capability{
        Name:        "current_weather",
        Description: "Returns current weather for a location.",
        InputTypes:  []string{"json"},
        OutputTypes: []string{"json"},
    })

    framework, _ := core.NewFramework(tool,
        core.WithRedisURL(os.Getenv("REDIS_URL")),
        core.WithDiscovery(true, "redis"),
    )
    framework.Run(context.Background())
}
```

After `framework.Run`, the framework auto-injects a `Registry`, registers the tool, and starts the heartbeat loop. You don't call `Register` yourself.

**3. Agent side — find and call.** From an agent, look up by capability and call:

```go
package main

import (
    "context"
    "fmt"
    "os"
    "github.com/truvaagents/truva-g3/core"
)

func main() {
    agent := core.NewBaseAgent("trip-planner")

    framework, _ := core.NewFramework(agent,
        core.WithRedisURL(os.Getenv("REDIS_URL")),
        core.WithDiscovery(true, "redis"),
    )
    go framework.Run(context.Background())

    // Find any tool that can give us weather.
    weatherTools, _ := agent.FindByCapability(context.Background(), "current_weather")
    for _, svc := range weatherTools {
        fmt.Printf("found %s at %s:%d\n", svc.Name, svc.Address, svc.Port)
    }
}
```

That's it. No service mesh, no DNS plumbing, no hardcoded URLs. Add a second weather tool with a different implementation, and the agent will start finding both — with no agent-side code change.

For a complete agent that uses discovery to orchestrate multiple tools, see [`examples/travel-chat-agent/`](../../examples/travel-chat-agent/).

## Architecture

### The Tool/Agent Split

```
┌───────────────────────────────────────────────────────────────────────┐
│                                                                       │
│  ┌────────────────┐                            ┌────────────────┐     │
│  │     Tool       │                            │     Agent      │     │
│  │  (passive)     │                            │   (active)     │     │
│  │                │                            │                │     │
│  │ implements     │                            │ implements     │     │
│  │ core.Registry  │                            │ core.Discovery │     │
│  │                │                            │  (= Registry + │     │
│  │ • Register     │                            │     lookup)    │     │
│  │ • UpdateHealth │                            │                │     │
│  │ • Unregister   │                            │ • Register     │     │
│  │                │                            │ • Discover     │     │
│  │  ✗ cannot      │                            │ • FindService  │     │
│  │    discover    │                            │ • FindBy…      │     │
│  └───────┬────────┘                            └────────┬───────┘     │
│          │                                              │             │
│          │   register / heartbeat        discover / find│             │
│          ▼                                              ▼             │
│       ┌────────────────────────────────────────────────────┐          │
│       │           Service Registry (pluggable)             │          │
│       │     default: Redis/Valkey                          │          │
│       │     interface: core.Discovery                      │          │
│       └────────────────────────────────────────────────────┘          │
│                                                                       │
└───────────────────────────────────────────────────────────────────────┘
```

The compile-time guarantee: a `Tool` cannot call discovery methods because the `Registry` interface doesn't include them. If you try, your code won't compile.

### End-to-End Flow

When a tool starts up and an agent needs it, here's the full lifecycle:

```
┌────────────┐                                                ┌────────────┐
│  Tool      │                                                │  Agent     │
│ (weather-  │                                                │ (trip-     │
│  tool-v2)  │                                                │  planner)  │
└─────┬──────┘                                                └─────┬──────┘
      │ 1. framework.Run()                                          │
      │    ┌──────────────────────────────────────────────────┐     │
      │    │ Auto-inject Registry, register, start heartbeat  │     │
      │    └──────────────────────────────────────────────────┘     │
      │                                                             │
      │ 2. Register                                                 │
      ├──────────────────► ┌──────────────────────────────────┐     │
      │                    │ Service Registry (Redis/Valkey)  │     │
      │                    │                                  │     │
      │                    │ truvag3:services:weather-tool-v2 │     │ 4. FindByCapability
      │                    │  → {ID, Name, Address, Port,     │◄────┤    "get_weather_forecast"
      │                    │     Capabilities, Metadata, ...} │     │
      │                    │                                  │     │
      │                    │ truvag3:capabilities:            │     │ 5. Returns
      │                    │     get_weather_forecast         ├────►│    [ServiceInfo, ...]
      │                    │  → {weather-tool-v2}             │     │
      │ 3. Heartbeat       │                                  │     │ 6. HTTP call
      │    every 15s       │ truvag3:names:weather-tool-v2    │     │    Address:Port
      │ ──────────────────►│  → {weather-tool-v2}             │     │    (Service DNS)
      │                    │                                  │     │
      │                    │ truvag3:types:tool               │     │
      │                    │  → {weather-tool-v2, …}          │     │
      │                    └──────────────────────────────────┘     │
      │                                                             │
      ▼                                                             ▼
```

The agent never has the tool's address baked into its config. Discovery happens fresh each time, and the tool can move pods, scale, or be replaced — the agent picks up the change automatically on the next lookup.

## How Components Register

### The Registration Record (`ServiceInfo`)

When a component registers, it sends a `ServiceInfo` record. Defined at [`core/component.go:49`](../../core/component.go#L49):

```go
type ServiceInfo struct {
    ID           string                 // Stable in K8s (= Name); UUID-suffixed otherwise
    Name         string                 // Logical name, e.g. "weather-tool-v2"
    Type         ComponentType          // "tool" or "agent"
    Description  string
    Address      string                 // Where to dial it
    Port         int                    // Service port
    Capabilities []Capability           // What this instance can do
    Metadata     map[string]interface{} // Auto-populated K8s fields + anything you add
    Health       HealthStatus           // "healthy" / "unhealthy" / "unknown"
    LastSeen     time.Time              // ISO-8601 UTC; refreshed on every heartbeat
}
```

The `ID` value depends on deployment mode (the framework picks for you):

- **In Kubernetes with `TRUVAG3_K8S_SERVICE_NAME` set**: `ID = Name`. All replicas of a Deployment share the same ID and write to the same registry key — *one logical entry per Deployment*. K8s Service handles load balancing across the pods. This is the default for production K8s deployments.
- **Outside Kubernetes** (or in K8s without `TRUVAG3_K8S_SERVICE_NAME`): `ID = "<name>-<uuid8>"`, generated at process start. Each instance gets its own registry entry. The non-K8s case.

The choice is made in [`core/agent.go:1019-1022`](../../core/agent.go#L1019-L1022) (agents) and [`core/agent.go:1086-1089`](../../core/agent.go#L1086-L1089) (tools). See [Multi-Replica Behaviour](#multi-replica-behaviour) for what each mode looks like in practice.

`Metadata` is a free-form map, but in Kubernetes the framework auto-populates several fields for you. From a real registry record:

```json
"metadata": {
    "container_port": "8351",
    "namespace": "truvag3-examples",
    "pod_name": "async-travel-agent-9ddbd67cd-j5l22",
    "pod_namespace": "truvag3-examples",
    "service_name": "async-travel-agent-service",
    "service_port": "80"
}
```

In service-scoped mode (multiple pods writing the same key), `pod_name` reflects whichever replica heartbeated most recently. In steady state this can be the same pod for extended periods — heartbeat scheduling tends to cluster, and once one pod's write lands last in a round, it can keep winning subsequent rounds. The value flips reliably when the pod set itself changes (rolling update, crash, replacement, scale event). Treat it as a "currently-observed pod" hint, not an authoritative routing target.

The `Capability` struct ([`core/agent.go:70`](../../core/agent.go#L70)) carries more than just a name and description — it also includes optional payload-generation hints (`InputSummary`, `OutputSummary`) and an auto-generated schema endpoint. Those are explained in the [Tool Development Guide](../building/TOOL_DEVELOPMENT_GUIDE.md); for discovery purposes, you mainly care about `Name`, `Description`, `Endpoint`, and `Internal` (a flag that hides admin-style capabilities from the AI-facing catalog).

### What the Registry Stores: Four Key Types

A single registration writes four key families to Redis (verified at [`core/redis_registry.go:198-234`](../../core/redis_registry.go#L198-L234)). All keys are namespace-prefixed; the default namespace is `truvag3`.

| Key | Type | Contents | TTL |
|---|---|---|---|
| `truvag3:services:<id>` | string (JSON) | Full `ServiceInfo` for this instance | 30 s (default; tunable) |
| `truvag3:capabilities:<cap_name>` | set | Instance IDs that provide this capability | 60 s (= 2× service TTL) |
| `truvag3:names:<name>` | set | Instance IDs registered under this logical name | 60 s |
| `truvag3:types:<type>` | set | Instance IDs of this type (`tool` / `agent`) | 60 s |

The individual record is the source of truth; the three index sets exist purely for fast lookups. When an agent calls `FindByCapability("current_weather")`, the framework reads the capability index set, then does a parallel `GET` on each `truvag3:services:<id>` to fetch the live record.

The two TTL values are deliberate. The service record's short TTL (30 s) means a crashed instance disappears within half a minute. The longer index TTL (60 s) means a slow heartbeat from one replica doesn't drop the whole capability index — see [Multi-Replica Behaviour](#multi-replica-behaviour) below.

### Atomic Registration

All four writes — service record + each capability set + name set + type set — happen in a single `TxPipeline` ([`core/redis_registry.go:194-264`](../../core/redis_registry.go#L194-L264)). Either all writes commit together, or none of them do.

The practical implication: an agent will never see a half-registered component. There's no window where `truvag3:capabilities:current_weather` lists an ID whose `truvag3:services:<id>` record hasn't been written yet. This matters during fast scale-up bursts and during recovery from a transient registry outage.

### Address Resolution: Pod IP vs Service DNS

What the framework writes into `ServiceInfo.Address` depends on where it's running:

- **In Kubernetes with `TRUVAG3_K8S_SERVICE_NAME` set**: the address becomes `<svc>.<ns>.svc.cluster.local`, and the port becomes `TRUVAG3_K8S_SERVICE_PORT` (default 80). This is what you want — agents dial through the K8s Service, not pod IPs, so traffic load-balances across replicas. Logic at [`core/address_resolver.go:32`](../../core/address_resolver.go#L32).
- **Outside K8s** (or in K8s without those env vars set): falls back to `config.Address` (default `localhost`) and `config.Port` (default 8080).

If you're seeing `0.0.0.0:8080` in your registry records, that's a sign the K8s wiring isn't set. The fix is wiring the env vars correctly — see [`KUBERNETES.md` §"Service Discovery on Kubernetes"](KUBERNETES.md#service-discovery-on-kubernetes).

## How Agents Discover Tools and Other Agents

The `Discovery` interface ([`core/interfaces.go:200`](../../core/interfaces.go#L200)) gives agents three ways to look up components. Each method maps to a concrete index lookup followed by record fetches.

### `FindByCapability` — the workhorse

The most common discovery pattern. Use it when you know what task needs doing but don't care which specific component does it.

```go
weather, err := agent.FindByCapability(ctx, "current_weather")
if err != nil {
    return err
}
if len(weather) == 0 {
    return fmt.Errorf("no provider for current_weather is available")
}
// Returns []*ServiceInfo for every healthy component that advertised "current_weather".
```

Backed by `truvag3:capabilities:<cap>` (a Redis set), so it's a single `SMEMBERS` followed by parallel `GET`s. Fast, returns all healthy providers, and supports automatic load-distribution downstream — pick one at random, round-robin, or pass the list to your routing logic.

### `FindService` — when you want a specific component

Use this when you have integration logic specific to a named component, or when you want to reach all replicas of one component (for example, to coordinate a rolling task across them).

```go
weatherInstances, _ := agent.FindService(ctx, "weather-tool")
// Returns all healthy replicas of "weather-tool" — typically one per pod.
```

Backed by the `truvag3:names:<name>` set. Same shape as `FindByCapability`, but indexed by logical name instead of capability.

### `Discover` with a filter — multi-criteria

When you need to combine constraints — type + capability + metadata — use the full filter:

```go
results, _ := agent.Discover(ctx, core.DiscoveryFilter{
    Type:         core.ComponentTypeTool,
    Capabilities: []string{"data_analysis", "statistical_modeling"},
    Metadata: map[string]interface{}{
        "env":     "production",
        "version": "v2",
    },
})
```

The full `DiscoveryFilter` has four fields: `Type`, `Name`, `Capabilities` (must contain *all* listed), `Metadata` (must match *all* key-value pairs). Filters that hit the indexes (`Type`, `Name`, single capability) stay fast; multi-capability + metadata combinations need to fetch records to evaluate, which is slower but still cheap by absolute terms.

### Which method should I pick?

| Use this | When |
|---|---|
| `FindByCapability` | You need the capability, you don't care which instance does it. **The default choice for orchestration.** |
| `FindService` | You want all replicas of a specific named component (load-balance manually, run a coordinated task, etc.). |
| `Discover` with filter | You have multiple constraints (type + capability + metadata combined). |

A practical rule: if you find yourself filtering the result of `FindByCapability` in application code, you probably want `Discover` with a filter instead.

## Heartbeat and TTL Management

The piece that makes all this self-cleaning: every component continuously *renews its lease* on its registry entry. If the renewal stops, the entry expires and the component disappears from discovery — no manual cleanup, no leader election.

### The Two-Layer Lease

Two TTL values, intentionally different:

| Layer | Default | Why this value |
|---|---|---|
| **Service record** (`truvag3:services:<id>`) | 30 s | Fast detection of crashed instances. Within 30 s of the last heartbeat, an unresponsive instance disappears from the registry. |
| **Index sets** (`truvag3:capabilities:`, `:names:`, `:types:`) | 60 s (= 2× service TTL) | Index sets are *shared* across all instances of a name/capability. The longer TTL means a single replica that's slow to heartbeat doesn't drop the whole capability index for everyone. |

Both TTLs are set on every successful registration *and* on every heartbeat. The `TxPipeline` re-applies them all at once.

### Heartbeat Interval

The heartbeat goroutine ticks at `ttl / 2` by default — 15 seconds for the default 30 s service TTL. The reasoning is the standard one for lease renewal: with two refresh attempts inside one TTL window, a single missed heartbeat (network hiccup, stop-the-world GC pause) doesn't cause the lease to expire.

The actual heartbeat math from [`core/redis_registry.go:967-985`](../../core/redis_registry.go#L967-L985):

| Rule | Value |
|---|---|
| Default interval | `ttl / 2` |
| Minimum interval | 2 s (clamped if a smaller value is requested) |
| Maximum interval | must be `< ttl`; falls back to `ttl/2` if a value `≥ ttl` is requested (otherwise the key would expire before renewal) |
| Per-startup jitter | up to `interval / 4`, drawn once when the heartbeat goroutine starts and reused for every tick. Spreads heartbeats across replicas without making any single replica's cadence drift over time. |

You can override with `TRUVAG3_DISCOVERY_HEARTBEAT` or `core.WithHeartbeatInterval(d)`. In practice, the defaults work for almost everything — the main reason to override is if you've also overridden the TTL.

**Note**: some in-tree example deployments set a custom heartbeat interval via their config maps. For example, the chat agents under [`examples/`](../../examples/) ship with `TRUVAG3_DISCOVERY_HEARTBEAT: 10s` — this is **a deployment-config override**, not a framework default change. If you're comparing pod behaviour across the example cluster and notice different cadences, check the deployment's env vars and configmaps:

```bash
kubectl get deploy -n <ns> <name> \
  -o jsonpath='{.spec.template.spec.containers[*].env[?(@.name=="TRUVAG3_DISCOVERY_HEARTBEAT")].value}'
# (empty output = framework default of ttl/2; otherwise the configured value)

# If the deployment's env list doesn't show it, check its configmap:
kubectl get configmap -n <ns> <name>-env-config -o yaml | grep TRUVAG3_DISCOVERY_HEARTBEAT
```

The framework default remains `ttl/2`; deployments choose to opt in to a different value when the workload pattern justifies it.

### What the Heartbeat Actually Does

Each heartbeat tick calls `UpdateHealth(ctx, id, status)` ([`core/redis_registry.go:294`](../../core/redis_registry.go#L294)), which re-runs the same `TxPipeline` as registration:

1. Re-write the service record with fresh TTL (`SET <key> <data> EX <ttl>`)
2. Re-add the ID to all four index sets (`SADD`)
3. Re-apply each index set's TTL (`EXPIRE <key> <ttl*2>`)

So heartbeats don't just refresh a single key — they refresh *the whole picture*. This matters when an index set has expired entirely and needs to be reconstructed (for instance, after a Redis restart): the heartbeat from any healthy replica will rebuild the affected sets.

There's also a self-healing path: if the heartbeat detects that the service record has gone missing (e.g., Redis was unavailable for longer than TTL and the entry expired), it re-registers from the in-memory state it kept around for exactly this case ([`core/redis_registry.go:737-790`](../../core/redis_registry.go#L737-L790)). See [Self-Healing Recovery](#self-healing-recovery-runtime-outages) below.

## Multi-Replica Behaviour

Running multiple replicas of the same tool or agent — `replicas: 3` in your Deployment, or HPA-driven scaling — is a first-class case. The framework supports two registration modes, and which one fires depends on whether K8s service-fronted discovery is wired (i.e., whether `TRUVAG3_K8S_SERVICE_NAME` is set).

### Mode 1 — Service-Scoped Registration (the K8s default)

When `TRUVAG3_K8S_SERVICE_NAME` is set, every replica of the Deployment registers with `ID = Name`. They all write to the same `truvag3:services:<name>` key, and they all carry the same K8s Service DNS as their `Address`.

```
truvag3:services:async-travel-agent
  → {ID: "async-travel-agent",
     Name: "async-travel-agent",
     Address: "async-travel-agent-service.<ns>.svc.cluster.local",
     Port: 80,
     ...}

truvag3:names:async-travel-agent → {async-travel-agent}        # one member
truvag3:capabilities:<cap>       → {async-travel-agent, …}     # one entry per Deployment, not per pod
truvag3:types:agent              → {async-travel-agent, …}
```

This is what's running in the canonical kind cluster: `async-travel-agent` has `replicas: 2` and shows up as **one** registry entry. Both pods race to write the same record on every heartbeat — last-writer-wins on `SET` and `SADD` semantics — and the K8s Service handles the actual load balancing across the live pods.

The trade-offs of this mode:

- ✓ Registry size is bounded by the number of Deployments, not the number of pods. HPA scaling doesn't churn the registry.
- ✓ Discovery returns one entry per logical service. Callers get a stable Service DNS to dial; K8s does the load balancing.
- ✓ No coordination needed — each pod's heartbeat is independent.
- ⚠ `metadata.pod_name` reflects whichever replica heartbeated most recently. It's a hint, not a routing target.
- ⚠ Per-replica observations (per-pod metrics, per-pod health drift) need a different signal — the registry record is *the service*, not *a specific pod*.

### Mode 2 — Instance-Scoped Registration (non-K8s, or K8s without `TRUVAG3_K8S_SERVICE_NAME`)

In this mode, every instance generates a unique ID (`<name>-<uuid8>`) at process start. Each registers independently:

```
truvag3:services:weather-tool-a1b2c3d4 → {Address: localhost:8080, ...}
truvag3:services:weather-tool-e5f6g7h8 → {Address: localhost:8081, ...}

truvag3:names:weather-tool        → {weather-tool-a1b2c3d4, weather-tool-e5f6g7h8}
truvag3:capabilities:<cap>        → {weather-tool-a1b2c3d4, weather-tool-e5f6g7h8}
```

This is what you get when running the tool standalone, in a non-K8s container, or in K8s without service-fronted wiring. `FindByCapability` returns both instances, and the caller (or its routing layer) picks one.

### Last-Writer-Wins on Shared Indexes

Both modes share one mechanic: heartbeats from any healthy replica refresh the shared index sets via `SADD <set> <id>` + `EXPIRE <set> <2×ttl>`. Redis's `EXPIRE` is last-writer-wins, so whenever any replica heartbeats, it pushes the index set's TTL back to the full 60 s. As long as one replica is healthy, the indexes stay alive.

In Mode 1, every replica's `SADD` adds the same ID, so the set has one stable member. In Mode 2, each replica adds its own unique ID — and the set grows or shrinks as replicas come and go.

### What Happens When One Replica Crashes

The two modes behave differently when a replica dies:

**Mode 1 (service-scoped, K8s default):**

```
T=0s    Replicas A, B, C all heartbeating against truvag3:services:async-travel-agent
        Record TTL: 30 s (refreshed by whichever pod heartbeated last)

T=15s   Pod A heartbeat → record TTL pushed back to 30 s
T=20s   Pod A crashes
T=25s   Pod B heartbeat → record TTL pushed back to 30 s

(record never expires; B and C keep refreshing it indefinitely)

K8s handles the rest: the dead pod's Endpoint is removed from the
async-travel-agent-service Service automatically (failed readiness),
so traffic stops routing to it. The registry still has one entry
pointing at the Service DNS — agents don't care which pod answers.
```

The dead pod doesn't show up in the registry as anything — there's nothing per-pod to disappear. All routing intelligence lives in the K8s Service. This is what makes Mode 1 simple operationally: the registry tracks *services*, not *pods*.

**Mode 2 (instance-scoped, non-K8s):**

```
T=0s    Three replicas healthy, three records, each with 30 s TTL
T=15s   Pod a1b2c3d4 heartbeat → its record TTL refreshed to 30 s
T=20s   Pod a1b2c3d4 crashes
T=30s   Pod e5f6g7h8 heartbeat → its record TTL refreshed
        Pod a1b2c3d4's record TTL: ~15 s remaining (no refresh since T=15s)

T=45s   Pod a1b2c3d4's service record expires
        truvag3:capabilities:<cap> still lists a1b2c3d4 (set entries don't
        auto-remove); but Discover() filters it out because the service
        record is gone — callers never see it.

T=~90s  Index set gets rebuilt fresh by ongoing heartbeats from healthy
        replicas, eventually GCing the dead ID.
```

User-visible effect: dead replicas are filtered out of discovery within 30 s, surviving replicas keep serving, no manual cleanup. The lazy GC in capability/name index sets is fine because `Discover()` always cross-references with the live service record.

## Resilience

Two failure modes the framework handles automatically. They look similar but fire under different conditions.

### Self-Healing Recovery (Runtime Outages)

The setup: your component started successfully, registered itself, and is happily heartbeating. Then Redis becomes temporarily unreachable.

What happens depends on outage duration:

| Outage duration | Effect |
|---|---|
| < TTL (under 30 s by default) | Heartbeat fails for one or two ticks; service record's TTL hasn't elapsed yet; on the next successful heartbeat, the world is back to normal. |
| > TTL | The service record expires from Redis. When Redis is reachable again, the next heartbeat detects "service not found" and triggers re-registration from in-memory state ([`core/redis_registry.go:737`](../../core/redis_registry.go#L737)). |

You don't enable this — it's always on. Heartbeat goroutines naturally retry on every tick.

### Background Retry (Cold-Start Race)

A different problem: at *startup*, the registry isn't reachable yet (e.g., Redis is also a Kubernetes Pod that hasn't become Ready). By default, `NewRedisRegistry` retries the initial connection 3× with backoff (~10 s total) and then returns an error. The framework runs the component in **standalone mode** — HTTP endpoints work, but the component never makes it into the registry.

Setting `TRUVAG3_DISCOVERY_RETRY=true` switches to a non-blocking retry policy: the framework reports the failed initial registration but keeps a background goroutine that retries on an exponentially-backing-off interval. From [`core/redis_registry.go:1128-1180`](../../core/redis_registry.go#L1128-L1180):

| Behaviour | Value |
|---|---|
| Initial retry interval | `TRUVAG3_DISCOVERY_RETRY_INTERVAL` (default 30 s) |
| Backoff | Doubles on each failure |
| Cap | 5 minutes (so retries stay frequent enough to recover quickly when Redis comes back) |
| Effect on first success | Registers, starts heartbeat, becomes discoverable. The component has been serving traffic the whole time; the registry just wasn't aware of it. |

This is the recommended pattern for production Kubernetes deployments. The alternative — an `initContainer` that blocks pod startup until Redis is up — works but serializes pod-readiness on the registry's recovery time, which gets ugly in cascading-startup situations.

### Which Mechanism Fires When?

| Scenario | Mechanism |
|---|---|
| Component starts cleanly, registry stays up | Neither — normal heartbeat is enough |
| Component starts, registry briefly hiccups (< TTL) for a few seconds at runtime | Heartbeat absorbs it silently |
| Component starts, registry goes down for > TTL at runtime | Self-healing recovery on next heartbeat after Redis returns |
| Component starts, registry isn't ready yet | Background retry (if `TRUVAG3_DISCOVERY_RETRY=true`); standalone mode otherwise |

## Configuration

### Environment Variables

The discovery-related variables — full reference with precedence rules in [`docs/reference/ENVIRONMENT_VARIABLES_GUIDE.md`](../reference/ENVIRONMENT_VARIABLES_GUIDE.md):

| Variable | Default | Effect |
|---|---|---|
| `TRUVAG3_DISCOVERY_ENABLED` | `false` | Master switch. If false, no registration or discovery. |
| `TRUVAG3_DISCOVERY_PROVIDER` | `redis` | Backend identifier. The in-tree default. |
| `TRUVAG3_REDIS_URL` / `REDIS_URL` | — | Connection string for the default Redis backend. Standard `REDIS_URL` is honoured. |
| `TRUVAG3_DISCOVERY_TTL` | `30s` | Service record TTL. Index TTL is always 2× this. |
| `TRUVAG3_DISCOVERY_HEARTBEAT` | `0` (= ttl/2) | Heartbeat interval. Clamped: min 2 s, must be < TTL. |
| `TRUVAG3_DISCOVERY_CACHE` | `true` | Local read-side cache for discovery results (within an agent). |
| `TRUVAG3_DISCOVERY_CACHE_TTL` | `5m` | Local cache entry lifetime. |
| `TRUVAG3_DISCOVERY_RETRY` | `false` | Enable background retry on initial connection failure. **Recommended for production K8s.** |
| `TRUVAG3_DISCOVERY_RETRY_INTERVAL` | `30s` | Initial retry interval; doubles on failure, caps at 5 minutes. |

For Kubernetes deployments, the `TRUVAG3_K8S_*` env vars (`SERVICE_NAME`, `SERVICE_PORT`, `NAMESPACE`, `POD_IP`, `NODE_NAME`) control how `Address` is populated in the registry record. See [`KUBERNETES.md` §"Service Discovery on Kubernetes"](KUBERNETES.md#service-discovery-on-kubernetes) for the deployment wiring.

### Programmatic Options

When configuring the framework in code:

| Option | Effect |
|---|---|
| `core.WithRedisURL(url)` | Set Redis URL (also honours `REDIS_URL` / `TRUVAG3_REDIS_URL`) |
| `core.WithDiscovery(true, "redis")` | Enable discovery with the named provider |
| `core.WithRedisDiscovery(url)` | Convenience: enables discovery + sets Redis URL in one call |
| `core.WithDiscoveryTTL(d)` | Override service-record TTL |
| `core.WithHeartbeatInterval(d)` | Override heartbeat interval |
| `core.WithDiscoveryCacheEnabled(b)` | Toggle the local read-side cache |

For tests:

| Option | Effect |
|---|---|
| `core.WithMockDiscovery(true)` | Use the in-memory `MockDiscovery` (no Redis required) |

Full signatures are in [`docs/reference/API_REFERENCE.md`](../reference/API_REFERENCE.md). Precedence rule (project-wide): when both env vars and option functions set the same value, **option functions win**. See [`core/ARCHITECTURE.md`](../../core/ARCHITECTURE.md) §"Environment Variable Loading".

## Observability

### Inspecting the Registry

When `redis-cli` is available and Redis is reachable (port-forward in K8s if needed), you have direct visibility into the registry:

```bash
# Port-forward to Redis if you're running in kind:
kubectl port-forward service/redis 6379:6379 &

# Every registered component
redis-cli KEYS "truvag3:services:*"

# Inspect a specific service record (returns the full ServiceInfo JSON)
redis-cli GET "truvag3:services:weather-tool-abc123"

# Find which components advertise a capability
redis-cli SMEMBERS "truvag3:capabilities:current_weather"

# Count replicas of a named component
redis-cli SCARD "truvag3:names:weather-tool"

# Watch heartbeats in real time (renews every ~15 s by default)
redis-cli MONITOR | grep "truvag3:services"

# Check remaining TTL on a record (useful for diagnosing heartbeat gaps)
redis-cli TTL "truvag3:services:weather-tool-abc123"
# Healthy: 15-30 (always between heartbeat-window-fresh and just-renewed).
# 0 or -2 means the record has expired or doesn't exist.
```

For a friendlier view of the whole registry, the [`examples/registry-viewer-app/`](../../examples/registry-viewer-app/) example is a standalone web dashboard that shows all registered services, their capabilities, and their health in real time. It's read-only, runs anywhere with access to your Redis, and is useful for both developers and ops.

The framework also emits Prometheus metrics for discovery operations (`discovery.registrations`, `discovery.lookups`, `discovery.lookup.duration_ms`). See [`examples/k8-deployment/OBSERVABILITY.md`](../../examples/k8-deployment/OBSERVABILITY.md) for the full metric catalog.

For log-based observability of the registration health, the framework emits a periodic `Heartbeat health summary` INFO line every ~5 minutes per service:

```
INFO  Heartbeat health summary  service_id=currency-tool service_name=currency-tool
      success_count=15048 failure_count=0 success_rate=100.00%
      uptime_minutes=4775 time_since_last_success_sec=0
```

Useful for ops alerting — set an alert if `success_rate` drops below a threshold (say 95%) or if no summary line appears for >6 minutes (the heartbeat goroutine has stopped). Per-heartbeat tick events are emitted at `DEBUG` level (`Service health updated`, `Refreshing index set TTLs`, `Index set TTL refresh completed`) — enable `TRUVAG3_LOG_LEVEL=debug` to see them.

### Expected Anomalies

These behaviours look surprising but are by design:

**An ID appears in `truvag3:capabilities:foo` but its service record is missing.**
The replica died. Its 30 s service-record TTL elapsed before the index set's 60 s TTL. `Discover()` filters this out — callers never see the dead instance, even though the index entry hasn't been GC'd yet.

**Discovery results differ between methods during a deployment.**
Index set TTLs lag service record TTLs by ~30 s. During rolling updates, you can have a brief window where a name set lists more IDs than have live service records. `Discover()` reconciles this; eventual consistency resolves it within ~30 s.

**Redis memory grows slowly even after pods are removed.**
Dead IDs sit in capability and name index sets until the index's 60 s TTL expires *or* a heartbeat from a healthy replica re-creates the set fresh. This is a feature: the system favours staying available over staying perfectly clean. GC happens; it's just lazy.

**A freshly-registered service shows stale `LastSeen` for a few seconds.**
Initial registration sets status to `healthy`, but `LastSeen` is updated on each heartbeat — the first tick comes after the heartbeat interval (default 15 s). For the first 15 s, `LastSeen` reflects the registration time, not a heartbeat.

## Troubleshooting

**Symptom: a tool starts but no agent finds it.**

```bash
# 1. Confirm the framework actually registered the tool.
redis-cli GET "truvag3:services:<your-tool-id>"
# Empty result → registration never succeeded. Check tool startup logs for connection errors.

# 2. Check the Address field in the JSON. If it's "0.0.0.0:8080" or a pod IP,
#    K8s service-fronted discovery isn't wired. Fix: set TRUVAG3_K8S_SERVICE_NAME
#    and TRUVAG3_K8S_NAMESPACE on the deployment.
#    See KUBERNETES.md §"Service Discovery on Kubernetes".

# 3. Check capability spelling. A typo in the agent's FindByCapability call
#    won't error; it'll silently return zero results.
redis-cli SMEMBERS "truvag3:capabilities:<exact-capability-name>"
```

**Symptom: heartbeat failures in logs.**

The log line `Failed to send heartbeat` indicates the registry is unreachable. Check Redis health (`redis-cli ping`), network policies (if applicable), and whether the component is running long blocking calls that prevent the heartbeat goroutine from being scheduled. The heartbeat is jittered by up to `interval/4` to avoid thundering-herd refreshes, so occasional clustered failures aren't necessarily a problem if they recover.

**Symptom: component doesn't re-register after Redis restart.**

The self-healing path requires the heartbeat goroutine to still be running. If logs show `Heartbeat stopped` or the goroutine has exited (e.g., the framework's parent context was cancelled), self-healing won't fire. A pod restart fixes this; if it recurs, run with `TRUVAG3_LOG_LEVEL=debug` and check the heartbeat lifecycle logs.

**Symptom: discovery returns components but calls fail.**

The address in the registry is the dial target. If the registered `Address` is a pod IP (e.g., `10.244.0.73:8080`) instead of the K8s Service DNS, calls land on whichever pod's IP got registered — and as soon as that specific pod is rotated by a rolling update, the address points nowhere. Same fix as the first symptom — set the `TRUVAG3_K8S_*` env vars so `Address` becomes the Service DNS.

## Extending: Pluggable Backends

Redis/Valkey is the default. The interface is the contract — any backend that satisfies it can replace the default.

### The Interface Contract

A custom backend implements `core.Discovery` ([`core/interfaces.go:200`](../../core/interfaces.go#L200)):

```go
type Registry interface {
    Register(ctx context.Context, info *ServiceInfo) error
    UpdateHealth(ctx context.Context, id string, status HealthStatus) error
    Unregister(ctx context.Context, id string) error
}

type Discovery interface {
    Registry  // embedded
    Discover(ctx context.Context, filter DiscoveryFilter) ([]*ServiceInfo, error)
    FindService(ctx context.Context, serviceName string) ([]*ServiceInfo, error)
    FindByCapability(ctx context.Context, capability string) ([]*ServiceInfo, error)
}
```

Tools only need `Registry` (3 methods); agents need the full `Discovery`.

### What an Implementation Must Provide

The framework's expectations, so callers can reason about behaviour:

1. **Registration is durable for at least the configured TTL.** Whether the backend implements TTL natively (Redis, etcd) or via a periodic GC pass is an implementation detail.
2. **`UpdateHealth` re-extends the lease.** The standard refresh-on-heartbeat pattern.
3. **Filter operations return only currently-live records.** Backends must not return entries whose underlying lease has expired. The Redis implementation enforces this by relying on Redis TTL semantics; an alternative implementation must do the equivalent check.
4. **Atomic registration is recommended but not required.** The Redis implementation uses `TxPipeline` so half-registered states never exist. A backend without transaction support can still satisfy the interface; callers handle the rare partial-state case by retrying.
5. **Concurrency-safe.** Multiple goroutines (heartbeat + discovery) share the same registry instance.

### Wiring a Custom Backend

```go
type MyBackend struct {
    // your fields
}

func (b *MyBackend) Register(ctx context.Context, info *core.ServiceInfo) error             { /* ... */ }
func (b *MyBackend) UpdateHealth(ctx context.Context, id string, s core.HealthStatus) error { /* ... */ }
func (b *MyBackend) Unregister(ctx context.Context, id string) error                        { /* ... */ }
func (b *MyBackend) Discover(ctx context.Context, f core.DiscoveryFilter) ([]*core.ServiceInfo, error) { /* ... */ }
func (b *MyBackend) FindService(ctx context.Context, name string) ([]*core.ServiceInfo, error)         { /* ... */ }
func (b *MyBackend) FindByCapability(ctx context.Context, cap string) ([]*core.ServiceInfo, error)     { /* ... */ }

// Inject before framework startup. The framework's auto-injection only fires
// when Registry/Discovery is nil; a developer-supplied implementation is honoured.
agent := core.NewBaseAgent("my-agent")
agent.Discovery = &MyBackend{}

framework, _ := core.NewFramework(agent)
framework.Run(context.Background())
```

The auto-injection logic ([`core/tool_dependency_injection_test.go`](../../core/tool_dependency_injection_test.go)) checks whether `Registry`/`Discovery` is already set before injecting the default. So pre-setting your custom backend wins.

### Test Path: `MockDiscovery`

For unit tests that don't want to spin up Redis, [`core/mock_discovery.go`](../../core/mock_discovery.go) provides an in-memory implementation of the full `Discovery` interface. Enable it via:

```go
framework, _ := core.NewFramework(agent,
    core.WithMockDiscovery(true),
)
```

`MockDiscovery` is sufficient for testing discovery interactions without external dependencies. It doesn't simulate TTL/heartbeat timing precisely, but the interface contract is preserved — your code paths that call `FindByCapability`, `Discover`, etc. work the same way.

## FAQ

**My agent calls `FindByCapability` and gets nothing back. What's wrong?**

Three likely causes. (1) The tool isn't registered — check `redis-cli GET "truvag3:services:<id>"`. (2) Capability name mismatch — check `redis-cli SMEMBERS "truvag3:capabilities:<exact-name>"`; typos in the lookup key silently return zero results. (3) The Redis URL is wrong — agent and tool must point at the same Redis. See [Troubleshooting](#troubleshooting) for the diagnostic walkthrough.

**What happens if Redis goes down?**

Components stay running and serve traffic normally — Redis is the registry, not the data path. New discovery requests fail until Redis comes back. Existing connections between components keep working. When Redis recovers, components self-heal: the next heartbeat detects "service not found" and re-registers automatically. No manual restart needed.

**Can I run multiple replicas of the same tool?**

Yes — that's a first-class case. Each replica registers under a unique ID but shares the logical name and capabilities. The K8s Service load-balances across replicas, and `FindByCapability` returns all healthy ones. See [Multi-Replica Behaviour](#multi-replica-behaviour).

**Does discovery work outside Kubernetes?**

Yes. The framework falls back to `config.Address:config.Port` (defaults `localhost:8080`) when the K8s env vars aren't set. Useful for local development. The Redis backend itself doesn't care about the deployment environment — only how `Address` is populated.

**Can I switch from Redis to another backend?**

Yes — implement `core.Discovery` and inject your implementation before `framework.Run`. The framework's auto-injection respects pre-set fields. See [Extending: Pluggable Backends](#extending-pluggable-backends).

**Why are there two TTLs (30 s service, 60 s index)?**

Because index sets are *shared* across replicas. A short index TTL would mean a single slow replica's missed heartbeat could drop the whole capability index — pulling all sibling replicas out of discovery for a moment. The 2× ratio means any healthy replica's heartbeat keeps the shared indexes alive, even when others are slow. See [The Two-Layer Lease](#the-two-layer-lease).

**Should I use service-fronted discovery (Service DNS) or pod-fronted (pod IP)?**

Service DNS, almost always. It gives you free K8s load balancing across replicas, survives pod rotation, and works with mesh sidecars. Pod-fronted addressing is only useful for niche cases like coordinating direct work on a specific pod, and even then you can use `FindService` to list pods explicitly. Wire `TRUVAG3_K8S_SERVICE_NAME` and you get Service DNS automatically.

**Do I need `TRUVAG3_DISCOVERY_RETRY=true` always?**

No, but it's recommended in production K8s. Without it, a pod that starts before Redis is ready will fall into permanent standalone mode and require a manual restart. With it, the pod retries in the background and self-registers as soon as Redis becomes reachable. The cost is roughly nothing — just a goroutine that's idle most of the time.

**How do I unregister a component cleanly on shutdown?**

The framework calls `Unregister` for you when its parent context is cancelled (graceful shutdown). If you crash hard, the entry expires via TTL within 30 s. You almost never need to call `Unregister` directly.

## See Also

- [Kubernetes Deployment Patterns](KUBERNETES.md) — env-var wiring for service-fronted discovery, NetworkPolicy, ServiceMonitor, troubleshooting in K8s
- [API Reference](../reference/API_REFERENCE.md) — exact signatures for `Registry`, `Discovery`, `Discover`, `FindService`, `FindByCapability`
- [Environment Variables Guide](../reference/ENVIRONMENT_VARIABLES_GUIDE.md) — precedence rules and full env-var reference
- [Framework Design Principles](../../FRAMEWORK_DESIGN_PRINCIPLES.md) — why Tool/Agent split is compile-time enforced
- [Core Module Architecture](../../core/ARCHITECTURE.md) — `Registry`/`Discovery` interface contracts and design rationale
- [Tool Development Guide](../building/TOOL_DEVELOPMENT_GUIDE.md) — building tools that register capabilities
- [Agent Development Guide](../building/AGENT_DEVELOPMENT_GUIDE.md) — building agents that discover and orchestrate tools
- [Examples: registry-viewer-app](../../examples/registry-viewer-app/) — live registry inspection dashboard
