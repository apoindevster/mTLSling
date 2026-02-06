package discovery

import (
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"net"
	"time"
)

// Advert is a signed multicast discovery payload.
type Advert struct {
	ID         string `json:"id"`
	Callback   string `json:"callback"`
	ServerName string `json:"server_name"`
	Timestamp  int64  `json:"ts"`
	CertPEM    string `json:"cert_pem"`
	Signature  string `json:"sig"`
}

// NewAdvert creates and signs an Advert using the provided TLS certificate.
func NewAdvert(id, callback, serverName string, cert tls.Certificate) (*Advert, error) {
	if len(cert.Certificate) == 0 {
		return nil, errors.New("certificate is empty")
	}

	leaf, err := x509.ParseCertificate(cert.Certificate[0])
	if err != nil {
		return nil, fmt.Errorf("parse certificate: %w", err)
	}

	priv := cert.PrivateKey
	if priv == nil {
		return nil, errors.New("certificate missing private key")
	}

	ad := &Advert{
		ID:         id,
		Callback:   callback,
		ServerName: serverName,
		Timestamp:  time.Now().Unix(),
		CertPEM:    string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: leaf.Raw})),
	}

	payload := ad.signingPayload()

	sig, err := sign(priv, payload)
	if err != nil {
		return nil, fmt.Errorf("sign advert: %w", err)
	}

	ad.Signature = base64.StdEncoding.EncodeToString(sig)
	return ad, nil
}

func (a *Advert) signingPayload() []byte {
	h := sha256.New()
	h.Write([]byte(a.ID))
	h.Write([]byte("|"))
	h.Write([]byte(a.Callback))
	h.Write([]byte("|"))
	h.Write([]byte(a.ServerName))
	h.Write([]byte("|"))
	h.Write([]byte(fmt.Sprintf("%d", a.Timestamp)))
	return h.Sum(nil)
}

// Marshal returns the JSON encoding of the advert.
func (a *Advert) Marshal() ([]byte, error) {
	return json.Marshal(a)
}

// Verify checks signature and certificate against the provided roots.
func (a *Advert) Verify(roots *x509.CertPool, maxSkew time.Duration) error {
	if maxSkew <= 0 {
		maxSkew = 2 * time.Minute
	}
	now := time.Now().Unix()
	if now-a.Timestamp > int64(maxSkew.Seconds()) || a.Timestamp-now > int64(maxSkew.Seconds()) {
		return fmt.Errorf("advert timestamp skew too large")
	}

	block, _ := pem.Decode([]byte(a.CertPEM))
	if block == nil {
		return errors.New("unable to decode certificate pem")
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return fmt.Errorf("parse cert: %w", err)
	}

	if roots != nil {
		opts := x509.VerifyOptions{Roots: roots}
		if _, err := cert.Verify(opts); err != nil {
			return fmt.Errorf("verify cert: %w", err)
		}
	}

	sig, err := base64.StdEncoding.DecodeString(a.Signature)
	if err != nil {
		return fmt.Errorf("decode signature: %w", err)
	}

	if err := verify(cert.PublicKey, a.signingPayload(), sig); err != nil {
		return fmt.Errorf("verify signature: %w", err)
	}
	return nil
}

// Listen joins the multicast group at addr (e.g. "239.255.42.99:9999") and emits verified adverts.
func Listen(ctx context.Context, addr string, roots *x509.CertPool) (<-chan *Advert, error) {
	laddr, err := net.ResolveUDPAddr("udp", addr)
	if err != nil {
		return nil, fmt.Errorf("resolve multicast addr: %w", err)
	}

	conn, err := net.ListenMulticastUDP("udp", nil, laddr)
	if err != nil {
		return nil, fmt.Errorf("listen multicast: %w", err)
	}
	if err := conn.SetReadBuffer(2 << 20); err != nil {
		// non-fatal
	}

	out := make(chan *Advert)

	go func() {
		defer close(out)
		defer conn.Close()
		buf := make([]byte, 64<<10)
		for {
			conn.SetReadDeadline(time.Now().Add(5 * time.Second))
			n, _, err := conn.ReadFrom(buf)
			if err != nil {
				if ne, ok := err.(net.Error); ok && ne.Timeout() {
					if ctx.Err() != nil {
						return
					}
					continue
				}
				return
			}

			var ad Advert
			if err := json.Unmarshal(buf[:n], &ad); err != nil {
				continue // drop malformed
			}
			if err := ad.Verify(roots, 2*time.Minute); err != nil {
				continue
			}
			select {
			case out <- &ad:
			case <-ctx.Done():
				return
			}
		}
	}()

	return out, nil
}

// Broadcast periodically sends the advert to the multicast group until ctx is cancelled.
func Broadcast(ctx context.Context, addr string, advert *Advert, interval time.Duration) error {
	if interval <= 0 {
		interval = 5 * time.Second
	}

	raddr, err := net.ResolveUDPAddr("udp", addr)
	if err != nil {
		return fmt.Errorf("resolve multicast addr: %w", err)
	}
	conn, err := net.DialUDP("udp", nil, raddr)
	if err != nil {
		return fmt.Errorf("dial multicast: %w", err)
	}
	defer conn.Close()

	payload, err := advert.Marshal()
	if err != nil {
		return fmt.Errorf("marshal advert: %w", err)
	}

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		if _, err := conn.Write(payload); err != nil {
			return err
		}
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}
	}
}

func sign(privKey crypto.PrivateKey, payload []byte) ([]byte, error) {
	switch k := privKey.(type) {
	case *rsa.PrivateKey:
		return rsa.SignPSS(rand.Reader, k, crypto.SHA256, payload, nil)
	case *ecdsa.PrivateKey:
		r, s, err := ecdsa.Sign(rand.Reader, k, payload)
		if err != nil {
			return nil, err
		}
		return append(r.Bytes(), s.Bytes()...), nil
	default:
		return nil, fmt.Errorf("unsupported key type %T", privKey)
	}
}

func verify(pub crypto.PublicKey, payload, sig []byte) error {
	switch k := pub.(type) {
	case *rsa.PublicKey:
		return rsa.VerifyPSS(k, crypto.SHA256, payload, sig, nil)
	case *ecdsa.PublicKey:
		// split r|s
		if len(sig)%2 != 0 {
			return errors.New("bad ecdsa signature length")
		}
		mid := len(sig) / 2
		r := sig[:mid]
		s := sig[mid:]
		var rr, ss = new(big.Int).SetBytes(r), new(big.Int).SetBytes(s)
		if !ecdsa.Verify(k, payload, rr, ss) {
			return errors.New("ecdsa verify failed")
		}
		return nil
	default:
		return fmt.Errorf("unsupported key type %T", pub)
	}
}
