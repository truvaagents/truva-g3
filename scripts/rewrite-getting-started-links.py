#!/usr/bin/env python3
"""Sync GETTING_STARTED.md (repo root) → docs/getting-started.md, rewriting
links from repo-root-relative to docs-site-relative.

Runs as a `prebuild`/`prestart` npm script so the docs-site copy is always
fresh from the source of truth. The output file is generated and should be
gitignored.

Compatible with Python 3.8+ (uses only stdlib; tested on 3.14).

Rewrites:
  - `docs/<section>/<file>.md` → `./<section>/<file>.md`  (Docusaurus-internal)
  - `examples/<path>`          → GitHub tree URL
  - `<module>/README.md`       → GitHub blob URL  (core, ai, orchestration, …)
  - `README.md`, `GETTING_STARTED.md`, `LICENSE`, `NOTICE`, `CONTRIBUTING.md`
    → GitHub blob URLs

Also prepends frontmatter for sidebar positioning.
"""
import re
import sys
from pathlib import Path

if sys.version_info < (3, 8):
    sys.exit(f'Python 3.8+ required (running on {sys.version.split()[0]}).')

REPO_URL = 'https://github.com/truvaagents/truva-g3'
BRANCH = 'main'

FRONTMATTER = """---
sidebar_position: 2
sidebar_label: Getting started
---

"""

MODULES = 'core|ai|orchestration|memory|resilience|telemetry'

DOCS_PATH = re.compile(
    r'\]\(docs/([a-z\-]+)/([A-Z0-9_]+)\.md(#[a-z0-9\-]+)?\)'
)
EXAMPLES_PATH = re.compile(r'\]\(examples/([^)]+)\)')
MODULE_README = re.compile(
    rf'\]\(({MODULES})/README\.md(#[a-z0-9\-]+)?\)'
)

REPO_ROOT_FILES = ['README.md', 'GETTING_STARTED.md', 'LICENSE', 'NOTICE',
                   'CONTRIBUTING.md']

repo_root = Path(__file__).resolve().parent.parent
source = repo_root / 'GETTING_STARTED.md'
target = repo_root / 'docs' / 'getting-started.md'

content = source.read_text()

content = DOCS_PATH.sub(r'](./\1/\2.md\3)', content)
content = EXAMPLES_PATH.sub(
    rf']({REPO_URL}/tree/{BRANCH}/examples/\1)', content,
)
content = MODULE_README.sub(
    rf']({REPO_URL}/blob/{BRANCH}/\1/README.md\2)', content,
)
for fname in REPO_ROOT_FILES:
    content = content.replace(
        f']({fname})',
        f']({REPO_URL}/blob/{BRANCH}/{fname})',
    )

target.write_text(FRONTMATTER + content)
print(f'Synced {source.name} → {target.relative_to(repo_root)}')
