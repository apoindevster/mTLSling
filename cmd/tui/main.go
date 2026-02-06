package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"flag"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/filepicker"
	"github.com/charmbracelet/bubbles/list"
	tea "github.com/charmbracelet/bubbletea"

	"multi-cast-transfer/internal/config"
	"multi-cast-transfer/internal/discovery"
	"multi-cast-transfer/internal/server"
)

// messages

// message emitted when a connection event occurs.
type connEventMsg server.ConnEvent

// message emitted when long-running command encounters an error.
type errMsg struct{ err error }

// message emitted when server goroutine stops.
type serverStoppedMsg struct{}

// message emitted when broadcast stops.
type broadcastStoppedMsg struct{}

// model holds UI state.
type model struct {
	fp         filepicker.Model
	files      []string
	events     []string
	list       list.Model
	running    bool
	addr       string
	multicast  string
	serverName string
	id         string
	insecure   bool

	tlsCert tls.Certificate
	roots   *x509.CertPool

	srv    *server.Server
	runCtx context.Context
	cancel context.CancelFunc
	advert *discovery.Advert
}

func newModel(cert tls.Certificate, roots *x509.CertPool, addr, multicast, serverName, id string, insecure bool) model {
	fp := filepicker.New()
	fp.ShowHidden = false
	fp.AutoHeight = true
	fp.SetHeight(12) // sensible default so files are visible immediately
	lst := list.New([]list.Item{}, list.NewDefaultDelegate(), 0, 0)
	lst.DisableQuitKeybindings()
	lst.SetShowStatusBar(false)
	lst.SetFilteringEnabled(false)
	lst.Title = "Connections"
	return model{
		fp:         fp,
		list:       lst,
		addr:       addr,
		multicast:  multicast,
		serverName: serverName,
		id:         id,
		insecure:   insecure,
		tlsCert:    cert,
		roots:      roots,
	}
}

func (m model) Init() tea.Cmd {
	return m.fp.Init()
}

// Update handles all messages.
func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.String() {
		case "ctrl+c", "q":
			if m.running && m.cancel != nil {
				m.cancel()
			}
			return m, tea.Quit
		case "s":
			if m.running {
				if m.cancel != nil {
					m.cancel()
				}
				return m, nil
			}
			cmd, err := m.start()
			if err != nil {
				m.events = append([]string{fmt.Sprintf("error: %v", err)}, m.events...)
				return m, nil
			}
			return m, cmd
		}

	case connEventMsg:
		ev := server.ConnEvent(msg)
		line := fmt.Sprintf("%s: %s (%s)", ev.State, ev.Remote, ev.ID)
		if ev.Err != nil {
			line = fmt.Sprintf("error: %v", ev.Err)
		}
		m.events = append([]string{line}, m.events...)
		m.list.SetItems(itemsFromEvents(m.events))
		return m, listenEventsCmd(m.srv.Events())

	case errMsg:
		m.events = append([]string{fmt.Sprintf("error: %v", msg.err)}, m.events...)
		m.running = false
		return m, nil

	case serverStoppedMsg:
		m.running = false
		return m, nil

	case broadcastStoppedMsg:
		return m, nil

	case tea.WindowSizeMsg:
		// Keep the file picker at a reasonable height relative to the terminal.
		h := msg.Height - 10
		if h < 6 {
			h = 6
		}
		m.fp.SetHeight(h)
		return m, nil
	}

	// default: update picker
	var cmd tea.Cmd
	m.fp, cmd = m.fp.Update(msg)
	if ok, path := m.fp.DidSelectFile(msg); ok {
		m.files = append(m.files, path)
		m.events = append([]string{fmt.Sprintf("added file %s", path)}, m.events...)
	}
	return m, cmd
}

func (m model) View() string {
	builder := &strings.Builder{}
	status := "stopped"
	if m.running {
		status = "running"
	}
	fmt.Fprintf(builder, "TUI sender - %s\n", status)
	fmt.Fprintf(builder, "Listen: %s\n", m.addr)
	fmt.Fprintf(builder, "Multicast: %s\n", m.multicast)
	fmt.Fprintf(builder, "Files (%d):\n", len(m.files))
	for _, f := range m.files {
		fmt.Fprintf(builder, "  - %s\n", f)
	}
	fmt.Fprintln(builder)
	fmt.Fprintln(builder, m.fp.View())
	fmt.Fprintln(builder)
	fmt.Fprintln(builder, "Connections:")
	for i, e := range m.events {
		if i >= 8 {
			break
		}
		fmt.Fprintf(builder, "  %s\n", e)
	}
	fmt.Fprintln(builder)
	fmt.Fprintln(builder, "Keys: arrows/jk navigate, enter to select file, s=start/stop, q=quit")
	return builder.String()
}

// start spins up the TLS server and multicast broadcaster.
func (m *model) start() (tea.Cmd, error) {
	if len(m.files) == 0 {
		return nil, fmt.Errorf("no files selected")
	}

	tlsCfg := config.ServerTLSConfig(m.tlsCert, m.roots, m.insecure)
	srv := server.New(m.addr, tlsCfg, server.FileProviderFunc(func() []string { return m.files }))

	advert, err := discovery.NewAdvert(m.id, m.addr, m.serverName, m.tlsCert)
	if err != nil {
		return nil, err
	}
	m.advert = advert

	ctx, cancel := context.WithCancel(context.Background())
	m.runCtx = ctx
	m.cancel = cancel
	m.srv = srv
	m.running = true

	return tea.Batch(
		runServerCmd(srv, ctx),
		runBroadcastCmd(m.multicast, advert, ctx),
		listenEventsCmd(srv.Events()),
	), nil
}

func runServerCmd(s *server.Server, ctx context.Context) tea.Cmd {
	return func() tea.Msg {
		if err := s.Start(ctx); err != nil {
			return errMsg{err: err}
		}
		return serverStoppedMsg{}
	}
}

func runBroadcastCmd(multicast string, advert *discovery.Advert, ctx context.Context) tea.Cmd {
	return func() tea.Msg {
		if err := discovery.Broadcast(ctx, multicast, advert, 5*time.Second); err != nil {
			return errMsg{err: err}
		}
		return broadcastStoppedMsg{}
	}
}

func listenEventsCmd(events <-chan server.ConnEvent) tea.Cmd {
	return func() tea.Msg {
		ev, ok := <-events
		if !ok {
			return serverStoppedMsg{}
		}
		return connEventMsg(ev)
	}
}

// list.Item implementation for connection log entries.
type eventItem string

func (e eventItem) Title() string       { return string(e) }
func (e eventItem) Description() string { return "" }
func (e eventItem) FilterValue() string { return string(e) }

func itemsFromEvents(evts []string) []list.Item {
	items := make([]list.Item, 0, len(evts))
	for _, e := range evts {
		items = append(items, eventItem(e))
	}
	return items
}

func main() {
	var (
		certPath   = flag.String("cert", "certs/server.crt", "server cert path")
		keyPath    = flag.String("key", "certs/server.key", "server key path")
		caPath     = flag.String("ca", "certs/ca.crt", "CA bundle path")
		addr       = flag.String("addr", ":8443", "TCP listen address")
		multicast  = flag.String("multicast", "239.255.42.99:9999", "multicast group")
		serverName = flag.String("server-name", "transfer.local", "server name advertised to clients")
		id         = flag.String("id", "sender-1", "unique sender id")
		insecure   = flag.Bool("insecure-skip-verify", false, "skip verifying client certificates (NOT recommended)")
	)
	flag.Parse()

	cert, err := config.LoadCertificate(*certPath, *keyPath)
	if err != nil {
		log.Fatalf("load cert: %v", err)
	}
	var roots *x509.CertPool
	if !*insecure {
		r, err := config.LoadCertPool(*caPath)
		if err != nil {
			log.Fatalf("load ca: %v", err)
		}
		roots = r
	}

	m := newModel(cert, roots, *addr, *multicast, *serverName, *id, *insecure)
	if err := tea.NewProgram(m).Start(); err != nil {
		log.Fatal(err)
	}
}
