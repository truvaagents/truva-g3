/**
 * Memory view — domain events, investigations, digest, and activities.
 *
 * Extracted from index.html; keeps the original DOM-based rendering
 * unchanged so the migration is a straight lift-and-shift.
 */

import {
    formatTimeAgo,
    escapeHtml,
    syntaxHighlightJson,
    truncateText,
    renderMarkdown,
} from '../utils/format.js';

import { fetchAPI } from '../api.js';
import { showLoading, hideLoading } from '../utils/dom.js';

// ---------------------------------------------------------------------------
// Module-level state (replaces former globals)
// ---------------------------------------------------------------------------
let domain = localStorage.getItem('memoryDomain') || '';
let timeRange = '24h';
let events = [];
let investigations = [];
let activities = [];
let digest = null;
let selectedEvent = null;
let activeTab = 'mem-event';

let eventInterval = null;
let investigationInterval = null;
let activityInterval = null;
let digestInterval = null;

// ---------------------------------------------------------------------------
// Event delegation
// ---------------------------------------------------------------------------

function handleEventListClick(e) {
    const card = e.target.closest('.memory-event-card[data-event-idx]');
    if (!card) return;
    const idx = parseInt(card.dataset.eventIdx, 10);
    if (idx >= 0 && idx < events.length) {
        selectMemoryEvent(events[idx].event_id);
    }
}

function handleDomainChange(e) {
    setMemoryDomain(e.target.value);
}

function handleTimeRangeClick(e) {
    const btn = e.target.closest('.filter-btn[data-range]');
    if (!btn) return;
    setMemoryTimeRange(btn.dataset.range);
}

function handleDetailTabClick(e) {
    const tab = e.target.closest('.detail-tab[data-tab]');
    if (!tab) return;
    setMemoryDetailTab(tab.dataset.tab);
}

function bindDelegation() {
    const eventsList = document.getElementById('memoryEventsList');
    if (eventsList) eventsList.addEventListener('click', handleEventListClick);

    const domainSelect = document.getElementById('memoryDomainSelect');
    if (domainSelect) domainSelect.addEventListener('change', handleDomainChange);

    const listPanel = document.getElementById('memoryListPanel');
    if (listPanel) listPanel.addEventListener('click', handleTimeRangeClick);

    const detailPanel = document.getElementById('memoryDetailPanel');
    if (detailPanel) detailPanel.addEventListener('click', handleDetailTabClick);
}

function unbindDelegation() {
    const eventsList = document.getElementById('memoryEventsList');
    if (eventsList) eventsList.removeEventListener('click', handleEventListClick);

    const domainSelect = document.getElementById('memoryDomainSelect');
    if (domainSelect) domainSelect.removeEventListener('change', handleDomainChange);

    const listPanel = document.getElementById('memoryListPanel');
    if (listPanel) listPanel.removeEventListener('click', handleTimeRangeClick);

    const detailPanel = document.getElementById('memoryDetailPanel');
    if (detailPanel) detailPanel.removeEventListener('click', handleDetailTabClick);
}

// ---------------------------------------------------------------------------
// Data fetching
// ---------------------------------------------------------------------------

async function fetchMemoryDomains() {
    try {
        const { data } = await fetchAPI('/api/memory/domains');
        const select = document.getElementById('memoryDomainSelect');
        select.innerHTML = '';
        if (data.domains && data.domains.length > 0) {
            data.domains.forEach(d => {
                const opt = document.createElement('option');
                opt.value = d;
                opt.textContent = d;
                if (d === domain) opt.selected = true;
                select.appendChild(opt);
            });
            if (!domain || !data.domains.includes(domain)) {
                domain = data.domains[0];
                select.value = domain;
                localStorage.setItem('memoryDomain', domain);
            }
        } else {
            select.innerHTML = '<option value="">No domains found</option>';
        }
        document.getElementById('memoryDomainLabel').textContent = domain || '-';
    } catch (e) {
        console.error('Failed to fetch memory domains:', e);
        document.getElementById('memoryDomainSelect').innerHTML = '<option value="">Error loading domains</option>';
    }
}

async function fetchMemoryEvents() {
    if (!domain) return;
    try {
        const since = timeRange || '24h';
        const { data } = await fetchAPI(`/api/memory/events?domain=${encodeURIComponent(domain)}&since=${encodeURIComponent(since)}&limit=50`);
        events = data.events || [];
        document.getElementById('memoryEventCount').textContent = events.length;
        renderMemoryEvents();
        document.getElementById('memoryLastUpdated').textContent = `Updated ${formatTimeAgo(new Date().toISOString())}`;
    } catch (e) {
        console.error('Failed to fetch memory events:', e);
        document.getElementById('memoryEventsList').innerHTML = `<div class="empty-detail" style="padding: 40px 20px;"><div class="empty-detail-icon">\u26A0\uFE0F</div><div>Failed to load events</div></div>`;
    }
}

async function fetchMemoryInvestigations() {
    if (!domain) return;
    try {
        const { data } = await fetchAPI(`/api/memory/investigations?domain=${encodeURIComponent(domain)}`);
        investigations = data.investigations || [];
        document.getElementById('memoryInvestigationCount').textContent = investigations.length;
        renderMemoryInvestigations(investigations);
    } catch (e) {
        console.error('Failed to fetch memory investigations:', e);
    }
}

async function fetchMemoryDigest() {
    if (!domain) return;
    try {
        const { data } = await fetchAPI(`/api/memory/digest?domain=${encodeURIComponent(domain)}`);
        digest = data;
        if (activeTab === 'mem-digest') {
            renderMemoryDigest(digest);
        }
    } catch (e) {
        console.error('Failed to fetch memory digest:', e);
    }
}

async function fetchMemoryActivities() {
    if (!domain) return;
    try {
        const { data } = await fetchAPI(`/api/memory/activities?domain=${encodeURIComponent(domain)}`);
        activities = data.activities || [];
        if (activeTab === 'mem-activity') {
            renderMemoryActivities(activities);
        }
    } catch (e) {
        console.error('Failed to fetch memory activities:', e);
    }
}

async function fetchMemoryData() {
    const listPanel = document.getElementById('memoryListPanel');
    showLoading(listPanel, 'Loading memory data...');
    try {
        await fetchMemoryDomains();
        await Promise.all([
            fetchMemoryEvents(),
            fetchMemoryInvestigations(),
            fetchMemoryDigest(),
            fetchMemoryActivities()
        ]);
    } finally {
        hideLoading(listPanel);
    }
}

// ---------------------------------------------------------------------------
// Rendering
// ---------------------------------------------------------------------------

function renderMemoryEvents() {
    const container = document.getElementById('memoryEventsList');
    if (events.length === 0) {
        container.innerHTML = `<div class="empty-detail" style="padding: 60px 20px;"><div class="empty-detail-icon">\uD83E\uDDE0</div><div>No events in this time range</div></div>`;
        return;
    }
    container.innerHTML = events.map((evt, idx) => {
        const selectedClass = selectedEvent && selectedEvent.event_id === evt.event_id ? 'selected' : '';
        const importance = Math.round(evt.importance || 0);

        // Outcome-based ambient glow colors (matching interaction-card pattern)
        const _oc = evt.outcome === 'success' ? [50,215,75] : evt.outcome === 'failure' ? [255,107,107] : [255,179,64];
        const _rgb = `${_oc[0]}, ${_oc[1]}, ${_oc[2]}`;
        const _baseShadow = `0 0 16px rgba(${_rgb}, 0.10), 0 0 5px rgba(${_rgb}, 0.16), 0 4px 20px rgba(0,0,0,0.2), inset 0 1px 0 rgba(255,255,255,0.08)`;
        const _hoverShadow = `0 0 28px rgba(${_rgb}, 0.18), 0 0 10px rgba(${_rgb}, 0.32), 0 8px 32px rgba(0,0,0,0.28), inset 0 1px 0 rgba(255,255,255,0.14)`;

        // Outcome badge colors
        const outcomeBadgeBg = evt.outcome === 'success' ? 'rgba(50,215,75,0.2)' : evt.outcome === 'failure' ? 'rgba(255,107,107,0.2)' : 'rgba(255,179,64,0.2)';
        const outcomeBadgeColor = evt.outcome === 'success' ? 'var(--accent-green)' : evt.outcome === 'failure' ? 'var(--accent-red)' : 'var(--accent-orange)';
        const outcomeBadgeBorder = evt.outcome === 'success' ? 'rgba(50,215,75,0.3)' : evt.outcome === 'failure' ? 'rgba(255,107,107,0.3)' : 'rgba(255,179,64,0.3)';

        // Scope badge
        const scopeColor = evt.scope === 'global' ? 'var(--accent-purple)' : evt.scope === 'shared_domain' ? 'var(--accent-teal)' : 'var(--text-muted)';
        const scopeBg = evt.scope === 'global' ? 'rgba(218,143,255,0.15)' : evt.scope === 'shared_domain' ? 'rgba(100,210,255,0.15)' : 'rgba(255,255,255,0.06)';

        // Truncated summary preview
        const preview = truncateText(evt.summary || '', 70);

        return `<div class="memory-event-card ${selectedClass}" data-event-idx="${idx}"
            style="border-color: rgba(${_rgb}, 0.25); box-shadow: ${_baseShadow};"
            onmouseenter="this.style.boxShadow='${_hoverShadow}'; this.style.borderColor='rgba(${_rgb}, 0.4)';"
            onmouseleave="this.style.boxShadow='${_baseShadow}'; this.style.borderColor='rgba(${_rgb}, 0.25)';">
            <div style="display: flex; align-items: center; gap: 8px; margin-bottom: 6px;">
                <span style="display: inline-flex; align-items: center; justify-content: center; min-width: 130px; padding: 4px 10px; border-radius: 20px; font-size: 10px; font-weight: 600; text-transform: uppercase; letter-spacing: 0.5px; background: ${outcomeBadgeBg}; color: ${outcomeBadgeColor}; border: 1px solid ${outcomeBadgeBorder};">${escapeHtml(evt.action_type || '-')}</span>
                <span style="font-size: 11px; color: var(--text-muted); flex: 1; overflow: hidden; text-overflow: ellipsis; white-space: nowrap;">${escapeHtml(preview)}</span>
            </div>
            <div style="display: flex; align-items: center; justify-content: space-between;">
                <div style="display: flex; align-items: center; gap: 8px;">
                    <span style="font-size: 11px; color: var(--text-muted);">${escapeHtml(evt.entity_type || '')}${evt.entity_id ? '/' + escapeHtml(evt.entity_id) : ''}</span>
                </div>
                <div style="display: flex; align-items: center; gap: 10px;">
                    <span class="memory-scope-badge" style="background: ${scopeBg}; color: ${scopeColor};">${escapeHtml(evt.scope || 'private')}</span>
                    <span style="font-size: 10.5px; color: rgba(220,170,140,0.75); font-family: 'SF Mono', monospace;">${importance}/10</span>
                    <span style="font-size: 10.5px; color: rgba(130,200,180,0.75);">${formatTimeAgo(evt.timestamp)}</span>
                    <span style="font-size: 10.5px; color: rgba(180,160,210,0.8);">${escapeHtml(evt.agent_name || '')}</span>
                </div>
            </div>
        </div>`;
    }).join('');
}

function selectMemoryEvent(eventId) {
    selectedEvent = events.find(e => e.event_id === eventId) || null;
    renderMemoryEvents();
    if (selectedEvent) {
        activeTab = 'mem-event';
        document.querySelectorAll('#memoryDetailPanel .detail-tab').forEach(btn => {
            btn.classList.toggle('active', btn.dataset.tab === 'mem-event');
        });
        renderMemoryEventDetail(selectedEvent);
    }
}

function renderMemoryEventDetail(event) {
    const content = document.getElementById('memoryDetailContent');
    document.getElementById('memoryDetailTitle').textContent = `${event.action_type || 'Event'} \u2014 ${event.entity_type || ''}${event.entity_id ? ' #' + event.entity_id : ''}`;

    const outcomeColor = event.outcome === 'success' ? 'var(--accent-green)' : event.outcome === 'failure' ? 'var(--accent-red)' : 'var(--accent-orange)';
    const importance = Math.round(event.importance || 0);

    let detailHtml = `<div style="padding: 20px; display: flex; flex-direction: column; gap: 16px;">`;

    // Header row
    detailHtml += `<div style="display: flex; gap: 12px; flex-wrap: wrap;">
        <div style="flex: 1; min-width: 200px; padding: 14px; background: rgba(255,255,255,0.04); border-radius: 12px; border: 1px solid var(--border-subtle);">
            <div style="font-size: 11px; color: var(--text-muted); margin-bottom: 6px; text-transform: uppercase; letter-spacing: 0.5px;">Outcome</div>
            <div style="font-size: 16px; font-weight: 600; color: ${outcomeColor};">${escapeHtml(event.outcome || 'unknown')}</div>
        </div>
        <div style="flex: 1; min-width: 200px; padding: 14px; background: rgba(255,255,255,0.04); border-radius: 12px; border: 1px solid var(--border-subtle);">
            <div style="font-size: 11px; color: var(--text-muted); margin-bottom: 6px; text-transform: uppercase; letter-spacing: 0.5px;">Importance</div>
            <div style="font-size: 16px; font-weight: 600; color: var(--accent-orange); font-family: 'SF Mono', monospace;">${importance}/10</div>
        </div>
    </div>`;

    // Info grid
    const fields = [
        { label: 'Event ID', value: event.event_id },
        { label: 'Agent', value: event.agent_name },
        { label: 'Domain', value: event.agent_domain },
        { label: 'Scope', value: event.scope },
        { label: 'Action', value: event.action_type },
        { label: 'Entity', value: (event.entity_type || '') + (event.entity_id ? ' #' + event.entity_id : '') },
        { label: 'Timestamp', value: event.timestamp ? new Date(event.timestamp).toLocaleString() + ' (' + formatTimeAgo(event.timestamp) + ')' : '-' },
        { label: 'Request ID', value: event.request_id },
    ];

    detailHtml += `<div style="display: grid; grid-template-columns: repeat(auto-fill, minmax(220px, 1fr)); gap: 8px;">`;
    fields.forEach(f => {
        if (f.value) {
            detailHtml += `<div style="padding: 10px 14px; background: rgba(255,255,255,0.03); border-radius: 8px; border: 1px solid var(--border-subtle);">
                <div style="font-size: 10px; color: var(--text-muted); text-transform: uppercase; letter-spacing: 0.5px; margin-bottom: 4px;">${f.label}</div>
                <div style="font-size: 13px; color: var(--text-primary); word-break: break-all;">${escapeHtml(String(f.value))}</div>
            </div>`;
        }
    });
    detailHtml += `</div>`;

    // Summary
    if (event.summary) {
        detailHtml += `<div style="padding: 14px; background: rgba(255,255,255,0.04); border-radius: 12px; border: 1px solid var(--border-subtle);">
            <div style="font-size: 11px; color: var(--text-muted); margin-bottom: 8px; text-transform: uppercase; letter-spacing: 0.5px;">Summary</div>
            <div style="font-size: 13px; color: var(--text-secondary); line-height: 1.6;">${escapeHtml(event.summary)}</div>
        </div>`;
    }

    // Entities
    if (event.entities && event.entities.length > 0) {
        detailHtml += `<div style="padding: 14px; background: rgba(255,255,255,0.04); border-radius: 12px; border: 1px solid var(--border-subtle);">
            <div style="font-size: 11px; color: var(--text-muted); margin-bottom: 8px; text-transform: uppercase; letter-spacing: 0.5px;">Entities</div>
            <div style="display: flex; flex-wrap: wrap; gap: 6px;">
                ${event.entities.map(e => `<span style="padding: 4px 10px; background: rgba(100,210,255,0.1); border: 1px solid rgba(100,210,255,0.2); border-radius: 6px; font-size: 11px; color: var(--accent-teal);">${escapeHtml(e.type)}/${escapeHtml(e.id)}</span>`).join('')}
            </div>
        </div>`;
    }

    // Metadata
    if (event.metadata && Object.keys(event.metadata).length > 0) {
        const ctx = event.metadata;
        detailHtml += `<div style="padding: 14px; background: rgba(255,255,255,0.04); border-radius: 12px; border: 1px solid var(--border-subtle);">
            <div style="font-size: 11px; color: var(--text-muted); margin-bottom: 8px; text-transform: uppercase; letter-spacing: 0.5px;">Metadata</div>
            <pre class="json-view" style="font-size: 12px; line-height: 1.5; white-space: pre-wrap; word-break: break-all; margin: 0;">${syntaxHighlightJson(ctx)}</pre>
        </div>`;
    }

    // Raw JSON (syntax highlighted)
    detailHtml += `<details style="padding: 14px; background: rgba(255,255,255,0.03); border-radius: 12px; border: 1px solid var(--border-subtle);">
        <summary style="font-size: 11px; color: var(--text-muted); text-transform: uppercase; letter-spacing: 0.5px; cursor: pointer;">Raw JSON</summary>
        <pre class="json-view" style="font-size: 11px; line-height: 1.4; white-space: pre-wrap; word-break: break-all; margin-top: 8px;">${syntaxHighlightJson(event)}</pre>
    </details>`;

    detailHtml += `</div>`;
    content.innerHTML = detailHtml;
}

function renderMemoryInvestigations(invs) {
    const strip = document.getElementById('memoryInvestigationsStrip');
    if (!invs || invs.length === 0) {
        strip.style.display = 'none';
        return;
    }
    strip.style.display = 'block';
    strip.innerHTML = invs.map(inv => {
        const entity = inv.entity_id || 'unknown';
        const holder = inv.holder || '';
        return `<span class="memory-pill" title="${escapeHtml(holder ? 'Held by ' + holder : 'Unclaimed')}">
            ${escapeHtml(entity)}${holder ? ' <span style="opacity:0.7;">(' + escapeHtml(holder) + ')</span>' : ''}
        </span>`;
    }).join('');
}

function renderMemoryDigest(data) {
    const content = document.getElementById('memoryDetailContent');
    if (!data || !data.available || !data.digest) {
        const msg = data?.message || 'No digest available for this domain';
        content.innerHTML = `<div class="empty-detail" style="padding: 60px 20px;"><div class="empty-detail-icon">\uD83D\uDCCB</div><div>${escapeHtml(msg)}</div></div>`;
        return;
    }
    const text = data.digest;
    content.innerHTML = `<div style="padding: 20px; display: flex; flex-direction: column; gap: 12px;">
        <div style="display: flex; justify-content: space-between; align-items: center;">
            <span style="font-size: 14px; font-weight: 600; color: var(--text-primary);">Domain Digest</span>
            <span style="font-size: 11px; color: var(--text-muted);">${data.generated_at ? 'Generated ' + formatTimeAgo(data.generated_at) : 'Domain: ' + escapeHtml(data.domain || domain)}</span>
        </div>
        <div style="padding: 20px; background: rgba(0,0,0,0.3); border-radius: 12px; border: 1px solid var(--border-subtle); font-size: 13px; color: var(--text-secondary); line-height: 1.6;">
            ${renderMarkdown(text)}
        </div>
        ${data.raw ? `<details style="padding: 14px; background: rgba(255,255,255,0.03); border-radius: 12px; border: 1px solid var(--border-subtle);">
            <summary style="font-size: 11px; color: var(--text-muted); text-transform: uppercase; letter-spacing: 0.5px; cursor: pointer;">Raw JSON</summary>
            <pre class="json-view" style="font-size: 11px; line-height: 1.4; white-space: pre-wrap; word-break: break-all; margin-top: 8px;">${syntaxHighlightJson(data.raw)}</pre>
        </details>` : ''}
    </div>`;
}

function renderMemoryActivities(acts) {
    const content = document.getElementById('memoryDetailContent');
    if (!acts || acts.length === 0) {
        content.innerHTML = `<div class="empty-detail" style="padding: 60px 20px;"><div class="empty-detail-icon">\uD83D\uDCE1</div><div>No live activity signals</div></div>`;
        return;
    }
    content.innerHTML = `<div style="padding: 16px; display: flex; flex-direction: column; gap: 8px;">
        ${acts.map(act => {
            const s = act.status || 'unknown';
            const statusColor = s === 'planning' ? 'var(--accent-blue)' : s === 'synthesizing' ? 'var(--accent-purple)' : s.startsWith('executing') ? 'var(--accent-green)' : s === 'completed' ? 'var(--text-muted)' : 'var(--accent-orange)';
            const statusBg = s === 'planning' ? 'rgba(10,132,255,0.15)' : s === 'synthesizing' ? 'rgba(191,90,242,0.15)' : s.startsWith('executing') ? 'rgba(50,215,75,0.15)' : s === 'completed' ? 'rgba(255,255,255,0.06)' : 'rgba(255,179,64,0.15)';
            const startedAt = act.started_at || act.timestamp;
            const meta = act.metadata || {};
            const metaEntries = Object.entries(meta);
            return `<div style="padding: 14px; background: rgba(255,255,255,0.04); border-radius: 12px; border: 1px solid var(--border-subtle);">
                <div style="display: flex; justify-content: space-between; align-items: flex-start;">
                    <div style="flex: 1; min-width: 0;">
                        <div style="display: flex; align-items: center; gap: 8px;">
                            <span style="font-size: 13px; font-weight: 600; color: var(--text-primary);">${escapeHtml(act.agent_name || 'unknown')}</span>
                            ${act.agent_domain ? `<span style="font-size: 11px; color: var(--text-muted); background: rgba(255,255,255,0.06); padding: 2px 6px; border-radius: 6px;">${escapeHtml(act.agent_domain)}</span>` : ''}
                        </div>
                        ${act.query ? `<div style="font-size: 12px; color: var(--text-secondary); margin-top: 6px; line-height: 1.4; overflow: hidden; text-overflow: ellipsis; display: -webkit-box; -webkit-line-clamp: 2; -webkit-box-orient: vertical;">${escapeHtml(act.query)}</div>` : ''}
                        <div style="display: flex; align-items: center; gap: 12px; margin-top: 6px;">
                            <span style="font-size: 11px; color: var(--text-muted);">${startedAt ? formatTimeAgo(startedAt) : '-'}</span>
                            ${act.request_id ? `<span style="font-size: 10px; font-family: var(--font-mono); color: var(--text-muted); opacity: 0.7;">${escapeHtml(act.request_id.substring(0, 16))}…</span>` : ''}
                        </div>
                        ${metaEntries.length > 0 ? `<div style="display: flex; flex-wrap: wrap; gap: 4px; margin-top: 6px;">${metaEntries.map(([k,v]) => `<span style="font-size: 10px; color: var(--text-muted); background: rgba(255,255,255,0.04); padding: 2px 6px; border-radius: 4px;">${escapeHtml(k)}: ${escapeHtml(v)}</span>`).join('')}</div>` : ''}
                    </div>
                    <span style="padding: 4px 10px; border-radius: 10px; font-size: 11px; font-weight: 600; background: ${statusBg}; color: ${statusColor}; text-transform: uppercase; white-space: nowrap; margin-left: 12px;">${escapeHtml(s)}</span>
                </div>
            </div>`;
        }).join('')}
    </div>`;
}

// ---------------------------------------------------------------------------
// State mutators
// ---------------------------------------------------------------------------

function setMemoryTimeRange(val) {
    timeRange = val;
    document.querySelectorAll('#memoryListPanel .filter-btn').forEach(btn => {
        btn.classList.toggle('active', btn.dataset.range === val);
    });
    fetchMemoryEvents();
}

function setMemoryDomain(d) {
    domain = d;
    localStorage.setItem('memoryDomain', d);
    document.getElementById('memoryDomainLabel').textContent = d || '-';
    fetchMemoryEvents();
    fetchMemoryInvestigations();
    fetchMemoryDigest();
    fetchMemoryActivities();
}

function setMemoryDetailTab(tab) {
    activeTab = tab;
    document.querySelectorAll('#memoryDetailPanel .detail-tab').forEach(btn => {
        btn.classList.toggle('active', btn.dataset.tab === tab);
    });
    // Start/stop polling based on which tab is now visible
    updateTabPolling();
    // Fetch fresh data immediately on tab switch (don't wait for interval)
    if (tab === 'mem-activity') fetchMemoryActivities();
    if (tab === 'mem-digest') fetchMemoryDigest();
    if (tab === 'mem-event') {
        if (selectedEvent) {
            renderMemoryEventDetail(selectedEvent);
        } else {
            document.getElementById('memoryDetailContent').innerHTML = `<div class="empty-detail"><div class="empty-detail-icon">\uD83E\uDDE0</div><div>Select an event to view details</div></div>`;
        }
    } else if (tab === 'mem-activity') {
        renderMemoryActivities(activities);
    } else if (tab === 'mem-digest') {
        renderMemoryDigest(digest);
    }
}

// ---------------------------------------------------------------------------
// Interval management
// ---------------------------------------------------------------------------

function setupMemoryIntervals() {
    clearMemoryIntervals();
    // Events and investigations always poll (visible in list panel)
    eventInterval = setInterval(fetchMemoryEvents, 20000);
    investigationInterval = setInterval(fetchMemoryInvestigations, 20000);
    // Activities and digest only poll when their tab is active
    updateTabPolling();
}

// Start/stop activity and digest polling based on the active detail tab.
// Called on setup and on every tab switch.
function updateTabPolling() {
    // Activities (live activity): poll at 5s when visible — this is the
    // real-time feed operators watch during an incident.
    if (activeTab === 'mem-activity') {
        if (!activityInterval) activityInterval = setInterval(fetchMemoryActivities, 5000);
    } else {
        if (activityInterval) { clearInterval(activityInterval); activityInterval = null; }
    }
    // Digest: poll at 20s when visible — LLM compaction output, changes slowly.
    if (activeTab === 'mem-digest') {
        if (!digestInterval) digestInterval = setInterval(fetchMemoryDigest, 20000);
    } else {
        if (digestInterval) { clearInterval(digestInterval); digestInterval = null; }
    }
}

function clearMemoryIntervals() {
    if (eventInterval) { clearInterval(eventInterval); eventInterval = null; }
    if (investigationInterval) { clearInterval(investigationInterval); investigationInterval = null; }
    if (activityInterval) { clearInterval(activityInterval); activityInterval = null; }
    if (digestInterval) { clearInterval(digestInterval); digestInterval = null; }
}

// ---------------------------------------------------------------------------
// Public API
// ---------------------------------------------------------------------------

export async function init() {
    bindDelegation();
    await fetchMemoryDomains();
    await fetchMemoryData();
    setupMemoryIntervals();
}

export async function refresh() {
    await fetchMemoryData();
}

export function destroy() {
    clearMemoryIntervals();
    unbindDelegation();
}
