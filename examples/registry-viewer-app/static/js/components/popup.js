/**
 * Unified popup system.
 *
 * Replaces: showFullFlowNodePopup (line 6052), showNodePopup (line 6561),
 *           CSS data-tooltip system (line 1136).
 *
 * One implementation for all views — DAG node clicks, badge hovers,
 * memory node clicks, and future user memory group clicks.
 */

/** @type {HTMLElement[]} */
let activePopups = [];

/**
 * Show a popup anchored to a DOM element or a position.
 *
 * @param {HTMLElement|{x: number, y: number}} anchor - DOM element or screen coordinates
 * @param {string|HTMLElement} content - HTML string or DOM element
 * @param {object} [options]
 * @param {number} [options.maxWidth=400] - maximum popup width in px
 * @param {'auto'|'top'|'bottom'} [options.position='auto'] - preferred position
 * @param {boolean} [options.dismissOnClickOutside=true] - dismiss when clicking outside
 * @param {string} [options.className] - additional CSS class for the popup
 * @returns {{ dismiss: () => void, element: HTMLElement }}
 */
export function showPopup(anchor, content, options = {}) {
    const {
        maxWidth = 400,
        position = 'auto',
        dismissOnClickOutside = true,
        className = '',
    } = options;

    // Dismiss existing popups
    dismissPopups();

    // Create popup element
    const popup = document.createElement('div');
    popup.className = `node-popup ${className}`.trim();
    popup.style.cssText = `
        position: absolute;
        max-width: ${maxWidth}px;
        z-index: 10000;
        background: linear-gradient(135deg, rgba(30, 30, 50, 0.98) 0%, rgba(20, 20, 35, 0.98) 100%);
        backdrop-filter: blur(24px) saturate(180%);
        -webkit-backdrop-filter: blur(24px) saturate(180%);
        border: 1px solid rgba(255, 255, 255, 0.12);
        border-radius: 16px;
        padding: 16px;
        box-shadow: 0 16px 48px rgba(0, 0, 0, 0.5), inset 0 1px 0 rgba(255, 255, 255, 0.08);
        color: var(--text-primary);
        font-size: 13px;
        line-height: 1.5;
    `;

    if (typeof content === 'string') {
        popup.innerHTML = content;
    } else {
        popup.appendChild(content);
    }

    // Add to DOM
    document.body.appendChild(popup);
    activePopups.push(popup);

    // Position relative to anchor
    requestAnimationFrame(() => {
        positionPopup(popup, anchor, position);
    });

    // Dismiss on outside click
    if (dismissOnClickOutside) {
        const closeHandler = (e) => {
            if (!popup.contains(e.target)) {
                dismiss();
                document.removeEventListener('click', closeHandler);
            }
        };
        setTimeout(() => document.addEventListener('click', closeHandler), 100);
    }

    const dismiss = () => {
        if (popup.parentNode) {
            popup.parentNode.removeChild(popup);
        }
        activePopups = activePopups.filter(p => p !== popup);
    };

    return { dismiss, element: popup };
}

/**
 * Dismiss all open popups.
 */
export function dismissPopups() {
    activePopups.forEach(popup => {
        if (popup.parentNode) popup.parentNode.removeChild(popup);
    });
    activePopups = [];
}

/**
 * Position a popup relative to an anchor, keeping it within the viewport.
 * @param {HTMLElement} popup
 * @param {HTMLElement|{x: number, y: number}} anchor
 * @param {'auto'|'top'|'bottom'} preferredPosition
 */
function positionPopup(popup, anchor, preferredPosition) {
    const popupRect = popup.getBoundingClientRect();
    const viewportW = window.innerWidth;
    const viewportH = window.innerHeight;
    let anchorX, anchorY, anchorW, anchorH;

    if (anchor instanceof HTMLElement) {
        const rect = anchor.getBoundingClientRect();
        anchorX = rect.left;
        anchorY = rect.top;
        anchorW = rect.width;
        anchorH = rect.height;
    } else {
        // {x, y} position (e.g., from Cytoscape node)
        anchorX = anchor.x;
        anchorY = anchor.y;
        anchorW = 0;
        anchorH = 0;
    }

    // Horizontal: center on anchor, clamp to viewport
    let left = anchorX + anchorW / 2 - popupRect.width / 2;
    left = Math.max(8, Math.min(left, viewportW - popupRect.width - 8));

    // Vertical: prefer below anchor, flip to above if not enough space
    let top;
    const spaceBelow = viewportH - (anchorY + anchorH);
    const spaceAbove = anchorY;

    if (preferredPosition === 'top' || (preferredPosition === 'auto' && spaceBelow < popupRect.height && spaceAbove > spaceBelow)) {
        top = anchorY - popupRect.height - 8;
    } else {
        top = anchorY + anchorH + 8;
    }
    top = Math.max(8, Math.min(top, viewportH - popupRect.height - 8));

    popup.style.left = `${left}px`;
    popup.style.top = `${top}px`;
}
