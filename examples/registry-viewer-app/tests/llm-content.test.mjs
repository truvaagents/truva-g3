// Role-aware LLM prompt content tests.
//
// Run: node tests/llm-content.test.mjs

import {
    getLLMInteractionContent,
    getLLMPromptSections,
} from '../static/js/utils/llm-content.js';

let failed = 0;

function test(name, fn) {
    try {
        fn();
        console.log(`  ✓ ${name}`);
    } catch (error) {
        failed += 1;
        console.error(`  ✗ ${name}`);
        console.error(`    ${error.message}`);
    }
}

function assertDeepEqual(actual, expected, message) {
    const actualJSON = JSON.stringify(actual);
    const expectedJSON = JSON.stringify(expected);
    if (actualJSON !== expectedJSON) {
        throw new Error(`${message || 'expected equality'} — got ${actualJSON}, want ${expectedJSON}`);
    }
}

console.log('llm-content.test.mjs');

test('system and user prompts are returned in provider message order', () => {
    const sections = getLLMPromptSections({
        system_prompt: '<runtime_context>framework</runtime_context>',
        prompt: '<user_request>hello</user_request>',
    });

    assertDeepEqual(
        sections.map(({ type, label, text }) => ({ type, label, text })),
        [
            {
                type: 'system_prompt',
                label: 'System Prompt',
                text: '<runtime_context>framework</runtime_context>',
            },
            {
                type: 'prompt',
                label: 'User Prompt',
                text: '<user_request>hello</user_request>',
            },
        ],
    );
});

test('missing or whitespace-only system prompts are omitted', () => {
    for (const systemPrompt of [undefined, null, '', '   ']) {
        const sections = getLLMPromptSections({
            system_prompt: systemPrompt,
            prompt: 'hello',
        });
        assertDeepEqual(
            sections.map(section => section.type),
            ['prompt'],
            `unexpected sections for ${JSON.stringify(systemPrompt)}`,
        );
    }
});

test('copy content lookup keeps system, user, and response roles separate', () => {
    const interaction = {
        system_prompt: 'system text',
        prompt: 'user text',
        response: 'assistant text',
    };

    assertDeepEqual(getLLMInteractionContent(interaction, 'system_prompt'), 'system text');
    assertDeepEqual(getLLMInteractionContent(interaction, 'prompt'), 'user text');
    assertDeepEqual(getLLMInteractionContent(interaction, 'response'), 'assistant text');
    assertDeepEqual(getLLMInteractionContent(interaction, 'unknown'), '');
});

test('the user prompt section remains available when its content is empty', () => {
    const sections = getLLMPromptSections({ system_prompt: 'system only' });
    assertDeepEqual(
        sections.map(({ type, text }) => ({ type, text })),
        [
            { type: 'system_prompt', text: 'system only' },
            { type: 'prompt', text: '' },
        ],
    );
});

if (failed > 0) {
    console.error(`\n${failed} test(s) failed`);
    process.exit(1);
}

console.log('\nAll tests passed.');
