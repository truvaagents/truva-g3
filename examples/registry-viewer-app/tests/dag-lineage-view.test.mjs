// Actual DAG view integration with a minimal DOM and recording transport.
// Run: node tests/dag-lineage-view.test.mjs
import assert from 'node:assert/strict';
import { readFile } from 'node:fs/promises';

const elements = new Map();
const parentTab = { style: {} };
function element() {
    return {
        _html: '', _text: '', value: '', style: {}, dataset: {}, children: [],
        get innerHTML() { return this._html; }, set innerHTML(value) { this._html = value; },
        get textContent() { return this._text; }, set textContent(value) {
            this._text = String(value);
            this._html = this._text.replaceAll('&', '&amp;').replaceAll('<', '&lt;').replaceAll('>', '&gt;');
        },
        classList: { add() {}, remove() {}, toggle() {}, contains() { return false; } },
        querySelector() { return null; }, querySelectorAll() { return []; },
        appendChild(child) { this.children.push(child); },
        prepend(child) { this.children.unshift(child); if (child.id) elements.set(child.id, child); },
        addEventListener() {}, removeEventListener() {}, focus() {}, scrollIntoView() {}, remove() {},
    };
}
globalThis.document = {
    getElementById(id) { if (!elements.has(id)) elements.set(id, element()); return elements.get(id); },
    createElement: element,
    querySelector(selector) { return selector === '[data-tab="dag-parent"]' ? parentTab : null; },
    querySelectorAll() { return []; },
};
globalThis.sessionStorage = { getItem() { return 'grouped'; } };
globalThis.requestAnimationFrame = callback => callback();

// Expose private view entry points only in this in-memory test module. Imports
// still point to their real utility implementations; production exports stay unchanged.
const moduleURL = new URL('../static/js/views/dag.js', import.meta.url);
let source = await readFile(moduleURL, 'utf8');
source = source.replace(/from '(\.\.[^']+)'/g, (_, path) => `from '${new URL(path, moduleURL).href}'`);
source += `\nexport { selectExecution, renderServerExecutionGroups, renderTimelineTurn, renderLegacyExecutionList, discoverExecutionLineage, renderExecutionLineageNavigation, renderAgentParentInfo, renderParentExecution, loadParentExecution, updateDetailTabs, renderExecutionDetail, renderSingleLLMCard };
export function testState() { return selected; }
export function testInstall(records, groups = []) { executions = records; groupedExecutionGroups = groups; activeTab = 'dag-raw'; for (const group of groups) expandedGroupKeys.add(group.group_key); }
export function testSetSelection(value, tab = 'dag-raw') { selected = value; activeTab = tab; }
export function testActiveTab() { return activeTab; }
export function testSetActiveTab(value) { activeTab = value; }
`;
const view = await import(`data:text/javascript;base64,${Buffer.from(source).toString('base64')}`);
const traceCard = values => view.renderSingleLLMCard({ type: 'plan_generation', prompt: 'Exact prompt', response: 'Exact response', success: true, ...values }, 0);
const firstTraceCard = traceCard({ trace_id: 'first-call-trace' });
assert.match(firstTraceCard, /href="\/api\/traces\/first-call-trace"[^>]*target="_blank"[^>]*rel="noopener noreferrer"/);
assert.match(firstTraceCard, />TraceID <span aria-hidden="true">↗<\/span>/);
assert.ok(firstTraceCard.indexOf('dag-call-trace-link') < firstTraceCard.indexOf('class="dag-step-body"'), 'trace link belongs to the card header');
assert.doesNotMatch(firstTraceCard, /Interaction trace:/, 'full trace IDs no longer consume a body row');
assert.match(traceCard({ trace_id: 'linked-background-trace' }), /href="\/api\/traces\/linked-background-trace"/, 'background call uses its own trace');
assert.doesNotMatch(traceCard({}), /dag-call-trace-link|Interaction trace:/, 'legacy calls do not inherit an execution or aggregate trace');
assert.match(traceCard({ trace_id: '<trace>&"' }), /href="\/api\/traces\/%3Ctrace%3E%26%22"/);
assert.match(traceCard({ trace_id: '<trace>&"' }), /&lt;trace&gt;&amp;&quot;/, 'per-call trace attribute text is escaped');
const tick = () => new Promise(resolve => setImmediate(resolve));
const record = (requestID, parent = '', step = '') => ({
    request_id: requestID, original_request_id: 'root', parent_request_id: parent, parent_step_id: step,
    original_request: `<${requestID}>`, created_at: '2026-11-01T00:00:00Z', record_revision: 1,
    lifecycle: { state: 'completed' }, plan: { steps: [] }, result: { steps: [] },
    llm_interactions: [],
});
const records = [record('root'), record('translation', 'root', 'step-1'), record('review', 'translation', 'step-17')];
const group = { group_key: 'conversation:test', conversation_id: 'test', turns: [{ execution: records[0], related_executions: records.slice(1) }] };
view.testInstall(records, [group]);
view.renderServerExecutionGroups([group]);
let html = elements.get('dagTableBody').innerHTML;
assert.match(html, /data-lineage-depth="2"/);
assert.equal((html.match(/data-request-id="review"/g) || []).length, 1);
assert.match(html, /&lt;review&gt;/, 'payload text is escaped');
const timeline = view.renderTimelineTurn(group.turns[0], 0, 1, 'review');
assert.match(timeline, /data-lineage-depth="2"/);
assert.match(timeline, /Last snapshot:/);
const resumeRecord = { ...records[1], metadata: { resume_checkpoint_id: 'checkpoint' } };
assert.match(view.renderTimelineTurn({ execution: records[0], related_executions: [resumeRecord] }, 0, 1, 'translation'), /HITL Resume/, 'timeline retains explicit resume provenance alongside depth');
view.renderLegacyExecutionList();
assert.match(elements.get('dagTableBody').innerHTML, /data-lineage-depth="2"/);

// All list modes use the same depth badges, with four distinct delegation
// colors. A missing prefix of the chain must stay unknown at every descendant.
const deepRecords = [record('root'), ...Array.from({ length: 4 }, (_, index) => ({
    ...record(`depth-${index + 1}`, index ? `depth-${index}` : 'root', `step-${index + 1}`),
    agent_name: `Agent ${index + 1}`,
}))];
const deepGroup = { ...group, turns: [{ execution: deepRecords[0], related_executions: deepRecords.slice(1) }] };
view.testInstall(deepRecords, [deepGroup]);
const assertDepthColors = (markup, renderer) => {
    for (let depth = 1; depth <= 4; depth++) {
        assert.match(markup, new RegExp(`dag-lineage-level-${depth}`), `${renderer}: depth ${depth} has its own color`);
        assert.match(markup, new RegExp(`data-lineage-depth="${depth}"`), `${renderer}: depth ${depth} remains visible`);
    }
};
view.renderServerExecutionGroups([deepGroup]);
assertDepthColors(elements.get('dagTableBody').innerHTML, 'grouped');
assertDepthColors(view.renderTimelineTurn(deepGroup.turns[0], 0, 1, 'depth-4'), 'timeline');
view.renderLegacyExecutionList();
assertDepthColors(elements.get('dagTableBody').innerHTML, 'legacy');
const incompleteRecords = [record('missing-prefix', 'expired-parent', 'step-1'), record('descendant', 'missing-prefix', 'step-2')];
const incompleteGroup = { ...group, turns: [], orphans: incompleteRecords };
view.testInstall(incompleteRecords, [incompleteGroup]);
view.renderServerExecutionGroups([incompleteGroup]);
assert.equal((elements.get('dagTableBody').innerHTML.match(/dag-lineage-unknown/g) || []).length, 2);
assert.doesNotMatch(elements.get('dagTableBody').innerHTML, /dag-lineage-level-[1-4]/, 'relative indentation does not claim a known absolute depth');
view.testInstall(records, [group]);
view.testInstall([{ ...records[2], created_at: '0001-01-01T00:00:00Z' }]);
view.renderLegacyExecutionList();
assert.match(elements.get('dagTableBody').innerHTML, /class="time-ago">Unknown</);
view.testInstall(records, [group]);

// Grouping and filtering membership do not grow when an explicit parent is
// elsewhere. The missing parent stays a navigation link in the current group.
const isolated = { ...group, turns: [{ execution: records[2], related_executions: [] }] };
view.renderServerExecutionGroups([isolated]);
html = elements.get('dagTableBody').innerHTML;
assert.equal((html.match(/data-request-id=/g) || []).length, 1);
assert.match(html, /data-lineage-execution="translation"/);

let lineageCalls = 0, unifiedCalls = 0, finishLineage;
globalThis.fetch = async endpoint => {
    if (endpoint.endsWith('/lineage')) {
        lineageCalls++;
        return new Promise(resolve => { finishLineage = () => resolve({ ok: true, json: async () => ({
            request_id: 'root', executions: records, partial: false, index_incomplete: false,
        }) }); });
    }
    let data;
    if (endpoint.endsWith('/unified')) {
        unifiedCalls++;
        const requestID = decodeURIComponent(endpoint.split('/')[3]);
        data = { ...records.find(item => item.request_id === requestID), llm_interactions: [{ type: 'synthesis', response: `late-${unifiedCalls}` }] };
    } else if (endpoint === '/api/services') data = { services: [] };
    else data = { groups: [group], timestamp: '2026-11-01T00:00:00Z' };
    return { ok: true, status: 200, headers: { get() { return null; } }, json: async () => data };
};
await view.selectExecution('root');
assert.equal(view.testState().request_id, 'root', 'slow lineage does not block selected details');
assert.match(elements.get('dagDetailContent').innerHTML, /late-1/);
assert.equal(lineageCalls, 1);
finishLineage();
await tick();
await view.selectExecution('translation');
assert.equal(lineageCalls, 1, 'known family navigation reuses one summary snapshot');
assert.match(view.renderAgentParentInfo(), /Invoked by/);
assert.match(view.renderAgentParentInfo(), /data-lineage-parent-step="step-1"/);
const relatedPanel = view.renderExecutionLineageNavigation();
assert.match(relatedPanel, /class="dag-parent-family dag-family-panel"/);
assert.match(relatedPanel, /aria-label="Known related executions"/);
assert.match(relatedPanel, /aria-current="page"/);
assert.match(relatedPanel, /aria-label="Refresh related executions"/);
assert.doesNotMatch(relatedPanel, /Immediate parent:|Invoked through:|<pre>|Lookup coverage and parent-read diagnostics/, 'family list omits repeated parent details and raw lookup dumps');
assert.equal(elements.get('dagDetailTitle').textContent, '<translation>', 'detail title remains the selected child request');
assert.equal(elements.has('dagExecutionLineage'), false, 'lineage has no block above the detail tabs');
view.refresh();
await tick();
await tick();
assert.equal(lineageCalls, 1, 'periodic refresh causes zero family scans');
assert.equal(unifiedCalls, 3);
assert.equal(view.testState().llm_interactions[0].response, 'late-3', 'late debug is refreshed at unchanged record revision');
view.discoverExecutionLineage({ force: true });
await tick();
assert.equal(lineageCalls, 2, 'explicit related-executions refresh initiates one lookup');
view.destroy();
finishLineage();
await tick();
assert.equal(view.testState(), null, 'late discovery cannot repopulate destroyed view');

let finishOldUnified;
globalThis.fetch = async endpoint => {
    const requestID = decodeURIComponent(endpoint.split('/')[3]);
    if (endpoint.endsWith('/lineage')) {
        return { ok: true, json: async () => ({ request_id: requestID, plan: {}, result: {} }) }; // older backend
    }
    if (requestID === 'old-selection') {
        return new Promise(resolve => { finishOldUnified = () => resolve({
            ok: true, status: 200, headers: { get() { return null; } }, json: async () => record(requestID),
        }); });
    }
    return { ok: true, status: 200, headers: { get() { return null; } }, json: async () => record(requestID) };
};
view.testInstall(records);
const oldSelection = view.selectExecution('old-selection');
await tick();
await view.selectExecution('new-selection');
finishOldUnified();
await oldSelection;
await tick();
assert.equal(view.testState().request_id, 'new-selection', 'stale unified response cannot replace a new selection');
assert.match(view.renderExecutionLineageNavigation(), /lookup unavailable/, 'old-backend raw fallback leaves selected details usable');
view.destroy();

// A failed details request must not graft its independently successful family
// response onto the previously displayed execution.
const requestedPaths = [];
globalThis.location = new URL('http://registry.localhost/?view=dag&request_id=stable-A&keep=1#evidence');
globalThis.history = { state: null, replaceState(_state, _title, url) { globalThis.location = new URL(url); } };
globalThis.fetch = async endpoint => {
    requestedPaths.push(endpoint);
    const requestID = decodeURIComponent(endpoint.split('/')[3]);
    if (endpoint.endsWith('/lineage')) return { ok: true, json: async () => ({
        request_id: requestID, executions: [{ ...record(requestID), original_request_id: requestID }], partial: false, index_incomplete: false,
    }) };
    if (requestID === 'unrelated-B') return { ok: false, status: 404, text: async () => 'execution unavailable' };
    return { ok: true, status: 200, headers: { get() { return null; } }, json: async () => ({ ...record(requestID), original_request_id: requestID }) };
};
view.testInstall([]);
await view.openExecution('stable-A');
await tick();
const previousConsoleError = console.error;
console.error = () => {};
try { await view.openExecution('unrelated-B'); } finally { console.error = previousConsoleError; }
await tick();
assert.equal(view.testState().request_id, 'stable-A');
assert.doesNotMatch(view.renderExecutionLineageNavigation(), /unrelated-B/);
assert.equal(location.searchParams.get('request_id'), 'stable-A', 'failed selection leaves the displayed execution URL truthful');
await view.selectExecution('stable-A', { background: true });
assert.doesNotMatch(view.renderExecutionLineageNavigation(), /unrelated-B/, 'later detail refresh also excludes the other family');
const escapedID = 'run:<x>&"?+';
await view.openExecution(escapedID);
await tick();
assert.equal(view.testState().request_id, escapedID);
assert.equal(location.searchParams.get('request_id'), escapedID);
assert.equal(location.searchParams.get('keep'), '1');
assert.equal(location.hash, '#evidence');
assert.ok(requestedPaths.includes(`/api/executions/${encodeURIComponent(escapedID)}/unified`));
const beforeInvalidOpen = requestedPaths.length;
await view.openExecution(undefined);
await view.openExecution('contains space');
assert.equal(requestedPaths.length, beforeInvalidOpen, 'invalid public open requests do not fetch an endpoint');
view.destroy();

// The parent tab is conditional. Reset its active state without attempting a
// Cytoscape render: that belongs to the ordinary graph-rendering tests.
view.testSetSelection(records[1], 'dag-parent');
view.updateDetailTabs();
assert.equal(parentTab.style.display, 'block');
assert.equal(view.testActiveTab(), 'dag-parent');
view.testSetSelection(records[0], 'dag-parent');
view.updateDetailTabs();
assert.equal(parentTab.style.display, 'none');
assert.equal(view.testActiveTab(), 'dag-viz', 'a root cannot retain a hidden active parent tab');
view.testSetSelection({ ...records[1], parent_request_id: 'parent:<x>&"', parent_step_id: 'step-<x>&"' });
const parentInfo = view.renderAgentParentInfo();
assert.match(parentInfo, /parent:&lt;x&gt;&amp;&quot;/);
assert.match(parentInfo, /step-&lt;x&gt;&amp;&quot;/);
assert.doesNotMatch(parentInfo, /parent:<x>/);
view.testSetSelection({ ...records[1], original_request: '', plan: { original_request: 'Own plan request', steps: [] } });
view.renderExecutionDetail();
assert.equal(elements.get('dagDetailTitle').textContent, 'Own plan request');
view.testSetSelection({ ...records[1], original_request: '', plan: { steps: [] } });
view.renderExecutionDetail();
assert.equal(elements.get('dagDetailTitle').textContent, 'Request text unavailable', 'missing child text is not replaced with the parent task');
view.destroy();

// Exact parent records are loaded only when the parent tab opens. The record
// remains separate from selected unified/debug state and uses its own step ID.
let parentRecordCalls = 0;
let parentResponseMode = 'available';
const exactParent = {
    ...record('parent-record'), original_request: '<parent task>', agent_name: 'Parent <agent>',
    lifecycle: { state: 'failed' },
    plan: { steps: [{ step_id: 'unrelated-step', instruction: 'not the caller' }] },
    phase_plans: [{ steps: [{ step_id: 'invoking-step', agent_name: 'translator', capability: '', metadata: { capability: 'translate<content>' }, instruction: '<translate exactly>' }] }],
    result: { success: false, steps: [{ step_id: 'invoking-step', success: false }] },
};
const parentChild = { ...record('parent-child', 'parent-record', 'invoking-step'), original_request: '<child task>' };
globalThis.fetch = async endpoint => {
    const id = decodeURIComponent(endpoint.split('/')[3]);
    if (endpoint.endsWith('/lineage')) return { ok: true, json: async () => ({ request_id: id, executions: [parentChild], partial: false, index_incomplete: false }) };
    if (endpoint.endsWith('/unified')) return { ok: true, status: 200, headers: { get() { return null; } }, json: async () => parentChild };
    parentRecordCalls++;
    assert.equal(endpoint, '/api/executions/parent-record', 'parent tab reads the exact record, without unified/debug expansion');
    if (parentResponseMode === 'missing') return { ok: false, status: 404 };
    if (parentResponseMode === 'error') return { ok: false, status: 503 };
    const data = parentResponseMode === 'missing-step' ? { ...exactParent, plan: { steps: [] }, phase_plans: [], result: { steps: [] } }
        : parentResponseMode === 'mismatched' ? { ...exactParent, request_id: 'other-parent', original_request: 'incorrect parent body' }
        : exactParent;
    return { ok: true, status: 200, json: async () => data };
};
view.testInstall([parentChild]);
await view.selectExecution(parentChild.request_id);
await tick();
assert.equal(parentRecordCalls, 0, 'selecting a child does not fetch the parent record');
assert.equal(elements.get('dagDetailTitle').textContent, '<child task>');
view.testSetActiveTab('dag-parent');
view.renderParentExecution(elements.get('dagDetailContent'));
assert.match(elements.get('dagDetailContent').innerHTML, /Loading exact parent record/);
await tick();
html = elements.get('dagDetailContent').innerHTML;
assert.equal(parentRecordCalls, 1);
assert.match(html, /Parent &lt;agent&gt;/);
assert.match(html, /&lt;parent task&gt;/);
assert.match(html, /&lt;translate exactly&gt;/);
assert.match(html, /translate&lt;content&gt;/, 'planned capability is also read from its stored metadata location');
assert.match(html, /Invoking step · invoking-step/);
assert.match(html, />Failed</);
assert.doesNotMatch(html, /not the caller/);
assert.equal(view.testState(), parentChild, 'parent details do not replace child unified state');
assert.equal(elements.get('dagDetailTitle').textContent, '<child task>', 'parent tab retains the child title');
view.renderParentExecution(elements.get('dagDetailContent'));
await tick();
assert.equal(parentRecordCalls, 1, 'rendering the same parent tab reuses its one exact record');
view.testSetActiveTab('dag-raw');
await view.selectExecution(parentChild.request_id, { background: true });
assert.equal(parentRecordCalls, 1, 'ordinary detail refresh does not reread the parent record');

for (const [mode, expected] of [
    ['missing-step', /invoking step is not present in the retained parent record/],
    ['missing', /Parent record unavailable or expired/],
    ['error', /Parent lookup failed; availability is unknown/],
    ['mismatched', /Parent lookup failed; availability is unknown/],
]) {
    parentResponseMode = mode;
    view.testSetActiveTab('dag-parent');
    view.loadParentExecution({ force: true });
    await tick();
    html = elements.get('dagDetailContent').innerHTML;
    assert.match(html, expected, mode);
    if (mode !== 'missing-step') assert.doesNotMatch(html, /invoking step is not present/, 'failed parent reads do not prove a missing step');
    assert.doesNotMatch(html, /incorrect parent body/, 'a response for another parent is rejected');
}
view.destroy();

// Parent navigation still works when no invoking step was recorded.
parentResponseMode = 'available';
view.testSetSelection({ ...parentChild, parent_step_id: '' }, 'dag-parent');
view.renderParentExecution(elements.get('dagDetailContent'));
await tick();
assert.match(elements.get('dagDetailContent').innerHTML, /Invoking step was not recorded/);
assert.doesNotMatch(elements.get('dagDetailContent').innerHTML, /invoking step is not present/);
view.destroy();

// A late parent read cannot graft details onto another selection or a closed
// view. Ignore abort in this transport to exercise the response guards too.
let completeParent, parentSignal;
globalThis.fetch = async (endpoint, options = {}) => {
    const id = decodeURIComponent(endpoint.split('/')[3]);
    if (endpoint.endsWith('/lineage')) return { ok: true, json: async () => ({ request_id: id, executions: [record(id)], partial: false, index_incomplete: false }) };
    if (endpoint.endsWith('/unified')) return { ok: true, status: 200, headers: { get() { return null; } }, json: async () => id === parentChild.request_id ? parentChild : record(id) };
    parentSignal = options.signal;
    return new Promise(resolve => { completeParent = () => resolve({ ok: true, status: 200, json: async () => exactParent }); });
};
view.testInstall([parentChild]);
await view.selectExecution(parentChild.request_id);
view.testSetActiveTab('dag-parent');
view.renderParentExecution(elements.get('dagDetailContent'));
view.testSetActiveTab('dag-raw');
await view.selectExecution('another-selected-run');
assert.equal(parentSignal.aborted, true);
const anotherDetails = elements.get('dagDetailContent').innerHTML;
completeParent();
await tick();
assert.equal(view.testState().request_id, 'another-selected-run');
assert.equal(elements.get('dagDetailContent').innerHTML, anotherDetails);
await view.selectExecution(parentChild.request_id);
view.testSetActiveTab('dag-parent');
view.renderParentExecution(elements.get('dagDetailContent'));
const beforeDestroy = elements.get('dagDetailContent').innerHTML;
view.destroy();
assert.equal(parentSignal.aborted, true);
completeParent();
await tick();
assert.equal(view.testState(), null);
assert.equal(elements.get('dagDetailContent').innerHTML, beforeDestroy);
assert.equal(elements.has('dagExecutionLineage'), false, 'no test path renders the removed top block');
console.log('All DAG lineage view integration tests passed.');
