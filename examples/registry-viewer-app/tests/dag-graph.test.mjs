// DAG renderer boundary tests.
//
// Run: node tests/dag-graph.test.mjs

import {
    assignAfterPlanningHooksToPhases,
    buildObservedPhases,
    executionDisplayStepNumber,
    filterInteractionsWithRenderedParent,
    filterRenderableEdges,
    hasVisibleRelationOwner,
    hitlContinuationStepOffset,
    isPostExecutionHook,
    isPreExecutionHook,
    pipelineHookHasDetailedInteractions,
    pipelineHookConsequence,
    projectPipelineHookWrappers,
    unplacedPipelineHooks,
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

test('classifies stored hooks into pre- and post-execution stages', () => {
    assertDeepEqual(isPreExecutionHook({ phase: 'before_planning' }), true);
    assertDeepEqual(isPreExecutionHook({ phase: 'after_planning' }), true);
    assertDeepEqual(isPostExecutionHook({ phase: 'after_execution' }), true);
    assertDeepEqual(isPostExecutionHook({ phase: 'after_synthesis' }), true);
    assertDeepEqual(isPostExecutionHook({ phase: 'after_planning' }), false);
});

test('suppresses only stored hook wrappers with richer Full Flow interaction nodes', () => {
    const interactions = [
        { type: 'user_memory_recall_identity' },
        { type: 'user_memory_extraction' },
    ];

    assertDeepEqual(
        pipelineHookHasDetailedInteractions(
            { hook_name: 'user-memory-enrichment', phase: 'before_planning' },
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

test('stored decisions distinguish failure, continuation, skipped callbacks and early answers', () => {
    const cases = [
        [{ status: 'failed', decision: { action: 'continue', failure_policy: 'fail_open', reason: 'hook_error' } }, 'Hook failed · request continued'],
        [{ status: 'failed', decision: { action: 'terminate', failure_policy: 'fail_closed', reason: 'hook_error' } }, 'Hook failed · request stopped'],
        [{ status: 'skipped', decision: { action: 'terminate', reason: 'clone_failed' } }, 'Hook could not run · request stopped'],
        [{ status: 'skipped', decision: { action: 'continue', reason: 'clone_failed' } }, 'Hook could not run · request continued'],
        [{ status: 'failed', decision: { action: 'continue', reason: 'invalid_plan' } }, 'Plan change rejected · request continued'],
        [{ decision: { action: 'continue', reason: 'cache_dimension_mismatch' } }, 'Cached answer not used · request continued'],
        [{ decision: { action: 'short_circuit', reason: 'authoritative' } }, 'Early answer selected · planning skipped'],
        [{ status: 'succeeded', decision: { action: 'continue', reason: 'completed' } }, 'Request continued'],
        [{ decision: { action: 'propagate_panic', reason: 'panic' } }, 'Panic propagated · request stopped'],
        [{ status: 'failed', effects: [{ status: 'succeeded' }] }, 'Pipeline consequence not recorded'],
        [{ status: 'succeeded', effects: [{ status: 'failed' }] }, 'Pipeline consequence not recorded'],
        [{ decision: { action: 'future_action' } }, 'Pipeline consequence not recognized'],
        [{ decision: { action: 'constructor' } }, 'Pipeline consequence not recognized'],
    ];
    cases.forEach(([hook, expected]) => assertDeepEqual(pipelineHookConsequence(hook).summary, expected));
});

test('grouped hook projection preserves exact decisions and does not deduplicate repeated names', () => {
    const hook = { hook_name: 'user-memory-extraction', phase: 'after_synthesis', sequence: 1, status: 'failed', decision: { action: 'terminate', reason: 'test' }, effects: [{ data: { exact: '<application value>' } }] };
    const inner = [{ type: 'user_memory_extraction', success: true }];
    assertDeepEqual(projectPipelineHookWrappers([hook], inner), { standalone: [], wrappers: { user_memory_after: [hook] } });
    const duplicate = { ...hook, sequence: 2 };
    assertDeepEqual(projectPipelineHookWrappers([hook, duplicate], inner), { standalone: [hook, duplicate], wrappers: {} });
    assertDeepEqual(projectPipelineHookWrappers([hook], []), { standalone: [hook], wrappers: {} });
    const wrongPhase = { ...hook, phase: 'after_planning', plan_phase: 2 };
    assertDeepEqual(projectPipelineHookWrappers([wrongPhase], inner), { standalone: [wrongPhase], wrappers: {} });
    const operationNotWrapper = { hook_name: 'memory-record', phase: 'after_execution' };
    assertDeepEqual(projectPipelineHookWrappers([operationNotWrapper], [{ type: 'event_summarization' }]), { standalone: [operationNotWrapper], wrappers: {} });
});

test('legacy hooks with missing placement remain evidence rather than guessed phase members', () => {
    const known = { phase: 'after_planning', plan_phase: 2, sequence: 1 };
    const missing = { phase: 'after_planning', sequence: 2 };
    const unknown = { phase: 'future_stage', sequence: 1 };
    const hooks = [known, missing, unknown, { phase: 'before_planning' }, { phase: 'after_synthesis' }];
    assertDeepEqual(unplacedPipelineHooks(hooks, [{ phase_number: 2 }]), [missing, unknown]);
    assertDeepEqual(assignAfterPlanningHooksToPhases([known, missing], [{ phase_number: 2 }]), [[known]]);
});

test('places AfterPlanning hooks using their stored plan phase', () => {
    const phases = [
        { phase_number: 1 },
        { phase_number: 3 },
    ];
    const hooks = [
        { hook_name: 'phase-three', plan_phase: 3 },
        { hook_name: 'phase-one', plan_phase: 1 },
        { hook_name: 'unplaced' },
    ];

    assertDeepEqual(
        assignAfterPlanningHooksToPhases(hooks, phases).map(items =>
            items.map(item => item.hook_name)
        ),
        [
            ['phase-one'],
            ['phase-three'],
        ]
    );
});

test('keeps a rejected first phase without inventing an admitted plan', () => {
    const hook = { hook_name: 'required-policy', phase: 'after_planning', plan_phase: 1, status: 'failed' };
    const execution = { plan: null, pipeline_hooks: [hook] };
    const phases = buildObservedPhases(execution, { 1: [{ type: 'plan_generation' }] });
    assertDeepEqual(phases, [{ phase_number: 1, plan: null }]);
    assertDeepEqual(assignAfterPlanningHooksToPhases([hook], phases), [[hook]]);
    assertDeepEqual(execution.plan, null);
});

test('retains the rejected second phase after the admitted first phase', () => {
    const plan = { phase_number: 1, steps: [{ step_id: 'step-1' }] };
    const hooks = [1, 2].map(n => ({ hook_name: 'required-policy', phase: 'after_planning', plan_phase: n }));
    const execution = { phase_plans: [plan], pipeline_hooks: hooks };
    const before = JSON.stringify(execution);
    const phases = buildObservedPhases(execution, { 1: [{}], 2: [{}] });
    assertDeepEqual(phases, [{ phase_number: 1, plan }, { phase_number: 2, plan: null }]);
    assertDeepEqual(assignAfterPlanningHooksToPhases(hooks, phases), [[hooks[0]], [hooks[1]]]);
    assertDeepEqual(JSON.stringify(execution), before, 'view construction must not rewrite admitted plans');
});

test('keeps rejected phases when LLM debug is disabled or expired', () => {
    const execution = { pipeline_hooks: [{ phase: 'after_planning', plan_phase: 2, status: 'failed' }] };
    assertDeepEqual(buildObservedPhases(execution), [{ phase_number: 2, plan: null }]);
});

test('keeps a planning-only phase without fabricating tools or hooks', () => {
    assertDeepEqual(buildObservedPhases({}, { 3: [{ type: 'continuation_plan_generation' }] }),
        [{ phase_number: 3, plan: null }]);
});

test('sorts and deduplicates observed phases without filling unobserved gaps', () => {
    const p3 = { phase_number: 3, steps: [] };
    const p1 = { phase_number: 1, steps: [{ step_id: 'one' }] };
    const execution = { phase_plans: [p3, p1], pipeline_hooks: [
        { phase: 'after_planning', plan_phase: 3 },
        { phase: 'after_planning', plan_phase: 5 },
    ] };
    assertDeepEqual(buildObservedPhases(execution, { 3: [{}], 5: [{}] }), [
        { phase_number: 1, plan: p1 }, { phase_number: 3, plan: p3 }, { phase_number: 5, plan: null },
    ]);
});

test('preserves legacy single plans and HITL phase identities', () => {
    const legacy = { steps: [{ step_id: 'existing' }] };
    assertDeepEqual(buildObservedPhases({ plan: legacy }), [{ phase_number: 1, plan: legacy }]);
    const resume = { phase_number: 4, steps: [{ step_id: 'resumed-step' }] };
    assertDeepEqual(buildObservedPhases({ plan: resume }), [{ phase_number: 4, plan: resume }]);
});

test('does not guess phases for unplaced hooks or for non-planning stages', () => {
    assertDeepEqual(buildObservedPhases({ pipeline_hooks: [
        { phase: 'before_planning' }, { phase: 'after_synthesis', plan_phase: 9 },
        { phase: 'after_planning' }, { phase: 'after_planning', plan_phase: -1 },
    ] }, { 1: [] }), []);
    assertDeepEqual(buildObservedPhases(null), []);
});

test('keeps a legitimate zero-step clarification plan distinct from a rejected candidate', () => {
    const plan = { phase_number: 2, steps: [], needs_user_input: true };
    assertDeepEqual(buildObservedPhases({ phase_plans: [plan] }), [{ phase_number: 2, plan }]);
});

if (failed > 0) {
    console.error(`\n${failed} test(s) failed`);
    process.exit(1);
}

console.log('\nAll tests passed.');
