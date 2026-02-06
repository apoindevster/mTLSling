package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"flag"
	"fmt"
	"log"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/filepicker"
	"github.com/charmbracelet/bubbles/list"
	"github.com/charmbracelet/bubbles/progress"
	tea "github.com/charmbracelet/bubbletea"

	"multi-cast-transfer/internal/config"
	"multi-cast-transfer/internal/discovery"
	"multi-cast-transfer/internal/server"
)

// messages

type connEventMsg server.ConnEvent

type errMsg struct{ err error }

type serverStoppedMsg struct{}

type broadcastStoppedMsg struct{}

// pages

type page int

const (
	pagePicker page = iota
	pageSelected
	pageConnections
	pageDetails
)

// model holds UI state.
type model struct {
	page        page
	fp          filepicker.Model
	fileList    list.Model
	connList    list.Model
	detailID    string
	connections map[string]*connInfo

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

	width  int
	height int
}

// connection tracking

type connInfo struct {
	id     string
	remote string
	state  string
	files  map[string]*fileProg
}

type fileProg struct {
	name        string
	total       int64
	transferred int64
	bar         progress.Model
}

func newModel(cert tls.Certificate, roots *x509.CertPool, addr, multicast, serverName, id string, insecure bool) model {
	fp := filepicker.New()
	fp.ShowHidden = false
	fp.AutoHeight = true
	fp.SetHeight(12)

	fileDelegate := list.NewDefaultDelegate()
	fl := list.New([]list.Item{}, fileDelegate, 0, 0)
	fl.SetShowStatusBar(false)
	fl.DisableQuitKeybindings()
	fl.SetFilteringEnabled(false)
	fl.Title = "Selected files (d=remove)"

	connDelegate := list.NewDefaultDelegate()
	cl := list.New([]list.Item{}, connDelegate, 0, 0)
	cl.SetShowStatusBar(false)
	cl.DisableQuitKeybindings()
	cl.SetFilteringEnabled(false)
	cl.Title = "Connections (enter=details)"

	return model{
		page:        pagePicker,
		fp:          fp,
		fileList:    fl,
		connList:    cl,
		connections: make(map[string]*connInfo),
		addr:        addr,
		multicast:   multicast,
		serverName:  serverName,
		id:          id,
		insecure:    insecure,
		tlsCert:     cert,
		roots:       roots,
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
		case "tab":
			m.page = (m.page + 1) % 4
		case "f", "p":
			m.page = pagePicker
		case "l":
			m.page = pageSelected
		case "c":
			m.page = pageConnections
		case "b":
			if m.page == pageDetails {
				m.page = pageConnections
			}
		case "s":
			if m.running {
				if m.cancel != nil {
					m.cancel()
				}
				return m, nil
			}
			cmd, err := m.start()
			if err != nil {
				return m, tea.Printf("error: %v", err)
			}
			return m, cmd
		}

		// page-specific key handling
		switch m.page {
		case pageSelected:
			if msg.String() == "d" && len(m.fileList.Items()) > 0 {
				idx := m.fileList.Index()
				items := m.fileList.Items()
				if idx >= 0 && idx < len(items) {
					items = append(items[:idx], items[idx+1:]...)
					m.fileList.SetItems(items)
				}
			}
		case pageConnections:
			if msg.String() == "enter" && len(m.connList.Items()) > 0 {
				if it, ok := m.connList.SelectedItem().(connItem); ok {
					m.detailID = it.id
					m.page = pageDetails
				}
			}
		case pageDetails:
			// handled via 'b' to go back
		}

	case connEventMsg:
		m.applyConnEvent(server.ConnEvent(msg))
		return m, listenEventsCmd(m.srv.Events())

	case errMsg:
		m.running = false
		return m, tea.Printf("error: %v", msg.err)

	case serverStoppedMsg:
		m.running = false
		return m, nil

	case broadcastStoppedMsg:
		return m, nil

	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		h := msg.Height - 8
		if h < 6 {
			h = 6
		}
		m.fp.SetHeight(h)
		m.fileList.SetSize(msg.Width, h)
		m.connList.SetSize(msg.Width, h)
		return m, nil
	}

	// default per-page updates
	var cmd tea.Cmd
	switch m.page {
	case pagePicker:
		var c1 tea.Cmd
		m.fp, c1 = m.fp.Update(msg)
		cmd = tea.Batch(cmd, c1)
		if ok, path := m.fp.DidSelectFile(msg); ok {
			items := append(m.fileList.Items(), fileItem{path: path})
			m.fileList.SetItems(items)
		}
	case pageSelected:
		var c2 tea.Cmd
		m.fileList, c2 = m.fileList.Update(msg)
		cmd = tea.Batch(cmd, c2)
	case pageConnections:
		var c tea.Cmd
		m.connList, c = m.connList.Update(msg)
		cmd = tea.Batch(cmd, c)
	case pageDetails:
		// no interactive widgets here
	}

	return m, cmd
}

func (m model) View() string {
	switch m.page {
	case pagePicker:
		return m.viewPicker()
	case pageSelected:
		return m.viewSelected()
	case pageConnections:
		return m.viewConnections()
	case pageDetails:
		return m.viewDetails()
	default:
		return ""
	}
}

func (m model) viewPicker() string {
	b := &strings.Builder{}
	status := "stopped"
	if m.running {
		status = "running"
	}
	fmt.Fprintf(b, "Picker page | status: %s | listen %s | multicast %s\n", status, m.addr, m.multicast)
	fmt.Fprintf(b, "Select files with arrows+enter. Tab/p/f to stay here, l=selected list, c=connections, s=start/stop, q=quit.\n")
	fmt.Fprintf(b, "Selected: %d files (press 'l' to manage)\n\n", len(m.fileList.Items()))
	fmt.Fprintln(b, m.fp.View())
	return b.String()
}

func (m model) viewSelected() string {
	b := &strings.Builder{}
	fmt.Fprintf(b, "Selected files | d=delete | tab to cycle | p/f=picker | c=connections | s=start/stop | q=quit\n\n")
	fmt.Fprintln(b, m.fileList.View())
	return b.String()
}

func (m model) viewConnections() string {
	b := &strings.Builder{}
	fmt.Fprintf(b, "Connections page | enter=details | tab to cycle | s=start/stop | q=quit\n\n")
	fmt.Fprintln(b, m.connList.View())
	return b.String()
}

func (m model) viewDetails() string {
	b := &strings.Builder{}
	fmt.Fprintf(b, "Connection details | id=%s | b=back | tab to cycle\n\n", m.detailID)
	ci, ok := m.connections[m.detailID]
	if !ok {
		fmt.Fprintln(b, "No data for connection.")
		return b.String()
	}
	fmt.Fprintf(b, "Remote: %s | State: %s\n\n", ci.remote, ci.state)
	names := make([]string, 0, len(ci.files))
	for name := range ci.files {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		fp := ci.files[name]
		pct := 0.0
		if fp.total > 0 {
			pct = float64(fp.transferred) / float64(fp.total)
		}
		bar := fp.bar.ViewAs(pct)
		fmt.Fprintf(b, "%s (%d/%d bytes)\n%s\n", name, fp.transferred, fp.total, bar)
	}
	if len(names) == 0 {
		fmt.Fprintln(b, "No file progress yet.")
	}
	return b.String()
}

// start spins up the TLS server and multicast broadcaster.
func (m *model) start() (tea.Cmd, error) {
	if len(m.fileList.Items()) == 0 {
		return nil, fmt.Errorf("no files selected")
	}

	files := make([]string, 0, len(m.fileList.Items()))
	for _, it := range m.fileList.Items() {
		if fi, ok := it.(fileItem); ok {
			files = append(files, fi.path)
		}
	}

	tlsCfg := config.ServerTLSConfig(m.tlsCert, m.roots, m.insecure)
	srv := server.New(m.addr, tlsCfg, server.FileProviderFunc(func() []string { return files }))

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

	// reset connection tracking
	m.connections = make(map[string]*connInfo)
	m.connList.SetItems(nil)
	m.detailID = ""

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

// list items

type fileItem struct{ path string }

func (f fileItem) Title() string       { return filepath.Base(f.path) }
func (f fileItem) Description() string { return f.path }
func (f fileItem) FilterValue() string { return f.path }

type connItem struct {
	id     string
	remote string
	state  string
}

func (c connItem) Title() string       { return fmt.Sprintf("%s (%s)", c.id, c.state) }
func (c connItem) Description() string { return c.remote }
func (c connItem) FilterValue() string { return c.id }

// event application

func (m *model) applyConnEvent(ev server.ConnEvent) {
	ci, ok := m.connections[ev.ID]
	if !ok {
		ci = &connInfo{id: ev.ID, remote: ev.Remote, state: ev.State, files: make(map[string]*fileProg)}
		m.connections[ev.ID] = ci
	}
	if ev.Remote != "" {
		ci.remote = ev.Remote
	}
	if ev.State != "progress" {
		ci.state = ev.State
	}

	if ev.State == "progress" && ev.File != "" {
		fp, ok := ci.files[ev.File]
		if !ok {
			p := progress.New(progress.WithDefaultGradient())
			p.Width = max(20, m.width-20)
			fp = &fileProg{name: ev.File, total: ev.Total, bar: p}
			ci.files[ev.File] = fp
		}
		fp.total = ev.Total
		fp.transferred = ev.Transferred
		pct := 0.0
		if ev.Total > 0 {
			pct = float64(ev.Transferred) / float64(ev.Total)
		}
		fp.bar.SetPercent(pct)
	}

	m.refreshConnList()
}

func (m *model) refreshConnList() {
	items := make([]list.Item, 0, len(m.connections))
	keys := make([]string, 0, len(m.connections))
	for id := range m.connections {
		keys = append(keys, id)
	}
	sort.Strings(keys)
	for _, id := range keys {
		ci := m.connections[id]
		items = append(items, connItem{id: ci.id, remote: ci.remote, state: ci.state})
	}
	m.connList.SetItems(items)
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
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
