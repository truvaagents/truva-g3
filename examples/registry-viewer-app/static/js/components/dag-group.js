/**
 * Collapsible container node for DAG visualization.
 *
 * Used by user memory DAG integration (USER_MEMORY_DAG_VISUALIZATION.md Phase 3).
 * Groups multiple steps into a single collapsed DAG node that expands via popup.
 *
 * Not used by any current view — exists for Phase 4 integration.
 */

import { showPopup } from './popup.js';
import { formatDuration, escapeHtml } from '../utils/format.js';

/**
 * Cytoscape style definition for user_memory_group nodes.
 * Add this to the Cytoscape stylesheet array.
 */
export const groupNodeStyle = {
    selector: 'node[nodeType="user_memory_group"]',
    style: {
        'shape': 'round-rectangle',
        'width': 280,
        'height': 50,
        'background-color': 'rgba(240, 160, 48, 0.15)',
        'border-width': 2,
        'border-style': 'dashed',
        'border-color': 'rgba(240, 160, 48, 0.6)',
        'color': '#f0a030',
        'text-valign': 'center',
        'text-halign': 'center',
        'font-size': '10px',
        'label': 'data(label)',
        'text-wrap': 'wrap',
        'text-max-width': '260px',
        'cursor': 'pointer',
    },
};

/**
 * Create a grouped container node in the Cytoscape DAG.
 *
 * @param {object} cy - Cytoscape instance
 * @param {object} config
 * @param {string} config.id - node ID (e.g., 'user_memory_before')
 * @param {string} config.label - summary label
 * @param {Array<object>} config.steps - LLMInteraction objects for this group
 * @param {string} config.stage - 'before_planning' | 'after_synthesis'
 * @param {string} [config.color='#f0a030'] - CSS color for border/text
 */
export function createGroupNode(cy, config) {
    const { id, label, steps, stage, color = '#f0a030' } = config;
    cy.add({
        data: {
            id,
            label,
            nodeType: 'user_memory_group',
            pipelineStage: stage,
            steps,
            stepCount: steps.length,
            totalDuration: steps.reduce((sum, s) => sum + (s.duration_ms || 0), 0),
        },
    });
}

/**
 * Show the group popup with step list and cross-tab navigation.
 *
 * @param {object} node - Cytoscape node
 * @param {Array<object>} steps - LLMInteraction objects
 * @param {Function} [onNavigateToLLM] - (interactionType: string) => void
 * @param {object} [typeLabels] - type-to-label mapping
 */
export function showGroupPopup(node, steps, onNavigateToLLM, typeLabels = {}) {
    const data = node.data();
    const stage = data.pipelineStage === 'before_planning' ? 'BeforePlanning' : 'AfterSynthesis';
    const llmCount = steps.filter(s =>
        (s.category || 'llm') === 'llm'
    ).length;

    let html = `<div style="max-width:420px;max-height:500px;overflow-y:auto;">`;
    html += `<div style="font-weight:600;margin-bottom:8px;color:#f0a030;">User Memory: ${stage}</div>`;
    html += `<div style="font-size:0.85em;color:var(--text-muted);margin-bottom:12px;">`;
    html += `${steps.length} steps | ${formatDuration(data.totalDuration)}`;
    if (llmCount) html += ` | ${llmCount} LLM calls`;
    html += `</div>`;

    steps.forEach((step) => {
        const isLLM = (step.category || 'llm') === 'llm';
        const label = typeLabels[step.type] || step.type;
        const opacity = isLLM ? '1.0' : '0.7';
        const badge = isLLM ? '<span style="background:#f0a030;color:#1e1e2e;padding:1px 4px;border-radius:3px;font-size:0.7em;margin-left:4px;">LLM</span>' : '';
        const clickable = isLLM && onNavigateToLLM ? `cursor:pointer;` : '';

        html += `<div class="group-step" data-type="${step.type}" style="padding:6px 8px;margin:2px 0;background:rgba(240,160,48,0.08);border-radius:6px;border-left:3px solid rgba(240,160,48,${opacity});${clickable}">`;
        html += `<div style="font-size:0.85em;color:var(--text-primary);">${label}${badge} <span style="color:var(--text-muted);float:right;">${formatDuration(step.duration_ms || 0)}</span></div>`;
        if (step.prompt) html += `<div style="font-size:0.75em;color:var(--text-muted);margin-top:2px;white-space:nowrap;overflow:hidden;text-overflow:ellipsis;">${escapeHtml(step.prompt.substring(0, 80))}</div>`;
        if (step.response) html += `<div style="font-size:0.75em;color:var(--text-secondary);margin-top:1px;white-space:nowrap;overflow:hidden;text-overflow:ellipsis;">${escapeHtml(step.response)}</div>`;
        if (!step.success) html += `<div style="font-size:0.75em;color:var(--accent-red);margin-top:1px;">Error: ${escapeHtml(step.error || '')}</div>`;
        html += `</div>`;
    });

    html += `</div>`;

    const pos = node.renderedPosition();
    const { element } = showPopup({ x: pos.x, y: pos.y }, html);

    // Cross-tab navigation: click LLM steps to navigate to LLM Calls tab
    if (onNavigateToLLM) {
        element.querySelectorAll('.group-step').forEach(el => {
            const step = steps.find(s => s.type === el.dataset.type);
            if (step && (step.category || 'llm') === 'llm') {
                el.addEventListener('click', () => onNavigateToLLM(step.type));
            }
        });
    }
}
