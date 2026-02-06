package config

import (
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"os"
)

// TLSFiles bundles common TLS file paths.
type TLSFiles struct {
	CertPath string
	KeyPath  string
	CAPath   string
}

// LoadCertificate loads a certificate/key pair from disk.
func LoadCertificate(certPath, keyPath string) (tls.Certificate, error) {
	cert, err := tls.LoadX509KeyPair(certPath, keyPath)
	if err != nil {
		return tls.Certificate{}, fmt.Errorf("load keypair: %w", err)
	}
	return cert, nil
}

// LoadCertPool loads a CA bundle from disk.
func LoadCertPool(caPath string) (*x509.CertPool, error) {
	caPEM, err := os.ReadFile(caPath)
	if err != nil {
		return nil, fmt.Errorf("read ca: %w", err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(caPEM) {
		return nil, fmt.Errorf("append ca: no certs found")
	}
	return pool, nil
}

// ClientTLSConfig builds a mutual‑TLS client configuration.
func ClientTLSConfig(cert tls.Certificate, roots *x509.CertPool, serverName string, insecureSkipVerify bool) *tls.Config {
	return &tls.Config{
		Certificates:       []tls.Certificate{cert},
		RootCAs:            roots,
		ServerName:         serverName,
		MinVersion:         tls.VersionTLS13,
		InsecureSkipVerify: insecureSkipVerify,
	}
}

// ServerTLSConfig builds a mutual‑TLS server configuration.
func ServerTLSConfig(cert tls.Certificate, roots *x509.CertPool, insecureSkipVerify bool) *tls.Config {
	cfg := &tls.Config{
		Certificates: []tls.Certificate{cert},
		MinVersion:   tls.VersionTLS13,
	}
	if insecureSkipVerify {
		// Still require a certificate but don't verify its chain.
		cfg.ClientAuth = tls.RequireAnyClientCert
	} else {
		cfg.ClientCAs = roots
		cfg.ClientAuth = tls.RequireAndVerifyClientCert
	}
	return cfg
}
