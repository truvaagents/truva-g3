/**
 * LLM Debug View.
 *
 * Displays LLM interaction records with filtering, detail views,
 * and request-lineage grouping. API failures remain visible; sample data
 * is available only through the backend's explicit mock mode.
 */

import {
    formatTimeAgo,
    formatDateTime,
    formatDuration,
    formatTokens,
    truncateText,
    escapeHtml,
    syntaxHighlightJson,
    formatJsonResponse,
    formatResponseJson,
    formatConversationRequest,
    copyToClipboard,
} from '../utils/format.js';
import { fetchAPI } from '../api.js';
import { createLoadGuard } from '../utils/load-guard.js';
import { showLoading, hideLoading } from '../utils/dom.js';
import {
    getLLMInteractionContent,
    getLLMPromptSections,
} from '../utils/llm-content.js';
import {
    isRetryType,
    getListLabel,
    getListStyledColors,
} from '../llm-types.js';

// ---------------------------------------------------------------------------
// Module state
// ---------------------------------------------------------------------------

const listLoad = createLoadGuard();
const detailLoad = createLoadGuard();
let records = [];
let selected = null;
let requestedSelection = null;
let detailPending = false;
let activeTab = 'interactions';
let typeFilter = 'all';
let expandedInteractions = new Set();
let lineageFilter = null;

// ---------------------------------------------------------------------------
// Lifecycle
// ---------------------------------------------------------------------------

export function init() {
    setupEventDelegation();
    fetchLLMRecords();
}

export function refresh() {
    fetchLLMRecords();
}

// Track bound listeners for cleanup
let boundListeners = [];

export function destroy() {
    requestedSelection = null;
    detailPending = false;
    listLoad.cancel();
    cancelDetailLoad();
    hideLoading(document.getElementById('llmDebugListPanel'));
    document.querySelector('.refresh-btn')?.classList.remove('loading');
    records = [];
    selected = null;
    expandedInteractions = new Set();
    lineageFilter = null;
    // Remove all event listeners added by setupEventDelegation
    boundListeners.forEach(({ el, type, fn }) => el.removeEventListener(type, fn));
    boundListeners = [];
}

// ---------------------------------------------------------------------------
// Event delegation
// ---------------------------------------------------------------------------

function addTrackedListener(el, type, fn) {
    el.addEventListener(type, fn);
    boundListeners.push({ el, type, fn });
}

function setupEventDelegation() {
    // Remove any existing listeners first (idempotent setup)
    boundListeners.forEach(({ el, type, fn }) => el.removeEventListener(type, fn));
    boundListeners = [];

    // Table row clicks
    const tbody = document.getElementById('llmTableBody');
    if (tbody) {
        addTrackedListener(tbody, 'click', (e) => {
            // Check for request-lineage filter click first
            const lineageEl = e.target.closest('[data-lineage-request-id]');
            if (lineageEl) {
                e.stopPropagation();
                filterByRequestLineage(lineageEl.dataset.lineageRequestId);
                return;
            }
            const row = e.target.closest('tr[data-request-id]');
            if (row) {
                selectLLMRecord(row.dataset.requestId);
            }
        });
    }

    // Filter buttons
    const filterContainer = document.getElementById('llmDebugListPanel');
    if (filterContainer) {
        addTrackedListener(filterContainer, 'click', (e) => {
            const btn = e.target.closest('.filter-btn');
            if (btn && btn.dataset.filter !== undefined) {
                setLLMTypeFilter(btn.dataset.filter);
            }
        });

        // Search input
        const searchInput = filterContainer.querySelector('#llmSearchInput');
        if (searchInput) {
            addTrackedListener(searchInput, 'input', () => filterLLMRecords());
        }
    }

    // Detail panel tabs and actions
    const detailPanel = document.getElementById('llmDebugDetailPanel');
    if (detailPanel) {
        addTrackedListener(detailPanel, 'click', (e) => {
            // Tab clicks
            const tab = e.target.closest('.detail-tab');
            if (tab && tab.dataset.tab) {
                setLLMDetailTab(tab.dataset.tab);
                return;
            }

            // Copy JSON button
            if (e.target.closest('[data-action="copy-llm-json"]')) {
                copyLLMJson(e);
                return;
            }

            // Expand/Collapse all buttons
            if (e.target.closest('[data-action="expand-all"]')) {
                expandAllInteractions();
                return;
            }
            if (e.target.closest('[data-action="collapse-all"]')) {
                collapseAllInteractions();
                return;
            }

            // Interaction header toggle
            const header = e.target.closest('.interaction-header');
            if (header) {
                const card = header.closest('.interaction-card');
                if (card) {
                    const index = parseInt(card.dataset.index, 10);
                    if (!isNaN(index)) {
                        toggleInteraction(index);
                    }
                }
                return;
            }

            // Copy prompt/response inline buttons
            const copyBtn = e.target.closest('[data-copy-type]');
            if (copyBtn) {
                e.stopPropagation();
                const index = parseInt(copyBtn.dataset.copyIndex, 10);
                const type = copyBtn.dataset.copyType;
                copyLLMInteractionContent(index, type, e);
                return;
            }

            // Request-lineage filter link in detail view
            const lineageLink = e.target.closest('[data-lineage-request-id]');
            if (lineageLink) {
                filterByRequestLineage(lineageLink.dataset.lineageRequestId);
            }
        });
    }

    // Refresh button
    const refreshBtn = document.querySelector('.refresh-btn');
    if (refreshBtn) {
        addTrackedListener(refreshBtn, 'click', () => fetchLLMRecords());
    }
}

// ---------------------------------------------------------------------------
// Filtering
// ---------------------------------------------------------------------------

function setLLMTypeFilter(filter) {
    typeFilter = filter;
    document.querySelectorAll('#llmDebugListPanel .filter-btn').forEach(btn => {
        btn.classList.toggle('active', btn.dataset.filter === filter);
    });
    filterLLMRecords();
}

function filterLLMRecords() {
    const searchInput = document.getElementById('llmSearchInput');
    const searchTerm = searchInput ? searchInput.value.toLowerCase() : '';
    let filtered = records.filter(record => {
        // Support both summary format (has_errors) and full format (interactions)
        const hasErrors = record.has_errors !== undefined ? record.has_errors :
            (record.interactions ? record.interactions.some(i => !i.success) : false);
        const origReqId = record.original_request_id || record.request_id;

        // Check request-lineage filter first
        if (lineageFilter && origReqId !== lineageFilter) {
            return false;
        }

        const matchesFilter = typeFilter === 'all' ||
            typeFilter === 'grouped' || // grouped mode shows all, but sorted
            (typeFilter === 'success' && !hasErrors) ||
            (typeFilter === 'error' && hasErrors);
        const matchesSearch = !searchTerm ||
            record.request_id.toLowerCase().includes(searchTerm) ||
            (origReqId && origReqId.toLowerCase().includes(searchTerm)) ||
            (record.trace_id && record.trace_id.toLowerCase().includes(searchTerm)) ||
            (record.originating_agent && record.originating_agent.toLowerCase().includes(searchTerm)) ||
            (record.source_components && record.source_components.some(s => s.toLowerCase().includes(searchTerm)));
        return matchesFilter && matchesSearch;
    });

    // In grouped mode, sort by original_request_id then by created_at
    if (typeFilter === 'grouped') {
        filtered.sort((a, b) => {
            const origA = a.original_request_id || a.request_id;
            const origB = b.original_request_id || b.request_id;
            if (origA !== origB) return origA.localeCompare(origB);
            return new Date(a.created_at) - new Date(b.created_at);
        });
    }

    renderLLMTable(filtered);
}

function countLineageRecords(originalRequestId) {
    return records.filter(r => {
        const origReqId = r.original_request_id || r.request_id;
        return origReqId === originalRequestId;
    }).length;
}

function filterByRequestLineage(originalRequestId) {
    // Don't allow filtering if this root request has only one record.
    const linkedCount = countLineageRecords(originalRequestId);
    if (linkedCount <= 1 && lineageFilter !== originalRequestId) {
        return;
    }

    if (lineageFilter === originalRequestId) {
        // Toggle off - clear the filter
        lineageFilter = null;
        const input = document.getElementById('llmSearchInput');
        if (input) input.placeholder = 'Search by request ID, request lineage, source agent, or type...';
    } else {
        lineageFilter = originalRequestId;
        const input = document.getElementById('llmSearchInput');
        if (input) input.placeholder = `Filtering by root request: ${truncateText(originalRequestId, 16)} (click again to clear)`;
    }
    filterLLMRecords();
}

// ---------------------------------------------------------------------------
// Rendering - Table
// ---------------------------------------------------------------------------

function renderSourceBadges(sourceComponents) {
    if (!sourceComponents || sourceComponents.length === 0) {
        return '<span class="source-badge orchestrator">orchestrator</span>';
    }
    return sourceComponents.map(src =>
        `<span class="source-badge agent">${src}</span>`
    ).join(' ');
}

// Prefer the originating agent (the orchestrator/job that initiated the request)
// for the table Source column. Falls back to the participant badges for records
// written before originating_agent was persisted (no backfill by design).
// Uses the .orchestrator badge style to visually group with the existing
// orchestrator-fallback badge — originator IS an orchestrator (or background job).
function renderSourceCell(record) {
    if (record.originating_agent) {
        return `<span class="source-badge orchestrator">${record.originating_agent}</span>`;
    }
    return renderSourceBadges(record.source_components);
}

function renderLLMTable(list) {
    const tbody = document.getElementById('llmTableBody');
    if (!tbody) return;
    if (list.length === 0) {
        tbody.innerHTML = `<tr><td colspan="7" style="text-align: center; color: var(--text-muted); padding: 48px;">No LLM debug records found</td></tr>`;
        return;
    }
    tbody.innerHTML = list.map(record => {
        // Support both summary format and full format
        const totalTokens = record.total_tokens !== undefined ? record.total_tokens :
            (record.interactions ? record.interactions.reduce((sum, i) => sum + (i.total_tokens || 0), 0) : 0);
        const hasErrors = record.has_errors !== undefined ? record.has_errors :
            (record.interactions ? record.interactions.some(i => !i.success) : false);
        const interactionCount = record.interaction_count !== undefined ? record.interaction_count :
            (record.interactions ? record.interactions.length : 0);
        // Original request ID links related HITL records
        const origReqId = record.original_request_id || record.request_id;
        const isResumed = origReqId !== record.request_id;
        // Check whether this request lineage has linked records.
        const hasLinkedRecords = countLineageRecords(origReqId) > 1;
        return `
        <tr data-request-id="${record.request_id}" class="${selected?.request_id === record.request_id ? 'selected' : ''}">
            <td><span class="request-id" title="${record.request_id}">${record.request_id}</span></td>
            <td>
                <span class="request-id" title="${origReqId}${hasLinkedRecords ? ' (click to filter linked records)' : ''}" style="${hasLinkedRecords ? 'cursor: pointer; text-decoration: underline; text-decoration-style: dotted;' : ''}" ${hasLinkedRecords ? `data-lineage-request-id="${origReqId}"` : ''}>
                    ${isResumed ? '🔗 ' : ''}${origReqId}
                </span>
            </td>
            <td>${renderSourceCell(record)}</td>
            <td>${interactionCount}</td>
            <td>${formatTokens(totalTokens)}</td>
            <td><span class="status-badge ${hasErrors ? 'error' : 'success'}">${hasErrors ? '⚠ Has Errors' : '✓ Success'}</span></td>
            <td><span class="time-ago">${formatTimeAgo(record.created_at)}</span></td>
        </tr>
    `}).join('');
}

// ---------------------------------------------------------------------------
// Record selection
// ---------------------------------------------------------------------------

function cancelDetailLoad() {
    detailLoad.cancel();
    detailPending = false;
    hideLoading(document.getElementById('llmDebugDetailPanel'));
}

function showLLMError(error) {
    const message = document.getElementById('llmErrorMessage');
    if (message) message.textContent = error.message;
    document.getElementById('llmErrorBanner')?.classList.add('visible');
}

async function selectLLMRecord(requestId) {
    requestedSelection = requestId;
    detailPending = true;
    const ticket = detailLoad.begin();
    if (!selected || selected.request_id !== requestId) expandedInteractions.clear();
    const panel = document.getElementById('llmDebugDetailPanel');
    showLoading(panel, 'Loading interactions...');
    try {
        const { data } = await fetchAPI(`/api/llm-debug/${encodeURIComponent(requestId)}`, { signal: ticket.signal });
        if (!detailLoad.current(ticket)) return false;
        selected = data;
        document.getElementById('llmErrorBanner')?.classList.remove('visible');
    } catch (error) {
        if (!detailLoad.current(ticket)) return false;
        selected = null; showLLMError(error);
        filterLLMRecords(); renderLLMDetail();
        return false;
    } finally {
        if (detailLoad.current(ticket)) { detailPending = false; hideLoading(panel); }
    }
    filterLLMRecords(); renderLLMDetail();
    return true;
}

// ---------------------------------------------------------------------------
// Detail tabs
// ---------------------------------------------------------------------------

function setLLMDetailTab(tab) {
    activeTab = tab;
    document.querySelectorAll('#llmDebugDetailPanel .detail-tab').forEach(btn => {
        btn.classList.toggle('active', btn.dataset.tab === tab);
    });
    renderLLMDetail();
}

function renderLLMDetail() {
    const content = document.getElementById('llmDetailContent');
    const title = document.getElementById('llmDetailTitle');
    if (!content || !title) return;
    if (!selected) {
        title.textContent = 'Select a record';
        content.innerHTML = `<div class="empty-detail"><div class="empty-detail-icon">🔍</div><div>Select a record to view LLM interactions</div></div>`;
        return;
    }
    title.textContent = `Request: ${truncateText(selected.request_id, 30)}`;
    if (activeTab === 'json') {
        content.innerHTML = `<div class="json-container"><button class="copy-btn" data-action="copy-llm-json">Copy</button><pre class="json-view">${syntaxHighlightJson(selected)}</pre></div>`;
    } else {
        content.innerHTML = renderLLMInteractionsView(selected);
    }
}

// ---------------------------------------------------------------------------
// Interactions view
// ---------------------------------------------------------------------------

function shortModelName(model) {
    if (!model) return '';
    const map = {
        'claude-sonnet-4-5-20250929': 'Sonnet 4.5',
        'claude-opus-4-6': 'Opus 4.6',
        'claude-haiku-4-5-20251001': 'Haiku 4.5',
        'claude-3-5-sonnet-20241022': 'Sonnet 3.5',
        'claude-3-opus-20240229': 'Opus 3',
        'claude-3-haiku-20240307': 'Haiku 3',
        'gpt-4o': 'GPT-4o',
        'gpt-4o-mini': 'GPT-4o Mini',
        'gpt-5': 'GPT-5',
        'o1': 'o1',
        'o3': 'o3',
        'o4-mini': 'o4 Mini',
    };
    if (map[model]) return map[model];
    // Fallback: shorten common patterns
    if (model.startsWith('claude-')) return model.replace(/^claude-/, '').replace(/-\d{8}$/, '');
    if (model.length > 20) return model.substring(0, 18) + '...';
    return model;
}

function getInteractionNoteState(interaction) {
    const isSuccessful = interaction.success !== false;
    return {
        hasNonFatalNote: isSuccessful && !!interaction.error,
        statusLabel: isSuccessful ? (interaction.error ? '✓ Note' : '✓') : '✗',
    };
}

function renderInteractionTrace(interaction) {
    // A request can include linked background work in a different trace. A
    // missing interaction trace is unknown, not the record's aggregate trace.
    const trace = interaction.trace_id ? escapeHtml(interaction.trace_id) : 'Not recorded';
    return `<div class="token-stats"><div class="token-stat">Interaction trace: <span class="token-stat-value mono">${trace}</span></div></div>`;
}

function renderLLMInteractionsView(record) {
    let html = `<div class="formatted-view">`;

    // Handle case where we only have summary data (no interactions).
    // Filter out non-LLM hook interactions — they have dedicated tabs in DAG view
    // (Pre-Execution, Post-Execution) and are not meaningful in the LLM Debug view.
    const hookTypes = new Set([
        'user_memory_recall_identity', 'user_memory_recall_summary', 'user_memory_recall_query',
        'user_memory_recall_universal', 'user_memory_enrichment_injected',
        'user_memory_embed_candidate', 'user_memory_similarity_search',
        'user_memory_reconciliation_skip',
        'user_memory_remember', 'user_memory_summary_remember',
    ]);
    // Card actions index the original recorded array. Preserve that identity
    // when hiding hooks so Copy and expansion still target the displayed call.
    const visibleInteractions = (record.interactions || [])
        .map((interaction, index) => ({ interaction, index }))
        .filter(({ interaction }) => !hookTypes.has(interaction.type));
    const interactions = visibleInteractions.map(({ interaction }) => interaction);

    // Extract unique models and providers from interactions
    const models = [...new Set(interactions.map(i => i.model).filter(Boolean))];
    const providers = [...new Set(interactions.map(i => i.provider).filter(Boolean))];

    // Interaction count - support both summary and full format
    const interactionCount = record.interaction_count !== undefined ? record.interaction_count : interactions.length;

    // Total input/output tokens across all interactions in this record.
    // Mirrors the per-interaction display in the DAG view's LLM Calls subsection.
    const totalPromptTokens = interactions.reduce((sum, i) => sum + (i.prompt_tokens || 0), 0);
    const totalCompletionTokens = interactions.reduce((sum, i) => sum + (i.completion_tokens || 0), 0);

    // Determine if this is a resumed request
    const origReqId = record.original_request_id || record.request_id;
    const isResumed = origReqId !== record.request_id;
    // Check whether this root request has linked records.
    const hasLinkedRecords = countLineageRecords(origReqId) > 1;
    const lineageAttribute = escapeHtml(origReqId).replaceAll('"', '&quot;');
    const executionURL = `?${new URLSearchParams({ view: 'dag', request_id: record.request_id })}`;

    // Record Info
    html += `<div class="info-section"><div class="info-section-title"><span class="section-icon">📊</span> Record Info</div><div class="info-grid">
        <div class="info-label">Request ID</div><div class="info-value mono">${escapeHtml(record.request_id)}</div>
        <div class="info-label">Root request ID</div><div class="info-value mono" style="display: flex; align-items: center; gap: 8px;">
            ${isResumed ? '<span title="This is a resumed HITL request">🔗</span>' : ''}
            <span style="${hasLinkedRecords ? 'cursor: pointer; text-decoration: underline; text-decoration-style: dotted;' : ''}" ${hasLinkedRecords ? `data-lineage-request-id="${lineageAttribute}" title="Click to filter linked records (${countLineageRecords(origReqId)} total)"` : ''}>${escapeHtml(origReqId)}</span>
            ${isResumed ? '<span style="font-size: 11px; color: var(--text-muted);">(HITL resume)</span>' : ''}
        </div>
        ${record.trace_id ? `<div class="info-label">Debug record trace</div><div class="info-value"><span class="mono">${escapeHtml(record.trace_id)}</span><div class="text-muted">Record-level reference; individual interactions may use other traces.</div></div>` : ''}
        <div class="info-label">Execution</div><div class="info-value"><a class="dag-open-timeline-btn" href="${escapeHtml(executionURL)}">Open execution DAG</a></div>
        <div class="info-label">Created</div><div class="info-value">${formatDateTime(record.created_at)}</div>
        <div class="info-label">Interactions</div><div class="info-value">${interactionCount}</div>
        ${interactions.length > 0 ? `<div class="info-label">Input Tokens</div><div class="info-value">${formatTokens(totalPromptTokens)}</div>` : ''}
        ${interactions.length > 0 ? `<div class="info-label">Output Tokens</div><div class="info-value">${formatTokens(totalCompletionTokens)}</div>` : ''}
        ${models.length > 0 ? `<div class="info-label">Model${models.length > 1 ? 's' : ''}</div><div class="info-value">${models.join(', ')}</div>` : ''}
        ${providers.length > 0 ? `<div class="info-label">Provider${providers.length > 1 ? 's' : ''}</div><div class="info-value">${providers.join(', ')}</div>` : ''}
        ${(() => { const rc = interactions.filter(i => isRetryType(i.type)).length; return rc > 0 ? `<div class="info-label">Regenerations</div><div class="info-value"><span style="background: rgba(255,140,50,0.15); border: 1px solid rgba(255,140,50,0.3); color: #ff8c32; padding: 2px 8px; border-radius: 4px; font-size: 11px;">⚠️ ${rc} regeneration${rc > 1 ? 's' : ''}</span></div>` : ''; })()}
    </div></div>`;

    // Show message if no interactions available
    if (interactions.length === 0) {
        html += `<div class="info-section">
            <div class="info-section-title"><span class="section-icon">💬</span> LLM Interactions</div>
            <div style="padding: 24px; text-align: center; color: var(--text-muted);">
                Loading interaction details...
            </div>
        </div></div>`;
        return html;
    }

    // Interactions
    html += `<div class="info-section">
        <div class="info-section-title" style="display: flex; justify-content: space-between; align-items: center;">
            <span><span class="section-icon">💬</span> LLM Interactions (${interactions.length})</span>
            <span style="display: flex; gap: 8px;">
                <button class="expand-btn" data-action="expand-all">Expand All</button>
                <button class="expand-btn" data-action="collapse-all">Collapse All</button>
            </span>
        </div>`;

    visibleInteractions.forEach(({ interaction, index }) => {
        const promptPreview = truncateText(interaction.prompt, 80);
        const isExpanded = expandedInteractions.has(index);
        const noteState = getInteractionNoteState(interaction);
        // Label, palette, and CSS-badge custom-properties all come from
        // the LLM-type registry. List palette (orange for retry types,
        // green for plan_generation, etc.) is preserved by getListStyledColors.
        const _rgb = getListStyledColors(interaction.type).rgb;
        const _accent = getListStyledColors(interaction.type).accent;
        const _baseShadow = `0 0 16px rgba(${_rgb}, 0.12), 0 0 5px rgba(${_rgb}, 0.2), 0 4px 20px rgba(0,0,0,0.2), inset 0 1px 0 rgba(255,255,255,0.08)`;
        const _hoverShadow = `0 0 28px rgba(${_rgb}, 0.2), 0 0 10px rgba(${_rgb}, 0.38), 0 8px 32px rgba(0,0,0,0.28), inset 0 1px 0 rgba(255,255,255,0.14)`;
        const interactionLabel = getListLabel(interaction.type);
        // Per-type CSS custom properties for the .type-badge rule. Set
        // inline on the badge element so the base CSS rule reads them
        // (see static/css/layout.css `.type-badge { background: rgba(var(--badge-rgb)…) }`).
        const _badgeStyle = `--badge-rgb: ${_rgb}; --badge-color: ${_accent};`;
        if ((interaction.category || 'llm') !== 'llm') {
            // Non-LLM (hook / memory) cards collapse by default and respect
            // expandedInteractions like the LLM cards do — driven by the
            // .expanded class plus the CSS rule .interaction-card.expanded
            // .interaction-body { display: block; }. Earlier this body had an
            // inline `style="display: block;"` that overrode the CSS,
            // forcing these cards always-expanded.
            html += `<div class="interaction-card csp-hover-card${isExpanded ? ' expanded' : ''}" data-index="${index}" id="interaction-${index}" style="--card-border: rgba(${_rgb}, 0.22); --card-hover-border: rgba(${_rgb}, 0.38); --card-shadow: ${_baseShadow}; --card-hover-shadow: ${_hoverShadow}; border-color: var(--active-card-border); box-shadow: var(--active-card-shadow);">
                <div class="interaction-header">
                    <div class="interaction-type-row">
                        <span class="type-badge ${interaction.type}" style="${_badgeStyle}">${interactionLabel}</span>
                        <span class="status-badge ${interaction.success ? 'success' : 'error'}">${noteState.statusLabel}</span>
                        <span class="interaction-preview">${escapeHtml(interaction.response || interaction.call_description || '')}</span>
                    </div>
                    <div class="interaction-meta">
                        <div class="interaction-meta-item col-model" style="color: rgba(180, 160, 210, 0.8); font-size: 10.5px;">Logic</div>
                        <div class="interaction-meta-item col-time" style="color: rgba(130, 200, 180, 0.75); font-size: 10.5px;">${formatDuration(interaction.duration_ms)}</div>
                        <div class="interaction-meta-item col-tokens"></div>
                        <span class="expand-indicator">▼</span>
                    </div>
                </div>
                <div class="interaction-body">
                    ${renderInteractionTrace(interaction)}
                    <div class="prompt-section">
                        <div class="prompt-label">ℹ Summary</div>
                        <div class="prompt-content">${escapeHtml(interaction.response || interaction.call_description || '')}</div>
                    </div>
                </div>
            </div>`;
            return;
        }
        html += `<div class="interaction-card csp-hover-card${isExpanded ? ' expanded' : ''}" data-index="${index}" id="interaction-${index}" style="--card-border: rgba(${_rgb}, 0.25); --card-hover-border: rgba(${_rgb}, 0.4); --card-shadow: ${_baseShadow}; --card-hover-shadow: ${_hoverShadow}; border-color: var(--active-card-border); box-shadow: var(--active-card-shadow);">
            <div class="interaction-header">
                <div class="interaction-type-row">
                    <span class="type-badge ${interaction.type}" style="${_badgeStyle}">${interactionLabel}</span>
                    ${interaction.source_component ? `<span class="type-badge" style="background: rgba(160,100,240,0.15); color: #a064f0; border: 1px solid rgba(160,100,240,0.3); font-size: 9px; padding: 1px 6px;">🔧 ${interaction.source_component}</span>` : ''}
                    ${interaction.call_description ? (
                        isRetryType(interaction.type)
                            ? `<div style="background: rgba(255,140,50,0.1); border: 1px solid rgba(255,140,50,0.3); border-radius: 4px; padding: 3px 8px; margin-top: 4px; font-size: 10px; color: #ff8c32;">⚠️ ${escapeHtml(interaction.call_description)}</div>`
                            : `<span style="color: var(--text-muted); font-size: 10px; font-style: italic; margin-left: 4px;">${escapeHtml(interaction.call_description)}</span>`
                    ) : ''}
                    <span class="status-badge ${interaction.success ? 'success' : 'error'}">${noteState.statusLabel}</span>
                    <span class="interaction-preview">${escapeHtml(promptPreview)}</span>
                </div>
                <div class="interaction-meta">
                    <div class="interaction-meta-item col-step">${interaction.step_id ? `<span class="step-id-badge" title="Associated with step">📍 ${interaction.step_id}</span>` : ''}</div>
                    <div class="interaction-meta-item col-model" title="${interaction.model ? escapeHtml(interaction.model) : ''}" style="color: rgba(180, 160, 210, 0.8); font-size: 10.5px;">${interaction.model ? shortModelName(interaction.model) : ''}</div>
                    <div class="interaction-meta-item col-time" style="color: rgba(130, 200, 180, 0.75); font-size: 10.5px;">${formatDuration(interaction.duration_ms)}</div>
                    <div class="interaction-meta-item col-tokens" title="Input / Output tokens" style="color: rgba(170, 185, 220, 0.75); font-size: 10.5px;">${interaction.total_tokens ? `${formatTokens(interaction.prompt_tokens || 0)} / ${formatTokens(interaction.completion_tokens || 0)}` : ''}</div>
                    ${interaction.attempt > 1 ? `<div class="interaction-meta-item" style="color: rgba(220, 170, 140, 0.75); font-size: 10.5px;">#${interaction.attempt}</div>` : ''}
                    <span class="expand-indicator">▼</span>
                </div>
            </div>
            <div class="interaction-body">
                ${renderInteractionTrace(interaction)}
                ${interaction.type === 'result_distillation' && interaction.call_description ? `
                <div style="margin-bottom: 10px; padding: 8px 12px; background: rgba(130,90,220,0.08); border: 1px solid rgba(130,90,220,0.25); border-radius: 8px; font-size: 11px;">
                    <span style="color: rgba(130,90,220,0.9); font-weight: 600;">🔬 Two-Stage Pipeline:</span>
                    <span style="color: var(--text-muted); margin-left: 6px;">${escapeHtml(interaction.call_description)}</span>
                </div>
                ` : ''}
                ${getLLMPromptSections(interaction).map((section) => `
                <div class="prompt-section">
                    <div class="prompt-label">${section.icon} ${section.label} <button class="copy-inline-btn" data-copy-index="${index}" data-copy-type="${section.type}">Copy</button></div>
                    <div class="prompt-content">${escapeHtml(section.text)}</div>
                </div>
                `).join('')}

                ${interaction.success ? `
                <div class="response-section">
                    <div class="response-label">📤 Response <button class="copy-inline-btn" data-copy-index="${index}" data-copy-type="response">Copy</button></div>
                    <div class="response-content">${escapeHtml((interaction.response || '').replace(/^\s*\`\`\`(?:json)?\s*\n?/, '').replace(/\n?\s*\`\`\`\s*$/, ''))}</div>
                </div>
                ${noteState.hasNonFatalNote ? `
                <div class="response-section">
                    <div class="response-label">ℹ Note</div>
                    <div class="prompt-content" style="border-color: rgba(255,179,64,0.25); background: rgba(255,179,64,0.08); color: #ffb340;">${escapeHtml(interaction.error)}</div>
                </div>
                ` : ''}

                <div class="token-stats">
                    ${interaction.model ? `<div class="token-stat">Model: <span class="token-stat-value">${interaction.model}</span></div>` : ''}
                    ${interaction.provider ? (() => {
                        const provider = interaction.provider.toLowerCase();
                        const providerColors = {
                            'openai': { bg: 'rgba(16, 163, 127, 0.2)', border: 'rgba(16, 163, 127, 0.4)', color: '#10a37f' },
                            'anthropic': { bg: 'rgba(204, 153, 102, 0.2)', border: 'rgba(204, 153, 102, 0.4)', color: '#cc9966' },
                            'groq': { bg: 'rgba(255, 140, 0, 0.2)', border: 'rgba(255, 140, 0, 0.4)', color: '#ff8c00' },
                            'gemini': { bg: 'rgba(66, 133, 244, 0.2)', border: 'rgba(66, 133, 244, 0.4)', color: '#4285f4' },
                            'deepseek': { bg: 'rgba(0, 122, 255, 0.2)', border: 'rgba(0, 122, 255, 0.4)', color: '#007aff' },
                            'default': { bg: 'rgba(150, 150, 150, 0.2)', border: 'rgba(150, 150, 150, 0.4)', color: '#aaaaaa' }
                        };
                        const colors = providerColors[provider] || providerColors['default'];
                        return `<div class="token-stat">Provider: <span style="
                            display: inline-block;
                            padding: 2px 10px;
                            background: linear-gradient(135deg, ${colors.bg} 0%, rgba(255,255,255,0.05) 100%);
                            border: 1px solid ${colors.border};
                            border-radius: 4px;
                            font-size: 11px;
                            font-weight: 600;
                            color: ${colors.color};
                            letter-spacing: 0.3px;
                        ">${interaction.provider}</span></div>`;
                    })() : ''}
                    <div class="token-stat">Prompt: <span class="token-stat-value">${interaction.prompt_tokens || 0}</span></div>
                    <div class="token-stat">Completion: <span class="token-stat-value">${interaction.completion_tokens || 0}</span></div>
                    <div class="token-stat">Total: <span class="token-stat-value">${interaction.total_tokens || 0}</span></div>
                </div>
                ` : `
                <div class="response-section">
                    <div class="response-label">❌ Error</div>
                    <div class="prompt-content error-content">${escapeHtml(interaction.error || 'Unknown error')}</div>
                </div>
                `}
            </div>
        </div>`;
    });

    html += `</div></div>`;
    return html;
}

// ---------------------------------------------------------------------------
// Interaction expand / collapse
// ---------------------------------------------------------------------------

function toggleInteraction(index) {
    const card = document.getElementById(`interaction-${index}`);
    if (card) {
        card.classList.toggle('expanded');
        if (card.classList.contains('expanded')) {
            expandedInteractions.add(index);
        } else {
            expandedInteractions.delete(index);
        }
    }
}

function expandAllInteractions() {
    document.querySelectorAll('.interaction-card').forEach((card) => {
        card.classList.add('expanded');
        const idx = parseInt(card.dataset.index, 10);
        if (!isNaN(idx)) expandedInteractions.add(idx);
    });
}

function collapseAllInteractions() {
    document.querySelectorAll('.interaction-card').forEach(card => {
        card.classList.remove('expanded');
    });
    expandedInteractions.clear();
}

// ---------------------------------------------------------------------------
// Copy helpers
// ---------------------------------------------------------------------------

function copyLLMJson(evt) {
    const text = JSON.stringify(selected, null, 2);
    copyToClipboard(text, evt);
}

function copyLLMInteractionContent(index, type, evt) {
    if (!selected?.interactions?.[index]) return;
    const interaction = selected.interactions[index];
    const text = getLLMInteractionContent(interaction, type);
    copyTextWithFeedback(text, evt?.target || evt);
}

function execCopyFallback(text) {
    const textarea = document.createElement('textarea');
    textarea.value = text;
    textarea.style.cssText = 'position:fixed;left:-9999px;top:-9999px;opacity:0';
    document.body.appendChild(textarea);
    textarea.focus();
    textarea.select();
    let ok = false;
    try { ok = document.execCommand('copy'); } catch (_) { /* swallow */ }
    document.body.removeChild(textarea);
    return ok ? Promise.resolve() : Promise.reject(new Error('execCommand copy failed'));
}

function writeClipboard(text) {
    // Try the modern Clipboard API first; if it rejects (permissions,
    // insecure context, browser quirks), fall back to execCommand.
    if (navigator.clipboard) {
        return navigator.clipboard.writeText(text).catch(() => execCopyFallback(text));
    }
    return execCopyFallback(text);
}

function copyTextWithFeedback(text, btn) {
    if (btn == null) return;
    if (!text) { text = ''; } // allow copying empty strings without silent bail
    writeClipboard(text).then(() => {
        const originalText = btn.textContent;
        btn.textContent = 'Copied!';
        btn.classList.add('copied');
        setTimeout(() => {
            btn.textContent = originalText;
            btn.classList.remove('copied');
        }, 1500);
    }).catch(err => {
        console.error('Copy failed:', err);
        btn.textContent = 'Failed';
        setTimeout(() => { btn.textContent = 'Copy'; }, 1500);
    });
}

// ---------------------------------------------------------------------------
// Data fetching
// ---------------------------------------------------------------------------

async function fetchLLMRecords() {
    const ticket = listLoad.begin();
    const refreshBtn = document.querySelector('.refresh-btn');
    const listPanel = document.getElementById('llmDebugListPanel');
    refreshBtn?.classList.add('loading');
    showLoading(listPanel, 'Loading LLM records...');
    try {
        const { data } = await fetchAPI('/api/llm-debug', { signal: ticket.signal });
        if (!listLoad.current(ticket)) return;
        records = data.records.sort((a, b) => new Date(b.created_at) - new Date(a.created_at));
        const total = records.reduce((sum, r) => sum + (r.total_tokens ?? r.interactions?.reduce((n, i) => n + (i.total_tokens || 0), 0) ?? 0), 0);
        const count = document.getElementById('recordCount');
        const tokens = document.getElementById('tokenCount');
        if (count) count.textContent = records.length;
        if (tokens) tokens.textContent = formatTokens(total);
        filterLLMRecords();
        let detailOK = true;
        const wanted = requestedSelection ?? selected?.request_id;
        if (wanted) {
            if (!records.some(r => r.request_id === wanted)) {
                cancelDetailLoad(); requestedSelection = null;
                selected = null; renderLLMDetail();
            } else if (!detailPending) {
                detailOK = await selectLLMRecord(wanted);
            }
        }
        if (!listLoad.current(ticket)) return;
        const updated = document.getElementById('llmLastUpdated');
        if (updated) updated.textContent = `Last updated: ${formatDateTime(new Date())}`;
        if (detailOK) document.getElementById('llmErrorBanner')?.classList.remove('visible');
    } catch (error) {
        if (!listLoad.current(ticket)) return;
        cancelDetailLoad(); requestedSelection = null; records = []; selected = null;
        for (const id of ['recordCount', 'tokenCount']) {
            const element = document.getElementById(id);
            if (element) element.textContent = 'Unavailable';
        }
        filterLLMRecords(); renderLLMDetail(); showLLMError(error);
    } finally {
        if (listLoad.current(ticket)) { refreshBtn?.classList.remove('loading'); hideLoading(listPanel); }
    }
}
