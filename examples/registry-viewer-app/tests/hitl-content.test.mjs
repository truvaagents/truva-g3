import assert from 'node:assert/strict';
import { test } from 'node:test';
import { formatCheckpointExpiry, renderCheckpointContext } from '../static/js/utils/hitl-content.js';

test('checkpoint deadlines distinguish future, passed, and unknown values', () => {
    const now = Date.parse('2026-09-08T00:00:00Z');
    const cases = [
        [null, 'Unknown'], ['', 'Unknown'], ['not-a-date', 'Unknown'],
        [now - 1000, 'Deadline passed'], [now, 'Deadline passed'],
        [now + 100, 'in 1s'], [now + 59000, 'in 59s'],
        [now + 60000, 'in 1m'], [now + 61000, 'in 2m'],
        [now + 3600000, 'in 1h'], [now + 86400000, 'in 1d'],
    ];
    for (const [deadline, expected] of cases) {
        assert.equal(formatCheckpointExpiry(deadline, now), expected);
    }
});

test('checkpoint context preserves nested JSON and escapes keys and values', (t) => {
    // Model the textContent/innerHTML boundary used by the shared escapeHtml.
    const original = globalThis.document;
    globalThis.document = {
        createElement: () => ({
            textContent: '',
            get innerHTML() {
                return String(this.textContent).replaceAll('&', '&amp;')
                    .replaceAll('<', '&lt;').replaceAll('>', '&gt;');
            },
        }),
    };
    t.after(() => {
        if (original === undefined) delete globalThis.document;
        else globalThis.document = original;
    });
    const html = renderCheckpointContext({
        '<key>': '<em>literal</em>',
        turns: [{ role: 'user', content: '<b>question</b>' }],
        count: 0, enabled: false, missing: null,
    });
    assert(!html.includes('[object Object]'));
    assert(!html.includes('<em>'));
    assert(!html.includes('<key>'));
    assert(html.includes('&lt;key&gt;'));
    assert(html.includes('&lt;b&gt;question&lt;/b&gt;'));
    assert(html.includes('"role": "user"'));
    for (const value of ['0', 'false', 'null']) assert(html.includes(`>${value}</div>`));
});
