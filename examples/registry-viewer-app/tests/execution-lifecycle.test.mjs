import assert from 'node:assert/strict';
import { test } from 'node:test';
import { executionStatus, matchesExecutionStatus, executionFailureNotice } from '../static/js/utils/execution-lifecycle.js';

test('request lifecycle stays independent of phase success and business disposition', () => {
    const running = { success: true, lifecycle: { state: 'running' } };
    const completedNegative = { success: false, lifecycle: { state: 'completed' } };
    assert.equal(executionStatus(running).state, 'running');
    assert.equal(matchesExecutionStatus(running, 'success'), false);
    assert.equal(matchesExecutionStatus(completedNegative, 'success'), true);
    for (const state of ['failed', 'cancelled']) {
        assert.equal(matchesExecutionStatus({ lifecycle: { state } }, 'failed'), true);
    }
    for (const legacy of [{ success: true }, { success: false }, { interrupted: true }, { lifecycle: { state: 'toString' } }]) {
        assert.equal(executionStatus(legacy).state, 'unknown');
        assert.equal(matchesExecutionStatus(legacy, 'success'), false);
        assert.equal(matchesExecutionStatus(legacy, 'failed'), false);
    }
});

test('failure notice uses lifecycle and qualifies unlinked skill diagnostics', () => {
    const evidence = { skills: { projections: [{ boundary: 'synthesis', outcome: 'budget_exceeded' }] } };
    assert.equal(executionFailureNotice({ ...evidence, lifecycle: { state: 'completed' } }), '');
    assert.equal(executionFailureNotice({ ...evidence, success: false }), '');
    const failed = executionFailureNotice({ ...evidence, lifecycle: { state: 'failed' } });
    assert.match(failed, /terminal cause is unavailable/);
    assert.match(failed, /synthesis: budget_exceeded/);
    assert.match(failed, /not linked to the terminal cause/);
    assert.match(executionFailureNotice({ lifecycle: { state: 'failed' }, result: { metadata: { termination_reason: 'plan_validation_exhausted' } } }), /Recorded termination reason: plan_validation_exhausted/);
    assert.match(executionFailureNotice({ lifecycle: { state: 'failed' }, metadata: { termination_reason: 'unattributed-top-level-value' } }), /terminal cause is unavailable/);
    const admission = executionFailureNotice({ lifecycle: { state: 'failed' }, skills: {
        resource_selections: [{ boundary: 'synthesis', admission: 'budget_exceeded' }],
        activations: [{ boundary: 'initial_planning', admission: 'budget_exceeded' }],
    } });
    assert.match(admission, /synthesis: budget_exceeded/);
    assert.match(admission, /initial_planning: budget_exceeded/);
    assert.match(admission, /not linked to the terminal cause/);
    assert.doesNotThrow(() => executionFailureNotice({ lifecycle: { state: 'failed' }, skills: { projections: {} } }));
});
