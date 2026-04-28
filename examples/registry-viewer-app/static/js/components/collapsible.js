/**
 * Collapsible section with deferred content rendering.
 *
 * Content DOM is created only on first expansion, not pre-rendered.
 * Subsequent toggles show/hide the already-rendered content.
 *
 * Replaces the pattern of pre-rendering all content and toggling display:none.
 */

/**
 * Create a collapsible section in a container.
 *
 * @param {HTMLElement} container - parent element to append the collapsible to
 * @param {object} config
 * @param {string} config.label - header text
 * @param {Function} config.renderContent - (contentContainer: HTMLElement) => void
 *   Called on first expand to populate the content area.
 * @param {boolean} [config.defaultOpen=false] - whether to start expanded
 * @param {string} [config.className] - additional CSS class for the wrapper
 * @returns {{ toggle: () => void, element: HTMLElement }}
 */
export function createCollapsible(container, config) {
    const {
        label,
        renderContent,
        defaultOpen = false,
        className = '',
    } = config;

    const wrapper = document.createElement('div');
    wrapper.className = `collapsible-section ${className}`.trim();

    const header = document.createElement('button');
    header.style.cssText = `
        width: 100%; padding: 8px 16px; background: none; border: none;
        border-top: 1px solid rgba(255, 255, 255, 0.06);
        color: var(--text-muted); cursor: pointer; text-align: left;
        font-size: 12px; display: flex; align-items: center; gap: 6px;
    `;
    header.innerHTML = `<span class="collapsible-chevron">${defaultOpen ? '▼' : '▶'}</span> ${label}`;

    const content = document.createElement('div');
    content.style.display = defaultOpen ? 'block' : 'none';
    content.style.padding = '0 16px 12px';

    let rendered = false;

    function toggle() {
        const isOpen = content.style.display !== 'none';
        if (!isOpen && !rendered) {
            renderContent(content);
            rendered = true;
        }
        content.style.display = isOpen ? 'none' : 'block';
        header.querySelector('.collapsible-chevron').textContent = isOpen ? '▶' : '▼';
    }

    if (defaultOpen && !rendered) {
        renderContent(content);
        rendered = true;
    }

    header.addEventListener('click', toggle);
    wrapper.appendChild(header);
    wrapper.appendChild(content);
    container.appendChild(wrapper);

    return { toggle, element: wrapper };
}
