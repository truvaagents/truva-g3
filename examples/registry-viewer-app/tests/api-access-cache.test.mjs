import assert from 'node:assert/strict';
import { fetchAPI, invalidateCache, postAPI, requestJSON } from '../static/js/api.js';
const json = (data, status = 200, headers = {}) => new Response(JSON.stringify(data), { status, headers });
const deferred = () => { let resolve; const promise = new Promise(r => { resolve = r; }); return { promise, resolve }; };
const rejectCode = code => error => error.code === code;

invalidateCache();
globalThis.fetch = async () => json({ value: 1 }, 200, { ETag: '"v1"' });
await fetchAPI('/data');
globalThis.fetch = async (_url, options) => {
    assert.equal(options.headers['If-None-Match'], '"v1"');
    return new Response(null, { status: 304 });
};
assert.deepEqual(await fetchAPI('/data'), { data: { value: 1 }, fromCache: true });

for (const status of [401, 403]) {
    for (const helper of [() => fetchAPI('/denied'), () => postAPI('/denied', {}), () => requestJSON('/denied')]) {
        globalThis.fetch = async () => json({ value: 1 }, 200, { ETag: '"v1"' });
        await fetchAPI('/data');
        const pending = deferred();
        globalThis.fetch = () => pending.promise;
        const stale = fetchAPI('/data');
        globalThis.fetch = async () => json({ message: 'denied', code: 'DENIED' }, status);
        await assert.rejects(helper(), error => error.status === status);
        pending.resolve(new Response(null, { status: 304 }));
        await assert.rejects(stale, rejectCode('REFRESH_REQUIRED'));
        globalThis.fetch = async (_url, options) => { assert.equal(options.headers['If-None-Match'], undefined); return json({ fresh: true }); };
        await fetchAPI('/data');
    }
}
for (const staleStatus of [200, 304]) {
    globalThis.fetch = async () => json({ old: true }, 200, { ETag: '"old"' });
    await fetchAPI('/data');
    const pending = deferred(); globalThis.fetch = () => pending.promise;
    const stale = fetchAPI('/data');
    invalidateCache('/data');
    pending.resolve(staleStatus === 304 ? new Response(null, { status: 304 }) : json({ obsolete: true }, 200, { ETag: '"obsolete"' }));
    await assert.rejects(stale, rejectCode('REFRESH_REQUIRED'));
}
// Invalidation while JSON is being parsed must also defeat a late successful body.
const body = deferred();
globalThis.fetch = async () => ({ ok: true, status: 200, headers: new Headers({ ETag: '"late"' }), json: () => body.promise });
const parsing = fetchAPI('/data');
await Promise.resolve(); invalidateCache(); body.resolve({ late: true });
await assert.rejects(parsing, rejectCode('REFRESH_REQUIRED'));

for (const control of ['private, NO-STORE', 'no-store']) {
    globalThis.fetch = async () => json({ transient: true }, 200, { ETag: '"uncacheable"', 'Cache-Control': control });
    await fetchAPI('/data');
    globalThis.fetch = async (_url, options) => { assert.equal(options.headers['If-None-Match'], undefined); return json({ fresh: true }); };
    await fetchAPI('/data');
}
globalThis.fetch = async () => new Response(null, { status: 304, headers: { 'Cache-Control': 'no-store' } });
await assert.rejects(fetchAPI('/data'), error => error.status === 304);

for (const [headers, expected] of [
    [{ ETag: '"standard"', 'X-TruvaG3-Skill-ETag': '"mirror"' }, '"standard"'],
    [{ 'X-TruvaG3-Skill-ETag': '"mirror"' }, '"mirror"'], [{}, ''],
]) {
    globalThis.fetch = async () => json({}, 200, headers);
    assert.equal((await requestJSON('/skills')).etag, expected);
}
globalThis.fetch = async () => json({ message: 'conflict', code: 'REVISION_CONFLICT' }, 409);
await assert.rejects(requestJSON('/skills', { method: 'PUT', body: {} }), error => error.status === 409 && error.code === 'REVISION_CONFLICT');
console.log('API cache/access/revision tests passed.');

// Long browsing sessions retain a bounded recent working set of responses.
invalidateCache();
globalThis.fetch = async url => json({ url }, 200, { ETag: '"bounded"' });
for (let index = 0; index < 40; index++) await fetchAPI(`/bounded/${index}`);
await fetchAPI('/bounded/0'); // Touch the oldest entry before inserting another.
await fetchAPI('/bounded/40');
globalThis.fetch = async (_url, options) => {
    assert.equal(options.headers['If-None-Match'], undefined, 'least recently used entry must be evicted');
    return json({ reloaded: true });
};
await fetchAPI('/bounded/1');
globalThis.fetch = async (_url, options) => {
    assert.equal(options.headers['If-None-Match'], '"bounded"', 'recently used entry should remain');
    return new Response(null, { status: 304 });
};
await fetchAPI('/bounded/0');

invalidateCache();
globalThis.fetch = async () => json({ text: 'x'.repeat(450 * 1024) }, 200, { ETag: '"medium"' });
for (let index = 0; index < 10; index++) await fetchAPI(`/byte-budget/${index}`);
globalThis.fetch = async (_url, options) => {
    assert.equal(options.headers['If-None-Match'], undefined, 'total serialized-byte budget must evict old entries');
    return json({ reloaded: true });
};
await fetchAPI('/byte-budget/0');

invalidateCache();
globalThis.fetch = async () => json({ text: 'x'.repeat(600 * 1024) }, 200, { ETag: '"large"' });
await fetchAPI('/large-detail');
globalThis.fetch = async (_url, options) => {
    assert.equal(options.headers['If-None-Match'], undefined, 'large detail bodies should not enter the shared cache');
    return json({ reloaded: true });
};
await fetchAPI('/large-detail');

invalidateCache();
const realNow = Date.now;
let clock = realNow();
try {
    Date.now = () => clock;
    globalThis.fetch = async () => json({ value: 'expires' }, 200, { ETag: '"timed"' });
    await fetchAPI('/expires');
    clock += 60_001;
    globalThis.fetch = async (_url, options) => {
        assert.equal(options.headers['If-None-Match'], undefined, 'expired entry must not be reused');
        return json({ fresh: true });
    };
    await fetchAPI('/expires');
} finally {
    Date.now = realNow;
}

invalidateCache();
globalThis.fetch = async () => json({ value: 'old' }, 200, { ETag: '"old"' });
await fetchAPI('/on-demand');
globalThis.fetch = async (_url, options) => {
    assert.equal(options.headers['If-None-Match'], undefined);
    assert.equal(options.cache, 'no-store');
    return json({ value: 'requested detail' }, 200, { ETag: '"new"' });
};
await fetchAPI('/on-demand', { cache: false });
globalThis.fetch = async (_url, options) => {
    assert.equal(options.headers['If-None-Match'], undefined, 'explicit no-cache request leaves no reusable body');
    return json({ fresh: true });
};
await fetchAPI('/on-demand');

invalidateCache();
const beforeGenerationReset = deferred();
globalThis.fetch = () => beforeGenerationReset.promise;
const invalidated = assert.rejects(fetchAPI('/generation-reset'), rejectCode('REFRESH_REQUIRED'));
for (let index = 0; index < 161; index++) invalidateCache(`/invalidated/${index}`);
beforeGenerationReset.resolve(json({ old: true }, 200, { ETag: '"old"' }));
await invalidated;
console.log('Bounded cache and on-demand bypass tests passed.');
