// Registry-completeness test for static/js/llm-types.js.
//
// Run:    node tests/llm-types.test.mjs
// Exit:   0 on success, 1 on missing types or assertion failure.
//
// Located outside static/ so the //go:embed directive in main.go does NOT
// pack this test file into the production binary. main.go serves only the
// embedded static tree — keeping tests under tests/ means they are
// runnable locally / in CI but never reach end users.
//
// No test framework is wired up for this app, so this file is intentionally
// a self-contained ESM script (.mjs so Node uses module loading without a
// package.json). Wire it into CI as `node ...`. If a test framework
// (Jest / Vitest / Node's built-in `node:test`) is added later, the
// EXPECTED_TYPES list and assertions below can move into a regular test
// file with no logic changes.
//
// ---------------------------------------------------------------------------
//
// Source of truth: every production `LLMInteraction{Type: "..."}` literal
// under orchestration/ (excluding *_test.go), PLUS dynamic types not
// emitted as literals. Known dynamic sources today:
//   * `agent_llm_call` — set from `record.CallType` at
//     orchestration/llm_call_recorder_adapter.go:30
//   * `user_memory_reconciliation_skip` and
//     `user_memory_reconciliation_batch_item` — set via the `recType`
//     local at orchestration/user_memory_extraction.go:507-514
//     (the literal-emitted siblings `user_memory_reconciliation` and
//     `user_memory_reconciliation_batch` ARE found by grep, but these
//     two are not)
//
// A useful cross-reference is the doc-comment block at
// orchestration/llm_debug_store.go:108-109 that already lists all four
// reconciliation types in one place — stale-able, but a starting point.
//
// At time of writing the literal sites span ~14 files: orchestrator.go,
// tiered_capability_provider.go, activity_compactor.go, result_distiller.go,
// conversation_compactor.go, user_memory_hooks.go, contextual_re_resolver.go,
// synthesizer.go, plan_refinement.go, micro_resolver.go, error_analyzer.go,
// conversation_history_processor.go, user_memory_extraction.go,
// event_summarizer.go.
//
// Refreshing EXPECTED_TYPES is currently a manual review — there is no
// reliable one-liner. The shape is `LLMInteraction{... Type: "..." ...}`
// across multiple lines, while a flat `grep 'Type:'` matches any Go struct
// field named `Type` (Kubernetes type strings, JSON-schema descriptors,
// metric kinds like counter/gauge/histogram, etc. all leak through). The
// rough-discovery command below is best treated as a starting candidate
// list to prune by hand against the emitter files above:
//
//     grep -rE 'Type:\s+"[a-z_]+"' orchestration \
//         --include='*.go' --exclude='*_test.go' \
//         | sed -E 's,.*"([^"]+)".*,\1,' | sort -u
//
// (Note: --exclude='*_test.go' is the correct flag — `grep -h ... | grep
// -v _test.go` would NOT actually filter test files because -h suppresses
// the filename in each match. The sed expression here uses comma
// delimiters to avoid the `*/` byte sequence that would otherwise
// terminate a /* ... */ block comment if this file were rewritten with
// JSDoc-style comments.)
//
// A reliable refresh would be a small AST script that walks composite
// literals of type LLMInteraction and collects the Type field — but even
// that catches only literal `Type: "..."` and would miss the
// `Type: recType` / `Type: record.CallType` cases. The dynamic sources
// have to be enumerated by hand alongside any AST script. Adding that
// tooling is a follow-up; until then, treat EXPECTED_TYPES as
// authoritative and update it deliberately.

import { LLM_TYPES } from '../static/js/llm-types.js';

const EXPECTED_TYPES = [
    // Planning / orchestration
    'plan_generation', 'plan_regeneration_fallback',
    'continuation_plan_generation', 'continuation_plan_regeneration',
    'tiered_selection', 'correction', 'hallucination_detection',
    'plan_refinement', 'semantic_retry',
    // Resolution / execution
    'micro_resolution', 'schema_mapping', 'agent_llm_call',
    'error_analysis', 'result_distillation',
    // Synthesis
    'synthesis', 'synthesis_streaming',
    // Memory / conversation / hooks
    'conversation_history_prepare', 'conversation_history_compaction',
    'activity_compaction', 'activity_compaction_incremental',
    'event_summarization',
    'user_memory_recall_identity', 'user_memory_recall_summary',
    'user_memory_recall_stable_namespace', 'user_memory_recall_query',
    'user_memory_recall_universal', 'user_memory_similarity_search',
    'user_memory_enrichment_injected', 'user_memory_extraction',
    'user_memory_summary', 'user_memory_remember',
    'user_memory_summary_remember', 'user_memory_persistence_policy',
    'user_memory_summary_persistence_policy',
    'user_memory_reconciliation',           // literal at user_memory_extraction.go:1027
    'user_memory_reconciliation_skip',      // dynamic via recType at user_memory_extraction.go:509
    'user_memory_reconciliation_batch',     // literal at user_memory_extraction.go:933,969,994
    'user_memory_reconciliation_batch_item',// dynamic via recType at user_memory_extraction.go:511
];

// ---------------------------------------------------------------------------
// Tiny test harness — no external deps.
// ---------------------------------------------------------------------------
let failed = 0;
function test(name, fn) {
    try {
        fn();
        console.log(`  ✓ ${name}`);
    } catch (e) {
        failed += 1;
        console.error(`  ✗ ${name}`);
        console.error(`    ${e.message}`);
    }
}
function assertDeepEqual(actual, expected, msg) {
    const a = JSON.stringify(actual);
    const e = JSON.stringify(expected);
    if (a !== e) throw new Error(`${msg || 'expected equality'} — got ${a}, want ${e}`);
}

console.log('llm-types.test.mjs');

test('every emitted orchestration type is in LLM_TYPES', () => {
    const missing = EXPECTED_TYPES.filter(t => !(t in LLM_TYPES));
    assertDeepEqual(
        missing,
        [],
        `Registry is missing ${missing.length} type(s) the orchestrator can emit`,
    );
});

test('every entry has the required fields (role, label, icon, rgb, accent)', () => {
    const required = ['role', 'label', 'icon', 'rgb', 'accent'];
    const broken = [];
    for (const [type, cfg] of Object.entries(LLM_TYPES)) {
        for (const field of required) {
            if (!(field in cfg)) broken.push(`${type}: missing ${field}`);
        }
    }
    assertDeepEqual(broken, [], 'Registry entries with missing required fields');
});

test('every retry type also has a list-palette override', () => {
    // Visual retry treatment in LLM Debug + CSS badge depends on listRgb.
    // Without it, retry types fall back to their planning-purple primary
    // palette and lose the warning-orange affordance.
    const broken = [];
    for (const [type, cfg] of Object.entries(LLM_TYPES)) {
        if (cfg.isRetry && !cfg.listRgb) broken.push(type);
    }
    assertDeepEqual(broken, [], 'isRetry types missing listRgb');
});

test('every closesPhase type also defines a phase() function', () => {
    const broken = [];
    for (const [type, cfg] of Object.entries(LLM_TYPES)) {
        if (cfg.closesPhase && typeof cfg.phase !== 'function') broken.push(type);
    }
    assertDeepEqual(broken, [], 'closesPhase types missing phase() function');
});

test('step-scoped types are not classified as planning', () => {
    // The step-specific filter at static/js/views/dag.js:1490 renders these
    // types as step-attached LLM nodes when they carry a step_id. If any of
    // them are also classified `role: 'planning'`, isPlanningType() would
    // pull them into the planner column too and produce duplicate nodes.
    //
    // (`micro_resolution` is special-cased in the planner-column filter
    // with `!i.step_id`, so it can stay role='resolution' AND survive the
    // dual-rendering scenario. The other entries below have no such
    // step_id guard in the planner-column filter — they MUST not be
    // classified as planning if they ever carry a step_id.)
    const stepScopedTypes = [
        'micro_resolution', 'semantic_retry',
        'error_analysis', 'result_distillation',
    ];
    const broken = stepScopedTypes.filter(t => {
        const cfg = LLM_TYPES[t];
        return cfg && cfg.role === 'planning';
    });
    assertDeepEqual(broken, [], 'step-scoped types incorrectly marked planning');
});

if (failed > 0) {
    console.error(`\n${failed} test(s) failed`);
    process.exit(1);
}
console.log('\nAll tests passed.');
