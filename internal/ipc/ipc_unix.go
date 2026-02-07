//go:build !windows

package ipc

import (
	"context"
	"fmt"
	"net"
	"os"
	"path/filepath"
)

func Listen(endpoint string) (net.Listener, error) {
	ep := NormalizeEndpoint(endpoint)
	if err := os.MkdirAll(filepath.Dir(ep), 0o755); err != nil {
		return nil, fmt.Errorf("mkdir ipc dir: %w", err)
	}
	if err := os.Remove(ep); err != nil && !os.IsNotExist(err) {
		return nil, fmt.Errorf("remove stale socket: %w", err)
	}

	ln, err := net.Listen("unix", ep)
	if err != nil {
		return nil, err
	}
	if err := os.Chmod(ep, 0o600); err != nil {
		ln.Close()
		return nil, fmt.Errorf("chmod ipc socket: %w", err)
	}
	return ln, nil
}

func DialContext(endpoint string) func(context.Context, string, string) (net.Conn, error) {
	ep := NormalizeEndpoint(endpoint)
	d := &net.Dialer{}
	return func(ctx context.Context, _, _ string) (net.Conn, error) {
		return d.DialContext(ctx, "unix", ep)
	}
}
