/**
 * Tabbed detail panel with lazy content rendering.
 *
 * Replaces: renderDetail, renderLLMDetail, renderHITLDetail,
 *           renderExecutionDetail, memory detail renderers (5 detail renderers)
 * Also replaces: setDetailTab, setLLMDetailTab, setHITLDetailTab,
 *                setDagDetailTab, setMemoryDetailTab (5 tab handlers)
 *
 * Tab content is rendered lazily — the render function for each tab is only
 * called when that tab becomes active, not pre-rendered and hidden.
 */

/**
 * Render a tabbed detail panel.
 *
 * @param {HTMLElement} container - the detail panel container
 * @param {object} config
 * @param {Array<{id: string, label: string, render: (container: HTMLElement) => void}>} config.tabs
 * @param {string} config.activeTab - ID of the currently active tab
 * @param {Function} config.onTabChange - (tabId: string) => void
 * @param {string} [config.emptyIcon] - emoji for no-selection state
 * @param {string} [config.emptyText] - text for no-selection state
 * @param {boolean} [config.hasSelection] - whether an item is selected (default: true)
 * @param {string} [config.title] - panel title text
 */
export function renderDetailPanel(container, config) {
    const {
        tabs, activeTab, onTabChange,
        emptyIcon = '', emptyText = 'Select an item to view details',
        hasSelection = true, title = '',
    } = config;

    if (!hasSelection) {
        container.innerHTML = `
            <div class="detail-header">
                <h3 class="detail-title">${title || 'Details'}</h3>
            </div>
            <div class="empty-detail">
                ${emptyIcon ? `<div class="empty-detail-icon">${emptyIcon}</div>` : ''}
                <div>${emptyText}</div>
            </div>`;
        return;
    }

    // Build tab bar
    const tabBar = tabs.map(tab =>
        `<button class="detail-tab ${tab.id === activeTab ? 'active' : ''}" data-tab="${tab.id}">${tab.label}</button>`
    ).join('');

    container.innerHTML = `
        <div class="detail-header">
            <h3 class="detail-title">${title}</h3>
            <div class="detail-tabs">${tabBar}</div>
        </div>
        <div class="detail-body" id="detailBody"></div>`;

    // Render active tab content
    const body = container.querySelector('#detailBody');
    const activeTabConfig = tabs.find(t => t.id === activeTab);
    if (activeTabConfig && body) {
        activeTabConfig.render(body);
    }

    // Delegated click handler for tab switching.
    // Remove previous handler before attaching (prevents leak on re-render).
    const tabsContainer = container.querySelector('.detail-tabs');
    if (tabsContainer && onTabChange) {
        if (container._tabClickHandler) {
            tabsContainer.removeEventListener('click', container._tabClickHandler);
        }
        container._tabClickHandler = (e) => {
            const btn = e.target.closest('.detail-tab');
            if (btn && btn.dataset.tab !== activeTab) {
                onTabChange(btn.dataset.tab);
            }
        };
        tabsContainer.addEventListener('click', container._tabClickHandler);
    }
}
