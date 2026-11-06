// Exercise the real views and API helper with an abort-ignoring transport.
// Run: node tests/access-failures.test.mjs
import assert from 'node:assert/strict';
import { readFile } from 'node:fs/promises';
import { invalidateCache } from '../static/js/api.js';

let elements = new Map();
function element() {
    const classes = new Set();
    return {
        _html: '', _text: '', value: '', style: {}, dataset: {}, children: [],
        get innerHTML() { return this._html; }, set innerHTML(value) { this._html = String(value); },
        get textContent() { return this._text; }, set textContent(value) {
            this._text = String(value);
            this._html = this._text.replaceAll('&', '&amp;').replaceAll('<', '&lt;').replaceAll('>', '&gt;').replaceAll('"', '&quot;');
        },
        classList: {
            add(value) { classes.add(value); }, remove(value) { classes.delete(value); },
            contains(value) { return classes.has(value); },
            toggle(value, force) {
                const add = force ?? !classes.has(value);
                if (add) classes.add(value); else classes.delete(value);
            },
        },
        querySelector(selector) {
            return this.children.find(child => selector === `.${child.className}`) || null;
        },
        querySelectorAll() { return []; }, closest() { return null; },
        appendChild(child) { child.parent = this; this.children.push(child); },
        remove() { if (this.parent) this.parent.children = this.parent.children.filter(child => child !== this); },
        addEventListener() {}, removeEventListener() {},
    };
}
const get = id => {
    if (!elements.has(id)) elements.set(id, element());
    return elements.get(id);
};
globalThis.document = {
    getElementById: get, createElement: element,
    querySelector(selector) { return selector === '.refresh-btn' ? get('refreshButton') : null; },
    querySelectorAll() { return []; },
};
function response(data, status = 200) {
    return {
        ok: status >= 200 && status < 300, status, statusText: `HTTP ${status}`,
        headers: { get() { return null; } },
        json: async () => structuredClone(data), text: async () => `Unavailable (${status})`,
    };
}
function deferred() {
    let resolve;
    const promise = new Promise(done => { resolve = done; });
    return { promise, resolve };
}
const tick = () => new Promise(resolve => setImmediate(resolve));
const views = [
    {
        name: 'HITL', file: 'hitl.js', listFunction: 'fetchCheckpoints', selectFunction: 'selectCheckpoint', collection: 'checkpoints',
        base: '/api/hitl/checkpoints', id: 'checkpoint_id', count: 'pendingCount', secondCount: 'criticalCount',
        banner: 'hitlErrorBanner', message: 'hitlErrorMessage', table: 'hitlTableBody', detail: 'hitlDetailContent',
        record(id) { return { checkpoint_id: id, created_at: '2026-11-01T00:00:00Z', expires_at: '2027-01-01T00:00:00Z', priority: 'critical', original_request: 'private-checkpoint', status: 'pending' }; },
    },
    {
        name: 'LLM', file: 'llm-debug.js', listFunction: 'fetchLLMRecords', selectFunction: 'selectLLMRecord', collection: 'records',
        base: '/api/llm-debug', id: 'request_id', count: 'recordCount', secondCount: 'tokenCount',
        banner: 'llmErrorBanner', message: 'llmErrorMessage', table: 'llmTableBody', detail: 'llmDetailContent',
        record(id) { return { request_id: id, created_at: '2026-11-01T00:00:00Z', interactions: [], total_tokens: 7, original_request: 'private-debug' }; },
    },
];
let failed = 0;
async function test(name, run) {
    try { await run(); console.log(`  PASS ${name}`); }
    catch (error) { failed++; console.error(`  FAIL ${name}: ${error.stack}`); }
}

for (const spec of views) {
    const moduleURL = new URL(`../static/js/views/${spec.file}`, import.meta.url);
    let source = await readFile(moduleURL, 'utf8');
    source = source.replace(/from '(\.\.[^']+)'/g, (_, path) => `from '${new URL(path, moduleURL).href}'`);
    source += `\nexport { ${spec.listFunction} as testFetch, ${spec.selectFunction} as testSelect };\n`
        + `export function testInstall(list, value = null) { ${spec.collection} = list; selected = value; activeTab = 'json'; }\n`
        + `export function testState() { return { list: ${spec.collection}, selected }; }`;
    const view = await import(`data:text/javascript;base64,${Buffer.from(source).toString('base64')}`);
    const record = spec.record('existing');
    const listData = values => ({ [spec.collection]: values });
    const reset = () => {
        view.destroy(); elements = new Map(); invalidateCache(); view.testInstall([record], record);
    };
    const assertCleared = () => {
        assert.deepEqual(view.testState(), { list: [], selected: null });
        assert.equal(get(spec.count).textContent, 'Unavailable');
        assert.equal(get(spec.secondCount).textContent, 'Unavailable');
        assert.equal(get(spec.banner).classList.contains('visible'), true);
        assert.doesNotMatch(get(spec.table).innerHTML + get(spec.detail).innerHTML, /private-checkpoint|private-debug|existing/);
    };

    for (const status of [401, 403, 500]) {
        await test(`${spec.name} list ${status} clears real data/counters without sample fallback`, async () => {
            reset(); globalThis.fetch = async () => response(null, status);
            await view.testFetch(); assertCleared();
            assert.match(get(spec.message).textContent, status === 401 ? /sign in/ : status === 403 ? /permission/ : /500/);
        });
        await test(`${spec.name} selected-detail ${status} survives successful outer refresh`, async () => {
            reset(); globalThis.fetch = async endpoint => endpoint === spec.base ? response(listData([record])) : response(null, status);
            await view.testFetch();
            assert.equal(view.testState().list.length, 1);
            assert.equal(view.testState().selected, null);
            assert.equal(get(spec.count).textContent, '1');
            assert.equal(get(spec.banner).classList.contains('visible'), true);
            assert.doesNotMatch(get(spec.detail).innerHTML, /private-checkpoint|private-debug/);
        });
    }
    await test(`${spec.name} denial followed by successful recovery restores counters and clears banner`, async () => {
        reset(); globalThis.fetch = async () => response(null, 403); await view.testFetch();
        globalThis.fetch = async endpoint => response(endpoint === spec.base ? listData([record]) : record);
        await view.testFetch(); await view.testSelect('existing');
        assert.equal(view.testState().selected[spec.id], 'existing');
        assert.equal(get(spec.count).textContent, '1');
        assert.equal(get(spec.secondCount).textContent, spec.name === 'HITL' ? '1' : '7');
        assert.equal(get(spec.banner).classList.contains('visible'), false);
    });
    await test(`${spec.name} late detail cannot restore data after a newer selection is denied`, async () => {
        reset(); const late = deferred();
        globalThis.fetch = async endpoint => endpoint.endsWith('/old') ? late.promise : response(null, 403);
        const pending = view.testSelect('old'); await view.testSelect('denied');
        late.resolve(response(spec.record('old'))); await pending;
        assert.equal(view.testState().selected, null);
        assert.equal(get(spec.banner).classList.contains('visible'), true);
    });
    await test(`${spec.name} list denial cancels an older detail load`, async () => {
        reset(); const late = deferred();
        globalThis.fetch = async endpoint => endpoint === spec.base ? response(null, 403) : late.promise;
        const pending = view.testSelect('old'); await view.testFetch();
        late.resolve(response(spec.record('old'))); await pending; assertCleared();
        if (spec.name === 'LLM') assert.equal(get('llmDebugDetailPanel').querySelector('.loading-overlay'), null, 'cancelled detail overlay is removed');
    });
    await test(`${spec.name} late list cannot restore data after a newer list is denied`, async () => {
        reset(); const late = deferred(); let calls = 0;
        globalThis.fetch = async () => ++calls === 1 ? late.promise : response(null, 403);
        const pending = view.testFetch(); await view.testFetch();
        late.resolve(response(listData([record]))); await pending; assertCleared();
    });
    await test(`${spec.name} destroy rejects late list and detail results even when transport ignores abort`, async () => {
        reset(); const list = deferred(); const detail = deferred();
        globalThis.fetch = async endpoint => endpoint === spec.base ? list.promise : detail.promise;
        const detailPending = view.testSelect('old'); const listPending = view.testFetch(); view.destroy();
        assert.equal(get(spec.name === 'LLM' ? 'llmDebugListPanel' : 'hitlListPanel').querySelector('.loading-overlay'), null, 'destroy removes list loading overlay');
        if (spec.name === 'LLM') assert.equal(get('llmDebugDetailPanel').querySelector('.loading-overlay'), null, 'destroy removes detail loading overlay');
        const before = [...elements].map(([id, el]) => [id, el.innerHTML, el.textContent]);
        detail.resolve(response(spec.record('old'))); list.resolve(response(listData([record])));
        await Promise.all([detailPending, listPending]);
        assert.deepEqual(view.testState(), { list: [], selected: null });
        assert.deepEqual([...elements].map(([id, el]) => [id, el.innerHTML, el.textContent]), before);
    });
    await test(`${spec.name} refreshed list removal invalidates an uncompleted detail selection`, async () => {
        reset(); view.testInstall([record]); const detail = deferred();
        globalThis.fetch = async endpoint => endpoint === spec.base ? response(listData([])) : detail.promise;
        const pending = view.testSelect('existing'); await view.testFetch();
        detail.resolve(response(record)); await pending;
        assert.equal(view.testState().selected, null, 'removed detail must not reappear outside the refreshed list');
    });
    await test(`${spec.name} older list refresh cannot replace a newer user selection`, async () => {
        reset(); const list = deferred(); const detail = deferred(); const newer = spec.record('newer');
        globalThis.fetch = async endpoint => {
            if (endpoint === spec.base) return list.promise;
            if (endpoint.endsWith('/newer')) return detail.promise;
            return response(record);
        };
        const listPending = view.testFetch(); const detailPending = view.testSelect('newer');
        list.resolve(response(listData([record, newer]))); await tick();
        detail.resolve(response(newer)); await Promise.all([listPending, detailPending]);
        assert.equal(view.testState().selected?.[spec.id], 'newer', 'background list refresh must preserve newer selection intent');
    });
}
assert.equal(failed, 0, `${failed} access failure/race checks failed`);
console.log('All access failure and request ownership tests passed.');
