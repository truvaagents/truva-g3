# Changelog

## v0.6.0

Requires Go 1.27.2 or newer. See the [root changelog](../CHANGELOG.md)
for coordinated module versions and adoption requirements.

### Compatibility and adoption

- Identify deterministic request-policy rejections (patch validation, mutation,
  Strict compatibility, draft validation and output capacity) as
  `core.ErrInvalidConfiguration` through `errors.Is`. They do not count against
  AI provider-health circuit breakers. Preserve wrapped causes; middleware and
  unknown extension stages retain their cause's classification.
- Clarify existing decoded-retry adoption: native OpenAI and compatible aliases
  skip same-provider retries on classified quota-exhausted HTTP 429 responses.
  Ordinary transient rate limits retain bounded retries; quota exhaustion can
  still fail over to the next chain entry. Request-level mocked chain tests cover
  quota failover, transient recovery and transient exhaustion.

- Align decoded-attempt success logging with transport-only retries: emit
  `ai_http_success` at Debug on first success and include `duration_ms` in
  `ai_request_recovery` at Info. Decoded duration includes response consumption
  and matches the successful attempt span, excluding prior attempts and waits.
  Errors and partial streams emit no success log; retry behavior, response
  fidelity, correlation and logical metric ownership remain unchanged.

- Bound framework-added reasoning headroom before compatibility policy, preserving
  Strict acceptance of valid caller budgets (50,000 × 3 becomes 128,000 for a
  reviewed 128,000-capacity model). Over-cap explicit base budgets still require
  application acknowledgement in Strict; final post-policy capacity checks remain.
  Add `RequestProfile.ReasoningHeadroomLimit`, bounded-headroom report/cache identity,
  and capacity-only 128,000 rows for native `gpt-5.6-sol` (including aliases resolving
  to it) and routed `openai/gpt-5.6-luna`. No new default-effort inference for these
  two rows, metadata fetching, Azure/snapshot capacity inference or retry changes.

- Apply reasoning headroom even with absent effort for reviewed exact native
  GPT-6 Astra/Sol/Luna/GPT-6.1 Sol and routed GPT-6 Luna/GPT-6.1 Sol. Providers
  supply the reviewed fact through `RequestProfile.ReasoningActiveByDefault`;
  no effort is injected and sampling/routing stay unchanged. Unknown defaults,
  explicit omission/`none`, native-only controls and multiplier 1 retain exact
  budgeting. Reports and cache identities distinguish inferred headroom.
- Wire `TRUVAG3_AI_TIMEOUT` into root AI constructors and managed chain entries.
  Positive code > positive-duration environment > existing default. Invalid
  present environment values fail construction. Five-minute defaults, standalone
  Bedrock's 60-minute default, caller deadlines, retry/failover and injected
  clients remain unchanged; output ceilings do not guarantee completion by a deadline.

- Bind active reasoning-token multipliers into the shared OpenAI-wire policy
  fingerprint, preventing AI-output cache reuse across 1x/3x/5x changes.
  Preserve stable identities for non-scaling and explicitly omitted reasoning.
  Align wire validation, output ceilings and effective reports on exact
  integral JSON-number handling, without changing caller-owned wire values.
  Fix overflow in the public legacy `BuildRequestBody` helper; it saturates at
  `math.MaxInt`, while the draft API rejects overflow before dispatch unless a
  reviewed limit safely bounds the derived headroom.

- General provider/managed-chain request timeout now defaults to 5 minutes.
  Explicit standalone Bedrock retains 60 minutes; explicit overrides still win.
  Reasoning-token scaling defaults to 3x, configurable in code or through
  `TRUVAG3_AI_REASONING_TOKEN_MULTIPLIER`; invalid explicit settings fail fast.
  Explicit 1x preserves an exact allowance. Supported active OpenRouter portable
  effort now uses scaling too. Reviewed exact native GPT-6 Astra/Sol/Luna and
  GPT-6.1 Sol, plus routed Luna/Sol, receive a 128000 output ceiling through
  Compatible/Strict policy and final validation. Unknown capacity remains unknown.
  Arithmetic overflow is rejected by the draft API; the legacy map-only helper
  saturates instead of wrapping. Reports record headroom and ceiling adjustments;
  retries, partial completion and routing/privacy ownership are unchanged.

- Set Anthropic `fast` to `claude-haiku-5-5` and `smart`/`code`/`vision` to
  `claude-opus-5-5`, whose standard token rates are lower than their predecessors.
  Sonnet/Fable aliases are unchanged. Add Haiku 5.5 to the existing built-in
  sampling-omission rules with Compatible/Strict adjustment reporting; Opus 5.5
  already matches the Opus 5 rule. Environment/concrete overrides, token budgets,
  native-extra pass-through and other provider catalogs remain unchanged.
  Vertex Claude uses the new semantic IDs too. Adopters should review native
  thinking/tool settings and small output budgets before rollout; see the
  [AI provider guide](README.md).

- Set OpenAI `fast` to `gpt-6-luna` and OpenRouter `fast` to
  `openai/gpt-6-luna`. Calls selecting that alias inherit the new model;
  concrete model pins and non-empty `TRUVAG3_OPENAI_MODEL_FAST` /
  `TRUVAG3_OPENROUTER_MODEL_FAST` overrides still win. Azure shared-catalog
  resolvers receive the new native semantic model. Existing sampling rules,
  reasoning effort, budgets, privacy, retry policy and other aliases are
  unchanged by this fast-tier migration.

- Change OpenAI's general `default` alias from `gpt-5.6-terra` to `gpt-6.1-sol`.
  Planning, synthesis and other calls inheriting the alias adopt it. The fast
  migration is listed separately above; smart, premium, code and vision remain
  unchanged.
  Non-empty `TRUVAG3_OPENAI_MODEL_DEFAULT` overrides and explicit concrete models
  remain authoritative. Azure/shared-catalog resolvers see the new semantic
  default too. Existing `none` effort is rejected for Sol, not silently changed;
  choose a supported effort, explicit omission, or pin the previous model.
- Support native `gpt-6.1-sol` and its hyphen-suffixed variants/snapshots through existing
  reasoning/JSON profiles and final validation. Unlike original GPT-6 Sol,
  GPT-6.1 Sol rejects `none`. Shared-codec consumers also recognize this exact
  family; arbitrary dotted GPT variants are not newly classified.
- Add independent exact OpenRouter `openai/gpt-6.1-sol` capability metadata,
  scoped temperature compatibility rule and fingerprinted final validation.
  Compatible/Strict policy semantics, application overrides within the provider
  contract and privacy routing are preserved. The separate budget update above
  changes active-effort scaling and adds reviewed output ceilings.
  The routed Sol support itself changes no aliases, retries or executor behavior;
  the fast-alias migration is documented separately above.
- Routed Sol final validation accepts JSON-equivalent typed/named reasoning
  maps without changing application values, and rejects null reasoning objects.
  The `openrouter-sol-6.1-v3` contract identity invalidates the earlier guards'
  cached results. Native-tool error guidance distinguishes original GPT-6 Sol
  from GPT-6.1 Sol.
- Validate exact routed Sol's `enabled`/`exclude` booleans and positive integer
  `max_tokens`. Reject disabled reasoning and final `effort` plus `max_tokens`
  combinations, including inherited/patched controls. Preserve exclusion,
  standalone budgets, application policy repair and unrelated extras/routes.
  Clarify that native suffix matching includes variants such as `-pro`, not only
  dated snapshots; recognition does not certify their provider contract.

- Explicit reasoning-effort omission now suppresses codec-default inheritance
  before budget/sampling preparation, including Azure v1. An omitted effort no
  longer applies the client's reasoning-token multiplier. Inherited and explicit
  active effort retain scaling; request reports reflect the final wire budget.
  Explicit-omission drafts carry `reasoning-omission-v1` in their policy identity
  to invalidate cached output from the old semantics. Other effort modes keep
  their identities.
- Compatible-codec surface/profile identities now include the v2 completion
  policy. Consumers of those identities incur a one-time cache invalidation;
  native OpenAI and Azure compatible-codec fingerprints change too. Unrelated
  providers that do not use this codec are outside this identity change.
- Default streaming delivery separates reasoning from final text. Reasoning-only
  callbacks have empty `Content`, nonempty `ReasoningContent` and
  `ContentSource=final` (the delivery policy, not a claim of nonempty final text), and count as
  delivered output for no-replay rules. Consumers must handle both channels.
- Non-HTML real HTTP failures in the decode-aware path now report
  `ai.attempt_status=decoded_error`, retaining the original HTTP status. Direct
  Azure classic/v1 does not adopt this decoded-retry loop.
- `IsReasoningModel` recognizes the GPT-6 family. Direct Azure v1 applies its
  reasoning profile to matching resolved semantic models; classic rejects that
  profile. A deployment's route name is not necessarily its semantic model.

- Add native OpenAI GPT-6 family-boundary matching, model-specific reasoning and
  sampling/tool restrictions, and request-policy validation. Preserve developer
  policy overrides within valid provider contracts. No per-model output-token
  ceiling is introduced by this change.
- Declare JSON-object capability for exact OpenRouter DeepSeek v4.1 Flash and
  GLM 5.3 Flash IDs. The existing JSON/schema and OpenAI-style reasoning row for
  exact GPT-5.6 Luna predates the comparison baseline. These rows do not grant features to unrelated models or
  every upstream endpoint; required-parameter routing remains enforced.

- Separate OpenAI-compatible final answers from provider reasoning by default;
  preserve exact finish reasons and content provenance in buffered/SSE responses,
  debug records and bounded logical-span metadata. Explicit single/chain/direct
  codec compatibility options restore the old substitution/concatenation with
  truthful provenance. Policy fingerprints vary by completion policy; reasoning
  delivery prevents retry/failover after a partial stream.

- Share one provider retry budget across HTTP/transport and classified decoded
  errors in OpenAI-compatible clients. Add an opt-in decode-aware base helper;
  retain other adapters' transport-only behavior and constructor-specific defaults.
  Preserve prepared requests, attempt-local credentials, terminal partial results,
  stream no-replay and chain/breaker ownership. Attempt spans now distinguish
  decoded failures from real HTTP status and carry available per-attempt usage.
  OpenRouter intentionally narrows actual non-HTML HTTP-error retries: unknown
  nonempty types on 429/5xx and untyped 501/505 no longer retry the same provider.
  Outer chain failover is unchanged; request-level regression tests pin this
  difference from other OpenAI-compatible HTTP routes.

- Add exact OpenRouter GPT-6 Luna capabilities and a scoped temperature-removal
  rule using the existing request-policy engine and Compatible/Strict modes.
  Preserve native-provider behavior, unrelated rule fingerprints and protected
  routing/privacy. Metadata/offline checks are not live inference certification.
- Classify unsupported client-default reasoning effort as Debug adaptation in
  OpenAI-compatible and direct Azure classic/v1 clients. Retain omission reports
  (add the missing Azure adjustment); preserve explicit unsupported errors and
  legacy per-request/native-extra warnings. No retry/failover policy changes.

## v0.5.0

See the [root changelog](../CHANGELOG.md) for the co-versioned release.
