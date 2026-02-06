package client

import (
	"context"
	"crypto/tls"
	"encoding/hex"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"

	"lukechampine.com/blake3"
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
		dest := filepath.Join(outDir, f.Path)
		if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
			return fmt.Errorf("mkdir: %w", err)
		}

		needDownload := true
		if existsHash, err := hashIfExists(dest); err == nil {
			log.Printf("hash check: dest=%s local=%s remote=%s", dest, existsHash, f.Hash)
			if existsHash == f.Hash {
				needDownload = false
			}
		} else if !os.IsNotExist(err) {
			log.Printf("hash check error for %s: %v", dest, err)
		}

		// send decision to server: 1 = send, 0 = skip
		if _, err := conn.Write([]byte{boolToByte(needDownload)}); err != nil {
			return fmt.Errorf("send decision: %w", err)
		}

		if !needDownload {
			log.Printf("skip download (hash match): %s", dest)
			continue
		}

		log.Printf("start download: %s size=%d", dest, f.Size)
		tmpDest := dest + ".part"
		out, err := os.Create(tmpDest)
		if err != nil {
			return fmt.Errorf("create %s: %w", tmpDest, err)
		}
		if err := copyExactly(out, conn, f.Size); err != nil {
			out.Close()
			return fmt.Errorf("download %s: %w", f.Path, err)
		}
		out.Close()
		if err := os.Rename(tmpDest, dest); err != nil {
			return fmt.Errorf("rename %s: %w", dest, err)
		}
		log.Printf("finished download: %s", dest)
	}
	return nil
}

func hashIfExists(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := blake3.New(32, nil)
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func boolToByte(b bool) byte {
	if b {
		return 1
	}
	return 0
}
