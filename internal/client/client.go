package client

import (
	"context"
	"crypto/tls"
	"fmt"
	"os"
	"path/filepath"
)

// Download connects to addr over TLS, validates server, reads manifest, then downloads files into outDir.
func Download(ctx context.Context, addr string, tlsCfg *tls.Config, outDir string) error {
	dialer := &tls.Dialer{Config: tlsCfg}
	conn, err := dialer.DialContext(ctx, "tcp", addr)
	if err != nil {
		return fmt.Errorf("dial tls: %w", err)
	}
	defer conn.Close()

	manifest, err := readManifest(conn)
	if err != nil {
		return fmt.Errorf("read manifest: %w", err)
	}

	for _, f := range manifest.Files {
		path := filepath.Join(outDir, f.Name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return fmt.Errorf("mkdir: %w", err)
		}
		out, err := os.Create(path)
		if err != nil {
			return fmt.Errorf("create %s: %w", path, err)
		}
		if err := copyExactly(out, conn, f.Size); err != nil {
			out.Close()
			return fmt.Errorf("download %s: %w", f.Name, err)
		}
		out.Close()
	}
	return nil
}
