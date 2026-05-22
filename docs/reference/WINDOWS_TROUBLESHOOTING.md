# Windows + WSL2 — Troubleshooting

Field-tested workarounds for issues that come up when running TruvaG3 on Windows + WSL2 + Docker Desktop. The quickstart in [GETTING_STARTED.md](https://github.com/truvaagents/truva-g3/blob/main/GETTING_STARTED.md) is the supported path — come here only when something breaks.

## Line-ending errors from `setup.sh`

Symptom: `setup.sh` fails immediately with `'\r': command not found`, `bad interpreter`, or syntax errors that don't match the file content. CRLF line endings from a Windows clone are the cause.

Fix the current clone:

```bash
find . -name "*.sh" -exec sed -i 's/\r$//' {} +
```

Prevent it from recurring on future clones:

```bash
git config --global core.autocrlf input
```

## Service discovery flickers (clock skew)

The discovery layer uses Redis TTLs (default 30s) to track healthy services. If clocks drift between Windows, WSL, and the Redis pod by more than the TTL, healthy services look expired and re-register repeatedly.

Symptoms:

- Service catalog flickers between calls.
- Logs show `Service not found for health update` followed by re-registration.
- Kubernetes events have `<invalid>` timestamps.

Diagnose:

```bash
date -u                                                     # WSL clock
kubectl exec -n truvag3-examples deploy/redis -- date -u    # Redis pod clock
```

If they differ by more than a few seconds, fix the WSL clock:

```bash
sudo systemctl stop systemd-timesyncd      # don't let it snap back
sudo date -u -s '<current Windows UTC>'
```

Restart affected components so they re-register against the corrected clock:

```bash
kubectl rollout restart \
  deployment/travel-chat-agent \
  deployment/weather-tool-v2 \
  deployment/geocoding-tool \
  deployment/currency-tool \
  deployment/country-info-tool \
  deployment/system-utilities-tool \
  deployment/travel-advisory-tool \
  -n truvag3-examples
```

Redis service TTLs should now land between 0 and 30 (`redis-cli ttl truvag3:services:<name>`) and discovery should be stable.

## Registry Viewer ingress points at a missing service

The `full-deploy` infra script may continue even if Registry Viewer's deployment step fails — leaving an orphan `registry-viewer-ingress` without a backing `registry-viewer-service`. Symptom: `http://registry.localhost/` returns nginx 404, or `kubectl describe ingress -n truvag3-examples registry-viewer-ingress` reports `services "registry-viewer-service" not found`.

Diagnose:

```bash
kubectl get deploy,svc,ingress -n truvag3-examples | grep -i registry
```

Expected — three rows (deployment, service, ingress). If only the ingress is present, deploy the missing pieces explicitly:

```bash
cd ~/truva-g3/examples/registry-viewer-app
./setup.sh deploy
```

After this, `curl -I http://registry.localhost/` should return `HTTP/1.1 200 OK`.

## Ingress restart deadlocks (single-node kind)

ingress-nginx uses `hostPort: 80, 443` to expose the cluster on `*.localhost`. On a single-node kind cluster, `kubectl rollout restart deployment/ingress-nginx-controller -n ingress-nginx` deadlocks — the new pod can't schedule because the old pod still owns those ports:

```
0/1 nodes are available: 1 node(s) didn't have free ports for the requested pod ports
```

Bypass the rolling strategy by deleting the old pod manually:

```bash
kubectl delete pod -n ingress-nginx -l app.kubernetes.io/component=controller
kubectl rollout status deployment/ingress-nginx-controller -n ingress-nginx --timeout=120s
```

~10–30s of ingress downtime while the new pod schedules. Local-dev only — multi-node clusters don't have this constraint.
