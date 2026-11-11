// Standalone debug-view trace attribution uses recorded facts, not inference.
// Run: node tests/llm-debug-view.test.mjs
import assert from 'node:assert/strict';
import { readFile } from 'node:fs/promises';

globalThis.document = {
    createElement() {
        return {
            set textContent(value) {
                this.innerHTML = String(value).replaceAll('&', '&amp;').replaceAll('<', '&lt;').replaceAll('>', '&gt;');
            },
            innerHTML: '',
        };
    },
};
// A renderer must not issue any execution lookup or mutate historical records.
globalThis.fetch = () => { throw new Error('Rendering must not fetch'); };

const moduleURL = new URL('../static/js/views/llm-debug.js', import.meta.url);
let source = await readFile(moduleURL, 'utf8');
source = source.replace(/from '(\.\.[^']+)'/g, (_, path) => `from '${new URL(path, moduleURL).href}'`);
source += '\nexport { renderLLMInteractionsView, copyLLMInteractionContent, toggleInteraction, expandAllInteractions, collapseAllInteractions, renderSourceCell };\nexport function testSetRecords(value) { records = value; }\nexport function testSetSelected(value) { selected = value; expandedInteractions.clear(); }';
const view = await import(`data:text/javascript;base64,${Buffer.from(source).toString('base64')}`);

assert.match(view.renderSourceCell({ summary_pending: true }), />Unknown</);
assert.match(view.renderSourceCell({ summary_pending: false, interaction_count: 3 }), />Unknown</, 'summary completion cannot invent source attribution');
assert.match(view.renderSourceCell({ summary_pending: false, source_components: ['recorded-agent'] }), />recorded-agent</);
assert.match(view.renderSourceCell({ originating_agent: 'origin-agent', source_components: ['participant'] }), />origin-agent</);

const interaction = values => ({
    type: 'plan_generation', timestamp: '2026-11-01T00:00:00Z', duration_ms: 10,
    prompt: 'Original prompt', response: 'Original response', success: true,
    ...values,
});
const record = values => ({
    request_id: 'run-1', original_request_id: 'run-1', trace_id: 'aggregate-trace',
    created_at: '2026-11-01T00:00:00Z', interactions: [interaction({})],
    ...values,
});
const traces = html => [...html.matchAll(/Interaction trace: <span class="token-stat-value mono">(.*?)<\/span>/g)].map(match => match[1]);

const legacy = record({});
const before = JSON.stringify(legacy);
let html = view.renderLLMInteractionsView(legacy);
assert.match(html, />Debug record trace</);
assert.match(html, /Record-level reference; individual interactions may use other traces\./);
assert.doesNotMatch(html, />Trace ID</, 'aggregate is not labelled as the execution or call trace');
assert.deepEqual(traces(html), ['Not recorded'], 'legacy call does not inherit the aggregate trace');
assert.equal(JSON.stringify(legacy), before, 'rendering preserves historical record facts');

html = view.renderLLMInteractionsView(record({ interactions: [
    interaction({ trace_id: 'execution-call-trace' }),
    interaction({ type: 'agent_llm_call', trace_id: 'background-call-trace', success: false, error: 'Observed failure' }),
    interaction({ type: 'event_summarization', trace_id: '' }),
    interaction({ type: 'user_memory_persistence_policy', category: 'logic', trace_id: 'logic-trace' }),
] }));
assert.deepEqual(traces(html), ['execution-call-trace', 'background-call-trace', 'Not recorded', 'logic-trace'], 'each card retains its own trace, including errors and logic observations');
assert.doesNotMatch(html, /Interaction trace: <span[^>]*>aggregate-trace</, 'aggregate is never used as a call fallback');

const malicious = '<script>alert("trace")</script>&';
const requestID = 'run:<img src=x onerror="alert(1)">&?+#';
const hostileRecord = record({ request_id: requestID, original_request_id: requestID, trace_id: malicious, interactions: [interaction({ trace_id: malicious })] });
view.testSetRecords([hostileRecord, { ...hostileRecord, request_id: 'linked-record' }]);
html = view.renderLLMInteractionsView(hostileRecord);
assert.doesNotMatch(html, /<script>|<img /, 'trace and request identifiers are escaped');
assert.match(html, /&lt;script&gt;alert\("trace"\)&lt;\/script&gt;&amp;/);
assert.match(html, /data-lineage-request-id="run:&lt;img src=x onerror=&quot;alert\(1\)&quot;&gt;&amp;\?\+#"/, 'linked request IDs cannot break out of their attribute');
const link = html.match(/<a class="dag-open-timeline-btn" href="([^"]+)">Open execution DAG<\/a>/);
assert.ok(link, 'record includes a plain navigation link to the execution DAG');
assert.match(link[1], /view=dag&amp;request_id=/, 'HTML attribute escapes the query separator');
const target = new URL(link[1].replaceAll('&amp;', '&'), 'http://registry.localhost/');
assert.equal(target.origin, 'http://registry.localhost');
assert.equal(target.pathname, '/');
assert.equal(target.searchParams.get('view'), 'dag');
assert.equal(target.searchParams.get('request_id'), requestID, 'request ID is encoded once and recovered exactly');
assert.equal(target.hash, '', 'request content cannot inject a URL fragment');

html = view.renderLLMInteractionsView(record({ trace_id: '', interactions: [] }));
assert.doesNotMatch(html, />Debug record trace</, 'an absent aggregate trace is not invented');
assert.match(html, /Open execution DAG/, 'execution navigation is available even without interaction details');

// Use the indexes emitted by the real renderer to drive the real Copy helper.
// Hook entries before/between visible calls must not change their source identity.
const repeatedCall = interaction({
    system_prompt: 'System\nPreserve *pointers* and <tags> exactly.',
    prompt: 'User\n```go\nvar values = []int{1, 2}\n```\n日本語 🌍',
    response: '  Response\nKeep leading/trailing whitespace.\t\n',
});
assert.ok(repeatedCall.prompt.includes('\n```go\n'), 'fixture contains real newlines');
assert.ok(repeatedCall.response.endsWith('\t\n'), 'fixture contains a real trailing tab and newline');
const filteredRecord = record({ interactions: [
    interaction({ type: 'user_memory_recall_identity', system_prompt: 'HOOK_SYSTEM', prompt: 'HOOK_PROMPT', response: 'HOOK_RESPONSE' }),
    repeatedCall,
    interaction({ type: 'user_memory_summary_remember', prompt: 'SECOND_HOOK_PROMPT' }),
    interaction({ type: 'synthesis', system_prompt: 'Synthesis system', prompt: 'Synthesis user', response: 'Synthesis response' }),
    repeatedCall,
] });
const cards = new Map();
document.getElementById = id => cards.get(id) || null;
document.querySelectorAll = selector => selector === '.interaction-card' ? [...cards.values()] : [];
const clipboard = [];
const originalNavigator = Object.getOwnPropertyDescriptor(globalThis, 'navigator');
Object.defineProperty(globalThis, 'navigator', {
    configurable: true,
    value: { clipboard: { writeText: async text => { clipboard.push(text); } } },
});
const originalSetTimeout = globalThis.setTimeout;
globalThis.setTimeout = () => 0; // Feedback timers are unrelated to clipboard selection.
try {
    for (const candidate of [filteredRecord, record({ interactions: [repeatedCall] })]) {
        view.testSetSelected(candidate);
        const snapshot = JSON.stringify(candidate);
        html = view.renderLLMInteractionsView(candidate);
        const expectedIndexes = candidate === filteredRecord ? [1, 3, 4] : [0];
        const displayedCalls = expectedIndexes.map(index => candidate.interactions[index]);
        const renderedIndexes = [...html.matchAll(/class="interaction-card[^\"]*" data-index="(\d+)" id="interaction-\d+"/g)].map(match => Number(match[1]));
        const buttons = [...html.matchAll(/data-copy-index="(\d+)" data-copy-type="([^\"]+)"/g)];
        assert.equal(buttons.length, expectedIndexes.length * 3, 'every visible call has system, user and response Copy controls');
        for (const [buttonNumber, [, indexText, type]] of buttons.entries()) {
            const index = Number(indexText);
            const button = { textContent: 'Copy', classList: { add() {}, remove() {} } };
            const beforeCopy = clipboard.length;
            view.copyLLMInteractionContent(index, type, { target: button });
            await Promise.resolve();
            assert.equal(clipboard.length, beforeCopy + 1, 'Copy writes exactly one value');
            assert.equal(clipboard.at(-1), displayedCalls[Math.floor(buttonNumber / 3)][type], `${type} is copied byte-for-byte from the displayed interaction`);
        }
        assert.deepEqual(renderedIndexes, expectedIndexes, 'visible cards retain original record indexes, including repeated object references');

        cards.clear();
        for (const index of renderedIndexes) {
            const classes = new Set();
            cards.set(`interaction-${index}`, {
                dataset: { index: String(index) },
                classList: {
                    add: value => classes.add(value), remove: value => classes.delete(value),
                    contains: value => classes.has(value),
                    toggle(value) { if (classes.has(value)) classes.delete(value); else classes.add(value); },
                },
            });
        }
        view.toggleInteraction(expectedIndexes[0]);
        html = view.renderLLMInteractionsView(candidate);
        assert.match(html, new RegExp(`class="interaction-card[^\"]* expanded" data-index="${expectedIndexes[0]}"`), 'individual expansion survives rerender with source indexes');
        view.expandAllInteractions();
        html = view.renderLLMInteractionsView(candidate);
        assert.equal((html.match(/class="interaction-card[^\"]* expanded"/g) || []).length, expectedIndexes.length, 'Expand All covers every visible source index');
        view.collapseAllInteractions();
        assert.doesNotMatch(view.renderLLMInteractionsView(candidate), /class="interaction-card[^\"]* expanded"/, 'Collapse All clears source-index expansion state');
        assert.equal(JSON.stringify(candidate), snapshot, 'rendering, expansion and copying leave the raw record unchanged');
    }
} finally {
    globalThis.setTimeout = originalSetTimeout;
    if (originalNavigator) Object.defineProperty(globalThis, 'navigator', originalNavigator);
    else delete globalThis.navigator;
}

console.log('All standalone LLM-debug trace attribution and filtered-copy tests passed.');
