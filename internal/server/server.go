package server

import (
	"context"
	"crypto/tls"
	"encoding/hex"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"lukechampine.com/blake3"
)

// FileProvider returns the list of files to serve.
type FileProvider interface {
	ListFiles() []string
}

// FileProviderFunc adapts a function.
type FileProviderFunc func() []string

// ListFiles implements FileProvider.
func (f FileProviderFunc) ListFiles() []string { return f() }

// ConnEvent describes a connection lifecycle change.
type ConnEvent struct {
	ID          string
	Remote      string
	State       string // e.g., "accepted", "completed", "error"
	Err         error
	File        string
	Transferred int64
	Total       int64
}

// Server handles TLS file transfers.
type Server struct {
	addr        string
	tlsConfig   *tls.Config
	provider    FileProvider
	ln          net.Listener
	events      chan ConnEvent
	mu          sync.Mutex
	stopped     chan struct{}
	connections map[string]net.Conn
}

// New creates a server instance.
func New(addr string, tlsCfg *tls.Config, provider FileProvider) *Server {
	return &Server{
		addr:        addr,
		tlsConfig:   tlsCfg,
		provider:    provider,
		events:      make(chan ConnEvent, 64),
		stopped:     make(chan struct{}),
		connections: make(map[string]net.Conn),
	}
}

// Events exposes connection events channel.
func (s *Server) Events() <-chan ConnEvent { return s.events }

// Start begins listening and serving until ctx is cancelled or Stop is called.
func (s *Server) Start(ctx context.Context) error {
	ln, err := tls.Listen("tcp", s.addr, s.tlsConfig)
	if err != nil {
		return fmt.Errorf("listen: %w", err)
	}
	s.ln = ln

	go func() {
		<-ctx.Done()
		s.Stop()
	}()

	for {
		conn, err := ln.Accept()
		if err != nil {
			select {
			case <-s.stopped:
				return nil
			default:
			}
			return fmt.Errorf("accept: %w", err)
		}
		id := fmt.Sprintf("%d", time.Now().UnixNano())
		s.mu.Lock()
		s.connections[id] = conn
		s.mu.Unlock()
		s.events <- ConnEvent{ID: id, Remote: conn.RemoteAddr().String(), State: "accepted"}
		go s.handleConn(ctx, id, conn)
	}
}

// Stop stops the server.
func (s *Server) Stop() {
	s.mu.Lock()
	defer s.mu.Unlock()
	select {
	case <-s.stopped:
		return
	default:
		close(s.stopped)
	}
	if s.ln != nil {
		s.ln.Close()
	}
	for id, c := range s.connections {
		c.Close()
		delete(s.connections, id)
	}
}

func (s *Server) handleConn(ctx context.Context, id string, c net.Conn) {
	defer func() {
		c.Close()
		s.mu.Lock()
		delete(s.connections, id)
		s.mu.Unlock()
	}()

	files := s.provider.ListFiles()
	entries := make([]FileEntry, 0, len(files))
	for _, path := range files {
		info, err := os.Stat(path)
		if err != nil {
			s.events <- ConnEvent{ID: id, Remote: c.RemoteAddr().String(), State: "error", Err: fmt.Errorf("stat %s: %w", path, err)}
			return
		}
		if info.IsDir() {
			continue
		}
		hash, err := fileHash(path)
		if err != nil {
			s.events <- ConnEvent{ID: id, Remote: c.RemoteAddr().String(), State: "error", Err: fmt.Errorf("hash %s: %w", path, err)}
			return
		}
		rel := relativePath(path)
		entries = append(entries, FileEntry{Name: filepath.Base(path), Path: rel, Size: info.Size(), Hash: hash})
	}

	if err := WriteManifest(c, Manifest{Files: entries}); err != nil {
		s.events <- ConnEvent{ID: id, Remote: c.RemoteAddr().String(), State: "error", Err: err}
		return
	}

	buf := make([]byte, 1)
	for i, entry := range entries {
		// Wait for client decision: 1 = send, 0 = skip
		if _, err := io.ReadFull(c, buf); err != nil {
			s.events <- ConnEvent{ID: id, Remote: c.RemoteAddr().String(), State: "error", Err: fmt.Errorf("read decision: %w", err)}
			return
		}
		if buf[0] == 0 {
			continue
		}

		f, err := os.Open(files[i])
		if err != nil {
			s.events <- ConnEvent{ID: id, Remote: c.RemoteAddr().String(), State: "error", Err: err}
			return
		}
		pw := &progressWriter{
			Conn:   c,
			ID:     id,
			Remote: c.RemoteAddr().String(),
			File:   entry.Path,
			Total:  entry.Size,
			Events: s.events,
		}
		if _, err := io.CopyN(pw, f, entry.Size); err != nil {
			f.Close()
			s.events <- ConnEvent{ID: id, Remote: c.RemoteAddr().String(), State: "error", Err: err}
			return
		}
		f.Close()
	}
	s.events <- ConnEvent{ID: id, Remote: c.RemoteAddr().String(), State: "completed"}
}

// progressWriter reports copy progress while writing to a connection.
type progressWriter struct {
	Conn   net.Conn
	ID     string
	Remote string
	File   string
	Total  int64
	Sent   int64
	last   time.Time
	Events chan<- ConnEvent
}

func (w *progressWriter) Write(p []byte) (int, error) {
	n, err := w.Conn.Write(p)
	w.Sent += int64(n)
	now := time.Now()
	if w.Total > 0 && (now.Sub(w.last) > 150*time.Millisecond || w.Sent == w.Total) {
		select {
		case w.Events <- ConnEvent{
			ID:          w.ID,
			Remote:      w.Remote,
			State:       "progress",
			File:        w.File,
			Transferred: w.Sent,
			Total:       w.Total,
		}:
		default:
			// drop if channel is full to avoid blocking
		}
		w.last = now
	}
	return n, err
}

// fileHash returns the SHA-256 hex digest of the file.
func fileHash(path string) (string, error) {
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

// relativePath produces a path without volume/leading separators so it can be joined under the destination root.
func relativePath(path string) string {
	clean := filepath.Clean(path)
	if vol := filepath.VolumeName(clean); vol != "" {
		clean = strings.TrimPrefix(clean, vol)
	}
	clean = strings.TrimPrefix(clean, string(filepath.Separator))
	return clean
}
