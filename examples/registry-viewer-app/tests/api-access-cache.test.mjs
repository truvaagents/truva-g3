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
