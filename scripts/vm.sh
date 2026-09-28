#!/bin/sh
# scripts/vm.sh — Lima-only local cluster, no Docker and no Docker Desktop
# (docs/TASKS.md T6.5/T6.6 alternative): one Ubuntu VM hosts containerd,
# PostgreSQL and three workers natively; the control plane (api, scheduler,
# monitor) runs on the host and reaches the database through Lima's port
# forwarding. The worker shares the VM filesystem with its containerd, so
# the client-side layer application just works.
#
#   vm.sh up     create/provision the VM on first use, then start the cluster
#   vm.sh down   stop the a1s processes (host + VM); the VM keeps running
#
# State: the VM is persistent — `limactl delete a1s` resets it. Logs live in
# .cluster/.
set -eu

VM="${A1S_VM_NAME:-a1s}"
DIR="$(cd "$(dirname "$0")/.." && pwd)"
STATE="$DIR/.cluster"
API_PORT="${A1S_CLUSTER_API_PORT:-1905}"
DSN="postgres://a1s:a1s@127.0.0.1:5437/a1s?sslmode=disable"
TOKEN="${A1S_VM_TOKEN:-dev-token}"

say() { echo "[vm] $*"; }
running() { [ -f "$STATE/$1.pid" ] && kill -0 "$(cat "$STATE/$1.pid")" 2>/dev/null; }

vm_running() {
	limactl list 2>/dev/null | awk '{print $1}' | grep -qx "$VM"
}

provision() {
	# containerd, PostgreSQL and curl inside the VM (idempotent)
	say "installing containerd and PostgreSQL inside the VM (first start only)"
	limactl shell "$VM" -- sudo apt-get update -qq
	limactl shell "$VM" -- sudo apt-get install -y -qq containerd postgresql curl >/dev/null

	limactl shell "$VM" -- sudo sh -c 'pgrep -x containerd >/dev/null || (nohup containerd >/var/log/containerd.log 2>&1 &)'

	limactl shell "$VM" -- sudo service postgresql start
	# the VM's postgres must not collide with a native postgres on the host's
	# 5432: move it to 5437, which Lima auto-forwards to host 5437
	limactl shell "$VM" -- sudo sh -c \
		'sed -i "s/^#\?port = .*/port = 5437/" /etc/postgresql/*/main/postgresql.conf && service postgresql restart'
	limactl shell "$VM" -- sudo -u postgres psql -tc \
		"SELECT 1 FROM pg_roles WHERE rolname='a1s'" | grep -q 1 ||
		limactl shell "$VM" -- sudo -u postgres psql -c "CREATE ROLE a1s LOGIN PASSWORD 'a1s' SUPERUSER"
	limactl shell "$VM" -- sudo -u postgres psql -tAc \
		"SELECT 1 FROM pg_database WHERE datname='a1s'" | grep -q 1 ||
		limactl shell "$VM" -- sudo -u postgres psql -c "CREATE DATABASE a1s OWNER a1s"

	say "migrate"
	(cd "$DIR" && A1S_DSN="$DSN" go run . db:migrate >/dev/null)

	say "provisioning done"
}

start_workers() {
	say "start three workers inside the VM"
	limactl shell "$VM" -- sudo pkill -f 'a1s worker' 2>/dev/null || true
	for n in 1 2 3; do
		limactl shell "$VM" -- sh -c \
			"A1S_INTERNAL_TOKEN='$TOKEN' A1S_API_URL='http://127.0.0.1:${API_PORT}' A1S_CONTAINERD_ADDR=/run/containerd/containerd.sock A1S_CONTAINERD_SNAPSHOTTER=overlayfs nohup $DIR/bin/a1s-vm worker --name w-vm-$n >/tmp/w$n.log 2>&1 &"
	done
}

up() {
	mkdir -p "$STATE"
	command -v limactl >/dev/null 2>&1 || {
		echo "[vm] FAIL: limactl is required (brew install lima)" >&2
		exit 1
	}

	if vm_running; then
		say "VM $VM already running"
	else
		say "creating VM $VM (template ubuntu; first start downloads the image)"
		limactl start --name "$VM" template:ubuntu --tty=false
	fi

	provision

	say "deploy the linux worker binary into the VM"
	GOARCH="$(limactl shell "$VM" -- uname -m | tr -d '[:space:]')"
	case "$GOARCH" in
	x86_64) GOARCH=amd64 ;;
	aarch64) GOARCH=arm64 ;;
	esac
	(cd "$DIR" && GOOS=linux GOARCH="$GOARCH" go build -o bin/a1s-vm .)
	# the repo lives under Lima's home mount, so the same absolute path is
	# visible inside the VM with identical content
	limactl cp "$DIR/bin/a1s-vm" "$VM:$DIR/bin/a1s-vm"
	limactl shell "$VM" -- chmod +x "$DIR/bin/a1s-vm"

	say "start the control plane on the host"
	start_process api env A1S_DSN="$DSN" A1S_INTERNAL_TOKEN="$TOKEN" AIRWAY_ENV=production AIRWAY_PORT="$API_PORT" \
		"$DIR/bin/a1s" api
	start_process scheduler env A1S_DSN="$DSN" "$DIR/bin/a1s" scheduler
	start_process monitor env A1S_DSN="$DSN" "$DIR/bin/a1s" monitor

	start_workers

	deadline=$(( $(date +%s) + 30 ))
	until curl -s -m 2 -o /dev/null "http://127.0.0.1:${API_PORT}/health"; do
		[ "$(date +%s)" -lt "$deadline" ] || {
			echo "[vm] FAIL: the api never came up (log $STATE/api.log)" >&2
			exit 1
		}
		sleep 1
	done

	say "cluster is up: api on ${API_PORT} (host), workers in VM $VM, database forwarded from the VM on 5437"
	say "export A1S_API_URL=http://127.0.0.1:${API_PORT} A1S_DSN='$DSN' A1S_INTERNAL_TOKEN='$TOKEN' before using the CLI"
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

down() {
	say "stopping host processes"
	for name in api scheduler monitor; do
		if running "$name"; then
			kill "$(cat "$STATE/$name.pid")" 2>/dev/null || true
			rm -f "$STATE/$name.pid"
		fi
	done

	if vm_running; then
		say "stopping workers inside the VM"
		limactl shell "$VM" -- sudo pkill -f 'a1s worker' 2>/dev/null || true
	fi

	say "cluster down (the VM keeps running; limactl stop $VM to pause it, limactl delete $VM to reset it)"
}

case "${1:-}" in
up) up ;;
down) down ;;
*)
	echo "usage: scripts/vm.sh up|down" >&2
	exit 2
	;;
esac
