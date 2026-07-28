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
    formatResponseJson,
    copyToClipboard,
    formatConversationRequest,
} from '../utils/format.js';
import { fetchAPI } from '../api.js';
import { showLoading, hideLoading } from '../utils/dom.js';
import {
    getLLMType,
    isPlanningType,
    isPhaseClosingType,
    getPhaseNumber,
} from '../llm-types.js';

// ---------------------------------------------------------------------------
// Module-level state
// ---------------------------------------------------------------------------
let executions = [];
let selected = null;
let activeTab = 'dag-viz';
let filter = 'all';
let cyInstance = null;
let viewMode = 'full';
let sortColumn = 'created_at';
let sortDirection = 'desc';

const DAG_GROUP_PAGE_SIZE = 50;
const DAG_GROUPING_STORAGE_KEY = 'truvag3.dag.groupingMode';

let groupingMode = readGroupingPreference();
let groupedExecutionGroups = [];
let groupedNextCursor = '';
let groupedQueryFingerprint = '';
let groupedResponsePartial = false;
let groupedRequestVersion = 0;
let groupedLoading = false;
let expandedGroupKeys = new Set();
let pendingAutoExpandRequestID = '';
let conversationCache = new Map();
let activeConversationID = '';
let listPanelMode = 'list';
let timelineReturnFocus = null;
let dagSearchTimer = null;

// LRU cache for Cytoscape instances — avoids rebuild on tab switch.
// Key: "requestId:viewMode", Value: { cy, container (child div), requestId }
const dagCache = new Map();

function readGroupingPreference() {
    try {
        return sessionStorage.getItem(DAG_GROUPING_STORAGE_KEY) === 'flat'
            ? 'flat'
            : 'grouped';
    } catch (_) {
        return 'grouped';
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

// ---------------------------------------------------------------------------
// LLM call identification primitives (ORCH-021 traceability)
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
// No DOM ids, no click-to-scroll — identifier-only per the agreed scope.
// If linking is ever added later, the label is a ready-made stable string.
function callLabel(i) {
    if (!i || !i.type) return '';
    const parts = [i.type];
    if (i.phase_number) parts.push(`P${i.phase_number}`);
    if (i.step_id) parts.push(i.step_id);
    if (i.attempt && i.attempt > 1) parts.push(`retry-${i.attempt}`);
    if (i.hook_phase && !i.phase_number) parts.push(i.hook_phase);
    return parts.join(' · ');
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

export function refresh() {
    fetchExecutions({ preserveExpansion: true });
    if (listPanelMode === 'timeline' && activeConversationID) {
        void loadConversationTimeline(activeConversationID, selected?.request_id, { force: true });
    }
}

export function destroy() {
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
    groupedRequestVersion++;
    groupedExecutionGroups = [];
    groupedNextCursor = '';
    groupedQueryFingerprint = '';
    groupedResponsePartial = false;
    groupedLoading = false;
    expandedGroupKeys.clear();
    pendingAutoExpandRequestID = '';
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
    const loadMore = e.target.closest('[data-load-more-groups]');
    if (loadMore) {
        if (!groupedLoading) void loadExecutionGroups({ reset: false });
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
function handleDagDetailContentClick(e) {
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
    // ORCH-022 Layer 4: navigate to the completed resume sibling when the
    // banner link is clicked. Reuses the same selection path the list click
    // uses so load/cache behavior is identical.
    const resumeSibling = e.target.closest('[data-resume-sibling]');
    if (resumeSibling) {
        e.preventDefault();
        const targetID = resumeSibling.dataset.resumeSibling;
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
        if (groupingMode === 'grouped') {
            void loadExecutionGroups({ reset: true });
        } else {
            renderExecutionList();
        }
    }, 250);
}

function bindDelegatedEvents() {
    addTrackedListener(document.getElementById('dagTableBody'), 'click', handleDagTableClick);
    addTrackedListener(document.getElementById('dagListPanel'), 'click', handleDagListPanelClick);
    addTrackedListener(document.getElementById('dagListPanel'), 'keydown', handleDagListPanelKeydown);
    addTrackedListener(document.getElementById('dagDetailPanel'), 'click', handleDagDetailPanelClick);
    addTrackedListener(document.getElementById('dagDetailContent'), 'click', handleDagDetailContentClick);
    addTrackedListener(document.getElementById('dagSearchInput'), 'input', handleDagSearchInput);
}

// ---------------------------------------------------------------------------
// Data fetching
// ---------------------------------------------------------------------------

async function fetchExecutions({ preserveExpansion = false } = {}) {
    if (groupingMode === 'grouped') {
        await loadExecutionGroups({
            reset: true,
            preserveExpansion,
            withLoadingOverlay: listPanelMode === 'list',
        });
        return;
    }
    await loadFlatExecutions({ withLoadingOverlay: listPanelMode === 'list' });
}

async function loadFlatExecutions({ withLoadingOverlay = true } = {}) {
    const listPanel = document.getElementById('dagListPanel');
    const requestVersion = ++groupedRequestVersion;
    if (withLoadingOverlay) showLoading(listPanel, 'Loading executions...');
    try {
        const { data } = await fetchAPI('/api/executions?limit=50');
        if (requestVersion !== groupedRequestVersion || groupingMode !== 'flat') return;
        executions = data.executions || [];
        setDagListDiagnostic('');
        renderExecutionList();
        updateDagStats();
        updateDagLastUpdated(data.timestamp);
    } catch (error) {
        if (requestVersion !== groupedRequestVersion) return;
        console.error('Failed to fetch executions:', error);
        setDagListDiagnostic(
            'The execution list could not be loaded. Refresh to try again.',
            'error',
        );
    } finally {
        if (requestVersion === groupedRequestVersion && withLoadingOverlay) {
            hideLoading(listPanel);
        }
    }
}

async function loadExecutionGroups({
    reset,
    withLoadingOverlay = true,
    preserveExpansion = false,
}) {
    const listPanel = document.getElementById('dagListPanel');
    const requestVersion = ++groupedRequestVersion;
    if (reset && !preserveExpansion) {
        requestSelectedGroupAutoExpansion();
    }
    groupedLoading = true;
    updateGroupedLoadMoreControl();
    if (withLoadingOverlay && reset) showLoading(listPanel, 'Loading conversation groups...');

    const query = new URLSearchParams({
        group_conversations: 'true',
        q: document.getElementById('dagSearchInput')?.value.trim() || '',
        status: filter,
        sort: sortColumn,
        direction: sortDirection,
        limit: String(DAG_GROUP_PAGE_SIZE),
    });
    if (!reset && groupedNextCursor) query.set('cursor', groupedNextCursor);

    try {
        const { data } = await fetchAPI(`/api/executions?${query.toString()}`);
        if (requestVersion !== groupedRequestVersion || groupingMode !== 'grouped') return;

        if (!Array.isArray(data.groups)) {
            setDagListDiagnostic(
                'This Registry Viewer server does not support conversation grouping, so the flat list is shown.',
                'warning',
                false,
            );
            groupedExecutionGroups = [];
            groupedNextCursor = '';
            pendingAutoExpandRequestID = '';
            groupingMode = 'flat';
            updateGroupingModeControls();
            executions = Array.isArray(data.executions) ? data.executions : [];
            renderExecutionList();
            updateDagStats(executions);
            updateDagLastUpdated(data.timestamp);
            return;
        }

        if (reset) {
            groupedExecutionGroups = data.groups;
            if (!preserveExpansion) {
                expandedGroupKeys.clear();
            }
        } else if (
            !groupedQueryFingerprint ||
            groupedQueryFingerprint === data.query_fingerprint
        ) {
            groupedExecutionGroups = groupedExecutionGroups.concat(data.groups);
        } else {
            throw new Error('grouped response fingerprint changed during pagination');
        }

        groupedQueryFingerprint = data.query_fingerprint || '';
        groupedNextCursor = data.next_cursor || '';
        groupedResponsePartial = Boolean(data.partial);
        applyPendingSelectedGroupAutoExpansion();
        renderExecutionList();

        const materialized = flattenGroupedExecutions(groupedExecutionGroups);
        executions = materialized;
        updateDagStats(materialized);
        updateDagLastUpdated(data.timestamp);
        updateGroupedDiagnostics(data);
    } catch (error) {
        if (requestVersion !== groupedRequestVersion) return;
        console.error('Failed to fetch conversation groups:', error);
        if (reset && !preserveExpansion) {
            groupedExecutionGroups = [];
            groupedNextCursor = '';
            renderServerExecutionGroups([]);
        }
        setDagListDiagnostic(
            'Conversation grouping is temporarily unavailable. The flat execution list remains available.',
            'error',
            true,
        );
    } finally {
        if (requestVersion === groupedRequestVersion) {
            groupedLoading = false;
            updateGroupedLoadMoreControl();
            if (withLoadingOverlay && reset) hideLoading(listPanel);
        }
    }
}

function updateGroupedLoadMoreControl() {
    const loadMore = document.getElementById('dagLoadMore');
    if (!loadMore) return;
    loadMore.disabled =
        groupedLoading ||
        !groupedNextCursor ||
        groupedResponsePartial;
    loadMore.textContent = groupedLoading ? 'Loading…' : 'Load more groups';
}

function updateDagStats(sourceExecutions = executions) {
    const unique = new Map();
    sourceExecutions.forEach(execution => {
        if (execution?.request_id) unique.set(execution.request_id, execution);
    });
    const values = Array.from(unique.values());
    const total = values.length;
    const successful = values.filter(e => e.success).length;
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
        messages.push('LLM duration and call counts are incomplete because DB 7 could not be fully read.');
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
    if (groupingMode === 'grouped') {
        void loadExecutionGroups({ reset: true });
    } else {
        renderExecutionList();
    }
}

function sortDagTable(column) {
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

    if (groupingMode === 'grouped') {
        void loadExecutionGroups({ reset: true });
    } else {
        renderExecutionList();
    }
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
    storeGroupingPreference(mode);
    groupedRequestVersion++;
    groupedLoading = false;
    groupedExecutionGroups = [];
    groupedNextCursor = '';
    groupedQueryFingerprint = '';
    groupedResponsePartial = false;
    expandedGroupKeys.clear();
    pendingAutoExpandRequestID = mode === 'grouped'
        ? (selected?.request_id || '')
        : '';
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
            ? 'Conversations stay together · sorted by latest matching turn'
            : 'One row per execution · legacy list behavior';
    }
    const loadMore = document.getElementById('dagLoadMore');
    if (loadMore && groupingMode === 'flat') loadMore.classList.add('hidden');
}

function showExecutionListMode() {
    listPanelMode = 'list';
    document.getElementById('dagListMode')?.classList.remove('hidden');
    document.getElementById('dagConversationMode')?.classList.add('hidden');
}

function toggleConversationGroup(groupKey, triggerKind = 'expand') {
    if (!groupKey) return;
    if (expandedGroupKeys.has(groupKey)) {
        expandedGroupKeys.delete(groupKey);
    } else {
        expandedGroupKeys.add(groupKey);
    }
    renderServerExecutionGroups(groupedExecutionGroups);
    requestAnimationFrame(() => {
        const trigger = Array.from(document.querySelectorAll(
            '[data-toggle-conversation-group]',
        )).find(button =>
            button.dataset.toggleConversationGroup === groupKey &&
            (triggerKind === 'title'
                ? button.classList.contains('dag-group-title-btn')
                : button.classList.contains('dag-group-expand-btn'))
        );
        trigger?.focus({ preventScroll: true });
    });
}

function groupContainsRequest(group, requestID) {
    if (!requestID) return false;
    return (group.turns || []).some(turn =>
        turn.execution?.request_id === requestID ||
        (turn.related_executions || []).some(item => item.request_id === requestID)
    ) || (group.orphans || []).some(item => item.request_id === requestID);
}

function requestSelectedGroupAutoExpansion() {
    pendingAutoExpandRequestID = selected?.request_id || '';
}

function applyPendingSelectedGroupAutoExpansion() {
    if (!pendingAutoExpandRequestID) return;
    const group = groupedExecutionGroups.find(candidate =>
        groupContainsRequest(candidate, pendingAutoExpandRequestID)
    );
    if (!group) return;

    // The selected record is now represented by the current server page, so
    // this one-shot request has been consumed. Standalone units have nothing
    // to expand; conversation units open once and can then be collapsed by
    // the user without a later render overriding that choice.
    pendingAutoExpandRequestID = '';
    if (group.conversation_id) {
        expandedGroupKeys.add(group.group_key);
    }
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
                    rows.push(renderGroupedExecutionRow(turn.execution, {
                        llmCallCount: turn.llm_call_count,
                        historyStatus: turn.history_status,
                    }));
                    (turn.related_executions || []).forEach(related => {
                        rows.push(renderGroupedExecutionRow(related, {
                            related: true,
                            relationLabel: 'Related',
                        }));
                    });
                });
                (group.orphans || []).forEach(orphan => {
                    rows.push(renderGroupedExecutionRow(orphan, {
                        related: true,
                        relationLabel: 'Related record',
                    }));
                });
                return;
            }

            const expanded = expandedGroupKeys.has(group.group_key);
            rows.push(renderConversationGroupHeader(group, expanded));
            if (!expanded) return;

            (group.turns || []).forEach(turn => {
                rows.push(renderGroupedExecutionRow(turn.execution, {
                    conversationTurn: true,
                    llmCallCount: turn.llm_call_count,
                    historyStatus: turn.history_status,
                }));
                (turn.related_executions || []).forEach(related => {
                    rows.push(renderGroupedExecutionRow(related, {
                        related: true,
                        relationLabel: 'Related execution',
                    }));
                });
            });
            (group.orphans || []).forEach(orphan => {
                rows.push(renderGroupedExecutionRow(orphan, {
                    related: true,
                    relationLabel: 'Unattached related record',
                    orphan: true,
                }));
            });
        });
        container.innerHTML = rows.join('');
    }

    const loadMore = document.getElementById('dagLoadMore');
    if (loadMore) {
        loadMore.classList.toggle(
            'hidden',
            !groupedNextCursor || groupedResponsePartial,
        );
        updateGroupedLoadMoreControl();
    }
}

function renderConversationGroupHeader(group, expanded) {
    const turns = group.turns || [];
    const anchor = turns[0]?.execution || group.orphans?.[0] || {};
    const executionCount = turns.reduce(
        (count, turn) => count + 1 + (turn.related_executions || []).length,
        (group.orphans || []).length,
    );
    const visibleExecutions = flattenGroupedExecutions([group]);
    const totalDuration =
        (anchor.total_duration_ms || 0) +
        (anchor.llm_total_duration_ms || 0);
    const successful = visibleExecutions.every(item => item.success);
    const interrupted = visibleExecutions.some(item => item.interrupted);
    const statusClass = interrupted ? 'interrupted' : (successful ? 'success' : 'error');
    const statusLabel = interrupted ? '⏸ Interrupted' : (successful ? '✓ Success' : '⚠ Mixed');
    const shortConversationID = truncateText(group.conversation_id, 28);
    const matchingTurnCount = group.matching_turn_count ?? turns.length;
    const totalTurnCount = group.total_turn_count || turns.length;
    const turnCountLabel = matchingTurnCount === totalTurnCount
        ? `${totalTurnCount} turn${totalTurnCount === 1 ? '' : 's'}`
        : `${matchingTurnCount} matching · ${totalTurnCount} total`;

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
                    <span class="dag-group-title">${escapeHtml(truncateText(anchor.original_request || 'Untitled conversation', 62))}</span>
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
        relationLabel = '',
        orphan = false,
        llmCallCount = 0,
        historyStatus = '',
    } = options;
    const isSelected = selected?.request_id === exec.request_id;
    const rowClasses = [
        isSelected ? 'selected' : '',
        conversationTurn ? 'dag-conversation-turn-row' : '',
        related ? 'child-row dag-related-execution-row' : '',
        orphan ? 'dag-orphan-execution-row' : '',
    ].filter(Boolean).join(' ');
    const requestID = exec.request_id || '';
    const originalRequestID = exec.original_request_id || requestID;
    const statusClass = exec.interrupted ? 'interrupted' : (exec.success ? 'success' : 'error');
    const statusLabel = exec.interrupted ? '⏸ Interrupted' : (exec.success ? '✓ Success' : '✗ Failed');
    const duration = (exec.total_duration_ms || 0) + (exec.llm_total_duration_ms || 0);

    return `
        <tr class="${rowClasses}"
            data-request-id="${escapeHtmlAttribute(requestID)}"
            data-original-request-id="${escapeHtmlAttribute(originalRequestID)}">
            <td>
                <span class="status-badge ${statusClass}">${statusLabel}</span>
            </td>
            <td>
                <div class="dag-execution-title-row">
                    ${related ? '<span class="child-indicator">↳</span>' : (conversationTurn ? '<span class="dag-turn-dot" aria-hidden="true"></span>' : '')}
                    <div class="dag-execution-title">
                        ${relationLabel ? `<span class="dag-relation-label">${escapeHtml(relationLabel)}</span>` : ''}
                        <div class="service-name">${escapeHtml(truncateText(exec.original_request || 'Unknown request', related ? 48 : 58))}</div>
                        <span class="request-id">${escapeHtml(requestID)}</span>
                        ${exec.interrupted ? '<span class="hitl-resume-badge">HITL Interrupted</span>' : ''}
                        ${exec.metadata?.resume_checkpoint_id ? '<span class="hitl-resume-badge dag-hitl-resume">HITL Resume</span>' : ''}
                        ${llmCallCount ? `<span class="dag-row-meta">${llmCallCount} LLM call${llmCallCount === 1 ? '' : 's'}</span>` : ''}
                        ${historyStatus ? `<span class="dag-row-meta">${escapeHtml(historyStatus)}</span>` : ''}
                    </div>
                </div>
            </td>
            <td>
                <div class="step-progress">
                    <span>${exec.step_count || 0} steps</span>
                    ${exec.failed_steps > 0 ? `<span class="dag-failed-steps">(${exec.failed_steps} failed)</span>` : ''}
                </div>
            </td>
            <td><span class="dag-mono">${formatDuration(duration)}</span></td>
            <td><span class="time-ago">${formatTimeAgo(exec.created_at)}</span></td>
        </tr>`;
}

function renderLegacyExecutionList() {
    const container = document.getElementById('dagTableBody');
    const searchInput = document.getElementById('dagSearchInput');
    const searchTerm = searchInput ? searchInput.value.toLowerCase() : '';

    let filtered = executions.filter(exec => {
        if (filter === 'success' && (!exec.success || exec.interrupted)) return false;
        if (filter === 'failed' && (exec.success || exec.interrupted)) return false;
        if (filter === 'interrupted' && !exec.interrupted) return false;
        if (searchTerm && !exec.original_request.toLowerCase().includes(searchTerm) &&
            !exec.request_id.toLowerCase().includes(searchTerm)) return false;
        return true;
    });

    // Sort the filtered list
    filtered.sort((a, b) => {
        let valA, valB;
        switch (sortColumn) {
            case 'original_request':
                valA = (a.original_request || '').toLowerCase();
                valB = (b.original_request || '').toLowerCase();
                break;
            case 'total_duration_ms':
                valA = (a.total_duration_ms || 0) + (a.llm_total_duration_ms || 0);
                valB = (b.total_duration_ms || 0) + (b.llm_total_duration_ms || 0);
                break;
            case 'created_at':
            default:
                valA = new Date(a.created_at).getTime();
                valB = new Date(b.created_at).getTime();
                break;
        }
        if (valA < valB) return sortDirection === 'asc' ? -1 : 1;
        if (valA > valB) return sortDirection === 'asc' ? 1 : -1;
        return 0;
    });

    if (filtered.length === 0) {
        container.innerHTML = `
            <tr>
                <td colspan="5" style="text-align: center; padding: 40px;">
                    <div class="empty-state-icon" style="font-size: 40px; margin-bottom: 12px;">🔀</div>
                    <div style="color: var(--text-muted);">No executions found</div>
                </td>
            </tr>`;
        return;
    }

    // Group executions by original_request_id for parent-child display.
    // Parent: where request_id === original_request_id or no original_request_id
    // Child: where original_request_id exists and differs from request_id
    // isHITL distinguishes HITL resumes (parent was interrupted) from agent delegation children.
    const groups = new Map(); // key: original_request_id, value: { parent, children, isHITL }

    filtered.forEach(exec => {
        const groupKey = exec.original_request_id || exec.request_id;
        const isParent = !exec.original_request_id || exec.request_id === exec.original_request_id;

        if (!groups.has(groupKey)) {
            groups.set(groupKey, { parent: null, children: [], isHITL: false });
        }

        const group = groups.get(groupKey);
        if (isParent) {
            group.parent = exec;
        } else {
            group.children.push(exec);
        }
    });

    // Determine HITL status per group: a group is HITL only if the parent
    // or any member was interrupted (has a checkpoint). Agent delegation
    // children inherit original_request_id via OTel baggage but are never interrupted.
    groups.forEach(group => {
        if (group.parent?.interrupted || group.children.some(c => c.interrupted)) {
            group.isHITL = true;
        }
    });

    // Sort children within each group by created_at (oldest first, so conversation flows naturally)
    groups.forEach(group => {
        group.children.sort((a, b) => new Date(a.created_at) - new Date(b.created_at));
    });

    // Convert groups to ordered array (sorted by current sort column/direction)
    const orderedGroups = Array.from(groups.values())
        .filter(g => g.parent) // Only groups with a parent
        .sort((a, b) => {
            let valA, valB;
            switch (sortColumn) {
                case 'original_request':
                    valA = (a.parent.original_request || '').toLowerCase();
                    valB = (b.parent.original_request || '').toLowerCase();
                    break;
                case 'total_duration_ms':
                    valA = (a.parent.total_duration_ms || 0) + (a.parent.llm_total_duration_ms || 0);
                    valB = (b.parent.total_duration_ms || 0) + (b.parent.llm_total_duration_ms || 0);
                    break;
                case 'created_at':
                default:
                    valA = new Date(a.parent.created_at).getTime();
                    valB = new Date(b.parent.created_at).getTime();
                    break;
            }
            if (valA < valB) return sortDirection === 'asc' ? -1 : 1;
            if (valA > valB) return sortDirection === 'asc' ? 1 : -1;
            return 0;
        });

    // Handle orphan children (where parent wasn't in filtered results)
    groups.forEach((group, groupKey) => {
        if (!group.parent && group.children.length > 0) {
            // Promote first child as pseudo-parent for display
            group.parent = group.children.shift();
            orderedGroups.push(group);
        }
    });

    // Re-sort after adding orphans to maintain correct order
    orderedGroups.sort((a, b) => {
        let valA, valB;
        switch (sortColumn) {
            case 'original_request':
                valA = (a.parent.original_request || '').toLowerCase();
                valB = (b.parent.original_request || '').toLowerCase();
                break;
            case 'total_duration_ms':
                valA = (a.parent.total_duration_ms || 0) + (a.parent.llm_total_duration_ms || 0);
                valB = (b.parent.total_duration_ms || 0) + (b.parent.llm_total_duration_ms || 0);
                break;
            case 'created_at':
            default:
                valA = new Date(a.parent.created_at).getTime();
                valB = new Date(b.parent.created_at).getTime();
                break;
        }
        if (valA < valB) return sortDirection === 'asc' ? -1 : 1;
        if (valA > valB) return sortDirection === 'asc' ? 1 : -1;
        return 0;
    });

    // Helper to render a single execution row
    const renderRow = (exec, isChild = false, hasChildren = false, isHITL = false) => {
        const isSelected = selected?.request_id === exec.request_id;
        const childClass = isChild ? 'child-row' : '';
        const expandableClass = hasChildren ? 'expandable' : '';

        return `
        <tr class="${isSelected ? 'selected' : ''} ${childClass} ${expandableClass}"
            data-request-id="${exec.request_id}"
            data-original-request-id="${exec.original_request_id || exec.request_id}">
            <td>
                <span class="status-badge ${exec.interrupted ? 'interrupted' : (exec.success ? 'success' : 'error')}">
                    ${exec.interrupted ? '⏸ Interrupted' : (exec.success ? '✓ Success' : '✗ Failed')}
                </span>
            </td>
            <td>
                <div style="display: flex; align-items: flex-start;">
                    ${isChild ? '<span class="child-indicator">↳</span>' : ''}
                    <div style="flex: 1;">
                        <div class="service-name" style="margin-bottom: 4px;">
                            ${truncateText(exec.original_request, isChild ? 50 : 60)}
                        </div>
                        <span class="request-id">${exec.request_id}</span>
                        ${exec.interrupted ? '<span class="hitl-resume-badge">HITL Interrupted</span>' : ''}
                        ${exec.metadata?.resume_checkpoint_id ? '<span class="hitl-resume-badge" style="background: rgba(46, 204, 113, 0.15); color: #2ecc71;">HITL Resume</span>' : ''}
                        ${isChild && !exec.interrupted && !exec.metadata?.resume_checkpoint_id ? '<span class="hitl-resume-badge" style="background: rgba(218, 143, 255, 0.15); color: var(--accent-purple);">Delegated</span>' : ''}
                    </div>
                </div>
            </td>
            <td>
                <div class="step-progress">
                    <span>${exec.step_count} steps</span>
                    ${exec.failed_steps > 0 ? `
                        <span style="color: var(--accent-red); font-size: 11px;">(${exec.failed_steps} failed)</span>
                    ` : ''}
                </div>
            </td>
            <td>
                <span style="font-family: 'SF Mono', monospace; font-size: 12px;">
                    ${formatDuration((exec.total_duration_ms || 0) + (exec.llm_total_duration_ms || 0))}
                </span>
            </td>
            <td>
                <span class="time-ago">${formatTimeAgo(exec.created_at)}</span>
            </td>
        </tr>`;
    };

    // Build HTML: parent followed by its children
    const rows = [];
    orderedGroups.forEach(group => {
        const hasChildren = group.children.length > 0;
        rows.push(renderRow(group.parent, false, hasChildren, group.isHITL));
        group.children.forEach(child => {
            rows.push(renderRow(child, true, false, group.isHITL));
        });
    });

    container.innerHTML = rows.join('');

    // Update last updated timestamp
    document.getElementById('dagLastUpdated').textContent = `Last updated: ${new Date().toLocaleTimeString()}`;
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

async function loadConversationTimeline(
    conversationID,
    selectedRequestID,
    { force = false } = {},
) {
    if (!conversationID) return null;
    const cached = conversationCache.get(conversationID);
    if (!force && cached?.data) {
        if (activeConversationID === conversationID && listPanelMode === 'timeline') {
            renderConversationTimeline(cached.data, selectedRequestID);
        }
        return cached.data;
    }
    if (!force && cached?.promise) return cached.promise;

    const endpoint = `/api/conversations?${new URLSearchParams({
        conversation_id: conversationID,
    }).toString()}`;
    const promise = fetchAPI(endpoint)
        .then(({ data }) => {
            conversationCache.set(conversationID, { data });
            if (activeConversationID === conversationID && listPanelMode === 'timeline') {
                renderConversationTimeline(
                    data,
                    selected?.request_id || selectedRequestID,
                );
            }
            return data;
        })
        .catch(error => {
            console.error('Failed to fetch conversation timeline:', error);
            if (cached?.data) {
                conversationCache.set(conversationID, { data: cached.data });
                if (activeConversationID === conversationID && listPanelMode === 'timeline') {
                    renderConversationTimeline(cached.data, selected?.request_id);
                    appendTimelineDiagnostic('The latest refresh failed; showing the cached timeline.');
                }
                return cached.data;
            }
            conversationCache.set(conversationID, { error });
            if (activeConversationID === conversationID && listPanelMode === 'timeline') {
                renderTimelineError();
            }
            return null;
        });
    conversationCache.set(conversationID, { promise });
    return promise;
}

function renderConversationTimeline(timeline, selectedRequestID = selected?.request_id) {
    if (!timeline || activeConversationID !== timeline.conversation_id) return;
    const turns = timeline.turns || [];
    const turnCount = timeline.turn_count || turns.length;
    const executionCount = timeline.execution_count || 0;
    const idTarget = document.getElementById('dagTimelineConversationID');
    if (idTarget) idTarget.textContent = timeline.conversation_id;
    const summary = document.getElementById('dagTimelineSummary');
    if (summary) {
        summary.textContent = `${turnCount} turn${turnCount === 1 ? '' : 's'} · ${executionCount} execution${executionCount === 1 ? '' : 's'} · chronological`;
    }

    const diagnosticMessages = [];
    if (timeline.index_incomplete) {
        diagnosticMessages.push('The reverse index is incomplete; verified DB 8 records are still shown.');
    }
    if (timeline.llm_enrichment_incomplete) {
        diagnosticMessages.push('LLM call counts and history status are incomplete.');
    }
    if (timeline.partial) {
        diagnosticMessages.push('The timeline reached a safety bound and may omit records.');
    }
    setTimelineDiagnostic(diagnosticMessages.join(' '));

    const currentTurnIndex = findConversationTurnIndex(timeline, selectedRequestID);
    const previous = document.getElementById('dagTimelinePrevious');
    const next = document.getElementById('dagTimelineNext');
    if (previous) previous.disabled = currentTurnIndex <= 0;
    if (next) next.disabled = currentTurnIndex < 0 || currentTurnIndex >= turns.length - 1;

    const content = document.getElementById('dagTimelineContent');
    if (!content) return;
    if (turns.length === 0 && (timeline.orphans || []).length === 0) {
        content.innerHTML = `
            <div class="empty-detail">
                <div class="empty-detail-icon">💬</div>
                <div>No verified executions are available for this conversation.</div>
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
                <p>These executions carry the conversation ID, but their top-level owner was not available.</p>
                ${(timeline.orphans || []).map(orphan =>
                    renderTimelineRelatedExecution(orphan, selectedRequestID, true)
                ).join('')}
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
    const statusClass = execution.interrupted
        ? 'interrupted'
        : (execution.success ? 'success' : 'error');
    const statusLabel = execution.interrupted
        ? '⏸ Interrupted'
        : (execution.success ? '✓ Success' : '✗ Failed');
    const duration = (execution.total_duration_ms || 0) +
        (execution.llm_total_duration_ms || 0);

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
                    aria-label="View DAG for turn ${index + 1}: ${escapeHtmlAttribute(truncateText(execution.original_request || 'Unknown request', 90))}"
                    ${execution.request_id === selectedRequestID ? 'aria-current="true"' : ''}
                >
                    <div class="dag-timeline-card-header">
                        <div>
                            <span class="dag-turn-label">Turn ${index + 1} of ${totalTurns}</span>
                            <time datetime="${escapeHtmlAttribute(execution.created_at || '')}">${formatDateTime(execution.created_at)}</time>
                        </div>
                        <span class="status-badge ${statusClass}">${statusLabel}</span>
                    </div>
                    <div class="dag-timeline-request">${escapeHtml(execution.original_request || 'Unknown request')}</div>
                    <div class="dag-timeline-meta">
                        ${execution.agent_name ? `<span>${escapeHtml(execution.agent_name)}</span>` : ''}
                        <span>${execution.step_count || 0} steps</span>
                        ${execution.failed_steps ? `<span class="dag-error-text">${execution.failed_steps} failed</span>` : ''}
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
                        ${related.map(item =>
                            renderTimelineRelatedExecution(item, selectedRequestID)
                        ).join('')}
                    </div>` : ''}
            </div>
        </article>`;
}

function renderTimelineRelatedExecution(execution, selectedRequestID, orphan = false) {
    const current = execution.request_id === selectedRequestID;
    const duration = (execution.total_duration_ms || 0) +
        (execution.llm_total_duration_ms || 0);
    const relation = orphan
        ? 'Unattached'
        : (execution.metadata?.resume_checkpoint_id ? 'HITL resume' : 'Delegated / related');
    return `
        <div
            class="dag-timeline-related ${current ? 'current' : ''}"
            role="button"
            tabindex="0"
            data-view-timeline-execution="${escapeHtmlAttribute(execution.request_id || '')}"
            aria-label="View DAG for ${escapeHtmlAttribute(relation.toLowerCase())} execution: ${escapeHtmlAttribute(truncateText(execution.original_request || 'Unknown request', 90))}"
            ${current ? 'aria-current="true"' : ''}
        >
            <span class="dag-related-branch" aria-hidden="true">↳</span>
            <div>
                <span class="dag-relation-label">${relation}</span>
                <div>${escapeHtml(truncateText(execution.original_request || 'Unknown request', 72))}</div>
                <div class="dag-timeline-meta">
                    ${execution.agent_name ? `<span>${escapeHtml(execution.agent_name)}</span>` : ''}
                    <span>${formatDateTime(execution.created_at)}</span>
                    <span>${execution.step_count || 0} steps</span>
                    <span>${formatDuration(duration)}</span>
                    <span class="${execution.success ? 'dag-success-text' : 'dag-error-text'}">${execution.interrupted ? 'Interrupted' : (execution.success ? 'Success' : 'Failed')}</span>
                </div>
                <div class="dag-timeline-request-id">${escapeHtml(execution.request_id || '')}</div>
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
    const conversationID = listPanelMode === 'timeline'
        ? activeConversationID
        : selected?.conversation_id;
    const timeline = conversationCache.get(conversationID)?.data;
    if (!timeline) return;
    const currentIndex = findConversationTurnIndex(timeline, selected?.request_id);
    const targetIndex = direction === 'previous' ? currentIndex - 1 : currentIndex + 1;
    const target = timeline.turns?.[targetIndex]?.execution?.request_id;
    if (target) void selectExecution(target);
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

async function selectExecution(
    requestId,
    { restoreTimelineActionFocus = false } = {},
) {
    document.querySelectorAll('.node-popup').forEach(p => p.remove());
    const detailPanel = document.getElementById('dagDetailPanel');
    showLoading(detailPanel, 'Loading execution...');
    try {
        // Use unified endpoint to get execution, LLM debug, and HITL data in one call
        const { data } = await fetchAPI(`/api/executions/${requestId}/unified`);
        const selectionChanged = selected?.request_id !== data.request_id;
        selected = data;
        // Multi-phase support: merge all phase plan steps into plan.steps
        // so existing rendering code works unchanged
        if (selected.phase_plans && selected.phase_plans.length > 1) {
            if (!selected.plan) {
                selected.plan = { steps: [] };
            }
            selected.plan.steps = selected.phase_plans.flatMap(p => p.steps || []);
        }
        // Pre-compute traceability labels once, globally across all LLM
        // interactions. Ensures DAG popups and LLM-call cards show
        // matching suffixes on iterative call types (tiered_selection,
        // etc.) where the base label collides across iterations.
        annotateCallLabels(selected.llm_interactions);
        if (selectionChanged) {
            requestSelectedGroupAutoExpansion();
        }
        applyPendingSelectedGroupAutoExpansion();
        renderExecutionList();
        const activeTimeline = conversationCache.get(activeConversationID)?.data;
        if (activeTimeline && listPanelMode === 'timeline') {
            renderConversationTimeline(activeTimeline, selected.request_id);
        }
        updateDetailTabs(); // Update tab visibility based on available data
        renderExecutionDetail();
        if (restoreTimelineActionFocus && listPanelMode === 'timeline') {
            requestAnimationFrame(() => {
                const action = Array.from(document.querySelectorAll(
                    '[data-view-timeline-execution]',
                )).find(button => button.dataset.viewTimelineExecution === requestId);
                action?.focus({ preventScroll: true });
            });
        }
    } catch (error) {
        console.error('Failed to fetch execution:', error);
    } finally {
        hideLoading(detailPanel);
    }
}

// Update tab visibility based on available data
function updateDetailTabs() {
    const llmTab = document.querySelector('[data-tab="dag-llm"]');
    const hitlTab = document.querySelector('[data-tab="dag-hitl"]');
    const preTab = document.querySelector('[data-tab="dag-pre"]');
    const postTab = document.querySelector('[data-tab="dag-post"]');

    if (llmTab) {
        llmTab.style.display = selected?.has_llm_data ? 'block' : 'none';
    }
    if (hitlTab) {
        hitlTab.style.display = selected?.has_hitl_data ? 'block' : 'none';
    }

    // Pre/Post tabs visible when hook interactions exist for the respective phase.
    const interactions = selected?.llm_interactions || [];
    const { isPreHook, isPostHook } = classifyInteractions(interactions);
    const hasPreHooks = interactions.some(isPreHook);
    const hasPostHooks = interactions.some(isPostHook);

    if (preTab) {
        preTab.style.display = hasPreHooks ? 'block' : 'none';
    }
    if (postTab) {
        postTab.style.display = hasPostHooks ? 'block' : 'none';
    }
}

function setDagDetailTab(tab) {
    activeTab = tab;
    document.querySelectorAll('.node-popup').forEach(p => p.remove());
    document.querySelectorAll('#dagDetailPanel .detail-tab').forEach(btn => {
        btn.classList.toggle('active', btn.dataset.tab === tab);
    });
    renderExecutionDetail();
}

function renderExecutionDetail() {
    if (!selected) return;

    const container = document.getElementById('dagDetailContent');

    // Update detail title
    document.getElementById('dagDetailTitle').textContent = truncateText(selected.original_request, 50);

    if (activeTab === 'dag-viz') {
        renderDAGVisualization(container);
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
    } else if (activeTab === 'dag-raw') {
        container.innerHTML = `
            <div class="json-container">
                <button class="copy-btn" onclick="event.stopPropagation()">Copy</button>
                <div class="json-view">${syntaxHighlightJson(selected)}</div>
            </div>`;
        // Bind copy button via delegation
        const copyBtn = container.querySelector('.copy-btn');
        if (copyBtn) {
            copyBtn.addEventListener('click', (e) => {
                copyToClipboard(JSON.stringify(selected, null, 2), e);
            });
        }
    }
}

// ---------------------------------------------------------------------------
// DAG view mode toggle
// ---------------------------------------------------------------------------

function setDagViewMode(mode) {
    if (viewMode === mode) return;
    viewMode = mode;
    // Re-render the DAG visualization
    if (selected) {
        renderExecutionDetail();
    }
}

// ---------------------------------------------------------------------------
// DAG Visualization
// ---------------------------------------------------------------------------

function renderDAGVisualization(container) {
    const stepCount = selected.plan?.steps?.length || 0;
    const hasLLM = selected.has_llm_data;
    const hasHITL = selected.has_hitl_data;
    const llmCallCount = selected.llm_interactions?.length || 0;

    // Compute total duration: executor time (tool calls) + LLM time (AI calls)
    // These are additive because executor handles tool invocations, LLM handles AI planning/synthesis
    const executorDurationMs = selected.total_duration_ms || 0;
    const llmDurationMs = selected.llm_debug_summary?.total_duration_ms || 0;
    const totalDurationMs = executorDurationMs + llmDurationMs;

    // Check if we can show Full Flow (has LLM or HITL data)
    const canShowFullFlow = hasLLM || hasHITL;
    const agentName = selected.agent_name || 'orchestrator';

    container.innerHTML = `
        <div class="dag-viz-container">
            <div class="dag-viz-header">
                <div class="dag-viz-title" title="Click to expand/collapse">${formatConversationRequest(selected.original_request || 'Unknown Request')}</div>
                <div class="dag-viz-meta">
                    ${agentName !== 'orchestrator' ? `<span class="dag-viz-meta-item" style="color: var(--accent-purple);">🤖 ${escapeHtml(agentName)}</span>` : ''}
                    <span class="dag-viz-meta-item">ID: ${(selected.request_id || '').substring(0, 20)}...</span>
                    ${selected.trace_id ? `<span class="dag-viz-meta-item">Trace: ${selected.trace_id.substring(0, 16)}...</span>` : ''}
                    <span class="dag-viz-meta-item">${stepCount} step${stepCount !== 1 ? 's' : ''}</span>
                    <span class="dag-viz-meta-item">⏱️ Total: ${formatDuration(totalDurationMs)}</span>
                    <span class="dag-viz-meta-item ${selected.interrupted ? 'interrupted' : (selected.success ? 'success' : 'error')}">${selected.interrupted ? '⏸ Interrupted' : (selected.success ? '✓ Success' : '✗ Failed')}</span>
                    ${hasLLM ? `<span class="dag-viz-meta-item" style="color: var(--accent-blue);">💭 ${llmCallCount} LLM calls</span>` : ''}
                    ${hasHITL && !selected.interrupted ? `<span class="dag-viz-meta-item" style="color: var(--accent-orange);">⏸️ HITL</span>` : ''}
                    ${selected.phase_count > 1 ? `<span class="dag-viz-meta-item" style="color: var(--accent-teal);">🔄 ${selected.phase_count} Phases</span>` : ''}
                    ${selected.forced_terminal ? `<span class="dag-viz-meta-item" style="color: var(--accent-orange);">⚠ Forced Terminal</span>` : ''}
                    ${canShowFullFlow ? `
                    <div class="dag-view-toggle">
                        <button class="toggle-btn steps-btn ${viewMode === 'steps' ? 'active' : ''}" data-view-mode="steps">Steps Only</button>
                        <button class="toggle-btn full-btn ${viewMode === 'full' ? 'active' : ''}" data-view-mode="full">Full Flow</button>
                    </div>` : ''}
                </div>
                ${(() => {
                    const regenRaw = selected.metadata?.plan_regeneration_events;
                    if (!regenRaw) return '';
                    try {
                        const evts = JSON.parse(regenRaw);
                        if (!Array.isArray(evts) || evts.length === 0) return '';
                        return evts.map(evt => {
                            const termChange = (evt.original_terminal !== undefined && evt.original_terminal !== evt.regenerated_terminal)
                                ? ` · terminal: ${evt.original_terminal} → ${evt.regenerated_terminal}` : '';
                            const planChange = evt.original_plan_id
                                ? ` · plan: ${escapeHtml(evt.original_plan_id)} → ${escapeHtml(evt.regenerated_plan_id)}` : '';
                            return `<div style="margin-top: 6px; padding: 6px 12px; background: rgba(255,140,50,0.08); border: 1px solid rgba(255,140,50,0.25); border-radius: 6px; font-size: 11px; color: #ff8c32;">⚠️ <strong>Phase ${evt.phase_number} plan regenerated</strong> — ${escapeHtml(evt.validation_error)}${termChange}${planChange}</div>`;
                        }).join('');
                    } catch(e) { return ''; }
                })()}
            </div>
            ${selected.interrupted && selected.resume_sibling_request_id ? `
            <div class="dag-resume-sibling-banner" style="margin: 8px 0 12px 0; padding: 10px 14px; background: rgba(46, 204, 113, 0.10); border: 1px solid rgba(46, 204, 113, 0.35); border-radius: 8px; font-size: 12px; color: #2ecc71; display: flex; align-items: center; gap: 8px;">
                <span>⏩</span>
                <span>This execution was interrupted for approval. A completed resume exists:</span>
                <a href="#" data-resume-sibling="${escapeHtml(selected.resume_sibling_request_id)}" style="color: #2ecc71; text-decoration: underline;">${escapeHtml((selected.resume_sibling_request_id || '').substring(0, 28))}…</a>
            </div>` : ''}
            <div id="dagContainer" class="dag-viz-canvas"></div>
            <div id="dagLegend" class="dag-viz-legend">
                ${viewMode === 'full' ? `
                <!-- Full Flow Legend -->
                <div class="dag-legend-item">
                    <span class="dag-legend-dot orchestrator"></span>
                    <span>Agent</span>
                </div>
                <div class="dag-legend-item">
                    <span class="dag-legend-dot llm-call"></span>
                    <span>LLM Call</span>
                </div>
                <div class="dag-legend-item">
                    <span class="dag-legend-dot" style="background: #a064f0;"></span>
                    <span>Agent LLM</span>
                </div>
                <div class="dag-legend-item">
                    <span class="dag-legend-dot diamond checkpoint"></span>
                    <span>Plan ✓</span>
                </div>
                <div class="dag-legend-item">
                    <span class="dag-legend-dot diamond checkpoint-before"></span>
                    <span>⏸️ Before</span>
                </div>
                <div class="dag-legend-item">
                    <span class="dag-legend-dot diamond checkpoint-after"></span>
                    <span>✓ After</span>
                </div>
                <div class="dag-legend-item">
                    <span class="dag-legend-dot diamond checkpoint-error"></span>
                    <span>⚠️ Error</span>
                </div>
                <div class="dag-legend-item">
                    <span class="dag-legend-dot completed"></span>
                    <span>Step (OK)</span>
                </div>
                <div class="dag-legend-item">
                    <span class="dag-legend-dot failed"></span>
                    <span>Step (Fail)</span>
                </div>
                <div class="dag-legend-item">
                    <span class="dag-legend-dot response"></span>
                    <span>Response</span>
                </div>
                <div class="dag-legend-item" title="All parameters resolved by auto-wiring (no LLM cost)">
                    <span class="dag-legend-dot res-auto-wire"></span>
                    <span>Auto-wire</span>
                </div>
                <div class="dag-legend-item" title="Some parameters required LLM micro-resolution">
                    <span class="dag-legend-dot res-llm"></span>
                    <span>LLM Resolved</span>
                </div>
                <div class="dag-legend-item" title="Semantic retry was used for error recovery">
                    <span class="dag-legend-dot res-retry"></span>
                    <span>Sem. Retry</span>
                </div>
                <div class="dag-legend-item" title="Template auto-include safety net fired (missing depends_on)">
                    <span class="dag-legend-dot res-auto-include"></span>
                    <span>Auto-Include</span>
                </div>
                <div class="dag-legend-item" title="Step result was trimmed before synthesis (dotted purple border)">
                    <span class="dag-legend-dot res-trim"></span>
                    <span>Result Trimmed</span>
                </div>
                ${selected.phase_count > 1 ? `
                <div class="dag-legend-item" title="Phase boundary between iterative planning phases">
                    <span class="dag-legend-dot phase-boundary"></span>
                    <span>Phase →</span>
                </div>` : ''}
                ` : `
                <!-- Steps Only Legend -->
                <div class="dag-legend-item">
                    <span class="dag-legend-dot completed"></span>
                    <span>Completed</span>
                </div>
                <div class="dag-legend-item">
                    <span class="dag-legend-dot failed"></span>
                    <span>Failed</span>
                </div>
                <div class="dag-legend-item">
                    <span class="dag-legend-dot pending"></span>
                    <span>Pending</span>
                </div>
                <div class="dag-legend-item">
                    <span class="dag-legend-dot blocked"></span>
                    <span>Blocked</span>
                </div>
                <div class="dag-legend-item">
                    <span class="dag-legend-dot skipped"></span>
                    <span>Skipped</span>
                </div>
                <div class="dag-legend-item" title="All parameters resolved by auto-wiring (no LLM cost)">
                    <span class="dag-legend-dot res-auto-wire"></span>
                    <span>Auto-wire</span>
                </div>
                <div class="dag-legend-item" title="Some parameters required LLM micro-resolution">
                    <span class="dag-legend-dot res-llm"></span>
                    <span>LLM Resolved</span>
                </div>
                <div class="dag-legend-item" title="Semantic retry was used for error recovery">
                    <span class="dag-legend-dot res-retry"></span>
                    <span>Sem. Retry</span>
                </div>
                <div class="dag-legend-item" title="Template auto-include safety net fired (missing depends_on)">
                    <span class="dag-legend-dot res-auto-include"></span>
                    <span>Auto-Include</span>
                </div>
                <div class="dag-legend-item" title="Step result was trimmed before synthesis (dotted purple border)">
                    <span class="dag-legend-dot res-trim"></span>
                    <span>Result Trimmed</span>
                </div>
                ${selected.phase_count > 1 ? `
                <div class="dag-legend-item" title="Phase boundary between iterative planning phases">
                    <span class="dag-legend-dot phase-boundary"></span>
                    <span>Phase →</span>
                </div>` : ''}
                `}
            </div>
        </div>`;

    // Bind view-mode toggle buttons via delegation
    const vizTitle = container.querySelector('.dag-viz-title');
    if (vizTitle) {
        vizTitle.addEventListener('click', function() {
            this.classList.toggle('expanded');
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
        } else {
            // Cache miss or stale (execution switched, dagContainer was rebuilt).
            // Remove stale cache entry if any.
            if (cached) {
                cached.cy.destroy();
                dagCache.delete(cacheKey);
            }
            initCytoscape();
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
    const planningCalls = llmInteractions.filter(i => isPlanningType(i.type));

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

// Update the DAG legend with parallelism statistics
function updateDAGLegend(depth, maxParallelism) {
    const legendContainer = document.querySelector('.dag-viz-legend');
    if (!legendContainer) return;

    // Check if stats already exist
    let statsDiv = legendContainer.querySelector('.dag-stats');
    if (!statsDiv) {
        statsDiv = document.createElement('div');
        statsDiv.className = 'dag-stats';
        statsDiv.style.cssText = 'margin-left: auto; display: flex; gap: 16px; font-size: 11px; color: var(--text-muted);';
        legendContainer.appendChild(statsDiv);
    }

    statsDiv.innerHTML = `
        <span style="display: flex; align-items: center; gap: 6px;">
            <span style="color: var(--accent-teal);">↕</span>
            Depth: ${depth}
        </span>
        <span style="display: flex; align-items: center; gap: 6px;">
            <span style="color: var(--accent-purple);">⇄</span>
            Max Parallel: ${maxParallelism}
        </span>
    `;
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
    if (!dagContainerParent || !selected?.plan?.steps) return;

    // Hide existing cached containers
    dagContainerParent.querySelectorAll('.dag-cached-graph').forEach(el => el.style.display = 'none');

    // Create a child container for this instance (allows caching multiple graphs)
    const dagContainer = document.createElement('div');
    dagContainer.className = 'dag-cached-graph';
    dagContainer.style.cssText = 'width: 100%; height: 100%;';
    dagContainerParent.appendChild(dagContainer);

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

    // Compute levels using topological sort for parallelism info
    const levels = computeStepLevels(selected.plan.steps);
    const maxParallelism = Math.max(...levels.map(l => l.length), 0);

    // Update legend with parallelism info (only for steps view)
    if (viewMode === 'steps') {
        updateDAGLegend(levels.length, maxParallelism);
    }

    let nodes = [];
    let edges = [];

    if (viewMode === 'full') {
        // === FULL FLOW MODE ===
        // Build complete execution flow: Orchestrator → LLM → [Checkpoint] → Steps → LLM → Response
        const agentName = selected.agent_name || 'orchestrator';
        const llmInteractions = selected.llm_interactions || [];
        const checkpoints = selected.hitl_checkpoints || [];

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
        const allPlanSteps = selected.plan.steps || [];
        const phasePlans = selected.phase_plans || [];
        const llmByPhase = groupLLMCallsByPhase(llmInteractions);
        const fullFlowLevels = computeStepLevels(allPlanSteps);

        // HITL plan-approval checkpoints (before_plan_execution) are attached
        // after Phase 1 planning LLM calls.
        const planCheckpoints = checkpoints.filter(c => c.interrupt_point === 'before_plan_execution');

        // Type labels live in static/js/llm-types.js — use getLLMType(type).label.

        let previousPhaseExitNode = 'orchestrator';
        let globalLLMIndex = 0;
        let memoryLLMIndex = 0;

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
                    label: `📝 User Memory: BeforePlanning (${userMemBeforeCalls.length} steps, ${formatDuration(totalDuration)})`,
                    nodeType: 'user_memory_group',
                    pipelineStage: 'before_planning',
                    steps: userMemBeforeCalls,
                    totalDuration: totalDuration,
                    stepCount: userMemBeforeCalls.length,
                }
            });
            edges.push({ data: { source: previousPhaseExitNode, target: nodeId, edgeType: 'memory_llm' } });
            previousPhaseExitNode = nodeId;
        }

        if (phasePlans.length > 0) {
            // === Per-phase graph building ===
            for (let phaseIdx = 0; phaseIdx < phasePlans.length; phaseIdx++) {
                const phasePlan = phasePlans[phaseIdx];
                const phaseNum = phasePlan.phase_number || (phaseIdx + 1);
                const phaseLLMCalls = llmByPhase[phaseNum] || [];
                const phaseSteps = phasePlan.steps || [];
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
                    nodes.push({
                        data: buildLLMBackedNodeData(call, {
                            id: nodeId,
                            label: `💭 ${getLLMType(call.type).label}`,
                            nodeType: 'llm_call',
                            phaseNumber: phaseNum,
                        })
                    });
                    edges.push({ data: { source: lastLLMNodeInPhase, target: nodeId } });
                    lastLLMNodeInPhase = nodeId;
                });

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
                    nodes.push({
                        data: {
                            id: step.step_id,
                            label: baseLabelFull + (resInfoFull.summary ? '\n' + resInfoFull.summary : '') + trimLabelFull,
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
                });

                // 2d. Phase boundary node (links this phase's leaf steps to next phase)
                if (phaseIdx < phasePlans.length - 1) {
                    const boundaryId = `phase_boundary_${phaseIdx + 1}`;
                    const dependedOn = new Set();
                    phaseSteps.forEach(s => {
                        (s.depends_on || []).forEach(d => {
                            if (phaseStepIds.has(d)) dependedOn.add(d);
                        });
                    });
                    const leafStepsInPhase = phaseSteps.filter(s => !dependedOn.has(s.step_id));
                    const nextPhaseNum = phasePlans[phaseIdx + 1]?.phase_number || (phaseNum + 1);
                    nodes.push({
                        data: {
                            id: boundaryId,
                            label: `Phase ${phaseNum} → ${nextPhaseNum}`,
                            nodeType: 'phase_boundary',
                            phaseFrom: phaseNum,
                            phaseTo: nextPhaseNum,
                            continuationNote: phasePlans[phaseIdx + 1]?.continuation_note || ''
                        }
                    });
                    leafStepsInPhase.forEach(s => {
                        edges.push({ data: { source: s.step_id, target: boundaryId, edgeType: 'phase_transition' } });
                    });
                    previousPhaseExitNode = boundaryId;
                }
            }
        } else {
            // === Fallback: no phase_plans data (single-phase backward compat) ===
            // Type membership comes from the LLM-type registry — see isPlanningType.
            // Orchestrator-level micro_resolution (without a step_id) is also
            // pulled in here because it represents pre-execution parameter
            // resolution that visually belongs in the planner column.
            const planningCalls = llmInteractions.filter(i =>
                isPlanningType(i.type) ||
                (i.type === 'micro_resolution' && !i.step_id)
            );
            planningCalls.forEach((call, idx) => {
                const nodeId = `llm_plan_${globalLLMIndex++}`;
                nodes.push({
                    data: buildLLMBackedNodeData(call, {
                        id: nodeId,
                        label: `💭 ${getLLMType(call.type).label}`,
                        nodeType: 'llm_call',
                    })
                });
                const source = idx === 0 ? previousPhaseExitNode : `llm_plan_${idx - 1}`;
                edges.push({ data: { source, target: nodeId } });
            });
            const lastPlanNode = planningCalls.length > 0 ? `llm_plan_${planningCalls.length - 1}` : previousPhaseExitNode;
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
                edges.push({ data: { source: lastPlanNode, target: nodeId } });
            });
            const stepEntryNode = planCheckpoints.length > 0
                ? `checkpoint_plan_${planCheckpoints.length - 1}`
                : lastPlanNode;
            const rootSteps = allPlanSteps.filter(s => !s.depends_on || s.depends_on.length === 0);
            allPlanSteps.forEach(step => {
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
                const level = fullFlowLevels.findIndex(l => l.includes(step.step_id));
                const parallelCount = level >= 0 ? fullFlowLevels[level].length : 1;
                const resInfoFull = getResolutionInfo(result);
                const baseLabelFull = step.agent_name || stepCapability;
                const trimMetaFull = result?.metadata?.result_trim;
                // Mirror the multi-phase path: show every recorded compaction,
                // while isLossyTrim remains the authoritative loss check.
                const wasTrimmedFull = hasTrimDisplay(trimMetaFull);
                const trimLabelFull = wasTrimmedFull ? trimNodeLabel(trimMetaFull) : '';
                nodes.push({
                    data: {
                        id: step.step_id,
                        label: baseLabelFull + (resInfoFull.summary ? '\n' + resInfoFull.summary : '') + trimLabelFull,
                        nodeType: 'step',
                        status: status,
                        resolutionType: resInfoFull.type,
                        resolutionSummary: resInfoFull.summary,
                        duration: getDurationMs(result),
                        capability: stepCapability,
                        instruction: step.instruction || '',
                        level: level + 1,
                        parallelCount: parallelCount,
                        isParallel: parallelCount > 1,
                        dependsOn: step.depends_on || [],
                        hasAutoIncludes: (result?.metadata?.template_auto_includes?.length > 0),
                        trimmed: !!wasTrimmedFull
                    }
                });
            });
            rootSteps.forEach(step => {
                edges.push({ data: { source: stepEntryNode, target: step.step_id } });
            });
            allPlanSteps.forEach(step => {
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
            });
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
        const stepSpecificLLMCalls = llmInteractions.filter(i =>
            ['micro_resolution', 'semantic_retry', 'error_analysis', 'result_distillation', 'hallucination_detection'].includes(i.type) && i.step_id
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
        const lastPhasePlanSteps = phasePlans.length > 0
            ? (phasePlans[phasePlans.length - 1].steps || [])
            : allPlanSteps;
        const lastPhaseStepIds = new Set(lastPhasePlanSteps.map(s => s.step_id));
        const lastPhaseDependedOn = new Set();
        lastPhasePlanSteps.forEach(s => {
            (s.depends_on || []).forEach(d => {
                if (lastPhaseStepIds.has(d)) lastPhaseDependedOn.add(d);
            });
        });
        const leafSteps = lastPhasePlanSteps.filter(s => !lastPhaseDependedOn.has(s.step_id));

        // ORCH-018: when the planner emits NeedsUserInput (clarification short-
        // circuit), the last phase has zero steps — leafSteps is empty and the
        // synthesis node would be orphaned in the DAG. In that case the synthesis
        // node should connect from the last upstream node before execution: the
        // last planning LLM call (or whatever previousPhaseExitNode points to,
        // e.g. user_memory_before). previousPhaseExitNode is a snapshot from the
        // top of the phase loop, so for clarification turns we recompute the
        // intended source by walking backwards from synthesis through the
        // existing planning chain. The most recent llm_plan_* node is the right
        // anchor — it represents the planner call that produced the clarification.
        const lastPlanningNodeId = (() => {
            // Find the highest-index llm_plan_N node we've already added.
            for (let i = nodes.length - 1; i >= 0; i--) {
                const id = nodes[i].data && nodes[i].data.id;
                if (typeof id === 'string' && id.startsWith('llm_plan_')) {
                    return id;
                }
            }
            // No planning node yet (extremely defensive — phase loop must have
            // produced at least one planner call to reach a clarification plan).
            // Fall back to the upstream of the phase loop entry.
            return previousPhaseExitNode;
        })();

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
                    if (leafSteps.length > 0) {
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
                        // Anchor it to the last upstream node, mirroring the ORCH-018
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
        const synthesisCalls = llmInteractions.filter(i => i.type === 'synthesis' || i.type === 'synthesis_streaming');
        if (synthesisCalls.length > 0) {
            synthesisCalls.forEach((call, idx) => {
                const nodeId = `llm_synth_${idx}`;
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
                        edges.push({ data: { source: eventSumNodes[eventSumNodes.length - 1], target: nodeId } });
                    } else if (leafSteps.length > 0) {
                        // Connect leaf steps directly to synthesis
                        leafSteps.forEach(step => {
                            const result = stepResults[step.step_id];
                            const isFailed = result && !result.success;
                            edges.push({
                                data: {
                                    source: step.step_id,
                                    target: nodeId,
                                    edgeType: isFailed ? 'failed' : undefined
                                }
                            });
                        });
                    } else {
                        // ORCH-018: clarification short-circuit case — last phase
                        // had zero steps because the planner emitted NeedsUserInput.
                        // Connect synthesis from the last planning node so the DAG
                        // remains a single connected graph instead of two orphaned
                        // components (planning chain | synthesis chain).
                        edges.push({ data: { source: lastPlanningNodeId, target: nodeId } });
                    }
                } else {
                    edges.push({ data: { source: `llm_synth_${idx - 1}`, target: nodeId } });
                }
            });
        }

        // 7. Response node
        // Source resolution priority:
        //   1. Last synthesis call (normal happy path)
        //   2. Last event summarization (rare — synthesis disabled)
        //   3. First leaf step (synthesis-less single-phase)
        //   4. Last planning node (ORCH-018 clarification short-circuit edge case
        //      where synthesis was somehow skipped — defensive)
        //   5. Orchestrator root (fully degenerate trace)
        const responseSource = synthesisCalls.length > 0 ? `llm_synth_${synthesisCalls.length - 1}` :
            (eventSumNodes.length > 0 ? eventSumNodes[eventSumNodes.length - 1] :
            (leafSteps.length > 0 ? leafSteps[0].step_id :
            (lastPlanningNodeId !== 'orchestrator' ? lastPlanningNodeId : 'orchestrator')));
        const successLabel = selected.success ? '✓ Complete' : (selected.interrupted ? '⏸ Paused (HITL)' : '✗ Failed');
        nodes.push({
            data: {
                id: 'response',
                label: `📤 ${successLabel}`,
                nodeType: 'response',
                success: selected.success,
                interrupted: selected.interrupted ? 'true' : 'false'
            }
        });
        if (synthesisCalls.length > 0 || eventSumNodes.length > 0) {
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
            // ORCH-018 defensive: no synthesis, no event summarization, no leaf
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
                    label: `📝 User Memory: AfterSynthesis (${userMemAfterCalls.length} steps, ${formatDuration(totalDuration)}${llmCount ? `, ${llmCount} LLM` : ''})`,
                    nodeType: 'user_memory_group',
                    pipelineStage: 'after_synthesis',
                    steps: userMemAfterCalls,
                    totalDuration: totalDuration,
                    stepCount: userMemAfterCalls.length,
                    llmCount: llmCount,
                }
            });
            edges.push({ data: { source: 'response', target: nodeId, edgeType: 'memory_llm' } });
        }

    } else {
        // === STEPS ONLY MODE (original behavior) ===
        nodes = selected.plan.steps.map(step => {
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
            return {
                data: {
                    id: step.step_id,
                    label: baseLabel + (resInfo.summary ? '\n' + resInfo.summary : ''),
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
                    hasAutoIncludes: (result?.metadata?.template_auto_includes?.length > 0)
                }
            };
        });

        // Build edges for steps mode
        // If source step failed, mark the edge as failed to show broken data flow
        selected.plan.steps.forEach(step => {
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

    // Create Cytoscape instance with premium dark styling
    cyInstance = cytoscape({
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
                // Response node when execution is interrupted (HITL pause) - amber/pause style
                selector: 'node[nodeType="response"][interrupted="true"]',
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
                setTimeout(function() { cyInstance.fit(50); }, 100);
            }
        },
        // Enable better rendering
        minZoom: 0.3,
        maxZoom: 2
        // wheelSensitivity intentionally left at the Cytoscape default.
        // The library prints a console warning when this is customized
        // because the optimal value depends on the user's mouse and OS,
        // and a hard-coded value zooms unnaturally on most hardware.
    });

    // Add click handler for nodes
    cyInstance.on('tap', 'node', function(evt) {
        const node = evt.target;
        const nodeData = node.data();
        const nodeType = nodeData.nodeType;

        // Handle user memory group nodes — show step list popup
        if (nodeType === 'user_memory_group') {
            showUserMemoryGroupPopup(node, nodeData);
            return;
        }
        // Handle different node types in full flow mode
        if (nodeType === 'orchestrator' || nodeType === 'llm_call' || nodeType === 'llm_step' || nodeType === 'agent_llm' || nodeType === 'memory_llm' || nodeType === 'checkpoint' || nodeType === 'response' || nodeType === 'phase_boundary') {
            showFullFlowNodePopup(node, nodeData);
        } else {
            // Step node - use existing logic
            const stepId = node.id();
            const result = stepResults[stepId];
            const step = selected.plan.steps.find(s => s.step_id === stepId);
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
                    <div style="font-family: 'SF Mono', monospace; font-size: 11px;">${selected.request_id}</div>
                </div>
            `;
            break;
        case 'llm_call':
            title = '💭 LLM Call';
            color = 'var(--accent-blue)';
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
            const parentStep = selected.plan.steps.find(s => s.step_id === parentStepId);
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
            const success = nodeData.success;
            const resultColor = success ? 'var(--accent-green)' : 'var(--accent-red)';
            // Compute total duration: executor time + LLM time
            const responseTotalDuration = (selected.total_duration_ms || 0) + (selected.llm_debug_summary?.total_duration_ms || 0);
            content = `
                <div style="margin-top: 10px;">
                    <div style="font-size: 11px; color: var(--text-muted);">Status</div>
                    <div style="color: ${resultColor}; font-weight: 600;">${success ? 'SUCCESS' : 'FAILED'}</div>
                </div>
                <div style="margin-top: 10px;">
                    <div style="font-size: 11px; color: var(--text-muted);">Total Duration</div>
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
                    <div style="font-family: 'SF Mono', monospace; font-size: 10px;">${stepDeps.join(', ')}</div>
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
        <div style="display: flex; align-items: center; gap: 10px; margin-bottom: 8px;">
            <div style="font-weight: 600; color: ${color};">${title}</div>
        </div>
        ${content}
    `;

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
        <div style="color: var(--text-secondary); margin-bottom: 12px; line-height: 1.5;">${truncateInstruction(step.instruction)}</div>

        <!-- Execution Flow Info -->
        <div style="display: flex; gap: 10px; flex-wrap: wrap; margin-bottom: 12px;">
            <span style="padding: 4px 10px; border-radius: 8px; font-size: 10px; background: rgba(10, 132, 255, 0.15); color: var(--accent-blue); font-family: 'SF Mono', 'Monaco', monospace;">
                ${step.step_id}
            </span>
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
        </div>

        ${dependsOn.length > 0 ? `
            <div style="margin-bottom: 12px; padding: 8px 10px; background: rgba(0,0,0,0.2); border-radius: 8px; font-size: 11px;">
                <span style="color: var(--text-muted);">Depends on:</span>
                <span style="color: var(--text-secondary); margin-left: 6px;">${dependsOn.join(', ')}</span>
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
            </div>
            ${result.error ? `
                <div style="margin-top: 10px; padding: 10px 12px; background: rgba(255, 107, 107, 0.1); border-radius: 8px; border: 1px solid rgba(255, 107, 107, 0.2); color: var(--accent-red); font-size: 11px;">
                    ${result.error}
                </div>
            ` : ''}
        ` : '<div style="color: var(--text-muted); font-style: italic;">Not executed yet</div>'}
    `;

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
    if (!selected?.plan?.steps) {
        container.innerHTML = '<div class="empty-detail">No steps available</div>';
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
                const usedBy = usedByMap[step.step_id] || [];
                const hasDependencies = dependsOn.length > 0 || usedBy.length > 0;

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
                    <div class="dag-step-card ${status}">
                        <div class="dag-step-header">
                            <div class="dag-step-title">
                                <span class="dag-step-number" style="${stepNumColors[status]} padding: 4px 12px; border-radius: 8px; font-weight: 700; font-size: 12px;">Step ${parseInt(step.step_id.replace(/\D/g, '')) || (idx + 1)}</span>
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
                            ${result?.error ? `
                                <div class="dag-step-error">
                                    <strong>Error:</strong> ${result.error}
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
    const text = type === 'prompt' ? interaction.prompt : interaction.response;
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
            if (type === 'prompt') {
                content.innerHTML = `<pre style="white-space: pre-wrap; word-wrap: break-word; font-size: 11px; color: var(--text-secondary);">${escapeHtml(interaction.prompt || '')}</pre>`;
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
// Pre-Execution tab — BeforePlanning hook steps
// ---------------------------------------------------------------------------

function renderPreExecution(container) {
    const interactions = selected?.llm_interactions || [];
    const { isPreHook } = classifyInteractions(interactions);
    const preSteps = interactions.filter(isPreHook);

    if (preSteps.length === 0) {
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
        ${preSteps.length} steps | ${formatDuration(totalDuration)} total | Runs before planning
    </div>`;

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
// Post-Execution tab — AfterSynthesis hook steps
// ---------------------------------------------------------------------------

function renderPostExecution(container) {
    const interactions = selected?.llm_interactions || [];
    const { isPostHook } = classifyInteractions(interactions);
    const postSteps = interactions.filter(isPostHook);

    if (postSteps.length === 0) {
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
        ${postSteps.length} steps | ${formatDuration(totalDuration)} total | ${llmCount} LLM calls | Runs after synthesis
    </div>`;

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
// is the traceability label shown next to the type (ORCH-021); when omitted
// it falls back to `callLabel(interaction)`. Callers that compute per-tab
// collision disambiguation pass a suffixed variant (e.g. "... #2"); the
// default is the plain base label.
function renderSingleLLMCard(interaction, idx, displayNum, displayLabel) {
    const config = getLLMCardConfig(interaction.type);
    const _r = config.rgb;
    const category = interaction.category || 'llm';
    const typeLabel = config.label || interaction.type || 'Unknown';
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
            <div style="display: flex; align-items: center; gap: 10px; padding: 8px 14px; margin-bottom: 4px;
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

    return `
        <div class="dag-step-card" style="background: linear-gradient(135deg, ${config.bg} 0%, rgba(255, 255, 255, 0.03) 100%); backdrop-filter: blur(16px) saturate(180%); -webkit-backdrop-filter: blur(16px) saturate(180%); border: 1px solid ${config.border}; box-shadow: ${baseShadow};" onmouseenter="this.style.boxShadow='${hoverShadow}'; this.style.borderColor='rgba(${_r}, 0.4)';" onmouseleave="this.style.boxShadow='${baseShadow}'; this.style.borderColor='${config.border}';">
            <div class="dag-step-header">
                <div class="dag-step-title">
                    <span style="padding: 4px 12px; border-radius: 8px; font-weight: 700; font-size: 12px; background: linear-gradient(135deg, ${config.bg}, rgba(255, 255, 255, 0.05)); border: 1px solid ${config.border}; color: ${config.color};">${config.icon} #${shownNum}</span>
                    <span class="dag-step-id" style="color: ${config.color}; font-weight: 600;" title="${escapeHtml(interaction.type || '')}">${typeLabel}</span>
                    ${shownLabel ? `<span style="font-family: 'SF Mono', monospace; font-size: 11px; color: var(--text-muted);" title="Correlation ID — matches the Call ID shown in the DAG node popup">${escapeHtml(shownLabel)}</span>` : ''}
                    ${interaction.step_id ? `<span class="step-id-badge" title="Associated with step">📍 ${interaction.step_id}</span>` : ''}
                </div>
                <div style="display: flex; align-items: center; gap: 12px;">
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
                <div class="dag-step-response" style="background: rgba(0, 0, 0, 0.15); border-radius: 12px; margin-top: 12px; overflow: hidden;">
                    <div class="dag-step-response-header" data-toggle-llm="${idx}" data-llm-type="prompt" style="background: rgba(255, 255, 255, 0.03); border-bottom: 1px solid rgba(255, 255, 255, 0.06);">
                        <span><span class="expand-arrow" id="llm-prompt-arrow-${idx}">▶</span> View Prompt</span>
                        <span style="display: flex; align-items: center; gap: 8px;">
                            <button class="copy-inline-btn" data-copy-llm="${idx}" data-copy-type="prompt">Copy</button>
                            <span style="color: var(--text-muted); font-size: 10px;">${(interaction.prompt_tokens || 0).toLocaleString()} tokens</span>
                        </span>
                    </div>
                    <div id="llm-prompt-${idx}" class="dag-step-response-content" style="display: none; background: rgba(0, 0, 0, 0.2);" data-deferred="prompt" data-llm-idx="${idx}">
                    </div>
                </div>
                <div class="dag-step-response" style="background: rgba(0, 0, 0, 0.15); border-radius: 12px; margin-top: 8px; overflow: hidden;">
                    <div class="dag-step-response-header" data-toggle-llm="${idx}" data-llm-type="response" style="background: rgba(255, 255, 255, 0.03); border-bottom: 1px solid rgba(255, 255, 255, 0.06);">
                        <span><span class="expand-arrow" id="llm-response-arrow-${idx}">▶</span> View Response</span>
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

function renderLLMCalls(container) {
    if (!selected?.llm_interactions?.length) {
        container.innerHTML = `
            <div class="empty-detail">
                <div class="empty-detail-icon">🤖</div>
                <div>No LLM calls recorded for this execution</div>
            </div>`;
        return;
    }

    // LLM Calls tab shows only orchestration-level calls (planning, synthesis,
    // tiered selection, etc.). Hook interactions route to the Pre-Execution /
    // Post-Execution tabs. classifyInteractions handles both new agents
    // (hook_phase-aware) and legacy agents (type-string fallback) uniformly.
    const { isOrchestrationCall } = classifyInteractions(selected.llm_interactions);
    const interactions = selected.llm_interactions.filter(isOrchestrationCall);

    if (interactions.length === 0) {
        container.innerHTML = `
            <div class="empty-detail">
                <div class="empty-detail-icon">🤖</div>
                <div>No orchestration LLM calls for this execution<br><span style="font-size: 12px; color: var(--text-muted);">Hook calls are shown in the Pre-Execution and Post-Execution tabs</span></div>
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
                        <span class="dag-step-label">Total Duration</span>
                        <span class="dag-step-value">${formatDuration(orchTotalDurationMs)}</span>
                    </div>
                    ${providerEntries.length > 0 ? `
                        <div style="margin-top: 12px; display: flex; gap: 12px; flex-wrap: wrap;">
                            ${providerEntries.map(([provider, count]) => `
                                <span style="padding: 6px 14px; border-radius: 20px; font-size: 11px; font-weight: 600; background: linear-gradient(135deg, rgba(10, 132, 255, 0.25), rgba(10, 132, 255, 0.1)); border: 1px solid rgba(10, 132, 255, 0.3); color: var(--accent-blue);">
                                    ${provider}: ${count} call${count > 1 ? 's' : ''}
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
            const observer = new IntersectionObserver((entries) => {
                if (!entries[0].isIntersecting || loadedCount >= interactions.length) return;
                const end = Math.min(loadedCount + BATCH, interactions.length);
                // Re-use the same card rendering by building HTML for the next batch
                const batchHtml = interactions.slice(loadedCount, end).map((interaction, batchIdx) => {
                    const origIdx = selected.llm_interactions.indexOf(interaction);
                    const displayNum = loadedCount + batchIdx + 1;
                    return renderSingleLLMCard(interaction, origIdx >= 0 ? origIdx : loadedCount + batchIdx, displayNum);
                }).join('');
                sentinel.insertAdjacentHTML('beforebegin', batchHtml);
                loadedCount = end;
                if (loadedCount >= interactions.length) {
                    observer.disconnect();
                    sentinel.remove();
                }
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
    const stage = nodeData.pipelineStage === 'before_planning' ? 'BeforePlanning' : 'AfterSynthesis';
    const llmCount = steps.filter(s =>
        (s.category || 'llm') === 'llm'
    ).length;

    let html = `
        <div style="font-weight: 600; margin-bottom: 8px; color: #f0a030;">User Memory: ${stage}</div>
        <div style="font-size: 0.85em; color: var(--text-muted); margin-bottom: 12px;">
            ${steps.length} steps | ${formatDuration(nodeData.totalDuration || 0)}${llmCount ? ` | ${llmCount} LLM calls` : ''}
        </div>
    `;

    steps.forEach(step => {
        const category = step.category || 'llm';
        const label = getLLMType(step.type).icon || '·';
        const typeName = step.type?.replace('user_memory_', '').replace(/_/g, ' ') || step.type;
        const opacity = category === 'llm' ? '1.0' : '0.7';
        const categoryBadges = {
            'llm':       '<span style="background:#f0a030;color:#1e1e2e;padding:1px 4px;border-radius:3px;font-size:0.7em;margin-left:4px;">LLM</span>',
            'vector_db': '<span style="background:rgba(100,210,255,0.2);color:#64d2ff;padding:1px 4px;border-radius:3px;font-size:0.7em;margin-left:4px;border:1px solid rgba(100,210,255,0.3);">Vector DB</span>',
            'storage':   '<span style="background:rgba(166,227,161,0.2);color:#a6e3a1;padding:1px 4px;border-radius:3px;font-size:0.7em;margin-left:4px;border:1px solid rgba(166,227,161,0.3);">Storage</span>',
            'embedding': '<span style="background:rgba(218,143,255,0.2);color:#da8fff;padding:1px 4px;border-radius:3px;font-size:0.7em;margin-left:4px;border:1px solid rgba(218,143,255,0.3);">Embedding</span>',
            'logic':     '<span style="background:rgba(255,255,255,0.08);color:var(--text-muted);padding:1px 4px;border-radius:3px;font-size:0.7em;margin-left:4px;border:1px solid rgba(255,255,255,0.12);">Logic</span>',
        };
        const badge = categoryBadges[category] || '';

        html += `<div style="padding:6px 8px;margin:2px 0;background:rgba(240,160,48,0.08);border-radius:6px;border-left:3px solid rgba(240,160,48,${opacity});">`;
        html += `<div style="font-size:0.85em;color:var(--text-primary);">${label} ${escapeHtml(typeName)}${badge} <span style="color:var(--text-muted);float:right;">${formatDuration(step.duration_ms || 0)}</span></div>`;
        if (step.response) html += `<div style="font-size:0.75em;color:var(--text-muted);margin-top:2px;white-space:nowrap;overflow:hidden;text-overflow:ellipsis;">${escapeHtml(step.response)}</div>`;
        if (!step.success) html += `<div style="font-size:0.75em;color:var(--accent-red);margin-top:1px;">Error: ${escapeHtml(step.error || '')}</div>`;
        html += `</div>`;
    });

    const popup = document.createElement('div');
    popup.className = 'node-popup';
    popup.style.cssText = `
        position: fixed;
        min-width: 380px; max-width: 520px; max-height: 500px; overflow-y: auto;
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
    if (!selected?.hitl_checkpoints?.length) {
        container.innerHTML = `
            <div class="empty-detail">
                <div class="empty-detail-icon">🛑</div>
                <div>No HITL checkpoints for this execution</div>
            </div>`;
        return;
    }

    const checkpoints = selected.hitl_checkpoints;

    container.innerHTML = `
        <div style="overflow-y: auto; height: 100%; padding: 16px;">
            ${checkpoints.map((checkpoint, idx) => {
                const statusColors = {
                    'pending': 'rgba(255, 179, 64, 0.2)',
                    'approved': 'rgba(50, 215, 75, 0.2)',
                    'rejected': 'rgba(255, 107, 107, 0.2)',
                    'expired': 'rgba(128, 128, 128, 0.2)'
                };
                const bgColor = statusColors[checkpoint.status] || 'rgba(255, 255, 255, 0.08)';

                return `
                    <div class="dag-step-card" style="background: linear-gradient(135deg, ${bgColor} 0%, rgba(255, 255, 255, 0.02) 100%);">
                        <div class="dag-step-header">
                            <div class="dag-step-title">
                                <span class="dag-step-number">#${idx + 1}</span>
                                <span class="dag-step-id">${checkpoint.interrupt_point || 'Checkpoint'}</span>
                            </div>
                            <div style="display: flex; align-items: center; gap: 12px;">
                                <span class="dag-step-status ${checkpoint.status === 'approved' ? 'completed' : checkpoint.status === 'rejected' ? 'failed' : 'pending'}">
                                    ${checkpoint.status === 'approved' ? '✓ Approved' :
                                      checkpoint.status === 'rejected' ? '✗ Rejected' :
                                      checkpoint.status === 'expired' ? '⊘ Expired' : '◐ Pending'}
                                </span>
                            </div>
                        </div>
                        <div class="dag-step-body">
                            <div class="dag-step-info">
                                <span class="dag-step-label">Checkpoint ID</span>
                                <span class="dag-step-value" style="font-family: 'SF Mono', monospace; font-size: 11px;">${checkpoint.checkpoint_id}</span>
                                ${checkpoint.agent_name ? `
                                    <span class="dag-step-label">Agent</span>
                                    <span class="dag-step-value highlight">${checkpoint.agent_name}</span>
                                ` : ''}
                                <span class="dag-step-label">Created</span>
                                <span class="dag-step-value">${new Date(checkpoint.created_at).toLocaleString()}</span>
                                <span class="dag-step-label">Expires</span>
                                <span class="dag-step-value">${new Date(checkpoint.expires_at).toLocaleString()}</span>
                            </div>
                            ${checkpoint.decision ? `
                                <div style="margin-top: 12px; padding: 12px; background: rgba(0,0,0,0.2); border-radius: 8px;">
                                    <div style="font-weight: 500; margin-bottom: 8px; color: var(--text-primary);">Decision Context</div>
                                    <div class="dag-step-info">
                                        <span class="dag-step-label">Reason</span>
                                        <span class="dag-step-value">${checkpoint.decision.reason || 'N/A'}</span>
                                        <span class="dag-step-label">Priority</span>
                                        <span class="dag-step-value">${checkpoint.decision.priority || 'N/A'}</span>
                                        ${checkpoint.decision.message ? `
                                            <span class="dag-step-label">Message</span>
                                            <span class="dag-step-value" style="grid-column: span 3;">${checkpoint.decision.message}</span>
                                        ` : ''}
                                    </div>
                                </div>
                            ` : ''}
                            ${checkpoint.current_step ? `
                                <div style="margin-top: 12px; padding: 12px; background: rgba(0,0,0,0.2); border-radius: 8px;">
                                    <div style="font-weight: 500; margin-bottom: 8px; color: var(--text-primary);">Current Step</div>
                                    <div class="dag-step-info">
                                        <span class="dag-step-label">Step ID</span>
                                        <span class="dag-step-value">${checkpoint.current_step.step_id || 'N/A'}</span>
                                        <span class="dag-step-label">Capability</span>
                                        <span class="dag-step-value highlight">${checkpoint.current_step.capability || 'N/A'}</span>
                                    </div>
                                </div>
                            ` : ''}
                            ${checkpoint.resolved_parameters ? `
                                <div class="dag-step-response">
                                    <div class="dag-step-response-header" data-toggle-hitl="${checkpoint.checkpoint_id}" data-hitl-section="params">
                                        <span><span class="expand-arrow" id="hitl-params-arrow-${checkpoint.checkpoint_id}">▶</span> Resolved Parameters</span>
                                    </div>
                                    <div id="hitl-params-${checkpoint.checkpoint_id}" class="dag-step-response-content" style="display: none;">
                                        ${formatResponseJson(checkpoint.resolved_parameters)}
                                    </div>
                                </div>
                            ` : ''}
                            ${checkpoint.plan ? `
                                <div class="dag-step-response">
                                    <div class="dag-step-response-header" data-toggle-hitl="${checkpoint.checkpoint_id}" data-hitl-section="plan">
                                        <span><span class="expand-arrow" id="hitl-plan-arrow-${checkpoint.checkpoint_id}">▶</span> Execution Plan (${checkpoint.plan.steps?.length || 0} steps)</span>
                                    </div>
                                    <div id="hitl-plan-${checkpoint.checkpoint_id}" class="dag-step-response-content" style="display: none;">
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
