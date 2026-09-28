dev:
  overmind start -f Procfile.dev

cluster:
  scripts/cluster.sh up

cluster-down:
  scripts/cluster.sh down

vm:
  scripts/vm.sh up

vm-down:
  scripts/vm.sh down

install-deps:
  go install github.com/air-verse/air@latest
  brew install tmux
  brew install overmind
