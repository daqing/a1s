#!/bin/sh
# scripts/cluster.sh — one-command local cluster (used by `just cluster`):
#
#   cluster.sh up     start postgres + a containerd node (Docker), migrate,
#                     and launch api + scheduler + monitor + 2 workers
#                     (inside the containerd node, sharing its filesystem)
#   cluster.sh down   stop the a1s processes; the Docker containers stay
#                     for the next `up`
#
# Everything is idempotent: `up` on a running cluster reports and exits.
# Logs and pid files live in .cluster/.
set -eu

DIR="$(cd "$(dirname "$0")/.." && pwd)"
STATE="$DIR/.cluster"
TOKEN="${A1S_E2E_TOKEN:-dev-token}"
PG_PORT="${A1S_CLUSTER_PG_PORT:-5435}"
CD_PORT="${A1S_CLUSTER_CD_PORT:-60003}"
API_PORT="${A1S_CLUSTER_API_PORT:-1905}"
DSN="postgres://a1s:a1s@127.0.0.1:${PG_PORT}/a1s?sslmode=disable"
CD_ADDR="tcp://127.0.0.1:${CD_PORT}"
CONTAINER_PROXY="${A1S_CLUSTER_CONTAINER_PROXY:-}"

say() { echo "[cluster] $*"; }
running() { [ -f "$STATE/$1.pid" ] && kill -0 "$(cat "$STATE/$1.pid")" 2>/dev/null; }

containers_up() {
	docker ps --format '{{.Names}}' 2>/dev/null | grep -qx "a1s-cluster-pg" &&
		docker ps --format '{{.Names}}' 2>/dev/null | grep -qx "a1s-cluster-containerd"
}

start_containers() {
	if ! docker ps --format '{{.Names}}' | grep -qx "a1s-cluster-pg"; then
		say "starting postgres (port ${PG_PORT})"
		docker rm -f a1s-cluster-pg >/dev/null 2>&1 || true
		docker run -d --name a1s-cluster-pg \
			-e POSTGRES_USER=a1s -e POSTGRES_PASSWORD=a1s -e POSTGRES_DB=a1s \
			-p "127.0.0.1:${PG_PORT}:5432" postgres:16 >/dev/null
	fi

	if ! docker ps --format '{{.Names}}' | grep -qx "a1s-cluster-containerd"; then
		say "starting the containerd node (port ${CD_PORT})"
		docker rm -f a1s-cluster-containerd >/dev/null 2>&1 || true
		proxy_env=""
		if [ -n "$CONTAINER_PROXY" ]; then
			proxy_env="-e HTTPS_PROXY=${CONTAINER_PROXY} -e HTTP_PROXY=${CONTAINER_PROXY} -e NO_PROXY=localhost,127.0.0.1"
		fi
		# shellcheck disable=SC2086
		docker run -d --name a1s-cluster-containerd --privileged \
			-p "127.0.0.1:${CD_PORT}:60001" \
			--add-host=host.docker.internal:host-gateway \
			$proxy_env alpine:latest \
			sh -c 'for i in 1 2 3 4 5; do apk add --no-cache containerd socat runc containerd-ctr >/dev/null 2>&1 && break; sleep 3; done; (containerd >/var/log/containerd.log 2>&1 &) && sleep 2 && exec socat TCP-LISTEN:60001,fork,reuseaddr UNIX-CONNECT:/run/containerd/containerd.sock' >/dev/null
	fi

	deadline=$(( $(date +%s) + 90 ))
	until docker exec a1s-cluster-pg pg_isready -U a1s >/dev/null 2>&1 &&
		docker exec a1s-cluster-containerd nc -z 127.0.0.1 60001 >/dev/null 2>&1; do
		[ "$(date +%s)" -lt "$deadline" ] || {
			docker logs --tail 5 a1s-cluster-containerd >&2 || true
			echo "[cluster] FAIL: the containers never became ready" >&2
			exit 1
		}
		sleep 2
	done
}

start_process() { # name, command...
	name="$1"
	shift

	if running "$name"; then
		say "$name already running (pid $(cat "$STATE/$name.pid"))"
		return
	fi

	nohup "$@" >"$STATE/$name.log" 2>&1 &
	echo $! >"$STATE/$name.pid"
	say "$name started (pid $(cat "$STATE/$name.pid"), log $STATE/$name.log)"
}

up() {
	mkdir -p "$STATE"
	command -v docker >/dev/null 2>&1 || {
		echo "[cluster] FAIL: docker is required" >&2
		exit 1
	}

	start_containers

	say "build"
	(cd "$DIR" && go build -o bin/a1s .)
	GOOS=linux GOARCH="$(go env GOARCH)" go -C "$DIR" build -o bin/a1s-linux .

	say "migrate"
	(cd "$DIR" && A1S_DSN="$DSN" go run . db:migrate >/dev/null)

	start_process api env A1S_DSN="$DSN" A1S_INTERNAL_TOKEN="$TOKEN" AIRWAY_ENV=production AIRWAY_PORT="$API_PORT" \
		"$DIR/bin/a1s" api
	start_process scheduler env A1S_DSN="$DSN" "$DIR/bin/a1s" scheduler
	start_process monitor env A1S_DSN="$DSN" "$DIR/bin/a1s" monitor

	say "start two workers inside the containerd node"
	docker cp "$DIR/bin/a1s-linux" a1s-cluster-containerd:/usr/local/bin/a1s
	docker exec a1s-cluster-containerd pkill -f 'a1s worker' 2>/dev/null || true
	worker_env="-e A1S_INTERNAL_TOKEN=${TOKEN} -e A1S_API_URL=http://host.docker.internal:${API_PORT} -e A1S_CONTAINERD_ADDR=/run/containerd/containerd.sock -e A1S_CONTAINERD_SNAPSHOTTER=native -e NO_PROXY=localhost,127.0.0.1,host.docker.internal"
	if [ -n "$CONTAINER_PROXY" ]; then
		worker_env="$worker_env -e HTTPS_PROXY=${CONTAINER_PROXY} -e HTTP_PROXY=${CONTAINER_PROXY}"
	fi
	# shellcheck disable=SC2086
	docker exec $worker_env -d a1s-cluster-containerd /bin/sh -c '/usr/local/bin/a1s worker --name w1 > /tmp/w1.log 2>&1'
	# shellcheck disable=SC2086
	docker exec $worker_env -d a1s-cluster-containerd /bin/sh -c '/usr/local/bin/a1s worker --name w2 > /tmp/w2.log 2>&1'
	echo "$CD_ADDR" >"$STATE/containerd.addr"

	deadline=$(( $(date +%s) + 30 ))
	until curl -s -m 2 -o /dev/null "http://127.0.0.1:${API_PORT}/health"; do
		[ "$(date +%s)" -lt "$deadline" ] || {
			echo "[cluster] FAIL: the api never came up (log $STATE/api.log)" >&2
			exit 1
		}
		sleep 1
	done

	say "cluster is up: api on ${API_PORT}, postgres on ${PG_PORT}, containerd on ${CD_PORT}"
	say "export A1S_API_URL=http://127.0.0.1:${API_PORT} A1S_DSN='$DSN' A1S_INTERNAL_TOKEN='$TOKEN' before using the CLI"
}

down() {
	say "stopping processes"
	for name in api scheduler monitor worker1 worker2; do
		if running "$name"; then
			kill "$(cat "$STATE/$name.pid")" 2>/dev/null || true
			rm -f "$STATE/$name.pid"
		fi
	done
	docker exec a1s-cluster-containerd pkill -f 'a1s worker' 2>/dev/null || true
	say "cluster down (Docker containers left running; docker rm -f a1s-cluster-pg a1s-cluster-containerd to remove them)"
}

case "${1:-}" in
up) up ;;
down) down ;;
*)
	echo "usage: scripts/cluster.sh up|down" >&2
	exit 2
	;;
esac
