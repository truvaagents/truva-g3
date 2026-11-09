// Exercise popup-to-card navigation through the actual DAG module and renderers.
import assert from 'node:assert/strict';
import { readFile } from 'node:fs/promises';

const elements = new Map();
const cards = new Map();
const focusEvents = [];
const scrollEvents = [];
const observers = [];
let sentinel = null;
let detailHTML = '';

function element(id = '') {
    let html = '', text = '';
    return {
        id, style: {}, dataset: {}, value: '', disabled: false,
        get innerHTML() { return id === 'dagDetailContent' ? detailHTML : html; },
        set innerHTML(value) {
            html = String(value);
            if (id === 'dagDetailContent') {
                detailHTML = html;
                cards.clear();
                sentinel = null;
                registerRendered(html);
            }
        },
        get textContent() { return text; },
        set textContent(value) {
            text = String(value);
            html = text.replaceAll('&', '&amp;').replaceAll('<', '&lt;').replaceAll('>', '&gt;');
        },
        classList: { add() {}, remove() {}, toggle() {}, contains() { return false; } },
        querySelector(selector) { return selector === '#llmLoadMoreSentinel' ? sentinel : null; },
        querySelectorAll() { return []; },
        prepend(child) { detailHTML = `${child.innerHTML}${detailHTML}`; },
        appendChild() {}, insertAdjacentHTML() {},
        addEventListener() {}, removeEventListener() {}, setAttribute() {}, removeAttribute() {},
        focus(options) { focusEvents.push({ id, options }); },
        scrollIntoView(options) { scrollEvents.push({ id, options }); },
        closest() { return this; }, remove() {},
    };
}

function registerRendered(html) {
    for (const [, id] of html.matchAll(/id="(dag-llm-call-\d+)"/g)) cards.set(id, element(id));
    if (html.includes('id="llmLoadMoreSentinel"')) {
        sentinel = element('llmLoadMoreSentinel');
        sentinel.parentElement = element('llmCardsContainer');
        sentinel.insertAdjacentHTML = (position, batch) => {
            assert.equal(position, 'beforebegin');
            detailHTML += batch;
            registerRendered(batch);
        };
        sentinel.remove = () => { sentinel = null; };
    }
}

globalThis.document = {
    getElementById(id) {
        if (id.startsWith('dag-llm-call-')) return cards.get(id) || null;
        if (id === 'llmLoadMoreSentinel') return sentinel;
        if (!elements.has(id)) elements.set(id, element(id));
        return elements.get(id);
    },
    createElement: () => element(),
    querySelector() { return null; }, querySelectorAll() { return []; },
};
globalThis.sessionStorage = { getItem() { return null; }, setItem() {} };
globalThis.requestAnimationFrame = () => {};
globalThis.IntersectionObserver = class {
    constructor(callback) { this.callback = callback; this.disconnected = false; observers.push(this); }
    observe(target) { this.target = target; }
    disconnect() { this.disconnected = true; }
    // Deliberately callable after disconnect, simulating a queued stale entry.
    emit() { this.callback([{ isIntersecting: true }]); }
};
const originalSetTimeout = globalThis.setTimeout;
globalThis.setTimeout = (callback, delay, ...args) => delay === 50 ? 0 : originalSetTimeout(callback, delay, ...args);

const moduleURL = new URL('../static/js/views/dag.js', import.meta.url);
let source = await readFile(moduleURL, 'utf8');
source = source.replace(/from '(\.\.[^']+)'/g, (_, path) => `from '${new URL(path, moduleURL).href}'`);
source += `
export { openLLMCallDetails, buildLLMBackedNodeData, setDagDetailTab, selectExecution };
export function testSeed(execution) {
    destroy();
    selected = execution;
    selectedOverview = { ...execution, llm_interactions: undefined };
    selectedSection = 'flow'; activeTab = 'dag-viz';
    annotateCallLabels(selected.llm_interactions);
}
export function testSnapshot() { return { selected, selectedSection, activeTab, selectionVersion }; }
`;
const view = await import(`data:text/javascript;base64,${Buffer.from(source).toString('base64')}`);

const interaction = (number, values = {}) => ({
    type: 'plan_generation', timestamp: `2026-11-07T12:00:${String(number).padStart(2, '0')}Z`,
    phase_number: 1, trace_id: '1234567890abcdef1234567890abcdef', model: 'test-model', provider: 'test-provider',
    prompt: `Unique prompt ${number}`, response: `Unique response ${number}`, success: true, ...values,
});
const record = (id, interactions = []) => ({
    request_id: id, original_request_id: id, original_request: `Request ${id}`, agent_name: 'test-agent',
    created_at: '2026-11-07T12:00:00Z', lifecycle: { state: 'completed' },
    plan: { steps: [] }, result: { steps: [] }, llm_interactions: interactions,
    available_sections: ['overview', 'flow', 'llm', 'pre', 'post', 'steps'],
});
let responseCalls = [];
let responseStatus = 200;
let delayResponse = null;
const requests = [];
globalThis.fetch = async endpoint => {
    const url = new URL(endpoint, 'http://viewer.local');
    requests.push(url);
    if (url.pathname === '/api/services') return { ok: true, status: 200, headers: new Headers(), json: async () => ({ services: [] }) };
    assert.ok(url.pathname.endsWith('/unified'), `Unexpected endpoint ${url.pathname}`);
    const section = url.searchParams.get('section');
    const id = decodeURIComponent(url.pathname.split('/')[3]);
    const status = responseStatus;
    const calls = JSON.parse(JSON.stringify(responseCalls));
    const gate = delayResponse;
    if (gate && section === gate.section) {
        gate.started();
        await gate.promise; // Ignores AbortSignal so ownership guards are exercised.
    }
    if (status !== 200) return { ok: false, status, headers: new Headers(), text: async () => status === 403 ? 'access denied' : 'execution deleted' };
    return { ok: true, status, headers: new Headers(), json: async () => ({ ...record(id, section === 'overview' ? [] : calls), detail_level: section, payloads_deferred: section === 'overview' }) };
};

function setup(calls, id = 'run-one') {
    responseCalls = calls;
    responseStatus = 200;
    delayResponse = null;
    focusEvents.length = 0;
    scrollEvents.length = 0;
    requests.length = 0;
    view.testSeed(record(id, JSON.parse(JSON.stringify(calls))));
}
function targetFor(index) {
    const snapshot = view.testSnapshot();
    const node = view.buildLLMBackedNodeData(snapshot.selected.llm_interactions[index]);
    return { target: { identity: node.llmCallIdentity, tab: node.llmDetailTab }, requestID: snapshot.selected.request_id, version: snapshot.selectionVersion, node };
}
function navigate(target) { return view.openLLMCallDetails(target.target, target.requestID, target.version); }
function deferred(section) {
    let resolve, started;
    const gate = { section, promise: new Promise(finish => { resolve = finish; }), startedPromise: new Promise(finish => { started = finish; }) };
    gate.started = started;
    gate.resolve = resolve;
    return gate;
}

try {
    // Every call has the same type/phase label; recorded identity survives a
    // refetch that inserts a row and moves the target beyond the initial batch.
    const calls = Array.from({ length: 25 }, (_, index) => interaction(index));
    setup(calls);
    const target = targetFor(2);
    const targetCall = { ...calls[2] };
    responseCalls = [interaction(40), ...calls.filter((_, index) => index !== 2).slice(0, 16), targetCall, ...calls.slice(17)];
    const newIndex = responseCalls.findIndex(call => call.timestamp === targetCall.timestamp);
    assert.ok(newIndex >= 10);
    assert.equal(await navigate(target), true);
    assert.equal(view.testSnapshot().activeTab, 'dag-llm');
    assert.equal(view.testSnapshot().selected.llm_interactions[newIndex].prompt, targetCall.prompt);
    assert.notEqual(view.buildLLMBackedNodeData(view.testSnapshot().selected.llm_interactions[newIndex]).callLabel, target.node.callLabel, 'display suffix may change after insertion/reordering');
    assert.deepEqual(focusEvents.map(event => event.id), [`dag-llm-call-${newIndex}`]);
    assert.deepEqual(scrollEvents.map(event => event.id), [`dag-llm-call-${newIndex}`]);
    assert.equal(cards.size, 20, 'explicit reveal renders the target batch without requiring observer intersection');
    assert.equal(cards.has('dag-llm-call-24'), false, 'later unrelated cards stay deferred');
    assert.equal(requests.length, 1, 'navigation performs one requested section read');

    // Hook navigation uses its real Pre/Post renderer and original array index.
    for (const [hookPhase, type, tab] of [['pre', 'activity_compaction', 'dag-pre'], ['post', 'event_summarization', 'dag-post']]) {
        setup([interaction(0), interaction(1, { hook_phase: hookPhase, type }), interaction(2)]);
        const hookTarget = targetFor(1);
        assert.equal(hookTarget.target.tab, tab);
        responseCalls = [responseCalls[2], responseCalls[0], responseCalls[1]];
        assert.equal(await navigate(hookTarget), true);
        assert.equal(view.testSnapshot().activeTab, tab);
        assert.deepEqual(focusEvents.map(event => event.id), ['dag-llm-call-2']);
        setup([interaction(0), interaction(1, { hook_phase: hookPhase, type }), interaction(2)]);
        const filteredTarget = targetFor(1);
        responseCalls = [responseCalls[1]]; // Current backend projects hook-only sections.
        assert.equal(await navigate(filteredTarget), true);
        assert.deepEqual(focusEvents.map(event => event.id), ['dag-llm-call-0'], 'filtered section uses its own retained-array index');
    }

    // Missing or indistinguishable retained evidence never focuses another row.
    for (const outcome of ['missing', 'ambiguous']) {
        setup([interaction(0), interaction(1)]);
        const target = targetFor(1);
        responseCalls = outcome === 'missing' ? [interaction(0)] : [interaction(1), interaction(1, { prompt: 'Indistinguishable metadata, different payload' })];
        assert.equal(await navigate(target), false);
        assert.equal(focusEvents.length, 0);
        assert.match(detailHTML, outcome === 'missing' ? /unavailable in the latest retained evidence/ : /cannot be distinguished/);
    }

    // A section read can finish after request selection changes or the view
    // closes, even when transport cancellation is ignored.
    for (const interruption of ['new-selection', 'different-tab', 'destroy']) {
        setup([interaction(0)]);
        const target = targetFor(0);
        const gate = deferred('llm');
        delayResponse = gate;
        const pending = navigate(target);
        await gate.startedPromise;
        delayResponse = null;
        if (interruption === 'new-selection') await view.selectExecution('new-run');
        else if (interruption === 'different-tab') await view.setDagDetailTab('dag-post');
        else view.destroy();
        gate.resolve();
        assert.equal(await pending, false);
        assert.equal(focusEvents.length, 0);
        assert.equal(view.testSnapshot().selected?.request_id ?? null, interruption === 'new-selection' ? 'new-run' : interruption === 'different-tab' ? 'run-one' : null);
        if (interruption === 'different-tab') assert.equal(view.testSnapshot().activeTab, 'dag-post');
    }

    for (const status of [404, 401, 403]) {
        setup([interaction(0)]);
        const target = targetFor(0);
        responseStatus = status;
        assert.equal(await navigate(target), false);
        assert.equal(focusEvents.length, 0);
        if (status === 401 || status === 403) assert.equal(view.testSnapshot().selected, null, 'denial clears retained execution evidence');
        else assert.match(detailHTML, /execution deleted/);
    }

    // Retired observers cannot append cards after a tab change, execution
    // change, access denial or view destruction, including queued callbacks.
    for (const interruption of ['tab', 'selection', 'denial', 'destroy']) {
        setup(calls);
        assert.equal(await navigate(targetFor(2)), true);
        assert.equal(cards.size, 10);
        const observer = observers.at(-1);
        assert.equal(observer.disconnected, false);
        if (interruption === 'tab') await view.setDagDetailTab('dag-pre');
        else if (interruption === 'selection') await view.selectExecution('new-run');
        else if (interruption === 'denial') {
            responseStatus = 403;
            await view.setDagDetailTab('dag-post');
        } else view.destroy();
        assert.equal(observer.disconnected, true, `${interruption} disconnects the observer`);
        const before = detailHTML;
        observer.emit();
        assert.equal(detailHTML, before, `${interruption}: a queued old callback cannot append cards`);
    }
    console.log('LLM popup navigation, lazy reveal and ownership checks passed.');
} finally {
    view.destroy();
    globalThis.setTimeout = originalSetTimeout;
}
