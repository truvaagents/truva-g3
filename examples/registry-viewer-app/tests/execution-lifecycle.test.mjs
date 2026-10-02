import assert from 'node:assert/strict';
import { test } from 'node:test';
import { executionStatus, matchesExecutionStatus } from '../static/js/utils/execution-lifecycle.js';

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
