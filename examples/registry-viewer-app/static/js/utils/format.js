/**
 * Formatting utilities.
 *
 * Pure functions with no dependencies — extracted from index.html.
 * Deduplicates escapeHtml (was defined twice) and consolidates
 * JSON formatting (formatJsonResponse + formatResponseJson → formatJson).
 */

/**
 * Format a date as a relative time string (e.g., "3m ago", "2h ago").
 * @param {string|Date} date
 * @returns {string}
 */
export function formatTimeAgo(date) {
    const seconds = Math.floor((new Date() - new Date(date)) / 1000);
    if (seconds < 5) return 'just now';
    if (seconds < 60) return `${seconds}s ago`;
    const minutes = Math.floor(seconds / 60);
    if (minutes < 60) return `${minutes}m ago`;
    const hours = Math.floor(minutes / 60);
    if (hours < 24) return `${hours}h ago`;
    return `${Math.floor(hours / 24)}d ago`;
}

/**
 * Format a date as a locale string.
 * @param {string|Date} date
 * @returns {string}
 */
export function formatDateTime(date) {
    return new Date(date).toLocaleString();
}

/**
 * Format a duration in milliseconds as a human-readable string.
 * @param {number} ms
 * @returns {string}
 */
export function formatDuration(ms) {
    if (ms < 1000) return `${ms}ms`;
    return `${(ms / 1000).toFixed(2)}s`;
}

/**
 * Format a token count with K suffix for large numbers.
 * @param {number} count
 * @returns {string}
 */
export function formatTokens(count) {
    if (count >= 1000) return `${(count / 1000).toFixed(1)}k`;
    return count.toString();
}

/**
 * Format byte count with KB/MB suffixes.
 * @param {number} bytes
 * @returns {string}
 */
export function formatBytes(bytes) {
    if (bytes >= 1024 * 1024) return (bytes / (1024 * 1024)).toFixed(1) + 'MB';
    if (bytes >= 1024) return (bytes / 1024).toFixed(1) + 'KB';
    return bytes + 'B';
}

/**
 * Truncate text to a maximum number of words, appending "..." if truncated.
 * @param {string} text
 * @param {number} maxWords
 * @returns {string}
 */
export function truncateInstruction(text, maxWords = 8) {
    if (!text) return 'No instruction';
    const words = text.split(/\s+/);
    if (words.length <= maxWords) return text;
    return words.slice(0, maxWords).join(' ') + '...';
}

/**
 * Truncate text to a maximum character length, appending "..." if truncated.
 * @param {string} text
 * @param {number} maxLength
 * @returns {string}
 */
export function truncateText(text, maxLength = 100) {
    if (!text || text.length <= maxLength) return text;
    return text.substring(0, maxLength) + '...';
}

/**
 * Escape HTML special characters for safe display.
 * Uses DOM textContent → innerHTML for correct escaping.
 * (Was defined twice in index.html — deduplicated here.)
 * @param {string} text
 * @returns {string}
 */
export function escapeHtml(text) {
    if (!text) return '';
    const div = document.createElement('div');
    div.textContent = text;
    return div.innerHTML;
}

/**
 * Syntax-highlight a JSON value with CSS class spans.
 * Expects json-key, json-string, json-number, json-boolean, json-null
 * CSS classes to be defined in layout.css.
 * @param {string|object} json
 * @returns {string} HTML string with syntax highlighting spans
 */
export function syntaxHighlightJson(json) {
    if (typeof json !== 'string') json = JSON.stringify(json, null, 2);
    json = json.trim();
    // HTML-escape < > & so raw HTML in JSON values doesn't break the DOM
    json = json.replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;');
    return json.replace(
        /("(\\u[a-zA-Z0-9]{4}|\\[^u]|[^\\"])*"(\s*:)?|\b(true|false|null)\b|-?\d+(?:\.\d*)?(?:[eE][+-]?\d+)?)/g,
        function (match) {
            let cls = 'json-number';
            if (/^"/.test(match)) {
                if (/:$/.test(match)) {
                    cls = 'json-key';
                    match = match.slice(0, -1) + '</span>:';
                    return '<span class="' + cls + '">' + match;
                } else {
                    cls = 'json-string';
                }
            } else if (/true|false/.test(match)) {
                cls = 'json-boolean';
            } else if (/null/.test(match)) {
                cls = 'json-null';
            }
            return '<span class="' + cls + '">' + match + '</span>';
        }
    );
}

/**
 * Parse and format a JSON response as a plain string (no HTML wrapping).
 * Used by the LLM debug JSON tab for copy-friendly display.
 *
 * @param {string|object} response
 * @returns {string} pretty-printed JSON string, or original text if not JSON
 */
export function formatJsonResponse(response) {
    if (!response) return '';
    try {
        const parsed = typeof response === 'string' ? JSON.parse(response) : response;
        return JSON.stringify(parsed, null, 2);
    } catch (e) {
        return typeof response === 'string' ? response : JSON.stringify(response);
    }
}

/**
 * Parse and format a JSON response for display as HTML.
 * Handles string responses (including markdown-fenced JSON), objects, and
 * non-JSON text. Returns HTML with syntax highlighting wrapped in <pre>.
 *
 * @param {string|object} response
 * @returns {string} HTML string
 */
export function formatResponseJson(response) {
    if (!response) return '';
    try {
        let data = response;
        if (typeof response === 'string') {
            // Strip markdown code fences if present (e.g., ```json ... ```)
            let cleaned = response.trim();
            if (cleaned.startsWith('```')) {
                cleaned = cleaned.replace(/^```(?:json)?\s*\n?/, '');
                cleaned = cleaned.replace(/\n?```\s*$/, '');
                cleaned = cleaned.trim();
            }
            try {
                data = JSON.parse(cleaned);
            } catch {
                // Not JSON — return escaped text in <pre>
                const escaped = response.replace(/</g, '&lt;').replace(/>/g, '&gt;');
                return `<pre style="margin: 0; white-space: pre-wrap; word-break: break-word; text-align: left;">${escaped}</pre>`;
            }
        }
        const highlighted = syntaxHighlightJson(data);
        return `<pre style="margin: 0; white-space: pre-wrap; word-break: break-word; text-align: left;">${highlighted}</pre>`;
    } catch (e) {
        const escaped = String(response).replace(/</g, '&lt;').replace(/>/g, '&gt;');
        return `<pre style="margin: 0; white-space: pre-wrap; word-break: break-word; text-align: left;">${escaped}</pre>`;
    }
}

/**
 * Format conversation request text with highlighted labels.
 * Styles "Previous conversation:" and "Current request:" with glassmorphic badges.
 * @param {string} text
 * @returns {string} HTML string
 */
export function formatConversationRequest(text) {
    if (!text) return '';

    const prevStyle = 'display: inline-block; padding: 1px 5px; margin-right: 3px; ' +
        'background: linear-gradient(135deg, rgba(99, 110, 143, 0.3) 0%, rgba(99, 110, 143, 0.15) 100%); ' +
        'border: 1px solid rgba(99, 110, 143, 0.5); border-radius: 3px; ' +
        'font-size: 10px; font-weight: 600; color: #8b95b3; letter-spacing: 0.2px; vertical-align: middle;';

    const currStyle = 'display: inline-block; padding: 1px 5px; margin-right: 3px; ' +
        'background: linear-gradient(135deg, rgba(0, 210, 211, 0.25) 0%, rgba(0, 210, 211, 0.12) 100%); ' +
        'border: 1px solid rgba(0, 210, 211, 0.5); border-radius: 3px; ' +
        'font-size: 10px; font-weight: 600; color: #00d2d3; letter-spacing: 0.2px; vertical-align: middle;';

    let escaped = escapeHtml(text);
    escaped = escaped.replace(
        /Previous conversation:/g,
        '<span style="' + prevStyle + '">Previous conversation:</span>'
    );
    escaped = escaped.replace(
        /Current request:/g,
        '<span style="' + currStyle + '">Current request:</span>'
    );
    return escaped;
}

/**
 * Copy text to clipboard with visual feedback on the clicked button.
 * @param {string} text
 * @param {Event|HTMLElement} evt
 */
/**
 * execCommand('copy') fallback for environments where navigator.clipboard
 * is unavailable or its Promise rejects (insecure context, permission denied).
 */
function execCopyFallback(text) {
    const textarea = document.createElement('textarea');
    textarea.value = text;
    textarea.style.cssText = 'position:fixed;left:-9999px;top:-9999px;opacity:0';
    document.body.appendChild(textarea);
    textarea.focus();
    textarea.select();
    let ok = false;
    try { ok = document.execCommand('copy'); } catch (_) { /* swallow */ }
    document.body.removeChild(textarea);
    return ok ? Promise.resolve() : Promise.reject(new Error('execCommand copy failed'));
}

/**
 * Write text to the clipboard. Tries Clipboard API first; on any failure
 * (permissions, insecure context, browser quirks) falls back to execCommand.
 */
function writeClipboardUtil(text) {
    if (navigator.clipboard) {
        return navigator.clipboard.writeText(text).catch(() => execCopyFallback(text));
    }
    return execCopyFallback(text);
}

export function copyToClipboard(text, evt) {
    writeClipboardUtil(text).then(() => {
        const btn = evt?.target || evt;
        if (!btn) return;
        const originalText = btn.textContent;
        btn.textContent = 'Copied!';
        btn.classList.add('copied');
        setTimeout(() => {
            btn.textContent = originalText;
            btn.classList.remove('copied');
        }, 1500);
    }).catch(err => {
        console.error('Copy failed:', err);
        const btn = evt?.target || evt;
        if (btn) {
            btn.textContent = 'Failed';
            setTimeout(() => { btn.textContent = 'Copy'; }, 1500);
        }
    });
}

/**
 * Check if a date is expiring within 5 minutes.
 * @param {string} expiresAt - ISO date string
 * @returns {boolean}
 */
export function isExpiringSoon(expiresAt) {
    if (!expiresAt) return false;
    const expiresTime = new Date(expiresAt).getTime();
    const now = Date.now();
    const fiveMinutes = 5 * 60 * 1000;
    return expiresTime - now < fiveMinutes && expiresTime > now;
}

/**
 * Render simple markdown to HTML with glassmorphic styling.
 * Handles headers (h1-h3), bold, list items, and line breaks.
 * @param {string} md - markdown text
 * @returns {string} HTML string
 */
export function renderMarkdown(md) {
    let html = escapeHtml(md);
    html = html.replace(/^### (.+)$/gm, '<div style="font-size: 13px; font-weight: 600; color: var(--accent-teal); margin: 14px 0 6px 0;">$1</div>');
    html = html.replace(/^## (.+)$/gm, '<div style="font-size: 14px; font-weight: 600; color: var(--accent-orange); margin: 18px 0 8px 0; padding-bottom: 6px; border-bottom: 1px solid rgba(255,255,255,0.08);">$1</div>');
    html = html.replace(/^# (.+)$/gm, '<div style="font-size: 16px; font-weight: 700; color: var(--text-primary); margin: 0 0 12px 0; padding-bottom: 8px; border-bottom: 1px solid rgba(255,255,255,0.12);">$1</div>');
    html = html.replace(/\*\*(.+?)\*\*/g, '<span style="font-weight: 600; color: var(--text-primary);">$1</span>');
    html = html.replace(/^- (.+)$/gm, '<div style="display: flex; gap: 8px; margin: 3px 0 3px 8px; line-height: 1.5;"><span style="color: var(--accent-teal); flex-shrink: 0;">•</span><span>$1</span></div>');
    html = html.replace(/\n\n/g, '<div style="height: 8px;"></div>');
    html = html.replace(/\n/g, '<br>');
    return html;
}

/**
 * Return a memory time-since duration string for the API.
 * @param {string} val - e.g., "1h", "6h", "24h", "3d", "7d"
 * @returns {string}
 */
export function getMemoryTimeSince(val) {
    return val || '24h';
}
