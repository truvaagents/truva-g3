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
        <tr data-service-id="${service.id}" class="${selected?.id === service.id ? 'selected' : ''}">
            <td><span class="type-badge ${service.type}">${service.type}</span></td>
            <td><span class="service-name">${service.name}</span></td>
            <td><div class="health-indicator"><span class="health-dot ${service.health || 'unknown'}"></span>${service.health || 'unknown'}</div></td>
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
        <div class="info-label">ID</div><div class="info-value mono">${service.id}</div>
        <div class="info-label">Name</div><div class="info-value">${service.name}</div>
        <div class="info-label">Type</div><div class="info-value"><span class="type-badge ${service.type}">${service.type}</span></div>
        <div class="info-label">Health</div><div class="info-value"><div class="health-indicator"><span class="health-dot ${service.health || 'unknown'}"></span>${service.health || 'unknown'}</div></div>
        <div class="info-label">Last Seen</div><div class="info-value">${lastSeen ? formatDateTime(lastSeen) : 'N/A'}</div>
        ${service.description ? `<div class="info-label">Description</div><div class="info-value">${service.description}</div>` : ''}
    </div></div>`;

    html += `<div class="info-section"><div class="info-section-title"><span class="section-icon">🌐</span> Network</div><div class="info-grid">
        <div class="info-label">Address</div><div class="info-value mono">${service.address}:${service.port}</div>
    </div></div>`;

    if (service.metadata && Object.keys(service.metadata).length > 0) {
        html += `<div class="info-section"><div class="info-section-title"><span class="section-icon">🏷️</span> Metadata</div><div class="info-grid">
            ${Object.entries(service.metadata).map(([key, value]) => `<div class="info-label">${key}</div><div class="info-value mono">${value}</div>`).join('')}
        </div></div>`;
    }

    if (service.capabilities && service.capabilities.length > 0) {
        const externalCount = service.capabilities.filter(c => !c.internal).length;
        const internalCount = service.capabilities.filter(c => c.internal).length;
        const countLabel = internalCount > 0 ? `${service.capabilities.length} — <span style="color: var(--accent-green);">${externalCount} external</span>, <span style="color: var(--accent-orange);">${internalCount} internal</span>` : `${service.capabilities.length}`;
        html += `<div class="info-section"><div class="info-section-title"><span class="section-icon">⚡</span> Capabilities (${countLabel})</div>
            ${service.capabilities.map((cap, idx) => `<div class="capability-card">
                <div class="capability-name">${cap.name} <span class="cap-visibility-badge ${cap.internal ? 'internal' : 'external'}">${cap.internal ? 'internal' : 'external'}</span></div>
                ${cap.description ? `<div class="capability-desc">${cap.description}</div>` : ''}
                ${cap.endpoint ? `<div class="capability-endpoint">${cap.endpoint}</div>` : ''}
                ${cap.input_summary ? `<div class="capability-params">
                    ${(cap.input_summary.required || []).map(p => `<span class="param-tag required">${p.name}: ${p.type}</span>`).join('')}
                    ${(cap.input_summary.optional || []).map(p => `<span class="param-tag optional">${p.name}?: ${p.type}</span>`).join('')}
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
}

export function refresh() {
    fetchServices();
}
