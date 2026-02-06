# multi-cast-transfer

Two Go binaries for local network file transfer with multicast discovery and mutual TLS.

## Binaries
- `listener` (headless): Joins a multicast group, authenticates signed adverts, then connects back over TLS and downloads advertised files.
- `tui` (Bubble Tea UI): Lets you pick files to share, starts/stops the TLS listener, broadcasts signed adverts over multicast, and shows incoming connection events.

## Quick start
1. Generate a small PKI (CA + server + client certs). Example:
   ```bash
   mkdir -p certs
   openssl req -x509 -newkey rsa:2048 -keyout certs/ca.key -out certs/ca.crt -days 365 -nodes -subj "/CN=ca"
   cat > certs/openssl.cnf <<'CONF'
   [ req ]
   default_bits       = 2048
   distinguished_name = req_distinguished_name
   prompt             = no
   req_extensions     = req_ext

   [ req_distinguished_name ]
   CN = transfer.local

   [ req_ext ]
   subjectAltName = @alt_names

   [ alt_names ]
   DNS.1 = transfer.local
   DNS.2 = localhost
   IP.1  = 127.0.0.1
   CONF

   openssl req -newkey rsa:2048 -nodes -keyout certs/server.key -out certs/server.csr -config certs/openssl.cnf
   openssl x509 -req -in certs/server.csr -CA certs/ca.crt -CAkey certs/ca.key -CAcreateserial -out certs/server.crt -days 365 -extfile certs/openssl.cnf -extensions req_ext
   openssl req -newkey rsa:2048 -nodes -keyout certs/client.key -out certs/client.csr -subj "/CN=client"
   openssl x509 -req -in certs/client.csr -CA certs/ca.crt -CAkey certs/ca.key -CAcreateserial -out certs/client.crt -days 365
   ```

2. Start the TUI sender (select files, press `s` to start broadcasting and listening):
   ```bash
   GOCACHE=/tmp/go-cache GOMODCACHE=/tmp/go-mod go run ./cmd/tui \
     -cert certs/server.crt -key certs/server.key -ca certs/ca.crt \
     -addr ":8443" -multicast "239.255.42.99:9999" -server-name "transfer.local" -id sender-1
   ```

3. Run the headless listener on another host (downloads into ./downloads by default):
   ```bash
   GOCACHE=/tmp/go-cache GOMODCACHE=/tmp/go-mod go run ./cmd/listener \
     -cert certs/client.crt -key certs/client.key -ca certs/ca.crt \
     -multicast "239.255.42.99:9999" -out downloads
   ```

## Notes
- Discovery messages are signed with the server certificate and verified against the CA bundle before any TCP connection is attempted.
- Both discovery and TCP transfers enforce TLS 1.3 mutual authentication.
- Listener caches advert IDs to avoid repeated downloads in a single run.
- Environment variables `GOCACHE` and `GOMODCACHE` are set in the examples to keep build artifacts inside writable directories.
- Both binaries expose `-insecure-skip-verify` to bypass CA validation for testing (not recommended in production).

## Next steps
- Add persistence for selected files/config in the TUI.
- Add transfer progress bars and per-connection cancellation.
- Add optional rate limiting and retry backoff on the listener.
