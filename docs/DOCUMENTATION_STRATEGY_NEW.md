# TruvaG3 Documentation Strategy (Two-Surface, NEW)

> **Status:** active. This document supersedes [DOCUMENTATION_STRATEGY.md](./DOCUMENTATION_STRATEGY.md) for the **publishing architecture**. The original strategy doc remains a valid reference for individual concerns (Docusaurus mechanics, the 601-link rewrite script, MDX gotchas, deployment configuration patterns).
>
> **Last updated:** 2026-05-16.

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
10. [Execution plan](#execution-plan)
11. [Maintenance equation](#maintenance-equation)
12. [Trade-offs and open decisions](#trade-offs-and-open-decisions)

---

## Why this revision

The original strategy assumed a single Docusaurus site at `truvag3.dev` with subpaths for docs, blog, examples, and whitepapers. That works for projects whose content is mostly reference docs with light supporting material. TruvaG3's content shape is different:

1. **Article-style long-form already exists and works.** `www/blogs/micro-agents-architecture.html` (migrated from `docs/blogs/` in Phase 1) is a 1389-line standalone HTML article with custom typography (760px max-width, 17px body, 1.65 line-height, custom aside/code styles). Re-implementing this in MDX would lose the magazine-style polish that the format actually benefits from.
2. **Whitepapers are an entire content category.** A handful of HTML whitepapers live at `~/Documents/Documents/TruvaG3/whitepapers/` and need a home. Each is standalone, formatted, self-contained.
3. **The Docusaurus homepage is structurally constrained.** A serious framework landing typically needs: code-snippet hero, architecture diagrams, customer/showcase signals, recent blog posts, GitHub stars badge, registry-viewer screenshots, multi-section narrative. None of those are comfortable to author in a Docusaurus `index.tsx` — every visual choice fights the underlying theme.
4. **One author = no coordination tax.** Splitting surfaces only makes sense when the team can afford the operational overhead. A solo maintainer with a single Cloudflare Pages account can ship and maintain two surfaces with no more friction than one — provided the boundary is clean.

The principle the original doc named ([line 304](./DOCUMENTATION_STRATEGY.md#L304)) — *"different aesthetics across surfaces is correct, not a bug"* — is right. This revision takes it more literally: two surfaces, two physical sites, no shared theme.

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

- `truvag3.dev` navbar → "Docs" link → `docs.truvag3.dev/docs/intro`
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
│   ├── DOCUMENTATION_STRATEGY.md          # original strategy (reference)
│   └── DOCUMENTATION_STRATEGY_NEW.md      # this file
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
│   │   ├── micro-agents-architecture.html # migrated from docs/blogs/
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
   - Primary CTA: "Get started" → `docs.truvag3.dev/docs/intro`
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

Currently embodied by `www/blogs/micro-agents-architecture.html`. Worth standardizing into a shared template:

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

## Execution plan

Sequenced for a solo maintainer. Phases are independent enough that pausing between any two is safe.

### Phase 1 — Repo scaffold (~1 hour)

1. Create `www/` at repo root with the structure above.

2. Move `docs/blogs/micro-agents-architecture.html` and its asset PNGs/JSONs into `www/blogs/`.

3. **Sweep every reference to `docs/blogs/` and to `micro-agents-architecture` in the repo** — not just the README. Run:

   ```bash
   rg "docs/blogs|micro-agents|truvag3-introduction" --type=md --type=html
   ```

   Confirmed reference sites (as of 2026-05-16):
   - [README.md:8](../README.md#L8) — micro-agents link in the "About this framework" blockquote
   - [README.md:587](../README.md#L587) — micro-agents link in the Guides section
   - [README.md:588](../README.md#L588) — link to `docs/blogs/truvag3-introduction.html`, **a file that does not currently exist** (pre-existing dead link; either restore the file before launch or delete the line)
   - [README.md:681](../README.md#L681) — micro-agents link in Next Steps
   
   Update each to point at the new website URL (`https://truvag3.dev/blogs/micro-agents-architecture` once live — no trailing slash, since Cloudflare Pages serves the `.html` file at its extensionless path; GitHub blob URL for the interim). Don't ship the dead `truvag3-introduction.html` link to launch.

4. **Inventory the whitepapers and stage them in the repo.** The source folder `~/Documents/Documents/TruvaG3/whitepapers/` is a personal-machine dependency — Cloudflare's build runner cannot read it, and no other contributor can find it. Required steps:
   - List exact filenames (and any sub-asset dependencies) once.
   - Copy each into `www/whitepapers/` and commit. From this point forward the repo is the source of truth.
   - Sanity-check each file for embedded absolute paths (`/Users/…`, file:// URLs, machine-specific font paths) and replace with repo-relative or web-relative paths.
   - **Until inventoried,** every reference to "whitepapers" in this plan is conditional on you doing this one-time staging. Don't proceed past Phase 3 with whitepapers still living outside the repo.

5. Create minimal placeholder `www/index.html` (just enough to verify deployment): brand mark + "Site under construction — see [docs](https://docs.truvag3.dev)" link.

6. Add `www/assets/css/brand.css` mirroring the brand tokens from `docs-site/src/css/custom.css`.

### Phase 2 — Cloudflare Pages — docs surface (~30 min)

> **Order matters.** Update Docusaurus config *before* the first Cloudflare deploy. The current `baseUrl: '/truva-g3/'` is correct only for GitHub Pages — leaving it in place during the first `pages.dev` deploy produces an unstyled site (asset paths break because `pages.dev` serves at `/`, not `/truva-g3/`).

7. **Update `docs-site/docusaurus.config.ts` first:**
   - `baseUrl: '/'` (was `'/truva-g3/'`)
   - `url: 'https://docs.truvag3.dev'` (was `'https://truvaagents.github.io'`)
   - Commit and push.
   - **Until the custom domain is live**, the local dev server URL changes — `npm run start` now serves at `http://localhost:3000/` (no `/truva-g3` prefix).
8. Push repo to GitHub if not already pushed.
9. Create Cloudflare account.
10. Create Pages project `truva-g3-docs`: connect to repo, root directory `docs-site/`, build command `npm install && npm run build`, output `build`.
11. First deploy lands at `https://truva-g3-docs.pages.dev/`. **Verify the site renders with styles and links work before adding the custom domain.** (If you skipped step 7, you'll see unstyled pages here — that's the failure mode the ordering prevents.)
12. Add custom domain `docs.truvag3.dev`. Add DNS record per your chosen DNS strategy (D1 or D2). Wait for SSL cert (~5 min).
13. Verify `docs.truvag3.dev/docs/intro` resolves and renders styled.

### Phase 3 — Cloudflare Pages — website surface (~10 min)

14. Create Pages project `truva-g3-www`: connect to same repo, root directory `www/`, no build command, output `.`.
15. First deploy at `https://truva-g3-www.pages.dev/` — should show the placeholder index page.
16. Add custom domain `truvag3.dev`. DNS record. Wait for cert.
17. Verify `truvag3.dev` resolves and shows the placeholder.

### Phase 4 — Cross-link cleanup (~30 min)

18. Update `docs/intro.md` and any other docs-site pages that link to the architecture article or whitepapers — change GitHub blob URLs to `https://truvag3.dev/blogs/…` and `https://truvag3.dev/whitepapers/…`.
19. Decommission the Docusaurus glassmorphism landing: replace `docs-site/src/pages/index.tsx` with a minimal redirect-or-welcome page that either redirects to `/docs/intro` or shows a one-line "Welcome — try the docs" page. (The website now owns the homepage role.)
20. Update `docs-site/docusaurus.config.ts` navbar to add a "Blog" link → `https://truvag3.dev/blogs/` and a "Whitepapers" link → `https://truvag3.dev/whitepapers/`.

### Phase 5 — Build the actual homepage (variable: 2–5 days)

21. **This is the largest content task.** Design and implement `www/index.html` per the [Website requirements](#website-requirements) above. Source content from README.md, ARCHITECTURE.md, FRAMEWORK_FEATURES_GUIDE.md, and the architecture article.
22. Build the supporting CSS: `www/assets/css/landing.css` and any additional brand styling.
23. Iterate using Cloudflare's per-branch preview deploys (push to a feature branch, get an automatic preview URL, share or self-review).

### Phase 6 — Polish surrounding pages (~1 day)

24. Standardize the blog template using `www/blogs/micro-agents-architecture.html` as the reference (already at this path after Phase 1 migration). Extract shared CSS into `www/assets/css/article.css`.
25. Build `www/blogs/index.html` listing all blog posts.
26. Build `www/whitepapers/index.html` listing all whitepapers.
27. Ensure the second blog (90% done) is finished and added to `www/blogs/`.
28. Add a navbar/header to the website surface that's consistent across index, blogs, whitepapers.

### Phase 7 — Launch readiness (~1 day)

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

### Launch

37. Announcement: HN Show HN, Reddit r/golang, relevant Go newsletters. (Same as the original Phase 7.)

### Total estimate

- Phases 1–4 (everything except website content): **~2 working days**.
- Phase 5 (homepage design + build): **2–5 days**, dominant variable.
- Phases 6–7 (polish + launch): **~2 working days**.
- **End-to-end: ~6–10 working days** for a solo maintainer.

This is comparable to the original strategy's 10–14 day estimate, but with a meaningfully better end state because the website surface isn't constrained by Docusaurus.

---

## Maintenance equation

| Activity | Frequency | Time |
|---|---|---|
| Edit a doc | per PR | 0 extra |
| Publish a new blog post | as needed | ~30 min (write HTML, drop in `www/blogs/`, update index) |
| Publish a new whitepaper | as needed | ~10 min (copy HTML, update index) |
| Docusaurus minor `npm update` in `docs-site/` | quarterly | ~10 min |
| Cloudflare Pages config drift | rare | ~5 min |
| Brand-token sync between `docs-site/` and `www/` | when changed | manual; script-able later if it becomes a problem |
| GA4 dashboards | weekly glance | ~5 min |

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

### What this revision does NOT change

- The framework itself. Zero TruvaG3 Go code changes.
- The docs content. All 30 markdown files render identically.
- The `prebuild` hook that syncs `GETTING_STARTED.md`.
- The link-rewrite scripts.
- The two-tone brand mark in the Docusaurus navbar.

---

## Reference: the original strategy

The original [DOCUMENTATION_STRATEGY.md](./DOCUMENTATION_STRATEGY.md) remains valid as a reference for:

- Docusaurus setup mechanics (sections "Phase 1 — Pipeline smoke test", "Docusaurus Configuration", "Markdown compatibility cleanups")
- The 601-link rewrite procedure and Python script
- MDX gotchas (curly braces in headings)
- Multi-release management (when versioning becomes relevant)
- Site-map decisions and surface model

Treat the original as a deep reference; treat this NEW doc as the canonical execution plan going forward.
