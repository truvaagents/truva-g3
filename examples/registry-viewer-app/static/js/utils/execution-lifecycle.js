// Request lifecycle is emitted by the framework. Step/result success is a
// separate fact and cannot establish request completion (including on legacy records).
export function executionStatus(execution) {
    const states = {
        running: { css: 'pending', label: '◐ Running' },
        completed: { css: 'success', label: '✓ Completed' },
        failed: { css: 'error', label: '✗ Failed' },
        cancelled: { css: 'error', label: '⊘ Cancelled' },
        suspended: { css: 'interrupted', label: '⏸ Suspended' },
    };
    const state = execution?.lifecycle?.state;
    return Object.hasOwn(states, state)
        ? { state, ...states[state] }
        : { state: 'unknown', css: 'pending', label: 'Lifecycle unknown' };
}

export function matchesExecutionStatus(execution, filter) {
    const { state } = executionStatus(execution);
    if (filter === 'success') return state === 'completed';
    if (filter === 'failed') return state === 'failed' || state === 'cancelled';
    return true;
}
