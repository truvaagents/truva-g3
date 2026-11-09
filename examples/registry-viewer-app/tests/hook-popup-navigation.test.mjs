// Hook popups summarize invocation facts and navigate to exact retained evidence.
import assert from 'node:assert/strict';
import { readFile } from 'node:fs/promises';

const elements = new Map(), cards = new Map();
const focusEvents = [], scrollEvents = [];
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
                for (const [, cardID] of html.matchAll(/id="(dag-hook-details-\d+)"/g)) cards.set(cardID, element(cardID));
            }
        },
        get textContent() { return text; },
        set textContent(value) { text = String(value); html = text.replaceAll('&', '&amp;').replaceAll('<', '&lt;').replaceAll('>', '&gt;'); },
        classList: { add() {}, remove() {}, toggle() {}, contains() { return false; } },
        querySelector() { return null; }, querySelectorAll() { return []; },
        prepend(child) { detailHTML = child.innerHTML + detailHTML; },
        appendChild() {}, insertAdjacentHTML() {}, addEventListener() {}, removeEventListener() {},
        setAttribute() {}, removeAttribute() {}, remove() {},
        focus(options) { focusEvents.push({ id, options }); },
        scrollIntoView(options) { scrollEvents.push({ id, options }); },
    };
}
globalThis.document = {
    getElementById(id) {
        if (id.startsWith('dag-hook-details-')) return cards.get(id) || null;
        if (!elements.has(id)) elements.set(id, element(id));
        return elements.get(id);
    },
    createElement: () => element(), querySelector() { return null; }, querySelectorAll() { return []; },
};
globalThis.sessionStorage = { getItem() { return null; }, setItem() {} };
globalThis.requestAnimationFrame = () => {};
const originalSetTimeout = globalThis.setTimeout;
globalThis.setTimeout = (callback, delay, ...args) => delay === 50 ? 0 : originalSetTimeout(callback, delay, ...args);

const moduleURL = new URL('../static/js/views/dag.js', import.meta.url);
let source = await readFile(moduleURL, 'utf8');
source = source.replace(/from '(\.\.[^']+)'/g, (_, path) => `from '${new URL(path, moduleURL).href}'`);
source += `
export { openHookDetails, pipelineHookNodeData, renderHookPopupSummary, setDagDetailTab, selectExecution };
export function testSeed(execution) {
    destroy(); selected = execution;
    selectedOverview = { ...execution, pipeline_hooks: undefined };
    selectedSection = 'flow'; activeTab = 'dag-viz';
}
export function testSnapshot() { return { selected, selectedSection, activeTab, selectionVersion }; }
`;
const view = await import(`data:text/javascript;base64,${Buffer.from(source).toString('base64')}`);

const hook = (values = {}) => ({
    hook_name: 'RepeatedHook', phase: 'after_planning', sequence: 2, plan_phase: 3,
    started_at: '2026-11-07T12:00:00Z', duration: 1234567, status: 'succeeded',
    decision: { failure_policy: 'fail_open', action: 'continue', reason: 'completed' }, effects: [], ...values,
});
const record = (id, hooks = []) => ({
    request_id: id, original_request_id: id, original_request: `Request ${id}`, agent_name: 'test-agent',
    created_at: '2026-11-07T12:00:00Z', lifecycle: { state: 'completed' },
    plan: { steps: [] }, result: { steps: [] }, llm_interactions: [], pipeline_hooks: hooks,
    available_sections: ['overview', 'flow', 'pre', 'post', 'steps', 'raw'],
});
let responseHooks = [], responseStatus = 200, responseDelay = null, missingRaw = false;
const requests = [];
globalThis.fetch = async endpoint => {
    const url = new URL(endpoint, 'http://viewer.local');
    requests.push(url);
    if (url.pathname === '/api/services') return { ok: true, status: 200, headers: new Headers(), json: async () => ({ services: [] }) };
    assert.ok(url.pathname.endsWith('/unified'), `Unexpected endpoint ${url.pathname}`);
    const section = url.searchParams.get('section');
    const id = decodeURIComponent(url.pathname.split('/')[3]);
    const hooks = JSON.parse(JSON.stringify(responseHooks));
    const status = responseStatus;
    const gate = responseDelay;
    if (gate?.section === section) { gate.started(); await gate.promise; }
    if (status !== 200) return { ok: false, status, headers: new Headers(), text: async () => status === 403 ? 'access denied' : 'evidence unavailable' };
    const data = { ...record(id, section === 'overview' ? [] : hooks), detail_level: section, payloads_deferred: section === 'overview' };
    if (section === 'raw' && !missingRaw) data.raw_execution_json = JSON.stringify({ request_id: id, pipeline_hooks: hooks });
    return { ok: true, status: 200, headers: new Headers(), json: async () => data };
};
function setup(hooks) {
    responseHooks = hooks; responseStatus = 200; responseDelay = null; missingRaw = false;
    requests.length = 0; focusEvents.length = 0; scrollEvents.length = 0;
    view.testSeed(record('run-one', JSON.parse(JSON.stringify(hooks))));
}
function targetFor(index) {
    const snapshot = view.testSnapshot();
    const data = view.pipelineHookNodeData(snapshot.selected.pipeline_hooks[index], `node-${index}`);
    return { target: { identity: data.hookIdentity, tab: data.hookDetailTab }, requestID: snapshot.selected.request_id, version: snapshot.selectionVersion, data };
}
function navigate(target) { return view.openHookDetails(target.target, target.requestID, target.version); }
function deferred(section) {
    let resolve, started;
    const gate = { section, promise: new Promise(finish => { resolve = finish; }), startedPromise: new Promise(finish => { started = finish; }) };
    gate.resolve = resolve; gate.started = started;
    return gate;
}

try {
    // Each tuple dimension distinguishes otherwise repeated invocations.
    const original = hook();
    const siblings = [
        hook({ hook_name: 'AnotherHook' }), hook({ phase: 'before_planning' }),
        hook({ sequence: 1 }), hook({ plan_phase: 4 }), hook({ started_at: '2026-11-07T12:00:01Z' }),
    ];
    setup([original, ...siblings]);
    const target = targetFor(0);
    assert.equal(new Set([target.data.hookIdentity, ...siblings.map((item, index) => view.pipelineHookNodeData(item, index).hookIdentity)]).size, 6);
    responseHooks = [...siblings.slice().reverse(), hook({ sequence: 8 }), original];
    assert.equal(await navigate(target), true);
    assert.equal(view.testSnapshot().activeTab, 'dag-pre');
    assert.equal(focusEvents.at(-1).id, 'dag-hook-details-6');
    assert.equal(scrollEvents.at(-1).id, 'dag-hook-details-6');
    assert.equal(requests.length, 1, 'navigation loads exactly the requested section');

    for (const [phase, tab] of [['before_planning', 'dag-pre'], ['after_planning', 'dag-pre'], ['after_execution', 'dag-post'], ['after_synthesis', 'dag-post']]) {
        setup([hook({ phase })]);
        const target = targetFor(0);
        assert.equal(target.target.tab, tab);
        assert.equal(await navigate(target), true);
        assert.deepEqual(focusEvents.map(event => event.id), ['dag-hook-details-0']);
    }
    setup([hook({ plan_phase: undefined })]);
    assert.equal(targetFor(0).target.tab, 'dag-pre', 'legacy AfterPlanning retains its known stage despite missing plan phase');
    assert.equal(await navigate(targetFor(0)), true);

    setup([hook({ phase: 'future_unrecognized_stage' })]);
    assert.equal(targetFor(0).target.tab, 'dag-raw');
    assert.equal(await navigate(targetFor(0)), true);
    assert.equal(focusEvents.length, 0, 'unknown placement opens raw evidence without guessing a hook card');
    assert.match(detailHTML, /future_unrecognized_stage/);
    setup([hook({ phase: 'future_unrecognized_stage' })]);
    missingRaw = true;
    assert.equal(await navigate(targetFor(0)), false);
    assert.equal(focusEvents.length, 0);

    for (const outcome of ['missing', 'ambiguous']) {
        setup([original]);
        const target = targetFor(0);
        responseHooks = outcome === 'missing' ? [hook({ sequence: 9 })] : [original, hook({ error: 'Different error but identical invocation identity' })];
        assert.equal(await navigate(target), false);
        assert.equal(focusEvents.length, 0);
        assert.match(detailHTML, outcome === 'missing' ? /unavailable in the latest retained evidence/ : /cannot be distinguished/);
    }

    // Invocation success, effect failures and pipeline action are independent.
    const detailed = hook({
        status: 'succeeded', error: 'FULL_INVOCATION_ERROR_MARKER',
        decision: { failure_policy: 'fail_open', action: 'continue', reason: 'clone_failed' },
        effects: [
            { effect_id: 'first', name: 'Reported action', status: 'failed', summary: 'FULL_EFFECT_SUMMARY_MARKER', error: 'FULL_EFFECT_ERROR_MARKER', data: { detail: 'FULL_EFFECT_DATA_MARKER' } },
            { effect_id: 'second', status: 'succeeded', data: { detail: 'SECOND_EFFECT_DATA_MARKER' } },
        ],
    });
    setup([detailed]);
    const before = JSON.stringify(view.testSnapshot().selected.pipeline_hooks[0]);
    const compact = targetFor(0);
    assert.equal(compact.data.effectCount, 2);
    assert.equal(compact.data.failedEffectCount, 1);
    const popup = view.renderHookPopupSummary(compact.data);
    assert.match(popup, /Invocation succeeded/);
    assert.match(popup, /2 reported effects · 1 reported failure/);
    assert.doesNotMatch(popup, /Invocation failed|business success|FULL_/);
    assert.doesNotMatch(JSON.stringify(compact.data), /FULL_|SECOND_EFFECT_DATA_MARKER/);
    assert.equal(Object.hasOwn(compact.data, 'effects'), false);
    assert.equal(Object.hasOwn(compact.data, 'hookError'), false);
    assert.equal(JSON.stringify(view.testSnapshot().selected.pipeline_hooks[0]), before, 'summary construction preserves raw evidence');
    assert.equal(await navigate(compact), true);
    for (const marker of ['FULL_INVOCATION_ERROR_MARKER', 'FULL_EFFECT_SUMMARY_MARKER', 'FULL_EFFECT_ERROR_MARKER', 'FULL_EFFECT_DATA_MARKER', 'SECOND_EFFECT_DATA_MARKER', 'clone_failed', 'fail_open']) assert.ok(detailHTML.includes(marker), `destination retains ${marker}`);
    assert.equal(JSON.stringify(view.testSnapshot().selected.pipeline_hooks[0]), before);

    for (const interruption of ['new-selection', 'different-tab', 'destroy']) {
        setup([original]);
        const target = targetFor(0), gate = deferred('pre');
        responseDelay = gate;
        const pending = navigate(target);
        await gate.startedPromise;
        responseDelay = null;
        if (interruption === 'new-selection') await view.selectExecution('new-run');
        else if (interruption === 'different-tab') await view.setDagDetailTab('dag-post');
        else view.destroy();
        gate.resolve();
        assert.equal(await pending, false);
        assert.equal(focusEvents.length, 0);
        assert.equal(view.testSnapshot().selected?.request_id ?? null, interruption === 'new-selection' ? 'new-run' : interruption === 'different-tab' ? 'run-one' : null);
        if (interruption === 'different-tab') assert.equal(view.testSnapshot().activeTab, 'dag-post');
    }
    for (const status of [404, 500, 401, 403]) {
        setup([original]);
        const target = targetFor(0);
        responseStatus = status;
        assert.equal(await navigate(target), false);
        assert.equal(focusEvents.length, 0);
        if (status === 401 || status === 403) assert.equal(view.testSnapshot().selected, null);
        else assert.match(detailHTML, /evidence unavailable/);
    }
    console.log('Hook popup summaries and exact retained-card navigation checks passed.');
} finally {
    view.destroy();
    globalThis.setTimeout = originalSetTimeout;
}
