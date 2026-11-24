package server

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"time"

	"github.com/4fuu/box/internal/store"
	"github.com/4fuu/box/internal/tunnel"
)

// PeerConfig is the private link between two box nodes. Address and
// Fingerprint name the other node; Listen is this node. Writer marks the one
// node allowed to change configuration.
type PeerConfig struct {
	Address     string
	Listen      string
	Fingerprint string
	Writer      bool
	// QUIC is the other node's public QUIC address, handed to computers at
	// join. Advertise is this node's own public QUIC address.
	QUIC      string
	Advertise string
}

func (p PeerConfig) enabled() bool { return p.Address != "" || p.Listen != "" }

const (
	peerConfigPath    = "/v1/config"
	peerPortalAddPath = "/v1/portal/add"
	peerPortalRmPath  = "/v1/portal/rm"
	peerPortalPrefix  = "/peer/portal"
	peerConfigLimit   = 8 << 20
	peerRPCLimit      = 64 << 10
	peerRPCTimeout    = 5 * time.Second
)

var errNotWriter = errors.New("this node is not the configuration writer")

// peerLink is the outbound half of the node link. The transport is shared by
// config calls and portal forwarding, so a forwarded request can reuse a
// connection the previous one left open.
type peerLink struct {
	address   string
	client    *http.Client
	transport *http.Transport
	forward   *httputil.ReverseProxy
}

func (s *Server) startPeer(ctx context.Context, cert tls.Certificate, cfg PeerConfig) error {
	pin := strings.ToLower(strings.TrimSpace(cfg.Fingerprint))
	digest, err := hex.DecodeString(pin)
	if err != nil || len(digest) != sha256.Size {
		return errors.New("--peer-fingerprint must be a sha256 hex digest")
	}
	if pin == s.fingerprint {
		return errors.New("--peer-fingerprint is this node's own certificate")
	}
	if cfg.Address == "" || cfg.Listen == "" {
		return errors.New("two-node mode needs both --peer-addr and --peer-listen")
	}
	if host, port, err := net.SplitHostPort(cfg.Address); err != nil || host == "" || port == "" {
		return errors.New("invalid --peer-addr")
	}
	if cfg.QUIC != "" {
		if host, port, err := net.SplitHostPort(cfg.QUIC); err != nil || host == "" || port == "" {
			return errors.New("invalid --peer-quic")
		}
	}
	if cfg.Advertise != "" {
		if host, port, err := net.SplitHostPort(cfg.Advertise); err != nil || host == "" || port == "" {
			return errors.New("invalid --quic-advertise")
		}
	}
	if cfg.QUIC == "" || cfg.Advertise == "" {
		return errors.New("two-node mode needs --peer-quic and --quic-advertise so computers can reach either node")
	}

	tlsConfig := &tls.Config{
		MinVersion:   tls.VersionTLS13,
		Certificates: []tls.Certificate{cert},
		VerifyConnection: func(cs tls.ConnectionState) error {
			if len(cs.PeerCertificates) == 0 ||
				subtle.ConstantTimeCompare([]byte(tunnel.Fingerprint(cs.PeerCertificates[0].Raw)), []byte(pin)) != 1 {
				return errors.New("peer certificate does not match")
			}
			return nil
		},
	}
	serverTLS := tlsConfig.Clone()
	serverTLS.ClientAuth = tls.RequireAnyClientCert
	clientTLS := tlsConfig.Clone()
	// The pin above is the identity check; there is no CA or DNS name to verify.
	clientTLS.InsecureSkipVerify = true

	mux := http.NewServeMux()
	mux.HandleFunc("POST "+peerConfigPath, s.receiveConfig)
	mux.HandleFunc("GET "+peerConfigPath, s.sendConfig)
	mux.HandleFunc("POST "+peerPortalAddPath, s.receivePortalAdd)
	mux.HandleFunc("POST "+peerPortalRmPath, s.receivePortalRm)
	mux.Handle(peerPortalPrefix+"/", http.StripPrefix(peerPortalPrefix, http.HandlerFunc(s.servePeerPortal)))

	ln, err := net.Listen("tcp", cfg.Listen)
	if err != nil {
		return err
	}
	s.peerSrv = &http.Server{
		Handler:           mux,
		TLSConfig:         serverTLS,
		ReadHeaderTimeout: 10 * time.Second,
		ErrorLog:          log.New(io.Discard, "", 0),
	}
	go s.peerSrv.ServeTLS(ln, "", "")

	transport := &http.Transport{
		TLSClientConfig:     clientTLS,
		MaxIdleConnsPerHost: 8,
		IdleConnTimeout:     60 * time.Second,
		DisableCompression:  true,
		ForceAttemptHTTP2:   false,
		// HTTP/1.1 carries WebSocket upgrades; HTTP/2 does not.
		TLSNextProto: map[string]func(string, *tls.Conn) http.RoundTripper{},
	}
	link := &peerLink{
		address:   cfg.Address,
		transport: transport,
		client: &http.Client{
			Transport: transport,
			Timeout:   peerRPCTimeout,
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
	}
	link.forward = &httputil.ReverseProxy{
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.Out.URL.Scheme = "https"
			pr.Out.URL.Host = cfg.Address
			pr.Out.URL.Path = peerPortalPrefix + pr.In.URL.Path
			pr.Out.URL.RawPath = ""
			pr.Out.Host = pr.In.Host
		},
		Transport:     transport,
		FlushInterval: -1,
		ErrorHandler: func(w http.ResponseWriter, _ *http.Request, _ error) {
			s.svc.Metrics.Failed()
			w.WriteHeader(http.StatusServiceUnavailable)
		},
	}
	s.peer = link
	s.configWriter = cfg.Writer
	fmt.Fprintf(s.cfg.Stdout, "peer mode: %s; this node's certificate fingerprint %s\n",
		map[bool]string{true: "configuration writer", false: "configuration replica"}[cfg.Writer], s.fingerprint)
	if cfg.Writer {
		s.peerWG.Add(1)
		go s.replicateLoop(ctx)
	} else {
		s.peerWG.Add(1)
		go s.pullConfig(ctx)
	}
	return nil
}

// configChanged records that the writer owes the replica a snapshot and
// wakes the loop. It does not wait: a change is committed locally first.
// The counter is the source of truth, so a change that lands while a push is
// in flight cannot be cleared by that push finishing.
func (s *Server) configChanged() {
	if s.peer == nil || !s.configWriter {
		return
	}
	s.configDirty.Add(1)
	select {
	case s.configWake <- struct{}{}:
	default:
	}
}

func (s *Server) replicateLoop(ctx context.Context) {
	defer s.peerWG.Done()
	tick := time.NewTicker(time.Second)
	defer tick.Stop()
	first := true
	for {
		if first || s.configFailed.Load() || s.configDirty.Load() != s.configSent.Load() {
			s.syncConfig(ctx)
		}
		first = false
		select {
		case <-ctx.Done():
			return
		case <-s.configWake:
		case <-tick.C:
		}
	}
}

// syncConfig pushes the whole configuration at a new revision. A full
// snapshot makes replay idempotent: a lost acknowledgement, a crash, or a
// replica restart only costs another copy.
func (s *Server) syncConfig(ctx context.Context) {
	s.configMu.Lock()
	defer s.configMu.Unlock()
	// Read before the snapshot: anything that lands after this point is
	// either included in the snapshot or left for the next pass.
	dirty := s.configDirty.Load()
	ctx, cancel := context.WithTimeout(ctx, peerRPCTimeout)
	defer cancel()
	rev, err := s.store.BumpConfigRevision(ctx)
	if err == nil {
		var snap store.ConfigSnapshot
		if snap, err = s.store.ConfigSnapshot(ctx, rev); err == nil {
			err = s.peer.PushConfig(ctx, snap)
		}
	}
	if err != nil {
		if ctx.Err() != nil {
			return
		}
		s.configFailed.Store(true)
		s.warnReplica(err)
		return
	}
	s.configFailed.Store(false)
	s.configSent.Store(dirty)
	if s.configWarned {
		fmt.Fprintf(s.cfg.Stdout, "replica %s confirmed configuration revision %d\n", s.peer.address, rev)
		s.configWarned = false
	}
}

func (s *Server) warnReplica(err error) {
	if s.configWarned {
		return
	}
	s.configWarned = true
	fmt.Fprintf(s.cfg.Stdout, "warning: configuration saved on this node; replica %s has not confirmed: %v\n", s.peer.address, err)
}

// pullConfig asks the writer for the current configuration at startup, so a
// replica that was offline through the writer's pushes still converges.
func (s *Server) pullConfig(ctx context.Context) {
	defer s.peerWG.Done()
	for {
		if err := s.fetchConfig(ctx); err == nil {
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(2 * time.Second):
		}
	}
}

func (s *Server) fetchConfig(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, peerRPCTimeout)
	defer cancel()
	snap, err := s.peer.FetchConfig(ctx)
	if err != nil {
		return err
	}
	applied, err := s.store.ApplyConfigSnapshot(ctx, snap)
	if err != nil {
		return err
	}
	if applied {
		s.reloadConfig()
	}
	return nil
}

// reloadConfig drops derived state after a snapshot: cached portal dialers
// and tunnels whose computer the writer deleted.
func (s *Server) reloadConfig() {
	s.dropAllPortalIdle()
	if s.svc.Live != nil {
		for _, c := range s.svc.Live.All() {
			name := s.svc.Live.CurrentName(c)
			if _, err := s.store.Computer(name); err != nil {
				s.svc.Live.Forget(c)
				_ = c.Close()
			}
		}
	}
	go s.svc.PushKeys(context.Background())
}

func (s *Server) sendConfig(w http.ResponseWriter, r *http.Request) {
	if s.peer == nil || !s.configWriter {
		http.Error(w, errNotWriter.Error(), http.StatusConflict)
		return
	}
	rev, err := s.store.ConfigRevision(r.Context())
	if err == nil && rev == 0 {
		rev, err = s.store.BumpConfigRevision(r.Context())
	}
	var snap store.ConfigSnapshot
	if err == nil {
		snap, err = s.store.ConfigSnapshot(r.Context(), rev)
	}
	if err != nil {
		http.Error(w, "configuration unavailable", http.StatusServiceUnavailable)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(snap)
}

func (s *Server) receiveConfig(w http.ResponseWriter, r *http.Request) {
	if s.peer == nil || s.configWriter {
		http.Error(w, "this node is not a configuration replica", http.StatusConflict)
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, peerConfigLimit))
	if err != nil {
		http.Error(w, "invalid configuration", http.StatusBadRequest)
		return
	}
	var snap store.ConfigSnapshot
	if err := json.Unmarshal(body, &snap); err != nil {
		http.Error(w, "invalid configuration", http.StatusBadRequest)
		return
	}
	applied, err := s.store.ApplyConfigSnapshot(r.Context(), snap)
	if err != nil {
		http.Error(w, "configuration not committed", http.StatusServiceUnavailable)
		return
	}
	if applied {
		s.reloadConfig()
	}
	w.WriteHeader(http.StatusNoContent)
}

// servePeerPortal runs a request the other node forwarded. It never forwards
// again: a request that cannot be served here fails here.
func (s *Server) servePeerPortal(w http.ResponseWriter, r *http.Request) {
	p, err := s.store.PortalByHost(hostname(r.Host))
	if err != nil {
		w.WriteHeader(http.StatusMisdirectedRequest)
		_, _ = io.WriteString(w, "unknown host\n")
		return
	}
	s.servePortal(w, r, p, false)
}

func (s *Server) forwardPortal(w http.ResponseWriter, r *http.Request) {
	s.peer.forward.ServeHTTP(w, r)
}

type peerPortalAddRequest struct {
	Computer string `json:"computer"`
	Label    string `json:"label"`
	Domain   string `json:"domain"`
	Port     int    `json:"port"`
	Private  bool   `json:"private"`
}

func (s *Server) receivePortalAdd(w http.ResponseWriter, r *http.Request) {
	if s.peer == nil || !s.configWriter {
		http.Error(w, errNotWriter.Error(), http.StatusConflict)
		return
	}
	var req peerPortalAddRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, peerRPCLimit)).Decode(&req); err != nil {
		http.Error(w, "invalid request", http.StatusBadRequest)
		return
	}
	res, err := s.svc.AddPortal(req.Computer, req.Label, req.Domain, req.Port, req.Private)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	s.dropPortalIdle(req.Computer)
	// The replica's caller is waiting for this portal to exist here; push
	// before answering so its next request sees the claim. The push is not
	// tied to the caller's request: the claim is already committed.
	ctx, cancel := context.WithTimeout(context.Background(), peerRPCTimeout)
	defer cancel()
	s.syncConfig(ctx)
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(res)
}

func (s *Server) receivePortalRm(w http.ResponseWriter, r *http.Request) {
	if s.peer == nil || !s.configWriter {
		http.Error(w, errNotWriter.Error(), http.StatusConflict)
		return
	}
	var req peerPortalAddRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, peerRPCLimit)).Decode(&req); err != nil {
		http.Error(w, "invalid request", http.StatusBadRequest)
		return
	}
	if err := s.svc.RemovePortal(req.Computer, req.Label, req.Domain); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	s.dropPortalIdle(req.Computer)
	ctx, cancel := context.WithTimeout(context.Background(), peerRPCTimeout)
	defer cancel()
	s.syncConfig(ctx)
	w.WriteHeader(http.StatusNoContent)
}

func (p *peerLink) PushConfig(ctx context.Context, snap store.ConfigSnapshot) error {
	return p.post(ctx, peerConfigPath, snap, nil)
}

func (p *peerLink) FetchConfig(ctx context.Context) (store.ConfigSnapshot, error) {
	var snap store.ConfigSnapshot
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, p.base(peerConfigPath), nil)
	if err != nil {
		return snap, err
	}
	res, err := p.client.Do(req)
	if err != nil {
		return snap, fmt.Errorf("peer %s: %w", p.address, err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return snap, p.statusError(res)
	}
	err = json.NewDecoder(io.LimitReader(res.Body, peerConfigLimit)).Decode(&snap)
	return snap, err
}

func (p *peerLink) AddPortal(ctx context.Context, req peerPortalAddRequest) (tunnel.PortalAddResponse, error) {
	var out tunnel.PortalAddResponse
	err := p.post(ctx, peerPortalAddPath, req, &out)
	return out, err
}

func (p *peerLink) RemovePortal(ctx context.Context, computer, label, domain string) error {
	return p.post(ctx, peerPortalRmPath, peerPortalAddRequest{Computer: computer, Label: label, Domain: domain}, nil)
}

func (p *peerLink) base(path string) string {
	return (&url.URL{Scheme: "https", Host: p.address, Path: path}).String()
}

func (p *peerLink) post(ctx context.Context, path string, body, out any) error {
	raw, err := json.Marshal(body)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.base(path), bytes.NewReader(raw))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	res, err := p.client.Do(req)
	if err != nil {
		return fmt.Errorf("peer %s: %w", p.address, err)
	}
	defer res.Body.Close()
	if res.StatusCode < 200 || res.StatusCode > 299 {
		return p.statusError(res)
	}
	if out == nil {
		_, _ = io.Copy(io.Discard, res.Body)
		return nil
	}
	return json.NewDecoder(io.LimitReader(res.Body, peerConfigLimit)).Decode(out)
}

func (p *peerLink) statusError(res *http.Response) error {
	body, _ := io.ReadAll(io.LimitReader(res.Body, 1024))
	msg := strings.TrimSpace(string(body))
	if msg == "" {
		msg = res.Status
	}
	return fmt.Errorf("peer %s: %s", p.address, msg)
}
