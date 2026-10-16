# Changelog

## v0.6.1

- Add opt-in `LastSuccessfulStep`, `SuccessfulToolData` and `ExecutionRequestID`
  inspection helpers. Inspect the final dispatch's raw response and reject
  ambiguous response envelopes, failed final dispatches or conflicting identity.
  Legacy/custom results without dispatches use their recorded Response with its
  existing fidelity limits. Domain receipt/outcome validation remains
  application-owned; planner, executor and retry behavior are unchanged.

## v0.6.0

Requires Go 1.27.2 or newer. See the [root changelog](../CHANGELOG.md)
for coordinated module versions and adoption requirements.

### Construction failure safety

- Reject integer overflow in built-in full and incremental activity-compaction
  budgets before AI invocation, and at enrichment-hook construction when that
  compactor is selected. Preserve the five-times-target allowance for valid
  budgets and leave custom compactor budget rules unchanged.
- Classify invalid cleanup-timeout environment values accurately as empty,
  invalid duration or out of range. All remain fatal in both environment modes;
  rejected raw values are not exposed.

- Preserve fatal environment-resolution errors in legacy defaults instead of
  silently replacing all settings with pure defaults. Check the new
  `DefaultConfigWithDiagnostics().Err` before reading its config. Error-returning
  factories reject the failure before bootstrap. `DefaultConfig` retains the
  error in a non-executable configuration; legacy errorless constructors return
  an inert instance whose `Start` and execution methods return that error.
- Validate cleanup and failed-operation view settings in legacy constructors
  as well as canonical construction. Unset direct cleanup gets the 5s default;
  explicitly non-positive cleanup and negative projection limits are rejected.
  Unrelated legacy partial-config semantics and per-setting compatible fallbacks
  remain unchanged. Add regression coverage for required/enum dispatch rejection.

### Three-day debug retention

- Default successful and failed execution records and LLM debug records to
  72 hours (3 days). Success retention increases from 24 hours; error retention
  decreases from 7 days. Explicit overrides and longer existing retention floors
  remain supported. This changes defaults for newly constructed stores, not
  existing Redis keys or running deployments. Checkpoint, workflow-state and
  memory retention are unchanged.
- Forward `TRUVAG3_EXECUTION_DEBUG_TTL` and
  `TRUVAG3_EXECUTION_DEBUG_ERROR_TTL` into the Redis preset's execution store.
  Invalid present durations fail loading for the selected execution role;
  explicit provider options still override valid environment values. Pure
  constructors, debug enablement and retention-floor behavior are unchanged.

### Terminal attribution and summary deadlines

- Attribute returned and stored terminal execution results to the owning request
  segment with copy-on-write metadata, including early failure publishers.
- Bound builtin event-summary AI calls to a configurable 2m default; distinguish
  child timeout from parent cancellation and retain successful late responses.
- These attribution and summary-deadline changes preserve plan validation,
  regeneration and executor behavior; other changes are listed separately below.

### Framework review corrections

- Keep distinct validation errors in bounded recency order when a regeneration
  repeats an earlier failure, retaining feedback about intervening errors.
- Recognize wrapped/joined partial-stream errors, preserving delivered content
  and available usage without retrying or marking the lifecycle successful.
  Interrupted synthesis calls now record failure and retain their original cause
  in debug storage and response errors. Delivery distinguishes interruption,
  cancellation and deadline expiry; its span receives bounded failure evidence.
- Preserve same-attempt provider request-policy evidence in authoritative
  planning, correction, tiered-selection and synthesis debug records.
- Deduplicate admitted memory detail by domain and event ID, matching storage
  identity; equal custom IDs in different domains no longer hide each other.

### Compatibility and adoption clarification

- Generous defaults: 20000 planning / 30000 synthesis output tokens; 6000
  synthesis skill-input tokens; 20 phases at 25m; 15m delegation execution
  allowance; 20m outbound HTTP; 64 KiB individual and 192 KiB aggregate result
  budgets. Continuation totals also default to 192 KiB. Parent deadlines and
  explicit overrides win; these ceilings are not reservations or model-limit
  guarantees. Default configuration/skill-budget fingerprints change accordingly.
- Event summaries default to 10000 output tokens per call. Explicit
  `WithSummarizerTokensPerStep` retains calculated budgeting; explicit fixed
  max-tokens wins regardless of option order. Parsing and fail-open semantics
  are unchanged. These larger caps may increase latency/cost and retained context.

- Explicit structured `retryable:false` stops all same-step resends and
  correction. Eligible non-transient 4xx may be corrected but are no longer
  resent unchanged; transport/408/429/5xx retry admitted inputs unchanged.
  Legacy correction callbacks are restricted to eligible 4xx, with no analyzer
  installed. Approved HITL inputs cannot be corrected, but ordinary approved
  steps can still be resent on eligible transient failures; use idempotency.
- Validate parameters before dispatch, including required fields, unresolved
  reserved references, enum membership and container/scalar mismatches. The
  lifecycle finalizer treats partial-content delivery as failed, not successful.
- Generated-plan normalization is model-only; shared dependency validation also
  applies to hook-mutated plans. Same-phase/surplus `implicit_deps` are rejected.
  Preserve resolved defaults when customizing `IterativePlanning`; its validated
  regeneration-round and feedback-byte fields must remain positive.
- Initial unknown-agent replacement parsing uses the hallucination-recovery budget;
  continuation unknown-agent recovery has one regeneration attempt. Parse/intent
  failures during validation regeneration consume validation rounds.
- Auto-wiring preserves source identity, declines conflicting matches and uses
  source-qualified `SourceKey` in the multi-source path. Application string bytes
  retain Markdown; only reserved numeric-index references are canonicalized.
- Compact planner return fields render `name:type` hints, not descriptions or
  examples. Authors must put essential invocation semantics in descriptions or
  guidance that reaches the planner. `WithFailedOperationView` replaces the
  entire environment-resolved configuration; it is not a per-field merge.
- Current memory views are `event-detail/v2-record-facts` and
  `historical-origin/v5-record-facts`; earlier v1/v3 entries below are historical.
  Invalid builder options now return construction errors. Negative activity
  prompt limits are rejected. Documented direct-constructor zero sentinels remain
  valid. Announcement environment values must be positive even though the direct
  announcement-hook constructor accepts zero as its default of 10. The separately
  documented zero-detail sentinel remains valid.

### Planning and payload integrity baseline

- Normalize numeric bracket references and derive prior-phase `implicit_deps`
  for fresh generated plans before existing validation. Keep same-phase
  dependencies planner-owned. Typed/direct and hook plans do not receive this
  normalization; hook results still face the stricter shared dependency checks.
- Reuse the phase catalog/skill projection during regeneration; supply bounded
  accumulated feedback and rejected-candidate evidence. Expose the 16-KiB
  `TRUVAG3_ITERATIVE_REGENERATION_FEEDBACK_MAX_BYTES` default. Malformed
  regenerated JSON consumes the existing correction budget.
- Preserve application strings through outer-wrapper and string-aware JSON
  repairs; reject valid JSON with incompatible field types without repair.
  Bind explicit references to their own source output, retain native types and
  provenance, and restrict missing-path inference to that source. Null is not
  converted into an invented empty value.
- Distinguish dependency skips from executed failures and refinement-satisfied
  work. Stop repeated dependency-only non-terminal phases with unchanged failed
  ancestors; actual dispatched work, including polling, resets that guard.
  Dependency skips no longer create duplicate failed-step memory events.

### Generic retry/completion hardening

- Classify transport and HTTP 408/429/5xx before every parameter-correction
  path; retry admitted inputs unchanged under existing budgets. Restrict 4xx
  correction to eligible mutable inputs; enforce explicit retry refusal and
  delegated single calls. These gates narrow previously broader correction paths.
- Preserve provider completion facts and separate reasoning in debug interactions.
- Complete recording coverage includes orchestration-owned planning, continuation,
  regeneration, correction, buffered/native-stream synthesis and tier-selection
  records when generic recording is deferred. Preserve per-attempt attribution,
  existing success/failure semantics and unknown facts for legacy responses.

### Truthful memory recording

- Bound heuristic failure causes with a configurable 256-byte default and
  explicit shortening; preserve original execution errors and custom summaries.
- Attribute excluded summary steps to `batch_limit` and report excluded counts.
- Replace the ambiguous combined backend-assignment hook-effect flag with
  independent ID/timestamp flags; existing historical effects remain unchanged.
- Include copied error facts in event summary inputs; bound failure/response
  projection with a configurable shared byte allowance and explicit omissions.
- Record summary source/fallback provenance and step occurrence time separately
  from recording time; project quality/time basis in versioned historical views.
- Add opt-in request entity extraction for recording without changing enrichment
  defaults or investigation ownership/cleanup. Count entity-less submitted events.
- Preserve raw execution payloads, public summarizer methods, query ordering,
  provider retention and executor behavior.

### Rejected-attempt diagnostic evidence

- Record each unavailable-agent rejection with the effective prompt, system
  prompt, raw response and catalog of that attempt. Later rejections previously
  attached the first attempt's payloads and catalog.
- Preserve planner feedback, recovery budgets, execution validation and diagnostic
  record types. No additional provider call or duplicated token usage is recorded.

### Latest planning-retry failure

- Remove the redundant zero-work sentence from active iterative instructions;
  retain the concrete terminal example, initial/retry guidance and legacy text.
- Correct malformed initial-planning replacements within the existing unknown-agent recovery
  budget using the latest parsing/intent error, selected catalog and system
  prompt. Preserve typed errors on exhaustion and keep ordinary agent recovery,
  phase fallback regeneration, validation and executor behavior.
- Emit correlated parsing/correction/exhaustion diagnostics without counting a
  parsing failure as another hallucination or inventing a model call.

### No-work prompt consistency

- Show explicit `{"terminal": true, "steps": []}` in the default initial builder,
  fallback/continuation planning rules and
  agent-name retry guidance. Limit omitted-terminal guidance to nonempty model
  plans in both active and legacy iterative instructions. Preserve decoder,
  typed/direct-plan defaults, executor behavior and existing retry budgets.

### Model clarification intent

- Reject missing or whitespace-only questions in fresh model clarification
  objects through existing bounded parse correction, including objects supplied
  alongside steps or terminal intent. Preserve valid question strings and typed,
  direct, refinement and resumed-plan behavior. Emit the bounded reason
  `invalid_clarification_request` on existing parse-error events.

### Trace-event request correlation

- Add the current request ID as the first attribute of template source inclusion
  and legacy investigation ownership events. Preserve their existing names,
  attributes, metric labels, binding behavior and claim ownership contracts.

### Explicit model plan intent

- Reject unknown-only envelopes, null roots and ambiguous zero-work model plans
  before normalization. Recognize nonempty legacy plans, explicit terminal intent
  and clarification; preserve typed/direct/refinement/HITL plan contracts.
- Feed bounded, encoded envelope-key diagnostics into existing parse correction
  attempts. Reuse the phase catalog and correction budget; record generic reason
  codes on existing parse-error events without changing executor/retry policy.

### Typed bindings and failed-operation evidence

- Preserve exact whole-value container bindings and JSON-looking string inputs;
  reject known container/scalar mismatches before dispatch without changing
  semantic nested extraction or application transform/approval ordering.
- Add configurable bounded same-attempt failure evidence to continuation and
  both built-in synthesis paths, an application restriction interface and an
  explicit delegation projector. Raw records and custom synthesizers remain
  unchanged. Report effective encoded limits, omissions and successful-step
  retention tradeoffs in request-correlated span events.

### Bounded digest windows

- Reuse activity digests only for an observed, matching bounded event window
  and configuration/AI identity. Deduplicate exact content, admit late events,
  rebaseline after expiry/edits/eviction, and preserve the prior cache on failure.
  Keep legacy episodic APIs fail-open; unknown read health bypasses cache reuse.
  Add read-health and digest-window hook effects plus bounded trace diagnostics.

### Post-memory verification corrections

- Emit request lifecycle independently of step success, with monotonic record
  revisions and latest-snapshot merges for delayed hook effects. Preserve legacy
  unknown state and request-ID-linked resume. Own activity renewal per request,
  serialize status changes, and join renewal before all-exit bounded cleanup;
  submission telemetry does not assert backend health.

- Manage token-safe investigation claims in a private request-segment scope with
  observed, bounded all-exit cleanup and fresh resume acquisition. Preserve the
  legacy custom-coordinator/direct-hook path. Add configurable 5s cleanup timeout
  with fail-fast explicit values; preserve original failure/panic and raw payloads.

- **Breaking construction contract:** return errors from `BuildMemoryHooks`; validate all hook-owned
  numeric environment values even without AI/dependencies. Keep absent defaults,
  the zero-detail sentinel and runtime fail-open. Announcement environment values
  must be positive; only the direct announcement-hook constructor treats zero as
  its default of 10. Update in-repo
  callers to validate synchronously before orchestrator startup. Reject non-finite
  weights/temperatures and overflowing calculated summary allowances.
- Classify declared prior-phase template inclusions as Debug rather than missing
  dependencies. Preserve legacy inclusion totals/metadata, with additive bounded
  reason telemetry; no scheduling or parameter-binding changes.
- Accept one complete summary array with plain prose before and/or after it.
  Preserve exact decoded payloads and truncated-prefix fallback behavior;
  reject additional JSON values/envelopes or ambiguous multiple arrays.

### Summary, memory-view and skill-selection improvements

- Separate event-summary generation allowance from stored prose: configurable
  1024-byte per-summary default, rune-safe shortening marker and observable
  counts, with raw debug completions and exact entity IDs preserved.

- Compact request-local memory event provenance into self-contained tuples with
  a budgeted field legend and `event-detail/v1` view identity in hook effects and
  traces. Preserve exact admitted IDs, named entity fields, section priority and
  explicit omissions. Stored events and full compactor inputs remain unchanged.

- Align both built-in event-summary prompts on the complete entity output
  contract. Add retained-summary entity-field coverage to existing hook effects,
  trace events and completion logs without redefining batch/provider success,
  changing the public summarizer interface or adding model calls. Preserve exact
  IDs, valid summary text and custom/explicit-metadata extraction paths.

- Preserve complete, individually valid event summaries before a truncated JSON
  suffix, with call-local complete/partial/fallback coverage and one recorder-owned
  fallback pass. Keep custom summarizers and model-derived entity extraction.
  Reject empty compactor digests without replacing usable cached history.
- Add semantic summary outcome telemetry alongside legacy provider-completion
  metrics; carry coverage in existing hook effects and use the actual trace ID
  (not a request ID) for newly recorded memory events.

- Bound derived memory views with complete encoded records, UTF-8-safe labelled
  shortening and admission/omission counts. Preserve stored events and existing
  section priority; deduplicate admitted rather than fetched records. Wire the
  existing enrichment-budget environment setting into raw and compacted hook
  construction, validate overflow and advance the digest view to v3.
- Count optional resource omission once per operation while preserving timeout
  diagnostics for each attempt and terminal parent cancellation.
- Render compact declared return fields in both full and tier-selected planner
  catalogs, including capabilities without a top-level return type.
- Version historical memory digest views, retain event/request origins and
  times, and distinguish request-start history from current execution evidence.
- Resource selector v2 accepts complete outer JSON fences and supports a
  configurable per-attempt deadline with one timeout retry within two attempts.
  Activation remains unchanged; timeout omissions and parent cancellation are
  distinct, and zero retains the prior parent-inherited deadline.

## v0.5.0

See the [root changelog](../CHANGELOG.md)
for the cross-module release summary.

### Backend composition and Redis/Valkey topology

- Add `OrchestrationBackends` composition, per-role overrides, capability
  validation, and reusable provider conformance tests. Application code can
  consume narrow contracts without constructing Redis adapters directly.
- Add standalone, Sentinel, and cluster Redis/Valkey support through DB-0
  keyspaces, same-slot atomic updates, and explicit client ownership. Global
  debug indexes are advisory rather than part of a cross-slot
  transaction. The v0.4.0 numbered-DB layout is not migrated automatically.

### Required planning hooks and observability

- Enforce `core.RequiredAfterPlanningHook` before HITL or execution. Reject
  stage signature drift, multiple required planning hooks, a required hook
  followed by another planning hook, and nil or typed-nil registrations.
  Optional hooks retain their documented fail-open behavior.
- Record pipeline invocations, decisions, application-reported effects, and
  failure evidence, including runs that fail before any tool step executes.
  Preserve panic propagation after recording the failed invocation.
- Preserve related execution retention and LLM-debug retention floors. Missing
  historical parents are treated as unavailable evidence, not fabricated
  lineage. Stored final application responses remain distinct from raw LLM
  synthesis output.

### Framework-owned HITL resume

- Add `ResumeCoordinator`, `ResumeExecutor`, and the optional
  `CheckpointResumePersistence` backend capability. Claim, renewal, release,
  and finalization use backend-time fencing and preserve checkpoint retention.
- Add `resuming` and `continued` states, durable successor provenance, guarded
  deletion, and expected-status CAS for human decisions. Keep the existing
  Redis/Valkey DB-0 keyspace, borrowed clients, and expiry callback behavior.
- Add result-bearing sync and streaming processing methods so adapters return
  the actual terminal result, including synthesis, hook, and callback failures.
- Remove the controller's unimplemented resume method. `NewHITLHandler` now
  returns an error and accepts `WithHITLResumer`; without it, no resume route is
  registered. Current workspace callers migrate directly, without compatibility
  wrappers, under the development-stage no-external-users policy.
- Default commands support approve/reject/abort only. Public checkpoint DTOs
  omit internal attempt ownership while preserving application payloads.
- Add correlated lifecycle events/logs and bounded resume counters. Status
  transition metrics count only successfully persisted command transitions.

### Canonical Redis adapter API cleanup

- Remove role-specific DB routing and `redisprovider.WithRoleDatabase`.
  Owned presets share one DB-0 client; `WithDatabase` accepts only zero.
- Reject non-empty obsolete role DB settings and Redis raw-prefix settings
  before constructing selected backends. Settings belonging only to unselected
  roles remain outside effective configuration. Errors never echo values.
- Remove `WithDebugRedisDB`, `WithExecutionDebugRedisDB`,
  `WithCheckpointRedisDB`, and `WithCommandStoreRedisDB`.
- Replace `WithDebugKeyPrefix`, `WithExecutionDebugKeyPrefix`,
  `WithCheckpointKeyPrefix`, and `WithCommandStoreKeyPrefix` with their typed
  `With*Keyspace` counterparts. Both HITL stores require the same keyspace and
  agent scope. Replace `RedisScheduleStoreConfig.KeyPrefix` with `Keyspace`.
- Remove `redisprovider.WithSkillStoreKeyPrefix` and its legacy `:{store}`
  branch. Use `WithSkillStoreKeyspace`; presets continue to use
  `WithNamespace`. Canonical skill keys and published versions are unchanged.
- Keep `ExecutionStoreConfig.KeyPrefix` for generic StorageProvider keys.
  A custom prefix is rejected by the direct Redis adapter unless an explicit
  `WithExecutionDebugKeyspace` overrides it.
- Remove deprecated `NewRedisExecutionStore` and `WithExecutionRedisURL`,
  `WithExecutionRedisDB`, `WithExecutionLogger`, `WithExecutionKeyPrefix`,
  `WithExecutionTTL`, and `WithExecutionErrorTTL`; use
  `NewRedisExecutionDebugStore` and the `WithExecutionDebug*` options.
- Remove the execution-ID-only `LegacyRedisStateStore`, its constructors and
  conformance fixture. `StateStore` now aliases `WorkflowStateStore`;
  `UpdateStepExecution` and `GetExecution` require `workflowID`.
- Remove `LLMDebugConfig.RedisDB` and provider deprecation diagnostics.
  Direct owning constructors use the canonical core DB-0 factory; injected
  clients remain open when adapters close.

The execution and LLM-debug stores already avoid automatic URL-constructor
redaction and use bounded diagnostics. This cleanup preserves that contract,
request-local atomicity, and separate advisory debug indexes.
The subsequent API cleanup does not change the new canonical key schema
further. This is not compatibility with the v0.4.0 numbered-DB layout. See the
[root changelog](../CHANGELOG.md) for the development-only cleanup authorization.
