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

## Development containerd

The worker needs a real containerd on the development machine. On macOS the
dev setup runs containerd inside Docker with its gRPC socket forwarded to
localhost over TCP (ghcr images are unreachable from this network, so the
container installs containerd from the Alpine package repository):

```sh
docker run -d --name a1s-containerd --privileged \
  -p 127.0.0.1:60001:60001 alpine:latest \
  sh -c "apk add --no-cache containerd socat >/dev/null && \
    (containerd >/var/log/containerd.log 2>&1 &) && sleep 2 && \
    socat TCP-LISTEN:60001,fork,reuseaddr UNIX-CONNECT:/run/containerd/containerd.sock"
```

The worker connects with `worker.Dial` (`app/worker/worker.go`): a
`tcp://host:port` address goes through a custom gRPC dialer, anything else
is a unix socket path — the production case. In production the worker agent
runs on Linux next to a native containerd and connects over
`/run/containerd/containerd.sock`; the TCP path exists only for development.

`TestContainerdConnectivity` (`app/worker/worker_test.go`) is the standing
connectivity check: it skips unless `A1S_CONTAINERD_ADDR` is set, then
lists namespaces, creates one, and lists again:

```sh
A1S_CONTAINERD_ADDR=tcp://127.0.0.1:60001 go test ./app/worker/ -v
```
