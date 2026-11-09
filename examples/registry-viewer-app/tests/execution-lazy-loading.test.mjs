// Request-level regression for bounded browsing and explicit evidence loading.
import assert from 'node:assert/strict';
import { readFile } from 'node:fs/promises';

const elements = new Map();
function element() {
    return {
        innerHTML: '', _text: '', value: '', style: {}, dataset: {}, disabled: false,
        get textContent() { return this._text; },
        set textContent(value) { this._text = String(value); this.innerHTML = this._text.replaceAll('&', '&amp;').replaceAll('<', '&lt;').replaceAll('>', '&gt;'); },
        classList: { add() {}, remove() {}, toggle() {}, contains() { return false; } },
        querySelector() { return null; }, querySelectorAll() { return []; },
        insertAdjacentHTML(_position, html) { this.innerHTML = html + this.innerHTML; },
        appendChild() {}, addEventListener() {}, removeEventListener() {}, setAttribute() {},
        removeAttribute() {}, focus() {}, scrollIntoView() {}, remove() {},
    };
}
globalThis.document = {
    getElementById(id) { if (!elements.has(id)) elements.set(id, element()); return elements.get(id); },
    createElement: element, querySelector() { return null; }, querySelectorAll() { return []; },
};
globalThis.sessionStorage = { getItem() { return null; }, setItem() {} };
globalThis.requestAnimationFrame = () => {};
const realSetTimeout = globalThis.setTimeout;
// Graph layout is covered separately; prevent the canvas timer in this DOM test.
globalThis.setTimeout = (callback, delay, ...args) => delay === 50 ? 0 : realSetTimeout(callback, delay, ...args);
const moduleURL = new URL('../static/js/views/dag.js', import.meta.url);
let source = await readFile(moduleURL, 'utf8');
source = source.replace(/from '(\.\.[^']+)'/g, (_, path) => `from '${new URL(path, moduleURL).href}'`);
source += `\nexport { fetchExecutions, navigateExecutionPage, selectExecution, setDagDetailTab, loadConversationTimeline, navigateConversationPage, setDagViewMode, rawExecutionText, loadSelectedSection, openConversationTimeline, closeConversationTimeline, discoverExecutionLineage };
export function testSnapshot() { return { executions, selected, selectedOverview, selectedSection, viewMode, groupingMode, listPageCursor, listPageNumber, listPageCursors: new Map(listPageCursors), conversationKeys: [...conversationCache.keys()] }; }
`;
const view = await import(`data:text/javascript;base64,${Buffer.from(source).toString('base64')}`);
const requests = [];
let emptySearch = false;
let unavailableLLM = false;
let denyRequest = null;
let denialStatus = 403;
let postSynthesisMode = 'none';
let delayedSteps = null;
let includeLLMCall = false;
let missingRawJSON = false;
let resumeCheckpointID = 'checkpoint-original';
const record = id => ({ request_id: id, original_request_id: id, original_request: `Request ${id}`, created_at: '2026-11-06T12:00:00Z', lifecycle: { state: 'completed' }, agent_name: 'test-agent' });
const overview = id => ({ ...record(id), ...(id === 'child' ? { parent_request_id: 'parent', parent_step_id: 'step-parent' } : {}), detail_level: 'overview', payloads_deferred: true, available_sections: ['overview', 'steps', 'llm', 'pre', 'post', 'hitl', 'skills', 'raw', 'flow'], plan: { steps: [] }, result: { steps: [] } });
globalThis.fetch = async (endpoint, options = {}) => {
    const url = new URL(endpoint, 'http://viewer.local');
    requests.push({ url, options });
    if (denyRequest?.(url)) return { ok: false, status: denialStatus, headers: new Headers(), text: async () => 'access denied' };
    let data;
    if (url.pathname === '/api/executions') {
        assert.equal(url.searchParams.get('limit'), '30');
        assert.equal(url.searchParams.get('group_conversations'), null, 'new session starts in flat mode');
        const cursor = url.searchParams.get('cursor');
        data = { executions: emptySearch ? [] : cursor ? [record('older')] : [record('recent-a'), record('recent-b')], next_cursor: cursor ? '' : 'next-page', timestamp: '2026-11-06T12:00:00Z' };
    } else if (url.pathname === '/api/services') {
        data = { services: [] };
    } else if (url.pathname.startsWith('/api/conversations')) {
        assert.equal(url.searchParams.get('limit'), '5');
        data = { conversation_id: url.searchParams.get('conversation_id'), turns: [{ execution: record('turn'), related_executions: [] }], execution_count: 1, next_cursor: url.searchParams.has('cursor') ? '' : 'turn-page-2' };
    } else if (url.pathname.endsWith('/unified')) {
        const id = decodeURIComponent(url.pathname.split('/')[3]);
        const section = url.searchParams.get('section');
        assert.ok(section, 'detail calls must request a named section');
        data = { ...overview(id), detail_level: section, payloads_deferred: section === 'overview', loaded_sections: [section] };
        if (section === 'flow') data.metadata = { plan_regeneration_events: JSON.stringify([{ phase_number: 7, validation_error: 'Verbose rejected-plan error', original_plan_id: 'old-plan', regenerated_plan_id: 'new-plan' }]) };
        if (id.startsWith('resumed-')) {
            data.metadata = { resume_checkpoint_id: resumeCheckpointID };
            data.resume_step_numbering = {
                checkpoint_id: resumeCheckpointID,
                completed_step_count: section === 'hitl' ? 3 : null,
                display_step_offset: section === 'hitl' ? 3 : null,
                ...(section === 'hitl' ? { source: 'agent_checkpoint_api' } : {}),
            };
            data.plan = { steps: [{ step_id: 'opaque-step-id', agent_name: 'test-tool', instruction: 'Continue approved work' }] };
            data.result = { steps: [] };
        }
        if (section === 'steps' && delayedSteps) await delayedSteps.promise;
        if (section === 'llm' || section === 'flow' || section === 'pre' || section === 'post') data.llm_interactions = [];
        if (section === 'llm' && includeLLMCall) data.llm_interactions = [{ type: 'plan_generation', timestamp: '2026-11-06T12:00:00Z', prompt: 'Recorded planning prompt', response: 'Recorded planning response', success: true }];
        if (section === 'post') {
            data.final_response = 'final app reply';
            data.llm_interactions = postSynthesisMode === 'none' ? [] : [{ type: 'synthesis', response: postSynthesisMode === 'equal' ? 'final app reply' : 'different model output' }];
        }
        if (section === 'llm' && unavailableLLM) data.section_warnings = ['LLM debug data is currently unavailable.'];
        if (section === 'raw' && !missingRawJSON) data.raw_execution_json = '  {"order_id":9007199254740993,"exact":"original raw execution","note":"a<b"}\n';
    } else {
        assert.fail(`Unexpected eager request: ${url.pathname}`);
    }
    return { ok: true, status: 200, headers: new Headers(), json: async () => data };
};

await view.fetchExecutions();
assert.equal(requests.length, 1, 'initial view fetches only a summary page');
const firstWindow = requests[0].url.searchParams.get('from');
assert.ok(firstWindow, 'default browsing has a bounded time window');
await view.navigateExecutionPage('next');
assert.equal(view.testSnapshot().executions.length, 1, 'next page replaces the old one');
assert.equal(requests[1].url.searchParams.get('from'), firstWindow, 'cursor retains its time window');
await view.navigateExecutionPage('previous');
assert.equal(view.testSnapshot().executions.length, 2);
assert.equal(view.testSnapshot().listPageNumber, 1);
emptySearch = true;
document.getElementById('dagSearchInput').value = 'missing needle';
await view.fetchExecutions();
assert.equal(document.getElementById('dagLoadMore').title, 'Continue search');
assert.equal(requests.at(-1).url.searchParams.get('q'), 'missing needle');
emptySearch = false;

requests.length = 0;
await view.selectExecution('chosen');
assert.equal(view.testSnapshot().selectedSection, 'flow');
assert.equal(view.testSnapshot().viewMode, 'full', 'explicit execution selection defaults to Full Flow');
assert.doesNotMatch(document.getElementById('dagDetailContent').innerHTML, /Verbose rejected-plan error|Phase 7 plan regenerated/, 'regeneration evidence does not add header text');
assert.equal(requests.filter(item => item.url.pathname.endsWith('/unified')).length, 2);
assert.equal(requests.find(item => item.url.pathname.endsWith('/unified')).url.searchParams.get('section'), 'overview');
assert.equal(requests.filter(item => item.url.pathname.endsWith('/unified')).at(-1).url.searchParams.get('section'), 'flow');
assert.ok(!requests.some(item => item.url.pathname.includes('/lineage')), 'selection does not discover a family');
await view.setDagDetailTab('dag-llm');
assert.equal(requests.at(-1).url.searchParams.get('section'), 'llm', 'LLM evidence loads only on tab action');
unavailableLLM = true;
await view.loadSelectedSection('llm', { force: true });
assert.match(document.getElementById('dagDetailContent').innerHTML, /currently unavailable/);
assert.match(document.getElementById('dagDetailContent').innerHTML, /data-retry-execution-section/);
assert.doesNotMatch(document.getElementById('dagDetailContent').innerHTML, /No LLM calls recorded/);
unavailableLLM = false;
await view.loadSelectedSection('llm', { force: true });
assert.doesNotMatch(document.getElementById('dagDetailContent').innerHTML, /currently unavailable/);
requests.length = 0;
await view.refresh();
assert.ok(requests.every(item => item.url.pathname === '/api/executions'), 'timer refresh does not fetch detail or timeline');
requests.length = 0;
await view.refresh({ manual: true });
assert.deepEqual(requests.filter(item => item.url.pathname.endsWith('/unified')).map(item => item.url.searchParams.get('section')), ['overview', 'llm'], 'manual refresh reloads only the selected evidence section after overview');
await view.setDagDetailTab('dag-post');
assert.match(document.getElementById('dagDetailContent').innerHTML, /comparison is unavailable/);
assert.doesNotMatch(document.getElementById('dagDetailContent').innerHTML, /application changed/);
postSynthesisMode = 'equal';
await view.loadSelectedSection('post', { force: true });
assert.doesNotMatch(document.getElementById('dagDetailContent').innerHTML, /application changed/);
postSynthesisMode = 'changed';
await view.loadSelectedSection('post', { force: true });
assert.match(document.getElementById('dagDetailContent').innerHTML, /application changed/);
assert.match(document.getElementById('dagDetailContent').innerHTML, /LLM Calls tab/);
await view.setDagDetailTab('dag-raw');
assert.match(document.getElementById('dagDetailContent').innerHTML, /original raw execution/);
assert.match(document.getElementById('dagDetailContent').innerHTML, /9007199254740993/);
assert.match(document.getElementById('dagDetailContent').innerHTML, /<pre class="json-view">\{\n  /, 'raw JSON is indented for display');
assert.equal(view.rawExecutionText(), '  {"order_id":9007199254740993,"exact":"original raw execution","note":"a<b"}\n', 'raw copy preserves exact JSON bytes and large numeric IDs');

// Returning to an already loaded tab must cancel a different section's pending
// request, including transports that ignore cancellation and still return data.
includeLLMCall = true;
for (const [tab, section] of [['dag-llm', 'llm'], ['dag-raw', 'raw']]) {
    await view.setDagDetailTab(tab);
    const loaded = document.getElementById('dagDetailContent').innerHTML;
    let finish;
    delayedSteps = { promise: new Promise(resolve => { finish = resolve; }) };
    const pending = view.setDagDetailTab('dag-steps');
    await new Promise(resolve => setImmediate(resolve));
    const pendingRequest = requests.at(-1);
    assert.equal(pendingRequest.url.searchParams.get('section'), 'steps');
    await view.setDagDetailTab(tab);
    assert.equal(pendingRequest.options.signal.aborted, true, 'returning to a loaded tab aborts other evidence loads');
    assert.equal(document.getElementById('dagDetailContent').innerHTML, loaded, 'loaded evidence displays immediately');
    finish(); await pending; delayedSteps = null;
    assert.equal(view.testSnapshot().selectedSection, section, 'late result cannot change the selected section');
    assert.equal(document.getElementById('dagDetailContent').innerHTML, loaded, 'late result cannot replace the active evidence');
    if (section === 'raw') assert.match(view.rawExecutionText(), /original raw execution/);
    else assert.doesNotMatch(loaded, /No LLM calls recorded/);
}
includeLLMCall = false;
assert.equal(view.rawExecutionText({ detail_level: 'steps', raw_execution: { request_id: 'derived' } }), null, 'derived DTOs are not raw execution JSON');
await view.setDagDetailTab('dag-viz');
missingRawJSON = true;
await view.setDagDetailTab('dag-raw');
assert.match(document.getElementById('dagDetailContent').innerHTML, /Raw execution JSON is unavailable/);
assert.doesNotMatch(document.getElementById('dagDetailContent').innerHTML, /class="copy-btn"/, 'missing raw bytes cannot be copied as a derived record');
missingRawJSON = false;
await view.loadSelectedSection('raw', { force: true });
assert.match(view.rawExecutionText(), /original raw execution/, 'raw evidence can be retried after an invalid response');
await view.setDagDetailTab('dag-viz');
await view.setDagViewMode('steps');
assert.equal(view.testSnapshot().selectedSection, 'overview', 'Steps Only returns to the retained lightweight overview');
await view.setDagViewMode('full');
await new Promise(resolve => setImmediate(resolve));
assert.equal(requests.at(-1).url.searchParams.get('section'), 'flow');

await view.loadConversationTimeline('conversation-a');
assert.equal(requests.at(-1).url.searchParams.get('limit'), '5');
await view.navigateConversationPage('next', 'conversation-a');
assert.equal(requests.at(-1).url.searchParams.get('cursor'), 'turn-page-2');
await view.navigateConversationPage('previous', 'conversation-a');
assert.equal(requests.at(-1).url.searchParams.get('cursor'), null);
for (let i = 0; i < 12; i++) await view.loadConversationTimeline(`bounded-${i}`);
assert.equal(view.testSnapshot().conversationKeys.length, 10, 'conversation cache is bounded');

// Unknown resume offsets are explicitly invocation-local. Once the operator
// loads the checkpoint, retain only its small numbering fact across tabs.
await view.selectExecution('resumed-first');
await view.setDagDetailTab('dag-steps');
assert.match(document.getElementById('dagDetailContent').innerHTML, /Resume step 1/);
assert.match(document.getElementById('dagDetailContent').innerHTML, /workflow-wide numbering is unavailable/);
await view.setDagDetailTab('dag-hitl');
assert.equal(view.testSnapshot().selected.resume_step_numbering.completed_step_count, 3);
assert.equal(view.testSnapshot().selected.resume_step_numbering.display_step_offset, 3);
await view.setDagDetailTab('dag-steps');
assert.match(document.getElementById('dagDetailContent').innerHTML, />Step 4<\/span>/);
assert.doesNotMatch(document.getElementById('dagDetailContent').innerHTML, /Resume step 1/);
assert.equal(view.testSnapshot().selectedOverview.resume_step_numbering.source, 'agent_checkpoint_api');
assert.equal(view.testSnapshot().selectedOverview.resume_step_numbering.display_step_offset, 3);
assert.equal(view.testSnapshot().selected.hitl_lifecycle, undefined, 'retaining the count does not retain a heavy checkpoint body');
await view.setDagDetailTab('dag-viz');
assert.equal(view.testSnapshot().selected.resume_step_numbering.completed_step_count, 3);
resumeCheckpointID = 'different-checkpoint';
await view.setDagDetailTab('dag-steps');
assert.equal(view.testSnapshot().selected.resume_step_numbering.completed_step_count, null, 'an observed count cannot cross checkpoint identities');
await view.setDagDetailTab('dag-viz');
assert.equal(view.testSnapshot().selected.resume_step_numbering.checkpoint_id, 'different-checkpoint');
assert.equal(view.testSnapshot().selected.resume_step_numbering.completed_step_count, null, 'overview cannot resurrect the previous checkpoint count');
resumeCheckpointID = 'checkpoint-original';
await view.selectExecution('resumed-second');
await view.setDagDetailTab('dag-steps');
assert.match(document.getElementById('dagDetailContent').innerHTML, /Resume step 1/);
assert.equal(view.testSnapshot().selected.resume_step_numbering.completed_step_count, null, 'a new execution cannot inherit another request\'s checkpoint count');

// A denial must remove cached evidence, not merely replace its visible card.
for (const status of [401, 403]) {
    denialStatus = status;
    for (const boundary of ['selection', 'section', 'list', 'timeline', 'parent', 'lineage']) {
        denyRequest = null;
        await view.selectExecution(boundary === 'parent' ? 'child' : 'chosen');
        await view.setDagDetailTab('dag-raw');
        assert.match(document.getElementById('dagDetailContent').innerHTML, /original raw execution/);
        if (boundary === 'selection') {
            denyRequest = url => url.pathname.includes('/forbidden/');
            await view.selectExecution('forbidden');
        } else if (boundary === 'section') {
            denyRequest = url => url.searchParams.get('section') === 'llm';
            await view.setDagDetailTab('dag-llm');
        } else if (boundary === 'list') {
            await view.fetchExecutions();
            denyRequest = url => url.pathname === '/api/executions';
            await view.fetchExecutions();
            assert.equal(view.testSnapshot().executions.length, 0, 'denied list drops old rows');
        } else if (boundary === 'timeline') {
            view.openConversationTimeline('restricted-conversation');
            await new Promise(resolve => setImmediate(resolve));
            assert.match(document.getElementById('dagTimelineContent').innerHTML, /Request turn/);
            denyRequest = url => url.pathname === '/api/conversations';
            await view.loadConversationTimeline('restricted-conversation', null, { force: true });
            assert.ok(!view.testSnapshot().conversationKeys.includes('restricted-conversation'));
            assert.doesNotMatch(document.getElementById('dagTimelineContent').innerHTML, /Request turn/, 'denied timeline never shows cached records');
            view.closeConversationTimeline();
        } else if (boundary === 'parent') {
            denyRequest = url => url.pathname.includes('/parent/');
            await view.setDagDetailTab('dag-parent');
            await new Promise(resolve => setImmediate(resolve));
        } else {
            denyRequest = url => url.pathname.endsWith('/lineage');
            view.discoverExecutionLineage({ force: true });
            await new Promise(resolve => setImmediate(resolve));
        }
        assert.equal(view.testSnapshot().selected, null, `${status} ${boundary}: selected evidence cleared`);
        assert.equal(view.testSnapshot().selectedOverview, null, `${status} ${boundary}: overview cannot be restored from cache`);
        assert.doesNotMatch(document.getElementById('dagDetailContent').innerHTML, /original raw execution/);
        await view.setDagDetailTab('dag-viz');
        assert.equal(view.testSnapshot().selected, null, 'switching tabs cannot resurrect denied evidence');
    }
}
denyRequest = null;
view.destroy();
assert.equal(view.testSnapshot().selected, null);
assert.match(document.getElementById('dagDetailContent').innerHTML, /Select an execution/);
assert.doesNotMatch(document.getElementById('dagDetailContent').innerHTML, /original raw execution/);
globalThis.setTimeout = realSetTimeout;
console.log('Execution paging and lazy evidence checks passed.');
