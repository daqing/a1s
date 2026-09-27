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
	"github.com/containerd/containerd/v2/pkg/namespaces"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
)

// Dial connects to a containerd daemon. A "tcp://host:port" address is
// dialed over TCP (the dev containerd runs behind a socat forward);
// anything else is treated as a unix socket path, the normal production
// case. Every call on the client is namespaced — client.New attaches those
// interceptors itself, the TCP path needs them spelled out on the
// connection.
func Dial(ctx context.Context, address, namespace string) (*client.Client, error) {
	if !strings.HasPrefix(address, "tcp://") {
		return client.New(address,
			client.WithDefaultNamespace(namespace),
			client.WithTimeout(10*time.Second))
	}

	conn, err := grpc.NewClient("passthrough:///"+strings.TrimPrefix(address, "tcp://"),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithContextDialer(func(ctx context.Context, addr string) (net.Conn, error) {
			d := net.Dialer{Timeout: 10 * time.Second}
			return d.DialContext(ctx, "tcp", addr)
		}),
		grpc.WithChainUnaryInterceptor(nsUnaryInterceptor(namespace)),
		grpc.WithChainStreamInterceptor(nsStreamInterceptor(namespace)))
	if err != nil {
		return nil, err
	}

	return client.NewWithConn(conn, client.WithDefaultNamespace(namespace))
}

func nsUnaryInterceptor(namespace string) grpc.UnaryClientInterceptor {
	return func(ctx context.Context, method string, req, reply any,
		cc *grpc.ClientConn, invoker grpc.UnaryInvoker, opts ...grpc.CallOption) error {
		return invoker(metadata.AppendToOutgoingContext(ctx, namespaces.GRPCHeader, namespace),
			method, req, reply, cc, opts...)
	}
}

func nsStreamInterceptor(namespace string) grpc.StreamClientInterceptor {
	return func(ctx context.Context, desc *grpc.StreamDesc, cc *grpc.ClientConn, method string,
		streamer grpc.Streamer, opts ...grpc.CallOption) (grpc.ClientStream, error) {
		return streamer(metadata.AppendToOutgoingContext(ctx, namespaces.GRPCHeader, namespace),
			desc, cc, method, opts...)
	}
}
