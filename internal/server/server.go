// Package server is `box serve`: SSH entry, HTTP routing, SQLite, pairing, and env.
package server

import (
	"context"
	"crypto/subtle"
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
	"strings"
	"sync"
	"time"

	"github.com/4fuu/box/internal/api"
	"github.com/4fuu/box/internal/control"
	"github.com/4fuu/box/internal/frp"
	"github.com/4fuu/box/internal/ident"
	"github.com/4fuu/box/internal/keys"
	"github.com/4fuu/box/internal/paths"
	"github.com/4fuu/box/internal/rpc"
	"github.com/4fuu/box/internal/store"
	"golang.org/x/crypto/ssh"
)

// Config is how the server process is started.
// Domain is required the first time. Later starts reuse the stored domain.
type Config struct {
	Domain     string
	DataDir    string
	SSHAddr    string
	HTTPAddr   string
	SocketPath string
	FRPVhost   string
	Stdout     io.Writer
	// Dial reaches a node proxy. "rpc" is the controller. "ssh/<name>" is that computer's sshd.
	Dial func(ctx context.Context, nodeID, proxy string) (net.Conn, error)
	// Backend, when set, is used instead of Dial for the container SSH handshake.
	Backend func(ctx context.Context, computer string) (net.Conn, ssh.PublicKey, error)
}

// Server is a running box serve.
type Server struct {
	cfg      Config
	store    *store.Store
	svc      *control.Service
	httpLn   net.Listener
	sshLn    net.Listener
	sockLn   net.Listener
	httpSrv  *http.Server
	sshSrv   *sshServer
	github   ssh.Signer
	cancel   context.CancelFunc
	once     sync.Once
	closeErr error
}

// Start opens the database, prints a one-time password on first init, and listens.
func Start(ctx context.Context, cfg Config) (*Server, error) {
	if cfg.DataDir == "" {
		cfg.DataDir = paths.DataDir
	}
	if cfg.SSHAddr == "" {
		cfg.SSHAddr = paths.SSHListen
	}
	if cfg.HTTPAddr == "" {
		cfg.HTTPAddr = paths.HTTPListen
	}
	if cfg.SocketPath == "" {
		cfg.SocketPath = paths.ServerSocket
	}
	if cfg.FRPVhost == "" {
		cfg.FRPVhost = fmt.Sprintf("127.0.0.1:%d", paths.FRPVHostPort)
	}
	if cfg.Stdout == nil {
		cfg.Stdout = os.Stdout
	}
	dbPath := filepath.Join(cfg.DataDir, "box.db")
	if cfg.Domain == "" {
		if _, statErr := os.Stat(dbPath); errors.Is(statErr, os.ErrNotExist) {
			return nil, errors.New("domain is set when the server starts: box serve --domain <domain>")
		}
	}
	if err := os.MkdirAll(cfg.DataDir, 0o700); err != nil {
		return nil, err
	}
	st, err := store.Open(dbPath)
	if err != nil {
		return nil, err
	}
	domain := cfg.Domain
	if domain != "" {
		domain, err = ident.Domain(domain)
		if err != nil {
			st.Close()
			return nil, err
		}
		if err := st.SetMeta("domain", domain); err != nil {
			st.Close()
			return nil, err
		}
	} else {
		stored, ok, err := st.Meta("domain")
		if err != nil {
			st.Close()
			return nil, err
		}
		if !ok || stored == "" {
			st.Close()
			return nil, errors.New("domain is set when the server starts: box serve --domain <domain>")
		}
		domain = stored
	}
	ghPath := filepath.Join(cfg.DataDir, "github_ed25519")
	ghLine, err := keys.Generate(ghPath, "box")
	if err != nil {
		st.Close()
		return nil, err
	}
	signer, err := keys.Signer(ghPath)
	if err != nil {
		st.Close()
		return nil, err
	}
	hostKey := filepath.Join(cfg.DataDir, "ssh_host_ed25519_key")
	if _, err := keys.Generate(hostKey, "box-host"); err != nil {
		st.Close()
		return nil, err
	}
	svc := &control.Service{Store: st, Domain: domain, GitHubPublic: ghLine}
	tok, err := svc.EnsureFRPToken()
	if err != nil {
		st.Close()
		return nil, err
	}
	if err := os.WriteFile(filepath.Join(cfg.DataDir, "frps.toml"), []byte(frp.ServerTOML(paths.FRPPort, paths.FRPVHostPort, tok)), 0o600); err != nil {
		st.Close()
		return nil, err
	}
	if err := printFirstPassword(svc, st, cfg.Stdout); err != nil {
		st.Close()
		return nil, err
	}
	s := &Server{cfg: cfg, store: st, svc: svc, github: signer}
	s.svc.Nodes = dialClient{dial: cfg.Dial}
	ctx, cancel := context.WithCancel(ctx)
	s.cancel = cancel
	if err := s.listen(ctx, hostKey); err != nil {
		s.Close()
		return nil, err
	}
	return s, nil
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

func (s *Server) listen(ctx context.Context, hostKey string) error {
	httpLn, err := net.Listen("tcp", s.cfg.HTTPAddr)
	if err != nil {
		return err
	}
	s.httpLn = httpLn
	s.httpSrv = &http.Server{Handler: s, ErrorLog: log.New(io.Discard, "", 0), ReadHeaderTimeout: 10 * time.Second}
	go s.httpSrv.Serve(httpLn)

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

// HTTPAddr is the fixed HTTP port.
func (s *Server) HTTPAddr() string {
	if s.httpLn == nil {
		return ""
	}
	return s.httpLn.Addr().String()
}

// SSHAddr is the control SSH port.
func (s *Server) SSHAddr() string { return s.sshLn.Addr().String() }

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

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	host := hostname(r.Host)
	if p, err := s.store.PortalByHost(host); err == nil {
		s.proxyPortal(w, r, p, host)
		return
	}
	if strings.HasPrefix(r.URL.Path, api.Prefix) && apiHost(host, s.svc.Domain) {
		s.nodeAPI(w, r)
		return
	}
	w.WriteHeader(http.StatusMisdirectedRequest)
	_, _ = io.WriteString(w, "unknown host\n")
}

func apiHost(host, domain string) bool {
	return host == domain || host == "localhost" || host == "127.0.0.1"
}

func (s *Server) proxyPortal(w http.ResponseWriter, r *http.Request, p store.Portal, host string) {
	n, err := s.store.NodeByID(p.NodeID)
	if err != nil || !s.svc.Online(n) {
		http.Error(w, "node offline\n", http.StatusBadGateway)
		return
	}
	target := s.cfg.FRPVhost
	rp := &httputil.ReverseProxy{
		Director: func(req *http.Request) {
			req.URL.Scheme = "http"
			req.URL.Host = target
			req.Host = host
		},
		ErrorHandler: func(w http.ResponseWriter, _ *http.Request, _ error) {
			http.Error(w, "bad gateway\n", http.StatusBadGateway)
		},
		FlushInterval: -1,
	}
	rp.ServeHTTP(w, r)
}

func (s *Server) nodeAPI(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method\n", http.StatusMethodNotAllowed)
		return
	}
	path := strings.TrimPrefix(r.URL.Path, api.Prefix)
	if path == "/join" {
		s.join(w, r)
		return
	}
	n, ok := s.authNode(r)
	if !ok {
		http.Error(w, "unauthorized\n", http.StatusUnauthorized)
		return
	}
	switch path {
	case "/heartbeat":
		s.heartbeat(w, r, n)
	case "/domain":
		writeJSON(w, api.DomainResponse{Domain: s.svc.Domain})
	case "/claim":
		s.claim(w, r, n)
	case "/check":
		s.check(w, r)
	case "/release":
		s.release(w, r, n)
	default:
		http.NotFound(w, r)
	}
}

func (s *Server) authNode(r *http.Request) (store.Node, bool) {
	h := r.Header.Get("Authorization")
	const prefix = "Bearer "
	if !strings.HasPrefix(h, prefix) {
		return store.Node{}, false
	}
	token := strings.TrimPrefix(h, prefix)
	n, err := s.store.NodeByToken(token)
	if err != nil {
		return store.Node{}, false
	}
	// Compare the stored token so a hash match is not the only check.
	stored, err := s.store.NodeToken(n.ID)
	if err != nil || subtle.ConstantTimeCompare([]byte(stored), []byte(token)) != 1 {
		return store.Node{}, false
	}
	return n, true
}

func (s *Server) join(w http.ResponseWriter, r *http.Request) {
	var req api.JoinRequest
	if err := readJSON(r, &req); err != nil {
		http.Error(w, "invalid code\n", http.StatusBadRequest)
		return
	}
	id, token, frpToken, err := s.svc.JoinNode(req.Name, req.Code)
	if err != nil {
		http.Error(w, err.Error()+"\n", http.StatusUnauthorized)
		return
	}
	writeJSON(w, api.JoinResponse{ID: id, Token: token, FRPToken: frpToken})
}

func (s *Server) heartbeat(w http.ResponseWriter, r *http.Request, n store.Node) {
	var req api.HeartbeatRequest
	if err := readJSON(r, &req); err != nil {
		http.Error(w, "bad request\n", http.StatusBadRequest)
		return
	}
	comps := make([]store.ReportedComputer, 0, len(req.Computers))
	for _, c := range req.Computers {
		comps = append(comps, store.ReportedComputer{Name: c.Name, State: c.State})
	}
	if err := s.store.Heartbeat(n.ID, store.Heartbeat{
		CPU: req.CPU, Memory: req.Memory, Disk: req.Disk,
		UsedCPU: req.UsedCPU, UsedMemory: req.UsedMemory, UsedDisk: req.UsedDisk,
		Images: req.Images, Computers: comps,
	}); err != nil {
		http.Error(w, "bad request\n", http.StatusBadRequest)
		return
	}
	lines, err := s.svc.AuthorizedLines()
	if err != nil {
		http.Error(w, "bad request\n", http.StatusBadRequest)
		return
	}
	ports, err := s.svc.PortalsForNode(n.ID)
	if err != nil {
		http.Error(w, "bad request\n", http.StatusBadRequest)
		return
	}
	out := api.HeartbeatResponse{Domain: s.svc.Domain, Keys: lines}
	for _, p := range ports {
		out.Portals = append(out.Portals, api.PortalReport{Label: p.Label, Host: p.Host, Port: p.Port, Container: s.containerOf(p.Host)})
	}
	writeJSON(w, out)
}

func (s *Server) containerOf(host string) string {
	p, err := s.store.PortalByHost(host)
	if err != nil {
		return ""
	}
	return p.Container
}

func (s *Server) claim(w http.ResponseWriter, r *http.Request, n store.Node) {
	var req api.ClaimRequest
	if err := readJSON(r, &req); err != nil {
		http.Error(w, "bad request\n", http.StatusBadRequest)
		return
	}
	c, err := s.store.Computer(req.Container)
	if err != nil || c.NodeID != n.ID {
		http.Error(w, "unknown computer\n", http.StatusForbidden)
		return
	}
	url, err := s.svc.ClaimPortal(req.Container, req.Label, req.Port)
	if err != nil {
		http.Error(w, err.Error()+"\n", http.StatusConflict)
		return
	}
	writeJSON(w, api.ClaimResponse{URL: url, Host: ident.Hostname(req.Label, s.svc.Domain)})
}

func (s *Server) check(w http.ResponseWriter, r *http.Request) {
	var req api.CheckRequest
	if err := readJSON(r, &req); err != nil {
		http.Error(w, "bad request\n", http.StatusBadRequest)
		return
	}
	free, holder, err := s.svc.CheckPortal(req.Label)
	if err != nil {
		http.Error(w, err.Error()+"\n", http.StatusBadRequest)
		return
	}
	writeJSON(w, api.CheckResponse{Free: free, Holder: holder})
}

func (s *Server) release(w http.ResponseWriter, r *http.Request, n store.Node) {
	var req api.ReleaseRequest
	if err := readJSON(r, &req); err != nil {
		http.Error(w, "bad request\n", http.StatusBadRequest)
		return
	}
	c, err := s.store.Computer(req.Container)
	if err != nil || c.NodeID != n.ID {
		http.Error(w, "unknown computer\n", http.StatusForbidden)
		return
	}
	if err := s.svc.RemovePortal(req.Container, req.Label); err != nil {
		http.Error(w, err.Error()+"\n", http.StatusNotFound)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func readJSON(r *http.Request, dest any) error {
	defer r.Body.Close()
	dec := json.NewDecoder(io.LimitReader(r.Body, 1<<20))
	return dec.Decode(dest)
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	enc := json.NewEncoder(w)
	_ = enc.Encode(v)
}

func hostname(host string) string {
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	return strings.ToLower(host)
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
	case "node_pair":
		p, err := s.svc.PairNode()
		if err != nil {
			return nil, err
		}
		return map[string]any{"secret": p.Secret, "expires": p.Expires, "kind": "node"}, nil
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
	case "key_copy":
		line, err := s.svc.KeyCopy()
		if err != nil {
			return nil, err
		}
		return map[string]string{"public": line}, nil
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
	case "status":
		return s.svc.Status()
	default:
		return nil, errors.New("unknown command")
	}
}
