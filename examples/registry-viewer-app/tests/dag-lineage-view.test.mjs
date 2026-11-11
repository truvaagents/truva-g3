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
        addEventListener() {}, removeEventListener() {}, setAttribute() {}, focus() {}, scrollIntoView() {}, remove() {},
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
const originalSetTimeout = globalThis.setTimeout;
globalThis.setTimeout = (callback, delay, ...args) => delay === 50 ? 0 : originalSetTimeout(callback, delay, ...args);

// Expose private view entry points only in this in-memory test module. Imports
// still point to their real utility implementations; production exports stay unchanged.
const moduleURL = new URL('../static/js/views/dag.js', import.meta.url);
let source = await readFile(moduleURL, 'utf8');
source = source.replace(/from '(\.\.[^']+)'/g, (_, path) => `from '${new URL(path, moduleURL).href}'`);
source += `\nexport { collectDAGLegendEntries, dagLegendSample, loadConversationTitles, selectExecution, renderServerExecutionGroups, renderTimelineTurn, renderLegacyExecutionList, discoverExecutionLineage, renderExecutionLineageNavigation, renderAgentParentInfo, renderParentExecution, loadParentExecution, updateDetailTabs, renderExecutionDetail, renderSingleLLMCard, renderDelegatedExecutionLinks };
export function testState() { return selected; }
export function testInstall(records, groups = []) { executions = records; groupedExecutionGroups = groups; activeTab = 'dag-raw'; for (const group of groups) { expandedGroupKeys.add(group.group_key); if (group.conversation_id) conversationCache.set(group.conversation_id, { data: group, history: [], cursor: '', pending: false }); } }
export function testSetSelection(value, tab = 'dag-raw') { selected = value; activeTab = tab; }
export function testActiveTab() { return activeTab; }
export function testSetActiveTab(value) { activeTab = value; }
export function testTitleGroups(value) { groupedExecutionGroups = value; groupingMode = 'grouped'; }
export function testTitleCache() { return new Map(conversationTitleCache); }
`;
const view = await import(`data:text/javascript;base64,${Buffer.from(source).toString('base64')}`);
// Legend entries are derived from actual rendered element styles and data.
const legendElement = (data, styles = {}) => ({ data: () => data, style: key => ({
    'background-color': '#112233', 'background-opacity': '0.7', 'border-color': '#abcdef',
    'border-style': 'solid', shape: 'round-rectangle', 'underlay-opacity': '0',
    'line-color': '#778899', 'line-style': 'solid', 'target-arrow-shape': 'triangle',
    'target-arrow-color': '#778899', ...styles,
})[key] });
const legendData = [
    { nodeType: 'orchestrator' },
    { nodeType: 'step', status: 'completed', isAgentInvocation: true },
    { nodeType: 'step', status: 'failed', trimmed: true, hasAutoIncludes: true },
    { nodeType: 'skill_llm', llmType: 'skill_activation_selection' },
    { nodeType: 'skill_llm', llmType: 'skill_resource_selection' },
    { nodeType: 'memory_llm', llmType: 'conversation_history_prepare' },
    { nodeType: 'pipeline_hook', hookStatus: 'failed', hookDecision: { action: 'continue' } },
];
const legendOriginal = JSON.stringify(legendData);
const entries = view.collectDAGLegendEntries({
    nodes: () => legendData.map(data => legendElement(data)),
    edges: () => [legendElement({ edgeType: 'skill_evidence' }, { 'line-style': 'dotted', 'target-arrow-shape': 'none' })],
});
assert.ok(entries.nodes.has('delegated_agent'));
assert.ok(entries.nodes.has('llm:skill_llm:skill_activation_selection'));
assert.ok(entries.nodes.has('llm:skill_llm:skill_resource_selection'));
assert.equal(entries.nodes.get('llm:memory_llm:conversation_history_prepare').description, 'Context preparation');
assert.ok(entries.indicators.has('status:failed'));
assert.ok(entries.indicators.has('trimmed'));
assert.ok(!entries.indicators.has('auto_include'), 'a border overridden by trimming is not advertised as visible');
assert.match(entries.indicators.get('hook:failed:continue').description, /request continued/);
assert.ok(![...entries.nodes.keys()].some(key => key.startsWith('checkpoint:')), 'absent checkpoint symbols are omitted');
const association = [...entries.connections.values()][0];
assert.match(association.description, /placement not recorded/);
assert.doesNotMatch(association.sample, /m33 9/, 'unplaced evidence has no directional arrow');
assert.match(association.sample, /stroke-dasharray="1 1"/);
const diamond = view.dagLegendSample(legendElement({}, { shape: 'diamond', 'border-color': '#123456' }));
assert.match(diamond, /<polygon/);
assert.match(diamond, /stroke="#123456"/);
assert.match(diamond, /width="44" height="28"/);
const dashedEdge = view.dagLegendSample(legendElement({}, { 'line-style': 'dashed', 'line-dash-pattern': '8px 4px' }), true);
assert.match(dashedEdge, /stroke-dasharray="8 4"/);
const provenance = legendElement({ edgeType: 'implicit_dependency' }, {
    'line-color': '#f3bb63', 'target-arrow-color': '#f3bb63',
    'line-style': 'dotted', 'line-dash-pattern': '6px 3px',
});
const allConnections = view.collectDAGLegendEntries({ nodes: () => [], edges: () => [
    provenance,
    legendElement({ edgeType: 'llm_step', llmType: 'result_distillation' }, { 'line-color': '#8250dc', 'line-style': 'dotted' }),
    legendElement({ edgeType: 'phase_transition' }, { 'line-style': 'dashed', 'line-dash-pattern': '8px 4px' }),
    { ...legendElement({}), source: () => legendElement({ nodeType: 'step' }), target: () => legendElement({ nodeType: 'step' }) },
    legendElement({ edgeType: 'failed' }),
] }).connections;
const connectionEntries = [...allConnections.values()];
const provenanceEntry = connectionEntries.find(entry => entry.label === 'Earlier-step data');
assert.match(provenanceEntry.sample, /stroke="#f3bb63"/);
assert.match(provenanceEntry.sample, /fill="#f3bb63"/);
assert.match(provenanceEntry.sample, /stroke-dasharray="1 1"/, 'dotted edges ignore inherited dash-pattern just like Cytoscape');
assert.match(provenanceEntry.sample, /stroke-linecap="butt"/, 'round caps must not close the small dotted gaps');
assert.match(provenanceEntry.description, /not execution ordering/);
assert.ok(connectionEntries.some(entry => entry.label === 'Result Distillation link'));
assert.ok(connectionEntries.some(entry => entry.label === 'Step dependency'));
assert.ok(connectionEntries.some(entry => entry.label === 'Failed-step connection'));
const dottedBorder = view.dagLegendSample(legendElement({}, { 'border-style': 'dotted', 'border-dash-pattern': '6px 3px' }));
assert.match(dottedBorder, /stroke-dasharray="1 1"/);
const dashedBorder = view.dagLegendSample(legendElement({}, { 'border-style': 'dashed', 'border-dash-pattern': '7px 2px' }));
assert.match(dashedBorder, /stroke-dasharray="7 2"/);
assert.equal(JSON.stringify(legendData), legendOriginal, 'legend collection does not rewrite graph evidence');
assert.equal(view.collectDAGLegendEntries(null).nodes.size, 0);

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
view.testSetSelection(records[0]);
const delegationLink = view.renderDelegatedExecutionLinks('step-1', records);
assert.match(delegationLink, /class="dag-popup-detail-link" data-lineage-execution="translation"/);
assert.match(delegationLink, />View agent execution →<\/button>/);
assert.doesNotMatch(delegationLink, /dag-row-meta|Each execution retains|Agent execution:|data-refresh-execution-lineage/);
assert.equal(view.renderDelegatedExecutionLinks('ordinary-tool-step', records), '', 'tools without an observed child have no delegation controls');
assert.equal(view.renderDelegatedExecutionLinks('step-17', records), '', 'a nested child belongs to its actual parent step');
const several = view.renderDelegatedExecutionLinks('step-1', [...records, record('second-translation', 'root', 'step-1')]);
assert.equal((several.match(/data-lineage-execution=/g) || []).length, 2);
assert.match(several, /View agent execution 1 →/);
assert.match(several, /View agent execution 2 →/);
assert.match(view.renderDelegatedExecutionLinks('step-1', [{ ...records[1], metadata: { resume_checkpoint_id: 'checkpoint' } }]), /View resumed execution →/);
view.testSetSelection(null);
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
view.testInstall(records, [isolated]);
view.renderServerExecutionGroups([isolated]);
html = elements.get('dagTableBody').innerHTML;
assert.equal((html.match(/data-request-id=/g) || []).length, 1);
assert.match(html, /data-lineage-execution="translation"/);

let lineageCalls = 0, unifiedCalls = 0, finishLineage;
globalThis.fetch = async endpoint => {
    if (endpoint.endsWith('/lineage')) {
        lineageCalls++;
        return new Promise(resolve => { finishLineage = () => resolve({ ok: true, status: 200, headers: { get() { return null; } }, json: async () => ({
            request_id: 'root', executions: records, partial: false, index_incomplete: false,
        }) }); });
    }
    let data;
    if (endpoint.includes('/unified?section=')) {
        unifiedCalls++;
        const requestID = decodeURIComponent(endpoint.split('/')[3]);
        data = { ...records.find(item => item.request_id === requestID), llm_interactions: [{ type: 'synthesis', response: `late-${unifiedCalls}` }] };
    } else if (endpoint === '/api/services') data = { services: [] };
    else data = { groups: [group], timestamp: '2026-11-01T00:00:00Z' };
    return { ok: true, status: 200, headers: { get() { return null; } }, json: async () => data };
};
await view.selectExecution('root');
assert.equal(view.testState().request_id, 'root', 'slow lineage does not block selected details');
assert.match(elements.get('dagDetailContent').innerHTML, /&lt;root&gt;/);
assert.equal(lineageCalls, 0, 'selection does not initiate a family scan');
view.discoverExecutionLineage({ force: true });
await tick();
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
const callsBeforeTimerRefresh = unifiedCalls;
view.refresh();
await tick();
await tick();
assert.equal(lineageCalls, 1, 'periodic refresh causes zero family scans');
assert.equal(unifiedCalls, callsBeforeTimerRefresh, 'timer refresh does not refetch selected evidence');
await view.refresh({ manual: true });
assert.equal(unifiedCalls, callsBeforeTimerRefresh + 2, 'explicit refresh reloads the overview and selected Full Flow');
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
        return { ok: true, status: 200, headers: { get() { return null; } }, json: async () => ({ request_id: requestID, plan: {}, result: {} }) }; // older backend
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
view.discoverExecutionLineage({ force: true });
await tick();
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
    if (endpoint.endsWith('/lineage')) return { ok: true, status: 200, headers: { get() { return null; } }, json: async () => ({
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
assert.ok(requestedPaths.includes(`/api/executions/${encodeURIComponent(escapedID)}/unified?section=overview`));
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
    if (endpoint === '/api/services') return { ok: true, status: 200, headers: { get() { return null; } }, json: async () => ({ services: [] }) };
    const id = decodeURIComponent(endpoint.split('/')[3]);
    if (endpoint.endsWith('/lineage')) return { ok: true, status: 200, headers: { get() { return null; } }, json: async () => ({ request_id: id, executions: [parentChild], partial: false, index_incomplete: false }) };
    if (endpoint.includes('/unified?section=') && id === parentChild.request_id) return { ok: true, status: 200, headers: { get() { return null; } }, json: async () => parentChild };
    parentRecordCalls++;
    assert.equal(endpoint, '/api/executions/parent-record/unified?section=overview', 'parent tab reads only exact parent overview, without debug expansion');
    if (parentResponseMode === 'missing') return { ok: false, status: 404, text: async () => 'not found' };
    if (parentResponseMode === 'error') return { ok: false, status: 503, text: async () => 'unavailable' };
    const data = parentResponseMode === 'missing-step' ? { ...exactParent, plan: { steps: [] }, phase_plans: [], result: { steps: [] } }
        : parentResponseMode === 'mismatched' ? { ...exactParent, request_id: 'other-parent', original_request: 'incorrect parent body' }
        : exactParent;
    return { ok: true, status: 200, headers: { get() { return null; } }, json: async () => data };
};
view.testInstall([parentChild]);
await view.selectExecution(parentChild.request_id);
await tick();
assert.equal(parentRecordCalls, 0, 'selecting a child does not fetch the parent record');
assert.equal(elements.get('dagDetailTitle').textContent, '<child task>');
const childStateBeforeParentRead = view.testState();
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
assert.equal(view.testState(), childStateBeforeParentRead, 'parent details do not replace child unified state');
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
    if (endpoint === '/api/services') return { ok: true, status: 200, headers: { get() { return null; } }, json: async () => ({ services: [] }) };
    const id = decodeURIComponent(endpoint.split('/')[3]);
    if (endpoint.endsWith('/lineage')) return { ok: true, status: 200, headers: { get() { return null; } }, json: async () => ({ request_id: id, executions: [record(id)], partial: false, index_incomplete: false }) };
    if (endpoint.includes('/unified?section=') && id !== 'parent-record') return { ok: true, status: 200, headers: { get() { return null; } }, json: async () => id === parentChild.request_id ? parentChild : record(id) };
    parentSignal = options.signal;
    return new Promise(resolve => { completeParent = () => resolve({ ok: true, status: 200, headers: { get() { return null; } }, json: async () => exactParent }); });
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
view.destroy();
const afterDestroy = elements.get('dagDetailContent').innerHTML;
assert.match(afterDestroy, /Select an execution/);
assert.equal(parentSignal.aborted, true);
completeParent();
await tick();
assert.equal(view.testState(), null);
assert.equal(elements.get('dagDetailContent').innerHTML, afterDestroy);
assert.equal(elements.has('dagExecutionLineage'), false, 'no test path renders the removed top block');

// Group titles compare recorded activity in a bounded independent lookup.
const titleGroups = Array.from({ length: 6 }, (_, index) => ({
    group_key: `title-group-${index}`, conversation_id: `title-conversation-${index}`,
    turns: [{ execution: { ...record(`title-latest-${index}`), original_request: 'Latest follow-up' } }], orphans: [],
}));
const titleLabels = titleGroups.map(group => ({ ...element(), dataset: { conversationTitle: group.conversation_id } }));
const originalQuerySelectorAll = document.querySelectorAll;
document.querySelectorAll = selector => selector === '[data-conversation-title]' ? titleLabels : originalQuerySelectorAll(selector);
let activeTitleReads = 0, maxTitleReads = 0, titleReads = 0;
const titleResponse = (conversationID, request = `First query <${conversationID}>`) => ({
    ok: true, status: 200, headers: { get() { return null; } },
    json: async () => ({ conversation_id: conversationID, turns: [
        { execution: { request_id: 'later-query', original_request: 'Second query with an older snapshot', first_observed_at: '2026-11-06T12:01:00Z' } },
        { execution: { request_id: 'first-query', original_request: request, first_observed_at: '2026-11-06T12:00:00Z' } },
    ] }),
});
globalThis.fetch = async endpoint => {
    const url = new URL(endpoint, 'http://viewer.local');
    assert.equal(url.pathname, '/api/conversations');
    assert.equal(url.searchParams.get('direction'), 'asc');
    assert.equal(url.searchParams.get('limit'), '5');
    assert.equal(url.searchParams.get('q'), null, 'browse search cannot hide the first query');
    assert.equal(url.searchParams.get('from'), null, 'browse window cannot hide the first query');
    titleReads++;
    maxTitleReads = Math.max(maxTitleReads, ++activeTitleReads);
    await new Promise(resolve => originalSetTimeout(resolve, 1));
    activeTitleReads--;
    return titleResponse(url.searchParams.get('conversation_id'));
};
view.testTitleGroups(titleGroups);
await view.loadConversationTitles(titleGroups);
assert.equal(maxTitleReads, 2);
assert.equal(titleReads, titleGroups.length);
assert.ok(titleLabels.every(label => label.textContent.startsWith('First query <')));
view.renderServerExecutionGroups(titleGroups);
assert.match(elements.get('dagTableBody').innerHTML, /First query &lt;title-conversation-0&gt;/);
assert.doesNotMatch(elements.get('dagTableBody').innerHTML, /Latest follow-up/, 'collapsed header uses the first query');
assert.ok(titleLabels.every(label => /earliest recorded activity/.test(label.title)));
await view.loadConversationTitles(titleGroups);
assert.equal(titleReads, titleGroups.length, 'rerenders reuse the preview cache');

const uncertainTitleGroups = [{ ...titleGroups[0], conversation_id: 'uncertain-title' }];
view.testTitleGroups(uncertainTitleGroups);
globalThis.fetch = async () => ({ ok: true, status: 200, headers: new Headers(), json: async () => ({
    conversation_id: 'uncertain-title', has_more: true,
    turns: [{ execution: { request_id: 'legacy-query', original_request: 'Legacy snapshot without start evidence' } }],
}) });
await view.loadConversationTitles(uncertainTitleGroups);
assert.match(view.testTitleCache().get('uncertain-title').hint, /Start evidence is unavailable/);
assert.match(view.testTitleCache().get('uncertain-title').hint, /outside this bounded lookup/);
assert.doesNotMatch(view.testTitleCache().get('uncertain-title').hint, /First retained query/);

const wrongTitleGroups = [{ ...titleGroups[0], conversation_id: 'expected-conversation' }];
view.testTitleGroups(wrongTitleGroups);
globalThis.fetch = async () => titleResponse('another-conversation', 'Wrong conversation query');
await view.loadConversationTitles(wrongTitleGroups);
assert.equal(view.testTitleCache().get('expected-conversation').text, '', 'wrong conversation text is rejected');

let finishTitle, titleSignal;
const lateTitleGroups = [{ ...titleGroups[0], conversation_id: 'late-conversation' }];
view.testTitleGroups(lateTitleGroups);
globalThis.fetch = (endpoint, options) => {
    titleSignal = options.signal;
    return new Promise(resolve => { finishTitle = () => resolve(titleResponse('late-conversation')); });
};
const pendingTitle = view.loadConversationTitles(lateTitleGroups);
view.destroy();
assert.equal(titleSignal.aborted, true);
finishTitle();
await pendingTitle;
assert.equal(view.testTitleCache().size, 0, 'late responses cannot restore title data after teardown');
document.querySelectorAll = originalQuerySelectorAll;
globalThis.setTimeout = originalSetTimeout;
console.log('All DAG lineage view integration tests passed.');
