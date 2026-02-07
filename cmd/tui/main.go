package main

import (
	"flag"
	"fmt"
	"log"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/filepicker"
	"github.com/charmbracelet/bubbles/list"
	"github.com/charmbracelet/bubbles/progress"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"

	"multi-cast-transfer/internal/ipc"
	"multi-cast-transfer/internal/ipcapi"
	"multi-cast-transfer/internal/senderdaemon"
)

type page int

const (
	pagePicker page = iota
	pageSelected
	pageConnections
	pageDetails
	pageSettings
)

type settingKey string

const (
	keySenderEnabled           settingKey = "sender.enabled"
	keySenderCert              settingKey = "sender.cert_path"
	keySenderKey               settingKey = "sender.key_path"
	keySenderCA                settingKey = "sender.ca_path"
	keySenderAddr              settingKey = "sender.addr"
	keySenderMulticast         settingKey = "sender.multicast"
	keySenderServerName        settingKey = "sender.server_name"
	keySenderID                settingKey = "sender.id"
	keySenderInsecure          settingKey = "sender.insecure_skip_verify"
	keySenderBroadcastInterval settingKey = "sender.broadcast_interval_seconds"
	keyListenerEnabled         settingKey = "listener.enabled"
	keyListenerCert            settingKey = "listener.cert_path"
	keyListenerKey             settingKey = "listener.key_path"
	keyListenerCA              settingKey = "listener.ca_path"
	keyListenerMulticast       settingKey = "listener.multicast"
	keyListenerOut             settingKey = "listener.out_dir"
	keyListenerServerName      settingKey = "listener.server_name_override"
	keyListenerInsecure        settingKey = "listener.insecure_skip_verify"
)

type stateMsg struct {
	snapshot senderdaemon.Snapshot
	err      error
}

type actionMsg struct {
	snapshot   senderdaemon.Snapshot
	err        error
	clearDirty bool
}

type tickMsg struct{}

type model struct {
	page         page
	fp           filepicker.Model
	fileList     list.Model
	connList     list.Model
	settingsList list.Model

	editInput textinput.Model
	editing   bool
	editKey   settingKey

	detailID    string
	connections map[string]senderdaemon.ConnectionSnapshot
	recent      []senderdaemon.ListenerEvent

	settings      senderdaemon.Settings
	settingsDirty bool

	senderRunning   bool
	listenerRunning bool
	senderError     string
	listenerError   string
	uiError         string

	client *ipcapi.Client

	width  int
	height int
}

func newModel(client *ipcapi.Client) model {
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
	cl.Title = "Sender connections (enter=details)"

	settingsDelegate := list.NewDefaultDelegate()
	sl := list.New([]list.Item{}, settingsDelegate, 0, 0)
	sl.SetShowStatusBar(false)
	sl.DisableQuitKeybindings()
	sl.SetFilteringEnabled(false)
	sl.Title = "Daemon settings"

	ti := textinput.New()
	ti.Prompt = "value> "
	ti.CharLimit = 512

	m := model{
		page:         pagePicker,
		fp:           fp,
		fileList:     fl,
		connList:     cl,
		settingsList: sl,
		editInput:    ti,
		connections:  make(map[string]senderdaemon.ConnectionSnapshot),
		client:       client,
	}
	m.rebuildSettingsList()
	return m
}

func (m model) Init() tea.Cmd {
	return tea.Batch(m.fp.Init(), requestStateCmd(m.client))
}

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		if m.page == pageSettings && m.editing {
			return m.updateEditing(msg)
		}

		switch msg.String() {
		case "ctrl+c", "q":
			return m, tea.Quit
		case "tab":
			m.page = (m.page + 1) % 5
		case "p", "f":
			m.page = pagePicker
		case "l":
			m.page = pageSelected
		case "c":
			m.page = pageConnections
		case "g":
			m.page = pageSettings
		case "b":
			if m.page == pageDetails {
				m.page = pageConnections
			}
		case "s":
			if m.senderRunning {
				return m, stopSenderCmd(m.client)
			}
			return m, startSenderCmd(m.client)
		case "r":
			if m.listenerRunning {
				return m, stopListenerCmd(m.client)
			}
			return m, startListenerCmd(m.client)
		}

		switch m.page {
		case pageSelected:
			if msg.String() == "d" && len(m.fileList.Items()) > 0 {
				idx := m.fileList.Index()
				items := m.fileList.Items()
				if idx >= 0 && idx < len(items) {
					items = append(items[:idx], items[idx+1:]...)
					paths := listItemsToPaths(items)
					m.syncFiles(paths)
					return m, setFilesCmd(m.client, paths)
				}
			}
		case pageConnections:
			if msg.String() == "enter" && len(m.connList.Items()) > 0 {
				if it, ok := m.connList.SelectedItem().(connItem); ok {
					m.detailID = it.id
					m.page = pageDetails
				}
			}
		case pageSettings:
			if msg.String() == "space" {
				if m.toggleSelectedSetting() {
					return m, nil
				}
			}
			if msg.String() == "e" {
				m.beginEditSelectedSetting()
				return m, nil
			}
			if msg.String() == "w" {
				return m, updateSettingsCmd(m.client, m.settings)
			}
		}

	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		h := msg.Height - 9
		if h < 6 {
			h = 6
		}
		m.fp.SetHeight(h)
		m.fileList.SetSize(msg.Width, h)
		m.connList.SetSize(msg.Width, h/2)
		m.settingsList.SetSize(msg.Width, h)
		return m, nil

	case stateMsg:
		if msg.err != nil {
			m.uiError = msg.err.Error()
			return m, nextPollCmd()
		}
		m.uiError = ""
		m.applySnapshot(msg.snapshot)
		return m, nextPollCmd()

	case actionMsg:
		if msg.err != nil {
			m.uiError = msg.err.Error()
			return m, requestStateCmd(m.client)
		}
		if msg.clearDirty {
			m.settingsDirty = false
		}
		m.uiError = ""
		m.applySnapshot(msg.snapshot)
		return m, nil

	case tickMsg:
		return m, requestStateCmd(m.client)
	}

	var cmd tea.Cmd
	switch m.page {
	case pagePicker:
		var c1 tea.Cmd
		m.fp, c1 = m.fp.Update(msg)
		cmd = c1
		if ok, path := m.fp.DidSelectFile(msg); ok {
			files := appendUnique(listItemsToPaths(m.fileList.Items()), path)
			m.syncFiles(files)
			return m, tea.Batch(cmd, setFilesCmd(m.client, files))
		}
	case pageSelected:
		var c2 tea.Cmd
		m.fileList, c2 = m.fileList.Update(msg)
		cmd = c2
	case pageConnections:
		var c3 tea.Cmd
		m.connList, c3 = m.connList.Update(msg)
		cmd = c3
	case pageSettings:
		var c4 tea.Cmd
		m.settingsList, c4 = m.settingsList.Update(msg)
		cmd = c4
	}

	return m, cmd
}

func (m model) updateEditing(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc":
		m.editing = false
		m.editKey = ""
		return m, nil
	case "enter":
		if err := m.commitSettingEdit(strings.TrimSpace(m.editInput.Value())); err != nil {
			m.uiError = err.Error()
			return m, nil
		}
		m.editing = false
		m.editKey = ""
		m.settingsDirty = true
		m.rebuildSettingsList()
		return m, nil
	}
	var cmd tea.Cmd
	m.editInput, cmd = m.editInput.Update(msg)
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
	case pageSettings:
		return m.viewSettings()
	default:
		return ""
	}
}

func (m model) viewPicker() string {
	b := &strings.Builder{}
	fmt.Fprintln(b, m.statusBanner())
	fmt.Fprintln(b)
	fmt.Fprintf(b, "Picker\n")
	fmt.Fprintf(b, "Keys: tab cycle | l selected | c connections | g settings | s toggle sender | r toggle listener | q quit\n")
	fmt.Fprintf(b, "Selected files: %d\n", len(m.fileList.Items()))
	if m.uiError != "" {
		fmt.Fprintf(b, "UI error: %s\n", m.uiError)
	}
	if m.senderError != "" {
		fmt.Fprintf(b, "Sender error: %s\n", m.senderError)
	}
	if m.listenerError != "" {
		fmt.Fprintf(b, "Listener error: %s\n", m.listenerError)
	}
	fmt.Fprintln(b)
	fmt.Fprintln(b, m.fp.View())
	return b.String()
}

func (m model) viewSelected() string {
	b := &strings.Builder{}
	fmt.Fprintln(b, m.statusBanner())
	fmt.Fprintln(b)
	fmt.Fprintf(b, "Selected files | d delete | tab cycle | p/f picker | g settings\n\n")
	fmt.Fprintln(b, m.fileList.View())
	return b.String()
}

func (m model) viewConnections() string {
	b := &strings.Builder{}
	fmt.Fprintln(b, m.statusBanner())
	fmt.Fprintln(b)
	fmt.Fprintf(b, "Connections | enter details | s toggle sender | r toggle listener | g settings\n")
	if m.uiError != "" {
		fmt.Fprintf(b, "UI error: %s\n", m.uiError)
	}
	fmt.Fprintln(b)
	fmt.Fprintln(b, m.connList.View())
	fmt.Fprintln(b, "Listener recent activity:")
	if len(m.recent) == 0 {
		fmt.Fprintln(b, "  (none)")
	} else {
		for i, ev := range m.recent {
			if i >= 8 {
				break
			}
			fmt.Fprintf(b, "  %s %s %s %s\n", ev.Timestamp.Format("15:04:05"), ev.Status, ev.Callback, ev.Detail)
		}
	}
	return b.String()
}

func (m model) viewDetails() string {
	b := &strings.Builder{}
	fmt.Fprintln(b, m.statusBanner())
	fmt.Fprintln(b)
	fmt.Fprintf(b, "Connection details | id=%s | b back\n\n", m.detailID)
	ci, ok := m.connections[m.detailID]
	if !ok {
		fmt.Fprintln(b, "No data for connection.")
		return b.String()
	}
	fmt.Fprintf(b, "Remote: %s | State: %s\n\n", ci.Remote, ci.State)
	files := append([]senderdaemon.FileProgressSnapshot(nil), ci.Files...)
	sort.Slice(files, func(i, j int) bool { return files[i].Name < files[j].Name })
	for _, fp := range files {
		pct := 0.0
		if fp.Total > 0 {
			pct = float64(fp.Transferred) / float64(fp.Total)
		}
		bar := progress.New(progress.WithDefaultGradient())
		bar.Width = max(20, m.width-20)
		fmt.Fprintf(b, "%s (%d/%d bytes)\n%s\n", fp.Name, fp.Transferred, fp.Total, bar.ViewAs(pct))
	}
	if len(files) == 0 {
		fmt.Fprintln(b, "No file progress yet.")
	}
	return b.String()
}

func (m model) viewSettings() string {
	b := &strings.Builder{}
	fmt.Fprintln(b, m.statusBanner())
	fmt.Fprintln(b)
	dirty := "saved"
	if m.settingsDirty {
		dirty = "modified"
	}
	fmt.Fprintf(b, "Settings (%s) | arrows navigate | e edit | space toggle bool | w write/apply\n", dirty)
	fmt.Fprintf(b, "Keys: tab cycle | p picker | c connections | s toggle sender | r toggle listener\n")
	if m.uiError != "" {
		fmt.Fprintf(b, "UI error: %s\n", m.uiError)
	}
	fmt.Fprintln(b)
	fmt.Fprintln(b, m.settingsList.View())
	if m.editing {
		fmt.Fprintln(b)
		fmt.Fprintf(b, "Editing %s (enter save, esc cancel)\n", m.editKey)
		fmt.Fprintln(b, m.editInput.View())
	}
	return b.String()
}

func (m *model) applySnapshot(s senderdaemon.Snapshot) {
	m.senderRunning = s.Sender.Running
	m.listenerRunning = s.Listener.Running
	m.senderError = s.Sender.LastError
	m.listenerError = s.Listener.LastError
	m.recent = s.Listener.Recent

	if !m.settingsDirty && !m.editing {
		m.settings = s.Settings
		m.rebuildSettingsList()
	}

	m.syncFiles(s.Sender.Files)

	m.connections = make(map[string]senderdaemon.ConnectionSnapshot, len(s.Sender.Connections))
	items := make([]list.Item, 0, len(s.Sender.Connections))
	for _, c := range s.Sender.Connections {
		m.connections[c.ID] = c
		items = append(items, connItem{id: c.ID, remote: c.Remote, state: c.State})
	}
	sort.Slice(items, func(i, j int) bool {
		return items[i].(connItem).id < items[j].(connItem).id
	})
	m.connList.SetItems(items)

	if m.detailID != "" {
		if _, ok := m.connections[m.detailID]; !ok {
			m.detailID = ""
			m.page = pageConnections
		}
	}
}

func (m *model) syncFiles(paths []string) {
	items := make([]list.Item, 0, len(paths))
	for _, p := range paths {
		items = append(items, fileItem{path: p})
	}
	m.fileList.SetItems(items)
}

func (m *model) rebuildSettingsList() {
	idx := m.settingsList.Index()
	items := []list.Item{
		settingItem{key: keySenderEnabled, label: "sender.enabled", value: strconv.FormatBool(m.settings.Sender.Enabled), togglable: true},
		settingItem{key: keySenderCert, label: "sender.cert_path", value: m.settings.Sender.CertPath, editable: true},
		settingItem{key: keySenderKey, label: "sender.key_path", value: m.settings.Sender.KeyPath, editable: true},
		settingItem{key: keySenderCA, label: "sender.ca_path", value: m.settings.Sender.CAPath, editable: true},
		settingItem{key: keySenderAddr, label: "sender.addr", value: m.settings.Sender.Addr, editable: true},
		settingItem{key: keySenderMulticast, label: "sender.multicast", value: m.settings.Sender.Multicast, editable: true},
		settingItem{key: keySenderServerName, label: "sender.server_name", value: m.settings.Sender.ServerName, editable: true},
		settingItem{key: keySenderID, label: "sender.id", value: m.settings.Sender.ID, editable: true},
		settingItem{key: keySenderInsecure, label: "sender.insecure_skip_verify", value: strconv.FormatBool(m.settings.Sender.InsecureSkipVerify), togglable: true},
		settingItem{key: keySenderBroadcastInterval, label: "sender.broadcast_interval_seconds", value: strconv.Itoa(m.settings.Sender.BroadcastIntervalSeconds), editable: true},
		settingItem{key: keyListenerEnabled, label: "listener.enabled", value: strconv.FormatBool(m.settings.Listener.Enabled), togglable: true},
		settingItem{key: keyListenerCert, label: "listener.cert_path", value: m.settings.Listener.CertPath, editable: true},
		settingItem{key: keyListenerKey, label: "listener.key_path", value: m.settings.Listener.KeyPath, editable: true},
		settingItem{key: keyListenerCA, label: "listener.ca_path", value: m.settings.Listener.CAPath, editable: true},
		settingItem{key: keyListenerMulticast, label: "listener.multicast", value: m.settings.Listener.Multicast, editable: true},
		settingItem{key: keyListenerOut, label: "listener.out_dir", value: m.settings.Listener.OutDir, editable: true},
		settingItem{key: keyListenerServerName, label: "listener.server_name_override", value: m.settings.Listener.ServerNameOverride, editable: true},
		settingItem{key: keyListenerInsecure, label: "listener.insecure_skip_verify", value: strconv.FormatBool(m.settings.Listener.InsecureSkipVerify), togglable: true},
	}
	m.settingsList.SetItems(items)
	if len(items) == 0 {
		return
	}
	if idx < 0 {
		idx = 0
	}
	if idx >= len(items) {
		idx = len(items) - 1
	}
	m.settingsList.Select(idx)
}

func (m *model) selectedSetting() (settingItem, bool) {
	item, ok := m.settingsList.SelectedItem().(settingItem)
	return item, ok
}

func (m *model) toggleSelectedSetting() bool {
	item, ok := m.selectedSetting()
	if !ok || !item.togglable {
		return false
	}
	switch item.key {
	case keySenderEnabled:
		m.settings.Sender.Enabled = !m.settings.Sender.Enabled
	case keySenderInsecure:
		m.settings.Sender.InsecureSkipVerify = !m.settings.Sender.InsecureSkipVerify
	case keyListenerEnabled:
		m.settings.Listener.Enabled = !m.settings.Listener.Enabled
	case keyListenerInsecure:
		m.settings.Listener.InsecureSkipVerify = !m.settings.Listener.InsecureSkipVerify
	default:
		return false
	}
	m.settingsDirty = true
	m.rebuildSettingsList()
	return true
}

func (m *model) beginEditSelectedSetting() {
	item, ok := m.selectedSetting()
	if !ok || !item.editable {
		return
	}
	m.editing = true
	m.editKey = item.key
	m.editInput.SetValue(item.value)
	m.editInput.Focus()
}

func (m *model) commitSettingEdit(value string) error {
	switch m.editKey {
	case keySenderCert:
		m.settings.Sender.CertPath = value
	case keySenderKey:
		m.settings.Sender.KeyPath = value
	case keySenderCA:
		m.settings.Sender.CAPath = value
	case keySenderAddr:
		m.settings.Sender.Addr = value
	case keySenderMulticast:
		m.settings.Sender.Multicast = value
	case keySenderServerName:
		m.settings.Sender.ServerName = value
	case keySenderID:
		m.settings.Sender.ID = value
	case keySenderBroadcastInterval:
		n, err := strconv.Atoi(value)
		if err != nil || n <= 0 {
			return fmt.Errorf("broadcast interval must be a positive integer")
		}
		m.settings.Sender.BroadcastIntervalSeconds = n
	case keyListenerCert:
		m.settings.Listener.CertPath = value
	case keyListenerKey:
		m.settings.Listener.KeyPath = value
	case keyListenerCA:
		m.settings.Listener.CAPath = value
	case keyListenerMulticast:
		m.settings.Listener.Multicast = value
	case keyListenerOut:
		m.settings.Listener.OutDir = value
	case keyListenerServerName:
		m.settings.Listener.ServerNameOverride = value
	default:
		return fmt.Errorf("setting %s is not editable", m.editKey)
	}
	return nil
}

type settingItem struct {
	key       settingKey
	label     string
	value     string
	editable  bool
	togglable bool
}

func (s settingItem) Title() string       { return s.label }
func (s settingItem) Description() string { return s.value }
func (s settingItem) FilterValue() string { return s.label }

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

func requestStateCmd(client *ipcapi.Client) tea.Cmd {
	return func() tea.Msg {
		snap, err := client.State()
		return stateMsg{snapshot: snap, err: err}
	}
}

func nextPollCmd() tea.Cmd {
	return tea.Tick(time.Second, func(time.Time) tea.Msg { return tickMsg{} })
}

func setFilesCmd(client *ipcapi.Client, files []string) tea.Cmd {
	return func() tea.Msg {
		snap, err := client.SetFiles(files)
		return actionMsg{snapshot: snap, err: err}
	}
}

func updateSettingsCmd(client *ipcapi.Client, settings senderdaemon.Settings) tea.Cmd {
	return func() tea.Msg {
		snap, err := client.UpdateSettings(settings)
		return actionMsg{snapshot: snap, err: err, clearDirty: true}
	}
}

func startSenderCmd(client *ipcapi.Client) tea.Cmd {
	return func() tea.Msg {
		snap, err := client.StartSender()
		return actionMsg{snapshot: snap, err: err}
	}
}

func stopSenderCmd(client *ipcapi.Client) tea.Cmd {
	return func() tea.Msg {
		snap, err := client.StopSender()
		return actionMsg{snapshot: snap, err: err}
	}
}

func startListenerCmd(client *ipcapi.Client) tea.Cmd {
	return func() tea.Msg {
		snap, err := client.StartListener()
		return actionMsg{snapshot: snap, err: err}
	}
}

func stopListenerCmd(client *ipcapi.Client) tea.Cmd {
	return func() tea.Msg {
		snap, err := client.StopListener()
		return actionMsg{snapshot: snap, err: err}
	}
}

func listItemsToPaths(items []list.Item) []string {
	out := make([]string, 0, len(items))
	for _, it := range items {
		if fi, ok := it.(fileItem); ok {
			out = append(out, fi.path)
		}
	}
	return out
}

func appendUnique(in []string, path string) []string {
	for _, p := range in {
		if p == path {
			return in
		}
	}
	return append(in, path)
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func runStatus(running bool) string {
	if running {
		return "ACTIVE"
	}
	return "INACTIVE"
}

func (m model) statusBanner() string {
	return fmt.Sprintf(
		"Sender: %s | Listener: %s",
		runStatus(m.senderRunning),
		runStatus(m.listenerRunning),
	)
}

func main() {
	ipcSocket := flag.String("ipc-socket", ipc.DefaultEndpoint, "unix socket path for daemon IPC")
	flag.Parse()

	client := ipcapi.NewClient(*ipcSocket)
	m := newModel(client)
	if err := tea.NewProgram(m).Start(); err != nil {
		log.Fatal(err)
	}
}
