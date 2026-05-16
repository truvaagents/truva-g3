# Go Package Reference

The canonical Go API reference for TruvaG3 — all exported types, interfaces,
functions, and their documentation — is hosted on **pkg.go.dev**, regenerated
automatically on every release.

**[→ pkg.go.dev/github.com/truvaagents/truva-g3](https://pkg.go.dev/github.com/truvaagents/truva-g3)**

## What's on pkg.go.dev

- **Every exported symbol** across all subpackages: `core/`, `orchestration/`,
  `memory/`, `ai/`, `tools/`, `telemetry/`, and more.
- **Doc comments** rendered from the source — same content as `go doc <pkg>`.
- **Type relationships** — interfaces and the concrete types that implement
  them, methods, embedded structs.
- **Examples** that show in the sidebar when source files include
  `Example_*` test functions.
- **Versioned views** — pick any tagged release from the version selector.

## When to use this vs. the docs site

- **You're reading guides, tutorials, or conceptual material** → stay on the
  docs site.
- **You're integrating against a specific function or type and need the exact
  signature, godoc, or example** → use pkg.go.dev.
- **You're writing code with an LLM/IDE assistant** → pkg.go.dev is the
  authoritative machine-readable surface.

## Why we don't mirror godoc here

Running `go doc` or one of the `godoc → markdown` generators would duplicate
content that pkg.go.dev already hosts, with two downsides:

1. **Drift.** A generated mirror gets stale between releases. pkg.go.dev
   re-indexes on every tag.
2. **Maintenance.** A solo maintainer can't credibly own both a CI generator
   and the docs site. pkg.go.dev does it for free.

So this page is intentionally one link. The detail lives where it stays
fresh.
