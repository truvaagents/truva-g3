#!/usr/bin/env python3
"""Rewrite ../../path links in docs/*.md to absolute GitHub URLs.

The docs reach out of the docs/ tree with ../../path references to source
files and directories. Docusaurus can't resolve those across its content
root, so we replace each with an absolute GitHub URL. Files get /blob/,
directories get /tree/ — the rewrite checks the target's actual type.

Fragments (#L42) and query strings are stripped before the fs-type check
but preserved in the rewritten URL.
"""
import re
import subprocess
from pathlib import Path

REPO_URL = 'https://github.com/truvaagents/truva-g3'
BRANCH = 'main'
PATTERN = re.compile(r'\]\(\.\./\.\./([^)]+)\)')

repo_root = Path(subprocess.check_output(
    ['git', 'rev-parse', '--show-toplevel']).decode().strip())

total = 0
missing = []

for md_path in (repo_root / 'docs').rglob('*.md'):
    content = md_path.read_text()

    def replace(match):
        global total
        target = match.group(1)
        fs_target = target.split('#')[0].split('?')[0]
        abs_target = repo_root / fs_target
        if not abs_target.exists():
            missing.append((md_path.relative_to(repo_root), target))
        kind = 'tree' if abs_target.is_dir() else 'blob'
        total += 1
        return f']({REPO_URL}/{kind}/{BRANCH}/{target})'

    new_content, n = PATTERN.subn(replace, content)
    if n > 0:
        md_path.write_text(new_content)
        print(f'  {md_path.relative_to(repo_root)}: {n} link(s)')

print(f'\nRewrote {total} link(s) total.')
if missing:
    print(f'\n{len(missing)} link(s) point to paths that do not exist on disk:')
    for src, target in missing[:20]:
        print(f'  {src} -> {target}')
    if len(missing) > 20:
        print(f'  ... and {len(missing) - 20} more')
