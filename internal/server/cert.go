package server

import (
	"bytes"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"os"

	"github.com/4fuu/box/internal/tunnel"
)

func loadOrCreateCert(path string) (tls.Certificate, string, error) {
	raw, err := os.ReadFile(path)
	if err == nil {
		cert, err := tls.X509KeyPair(raw, raw)
		if err != nil {
			return tls.Certificate{}, "", err
		}
		if len(cert.Certificate) == 0 {
			return tls.Certificate{}, "", errors.New("quic certificate is empty")
		}
		return cert, tunnel.Fingerprint(cert.Certificate[0]), nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return tls.Certificate{}, "", err
	}
	cert, _, err := tunnel.NewCertificate()
	if err != nil {
		return tls.Certificate{}, "", err
	}
	if err := writeCert(path, cert); err != nil {
		return tls.Certificate{}, "", err
	}
	if len(cert.Certificate) == 0 {
		return tls.Certificate{}, "", errors.New("quic certificate is empty")
	}
	return cert, tunnel.Fingerprint(cert.Certificate[0]), nil
}

func writeCert(path string, cert tls.Certificate) error {
	var buf bytes.Buffer
	if err := pem.Encode(&buf, &pem.Block{Type: "CERTIFICATE", Bytes: cert.Certificate[0]}); err != nil {
		return err
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(cert.PrivateKey)
	if err != nil {
		return err
	}
	if err := pem.Encode(&buf, &pem.Block{Type: "PRIVATE KEY", Bytes: keyDER}); err != nil {
		return err
	}
	if err := os.WriteFile(path, buf.Bytes(), 0o600); err != nil {
		return err
	}
	return os.Chmod(path, 0o600)
}
