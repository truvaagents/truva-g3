/**
 * Registry view — service list, filtering, and detail panel.
 *
 * Extracted from index.html; keeps the original DOM-based rendering
 * unchanged so the migration is a straight lift-and-shift.
 */

import {
    formatTimeAgo,
    formatDateTime,
    syntaxHighlightJson,
    formatResponseJson,
    copyToClipboard,
    escapeHtml,
} from '../utils/format.js';

import { fetchAPI } from '../api.js';
import { showLoading, hideLoading } from '../utils/dom.js';

// ---------------------------------------------------------------------------
// Module-level state (replaces former globals)
// ---------------------------------------------------------------------------
let typeFilter = 'all';
let services = [];
let selected = null;
let activeTab = 'formatted';
let inspectedRecord = null;

// Catalogue text is application data, not trusted HTML. Quotes also need
// escaping in attributes on a page that accepts a transient read credential.
const escapeAttribute = value => escapeHtml(String(value ?? '')).replace(/"/g, '&quot;').replace(/'/g, '&#39;');

// ---------------------------------------------------------------------------
// Internal helpers
// ---------------------------------------------------------------------------

function setTypeFilter(filter) {
    typeFilter = filter;
    document.querySelectorAll('#registryListPanel .filter-btn').forEach(btn => {
        btn.classList.toggle('active', btn.dataset.filter === filter);
    });
    filterServices();
}

function filterServices() {
    const searchTerm = document.getElementById('searchInput').value.toLowerCase();
    const filtered = services.filter(service => {
        const matchesType = typeFilter === 'all' || service.type === typeFilter;
        const matchesSearch = !searchTerm ||
            service.name.toLowerCase().includes(searchTerm) ||
            service.id.toLowerCase().includes(searchTerm) ||
            (service.description && service.description.toLowerCase().includes(searchTerm));
        return matchesType && matchesSearch;
    });
    renderServiceTable(filtered);
}

function renderServiceTable(list) {
    const tbody = document.getElementById('serviceTableBody');
    if (list.length === 0) {
        tbody.innerHTML = `<tr><td colspan="5" style="text-align: center; color: var(--text-muted); padding: 48px;">No services found</td></tr>`;
        return;
    }
    tbody.innerHTML = list.map(service => `
        <tr data-service-id="${escapeAttribute(service.id)}" class="${selected?.id === service.id ? 'selected' : ''}">
            <td><span class="type-badge ${escapeAttribute(service.type)}">${escapeHtml(service.type)}</span></td>
            <td><span class="service-name">${escapeHtml(service.name)}</span></td>
            <td><div class="health-indicator"><span class="health-dot ${escapeAttribute(service.health || 'unknown')}"></span>${escapeHtml(service.health || 'unknown')}</div></td>
            <td><span class="time-ago">${formatTimeAgo(service.lastSeen || service.last_seen)}</span></td>
            <td>${service.capabilities?.length || 0}</td>
        </tr>
    `).join('');
}

function selectService(serviceId) {
    selected = services.find(s => s.id === serviceId);
    filterServices();
    renderDetail();
}

function setDetailTab(tab) {
    activeTab = tab;
    document.querySelectorAll('#registryDetailPanel .detail-tab').forEach(btn => {
        btn.classList.toggle('active', btn.dataset.tab === tab);
    });
    renderDetail();
}

function renderDetail() {
    const content = document.getElementById('detailContent');
    const title = document.getElementById('detailTitle');
    if (!selected) {
        title.textContent = 'Select a service';
        content.innerHTML = `<div class="empty-detail"><div class="empty-detail-icon">📋</div><div>Select a service to view details</div></div>`;
        return;
    }
    title.textContent = selected.name;
    // A catalogue poll should not erase an operator's in-progress read form.
    if (document.activeElement?.closest('.application-inspector') && content.querySelector('.application-inspector')?.dataset.serviceId === selected.id) return;
    if (activeTab === 'json') {
        content.innerHTML = `<div class="json-container"><button class="copy-btn" data-action="copy-json">Copy</button><pre class="json-view">${syntaxHighlightJson(selected)}</pre></div>`;
    } else {
        content.innerHTML = renderFormattedView(selected);
    }
}

function renderFormattedView(service) {
    const lastSeen = service.lastSeen || service.last_seen;
    let html = `<div class="formatted-view">`;

    html += `<div class="info-section"><div class="info-section-title"><span class="section-icon">ℹ️</span> Service Info</div><div class="info-grid">
        <div class="info-label">ID</div><div class="info-value mono">${escapeHtml(service.id)}</div>
        <div class="info-label">Name</div><div class="info-value">${escapeHtml(service.name)}</div>
        <div class="info-label">Type</div><div class="info-value"><span class="type-badge ${escapeAttribute(service.type)}">${escapeHtml(service.type)}</span></div>
        <div class="info-label">Health</div><div class="info-value"><div class="health-indicator"><span class="health-dot ${escapeAttribute(service.health || 'unknown')}"></span>${escapeHtml(service.health || 'unknown')}</div></div>
        <div class="info-label">Last Seen</div><div class="info-value">${lastSeen ? formatDateTime(lastSeen) : 'N/A'}</div>
        ${service.description ? `<div class="info-label">Description</div><div class="info-value">${escapeHtml(service.description)}</div>` : ''}
    </div></div>`;

    html += `<div class="info-section"><div class="info-section-title"><span class="section-icon">🌐</span> Network</div><div class="info-grid">
        <div class="info-label">Address</div><div class="info-value mono">${escapeHtml(service.address)}:${escapeHtml(String(service.port))}</div>
    </div></div>`;

    if (service.metadata && Object.keys(service.metadata).length > 0) {
        html += `<div class="info-section"><div class="info-section-title"><span class="section-icon">🏷️</span> Metadata</div><div class="info-grid">
            ${Object.entries(service.metadata).map(([key, value]) => `<div class="info-label">${escapeHtml(key)}</div><div class="info-value mono">${escapeHtml(String(value))}</div>`).join('')}
        </div></div>`;
    }

    if ((service.capabilities || []).some(cap => cap.name === 'inspect_application_records_v1' && cap.internal)) {
        const captured = inspectedRecord?.serviceID === service.id ? inspectedRecord.data : null;
        html += `<section class="info-section application-inspector" data-service-id="${escapeAttribute(service.id)}">
            <div class="info-section-title">Application records (read-only)</div>
            <p>Application outcomes are separate from framework DAG status. A record with no investigation has no DAG. Supply this component's authorized read credential for this request only.</p>
            <label>Read parameters (JSON)<textarea id="applicationRecordInput" rows="4" style="width:100%" spellcheck="false">{}</textarea></label>
            <label>Component read credential<input id="applicationRecordCredential" type="password" autocomplete="off" maxlength="4096" style="width:100%"></label>
            <button type="button" data-action="inspect-application-records">Read application records</button>
            <div id="applicationRecordStatus" role="status"></div>
            <div id="applicationRecordResult">${captured ? formatResponseJson(captured) : ''}</div>
        </section>`;
    }

    if (service.capabilities && service.capabilities.length > 0) {
        const externalCount = service.capabilities.filter(c => !c.internal).length;
        const internalCount = service.capabilities.filter(c => c.internal).length;
        const countLabel = internalCount > 0 ? `${service.capabilities.length} — <span style="color: var(--accent-green);">${externalCount} external</span>, <span style="color: var(--accent-orange);">${internalCount} internal</span>` : `${service.capabilities.length}`;
        html += `<div class="info-section"><div class="info-section-title"><span class="section-icon">⚡</span> Capabilities (${countLabel})</div>
            ${service.capabilities.map((cap, idx) => `<div class="capability-card">
                <div class="capability-name">${escapeHtml(cap.name)} <span class="cap-visibility-badge ${cap.internal ? 'internal' : 'external'}">${cap.internal ? 'internal' : 'external'}</span></div>
                ${cap.description ? `<div class="capability-desc">${escapeHtml(cap.description)}</div>` : ''}
                ${cap.endpoint ? `<div class="capability-endpoint">${escapeHtml(cap.endpoint)}</div>` : ''}
                ${cap.input_summary ? `<div class="capability-params">
                    ${(cap.input_summary.required || []).map(p => `<span class="param-tag required">${escapeHtml(p.name)}: ${escapeHtml(p.type)}</span>`).join('')}
                    ${(cap.input_summary.optional || []).map(p => `<span class="param-tag optional">${escapeHtml(p.name)}?: ${escapeHtml(p.type)}</span>`).join('')}
                </div>` : ''}
                <div class="dag-step-response-header" data-toggle-cap="cap-json-${idx}" style="margin-top: 12px; cursor: pointer; display: flex; justify-content: space-between; align-items: center; padding: 10px 14px; background: rgba(0, 0, 0, 0.2); border-radius: 10px; font-size: 12px; color: var(--text-secondary);">
                    <span><span class="expand-arrow" id="cap-arrow-${idx}">▶</span> View Full JSON</span>
                    <span style="color: var(--text-muted);">Click to expand</span>
                </div>
                <div id="cap-json-${idx}" class="dag-step-response-content" style="display: none; margin-top: 8px;">${formatResponseJson(cap)}</div>
            </div>`).join('')}
        </div>`;
    }

    html += `</div>`;
    return html;
}

function toggleCapabilityJson(elementId) {
    const content = document.getElementById(elementId);
    if (content) {
        const isHidden = content.style.display === 'none';
        content.style.display = isHidden ? 'block' : 'none';
        const idx = elementId.replace('cap-json-', '');
        const arrow = document.getElementById(`cap-arrow-${idx}`);
        if (arrow) arrow.textContent = isHidden ? '▼' : '▶';
    }
}

function copyJson(evt) {
    const text = JSON.stringify(selected, null, 2);
    copyToClipboard(text, evt);
}

async function fetchServices() {
    const refreshBtn = document.querySelector('.refresh-btn');
    const errorBanner = document.getElementById('errorBanner');
    const listPanel = document.getElementById('registryListPanel');
    refreshBtn.classList.add('loading');
    showLoading(listPanel, 'Loading services...');

    try {
        const { data } = await fetchAPI('/api/services');

        document.getElementById('totalCount').textContent = data.totalCount;
        document.getElementById('agentCount').textContent = data.agentCount;
        document.getElementById('toolCount').textContent = data.toolCount;

        // Backend canonicalizes to `[]` (see nonNilSlice in main.go); the
        // `|| []` here is belt-and-suspenders against a future backend
        // regression or alternate endpoint that slips a `null` through.
        services = (data.services || []).sort((a, b) => {
            if (a.type !== b.type) return a.type === 'agent' ? -1 : 1;
            return a.name.localeCompare(b.name);
        });

        filterServices();

        if (selected) {
            const updated = services.find(s => s.id === selected.id);
            if (updated) { selected = updated; renderDetail(); }
        }

        document.getElementById('lastUpdated').textContent = `Last updated: ${formatDateTime(data.timestamp)}`;
        errorBanner.classList.remove('visible');
    } catch (error) {
        console.error('Failed to fetch services:', error);
        document.getElementById('errorMessage').textContent = error.message;
        errorBanner.classList.add('visible');
    } finally {
        refreshBtn.classList.remove('loading');
        hideLoading(listPanel);
    }
}

// ---------------------------------------------------------------------------
// Event delegation setup
// ---------------------------------------------------------------------------

function handleTableClick(e) {
    const row = e.target.closest('tr[data-service-id]');
    if (row) {
        selectService(row.dataset.serviceId);
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
    const inspect = e.target.closest('[data-action="inspect-application-records"]');
    if (inspect) { void inspectApplicationRecords(inspect); return; }
    // Copy JSON button
    const copyBtn = e.target.closest('[data-action="copy-json"]');
    if (copyBtn) {
        copyJson(e);
        return;
    }

    // Toggle capability JSON expand/collapse
    const toggleHeader = e.target.closest('[data-toggle-cap]');
    if (toggleHeader) {
        toggleCapabilityJson(toggleHeader.dataset.toggleCap);
    }
}

async function inspectApplicationRecords(button) {
    const panel = button.closest('.application-inspector');
    const status = panel.querySelector('#applicationRecordStatus');
    const output = panel.querySelector('#applicationRecordResult');
    const credential = panel.querySelector('#applicationRecordCredential');
    button.disabled = true;
    status.textContent = 'Reading the registered component…';
    try {
        const input = JSON.parse(panel.querySelector('#applicationRecordInput').value);
        if (!input || Array.isArray(input) || typeof input !== 'object') throw new Error('Supply one JSON object.');
        const response = await fetch('/api/application-records', {
            method: 'POST', cache: 'no-store',
            headers: {'Content-Type': 'application/json', 'Authorization': `Bearer ${credential.value}`},
            body: JSON.stringify({service_id: panel.dataset.serviceId, input}),
        });
        // Credentials stay neither in component parameters nor durable UI state.
        credential.value = '';
        if (!response.ok) throw new Error(`Read rejected or unavailable (HTTP ${response.status}).`);
        const result = await response.json();
        if (result.success !== true || result.data?.schema_version !== 1 || !Array.isArray(result.data.records)) throw new Error('Unsupported inspection response.');
        inspectedRecord = {serviceID: panel.dataset.serviceId, data: result.data};
        output.innerHTML = formatResponseJson(result.data);
        status.textContent = result.data.truncated ? 'Bounded records returned; more records are omitted.' : 'Application records returned. No investigation was launched by this read.';
    } catch (error) {
        inspectedRecord = null;
        output.textContent = '';
        status.textContent = error.message;
    } finally {
        credential.value = '';
        button.disabled = false;
    }
}

function handleSearchInput() {
    filterServices();
}

// ---------------------------------------------------------------------------
// Exported lifecycle hooks
// ---------------------------------------------------------------------------

export function init() {
    // Attach delegated event listeners
    const tbody = document.getElementById('serviceTableBody');
    if (tbody) tbody.addEventListener('click', handleTableClick);

    const listPanel = document.getElementById('registryListPanel');
    if (listPanel) listPanel.addEventListener('click', handleFilterClick);

    const detailPanel = document.getElementById('registryDetailPanel');
    if (detailPanel) {
        detailPanel.addEventListener('click', handleDetailTabClick);
        detailPanel.addEventListener('click', handleDetailContentClick);
    }

    const searchInput = document.getElementById('searchInput');
    if (searchInput) searchInput.addEventListener('input', handleSearchInput);

    // Initial data load
    fetchServices();
}

export function destroy() {
    // Remove event listeners to avoid leaks if the view is torn down
    const tbody = document.getElementById('serviceTableBody');
    if (tbody) tbody.removeEventListener('click', handleTableClick);

    const listPanel = document.getElementById('registryListPanel');
    if (listPanel) listPanel.removeEventListener('click', handleFilterClick);

    const detailPanel = document.getElementById('registryDetailPanel');
    if (detailPanel) {
        detailPanel.removeEventListener('click', handleDetailTabClick);
        detailPanel.removeEventListener('click', handleDetailContentClick);
    }

    const searchInput = document.getElementById('searchInput');
    if (searchInput) searchInput.removeEventListener('input', handleSearchInput);

    // Reset state
    typeFilter = 'all';
    services = [];
    selected = null;
    activeTab = 'formatted';
    inspectedRecord = null;
}

export function refresh() {
    fetchServices();
}
