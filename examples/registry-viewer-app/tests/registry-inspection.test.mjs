import assert from 'node:assert/strict';
import { readFile } from 'node:fs/promises';
import { invalidateCache } from '../static/js/api.js';
const elements = new Map();
function element() {
    const classes = new Set();
    return {
        value: '', innerHTML: '', dataset: {}, style: {}, disabled: false,
        set textContent(text) { this._text = String(text); this.innerHTML = String(text).replaceAll('&', '&amp;').replaceAll('<', '&lt;').replaceAll('>', '&gt;'); },
        get textContent() { return this._text || ''; },
        classList: { add(v) { classes.add(v); }, remove(v) { classes.delete(v); }, contains(v) { return classes.has(v); }, toggle() {} },
        querySelector() { return null; }, querySelectorAll() { return []; }, closest() { return null; },
        appendChild() {}, remove() {}, addEventListener() {}, removeEventListener() {},
    };
}
const get = id => { if (!elements.has(id)) elements.set(id, element()); return elements.get(id); };
globalThis.document = {
    activeElement: null, createElement: element, getElementById: get,
    querySelector: () => get('refresh'), querySelectorAll: () => [],
};
const moduleURL = new URL('../static/js/views/registry.js', import.meta.url);
let source = await readFile(moduleURL, 'utf8');
source = source.replace(/from '(\.\.[^']+)'/g, (_, path) => `from '${new URL(path, moduleURL).href}'`);
source += '\nexport { fetchServices, selectService, inspectApplicationRecords }; export function state() { return { selected, inspectedRecord, applicationRecordsAvailable }; }';
const view = await import(`data:text/javascript;base64,${Buffer.from(source).toString('base64')}`);
const service = { id: 'service-1', name: 'Inspector', type: 'tool', capabilities: [{ name: 'inspect_application_records_v1', internal: true }] };
const catalog = supported => ({ services: [service], totalCount: 1, agentCount: 0, toolCount: 1, timestamp: '2026-11-04T00:00:00Z', ...(supported === undefined ? {} : { viewer_capabilities: { application_records: supported } }) });
const response = (data, status = 200) => new Response(JSON.stringify(data), { status, headers: { 'Content-Type': 'application/json' } });
const deferred = () => { let resolve; const promise = new Promise(r => { resolve = r; }); return { promise, resolve }; };
const panel = { dataset: { serviceId: 'service-1' }, querySelector: selector => get(selector.slice(1)) };
const button = { disabled: false, closest: () => panel };
const records = { success: true, data: { schema_version: 1, records: [{ id: 'visible-result' }] } };
async function load(supported) {
    invalidateCache(); globalThis.fetch = async () => response(catalog(supported));
    await view.fetchServices(); view.selectService('service-1');
    get('applicationRecordInput').value = '{}';
}
await load(undefined);
assert.equal(view.state().applicationRecordsAvailable, true, 'older ordinary catalogue remains compatible');
assert.match(get('detailContent').innerHTML, /Component read credential/);
get('applicationRecordCredential').value = 'component-only';
let writes = 0;
globalThis.fetch = async (url, options) => {
    writes++; assert.equal(url, '/api/application-records'); assert.equal(options.headers.Authorization, 'Bearer component-only');
    assert.equal(get('applicationRecordCredential').value, '', 'credential clears before request awaits');
    return response(records);
};
await view.inspectApplicationRecords(button);
assert.equal(writes, 1); assert.equal(view.state().inspectedRecord.data.records[0].id, 'visible-result');

// A focused old form cannot prevent protected-mode removal and credential clearing.
get('applicationRecordCredential').value = 'old-secret';
document.activeElement = { closest: () => panel };
get('detailContent').querySelector = () => panel;
await load(false);
assert.equal(get('applicationRecordCredential').value, '');
assert.equal(view.state().inspectedRecord, null);
assert.doesNotMatch(get('detailContent').innerHTML, /type="password"|inspect-application-records/);
assert.match(get('detailContent').innerHTML, /unavailable/);
globalThis.fetch = () => { throw new Error('protected inspection must not send a request'); };
await view.inspectApplicationRecords({ closest() { throw new Error('must check availability before reading form'); } });

// An abort-ignoring response cannot restore records after the capability changes.
document.activeElement = null;
await load(true); get('applicationRecordCredential').value = 'one-request';
const pending = deferred(); globalThis.fetch = () => pending.promise;
const read = view.inspectApplicationRecords(button);
globalThis.fetch = async () => response(catalog(false)); await view.fetchServices();
pending.resolve(response(records)); await read;
assert.equal(view.state().inspectedRecord, null);
assert.equal(get('applicationRecordResult').textContent, '');

// A late older catalogue cannot re-enable the form after a newer protected result.
const oldCatalog = deferred(); globalThis.fetch = () => oldCatalog.promise;
const old = view.fetchServices();
globalThis.fetch = async () => response(catalog(false)); await view.fetchServices();
oldCatalog.resolve(response(catalog(true))); await old;
assert.equal(view.state().applicationRecordsAvailable, false);

await load(true); get('applicationRecordCredential').value = 'erase-on-failure';
globalThis.fetch = async () => response({}, 403);
const originalError = console.error; console.error = () => {};
try { await view.fetchServices(); } finally { console.error = originalError; }
assert.equal(view.state().applicationRecordsAvailable, false);
assert.equal(view.state().selected, null);
assert.equal(get('applicationRecordCredential').value, '');

await load(true); get('applicationRecordCredential').value = 'erase-on-selection';
const changing = deferred(); globalThis.fetch = () => changing.promise;
const changingRead = view.inspectApplicationRecords(button);
view.selectService('missing'); changing.resolve(response(records)); await changingRead;
assert.equal(view.state().inspectedRecord, null);
await load(true); get('applicationRecordCredential').value = 'erase-on-destroy';
const closing = deferred(); globalThis.fetch = () => closing.promise;
const closingRead = view.inspectApplicationRecords(button);
view.destroy(); closing.resolve(response(records)); await closingRead;
assert.equal(view.state().inspectedRecord, null);
assert.equal(get('applicationRecordCredential').value, '');
console.log('Registry capability, credential and late-response tests passed.');
