# Registry Viewer Live-Data Verification

This runbook proves that framework-produced DB-0 data remains visible through
the Registry Viewer when Redis or Valkey runs in standalone, Sentinel, or
cluster mode. The verification is read-only: it never constructs backend keys
or writes fixtures directly to Redis.

## Table of Contents

- [What this verifies](#what-this-verifies)
- [Prerequisites](#prerequisites)
- [Deploy the Viewer](#deploy-the-viewer)
- [Run the Local Kind Cluster Matrix](#run-the-local-kind-cluster-matrix)
- [Verify a DB-0 Development Cleanup](#verify-a-db-0-development-cleanup)
- [Run the API verification](#run-the-api-verification)
- [Run strict evidence verification](#run-strict-evidence-verification)
- [Manual UI checklist](#manual-ui-checklist)
- [Cluster acceptance rules](#cluster-acceptance-rules)
- [Record the evidence](#record-the-evidence)

## What this verifies

The path under test is:

```text
framework producer
  -> versioned deployment-scoped DB-0 keys
  -> Redis/Valkey topology routing
  -> Registry Viewer shared universal client
  -> Viewer HTTP APIs
  -> browser rendering
```

The automated check covers liveness, Redis readiness, service and Swagger
discovery, execution listing/search/grouping, LLM debug, HITL, skills/schema,
resolution analytics, and every configured memory API. When expected record
identifiers are supplied, it also proves the corresponding index memberships,
loads the detail views, checks skill history, and validates the trace redirect.

## Prerequisites

- Deploy framework producers through their own `setup.sh` scripts.
- Configure every producer and the Viewer with the same Redis topology and
  `TRUVAG3_REDIS_NAMESPACE`.
- Enable the producer features whose evidence you want to inspect, including
  LLM debug, HITL, skills, memory, and tracing.
- Install `curl` and `jq` on the verification host.
- Expose the Viewer at `http://localhost:8361` with `./setup.sh forward`, or set
  `TRUVAG3_VIEWER_URL` to another reachable base URL.

Do not seed raw Redis keys. Generate registrations, executions, LLM calls,
checkpoints, skills, and memory evidence through their framework-owned APIs.

## Deploy the Viewer

Standalone DB 0 uses the standard URL shorthand:

```bash
REDIS_URL=redis://redis.example:6379/0 \
TRUVAG3_REDIS_NAMESPACE=verification \
./setup.sh rebuild
```

Cluster mode uses the structured form:

```bash
TRUVAG3_REDIS_MODE=cluster \
TRUVAG3_REDIS_ADDRS=redis-cluster-0:6379,redis-cluster-1:6379,redis-cluster-2:6379 \
TRUVAG3_REDIS_DB=0 \
TRUVAG3_REDIS_NAMESPACE=verification \
./setup.sh rebuild
```

For authenticated or TLS deployments, the setup script stores `REDIS_URL`,
data-node credentials, and Sentinel credentials in a Kubernetes Secret. A
configured `TRUVAG3_REDIS_CA_FILE` is copied into a separate Secret and mounted
read-only; its host path is not sent to the pod.

Do not combine `REDIS_URL` with structured `TRUVAG3_REDIS_*` connection fields.
Operational pool, timeout, and retry settings may accompany either form.
The Registry Viewer accepts only DB 0 in standalone, Sentinel, and cluster
mode; a URL or structured configuration that selects another database fails.

## Run the Local Kind Cluster Matrix

The shared infrastructure directory contains a lightweight validation-only
topology that coexists with the normal standalone Redis service. It creates
three primaries without replicas and pins Redis OSS 8.8.0, Valkey 8.1.9, and
Valkey 9.1.1. Replica promotion remains covered by the separate Linux
integration fixture.

```bash
cd ../k8-deployment
./setup-redis-cluster-validation.sh deploy redis-oss
./setup-redis-cluster-validation.sh configure-cluster redis-cluster-validation \
  registry-viewer weather-tool-v2 geocoding-tool travel-chat-agent
```

The structured seed list configured by the helper is:

```text
redis-cluster-validation-0.redis-cluster-validation.truvag3-examples.svc.cluster.local:6379
redis-cluster-validation-1.redis-cluster-validation.truvag3-examples.svc.cluster.local:6379
redis-cluster-validation-2.redis-cluster-validation.truvag3-examples.svc.cluster.local:6379
```

Reconcile skills through each owning example's `./setup.sh skills-sync`, create
representative framework records, and run strict verification. Repeat after
`cleanup` with `deploy valkey-8` and `deploy valkey-9`; use a fresh namespace
for each provider so records cannot leak between runs. The helper's `status`
command verifies all slots and the expected primary count.

This manual Kind exercise verifies application and Viewer behavior. The optional
Linux integration fixture separately covers deterministic `ASK`, `MOVED`,
unavailable-shard, Sentinel, and capacity cases. Neither fixture runs in CI;
the unchanged default unit-test suite remains the primary automated gate.

## Verify a DB-0 Development Cleanup

When updating examples after removal of obsolete Redis APIs, keep the existing
canonical DB-0 namespace and records. This cleanup does not require a Redis
wipe, a new deployment namespace, or republishing unchanged skills.

1. Check each example's private `.env` against its checked-in `.env.example`.
   Remove obsolete role-specific `TRUVAG3_*_REDIS_DB` settings, the duplicate
   `TRUVAG3_REDIS_URL`, and removed HITL/debug raw-prefix settings. Preserve
   current provider credentials; never copy their values into `.env.example`.
2. Rebuild Registry Viewer through `setup.sh rebuild` with the existing
   structured connection and namespace. Then rebuild each changed producer
   through its own `setup.sh rebuild`. Use `rollout` only for configuration-only
   changes; it refreshes Secret/ConfigMap data but does not rebuild the image.
3. For the local validation cluster, re-run `configure-cluster` after each
   producer rebuild. Its portable manifest restores standalone defaults, so a
   successful rebuild alone does not prove that the pod is back on the cluster.
   Keep the management writer and skill readers on the same namespace.
4. Run `skills-check` in the owning skill-enabled examples. Unchanged packages
   should still match Git and retain their published versions.
5. Create fresh requests through the agents: a tool lookup, a read-only DevOps
   request that exercises skills/memory/hooks, and a HITL-gated lookup. Inspect
   the pending checkpoint in the viewer and use **Approve & Resume**. Confirm
   the checkpoint completes, leaves the pending list, and has a successful
   resumed execution. The original interrupted execution remains historical
   evidence; inspect its related resumed execution rather than treating the
   original record as a new pending checkpoint.
6. Run the API checks below with the fresh request, conversation, and skill
   identifiers. Inspect all six viewer screens and expand the recorded hook
   values. Match pod and Loki `trace_id`/`span_id` values against Jaeger spans;
   use the [structured-metadata LogQL filters](../../docs/observability/LOGGING_IMPLEMENTATION_GUIDE.md#service-identity-contract-loki-service_name).

Record the exact topology, rebuilt examples, identifiers, checks, and any
failures. A targeted cleanup check is not a repeat of the full Redis/Valkey,
Sentinel, replica-failover, or asynchronous-delivery matrix. Unit tests remain
the unchanged CI gate; live verification is supplemental.

## Run the API verification

In another terminal, start the Viewer port forward and run:

```bash
./setup.sh verify
```

The command requires `/api/readiness` to report a reachable Redis backend,
DB 0, a validated namespace, and one of the supported topology modes. It then
checks the response contract for every Viewer data API and prints the visible
record counts. Empty collections pass this basic availability check.

Pin the expected topology when changing between standalone and cluster:

```bash
TRUVAG3_VIEWER_EXPECT_REDIS_MODE=cluster \
TRUVAG3_VIEWER_EXPECT_NAMESPACE=verification \
./setup.sh verify
```

## Run strict evidence verification

First produce one representative record for every surface. Capture identifiers
from framework/API responses, then run:

```bash
TRUVAG3_VIEWER_EXPECT_REDIS_MODE=cluster \
TRUVAG3_VIEWER_EXPECT_NAMESPACE=verification \
TRUVAG3_VIEWER_EXPECT_SERVICE=travel-chat-agent \
TRUVAG3_VIEWER_EXPECT_REQUEST_ID=orch-123 \
TRUVAG3_VIEWER_EXPECT_LLM_REQUEST_ID=orch-123 \
TRUVAG3_VIEWER_EXPECT_CONVERSATION_ID=conversation-123 \
TRUVAG3_VIEWER_EXPECT_SKILL=travel/trip-planning \
TRUVAG3_VIEWER_EXPECT_HITL_CHECKPOINT_ID=checkpoint-123 \
TRUVAG3_VIEWER_EXPECT_MEMORY_DOMAIN=infrastructure \
TRUVAG3_VIEWER_REQUIRE_PIPELINE_HOOKS=true \
TRUVAG3_VIEWER_REQUIRE_TRACE_LINK=true \
./setup.sh verify-all
```

Strict verification requires:

- the expected service and at least one advertised capability;
- an execution detail and computed DAG;
- execution and conversation index membership without incomplete markers;
- non-empty, well-formed pipeline-hook diagnostics stored with that execution;
- a valid optional trace browser redirect when
  `TRUVAG3_VIEWER_REQUIRE_TRACE_LINK=true` is supplied;
- LLM index membership and a non-empty interaction record;
- a complete conversation timeline without partial/index/enrichment flags;
- catalog/history membership for the exact published skill and list/detail
  membership for the exact HITL checkpoint;
- at least one event, investigation, digest, or activity in the expected memory
  domain.

The check does not approve, reject, resume, publish, or delete anything.

## Manual UI checklist

After strict API verification passes, open the dashboard and confirm:

- **Registry:** agent/tool counts, health, last heartbeat, addresses,
  capabilities, schema summaries, and metadata render correctly.
- **LLM Debug:** the expected request shows every interaction, recording site,
  model/provider, token counts, duration, prompt, response, and errors.
- **Execution DAG:** the expected execution shows plan phases, step edges,
  results, timing, lineage, final response, skills evidence, LLM calls, HITL
  lifecycle, and the trace link where applicable.
- **HITL:** the expected checkpoint shows request/trace identity, interrupt
  point, status, decision context, plan, completed steps, and expiry data.
- **Skills:** current metadata, full package, version history, resources, and
  ETag-driven editing state render for the expected skill.
- **Memory:** configured domains, recent events, investigations, digest, and
  activity signals render without browser errors.
- Browser developer tools show no failed Viewer API requests or JavaScript
  exceptions while navigating and refreshing each view.

## Cluster acceptance rules

- `/api/readiness` reports `redis_mode=cluster`, `redis_db=0`, and the exact
  deployment namespace used by the producers.
- Records produced with request and conversation identities that map to
  different primaries are all visible. The framework cluster suite proves slot
  placement; this Viewer run proves the cross-shard read path.
- A missing advisory-index member may omit a result until repaired, but a stale
  member must not create a false record. Verified stale members may be pruned
  only from advisory execution indexes.
- A shard/read failure must produce an explicit error or an incomplete/partial
  marker. The Viewer must not present an ambiguous partial response as complete.
- No `CROSSSLOT`, `MOVED`, `ASK`, authentication, or TLS error appears in the
  Viewer logs during a stable verification run.

## Record the evidence

For each release candidate, record:

- commit and candidate version;
- Redis/Valkey provider and version;
- standalone, Sentinel, or cluster topology;
- namespace and UTC verification time;
- the `verify-all` summary and pass/fail result;
- the representative request, conversation, checkpoint, and skill identifiers;
- manual UI result and any intentionally unavailable external integration.

Do not record endpoints, usernames, passwords, connection URLs, or CA contents.
