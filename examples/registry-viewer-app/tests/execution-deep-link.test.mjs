// Focused URL bootstrap and default-startup regression checks.
// Run: node tests/execution-deep-link.test.mjs
import assert from 'node:assert/strict';
import { readFile } from 'node:fs/promises';
import { executionDeepLinkRequestID } from '../static/js/utils/dag-graph.js';

for (const search of ['', '?request_id=run-1', '?view=dag', '?view=registry&request_id=run-1',
    '?view=dag&request_id=', '?view=dag&view=dag&request_id=run-1', '?view=dag&request_id=a&request_id=b',
    '?view=dag&request_id=contains+space', '?view=dag&request_id=%0A', '?view=dag&request_id=%E0',
    '?view=dag&request_id=%E4%B8%AD', `?view=dag&request_id=${'a'.repeat(513)}`]) {
    assert.equal(executionDeepLinkRequestID(search), null, `invalid/absent link: ${search}`);
}
const escapedID = 'run:<x>&"?+';
assert.equal(executionDeepLinkRequestID(`?view=dag&request_id=${encodeURIComponent(escapedID)}`), escapedID);
assert.equal(executionDeepLinkRequestID('?view=dag&request_id=run%2520literal'), 'run%20literal', 'decode once only');
assert.equal(executionDeepLinkRequestID(`?view=dag&request_id=${'a'.repeat(512)}`), 'a'.repeat(512));

const calls = [];
globalThis.__deepLinkViews = {};
for (const name of ['registry', 'llmDebug', 'hitl', 'dag', 'skills', 'memory']) {
    globalThis.__deepLinkViews[name] = {
        init() { calls.push(['init', name]); }, destroy() { calls.push(['destroy', name]); },
        openExecution(requestID) { calls.push(['open', requestID]); return Promise.resolve(); },
    };
}
globalThis.document = {
    addEventListener() {}, querySelectorAll() { return []; },
    getElementById() { return { checked: false, classList: { remove() {}, add() {}, toggle() {} } }; },
};
globalThis.history = { state: { preserved: true }, replaceState(_state, _title, url) { globalThis.location = new URL(url); } };
const appURL = new URL('../static/js/app.js', import.meta.url);
let source = await readFile(appURL, 'utf8');
source = source.replace(/^import \* as (\w+) from '[^']+';/gm, (_, name) => `const ${name} = globalThis.__deepLinkViews.${name};`);
source = source.replace(/from '(\.\/[^']+)'/g, (_, path) => `from '${new URL(path, appURL).href}'`);
source += '\nexport { initializeInitialView, switchView };';
const app = await import(`data:text/javascript;base64,${Buffer.from(source).toString('base64')}`);

for (const search of ['', '?view=dag', '?view=dag&request_id=invalid+space']) {
    globalThis.location = new URL(`http://registry.localhost/${search}`);
    calls.length = 0;
    app.initializeInitialView();
    assert.deepEqual(calls.filter(call => call[0] === 'init'), [['init', 'registry']]);
    assert.equal(calls.some(call => call[0] === 'open'), false);
}
globalThis.location = new URL(`http://registry.localhost/?keep=1&view=dag&request_id=${encodeURIComponent(escapedID)}#evidence`);
calls.length = 0;
app.initializeInitialView();
assert.deepEqual(calls.filter(call => call[0] === 'init'), [['init', 'dag']]);
assert.deepEqual(calls.filter(call => call[0] === 'open'), [['open', escapedID]]);
assert.equal(location.searchParams.get('keep'), '1');
app.switchView('registry');
assert.equal(location.searchParams.has('view'), false);
assert.equal(location.searchParams.has('request_id'), false);
assert.equal(location.searchParams.get('keep'), '1');
assert.equal(location.hash, '#evidence');
console.log('All execution deep-link bootstrap tests passed.');
