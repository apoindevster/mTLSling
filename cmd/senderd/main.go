package main

import (
	"context"
	"errors"
	"flag"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"

	"multi-cast-transfer/internal/ipc"
	"multi-cast-transfer/internal/ipcapi"
	"multi-cast-transfer/internal/senderdaemon"
)

func main() {
	var (
		senderCertPath   = flag.String("sender-cert", "certs/server.crt", "sender server cert path")
		senderKeyPath    = flag.String("sender-key", "certs/server.key", "sender server key path")
		senderCAPath     = flag.String("sender-ca", "certs/ca.crt", "sender CA bundle path")
		senderAddr       = flag.String("sender-addr", ":8443", "sender TCP listen address")
		senderMulticast  = flag.String("sender-multicast", "239.255.42.99:9999", "sender multicast group")
		senderServerName = flag.String("sender-server-name", "transfer.local", "sender advertised TLS server name")
		senderID         = flag.String("sender-id", "sender-1", "sender advert ID")
		senderInsecure   = flag.Bool("sender-insecure-skip-verify", false, "sender skip verifying client certificates")
		senderEnabled    = flag.Bool("sender-enabled", false, "start sender component at daemon boot")
		senderInterval   = flag.Int("sender-broadcast-interval-seconds", 5, "sender multicast broadcast interval in seconds")

		listenerCertPath   = flag.String("listener-cert", "certs/client.crt", "listener client cert path")
		listenerKeyPath    = flag.String("listener-key", "certs/client.key", "listener client key path")
		listenerCAPath     = flag.String("listener-ca", "certs/ca.crt", "listener CA bundle path")
		listenerMulticast  = flag.String("listener-multicast", "239.255.42.99:9999", "listener multicast group")
		listenerOutDir     = flag.String("listener-out", "downloads", "listener output directory")
		listenerServerName = flag.String("listener-server-name", "", "listener TLS server-name override")
		listenerInsecure   = flag.Bool("listener-insecure-skip-verify", false, "listener skip TLS/advert certificate verification")
		listenerEnabled    = flag.Bool("listener-enabled", false, "start listener component at daemon boot")

		ipcSocket = flag.String("ipc-socket", ipc.DefaultEndpoint, "unix socket path for local IPC")
	)
	flag.Parse()

	daemon := senderdaemon.New(senderdaemon.Settings{
		Sender: senderdaemon.SenderSettings{
			Enabled:                  *senderEnabled,
			CertPath:                 *senderCertPath,
			KeyPath:                  *senderKeyPath,
			CAPath:                   *senderCAPath,
			Addr:                     *senderAddr,
			Multicast:                *senderMulticast,
			ServerName:               *senderServerName,
			ID:                       *senderID,
			InsecureSkipVerify:       *senderInsecure,
			BroadcastIntervalSeconds: *senderInterval,
		},
		Listener: senderdaemon.ListenerSettings{
			Enabled:            *listenerEnabled,
			CertPath:           *listenerCertPath,
			KeyPath:            *listenerKeyPath,
			CAPath:             *listenerCAPath,
			Multicast:          *listenerMulticast,
			OutDir:             *listenerOutDir,
			ServerNameOverride: *listenerServerName,
			InsecureSkipVerify: *listenerInsecure,
		},
	})

	if err := daemon.Reconcile(); err != nil {
		log.Fatalf("apply initial settings: %v", err)
	}

	ln, err := ipc.Listen(*ipcSocket)
	if err != nil {
		log.Fatalf("listen ipc: %v", err)
	}
	defer func() {
		_ = ln.Close()
		_ = os.Remove(ipc.NormalizeEndpoint(*ipcSocket))
	}()

	httpServer := &http.Server{Handler: ipcapi.NewHandler(daemon)}
	go func() {
		if err := httpServer.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatalf("ipc http serve: %v", err)
		}
	}()

	log.Printf("combined daemon ready; ipc socket=%s", ipc.NormalizeEndpoint(*ipcSocket))

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	<-ctx.Done()

	daemon.Shutdown()
	_ = httpServer.Shutdown(context.Background())
}
