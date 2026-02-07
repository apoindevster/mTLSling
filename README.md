# multi-cast-transfer

Two Go binaries for local network file transfer with multicast discovery and mutual TLS.

## Binaries
- `senderd` (headless combined daemon): Runs both sender and listener components and exposes local IPC control APIs.
- `tui` (Bubble Tea UI): UI-only controller. Talks to `senderd` over IPC to manage sender file selection, component settings, and sender/listener start-stop states.
- `listener` (legacy standalone): Headless listener kept for compatibility; the combined daemon now supersedes it.

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

2. Start the combined headless daemon:
   ```bash
   GOCACHE=/tmp/go-cache GOMODCACHE=/tmp/go-mod go run ./cmd/senderd \
     -sender-cert certs/server.crt -sender-key certs/server.key -sender-ca certs/ca.crt \
     -sender-addr ":8443" -sender-multicast "239.255.42.99:9999" -sender-server-name "transfer.local" -sender-id sender-1 \
     -listener-cert certs/client.crt -listener-key certs/client.key -listener-ca certs/ca.crt \
     -listener-multicast "239.255.42.99:9999" -listener-out downloads \
     -ipc-socket /tmp/multi-cast-transfer.sock
   ```

3. Start the TUI and control the daemon over IPC:
   ```bash
   GOCACHE=/tmp/go-cache GOMODCACHE=/tmp/go-mod go run ./cmd/tui \
     -ipc-socket /tmp/multi-cast-transfer.sock
   ```

4. In the TUI:
- Pick sender files on the picker page.
- Open settings page (`g`) to edit all sender/listener daemon arguments.
- Toggle sender (`s`) and listener (`r`) from any page, or set `sender.enabled` / `listener.enabled` and press `w` on settings page to apply.

5. Optional: run legacy standalone listener on another host (downloads into ./downloads by default):
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
- Combined daemon exposes sender and listener security knobs independently:
- `-sender-insecure-skip-verify`
- `-listener-insecure-skip-verify`
- IPC is isolated in `internal/ipc` behind build tags (`!windows` and `windows`) so transport can be swapped for Windows later without changing daemon/TUI business logic.

## Next steps
- Add persistence for selected files/config in the TUI.
- Add transfer progress bars and per-connection cancellation.
- Add optional rate limiting and retry backoff on the listener.
