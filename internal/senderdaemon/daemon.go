package senderdaemon

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"

	"multi-cast-transfer/internal/client"
	"multi-cast-transfer/internal/config"
	"multi-cast-transfer/internal/discovery"
	"multi-cast-transfer/internal/server"
)

type connState struct {
	id     string
	remote string
	state  string
	files  map[string]FileProgressSnapshot
}

type senderRuntime struct {
	running     bool
	lastError   string
	connections map[string]*connState
	cancel      context.CancelFunc
	srv         *server.Server
}

type listenerRuntime struct {
	running   bool
	lastError string
	recent    []ListenerEvent
	seen      map[string]int64
	cancel    context.CancelFunc
}

type Daemon struct {
	mu sync.RWMutex

	settings    Settings
	senderFiles []string

	sender   senderRuntime
	listener listenerRuntime
}

func New(settings Settings) *Daemon {
	settings = normalizeSettings(settings)
	return &Daemon{
		settings: settings,
		sender: senderRuntime{
			connections: make(map[string]*connState),
		},
		listener: listenerRuntime{
			recent: make([]ListenerEvent, 0, 64),
			seen:   make(map[string]int64),
		},
	}
}

// Reconcile applies settings enabled flags to running components.
func (d *Daemon) Reconcile() error {
	settings := d.Settings()
	var firstErr error

	if settings.Sender.Enabled {
		if err := d.startSender(false); err != nil && firstErr == nil {
			firstErr = err
		}
	} else {
		d.stopSender(false)
	}

	if settings.Listener.Enabled {
		if err := d.startListener(false); err != nil && firstErr == nil {
			firstErr = err
		}
	} else {
		d.stopListener(false)
	}

	return firstErr
}

func (d *Daemon) Shutdown() {
	d.stopSender(false)
	d.stopListener(false)
}

func (d *Daemon) Settings() Settings {
	d.mu.RLock()
	defer d.mu.RUnlock()
	return d.settings
}

func (d *Daemon) UpdateSettings(settings Settings) error {
	settings = normalizeSettings(settings)

	d.mu.Lock()
	senderWasRunning := d.sender.running
	listenerWasRunning := d.listener.running
	d.settings = settings
	d.mu.Unlock()

	if senderWasRunning {
		d.stopSender(false)
	}
	if listenerWasRunning {
		d.stopListener(false)
	}

	return d.Reconcile()
}

func (d *Daemon) SetSenderFiles(files []string) {
	files = normalizeFiles(files)
	d.mu.Lock()
	defer d.mu.Unlock()
	d.senderFiles = files
}

func (d *Daemon) StartSender() error {
	return d.startSender(true)
}

func (d *Daemon) StopSender() {
	d.stopSender(true)
}

func (d *Daemon) StartListener() error {
	return d.startListener(true)
}

func (d *Daemon) StopListener() {
	d.stopListener(true)
}

func (d *Daemon) Snapshot() Snapshot {
	d.mu.RLock()
	defer d.mu.RUnlock()

	snap := Snapshot{
		Settings: d.settings,
		Sender: SenderState{
			Running:   d.sender.running,
			Files:     append([]string(nil), d.senderFiles...),
			LastError: d.sender.lastError,
		},
		Listener: ListenerState{
			Running:   d.listener.running,
			LastError: d.listener.lastError,
			Recent:    append([]ListenerEvent(nil), d.listener.recent...),
		},
	}

	senderKeys := make([]string, 0, len(d.sender.connections))
	for id := range d.sender.connections {
		senderKeys = append(senderKeys, id)
	}
	sort.Strings(senderKeys)
	for _, id := range senderKeys {
		c := d.sender.connections[id]
		cs := ConnectionSnapshot{ID: c.id, Remote: c.remote, State: c.state}

		fkeys := make([]string, 0, len(c.files))
		for name := range c.files {
			fkeys = append(fkeys, name)
		}
		sort.Strings(fkeys)
		for _, name := range fkeys {
			cs.Files = append(cs.Files, c.files[name])
		}

		snap.Sender.Connections = append(snap.Sender.Connections, cs)
	}

	return snap
}

func (d *Daemon) startSender(updateEnabled bool) error {
	d.mu.Lock()
	if d.sender.running {
		if updateEnabled {
			d.settings.Sender.Enabled = true
		}
		d.mu.Unlock()
		return nil
	}
	settings := d.settings.Sender
	files := append([]string(nil), d.senderFiles...)
	if updateEnabled {
		d.settings.Sender.Enabled = true
	}
	d.mu.Unlock()

	if len(files) == 0 {
		err := errors.New("no sender files configured")
		d.setSenderError(err)
		return err
	}

	cert, roots, err := loadTLS(settings.CertPath, settings.KeyPath, settings.CAPath, settings.InsecureSkipVerify)
	if err != nil {
		err = fmt.Errorf("load sender tls: %w", err)
		d.setSenderError(err)
		return err
	}

	tlsCfg := config.ServerTLSConfig(cert, roots, settings.InsecureSkipVerify)
	srv := server.New(settings.Addr, tlsCfg, server.FileProviderFunc(func() []string {
		d.mu.RLock()
		defer d.mu.RUnlock()
		return append([]string(nil), d.senderFiles...)
	}))
	advert, err := discovery.NewAdvert(settings.ID, settings.Addr, settings.ServerName, cert)
	if err != nil {
		err = fmt.Errorf("build sender advert: %w", err)
		d.setSenderError(err)
		return err
	}

	ctx, cancel := context.WithCancel(context.Background())

	d.mu.Lock()
	d.sender.running = true
	d.sender.lastError = ""
	d.sender.cancel = cancel
	d.sender.srv = srv
	d.sender.connections = make(map[string]*connState)
	d.mu.Unlock()

	interval := time.Duration(settings.BroadcastIntervalSeconds) * time.Second
	go d.consumeSenderEvents(ctx, srv.Events())
	go d.runSenderServer(ctx, srv, cancel)
	go d.runSenderBroadcast(ctx, settings.Multicast, advert, interval, cancel)
	return nil
}

func (d *Daemon) stopSender(updateEnabled bool) {
	d.mu.Lock()
	cancel := d.sender.cancel
	srv := d.sender.srv
	running := d.sender.running
	if updateEnabled {
		d.settings.Sender.Enabled = false
	}
	d.sender.running = false
	d.sender.cancel = nil
	d.sender.srv = nil
	d.mu.Unlock()

	if !running {
		return
	}
	if cancel != nil {
		cancel()
	}
	if srv != nil {
		srv.Stop()
	}
}

func (d *Daemon) runSenderServer(ctx context.Context, srv *server.Server, cancel context.CancelFunc) {
	if err := srv.Start(ctx); err != nil && ctx.Err() == nil {
		d.setSenderError(fmt.Errorf("sender server stopped: %w", err))
	}
	cancel()
	d.markSenderStopped(srv)
}

func (d *Daemon) runSenderBroadcast(ctx context.Context, multicast string, advert *discovery.Advert, interval time.Duration, cancel context.CancelFunc) {
	if err := discovery.Broadcast(ctx, multicast, advert, interval); err != nil && ctx.Err() == nil {
		d.setSenderError(fmt.Errorf("sender multicast stopped: %w", err))
		cancel()
	}
}

func (d *Daemon) consumeSenderEvents(ctx context.Context, events <-chan server.ConnEvent) {
	for {
		select {
		case <-ctx.Done():
			return
		case ev, ok := <-events:
			if !ok {
				return
			}
			d.applySenderEvent(ev)
		}
	}
}

func (d *Daemon) applySenderEvent(ev server.ConnEvent) {
	d.mu.Lock()
	defer d.mu.Unlock()

	c, ok := d.sender.connections[ev.ID]
	if !ok {
		c = &connState{id: ev.ID, remote: ev.Remote, state: ev.State, files: make(map[string]FileProgressSnapshot)}
		d.sender.connections[ev.ID] = c
	}
	if ev.Remote != "" {
		c.remote = ev.Remote
	}
	if ev.State != "progress" {
		c.state = ev.State
	}
	if ev.State == "error" && ev.Err != nil {
		d.sender.lastError = ev.Err.Error()
	}

	if ev.State == "progress" && ev.File != "" {
		c.files[ev.File] = FileProgressSnapshot{
			Name:        ev.File,
			Transferred: ev.Transferred,
			Total:       ev.Total,
		}
	}
}

func (d *Daemon) setSenderError(err error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.sender.lastError = err.Error()
}

func (d *Daemon) markSenderStopped(srv *server.Server) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.sender.srv == srv {
		d.sender.running = false
		d.sender.cancel = nil
		d.sender.srv = nil
	}
}

func (d *Daemon) startListener(updateEnabled bool) error {
	d.mu.Lock()
	if d.listener.running {
		if updateEnabled {
			d.settings.Listener.Enabled = true
		}
		d.mu.Unlock()
		return nil
	}
	settings := d.settings.Listener
	if updateEnabled {
		d.settings.Listener.Enabled = true
	}
	d.mu.Unlock()

	cert, roots, err := loadTLS(settings.CertPath, settings.KeyPath, settings.CAPath, settings.InsecureSkipVerify)
	if err != nil {
		err = fmt.Errorf("load listener tls: %w", err)
		d.setListenerError(err)
		return err
	}

	ctx, cancel := context.WithCancel(context.Background())
	d.mu.Lock()
	d.listener.running = true
	d.listener.cancel = cancel
	d.listener.lastError = ""
	d.mu.Unlock()

	go d.runListener(ctx, settings, cert, roots)
	return nil
}

func (d *Daemon) stopListener(updateEnabled bool) {
	d.mu.Lock()
	cancel := d.listener.cancel
	running := d.listener.running
	if updateEnabled {
		d.settings.Listener.Enabled = false
	}
	d.listener.running = false
	d.listener.cancel = nil
	d.mu.Unlock()

	if !running {
		return
	}
	if cancel != nil {
		cancel()
	}
}

func (d *Daemon) runListener(ctx context.Context, settings ListenerSettings, cert tls.Certificate, roots *x509.CertPool) {
	defer d.markListenerStopped()

	adverts, err := discovery.Listen(ctx, settings.Multicast, roots)
	if err != nil {
		d.setListenerError(fmt.Errorf("listen multicast: %w", err))
		return
	}

	for {
		select {
		case <-ctx.Done():
			return
		case ad, ok := <-adverts:
			if !ok {
				return
			}
			if d.isOldAdvert(ad.ID, ad.Timestamp) {
				continue
			}

			expected := ad.ServerName
			if settings.ServerNameOverride != "" {
				expected = settings.ServerNameOverride
			}
			tlsCfg := config.ClientTLSConfig(cert, roots, expected, settings.InsecureSkipVerify)

			d.addListenerEvent(ListenerEvent{
				Timestamp: time.Now(),
				AdvertID:  ad.ID,
				Callback:  ad.Callback,
				Status:    "connecting",
				Detail:    "starting download session",
			})

			if err := client.Download(ctx, ad.Callback, tlsCfg, settings.OutDir); err != nil {
				d.setListenerError(fmt.Errorf("download from %s failed: %w", ad.Callback, err))
				d.addListenerEvent(ListenerEvent{
					Timestamp: time.Now(),
					AdvertID:  ad.ID,
					Callback:  ad.Callback,
					Status:    "error",
					Detail:    err.Error(),
				})
				continue
			}

			d.addListenerEvent(ListenerEvent{
				Timestamp: time.Now(),
				AdvertID:  ad.ID,
				Callback:  ad.Callback,
				Status:    "completed",
				Detail:    "download completed",
			})
		}
	}
}

func (d *Daemon) isOldAdvert(id string, ts int64) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	if last, ok := d.listener.seen[id]; ok && ts <= last {
		return true
	}
	d.listener.seen[id] = ts
	return false
}

func (d *Daemon) addListenerEvent(ev ListenerEvent) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.listener.recent = append([]ListenerEvent{ev}, d.listener.recent...)
	if len(d.listener.recent) > 64 {
		d.listener.recent = d.listener.recent[:64]
	}
}

func (d *Daemon) setListenerError(err error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.listener.lastError = err.Error()
}

func (d *Daemon) markListenerStopped() {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.listener.running = false
	d.listener.cancel = nil
}

func loadTLS(certPath, keyPath, caPath string, insecure bool) (tls.Certificate, *x509.CertPool, error) {
	cert, err := config.LoadCertificate(certPath, keyPath)
	if err != nil {
		return tls.Certificate{}, nil, err
	}
	if insecure {
		return cert, nil, nil
	}
	roots, err := config.LoadCertPool(caPath)
	if err != nil {
		return tls.Certificate{}, nil, err
	}
	return cert, roots, nil
}

func normalizeFiles(files []string) []string {
	seen := make(map[string]struct{}, len(files))
	out := make([]string, 0, len(files))
	for _, f := range files {
		if f == "" {
			continue
		}
		if _, ok := seen[f]; ok {
			continue
		}
		seen[f] = struct{}{}
		out = append(out, f)
	}
	return out
}

func normalizeSettings(s Settings) Settings {
	if s.Sender.BroadcastIntervalSeconds <= 0 {
		s.Sender.BroadcastIntervalSeconds = 5
	}
	if s.Listener.OutDir == "" {
		s.Listener.OutDir = "downloads"
	}
	return s
}
