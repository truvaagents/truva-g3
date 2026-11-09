// Query resets and grouped matches stay independent from conversation history.
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
globalThis.setTimeout = (callback, delay, ...args) => delay === 50 ? 0 : realSetTimeout(callback, delay, ...args);
const realDateNow = Date.now;
let now = Date.UTC(2026, 10, 6, 12);
Date.now = () => now;
const moduleURL = new URL('../static/js/views/dag.js', import.meta.url);
let source = await readFile(moduleURL, 'utf8');
source = source.replace(/from '(\.\.[^']+)'/g, (_, path) => `from '${new URL(path, moduleURL).href}'`);
source += `\nexport { fetchExecutions, navigateExecutionPage, jumpToExecutionPage, setGroupingMode, toggleConversationGroup, navigateConversationMatches, openConversationTimeline, closeConversationTimeline, navigateConversationPage, selectExecution };
export function testSnapshot() { return { executions, listPageCursor, listPageNumber, listPageCursors: new Map(listPageCursors), groupedNextCursor, groupMatchPages, selected }; }
`;
const view = await import(`data:text/javascript;base64,${Buffer.from(source).toString('base64')}`);
const requests = [];
let failure = false;
let denied = false;
let numberedPages = false;
let changedBoundary = false;
let pageFailureStatus = 0;
const record = id => ({ request_id: id, original_request_id: id, original_request: `Request ${id}`, conversation_id: 'conversation-one', created_at: '2026-11-06T12:00:00Z', lifecycle: { state: 'completed' } });
const matches = ['old-exact-match', ...Array.from({ length: 6 }, (_, index) => `match-${index + 2}`)];
const group = {
    group_key: 'group-one', conversation_id: 'conversation-one', matching_turn_count: matches.length,
    turns: matches.map(id => ({ execution: record(id), related_executions: [] })),
    orphans: ['orphan-a', 'orphan-b'].map(id => ({ ...record(id), original_request_id: 'missing-root', ...(id === 'orphan-b' ? { parent_request_id: 'orphan-a', parent_step_id: 'delegate-step' } : {}) })),
};
globalThis.fetch = async endpoint => {
    const url = new URL(endpoint, 'http://viewer.local');
    requests.push(url);
    if (denied) return { ok: false, status: 403, headers: new Headers(), text: async () => 'access denied' };
    if (failure) return { ok: false, status: url.searchParams.has('cursor') ? 400 : 503, headers: new Headers(), text: async () => 'backend unavailable' };
    let data;
    if (url.pathname === '/api/executions') {
        if (pageFailureStatus) return { ok: false, status: pageFailureStatus, headers: new Headers(), text: async () => 'page unavailable' };
        if (numberedPages) {
            const page = Number(url.searchParams.get('cursor')?.match(/page-(\d+)$/)?.[1] || 1);
            data = { executions: [record(`page-${page}-record`)], next_cursor: page < 105 ? (changedBoundary && page === 1 ? 'changed-page-2' : `page-${page + 1}`) : '' };
        } else data = url.searchParams.get('group_conversations') === 'true'
            ? { groups: [group] }
            : { executions: [record('flat-result')], next_cursor: url.searchParams.has('cursor') ? 'page-3' : 'page-2' };
    } else if (url.pathname === '/api/conversations') {
        assert.equal(url.searchParams.get('limit'), '5');
        data = { conversation_id: 'conversation-one', execution_count: 5, turns: Array.from({ length: 5 }, (_, index) => ({ execution: record(`recent-${index}`), related_executions: [] })), next_cursor: url.searchParams.has('cursor') ? '' : 'older-turns' };
    } else if (url.pathname.endsWith('/unified')) {
        data = { ...record(decodeURIComponent(url.pathname.split('/')[3])), detail_level: 'overview', payloads_deferred: true, plan: { steps: [] }, result: { steps: [] } };
    } else if (url.pathname === '/api/services') data = { services: [] };
    else assert.fail(`Unexpected request: ${url.pathname}`);
    return { ok: true, status: 200, headers: new Headers(), json: async () => data };
};

// Visited page numbers retain only cursor positions, not prior page bodies.
numberedPages = true;
await view.fetchExecutions();
for (let page = 2; page <= 4; page++) await view.navigateExecutionPage('next');
assert.equal(view.testSnapshot().listPageNumber, 4);
assert.deepEqual([...view.testSnapshot().listPageCursors.keys()], [1, 2, 3, 4]);
assert.match(document.getElementById('dagPageNumbers').innerHTML, /data-execution-page="4" aria-label="Page 4" aria-current="page"/);
const visitedWindow = requests.at(-1).searchParams.get('from');
let beforeJump = requests.length;
await view.jumpToExecutionPage(1);
assert.equal(requests.length, beforeJump + 1, 'a page-number jump performs one summary read');
assert.equal(requests.at(-1).searchParams.get('cursor'), null);
assert.equal(requests.at(-1).searchParams.get('from'), visitedWindow, 'jump to page one retains the original time window');
assert.deepEqual([...view.testSnapshot().listPageCursors.keys()], [1, 2, 3, 4], 'backward jumps retain visited forward positions');
await view.jumpToExecutionPage(3);
assert.equal(requests.at(-1).searchParams.get('cursor'), 'page-3');
assert.equal(view.testSnapshot().listPageNumber, 3);
assert.equal(view.testSnapshot().executions[0].request_id, 'page-3-record');
await view.navigateExecutionPage('previous');
assert.equal(view.testSnapshot().listPageNumber, 2);
await view.navigateExecutionPage('next');
assert.equal(view.testSnapshot().listPageNumber, 3);
beforeJump = requests.length;
for (const page of [99, -1, NaN, 1.5, 3]) await view.jumpToExecutionPage(page);
assert.equal(requests.length, beforeJump, 'unvisited, invalid and current page numbers do not dispatch');
pageFailureStatus = 503;
await view.jumpToExecutionPage(1);
assert.equal(view.testSnapshot().listPageNumber, 3, 'failed jumps preserve the current page number');
assert.equal(view.testSnapshot().executions[0].request_id, 'page-3-record');
assert.equal(view.testSnapshot().listPageCursors.size, 4);
pageFailureStatus = 409;
await view.jumpToExecutionPage(1);
assert.equal(view.testSnapshot().listPageCursors.size, 0, 'expired anchors clear all page links');
assert.equal(view.testSnapshot().listPageNumber, 3, 'retained rows keep their displayed page number until refresh');
pageFailureStatus = 0;
await view.refresh();
assert.equal(view.testSnapshot().listPageNumber, 1, 'refresh after cursor expiration starts again at page one');
assert.deepEqual([...view.testSnapshot().listPageCursors.keys()], [1]);
for (let page = 2; page <= 4; page++) await view.navigateExecutionPage('next');
changedBoundary = true;
await view.jumpToExecutionPage(1);
assert.deepEqual([...view.testSnapshot().listPageCursors.keys()], [1], 'changed boundaries discard stale forward page positions');
changedBoundary = false;
await view.fetchExecutions();
for (let page = 2; page <= 105; page++) await view.navigateExecutionPage('next');
assert.equal(view.testSnapshot().listPageNumber, 105, 'page numbers keep advancing beyond the retained history limit');
assert.equal(view.testSnapshot().listPageCursors.size, 100);
assert.equal(Math.min(...view.testSnapshot().listPageCursors.keys()), 6);
assert.ok((document.getElementById('dagPageNumbers').innerHTML.match(/data-execution-page=/g) || []).length <= 5, 'visible page buttons stay compact');
assert.equal((document.getElementById('dagPageJump').innerHTML.match(/<option/g) || []).length, 100, 'the picker exposes the bounded visited history');
await view.jumpToExecutionPage(6);
assert.equal(view.testSnapshot().listPageNumber, 6);
beforeJump = requests.length;
await view.navigateExecutionPage('previous');
assert.equal(requests.length, beforeJump, 'evicted page positions are not guessed');

// A late jump cannot restore the page numbers for an older search.
await view.fetchExecutions();
for (let page = 2; page <= 4; page++) await view.navigateExecutionPage('next');
const ordinaryFetch = globalThis.fetch;
let releaseJump;
const heldJump = new Promise(resolve => { releaseJump = resolve; });
globalThis.fetch = async (...args) => {
    const response = await ordinaryFetch(...args);
    if (new URL(args[0], 'http://viewer.local').searchParams.get('cursor') === 'page-2') await heldJump;
    return response;
};
const lateJump = view.jumpToExecutionPage(2);
await new Promise(resolve => setImmediate(resolve));
document.getElementById('dagSearchInput').value = 'reset-during-jump';
await view.fetchExecutions();
releaseJump();
await lateJump;
assert.equal(view.testSnapshot().listPageNumber, 1);
assert.deepEqual([...view.testSnapshot().listPageCursors.keys()], [1]);
globalThis.fetch = ordinaryFetch;
document.getElementById('dagSearchInput').value = '';
numberedPages = false;

for (const manual of [false, true]) {
    await view.fetchExecutions();
    const firstPage = requests.at(-1);
    assert.equal(firstPage.searchParams.get('cursor'), null);
    now += 10_000;
    failure = true;
    await view.refresh({ manual });
    assert.notEqual(requests.at(-1).searchParams.get('to'), firstPage.searchParams.get('to'), 'refresh proposes a new time window');
    assert.equal(view.testSnapshot().groupedNextCursor, 'page-2', 'failed refresh preserves current page navigation');
    assert.equal(view.testSnapshot().executions[0].request_id, 'flat-result', 'failed refresh preserves current rows');
    failure = false;
    await view.navigateExecutionPage('next');
    const nextPage = requests.at(-1);
    assert.equal(nextPage.searchParams.get('cursor'), 'page-2');
    assert.equal(nextPage.searchParams.get('from'), firstPage.searchParams.get('from'), 'Next retains its cursor’s original lower bound after failed refresh');
    assert.equal(nextPage.searchParams.get('to'), firstPage.searchParams.get('to'), 'Next retains its cursor’s original upper bound after failed refresh');
}
assert.equal(view.testSnapshot().listPageCursor, 'page-2');
assert.equal(view.testSnapshot().listPageNumber, 2);
document.getElementById('dagSearchInput').value = 'new-search';
failure = true;
const pending = view.fetchExecutions();
assert.equal(view.testSnapshot().listPageCursor, '', 'reset discards old cursor before the request completes');
assert.equal(view.testSnapshot().listPageNumber, 1);
assert.equal(view.testSnapshot().groupedNextCursor, '');
assert.equal(view.testSnapshot().executions.length, 0, 'old results are not labelled with a new search');
await pending;
await view.refresh({ manual: true });
assert.equal(requests.at(-1).searchParams.get('q'), 'new-search');
assert.equal(requests.at(-1).searchParams.get('cursor'), null, 'retry after failed reset starts the new query');
failure = false;
await view.fetchExecutions();
await view.navigateExecutionPage('next');
document.getElementById('dagSearchInput').value = 'changed-before-debounce';
await view.refresh();
assert.equal(requests.at(-1).searchParams.get('q'), 'changed-before-debounce');
assert.equal(requests.at(-1).searchParams.get('cursor'), null, 'timer refresh detects query changes before debounce');

document.getElementById('dagSearchInput').value = 'old-exact-match';
view.setGroupingMode('grouped');
await new Promise(resolve => setImmediate(resolve));
requests.length = 0;
view.toggleConversationGroup('group-one');
let html = document.getElementById('dagTableBody').innerHTML;
assert.match(html, /data-request-id="old-exact-match"/, 'the actual matching execution is selectable');
assert.match(html, /data-request-id="match-5"/);
assert.doesNotMatch(html, /data-request-id="match-6"/);
assert.doesNotMatch(html, /data-request-id="recent-/);
assert.equal((html.match(/data-request-id=/g) || []).length, 5, 'expansion shows five current-page matches');
assert.equal(requests.length, 0, 'expansion does not load unrelated history');
view.navigateConversationMatches('next', 'group-one');
html = document.getElementById('dagTableBody').innerHTML;
for (const id of ['match-6', 'match-7', 'orphan-a', 'orphan-b']) assert.match(html, new RegExp(`data-request-id="${id}"`));
assert.match(html, /data-lineage-depth="1"/, 'an orphan keeps its visible immediate parent even when their root is absent');
assert.doesNotMatch(html, /Parent outside loaded records/, 'visible orphan parents are resolved within the matching page');
assert.equal((html.match(/data-request-id=/g) || []).length, 4);
assert.equal(requests.length, 0, 'local matching pagination does not reread records');
view.navigateConversationMatches('previous', 'group-one');
await view.selectExecution('old-exact-match');
assert.equal(view.testSnapshot().selected.request_id, 'old-exact-match');

view.openConversationTimeline('conversation-one');
await new Promise(resolve => setImmediate(resolve));
assert.equal(requests.at(-1).pathname, '/api/conversations', 'history is loaded by its own explicit action');
assert.match(document.getElementById('dagTimelineContent').innerHTML, /Request recent-0/);
await view.navigateConversationPage('next', 'conversation-one');
assert.equal(requests.at(-1).searchParams.get('cursor'), 'older-turns');
view.closeConversationTimeline();
assert.match(document.getElementById('dagTableBody').innerHTML, /data-request-id="old-exact-match"/, 'history never replaces the matching summary rows');
assert.doesNotMatch(document.getElementById('dagTableBody').innerHTML, /data-request-id="recent-/);
await view.refresh();
assert.match(document.getElementById('dagTableBody').innerHTML, /data-request-id="old-exact-match"/, 'summary refresh preserves expanded matching rows');
denied = true;
await view.fetchExecutions();
assert.equal(view.testSnapshot().groupMatchPages.size, 0, 'denial clears matching-page state');
assert.equal(view.testSnapshot().listPageCursors.size, 0, 'denial clears visited execution-page positions');
assert.equal(view.testSnapshot().selected, null);
assert.doesNotMatch(document.getElementById('dagTableBody').innerHTML, /data-request-id="old-exact-match"/);
view.destroy();
assert.equal(view.testSnapshot().listPageCursors.size, 0, 'leaving the view releases visited-page positions');
globalThis.setTimeout = realSetTimeout;
Date.now = realDateNow;
console.log('Grouped search and query reset navigation checks passed.');
