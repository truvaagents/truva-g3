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

// Only recorded lifecycle owns failure. A diagnostic can be displayed without
// claiming that it caused termination, including after successful tool steps.
export function executionFailureNotice(execution) {
    if (executionStatus(execution).state !== 'failed') return '';
    const reason = execution?.result?.metadata?.termination_reason;
    const diagnostics = ['projections', 'activations', 'resource_selections'].flatMap(field =>
        Array.isArray(execution?.skills?.[field]) ? execution.skills[field] : []);
    const observed = [...new Set(diagnostics
        .filter(item => item && (item.outcome === 'budget_exceeded' || item.admission === 'budget_exceeded'))
        .map(item => `${item.boundary || 'unknown boundary'}: budget_exceeded`))];
    const terminal = typeof reason === 'string' && reason
        ? `Recorded termination reason: ${reason}.`
        : 'The terminal cause is unavailable in the stored execution.';
    return `The request failed. Tool-step outcomes are separate from this lifecycle failure. ${terminal}` +
        (observed.length ? ` Observed skill diagnostics: ${observed.join('; ')}. These diagnostics are not linked to the terminal cause.` : '');
}
