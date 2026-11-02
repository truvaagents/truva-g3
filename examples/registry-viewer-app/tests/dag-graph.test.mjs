// DAG renderer boundary tests.
//
// Run: node tests/dag-graph.test.mjs

import {
    assignAfterPlanningHooksToPhases,
    buildObservedPhases,
    buildExecutionHierarchy,
    createExecutionLineageLoader,
    executionLineageSummary,
    executionDisplayStepNumber,
    filterInteractionsWithRenderedParent,
    filterRenderableEdges,
    findDelegatedExecutions,
    hasVisibleRelationOwner,
    hitlContinuationStepOffset,
    isAgentInvocation,
    isPostExecutionHook,
    isPreExecutionHook,
    pipelineHookHasDetailedInteractions,
    pipelineHookConsequence,
    projectPipelineHookWrappers,
    unplacedPipelineHooks,
    validExecutionLineageResponse,
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

test('explicit parents preserve three levels and concurrent reviewers without requiring root results', () => {
    const records = [
        { request_id: 'root', lifecycle: { state: 'failed' }, step_count: 0 },
        { request_id: 'translator', original_request_id: 'root', parent_request_id: 'root', parent_step_id: 'step-1' },
        { request_id: 'review-b', original_request_id: 'root', parent_request_id: 'translator', parent_step_id: 'step-18' },
        { request_id: 'review-a', original_request_id: 'root', parent_request_id: 'translator', parent_step_id: 'step-17' },
    ];
    const hierarchy = buildExecutionHierarchy(records);
    assertDeepEqual(hierarchy.ordered.map(entry => [entry.execution.request_id, entry.depth]), [
        ['root', 0], ['translator', 1], ['review-a', 2], ['review-b', 2],
    ]);
    assertDeepEqual(records[0].lifecycle.state, 'failed', 'children do not relabel a failed parent');
    assertDeepEqual(hierarchy.byRequest.size, 4);
});

test('six-execution failed-root fixture and resumed child preserve recorded links without fabricated steps', () => {
    const records = [
        { request_id: 'review-a', parent_request_id: 'child-a', parent_step_id: 'step-1' },
        { request_id: 'review-b', parent_request_id: 'child-b', parent_step_id: 'step-1' },
        { request_id: 'child-c', parent_request_id: 'failed-root', parent_step_id: 'step-3', metadata: { resume_checkpoint_id: 'checkpoint' } },
        { request_id: 'child-a', parent_request_id: 'failed-root', parent_step_id: 'step-1' },
        { request_id: 'child-b', parent_request_id: 'failed-root', parent_step_id: 'step-2' },
        { request_id: 'failed-root', step_count: 0, lifecycle: { state: 'failed' } },
    ];
    const hierarchy = buildExecutionHierarchy(records);
    assertDeepEqual(hierarchy.ordered.map(entry => [entry.execution.request_id, entry.depth]), [
        ['failed-root', 0], ['child-a', 1], ['review-a', 2], ['child-b', 1], ['review-b', 2], ['child-c', 1],
    ]);
    assertDeepEqual(hierarchy.byRequest.get('failed-root').step_count, 0);
    assertDeepEqual(hierarchy.byRequest.get('child-c').metadata.resume_checkpoint_id, 'checkpoint');
});

test('contradictory roots and a step without a parent are unresolved while cross-conversation parents remain valid', () => {
    const hierarchy = buildExecutionHierarchy([
        { request_id: 'root', original_request_id: 'root', conversation_id: 'a' },
        { request_id: 'valid', original_request_id: 'root', parent_request_id: 'root', conversation_id: 'b' },
        { request_id: 'conflict', original_request_id: 'other', parent_request_id: 'root' },
        { request_id: 'step-only', parent_step_id: 'step-1' },
    ]);
    assertDeepEqual(hierarchy.childrenByRequest.get('root').map(item => item.request_id), ['valid']);
    assertDeepEqual(hierarchy.ordered.find(entry => entry.execution.request_id === 'conflict').reason, 'root_conflict');
    assertDeepEqual(hierarchy.ordered.find(entry => entry.execution.request_id === 'step-only').reason, 'parent_request_missing');
});

test('same-step children require both the parent execution and its exact step identity', () => {
    const records = [
        { request_id: 'a', parent_request_id: 'root', parent_step_id: 'step-1' },
        { request_id: 'b', parent_request_id: 'root', parent_step_id: 'step-1' },
        { request_id: 'c', parent_request_id: 'other', parent_step_id: 'step-1' },
        { request_id: 'd', parent_request_id: 'root' },
    ];
    assertDeepEqual(findDelegatedExecutions(records, 'root', 'step-1').map(item => item.request_id), ['a', 'b']);
    assertDeepEqual(findDelegatedExecutions(records, '', 'step-1'), []);
    assertDeepEqual(findDelegatedExecutions(records, 'root', ''), []);
});

test('absolute depth requires a recorded root and stays unknown through incomplete ancestry', () => {
    const known = buildExecutionHierarchy([
        { request_id: 'root', original_request_id: 'root' },
        { request_id: 'child', original_request_id: 'root', parent_request_id: 'root' },
        { request_id: 'grandchild', original_request_id: 'root', parent_request_id: 'child' },
        { request_id: 'great-grandchild', original_request_id: 'root', parent_request_id: 'grandchild' },
        { request_id: 'level-four', original_request_id: 'root', parent_request_id: 'great-grandchild' },
    ]);
    assertDeepEqual(known.ordered.map(entry => [entry.depth, entry.depthKnown]), [[0, true], [1, true], [2, true], [3, true], [4, true]]);

    for (const ancestor of [
        { request_id: 'partial', original_request_id: 'expired-root', parent_request_id: 'expired-parent' },
        { request_id: 'partial', original_request_id: 'legacy-root' },
        { request_id: 'partial' },
    ]) {
        const records = [ancestor, { request_id: 'descendant', parent_request_id: 'partial' }, { request_id: 'deep-descendant', parent_request_id: 'descendant' }];
        const hierarchy = buildExecutionHierarchy(records);
        assertDeepEqual(hierarchy.ordered.map(entry => entry.depthKnown), [false, false, false], 'an incomplete prefix affects the whole visible branch');
        assertDeepEqual(hierarchy.ordered.map(entry => entry.depth), [0, 1, 2], 'known relative edges remain navigable');
        assertDeepEqual(new Set(hierarchy.ordered.map(entry => entry.execution.request_id)).size, 3);
    }
});

test('snapshot ordering is deterministic, missing times last, latest revisions win without mutating inputs', () => {
    const records = [
        { request_id: 'root' },
        { request_id: 'b', parent_request_id: 'root', created_at: '2026-11-01T14:00:00Z', record_revision: 2 },
        { request_id: 'a', parent_request_id: 'root', created_at: '2026-11-01T14:00:00Z' },
        { request_id: 'b', parent_request_id: 'root', created_at: '2026-11-01T15:00:00Z', record_revision: 1 },
        { request_id: 'z', parent_request_id: 'root', created_at: 'invalid' },
        { request_id: 'y', parent_request_id: 'root' },
        { request_id: 'go-zero', parent_request_id: 'root', created_at: '0001-01-01T00:00:00Z' },
    ];
    const before = JSON.stringify(records);
    assertDeepEqual(buildExecutionHierarchy(records).ordered.map(entry => entry.execution.request_id), ['root', 'a', 'b', 'go-zero', 'y', 'z']);
    assertDeepEqual(JSON.stringify(records), before);
    const later = [...records, { request_id: 'a', parent_request_id: 'root', record_revision: 3, created_at: '2026-11-01T16:00:00Z' }];
    assertDeepEqual(buildExecutionHierarchy(later).ordered.map(entry => entry.execution.request_id), ['root', 'b', 'a', 'go-zero', 'y', 'z']);
});

test('legacy, self links, cycles, expired and unknown parents remain visible exactly once', () => {
    const records = [
        { request_id: 'root', trace_id: 'shared' },
        { request_id: 'legacy', original_request_id: 'root', trace_id: 'shared' },
        { request_id: 'self', parent_request_id: 'self' },
        { request_id: 'cycle-a', parent_request_id: 'cycle-b' },
        { request_id: 'cycle-b', parent_request_id: 'cycle-a' },
        { request_id: 'after-cycle', parent_request_id: 'cycle-a' },
        { request_id: 'expired', parent_request_id: 'gone' },
        { request_id: 'unknown', parent_request_id: 'outside-page' },
    ];
    const hierarchy = buildExecutionHierarchy(records, { parent_lookups: [
        { request_id: 'expired', parent_request_id: 'gone', status: 'unavailable' },
    ] });
    assertDeepEqual(hierarchy.ordered.length, records.length);
    assertDeepEqual(new Set(hierarchy.ordered.map(entry => entry.execution.request_id)).size, records.length);
    assertDeepEqual(hierarchy.legacy.map(item => item.request_id), ['legacy']);
    assertDeepEqual(hierarchy.ordered.find(entry => entry.execution.request_id === 'expired').reason, 'parent_unavailable');
    assertDeepEqual(hierarchy.ordered.find(entry => entry.execution.request_id === 'unknown').reason, 'parent_unknown');
    assertDeepEqual(hierarchy.ordered.filter(entry => entry.reason === 'cycle').length, 3);
    assertDeepEqual(hierarchy.childrenByRequest.has('root'), false, 'root and trace alone create no edges');
});

test('deep hierarchy traversal remains iterative and cross-conversation identity is left unchanged', () => {
    const records = Array.from({ length: 12000 }, (_, index) => ({
        request_id: `r-${index}`, parent_request_id: index ? `r-${index - 1}` : '',
        conversation_id: index % 2 ? 'other' : 'current',
    }));
    const hierarchy = buildExecutionHierarchy(records);
    assertDeepEqual(hierarchy.ordered.length, records.length);
    assertDeepEqual(hierarchy.ordered.at(-1).depth, records.length - 1);
    assertDeepEqual(hierarchy.byRequest.get('r-1').conversation_id, 'other');
});

test('summary cache omits raw/LLM payloads and old-backend raw fallback is rejected', () => {
    const raw = { request_id: 'child', parent_request_id: 'parent', llm_interactions: [{ prompt: 'private' }],
        result: { steps: [] }, plan: { steps: [] }, metadata: { resume_checkpoint_id: 'checkpoint', secret: 'unused' } };
    assertDeepEqual(executionLineageSummary(raw), {
        request_id: 'child', parent_request_id: 'parent', metadata: { resume_checkpoint_id: 'checkpoint' },
    });
    assertDeepEqual(validExecutionLineageResponse(raw, 'child'), false);
    assertDeepEqual(validExecutionLineageResponse({ request_id: 'child', executions: [], partial: false, index_incomplete: false }, 'child'), false);
});

test('agent invocation styling uses registered type and capability, not a name heuristic', () => {
    const services = [
        { name: 'reviewer', type: 'agent', capabilities: [{ name: 'review' }] },
        { name: 'pretend-agent', type: 'tool', capabilities: [{ name: 'inspect' }] },
    ];
    assertDeepEqual(isAgentInvocation('reviewer', 'review', services), true);
    assertDeepEqual(isAgentInvocation('reviewer', 'other', services), false);
    assertDeepEqual(isAgentInvocation('pretend-agent', 'inspect', services), false);
    assertDeepEqual(isAgentInvocation('missing-agent', 'review', services), false);
});

test('agent invocation replicas must agree and unavailable metadata stays unclassified', () => {
    const agent = { name: 'reviewer', type: 'agent', capabilities: [{ name: 'review' }] };
    assertDeepEqual(isAgentInvocation('reviewer', 'review', [agent, { ...agent }]), true);
    assertDeepEqual(isAgentInvocation('reviewer', 'review', [agent, { ...agent, type: 'tool' }]), false);
    assertDeepEqual(isAgentInvocation('reviewer', 'review', [agent, { ...agent, type: 'unknown' }]), false);
    assertDeepEqual(isAgentInvocation('', 'review', [agent]), false);
    assertDeepEqual(isAgentInvocation('reviewer', '', [agent]), false);
    assertDeepEqual(isAgentInvocation('reviewer', 'review', null), false);
    assertDeepEqual(isAgentInvocation('reviewer', 'review'), false);
});

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

async function asyncTest(name, fn) {
    try {
        await fn();
        console.log(`  ✓ ${name}`);
    } catch (error) {
        failed++;
        console.error(`  ✗ ${name}\n    ${error.message}`);
    }
}

function lineageResponse(requestID, relatives = []) {
    return { request_id: requestID, executions: [{ request_id: requestID }, ...relatives], partial: false, index_incomplete: false };
}

await asyncTest('cold selection coalesces discovery; known family navigation and detail refresh need no scan', async () => {
    let calls = 0, complete;
    const loader = createExecutionLineageLoader(() => {
        calls++;
        return new Promise(resolve => { complete = resolve; });
    }, () => 1000);
    loader.select('root');
    const first = loader.discover();
    const concurrent = loader.discover({ force: true });
    assertDeepEqual(first === concurrent, true);
    await Promise.resolve();
    complete(lineageResponse('root', [{ request_id: 'child', parent_request_id: 'root', llm_interactions: ['omitted'] }]));
    await first;
    assertDeepEqual(calls, 1);
    loader.select('child');
    await loader.discover();
    assertDeepEqual(calls, 1);
    loader.select('newly-listed-child', { request_id: 'newly-listed-child', parent_request_id: 'root' });
    await loader.discover();
    assertDeepEqual(calls, 1, 'a new child already linked by refreshed list summaries reuses family discovery');
    assertDeepEqual(loader.snapshot().executions.length, 2, 'fresh list revisions do not accumulate in the discovery snapshot');
    assertDeepEqual(loader.forRequest('newly-listed-child'), loader.snapshot());
    assertDeepEqual(loader.forRequest('unrelated'), null, 'another selection cannot borrow this family');
    assertDeepEqual(loader.snapshot().discoveredAt, 1000);
    assertDeepEqual(loader.snapshot().executions[1].llm_interactions, undefined);
    // The view's periodic path only reads the current snapshot and refreshes
    // unified details. Reading/reusing it cannot initiate a broad request.
    for (let index = 0; index < 10; index++) loader.snapshot();
    assertDeepEqual(calls, 1);
});

await asyncTest('late cancelled responses cannot overwrite a newly selected family', async () => {
    const requests = [];
    const loader = createExecutionLineageLoader((requestID, signal) => new Promise(resolve => {
        requests.push({ requestID, signal, resolve });
    }));
    loader.select('old');
    const old = loader.discover();
    await Promise.resolve();
    loader.select('new');
    const next = loader.discover();
    await Promise.resolve();
    assertDeepEqual(requests[0].signal.aborted, true);
    requests[1].resolve(lineageResponse('new'));
    await next;
    requests[0].resolve(lineageResponse('old'));
    await old;
    assertDeepEqual(loader.snapshot().requestID, 'new');
    loader.destroy();
    assertDeepEqual(loader.snapshot(), null);
});

await asyncTest('failed discovery is retained without automatic retries; manual refresh replaces bounded snapshot', async () => {
    let calls = 0;
    const loader = createExecutionLineageLoader(async requestID => {
        calls++;
        if (calls === 1) return { request_id: requestID, result: {}, plan: {} }; // old server fallthrough
        return lineageResponse(requestID, [{ request_id: `child-${calls}`, parent_request_id: requestID }]);
    });
    loader.select('root');
    await loader.discover();
    assertDeepEqual(loader.snapshot().state, 'unavailable');
    await loader.discover();
    assertDeepEqual(calls, 1);
    await loader.discover({ force: true });
    assertDeepEqual(loader.snapshot().executions.map(item => item.request_id), ['root', 'child-2']);
    await loader.discover({ force: true });
    assertDeepEqual(loader.snapshot().executions.map(item => item.request_id), ['root', 'child-3']);
});

await asyncTest('manual refresh failure preserves known summaries and their last discovery timestamp', async () => {
    let calls = 0;
    const loader = createExecutionLineageLoader(async requestID => {
        if (++calls > 1) throw new Error('unavailable');
        return lineageResponse(requestID, [{ request_id: 'child', parent_request_id: requestID }]);
    }, () => calls * 1000);
    loader.select('root');
    await loader.discover();
    await loader.discover({ force: true });
    assertDeepEqual(loader.snapshot().discoveredAt, 1000);
    assertDeepEqual(loader.snapshot().executions.length, 2);
    assertDeepEqual(loader.snapshot().coverage.partial, true);
});

if (failed > 0) {
    console.error(`\n${failed} test(s) failed`);
    process.exit(1);
}

console.log('\nAll tests passed.');
