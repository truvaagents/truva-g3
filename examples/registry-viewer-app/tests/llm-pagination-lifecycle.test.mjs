// Pagination must keep working when the persistent view DOM is reinitialized.
// Run: node tests/llm-pagination-lifecycle.test.mjs
import assert from 'node:assert/strict';
import { invalidateCache } from '../static/js/api.js';

const elements = new Map();
let paginationCreated = 0;
function element() {
    const listeners = new Map();
    const classes = new Set();
    return {
        innerHTML: '', value: '', style: {}, dataset: {}, children: [],
        set textContent(value) { this.innerHTML = String(value).replaceAll('&', '&amp;').replaceAll('<', '&lt;').replaceAll('>', '&gt;'); },
        classList: {
            add(value) { classes.add(value); }, remove(value) { classes.delete(value); },
            contains(value) { return classes.has(value); },
            toggle(value, force) { if (force ?? !classes.has(value)) classes.add(value); else classes.delete(value); },
        },
        querySelector(selector) { return this.children.find(child => selector === `.${child.className}`) || null; },
        querySelectorAll() { return []; },
        addEventListener(type, callback) {
            if (!listeners.has(type)) listeners.set(type, new Set());
            listeners.get(type).add(callback);
        },
        removeEventListener(type, callback) { listeners.get(type)?.delete(callback); },
        dispatch(type, target) { for (const callback of listeners.get(type) || []) callback({ target }); },
        insertAdjacentHTML(_position, html) {
            assert.match(html, /id="llmMetadataPagination"/);
            paginationCreated++;
            elements.set('llmMetadataPagination', element());
        },
        appendChild(child) { child.parent = this; this.children.push(child); },
        remove() { if (this.parent) this.parent.children = this.parent.children.filter(child => child !== this); },
    };
}
const get = id => {
    if (id === 'llmMetadataPagination' && !elements.has(id)) return null;
    if (!elements.has(id)) elements.set(id, element());
    return elements.get(id);
};
globalThis.document = {
    getElementById: get, createElement: element,
    querySelector(selector) { return selector === '.refresh-btn' ? get('refreshButton') : selector.startsWith('[data-llm-') ? get(selector) : null; },
    querySelectorAll() { return []; },
};
const tick = () => new Promise(resolve => setImmediate(resolve));
const requests = [];
const summaryRequests = [];
let delayedSummary = null;
let summaryStatus = 200;
let expiredCursor = false;
const record = request_id => ({ request_id, created_at: '2026-11-06T12:00:00Z', summary_pending: true });
globalThis.fetch = async endpoint => {
    const url = new URL(endpoint, 'http://viewer.local');
    if (url.pathname === '/api/llm-debug/summaries') {
        summaryRequests.push(url);
        const ids = url.searchParams.getAll('request_id');
        assert.ok(ids.length <= 5, 'summary batches are bounded');
        if (delayedSummary) return delayedSummary.promise;
        return { ok: summaryStatus === 200, status: summaryStatus, headers: new Headers(), text: async () => 'denied', json: async () => ({ records: ids.map(id => ({ request_id: id, state: 'ready', interaction_count: 3, total_tokens: 123, has_errors: false })) }) };
    }
    assert.equal(url.pathname, '/api/llm-debug');
    assert.equal(url.searchParams.get('limit'), '30');
    requests.push(url);
    if (expiredCursor && url.searchParams.has('cursor')) {
        return { ok: false, status: 409, headers: new Headers(), text: async () => 'browse cursor changed or expired; refresh the list' };
    }
    const older = url.searchParams.get('cursor') === 'page-2';
    const data = {
        records: older ? [record('older-header')] : Array.from({ length: 30 }, (_, index) => record(`recent-header-${index}`)),
        next_cursor: older ? '' : 'page-2',
    };
    return { ok: true, status: 200, headers: new Headers(), json: async () => data };
};
const view = await import('../static/js/views/llm-debug.js');
const panel = get('llmDebugListPanel');
// Mirror app.js ownership of the shared button. The view must not bind a
// second listener that aborts this refresh and duplicates metadata reads.
let sharedRefresh;
get('refreshButton').addEventListener('click', () => { sharedRefresh = view.refresh({ manual: true }); });
async function clickPage(direction) {
    invalidateCache();
    const before = requests.length;
    const selector = `[data-llm-${direction}-page]`;
    panel.dispatch('click', { closest: candidate => candidate === selector ? {} : null });
    await tick();
    assert.equal(requests.length, before + 1, 'one page action issues one metadata request');
}

for (let entry = 0; entry < 3; entry++) {
    invalidateCache();
    view.init(); await tick();
    assert.equal(paginationCreated, 1, 'reentry reuses the existing controls');
    assert.match(get('llmTableBody').innerHTML, /recent-header-29/);
    assert.match(get('llmTableBody').innerHTML, /✓ No Errors/);
    assert.doesNotMatch(get('llmTableBody').innerHTML, /Not loaded|Loading…/);
    assert.equal(summaryRequests.at(-1).searchParams.getAll('request_id').length, 5);
    const beforeRefresh = requests.length;
    get('refreshButton').dispatch('click', { closest() { return null; } });
    await sharedRefresh;
    assert.equal(requests.length, beforeRefresh + 1, 'one shared refresh action issues one owned metadata read');
    await clickPage('next');
    assert.equal(requests.at(-1).searchParams.get('cursor'), 'page-2');
    assert.match(get('llmTableBody').innerHTML, /older-header/);
    assert.doesNotMatch(get('llmTableBody').innerHTML, /recent-header/);
    await clickPage('previous');
    assert.equal(requests.at(-1).searchParams.get('cursor'), null);
    assert.match(get('llmTableBody').innerHTML, /recent-header-29/);
    await clickPage('next');
    expiredCursor = true;
    await view.refresh();
    assert.match(get('llmErrorMessage').innerHTML, /Refresh to restart/);
    assert.equal(get('[data-llm-previous-page]').classList.contains('hidden'), true);
    assert.equal(get('[data-llm-next-page]').classList.contains('hidden'), true);
    expiredCursor = false;
    await view.refresh({ manual: true });
    assert.equal(requests.at(-1).searchParams.get('cursor'), null, 'refresh after expiry restarts at the first page');
    assert.match(get('llmTableBody').innerHTML, /recent-header-29/);
    view.destroy();
    const before = requests.length;
    panel.dispatch('click', { closest: candidate => candidate === '[data-llm-next-page]' ? {} : null });
    await tick();
    assert.equal(requests.length, before, 'destroy removes pagination listeners');
}
// Late summary responses must not restore rows after navigation or destroy.
function deferred() {
    let resolve;
    const promise = new Promise(done => { resolve = done; });
    return { promise, resolve };
}
delayedSummary = deferred();
view.init(); await tick();
assert.match(get('llmTableBody').innerHTML, /Loading…/);
const old = delayedSummary;
delayedSummary = null;
await clickPage('next');
old.resolve({ ok: true, status: 200, headers: new Headers(), json: async () => ({ records: [{ request_id: 'recent-header-0', state: 'ready', interaction_count: 9999, total_tokens: 9999, has_errors: true }] }) });
await tick();
assert.match(get('llmTableBody').innerHTML, /older-header/);
assert.doesNotMatch(get('llmTableBody').innerHTML, /9999|recent-header/);
view.destroy();

delayedSummary = deferred();
view.init(); await tick();
const late = delayedSummary;
view.destroy();
late.resolve({ ok: true, status: 200, headers: new Headers(), json: async () => ({ records: [{ request_id: 'recent-header-0', state: 'ready', interaction_count: 9999, total_tokens: 9999, has_errors: true }] }) });
await tick();
assert.doesNotMatch(get('llmTableBody').innerHTML, /9999/);
delayedSummary = null;

// A denied automatic summary read clears the page and selected data, just
// like a denied list/detail read. It is not rendered as successful zero usage.
summaryStatus = 403;
view.init(); await tick();
assert.match(get('llmErrorMessage').innerHTML, /permission/);
assert.equal(get('recordCount').innerHTML, 'Unavailable');
assert.doesNotMatch(get('llmTableBody').innerHTML, /recent-header/);
view.destroy();
console.log('LLM pagination and automatic summaries survive lifecycle changes.');
