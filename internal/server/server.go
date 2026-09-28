// Package server is `box serve`: SSH entry, HTTP portals, QUIC tunnels, SQLite.
package server

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httputil"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/4fuu/box/internal/approve"
	"github.com/4fuu/box/internal/control"
	"github.com/4fuu/box/internal/event"
	"github.com/4fuu/box/internal/ident"
	"github.com/4fuu/box/internal/keys"
	"github.com/4fuu/box/internal/paths"
	"github.com/4fuu/box/internal/rpc"
	"github.com/4fuu/box/internal/store"
	"github.com/4fuu/box/internal/tunnel"
	"golang.org/x/crypto/ssh"
)

// Config is how the server process is started.
// Domain is required the first time. Later starts reuse the stored domain
// and the stored listen addresses; flags are ignored once those exist.
type Config struct {
	Domain     string
	DataDir    string
	SSHAddr    string
	HTTPAddr   string
	QUICAddr   string
	SocketPath string
	Stdout     io.Writer
}

// Server is a running box serve.
type Server struct {
	cfg         Config
	store       *store.Store
	svc         *control.Service
	httpLn      net.Listener
	sshLn       net.Listener
	sockLn      net.Listener
	httpSrv     *http.Server
	sshSrv      *sshServer
	quic        *tunnel.Server
	splice      ssh.Signer
	fingerprint string
	httpPort    int
	quicPort    int

	mu       sync.Mutex
	waiters  map[string]*joinWaiter
	cancel   context.CancelFunc
	once     sync.Once
	closeErr error
}

// Start opens the database, prints a one-time password on first init, and listens.
func Start(ctx context.Context, cfg Config) (*Server, error) {
	if cfg.DataDir == "" {
		cfg.DataDir = paths.DataDir
	}
	if cfg.SocketPath == "" {
		cfg.SocketPath = paths.ServerSocket
	}
	if cfg.Stdout == nil {
		cfg.Stdout = os.Stdout
	}
	if err := os.MkdirAll(cfg.DataDir, 0o700); err != nil {
		return nil, err
	}
	dbPath := filepath.Join(cfg.DataDir, "box.db")
	st, err := store.Open(dbPath)
	if err != nil {
		return nil, err
	}
	domain, err := persistedDomain(st, cfg.Domain)
	if err != nil {
		st.Close()
		return nil, err
	}
	sshAddr, httpAddr, quicAddr, err := persistedAddrs(st, cfg.SSHAddr, cfg.HTTPAddr, cfg.QUICAddr)
	if err != nil {
		st.Close()
		return nil, err
	}
	cfg.SSHAddr, cfg.HTTPAddr, cfg.QUICAddr = sshAddr, httpAddr, quicAddr

	splicePath := filepath.Join(cfg.DataDir, "splice_ed25519")
	spliceLine, err := keys.Generate(splicePath, "box-splice")
	if err != nil {
		st.Close()
		return nil, err
	}
	if err := os.Chmod(splicePath, 0o600); err != nil {
		st.Close()
		return nil, err
	}
	signer, err := keys.Signer(splicePath)
	if err != nil {
		st.Close()
		return nil, err
	}
	hostKey := filepath.Join(cfg.DataDir, "ssh_host_ed25519_key")
	if _, err := keys.Generate(hostKey, "box-host"); err != nil {
		st.Close()
		return nil, err
	}
	cert, fp, err := loadOrCreateCert(filepath.Join(cfg.DataDir, "quic.pem"))
	if err != nil {
		st.Close()
		return nil, err
	}
	svc := &control.Service{
		Store:        st,
		Domain:       domain,
		SplicePublic: strings.TrimSpace(spliceLine),
		Queue:        approve.New(),
		Live:         control.NewLive(),
		Events:       event.New(),
		Metrics:      control.NewMetrics(),
	}
	if err := printFirstPassword(svc, st, cfg.Stdout); err != nil {
		st.Close()
		return nil, err
	}
	s := &Server{
		cfg:         cfg,
		store:       st,
		svc:         svc,
		splice:      signer,
		fingerprint: fp,
		waiters:     map[string]*joinWaiter{},
	}
	svc.Grant = s.grant
	ctx, cancel := context.WithCancel(ctx)
	s.cancel = cancel
	if err := s.listen(ctx, hostKey, cert); err != nil {
		s.Close()
		return nil, err
	}
	return s, nil
}

func persistedDomain(st *store.Store, flag string) (string, error) {
	stored, ok, err := st.Meta("domain")
	if err != nil {
		return "", err
	}
	if ok && stored != "" {
		return stored, nil
	}
	if strings.TrimSpace(flag) == "" {
		return "", errors.New("domain is set when the server starts: box serve --domain <domain>")
	}
	domain, err := ident.Domain(flag)
	if err != nil {
		return "", err
	}
	if err := st.SetMeta("domain", domain); err != nil {
		return "", err
	}
	return domain, nil
}

func persistedAddrs(st *store.Store, sshAddr, httpAddr, quicAddr string) (string, string, string, error) {
	storedSSH, okS, err := st.Meta("ssh_addr")
	if err != nil {
		return "", "", "", err
	}
	storedHTTP, okH, err := st.Meta("http_addr")
	if err != nil {
		return "", "", "", err
	}
	storedQUIC, okQ, err := st.Meta("quic_addr")
	if err != nil {
		return "", "", "", err
	}
	if okS && okH && okQ && storedSSH != "" && storedHTTP != "" && storedQUIC != "" {
		return storedSSH, storedHTTP, storedQUIC, nil
	}
	if sshAddr == "" {
		sshAddr = ":22"
	}
	if httpAddr == "" {
		httpAddr = ":80"
	}
	if quicAddr == "" {
		quicAddr = ":7443"
	}
	if err := st.SetMeta("ssh_addr", sshAddr); err != nil {
		return "", "", "", err
	}
	if err := st.SetMeta("http_addr", httpAddr); err != nil {
		return "", "", "", err
	}
	if err := st.SetMeta("quic_addr", quicAddr); err != nil {
		return "", "", "", err
	}
	return sshAddr, httpAddr, quicAddr, nil
}

func printFirstPassword(svc *control.Service, st *store.Store, w io.Writer) error {
	inited, ok, err := st.Meta("initialized")
	if err != nil {
		return err
	}
	if ok && inited == "1" {
		return nil
	}
	p, err := svc.PairClient()
	if err != nil {
		return err
	}
	if _, err := fmt.Fprintf(w, "one-time password: %s\nexpires: %s\n", p.Secret, p.Expires.UTC().Format(time.RFC3339)); err != nil {
		return err
	}
	return st.SetMeta("initialized", "1")
}

func (s *Server) listen(ctx context.Context, hostKey string, cert tls.Certificate) error {
	httpLn, err := net.Listen("tcp", s.cfg.HTTPAddr)
	if err != nil {
		return err
	}
	s.httpLn = httpLn
	s.httpPort = configuredPort(s.cfg.HTTPAddr, httpLn.Addr().String())
	s.httpSrv = &http.Server{Handler: s, ErrorLog: log.New(io.Discard, "", 0), ReadHeaderTimeout: 10 * time.Second}
	go s.httpSrv.Serve(httpLn)

	quicSrv, err := tunnel.Listen(s.cfg.QUICAddr, cert, s.onHello)
	if err != nil {
		return err
	}
	s.quic = quicSrv
	s.quicPort = configuredPort(s.cfg.QUICAddr, quicSrv.Addr())
	go s.acceptTunnels(ctx)

	if err := os.MkdirAll(filepath.Dir(s.cfg.SocketPath), 0o700); err != nil {
		return err
	}
	_ = os.Remove(s.cfg.SocketPath)
	sock, err := net.Listen("unix", s.cfg.SocketPath)
	if err != nil {
		return err
	}
	if err := os.Chmod(s.cfg.SocketPath, 0o600); err != nil {
		sock.Close()
		return err
	}
	s.sockLn = sock
	go s.acceptSock(ctx, sock)

	sshSrv, err := newSSH(s, hostKey)
	if err != nil {
		return err
	}
	sshLn, err := net.Listen("tcp", s.cfg.SSHAddr)
	if err != nil {
		return err
	}
	s.sshLn = sshLn
	s.sshSrv = sshSrv
	go sshSrv.serve(sshLn)
	go func() {
		<-ctx.Done()
		s.Close()
	}()
	return nil
}

func (s *Server) acceptTunnels(ctx context.Context) {
	for {
		c, err := s.quic.Accept(ctx)
		if err != nil {
			// A refused hello fails that computer only. The listener stays up.
			if ctx.Err() != nil {
				return
			}
			continue
		}
		s.attach(c)
	}
}

func (s *Server) onHello(id tunnel.Identity) (tunnel.HelloResult, error) {
	ok, err := s.store.TokenMatches(id.Name, id.Token)
	if err != nil {
		return tunnel.HelloResult{}, errors.New("unauthorized")
	}
	if !ok {
		return tunnel.HelloResult{}, errors.New("unauthorized")
	}
	if id.User != "" || id.HostKey != "" {
		if err := s.store.SetComputerInfo(id.Name, id.User, id.HostKey); err != nil {
			return tunnel.HelloResult{}, errors.New("unauthorized")
		}
	}
	return tunnel.HelloResult{Domain: s.svc.Domain}, nil
}

func (s *Server) attach(c *tunnel.Conn) {
	old, ok := s.svc.Live.AttachIf(c.Name(), c, func() bool {
		_, err := s.store.Computer(c.Name())
		return err == nil
	})
	if !ok {
		_ = c.Close()
		return
	}
	if old != nil {
		_ = old.Close()
	}
	c.Handle(s.onControl(c))
	go func() {
		<-c.Done()
		s.svc.Live.Forget(c)
	}()
	go s.svc.PushAll(context.Background(), c)
}

func (s *Server) onControl(c *tunnel.Conn) tunnel.Handler {
	return func(op string, body json.RawMessage) (any, error) {
		name := s.svc.Live.CurrentName(c)
		switch op {
		case rpc.OpDomain:
			return rpc.DomainBody{Domain: s.svc.Domain}, nil
		case tunnel.OpPortalCheck:
			var req tunnel.PortalCheckRequest
			if err := json.Unmarshal(body, &req); err != nil {
				return nil, errors.New("bad request")
			}
			return s.svc.CheckPortal(req.Label)
		case tunnel.OpPortalAdd:
			var req tunnel.PortalAddRequest
			if err := json.Unmarshal(body, &req); err != nil {
				return nil, errors.New("bad request")
			}
			return s.svc.AddPortal(name, req.Label, req.Port, req.Private)
		case tunnel.OpPortalRm:
			var req tunnel.PortalRmRequest
			if err := json.Unmarshal(body, &req); err != nil {
				return nil, errors.New("bad request")
			}
			return nil, s.svc.RemovePortal(name, req.Label)
		case tunnel.OpPortalLs:
			return s.svc.ListPortals(name)
		case tunnel.OpEventPub:
			var req tunnel.EventPublish
			if err := json.Unmarshal(body, &req); err != nil {
				return nil, errors.New("bad request")
			}
			item, err := s.svc.PublishEvent(name, req.Topic, req.Body)
			if err != nil {
				return nil, err
			}
			return control.WireEvent(item), nil
		case tunnel.OpEventGet:
			var req tunnel.EventQuery
			if err := json.Unmarshal(body, &req); err != nil {
				return nil, errors.New("bad request")
			}
			list, err := s.svc.ReadEvents(req.Since, req.Topic)
			if err != nil {
				return nil, err
			}
			out := tunnel.EventList{Events: make([]tunnel.EventItem, 0, len(list))}
			for _, item := range list {
				out.Events = append(out.Events, control.WireEvent(item))
			}
			return out, nil
		default:
			return nil, errors.New("unsupported")
		}
	}
}

func (s *Server) quicEndpoint() string {
	return fmt.Sprintf("%s:%d", s.svc.Domain, s.quicPort)
}

// HTTPAddr is the HTTP port that was bound.
func (s *Server) HTTPAddr() string {
	if s.httpLn == nil {
		return ""
	}
	return s.httpLn.Addr().String()
}

// SSHAddr is the control SSH port.
func (s *Server) SSHAddr() string {
	if s.sshLn == nil {
		return ""
	}
	return s.sshLn.Addr().String()
}

// QUICAddr is the UDP port computers dial.
func (s *Server) QUICAddr() string {
	if s.quic == nil {
		return ""
	}
	return s.quic.Addr()
}

// QUICFingerprint is the pinned certificate fingerprint.
func (s *Server) QUICFingerprint() string { return s.fingerprint }

// Close stops listeners. It is safe to call more than once.
func (s *Server) Close() error {
	s.once.Do(func() {
		if s.cancel != nil {
			s.cancel()
		}
		if s.httpSrv != nil {
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			s.closeErr = s.httpSrv.Shutdown(ctx)
			cancel()
		}
		if s.sshSrv != nil {
			s.sshSrv.close()
		}
		if s.quic != nil {
			if err := s.quic.Close(); err != nil && s.closeErr == nil {
				s.closeErr = err
			}
		}
		if s.sshLn != nil {
			s.sshLn.Close()
		}
		if s.sockLn != nil {
			s.sockLn.Close()
		}
		if s.store != nil {
			if err := s.store.Close(); err != nil && s.closeErr == nil {
				s.closeErr = err
			}
		}
	})
	return s.closeErr
}

func (s *Server) proxyPortal(w http.ResponseWriter, r *http.Request, p store.Portal) {
	conn := s.svc.Live.Get(p.Computer)
	if conn == nil {
		s.svc.Metrics.Failed()
		http.Error(w, "computer offline\n", http.StatusBadGateway)
		return
	}
	host := p.Hostname
	s.stripGate(r)
	rp := &httputil.ReverseProxy{
		Director: func(req *http.Request) {
			req.URL.Scheme = "http"
			req.URL.Host = host
		},
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
				return conn.OpenPortal(ctx, p.Port)
			},
			DisableKeepAlives: true,
		},
		ErrorHandler: func(w http.ResponseWriter, _ *http.Request, _ error) {
			s.svc.Metrics.Failed()
			http.Error(w, "bad gateway\n", http.StatusBadGateway)
		},
		FlushInterval: -1,
	}
	rp.ServeHTTP(w, r)
}

func (s *Server) acceptSock(ctx context.Context, ln net.Listener) {
	for {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		go func() {
			defer conn.Close()
			_ = rpc.Serve(conn, s.local)
		}()
	}
}

func (s *Server) local(op string, body json.RawMessage) (any, error) {
	switch op {
	case "pair":
		p, err := s.svc.PairClient()
		if err != nil {
			return nil, err
		}
		return map[string]any{"secret": p.Secret, "expires": p.Expires, "kind": "client"}, nil
	case "key_ls":
		return s.svc.Keys()
	case "key_rm":
		var req struct {
			Match string `json:"match"`
		}
		if err := json.Unmarshal(body, &req); err != nil {
			return nil, err
		}
		return nil, s.svc.RemoveKey(req.Match)
	case "env_set":
		var req struct {
			Name  string `json:"name"`
			Value string `json:"value"`
		}
		if err := json.Unmarshal(body, &req); err != nil {
			return nil, err
		}
		msg, err := s.svc.SetEnv(req.Name, req.Value)
		if err != nil {
			return nil, err
		}
		return map[string]string{"message": msg}, nil
	case "env_rm":
		var req struct {
			Name string `json:"name"`
		}
		if err := json.Unmarshal(body, &req); err != nil {
			return nil, err
		}
		return nil, s.svc.DeleteEnv(req.Name)
	case "env_ls":
		names, err := s.svc.EnvNames()
		if err != nil {
			return nil, err
		}
		return map[string]any{"names": names}, nil
	case "ls":
		return s.svc.ListComputers()
	case "pending":
		return s.svc.Pending(), nil
	case "approve":
		var req struct {
			Code string `json:"code"`
		}
		if err := json.Unmarshal(body, &req); err != nil {
			return nil, err
		}
		msg, err := s.svc.Approve(req.Code, "local")
		if err != nil {
			return nil, err
		}
		return map[string]string{"message": msg}, nil
	case "rm":
		var req struct {
			Name string `json:"name"`
		}
		if err := json.Unmarshal(body, &req); err != nil {
			return nil, err
		}
		return nil, s.svc.Remove(req.Name)
	case "rename":
		var req struct {
			Old string `json:"old"`
			New string `json:"new"`
		}
		if err := json.Unmarshal(body, &req); err != nil {
			return nil, err
		}
		return nil, s.svc.Rename(req.Old, req.New)
	case "stat":
		var req struct {
			Name string `json:"name"`
		}
		if err := json.Unmarshal(body, &req); err != nil {
			return nil, err
		}
		return s.svc.Stat(context.Background(), req.Name)
	case "status":
		return s.svc.Status()
	case "summary":
		return s.svc.Summary(context.Background())
	case "snapshot":
		// Tokens are included so the TUI can copy them. Do not log this value.
		return s.svc.Snapshot()
	case "token_add":
		var req struct {
			Comment string `json:"comment"`
			For     string `json:"for"`
		}
		if err := json.Unmarshal(body, &req); err != nil && len(body) != 0 && string(body) != "null" {
			return nil, err
		}
		ttl, err := control.ParseTTL(req.For)
		if err != nil {
			return nil, err
		}
		return s.svc.AddToken(req.Comment, ttl)
	case "token_ls":
		return s.svc.Tokens()
	case "token_rm":
		var req struct {
			ID int64 `json:"id"`
		}
		if err := json.Unmarshal(body, &req); err != nil {
			return nil, err
		}
		return nil, s.svc.RemoveToken(req.ID)
	case "event_pub":
		var req struct {
			Topic string `json:"topic"`
			Body  string `json:"body"`
		}
		if err := json.Unmarshal(body, &req); err != nil {
			return nil, err
		}
		return s.svc.PublishEvent("server", req.Topic, req.Body)
	case "event_get":
		var req struct {
			Since int64  `json:"since"`
			Topic string `json:"topic"`
		}
		if len(body) != 0 && string(body) != "null" {
			if err := json.Unmarshal(body, &req); err != nil {
				return nil, err
			}
		}
		list, err := s.svc.ReadEvents(req.Since, req.Topic)
		if err != nil {
			return nil, err
		}
		return struct {
			Events []event.Item `json:"events"`
		}{Events: list}, nil
	default:
		return nil, errors.New("unknown command")
	}
}

func hostname(host string) string {
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	return strings.ToLower(host)
}

func configuredPort(addr, actual string) int {
	p := splitPort(addr)
	if p == 0 {
		p = splitPort(actual)
	}
	return p
}

func splitPort(addr string) int {
	_, port, err := net.SplitHostPort(addr)
	if err != nil {
		return 0
	}
	n, err := strconv.Atoi(port)
	if err != nil {
		return 0
	}
	return n
}
