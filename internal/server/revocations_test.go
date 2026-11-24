package server_test

import (
	"crypto/tls"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/4fuu/box/internal/secret"
	"github.com/4fuu/box/internal/server"
	"github.com/4fuu/box/internal/store"
	"github.com/4fuu/box/internal/tunnel"
)

func TestTokenRevocationPeerRecoversAfterOutage(t *testing.T) {
	newConfig := func() server.Config {
		dir := t.TempDir()
		return server.Config{
			Domains: []string{"box.example.com"}, DataDir: dir, SocketPath: filepath.Join(dir, "box.sock"),
			SSHAddr: "127.0.0.1:0", QUICAddr: "127.0.0.1:0", HTTPAddr: "127.0.0.1:0", Stdout: io.Discard,
		}
	}
	start := func(c server.Config) *server.Server {
		t.Helper()
		s, err := server.Start(t.Context(), c)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = s.Close() })
		return s
	}
	open := func(c server.Config) *store.Store {
		t.Helper()
		s, err := store.Open(filepath.Join(c.DataDir, "box.db"))
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = s.Close() })
		return s
	}
	writerCfg, replicaCfg := newConfig(), newConfig()
	writer, replica := start(writerCfg), start(replicaCfg)
	peerAddr := freeTCP(t)
	writerCfg.Revocations = &server.RevocationPeerConfig{Address: peerAddr, Fingerprint: replica.QUICFingerprint()}
	replicaCfg.Revocations = &server.RevocationPeerConfig{Listen: peerAddr, Fingerprint: writer.QUICFingerprint()}
	closeNode := func(s *server.Server) {
		t.Helper()
		if err := s.Close(); err != nil {
			t.Fatal(err)
		}
	}
	closeNode(writer)
	closeNode(replica)
	const online, offline, unaffected = "revoke-online", "revoke-offline", "keep-this-token"
	add := func(s *store.Store, raw string) int64 {
		t.Helper()
		token, err := s.AddToken(raw, "", time.Time{})
		if err != nil {
			t.Fatal(err)
		}
		return token.ID
	}
	writerDB, replicaDB := open(writerCfg), open(replicaCfg)
	onlineID, offlineID := add(writerDB, online), add(writerDB, offline)
	add(writerDB, unaffected)
	add(replicaDB, unaffected)
	peerOnlineID := add(replicaDB, online)
	add(replicaDB, offline)
	writer, replica = start(writerCfg), start(replicaCfg)

	status := func(s *server.Server, raw string) int {
		code, _ := do(t, s.HTTPAddr(), http.MethodGet, "event.box.example.com", "/api/events", raw, nil)
		return code
	}
	remove := func(cfg server.Config, id int64) error {
		return localCall(t, cfg.SocketPath, "token_rm", map[string]int64{"id": id}, nil)
	}
	if err := remove(replicaCfg, peerOnlineID); err == nil {
		t.Fatal("replica accepted a local revocation")
	}
	otherCert, _, err := tunnel.NewCertificate()
	if err != nil {
		t.Fatal(err)
	}
	transport := &http.Transport{TLSClientConfig: &tls.Config{
		InsecureSkipVerify: true, Certificates: []tls.Certificate{otherCert},
	}}
	t.Cleanup(transport.CloseIdleConnections)
	client := &http.Client{Transport: transport, Timeout: 2 * time.Second}
	response, err := client.Post("https://"+peerAddr+"/v1/token-revocations", "text/plain", strings.NewReader(secret.Hash(online)))
	if response != nil {
		response.Body.Close()
	}
	if err == nil || status(replica, online) != http.StatusOK {
		t.Fatal("an unpinned peer was not rejected at TLS without revoking the token")
	}
	if err := remove(writerCfg, onlineID); err != nil {
		t.Fatal(err)
	}
	if status(writer, online) != http.StatusUnauthorized || status(replica, online) != http.StatusUnauthorized {
		t.Fatal("acknowledged revocation did not deny access on both nodes")
	}
	closeNode(replica)
	if err := remove(writerCfg, offlineID); err == nil || !strings.Contains(err.Error(), "replica confirmation pending") {
		t.Fatalf("revocation during outage: %v", err)
	}
	if status(writer, offline) != http.StatusUnauthorized {
		t.Fatal("writer still accepts the locally revoked token")
	}
	closeNode(writer)
	replica = start(replicaCfg)
	if status(replica, offline) != http.StatusOK {
		t.Fatal("replica unexpectedly received the revocation while offline")
	}
	writer = start(writerCfg)
	timeout := time.Now().Add(5 * time.Second)
	for {
		pending, err := writerDB.PendingTokenRevocations(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		if status(replica, offline) == http.StatusUnauthorized && len(pending) == 0 {
			break
		}
		if time.Now().After(timeout) {
			t.Fatal("writer did not replay and confirm its pending revocation after restart")
		}
		time.Sleep(20 * time.Millisecond)
	}
	if status(writer, unaffected) != http.StatusOK || status(replica, unaffected) != http.StatusOK {
		t.Fatal("revocation denied access to an unrelated token")
	}
}
