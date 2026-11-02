/**
 * Component identity comes from the current registry, not naming conventions
 * or an LLM-authored plan. Replicas may agree on a type; ambiguous or missing
 * registrations stay unclassified. This is display metadata, not execution
 * evidence or an authorization decision.
 */
export function isAgentInvocation(serviceName, capability, services = []) {
    if (!serviceName || !capability || !Array.isArray(services)) return false;
    const matches = services.filter(service =>
        service?.name === serviceName &&
        Array.isArray(service.capabilities) &&
        service.capabilities.some(item => item?.name === capability)
    );
    return matches.length > 0 && matches.every(service => service.type === 'agent');
}

/** Summary copies deliberately exclude graphs, debug interactions and payloads. */
export function executionLineageSummary(execution) {
    if (!execution || typeof execution.request_id !== 'string' || !execution.request_id) return null;
    const summary = {};
    for (const key of [
        'request_id', 'original_request_id', 'parent_request_id', 'parent_step_id',
        'conversation_id', 'agent_name', 'created_at', 'record_revision', 'lifecycle',
        'original_request', 'step_count', 'failed_steps', 'skipped_steps', 'interrupted',
        'total_duration_ms', 'wall_clock_duration_ms', 'trace_id', 'relation_status', 'missing_owner_id',
    ]) {
        if (execution[key] !== undefined) summary[key] = execution[key];
    }
    // Keep only the existing resume fact, rather than retaining request metadata.
    if (execution.metadata?.resume_checkpoint_id) {
        summary.metadata = { resume_checkpoint_id: execution.metadata.resume_checkpoint_id };
    }
    return summary;
}

/** Execution links carry one opaque framework identifier, decoded once. */
export function executionDeepLinkRequestID(search) {
    const query = new URLSearchParams(search);
    const views = query.getAll('view'), ids = query.getAll('request_id');
    if (views.length !== 1 || views[0] !== 'dag' || ids.length !== 1) return null;
    const id = ids[0];
    return typeof id === 'string' && id.length > 0 && id.length <= 512 && /^[\x21-\x7e]+$/.test(id) ? id : null;
}

const goZeroTimestamp = Date.parse('0001-01-01T00:00:00Z');

export function executionSnapshotTimestamp(execution) {
    const timestamp = Date.parse(execution?.created_at);
    // time.Time's zero value is serialized, rather than omitted, by the Go
    // summary model. It expresses an unknown snapshot time, not year-one work.
    return timestamp === goZeroTimestamp ? NaN : timestamp;
}

export function compareExecutionSnapshots(a, b) {
    const left = executionSnapshotTimestamp(a);
    const right = executionSnapshotTimestamp(b);
    if (Number.isFinite(left) !== Number.isFinite(right)) return Number.isFinite(left) ? -1 : 1;
    if (Number.isFinite(left) && left !== right) return left - right;
    const leftID = String(a?.request_id || ''), rightID = String(b?.request_id || '');
    return leftID < rightID ? -1 : leftID > rightID ? 1 : 0;
}

/** Build navigation from explicit parents only. A shared root/trace is no edge.
 * Iterative traversal keeps deep/cyclic retained histories safe to display. */
export function buildExecutionHierarchy(summaries, coverage = {}) {
    const byRequest = new Map();
    for (const record of summaries || []) {
        if (!record || typeof record.request_id !== 'string' || !record.request_id) continue;
        const old = byRequest.get(record.request_id);
        if (!old || Number(record.record_revision || 0) > Number(old.record_revision || 0) ||
            (Number(record.record_revision || 0) === Number(old.record_revision || 0) && compareExecutionSnapshots(old, record) < 0)) {
            byRequest.set(record.request_id, record);
        }
    }
    const parentLookups = new Map((coverage.parent_lookups || []).map(item => [item.request_id, item]));
    const reasons = new Map();
    const visited = new Set();
    for (const id of byRequest.keys()) {
        if (visited.has(id)) continue;
        const path = [];
        const positions = new Map();
        let current = id;
        while (byRequest.has(current) && !visited.has(current)) {
            if (positions.has(current)) {
                for (const cyclic of path.slice(positions.get(current))) reasons.set(cyclic, 'cycle');
                break;
            }
            positions.set(current, path.length);
            path.push(current);
            const parent = byRequest.get(current).parent_request_id;
            if (!parent) break;
            current = parent;
        }
        for (const member of path) visited.add(member);
    }
    const roots = [], legacy = [], unresolved = [];
    const childrenByRequest = new Map();
    for (const record of byRequest.values()) {
        const id = record.request_id;
        const parent = record.parent_request_id;
        const parentRecord = byRequest.get(parent);
        if (!reasons.has(id) && record.parent_step_id && !parent) reasons.set(id, 'parent_request_missing');
        if (!reasons.has(id) && record.original_request_id && parentRecord?.original_request_id &&
            record.original_request_id !== parentRecord.original_request_id) reasons.set(id, 'root_conflict');
        if (reasons.has(id)) {
            unresolved.push(record);
        } else if (parent && byRequest.has(parent)) {
            if (!childrenByRequest.has(parent)) childrenByRequest.set(parent, []);
            childrenByRequest.get(parent).push(record);
        } else if (parent) {
            const lookup = parentLookups.get(id);
            reasons.set(id, lookup?.parent_request_id === parent && lookup.status === 'unavailable'
                ? 'parent_unavailable' : 'parent_unknown');
            unresolved.push(record);
        } else if (record.original_request_id && record.original_request_id !== id) {
            legacy.push(record);
        } else {
            roots.push(record);
        }
    }
    for (const list of [roots, legacy, unresolved, ...childrenByRequest.values()]) list.sort(compareExecutionSnapshots);
    const legacyIDs = new Set(legacy.map(item => item.request_id));
    const ordered = [];
    const stack = [...roots, ...legacy, ...unresolved].reverse().map(execution => ({
        execution, depth: 0,
        depthKnown: !reasons.has(execution.request_id) && !legacyIDs.has(execution.request_id) &&
            execution.original_request_id === execution.request_id,
    }));
    const rendered = new Set();
    while (stack.length) {
        const entry = stack.pop();
        const id = entry.execution.request_id;
        if (rendered.has(id)) continue;
        rendered.add(id);
        ordered.push({ ...entry, reason: reasons.get(id) || '', legacy: legacyIDs.has(id),
            parentAgentName: byRequest.get(entry.execution.parent_request_id)?.agent_name || '' });
        const children = childrenByRequest.get(id) || [];
        for (let index = children.length - 1; index >= 0; index--) {
            stack.push({ execution: children[index], depth: entry.depth + 1, depthKnown: entry.depthKnown });
        }
    }
    return { roots, legacy, unresolved, childrenByRequest, byRequest, ordered };
}

/** A step name has meaning only within its own execution. Preserve all calls. */
export function findDelegatedExecutions(summaries, parentRequestID, parentStepID) {
    if (!parentRequestID || !parentStepID) return [];
    return [...buildExecutionHierarchy(summaries).byRequest.values()].filter(execution =>
        execution.parent_request_id === parentRequestID && execution.parent_step_id === parentStepID
    ).sort(compareExecutionSnapshots);
}

/** Reject old-backend raw-record fallthrough as well as malformed coverage. */
export function validExecutionLineageResponse(data, requestID) {
    return data?.request_id === requestID && Array.isArray(data.executions) &&
        data.executions.some(item => item?.request_id === requestID) &&
        data.executions.every(item => typeof item?.request_id === 'string' && item.request_id) &&
        typeof data.partial === 'boolean' && typeof data.index_incomplete === 'boolean' &&
        (data.reasons === undefined || (Array.isArray(data.reasons) && data.reasons.every(reason => typeof reason === 'string'))) &&
        (data.parent_lookups === undefined || (Array.isArray(data.parent_lookups) && data.parent_lookups.every(item =>
            typeof item?.request_id === 'string' && typeof item.parent_request_id === 'string' &&
            ['available', 'unavailable', 'unknown'].includes(item.status))));
}

/** One view-local, summary-only discovery snapshot. Normal detail refresh does
 * not call discover(). Aborted/late responses cannot populate another family. */
export function createExecutionLineageLoader(load, now = () => Date.now()) {
    let selectedID = '', generation = 0, snapshot = null, pending = null, selectedUsesSnapshot = false;
    const contains = id => snapshot?.executions.some(item => item.request_id === id) || snapshot?.requestID === id ||
        (snapshot && id === selectedID && selectedUsesSnapshot);
    const cancel = () => {
        generation++;
        pending?.controller.abort();
        pending = null;
    };
    return {
        select(requestID, knownRecord = null) {
            if (selectedID !== requestID) {
                const knownIDs = new Set(snapshot?.executions.map(item => item.request_id) || []);
                if (snapshot?.requestID) knownIDs.add(snapshot.requestID);
                const knownRoots = new Set((snapshot?.executions || []).map(item => item.original_request_id || item.request_id));
                const reuse = !!contains(requestID) || !!(snapshot && knownRecord?.request_id === requestID &&
                    (knownIDs.has(knownRecord.parent_request_id) ||
                     (knownRecord.original_request_id && knownRoots.has(knownRecord.original_request_id))));
                cancel();
                selectedID = requestID;
                selectedUsesSnapshot = reuse;
                if (!reuse) snapshot = null;
            }
            return snapshot;
        },
        snapshot: () => snapshot,
        forRequest: requestID => contains(requestID) ? snapshot : null,
        pending: () => !!pending,
        discover({ force = false } = {}) {
            if (!selectedID) return Promise.resolve(null);
            if (pending) return pending.promise;
            if (!force && contains(selectedID)) return Promise.resolve(snapshot);
            const requestID = selectedID, version = generation;
            const controller = new AbortController();
            const promise = Promise.resolve().then(() => load(requestID, controller.signal)).then(data => {
                if (version !== generation || selectedID !== requestID) return null;
                if (!validExecutionLineageResponse(data, requestID)) throw new Error('lineage_unavailable');
                snapshot = {
                    requestID, state: 'available', discoveredAt: now(),
                    executions: data.executions.map(executionLineageSummary),
                    coverage: {
                        partial: data.partial, index_incomplete: data.index_incomplete,
                        reasons: [...(data.reasons || [])],
                        parent_lookups: (data.parent_lookups || []).map(item => ({ ...item })),
                    },
                };
                return snapshot;
            }).catch(() => {
                if (version !== generation || selectedID !== requestID) return null;
                snapshot = {
                    requestID, state: 'unavailable', discoveredAt: snapshot?.discoveredAt || now(),
                    executions: snapshot?.executions || [], coverage: { ...(snapshot?.coverage || {}), partial: true },
                };
                return snapshot;
            }).finally(() => {
                if (pending?.promise === promise) pending = null;
            });
            pending = { controller, promise };
            return promise;
        },
        destroy() { cancel(); snapshot = null; selectedID = ''; selectedUsesSnapshot = false; },
    };
}

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
