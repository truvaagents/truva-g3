# AI Provider Change Playbook

AI providers change under you: a new model family rejects a parameter that
worked yesterday, an identity team rotates the auth scheme, a gateway moves
regions, a vendor ships a field you need today. This playbook is the
**event-indexed** companion to the
[AI Providers Setup Guide](AI_PROVIDERS_SETUP_GUIDE.md) and the
[Custom AI Providers and Enterprise Integration Guide](CUSTOM_AI_PROVIDER_GUIDE.md):
find the situation, apply the day-0 response, then schedule the durable fix.

Each entry follows the same shape:

- **Day-0 response** — what you can do *today*, in application code or
  configuration, without waiting for a framework release.
- **Durable fix** — the longer-term home for the change.
- **Canonical reference** — the guide section that owns the full detail.

The snippets are commented so a reader new to the AI module can apply them
without reading the full guides first. The linked guide sections remain the
authoritative documentation for each mechanism.

## Before You Start: Terms the Scenarios Use

**The client APIs.** TruvaG3 has more than one way to build an AI client, and
each scenario says which one its snippet needs:

- A **legacy client** (`ai.NewClient`) is the simple API most examples use:
  call `GenerateResponse` with a prompt and `core.AIOptions`, get text back.
  It is the right choice until you need one of the hooks below.
- A **request-aware client** (`ai.NewRequestClient`) accepts a structured
  request and adds the enterprise hooks this playbook leans on: declarative
  request rules, dynamic credentials, per-request routing, and cache-safe
  fingerprints. See
  [Understand the Request-Aware API](CUSTOM_AI_PROVIDER_GUIDE.md#3-understand-the-request-aware-api)
  for the full picture.
- A **chain client** (`ai.NewChain` or `ai.NewChainClient`) wraps several
  providers into one client with automatic failover. See
  [The Two Types of AI Clients](AI_PROVIDERS_SETUP_GUIDE.md#the-two-types-of-ai-clients).

**Blank imports.** All Go snippets assume the providers you use are activated
with blank imports — without them, client construction fails with
`provider '<name>' not registered`:

```go
import (
    "github.com/truvaagents/truva-g3/ai"
    "github.com/truvaagents/truva-g3/core"

    // Each provider registers itself when its package is imported.
    // Import the ones you actually use.
    _ "github.com/truvaagents/truva-g3/ai/providers/openai"
    _ "github.com/truvaagents/truva-g3/ai/providers/anthropic"
)
```

## Quick Index

| Something changed... | Go to |
|---|---|
| A model family started rejecting a parameter | [Scenario 1](#scenario-1-a-provider-rejects-a-parameter-it-used-to-accept) |
| A provider shipped a new parameter you want now | [Scenario 2](#scenario-2-a-provider-added-a-field-you-want-to-use-today) |
| A new model was released (or one you use has issues) | [Scenario 3](#scenario-3-a-new-model-was-released--or-you-need-to-roll-one-back) |
| A new provider or vendor appeared | [Scenario 4](#scenario-4-a-new-provider-appeared) |
| Authentication changed (rotation, OAuth, new header) | [Scenario 5](#scenario-5-authentication-changed) |
| The endpoint, gateway, or region moved | [Scenario 6](#scenario-6-the-endpoint-or-gateway-moved) |
| Responses carry new or unexpected fields | [Scenario 7](#scenario-7-the-response-envelope-changed) |
| You changed AI behavior and worry about stale caches | [Scenario 8](#scenario-8-you-changed-ai-behavior-and-caches-must-not-serve-stale-answers) |
| A provider is degrading or drifting in production | [Scenario 9](#scenario-9-a-provider-is-degrading-or-drifting-in-production) |
| None of the hooks can express the change | [When the Playbook Is Not Enough](#when-the-playbook-is-not-enough) |

---

## Scenario 1: A Provider Rejects a Parameter It Used to Accept

**Situation:** A newly released model family returns 400 for a parameter your
requests always sent — for example, a sampling control such as `temperature`.
This has happened in production before; it will happen again.

**Day-0 response:** attach a scoped removal rule to a
[request-aware client](#before-you-start-terms-the-scenarios-use). No
framework upgrade, no call-site changes — orchestration and agents keep
sending the same portable intent, and the rule strips the field for exactly
the affected family:

```go
client, err := ai.NewRequestClient(
    ai.WithProvider("openai"),
    // A request rule is a named, versioned patch applied to matching
    // requests just before they are encoded for the wire.
    ai.WithRequestRules(core.AIProviderPatch{
        // Name identifies the rule in reports and traces. Version joins the
        // cache fingerprint — bump it whenever you change the rule.
        Name:    "gpt6-sampling-compatibility",
        Version: "1",
        // The Selector scopes the rule: only requests matching ALL of its
        // fields are touched; everything else passes through unchanged.
        Selector: core.AIProviderSelector{
            Provider: "openai",           // provider identity
            Surface:  "chat-completions", // the provider's wire API shape
            Model:    "gpt-6*",           // glob: matches the whole family
        },
        // Fields to delete from the outgoing request body, addressed by
        // JSON Pointer: "/temperature" is the top-level "temperature" key.
        Remove: []string{"/temperature", "/top_p"},
    }),
)
```

`Surface` names the wire API a provider speaks: `"chat-completions"` for
OpenAI-style providers, `"messages"` for Anthropic, `"converse"` for Bedrock.
The same mechanism works for every request-aware provider, but JSON Pointer
paths are contracts of that provider surface. Confirm the provider's draft and
protected-field rules before copying a path such as `/temperature`; then change
the selector's `Provider` and `Surface` to match. Because the rule's name and
version join the policy fingerprint (the cache key explained in
[Scenario 8](#scenario-8-you-changed-ai-behavior-and-caches-must-not-serve-stale-answers)),
AI-output caches roll over automatically instead of serving answers produced
under the old semantics.

**Durable fix:** upgrade to the framework release that adds the family to the
provider's built-in compatibility rules, then delete your rule. Keeping the
built-in list current gives every caller identical sync/stream behavior.

**Canonical reference:**
[Anthropic Sampling Compatibility](AI_PROVIDERS_SETUP_GUIDE.md#anthropic-sampling-compatibility)
(the incident that motivated this mechanism) and
[Declarative Request Rules](CUSTOM_AI_PROVIDER_GUIDE.md#declarative-request-rules).

---

## Scenario 2: A Provider Added a Field You Want to Use Today

**Situation:** The provider documents a new top-level request field — a
verbosity knob, a routing hint — and TruvaG3 has no portable option for it
yet.

**Day-0 response (legacy client):** on the simple `ai.NewClient` API (see
[the terms above](#before-you-start-terms-the-scenarios-use)), pass the field
through `Extra`. Unknown fields merge into the request body unless they
collide with structure the framework owns, such as the model and messages:

```go
response, err := client.GenerateResponse(ctx, prompt, &core.AIOptions{
    Model: "smart", // portable model alias (see Scenario 3)
    // Extra fields merge into the top level of the request body: the
    // provider receives {"model": ..., "messages": ..., "verbosity": "low"}.
    Extra: map[string]interface{}{
        "verbosity": "low",
    },
})
```

**Day-0 response (request-aware client):** on `ai.NewRequestClient` (see
[the terms above](#before-you-start-terms-the-scenarios-use)), prefer a named,
versioned rule so the change is scoped, reported, and cache-visible:

```go
client, err := ai.NewRequestClient(
    ai.WithProvider("openai"),
    ai.WithRequestRules(core.AIProviderPatch{
        Name:    "enable-verbosity",
        Version: "1",
        // Scoped exactly like Scenario 1's rule: only requests matching all
        // three selector fields carry the new field.
        Selector: core.AIProviderSelector{
            Provider: "openai",
            Surface:  "chat-completions",
            Model:    "gpt-6*",
        },
        // Set writes a value at a JSON Pointer path in the request body —
        // here it adds a top-level "verbosity" field to matching requests.
        Set: map[string]interface{}{
            "/verbosity": "low",
        },
    }),
)
```

Scope the rule to the models that actually support the field — an
OpenAI-compatible service returning HTTP 200 does not prove it honored the
field. Add a contract test for any field your application depends on.

**Durable fix:** if the field is genuinely portable across providers, propose
it as a typed option; provider-specific fields stay as rules or `Extra`.

**Canonical reference:**
[Portable Fields vs Provider-Specific Escape Hatches](AI_PROVIDERS_SETUP_GUIDE.md#portable-fields-vs-provider-specific-escape-hatches).

---

## Scenario 3: A New Model Was Released — or You Need to Roll One Back

**Situation:** You want traffic on a newly released model, or the model you
adopted last week has quality issues and you need yesterday's model back.

**Day-0 response:** no code at all. Model aliases are resolved through
environment variables, so a rollout or rollback is a config change and a
rolling restart:

```bash
# Call sites ask for a portable alias ("smart", "fast", "default"); these
# variables decide which concrete model each alias resolves to, per provider.

# Adopt the new model
export TRUVAG3_OPENAI_MODEL_SMART=gpt-6

# Roll back after issues
export TRUVAG3_OPENAI_MODEL_SMART=o3

# Catalog-backed direct providers and OpenAI-compatible aliases use this
# pattern (strip the "openai." prefix for OpenAI-compatible sub-providers).
export TRUVAG3_ANTHROPIC_MODEL_SMART=claude-sonnet-4-5-20250929
export TRUVAG3_GROQ_MODEL_DEFAULT=llama-3.1-8b-instant
```

This shortcut is not universal. Bedrock has no portable model-alias catalog.
Azure OpenAI resolves semantic models through the built-in OpenAI catalog and
`TRUVAG3_OPENAI_MODEL_*`; `anthropic.vertex` resolves through the Anthropic
catalog and `TRUVAG3_ANTHROPIC_MODEL_*`. Hosted resolvers map those resolved
semantic model IDs—not aliases such as `smart`—to deployments or publisher
model IDs.

Keep call sites on portable aliases (`smart`, `fast`, `default`) — that is
what makes this play possible and is the normal choice for heterogeneous
failover. A concrete model can be used only when every chain entry accepts the
same literal ID.

If the new model also *behaves* differently on the wire (rejects a parameter,
needs a field), combine this with [Scenario 1](#scenario-1-a-provider-rejects-a-parameter-it-used-to-accept)
or [Scenario 2](#scenario-2-a-provider-added-a-field-you-want-to-use-today).

**Durable fix:** when the model choice stabilizes, bake it into the
environment ConfigMap for each deployment tier.

**Canonical reference:**
[Model Aliases](AI_PROVIDERS_SETUP_GUIDE.md#model-aliases-portable-model-names) and
[Environment Variable Overrides](AI_PROVIDERS_SETUP_GUIDE.md#environment-variable-overrides).

---

## Scenario 4: A New Provider Appeared

**Situation:** A new vendor you want to try, an internal model platform, or an
OpenAI-compatible service TruvaG3 has no alias for.

The two day-0 responses below answer different questions, and most rollouts
use them in sequence: the first gets you a client that can talk to the new
vendor at all; the second puts that client into production without betting
everything on an untested vendor.

**Day-0 response (get a client talking to the vendor):** most new vendors
launch with an OpenAI-compatible API, and TruvaG3's generic `openai` provider
is not hardwired to api.openai.com — it speaks that wire format to any base
URL you give it:

```go
// "openai" here selects the wire format, not the company: the generic
// provider sends standard chat-completions requests to whatever base URL
// you configure.
newVendorClient, err := ai.NewClient(
    ai.WithProvider("openai"),
    ai.WithBaseURL("https://api.newvendor.ai/v1"), // the vendor's endpoint
    ai.WithAPIKey(os.Getenv("NEWVENDOR_API_KEY")), // the vendor's key, not OpenAI's
    ai.WithModel("newvendor-large-1"),             // concrete model name from the vendor's docs
)
```

This alone is enough to try the vendor from a script or a single service;
nothing about your existing deployment changes.

**Day-0 response (adopt it without betting production on it):** a brand-new
vendor is exactly what you do not want as your only provider. Put the client
you just built into a failover chain behind your proven provider. The chain
API takes two kinds of entries:

```go
chain, err := ai.NewChain(
    // ProviderEntry: "framework, build a client for this provider alias."
    ai.ProviderEntry("primary", "openai"),

    // ClientEntry: "here is a client I already built — use it as-is." The
    // chain never reconfigures an injected client's model, URL, or
    // credentials; it only decides when to call it. Entries fail over in
    // order, so this one is reached only when "primary" fails.
    ai.ClientEntry("newvendor-trial", newVendorClient),
)
```

The entry names (`"primary"`, `"newvendor-trial"`) are operator labels: they
identify each entry in failover reports, logs, and traces, so pick stable,
meaningful ones.

**Durable fix:** both responses share one limitation — the new vendor has no
identity of its own, so reports, request-rule selectors, traces, and cache
fingerprints all see it as `openai`. When the vendor deserves its own name in
those places, or its API is not OpenAI-compatible at all, implement a small
provider factory. OpenAI-compatible adapters reuse the public `openaiwire`
codec and write no wire code; SDK-native providers follow the Bedrock draft
pattern. Both are application-local packages registered with a blank import:
no framework fork required.

**Canonical reference:**
[Adding New OpenAI-Compatible Services](../../ai/README.md#adding-new-openai-compatible-services),
[Reuse the OpenAI-Compatible Codec](CUSTOM_AI_PROVIDER_GUIDE.md#reuse-the-openai-compatible-codec), and
[Adapt an SDK-Native Provider](CUSTOM_AI_PROVIDER_GUIDE.md#adapt-an-sdk-native-provider).

---

## Scenario 5: Authentication Changed

**Situation:** Static API keys are being replaced with short-lived OAuth or
cloud-identity tokens; or the gateway expects a different header; or security
requires rotation without redeploys.

**Day-0 response:** supply the credential dynamically. The callback runs for
**every transport attempt**, so rotation and retries are safe by construction:

```go
client, err := ai.NewRequestClient(
    ai.WithProvider("openai"),
    // WithAuthHeader takes the header name to send and a callback that
    // produces its value. The framework invokes the callback before every
    // transport attempt, so a token rotated between attempts is picked up
    // automatically.
    ai.WithAuthHeader("Authorization", func(ctx context.Context) (string, error) {
        // Fetch or reuse a token. Caching and refresh are application-owned —
        // this runs on the request path, so keep it fast.
        token, err := tokenSource.AccessToken(ctx)
        if err != nil {
            return "", err // the request fails before anything is sent
        }
        return "Bearer " + token, nil // the exact header value to send
    }),
)
```

A dynamic credential automatically replaces the provider's static-key header —
do not set `WithAPIKey` alongside it. If the provider expects a different
spelling (for example Azure's `api-key`), change the header name; the value
never appears in reports, fingerprints, logs, or traces.

**Durable fix:** when credential selection depends on the route (scopes,
deployments) or you must invalidate cached tokens on HTTP 401/403, implement
`ai.CredentialSource` and `ai.CredentialRejectionObserver` on one value.

**Canonical reference:**
[Dynamic Credentials](CUSTOM_AI_PROVIDER_GUIDE.md#dynamic-credentials) and
[React to Rejected Credentials](CUSTOM_AI_PROVIDER_GUIDE.md#react-to-rejected-credentials).

---

## Scenario 6: The Endpoint or Gateway Moved

**Situation:** Traffic must move to a new gateway, a regional endpoint, or a
migrated URL — sometimes per tenant or per request.

**Day-0 response (one static URL):** direct HTTP aliases that expose a static
base URL can be moved through their documented environment override:

```bash
export OPENAI_BASE_URL=https://ai-gateway.company.internal/openai/v1
export GROQ_BASE_URL=https://ai-gateway.company.internal/groq/openai/v1
```

This does not apply to every provider profile. Azure OpenAI and
`anthropic.vertex` require `EndpointResolver`. Bedrock's AWS service endpoint,
region, credentials, signing, and transport remain AWS SDK configuration, but
its semantic-model-to-`modelId` selection may use a constrained
`EndpointResolver`. Use the hosted-cloud recipes in the custom-provider guide
for those surfaces.

**Day-0 response (per-request routing):** an `EndpointResolver` owns the
complete request URL and gives the route a stable, non-secret identity:

```go
// An EndpointResolver takes over URL construction entirely: the URL it
// returns is used verbatim — the framework appends no path of its own.
type gatewayResolver struct {
    endpoint *url.URL // complete URL, including the request path
}

// The EndpointRequest argument describes the outgoing call (provider,
// resolved model, surface, operation) — use it when routing depends on the
// request, e.g. per-tenant or per-model gateways.
func (r *gatewayResolver) ResolveEndpoint(
    _ context.Context,
    _ ai.EndpointRequest,
) (ai.ResolvedEndpoint, error) {
    return ai.ResolvedEndpoint{
        URL: r.endpoint,
        // A stable, non-secret label for this route. It joins the cache
        // fingerprint — bump it when a routing change can affect answers
        // (different backend, deployment, or api-version).
        RouteIdentity: "gateway-eu-v2",
    }, nil
}

client, err := ai.NewRequestClient(
    ai.WithProvider("openai"),
    ai.WithEndpointResolver(&gatewayResolver{endpoint: gatewayURL}),
)
```

The HTTP example above must return a complete URL. Bedrock uses the same shared
resolver at a narrower SDK boundary: return no URL, query, or credential scope;
put the exact foundation-model, inference-profile, application-profile, or
provisioned-model ID/ARN in `Deployment`:

```go
type bedrockResolver struct {
    routes map[string]string // keyed by post-default semantic model ID
}

func (resolver *bedrockResolver) ResolveEndpoint(
    _ context.Context,
    request ai.EndpointRequest,
) (ai.ResolvedEndpoint, error) {
    modelID, ok := resolver.routes[request.ResolvedModel]
    if !ok {
        return ai.ResolvedEndpoint{}, fmt.Errorf(
            "no Bedrock route for semantic model %q",
            request.ResolvedModel,
        )
    }
    return ai.ResolvedEndpoint{
        Deployment:    modelID,
        RouteIdentity: "bedrock-us-primary-v2",
    }, nil
}
```

Change `RouteIdentity` whenever the mapping changes in a way that can affect
answers. Never put a raw ARN, account ID, tenant ID, or credential in it.

Resolvers must be deterministic, concurrency-safe, and free of side effects —
the framework may call them during cache-fingerprint preflight as well as on
the live request.

**Durable fix:** for HTTP providers using corporate CAs, mTLS, or proxies, pair
the resolver with `ai.WithHTTPClient(corporateHTTPClient)`. For Bedrock, change
the application-owned `aws.Config`; the Bedrock factory rejects the shared HTTP
and credential hooks.

**Canonical reference:**
[Per-Request Endpoint Routing](CUSTOM_AI_PROVIDER_GUIDE.md#per-request-endpoint-routing) and
[Custom HTTP Clients](CUSTOM_AI_PROVIDER_GUIDE.md#custom-http-clients). For
Bedrock, see
[AWS Bedrock SDK-Native Routing](CUSTOM_AI_PROVIDER_GUIDE.md#aws-bedrock-sdk-native-routing).

---

## Scenario 7: The Response Envelope Changed

**Situation:** The provider added response members — content filters, service
tiers, new usage counters — or a gateway wraps responses with extra metadata.

**Day-0 response:** usually nothing when the new fields are optional. The
built-in decoders read the documented chat contract and **ignore unknown
response fields**, so additive envelope changes do not break requests.
Adapters normalize usage fields they explicitly understand; the open counters
map carries only counters an adapter deliberately maps, not every unknown JSON
member:

```go
result, err := core.GenerateAI(ctx, client, request)
if err == nil && result.UsageDetails != nil {
    // Well-known usage numbers have typed fields...
    cached := result.UsageDetails.CachedInputTokens
    // ...and provider adapters may expose additional reviewed counters in the
    // open Counters map.
    for name, value := range result.UsageDetails.Counters {
        recordUsageMetric(name, value) // e.g. "cache_write_input_tokens"
    }
    _ = cached
}
```

**Durable fix:** if your application must consume a newly documented usage
counter, update and test the owning adapter's normalization. If it must consume
a non-standard response member (not just tolerate it), that requirement crosses
the portable `core.AIResult` boundary—implement a custom provider adapter with
its own result contract rather than parsing raw bodies around the framework.

**Canonical reference:**
[Know the Compatibility Boundary](CUSTOM_AI_PROVIDER_GUIDE.md#know-the-compatibility-boundary).

---

## Scenario 8: You Changed AI Behavior and Caches Must Not Serve Stale Answers

**Situation:** You edited a request rule, switched models, changed middleware,
or re-routed traffic — and the result-distillation, conversation-summary, and
activity-digest caches hold answers generated under the *old* behavior.

**Day-0 response:** version your change; the caches take care of themselves.
Every rule's name and version, the resolved model, deterministic middleware
versions, and the route identity are part of the request's policy fingerprint.
AI-output caches key on it, so a semantic change is a cache miss, not a stale
hit:

```go
ai.WithRequestRules(core.AIProviderPatch{
    Name:    "json-for-extraction",
    // Was "1". The bump alone is what rolls the caches over: the name and
    // version are part of the cache key, so every cached answer produced
    // under version "1" simply stops matching.
    Version: "2",
    // ... same selector, changed Set/Remove ...
})
```

Two rules to keep this working:

- **Bump `Version` on every semantic rule change.** Rule *values* are
  deliberately excluded from the fingerprint; the version is the change
  signal.
- **Bump `RouteIdentity`** when a routing change (different deployment,
  api-version, backend) can affect output.

If a client cannot produce a stable fingerprint (for example, unversioned
middleware), the caches bypass reads *and* writes — safe, but you lose cache
hits until the behavior is versioned.

**Durable fix:** none needed — this is the steady-state mechanism. Treat
versionless behavior changes as the bug.

**Canonical reference:**
[AI-Output Caches](CUSTOM_AI_PROVIDER_GUIDE.md#ai-output-caches).

---

## Scenario 9: A Provider Is Degrading or Drifting in Production

**Situation:** Elevated errors from one provider, odd new failure modes, or a
suspicion that the provider quietly changed behavior under your requests.

**Day-0 response (availability):** put a second provider behind the first
with a [chain client](#before-you-start-terms-the-scenarios-use). Portable
aliases make the same request work on both:

```go
// One client, two providers: every request tries "openai" first and fails
// over to "anthropic" when the failure is one another provider could
// survive (outages, auth failures, rate limits — not malformed requests).
// Portable model aliases ("smart", "fast") resolve per provider, so the
// same request is valid on both sides of the failover.
client, err := ai.NewChainClient(
    ai.WithProviderChain("openai", "anthropic"),
)
```

**Day-0 response (diagnosis):** classify failures structurally instead of
string-matching. Capability refusals and provider errors are both typed:

```go
result, err := core.GenerateAI(ctx, client, request)
if err != nil {
    // errors.Is answers "is this a capability refusal?"...
    if errors.Is(err, core.ErrAIRequestFeatureUnsupported) {
        // This client cannot represent the request — a capability gap,
        // not an outage. Retrying the same provider will not help.
    }
    // ...and errors.As extracts the typed provider error, carrying the
    // HTTP status and the framework's classification of the failure.
    var providerErr core.ProviderError
    if errors.As(err, &providerErr) {
        log.Warn("provider failure", map[string]interface{}{
            "status":    providerErr.StatusCode(),
            "provider":  providerErr.Provider(),
            "transient": providerErr.IsTransient(), // proxy/CDN hiccup
            "retryable": providerErr.IsRetryable(), // e.g. billing exhaustion
        })
    }
}
_ = result
```

**Day-0 response (drift tripwire):** strict compatibility mode turns silent
provider-side adjustments into visible pre-network failures. If a built-in
compatibility rule ever starts modifying your explicit request intent — a
sign the provider's contract moved — the call fails loudly instead of
behaving differently:

```go
import "github.com/truvaagents/truva-g3/ai/requestpolicy"

// Compatible (the default) lets built-in rules quietly adjust requests to
// keep them valid for the provider. Strict turns any such adjustment of
// your explicit request intent into a pre-network failure instead — drift
// becomes visible rather than silent.
client, err := ai.NewRequestClient(
    ai.WithProvider("anthropic"),
    ai.WithCompatibilityMode(requestpolicy.CompatibilityStrict),
)
```

**Durable fix:** alert on failover rates and on `IsRetryable` provider errors;
review strict-mode failures as provider-contract change reports.

**Canonical reference:**
[Understanding Failover Behavior](AI_PROVIDERS_SETUP_GUIDE.md#understanding-failover-behavior),
[Error Classification Reference](AI_PROVIDERS_SETUP_GUIDE.md#error-classification-reference), and
[Compatible vs Strict Mode](CUSTOM_AI_PROVIDER_GUIDE.md#compatible-vs-strict-mode).

---

## When the Playbook Is Not Enough

Some provider changes cannot be absorbed by rules, credentials, or routing —
by design. Provider drafts — the wire-request skeleton a provider builds
before your rules are applied — protect structural fields (`/model`,
`/messages`, `/stream`, credential and content-type headers), so no patch can
remove the body model a surface requires or forge transport structure. When a provider
change collides with protected structure — a hosted surface that forbids the
body `model`, a new wire contract, an SDK migration — the escalation path is a
narrowly scoped custom adapter that reuses the framework's codec, policy,
retry, and credential planes:

- [The Three Integration Levels](CUSTOM_AI_PROVIDER_GUIDE.md#the-three-integration-levels)
  — confirm you actually need Level 3.
- [Implement a New Provider](CUSTOM_AI_PROVIDER_GUIDE.md#7-scenario-4-implement-a-new-provider)
  — the full adapter contract.
- [Choose a Hosted Cloud Recipe](CUSTOM_AI_PROVIDER_GUIDE.md#choose-a-hosted-cloud-recipe)
  — Azure, Google, and enterprise-gateway specifics.

If the change breaks one of this playbook's entries — a play that no longer
works as written — treat that as a documentation bug and fix this file in the
same change that adapts the code.

## See Also

- [AI Providers Setup Guide](AI_PROVIDERS_SETUP_GUIDE.md) — configuration,
  aliases, failover, and operations
- [Custom AI Providers and Enterprise Integration Guide](CUSTOM_AI_PROVIDER_GUIDE.md)
  — request-aware contracts, policy, credentials, routing, and adapters
- [ai/README.md](../../ai/README.md)
  — AI module overview
