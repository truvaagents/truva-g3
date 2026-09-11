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
 * Stored hook records wrap hook implementations. Some framework hooks expose
 * their internal operations in an existing User Memory group. Only those
 * groups can replace a standalone invocation node. Other internal operations
 * are not hook wrappers and must not hide the authoritative hook record.
 */
export function pipelineHookHasDetailedInteractions(hook, interactions) {
    const name = hook?.hook_name || '';
    const calls = interactions || [];

    if (name === 'user-memory-enrichment' && hook.phase === 'before_planning') {
        return calls.some(call =>
            call?.type?.startsWith('user_memory_recall_') ||
            call?.type === 'user_memory_enrichment_injected'
        );
    }
    if (name === 'user-memory-extraction' && hook.phase === 'after_synthesis') {
        return calls.some(call =>
            call?.type?.startsWith('user_memory_') &&
            !call?.type?.startsWith('user_memory_recall_') &&
            call?.type !== 'user_memory_enrichment_injected'
        );
    }
    return false;
}

/** Preserve each invocation exactly once. Ambiguous repeated registrations
 * stay standalone: do not attach a decision to a guessed inner operation. */
export function projectPipelineHookWrappers(hooks, interactions) {
    const standalone = [];
    const wrappers = {};
    (hooks || []).forEach(hook => {
        const unique = hooks.filter(other => other.hook_name === hook.hook_name && other.phase === hook.phase).length === 1;
        if (!unique || !pipelineHookHasDetailedInteractions(hook, interactions)) {
            standalone.push(hook);
            return;
        }
        const id = hook.phase === 'before_planning' ? 'user_memory_before' : 'user_memory_after';
        wrappers[id] = [hook];
    });
    return { standalone, wrappers };
}

/** Describe only the stored boundary decision, never infer it from an effect,
 * request status, hook name, or a trace. Values remain available verbatim. */
export function pipelineHookConsequence(hook) {
    const decision = hook?.decision;
    const action = decision?.action;
    const labels = {
        continue: ['Request continued', 'Continued'],
        terminate: ['Request stopped', 'Stopped'],
        short_circuit: ['Early answer selected · planning skipped', 'Early answer'],
        propagate_panic: ['Panic propagated · request stopped', 'Panic propagated'],
    };
    let [summary, nodeLabel] = (Object.hasOwn(labels, action) ? labels[action] : null) || [
        decision ? 'Pipeline consequence not recognized' : 'Pipeline consequence not recorded',
        decision ? 'Unknown decision' : 'Not recorded',
    ];
    if (action === 'continue' || action === 'terminate') {
        if (decision.reason === 'clone_failed') summary = `Hook could not run · ${summary.toLowerCase()}`;
        else if (['invalid_type', 'invalid_plan'].includes(decision.reason)) summary = `Plan change rejected · ${summary.toLowerCase()}`;
        else if (hook.status === 'failed') summary = `Hook failed · ${summary.toLowerCase()}`;
        else if (['cache_read_disabled', 'cache_dimension_mismatch'].includes(decision.reason)) summary = `Cached answer not used · ${summary.toLowerCase()}`;
    }
    return { summary, nodeLabel, action: action || '', policy: decision?.failure_policy || '', reason: decision?.reason || '' };
}

/** Legacy hooks without a usable phase identity remain visible as evidence,
 * but must not be inserted into a guessed chronological phase. */
export function unplacedPipelineHooks(hooks, phases) {
    const known = new Set((phases || []).map(p => p.phase_number));
    return (hooks || []).filter(hook => {
        if (hook.phase === 'after_planning') return !known.has(Number(hook.plan_phase));
        return !isPreExecutionHook(hook) && !isPostExecutionHook(hook);
    });
}

/**
 * Associate AfterPlanning hook records with their explicitly stored iterative
 * plan phase. Unknown phases are handled separately as unplaced evidence,
 * instead of being attached to a guessed phase.
 */
export function assignAfterPlanningHooksToPhases(hooks, phasePlans) {
    const plans = phasePlans || [];
    const assignments = plans.map(() => []);

    (hooks || []).forEach(hook => {
        const phaseNumber = Number(hook?.plan_phase);
        if (!Number.isInteger(phaseNumber) || phaseNumber <= 0) return;
        const phaseIndex = plans.findIndex((plan, index) =>
            Number(plan?.phase_number || index + 1) === phaseNumber
        );
        if (phaseIndex >= 0) assignments[phaseIndex].push(hook);
    });

    return assignments;
}

/**
 * Full Flow follows observed phases, not just admitted plans. A required hook
 * can reject a generated plan, so its phase has planning/hook evidence but no
 * admitted tool steps. Keep that distinction in this view model; never insert
 * a synthetic plan into the execution record.
 */
export function buildObservedPhases(execution, planningCallsByPhase = {}) {
    const phases = new Map();
    const ensurePhase = value => {
        const number = Number(value);
        if (!Number.isInteger(number) || number <= 0) return null;
        if (!phases.has(number)) phases.set(number, { phase_number: number, plan: null });
        return phases.get(number);
    };
    const admittedPlans = execution?.phase_plans?.length
        ? execution.phase_plans
        : (execution?.plan ? [execution.plan] : []);
    admittedPlans.forEach((plan, index) => {
        const phase = ensurePhase(plan.phase_number || index + 1);
        if (phase) phase.plan = plan;
    });
    for (const hook of execution?.pipeline_hooks || []) {
        if (hook.phase === 'after_planning') ensurePhase(hook.plan_phase);
    }
    for (const [number, calls] of Object.entries(planningCallsByPhase)) {
        if (calls.length > 0) ensurePhase(number);
    }
    return [...phases.values()].sort((a, b) => a.phase_number - b.phase_number);
}
