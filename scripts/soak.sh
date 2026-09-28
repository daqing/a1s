#!/bin/sh
# scripts/soak.sh — multi-instance soak acceptance (docs/TASKS.md T6.7):
#
#   2x api, 2x scheduler, 2x monitor, 3 workers on a containerd node, all
#   churning through randomized chaos (process kills with restarts, new
#   containers, stops, worker deaths) for A1S_SOAK_MINUTES (default 60).
#   After every chaos round the global invariants are asserted:
#
#     - every container row status is a legal enum value
#     - every scheduled/running container sits on an ACTIVE worker
#     - no container has more than one in-flight start command
#     - restart counters stay bounded (no infinite restart loops)
#     - both api instances answer
#
#   Exits 0 when the full soak completes with green invariants.
set -eu

MINUTES="${A1S_SOAK_MINUTES:-60}"
TOKEN="${A1S_SOAK_TOKEN:-soak-token}"
PG_PORT="${A1S_SOAK_PG_PORT:-5436}"
CD_PORT="${A1S_SOAK_CD_PORT:-60004}"
API1="${A1S_SOAK_API1:-1905}"
API2="${A1S_SOAK_API2:-1906}"
DSN="postgres://a1s:a1s@127.0.0.1:${PG_PORT}/a1s_soak?sslmode=disable"
GOARCH="$(go env GOARCH)"

say() { echo "[soak $(date +%H:%M:%S)] $*"; }
die() { echo "[soak] FAIL: $*" >&2; exit 1; }

command -v docker >/dev/null 2>&1 || die "docker is required"
command -v go >/dev/null 2>&1 || die "go is required"

cleanup() {
	say "cleanup (logs kept in soak-*.log)"
	pkill -f 'bin/a1s-soak' 2>/dev/null || true
	docker exec a1s-soak-containerd pkill -f 'a1s worker' 2>/dev/null || true
	docker rm -f a1s-soak-pg a1s-soak-containerd >/dev/null 2>&1 || true
	rm -f bin/a1s-soak bin/a1s-soak-linux
}
trap cleanup EXIT

docker rm -f a1s-soak-pg a1s-soak-containerd >/dev/null 2>&1 || true

say "start postgres (port ${PG_PORT}) and the containerd node (port ${CD_PORT})"
docker run -d --name a1s-soak-pg \
	-e POSTGRES_USER=a1s -e POSTGRES_PASSWORD=a1s -e POSTGRES_DB=a1s_soak \
	-p "127.0.0.1:${PG_PORT}:5432" postgres:16 >/dev/null
docker run -d --name a1s-soak-containerd --privileged \
	-p "127.0.0.1:${CD_PORT}:60001" \
	--add-host=host.docker.internal:host-gateway \
	alpine:latest \
	sh -c 'for i in 1 2 3 4 5; do apk add --no-cache containerd socat runc containerd-ctr >/dev/null 2>&1 && break; sleep 3; done; (containerd >/var/log/containerd.log 2>&1 &) && sleep 2 && exec socat TCP-LISTEN:60001,fork,reuseaddr UNIX-CONNECT:/run/containerd/containerd.sock' >/dev/null

say "build"
go build -o bin/a1s-soak .
GOOS=linux GOARCH="$GOARCH" go build -o bin/a1s-soak-linux .

deadline=$(( $(date +%s) + 90 ))
until docker exec a1s-soak-pg pg_isready -U a1s >/dev/null 2>&1 &&
	docker exec a1s-soak-containerd nc -z 127.0.0.1 60001 >/dev/null 2>&1; do
	[ "$(date +%s)" -lt "$deadline" ] || die "the containers never became ready"
	sleep 2
done

say "migrate"
A1S_DSN="$DSN" go run . db:migrate >/dev/null

say "start 2x api, 2x scheduler, 2x monitor"
A1S_DSN="$DSN" A1S_INTERNAL_TOKEN="$TOKEN" AIRWAY_ENV=production AIRWAY_PORT="$API1" \
	./bin/a1s-soak api >soak-api1.log 2>&1 &
A1_PID=$!
A1S_DSN="$DSN" A1S_INTERNAL_TOKEN="$TOKEN" AIRWAY_ENV=production AIRWAY_PORT="$API2" \
	./bin/a1s-soak api >soak-api2.log 2>&1 &
A2_PID=$!
A1S_DSN="$DSN" ./bin/a1s-soak scheduler >soak-s1.log 2>&1 &
S1_PID=$!
A1S_DSN="$DSN" ./bin/a1s-soak scheduler >soak-s2.log 2>&1 &
S2_PID=$!
A1S_DSN="$DSN" A1S_HEARTBEAT_TIMEOUT=10s ./bin/a1s-soak monitor >soak-m1.log 2>&1 &
M1_PID=$!
A1S_DSN="$DSN" A1S_HEARTBEAT_TIMEOUT=10s ./bin/a1s-soak monitor >soak-m2.log 2>&1 &
M2_PID=$!

deadline=$(( $(date +%s) + 30 ))
until curl -s -m 2 -o /dev/null "http://127.0.0.1:${API1}/health" &&
	curl -s -m 2 -o /dev/null "http://127.0.0.1:${API2}/health"; do
	[ "$(date +%s)" -lt "$deadline" ] || die "the apis did not come up"
	sleep 1
done

say "start 3 workers inside the containerd node"
docker cp bin/a1s-soak-linux a1s-soak-containerd:/usr/local/bin/a1s
worker_env="-e A1S_INTERNAL_TOKEN=${TOKEN} -e A1S_API_URL=http://host.docker.internal:${API1} -e A1S_CONTAINERD_ADDR=/run/containerd/containerd.sock -e A1S_CONTAINERD_SNAPSHOTTER=native"
docker exec $worker_env -d a1s-soak-containerd /bin/sh -c '/usr/local/bin/a1s worker --name w-soak-1 > /tmp/w1.log 2>&1'
docker exec $worker_env -d a1s-soak-containerd /bin/sh -c '/usr/local/bin/a1s worker --name w-soak-2 > /tmp/w2.log 2>&1'
docker exec $worker_env -d a1s-soak-containerd /bin/sh -c '/usr/local/bin/a1s worker --name w-soak-3 > /tmp/w3.log 2>&1'

row_status() {
	docker exec a1s-soak-pg psql -U a1s -d a1s_soak -tAc "$1" | tr -d '[:space:]'
}

# assert_invariants dies when the system is in an impossible state.
assert_invariants() {
	bad_status="$(row_status "SELECT count(*) FROM containers
		WHERE status NOT IN ('pending','scheduled','running','stopped','failed','lost')")"
	[ "$bad_status" = "0" ] || die "containers with an illegal status: $bad_status"

	orphaned="$(row_status "SELECT count(*) FROM containers c
		JOIN workers w ON w.id = c.worker_id
		WHERE c.status IN ('scheduled','running') AND w.status <> 'active'")"
	[ "$orphaned" = "0" ] || die "containers scheduled/running on a non-active worker: $orphaned"

	dup="$(row_status "SELECT count(*) FROM (
		SELECT container_id FROM commands
		WHERE action = 'start' AND status IN ('queued','delivered')
		GROUP BY container_id HAVING count(*) > 1) d")"
	[ "$dup" = "0" ] || die "containers with duplicate in-flight start commands: $dup"

	runaway="$(row_status "SELECT coalesce(max(restart_count), 0) FROM containers")"
	[ "${runaway:-0}" -lt 20 ] || die "a container is stuck in a restart loop (count $runaway)"

	curl -s -m 2 -o /dev/null "http://127.0.0.1:${API1}/health" || die "api instance 1 is down"
	curl -s -m 2 -o /dev/null "http://127.0.0.1:${API2}/health" || die "api instance 2 is down"
}

restart_proc() { # pattern-match, log-prefix
	pkill -9 -f "$1" 2>/dev/null || true
}

say "soak started: $MINUTES minutes of randomized chaos"
END=$(( $(date +%s) + MINUTES * 60 ))
round=0
created=0
killed=0

while [ "$(date +%s)" -lt "$END" ]; do
	round=$((round + 1))

	case $((round % 6)) in
	0)
		# run a new container, alternating between the two apis
		if [ $((round % 12)) = 0 ]; then BASE_API="$API1"; else BASE_API="$API2"; fi
		curl -s -m 10 -X POST "http://127.0.0.1:${BASE_API}/api/v1/containers" \
			-H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
			-d "{\"name\":\"soak-$round\",\"image\":\"docker.io/library/busybox:latest\",\"command\":\"sleep\",\"args\":[\"3600\"],\"restart_policy\":\"on-failure\"}" \
			| grep -q '"pending"' || die "round $round: run failed"
		created=$((created + 1))
		say "round $round: ran soak-$round (created total $created)"
		;;
	1)
		# stop a random running container
		name="$(row_status "SELECT name FROM containers WHERE status = 'running' ORDER BY random() LIMIT 1")"
		if [ -n "$name" ]; then
			id="$(row_status "SELECT id FROM containers WHERE name = '$name'")"
			curl -s -m 10 -X POST "http://127.0.0.1:${API1}/api/v1/containers/$id/stop" \
				-H "Authorization: Bearer $TOKEN" >/dev/null || true
			say "round $round: stopped $name (id $id)"
		fi
		;;
	2)
		# kill -9 one scheduler by pid; the other instance keeps scheduling
		if [ $((round % 4)) = 2 ]; then
			kill -9 "$S1_PID" 2>/dev/null || true
			A1S_DSN="$DSN" ./bin/a1s-soak scheduler >soak-s1.log 2>&1 &
			S1_PID=$!
		else
			kill -9 "$S2_PID" 2>/dev/null || true
			A1S_DSN="$DSN" ./bin/a1s-soak scheduler >soak-s2.log 2>&1 &
			S2_PID=$!
		fi
		killed=$((killed + 1))
		say "round $round: killed+restarted a scheduler (kills total $killed)"
		;;
	3)
		# kill -9 one monitor by pid
		if [ $((round % 4)) = 3 ]; then
			kill -9 "$M1_PID" 2>/dev/null || true
			A1S_DSN="$DSN" A1S_HEARTBEAT_TIMEOUT=10s ./bin/a1s-soak monitor >soak-m1.log 2>&1 &
			M1_PID=$!
		else
			kill -9 "$M2_PID" 2>/dev/null || true
			A1S_DSN="$DSN" A1S_HEARTBEAT_TIMEOUT=10s ./bin/a1s-soak monitor >soak-m2.log 2>&1 &
			M2_PID=$!
		fi
		killed=$((killed + 1))
		say "round $round: killed+restarted a monitor (kills total $killed)"
		;;
	4)
		# kill -9 one api by pid on its fixed port
		if [ $((round % 8)) = 4 ]; then
			kill -9 "$A1_PID" 2>/dev/null || true
			A1S_DSN="$DSN" A1S_INTERNAL_TOKEN="$TOKEN" AIRWAY_ENV=production AIRWAY_PORT="$API1" \
				./bin/a1s-soak api >soak-api1.log 2>&1 &
			A1_PID=$!
		else
			kill -9 "$A2_PID" 2>/dev/null || true
			A1S_DSN="$DSN" A1S_INTERNAL_TOKEN="$TOKEN" AIRWAY_ENV=production AIRWAY_PORT="$API2" \
				./bin/a1s-soak api >soak-api2.log 2>&1 &
			A2_PID=$!
		fi
		killed=$((killed + 1))
		say "round $round: killed+restarted an api (kills total $killed)"
		;;
	5)
		# kill -9 exactly one worker by name: its row goes lost and its
		# containers migrate while a fresh worker registers
		restart_proc 'a1s worker --name w-soak-1'
		sleep 1
		docker exec $worker_env -d a1s-soak-containerd /bin/sh -c \
			'/usr/local/bin/a1s worker --name w-soak-1 > /tmp/w1.log 2>&1'
		killed=$((killed + 1))
		say "round $round: killed+replaced worker w-soak-1 (kills total $killed)"
		;;
	esac

	# let the detection window (T=10s, M=2s... monitor interval 5s default
	# here) and one scheduling round settle before asserting
	sleep 25
	assert_invariants
	say "round $round: invariants green"
done

say "final invariant sweep"
assert_invariants
totals="$(row_status "SELECT status || ':' || count(*) FROM containers GROUP BY status ORDER BY status")"
say "final container state: $(echo "$totals" | tr '\n' ' ')"
say "created $created containers, performed $killed kills across $round rounds — SOAK PASSED"
