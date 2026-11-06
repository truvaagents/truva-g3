/** JSON API helpers. ETags save transfer bytes; endpoint handlers still run. */
const cache = new Map();
const endpointGenerations = new Map();
let accessGeneration = 0;

function generation(endpoint) {
    return `${accessGeneration}:${endpointGenerations.get(endpoint) || 0}`;
}

function accessError(status, fallback) {
    const error = new Error(status === 403
        ? 'Your account does not have permission to view or change this data.'
        : status === 401 ? 'Please sign in again to access this data.' : fallback);
    error.status = status;
    return error;
}

function assertCurrent(endpoint, expected, signal) {
    if (signal?.aborted) throw new DOMException('Request cancelled.', 'AbortError');
    if (generation(endpoint) !== expected) {
        const error = new Error('Data access changed while loading. Refresh to try again.');
        error.code = 'REFRESH_REQUIRED';
        throw error;
    }
}

function noStore(response) {
    return (response.headers.get('Cache-Control') || '').split(',')
        .some(part => part.trim().split('=')[0].trim().toLowerCase() === 'no-store');
}

/** @returns {Promise<{data: *, fromCache: boolean}>} */
export async function fetchAPI(endpoint, options = {}) {
    const cached = cache.get(endpoint);
    const expected = generation(endpoint);
    const headers = {};
    if (cached?.etag) headers['If-None-Match'] = cached.etag;
    let resp;
    try {
        resp = await fetch(endpoint, { headers, signal: options.signal });
    } catch (error) {
        // Superseding a load must not invalidate the new request that replaced it.
        if (!options.signal?.aborted && error.name !== 'AbortError' && generation(endpoint) === expected) cache.delete(endpoint);
        throw error;
    }
    assertCurrent(endpoint, expected, options.signal);
    if (!resp.ok && resp.status !== 304) {
        if (resp.status === 401 || resp.status === 403) invalidateCache();
        else cache.delete(endpoint);
        const text = await resp.text().catch(() => resp.statusText);
        throw accessError(resp.status, text || `HTTP ${resp.status}`);
    }
    const uncacheable = noStore(resp);
    if (uncacheable) {
        cache.delete(endpoint);
    }
    if (resp.status === 304) {
        if (cached && cache.get(endpoint) === cached && !uncacheable) return { data: cached.data, fromCache: true };
        throw accessError(304, 'Cached data is unavailable. Refresh to try again.');
    }
    let data;
    try {
        data = await resp.json();
    } catch (error) {
        if (!options.signal?.aborted && generation(endpoint) === expected) cache.delete(endpoint);
        throw error;
    }
    assertCurrent(endpoint, expected, options.signal);
    const etag = resp.headers.get('ETag');
    if (etag && !uncacheable) cache.set(endpoint, { data, etag });
    else cache.delete(endpoint);
    return { data, fromCache: false };
}

/** POST responses are never cached or automatically replayed. */
export async function postAPI(endpoint, body) {
    const { data } = await requestJSON(endpoint, { method: 'POST', body });
    return { data, ok: true };
}

/** Retain response metadata needed for semantic conditional writes. */
export async function requestJSON(endpoint, options = {}) {
    const headers = { ...(options.headers || {}) };
    if (options.body !== undefined) headers['Content-Type'] = 'application/json';
    const resp = await fetch(endpoint, {
        method: options.method || 'GET', headers, signal: options.signal,
        body: options.body === undefined ? undefined : JSON.stringify(options.body),
    });
    // Invalidate as soon as denial is observed, before awaiting its body.
    if (resp.status === 401 || resp.status === 403) invalidateCache();
    const text = await resp.text();
    let data = null;
    if (text) {
        try { data = JSON.parse(text); } catch { data = { message: text }; }
    }
    if (!resp.ok) {
        const error = accessError(resp.status, data?.message || data?.error || `HTTP ${resp.status}`);
        error.code = data?.code || '';
        throw error;
    }
    return { data, status: resp.status, etag: resp.headers.get('ETag') || resp.headers.get('X-TruvaG3-Skill-ETag') || '' };
}

export function invalidateCache(endpoint) {
    if (endpoint) {
        endpointGenerations.set(endpoint, (endpointGenerations.get(endpoint) || 0) + 1);
        cache.delete(endpoint);
    } else {
        accessGeneration++;
        endpointGenerations.clear();
        cache.clear();
    }
}
