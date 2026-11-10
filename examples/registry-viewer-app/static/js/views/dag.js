/**
 * Execution DAG View.
 *
 * Extracted from index.html — the largest view (~3,300 lines).
 * Renders execution list, DAG visualization (Cytoscape), step details,
 * LLM calls, and HITL checkpoints within the DAG detail panel.
 */

import {
    formatTimeAgo,
    formatDateTime,
    formatDuration,
    formatTokens,
    formatBytes,
    truncateInstruction,
    truncateText,
    escapeHtml,
    syntaxHighlightJson,
    prettyPrintJsonText,
    formatResponseJson,
    copyToClipboard,
    formatConversationRequest,
} from '../utils/format.js';
import { fetchAPI } from '../api.js';
import { createLoadGuard } from '../utils/load-guard.js';
import { executionStatus, executionFailureNotice } from '../utils/execution-lifecycle.js';
import { showLoading, hideLoading } from '../utils/dom.js';
import {
    getLLMInteractionContent,
    getLLMPromptSections,
} from '../utils/llm-content.js';
import {
    getLLMType,
    isPlanningType,
    isPhaseClosingType,
    getPhaseNumber,
} from '../llm-types.js';
import {
    assignAfterPlanningHooksToPhases,
    assignSkillCallsToConsumers,
    buildObservedPhases,
    buildExecutionHierarchy,
    createExecutionLineageLoader,
    executionLineageSummary,
    executionDeepLinkRequestID,
    executionSnapshotTimestamp,
    executionDisplayStepLabel,
    executionStepNumberingNote,
    filterInteractionsWithRenderedParent,
    filterRenderableEdges,
    findDelegatedExecutions,
    isAgentInvocation,
    isPostExecutionHook,
    isPreExecutionHook,
    pipelineHookConsequence,
    projectPipelineHookWrappers,
    unplacedPipelineHooks,
} from '../utils/dag-graph.js';

// ---------------------------------------------------------------------------
// Module-level state
// ---------------------------------------------------------------------------
let executions = [];
let selected = null;
let selectionVersion = 0;
let activeTab = 'dag-viz';
let filter = 'all';
let cyInstance = null;
let legendCleanup = null;
let viewMode = 'full';
let sortColumn = 'created_at';
let sortDirection = 'desc';

const DAG_GROUP_PAGE_SIZE = 30;
const CONVERSATION_PAGE_SIZE = 5;
const CONVERSATION_CACHE_MAX = 10;
const CONVERSATION_TITLE_CACHE_MAX = 100;
const CONVERSATION_TITLE_CACHE_TTL_MS = 60_000;
const CURSOR_HISTORY_MAX = 100;
// Amber dotted provenance remains distinct from dashed memory/approval links.
const DAG_PROVENANCE_COLOR = '#f3bb63';
const listLoad = createLoadGuard();
const detailLoad = createLoadGuard();
const sectionLoad = createLoadGuard();
const timelineLoad = createLoadGuard();
const conversationTitleLoad = createLoadGuard();
const conversationTitleCache = new Map();
let listPageCursor = '';
let listPageNumber = 1;
const listPageCursors = new Map();
let listQueryKey = '';
let listTimeRange = '24h';
let listWindowFrom = '';
let listWindowTo = '';
let selectedOverview = null;
let selectedSection = 'overview';
let sectionPending = false;
let sectionError = '';
let llmCardBatch = null;
const DAG_GROUPING_STORAGE_KEY = 'truvag3.dag.groupingMode';

let groupingMode = readGroupingPreference();
let groupedExecutionGroups = [];
let groupedNextCursor = '';
let groupedLoading = false;
let expandedGroupKeys = new Set();
let groupMatchPages = new Map();
let conversationCache = new Map();
let activeConversationID = '';
let listPanelMode = 'list';
let timelineReturnFocus = null;
let dagSearchTimer = null;
let invocationServices = [];
let invocationServicesPending = null;
let invocationServicesVersion = 0;
let invokingStepTarget = null;
let parentExecutionLookup = null;
const lineageLoader = createExecutionLineageLoader(async (requestID, signal) => {
    // The generic API cache retains every ETag response. Family discovery has
    // its own one-family lifetime and must not accumulate there.
    try {
        const { data } = await fetchAPI(`/api/executions/${encodeURIComponent(requestID)}/lineage`, { signal, cache: false });
        return data;
    } catch (error) {
        if (isDagAccessDenied(error)) clearDagAccessState(error);
        throw error;
    }
});

function selectedLineageSnapshot() {
    return selected?.request_id ? lineageLoader.forRequest(selected.request_id) : null;
}

function loadedLineageSummaries() {
    const records = [...executions];
    const append = group => {
        for (const turn of group?.turns || []) {
            records.push(turn.execution, ...(turn.related_executions || []));
        }
        records.push(...(group?.orphans || []));
    };
    groupedExecutionGroups.forEach(append);
    for (const cached of conversationCache.values()) if (cached.data) append(cached.data);
    records.push(...(selectedLineageSnapshot()?.executions || []), selected);
    return records.map(executionLineageSummary).filter(Boolean);
}

function lineageRelationLabel(entry) {
    if (entry.execution.metadata?.resume_checkpoint_id) return 'HITL resume';
    if (entry.reason === 'cycle') return 'Unresolved parent cycle';
    if (entry.reason === 'root_conflict') return 'Unresolved · parent and child roots conflict';
    if (entry.reason === 'parent_request_missing') return 'Unresolved · invoking step has no parent request';
    if (entry.reason === 'parent_unavailable') return 'Parent unavailable';
    if (entry.reason === 'parent_unknown') return 'Parent outside loaded records · lookup unknown';
    if (entry.legacy) return 'Related · immediate parent not recorded';
    return entry.depth > 0 ? 'Delegated execution' : '';
}

// Depth is absolute only when the loaded explicit chain reaches a recorded root.
// Filtered, expired and legacy ancestry stays unknown, including its descendants.
function renderLineageBadge(entry) {
    const execution = entry.execution;
    const known = entry.depthKnown === true;
    const level = known ? String(entry.depth) : '?';
    const tone = known ? (entry.depth ? `level-${((entry.depth - 1) % 4) + 1}` : 'root') : 'unknown';
    const parent = execution.parent_request_id;
    const title = known ? `Delegation depth ${entry.depth}${parent ? `; parent ${parent}` : '; root execution'}`
        : `${lineageRelationLabel(entry) || 'Full ancestry not recorded'}${parent ? `; parent ${parent}` : ''}`;
    const arrow = parent ? (entry.depth > 0 && !entry.reason ? '↳' : '↗') : known ? '◆' : '⋯';
    return `<span class="dag-lineage-badge dag-lineage-${tone}" title="${escapeHtmlAttribute(title)}" aria-label="${escapeHtmlAttribute(title)}"><span aria-hidden="true">${arrow}</span> ${known && !entry.depth ? 'Root' : `L${level}`}</span>`;
}

function renderLineageRowContext(entry) {
    const execution = entry.execution;
    const parentID = execution.parent_request_id;
    // Keep the full identity in the link title; the visible relationship is compact.
    return `<div class="dag-lineage-row-context">${renderLineageBadge(entry)}${execution.metadata?.resume_checkpoint_id ? '<span class="hitl-resume-badge dag-hitl-resume">HITL Resume</span>' : ''}<span class="dag-lineage-owner">${escapeHtml(execution.agent_name || 'Agent')}</span>${parentID
        ? `<span class="dag-lineage-parent" title="${escapeHtmlAttribute(`Parent ${parentID}${execution.parent_step_id ? ` · ${execution.parent_step_id}` : ''}`)}">← ${lineageNavigationButton(parentID, `${entry.parentAgentName || 'parent'}${execution.parent_step_id ? ` · ${execution.parent_step_id}` : ''}`, execution.parent_step_id)}</span>`
        : entry.legacy || entry.reason ? `<span class="dag-lineage-parent">${escapeHtml(lineageRelationLabel(entry))}</span>` : ''}</div>`;
}

function executionRequestText(execution) {
    // A missing child request is not permission to substitute its parent's task.
    return execution?.original_request || execution?.plan?.original_request || 'Request text unavailable';
}

function selectedFamilySummaries() {
    if (!selected) return [];
    const all = buildExecutionHierarchy(loadedLineageSummaries()).byRequest;
    const root = selected.original_request_id || selected.request_id;
    const members = new Set([selected.request_id]);
    for (const execution of all.values()) {
        if ((execution.original_request_id || execution.request_id) === root) members.add(execution.request_id);
    }
    for (const execution of selectedLineageSnapshot()?.executions || []) members.add(execution.request_id);
    const children = new Map();
    for (const execution of all.values()) {
        if (!execution.parent_request_id) continue;
        if (!children.has(execution.parent_request_id)) children.set(execution.parent_request_id, []);
        children.get(execution.parent_request_id).push(execution.request_id);
    }
    const queue = [...members];
    for (let index = 0; index < queue.length; index++) {
        const execution = all.get(queue[index]);
        for (const id of [execution?.parent_request_id, ...(children.get(queue[index]) || [])]) {
            if (id && all.has(id) && !members.has(id)) { members.add(id); queue.push(id); }
        }
    }
    return [...members].map(id => all.get(id)).filter(Boolean);
}

function lineageNavigationButton(requestID, label, stepID = '') {
    return `<button type="button" class="dag-open-timeline-btn" data-lineage-execution="${escapeHtmlAttribute(requestID)}"${stepID ? ` data-lineage-parent-step="${escapeHtmlAttribute(stepID)}"` : ''}>${escapeHtml(label)}</button>`;
}

function lastSnapshotLabel(execution, relative = false) {
    if (!Number.isFinite(executionSnapshotTimestamp(execution))) return 'Unknown';
    return relative ? formatTimeAgo(execution.created_at) : formatDateTime(execution.created_at);
}

function renderDelegatedExecutionLinks(stepID, summaries = selectedFamilySummaries()) {
    const children = findDelegatedExecutions(summaries, selected?.request_id, stepID);
    if (!children.length) return '';
    return children.map((child, index) => {
        const kind = child.metadata?.resume_checkpoint_id ? 'resumed execution' : 'agent execution';
        const label = `View ${kind}${children.length > 1 ? ` ${index + 1}` : ''} →`;
        const title = `${child.agent_name || 'Agent'} · ${child.request_id}`;
        return `<button type="button" class="dag-popup-detail-link" data-lineage-execution="${escapeHtmlAttribute(child.request_id)}" title="${escapeHtmlAttribute(title)}">${label}</button>`;
    }).join('');
}

function renderExecutionLineageNavigation(summaries = selectedFamilySummaries(), expanded = false) {
    if (!selected) return '';
    const snapshot = selectedLineageSnapshot();
    const hierarchy = buildExecutionHierarchy(summaries, snapshot?.coverage);
    const coverage = snapshot?.coverage;
    const pending = lineageLoader.pending();
    const unavailable = snapshot?.state === 'unavailable';
    const partial = coverage?.partial || coverage?.index_incomplete;
    const status = pending ? 'Loading related executions…'
        : unavailable ? 'Related execution lookup unavailable. Refresh to try again.'
        : partial ? 'Some related executions may be missing.'
        : snapshot ? 'Retained execution history' : 'Loaded records only · history coverage unknown';
    const updated = snapshot?.discoveredAt ? new Date(snapshot.discoveredAt).toISOString() : '';
    const count = hierarchy.ordered.length;
    return `<details class="dag-parent-family dag-family-panel"${expanded ? ' open' : ''}>
        <summary><span>Related executions</span><span class="dag-family-count" title="Known retained executions; this is not a complete history count">${count}</span>${!pending && (unavailable || partial) ? '<span class="dag-family-limited">Partial history</span>' : ''}</summary>
        <div class="dag-family-content">
            <div class="dag-family-toolbar">
                <div class="dag-family-discovery${unavailable || partial ? ' dag-family-warning' : ''}" role="status">
                    <span>${escapeHtml(status)}</span>
                    ${updated ? `<span class="dag-family-updated" title="${escapeHtmlAttribute(`Last lookup: ${formatDateTime(updated)}`)}">Updated ${escapeHtml(formatTimeAgo(updated))}</span>` : ''}
                </div>
                <button type="button" class="dag-family-refresh" data-refresh-execution-lineage title="Check for newly recorded related executions" aria-label="Refresh related executions" aria-disabled="${pending}"><svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.7" aria-hidden="true"><path d="M20 7v5h-5M4 17v-5h5"/><path d="M6.1 8a7 7 0 0 1 11.6-2L20 9M4 15l2.3 3A7 7 0 0 0 17.9 16"/></svg>Refresh</button>
            </div>
            <ul class="dag-family-list" aria-label="Known related executions">
                ${hierarchy.ordered.map(entry => {
                    const execution = entry.execution;
                    const current = execution.request_id === selected.request_id;
                    const state = executionStatus(execution);
                    const identity = execution.request_id || '';
                    const shortID = identity.length > 18 ? `…${identity.slice(-14)}` : identity;
                    const relation = entry.reason || entry.legacy || execution.metadata?.resume_checkpoint_id ? lineageRelationLabel(entry) : '';
                    return `<li class="dag-family-row${current ? ' dag-family-current' : ''}" style="--family-depth: ${Math.min(entry.depth, 4)};">
                        ${renderLineageBadge(entry)}
                        <div class="dag-family-identity">
                            <div class="dag-family-name">${current ? `<span aria-current="page">${escapeHtml(execution.agent_name || 'Agent')}</span><span class="dag-family-current-label">Current</span>` : lineageNavigationButton(identity, execution.agent_name || 'Agent')}</div>
                            <div class="dag-family-meta"><span class="dag-mono" title="${escapeHtmlAttribute(identity)}">${escapeHtml(shortID)}</span>${execution.parent_step_id ? `<span>${escapeHtml(execution.parent_step_id)}</span>` : ''}${relation ? `<span class="dag-family-warning">${escapeHtml(relation)}</span>` : ''}</div>
                        </div>
                        <span class="dag-family-status ${state.css} ${state.state}">${escapeHtml(state.label)}</span>
                    </li>`;
                }).join('')}
            </ul>
        </div>
    </details>`;
}

function resetParentExecutionLookup() {
    parentExecutionLookup?.controller.abort();
    parentExecutionLookup = null;
}

function loadParentExecution({ force = false } = {}) {
    if (!selected?.parent_request_id) return;
    if (!force && parentExecutionLookup?.childID === selected.request_id &&
        parentExecutionLookup.parentID === selected.parent_request_id) return;
    resetParentExecutionLookup();
    const lookup = { childID: selected.request_id, parentID: selected.parent_request_id,
        state: 'loading', data: null, controller: new AbortController() };
    parentExecutionLookup = lookup;
    // Read just the exact parent's execution record on demand, not its LLM store
    // or its relatives. Keep one view-local record and discard it on selection.
    void (async () => {
        try {
            const { data } = await fetchAPI(`/api/executions/${encodeURIComponent(lookup.parentID)}/unified?section=overview`, { signal: lookup.controller.signal, cache: false });
            if (data?.request_id !== lookup.parentID) throw new Error('parent_identity_mismatch');
            lookup.data = data;
            lookup.state = 'available';
        } catch (error) {
            if (isDagAccessDenied(error)) {
                clearDagAccessState(error);
                return;
            }
            lookup.state = error.status === 404 || error.status === 410 ? 'unavailable' : 'unknown';
        }
        if (parentExecutionLookup === lookup && selected?.request_id === lookup.childID &&
            selected.parent_request_id === lookup.parentID && activeTab === 'dag-parent') {
            renderParentExecution(document.getElementById('dagDetailContent'));
        }
    })();
}

function renderAgentParentInfo() {
    if (!selected) return '';
    const parentID = selected.parent_request_id;
    const parent = selectedFamilySummaries().find(item => item.request_id === parentID);
    return `<div class="dag-agent-lineage-info">
        ${parentID ? `<div class="dag-parent-field-label">Invoked by</div>
            <div>${escapeHtml(parent?.agent_name || 'Parent agent')}</div>
            <div class="dag-parent-identity">${escapeHtml(parentID)}</div>
            <div>${selected.parent_step_id ? `Invoking step: ${escapeHtml(selected.parent_step_id)}` : 'Invoking step not recorded'}</div>
            ${lineageNavigationButton(parentID, selected.parent_step_id ? 'Open parent at invoking step →' : 'Open parent execution →', selected.parent_step_id)}
            <button type="button" class="dag-open-timeline-btn" data-open-parent-execution>Parent details</button>`
            : `<div class="dag-parent-field-label">${selected.original_request_id && selected.original_request_id !== selected.request_id ? 'Immediate parent not recorded' : 'No recorded parent'}</div>`}
        <div><button type="button" class="dag-open-timeline-btn" data-refresh-execution-lineage ${lineageLoader.pending() ? 'disabled' : ''}>Refresh related executions</button></div>
    </div>`;
}

function renderParentExecution(container) {
    if (!selected?.parent_request_id) return;
    const familyExpanded = container.querySelector('.dag-family-panel')?.open === true;
    const refreshFocused = container.querySelector('[data-refresh-execution-lineage]') === document.activeElement;
    loadParentExecution();
    const parentID = selected.parent_request_id;
    const lookup = parentExecutionLookup;
    const parent = lookup?.data || selectedFamilySummaries().find(item => item.request_id === parentID);
    const stepID = selected.parent_step_id;
    const plans = lookup?.data?.phase_plans || [];
    const checkpoint = lookup?.data?.checkpoint;
    // Match ordinary execution normalization: current plan/result evidence wins,
    // with retained checkpoint evidence filling only absent step information.
    const result = lookup?.data?.result?.steps?.find(item => item.step_id === stepID)
        || checkpoint?.step_results?.[stepID];
    const planSteps = plans.length ? plans.flatMap(plan => plan.steps || []) : lookup?.data?.plan?.steps || [];
    const step = planSteps.find(item => item.step_id === stepID)
        || checkpoint?.plan?.steps?.find(item => item.step_id === stepID)
        || result;
    const capability = step?.capability || step?.metadata?.capability;
    const state = lookup?.state;
    container.innerHTML = `<div class="dag-parent-execution">
        <h2>Parent execution</h2>
        <p>This execution was invoked by the parent below. Each execution keeps its own lifecycle, steps and LLM records.</p>
        <section class="dag-parent-card">
            <h3>${escapeHtml(parent?.agent_name || 'Parent agent')}</h3>
            <div class="dag-parent-identity">${escapeHtml(parentID)}</div>
            ${parent ? `<div class="status-badge ${executionStatus(parent).css}">${executionStatus(parent).label}</div><div class="dag-parent-field-label">Parent request</div><div class="dag-parent-request">${escapeHtml(executionRequestText(parent))}</div>` : ''}
            <div class="dag-parent-actions">${lineageNavigationButton(parentID, 'Open parent execution →')}
                <button type="button" class="dag-open-timeline-btn" data-refresh-parent-execution ${state === 'loading' ? 'disabled' : ''}>Refresh parent record</button></div>
            <div role="status" class="dag-parent-field-label">${state === 'loading' ? 'Loading exact parent record…' : state === 'unavailable' ? 'Parent record unavailable or expired.' : state === 'unknown' ? 'Parent lookup failed; availability is unknown.' : 'Parent overview loaded. Open the parent step for exact evidence.'}</div>
        </section>
        <section class="dag-parent-card">
            <h3>Invoking step${stepID ? ` · ${escapeHtml(stepID)}` : ''}</h3>
            ${step ? `<div>${escapeHtml(step.agent_name || step.agent || '')}${capability ? ` · ${escapeHtml(capability)}` : ''}</div><p class="dag-parent-request">${escapeHtml(step.instruction || '')}</p>` : ''}
            ${result ? `<div class="status-badge ${result.skipped ? 'pending' : result.success ? 'success' : 'error'}">${result.skipped ? 'Skipped' : result.success ? 'Success' : 'Failed'}</div>` : ''}
            ${stepID ? lineageNavigationButton(parentID, 'Open parent at this step →', stepID) : '<p>Invoking step was not recorded.</p>'}
            ${stepID && state === 'available' && !step && !result ? '<p class="dag-data-warning">The invoking step is not present in the retained parent record.</p>' : ''}
        </section>
        ${renderExecutionLineageNavigation(selectedFamilySummaries(), familyExpanded)}
    </div>`;
    if (refreshFocused) container.querySelector('[data-refresh-execution-lineage]')?.focus({ preventScroll: true });
}

function refreshExecutionLineageUI() {
    const summaries = selectedFamilySummaries();
    if (activeTab === 'dag-parent' && selected?.parent_request_id) renderParentExecution(document.getElementById('dagDetailContent'));
    document.querySelectorAll('[data-agent-parent-info]').forEach(element => { element.innerHTML = renderAgentParentInfo(); });
    document.querySelectorAll('[data-lineage-step]').forEach(element => {
        if (element.dataset.lineageRequest === selected?.request_id) element.innerHTML = renderDelegatedExecutionLinks(element.dataset.lineageStep, summaries);
    });
}

function discoverExecutionLineage({ force = false, requestID = selected?.request_id } = {}) {
    const version = selectionVersion;
    const promise = lineageLoader.discover({ force });
    if (selected?.request_id === requestID) refreshExecutionLineageUI();
    void promise.then(() => {
        if (version === selectionVersion && selected?.request_id === requestID) refreshExecutionLineageUI();
    });
}

function navigateExecutionLineage(target) {
    invokingStepTarget = target.dataset.lineageParentStep
        ? { requestID: target.dataset.lineageExecution, stepID: target.dataset.lineageParentStep } : null;
    if (invokingStepTarget) activeTab = 'dag-steps';
    void selectExecution(target.dataset.lineageExecution);
}

// LRU cache for Cytoscape instances — avoids rebuild on tab switch.
// Key: "requestId:viewMode", Value: { cy, container (child div), requestId }
const dagCache = new Map();

// Registry decoration is optional and must not delay execution rendering.
// Update cached canvases in place so a late response preserves their viewport.
function refreshInvocationServices() {
    if (invocationServicesPending) return;
    const version = ++invocationServicesVersion;
    invocationServicesPending = fetchAPI('/api/services')
        .then(({ data }) => {
            if (version !== invocationServicesVersion) return;
            invocationServices = Array.isArray(data.services) ? data.services : [];
            updateInvocationNodes();
        })
        .catch(() => {
            if (version !== invocationServicesVersion) return;
            invocationServices = [];
            updateInvocationNodes();
        })
        .finally(() => {
            if (version === invocationServicesVersion) invocationServicesPending = null;
        });
}

function updateInvocationNodes() {
    for (const { cy } of dagCache.values()) {
        if (cy.destroyed()) continue;
        cy.batch(() => {
            cy.nodes().forEach(node => {
                if (node.data('stepLabel') === undefined) return;
                const agent = isAgentInvocation(node.data('serviceName'), node.data('capability'), invocationServices);
                node.data({ isAgentInvocation: agent, label: (agent ? '🤖 ' : '') + node.data('stepLabel') });
            });
        });
    }
    if (cyInstance && !cyInstance.destroyed()) updateDAGLegend(cyInstance);
}

function readGroupingPreference() {
    try {
        return sessionStorage.getItem(DAG_GROUPING_STORAGE_KEY) === 'grouped'
            ? 'grouped'
            : 'flat';
    } catch (_) {
        return 'flat';
    }
}

function storeGroupingPreference(mode) {
    try {
        sessionStorage.setItem(DAG_GROUPING_STORAGE_KEY, mode);
    } catch (_) {
        // Private browsing and embedded viewers may disable web storage.
    }
}

function escapeHtmlAttribute(value) {
    return escapeHtml(String(value ?? '')).replace(/"/g, '&quot;');
}

// The framework's phase-loop time and aggregate LLM call time overlap. The
// viewer must never add them. Prefer the backend's elapsed-time envelope and
// retain total_duration_ms only as a compatibility fallback for older rows.
function primaryDurationMs(execution) {
    return execution?.wall_clock_duration_ms || execution?.total_duration_ms || 0;
}

function jaegerTraceURL(traceID) {
    return `/api/traces/${encodeURIComponent(traceID || '')}`;
}

function renderInteractionTraceLink(interaction) {
    const traceID = interaction.trace_id;
    if (typeof traceID !== 'string' || !traceID.trim()) return '';
    return `<a class="dag-viz-meta-item dag-viz-trace-link dag-call-trace-link" href="${jaegerTraceURL(traceID)}" target="_blank" rel="noopener noreferrer" title="${escapeHtmlAttribute(`Open interaction trace ${traceID} in Jaeger`)}" aria-label="${escapeHtmlAttribute(`Open interaction trace ${traceID} in Jaeger`)}">TraceID <span aria-hidden="true">↗</span></a>`;
}

// ---------------------------------------------------------------------------
// Hook-phase classification
// ---------------------------------------------------------------------------
// Source of truth: LLMInteraction.hook_phase (set on the Go side).
// Fallback: agents built before hook_phase existed emit records without it;
// detect that case (no interaction in the record has hook_phase set) and
// fall back to the legacy type-string heuristic so the viewer keeps working
// during a cross-component rollout. Once all agents are rebuilt, the
// fallback branch is dead weight — but cheap and safe to keep.
function classifyInteractions(interactions) {
    const anyPhaseSet = interactions.some(i => i.hook_phase);
    const isPreHook = anyPhaseSet
        ? (i) => i.hook_phase === 'pre'
        : (i) => (i.type?.startsWith('user_memory_recall_') ||
                  i.type === 'user_memory_enrichment_injected' ||
                  i.type === 'conversation_history_prepare' ||
                  i.type === 'conversation_history_compaction' ||
                  i.type === 'activity_compaction' ||
                  i.type === 'activity_compaction_incremental');
    const isPostHook = anyPhaseSet
        ? (i) => i.hook_phase === 'post'
        : (i) => ((i.type?.startsWith('user_memory_') &&
                   !i.type?.startsWith('user_memory_recall_') &&
                   i.type !== 'user_memory_enrichment_injected') ||
                  i.type === 'event_summarization');
    const isOrchestrationCall = anyPhaseSet
        ? (i) => !i.hook_phase
        : (i) => !isPreHook(i) && !isPostHook(i);
    return { isPreHook, isPostHook, isOrchestrationCall };
}

function pipelineHookPhaseLabel(phase) {
    const labels = {
        before_planning: 'BeforePlanning',
        after_planning: 'AfterPlanning',
        after_execution: 'AfterExecution',
        after_synthesis: 'AfterSynthesis',
    };
    return labels[phase] || String(phase || 'Unknown').replaceAll('_', ' ');
}

function formatPipelineHookDuration(durationNS) {
    const microseconds = Math.round(Math.max(0, Number(durationNS) || 0) / 1000);
    if (microseconds < 1000) return `${microseconds}µs`;
    const milliseconds = microseconds / 1000;
    return milliseconds < 10 ? `${milliseconds.toFixed(2)}ms` : formatDuration(Math.round(milliseconds));
}

function pipelineHookStatus(status) {
    switch (status) {
    case 'succeeded':
        return { css: 'completed', label: '✓ Invocation succeeded', text: 'INVOCATION SUCCEEDED', color: 'var(--accent-green)' };
    case 'failed':
        return { css: 'failed', label: '✗ Invocation failed', text: 'INVOCATION FAILED', color: 'var(--accent-red)' };
    case 'skipped':
        return { css: 'skipped', label: '– Invocation skipped', text: 'INVOCATION SKIPPED', color: 'var(--text-muted)' };
    default:
        return { css: 'pending', label: '? Invocation unknown', text: 'INVOCATION UNKNOWN', color: 'var(--accent-orange)' };
    }
}

function pipelineHookEffectStatus(status) {
    switch (status) {
    case 'succeeded':
        return { css: 'completed', label: 'Producer reports success', color: 'var(--accent-green)' };
    case 'partial':
        return { css: 'partial', label: 'Producer reports partial', color: 'var(--accent-orange)' };
    case 'failed':
        return { css: 'failed', label: 'Producer reports failure', color: 'var(--accent-red)' };
    case 'skipped':
        return { css: 'skipped', label: 'Producer reports skipped', color: 'var(--text-muted)' };
    case 'pending':
        return { css: 'pending', label: 'Producer reports pending', color: 'var(--accent-orange)' };
    default:
        return { css: 'pending', label: 'Effect status unknown', color: 'var(--accent-orange)' };
    }
}

function renderPipelineHookEffects(effects) {
    if (!effects?.length) {
        return `<div class="pipeline-hook-effects-empty">No structured effects were reported by this invocation.</div>`;
    }
    return `<div class="pipeline-hook-effects">
        <div class="pipeline-hook-effects-heading">Reported effects</div>
        ${effects.map(effect => {
            const status = pipelineHookEffectStatus(effect.status);
            const hasData = effect.data !== undefined && effect.data !== null;
            return `<article class="pipeline-hook-effect ${status.css}">
                <div class="pipeline-hook-effect-header">
                    <div>
                        <span class="pipeline-hook-effect-name">${escapeHtml(effect.name || effect.effect_id || 'Unnamed effect')}</span>
                        <span class="pipeline-hook-effect-id">${escapeHtml(effect.effect_id || '')}${effect.schema_version ? ` · schema v${Number(effect.schema_version)}` : ''}</span>
                    </div>
                    <span class="pipeline-hook-effect-status ${status.css}">${status.label}</span>
                </div>
                ${effect.summary ? `<div class="pipeline-hook-effect-summary">${escapeHtml(effect.summary)}</div>` : ''}
                <div class="pipeline-hook-effect-meta">
                    ${effect.started_at ? `<span>Started ${escapeHtml(formatDateTime(effect.started_at))}</span>` : ''}
                    ${effect.duration ? `<span>${formatPipelineHookDuration(effect.duration)}</span>` : ''}
                </div>
                ${effect.error ? `<div class="pipeline-hook-effect-error"><span>Effect failure</span>${escapeHtml(effect.error)}</div>` : ''}
                ${hasData ? `<details class="pipeline-hook-effect-values">
                    <summary>View captured values</summary>
                    <div class="json-container"><pre class="json-view pipeline-hook-effect-json">${syntaxHighlightJson(effect.data)}</pre></div>
                </details>` : ''}
            </article>`;
        }).join('')}
    </div>`;
}

function pipelineHookIdentity(hook) {
    return JSON.stringify([hook.hook_name, hook.phase, hook.sequence, hook.plan_phase, hook.started_at]);
}

function pipelineHookDetailTab(hook) {
    return isPreExecutionHook(hook) ? 'dag-pre' : isPostExecutionHook(hook) ? 'dag-post' : 'dag-raw';
}

async function openHookDetails(target, requestID, version) {
    const ownsView = () => selected?.request_id === requestID && selectionVersion === version;
    if (!ownsView()) return false;
    if (!await setDagDetailTab(target.tab) || !ownsView() || activeTab !== target.tab) return false;
    if (target.tab === 'dag-raw') return true;
    const hooks = (selected.pipeline_hooks || []).filter(target.tab === 'dag-pre' ? isPreExecutionHook : isPostExecutionHook);
    const matches = hooks.map((hook, index) => ({ hook, index }))
        .filter(({ hook }) => pipelineHookIdentity(hook) === target.identity);
    if (matches.length !== 1) {
        const notice = document.createElement('div');
        notice.className = 'dag-data-warning';
        notice.setAttribute('role', 'status');
        notice.textContent = matches.length ? 'This hook invocation cannot be distinguished from another retained record.'
            : 'This hook invocation is unavailable in the latest retained evidence.';
        document.getElementById('dagDetailContent').prepend(notice);
        return false;
    }
    const card = document.getElementById(`dag-hook-details-${matches[0].index}`);
    if (!card) return false;
    card.focus({ preventScroll: true });
    card.scrollIntoView({ block: 'start' });
    return true;
}

function pipelineHookNodeData(hook, id) {
    const phase = pipelineHookPhaseLabel(hook.phase);
    const hookName = hook.hook_name || 'Unknown hook';
    return {
        id,
        label: `🪝 ${phase}\n${hookName}\n${pipelineHookConsequence(hook).nodeLabel}`,
        nodeType: 'pipeline_hook',
        hookName,
        hookPhase: hook.phase || '',
        hookStatus: hook.status || '',
        durationNS: hook.duration || 0,
        sequence: hook.sequence || 0,
        planPhase: hook.plan_phase || 0,
        startedAt: hook.started_at || '',
        effectCount: hook.effects?.length || 0,
        failedEffectCount: (hook.effects || []).filter(effect => effect.status === 'failed').length,
        hookDecision: hook.decision ? {
            action: hook.decision.action,
            reason: hook.decision.reason,
            failure_policy: hook.decision.failure_policy,
        } : null,
        hookAction: pipelineHookConsequence(hook).action,
        hookIdentity: pipelineHookIdentity(hook),
        hookDetailTab: pipelineHookDetailTab(hook),
    };
}

function renderHookPopupSummary(data) {
    const status = pipelineHookStatus(data.hookStatus);
    const consequence = pipelineHookConsequence({ status: data.hookStatus, decision: data.hookDecision });
    const statusText = { completed: '✓ Succeeded', failed: '✗ Failed', skipped: '– Skipped', pending: '? Unknown' }[status.css];
    const order = Number.isInteger(data.sequence) && data.sequence > 0 ? `#${data.sequence} in this stage` : 'Order not recorded';
    return `<div class="dag-hook-popup-summary">
        <div class="dag-hook-popup-heading">
            <span class="dag-hook-popup-name">🪝 ${escapeHtml(truncateText(data.hookName || 'Unknown hook', 96))}</span>
            <span class="dag-step-status ${status.css}" aria-label="${escapeHtmlAttribute(status.label)}" title="${escapeHtmlAttribute(status.label)}">${statusText}</span>
        </div>
        <div class="dag-hook-popup-field">
            <div class="dag-hook-popup-label">Stage</div>
            <span class="dag-hook-popup-stage">${escapeHtml(pipelineHookPhaseLabel(data.hookPhase).replace(/([a-z])([A-Z])/g, '$1 $2'))}</span>
        </div>
        <div class="dag-hook-popup-field">
            <div class="dag-hook-popup-label">Pipeline outcome</div>
            <div class="dag-hook-popup-outcome">${escapeHtml(consequence.summary)}</div>
        </div>
        <div class="dag-hook-popup-metrics">
            <span title="Invocation duration">⏱ ${formatPipelineHookDuration(data.durationNS)}</span>
            <span class="dag-hook-popup-order">${order}</span>
            ${data.planPhase ? `<span class="dag-hook-popup-phase">Plan phase ${Number(data.planPhase)}</span>` : ''}
            ${data.effectCount ? `<span class="dag-hook-popup-effect-count${data.failedEffectCount ? ' has-failure' : ''}">${data.effectCount} reported effect${data.effectCount === 1 ? '' : 's'}${data.failedEffectCount ? ` · ${data.failedEffectCount} reported failure${data.failedEffectCount === 1 ? '' : 's'}` : ''}</span>` : ''}
        </div>
    </div>`;
}

function renderPipelineHookDecision(hook) {
    const consequence = pipelineHookConsequence(hook);
    return `<section class="pipeline-hook-decision">
        <span class="dag-step-label">Pipeline consequence</span>
        <div class="pipeline-hook-decision-summary">${escapeHtml(consequence.summary)}</div>
        ${hook.decision ? `<details class="pipeline-hook-decision-details">
            <summary>Decision details</summary>
            <div class="dag-step-info">
                <span class="dag-step-label">Failure policy</span><span class="dag-step-value">${escapeHtml(consequence.policy || 'Not recorded')}</span>
                <span class="dag-step-label">Action</span><span class="dag-step-value">${escapeHtml(consequence.action || 'Not recorded')}</span>
                <span class="dag-step-label">Reason</span><span class="dag-step-value">${escapeHtml(consequence.reason || 'Not recorded')}</span>
            </div>
        </details>` : ''}
    </section>`;
}

function renderPipelineHookObservations(hooks) {
    if (!hooks?.length) return '';
    return `
        <section class="pipeline-hook-section">
            <div class="pipeline-hook-section-heading">
                <span>🪝 Pipeline hook executions</span>
                <span class="pipeline-hook-section-count">${hooks.length} recorded invocation${hooks.length === 1 ? '' : 's'}</span>
            </div>
            <div class="pipeline-hook-card-list">
                ${hooks.map((hook, index) => {
                    const status = pipelineHookStatus(hook.status);
                    const startedAt = hook.started_at ? formatDateTime(hook.started_at) : 'Unavailable';
                    const sequence = Number(hook.sequence);
                    const order = Number.isInteger(sequence) && sequence > 0
                        ? `#${sequence} in this phase`
                        : 'Unavailable';
                    const phaseDetail = hook.plan_phase
                        ? `<span class="dag-step-label">Plan phase</span><span class="dag-step-value">${Number(hook.plan_phase)}</span>`
                        : '';
                    const errorDetail = hook.error
                        ? `<span class="dag-step-label">Failure</span><span class="dag-step-value pipeline-hook-error">${escapeHtml(hook.error)}</span>`
                        : '';
                    return `
                        <article id="dag-hook-details-${index}" tabindex="-1" class="dag-step-card pipeline-hook-card ${status.css}">
                            <div class="dag-step-header">
                                <div class="dag-step-title pipeline-hook-title">
                                    <span class="pipeline-hook-kind">🪝 Hook</span>
                                    <span class="pipeline-hook-name">${escapeHtml(hook.hook_name || 'Unknown hook')}</span>
                                    <span class="dag-step-id">${escapeHtml(pipelineHookPhaseLabel(hook.phase))}</span>
                                </div>
                                <span class="dag-step-status ${status.css}">${status.label}</span>
                            </div>
                            <div class="dag-step-body">
                                <div class="dag-step-info pipeline-hook-info">
                                    <span class="dag-step-label">Order</span>
                                    <span class="dag-step-value">${order}</span>
                                    ${phaseDetail}
                                    <span class="dag-step-label">Duration</span>
                                    <span class="dag-step-value">${formatPipelineHookDuration(hook.duration)}</span>
                                    <span class="dag-step-label">Started</span>
                                    <span class="dag-step-value">${escapeHtml(startedAt)}</span>
                                    ${errorDetail}
                                </div>
                                ${renderPipelineHookDecision(hook)}
                                ${renderPipelineHookEffects(hook.effects || [])}
                            </div>
                        </article>`;
                }).join('')}
            </div>
        </section>`;
}

// ---------------------------------------------------------------------------
// LLM call identification primitives
// ---------------------------------------------------------------------------
// Derived entirely from fields LLMInteraction already carries — no Go-side
// schema change needed. The same string is rendered in both the DAG popup
// and the LLM Calls card, giving operators a stable visual correlator
// when several calls share a type (e.g. multi-phase planning).
//
// Format, left-to-right as "what · where · modifier":
//   plan_generation · P1                 → phased planning
//   plan_generation · P1 · retry-2       → retry within a phase
//   synthesis_streaming · P2             → phased synthesis
//   error_analysis · step-3              → step-scoped (DAG llm_step nodes)
//   user_memory_extraction · post        → hook-phase without phase_number
//
// This is a display label; navigation uses recorded call facts separately so
// a changed array order cannot send an operator to a different call.
function callLabel(i) {
    if (!i || !i.type) return '';
    const parts = [i.type];
    if (i.phase_number) parts.push(`P${i.phase_number}`);
    if (i.step_id) parts.push(i.step_id);
    if (i.attempt && i.attempt > 1) parts.push(`retry-${i.attempt}`);
    if (i.hook_phase && !i.phase_number) parts.push(i.hook_phase);
    return parts.join(' · ');
}

function llmCallIdentity(call) {
    if (!call) return '';
    // Exact recorded boundary facts, not an array position or display label.
    // If legacy records collide, navigation reports ambiguity instead of
    // selecting another call. Prompt/response payloads stay out of graph data.
    return JSON.stringify([call.timestamp, call.type, call.trace_id, call.step_id,
        call.phase_number, call.attempt, call.source_component, call.hook_phase,
        call.model, call.provider]);
}

function llmCallDetailTab(call, interactions = selected?.llm_interactions || []) {
    const { isPreHook, isPostHook } = classifyInteractions(interactions);
    return isPreHook(call) ? 'dag-pre' : isPostHook(call) ? 'dag-post' : 'dag-llm';
}

async function openLLMCallDetails(target, requestID, version) {
    const ownsView = () => selected?.request_id === requestID && selectionVersion === version;
    if (!ownsView()) return false;
    if (!await setDagDetailTab(target.tab) || !ownsView() || activeTab !== target.tab) return false;
    const matches = (selected.llm_interactions || []).map((call, index) => ({ call, index }))
        .filter(({ call }) => llmCallIdentity(call) === target.identity && llmCallDetailTab(call) === target.tab);
    if (matches.length !== 1) {
        const message = matches.length ? 'This call cannot be distinguished from another retained LLM record.'
            : 'This LLM call is unavailable in the latest retained evidence.';
        const notice = document.createElement('div');
        notice.className = 'dag-data-warning';
        notice.setAttribute('role', 'status');
        notice.textContent = message;
        document.getElementById('dagDetailContent').prepend(notice);
        return false;
    }
    const index = matches[0].index;
    llmCardBatch?.reveal(index);
    const card = document.getElementById(`dag-llm-call-${index}`);
    if (!card) return false;
    card.focus({ preventScroll: true });
    card.scrollIntoView({ block: 'start' });
    return true;
}

// Shared constructor for the `data` payload of any DAG node backed by an
// LLMInteraction. Routing the four LLM-backed node types (llm_call,
// llm_step, memory_llm, agent_llm) through this function guarantees
// every popup can render `callLabel` without the construction site
// remembering to stash it — if a future 5th node type is added, it
// inherits traceability by using this helper too.
//
// Uses `call._callLabel` when present (annotated by annotateCallLabels
// for iterative call types like tiered_selection where the base label
// collides across iterations). Falls back to the plain base label if the
// annotation pass hasn't run yet — safe default.
function buildLLMBackedNodeData(call, extras) {
    return Object.assign({
        callLabel: call._callLabel || callLabel(call),
        llmType: call.type || '',
        model: call.model || '',
        duration: call.duration_ms || 0,
        tokens: (call.prompt_tokens || 0) + (call.completion_tokens || 0),
        llmCallIdentity: llmCallIdentity(call),
        llmDetailTab: llmCallDetailTab(call),
    }, extras || {});
}

// annotateCallLabels stamps each interaction with `_callLabel` — the
// globally-disambiguated label that both DAG popups and LLM-call cards
// render. Iterative call types (tiered_selection runs 1..N times per
// phase; retries may land without `attempt` set) produce identical base
// labels; without a shared disambiguation pass, popup and card disagree
// and operators can't correlate them.
//
// Called once per selected record load, before any DAG construction or
// card rendering. Both the DAG node construction (via
// buildLLMBackedNodeData) and the card render (via renderSingleLLMCard's
// default) read `_callLabel` — single source of truth, suffix numbers
// match on both sides.
//
// Annotation is in-place. Filters/sorts on `selected.llm_interactions`
// preserve object references, so the annotation propagates to every
// downstream consumer without extra plumbing.
function annotateCallLabels(interactions) {
    if (!Array.isArray(interactions)) return;
    const counts = new Map();
    for (const i of interactions) {
        const base = callLabel(i);
        if (base) counts.set(base, (counts.get(base) || 0) + 1);
    }
    const seq = new Map();
    for (const i of interactions) {
        const base = callLabel(i);
        if (!base) { i._callLabel = ''; continue; }
        if ((counts.get(base) || 0) < 2) {
            i._callLabel = base;
        } else {
            const n = (seq.get(base) || 0) + 1;
            seq.set(base, n);
            i._callLabel = `${base} #${n}`;
        }
    }
}

const DAG_CACHE_MAX = 3;

// ---------------------------------------------------------------------------
// Lifecycle
// ---------------------------------------------------------------------------

// Tracks every event listener registered by bindDelegatedEvents() so destroy()
// can remove them on view-switch. app.js calls init() every time the user
// navigates back to this view from another top-level nav tab; without this
// removal, listeners would stack on the persistent DOM elements
// (dagDetailContent, dagDetailPanel, dagListPanel, dagTableBody, dagSearchInput),
// causing every click to fire twice (or four times, etc.) and making
// expand/collapse behave intermittently — a click on "View Request" would
// toggle display N times and end up where it started whenever N is even.
//
// Matches the listener-tracking pattern used by llm-debug.js, hitl.js, and
// memory.js — dag.js previously did NOT clean up its listeners in destroy().
let dagBoundListeners = [];

function addTrackedListener(el, type, fn) {
    if (!el) return;
    el.addEventListener(type, fn);
    dagBoundListeners.push({ el, type, fn });
}

function unbindAllListeners() {
    dagBoundListeners.forEach(({ el, type, fn }) => el.removeEventListener(type, fn));
    dagBoundListeners = [];
}

export function init() {
    updateGroupingModeControls();
    showExecutionListMode();
    bindDelegatedEvents();
    fetchExecutions();
}

/** Open an explicit execution independently of list/timeline availability. */
export function openExecution(requestID) {
    if (typeof requestID !== 'string' || !executionDeepLinkRequestID(new URLSearchParams({ view: 'dag', request_id: requestID }).toString())) {
        return Promise.resolve();
    }
    return selectExecution(requestID);
}

function updateExecutionDeepLink(requestID) {
    if (typeof location === 'undefined' || typeof history === 'undefined') return;
    if (!executionDeepLinkRequestID(location.search)) return;
    try {
        const url = new URL(location.href);
        url.searchParams.set('request_id', requestID);
        history.replaceState(history.state, '', url);
    } catch (_) {
        // Read-only/embedded history support must not block detail loading.
    }
}

export async function refresh({ manual = false } = {}) {
    // Timer refresh is summary-only. Explicit operator refresh may reload the
    // currently requested evidence and current conversation page.
    const tasks = [];
    if (listPanelMode === 'list' && !groupedLoading) tasks.push(fetchExecutions({ preserveExpansion: true }));
    if (manual && selected) tasks.push(selectExecution(selected.request_id, { restoreTab: activeTab, restoreViewMode: viewMode }));
    if (manual && listPanelMode === 'timeline' && activeConversationID) {
        tasks.push(loadConversationTimeline(activeConversationID, selected?.request_id, { force: true }));
    }
    await Promise.all(tasks);
}

function isDagAccessDenied(error) {
    return error?.status === 401 || error?.status === 403;
}

function clearDagAccessState(error) {
    cleanupDAGLegend();
    llmCardBatch?.dispose();
    llmCardBatch = null;
    selectionVersion++;
    listLoad.cancel();
    detailLoad.cancel();
    sectionLoad.cancel();
    timelineLoad.cancel();
    conversationTitleLoad.cancel();
    conversationTitleCache.clear();
    lineageLoader.destroy();
    resetParentExecutionLookup();
    selected = null;
    selectedOverview = null;
    selectedSection = 'overview';
    sectionPending = false;
    sectionError = '';
    invokingStepTarget = null;
    executions = [];
    groupedExecutionGroups = [];
    groupedNextCursor = '';
    groupedLoading = false;
    listPageCursor = '';
    listPageNumber = 1;
    listPageCursors.clear();
    listQueryKey = '';
    expandedGroupKeys.clear();
    groupMatchPages.clear();
    conversationCache.clear();
    invocationServicesVersion++;
    invocationServices = [];
    invocationServicesPending = null;
    for (const entry of dagCache.values()) {
        entry.cy.destroy();
        if (entry.container.parentNode) entry.container.parentNode.removeChild(entry.container);
    }
    dagCache.clear();
    cyInstance = null;
    document.querySelectorAll('.node-popup').forEach(popup => popup.remove());
    const message = error.message || 'Execution data access is unavailable.';
    const detail = document.getElementById('dagDetailContent');
    if (detail) detail.innerHTML = `<div class="empty-detail" role="alert">${escapeHtml(message)}</div>`;
    const title = document.getElementById('dagDetailTitle');
    if (title) title.textContent = 'Execution access unavailable';
    const timeline = document.getElementById('dagTimelineContent');
    if (timeline) timeline.innerHTML = `<div class="empty-detail" role="alert">${escapeHtml(message)}</div>`;
    const conversationID = document.getElementById('dagTimelineConversationID');
    if (conversationID) conversationID.textContent = '';
    const timelineSummary = document.getElementById('dagTimelineSummary');
    if (timelineSummary) timelineSummary.textContent = 'Conversation access unavailable';
    for (const id of ['dagTimelinePrevious', 'dagTimelineNext']) {
        const button = document.getElementById(id);
        if (button) button.disabled = true;
    }
    renderExecutionList();
    updateDagStats([]);
    updateGroupedLoadMoreControl();
    updateDetailTabs();
    setDagListDiagnostic(message, 'error');
    setTimelineDiagnostic(message);
    hideLoading(document.getElementById('dagDetailPanel'));
    hideLoading(document.getElementById('dagListPanel'));
}

export function destroy() {
    cleanupDAGLegend();
    llmCardBatch?.dispose();
    llmCardBatch = null;
    selectionVersion++;
    listLoad.cancel();
    detailLoad.cancel();
    sectionLoad.cancel();
    timelineLoad.cancel();
    conversationTitleLoad.cancel();
    conversationTitleCache.clear();
    selectedOverview = null;
    selectedSection = 'overview';
    sectionPending = false;
    sectionError = '';
    listPageCursor = '';
    listPageNumber = 1;
    listPageCursors.clear();
    listQueryKey = '';
    groupMatchPages.clear();
    listWindowFrom = '';
    listWindowTo = '';
    lineageLoader.destroy();
    invokingStepTarget = null;
    resetParentExecutionLookup();
    selected = null;
    const detailContent = document.getElementById('dagDetailContent');
    if (detailContent) detailContent.innerHTML = '<div class="empty-detail"><div class="empty-detail-icon">🔀</div><div>Select an execution to view its overview.</div></div>';
    const detailTitle = document.getElementById('dagDetailTitle');
    if (detailTitle) detailTitle.textContent = 'Select an execution';
    invocationServicesVersion++;
    invocationServices = [];
    invocationServicesPending = null;
    // Dismiss any open popups (they're on document.body, not inside the DAG container)
    document.querySelectorAll('.node-popup').forEach(p => p.remove());
    // Destroy all cached Cytoscape instances
    for (const entry of dagCache.values()) {
        entry.cy.destroy();
        if (entry.container.parentNode) entry.container.parentNode.removeChild(entry.container);
    }
    dagCache.clear();
    cyInstance = null;
    if (dagSearchTimer) {
        clearTimeout(dagSearchTimer);
        dagSearchTimer = null;
    }
    groupedExecutionGroups = [];
    groupedNextCursor = '';
    groupedLoading = false;
    expandedGroupKeys.clear();
    conversationCache.clear();
    activeConversationID = '';
    listPanelMode = 'list';
    timelineReturnFocus = null;
    // Remove every event listener registered by bindDelegatedEvents() so a
    // subsequent init() (when the user comes back to this view) starts clean.
    unbindAllListeners();
}

// ---------------------------------------------------------------------------
// Event delegation
// ---------------------------------------------------------------------------
//
// Handlers are extracted as named functions (not inline arrows) so that
// destroy() → unbindAllListeners() can pass the same function reference to
// removeEventListener. Inline closures cannot be removed and would leak.

function handleDagTableClick(e) {
    if (e.target.closest('button')) return;
    const row = e.target.closest('tr[data-request-id]');
    if (row) {
        selectExecution(row.dataset.requestId);
    }
}

function handleDagListPanelClick(e) {
    const lineageTarget = e.target.closest('[data-lineage-execution]');
    if (lineageTarget) {
        navigateExecutionLineage(lineageTarget);
        return;
    }
    const groupingBtn = e.target.closest('[data-grouping-mode]');
    if (groupingBtn) {
        setGroupingMode(groupingBtn.dataset.groupingMode);
        return;
    }
    const groupToggle = e.target.closest('[data-toggle-conversation-group]');
    if (groupToggle) {
        toggleConversationGroup(
            groupToggle.dataset.toggleConversationGroup,
            groupToggle.classList.contains('dag-group-title-btn')
                ? 'title'
                : 'expand',
        );
        return;
    }
    const matchPage = e.target.closest('[data-conversation-matches-page]');
    if (matchPage) {
        navigateConversationMatches(matchPage.dataset.conversationMatchesPage, matchPage.dataset.groupKey);
        return;
    }
    const openTimeline = e.target.closest('[data-open-conversation]');
    if (openTimeline) {
        openConversationTimeline(
            openTimeline.dataset.openConversation,
            openTimeline,
        );
        return;
    }
    const closeTimeline = e.target.closest('[data-close-conversation-timeline]');
    if (closeTimeline) {
        closeConversationTimeline();
        return;
    }
    if (e.target.closest('[data-previous-execution-page]')) {
        if (!groupedLoading) void navigateExecutionPage('previous');
        return;
    }
    const pageButton = e.target.closest('[data-execution-page]');
    if (pageButton) {
        void jumpToExecutionPage(Number(pageButton.dataset.executionPage));
        return;
    }
    const loadMore = e.target.closest('[data-load-more-groups]');
    if (loadMore) {
        if (!groupedLoading && groupedNextCursor) void navigateExecutionPage('next');
        return;
    }
    const timelineExecution = e.target.closest('[data-view-timeline-execution]');
    if (timelineExecution) {
        void selectExecution(
            timelineExecution.dataset.viewTimelineExecution,
            { restoreTimelineActionFocus: true },
        );
        return;
    }
    const conversationPage = e.target.closest('[data-conversation-page]');
    if (conversationPage) {
        void navigateConversationPage(conversationPage.dataset.conversationPage, conversationPage.dataset.conversationId);
        return;
    }
    const timelineNav = e.target.closest('[data-timeline-nav]');
    if (timelineNav) {
        navigateConversationTurn(timelineNav.dataset.timelineNav);
        return;
    }
    const copyConversation = e.target.closest('[data-copy-conversation-id]');
    if (copyConversation) {
        void copyConversationID(copyConversation);
        return;
    }
    const flatFallback = e.target.closest('[data-use-flat-list]');
    if (flatFallback) {
        setGroupingMode('flat');
        return;
    }
    const filterBtn = e.target.closest('.filter-btn[data-filter]');
    if (filterBtn) {
        setDagFilter(filterBtn.dataset.filter);
        return;
    }
    const sortTh = e.target.closest('th[data-sort]');
    if (sortTh) {
        sortDagTable(sortTh.dataset.sort);
    }
}

function handleDagListPanelKeydown(e) {
    if (e.repeat || (e.key !== 'Enter' && e.key !== ' ')) return;
    if (e.target.closest('[data-lineage-execution]')) return;
    const timelineExecution = e.target.closest(
        '[data-view-timeline-execution][role="button"]',
    );
    if (!timelineExecution) return;
    e.preventDefault();
    void selectExecution(
        timelineExecution.dataset.viewTimelineExecution,
        { restoreTimelineActionFocus: true },
    );
}

function handleDagDetailPanelClick(e) {
    const tab = e.target.closest('.detail-tab[data-tab]');
    if (tab) {
        setDagDetailTab(tab.dataset.tab);
    }
}

// Unified delegated click handler covering every expandable element across
// all detail tabs (Step Details, LLM Calls, Pre-Execution, Post-Execution,
// HITL). Bound against the persistent dagDetailContent element. Each render
// function used to bind its own listener via container.addEventListener,
// which stacked handlers and caused expand/collapse to behave intermittently.
function handleLineageAction(e) {
    const lineageTarget = e.target.closest('[data-lineage-execution]');
    if (lineageTarget) {
        e.preventDefault();
        navigateExecutionLineage(lineageTarget);
        return true;
    }
    if (e.target.closest('[data-refresh-execution-lineage]')) {
        if (!lineageLoader.pending()) discoverExecutionLineage({ force: true });
        return true;
    }
    if (e.target.closest('[data-open-parent-execution]')) {
        setDagDetailTab('dag-parent');
        return true;
    }
    if (e.target.closest('[data-refresh-parent-execution]')) {
        loadParentExecution({ force: true });
        renderParentExecution(document.getElementById('dagDetailContent'));
        return true;
    }
    return false;
}

function handleDagDetailContentClick(e) {
    if (handleLineageAction(e)) return;
    if (e.target.closest('[data-retry-execution-section]')) {
        void loadSelectedSection(activeDetailSection(), { force: true });
        return;
    }
    if (e.target.closest('[data-refresh-execution-overview]')) {
        if (selected) void selectExecution(selected.request_id);
        return;
    }
    // Toggle "Show more" on long step instructions
    const instrBtn = e.target.closest('[data-toggle-instruction]');
    if (instrBtn) {
        toggleInstruction(instrBtn.dataset.toggleInstruction);
        return;
    }
    // Toggle a step section (View Request / Resolution / Result Trim / View Response)
    const sectionHeader = e.target.closest('[data-toggle-section]');
    if (sectionHeader) {
        toggleStepSection(sectionHeader.dataset.toggleSection);
        return;
    }
    // Copy LLM prompt or response — must be checked BEFORE the toggle-llm
    // branch so a click on the Copy button doesn't also toggle the section.
    const copyBtn = e.target.closest('[data-copy-llm]');
    if (copyBtn) {
        e.stopPropagation();
        copyDAGLLMContent(parseInt(copyBtn.dataset.copyLlm), copyBtn.dataset.copyType, { target: copyBtn });
        return;
    }
    // Toggle an LLM card section (View Prompt / View Response) — works for
    // cards rendered by LLM Calls AND by Pre-Execution / Post-Execution tabs.
    const toggleHeader = e.target.closest('[data-toggle-llm]');
    if (toggleHeader) {
        toggleLLMInteraction(toggleHeader.dataset.toggleLlm, toggleHeader.dataset.llmType);
        return;
    }
    // Toggle a HITL checkpoint section (Plan / Resolved Parameters / etc.)
    // in the DAG → HITL tab.
    const hitlHeader = e.target.closest('[data-toggle-hitl]');
    if (hitlHeader) {
        toggleHITLSection(hitlHeader.dataset.toggleHitl, hitlHeader.dataset.hitlSection);
        return;
    }
    // Navigate between immutable interruption and resume execution records.
    const hitlExecution = e.target.closest('[data-hitl-execution], [data-resume-sibling]');
    if (hitlExecution) {
        e.preventDefault();
        const targetID = hitlExecution.dataset.hitlExecution || hitlExecution.dataset.resumeSibling;
        if (targetID) {
            selectExecution(targetID);
        }
        return;
    }
}

function handleDagSearchInput() {
    if (dagSearchTimer) clearTimeout(dagSearchTimer);
    dagSearchTimer = setTimeout(() => {
        dagSearchTimer = null;
        void fetchExecutions();
    }, 250);
}

function bindDelegatedEvents() {
    addTrackedListener(document.getElementById('dagTableBody'), 'click', handleDagTableClick);
    addTrackedListener(document.getElementById('dagListPanel'), 'click', handleDagListPanelClick);
    addTrackedListener(document.getElementById('dagListPanel'), 'keydown', handleDagListPanelKeydown);
    addTrackedListener(document.getElementById('dagDetailPanel'), 'click', handleDagDetailPanelClick);
    addTrackedListener(document.getElementById('dagDetailContent'), 'click', handleDagDetailContentClick);
    addTrackedListener(document.getElementById('dagSearchInput'), 'input', handleDagSearchInput);
    addTrackedListener(document.getElementById('dagTimeRange'), 'change', () => {
        listTimeRange = document.getElementById('dagTimeRange').value;
        void fetchExecutions();
    });
    addTrackedListener(document.getElementById('dagPageJump'), 'change', event => {
        void jumpToExecutionPage(Number(event.target.value));
    });
}

// ---------------------------------------------------------------------------
// Data fetching
// ---------------------------------------------------------------------------

async function fetchExecutions({ preserveExpansion = false } = {}) {
    await loadExecutionPage({ reset: !preserveExpansion, preserveExpansion });
}

async function loadFlatExecutions({ withLoadingOverlay = true } = {}) {
    return loadExecutionPage({ reset: true, withLoadingOverlay });
}

async function loadExecutionGroups({ reset = true, preserveExpansion = false, withLoadingOverlay = true } = {}) {
    if (!reset) return navigateExecutionPage('next');
    return loadExecutionPage({ reset, preserveExpansion, withLoadingOverlay });
}

async function navigateExecutionPage(direction) {
    if (groupedLoading) return;
    if (direction === 'previous') return jumpToExecutionPage(listPageNumber - 1);
    if (direction !== 'next' || !groupedNextCursor) return;
    await loadExecutionPage({ cursor: groupedNextCursor, navigation: 'next', targetPage: listPageNumber + 1 });
}

async function jumpToExecutionPage(pageNumber) {
    if (groupedLoading || !Number.isSafeInteger(pageNumber) || pageNumber === listPageNumber || !listPageCursors.has(pageNumber)) return;
    await loadExecutionPage({ cursor: listPageCursors.get(pageNumber), navigation: 'jump', targetPage: pageNumber });
}

async function loadExecutionPage({ reset = false, preserveExpansion = false, cursor = listPageCursor, navigation = '', targetPage = listPageNumber, withLoadingOverlay = listPanelMode === 'list' } = {}) {
    const listPanel = document.getElementById('dagListPanel');
    const paginationHadFocus = !!document.activeElement?.closest?.('.dag-pagination');
    const ticket = listLoad.begin();
    const mode = groupingMode;
    const search = document.getElementById('dagSearchInput')?.value.trim() || '';
    const queryKey = JSON.stringify([mode, search, filter, sortDirection, listTimeRange]);
    // Query controls may change before a debounced search starts. A timer or
    // retry must never combine those controls with the previous query's cursor.
    if (queryKey !== listQueryKey) {
        reset = true;
        navigation = '';
        preserveExpansion = false;
    }
    listQueryKey = queryKey;
    if (reset) {
        conversationTitleLoad.cancel();
        cursor = '';
        listPageCursor = '';
        listPageNumber = 1;
        targetPage = 1;
        listPageCursors.clear();
        groupedNextCursor = '';
        executions = [];
        groupedExecutionGroups = [];
        groupMatchPages.clear();
        if (!preserveExpansion) expandedGroupKeys.clear();
        renderExecutionList();
        updateDagStats([]);
    }
    if (!cursor && !navigation) targetPage = 1;
    // Keep a refresh's proposed bounds private until its page succeeds. The
    // retained page's cursor is bound to its existing window on the backend.
    let windowFrom = listWindowFrom;
    let windowTo = listWindowTo;
    if (reset || (!cursor && !navigation)) {
        const hours = { '1h': 1, '24h': 24, '72h': 72 }[listTimeRange];
        const now = Date.now();
        windowFrom = hours ? new Date(now - hours * 3600000).toISOString() : '';
        windowTo = hours ? new Date(now).toISOString() : '';
    }
    groupedLoading = true;
    updateGroupedLoadMoreControl();
    if (withLoadingOverlay) showLoading(listPanel, 'Loading execution summaries…');
    const query = new URLSearchParams({
        limit: String(DAG_GROUP_PAGE_SIZE),
        q: search,
        status: filter,
        sort: 'created_at',
        direction: sortDirection,
    });
    if (windowFrom) query.set('from', windowFrom);
    if (windowTo) query.set('to', windowTo);
    if (mode === 'grouped') query.set('group_conversations', 'true');
    if (cursor) query.set('cursor', cursor);
    try {
        const { data } = await fetchAPI(`/api/executions?${query.toString()}`, { signal: ticket.signal });
        if (!listLoad.current(ticket) || mode !== groupingMode) return;
        if (mode === 'grouped' && !Array.isArray(data.groups)) throw new Error('This server does not support bounded conversation grouping.');
        const nextGroups = mode === 'grouped' ? data.groups : [];
        const nextExecutions = mode === 'grouped' ? flattenGroupedExecutions(nextGroups)
            : Array.isArray(data.executions) ? data.executions : [];
        // Page numbers belong to this query and time window. Publish them only
        // after a successful read; failed navigation retains the displayed page.
        if (windowFrom !== listWindowFrom || windowTo !== listWindowTo) listPageCursors.clear();
        const nextCursor = data.next_cursor || '';
        if (!nextCursor || (listPageCursors.has(targetPage + 1) && listPageCursors.get(targetPage + 1) !== nextCursor)) {
            // Snapshot updates may change a page boundary. Later visited pages
            // must be rediscovered rather than offered with stale positions.
            for (const page of listPageCursors.keys()) if (page > targetPage) listPageCursors.delete(page);
        }
        listWindowFrom = windowFrom;
        listWindowTo = windowTo;
        listPageNumber = targetPage;
        listPageCursors.set(targetPage, cursor);
        if (listPageCursors.size > CURSOR_HISTORY_MAX) listPageCursors.delete(Math.min(...listPageCursors.keys()));
        listPageCursor = cursor;
        if (!preserveExpansion) {
            expandedGroupKeys.clear();
            groupMatchPages.clear();
        }
        groupedNextCursor = nextCursor;
        groupedExecutionGroups = nextGroups;
        executions = nextExecutions;
        if (mode === 'grouped') {
            const activeGroups = new Set(groupedExecutionGroups.map(group => group.group_key));
            for (const key of groupMatchPages.keys()) if (!activeGroups.has(key)) groupMatchPages.delete(key);
        }
        renderExecutionList();
        updateDagStats(executions);
        if (mode === 'grouped') void loadConversationTitles(nextGroups);
        updateDagLastUpdated(data.timestamp);
        const messages = [];
        if (!executions.length && groupedNextCursor) messages.push('No matches in this scanned page. Continue search to inspect the next page.');
        if (data.partial) messages.push('This page has partial backend coverage; retained evidence is still available by request ID.');
        setDagListDiagnostic(messages.join(' '), data.partial ? 'warning' : 'info');
    } catch (error) {
        if (isDagAccessDenied(error)) { clearDagAccessState(error); return; }
        if (!listLoad.current(ticket) || error.name === 'AbortError') return;
        if (error.status === 409) {
            listPageCursor = '';
            listPageCursors.clear();
            groupedNextCursor = '';
            setDagListDiagnostic('This page moved or expired. Refresh to restart browsing from the latest summaries.', 'warning');
        } else setDagListDiagnostic(`Execution summaries could not be loaded. ${error.message || 'Refresh to try again.'}`, 'error');
    } finally {
        if (listLoad.current(ticket)) {
            groupedLoading = false;
            updateGroupedLoadMoreControl();
            if (withLoadingOverlay) hideLoading(listPanel);
            if (paginationHadFocus && document.activeElement === document.body) {
                // Replacing page buttons may remove the focused control. Keep
                // keyboard navigation in the footer unless focus moved elsewhere.
                const control = ['dagPageJump', 'dagLoadMore', 'dagPreviousPage']
                    .map(id => document.getElementById(id))
                    .find(element => element && !element.disabled);
                control?.focus({ preventScroll: true });
            }
        }
    }
}

function updateGroupedLoadMoreControl() {
    const next = document.getElementById('dagLoadMore');
    const previous = document.getElementById('dagPreviousPage');
    if (next) {
        next.classList.toggle('hidden', !groupedNextCursor);
        next.disabled = groupedLoading || !groupedNextCursor;
        const label = !executions.length ? 'Continue search' : 'Next page';
        next.title = label;
        next.setAttribute('aria-label', label);
    }
    if (previous) {
        const hasPrevious = listPageCursors.has(listPageNumber - 1);
        previous.classList.toggle('hidden', !hasPrevious);
        previous.disabled = groupedLoading || !hasPrevious;
    }
    const pages = [...listPageCursors.keys()].sort((a, b) => a - b);
    const jump = document.getElementById('dagPageJump');
    if (jump) {
        const choices = pages.length ? pages : [listPageNumber];
        jump.innerHTML = choices.map(page => `<option value="${page}"${page === listPageNumber ? ' selected' : ''}>Page ${page}</option>`).join('');
        jump.value = String(listPageNumber);
        jump.disabled = groupedLoading || pages.length < 2;
    }
    const numbers = document.getElementById('dagPageNumbers');
    if (numbers) {
        const visible = pages.filter((page, index) => index === 0 || index === pages.length - 1 || Math.abs(page - listPageNumber) <= 1);
        numbers.innerHTML = visible.map((page, index) => {
            const gap = index > 0 && page > visible[index - 1] + 1 ? '<span class="dag-page-gap" aria-hidden="true">…</span>' : '';
            const current = page === listPageNumber;
            return `${gap}<button class="dag-page-number${current ? ' active' : ''}" type="button" data-execution-page="${page}" aria-label="Page ${page}"${current ? ' aria-current="page"' : ''}${groupedLoading || current ? ' disabled' : ''}>${page}</button>`;
        }).join('');
    }
}

function updateDagStats(sourceExecutions = executions) {
    const unique = new Map();
    sourceExecutions.forEach(execution => {
        if (execution?.request_id) unique.set(execution.request_id, execution);
    });
    const values = Array.from(unique.values());
    const total = values.length;
    const successful = values.filter(e => executionStatus(e).state === 'completed').length;
    const rate = total > 0 ? Math.round((successful / total) * 100) : 0;

    document.getElementById('executionCount').textContent = total;
    document.getElementById('successRate').textContent = `${rate}%`;
}

function flattenGroupedExecutions(groups) {
    const result = [];
    (groups || []).forEach(group => {
        (group.turns || []).forEach(turn => {
            if (turn.execution) result.push(turn.execution);
            result.push(...(turn.related_executions || []));
        });
        result.push(...(group.orphans || []));
    });
    return result;
}

function updateDagLastUpdated(timestamp) {
    const value = timestamp ? new Date(timestamp) : new Date();
    const label = Number.isNaN(value.getTime()) ? new Date() : value;
    const target = document.getElementById('dagLastUpdated');
    if (target) target.textContent = `Last updated: ${label.toLocaleTimeString()}`;
}

function updateGroupedDiagnostics(data) {
    const messages = [];
    if (data.llm_enrichment_incomplete) {
        const labels = {
            execution_count_limit: 'the execution-count limit was reached',
            interaction_count_limit: 'an execution exceeded the interaction-count limit',
            interaction_byte_limit: 'an execution exceeded the cumulative interaction-byte limit',
            parse_failure: 'an interaction could not be decoded',
            backend_failure: 'the debug backend could not be read'
        };
        const reasons = [...new Set((data.llm_enrichment_reasons || []).map(reason => labels[reason.code]).filter(Boolean))];
        messages.push(`Optional LLM duration and call-count enrichment is incomplete${reasons.length ? ': ' + reasons.join('; ') : ''}. This does not establish missing execution records.`);
    }
    if (data.partial) {
        messages.push('This grouped result reached a safety bound; some groups may not be shown.');
    }
    if (messages.length > 0) {
        setDagListDiagnostic(messages.join(' '), 'warning', true);
    } else {
        setDagListDiagnostic('');
    }
}

function setDagListDiagnostic(message, tone = 'warning', offerFlat = false) {
    const target = document.getElementById('dagListDiagnostic');
    if (!target) return;
    target.classList.toggle('hidden', !message);
    target.classList.toggle('error', tone === 'error');
    target.classList.toggle('warning', tone === 'warning');
    target.innerHTML = message
        ? `<span>${escapeHtml(message)}</span>${offerFlat ? '<button type="button" data-use-flat-list>Use flat list</button>' : ''}`
        : '';
}

// ---------------------------------------------------------------------------
// Filters and sorting
// ---------------------------------------------------------------------------

function setDagFilter(f) {
    filter = f;
    document.querySelectorAll('#dagListPanel .filter-btn').forEach(btn => {
        btn.classList.toggle('active', btn.dataset.filter === f);
    });
    void fetchExecutions();
}

function sortDagTable(column) {
    if (column !== 'created_at') return;
    // Update sort indicators in the header
    document.querySelectorAll('#dagListPanel th.sortable').forEach(th => {
        th.classList.remove('asc', 'desc');
    });

    if (sortColumn === column) {
        sortDirection = sortDirection === 'asc' ? 'desc' : 'asc';
    } else {
        sortColumn = column;
        sortDirection = column === 'created_at' ? 'desc' : 'asc';
    }

    const th = document.querySelector(`#dagListPanel th[data-sort="${column}"]`);
    if (th) th.classList.add(sortDirection);

    void fetchExecutions();
}

// ---------------------------------------------------------------------------
// Execution list rendering
// ---------------------------------------------------------------------------

function renderExecutionList() {
    if (groupingMode === 'grouped') {
        renderServerExecutionGroups(groupedExecutionGroups);
        return;
    }
    renderLegacyExecutionList();
}

function setGroupingMode(mode) {
    if (mode !== 'grouped' && mode !== 'flat') return;
    if (groupingMode === mode) return;

    groupingMode = mode;
    listLoad.cancel();
    conversationTitleLoad.cancel();
    listPageCursor = '';
    listPageNumber = 1;
    listPageCursors.clear();
    storeGroupingPreference(mode);
    groupedLoading = false;
    groupedExecutionGroups = [];
    groupedNextCursor = '';
    expandedGroupKeys.clear();
    groupMatchPages.clear();
    setDagListDiagnostic('');
    updateGroupingModeControls();
    showExecutionListMode();
    void fetchExecutions();
}

function updateGroupingModeControls() {
    document.querySelectorAll('[data-grouping-mode]').forEach(button => {
        const active = button.dataset.groupingMode === groupingMode;
        button.classList.toggle('active', active);
        button.setAttribute('aria-pressed', String(active));
    });
    const hint = document.getElementById('dagGroupingHint');
    if (hint) {
        hint.textContent = groupingMode === 'grouped'
            ? `Groups from this ${DAG_GROUP_PAGE_SIZE}-execution page · expand matching turns, ${CONVERSATION_PAGE_SIZE} at a time`
            : `${DAG_GROUP_PAGE_SIZE} execution summaries per page · open a row for details`;
    }
    document.querySelectorAll('#dagListPanel th[data-sort]').forEach(th => {
        if (th.dataset.sort !== 'created_at') {
            th.classList.remove('sortable', 'asc', 'desc');
            th.removeAttribute('data-sort');
        }
    });
    updateGroupedLoadMoreControl();
}

function showExecutionListMode() {
    listPanelMode = 'list';
    document.getElementById('dagListMode')?.classList.remove('hidden');
    document.getElementById('dagConversationMode')?.classList.add('hidden');
}

function toggleConversationGroup(groupKey, triggerKind = 'expand') {
    if (expandedGroupKeys.has(groupKey)) {
        expandedGroupKeys.delete(groupKey);
    } else {
        expandedGroupKeys.add(groupKey);
    }
    renderServerExecutionGroups(groupedExecutionGroups);
    requestAnimationFrame(() => {
        const trigger = Array.from(document.querySelectorAll('[data-toggle-conversation-group]')).find(button =>
            button.dataset.toggleConversationGroup === groupKey &&
            (triggerKind === 'title' ? button.classList.contains('dag-group-title-btn') : button.classList.contains('dag-group-expand-btn')));
        trigger?.focus({ preventScroll: true });
    });
}

function conversationGroupMatches(group) {
    return [...(group.turns || []).map(turn => ({ turn })), ...(group.orphans || []).map(execution => ({ execution }))];
}

function navigateConversationMatches(direction, groupKey) {
    const group = groupedExecutionGroups.find(item => item.group_key === groupKey);
    if (!group) return;
    const count = conversationGroupMatches(group).length;
    const lastPage = Math.max(0, Math.ceil(count / CONVERSATION_PAGE_SIZE) - 1);
    const currentPage = groupMatchPages.get(groupKey) || 0;
    const page = Math.max(0, Math.min(lastPage, currentPage + (direction === 'previous' ? -1 : 1)));
    groupMatchPages.set(groupKey, page);
    renderServerExecutionGroups(groupedExecutionGroups);
    requestAnimationFrame(() => {
        const controls = Array.from(document.querySelectorAll('[data-conversation-matches-page]'))
            .filter(button => button.dataset.groupKey === groupKey && !button.disabled);
        (controls.find(button => button.dataset.conversationMatchesPage === direction) || controls[0])?.focus({ preventScroll: true });
    });
}


function renderServerExecutionGroups(groups) {
    const container = document.getElementById('dagTableBody');
    if (!container) return;

    if (groups.length === 0) {
        container.innerHTML = `
            <tr>
                <td colspan="5" class="dag-list-empty">
                    <div class="empty-state-icon">💬</div>
                    <div>No matching conversation groups found</div>
                </td>
            </tr>`;
    } else {
        const rows = [];
        groups.forEach(group => {
            if (!group.conversation_id) {
                (group.turns || []).forEach(turn => {
                    rows.push(renderHierarchyGroupedRows([turn.execution, ...(turn.related_executions || [])], turn.execution?.request_id, {
                        llmCallCount: turn.llm_call_count,
                        historyStatus: turn.history_status,
                    }));
                });
                rows.push(renderHierarchyGroupedRows(group.orphans || []));
                return;
            }

            const expanded = expandedGroupKeys.has(group.group_key);
            rows.push(renderConversationGroupHeader(group, expanded));
            if (!expanded) return;

            // Expansion keeps the current query's matches. The separate timeline
            // action loads broader history; it must not replace an older match
            // with unrelated recent turns simply because the group was expanded.
            const matches = conversationGroupMatches(group);
            const lastPage = Math.max(0, Math.ceil(matches.length / CONVERSATION_PAGE_SIZE) - 1);
            const page = Math.min(groupMatchPages.get(group.group_key) || 0, lastPage);
            groupMatchPages.set(group.group_key, page);
            const start = page * CONVERSATION_PAGE_SIZE;
            const visibleMatches = matches.slice(start, start + CONVERSATION_PAGE_SIZE);
            visibleMatches.forEach(({ turn }) => {
                if (!turn) return;
                rows.push(renderHierarchyGroupedRows([turn.execution, ...(turn.related_executions || [])], turn.execution?.request_id, {
                    conversationTurn: true, llmCallCount: turn.llm_call_count, historyStatus: turn.history_status,
                }));
            });
            // Orphans can still have an immediate parent on this page even
            // when their common root is absent. Resolve those links together.
            rows.push(renderHierarchyGroupedRows(visibleMatches.flatMap(item => item.execution ? [item.execution] : [])));
            if (matches.length > CONVERSATION_PAGE_SIZE) {
                rows.push(`<tr><td colspan="5"><div class="dag-list-footer">
                    <button type="button" class="dag-load-more" data-conversation-matches-page="previous" data-group-key="${escapeHtmlAttribute(group.group_key)}" ${page === 0 ? 'disabled' : ''}>← Previous matches</button>
                    <span>Matches ${start + 1}–${Math.min(start + CONVERSATION_PAGE_SIZE, matches.length)} of ${matches.length} on this page</span>
                    <button type="button" class="dag-load-more" data-conversation-matches-page="next" data-group-key="${escapeHtmlAttribute(group.group_key)}" ${page === lastPage ? 'disabled' : ''}>Next matches →</button>
                </div></td></tr>`);
            }
        });
        container.innerHTML = rows.join('');
    }

    updateGroupedLoadMoreControl();
}

function renderHierarchyGroupedRows(records, primaryID = '', primaryOptions = {}) {
    return buildExecutionHierarchy(records).ordered.map(entry => renderGroupedExecutionRow(entry.execution, {
        ...(entry.execution.request_id === primaryID ? primaryOptions : {}),
        related: entry.depth > 0 || entry.legacy || !!entry.reason,
        depth: entry.depth,
        hierarchyEntry: entry,
        orphan: !!entry.reason,
    })).join('');
}

function orphanRelationLabel(execution) {
    if (execution?.relation_status === 'owner_unavailable') {
        return 'Parent execution unavailable';
    }
    if (execution?.relation_status === 'owner_unknown') {
        return 'Parent relationship incomplete';
    }
    return 'Unattached related record';
}

// Titles are independent of the recent browse page, search matches and timeline
// pagination. Reuse the bounded conversation reader for one oldest retained
// top-level query, with two concurrent lookups and a small preview-only cache.
async function loadConversationTitles(groups) {
    const ticket = conversationTitleLoad.begin();
    const now = Date.now();
    const queue = [...new Set(groups.map(group => group.conversation_id).filter(Boolean))]
        .filter(id => now - (conversationTitleCache.get(id)?.loadedAt || 0) >= CONVERSATION_TITLE_CACHE_TTL_MS);
    let next = 0;
    const current = () => conversationTitleLoad.current(ticket) && groups === groupedExecutionGroups && groupingMode === 'grouped';
    await Promise.all(Array.from({ length: Math.min(2, queue.length) }, async () => {
        while (current() && next < queue.length) {
            const id = queue[next++];
            let entry;
            try {
                const query = new URLSearchParams({ conversation_id: id, direction: 'asc', limit: '1' });
                const { data } = await fetchAPI(`/api/conversations?${query}`, { signal: ticket.signal, cache: false });
                if (!current()) return;
                if (data.conversation_id !== id) throw new Error('Conversation identity changed while loading its title.');
                const request = data.turns?.[0]?.execution?.original_request;
                const text = typeof request === 'string' ? request : '';
                entry = { text, loadedAt: Date.now(), hint: data.partial
                    ? 'Earliest query available in this bounded lookup; earlier records may be unavailable.'
                    : 'First retained query in this conversation.' };
            } catch (error) {
                if (!current() || error.name === 'AbortError') return;
                if (isDagAccessDenied(error)) { clearDagAccessState(error); return; }
                entry = { text: conversationTitleCache.get(id)?.text || '', loadedAt: 0, hint: 'First query unavailable. Refresh the list to try again.' };
            }
            conversationTitleCache.delete(id);
            conversationTitleCache.set(id, entry);
            while (conversationTitleCache.size > CONVERSATION_TITLE_CACHE_MAX) conversationTitleCache.delete(conversationTitleCache.keys().next().value);
            // Update only the label, preserving expansion, keyboard focus and
            // the selected execution while titles arrive in the background.
            document.querySelectorAll('[data-conversation-title]').forEach(label => {
                if (label.dataset.conversationTitle !== id) return;
                label.textContent = truncateText(entry.text || 'Conversation', 62);
                label.title = entry.text ? `${entry.hint}\n${entry.text}` : entry.hint;
            });
        }
    }));
}

function renderConversationGroupHeader(group, expanded) {
    const turns = group.turns || [];
    const anchor = turns[0]?.execution || group.orphans?.[0] || {};
    const titleEntry = conversationTitleCache.get(group.conversation_id);
    const title = titleEntry ? titleEntry.text || 'Conversation' : 'Loading first query…';
    const titleHint = titleEntry?.text ? `${titleEntry.hint}\n${titleEntry.text}` : titleEntry?.hint || 'Looking up the first retained query in this conversation.';
    const executionCount = turns.reduce(
        (count, turn) => count + 1 + (turn.related_executions || []).length,
        (group.orphans || []).length,
    );
    const visibleExecutions = flattenGroupedExecutions([group]);
    const totalDuration = primaryDurationMs(anchor);
    const statuses = visibleExecutions.map(executionStatus);
    const uniform = statuses.length > 0 && statuses.every(item => item.state === statuses[0].state);
    const statusClass = uniform ? statuses[0].css : 'pending';
    const statusLabel = uniform ? statuses[0].label : 'Mixed lifecycle states';
    const shortConversationID = truncateText(group.conversation_id, 28);
    const matchingTurnCount = group.matching_turn_count ?? turns.length;
    const turnCountLabel = `${matchingTurnCount} matching turn${matchingTurnCount === 1 ? '' : 's'} in this page`;

    return `
        <tr class="dag-conversation-group-row ${expanded ? 'expanded' : ''}">
            <td>
                <button
                    class="dag-group-expand-btn"
                    type="button"
                    data-toggle-conversation-group="${escapeHtmlAttribute(group.group_key)}"
                    aria-expanded="${expanded}"
                    aria-label="${expanded ? 'Collapse' : 'Expand'} conversation ${escapeHtmlAttribute(shortConversationID)}"
                >${expanded ? '▾' : '▸'}</button>
                <span class="status-badge ${statusClass}">${statusLabel}</span>
            </td>
            <td>
                <button
                    class="dag-group-title-btn"
                    type="button"
                    data-toggle-conversation-group="${escapeHtmlAttribute(group.group_key)}"
                    aria-expanded="${expanded}"
                >
                    <span class="dag-group-kicker">Conversation · ${turnCountLabel}</span>
                    <span class="dag-group-title" data-conversation-title="${escapeHtmlAttribute(group.conversation_id)}" title="${escapeHtmlAttribute(titleHint)}">${escapeHtml(truncateText(title, 62))}</span>
                    <span class="dag-group-id">${escapeHtml(shortConversationID)}</span>
                </button>
                <button
                    class="dag-open-timeline-btn"
                    type="button"
                    data-open-conversation="${escapeHtmlAttribute(group.conversation_id)}"
                >View timeline</button>
                ${group.index_incomplete ? '<span class="dag-data-warning">Index incomplete</span>' : ''}
            </td>
            <td><span class="dag-group-count">${executionCount} execution${executionCount === 1 ? '' : 's'}</span></td>
            <td><span class="dag-mono">${formatDuration(totalDuration)}</span></td>
            <td>
                <span class="time-ago">${formatTimeAgo(group.anchor_created_at || anchor.created_at)}</span>
                <span class="dag-group-sort-note">latest match</span>
            </td>
        </tr>`;
}

function renderGroupedExecutionRow(exec, options = {}) {
    if (!exec) return '';
    const {
        conversationTurn = false,
        related = false,
        orphan = false,
        llmCallCount = 0,
        historyStatus = '',
        depth = 0,
        hierarchyEntry = { execution: exec, depth },
    } = options;
    const renderRelationBranch = depth > 0 && !orphan;
    const isSelected = selected?.request_id === exec.request_id;
    const rowClasses = [
        isSelected ? 'selected' : '',
        conversationTurn ? 'dag-conversation-turn-row' : '',
        renderRelationBranch ? 'child-row dag-related-execution-row' : '',
        orphan ? 'dag-orphan-execution-row' : '',
    ].filter(Boolean).join(' ');
    const requestID = exec.request_id || '';
    const originalRequestID = exec.original_request_id || requestID;
    const { css: statusClass, label: statusLabel } = executionStatus(exec);
    const duration = primaryDurationMs(exec);

    return `
        <tr class="${rowClasses}"
            data-request-id="${escapeHtmlAttribute(requestID)}"
            data-original-request-id="${escapeHtmlAttribute(originalRequestID)}">
            <td>
                <span class="status-badge ${statusClass}">${statusLabel}</span>
            </td>
            <td>
                <div class="dag-execution-title-row" style="margin-left: ${Math.min(depth, 4) * 12}px;" data-lineage-depth="${depth}">
                    <div class="dag-execution-title">
                        ${renderLineageRowContext(hierarchyEntry)}
                        <div class="service-name">${escapeHtml(truncateText(executionRequestText(exec), related ? 48 : 58))}</div>
                        <span class="request-id">${escapeHtml(requestID)}</span>
                        ${exec.interrupted ? '<span class="hitl-resume-badge">HITL Interrupted</span>' : ''}
                        ${llmCallCount ? `<span class="dag-row-meta">${llmCallCount} LLM call${llmCallCount === 1 ? '' : 's'}</span>` : ''}
                        ${historyStatus ? `<span class="dag-row-meta">${escapeHtml(historyStatus)}</span>` : ''}
                        ${exec.missing_owner_id ? `<span class="dag-row-meta">Parent: ${escapeHtml(exec.missing_owner_id)}</span>` : ''}
                    </div>
                </div>
            </td>
            <td>
                <div class="step-progress">
                    <span>${exec.step_count || 0} steps</span>
                    ${exec.failed_steps > 0 ? `<span class="dag-failed-steps">(${exec.failed_steps} failed)</span>` : ''}
                    ${exec.skipped_steps > 0 ? `<span class="dag-skipped-steps" style="color: var(--text-muted);">(${exec.skipped_steps} skipped)</span>` : ''}
                </div>
            </td>
            <td><span class="dag-mono">${formatDuration(duration)}</span></td>
            <td><span class="time-ago">${lastSnapshotLabel(exec, true)}</span><span class="dag-group-sort-note">Last snapshot</span></td>
        </tr>`;
}

function renderLegacyExecutionList() {
    const container = document.getElementById('dagTableBody');
    if (!container) return;
    const filtered = [...executions];
    filtered.sort((a, b) => {
        let left, right;
        if (sortColumn === 'original_request') {
            left = (a.original_request || '').toLowerCase();
            right = (b.original_request || '').toLowerCase();
        } else if (sortColumn === 'total_duration_ms') {
            left = primaryDurationMs(a); right = primaryDurationMs(b);
        } else {
            left = Date.parse(a.created_at) || 0; right = Date.parse(b.created_at) || 0;
        }
        return (left < right ? -1 : left > right ? 1 : 0) * (sortDirection === 'asc' ? 1 : -1);
    });
    // Root sections retain the chosen list sort. Explicit sibling order is
    // always ascending snapshot time; shared root IDs do not create edges.
    const hierarchy = buildExecutionHierarchy(filtered);
    const ranks = new Map(filtered.map((item, index) => [item.request_id, index]));
    const sections = [];
    for (const entry of hierarchy.ordered) {
        if (entry.depth === 0) sections.push([]);
        sections[sections.length - 1].push(entry);
    }
    sections.sort((a, b) => ranks.get(a[0].execution.request_id) - ranks.get(b[0].execution.request_id));
    container.innerHTML = sections.flat().map(entry => renderGroupedExecutionRow(entry.execution, {
        depth: entry.depth,
        hierarchyEntry: entry,
        related: entry.depth > 0 || entry.legacy || !!entry.reason,
        orphan: !!entry.reason,
    })).join('') || '<tr><td colspan="5" class="dag-list-empty">No executions found</td></tr>';
    const updated = document.getElementById('dagLastUpdated');
    if (updated) updated.textContent = `Last updated: ${new Date().toLocaleTimeString()}`;
}

// ---------------------------------------------------------------------------
// Conversation timeline
// ---------------------------------------------------------------------------

function openConversationTimeline(conversationID, trigger) {
    if (!conversationID) return;
    if (listPanelMode !== 'timeline') {
        timelineReturnFocus = trigger || document.activeElement;
    }
    activeConversationID = conversationID;
    listPanelMode = 'timeline';
    document.getElementById('dagListMode')?.classList.add('hidden');
    document.getElementById('dagConversationMode')?.classList.remove('hidden');
    renderConversationTimelineState(conversationID);
    document.getElementById('dagTimelineHeading')?.focus({ preventScroll: true });
    void loadConversationTimeline(conversationID, selected?.request_id);
}

function closeConversationTimeline() {
    timelineLoad.cancel();
    showExecutionListMode();
    const refreshedGroupTrigger = Array.from(document.querySelectorAll(
        '[data-open-conversation]',
    )).find(button => button.dataset.openConversation === activeConversationID);
    const focusTarget = timelineReturnFocus?.isConnected
        ? timelineReturnFocus
        : (refreshedGroupTrigger ||
            document.getElementById('dagSearchInput'));
    timelineReturnFocus = null;
    focusTarget?.focus({ preventScroll: true });
}

function renderConversationTimelineState(conversationID) {
    const cached = conversationCache.get(conversationID);
    if (cached?.data) {
        renderConversationTimeline(cached.data);
        return;
    }
    const idTarget = document.getElementById('dagTimelineConversationID');
    if (idTarget) idTarget.textContent = conversationID;
    const summary = document.getElementById('dagTimelineSummary');
    if (summary) summary.textContent = 'Loading conversation…';
    setTimelineDiagnostic('');
    const content = document.getElementById('dagTimelineContent');
    if (content) {
        content.innerHTML = `
            <div class="empty-detail">
                <div class="empty-detail-icon">💬</div>
                <div>Loading conversation timeline…</div>
            </div>`;
    }
}

function rememberConversationPage(conversationID, entry) {
    conversationCache.delete(conversationID);
    conversationCache.set(conversationID, entry);
    while (conversationCache.size > CONVERSATION_CACHE_MAX) conversationCache.delete(conversationCache.keys().next().value);
}

async function loadConversationTimeline(conversationID, selectedRequestID, { force = false, cursor, navigation = '' } = {}) {
    if (!conversationID) return null;
    const cached = conversationCache.get(conversationID);
    const requestedCursor = cursor ?? cached?.cursor ?? '';
    if (!force && cached?.data && cached.cursor === requestedCursor) {
        rememberConversationPage(conversationID, cached);
        if (activeConversationID === conversationID && listPanelMode === 'timeline') renderConversationTimeline(cached.data, selectedRequestID);
        return cached.data;
    }
    const ticket = timelineLoad.begin();
    const entry = { data: cached?.data || null, cursor: cached?.cursor || '', history: [...(cached?.history || [])], pending: true, error: '' };
    rememberConversationPage(conversationID, entry);
    const query = new URLSearchParams({ conversation_id: conversationID, limit: String(CONVERSATION_PAGE_SIZE) });
    if (requestedCursor) query.set('cursor', requestedCursor);
    try {
        const { data } = await fetchAPI(`/api/conversations?${query.toString()}`, { signal: ticket.signal, cache: false });
        if (!timelineLoad.current(ticket)) return null;
        if (data.conversation_id !== conversationID) throw new Error('Conversation identity changed while loading.');
        if (navigation === 'next') {
            entry.history.push(cached?.cursor || '');
            if (entry.history.length > CURSOR_HISTORY_MAX) entry.history.shift();
        } else if (navigation === 'previous') entry.history.pop();
        entry.data = data;
        entry.cursor = requestedCursor;
        entry.pending = false;
        rememberConversationPage(conversationID, entry);
        if (activeConversationID === conversationID && listPanelMode === 'timeline') renderConversationTimeline(data, selected?.request_id || selectedRequestID);
        if (groupingMode === 'grouped' && listPanelMode === 'list') renderServerExecutionGroups(groupedExecutionGroups);
        return data;
    } catch (error) {
        if (isDagAccessDenied(error)) { clearDagAccessState(error); return null; }
        if (conversationCache.get(conversationID) === entry) {
            entry.pending = false;
            entry.error = error.status === 409 ? 'This page moved or expired. Reopen the conversation or refresh to start again.' : error.name === 'AbortError' ? 'Load interrupted; reopen to retry.' : error.message;
            if (error.status === 409) { entry.data = null; entry.cursor = ''; entry.history = []; }
        }
        if (!timelineLoad.current(ticket) || error.name === 'AbortError') return null;
        if (activeConversationID === conversationID && listPanelMode === 'timeline') {
            if (entry.data) {
                renderConversationTimeline(entry.data, selected?.request_id);
                appendTimelineDiagnostic('The requested page could not be loaded. The previous page remains visible.');
            } else { renderTimelineError(); if (entry.error) appendTimelineDiagnostic(entry.error); }
        }
        if (groupingMode === 'grouped' && listPanelMode === 'list') renderServerExecutionGroups(groupedExecutionGroups);
        return null;
    }
}

async function navigateConversationPage(direction, conversationID = activeConversationID) {
    const page = conversationCache.get(conversationID);
    if (!page || page.pending) return;
    const cursor = direction === 'previous' ? page.history.at(-1) : page.data?.next_cursor;
    if (cursor === undefined || (direction === 'next' && !cursor)) return;
    await loadConversationTimeline(conversationID, selected?.request_id, { force: true, cursor, navigation: direction });
}

function renderConversationTimeline(timeline, selectedRequestID = selected?.request_id) {
    if (!timeline || activeConversationID !== timeline.conversation_id) return;
    const turns = timeline.turns || [];
    const executionCount = timeline.execution_count || 0;
    const idTarget = document.getElementById('dagTimelineConversationID');
    if (idTarget) idTarget.textContent = timeline.conversation_id;
    const summary = document.getElementById('dagTimelineSummary');
    if (summary) {
        summary.textContent = `${turns.length} turn${turns.length === 1 ? '' : 's'} in this page · ${executionCount} execution summaries · 5 turns per page`;
    }

    const diagnosticMessages = [];
    if (timeline.index_incomplete) {
        diagnosticMessages.push('The reverse index is incomplete; verified execution records are still shown.');
    }
    if (timeline.llm_enrichment_incomplete) {
        diagnosticMessages.push('LLM call counts and history status are incomplete.');
    }
    if (timeline.partial) {
        diagnosticMessages.push('The timeline reached a safety bound and may omit records.');
    }
    setTimelineDiagnostic(diagnosticMessages.join(' '));

    const page = conversationCache.get(timeline.conversation_id);
    const previous = document.getElementById('dagTimelinePrevious');
    const next = document.getElementById('dagTimelineNext');
    if (previous) { previous.disabled = !page?.history.length || !!page?.pending; previous.title = 'Previous 5 conversation turns'; previous.setAttribute('aria-label', previous.title); }
    if (next) { next.disabled = !timeline.next_cursor || !!page?.pending; next.title = 'Next 5 conversation turns'; next.setAttribute('aria-label', next.title); }

    const content = document.getElementById('dagTimelineContent');
    if (!content) return;
    if (turns.length === 0 && (timeline.orphans || []).length === 0) {
        content.innerHTML = `
            <div class="empty-detail">
                <div class="empty-detail-icon">💬</div>
                <div>${timeline.next_cursor ? 'No top-level turns in this scanned page. Use the next arrow to continue.' : 'No verified turns are available in this page.'}</div>
            </div>`;
        return;
    }

    const cards = turns.map((turn, index) =>
        renderTimelineTurn(turn, index, turns.length, selectedRequestID)
    );
    if ((timeline.orphans || []).length > 0) {
        cards.push(`
            <section class="dag-timeline-orphans">
                <h3>Unattached related records</h3>
                <p>These executions reference a parent that could not be attached. Each card shows whether the parent was confirmed unavailable or the lookup was incomplete.</p>
                ${renderTimelineHierarchy(timeline.orphans || [], selectedRequestID)}
            </section>`);
    }
    content.innerHTML = cards.join('');
    content.querySelector('.dag-timeline-card.current, .dag-timeline-related.current')
        ?.scrollIntoView({ block: 'nearest' });
}

function renderTimelineTurn(turn, index, totalTurns, selectedRequestID) {
    const execution = turn.execution || {};
    const related = turn.related_executions || [];
    const containsSelected = execution.request_id === selectedRequestID ||
        related.some(item => item.request_id === selectedRequestID);
    const { css: statusClass, label: statusLabel } = executionStatus(execution);
    const duration = primaryDurationMs(execution);
    const hierarchyEntry = buildExecutionHierarchy([execution, ...related]).ordered.find(entry => entry.execution.request_id === execution.request_id);

    return `
        <article class="dag-timeline-card ${containsSelected ? 'contains-current' : ''} ${execution.request_id === selectedRequestID ? 'current' : ''}" ${containsSelected ? 'aria-current="true"' : ''}>
            <div class="dag-timeline-rail" aria-hidden="true">
                <span>${index + 1}</span>
            </div>
            <div class="dag-timeline-card-body">
                <div
                    class="dag-timeline-card-action"
                    role="button"
                    tabindex="0"
                    data-view-timeline-execution="${escapeHtmlAttribute(execution.request_id || '')}"
                    aria-label="View DAG for turn ${index + 1}: ${escapeHtmlAttribute(truncateText(executionRequestText(execution), 90))}"
                    ${execution.request_id === selectedRequestID ? 'aria-current="true"' : ''}
                >
                    <div class="dag-timeline-card-header">
                        <div>
                            <span class="dag-turn-label">Turn ${index + 1} of ${totalTurns}</span>
                            <time datetime="${escapeHtmlAttribute(execution.created_at || '')}">Last snapshot: ${lastSnapshotLabel(execution)}</time>
                        </div>
                        <span class="status-badge ${statusClass}">${statusLabel}</span>
                    </div>
                    ${hierarchyEntry ? renderLineageRowContext(hierarchyEntry) : ''}
                    <div class="dag-timeline-request">${escapeHtml(executionRequestText(execution))}</div>
                    <div class="dag-timeline-meta">
                        ${execution.agent_name ? `<span>${escapeHtml(execution.agent_name)}</span>` : ''}
                        <span>${execution.step_count || 0} steps</span>
                        ${execution.failed_steps ? `<span class="dag-error-text">${execution.failed_steps} failed</span>` : ''}
                        ${execution.skipped_steps ? `<span>${execution.skipped_steps} skipped</span>` : ''}
                        <span>${formatDuration(duration)}</span>
                        ${turn.llm_call_count ? `<span>${turn.llm_call_count} LLM call${turn.llm_call_count === 1 ? '' : 's'}</span>` : ''}
                        ${turn.history_status ? `<span>${escapeHtml(turn.history_status)}</span>` : ''}
                    </div>
                    <div class="dag-timeline-card-footer">
                        <span class="dag-timeline-request-id">${escapeHtml(execution.request_id || '')}</span>
                        <span class="dag-view-dag-btn" aria-hidden="true">
                            ${execution.request_id === selectedRequestID ? 'Viewing DAG' : 'View DAG'}
                        </span>
                    </div>
                </div>
                ${related.length > 0 ? `
                    <div class="dag-timeline-related-list">
                        <div class="dag-related-heading">${related.length} related execution${related.length === 1 ? '' : 's'}</div>
                        <div class="dag-row-meta">Sibling order: last snapshot, oldest first</div>
                        ${renderTimelineHierarchy([execution, ...related], selectedRequestID, execution.request_id)}
                    </div>` : ''}
            </div>
        </article>`;
}

function renderTimelineHierarchy(records, selectedRequestID, primaryID = '') {
    return buildExecutionHierarchy(records).ordered.filter(entry => entry.execution.request_id !== primaryID)
        .map(entry => renderTimelineRelatedExecution(entry.execution, selectedRequestID, !!entry.reason, entry)).join('');
}

function renderTimelineRelatedExecution(execution, selectedRequestID, orphan = false, entry = {}) {
    const current = execution.request_id === selectedRequestID;
    const duration = primaryDurationMs(execution);
    const relation = lineageRelationLabel({ ...entry, execution }) || 'Related · immediate parent not recorded';
    const depth = entry.depth || 0;
    return `
        <div
            class="dag-timeline-related ${current ? 'current' : ''}"
            style="margin-left: ${Math.min(depth, 4) * 12}px;" data-lineage-depth="${depth}"
            role="button"
            tabindex="0"
            data-view-timeline-execution="${escapeHtmlAttribute(execution.request_id || '')}"
            aria-label="View DAG for ${escapeHtmlAttribute(relation.toLowerCase())} execution: ${escapeHtmlAttribute(truncateText(executionRequestText(execution), 90))}"
            ${current ? 'aria-current="true"' : ''}
        >
            <div>
                ${renderLineageRowContext({ ...entry, execution })}
                <div>${escapeHtml(truncateText(executionRequestText(execution), 72))}</div>
                <div class="dag-timeline-meta">
                    ${execution.agent_name ? `<span>${escapeHtml(execution.agent_name)}</span>` : ''}
                    <span>Last snapshot: ${lastSnapshotLabel(execution)}</span>
                    <span>${execution.step_count || 0} steps</span>
                    <span>${formatDuration(duration)}</span>
                    <span class="status-badge ${executionStatus(execution).css}">${executionStatus(execution).label}</span>
                </div>
                <div class="dag-timeline-request-id">${escapeHtml(execution.request_id || '')}</div>
                ${execution.missing_owner_id ? `<div class="dag-timeline-request-id">Parent: ${escapeHtml(execution.missing_owner_id)}</div>` : ''}
            </div>
            <span class="dag-view-dag-btn" aria-hidden="true">
                ${current ? 'Viewing DAG' : 'View DAG'}
            </span>
        </div>`;
}

function findConversationTurnIndex(timeline, requestID) {
    if (!timeline || !requestID) return -1;
    return (timeline.turns || []).findIndex(turn =>
        turn.execution?.request_id === requestID ||
        (turn.related_executions || []).some(item => item.request_id === requestID)
    );
}

function navigateConversationTurn(direction) {
    void navigateConversationPage(direction);
}

function copyConversationID(button) {
    if (activeConversationID) copyToClipboard(activeConversationID, button);
}

function setTimelineDiagnostic(message) {
    const target = document.getElementById('dagTimelineDiagnostic');
    if (!target) return;
    target.classList.toggle('hidden', !message);
    target.textContent = message || '';
}

function appendTimelineDiagnostic(message) {
    const target = document.getElementById('dagTimelineDiagnostic');
    if (!target || !message) return;
    const combined = [target.textContent.trim(), message]
        .filter(Boolean)
        .join(' ');
    target.classList.remove('hidden');
    target.textContent = combined;
}

function renderTimelineError() {
    const summary = document.getElementById('dagTimelineSummary');
    if (summary) summary.textContent = 'Conversation unavailable';
    setTimelineDiagnostic('The conversation timeline could not be loaded. Refresh to try again.');
    const content = document.getElementById('dagTimelineContent');
    if (content) {
        content.innerHTML = `
            <div class="empty-detail">
                <div class="empty-detail-icon">⚠</div>
                <div>The selected execution remains available in the DAG panel.</div>
            </div>`;
    }
}

// ---------------------------------------------------------------------------
// Execution selection & detail
// ---------------------------------------------------------------------------

function normalizeExecutionView(data) {
    if (data.phase_plans?.length > 1) {
        return { ...data, plan: { ...(data.plan || {}), steps: data.phase_plans.flatMap(plan => plan.steps || []) } };
    }
    return data;
}

function sectionAvailable(section) {
    const available = selectedOverview?.available_sections || selected?.available_sections;
    return Array.isArray(available) ? available.includes(section) : available?.[section] === true;
}

function activeDetailSection() {
    const sections = { 'dag-steps': 'steps', 'dag-llm': 'llm', 'dag-pre': 'pre', 'dag-post': 'post', 'dag-hitl': 'hitl', 'dag-skills': 'skills', 'dag-raw': 'raw' };
    return activeTab === 'dag-viz' && viewMode === 'full' ? 'flow' : sections[activeTab] || 'overview';
}

// Keep only the small checkpoint numbering fact across section changes. The
// checkpoint body remains on demand and belongs to its explicit HITL section.
function retainObservedResumeNumbering(data) {
    const previous = selectedOverview?.resume_step_numbering;
    const incoming = data.resume_step_numbering;
    if (data.request_id !== selectedOverview?.request_id || !incoming?.checkpoint_id) return data;
    const hasNumbering = fact => Number.isSafeInteger(fact?.completed_step_count) && fact.completed_step_count >= 0
        && Number.isSafeInteger(fact?.display_step_offset) && fact.display_step_offset >= 0
        && fact.display_step_offset <= fact.completed_step_count;
    const fact = !hasNumbering(incoming) && previous?.checkpoint_id === incoming.checkpoint_id && hasNumbering(previous)
        ? previous : incoming;
    selectedOverview = { ...selectedOverview, resume_step_numbering: fact };
    return { ...data, resume_step_numbering: fact };
}

async function loadSelectedSection(section = activeDetailSection(), { force = false } = {}) {
    if (!selected) return false;
    if (!selectedOverview) selectedOverview = selected;
    if (section === 'overview') {
        sectionLoad.cancel();
        selected = selectedOverview;
        selectedSection = 'overview';
        sectionPending = false;
        sectionError = '';
        renderExecutionDetail();
        return true;
    }
    if (!force && selectedSection === section && !sectionError) {
        // Returning to the loaded section also owns the current view. An older
        // request for another tab must not replace this section when it arrives.
        sectionLoad.cancel();
        sectionPending = false;
        renderExecutionDetail();
        return true;
    }
    const requestID = selected.request_id;
    const version = selectionVersion;
    const ticket = sectionLoad.begin();
    sectionPending = true;
    sectionError = '';
    renderExecutionDetail();
    try {
        const { data } = await fetchAPI(`/api/executions/${encodeURIComponent(requestID)}/unified?section=${encodeURIComponent(section)}`, { signal: ticket.signal, cache: false });
        if (!sectionLoad.current(ticket) || version !== selectionVersion || selected?.request_id !== requestID || section !== activeDetailSection()) return false;
        if (data.request_id !== requestID) throw new Error('Execution identity changed while loading.');
        if (section === 'raw' && typeof data.raw_execution_json !== 'string') throw new Error('Raw execution JSON is unavailable.');
        selected = normalizeExecutionView({ ...selectedOverview, ...retainObservedResumeNumbering(data) });
        selectedSection = section;
        sectionPending = false;
        annotateCallLabels(selected.llm_interactions);
        updateDetailTabs();
        renderExecutionDetail();
        return true;
    } catch (error) {
        if (isDagAccessDenied(error)) { clearDagAccessState(error); return false; }
        if (!sectionLoad.current(ticket) || error.name === 'AbortError') return false;
        sectionPending = false;
        sectionError = error.message || 'Evidence could not be loaded.';
        renderExecutionDetail();
        return false;
    }
}

async function selectExecution(requestId, { restoreTimelineActionFocus = false, restoreTab = '', restoreViewMode = 'full' } = {}) {
    const version = ++selectionVersion;
    const ticket = detailLoad.begin();
    sectionLoad.cancel();
    sectionPending = false;
    sectionError = '';
    const knownSummary = loadedLineageSummaries().find(item => item.request_id === requestId);
    document.querySelectorAll('.node-popup').forEach(p => p.remove());
    const detailPanel = document.getElementById('dagDetailPanel');
    showLoading(detailPanel, 'Loading execution overview…');
    try {
        const { data } = await fetchAPI(`/api/executions/${encodeURIComponent(requestId)}/unified?section=overview`, { signal: ticket.signal, cache: false });
        if (!detailLoad.current(ticket) || version !== selectionVersion) return;
        if (data.request_id !== requestId) throw new Error('Execution identity changed while loading.');
        lineageLoader.select(requestId, knownSummary);
        resetParentExecutionLookup();
        selectedOverview = normalizeExecutionView(data);
        selected = selectedOverview;
        selectedSection = 'overview';
        viewMode = restoreViewMode === 'steps' ? 'steps' : 'full';
        activeTab = restoreTab || (invokingStepTarget?.requestID === requestId ? 'dag-steps' : 'dag-viz');
        if (activeTab === 'dag-parent' && !selected.parent_request_id) activeTab = 'dag-viz';
        updateExecutionDeepLink(selected.request_id);
        annotateCallLabels(selected.llm_interactions);
        refreshInvocationServices();
        renderExecutionList();
        updateDetailTabs();
        document.querySelectorAll('#dagDetailPanel .detail-tab').forEach(button => button.classList.toggle('active', button.dataset.tab === activeTab));
        if (activeDetailSection() === 'overview') renderExecutionDetail();
        else await loadSelectedSection(activeDetailSection(), { force: true });
        if (!detailLoad.current(ticket)) return;
        if (invokingStepTarget?.requestID === selected.request_id) {
            const stepID = invokingStepTarget.stepID;
            requestAnimationFrame(() => {
                if (version !== selectionVersion) return;
                const card = document.getElementById(`dag-step-details-${stepID}`);
                card?.focus({ preventScroll: true });
                card?.scrollIntoView({ block: 'nearest' });
            });
        }
        if (restoreTimelineActionFocus && listPanelMode === 'timeline') {
            requestAnimationFrame(() => {
                const action = Array.from(document.querySelectorAll('[data-view-timeline-execution]')).find(button => button.dataset.viewTimelineExecution === requestId);
                action?.focus({ preventScroll: true });
            });
        }
    } catch (error) {
        if (isDagAccessDenied(error)) { clearDagAccessState(error); return; }
        if (!detailLoad.current(ticket) || error.name === 'AbortError') return;
        document.getElementById('dagDetailContent').innerHTML = `<div class="empty-detail" role="alert">${escapeHtml(error.message || 'Execution overview could not be loaded.')}</div>`;
    } finally {
        if (detailLoad.current(ticket)) hideLoading(detailPanel);
    }
}

// Update tab visibility based on available data
function updateDetailTabs() {
    const parentTab = document.querySelector('[data-tab="dag-parent"]');
    if (parentTab) parentTab.style.display = selected?.parent_request_id ? 'block' : 'none';
    if (activeTab === 'dag-parent' && !selected?.parent_request_id) activeTab = 'dag-viz';
    const llmTab = document.querySelector('[data-tab="dag-llm"]');
    const hitlTab = document.querySelector('[data-tab="dag-hitl"]');
    const preTab = document.querySelector('[data-tab="dag-pre"]');
    const postTab = document.querySelector('[data-tab="dag-post"]');
    const skillsTab = document.querySelector('[data-tab="dag-skills"]');

    if (llmTab) {
        llmTab.style.display = selected?.has_llm_data || sectionAvailable('llm') ? 'block' : 'none';
    }
    if (hitlTab) {
        hitlTab.style.display = selected?.has_hitl_data || sectionAvailable('hitl') ? 'block' : 'none';
    }

    // LLM-debug interactions describe hook-internal operations. Stored
    // pipeline_hooks preserve deterministic hook invocations that make no LLM
    // call. Either source must make the respective tab visible.
    const interactions = selected?.llm_interactions || [];
    const pipelineHooks = selected?.pipeline_hooks || [];
    const { isPreHook, isPostHook } = classifyInteractions(interactions);
    const hasPreHooks = interactions.some(isPreHook) || pipelineHooks.some(isPreExecutionHook);
    const hasPostHooks = interactions.some(isPostHook) ||
        pipelineHooks.some(isPostExecutionHook) ||
        typeof selected?.final_response === 'string';

    if (preTab) {
        preTab.style.display = hasPreHooks || sectionAvailable('pre') ? 'block' : 'none';
    }
    if (postTab) {
        postTab.style.display = hasPostHooks || sectionAvailable('post') ? 'block' : 'none';
    }
    if (skillsTab) {
        skillsTab.style.display = selected?.skills || sectionAvailable('skills') ? 'block' : 'none';
    }
}

function setDagDetailTab(tab) {
    activeTab = tab === 'dag-parent' && !selected?.parent_request_id ? 'dag-viz' : tab;
    document.querySelectorAll('.node-popup').forEach(p => p.remove());
    document.querySelectorAll('#dagDetailPanel .detail-tab').forEach(button => button.classList.toggle('active', button.dataset.tab === activeTab));
    return loadSelectedSection(activeDetailSection());
}

function rawExecutionText(execution = selected) {
    return typeof execution?.raw_execution_json === 'string'
        ? execution.raw_execution_json
        : null;
}

function renderExecutionDetail(viewport = null) {
    cleanupDAGLegend();
    if (!selected) return;

    llmCardBatch?.dispose();
    llmCardBatch = null;

    const container = document.getElementById('dagDetailContent');

    if (sectionPending) {
        container.innerHTML = '<div class="empty-detail" role="status">Loading requested execution evidence…</div>';
        return;
    }
    if (sectionError) {
        container.innerHTML = `<div class="empty-detail" role="alert">${escapeHtml(sectionError)}<button type="button" class="dag-open-timeline-btn" data-retry-execution-section>Retry this section</button></div>`;
        return;
    }

    // Update detail title
    document.getElementById('dagDetailTitle').textContent = truncateText(executionRequestText(selected), 50);

    if (activeTab === 'dag-viz') {
        renderDAGVisualization(container, viewport);
    } else if (activeTab === 'dag-parent') {
        renderParentExecution(container);
    } else if (activeTab === 'dag-pre') {
        renderPreExecution(container);
    } else if (activeTab === 'dag-steps') {
        renderStepDetails(container);
    } else if (activeTab === 'dag-llm') {
        renderLLMCalls(container);
    } else if (activeTab === 'dag-post') {
        renderPostExecution(container);
    } else if (activeTab === 'dag-hitl') {
        renderHITLCheckpoints(container);
    } else if (activeTab === 'dag-skills') {
        renderSkillExecution(container);
    } else if (activeTab === 'dag-raw') {
        const rawText = rawExecutionText();
        if (rawText === null) {
            container.innerHTML = '<div class="empty-detail" role="alert">Raw execution JSON is unavailable.<button type="button" class="dag-open-timeline-btn" data-retry-execution-section>Retry this section</button></div>';
            return;
        }
        container.innerHTML = `
            <div class="json-container">
                <button class="copy-btn">Copy</button>
                <pre class="json-view">${syntaxHighlightJson(prettyPrintJsonText(rawText))}</pre>
            </div>`;
        // Bind copy button via delegation
        const copyBtn = container.querySelector('.copy-btn');
        if (copyBtn) {
            copyBtn.addEventListener('click', (e) => {
                e.stopPropagation();
                copyToClipboard(rawText, e);
            });
        }
    }
    const warnings = Array.isArray(selected.section_warnings) ? selected.section_warnings.filter(value => typeof value === 'string') : [];
    if (warnings.length) {
        container.insertAdjacentHTML('afterbegin', `<div class="dag-list-diagnostic warning" role="status"><span>${warnings.map(escapeHtml).join(' ')}</span><button type="button" data-retry-execution-section>Retry this section</button></div>`);
    }
}

function skillVersionIdentity(ref) {
    const logical = ref?.ref || ref || {};
    const base = [logical.namespace, logical.name].filter(Boolean).join('/');
    return `${base || 'unknown-skill'}${ref?.version ? `@v${ref.version}` : ''}`;
}

function skillLogicalIdentity(ref) {
    const logical = ref?.ref || ref || {};
    return [logical.namespace, logical.name].filter(Boolean).join('/') || 'unknown-skill';
}

function skillMetadataIndex(candidates) {
    const index = new Map();
    (candidates || []).forEach(candidate => {
        const metadata = {
            displayName: candidate.display_name || candidate.ref?.name || 'Unnamed skill',
            description: candidate.description || '',
        };
        index.set(skillLogicalIdentity(candidate.ref), metadata);
        if (candidate.resolved) index.set(skillVersionIdentity(candidate.resolved), metadata);
    });
    return index;
}

function skillMetadataFor(ref, metadata) {
    return metadata.get(skillVersionIdentity(ref)) ||
        metadata.get(skillLogicalIdentity(ref)) || {
            displayName: ref?.ref?.name || ref?.name || 'Unnamed skill',
            description: '',
        };
}

function skillEventStatus(...values) {
    const status = values.filter(Boolean).join(' ').toLowerCase();
    if (/(fail|error|reject|invalid|unavailable|not_found|deleted)/.test(status)) return 'failed';
    if (/(skip|bypass|degrad|not_selected|ineligible|pending)/.test(status)) return 'pending';
    return 'success';
}

function renderSkillIdentity(ref, metadata, { compact = false } = {}) {
    const identity = skillMetadataFor(ref, metadata);
    return `<div class="skill-inline-identity ${compact ? 'compact' : ''}">
        <div class="skill-inline-heading">
            <span class="skill-inline-name">${escapeHtml(identity.displayName)}</span>
            <code>${escapeHtml(skillVersionIdentity(ref))}</code>
        </div>
        <p>${identity.description
            ? escapeHtml(identity.description)
            : '<span class="skill-metadata-unavailable">Description was not captured by this execution.</span>'}</p>
    </div>`;
}

function renderSkillTechnicalRecord(item, label = 'Technical record') {
    return `<details class="skill-technical-details">
        <summary>${escapeHtml(label)}</summary>
        <div class="json-container"><pre class="json-view skill-json-view">${syntaxHighlightJson(item)}</pre></div>
    </details>`;
}

function renderSkillEventCard({ type, status, title, badge, identity, body, technical }) {
    return `<article class="skill-event-card skill-event-${type} skill-status-${status}">
        <div class="skill-event-header">
            <div><span class="skill-event-kind">${escapeHtml(type.replace(/-/g, ' '))}</span><h4>${escapeHtml(title)}</h4></div>
            <span class="skill-lifecycle-badge ${status}">${escapeHtml(badge)}</span>
        </div>
        ${identity || ''}
        ${body || ''}
        ${renderSkillTechnicalRecord(technical)}
    </article>`;
}

function renderSkillStageHeader(stage, title, description, count) {
    return `<div class="skill-card-heading">
        <div class="skill-stage-heading-copy">
            <span class="skill-eyebrow">${escapeHtml(stage)}</span>
            <h3>${escapeHtml(title)}</h3>
            <p class="skill-stage-description">${escapeHtml(description)}</p>
        </div>
        <span class="skill-card-hint">${count} record${count === 1 ? '' : 's'}</span>
    </div>`;
}

function renderSkillLifecycleExplainer() {
    const stages = [
        {
            number: 1,
            short: 'Pin',
            title: 'Resolve configured candidates',
            description: 'The agent’s bindings are resolved together. Each available skill is fixed to an exact immutable version for the entire request.',
            detail: 'Bindings → exact versions',
            type: 'pinning',
        },
        {
            number: 2,
            short: 'Activate',
            title: 'Decide which skills apply',
            description: 'Always skills activate directly, explicit skills require a trusted host request, and auto skills are selected from their metadata and the current task.',
            detail: 'Required controls failure handling, not relevance',
            type: 'activation',
        },
        {
            number: 3,
            short: 'Select resources',
            title: 'Choose phase-relevant resources',
            description: 'For active skills, deterministic requirements and developer resolvers run first; an optional selector can choose among the remaining eligible resources.',
            detail: 'Re-evaluated at execution boundaries',
            type: 'resources',
        },
        {
            number: 4,
            short: 'Load',
            title: 'Load and verify admitted content',
            description: 'Instruction and resource bodies are loaded only when needed, checked against their pinned hashes, and admitted within configured limits.',
            detail: 'Progressive disclosure—not one large blob',
            type: 'loads',
        },
        {
            number: 5,
            short: 'Project',
            title: 'Add content to the current prompt',
            description: 'Only admitted instructions and resources relevant to the current planning, continuation, or synthesis boundary enter the model prompt.',
            detail: 'Phase-local content within token budgets',
            type: 'projection',
        },
    ];
    const compact = stages.map((stage, index) => `${index ? '<i aria-hidden="true">→</i>' : ''}
        <span><strong>${stage.number}</strong>${escapeHtml(stage.short)}</span>`).join('');
    const expanded = stages.map((stage, index) => `${index ? '<div class="skill-flow-arrow" aria-hidden="true"><span>↓</span></div>' : ''}
        <article class="skill-flow-step skill-flow-${stage.type}">
            <div class="skill-flow-marker"><strong>${stage.number}</strong><span>${escapeHtml(stage.short)}</span></div>
            <div class="skill-flow-copy">
                <span class="skill-eyebrow">Stage ${stage.number}</span>
                <h4>${escapeHtml(stage.title)}</h4>
                <p>${escapeHtml(stage.description)}</p>
                <code>${escapeHtml(stage.detail)}</code>
            </div>
        </article>`).join('');

    return `<details class="skill-lifecycle-explainer">
        <summary class="skill-lifecycle-summary">
            <span class="skill-lifecycle-summary-copy">
                <span class="skill-eyebrow">Selection flow</span>
                <strong>How a skill reaches the model prompt</strong>
                <span>Expand for a brief stage-by-stage explanation of runtime selection.</span>
            </span>
            <span class="skill-lifecycle-compact" aria-label="Pin, activate, select resources, load, then project">${compact}</span>
            <span class="skill-lifecycle-toggle"><span></span><i aria-hidden="true">⌄</i></span>
        </summary>
        <div class="skill-lifecycle-flow">${expanded}</div>
    </details>`;
}

function renderSkillExecution(container) {
    const debug = selected?.skills;
    if (!debug) {
        container.innerHTML = `<div class="empty-detail"><div class="empty-detail-icon">🧩</div><div>No skill lifecycle data was recorded for this execution.</div></div>`;
        return;
    }

    const candidates = debug.candidates || [];
    const activations = debug.activations || [];
    const resourceSelections = debug.resource_selections || [];
    const contentLoads = debug.content_loads || [];
    const projections = debug.projections || [];
    const metadata = skillMetadataIndex(candidates);
    const rows = (items, render) => items.length
        ? items.map(render).join('')
        : '<div class="skill-empty-state">None recorded for this execution.</div>';
    const uniqueProjectedSkills = new Set(
        projections.flatMap(item => item.skill_refs || []).map(skillVersionIdentity),
    ).size;

    const candidateCards = rows(candidates, item => {
        const status = skillEventStatus(item.status);
        const ref = item.resolved || item.ref;
        return renderSkillEventCard({
            type: 'pinning', status,
            title: item.resolved ? `Pinned ${skillVersionIdentity(item.resolved)}` : skillLogicalIdentity(item.ref),
            badge: item.status || 'unknown',
            identity: renderSkillIdentity(ref, metadata),
            body: `<div class="skill-event-meta">
                <span>Requested <strong>${escapeHtml(item.requested_version || 'published')}</strong></span>
                <span>Activation <strong>${escapeHtml(item.activation || '—')}</strong></span>
                ${item.required ? '<span><strong>Required</strong></span>' : ''}
            </div>`,
            technical: item,
        });
    });

    const activationCards = rows(activations, item => {
        const status = skillEventStatus(item.decision, item.admission);
        return renderSkillEventCard({
            type: 'activation', status,
            title: `${item.boundary || 'unknown boundary'} · phase ${item.phase_number || 0}`,
            badge: item.decision || item.admission || 'recorded',
            identity: renderSkillIdentity(item.skill, metadata),
            body: `<div class="skill-event-meta">
                <span>Selector <strong>${escapeHtml(item.selector || '—')}</strong></span>
                <span>Admission <strong>${escapeHtml(item.admission || '—')}</strong></span>
                <span>Activation <strong>${escapeHtml(item.activation || '—')}</strong></span>
            </div>
            ${item.reason ? `<div class="skill-selection-reason"><span>Selection reason</span><p>${escapeHtml(item.reason)}</p></div>` : ''}`,
            technical: item,
        });
    });

    const resourceCards = rows(resourceSelections, item => {
        const status = skillEventStatus(item.decision, item.admission, item.eligibility);
        return renderSkillEventCard({
            type: 'resource-selection', status,
            title: item.resource?.name || 'Unnamed resource',
            badge: item.decision || item.admission || 'recorded',
            identity: renderSkillIdentity(item.resource?.skill, metadata, { compact: true }),
            body: `<div class="skill-event-meta">
                <span>${escapeHtml(item.boundary || 'unknown boundary')}</span>
                <span>Phase <strong>${item.phase_number || 0}</strong></span>
                <span>Eligibility <strong>${escapeHtml(item.eligibility || '—')}</strong></span>
                <span>Selector <strong>${escapeHtml(item.selector || '—')}</strong></span>
                ${item.required_when_selected ? '<span><strong>Required when selected</strong></span>' : ''}
            </div>
            ${item.reason ? `<div class="skill-selection-reason"><span>Selection reason</span><p>${escapeHtml(item.reason)}</p></div>` : ''}`,
            technical: item,
        });
    });

    const loadCards = rows(contentLoads, item => {
        const status = skillEventStatus(item.outcome, item.retry_outcome);
        const label = item.content_kind === 'resource'
            ? `Resource · ${item.resource_name || 'unnamed'}`
            : 'Main skill instructions';
        return renderSkillEventCard({
            type: 'content-load', status,
            title: label,
            badge: item.outcome || 'recorded',
            identity: renderSkillIdentity(item.skill, metadata, { compact: true }),
            body: `<div class="skill-event-meta">
                <span>${escapeHtml(item.boundary || 'unknown boundary')}</span>
                <span>Phase <strong>${item.phase_number || 0}</strong></span>
                <span>Cache <strong>${escapeHtml(item.cache_outcome || '—')}</strong></span>
                <span>Source <strong>${escapeHtml(item.source || '—')}</strong></span>
                <span>Attempt <strong>${item.attempt || 0}</strong></span>
                <span>Size <strong>${item.byte_estimate || 0} bytes</strong></span>
                <span>Estimate <strong>${item.token_estimate || 0} tokens</strong></span>
                <span>Load time <strong>${formatDuration(item.duration_ms || 0)}</strong></span>
            </div>`,
            technical: item,
        });
    });

    const projectionCards = rows(projections, item => {
        const status = skillEventStatus(item.outcome);
        const projectedSkills = (item.skill_refs || []).map(ref => renderSkillIdentity(ref, metadata, { compact: true })).join('');
        const projectedResources = (item.resource_refs || []).map(ref => `<div class="skill-resource-ref">
            <span>${escapeHtml(ref.name || 'Unnamed resource')}</span>
            <code>${escapeHtml(skillVersionIdentity(ref.skill))}</code>
        </div>`).join('');
        return renderSkillEventCard({
            type: 'projection', status,
            title: `${item.boundary || 'unknown boundary'} · ${item.prompt_kind || 'prompt'}`,
            badge: item.outcome || 'recorded',
            body: `<div class="skill-event-meta">
                <span>Phase <strong>${item.phase_number || 0}</strong></span>
                <span>Total <strong>${item.total_tokens || 0} tokens</strong></span>
                <span>Main <strong>${item.main_instruction_tokens || 0}</strong></span>
                <span>Resources <strong>${item.resource_tokens || 0}</strong></span>
            </div>
            <div class="skill-projection-content">
                <div><span class="skill-meta-label">Injected skills</span>${projectedSkills || '<span class="skill-empty-value">None</span>'}</div>
                <div><span class="skill-meta-label">Injected resources</span>${projectedResources || '<span class="skill-empty-value">None</span>'}</div>
            </div>`,
            technical: item,
        });
    });

    container.innerHTML = `<div class="skill-execution-view">
        <section class="glass-card skill-execution-summary">
            <div class="skill-summary-heading">
                <div><span class="skill-eyebrow">Execution lifecycle</span><h3>Skill activity</h3><code class="skill-exact-ref">${escapeHtml(debug.binding_source || 'unknown')} bindings</code></div>
                <span class="skill-status-badge ${debug.runtime_policy?.response_cache_eligible ? 'success' : 'pending'}">Cache ${debug.runtime_policy?.response_cache_eligible ? 'eligible' : 'ineligible'}</span>
            </div>
            <div class="skill-execution-stats">
                <div><strong>${candidates.length}</strong><span>Candidates</span></div>
                <div><strong>${activations.length}</strong><span>Decisions</span></div>
                <div><strong>${resourceSelections.length}</strong><span>Resource decisions</span></div>
                <div><strong>${uniqueProjectedSkills}</strong><span>Injected skills</span></div>
            </div>
            ${renderSkillTechnicalRecord({
                binding_source: debug.binding_source,
                binding_fingerprint: debug.binding_fingerprint,
                budget_fingerprint: debug.budget_fingerprint,
                cache_fingerprint: debug.cache_fingerprint,
                runtime_policy: debug.runtime_policy,
            }, 'Request pinning and runtime policy')}
        </section>

        ${renderSkillLifecycleExplainer()}

        <section class="glass-container skill-lifecycle-section skill-section-pinning">
            ${renderSkillStageHeader('Stage 1', 'Candidate pinning', 'Shows the developer-bound skills and exact immutable versions fixed for this request.', candidates.length)}
            <div class="skill-event-list">${candidateCards}</div>
        </section>
        <section class="glass-container skill-lifecycle-section skill-section-activation">
            ${renderSkillStageHeader('Stage 2', 'Activation decisions', 'Shows which pinned skills were activated or skipped at each execution boundary, including the selector reason.', activations.length)}
            <div class="skill-event-list">${activationCards}</div>
        </section>
        <section class="glass-container skill-lifecycle-section skill-section-resources">
            ${renderSkillStageHeader('Stage 3', 'Resource selections', 'Shows which resources from active skills were selected for each phase and why they were chosen.', resourceSelections.length)}
            <div class="skill-event-list">${resourceCards}</div>
        </section>
        <section class="glass-container skill-lifecycle-section skill-section-loads">
            ${renderSkillStageHeader('Stage 4', 'Content loads', 'Shows when selected instructions and resources were loaded, including source, cache outcome, and retries.', contentLoads.length)}
            <div class="skill-event-list">${loadCards}</div>
        </section>
        <section class="glass-container skill-lifecycle-section skill-section-projections">
            ${renderSkillStageHeader('Stage 5', 'Prompt projections', 'Shows which loaded skill instructions and resources entered each phase prompt, with their token usage.', projections.length)}
            <div class="skill-event-list">${projectionCards}</div>
        </section>
        ${debug.diagnostics?.length ? `<section class="glass-container skill-lifecycle-section skill-section-diagnostics">
            ${renderSkillStageHeader('Diagnostics', 'Skill warnings and failures', 'Shows lifecycle conditions that may explain missing, skipped, degraded, or failed skill behavior.', debug.diagnostics.length)}
            <div class="skill-event-list">${debug.diagnostics.map(item => renderSkillEventCard({
                type: 'diagnostic', status: skillEventStatus(item.code) === 'failed' ? 'failed' : 'pending', title: item.code || 'Diagnostic', badge: item.boundary || 'runtime',
                body: item.detail ? `<p class="skill-diagnostic-detail">${escapeHtml(item.detail)}</p>` : '', technical: item,
            })).join('')}</div>
        </section>` : ''}
    </div>`;
}

// ---------------------------------------------------------------------------
// DAG view mode toggle
// ---------------------------------------------------------------------------

function setDagViewMode(mode) {
    if (mode !== 'steps' && mode !== 'full') return;
    if (viewMode === mode) return;
    viewMode = mode;
    if (selected) void loadSelectedSection(mode === 'full' ? 'flow' : 'overview');
}

// ---------------------------------------------------------------------------
// DAG Visualization
// ---------------------------------------------------------------------------

function validClarification(value) {
    return value && typeof value.question === 'string' && value.question.trim() ? value : null;
}

function recordedClarification(execution) {
    return validClarification(execution?.result?.clarification_needed);
}

function clarificationOutcomeLabel(execution) {
    return executionStatus(execution).state === 'completed' ? 'Clarification requested' : 'Recorded clarification';
}

function renderClarificationDetails(value, label, description, compact = false) {
    const clarification = validClarification(value);
    if (!clarification) return '';
    const missingFields = Array.isArray(clarification.missing_fields)
        ? clarification.missing_fields.filter(field => typeof field === 'string') : [];
    return `<details class="dag-clarification-details" style="${compact ? 'display: inline-block;' : 'margin-top: 10px;'} font-size: 12px;">
        <summary style="cursor: pointer; color: var(--accent-orange); font-weight: 600;">${escapeHtml(label)}</summary>
        <div style="margin-top: 8px; color: var(--text-primary); overflow-wrap: anywhere; max-width: 560px;">
            <div style="font-size: 11px; color: var(--text-muted); margin-bottom: 6px;">${escapeHtml(description)}</div>
            <pre style="white-space: pre-wrap; font: inherit; margin: 0 0 10px;">${escapeHtml(clarification.question)}</pre>
            ${missingFields.length ? `<strong>Missing fields</strong><ul style="white-space: pre-wrap; padding-left: 20px; margin: 4px 0 10px;">${missingFields.map(field => `<li>${escapeHtml(field)}</li>`).join('')}</ul>` : ''}
            ${typeof clarification.partial_progress === 'string' && clarification.partial_progress ? `<strong>Partial progress</strong><pre style="white-space: pre-wrap; font: inherit; margin: 4px 0 0;">${escapeHtml(clarification.partial_progress)}</pre>` : ''}
        </div>
    </details>`;
}

function renderRecordedClarification(execution, compact = false) {
    return renderClarificationDetails(recordedClarification(execution), clarificationOutcomeLabel(execution),
        'Recorded outcome for this execution; client delivery is separate.', compact);
}

function renderPlannerClarifications(execution, phaseNumber = null) {
    const plans = execution?.phase_plans?.length ? execution.phase_plans : [execution?.plan];
    return plans.map((plan, index) => {
        const number = plan?.phase_number || index + 1;
        if (phaseNumber !== null && number !== phaseNumber) return '';
        return renderClarificationDetails(plan?.needs_user_input, `Phase ${number} planner observation`,
            'Input requested in the plan; this does not establish delivery to the caller.');
    }).join('');
}

function renderDAGVisualization(container, viewport = null) {
    cleanupDAGLegend();
    const renderedSelection = selected;
    const renderedVersion = selectionVersion;
    const renderedMode = viewMode;
    const stepCount = selected.plan?.steps?.length || 0;
    const hasLLM = selected.has_llm_data;
    const hasHITL = selected.has_hitl_data;
    const hasPipelineHooks = (selected.pipeline_hooks || []).length > 0;
    const llmCallCount = selected.llm_interactions?.length || 0;

    // Elapsed time and aggregate LLM call time are separate diagnostics. LLM
    // calls occur inside the elapsed interval, so they are not additive.
    const totalDurationMs = primaryDurationMs(selected);
    const llmDurationMs = selected.llm_debug_summary?.total_duration_ms || 0;

    // Check if we can show Full Flow (has LLM or HITL data)
    const canShowFullFlow = hasLLM || hasHITL || hasPipelineHooks || sectionAvailable('flow');
    const agentName = selected.agent_name || 'orchestrator';
    // The trace ID is stored with the execution. This link is an optional
    // navigation affordance; no Viewer data is loaded from Jaeger.
    const traceID = selected.trace_id;
    const failureNotice = executionFailureNotice(selected);
    const requestHelp = selected.payloads_deferred
        ? 'Overview request preview. Open Raw JSON for the complete stored request. Expand or collapse this preview.'
        : 'Expand or collapse the recorded request.';

    container.innerHTML = `
        <div class="dag-viz-container">
            ${failureNotice ? `<div class="dag-lifecycle-failure" role="status" style="padding: 12px; border-left: 3px solid var(--accent-red); color: var(--text-primary); background: rgba(255,107,107,0.08);">${escapeHtml(failureNotice)}</div>` : ''}
            <div class="dag-viz-header">
                <div class="dag-viz-title" role="button" tabindex="0" aria-expanded="false" title="${escapeHtmlAttribute(requestHelp)}" aria-label="${escapeHtmlAttribute(requestHelp)}">${formatConversationRequest(executionRequestText(selected))}</div>
                <div class="dag-viz-meta">
                    ${agentName !== 'orchestrator' ? `<span class="dag-viz-meta-item" style="color: var(--accent-purple);">🤖 ${escapeHtml(agentName)}</span>` : ''}
                    <span class="dag-viz-meta-item">ID: ${(selected.request_id || '').substring(0, 20)}...</span>
                    ${traceID ? `<a class="dag-viz-meta-item dag-viz-trace-link" href="${jaegerTraceURL(traceID)}" target="_blank" rel="noopener noreferrer" title="Open execution trace ${escapeHtmlAttribute(traceID)} in Jaeger">Trace</a>` : ''}
                    <span class="dag-viz-meta-item">${stepCount} step${stepCount !== 1 ? 's' : ''}</span>
                    <span class="dag-viz-meta-item" title="${escapeHtmlAttribute(selected.duration_source || 'legacy execution duration')}">⏱️ Elapsed: ${formatDuration(totalDurationMs)}</span>
                    <span class="dag-viz-meta-item ${executionStatus(selected).css}">${executionStatus(selected).label}</span>
                    ${renderRecordedClarification(selected, true)}
                    ${hasLLM ? `<span class="dag-viz-meta-item" style="color: var(--accent-blue);">💭 ${llmCallCount} LLM calls · ${formatDuration(llmDurationMs)}</span>` : ''}
                    ${hasHITL && !selected.interrupted ? `<span class="dag-viz-meta-item" style="color: var(--accent-orange);">⏸️ HITL</span>` : ''}
                    ${selected.phase_count > 1 ? `<span class="dag-viz-meta-item" style="color: var(--accent-teal);">🔄 ${selected.phase_count} Phases</span>` : ''}
                    ${selected.forced_terminal ? `<span class="dag-viz-meta-item" style="color: var(--accent-orange);">⚠ Forced Terminal</span>` : ''}
                    <div class="dag-viz-actions">
                        <button type="button" class="dag-viz-refresh" data-refresh-execution-overview aria-label="Refresh execution" title="Refresh execution snapshot">
                            <svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><path d="M3 12a9 9 0 0 1 15.36-6.36L21 8M21 3v5h-5M21 12a9 9 0 0 1-15.36 6.36L3 16M3 21v-5h5"></path></svg>
                        </button>
                        ${canShowFullFlow ? `
                        <div class="dag-view-toggle">
                            <button class="toggle-btn steps-btn ${viewMode === 'steps' ? 'active' : ''}" data-view-mode="steps">Steps Only</button>
                            <button class="toggle-btn full-btn ${viewMode === 'full' ? 'active' : ''}" data-view-mode="full">Full Flow</button>
                        </div>` : ''}
                    </div>
                </div>
            </div>
            <div class="dag-canvas-stage">
                <div id="dagContainer" class="dag-viz-canvas"></div>
                <div class="dag-legend-controls">
                    <div class="dag-canvas-toolbar">
                        <button type="button" class="dag-fit-button" id="dagFitButton" title="Fit the whole diagram in the canvas" disabled>
                            <svg width="14" height="14" viewBox="0 0 18 18" fill="none" stroke="currentColor" stroke-width="1.4" aria-hidden="true"><path d="M6 2H2v4m10-4h4v4M2 12v4h4m10-4v4h-4"></path><rect x="5.5" y="5.5" width="7" height="7" rx="1"></rect></svg>
                            Fit
                        </button>
                        <button type="button" class="dag-legend-toggle" id="dagLegendToggle" aria-expanded="false" aria-controls="dagLegendPanel">
                            <svg width="14" height="14" viewBox="0 0 18 18" fill="none" stroke="currentColor" stroke-width="1.4" aria-hidden="true"><rect x="1.5" y="2" width="5" height="5" rx="1.5"></rect><path d="M10 4.5h6M10 13.5h6M4 10l3.5 3.5L4 17 .5 13.5Z"></path></svg>
                            Legend
                        </button>
                    </div>
                    <section class="dag-legend-panel" id="dagLegendPanel" aria-labelledby="dagLegendHeading" hidden>
                        <div class="dag-legend-heading">
                            <div><h3 id="dagLegendHeading">Legend</h3><span>Symbols in this view</span></div>
                            <button type="button" class="dag-legend-close" aria-label="Close legend">×</button>
                        </div>
                        <div class="dag-legend-body" id="dagLegendContent"><p class="dag-legend-empty">Preparing graph symbols…</p></div>
                    </section>
                </div>
            </div>
        </div>`;

    bindDAGCanvasControls(container);

    // Bind view-mode toggle buttons via delegation
    const vizTitle = container.querySelector('.dag-viz-title');
    if (vizTitle) {
        const toggleRequest = () => {
            const expanded = vizTitle.classList.toggle('expanded');
            vizTitle.setAttribute('aria-expanded', String(expanded));
        };
        vizTitle.addEventListener('click', toggleRequest);
        vizTitle.addEventListener('keydown', event => {
            if (event.key !== 'Enter' && event.key !== ' ') return;
            event.preventDefault();
            toggleRequest();
        });
    }
    const toggleBtns = container.querySelectorAll('[data-view-mode]');
    toggleBtns.forEach(btn => {
        btn.addEventListener('click', () => {
            setDagViewMode(btn.dataset.viewMode);
        });
    });

    // Initialize Cytoscape after DOM is ready — use cache if available.
    // Cache only helps for tab switches within the same execution (DAG → LLM Calls → DAG).
    // When switching executions, innerHTML rebuilds the dagContainer, orphaning cached children.
    setTimeout(() => {
        if (selected !== renderedSelection || selectionVersion !== renderedVersion || viewMode !== renderedMode || activeTab !== 'dag-viz') return;
        const cacheKey = `${selected.request_id}:${viewMode}`;
        const cached = dagCache.get(cacheKey);
        const dagContainer = document.getElementById('dagContainer');

        if (cached && dagContainer && cached.container.parentNode === dagContainer) {
            // Cache hit — the cached child is still attached to the current dagContainer.
            // This happens on tab switch within the same execution.
            dagContainer.querySelectorAll('.dag-cached-graph').forEach(el => el.style.display = 'none');
            cached.container.style.display = 'block';
            cyInstance = cached.cy;
            cyInstance.resize();
            cyInstance.fit(50);
            updateDAGLegend(cyInstance);
        } else {
            // Cache miss or stale (execution switched, dagContainer was rebuilt).
            // Remove stale cache entry if any.
            if (cached) {
                cached.cy.destroy();
                dagCache.delete(cacheKey);
            }
            initCytoscape();
        }
        const fitButton = container.querySelector('#dagFitButton');
        if (fitButton) fitButton.disabled = !cyInstance || cyInstance.destroyed();
        if (viewport && cyInstance) {
            cyInstance.zoom(viewport.zoom);
            cyInstance.pan(viewport.pan);
        }
    }, 50);
}

// ---------------------------------------------------------------------------
// Cytoscape helpers
// ---------------------------------------------------------------------------

// Group orchestrator-level planning LLM calls by phase for multi-phase DAG topology.
// Uses the phase_number on continuation_plan_generation calls; pairs tiered_selection
// calls with the plan call that immediately follows them in the interaction stream.
//
// Type membership and phase-closing semantics are driven by the LLM-type
// registry at static/js/llm-types.js — see isPlanningType / isPhaseClosingType.
function groupLLMCallsByPhase(llmInteractions) {
    const planningCalls = llmInteractions.filter(i =>
        isPlanningType(i.type) || (i.type === 'micro_resolution' && !i.step_id)
    );

    const phases = {};
    let currentGroup = [];

    for (const call of planningCalls) {
        currentGroup.push(call);
        if (isPhaseClosingType(call.type)) {
            const phaseNum = getPhaseNumber(call);
            // Append rather than overwrite: a phase may have continuation_plan_generation
            // followed by continuation_plan_regeneration (plan regen flow), and we want
            // all calls for that phase grouped together in the DAG.
            phases[phaseNum] = (phases[phaseNum] || []).concat(currentGroup);
            currentGroup = [];
        }
    }
    // Orphan calls (tiered_selection without a following plan) attach to last recorded phase
    if (currentGroup.length > 0) {
        const keys = Object.keys(phases).map(Number);
        const lastPhase = keys.length > 0 ? Math.max(...keys) : 1;
        phases[lastPhase] = (phases[lastPhase] || []).concat(currentGroup);
    }

    return phases;
}

// Compute step levels using topological sort (for parallelism visualization)
function computeStepLevels(steps) {
    if (!steps || steps.length === 0) return [];

    // Build in-degree map and adjacency list
    const inDegree = {};
    const dependents = {};

    steps.forEach(step => {
        inDegree[step.step_id] = (step.depends_on || []).length;
        dependents[step.step_id] = [];
    });

    steps.forEach(step => {
        (step.depends_on || []).forEach(dep => {
            if (dependents[dep]) {
                dependents[dep].push(step.step_id);
            }
        });
    });

    // Kahn's algorithm for topological sort with level tracking
    const levels = [];
    let currentLevel = steps.filter(s => inDegree[s.step_id] === 0).map(s => s.step_id);

    while (currentLevel.length > 0) {
        levels.push(currentLevel);

        const nextLevel = [];
        currentLevel.forEach(stepId => {
            (dependents[stepId] || []).forEach(dep => {
                inDegree[dep]--;
                if (inDegree[dep] === 0) {
                    nextLevel.push(dep);
                }
            });
        });
        currentLevel = nextLevel;
    }

    return levels;
}

function computeExecutionStepLevels(execution) {
    const phasePlans = execution?.phase_plans || [];
    if (phasePlans.length <= 1) {
        return computeStepLevels(execution?.plan?.steps || []);
    }

    const levels = [];
    phasePlans.forEach((phase, phaseIndex) => {
        const phaseSteps = phase.steps || [];
        const phaseStepIDs = new Set(phaseSteps.map(step => step.step_id));
        const scopedSteps = phaseSteps.map(step => ({
            ...step,
            depends_on: (step.depends_on || []).filter(dep => phaseStepIDs.has(dep)),
        }));
        levels.push(...computeStepLevels(scopedSteps));
        if (phaseIndex < phasePlans.length - 1) {
            levels.push([`phase_boundary_${phaseIndex + 1}`]);
        }
    });
    return levels;
}

// Legend overlays are request/view-owned. Only an open panel installs document
// listeners, and every render, access denial and view teardown releases them.
function cleanupDAGLegend() {
    legendCleanup?.();
    legendCleanup = null;
}

function bindDAGCanvasControls(container) {
    const controls = container.querySelector('.dag-legend-controls');
    const toggle = controls?.querySelector('#dagLegendToggle');
    const panel = controls?.querySelector('#dagLegendPanel');
    const closeButton = controls?.querySelector('.dag-legend-close');
    if (!toggle || !panel || !closeButton) return;
    let dismissal = null;
    const close = (restoreFocus = false) => {
        panel.hidden = true;
        toggle.setAttribute('aria-expanded', 'false');
        dismissal?.abort();
        dismissal = null;
        if (restoreFocus && toggle.isConnected) toggle.focus({ preventScroll: true });
    };
    controls.querySelector('#dagFitButton')?.addEventListener('click', () => {
        if (!cyInstance || cyInstance.destroyed() || !container.contains(cyInstance.container())) return;
        close();
        document.querySelectorAll('.node-popup').forEach(p => p.remove());
        cyInstance.resize();
        cyInstance.fit(50);
    });
    toggle.addEventListener('click', () => {
        if (!panel.hidden) { close(true); return; }
        if (cyInstance && !cyInstance.destroyed() && container.contains(cyInstance.container())) updateDAGLegend(cyInstance);
        panel.hidden = false;
        toggle.setAttribute('aria-expanded', 'true');
        dismissal = new AbortController();
        const options = { signal: dismissal.signal, capture: true };
        document.addEventListener('pointerdown', event => {
            if (!controls.contains(event.target)) close();
        }, options);
        document.addEventListener('focusin', event => {
            if (!controls.contains(event.target)) close();
        }, options);
        document.addEventListener('keydown', event => {
            if (event.key !== 'Escape') return;
            event.preventDefault();
            event.stopPropagation();
            close(true);
        }, options);
        closeButton.focus({ preventScroll: true });
    });
    closeButton.addEventListener('click', () => close(true));
    legendCleanup = () => close();
}

// Read computed Cytoscape styles instead of maintaining another color/shape
// table. Samples describe the graph actually rendered, including overrides.
function dagLegendSample(element, edge = false) {
    const style = name => element.style(name);
    const attr = value => escapeHtmlAttribute(String(value || ''));
    const dash = (value, pattern = '') => {
        // Cytoscape fixes dotted strokes at [1, 1], ignoring dash-pattern.
        if (value === 'dotted') return '1 1';
        if (value !== 'dashed') return '';
        const segments = String(pattern).match(/\d+(?:\.\d+)?/g)?.slice(0, 4);
        return segments?.length ? segments.join(' ') : '6 3';
    };
    const opacity = value => Number.isFinite(parseFloat(value)) ? Math.min(1, Math.max(0, parseFloat(value))) : 1;
    const svg = body => `<svg class="dag-legend-sample" width="44" height="28" viewBox="0 0 44 28" aria-hidden="true">${body}</svg>`;
    if (edge) {
        const color = attr(style('line-color'));
        const pattern = dash(style('line-style'), style('line-dash-pattern'));
        const arrow = style('target-arrow-shape') !== 'none';
        return svg(`<path d="M3 14h${arrow ? 33 : 38}" fill="none" stroke="${color}" stroke-width="2.2" stroke-linecap="${attr(style('line-cap') || 'butt')}"${pattern ? ` stroke-dasharray="${pattern}"` : ''}/>${arrow ? `<path d="m33 9 8 5-8 5Z" fill="${attr(style('target-arrow-color'))}"/>` : ''}`);
    }
    const pattern = dash(style('border-style'), style('border-dash-pattern'));
    const attributes = `fill="${attr(style('background-color'))}" fill-opacity="${opacity(style('background-opacity'))}" stroke="${attr(style('border-color'))}" stroke-width="2" stroke-linecap="${attr(style('border-cap') || 'butt')}"${pattern ? ` stroke-dasharray="${pattern}"` : ''}`;
    const underlay = opacity(style('underlay-opacity') || 0);
    const glow = underlay > 0 ? `<rect x="1" y="1" width="42" height="26" rx="7" fill="${attr(style('underlay-color'))}" fill-opacity="${underlay}"/>` : '';
    const shape = style('shape') === 'diamond'
        ? `<polygon points="22,2 41,14 22,26 3,14" ${attributes}/>`
        : `<rect x="5" y="4" width="34" height="20" rx="5" ${attributes}/>`;
    return svg(glow + shape);
}

function collectDAGLegendEntries(graph) {
    const groups = { nodes: new Map(), indicators: new Map(), connections: new Map() };
    const add = (group, key, label, description, element, edge = false) => {
        if (!groups[group].has(key)) groups[group].set(key, { label, description, sample: dagLegendSample(element, edge) });
    };
    const nodeLabels = {
        orchestrator: ['Agent entry', 'Starts this execution'],
        step: ['Step', 'Capability execution'],
        pipeline_hook: ['Pipeline hook', 'Framework hook invocation'],
        user_memory_group: ['Memory hook group', 'Grouped memory activity'],
        phase_boundary: ['Phase boundary', 'Separates planning phases'],
        response: ['Execution outcome', 'Recorded result or interruption'],
    };
    const statusLabels = { completed: 'Completed', failed: 'Failed', pending: 'Pending', blocked: 'Awaiting human input', skipped: 'Skipped' };
    const resolutionLabels = { auto_wire_only: ['Auto-wired', 'Parameters resolved without an LLM'], llm_resolved: ['LLM-resolved', 'Parameter resolution used an LLM'], semantic_retry: ['Semantic retry', 'LLM-assisted error correction'] };
    const checkpointLabels = { before_step: 'Before-step checkpoint', after_step: 'After-step checkpoint', on_error: 'Error checkpoint' };
    graph?.nodes().forEach(node => {
        const data = node.data();
        const type = data.nodeType || 'step';
        if (data.isAgentInvocation) add('nodes', 'delegated_agent', 'Delegated agent', 'Invokes another agent', node);
        else if (['llm_call', 'llm_step', 'skill_llm', 'agent_llm', 'memory_llm'].includes(type)) {
            add('nodes', `llm:${type}:${data.llmType}`, getLLMType(data.llmType).label, data.llmType === 'conversation_history_prepare' ? 'Context preparation' : 'LLM call', node);
        } else if (type === 'checkpoint') {
            add('nodes', `checkpoint:${data.checkpointType || 'plan'}`, checkpointLabels[data.checkpointType] || 'Plan approval', 'Human-in-the-loop checkpoint', node);
        } else {
            const [label, description] = nodeLabels[type] || ['Graph node', 'Recorded execution activity'];
            add('nodes', type, label, description, node);
        }
        if (statusLabels[data.status]) add('indicators', `status:${data.status}`, statusLabels[data.status], 'Recorded step status', node);
        if (data.hookStatus === 'failed' || data.hookStatus === 'skipped') {
            const consequence = pipelineHookConsequence({ status: data.hookStatus, decision: data.hookDecision });
            add('indicators', `hook:${data.hookStatus}:${consequence.action}`, data.hookStatus === 'failed' ? 'Hook failure' : 'Hook skipped', consequence.summary, node);
        }
        if (resolutionLabels[data.resolutionType]) {
            const [label, description] = resolutionLabels[data.resolutionType];
            add('indicators', data.resolutionType, label, description, node);
        }
        if (data.hasAutoIncludes && !data.trimmed) add('indicators', 'auto_include', 'Auto-included input', 'A referenced result was included automatically', node);
        if (data.trimmed) add('indicators', 'trimmed', 'Result trimmed', 'Result shortened for model context', node);
    });
    const edgeLabels = {
        checkpoint: ['Approval connection', 'Links a checkpoint to its step'],
        llm_step: ['Step LLM call', 'Links a step to its model interaction'],
        agent_llm: ['Agent LLM call', 'Links agent-side model activity'],
        memory_llm: ['Memory activity', 'Links memory processing'],
        failed: ['Failed-step connection', 'Links activity from a failed step'],
        implicit_dependency: ['Earlier-step data', 'Data provenance; not execution ordering'],
        pipeline_hook: ['Hook sequence', 'Places a hook in the execution flow'],
        phase_transition: ['Phase transition', 'Connects planning phases'],
        skill_selection: ['Skill selection', 'Links a selector to its consumer'],
        hook_evidence: ['Unplaced evidence', 'Association only; placement not recorded'],
        skill_evidence: ['Unplaced evidence', 'Association only; placement not recorded'],
    };
    graph?.edges().forEach(edge => {
        const data = edge.data();
        const type = data.edgeType || 'flow';
        let [label, description] = edgeLabels[type] || ['Execution flow', 'Connects execution activity'];
        if (type === 'llm_step' && data.llmType) {
            label = `${getLLMType(data.llmType).label} link`;
        } else if (type === 'flow') {
            const source = edge.source?.()?.data?.();
            const target = edge.target?.()?.data?.();
            if (source && target && (!source.nodeType || source.nodeType === 'step') && (!target.nodeType || target.nodeType === 'step')) {
                label = 'Step dependency';
                description = 'The target waits for its prerequisite step';
            }
        }
        const key = JSON.stringify([label, edge.style('line-color'), edge.style('line-style'), edge.style('line-dash-pattern'), edge.style('target-arrow-color'), edge.style('target-arrow-shape')]);
        add('connections', key, label, description, edge, true);
    });
    return groups;
}

function updateDAGLegend(graph) {
    const content = document.getElementById('dagLegendContent');
    if (!content) return;
    const groups = collectDAGLegendEntries(graph);
    const labels = { nodes: 'Node types', indicators: 'Status & indicators', connections: 'Connections' };
    const sections = Object.entries(groups).filter(([, entries]) => entries.size).map(([key, entries]) => `
        <section class="dag-legend-section"><h4>${labels[key]}</h4>${[...entries.values()].map(entry => `
            <div class="dag-legend-row">${entry.sample}<div><strong>${escapeHtml(entry.label)}</strong><span>${escapeHtml(entry.description)}</span></div></div>
        `).join('')}</section>`).join('');
    const levels = selected ? computeExecutionStepLevels(selected) : [];
    const widest = Math.max(0, ...levels.map(level => level.length));
    content.innerHTML = sections
        ? `${sections}${levels.length ? `<div class="dag-legend-stats" title="Dependency layout, not observed runtime concurrency"><span>Step depth <strong>${levels.length}</strong></span><span>Widest level <strong>${widest}</strong></span></div>` : ''}`
        : '<p class="dag-legend-empty">No graph symbols in this view.</p>';
}

// Get resolution info from step result for DAG node badges
function getResolutionInfo(result) {
    const resolution = result?.metadata?.resolution;
    if (!resolution) return { type: 'none', summary: '' };

    const parts = [];
    if (resolution.auto_wired_count > 0) parts.push(`${resolution.auto_wired_count}AW`);
    if (resolution.micro_resolved_count > 0) parts.push(`${resolution.micro_resolved_count}LLM`);
    if (resolution.semantic_retry_count > 0) parts.push(`${resolution.semantic_retry_count}SR`);
    if (resolution.user_provided_count > 0) parts.push(`${resolution.user_provided_count}UP`);

    let type = 'none';
    if (resolution.semantic_retry_count > 0) type = 'semantic_retry';
    else if (resolution.micro_resolved_count > 0) type = 'llm_resolved';
    else if (resolution.auto_wired_count > 0) type = 'auto_wire_only';

    return { type, summary: parts.join(' \u00b7 ') };
}

// ---------------------------------------------------------------------------
// initCytoscape — builds the full DAG graph
// ---------------------------------------------------------------------------

function initCytoscape() {
    const dagContainerParent = document.getElementById('dagContainer');
    if (!dagContainerParent || !selected) return;
    const planSteps = selected.plan?.steps || [];

    // Hide existing cached containers
    dagContainerParent.querySelectorAll('.dag-cached-graph').forEach(el => el.style.display = 'none');

    // Create a child container for this instance (allows caching multiple graphs)
    const dagContainer = document.createElement('div');
    dagContainer.className = 'dag-cached-graph';
    dagContainer.style.cssText = 'width: 100%; height: 100%;';
    dagContainerParent.appendChild(dagContainer);

    if (viewMode === 'steps' && planSteps.length === 0) {
        cyInstance = null;
        dagContainer.innerHTML = '<div class="empty-detail">No admitted steps to display. Full Flow shows the recorded execution activity.</div>';
        updateDAGLegend(null);
        return;
    }

    // Helper to extract duration in ms from result (handles both duration_ms and duration in nanoseconds)
    const getDurationMs = (result) => {
        if (!result) return 0;
        if (result.duration_ms) return result.duration_ms;
        if (result.duration) return Math.round(result.duration / 1000000); // ns to ms
        return 0;
    };

    // Build step result lookup
    const stepResults = {};
    // First check result.steps (for completed executions)
    if (selected.result?.steps) {
        selected.result.steps.forEach(step => {
            stepResults[step.step_id] = step;
        });
    }
    // Fall back to checkpoint.step_results for HITL-interrupted executions
    else if (selected.checkpoint?.step_results) {
        Object.values(selected.checkpoint.step_results).forEach(step => {
            stepResults[step.step_id] = step;
        });
    }

    // Track the current step (blocked by HITL) for proper status display
    const currentStepId = selected.checkpoint?.current_step?.step_id;

    // Phase boundaries serialize iterative plans. Including those boundaries
    // prevents steps from different phases appearing at the same level.
    const levels = computeExecutionStepLevels(selected);
    let nodes = [];
    let edges = [];

    if (viewMode === 'full') {
        // === FULL FLOW MODE ===
        // Build complete execution flow: Orchestrator → LLM → [Checkpoint] → Steps → LLM → Response
        const agentName = selected.agent_name || 'orchestrator';
        const llmInteractions = selected.llm_interactions || [];
        const checkpoints = selected.hitl_checkpoints || [];
        const hookProjection = projectPipelineHookWrappers(selected.pipeline_hooks || [], llmInteractions);
        const storedPipelineHooks = hookProjection.standalone;

        // 1. Orchestrator node (root)
        nodes.push({
            data: {
                id: 'orchestrator',
                label: `🤖 ${agentName}`,
                nodeType: 'orchestrator'
            }
        });

        // 2. Phase-aware graph building: group LLM planning calls by phase, then
        // build per-phase subgraphs so each phase's planning → steps → boundary
        // topology is preserved in multi-phase iterative executions.
        const allPlanSteps = planSteps;
        const llmByPhase = groupLLMCallsByPhase(llmInteractions);
        const graphPhases = buildObservedPhases(selected, llmByPhase);
        const synthesisCalls = llmInteractions.filter(call => call.type === 'synthesis' || call.type === 'synthesis_streaming');
        const consumerPhases = new Map();
        for (const [phase, calls] of Object.entries(llmByPhase)) {
            for (const call of calls) consumerPhases.set(call, Number(phase));
        }
        const finalObservedPhase = graphPhases.at(-1)?.phase_number;
        if (finalObservedPhase) {
            for (const call of synthesisCalls) consumerPhases.set(call, finalObservedPhase);
        }
        const skillPlacement = assignSkillCallsToConsumers(
            llmInteractions, [...Object.values(llmByPhase).flat(), ...synthesisCalls], consumerPhases,
        );
        let skillNodeIndex = 0;
        const skillNode = (call, placementUnknown = false) => {
            const id = `llm_skill_${skillNodeIndex++}`;
            const config = getLLMType(call.type);
            nodes.push({ data: buildLLMBackedNodeData(call, {
                id, label: `${config.icon} ${config.label}`, nodeType: 'skill_llm',
                phaseNumber: call.phase_number, success: call.success !== false,
                error: call.error || '', placementUnknown,
            }) });
            return id;
        };
        // Only an observed selector/consumer interval creates an ordered edge.
        // The same helper handles planning and synthesis without classifying
        // every skill resource call as a planning call.
        const skillEntryFor = (consumer, consumerID) => {
            let first = '', previous = '';
            for (const call of skillPlacement.beforeCall.get(consumer) || []) {
                const id = skillNode(call);
                if (!first) first = id;
                if (previous) edges.push({ data: { source: previous, target: id, edgeType: 'skill_selection' } });
                previous = id;
            }
            if (previous) edges.push({ data: { source: previous, target: consumerID, edgeType: 'skill_selection' } });
            return first || consumerID;
        };
        for (const call of skillPlacement.unplacedCalls) {
            const id = skillNode(call, true);
            edges.push({ data: { source: 'orchestrator', target: id, edgeType: 'skill_evidence' } });
        }
        const afterPlanningByPhase = assignAfterPlanningHooksToPhases(
            storedPipelineHooks.filter(hook => hook.phase === 'after_planning'),
            graphPhases
        );

        // HITL plan-approval checkpoints (before_plan_execution) are attached
        // after Phase 1 planning LLM calls.
        const planCheckpoints = checkpoints.filter(c => c.interrupt_point === 'before_plan_execution');

        // Type labels live in static/js/llm-types.js — use getLLMType(type).label.

        let previousPhaseExitNode = 'orchestrator';
        let globalLLMIndex = 0;
        let memoryLLMIndex = 0;
        let pipelineHookIndex = 0;

        const appendPipelineHookChain = (hooks, sourceNodeIDs) => {
            const sources = Array.isArray(sourceNodeIDs) ? sourceNodeIDs : [sourceNodeIDs];
            let previousNodeID = '';
            hooks.forEach((hook, index) => {
                // Insert a detailed wrapper at this invocation's actual
                // position, not at the end of the stage. Its node is populated
                // below from the existing User Memory interaction group.
                const wrapperID = Object.keys(hookProjection.wrappers).find(id =>
                    hookProjection.wrappers[id].includes(hook)
                );
                const nodeId = wrapperID || `pipeline_hook_${pipelineHookIndex++}`;
                if (!wrapperID) nodes.push({ data: pipelineHookNodeData(hook, nodeId) });
                if (index === 0) {
                    sources.filter(Boolean).forEach(source => {
                        edges.push({ data: { source, target: nodeId, edgeType: 'pipeline_hook' } });
                    });
                } else {
                    edges.push({ data: { source: previousNodeID, target: nodeId, edgeType: 'pipeline_hook' } });
                }
                previousNodeID = nodeId;
            });
            return previousNodeID || sources[0] || '';
        };

        // Missing legacy placement is not a license to invent phase order.
        // Keep the record as a non-directional evidence branch from the root.
        unplacedPipelineHooks(storedPipelineHooks, graphPhases).forEach(hook => {
            const nodeId = `pipeline_hook_${pipelineHookIndex++}`;
            nodes.push({ data: { ...pipelineHookNodeData(hook, nodeId), placementUnknown: true } });
            edges.push({ data: { source: 'orchestrator', target: nodeId, edgeType: 'hook_evidence' } });
        });

        const beforePlanningHooks = (selected.pipeline_hooks || []).filter(hook => hook.phase === 'before_planning');
        if (beforePlanningHooks.length > 0) {
            previousPhaseExitNode = appendPipelineHookChain(beforePlanningHooks, previousPhaseExitNode);
        }

        // Pre-planning conversation-history and memory-hook calls
        const compactionCalls = llmInteractions.filter(i =>
            (
                i.type === 'conversation_history_prepare' ||
                i.type === 'conversation_history_compaction' ||
                i.type === 'activity_compaction_incremental'
            ) && !i.step_id
        );
        compactionCalls.forEach(call => {
            const nodeId = `llm_memory_${memoryLLMIndex++}`;
            const nodeIcon = call.type === 'conversation_history_prepare' ? '🛡️' :
                call.type === 'conversation_history_compaction' ? '🗜️' : '📝';
            nodes.push({
                data: buildLLMBackedNodeData(call, {
                    id: nodeId,
                    label: `${nodeIcon} ${getLLMType(call.type).label}`,
                    nodeType: 'memory_llm',
                })
            });
            edges.push({ data: { source: previousPhaseExitNode, target: nodeId, edgeType: 'memory_llm' } });
            previousPhaseExitNode = nodeId;
        });

        // User Memory: BeforePlanning group node (recall + enrichment steps)
        const userMemBeforeCalls = llmInteractions.filter(i =>
            i.type && (i.type.startsWith('user_memory_recall_') || i.type === 'user_memory_enrichment_injected')
        );
        if (userMemBeforeCalls.length > 0) {
            const totalDuration = userMemBeforeCalls.reduce((sum, c) => sum + (c.duration_ms || 0), 0);
            const nodeId = 'user_memory_before';
            nodes.push({
                data: {
                    id: nodeId,
                    label: `📝 User Memory: BeforePlanning (${userMemBeforeCalls.length} steps, ${formatDuration(totalDuration)})` +
                        (hookProjection.wrappers[nodeId]?.length ? `\n${pipelineHookConsequence(hookProjection.wrappers[nodeId][0]).nodeLabel}` : ''),
                    nodeType: 'user_memory_group',
                    pipelineStage: 'before_planning',
                    pipelineHooks: hookProjection.wrappers[nodeId] || [],
                    hookAction: hookProjection.wrappers[nodeId]?.[0]?.decision?.action || '',
                    hookStatus: hookProjection.wrappers[nodeId]?.[0]?.status || '',
                    steps: userMemBeforeCalls,
                    totalDuration: totalDuration,
                    stepCount: userMemBeforeCalls.length,
                }
            });
            if (!hookProjection.wrappers[nodeId]?.length) {
                edges.push({ data: { source: previousPhaseExitNode, target: nodeId, edgeType: 'memory_llm' } });
                previousPhaseExitNode = nodeId;
            }
        }

        let lastPhaseEntryNode = previousPhaseExitNode;
        if (graphPhases.length > 0) {
            // === Per-phase graph building ===
            for (let phaseIdx = 0; phaseIdx < graphPhases.length; phaseIdx++) {
                const phase = graphPhases[phaseIdx];
                const phaseNum = phase.phase_number;
                const phaseLLMCalls = llmByPhase[phaseNum] || [];
                const phaseSteps = phase.plan?.steps || [];
                const phaseStepIds = new Set(phaseSteps.map(s => s.step_id));

                // Phase-local topological levels for parallelism counts.
                // A step's `depends_on` may reference earlier-phase steps;
                // those are already-satisfied at phase entry, so we strip
                // them before topo-sorting — otherwise computeStepLevels
                // would never decrement their in-degree to zero and the
                // step would be silently dropped from the level list.
                // Without this scoping, parallelCount falls back to the
                // global plan's level width, which is incorrect for
                // multi-phase executions (the popup card showed total
                // parallelism across the whole execution rather than
                // within the phase).
                const phaseScopedSteps = phaseSteps.map(s => ({
                    ...s,
                    depends_on: (s.depends_on || []).filter(d => phaseStepIds.has(d)),
                }));
                const phaseLevels = computeStepLevels(phaseScopedSteps);

                // 2a. LLM planning nodes for this phase
                let lastLLMNodeInPhase = previousPhaseExitNode;
                phaseLLMCalls.forEach(call => {
                    const nodeId = `llm_plan_${globalLLMIndex++}`;
                    const entryNodeId = skillEntryFor(call, nodeId);
                    nodes.push({
                        data: buildLLMBackedNodeData(call, {
                            id: nodeId,
                            label: `💭 ${getLLMType(call.type).label}`,
                            nodeType: 'llm_call',
                            phaseNumber: phaseNum,
                        })
                    });
                    edges.push({ data: { source: lastLLMNodeInPhase, target: entryNodeId } });
                    lastLLMNodeInPhase = nodeId;
                });

                // Stored AfterPlanning hooks run after the phase plan is
                // generated and before any approval checkpoint or step from
                // that phase. The framework records the iterative plan phase
                // explicitly, so placement does not infer from timestamps.
                const phaseAfterPlanningHooks = afterPlanningByPhase[phaseIdx] || [];
                if (phaseAfterPlanningHooks.length > 0) {
                    lastLLMNodeInPhase = appendPipelineHookChain(
                        phaseAfterPlanningHooks,
                        lastLLMNodeInPhase
                    );
                }

                // 2b. HITL plan-approval checkpoint (Phase 1 only)
                if (phaseIdx === 0 && planCheckpoints.length > 0) {
                    planCheckpoints.forEach((cp, idx) => {
                        const nodeId = `checkpoint_plan_${idx}`;
                        const statusLabel = cp.status === 'approved' ? '✓' : cp.status === 'pending' ? '⏳' : '✗';
                        nodes.push({
                            data: {
                                id: nodeId,
                                label: `${statusLabel}`,
                                nodeType: 'checkpoint',
                                checkpointType: 'plan_approval',
                                status: cp.status
                            }
                        });
                        edges.push({ data: { source: lastLLMNodeInPhase, target: nodeId } });
                        lastLLMNodeInPhase = nodeId;
                    });
                }

                const stepEntryNode = lastLLMNodeInPhase;
                lastPhaseEntryNode = stepEntryNode;

                // 2c. Step nodes for this phase
                phaseSteps.forEach(step => {
                    const result = stepResults[step.step_id];
                    let status = 'pending';
                    if (result) {
                        if (result.skipped) status = 'skipped';
                        else if (result.success) status = 'completed';
                        else if (result.error) status = 'failed';
                    } else if (currentStepId === step.step_id) {
                        status = 'blocked';
                    }
                    const stepCapability = step.metadata?.capability || result?.metadata?.capability || step.capability || step.agent_name || '';
                    // Use phase-local levels so the popup card's "(N parallel)"
                    // count reflects parallelism within this phase, not across
                    // the whole execution.
                    const level = phaseLevels.findIndex(l => l.includes(step.step_id));
                    const parallelCount = level >= 0 ? phaseLevels[level].length : 1;
                    const resInfoFull = getResolutionInfo(result);
                    const baseLabelFull = step.agent_name || stepCapability;
                    const trimMetaFull = result?.metadata?.result_trim;
                    // A step can be compacted without deterministic source loss
                    // (for example, a distiller that analyzed the full source).
                    // Keep compaction visibility separate from the loss warning.
                    const wasTrimmedFull = hasTrimDisplay(trimMetaFull);
                    const trimLabelFull = wasTrimmedFull ? trimNodeLabel(trimMetaFull) : '';
                    const serviceName = result?.agent_name || step.agent_name || step.service_name || '';
                    const agentInvocation = isAgentInvocation(serviceName, stepCapability, invocationServices);
                    const stepLabel = baseLabelFull + (resInfoFull.summary ? '\n' + resInfoFull.summary : '') + trimLabelFull;
                    nodes.push({
                        data: {
                            id: step.step_id,
                            label: (agentInvocation ? '🤖 ' : '') + stepLabel,
                            stepLabel,
                            serviceName,
                            isAgentInvocation: agentInvocation,
                            nodeType: 'step',
                            status: status,
                            phaseNumber: phaseNum,
                            resolutionType: resInfoFull.type,
                            resolutionSummary: resInfoFull.summary,
                            duration: getDurationMs(result),
                            capability: stepCapability,
                            instruction: step.instruction || '',
                            level: level + 1,
                            parallelCount: parallelCount,
                            isParallel: parallelCount > 1,
                            dependsOn: step.depends_on || [],
                            implicitDeps: step.implicit_deps || [],
                            hasAutoIncludes: (result?.metadata?.template_auto_includes?.length > 0),
                            trimmed: !!wasTrimmedFull
                        }
                    });
                    // Root steps (no intra-phase deps) connect from stepEntryNode
                    const deps = step.depends_on || [];
                    const hasIntraPhaseDep = deps.some(d => phaseStepIds.has(d));
                    if (!hasIntraPhaseDep) {
                        edges.push({ data: { source: stepEntryNode, target: step.step_id } });
                    }
                    // Intra-phase dependency edges
                    deps.forEach(dep => {
                        if (phaseStepIds.has(dep)) {
                            const depResult = stepResults[dep];
                            const depFailed = depResult && !depResult.success;
                            edges.push({
                                data: {
                                    source: dep,
                                    target: step.step_id,
                                    edgeType: depFailed ? 'failed' : undefined
                                }
                            });
                        }
                    });
                    (step.implicit_deps || []).forEach(dep => {
                        edges.push({
                            data: {
                                source: dep,
                                target: step.step_id,
                                edgeType: 'implicit_dependency'
                            }
                        });
                    });
                });

                // 2d. Phase boundary node (links this phase's leaf steps to next phase)
                if (phaseIdx < graphPhases.length - 1) {
                    const boundaryId = `phase_boundary_${phaseIdx + 1}`;
                    const dependedOn = new Set();
                    phaseSteps.forEach(s => {
                        (s.depends_on || []).forEach(d => {
                            if (phaseStepIds.has(d)) dependedOn.add(d);
                        });
                    });
                    const leafStepsInPhase = phaseSteps.filter(s => !dependedOn.has(s.step_id));
                    const nextPhase = graphPhases[phaseIdx + 1];
                    const nextPhaseNum = nextPhase.phase_number;
                    nodes.push({
                        data: {
                            id: boundaryId,
                            label: `Phase ${phaseNum} → ${nextPhaseNum}`,
                            nodeType: 'phase_boundary',
                            phaseFrom: phaseNum,
                            phaseTo: nextPhaseNum,
                            continuationNote: nextPhase.plan?.continuation_note || ''
                        }
                    });
                    leafStepsInPhase.forEach(s => {
                        edges.push({ data: { source: s.step_id, target: boundaryId, edgeType: 'phase_transition' } });
                    });
                    if (phaseSteps.length === 0) {
                        edges.push({ data: { source: stepEntryNode, target: boundaryId, edgeType: 'phase_transition' } });
                    }
                    previousPhaseExitNode = boundaryId;
                }
            }
        }

        // Create stepIds set (needed by sections 4b, 4c, 4d below)
        const stepIds = new Set(allPlanSteps.map(s => s.step_id));

        // 4b. HITL Step-level checkpoints (before_step, after_step, on_error)
        // These checkpoints are associated with specific steps via current_step.step_id
        const stepCheckpoints = checkpoints.filter(c =>
            ['before_step', 'after_step', 'on_error'].includes(c.interrupt_point)
        );
        stepCheckpoints.forEach((cp, idx) => {
            const nodeId = `checkpoint_step_${idx}`;
            const parentStepId = cp.current_step?.step_id;

            // Determine visual label based on interrupt point
            let statusIcon, checkpointLabel;
            switch (cp.interrupt_point) {
                case 'before_step':
                    statusIcon = cp.status === 'approved' ? '▶' : cp.status === 'pending' ? '⏸️' : '⏹';
                    checkpointLabel = 'Before';
                    break;
                case 'after_step':
                    statusIcon = cp.status === 'approved' ? '✓' : cp.status === 'pending' ? '⏳' : '✗';
                    checkpointLabel = 'After';
                    break;
                case 'on_error':
                    statusIcon = cp.status === 'approved' ? '🔧' : cp.status === 'pending' ? '⚠️' : '❌';
                    checkpointLabel = 'Error';
                    break;
                default:
                    statusIcon = '◯';
                    checkpointLabel = cp.interrupt_point;
            }

            nodes.push({
                data: {
                    id: nodeId,
                    label: `${statusIcon}`,
                    nodeType: 'checkpoint',
                    checkpointType: cp.interrupt_point,
                    checkpointLabel: checkpointLabel,
                    status: cp.status,
                    parentStepId: parentStepId,
                    checkpointId: cp.checkpoint_id,
                    message: cp.message || '',
                    reason: cp.reason || ''
                }
            });

            // Connect to parent step if available
            if (parentStepId && stepIds.has(parentStepId)) {
                // For before_step: edge from step to checkpoint (shows pause before execution)
                // For after_step/on_error: edge from checkpoint to step (shows checkpoint after execution)
                if (cp.interrupt_point === 'before_step') {
                    // Insert checkpoint between the step's dependencies and the step
                    // We need to re-route: instead of dep -> step, do dep -> checkpoint -> step
                    // For simplicity, we'll show it as a child of the step with a dashed line
                    edges.push({ data: { source: parentStepId, target: nodeId, edgeType: 'checkpoint' } });
                } else {
                    // after_step and on_error: show as child of the step
                    edges.push({ data: { source: parentStepId, target: nodeId, edgeType: 'checkpoint' } });
                }
            }
        });

        // 4c. Step-specific LLM calls (micro_resolution and semantic_retry WITH step_id)
        // These are parameter resolution calls associated with specific execution steps
        const stepSpecificLLMCalls = filterInteractionsWithRenderedParent(
            llmInteractions.filter(i =>
                ['micro_resolution', 'semantic_retry', 'error_analysis', 'result_distillation', 'hallucination_detection'].includes(i.type) && i.step_id
            ),
            stepIds
        );
        stepSpecificLLMCalls.forEach((call, idx) => {
            const nodeId = `llm_step_${idx}`;
            const parentStepId = call.step_id;
            // Step-specific LLM types use a shorter label here than the
            // generic registry value (e.g. "Distill" vs. "Result Distillation"),
            // so we override only those four; everything else falls back to
            // the registry's icon + label.
            const stepLabelOverrides = {
                'semantic_retry': '🔄 Retry',
                'result_distillation': '🔬 Distill',
            };
            const cfg = getLLMType(call.type);
            const llmType = stepLabelOverrides[call.type] || `${cfg.icon} ${cfg.label}`;

            nodes.push({
                data: buildLLMBackedNodeData(call, {
                    id: nodeId,
                    label: llmType,
                    nodeType: 'llm_step',
                    parentStepId: parentStepId,
                    success: call.success !== false,
                    error: call.error || '',
                })
            });

            // Connect to parent step; carry llmType so edge can be styled per type
            if (parentStepId && stepIds.has(parentStepId)) {
                edges.push({ data: { source: parentStepId, target: nodeId, edgeType: 'llm_step', llmType: call.type } });
            }
        });

        // 4d. Agent-level LLM calls (agent_llm_call WITH step_id)
        // These are LLM calls made by agents during step execution
        const agentLLMCalls = llmInteractions.filter(i =>
            i.type === 'agent_llm_call' && i.step_id && i.source_component
        );
        agentLLMCalls.forEach((call, idx) => {
            const nodeId = `agent_llm_${idx}`;
            const parentStepId = call.step_id;
            const label = call.call_description
                ? `🔧 ${call.source_component}\n${call.call_description}`
                : `🔧 ${call.source_component}`;

            nodes.push({
                data: buildLLMBackedNodeData(call, {
                    id: nodeId,
                    label: label,
                    nodeType: 'agent_llm',
                    sourceComponent: call.source_component,
                    success: call.success !== false,
                    error: call.error || '',
                })
            });

            if (parentStepId && stepIds.has(parentStepId)) {
                edges.push({ data: { source: parentStepId, target: nodeId, edgeType: 'agent_llm' } });
            }
        });

        // 5b. Find leaf steps of the LAST phase for synthesis/response connection.
        // For multi-phase executions only last-phase leaves feed into synthesis.
        const lastPhasePlanSteps = graphPhases.at(-1)?.plan?.steps || [];
        const lastPhaseStepIds = new Set(lastPhasePlanSteps.map(s => s.step_id));
        const lastPhaseDependedOn = new Set();
        lastPhasePlanSteps.forEach(s => {
            (s.depends_on || []).forEach(d => {
                if (lastPhaseStepIds.has(d)) lastPhaseDependedOn.add(d);
            });
        });
        const leafSteps = lastPhasePlanSteps.filter(s => !lastPhaseDependedOn.has(s.step_id));

        // A zero-step phase can end at a hook or checkpoint, not only an LLM
        // call. Keep its last actual entry node as the tail's source.
        const lastPlanningNodeId = lastPhaseEntryNode;

        // Deterministic AfterExecution hooks do not necessarily emit an LLM
        // interaction. Place their stored nodes after the terminal step
        // leaves and before synthesis. Hooks whose internal activity already
        // has richer graph nodes were filtered above.
        const afterExecutionHooks = storedPipelineHooks.filter(hook => hook.phase === 'after_execution');
        let afterExecutionHookExit = '';
        if (afterExecutionHooks.length > 0) {
            const sources = leafSteps.length > 0
                ? leafSteps.map(step => step.step_id)
                : [lastPlanningNodeId];
            afterExecutionHookExit = appendPipelineHookChain(afterExecutionHooks, sources);
        }

        // 5c. Post-execution memory hook LLM calls (event summarization)
        let eventSumIndex = 0;
        const eventSumCalls = llmInteractions.filter(i =>
            i.type === 'event_summarization' && !i.step_id
        );
        const eventSumNodes = [];
        if (eventSumCalls.length > 0) {
            eventSumCalls.forEach((call, idx) => {
                const nodeId = `llm_evtsum_${eventSumIndex++}`;
                nodes.push({
                    data: buildLLMBackedNodeData(call, {
                        id: nodeId,
                        label: `📝 ${getLLMType(call.type).label}`,
                        nodeType: 'memory_llm',
                    })
                });
                if (idx === 0) {
                    if (afterExecutionHookExit) {
                        edges.push({ data: { source: afterExecutionHookExit, target: nodeId, edgeType: 'memory_llm' } });
                    } else if (leafSteps.length > 0) {
                        leafSteps.forEach(step => {
                            const result = stepResults[step.step_id];
                            const isFailed = result && !result.success;
                            edges.push({
                                data: {
                                    source: step.step_id,
                                    target: nodeId,
                                    edgeType: isFailed ? 'failed' : 'memory_llm'
                                }
                            });
                        });
                    } else {
                        // Synthesis-only final phase: the last phase emitted zero
                        // steps (the planner's continuation decided no further work,
                        // just synthesize), so there are no leaf steps to feed this
                        // post-execution memory hook. Without a fallback edge the
                        // event-summarization node — and the synthesis + response
                        // chain that hangs off it — float away as an orphaned
                        // component ("disconnect from the event summary box onwards").
                        // Anchor it to the last upstream node, mirroring the
                        // synthesis fallback below, so the tail stays connected.
                        edges.push({ data: { source: lastPlanningNodeId, target: nodeId, edgeType: 'memory_llm' } });
                    }
                } else {
                    edges.push({ data: { source: `llm_evtsum_${idx - 1}`, target: nodeId, edgeType: 'memory_llm' } });
                }
                eventSumNodes.push(nodeId);
            });
        }

        // 6. LLM calls for synthesis (includes synthesis and synthesis_streaming)
        if (synthesisCalls.length > 0) {
            synthesisCalls.forEach((call, idx) => {
                const nodeId = `llm_synth_${idx}`;
                const entryNodeId = skillEntryFor(call, nodeId);
                const isStreaming = call.type === 'synthesis_streaming';
                nodes.push({
                    data: buildLLMBackedNodeData(call, {
                        id: nodeId,
                        label: isStreaming ? '💭 Synthesis (Stream)' : '💭 Synthesis',
                        nodeType: 'llm_call',
                    })
                });
                if (idx === 0) {
                    if (eventSumNodes.length > 0) {
                        // Chain from last event summarization node
                        edges.push({ data: { source: eventSumNodes[eventSumNodes.length - 1], target: entryNodeId } });
                    } else if (afterExecutionHookExit) {
                        edges.push({ data: { source: afterExecutionHookExit, target: entryNodeId, edgeType: 'pipeline_hook' } });
                    } else if (leafSteps.length > 0) {
                        // Connect leaf steps directly to synthesis
                        leafSteps.forEach(step => {
                            const result = stepResults[step.step_id];
                            const isFailed = result && !result.success;
                            edges.push({
                                data: {
                                    source: step.step_id,
                                    target: entryNodeId,
                                    edgeType: isFailed ? 'failed' : undefined
                                }
                            });
                        });
                    } else {
                        // Clarification short-circuit case — the last phase
                        // had zero steps because the planner emitted NeedsUserInput.
                        // Connect synthesis from the last planning node so the DAG
                        // remains a single connected graph instead of two orphaned
                        // components (planning chain | synthesis chain).
                        edges.push({ data: { source: lastPlanningNodeId, target: entryNodeId } });
                    }
                } else {
                    edges.push({ data: { source: `llm_synth_${idx - 1}`, target: entryNodeId } });
                }
            });
        }

        // 7. Response node
        // Source resolution priority:
        //   1. Last synthesis call (normal happy path)
        //   2. Last event summarization (rare — synthesis disabled)
        //   3. First leaf step (synthesis-less single-phase)
        //   4. Last planning node (clarification short-circuit edge case
        //      where synthesis was somehow skipped — defensive)
        //   5. Orchestrator root (fully degenerate trace)
        let responseSource = synthesisCalls.length > 0 ? `llm_synth_${synthesisCalls.length - 1}` :
            (eventSumNodes.length > 0 ? eventSumNodes[eventSumNodes.length - 1] :
            (afterExecutionHookExit ? afterExecutionHookExit :
            (leafSteps.length > 0 ? leafSteps[0].step_id :
            (lastPlanningNodeId !== 'orchestrator' ? lastPlanningNodeId : 'orchestrator'))));

        // AfterSynthesis hooks run after the model draft and before the
        // terminal application response. Their stored records preserve
        // deterministic response-governance outcomes without tracing.
        const afterSynthesisHooks = (selected.pipeline_hooks || []).filter(hook => hook.phase === 'after_synthesis');
        if (afterSynthesisHooks.length > 0) {
            const hookSources = synthesisCalls.length > 0 || eventSumNodes.length > 0 || afterExecutionHookExit
                ? [responseSource]
                : (leafSteps.length > 0 ? leafSteps.map(step => step.step_id) : [responseSource]);
            responseSource = appendPipelineHookChain(afterSynthesisHooks, hookSources);
        }
        const successLabel = executionStatus(selected).label;
        nodes.push({
            data: {
                id: 'response',
                label: `📤 ${successLabel}${recordedClarification(selected) ? `\n${clarificationOutcomeLabel(selected)}` : ''}`,
                nodeType: 'response',
                lifecycleState: executionStatus(selected).state,
                success: executionStatus(selected).state === 'completed',
                interrupted: selected.interrupted ? 'true' : 'false'
            }
        });
        if (synthesisCalls.length > 0 || eventSumNodes.length > 0 || afterExecutionHookExit || afterSynthesisHooks.length > 0) {
            edges.push({ data: { source: responseSource, target: 'response' } });
        } else if (leafSteps.length > 0) {
            // No synthesis or event summarization - connect leaf steps directly to response
            // Failed steps get dashed red edges (but not interrupted ones)
            leafSteps.forEach(step => {
                const result = stepResults[step.step_id];
                const isFailed = result && !result.success && !selected.interrupted;
                edges.push({
                    data: {
                        source: step.step_id,
                        target: 'response',
                        edgeType: isFailed ? 'failed' : undefined
                    }
                });
            });
        } else {
            // Defensive case: no synthesis, no event summarization, no leaf
            // steps (clarification short-circuit with synthesis disabled — not
            // currently reachable in the orchestrator but kept connected so a
            // future config that disables synthesis still produces a valid DAG).
            edges.push({ data: { source: responseSource, target: 'response' } });
        }

        // User Memory: AfterSynthesis group node (extraction + reconciliation + summary)
        const userMemAfterCalls = llmInteractions.filter(i =>
            i.type && i.type.startsWith('user_memory_') &&
            !i.type.startsWith('user_memory_recall_') &&
            i.type !== 'user_memory_enrichment_injected'
        );
        if (userMemAfterCalls.length > 0) {
            const totalDuration = userMemAfterCalls.reduce((sum, c) => sum + (c.duration_ms || 0), 0);
            const llmCount = userMemAfterCalls.filter(c =>
                c.category === 'llm' || ['user_memory_extraction', 'user_memory_reconciliation', 'user_memory_summary'].includes(c.type)
            ).length;
            const nodeId = 'user_memory_after';
            nodes.push({
                data: {
                    id: nodeId,
                    label: `📝 User Memory: AfterSynthesis (${userMemAfterCalls.length} steps, ${formatDuration(totalDuration)}${llmCount ? `, ${llmCount} LLM` : ''})` +
                        (hookProjection.wrappers[nodeId]?.length ? `\n${pipelineHookConsequence(hookProjection.wrappers[nodeId][0]).nodeLabel}` : ''),
                    nodeType: 'user_memory_group',
                    pipelineStage: 'after_synthesis',
                    pipelineHooks: hookProjection.wrappers[nodeId] || [],
                    hookAction: hookProjection.wrappers[nodeId]?.[0]?.decision?.action || '',
                    hookStatus: hookProjection.wrappers[nodeId]?.[0]?.status || '',
                    steps: userMemAfterCalls,
                    totalDuration: totalDuration,
                    stepCount: userMemAfterCalls.length,
                    llmCount: llmCount,
                }
            });
            if (!hookProjection.wrappers[nodeId]?.length) {
                edges.push({ data: { source: 'response', target: nodeId, edgeType: 'memory_llm' } });
            }
        }

    } else {
        // === STEPS ONLY MODE (original behavior) ===
        nodes = planSteps.map(step => {
            const result = stepResults[step.step_id];
            // Determine step status: completed/failed/skipped from result, blocked if current HITL step, pending otherwise
            let status = 'pending';
            if (result) {
                if (result.skipped) status = 'skipped';
                else if (result.success) status = 'completed';
                else if (result.error) status = 'failed';
            } else if (currentStepId === step.step_id) {
                status = 'blocked'; // Awaiting HITL approval
            }

            // Find the level (depth) of this step
            const level = levels.findIndex(l => l.includes(step.step_id));
            const parallelCount = level >= 0 ? levels[level].length : 1;
            const isParallel = parallelCount > 1;

            // Get capability from metadata (where orchestration stores it)
            const stepCapability = step.metadata?.capability || result?.metadata?.capability || step.capability || step.agent_name || '';

            const resInfo = getResolutionInfo(result);
            const baseLabel = step.agent_name || stepCapability;
            const serviceName = result?.agent_name || step.agent_name || step.service_name || '';
            const agentInvocation = isAgentInvocation(serviceName, stepCapability, invocationServices);
            const stepLabel = baseLabel + (resInfo.summary ? '\n' + resInfo.summary : '');
            return {
                data: {
                    id: step.step_id,
                    label: (agentInvocation ? '🤖 ' : '') + stepLabel,
                    stepLabel,
                    serviceName,
                    isAgentInvocation: agentInvocation,
                    capability: stepCapability,
                    instruction: step.instruction || '',
                    status: status,
                    resolutionType: resInfo.type,
                    resolutionSummary: resInfo.summary,
                    duration: getDurationMs(result),
                    level: level + 1,
                    parallelCount: parallelCount,
                    isParallel: isParallel,
                    dependsOn: step.depends_on || [],
                    implicitDeps: step.implicit_deps || [],
                    hasAutoIncludes: (result?.metadata?.template_auto_includes?.length > 0)
                }
            };
        });

        // Build edges for steps mode
        // If source step failed, mark the edge as failed to show broken data flow
        planSteps.forEach(step => {
            (step.depends_on || []).forEach(dep => {
                const depResult = stepResults[dep];
                const depFailed = depResult && !depResult.success;
                edges.push({
                    data: {
                        source: dep,
                        target: step.step_id,
                        edgeType: depFailed ? 'failed' : undefined
                    }
                });
            });
            (step.implicit_deps || []).forEach(dep => {
                edges.push({
                    data: {
                        source: dep,
                        target: step.step_id,
                        edgeType: 'implicit_dependency'
                    }
                });
            });
        });

        // Phase boundary nodes for multi-phase executions (Steps Only mode)
        if (selected.phase_plans && selected.phase_plans.length > 1) {
            const phasePlans = selected.phase_plans;
            let cumulativeStepCount = 0;

            for (let pi = 0; pi < phasePlans.length - 1; pi++) {
                const currentPhaseSteps = phasePlans[pi].steps || [];
                cumulativeStepCount += currentPhaseSteps.length;
                const nextPhaseSteps = phasePlans[pi + 1].steps || [];

                // Current phase step IDs
                const currentPhaseStepIds = new Set(currentPhaseSteps.map(s => s.step_id));
                const nextPhaseStepIds = new Set(nextPhaseSteps.map(s => s.step_id));

                // Leaf steps of current phase: not depended on by any other step in same phase
                const currentPhaseDependents = new Set();
                currentPhaseSteps.forEach(s => {
                    (s.depends_on || []).forEach(d => { if (currentPhaseStepIds.has(d)) currentPhaseDependents.add(d); });
                });
                const phaseLeafSteps = currentPhaseSteps.filter(s => !currentPhaseDependents.has(s.step_id));

                // Root steps of next phase: no depends_on, or depends only on prior-phase steps
                const phaseRootSteps = nextPhaseSteps.filter(s => {
                    const deps = s.depends_on || [];
                    return deps.length === 0 || deps.every(d => !nextPhaseStepIds.has(d));
                });

                const continuationNote = phasePlans[pi + 1].continuation_note || '';
                const boundaryId = `phase_boundary_${pi + 1}`;
                nodes.push({
                    data: {
                        id: boundaryId,
                        label: `Phase ${pi + 1} → ${pi + 2}`,
                        nodeType: 'phase_boundary',
                        phaseFrom: pi + 1,
                        phaseTo: pi + 2,
                        continuationNote: continuationNote
                    }
                });

                // Edges: leaf steps → boundary
                phaseLeafSteps.forEach(s => {
                    edges.push({ data: { source: s.step_id, target: boundaryId, edgeType: 'phase_transition' } });
                });
                // Edges: boundary → root steps of next phase
                phaseRootSteps.forEach(s => {
                    edges.push({ data: { source: boundaryId, target: s.step_id, edgeType: 'phase_transition' } });
                });
            }
        }
    }

    // Cytoscape rejects the entire graph when even one edge references a node
    // outside the current execution. This can happen legitimately on a HITL
    // resume when an implicit provenance dependency points to a step stored on
    // the interrupted parent record. Keep the current record renderable while
    // retaining a diagnostic for malformed or cross-record edges.
    const filteredEdges = filterRenderableEdges(nodes, edges);
    edges = filteredEdges.renderable;
    if (filteredEdges.omitted.length > 0) {
        console.warn(
            `Omitted ${filteredEdges.omitted.length} DAG edge(s) whose endpoints are not present in this execution`,
            filteredEdges.omitted.map(edge => edge.data)
        );
    }

    // Create Cytoscape instance with premium dark styling
    const graph = cytoscape({
        container: dagContainer,
        elements: { nodes, edges },
        style: [
            {
                // Base node style - glassy dark look
                selector: 'node',
                style: {
                    'label': 'data(label)',
                    'text-valign': 'center',
                    'text-halign': 'center',
                    'background-color': '#151820',
                    'background-opacity': 0.7,
                    'color': '#e8eaed',
                    'font-size': '11px',
                    'font-weight': '500',
                    'font-family': '-apple-system, BlinkMacSystemFont, "SF Pro Display", sans-serif',
                    'width': '188px',
                    'height': '68px',
                    'shape': 'round-rectangle',
                    'text-wrap': 'wrap',
                    'text-max-width': '169px',
                    // Allow breaks at any character so long single-token names
                    // (e.g. "research-agent-telemetry-service") wrap to a
                    // second line instead of overflowing the node border.
                    'text-overflow-wrap': 'anywhere',
                    'border-width': '1px',
                    'border-color': '#3a4556',
                    'border-opacity': 0.8,
                    'text-outline-color': '#0d1117',
                    'text-outline-width': '1px',
                    'overlay-padding': '6px',
                    'z-index': 10
                }
            },
            {
                // Completed - glassy green tint
                selector: 'node[status="completed"]',
                style: {
                    'background-color': '#0f2818',
                    'background-opacity': 0.65,
                    'border-color': '#32d74b',
                    'border-width': '2px',
                    'border-opacity': 0.9,
                    'color': '#32d74b'
                }
            },
            {
                // Failed - glassy red tint
                selector: 'node[status="failed"]',
                style: {
                    'background-color': '#2a0f0f',
                    'background-opacity': 0.65,
                    'border-color': '#ff6b6b',
                    'border-width': '2px',
                    'border-opacity': 0.9,
                    'color': '#ff6b6b'
                }
            },
            {
                // Pending - glassy amber tint
                selector: 'node[status="pending"]',
                style: {
                    'background-color': '#2a1f0a',
                    'background-opacity': 0.65,
                    'border-color': '#ffb340',
                    'border-width': '2px',
                    'border-opacity': 0.9,
                    'color': '#ffb340'
                }
            },
            {
                // Skipped - glassy muted gray
                selector: 'node[status="skipped"]',
                style: {
                    'background-color': '#12151a',
                    'background-opacity': 0.5,
                    'border-color': '#3a4556',
                    'border-width': '1px',
                    'border-opacity': 0.6,
                    'color': '#6b7280'
                }
            },
            {
                // Blocked - glassy blue tint (awaiting HITL approval)
                selector: 'node[status="blocked"]',
                style: {
                    'background-color': '#0a1a2e',
                    'background-opacity': 0.65,
                    'border-color': '#0a84ff',
                    'border-width': '3px',
                    'border-style': 'dashed',
                    'border-opacity': 0.9,
                    'color': '#0a84ff'
                }
            },
            {
                // Agent identity is independent of outcome: purple fill matches
                // the starting agent, while failure/pending/HITL borders remain.
                selector: 'node[?isAgentInvocation]',
                style: {
                    'background-color': '#3d1a4d',
                    'background-opacity': 0.7,
                    'color': '#ffffff'
                }
            },
            {
                selector: 'node[?isAgentInvocation][status="completed"]',
                style: { 'border-color': '#da8fff' }
            },
            {
                // Template auto-include safety net fired - amber dashed border overlay
                selector: 'node[?hasAutoIncludes]',
                style: {
                    'border-style': 'dashed',
                    'border-color': 'rgba(255, 140, 50, 0.6)',
                    'border-width': '3px'
                }
            },
            {
                // Result trimming was applied to this step - purple dotted border overlay
                selector: 'node[?trimmed]',
                style: {
                    'border-style': 'dotted',
                    'border-color': 'rgba(130, 90, 220, 0.7)',
                    'border-width': '2px'
                }
            },
            {
                // Selected - glassy blue highlight
                selector: 'node:selected',
                style: {
                    'border-width': '2px',
                    'border-color': '#0a84ff',
                    'border-opacity': 1,
                    'background-color': '#0a1a2e',
                    'background-opacity': 0.8,
                    'color': '#ffffff'
                }
            },
            {
                selector: 'node:active',
                style: {
                    'overlay-color': '#0a84ff',
                    'overlay-opacity': 0.2
                }
            },
            // === Resolution Type Visual Indicators ===
            // Uses underlay glow behind the glass node body for a
            // glassmorphism-compatible depth effect (alpha 0.12-0.15).
            {
                // Auto-wire only - green glow (fast, no LLM cost)
                selector: 'node[resolutionType="auto_wire_only"]',
                style: {
                    'underlay-color': '#32d74b',
                    'underlay-opacity': 0.12,
                    'underlay-padding': '5px',
                    'underlay-shape': 'round-rectangle'
                }
            },
            {
                // LLM resolved - blue glow (LLM micro-resolution used)
                selector: 'node[resolutionType="llm_resolved"]',
                style: {
                    'underlay-color': '#64a0ff',
                    'underlay-opacity': 0.12,
                    'underlay-padding': '5px',
                    'underlay-shape': 'round-rectangle'
                }
            },
            {
                // Semantic retry - purple glow (error recovery used)
                selector: 'node[resolutionType="semantic_retry"]',
                style: {
                    'underlay-color': '#af82ff',
                    'underlay-opacity': 0.15,
                    'underlay-padding': '5px',
                    'underlay-shape': 'round-rectangle'
                }
            },
            {
                // Edges - subtle teal lines
                selector: 'edge',
                style: {
                    'width': 2,
                    'line-color': '#3d5a6e',
                    'target-arrow-color': '#64d2ff',
                    'target-arrow-shape': 'triangle',
                    'curve-style': 'bezier',
                    'arrow-scale': 1.2,
                    'line-opacity': 0.8
                }
            },
            {
                selector: 'edge:selected',
                style: {
                    'line-color': '#0a84ff',
                    'target-arrow-color': '#0a84ff',
                    'width': 3
                }
            },
            // === Full Flow Node Types ===
            {
                selector: 'node[nodeType="skill_llm"]',
                style: {
                    'background-color': '#2c3316',
                    'border-color': getLLMType('skill_activation_selection').accent,
                    'border-width': 2,
                    'color': '#f3f7df',
                    'shape': 'round-rectangle',
                    'width': 170,
                    'height': 45,
                }
            },
            {
                // Orchestrator Agent - purple hexagon style
                selector: 'node[nodeType="orchestrator"]',
                style: {
                    'background-color': '#3d1a4d',
                    'border-color': '#da8fff',
                    'border-width': '2px',
                    'color': '#ffffff',
                    'shape': 'round-rectangle',
                    'width': '200px',
                    'height': '63px'
                }
            },
            {
                // LLM Call - blue pill
                selector: 'node[nodeType="llm_call"]',
                style: {
                    'background-color': '#1a3a5c',
                    'border-color': '#0a84ff',
                    'border-width': '2px',
                    'color': '#ffffff',
                    'shape': 'round-rectangle',
                    'width': '175px',
                    'height': '55px'
                }
            },
            {
                // Step-specific LLM Call - teal pill (micro_resolution attached to step)
                selector: 'node[nodeType="llm_step"][llmType="micro_resolution"]',
                style: {
                    'background-color': '#0d3d4d',
                    'border-color': '#64d2ff',
                    'border-width': '2px',
                    'color': '#ffffff',
                    'shape': 'round-rectangle',
                    'width': '150px',
                    'height': '45px'
                }
            },
            {
                // Step-specific LLM Call - pink pill (semantic_retry attached to step)
                selector: 'node[nodeType="llm_step"][llmType="semantic_retry"]',
                style: {
                    'background-color': '#4d1a3d',
                    'border-color': '#ff6eb4',
                    'border-width': '2px',
                    'color': '#ffffff',
                    'shape': 'round-rectangle',
                    'width': '150px',
                    'height': '45px'
                }
            },
            {
                // Hallucination Detection - red pill (indicates LLM generated non-existent agent reference)
                selector: 'node[nodeType="llm_step"][llmType="hallucination_detection"]',
                style: {
                    'background-color': '#4d1a1a',
                    'border-color': '#ff5050',
                    'border-width': '2px',
                    'color': '#ffffff',
                    'shape': 'round-rectangle',
                    'width': '150px',
                    'height': '45px'
                }
            },
            {
                // Error Analysis - amber-red pill (error recovery LLM call attached to a failed step)
                selector: 'node[nodeType="llm_step"][llmType="error_analysis"]',
                style: {
                    'background-color': '#4d1f1a',
                    'border-color': '#ff6b6b',
                    'border-width': '2px',
                    'color': '#ffffff',
                    'shape': 'round-rectangle',
                    'width': '150px',
                    'height': '45px'
                }
            },
            {
                // Result Distillation - indigo pill (large-result summarization attached to a step)
                selector: 'node[nodeType="llm_step"][llmType="result_distillation"]',
                style: {
                    'background-color': '#28184d',
                    'border-color': '#8250dc',
                    'border-width': '2px',
                    'color': '#ffffff',
                    'shape': 'round-rectangle',
                    'width': '150px',
                    'height': '45px'
                }
            },
            {
                // Agent LLM Call - purple pill (agent-level LLM calls)
                selector: 'node[nodeType="agent_llm"]',
                style: {
                    'background-color': '#2d1a4d',
                    'border-color': '#a064f0',
                    'border-width': '2px',
                    'color': '#ffffff',
                    'shape': 'round-rectangle',
                    'width': '175px',
                    'height': '50px'
                }
            },
            {
                // Memory LLM Call - amber pill (activity compaction, event summarization)
                selector: 'node[nodeType="memory_llm"]',
                style: {
                    'background-color': '#3d2e0a',
                    'border-color': '#f0a030',
                    'border-width': '2px',
                    'color': '#ffffff',
                    'shape': 'round-rectangle',
                    'width': '175px',
                    'height': '55px'
                }
            },
            {
                // User Memory group — collapsed container node, matches memory_llm aesthetic
                // Dark glassy background with amber border (same as other memory nodes)
                selector: 'node[nodeType="user_memory_group"]',
                style: {
                    'shape': 'round-rectangle',
                    'width': '350px',
                    'height': '63px',
                    'background-color': '#3d2e0a',
                    'background-opacity': 0.7,
                    'border-width': '2px',
                    'border-style': 'dashed',
                    'border-color': '#f0a030',
                    'border-opacity': 0.8,
                    'color': '#f0a030',
                    'text-valign': 'center',
                    'text-halign': 'center',
                    'font-size': '10px',
                    'text-outline-color': '#0d1117',
                    'text-outline-width': '1px',
                    'text-wrap': 'wrap',
                    'text-max-width': '325px'
                    // Note: 'cursor' is not a valid Cytoscape style property
                    // (it's a CSS property, not a Cytoscape one). Cytoscape
                    // automatically shows a pointer cursor on tappable nodes
                    // and edges, so this was both invalid and unnecessary.
                }
            },
            {
                // Stored framework pipeline hook. This remains visible
                // even when the hook is deterministic and emits no LLM record.
                selector: 'node[nodeType="pipeline_hook"]',
                style: {
                    'shape': 'round-rectangle',
                    'width': '220px',
                    'height': '58px',
                    'background-color': '#112c35',
                    'border-color': '#64d2ff',
                    'border-width': '2px',
                    'border-style': 'dashed',
                    'color': '#b9eaff',
                    'font-size': '10px',
                    'text-wrap': 'wrap',
                    'text-max-width': '200px'
                }
            },
            {
                selector: 'node[nodeType="pipeline_hook"][hookStatus="failed"]',
                style: {
                    'background-color': '#3d1717',
                    'border-color': '#ff6b6b',
                    'color': '#ffd0d0'
                }
            },
            {
                selector: 'node[nodeType="pipeline_hook"][hookStatus="skipped"]',
                style: {
                    'background-color': '#292d33',
                    'border-color': '#8b949e',
                    'color': '#c2c9d1'
                }
            },
            {
                // A failed optional callback continued; do not style it as a
                // stopping boundary. Invocation details still retain failure.
                selector: 'node[hookAction="continue"][hookStatus="failed"]',
                style: {
                    'background-color': '#3d2e0a',
                    'border-color': '#f0a030',
                    'color': '#ffe0a3'
                }
            },
            {
                // Includes skipped required invocations and grouped wrappers.
                selector: 'node[hookAction="terminate"], node[hookAction="propagate_panic"]',
                style: {
                    'background-color': '#3d1717',
                    'border-color': '#ff6b6b',
                    'color': '#ffd0d0'
                }
            },
            {
                // HITL Checkpoint - default orange diamond style (plan approval)
                selector: 'node[nodeType="checkpoint"]',
                style: {
                    'background-color': '#4d3a1a',
                    'border-color': '#ffb340',
                    'border-width': '2px',
                    'color': '#ffffff',
                    'shape': 'diamond',
                    'width': '100px',
                    'height': '100px',
                    'text-valign': 'center',
                    'font-size': '10px'
                }
            },
            {
                // HITL Step Checkpoint - before_step (gold/pause)
                selector: 'node[checkpointType="before_step"]',
                style: {
                    'background-color': '#4d4a1a',
                    'border-color': '#ffd60a',
                    'width': '75px',
                    'height': '75px',
                    'font-size': '14px'
                }
            },
            {
                // HITL Step Checkpoint - after_step (green/complete)
                selector: 'node[checkpointType="after_step"]',
                style: {
                    'background-color': '#1a4d2e',
                    'border-color': '#30d158',
                    'width': '75px',
                    'height': '75px',
                    'font-size': '14px'
                }
            },
            {
                // HITL Step Checkpoint - on_error (red/warning)
                selector: 'node[checkpointType="on_error"]',
                style: {
                    'background-color': '#4d1a1a',
                    'border-color': '#ff453a',
                    'width': '75px',
                    'height': '75px',
                    'font-size': '14px'
                }
            },
            {
                // Checkpoint edge style (dashed line)
                selector: 'edge[edgeType="checkpoint"]',
                style: {
                    'line-style': 'dashed',
                    'line-dash-pattern': [6, 3],
                    'line-color': '#ffb340',
                    'target-arrow-color': '#ffb340',
                    'opacity': 0.7
                }
            },
            {
                // Step-specific LLM edge style (dotted teal line)
                selector: 'edge[edgeType="llm_step"]',
                style: {
                    'line-style': 'dotted',
                    'line-dash-pattern': [3, 3],
                    'line-color': '#64d2ff',
                    'target-arrow-color': '#64d2ff',
                    'opacity': 0.8
                }
            },
            {
                // Semantic retry edge - pink dotted
                selector: 'edge[edgeType="llm_step"][llmType="semantic_retry"]',
                style: {
                    'line-style': 'dotted',
                    'line-dash-pattern': [3, 3],
                    'line-color': '#ff6eb4',
                    'target-arrow-color': '#ff6eb4',
                    'opacity': 0.8
                }
            },
            {
                // Error analysis edge - red dotted
                selector: 'edge[edgeType="llm_step"][llmType="error_analysis"]',
                style: {
                    'line-style': 'dotted',
                    'line-dash-pattern': [3, 3],
                    'line-color': '#ff6b6b',
                    'target-arrow-color': '#ff6b6b',
                    'opacity': 0.8
                }
            },
            {
                // Result distillation edge - indigo dotted
                selector: 'edge[edgeType="llm_step"][llmType="result_distillation"]',
                style: {
                    'line-style': 'dotted',
                    'line-dash-pattern': [3, 3],
                    'line-color': '#8250dc',
                    'target-arrow-color': '#8250dc',
                    'opacity': 0.8
                }
            },
            {
                // Agent LLM edge style (dashed purple line)
                selector: 'edge[edgeType="agent_llm"]',
                style: {
                    'line-color': 'rgba(160, 100, 240, 0.5)',
                    'target-arrow-color': 'rgba(160, 100, 240, 0.7)',
                    'line-style': 'dashed',
                    'line-dash-pattern': [4, 4]
                }
            },
            {
                // Memory LLM edge style (dashed amber line)
                selector: 'edge[edgeType="memory_llm"]',
                style: {
                    'line-color': 'rgba(240, 160, 48, 0.5)',
                    'target-arrow-color': 'rgba(240, 160, 48, 0.7)',
                    'line-style': 'dashed',
                    'line-dash-pattern': [5, 3]
                }
            },
            {
                // Failed step edge style (dashed red line with X marker)
                selector: 'edge[edgeType="failed"]',
                style: {
                    'line-style': 'dashed',
                    'line-dash-pattern': [4, 4],
                    'line-color': '#ff6b6b',
                    'target-arrow-color': '#ff6b6b',
                    'opacity': 0.6,
                    'width': 1.5
                }
            },
            {
                // Response - teal rounded rectangle
                selector: 'node[nodeType="response"]',
                style: {
                    'background-color': '#1a4d4d',
                    'border-color': '#64d2ff',
                    'border-width': '2px',
                    'color': '#ffffff',
                    'shape': 'round-rectangle',
                    'width': '175px',
                    'height': '55px'
                }
            },
            {
                selector: 'node[nodeType="response"][lifecycleState="failed"], node[nodeType="response"][lifecycleState="cancelled"]',
                style: { 'background-color': '#2a0f0f', 'border-color': '#ff6b6b', 'color': '#ff6b6b' }
            },
            {
                selector: 'node[nodeType="response"][lifecycleState="running"], node[nodeType="response"][lifecycleState="unknown"]',
                style: { 'background-color': '#2a1f0a', 'border-color': '#ffb340', 'color': '#ffb340' }
            },
            {
                // Response node when execution is interrupted (HITL pause) - amber/pause style
                selector: 'node[nodeType="response"][lifecycleState="suspended"]',
                style: {
                    'background-color': '#2a1f0a',
                    'border-color': '#ffb340',
                    'border-width': '2px',
                    'border-style': 'dashed',
                    'color': '#ffb340',
                    'width': '200px'
                }
            },
            {
                // Execution phase container (visual grouping)
                selector: 'node[nodeType="step"]',
                style: {
                    'width': '188px',
                    'height': '58px'
                }
            },
            {
                // Phase boundary node (multi-phase iterative planning)
                selector: 'node[nodeType="phase_boundary"]',
                style: {
                    'shape': 'round-rectangle',
                    'width': '225px',
                    'height': '45px',
                    'background-color': '#0d2a3d',
                    'border-color': '#64d2ff',
                    'border-width': '2px',
                    'border-style': 'dashed',
                    'color': '#ffffff',
                    'label': 'data(label)',
                    'text-valign': 'center',
                    'text-halign': 'center',
                    'font-size': '11px',
                    'font-weight': '600',
                    'text-outline-color': '#0d2a3d',
                    'text-outline-width': '2px'
                }
            },
            {
                // Cross-phase data provenance. This is advisory and does not
                // express scheduler ordering inside a phase.
                selector: 'edge[edgeType="implicit_dependency"]',
                style: {
                    'line-color': DAG_PROVENANCE_COLOR,
                    'target-arrow-color': DAG_PROVENANCE_COLOR,
                    'target-arrow-shape': 'triangle',
                    'line-style': 'dotted',
                    'width': 2,
                    'opacity': 0.85,
                    'curve-style': 'bezier'
                }
            },
            {
                selector: 'edge[edgeType="pipeline_hook"]',
                style: {
                    'line-color': '#64d2ff',
                    'target-arrow-color': '#64d2ff',
                    'target-arrow-shape': 'triangle',
                    'line-style': 'dashed',
                    'width': 2,
                    'opacity': 0.8,
                    'curve-style': 'bezier'
                }
            },
            {
                selector: 'edge[edgeType="hook_evidence"], edge[edgeType="skill_evidence"]',
                style: {
                    'line-style': 'dotted',
                    'line-color': '#8b949e',
                    'target-arrow-shape': 'none',
                    'label': 'Placement not recorded',
                    'font-size': '9px',
                    'color': '#c2c9d1'
                }
            },
            {
                // Phase transition edge (dashed teal)
                selector: 'edge[edgeType="phase_transition"]',
                style: {
                    'line-color': '#64d2ff',
                    'target-arrow-color': '#64d2ff',
                    'target-arrow-shape': 'triangle',
                    'line-style': 'dashed',
                    'line-dash-pattern': [8, 4],
                    'width': 2,
                    'opacity': 0.7,
                    'curve-style': 'bezier'
                }
            }
        ],
        layout: {
            name: 'dagre',
            rankDir: 'TB',
            nodeSep: 80,
            rankSep: 100,
            padding: 50,
            fit: true,
            animate: false,
            stop: function() {
                // Re-fit after layout completes to handle container sizing race
                setTimeout(() => {
                    if (cyInstance === graph && !graph.destroyed()) graph.fit(50);
                }, 100);
            }
        },
        // Retain Cytoscape's effectively unrestricted minimum zoom so fitting
        // deep/wide executions is not clamped to an application-wide scale.
        maxZoom: 2
        // wheelSensitivity intentionally left at the Cytoscape default.
        // The library prints a console warning when this is customized
        // because the optimal value depends on the user's mouse and OS,
        // and a hard-coded value zooms unnaturally on most hardware.
    });

    cyInstance = graph;
    updateDAGLegend(graph);

    // Add click handler for nodes
    cyInstance.on('tap', 'node', function(evt) {
        const node = evt.target;
        const nodeData = node.data();
        const nodeType = nodeData.nodeType;

        // Handle user memory group nodes — show a brief hook summary
        if (nodeType === 'user_memory_group') {
            showUserMemoryGroupPopup(node, nodeData);
            return;
        }
        // Handle different node types in full flow mode
        if (nodeType === 'orchestrator' || nodeType === 'llm_call' || nodeType === 'skill_llm' || nodeType === 'llm_step' || nodeType === 'agent_llm' || nodeType === 'memory_llm' || nodeType === 'pipeline_hook' || nodeType === 'checkpoint' || nodeType === 'response' || nodeType === 'phase_boundary') {
            showFullFlowNodePopup(node, nodeData);
        } else {
            // Step node - use existing logic
            const stepId = node.id();
            const result = stepResults[stepId];
            const step = selected.plan?.steps?.find(s => s.step_id === stepId);
            showNodePopup(node, step, result);
        }
    });

    // Close popup when clicking on background (canvas)
    cyInstance.on('tap', function(evt) {
        if (evt.target === cyInstance) {
            // Clicked on background, not a node
            document.querySelectorAll('.node-popup').forEach(p => p.remove());
        }
    });

    // Cache this instance for fast re-display on tab switch
    const cacheKey = `${selected.request_id}:${viewMode}`;
    dagCache.set(cacheKey, { cy: cyInstance, container: dagContainer, requestId: selected.request_id });

    // LRU eviction — keep only the most recent DAG_CACHE_MAX entries
    if (dagCache.size > DAG_CACHE_MAX) {
        const oldestKey = dagCache.keys().next().value;
        const oldest = dagCache.get(oldestKey);
        oldest.cy.destroy();
        if (oldest.container.parentNode) oldest.container.parentNode.removeChild(oldest.container);
        dagCache.delete(oldestKey);
    }
}

// ---------------------------------------------------------------------------
// DAG Popups
// ---------------------------------------------------------------------------

// Popup for Full Flow node types (orchestrator, llm_call, checkpoint, response)
function showFullFlowNodePopup(node, nodeData) {
    document.querySelectorAll('.node-popup').forEach(p => p.remove());

    const nodeType = nodeData.nodeType;
    const popupRequestID = selected?.request_id;
    const popupSelectionVersion = selectionVersion;
    let title, content, color;

    switch (nodeType) {
        case 'orchestrator':
            title = '🤖 Orchestrator Agent';
            color = 'var(--accent-purple)';
            const agentName = selected.agent_name || 'orchestrator';
            content = `
                <div style="margin-top: 10px;">
                    <div style="font-size: 11px; color: var(--text-muted);">Agent Name</div>
                    <div style="font-weight: 500;">${escapeHtml(agentName)}</div>
                </div>
                <div style="margin-top: 10px;">
                    <div style="font-size: 11px; color: var(--text-muted);">Request ID</div>
                    <div style="font-family: 'SF Mono', monospace; font-size: 11px;">${escapeHtml(selected.request_id)}</div>
                </div>
                <div data-agent-parent-info>${renderAgentParentInfo()}</div>
            `;
            break;
        case 'skill_llm':
        case 'llm_call':
            const skillCall = nodeType === 'skill_llm';
            const callConfig = getLLMType(nodeData.llmType);
            title = skillCall ? `${callConfig.icon} ${callConfig.label}` : '💭 LLM Call';
            color = skillCall ? callConfig.accent : 'var(--accent-blue)';
            const llmType = nodeData.llmType || 'unknown';
            const model = nodeData.model || 'unknown';
            const duration = nodeData.duration || 0;
            const tokens = nodeData.tokens || 0;
            // Format type for display (capitalize and replace underscores)
            const formattedType = llmType.replace(/_/g, ' ').toUpperCase();
            content = `
                <div style="margin-top: 10px;">
                    <div style="font-size: 11px; color: var(--text-muted); margin-bottom: 6px;">Type</div>
                    <span style="
                        display: inline-block;
                        padding: 4px 12px;
                        background: linear-gradient(135deg, rgba(0, 210, 211, 0.2) 0%, rgba(0, 210, 211, 0.1) 100%);
                        border: 1px solid rgba(0, 210, 211, 0.4);
                        border-radius: 6px;
                        font-size: 11px;
                        font-weight: 600;
                        color: #00d2d3;
                        letter-spacing: 0.5px;
                    ">${escapeHtml(formattedType)}</span>
                </div>
                <div style="margin-top: 12px;">
                    <div style="font-size: 11px; color: var(--text-muted); margin-bottom: 6px;">Model</div>
                    <span style="
                        display: inline-block;
                        padding: 4px 12px;
                        background: linear-gradient(135deg, rgba(165, 94, 234, 0.2) 0%, rgba(165, 94, 234, 0.1) 100%);
                        border: 1px solid rgba(165, 94, 234, 0.4);
                        border-radius: 6px;
                        font-family: 'SF Mono', monospace;
                        font-size: 11px;
                        font-weight: 500;
                        color: #d4a5ff;
                    ">${escapeHtml(model)}</span>
                </div>
                ${nodeData.callLabel ? `
                <div style="margin-top: 12px;">
                    <div style="font-size: 11px; color: var(--text-muted); margin-bottom: 6px;">Call ID</div>
                    <span style="
                        display: inline-block;
                        padding: 3px 10px;
                        background: rgba(255, 255, 255, 0.04);
                        border: 1px solid rgba(255, 255, 255, 0.12);
                        border-radius: 6px;
                        font-family: 'SF Mono', monospace;
                        font-size: 11px;
                        color: var(--text-muted);
                    ">${escapeHtml(nodeData.callLabel)}</span>
                </div>` : ''}
                ${duration > 0 ? `
                <div style="margin-top: 12px;">
                    <div style="font-size: 11px; color: var(--text-muted);">Duration</div>
                    <div>${formatDuration(duration)}</div>
                </div>` : ''}
                ${tokens > 0 ? `
                <div style="margin-top: 12px;">
                    <div style="font-size: 11px; color: var(--text-muted);">Tokens</div>
                    <div>${tokens.toLocaleString()}</div>
                </div>` : ''}
                ${isPlanningType(llmType) ? renderPlannerClarifications(selected, nodeData.phaseNumber || 1) : ''}
                ${skillCall && nodeData.success === false ? `<div class="dag-data-warning"><strong>Failed</strong>${nodeData.error ? `<div>${escapeHtml(nodeData.error)}</div>` : ''}</div>` : ''}
                ${skillCall && nodeData.placementUnknown ? '<p class="pipeline-hook-placement-note">Placement could not be established from retained phase and timing evidence. The dotted link associates this call with the execution.</p>' : ''}
            `;
            break;
        case 'memory_llm': {
            // Pre-execution / memory hook LLM call (conversation history, activity compaction, event summarization)
            const memLlmType = nodeData.llmType || 'unknown';
            const memModel = nodeData.model || 'unknown';
            const memDuration = nodeData.duration || 0;
            const memTokens = nodeData.tokens || 0;
            const formattedMemType = memLlmType.replace(/_/g, ' ').toUpperCase();
            const accent = (memLlmType === 'conversation_history_prepare' || memLlmType === 'conversation_history_compaction')
                ? {
                    title: memLlmType === 'conversation_history_prepare' ? '🛡️ Conversation Preparation Step' : '🗜️ Conversation LLM Call',
                    color: '#6cc2ff',
                    bg: 'rgba(108, 194, 255, 0.06)',
                    border: 'rgba(108, 194, 255, 0.3)',
                }
                : { title: '📝 Memory LLM Call', color: '#f0a030', bg: 'rgba(240, 160, 48, 0.06)', border: 'rgba(240, 160, 48, 0.3)' };

            // Map LLM type to its pipeline hook context
            const memHookInfo = {
                'conversation_history_prepare': {
                    hook: 'ConversationHistoryPreparer',
                    phase: 'BeforePlanning',
                    description: 'Applies token-aware conversation history protection before planning. This step may pass history through unchanged, or it may drop, elide, or truncate older context to stay within the configured prompt budget.'
                },
                'conversation_history_compaction': {
                    hook: 'ConversationHistoryPreparer',
                    phase: 'BeforePlanning',
                    description: 'Compacts older conversation turns into a concise pre-planning summary so the current request keeps enough conversational continuity without overflowing the prompt budget.'
                },
                'activity_compaction_incremental': {
                    hook: 'MemoryEnrichmentHook',
                    phase: 'BeforePlanning',
                    description: 'Compacts recent domain event history into a fixed-size digest for the planning prompt. Runs as part of the memory enrichment pipeline hook before plan generation.'
                },
                'event_summarization': {
                    hook: 'MemoryRecordHook',
                    phase: 'AfterExecution',
                    description: 'Summarizes execution step outcomes into concise episodic memory events. Runs as part of the memory record pipeline hook after step execution completes.'
                }
            };
            const hookCtx = memHookInfo[memLlmType] || {
                hook: 'Unknown Hook',
                phase: 'Unknown Phase',
                description: 'Memory subsystem LLM call'
            };

            title = accent.title;
            color = accent.color;
            content = `
                <div style="margin-top: 10px;">
                    <div style="font-size: 11px; color: var(--text-muted); margin-bottom: 6px;">Pipeline Hook</div>
                    <div style="display: flex; gap: 6px; align-items: center; flex-wrap: wrap;">
                        <span style="
                            display: inline-block;
                            padding: 4px 10px;
                            background: linear-gradient(135deg, rgba(240, 160, 48, 0.15) 0%, rgba(240, 160, 48, 0.05) 100%);
                            border: 1px solid rgba(240, 160, 48, 0.3);
                            border-radius: 6px;
                            font-family: 'SF Mono', monospace;
                            font-size: 11px;
                            font-weight: 600;
                            color: #f0a030;
                        ">${escapeHtml(hookCtx.hook)}</span>
                        <span style="
                            display: inline-block;
                            padding: 4px 10px;
                            background: linear-gradient(135deg, rgba(100, 210, 255, 0.15) 0%, rgba(100, 210, 255, 0.05) 100%);
                            border: 1px solid rgba(100, 210, 255, 0.3);
                            border-radius: 6px;
                            font-family: 'SF Mono', monospace;
                            font-size: 10px;
                            font-weight: 500;
                            color: #64d2ff;
                        ">${escapeHtml(hookCtx.phase)}</span>
                    </div>
                </div>
                <div style="margin-top: 10px;">
                    <div style="
                        padding: 8px 12px;
                        background: ${accent.bg};
                        border-left: 2px solid ${accent.border};
                        border-radius: 0 4px 4px 0;
                        font-size: 11px;
                        color: var(--text-secondary);
                        line-height: 1.5;
                    ">${escapeHtml(hookCtx.description)}</div>
                </div>
                <div style="margin-top: 12px;">
                    <div style="font-size: 11px; color: var(--text-muted); margin-bottom: 6px;">LLM Call Type</div>
                    <span style="
                        display: inline-block;
                        padding: 4px 12px;
                        background: linear-gradient(135deg, rgba(240, 160, 48, 0.2) 0%, rgba(240, 160, 48, 0.1) 100%);
                        border: 1px solid rgba(240, 160, 48, 0.4);
                        border-radius: 6px;
                        font-size: 11px;
                        font-weight: 600;
                        color: #f0a030;
                        letter-spacing: 0.5px;
                    ">${escapeHtml(formattedMemType)}</span>
                </div>
                <div style="margin-top: 12px;">
                    <div style="font-size: 11px; color: var(--text-muted); margin-bottom: 6px;">Model</div>
                    <span style="
                        display: inline-block;
                        padding: 4px 12px;
                        background: linear-gradient(135deg, rgba(165, 94, 234, 0.2) 0%, rgba(165, 94, 234, 0.1) 100%);
                        border: 1px solid rgba(165, 94, 234, 0.4);
                        border-radius: 6px;
                        font-family: 'SF Mono', monospace;
                        font-size: 11px;
                        font-weight: 500;
                        color: #d4a5ff;
                    ">${escapeHtml(memModel)}</span>
                </div>
                ${nodeData.callLabel ? `
                <div style="margin-top: 12px;">
                    <div style="font-size: 11px; color: var(--text-muted); margin-bottom: 6px;">Call ID</div>
                    <span style="
                        display: inline-block;
                        padding: 3px 10px;
                        background: rgba(255, 255, 255, 0.04);
                        border: 1px solid rgba(255, 255, 255, 0.12);
                        border-radius: 6px;
                        font-family: 'SF Mono', monospace;
                        font-size: 11px;
                        color: var(--text-muted);
                    ">${escapeHtml(nodeData.callLabel)}</span>
                </div>` : ''}
                ${memDuration > 0 ? `
                <div style="margin-top: 12px;">
                    <div style="font-size: 11px; color: var(--text-muted);">Duration</div>
                    <div>${formatDuration(memDuration)}</div>
                </div>` : ''}
                ${memTokens > 0 ? `
                <div style="margin-top: 12px;">
                    <div style="font-size: 11px; color: var(--text-muted);">Tokens</div>
                    <div>${memTokens.toLocaleString()}</div>
                </div>` : ''}
            `;
            break;
        }
        case 'pipeline_hook': {
            const status = pipelineHookStatus(nodeData.hookStatus);
            title = `🪝 ${pipelineHookPhaseLabel(nodeData.hookPhase)}`;
            color = status.color;
            content = renderHookPopupSummary(nodeData);
            if (nodeData.placementUnknown) {
                content += '<p class="pipeline-hook-placement-note">Execution order was not recorded. The line links stored evidence to this request; it is not a dependency or execution-order arrow.</p>';
            }
            break;
        }
        case 'llm_step':
            // Step-specific LLM call attached to a parent step
            const stepLlmType = nodeData.llmType || 'unknown';
            const _stepLlmLabels = {
                'micro_resolution':      '🔍 Parameter Resolution',
                'semantic_retry':         '🔄 Semantic Retry',
                'error_analysis':         '⚠️ Error Analysis',
                'result_distillation':    '🔬 Result Distillation',
                'hallucination_detection': '🚫 Hallucination Detection'
            };
            const _stepLlmColors = {
                'micro_resolution':      'var(--accent-teal)',
                'semantic_retry':         'var(--accent-pink)',
                'error_analysis':         'var(--accent-red)',
                'result_distillation':    '#8250dc',
                'hallucination_detection': '#ff5050'
            };
            const stepLlmTypeLabel = _stepLlmLabels[stepLlmType] || stepLlmType;
            title = stepLlmTypeLabel;
            color = _stepLlmColors[stepLlmType] || 'var(--accent-teal)';
            const stepLlmModel = nodeData.model || 'unknown';
            const stepLlmDuration = nodeData.duration || 0;
            const stepLlmTokens = nodeData.tokens || 0;
            const parentStepId = nodeData.parentStepId || '';
            const stepLlmSuccess = nodeData.success !== false;
            const stepLlmError = nodeData.error || '';

            // Find parent step info
            const parentStep = selected.plan?.steps?.find(s => s.step_id === parentStepId);
            const parentStepLabel = parentStep ? (parentStep.agent_name || parentStep.metadata?.capability || parentStepId) : parentStepId;

            content = `
                <div style="margin-top: 10px;">
                    <div style="font-size: 11px; color: var(--text-muted); margin-bottom: 6px;">Parent Step</div>
                    <span style="
                        display: inline-block;
                        padding: 4px 12px;
                        background: linear-gradient(135deg, rgba(50, 215, 75, 0.2) 0%, rgba(50, 215, 75, 0.1) 100%);
                        border: 1px solid rgba(50, 215, 75, 0.4);
                        border-radius: 6px;
                        font-size: 11px;
                        font-weight: 500;
                        color: #32d74b;
                    ">${escapeHtml(parentStepLabel)}</span>
                </div>
                <div style="margin-top: 12px;">
                    <div style="font-size: 11px; color: var(--text-muted); margin-bottom: 6px;">Model</div>
                    <span style="
                        display: inline-block;
                        padding: 4px 12px;
                        background: linear-gradient(135deg, rgba(165, 94, 234, 0.2) 0%, rgba(165, 94, 234, 0.1) 100%);
                        border: 1px solid rgba(165, 94, 234, 0.4);
                        border-radius: 6px;
                        font-family: 'SF Mono', monospace;
                        font-size: 11px;
                        font-weight: 500;
                        color: #d4a5ff;
                    ">${escapeHtml(stepLlmModel)}</span>
                </div>
                ${nodeData.callLabel ? `
                <div style="margin-top: 12px;">
                    <div style="font-size: 11px; color: var(--text-muted); margin-bottom: 6px;">Call ID</div>
                    <span style="
                        display: inline-block;
                        padding: 3px 10px;
                        background: rgba(255, 255, 255, 0.04);
                        border: 1px solid rgba(255, 255, 255, 0.12);
                        border-radius: 6px;
                        font-family: 'SF Mono', monospace;
                        font-size: 11px;
                        color: var(--text-muted);
                    ">${escapeHtml(nodeData.callLabel)}</span>
                </div>` : ''}
                ${!stepLlmSuccess ? `
                <div style="margin-top: 12px;">
                    <div style="font-size: 11px; color: var(--accent-red); font-weight: 600;">⚠️ Failed</div>
                    ${stepLlmError ? `<div style="font-size: 11px; color: var(--text-muted); margin-top: 4px;">${escapeHtml(stepLlmError)}</div>` : ''}
                </div>` : ''}
                ${stepLlmDuration > 0 ? `
                <div style="margin-top: 12px;">
                    <div style="font-size: 11px; color: var(--text-muted);">Duration</div>
                    <div>${formatDuration(stepLlmDuration)}</div>
                </div>` : ''}
                ${stepLlmTokens > 0 ? `
                <div style="margin-top: 12px;">
                    <div style="font-size: 11px; color: var(--text-muted);">Tokens</div>
                    <div>${stepLlmTokens.toLocaleString()}</div>
                </div>` : ''}
            `;
            break;
        case 'agent_llm':
            title = `🔧 Agent LLM Call`;
            color = '#a064f0';
            const agentLlmComponent = nodeData.sourceComponent || 'unknown';
            const agentLlmModel = nodeData.model || 'unknown';
            const agentLlmDuration = nodeData.duration || 0;
            const agentLlmTokens = nodeData.tokens || 0;
            const formattedAgentType = agentLlmComponent.toUpperCase();
            content = `
                <div style="margin-top: 10px;">
                    <div style="font-size: 11px; color: var(--text-muted); margin-bottom: 6px;">Type</div>
                    <span style="
                        display: inline-block;
                        padding: 4px 12px;
                        background: linear-gradient(135deg, rgba(160, 100, 240, 0.2) 0%, rgba(160, 100, 240, 0.1) 100%);
                        border: 1px solid rgba(160, 100, 240, 0.4);
                        border-radius: 6px;
                        font-size: 11px;
                        font-weight: 600;
                        color: #a064f0;
                        letter-spacing: 0.5px;
                    ">AGENT LLM CALL</span>
                </div>
                <div style="margin-top: 12px;">
                    <div style="font-size: 11px; color: var(--text-muted); margin-bottom: 6px;">Source Component</div>
                    <span style="
                        display: inline-block;
                        padding: 4px 12px;
                        background: linear-gradient(135deg, rgba(0, 210, 211, 0.2) 0%, rgba(0, 210, 211, 0.1) 100%);
                        border: 1px solid rgba(0, 210, 211, 0.4);
                        border-radius: 6px;
                        font-size: 11px;
                        font-weight: 600;
                        color: #00d2d3;
                        letter-spacing: 0.5px;
                    ">${escapeHtml(formattedAgentType)}</span>
                </div>
                <div style="margin-top: 12px;">
                    <div style="font-size: 11px; color: var(--text-muted); margin-bottom: 6px;">Model</div>
                    <span style="
                        display: inline-block;
                        padding: 4px 12px;
                        background: linear-gradient(135deg, rgba(165, 94, 234, 0.2) 0%, rgba(165, 94, 234, 0.1) 100%);
                        border: 1px solid rgba(165, 94, 234, 0.4);
                        border-radius: 6px;
                        font-family: 'SF Mono', monospace;
                        font-size: 11px;
                        font-weight: 500;
                        color: #d4a5ff;
                    ">${escapeHtml(agentLlmModel)}</span>
                </div>
                ${nodeData.callLabel ? `
                <div style="margin-top: 12px;">
                    <div style="font-size: 11px; color: var(--text-muted); margin-bottom: 6px;">Call ID</div>
                    <span style="
                        display: inline-block;
                        padding: 3px 10px;
                        background: rgba(255, 255, 255, 0.04);
                        border: 1px solid rgba(255, 255, 255, 0.12);
                        border-radius: 6px;
                        font-family: 'SF Mono', monospace;
                        font-size: 11px;
                        color: var(--text-muted);
                    ">${escapeHtml(nodeData.callLabel)}</span>
                </div>` : ''}
                ${agentLlmDuration > 0 ? `
                <div style="margin-top: 12px;">
                    <div style="font-size: 11px; color: var(--text-muted);">Duration</div>
                    <div>${formatDuration(agentLlmDuration)}</div>
                </div>` : ''}
                ${agentLlmTokens > 0 ? `
                <div style="margin-top: 12px;">
                    <div style="font-size: 11px; color: var(--text-muted);">Tokens</div>
                    <div>${agentLlmTokens.toLocaleString()}</div>
                </div>` : ''}
                ${nodeData.error ? `
                <div style="margin-top: 12px;">
                    <div style="font-size: 11px; color: var(--accent-red); font-weight: 600;">⚠️ Error</div>
                    <div style="font-size: 11px; color: var(--text-muted); margin-top: 4px;">${escapeHtml(nodeData.error)}</div>
                </div>` : ''}
            `;
            break;
        case 'checkpoint':
            const cpType = nodeData.checkpointType || 'unknown';
            const cpStatus = nodeData.status || 'unknown';
            const cpLabel = nodeData.checkpointLabel || cpType.replace(/_/g, ' ');
            const cpParentStep = nodeData.parentStepId || '';
            const cpMessage = nodeData.message || '';
            const cpReason = nodeData.reason || '';

            // Set title and color based on checkpoint type
            const isStepLevel = ['before_step', 'after_step', 'on_error'].includes(cpType);
            if (cpType === 'before_step') {
                title = '⏸️ Before Step';
                color = 'var(--accent-purple)';
            } else if (cpType === 'after_step') {
                title = '✓ After Step';
                color = 'var(--accent-green)';
            } else if (cpType === 'on_error') {
                title = '⚠️ On Error';
                color = 'var(--accent-red)';
            } else {
                title = '⏸️ HITL Checkpoint';
                color = 'var(--accent-orange)';
            }

            const statusColor = cpStatus === 'approved' ? 'var(--accent-green)' :
                               cpStatus === 'rejected' ? 'var(--accent-red)' : 'var(--accent-orange)';
            content = `
                <div style="margin-top: 10px;">
                    <div style="font-size: 11px; color: var(--text-muted);">Type</div>
                    <div style="font-weight: 500;">${escapeHtml(cpLabel)}</div>
                </div>
                <div style="margin-top: 10px;">
                    <div style="font-size: 11px; color: var(--text-muted);">Status</div>
                    <div style="color: ${statusColor}; font-weight: 600;">${escapeHtml(cpStatus.toUpperCase())}</div>
                </div>
                ${cpParentStep ? `
                <div style="margin-top: 10px;">
                    <div style="font-size: 11px; color: var(--text-muted);">Parent Step</div>
                    <div class="mono" style="font-size: 12px; color: var(--accent-blue);">${escapeHtml(cpParentStep)}</div>
                </div>` : ''}
                ${cpReason ? `
                <div style="margin-top: 10px;">
                    <div style="font-size: 11px; color: var(--text-muted);">Reason</div>
                    <div style="font-size: 12px;">${escapeHtml(cpReason)}</div>
                </div>` : ''}
                ${cpMessage ? `
                <div style="margin-top: 10px;">
                    <div style="font-size: 11px; color: var(--text-muted);">Message</div>
                    <div style="font-size: 12px; max-width: 200px; word-wrap: break-word;">${escapeHtml(cpMessage)}</div>
                </div>` : ''}
            `;
            break;
        case 'response':
            title = '📤 Response';
            color = 'var(--accent-teal)';
            const responseStatus = executionStatus(selected);
            const resultColor = responseStatus.state === 'completed' ? 'var(--accent-green)' :
                ['failed', 'cancelled'].includes(responseStatus.state) ? 'var(--accent-red)' : 'var(--text-muted)';
            const responseTotalDuration = primaryDurationMs(selected);
            content = `
                <div style="margin-top: 10px;">
                    <div style="font-size: 11px; color: var(--text-muted);">Status</div>
                    <div style="color: ${resultColor}; font-weight: 600;">${responseStatus.label}</div>
                </div>
                ${executionFailureNotice(selected) ? `<div style="margin-top: 10px; font-size: 12px;">${escapeHtml(executionFailureNotice(selected))}</div>` : ''}
                ${renderRecordedClarification(selected)}
                <div style="margin-top: 10px;">
                    <div style="font-size: 11px; color: var(--text-muted);">Elapsed Duration</div>
                    <div>${formatDuration(responseTotalDuration)}</div>
                </div>
            `;
            break;
        case 'step':
            // Handle step nodes in Full Flow mode
            const stepStatus = nodeData.status || 'pending';
            const stepStatusColor = stepStatus === 'completed' ? 'var(--accent-green)' :
                                   stepStatus === 'failed' ? 'var(--accent-red)' :
                                   stepStatus === 'skipped' ? 'var(--text-muted)' :
                                   stepStatus === 'blocked' ? '#ff3b30' : 'var(--accent-orange)';
            title = `🔧 ${escapeHtml(nodeData.label || 'Step')}`;
            color = stepStatusColor;
            const stepLevel = nodeData.level || 1;
            const stepParallel = nodeData.parallelCount || 1;
            const stepDeps = nodeData.dependsOn || [];
            const stepImplicitDeps = nodeData.implicitDeps || [];
            content = `
                <div style="margin-top: 10px;">
                    <div style="font-size: 11px; color: var(--text-muted);">Status</div>
                    <div style="color: ${stepStatusColor}; font-weight: 600;">${stepStatus.toUpperCase()}</div>
                </div>
                ${nodeData.capability ? `
                <div style="margin-top: 10px;">
                    <div style="font-size: 11px; color: var(--text-muted);">Capability</div>
                    <div style="font-family: 'SF Mono', monospace; font-size: 11px;">${escapeHtml(nodeData.capability)}</div>
                </div>` : ''}
                <div style="margin-top: 10px;">
                    <div style="font-size: 11px; color: var(--text-muted);">Level</div>
                    <div>Level ${stepLevel} ${stepParallel > 1 ? `<span style="color: var(--accent-purple);">(${stepParallel} parallel)</span>` : '(sequential)'}</div>
                </div>
                ${nodeData.duration > 0 ? `
                <div style="margin-top: 10px;">
                    <div style="font-size: 11px; color: var(--text-muted);">Duration</div>
                    <div>${formatDuration(nodeData.duration)}</div>
                </div>` : ''}
                ${stepDeps.length > 0 ? `
                <div style="margin-top: 10px;">
                    <div style="font-size: 11px; color: var(--text-muted);">Depends On</div>
                    <div style="font-family: 'SF Mono', monospace; font-size: 10px;">${stepDeps.map(escapeHtml).join(', ')}</div>
                </div>` : ''}
                ${stepImplicitDeps.length > 0 ? `
                <div style="margin-top: 10px;">
                    <div style="font-size: 11px; color: var(--text-muted);">Prior-phase Data</div>
                    <div style="font-family: 'SF Mono', monospace; font-size: 10px; color: ${DAG_PROVENANCE_COLOR};">${stepImplicitDeps.map(escapeHtml).join(', ')}</div>
                    <div style="font-size: 10px; color: var(--text-muted); margin-top: 3px;">Provenance only; this does not schedule the step.</div>
                </div>` : ''}
                ${nodeData.instruction ? `
                <div style="margin-top: 10px;">
                    <div style="font-size: 11px; color: var(--text-muted);">Instruction</div>
                    <div style="font-size: 11px; max-height: 60px; overflow-y: auto;">${escapeHtml(nodeData.instruction).substring(0, 150)}${nodeData.instruction.length > 150 ? '...' : ''}</div>
                </div>` : ''}
            `;
            break;
        case 'phase_boundary':
            title = `🔄 Phase ${nodeData.phaseFrom || '?'} → ${nodeData.phaseTo || '?'}`;
            color = '#64d2ff';
            const phaseContinuationNote = nodeData.continuationNote || '';
            content = `
                <div style="margin-top: 10px;">
                    <div style="font-size: 11px; color: var(--text-muted);">Phase Transition</div>
                    <div style="font-weight: 500; color: #64d2ff;">Phase ${nodeData.phaseFrom || '?'} completed → Phase ${nodeData.phaseTo || '?'} begins</div>
                </div>
                ${phaseContinuationNote ? `
                <div style="margin-top: 10px;">
                    <div style="font-size: 11px; color: var(--text-muted);">Continuation Note</div>
                    <div style="font-size: 12px; line-height: 1.4; max-height: 80px; overflow-y: auto;">${escapeHtml(phaseContinuationNote)}</div>
                </div>` : ''}
                <div style="margin-top: 10px;">
                    <div style="font-size: 11px; color: var(--text-muted);">Type</div>
                    <span style="
                        display: inline-block;
                        padding: 4px 12px;
                        background: linear-gradient(135deg, rgba(100, 210, 255, 0.2) 0%, rgba(100, 210, 255, 0.1) 100%);
                        border: 1px solid rgba(100, 210, 255, 0.4);
                        border-radius: 6px;
                        font-size: 11px;
                        font-weight: 600;
                        color: #64d2ff;
                        letter-spacing: 0.5px;
                    ">ITERATIVE PLANNING</span>
                </div>
            `;
            break;
        default:
            return;
    }

    const popup = document.createElement('div');
    popup.className = nodeType === 'pipeline_hook' ? 'node-popup dag-hook-popup' : 'node-popup';
    popup.style.cssText = `
        position: absolute;
        background: linear-gradient(135deg, rgba(25, 25, 35, 0.98) 0%, rgba(15, 15, 22, 0.98) 100%);
        backdrop-filter: blur(24px) saturate(180%);
        -webkit-backdrop-filter: blur(24px) saturate(180%);
        border: 1px solid rgba(255, 255, 255, 0.15);
        border-radius: 16px;
        padding: 18px 20px;
        ${nodeType === 'pipeline_hook' ? 'width: 340px; max-width: calc(100vw - 16px); min-width: 0;' : 'max-width: 380px; min-width: 280px;'}
        z-index: 1000;
        font-size: 13px;
        box-shadow: 0 20px 60px rgba(0, 0, 0, 0.5),
                    0 8px 24px rgba(0, 0, 0, 0.3),
                    inset 0 1px 0 rgba(255, 255, 255, 0.1),
                    inset 0 -1px 0 rgba(0, 0, 0, 0.2);
    `;

    popup.innerHTML = `
        ${nodeType === 'pipeline_hook' ? '' : `<div style="display: flex; align-items: center; gap: 10px; margin-bottom: 8px;">
            <div style="font-weight: 600; color: ${color};">${title}</div>
        </div>`}
        ${content}
        ${nodeData.llmCallIdentity ? `<button type="button" class="dag-popup-detail-link" data-llm-call-details>View details in ${nodeData.llmDetailTab === 'dag-pre' ? 'Pre-Execution' : nodeData.llmDetailTab === 'dag-post' ? 'Post-Execution' : 'LLM Calls'} →</button>` : ''}
        ${nodeData.hookIdentity ? `<button type="button" class="dag-popup-detail-link" data-hook-details>View details in ${nodeData.hookDetailTab === 'dag-pre' ? 'Pre-Execution' : nodeData.hookDetailTab === 'dag-post' ? 'Post-Execution' : 'Raw JSON'} →</button>` : ''}
    `;

    popup.addEventListener('click', handleLineageAction);
    popup.querySelector('[data-llm-call-details]')?.addEventListener('click', () => {
        void openLLMCallDetails({ identity: nodeData.llmCallIdentity, tab: nodeData.llmDetailTab }, popupRequestID, popupSelectionVersion);
    });
    popup.querySelector('[data-hook-details]')?.addEventListener('click', () => {
        void openHookDetails({ identity: nodeData.hookIdentity, tab: nodeData.hookDetailTab }, popupRequestID, popupSelectionVersion);
    });

    // Position popup near node — appended to document.body so it's not
    // clipped by the DAG container's overflow:hidden.
    const dagContainerEl = document.getElementById('dagContainer');
    const pos = node.renderedPosition();
    const containerRect = dagContainerEl.getBoundingClientRect();

    // Convert node's rendered position (relative to container) to viewport coords
    let popupLeft = containerRect.left + pos.x + 80;
    let popupTop = containerRect.top + pos.y - 80;

    // Clamp to viewport so popup is fully visible
    document.body.appendChild(popup);
    const popupRect = popup.getBoundingClientRect();
    popupLeft = Math.max(8, Math.min(popupLeft, window.innerWidth - popupRect.width - 8));
    popupTop = Math.max(8, Math.min(popupTop, window.innerHeight - popupRect.height - 8));

    popup.style.position = 'fixed';
    popup.style.left = popupLeft + 'px';
    popup.style.top = popupTop + 'px';
    // Keep summary popups within the viewport on short screens.
    popup.style.boxSizing = 'border-box';
    popup.style.maxHeight = `${window.innerHeight - popupTop - 8}px`;
    popup.style.overflowY = 'auto';
}

function showNodePopup(node, step, result) {
    // Remove existing popups
    document.querySelectorAll('.node-popup').forEach(p => p.remove());

    const status = result ? (result.skipped ? 'skipped' : (result.success ? 'completed' : 'failed')) : 'pending';
    const statusColor = status === 'completed' ? 'var(--accent-green)' :
                       status === 'failed' ? 'var(--accent-red)' :
                       status === 'skipped' ? 'var(--text-muted)' : 'var(--accent-orange)';

    // Get parallelism info from node data
    const nodeData = node.data();
    const level = nodeData.level || 1;
    const parallelCount = nodeData.parallelCount || 1;
    const isParallel = nodeData.isParallel || false;
    const dependsOn = nodeData.dependsOn || step.depends_on || [];
    const implicitDeps = nodeData.implicitDeps || step.implicit_deps || [];
    const stepIndex = (selected.plan?.steps || []).findIndex(candidate =>
        candidate.step_id === step.step_id
    );
    const displayStepLabel = executionDisplayStepLabel(stepIndex, selected);
    // Get capability from metadata (where orchestration stores it)
    const capability = step.metadata?.capability || result?.metadata?.capability || step.capability || step.agent_name || '';

    const popup = document.createElement('div');
    popup.className = 'node-popup';
    popup.style.cssText = `
        position: absolute;
        background: linear-gradient(135deg, rgba(25, 25, 35, 0.98) 0%, rgba(15, 15, 22, 0.98) 100%);
        backdrop-filter: blur(24px) saturate(180%);
        -webkit-backdrop-filter: blur(24px) saturate(180%);
        border: 1px solid rgba(255, 255, 255, 0.15);
        border-radius: 16px;
        padding: 18px 20px;
        max-width: 380px;
        min-width: 280px;
        z-index: 1000;
        font-size: 13px;
        box-shadow: 0 20px 60px rgba(0, 0, 0, 0.5),
                    0 8px 24px rgba(0, 0, 0, 0.3),
                    inset 0 1px 0 rgba(255, 255, 255, 0.1),
                    inset 0 -1px 0 rgba(0, 0, 0, 0.2);
    `;

    popup.innerHTML = `
        <div style="display: flex; justify-content: space-between; align-items: center; margin-bottom: 8px;">
            <span style="font-weight: 600; font-size: 13px; color: var(--accent-teal);">${step.agent_name || capability}</span>
            <span style="padding: 4px 10px; border-radius: 12px; font-size: 10px; font-weight: 600; background: ${statusColor}20; color: ${statusColor};">
                ${status.toUpperCase()}
            </span>
        </div>
        ${capability && capability !== step.agent_name ? `
            <div style="font-size: 11px; color: var(--text-muted); margin-bottom: 8px;">
                <span style="color: var(--accent-purple);">⚡</span> ${capability}
            </div>
        ` : ''}
        ${step.metadata?.viewer_payloads_deferred || result?.metadata?.viewer_payloads_deferred ? '<div class="dag-data-warning">Overview preview. Open Step Details for the exact instruction, request, response and error.</div>' : ''}
        <div style="color: var(--text-secondary); margin-bottom: 12px; line-height: 1.5;">${truncateInstruction(step.instruction)}</div>

        <!-- Execution Flow Info -->
        <div style="display: flex; gap: 10px; flex-wrap: wrap; margin-bottom: 12px;">
            ${displayStepLabel ? `
            <span title="${escapeHtmlAttribute(executionStepNumberingNote(selected))}" style="padding: 4px 10px; border-radius: 8px; font-size: 10px; font-weight: 700; background: rgba(50, 215, 75, 0.15); color: var(--accent-green);">
                ${displayStepLabel}
            </span>` : ''}
            <span style="padding: 4px 10px; border-radius: 8px; font-size: 10px; background: rgba(100, 210, 255, 0.15); color: var(--accent-teal);">
                Level ${level}
            </span>
            ${isParallel ? `
                <span style="padding: 4px 10px; border-radius: 8px; font-size: 10px; background: rgba(218, 143, 255, 0.15); color: var(--accent-purple);">
                    ⇄ ${parallelCount} parallel
                </span>
            ` : `
                <span style="padding: 4px 10px; border-radius: 8px; font-size: 10px; background: rgba(255, 179, 64, 0.15); color: var(--accent-orange);">
                    ↓ Sequential
                </span>
            `}
            ${dependsOn.length > 0 ? `
                <span style="padding: 4px 10px; border-radius: 8px; font-size: 10px; background: rgba(255, 255, 255, 0.08); color: var(--text-muted);">
                    ← ${dependsOn.length} dep${dependsOn.length > 1 ? 's' : ''}
                </span>
            ` : `
                <span style="padding: 4px 10px; border-radius: 8px; font-size: 10px; background: rgba(50, 215, 75, 0.15); color: var(--accent-green);">
                    ○ Root step
                </span>
            `}
            ${implicitDeps.length > 0 ? `
                <span style="padding: 4px 10px; border-radius: 8px; font-size: 10px; background: ${DAG_PROVENANCE_COLOR}1f; color: ${DAG_PROVENANCE_COLOR};">
                    ⇠ ${implicitDeps.length} prior-phase data
                </span>
            ` : ''}
        </div>

        ${dependsOn.length > 0 ? `
            <div style="margin-bottom: 12px; padding: 8px 10px; background: rgba(0,0,0,0.2); border-radius: 8px; font-size: 11px;">
                <span style="color: var(--text-muted);">Depends on:</span>
                <span style="color: var(--text-secondary); margin-left: 6px;">${dependsOn.join(', ')}</span>
            </div>
        ` : ''}

        ${implicitDeps.length > 0 ? `
            <div style="margin-bottom: 12px; padding: 8px 10px; background: ${DAG_PROVENANCE_COLOR}14; border-left: 2px dotted ${DAG_PROVENANCE_COLOR}; border-radius: 8px; font-size: 11px;">
                <span style="color: ${DAG_PROVENANCE_COLOR}; font-weight: 600;">Uses results from earlier steps:</span>
                <span style="color: var(--text-secondary); margin-left: 6px;">${implicitDeps.join(', ')}</span>
                <div style="color: var(--text-muted); margin-top: 4px;">Data lineage only; this does not control execution order.</div>
            </div>
        ` : ''}

        ${result ? `
            <div style="display: flex; gap: 14px; padding: 10px 12px; background: rgba(0,0,0,0.25); border-radius: 8px; color: var(--text-muted); font-size: 11px;">
                <span style="display: flex; align-items: center; gap: 4px;">
                    <span style="color: ${result.skipped ? 'var(--text-muted)' : (result.success ? 'var(--accent-green)' : 'var(--accent-red)')};">${result.skipped ? '⊘' : (result.success ? '✓' : '✗')}</span>
                    ${result.skipped ? 'Skipped' : (result.success ? 'Success' : 'Failed')}
                </span>
                <span>${formatDuration(result.duration_ms || (result.duration ? Math.round(result.duration / 1000000) : 0))}</span>
                <span>${result.attempts || 1} attempt${(result.attempts || 1) > 1 ? 's' : ''}</span>
                ${Number.isInteger(result.dispatch_count) ? `<span>${result.dispatch_count} HTTP dispatch${result.dispatch_count === 1 ? '' : 'es'}</span>` : ''}
            </div>
            ${result.error && !result.success ? `
                <div style="margin-top: 10px; padding: 10px 12px; background: rgba(255, 107, 107, 0.1); border-radius: 8px; border: 1px solid rgba(255, 107, 107, 0.2); color: var(--accent-red); font-size: 11px;">
                    ${escapeHtml(result.error)}
                </div>
            ` : ''}
        ` : '<div style="color: var(--text-muted); font-style: italic;">Not executed yet</div>'}
        <div data-lineage-request="${escapeHtmlAttribute(selected.request_id)}" data-lineage-step="${escapeHtmlAttribute(step.step_id)}">${renderDelegatedExecutionLinks(step.step_id)}</div>
        <button type="button" class="dag-popup-detail-link" data-step-details>View details in Step Details →</button>
    `;
    popup.addEventListener('click', event => {
        const target = event.target.closest('[data-lineage-execution]');
        if (target) {
            event.stopPropagation();
            navigateExecutionLineage(target);
        }
    });
    popup.querySelector('[data-step-details]').addEventListener('click', async () => {
        await setDagDetailTab('dag-steps');
        const card = document.getElementById(`dag-step-details-${step.step_id}`);
        if (card) {
            card.focus({ preventScroll: true });
            card.scrollIntoView({ block: 'start' });
        }
    });
    popup.style.boxSizing = 'border-box';
    popup.style.maxHeight = 'calc(100vh - 16px)';
    popup.style.overflowY = 'auto';

    // Position popup near node — appended to document.body so it's not
    // clipped by the DAG container's overflow:hidden.
    const containerEl = document.getElementById('dagContainer');
    const pos = node.renderedPosition();
    const containerRect = containerEl.getBoundingClientRect();

    let popupLeft = containerRect.left + pos.x + 80;
    let popupTop = containerRect.top + pos.y - 60;

    document.body.appendChild(popup);
    const popupRect = popup.getBoundingClientRect();
    popupLeft = Math.max(8, Math.min(popupLeft, window.innerWidth - popupRect.width - 8));
    popupTop = Math.max(8, Math.min(popupTop, window.innerHeight - popupRect.height - 8));

    popup.style.position = 'fixed';
    popup.style.left = popupLeft + 'px';
    popup.style.top = popupTop + 'px';

    // Close on click outside
    const closeHandler = (e) => {
        if (!popup.contains(e.target)) {
            popup.remove();
            document.removeEventListener('click', closeHandler);
        }
    };
    setTimeout(() => document.addEventListener('click', closeHandler), 100);
}

// ---------------------------------------------------------------------------
// Step Details tab helpers
// ---------------------------------------------------------------------------

function toggleInstruction(stepId) {
    const container = document.getElementById(`instruction-${stepId}`);
    if (!container) return;
    const text = container.querySelector('.instruction-text');
    const btn = container.querySelector('.instruction-toggle');
    if (text.classList.contains('collapsed')) {
        text.classList.remove('collapsed');
        btn.textContent = 'Show less';
    } else {
        text.classList.add('collapsed');
        btn.textContent = 'Show more';
    }
}

function toggleStepSection(elementId) {
    const content = document.getElementById(elementId);
    if (content) {
        const isHidden = content.style.display === 'none';
        content.style.display = isHidden ? 'block' : 'none';
        const arrow = document.getElementById(`${elementId}-arrow`) || content?.previousElementSibling?.querySelector('.expand-arrow');
        if (arrow) arrow.textContent = isHidden ? '▼' : '▶';
    }
}

// isLossyTrim: did this trim LOSE content? content_lost carries the authoritative tri-state:
// true = lossy (even at equal/growing byte counts — the disclosure annotation rides on top of
// the body); EXPLICIT false = verified lossless (never labeled lossy, whatever the byte
// delta); absent (legacy records predating the field) = fall back to size inequality.
function isLossyTrim(trim) {
    if (!trim || !(trim.original_bytes > 0)) return false;
    if (trim.content_lost === true) return true;
    return trim.content_lost === undefined && trim.original_bytes !== trim.trimmed_bytes;
}

// hasTrimDisplay: should the trim panel/summary be surfaced at all? Lossy trims, plus
// verified-lossless re-serialization shrinks — the byte accounting is worth showing, with the
// green "no content lost" chip as the disambiguator.
function hasTrimDisplay(trim) {
    return !!(trim && trim.original_bytes > 0 && (isLossyTrim(trim) || trim.original_bytes !== trim.trimmed_bytes));
}

// trimNodeLabel renders the node's scissors marker whenever compaction changed the result.
// 'lossy' disambiguates the unusual case where known content loss still produced an
// equal-sized or larger output because disclosure annotations added bytes.
function trimNodeLabel(trim) {
    const marker = trim.content_lost === true && trim.trimmed_bytes >= trim.original_bytes ? ' lossy' : '';
    return `\n✂ ${formatBytes(trim.original_bytes)}→${formatBytes(trim.trimmed_bytes)}${marker}`;
}

// trimCoverageText renders the approximate share of the source an LLM actually saw, or ''
// when full/absent. The basis varies by method: byte-based for single-call distill;
// SEGMENT-based for distill_mapreduce — except on a wrapper drop, where the Go side records
// the byte×segment composition (so the ratio here can legitimately disagree with the N/M
// segments line). Display is capped at 99% so a lossy near-full trim never renders "~100%".
function trimCoverageText(trim) {
    const r = trim.source_coverage_ratio;
    if (!(r > 0 && r < 1)) return '';
    return `~${Math.min(99, Math.round(r * 100))}% of source seen`;
}

function renderTrimSummary(trim) {
    if (!trim || !trim.original_bytes) return '';
    // Sign-aware: a lossy trim can GROW past the original (disclosure annotation overhead);
    // "0% reduced" next to growing byte figures would be affirmatively false.
    const delta = 1 - trim.trimmed_bytes / trim.original_bytes;
    const sizeText = delta >= 0 ? `(${(delta * 100).toFixed(0)}% reduced)` : `(grew ${(-delta * 100).toFixed(0)}%)`;
    const parts = [`${formatBytes(trim.original_bytes)} → ${formatBytes(trim.trimmed_bytes)} ${sizeText}`];
    if (trim.method) parts.push(trim.method);
    const cov = trimCoverageText(trim);
    if (cov) parts.push(cov);
    // A sub-100% coverage figure already implies loss — the generic token only adds
    // information when no coverage text rendered (same dedup policy as the flag chips).
    if (trim.content_lost && !cov) parts.push('content lost');
    return parts.join(' · ');
}

function renderTrimDetails(trim) {
    if (!trim || !trim.original_bytes) return '';
    const ratio = Math.min(1, trim.trimmed_bytes / trim.original_bytes); // bar never exceeds 100%
    const methodColors = {
        'structural':      { bg: 'rgba(100,210,255,0.15)', border: 'rgba(100,210,255,0.4)', text: '#64d2ff' },
        'structural_array':{ bg: 'rgba(100,210,255,0.15)', border: 'rgba(100,210,255,0.4)', text: '#64d2ff' },
        'structural_text': { bg: 'rgba(100,210,255,0.15)', border: 'rgba(100,210,255,0.4)', text: '#64d2ff' },
        'truncate':        { bg: 'rgba(255,179,64,0.15)',  border: 'rgba(255,179,64,0.4)',  text: '#ffb340' },
        'distill':         { bg: 'rgba(130,90,220,0.15)',  border: 'rgba(130,90,220,0.4)',  text: '#825adc' },
        'distill_mapreduce':{ bg: 'rgba(130,90,220,0.15)', border: 'rgba(130,90,220,0.4)',  text: '#825adc' }
    };
    const mc = methodColors[trim.method] || { bg: 'rgba(255,255,255,0.05)', border: 'rgba(255,255,255,0.1)', text: 'var(--text-secondary)' };

    let html = '<div style="padding: 8px 0;">';

    // Size reduction bar
    html += `<div style="margin-bottom: 12px;">
        <div style="display: flex; justify-content: space-between; font-size: 11px; color: var(--text-muted); margin-bottom: 4px;">
            <span>Original: ${formatBytes(trim.original_bytes)}</span>
            <span>Trimmed: ${formatBytes(trim.trimmed_bytes)}</span>
        </div>
        <div style="height: 6px; background: rgba(255,255,255,0.05); border-radius: 3px; overflow: hidden;">
            <div style="height: 100%; width: ${(ratio * 100).toFixed(1)}%; background: linear-gradient(90deg, rgba(50,215,75,0.6), rgba(255,179,64,0.6)); border-radius: 3px;"></div>
        </div>
    </div>`;

    // Method badge + optional budget badge
    html += `<div style="margin-bottom: 8px;">
        <span style="background: ${mc.bg}; border: 1px solid ${mc.border}; color: ${mc.text}; padding: 2px 8px; border-radius: 4px; font-size: 11px;">Method: ${escapeHtml(trim.method)}</span>
        ${trim.budget_allocated ? `<span style="background: rgba(255,255,255,0.05); border: 1px solid rgba(255,255,255,0.1); color: var(--text-muted); padding: 2px 8px; border-radius: 4px; font-size: 11px; margin-left: 4px;">Budget: ${formatBytes(trim.budget_allocated)}</span>` : ''}
    </div>`;

    // Phase 16 coverage: how much of the source an LLM actually saw (basis varies by method —
    // see trimCoverageText), and the map-reduce N-of-M segment count.
    const covParts = [];
    const covText = trimCoverageText(trim);
    if (covText) {
        covParts.push(covText);
    }
    if (trim.segments_total > 1) {
        covParts.push(`${trim.segments_analyzed ?? 0}/${trim.segments_total} segments`);
    }
    if (trim.llm_input_bytes > 0) {
        covParts.push(`LLM input: ${formatBytes(trim.llm_input_bytes)}`);
    }
    if (covParts.length > 0) {
        html += `<div style="font-size: 11px; color: var(--text-muted); margin-bottom: 6px;">
            Coverage: ${covParts.join(' · ')}
        </div>`;
    }
    const flagChip = (label, rgb) => `<span style="background: rgba(${rgb},0.12); border: 1px solid rgba(${rgb},0.35); color: rgba(${rgb},0.9); padding: 1px 6px; border-radius: 3px; font-size: 10px; margin-right: 4px;">${label}</span>`;
    const flags = [];
    if (trim.partial_coverage) flags.push(flagChip('partial coverage', '255,179,64'));
    if (trim.combine_truncated) flags.push(flagChip('findings truncated', '255,107,107'));
    if (trim.degenerate) flags.push(flagChip('degenerate (severe loss)', '255,107,107'));
    // content_lost is a superset of every specific flag (guaranteed by the Go emitters), so
    // the generic chip only adds information when no specific flag rendered. An EXPLICIT
    // false (the field is not omitempty) means verified lossless — distinct from legacy
    // records where the key is absent and nothing can be claimed either way.
    if (trim.content_lost && flags.length === 0) flags.push(flagChip('content lost', '255,179,64'));
    // The green chip only renders when NOTHING claims loss: a stale cached record can carry
    // an explicit false beside loss flags, and a contradictory pair would erode trust in the
    // honesty pipeline this panel surfaces.
    if (trim.content_lost === false && flags.length === 0) flags.push(flagChip('no content lost', '50,215,75'));
    if (flags.length > 0) {
        html += `<div style="font-size: 11px; color: var(--text-muted); margin-bottom: 6px;">
            ${flags.join('')}
        </div>`;
    }

    // Fields kept / dropped (structural methods)
    if (trim.fields_kept || trim.fields_dropped) {
        html += `<div style="font-size: 11px; color: var(--text-muted); margin-bottom: 6px;">
            Fields: <span style="color: rgba(50,215,75,0.8);">${trim.fields_kept} kept</span> · <span style="color: rgba(255,107,107,0.8);">${trim.fields_dropped} dropped</span>
        </div>`;
    }

    // Keywords extracted from the step instruction
    if (trim.keywords?.length > 0) {
        html += `<div style="font-size: 11px; color: var(--text-muted); margin-bottom: 6px;">
            Keywords: ${trim.keywords.map(k => `<span style="background: rgba(100,210,255,0.1); border: 1px solid rgba(100,210,255,0.2); padding: 1px 5px; border-radius: 3px; font-size: 10px; color: rgba(100,210,255,0.8);">${escapeHtml(k)}</span>`).join(' ')}
        </div>`;
    }

    // Fields selected by keyword relevance
    if (trim.matched_paths?.length > 0) {
        html += `<div style="font-size: 11px; color: var(--text-muted);">
            Matched: <span style="font-family: monospace; color: rgba(50,215,75,0.8);">${trim.matched_paths.map(p => escapeHtml(p)).join(', ')}</span>
        </div>`;
    }

    html += '</div>';
    return html;
}

function renderResolutionSummary(resolution) {
    if (!resolution) return '';
    const parts = [];
    if (resolution.auto_wired_count > 0) parts.push(`${resolution.auto_wired_count} auto-wired`);
    if (resolution.micro_resolved_count > 0) parts.push(`${resolution.micro_resolved_count} LLM-resolved`);
    if (resolution.auto_wiring_duration_us > 0) parts.push(`${resolution.auto_wiring_duration_us}\u00b5s`);
    if (resolution.micro_resolution_duration_ms > 0) parts.push(`${resolution.micro_resolution_duration_ms}ms LLM`);
    return parts.join(' · ');
}

function renderResolutionDetails(resolution) {
    if (!resolution) return '';
    const params = resolution.parameters || [];

    const layerColors = {
        'auto_wire': { bg: 'rgba(50, 215, 75, 0.15)', border: 'rgba(50, 215, 75, 0.4)', text: '#32d74b' },
        'micro_resolution': { bg: 'rgba(100, 160, 255, 0.15)', border: 'rgba(100, 160, 255, 0.4)', text: '#64a0ff' },
        'user_provided': { bg: 'rgba(255, 179, 64, 0.15)', border: 'rgba(255, 179, 64, 0.4)', text: '#ffb340' },
        'semantic_retry': { bg: 'rgba(175, 130, 255, 0.15)', border: 'rgba(175, 130, 255, 0.4)', text: '#af82ff' },
        'template_auto_include': { bg: 'rgba(255, 140, 50, 0.15)', border: 'rgba(255, 140, 50, 0.4)', text: '#ff8c32' }
    };

    let html = '<div style="padding: 8px 0;">';

    // Summary stats
    html += '<div style="display: flex; gap: 12px; margin-bottom: 12px; flex-wrap: wrap;">';
    if (resolution.auto_wired_count > 0) {
        html += `<span style="background: ${layerColors.auto_wire.bg}; border: 1px solid ${layerColors.auto_wire.border}; color: ${layerColors.auto_wire.text}; padding: 2px 8px; border-radius: 4px; font-size: 11px;">Auto-wired: ${resolution.auto_wired_count}</span>`;
    }
    if (resolution.micro_resolved_count > 0) {
        html += `<span style="background: ${layerColors.micro_resolution.bg}; border: 1px solid ${layerColors.micro_resolution.border}; color: ${layerColors.micro_resolution.text}; padding: 2px 8px; border-radius: 4px; font-size: 11px;">LLM Micro-resolved: ${resolution.micro_resolved_count}</span>`;
    }
    if (resolution.source_data_key_count > 0) {
        html += `<span style="background: rgba(255,255,255,0.05); border: 1px solid rgba(255,255,255,0.1); color: var(--text-muted); padding: 2px 8px; border-radius: 4px; font-size: 11px;">Source keys: ${resolution.source_data_key_count}</span>`;
    }
    html += '</div>';

    // Per-parameter table
    if (params.length > 0) {
        html += '<table style="width: 100%; border-collapse: collapse; font-size: 12px;">';
        html += '<thead><tr style="border-bottom: 1px solid rgba(255,255,255,0.1);">';
        html += '<th style="text-align: left; padding: 4px 8px; color: var(--text-muted);">Parameter</th>';
        html += '<th style="text-align: left; padding: 4px 8px; color: var(--text-muted);">Layer</th>';
        html += '<th style="text-align: left; padding: 4px 8px; color: var(--text-muted);">Match</th>';
        html += '<th style="text-align: left; padding: 4px 8px; color: var(--text-muted);">Value</th>';
        html += '</tr></thead><tbody>';
        params.forEach(p => {
            const colors = layerColors[p.layer] || { bg: 'transparent', border: 'rgba(255,255,255,0.1)', text: 'var(--text-secondary)' };
            const valueStr = typeof p.value === 'object' ? JSON.stringify(p.value) : String(p.value);
            const truncatedValue = valueStr.length > 60 ? valueStr.substring(0, 57) + '...' : valueStr;
            html += `<tr style="border-bottom: 1px solid rgba(255,255,255,0.05);">`;
            html += `<td style="padding: 4px 8px; font-family: monospace; color: var(--text-primary);">${escapeHtml(p.name)}</td>`;
            html += `<td style="padding: 4px 8px;"><span style="background: ${colors.bg}; border: 1px solid ${colors.border}; color: ${colors.text}; padding: 1px 6px; border-radius: 3px; font-size: 10px;">${p.layer === 'auto_wire' ? 'auto-wire' : p.layer === 'micro_resolution' ? 'LLM' : p.layer}</span></td>`;
            html += `<td style="padding: 4px 8px; color: var(--text-muted); font-size: 11px;">${p.match_type || '-'}${p.source_key ? ' (' + escapeHtml(p.source_key) + ')' : ''}</td>`;
            html += `<td style="padding: 4px 8px; font-family: monospace; color: var(--accent-blue); font-size: 11px;" title="${escapeHtml(valueStr)}">${escapeHtml(truncatedValue)}</td>`;
            html += '</tr>';
        });
        html += '</tbody></table>';
    }

    // Timing
    html += '<div style="margin-top: 8px; font-size: 11px; color: var(--text-muted);">';
    if (resolution.auto_wiring_duration_us > 0) html += `Auto-wire: ${resolution.auto_wiring_duration_us}\u00b5s`;
    if (resolution.micro_resolution_duration_ms > 0) html += ` · LLM: ${resolution.micro_resolution_duration_ms}ms`;
    if (resolution.dependency_step_ids?.length > 0) html += ` · Sources: ${resolution.dependency_step_ids.map(id => escapeHtml(id)).join(', ')}`;
    html += '</div>';

    html += '</div>';
    return html;
}

function toggleCapabilityJson(elementId) {
    const content = document.getElementById(elementId);
    if (content) {
        const isHidden = content.style.display === 'none';
        content.style.display = isHidden ? 'block' : 'none';
        // Update arrow - extract index from elementId (e.g., 'cap-json-0' -> '0')
        const idx = elementId.replace('cap-json-', '');
        const arrow = document.getElementById(`cap-arrow-${idx}`);
        if (arrow) arrow.textContent = isHidden ? '▼' : '▶';
    }
}

// ---------------------------------------------------------------------------
// Step Details tab
// ---------------------------------------------------------------------------

function renderStepDetails(container) {
    const lineageSummaries = selectedFamilySummaries();
    const targetStep = invokingStepTarget?.requestID === selected?.request_id ? invokingStepTarget.stepID : '';
    const targetPresent = !targetStep || (selected?.plan?.steps || []).some(step => step.step_id === targetStep);
    const targetNotice = targetPresent ? '' : `<div class="dag-data-warning" role="status">Invoking step ${escapeHtml(targetStep)} is unavailable in this retained parent record.</div>`;
    const clarificationDetails = renderRecordedClarification(selected) + renderPlannerClarifications(selected);
    if (!selected?.plan?.steps) {
        container.innerHTML = `${targetNotice}${clarificationDetails}<div class="empty-detail">No steps available</div>`;
        return;
    }

    const stepResults = {};
    // First check result.steps (for completed executions)
    if (selected.result?.steps) {
        selected.result.steps.forEach(step => {
            stepResults[step.step_id] = step;
        });
    }
    // Fall back to checkpoint.step_results for HITL-interrupted executions
    else if (selected.checkpoint?.step_results) {
        Object.values(selected.checkpoint.step_results).forEach(step => {
            stepResults[step.step_id] = step;
        });
    }

    // Track the current step (blocked by HITL) for proper status display
    const currentStepId = selected.checkpoint?.current_step?.step_id;

    // Build reverse dependency map (which steps depend on each step)
    const usedByMap = {};
    selected.plan.steps.forEach(step => {
        usedByMap[step.step_id] = [];
    });
    selected.plan.steps.forEach(step => {
        (step.depends_on || []).forEach(dep => {
            if (usedByMap[dep]) {
                usedByMap[dep].push(step.step_id);
            }
        });
    });

    // Helper to validate a timestamp is real (not Go's zero time "0001-01-01T00:00:00Z")
    const isValidTimestamp = (timestamp) => {
        if (!timestamp) return false;
        const date = new Date(timestamp);
        // Check if date is valid and after year 2020 (reasonable minimum)
        return !isNaN(date.getTime()) && date.getFullYear() > 2020;
    };

    // Helper to calculate wait time for a step
    // Wait time = step.start_time - max(dependency.end_time for all dependencies)
    // Source: orchestration/interfaces.go StepResult.StartTime, EndTime
    const calculateWaitTime = (step, result) => {
        const dependsOn = step.depends_on || [];
        if (dependsOn.length === 0) return null;
        if (!isValidTimestamp(result?.start_time)) return null;

        // Find the latest end_time among all dependencies
        let latestDepEndTime = null;
        dependsOn.forEach(depId => {
            const depResult = stepResults[depId];
            if (isValidTimestamp(depResult?.end_time)) {
                const depEnd = new Date(depResult.end_time);
                if (!latestDepEndTime || depEnd > latestDepEndTime) {
                    latestDepEndTime = depEnd;
                }
            }
        });

        if (!latestDepEndTime) return null;

        const stepStart = new Date(result.start_time);
        const waitMs = stepStart - latestDepEndTime;
        return waitMs >= 0 ? waitMs : 0;
    };

    // Helper to get duration in ms from result
    // Source: orchestration/interfaces.go StepResult.Duration (stored as nanoseconds)
    const getDurationMs = (result) => {
        if (!result) return null;
        // Check for pre-computed duration_ms first, then convert from nanoseconds
        if (result.duration_ms) return result.duration_ms;
        if (result.duration) return Math.round(result.duration / 1000000);
        return null;
    };

    // Compute phase breakpoints for phase dividers
    const phaseBreakpoints = new Map(); // stepIndex → { phaseFrom, phaseTo, continuationNote }
    if (selected.phase_plans && selected.phase_plans.length > 1) {
        let cumulativeIdx = 0;
        for (let pi = 0; pi < selected.phase_plans.length - 1; pi++) {
            cumulativeIdx += (selected.phase_plans[pi].steps || []).length;
            phaseBreakpoints.set(cumulativeIdx, {
                phaseFrom: pi + 1,
                phaseTo: pi + 2,
                continuationNote: selected.phase_plans[pi + 1].continuation_note || ''
            });
        }
    }

    container.innerHTML = `
        <div style="overflow-y: auto; height: 100%; padding: 16px;">
            ${targetNotice}
            ${clarificationDetails}
            ${selected.plan.steps.map((step, idx) => {
                const result = stepResults[step.step_id];
                // Phase divider before this step if it's the first step of a new phase
                const phaseDivider = phaseBreakpoints.has(idx) ? (() => {
                    const pb = phaseBreakpoints.get(idx);
                    return `<div class="dag-phase-divider">
                        <div class="dag-phase-divider-label">🔄 Phase ${pb.phaseFrom} → ${pb.phaseTo}</div>
                        ${pb.continuationNote ? `<div class="dag-phase-divider-note">${escapeHtml(pb.continuationNote)}</div>` : ''}
                    </div>`;
                })() : '';
                // Determine step status: completed/failed/skipped from result, blocked if current HITL step, pending otherwise
                let status;
                if (result) {
                    status = result.skipped ? 'skipped' : (result.success ? 'completed' : 'failed');
                } else if (currentStepId === step.step_id) {
                    status = 'blocked'; // Awaiting HITL approval
                } else {
                    status = 'pending';
                }
                // Get capability from metadata first (that's where orchestration stores it), then other fields
                const capability = step.metadata?.capability || result?.metadata?.capability || result?.capability || step.capability || step.capability_name || step.agent_name || 'N/A';
                const agentName = step.agent_name || result?.agent_name || 'N/A';
                const dependsOn = step.depends_on || [];
                const implicitDeps = step.implicit_deps || [];
                const usedBy = usedByMap[step.step_id] || [];
                const hasDependencies = dependsOn.length > 0 || implicitDeps.length > 0 || usedBy.length > 0;

                // Timing data from orchestration/interfaces.go StepResult
                const durationMs = getDurationMs(result);
                const waitTimeMs = calculateWaitTime(step, result);

                // Status-based step number colors
                const stepNumColors = {
                    'completed': 'background: linear-gradient(135deg, rgba(50, 215, 75, 0.3), rgba(50, 215, 75, 0.15)); color: #32d74b; border: 1px solid rgba(50, 215, 75, 0.4);',
                    'failed': 'background: linear-gradient(135deg, rgba(255, 107, 107, 0.3), rgba(255, 107, 107, 0.15)); color: #ff6b6b; border: 1px solid rgba(255, 107, 107, 0.4);',
                    'skipped': 'background: linear-gradient(135deg, rgba(128, 128, 128, 0.3), rgba(128, 128, 128, 0.15)); color: #888; border: 1px solid rgba(128, 128, 128, 0.4);',
                    'pending': 'background: linear-gradient(135deg, rgba(255, 179, 64, 0.3), rgba(255, 179, 64, 0.15)); color: #ffb340; border: 1px solid rgba(255, 179, 64, 0.4);',
                    'blocked': 'background: linear-gradient(135deg, rgba(255, 179, 64, 0.3), rgba(255, 179, 64, 0.15)); color: #ffb340; border: 1px solid rgba(255, 179, 64, 0.4);'
                };

                return `${phaseDivider}
                    <div class="dag-step-card ${status}" id="dag-step-details-${escapeHtmlAttribute(step.step_id)}" tabindex="-1">
                        <div data-lineage-request="${escapeHtmlAttribute(selected.request_id)}" data-lineage-step="${escapeHtmlAttribute(step.step_id)}">${renderDelegatedExecutionLinks(step.step_id, lineageSummaries)}</div>
                        <div class="dag-step-header">
                            <div class="dag-step-title">
                                <span class="dag-step-number" title="${escapeHtmlAttribute(executionStepNumberingNote(selected))}" style="${stepNumColors[status]} padding: 4px 12px; border-radius: 8px; font-weight: 700; font-size: 12px;">${executionDisplayStepLabel(idx, selected)}</span>
                                <span class="dag-step-id">${step.step_id}</span>
                            </div>
                            <div style="display: flex; align-items: center; gap: 12px;">
                                ${durationMs !== null ? `<span class="timing-badge execution-time" data-tooltip="⏱ Execution Time\n\nHow long this step took to execute from start to completion.">⏱ ${formatDuration(durationMs)}</span>` : ''}
                                ${waitTimeMs !== null && waitTimeMs > 0 ? `<span class="timing-badge wait-time" data-tooltip="⏳ Wait Time\n\nTime this step spent waiting for its dependencies to complete before starting execution.">⏳ ${formatDuration(waitTimeMs)}</span>` : ''}
                                <span class="dag-step-status ${status}">
                                    ${status === 'completed' ? '✓ Completed' : status === 'failed' ? '✗ Failed' : status === 'skipped' ? '⊘ Skipped' : status === 'blocked' ? '⏸ Blocked' : '◐ Pending'}
                                </span>
                            </div>
                        </div>
                        <div class="dag-step-body">
                            <div class="dag-step-info">
                                <span class="dag-step-label">Agent</span>
                                <span class="dag-step-value highlight">${agentName}</span>
                                <span class="dag-step-label">Capability</span>
                                <span class="dag-step-value" style="color: var(--accent-purple);">${capability}</span>
                                ${step.instruction ? `
                                    <span class="dag-step-label">Instruction</span>
                                    <span class="dag-step-value">${step.instruction.length > 500 ? `
                                        <div class="instruction-collapsible" id="instruction-${step.step_id}">
                                            <div class="instruction-text collapsed">${escapeHtml(step.instruction)}</div>
                                            <button class="instruction-toggle" data-toggle-instruction="${step.step_id}">Show more</button>
                                        </div>
                                    ` : escapeHtml(step.instruction)}</span>
                                ` : ''}
                                ${result?.skipped && result?.skip_reason ? `
                                    <span class="dag-step-label">Skip Reason</span>
                                    <span class="dag-step-value" style="color: var(--text-muted); font-style: italic;">${escapeHtml(result.skip_reason)}</span>
                                ` : ''}
                            </div>
                            ${hasDependencies ? `
                                <div class="dag-step-depends">
                                    ${dependsOn.length > 0 ? `
                                        <div class="dag-step-depends-row">
                                            <span class="dag-step-depends-label">⬅ Waits for:</span>
                                            <div class="dag-step-depends-list">
                                                ${dependsOn.map(dep => `<span class="dag-step-depends-item">${dep}</span>`).join('')}
                                            </div>
                                        </div>
                                    ` : ''}
                                    ${implicitDeps.length > 0 ? `
                                        <div class="dag-step-depends-row">
                                            <span class="dag-step-depends-label" style="color: ${DAG_PROVENANCE_COLOR};">⇠ Uses earlier results:</span>
                                            <div class="dag-step-depends-list">
                                                ${implicitDeps.map(dep => `<span class="dag-step-depends-item" title="Data lineage only; this does not control execution order">${dep}</span>`).join('')}
                                            </div>
                                        </div>
                                    ` : ''}
                                    ${usedBy.length > 0 ? `
                                        <div class="dag-step-depends-row">
                                            <span class="dag-step-depends-label">➡ Used by:</span>
                                            <div class="dag-step-depends-list">
                                                ${usedBy.map(dep => `<span class="dag-step-depends-item used-by">${dep}</span>`).join('')}
                                            </div>
                                        </div>
                                    ` : ''}
                                </div>
                            ` : ''}
                            ${result?.parameters && Object.keys(result.parameters).length > 0 ? `
                                <div class="dag-step-response">
                                    <div class="dag-step-response-header" data-toggle-section="step-request-${step.step_id}">
                                        <span><span class="expand-arrow" id="step-request-${step.step_id}-arrow">▶</span> View Request</span>
                                        <span style="color: var(--text-muted);">Click to expand</span>
                                    </div>
                                    <div id="step-request-${step.step_id}" class="dag-step-response-content" style="display: none;">
                                        ${formatResponseJson(result.parameters)}
                                    </div>
                                </div>
                            ` : ''}
                            ${result?.dispatches?.length ? `
                                <details class="dag-step-response">
                                    <summary class="dag-step-response-header">HTTP dispatch history (${result.dispatches.length})</summary>
                                    <div class="dag-step-response-content">${formatResponseJson(result.dispatches)}</div>
                                </details>
                            ` : ''}
                            ${result?.tool_error ? `
                                <details class="dag-step-response">
                                    <summary class="dag-step-response-header">Structured failure (not successful output)</summary>
                                    <div class="dag-step-response-content">${formatResponseJson(result.tool_error)}</div>
                                </details>
                            ` : ''}
                            ${result?.metadata?.bindings ? `
                                <details class="dag-step-response">
                                    <summary class="dag-step-response-header">Parameter binding evidence</summary>
                                    <div class="dag-step-response-content">${formatResponseJson(result.metadata.bindings)}</div>
                                </details>
                            ` : ''}
                            ${result?.metadata?.resolution ? `
                                <div class="dag-step-response">
                                    <div class="dag-step-response-header" data-toggle-section="step-resolution-${step.step_id}">
                                        <span><span class="expand-arrow" id="step-resolution-${step.step_id}-arrow">▶</span> Resolution</span>
                                        <span style="color: var(--text-muted);">${renderResolutionSummary(result.metadata.resolution)}${result?.metadata?.template_auto_includes?.length > 0 ? ` · <span style="color: #ff8c32;">${result.metadata.template_auto_includes.length} auto-included</span>` : ''}</span>
                                    </div>
                                    <div id="step-resolution-${step.step_id}" class="dag-step-response-content" style="display: none;">
                                        ${renderResolutionDetails(result.metadata.resolution)}
                                    </div>
                                </div>
                            ` : ''}
                            ${hasTrimDisplay(result?.metadata?.result_trim) ? `
                                <div class="dag-step-response">
                                    <div class="dag-step-response-header" data-toggle-section="step-trim-${step.step_id}">
                                        <span><span class="expand-arrow" id="step-trim-${step.step_id}-arrow">▶</span> Result Trim</span>
                                        <span style="color: var(--text-muted);">${renderTrimSummary(result.metadata.result_trim)}</span>
                                    </div>
                                    <div id="step-trim-${step.step_id}" class="dag-step-response-content" style="display: none;">
                                        ${renderTrimDetails(result.metadata.result_trim)}
                                    </div>
                                </div>
                            ` : ''}
                            ${result?.metadata?.template_auto_includes?.length > 0 ? `
                                <div style="background: rgba(255, 140, 50, 0.08); border: 1px solid rgba(255, 140, 50, 0.3); border-radius: 8px; padding: 10px 14px; margin: 8px 0;">
                                    <div style="display: flex; align-items: center; gap: 6px; margin-bottom: 6px;">
                                        <span style="color: #ff8c32; font-weight: 600; font-size: 12px;">⚠ Template Auto-Include (${result.metadata.template_auto_includes.length})</span>
                                    </div>
                                    <div style="font-size: 11px; color: var(--text-muted); margin-bottom: 6px;">
                                        These template references were resolved via the safety net — the LLM did not list them in depends_on.
                                    </div>
                                    ${result.metadata.template_auto_includes.map(ai => `
                                        <div style="display: flex; gap: 8px; align-items: baseline; margin: 3px 0; font-size: 11px;">
                                            <span style="color: #ff8c32; font-family: monospace;">${escapeHtml(ai.referenced_step)}</span>
                                            <span style="color: var(--text-muted);">←</span>
                                            <span style="color: var(--text-secondary); font-family: monospace; font-size: 10px;">${escapeHtml(ai.template)}</span>
                                        </div>
                                    `).join('')}
                                </div>
                            ` : ''}
                            ${result?.error && !result?.success ? `
                                <div class="dag-step-error">
                                    <strong>Error:</strong> ${escapeHtml(result.error)}
                                </div>
                            ` : ''}
                            ${result?.response ? `
                                <div class="dag-step-response">
                                    <div class="dag-step-response-header" data-toggle-section="step-response-${step.step_id}">
                                        <span><span class="expand-arrow" id="step-response-${step.step_id}-arrow">▶</span> View Response</span>
                                        <span style="color: var(--text-muted);">Click to expand</span>
                                    </div>
                                    <div id="step-response-${step.step_id}" class="dag-step-response-content" style="display: none;">
                                        ${formatResponseJson(result.response)}
                                    </div>
                                </div>
                            ` : ''}
                        </div>
                    </div>
                `;
            }).join('')}
        </div>
    `;
    // Click handlers are bound once on dagDetailContent in bindDelegatedEvents()
    // — no per-render listener needed here.
}

// ---------------------------------------------------------------------------
// LLM Calls tab
// ---------------------------------------------------------------------------

// Helper function to copy text and show feedback on button
function copyTextWithFeedback(text, btn) {
    if (!text || !btn) return;
    navigator.clipboard.writeText(text).then(() => {
        const originalText = btn.textContent;
        btn.textContent = 'Copied!';
        btn.classList.add('copied');
        setTimeout(() => {
            btn.textContent = originalText;
            btn.classList.remove('copied');
        }, 1500);
    }).catch(err => console.error('Copy failed:', err));
}

function copyDAGLLMContent(index, type, evt) {
    if (!selected?.llm_interactions?.[index]) return;
    const interaction = selected.llm_interactions[index];
    const text = getLLMInteractionContent(interaction, type);
    copyTextWithFeedback(text, evt?.target || evt);
}

function toggleLLMInteraction(idx, type) {
    const content = document.getElementById(`llm-${type}-${idx}`);
    const arrow = document.getElementById(`llm-${type}-arrow-${idx}`);
    if (!content) return;

    const isHidden = content.style.display === 'none';

    // Deferred rendering: populate content on first expand (Phase 3c)
    if (isHidden && content.dataset.deferred && !content.dataset.rendered) {
        const interaction = selected?.llm_interactions?.[parseInt(content.dataset.llmIdx)];
        if (interaction) {
            if (type === 'system_prompt' || type === 'prompt') {
                content.innerHTML = `<pre style="white-space: pre-wrap; word-wrap: break-word; font-size: 11px; color: var(--text-secondary);">${escapeHtml(getLLMInteractionContent(interaction, type))}</pre>`;
            } else if (type === 'response') {
                content.innerHTML = formatResponseJson(interaction.response || '');
            }
        }
        content.dataset.rendered = 'true';
    }

    content.style.display = isHidden ? 'block' : 'none';
    if (arrow) arrow.textContent = isHidden ? '▼' : '▶';
}

// ---------------------------------------------------------------------------
// Pre-Execution tab — BeforePlanning / AfterPlanning hooks and their activity
// ---------------------------------------------------------------------------

function renderPreExecution(container) {
    const interactions = selected?.llm_interactions || [];
    const { isPreHook } = classifyInteractions(interactions);
    const preSteps = interactions.filter(isPreHook);
    const pipelineHooks = (selected?.pipeline_hooks || []).filter(isPreExecutionHook);

    if (preSteps.length === 0 && pipelineHooks.length === 0) {
        container.innerHTML = `
            <div class="empty-detail">
                <div class="empty-detail-icon">🔄</div>
                <div>No pre-execution hook data for this request</div>
            </div>`;
        return;
    }

    const totalDuration = preSteps.reduce((sum, s) => sum + (s.duration_ms || 0), 0);

    // Group by hook source. The `otherSteps` catch-all guarantees every
    // pre-phase step lands in exactly one rendered section even as new
    // pre-phase types are added in Go — no silent drops.
    const userMemorySteps = preSteps.filter(i => i.type?.startsWith('user_memory_'));
    const historyPrepSteps = preSteps.filter(i =>
        i.type === 'conversation_history_prepare' ||
        i.type === 'conversation_history_compaction'
    );
    const compactionSteps = preSteps.filter(i =>
        i.type === 'activity_compaction' || i.type === 'activity_compaction_incremental'
    );
    const knownSet = new Set([...userMemorySteps, ...historyPrepSteps, ...compactionSteps]);
    const otherSteps = preSteps.filter(i => !knownSet.has(i));

    let html = `<div style="overflow-y: auto; height: 100%; padding: 16px;">`;
    html += `<div style="margin-bottom: 16px; font-size: 13px; color: var(--text-muted);">
        ${pipelineHooks.length} hook invocation${pipelineHooks.length === 1 ? '' : 's'} | ${preSteps.length} recorded operation${preSteps.length === 1 ? '' : 's'} | ${formatDuration(totalDuration)} aggregate operation time | Runs before step execution
    </div>`;
    html += renderPipelineHookObservations(pipelineHooks);

    // User memory enrichment section
    if (userMemorySteps.length > 0) {
        html += `<div style="margin-bottom: 20px;">
            <div style="font-size: 14px; font-weight: 600; color: #f0a030; margin-bottom: 10px; display: flex; align-items: center; gap: 8px;">
                📝 User Memory Enrichment
                <span style="font-size: 11px; font-weight: 400; color: var(--text-muted);">${userMemorySteps.length} steps</span>
            </div>`;
        userMemorySteps.forEach((step, idx) => {
            const config = getLLMCardConfig(step.type);
            const isLLM = (step.category || 'llm') === 'llm';
            const label = step.type?.replace('user_memory_', '').replace(/_/g, ' ') || step.type;
            html += `
                <div style="display: flex; align-items: center; gap: 10px; padding: 10px 14px; margin-bottom: 4px;
                            border-left: 3px solid rgba(${config.rgb}, ${isLLM ? '0.8' : '0.4'}); background: rgba(${config.rgb}, 0.04);
                            border-radius: 8px; font-size: 12px;">
                    <span style="min-width: 20px;">${config.icon}</span>
                    <span style="font-weight: 500; min-width: 140px; color: ${config.color};">${escapeHtml(label)}</span>
                    <span style="color: var(--text-muted); font-size: 11px; min-width: 60px;">${formatDuration(step.duration_ms || 0)}</span>
                    <span style="color: ${step.success !== false ? 'var(--accent-green)' : 'var(--accent-red)'}; font-size: 11px;">${step.success !== false ? '✓' : '✗'}</span>
                    ${step.response ? `<span style="color: var(--text-muted); font-size: 11px; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; flex: 1;">${escapeHtml(step.response)}</span>` : ''}
                </div>`;
        });
        html += `</div>`;
    }

    // Conversation history preparation section
    if (historyPrepSteps.length > 0) {
        html += `<div style="margin-bottom: 20px;">
            <div style="font-size: 14px; font-weight: 600; color: #6cc2ff; margin-bottom: 10px; display: flex; align-items: center; gap: 8px;">
                🗜️ Conversation History
                <span style="font-size: 11px; font-weight: 400; color: var(--text-muted);">${historyPrepSteps.length} call${historyPrepSteps.length === 1 ? '' : 's'}</span>
            </div>`;
        historyPrepSteps.forEach(step => {
            html += renderSingleLLMCard(step, interactions.indexOf(step));
        });
        html += `</div>`;
    }

    // Memory compaction section
    if (compactionSteps.length > 0) {
        html += `<div style="margin-bottom: 20px;">
            <div style="font-size: 14px; font-weight: 600; color: #f0a030; margin-bottom: 10px; display: flex; align-items: center; gap: 8px;">
                📝 Memory Compaction
                <span style="font-size: 11px; font-weight: 400; color: var(--text-muted);">${compactionSteps.length} calls</span>
            </div>`;
        compactionSteps.forEach(step => {
            // idx must be the index into `interactions` (unfiltered), not `preSteps`,
            // so delegated click handlers resolve data-toggle-llm / data-copy-llm
            // ids back to the correct entry in selected.llm_interactions.
            html += renderSingleLLMCard(step, interactions.indexOf(step));
        });
        html += `</div>`;
    }

    // Catch-all for future pre-phase types that don't match a known section.
    // Prevents silent drops when new hook_phase="pre" types are added in Go.
    if (otherSteps.length > 0) {
        html += `<div style="margin-bottom: 20px;">
            <div style="font-size: 14px; font-weight: 600; color: var(--text-muted); margin-bottom: 10px; display: flex; align-items: center; gap: 8px;">
                📝 Other Pre-Execution Activity
                <span style="font-size: 11px; font-weight: 400; color: var(--text-muted);">${otherSteps.length} calls</span>
            </div>`;
        otherSteps.forEach(step => {
            // See note on compactionSteps above — idx is the unfiltered-array index.
            html += renderSingleLLMCard(step, interactions.indexOf(step));
        });
        html += `</div>`;
    }

    html += `</div>`;
    container.innerHTML = html;
}

// ---------------------------------------------------------------------------
// Post-Execution tab — AfterExecution / AfterSynthesis hooks and their activity
// ---------------------------------------------------------------------------

function renderPostExecution(container) {
    const interactions = selected?.llm_interactions || [];
    const { isPostHook } = classifyInteractions(interactions);
    const postSteps = interactions.filter(isPostHook);
    const pipelineHooks = (selected?.pipeline_hooks || []).filter(isPostExecutionHook);
    const governedResponsePanel = renderGovernedResponsePanel(interactions);

    if (postSteps.length === 0 && pipelineHooks.length === 0 && !governedResponsePanel) {
        container.innerHTML = `
            <div class="empty-detail">
                <div class="empty-detail-icon">🔄</div>
                <div>No post-execution hook data for this request</div>
            </div>`;
        return;
    }

    const totalDuration = postSteps.reduce((sum, s) => sum + (s.duration_ms || 0), 0);
    const llmCount = postSteps.filter(s => (s.category || 'llm') === 'llm').length;

    // Group by hook source. The `otherSteps` catch-all guarantees every
    // post-phase step lands in exactly one rendered section even as new
    // post-phase types are added in Go — no silent drops.
    const userMemorySteps = postSteps.filter(i => i.type?.startsWith('user_memory_'));
    const eventSumSteps = postSteps.filter(i => i.type === 'event_summarization');
    const knownSet = new Set([...userMemorySteps, ...eventSumSteps]);
    const otherSteps = postSteps.filter(i => !knownSet.has(i));

    let html = `<div style="overflow-y: auto; height: 100%; padding: 16px;">`;
    html += `<div style="margin-bottom: 16px; font-size: 13px; color: var(--text-muted);">
        ${pipelineHooks.length} hook invocation${pipelineHooks.length === 1 ? '' : 's'} | ${postSteps.length} recorded operation${postSteps.length === 1 ? '' : 's'} | ${llmCount} LLM call${llmCount === 1 ? '' : 's'} | ${formatDuration(totalDuration)} aggregate operation time
    </div>`;
    html += renderPipelineHookObservations(pipelineHooks);
    html += governedResponsePanel;

    // User memory extraction section
    if (userMemorySteps.length > 0) {
        const umLLMCount = userMemorySteps.filter(s => (s.category || 'llm') === 'llm').length;
        html += `<div style="margin-bottom: 20px;">
            <div style="font-size: 14px; font-weight: 600; color: #f0a030; margin-bottom: 10px; display: flex; align-items: center; gap: 8px;">
                📝 User Memory Extraction
                <span style="font-size: 11px; font-weight: 400; color: var(--text-muted);">${userMemorySteps.length} steps, ${umLLMCount} LLM</span>
            </div>`;
        userMemorySteps.forEach((step, idx) => {
            html += renderSingleLLMCard(step, interactions.indexOf(step));
        });
        html += `</div>`;
    }

    // Event summarization section
    if (eventSumSteps.length > 0) {
        html += `<div style="margin-bottom: 20px;">
            <div style="font-size: 14px; font-weight: 600; color: #f0a030; margin-bottom: 10px; display: flex; align-items: center; gap: 8px;">
                📝 Event Summarization
                <span style="font-size: 11px; font-weight: 400; color: var(--text-muted);">${eventSumSteps.length} calls</span>
            </div>`;
        eventSumSteps.forEach(step => {
            html += renderSingleLLMCard(step, interactions.indexOf(step));
        });
        html += `</div>`;
    }

    // Catch-all for future post-phase types that don't match a known section.
    // Prevents silent drops when new hook_phase="post" types are added in Go.
    if (otherSteps.length > 0) {
        html += `<div style="margin-bottom: 20px;">
            <div style="font-size: 14px; font-weight: 600; color: var(--text-muted); margin-bottom: 10px; display: flex; align-items: center; gap: 8px;">
                📝 Other Post-Execution Activity
                <span style="font-size: 11px; font-weight: 400; color: var(--text-muted);">${otherSteps.length} calls</span>
            </div>`;
        otherSteps.forEach(step => {
            html += renderSingleLLMCard(step, interactions.indexOf(step));
        });
        html += `</div>`;
    }

    html += `</div>`;
    container.innerHTML = html;
    // Click handlers are bound once on dagDetailContent in bindDelegatedEvents()
    // — the unified delegated handler picks up [data-toggle-llm] and
    // [data-copy-llm] inside the rendered LLM cards.
}

// LLM card-detail label overrides — used only by getLLMCardConfig() below.
// These verbose labels are shown in the LLM card detail panel inside the
// DAG, where space is plentiful; the planner-column node labels (shorter,
// e.g. "Tier Select" vs. "Tool Catalog Selection") come from the registry's
// primary `label` field. Migrating these into the registry as a third
// label dimension would propagate context-specific labels everywhere; we
// keep them localized here instead.
const dagCardLabelOverrides = {
    tiered_selection: 'Tool Catalog Selection',
    plan_generation: 'Plan Generation',
    plan_regeneration_fallback: 'Plan Regeneration (retry)',
    synthesis_streaming: 'Synthesis (streaming)',
    micro_resolution: 'Micro Resolution',
    correction: 'Correction',
    hallucination_detection: 'Hallucination Detection',
    result_distillation: 'Result Distillation',
    continuation_plan_generation: 'Continuation Plan',
    continuation_plan_regeneration: 'Continuation Plan (retry)',
    conversation_history_prepare: 'History Preparation',
    conversation_history_compaction: 'History Compaction',
};

// Returns the per-type card-detail config: { rgb, bg, border, color, icon, label }.
// Reads colors/icon from the LLM-type registry. Label resolution is a
// three-tier fallback so the card detail (which has space for verbose
// labels) keeps the labels it had before this migration:
//   1. dagCardLabelOverrides — for ~12 types whose card-detail label is
//      neither the short DAG primary nor the LLM Debug listLabel
//      (e.g. tiered_selection -> "Tool Catalog Selection").
//   2. cfg.listLabel — for the ~16 user_memory_* types and a few others
//      whose old card-detail label happens to match the LLM Debug list
//      label exactly (e.g. user_memory_recall_identity ->
//      "Recall: identity facts").
//   3. cfg.label — the short DAG primary, used when neither override
//      exists.
// For unknown types, the registry's HSL hash fallback supplies stable
// colors and a default "💬" icon, and `cfg.label` is the snake-case-with-
// spaces type name.
function getLLMCardConfig(type) {
    const cfg = getLLMType(type);
    return {
        rgb: cfg.rgb,
        bg: `rgba(${cfg.rgb}, 0.15)`,
        border: `rgba(${cfg.rgb}, 0.3)`,
        color: cfg.accent,
        icon: cfg.icon,
        label: dagCardLabelOverrides[type] || cfg.listLabel || cfg.label,
    };
}

// `idx` is the original index into selected.llm_interactions — used for
// DOM ids and data-attrs so deferred content lookup and click handlers
// stay correct. `displayNum` (optional) is the human-facing "#N" shown in
// the card header; when omitted it falls back to idx+1. This lets the
// LLM Calls tab renumber contiguously after filtering out hook ops
// without breaking per-interaction DOM references. `displayLabel` (optional)
// is the traceability label shown next to the type; when omitted
// it falls back to `callLabel(interaction)`. Callers that compute per-tab
// collision disambiguation pass a suffixed variant (e.g. "... #2"); the
// default is the plain base label.
function renderSingleLLMCard(interaction, idx, displayNum, displayLabel) {
    const config = getLLMCardConfig(interaction.type);
    const _r = config.rgb;
    const category = interaction.category || 'llm';
    const isStreamingSynthesis = interaction.type === 'synthesis_streaming';
    const isSynthesis = interaction.type === 'synthesis' || isStreamingSynthesis;
    const typeLabel = isStreamingSynthesis
        ? 'LLM synthesis output streamed to client'
        : interaction.type === 'synthesis'
            ? 'Pre-hook LLM synthesis output'
        : (config.label || interaction.type || 'Unknown');
    const shownNum = displayNum != null ? displayNum : (idx + 1);
    // Prefer the globally-annotated label (stamped by annotateCallLabels)
    // so popup and card suffixes always match. Falls back to the base
    // label if annotation hasn't run yet — safe default.
    const shownLabel = displayLabel != null
        ? displayLabel
        : (interaction._callLabel || callLabel(interaction));
    const isSuccessful = interaction.success !== false;
    const nonFatalNote = isSuccessful && !!interaction.error;
    const statusLabel = isSuccessful
        ? (nonFatalNote ? '✓ Note' : '✓ Success')
        : '✗ Failed';
    const statusClass = isSuccessful ? 'completed' : 'failed';

    // Non-LLM interactions render as compact rows (vector_db, storage, embedding, logic)
    if (category !== 'llm') {
        const status = interaction.success !== false ? '✓' : '✗';
        const statusColor = interaction.success !== false ? 'var(--accent-green)' : 'var(--accent-red)';
        const summary = interaction.response ? escapeHtml(interaction.response) : '';
        return `
            <div id="dag-llm-call-${idx}" tabindex="-1" class="dag-llm-call-card" style="display: flex; align-items: center; gap: 10px; padding: 8px 14px; margin-bottom: 4px;
                        border-left: 3px solid rgba(${_r}, 0.5); background: rgba(${_r}, 0.04);
                        border-radius: 8px; font-size: 12px;">
                <span style="opacity: 0.5; min-width: 24px; font-size: 11px;">#${shownNum}</span>
                <span>${config.icon}</span>
                <span style="font-weight: 500; min-width: 130px; color: ${config.color};" title="${escapeHtml(interaction.type || '')}">${typeLabel}</span>
                <span style="color: var(--text-muted); font-size: 11px; min-width: 60px;">${formatDuration(interaction.duration_ms || 0)}</span>
                <span style="color: ${statusColor}; font-size: 11px;">${status}</span>
                ${summary ? `<span style="color: var(--text-muted); font-size: 11px; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; flex: 1;">${summary}</span>` : ''}
            </div>`;
    }

    // Check if we have actual LLM metadata (provider, model, tokens).
    // Extraction/reconciliation calls go through swappable interfaces that don't expose
    // AIResponse metadata — they record as category "llm" but with empty fields.
    const hasLLMMetadata = !!(interaction.provider || interaction.model || interaction.prompt_tokens || interaction.completion_tokens);

    // Full LLM card for llm-category interactions
    const baseShadow = `0 0 20px rgba(${_r}, 0.15), 0 0 6px rgba(${_r}, 0.35), 0 8px 32px rgba(0,0,0,0.25), inset 0 1px 0 rgba(255,255,255,0.1)`;
    const hoverShadow = `0 0 30px rgba(${_r}, 0.22), 0 0 10px rgba(${_r}, 0.45), 0 16px 48px rgba(0,0,0,0.3), inset 0 1px 0 rgba(255,255,255,0.15)`;
    const provider = (interaction.provider || 'unknown').toLowerCase();
    const providerColors = {
        'openai': { bg: 'rgba(16, 163, 127, 0.2)', border: 'rgba(16, 163, 127, 0.4)', color: '#10a37f' },
        'anthropic': { bg: 'rgba(204, 153, 102, 0.2)', border: 'rgba(204, 153, 102, 0.4)', color: '#cc9966' },
        'groq': { bg: 'rgba(255, 140, 0, 0.2)', border: 'rgba(255, 140, 0, 0.4)', color: '#ff8c00' },
        'gemini': { bg: 'rgba(66, 133, 244, 0.2)', border: 'rgba(66, 133, 244, 0.4)', color: '#4285f4' },
        'deepseek': { bg: 'rgba(0, 122, 255, 0.2)', border: 'rgba(0, 122, 255, 0.4)', color: '#007aff' },
        'default': { bg: 'rgba(150, 150, 150, 0.2)', border: 'rgba(150, 150, 150, 0.4)', color: '#aaaaaa' }
    };
    const pColors = providerColors[provider] || providerColors['default'];
    const providerBadge = '<span style="display: inline-block; padding: 3px 10px; background: linear-gradient(135deg, ' + pColors.bg + ' 0%, rgba(255,255,255,0.05) 100%); border: 1px solid ' + pColors.border + '; border-radius: 5px; font-size: 12px; font-weight: 600; color: ' + pColors.color + '; letter-spacing: 0.3px;">' + (interaction.provider || 'Unknown') + '</span>';
    const promptSections = getLLMPromptSections(interaction).map((section) => `
                <div class="dag-step-response" style="background: rgba(0, 0, 0, 0.15); border-radius: 12px; margin-top: 8px; overflow: hidden;">
                    <div class="dag-step-response-header" data-toggle-llm="${idx}" data-llm-type="${section.type}" style="background: rgba(255, 255, 255, 0.03); border-bottom: 1px solid rgba(255, 255, 255, 0.06);">
                        <span><span class="expand-arrow" id="llm-${section.type}-arrow-${idx}">▶</span> View ${section.label}</span>
                        <span style="display: flex; align-items: center; gap: 8px;">
                            <button class="copy-inline-btn" data-copy-llm="${idx}" data-copy-type="${section.type}">Copy</button>
                            ${section.type === 'prompt' ? `<span style="color: var(--text-muted); font-size: 10px;">${(interaction.prompt_tokens || 0).toLocaleString()} total input tokens</span>` : ''}
                        </span>
                    </div>
                    <div id="llm-${section.type}-${idx}" class="dag-step-response-content" style="display: none; background: rgba(0, 0, 0, 0.2);" data-deferred="${section.type}" data-llm-idx="${idx}">
                    </div>
                </div>
    `).join('');

    return `
        <div id="dag-llm-call-${idx}" tabindex="-1" class="dag-step-card csp-hover-card dag-llm-call-card" style="--card-border: ${config.border}; --card-hover-border: rgba(${_r}, 0.4); --card-shadow: ${baseShadow}; --card-hover-shadow: ${hoverShadow}; background: linear-gradient(135deg, ${config.bg} 0%, rgba(255, 255, 255, 0.03) 100%); backdrop-filter: blur(16px) saturate(180%); -webkit-backdrop-filter: blur(16px) saturate(180%); border: 1px solid var(--active-card-border); box-shadow: var(--active-card-shadow);">
            <div class="dag-step-header dag-llm-card-header">
                <div class="dag-step-title">
                    <span style="padding: 4px 12px; border-radius: 8px; font-weight: 700; font-size: 12px; background: linear-gradient(135deg, ${config.bg}, rgba(255, 255, 255, 0.05)); border: 1px solid ${config.border}; color: ${config.color};">${config.icon} #${shownNum}</span>
                    <span class="dag-step-id" style="color: ${config.color}; font-weight: 600;" title="${escapeHtml(interaction.type || '')}">${typeLabel}</span>
                    ${shownLabel ? `<span style="font-family: 'SF Mono', monospace; font-size: 11px; color: var(--text-muted);" title="Correlation ID — matches the Call ID shown in the DAG node popup">${escapeHtml(shownLabel)}</span>` : ''}
                    ${interaction.step_id ? `<span class="step-id-badge" title="Associated with step">📍 ${interaction.step_id}</span>` : ''}
                </div>
                <div class="dag-llm-card-actions">
                    ${renderInteractionTraceLink(interaction)}
                    <span style="font-family: 'SF Mono', monospace; font-size: 11px; color: var(--text-muted);">
                        ${formatDuration(interaction.duration_ms || 0)}
                    </span>
                    <span class="dag-step-status ${statusClass}">
                        ${statusLabel}
                    </span>
                </div>
            </div>
            <div class="dag-step-body">
                ${hasLLMMetadata ? `
                <div class="dag-step-info">
                    <span class="dag-step-label">Provider</span>
                    <span class="dag-step-value">${providerBadge}</span>
                    <span class="dag-step-label">Model</span>
                    <span class="dag-step-value" style="color: var(--accent-teal);">${interaction.model || 'N/A'}</span>
                    <span class="dag-step-label">Tokens</span>
                    <span class="dag-step-value"><span style="color: var(--accent-green);">${interaction.prompt_tokens || 0}</span> in / <span style="color: var(--accent-purple);">${interaction.completion_tokens || 0}</span> out</span>
                    <span class="dag-step-label">Temperature</span>
                    <span class="dag-step-value">${interaction.temperature ?? 'N/A'}</span>
                </div>` : `
                <div style="padding: 4px 0; font-size: 12px; color: var(--text-muted);">
                    ${interaction.response ? escapeHtml(interaction.response) : 'LLM call via swappable interface — metadata not captured'}
                </div>`}
                ${!interaction.success && interaction.error ? `
                    <div class="dag-step-error">
                        <strong>Error:</strong> ${interaction.error}
                    </div>
                ` : ''}
                ${nonFatalNote ? `
                    <div class="dag-step-note">
                        <strong>Note:</strong> ${interaction.error}
                    </div>
                ` : ''}
                ${promptSections}
                <div class="dag-step-response" style="background: rgba(0, 0, 0, 0.15); border-radius: 12px; margin-top: 8px; overflow: hidden;">
                    <div class="dag-step-response-header" data-toggle-llm="${idx}" data-llm-type="response" style="background: rgba(255, 255, 255, 0.03); border-bottom: 1px solid rgba(255, 255, 255, 0.06);">
                        <span><span class="expand-arrow" id="llm-response-arrow-${idx}">▶</span> ${isStreamingSynthesis ? 'View Streamed Output' : isSynthesis ? 'View Pre-hook Output' : 'View Response'}</span>
                        <span style="display: flex; align-items: center; gap: 8px;">
                            <button class="copy-inline-btn" data-copy-llm="${idx}" data-copy-type="response">Copy</button>
                            <span style="color: var(--text-muted); font-size: 10px;">${(interaction.completion_tokens || 0).toLocaleString()} tokens</span>
                        </span>
                    </div>
                    <div id="llm-response-${idx}" class="dag-step-response-content" style="display: none; background: rgba(0, 0, 0, 0.2);" data-deferred="response" data-llm-idx="${idx}">
                    </div>
                </div>
            </div>
        </div>
    `;
}

function renderGovernedResponsePanel(interactions) {
    const synthesisOutputs = (interactions || []).filter(interaction =>
        interaction.type === 'synthesis' || interaction.type === 'synthesis_streaming'
    );
    const latestSynthesis = synthesisOutputs.at(-1);
    const latestOutput = latestSynthesis?.response || '';
    const isStreamingSynthesis = latestSynthesis?.type === 'synthesis_streaming';
    const governedResponse = selected?.final_response;
    if (typeof governedResponse !== 'string') {
        return '';
    }
    const comparisonKnown = !!latestSynthesis && typeof latestSynthesis.response === 'string';
    const governedDiffers = comparisonKnown && governedResponse !== latestOutput;
    return `
        <div class="dag-step-card" style="margin-bottom: 20px; background: linear-gradient(135deg, rgba(50, 215, 75, 0.16), rgba(182, 133, 244, 0.08)); border: 2px solid rgba(50, 215, 75, 0.45);">
            <div class="dag-step-header">
                <div class="dag-step-title">
                    <span style="font-size: 15px; font-weight: 700; color: var(--accent-green);">✓ Post-hook application response</span>
                    <span class="dag-step-id">${escapeHtml((selected.final_response_source || 'post-hook application output').replaceAll('_', ' '))}</span>
                </div>
            </div>
            <div class="dag-step-body">
                <div style="font-size: 11px; color: var(--text-muted); line-height: 1.5; margin-bottom: 10px;">
                    This terminal response was captured after application AfterSynthesis hooks. ${comparisonKnown ? 'The LLM Calls tab preserves the recorded synthesis output from before those hooks.' : 'The pre-hook synthesis output was not recorded; comparison is unavailable.'}
                </div>
                ${governedDiffers ? `<div style="margin-bottom: 10px; color: var(--accent-orange); font-size: 11px; font-weight: 600;">${isStreamingSynthesis ? 'The stored post-hook response differs from the recorded streaming synthesis output.' : 'The application changed the pre-hook synthesis output.'}</div>` : ''}
                <pre style="white-space: pre-wrap; overflow-wrap: anywhere; max-height: 320px; overflow: auto; padding: 12px; background: rgba(0,0,0,0.24); border-radius: 8px; color: var(--text-primary);">${escapeHtml(governedResponse)}</pre>
            </div>
        </div>`;
}

function renderLLMCalls(container) {
    const allInteractions = selected?.llm_interactions || [];
    if (allInteractions.length === 0) {
        container.innerHTML = `
            <div style="overflow-y: auto; height: 100%; padding: 16px;">
                <div class="empty-detail">
                    <div class="empty-detail-icon">🤖</div>
                    <div>${selected.section_warnings?.length ? 'LLM evidence could not be confirmed for this read.' : 'No LLM calls recorded for this execution'}</div>
                </div>
            </div>`;
        return;
    }

    // LLM Calls tab shows only orchestration-level calls (planning, synthesis,
    // tiered selection, etc.). Hook interactions route to the Pre-Execution /
    // Post-Execution tabs. classifyInteractions handles both new agents
    // (hook_phase-aware) and legacy agents (type-string fallback) uniformly.
    const { isOrchestrationCall } = classifyInteractions(allInteractions);
    const interactions = allInteractions.filter(isOrchestrationCall);

    if (interactions.length === 0) {
        container.innerHTML = `
            <div style="overflow-y: auto; height: 100%; padding: 16px;">
                <div class="empty-detail">
                    <div class="empty-detail-icon">🤖</div>
                    <div>No orchestration LLM calls for this execution<br><span style="font-size: 12px; color: var(--text-muted);">Hook calls are shown in the Pre-Execution and Post-Execution tabs</span></div>
                </div>
            </div>`;
        return;
    }

    // Disambiguation is handled globally by annotateCallLabels (called
    // once per record load in selectExecution). Interactions carry their
    // disambiguated label on `_callLabel`, so popup and card always
    // agree — renderSingleLLMCard's default reads the same field.

    // Recompute totals from the filtered orchestration interactions so the
    // header numbers match what's rendered below. Backend `llm_debug_summary`
    // aggregates across all interactions (including hooks) and would
    // contradict a header labelled "Orchestration LLM Activity".
    const orchTotalTokensIn = interactions.reduce((s, i) => s + (i.prompt_tokens || 0), 0);
    const orchTotalTokensOut = interactions.reduce((s, i) => s + (i.completion_tokens || 0), 0);
    const orchTotalDurationMs = interactions.reduce((s, i) => s + (i.duration_ms || 0), 0);
    const orchProviderBreakdown = {};
    for (const i of interactions) {
        if (i.provider) {
            orchProviderBreakdown[i.provider] = (orchProviderBreakdown[i.provider] || 0) + 1;
        }
    }
    const providerEntries = Object.entries(orchProviderBreakdown);

    container.innerHTML = `
        <div style="overflow-y: auto; height: 100%; padding: 16px;">
            <div class="dag-step-card" style="margin-bottom: 20px; background: linear-gradient(135deg, rgba(10, 132, 255, 0.2) 0%, rgba(10, 132, 255, 0.08) 100%); backdrop-filter: blur(20px) saturate(180%); -webkit-backdrop-filter: blur(20px) saturate(180%); border: 1px solid rgba(10, 132, 255, 0.3); box-shadow: 0 0 24px rgba(10, 132, 255, 0.2), 0 0 8px rgba(10, 132, 255, 0.4), 0 8px 32px rgba(0, 0, 0, 0.25), inset 0 1px 0 rgba(255, 255, 255, 0.12);">
                <div class="dag-step-header">
                    <div class="dag-step-title">
                        <span style="font-size: 16px; font-weight: 600; color: var(--accent-blue);">🤖 Orchestration LLM Activity</span>
                    </div>
                </div>
                <div class="dag-step-body">
                    <div class="dag-step-info" style="grid-template-columns: repeat(4, 1fr);">
                        <span class="dag-step-label">Total Calls</span>
                        <span class="dag-step-value highlight" style="font-size: 18px; font-weight: 700;">${interactions.length}</span>
                        <span class="dag-step-label">Input Tokens</span>
                        <span class="dag-step-value" style="color: var(--accent-teal);">${orchTotalTokensIn.toLocaleString()}</span>
                        <span class="dag-step-label">Output Tokens</span>
                        <span class="dag-step-value" style="color: var(--accent-purple);">${orchTotalTokensOut.toLocaleString()}</span>
                        <span class="dag-step-label">Aggregate Call Time</span>
                        <span class="dag-step-value">${formatDuration(orchTotalDurationMs)}</span>
                    </div>
                    ${providerEntries.length > 0 ? `
                        <div style="margin-top: 12px; display: flex; gap: 12px; flex-wrap: wrap;">
                            ${providerEntries.map(([provider, count]) => `
                                <span style="padding: 6px 14px; border-radius: 20px; font-size: 11px; font-weight: 600; background: linear-gradient(135deg, rgba(10, 132, 255, 0.25), rgba(10, 132, 255, 0.1)); border: 1px solid rgba(10, 132, 255, 0.3); color: var(--accent-blue);">
                                    ${escapeHtml(provider)}: ${count} call${count > 1 ? 's' : ''}
                                </span>
                            `).join('')}
                        </div>
                    ` : ''}
                </div>
            </div>

            <div style="margin-bottom: 16px; padding: 10px 14px; background: rgba(255, 255, 255, 0.03); border: 1px solid rgba(255, 255, 255, 0.06); border-radius: 8px; font-size: 11px; color: var(--text-muted); line-height: 1.5;">
                Orchestration-level LLM calls for this request, numbered in order.
                Memory hook operations (recall, extraction, reconciliation) are shown in the
                <strong style="color: var(--accent-blue); font-weight: 600;">Pre-Execution</strong> and
                <strong style="color: var(--accent-blue); font-weight: 600;">Post-Execution</strong> tabs.
            </div>

            ${interactions.slice(0, 10).map((interaction, idx) => {
                // origIdx: original index into selected.llm_interactions — used for
                //   DOM ids / deferred prompt+response lookup. Must match unfiltered order.
                // displayNum: 1-based position within the filtered list — what users see
                //   as "#N". Contiguous so gaps from filtered-out hook ops never appear.
                // displayLabel defaults via _callLabel annotation (see
                //   annotateCallLabels) so popup and card agree on disambiguation.
                const origIdx = selected.llm_interactions.indexOf(interaction);
                return renderSingleLLMCard(interaction, origIdx >= 0 ? origIdx : idx, idx + 1);
            }).join('')}
            ${interactions.length > 10 ? '<div id="llmLoadMoreSentinel" style="height: 1px;"></div>' : ''}
        </div>
    `;

    // Batch-load remaining cards as user scrolls (Phase 3a — deferred rendering).
    // First 10 rendered above; the rest are appended in batches of 10 via IntersectionObserver.
    if (interactions.length > 10) {
        let loadedCount = 10;
        const BATCH = 10;
        const sentinel = container.querySelector('#llmLoadMoreSentinel');
        const cardsContainer = sentinel?.parentElement;

        if (sentinel && cardsContainer) {
            const owner = selected;
            const ownerVersion = selectionVersion;
            let disposed = false;
            let observer = null;
            const renderThrough = end => {
                if (disposed || selected !== owner || selectionVersion !== ownerVersion || activeTab !== 'dag-llm' || end <= loadedCount) return;
                end = Math.min(end, interactions.length);
                // Re-use the same card rendering by building HTML for the next batch
                const batchHtml = interactions.slice(loadedCount, end).map((interaction, batchIdx) => {
                    const origIdx = selected.llm_interactions.indexOf(interaction);
                    const displayNum = loadedCount + batchIdx + 1;
                    return renderSingleLLMCard(interaction, origIdx >= 0 ? origIdx : loadedCount + batchIdx, displayNum);
                }).join('');
                sentinel.insertAdjacentHTML('beforebegin', batchHtml);
                loadedCount = end;
                if (loadedCount >= interactions.length) {
                    observer?.disconnect();
                    sentinel.remove();
                }
            };
            llmCardBatch = {
                reveal(originalIndex) {
                    const position = interactions.indexOf(owner.llm_interactions[originalIndex]);
                    if (position >= loadedCount) renderThrough(Math.ceil((position + 1) / BATCH) * BATCH);
                },
                dispose() { disposed = true; observer?.disconnect(); },
            };
            observer = new IntersectionObserver((entries) => {
                if (!entries.some(entry => entry.isIntersecting)) return;
                renderThrough(loadedCount + BATCH);
            }, { root: cardsContainer.closest('[style*="overflow"]') || null, threshold: 0 });
            observer.observe(sentinel);
        }
    }

    // Click handlers are bound once on dagDetailContent in bindDelegatedEvents()
    // — no per-render listener needed here.
}

// ---------------------------------------------------------------------------
// User Memory group popup
// ---------------------------------------------------------------------------

function showUserMemoryGroupPopup(node, nodeData) {
    document.querySelectorAll('.node-popup').forEach(p => p.remove());

    const steps = nodeData.steps || [];
    const isBeforePlanning = nodeData.pipelineStage === 'before_planning';
    const stage = isBeforePlanning ? 'BeforePlanning' : 'AfterSynthesis';
    const detailTab = isBeforePlanning ? 'dag-pre' : 'dag-post';
    const detailLabel = isBeforePlanning ? 'Pre-Execution' : 'Post-Execution';
    const popupRequestID = selected?.request_id;
    const popupSelectionVersion = selectionVersion;
    const hookTargets = (nodeData.pipelineHooks || []).map(hook => ({
        identity: pipelineHookIdentity(hook), tab: pipelineHookDetailTab(hook),
    }));
    const llmCount = steps.filter(s =>
        (s.category || 'llm') === 'llm'
    ).length;
    const failedCount = steps.filter(s => s.success === false).length;

    let html = `
        <div style="font-weight: 600; margin-bottom: 8px; color: #f0a030;">User Memory: ${stage}</div>
        <div style="font-size: 0.85em; color: var(--text-muted); margin-bottom: 12px;">
            ${steps.length} operations · ${llmCount} LLM calls<br>
            ${formatDuration(nodeData.totalDuration || 0)} aggregate operation time
        </div>
        ${failedCount ? `<div style="color: var(--accent-red); margin-bottom: 10px;">${failedCount} operation${failedCount === 1 ? '' : 's'} failed</div>` : ''}
    `;

    // Keep invocation outcome and pipeline consequence tied to stored hook
    // records. Full effects, decisions and operation payloads belong in the tab.
    (nodeData.pipelineHooks || []).forEach((hook, index) => {
        const targetLabel = hookTargets[index].tab === 'dag-pre' ? 'Pre-Execution'
            : hookTargets[index].tab === 'dag-post' ? 'Post-Execution' : 'Raw JSON';
        html += `
            <div class="dag-hook-popup-group-item">
                ${renderHookPopupSummary(pipelineHookNodeData(hook, ''))}
                <button type="button" class="dag-popup-detail-link" data-group-hook-details="${index}">View details in ${targetLabel} →</button>
            </div>`;
    });
    if (!hookTargets.length) html += `<button type="button" class="dag-popup-detail-link" data-memory-hook-details>View details in ${detailLabel} →</button>`;

    const popup = document.createElement('div');
    popup.className = 'node-popup';
    popup.style.cssText = `
        position: fixed;
        width: 340px; max-width: calc(100vw - 16px); max-height: calc(100vh - 16px);
        box-sizing: border-box; overflow-y: auto;
        background: linear-gradient(135deg, rgba(25, 25, 35, 0.98) 0%, rgba(15, 15, 22, 0.98) 100%);
        backdrop-filter: blur(24px) saturate(180%);
        -webkit-backdrop-filter: blur(24px) saturate(180%);
        border: 1px solid rgba(240, 160, 48, 0.3);
        border-radius: 16px;
        padding: 18px 20px;
        z-index: 10000;
        font-size: 13px;
        box-shadow: 0 20px 60px rgba(0, 0, 0, 0.5), 0 0 20px rgba(240, 160, 48, 0.15);
    `;
    popup.innerHTML = html;
    popup.querySelector('[data-memory-hook-details]')?.addEventListener('click', async () => {
        if (selected?.request_id !== popupRequestID || selectionVersion !== popupSelectionVersion) return;
        if (!await setDagDetailTab(detailTab) || selected?.request_id !== popupRequestID
            || selectionVersion !== popupSelectionVersion || activeTab !== detailTab) return;
        document.querySelector(`#dagDetailPanel [data-tab="${detailTab}"]`)?.focus();
    });
    popup.addEventListener('click', event => {
        const button = event.target.closest('[data-group-hook-details]');
        if (!button) return;
        const target = hookTargets[Number(button.dataset.groupHookDetails)];
        if (target) void openHookDetails(target, popupRequestID, popupSelectionVersion);
    });

    const dagContainerEl = document.getElementById('dagContainer');
    const pos = node.renderedPosition();
    const containerRect = dagContainerEl.getBoundingClientRect();

    document.body.appendChild(popup);
    const popupRect = popup.getBoundingClientRect();
    let popupLeft = Math.max(8, Math.min(containerRect.left + pos.x + 80, window.innerWidth - popupRect.width - 8));
    let popupTop = Math.max(8, Math.min(containerRect.top + pos.y - 80, window.innerHeight - popupRect.height - 8));

    popup.style.left = popupLeft + 'px';
    popup.style.top = popupTop + 'px';
}

// ---------------------------------------------------------------------------
// HITL Checkpoints tab
// ---------------------------------------------------------------------------

function toggleHITLSection(checkpointId, section) {
    const content = document.getElementById(`hitl-${section}-${checkpointId}`);
    const arrow = document.getElementById(`hitl-${section}-arrow-${checkpointId}`);
    if (content) {
        const isHidden = content.style.display === 'none';
        content.style.display = isHidden ? 'block' : 'none';
        if (arrow) arrow.textContent = isHidden ? '▼' : '▶';
    }
}

function renderHITLCheckpoints(container) {
    const lifecycle = selected?.hitl_lifecycle;
    if (!lifecycle && !selected?.hitl_checkpoints?.length) {
        container.innerHTML = `
            <div class="empty-detail">
                <div class="empty-detail-icon">🛑</div>
                <div>${selected.section_warnings?.length ? 'Current HITL evidence could not be confirmed for this read.' : 'No HITL checkpoints for this execution'}</div>
            </div>`;
        return;
    }

    const checkpoints = selected.hitl_checkpoints || [];
    const lifecyclePanel = lifecycle ? `
        <div class="dag-step-card" style="border-color: rgba(182, 133, 244, 0.45); margin-bottom: 16px;">
            <div class="dag-step-header">
                <div class="dag-step-title">
                    <span class="dag-step-number" style="color: #b685f4;">Lifecycle</span>
                    <span class="dag-step-id">${lifecycle.is_resume ? 'Resume execution' : 'Interruption execution'}</span>
                </div>
            </div>
            <div class="dag-step-body">
                <div class="dag-step-info">
                    <span class="dag-step-label">Checkpoint ID</span>
                    <span class="dag-step-value dag-mono">${escapeHtml(lifecycle.checkpoint_id || 'Unavailable')}</span>
                    <span class="dag-step-label">Initial request</span>
                    <span class="dag-step-value dag-mono">${escapeHtml(lifecycle.initial_request_id || 'Unavailable')}</span>
                    ${lifecycle.snapshot_status ? `<span class="dag-step-label">Stored snapshot</span><span class="dag-step-value">${escapeHtml(lifecycle.snapshot_status)}</span>` : ''}
                    <span class="dag-step-label">Current status</span>
                    <span class="dag-step-value" style="color: #b685f4;">${escapeHtml(lifecycle.current_status || 'Unavailable')}</span>
                    ${lifecycle.current_status_source ? `<span class="dag-step-label">Status source</span><span class="dag-step-value">${escapeHtml(lifecycle.current_status_source.replaceAll('_', ' '))}</span>` : ''}
                </div>
                ${lifecycle.snapshot_status && lifecycle.current_status && lifecycle.snapshot_status !== lifecycle.current_status ? `
                    <div style="margin-top: 12px; padding: 10px 12px; background: rgba(182, 133, 244, 0.08); border-left: 3px solid #b685f4; border-radius: 6px;">
                        The stored interruption remains <strong>${escapeHtml(lifecycle.snapshot_status)}</strong> as historical evidence. The live checkpoint is now <strong>${escapeHtml(lifecycle.current_status)}</strong>.
                    </div>
                ` : ''}
                ${(lifecycle.related_executions || []).length ? `
                    <div style="margin-top: 12px; display: flex; gap: 8px; flex-wrap: wrap;">
                        ${(lifecycle.related_executions || []).map(link => `<a href="#" data-hitl-execution="${escapeHtmlAttribute(link.request_id)}" class="dag-viz-meta-item" style="color: #b685f4;">Open ${escapeHtml(link.role)} execution ↗</a>`).join('')}
                    </div>
                ` : ''}
            </div>
        </div>` : '';

    container.innerHTML = `
        <div style="overflow-y: auto; height: 100%; padding: 16px;">
            ${lifecyclePanel}
            ${checkpoints.length === 0 ? '<div class="empty-detail">The lifecycle is known, but the current checkpoint payload is unavailable.</div>' : checkpoints.map((checkpoint, idx) => {
                const normalizedStatus = String(checkpoint.status || 'unknown').toLowerCase();
                const statusColors = {
                    'pending': 'rgba(255, 179, 64, 0.2)',
                    'approved': 'rgba(50, 215, 75, 0.2)',
                    'completed': 'rgba(50, 215, 75, 0.2)',
                    'rejected': 'rgba(255, 107, 107, 0.2)',
                    'aborted': 'rgba(255, 107, 107, 0.2)',
                    'expired': 'rgba(128, 128, 128, 0.2)'
                };
                const bgColor = statusColors[normalizedStatus] || 'rgba(255, 255, 255, 0.08)';
                const successfulStatus = normalizedStatus === 'approved' || normalizedStatus === 'completed';
                const failedStatus = normalizedStatus === 'rejected' || normalizedStatus === 'aborted';
                const statusClass = successfulStatus ? 'completed' : failedStatus ? 'failed' : 'pending';
                const statusIcon = successfulStatus ? '✓' : failedStatus ? '✗' : normalizedStatus === 'expired' ? '⊘' : '◐';
                const statusLabel = normalizedStatus === 'unknown'
                    ? 'Unknown'
                    : normalizedStatus.charAt(0).toUpperCase() + normalizedStatus.slice(1);
                const checkpointID = String(checkpoint.checkpoint_id || `checkpoint-${idx + 1}`);
                const checkpointIDAttr = escapeHtmlAttribute(checkpointID);

                return `
                    <div class="dag-step-card" style="background: linear-gradient(135deg, ${bgColor} 0%, rgba(255, 255, 255, 0.02) 100%);">
                        <div class="dag-step-header">
                            <div class="dag-step-title">
                                <span class="dag-step-number">#${idx + 1}</span>
                                <span class="dag-step-id">${escapeHtml(checkpoint.interrupt_point || 'Checkpoint')}</span>
                            </div>
                            <div style="display: flex; align-items: center; gap: 12px;">
                                <span class="dag-step-status ${statusClass}">
                                    ${statusIcon} ${escapeHtml(statusLabel)}
                                </span>
                            </div>
                        </div>
                        <div class="dag-step-body">
                            <div class="dag-step-info">
                                <span class="dag-step-label">Checkpoint ID</span>
                                <span class="dag-step-value" style="font-family: 'SF Mono', monospace; font-size: 11px;">${escapeHtml(checkpointID)}</span>
                                ${checkpoint.agent_name ? `
                                    <span class="dag-step-label">Agent</span>
                                    <span class="dag-step-value highlight">${escapeHtml(checkpoint.agent_name)}</span>
                                ` : ''}
                                <span class="dag-step-label">Created</span>
                                <span class="dag-step-value">${new Date(checkpoint.created_at).toLocaleString()}</span>
                                <span class="dag-step-label">Expires</span>
                                <span class="dag-step-value">${new Date(checkpoint.expires_at).toLocaleString()}</span>
                            </div>
                            ${checkpoint.decision ? `
                                <div class="hitl-context-panel hitl-context-panel-decision">
                                    <div class="hitl-context-title">Decision Context</div>
                                    <div class="dag-step-info hitl-context-grid">
                                        <span class="dag-step-label">Reason</span>
                                        <span class="dag-step-value">${escapeHtml(checkpoint.decision.reason || 'N/A')}</span>
                                        <span class="dag-step-label">Priority</span>
                                        <span class="dag-step-value">${escapeHtml(checkpoint.decision.priority || 'N/A')}</span>
                                        ${checkpoint.decision.message ? `
                                            <span class="dag-step-label">Message</span>
                                            <span class="dag-step-value">${escapeHtml(checkpoint.decision.message)}</span>
                                        ` : ''}
                                    </div>
                                </div>
                            ` : ''}
                            ${checkpoint.current_step ? `
                                <div class="hitl-context-panel hitl-context-panel-step">
                                    <div class="hitl-context-title">Current Step</div>
                                    <div class="dag-step-info hitl-context-grid">
                                        <span class="dag-step-label">Step ID</span>
                                        <span class="dag-step-value">${escapeHtml(checkpoint.current_step.step_id || 'N/A')}</span>
                                        <span class="dag-step-label">Capability</span>
                                        <span class="dag-step-value highlight">${escapeHtml(checkpoint.current_step.capability || 'N/A')}</span>
                                    </div>
                                </div>
                            ` : ''}
                            ${checkpoint.resolved_parameters ? `
                                <div class="dag-step-response">
                                    <div class="dag-step-response-header" data-toggle-hitl="${checkpointIDAttr}" data-hitl-section="params">
                                        <span><span class="expand-arrow" id="hitl-params-arrow-${checkpointIDAttr}">▶</span> Resolved Parameters</span>
                                    </div>
                                    <div id="hitl-params-${checkpointIDAttr}" class="dag-step-response-content" style="display: none;">
                                        ${formatResponseJson(checkpoint.resolved_parameters)}
                                    </div>
                                </div>
                            ` : ''}
                            ${checkpoint.plan ? `
                                <div class="dag-step-response">
                                    <div class="dag-step-response-header" data-toggle-hitl="${checkpointIDAttr}" data-hitl-section="plan">
                                        <span><span class="expand-arrow" id="hitl-plan-arrow-${checkpointIDAttr}">▶</span> Execution Plan (${checkpoint.plan.steps?.length || 0} steps)</span>
                                    </div>
                                    <div id="hitl-plan-${checkpointIDAttr}" class="dag-step-response-content" style="display: none;">
                                        ${formatResponseJson(checkpoint.plan)}
                                    </div>
                                </div>
                            ` : ''}
                        </div>
                    </div>
                `;
            }).join('')}
        </div>
    `;

    // Click handlers are bound once on dagDetailContent in bindDelegatedEvents()
    // — no per-render listener needed here.
}
