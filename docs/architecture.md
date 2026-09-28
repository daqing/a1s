# A1s Architecture Notes

Decision records for choices the roadmap leaves open. Each entry states the
decision, the alternatives, and why.

## Container runtime client (Phase 3, T3.1)

**Decision:** the worker agent talks to containerd through the official
high-level Go client (`github.com/containerd/containerd/v2/client`), not the
CRI gRPC API.

Why:

- A1s runs standalone containers, not Kubernetes pods. CRI's API is shaped
  around pod sandboxes (RunPodSandbox/StartContainer), which adds ceremony
  with no benefit outside a kubelet.
- The high-level client covers everything A1s needs directly: pull, create,
  start, stop, remove, and status of containers and tasks.
- The README's implementation-foundation section already points at the
  official upstream Go libraries for the containerd client; the "via CRI"
  phrasing there meant "containerd, like Kubernetes — not the Docker API",
  which this decision keeps: we speak to containerd itself.

## Packaging

One fat image with a command override: every A1s process is a subcommand of
the same binary, so a deployment runs `docker run a1s api`,
`docker run a1s scheduler`, and so on, instead of maintaining one image per
process. Worker containers need the host's containerd socket mounted
(`-v /run/containerd/containerd.sock:/run/containerd/containerd.sock`) and
run on a Linux host sharing filesystems with that containerd — layer
application is client-side.

## Lima-only development (no Docker)

`scripts/vm.sh` replaces Docker entirely for local development: one Ubuntu
Lima VM hosts containerd, PostgreSQL and the workers natively — the
production shape, with none of the nested-container workarounds (overlayfs
works on the VM disk, the worker shares the filesystem with its containerd
by construction, no socat, no proxy juggling). Only the control plane
(api/scheduler/monitor) stays on the host and reaches the database through
Lima's port forwarding. The VM's PostgreSQL listens on 5437 so it cannot
collide with a native postgres on 5432.

## Development containerd

The worker needs a real containerd on the development machine. On macOS the
dev setup runs containerd inside Docker with its gRPC socket forwarded to
localhost over TCP. Three setup facts matter:

- **ghcr images are unreachable from this network**, so the container
  installs containerd, runc and ctr from the Alpine package repository, and
  Docker Desktop's built-in HTTP proxy makes registry pulls work from
  inside the container.
- **Layer application is client-side**: `WithNewSnapshot`/`ApplyLayer` run
  in the worker process against the daemon's filesystem paths, so a macOS
  worker cannot drive a remote containerd. The dev worker therefore runs
  *inside* the container (cross-compiled linux/arm64 binary), which is also
  the production shape: worker and containerd share the node's filesystem.
- **The Docker Desktop VM kernel rejects stacked overlay mounts**
  (`invalid argument` on the task rootfs mount), so the dev worker sets
  `A1S_CONTAINERD_SNAPSHOTTER=native` — the native snapshotter needs no
  mounts for unpack, and bind-mounts the task rootfs.

```sh
docker run -d --name a1s-containerd --privileged \
  -p 127.0.0.1:60001:60001 \
  -e HTTPS_PROXY=http://http.docker.internal:3128 \
  -e HTTP_PROXY=http://http.docker.internal:3128 \
  -e NO_PROXY=localhost,127.0.0.1,http.docker.internal \
  alpine:latest \
  sh -c "apk add --no-cache containerd socat runc containerd-ctr >/dev/null && \
    (containerd >/var/log/containerd.log 2>&1 &) && sleep 2 && \
    socat TCP-LISTEN:60001,fork,reuseaddr UNIX-CONNECT:/run/containerd/containerd.sock"
```

The worker connects with `worker.Dial` (`app/worker/worker.go`): a
`tcp://host:port` address goes through a custom gRPC dialer plus explicit
namespace interceptors, anything else is a unix socket path — the
production case. In production the worker agent runs on Linux next to a
native containerd and connects over `/run/containerd/containerd.sock`.

Seeding images for offline dev: pulls from inside the container work
through the proxy (`docker exec a1s-containerd ctr -n a1s images pull
--platform linux/arm64 docker.io/library/busybox:latest` unpacks to the
default overlayfs), and the worker's `ensureImage` unpacks an existing
image record into the configured snapshotter on demand.

`TestContainerdConnectivity` (`app/worker/worker_test.go`) is the standing
connectivity check: it skips unless `A1S_CONTAINERD_ADDR` is set, then
lists namespaces, creates one, and lists again:

```sh
A1S_CONTAINERD_ADDR=tcp://127.0.0.1:60001 go test ./app/worker/ -v
```

`TestStartExecutorRunsTask` additionally runs a container end to end; it
requires a Linux client (see above).

The worker-side `A1S_CONTAINERD_*` variables: `A1S_CONTAINERD_ADDR`,
`A1S_CONTAINERD_NAMESPACE` (default `a1s`),
`A1S_CONTAINERD_SNAPSHOTTER` (default `overlayfs`) and
`A1S_CONTAINERD_PLATFORM` (pull/spec platform override for cross-platform
clients).
