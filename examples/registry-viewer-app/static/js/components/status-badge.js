/**
 * Status and type badge rendering.
 *
 * Consistent badge styling across all views — service type badges,
 * execution status badges, priority badges, provider badges.
 */

/**
 * Render an execution status badge.
 * @param {object} execution - { success, interrupted }
 * @returns {string} HTML string
 */
export function renderStatusBadge(execution) {
    if (execution.interrupted) {
        return `<span class="status-badge interrupted">⏸ Interrupted</span>`;
    }
    if (execution.success) {
        return `<span class="status-badge success">✓ Success</span>`;
    }
    return `<span class="status-badge error">✗ Failed</span>`;
}

/**
 * Render a service type badge.
 * @param {string} type - 'agent' or 'tool'
 * @returns {string} HTML string
 */
export function renderTypeBadge(type) {
    return `<span class="type-badge ${type}">${type}</span>`;
}

/**
 * Render a provider badge with consistent styling.
 * @param {string} provider - e.g., 'openai', 'anthropic', 'groq'
 * @returns {string} HTML string
 */
export function renderProviderBadge(provider) {
    if (!provider) return '';
    const colors = {
        openai: 'rgba(16, 163, 127, 0.3)',
        anthropic: 'rgba(204, 153, 102, 0.3)',
        groq: 'rgba(255, 107, 107, 0.3)',
        gemini: 'rgba(66, 133, 244, 0.3)',
        deepseek: 'rgba(100, 210, 255, 0.3)',
    };
    const bg = colors[provider.toLowerCase()] || 'rgba(255, 255, 255, 0.08)';
    return `<span style="padding: 2px 8px; border-radius: 4px; font-size: 11px; background: ${bg};">${provider}</span>`;
}

/**
 * Render a HITL priority badge.
 * @param {string} priority - 'critical', 'high', 'medium', 'low'
 * @returns {string} HTML string
 */
export function renderPriorityBadge(priority) {
    return `<span class="priority-badge ${priority || 'medium'}">${priority || 'medium'}</span>`;
}
