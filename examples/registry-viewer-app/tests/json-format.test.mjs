import assert from 'node:assert/strict';
import { prettyPrintJsonText, syntaxHighlightJson } from '../static/js/utils/format.js';

// Keep the source valid and include escape sequences as literal JSON bytes.
const escapedRaw = String.raw`{"order_id":9007199254740993,"k":1,"k":2,"nested":{"empty":[],"text":"a<b \"quoted\" [1, 2, ]","escaped":"\\n\u00e9"},"precise":1.2300e+40}`;
const pretty = prettyPrintJsonText(escapedRaw);
assert.match(pretty, /^\{\n  "order_id": 9007199254740993,/);
assert.match(pretty, /"k": 1,\n  "k": 2,/);
assert.match(pretty, /"empty": \[\]/);
assert.match(pretty, /1\.2300e\+40/);
// Remove only structural whitespace to compare every value/key token exactly.
const compact = text => (text.match(/"(?:\\[\s\S]|[^"\\])*"|[^\x20\t\r\n"]/g) || []).join('');
assert.equal(compact(pretty), compact(escapedRaw), 'formatting preserves all original JSON tokens');
assert.deepEqual(JSON.parse(pretty), JSON.parse(escapedRaw));
assert.equal(prettyPrintJsonText(' { "a" : {}, "b": [] } '), '{\n  "a": {},\n  "b": []\n}');
assert.equal(prettyPrintJsonText('[1,{"ok":true},null]'), '[\n  1,\n  {\n    "ok": true\n  },\n  null\n]');
for (const scalar of ['"text with [1, 2, ]"', '9007199254740993', 'false', 'null']) {
    assert.equal(prettyPrintJsonText(` \n${scalar}\t`), scalar);
}
assert.match(syntaxHighlightJson(pretty), /a&lt;b/);
assert.doesNotMatch(syntaxHighlightJson(pretty), /a<b/);
const visibleText = html => html.replace(/<\/?span(?: class="[^"]*")?>/g, '')
    .replace(/&lt;/g, '<').replace(/&gt;/g, '>').replace(/&amp;/g, '&');
assert.equal(visibleText(syntaxHighlightJson(pretty)), pretty, 'highlighting preserves every displayed JSON byte');
const classes = syntaxHighlightJson({ yes: true, no: false, empty: null, number: -1.25e10, text: 'false 123 <script>&' });
for (const cls of ['json-key', 'json-boolean', 'json-null', 'json-number', 'json-string']) {
    assert.ok(classes.includes(`class="${cls}"`), `shared highlighter retains ${cls}`);
}
assert.deepEqual(JSON.parse(visibleText(classes)), { yes: true, no: false, empty: null, number: -1.25e10, text: 'false 123 <script>&' });

// Real execution responses can carry multi-megabyte literals. The former
// repeated regex exhausted the regexp engine stack during both formatting and
// highlighting; exercise the complete Raw JSON display pipeline.
const largeValue = 'x'.repeat(8 * 1024 * 1024) + String.raw`\"quotes\" \\n\u00e9 [1, 2, ] <script>&`;
const largeRaw = `{"body":"${largeValue}","order_id":9007199254740993,"k":1,"k":2}`;
const largeExpected = `{\n  "body": "${largeValue}",\n  "order_id": 9007199254740993,\n  "k": 1,\n  "k": 2\n}`;
const largePretty = prettyPrintJsonText(largeRaw);
assert.equal(largePretty, largeExpected, 'large literals retain exact escapes, integer and duplicate keys');
assert.equal(visibleText(syntaxHighlightJson(largePretty)), largeExpected, 'large formatted JSON remains fully visible without truncation or stack overflow');
console.log('Raw JSON formatting preserves number literals, duplicate keys and string bytes.');
