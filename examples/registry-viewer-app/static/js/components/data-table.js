/**
 * Reusable sortable table with row selection.
 *
 * Replaces: renderServiceTable, renderLLMTable, renderHITLTable,
 *           renderExecutionList, renderMemoryEvents, renderMemoryInvestigations,
 *           renderMemoryActivities (7 separate table renderers).
 *
 * Each view defines its columns and row rendering as data declarations.
 */

/**
 * Render a table into a tbody element.
 *
 * @param {HTMLElement} container - tbody or list container element
 * @param {object} config
 * @param {Array<{key: string, label: string, render?: (row: object) => string}>} config.columns
 *   Column definitions. If render is omitted, displays row[key] as text.
 * @param {Array<object>} config.rows - data rows
 * @param {string|null} config.selectedId - ID of the currently selected row
 * @param {Function} config.onSelect - (id: string) => void
 * @param {Function} config.rowId - (row: object) => string — extracts unique ID
 * @param {string} [config.emptyIcon] - emoji for empty state (e.g., '📋')
 * @param {string} [config.emptyText] - text for empty state
 * @param {number} [config.emptyColspan] - colspan for empty state row (default: columns.length)
 * @param {Function} [config.rowClass] - (row: object) => string — optional extra CSS class per row
 * @param {Function} [config.rowAttrs] - (row: object) => string — optional extra attributes per row
 */
export function renderTable(container, config) {
    const {
        columns, rows, selectedId, onSelect, rowId,
        emptyIcon = '', emptyText = 'No data',
        emptyColspan, rowClass, rowAttrs,
    } = config;

    if (!rows || rows.length === 0) {
        const colspan = emptyColspan || columns.length;
        container.innerHTML = `
            <tr>
                <td colspan="${colspan}" style="text-align: center; color: var(--text-muted); padding: 48px;">
                    ${emptyIcon ? `<div style="font-size: 40px; margin-bottom: 12px;">${emptyIcon}</div>` : ''}
                    <div>${emptyText}</div>
                </td>
            </tr>`;
        return;
    }

    container.innerHTML = rows.map(row => {
        const id = rowId(row);
        const selected = selectedId === id ? 'selected' : '';
        const extraClass = rowClass ? rowClass(row) : '';
        const extraAttrs = rowAttrs ? rowAttrs(row) : '';
        const cells = columns.map(col =>
            `<td>${col.render ? col.render(row) : (row[col.key] ?? '')}</td>`
        ).join('');
        return `<tr data-id="${id}" class="${selected} ${extraClass}" ${extraAttrs}>${cells}</tr>`;
    }).join('');

    // Delegated click handler for row selection.
    // Remove previous handler before attaching (prevents leak on re-render).
    if (onSelect) {
        if (container._tableClickHandler) {
            container.removeEventListener('click', container._tableClickHandler);
        }
        container._tableClickHandler = (e) => {
            const tr = e.target.closest('tr[data-id]');
            if (tr) onSelect(tr.dataset.id);
        };
        container.addEventListener('click', container._tableClickHandler);
    }
}

/**
 * Render sortable table headers with click-to-sort and direction indicators.
 *
 * @param {HTMLElement} theadRow - the <tr> inside <thead>
 * @param {Array<{key: string, label: string, sortable?: boolean, width?: string}>} columns
 * @param {string} sortColumn - currently sorted column key
 * @param {string} sortDirection - 'asc' or 'desc'
 * @param {Function} onSort - (column: string) => void
 */
export function renderTableHeaders(theadRow, columns, sortColumn, sortDirection, onSort) {
    theadRow.innerHTML = columns.map(col => {
        if (!col.sortable) {
            return `<th${col.width ? ` style="width:${col.width}"` : ''}>${col.label}</th>`;
        }
        const active = sortColumn === col.key;
        const arrow = active ? (sortDirection === 'asc' ? ' ↑' : ' ↓') : ' ⇅';
        return `<th data-sort="${col.key}" style="cursor:pointer;${col.width ? `width:${col.width};` : ''}" class="${active ? sortDirection : ''}">${col.label}${arrow}</th>`;
    }).join('');

    // Delegated click handler for sorting.
    // Remove previous handler before attaching (prevents leak on re-render).
    if (onSort) {
        if (theadRow._sortClickHandler) {
            theadRow.removeEventListener('click', theadRow._sortClickHandler);
        }
        theadRow._sortClickHandler = (e) => {
            const th = e.target.closest('th[data-sort]');
            if (th) onSort(th.dataset.sort);
        };
        theadRow.addEventListener('click', theadRow._sortClickHandler);
    }
}
