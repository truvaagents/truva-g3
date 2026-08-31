# AGENTS.md — orchestration backend portability reference

This file applies to `examples/orchestration-backend-portability/` and inherits
the repository-root and `examples/` guidance.

## Use the example's setup helper

Run `./setup.sh help` before changing cluster state. This example deliberately
uses a dedicated Kind cluster rather than the normal shared example cluster.

- `full-deploy` creates the dedicated cluster, deploys the complete stack, and
  runs conformance plus both live workflows.
- `deploy` and `rebuild` also run conformance and live verification after the
  rollout; they are not deployment-only commands.
- `conformance-test` runs the live PostgreSQL, NATS, Redis-lock, and mixed
  composition tests.
- `evidence` collects independent PostgreSQL, NATS, Redis, Kubernetes, and
  migration evidence.
- `cleanup` removes only portability-owned Kubernetes resources.
- `cleanup-all` deletes the entire dedicated portability cluster. Confirm that
  this destructive scope is intended before using it.

Do not substitute hand-written image build, Kind load, or `kubectl apply`
commands for the setup helper.

## Go commands

This example is intentionally outside the repository's `go.work`. Run local Go
commands from this directory with `GOWORK=off`, for example:

```bash
GOWORK=off go test ./...
GOWORK=off go test -race ./...
GOWORK=off go vet ./...
GOWORK=off go build ./...
```

Provider integration tests require the dedicated cluster environment and are
normally exercised through `./setup.sh conformance-test`. Ordinary `go test`
still runs adapter unit tests and skips only tests guarded by
`PORTABILITY_INTEGRATION=1`.

Keep provider SDK imports in `backends.go`, tests, or the matching
`internal/*adapter` package. Application roles must depend on framework
contracts rather than provider implementations.
