# TruvaG3 Documentation Strategy (Two-Surface, NEW)

> **Status:** active. Publishing architecture for the TruvaG3 documentation surfaces.
>
> **Last updated:** 2026-05-21.
>
> **Implementation status (2026-05-21):** Both surfaces are live — [truvag3.dev](https://truvag3.dev) (website) and [docs.truvag3.dev](https://docs.truvag3.dev) (docs). Phases 1–3 complete, Phase 4 mostly complete (one outstanding step — see below), Phase 5 complete, Phase 6 largely complete, Phase 7 partial, **Phase 8A complete**, Phase 8B optional/deferred. Per-phase status annotated inline in the [Execution plan](#execution-plan).
>
> **Known outstanding items:**
> - **Phase 4 step 19** — Docusaurus glassmorphism landing not yet decommissioned. [docs-site/src/pages/index.tsx](../docs-site/src/pages/index.tsx) still renders Hero + WhyTruvaG3, duplicating the website homepage's role.
> - **Phase 7** — Launch-readiness items (GA4, OG tags, Lighthouse audit, cross-browser smoke, README canonical URL update) status not tracked here; verify before announcing.
> - **Phase 8A — search engine discoverability** — ✓ Complete. Both sitemaps and `robots.txt` files live; all 52 URLs across both surfaces return `200` direct (no redirect hops); both sitemaps submitted to GSC + Bing. **One mid-flight fix worth noting:** Docusaurus defaults emit no-slash URLs in the sitemap while CF Pages serves the canonical slashed form (`/docs/intro/`), so every docs URL was 307-redirecting. Fixed by setting `trailingSlash: true` in [docs-site/docusaurus.config.ts](../docs-site/docusaurus.config.ts) and sweeping all www→docs links to add the trailing slash. Future Docusaurus surfaces on CF Pages should set this from day one (now folded into Phase 2 step 7).
> - **Phase 8B — search/social appearance polish** — optional. Defer until 8A confirms pages are getting indexed.
> - **Open decisions** table at the end of this doc — most rows still "Undecided".

## Executive Summary

TruvaG3's public web presence will ship as **two independent surfaces under one repository**:

- **`truvag3.dev`** — hand-crafted HTML/CSS website. Homepage, blog posts, whitepapers. Custom design. No JavaScript framework constraints.
- **`docs.truvag3.dev`** — Docusaurus reference docs (existing). Markdown source, sidebar navigation, edit-on-GitHub. (Search is *not* configured by default — Docusaurus 3 ships without a search backend; add Algolia DocSearch or a local-search plugin when traffic justifies it.)

Both surfaces deploy from this repository via **Cloudflare Pages** (free tier), one Pages project per surface, each watching a different subfolder. No GitHub Actions workflow is required for publishing.

This revises the single-surface plan in the original strategy doc, motivated by (a) growing HTML long-form content (an architecture article shipped, a second blog ~90% drafted, multiple whitepapers in inventory) that doesn't fit Docusaurus's React landing model, and (b) dissatisfaction with the current Docusaurus homepage's ability to telegraph "serious framework" to first-time visitors.

---

## Table of Contents

1. [Why this revision](#why-this-revision)
2. [The two-surface architecture](#the-two-surface-architecture)
3. [What's preserved](#whats-preserved-from-the-current-build)
4. [What's new](#whats-new)
5. [Content allocation](#content-allocation)
6. [Repository structure](#repository-structure)
7. [Publishing pipeline](#publishing-pipeline-cloudflare-pages)
8. [DNS strategy](#dns-strategy)
9. [Website requirements](#website-requirements)
10. [Search engine discoverability](#search-engine-discoverability)
11. [Execution plan](#execution-plan)
12. [Maintenance equation](#maintenance-equation)
13. [Trade-offs and open decisions](#trade-offs-and-open-decisions)

---

## Why this revision

The original strategy assumed a single Docusaurus site at `truvag3.dev` with subpaths for docs, blog, examples, and whitepapers. That works for projects whose content is mostly reference docs with light supporting material. TruvaG3's content shape is different:

1. **Article-style long-form already exists and works.** `www/blogs/microagents-architecture.html` (migrated from `docs/blogs/` in Phase 1) is a 1389-line standalone HTML article with custom typography (760px max-width, 17px body, 1.65 line-height, custom aside/code styles). Re-implementing this in MDX would lose the magazine-style polish that the format actually benefits from.
2. **Whitepapers are an entire content category.** A handful of HTML whitepapers live at `~/Documents/Documents/TruvaG3/whitepapers/` and need a home. Each is standalone, formatted, self-contained.
3. **The Docusaurus homepage is structurally constrained.** A serious framework landing typically needs: code-snippet hero, architecture diagrams, customer/showcase signals, recent blog posts, GitHub stars badge, registry-viewer screenshots, multi-section narrative. None of those are comfortable to author in a Docusaurus `index.tsx` — every visual choice fights the underlying theme.
4. **One author = no coordination tax.** Splitting surfaces only makes sense when the team can afford the operational overhead. A solo maintainer with a single Cloudflare Pages account can ship and maintain two surfaces with no more friction than one — provided the boundary is clean.

The principle *"different aesthetics across surfaces is correct, not a bug"* holds here. This plan takes it literally: two surfaces, two physical sites, no shared theme.

---

## The two-surface architecture

```
┌─────────────────────────┐         ┌──────────────────────────────┐
│      truvag3.dev        │         │     docs.truvag3.dev         │
│   ─────────────────     │         │   ─────────────────────────  │
│                         │         │                              │
│  Website                │         │  Docusaurus reference        │
│  • Homepage             │         │  • Quick-start funnel        │
│  • Blogs                │         │  • Concepts & guides         │
│  • Whitepapers          │         │  • API surface               │
│  • Showcase             │         │  • Module READMEs (linked)   │
│                         │         │                              │
│  Hand-crafted HTML/CSS  │         │  Auto-generated sidebar      │
│  No framework           │         │  Edit-on-GitHub button       │
│  Custom typography      │         │  Last-updated timestamps     │
│                         │         │                              │
│  Reader's question:     │         │  Reader's question:          │
│  "Why should I care?"   │         │  "How do I do X?"            │
└─────────────────────────┘         └──────────────────────────────┘
            ▲                                   ▲
            │                                   │
            │      Cloudflare Pages (free)      │
            │   ┌─────────────────────────────┐ │
            └───┤  Same repo, two projects    ├─┘
                │  truva-g3-www  / www/       │
                │  truva-g3-docs / docs-site/ │
                └─────────────────────────────┘
                              ▲
                              │
                              │
                ┌─────────────────────────────┐
                │  github.com/truvaagents/    │
                │       truva-g3              │
                │                             │
                │  Single source of truth     │
                │  Push to main → both        │
                │  surfaces re-deploy         │
                │  (only what changed)        │
                └─────────────────────────────┘
```

**Cross-surface navigation:**

- `truvag3.dev` navbar → "Docs" link → `docs.truvag3.dev/docs/intro/`
- `docs.truvag3.dev` navbar → "Blog / Whitepapers" links → `truvag3.dev/blogs/` and `truvag3.dev/whitepapers/`
- Both sites share brand-color tokens and the two-tone TruvaG3 mark for visual continuity, but otherwise are free to diverge in layout, typography, and density.

---

## What's preserved from the current build

The current Docusaurus build (everything done on the `docs/site-launch` branch) **carries forward intact**. The two-surface architecture preserves all docs-side work:

| Asset | Status under new architecture |
|---|---|
| `docs-site/` Docusaurus scaffold + config | Preserved. Becomes the `docs.truvag3.dev` surface. |
| 30 markdown docs with cross-source links rewritten (601 links) | Preserved. |
| `docs/intro.md` (architectural orientation page) | Preserved. |
| `docs/reference/GO_PACKAGE_REFERENCE.md` | Preserved. |
| Two-tone navbar brand mark + brand-color tokens | Preserved. Same CSS will inform the website's brand-color tokens. |
| GETTING_STARTED.md prebuild sync hook | Preserved. Same pattern may be reused for the website's blog index. |
| `scripts/rewrite-cross-source-links.py` and `scripts/rewrite-getting-started-links.py` | Preserved. Same tooling pattern. |
| MDX fixes (`{id}` headings backticked) | Preserved. |
| `docs/overview/ARCHITECTURE.md` exclusion | Preserved. |
| Docusaurus glassmorphism landing (`docs-site/src/pages/index.tsx`) | **Deprecated** — `docs.truvag3.dev/` will redirect or show a minimal "Welcome — try /docs/intro" page. The website surface owns the homepage. |
| Planned GitHub Actions workflow (`.github/workflows/docs.yml`) | **Obsolete** — Cloudflare Pages handles CI/CD for both surfaces. No workflow file needed. |

**Net change to docs side: minimal.** The Docusaurus build keeps running. Only the homepage (landing page) loses its top-of-funnel role; everything else continues as-is.

---

## What's new

| Item | Purpose |
|---|---|
| `www/` directory at repo root | Hand-crafted HTML/CSS website surface. |
| `www/index.html` | New homepage. Replaces the Docusaurus landing as the brand-facing entry point. |
| `www/blogs/` | Migration target for `docs/blogs/*.html`. Future blogs land here. |
| `www/whitepapers/` | New. Whitepapers migrated from `~/Documents/Documents/TruvaG3/whitepapers/`. |
| `www/assets/` | Shared CSS, fonts, images, brand assets. |
| Cloudflare account + two Pages projects | Publishing pipeline. |
| Two DNS records (or DNS migration to Cloudflare) | `truvag3.dev` + `docs.truvag3.dev` pointed at Cloudflare. |
| Updated cross-links | Docs site references to blogs/whitepapers point to `truvag3.dev` URLs, not GitHub blob URLs. |

---

## Content allocation

Single source of truth for "what belongs where":

| Content type | Surface | Path | Format |
|---|---|---|---|
| Homepage / landing | Website | `www/index.html` | HTML |
| Brand intro / "what is TruvaG3" pitch | Website | `www/index.html` (hero) | HTML |
| Long-form articles / blog posts | Website | `www/blogs/<slug>.html` | HTML |
| Architecture deep-dives (whitepapers) | Website | `www/whitepapers/<slug>.html` | HTML |
| Showcase / "who's using" / case studies | Website | `www/index.html` (section) | HTML |
| Press / brand / logo assets for press | Website | `www/press/` | HTML + downloads |
| Quick-start funnel / "I want to run it" | Docs | `docs/intro.md` + `docs/getting-started.md` | Markdown |
| Framework concepts | Docs | `docs/overview/FRAMEWORK_FEATURES_GUIDE.md` | Markdown |
| Per-module guides (building, observability, orchestration, etc.) | Docs | `docs/<section>/*.md` | Markdown |
| API reference | Docs (link out) | `docs/reference/GO_PACKAGE_REFERENCE.md` → pkg.go.dev | Markdown + external |
| Environment variables, limits, runtime config | Docs | `docs/reference/*.md` | Markdown |
| Module-level READMEs (core/, ai/, orchestration/, …) | Stays in repo, linked from docs | `<module>/README.md` | Markdown (GitHub-rendered) |
| Examples (~50 example projects) | Stays in repo, linked from docs | `examples/<name>/` | Mixed |

**Boundary heuristic when in doubt:** *Could a reader answer the question by reading this page in isolation, or do they need surrounding navigation?* If isolated, website. If they need a sidebar to navigate to related material, docs.

---

## Repository structure

Proposed layout — adds `www/` next to existing `docs-site/`:

```
truva-g3/
├── docs/                                  # Markdown source (unchanged)
│   ├── intro.md
│   ├── getting-started.md                 # generated, gitignored
│   ├── overview/
│   ├── building/
│   ├── orchestration/
│   ├── observability/
│   ├── operations/
│   ├── memory-and-chat/
│   ├── reference/
│   └── DOCUMENTATION_STRATEGY_NEW.md      # this file (publishing architecture)
│
├── docs-site/                               # Docusaurus (unchanged)
│   ├── docusaurus.config.ts
│   ├── sidebars.ts
│   ├── src/
│   ├── static/
│   ├── package.json
│   └── …
│
├── www/                             # NEW: hand-crafted website
│   ├── index.html                         # homepage
│   ├── blogs/
│   │   ├── index.html                     # blog index page
│   │   ├── microagents-architecture.html # migrated from docs/blogs/
│   │   └── <new-blog-90pct>.html          # new blog draft
│   ├── whitepapers/
│   │   ├── index.html                     # whitepapers index
│   │   └── <whitepaper-files>.html        # copied from ~/Documents/…
│   ├── assets/
│   │   ├── css/
│   │   │   ├── brand.css                  # shared brand tokens (mirrors docs-site/src/css)
│   │   │   ├── article.css                # long-form article styles
│   │   │   └── landing.css                # homepage-specific
│   │   ├── img/
│   │   │   ├── logo.svg
│   │   │   └── og-card.png                # default open-graph image
│   │   └── fonts/                         # only if self-hosting
│   ├── _headers                           # Cloudflare Pages: cache/CSP headers
│   └── _redirects                         # Cloudflare Pages: redirects (optional)
│
├── GETTING_STARTED.md                     # source of truth (unchanged)
├── README.md                              # unchanged
├── scripts/
│   ├── rewrite-cross-source-links.py      # unchanged
│   ├── rewrite-getting-started-links.py   # unchanged
│   └── sync-blogs-to-www.py               # NEW (optional): if blogs source-of-truth stays elsewhere
├── examples/
├── core/  ai/  orchestration/  …          # framework modules
└── .gitignore                             # add: docs/getting-started.md (already), nothing in www/
```

**`www/` is fully committed to git.** Unlike `docs/getting-started.md` (generated), the website HTML is the source of truth. No build step is required; Cloudflare Pages serves it verbatim.

**A naming note.** Folder names map directly to their roles: `docs-site/` holds the Docusaurus app that renders `docs.truvag3.dev`; `www/` holds the hand-crafted HTML for `truvag3.dev`. (`docs-site/` started life as Docusaurus's scaffolder-default `website/` and was renamed in Phase 1 to remove the docs-folder-called-"website" ambiguity. The Cloudflare project name `truva-g3-docs` now matches the folder name, and "website" in the rest of this doc unambiguously refers to the `www/` surface.)

---

## Publishing pipeline (Cloudflare Pages)

### One-time setup

1. **Push repo to GitHub** (private or public — Cloudflare works with both).
2. **Cloudflare account** — free tier. No credit card required at signup.
3. **Two Pages projects**, both connected to the same GitHub repo, each pointed at a different root directory:

| Project | Root directory | Build command | Build output | Custom domain |
|---|---|---|---|---|
| `truva-g3-docs` | `docs-site/` | `npm install && npm run build` | `build` | `docs.truvag3.dev` |
| `truva-g3-www` | `www/` | _(empty — static HTML)_ | `.` | `truvag3.dev` |

4. **Configure build watch paths** to prevent unnecessary rebuilds:

| Project | Include |
|---|---|
| `truva-g3-docs` | `docs-site/*`, `docs/*`, `GETTING_STARTED.md`, `scripts/rewrite-getting-started-links.py` |
| `truva-g3-www` | `www/*` |

   A change to `docs/` won't kick a `www/` rebuild; a change to `www/` won't kick a docs rebuild.

5. **SSL certs are auto-issued** by Cloudflare on each custom domain. No manual cert management.

### Deploy flow on every `git push origin main`

```
git push
   ↓
GitHub webhook → Cloudflare (immediately)
   ↓
Cloudflare evaluates build watch paths per project
   ↓
Only the project(s) whose watched paths changed build
   │
   ├── truva-g3-docs build path:
   │     cd docs-site
   │     npm install
   │     npm run build       ← prebuild hook fires: Python sync script runs
   │     deploy ./build → docs.truvag3.dev
   │
   └── truva-g3-www build path:
         (no build step — static HTML)
         deploy ./www → truvag3.dev
   ↓
Both surfaces live, ~30s after push
```

### Per-PR preview deploys

When a pull request is opened on the repo, Cloudflare automatically builds and deploys it to a temporary URL:

- `https://<branch>.truva-g3-docs.pages.dev`
- `https://<branch>.truva-g3-www.pages.dev`

URLs are posted as PR comments. Lets you review the rendered site before merging.

### What replaces the planned GitHub Actions workflow

The original strategy doc had a Phase 1 step to author `.github/workflows/docs.yml` using `peaceiris/actions-gh-pages@v3`. **Under Path B, this file is not created.** Cloudflare Pages is the CI/CD; no GitHub Actions are required for publishing.

(GitHub Actions may still be desired for *other* purposes — running tests, linting, security scans. Those are independent of publishing.)

### Cloudflare Pages free-tier limits

Both projects share one Cloudflare account's quota. Realistic usage for a solo-maintained docs+website will not approach these limits:

| Resource | Free-tier limit | Realistic usage |
|---|---|---|
| Builds per month (per account) | 500 | `<30` (both projects combined) |
| Concurrent builds | 1 | Fine for solo dev |
| Build minutes | Unlimited | — |
| Bandwidth | Unlimited | — |
| Requests | Unlimited | — |
| Custom domains | 100 per account | 2 used |
| Pages projects per repo | 5 | 2 used |
| Preview deployments active | Unlimited per project | — |

Sources: [Cloudflare Pages limits](https://developers.cloudflare.com/pages/platform/limits/) ([monorepo support](https://developers.cloudflare.com/pages/configuration/monorepos/) confirms the 5-projects-per-repo number).

---

## DNS strategy

Two options, pick one:

### Option D1 — Move DNS to Cloudflare (recommended)

- Transfer `truvag3.dev` DNS to Cloudflare's nameservers (free; takes ~15 min at the registrar).
- Adding custom domains in Cloudflare Pages becomes one-click — CF auto-configures the records.
- Bonus: Cloudflare DNS is among the fastest globally and includes free DDoS protection.
- Trade-off: another service dependency. Migrating off requires a nameserver change at the registrar.

### Option D2 — Keep DNS at current registrar

- Add two CNAME records at your DNS provider:
  - `truvag3.dev` → `truva-g3-www.pages.dev`
  - `docs.truvag3.dev` → `truva-g3-docs.pages.dev`
- (Apex domain CNAME support depends on your DNS provider. Most modern providers — Cloudflare, Route 53, Namecheap, Google Domains — support CNAME flattening or ALIAS records. Some legacy providers do not. Verify before committing.)
- Trade-off: more moving parts; less integrated CF experience.

---

## Website requirements

This section names what the website surface needs to *do*, not what it should look like. The design is a separate creative exercise.

### Homepage (`www/index.html`)

Should answer "why should I care about TruvaG3?" within the first viewport. Required sections:

1. **Hero**
   - Two-tone TruvaG3 brand mark.
   - Tagline: "True. Dynamic Discovery. Decentralized. Observable."
   - Primary CTA: "Get started" → `docs.truvag3.dev/docs/intro/`
   - Secondary CTA: "View on GitHub" → repo URL
   - Optional: short tagline-extension sentence below.

2. **Architecture at a glance**
   - Two coordination layers (orchestration inside agents; decentralized between agents) — this is the most distinctive thing about the framework.
   - Either a SVG diagram or a labeled code-snippet showing the discovery flow.

3. **Code snippet hero (one block)**
   - "Look how simple it is": a 10–15 line Go snippet showing a minimal agent.
   - Source from `examples/agent-example/` or similar.

4. **Why TruvaG3 — feature matrix**
   - 4 cards (current Docusaurus content can lift directly): capability-based discovery, decentralized coordination, vendor-agnostic AI, plain Kubernetes plain HTTP.
   - Optionally: an honest comparison strip vs LangGraph/CrewAI/AutoGen (the README has this).

5. **Live trust signals**
   - **GitHub stars badge** (real-time count or hard-coded with periodic refresh; either works for a low-cadence repo).
   - Latest release version + date (read from a release tag).
   - License + "self-hosted, no SaaS" badge.

6. **Registry Viewer screenshot or short video loop**
   - The registry-viewer-app is one of the most demo-able artifacts in the project — a live dashboard of services discovering each other. A still screenshot or a 5–10s autoplay video loop earns its place near the architecture section.

7. **Recent posts / what's new**
   - 3 most recent blog posts (link to `www/blogs/`).
   - If the blog index is hand-curated, no auto-generation needed. If it grows, a prebuild script can read `www/blogs/*.html` frontmatter and emit a JSON index.

8. **Showcase / who's using**
   - Empty section is acceptable at launch. Real adopter logos are a long-term goal.
   - Alternative: showcase the 50 example projects (categorized).

9. **Footer**
   - Quick links: Docs, Blog, Whitepapers, GitHub, License.
   - Brand mark + tagline.

### Blogs index (`www/blogs/index.html`)

- List of all blog posts, newest first.
- Each entry: title, date, one-line description, link.
- No infinite scroll, no pagination needed until count > 20.

### Blog post template

Currently embodied by `www/blogs/microagents-architecture.html`. Worth standardizing into a shared template:

- Header with article title, date, author, reading time.
- 760px max-width body, 17px font, 1.65 line-height (matches existing).
- Custom-styled asides, code blocks, tables (already in existing).
- Footer with "Back to blog" link, social share (optional), "Edit on GitHub" (optional).

### Whitepapers index (`www/whitepapers/index.html`)

- List of all whitepapers.
- Each entry: title, abstract (3-line summary), download link (PDF if available, HTML otherwise).
- Same template as blog index.

### Shared brand styling

- `www/assets/css/brand.css` should define the same brand-color tokens already defined in `docs-site/src/css/custom.css`. Keeping them in sync requires discipline (no shared build for this; consider a Python script if drift becomes a problem).
- The two-tone "Truva / G3" mark should appear identically on both surfaces.

### What the website does NOT need

- A CMS.
- A JavaScript framework.
- A build step (Cloudflare serves the HTML directly).
- Authentication.
- Comments.
- A newsletter signup form (defer until there's an actual newsletter).
- Per-post analytics (free GA4 in the `<head>` is enough).

---

## Search engine discoverability

**Goal:** both surfaces show up in Google search (and Bing) for relevant queries. Sitemaps and `robots.txt` are the minimum baseline that lets crawlers find every intended URL. Indexing and ranking are separate decisions Google makes based on page-level signals (title, description, content quality, internal links) — sitemap submission gets us *eligible* to rank, not *ranked*. Everything beyond crawl discovery and submission is a separate ranking/appearance layer, deferred from this phase.

As of 2026-05-21:

- `docs.truvag3.dev/sitemap.xml` is **auto-generated by `@docusaurus/preset-classic`** — `200 OK`, served as `application/xml`, currently 33 URLs (one of which is the stray scaffolder demo at `/markdown-page` and should be removed).
- `truvag3.dev` has **no sitemap**.
- **Neither surface has a `robots.txt`** — crawlers find the docs sitemap only by GSC submission, and can't find the website sitemap at all.

### Per-surface strategy

**`docs.truvag3.dev` — let the framework do it.** Docusaurus ships `/sitemap.xml` at build time with sensible defaults (`changefreq: weekly`, `priority: 0.5`). To make it crawl cleanly we need to (a) delete the stray scaffolder demo page that's leaking into the sitemap, (b) add a `robots.txt` in [docs-site/static/](../docs-site/static/) that points at the sitemap, and (c) set `trailingSlash: true` in [docs-site/docusaurus.config.ts](../docs-site/docusaurus.config.ts) so sitemap URLs and internal `<link rel="canonical">` tags match the slashed form CF Pages actually serves. Without (c) every docs URL 307s, which is suboptimal for crawl budget and reads as "Page with redirect" in GSC.

**`truvag3.dev` (www/) — script-generate, don't hand-author.** The website has 20 URLs today (1 homepage + 3 under `blogs/` + 16 under `whitepapers/`), but blogs and whitepapers are growth categories — a hand-authored `sitemap.xml` rots within months. A tiny Python script that walks `www/**/*.html` and emits `sitemap.xml` is the right size; Cloudflare Pages serves the result verbatim. Two sub-decisions about how the generator runs:

| Decision | Options | Recommended default |
|---|---|---|
| When does the script run? | (a) manually before commit, output committed; (b) on every CF Pages build step | (a) — preserves www/'s "no build step" property |
| `lastmod` source | (a) today's date at run time; (b) `git log -1 --format=%cI <file>` per file | (b) — accurate from day one; "today" advances `lastmod` on unchanged files every regen, which is a noisy crawl signal |

### Search and social appearance metadata

Three different mechanisms drive three different outcomes — easy to conflate, worth separating:

- **Google search snippets** come from `<title>`, `<meta name="description">`, page content, and Google's own rewriting. OG tags do *not* directly drive Google search appearance.
- **Social previews** (Twitter, LinkedIn, Slack unfurls) come from Open Graph and Twitter Card `<meta>` tags.
- **Canonical URLs** (`<link rel="canonical">`) tell Google which URL to treat as the indexable version when the same content is reachable via more than one URL — particularly relevant for whitepapers that may also exist as PDF downloads. **The canonical URL on each page must match the URL form used in the sitemap exactly** (same trailing-slash and `.html` decisions); conflicting signals are explicitly bad per Google's duplicate-URL consolidation guidance.

Every page on both surfaces should have, at minimum:

- `<title>` — unique, descriptive
- `<meta name="description">` — under 160 chars
- `<link rel="canonical" href="…">` — same URL form as the sitemap entry

Optional appearance polish (Phase 8B): Open Graph + Twitter Card meta for social previews, plus a default OG card at `www/assets/img/og-card.png` (1200×630) referenced in the [Website requirements](#website-requirements) section.

**Structured data (JSON-LD) is deferred.** Schemas like `Article`, `BreadcrumbList`, and `Organization` unlock Google's "rich result" treatments (article cards with images, breadcrumb hierarchies in result listings) but are not required for basic visibility. Revisit only if rich-result eligibility becomes a priority — out of scope for the "come up in search" goal.

### Google Search Console

A **Domain property** for `truvag3.dev` (verified via DNS TXT record) covers both surfaces under a single GSC roof — `truvag3.dev` + `docs.truvag3.dev` + any future subdomain. The alternative — two URL-prefix properties, one per surface — works without DNS access but splits the telemetry. Recommend the Domain property.

Bing Webmaster Tools accepts the same sitemap and similar verification material. Submitting in parallel costs ~5 extra minutes and broadens reach. Recommended.

---

## Execution plan

Sequenced for a solo maintainer. Phases are independent enough that pausing between any two is safe.

### Phase 1 — Repo scaffold (~1 hour) — ✓ Complete

1. Create `www/` at repo root with the structure above.

2. Move `docs/blogs/microagents-architecture.html` and its asset PNGs/JSONs into `www/blogs/`.

3. **Sweep every reference to `docs/blogs/` and to `microagents-architecture` in the repo** — not just the README. Run:

   ```bash
   rg "docs/blogs|microagents|truvag3-introduction" --type=md --type=html
   ```

   Confirmed reference sites (as of 2026-05-16):
   - [README.md:8](../README.md#L8) — microagents link in the "About this framework" blockquote
   - [README.md:587](../README.md#L587) — microagents link in the Guides section
   - [README.md:588](../README.md#L588) — link to the *Introduction to TruvaG3* blog. (The file was added to `main` after this strategy doc was written; Phase 1 moved it to `www/blogs/truvag3-introduction.html` alongside the other blog content.)
   - [README.md:681](../README.md#L681) — microagents link in Next Steps
   
   Update each to point at the new website URL (`https://truvag3.dev/blogs/microagents-architecture` once live — no trailing slash, since Cloudflare Pages serves the `.html` file at its extensionless path; GitHub blob URL for the interim).

4. **Inventory the whitepapers and stage them in the repo.** The source folder `~/Documents/Documents/TruvaG3/whitepapers/` is a personal-machine dependency — Cloudflare's build runner cannot read it, and no other contributor can find it. Required steps:
   - List exact filenames (and any sub-asset dependencies) once.
   - Copy each into `www/whitepapers/` and commit. From this point forward the repo is the source of truth.
   - Sanity-check each file for embedded absolute paths (`/Users/…`, file:// URLs, machine-specific font paths) and replace with repo-relative or web-relative paths.
   - **Until inventoried,** every reference to "whitepapers" in this plan is conditional on you doing this one-time staging. Don't proceed past Phase 3 with whitepapers still living outside the repo.

5. Create minimal placeholder `www/index.html` (just enough to verify deployment): brand mark + "Site under construction — see [docs](https://docs.truvag3.dev)" link.

6. Add `www/assets/css/brand.css` mirroring the brand tokens from `docs-site/src/css/custom.css`.

### Phase 2 — Cloudflare Pages — docs surface (~30 min) — ✓ Complete

> **Order matters.** Update Docusaurus config *before* the first Cloudflare deploy. The current `baseUrl: '/truva-g3/'` is correct only for GitHub Pages — leaving it in place during the first `pages.dev` deploy produces an unstyled site (asset paths break because `pages.dev` serves at `/`, not `/truva-g3/`).

7. **Update `docs-site/docusaurus.config.ts` first:**
   - `baseUrl: '/'` (was `'/truva-g3/'`)
   - `url: 'https://docs.truvag3.dev'` (was `'https://truvaagents.github.io'`)
   - `trailingSlash: true` — Docusaurus emits `route/index.html` and CF Pages serves the slashed form (`/docs/intro/`) as canonical. Without this, the auto-generated sitemap lists no-slash URLs that 307-redirect, every docs URL costs an extra crawl hop, and GSC flags "Page with redirect." (Discovered the hard way during Phase 8A validation; folding into Phase 2 here so future Docusaurus surfaces on CF Pages don't repeat the gotcha.)
   - Commit and push.
   - **Until the custom domain is live**, the local dev server URL changes — `npm run start` now serves at `http://localhost:3000/` (no `/truva-g3` prefix).
8. Push repo to GitHub if not already pushed.
9. Create Cloudflare account.
10. Create Pages project `truva-g3-docs`: connect to repo, root directory `docs-site/`, build command `npm install && npm run build`, output `build`.
11. First deploy lands at `https://truva-g3-docs.pages.dev/`. **Verify the site renders with styles and links work before adding the custom domain.** (If you skipped step 7, you'll see unstyled pages here — that's the failure mode the ordering prevents.)
12. Add custom domain `docs.truvag3.dev`. Add DNS record per your chosen DNS strategy (D1 or D2). Wait for SSL cert (~5 min).
13. Verify `docs.truvag3.dev/docs/intro/` resolves and renders styled.

### Phase 3 — Cloudflare Pages — website surface (~10 min) — ✓ Complete

14. Create Pages project `truva-g3-www`: connect to same repo, root directory `www/`, no build command, output `.`.
15. First deploy at `https://truva-g3-www.pages.dev/` — should show the placeholder index page.
16. Add custom domain `truvag3.dev`. DNS record. Wait for cert.
17. Verify `truvag3.dev` resolves and shows the placeholder.

### Phase 4 — Cross-link cleanup (~30 min) — ◐ Partial (step 19 pending)

18. Update `docs/intro.md` and any other docs-site pages that link to the architecture article or whitepapers — change GitHub blob URLs to `https://truvag3.dev/blogs/…` and `https://truvag3.dev/whitepapers/…`.
19. **⚠ Still pending.** Decommission the Docusaurus glassmorphism landing: replace `docs-site/src/pages/index.tsx` with a minimal redirect-or-welcome page that either redirects to `/docs/intro` or shows a one-line "Welcome — try the docs" page. (The website now owns the homepage role.) As of 2026-05-21 [docs-site/src/pages/index.tsx](../docs-site/src/pages/index.tsx) still renders the Hero + WhyTruvaG3 components — duplicating content on `truvag3.dev`.
20. Update `docs-site/docusaurus.config.ts` navbar to add a "Blog" link → `https://truvag3.dev/blogs/` and a "Whitepapers" link → `https://truvag3.dev/whitepapers/`.

### Phase 5 — Build the actual homepage (variable: 2–5 days) — ✓ Complete

21. **This is the largest content task.** Design and implement `www/index.html` per the [Website requirements](#website-requirements) above. Source content from README.md, ARCHITECTURE.md, FRAMEWORK_FEATURES_GUIDE.md, and the architecture article.
22. Build the supporting CSS: `www/assets/css/landing.css` and any additional brand styling.
23. Iterate using Cloudflare's per-branch preview deploys (push to a feature branch, get an automatic preview URL, share or self-review).

### Phase 6 — Polish surrounding pages (~1 day) — ✓ Largely complete (article.css extraction in step 24 not verified)

24. Standardize the blog template using `www/blogs/microagents-architecture.html` as the reference (already at this path after Phase 1 migration). Extract shared CSS into `www/assets/css/article.css`.
25. Build `www/blogs/index.html` listing all blog posts.
26. Build `www/whitepapers/index.html` listing all whitepapers.
27. Ensure the second blog (90% done) is finished and added to `www/blogs/`.
28. Add a navbar/header to the website surface that's consistent across index, blogs, whitepapers.

### Phase 7 — Launch readiness (~1 day) — ◐ Partial (verify each step before announcing)

29. **Pre-launch gate — flip the GitHub repo to public.** Required before attaching either Cloudflare custom domain. The following features all depend on anonymous read access; missing this step ships visibly broken UX:
    - **Edit-on-GitHub** links on every docs page (Docusaurus `editUrl` config at [docs-site/docusaurus.config.ts:51](../docs-site/docusaurus.config.ts#L51) — leads to a sign-in / 404 page for private repos)
    - **~601 GitHub blob URLs** from the cross-source link rewrite (the doc set explicitly links out to source files; all return 404 for anonymous visitors on a private repo)
    - **Module-level README links** in `docs/getting-started.md` (`core/README.md`, `ai/README.md`, etc. — all blob URLs)
    - **Architecture article link** (until migrated to `www/`)
    - **GitHub stars badge** on the website homepage (returns error / zero for private repos)
    
    Verify each works by visiting the live `docs.truvag3.dev` in a fresh browser session (no GitHub cookie) before announcing.

30. **Clean up known broken anchors.** Run `npm run build` in `docs-site/` — Docusaurus reports broken anchors across these files as of 2026-05-16: `building/EFFECTIVE_PROMPTS_GUIDE.md`, `building/TOOL_DEVELOPMENT_GUIDE.md`, `building/TOOL_SCHEMA_DISCOVERY_GUIDE.md`, `operations/AUTO_DISCOVERY_GUIDE.md`, `orchestration/ASYNC_ORCHESTRATION_GUIDE.md`, `orchestration/HUMAN_IN_THE_LOOP_USER_GUIDE.md`, `orchestration/INTELLIGENT_ERROR_HANDLING.md`, `overview/FRAMEWORK_FEATURES_GUIDE.md`, `reference/ENVIRONMENT_VARIABLES_GUIDE.md`. Budget ~30–60 min; warnings don't fail the build but visibly degrade docs polish.

31. Add GA4 to both surfaces (one `<script>` tag in the `www/` HTML `<head>`; Docusaurus has built-in GA4 config).

32. Add Open Graph / Twitter Card meta tags to the homepage and each blog/whitepaper.

33. Add a favicon set (16x16, 32x32, apple-touch-icon).

34. Lighthouse audit on both surfaces. Target > 90 across the board.

35. Cross-browser smoke test (Chrome, Safari, Firefox; mobile + desktop).

36. Update README.md to point to `truvag3.dev` as the canonical project URL.

### Phase 8A — Search engine discoverability (~1 hour) — ✓ Complete

The minimum to make every URL on both surfaces crawlable and submitted to Google. **Required for the "come up in search" goal.** Independent of launch — ship before or after, but **before** is preferable so launch traffic lands in a GSC that's already collecting data.

38. **Docs surface cleanup.** Delete [docs-site/src/pages/markdown-page.mdx](../docs-site/src/pages/markdown-page.mdx) (Docusaurus scaffolder demo leaking `/markdown-page` into the sitemap). Add [docs-site/static/robots.txt](../docs-site/static/robots.txt):
    ```
    User-agent: *
    Allow: /
    Sitemap: https://docs.truvag3.dev/sitemap.xml
    ```
    Confirm `trailingSlash: true` is set in [docs-site/docusaurus.config.ts](../docs-site/docusaurus.config.ts) — this should already have landed in Phase 2 step 7, but if you skipped it, set it now and sweep any `docs.truvag3.dev/docs/<page>` links across `www/` to add the trailing slash so external references match the canonical form.

39. **Website sitemap generator.** Create [scripts/generate-www-sitemap.py](../scripts/generate-www-sitemap.py): walks `www/**/*.html`, emits `www/sitemap.xml`. Run once, commit script and output. Document the regenerate workflow in the script's header comment ("run before committing new blogs/whitepapers").

    **URL canonicalization rules** (apply exactly — Google treats `/blogs/` and `/blogs/index` as duplicate content if both leak into the sitemap):
    - `www/index.html` → `https://truvag3.dev/`
    - `www/<folder>/index.html` → `https://truvag3.dev/<folder>/` (trailing slash)
    - `www/<folder>/<slug>.html` → `https://truvag3.dev/<folder>/<slug>` (no `.html`, no trailing slash)
    
    The last rule matches the canonical form already established in Phase 1 step 3 (CF Pages serves the `.html` file at its extensionless path).
    
    **`lastmod` source:** per-file from git — `git log -1 --format=%cI <file>`. Falls back to `git log -1 --format=%cI` (repo HEAD) for files not yet committed.

40. **Website robots.txt.** Create [www/robots.txt](../www/robots.txt):
    ```
    User-agent: *
    Allow: /
    Sitemap: https://truvag3.dev/sitemap.xml
    ```

41. **Google Search Console.** Sign in at https://search.google.com/search-console. Add `truvag3.dev` as a **Domain property**; verify via DNS TXT record at your DNS provider. Once verified, submit both sitemaps under **Sitemaps**:
    - `https://truvag3.dev/sitemap.xml`
    - `https://docs.truvag3.dev/sitemap.xml`

42. **Bing Webmaster Tools.** Add the same domain, verify, submit the same two sitemap URLs. ~5 minutes for non-trivial additional reach.

43. **Validate after deploy.** Single end-of-phase check — run all of these in one pass after the last CF Pages deploy completes:
    ```bash
    # All four files return 200
    curl -sI https://truvag3.dev/sitemap.xml          | head -1
    curl -sI https://truvag3.dev/robots.txt           | head -1
    curl -sI https://docs.truvag3.dev/sitemap.xml     | head -1
    curl -sI https://docs.truvag3.dev/robots.txt      | head -1
    
    # Docs sitemap no longer leaks /markdown-page
    curl -s https://docs.truvag3.dev/sitemap.xml | grep -o "<url>" | wc -l   # expect 32, not 33
    
    # Every URL in BOTH sitemaps actually returns 200 direct (no 307 redirects).
    # Extract only <loc> values so XML namespace URLs aren't tested; works on
    # both single-line and multi-line sitemap XML. If you see any 307s here
    # the trailingSlash config is missing — see Phase 2 step 7.
    for SM in https://truvag3.dev/sitemap.xml https://docs.truvag3.dev/sitemap.xml; do
      echo "--- $SM ---"
      curl -s "$SM" \
        | grep -oE '<loc>[^<]+' \
        | sed 's:<loc>::' \
        | xargs -I{} curl -sI -o /dev/null -w "%{http_code} {}\n" {} \
        | sort | uniq -c
    done
    ```
    Then in GSC: open URL Inspection on 3–5 key pages (homepage, `/docs/intro/`, both blog posts, one whitepaper) and **Request indexing**. Confirm "Success" beside each submitted sitemap (first crawl typically 24–48h; full indexing of new domain can take days-to-weeks).

### Phase 8B — Search and social appearance polish (~1.5 hours) — optional

These improve *how pages appear* in search results and on social previews. **None are required for basic Google visibility**; defer if the priority is just "come up in search". The honest sequence: complete 8A, confirm pages are getting indexed, *then* invest here.

44. **Page metadata audit on website.** Each `www/**/*.html` page should have:
    - `<title>` — unique, descriptive
    - `<meta name="description">` — under 160 chars (most influential non-sitemap signal for Google snippet quality)
    - `<link rel="canonical" href="…">` — **must use exactly the same URL form as the sitemap entry** (same trailing-slash and `.html` choices per Phase 8A step 39)
    
    Audit by `view-source:` on the homepage + a representative blog post + a representative whitepaper.

45. **Social previews (Open Graph / Twitter Card).** Add `og:title`, `og:description`, `og:url`, `og:image`, and `twitter:card` to each `www/**/*.html` page. Produce default OG card at `www/assets/img/og-card.png` (1200×630). Docusaurus pages already emit OG meta from `themeConfig.image` and per-page frontmatter — but the current `themeConfig.image` points at the Docusaurus scaffolder card (generic "Docusaurus" branding), so replace [docs-site/static/img/docusaurus-social-card.jpg](../docs-site/static/img/docusaurus-social-card.jpg) with a TruvaG3-branded image of the same dimensions; same path means no config change.

### Launch

46. Announcement: HN Show HN, Reddit r/golang, relevant Go newsletters. (Same as the original Phase 7.)

### Total estimate

- Phases 1–4 (everything except website content): **~2 working days**.
- Phase 5 (homepage design + build): **2–5 days**, dominant variable.
- Phases 6–7 (polish + launch): **~2 working days**.
- Phase 8A (search engine discoverability): **~1 hour** + GSC verification wait (DNS TXT propagates in minutes, first crawl in 24–48h).
- Phase 8B (search/social appearance polish): **~1.5 hours** — optional, defer until 8A confirms indexing.
- **End-to-end: ~6–10 working days** for a solo maintainer.

This is comparable to the original strategy's 10–14 day estimate, but with a meaningfully better end state because the website surface isn't constrained by Docusaurus.

---

## Maintenance equation

| Activity | Frequency | Time |
|---|---|---|
| Edit a doc | per PR | 0 extra |
| Publish a new blog post | as needed | ~30 min (write HTML, drop in `www/blogs/`, update index) |
| Publish a new whitepaper | as needed | ~10 min (copy HTML, update index) |
| Regenerate `www/sitemap.xml` after adding a blog or whitepaper | as needed | ~10s (`python3 scripts/generate-www-sitemap.py`) |
| Docusaurus minor `npm update` in `docs-site/` | quarterly | ~10 min |
| Cloudflare Pages config drift | rare | ~5 min |
| Brand-token sync between `docs-site/` and `www/` | when changed | manual; script-able later if it becomes a problem |
| GA4 dashboards | weekly glance | ~5 min |
| Glance at GSC for crawl errors / coverage | weekly | ~5 min |

**Total ongoing maintenance: ~1–2 hours per month**, same as the original Solo Developer Minimal Track estimated, dominated by writing actual content (which you'd do anyway).

---

## Trade-offs and open decisions

### Acknowledged trade-offs

1. **Two surfaces = two design responsibilities.** The website's CSS is hand-maintained. Drift between website and docs brand styling is possible. Mitigation: keep brand tokens in a single CSS file, mirrored verbatim.
2. **Cloudflare is a vendor dependency.** Free tier is generous and stable, but corporate priorities can shift. Migration cost off Cloudflare Pages is low (repo is unchanged; you change DNS), but switching CDNs is friction.
3. **No CMS for blog posts.** Adding a post means writing HTML. For a low-cadence blog (1–2 posts/month) this is fine. If the blog grows beyond ~20 posts and edit cadence matters, consider migrating blogs into Docusaurus's `blog` plugin (already supported, just disabled in `docs-site/docusaurus.config.ts`).
4. **Lose the Docusaurus landing's React reactivity.** The website is static HTML — no dynamic GitHub stars badge fetched at page load, no live announcement banners. Workarounds: hard-code stars and refresh occasionally; or use Cloudflare Workers for dynamic content (more setup).
5. **Brand-color tokens duplicated.** `docs-site/src/css/custom.css` and `www/assets/css/brand.css` both define the same `--brand-truva*` and `--brand-g3*` tokens. Edits to brand need two file edits.

### Open decisions (mark these explicit, do not assume defaults)

| Decision | Options | Default |
|---|---|---|
| DNS strategy | D1 (move to Cloudflare) or D2 (keep at current registrar) | _Undecided_ |
| Apex CNAME vs ALIAS support at current DNS provider | Verify support before committing to D2 | _Undecided_ |
| Website analytics | GA4, Plausible (paid), Cloudflare Web Analytics (free, lightweight) | _Undecided_ |
| Per-blog-post comments | None (default) vs Disqus (heavy) vs Giscus (GitHub Discussions) | None at launch |
| Open Graph card generation | Hand-author per page vs auto-generate via screenshot service | Hand-author |
| Blog/whitepaper index — manual or auto-generated | Manual at low volume; script-generated past ~10 entries | Manual at launch |
| Disposition of Docusaurus glassmorphism landing | Replace with redirect to `/docs/intro` vs keep as docs-site welcome page | _Undecided_ |
| Docs-site search | Algolia DocSearch (free, 3–5 day approval wait) vs `@easyops-cn/docusaurus-search-local` plugin (instant, on-device) vs none until traffic justifies | _None at launch_ |
| Repo visibility | Private vs public on GitHub | **Decided: public** — flipped in Phase 7 step 29, before Cloudflare custom-domain attachment. Currently private (anonymous GET to `github.com/truvaagents/truva-g3` returns 404). |
| Sitemap generator timing for www/ | Manual before commit (output committed) vs CF Pages build step | Manual at launch — preserves www/'s "no build step" property |
| Sitemap `lastmod` source | Run-time date vs `git log -1 --format=%cI <file>` per file | `git log` per file — strictly more honest, no extra ongoing cost; "today" advances `lastmod` on unchanged files every regen |
| GSC property type | Domain property (DNS TXT verification) vs two URL-prefix properties | Domain property |
| Submit to Bing Webmaster Tools | Yes / no | Yes |

### What this revision does NOT change

- The framework itself. Zero TruvaG3 Go code changes.
- The docs content. All 30 markdown files render identically.
- The `prebuild` hook that syncs `GETTING_STARTED.md`.
- The link-rewrite scripts.
- The two-tone brand mark in the Docusaurus navbar.
