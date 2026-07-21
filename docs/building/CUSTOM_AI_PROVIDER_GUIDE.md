# Custom AI Providers and Enterprise Integration

This guide explains how to extend TruvaG3's AI layer without coupling
orchestration code to a provider SDK or HTTP wire format. It covers the
request-aware API, request policy, dynamic credentials and routing,
heterogeneous failover chains, reusable wire codecs, and custom provider
factories.

For ordinary provider and model configuration, start with the
[AI Providers Setup Guide](AI_PROVIDERS_SETUP_GUIDE.md). For the package-level
contracts, see the [API Reference](../reference/API_REFERENCE.md#request-aware-ai-api).

## 1. Choose the Smallest Integration Surface

Use the simplest path that preserves the behavior you need:

| Need | Recommended path |
|---|---|
| A built-in provider with legacy options | `ai.NewClient(...)` |
| Presence-aware parameters, request policy, dynamic routing, or credentials | `ai.NewRequestClient(...)` |
| An OpenAI-compatible private endpoint | Configure the built-in `openai` provider; add `WithEndpointResolver` or `WithCredentialSource` when needed |
| A custom OpenAI-compatible provider identity or transport | Reuse `ai/providerkit/openaiwire` in a custom `RequestProviderFactory` |
| A provider with an SDK-native request model | Implement a provider-local `requestpolicy.Draft`, following the Bedrock adapter pattern |
| Failover across independently configured or injected clients | `ai.NewChain(...)` with `ProviderEntry` and `ClientEntry` |

`NewRequestClient` currently has request-aware built-in implementations for
Anthropic and OpenAI. Bedrock is also request-aware when the `bedrock` build tag
is enabled. Gemini remains available through the legacy client API; asking its
factory for request-aware construction fails with
`core.ErrAIRequestFeatureUnsupported` instead of silently dropping intent.

## 2. Provider-Neutral Requests

The request-aware API is defined in `core`, so orchestration and other
high-level modules do not need to import `ai`:

```go
client, err := ai.NewRequestClient(
    ai.WithProvider("openai"),
    ai.WithModel("smart"),
)
if err != nil {
    return err
}

request := core.NewAIRequest("Explain this incident", "incident_analysis")
request.Generation.Temperature = core.SetAIParameter(float32(0.2))
request.Generation.MaxTokens = core.SetAIParameter(1200)

result, err := core.GenerateAI(ctx, client, request)
if err != nil {
    return err
}
fmt.Println(result.Response.Content)
```

`Purpose` is a stable, provider-neutral, non-secret operation label. Providers
may expose it in sanitized reports, policy selectors, and traces. Do not put
user text, tenant secrets, credentials, or request IDs in it.

### Presence-aware parameters

`core.AIParameter[T]` distinguishes three intentions that a plain Go zero
value cannot:

| Mode | Meaning |
|---|---|
| `InheritAIParameter[T]()` or the zero value | Let lower-precedence defaults decide |
| `SetAIParameter(value)` | Explicitly send the value, including `0`, `false`, or an empty string |
| `OmitAIParameter[T]()` | Require that the provider request omit the field |

The presence-aware fields are `Temperature`, `TopP`, `TopK`, `MaxTokens`,
`SystemPrompt`, `ReasoningEffort`, and `ResponseFormat`. `Model` is structural:
an empty string inherits model selection, and model cannot be explicitly
omitted.

The legacy `core.AIOptions` API remains supported. Use it for existing code and
simple portable calls. Use `AIRequest` when omission, explicit zero values,
policy patches, reports, or enterprise integration behavior matters.

### Dispatch and feature refusal

Use `core.GenerateAI` and `core.StreamAI` when the concrete client capability is
not known. They prefer `AIRequestClient` and `StreamingAIRequestClient`. A
legacy client is used only when the request can be represented without losing
new semantics. Otherwise they return a typed `core.AIRequestFeatureError` that
matches `core.ErrAIRequestFeatureUnsupported`.

`AIResult` contains the normalized `AIResponse`, an optional sanitized
`AIRequestReport`, and optional provider usage details. Reports never contain
the prompt, credentials, raw body, or secret values.

## 3. Request Rules and Middleware

Request-aware clients accept application rules and constrained middleware at
construction time:

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
            "/response_format": map[string]interface{}{"type": "json_object"},
        },
    }),
    ai.WithCompatibilityMode(requestpolicy.CompatibilityStrict),
)
```

Body paths use RFC 6901 JSON Pointer syntax. `Set` values must be JSON-native:
string-keyed maps, slices, arrays, finite scalar values, or `nil`. Pointers,
structs such as `time.Time`, non-finite floats, non-string map keys, functions,
channels, and cyclic values are rejected during defensive cloning.

Selectors can match provider, alias, surface, model, operation, and purpose.
Use `AllProviders` only for rules intentionally valid across every provider
surface. Rules are validated and copied when the client is constructed, and
per-request patches are copied before evaluation.

The effective order is:

1. provider draft construction and model resolution
2. provider built-in compatibility rules
3. application rules in declaration order
4. middleware in declaration order
5. per-request `AIRequest.Patches` in declaration order
6. compatibility and provider-draft validation

Provider drafts protect structural fields and transport-owned headers. A patch
cannot mutate protected paths such as the resolved model or streaming
invariants, or credentials and content-type headers.

`CompatibilityCompatible` is the default and permits a provider's built-in
compatibility rule to adjust explicit intent while recording the adjustment.
`CompatibilityStrict` rejects an unacknowledged built-in adjustment. An app
rule, middleware, or per-request patch touching that path is treated as an
explicit application acknowledgment.

### Middleware contract

Request middleware receives only the constrained `requestpolicy.RequestEditor`:

```go
type TenantPolicy struct{}

func (TenantPolicy) Name() string    { return "tenant-policy" }
func (TenantPolicy) Version() string { return "2" }
func (TenantPolicy) Apply(ctx context.Context, editor requestpolicy.RequestEditor) error {
    if editor.Info().Purpose == "brief_summary" {
        return editor.Set("/max_tokens", 500)
    }
    return nil
}
```

Middleware retained by a client must be safe for concurrent use and must not
retain the call-local editor. It is considered fingerprint-unstable by default.
Implement `requestpolicy.StableRequestMiddleware` and return `true` only when
the middleware's name and version fully identify deterministic semantics.

## 4. Dynamic Credentials, Routing, and HTTP Clients

Enterprise HTTP integrations are configured with `ClientOption` values:

```go
client, err := ai.NewRequestClient(
    ai.WithProvider("openai"),
    ai.WithEndpointResolver(myResolver),
    ai.WithCredentialSource(myCredentialSource),
    ai.WithHTTPClient(sharedHTTPClient),
)
```

### Credential sources

`CredentialSource.Credential` runs for every transport attempt, after policy
and routing have completed. This permits short-lived token rotation and
retry-safe credentials. Implementations must be concurrency-safe. Credential
names and values are attached only at the transport boundary and are excluded
from request reports, fingerprints, and logs.

`WithAuthHeader` is a convenience adapter when the credential is one dynamic
header. A source may also implement `CredentialRejectionObserver`; it is
notified after HTTP 401 or 403 responses. Observer failures are diagnostic and
never replace the provider's original error.

### Endpoint resolvers

`EndpointResolver` runs after model resolution and policy preparation. It
returns a complete URL plus a stable, non-secret `RouteIdentity`. Cache
fingerprint preflight may evaluate a resolver and a cache miss may evaluate it
again, so it must be concurrency-safe, stable for the same semantic request,
and free of side effects. Do not perform credential acquisition or remote
discovery in a resolver.

`ResolvedEndpoint.Query`, `CredentialScope`, and `Deployment` are trusted
routing inputs and are excluded from reports. `RouteIdentity` is included in
the semantic fingerprint, so change it when a route change affects AI output.

`WithHTTPClient` accepts a caller-owned `http.Client`. Supporting providers
shallow-copy it and do not mutate the supplied value. HTTP-only integrations
are supported by the request-aware Anthropic and OpenAI clients. The SDK-native
Bedrock adapter rejects them explicitly.

## 5. Heterogeneous Failover Chains

`NewChain` builds a request-aware chain whose entries can have independent
configuration:

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

Entry names must be unique, stable, non-secret operator labels. They can appear
in sanitized reports, logs, and traces. `ProviderEntry` constructs a
request-aware built-in provider, while `ClientEntry` accepts any caller-owned
`core.AIClient`. The chain invokes injected clients but does not mutate them
through optional logger, telemetry, or lifecycle setters.

The chain uses the core request dispatchers for both sync and streaming calls.
It can fall back to a legacy client only for a losslessly representable
request. Streaming failover is allowed only before a provider has delivered a
chunk; switching providers after output is visible would duplicate or corrupt
the stream.

`NewChainClient` remains available for the legacy homogeneous configuration
style. Prefer `NewChain` when entries need different credentials, routes,
policy, or concrete client implementations.

AI-output caches treat a chain as one semantically interchangeable logical
service. Its fingerprint includes ordered entry identities, but a cache hit may
serve output previously produced by a different entry than the one that would
win today. Do not use one chain for providers whose answers are not acceptable
substitutes for each other.

## 6. Implementing a Provider Factory

Every provider registers a legacy `ai.ProviderFactory`. New providers should
also implement the optional error-capable and request-aware contracts:

```go
type Factory struct{}

func (*Factory) Name() string        { return "acme" }
func (*Factory) Description() string { return "Acme enterprise LLM" }
func (*Factory) DetectEnvironment() (int, bool) {
    return 500, os.Getenv("ACME_TOKEN") != ""
}

func (*Factory) Create(config *ai.AIConfig) core.AIClient {
    client, err := newLegacyClient(config)
    if err != nil {
        return &errorClient{err: err}
    }
    return client
}

func (*Factory) CreateValidated(config *ai.AIConfig) (core.AIClient, error) {
    return newLegacyClient(config)
}

func (*Factory) CreateRequestClient(
    config *ai.AIConfig,
    integration ai.ProviderIntegrationConfig,
) (core.AIRequestClient, error) {
    return newRequestClient(config, integration)
}

func init() { ai.MustRegister(&Factory{}) }
```

`NewClient` prefers `ValidatedProviderFactory.CreateValidated`, preventing
construction-time validation failures from becoming panics. `NewRequestClient`
uses `RequestProviderFactory.CreateRequestClient` and passes an isolated,
validated integration snapshot. Treat `AIConfig.Headers`, `AIConfig.Extra`,
request rules, and provider requests as caller-owned values: clone them before
defaulting or mutation.

When a request cannot be represented by a provider surface, return a
`core.AIRequestFeatureError`. This lets callers and chains use
`errors.Is(err, core.ErrAIRequestFeatureUnsupported)` instead of parsing error
strings.

### Reusing the OpenAI-compatible codec

`ai/providerkit/openaiwire` owns only the OpenAI chat-completions wire contract.
It deliberately does not own provider identity, endpoint routing, credentials,
retries, or telemetry.

A custom OpenAI-compatible adapter should:

1. create a codec with `openaiwire.NewConfiguredCodec`
2. resolve the portable model
3. call `BuildDraft`
4. call `Draft.BindIdentity` before policy evaluation
5. apply one shared `requestpolicy.Engine`
6. call `Encode`, execute the transport, then `Decode` or `DecodeStream`
7. attach the sanitized policy report and usage details to the result

Use a provider-local logical draft instead when the provider is SDK-native.
The build-tagged Bedrock Converse adapter demonstrates this approach without
masquerading as an OpenAI HTTP endpoint.

## 7. Retry and Request-Body Requirements

Providers using `providers.BaseClient.ExecuteWithRetry` must supply replayable
request bodies, even when retries are configured to zero. A non-replayable body
fails before transport attempt 0; this prevents a retry from silently sending
an empty body.

`http.NewRequestWithContext` sets `GetBody` automatically for
`*bytes.Buffer`, `*bytes.Reader`, and `*strings.Reader`. For any other body
source, set `GetBody` so it returns a new `io.ReadCloser`. Use
`ExecuteWithRetryPrepared` when credentials or other attempt-local transport
state must be applied after a fresh body is installed and immediately before
each HTTP attempt.

## 8. Fingerprints, Caches, and Observability

Request-aware clients expose a sanitized `AIRequestReport`. Its fingerprint
represents provider surface, resolved model, policy rules and versions,
deterministic middleware, and route identity. It intentionally excludes prompt
text and credentials.

Clients may implement `core.AIRequestFingerprinter` so an AI-output cache can
obtain that identity before lookup. The method must not perform network I/O or
acquire credentials. If the identity cannot be guaranteed stable, return
`stable=false`; callers must bypass both cache reads and writes.

TruvaG3's result-distillation, conversation-summary, and activity-digest caches
include stable AI policy fingerprints. They miss or reset when the fingerprint
changes and bypass caching for unstable requests. After a miss, these caches
also verify that the executed request report matches the preflight fingerprint
before writing.

With telemetry enabled, logical calls use `ai.generate` or `ai.stream`, provider
execution uses `ai.generate_response` or `ai.stream_response`, and the sanitized
report is recorded as the `ai.request.prepared` span event. See the
[Distributed Tracing Guide](../observability/DISTRIBUTED_TRACING_GUIDE.md#17-ai-module-distributed-tracing).

## 9. Provider Review Checklist

Before registering a provider:

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
- test sync/stream parity, caller isolation, protected fields, selector
  boundaries, error paths, credential rotation, route identity, and fingerprint
  stability
- run the repository's complete Go pre-commit gate set
