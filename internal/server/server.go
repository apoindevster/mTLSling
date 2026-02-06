package server

import (
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"sync"
	"time"
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
	ID     string
	Remote string
	State  string // e.g., "accepted", "completed", "error"
	Err    error
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
		events:      make(chan ConnEvent, 8),
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
		entries = append(entries, FileEntry{Name: filepath.Base(path), Size: info.Size()})
	}

	if err := WriteManifest(c, Manifest{Files: entries}); err != nil {
		s.events <- ConnEvent{ID: id, Remote: c.RemoteAddr().String(), State: "error", Err: err}
		return
	}

	for i, entry := range entries {
		f, err := os.Open(files[i])
		if err != nil {
			s.events <- ConnEvent{ID: id, Remote: c.RemoteAddr().String(), State: "error", Err: err}
			return
		}
		if _, err := io.CopyN(c, f, entry.Size); err != nil {
			f.Close()
			s.events <- ConnEvent{ID: id, Remote: c.RemoteAddr().String(), State: "error", Err: err}
			return
		}
		f.Close()
	}
	s.events <- ConnEvent{ID: id, Remote: c.RemoteAddr().String(), State: "completed"}
}
