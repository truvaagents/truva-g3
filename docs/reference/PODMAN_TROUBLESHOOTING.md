# Podman — Troubleshooting

Field-tested notes for running TruvaG3 on Podman (instead of Docker Desktop / OrbStack), with `kind` as the local Kubernetes provider. The quickstart in [GETTING_STARTED.md](https://github.com/truvaagents/truva-g3/blob/main/GETTING_STARTED.md) is the supported path — come here only when something breaks, or before deploying more than the handful of components in the quickstart.

Every item below is a **known upstream Podman/kind behavior**, not specific to TruvaG3 — each links the relevant issue so you can verify and track it. The example `setup.sh` scripts already work around the ones they can (runtime detection, image loading); the rest are environment-level and are on you to apply.

## kind needs a rootful podman machine (macOS)

Symptom: `kind create cluster` (or `./setup.sh full-deploy`) fails on macOS when the podman machine is rootless.

Root cause: kind's podman provider requires root inside the VM. Apple Silicon machines default to rootless. This is a documented kind requirement.

Fix:

```bash
podman machine stop
podman machine set --rootful
podman machine start
export KIND_EXPERIMENTAL_PROVIDER=podman   # needed for manual kind commands (setup.sh sets it itself)
```

Upstream: [kind#3092](https://github.com/kubernetes-sigs/kind/issues/3092), [kind#2888](https://github.com/kubernetes-sigs/kind/issues/2888).

## The setup scripts call `docker` by name — `alias docker=podman` won't help

Symptom: `setup.sh` fails with a Docker socket error (e.g. `dial unix .../docker.sock: connect: no such file or directory`) even though `podman` works, or even though you set a shell alias.

Root cause: `setup.sh` runs in a non-interactive `set -e` subshell, where shell aliases do not apply.

Fix: either pin the runtime explicitly, or put a real `docker`→`podman` shim (a script, not an alias) on `PATH`:

```bash
export TRUVAG3_CONTAINER_RUNTIME=podman    # honored by the scripts' runtime detection
```

The scripts prefer `docker` only when its daemon actually responds, so a stopped Docker Desktop / OrbStack install with a dead socket is skipped in favor of podman automatically.

## `kind load docker-image` fails under podman ("not present locally")

Symptom: loading a locally built image into the cluster fails with `ERROR: image: "<name>:latest" not present locally`, even though `podman image ls` shows it.

Root cause: a long-standing incompatibility between `kind load docker-image` and the podman provider. Podman also tags local builds as `localhost/<name>`, which containerd on the kind node will not match against a manifest's bare `image: <name>:tag` (containerd normalizes that to `docker.io/library/<name>:tag`).

Fix: the example scripts handle this automatically. If you load an image by hand, use an archive and retag to the normalized reference:

```bash
podman tag <name>:latest docker.io/library/<name>:latest
podman save docker.io/library/<name>:latest -o /tmp/img.tar
kind load image-archive /tmp/img.tar --name "truvag3-demo-$(whoami)"
```

Upstream: [kind#3105](https://github.com/kubernetes-sigs/kind/issues/3105), [kind#2027](https://github.com/kubernetes-sigs/kind/issues/2027), [kind#2417](https://github.com/kubernetes-sigs/kind/issues/2417), [kind#3822](https://github.com/kubernetes-sigs/kind/issues/3822) (reported across versions 2021–2024).

## Ingress hostnames intermittently time out (gvproxy)

Symptom: `*.localhost` ingress URLs flake — `curl` returns `000` / times out, a browser loads them only sometimes — **even though the cluster is healthy**: no node resource pressure, all pods Ready, and the service answers `200` via its in-cluster ClusterIP.

Root cause: Podman's host→VM port forwarder, **gvproxy**, can stop forwarding new connections reliably after a period of use — a known macOS issue. It is not a cluster or deployment problem (the backends are fine). Docker Desktop / OrbStack use different host-networking stacks and don't show this.

Fix: gvproxy can't be restarted on its own — refresh it by bouncing the machine, then restart the kind node (its container restart policy is `no`) and wait for ingress:

```bash
podman machine stop && podman machine start
# the kind node container — default cluster name is truvag3-demo-$(whoami);
# if you renamed the cluster, derive it: podman ps -a --format '{{.Names}}' | grep -- '-control-plane$'
podman start "truvag3-demo-$(whoami)-control-plane"
kubectl wait --for=condition=ready pod -n ingress-nginx \
  -l app.kubernetes.io/component=controller --timeout=120s
```

After the refresh, ingress is reliable again (verify with a few repeated `curl http://travel-chat-agent.localhost/health`).

Upstream: [podman#18017 — "gvproxy runs for a while and stops forwarding"](https://github.com/containers/podman/issues/18017), [podman#28047](https://github.com/containers/podman/issues/28047), [podman#11396](https://github.com/containers/podman/issues/11396), [podman#11532](https://github.com/containers/podman/issues/11532).

## Sizing the podman machine for the full example catalog

The travel quickstart (~6 tools) runs fine on a modestly-sized machine. Deploying many or all of the ~45 examples does not — a small CPU/disk allocation (e.g. 2 CPUs) won't fit it.

Symptoms when under-provisioned:
- Pods stuck `Pending` with `0/1 nodes are available: 1 Insufficient cpu`.
- Image builds failing mid-run with `no space left on device` during `go mod download` (each Go example build adds layers; dozens of sequential builds overflow a small disk).

Fix — size the machine up front:

```bash
podman machine stop
podman machine set --cpus 6 --memory 12288 --disk-size 80
podman machine start
```

(6 CPU / 12 GB / 80 GB comfortably runs the full catalog on a 10-core / 32 GB host. Scale to your hardware.) Note that `--disk-size` enlarges the virtual disk but does **not** grow the filesystem inside the VM — see [Growing the machine disk doesn't resize the filesystem](#growing-the-machine-disk-doesnt-resize-the-filesystem) below for the one-time `growpart`/`xfs_growfs` step. Alternatively, keep the default disk and prune dangling build layers between deploys (`podman image prune -f`). The Go module-cache volume is worth keeping — it speeds rebuilds.

## Growing the machine disk doesn't resize the filesystem

Symptom: after `podman machine set --disk-size N`, the VM still reports the old free space and builds keep failing with `no space left on device`.

Root cause: increasing the qcow disk does not auto-expand the root partition/filesystem inside the VM. (Behavior varies by podman version and provider — some report `--disk-size` having no effect at all on Apple Silicon `applehv`, requiring a machine re-create via `podman machine init`.)

Fix — grow the partition and filesystem once, inside the VM:

```bash
# growpart + xfs_growfs run inside the VM; the trailing df confirms the new size
podman machine ssh 'sudo growpart /dev/vda 4 && sudo xfs_growfs / && df -h /'
```

(`/dev/vda4` is the root partition on the current podman machine image; confirm with `podman machine ssh lsblk` if yours differs.)

Upstream: [podman#25890 — "Increase Machine Disk Size has no effect"](https://github.com/containers/podman/issues/25890), [podman#20564](https://github.com/containers/podman/issues/20564), [podman#13633](https://github.com/containers/podman/issues/13633).
