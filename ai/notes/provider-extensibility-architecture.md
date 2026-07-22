# AI Provider Extensibility Architecture

**Status:** Accepted architecture; Phase 9 implementation pending

**Date:** 2026-07-18

**Scope:** `core`, `ai`, and the AI-facing boundary of `orchestration`

**Purpose:** Define a durable provider-integration architecture that lets
applications adapt to provider changes without waiting for a framework release,
while preserving safety, portability, retries, streaming, failover, and
observability.

**Implementation status:** In progress. Phases 0A through 8 are committed on
the local `feat/provider-extensibility` branch. Phase 9 was holistically
reviewed and approved on 2026-07-22 and remains unimplemented; Section 19.12 is
its code-level implementation blueprint. Public Phase 9 names remain proposed
until their implementation API review.

---

## Contents

1. [Executive decision](#1-executive-decision)
2. [Authority and alignment](#2-authority-and-alignment)
3. [Motivating failures](#3-motivating-failures)
4. [Goals, non-goals, and invariants](#4-goals-non-goals-and-invariants)
5. [Provider integration planes](#5-provider-integration-planes)
6. [Layered public access model](#6-layered-public-access-model)
7. [Additive core request/result capability](#7-additive-core-requestresult-capability)
8. [Request lifecycle and precedence](#8-request-lifecycle-and-precedence)
9. [Request policy engine](#9-request-policy-engine)
10. [Provider request drafts and codecs](#10-provider-request-drafts-and-codecs)
11. [Endpoint and deployment resolution](#11-endpoint-and-deployment-resolution)
12. [Credentials and transport](#12-credentials-and-transport)
13. [Retry and error architecture](#13-retry-and-error-architecture)
14. [Chain composition](#14-chain-composition)
15. [Provider factory evolution](#15-provider-factory-evolution)
16. [Result normalization, usage, pricing, and telemetry](#16-result-normalization-usage-pricing-and-telemetry)
17. [Orchestration integration](#17-orchestration-integration)
18. [Backward compatibility and migration](#18-backward-compatibility-and-migration)
19. [Implementation plan](#19-implementation-plan)
20. [Test and conformance plan](#20-test-and-conformance-plan)
21. [Acceptance criteria](#21-acceptance-criteria)
22. [Alternatives rejected](#22-alternatives-rejected)
23. [Bounded implementation decisions](#23-bounded-implementation-decisions)
24. [Final architectural position](#24-final-architectural-position)

---

## 1. Executive decision

Two incidents expose one architectural problem:

1. [Anthropic current-generation sampling incompatibility](ANTHROPIC_ADAPTIVE_THINKING_TEMPERATURE_DEPRECATION.md)
   shows that a provider can reject a field that the framework inserts and that
   applications currently cannot remove.
2. [Custom enterprise provider integration](custom-enterprise-provider-integration.md)
   shows that an OpenAI-compatible provider can require dynamic credentials,
   a nonstandard authentication header, deployment-aware routing, custom fields,
   or a reusable request/response codec that the framework cannot configure
   cleanly.

The immediate symptoms are different, but the shared failure is:

> TruvaG3 owns final provider-request construction without exposing a safe,
> provider-scoped, application-composable finalization boundary.

The long-term solution is a layered provider integration pipeline:

1. presence-aware portable intent;
2. provider/model/surface-scoped declarative request rules;
3. constrained request middleware over a safe provider draft;
4. endpoint and deployment resolution;
5. protected credential and transport strategies;
6. reusable request/response codecs and normalized results;
7. explicit per-entry chain composition;
8. a custom `core.AIClient` as the full-control exit path.

The architecture deliberately does **not** turn TruvaG3 into a universal schema
for every provider. It makes common behavior portable, makes optional
provider-native behavior safely editable, and provides an honest lower layer
when a protocol or SDK cannot be represented.

### Key compatibility decision

Do not break or expand the method set of the existing `core.AIClient`.

Instead, introduce an additive, optional request/result capability whose exact
names can be finalized during implementation:

~~~go
// Existing contract remains unchanged.
type AIClient interface {
    GenerateResponse(
        ctx context.Context,
        prompt string,
        options *AIOptions,
    ) (*AIResponse, error)
}

// Proposed additive capability.
type AIRequestClient interface {
    AIClient
    Generate(
        ctx context.Context,
        request *AIRequest,
    ) (*AIResult, error)
}

type StreamingAIRequestClient interface {
    AIRequestClient
    Stream(
        ctx context.Context,
        request *AIRequest,
        callback StreamCallback,
    ) (*AIResult, error)
}
~~~

New built-in clients implement both interfaces through one internal request
pipeline. Existing applications, mocks, factories, and custom clients continue
to compile. New callers can use presence-aware parameters, provider patches,
purpose metadata, and an effective-request report without overloading legacy
zero values or growing `AIResponse` indefinitely.

This is more reliable than adding a raw body mutator and more compatible than
changing the existing `AIClient` method signature.

---

## 2. Authority and alignment

This proposal is subordinate to the repository's governing documents. Every
implementation slice must review and obey all applicable authorities below;
this plan cannot silently supersede them.

Repository-wide authorities:

- [Repository agent instructions](../../AGENTS.md) — repository workflow,
  verification, example deployment, and documentation-sign-off rules;
- [Framework Design Principles](../../FRAMEWORK_DESIGN_PRINCIPLES.md) — module
  dependency DAG, interface-first design, layered composition, configuration,
  compatibility, security, reliability, and observability principles;
- [Framework Architecture Overview](../../docs/overview/ARCHITECTURE.md) —
  system boundaries, provider abstraction, enterprise integration, security,
  and operational architecture;
- [Contributing Guide](../../CONTRIBUTING.md) — Go style, conformance suites,
  workspace-wide test procedure, and pre-commit gates.

Module authorities:

- [Core Module Architecture](../../core/ARCHITECTURE.md);
- [AI Module Architecture](../ARCHITECTURE.md);
- [Orchestration Module Architecture](../../orchestration/ARCHITECTURE.md);
- [Memory Module Architecture](../../memory/ARCHITECTURE.md);
- [Telemetry Module Architecture](../../telemetry/ARCHITECTURE.md).

The current tree has no `resilience/ARCHITECTURE.md`; its closest module-level
authority is the [Resilience Module README](../../resilience/README.md). This
proposal does not authorize changes to or imports of `resilience`. If a later
slice expands into that module, it must first identify and obtain approval for
the governing resilience design rather than infer one from this plan.

Cross-cutting authorities, when their trigger applies:

- [Distributed Tracing Guide](../../docs/observability/DISTRIBUTED_TRACING_GUIDE.md)
  for code that creates spans, events, or trace attributes;
- [Logging Implementation Guide](../../docs/observability/LOGGING_IMPLEMENTATION_GUIDE.md)
  for code that emits framework logs;
- [Environment Variables Guide](../../docs/reference/ENVIRONMENT_VARIABLES_GUIDE.md)
  and [Limits Cheatsheet](../../docs/reference/LIMITS_CHEATSHEET.md) if a slice
  adds or changes environment variables or numeric limits;
- [Custom AI Provider Guide](../../docs/building/CUSTOM_AI_PROVIDER_GUIDE.md)
  for the public enterprise-provider behavior and examples delivered by this
  work.

If accepted, every affected module architecture document must be updated before
or in the same review slice as the code that changes its contract. A code change
must not lead a contradictory architecture document with a promise to reconcile
it later. The two incident notes should remain as case studies and implementation
evidence rather than being deleted or merged.

Once approved, this document supersedes conflicting long-term solution
recommendations in the incident notes. It does not replace their incident
evidence, immediate fixes, provider-specific research, or rollout cautions.

### 2.1 Alignment matrix

| Governing principle | Architectural response |
|---|---|
| Production-first reliability | Fresh retry bodies, deterministic policy execution, response-aware credential invalidation, sync/stream parity, and conformance tests are required. |
| Compile-time architectural enforcement | Cross-module contracts are small typed interfaces and data structures; orchestration never imports `ai`; unsupported provider capabilities fail validation. |
| Intelligent configuration | Portable defaults remain easy; explicit application options override them; complex provider behavior uses injected interfaces at the call site. |
| Simple things simple, complex things possible | Existing `NewClient` remains the convenience path; rules and credential sources are customization paths; direct provider constructors and custom clients remain the full-control path. |
| Interface-first design | `core` owns only minimal interface-crossing contracts; behavior engines, HTTP types, credentials, codecs, and provider adapters remain in `ai`. |
| Composition over bundling | Request policy, routing, credentials, transport, pricing, and chain entries are independent primitives. No monolithic “enterprise provider” constructor is required. |
| The factory stays dumb | Factories construct provider clients from resolved configuration. They do not own orchestration lifecycle or application policy sources. |
| Fail-fast configuration | Invalid rules, ambiguous patches, unsupported HTTP options, invalid auth headers, and incompatible chain entries fail during construction or pre-network preparation. |
| Resilient runtime behavior | Provider outages, token rejection, retryable transport errors, and chain failover are handled without weakening request validation. |
| Backward compatibility | Existing `AIClient`, `StreamingAIClient`, `ProviderFactory`, `AIOptions`, and `WithExtra` behavior remain available during migration. |
| Secrets management | Credentials are injected after application policy, are not exposed through editors, and never enter reports, policy fingerprints, logs, or debug metadata. |
| Optional telemetry | All observability remains nil-safe and fail-open; telemetry failures never fail an AI request. |

### 2.2 Module dependency boundary

The valid dependency graph remains:

~~~text
core
  ↑
  ├── ai ─────────────→ telemetry
  └── orchestration ──→ telemetry
~~~

In particular:

- `core` must not import `ai`, `orchestration`, or `telemetry`;
- `orchestration` must not import `ai`;
- provider policy callbacks, HTTP clients, transports, token sources, endpoint
  resolvers, and codecs must not live in `core`;
- data required to cross from orchestration to an arbitrary AI client may live
  in `core`;
- the application remains responsible for constructing and composing the AI
  client and orchestration modules.

### 2.3 Principle conformance review

§2.1 maps this design to the governing principles at a glance. This section is
the original deeper conformance assessment against the primary framework, AI,
and core authorities — where the design conforms, the genuine tensions, the
existing violations it corrects, and the guardrails that keep the recommended
MVP subset (defined below) aligned. It reflects an independent architecture
review (2026-07-18). The complete Phase 9 scope audit against every repository
and cross-cutting authority listed above is recorded in §19.12.1.

**Primary sources for this original review:**
[FRAMEWORK_DESIGN_PRINCIPLES.md](../../FRAMEWORK_DESIGN_PRINCIPLES.md) (FDP),
[ai/ARCHITECTURE.md](../ARCHITECTURE.md), and
[core/ARCHITECTURE.md](../../core/ARCHITECTURE.md).

#### Where it conforms

| Principle (source) | How this design satisfies it |
|---|---|
| Composition over bundling (FDP §4) | No monolithic "enterprise provider" constructor; credential source, endpoint resolver, HTTP client, pricing resolver, and request rules are independent primitives composed at the call site (§6, §12–§16, §22). |
| Simple simple / complex possible / no cliffs (FDP §3) | The five-layer access model (§6): `NewClient` unchanged → presence-aware → declarative rules → plugs → custom client. A custom client remains one `ClientEntry` in a chain (§14), closing the abstraction cliff. |
| Small, focused interfaces (core §1; FDP "bigger interface = weaker abstraction") | `AIRequestClient`/`StreamingAIRequestClient` are 1–2 methods; `RequestEditor`, `CredentialSource`, `EndpointResolver`, `PricingResolver` are single-purpose. "One giant integration interface" is explicitly rejected (§22). |
| Factory stays dumb; plugins never reach into lifecycle (FDP synthesis 2–3) | Factories validate and wire only; they do not invoke, refresh, or own the runtime lifecycle of injected components (§15, §19.6). Control flows framework → plugin. |
| Dependency DAG; orchestration ⊄ ai; core imports nothing (FDP §2; core §2) | Data in `core`, engine in `ai/requestpolicy` (core-only), codec in `providerkit/openaiwire` (no root-`ai` import), orchestration consumes core only (§2.2, §9.1, §17). Verified: orchestration imports no `ai` today, and the design keeps it so. |
| Backward compatibility / API stability (FDP; core closing note) | `AIClient` untouched; keyed-literal guards; sealed `ClientOption` preserving `AIConfig`/`NewClient`; legacy adapter; staged deprecation (§7, §18, §22). |
| Fail-fast config; resilient runtime; fail-open telemetry (FDP "Error Handling") | Invalid rules/patches/endpoints/auth fail at construction; telemetry stays nil-safe; `CreateValidated` closes the current "factory cannot error" gap (§13.3, §15, §16). |
| Secrets management; rotation without restart (FDP/core Security) | Credentials attached after policy, excluded from reports/fingerprints/logs; `CredentialSource` supports rotation without restart (§12, §16.4). |
| Framework is domain-agnostic (FDP) | Provider/model knowledge stays in the provider; orchestration and core carry no provider names; tenant/domain policy is application-supplied (§17.1). |
| ai/ARCHITECTURE §5–7: valuable-not-mandatory; portable core + escape hatches | Declarative patches deliver "add/remove a provider field without leaving `ai/`"; Layer 5 is the documented native-SDK exit. This is the literal fulfillment of ai/ARCHITECTURE's stated intent. |

#### Genuine tensions

1. **Core public-surface growth (the primary one).** Core's defining virtue is
   minimalism and stability. This design roughly triples the AI-related core
   surface (adds `AIRequest`, `AIResult`, `AIParameter[T]`, `AIGenerationOptions`,
   `AIProviderPatch`, `AIProviderSelector`, `AIRequestReport`, `AIUsageDetails`,
   `AICost`, plus `GenerateAI`/`CloneAIRequest`). It is *defensible*: the types are
   provider-neutral **data**, and most must live in `core` precisely because
   orchestration consumes them and may not import `ai` — a core principle forcing
   the surface into core. The mutation *logic* correctly stays out of core in
   `ai/requestpolicy` (§9.1). The growth is therefore the principle-consistent
   *price* of keeping orchestration provider-neutral, but it is the largest, most
   permanent commitment and must be minimized (see guardrails).
2. **Mechanism weight vs. "thin compatibility guardrails / minimal compatibility
   responsibility" (ai/ARCHITECTURE §7).** The full pipeline — policy engine,
   JSON-Pointer patching, fingerprinting, compatibility modes, the report triad,
   cache integration, middleware — is heavy relative to "thin guardrails, not an
   endless compatibility matrix." The defense (a generic *mechanism*, not
   per-provider *tables*) is sound, but the weight is the least principle-aligned
   aspect. The MVP subset directly mitigates this by deferring fingerprinting,
   compatibility modes, middleware, the report triad, and cache integration.
3. **`AIParameter[T]` generics in core.** Permitted, but it is the first generic in
   core's public API and a permanent stability commitment in the module that most
   values stability. §23.2 keeps generics-vs-constructors open; prefer whichever
   keeps the core surface smallest.

#### Existing violations this design corrects

- `BaseClient.ApplyDefaults` mutates caller options today — a violation of core's
  "return a copy to prevent external modification"; the clone-first invariant
  (§4.3, §19.3) fixes it.
- Cost is stamped only in the OpenAI client — an inconsistency; §16 centralizes it.
- `ProviderFactory.Create` cannot return an error — a fail-fast gap; `CreateValidated`
  (§15) closes it.
- Anthropic sync/stream drift — a consistency violation; the shared builder (§10.1,
  §19.3) removes it.

#### MVP conformance guardrails

The MVP subset — presence-aware parameters + declarative patches +
credential/endpoint/HTTP injection + explicit chain entries + codec extraction +
pricing relocation — is the *more* conformant path (it defers the heaviest,
least-aligned machinery in tension 2). To keep it aligned during implementation:

1. Core receives data plus the two additive interfaces only — no engine, HTTP,
   credentials, or codecs in `core`.
2. Every core type must be justified by a concrete cross-module (orchestration)
   consumer; if only `ai` uses it, it belongs in `ai`.
3. Keep `GenerateAI`/`CloneAIRequest` thin — dispatch and defensive copy only, no
   provider knowledge.
4. Prefer the smallest presence-aware shape; weigh generics against core's
   stability mandate.
5. Ship the Phase 0A/0B incident fixes as independent PRs; they are provider-local
   conformance improvements and must not wait on the public surface.

**Bottom line:** the design conforms to all three authority documents — strongly on
composition, layering, small interfaces, factory discipline, backward
compatibility, secrets, and the ai-module's escape-hatch philosophy — with the only
genuine friction being `core`/`ai` minimalism, which the MVP is the correct
instrument for honoring.

---

## 3. Motivating failures

### 3.1 Provider rejects a framework-managed field

The native Anthropic path currently applies a client default when
`AIOptions.Temperature == 0`, then serializes temperature unconditionally.
Affected models reject non-default sampling parameters.

The current application escape hatches cannot correct the request:

- legacy zero means “inherit the client default,” not explicit zero or omit;
- `Extra` is additive and cannot remove a framework-managed field;
- headers cannot modify request bodies;
- a static framework model table necessarily lags provider changes;
- sync and streaming construction can drift.

The immediate Anthropic fix remains provider-local: after concrete model
resolution and all merges, omit incompatible sampling fields using one shared
sync/stream builder. The architecture in this document ensures the next
provider change does not require an emergency framework release.

### 3.2 Enterprise provider changes routing and credentials

An enterprise-hosted OpenAI-compatible service may retain the OpenAI body and
response schema but change:

- endpoint shape or deployment routing;
- API-version query parameters;
- token acquisition and refresh;
- authentication header name;
- mutual TLS or content-dependent request signing;
- required application fields;
- returned model identity or usage details.

The stock OpenAI client can express a custom base URL and additive body fields,
but it cannot cleanly inject a rotating credential, replace protected Bearer
authentication, or carry per-provider transport configuration through
`ChainClient`.

### 3.3 Shared root cause

Both failures occur after portable caller intent has entered a concrete
provider client:

~~~text
portable intent
    -> provider assumptions
    -> final wire request
~~~

There is no supported application-controlled stage between provider translation
and protected transport finalization. The solution must add that stage without
exposing credentials, allowing structural corruption, or coupling orchestration
to a provider.

---

## 4. Goals, non-goals, and invariants

### 4.1 Goals

The architecture must let an application:

1. represent inherit, explicit value—including zero—and explicit omission for
   portable optional parameters;
2. set or remove an unknown optional provider field after concrete model and
   surface resolution;
3. scope changes to provider, provider alias, surface, concrete model,
   operation, and optional request purpose;
4. inject rotating credentials, custom auth headers, mTLS, or signing without
   concrete-client type assertions;
5. route portable model intent to enterprise deployments;
6. use identical semantic preparation for synchronous and streaming calls;
7. compose differently configured provider instances in one failover chain;
8. preserve normalized response, usage, tracing, LLM debug, and cost behavior;
9. retain a clean native-client path for incompatible protocols;
10. migrate incrementally without breaking current applications.

### 4.2 Non-goals

The architecture does not:

- model every field of every provider;
- guarantee that a generic patch can add a member absent from a compiled SDK;
- expose raw provider SDK pointers as a universal stable interface;
- expose credentials or arbitrary `*http.Request` objects to application policy;
- put provider/model compatibility logic in orchestration;
- silently repair arbitrary remote 400 responses and retry them;
- automatically make every custom client fully observable without a normalized
  result or an instrumentation wrapper;
- require a first-class Azure provider when an OpenAI-compatible surface plus
  an endpoint resolver is sufficient.

### 4.3 Non-bypassable invariants

1. Caller-owned options, maps, slices, clients, and transports are never
   mutated.
2. Credentials are attached after request policy and are never visible to it.
3. Destination, HTTP method, required model/input structure, and stream
   protocol are protected from ordinary request rules.
4. Explicit application policy may override built-in compatibility knowledge
   only for optional fields.
5. Invalid or unsupported configuration fails before a network call.
6. A logical provider attempt finalizes semantic request policy exactly once.
7. Every transport retry receives a fresh replayable body.
8. Sync and streaming share provider-local semantic construction.
9. Reports and fingerprints never contain prompts, credentials, secret headers,
   or raw request bodies.
10. Optional telemetry remains fail-open.

---

## 5. Provider integration planes

The enterprise note’s wire-format/auth distinction is useful but not sufficient
for the combined problem. Use five explicit planes.

### 5.1 Intent and policy plane

Defines portable caller intent, presence semantics, model compatibility,
application rules, and safe dynamic middleware.

Examples:

- omit temperature for a concrete Anthropic model;
- set a newly introduced beta parameter;
- add a tenant-specific optional request field;
- choose strict versus compatibility behavior.

### 5.2 Surface and routing plane

Resolves where and through which provider surface the request is sent.

Examples:

- native Anthropic Messages API;
- Anthropic through Bedrock Converse;
- OpenAI Chat Completions;
- Azure deployment path plus `api-version`;
- regional enterprise gateway.

### 5.3 Codec plane

Translates between the logical request draft and the wire or SDK type, then
normalizes the response and streaming events.

Examples:

- OpenAI JSON and SSE;
- Azure using the OpenAI codec with a different endpoint resolver;
- Bedrock Converse SDK inputs and outputs;
- a custom native provider adapter.

### 5.4 Credential and transport plane

Obtains credentials, injects protected authentication, performs mTLS or
signing, sends requests, and handles transport retries.

Examples:

- static API key;
- cached OAuth client-credentials token;
- custom `api-key` header;
- SigV4 signing on every transport attempt;
- caller-supplied HTTP client.

### 5.5 Result and observability plane

Normalizes provider identity, model identity, usage, effective request changes,
cost, errors, and trace attributes.

These planes compose in order but remain independently replaceable. A developer
can replace credential acquisition without replacing the OpenAI codec, or use a
custom client without changing orchestration.

---

## 6. Layered public access model

The public API should follow the framework’s no-cliffs principle.

### Layer 1 — existing convenience API

~~~go
client, err := ai.NewClient(
    ai.WithProvider("anthropic"),
    ai.WithModel("default"),
)
~~~

Existing `core.AIOptions` and environment-driven defaults continue to work.
Legacy calls are translated into the new internal pipeline.

### Layer 2 — presence-aware portable request

~~~go
result, err := requestClient.Generate(ctx, &core.AIRequest{
    Prompt: prompt,
    Purpose: "planning",
    Generation: core.AIGenerationOptions{
        Temperature: core.SetAIParameter(float32(0)),
        MaxTokens:   core.SetAIParameter(4000),
    },
})
~~~

This is the preferred API when exact presence matters.

### Layer 3 — declarative provider request rules

~~~go
client, err := ai.NewRequestClient(
    ai.WithProvider("anthropic"),
    ai.WithRequestRules(core.AIProviderPatch{
        Name:    "app-anthropic-sonnet-5-sampling-workaround",
        Version: "1",
        Selector: core.AIProviderSelector{
            Provider: "anthropic",
            Surface:  "messages",
            Model:    "claude-sonnet-5-*",
        },
        Remove: []string{
            "/temperature",
            "/top_p",
            "/top_k",
        },
    }),
)
~~~

Rules are immutable, serializable, validated, and fingerprintable. They are the
preferred operational workaround for provider changes.

### Layer 4 — constrained middleware and integration strategies

~~~go
client, err := ai.NewRequestClient(
    ai.WithRequestMiddleware(tenantPolicy),
    ai.WithCredentialSource(credentials),
    ai.WithEndpointResolver(deployments),
    ai.WithHTTPClient(httpClient),
)
~~~

These are behavioral plugs configured at the application call site. Each owns
one concern.

The advanced `With...` values are `ClientOption`s accepted by
`NewRequestClient` and explicit provider chain entries. Existing `AIOption`
values also satisfy `ClientOption`, so provider/model/default configuration
does not need a second set of helpers. Legacy `NewClient(...AIOption)` and its
exported `AIConfig` remain unchanged.

### Layer 5 — explicit provider or native client

Applications can:

- construct a built-in provider directly;
- register a reusable custom `ProviderFactory`;
- inject an application-local `core.AIClient`;
- combine that client with built-ins through explicit chain entries.

Dropping to this layer for one provider does not require abandoning
`ChainClient`, instrumentation, or orchestration.

---

## 7. Additive core request/result capability

The following types are illustrative. Final names must follow repository
conventions, but the separation of responsibilities is normative.

All new public structs intended to evolve—especially `AIRequest`, `AIResult`,
reports, selectors, and usage details—must be designed for keyed literals only,
using the repository-approved equivalent of an unexported no-unkeyed-literals
marker. Constructors should be provided for stateful values. This prevents the
new evolution seam from repeating the exported-struct compatibility problem it
is intended to solve.

### 7.1 Presence-aware parameters

~~~go
type AIParameterMode uint8

const (
    AIParameterInherit AIParameterMode = iota
    AIParameterSet
    AIParameterOmit
)

type AIParameter[T any] struct {
    Mode  AIParameterMode
    Value T
}

type AIGenerationOptions struct {
    Model           string
    Temperature     AIParameter[float32]
    TopP            AIParameter[float32]
    TopK            AIParameter[int]
    MaxTokens       AIParameter[int]
    SystemPrompt    AIParameter[string]
    ReasoningEffort AIParameter[string]
    ResponseFormat  AIParameter[string]
}
~~~

Rules:

- the zero value of `AIParameter[T]` means inherit;
- `Set` preserves zero as an explicit value;
- `Omit` means the provider must not send the optional field;
- required structural values such as the resolved model cannot be omitted;
- new portable fields can be added gradually without making provider-native
  patches obsolete.

### 7.2 Declarative patches

~~~go
type AIProviderSelector struct {
    Provider      string
    ProviderAlias string
    Surface       string
    Model         string
    Operation     string
    Purpose       string
    AllProviders  bool
}

type AIProviderPatch struct {
    Name          string
    Version       string
    Selector      AIProviderSelector
    Set           map[string]interface{}
    Remove        []string
    SetHeaders    map[string]string
    RemoveHeaders []string
}
~~~

Body patch paths use RFC 6901 JSON Pointer. The empty root path is invalid;
`Set` creates missing object parents and adds or replaces the final member;
array paths may address only an existing numeric index; the append token `-`
is not supported in the first release; and removing a missing path is an
idempotent no-op that produces no adjustment. Provider adapters may expose
logical surface paths for SDK-backed requests, but those paths must obey the
same rules and remain stable within a documented provider surface.

Patch validation must reject:

- the same path in both `Set` and `Remove`;
- credentials or protected headers;
- destination, method, required model/input, or streaming invariants;
- unsupported runtime values where deep copying is promised;
- invalid header names or values;
- a selector with no provider, alias, surface, or model constraint unless
  `AllProviders` is explicitly true.

`nil` as a set value means a literal null value. It never means removal.

### 7.3 Request and result envelope

~~~go
type AIRequest struct {
    Prompt     string
    Purpose    string
    Generation AIGenerationOptions
    Patches    []AIProviderPatch
    legacyOptions *AIOptions // set only by NewAIRequestFromLegacy
}

type AIResult struct {
    Response      *AIResponse
    RequestReport *AIRequestReport
    UsageDetails  *AIUsageDetails
    Cost          *AICost
}
~~~

The request envelope provides an evolution seam without changing the existing
`AIClient` signature. The result envelope keeps legacy `AIResponse` stable while
allowing optional normalized details.

`NewAIRequestFromLegacy` must defensively copy legacy options into the
unexported bridge field. Built-in clients use that bridge at the lowest
precedence, so legacy `Extra` and `Headers` survive migration without becoming
part of the preferred new request surface.

On success, `AIResult.Response` must be non-nil. When a call fails after enough
request preparation to produce a useful report, the client may return a
non-nil result together with the error. The error remains authoritative; common
instrumentation and chains must preserve the sanitized failure report before
propagating or aggregating the error.

An implementation advertising `AIRequestClient` must honor each supplied
portable parameter and patch or return a structured unsupported-feature error
before the network call. It must never silently drop new request semantics.

### 7.4 Request report

The report should contain only sanitized, reproducible information:

~~~go
type AIRequestReport struct {
    Provider       string
    ProviderAlias  string
    Surface        string
    Operation      string
    Purpose        string
    RequestedModel string
    ResolvedModel  string
    Adjustments    []AIRequestAdjustment
    Fingerprint    string
    Stable         bool
}

type AIRequestAdjustment struct {
    Source  string // portable, built-in-rule, app-rule, middleware, request-patch
    Rule    string
    Path    string
    Action  string // set, remove, translate, degrade
    Reason  string
}
~~~

Reports must not contain values for arbitrary provider fields. A small
allowlisted portable summary may include safe generation values when needed to
distinguish requested and effective intent. Prompts, body content, credentials,
auth headers, endpoints containing secrets, and secret extras are prohibited.

### 7.5 Legacy adapter semantics

Legacy `GenerateResponse` calls are translated as follows:

| Legacy value | New semantic value |
|---|---|
| Non-zero temperature | explicit set |
| Zero temperature | inherit, preserving existing behavior |
| Non-zero max tokens | explicit set |
| Empty string optional field | inherit |
| `Extra` | copied legacy provider fields with current additive precedence |
| `Headers` | copied eligible non-protected headers |

The legacy adapter must recursively copy supported nested maps and slices. It
must never call the provider with the caller’s mutable `AIOptions` instance.

Built-in model compatibility may still omit a legacy-inherited field. In
compatibility mode, that adjustment is reported. An explicit new API value can
be treated according to compatibility mode.

---

## 8. Request lifecycle and precedence

### 8.1 Normative lifecycle

~~~text
snapshot caller request without mutation
    -> resolve legacy and presence-aware portable intent
    -> resolve provider alias
    -> resolve concrete provider model
    -> resolve provider surface and endpoint route
    -> build a provider-local logical request draft
    -> merge copied legacy client/request extras and eligible headers
    -> enforce portable omit directives
    -> apply built-in provider/model compatibility rules
    -> apply application client rules
    -> apply application client middleware
    -> apply per-request provider patches
    -> validate protected structural and security invariants
    -> encode immutable request semantics
    -> acquire and inject protected credentials
    -> create a fresh transport request for each attempt
    -> apply per-attempt signing/transport behavior
    -> send
    -> decode and normalize result
    -> emit sanitized report, cost, logs, metrics, and spans
~~~

### 8.2 Deterministic precedence

From lowest to highest authority:

1. framework/provider portable defaults;
2. legacy client-level portable options;
3. new client-level presence-aware portable options;
4. legacy per-request portable options;
5. new per-request presence-aware portable options;
6. copied client-level legacy `Extra` and eligible headers;
7. copied per-request legacy `Extra` and eligible headers;
8. explicit portable omit directives;
9. built-in provider/model compatibility rules;
10. application client rules in declaration order;
11. application client middleware in declaration order;
12. per-request provider patches in declaration order;
13. non-bypassable structural and security finalization.

Consequences:

- application rules may correct stale built-in knowledge for optional fields;
- such an override produces a warning adjustment;
- protected fields cannot be overridden even at the last application layer;
- the last explicit operation wins across ordered patches;
- ambiguous operations within one patch fail validation;
- headers use case-insensitive matching;
- provider body paths are case-sensitive unless a surface documents otherwise.

### 8.3 Compatibility modes

Support two modes initially:

| Mode | Behavior |
|---|---|
| Compatible (default) | Built-in compatibility rules may translate or omit incompatible optional fields and report the adjustment. |
| Strict | If final compatibility behavior would change explicitly requested new-API intent without an explicit application rule acknowledging the change, fail before the network call with a structured policy error. |

Do not add a global unsafe “disable all invariants” mode. Applications can
override optional built-in rules explicitly or use a custom client when they
need structural control.

---

## 9. Request policy engine

### 9.1 Package ownership

Use a small dependency-neutral subpackage, conceptually `ai/requestpolicy`,
which imports only `core`.

It owns:

- selector matching;
- patch validation and application;
- the constrained editor;
- deterministic ordering;
- deep copying of documented JSON-compatible values;
- adjustment records;
- stable policy fingerprints.

The root `ai` package owns public construction options and chain propagation.
Each provider owns its surface adapter, protected fields, and encoding.

### 9.2 Constrained middleware

~~~go
type RequestInfo struct {
    Provider       string
    ProviderAlias  string
    Surface        string
    Operation      string
    Purpose        string
    RequestedModel string
    ResolvedModel  string
}

type RequestEditor interface {
    Info() RequestInfo
    Get(path string) (interface{}, bool)
    Set(path string, value interface{}) error
    Remove(path string) error
    SetHeader(name, value string) error
    RemoveHeader(name string) error
}

type RequestMiddleware interface {
    Name() string
    Version() string
    Apply(context.Context, RequestEditor) error
}
~~~

Middleware:

- runs once per logical provider attempt;
- receives isolated call-local state;
- may be called concurrently and must be concurrency-safe;
- must not retain the editor;
- cannot see credentials or protected transport state;
- returns a structured pre-network error on failure;
- reports changed paths and middleware identity, never changed values;
- must be named and versioned for reproducibility.

Declarative patches and middleware must use the same underlying editor. Two
mutation engines would recreate provider and sync/stream drift.

### 9.3 Policy fingerprints

The fingerprint includes:

- rule and middleware name/version;
- selector and operation identity;
- non-secret deterministic policy structure;
- provider surface adapter version;
- route-policy identity where it changes model semantics.

It excludes:

- credentials;
- raw endpoint secrets;
- prompts and request bodies;
- arbitrary secret provider-field values.

If dynamic middleware or secret-dependent behavior cannot produce a stable
fingerprint, the report sets `Stable=false`. AI-output caches must then bypass
the affected entry or use an application-supplied stable namespace.

Do not hash low-entropy secrets into a public fingerprint.

---

## 10. Provider request drafts and codecs

### 10.1 One semantic builder per provider surface

Every built-in provider must use a shared semantic builder for synchronous and
streaming requests:

~~~text
resolved portable intent
    -> provider draft
    -> shared policy pipeline
    -> sync encoder or streaming encoder
~~~

Streaming may add only protocol-specific transport fields after common
semantic finalization. It must not independently reimplement defaulting,
extras, compatibility filtering, headers, or model resolution.

### 10.2 HTTP/JSON surfaces

An HTTP/JSON draft owns:

- provider and surface metadata;
- a deep-copied body tree;
- eligible non-secret headers;
- protected paths and headers;
- ordered adjustments;
- a route reference that is not editable by ordinary request rules.

After finalization, encode the body once into immutable bytes. Each transport
attempt creates a new `http.Request` and body reader from those bytes.

### 10.3 SDK-backed surfaces

An SDK-backed provider creates a logical draft before SDK input materialization:

~~~text
resolved intent
    -> logical provider draft
    -> policy
    -> SDK input adapter
    -> provider SDK call
~~~

For Bedrock Converse, this means policy runs before constructing
`types.InferenceConfiguration`, `ConverseInput`, or `ConverseStreamInput`.

A generic patch cannot invent a field absent from the compiled SDK or surface
adapter. The application must then:

- use a documented provider-native hook;
- upgrade or directly construct the provider client;
- or inject a custom `core.AIClient`.

### 10.4 Shared OpenAI codec

Before creating a separate Azure provider that duplicates logic, extract a
reusable OpenAI-compatible codec responsible for:

- logical request-to-body translation;
- response decoding;
- streaming event decoding;
- finish reason and error normalization;
- usage normalization;
- provider-agnostic request validation.

Endpoint construction, credential strategy, and provider labeling remain
outside the codec.

Place this narrowly scoped public extension kit under
`ai/providerkit/openaiwire`, not a Go `internal` directory, so application-local
and third-party adapters can compose it. The package imports `core` and the
dependency-neutral request-policy contracts but does not import the root `ai`
package.

An Azure provider or enterprise gateway adapter can then compose:

~~~text
OpenAI-compatible codec
    + Azure/enterprise endpoint resolver
    + credential source
    + provider identity
~~~

Build a first-class Azure provider only when deployment mapping or API-version
behavior cannot be expressed cleanly by the stock OpenAI-compatible surface.

---

## 11. Endpoint and deployment resolution

Endpoint routing is a separate trusted stage, not an ordinary body patch.

An endpoint resolver receives only sanitized request identity:

~~~go
type EndpointRequest struct {
    Provider       string
    ProviderAlias  string
    Surface        string
    ResolvedModel  string
    Operation      string
    Purpose        string
}

type EndpointResolver interface {
    ResolveEndpoint(
        ctx context.Context,
        request EndpointRequest,
    ) (ResolvedEndpoint, error)
}
~~~

A resolved endpoint may contain:

- base URL or SDK destination;
- deployment identifier;
- version/query configuration;
- a non-secret route identity used in reports and fingerprints.
- an optional trusted credential scope used only for token acquisition.

Rules:

1. Resolve endpoints after concrete model resolution.
2. Do not expose credential-bearing query values in reports.
3. Validate scheme, host policy, and required path structure.
4. Protect the resolved destination from request middleware.
5. Let model-to-deployment mapping occur here, not in orchestration.
6. Treat a raw URL-rewriting transport as a trusted full-power escape hatch,
   not as the normal routing API.

---

## 12. Credentials and transport

### 12.1 Keep credentials outside request policy

Credentials are attached after semantic request validation. Request rules and
middleware cannot read, set, remove, or report credential headers.

### 12.2 Layered credential API

Provide three levels:

1. existing static API key convenience;
2. a simple dynamic auth-header convenience;
3. an injected credential source with optional rejection observation.

Illustrative contracts:

~~~go
type HeaderCredential struct {
    Name  string
    Value string
}

type CredentialRequest struct {
    Provider        string
    ProviderAlias   string
    Surface         string
    Operation       string
    ResolvedModel   string
    RouteIdentity   string
    Deployment      string
    CredentialScope string
}

type CredentialSource interface {
    Credential(
        ctx context.Context,
        request CredentialRequest,
    ) (HeaderCredential, error)
}

type CredentialRejectionObserver interface {
    CredentialRejected(
        ctx context.Context,
        request CredentialRequest,
        statusCode int,
    ) error
}
~~~

`WithAuthHeader(name, callback)` may remain as a convenience adapter, but its
contract must be honest: a value callback alone cannot observe a 401 and cannot
invalidate a cached revoked token.

The callback returns the complete header value, must be safe for concurrent
use, and must be non-nil. Invalid names and nil callbacks fail configuration.

For reliable early-revocation handling, use a source implementing the optional
rejection observer or a response-aware transport.

`CredentialScope` is trusted routing output such as an OAuth audience. It is
passed only to the credential source and is excluded from request reports,
logs, and policy fingerprints. Credential selection can therefore vary by a
resolved enterprise route without exposing the full endpoint or coupling
credentials to request middleware.

### 12.3 Authentication retry

On a credential rejection:

1. notify the rejection observer before returning;
2. invalidate cached credentials if supported;
3. do not blindly retry non-idempotent generation;
4. permit at most one immediate refresh-and-retry only when an explicit
   credential policy opts in and the provider response proves the request was
   not accepted;
5. rebuild a fresh transport request and body for that retry.

An observer error is recorded and attached as diagnostic context, but it does
not replace or hide the original provider rejection.

Authentication failure is provider-specific. After local credential handling
is exhausted, a chain may fail over to another independently configured
provider using the existing provider-specific retry classification.

### 12.4 HTTP client injection

Add a chat-level HTTP client option for HTTP-capable providers, with these
semantics:

- the injected `*http.Client` is caller-owned;
- the framework never mutates its timeout, transport, jar, or redirect policy;
- if framework wrappers are required, shallow-copy the client and compose
  around the existing transport;
- a nil transport means `http.DefaultTransport`;
- static eligible headers must wrap rather than replace the existing transport;
- unsupported providers reject the option during validation;
- the option can be scoped to one explicit chain entry;
- credentials and telemetry wrappers have deterministic ordering.

The common request timeout should be enforced with context deadlines so an
injected client does not need to be mutated. A caller-supplied client may still
have its own shorter timeout.

Request signing, mTLS, nonces, and other per-attempt behavior belong in a
`RoundTripper` or provider SDK transport because they may depend on final
serialized bytes and retry time.

### 12.5 Header protection

Protected header conflicts must return actionable configuration errors. They
must not be silently ignored.

The protected set includes:

- `Authorization` when managed by the provider;
- the active custom credential header;
- content-type and streaming protocol headers where required;
- provider-required version/signature headers where application replacement
  would invalidate the request.

The set must be minimal and documented per surface.

---

## 13. Retry and error architecture

### 13.1 Replayable request bodies

The current `BaseClient.ExecuteWithRetry` shallow-clones `http.Request`.
`Clone` does not itself replace a consumed body by calling `GetBody`.
Automatic `GetBody` use by `net/http` for redirects or internal transport
retries does not make a separate application-level `Do` loop replayable.

The architecture therefore treats body replay as a correctness requirement:

~~~text
final immutable body bytes
    -> attempt 1: new request + new reader
    -> attempt 2: new request + new reader
    -> attempt N: new request + new reader
~~~

The contradictory “retry body replay is a nonissue” conclusion in the
enterprise incident note must not guide implementation. A regression test must
prove that a 429/5xx followed by success delivers identical non-empty bytes on
every attempt.

### 13.2 Separate semantic and transport retries

- Provider rules and application middleware run once per logical provider
  attempt.
- Transport retries reuse immutable finalized semantics.
- Credentials may be reacquired only according to the credential policy.
- Per-attempt signing and nonce generation run for every send.
- Non-idempotent application middleware never reruns merely because the network
  retried.

### 13.3 Error classes

Use structured errors to distinguish:

- configuration validation errors: fail before construction or network;
- request-policy errors: fail before network;
- provider-compatible translation adjustments: continue and report;
- provider-specific terminal errors: eligible for chain failover;
- transport, 429, and 5xx errors: eligible for retry/failover;
- malformed portable input: fail fast;
- unknown 400 errors: do not automatically sanitize and retry.

Known provider/model incompatibility responses may be marked provider-specific
for chain failover, but the durable fix remains pre-network policy.

---

## 14. Chain composition

### 14.1 Explicit chain entries

Keep the existing `WithProviderChain` convenience API, but compile it internally
into explicit entries.

Add constructors conceptually equivalent to:

~~~go
type ChainEntry struct {
    // constructed through helpers to avoid ambiguous states
}

func ProviderEntry(name, providerAlias string, opts ...ClientOption) ChainEntry
func ClientEntry(name string, client core.AIClient) ChainEntry
func NewChain(entries ...ChainEntry) (*ChainClient, error)
~~~

This replaces two incomplete proposals—global uniform chain options and a
client-only chain constructor—with one composable model.

Each provider entry can have its own:

- alias and model defaults;
- base endpoint and resolver;
- credential source and HTTP client;
- request rules and middleware;
- pricing resolver;
- timeout and retry budget.

A client entry supports application-local native adapters without process-global
registration.

### 14.2 Chain invariants

1. Every child receives an independent immutable request snapshot.
2. Rules match after that child resolves its own provider, surface, and model.
3. Provider-specific patches never leak to another child.
4. A failed provider cannot mutate input observed by the next provider.
5. Nested maps and slices are recursively copied.
6. Optional request/result and streaming capabilities are preserved.
7. Legacy-only custom clients remain valid but produce a limited report.
8. Duplicate entry names fail validation.
9. Empty or nil client entries fail construction.
10. Per-entry transport and credential state may be shared only when the
    application explicitly supplies a concurrency-safe shared implementation.

### 14.3 Provider registry remains useful

Import-driven registration remains the correct convenience mechanism for
reusable providers. Explicit client entries do not replace it; they complement
it for per-instance and application-local composition.

---

## 15. Provider factory evolution

Do not break `ProviderFactory.Create`.

Add an optional error-capable legacy construction interface and a separate
full-capability construction interface:

~~~go
type ValidatedProviderFactory interface {
    ProviderFactory
    CreateValidated(*AIConfig) (core.AIClient, error)
}

type RequestProviderFactory interface {
    ProviderFactory
    CreateRequestClient(
        *AIConfig,
        ProviderIntegrationConfig,
    ) (core.AIRequestClient, error)
}
~~~

`NewClient` prefers the optional interface and falls back to legacy `Create`.
`NewRequestClient` prefers `RequestProviderFactory`. With a zero integration
config it may fall back to a legacy factory whose returned client already
implements `core.AIRequestClient`; with non-zero integration behavior it
requires the new factory contract. It never passes advanced semantics to a
factory that cannot honor them. Common static validation still occurs before
factory construction.

This supports:

- invalid endpoint configuration;
- malformed selectors or patches;
- unsupported HTTP client injection;
- missing credential strategy;
- invalid model-to-deployment maps;
- SDK initialization errors.

Factories remain construction mechanisms. Hot policy sources, token-refresh
lifecycle, and orchestration behavior stay in injected application-owned
components.

Third-party factories are not required to implement either new interface,
request draft pipeline, or reports. Documentation should distinguish:

- basic compatibility: implements `core.AIClient`;
- full TruvaG3 provider compatibility: also satisfies request-policy,
  streaming, report, error, and telemetry conformance tests.

---

## 16. Result normalization, usage, pricing, and telemetry

### 16.1 Normalized result contract

`AIResult` should carry:

- the legacy normalized `AIResponse`;
- sanitized request report;
- optional normalized usage details;
- optional cost result and pricing source.

`AIUsageDetails` should preserve common evolving counters without forcing
provider-specific logic into orchestration, for example:

- cached input tokens;
- reasoning tokens;
- audio tokens;
- provider-reported categories that can be represented as sanitized numeric
  counters.

Raw provider response bodies do not belong in the normalized result.

An illustrative data shape is:

~~~go
type AIUsageDetails struct {
    CachedInputTokens int64
    ReasoningTokens   int64
    AudioInputTokens  int64
    AudioOutputTokens int64
    Counters          map[string]int64
}

type AICost struct {
    Amount   float64
    Currency string
    Source   string
}
~~~

Unknown counters may be preserved only when they are numeric, sanitized, and
documented as provider-reported. They must not become a raw-response escape
hatch.

### 16.2 Pricing

Move cost resolution out of the stock OpenAI client into common AI
instrumentation.

Use an application-composable resolver keyed by:

- provider;
- provider alias or surface;
- concrete returned/resolved model;
- input/output and eligible detailed token counts.

The resolver is an `ai`-module behavioral plug, not a `core` dependency:

~~~go
type PricingResolver interface {
    Estimate(PricingRequest) (AICost, bool)
}
~~~

It should be deterministic and local; cost lookup must not add a remote network
dependency to the generation critical path.

The built-in pricing table remains the default. Applications can supply pricing
for renamed enterprise deployments without editing framework source.

Unknown pricing is not an error; cost is absent and the request succeeds.

### 16.3 Common instrumentation

Create one logical generation span in common AI instrumentation:

~~~text
ai.generate or ai.stream
    ├── provider preparation
    ├── ai.http_attempt 1
    ├── ai.http_attempt 2
    └── normalization
~~~

Provider adapters contribute provider/model/surface/error details. The common
layer records normalized duration, usage, cost, policy adjustments, and chain
attempt identity.

Custom clients retain observability when:

- they return a normalized response/result; and
- the application wraps them with the common instrumented client.

Do not require every custom provider to duplicate OpenAI-specific cost stamping.

### 16.4 Secret-safe reporting

Record:

- provider, alias, surface, operation, and concrete model;
- rule/middleware identity and changed paths;
- whether portable intent was translated, omitted, or degraded;
- stable policy/route fingerprint;
- retry and chain attempt counts;
- usage and known cost.

Never record through this mechanism:

- credential values or credential headers;
- prompts, system prompts, messages, or raw body values;
- arbitrary provider extras;
- complete endpoints containing tenant or secret query data.

The existing explicitly enabled LLM Debug payload feature remains a separate
operational choice with its own retention and security policy.

---

## 17. Orchestration integration

### 17.1 Ownership

Orchestration remains provider-neutral and depends only on `core`.

It must not:

- match Anthropic or Azure model names;
- build provider patches automatically from provider facts;
- acquire credentials;
- configure HTTP clients;
- choose provider endpoints or deployments;
- import `ai`.

The application constructs the configured AI client and injects it through
`OrchestratorDependencies.AIClient`, preserving the current architecture.

### 17.2 Purpose metadata

Every framework module that makes AI calls should assign a stable,
provider-neutral purpose. Orchestration owns purpose attribution for the calls
it makes, such as:

- `planning`;
- `continuation-planning`;
- `synthesis`;
- `micro-resolution`;
- `semantic-retry`;
- `tiered-selection`;
- `conversation-compaction`;
- `result-distillation`;
- `error-analysis`;
- `knowledge-extraction`;
- `user-memory-extraction`.

Other modules use the same core field without importing orchestration; for
example, the memory module can use `reflection` for its reflection job.

Purpose describes why the model is called, not which provider should handle it.
Applications may use it in request-rule selectors.

### 17.3 Central invocation helper

Orchestration has many direct AI call sites. Introduce an internal helper that:

1. receives purpose, prompt, and phase-specific portable options;
2. creates a `core.AIRequest`;
3. uses `core.AIRequestClient` when the injected client implements it;
4. falls back to legacy `core.AIClient`;
5. records the sanitized report when available;
6. preserves existing LLM Debug deferral/deduplication behavior.

This helper belongs in orchestration and imports only `core`.

### 17.4 Per-phase options

The existing `AIOptionsOverride` pointer fields already distinguish explicit
zero from “leave unchanged,” but they cannot express explicit omission.

Migration should:

- keep current fields and legacy bridging;
- add presence-aware core request parameters to the internal request path;
- allow opaque declarative per-request patches only when explicitly supplied
  by the application;
- avoid duplicating provider request policy in every orchestration phase.

A client-global request rule should cover all orchestration calls by default.

### 17.5 Caching

AI-derived cache entries are valid only under the request policy and route that
produced them.

Add an optional core capability, if required by caches:

~~~go
type AIRequestFingerprinter interface {
    RequestFingerprint(
        ctx context.Context,
        request *AIRequest,
    ) (fingerprint string, stable bool)
}
~~~

Orchestration caches that depend on AI output should:

- include a stable fingerprint in their key;
- bypass the cache when the fingerprint is unstable;
- never use a credential or secret value in the key;
- bypass the affected cache when new request semantics are active but
  fingerprinting is unsupported, while allowing the AI request itself to
  continue.

Legacy clients using only legacy request semantics may retain their existing
cache namespace during migration. Once new rules, routing policy, or middleware
affect output semantics, absence of a stable fingerprint means cache bypass,
not reuse under an incomplete key.

Do not add policy fingerprints to caches whose content is purely structural and
independent of AI generation behavior.

### 17.6 Orchestration impact summary

| Area | Impact |
|---|---|
| Provider-specific code | None |
| Dependency graph | Unchanged |
| AI call construction | Centralized request envelope with legacy fallback |
| Phase configuration | Gains omission-capable portable intent over time |
| LLM Debug | Adds requested/resolved/effective metadata when available |
| Caching | Policy/route fingerprint for AI-semantic caches |
| Tests | Regression coverage across every AI-producing purpose |

The code ownership impact is modest, but the behavioral and observability test
surface is broad.

---

## 18. Backward compatibility and migration

### 18.1 Contracts preserved

The initial implementation must preserve:

- `core.AIClient`;
- `core.StreamingAIClient`;
- `core.AIOptions`;
- `core.AIResponse`;
- `ai.AIConfig`;
- `ai.ProviderFactory`;
- `ai.NewClient`;
- `ai.NewChainClient`;
- `WithExtra` and `WithHeaders`;
- existing provider imports and environment detection;
- current single-client and chain retry defaults.

Legacy protected headers require a migration window: the existing API may keep
ignoring them while emitting an actionable warning, whereas new rules and
credential options reject the conflict immediately. After the documented
deprecation window, protected legacy conflicts should become errors in the next
compatible major release.

### 18.2 New additive contracts

Add, without replacing the old path:

- request/result client capability;
- streaming request/result capability;
- `ClientOption`, `ProviderIntegrationConfig`, and `NewRequestClient`;
- presence-aware request data;
- declarative patches and reports;
- request rules and constrained middleware;
- credential source and endpoint resolver;
- provider-scoped HTTP client configuration;
- explicit chain entries;
- optional validated and request-capable factory interfaces;
- pricing resolver and usage details;
- optional request fingerprinting.

### 18.3 One internal implementation

Built-in provider legacy methods must adapt into the new request pipeline:

~~~text
legacy GenerateResponse
    -> translate legacy options
    -> shared request pipeline
    -> return result.Response

new Generate
    -> shared request pipeline
    -> return full result
~~~

Do not maintain parallel legacy and new provider implementations.

### 18.4 Deprecation posture

Do not immediately deprecate `AIOptions`, `Extra`, or `Headers`.

First:

1. ship the new path;
2. migrate built-in clients and orchestration internally;
3. publish examples;
4. measure adoption;
5. deprecate only redundant unsafe patterns with a documented migration path.

A raw request mutator should never be introduced, so no migration from it is
needed.

---

## 19. Implementation plan

### 19.0 Execution decision — read first

**Status: approved to start (2026-07-18). Strategy: full scope, phased
delivery.** This block is the governing decision for everything below; read it
before opening the first PR.

This section was previously ambiguous between an MVP subset and the full build.
The execution decision is:

- **Scope — full.** Build the complete extensibility model, not a minimal
  subset. This is a foundational framework change with no delivery deadline, so
  the goal is one coherent design rather than a partial landing that forces
  re-opening already-released contracts later.
- **Delivery — phased PRs, not a big-bang merge.** Land the phases as the small,
  independently reviewable PRs in §19.13; every PR keeps the tree compiling and
  passes the full Go gates in `CONTRIBUTING.md`. "No rush" is the argument *for*
  incremental, bisectable delivery — a ~4k-line framework change is the worst
  place to lose a bisectable history or rubber-stamp a 3k-line review.
- **Start at Phase 0B (§19.3).** The Anthropic sampling fix is a live production
  bug, provider-local, and depends on none of the new architecture. Ship it
  first, on its own, before any public-contract work. It is the highest-value
  single change in this plan.
- **Let the enterprise forcing-function validate the contracts.** Phases 3–4
  (construction options, credentials/routing) exercise the new public surface
  against the one real integration. Land them before piling Phases 5–8 on top,
  so the ~55 new exported symbols are validated before they are frozen.
- **Gate the speculative tails on a real consumer.** Full scope does **not**
  mean build-on-spec. Policy **fingerprints + cache integration** (§19.5,
  §19.11), **request middleware** (§19.5), and **non-HTTP / Bedrock drafts**
  (§19.10) have no consumer in the two problems this design exists to solve —
  enterprise gateway integration and Anthropic sampling. Build them only when a
  second real provider or an actual caching need forces them, not because the
  sequence lists them.
- **Phase 0A is defensive hardening, not a live-bug fix (§19.2).** It must not
  be prioritized ahead of 0B or described as fixing current breakage.
- **Phases 1–2 are blueprint-fidelity.** They freeze the largest share of new
  public surface (~55 exported symbols across `core`, `ai`, and two new
  packages); expect their signatures to firm up as they are written and
  validated against Phases 3–4. That sequencing is deliberate, not incidental.

Any change to the ownership, precedence, compatibility, or security properties
in this section must update this decision block and §1 first.

This proposal is actionable. This section is the implementation blueprint, not
additional conceptual API exploration. The snippets show the intended contract
and control flow; an implementation PR may split helpers into smaller files but
should not change the ownership, precedence, compatibility, or security
properties without updating this decision document.

Each PR keeps the tree compiling and is independently releasable. Phase 0B fixes
a live correctness problem (Anthropic sampling, §19.3) and must not wait for the
public architecture; Phase 0A (§19.2) is defensive hardening, not a prerequisite
for 0B.

### 19.1 File ownership and merge order

| Phase | Primary files | Deliverable |
|---|---|---|
| 0A | `ai/providers/base.go`, `base_test.go` | Replayable application-level retries |
| 0B | `ai/providers/anthropic/request_policy.go`, `request_builder.go`, `client.go` | One Anthropic semantic builder for sync and stream |
| 1 | `core/ai_request.go`, `core/errors.go` | Additive request/result capability and legacy adapter |
| 2 | `ai/requestpolicy/*`, provider draft adapters | One validated patch/middleware engine |
| 3 | `ai/provider.go`, `ai/client.go`, `ai/registry.go` | Public construction options and error-capable factories |
| 4 | `ai/integration.go`, HTTP provider factories | Credentials, routing, and caller-owned HTTP clients |
| 5 | `ai/chain_client.go`, `ai/chain_client_test.go` | Explicit heterogeneous chain entries |
| 6 | `ai/instrumented_client.go`, `ai/pricing.go`, `ai/providers/pricing.go` | Common reporting, usage, and pricing |
| 7 | `ai/providerkit/openaiwire/*`, provider adapters | Reusable codecs and SDK logical drafts |
| 8 | `orchestration/ai_invocation.go` and existing AI call sites | Provider-neutral orchestration adoption |
| 9 | `ai/providerkit/openaiwire/*`, `ai/providers/openai/*`, `ai/providers/anthropic/*`, new `ai/providers/azureopenai/*`, cloud contract tests, provider guide | Route-before-draft preparation, typed hosted-provider profiles, Azure v1/classic, Vertex Claude, and verified enterprise recipes |

The dependency order is deliberate:

~~~text
core request/result contracts
        ↓
ai/requestpolicy and provider drafts
        ↓
ai construction, integration, chain, instrumentation
        ↓
orchestration adoption through core only
~~~

### 19.2 Phase 0A — make every HTTP retry body replayable

**Status:** Implemented and locally verified (2026-07-19); pending review and
commit.

This is defensive hardening, not a fix for current breakage. An empirical repro
(clone-per-attempt over a `bytes.Buffer` body, forcing a `500` then a `200`)
confirmed the present code already replays the full body on retry: `http.NewRequest`
sets `GetBody` for buffer/reader bodies and `net/http` rewinds them. The change
matters for a *future non-replayable body* — e.g. a request built from a
non-seekable `io.Reader` — where `http.Request.Clone` copies the body reference
rather than creating a fresh one. `BaseClient` should acquire a new body for
every attempt and fail explicitly when a request with a body is not replayable,
rather than silently sending an empty body.

~~~go
// ai/providers/base.go
func requestForAttempt(
    ctx context.Context,
    request *http.Request,
) (*http.Request, error) {
    clone := request.Clone(ctx)
    if request.Body == nil || request.Body == http.NoBody {
        return clone, nil
    }
    if request.GetBody == nil {
        return nil, fmt.Errorf("AI request body is not replayable")
    }

    body, err := request.GetBody()
    if err != nil {
        return nil, fmt.Errorf("recreate AI request body: %w", err)
    }
    clone.Body = body
    return clone, nil
}

func (b *BaseClient) ExecuteWithRetry(
    ctx context.Context,
    request *http.Request,
) (*http.Response, error) {
    var lastErr error
    for attempt := 0; attempt <= b.MaxRetries; attempt++ {
        attemptCtx, attemptSpan := b.StartSpan(ctx, "ai.http_attempt")

        attemptRequest, err := requestForAttempt(attemptCtx, request)
        if err != nil {
            attemptSpan.RecordError(err)
            attemptSpan.End()
            return nil, err
        }

        // Existing status classification, body closure, telemetry, and backoff
        // continue here, using attemptRequest rather than request.Clone(...).
        response, err := b.HTTPClient.Do(attemptRequest)
        // ...
    }
    return nil, lastErr
}
~~~

All HTTP providers should construct requests from immutable encoded bytes using
`http.NewRequestWithContext`; for `bytes.Reader` and `bytes.Buffer`, Go supplies
`GetBody`. The retry test must return `500` once and `200` next, capture both
bodies, and assert that both equal the original non-empty bytes. A second test
must prove a non-replayable body fails before the first network attempt rather
than sending an empty request.

### 19.3 Phase 0B — converge Anthropic sync and stream preparation

**Status:** Implemented, locally verified, and committed as `676da81`
(2026-07-19).

The immediate compatibility table is provider-owned and applies after concrete
model resolution. A tri-state result prevents an unknown future model from
being silently treated as known-compatible.

~~~go
// ai/providers/anthropic/request_policy.go
type samplingPolicy uint8

const (
    samplingUnknown samplingPolicy = iota
    samplingAllowed
    samplingOmitted
)

var omitSamplingPrefixes = []string{
    "claude-opus-4-7",
    "claude-opus-4-8",
    "claude-sonnet-5",
    "claude-fable-5",
    "claude-mythos-5",
    "claude-mythos-preview",
}

func samplingPolicyForModel(model string) samplingPolicy {
    normalized := strings.ToLower(strings.TrimSpace(model))
    for _, prefix := range omitSamplingPrefixes {
        if modelInFamily(normalized, prefix) {
            return samplingOmitted
        }
    }
    if modelInFamily(normalized, "claude-sonnet-4-6") ||
        modelInFamily(normalized, "claude-haiku-4-5") {
        return samplingAllowed
    }
    return samplingUnknown
}

func modelInFamily(normalizedModel, normalizedFamily string) bool {
    return normalizedModel == normalizedFamily ||
        strings.HasPrefix(normalizedModel, normalizedFamily+"-")
}

func deleteKeyFold(body map[string]interface{}, names ...string) []string {
	var removed []string
	for _, name := range names {
		found := false
		for key := range body {
			if strings.EqualFold(key, name) {
				delete(body, key)
				found = true
			}
		}
		if found {
			removed = append(removed, "/"+name)
		}
	}
	return removed
}
~~~

Sync and stream then call one preparation function. It clones caller-owned
options, merges legacy extras first, and applies the non-bypassable model rule
last so `Extra` cannot reinsert an invalid field.

~~~go
// ai/providers/anthropic/request_builder.go
type preparedRequest struct {
    Model       string
    Body        []byte
    Headers     http.Header
    Adjustments []requestAdjustment
}

// Phase 0 keeps this provider-local so the incident fix does not depend on
// the Phase 1 public core contracts. Phase 1 maps it to AIRequestAdjustment.
type requestAdjustment struct {
    Rule   string
    Path   string
    Action string
    Reason string
}

func (c *Client) prepareRequest(
    prompt string,
    supplied *core.AIOptions,
    stream bool,
) (*preparedRequest, error) {
    options, err := providers.CloneAIOptions(supplied)
    if err != nil {
        return nil, err
    }
    options = c.ApplyDefaults(options)
    options.Model = resolveModel(options.Model)

    body := map[string]interface{}{
        "model":       options.Model,
        "messages":    []Message{{Role: "user", Content: prompt}},
        "max_tokens":  options.MaxTokens,
        "temperature": options.Temperature,
    }
    if options.SystemPrompt != "" {
        body["system"] = options.SystemPrompt
    }
    if stream {
        body["stream"] = true
    }
    if options.ResponseFormat != "" {
        body["response_format"] = options.ResponseFormat
    }
    for key, value := range providers.MergeAnyMaps(c.defaultExtra, options.Extra) {
        if _, structural := body[key]; !structural {
            body[key] = value
        }
    }

	var adjustments []requestAdjustment
	if samplingPolicyForModel(options.Model) == samplingOmitted {
		removedPaths := deleteKeyFold(body, "temperature", "top_p", "top_k")
		for _, path := range removedPaths {
            adjustments = append(adjustments, requestAdjustment{
                Rule:   "anthropic-adaptive-thinking-sampling-v1",
                Path:   path,
                Action: "remove",
                Reason: "resolved model rejects explicit sampling controls",
            })
        }
    }

    encoded, err := json.Marshal(body)
    if err != nil {
        return nil, fmt.Errorf("marshal Anthropic request: %w", err)
    }

    headers := make(http.Header)
    headers.Set("Content-Type", "application/json")
    headers.Set("x-api-key", c.apiKey)
    headers.Set("anthropic-version", APIVersion)
    if stream {
        headers.Set("Accept", "text/event-stream")
    }
    providers.ApplyLegacyHeaders(
        headers,
        anthropicProtectedHeaders(),
        c.defaultHeaders,
        options.Headers,
    )

    return &preparedRequest{
        Model: options.Model, Body: encoded, Headers: headers,
        Adjustments: adjustments,
    }, nil
}
~~~

Both public paths become thin execution/decoding adapters:

~~~go
prepared, err := c.prepareRequest(prompt, options, false) // true for stream
if err != nil {
    return nil, err
}
request, err := http.NewRequestWithContext(
    ctx,
    http.MethodPost,
    c.baseURL+"/messages",
    bytes.NewReader(prepared.Body),
)
if err != nil {
    return nil, err
}
request.Header = prepared.Headers.Clone()
response, err := c.ExecuteWithRetry(ctx, request)
~~~

The sync/stream parity test should inspect captured requests and assert equal
model resolution, defaults, extras, protected headers, compatibility
adjustments, and body fields other than `stream`. Caller options and nested
extras must remain byte-for-byte logically unchanged after both calls.
Unknown models preserve the legacy sampling behavior in Phase 0 and emit a
sanitized debug classification; they are not silently added to the allowlist.
`ApplyLegacyHeaders` preserves the existing protected-header ignore behavior
and adds the migration warning described in Section 18. New request rules and
credential options use the checked path and fail on protected conflicts.

### 19.4 Phase 1 — add the core request/result capability

**Status:** Implemented, locally verified, and committed as `2c62676`
(2026-07-19).

Add new contracts without changing `AIClient`, `AIOptions`, `AIResponse`, or
`StreamingAIClient`. Evolving structs use keyed literals only.

~~~go
// core/ai_request.go
type noUnkeyedLiterals struct{}

type AIParameterMode uint8

const (
    AIParameterInherit AIParameterMode = iota
    AIParameterSet
    AIParameterOmit
)

type AIParameter[T any] struct {
    _     noUnkeyedLiterals
    Mode  AIParameterMode
    Value T
}

func InheritAIParameter[T any]() AIParameter[T] {
    return AIParameter[T]{Mode: AIParameterInherit}
}

func SetAIParameter[T any](value T) AIParameter[T] {
    return AIParameter[T]{Mode: AIParameterSet, Value: value}
}

func OmitAIParameter[T any]() AIParameter[T] {
    return AIParameter[T]{Mode: AIParameterOmit}
}

type AIRequest struct {
    _          noUnkeyedLiterals
    Prompt     string
    Purpose    string
    Generation AIGenerationOptions
    Patches    []AIProviderPatch

    legacyOptions *AIOptions
}

type AIResult struct {
    _             noUnkeyedLiterals
    Response      *AIResponse
    RequestReport *AIRequestReport
    UsageDetails  *AIUsageDetails
    Cost          *AICost
}

type AIRequestClient interface {
    AIClient
    Generate(context.Context, *AIRequest) (*AIResult, error)
}

type StreamingAIRequestClient interface {
    AIRequestClient
    Stream(
        context.Context,
        *AIRequest,
        StreamCallback,
    ) (*AIResult, error)
}
~~~

`AIGenerationOptions`, `AIProviderSelector`, `AIProviderPatch`,
`AIRequestReport`, `AIUsageDetails`, and `AICost` use the fields defined in
Sections 7 and 16 and the same keyed-literal guard.

The legacy constructor owns the only bridge from legacy extras and headers:

~~~go
func NewAIRequest(prompt, purpose string) *AIRequest {
    return &AIRequest{Prompt: prompt, Purpose: purpose}
}

func NewAIRequestFromLegacy(
    prompt string,
    purpose string,
    options *AIOptions,
) *AIRequest {
    return &AIRequest{
        Prompt:        prompt,
        Purpose:       purpose,
        legacyOptions: cloneLegacyAIOptions(options),
    }
}

// LegacyOptions returns an isolated copy for provider and fallback adapters.
// It is not an application mutation surface.
func (r *AIRequest) LegacyOptions() *AIOptions {
    if r == nil {
        return nil
    }
    return cloneLegacyAIOptions(r.legacyOptions)
}

func CloneAIRequest(request *AIRequest) (*AIRequest, error) {
    if request == nil {
        return nil, errors.New("AI request is nil")
    }
    patches, err := cloneProviderPatches(request.Patches)
    if err != nil {
        return nil, fmt.Errorf("clone AI request patches: %w", err)
    }
    clone := *request
    clone.Patches = patches
    clone.legacyOptions = cloneLegacyAIOptions(request.legacyOptions)
    return &clone, nil
}
~~~

Core also supplies the provider-neutral capability adapter used by
orchestration and chains:

~~~go
// core/errors.go
var ErrAIRequestFeatureUnsupported =
    errors.New("AI request feature is unsupported by this client")

type AIRequestFeatureError struct {
    ClientType string
    Feature    string
}

func (e *AIRequestFeatureError) Error() string {
    return fmt.Sprintf(
        "%s does not support AI request feature %q",
        e.ClientType,
        e.Feature,
    )
}

func (e *AIRequestFeatureError) Is(target error) bool {
    return target == ErrAIRequestFeatureUnsupported
}

// core/ai_request.go
func GenerateAI(
    ctx context.Context,
    client AIClient,
    request *AIRequest,
) (*AIResult, error) {
    if client == nil {
        return nil, errors.New("AI client is nil")
    }
    if request == nil {
        return nil, errors.New("AI request is nil")
    }
    if advanced, ok := client.(AIRequestClient); ok {
        return advanced.Generate(ctx, request)
    }

    if feature := request.firstUnsupportedLegacyFeature(); feature != "" {
        return nil, &AIRequestFeatureError{
            ClientType: fmt.Sprintf("%T", client),
            Feature:    feature,
        }
    }
    options := request.toLegacyOptions()
    response, err := client.GenerateResponse(ctx, request.Prompt, options)
    if err != nil {
        return nil, err
    }
    if response == nil {
        return nil, errors.New("AI client returned a nil response without error")
    }
    return &AIResult{
        Response: response,
        RequestReport: &AIRequestReport{
            Provider:      response.Provider,
            ResolvedModel: response.Model,
            Purpose:       request.Purpose,
            Stable:        false,
        },
    }, nil
}
~~~

`firstUnsupportedLegacyFeature` detects `Omit`, `TopP`, `TopK`, provider
patches, and explicit zero or empty `Set` values whose legacy meaning is
inherit, because a legacy-only client cannot prove that it honored them.
`toLegacyOptions` then overlays only representable presence-aware set values on
the cloned legacy options. This prevents a capability fallback from silently
discarding new semantics. The limited fallback report is deliberately unstable
because a legacy client cannot fingerprint the effective provider request.

Built-in provider legacy methods are then inverted to use the new path:

~~~go
func (c *Client) GenerateResponse(
    ctx context.Context,
    prompt string,
    options *core.AIOptions,
) (*core.AIResponse, error) {
    result, err := c.Generate(
        ctx,
        core.NewAIRequestFromLegacy(prompt, "", options),
    )
    if result != nil && result.Response != nil {
        return result.Response, err
    }
    return nil, err
}
~~~

### 19.5 Phase 2 — implement one request-policy engine

**Entry gate:** Before adding the policy engine's third clone routine, align
`ai/providers.CloneAIOptions` with the Core clone for every supported acyclic
JSON-compatible map, slice, array, and named scalar shape, then add shared
conformance fixtures covering both routines. Extend the same fixtures to the
policy clone as it is introduced. Opaque legacy leaves may remain shared only
where the documented backward-compatibility contract permits it.

Create `ai/requestpolicy` with no dependency on the root `ai` package. Providers
adapt their logical requests to this interface:

~~~go
// ai/requestpolicy/types.go
type Draft interface {
    Info() RequestInfo
    Get(path string) (interface{}, bool)
    Set(path string, value interface{}) error
    Remove(path string) error
    SetHeader(name, value string) error
    RemoveHeader(name string) error
    Validate() error
}

type RequestMiddleware interface {
    Name() string
    Version() string
    Apply(context.Context, RequestEditor) error
}

type Engine struct {
    builtIns   []core.AIProviderPatch
    appRules   []core.AIProviderPatch
    middleware []RequestMiddleware
    mode       CompatibilityMode
}

func NewEngine(config Config) (*Engine, error) {
    snapshot, err := validateAndCloneConfig(config)
    if err != nil {
        return nil, err
    }
    return &Engine{
        builtIns:   snapshot.BuiltIns,
        appRules:   snapshot.AppRules,
        middleware: snapshot.Middleware,
        mode:       snapshot.Mode,
    }, nil
}

func (e *Engine) Apply(
    ctx context.Context,
    draft Draft,
    perRequest []core.AIProviderPatch,
) (*core.AIRequestReport, error) {
    if draft == nil {
        return nil, errors.New("request policy draft is nil")
    }
    editor := newTrackingEditor(draft)

    for _, rule := range e.builtIns {
        if matches(rule.Selector, draft.Info()) {
            if err := editor.applyPatch(rule, "built-in-rule"); err != nil {
                return editor.report(), err
            }
        }
    }
    for _, rule := range e.appRules {
        if matches(rule.Selector, draft.Info()) {
            if err := editor.applyPatch(rule, "app-rule"); err != nil {
                return editor.report(), err
            }
        }
    }
    for _, middleware := range e.middleware {
        if err := editor.applyMiddleware(ctx, middleware); err != nil {
            return editor.report(), err
        }
    }
    for _, patch := range perRequest {
        if matches(patch.Selector, draft.Info()) {
            if err := editor.applyPatch(patch, "request-patch"); err != nil {
                return editor.report(), err
            }
        }
    }
    if err := editor.validateCompatibility(e.mode); err != nil {
        return editor.report(), err
    }
    if err := draft.Validate(); err != nil {
        return editor.report(), err
    }
    return editor.finalReport(e.fingerprint(perRequest)), nil
}
~~~

Selector validation must be bounded and deterministic. Model matching should
use an anchored, escaped `*` glob rather than accepting arbitrary regular
expressions:

~~~go
func compileModelGlob(pattern string) (*regexp.Regexp, error) {
    if len(pattern) > 256 {
        return nil, errors.New("model selector exceeds 256 bytes")
    }
    quoted := regexp.QuoteMeta(strings.ToLower(pattern))
    expression := "^" + strings.ReplaceAll(quoted, `\*`, ".*") + "$"
    return regexp.Compile(expression)
}

func validateSelector(selector core.AIProviderSelector) error {
    scoped := selector.Provider != "" ||
        selector.ProviderAlias != "" ||
        selector.Surface != "" ||
        selector.Model != ""
    if !scoped && !selector.AllProviders {
        return errors.New(
            "request rule requires provider, alias, surface, or model; " +
                "set AllProviders explicitly for a global rule",
        )
    }
    return nil
}
~~~

The tracking editor recursively clones JSON-compatible maps/slices,
canonicalizes header names case-insensitively, rejects protected paths and
headers, records changed paths without values, and computes the policy
fingerprint from identities and deterministic structure only. Provider drafts
expose protected fields through validation; the engine never receives
credentials or a mutable `http.Request`.

Every public rule and per-request patch must have a non-empty `Name` and
`Version`. Those identities are part of the fingerprint and the version must
change whenever set/remove semantics or values change. Arbitrary field values
are not copied into reports or blindly hashed. Middleware that cannot declare a
stable versioned semantic identity makes the report unstable; affected caches
bypass rather than reuse an incomplete key.

`ai/providers/request_options.go` should contain the common provider-side
deep-copy routine used by legacy provider adapters, the Anthropic builder, and
other built-in providers. The policy engine owns the equivalent routine within
its dependency-neutral package. The core request clone owns a third, small
dependency-free copy because `core` cannot import `ai`.

All three recursively copy JSON-compatible maps and slices. New policy APIs
reject unsupported runtime values with a path-qualified error. For backward
compatibility, legacy `Extra` cloning retains opaque leaf values by reference
but the framework never mutates them; this limitation is documented. Shared
conformance fixtures must prove that the three routines agree for every
supported acyclic JSON-compatible value. New policy and patch clone paths
reject cycles deterministically. The compatibility-only legacy clone must at
least terminate safely for cyclic or opaque graphs and leave the existing
provider encoder responsible for rejecting values it cannot serialize.

### 19.6 Phase 3 — expose construction options without breaking factories

Do not add advanced fields to the already-exported `AIConfig`. External code
can legally use unkeyed literals today, so adding fields would create the
source-compatibility risk identified in Section 22. Keep `NewClient` and
`AIOption` unchanged and add a sealed superset option accepted by the new
constructor. Existing `AIOption` values implement it automatically.

~~~go
// ai/provider.go
type ProviderIntegrationConfig struct {
    _                  noUnkeyedLiterals
    RequestRules      []core.AIProviderPatch
    RequestMiddleware []requestpolicy.RequestMiddleware
    CompatibilityMode requestpolicy.CompatibilityMode

    CredentialSource CredentialSource
    EndpointResolver EndpointResolver
    HTTPClient       *http.Client
    PricingResolver  PricingResolver
}

type clientConfig struct {
    legacy      AIConfig
    integration ProviderIntegrationConfig

    credentialSourceSet bool
    endpointResolverSet bool
    httpClientSet       bool
    pricingResolverSet  bool
}

// ClientOption is sealed intentionally. Applications compose values returned
// by WithXXX helpers; arbitrary option implementations cannot retain config.
type ClientOption interface {
    applyClient(*clientConfig) error
}

type clientOptionFunc func(*clientConfig) error

func (option clientOptionFunc) applyClient(config *clientConfig) error {
    return option(config)
}

// AIOption remains its existing function type and therefore keeps NewClient
// source compatible. Adding this method lets WithProvider, WithModel, and every
// existing AI option be passed directly to NewRequestClient.
func (option AIOption) applyClient(config *clientConfig) error {
    if option == nil {
        return errors.New("AI option is nil")
    }
    option(&config.legacy)
    return nil
}

func WithRequestRules(rules ...core.AIProviderPatch) ClientOption {
    copied, cloneErr := requestpolicy.ClonePatches(rules)
    return clientOptionFunc(func(config *clientConfig) error {
        if cloneErr != nil {
            return cloneErr
        }
        config.integration.RequestRules =
            append(config.integration.RequestRules, copied...)
        return nil
    })
}

func WithRequestMiddleware(
    middleware ...requestpolicy.RequestMiddleware,
) ClientOption {
    copied := append([]requestpolicy.RequestMiddleware(nil), middleware...)
    return clientOptionFunc(func(config *clientConfig) error {
        config.integration.RequestMiddleware =
            append(config.integration.RequestMiddleware, copied...)
        return nil
    })
}

func WithCompatibilityMode(
    mode requestpolicy.CompatibilityMode,
) ClientOption {
    return clientOptionFunc(func(config *clientConfig) error {
        if !mode.Valid() {
            return fmt.Errorf("invalid AI compatibility mode %d", mode)
        }
        config.integration.CompatibilityMode = mode
        return nil
    })
}

func WithHTTPClient(client *http.Client) ClientOption {
    return clientOptionFunc(func(config *clientConfig) error {
        config.integration.HTTPClient = client
        config.httpClientSet = true
        return nil
    })
}

func WithPricingResolver(resolver PricingResolver) ClientOption {
    return clientOptionFunc(func(config *clientConfig) error {
        config.integration.PricingResolver = resolver
        config.pricingResolverSet = true
        return nil
    })
}
~~~

The closure returned by every new option copies captured values, may return a
static validation/clone error, and must not retain the config or perform I/O.
Common and provider-specific validation runs after all options are applied so
it sees final precedence. Presence flags distinguish “option not supplied”
from an explicitly supplied nil implementation.

Existing map-taking options such as `WithHeaders` should also snapshot inputs
when the option is created; that is a backward-compatible isolation fix.
Factories and clients likewise snapshot `AIConfig.Headers`, `Extra`, integration
rules, and every other retained collection. No runtime request reads a
caller-mutable configuration map or slice.

Keep `ProviderFactory.Create` and add the optional interface:

~~~go
type ValidatedProviderFactory interface {
    ProviderFactory
    CreateValidated(*AIConfig) (core.AIClient, error)
}

type RequestProviderFactory interface {
    ProviderFactory
    CreateRequestClient(
        *AIConfig,
        ProviderIntegrationConfig,
    ) (core.AIRequestClient, error)
}

func createFromFactory(
    factory ProviderFactory,
    config *AIConfig,
) (core.AIClient, error) {
    if validated, ok := factory.(ValidatedProviderFactory); ok {
        client, err := validated.CreateValidated(config)
        if err != nil {
            return nil, err
        }
        if client == nil {
            return nil, errors.New("provider factory returned a nil client")
        }
        return client, nil
    }

    client := factory.Create(config)
    if client == nil {
        return nil, errors.New("provider factory returned a nil client")
    }
    return client, nil
}

func NewRequestClient(
    options ...ClientOption,
) (core.AIRequestClient, error) {
    config := newClientConfigWithDefaults()
    for index, option := range options {
        if option == nil {
            return nil, fmt.Errorf("client option %d is nil", index)
        }
        if err := option.applyClient(config); err != nil {
            return nil, fmt.Errorf("apply client option %d: %w", index, err)
        }
    }

    factory, err := resolveProviderFactory(&config.legacy)
    if err != nil {
        return nil, err
    }
    integration, err := validateAndSnapshotIntegration(config)
    if err != nil {
        return nil, err
    }
    requestFactory, ok := factory.(RequestProviderFactory)
    if !ok {
        if !integrationIsZero(integration) {
            return nil, fmt.Errorf(
                "%w: provider factory %T cannot accept integration options",
                core.ErrAIRequestFeatureUnsupported,
                factory,
            )
        }
        legacyClient, err := createFromFactory(factory, &config.legacy)
        if err != nil {
            return nil, err
        }
        requestClient, requestCapable :=
            legacyClient.(core.AIRequestClient)
        if !requestCapable {
            return nil, fmt.Errorf(
                "%w: provider client %T",
                core.ErrAIRequestFeatureUnsupported,
                legacyClient,
            )
        }
        return requestClient, nil
    }
    client, err := requestFactory.CreateRequestClient(
        &config.legacy,
        integration,
    )
    if err != nil {
        return nil, err
    }
    if client == nil {
        return nil, errors.New("provider factory returned a nil request client")
    }
    return client, nil
}
~~~

`NewClient` keeps its current option/env/default precedence, performs common
static validation, and calls `createFromFactory`.
`newClientConfigWithDefaults` and `resolveProviderFactory` are extracted from
the current `NewClient` implementation so both constructors use exactly the
same defaults, explicit-option/env precedence, alias resolution, provider
detection, logging, and registry lookup. Existing third-party factories and
all calls to `NewClient` compile unchanged. The current `MustNewClient` behavior
also remains unchanged; a matching `MustNewRequestClient` is optional
convenience, not a requirement for the first release.

Factories remain static wiring only: they validate whether their provider
supports the configured primitives and pass application-owned policy,
credential, route, transport, and pricing components to the constructed client.
They do not invoke those components, refresh them, or assume ownership of their
runtime lifecycle. This preserves the framework rule that the application
composes behavioral plugs and the factory stays dumb.

### 19.7 Phase 4 — implement enterprise credentials, routing, and HTTP injection

The root `ai` package owns these behavioral integration contracts:

~~~go
// ai/integration.go
type noUnkeyedLiterals struct{}

type HeaderCredential struct {
    _     noUnkeyedLiterals
    Name  string
    Value string
}

func NewHeaderCredential(name, value string) HeaderCredential {
    return HeaderCredential{Name: name, Value: value}
}

type CredentialSource interface {
    Credential(
        context.Context,
        CredentialRequest,
    ) (HeaderCredential, error)
}

type CredentialRejectionObserver interface {
    CredentialRejected(
        context.Context,
        CredentialRequest,
        int,
    ) error
}

type EndpointResolver interface {
    ResolveEndpoint(
        context.Context,
        EndpointRequest,
    ) (ResolvedEndpoint, error)
}

type ResolvedEndpoint struct {
    _               noUnkeyedLiterals
    URL             *url.URL
    Deployment      string
    RouteIdentity   string
    CredentialScope string
    Query           url.Values
}

type AuthHeaderFunc func(context.Context) (string, error)

func WithCredentialSource(source CredentialSource) ClientOption {
    return clientOptionFunc(func(config *clientConfig) error {
        config.integration.CredentialSource = source
        config.credentialSourceSet = true
        return nil
    })
}

func WithEndpointResolver(resolver EndpointResolver) ClientOption {
    return clientOptionFunc(func(config *clientConfig) error {
        config.integration.EndpointResolver = resolver
        config.endpointResolverSet = true
        return nil
    })
}

func WithAuthHeader(name string, value AuthHeaderFunc) ClientOption {
    return clientOptionFunc(func(config *clientConfig) error {
        config.integration.CredentialSource =
            callbackCredentialSource{name: name, value: value}
        config.credentialSourceSet = true
        return nil
    })
}
~~~

Configuration validation rejects an explicitly nil credential source, endpoint
resolver, HTTP client, or pricing resolver; a nil auth callback; an invalid
header name; an empty credential value; an unsupported provider option; a
protected-header collision; an invalid endpoint scheme; or a route without a
stable non-secret identity.
Interface validation is typed-nil-aware so a nil pointer stored inside an
interface cannot pass construction and panic on first use.
Credential acquisition occurs after semantic policy validation and once per
transport attempt when refresh behavior requires it.
Factories defensively clone `ResolvedEndpoint.URL` and `Query` before retaining
or using them so resolver-owned state cannot be mutated by the framework and
cannot mutate an in-flight request.

Caller-owned HTTP clients are never mutated:

~~~go
func providerHTTPClient(client *http.Client) *http.Client {
    // validateAndSnapshotIntegration has already rejected an explicitly nil
    // WithHTTPClient value. Nil here means the option was not supplied.
    if client == nil {
        return &http.Client{}
    }
    clone := *client
    if clone.Transport == nil {
        clone.Transport = http.DefaultTransport
    }
    return &clone
}
~~~

The framework timeout wraps the logical call with `context.WithTimeout`; it
does not overwrite `http.Client.Timeout`. Static application headers are
applied during request preparation, and the custom `RoundTripper` receives the
final body and headers so mTLS/signing transports continue to work. The current
OpenAI header transport should be removed once this path is adopted because it
can replace or bypass application transport composition.

On `401`/`403`, notify `CredentialRejectionObserver` if implemented. Preserve
the original provider error even if notification fails. Immediate re-auth is a
separate explicit option, capped at one replay, and permitted only where the
provider proves the generation was not accepted; ordinary chain failover
remains the default safe behavior.

~~~go
func observeCredentialRejection(
    ctx context.Context,
    source CredentialSource,
    request CredentialRequest,
    statusCode int,
    logger core.Logger,
) {
    if source == nil {
        return
    }
    if statusCode != http.StatusUnauthorized &&
        statusCode != http.StatusForbidden {
        return
    }
    observer, ok := source.(CredentialRejectionObserver)
    if !ok {
        return
    }
    if err := observer.CredentialRejected(ctx, request, statusCode); err != nil {
        // Diagnostic only: the caller still receives the original provider
        // rejection, and secrets from err are not attached to the report.
        logger.WarnWithContext(ctx, "credential rejection observer failed",
            map[string]interface{}{
                "provider":    request.Provider,
                "status_code": statusCode,
            },
        )
    }
}
~~~

The caller invokes this helper before closing/returning the rejection response.
The source is nil-checked before the type assertion, and the logger is always a
no-op implementation rather than nil at this internal boundary.

### 19.8 Phase 5 — make chains heterogeneous and request-aware

Represent chain entries explicitly while preserving the existing
`NewChainClient` API as a compiler into provider entries.

~~~go
// ai/chain_client.go
type chainEntryKind uint8

const (
    chainRequestProvider chainEntryKind = iota
    chainLegacyProvider
    chainInjectedClient
)

type ChainEntry struct {
    name          string
    providerAlias string
    options       []ClientOption
    legacyOptions []AIOption
    client        core.AIClient
    kind          chainEntryKind
}

func ProviderEntry(
    name string,
    providerAlias string,
    options ...ClientOption,
) ChainEntry {
    return ChainEntry{
        name:          name,
        providerAlias: providerAlias,
        options:       append([]ClientOption(nil), options...),
        kind:          chainRequestProvider,
    }
}

func ClientEntry(name string, client core.AIClient) ChainEntry {
    return ChainEntry{
        name: name, client: client, kind: chainInjectedClient,
    }
}

func legacyProviderEntry(
    name string,
    providerAlias string,
    options ...AIOption,
) ChainEntry {
    return ChainEntry{
        name:          name,
        providerAlias: providerAlias,
        legacyOptions: append([]AIOption(nil), options...),
        kind:          chainLegacyProvider,
    }
}

func NewChain(entries ...ChainEntry) (*ChainClient, error) {
    validated, err := validateAndCopyEntries(entries)
    if err != nil {
        return nil, err
    }
    materialized, err := materializeEntries(validated)
    if err != nil {
        return nil, err
    }
    return &ChainClient{entries: materialized}, nil
}
~~~

For public provider entries, `materializeEntries` applies copied per-entry
options, forces `WithProviderAlias(providerAlias)` last, and constructs the
child through `NewRequestClient`. The existing `NewChainClient` convenience
compiler creates internal `chainLegacyProvider` entries instead and uses
`NewClient`, preserving legacy third-party factories exactly. It first copies
chain defaults into each child and then forces the alias.

Injected client entries are caller-owned: the chain must not mutate them
through `SetLogger`, `SetTelemetry`, or other optional setters. Both provider
entry kinds are framework-managed and may receive normal component
configuration.

~~~go
func (c *ChainClient) Generate(
    ctx context.Context,
    request *core.AIRequest,
) (*core.AIResult, error) {
    var failures []error
    var lastResult *core.AIResult
    for _, entry := range c.entries {
        attempt, cloneErr := core.CloneAIRequest(request)
        if cloneErr != nil {
            return lastResult, cloneErr
        }
        result, err := core.GenerateAI(ctx, entry.client, attempt)
        if err == nil {
            return result, nil
        }
        lastResult = result
        failures = append(failures, annotateChainError(entry.name, err))
        if !shouldFailOver(err) {
            return result, err
        }
    }
    return lastResult, errors.Join(failures...)
}

func (c *ChainClient) GenerateResponse(
    ctx context.Context,
    prompt string,
    options *core.AIOptions,
) (*core.AIResponse, error) {
    result, err := c.Generate(
        ctx,
        core.NewAIRequestFromLegacy(prompt, "", options),
    )
    if result != nil {
        return result.Response, err
    }
    return nil, err
}
~~~

`CloneAIRequest` recursively copies the legacy bridge, patches, selectors,
headers, and supported nested values. It is intentionally in `core` so chains
and provider-neutral callers do not need an `ai` import. The actual chain
implementation must preserve a useful sanitized report returned with an error
and attach the chain entry name and attempt number without discarding the
provider report.

Streaming follows the same entry loop and may fail over only before a chunk is
delivered; once output is visible, switching providers would corrupt stream
semantics. A legacy-only entry may be used for a legacy request. It must be
skipped with an unsupported-capability failure—not called with degraded
semantics—when the request contains features it cannot honor.

### 19.9 Phase 6 — centralize usage, pricing, and logical instrumentation

Pricing becomes an application-composable, synchronous lookup:

~~~go
// ai/pricing.go
type PricingRequest struct {
    Provider      string
    ProviderAlias string
    Surface       string
    Model         string
    Usage         core.TokenUsage
    Details       *core.AIUsageDetails
}

type PricingResolver interface {
    Estimate(PricingRequest) (core.AICost, bool)
}

type CompositePricingResolver []PricingResolver

func (r CompositePricingResolver) Estimate(
    request PricingRequest,
) (core.AICost, bool) {
    for _, resolver := range r {
        if resolver == nil {
            continue
        }
        if cost, ok := resolver.Estimate(request); ok {
            return cost, true
        }
    }
    return core.AICost{}, false
}
~~~

The built-in table is the final resolver. Application pricing is prepended, so
enterprise deployment names can be handled without changing framework source.
Unknown price returns no cost and never fails generation.

The common wrapper implements the new request capability and delegates through
the capability adapter:

~~~go
type InstrumentedAIClient struct {
    wrapped       core.AIClient
    recorder      telemetry.LLMCallRecorder
    logger        core.Logger
    componentName string
    defaultType   string
    debugWg       sync.WaitGroup
    pricing       PricingResolver
}

func WithInstrumentedPricingResolver(
    resolver PricingResolver,
) InstrumentedOption {
    return func(client *InstrumentedAIClient) {
        client.pricing = resolver
    }
}

func (c *InstrumentedAIClient) Generate(
    ctx context.Context,
    request *core.AIRequest,
) (result *core.AIResult, err error) {
    if request == nil {
        return nil, errors.New("AI request is nil")
    }
    ctx, span := c.startLogicalSpan(ctx, "ai.generate", request.Purpose)
    defer func() {
        c.finishLogicalSpan(span, result, err)
    }()

    result, err = core.GenerateAI(ctx, c.wrapped, request)
    if result == nil || result.Response == nil {
        return result, err
    }
    if c.pricing != nil {
        if cost, ok := c.pricing.Estimate(pricingRequest(result)); ok {
            copy := *result
            copy.Cost = &cost
            result = &copy
        }
    }
    return result, err
}
~~~

`finishLogicalSpan` attaches only the report’s sanitized identities and changed
paths. The wrapper exposes the request capability and delegates through
`core.GenerateAI`, so a representable request can still use a legacy wrapped
client while unsupported advanced semantics fail explicitly. Its legacy method
adapts through `NewAIRequestFromLegacy`.
Provider-local network attempt spans remain below the logical span; duplicated
provider-local cost stamping is removed only after telemetry compatibility
tests pass.

For provider-factory clients, `NewClient` composes the application resolver
ahead of the built-in resolver and passes that composite to the common
normalized-result path. Factories only wire the resolver; provider transports
do not perform pricing lookup. Application-local custom clients get identical
behavior by using `NewInstrumentedClient` with
`WithInstrumentedPricingResolver`.

### 19.10 Phase 7 — extract codecs and support non-HTTP drafts

Move reusable OpenAI wire behavior into a narrowly scoped provider extension
package:

~~~go
// ai/providerkit/openaiwire/codec.go
type Codec interface {
    BuildDraft(
        request *core.AIRequest,
        resolvedModel string,
        stream bool,
    ) (*Draft, error)
    Encode(draft *Draft) ([]byte, error)
    Decode(response io.Reader) (*core.AIResult, error)
    DecodeStream(
        response io.Reader,
        callback core.StreamCallback,
    ) (*core.AIResult, error)
    SurfaceVersion() string
}

func NewCodec(surfaceVersion string) (Codec, error) {
    if strings.TrimSpace(surfaceVersion) == "" {
        return nil, errors.New("OpenAI wire surface version is required")
    }
    return &codec{surfaceVersion: surfaceVersion}, nil
}
~~~

The package is public and narrowly scoped so application-local and third-party
enterprise adapters can import it; placing it under Go's `internal` boundary
would defeat that goal. It imports `core` and `ai/requestpolicy`, but not the
root `ai` package, preventing a cycle. The stock OpenAI provider composes this
codec with its endpoint resolver, credential source, and provider identity.
Enterprise adapters may reuse the codec without masquerading as stock OpenAI.
Reusable adapters can register a `RequestProviderFactory`; application-local
adapters can be constructed directly and inserted with `ClientEntry`.
Azure gets a first-class adapter only when its deployment/API-version behavior
cannot be represented by route composition.

Bedrock implements the same policy `Draft` over a logical map, validates it,
then translates it to SDK input. It does not serialize an artificial HTTP JSON
request merely to reuse the engine:

~~~go
logicalDraft := bedrock.NewDraft(resolvedModel, request)
report, err := policy.Apply(ctx, logicalDraft, request.Patches)
if err != nil {
    return &core.AIResult{RequestReport: report}, err
}
input, err := logicalDraft.SDKInput()
~~~

Every provider surface gets a conformance fixture covering portable set,
explicit omit, scoped patch, protected field rejection, request isolation,
sync/stream parity where supported, and sanitized reporting.

### 19.11 Phase 8 — centralize orchestration invocation through core

The current baseline is 27 direct sync/stream invocations across 14
non-test files:

~~~text
activity_compactor.go             contextual_re_resolver.go
conversation_compactor.go         error_analyzer.go
event_summarizer.go               knowledge_extraction_hook.go
micro_resolver.go                 orchestrator.go
plan_refinement.go                result_distiller.go
result_mapreduce.go               synthesizer.go
tiered_capability_provider.go     user_memory_extraction.go
~~~

Add one internal helper and migrate every direct invocation to it:

~~~go
// orchestration/ai_invocation.go
type aiInvocation struct {
    Purpose    string
    Prompt     string
    Options    *core.AIOptions // compatibility input during migration
    Generation core.AIGenerationOptions
    Patches    []core.AIProviderPatch
}

func invokeAI(
    ctx context.Context,
    client core.AIClient,
    invocation aiInvocation,
) (*core.AIResponse, *core.AIRequestReport, error) {
    request := core.NewAIRequestFromLegacy(
        invocation.Prompt,
        invocation.Purpose,
        invocation.Options,
    )
    request.Generation = invocation.Generation
    request.Patches = append(
        []core.AIProviderPatch(nil),
        invocation.Patches...,
    )
    result, err := core.GenerateAI(ctx, client, request)
    if result == nil {
        return nil, nil, err
    }
    return result.Response, result.RequestReport, err
}
~~~

The helper is also the single place to preserve existing LLM Debug
deferral/deduplication and to attach the report to telemetry. Add the
corresponding provider-neutral dispatcher as `core.StreamAI`; the orchestration
stream helper calls it rather than duplicating capability checks. `StreamAI`
prefers `core.StreamingAIRequestClient`, falls back to `core.StreamingAIClient`
only when the request can be represented faithfully, and returns
`ErrAIRequestFeatureUnsupported` otherwise.

Assign the stable purposes listed in Section 17 at each call site. After
migration, this check should find direct calls only inside the helper:

~~~bash
rg -n '\.(GenerateResponse|StreamResponse)\(' orchestration \
  --glob '*.go' --glob '!*_test.go'
~~~

AI-semantic caches request a fingerprint before lookup. Stable fingerprints
join the cache key; unstable or unavailable fingerprints bypass only the
affected cache. Orchestration never imports `ai`, never matches provider/model
names, and never handles credentials or routes.

### 19.12 Phase 9 — make hosted provider contracts explicit

**Status:** Architecture decisions and detailed code blueprint holistically
reviewed and approved (2026-07-22); not implemented.

This phase follows the first full implementation and documentation audit. The
basic Azure OpenAI v1, Azure classic, and Google OpenAI-compatible request
shapes can be assembled with the Phase 4 and Phase 7 hooks, but the stock
OpenAI path still conflates three identities that hosted services may keep
separate:

1. the requested application model or alias;
2. the resolved semantic model used for capabilities, policy, and reports;
3. the provider deployment or model identifier placed on the wire.

That conflation is observable for an Azure reasoning deployment named, for
example, `prod-chat-west`: capability lookup treats the deployment as an
unknown non-reasoning model, while Azure v1 requires the deployment in the body
and applies the underlying model's reasoning contract. The current codec also
encodes reasoning effort as a nested object, whereas the current OpenAI-style
Azure v1 and Google chat-completions contracts use a top-level
`reasoning_effort` scalar.

Google Cloud also hosts Anthropic Claude through its publisher-model prediction
API. That surface is nearly identical to Anthropic Messages, but moves the model
to the endpoint URL, moves `anthropic_version` from a header into the body, and
uses a Google bearer token. Those differences require the same explicit
semantic-model/wire-model separation; a plain custom Anthropic base URL is not
sufficient.

Phase 9 is a corrective conformance phase, not a new integration plane. It must
use the codec, route, credential, policy, and result planes already established
by this architecture.

The provider contract baselines reviewed for this proposal are:

- [OpenAI Chat Completions create](https://developers.openai.com/api/reference/resources/chat/subresources/completions/methods/create);
- [Azure OpenAI v1 chat REST API](https://learn.microsoft.com/en-us/rest/api/microsoft-foundry/azureopenai/chat);
- [Azure OpenAI model-versus-deployment naming](https://learn.microsoft.com/en-us/azure/developer/ai/how-to/switching-endpoints#specify-the-model);
- [Azure OpenAI classic 2024-10-21 REST specification](https://github.com/Azure/azure-rest-api-specs/tree/main/specification/cognitiveservices/data-plane/AzureOpenAI/inference/stable/2024-10-21);
- [Azure OpenAI classic route, versioning, and authentication overview](https://learn.microsoft.com/en-us/azure/foundry/openai/reference?view=foundry-classic);
- [Google OpenAI compatibility](https://docs.cloud.google.com/gemini-enterprise-agent-platform/models/start/openai);
- [Google OpenAI-compatible authentication and regional endpoint setup](https://docs.cloud.google.com/gemini-enterprise-agent-platform/models/migrate/openai/auth-and-credentials);
- [Google Cloud requests for Anthropic Claude publisher models](https://docs.cloud.google.com/gemini-enterprise-agent-platform/models/partner-models/claude/use-claude);
- [Google Cloud partner-model global, multi-region, and regional endpoints](https://docs.cloud.google.com/gemini-enterprise-agent-platform/models/partner-models/use-partner-models); and
- [Anthropic's Claude-on-Google-Cloud contract](https://platform.claude.com/docs/en/build-with-claude/claude-on-vertex-ai).

Provider contracts remain external versioned dependencies. Re-check these
authorities when implementing the phase and before publishing updated examples;
the review date above is evidence of this proposal, not a permanent support
claim.

#### 19.12.1 Architecture-conformance audit and entry gate

Phase 9 was reviewed against every repository authority listed in §2 on
2026-07-22. The proposal preserves the framework dependency DAG and module
ownership. The audit originally found this documentation conflict:

- §8.1 of this design and Phase 9 require semantic model and endpoint-route
  resolution before provider-draft construction.
- The previous [AI Module Architecture](../ARCHITECTURE.md) text said that
  `EndpointResolver` runs after semantic policy. Because policy operates on the
  provider draft, that sentence described the current route-after-draft code
  flow that Phase 9 is intended to correct.

Reviewers approved the module-wide route-before-draft lifecycle on 2026-07-21,
including OpenAI and Anthropic, and `ai/ARCHITECTURE.md` now records it as the
normative design. Basic portable-intent, semantic-model, and capability
validation precedes routing; application policy follows routing; credential
acquisition remains after finalized policy. A resolver may therefore run before
a later policy failure, and a route failure takes precedence when both routing
and a downstream policy stage would fail. Implementation conformance remains
pending until the Phase 9 code lands. The 2026-07-22 holistic review rechecked
that decision against the completed Phase 0A-through-8 code, the illustrative
Phase 9 snippets, every authority in §2, and the current provider contracts
linked above; it found no dependency-boundary or ownership change beyond the
already approved AI-module lifecycle amendment.

The audit result by authority is:

| Authority | Phase 9 obligation and audit result |
|---|---|
| Framework Design Principles | Aligned: small additive interfaces, application composition, dumb factories, fail-fast validation, preserved public APIs, secret rotation, and no optional-module import from `core`. |
| Framework Architecture Overview | Aligned: agents continue to consume abstract `core.AIClient` capabilities; provider adapters, fallback, enterprise routing, and secret-free fingerprints remain below that boundary. |
| Core Module Architecture | Aligned: Phase 9 adds no provider-specific type, HTTP type, credential, codec, or new dependency to `core`; existing request/result capabilities remain the only cross-module contracts. |
| AI Module Architecture | Design aligned: the approved resolver order, provider registration, optional request-factory interfaces, `openaiwire` ownership, client-owned transport, and common instrumentation are preserved. OpenAI and Anthropic implementation conformance remains a Phase 9 delivery requirement. |
| Orchestration Module Architecture | Aligned: orchestration continues to import only `core` and `telemetry`, invokes through `core.GenerateAI`/`core.StreamAI`, and never branches on provider, model, deployment, or hostname. |
| Memory Module Architecture | No Phase 9 code impact: provider routing and AI-output cache fingerprints do not add an `ai` import or provider knowledge to `memory`. |
| Telemetry Module Architecture and observability guides | Design aligned; implementation evidence pending: §19.12.8 defines the common/provider/attempt span hierarchy, context propagation, sanitized error recording, safe request/response metadata, bounded module-local metrics, nil-safe fail-open behavior, and deterministic observability fixtures. Hosted and chain paths do not use the legacy raw-content logging helpers, record raw provider errors on spans, or place semantic models, chain entry names, deployments, route identities, credential scopes, or endpoints in metric labels. |
| Resilience module boundary | No Phase 9 code impact: reuse the AI module's replayable provider-attempt machinery; do not import or modify `resilience`. |
| Repository instructions and Contributing Guide | Pending implementation evidence: preserve human sign-off for Markdown, run the full workspace Go gates for every Go-touching slice, and add deterministic external-package and request-contract tests. |

Phase 9 code ownership is deliberately narrow:

| Concern | Planned files | Ownership rule |
|---|---|---|
| Common AI observability hardening | `ai/providers/base.go`, `base_test.go`, `ai/chain_request.go`, OpenAI/Anthropic/Gemini clients and factories, tagged Bedrock client tests, chain/provider tests, and `ai/instrumented_client.go` only if logical-span sanitization needs adjustment | `ai` preserves the common/logical/provider/attempt hierarchy, context-aware `framework/ai` logs, bounded module-local metrics, and sanitized errors. All built-ins migrate from raw-content helpers; complete base URLs become safe booleans at factory startup. Chain entry names remain stable non-secret log/report/span fields, never metric dimensions. No Phase 9 observability behavior moves into `core`, `telemetry`, or provider codecs. |
| Profiled OpenAI wire shape | `ai/providerkit/openaiwire/profile.go`, `codec.go`, `codec_test.go` | `openaiwire` owns only typed JSON/body decisions; it does not import root `ai`, resolve routes, acquire credentials, or send HTTP. |
| Shared OpenAI semantic alias catalog | new `ai/providers/openai/modelcatalog/catalog.go` plus compatibility wrappers/tests in `ai/providers/openai/models.go` | Stock OpenAI and Azure may resolve the same application aliases before capability lookup without making Azure depend on the OpenAI client package or ever rewriting route deployment. |
| Generic OpenAI lifecycle | `ai/providers/openai/request_builder.go`, `profile.go`, `client.go`, `integration.go`, existing OpenAI tests | The OpenAI adapter owns semantic capability validation and uses the resolver before profile/draft construction. A generic OpenAI route never substitutes `ResolvedEndpoint.Deployment` into the body. |
| Direct and Vertex Anthropic lifecycle | `ai/providers/anthropic/profile.go`, `request_builder.go`, `draft.go`, `client.go`, `integration.go`, `factory.go`, existing Anthropic tests | The existing Anthropic adapter owns the two exact profiles. Only `anthropic.vertex` consumes route deployment and emits the Vertex body/header contract. |
| Azure OpenAI adapter | new `ai/providers/azureopenai/factory.go`, `client.go`, `integration.go`, `profile.go`, `request_builder.go`, and `_test.go` files | The adapter owns exact Azure aliases, route validation, Azure auth header selection, and Azure response/SSE execution while reusing `openaiwire`, request policy, retry, and result contracts. |
| Public guide and compile fixtures | `docs/building/CUSTOM_AI_PROVIDER_GUIDE.md` and externally compiled examples/tests selected during implementation | Examples compose public APIs only; no guide-only framework hook or provider-specific `core` contract is permitted. |
| Normative documentation | `ai/ARCHITECTURE.md` and this file | The module architecture records stable ownership/lifecycle; this plan records implementation sequence, contracts, and evidence. |

No Phase 9 production change belongs in `core`, `orchestration`, `memory`,
`telemetry`, or `resilience`. Test-only import-boundary checks may inspect those
modules without changing their public or runtime behavior.

No other framework-architecture violation was identified in the proposal. That
finding is not a waiver: each implementation slice must repeat the review
against the then-current documents in §2 and record any newly discovered delta
before code is merged.

#### 19.12.2 Governing invariants

1. Preserve the normative lifecycle in §8.1: resolve the semantic model and
   endpoint route before constructing the provider draft.
2. Reject invalid portable intent, semantic models, and known unsupported
   capabilities before invoking the resolver. Application rules, middleware,
   per-request patches, and final draft validation run after routing. A route
   error takes precedence over any failure in those downstream stages.
3. Use the semantic model for capability lookup, selectors, reports, and
   compatibility decisions. Never infer capabilities from an arbitrary
   deployment label.
4. Treat the wire model/deployment as protected route-owned structure. An
   ordinary request patch cannot replace or remove it.
5. Make wire differences typed, explicit, and versioned. Do not select an
   Azure, Google OpenAI-compatible, Google-hosted Anthropic, or private-gateway
   contract by hostname inspection.
6. Keep credentials outside drafts and policy. Credential acquisition remains
   per transport attempt and happens only after final semantic validation.
7. Sync and streaming use one profiled draft builder and differ only in
   protocol-required streaming fields and decoding.
8. Keep `core` and `orchestration` provider-neutral. Hosted-provider behavior
   belongs in `ai/providerkit`, provider adapters, and application integration
   packages.
9. Preserve the existing `openaiwire.Codec` entry point for external adopters;
   add a profiled path rather than silently changing an application-selected
   legacy surface contract.
10. Existing generic OpenAI and Anthropic resolvers ignore `Deployment` for
    body construction. Only an explicitly selected hosted-provider profile may
    consume it as a wire model or publisher-model identifier.
11. Keep observability bounded and provider-neutral outside `ai`. Semantic
   models, chain entry names, deployments, publisher-model IDs, route
   identities, credential scopes, complete endpoints, query values, request
   IDs, and tenant identities are not provider metric labels. Credential values,
   prompts, system prompts, generated content, and serialized request/response
   bodies never enter common
   provider logs, metrics, reports, or spans. A sanitized stable route identity
   may appear in a request report or span, but never in metrics. The optional
   application-controlled `LLMCallRecorder` remains a separate explicit debug
   facility rather than a provider logging path.

#### 19.12.3 Separate semantic and wire identities

Refactor every provider that supports the shared `EndpointResolver`, currently
OpenAI and Anthropic, so route resolution no longer happens after draft
construction. The call-local flow becomes:

~~~text
clone request
    -> validate portable intent and resolve provider alias and semantic model
    -> validate known semantic capabilities
    -> resolve deterministic endpoint route and deployment
    -> derive a typed, versioned wire profile
    -> build one policy-editable draft
    -> apply policy and validate invariants
    -> encode immutable bytes
    -> acquire credentials and execute retries
~~~

OpenAI and Anthropic implement that order with the same provider-local split.
The following names are the intended internal contract; `requestSemantics` and
`preparedInvocation` are not new public interfaces. The shown semantic shape is
for the OpenAI family, including Azure. Anthropic uses its own provider-local
equivalent and does not carry OpenAI-family classification fields:

~~~go
// ai/providers/openai/request_builder.go
// ai/providers/azureopenai/request_builder.go
type requestSemantics struct {
    Request        *core.AIRequest // cloned, never caller-owned
    Options        *core.AIOptions // defaults applied; semantic model resolved
    RequestedModel string
    SemanticModel  string
    ProviderAlias  string
    Surface        string
    Operation      string
    Purpose        string
    Capabilities   providers.ModelCapabilities
    ReasoningModel bool // derived once from SemanticModel, never wire deployment
}

func (s *requestSemantics) endpointRequest(provider string) ai.EndpointRequest {
    return ai.EndpointRequest{
        Provider:      provider,
        ProviderAlias: s.ProviderAlias,
        Surface:       s.Surface,
        ResolvedModel: s.SemanticModel,
        Operation:     s.Operation,
        Purpose:       s.Purpose,
    }
}

type preparedInvocation struct {
    Request *preparedRequest
    Route   resolvedRoute
}

func (c *Client) prepareInvocation(
    ctx context.Context,
    supplied *core.AIRequest,
    stream bool,
) (*preparedInvocation, error) {
    semantics, err := c.prepareSemantics(ctx, supplied, stream)
    if err != nil { // clone/default/portable-intent/capability failures
        return nil, err
    }

    route, err := c.resolveEndpoint(ctx, semantics.endpointRequest(c.providerBaseName()))
    if err != nil {
        return nil, err
    }
    profile, err := c.requestProfile(semantics, route)
    if err != nil {
        return nil, err
    }
    prepared, err := c.buildPolicyRequest(ctx, semantics, profile, stream)
    if err != nil {
        return &preparedInvocation{Request: prepared, Route: route}, err
    }
    bindRouteFingerprint(prepared.Report, route.identity)
    return &preparedInvocation{Request: prepared, Route: route}, nil
}
~~~

OpenAI-family preparation computes `ReasoningModel` once, after semantic alias
and environment-override resolution, with the existing model-family predicate
(`openaiwire.IsReasoningModel(SemanticModel)`). It never derives that fact from
`ModelCapabilities.ReasoningStyle`, a route deployment, body `model`, or
hostname. `ReasoningStyle` answers only which reasoning-control family the
selected surface accepts; the exact provider alias then selects the concrete
top-level, nested, or omitted field spelling. The effective effort remains in
the cloned request/options consumed by the codec; do not duplicate it as
another scalar in `requestSemantics`.

`providerBaseName` above returns the adapter's constant (`openai` or
`anthropic`); it is not a runtime provider switch. Each provider keeps its own
typed profile and draft builder. Generate, stream, and `RequestFingerprint` call
`prepareInvocation` exactly once and reuse its route; they do not resolve a
second route after preparation:

~~~go
func (c *Client) RequestFingerprint(ctx context.Context, request *core.AIRequest) (string, bool) {
    invocation, err := c.prepareInvocation(ctx, request, false)
    if err != nil || invocation == nil || invocation.Request == nil || invocation.Request.Report == nil {
        return "", false
    }
    report := invocation.Request.Report
    return report.Fingerprint, report.Stable && report.Fingerprint != ""
}

func (c *Client) Generate(ctx context.Context, request *core.AIRequest) (*core.AIResult, error) {
    invocation, err := c.prepareInvocation(ctx, request, false)
    if err != nil {
        var prepared *preparedRequest
        if invocation != nil {
            prepared = invocation.Request
        }
        return resultWithReport(prepared, nil), err
    }
    return c.executePrepared(ctx, invocation.Request, invocation.Route)
}
~~~

The concrete implementation may retain the public `GenerateResponse` wrappers
and provider-specific result helpers. It must not retain the current
`prepareAIRequest`-then-`resolveEndpoint(prepared)` lifecycle. The resolver
signature becomes `resolveEndpoint(context.Context, ai.EndpointRequest)` so it
cannot depend on a draft, policy report, headers, or serialized body.

The endpoint resolver still receives only the sanitized `EndpointRequest` from
§11. It must remain deterministic and side-effect-free when used during cache
fingerprint preflight. `ResolvedEndpoint.Deployment` supplies the wire model
when the selected surface requires one; an empty deployment falls back to the
resolved semantic model only for contracts that explicitly allow that
identity.

`ResolvedEndpoint.Deployment` is opaque route output. It must never be passed
through provider model-alias resolution, hard-coded model aliases, or
`TRUVAG3_<PROVIDER>_MODEL_<ALIAS>` overrides. In particular, Azure deployment
names that happen to equal the current OpenAI aliases `fast`, `smart`,
`vision`, `code`, or `default` must reach the wire unchanged. The same rule
protects Google Cloud publisher-model IDs selected for `anthropic.vertex`.

The draft's `RequestInfo.ResolvedModel` remains the semantic model. Route
identity, not the raw endpoint or credential scope, binds deployment-affecting
semantics into the fingerprint. Reports must not acquire a second ambiguous
"model" field merely to expose transport routing.

Because the resolver now precedes application policy, it can be called before a
later rule, middleware, patch, or final draft-validation failure. This is an
intentional behavior change under the existing side-effect-free resolver
contract. Resolver input continues to contain only immutable sanitized request
identity; no supported routing behavior previously depended on policy-edited
body or header values.

#### 19.12.4 Add an explicit OpenAI wire profile

Add an optional profiled codec capability without expanding `core` or making
the reusable package depend on the root `ai` package. The exact enum names may
be refined during API review, but the profile must represent these closed
decisions rather than accepting arbitrary field names:

~~~go
// ai/providerkit/openaiwire/profile.go
type ModelFieldMode uint8

const (
    ModelFieldRequired ModelFieldMode = iota + 1
    ModelFieldOmitted
)

type TokenLimitField uint8

const (
    TokenLimitMaxTokens TokenLimitField = iota + 1
    TokenLimitMaxCompletionTokens
)

type ReasoningEffortStyle uint8

const (
    ReasoningEffortOmitted ReasoningEffortStyle = iota + 1
    ReasoningEffortTopLevel
    ReasoningEffortNestedObject
)

type SamplingPolicy uint8

const (
    SamplingOrdinary SamplingPolicy = iota + 1
    SamplingReasoningRestricted
)

type RequestProfile struct {
    SemanticModel   string
    WireModel       string
    ModelField      ModelFieldMode
    TokenLimit      TokenLimitField
    ReasoningEffort ReasoningEffortStyle
    Sampling        SamplingPolicy
}

type ProfiledCodec interface {
    Codec
    BuildDraftWithProfile(
        request *core.AIRequest,
        profile RequestProfile,
        stream bool,
    ) (*Draft, error)
}

// NewProfiledCodec is additive. NewCodec and NewConfiguredCodec keep their
// existing Codec return types for source compatibility.
func NewProfiledCodec(config Config) (ProfiledCodec, error) {
    configured, err := newConfiguredCodec(config)
    if err != nil {
        return nil, err
    }
    return configured, nil
}

func (p RequestProfile) Validate() error {
    if strings.TrimSpace(p.SemanticModel) == "" {
        return errors.New("OpenAI wire semantic model is empty")
    }
    switch p.ModelField {
    case ModelFieldRequired:
        if strings.TrimSpace(p.WireModel) == "" {
            return errors.New("OpenAI wire model is required by the profile")
        }
    case ModelFieldOmitted:
        if p.WireModel != "" {
            return errors.New("OpenAI wire model must be empty when the body field is omitted")
        }
    default:
        return errors.New("OpenAI wire model-field mode is invalid")
    }
    if p.TokenLimit != TokenLimitMaxTokens &&
        p.TokenLimit != TokenLimitMaxCompletionTokens {
        return errors.New("OpenAI wire token-limit field is invalid")
    }
    if p.ReasoningEffort < ReasoningEffortOmitted ||
        p.ReasoningEffort > ReasoningEffortNestedObject {
        return errors.New("OpenAI wire reasoning-effort style is invalid")
    }
    if p.Sampling != SamplingOrdinary &&
        p.Sampling != SamplingReasoningRestricted {
        return errors.New("OpenAI wire sampling policy is invalid")
    }
    if p.Sampling == SamplingReasoningRestricted &&
        p.TokenLimit != TokenLimitMaxCompletionTokens {
        return errors.New("reasoning-restricted sampling requires max_completion_tokens")
    }
    return nil
}
~~~

The closed modes cover at least:

| Decision | Required modes |
|---|---|
| Body model | required protected wire model; intentionally omitted by a deployment-in-path surface |
| Token limit | `max_tokens`; `max_completion_tokens` |
| Reasoning effort | unsupported/omitted; top-level scalar; nested compatibility object |
| Sampling | ordinary sampling; reasoning-restricted sampling |

`SamplingReasoningRestricted` means the semantic model belongs to a family
whose wire contract uses reasoning-model token and sampling rules. The draft
omits and rejects incompatible sampling fields when the effective reasoning
effort enables reasoning; an effective effort of `none` retains the existing
stock behavior that can permit ordinary sampling. The profile is not a blanket
ban independent of effective request intent.

Sampling/token selection and reasoning-effort spelling are independent profile
dimensions. A reasoning model may use `max_completion_tokens` and restricted
sampling even when its selected surface exposes no reasoning-effort field.
Conversely, Ollama may accept a nested reasoning-effort object for a
non-reasoning model while retaining `max_tokens` and ordinary sampling; the
nested object is emitted only when an effective effort is set.

`RequestProfile` contains no credentials, endpoints, prompts, or arbitrary
provider values. Validation rejects contradictory profiles, such as a required
model with an empty `WireModel` or reasoning-restricted sampling with the wrong
token field. Provider-local profile selection rejects a reasoning-effort style
unsupported by the selected surface; the codec validates only the closed wire
modes and their provider-neutral structural relationships.

The existing `Codec.BuildDraft` remains available as the compatibility entry
point. Built-in providers move to `BuildDraftWithProfile` so their current
official wire contracts are deliberate rather than inferred inside the codec.
The stock OpenAI chat-completions profile uses the current top-level
`reasoning_effort` contract. A provider such as Ollama may explicitly retain a
nested compatibility object without changing stock OpenAI or Azure behavior.

Profile behavior contributes a versioned, secret-free surface identity to the
policy fingerprint. Changing a field spelling, token-limit rule, or sampling
restriction requires a surface-version bump.

`codec` implements both interfaces. The legacy method delegates to a profile
that preserves its currently documented compatibility behavior; it is not
silently switched to the built-in OpenAI profile:

~~~go
// ai/providerkit/openaiwire/codec.go
func (c *codec) BuildDraft(
    request *core.AIRequest,
    resolvedModel string,
    stream bool,
) (*Draft, error) {
    return c.BuildDraftWithProfile(request, legacyProfile(resolvedModel, c), stream)
}

func NewConfiguredCodec(config Config) (Codec, error) {
    return newConfiguredCodec(config)
}

func (c *codec) BuildDraftWithProfile(
    request *core.AIRequest,
    profile RequestProfile,
    stream bool,
) (*Draft, error) {
    if err := profile.Validate(); err != nil {
        return nil, err
    }
    // Clone request, resolve portable modes, then construct exactly one body.
    // /model remains protected even when ModelFieldOmitted requires it absent.
    // TokenLimit and ReasoningEffort select closed field spellings here.
    // Sampling controls post-policy validation; it is not inferred from
    // WireModel or a hostname.
    return c.buildProfiledDraft(request, profile, stream)
}

func profileFingerprintIdentity(surfaceVersion string, profile RequestProfile) string {
    // Deliberately excludes SemanticModel and WireModel: RequestInfo carries
    // the former, while the sanitized route identity binds deployment changes.
    return fmt.Sprintf(
        "%s|profile-v1|model=%d|tokens=%d|reasoning=%d|sampling=%d",
        surfaceVersion,
        profile.ModelField,
        profile.TokenLimit,
        profile.ReasoningEffort,
        profile.Sampling,
    )
}
~~~

The draft stores `semanticModel`, `modelField`, and the versioned profile
identity. Its final validator requires `/model == WireModel` for
`ModelFieldRequired`, requires `/model` to be absent for `ModelFieldOmitted`,
and always protects `/model` from ordinary policy. `RequestInfo.ResolvedModel`
is always `SemanticModel`.

The generic OpenAI adapter selects the profile only from the resolved semantic
model-family fact, exact provider alias, and semantic capabilities. Its route
deployment is deliberately ignored:

~~~go
// ai/providers/openai/profile.go
func (c *Client) requestProfile(
    semantics *requestSemantics,
    _ resolvedRoute,
) (openaiwire.RequestProfile, error) {
    profile := openaiwire.RequestProfile{
        SemanticModel:   semantics.SemanticModel,
        WireModel:       semantics.SemanticModel,
        ModelField:      openaiwire.ModelFieldRequired,
        TokenLimit:      openaiwire.TokenLimitMaxTokens,
        ReasoningEffort: openaiwire.ReasoningEffortOmitted,
        Sampling:        openaiwire.SamplingOrdinary,
    }
    if semantics.ReasoningModel {
        profile.TokenLimit = openaiwire.TokenLimitMaxCompletionTokens
        profile.Sampling = openaiwire.SamplingReasoningRestricted
    }
    if semantics.Capabilities.ReasoningStyle == "openai" {
        profile.ReasoningEffort = openaiwire.ReasoningEffortTopLevel
        if semantics.ProviderAlias == "openai.ollama" {
            profile.ReasoningEffort = openaiwire.ReasoningEffortNestedObject
        }
    }
    return profile, profile.Validate()
}
~~~

This separation preserves the current `openai.ollama` behavior. For example,
`gemma4:31b` is not an OpenAI reasoning-model family merely because the Ollama
surface accepts the OpenAI-style reasoning control. Without an effective
effort it keeps `max_tokens`, ordinary sampling, and no reasoning field; with an
effective effort it keeps those token/sampling rules and adds only the nested
`reasoning.effort` compatibility object. OpenAI reasoning families continue to
use the model-family token and sampling contract, with effort spelling selected
independently by the surface capability.

`ForceReasoningObject` remains source-compatible while external users migrate
to `ProfiledCodec`; the built-in OpenAI client no longer uses that boolean to
choose the stock wire contract. An implementation PR must test both the old
`Codec.BuildDraft` behavior and the explicit built-in profile so the additive
API does not disguise a compatibility break.

#### 19.12.5 Add a small Azure OpenAI adapter

The classic Azure contract identifies the deployment in the URL and does not
require a body `model`; ordinary request policy cannot remove the stock codec's
protected model. Together with deployment-aware reasoning, this satisfies the
§10.4 threshold for a first-class Azure adapter rather than more conditionals
inside the generic OpenAI client.

Add a narrowly scoped `ai/providers/azureopenai` adapter that reuses the
profiled OpenAI codec, request-policy engine, response/SSE normalization,
credential source, injected HTTP client, and replayable retry execution. It
owns only Azure surface selection and the corresponding protected route and
wire profile:

| Exact provider alias | Route and body contract |
|---|---|
| `azureopenai.v1` | `/openai/v1/chat/completions`; deployment is the protected body model; top-level reasoning effort |
| `azureopenai.classic` | `/openai/deployments/{deployment}/chat/completions`; required `api-version`; body model intentionally omitted |

The new package registers one base provider and accepts only those two aliases.
Azure is request-aware-only in Phase 9 because correct construction requires a
semantic-to-deployment resolver. The legacy constructor cannot manufacture
that mapping without collapsing the identities this phase separates:

~~~go
// ai/providers/azureopenai/factory.go
package azureopenai

const providerName = "azureopenai"

type Factory struct{}

var _ ai.ProviderFactory = (*Factory)(nil)
var _ ai.ValidatedProviderFactory = (*Factory)(nil)
var _ ai.RequestProviderFactory = (*Factory)(nil)

func init() { ai.MustRegister(&Factory{}) }

func (*Factory) Name() string { return providerName }
func (*Factory) Description() string { return "Azure OpenAI" }

// No implicit environment detection: an endpoint resolver is mandatory and
// Azure credential audiences are application-owned.
func (*Factory) DetectEnvironment() (int, bool) { return 0, false }

func (f *Factory) Create(config *ai.AIConfig) core.AIClient {
    client, err := f.CreateValidated(config)
    if err != nil {
        panic(fmt.Sprintf("create Azure OpenAI client: %v", err))
    }
    return client
}

func (*Factory) CreateValidated(*ai.AIConfig) (core.AIClient, error) {
    return nil, fmt.Errorf(
        "%w: Azure OpenAI requires ai.NewRequestClient with an endpoint resolver",
        core.ErrAIRequestFeatureUnsupported,
    )
}

func (*Factory) CreateRequestClient(
    config *ai.AIConfig,
    integration ai.ProviderIntegrationConfig,
) (core.AIRequestClient, error) {
    if config == nil {
        return nil, errors.New("Azure OpenAI AI config is nil")
    }
    selected, err := parseSurface(config.ProviderAlias)
    if err != nil {
        return nil, err
    }
    if integration.EndpointResolver == nil {
        return nil, errors.New("Azure OpenAI endpoint resolver is required")
    }
    if integration.CredentialSource == nil && strings.TrimSpace(config.APIKey) == "" {
        return nil, errors.New("Azure OpenAI credential is required")
    }
    if strings.TrimSpace(config.BaseURL) != "" {
        return nil, errors.New("Azure OpenAI BaseURL is not accepted; return the complete URL from EndpointResolver")
    }
    return newClient(config, integration, selected)
}
~~~

`Factory.Create` is a guaranteed panic because `CreateValidated` always returns
an error. It exists only to satisfy the legacy `ProviderFactory` interface and
is an unsupported direct-call-only path. `ai.NewClient` detects
`ValidatedProviderFactory`, calls `CreateValidated`, and returns that error
without invoking `Create`; `DetectEnvironment` returns `(0, false)`, so automatic
selection cannot reach either path. The sole supported Azure construction path
is `ai.NewRequestClient`. The guide and exported documentation must state this
plainly rather than imply that `ai.NewClient` supports Azure.

Before adding the Azure adapter, extract the existing alias algorithm and
catalog into a side-effect-free subpackage. This avoids an Azure import of the
OpenAI client/factory package while preserving the current exported OpenAI
wrappers and mutable `ModelAliases` compatibility surface:

~~~go
// ai/providers/openai/modelcatalog/catalog.go
package modelcatalog

// defaultAliases is the current OpenAI-compatible catalog moved verbatim from
// openai/models.go. DefaultAliases returns a deep copy.
var defaultAliases = map[string]map[string]string{/* existing rows unchanged */}

func DefaultAliases() map[string]map[string]string {
    return cloneAliases(defaultAliases)
}

func cloneAliases(source map[string]map[string]string) map[string]map[string]string {
    clone := make(map[string]map[string]string, len(source))
    for providerAlias, aliases := range source {
        providerClone := make(map[string]string, len(aliases))
        for alias, model := range aliases {
            providerClone[alias] = model
        }
        clone[providerAlias] = providerClone
    }
    return clone
}

func Resolve(providerAlias, model string) string {
    return ResolveWithAliases(defaultAliases, providerAlias, model)
}

func ResolveWithAliases(
    aliases map[string]map[string]string,
    providerAlias, model string,
) string {
    if providerAlias == "" {
        providerAlias = "openai"
    }
    envProvider := strings.TrimPrefix(providerAlias, "openai.")
    envKey := "TRUVAG3_" + strings.ToUpper(envProvider) +
        "_MODEL_" + strings.ToUpper(model)
    if override := os.Getenv(envKey); override != "" {
        return override
    }
    if providerAliases, ok := aliases[providerAlias]; ok {
        if resolved, ok := providerAliases[model]; ok {
            return resolved
        }
    }
    return model
}

// ai/providers/openai/models.go — source-compatible facade
var ModelAliases = modelcatalog.DefaultAliases()

func ResolveModel(providerAlias, model string) string {
    return modelcatalog.ResolveWithAliases(ModelAliases, providerAlias, model)
}
~~~

Azure preparation uses that catalog only on the application-supplied model,
then uses the underlying OpenAI capability table. It never calls the catalog on
route output:

~~~go
requestedModel := options.Model
semanticModel := modelcatalog.Resolve("openai", requestedModel)
capabilities := providers.LookupModelCapabilities("openai", semanticModel)

semantics := &requestSemantics{
    RequestedModel: requestedModel,
    SemanticModel:  semanticModel,
    Capabilities:   capabilities,
    ReasoningModel: openaiwire.IsReasoningModel(semanticModel),
    // remaining sanitized request facts
}

route, err := c.resolveEndpoint(
    ctx,
    semantics.endpointRequest("azureopenai"),
)
// route.deployment is opaque from this point onward.
~~~

This intentionally reuses existing `TRUVAG3_OPENAI_MODEL_*` semantic alias
overrides without introducing Azure environment variables. A deployment named
`smart`, `fast`, or another alias is unaffected because it is produced only
after semantic resolution. Azure semantic resolution uses the immutable
built-in catalog plus environment overrides; runtime mutations of the exported
`openai.ModelAliases` compatibility map deliberately do not apply to Azure.
That mutable surface remains local compatibility debt of the existing OpenAI
adapter and must not force Azure to import or depend on the OpenAI client/factory
package. Model-catalog tests must prove current OpenAI alias,
environment-override, pass-through, and externally mutated `ModelAliases`
behavior remains unchanged, while Azure tests prove its immutable-catalog
boundary separately.

Surface and request-profile selection are closed and alias-driven:

~~~go
// ai/providers/azureopenai/profile.go
type surface uint8

const (
    surfaceV1 surface = iota + 1
    surfaceClassic
)

const (
    surfaceVersionV1      = "azure-openai-v1-chat-v1"
    surfaceVersionClassic = "azure-openai-classic-chat-v1"
)

func parseSurface(alias string) (surface, error) {
    switch alias {
    case "azureopenai.v1":
        return surfaceV1, nil
    case "azureopenai.classic":
        return surfaceClassic, nil
    default:
        return 0, fmt.Errorf("unsupported Azure OpenAI provider alias %q", alias)
    }
}

func (selected surface) surfaceVersion() (string, error) {
    switch selected {
    case surfaceV1:
        return surfaceVersionV1, nil
    case surfaceClassic:
        return surfaceVersionClassic, nil
    default:
        return "", errors.New("Azure OpenAI surface is invalid")
    }
}

type surfaceContract struct {
    supportsOpenAIReasoning bool
}

func (c *Client) surfaceContract(route resolvedRoute) (surfaceContract, error) {
    switch c.surface {
    case surfaceV1:
        // The v1 schema owns current reasoning_effort and
        // max_completion_tokens behavior.
        return surfaceContract{supportsOpenAIReasoning: true}, nil
    case surfaceClassic:
        versions := route.url.Query()["api-version"]
        if len(versions) != 1 {
            return surfaceContract{}, errors.New("Azure OpenAI classic api-version is invalid")
        }
        switch versions[0] {
        case "2024-10-21":
            // The pinned GA schema has max_completion_tokens but does not
            // define reasoning_effort; do not claim reasoning-model support.
            return surfaceContract{}, nil
        default:
            // Unknown versions remain conservative until an official schema
            // and request fixture add an explicit versioned row here.
            return surfaceContract{}, nil
        }
    default:
        return surfaceContract{}, errors.New("Azure OpenAI surface is invalid")
    }
}

func (c *Client) requestProfile(
    semantics *requestSemantics,
    route resolvedRoute,
) (openaiwire.RequestProfile, error) {
    if strings.TrimSpace(route.deployment) == "" {
        return openaiwire.RequestProfile{}, errors.New("Azure OpenAI deployment is empty")
    }
    contract, err := c.surfaceContract(route)
    if err != nil {
        return openaiwire.RequestProfile{}, err
    }
    profile := openaiwire.RequestProfile{
        SemanticModel:   semantics.SemanticModel,
        TokenLimit:      openaiwire.TokenLimitMaxTokens,
        ReasoningEffort: openaiwire.ReasoningEffortOmitted,
        Sampling:        openaiwire.SamplingOrdinary,
    }
    if semantics.ReasoningModel {
        if !contract.supportsOpenAIReasoning {
            return openaiwire.RequestProfile{}, fmt.Errorf(
                "%w: Azure OpenAI surface %q does not have a verified reasoning contract",
                core.ErrAIRequestFeatureUnsupported,
                semantics.ProviderAlias,
            )
        }
        profile.TokenLimit = openaiwire.TokenLimitMaxCompletionTokens
        profile.Sampling = openaiwire.SamplingReasoningRestricted
    }
    if contract.supportsOpenAIReasoning &&
        semantics.Capabilities.ReasoningStyle == "openai" {
        profile.ReasoningEffort = openaiwire.ReasoningEffortTopLevel
    }
    switch c.surface {
    case surfaceV1:
        profile.ModelField = openaiwire.ModelFieldRequired
        profile.WireModel = route.deployment // opaque: never ResolveModel
    case surfaceClassic:
        profile.ModelField = openaiwire.ModelFieldOmitted
    default:
        return openaiwire.RequestProfile{}, errors.New("Azure OpenAI surface is invalid")
    }
    return profile, profile.Validate()
}
~~~

The Phase 9 classic baseline is the pinned `2024-10-21` GA schema. It supports
the ordinary Chat Completions body without `model`, but that schema does not
define `reasoning_effort`; a semantic OpenAI reasoning model therefore fails
before policy, credentials, or transport on this surface. Supporting reasoning
on another classic API version requires a new reviewed `surfaceContract` row,
wire fixture, guide qualification, and route-identity version bump. The adapter
does not infer support merely because a deployment accepts the request.

The Azure client is intentionally a small owner of transport integration. It
embeds the existing retry base and delegates body/response protocol work to the
public codec instead of copying OpenAI JSON or SSE types:

~~~go
// ai/providers/azureopenai/client.go
type Client struct {
    *providers.BaseClient
    providerAlias    string
    surface          surface
    staticAPIKey     string
    requestPolicy    *requestpolicy.Engine
    credentialSource ai.CredentialSource
    endpointResolver ai.EndpointResolver
    codec            openaiwire.ProfiledCodec
    requestTimeout   time.Duration
}

var _ core.AIClient = (*Client)(nil)
var _ core.AIRequestClient = (*Client)(nil)
var _ core.AIRequestFingerprinter = (*Client)(nil)

func (c *Client) decode(response io.Reader) (*core.AIResult, error) {
    result, err := c.codec.Decode(response)
    return c.bindResultIdentity(result), err
}

func (c *Client) decodeStream(
    response io.Reader,
    callback core.StreamCallback,
) (*core.AIResult, error) {
    result, err := c.codec.DecodeStream(response, callback)
    return c.bindResultIdentity(result), err
}

func (c *Client) bindResultIdentity(result *core.AIResult) *core.AIResult {
    if result != nil && result.Response != nil {
        result.Response.Provider = c.providerAlias
    }
    return result
}
~~~

This is reuse through existing module contracts, not a dependency on private
fields or methods of `openai.Client`. `newClient` constructs a configured
`providers.BaseClient`, policy engine, `openaiwire` codec, and a shallow copy of
the injected HTTP client using the same rules as other HTTP providers. The
codec-preserved response model remains provider-reported wire data; the request
report remains the authoritative semantic model identity.

~~~go
version, err := selected.surfaceVersion()
if err != nil {
    return nil, err
}
codec, err := openaiwire.NewProfiledCodec(openaiwire.Config{
    SurfaceVersion:           version,
    ReasoningTokenMultiplier: config.ReasoningTokenMultiplier,
    DefaultReasoningEffort:   config.ReasoningEffort,
})
~~~

The surface version changes when Azure-specific body invariants change. A
resolver's sanitized `RouteIdentity` changes when deployment mapping,
`api-version`, or other semantic query configuration changes; raw values remain
excluded from the identity and report.

The adapter validates the complete resolver URL after merging the snapshotted
`ResolvedEndpoint.Query`. Alias—not URL inspection—selects the validator:

~~~go
// ai/providers/azureopenai/integration.go
func validateResolvedRoute(selected surface, route resolvedRoute) error {
    endpoint := route.url
    if endpoint == nil || endpoint.Scheme != "https" || endpoint.Hostname() == "" {
        return errors.New("Azure OpenAI endpoint must use HTTPS with a nonempty host")
    }
    if endpoint.User != nil || endpoint.Port() != "" || endpoint.Fragment != "" {
        return errors.New("Azure OpenAI endpoint must not contain user information, a port, or a fragment")
    }
    if strings.TrimSpace(route.identity) == "" {
        return errors.New("Azure OpenAI route identity is empty")
    }
    if strings.TrimSpace(route.deployment) == "" {
        return errors.New("Azure OpenAI deployment is empty")
    }

    switch selected {
    case surfaceV1:
        if endpoint.EscapedPath() != "/openai/v1/chat/completions" {
            return errors.New("Azure OpenAI v1 endpoint path is invalid")
        }
        versions := endpoint.Query()["api-version"]
        if len(versions) > 1 ||
            (len(versions) == 1 && strings.TrimSpace(versions[0]) == "") {
            return errors.New("Azure OpenAI v1 api-version must be singular and nonempty when supplied")
        }
    case surfaceClassic:
        want := "/openai/deployments/" + url.PathEscape(route.deployment) + "/chat/completions"
        if endpoint.EscapedPath() != want {
            return errors.New("Azure OpenAI classic endpoint path does not match deployment")
        }
        versions := endpoint.Query()["api-version"]
        if len(versions) != 1 || strings.TrimSpace(versions[0]) == "" {
            return errors.New("Azure OpenAI classic endpoint requires exactly one api-version")
        }
    default:
        return errors.New("Azure OpenAI surface is invalid")
    }
    return nil
}
~~~

Classic may retain documented non-version query members supplied by the trusted
resolver, but must reject a missing, empty, or repeated `api-version`. Azure v1
permits a resolver-supplied optional `api-version` and other currently
documented extension query members; if `api-version` is present, it must be
singular and nonempty. Provider errors and telemetry must sanitize the complete
URL exactly as the existing OpenAI and Anthropic integrations do.

Credential preparation runs once per replayed attempt and accepts only the two
Azure authentication contracts:

~~~go
func (c *Client) prepareCredential(
    ctx context.Context,
    attempt *http.Request,
    identity ai.CredentialRequest,
) error {
    if c.credentialSource == nil {
        if strings.TrimSpace(c.staticAPIKey) == "" ||
            containsInvalidHTTPHeaderValue(c.staticAPIKey) {
            return errors.New("Azure OpenAI API key is invalid")
        }
        if attempt.Header.Values("api-key") != nil {
            return errors.New("Azure OpenAI api-key conflicts with prepared headers")
        }
        attempt.Header.Set("api-key", c.staticAPIKey)
        return nil
    }
    credential, err := c.credentialSource.Credential(ctx, identity)
    if err != nil {
        return &integrationInvocationError{stage: "credential acquisition", cause: err}
    }
    if err := validateAzureCredential(credential); err != nil {
        return err
    }
    if attempt.Header.Values(credential.Name) != nil {
        return fmt.Errorf("Azure OpenAI credential header %q conflicts with prepared headers", credential.Name)
    }
    attempt.Header.Set(credential.Name, credential.Value)
    return nil
}

func validateAzureCredential(credential ai.HeaderCredential) error {
    if strings.TrimSpace(credential.Value) == "" ||
        containsInvalidHTTPHeaderValue(credential.Value) {
        return errors.New("Azure OpenAI credential value is invalid")
    }
    switch {
    case strings.EqualFold(credential.Name, "api-key"):
        return nil
    case strings.EqualFold(credential.Name, "Authorization"):
        parts := strings.SplitN(credential.Value, " ", 2)
        if len(parts) == 2 && strings.EqualFold(parts[0], "Bearer") &&
            strings.TrimSpace(parts[1]) != "" {
            return nil
        }
    }
    return errors.New("Azure OpenAI credential must be api-key or Authorization: Bearer")
}

func containsInvalidHTTPHeaderValue(value string) bool {
    for index := 0; index < len(value); index++ {
        character := value[index]
        if character == 0x7f || character < 0x20 && character != '\t' {
            return true
        }
    }
    return false
}
~~~

Both `api-key` and `authorization` are provider-protected headers. Static API
keys are attached per attempt as well; no credential value enters the draft,
policy report, fingerprint, error, or telemetry. The client passes the
resolver-supplied `CredentialScope` through `ai.CredentialRequest` unchanged.

The guide's Azure resolver maps semantic models to safe deployment identifiers
and constructs the exact URL from a validated resource origin:

~~~go
var azureDeploymentPattern = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)

type azureOpenAIResolver struct {
    origin          url.URL
    providerAlias   string
    apiVersion      string
    deployments     map[string]string // post-alias semantic model -> deployment
    routeIdentity   string
    credentialScope string
}

func newAzureOpenAIResolver(
    rawOrigin, providerAlias, apiVersion, routeIdentity, credentialScope string,
    deployments map[string]string,
) (*azureOpenAIResolver, error) {
    origin, err := url.Parse(strings.TrimRight(rawOrigin, "/"))
    if err != nil || origin.Scheme != "https" || origin.Hostname() == "" ||
        origin.User != nil || origin.Port() != "" || origin.Path != "" ||
        origin.RawQuery != "" || origin.Fragment != "" {
        return nil, errors.New("Azure OpenAI resource endpoint must be an HTTPS origin without an explicit port")
    }
    if providerAlias != "azureopenai.v1" && providerAlias != "azureopenai.classic" {
        return nil, errors.New("Azure OpenAI provider alias is invalid")
    }
    if providerAlias == "azureopenai.classic" && strings.TrimSpace(apiVersion) == "" {
        return nil, errors.New("Azure OpenAI classic api-version is required")
    }
    if strings.TrimSpace(routeIdentity) == "" || len(deployments) == 0 {
        return nil, errors.New("Azure OpenAI route identity and deployment map are required")
    }
    snapshot := make(map[string]string, len(deployments))
    for semanticModel, deployment := range deployments {
        if strings.TrimSpace(semanticModel) == "" ||
            !azureDeploymentPattern.MatchString(deployment) {
            return nil, errors.New("Azure OpenAI deployment map is invalid")
        }
        snapshot[semanticModel] = deployment
    }
    return &azureOpenAIResolver{
        origin:          *origin,
        providerAlias:   providerAlias,
        apiVersion:      apiVersion,
        deployments:     snapshot,
        routeIdentity:   routeIdentity,
        credentialScope: credentialScope,
    }, nil
}

func (r *azureOpenAIResolver) ResolveEndpoint(
    _ context.Context,
    request ai.EndpointRequest,
) (ai.ResolvedEndpoint, error) {
    if request.Provider != "azureopenai" ||
        request.ProviderAlias != r.providerAlias ||
        request.Surface != "chat-completions" {
        return ai.ResolvedEndpoint{}, errors.New("Azure OpenAI resolver identity mismatch")
    }
    deployment, ok := r.deployments[request.ResolvedModel]
    if !ok {
        return ai.ResolvedEndpoint{}, fmt.Errorf(
            "no Azure deployment for semantic model %q",
            request.ResolvedModel,
        )
    }
    endpoint := r.origin
    resolved := ai.ResolvedEndpoint{
        Deployment:      deployment,
        RouteIdentity:   r.routeIdentity,
        CredentialScope: r.credentialScope,
    }
    switch r.providerAlias {
    case "azureopenai.v1":
        endpoint.Path = "/openai/v1/chat/completions"
        if strings.TrimSpace(r.apiVersion) != "" {
            resolved.Query = url.Values{"api-version": {r.apiVersion}}
        }
    case "azureopenai.classic":
        endpoint.Path = "/openai/deployments/" + deployment + "/chat/completions"
        resolved.Query = url.Values{"api-version": {r.apiVersion}}
    }
    resolved.URL = &endpoint
    return resolved, nil
}
~~~

The `deployments` map is keyed by `EndpointRequest.ResolvedModel`: the concrete
semantic model after built-in alias and `TRUVAG3_OPENAI_MODEL_*` override
resolution. An application input alias such as `smart` is not a map key unless
its post-resolution semantic value is literally `smart`. The guide creates two
resolvers when an application uses both surfaces; one resolver never changes
surface based on a request URL. `routeIdentity` is a
stable non-secret configuration/version label, not the resource URL, project
name, raw API version, credential scope, or credential value. Its version is
bumped whenever any of those route inputs changes effective request semantics.

The exact alias selects the surface; the adapter must not infer v1 versus
classic behavior from the configured hostname or URL path. Both aliases resolve
through the single import-driven `azureopenai` factory, so this adds no Azure
field to `AIConfig` and no parallel provider registry.

The application supplies the semantic model and a resolver-controlled
deployment mapping. For example, semantic `gpt-5` may map to
`prod-chat-west`; policy and capability matching continue to use `gpt-5`, while
the Azure wire body uses `prod-chat-west`. When an application knows only an
opaque deployment and cannot state its underlying capabilities, explicit
portable reasoning intent fails before the network instead of guessing.

The adapter supports both exact authentication spellings through the existing
credential contracts:

- `api-key: <secret>` for Azure API-key authentication;
- `Authorization: Bearer <token>` for Microsoft Entra authentication.

Credential scope remains application- or resolver-supplied because Azure
resource types can require different audiences. The adapter must not silently
replace a supplied scope.

Azure resource-origin helpers require HTTPS, a nonempty host, no user
information, path, query, or fragment before they construct the complete
resolver URL. The adapter then applies the surface-specific complete-route
validation above, including classic `api-version`. Generic OpenAI base URLs
continue to permit HTTP for intentional loopback and local-provider use; Azure
hardening must not remove that compatibility.

Register the adapter through the existing import-driven factory mechanism and
reuse `RequestProviderFactory` plus `ValidatedProviderFactory` where the legacy
construction path is supported. Do not change `ProviderFactory.Create`, invent
a parallel registry, add Azure types to `core`, branch on Azure in
orchestration, or require callers to use a raw URL-rewriting transport.

#### 19.12.6 Keep Google-hosted OpenAI on the generic compatibility surface

Google's documented OpenAI-compatible endpoint already fits the generic
endpoint, Bearer credential, model, JSON response, and SSE contracts. Do not
create a first-class Google provider in this phase.

Endpoint construction must follow Google's location-dependent host examples:

- `global` uses `aiplatform.googleapis.com`;
- a regional location such as `us-central1` uses
  `<location>-aiplatform.googleapis.com`.

The guide helper is application code and returns the base URL ending at
`/endpoints/openapi`; the generic OpenAI adapter appends
`/chat/completions`. Build the URL structurally after validating the only value
that enters the hostname:

~~~go
var (
    googleProjectIDPattern = regexp.MustCompile(
        `^(?:[a-z][a-z0-9-]{4,28}[a-z0-9]|[0-9]{6,30})$`,
    )
    googleLocationPattern = regexp.MustCompile(
        `^[a-z](?:[a-z0-9-]{0,61}[a-z0-9])?$`,
    )
)

func googleOpenAIBaseURL(projectID, location string) (*url.URL, error) {
    projectID = strings.TrimSpace(projectID)
    location = strings.TrimSpace(location)
    if !googleProjectIDPattern.MatchString(projectID) {
        return nil, errors.New("Google project ID is invalid")
    }
    if !googleLocationPattern.MatchString(location) {
        return nil, errors.New("Google location is invalid")
    }

    host := "aiplatform.googleapis.com"
    if location != "global" {
        host = location + "-aiplatform.googleapis.com"
    }
    return &url.URL{
        Scheme: "https",
        Host:   host,
        Path: fmt.Sprintf(
            "/v1/projects/%s/locations/%s/endpoints/openapi",
            url.PathEscape(projectID),
            url.PathEscape(location),
        ),
    }, nil
}

func newGoogleHostedOpenAIClient(
    projectID, location, semanticModel string,
    tokens AccessTokenSource,
) (core.AIRequestClient, error) {
    endpoint, err := googleOpenAIBaseURL(projectID, location)
    if err != nil {
        return nil, err
    }
    if strings.TrimSpace(semanticModel) == "" || tokens == nil {
        return nil, errors.New("Google model and token source are required")
    }
    return ai.NewRequestClient(
        ai.WithProvider("openai"),
        ai.WithBaseURL(endpoint.String()),
        ai.WithModel(semanticModel), // complete documented google/... ID
        ai.WithAuthHeader("Authorization", bearerHeader(tokens)),
    )
}
~~~

The implementation fixture must assert both complete URLs:

~~~text
global:
https://aiplatform.googleapis.com/v1/projects/acme-prod/locations/global/endpoints/openapi/chat/completions

regional:
https://us-central1-aiplatform.googleapis.com/v1/projects/acme-prod/locations/us-central1/endpoints/openapi/chat/completions
~~~

If Google expands the accepted project or location grammar, broaden the helper
only with a cited contract test. Do not accept arbitrary input and rely on
`url.PathEscape` for a hostname component.

The project and location remain in the request path in both cases. Validate a
regional location against Google's location-identifier grammar before placing
it in a hostname; path escaping alone does not make an arbitrary hostname
component safe. Contract tests must cover both forms. If implementation cannot
validate and support regional identifiers deliberately, constrain the public
helper to `global` instead of accepting a location it cannot route correctly.

Use the stock OpenAI profiled codec plus a scoped provider-native rule for
Google-only top-level fields that lack a portable TruvaG3 representation. The
guide must state that current OpenAI-style reasoning effort is also top-level;
the rule is required because capability support for a `google/...` model is not
implicitly claimed by the stock OpenAI capability table, not because Google
uses a different field shape.

The post-Phase-9 native-rule example therefore uses the same top-level field
spelling as stock OpenAI, while explaining that policy supplies a
provider-model capability assertion absent from TruvaG3's stock table:

~~~go
googleReasoningRule := core.AIProviderPatch{
    Name:    "google-reasoning-effort",
    Version: "1",
    Selector: core.AIProviderSelector{
        Provider: "openai",
        Model:    "google/*",
    },
    Set: map[string]interface{}{
        "/reasoning_effort": "low",
    },
}
~~~

Unsupported Google fields may be ignored remotely. Keep the guide's requirement
for provider contract tests, and do not infer semantic support from an HTTP 200
response. Streaming remains conditional on the selected model accepting or
ignoring the protected `stream_options.include_usage` request member.

#### 19.12.7 Add an explicit Google-hosted Anthropic profile

Google Cloud hosts Anthropic Claude through its publisher-model prediction API.
This is not the Google OpenAI-compatible endpoint from §19.12.6 and is not an
ordinary custom Anthropic base URL. Anthropic documents the request as nearly
identical to Messages with two structural differences: the model is in the
Google Cloud endpoint URL rather than the body, and `anthropic_version` is the
protected body value `vertex-2023-10-16` rather than the
`anthropic-version` header.

Extend the existing Anthropic provider with an exact `anthropic.vertex` alias
and a provider-local typed wire profile. Do not create a second generic Google
provider or infer the profile from an `aiplatform.googleapis.com` hostname. The
two Anthropic profiles are:

| Exact provider alias | Model/version/auth contract |
|---|---|
| `anthropic` | Semantic model is the protected body `model`; `anthropic-version` remains a protected header; native Anthropic credential behavior is unchanged |
| `anthropic.vertex` | Route-owned publisher model is omitted from the body; protected body `anthropic_version` is `vertex-2023-10-16`; `anthropic-version` and `x-api-key` are omitted; Google access token uses `Authorization: Bearer ...` through `CredentialSource` |

Only the exact `anthropic.vertex` alias activates the hosted profile. Existing
application aliases such as `anthropic.primary` continue to use the direct
Messages profile and ignore route deployment for body construction. The
provider-local profile is closed and contains no endpoint or credential:

~~~go
requestedModel := options.Model
semanticModel := resolveModel(requestedModel) // existing Anthropic aliases/env
capabilities := providers.LookupModelCapabilities("anthropic", semanticModel)

semantics := &requestSemantics{
    RequestedModel: requestedModel,
    SemanticModel:  semanticModel,
    Capabilities:   capabilities,
    ProviderAlias:  c.providerAlias,
    // remaining sanitized request facts
}
~~~

Capability lookup deliberately uses the semantic provider `anthropic`, not the
host alias `anthropic.vertex`. The route-owned publisher model never enters
`resolveModel`, the capability table, or policy selectors.

~~~go
// ai/providers/anthropic/profile.go
type modelFieldMode uint8
type versionPlacement uint8

const (
    modelInBody modelFieldMode = iota + 1
    modelInRoute
)

const (
    versionInHeader versionPlacement = iota + 1
    versionInBody
)

const (
    directProfileIdentity = "anthropic-messages-v1"
    vertexProfileIdentity = "anthropic-vertex-predict-v1"
    vertexAPIVersion       = "vertex-2023-10-16"
)

type requestProfile struct {
    fingerprintIdentity string
    semanticModel       string
    wireModel           string
    modelField          modelFieldMode
    versionPlacement    versionPlacement
    version              string
}

func (c *Client) requestProfile(
    semantics *requestSemantics,
    route resolvedRoute,
) (requestProfile, error) {
    if semantics.ProviderAlias != "anthropic.vertex" {
        return requestProfile{
            fingerprintIdentity: directProfileIdentity,
            semanticModel:       semantics.SemanticModel,
            wireModel:           semantics.SemanticModel,
            modelField:          modelInBody,
            versionPlacement:    versionInHeader,
            version:             APIVersion,
        }, nil // route.deployment intentionally ignored
    }
    if strings.TrimSpace(route.deployment) == "" {
        return requestProfile{}, errors.New("Vertex Anthropic publisher model is empty")
    }
    return requestProfile{
        fingerprintIdentity: vertexProfileIdentity,
        semanticModel:       semantics.SemanticModel,
        wireModel:           route.deployment,
        modelField:          modelInRoute,
        versionPlacement:    versionInBody,
        version:             vertexAPIVersion,
    }, nil
}
~~~

Draft construction and validation consume the profile directly:

~~~go
// ai/providers/anthropic/request_builder.go and draft.go
func newAnthropicDraft(
    semantics *requestSemantics,
    profile requestProfile,
    stream bool,
) (*anthropicDraft, error) {
    body := map[string]interface{}{
        "messages":   []Message{{Role: "user", Content: semantics.Request.Prompt}},
        "max_tokens": semantics.Options.MaxTokens,
    }
    switch profile.modelField {
    case modelInBody:
        body["model"] = profile.wireModel
    case modelInRoute:
        // /model is protected but intentionally absent.
    default:
        return nil, errors.New("Anthropic model-field mode is invalid")
    }
    if profile.versionPlacement == versionInBody {
        body["anthropic_version"] = profile.version
    }

    document, err := requestpolicy.NewDocument(requestpolicy.DocumentConfig{
        Info: requestpolicy.RequestInfo{
            Provider:       "anthropic",
            ProviderAlias:  semantics.ProviderAlias,
            Surface:        "messages",
            Operation:      semantics.Operation,
            Purpose:        semantics.Purpose,
            RequestedModel: semantics.RequestedModel,
            ResolvedModel:  semantics.SemanticModel,
        },
        Body:           body,
        ProtectedPaths: []string{"/model", "/anthropic_version", "/messages", "/stream"},
        // Protected headers include authorization, x-api-key,
        // anthropic-version, content-type, and streaming accept.
    })
    if err != nil {
        return nil, err
    }
    return &anthropicDraft{Document: document, profile: profile, stream: stream}, nil
}

func (d *anthropicDraft) PolicyFingerprintIdentity() string {
    return d.profile.fingerprintIdentity
}

func (d *anthropicDraft) Validate() error {
    // Existing messages, max_tokens, stream, and adaptive-sampling checks run
    // first. These profile checks then make omitted fields invariant.
    switch d.profile.modelField {
    case modelInBody:
        model, ok := d.Get("/model")
        if !ok || model != d.profile.wireModel {
            return errors.New("Anthropic body model invariant was not preserved")
        }
    case modelInRoute:
        if _, ok := d.Get("/model"); ok {
            return errors.New("Vertex Anthropic body model must be omitted")
        }
    }
    version, bodyVersion := d.Get("/anthropic_version")
    if d.profile.versionPlacement == versionInBody &&
        (!bodyVersion || version != d.profile.version) {
        return errors.New("Vertex Anthropic body version invariant was not preserved")
    }
    if d.profile.versionPlacement == versionInHeader && bodyVersion {
        return errors.New("direct Anthropic version must not appear in the body")
    }
    return nil
}
~~~

The implementation folds the existing common draft checks into the shown
validator rather than returning early as this abbreviated block does. It also
keeps the existing portable omission and adaptive-thinking sampling rules for
both profiles; Vertex hosting does not weaken semantic Claude validation.

Header finalization is likewise profile-driven:

~~~go
func (c *Client) finalizeHeaders(
    profile requestProfile,
    draft *anthropicDraft,
    stream bool,
) (http.Header, error) {
    headers := make(http.Header)
    headers.Set("Content-Type", "application/json")
    if stream {
        headers.Set("Accept", "text/event-stream")
    }
    switch profile.versionPlacement {
    case versionInHeader:
        headers.Set("anthropic-version", profile.version)
        if c.credentialSource == nil && c.apiKey != "" {
            headers.Set("x-api-key", c.apiKey)
        }
    case versionInBody:
        // No anthropic-version and no x-api-key on Vertex.
    default:
        return nil, errors.New("Anthropic version placement is invalid")
    }
    providers.ApplyLegacyHeaders(headers, anthropicProtectedHeaders(stream), draft.Headers(), nil)
    return headers, nil
}
~~~

`Factory.CreateValidated` rejects `anthropic.vertex` with
`core.ErrAIRequestFeatureUnsupported`; `Factory.CreateRequestClient` requires
both `EndpointResolver` and `CredentialSource` for that exact alias and rejects
a static Anthropic API key or custom Anthropic base URL. Direct aliases preserve
legacy construction, environment lookup, custom base URLs, and current dynamic
credential behavior. Vertex credential preparation accepts only a nonempty
`Authorization: Bearer ...` credential and runs once per replayed attempt.

~~~go
// ai/providers/anthropic/factory.go
func (f *Factory) CreateValidated(config *ai.AIConfig) (core.AIClient, error) {
    if config != nil && config.ProviderAlias == "anthropic.vertex" {
        return nil, fmt.Errorf(
            "%w: anthropic.vertex requires ai.NewRequestClient",
            core.ErrAIRequestFeatureUnsupported,
        )
    }
    return f.createDirectClient(config)
}

func (f *Factory) CreateRequestClient(
    config *ai.AIConfig,
    integration ai.ProviderIntegrationConfig,
) (core.AIRequestClient, error) {
    if config != nil && config.ProviderAlias == "anthropic.vertex" {
        if integration.EndpointResolver == nil || integration.CredentialSource == nil {
            return nil, errors.New("anthropic.vertex requires endpoint and credential sources")
        }
        if strings.TrimSpace(config.APIKey) != "" || strings.TrimSpace(config.BaseURL) != "" {
            return nil, errors.New("anthropic.vertex does not accept Anthropic APIKey or BaseURL")
        }
        return f.createVertexClient(config, integration)
    }
    return f.createDirectRequestClient(config, integration)
}
~~~

`Create` delegates to `CreateValidated` and retains the registry's existing
panic-on-direct-factory-error compatibility behavior. The split helpers share
only provider-neutral configuration snapshots; `createVertexClient` does not
read `ANTHROPIC_API_KEY` or `ANTHROPIC_BASE_URL`.

The route resolver receives the semantic Claude model and returns the exact
Google publisher-model ID in `ResolvedEndpoint.Deployment`. Sync requests use:

~~~text
/v1/projects/{project}/locations/{location}/publishers/anthropic/models/{model}:rawPredict
~~~

Streaming requests use the same path with `:streamRawPredict`. Host selection
is explicit and location-dependent:

- `global` uses `aiplatform.googleapis.com` with `locations/global`;
- `us` and `eu` multi-regions use `aiplatform.us.rep.googleapis.com` and
  `aiplatform.eu.rep.googleapis.com` respectively; and
- a supported regional location uses `<location>-aiplatform.googleapis.com`.

The guide composes this first-class profile with an application-owned resolver.
The endpoint helper validates values before placing them into a host or path:

~~~go
var googlePublisherModelPattern = regexp.MustCompile(`^[A-Za-z0-9._@-]+$`)

func googlePartnerModelHost(location string) (string, error) {
    switch location {
    case "global":
        return "aiplatform.googleapis.com", nil
    case "us", "eu":
        return "aiplatform." + location + ".rep.googleapis.com", nil
    default:
        if !googleLocationPattern.MatchString(location) {
            return "", errors.New("Google partner-model location is invalid")
        }
        return location + "-aiplatform.googleapis.com", nil
    }
}

func googleClaudeEndpoint(
    projectID, location, publisherModel, operation string,
) (*url.URL, error) {
    if !googleProjectIDPattern.MatchString(projectID) {
        return nil, errors.New("Google project ID is invalid")
    }
    if !googlePublisherModelPattern.MatchString(publisherModel) {
        return nil, errors.New("Google publisher model is invalid")
    }
    host, err := googlePartnerModelHost(location)
    if err != nil {
        return nil, err
    }
    suffix := ":rawPredict"
    if operation == "stream" {
        suffix = ":streamRawPredict"
    } else if operation != "generate" {
        return nil, fmt.Errorf("unsupported Anthropic operation %q", operation)
    }
    return &url.URL{
        Scheme: "https",
        Host:   host,
        Path: fmt.Sprintf(
            "/v1/projects/%s/locations/%s/publishers/anthropic/models/%s%s",
            projectID,
            location,
            publisherModel,
            suffix,
        ),
    }, nil
}

type vertexClaudeResolver struct {
    projectID      string
    location       string
    publisherModel map[string]string // post-alias semantic model -> exact publisher ID
}

func (r *vertexClaudeResolver) ResolveEndpoint(
    _ context.Context,
    request ai.EndpointRequest,
) (ai.ResolvedEndpoint, error) {
    if request.Provider != "anthropic" || request.ProviderAlias != "anthropic.vertex" {
        return ai.ResolvedEndpoint{}, errors.New("Vertex Claude resolver received the wrong provider")
    }
    deployment, ok := r.publisherModel[request.ResolvedModel]
    if !ok {
        return ai.ResolvedEndpoint{}, fmt.Errorf(
            "no Vertex publisher model for semantic model %q",
            request.ResolvedModel,
        )
    }
    endpoint, err := googleClaudeEndpoint(
        r.projectID,
        r.location,
        deployment,
        request.Operation,
    )
    if err != nil {
        return ai.ResolvedEndpoint{}, err
    }
    return ai.ResolvedEndpoint{
        URL:             endpoint,
        Deployment:      deployment,
        RouteIdentity:   "vertex-claude-primary-v1",
        CredentialScope: "https://www.googleapis.com/auth/cloud-platform",
    }, nil
}
~~~

The `publisherModel` map is likewise keyed by
`EndpointRequest.ResolvedModel`, after Anthropic alias/environment resolution,
not by the application-supplied alias. Resolver examples and fixtures must use
the concrete semantic model as the key and the exact Google publisher-model ID
as the value.

The adapter independently validates a resolved Vertex route: HTTPS without
userinfo, port, query, or fragment; exact Google host for the path location;
exact publisher `anthropic`; exact route deployment; and `:rawPredict` versus
`:streamRawPredict` matching the operation. It does not trust a helper merely
because that helper appears in the guide.

~~~go
// ai/providers/anthropic/integration.go
var (
    vertexClaudePathPattern = regexp.MustCompile(
        `^/v1/projects/([^/]+)/locations/([^/]+)/publishers/anthropic/models/([^/:]+):(rawPredict|streamRawPredict)$`,
    )
    vertexProjectPattern = regexp.MustCompile(
        `^(?:[a-z][a-z0-9-]{4,28}[a-z0-9]|[0-9]{6,30})$`,
    )
    vertexLocationPattern = regexp.MustCompile(
        `^[a-z](?:[a-z0-9-]{0,61}[a-z0-9])?$`,
    )
    vertexPublisherModelPattern = regexp.MustCompile(`^[A-Za-z0-9._@-]+$`)
)

func vertexGoogleHost(location string) (string, error) {
    switch location {
    case "global":
        return "aiplatform.googleapis.com", nil
    case "us", "eu":
        return "aiplatform." + location + ".rep.googleapis.com", nil
    default:
        if !vertexLocationPattern.MatchString(location) {
            return "", errors.New("Vertex Anthropic location is invalid")
        }
        return location + "-aiplatform.googleapis.com", nil
    }
}

func validateVertexRoute(route resolvedRoute, operation string) error {
    endpoint := route.url
    if endpoint == nil || endpoint.Scheme != "https" || endpoint.Hostname() == "" {
        return errors.New("Vertex Anthropic endpoint must use HTTPS with a nonempty host")
    }
    if endpoint.User != nil || endpoint.Port() != "" ||
        endpoint.RawQuery != "" || endpoint.Fragment != "" {
        return errors.New("Vertex Anthropic endpoint contains a forbidden URL component")
    }
    match := vertexClaudePathPattern.FindStringSubmatch(endpoint.Path)
    if len(match) != 5 {
        return errors.New("Vertex Anthropic endpoint path is invalid")
    }
    projectID, location, publisherModel, method := match[1], match[2], match[3], match[4]
    if !vertexProjectPattern.MatchString(projectID) ||
        !vertexLocationPattern.MatchString(location) ||
        !vertexPublisherModelPattern.MatchString(publisherModel) {
        return errors.New("Vertex Anthropic endpoint contains an invalid resource identifier")
    }
    expectedHost, err := vertexGoogleHost(location)
    if err != nil || endpoint.Hostname() != expectedHost {
        return errors.New("Vertex Anthropic endpoint host does not match its location")
    }
    if publisherModel != route.deployment {
        return errors.New("Vertex Anthropic route deployment does not match its URL")
    }
    switch operation {
    case "generate":
        if method != "rawPredict" {
            return errors.New("Vertex Anthropic prediction method does not match generate")
        }
    case "stream":
        if method != "streamRawPredict" {
            return errors.New("Vertex Anthropic prediction method does not match stream")
        }
    default:
        return fmt.Errorf("unsupported Anthropic operation %q", operation)
    }
    return nil
}
~~~

The framework-private validation expressions and host helper may share a small
file within `ai/providers/anthropic`; they must not be imported from docs or an
example package. The guide construction remains entirely public API:

~~~go
client, err := ai.NewRequestClient(
    ai.WithProviderAlias("anthropic.vertex"),
    ai.WithModel("claude-sonnet-4-5-20250929"), // semantic capability identity
    ai.WithEndpointResolver(vertexResolver),
    ai.WithCredentialSource(adcBearerCredentials),
)
~~~

Validate project, location, and publisher-model path segments before endpoint
construction. Model availability varies by endpoint type and location, so the
framework must not hard-code a claim that every Claude model works in every
location. ADC token acquisition and refresh remain application-owned through
the existing credential source.

The current Anthropic response and SSE decoders may be reused only after
recording-transport tests prove the Google response envelope and event sequence
remain compatible. The profile must retain semantic-model capability checks,
sampling restrictions, policy selectors, reports, and fingerprints even when
the route uses a dated or otherwise different publisher-model ID.

#### 19.12.8 Preserve the logging, tracing, and metric contracts

Phase 9 retains the AI module's existing three-level trace shape rather than
creating hosted-provider-specific top-level spans:

~~~text
ai.generate or ai.stream                    common InstrumentedAIClient
    -> ai.generate_response/stream_response provider-local preparation/execution
        -> ai.http_attempt                   one span per replayable attempt
~~~

`ai.NewClient` and `ai.NewRequestClient` continue to install the common logical
decorator. OpenAI, Azure OpenAI, direct Anthropic, and `anthropic.vertex` create
only the provider-local span. `providers.BaseClient.ExecuteWithRetryPrepared`
owns attempt spans and receives the provider-span context, so `request_id` and
the trace parent propagate through endpoint resolution, credential acquisition,
HTTP construction, retries, response decoding, callbacks, and request-scoped
logs. Sync and stream share that lifecycle; streaming may retry only before a
successful response body is handed to the decoder and never replays after
partial content has been delivered.

The provider-local method uses one deferred completion path so every returned
error is recorded exactly once on that span. Attempt failures remain recorded
on their corresponding `ai.http_attempt` span. Direct `span.RecordError` is the
provider-local equivalent of `telemetry.RecordSpanError(ctx, err)` and ensures
the exact owned span receives the sanitized error. Phase 9 adds no provider
content events; if implementation evidence requires a new span event,
`request_id` is its first attribute and every remaining attribute follows the
same sanitization and cardinality rules. The concrete implementation may
factor this helper into `providers.BaseClient`, but it must preserve this shape:

~~~go
func (c *Client) Generate(
    ctx context.Context,
    request *core.AIRequest,
) (result *core.AIResult, err error) {
    started := time.Now()
    ctx, cancel := c.withRequestTimeout(ctx)
    defer cancel()
    ctx, span := c.StartSpan(ctx, "ai.generate_response")
    defer func() {
        finishProviderSpan(
            ctx,
            c.BaseClient,
            span,
            "ai_request",
            c.providerName,
            c.providerAlias,
            started,
            result,
            err,
        )
    }()

    span.SetAttribute("ai.provider", c.providerName)         // bounded base name
    span.SetAttribute("ai.provider_alias", c.providerAlias) // exact closed alias

    if request == nil {
        return nil, errors.New("AI request is nil")
    }

    span.SetAttribute("ai.prompt_length", len(request.Prompt))

    prepared, route, err := c.prepareAIRequest(ctx, request, false)
    if err != nil {
        return resultWithReport(prepared, nil), err
    }

    // Semantic and sanitized identities only. Never attach route.deployment,
    // route.url, route.credentialScope, request/response bodies, or content.
    span.SetAttribute("ai.model", prepared.SemanticModel)
    span.SetAttribute("ai.surface", prepared.Surface)
    span.SetAttribute("ai.request.route_identity", route.identity)

    c.LogRequestMetadata(ctx, providers.RequestObservation{
        Provider:      c.providerName,
        ProviderAlias: c.providerAlias,
        SemanticModel: prepared.SemanticModel,
        PromptLength:  len(request.Prompt),
    })

    httpRequest, err := prepared.HTTPRequest(ctx, route)
    if err != nil {
        return resultWithReport(prepared, nil), err
    }
    response, err := c.ExecuteWithRetryPrepared(
        ctx,
        httpRequest,
        c.credentialPreparer(prepared, route),
    )
    if err != nil {
        return resultWithReport(prepared, nil), err
    }
    defer func() { _ = response.Body.Close() }()

    result, err = c.decode(response.Body)
    if err != nil {
        return resultWithReport(prepared, result), err
    }
    if result != nil && result.Response != nil {
        c.LogResponseMetadata(ctx, providers.ResponseObservation{
            Provider:      c.providerName,
            ProviderAlias: c.providerAlias,
            SemanticModel: prepared.SemanticModel,
            Usage:         result.Response.Usage,
            Duration:      time.Since(started),
        })
    }
    return resultWithReport(prepared, result), nil
}

func finishProviderSpan(
    ctx context.Context,
    base *providers.BaseClient,
    span core.Span,
    operation string,
    provider string,
    providerAlias string,
    started time.Time,
    result *core.AIResult,
    err error,
) {
    if span == nil {
        span = &core.NoOpSpan{}
    }
    defer span.End()
    span.SetAttribute("ai.duration_ms", time.Since(started).Milliseconds())
    if err != nil {
        errorType, safeError := sanitizedProviderObservationError(err)
        span.SetAttribute("ai.status", "error")
        span.SetAttribute("ai.error_type", errorType)
        span.RecordError(safeError)
        if base != nil {
            base.LogErrorMetadata(ctx, providers.ErrorObservation{
                Operation:     operation,
                Provider:      provider,
                ProviderAlias: providerAlias,
                ErrorType:     errorType,
                Duration:      time.Since(started),
            })
        }
        return
    }
    span.SetAttribute("ai.status", "success")
    if result != nil && result.Response != nil {
        span.SetAttribute("ai.prompt_tokens", result.Response.Usage.PromptTokens)
        span.SetAttribute("ai.completion_tokens", result.Response.Usage.CompletionTokens)
        span.SetAttribute("ai.total_tokens", result.Response.Usage.TotalTokens)
        span.SetAttribute("ai.response_length", len(result.Response.Content))
    }
}
~~~

The names above are implementation targets; `prepared.HTTPRequest` represents
the provider-local immutable-request builder rather than a new public draft
escape hatch. The streaming method follows the same deferred span completion
pattern and adds only bounded stream metadata such as chunk count, partial
status, or callback termination. A callback error or
`core.ErrStreamPartiallyCompleted` is still a returned error and is recorded in
sanitized form.

`BaseClient.StartSpan` remains optional and fail-open. It returns a no-op span
when telemetry is absent and normalizes a nil context or nil span returned by a
custom telemetry implementation. It does not initialize telemetry, recover
exporter panics, or change application ownership of telemetry lifecycle:

~~~go
func (b *BaseClient) StartSpan(
    ctx context.Context,
    name string,
) (context.Context, core.Span) {
    if b.Telemetry == nil {
        return ctx, &core.NoOpSpan{}
    }
    spanCtx, span := b.Telemetry.StartSpan(ctx, name)
    if spanCtx == nil {
        spanCtx = ctx
    }
    if span == nil {
        span = &core.NoOpSpan{}
    }
    telemetry.SetCommonAttrsOn(spanCtx, span)
    return spanCtx, span
}
~~~

The current `BaseClient.LogRequest` and `LogResponseContent` helpers write raw
prompt or generated content through non-context logger methods, while
`LogResponse` places its ambiguous `model` argument in both a log and metrics.
Phase 9 must not copy or reuse those behaviors. Preserve the exported method
signatures for source compatibility, make `LogRequest` and
`LogResponseContent` safe no-ops, and make legacy `LogResponse` omit `model`
from both its log and metric labels. Mark all three deprecated in favor of
context-aware metadata helpers. Migrate all built-in providers, including
Gemini and tagged Bedrock, to the new helpers so the normative AI architecture
does not knowingly leave a second observation path behind. Replace OpenAI and
Gemini factory `base_url` fields with safe `custom_endpoint` booleans; no
provider factory startup log emits a complete endpoint.

The new helpers accept semantic identity explicitly and keep metric dimensions
bounded:

~~~go
type RequestObservation struct {
    Provider      string
    ProviderAlias string
    SemanticModel string
    PromptLength  int
}

type ResponseObservation struct {
    Provider      string
    ProviderAlias string
    SemanticModel string
    Usage         core.TokenUsage
    Duration      time.Duration
}

type ErrorObservation struct {
    Operation     string
    Provider      string
    ProviderAlias string
    ErrorType     string // bounded classifier, never err.Error()
    Duration      time.Duration
}

func addObservationRequestID(
    ctx context.Context,
    fields map[string]interface{},
) {
    var requestID string
    if baggage := telemetry.GetBaggage(ctx); baggage != nil {
        requestID = baggage["request_id"]
    }
    if requestID == "" {
        requestID = core.GetRequestID(ctx)
    }
    if requestID != "" {
        fields["request_id"] = requestID
    }
}

func normalizeObservationErrorType(value string) string {
    switch value {
    case "invalid_request", "route", "policy", "credential", "transport",
        "provider_client", "provider_rate_limit", "provider_server", "decode",
        "callback", "partial_stream", "cancelled", "deadline":
        return value
    default:
        return "unknown"
    }
}

func (b *BaseClient) LogRequestMetadata(
    ctx context.Context,
    observation RequestObservation,
) {
    if b.Logger == nil {
        return
    }
    fields := map[string]interface{}{
        "operation":      "ai_request",
        "provider":       observation.Provider,
        "provider_alias": observation.ProviderAlias,
        "model":          observation.SemanticModel,
        "prompt_length":  observation.PromptLength,
    }
    addObservationRequestID(ctx, fields)
    b.Logger.InfoWithContext(ctx, "AI request initiated", fields)
}

func (b *BaseClient) LogResponseMetadata(
    ctx context.Context,
    observation ResponseObservation,
) {
    // These helpers are nil-safe when telemetry is not initialized and expose
    // no error path that can fail the provider request.
    telemetry.RecordAIRequest(
        telemetry.ModuleAI,
        observation.Provider,
        float64(observation.Duration.Milliseconds()),
        "success",
    )
    if observation.Usage.PromptTokens > 0 {
        telemetry.RecordAITokens(
            telemetry.ModuleAI,
            observation.Provider,
            "input",
            int64(observation.Usage.PromptTokens),
        )
    }
    if observation.Usage.CompletionTokens > 0 {
        telemetry.RecordAITokens(
            telemetry.ModuleAI,
            observation.Provider,
            "output",
            int64(observation.Usage.CompletionTokens),
        )
    }
    if b.Logger == nil {
        return
    }
    fields := map[string]interface{}{
        "operation":         "ai_response",
        "provider":          observation.Provider,
        "provider_alias":    observation.ProviderAlias,
        "model":             observation.SemanticModel,
        "prompt_tokens":     observation.Usage.PromptTokens,
        "completion_tokens": observation.Usage.CompletionTokens,
        "total_tokens":      observation.Usage.TotalTokens,
        "duration_ms":       observation.Duration.Milliseconds(),
        "status":            "success",
    }
    addObservationRequestID(ctx, fields)
    b.Logger.InfoWithContext(ctx, "AI response received", fields)
}

func (b *BaseClient) LogErrorMetadata(
    ctx context.Context,
    observation ErrorObservation,
) {
    if b.Logger == nil {
        return
    }
    errorType := normalizeObservationErrorType(observation.ErrorType)
    safeError := "AI provider request failed: " + errorType
    fields := map[string]interface{}{
        "operation":      observation.Operation,
        "provider":       observation.Provider,
        "provider_alias": observation.ProviderAlias,
        "status":         "error",
        "error":          safeError,
        "error_type":     errorType,
        "duration_ms":    observation.Duration.Milliseconds(),
    }
    addObservationRequestID(ctx, fields)
    b.Logger.ErrorWithContext(ctx, "AI provider request failed", fields)
}
~~~

`provider` is a bounded framework-owned base name used by logging-derived and
AI metrics. `provider_alias`, semantic `model`, surface, and stable route
identity remain useful log/span/report fields but are not metric dimensions.
Complete endpoints, query values, deployment/publisher-model IDs, credential
scope, and credential material are absent from both observation structs.
`BaseClient.SetLogger` continues to apply `framework/ai`; every request-scoped
log uses a `WithContext` method for trace correlation and explicitly copies a
nonempty `request_id` with the common instrumented client's baggage-first,
core-context-fallback precedence. This keeps request correlation when telemetry
is absent and remains safe because `request_id` is excluded from metric labels.
Every new error log includes
`operation`, a bounded `error_type`, and a sanitized `error` string.
Startup-only factory logs may use the context-free methods but must still
include `operation` and may log only safe booleans and bounded identities,
never a complete endpoint.

Provider error classification may inspect a bounded response body internally
for retry/failover decisions. `BaseClient.HandleError` currently includes some
provider response detail in its returned `Error()` text, and existing consumers
and tests may depend on that diagnostic contract. Phase 9 therefore does not
silently change returned provider-error text. Instead, every framework
observation converts the original error to a stable classification and a new
sanitized observation error without calling `err.Error()`:

~~~go
func sanitizedProviderObservationError(err error) (string, error) {
    errorType := "unknown"
    switch {
    case errors.Is(err, context.Canceled):
        errorType = "cancelled"
    case errors.Is(err, context.DeadlineExceeded):
        errorType = "deadline"
    default:
        var providerErr core.ProviderError
        if errors.As(err, &providerErr) {
            switch {
            case providerErr.StatusCode() == http.StatusTooManyRequests:
                errorType = "provider_rate_limit"
            case providerErr.StatusCode() >= 500:
                errorType = "provider_server"
            default:
                errorType = "provider_client"
            }
        }
    }
    errorType = normalizeObservationErrorType(errorType)
    return errorType, errors.New("AI provider request failed: " + errorType)
}
~~~

Provider-local and chain spans record only the returned sanitized observation
error. Framework logs construct their sanitized `error` field from the same
bounded `errorType`. The original provider error—including its status,
retryability, `errors.Is`/`errors.As` behavior, and current caller-visible
diagnostic text—is returned unchanged. Redacting or structurally exposing raw
provider diagnostics to callers would be a separate compatibility proposal,
not a hidden consequence of hosted-provider support.

Credential-source, resolver, transport, and decoding errors receive the same
observation-boundary treatment. Their original causes remain available to
`errors.Is`/`errors.As`, while framework logs and spans use only the bounded
classification and sanitized observation error. This constraint applies
equally to sync, stream, retry, chain failover, and credential-rejection
observer paths.

`BaseClient` retry instrumentation is explicitly inside this boundary. Every
`ai.http_attempt` `RecordError`, `ai.previous_error` attribute, retry warning,
retry-wait log, cancellation log, and final-exhaustion log must use the bounded
classification and sanitized observation message; none may call `err.Error()`
or format a concrete error type into an observation field. HTTP status,
content type, attempt counts, retryability, and bounded attempt status remain
safe structured metadata. The original error remains unchanged in the returned
error chain. This requirement covers request-body replay errors, credential
preparation failures, transport failures, proxy responses, provider 429/5xx
responses, cancellation/deadline failures, and exhausted retries.

`ChainClient` is part of this slice because hosted-provider errors flow through
its attempt and aggregate spans. Replace `RecordError(callErr)` and
`RecordError(errors.Join(...))` with a stable sanitized observation error while
preserving the original error returned to the caller and its
`errors.Is`/`errors.As` chain. Chain entry names remain validated stable
non-secret operator labels in reports, logs, and spans, but they and numeric
attempt indexes are not metric labels. Chain counters use only `module`,
bounded `status`, and bounded failure/recovery reason dimensions; provider and
entry transitions remain visible on the correlated span and log. A
caller-owned custom entry with an arbitrary name therefore cannot create an
unbounded metric series or place sensitive callback text in trace exception
events.

#### 19.12.9 Reconcile the provider guide with implemented behavior

Update `docs/building/CUSTOM_AI_PROVIDER_GUIDE.md` in the same phase as the code
it documents:

1. Add the required blank import for every built-in provider used by a hosted
   recipe.
2. Show semantic-model-to-deployment routing for Azure reasoning models rather
   than setting an opaque deployment as the only model identity. Until the new
   adapter replaces the generic recipe, warn that passing a deployment through
   `WithModel` can rewrite names that collide with OpenAI aliases or matching
   `TRUVAG3_OPENAI_MODEL_*` overrides. State that the resolver map is keyed by
   the concrete `EndpointRequest.ResolvedModel` after built-in alias and
   environment-override resolution, not by the application input alias. Also
   state that Azure uses the immutable built-in catalog plus environment
   overrides and does not observe runtime mutations of `openai.ModelAliases`.
3. Replace the generic classic workaround with the Azure classic profiled
   adapter; retain a warning that supported fields vary by API version. State
   that `ai.NewRequestClient` is the only supported Azure constructor,
   `ai.NewClient` returns an unsupported-construction error, and direct legacy
   `Factory.Create` invocation is a guaranteed-panic compatibility method.
4. Refresh the classic Azure citations: pair the current Foundry chat reference
   with the canonical version-pinned Azure REST specification, and retain the
   drifted classic page only for the route, `api-version`, and authentication
   context it still documents. Re-verify any Microsoft Learn version moniker
   before publication rather than assuming it still renders the classic schema.
5. Add strict HTTPS-origin validation to Azure helpers, reject explicit ports,
   and prevent duplicated `/openai/v1` paths.
6. Fix the Google helper's location-dependent hostname selection, validate a
   regional location before hostname interpolation, and show both `global` and
   regional examples.
7. Correct the Google/OpenAI reasoning-field explanation.
8. Add a separate Google-hosted Claude recipe using `anthropic.vertex`, a
   semantic-model-to-publisher-model resolver, ADC bearer-token refresh, and
   the documented global, `us`/`eu` multi-region, and regional endpoint forms.
   Do not present it as a custom Anthropic base URL or reuse the Google
   OpenAI-compatible helper. State that the publisher-model map is keyed by the
   post-alias `EndpointRequest.ResolvedModel`, not by the application input
   alias.
9. Remove `ProtectedHeaders: []string{"*"}` from the SDK draft example and show
   a real provider `Validate` method.
10. State that rule values are excluded from fingerprints and therefore every
   semantic rule change requires a `Version` bump.
11. Document selector scoping, case matching, full-model globs, and the rejected
   JSON Pointer append token.
12. Qualify cache bypass for legacy-only representable clients and qualify the
   `ai.request.prepared` event as an orchestration event.
13. Promise secrecy for credential values; explain that non-secret header names
    may appear in validation and conflict diagnostics.
14. Separate locally executable contract tests from optional live cloud smoke
    tests requiring application-owned credentials.
15. Expand the observability section to show the complete logical/provider/
    attempt span hierarchy, semantic-model-versus-deployment identity rules,
    context-aware `framework/ai` logging contract, bounded metric dimensions,
    raw-content/error-body exclusions, optional fail-open telemetry behavior,
    and links to both repository observability guides.

Every compiled guide fixture includes the factory registration import matching
the recipe it exercises:

~~~go
import (
    "github.com/truvaagents/truva-g3/ai"
    _ "github.com/truvaagents/truva-g3/ai/providers/anthropic"   // anthropic.vertex
    _ "github.com/truvaagents/truva-g3/ai/providers/azureopenai" // Azure only
    _ "github.com/truvaagents/truva-g3/ai/providers/openai"      // Google OpenAI compatibility
)
~~~

Individual examples should import only the provider package they use. The
canonical Azure API-key construction is:

~~~go
client, err := ai.NewRequestClient(
    ai.WithProviderAlias("azureopenai.v1"),
    ai.WithModel("gpt-5"), // semantic model, not deployment
    ai.WithAPIKey(azureAPIKey),
    ai.WithEndpointResolver(azureResolver),
)
~~~

The Entra form replaces `WithAPIKey` with a rotating
`WithCredentialSource` that returns `Authorization: Bearer ...`; classic
replaces the alias and resolver together. Google OpenAI and Vertex Claude use
the constructors in §§19.12.6–19.12.7. None of the recipes capture a startup
access token as a static string.

After Phase 9 lands, update this document's top-level implementation status and
the phase statuses to reflect the committed branch. Do not mark Phase 9
implemented merely because the documentation proposal has been approved.

#### 19.12.10 Contract and regression tests

Use recording transports and external-package compile fixtures for deterministic
CI coverage. No cloud credential is required for the conformance suite.

The lifecycle tests must make ordering observable instead of relying on a final
body assertion:

~~~go
type eventResolver struct {
    events   *[]string
    endpoint *url.URL
    err      error
}

func (r *eventResolver) ResolveEndpoint(
    _ context.Context,
    _ ai.EndpointRequest,
) (ai.ResolvedEndpoint, error) {
    *r.events = append(*r.events, "route")
    if r.err != nil {
        return ai.ResolvedEndpoint{}, r.err
    }
    return ai.ResolvedEndpoint{
        URL:           r.endpoint,
        RouteIdentity: "contract-route-v1",
        Deployment:    "wire-deployment",
    }, nil
}

type eventMiddleware struct{ events *[]string }

func (*eventMiddleware) Name() string    { return "event-policy" }
func (*eventMiddleware) Version() string { return "1" }
func (m *eventMiddleware) Apply(
    _ context.Context,
    _ requestpolicy.RequestEditor,
) error {
    *m.events = append(*m.events, "policy")
    return nil
}

func TestPreparationOrderIsValidateRouteProfilePolicy(t *testing.T) {
    for _, operation := range []string{"generate", "stream", "fingerprint"} {
        t.Run(operation, func(t *testing.T) {
            events := []string{}
            client := newLifecycleTestClient(t, operation, &eventResolver{
                events:   &events,
                endpoint: contractEndpoint(t, operation),
            }, &eventMiddleware{events: &events})

            invokeLifecycle(t, client, operation, validSemanticRequest())
            if diff := cmp.Diff([]string{"route", "policy"}, events); diff != "" {
                t.Fatalf("lifecycle order (-want +got):\n%s", diff)
            }
        })
    }
}

func TestPortableValidationAndRouteErrorPrecedence(t *testing.T) {
    // Invalid portable intent never calls the resolver or middleware.
    // A resolver error produces ["route"] and prevents downstream policy.
    // A policy error after a successful resolver produces ["route", "policy"].
    // Repeat the table for OpenAI and Anthropic.
}
~~~

`newLifecycleTestClient`, `contractEndpoint`, and `invokeLifecycle` are
provider-test helpers, not production abstractions. Tests use a recording
`RoundTripper`, not an `httptest.Server`, when proving that no network attempt
or credential acquisition occurs.

Observability fixtures use a component-aware recording logger, recording
`core.Telemetry`/`core.Span`, and an isolated metrics registry. Run the same
contract for OpenAI, both Azure aliases, direct Anthropic, and
`anthropic.vertex`; cover sync success, stream success, one recovered retry,
terminal provider error, credential error, resolver error, decoder error,
callback error, and partial stream.

The shared metadata-helper and legacy-helper regression table additionally
covers Gemini and tagged Bedrock. It proves those built-ins no longer emit raw
prompt/response content, retain semantic model identity only on permitted
surfaces, and preserve their existing provider-specific trace shapes. Bedrock
does not manufacture an `ai.http_attempt` span because its SDK owns transport;
its tagged test asserts the logical/provider hierarchy instead.

A representative hosted-provider fixture has this shape:

~~~go
func TestHostedProviderObservabilityContract(t *testing.T) {
    const (
        requestID      = "request-contract-123"
        semanticModel  = "gpt-5"
        routeIdentity  = "route-contract-v1"
        deployment     = "wire-deployment-secret"
        credential     = "credential-secret"
        credentialScope = "credential-scope-secret"
        prompt          = "prompt-secret"
        responseContent = "response-secret"
        providerBody    = `{"error":{"message":"provider-body-secret"}}`
    )

    for _, operation := range []string{
        "generate_success",
        "stream_success",
        "retry_recovery",
        "provider_error",
        "credential_error",
        "resolver_error",
        "decoder_error",
        "callback_error",
        "partial_stream",
    } {
        t.Run(operation, func(t *testing.T) {
            logger := newRecordingComponentLogger()
            traces := newRecordingTelemetry()
            metrics := newIsolatedMetricRecorder(t)
            client := newObservationTestClient(t, observationFixture{
                Operation:       operation,
                Logger:          logger,
                Telemetry:       traces,
                Metrics:         metrics,
                SemanticModel:   semanticModel,
                RouteIdentity:   routeIdentity,
                Deployment:      deployment,
                Credential:      credential,
                CredentialScope: credentialScope,
                Prompt:          prompt,
                ResponseContent: responseContent,
                ProviderBody:    providerBody,
            })

            ctx := telemetry.WithBaggage(
                context.Background(),
                "request_id",
                requestID,
            )
            invokeObservationFixture(ctx, client, operation)

            logger.RequireComponent(t, "framework/ai")
            logger.RequireRequestContext(t, requestID)
            logger.RequireFieldOnEveryEntry(t, "operation")
            logger.RequireBoundedErrorFields(t)
            traces.RequireHierarchy(t,
                logicalSpanFor(operation),
                providerSpanFor(operation),
                "ai.http_attempt",
            )
            metrics.RequireLabel(t, "module", telemetry.ModuleAI)
            metrics.RequireOnlyProviderDimensions(t)

            observations := collectObservationText(logger, traces, metrics)
            for _, forbidden := range []string{
                deployment,
                credential,
                credentialScope,
                prompt,
                responseContent,
                "provider-body-secret",
                "https://enterprise.example/openai/v1/chat/completions",
                "api-version=2024-10-21",
            } {
                if strings.Contains(observations, forbidden) {
                    t.Fatalf("observation leaked %q", forbidden)
                }
            }
            metrics.RequireAbsentLabels(t,
                "model",
                "provider_alias",
                "surface",
                "route_identity",
                "entry_name",
                "from_provider",
                "to_provider",
                "attempt",
                "deployment",
                "credential_scope",
                "endpoint",
                "request_id",
                "tenant_id",
            )

            // Semantic identity and a stable non-secret route identity are
            // trace/report fields, not metric dimensions.
            traces.RequireAttribute(t, providerSpanFor(operation), "ai.model", semanticModel)
            traces.RequireAttribute(t, providerSpanFor(operation), "ai.request.route_identity", routeIdentity)
            traces.RequireSanitizedRecordedErrors(t)
        })
    }
}

func TestHostedProviderTelemetryIsOptionalAndFailOpen(t *testing.T) {
    // Run successful sync and stream calls with nil telemetry, with a telemetry
    // implementation returning nil context/span, and with a nil logger. The
    // provider result, callback sequence, retry count, and error identity must
    // be identical to the fully instrumented control. A conflicting core
    // request ID and baggage request ID proves the common baggage-first
    // correlation precedence.
}
~~~

`newObservationTestClient`, `invokeObservationFixture`, and recorder assertions
are test-only helpers. The trace hierarchy assertion accounts for operations
that fail before transport by requiring zero attempt spans in those rows. Retry
rows require one attempt span per actual attempt; stream rows prove no retry
occurs after the first delivered content. `RequireSanitizedRecordedErrors`
checks both recorded exception messages and attached error attributes. Logger
assertions distinguish startup entries from request-scoped entries and require
`error` plus bounded `error_type` on every request error.

Wire fixtures are table-driven and assert the exact route, structural presence,
and protected headers. The implementation expands this table for both sync and
stream:

~~~go
tests := []struct {
    name          string
    alias         string
    semanticModel string
    deployment    string
    wantPath      string
    wantQuery     url.Values
    wantBody      map[string]interface{}
    absentBody    []string
    wantHeaders   map[string]string
    absentHeaders []string
}{
    {
        name:          "stock OpenAI reasoning",
        alias:         "openai",
        semanticModel: "gpt-5",
        deployment:    "ignored-by-generic-profile",
        wantPath:      "/v1/chat/completions",
        wantBody: map[string]interface{}{
            "model":                 "gpt-5",
            "reasoning_effort":      "low",
            "max_completion_tokens": float64(5000),
        },
        absentBody: []string{"reasoning", "max_tokens"},
    },
    {
        name:          "Azure v1 reasoning deployment",
        alias:         "azureopenai.v1",
        semanticModel: "gpt-5",
        deployment:    "smart",
        wantPath:      "/openai/v1/chat/completions",
        wantBody: map[string]interface{}{
            "model":                 "smart",
            "reasoning_effort":      "low",
            "max_completion_tokens": float64(5000),
        },
        absentBody:  []string{"reasoning", "max_tokens"},
        wantHeaders: map[string]string{"api-key": "<recorded>"},
    },
    {
        name:          "Azure classic deployment in path",
        alias:         "azureopenai.classic",
        semanticModel: "gpt-4.1",
        deployment:    "fast",
        wantPath:      "/openai/deployments/fast/chat/completions",
        wantQuery:     url.Values{"api-version": {"2024-10-21"}},
        absentBody:    []string{"model", "reasoning_effort"},
        wantHeaders:   map[string]string{"Authorization": "Bearer <recorded>"},
    },
    {
        name:          "direct Anthropic",
        alias:         "anthropic",
        semanticModel: "claude-sonnet-4-5-20250929",
        deployment:    "ignored-by-generic-profile",
        wantPath:      "/v1/messages",
        wantBody: map[string]interface{}{
            "model": "claude-sonnet-4-5-20250929",
        },
        absentBody:  []string{"anthropic_version"},
        wantHeaders: map[string]string{"anthropic-version": "2023-06-01", "x-api-key": "<recorded>"},
    },
    {
        name:          "Vertex Anthropic sync",
        alias:         "anthropic.vertex",
        semanticModel: "claude-opus-4-7",
        deployment:    "claude-opus-4-7",
        wantPath:      "/v1/projects/acme-prod/locations/us/publishers/anthropic/models/claude-opus-4-7:rawPredict",
        wantBody: map[string]interface{}{
            "anthropic_version": "vertex-2023-10-16",
        },
        absentBody:    []string{"model"},
        wantHeaders:   map[string]string{"Authorization": "Bearer <recorded>"},
        absentHeaders: []string{"anthropic-version", "x-api-key"},
    },
}
~~~

The Google OpenAI guide fixture separately compiles the public helper and
asserts the global and `us-central1` URLs shown in §19.12.6, a complete
`google/...` body model, a refreshed Bearer header on every retry, and the
scoped `/reasoning_effort` policy result. Vertex fixtures add global, `us`,
`eu`, and regional host rows plus `:streamRawPredict`.

At minimum, prove:

- OpenAI and Anthropic both validate portable intent and semantic capabilities,
  resolve the route, build the provider draft, and then run application policy
  in that order for generate, stream, and fingerprint preflight;
- basic invalid intent does not invoke the resolver, while a later policy
  failure can occur after one resolver invocation, and a route error takes
  precedence over a downstream policy error;
- generic OpenAI and Anthropic profiles ignore a populated route `Deployment`
  for body construction;
- stock OpenAI reasoning uses top-level `reasoning_effort`, the correct token
  limit field, and the appropriate sampling restrictions;
- OpenAI-family reasoning classification comes from the resolved semantic
  model-family predicate, never from `ModelCapabilities.ReasoningStyle` alone;
  profile validation permits reasoning token/sampling rules without an effort
  field and ordinary sampling with a supported effort-field spelling;
- for both sync and stream, an `openai.ollama` non-reasoning model such as
  `gemma4:31b` retains `max_tokens` and ordinary temperature/sampling, emits no
  reasoning field when no effective effort is set, and emits only the nested
  `reasoning.effort` object when effort is set; it never gains
  `max_completion_tokens` or reasoning-restricted sampling merely because the
  Ollama capability row has `ReasoningStyle: "openai"`;
- an Azure semantic reasoning model mapped to an arbitrary deployment retains
  its semantic capability profile while emitting the deployment on the wire;
- Azure semantic resolution honors the immutable built-in OpenAI catalog,
  `TRUVAG3_OPENAI_MODEL_*` overrides, and unknown-model pass-through, while a
  runtime mutation of `openai.ModelAliases` continues to affect the existing
  OpenAI adapter but deliberately does not affect Azure;
- Azure and Vertex resolver fixtures receive the concrete post-alias semantic
  model in `EndpointRequest.ResolvedModel`; maps keyed by that value resolve,
  while maps keyed only by the original application alias fail with the
  provider's clear missing-deployment/publisher-model error;
- Azure deployments named `fast`, `smart`, `vision`, `code`, or `default`
  remain exact route-owned identifiers even when matching OpenAI model-alias
  environment overrides are present;
- Azure construction tests prove `ai.NewRequestClient` is the supported path,
  `ai.NewClient` returns the `CreateValidated` unsupported-construction error,
  automatic detection never selects Azure, and a direct legacy
  `Factory.Create` call has the documented guaranteed-panic behavior;
- Azure v1 API-key and Entra flows use the exact URL and header spellings;
- Azure v1 accepts an absent or singular nonempty resolver-supplied
  `api-version` without confusing it with the classic required version;
- Azure classic places deployment and API version in the route and omits the
  body model;
- Azure classic `2024-10-21` rejects semantic reasoning models before policy,
  credentials, or transport because its pinned schema does not define
  `reasoning_effort`;
- invalid Azure origins and resolved routes, including either form with an
  explicit port, fail before credential acquisition or transport;
- Google uses the documented global and regional hosts with the corresponding
  `/locations/{location}/endpoints/openapi/chat/completions` route, Bearer
  token, complete model ID, and scoped top-level reasoning patch;
- `anthropic.vertex` preserves the semantic Claude model for capabilities and
  policy while using the route-owned publisher model only in the Google Cloud
  URL, omitting body `model`, placing protected
  `anthropic_version=vertex-2023-10-16` in the body, omitting native Anthropic
  version/API-key headers, and acquiring an ADC bearer token per attempt;
- Google-hosted Claude uses `:rawPredict` for sync and `:streamRawPredict` for
  stream with the documented global, `us`/`eu` multi-region, and regional
  hosts, and direct `anthropic` requests retain their current route, body,
  headers, response decoding, and SSE behavior;
- sync and streaming produce the same profiled semantic body before protected
  streaming fields are added;
- route and surface profile changes invalidate stable fingerprints without
  exposing the raw endpoint, deployment secret, or credential scope;
- existing `openaiwire.Codec` consumers and legacy OpenAI aliases continue to
  compile and retain their documented compatibility behavior;
- module-import checks prove that `core` imports no optional module,
  `orchestration` imports neither `ai` nor `resilience`, `openaiwire` does not
  import the root `ai` package, and Phase 9 adds no `resilience` dependency;
- direct `core.GenerateAI` calls do not claim the orchestration-only prepared
  event, and legacy-representable clients retain their cache compatibility;
- common logical, provider, and attempt spans preserve parentage and propagated
  `request_id`, record every returned error in sanitized form, and do not create
  duplicate logical spans;
- BaseClient attempt spans and retry/recovery/final-failure logs never expose a
  raw current or previous error, concrete error type, request URL, query value,
  provider body, credential failure text, or callback text; returned error
  identity and retry decisions remain unchanged;
- request-scoped logs use context-aware methods, the `framework/ai` component,
  and stable `operation`; error logs also carry sanitized `error` and bounded
  `error_type`;
- provider metrics include `module=ai` plus bounded provider/status/token-type
  dimensions, but never semantic model, provider alias, surface, deployment,
  route identity, endpoint, credential scope, request ID, tenant identity,
  chain entry/transition identity, or attempt index;
- credential values, prompts, generated content, serialized request/response
  bodies, complete endpoints, query values, deployment/publisher-model IDs, and
  credential scopes never appear in common reports, observation-error
  messages, logs, metrics, or span attributes; a sanitized stable route
  identity remains allowed only in reports and spans;
- nil telemetry, a nil span returned by a custom telemetry implementation, and
  a nil logger do not change provider results, retry behavior, stream callback
  behavior, or returned errors.

Optional live smoke tests run outside ordinary CI against application-owned
Azure and Google projects. They verify IAM assignments, token audience/scope,
deployed model availability, provider-side parameter acceptance, and streaming
behavior. A live smoke test is release evidence, not a substitute for the
deterministic request-contract suite.

#### 19.12.11 Delivery slices and completion gate

The approved Phase 9 plan and the normative `ai/ARCHITECTURE.md` lifecycle
amendment land before production implementation. After that documentation gate,
land Phase 9 as five independently reviewable implementation changes:

1. common AI observability hardening: context-aware metadata helpers,
   raw-content logging removal, bounded provider metrics, sanitized provider
   and chain errors, nil-safe span normalization, chain metric hardening, and
   cross-provider observability tests, including tagged Bedrock coverage;
2. route-before-draft preparation for OpenAI and Anthropic, the
   side-effect-free OpenAI semantic alias catalog, additive typed wire profiles,
   and direct-provider regression tests;
3. Azure v1/classic adapter, strict endpoint validation, and hosted-provider
   contract fixtures;
4. Google-hosted OpenAI helpers plus the `anthropic.vertex` profile, endpoint
   validation, and hosted-provider contract fixtures;
5. provider guide reconciliation, compiled example fixtures, and architecture
   status update.

Phase 9 is complete only when all five slices pass the full repository gates,
the affected modules pass `go test -race ./...`, the cloud examples compile as
external consumers, and each remotely meaningful request shape is checked
against the current official provider documentation. The completion review must
also re-read every applicable authority in §2, confirm the dependency-import
checks above, and find no unresolved architecture delta. Human documentation
sign-off remains required before committing the Markdown changes.

### 19.13 Pull-request sequence and completion gates

Use small, reviewable PRs in this order:

1. retry replay fix and tests;
2. Anthropic shared preparation and compatibility tests;
3. additive core request/result contracts and legacy adapter tests;
4. internal policy engine plus Anthropic draft migration;
5. public rules/middleware and validated construction;
6. credentials, endpoint resolver, and HTTP injection;
7. explicit chain entries and request-aware failover;
8. common pricing/instrumentation;
9. OpenAI codec and remaining provider drafts;
10. orchestration helper, purposes, reports, and cache integration;
11. common AI observability hardening and cross-provider regression fixtures;
12. route-before-draft preparation for OpenAI and Anthropic, shared OpenAI semantic aliases, and typed wire profiles;
13. Azure v1/classic adapter and hosted-provider contract fixtures;
14. Google-hosted OpenAI and `anthropic.vertex` contract fixtures;
15. provider guide reconciliation and final architecture status update.

Each PR must keep existing constructors and interfaces compiling, pass the full
repository Go gates in `CONTRIBUTING.md`, and include race tests for retained
callbacks, middleware, credential sources, and shared chain state.
Documentation examples are added in the same phase as their public API.
Deprecation does not begin until built-in clients and orchestration use the new
path internally.

---

## 20. Test and conformance plan

### 20.1 Architecture tests

- `core` imports no optional framework module.
- `orchestration` imports no `ai` package.
- existing compile-time interface assertions continue to pass.
- legacy mocks implementing only `AIClient` remain usable.

### 20.2 Compatibility tests

- legacy zero temperature retains legacy inherit behavior;
- new explicit zero remains zero unless a compatibility rule applies;
- explicit omit never serializes the field;
- existing `Extra` and `Headers` precedence is preserved;
- existing constructors and provider factories compile unchanged;
- an external-package compile fixture using the current `AIConfig` shape still
  compiles;
- every existing `AIOption` is accepted by `NewRequestClient` as a
  `ClientOption`;
- legacy factories remain usable through `NewClient` and `NewChainClient`.

### 20.3 Policy tests

- selector matching covers provider, alias, surface, model, operation, purpose;
- patches set and remove nested fields deterministically;
- ambiguous patches fail;
- protected fields fail with actionable errors;
- built-in rules run below explicit application rules;
- middleware is ordered, concurrent-safe, and value-redacted;
- unstable middleware disables stable fingerprints.

### 20.4 Provider tests

- sync and streaming produce identical semantic requests;
- extras and headers have parity;
- resolved models are used for policy matching;
- Bedrock profile prefixes do not cause cross-provider rules;
- codecs normalize standard and extended responses;
- unsupported SDK fields fail clearly or require the custom-client path.

### 20.5 Credential and transport tests

- static and dynamic credentials are mutually well-defined;
- custom credential header suppresses default Bearer authentication;
- credential headers cannot be overwritten by legacy headers or patches;
- 401 reaches the rejection observer;
- optional auth retry is bounded to one and uses a fresh body;
- injected clients are not mutated;
- existing transports are composed, not clobbered;
- mTLS/signing transports see final serialized requests;
- secret values never appear in reports or logs.

### 20.6 Retry tests

- every 429/5xx retry receives byte-identical non-empty request content;
- network failures before and after partial writes are handled;
- semantic middleware runs once while signing runs per attempt;
- canceled contexts stop retries promptly;
- response bodies are closed on failed attempts;
- race tests cover shared clients and token sources.

### 20.7 Chain tests

- different entries receive different credentials, rules, routes, and models;
- provider-specific patches do not leak;
- input remains unchanged after failed attempts;
- two instances of the same provider can coexist;
- custom client and registered provider entries compose;
- legacy-only clients retain failover behavior;
- streaming capability is preserved per entry.

### 20.8 Observability tests

- common, provider-local, and per-attempt spans preserve their parentage and
  propagated request context without duplication;
- provider, semantic model, provider alias, surface, and sanitized route
  identity are attached only to the permitted log/report/span surfaces;
- AI metrics include `module=ai` and bounded provider/status/token-type labels,
  never semantic model, provider alias, surface, deployment, route identity,
  endpoint, credential scope, request ID, tenant identity, chain entry name,
  transition identity, or attempt index;
- requested/resolved/effective adjustments are distinguishable;
- policy fingerprints change with stable policy versions;
- dynamic policies mark fingerprints unstable;
- custom pricing resolves renamed deployments;
- nil telemetry, nil spans, and nil loggers do not fail or change the request;
- every request-scoped log is context-aware and has `operation`; error logs also
  have sanitized `error` and bounded `error_type`;
- prompts, system prompts, generated content, serialized request/response
  bodies, endpoints, query values, deployments, publisher-model IDs,
  credential scopes, and credentials are absent from common observations.

### 20.9 Orchestration tests

Cover planning, continuation planning, synthesis, streaming synthesis,
micro-resolution, semantic retry, tiered selection, compaction, distillation,
error analysis, knowledge extraction, user-memory calls, and reflection.

For each applicable call:

- purpose is correct;
- legacy fallback still works;
- request report is recorded once;
- LLM Debug deduplication remains correct;
- cache behavior includes or bypasses unstable fingerprints;
- no provider-specific package is imported.

### 20.10 Required implementation gates

Any Go implementation must pass all repository-required gates:

- `go vet`;
- `go build ./...`;
- `go test ./...`;
- `goimports`;
- `golangci-lint run`;
- `gosec`;
- `govulncheck`.

High-risk concurrency and transport changes should additionally pass
`go test -race ./...` in every affected module.

---

## 21. Acceptance criteria

The architecture is complete only when:

1. An application can use a new Anthropic model by removing a rejected optional
   field without upgrading TruvaG3.
2. An application can add a new optional provider field without a framework
   release.
3. An OAuth-secured OpenAI-compatible gateway can use the stock codec without a
   concrete-client type assertion.
4. A rejected cached token can be invalidated through an explicit response-aware
   contract.
5. A model can map to an enterprise deployment without orchestration knowing the
   deployment.
6. Sync and streaming share semantic preparation.
7. Every retry sends a fresh complete body.
8. Request policy cannot access or overwrite credentials or structural
   invariants.
9. Existing `AIClient`, `AIConfig`, custom providers, mocks, constructors, and
   chain configurations remain supported.
10. A custom client can be one named entry in a standard chain.
11. Requested, resolved, and effective request behavior is observable without
    logging secret values.
12. Renamed enterprise models can obtain application-defined cost estimates.
13. AI-semantic caches cannot silently reuse output under an incompatible
    request policy.
14. Orchestration remains provider-neutral and imports no `ai` package.
15. Unsupported provider capabilities fail before the network rather than being
    silently ignored.
16. All conformance, security, retry, streaming, chain, and orchestration tests
    pass.

---

## 22. Alternatives rejected

| Alternative | Reason rejected |
|---|---|
| Add only an Anthropic model prefix table | Necessary as a built-in default, but applications remain blocked by the next provider change. |
| Add only `WithHTTPClient` and `WithAuthHeader` | Solves part of enterprise auth but not field omission, routing, chain scoping, codecs, or reports. |
| Raw `WithRequestMutator(func(map[string]any))` | Unscoped, JSON-only, structurally unsafe, hard to validate/fingerprint, and likely to drift across sync/stream. |
| Raw `*http.Request` middleware | Exposes credentials and destinations, bypasses structural invariants, and cannot represent SDK transports. |
| Let `Extra` override every field | Breaks current precedence and can corrupt required model/message structure. |
| Use `nil` to mean remove | Confuses JSON null with absence. |
| Expand the existing `AIClient` method | Breaks every implementation and mock. |
| Add fields directly to exported legacy structs without a compatibility plan | Exported struct evolution can break unkeyed composite literals and still leaves a weak long-term seam. |
| Put advanced integration fields directly on `AIConfig` | Can break external unkeyed literals; the additive `ClientOption`/`ProviderIntegrationConfig` path preserves `AIConfig` and `NewClient`. |
| Break `ProviderFactory.Create` to return error | Unnecessary; an additive optional interface provides validation without breaking factories. |
| One giant provider integration interface | Mixes policy, routing, auth, transport, codec, pricing, and lifecycle; violates small-interface and composition principles. |
| Mutable global policy registry | Causes ordering, isolation, concurrency, and reproducibility problems. |
| Provider logic in orchestration | Violates the dependency graph and misses direct AI callers. |
| Automatically sanitize and retry every 400 | Masks genuine invalid requests and can silently change semantics. |
| Require every exception to use a custom client | Technically flexible but creates an abstraction cliff and discards useful routing, retry, and codec behavior for small deviations. |
| Fully normalize every provider feature | Creates an unbounded compatibility matrix contrary to the AI module’s “valuable, not mandatory” philosophy. |

---

## 23. Bounded implementation decisions

The architectural direction is settled by this document. Implementation may
still choose:

1. final names for the additive request/result interfaces;
2. whether parameter helpers use generics or field-specific constructors;
3. the documented logical path catalog for each SDK-backed provider surface;
4. whether immediate post-401 refresh-and-retry ships initially or remains
   opt-in later;
5. the first normalized shape for extended usage counters;
6. whether an endpoint resolver is initially public for every HTTP provider or
   introduced first for OpenAI-compatible surfaces;
7. the release in which orchestration begins preferring the new request
   capability.

These choices must not weaken the module boundary, protected invariants,
compatibility adapter, immutable retry semantics, or layered access model.

---

## 24. Final architectural position

The durable abstraction is not “Anthropic compatibility” or “enterprise
authentication.” It is a provider integration pipeline with explicit,
composable seams.

TruvaG3 should own:

- a small portable intent model;
- safe compatibility defaults;
- deterministic request preparation;
- common resilience and observability;
- normalized cross-module contracts.

Applications should own:

- provider-specific policy overlays;
- tenant and domain behavior;
- credential implementations;
- deployment maps;
- custom pricing;
- native clients when the provider surface genuinely differs.

Providers should own:

- surface-specific drafts and protected invariants;
- endpoint and SDK adaptation;
- request/response/stream codecs;
- structured provider error normalization.

Orchestration should own:

- provider-neutral call purpose;
- phase-specific portable intent;
- consumption of sanitized reports;
- cache correctness;
- injection of the application-composed `core.AIClient`.

This division follows the framework’s central rule: each module does its job,
and the application composes. It allows TruvaG3 to remain a strong default
without becoming a release bottleneck or an abstraction prison.
