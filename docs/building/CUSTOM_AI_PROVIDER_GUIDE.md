# Custom AI Providers and Enterprise Integration Guide

Welcome to the TruvaG3 custom AI provider guide! This guide begins where the
[AI Providers Setup Guide](AI_PROVIDERS_SETUP_GUIDE.md#request-aware-and-custom-integrations)
ends. It covers enterprise integration hooks and the contracts needed to add a
provider without coupling agents or orchestration code to its SDK or wire
format.

If you only need API keys, provider aliases, model selection, timeouts, or
ordinary failover, stay with the setup guide. A new provider factory is the
last extension point, not the first.

Use this guide in two ways:

- **Extending a built-in provider:** read Sections 2 through 6, then the
  end-to-end example in Section 10.
- **Implementing a new provider:** start with the same foundation, then follow
  Sections 7 through 9 for adapter, retry, security, cache, and telemetry
  contracts.

## Table of Contents

1. [Why This Guide Exists](#1-why-this-guide-exists)
2. [Build the Right Mental Model](#2-build-the-right-mental-model)
   - [A Running Example](#a-running-example)
   - [The Three Integration Levels](#the-three-integration-levels)
   - [Choose a Scenario](#choose-a-scenario)
   - [What This API Does Not Cover](#what-this-api-does-not-cover)
   - [Key Terms in Plain English](#key-terms-in-plain-english)
3. [Understand the Request-Aware API](#3-understand-the-request-aware-api)
   - [Why Ordinary Options Are Not Enough](#why-ordinary-options-are-not-enough)
   - [The Request Lifecycle](#the-request-lifecycle)
   - [Presence-Aware Parameters](#presence-aware-parameters)
   - [Safe Dispatch and Feature Refusal](#safe-dispatch-and-feature-refusal)
4. [Scenario 1: Add Enterprise Credentials and Routing](#4-scenario-1-add-enterprise-credentials-and-routing)
   - [Dynamic Credentials](#dynamic-credentials)
   - [React to Rejected Credentials](#react-to-rejected-credentials)
   - [Per-Request Endpoint Routing](#per-request-endpoint-routing)
   - [Custom HTTP Clients](#custom-http-clients)
   - [Choose a Hosted Cloud Recipe](#choose-a-hosted-cloud-recipe)
   - [Azure OpenAI v1](#azure-openai-v1)
   - [Azure OpenAI Classic Deployment API](#azure-openai-classic-deployment-api)
   - [OAuth-Protected Azure-Style Enterprise Gateway](#oauth-protected-azure-style-enterprise-gateway)
   - [Google Cloud OpenAI Compatibility](#google-cloud-openai-compatibility)
   - [Google-hosted Anthropic Claude](#google-hosted-anthropic-claude)
   - [AWS Bedrock SDK-Native Routing](#aws-bedrock-sdk-native-routing)
   - [Know the Compatibility Boundary](#know-the-compatibility-boundary)
5. [Scenario 2: Apply Request Rules and Middleware](#5-scenario-2-apply-request-rules-and-middleware)
   - [Declarative Request Rules](#declarative-request-rules)
   - [Policy Order and Protected Fields](#policy-order-and-protected-fields)
   - [Compatible vs Strict Mode](#compatible-vs-strict-mode)
   - [Request Middleware](#request-middleware)
6. [Scenario 3: Build a Heterogeneous Failover Chain](#6-scenario-3-build-a-heterogeneous-failover-chain)
7. [Scenario 4: Implement a New Provider](#7-scenario-4-implement-a-new-provider)
   - [What the Provider Owns](#what-the-provider-owns)
   - [A Practical Package Layout](#a-practical-package-layout)
   - [Choose the Adapter Shape](#choose-the-adapter-shape)
   - [Provider Factory Contracts](#provider-factory-contracts)
   - [Implement the Legacy Compatibility Bridge](#implement-the-legacy-compatibility-bridge)
   - [Reuse the OpenAI-Compatible Codec](#reuse-the-openai-compatible-codec)
   - [Adapt Provider-Specific HTTP JSON](#adapt-provider-specific-http-json)
   - [Adapt an SDK-Native Provider](#adapt-an-sdk-native-provider)
   - [Return Chain-Aware Errors](#return-chain-aware-errors)
   - [Implement Semantic Fingerprinting](#implement-semantic-fingerprinting)
   - [Test the Provider Contract](#test-the-provider-contract)
8. [Retries and Replayable Request Bodies](#8-retries-and-replayable-request-bodies)
9. [Security, Caching, and Observability](#9-security-caching-and-observability)
   - [Sanitized Reports and Fingerprints](#sanitized-reports-and-fingerprints)
   - [AI-Output Caches](#ai-output-caches)
   - [Distributed Tracing](#distributed-tracing)
   - [Logging and Metrics](#logging-and-metrics)
10. [Putting It Together: The Acme Gateway](#10-putting-it-together-the-acme-gateway)
11. [Troubleshooting Common Issues](#11-troubleshooting-common-issues)
12. [Production Review Checklist](#12-production-review-checklist)
13. [Quick Reference](#13-quick-reference)
14. [See Also](#14-see-also)

---

## 1. Why This Guide Exists

Enterprise AI integration is rarely just a different base URL. A production
deployment may also need:

- short-lived credentials that rotate without rebuilding a client
- tenant-, region-, or deployment-aware endpoint selection
- organization-wide request policy and provider compatibility rules
- failover across providers with different authentication and routing
- a private provider identity even when the wire format resembles OpenAI
- safe cache invalidation when policy or routing changes
- traces that explain the effective request without exposing secrets

Without a shared extension model, these concerns tend to spread into agents,
prompt builders, and orchestration code. That makes provider changes risky and
causes sync, streaming, retry, and failover behavior to drift apart.

TruvaG3 keeps the boundary explicit:

```text
Agent or orchestrator
        │
        ▼
core.AIRequest                  provider-neutral intent
        │
        ▼
core.GenerateAI / StreamAI     capability-aware dispatch
        │
        ▼
Semantic model + route         provider identity and wire destination
        │
        ▼
Provider wire profile          semantic model → protected wire shape
        │
        ▼
Provider draft                 provider-local logical request
        │
        ├── built-in compatibility rules
        ├── application rules
        ├── middleware
        └── per-request patches
        │
        ▼
Credentials → transport
        │
        ▼
core.AIResult                  normalized response + sanitized report
```

The important design rule is simple: high-level framework code speaks in
`core` types. Provider SDKs, wire envelopes, authentication headers, and
endpoint details stay below the provider boundary.

---

## 2. Build the Right Mental Model

Before looking at interfaces, separate **provider behavior** from **enterprise
deployment behavior**:

- Provider behavior defines the request shape, response shape, supported
  parameters, model rules, and error semantics.
- Enterprise deployment behavior decides where the request goes, how it is
  authenticated, which HTTP transport it uses, and which fallback runs next.

Keeping those concerns separate is the central idea of this guide. It lets an
organization change gateways or rotate credentials without rewriting the
provider, and it lets a provider adapter evolve without changing agent code.

### A Running Example

Imagine Acme Corp uses an internal AI gateway:

- the gateway accepts OpenAI chat-completions requests
- US and EU tenants must use different endpoints
- an identity service issues a short-lived token for each HTTP attempt
- extraction requests must ask for JSON
- a second OpenAI-compatible Acme deployment is an acceptable fallback when
  the primary route is unavailable

At first glance this sounds like a custom provider. It is not. The request and
response wire format is still OpenAI, so the built-in OpenAI adapter remains
the correct provider. Acme only needs:

```text
OpenAI provider behavior
        +
Acme endpoint resolver       where should this request go?
        +
Acme credential source       how should this attempt authenticate?
        +
Acme request rule            what organization policy applies?
        +
Secondary Acme entry         what should run if the first entry fails?
```

Later, suppose Acme replaces the gateway with an SDK that has its own request
objects and streaming events. That is when Acme needs a provider factory and
adapter.

The rest of this guide develops those two versions of the example: first
extending a built-in provider, then implementing a genuinely new one.

### The Three Integration Levels

| Level | What changes | What you use |
|---|---|---|
| 1. Configure | API key, model, timeout, or static base URL | The ordinary setup guide and `ai.NewClient` |
| 2. Extend | Policy, short-lived credentials, dynamic routes, custom HTTP transport, or independent failover entries | A built-in provider with `ai.NewRequestClient` or `ai.NewChain` |
| 3. Implement | Provider identity, model rules, wire format, SDK types, response decoding, or error translation | A custom `RequestProviderFactory` and provider adapter |

Use Level 2 whenever the built-in provider still describes the service
honestly. Move to Level 3 only when the provider itself is different.

The request-aware OpenAI, Azure OpenAI, and Anthropic clients support Level 2
HTTP integrations. Bedrock supports request policy and SDK-destination routing
under the `bedrock` build tag, but rejects HTTP and credential hooks because
the AWS SDK owns those concerns. Gemini remains legacy-only and can be included
in a heterogeneous chain with `ClientEntry`.

### Choose a Scenario

Use this matrix as the entry point. Every framework-supplied extension is
documented here so an integration author should not need to inspect a built-in
provider to discover the contract.

| Need | Solution | Start here |
|---|---|---|
| Static API key, model, timeout, or base URL | Configure a built-in provider | [AI Providers Setup Guide](AI_PROVIDERS_SETUP_GUIDE.md) |
| Token changes over time | `CredentialSource` or `WithAuthHeader` | [Dynamic Credentials](#dynamic-credentials) |
| Invalidate a token after HTTP 401/403 | `CredentialRejectionObserver` | [React to Rejected Credentials](#react-to-rejected-credentials) |
| Route each request to a tenant, region, or deployment | `EndpointResolver` | [Per-Request Endpoint Routing](#per-request-endpoint-routing) |
| Corporate CA, mTLS, proxy, or custom connection pool | `WithHTTPClient` | [Custom HTTP Clients](#custom-http-clients) |
| Connect to Azure OpenAI v1 with an API key or Microsoft Entra token | `azureopenai.v1` plus an `EndpointResolver` | [Azure OpenAI v1](#azure-openai-v1) |
| Connect to the Azure classic deployment URL with `api-version` | `azureopenai.classic` plus an `EndpointResolver` | [Azure OpenAI Classic Deployment API](#azure-openai-classic-deployment-api) |
| Exchange enterprise OAuth credentials, then send the token as `api-key` | Application token manager plus `CredentialSource` | [OAuth-Protected Azure-Style Enterprise Gateway](#oauth-protected-azure-style-enterprise-gateway) |
| Connect to a Google-hosted OpenAI-compatible endpoint | OpenAI provider plus an application-owned Google access-token source | [Google Cloud OpenAI Compatibility](#google-cloud-openai-compatibility) |
| Connect to Anthropic Claude hosted on Google Vertex AI | `anthropic.vertex` plus a publisher-model resolver and Google access-token source | [Google-hosted Anthropic Claude](#google-hosted-anthropic-claude) |
| Select an AWS Bedrock model, inference profile, or provisioned route per semantic model | `bedrock` plus an SDK-destination resolver | [AWS Bedrock SDK-Native Routing](#aws-bedrock-sdk-native-routing) |
| Enforce a stable declarative request rule | `AIProviderPatch` | [Declarative Request Rules](#declarative-request-rules) |
| Derive a request edit from trusted runtime context | `RequestMiddleware` | [Request Middleware](#request-middleware) |
| Fail over between independently configured providers | `ai.NewChain` | [Heterogeneous Failover](#6-scenario-3-build-a-heterogeneous-failover-chain) |
| Add an OpenAI-compatible service with its own provider identity | `RequestProviderFactory` plus `openaiwire.Codec` | [Reuse the OpenAI-Compatible Codec](#reuse-the-openai-compatible-codec) |
| Add a provider with native SDK request types | `RequestProviderFactory` plus a `requestpolicy.Draft` | [Adapt an SDK-Native Provider](#adapt-an-sdk-native-provider) |
| Make AI-output caches policy-aware | `AIRequestFingerprinter` | [Semantic Fingerprinting](#implement-semantic-fingerprinting) |

### What This API Does Not Cover

The request-aware API described here covers text/chat generation and streaming
through `core.AIRequest`, `core.AIResult`, and `core.StreamCallback`. It does
not define portable contracts for embeddings, image or audio generation,
batch jobs, file upload, model administration, or provider-specific tool-call
objects. Do not squeeze one of those surfaces into `AIRequest`; introduce a
separate provider-neutral contract before exposing it through framework code.

Provider-native options that have no portable field may be expressed through
validated patches or legacy `AIOptions.Extra` only when the selected provider
surface explicitly supports them. They remain provider-specific and may make
a request unrepresentable by a legacy fallback.

### Key Terms in Plain English

| Term | Plain-English meaning |
|---|---|
| `AIRequest` | A provider-neutral description of what the caller wants |
| Request-aware client | A client that understands explicit set, inherit, omit, patches, and sanitized reports |
| Draft | A private, editable logical request before it becomes HTTP JSON or an SDK object |
| Request rule | A named, versioned edit applied when a selector matches |
| Middleware | Go code that makes a constrained request edit using runtime context |
| Endpoint resolver | A callback that chooses the destination and gives that route a safe identity |
| Credential source | A callback that supplies authentication for one transport attempt |
| Provider factory | The registry plugin that validates configuration and constructs a provider client |
| Wire codec | Code that converts between a logical request and provider HTTP bytes |
| Fingerprint | A secret-free identity for the policy and route semantics that produced a request |

---

## 3. Understand the Request-Aware API

Custom providers implement a contract defined in `core`. High-level framework
packages can therefore call `core.GenerateAI` and `core.StreamAI` without
importing `ai`, a provider SDK, or a wire-format package.

`core.AIRequest` carries the prompt, provider-neutral generation intent, and a
`Purpose` such as `incident_analysis` or `entity_extraction`. Purpose is a
stable, non-secret operation label used by policy selectors, reports, and
traces. Never place prompts, tenant secrets, credentials, or unique request IDs
in it.

### Why Ordinary Options Are Not Enough

The legacy `AIOptions` API is intentionally simple, but a zero-valued Go field
can be ambiguous. Consider temperature:

```go
options := &core.AIOptions{
    Temperature: 0,
}
```

Depending on the field and provider, zero may mean "the caller explicitly wants
zero" or "the caller did not supply a value; use a default." The value alone
cannot tell the provider which meaning was intended.

`AIRequest` makes that choice explicit:

```go
request := core.NewAIRequest("Extract entities", "entity_extraction")

// Send exactly zero.
request.Generation.Temperature = core.SetAIParameter(float32(0))

// Require top_p to be absent from the provider request.
request.Generation.TopP = core.OmitAIParameter[float32]()

// Leave max tokens to client or provider defaults.
request.Generation.MaxTokens = core.InheritAIParameter[int]()
```

This distinction matters when a provider rejects a field, when policy must
remove it, or when a migration must prove that no caller intent was silently
lost.

### The Request Lifecycle

For a request-aware HTTP provider, one logical call follows this path. Route
resolution deliberately precedes wire-profile and draft construction so a
route-owned deployment can differ from the semantic model without changing
capability checks or policy selectors:

| Stage | What happens | Who owns it |
|---|---|---|
| 1. Dispatch | `core.GenerateAI` or `core.StreamAI` selects the request-aware capability | Core |
| 2. Resolve semantics | The provider clones caller-owned values, resolves the provider/model alias, and validates portable intent and known semantic capabilities | Provider adapter |
| 3. Route | The endpoint resolver chooses a complete destination, opaque deployment, and stable route identity | Application integration |
| 4. Select profile | The provider chooses an explicit wire surface using semantic facts and the resolved route | Provider adapter |
| 5. Build and apply policy | The provider builds one logical draft; built-ins, application rules, middleware, and per-request patches edit it | Provider adapter + shared policy engine |
| 6. Validate and encode | The provider confirms the final draft and protected omissions can be represented safely, then creates replayable bytes | Provider adapter |
| 7. Attempt | The provider obtains a fresh credential after installing a fresh body for that attempt | Provider transport + credential source |
| 8. Execute | HTTP or the provider SDK sends the request and receives the response | Provider transport |
| 9. Normalize | The provider returns `AIResult` with `AIResponse`, usage, and a sanitized report | Provider adapter |

For the Acme example, the OpenAI adapter still owns model and JSON behavior.
Acme's rule changes the logical draft, its resolver selects US or EU, and its
credential source adds a token only when the HTTP attempt is ready to run.

Sync and streaming calls use the same preparation policy. The wire execution
differs, but model resolution, field protection, routing, credential handling,
and request reporting do not.

### Presence-Aware Parameters

A plain Go zero value cannot distinguish "inherit the default" from "send
zero." `core.AIParameter[T]` preserves that intent:

| Mode | Meaning |
|---|---|
| `InheritAIParameter[T]()` or the zero value | Let lower-precedence defaults decide |
| `SetAIParameter(value)` | Explicitly send the value, including `0`, `false`, or an empty string |
| `OmitAIParameter[T]()` | Require the provider request to omit the field |

The presence-aware fields are `Temperature`, `TopP`, `TopK`, `MaxTokens`,
`SystemPrompt`, `ReasoningEffort`, and `ResponseFormat`. `Model` is structural:
an empty string inherits model selection, and model cannot be explicitly
omitted.

### Safe Dispatch and Feature Refusal

Use `core.GenerateAI` and `core.StreamAI` when the concrete client capability is
not known:

- request-aware clients receive the complete `AIRequest`
- legacy clients are used only when the request can be represented losslessly
- unsupported intent returns a typed `core.AIRequestFeatureError`
- `errors.Is(err, core.ErrAIRequestFeatureUnsupported)` identifies that case

This is important for failover. A provider that cannot represent a feature
must refuse it explicitly; silently dropping the feature could produce a
successful but semantically wrong response.

`AIResult` contains the normalized `AIResponse`, an optional sanitized
`AIRequestReport`, and optional provider usage counters. It never makes
provider response envelopes part of the framework contract.

---

## 4. Scenario 1: Add Enterprise Credentials and Routing

**Problem:** Your company exposes OpenAI through regional gateways. Tokens are
short-lived, routes depend on request identity, and the transport needs a
corporate TLS configuration.

**Solution:** Keep the built-in OpenAI request and wire behavior, then supply
the enterprise concerns independently.

Each extension answers one question:

| Component | Question it answers | Example responsibility |
|---|---|---|
| Built-in OpenAI provider | What does an OpenAI request and response mean? | Model resolution, JSON body, streaming decode |
| `EndpointResolver` | Where should this semantic request go? | Select the US or EU gateway |
| `CredentialSource` | How should this transport attempt authenticate? | Obtain a token scoped to the selected route |
| `http.Client` | How should the network connection be made? | Corporate CA, proxy, connection pool |

Keeping these separate prevents a common mistake: putting token acquisition
inside routing or putting tenant routing inside the provider's JSON encoder.

```go
credentialSource := &gatewayCredentials{
    tokens: tokenProvider,
}

client, err := ai.NewRequestClient(
    ai.WithProvider("openai"),
    ai.WithModel("smart"),
    ai.WithEndpointResolver(acmeRoutes),
    ai.WithCredentialSource(credentialSource),
    ai.WithHTTPClient(corporateHTTPClient),
)
```

Here, `acmeRoutes`, `tokenProvider`, and `corporateHTTPClient` are
application-owned dependencies. The following subsections define the framework
interfaces they satisfy; how Acme reads tenant identity, talks to its identity
service, or constructs its TLS transport remains application code.

This keeps agents unaware of regions, token rotation, TLS roots, proxies, and
gateway URLs. When an Acme request runs:

1. OpenAI preparation resolves the semantic model and validates portable
   intent.
2. `acmeRoutes` selects a complete US or EU endpoint before the wire profile
   and policy draft are built.
3. The OpenAI adapter builds and validates the profiled draft, including
   application policy.
4. `gatewayCredentials` receives trusted route information and obtains a
   token for that transport attempt.
5. The provider attaches the token, sends the prepared OpenAI request through
   `corporateHTTPClient`, and normalizes the response.

### Dynamic Credentials

`CredentialSource.Credential` runs for every transport attempt, after policy
and routing. This supports expiring tokens and retry-safe credential rotation:

```go
type gatewayCredentials struct {
    tokens TokenProvider
}

type TokenProvider interface {
    Token(context.Context, string) (string, error)
    Invalidate(context.Context, string) error
}

func (source *gatewayCredentials) Credential(
    ctx context.Context,
    request ai.CredentialRequest,
) (ai.HeaderCredential, error) {
    token, err := source.tokens.Token(ctx, request.CredentialScope)
    if err != nil {
        return ai.HeaderCredential{}, err
    }
    return ai.NewHeaderCredential("Authorization", "Bearer "+token), nil
}
```

Credential sources retained by a client must be safe for concurrent use.
Credential names and values are attached only at the transport boundary and
are excluded from reports, fingerprints, and logs.

Use `WithAuthHeader` when one callback can supply the complete dynamic header
value. Use `WithCredentialSource` when credential selection needs trusted route
inputs such as `CredentialScope` or `Deployment`.

A source may also implement `CredentialRejectionObserver`. It is notified
after HTTP 401 or 403 responses, which is useful for invalidating a cached
token.

### React to Rejected Credentials

Implement both credential interfaces on the same value when a rejection
should evict an attempt-local token:

```go
var (
    _ ai.CredentialSource            = (*gatewayCredentials)(nil)
    _ ai.CredentialRejectionObserver = (*gatewayCredentials)(nil)
)

func (source *gatewayCredentials) CredentialRejected(
    ctx context.Context,
    request ai.CredentialRequest,
    status int,
) error {
    if status != http.StatusUnauthorized && status != http.StatusForbidden {
        return nil
    }
    return source.tokens.Invalidate(ctx, request.CredentialScope)
}
```

The provider returns its original 401/403 error even if `Invalidate` fails;
observer failures are diagnostics, not replacement failures. Do not log the
credential, authorization header, route query, or token-cache key. Make token
lookup and invalidation concurrency-safe because different requests can call
both methods at the same time.

### Per-Request Endpoint Routing

`EndpointResolver` runs after portable intent and semantic-model validation but
before wire-profile selection, draft construction, and application policy:

```go
type regionalResolver struct {
    endpoints map[string]*url.URL
}

func (resolver *regionalResolver) ResolveEndpoint(
    ctx context.Context,
    request ai.EndpointRequest,
) (ai.ResolvedEndpoint, error) {
    endpoint := resolver.endpoints[selectRegion(ctx)]
    return ai.ResolvedEndpoint{
        URL:             endpoint,
        RouteIdentity:   "openai-gateway-v2",
        Deployment:      request.ResolvedModel,
        CredentialScope: "openai-production",
    }, nil
}
```

In this example, `selectRegion` is a local application helper that reads
trusted request context. It must not infer routing from prompt text or perform
remote discovery. Each map value must be the complete provider endpoint,
including the request path.

The returned `URL` is the complete provider request endpoint.
`RouteIdentity` must be stable and non-secret because it participates in the
semantic fingerprint. Change it when a routing change can affect AI output.

`Query`, `CredentialScope`, and `Deployment` are trusted routing inputs and are
excluded from reports. Cache fingerprint preflight may evaluate a resolver,
and a cache miss may evaluate it again. A resolver must therefore be:

- concurrency-safe
- stable for the same semantic request
- free of side effects
- free of credential acquisition and remote discovery

### Custom HTTP Clients

Use `WithHTTPClient` for corporate TLS roots, proxies, connection pools, or
transport-level test doubles. Supporting providers shallow-copy the
`http.Client` and never mutate the caller-owned value.

The following helper creates a concrete corporate transport with a private CA,
optional mTLS certificate, and an explicit proxy:

```go
func corporateClient(
    caPEM []byte,
    clientCertificate *tls.Certificate,
    proxyURL *url.URL,
) (*http.Client, error) {
    roots, err := x509.SystemCertPool()
    if err != nil {
        return nil, fmt.Errorf("load system CAs: %w", err)
    }
    if ok := roots.AppendCertsFromPEM(caPEM); !ok {
        return nil, errors.New("corporate CA bundle contains no certificates")
    }

    transport := http.DefaultTransport.(*http.Transport).Clone()
    transport.Proxy = http.ProxyURL(proxyURL)
    transport.TLSClientConfig = &tls.Config{
        MinVersion: tls.VersionTLS12,
        RootCAs:    roots,
    }
    if clientCertificate != nil {
        transport.TLSClientConfig.Certificates = []tls.Certificate{
            *clientCertificate,
        }
    }

    return &http.Client{
        Transport: transport,
        Timeout:   90 * time.Second,
    }, nil
}
```

Construct this client once, pass it through `ai.WithHTTPClient`, and do not
mutate its transport while requests are running. Pass `nil` as `proxyURL` to
disable proxying, or set `transport.Proxy = http.ProxyFromEnvironment` when
deployment-owned `HTTP_PROXY`, `HTTPS_PROXY`, and `NO_PROXY` values should
apply. The provider-specific request timeout and `http.Client.Timeout` are both
upper bounds; configure them consistently.

HTTP-only integrations are supported by the request-aware OpenAI, Azure OpenAI,
and Anthropic clients. The Bedrock adapter is SDK-native and rejects these
options instead of pretending they apply; it separately accepts a resolver
whose result contains only an SDK `modelId` and sanitized route identity.

### Choose a Hosted Cloud Recipe

Azure OpenAI, Google-hosted models, and private enterprise gateways often
expose an OpenAI-compatible chat-completions surface. That does not
automatically make every endpoint interchangeable. The URL layout,
authentication header, token lifetime, required query parameters, and accepted
body fields still vary.

Start with the row that describes the endpoint contract you were given:

| Service contract | Request destination | Authentication | Recipe |
|---|---|---|---|
| Azure OpenAI v1 | `{resource-endpoint}/openai/v1/chat/completions` | `api-key` or Microsoft Entra bearer token | [Azure OpenAI v1](#azure-openai-v1) |
| Azure OpenAI classic | `{resource-endpoint}/openai/deployments/{deployment}/chat/completions?api-version=...` | `api-key` or Microsoft Entra bearer token | [Azure OpenAI Classic Deployment API](#azure-openai-classic-deployment-api) |
| Private Azure-style gateway | Gateway-specific `/openai/deployments/{deployment}/chat/completions` | Often a short-lived OAuth token sent as `api-key` | [OAuth-Protected Azure-Style Enterprise Gateway](#oauth-protected-azure-style-enterprise-gateway) |
| Google Cloud OpenAI compatibility | Global or location-prefixed `aiplatform.googleapis.com/.../endpoints/openapi/chat/completions` | Google access token as `Authorization: Bearer ...` | [Google Cloud OpenAI Compatibility](#google-cloud-openai-compatibility) |
| Anthropic Claude on Google Vertex AI | Google publisher-model `:rawPredict` / `:streamRawPredict` endpoint | Google access token as `Authorization: Bearer ...` | [Google-hosted Anthropic Claude](#google-hosted-anthropic-claude) |
| Native Vertex AI `generateContent`, another provider SDK, or a non-chat response envelope | Provider-specific | Provider-specific | [Implement a New Provider](#7-scenario-4-implement-a-new-provider) |

These recipes deliberately do **not** implement OAuth client-credential
exchange, Microsoft Entra authentication, Google Application Default
Credentials, workload identity, token caching, or token refresh. Those choices
depend on the application's identity platform and deployment environment. The
application owns that code and gives TruvaG3 either:

- an `ai.AuthHeaderFunc` that returns a complete current header value, or
- an `ai.CredentialSource` when authentication depends on the resolved route or
  must react to HTTP 401/403 responses.

For bearer-token services, a small application-owned interface keeps cloud SDK
types outside framework construction:

```go
type AccessTokenSource interface {
    // AccessToken returns a currently usable raw access token. The
    // implementation owns acquisition, caching, early refresh, and retries.
    AccessToken(context.Context) (string, error)
}

func bearerHeader(source AccessTokenSource) ai.AuthHeaderFunc {
    return func(ctx context.Context) (string, error) {
        if source == nil {
            return "", errors.New("access token source is nil")
        }

        token, err := source.AccessToken(ctx)
        if err != nil {
            return "", fmt.Errorf("get access token: %w", err)
        }
        token = strings.TrimSpace(token)
        if token == "" {
            return "", errors.New("access token source returned an empty token")
        }
        return "Bearer " + token, nil
    }
}
```

The snippets in this section use the standard-library `context`,
`encoding/json`, `errors`, `fmt`, `net/http`, `net/url`, `regexp`, and `strings`
packages, plus `github.com/truvaagents/truva-g3/ai` and
`github.com/truvaagents/truva-g3/core`. Keep the helpers in an application
integration package rather than copying them into agents.

Provider registration is import-driven. An application using the following
recipes must include the matching blank imports in the binary that constructs
the clients:

```go
import (
    _ "github.com/truvaagents/truva-g3/ai/providers/anthropic"   // anthropic.vertex
    _ "github.com/truvaagents/truva-g3/ai/providers/azureopenai" // Azure v1/classic
    _ "github.com/truvaagents/truva-g3/ai/providers/openai"      // Google OpenAI compatibility
)
```

An application can implement `AccessTokenSource` with its approved Azure
Identity credential, Google Application Default Credentials, workload identity
provider, secret sidecar, Vault client, or internal token broker. Do not log the
returned token or place it in `WithHeaders`, `WithExtra`, request patches,
route identities, or telemetry.

All following examples use `ai.NewRequestClient`, not `ai.NewClient`, because
credential sources and endpoint resolvers are request-aware integration hooks.
Azure OpenAI and `anthropic.vertex` are request-aware-only: legacy construction
returns `core.ErrAIRequestFeatureUnsupported` (and direct calls to the legacy
Azure factory's `Create` method are a guaranteed panic). Load and validate
endpoint names, deployment names, API versions, and secrets from application
configuration before constructing the client.

### Azure OpenAI v1

Use the exact `azureopenai.v1` alias for the Azure OpenAI v1
chat-completions contract. The application resolver returns the complete
`/openai/v1/chat/completions` URL and the opaque Azure deployment. TruvaG3
retains the resolved OpenAI model as semantic identity for capability checks,
policy selectors, reports, and fingerprints, while emitting the deployment as
the protected body `model`.

The current Microsoft references are the
[Azure OpenAI v1 chat REST API](https://learn.microsoft.com/en-us/rest/api/microsoft-foundry/azureopenai/chat)
and the
[OpenAI-to-Azure endpoint comparison](https://learn.microsoft.com/en-us/azure/developer/ai/how-to/switching-endpoints).
Confirm the endpoint form and authentication policy against your Azure resource
before deployment.

The resolver below is shared by the v1 and classic recipes. Its deployment map
is keyed by `EndpointRequest.ResolvedModel`: the concrete semantic model after
TruvaG3 alias and environment resolution, not the application-supplied alias.
For example, a client configured with `WithModel("smart")` normally looks up
the current concrete OpenAI model, not the key `"smart"`. The deployment value
is route-owned and opaque, so a deployment literally named `smart`, `fast`,
`cheap`, `vision`, `code`, or `default` is not rewritten by TruvaG3 aliases.

```go
type azureOpenAIResolver struct {
    providerAlias string
    origin        *url.URL
    deployments   map[string]string // post-alias semantic model -> Azure deployment
    apiVersion    string
    routeIdentity string
}

func newAzureOpenAIResolver(
    providerAlias string,
    resourceEndpoint string,
    deployments map[string]string,
    apiVersion string,
    routeIdentity string,
) (*azureOpenAIResolver, error) {
    if providerAlias != "azureopenai.v1" &&
        providerAlias != "azureopenai.classic" {
        return nil, fmt.Errorf("unsupported Azure OpenAI alias %q", providerAlias)
    }
    origin, err := url.Parse(strings.TrimSpace(resourceEndpoint))
    if err != nil {
        return nil, errors.New("Azure OpenAI resource endpoint is invalid")
    }
    if origin.Scheme != "https" || origin.Hostname() == "" ||
        origin.User != nil || origin.Port() != "" ||
        origin.RawQuery != "" || origin.Fragment != "" ||
        origin.EscapedPath() != "" && origin.EscapedPath() != "/" {
        return nil, errors.New(
            "Azure OpenAI resource endpoint must be an HTTPS origin without a port, path, query, user info, or fragment",
        )
    }
    routeIdentity = strings.TrimSpace(routeIdentity)
    if routeIdentity == "" {
        return nil, errors.New("Azure OpenAI route identity is required")
    }
    copied := make(map[string]string, len(deployments))
    for semanticModel, deployment := range deployments {
        semanticModel = strings.TrimSpace(semanticModel)
        deployment = strings.TrimSpace(deployment)
        if semanticModel == "" || deployment == "" {
            return nil, errors.New(
                "Azure OpenAI deployment mappings require nonempty semantic models and deployments",
            )
        }
        copied[semanticModel] = deployment
    }
    if len(copied) == 0 {
        return nil, errors.New("Azure OpenAI deployment mappings are required")
    }
    apiVersion = strings.TrimSpace(apiVersion)
    if providerAlias == "azureopenai.classic" && apiVersion == "" {
        return nil, errors.New("Azure OpenAI classic API version is required")
    }
    origin.Path = ""
    origin.RawPath = ""
    return &azureOpenAIResolver{
        providerAlias: providerAlias,
        origin:        origin,
        deployments:   copied,
        apiVersion:    apiVersion,
        routeIdentity: routeIdentity,
    }, nil
}

func (resolver *azureOpenAIResolver) ResolveEndpoint(
    _ context.Context,
    request ai.EndpointRequest,
) (ai.ResolvedEndpoint, error) {
    if request.Provider != "azureopenai" ||
        request.ProviderAlias != resolver.providerAlias {
        return ai.ResolvedEndpoint{}, errors.New(
            "Azure OpenAI resolver received the wrong provider",
        )
    }
    if request.Operation != "generate" && request.Operation != "stream" {
        return ai.ResolvedEndpoint{}, fmt.Errorf(
            "unsupported Azure OpenAI operation %q",
            request.Operation,
        )
    }
    deployment, ok := resolver.deployments[request.ResolvedModel]
    if !ok {
        return ai.ResolvedEndpoint{}, fmt.Errorf(
            "no Azure deployment for semantic model %q",
            request.ResolvedModel,
        )
    }

    rawURL := strings.TrimRight(resolver.origin.String(), "/")
    query := make(url.Values)
    switch resolver.providerAlias {
    case "azureopenai.v1":
        rawURL += "/openai/v1/chat/completions"
        if resolver.apiVersion != "" {
            query.Set("api-version", resolver.apiVersion)
        }
    case "azureopenai.classic":
        rawURL += "/openai/deployments/" +
            url.PathEscape(deployment) + "/chat/completions"
        query.Set("api-version", resolver.apiVersion)
    }
    endpoint, err := url.Parse(rawURL)
    if err != nil {
        return ai.ResolvedEndpoint{}, errors.New(
            "construct Azure OpenAI endpoint",
        )
    }
    return ai.ResolvedEndpoint{
        URL:             endpoint,
        Query:           query,
        Deployment:      deployment,
        RouteIdentity:   resolver.routeIdentity,
        CredentialScope: "https://cognitiveservices.azure.com/.default",
    }, nil
}
```

Keep resolver maps immutable after construction, and bump `routeIdentity` when
mapping or routing semantics change. Azure semantic resolution uses TruvaG3's
built-in OpenAI catalog plus `TRUVAG3_OPENAI_MODEL_*` environment overrides;
runtime mutations of the legacy mutable `openai.ModelAliases` map deliberately
do not apply to Azure.

#### Authenticate with an Azure API Key

Azure expects the exact `api-key` header. On the first-class Azure adapter,
`WithAPIKey` has that exact meaning and the value is attached at the transport
attempt boundary.

```go
func newAzureOpenAIV1WithAPIKey(
    resourceEndpoint string,
    semanticModel string,
    deployments map[string]string,
    apiKey string,
) (core.AIRequestClient, error) {
    apiKey = strings.TrimSpace(apiKey)
    if apiKey == "" {
        return nil, errors.New("Azure OpenAI API key is required")
    }
    resolver, err := newAzureOpenAIResolver(
        "azureopenai.v1",
        resourceEndpoint,
        deployments,
        "", // v1 does not require api-version
        "azure-openai-v1-primary-v1",
    )
    if err != nil {
        return nil, err
    }

    return ai.NewRequestClient(
        ai.WithProviderAlias("azureopenai.v1"),
        ai.WithModel(semanticModel),
        ai.WithEndpointResolver(resolver),
        ai.WithAPIKey(apiKey),
    )
}
```

Example configuration values look like this:

```text
resourceEndpoint = https://<resource-name>.openai.azure.com
semanticModel    = gpt-4.1
deployments      = {"gpt-4.1": "<your-deployment-name>"}
```

Some Azure resources use a Foundry service endpoint rather than the
`openai.azure.com` hostname. Treat the resource endpoint as configuration; do
not derive it from the resource name in framework code.

#### Authenticate with Microsoft Entra

For keyless authentication, the application obtains and refreshes a Microsoft
Entra access token, normally for the
`https://cognitiveservices.azure.com/.default` scope. TruvaG3 receives only the
application-owned token source:

```go
func newAzureOpenAIV1WithEntra(
    resourceEndpoint string,
    semanticModel string,
    deployments map[string]string,
    tokens AccessTokenSource,
) (core.AIRequestClient, error) {
    if tokens == nil {
        return nil, errors.New("Azure OpenAI token source is required")
    }
    resolver, err := newAzureOpenAIResolver(
        "azureopenai.v1",
        resourceEndpoint,
        deployments,
        "",
        "azure-openai-v1-primary-v1",
    )
    if err != nil {
        return nil, err
    }

    return ai.NewRequestClient(
        ai.WithProviderAlias("azureopenai.v1"),
        ai.WithModel(semanticModel),
        ai.WithEndpointResolver(resolver),
        ai.WithAuthHeader("Authorization", bearerHeader(tokens)),
    )
}
```

The token source, not TruvaG3, chooses managed identity, workload identity,
client credentials, interactive development credentials, or another Entra
flow. It must be concurrency-safe and refresh before returning an expired
token.

Both v1 clients are called in the same provider-neutral way:

```go
request := core.NewAIRequest(
    "Summarize the operational risk in this incident.",
    "incident_summary",
)
request.Generation.MaxTokens = core.SetAIParameter(500)

result, err := core.GenerateAI(ctx, client, request)
if err != nil {
    return err
}
fmt.Println(result.Response.Content)
```

### Azure OpenAI Classic Deployment API

Use this recipe only for the classic Azure contract whose deployment is part
of the path and whose API version is a query parameter. The exact
`azureopenai.classic` profile places the route-owned deployment in the URL and
protects the intentional absence of body `model`.

The Azure classic URL shape is documented in the
[Azure OpenAI classic reference](https://learn.microsoft.com/en-us/azure/foundry/openai/reference?view=foundry-classic)
and the durable
[Foundry chat REST reference](https://learn.microsoft.com/en-us/rest/api/microsoft-foundry/azureopenai/chat),
which includes classic-version monikers. Phase 9 verifies the ordinary classic
wire contract against the pinned `2024-10-21` GA schema. That schema does not
define `reasoning_effort`, so TruvaG3 refuses a semantic OpenAI reasoning model
on this surface before policy, credentials, or transport.

Construct an API-key client like this:

```go
func newAzureClassicClient(
    resourceEndpoint string,
    semanticModel string,
    deployments map[string]string,
    apiKey string,
) (core.AIRequestClient, error) {
    apiKey = strings.TrimSpace(apiKey)
    if apiKey == "" {
        return nil, errors.New("Azure OpenAI API key is required")
    }
    resolver, err := newAzureOpenAIResolver(
        "azureopenai.classic",
        resourceEndpoint,
        deployments,
        "2024-10-21",
        "azure-openai-classic-primary-v1",
    )
    if err != nil {
        return nil, err
    }

    return ai.NewRequestClient(
        ai.WithProviderAlias("azureopenai.classic"),
        ai.WithModel(semanticModel),
        ai.WithEndpointResolver(resolver),
        ai.WithAPIKey(apiKey),
    )
}
```

To use Microsoft Entra instead, replace the final `WithAPIKey` with:

```go
ai.WithAuthHeader("Authorization", bearerHeader(entraTokens))
```

Do not add `WithBaseURL`: the Azure factory rejects it because the resolver owns
the complete validated route. Supporting a different classic API version or a
classic reasoning contract requires an explicit reviewed surface row and wire
fixture; changing the string alone is not evidence of compatibility.

### OAuth-Protected Azure-Style Enterprise Gateway

Private gateways frequently combine Azure-style deployment paths with a
company identity service. A representative contract looks like this:

1. The application performs an OAuth client-credentials exchange against its
   identity service.
2. The identity service returns a short-lived `access_token` and expiry.
3. The chat gateway expects the raw access token in `api-key`, not in an
   `Authorization: Bearer` header.
4. The chat body adds gateway fields such as `user` and `stop` while retaining
   an OpenAI chat-completions response envelope.

The framework should not own step 1. Client ID storage, client secret storage,
OAuth audience or scope, refresh skew, backoff, single-flight refresh, and
identity-server TLS policy all belong to the application. The existing
`TokenProvider` interface from [Dynamic Credentials](#dynamic-credentials) is
enough to inject the result.

This credential adapter deliberately changes the header spelling from the
earlier bearer example:

```go
type oauthAPIKeyCredentials struct {
    tokens TokenProvider
    scope  string
}

var (
    _ ai.CredentialSource = (*oauthAPIKeyCredentials)(nil)
    _ ai.CredentialRejectionObserver = (*oauthAPIKeyCredentials)(nil)
)

func (source *oauthAPIKeyCredentials) Credential(
    ctx context.Context,
    _ ai.CredentialRequest,
) (ai.HeaderCredential, error) {
    token, err := source.tokens.Token(ctx, source.scope)
    if err != nil {
        return ai.HeaderCredential{}, fmt.Errorf("get enterprise OAuth token: %w", err)
    }
    token = strings.TrimSpace(token)
    if token == "" {
        return ai.HeaderCredential{}, errors.New(
            "enterprise OAuth token provider returned an empty token",
        )
    }
    return ai.NewHeaderCredential("api-key", token), nil
}

func (source *oauthAPIKeyCredentials) CredentialRejected(
    ctx context.Context,
    _ ai.CredentialRequest,
    status int,
) error {
    if status != http.StatusUnauthorized && status != http.StatusForbidden {
        return nil
    }
    return source.tokens.Invalidate(ctx, source.scope)
}
```

The following construction matches a gateway URL of the form
`https://<gateway>/openai/deployments/<deployment>/chat/completions`. The
built-in OpenAI client appends `/chat/completions`, so the configured base URL
ends at the deployment name:

```go
func newEnterpriseGatewayClient(
    gatewayEndpoint string,
    deployment string,
    appKey string,
    tokenProvider TokenProvider,
) (core.AIRequestClient, error) {
    gatewayEndpoint = strings.TrimRight(strings.TrimSpace(gatewayEndpoint), "/")
    deployment = strings.TrimSpace(deployment)
    appKey = strings.TrimSpace(appKey)
    if gatewayEndpoint == "" || deployment == "" ||
        appKey == "" || tokenProvider == nil {
        return nil, errors.New(
            "gateway endpoint, deployment, app key, and token provider are required",
        )
    }

    userMetadata, err := json.Marshal(map[string]string{
        "appkey": appKey,
    })
    if err != nil {
        return nil, fmt.Errorf("encode gateway user metadata: %w", err)
    }

    return ai.NewRequestClient(
        ai.WithProvider("openai"),
        ai.WithBaseURL(
            gatewayEndpoint + "/openai/deployments/" + url.PathEscape(deployment),
        ),
        ai.WithModel(deployment),
        ai.WithCredentialSource(&oauthAPIKeyCredentials{
            tokens: tokenProvider,
            scope:  "enterprise-chat",
        }),
        ai.WithExtra("user", string(userMetadata)),
        ai.WithExtra("stop", []string{"<|im_end|>"}),
        ai.WithHeaders(map[string]string{
            "Accept": "application/json",
        }),
    )
}
```

This generic OpenAI recipe passes the deployment through ordinary model
resolution. Avoid deployment names that collide with TruvaG3 aliases such as
`smart`, `fast`, `vision`, `code`, or `default`. A deployment literal also
collides when a matching `TRUVAG3_OPENAI_MODEL_<DEPLOYMENT>` override is set.
Use a first-class or custom route-owned deployment profile when such a name
cannot be changed.

To reproduce a request that intentionally omits default sampling and token
fields, use presence-aware omission on the call:

```go
request := core.NewAIRequest(
    "What is the Model Context Protocol (MCP)? Answer in three concise sentences.",
    "enterprise_chat",
)
request.Generation.Temperature = core.OmitAIParameter[float32]()
request.Generation.MaxTokens = core.OmitAIParameter[int]()

result, err := core.GenerateAI(ctx, client, request)
if err != nil {
    return err
}
fmt.Println(result.Response.Content)
```

For that call, TruvaG3 builds the standard `messages`, adds `model`, `user`, and
`stop`, and leaves `temperature` and `max_tokens` absent. A synchronous request
does not emit `stream: false`; absence has the same meaning in standard
OpenAI-compatible APIs. Verify both differences against a private gateway's
schema. If it requires the literal `stream: false` or rejects the matching body
`model`, use a custom adapter because both fields are protected structural
invariants in the reusable codec.

The standard decoder accepts the representative enterprise response envelope:
it reads `choices[0].message.content`, the response model, prompt/completion/
total token counts, and supported nested token details. Extra fields such as
content filters, latency checkpoints, service tier, annotations, and gateway
session metadata are safely ignored. If application code must consume those
extra response fields, that requirement crosses the portable `core.AIResult`
boundary and needs a custom provider-specific result contract.

### Google Cloud OpenAI Compatibility

Use this recipe for Google's documented OpenAI-compatible chat-completions
endpoint, not for the native Vertex AI `generateContent` API. The configured
base URL ends at `/endpoints/openapi`; TruvaG3 appends `/chat/completions`.

See Google's
[OpenAI compatibility documentation](https://cloud.google.com/vertex-ai/generative-ai/docs/start/openai)
for the current endpoint, model names, supported parameters, and limitations.
For production credential selection, see
[Application Default Credentials](https://cloud.google.com/docs/authentication/provide-credentials-adc).

Google access tokens are short-lived. The `AccessTokenSource` implementation
must obtain and refresh them using the application's chosen Google credentials;
do not capture one startup token in a static string.

```go
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
    projectID string,
    location string,
    model string,
    tokens AccessTokenSource,
) (core.AIRequestClient, error) {
    model = strings.TrimSpace(model)
    if model == "" || tokens == nil {
        return nil, errors.New(
            "Google model and token source are required",
        )
    }
    baseURL, err := googleOpenAIBaseURL(projectID, location)
    if err != nil {
        return nil, err
    }

    return ai.NewRequestClient(
        ai.WithProvider("openai"),
        ai.WithBaseURL(baseURL.String()),
        // Use the complete model ID required by the Google endpoint, such as
        // a documented google/... model name. Do not use a TruvaG3 alias here.
        ai.WithModel(model),
        ai.WithAuthHeader("Authorization", bearerHeader(tokens)),
    )
}
```

The exact completed destinations are:

```text
global:
https://aiplatform.googleapis.com/v1/projects/acme-prod/locations/global/endpoints/openapi/chat/completions

regional:
https://us-central1-aiplatform.googleapis.com/v1/projects/acme-prod/locations/us-central1/endpoints/openapi/chat/completions
```

The plain `aiplatform.googleapis.com` host is the documented `global` form.
Regional locations must use the location-prefixed host; a global host with a
regional `/locations/...` path is not the documented regional endpoint.

Ordinary portable text generation is unchanged:

```go
request := core.NewAIRequest(
    "Explain why idempotency matters in a distributed workflow.",
    "architecture_explanation",
)
request.Generation.MaxTokens = core.SetAIParameter(600)

result, err := core.GenerateAI(ctx, client, request)
if err != nil {
    return err
}
fmt.Println(result.Response.Content)
```

If Google documents a top-level OpenAI-compatible field that has no portable
TruvaG3 parameter, express it as a scoped provider-native rule. Current
OpenAI-style `reasoning_effort` and the Google compatibility field use the same
top-level scalar shape. The native rule is needed because TruvaG3's built-in
OpenAI capability table does not claim reasoning support for arbitrary
`google/...` model IDs, not because Google uses a different field shape. Scope
the assertion to Google model IDs so it cannot affect ordinary OpenAI calls:

```go
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

client, err := ai.NewRequestClient(
    ai.WithProvider("openai"),
    ai.WithBaseURL(baseURL),
    ai.WithModel("google/<documented-model-id>"),
    ai.WithAuthHeader("Authorization", bearerHeader(tokens)),
    ai.WithRequestRules(googleReasoningRule),
)
```

Do not set `request.Generation.ReasoningEffort` for an uncataloged Google model.
That portable field is capability-checked and may be refused when TruvaG3 has
no semantic reasoning row for the model. The scoped native rule is an explicit
application assertion about that provider-model contract; it does not
reclassify the model family or change ordinary `max_tokens` and sampling
behavior.

Google documents only a subset of the OpenAI surface and may ignore unsupported
parameters. An ignored provider-native field can silently change semantics, so
add an integration contract test for every field your application depends on.
For streaming, also verify that the selected Google model accepts the protected
`stream_options: {"include_usage": true}` field emitted by the TruvaG3 codec;
usage may be absent if the service ignores it.

### Google-hosted Anthropic Claude

Use the exact `anthropic.vertex` alias for Anthropic's Messages-compatible
Claude surface on Vertex AI. This is a first-class TruvaG3 profile, not an
OpenAI-compatibility recipe: it preserves the semantic Claude model for
capability checks and policy, while the resolver supplies Google's opaque
publisher-model ID for the route.

Google documents the partner-model endpoint in
[Use Claude models](https://cloud.google.com/vertex-ai/generative-ai/docs/partner-models/claude/use-claude)
and the shared authentication and IAM requirements in
[Use partner models](https://cloud.google.com/vertex-ai/generative-ai/docs/partner-models/use-partner-models).
Model availability and publisher-model IDs vary by release and location, so
verify both against Google's current model table before deployment.

The resolver map is keyed by `EndpointRequest.ResolvedModel`: the concrete
semantic Claude model after TruvaG3 alias and environment resolution, not an
application alias such as `smart`. Its value is the exact Google
publisher-model ID used in the URL.

```go
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
    projectID string,
    location string,
    publisherModel string,
    operation string,
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

    method := "rawPredict"
    if operation == "stream" {
        method = "streamRawPredict"
    } else if operation != "generate" {
        return nil, fmt.Errorf(
            "unsupported Anthropic operation %q",
            operation,
        )
    }
    return &url.URL{
        Scheme: "https",
        Host:   host,
        Path: fmt.Sprintf(
            "/v1/projects/%s/locations/%s/publishers/anthropic/models/%s:%s",
            projectID,
            location,
            publisherModel,
            method,
        ),
    }, nil
}

type vertexClaudeResolver struct {
    projectID       string
    location        string
    publisherModels map[string]string // post-alias semantic model -> publisher ID
    routeIdentity   string
}

func newVertexClaudeResolver(
    projectID string,
    location string,
    publisherModels map[string]string,
    routeIdentity string,
) (*vertexClaudeResolver, error) {
    projectID = strings.TrimSpace(projectID)
    location = strings.TrimSpace(location)
    routeIdentity = strings.TrimSpace(routeIdentity)
    if !googleProjectIDPattern.MatchString(projectID) {
        return nil, errors.New("Google project ID is invalid")
    }
    if _, err := googlePartnerModelHost(location); err != nil {
        return nil, err
    }
    if routeIdentity == "" {
        return nil, errors.New("Vertex Claude route identity is required")
    }

    copied := make(map[string]string, len(publisherModels))
    for semanticModel, publisherModel := range publisherModels {
        semanticModel = strings.TrimSpace(semanticModel)
        publisherModel = strings.TrimSpace(publisherModel)
        if semanticModel == "" ||
            !googlePublisherModelPattern.MatchString(publisherModel) {
            return nil, errors.New(
                "Vertex Claude mappings require a semantic model and a valid publisher model",
            )
        }
        copied[semanticModel] = publisherModel
    }
    if len(copied) == 0 {
        return nil, errors.New("Vertex Claude publisher-model mappings are required")
    }
    return &vertexClaudeResolver{
        projectID:       projectID,
        location:        location,
        publisherModels: copied,
        routeIdentity:   routeIdentity,
    }, nil
}

func (resolver *vertexClaudeResolver) ResolveEndpoint(
    _ context.Context,
    request ai.EndpointRequest,
) (ai.ResolvedEndpoint, error) {
    if request.Provider != "anthropic" ||
        request.ProviderAlias != "anthropic.vertex" {
        return ai.ResolvedEndpoint{}, errors.New(
            "Vertex Claude resolver received the wrong provider",
        )
    }
    publisherModel, ok := resolver.publisherModels[request.ResolvedModel]
    if !ok {
        return ai.ResolvedEndpoint{}, fmt.Errorf(
            "no Vertex publisher model for semantic model %q",
            request.ResolvedModel,
        )
    }
    endpoint, err := googleClaudeEndpoint(
        resolver.projectID,
        resolver.location,
        publisherModel,
        request.Operation,
    )
    if err != nil {
        return ai.ResolvedEndpoint{}, err
    }
    return ai.ResolvedEndpoint{
        URL:             endpoint,
        Deployment:      publisherModel,
        RouteIdentity:   resolver.routeIdentity,
        CredentialScope: "https://www.googleapis.com/auth/cloud-platform",
    }, nil
}
```

Construct the request-aware client with a semantic Claude model and a dynamic
Google bearer token:

```go
func newVertexClaudeClient(
    projectID string,
    location string,
    semanticModel string,
    publisherModels map[string]string,
    tokens AccessTokenSource,
) (core.AIRequestClient, error) {
    if strings.TrimSpace(semanticModel) == "" || tokens == nil {
        return nil, errors.New(
            "Vertex Claude semantic model and token source are required",
        )
    }
    resolver, err := newVertexClaudeResolver(
        projectID,
        location,
        publisherModels,
        "vertex-claude-primary-v1",
    )
    if err != nil {
        return nil, err
    }
    return ai.NewRequestClient(
        ai.WithProviderAlias("anthropic.vertex"),
        ai.WithModel(semanticModel),
        ai.WithEndpointResolver(resolver),
        ai.WithAuthHeader("Authorization", bearerHeader(tokens)),
    )
}
```

For example, a client configured with semantic model
`claude-sonnet-4-5-20250929` must use that concrete ID as the resolver-map key;
the value is the current Google publisher ID for the chosen location. Do not
copy an illustrative publisher ID without confirming it in Google's model
table.

The first-class profile sends `:rawPredict` for sync and `:streamRawPredict`
for streaming. It omits body `model`, `anthropic-version`, and `x-api-key`,
adds the protected body value `anthropic_version: "vertex-2023-10-16"`, and
requires `Authorization: Bearer ...`. The adapter independently validates the
exact Google host, location, publisher path, deployment, and operation before
policy or credentials run. `anthropic.vertex` is request-aware-only and
rejects `WithAPIKey`, `WithBaseURL`, and legacy `ai.NewClient`; direct
Anthropic behavior is unchanged.

The token source must request a credential with the
`https://www.googleapis.com/auth/cloud-platform` scope and an identity whose
IAM permissions cover prediction. Keep token refresh application-owned and
contract-test both sync and streaming against the selected model and location.

### AWS Bedrock SDK-Native Routing

Bedrock is a first-class SDK-native provider behind the `bedrock` build tag. It
uses AWS Bedrock Runtime Converse/ConverseStream for portable generation and
InvokeModel for its Titan-shaped embedding helper. It does not use an
OpenAI-compatible URL and does not expose AWS SDK types through `core`.

Compile the provider and import its package to register the factory:

```bash
go build -tags bedrock ./...
```

```go
import (
    "context"
    "errors"
    "fmt"
    "strings"

    "github.com/truvaagents/truva-g3/ai"
    "github.com/truvaagents/truva-g3/ai/providers/bedrock"
    "github.com/truvaagents/truva-g3/core"
)
```

The generation default is the direct model ID
`anthropic.claude-sonnet-5` in `us-east-1`. AWS currently documents that exact
model as available in-region in `us-east-1`, and also documents separate
`us.anthropic.claude-sonnet-5` and
`global.anthropic.claude-sonnet-5` inference-profile IDs. TruvaG3 does not
silently select a profile: in-region, geographic cross-region, and global
cross-region routing can differ in residency, IAM/SCP behavior, availability,
and price.

If the factory resolves another region while no model or endpoint resolver is
configured, construction fails locally with a targeted routing error. This
guard applies only to the implicit default. An explicit supported model/profile
ID or an endpoint resolver remains application-owned routing intent.

Use a direct ID when that model is supported in the configured region:

```go
client, err := ai.NewClient(
    ai.WithProvider("bedrock"),
    ai.WithRegion("us-east-1"),
    ai.WithModel(bedrock.ModelClaudeSonnet5),
)
```

Use an SDK-destination resolver when an application must choose an inference
profile, application inference profile, provisioned model, or another
Converse-supported `modelId`. The resolver map is keyed by
`EndpointRequest.ResolvedModel`, the post-default semantic model ID—not by the
wire profile ID:

```go
type bedrockRoute struct {
    modelID       string
    routeIdentity string
}

type bedrockResolver struct {
    routes map[string]bedrockRoute
}

func newBedrockResolver(
    routes map[string]bedrockRoute,
) (*bedrockResolver, error) {
    if len(routes) == 0 {
        return nil, errors.New("at least one Bedrock route is required")
    }
    copied := make(map[string]bedrockRoute, len(routes))
    for semanticModel, route := range routes {
        semanticModel = strings.TrimSpace(semanticModel)
        route.modelID = strings.TrimSpace(route.modelID)
        route.routeIdentity = strings.TrimSpace(route.routeIdentity)
        if semanticModel == "" || route.modelID == "" ||
            route.routeIdentity == "" {
            return nil, errors.New(
                "Bedrock semantic model, model ID, and route identity are required",
            )
        }
        copied[semanticModel] = route
    }
    return &bedrockResolver{routes: copied}, nil
}

func (resolver *bedrockResolver) ResolveEndpoint(
    _ context.Context,
    request ai.EndpointRequest,
) (ai.ResolvedEndpoint, error) {
    if request.Provider != "bedrock" || request.Surface != "converse" {
        return ai.ResolvedEndpoint{}, errors.New(
            "Bedrock resolver received the wrong provider surface",
        )
    }
    route, ok := resolver.routes[request.ResolvedModel]
    if !ok {
        return ai.ResolvedEndpoint{}, fmt.Errorf(
            "no Bedrock route for semantic model %q",
            request.ResolvedModel,
        )
    }
    return ai.ResolvedEndpoint{
        Deployment:    route.modelID,
        RouteIdentity: route.routeIdentity,
    }, nil
}
```

Construct a US cross-region client like this:

```go
resolver, err := newBedrockResolver(map[string]bedrockRoute{
    bedrock.ModelClaudeSonnet5: {
        modelID:       "us.anthropic.claude-sonnet-5",
        routeIdentity: "bedrock-us-sonnet-primary-v1",
    },
})
if err != nil {
    return nil, err
}

client, err := ai.NewRequestClient(
    ai.WithProvider("bedrock"),
    ai.WithRegion("us-east-1"),
    ai.WithModel(bedrock.ModelClaudeSonnet5),
    ai.WithEndpointResolver(resolver),
)
```

The resolver must return `URL == nil`, no query values, and no credential
scope. `Deployment` becomes only the protected AWS SDK `ModelId`.
`RouteIdentity` is the non-secret, versioned identity used to distinguish cache
behavior. Raw model/profile IDs and ARNs are excluded from reports,
fingerprints, logs, and spans. Change the route identity when a routing change
can affect the answer.

Resolvers may run during cache fingerprint preflight and again during live
execution, so the general resolver rules still apply: make them deterministic,
side-effect-free, and concurrency-safe. The AWS SDK—not the resolver—continues
to own the service endpoint, region, credentials, SigV4 signing, and HTTP
transport. Bedrock rejects `WithCredentialSource`, `WithHTTPClient`, static
request headers, and any resolver URL/query/credential scope.

The Bedrock provider uses one boundary-aware, case-insensitive family
classifier for both policy mutation and final validation:

- Sonnet 5 and Opus 4.7/4.8 omit modified `temperature`, `top_p`, and `top_k`.
- Fable 5 accepts temperature `1` or omission and top-p in `[0.99, 1)`, and
  rejects top-k; incompatible inherited temperature and top-k are omitted,
  while documented-valid explicit values survive compatible and strict modes.
- Unrelated and legacy models retain ordinary sampling so a model is not
  classified merely because it is served by Bedrock.
- Mythos is intentionally absent because its current Bedrock model cards expose
  the Messages surface rather than Converse.
- Application rules can narrow current compatibility, but final validation
  prevents them from restoring a model-invalid sampling value.

Model access remains an AWS account concern. For example, AWS currently states
that Claude Fable 5 requires its documented provider-data-sharing retention
mode; the TruvaG3 adapter does not opt an account into that policy. Verify the
[Fable 5 model card](https://docs.aws.amazon.com/bedrock/latest/userguide/model-card-anthropic-claude-fable-5.html)
before selecting it.

Sync and streaming use the same route, logical draft, built-in rules,
application policy, and final validation. Reasoning-content stream deltas are
intentionally omitted from TruvaG3's text-only normalized output; only text
deltas become response content. A future Bedrock Mantle Messages adapter or
reasoning-block replay would require a separate provider-surface design.

`WithMaxRetries(n)` counts retries after the first request. The Bedrock adapter
sets the AWS SDK operation maximum to `n+1` total attempts, with a minimum of
one, for Converse, ConverseStream, and InvokeModel. Policy and routing run once
per logical request; the SDK owns retry classification, delay, replay, and
fresh signing. Typed Bedrock service exceptions are returned as
`core.ProviderError` values while retaining the original SDK error through
`errors.Is` and `errors.As`, allowing chain failover to distinguish validation,
authorization, throttling, quota, timeout, and server failures.

An explicitly selected standalone Bedrock provider declares a 60-minute
default request timeout, and direct `bedrock.NewClient` construction uses the
same value. Auto-detected clients and framework-managed chain entries retain
the failover-safe 180-second framework default; use `ai.WithChainTimeout` to
override managed chain entries. Caller-owned `ClientEntry` values are not
mutated. An explicit `ai.WithTimeout` wins, and context cancellation also stops
AWS SDK retries.

The Bedrock-specific `GetEmbeddings` helper defaults to Amazon Titan Text
Embeddings V2 and deliberately remains a single-text convenience API rather
than claiming to implement the provider-neutral batch `core.EmbeddingClient`:

```go
func titanVector(
    ctx context.Context,
    region string,
    text string,
) ([]float32, error) {
    awsConfig, err := bedrock.CreateAWSConfig(ctx, region)
    if err != nil {
        return nil, err
    }
    client := bedrock.NewClient(
        awsConfig,
        region,
        &core.NoOpLogger{},
    )
    return client.GetEmbeddings(
        ctx,
        text,
        bedrock.WithEmbeddingDimensions(512),
        bedrock.WithEmbeddingNormalization(true),
    )
}
```

The model defaults to `amazon.titan-embed-text-v2:0`. Supported dimensions are
`256`, `512`, and `1024`; omitting the option uses AWS's default. The helper
sends only `inputText` plus configured `dimensions` and `normalize`, and
decodes the float `embedding` member. Per-call options operate on a local copy
and do not mutate client defaults. Except for the explicit Titan V1 migration
pin below, use `bedrock.WithEmbeddingModel` only for a model that accepts this
same Titan V2 request/response shape.

Titan V2 defaults to 1024 dimensions; Titan V1 produces 1536. If an existing
vector store was populated by V1, pin that model until the index is explicitly
migrated or rebuilt:

```go
vector, err := client.GetEmbeddings(
    ctx,
    text,
    bedrock.WithEmbeddingModel(bedrock.ModelTitanEmbedV1),
)
```

Do not pass `WithEmbeddingDimensions` or `WithEmbeddingNormalization` with
`ModelTitanEmbedV1`; the framework rejects those V2-only controls before the
AWS SDK call. Do not mix V1 and V2 vectors in one index.

Keep the following official references with the deployment configuration,
because model catalogs and regional availability change independently of the
framework:

- [Converse API `modelId` forms and errors](https://docs.aws.amazon.com/bedrock/latest/APIReference/API_runtime_Converse.html)
- [Claude Sonnet 5 model card and routing IDs](https://docs.aws.amazon.com/bedrock/latest/userguide/model-card-anthropic-claude-sonnet-5.html)
- [Claude Opus 4.7 model card and routing IDs](https://docs.aws.amazon.com/bedrock/latest/userguide/model-card-anthropic-claude-opus-4-7.html)
- [Claude Opus 4.8 model card and routing IDs](https://docs.aws.amazon.com/bedrock/latest/userguide/model-card-anthropic-claude-opus-4-8.html)
- [Claude Fable 5 sampling contract](https://docs.aws.amazon.com/bedrock/latest/userguide/model-card-anthropic-claude-fable-5.html)
- [Claude Mythos 5 Messages surface](https://docs.aws.amazon.com/bedrock/latest/userguide/model-card-anthropic-claude-mythos-5.html)
- [Cross-region inference](https://docs.aws.amazon.com/bedrock/latest/userguide/cross-region-inference.html)
- [AWS SDK for Go v2 retries and context timeouts](https://docs.aws.amazon.com/sdk-for-go/v2/developer-guide/configure-retries-timeouts.html)
- [Claude Messages timeout guidance](https://docs.aws.amazon.com/bedrock/latest/userguide/model-parameters-anthropic-claude-messages.html)
- [Titan Text Embeddings V2 model](https://docs.aws.amazon.com/bedrock/latest/userguide/titan-embedding-models.html)
- [Titan V2 request and response fields](https://docs.aws.amazon.com/bedrock/latest/userguide/model-parameters-titan-embed-text.html)
- [Anthropic sampling-parameter deprecations](https://platform.claude.com/docs/en/about-claude/model-deprecations)

### Know the Compatibility Boundary

The generic OpenAI recipe is appropriate only while a hosted endpoint accepts
the framework's OpenAI chat contract. Azure OpenAI and Vertex Claude use their
own first-class profiles because their protected wire shapes differ. Review
this table before calling an integration complete:

| Requirement | TruvaG3 behavior | What to do |
|---|---|---|
| Generic OpenAI-compatible text | Emits protected `messages` and body `model` | Use the OpenAI recipe only when the endpoint accepts both |
| Generic OpenAI-compatible sync | Omits `stream` rather than sending `false` | Confirm absence is accepted |
| Generic OpenAI-compatible streaming | Sends `stream: true` and `stream_options.include_usage: true` | Contract-test the endpoint's SSE and final usage behavior |
| API key or bearer token | Exact header can be supplied per attempt | Use `WithAuthHeader` or `CredentialSource` |
| Deployment path or required query | Complete URL and query can be resolved locally | Use `EndpointResolver` |
| Extra top-level request fields | Supported through `WithExtra` or scoped request rules unless protected | Validate the provider actually honors the field |
| Standard chat response and usage | Normalized into `core.AIResult`; unknown response members are ignored | Use directly when extra response members are not application data |
| Azure OpenAI v1 or classic | Reported as provider `azureopenai` with the exact alias | Use the first-class Azure profiles and route-owned deployment |
| Vertex Claude | Reported as provider `anthropic`, alias `anthropic.vertex` | Use the first-class Vertex profile and route-owned publisher model |
| AWS Bedrock Runtime | Reported as provider `bedrock`; semantic model remains separate from SDK `ModelId` | Use the SDK-native Bedrock profile and optional route-owned model/profile ID |
| Google OpenAI compatibility or a private generic gateway | Reported as provider `openai` | Register a custom provider only if distinct operational identity is required |
| Native Vertex `generateContent`, provider-specific tool objects, image/audio results, or a proprietary envelope | Outside the portable text contract | Implement a provider-specific adapter and contract |

An endpoint accepting an OpenAI-shaped URL is not enough. Run at least one
sync request, one streaming request if used, one authentication refresh, one
401/403 rejection, and one request for every provider-native field on which the
application relies. This small contract suite catches cloud API-version drift
without adding cloud-specific OAuth or SDK dependencies to TruvaG3.

---

## 5. Scenario 2: Apply Request Rules and Middleware

**Problem:** Every entity-extraction request must ask OpenAI for JSON, and the
application should fail construction if that policy is invalid.

**Solution:** Attach a validated, immutable policy snapshot when constructing
the client.

A request rule is a small, named transformation of the provider's **logical**
request. It runs before the request becomes transport bytes. For the Acme
example, the intent is:

```text
Before the rule                     After the rule
-------------------------------     ----------------------------------------
model: "gpt-..."                     model: "gpt-..."
messages: [...]                      messages: [...]
                                    response_format: {type: "json_object"}
```

The rule does not need to construct an HTTP request or know the gateway URL.
The provider draft translates the logical path into the correct wire field.

### Declarative Request Rules

```go
client, err := ai.NewRequestClient(
    ai.WithProvider("openai"),
    ai.WithRequestRules(core.AIProviderPatch{
        Name:    "json-for-extraction",
        Version: "1",
        Selector: core.AIProviderSelector{
            Provider: "openai",
            Purpose:  "entity_extraction",
        },
        Set: map[string]interface{}{
            "/response_format": map[string]interface{}{
                "type": "json_object",
            },
        },
    }),
    ai.WithCompatibilityMode(requestpolicy.CompatibilityStrict),
)
```

Read this rule from top to bottom:

1. `Name` and `Version` give the behavior a stable identity for reports,
   fingerprints, and cache invalidation.
2. `Selector` limits it to OpenAI requests whose purpose is
   `entity_extraction`.
3. `Set` adds or replaces `/response_format` in the logical request.

Body paths use RFC 6901 JSON Pointer syntax. For example,
`/response_format/type` means the `type` member inside `response_format`.
Selectors can match provider, provider alias, surface, model, operation, and
purpose. Use `AllProviders` only for a rule intentionally valid across every
provider surface; the same path can mean different things on different
provider drafts.

`Set` values must be JSON-native: string-keyed maps, slices, arrays, finite
scalar values, or `nil`. Pointers, structs such as `time.Time`, non-finite
floats, non-string map keys, functions, channels, and cyclic values are
rejected during defensive cloning.

Rules are validated and copied at construction. Per-request patches are also
copied before evaluation, so callers can safely reuse or mutate their original
configuration after construction without changing the live client.

### Policy Order and Protected Fields

The effective order is:

1. clone the request, resolve the semantic model, and validate portable intent
2. resolve and validate the route
3. select the explicit provider wire profile and construct the draft
4. apply provider built-in compatibility rules
5. apply application rules in declaration order
6. apply middleware in declaration order
7. apply per-request `AIRequest.Patches` in declaration order
8. run compatibility and provider-draft validation, then encode

Later application layers can intentionally refine earlier application policy,
but provider drafts protect structural and transport-owned fields. Policy
cannot mutate the resolved model, streaming invariants, credentials, or
content-type headers. Route resolution runs before policy so a route-owned
deployment can select the protected wire shape, but deployment, query, and
credential scope are not exposed to policy selectors or request reports.

### Compatible vs Strict Mode

| Mode | Behavior |
|---|---|
| `CompatibilityCompatible` | Allows a provider built-in rule to adjust explicit intent and records the adjustment |
| `CompatibilityStrict` | Rejects an unacknowledged built-in adjustment to explicit request intent |

Compatible mode is the default and is appropriate when provider compatibility
should take priority automatically. Strict mode is useful when an application
must explicitly approve such behavior. An application rule, middleware, or
per-request patch touching the affected path counts as that approval.

For example, suppose a caller explicitly sets `temperature` but the resolved
model family rejects all sampling controls:

- In compatible mode, the provider's built-in rule removes temperature and the
  request report records that adjustment.
- In strict mode, the request fails before network I/O unless application
  policy explicitly acknowledges the removal.

Strict mode is therefore not "more compatible." It is a change-control guard
for applications that prefer a visible failure over an automatic provider
adjustment.

### Request Middleware

Use a declarative rule when the condition can be expressed by request identity
such as provider, model, operation, or purpose. Use middleware when the
decision needs runtime context or application logic that a selector cannot
express:

```go
type TenantPolicy struct{}

func (TenantPolicy) Name() string    { return "tenant-policy" }
func (TenantPolicy) Version() string { return "2" }

func (TenantPolicy) Apply(
    ctx context.Context,
    editor requestpolicy.RequestEditor,
) error {
    if editor.Info().Purpose == "brief_summary" {
        return editor.Set("/max_tokens", 500)
    }
    return nil
}
```

Attach it when the client is constructed:

```go
client, err := ai.NewRequestClient(
    ai.WithProvider("openai"),
    ai.WithRequestMiddleware(TenantPolicy{}),
)
```

Middleware receives only a constrained `RequestEditor`, not a raw HTTP request
or provider SDK object. Middleware retained by a client must:

- be safe for concurrent calls
- never retain the call-local editor
- use a stable name and version
- return errors rather than partially hiding failed policy

Middleware is fingerprint-unstable by default. Implement
`requestpolicy.StableRequestMiddleware` and return `true` only when its name and
version fully identify deterministic behavior.

---

## 6. Scenario 3: Build a Heterogeneous Failover Chain

**Problem:** The primary and fallback providers use different credentials,
regions, policy, or even different client implementations.

**Solution:** Build a request-aware chain with independently configured entries.

`NewChain` is different from putting several names in a homogeneous provider
list. Each entry is a complete client configuration:

```text
Request
  │
  ├─ 1. primary-us
  │     OpenAI + US resolver + US credentials
  │
  ├─ 2. secondary-eu
  │     Anthropic + EU resolver + EU credentials
  │
  └─ 3. private-fallback
        caller-owned client
```

```go
chain, err := ai.NewChain(
    ai.ProviderEntry("primary-us", "openai",
        ai.WithEndpointResolver(usResolver),
        ai.WithCredentialSource(usCredentials),
    ),
    ai.ProviderEntry("secondary-eu", "anthropic",
        ai.WithEndpointResolver(euResolver),
        ai.WithCredentialSource(euCredentials),
    ),
    ai.ClientEntry("private-fallback", privateClient),
)
```

Entry names must be unique, stable, non-secret operator labels. They may appear
in reports, logs, and traces.

| Entry type | Use it when |
|---|---|
| `ProviderEntry` | The framework should construct a request-aware provider with entry-specific options |
| `ClientEntry` | You already own a client instance, including a legacy or custom client |

The chain invokes a `ClientEntry` client but never mutates it through optional
logger, telemetry, or lifecycle setters. `ProviderEntry` currently supports
OpenAI, Azure OpenAI, Anthropic (including `anthropic.vertex`), and build-tagged
Bedrock; use `ClientEntry` to place a legacy-only provider such as Gemini in
the chain. The second `ProviderEntry` argument is the exact provider alias, so
use `azureopenai.v1`, `azureopenai.classic`, or `anthropic.vertex` when that
surface is required.

Both sync and streaming calls use the core capability-aware dispatchers. A
legacy client receives a request only when it can represent that request
losslessly. Streaming failover is allowed only before a provider emits the
first chunk—switching providers after visible output would duplicate or
corrupt the stream.

For each attempted entry, model aliases, built-in compatibility rules, app
policy, routing, and credentials are evaluated for that entry. The chain does
not prepare one OpenAI body and send it to Anthropic; every provider gets a
fresh provider-local preparation of the same `AIRequest`.

`NewChainClient` remains available for the legacy homogeneous configuration
style. Prefer `NewChain` when entries need independent credentials, routes,
policy, or concrete client implementations.

**Cache behavior:** AI-output caches treat a chain as one semantically
interchangeable logical service. A hit may return output produced by a
different entry than the provider that would win today. Only place providers
in one chain when their answers are acceptable substitutes.

---

## 7. Scenario 4: Implement a New Provider

Create a provider only after confirming that built-in configuration and
enterprise hooks cannot express the integration. A provider owns its identity,
model resolution, logical request draft, wire or SDK conversion, response
normalization, and provider-specific validation.

It should not leak those details into `core`, orchestration, or agent packages.

### What the Provider Owns

A provider factory is only the construction entry point. A complete adapter has
several responsibilities:

| Part | Responsibility |
|---|---|
| Factory | Detect configuration, validate it, and construct clients |
| Client | Implement sync and, when supported, streaming calls |
| Model resolver | Turn portable aliases such as `smart` into provider model IDs |
| Draft | Represent a call-local logical request that policy can safely edit |
| Transport | Execute HTTP or SDK calls, including retry and attempt-local auth |
| Decoder | Convert provider responses and stream events into `core.AIResponse` |
| Error translator | Preserve useful provider status while returning framework-compatible errors |

The provider should use one preparation path for sync and streaming. Otherwise
a rule, model restriction, or protected header can behave differently
depending on whether the caller streams.

### A Practical Package Layout

File names are not part of the API, but this layout keeps responsibilities
clear:

```text
ai/providers/acme/
├── factory.go          registration and construction
├── client.go           Generate, Stream, and shared preparation
├── models.go           aliases and capability classification
├── draft.go            provider-local requestpolicy.Draft
├── transport.go        HTTP or SDK execution
├── decode.go           response and stream normalization
└── *_test.go           parity, isolation, policy, retry, and error tests
```

An application activates the provider with a blank import, just like built-in
providers:

```go
import (
    "github.com/truvaagents/truva-g3/ai"
    _ "example.com/company/ai/providers/acme"
)
```

The blank import runs the provider package's `init` function, which registers
the factory. Calls such as `ai.WithProvider("acme")` can then resolve it through
the registry.

### Choose the Adapter Shape

Choose the adapter from the provider's real request surface, not from the name
of its service or gateway:

| Provider surface | Build this | Framework code you reuse |
|---|---|---|
| OpenAI-compatible chat completions over HTTP | HTTP adapter with an honest provider identity | `openaiwire.Codec`, `requestpolicy.Engine`, `providers.BaseClient` |
| Provider-specific JSON over HTTP | HTTP adapter with a provider-local draft and encoder | `requestpolicy.Document`, `requestpolicy.Engine`, `providers.BaseClient` |
| Native SDK operation | SDK adapter with a provider-local logical draft | `requestpolicy.Document`, `requestpolicy.Engine`; the provider SDK owns execution |

Do not register a new provider when only the hostname, token source, TLS
transport, or request policy changes; Section 4 already supplies those seams.
Conversely, do not label a non-compatible SDK as OpenAI merely to reuse its
transport. Provider identity is part of selection, reports, traces, errors,
and cache fingerprints.

### Provider Factory Contracts

Every provider registers the legacy `ai.ProviderFactory`. A new provider should
also implement the optional validated and request-aware contracts:

| Contract | Purpose |
|---|---|
| `ProviderFactory` | Registration, environment detection, and legacy client construction |
| `ValidatedProviderFactory` | Error-capable legacy construction |
| `RequestProviderFactory` | Request-aware construction with an isolated integration snapshot |
| `ProviderRequestTimeoutFactory` | Optional positive request-timeout default when 180 seconds is not appropriate |

```go
type Factory struct{}

var (
    _ ai.ProviderFactory          = (*Factory)(nil)
    _ ai.ValidatedProviderFactory = (*Factory)(nil)
    _ ai.RequestProviderFactory   = (*Factory)(nil)
)

func (*Factory) Name() string        { return "acme" }
func (*Factory) Description() string { return "Acme enterprise LLM" }

func (*Factory) DetectEnvironment() (int, bool) {
    return 500, os.Getenv("ACME_TOKEN") != ""
}

func (*Factory) Create(config *ai.AIConfig) core.AIClient {
    client, err := buildClient(config, ai.ProviderIntegrationConfig{})
    if err != nil {
        return &errorClient{err: err}
    }
    return client
}

func (*Factory) CreateValidated(
    config *ai.AIConfig,
) (core.AIClient, error) {
    return buildClient(config, ai.ProviderIntegrationConfig{})
}

func (*Factory) CreateRequestClient(
    config *ai.AIConfig,
    integration ai.ProviderIntegrationConfig,
) (core.AIRequestClient, error) {
    return buildClient(config, integration)
}

func init() {
    ai.MustRegister(&Factory{})
}
```

`buildClient` is provider code, not a framework function. It should validate a
non-nil configuration, clone caller-owned header and extra containers, create
the immutable policy engine, configure transport, and return one client that
implements both the legacy and request-aware interfaces. Its integration
configuration is already validated and snapshotted by `ai.NewRequestClient`,
but the provider must explicitly reject integration hooks its transport cannot
honor.

The non-panicking legacy error adapter is small and complete:

```go
type errorClient struct {
    err error
}

func (client *errorClient) GenerateResponse(
    context.Context,
    string,
    *core.AIOptions,
) (*core.AIResponse, error) {
    return nil, client.err
}
```

`NewClient` prefers `CreateValidated`, preventing configuration errors from
becoming construction-time panics. Because the legacy `Create` method cannot
return an error, use a small provider-local `errorClient` that consistently
reports the saved construction error instead of panicking.

`NewRequestClient` uses `CreateRequestClient` and passes an isolated, validated
integration snapshot. Treat `AIConfig.Headers`, `AIConfig.Extra`, request
rules, and provider requests as caller-owned. Clone them before applying
defaults or mutating nested values.

Most providers should keep the framework's 180-second request timeout. If the
official operation contract needs different default headroom, the factory may
also implement:

```go
func (*Factory) DefaultRequestTimeout() time.Duration {
    return 60 * time.Minute
}
```

The root constructor uses this positive value only for an explicitly selected
standalone provider when the application did not supply a positive
`ai.WithTimeout`. Bedrock uses the optional contract for its 60-minute
standalone default. Auto-detected clients and framework-managed chains retain
180 seconds; caller-owned chain clients remain untouched. Do not branch on
provider names in root construction.

A practical implementation order is:

1. validate and snapshot `AIConfig` and `ProviderIntegrationConfig`
2. create one immutable request-policy engine for the client
3. implement model resolution and one call-local draft
4. implement a shared prepare function used by sync, stream, and fingerprint
   preflight
5. add transport execution and response normalization
6. expose the client through `CreateRequestClient`
7. keep `CreateValidated` and the legacy `Create` adapter for compatibility

When a request cannot be represented, return `core.AIRequestFeatureError`. This
lets chains and callers use
`errors.Is(err, core.ErrAIRequestFeatureUnsupported)` instead of parsing error
strings.

### Implement the Legacy Compatibility Bridge

A request-aware provider still implements `core.AIClient`; this keeps existing
agents working and lets the registry expose one client. Make the legacy method
an adapter into the request-aware implementation so both APIs use identical
model resolution, policy, routing, transport, and decoding:

```go
var _ core.AIRequestClient = (*Client)(nil)

func (client *Client) GenerateResponse(
    ctx context.Context,
    prompt string,
    options *core.AIOptions,
) (*core.AIResponse, error) {
    request := core.NewAIRequestFromLegacy(prompt, "", options)
    result, err := client.Generate(ctx, request)
    if err != nil {
        return nil, err
    }
    if result == nil || result.Response == nil {
        return nil, errors.New("acme returned no response")
    }
    return result.Response, nil
}
```

If the provider streams, use the same direction for the streaming bridge:

```go
var _ core.StreamingAIRequestClient = (*Client)(nil)
var _ core.StreamingAIClient = (*Client)(nil)

func (client *Client) StreamResponse(
    ctx context.Context,
    prompt string,
    options *core.AIOptions,
    callback core.StreamCallback,
) (*core.AIResponse, error) {
    request := core.NewAIRequestFromLegacy(prompt, "", options)
    result, err := client.Stream(ctx, request, callback)
    if result == nil || result.Response == nil {
        if err == nil {
            err = errors.New("acme returned no streaming response")
        }
        return nil, err
    }
    return result.Response, err
}

func (*Client) SupportsStreaming() bool { return true }
```

The request-aware methods must reject nil requests, and `Stream` must reject a
nil callback. A provider without streaming should omit both streaming
interfaces; `core.StreamAI` then returns a typed feature error instead of
pretending streaming is available.

### Reuse the OpenAI-Compatible Codec

If the provider uses an OpenAI-compatible chat-completions wire format, reuse
`ai/providerkit/openaiwire`. The codec owns:

- request draft construction
- OpenAI-compatible reasoning and parameter behavior
- JSON encoding
- sync and streaming response decoding
- normalized usage extraction

It deliberately does **not** own provider identity, endpoint routing,
credentials, retries, telemetry, or lifecycle.

A new custom adapter should follow this sequence:

1. construct the codec with `openaiwire.NewProfiledCodec`
2. resolve and validate the provider's semantic model
3. resolve and validate the route
4. derive an explicit `openaiwire.RequestProfile` from semantic model facts and
   route-owned wire identity
5. call `BuildDraftWithProfile`, then `Draft.BindIdentity`
6. apply one shared `requestpolicy.Engine`
7. call `Encode` and execute the transport
8. call `Decode` or `DecodeStream`
9. attach the sanitized policy report, route fingerprint, and usage details

Using the shared codec avoids reimplementing the most failure-prone parts of
OpenAI-compatible sync, streaming, reasoning, and usage handling while keeping
your provider's operational identity honest.

Create the codec and policy engine once when constructing the client:

```go
codec, err := openaiwire.NewProfiledCodec(openaiwire.Config{
    SurfaceVersion: "acme-chat-completions-v1",
})
if err != nil {
    return nil, fmt.Errorf("create Acme wire codec: %w", err)
}

policy, err := requestpolicy.NewEngine(requestpolicy.Config{
    BuiltIns:   acmeCompatibilityRules(),
    AppRules:   integration.RequestRules,
    Middleware: integration.RequestMiddleware,
    Mode:       integration.CompatibilityMode,
})
if err != nil {
    return nil, fmt.Errorf("configure Acme request policy: %w", err)
}
```

The surface version is an adapter-contract version, not a release version.
Change it when the same logical request could produce a meaningfully different
wire request after an adapter change.

Then use one preparation function for sync, stream, and fingerprint preflight:

```go
type preparedRequest struct {
    body   []byte
    draft  *openaiwire.Draft
    report *core.AIRequestReport
    route  resolvedRoute
}

func (client *Client) prepare(
    ctx context.Context,
    request *core.AIRequest,
    stream bool,
) (*preparedRequest, error) {
    if request == nil {
        return nil, errors.New("Acme AI request is nil")
    }

    resolvedModel, err := client.resolveModel(request)
    if err != nil {
        return nil, err
    }
    operation := "generate"
    if stream {
        operation = "stream"
    }
    route, err := client.resolveEndpoint(ctx, ai.EndpointRequest{
        Provider:      "acme",
        ProviderAlias: client.providerAlias,
        Surface:       "chat-completions",
        ResolvedModel: resolvedModel,
        Operation:     operation,
        Purpose:       request.Purpose,
    })
    if err != nil {
        return nil, err
    }
    profile, err := client.requestProfile(resolvedModel, route)
    if err != nil {
        return nil, err
    }
    draft, err := client.codec.BuildDraftWithProfile(
        request,
        profile,
        stream,
    )
    if err != nil {
        return nil, err
    }
    if err := draft.BindIdentity("acme", client.providerAlias); err != nil {
        return nil, err
    }

    report, err := client.policy.Apply(ctx, draft, request.Patches)
    if report != nil {
        report.Adjustments = append(draft.Adjustments(), report.Adjustments...)
    }
    if err != nil {
        return nil, err
    }
    body, err := client.codec.Encode(draft)
    if err != nil {
        return nil, fmt.Errorf("encode Acme request: %w", err)
    }
    bindRouteFingerprint(report, route.routeIdentity)
    return &preparedRequest{
        body:   body,
        draft:  draft,
        report: report,
        route:  route,
    }, nil
}
```

`resolvedRoute`, `resolveEndpoint`, and `requestProfile` are provider-local
types and functions in this illustrative adapter. `resolveModel` is the
provider-owned alias lookup. It must return an error for an unknown or empty
result and avoid mutating the request. `resolveEndpoint` must invoke the public
resolver before draft construction, validate the complete URL and operation,
and snapshot the result. `requestProfile` must keep the semantic model separate
from any route-owned wire model or deployment and return a fully populated
`openaiwire.RequestProfile`.

Do not infer that a model is a reasoning family merely because a capability
row says which reasoning-control spelling a surface accepts. Classify the
model family with an explicit family predicate, then choose
`TokenLimitMaxCompletionTokens` and `SamplingReasoningRestricted` only for that
family. `ReasoningEffortTopLevel` and `ReasoningEffortNestedObject` describe
wire spelling, not model classification. In particular, a non-reasoning
`openai.ollama` model must retain `max_tokens`, ordinary sampling, and emit its
nested reasoning object only when effort is actually set.

`acmeCompatibilityRules` is the provider-owned, versioned list of model
restrictions; application rules are supplied separately through the
integration snapshot.

`NewConfiguredCodec` and `BuildDraft` remain as backward-compatible stock
OpenAI helpers. New provider adapters should use the profiled API so route
resolution and the semantic-versus-wire model distinction cannot be hidden in
an implicit default.

For HTTP execution, build the request body from `*bytes.Reader` or
`*bytes.Buffer`, copy `draft.Headers()` into a new `http.Header`, set structural
headers such as `Content-Type` in the transport layer, and call
`providers.BaseClient.ExecuteWithRetry` or `ExecuteWithRetryPrepared`. Decode a
successful response with `client.codec.Decode`; decode a successful event
stream with `client.codec.DecodeStream`. Always close the response body.

The adapter still owns the following deliberately provider-specific work:

| Provider-owned function | Required behavior |
|---|---|
| Model resolver | Resolve portable aliases and validate semantic model families before routing and policy |
| Endpoint builder | Produce a complete request URL and validate scheme, host, user info, path, and fragment |
| Route resolver bridge | Call `EndpointResolver` before wire-profile and draft construction; validate and snapshot its result |
| Wire-profile resolver | Select protected model, token-limit, reasoning-spelling, and sampling modes without conflating surface spelling with model family |
| Credential bridge | Acquire and validate one header on every attempt; reject conflicts with prepared headers |
| HTTP status decoder | Return a provider error with status, provider, resolved model, transient, and retryable classification |
| Lifecycle | Apply timeout/retry configuration, expose logger/telemetry setters when required, and close owned resources |

Do not include credentials, the prompt, raw body, full endpoint, deployment,
credential scope, or route query in errors, logs, reports, or fingerprints.
The only route value eligible for a fingerprint is the resolver's stable,
non-secret `RouteIdentity`.

### Adapt Provider-Specific HTTP JSON

For a non-OpenAI JSON API, use the same client and transport lifecycle but
replace `openaiwire.Codec` with a provider-local draft, encoder, and decoder.
Build the draft on `requestpolicy.Document` so rules and middleware retain the
framework's JSON Pointer, cloning, protected-field, header, report, and
fingerprint semantics:

```go
document, err := requestpolicy.NewDocument(requestpolicy.DocumentConfig{
    Info: requestpolicy.RequestInfo{
        Provider:       "acme",
        ProviderAlias:  client.providerAlias,
        Surface:        "acme-messages",
        Operation:      operation,
        Purpose:        request.Purpose,
        RequestedModel: requestedModel,
        ResolvedModel:  resolvedModel,
    },
    Body: map[string]interface{}{
        "model": resolvedModel,
        "input": []interface{}{
            map[string]interface{}{
                "role": "user",
                "text": request.Prompt,
            },
        },
    },
    ProtectedPaths: []string{
        "/model",
        "/input",
        "/stream",
    },
    ProtectedHeaders: []string{
        "Authorization",
        "Content-Type",
    },
})
```

Wrap the document in a provider `Draft` that implements `Validate`,
`HasExplicitIntent`, and `PolicyFingerprintIdentity`, as shown in the next
section. After policy succeeds, encode only `draft.Body()` with `json.Marshal`.
Set protected structural and credential headers after policy, execute through
`providers.BaseClient`, and decode the provider envelope into
`core.AIResponse`, `core.TokenUsage`, and optional `AIUsageDetails`.

The provider must document its patchable logical paths and expected JSON value
types. A patch path is part of the provider integration contract even if the
external API uses a different field name. Version the draft identity when a
path is renamed or its meaning changes. If a dynamic credential source returns
a header name that policy already set, fail the request rather than overwrite
either value.

### Adapt an SDK-Native Provider

An SDK-native provider should expose a provider-local logical draft that
implements `requestpolicy.Draft`. `requestpolicy.Document` supplies the shared
JSON Pointer and eligible-header behavior; the provider wrapper adds portable
parameter mapping, invariants, and SDK conversion.

A minimal draft has this shape:

```go
const adapterVersion = "acme-converse-v1"

type Draft struct {
    *requestpolicy.Document
    resolvedModel string
    explicit      map[string]struct{}
}

func newDraft(
    resolvedModel string,
    request *core.AIRequest,
    stream bool,
) (*Draft, error) {
    if request == nil {
        return nil, errors.New("Acme AI request is nil")
    }
    request, err := core.CloneAIRequest(request)
    if err != nil {
        return nil, fmt.Errorf("clone Acme request: %w", err)
    }

    operation := "generate"
    if stream {
        operation = "stream"
    }
    body := map[string]interface{}{
        "model": resolvedModel,
        "messages": []interface{}{
            map[string]interface{}{
                "role":    "user",
                "content": request.Prompt,
            },
        },
    }
    explicit := make(map[string]struct{})
    // Apply inherited defaults and each Generation Set/Omit mode here.

    requestedModel := request.Generation.Model
    if requestedModel == "" {
        if legacy := request.LegacyOptions(); legacy != nil {
            requestedModel = legacy.Model
        }
    }

    document, err := requestpolicy.NewDocument(requestpolicy.DocumentConfig{
        Info: requestpolicy.RequestInfo{
            Provider:       "acme",
            ProviderAlias:  "acme",
            Surface:        "converse",
            Operation:      operation,
            Purpose:        request.Purpose,
            RequestedModel: requestedModel,
            ResolvedModel:  resolvedModel,
        },
        Body:             body,
        ProtectedPaths:   []string{"/model", "/messages"},
        ProtectedHeaders: []string{"*"},
    })
    if err != nil {
        return nil, err
    }
    draft := &Draft{
        Document:      document,
        resolvedModel: resolvedModel,
        explicit:      explicit,
    }
    return draft, draft.Validate()
}

func (draft *Draft) HasExplicitIntent(path string) bool {
    _, exists := draft.explicit[path]
    return exists
}

func (*Draft) PolicyFingerprintIdentity() string { return adapterVersion }

func (draft *Draft) SetHeader(name, _ string) error {
    return fmt.Errorf("header %q is unsupported by the Acme SDK surface", name)
}

func (draft *Draft) RemoveHeader(name string) error {
    return fmt.Errorf("header %q is unsupported by the Acme SDK surface", name)
}

func (*Draft) Header(string) (string, bool) { return "", false }
```

For example, a provider that accepts temperature from 0 through 1 can apply
that portable field without losing explicit zero:

```go
func applyTemperature(
    body map[string]interface{},
    parameter core.AIParameter[float32],
    explicit map[string]struct{},
) error {
    switch parameter.Mode {
    case core.AIParameterInherit:
        return nil
    case core.AIParameterSet:
        if parameter.Value < 0 || parameter.Value > 1 {
            return errors.New("generation.temperature must be between 0 and 1")
        }
        body["temperature"] = parameter.Value
        explicit["/temperature"] = struct{}{}
        return nil
    case core.AIParameterOmit:
        delete(body, "temperature")
        return nil
    default:
        return fmt.Errorf(
            "invalid generation.temperature mode %d",
            parameter.Mode,
        )
    }
}
```

Apply the same switch deliberately for top-p, top-k, max tokens, system prompt,
reasoning effort, and response format. Use the provider's actual logical path,
range, and type. If the provider cannot support `Set`, return an
`AIRequestFeatureError`; if it supports only omission, accept `Omit` and reject
`Set`. Run these mappings after request-local defaults are cloned and before
the policy engine, so built-in compatibility rules can see explicit intent.

Apply every portable parameter deliberately:

| Parameter mode | Draft behavior |
|---|---|
| `Inherit` | Keep the cloned legacy value or provider default |
| `Set` | Validate and write the provider-local logical path; record that path in `explicit` |
| `Omit` | Remove the provider-local value, including inherited defaults |
| Unsupported `Set` | Return `*core.AIRequestFeatureError` naming the portable feature |
| Invalid mode | Return a validation error before SDK or network execution |

`Validate` runs after built-ins, application rules, middleware, and
per-request patches. It must re-check protected structural fields, required
messages, allowed logical fields, mutually exclusive options, numeric ranges,
and model-specific restrictions. Only after validation should a provider-local
`toSDKInput` function translate `draft.Body()` into the SDK's request types.
Never expose SDK input or output types through `core`.

An SDK adapter normally rejects `CredentialSource` and `HTTPClient` in
`CreateRequestClient` with `AIRequestFeatureError`, because its SDK owns those
concerns. Routing is provider-specific: reject `EndpointResolver` unless the
adapter defines a constrained SDK-destination contract. Bedrock is the
built-in example—it accepts only `Deployment` plus a sanitized
`RouteIdentity`, and rejects URL, query, and credential-scope fields. If an SDK
offers other equivalent, safe injection points, support them explicitly and
document their attempt, concurrency, and secrecy semantics. Sync and streaming
must call the same `newDraft` and policy engine; only `toSDKInput` and SDK
execution should differ.

Do not make an SDK-native provider masquerade as an OpenAI endpoint merely to
reuse unrelated transport code.

### Return Chain-Aware Errors

Return `AIRequestFeatureError` for unrepresentable request intent. Return a
provider error for a completed HTTP or SDK response that failed, so failover
can use structured status and classification rather than message matching:

```go
type providerError struct {
    cause      error
    status     int
    provider   string
    model      string
    transient  bool
    retryable  bool
}

func (err *providerError) Error() string     { return err.cause.Error() }
func (err *providerError) Unwrap() error     { return err.cause }
func (err *providerError) StatusCode() int   { return err.status }
func (err *providerError) Provider() string  { return err.provider }
func (err *providerError) Model() string     { return err.model }
func (err *providerError) IsTransient() bool { return err.transient }
func (err *providerError) IsRetryable() bool { return err.retryable }

var _ core.ProviderError = (*providerError)(nil)
```

Derive `transient` and `retryable` from status codes and documented provider
error metadata, not string matching at the chain layer. A malformed-request 4xx
normally sets neither flag. A proxy-generated failure may be transient even
when its status is unusual; an account or capacity error may be provider-
retryable without being a transport failure.

Preserve `context.Canceled` and `context.DeadlineExceeded` so callers can use
`errors.Is`. For streaming, an error before the first delivered chunk may be
eligible for chain failover. After any chunk has been delivered, return the
partial normalized result and an error matching
`core.ErrStreamPartiallyCompleted`; the chain must not restart the answer on a
different provider.

### Implement Semantic Fingerprinting

The policy engine creates the base fingerprint when the draft supplies a
versioned `PolicyFingerprintIdentity`. If endpoint routing can change output,
fold only the stable `RouteIdentity` into that fingerprint:

```go
func bindRouteFingerprint(report *core.AIRequestReport, routeIdentity string) {
    if report == nil || !report.Stable || report.Fingerprint == "" {
        return
    }
    sum := sha256.Sum256([]byte(
        "policy=" + report.Fingerprint + "\nroute=" + routeIdentity,
    ))
    report.Fingerprint = hex.EncodeToString(sum[:])
}

func (client *Client) RequestFingerprint(
    ctx context.Context,
    request *core.AIRequest,
) (string, bool) {
    prepared, err := client.prepare(ctx, request, false)
    if err != nil || prepared.report == nil {
        return "", false
    }
    return prepared.report.Fingerprint,
        prepared.report.Stable && prepared.report.Fingerprint != ""
}
```

The shared `prepare` path above resolves and binds the route before it applies
policy, so fingerprint preflight and execution use the same semantic and wire
profile. Fingerprint preflight may run before the real call. It may clone,
resolve a model, run a deterministic endpoint resolver, select a profile,
apply policy, and encode locally.
It must not perform network I/O, acquire credentials, consume rate limits, or
mutate application state. Middleware makes a fingerprint unstable unless it
implements `StableRequestMiddleware` and returns true.

If the client does not implement `AIRequestFingerprinter`, or cannot produce a
stable identity, framework AI-output caches safely bypass reads and writes.

### Test the Provider Contract

Provider tests should verify behavior at the public boundary, not only private
helpers. Start with compile-time interface assertions and a recording transport
or fake SDK; no live provider account is required.

At minimum, cover these cases:

| Test group | Assertions |
|---|---|
| Factory | Registration name, environment priority, validation errors, no panic from legacy `Create` |
| Isolation | Caller request, `AIOptions`, headers, extras, and nested patch values remain unchanged |
| Model and policy | Alias resolves before model rules; selector boundary matches; built-in/app/middleware/request order is preserved |
| Wire profiles | Semantic and route-owned wire models stay distinct; surface spelling does not classify an ordinary model as reasoning |
| Presence | `Set` preserves explicit zero; `Omit` removes inherited values; unsupported values return `AIRequestFeatureError` |
| Protected state | Model, messages, stream flags, content type, and credentials cannot be changed by patches |
| Sync/stream parity | The same semantic request produces the same model, policy edits, headers, and report identity |
| Retry | Every attempt receives identical body bytes and a newly acquired credential |
| Routing | Resolver inputs are sanitized; route identity affects fingerprint; query and credential scope do not appear in reports |
| Errors | Nil input, malformed response, empty choices/events, HTTP failure, SDK failure, callback abort, and partial stream failure |
| Fingerprint | Stable inputs repeat; policy/model/route version changes invalidate; unstable middleware returns `stable=false` |
| Concurrency | Shared middleware, resolver, credential source, and client pass `go test -race` |

A useful policy-order test records the final wire body in a fake transport. Give
the same path four different values—built-in, app rule, middleware, and
per-request patch—and assert the per-request value wins. Run that test for both
`Generate` and `Stream`; it catches preparation drift without depending on a
provider service.

For an OpenAI-compatible adapter, add a regression fixture proving that a
non-reasoning Ollama model retains `max_tokens`, ordinary sampling, and no
nested reasoning object unless reasoning effort is explicitly set. This guards
against treating a surface's reasoning-control spelling as a model-family
classification.

---

## 8. Retries and Replayable Request Bodies

**Problem:** An HTTP retry reuses a consumed body and silently sends an empty
request.

An HTTP body is a stream. Once attempt 1 reads it, its cursor is at the end:

```text
Without replay support
attempt 1: reads request bytes → provider returns retryable 503
attempt 2: reads EOF           → empty or invalid provider request

With replay support
attempt 1: fresh reader → provider returns retryable 503
attempt 2: fresh reader → the same request bytes are sent again
```

**Framework guarantee:** `providers.BaseClient.ExecuteWithRetry` rejects a
non-replayable body before transport attempt 0—even when retries are configured
to zero. The early failure makes the provider contract predictable and avoids
a latent production bug when retries are enabled later.

`http.NewRequestWithContext` sets `GetBody` automatically when the body is:

- `*bytes.Buffer`
- `*bytes.Reader`
- `*strings.Reader`

For any other body source, set `GetBody` so it returns a new `io.ReadCloser`
containing the same bytes.

Use `ExecuteWithRetryPrepared` when credentials or other attempt-local
transport state must be attached after a fresh body is installed and
immediately before each HTTP attempt. Do not acquire a short-lived credential
once and reuse it across retries.

---

## 9. Security, Caching, and Observability

### Sanitized Reports and Fingerprints

Request-aware clients can return an `AIRequestReport` that explains:

- provider, alias, surface, operation, and purpose
- requested and resolved model
- sanitized policy adjustments
- semantic fingerprint and whether it is stable

Reports and fingerprints must never contain prompts, credentials, raw request
bodies, secret values, trusted route query parameters, or full secret-bearing
endpoints.

The fingerprint represents the semantic provider surface, resolved model,
policy rules and versions, deterministic middleware, and route identity. It is
an invalidation identity, not an audit log of secret request data.

Think of it as a version label for "how this AI request would be prepared." For
example:

```text
openai chat-completions
+ resolved model gpt-...
+ json-for-extraction rule version 1
+ route identity acme-us-v2
= one semantic fingerprint
```

Changing the prompt does not change this policy fingerprint. Changing the rule
version, resolved model, deterministic middleware, or semantic route does.

### AI-Output Caches

Clients may implement `core.AIRequestFingerprinter` so an AI-output cache can
obtain the semantic identity before lookup. Fingerprinting must not perform
network I/O or acquire credentials.

If an identity cannot be guaranteed stable, return `stable=false`. Callers must
bypass both reads and writes; serving an answer under an uncertain identity is
less safe than missing the cache.

TruvaG3's AI-backed caches follow this rule:

| Cache | Fingerprint behavior |
|---|---|
| Result distillation | Includes a stable AI policy fingerprint |
| Conversation summary | Includes a stable AI policy fingerprint and resets when it changes |
| Activity digest | Includes a stable AI policy fingerprint |

After a miss, these caches verify that the executed request report matches the
preflight fingerprint before writing. This protects against a route or policy
change between lookup and execution.

### Distributed Tracing

With telemetry enabled:

| Record | Kind | Purpose |
|---|---|---|
| `ai.generate` / `ai.stream` | Span | Logical normalized AI call |
| `ai.generate_response` / `ai.stream_response` | Span | Provider preparation and execution |
| `ai.http_attempt` | Span | One HTTP transport attempt, including each retry |
| `ai.request.prepared` | Event on the active orchestration phase span | Sanitized effective request report |

TruvaG3 records token usage where providers supply it. It does not emit
`ai.cost_usd`; provider pricing changes independently and is not treated as a
trustworthy framework-owned measurement.

See the
[Distributed Tracing Guide](../observability/DISTRIBUTED_TRACING_GUIDE.md#17-ai-module-distributed-tracing)
for the complete span hierarchy and attribute contract.

### Logging and Metrics

Provider request logs must use the context-aware logger methods and the
`framework/ai` component. Every request-scoped record has a stable `operation`.
When the incoming context carries `request_id`, the framework copies it into
the record; telemetry baggage takes precedence over the core request context
when both are present. Framework HTTP and orchestration entry points seed that
correlation value, while a direct application call with a bare context does
not synthesize one inside the provider. Error records use a bounded
`error_type` and a sanitized `error` string.

Custom adapters that embed `providers.BaseClient` should use
`LogRequestMetadata`, `LogResponseMetadata`, and `LogErrorMetadata` with the
exported `RequestObservation`, `ResponseObservation`, and `ErrorObservation`
values. Those helpers apply correlation, bounded provider labels, token
metrics, and the safe error shape consistently. On a provider-owned span, call
`providers.RecordObservationError(span, err, fallbackType)` before returning
the original error; it records only a sanitized error and returns the bounded
classifier. For an additional custom context-aware log,
`providers.AddObservationRequestID` applies the same baggage-first request-ID
precedence. Do not derive `error_type` from `err.Error()`; use
`providers.NormalizeObservationErrorType` or
`providers.SanitizedObservationError`.

Safe log and span fields include bounded provider/surface identity, semantic
model, purpose, status, duration, token counts, prompt/response lengths, and a
sanitized route identity where the contract explicitly allows it. Never emit
prompt or response content, system prompts, serialized bodies, credentials,
credential scopes, complete endpoints, query values, or route-owned Azure
deployments and Vertex publisher-model IDs. A provider error body is not safe
merely because it arrived as an `error`.

Metrics use bounded dimensions such as `module`, provider, status, token type,
and bounded error type. Do not label metrics with semantic or wire model,
provider alias, chain-entry name, route identity, deployment, publisher model,
endpoint, request ID, or tenant identity. Keep those diagnostic identities in
sanitized logs, reports, and spans instead of creating unbounded time series.

See the
[Logging Implementation Guide](../observability/LOGGING_IMPLEMENTATION_GUIDE.md#11-required-patterns-for-framework-level-logging)
for the framework logging contract and component propagation rules.

---

## 10. Putting It Together: The Acme Gateway

The following construction combines the running example. The resolver,
credential source, and HTTP client are application-owned implementations from
Section 4:

```go
jsonExtractionRule := core.AIProviderPatch{
    Name:    "json-for-extraction",
    Version: "1",
    Selector: core.AIProviderSelector{
        Provider: "openai",
        Purpose:  "entity_extraction",
    },
    Set: map[string]interface{}{
        "/response_format": map[string]interface{}{
            "type": "json_object",
        },
    },
}

chain, err := ai.NewChain(
    ai.ProviderEntry("acme-openai-primary", "openai",
        ai.WithModel("smart"),
        ai.WithRequestRules(jsonExtractionRule),
        ai.WithEndpointResolver(acmeRoutes),
        ai.WithCredentialSource(&gatewayCredentials{
            tokens: tokenProvider,
        }),
        ai.WithHTTPClient(corporateHTTPClient),
    ),
    ai.ProviderEntry("acme-openai-secondary", "openai",
        ai.WithModel("smart"),
        ai.WithRequestRules(jsonExtractionRule),
        ai.WithEndpointResolver(acmeBackupRoutes),
        ai.WithCredentialSource(&gatewayCredentials{
            tokens: tokenProvider,
        }),
        ai.WithHTTPClient(corporateHTTPClient),
    ),
)
if err != nil {
    return err
}

request := core.NewAIRequest(
    "Extract the people and services mentioned in this incident.",
    "entity_extraction",
)
request.Generation.Temperature = core.SetAIParameter(float32(0))

result, err := core.GenerateAI(ctx, chain, request)
if err != nil {
    return err
}
fmt.Println(result.Response.Content)
```

Here is what happens at runtime:

1. The chain starts with `acme-openai-primary`.
2. The OpenAI adapter resolves `smart` to a concrete semantic model.
3. `acmeRoutes` selects the correct Acme gateway and returns a stable,
   non-secret route identity.
4. The adapter selects its wire profile, constructs the draft, and sees that
   `jsonExtractionRule` matches the request purpose.
5. The rule adds the JSON response format. The provider validates and encodes
   the draft.
6. `gatewayCredentials` obtains a token scoped to that route. The token is
   attached only to the HTTP attempt.
7. If OpenAI succeeds, the chain returns its normalized result and sanitized
   preparation report.
8. If the primary route fails with an error that permits failover before
   streaming output begins, the chain prepares the original `AIRequest` again
   for `acme-openai-secondary`. The same versioned JSON rule applies, while the
   secondary resolver and credential attempt remain independent.

If Acme later replaces its OpenAI-compatible gateway with a proprietary SDK,
the agent call and `AIRequest` can remain unchanged. The affected chain entries
move from the built-in OpenAI adapter to the custom factory described in
Section 7.

---

## 11. Troubleshooting Common Issues

### Issue 1: `NewRequestClient` Returns "Feature Unsupported"

**Cause:** The selected factory does not implement request-aware construction.
Gemini is currently legacy-only, and custom factories must implement
`RequestProviderFactory`.

**Fix:** Use `NewClient` for a legacy call, inject the legacy client with
`ClientEntry`, or add a request-aware factory implementation. Do not cast away
the error or silently discard request intent.

### Issue 2: A Request Fails During Legacy Fallback

**Cause:** The request contains a presence-aware value, omission, patch, or
other feature that `core.AIOptions` cannot represent losslessly.

**Fix:** Use a request-aware provider, remove the unsupported intent if it is
not required, or handle `core.ErrAIRequestFeatureUnsupported` as a deliberate
capability refusal.

### Issue 3: A Rule Fails While Constructing the Client

**Cause:** A selector, JSON Pointer, rule identity, or `Set` value is invalid.
Common examples are a `time.Time` value, a pointer, `NaN`, a non-string map key,
or a cyclic value.

**Fix:** Convert policy values to JSON-native data and give every reusable rule
a stable, non-empty name and version.

### Issue 4: Policy Cannot Change a Field or Header

**Cause:** The provider owns that structural or transport field. Typical
protected values include the resolved model, stream flag, credential headers,
and content type.

**Fix:** Configure structural values through typed client or request options.
Configure credentials and routes through their dedicated integration hooks.

### Issue 5: Strict Compatibility Rejects a Request

**Cause:** A provider built-in rule must adjust explicit request intent, but
the application has not acknowledged that adjustment.

**Fix:** Confirm the provider contract, then add an app rule, middleware edit,
or per-request patch for that path. Use compatible mode only when automatic
provider adjustment is acceptable.

### Issue 6: The Endpoint Resolver Runs Twice

**Cause:** A cache asks for a fingerprint before lookup, then the actual call
resolves the endpoint again after a miss.

**Fix:** Keep resolvers deterministic, local, and side-effect-free. Move token
acquisition and remote discovery out of the resolver.

### Issue 7: An AI-Output Cache Is Always Bypassed

**Cause:** Middleware has not declared a stable policy fingerprint, the route
identity is unstable, or the client cannot guarantee deterministic request
identity.

**Fix:** Implement `StableRequestMiddleware` only when name and version fully
describe deterministic behavior. Otherwise the bypass is the safe outcome.

### Issue 8: HTTP Execution Says the Body Is Not Replayable

**Cause:** The provider built its request from a consumed `io.Reader` without a
`GetBody` function.

**Fix:** Use `bytes.Buffer`, `bytes.Reader`, or `strings.Reader`, or supply a
`GetBody` function that reconstructs the body.

### Issue 9: Streaming Does Not Try the Next Provider

**Cause:** The current provider already emitted a chunk. Failover after that
point would mix two providers in one visible stream.

**Fix:** Treat the partial-stream error as final. If failover before output is
important, make connection and preflight failures happen before invoking the
callback.

### Issue 10: A Credential Is Missing from Reports or Traces

**Cause:** This is intentional. Credential names and values are transport-only
secrets and are excluded from reports, fingerprints, logs, and span data.

**Fix:** Observe credential-source success or failure using secret-free metrics
inside your credential implementation. Never add the credential value to
framework telemetry.

### Issue 11: Azure or Vertex Reports No Deployment for the Model

**Cause:** Resolver maps are keyed by the post-alias
`EndpointRequest.ResolvedModel`, but the application used an alias such as
`smart` as the map key. The route is therefore missing for the concrete
semantic model.

**Fix:** Inspect the concrete catalog or configured environment override and
key the Azure deployment or Vertex publisher-model map by that resolved
semantic ID. Keep the route-owned deployment or publisher ID only in the map
value. For Azure classic, also use a model supported by the pinned ordinary
`2024-10-21` surface; semantic reasoning models are deliberately refused.

### Issue 12: A Hosted Endpoint Works Until Its First Token Expires

**Cause:** A startup access token was captured as a static API key. Azure Entra,
Google, and enterprise OAuth access tokens are not permanent credentials.

**Fix:** Supply an application-owned, concurrency-safe token source through
`WithAuthHeader` or `WithCredentialSource`. Refresh before expiry and implement
`CredentialRejectionObserver` when a 401/403 should invalidate cached state.

### Issue 13: Google Accepts a Request but Ignores a Field

**Cause:** Google's OpenAI compatibility surface documents a subset of the
OpenAI parameters and may ignore unsupported fields. A provider-native patch
can be syntactically valid to TruvaG3 even when the remote service does nothing
with it.

**Fix:** Scope Google-specific rules by provider and model, and add a real
endpoint contract test that proves every semantically required field changes
the observed behavior. Do not infer support from HTTP 200 alone.

### Issue 14: Hosted Streaming Fails While Synchronous Calls Work

**Cause:** Streaming adds protected `stream: true` and
`stream_options.include_usage: true` fields and expects standard SSE chunks.
The hosted endpoint accepts ordinary chat JSON but differs on one of those
streaming details.

**Fix:** Contract-test streaming separately. If the endpoint cannot accept the
protected request shape or does not return standard OpenAI SSE, use a custom
stream adapter rather than weakening the shared codec invariants.

### Issue 15: Vertex Claude Rejects the Route Before Authentication

**Cause:** The first-class `anthropic.vertex` adapter strictly validates the
host/location pairing, publisher path, publisher-model deployment, URL
components, and `rawPredict` versus `streamRawPredict` operation. A global host
with a regional path, a regional host with a port, or a sync method on a stream
call is rejected locally.

**Fix:** Build the URL structurally with the recipe helper and return the exact
publisher-model ID in `ResolvedEndpoint.Deployment`. Use the global,
US/EU multi-region, or regional hostname that matches the path location.

---

## 12. Production Review Checklist

Before registering or shipping a provider:

- keep provider wire and SDK types below the provider boundary
- use the same preparation path for sync and streaming
- resolve aliases before model-specific compatibility rules
- apply defaults to a request-local clone, never caller-owned maps or slices
- validate and snapshot rules at construction time
- keep credentials, prompts, raw bodies, full endpoints, and secret values out
  of reports, fingerprints, logs, and span attributes
- make middleware, credential sources, and endpoint resolvers concurrency-safe
- version deterministic policy behavior and return `stable=false` otherwise
- reject unsupported request intent with `AIRequestFeatureError`
- make HTTP request bodies replayable before using the retry helper
- document every supported patch path and its expected JSON value type
- reject integration hooks that the provider transport cannot honor
- test sync/stream parity and caller isolation
- test protected fields, selector boundaries, and validation failures
- test credential rotation, rejection observation, and route identity
- contract-test every hosted endpoint's exact body, authentication header,
  API-version query, response envelope, and streaming shape
- keep cloud token acquisition and refresh application-owned; inject only the
  current credential at the transport boundary
- use the first-class `azureopenai.v1`, `azureopenai.classic`, or
  `anthropic.vertex` identity for those surfaces; decide separately whether a
  generic Google OpenAI endpoint or private gateway may be reported as
  `openai`
- test fingerprint stability, unstable bypass, and cache write guards
- run the repository's complete Go pre-commit gate set

---

## 13. Quick Reference

### Core Request Types

| Type | Purpose |
|---|---|
| `core.AIRequest` | Provider-neutral prompt, purpose, generation intent, and per-request patches |
| `core.AIParameter[T]` | Distinguishes inherit, explicit set, and omit |
| `core.AIResult` | Normalized response, sanitized report, and optional usage |
| `core.AIRequestReport` | Secret-free effective request identity and adjustments |
| `core.AIRequestFeatureError` | Typed refusal when request intent cannot be represented |

### Main Extension Points

| Extension point | Owns | Must not own |
|---|---|---|
| `AIProviderPatch` | Declarative logical body/header policy | Credentials or raw transport mutation |
| `RequestMiddleware` | Context-aware constrained policy edits | Retained request editors or mutable call state |
| `EndpointResolver` | Complete HTTP URL or explicitly supported SDK destination, plus stable route identity | Credentials, network discovery, side effects |
| `CredentialSource` | Attempt-local authentication | Reports, fingerprints, or routing policy |
| `RequestProviderFactory` | Request-aware provider construction | Agent or orchestration behavior |
| `ProviderRequestTimeoutFactory` | Optional provider request-timeout default | Overriding an explicit positive application timeout |
| `openaiwire.Codec` | OpenAI-compatible wire encoding/decoding | Provider identity, routing, credentials, retries, telemetry |
| `requestpolicy.Document` | JSON Pointer edits, protected paths, and eligible headers | Provider validation or SDK conversion |
| `requestpolicy.Engine` | Ordered policy, reports, and base fingerprints | Routing, credentials, transport, or decoding |
| `providers.BaseClient` | Replay-safe HTTP retries and attempt spans | Provider wire semantics or response decoding |

### Error Handling

```go
if errors.Is(err, core.ErrAIRequestFeatureUnsupported) {
    // The client cannot preserve this request's semantics.
}

var featureErr *core.AIRequestFeatureError
if errors.As(err, &featureErr) {
    // Inspect the typed ClientType and Feature fields as needed.
}
```

---

## 14. See Also

- [AI Providers Setup Guide](AI_PROVIDERS_SETUP_GUIDE.md) — API keys, provider
  aliases, model aliases, ordinary failover, timeouts, and built-in setup
- [AI Provider Change Playbook](AI_PROVIDER_CHANGE_PLAYBOOK.md) — day-0
  responses to model, parameter, authentication, endpoint, and response drift
- [API Reference](../reference/API_REFERENCE.md#request-aware-ai-api) —
  package-level request-aware interfaces and type signatures
- [Framework Design Principles](../../FRAMEWORK_DESIGN_PRINCIPLES.md) —
  stability, ownership, isolation, and dependency rules
- [AI Architecture](../../ai/ARCHITECTURE.md) — AI module boundaries and
  provider extension points
- [Core Architecture](../../core/ARCHITECTURE.md) — provider-neutral contracts
  and dependency direction
- [Distributed Tracing Guide](../observability/DISTRIBUTED_TRACING_GUIDE.md#17-ai-module-distributed-tracing) —
  logical spans, provider spans, and sanitized preparation events
- [Logging Implementation Guide](../observability/LOGGING_IMPLEMENTATION_GUIDE.md#11-required-patterns-for-framework-level-logging) —
  context-aware framework logs, correlation fields, and component conventions
- [Result Compaction Guide](../orchestration/RESULT_COMPACTION_GUIDE.md) —
  fingerprint-aware distillation, summary, and digest caching
