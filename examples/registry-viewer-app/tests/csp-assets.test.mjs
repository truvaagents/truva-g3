import assert from 'node:assert/strict';
import { readFile, readdir } from 'node:fs/promises';
import { createHash } from 'node:crypto';
const staticRoot = new URL('../static/', import.meta.url);
const vendor = new URL('js/vendor/', staticRoot);
const packages = JSON.parse(await readFile(new URL('packages.json', vendor), 'utf8'));
for (const item of packages.packages) {
    for (const [name, hash] of [['file', 'sha256'], ['license_file', 'license_sha256']]) {
        const bytes = await readFile(new URL(item[name], vendor));
        assert.equal(createHash('sha256').update(bytes).digest('hex'), item[hash], `${item.package} ${name} integrity`);
    }
}
const html = await readFile(new URL('index.html', staticRoot), 'utf8');
const scripts = [...html.matchAll(/<script[^>]+src="([^"]+)"/g)].map(match => match[1]);
assert.deepEqual(scripts.slice(0, 3), ['./js/vendor/cytoscape.js', './js/vendor/dagre.js', './js/vendor/cytoscape-dagre.js']);
assert.ok(scripts.every(path => !/^(https?:)?\/\//.test(path)), 'scripts load from the Viewer origin');
async function inspect(directory) {
    for (const entry of await readdir(directory, { withFileTypes: true })) {
        if (entry.name === 'vendor') continue;
        const file = new URL(entry.name + (entry.isDirectory() ? '/' : ''), directory);
        if (entry.isDirectory()) await inspect(file);
        else if (/\.(js|html)$/.test(entry.name)) {
            const source = await readFile(file, 'utf8');
            assert.doesNotMatch(source, /\son[a-z]+\s*=/i, `${file.pathname}: inline event attribute violates script-src self`);
            assert.doesNotMatch(source, /["']javascript:/i, `${file.pathname}: script URL violates script-src self`);
        }
    }
}
await inspect(staticRoot);
console.log('Local script integrity, dependency order and CSP source checks passed.');
