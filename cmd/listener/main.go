package main

import (
	"context"
	"crypto/x509"
	"flag"
	"log"
	"os/signal"
	"syscall"
	"time"

	"multi-cast-transfer/internal/client"
	"multi-cast-transfer/internal/config"
	"multi-cast-transfer/internal/discovery"
)

func main() {
	var (
		multicast  = flag.String("multicast", "239.255.42.99:9999", "multicast address to listen on")
		outDir     = flag.String("out", "downloads", "directory to store downloaded files")
		certPath   = flag.String("cert", "certs/client.crt", "client certificate path")
		keyPath    = flag.String("key", "certs/client.key", "client key path")
		caPath     = flag.String("ca", "certs/ca.crt", "CA certificate path")
		serverName = flag.String("server-name", "", "override expected server name (defaults to advert value)")
		insecure   = flag.Bool("insecure-skip-verify", false, "skip TLS and advert CA verification (NOT recommended)")
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

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	adverts, err := discovery.Listen(ctx, *multicast, roots)
	if err != nil {
		log.Fatalf("listen multicast: %v", err)
	}

	seen := make(map[string]time.Time)
	for {
		select {
		case ad, ok := <-adverts:
			if !ok {
				return
			}
			if _, exists := seen[ad.ID]; exists {
				continue
			}
			seen[ad.ID] = time.Now()

			expected := ad.ServerName
			if *serverName != "" {
				expected = *serverName
			}
			tlsCfg := config.ClientTLSConfig(cert, roots, expected, *insecure)
			log.Printf("connecting to %s (id=%s)...", ad.Callback, ad.ID)
			if err := client.Download(ctx, ad.Callback, tlsCfg, *outDir); err != nil {
				log.Printf("download failed: %v", err)
				continue
			}
			log.Printf("download from %s completed", ad.Callback)
		case <-ctx.Done():
			return
		}
	}
}
