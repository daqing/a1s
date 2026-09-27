// Package worker implements the worker agent process: it runs on every
// worker node, manages containers through containerd, and talks to the
// control plane API (see docs/ROADMAP.md, Phase 3).
package worker

import (
	"context"
	"net"
	"strings"
	"time"

	"github.com/containerd/containerd/v2/client"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

// Dial connects to a containerd daemon. A "tcp://host:port" address is
// dialed over TCP (the dev containerd runs behind a socat forward);
// anything else is treated as a unix socket path, the normal production
// case.
func Dial(ctx context.Context, address string) (*client.Client, error) {
	if !strings.HasPrefix(address, "tcp://") {
		return client.New(address, client.WithTimeout(10*time.Second))
	}

	conn, err := grpc.NewClient("passthrough:///"+strings.TrimPrefix(address, "tcp://"),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithContextDialer(func(ctx context.Context, addr string) (net.Conn, error) {
			d := net.Dialer{Timeout: 10 * time.Second}
			return d.DialContext(ctx, "tcp", addr)
		}))
	if err != nil {
		return nil, err
	}

	return client.NewWithConn(conn)
}
