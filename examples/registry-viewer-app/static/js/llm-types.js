/**
 * LLM-interaction type registry — single source of truth for type metadata.
 *
 * Replaces the previously-scattered enumerations in dag.js (planningTypes,
 * llmTypeLabels, llmTypeConfig), llm-debug.js (llmTypeLabels, _ic), and
 * components/card.js (defaultTypeConfig, defaultTypeLabels).
 *
 * Adding a new orchestration LLM call type? Add ONE entry below AND extend
 * EXPECTED_TYPES in tests/llm-types.test.mjs.
 *
 * ### Field reference
 *
 * Required:
 *   role        : 'planning' | 'resolution' | 'synthesis' | 'execution' | 'memory' | 'hook'
 *   label       : short display label (DAG/Cytoscape/Card view; also the LLM Debug
 *                 label unless `listLabel` overrides)
 *   icon        : single-glyph icon
 *   rgb         : "R, G, B" (DAG / Cytoscape / Card "structural" palette)
 *   accent      : CSS color string for borders/text (DAG palette)
 *
 * Optional:
 *   listLabel   : LLM Debug list view label (overrides `label` when present)
 *   listRgb     : LLM Debug list view + CSS badge palette override —
 *                 used for both the warning-orange treatment retry types
 *                 get AND for per-type list palettes that historically
 *                 diverged from the DAG palette (e.g. `plan_generation`
 *                 is purple in the DAG but green in the list view).
 *                 Falls through to `rgb` when absent.
 *   listAccent  : same rule as listRgb, for borders/text
 *   closesPhase : if true, this type ends a phase group in the DAG planner-
 *                 column layout (see groupLLMCallsByPhase in views/dag.js)
 *   phase       : (call) => phaseNumber — used when closesPhase is true to
 *                 decide which phase the call belongs to
 *   isRetry     : if true, the LLM Debug retry warning callout is shown
 *
 * ### Why two palettes
 *
 * Two palettes coexist by historical design and are preserved by this
 * registry:
 *   - "Structural" palette (rgb/accent) — used by the DAG / Cytoscape view
 *     and the standalone card component. Groups planning operations under
 *     a purple/violet family so structural relationships read clearly.
 *   - "List" palette (listRgb/listAccent) — used by the LLM Debug list view
 *     and the corresponding CSS badge. Spreads colors across a wider range
 *     so each row is visually distinct when scrolling. Retry types
 *     specifically use orange in this view as a warning treatment.
 *
 * Consolidating the two palettes is a future design decision, not a
 * migration goal. Until then, this registry preserves both.
 */

/* eslint-disable max-len */
export const LLM_TYPES = {
    // -----------------------------------------------------------------------
    // Planning / orchestration
    // -----------------------------------------------------------------------
    plan_generation: {
        role: 'planning', label: 'Planning', icon: '📋',
        rgb: '218, 143, 255', accent: 'var(--accent-purple)',
        listRgb: '50, 215, 75', listAccent: 'var(--accent-green)',
        closesPhase: true, phase: () => 1,
    },
    plan_regeneration_fallback: {
        role: 'planning', label: 'Plan Regen ⚠️', icon: '🔄',
        rgb: '218, 143, 255', accent: 'var(--accent-purple)',
        listRgb: '255, 140, 50', listAccent: '#ff8c32',
        closesPhase: true, phase: () => 1, isRetry: true,
        listLabel: 'Plan Regeneration (retry)',
    },
    continuation_plan_generation: {
        role: 'planning', label: 'Phase Plan', icon: '📋',
        rgb: '190, 120, 255', accent: '#be78ff',
        listRgb: '50, 215, 75', listAccent: 'var(--accent-green)',
        listLabel: 'Continuation Plan',
        closesPhase: true, phase: c => c.phase_number || 1,
    },
    continuation_plan_regeneration: {
        role: 'planning', label: 'Plan Regen ⚠️', icon: '🔄',
        rgb: '190, 120, 255', accent: '#be78ff',
        listRgb: '255, 140, 50', listAccent: '#ff8c32',
        listLabel: 'Continuation Plan Retry',
        closesPhase: true, phase: c => c.phase_number || 1, isRetry: true,
    },
    tiered_selection: {
        role: 'planning', label: 'Tier Select', icon: '🎯',
        rgb: '140, 150, 255', accent: '#a0a8ff',
        listRgb: '255, 179, 64', listAccent: 'var(--accent-orange)',
    },
    correction: {
        role: 'planning', label: 'Plan Fix', icon: '🔧',
        rgb: '255, 179, 64', accent: 'var(--accent-orange)',
    },
    hallucination_detection: {
        role: 'planning', label: 'Hallucination', icon: '⚠️',
        listLabel: 'Hallucination Check',
        rgb: '255, 80, 80', accent: '#ff5050',
    },
    plan_refinement: {
        role: 'planning', label: 'Plan Refinement', icon: '✏️',
        rgb: '218, 143, 255', accent: 'var(--accent-purple)',
    },
    semantic_retry: {
        role: 'resolution', label: 'Semantic Retry', icon: '🔄',
        rgb: '255, 110, 180', accent: 'var(--accent-pink)',
        // role MUST be 'resolution' (not 'planning'): semantic_retry is
        // emitted with a StepID by contextual_re_resolver.go and rendered
        // as a step-attached node by the step-specific filter at
        // views/dag.js:1490. Marking it 'planning' would also pull it into
        // the planner column via isPlanningType(), causing every retry to
        // render twice (planner column + step node).
        //
        // NOT marked isRetry: this flag drives the LLM Debug "Regenerations"
        // header count and the orange warning callout, both of which today
        // only trigger for plan-level recoveries (plan_regeneration_fallback
        // and continuation_plan_regeneration). semantic_retry is a different
        // concept (LLM output parse retry) and stays visually neutral.
    },

    // -----------------------------------------------------------------------
    // Resolution / execution
    // -----------------------------------------------------------------------
    micro_resolution: {
        role: 'resolution', label: 'Resolution', icon: '🔍',
        rgb: '100, 210, 255', accent: 'var(--accent-teal)',
    },
    schema_mapping: {
        role: 'resolution', label: 'Schema Mapping', icon: '🗺️',
        rgb: '0, 200, 170', accent: '#00c8aa',
    },
    agent_llm_call: {
        role: 'execution', label: 'Agent LLM Call', icon: '🔧',
        rgb: '160, 100, 240', accent: '#a064f0',
    },
    error_analysis: {
        role: 'execution', label: 'Error Analysis', icon: '⚠️',
        rgb: '255, 107, 107', accent: 'var(--accent-red)',
    },
    result_distillation: {
        role: 'execution', label: 'Result Distillation', icon: '🔬',
        rgb: '130, 90, 220', accent: '#8250dc',
    },

    // -----------------------------------------------------------------------
    // Synthesis
    // -----------------------------------------------------------------------
    synthesis: {
        role: 'synthesis', label: 'Synthesis', icon: '🔗',
        rgb: '52, 211, 153', accent: '#34d399',
        listRgb: '218, 143, 255', listAccent: 'var(--accent-purple)',
    },
    synthesis_streaming: {
        role: 'synthesis', label: 'Synthesis', icon: '🔗',
        listLabel: 'Streaming Synthesis',
        rgb: '52, 211, 153', accent: '#34d399',
        listRgb: '218, 143, 255', listAccent: 'var(--accent-purple)',
    },

    // -----------------------------------------------------------------------
    // Memory / conversation / hooks
    // -----------------------------------------------------------------------
    conversation_history_prepare: {
        role: 'hook', label: 'History Prepare', icon: '🛡️',
        listLabel: 'History Preparation',
        rgb: '108, 194, 255', accent: '#6cc2ff',
    },
    conversation_history_compaction: {
        role: 'hook', label: 'History Compact', icon: '🗜️',
        listLabel: 'History Compaction',
        rgb: '108, 194, 255', accent: '#6cc2ff',
    },
    activity_compaction: {
        role: 'hook', label: 'Activity Compact', icon: '📝',
        rgb: '240, 160, 48', accent: '#f0a030',
    },
    activity_compaction_incremental: {
        role: 'hook', label: 'Memory Compact', icon: '📝',
        rgb: '240, 160, 48', accent: '#f0a030',
    },
    event_summarization: {
        role: 'hook', label: 'Event Summary', icon: '📝',
        rgb: '240, 160, 48', accent: '#f0a030',
    },
    user_memory_recall_identity: {
        role: 'memory', label: 'Recall Identity', icon: '↩',
        listLabel: 'Recall: identity facts',
        rgb: '200, 140, 48', accent: '#c88c30',
    },
    user_memory_recall_summary: {
        role: 'memory', label: 'Recall Summary', icon: '↩',
        listLabel: 'Recall: session summaries',
        rgb: '200, 140, 48', accent: '#c88c30',
    },
    user_memory_recall_stable_namespace: {
        role: 'memory', label: 'Recall Stable', icon: '↩',
        listLabel: 'Recall: durable profile',
        rgb: '200, 140, 48', accent: '#c88c30',
    },
    user_memory_recall_query: {
        role: 'memory', label: 'Recall Query', icon: '↩',
        listLabel: 'Recall: query-relevant',
        rgb: '200, 140, 48', accent: '#c88c30',
    },
    user_memory_recall_universal: {
        role: 'memory', label: 'Recall Universal', icon: '↩',
        listLabel: 'Recall: universal facts',
        rgb: '200, 140, 48', accent: '#c88c30',
    },
    user_memory_similarity_search: {
        role: 'memory', label: 'Similarity Search', icon: '◎',
        listLabel: 'Similarity search',
        rgb: '200, 140, 48', accent: '#c88c30',
    },
    user_memory_enrichment_injected: {
        role: 'memory', label: 'Profile Injected', icon: '✓',
        listLabel: 'Prompt enrichment',
        rgb: '180, 140, 60', accent: '#b48c3c',
    },
    user_memory_extraction: {
        role: 'memory', label: 'Extract Facts', icon: '✦',
        listLabel: 'Fact extraction',
        rgb: '240, 160, 48', accent: '#f0a030',
    },
    user_memory_embed_candidate: {
        role: 'memory', label: 'Embed Candidate', icon: '◈',
        listLabel: 'Embed candidate',
        rgb: '200, 140, 48', accent: '#c88c30',
    },
    user_memory_summary: {
        role: 'memory', label: 'Session Summary', icon: '≡',
        listLabel: 'Summary generation',
        rgb: '240, 160, 48', accent: '#f0a030',
    },
    user_memory_remember: {
        role: 'memory', label: 'Store Fact', icon: '✎',
        listLabel: 'Remember',
        rgb: '200, 140, 48', accent: '#c88c30',
    },
    user_memory_summary_remember: {
        role: 'memory', label: 'Store Summary', icon: '✎',
        listLabel: 'Persist summary',
        rgb: '200, 140, 48', accent: '#c88c30',
    },
    user_memory_persistence_policy: {
        role: 'memory', label: 'Persistence Policy', icon: '⚖',
        listLabel: 'Persistence policy',
        rgb: '180, 140, 60', accent: '#b48c3c',
    },
    user_memory_summary_persistence_policy: {
        role: 'memory', label: 'Persistence Policy', icon: '⚖',
        listLabel: 'Persistence policy: summary',
        rgb: '180, 140, 60', accent: '#b48c3c',
    },
    user_memory_reconciliation: {
        role: 'memory', label: 'Reconcile', icon: '⇄',
        listLabel: 'Reconciliation',
        rgb: '240, 160, 48', accent: '#f0a030',
    },
    user_memory_reconciliation_skip: {
        role: 'memory', label: 'Reconcile (skip)', icon: '⇄',
        listLabel: 'Reconciliation (skipped)',
        rgb: '180, 140, 60', accent: '#b48c3c',
    },
    user_memory_reconciliation_batch: {
        role: 'memory', label: 'Reconcile Batch', icon: '⇄',
        listLabel: 'Reconciliation (batch)',
        rgb: '240, 160, 48', accent: '#f0a030',
    },
    user_memory_reconciliation_batch_item: {
        role: 'memory', label: 'Reconcile Item', icon: '·',
        listLabel: 'Reconciliation (batch item)',
        rgb: '180, 140, 60', accent: '#b48c3c',
    },
};
/* eslint-enable max-len */

// HSL-derived deterministic-hash fallback for unknown types. Preserves the
// algorithm previously inlined in dag.js's getLLMCardConfig — visible with
// default styling, but NOT treated as planning. Tolerates missing / empty /
// non-string `type` so malformed records do not throw during rendering.
function fallback(type) {
    const t = (typeof type === 'string' && type.length > 0) ? type : 'unknown';
    let hash = 0;
    for (let i = 0; i < t.length; i++) {
        hash = ((hash << 5) - hash + t.charCodeAt(i)) | 0;
    }
    const hue = ((hash % 360) + 360) % 360;
    const r = Math.round(150 + 80 * Math.cos(hue * Math.PI / 180));
    const g = Math.round(150 + 80 * Math.cos((hue - 120) * Math.PI / 180));
    const b = Math.round(150 + 80 * Math.cos((hue - 240) * Math.PI / 180));
    const rgb = `${r}, ${g}, ${b}`;
    const hex = '#' + [r, g, b].map(c => c.toString(16).padStart(2, '0')).join('');
    return {
        role: 'unknown',
        label: t.replace(/_/g, ' '),
        icon: '💬',
        rgb, accent: hex,
    };
}

/** Returns the registry entry for `type`, or a hash-derived fallback. */
export function getLLMType(type) {
    return LLM_TYPES[type] || fallback(type);
}

/** True if the type is a planning-stage LLM call. */
export function isPlanningType(type) {
    return getLLMType(type).role === 'planning';
}

/** True if this type closes a phase group in the DAG planner column. */
export function isPhaseClosingType(type) {
    return !!getLLMType(type).closesPhase;
}

/**
 * Returns the phase number a phase-closing call belongs to.
 * `call` is the full LLMInteraction record (so phase functions can read
 * `call.phase_number`, etc.).
 */
export function getPhaseNumber(call) {
    const cfg = getLLMType(call.type);
    return cfg.phase ? cfg.phase(call) : (call.phase_number || 1);
}

/** True if this type warrants a retry warning callout in the LLM Debug list. */
export function isRetryType(type) {
    return !!getLLMType(type).isRetry;
}

/**
 * Returns the LLM Debug list view label for `type`. Falls back to the primary
 * `label` when no `listLabel` override is set.
 */
export function getListLabel(type) {
    const cfg = getLLMType(type);
    return cfg.listLabel || cfg.label;
}

/**
 * Returns the LLM Debug + CSS badge palette for `type`.
 *
 * Falls through to the primary `rgb`/`accent` palette when no list override
 * is set. Use this anywhere you would set `--badge-rgb` / `--badge-color`
 * inline on a `.type-badge` element, or anywhere reading the warning-orange
 * retry treatment for retry types.
 */
export function getListStyledColors(type) {
    const cfg = getLLMType(type);
    return {
        rgb:    cfg.listRgb    ?? cfg.rgb,
        accent: cfg.listAccent ?? cfg.accent,
    };
}
