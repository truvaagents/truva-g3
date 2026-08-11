# Contributing to TruvaG3

Thank you for your interest in contributing to TruvaG3! Contributions for bug fixes and documentation updates are welcome.

## Table of Contents

- [Code of Conduct](#code-of-conduct)
- [Getting Started](#getting-started)
- [How to Contribute](#how-to-contribute)
- [Development Setup](#development-setup)
- [Project Layout](#project-layout)
- [Coding Standards](#coding-standards)
- [Testing Guidelines](#testing-guidelines)
- [Pre-commit Gates](#pre-commit-gates)
- [Submitting Changes](#submitting-changes)
- [Issue Guidelines](#issue-guidelines)
- [License](#license)
- [Getting Help](#getting-help)
- [Recognition](#recognition)

## Code of Conduct

By participating in this project, you agree to abide by our Code of Conduct:
- Be respectful and inclusive
- Welcome newcomers and help them get started
- Focus on constructive criticism
- Accept responsibility for mistakes

## Getting Started

1. Fork the repository on GitHub
2. Clone your fork locally:
   ```bash
   git clone https://github.com/YOUR-USERNAME/truva-g3.git
   cd truva-g3
   ```
3. Add the upstream repository:
   ```bash
   git remote add upstream https://github.com/truvaagents/truva-g3.git
   ```
4. Enable the project's pre-commit hook for secret detection (see [Pre-commit Gates](#pre-commit-gates)):
   ```bash
   git config core.hooksPath .githooks
   ```

## How to Contribute

### Reporting Bugs

- Check if the bug has already been reported in [Issues](https://github.com/truvaagents/truva-g3/issues)
- Create a new issue with a clear title and description
- Include:
  - Go version (`go version`)
  - Operating system and version
  - Steps to reproduce the issue
  - Expected behavior vs actual behavior
  - Any relevant error messages or logs

### Suggesting Features

- Check existing issues for similar suggestions
- Open a new issue and suggest the `enhancement` label in the body (maintainers apply labels during triage — see [Issue Guidelines](#issue-guidelines))
- Clearly describe:
  - The problem you're trying to solve
  - Your proposed solution
  - Any alternatives you've considered
  - Potential impact on existing functionality

### Contributing Code

1. **Find an Issue**: Look for issues labeled `good first issue` or `help wanted`
2. **Comment**: Let others know you're working on it
3. **Branch**: Create a feature branch from `main`
4. **Code**: Make your changes following our [coding standards](#coding-standards)
5. **Test**: Add tests for new functionality
6. **Gate**: Run [pre-commit gates](#pre-commit-gates) locally
7. **Document**: Update documentation as needed
8. **Commit**: Use clear, descriptive commit messages (see [Commit Messages](#commit-messages))
9. **Push**: Push to your fork
10. **PR**: Create a pull request against `main`

## Development Setup

### Prerequisites

- **Go 1.26 or higher** (the module is pinned to `go 1.26.4` in `go.mod`)
- **Docker** (recommended) for running the in-tree Redis/Valkey backend used by integration tests and example deployments
- **kind** (Kubernetes-in-Docker) for running the example multi-agent system locally

The framework's service registry is pluggable behind the `core.Discovery` interface; Redis/Valkey is the in-tree default. Unit tests do not require Redis. Integration tests gated by `//go:build integration` do.

### Building and Testing

The repo is a Go workspace with seven modules — the root (`.`) plus `ai`, `core`, `memory`, `orchestration`, `resilience`, `telemetry`. Each has its own `go.mod`. Commands run from the repo root only cover the root module; to exercise everything, iterate over the workspace modules the same way CI does:

```bash
# Build and unit-test every workspace module (mirrors CI's "test" job)
for mod in . ai core memory orchestration resilience telemetry; do
  echo "── $mod ──"
  (cd "$mod" && go build ./... && go test -race -coverprofile=coverage.out -covermode=atomic ./...)
done

# Integration tests (also workspace-aware; require Redis on localhost:6379 or via REDIS_URL)
for mod in . ai core memory orchestration resilience telemetry; do
  (cd "$mod" && go test -tags integration ./...)
done

# To work on a single module, just cd into it:
cd ai && go test ./...
```

CI runs the test loop above (and the lint, vuln, and examples jobs documented under [Pull Request Process](#pull-request-process)) on every push and PR — **except** when the change touches only Markdown, files under `docs/`, or `LICENSE`. Those paths are listed under `paths-ignore` in [`.github/workflows/ci.yml`](.github/workflows/ci.yml) and do not trigger CI.

### Running Examples

Each runnable agent and tool example ships with a standardized `setup.sh` that provisions it into a local kind cluster, plus a `k8-deployment.yaml` for production-style deploys. Support directories like `examples/k8-deployment/`, `examples/mock-services/`, and `examples/slack-gateway/` are not standalone examples — see the documentation file inside each (e.g., the README for `k8-deployment` and `mock-services`, or [`GATEWAY_DEVELOPMENT_GUIDE.md`](examples/slack-gateway/GATEWAY_DEVELOPMENT_GUIDE.md) for `slack-gateway`).

Browse [`examples/`](examples/) for the available agents and tools.

```bash
# Quickest single-agent example (uses Redis from its own deployment)
cd examples/agent-example
./setup.sh

# Multi-agent orchestration example
cd examples/agent-with-orchestration
./setup.sh
```

Tear-down is a counterpart `setup.sh cleanup` (or, in older examples, by deleting the kind cluster).

## Project Layout

TruvaG3 uses a flat top-level package layout — there is no `pkg/` directory. Each module is its own importable package:

```
truva-g3/
├── core/             # Foundation: interfaces, base types, config, discovery primitives
├── ai/               # LLM clients, embeddings, intelligent agent
├── memory/           # Cross-agent shared memory + per-user memory backends
├── orchestration/    # Workflow engine, planners, pipeline hooks, executors
├── resilience/       # Circuit breakers, retry, timeout policies
├── telemetry/        # OpenTelemetry integration (implements core.Telemetry)
├── examples/         # Runnable agents and tools
├── docs/             # Architecture, guides, API reference
└── .github/          # CI workflows, PR template, CODEOWNERS, dependabot
```

Each module has its own `ARCHITECTURE.md` describing internal structure. A new file generally lives in the module whose interface it implements; see [`FRAMEWORK_DESIGN_PRINCIPLES.md`](FRAMEWORK_DESIGN_PRINCIPLES.md) §"Module Architecture" for the dependency rules — most importantly, `core` does **not** import any optional module.

## Coding Standards

### Read These First

Project-specific architectural rules (compile-time tool/agent separation, layered composition, Runnable lifecycle, etc.) live in:

- [`FRAMEWORK_DESIGN_PRINCIPLES.md`](FRAMEWORK_DESIGN_PRINCIPLES.md) — framework-wide design principles
- [`core/ARCHITECTURE.md`](core/ARCHITECTURE.md) — core module contracts
- `<module>/ARCHITECTURE.md` — per-module internal design

Cross-cutting implementation guides — these contain prescriptive rules that apply across modules. Conformance is checked in review:

- [`docs/observability/DISTRIBUTED_TRACING_GUIDE.md`](docs/observability/DISTRIBUTED_TRACING_GUIDE.md) — tracing patterns, context propagation, server/client middleware, and §"Required Patterns for Framework-Level Tracing".
  **Triggers**: any code path that emits spans or propagates trace context.
- [`docs/observability/LOGGING_IMPLEMENTATION_GUIDE.md`](docs/observability/LOGGING_IMPLEMENTATION_GUIDE.md) — logger interface usage, log levels, structured field naming standards, and §"Required Patterns for Framework-Level Logging".
  **Triggers**: any code path that emits logs.
- [`telemetry/ARCHITECTURE.md`](telemetry/ARCHITECTURE.md) — especially §"Module Boundaries for Metrics", §"HTTP Middleware Extensibility", and §"Common Pitfalls".
  **Triggers**: changes that emit metrics (`telemetry.Counter` / `Histogram` / `Gauge`), add or rename a metric in `telemetry/unified_metrics.go`, alter telemetry initialization or shutdown in `main()`, or add HTTP middleware that interacts with tracing.

Self-review your diff against the relevant guides before opening the PR. Reviewers will check the same.

PRs that violate these will be flagged in review.

### Go Code Style

- Follow [Effective Go](https://go.dev/doc/effective_go) and the [Go Code Review Comments](https://go.dev/wiki/CodeReviewComments)
- Format with `gofmt` and `goimports`
- Pass `go vet ./...`
- Pass `golangci-lint run` (see [`.golangci.yml`](.golangci.yml) — the project enables `gosec` and `misspell` on top of the standard set)
- Pass `gosec ./...` for security lint
- Pass `govulncheck ./...` for known-CVE checks against dependencies
- Keep functions small and focused
- Write clear, self-documenting code; default to no comments unless a non-obvious *why* needs explaining
- Add godoc on exported types and functions

### Naming Conventions

- Use descriptive, meaningful names
- Follow Go naming conventions:
  - Exported identifiers start with capital letters
  - Unexported identifiers start with lowercase
  - Acronyms keep their case (e.g., `HTTPServer`, `URLParser`, `LLMClient`)
  - Single-method interfaces typically end with `-er`

### Error Handling

- Always check returned errors
- Wrap with context using `fmt.Errorf("...: %w", err)`
- Use the project's `core.ToolError` and `core.ClassifyUpstreamError` for tool-side failures (see [`core/tool_error.go`](core/tool_error.go))
- Surface actionable messages — name the env var or config field that would fix it when relevant

## Testing Guidelines

### Coverage and Style

- Add unit tests in the same change for every new production behavior that can
  be isolated. Include small public adapters and defaulting wrappers—their
  contract is still production behavior. Do not manufacture tests for
  declarations or platform-unreachable branches; record that rationale in the
  review instead.
- Aim for at least 80% test coverage on new code
- Cover both positive and negative paths, plus edge cases
- Use table-driven tests for parameterized logic
- Treat coverage percentages as supporting evidence, not a replacement for an
  assertion that fails when the intended behavior regresses.

```go
func TestFunctionName(t *testing.T) {
    tests := []struct {
        name    string
        input   InputType
        want    OutputType
        wantErr bool
    }{
        {name: "successful case", input: validInput, want: expectedOutput},
        {name: "error case", input: invalidInput, wantErr: true},
    }

    for _, tt := range tests {
        t.Run(tt.name, func(t *testing.T) {
            got, err := FunctionToTest(tt.input)
            if (err != nil) != tt.wantErr {
                t.Errorf("FunctionToTest() error = %v, wantErr %v", err, tt.wantErr)
                return
            }
            if !reflect.DeepEqual(got, tt.want) {
                t.Errorf("FunctionToTest() = %v, want %v", got, tt.want)
            }
        })
    }
}
```

### Integration Tests

Integration tests live **alongside** unit tests inside each module — there is no top-level `test/` directory. Gate them with the `integration` build tag:

```go
//go:build integration

package core

// ... test code requiring Redis, network, etc.
```

Use `//go:build integration` for new files. Some pre-existing test files in the repo carry both lines (`//go:build integration` plus the legacy `// +build integration`) for compatibility with pre-Go-1.17 toolchains, but new code only needs the modern form. `gofmt` does not auto-add or auto-remove the legacy line.

Run them per workspace module — same loop pattern as for unit tests:

```bash
for mod in . ai core memory orchestration resilience telemetry; do
  (cd "$mod" && go test -tags integration ./...)
done
```

These typically expect Redis on `localhost:6379`. Override with `REDIS_URL=redis://host:port` if needed.

### Conformance Suites

Provider-neutral interfaces ship with executable conformance suites. Task
delivery contracts live under [`core/conformance/`](core/conformance/) (for
example, `RunTaskConsumerConformance` and
`RunTaskDeliveryProfileConformance`). Orchestration persistence, coordination,
and lock contracts live in the test-only
[`orchestration/backendconformance/`](orchestration/backendconformance/). If you
implement a new backend, run every suite that corresponds to the capabilities
it supplies. Provider tests may import these packages; runtime code must not.

## Pre-commit Gates

Before opening a PR, every Go-touching change must pass the local contributor gate set. This is a **superset** of CI: it includes everything CI checks plus a few standalone tools that are redundant with `golangci-lint` (which already runs `govet`, `gosec`, `misspell`, and others per [`.golangci.yml`](.golangci.yml)) but are useful as fast targeted checks. The PR template at [`.github/pull_request_template.md`](.github/pull_request_template.md) lists them as a checklist:

```bash
# Run each over every workspace module (use the same per-module loop shown above).
go vet ./...                                              # redundant with golangci-lint's govet, but quick
go build ./...                                            # CI runs this in its "test" job
go test -race -coverprofile=coverage.out -covermode=atomic ./...  # CI runs this in its "test" job
goimports -l . | tee /tmp/goimports.out && [ ! -s /tmp/goimports.out ]  # local-only
golangci-lint run                                         # CI runs this as its "lint" job (includes gosec via .golangci.yml)
gosec ./...                                               # redundant with golangci-lint, but quick
govulncheck ./...                                         # CI runs this as its "vuln" job
```

CI itself runs four jobs — `test`, `lint`, `vuln`, and `examples` (build-only) — over each workspace module. The standalone `go vet`, `goimports`, and `gosec` invocations above are not separate CI jobs; they're convenience checks that catch the same issues earlier with crisper output.

Partial passes are not acceptable — the merge gate is all green or none.

The repo also ships a secret-detection pre-commit hook at [`.githooks/pre-commit`](.githooks/pre-commit) that scans staged content for likely API keys (OpenAI, Anthropic, AWS, Slack, etc.). Activate it once per clone — works on macOS, Linux, and Windows (Git Bash, bundled with Git for Windows):

```bash
git config core.hooksPath .githooks
```

The hook runs two layers:

1. **Built-in bash patterns** — fast, no dependency; catches obvious provider key formats (`sk-proj-…`, `sk-ant-…`, `gsk_…`, `AIza…`, `AKIA…`, `ghp_…`, etc.).
2. **gitleaks** (optional but recommended) — adds ~100 maintained rules plus entropy heuristics, catching private keys, JWTs, Stripe/HuggingFace/DigitalOcean tokens, and high-entropy strings near credential keywords. Install:

   ```bash
   brew install gitleaks            # macOS / Linuxbrew
   scoop install gitleaks           # Windows (Scoop)
   choco install gitleaks           # Windows (Chocolatey)
   # Or download from: https://github.com/gitleaks/gitleaks/releases
   ```

   If gitleaks isn't installed, the hook skips layer 2 with a warning rather than blocking.

If the hook ever blocks a legitimate commit, fix the false positive (reword placeholders, or add a targeted allow-rule to `.gitleaks.toml`) rather than bypassing it with `--no-verify`.

## Submitting Changes

### Commit Messages

Use the conventional commit format:

```
<type>(<scope>): <subject>

<body>

<footer>
```

Types:
- `feat`: new feature
- `fix`: bug fix
- `docs`: documentation changes
- `style`: code style changes (formatting, etc.)
- `refactor`: code refactoring without behavior change
- `test`: test additions or changes
- `chore`: build, tooling, dependency, or auxiliary changes

Example:

```
feat(discovery): add kubernetes service discovery

Implement service discovery using the Kubernetes API to automatically
register and discover agents within a cluster.

Closes #123
```

Do not include AI/assistant attribution lines (`Co-Authored-By: <bot>`, "Generated with ...") in commit messages or PR descriptions.

### Pull Request Process

1. **Update Documentation**: Reflect any user-visible behavior change in `docs/`, the relevant `*/README.md`, and examples.
2. **Self-Review Against Cross-Cutting Guides**: Re-read your diff against any of the three cross-cutting guides whose triggers fire for this PR (see [Coding Standards → Read These First](#read-these-first) for the full trigger list):
   - [`docs/observability/DISTRIBUTED_TRACING_GUIDE.md`](docs/observability/DISTRIBUTED_TRACING_GUIDE.md) — if the change emits spans or propagates trace context.
   - [`docs/observability/LOGGING_IMPLEMENTATION_GUIDE.md`](docs/observability/LOGGING_IMPLEMENTATION_GUIDE.md) — if the change emits logs.
   - [`telemetry/ARCHITECTURE.md`](telemetry/ARCHITECTURE.md) — if the change emits metrics, modifies `telemetry/unified_metrics.go`, alters telemetry init in `main()`, or adds HTTP middleware that interacts with tracing.

   This is a review checkpoint, not a suggestion.
3. **Run Pre-commit Gates**: All gates listed in [Pre-commit Gates](#pre-commit-gates) must pass locally.
4. **Pass CI**: The repository runs four jobs on every PR:
   - **test** — `go build ./...` + `go test -race -coverprofile=coverage.out -covermode=atomic ./...` across the root module and each sub-module (`ai`, `core`, `memory`, `orchestration`, `resilience`, `telemetry`)
   - **lint** — `golangci-lint` (currently pinned to v2.11.4 in [`.github/workflows/ci.yml`](.github/workflows/ci.yml))
   - **vuln** — `govulncheck` against the current dependency tree
   - **examples** — build-only smoke check on every example to catch framework changes that break consumers
5. **Mark Ready for Review** once CI is green.
6. **Address Feedback** promptly; rebase on `main` if requested.
7. **Clean History**: squash commits if requested.

### Pull Request Template

The repo ships a PR template at [`.github/pull_request_template.md`](.github/pull_request_template.md). GitHub auto-populates new PRs with it. Its sections are:

- **Summary** — one or two sentences on what the PR does
- **Why** — motivation; link issues with `Closes #N` if applicable
- **Test plan** — commands run, scenarios covered
- **Checklist** — pre-commit gates, dependency justification, doc/example updates

Fill in every section. PRs without a test plan typically don't get reviewed.

## Issue Guidelines

The repo currently uses plain GitHub Issues — there are no formal issue templates, so be explicit in the issue body about whether you are reporting a bug, requesting a feature, asking a question, or flagging a doc problem.

### Useful Labels

- `bug` — something isn't working
- `enhancement` — feature request
- `documentation` — doc improvement
- `good first issue` — friendly for newcomers
- `help wanted` — community help welcome
- `question` — discussion / Q&A
- `duplicate` — already tracked elsewhere
- `wontfix` — out of scope

Maintainers will apply labels during triage; you are welcome to suggest one in the issue body.

## License

TruvaG3 is licensed under the [Apache License 2.0](LICENSE). By submitting a contribution, you agree to license your contribution under the same license, in accordance with §5 ("Submission of Contributions") of Apache 2.0:

> Unless You explicitly state otherwise, any Contribution intentionally submitted for inclusion in the Work by You to the Licensor shall be under the terms and conditions of this License, without any additional terms or conditions.

The project does not currently require a separate Contributor License Agreement (CLA). The Apache 2.0 inbound = outbound model is the contribution agreement.

## Getting Help

- Read the documentation under [`docs/`](docs/) — organized by topic into `overview/`, `building/`, `orchestration/`, `memory-and-chat/`, `observability/`, `operations/`, and `reference/` subdirectories
- Check [existing issues](https://github.com/truvaagents/truva-g3/issues)
- Open a new issue with the `question` label for anything unclear

## Recognition

Contributors are recognized in:

- The project README
- Release notes
- Special thanks in documentation

Thank you for contributing to TruvaG3!
