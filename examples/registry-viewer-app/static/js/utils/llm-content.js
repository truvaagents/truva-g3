/**
 * Role-aware accessors for content recorded at an LLM boundary.
 *
 * Keeping this mapping independent of the view renderers prevents the DAG,
 * LLM Debug, and shared interaction cards from silently presenting different
 * prompt roles. It is deliberately DOM-free so it can be tested with Node.
 */

const CONTENT_FIELDS = Object.freeze({
    system_prompt: 'system_prompt',
    prompt: 'prompt',
    response: 'response',
});

/**
 * Return the recorded text for a supported LLM interaction content type.
 * Unknown types are rejected with an empty value rather than falling through
 * to response content.
 *
 * @param {object|null|undefined} interaction
 * @param {'system_prompt'|'prompt'|'response'|string} type
 * @returns {string}
 */
export function getLLMInteractionContent(interaction, type) {
    const field = CONTENT_FIELDS[type];
    if (!interaction || !field) return '';
    const value = interaction[field];
    return value == null ? '' : String(value);
}

/**
 * Return prompt-role sections in provider message order. System prompt is
 * omitted when it was not recorded; user prompt remains visible even if empty
 * so a recorded call never loses its primary prompt affordance.
 *
 * @param {object|null|undefined} interaction
 * @returns {Array<{type: string, label: string, icon: string, text: string}>}
 */
export function getLLMPromptSections(interaction) {
    const sections = [];
    const systemPrompt = getLLMInteractionContent(interaction, 'system_prompt');

    if (systemPrompt.trim() !== '') {
        sections.push({
            type: 'system_prompt',
            label: 'System Prompt',
            icon: '⚙️',
            text: systemPrompt,
        });
    }

    sections.push({
        type: 'prompt',
        label: 'User Prompt',
        icon: '📥',
        text: getLLMInteractionContent(interaction, 'prompt'),
    });

    return sections;
}
