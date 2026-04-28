/**
 * Application Shell.
 *
 * Handles view switching, auto-refresh, resizable panels, and initialization.
 * Imports view modules and manages their lifecycle (init/destroy/refresh).
 */

import * as registry from './views/registry.js';
import * as llmDebug from './views/llm-debug.js';
import * as hitl from './views/hitl.js';
import * as dag from './views/dag.js';
import * as memory from './views/memory.js';

// ─── View Registry ──────────────────────────────────────────────────────────

const views = {
    'registry': registry,
    'llm-debug': llmDebug,
    'hitl': hitl,
    'dag': dag,
    'memory': memory,
};

let currentView = 'registry';
let activeViewModule = null;
let autoRefreshInterval = null;

// ─── View Switching ─────────────────────────────────────────────────────────

/**
 * Switch to a different view. Destroys the previous view and initializes
 * the new one.
 */
function switchView(view) {
    // Destroy previous view
    if (activeViewModule && activeViewModule.destroy) {
        activeViewModule.destroy();
    }

    currentView = view;
    activeViewModule = views[view];

    // Update nav buttons
    document.querySelectorAll('.main-nav-btn').forEach(btn => {
        btn.classList.toggle('active', btn.dataset.view === view);
    });

    // Toggle layout class for different views
    const mainContainer = document.getElementById('mainContainer');
    mainContainer.classList.remove('llm-debug-view', 'hitl-view', 'dag-view', 'memory-view');
    if (view === 'llm-debug') mainContainer.classList.add('llm-debug-view');
    if (view === 'hitl') mainContainer.classList.add('hitl-view');
    if (view === 'dag') mainContainer.classList.add('dag-view');
    if (view === 'memory') mainContainer.classList.add('memory-view');

    // Toggle panels
    document.getElementById('registryListPanel').classList.toggle('hidden', view !== 'registry');
    document.getElementById('registryDetailPanel').classList.toggle('hidden', view !== 'registry');
    document.getElementById('registryResizeHandle').classList.toggle('hidden', view !== 'registry');
    document.getElementById('llmDebugListPanel').classList.toggle('hidden', view !== 'llm-debug');
    document.getElementById('llmDebugDetailPanel').classList.toggle('hidden', view !== 'llm-debug');
    document.getElementById('llmResizeHandle').classList.toggle('hidden', view !== 'llm-debug');
    document.getElementById('hitlListPanel').classList.toggle('hidden', view !== 'hitl');
    document.getElementById('hitlDetailPanel').classList.toggle('hidden', view !== 'hitl');
    document.getElementById('hitlResizeHandle').classList.toggle('hidden', view !== 'hitl');
    document.getElementById('dagListPanel').classList.toggle('hidden', view !== 'dag');
    document.getElementById('dagDetailPanel').classList.toggle('hidden', view !== 'dag');
    document.getElementById('dagResizeHandle').classList.toggle('hidden', view !== 'dag');
    document.getElementById('memoryListPanel').classList.toggle('hidden', view !== 'memory');
    document.getElementById('memoryDetailPanel').classList.toggle('hidden', view !== 'memory');
    document.getElementById('memoryResizeHandle').classList.toggle('hidden', view !== 'memory');

    // Toggle stats
    document.getElementById('registryStats').classList.toggle('hidden', view !== 'registry');
    document.getElementById('llmDebugStats').classList.toggle('hidden', view !== 'llm-debug');
    document.getElementById('hitlStats').classList.toggle('hidden', view !== 'hitl');
    document.getElementById('dagStats').classList.toggle('hidden', view !== 'dag');
    document.getElementById('memoryStats').classList.toggle('hidden', view !== 'memory');

    // Handle auto-refresh per view — restart the interval so it picks up
    // the new view's cadence from VIEW_REFRESH_INTERVALS.
    updateAutoRefresh();

    // Initialize the new view
    if (activeViewModule && activeViewModule.init) {
        activeViewModule.init();
    }
}

// ─── Auto Refresh ───────────────────────────────────────────────────────────

// Per-view refresh intervals (ms). Views not listed here use 5000 ms default.
// LLM Debug is force-disabled via the checkbox (see switchView), so it never
// reaches the interval lookup.
const VIEW_REFRESH_INTERVALS = {
    'registry':  15000,  // Services register/deregister infrequently
    'llm-debug': 30000,  // High volume, keep polling gentle
    'dag':       30000,  // Execution list changes slowly
    'hitl':       5000,  // Time-sensitive approvals
    // 'memory'   — runs its own per-endpoint timers, global refresh still fires
};

function getActiveViewInterval() {
    return VIEW_REFRESH_INTERVALS[currentView] || 5000;
}

function updateAutoRefresh() {
    if (autoRefreshInterval) {
        clearInterval(autoRefreshInterval);
        autoRefreshInterval = null;
    }

    const checkbox = document.getElementById('autoRefresh');
    if (checkbox.checked) {
        autoRefreshInterval = setInterval(() => {
            if (activeViewModule && activeViewModule.refresh) {
                activeViewModule.refresh();
            }
        }, getActiveViewInterval());
    }
}

function setupAutoRefresh() {
    const checkbox = document.getElementById('autoRefresh');
    checkbox.addEventListener('change', updateAutoRefresh);
    updateAutoRefresh();
}

// ─── Manual Refresh ─────────────────────────────────────────────────────────

function handleRefresh() {
    const refreshBtn = document.querySelector('.refresh-btn');
    refreshBtn.classList.add('loading');

    if (activeViewModule && activeViewModule.refresh) {
        // refresh() is async in most views — wait for it, then remove loading
        Promise.resolve(activeViewModule.refresh()).finally(() => {
            refreshBtn.classList.remove('loading');
        });
    } else {
        refreshBtn.classList.remove('loading');
    }
}

// ─── Resizable Panels ──────────────────────────────────────────────────────

function setupResizeHandle(handleId, panelId, defaultWidth) {
    const resizeHandle = document.getElementById(handleId);
    const listPanel = document.getElementById(panelId);
    const mainContainer = document.getElementById('mainContainer');

    if (!resizeHandle || !listPanel) return;

    let isResizing = false;
    let startX = 0;
    let startWidth = 0;

    resizeHandle.addEventListener('mousedown', (e) => {
        isResizing = true;
        startX = e.clientX;
        startWidth = listPanel.offsetWidth;
        resizeHandle.classList.add('active');
        document.body.classList.add('resizing');
        e.preventDefault();
    });

    document.addEventListener('mousemove', (e) => {
        if (!isResizing) return;

        const containerWidth = mainContainer.offsetWidth;
        const deltaX = e.clientX - startX;
        let newWidth = startWidth + deltaX;

        const minWidth = 280;
        const maxWidth = containerWidth * 0.7;
        newWidth = Math.max(minWidth, Math.min(maxWidth, newWidth));

        const widthPercent = (newWidth / containerWidth) * 100;
        listPanel.style.width = `${widthPercent}%`;
    });

    document.addEventListener('mouseup', () => {
        if (isResizing) {
            isResizing = false;
            resizeHandle.classList.remove('active');
            document.body.classList.remove('resizing');
        }
    });

    resizeHandle.addEventListener('dblclick', () => {
        listPanel.style.width = defaultWidth;
    });
}

function setupResizablePanels() {
    setupResizeHandle('registryResizeHandle', 'registryListPanel', '35%');
    setupResizeHandle('llmResizeHandle', 'llmDebugListPanel', '35%');
    setupResizeHandle('hitlResizeHandle', 'hitlListPanel', '40%');
    setupResizeHandle('dagResizeHandle', 'dagListPanel', '35%');
    setupResizeHandle('memoryResizeHandle', 'memoryListPanel', '35%');
}

// ─── Global Event Delegation ────────────────────────────────────────────────

function setupGlobalHandlers() {
    // Navigation buttons
    document.querySelector('.main-nav').addEventListener('click', (e) => {
        const btn = e.target.closest('.main-nav-btn');
        if (btn && btn.dataset.view) {
            switchView(btn.dataset.view);
        }
    });

    // Refresh button
    document.querySelector('.refresh-btn').addEventListener('click', handleRefresh);

    // Pause polling when tab is not visible
    document.addEventListener('visibilitychange', () => {
        if (document.hidden) {
            if (autoRefreshInterval) {
                clearInterval(autoRefreshInterval);
                autoRefreshInterval = null;
            }
        } else {
            updateAutoRefresh();
        }
    });
}

// ─── Initialize ─────────────────────────────────────────────────────────────

document.addEventListener('DOMContentLoaded', () => {
    setupGlobalHandlers();
    setupAutoRefresh();
    setupResizablePanels();

    // Initialize the default view with full setup (nav active class, panel visibility, etc.)
    switchView('registry');
});
