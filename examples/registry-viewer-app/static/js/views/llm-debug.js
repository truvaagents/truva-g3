/**
 * LLM Debug View.
 *
 * Displays LLM interaction records with filtering, detail views,
 * and conversation grouping. Falls back to mock data when the API
 * is unavailable (e.g., local development without a running backend).
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
import { showLoading, hideLoading } from '../utils/dom.js';
import {
    isRetryType,
    getListLabel,
    getListStyledColors,
} from '../llm-types.js';

// ---------------------------------------------------------------------------
// Module state
// ---------------------------------------------------------------------------

let records = [];
let selected = null;
let activeTab = 'interactions';
let typeFilter = 'all';
let expandedInteractions = new Set();
let conversationFilter = null;

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
    expandedInteractions = new Set();
    conversationFilter = null;
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
            // Check for conversation filter click first
            const convEl = e.target.closest('[data-conversation-id]');
            if (convEl) {
                e.stopPropagation();
                filterByConversation(convEl.dataset.conversationId);
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

            // Conversation filter link in detail view
            const convLink = e.target.closest('[data-conversation-id]');
            if (convLink) {
                filterByConversation(convLink.dataset.conversationId);
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

        // Check conversation filter first
        if (conversationFilter && origReqId !== conversationFilter) {
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

function countLinkedRecords(conversationId) {
    return records.filter(r => {
        const origReqId = r.original_request_id || r.request_id;
        return origReqId === conversationId;
    }).length;
}

function filterByConversation(conversationId) {
    // Don't allow filtering if there's only one record with this conversation ID
    const linkedCount = countLinkedRecords(conversationId);
    if (linkedCount <= 1 && conversationFilter !== conversationId) {
        return;
    }

    if (conversationFilter === conversationId) {
        // Toggle off - clear the filter
        conversationFilter = null;
        const input = document.getElementById('llmSearchInput');
        if (input) input.placeholder = 'Search by request ID, conversation, source agent, or type...';
    } else {
        conversationFilter = conversationId;
        const input = document.getElementById('llmSearchInput');
        if (input) input.placeholder = `Filtering by conversation: ${truncateText(conversationId, 16)} (click again to clear)`;
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
        // Check if this conversation has linked records (makes it clickable)
        const hasLinkedRecords = countLinkedRecords(origReqId) > 1;
        return `
        <tr data-request-id="${record.request_id}" class="${selected?.request_id === record.request_id ? 'selected' : ''}">
            <td><span class="request-id" title="${record.request_id}">${record.request_id}</span></td>
            <td>
                <span class="request-id" title="${origReqId}${hasLinkedRecords ? ' (click to filter linked records)' : ''}" style="${hasLinkedRecords ? 'cursor: pointer; text-decoration: underline; text-decoration-style: dotted;' : ''}" ${hasLinkedRecords ? `data-conversation-id="${origReqId}"` : ''}>
                    ${isResumed ? '🔗 ' : ''}${origReqId}
                </span>
            </td>
            <td>${renderSourceBadges(record.source_components)}</td>
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

async function selectLLMRecord(requestId) {
    // Clear expanded state when switching to a different record
    if (!selected || selected.request_id !== requestId) {
        expandedInteractions.clear();
    }

    const detailPanel = document.getElementById('llmDebugDetailPanel');

    // Check if we already have the full record (with interactions)
    const summaryRecord = records.find(r => r.request_id === requestId);
    if (summaryRecord && summaryRecord.interactions) {
        // Already have full record (from mock data)
        selected = summaryRecord;
    } else {
        // Need to fetch full record from API
        showLoading(detailPanel, 'Loading interactions...');
        try {
            const { data } = await fetchAPI(`/api/llm-debug/${encodeURIComponent(requestId)}`);
            selected = data;
        } catch (error) {
            console.error('Failed to fetch LLM record:', error);
            // Fallback: use summary record or mock data
            const mockRecord = getMockLLMDebugRecord(requestId);
            selected = summaryRecord || mockRecord || null;
        } finally {
            hideLoading(detailPanel);
        }
    }

    filterLLMRecords();
    renderLLMDetail();
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
    const interactions = (record.interactions || []).filter(i => !hookTypes.has(i.type));

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
    // Check if this conversation has linked records
    const hasLinkedRecords = countLinkedRecords(origReqId) > 1;

    // Record Info
    html += `<div class="info-section"><div class="info-section-title"><span class="section-icon">📊</span> Record Info</div><div class="info-grid">
        <div class="info-label">Request ID</div><div class="info-value mono">${record.request_id}</div>
        <div class="info-label">Conversation ID</div><div class="info-value mono" style="display: flex; align-items: center; gap: 8px;">
            ${isResumed ? '<span title="This is a resumed HITL request">🔗</span>' : ''}
            <span style="${hasLinkedRecords ? 'cursor: pointer; text-decoration: underline; text-decoration-style: dotted;' : ''}" ${hasLinkedRecords ? `data-conversation-id="${origReqId}" title="Click to filter linked records (${countLinkedRecords(origReqId)} total)"` : ''}>${origReqId}</span>
            ${isResumed ? '<span style="font-size: 11px; color: var(--text-muted);">(HITL resume)</span>' : ''}
        </div>
        ${record.trace_id ? `<div class="info-label">Trace ID</div><div class="info-value mono">${record.trace_id}</div>` : ''}
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

    interactions.forEach((interaction, index) => {
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
            html += `<div class="interaction-card${isExpanded ? ' expanded' : ''}" data-index="${index}" id="interaction-${index}" style="border-color: rgba(${_rgb}, 0.22); box-shadow: ${_baseShadow};" onmouseenter="this.style.boxShadow='${_hoverShadow}'; this.style.borderColor='rgba(${_rgb}, 0.38)';" onmouseleave="this.style.boxShadow='${_baseShadow}'; this.style.borderColor='rgba(${_rgb}, 0.22)';">
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
                    <div class="prompt-section">
                        <div class="prompt-label">ℹ Summary</div>
                        <div class="prompt-content">${escapeHtml(interaction.response || interaction.call_description || '')}</div>
                    </div>
                </div>
            </div>`;
            return;
        }
        html += `<div class="interaction-card${isExpanded ? ' expanded' : ''}" data-index="${index}" id="interaction-${index}" style="border-color: rgba(${_rgb}, 0.25); box-shadow: ${_baseShadow};" onmouseenter="this.style.boxShadow='${_hoverShadow}'; this.style.borderColor='rgba(${_rgb}, 0.4)';" onmouseleave="this.style.boxShadow='${_baseShadow}'; this.style.borderColor='rgba(${_rgb}, 0.25)';">
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
                ${interaction.type === 'result_distillation' && interaction.call_description ? `
                <div style="margin-bottom: 10px; padding: 8px 12px; background: rgba(130,90,220,0.08); border: 1px solid rgba(130,90,220,0.25); border-radius: 8px; font-size: 11px;">
                    <span style="color: rgba(130,90,220,0.9); font-weight: 600;">🔬 Two-Stage Pipeline:</span>
                    <span style="color: var(--text-muted); margin-left: 6px;">${escapeHtml(interaction.call_description)}</span>
                </div>
                ` : ''}
                <div class="prompt-section">
                    <div class="prompt-label">📥 Prompt <button class="copy-inline-btn" data-copy-index="${index}" data-copy-type="prompt">Copy</button></div>
                    <div class="prompt-content">${escapeHtml(interaction.prompt)}</div>
                </div>

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
    const text = type === 'prompt' ? interaction.prompt : interaction.response;
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
    const refreshBtn = document.querySelector('.refresh-btn');
    const errorBanner = document.getElementById('llmErrorBanner');
    const listPanel = document.getElementById('llmDebugListPanel');
    if (refreshBtn) refreshBtn.classList.add('loading');
    showLoading(listPanel, 'Loading LLM records...');

    try {
        // Try to fetch from API, fallback to mock data
        let data;
        let isFromAPI = false;
        try {
            const response = await fetch('/api/llm-debug');
            if (response.ok) {
                data = await response.json();
                isFromAPI = true;
            } else {
                throw new Error('API not available');
            }
        } catch {
            // Use mock data for development
            data = getMockLLMDebugData();
        }

        // Calculate total tokens - handle both summary format and full format
        const totalTokens = data.records.reduce((sum, r) => {
            // Summary format has total_tokens directly
            if (r.total_tokens !== undefined) return sum + r.total_tokens;
            // Full format needs to sum from interactions
            if (r.interactions) return sum + r.interactions.reduce((s, i) => s + (i.total_tokens || 0), 0);
            return sum;
        }, 0);

        const recordCountEl = document.getElementById('recordCount');
        const tokenCountEl = document.getElementById('tokenCount');
        if (recordCountEl) recordCountEl.textContent = data.records.length;
        if (tokenCountEl) tokenCountEl.textContent = formatTokens(totalTokens);

        records = data.records.sort((a, b) =>
            new Date(b.created_at) - new Date(a.created_at)
        );

        filterLLMRecords();

        // If we have a selected record, refresh its data
        if (selected && isFromAPI) {
            // Re-fetch full record to get latest data
            await selectLLMRecord(selected.request_id);
        } else if (selected) {
            const updated = records.find(r => r.request_id === selected.request_id);
            if (updated) { selected = updated; renderLLMDetail(); }
        }

        const lastUpdatedEl = document.getElementById('llmLastUpdated');
        if (lastUpdatedEl) lastUpdatedEl.textContent = `Last updated: ${formatDateTime(new Date())}`;
        if (errorBanner) errorBanner.classList.remove('visible');
    } catch (error) {
        console.error('Failed to fetch LLM records:', error);
        const msgEl = document.getElementById('llmErrorMessage');
        if (msgEl) msgEl.textContent = error.message;
        if (errorBanner) errorBanner.classList.add('visible');
    } finally {
        if (refreshBtn) refreshBtn.classList.remove('loading');
        hideLoading(listPanel);
    }
}

// ---------------------------------------------------------------------------
// Mock data (used when the API is unavailable)
// ---------------------------------------------------------------------------

function getMockLLMDebugRecord(requestId) {
    const mockData = getMockLLMDebugData();
    return mockData.records.find(r => r.request_id === requestId) || null;
}

function getMockLLMDebugData() {
    const now = new Date();
    return {
        records: [
            {
                request_id: "debug-1736621234567-1",
                original_request_id: "debug-1736621234567-1",
                trace_id: "369fecb4e3156c34e0950c61f1f99d62",
                created_at: new Date(now - 60000).toISOString(),
                updated_at: new Date(now - 58000).toISOString(),
                interactions: [
                    {
                        type: "plan_generation",
                        timestamp: new Date(now - 60000).toISOString(),
                        duration_ms: 1523,
                        prompt: `You are an intelligent orchestrator for the TruvaG3 framework.\n\nGiven the user request and available capabilities, generate an execution plan.\n\nUSER REQUEST: "What's the weather like in Tokyo and what are some good restaurants there?"\n\nAVAILABLE CAPABILITIES:\n1. weather-service.get_weather - Get current weather for a location\n   Parameters: location (string, required)\n\n2. restaurant-finder.search_restaurants - Find restaurants in a city\n   Parameters: city (string, required), cuisine (string, optional)\n\nGenerate a JSON execution plan with steps to fulfill this request.`,
                        response: `{\n  "routing_plan": {\n    "steps": [\n      {\n        "step_id": "step-1",\n        "capability": "weather-service.get_weather",\n        "parameters": {"location": "Tokyo, Japan"},\n        "depends_on": []\n      },\n      {\n        "step_id": "step-2",\n        "capability": "restaurant-finder.search_restaurants",\n        "parameters": {"city": "Tokyo"},\n        "depends_on": []\n      }\n    ],\n    "synthesis_strategy": "llm"\n  }\n}`,
                        temperature: 0.3,
                        max_tokens: 2000,
                        model: "gpt-4o-mini",
                        provider: "openai",
                        prompt_tokens: 247,
                        completion_tokens: 156,
                        total_tokens: 403,
                        success: true,
                        attempt: 1
                    },
                    {
                        type: "synthesis",
                        timestamp: new Date(now - 58000).toISOString(),
                        duration_ms: 892,
                        prompt: `Synthesize the following tool results into a coherent, user-friendly response.\n\nUSER QUERY: "What's the weather like in Tokyo and what are some good restaurants there?"\n\nTOOL RESULTS:\n1. weather-service.get_weather:\n   {"temperature": 22, "condition": "Partly Cloudy", "humidity": 65}\n\n2. restaurant-finder.search_restaurants:\n   {"restaurants": [{"name": "Sukiyabashi Jiro", "cuisine": "Sushi", "rating": 4.9}, {"name": "Narisawa", "cuisine": "French-Japanese", "rating": 4.8}]}\n\nCreate a natural, helpful response that combines this information.`,
                        response: `Based on the current conditions in Tokyo:\n\n**Weather in Tokyo:**\nThe weather is pleasant at 22°C (72°F) with partly cloudy skies and 65% humidity - perfect weather for exploring the city!\n\n**Top Restaurant Recommendations:**\nHere are some excellent dining options:\n\n1. **Sukiyabashi Jiro** ⭐ 4.9\n   - Cuisine: Sushi\n   - World-renowned sushi restaurant featured in "Jiro Dreams of Sushi"\n\n2. **Narisawa** ⭐ 4.8\n   - Cuisine: French-Japanese Fusion\n   - Innovative dining experience blending French techniques with Japanese ingredients\n\nBoth restaurants are highly acclaimed and would make for a memorable dining experience during your visit!`,
                        temperature: 0.5,
                        max_tokens: 1500,
                        model: "gpt-4o-mini",
                        provider: "openai",
                        prompt_tokens: 312,
                        completion_tokens: 198,
                        total_tokens: 510,
                        success: true,
                        attempt: 1
                    }
                ]
            },
            {
                request_id: "debug-1736621200000-2",
                original_request_id: "debug-1736621234567-1",
                trace_id: "abc123def456789012345678901234ab",
                created_at: new Date(now - 300000).toISOString(),
                updated_at: new Date(now - 298000).toISOString(),
                interactions: [
                    {
                        type: "plan_generation",
                        timestamp: new Date(now - 300000).toISOString(),
                        duration_ms: 1234,
                        prompt: `You are an intelligent orchestrator for the TruvaG3 framework.\n\nUSER REQUEST: "Convert 100 USD to EUR"\n\nAVAILABLE CAPABILITIES:\n1. currency-tool.convert - Convert between currencies\n   Parameters: amount (number, required), from (string, required), to (string, required)\n\nGenerate a JSON execution plan.`,
                        response: `{\n  "routing_plan": {\n    "steps": [\n      {\n        "step_id": "step-1",\n        "capability": "currency-tool.convert",\n        "parameters": {"amount": 100, "from": "USD", "to": "EUR"},\n        "depends_on": []\n      }\n    ],\n    "synthesis_strategy": "simple"\n  }\n}`,
                        temperature: 0.3,
                        max_tokens: 2000,
                        model: "gpt-4o-mini",
                        provider: "openai",
                        prompt_tokens: 156,
                        completion_tokens: 89,
                        total_tokens: 245,
                        success: true,
                        attempt: 1
                    }
                ]
            },
            {
                request_id: "debug-1736620900000-3",
                original_request_id: "debug-1736620900000-3",
                trace_id: "error123trace456",
                created_at: new Date(now - 600000).toISOString(),
                updated_at: new Date(now - 598000).toISOString(),
                interactions: [
                    {
                        type: "plan_generation",
                        timestamp: new Date(now - 600000).toISOString(),
                        duration_ms: 2100,
                        prompt: `You are an intelligent orchestrator...\n\nUSER REQUEST: "Book a flight to Mars"\n\nAVAILABLE CAPABILITIES:\n1. flight-search.search - Search for flights\n   Parameters: from (string), to (string), date (string)`,
                        success: false,
                        error: "LLM rate limit exceeded. Please retry after 60 seconds.",
                        attempt: 1
                    },
                    {
                        type: "plan_generation",
                        timestamp: new Date(now - 598000).toISOString(),
                        duration_ms: 1456,
                        prompt: `You are an intelligent orchestrator...\n\nUSER REQUEST: "Book a flight to Mars"\n\nAVAILABLE CAPABILITIES:\n1. flight-search.search - Search for flights\n   Parameters: from (string), to (string), date (string)`,
                        response: `{\n  "routing_plan": {\n    "steps": [],\n    "error": "No capability available for interplanetary travel"\n  }\n}`,
                        temperature: 0.3,
                        max_tokens: 2000,
                        model: "gpt-4o",
                        provider: "openai",
                        prompt_tokens: 134,
                        completion_tokens: 45,
                        total_tokens: 179,
                        success: true,
                        attempt: 2
                    }
                ]
            },
            {
                request_id: "micro-1736620500000-4",
                original_request_id: "debug-1736620900000-3",
                source_components: ["research-assistant"],
                created_at: new Date(now - 900000).toISOString(),
                updated_at: new Date(now - 898000).toISOString(),
                interactions: [
                    {
                        type: "micro_resolution",
                        source_component: "research-assistant",
                        timestamp: new Date(now - 900000).toISOString(),
                        duration_ms: 456,
                        prompt: `Extract the "city" parameter for the restaurant-finder.search_restaurants capability.\n\nSOURCE DATA:\n{\n  "weather_result": {"location": "Paris, France", "temp": 18}\n}\n\nUSER QUERY: "Find restaurants near where I checked the weather"\n\nThe city parameter should be extracted from the context. Return JSON with the resolved value.`,
                        response: `{"city": "Paris"}`,
                        temperature: 0.0,
                        max_tokens: 500,
                        model: "gpt-4o-mini",
                        provider: "openai",
                        prompt_tokens: 89,
                        completion_tokens: 12,
                        total_tokens: 101,
                        success: true,
                        attempt: 1,
                        step_id: "step-2"
                    }
                ]
            },
            {
                request_id: "reresolver-1736620000000-5",
                original_request_id: "reresolver-1736620000000-5",
                source_components: ["travel-agent", "research-assistant"],
                created_at: new Date(now - 1200000).toISOString(),
                updated_at: new Date(now - 1198000).toISOString(),
                interactions: [
                    {
                        type: "semantic_retry",
                        source_component: "travel-agent",
                        timestamp: new Date(now - 1200000).toISOString(),
                        duration_ms: 789,
                        prompt: `TASK: Re-resolve parameters after execution failure\n\nUSER REQUEST: "Get stock price for Apple"\n\nSOURCE DATA FROM PREVIOUS STEPS:\n{}\n\nFAILED ATTEMPT:\n- Capability: stock-market-tool.get_price\n- Parameters sent: {"symbol": "Apple"}\n- Error received: "Invalid symbol format. Expected ticker symbol like 'AAPL'"\n- HTTP Status: 400\n\nTARGET CAPABILITY SCHEMA:\n- symbol (string, required): Stock ticker symbol (e.g., AAPL, GOOGL)\n\nINSTRUCTIONS:\nAnalyze the error and provide corrected parameters.`,
                        response: `{\n  "should_retry": true,\n  "analysis": "The user said 'Apple' but the API requires the ticker symbol. Apple Inc's ticker is 'AAPL'.",\n  "corrected_parameters": {\n    "symbol": "AAPL"\n  }\n}`,
                        temperature: 0.0,
                        max_tokens: 1000,
                        model: "gpt-4o-mini",
                        provider: "openai",
                        prompt_tokens: 178,
                        completion_tokens: 67,
                        total_tokens: 245,
                        success: true,
                        attempt: 1,
                        step_id: "step-1"
                    }
                ]
            }
        ],
        timestamp: new Date().toISOString()
    };
}
