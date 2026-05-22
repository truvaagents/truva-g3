# System Utilities Tool

A TruvaG3 tool that provides self-contained system utility capabilities: date/time operations, shell + Python command execution, unique ID generation, and bounded sleep. Unlike most tools in the framework, this tool requires **no external API keys** — all capabilities are powered by Go's standard library plus a Python 3.12 slim runtime with common shell utilities, and run entirely within the container.

> **Browser automation moved.** The `stealth_browser` and `browser_test` capabilities previously lived here. They now live in [`playwright-tool`](../playwright-tool/), which already ships the Chromium runtime needed for them. Discovery by capability name still routes correctly — no agent-side changes needed.

## Table of Contents

- [How to Run This Example](#how-to-run-this-example)
  - [Prerequisites](#prerequisites)
  - [Quick Start (Recommended)](#quick-start-recommended)
  - [Step-by-Step Deployment](#step-by-step-deployment)
- [Features](#features)
- [Registered Capabilities](#registered-capabilities)
- [Architecture](#architecture)
- [Configuration](#configuration)
- [Project Structure](#project-structure)
- [Troubleshooting](#troubleshooting)

---

## How to Run This Example

This tool provides system utility capabilities that agents can discover and use. It is entirely self-contained — no API keys, no external services beyond Redis for discovery.

### Prerequisites

You need Docker, Kind, kubectl, and Go installed. See the [examples README](../README.md#2-quick-start) for detailed installation instructions for each platform.

**Quick verification:**
```bash
docker --version    # Docker 20.10+
kind --version      # kind 0.17+
kubectl version     # Client 1.25+
go version          # go1.26+
```

---

### Quick Start (Recommended)

```bash
cd examples/system-utilities-tool

# Deploy to Kubernetes (requires cluster and Redis to be running)
./setup.sh deploy
```

**What `./setup.sh deploy` does:**
1. Builds the Docker image (`python:3.12-slim` with bash, Python 3, and common shell + network utilities)
2. Loads it into the Kind cluster
3. Deploys the tool to Kubernetes (2 replicas)
4. Registers 7 capabilities with Redis for agent discovery

Once complete, set up port forwarding and test:

```bash
# Port forward
./setup.sh forward

# Test
curl http://localhost:8348/health
```

| Service | URL | Description |
|---------|-----|-------------|
| **System Utilities API** | http://localhost:8348 | Date/time, shell + Python commands, IDs, sleep |

### Step-by-Step Deployment

If you prefer to understand each step:

#### Step 1: Ensure Infrastructure is Running

The tool requires Redis for service discovery. If you haven't already set up infrastructure:

```bash
# From any example with a setup script
cd examples/stock-market-tool   # or any other example
./setup.sh cluster              # Create Kind cluster
./setup.sh infra                # Deploy Redis and observability stack
```

#### Step 2: Build and Deploy

```bash
cd examples/system-utilities-tool

# Build Docker image
./setup.sh docker-build

# Load into Kind and deploy
./setup.sh deploy

# Verify deployment
kubectl get pods -n truvag3-examples -l app=system-utilities-tool
```

#### Step 3: Test the Tool

```bash
# Port forward to access locally
kubectl port-forward -n truvag3-examples svc/system-utilities-service 8348:80

# Test current time
curl -X POST http://localhost:8348/api/capabilities/get_current_time \
  -H "Content-Type: application/json" \
  -d '{"timezone": "America/New_York"}'

# Test command execution
curl -X POST http://localhost:8348/api/capabilities/execute_command \
  -H "Content-Type: application/json" \
  -d '{"command": "echo hello world"}'

# Test ID generation
curl -X POST http://localhost:8348/api/capabilities/generate_id \
  -H "Content-Type: application/json" \
  -d '{"type": "uuid", "count": 3}'

# Test Python via execute_command
curl -X POST http://localhost:8348/api/capabilities/execute_command \
  -H "Content-Type: application/json" \
  -d '{"command": "python3 -c \"import numpy; print(numpy.__version__)\""}'
```

---

## Features

- **Date/Time Operations** - Current time in any timezone, timezone conversion, date arithmetic
- **Timezone Database** - ~80 curated IANA timezones with live offset and DST resolution
- **Shell + Python Execution** - Run commands inside an isolated `python:3.12-slim` container with bash, Python 3, common text-processing tools (`jq`, `grep`, `sed`, `awk`), and network diagnostics (`curl`, `dig`, `nc`, `ping`, `traceroute`)
- **Unique ID Generation** - UUID v4, ULID, and nanoid formats
- **Zero External Dependencies** - No API keys, no external services needed
- **Full Telemetry** - OpenTelemetry traces, metrics, and structured logging
- **Automatic Service Discovery** - Registers with Redis for agent discovery

---

## Registered Capabilities

The tool registers 7 capabilities with the service mesh:

### 1. Get Current Time (`get_current_time`)

**Endpoint:** `/api/capabilities/get_current_time`

Gets the current date and time in a specified timezone.

**Request:**
```json
{
  "timezone": "Asia/Seoul",
  "format": "iso8601"
}
```

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `timezone` | string | Yes | IANA timezone name (e.g., `Asia/Seoul`, `America/New_York`, `UTC`) |
| `format` | string | No | Output format: `iso8601` (default), `unix`, `human`, or a Go time layout |

**Response:**
```json
{
  "success": true,
  "data": {
    "timezone": "Asia/Seoul",
    "datetime": "2026-02-25T01:57:09+09:00",
    "unix_timestamp": 1771952229,
    "utc_offset": "+09:00",
    "is_dst": false,
    "abbreviation": "KST"
  }
}
```

---

### 2. Convert Timezone (`convert_timezone`)

**Endpoint:** `/api/capabilities/convert_timezone`

Converts a datetime from one timezone to another.

**Request:**
```json
{
  "datetime": "2026-02-24T15:00:00Z",
  "from_timezone": "UTC",
  "to_timezone": "Asia/Tokyo"
}
```

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `datetime` | string | Yes | ISO 8601 datetime string |
| `from_timezone` | string | Yes | Source IANA timezone name |
| `to_timezone` | string | Yes | Target IANA timezone name |

**Response:**
```json
{
  "success": true,
  "data": {
    "original": "2026-02-24T15:00:00Z",
    "converted": "2026-02-25T00:00:00+09:00",
    "from_timezone": "UTC",
    "to_timezone": "Asia/Tokyo",
    "offset_difference": "+09:00"
  }
}
```

---

### 3. List Timezones (`list_timezones`)

**Endpoint:** `/api/capabilities/list_timezones`

Lists available timezones grouped by region with current offsets.

**Request:**
```json
{
  "region": "Pacific"
}
```

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `region` | string | No | Filter by region: `Africa`, `America`, `Asia`, `Australia`, `Europe`, `Pacific`, `UTC` |

**Response:**
```json
{
  "success": true,
  "data": {
    "region": "Pacific",
    "timezones": [
      {"name": "Pacific/Auckland", "current_offset": "+13:00", "abbreviation": "NZDT"},
      {"name": "Pacific/Honolulu", "current_offset": "-10:00", "abbreviation": "HST"},
      {"name": "Pacific/Guam", "current_offset": "+10:00", "abbreviation": "ChST"}
    ]
  }
}
```

---

### 4. Date Arithmetic (`date_arithmetic`)

**Endpoint:** `/api/capabilities/date_arithmetic`

Adds or subtracts durations from a date.

**Request:**
```json
{
  "date": "2026-02-24",
  "operation": "add",
  "value": 7,
  "unit": "days"
}
```

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `date` | string | Yes | Starting date in `YYYY-MM-DD` or ISO 8601 format |
| `operation` | string | Yes | `add` or `subtract` |
| `value` | integer | Yes | Number of units to add or subtract |
| `unit` | string | Yes | `days`, `hours`, `minutes`, `weeks`, `months`, or `years` |
| `timezone` | string | No | Timezone for the calculation (default: `UTC`) |

**Response:**
```json
{
  "success": true,
  "data": {
    "original_date": "2026-02-24T00:00:00Z",
    "result_date": "2026-03-03T00:00:00Z",
    "operation": "add",
    "value": 7,
    "unit": "days",
    "days_between": 7
  }
}
```

---

### 5. Execute Command (`execute_command`)

**Endpoint:** `/api/capabilities/execute_command`

Executes a shell command inside the isolated container and returns stdout, stderr, and exit code.

**Request:**
```json
{
  "command": "echo hello world && python3 --version",
  "timeout": 30
}
```

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `command` | string | Yes | Shell command to execute (run as-is via `sh -c`) |
| `timeout` | integer | No | Execution timeout in seconds (default: 30, max: 300) |
| `working_directory` | string | No | Working directory for command execution |

**Available Tools in Container:**

The container is `python:3.12-slim`-based (glibc) with a focused set of shell, scripting, and network utilities:

| Category | Tools | Use Cases |
|----------|-------|-----------|
| **Shell & Scripting** | `bash`, `python3`, `pip3`, `bc` | Shell scripts, Python one-liners, package install via `pip3 install --user`, math calculations |
| **Pre-installed Python packages** | `numpy`, `requests` | Numerical work, HTTP calls from Python |
| **Text Processing** | `grep`, `sed`, `awk` (`gawk`), `jq` | Log parsing, JSON processing |
| **Core Utilities** | `coreutils` (`sort`, `uniq`, `cut`, `tr`, `wc`, `head`, `tail`, `base64`, `sha256sum`, `md5sum`, `date`, `ls`, `cat`, `tee`, `xargs`, etc.); `tar`, `gzip` (Debian Essential, pre-installed) | General-purpose data manipulation |
| **Networking** | `curl`, `openssl`, `dig` (`dnsutils`), `ping` (`iputils-ping`), `traceroute`, `nc` (`netcat-openbsd`) | HTTP requests, DNS lookup, connectivity testing, TLS/cert debugging |
| **Version Control** | `git` | Repository operations |

> **Heavy tooling removed.** The previous image bundled `nodejs`, `npm`, Playwright + Chromium, `nmap`, `tcpdump`, `wget`, `openssh-client`, `htop`, `strace`, `lsof`, `sysstat`, `iproute2`, `net-tools` — together ~1.5 GB. Browser caps now live in [`playwright-tool`](../playwright-tool/). If you specifically need any of the dropped CLI tools, install via `apt-get` in a derived image or run `pip3 install --user` for Python equivalents.

**Response:**
```json
{
  "success": true,
  "data": {
    "command": "echo hello world && python3 --version",
    "stdout": "hello world\nPython 3.12.12\n",
    "stderr": "",
    "exit_code": 0,
    "duration_ms": 15
  }
}
```

**Safety:** Commands run inside an isolated container as a non-root user (`appuser`) with a proper bash shell and home directory. Resource limits are enforced via Kubernetes. Configurable timeout prevents runaway processes.

---

### 6. Generate ID (`generate_id`)

**Endpoint:** `/api/capabilities/generate_id`

Generates unique identifiers in various formats.

**Request:**
```json
{
  "type": "uuid",
  "count": 3
}
```

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `type` | string | No | `uuid` (default), `ulid`, or `nanoid` |
| `count` | integer | No | Number of IDs to generate (default: 1, max: 100) |

**Response:**
```json
{
  "success": true,
  "data": {
    "type": "uuid",
    "ids": [
      "7dd9f987-732a-420c-afcd-b2a1bd264cd0",
      "db48d01d-71e2-4a50-8ebf-48aa334a7016",
      "ff3f0eef-a3cf-4066-9f76-f0799250ad70"
    ]
  }
}
```

---

### 7. Sleep (`sleep`)

**Endpoint:** `/api/capabilities/sleep`

Sleeps (pauses) for a short, bounded duration before returning. Use this for brief in-line pauses — e.g., to let an external system settle after a write, or to space out polling checks.

**Request:**
```json
{
  "duration_seconds": 30,
  "reason": "waiting for kubectl rollout to propagate"
}
```

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `duration_seconds` | integer | Yes | How long to sleep, in seconds. Must be between 1 and 120. Requests above 120 are clamped. |
| `reason` | string | No | Free-text explanation of why the pause is needed; echoed into traces and logs. |

**Response:**
```json
{
  "success": true,
  "data": {
    "requested_seconds": 30,
    "duration_seconds": 30,
    "reason": "waiting for kubectl rollout to propagate",
    "started_at": "2026-04-12T10:00:00Z",
    "ended_at": "2026-04-12T10:00:30Z",
    "cancelled": false
  }
}
```

**Clamping:** If `duration_seconds` exceeds 120, it is silently clamped to 120. The response's `requested_seconds` preserves the original value so the caller can detect the clamp.

**Cancellation:** If the request context is cancelled (client disconnect, executor timeout, execution cancellation) before the duration elapses, the handler returns immediately with `cancelled: true` and `duration_seconds` reflecting the actual elapsed time.

**For longer pauses:** Use `scheduler-tool/schedule_task` instead — it frees the worker and checkpoints the DAG.

---

### Schema Discovery

Every capability has an auto-generated JSON Schema endpoint for Phase 3 validation:

```bash
curl http://localhost:8348/api/capabilities/get_current_time/schema
```

Returns a JSON Schema draft-07 document generated from the Phase 2 `InputSummary` field hints.

---

## Architecture

```
System Utilities Tool (Passive) — python:3.12-slim container
    |
    +-- Registers 7 capabilities in Redis
    +-- Receives requests from agents
    +-- Processes in-process or via local subprocess (sh -c for shell commands)
    +-- Returns standardized ToolResponse
    |
    +-- Capabilities:
        +-- get_current_time    (Go time.Now + time.LoadLocation)
        +-- convert_timezone    (Go time.Parse + In)
        +-- list_timezones      (Curated IANA zones + live offset resolution)
        +-- date_arithmetic     (Go time.AddDate / time.Add)
        +-- execute_command     (os/exec.CommandContext with timeout)
        +-- generate_id         (google/uuid + custom ULID/nanoid)
        +-- sleep               (time.NewTimer with context cancellation)
```

### Why This Tool Exists

The orchestrator needs deterministic operations that LLMs cannot reliably perform:
- **"What time is it in Seoul?"** - LLMs hallucinate dates; this tool gives the real answer
- **"Calculate 100 * 468.285"** - The orchestrator can `execute_command` instead of burning an LLM retry
- **"Add 30 days to today"** - Date arithmetic with timezone awareness
- **"Generate a unique ID"** - Deterministic UUID/ULID generation
- **"Run this Python snippet"** - `execute_command` with `python3 -c '...'` for inline data work using `numpy`, `requests`

### Integration with Agents

Once deployed, the tool is automatically discovered by agents via Redis:

```bash
# Query through an orchestrating agent
curl -X POST http://localhost:8353/api/capabilities/research_topic \
  -H "Content-Type: application/json" \
  -d '{
    "topic": "what time is it right now in Tokyo and New York",
    "ai_synthesis": true
  }'
```

---

## Configuration

### Environment Variables

| Variable | Description | Default | Required |
|----------|-------------|---------|----------|
| `REDIS_URL` | Redis connection URL | - | Yes |
| `PORT` | HTTP server port | `8348` | No |
| `NAMESPACE` | Kubernetes namespace | `default` | No |
| `DEV_MODE` | Development mode flag | `false` | No |
| `APP_ENV` | Environment profile (`development`/`staging`/`production`) | `development` | No |
| `TRUVAG3_LOG_LEVEL` | Logging level (`error`\|`warn`\|`info`\|`debug`) | `info` | No |
| `TRUVAG3_LOG_FORMAT` | Log format (`json`\|`text`) | `json` | No |
| `OTEL_EXPORTER_OTLP_ENDPOINT` | OpenTelemetry collector endpoint | - | No |

No API keys required. The tool is entirely self-contained. Python 3, `pip3`, and common shell + network utilities are pre-installed in the Docker image.

---

## Project Structure

```
system-utilities-tool/
├── main.go                 # Entry point, config validation, telemetry, framework setup
├── system_tool.go          # Tool struct, request/response types, capability registration
├── handlers.go             # HTTP handlers for all 7 capabilities with full telemetry
├── go.mod                  # Go module definition
├── .env.example            # Environment variable documentation
├── Dockerfile              # Standalone container (python:3.12-slim + shell + network utilities)
├── Dockerfile.workspace    # Dev container build from truvag3 root
├── k8-deployment.yaml      # Kubernetes Service + Deployment manifests
├── setup.sh                # Full lifecycle script (build, run, deploy, test, clean)
└── README.md               # This file
```

---

## Troubleshooting

### Common Issues

**1. Tool not appearing in discovery**

```bash
# Check Redis registration
kubectl exec -n truvag3-examples deploy/redis -- redis-cli KEYS "truvag3:*" | grep system

# Check tool logs
kubectl logs -n truvag3-examples -l app=system-utilities-tool --tail=20
```

**2. execute_command returns permission denied**

Commands run as non-root user `appuser`. Operations requiring root (e.g., system-wide `apt-get install`, port binding below 1024) will fail by design. Python packages can still be installed at runtime via `pip3 install --user <package>` — those land in `appuser`'s home and don't need root.

```bash
# Check what user the command runs as
curl -X POST http://localhost:8348/api/capabilities/execute_command \
  -H "Content-Type: application/json" \
  -d '{"command": "whoami && id"}'
```

**3. Timezone not found**

The container includes the `tzdata` package. Use standard IANA timezone names:

```bash
# List available timezones for a region
curl -X POST http://localhost:8348/api/capabilities/list_timezones \
  -H "Content-Type: application/json" \
  -d '{"region": "Asia"}'
```

**4. Command timeout**

Default timeout is 30 seconds (max 300). For long-running commands, increase the timeout:

```json
{"command": "sleep 60 && echo done", "timeout": 120}
```

**5. Pod not starting**

```bash
# Check pod events
kubectl describe pod -n truvag3-examples -l app=system-utilities-tool

# Check logs
kubectl logs -n truvag3-examples -l app=system-utilities-tool --tail=50
```

### Useful Commands

```bash
# View tool logs
kubectl logs -n truvag3-examples -l app=system-utilities-tool -f --tail=100

# Check pod status
kubectl get pods -n truvag3-examples -l app=system-utilities-tool

# Port forward for local testing
kubectl port-forward -n truvag3-examples svc/system-utilities-service 8348:80

# Run all tests via setup script
./setup.sh test

# Rebuild and redeploy
./setup.sh rebuild
```

---

## Related Examples

- [agent-with-orchestration](../agent-with-orchestration/) - Orchestration agent that can discover and use this tool
- [stock-market-tool](../stock-market-tool/) - Stock market data tool (similar passive pattern)
- [weather-tool-v2](../weather-tool-v2/) - Weather data tool
- [currency-tool](../currency-tool/) - Currency exchange tool

For infrastructure setup details, see [k8-deployment/README.md](../k8-deployment/README.md).
