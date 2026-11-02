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
source += '\nexport { renderLLMInteractionsView };\nexport function testSetRecords(value) { records = value; }';
const view = await import(`data:text/javascript;base64,${Buffer.from(source).toString('base64')}`);

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

console.log('All standalone LLM-debug trace attribution tests passed.');
