/**
 * Interaction card with category-aware rendering.
 *
 * Replaces the card rendering in renderLLMCalls (line 7171) and
 * renderLLMInteractionsView (line 2686).
 *
 * Adapts layout based on interaction.category:
 *   - "llm" (or empty): Full card with Provider, Model, Tokens, Temperature,
 *                        collapsible Prompt/Response
 *   - "embedding":       Compact row — icon, label, duration, input text
 *   - "vector_db":       Compact row — icon, label, duration, result count
 *   - "storage":         Compact row — icon, label, duration, success/failure
 *   - "logic":           Minimal row — icon, label, duration
 */

import { formatDuration, escapeHtml, formatResponseJson } from '../utils/format.js';

/**
 * Default type configuration — maps interaction type to display properties.
 * Views can extend this with additional types (e.g., user memory types).
 */
export const defaultTypeConfig = {
    'tiered_selection': { rgb: '140, 150, 255', icon: '🎯' },
    'plan_generation': { rgb: '218, 143, 255', icon: '📋' },
    'continuation_plan_generation': { rgb: '190, 120, 255', icon: '📋' },
    'continuation_plan_regeneration': { rgb: '190, 120, 255', icon: '🔄' },
    'synthesis': { rgb: '52, 211, 153', icon: '🔗' },
    'synthesis_streaming': { rgb: '52, 211, 153', icon: '🔗' },
    'micro_resolution': { rgb: '100, 210, 255', icon: '🔍' },
    'semantic_retry': { rgb: '255, 110, 180', icon: '🔄' },
    'correction': { rgb: '255, 179, 64', icon: '🔧' },
    'error_analysis': { rgb: '255, 107, 107', icon: '⚠️' },
    'agent_llm_call': { rgb: '160, 100, 240', icon: '🔧' },
    'hallucination_detection': { rgb: '255, 80, 80', icon: '⚠️' },
    'result_distillation': { rgb: '130, 90, 220', icon: '🔬' },
    'schema_mapping': { rgb: '0, 200, 170', icon: '🗺️' },
    'conversation_history_prepare': { rgb: '108, 194, 255', icon: '🛡️' },
    'conversation_history_compaction': { rgb: '108, 194, 255', icon: '🗜️' },
    'activity_compaction_incremental': { rgb: '240, 160, 48', icon: '📝' },
    'event_summarization': { rgb: '240, 160, 48', icon: '📝' },
};

/**
 * Default type labels — maps interaction type to display label.
 */
export const defaultTypeLabels = {
    'tiered_selection': 'Tier Select',
    'micro_resolution': 'Resolution',
    'plan_generation': 'Planning',
    'continuation_plan_generation': 'Phase Plan',
    'continuation_plan_regeneration': 'Plan Regen ⚠️',
    'correction': 'Plan Fix',
    'hallucination_detection': 'Hallucination',
    'conversation_history_prepare': 'History Prepare',
    'conversation_history_compaction': 'History Compact',
    'activity_compaction_incremental': 'Memory Compact',
    'event_summarization': 'Event Summary',
};

/**
 * Get type config for an interaction, with fallback to a deterministic hash color.
 * @param {string} type
 * @param {object} [typeConfig] - custom type config (merged with defaults)
 * @returns {{rgb: string, icon: string}}
 */
export function getTypeConfig(type, typeConfig) {
    const merged = typeConfig ? { ...defaultTypeConfig, ...typeConfig } : defaultTypeConfig;
    if (merged[type]) return merged[type];

    // Deterministic hash color for unknown types
    let hash = 0;
    for (let i = 0; i < type.length; i++) {
        hash = type.charCodeAt(i) + ((hash << 5) - hash);
    }
    const r = (hash >> 16) & 0xFF;
    const g = (hash >> 8) & 0xFF;
    const b = hash & 0xFF;
    return { rgb: `${r}, ${g}, ${b}`, icon: '💬' };
}

/**
 * Render an interaction card. Returns HTML string.
 *
 * @param {object} interaction - LLMInteraction from the API
 * @param {number} index - sequential index for display
 * @param {object} [options]
 * @param {boolean} [options.expanded=false] - whether prompt/response sections are open
 * @param {object} [options.typeConfig] - custom type config entries
 * @param {object} [options.typeLabels] - custom type label entries
 * @returns {string} HTML string
 */
export function renderInteractionCard(interaction, index, options = {}) {
    const { expanded = false, typeConfig, typeLabels } = options;
    const category = interaction.category || 'llm';
    const config = getTypeConfig(interaction.type, typeConfig);
    const labels = typeLabels ? { ...defaultTypeLabels, ...typeLabels } : defaultTypeLabels;
    const label = labels[interaction.type] || interaction.type;

    if (category !== 'llm') {
        return renderCompactRow(interaction, index, config, label, category);
    }
    return renderFullCard(interaction, index, config, label, expanded);
}

/**
 * Render a compact single-line row for non-LLM interactions.
 */
function renderCompactRow(interaction, index, config, label, category) {
    const duration = formatDuration(interaction.duration_ms || 0);
    const status = interaction.success !== false ? '✓' : '✗';
    const statusColor = interaction.success !== false ? 'var(--accent-green)' : 'var(--accent-red)';
    const summary = interaction.response ? escapeHtml(interaction.response) : '';

    return `
        <div class="llm-interaction-card compact" data-interaction-type="${interaction.type}"
             style="border-left: 3px solid rgba(${config.rgb}, 0.5);
                    background: rgba(${config.rgb}, 0.04);
                    padding: 8px 12px; margin-bottom: 4px; border-radius: 8px;
                    display: flex; align-items: center; gap: 10px; font-size: 12px;">
            <span style="opacity: 0.5; min-width: 20px;">#${index + 1}</span>
            <span>${config.icon}</span>
            <span style="font-weight: 500; min-width: 120px;">${label}</span>
            <span style="color: var(--text-muted); font-size: 11px; min-width: 60px;">${duration}</span>
            <span style="color: ${statusColor}; font-size: 11px;">${status}</span>
            ${summary ? `<span style="color: var(--text-muted); font-size: 11px; overflow: hidden; text-overflow: ellipsis; white-space: nowrap;">${summary}</span>` : ''}
        </div>`;
}

/**
 * Render a full LLM interaction card with metadata and collapsible sections.
 */
function renderFullCard(interaction, index, config, label, expanded) {
    const duration = formatDuration(interaction.duration_ms || 0);
    const status = interaction.success !== false;
    const nonFatalNote = status && !!interaction.error;
    const statusBadge = status
        ? `<span style="color: var(--accent-green); font-size: 11px;">${nonFatalNote ? '✓ Note' : '✓ Success'}</span>`
        : `<span style="color: var(--accent-red); font-size: 11px;">✗ Failed</span>`;

    const providerBadge = interaction.provider
        ? `<span style="font-size: 11px; color: var(--text-muted);">${interaction.provider}</span>`
        : '';
    const modelBadge = interaction.model
        ? `<span style="font-size: 11px; font-family: monospace; color: var(--text-secondary);">${interaction.model}</span>`
        : '';

    const tokenInfo = (interaction.prompt_tokens || interaction.completion_tokens)
        ? `<span style="font-size: 11px; color: var(--text-muted);">${interaction.prompt_tokens || 0} in / ${interaction.completion_tokens || 0} out</span>`
        : '';

    const display = expanded ? 'block' : 'none';

    return `
        <div class="llm-interaction-card" data-interaction-type="${interaction.type}"
             style="border: 1px solid rgba(${config.rgb}, 0.3);
                    background: linear-gradient(135deg, rgba(${config.rgb}, 0.08) 0%, rgba(255,255,255,0.03) 100%);
                    border-radius: 12px; margin-bottom: 8px; overflow: hidden;">
            <div style="padding: 12px 16px; display: flex; align-items: center; gap: 12px; flex-wrap: wrap;">
                <span style="opacity: 0.5; font-size: 11px;">#${index + 1}</span>
                <span>${config.icon}</span>
                <span style="font-weight: 600; font-size: 13px;">${label}</span>
                ${interaction.step_id ? `<span style="font-size: 10px; padding: 1px 6px; background: rgba(255,255,255,0.08); border-radius: 4px;">${interaction.step_id}</span>` : ''}
                <span style="margin-left: auto; font-size: 12px; color: var(--text-muted);">${duration}</span>
                ${statusBadge}
            </div>
            <div style="padding: 0 16px 12px; display: flex; gap: 16px; flex-wrap: wrap; font-size: 12px;">
                ${providerBadge}${modelBadge}${tokenInfo}
                ${interaction.temperature ? `<span style="font-size: 11px; color: var(--text-muted);">temp: ${interaction.temperature}</span>` : ''}
            </div>
            ${!status && interaction.error ? `<div style="padding: 8px 16px; color: var(--accent-red); font-size: 12px; background: rgba(255,107,107,0.08);">${escapeHtml(interaction.error)}</div>` : ''}
            ${nonFatalNote ? `<div style="padding: 8px 16px; color: #ffb340; font-size: 12px; background: rgba(255,179,64,0.08);">Semantic note: ${escapeHtml(interaction.error)}</div>` : ''}
            <div style="border-top: 1px solid rgba(255,255,255,0.06);">
                <button onclick="this.nextElementSibling.style.display = this.nextElementSibling.style.display === 'none' ? 'block' : 'none'"
                        style="width: 100%; padding: 8px 16px; background: none; border: none; color: var(--text-muted); cursor: pointer; text-align: left; font-size: 12px;">
                    ▶ Prompt
                </button>
                <div style="display: ${display}; padding: 0 16px 12px;">
                    <pre style="margin: 0; white-space: pre-wrap; word-break: break-word; font-size: 11px; color: var(--text-secondary); max-height: 300px; overflow-y: auto;">${escapeHtml(interaction.prompt || '')}</pre>
                </div>
            </div>
            <div style="border-top: 1px solid rgba(255,255,255,0.06);">
                <button onclick="this.nextElementSibling.style.display = this.nextElementSibling.style.display === 'none' ? 'block' : 'none'"
                        style="width: 100%; padding: 8px 16px; background: none; border: none; color: var(--text-muted); cursor: pointer; text-align: left; font-size: 12px;">
                    ▶ Response
                </button>
                <div style="display: ${display}; padding: 0 16px 12px;">
                    <div style="font-size: 11px; color: var(--text-secondary); max-height: 300px; overflow-y: auto;">${formatResponseJson(interaction.response)}</div>
                </div>
            </div>
        </div>`;
}
