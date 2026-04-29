/**
 * HITL (Human-in-the-Loop) view — checkpoint list, filtering, and detail panel.
 *
 * Extracted from index.html; keeps the original DOM-based rendering
 * unchanged so the migration is a straight lift-and-shift.
 */

import {
    formatTimeAgo,
    formatDateTime,
    formatDuration,
    truncateText,
    truncateInstruction,
    escapeHtml,
    syntaxHighlightJson,
    formatJsonResponse,
    copyToClipboard,
    isExpiringSoon,
} from '../utils/format.js';

import { fetchAPI, postAPI } from '../api.js';
import { showLoading, hideLoading } from '../utils/dom.js';

// ---------------------------------------------------------------------------
// Module-level state (replaces former globals)
// ---------------------------------------------------------------------------
let checkpoints = [];
let selected = null;
let activeTab = 'overview';
let typeFilter = 'all';
let sortColumn = 'expires_at';
let sortDirection = 'asc';

// ---------------------------------------------------------------------------
// Internal helpers
// ---------------------------------------------------------------------------

function setTypeFilter(filter) {
    typeFilter = filter;
    document.querySelectorAll('#hitlListPanel .filter-btn').forEach(btn => {
        btn.classList.toggle('active', btn.dataset.filter === filter);
    });
    filterCheckpoints();
}

function sortTable(column) {
    if (sortColumn === column) {
        sortDirection = sortDirection === 'asc' ? 'desc' : 'asc';
    } else {
        sortColumn = column;
        sortDirection = 'asc';
    }

    document.querySelectorAll('#hitlTableBody').forEach(tbody => {
        const table = tbody.closest('table');
        if (table) {
            table.querySelectorAll('th.sortable').forEach(th => {
                th.classList.remove('asc', 'desc');
                if (th.dataset.sort === column) {
                    th.classList.add(sortDirection);
                }
            });
        }
    });

    filterCheckpoints();
}

function filterCheckpoints() {
    const searchInput = document.getElementById('hitlSearchInput');
    const searchTerm = searchInput ? searchInput.value.toLowerCase() : '';
    const filtered = checkpoints.filter(checkpoint => {
        const interruptType = checkpoint.interrupt_point || '';
        const isPlan = interruptType === 'plan_generated';
        const isStep = interruptType === 'before_step' || interruptType === 'after_step';
        const isError = interruptType === 'on_error';

        const matchesFilter = typeFilter === 'all' ||
            (typeFilter === 'plan' && isPlan) ||
            (typeFilter === 'step' && isStep) ||
            (typeFilter === 'error' && isError);

        const matchesSearch = !searchTerm ||
            checkpoint.checkpoint_id.toLowerCase().includes(searchTerm) ||
            (checkpoint.original_request && checkpoint.original_request.toLowerCase().includes(searchTerm)) ||
            (checkpoint.message && checkpoint.message.toLowerCase().includes(searchTerm)) ||
            (checkpoint.reason && checkpoint.reason.toLowerCase().includes(searchTerm));

        return matchesFilter && matchesSearch;
    });

    // Apply column sorting
    filtered.sort((a, b) => {
        let aVal, bVal;

        if (sortColumn === 'agent_name') {
            aVal = (a.agent_name || '').toLowerCase();
            bVal = (b.agent_name || '').toLowerCase();
            const cmp = aVal.localeCompare(bVal);
            return sortDirection === 'asc' ? cmp : -cmp;
        } else if (sortColumn === 'created_at') {
            aVal = new Date(a.created_at || 0);
            bVal = new Date(b.created_at || 0);
        } else if (sortColumn === 'expires_at') {
            aVal = new Date(a.expires_at || 0);
            bVal = new Date(b.expires_at || 0);
        } else {
            return 0;
        }

        const diff = aVal - bVal;
        return sortDirection === 'asc' ? diff : -diff;
    });

    renderTable(filtered);
}

function renderTable(list) {
    const tbody = document.getElementById('hitlTableBody');
    if (list.length === 0) {
        tbody.innerHTML = `<tr><td colspan="7" style="text-align: center; color: var(--text-muted); padding: 48px;">
            <div style="font-size: 32px; margin-bottom: 12px;">✓</div>
            No pending checkpoints awaiting approval
        </td></tr>`;
        return;
    }

    tbody.innerHTML = list.map(cp => {
        const stepCount = cp.step_count || 0;
        const completedCount = cp.completed_count || 0;
        const progressPercent = stepCount > 0 ? Math.round((completedCount / stepCount) * 100) : 0;

        // Format interrupt point for display
        const interruptLabels = {
            'plan_generated': '📋 Plan',
            'before_step': '⏸️ Before Step',
            'after_step': '✓ After Step',
            'on_error': '⚠️ Error'
        };
        const interruptLabel = interruptLabels[cp.interrupt_point] || cp.interrupt_point;

        // Agent name display
        const agentName = cp.agent_name || '(default)';

        // Step context — distinguishes per-step approvals that share the same message.
        const stepBadge = cp.step_id
            ? `<span class="mono" style="font-size: 10px; padding: 1px 6px; background: rgba(10, 132, 255, 0.15); border: 1px solid rgba(10, 132, 255, 0.3); border-radius: 3px; color: var(--accent-blue); margin-right: 6px;">${escapeHtml(cp.step_id)}</span>`
            : '';
        const stepDetail = cp.step_instruction
            ? `<div style="font-size: 11px; color: var(--text-muted); margin-top: 2px; font-style: italic; white-space: nowrap; overflow: hidden; text-overflow: ellipsis;" title="${escapeHtml(cp.step_instruction)}">${escapeHtml(truncateText(cp.step_instruction, 60))}</div>`
            : '';

        return `
        <tr data-checkpoint-id="${cp.checkpoint_id}" class="${selected?.checkpoint_id === cp.checkpoint_id ? 'selected' : ''}">
            <td><span class="priority-badge ${cp.priority || 'normal'}">${(cp.priority || 'normal').toUpperCase()}</span></td>
            <td><span class="mono" style="font-size: 12px; color: var(--accent-blue);" title="${escapeHtml(agentName)}">${escapeHtml(truncateText(agentName, 20))}</span></td>
            <td><span class="interrupt-badge ${cp.interrupt_point}">${interruptLabel}</span></td>
            <td>
                <div style="max-width: 250px;">
                    <div style="font-weight: 500; white-space: nowrap; overflow: hidden; text-overflow: ellipsis;" title="${escapeHtml(cp.original_request || '')}">${escapeHtml(truncateText(cp.original_request || 'N/A', 40))}</div>
                    <div style="font-size: 11px; color: var(--text-muted); margin-top: 4px;">${stepBadge}${escapeHtml(truncateText(cp.message || '', 50))}</div>
                    ${stepDetail}
                </div>
            </td>
            <td>
                <div class="step-progress">
                    <div class="step-progress-bar">
                        <div class="step-progress-fill" style="width: ${progressPercent}%"></div>
                    </div>
                    <span>${completedCount}/${stepCount}</span>
                </div>
            </td>
            <td><span class="time-ago">${formatTimeAgo(cp.created_at)}</span></td>
            <td><span class="time-ago" style="color: ${isExpiringSoon(cp.expires_at) ? 'var(--accent-orange)' : 'inherit'}">${formatTimeAgo(cp.expires_at)}</span></td>
        </tr>
    `}).join('');
}

async function selectCheckpoint(checkpointId) {
    // Check if we already have the full checkpoint
    const summaryCheckpoint = checkpoints.find(c => c.checkpoint_id === checkpointId);

    // Fetch full checkpoint details from API
    try {
        const { data } = await fetchAPI(`/api/hitl/checkpoints/${encodeURIComponent(checkpointId)}`);
        selected = data;
    } catch (error) {
        console.error('Failed to fetch HITL checkpoint:', error);
        if (summaryCheckpoint) {
            // API failed but we have summary - use what we can
            selected = summaryCheckpoint;
        } else {
            // Fallback to mock if available
            const mockCheckpoint = getMockHITLCheckpoint(checkpointId);
            selected = mockCheckpoint || null;
        }
    }

    filterCheckpoints();
    renderDetail();
}

function setDetailTab(tab) {
    activeTab = tab;
    document.querySelectorAll('#hitlDetailPanel .detail-tab').forEach(btn => {
        btn.classList.toggle('active', btn.dataset.tab === tab);
    });
    renderDetail();
}

function renderDetail() {
    const content = document.getElementById('hitlDetailContent');
    const title = document.getElementById('hitlDetailTitle');

    if (!selected) {
        title.textContent = 'Select a checkpoint';
        content.innerHTML = `<div class="empty-detail"><div class="empty-detail-icon">✋</div><div>Select a checkpoint to view details</div></div>`;
        return;
    }

    title.textContent = `Checkpoint: ${truncateText(selected.checkpoint_id, 20)}`;

    if (activeTab === 'json') {
        content.innerHTML = `<div class="json-container"><button class="copy-btn" data-action="copy-hitl-json">Copy</button><pre class="json-view">${syntaxHighlightJson(selected)}</pre></div>`;
    } else if (activeTab === 'plan') {
        content.innerHTML = renderPlanView(selected);
    } else {
        content.innerHTML = renderOverviewView(selected);
    }
}

function renderOverviewView(checkpoint) {
    let html = `<div class="formatted-view">`;

    // Hoist canAct / isStreaming before the Blocked Step card (RC3 + RC4).
    const isPending = checkpoint.status === 'pending';
    const canAct = isPending && !!checkpoint.agent_name;
    const isStreaming = checkpoint.request_mode === 'streaming';
    const noAgentWarning = (isPending && !canAct) ?
        `<div style="font-size:11px; color:var(--accent-orange); margin-bottom:10px;">
            ⚠️ Cannot route command: this checkpoint has no agent_name.
            Ensure the agent sets <code>TRUVAG3_AGENT_NAME</code>.
        </div>` : '';

    // Blocked Step card — rendered FIRST with embedded Approve/Reject buttons (RC4).
    // When current_step exists the standalone action panel below is suppressed.
    if (checkpoint.current_step) {
        const step = checkpoint.current_step;
        const capability = step.capability || step.metadata?.capability || '';
        const params = (step.parameters && Object.keys(step.parameters).length > 0) ? step.parameters : step.metadata?.parameters;
        html += `<div class="info-section">
            <div class="info-section-title"><span class="section-icon">⏸️</span> Blocked Step${isPending ? ' — Action Required' : ' (Awaiting Approval)'}</div>
            <div class="plan-step-card current">
                <div class="plan-step-header">
                    <span class="plan-step-id">${step.step_id}</span>
                    <span class="plan-step-status current">⏳ AWAITING APPROVAL</span>
                </div>
                ${capability ? `<div class="plan-step-capability">${escapeHtml(capability)}</div>` : ''}
                ${step.service_name ? `<div style="font-size: 12px; color: var(--text-muted); margin-top: 4px;">Service: <span style="color: var(--accent-blue);">${escapeHtml(step.service_name)}</span>${step.capability_name ? ` • Capability: <span style="color: var(--accent-green);">${escapeHtml(step.capability_name)}</span>` : ''}</div>` : ''}
                ${step.instruction ? `<div style="font-size: 12px; color: var(--text-secondary); margin-top: 8px; padding: 8px; background: rgba(0,0,0,0.2); border-radius: 6px; font-style: italic;">📝 ${escapeHtml(step.instruction)}</div>` : ''}
                ${step.description ? `<div class="plan-step-description">${escapeHtml(step.description)}</div>` : ''}
                ${params && Object.keys(params).length > 0 ? `<div class="plan-step-params">${JSON.stringify(params, null, 2)}</div>` : ''}
                ${step.depends_on && step.depends_on.length > 0 ? `<div class="plan-step-depends">Depends on: ${step.depends_on.join(', ')}</div>` : ''}
            </div>
            ${isPending ? (isStreaming ? `
            <div style="font-size: 13px; color: var(--text-secondary); margin-top: 8px;">
                ⚡ Streaming request — approve/reject must be performed by the connected client.
            </div>` : `
            ${noAgentWarning}
            <textarea id="hitl-reason-${checkpoint.checkpoint_id}" class="hitl-reason-input"
                placeholder="Optional reason for your decision..." style="margin-top: 12px;"></textarea>
            <div class="hitl-action-buttons" style="margin-top: 8px;">
                <button class="hitl-approve-btn" id="hitl-approve-btn-${checkpoint.checkpoint_id}"
                    ${canAct ? '' : 'disabled'}
                    data-action="hitl-command" data-checkpoint-id="${checkpoint.checkpoint_id}" data-command="approve">
                    ✓ Approve &amp; Resume
                </button>
                <button class="hitl-reject-btn" id="hitl-reject-btn-${checkpoint.checkpoint_id}"
                    ${canAct ? '' : 'disabled'}
                    data-action="hitl-command" data-checkpoint-id="${checkpoint.checkpoint_id}" data-command="reject">
                    ✗ Reject
                </button>
            </div>
            <div id="hitl-action-status-${checkpoint.checkpoint_id}" class="hitl-action-status"></div>`) : ''}
        </div>`;
    }

    // Decision Info (the reason for interruption)
    if (checkpoint.decision) {
        const decision = checkpoint.decision;
        html += `<div class="info-section"><div class="info-section-title"><span class="section-icon">⚠️</span> Interrupt Decision</div><div class="info-grid">
            <div class="info-label">Reason</div><div class="info-value"><span class="priority-badge ${decision.priority || 'normal'}">${decision.reason || 'N/A'}</span></div>
            <div class="info-label">Priority</div><div class="info-value"><span class="priority-badge ${decision.priority || 'normal'}">${(decision.priority || 'normal').toUpperCase()}</span></div>
            <div class="info-label">Message</div><div class="info-value">${escapeHtml(decision.message || 'N/A')}</div>
            ${decision.default_action ? `<div class="info-label">Default Action</div><div class="info-value"><span class="action-badge ${decision.default_action}">${decision.default_action.toUpperCase()}</span></div>` : ''}
        </div></div>`;
    }

    // Checkpoint Info
    const agentName = checkpoint.agent_name || '(default)';
    html += `<div class="info-section"><div class="info-section-title"><span class="section-icon">📊</span> Checkpoint Info</div><div class="info-grid">
        <div class="info-label">Checkpoint ID</div><div class="info-value mono">${checkpoint.checkpoint_id}</div>
        <div class="info-label">Request ID</div><div class="info-value mono">${checkpoint.request_id}</div>
        <div class="info-label">Agent</div><div class="info-value"><span style="color: var(--accent-blue); font-weight: 500;">${escapeHtml(agentName)}</span></div>
        <div class="info-label">Interrupt Point</div><div class="info-value"><span class="interrupt-badge ${checkpoint.interrupt_point}">${checkpoint.interrupt_point}</span></div>
        <div class="info-label">Status</div><div class="info-value"><span class="status-badge ${checkpoint.status === 'pending' ? 'error' : 'success'}">${checkpoint.status}</span></div>
        <div class="info-label">Created</div><div class="info-value">${formatDateTime(checkpoint.created_at)}</div>
        <div class="info-label">Expires</div><div class="info-value" style="color: ${isExpiringSoon(checkpoint.expires_at) ? 'var(--accent-orange)' : 'inherit'}">${formatDateTime(checkpoint.expires_at)}</div>
    </div></div>`;

    // Original Request
    html += `<div class="info-section"><div class="info-section-title"><span class="section-icon">💬</span> Original Request</div>
        <div style="padding: 16px; background: rgba(0,0,0,0.2); border-radius: 10px; font-size: 14px; line-height: 1.6;">
            ${escapeHtml(checkpoint.original_request || 'N/A')}
        </div>
    </div>`;

    // Standalone action panel — only for plan-level HITL (no current_step).
    // When current_step exists, buttons are embedded on the Blocked Step card above (RC4).
    if (!checkpoint.current_step && isPending) {
        if (isStreaming) {
            html += `
                <div class="hitl-action-panel">
                    <div class="hitl-action-header"><span>⚡</span><span class="hitl-action-title">Streaming Request — Awaiting User Response</span></div>
                    <div class="hitl-action-desc" style="color: var(--text-secondary);">
                        This checkpoint was created during a live streaming session.
                        Approve/Reject must be performed by the connected client, not from the registry viewer.
                    </div>
                </div>`;
        } else {
            html += `
                <div class="hitl-action-panel">
                    <div class="hitl-action-header">
                        <span>⚡</span>
                        <span class="hitl-action-title">Action Required</span>
                    </div>
                    <div class="hitl-action-desc">${escapeHtml(checkpoint.decision?.message || 'Review the details and approve or reject this checkpoint.')}</div>
                    ${noAgentWarning}
                    <textarea id="hitl-reason-${checkpoint.checkpoint_id}"
                        class="hitl-reason-input"
                        placeholder="Optional reason for your decision..."></textarea>
                    <div class="hitl-action-buttons">
                        <button class="hitl-approve-btn"
                            id="hitl-approve-btn-${checkpoint.checkpoint_id}"
                            ${canAct ? '' : 'disabled'}
                            data-action="hitl-command" data-checkpoint-id="${checkpoint.checkpoint_id}" data-command="approve">
                            ✓ Approve &amp; Resume
                        </button>
                        <button class="hitl-reject-btn"
                            id="hitl-reject-btn-${checkpoint.checkpoint_id}"
                            ${canAct ? '' : 'disabled'}
                            data-action="hitl-command" data-checkpoint-id="${checkpoint.checkpoint_id}" data-command="reject">
                            ✗ Reject
                        </button>
                    </div>
                    <div id="hitl-action-status-${checkpoint.checkpoint_id}" class="hitl-action-status"></div>
                </div>`;
        }
    }

    // Resolved Parameters (for step-level HITL)
    if (checkpoint.resolved_parameters && Object.keys(checkpoint.resolved_parameters).length > 0) {
        html += `<div class="info-section"><div class="info-section-title"><span class="section-icon">🔧</span> Resolved Parameters</div>
            <div class="plan-step-params">${JSON.stringify(checkpoint.resolved_parameters, null, 2)}</div>
        </div>`;
    }

    // Completed Steps (show what has already executed)
    if (checkpoint.completed_steps && checkpoint.completed_steps.length > 0) {
        html += `<div class="info-section"><div class="info-section-title"><span class="section-icon">✅</span> Completed Steps (${checkpoint.completed_steps.length})</div>`;
        checkpoint.completed_steps.forEach((result, index) => {
            const statusClass = result.skipped ? 'skipped' : (result.success ? 'completed' : 'error');
            const statusIcon = result.skipped ? '⊘' : (result.success ? '✓' : '✗');
            const statusLabel = result.skipped ? 'SKIPPED' : (result.success ? 'SUCCESS' : 'FAILED');
            const serviceName = result.agent_name || result.service_name || result.capability?.split('.')[0] || 'unknown';
            const durationMs = result.duration_ms || (result.duration ? Math.round(result.duration / 1000000) : null);
            html += `<div class="plan-step-card ${statusClass}" style="margin-bottom: 8px;">
                <div class="plan-step-header">
                    <span class="plan-step-id">${result.step_id}</span>
                    <span class="plan-step-status ${statusClass}">${statusIcon} ${statusLabel}</span>
                </div>
                <div class="plan-step-capability">${escapeHtml(serviceName)}</div>
                ${result.instruction ? `<div style="font-size: 12px; color: var(--text-secondary); margin-top: 8px; padding: 8px; background: rgba(0,0,0,0.2); border-radius: 6px; font-style: italic;">📝 ${escapeHtml(result.instruction)}</div>` : ''}
                ${durationMs ? `<div style="font-size: 11px; color: var(--text-muted); margin-top: 4px;">Duration: ${formatDuration(durationMs)}${result.attempts > 1 ? ` (${result.attempts} attempts)` : ''}</div>` : ''}
                ${result.error ? `<div style="font-size: 12px; color: var(--accent-red); margin-top: 8px; padding: 8px; background: rgba(255,59,48,0.1); border-radius: 6px;">Error: ${escapeHtml(result.error)}</div>` : ''}
                ${result.response_text ? `<div class="plan-step-params" style="margin-top: 8px;"><div style="font-size: 11px; color: var(--text-muted); margin-bottom: 4px;">Response:</div><div style="white-space: pre-wrap;">${escapeHtml(result.response_text)}</div></div>` :
                  (result.response ? `<div class="plan-step-params" style="margin-top: 8px;"><div style="font-size: 11px; color: var(--text-muted); margin-bottom: 4px;">Response:</div>${formatJsonResponse(result.response)}</div>` : '')}
            </div>`;
        });
        html += `</div>`;
    }

    // Current Step Result (for error escalation)
    if (checkpoint.current_step_result) {
        const result = checkpoint.current_step_result;
        html += `<div class="info-section"><div class="info-section-title"><span class="section-icon">❌</span> Error Details</div>
            <div class="info-grid">
                <div class="info-label">Step</div><div class="info-value mono">${result.capability}</div>
                ${result.service_name ? `<div class="info-label">Service</div><div class="info-value">${escapeHtml(result.service_name)}</div>` : ''}
                ${result.instruction ? `<div class="info-label">Instruction</div><div class="info-value" style="font-style: italic;">${escapeHtml(result.instruction)}</div>` : ''}
                <div class="info-label">Duration</div><div class="info-value">${formatDuration(result.duration_ms || 0)}${result.attempts > 1 ? ` (${result.attempts} attempts)` : ''}</div>
                <div class="info-label">Error</div><div class="info-value" style="color: var(--accent-red);">${escapeHtml(result.error || 'Unknown error')}</div>
            </div>
        </div>`;
    }

    // User Context
    if (checkpoint.user_context && Object.keys(checkpoint.user_context).length > 0) {
        html += `<div class="info-section"><div class="info-section-title"><span class="section-icon">👤</span> User Context</div><div class="info-grid">
            ${Object.entries(checkpoint.user_context).map(([key, value]) => `<div class="info-label">${key}</div><div class="info-value mono">${value}</div>`).join('')}
        </div></div>`;
    }

    html += `</div>`;
    return html;
}

function renderPlanView(checkpoint) {
    if (!checkpoint.plan || !checkpoint.plan.steps) {
        return `<div class="empty-detail"><div class="empty-detail-icon">📋</div><div>No execution plan available</div></div>`;
    }

    const plan = checkpoint.plan;
    // Create a map of completed step results for quick lookup
    const completedStepsMap = new Map((checkpoint.completed_steps || []).map(s => [s.step_id, s]));
    const currentStepId = checkpoint.current_step?.step_id;

    let html = `<div class="formatted-view">`;

    // Plan summary with agent name and checkpoint status
    const agentName = checkpoint.agent_name || '(default)';
    html += `<div class="info-section"><div class="info-section-title"><span class="section-icon">📋</span> Execution Plan</div><div class="info-grid">
        ${plan.request_id ? `<div class="info-label">Request ID</div><div class="info-value mono">${plan.request_id}</div>` : ''}
        <div class="info-label">Agent</div><div class="info-value"><span style="color: var(--accent-blue); font-weight: 500;">${escapeHtml(agentName)}</span></div>
        <div class="info-label">Status</div><div class="info-value"><span class="status-badge ${checkpoint.status === 'pending' ? 'error' : 'success'}">${checkpoint.status || 'unknown'}</span></div>
        <div class="info-label">Total Steps</div><div class="info-value">${plan.steps.length}</div>
        <div class="info-label">Completed</div><div class="info-value">${completedStepsMap.size}</div>
        ${plan.synthesis_strategy ? `<div class="info-label">Synthesis</div><div class="info-value">${plan.synthesis_strategy}</div>` : ''}
    </div></div>`;

    // Rationale
    if (plan.rationale) {
        html += `<div class="info-section"><div class="info-section-title"><span class="section-icon">💭</span> Plan Rationale</div>
            <div style="padding: 16px; background: rgba(0,0,0,0.2); border-radius: 10px; font-size: 13px; line-height: 1.6; color: var(--text-secondary);">
                ${escapeHtml(plan.rationale)}
            </div>
        </div>`;
    }

    // Steps
    html += `<div class="info-section"><div class="info-section-title"><span class="section-icon">📝</span> Plan Steps (${plan.steps.length})</div>`;

    plan.steps.forEach((step, index) => {
        const completedResult = completedStepsMap.get(step.step_id);
        const isCompleted = !!completedResult;
        const isCurrent = step.step_id === currentStepId;
        const status = isCompleted ? (completedResult.success ? 'completed' : 'error') : (isCurrent ? 'current' : 'pending');
        const statusIcon = isCompleted ? (completedResult.success ? '✓' : '✗') : (isCurrent ? '⏸' : '○');
        const statusLabel = isCompleted ? (completedResult.success ? 'COMPLETED' : 'FAILED') : (isCurrent ? 'BLOCKED' : 'PENDING');

        // Handle capability from metadata fallback (orchestrator stores capability in metadata.capability)
        const capability = step.capability || step.metadata?.capability || '';
        const serviceName = step.service_name || '';
        const capabilityName = step.capability_name || '';
        // Parameters can be at top level or in metadata
        const params = (step.parameters && Object.keys(step.parameters).length > 0) ? step.parameters : step.metadata?.parameters;

        html += `<div class="plan-step-card ${status}">
            <div class="plan-step-header">
                <span class="plan-step-id">${step.step_id}</span>
                <span class="plan-step-status ${status}">${statusIcon} ${statusLabel}</span>
            </div>
            ${capability ? `<div class="plan-step-capability">${escapeHtml(capability)}</div>` : ''}
            ${serviceName ? `<div style="font-size: 12px; color: var(--text-muted); margin-top: 4px;">Service: <span style="color: var(--accent-blue);">${escapeHtml(serviceName)}</span>${capabilityName ? ` • Capability: <span style="color: var(--accent-green);">${escapeHtml(capabilityName)}</span>` : ''}</div>` : ''}
            ${step.instruction ? `<div style="font-size: 12px; color: var(--text-secondary); margin-top: 8px; padding: 8px; background: rgba(0,0,0,0.2); border-radius: 6px; font-style: italic;">📝 ${escapeHtml(step.instruction)}</div>` : ''}
            ${step.description ? `<div class="plan-step-description">${escapeHtml(step.description)}</div>` : ''}
            ${params && Object.keys(params).length > 0 ? `
                <div class="plan-step-params"><div style="font-size: 11px; color: var(--text-muted); margin-bottom: 4px;">Parameters:</div>${JSON.stringify(params, null, 2)}</div>
            ` : ''}
            ${step.depends_on && step.depends_on.length > 0 ? `
                <div class="plan-step-depends">Depends on: ${step.depends_on.join(', ')}</div>
            ` : ''}
            ${completedResult ? `
                <div style="margin-top: 12px; padding-top: 12px; border-top: 1px solid rgba(255,255,255,0.1);">
                    <div style="font-size: 11px; color: var(--text-muted); margin-bottom: 4px;">Execution Result:</div>
                    ${completedResult.instruction ? `<div style="font-size: 12px; color: var(--text-secondary); margin-bottom: 8px; padding: 8px; background: rgba(0,0,0,0.2); border-radius: 6px; font-style: italic;">📝 ${escapeHtml(completedResult.instruction)}</div>` : ''}
                    ${(completedResult.duration_ms || completedResult.duration) ? `<div style="font-size: 11px; color: var(--text-muted);">Duration: ${formatDuration(completedResult.duration_ms || Math.round(completedResult.duration / 1000000))}${completedResult.attempts > 1 ? ` (${completedResult.attempts} attempts)` : ''}</div>` : ''}
                    ${completedResult.error ? `<div style="font-size: 12px; color: var(--accent-red); margin-top: 8px; padding: 8px; background: rgba(255,59,48,0.1); border-radius: 6px;">Error: ${escapeHtml(completedResult.error)}</div>` : ''}
                    ${completedResult.response_text ? `<div class="plan-step-params" style="margin-top: 8px;"><div style="font-size: 11px; color: var(--text-muted); margin-bottom: 4px;">Response:</div><div style="white-space: pre-wrap;">${escapeHtml(completedResult.response_text)}</div></div>` :
                      (completedResult.response ? `<div class="plan-step-params" style="margin-top: 8px;"><div style="font-size: 11px; color: var(--text-muted); margin-bottom: 4px;">Response:</div>${formatJsonResponse(completedResult.response)}</div>` : '')}
                </div>
            ` : ''}
        </div>`;
    });

    html += `</div></div>`;
    return html;
}

function copyJson(evt) {
    const text = JSON.stringify(selected, null, 2);
    copyToClipboard(text, evt);
}

// submitCommand sends an approve or reject command to the agent and
// triggers resume if the agent signals should_resume: true.
async function submitCommand(checkpointId, commandType) {
    const reasonInput = document.getElementById(`hitl-reason-${checkpointId}`);
    const statusEl   = document.getElementById(`hitl-action-status-${checkpointId}`);
    const approveBtn = document.getElementById(`hitl-approve-btn-${checkpointId}`);
    const rejectBtn  = document.getElementById(`hitl-reject-btn-${checkpointId}`);

    const feedback = reasonInput?.value?.trim() || '';

    // Disable buttons and show working state
    if (approveBtn) approveBtn.disabled = true;
    if (rejectBtn)  rejectBtn.disabled  = true;
    if (statusEl) {
        statusEl.className = 'hitl-action-status working';
        statusEl.textContent = commandType === 'approve' ? 'Sending approval...' : 'Sending rejection...';
    }

    try {
        // Step 1: Send the command to the agent via the registry viewer proxy
        const { data: cmdResult } = await postAPI('/api/hitl/command', {
            checkpoint_id: checkpointId,
            command_type:  commandType,
            feedback:      feedback,
        });

        // Step 2: If approved and agent signals should_resume, trigger resume.
        // Skip resume for non_streaming checkpoints — the server-side caller
        // (e.g., ProcessQuery's HITL wait loop) is already listening on Redis
        // SUBSCRIBE and will resume internally. Calling the SSE resume endpoint
        // here would create a duplicate parallel execution.
        const cp = checkpoints?.find(c => c.checkpoint_id === checkpointId);
        const isNonStreaming = cp?.request_mode === 'non_streaming';
        if (commandType === 'approve' && cmdResult.should_resume && !isNonStreaming) {
            if (statusEl) statusEl.textContent = 'Resuming execution...';

            try {
                await postAPI(`/api/hitl/resume/${checkpointId}`, {});
            } catch (resumeErr) {
                // Command was accepted; resume failed — show warning but don't block UI refresh
                if (statusEl) {
                    statusEl.className = 'hitl-action-status error';
                    statusEl.textContent = `⚠️ Approved, but resume call failed: ${resumeErr.message}`;
                }
                setTimeout(() => fetchCheckpoints(), 2000);
                return;
            }
        }

        // Success
        if (statusEl) {
            statusEl.className = 'hitl-action-status success';
            statusEl.textContent = commandType === 'approve'
                ? '✓ Approved — execution is resuming'
                : '✓ Rejected';
        }

        // Refresh the checkpoint list after a short delay so the status update propagates
        setTimeout(() => fetchCheckpoints(), 1200);

    } catch (err) {
        // Re-enable buttons on error so the user can retry
        if (approveBtn) approveBtn.disabled = false;
        if (rejectBtn)  rejectBtn.disabled  = false;
        if (statusEl) {
            statusEl.className = 'hitl-action-status error';
            statusEl.textContent = `Error: ${err.message}`;
        }
    }
}

async function fetchCheckpoints() {
    const refreshBtn = document.querySelector('.refresh-btn');
    const errorBanner = document.getElementById('hitlErrorBanner');
    const listPanel = document.getElementById('hitlListPanel');
    if (refreshBtn) refreshBtn.classList.add('loading');
    showLoading(listPanel, 'Loading checkpoints...');

    try {
        let data;
        try {
            const result = await fetchAPI('/api/hitl/checkpoints');
            data = result.data;
        } catch {
            // Use mock data for development
            data = getMockHITLData();
        }

        // Count critical checkpoints
        const criticalCount = data.checkpoints.filter(c => c.priority === 'critical').length;

        const pendingEl = document.getElementById('pendingCount');
        const criticalEl = document.getElementById('criticalCount');
        if (pendingEl) pendingEl.textContent = data.checkpoints.length;
        if (criticalEl) criticalEl.textContent = criticalCount;

        checkpoints = data.checkpoints.sort((a, b) => {
            // Sort by priority (critical first), then by created_at
            const priorityOrder = { critical: 0, high: 1, normal: 2, low: 3 };
            const aPriority = priorityOrder[a.priority] ?? 2;
            const bPriority = priorityOrder[b.priority] ?? 2;
            if (aPriority !== bPriority) return aPriority - bPriority;
            return new Date(b.created_at) - new Date(a.created_at);
        });

        filterCheckpoints();

        // Refresh selected checkpoint if still exists
        if (selected) {
            const updated = checkpoints.find(c => c.checkpoint_id === selected.checkpoint_id);
            if (updated) {
                await selectCheckpoint(selected.checkpoint_id);
            } else {
                // Checkpoint was resolved/expired
                selected = null;
                renderDetail();
            }
        }

        const lastUpdatedEl = document.getElementById('hitlLastUpdated');
        if (lastUpdatedEl) lastUpdatedEl.textContent = `Last updated: ${formatDateTime(new Date())}`;
        if (errorBanner) errorBanner.classList.remove('visible');
    } catch (error) {
        console.error('Failed to fetch HITL checkpoints:', error);
        const errMsgEl = document.getElementById('hitlErrorMessage');
        if (errMsgEl) errMsgEl.textContent = error.message;
        if (errorBanner) errorBanner.classList.add('visible');
    } finally {
        if (refreshBtn) refreshBtn.classList.remove('loading');
        hideLoading(listPanel);
    }
}

// ---------------------------------------------------------------------------
// Mock data for development
// ---------------------------------------------------------------------------

function getMockHITLData() {
    const now = new Date();
    return {
        checkpoints: [
            {
                checkpoint_id: "cp-abc123-plan",
                request_id: "req-travel-001",
                interrupt_point: "plan_generated",
                reason: "plan_approval",
                priority: "normal",
                message: "Execution plan requires approval before proceeding",
                original_request: "What's the weather in Tokyo and book me a flight there?",
                step_count: 3,
                completed_count: 0,
                current_step: "",
                created_at: new Date(now - 2 * 60000).toISOString(),
                expires_at: new Date(now.getTime() + 5 * 60000).toISOString(),
                status: "pending",
                agent_name: "travel-agent"
            },
            {
                checkpoint_id: "cp-def456-step",
                request_id: "req-stock-002",
                interrupt_point: "before_step",
                reason: "sensitive_operation",
                priority: "high",
                message: "About to execute sensitive operation: stock_trade.execute_trade",
                original_request: "Buy 100 shares of AAPL",
                step_count: 2,
                completed_count: 1,
                current_step: "stock_trade.execute_trade",
                created_at: new Date(now - 5 * 60000).toISOString(),
                expires_at: new Date(now.getTime() + 10 * 60000).toISOString(),
                status: "pending",
                agent_name: "trading-bot"
            },
            {
                checkpoint_id: "cp-ghi789-error",
                request_id: "req-payment-003",
                interrupt_point: "on_error",
                reason: "escalation",
                priority: "critical",
                message: "Payment processing failed after 3 retries - human intervention required",
                original_request: "Process refund for order #12345",
                step_count: 1,
                completed_count: 0,
                current_step: "payment.process_refund",
                created_at: new Date(now - 10 * 60000).toISOString(),
                expires_at: new Date(now.getTime() + 30 * 60000).toISOString(),
                status: "pending",
                agent_name: "payment-service"
            }
        ],
        total: 3,
        timestamp: now.toISOString()
    };
}

function getMockHITLCheckpoint(checkpointId) {
    // For mock mode, return detailed checkpoint data
    const mockData = getMockHITLData();
    const summary = mockData.checkpoints.find(c => c.checkpoint_id === checkpointId);
    if (!summary) return null;

    // Expand to full checkpoint (simulating API response)
    const now = new Date();
    if (checkpointId === 'cp-abc123-plan') {
        return {
            ...summary,
            decision: {
                should_interrupt: true,
                reason: "plan_approval",
                message: "Execution plan requires approval before proceeding",
                priority: "normal",
                default_action: "approve"
            },
            plan: {
                request_id: "req-travel-001",
                steps: [
                    { step_id: "step-1", capability: "weather-tool.get_weather", service_name: "weather-tool", capability_name: "get_weather", parameters: { location: "Tokyo, Japan" }, depends_on: [], description: "Get current weather for Tokyo" },
                    { step_id: "step-2", capability: "flight-search.search_flights", service_name: "flight-search", capability_name: "search_flights", parameters: { destination: "Tokyo", date: "2024-03-15" }, depends_on: [], description: "Search for available flights to Tokyo" },
                    { step_id: "step-3", capability: "flight-booking.book_flight", service_name: "flight-booking", capability_name: "book_flight", parameters: { flight_id: "{{step-2.flights[0].id}}" }, depends_on: ["step-2"], description: "Book the selected flight" }
                ],
                synthesis_strategy: "llm",
                rationale: "User wants weather info and flight booking. Steps 1 and 2 can run in parallel, step 3 depends on step 2 results."
            },
            completed_steps: [],
            user_context: { user_id: "user-123", session_id: "session-abc" }
        };
    } else if (checkpointId === 'cp-def456-step') {
        return {
            ...summary,
            decision: {
                should_interrupt: true,
                reason: "sensitive_operation",
                message: "About to execute sensitive operation: stock_trade.execute_trade",
                priority: "high"
            },
            plan: {
                steps: [
                    { step_id: "step-1", capability: "stock-market.get_quote", service_name: "stock-market", capability_name: "get_quote", parameters: { symbol: "AAPL" }, depends_on: [], description: "Get current stock quote for AAPL" },
                    { step_id: "step-2", capability: "stock_trade.execute_trade", service_name: "stock-trade", capability_name: "execute_trade", parameters: { symbol: "AAPL", quantity: 100, action: "buy" }, depends_on: ["step-1"], description: "Execute buy order for 100 shares of AAPL" }
                ]
            },
            completed_steps: [{
                step_id: "step-1",
                capability: "stock-market.get_quote",
                service_name: "stock-market",
                instruction: "Get the current stock price and trading information for AAPL",
                success: true,
                response: { symbol: "AAPL", price: 178.50, change: "+2.35", volume: "45.2M" },
                response_text: "AAPL is currently trading at $178.50, up $2.35 (+1.33%) today with volume of 45.2M shares.",
                duration_ms: 234,
                attempts: 1
            }],
            current_step: { step_id: "step-2", capability: "stock_trade.execute_trade", service_name: "stock-trade", capability_name: "execute_trade", instruction: "Execute a market buy order for 100 shares of AAPL at current price", parameters: { symbol: "AAPL", quantity: 100, action: "buy" }, depends_on: ["step-1"], description: "Execute buy order for 100 shares of AAPL" },
            resolved_parameters: { symbol: "AAPL", quantity: 100, action: "buy", price: 178.50, total: 17850.00 },
            user_context: { user_id: "user-456", account_type: "premium" }
        };
    } else if (checkpointId === 'cp-ghi789-error') {
        return {
            ...summary,
            decision: {
                should_interrupt: true,
                reason: "escalation",
                message: "Payment processing failed after 3 retries - human intervention required",
                priority: "critical"
            },
            plan: {
                steps: [{ step_id: "step-1", capability: "payment.process_refund", service_name: "payment-gateway", capability_name: "process_refund", parameters: { order_id: "#12345", amount: 99.99 }, depends_on: [], description: "Process refund for the order" }]
            },
            completed_steps: [],
            current_step: { step_id: "step-1", capability: "payment.process_refund", service_name: "payment-gateway", capability_name: "process_refund", parameters: { order_id: "#12345", amount: 99.99 }, description: "Process refund for the order" },
            current_step_result: { step_id: "step-1", capability: "payment.process_refund", service_name: "payment-gateway", instruction: "Process a refund of $99.99 for order #12345", success: false, error: "Payment gateway timeout after 30s - gateway returned 504", duration_ms: 30234, attempts: 3 },
            user_context: { user_id: "user-789", order_id: "#12345", customer: "John Doe", email: "john@example.com" }
        };
    }
    return summary;
}

// ---------------------------------------------------------------------------
// Event delegation setup
// ---------------------------------------------------------------------------

function handleTableClick(e) {
    const row = e.target.closest('tr[data-checkpoint-id]');
    if (row) {
        selectCheckpoint(row.dataset.checkpointId);
    }
}

function handleSortClick(e) {
    const th = e.target.closest('th.sortable[data-sort]');
    if (th) {
        sortTable(th.dataset.sort);
    }
}

function handleFilterClick(e) {
    const btn = e.target.closest('.filter-btn[data-filter]');
    if (btn) {
        setTypeFilter(btn.dataset.filter);
    }
}

function handleDetailTabClick(e) {
    const btn = e.target.closest('.detail-tab[data-tab]');
    if (btn) {
        setDetailTab(btn.dataset.tab);
    }
}

function handleDetailContentClick(e) {
    // Copy JSON button
    const copyBtn = e.target.closest('[data-action="copy-hitl-json"]');
    if (copyBtn) {
        copyJson(e);
        return;
    }

    // Approve / Reject buttons
    const actionBtn = e.target.closest('[data-action="hitl-command"]');
    if (actionBtn) {
        const cpId = actionBtn.dataset.checkpointId;
        const command = actionBtn.dataset.command;
        if (cpId && command) {
            submitCommand(cpId, command);
        }
    }
}

function handleSearchInput() {
    filterCheckpoints();
}

// ---------------------------------------------------------------------------
// Exported lifecycle hooks
// ---------------------------------------------------------------------------

export function init() {
    // Attach delegated event listeners
    const tbody = document.getElementById('hitlTableBody');
    if (tbody) {
        tbody.addEventListener('click', handleTableClick);
        const thead = tbody.closest('table')?.querySelector('thead');
        if (thead) thead.addEventListener('click', handleSortClick);
    }

    const listPanel = document.getElementById('hitlListPanel');
    if (listPanel) listPanel.addEventListener('click', handleFilterClick);

    const detailPanel = document.getElementById('hitlDetailPanel');
    if (detailPanel) {
        detailPanel.addEventListener('click', handleDetailTabClick);
        detailPanel.addEventListener('click', handleDetailContentClick);
    }

    const searchInput = document.getElementById('hitlSearchInput');
    if (searchInput) searchInput.addEventListener('input', handleSearchInput);

    // Initial data load
    fetchCheckpoints();
}

export function destroy() {
    // Remove event listeners to avoid leaks if the view is torn down
    const tbody = document.getElementById('hitlTableBody');
    if (tbody) {
        tbody.removeEventListener('click', handleTableClick);
        const thead = tbody.closest('table')?.querySelector('thead');
        if (thead) thead.removeEventListener('click', handleSortClick);
    }

    const listPanel = document.getElementById('hitlListPanel');
    if (listPanel) listPanel.removeEventListener('click', handleFilterClick);

    const detailPanel = document.getElementById('hitlDetailPanel');
    if (detailPanel) {
        detailPanel.removeEventListener('click', handleDetailTabClick);
        detailPanel.removeEventListener('click', handleDetailContentClick);
    }

    const searchInput = document.getElementById('hitlSearchInput');
    if (searchInput) searchInput.removeEventListener('input', handleSearchInput);

    // Reset state
    checkpoints = [];
    selected = null;
    activeTab = 'overview';
    typeFilter = 'all';
    sortColumn = 'expires_at';
    sortDirection = 'asc';
}

export function refresh() {
    fetchCheckpoints();
}
