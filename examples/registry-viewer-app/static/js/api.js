/**
 * API Layer with ETag Caching.
 *
 * Wraps fetch() with automatic ETag-based caching for GET requests.
 * On 304 Not Modified: returns cached data without JSON parsing.
 * On 200: caches data + ETag, returns parsed JSON.
 *
 * Combined with the backend ETag middleware (Phase 1a), the 5s polling
 * becomes near-zero-cost when data hasn't changed.
 */

/** @type {Map<string, {data: *, etag: string, timestamp: number}>} */
const cache = new Map();

/**
 * Fetch a JSON API endpoint with ETag caching.
 *
 * @param {string} endpoint - URL path, e.g., '/api/executions?limit=50'
 * @returns {Promise<{data: *, fromCache: boolean}>}
 * @throws {Error} on non-200/304 responses
 */
export async function fetchAPI(endpoint) {
    const cached = cache.get(endpoint);
    const headers = {};
    if (cached?.etag) {
        headers['If-None-Match'] = cached.etag;
    }

    const resp = await fetch(endpoint, { headers });

    if (resp.status === 304 && cached) {
        return { data: cached.data, fromCache: true };
    }

    if (!resp.ok) {
        const text = await resp.text().catch(() => resp.statusText);
        throw new Error(text || `HTTP ${resp.status}`);
    }

    const data = await resp.json();
    const etag = resp.headers.get('ETag');
    if (etag) {
        cache.set(endpoint, { data, etag, timestamp: Date.now() });
    }
    return { data, fromCache: false };
}

/**
 * POST JSON to an API endpoint. No caching.
 *
 * @param {string} endpoint - URL path
 * @param {object} body - request body (serialized as JSON)
 * @returns {Promise<{data: *, ok: boolean}>}
 * @throws {Error} on non-2xx responses
 */
export async function postAPI(endpoint, body) {
    const resp = await fetch(endpoint, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify(body),
    });

    if (!resp.ok) {
        const text = await resp.text().catch(() => resp.statusText);
        let errorMsg = text;
        try {
            errorMsg = JSON.parse(text).error || text;
        } catch { /* use raw text */ }
        throw new Error(errorMsg);
    }

    const data = await resp.json();
    return { data, ok: true };
}

/**
 * Send a JSON control-plane request and retain response metadata such as ETag.
 * Unlike fetchAPI, mutations are never cached.
 */
export async function requestJSON(endpoint, options = {}) {
    const headers = { ...(options.headers || {}) };
    if (options.body !== undefined) headers['Content-Type'] = 'application/json';
    const resp = await fetch(endpoint, {
        method: options.method || 'GET',
        headers,
        body: options.body === undefined ? undefined : JSON.stringify(options.body),
    });
    const text = await resp.text();
    let data = null;
    if (text) {
        try { data = JSON.parse(text); } catch { data = { message: text }; }
    }
    if (!resp.ok) {
        const error = new Error(data?.message || data?.error || `HTTP ${resp.status}`);
        error.status = resp.status;
        error.code = data?.code || '';
        throw error;
    }
    return { data, status: resp.status, etag: resp.headers.get('ETag') || '' };
}

/**
 * Clear cache for a specific endpoint or all endpoints.
 * @param {string} [endpoint] - if omitted, clears all
 */
export function invalidateCache(endpoint) {
    if (endpoint) {
        cache.delete(endpoint);
    } else {
        cache.clear();
    }
}
