// DAG renderer boundary tests.
//
// Run: node tests/dag-graph.test.mjs

import {
    assignAfterPlanningHooksToPhases,
    executionDisplayStepNumber,
    filterInteractionsWithRenderedParent,
    filterRenderableEdges,
    hasVisibleRelationOwner,
    hitlContinuationStepOffset,
    isPostExecutionHook,
    isPreExecutionHook,
    pipelineHookHasDetailedInteractions,
} from '../static/js/utils/dag-graph.js';

let failed = 0;

function test(name, fn) {
    try {
        fn();
        console.log(`  ✓ ${name}`);
    } catch (error) {
        failed += 1;
        console.error(`  ✗ ${name}`);
        console.error(`    ${error.message}`);
    }
}

function assertDeepEqual(actual, expected, message) {
    const actualJSON = JSON.stringify(actual);
    const expectedJSON = JSON.stringify(expected);
    if (actualJSON !== expectedJSON) {
        throw new Error(`${message || 'expected equality'} — got ${actualJSON}, want ${expectedJSON}`);
    }
}

console.log('dag-graph.test.mjs');

test('omits a HITL resume provenance edge whose parent step is outside the graph', () => {
    const nodes = [
        { data: { id: 'step-10-governed-execute' } },
        { data: { id: 'step-10-governed-verify' } },
        { data: { id: 'step-10-governed-notify' } },
    ];
    const validDependency = {
        data: {
            source: 'step-10-governed-execute',
            target: 'step-10-governed-verify',
            edgeType: 'dependency',
        },
    };
    const crossExecutionProvenance = {
        data: {
            source: 'step-10',
            target: 'step-10-governed-execute',
            edgeType: 'implicit_dependency',
        },
    };

    const result = filterRenderableEdges(
        nodes,
        [validDependency, crossExecutionProvenance]
    );

    assertDeepEqual(result.renderable, [validDependency]);
    assertDeepEqual(result.omitted, [crossExecutionProvenance]);
});

test('preserves edges when both endpoints are rendered', () => {
    const nodes = [
        { data: { id: 'a' } },
        { data: { id: 'b' } },
    ];
    const edge = { data: { source: 'a', target: 'b' } };

    const result = filterRenderableEdges(nodes, [edge]);

    assertDeepEqual(result.renderable, [edge]);
    assertDeepEqual(result.omitted, []);
});

test('omits carried step-scoped LLM calls whose parent belongs to another execution', () => {
    const interactions = [
        { type: 'micro_resolution', step_id: 'step-10-governed-notify' },
        { type: 'result_distillation', step_id: 'step-4' },
        { type: 'synthesis' },
    ];

    const result = filterInteractionsWithRenderedParent(
        interactions,
        new Set([
            'step-10-governed-execute',
            'step-10-governed-verify',
            'step-10-governed-notify',
        ])
    );

    assertDeepEqual(result, [interactions[0]]);
});

test('renders a relationship branch only when the owner is visible', () => {
    assertDeepEqual(hasVisibleRelationOwner({}, false), true);
    assertDeepEqual(
        hasVisibleRelationOwner({ relation_status: 'owner_unavailable' }, true),
        false
    );
    assertDeepEqual(
        hasVisibleRelationOwner({ relation_status: 'owner_unknown' }, true),
        false
    );
    assertDeepEqual(hasVisibleRelationOwner({}, true), false);
});

test('uses unique numbers when normal-execution step IDs share a numeric prefix', () => {
    const steps = [
        { step_id: 'step-10-governed-execute' },
        { step_id: 'step-10-governed-verify' },
        { step_id: 'step-10-governed-notify' },
    ];

    assertDeepEqual(
        steps.map((_, index) => executionDisplayStepNumber(index, {})),
        [1, 2, 3]
    );
    assertDeepEqual(executionDisplayStepNumber(-1, {}), null);
});

test('continues HITL resume numbering after distinct completed steps', () => {
    const execution = {
        hitl_lifecycle: {
            is_resume: true,
            current_checkpoint: {
                completed_steps: [
                    { step_id: 'step-1' },
                    { step_id: 'step-2' },
                    { step_id: 'step-2' },
                    { step_id: 'step-3' },
                ],
            },
        },
    };

    assertDeepEqual(hitlContinuationStepOffset(execution), 3);
    assertDeepEqual(
        [0, 1, 2].map(index => executionDisplayStepNumber(index, execution)),
        [4, 5, 6]
    );
});

test('classifies trace-backed hooks into pre- and post-execution stages', () => {
    assertDeepEqual(isPreExecutionHook({ phase: 'before_planning' }), true);
    assertDeepEqual(isPreExecutionHook({ phase: 'after_planning' }), true);
    assertDeepEqual(isPostExecutionHook({ phase: 'after_execution' }), true);
    assertDeepEqual(isPostExecutionHook({ phase: 'after_synthesis' }), true);
    assertDeepEqual(isPostExecutionHook({ phase: 'after_planning' }), false);
});

test('suppresses only trace wrappers with richer Full Flow interaction nodes', () => {
    const interactions = [
        { type: 'user_memory_recall_identity' },
        { type: 'user_memory_extraction' },
    ];

    assertDeepEqual(
        pipelineHookHasDetailedInteractions(
            { hook_name: 'user-memory-enrichment' },
            interactions
        ),
        true
    );
    assertDeepEqual(
        pipelineHookHasDetailedInteractions(
            { hook_name: 'governed-order-response' },
            interactions
        ),
        false
    );
});

test('places AfterPlanning spans after the phase whose plan preceded them', () => {
    const phases = [
        { created_at: '2026-08-27T15:34:36.900Z' },
        { created_at: '2026-08-27T15:34:47.500Z' },
    ];
    const hooks = [
        { hook_name: 'guard', started_at: '2026-08-27T15:34:47.600Z' },
        { hook_name: 'guard', started_at: '2026-08-27T15:34:36.950Z' },
        { hook_name: 'unplaced' },
    ];

    assertDeepEqual(
        assignAfterPlanningHooksToPhases(hooks, phases).map(items =>
            items.map(item => item.started_at)
        ),
        [
            ['2026-08-27T15:34:36.950Z'],
            ['2026-08-27T15:34:47.600Z'],
        ]
    );
});

if (failed > 0) {
    console.error(`\n${failed} test(s) failed`);
    process.exit(1);
}

console.log('\nAll tests passed.');
