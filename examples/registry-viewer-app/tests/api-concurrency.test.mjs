import assert from 'node:assert/strict';
import { fetchAPI, invalidateCache, postAPI } from '../static/js/api.js';
import { createLoadGuard } from '../static/js/utils/load-guard.js';

const deferred = () => {
    let resolve, reject;
    const promise = new Promise((done, fail) => { resolve = done; reject = fail; });
    return { promise, resolve, reject };
};
const json = (data, headers = {}) => new Response(JSON.stringify(data), { headers });
const notModified = () => new Response(null, { status: 304 });
const unchanged = async (endpoint, expectedTag, expectedData) => {
    globalThis.fetch = async (_url, options) => {
        assert.equal(options.headers['If-None-Match'], expectedTag);
        return notModified();
    };
    assert.deepEqual(await fetchAPI(endpoint), { data: expectedData, fromCache: true });
};

const cases = [
    ['an aborted superseded load cannot invalidate its successful replacement', async () => {
        const endpoint = '/review/superseded';
        const guard = createLoadGuard();
        const replacement = deferred();
        let calls = 0;
        globalThis.fetch = (_url, { signal }) => {
            if (++calls === 1) {
                return new Promise((_resolve, reject) => {
                    signal.addEventListener('abort', () => reject(new DOMException('Superseded', 'AbortError')), { once: true });
                });
            }
            return replacement.promise;
        };
        const oldTicket = guard.begin();
        const oldRejected = assert.rejects(fetchAPI(endpoint, { signal: oldTicket.signal }), error => error.name === 'AbortError');
        const ticket = guard.begin();
        const current = fetchAPI(endpoint, { signal: ticket.signal });
        await oldRejected;
        replacement.resolve(json({ current: true }, { ETag: '"current"' }));
        assert.deepEqual(await current, { data: { current: true }, fromCache: false });
        assert.equal(calls, 2, 'supersession must not replay either request');
        await unchanged(endpoint, '"current"', { current: true });
    }],
    ...['network rejection', 'HTTP 500'].map(failure => [`an old ${failure} after explicit invalidation cannot evict a newer cache entry`, async () => {
        const endpoint = '/review/stale-rejection';
        const late = deferred();
        globalThis.fetch = () => late.promise;
        const oldRejected = assert.rejects(fetchAPI(endpoint));
        invalidateCache(endpoint);
        globalThis.fetch = async () => json({ current: true }, { ETag: '"fresh"' });
        await fetchAPI(endpoint);
        if (failure === 'network rejection') late.reject(new TypeError('Old connection failed'));
        else late.resolve(new Response('Old unavailable response', { status: 500 }));
        await oldRejected;
        await unchanged(endpoint, '"fresh"', { current: true });
    }]),
    ['two concurrent no-store successes do not invalidate each other', async () => {
        const endpoint = '/review/no-store';
        const responses = [deferred(), deferred()];
        let calls = 0;
        globalThis.fetch = () => responses[calls++].promise;
        const old = fetchAPI(endpoint);
        const current = fetchAPI(endpoint);
        responses[0].resolve(json({ older: true }, { ETag: '"old"', 'Cache-Control': 'private, no-store' }));
        assert.deepEqual(await old, { data: { older: true }, fromCache: false });
        responses[1].resolve(json({ current: true }, { ETag: '"new"', 'Cache-Control': 'private, NO-STORE' }));
        assert.deepEqual(await current, { data: { current: true }, fromCache: false });
        assert.equal(calls, 2);
        globalThis.fetch = async (_url, options) => {
            assert.equal(options.headers['If-None-Match'], undefined, 'no-store success must leave no reusable cache entry');
            return json({ refreshed: true });
        };
        await fetchAPI(endpoint);
    }],
    ['no-store eviction prevents an already-pending 304 from reusing its captured entry', async () => {
        const endpoint = '/review/evicted-304';
        globalThis.fetch = async () => json({ private: 'old' }, { ETag: '"old"' });
        await fetchAPI(endpoint);
        const late = deferred();
        globalThis.fetch = () => late.promise;
        const rejected = assert.rejects(fetchAPI(endpoint));
        globalThis.fetch = async () => json({ transient: true }, { 'Cache-Control': 'no-store' });
        assert.deepEqual(await fetchAPI(endpoint), { data: { transient: true }, fromCache: false });
        late.resolve(notModified());
        await rejected;
        globalThis.fetch = async (_url, options) => {
            assert.equal(options.headers['If-None-Match'], undefined);
            return json({ fresh: true });
        };
        await fetchAPI(endpoint);
    }],
    ['a failed mutation is never replayed by cache recovery', async () => {
        let writes = 0;
        globalThis.fetch = async (_url, options) => {
            writes++;
            assert.equal(options.method, 'POST');
            return new Response('{"error":"Unavailable"}', { status: 503 });
        };
        await assert.rejects(postAPI('/review/write', {}), error => error.status === 503);
        assert.equal(writes, 1);
    }],
];

let failures = 0;
for (const [name, run] of cases) {
    invalidateCache();
    try { await run(); console.log(`  PASS ${name}`); }
    catch (error) { failures++; console.error(`  FAIL ${name}: ${error.stack}`); }
}
assert.equal(failures, 0, `${failures} API concurrency regressions`);
console.log('API concurrent invalidation tests passed.');
