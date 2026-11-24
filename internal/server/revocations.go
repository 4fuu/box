package server

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"crypto/tls"
	"encoding/hex"
	"errors"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/4fuu/box/internal/tunnel"
)

// RevocationPeerConfig enables the two-node revocation transport. The writer
// sets Address; the replica sets Listen. Both pin the other node's certificate.
type RevocationPeerConfig struct {
	Address     string
	Listen      string
	Fingerprint string
}

const revocationPath = "/v1/token-revocations"

var errRevocationPending = errors.New("token revoked locally; replica confirmation pending")

func (s *Server) startRevocations(ctx context.Context, cert tls.Certificate, cfg RevocationPeerConfig) error {
	pin := strings.ToLower(strings.TrimSpace(cfg.Fingerprint))
	digest, err := hex.DecodeString(pin)
	if err != nil || len(digest) != sha256.Size || pin == s.fingerprint || (cfg.Address == "") == (cfg.Listen == "") {
		return errors.New("revocation peer needs one address or listen address and a different peer certificate fingerprint")
	}
	tlsConfig := &tls.Config{
		MinVersion:   tls.VersionTLS13,
		Certificates: []tls.Certificate{cert},
		VerifyConnection: func(cs tls.ConnectionState) error {
			if len(cs.PeerCertificates) == 0 || subtle.ConstantTimeCompare([]byte(tunnel.Fingerprint(cs.PeerCertificates[0].Raw)), []byte(pin)) != 1 {
				return errors.New("revocation peer certificate does not match")
			}
			return nil
		},
	}
	if cfg.Listen != "" {
		ln, err := net.Listen("tcp", cfg.Listen)
		if err != nil {
			return err
		}
		tlsConfig.ClientAuth = tls.RequireAnyClientCert
		mux := http.NewServeMux()
		mux.HandleFunc("POST "+revocationPath, s.receiveRevocation)
		s.revocationSrv = &http.Server{
			Handler: mux, TLSConfig: tlsConfig, ReadTimeout: 3 * time.Second, WriteTimeout: 3 * time.Second,
			ErrorLog: log.New(io.Discard, "", 0),
		}
		s.svc.RevokeToken = func(int64) error { return errors.New("revoke tokens on the configuration writer") }
		go s.revocationSrv.ServeTLS(ln, "", "")
		return nil
	}
	if host, port, err := net.SplitHostPort(cfg.Address); err != nil || host == "" || port == "" {
		return errors.New("invalid revocation peer address")
	}
	// VerifyConnection checks the explicit pin rather than a public CA or DNS name.
	tlsConfig.InsecureSkipVerify = true
	transport := &http.Transport{TLSClientConfig: tlsConfig}
	client := &http.Client{
		Transport: transport, Timeout: 2 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	endpoint := (&url.URL{Scheme: "https", Host: cfg.Address, Path: revocationPath}).String()
	send := func(hash string) error {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(hash))
		if err != nil {
			return err
		}
		res, err := client.Do(req)
		if err != nil {
			return err
		}
		res.Body.Close()
		if res.StatusCode != http.StatusNoContent {
			return errors.New("replica did not confirm revocation")
		}
		return s.store.ConfirmTokenRevocation(ctx, hash)
	}
	s.svc.RevokeToken = func(id int64) error {
		hash, err := s.store.QueueTokenRevocation(ctx, id)
		if err != nil {
			return err
		}
		if err := send(hash); err != nil {
			return errRevocationPending
		}
		return nil
	}
	done := make(chan struct{})
	s.revocationDone = done
	go func() {
		defer close(done)
		defer transport.CloseIdleConnections()
		tick := time.NewTicker(time.Second)
		defer tick.Stop()
		for {
			if hashes, err := s.store.PendingTokenRevocations(ctx); err == nil {
				for _, hash := range hashes {
					if err := send(hash); err != nil {
						break
					}
				}
			}
			select {
			case <-ctx.Done():
				return
			case <-tick.C:
			}
		}
	}()
	return nil
}

func (s *Server) receiveRevocation(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, sha256.Size*2))
	if err != nil || len(body) != sha256.Size*2 {
		http.Error(w, "invalid token revocation", http.StatusBadRequest)
		return
	}
	if err := s.store.ApplyTokenRevocation(r.Context(), string(body)); err != nil {
		http.Error(w, "token revocation not committed", http.StatusServiceUnavailable)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
