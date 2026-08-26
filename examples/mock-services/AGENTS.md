# AGENTS.md — `examples/mock-services/`

Shared **mock backend services** used as test dependencies or local simulators.
The Kubernetes mocks are not standalone runnable examples; the paired agent's
`setup.sh` builds/deploys the one it needs. The enterprise Ollama gateway is a
local-only simulator with its own development commands. Inherits
[../AGENTS.md](../AGENTS.md) and the repo-root [AGENTS.md](../../AGENTS.md).

## Which service pairs with which example

| Mock service | Deployed / used by | Role |
|---|---|---|
| `grocery-store-api` | [agent-with-resilience](../agent-with-resilience/) (builds + deploys it) and [grocery-tool](../grocery-tool/) (calls it in-cluster) | Error-injection backend for **resilience** testing (`/admin/inject-error`, `/admin/reset`). |
| `product-catalog-api` | [event-driven-agent](../event-driven-agent/) and [my-async-agent](../my-async-agent/) (each deploys it during `full-deploy`) | Incident-simulation target for **E2E / HITL** testing — `/admin/simulate/degrade` + `/admin/simulate/recover` trip real Prometheus alerts. Also scraped by the shared Prometheus job `truvag3-mock-services` and charted by two Grafana dashboards in [../k8-deployment/](../k8-deployment/). |
| `enterprise-ollama-gateway` | Local framework/provider development only | OAuth2 + Azure-style chat API simulator backed by a developer-run Ollama instance. It is not deployed by another example. |

In-cluster DNS for the two Kubernetes mocks: `<name>.truvag3-examples.svc.cluster.local` (container
port `8081` → service `:80`). `event-driven-agent` uses **product-catalog-api
only** — it does not touch grocery-store-api, and vice versa. The enterprise
gateway is not part of this in-cluster pair.

## How they get deployed — don't deploy by hand

- **`grocery-store-api` has no `setup.sh`.** [agent-with-resilience](../agent-with-resilience/)'s
  `setup.sh` builds its image, loads it into Kind, and applies its
  `k8-deployment.yaml` as part of that example's deploy. Run *that* example's
  setup — never `docker build`/`podman build` or `kubectl apply` the mock service directly.
- **`product-catalog-api` has its own `setup.sh`,** but you normally don't call it
  directly either: [event-driven-agent](../event-driven-agent/) and
  [my-async-agent](../my-async-agent/) deploy it during `full-deploy` and expose a
  `./setup.sh mock-service …` subcommand to drive degrade/recover. See those
  examples' READMEs.
- **`enterprise-ollama-gateway` is local-only.** Follow its README and use its
  `./setup.sh run`, `test`, and `smoke` commands; it is not deployed to Kind by a
  paired example.

To rebuild a mock service after a code change, use the **paired example's**
`setup.sh` (it owns building and loading the image into Kind) — check its
`./setup.sh help` for the exact verb. Editing a mock service in isolation and
rebuilding it by hand will not refresh the image the agent actually runs.
This paired-deployment rule does not apply to the local-only enterprise simulator.
