import { escapeHtml } from './format.js';

// A passed deadline does not mean the expiry worker has changed the stored
// checkpoint status yet. Keep the deadline and lifecycle status distinct.
export function formatCheckpointExpiry(expiresAt, now = Date.now()) {
    if (!expiresAt) return 'Unknown';
    const deadline = new Date(expiresAt).getTime();
    if (!Number.isFinite(deadline)) return 'Unknown';
    const seconds = Math.ceil((deadline - now) / 1000);
    if (seconds <= 0) return 'Deadline passed';
    if (seconds < 60) return `in ${seconds}s`;
    if (seconds < 3600) return `in ${Math.ceil(seconds / 60)}m`;
    if (seconds < 86400) return `in ${Math.ceil(seconds / 3600)}h`;
    return `in ${Math.ceil(seconds / 86400)}d`;
}

export function renderCheckpointContext(context) {
    return Object.entries(context).map(([key, value]) => {
        const text = value !== null && typeof value === 'object'
            ? JSON.stringify(value, null, 2)
            : String(value);
        return `<div class="info-label">${escapeHtml(key)}</div><div class="info-value mono" style="white-space: pre-wrap;">${escapeHtml(text)}</div>`;
    }).join('');
}
