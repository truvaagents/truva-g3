# AI Provider Extensibility Architecture

**Status:** Proposed architecture

**Date:** 2026-07-18

**Scope:** `core`, `ai`, and the AI-facing boundary of `orchestration`

**Purpose:** Define a durable provider-integration architecture that lets
applications adapt to provider changes without waiting for a framework release,
while preserving safety, portability, retries, streaming, failover, and
observability.

**Implementation status:** In progress. Phase 0B is implemented and locally
verified as of 2026-07-18, pending review and commit; Section 19 contains the
remaining code-level implementation blueprint. Public names remain proposed
until final API review.

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

This proposal is subordinate to and designed to align with:

- [Framework Design Principles](../../FRAMEWORK_DESIGN_PRINCIPLES.md)
- [AI Module Architecture](../ARCHITECTURE.md)
- [Core Module Architecture](../../core/ARCHITECTURE.md)
- [Orchestration Module Architecture](../../orchestration/ARCHITECTURE.md)

If accepted, the AI and core architecture documents should later summarize and
link to this design. The two incident notes should remain as case studies and
implementation evidence rather than being deleted or merged.

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

§2.1 maps this design to the governing principles at a glance. This section is the
deeper conformance assessment against the three authority documents — where the
design conforms, the genuine tensions, the existing violations it corrects, and
the guardrails that keep the recommended MVP subset (defined below) aligned. It
reflects an independent architecture review (2026-07-18).

**Sources:** [FRAMEWORK_DESIGN_PRINCIPLES.md](../../FRAMEWORK_DESIGN_PRINCIPLES.md)
(FDP), [ai/ARCHITECTURE.md](../ARCHITECTURE.md),
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
  independently reviewable PRs in §19.12; every PR keeps the tree compiling and
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

**Status:** Implemented and locally verified (2026-07-18); pending review and
commit.

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

`firstUnsupportedLegacyFeature` detects `Omit`, `TopP`, `TopK`, and provider
patches because a legacy-only client cannot prove that it honored them.
`toLegacyOptions` then overlays representable presence-aware set values on the
cloned legacy options. This prevents a capability fallback from silently
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
deferral/deduplication and to attach the report to telemetry. A corresponding
stream helper prefers `core.StreamingAIRequestClient`, falls back to
`core.StreamingAIClient` only when the request can be represented faithfully,
and returns `ErrAIRequestFeatureUnsupported` otherwise.

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

### 19.12 Pull-request sequence and completion gates

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
10. orchestration helper, purposes, reports, and cache integration.

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

- provider/model/surface labels are correct;
- requested/resolved/effective adjustments are distinguishable;
- policy fingerprints change with stable policy versions;
- dynamic policies mark fingerprints unstable;
- custom pricing resolves renamed deployments;
- telemetry failure does not fail the request;
- prompts, bodies, endpoints with secrets, and credentials are absent.

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
