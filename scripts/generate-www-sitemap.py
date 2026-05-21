#!/usr/bin/env python3
"""Generate www/sitemap.xml by walking www/**/*.html.

Run before committing new blogs or whitepapers:
    python3 scripts/generate-www-sitemap.py

URL canonicalization (per docs/DOCUMENTATION_STRATEGY_NEW.md Phase 8A step 39 —
Google treats /blogs/ and /blogs/index as duplicate content if both leak in):
    www/index.html            -> https://truvag3.dev/
    www/<folder>/index.html   -> https://truvag3.dev/<folder>/         (trailing slash)
    www/<folder>/<slug>.html  -> https://truvag3.dev/<folder>/<slug>   (no .html, no trailing slash)

`lastmod` is derived per file from git:
    git log -1 --format=%cI -- <file>
Falls back to current HEAD commit time for files not yet committed.
"""
import subprocess
import sys
from pathlib import Path

SITE_URL = "https://truvag3.dev"
REPO_ROOT = Path(__file__).resolve().parent.parent
WWW_DIR = REPO_ROOT / "www"
OUTPUT = WWW_DIR / "sitemap.xml"


def canonical_url(html_path: Path) -> str:
    rel = html_path.relative_to(WWW_DIR)
    parts = rel.parts
    if rel.name == "index.html":
        if len(parts) == 1:
            return f"{SITE_URL}/"
        folder = "/".join(parts[:-1])
        return f"{SITE_URL}/{folder}/"
    slug = rel.stem
    if len(parts) == 1:
        return f"{SITE_URL}/{slug}"
    folder = "/".join(parts[:-1])
    return f"{SITE_URL}/{folder}/{slug}"


def git_lastmod(html_path: Path) -> str:
    rel = html_path.relative_to(REPO_ROOT)
    result = subprocess.run(
        ["git", "log", "-1", "--format=%cI", "--", str(rel)],
        cwd=REPO_ROOT,
        capture_output=True,
        text=True,
    )
    out = result.stdout.strip()
    if out:
        return out
    head = subprocess.run(
        ["git", "log", "-1", "--format=%cI"],
        cwd=REPO_ROOT,
        capture_output=True,
        text=True,
    )
    return head.stdout.strip()


def main() -> int:
    html_files = sorted(WWW_DIR.rglob("*.html"))
    if not html_files:
        print("ERROR: no HTML files found under www/", file=sys.stderr)
        return 1

    lines = [
        '<?xml version="1.0" encoding="UTF-8"?>',
        '<urlset xmlns="http://www.sitemaps.org/schemas/sitemap/0.9">',
    ]
    for path in html_files:
        url = canonical_url(path)
        lastmod = git_lastmod(path)
        lines.append("  <url>")
        lines.append(f"    <loc>{url}</loc>")
        if lastmod:
            lines.append(f"    <lastmod>{lastmod}</lastmod>")
        lines.append("  </url>")
    lines.append("</urlset>")
    lines.append("")

    OUTPUT.write_text("\n".join(lines))
    print(f"Wrote {OUTPUT.relative_to(REPO_ROOT)} ({len(html_files)} URLs)", file=sys.stderr)
    return 0


if __name__ == "__main__":
    sys.exit(main())
