/**
 * Remove graph edges whose endpoints are not present in the rendered node set.
 *
 * HITL resume records can legitimately retain implicit data-provenance links to
 * steps stored on the interrupted parent execution. Those parent steps are not
 * nodes in the resume record's own DAG. Cytoscape rejects any such dangling
 * edge and otherwise leaves the whole canvas blank, so callers must validate
 * the graph boundary before handing elements to the renderer.
 */
export function filterRenderableEdges(nodes, edges) {
    const nodeIDs = new Set(
        (nodes || [])
            .map(node => node?.data?.id)
            .filter(id => typeof id === 'string' && id !== '')
    );
    const renderable = [];
    const omitted = [];

    (edges || []).forEach(edge => {
        const source = edge?.data?.source;
        const target = edge?.data?.target;
        if (nodeIDs.has(source) && nodeIDs.has(target)) {
            renderable.push(edge);
        } else {
            omitted.push(edge);
        }
    });

    return { renderable, omitted };
}

/**
 * Keep step-scoped interactions only when their parent step is part of the
 * graph currently being rendered. A HITL resume can carry debug interactions
 * from its interrupted parent execution; those remain available in the LLM
 * Calls tab but must not appear as disconnected nodes in the resume DAG.
 */
export function filterInteractionsWithRenderedParent(interactions, stepIDs) {
    const renderedStepIDs = stepIDs instanceof Set ? stepIDs : new Set(stepIDs || []);
    return (interactions || []).filter(interaction =>
        renderedStepIDs.has(interaction?.step_id)
    );
}

/**
 * A relationship branch is meaningful only when its owner is present in the
 * rendered execution group. Orphan rows retain lineage text for audit, but a
 * branch glyph or connector would falsely imply a visible parent.
 */
export function hasVisibleRelationOwner(execution, orphan = false) {
    if (orphan) return false;
    return execution?.relation_status !== 'owner_unavailable' &&
        execution?.relation_status !== 'owner_unknown';
}

/**
 * Count the distinct steps completed before a HITL resume. These steps belong
 * to the same logical workflow, so the resume must continue their visible
 * numbering instead of restarting at one.
 */
export function hitlContinuationStepOffset(execution) {
    const lifecycle = execution?.hitl_lifecycle;
    if (!lifecycle?.is_resume) return 0;

    const completedSteps = lifecycle.current_checkpoint?.completed_steps;
    let stepIDs = [];

    if (Array.isArray(completedSteps)) {
        stepIDs = completedSteps.map(step => step?.step_id);
    } else if (completedSteps && typeof completedSteps === 'object') {
        stepIDs = Object.keys(completedSteps);
    }

    return new Set(stepIDs.filter(Boolean)).size;
}

/**
 * Step IDs are opaque identifiers, not display ordinals. Number a normal
 * execution from one, and continue after the completed pre-interruption steps
 * for a HITL resume.
 */
export function executionDisplayStepNumber(index, execution) {
    if (!Number.isInteger(index) || index < 0) return null;
    return hitlContinuationStepOffset(execution) + index + 1;
}

const preExecutionHookPhases = new Set(['before_planning', 'after_planning']);
const postExecutionHookPhases = new Set(['after_execution', 'after_synthesis']);

export function isPreExecutionHook(hook) {
    return preExecutionHookPhases.has(hook?.phase);
}

export function isPostExecutionHook(hook) {
    return postExecutionHookPhases.has(hook?.phase);
}

/**
 * Trace spans wrap hook implementations. Some framework hooks already expose
 * their internal operations as richer LLM-debug nodes. Suppress only those
 * wrapper nodes in Full Flow so the graph does not imply duplicate execution;
 * the Pre/Post tabs still show both the hook invocation and its operations.
 */
export function pipelineHookHasDetailedInteractions(hook, interactions) {
    const name = hook?.hook_name || '';
    const calls = interactions || [];

    if (name === 'user-memory-enrichment') {
        return calls.some(call =>
            call?.type?.startsWith('user_memory_recall_') ||
            call?.type === 'user_memory_enrichment_injected'
        );
    }
    if (name === 'user-memory-extraction') {
        return calls.some(call =>
            call?.type?.startsWith('user_memory_') &&
            !call?.type?.startsWith('user_memory_recall_') &&
            call?.type !== 'user_memory_enrichment_injected'
        );
    }
    if (name === 'memory-enrichment') {
        return calls.some(call =>
            call?.type === 'activity_compaction' ||
            call?.type === 'activity_compaction_incremental'
        );
    }
    if (name === 'memory-record') {
        return calls.some(call => call?.type === 'event_summarization');
    }
    return false;
}

/**
 * Associate AfterPlanning spans with the latest phase plan created before the
 * span began. The trace schema does not carry phase_number on hook spans, but
 * both timestamps are authoritative and preserve the actual iterative order.
 * Hooks that cannot be placed safely remain tab-visible and are omitted from
 * Full Flow instead of being attached to a guessed phase.
 */
export function assignAfterPlanningHooksToPhases(hooks, phasePlans) {
    const plans = phasePlans || [];
    const assignments = plans.map(() => []);
    const planTimes = plans.map(plan => Date.parse(plan?.created_at || ''));

    (hooks || []).forEach(hook => {
        const hookTime = Date.parse(hook?.started_at || '');
        if (!Number.isFinite(hookTime)) return;

        let bestIndex = -1;
        let bestTime = -Infinity;
        planTimes.forEach((planTime, index) => {
            if (Number.isFinite(planTime) && planTime <= hookTime && planTime > bestTime) {
                bestIndex = index;
                bestTime = planTime;
            }
        });
        if (bestIndex >= 0) assignments[bestIndex].push(hook);
    });

    return assignments;
}
