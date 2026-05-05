# `<TOOL NAME>` — a TruvaG3 Tool

> **Template scaffold.** This README is a placeholder. After your coding
> agent populates the tool from [`PROMPT.md`](PROMPT.md), replace this
> file with a real description. Use the canonical references below as a
> writing template:
>
> - API-backed: [`examples/stock-market-tool/README.md`](../stock-market-tool/README.md)
> - Stdlib-only: [`examples/system-utilities-tool/README.md`](../system-utilities-tool/README.md)

---

## What this tool does

`<one-paragraph description — what data or operations does it expose, and
which agents would call it?>`

## Capabilities

| Name | Description | Required input | Returns |
|------|-------------|----------------|---------|
| `<capability_1>` | `<what it does>` | `<key fields>` | `<key output fields>` |
| `<capability_2>` | `<what it does>` | `<key fields>` | `<key output fields>` |

## How to run

```bash
# 1. Configure (only if your tool needs API keys / external services)
cp .env.example .env
# Edit .env, set any required keys

# 2. Deploy into the existing kind cluster (assumes cluster + infra are up)
./setup.sh deploy

# 3. Verify the tool registered with the framework registry
curl -s http://travel-chat-agent.localhost/discover | \
  jq '.tools[] | select(.name=="<tool-name>")'

# 4. Test a capability directly via port-forward
./setup.sh test
```

## Required reading for contributors

Before changing this tool, skim:

- [`docs/building/TOOL_DEVELOPMENT_GUIDE.md`](../../docs/building/TOOL_DEVELOPMENT_GUIDE.md)
- [`docs/building/TOOL_SCHEMA_DISCOVERY_GUIDE.md`](../../docs/building/TOOL_SCHEMA_DISCOVERY_GUIDE.md)
- [`docs/observability/DISTRIBUTED_TRACING_GUIDE.md`](../../docs/observability/DISTRIBUTED_TRACING_GUIDE.md)
- [`docs/observability/LOGGING_IMPLEMENTATION_GUIDE.md`](../../docs/observability/LOGGING_IMPLEMENTATION_GUIDE.md)

## Configuration

| Env var | Purpose | Default |
|---------|---------|---------|
| `PORT` | HTTP server port | `8390` |
| `REDIS_URL` | Redis URL for service discovery | `redis://localhost:6379` |
| `NAMESPACE` | Kubernetes namespace | `truvag3-examples` |
| `TRUVAG3_LOG_LEVEL` | Log level (`debug`, `info`, `warn`, `error`) | `info` |
| `TRUVAG3_LOG_FORMAT` | `json` or `text` | `json` |
| `OTEL_EXPORTER_OTLP_ENDPOINT` | OpenTelemetry collector | `http://otel-collector.truvag3-examples:4318` |
| `<YOUR_API_KEY>` | `<add any API keys / external creds your tool needs>` | — |

See [`.env.example`](.env.example) for the full list with comments.
