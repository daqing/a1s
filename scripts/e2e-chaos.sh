#!/bin/sh
# scripts/e2e-chaos.sh — end-to-end chaos acceptance (docs/TASKS.md T6.4):
#
#   postgres + containerd (both in Docker), api + scheduler + monitor on the
#   host, two workers inside the containerd node; run containers, kill -9 a
#   worker and assert rescheduling onto the survivor, then externally kill a
#   container and assert the restart policy repairs it. Exits 0 when every
#   assertion holds, non-zero with diagnostics otherwise.
#
# Environment:
#   A1S_E2E_TOKEN             internal token          (default e2e-token)
#   A1S_E2E_PG_PORT           postgres port           (default 5434)
#   A1S_E2E_CD_PORT           containerd gRPC port    (default 60002)
#   A1S_E2E_API_PORT          api port                (default 1906)
#   A1S_E2E_CONTAINER_PROXY   HTTP(S) proxy for the containers (Docker
#                             Desktop setups need the built-in proxy,
#                             e.g. http://http.docker.internal:3128)
set -eu

TOKEN="${A1S_E2E_TOKEN:-e2e-token}"
PG_PORT="${A1S_E2E_PG_PORT:-5434}"
CD_PORT="${A1S_E2E_CD_PORT:-60002}"
API_PORT="${A1S_E2E_API_PORT:-1906}"
BASE="http://127.0.0.1:${API_PORT}"
DSN="postgres://a1s:a1s@127.0.0.1:${PG_PORT}/a1s_e2e?sslmode=disable"
GOARCH="$(go env GOARCH)"

say() { echo "[e2e] $*"; }
die() { echo "[e2e] FAIL: $*" >&2; exit 1; }

command -v docker >/dev/null 2>&1 || die "docker is required"
command -v go >/dev/null 2>&1 || die "go is required"

cleanup() {
	say "cleanup (logs kept: e2e-api.log e2e-scheduler.log e2e-monitor.log)"
	pkill -f 'bin/a1s-e2e' 2>/dev/null || true
	docker exec a1s-e2e-containerd pkill -f 'a1s worker' 2>/dev/null || true
	docker rm -f a1s-e2e-pg a1s-e2e-containerd >/dev/null 2>&1 || true
	rm -f bin/a1s-e2e bin/a1s-e2e-linux
}
trap cleanup EXIT

docker rm -f a1s-e2e-pg a1s-e2e-containerd >/dev/null 2>&1 || true

say "start postgres (port ${PG_PORT})"
docker run -d --name a1s-e2e-pg \
	-e POSTGRES_USER=a1s -e POSTGRES_PASSWORD=a1s -e POSTGRES_DB=a1s_e2e \
	-p "127.0.0.1:${PG_PORT}:5432" postgres:16 >/dev/null

say "start the containerd node (port ${CD_PORT})"
proxy_env=""
if [ -n "${A1S_E2E_CONTAINER_PROXY:-}" ]; then
	proxy_env="-e HTTPS_PROXY=${A1S_E2E_CONTAINER_PROXY} -e HTTP_PROXY=${A1S_E2E_CONTAINER_PROXY} -e NO_PROXY=localhost,127.0.0.1"
fi
# shellcheck disable=SC2086
docker run -d --name a1s-e2e-containerd --privileged \
	-p "127.0.0.1:${CD_PORT}:60001" \
	--add-host=host.docker.internal:host-gateway \
	$proxy_env alpine:latest \
	sh -c 'for i in 1 2 3 4 5; do apk add --no-cache containerd socat runc containerd-ctr >/dev/null 2>&1 && break; sleep 3; done; (containerd >/var/log/containerd.log 2>&1 &) && sleep 2 && exec socat TCP-LISTEN:60001,fork,reuseaddr UNIX-CONNECT:/run/containerd/containerd.sock' >/dev/null

say "wait for the containerd gRPC endpoint"
deadline=$(( $(date +%s) + 90 ))
until docker exec a1s-e2e-containerd nc -z 127.0.0.1 60001 >/dev/null 2>&1; do
	[ "$(date +%s)" -lt "$deadline" ] || {
		docker logs --tail 10 a1s-e2e-containerd >&2 || true
		die "the containerd node never opened its gRPC endpoint"
	}
	sleep 2
done

say "build"
go build -o bin/a1s-e2e .
GOOS=linux GOARCH="$GOARCH" go build -o bin/a1s-e2e-linux .

say "wait for postgres"
deadline=$(( $(date +%s) + 60 ))
until docker exec a1s-e2e-pg pg_isready -U a1s >/dev/null 2>&1; do
	[ "$(date +%s)" -lt "$deadline" ] || die "postgres did not become ready"
	sleep 2
done

say "migrate"
A1S_DSN="$DSN" go run . db:migrate >/dev/null

say "start api, scheduler, monitor"
A1S_DSN="$DSN" A1S_INTERNAL_TOKEN="$TOKEN" AIRWAY_ENV=production AIRWAY_PORT="$API_PORT" \
	./bin/a1s-e2e api >e2e-api.log 2>&1 &
A1S_DSN="$DSN" ./bin/a1s-e2e scheduler >e2e-scheduler.log 2>&1 &
A1S_DSN="$DSN" ./bin/a1s-e2e monitor >e2e-monitor.log 2>&1 &

deadline=$(( $(date +%s) + 30 ))
until curl -s -m 2 -o /dev/null "$BASE/health"; do
	[ "$(date +%s)" -lt "$deadline" ] || die "api did not come up"
	sleep 1
done

say "start two workers inside the containerd node"
docker cp bin/a1s-e2e-linux a1s-e2e-containerd:/usr/local/bin/a1s
worker_env="-e A1S_INTERNAL_TOKEN=${TOKEN} -e A1S_API_URL=http://host.docker.internal:${API_PORT} -e A1S_CONTAINERD_ADDR=/run/containerd/containerd.sock -e A1S_CONTAINERD_SNAPSHOTTER=native -e NO_PROXY=localhost,127.0.0.1,host.docker.internal"
if [ -n "${A1S_E2E_CONTAINER_PROXY:-}" ]; then
	worker_env="$worker_env -e HTTPS_PROXY=${A1S_E2E_CONTAINER_PROXY} -e HTTP_PROXY=${A1S_E2E_CONTAINER_PROXY}"
fi
# shellcheck disable=SC2086
docker exec $worker_env -d a1s-e2e-containerd \
	/bin/sh -c '/usr/local/bin/a1s worker --name w-e2e-a > /tmp/w-a.log 2>&1'
# shellcheck disable=SC2086
docker exec $worker_env -d a1s-e2e-containerd \
	/bin/sh -c '/usr/local/bin/a1s worker --name w-e2e-b > /tmp/w-b.log 2>&1'

row_status() {
	docker exec a1s-e2e-pg psql -U a1s -d a1s_e2e -tAc \
		"SELECT status FROM containers WHERE name = '$1'" | tr -d '[:space:]'
}

row_worker() {
	docker exec a1s-e2e-pg psql -U a1s -d a1s_e2e -tAc \
		"SELECT w.name FROM containers c JOIN workers w ON w.id = c.worker_id WHERE c.name = '$1'" | tr -d '[:space:]'
}

wait_status() { # name, expected, deadline_seconds
	deadline=$(( $(date +%s) + $3 ))
	until [ "$(row_status "$1")" = "$2" ]; do
		[ "$(date +%s)" -lt "$deadline" ] || die "$1 did not reach $2 in ${3}s (status: $(row_status "$1"))"
		sleep 2
	done
}

say "run two containers through the API"
api_post() {
	curl -s -m 10 -X POST "$BASE/api/v1/containers" \
		-H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' "$@"
}

api_post -d '{"name":"e2e-one","image":"docker.io/library/busybox:latest","command":"sleep","args":["900"],"restart_policy":"on-failure"}' \
	| grep -q '"status":"pending"' || die "creating e2e-one failed"
api_post -d '{"name":"e2e-two","image":"docker.io/library/busybox:latest","command":"sleep","args":["900"],"restart_policy":"on-failure"}' \
	| grep -q '"status":"pending"' || die "creating e2e-two failed"

say "wait for both containers to run (pull included, 120s deadline)"
wait_status e2e-one running 120
wait_status e2e-two running 120

victim="$(row_worker e2e-one)"
survivor="$(row_worker e2e-two)"
[ -n "$victim" ] && [ -n "$survivor" ] || die "workers not assigned"
[ "$victim" != "$survivor" ] || say "both containers landed on $victim; the kill still exercises rescheduling"

pid_before="$(docker exec a1s-e2e-containerd ctr -n a1s tasks ls | awk '$1=="e2e-one" {print $2}')"
[ -n "$pid_before" ] || die "no running task for e2e-one"

say "chaos 1: kill -9 worker $victim (owns e2e-one)"
docker exec a1s-e2e-containerd pkill -9 -f "worker --name $victim"

say "wait for e2e-one to run on the survivor (detection window, 60s)"
deadline=$(( $(date +%s) + 60 ))
until [ "$(row_status e2e-one)" = "running" ] && [ "$(row_worker e2e-one)" = "$survivor" ]; do
	[ "$(date +%s)" -lt "$deadline" ] || die "e2e-one not rescheduled onto $survivor in 60s (status: $(row_status e2e-one), worker: $(row_worker e2e-one))"
	sleep 2
done
say "e2e-one rescheduled onto $survivor"

say "chaos 2: externally kill the e2e-one task (restart policy repairs it)"
docker exec a1s-e2e-containerd ctr -n a1s tasks kill -s KILL "e2e-one"

say "wait for the auto-restart (60s deadline)"
deadline=$(( $(date +%s) + 60 ))
until [ "$(row_status e2e-one)" = "running" ]; do
	[ "$(date +%s)" -lt "$deadline" ] || die "e2e-one did not auto-restart in 60s (status: $(row_status e2e-one))"
	sleep 2
done

pid_after="$(docker exec a1s-e2e-containerd ctr -n a1s tasks ls | awk '$1=="e2e-one" {print $2}')"
[ -n "$pid_after" ] || die "no running task for e2e-one after the restart"
[ "$pid_after" != "$pid_before" ] || die "the task pid did not change: no restart happened"

say "ALL ASSERTIONS PASSED"
