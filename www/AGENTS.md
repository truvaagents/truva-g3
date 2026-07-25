# AGENTS.md — `www/` (the marketing site)

Guidance for AI coding assistants and humans working on the static site under
`www/`. This is the file closest to the site, so it wins over the repo-root
[AGENTS.md](../AGENTS.md) for anything in this directory.

## What this is

A plain static site — no build step, no framework, no bundler. Cloudflare
serves the directory as-is (`www/wrangler.jsonc` → `assets.directory: "./"`).
Every page is hand-written HTML with its own inline `<style>`; only genuinely
cross-page assets live in `www/assets/`.

```
www/
  index.html            homepage
  blogs/                index.html + one file per post
  whitepapers/          index.html + one file per paper
  assets/css/           brand, landing, site-nav, code-highlight, toc-rail
  assets/js/            code-highlight.js, toc-rail.js
  sitemap.xml           every page, extensionless URLs
```

**URLs are extensionless in production** (`/blogs/truvag3-introduction`), but
in-page links and `sitemap.xml` conventions differ — links use `.html`, and
`og:url` / `sitemap.xml` use the extensionless form. Follow what neighbouring
entries do.

### Previewing locally

```bash
python3 -m http.server 8000 --directory www
```

The production worker resolves extensionless URLs; this plain server does not,
so browse with `.html` locally.

## Publishing a new post or paper

Copy the closest existing page and edit it. Then update **all** of these — a
missed one is the usual review finding:

| File | What to add |
|---|---|
| `www/blogs/index.html` *or* `www/whitepapers/index.html` | listing entry (whitepapers are grouped **Standalone papers** / **Multipart guides**) |
| `www/sitemap.xml` | `<loc>` (extensionless) + `<lastmod>` |
| `www/index.html` | homepage card — see below |
| the page itself | `og:url`, `og:title`, `og:description`, byline date |

**Homepage cards:** the two visible cards are deliberate anchors. Add new
entries to the overflow list (`read-expand`) and bump the "Show N more blogs"
count — do not displace the anchors.

## The section rail (`#toc-rail`)

The fixed left scroll-spy rail on long-form pages. Shared implementation:

- `assets/css/toc-rail.css`
- `assets/js/toc-rail.js`

Both files carry a full contract comment at the top. **Do not copy rail CSS or
JS into a page** — a page that wants a rail links these two files instead, and
that is the whole point of the shared assets. Only rail-enabled pages load
them; the index pages, `blogs/microagents-architecture.html`, and the multipart
guide chapters have no rail and link neither file.

### Adding a rail to a new page

Three edits. First, in `<head>` (after the other stylesheets):

```html
<link rel="stylesheet" href="/assets/css/toc-rail.css">
```

Then before `</body>`:

```html
<script src="/assets/js/toc-rail.js"></script>
```

Then the rail element itself. **One attribute — `data-rail` — configures
everything** (visual scale, and which content column the script measures to
decide when the rail would overlap the text):

**A whitepaper** — leave the element empty and the dots are derived at runtime
from that page's own `.pillrow`, reusing its targets, labels and colours:

```html
<aside id="toc-rail" data-rail="section" aria-label="Section navigation"></aside>
```

Add rail-only dots (by convention the glossary, which the pill rows omit) with
`data-rail-extra="id:Label:colour"`, semicolon-separated for several:

```html
<aside id="toc-rail" data-rail="section"
       data-rail-extra="glossary:Glossary:#c4b5fd"
       aria-label="Section navigation"></aside>
```

This is the preferred form: there is no second copy of the rail's targets,
labels and colours to keep in sync, so that class of drift is designed out —
you edit one list, not two. It is not a guarantee that the rendered rail always
matches the pill row: a pill pointing at an `id` that no longer exists is
skipped (with a console warning), so the rail will be one dot shorter than the
pill row until the stale pill is fixed.

**A blog post** — blog rails are editorial (a curated subset of the TOC, with
shortened labels and a designed hue ramp), so author the dots explicitly. Any
`.toc-dot` children you write are used as-is and derivation is skipped:

```html
<aside id="toc-rail" data-rail="blog" aria-label="Section navigation">
  <a class="toc-dot" href="#first" data-target="first"><span class="d" style="background:#9ec5ff"></span><span class="lbl">First section</span></a>
  ...
</aside>
```

Place it after `</header>` on a whitepaper, or between `</nav>` and `<article>`
on a blog post.

### Rules that are easy to get wrong

- **`data-rail="blog"` vs `"section"` is not cosmetic.** It selects the element
  the script measures — `article` for blogs, `section .container` for
  whitepapers and the homepage. Get it wrong and the rail silently stops
  auto-hiding on narrow windows. The script logs a `[toc-rail]` console warning
  when the measured element is missing, so **check the console** on a new page.
- **Do not infer the measured element.** `www/index.html` contains *both* a
  `<section class="container">` and an `<article>`; that is why the family is
  declared rather than sniffed. `data-measure="<selector>"` exists as an escape
  hatch for an unusual layout.
- **Every dot needs a real target.** `data-target` must match an `id` on the
  page. Dangling dots are dropped either way, with a console warning: a derived
  pill is skipped before the dot is built, and an authored dot is hidden once
  the script sees its target is missing.
- **Rail order must follow document order** — scroll-spy takes the last section
  above the probe line, so an out-of-order dot never activates.
- **Keep rails to roughly 14 dots.** Pills deliberately do not shrink, so the
  stylesheet tightens spacing on short viewports, and if a rail still does not
  fit the height of the window the script hides it outright rather than let it
  spill off-screen. That is a safe failure, not a good one — a page that needs
  more entries should use sub-items (below) rather than a longer flat rail.
- **Colour conventions**, followed across the existing papers: `#9ec5ff` for
  the first/intro dot, `var(--good)` for Summary/takeaways, `#c4b5fd` for the
  glossary (always last), `#fff` for neutral comparison sections, and a
  page-specific `--var` where a section has a named topic identity.

### Sub-items

When one section dominates a page, its subsections can get nested dots that
appear only while that section is in view. Mark the parent `has-subs` and the
children `sub`; the script adds the `rail-subs` marker itself:

```html
<a class="toc-dot has-subs" href="#big" data-target="big">...</a>
<a class="toc-dot sub"      href="#part-one" data-target="part-one">...</a>
```

**Only one `has-subs` group per page is supported.** The script binds every
`.sub` dot to the first `.has-subs` it finds, so a second group would reveal
all sub-dots together. It warns on the console if it sees more than one; if a
page ever genuinely needs two, the grouping logic has to be generalised first.

See `blogs/truvag3-introduction.html`, the one page that uses this.

### Verifying a rail

There is no test suite for the site. The rail is behavioural, so check it in a
browser: dots track sections while scrolling, the active label appears, hover
and keyboard `Tab` both expand the rail, and the console is free of
`[toc-rail]` warnings. Resize narrow — the rail hides below 1180px — and short,
where it tightens and, with sub-items, falls back to top-level dots.

## Conventions

- **Self-contained pages.** Page-specific CSS stays in that page's inline
  `<style>`. Promote to `assets/` only when a second page genuinely needs it.
- **No external JS/CSS beyond the existing CDN Prism** used by pages with code
  samples.
- **Dark theme throughout**, with the palette in each page's `:root`. Note the
  two families name the rule colour differently — blogs define `--rule`,
  whitepapers `--line` — and the shared rail CSS falls back across both.
- **Brand wordmark on light backgrounds** uses solid `#4A8AB8` / `#C56710`, not
  the gradient from `brand.css`.

## Docs changes

Per the repo-root [AGENTS.md](../AGENTS.md), `*.md` edits need explicit human
sign-off before commit — propose the change and stop at "ready to commit".
