// Package control is the server-side behavior shared by the REPL and the localhost CLI.
package control

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/4fuu/box/internal/approve"
	"github.com/4fuu/box/internal/ident"
	"github.com/4fuu/box/internal/keys"
	"github.com/4fuu/box/internal/secret"
	"github.com/4fuu/box/internal/store"
	"github.com/4fuu/box/internal/tunnel"
	"golang.org/x/crypto/ssh"
)

const PairingTTL = 10 * time.Minute

// Service is the control plane.
type Service struct {
	Store        *store.Store
	Domain       string
	SplicePublic string
	Queue        *approve.Queue
	Live         *Live
	// Grant finishes an approved join. The server parks the SSH session and
	// sends the token there. Approve does not hold the queue lock across Grant.
	Grant      func(approve.Pending) error
	HTTPPort   func() int
	PairingTTL time.Duration
}

func (s *Service) ttl() time.Duration {
	if s.PairingTTL == 0 {
		return PairingTTL
	}
	return s.PairingTTL
}

func (s *Service) httpPort() int {
	if s.HTTPPort == nil {
		return 80
	}
	p := s.HTTPPort()
	if p <= 0 {
		return 80
	}
	return p
}

type Pairing struct {
	Secret  string
	Expires time.Time
}

func (s *Service) PairClient() (Pairing, error) {
	raw, err := secret.ClientPassword()
	if err != nil {
		return Pairing{}, err
	}
	exp, err := s.Store.NewPairing(raw, s.ttl())
	if err != nil {
		return Pairing{}, err
	}
	return Pairing{Secret: raw, Expires: exp}, nil
}

// ConsumeClient checks a one-time password. The password is not retained.
func (s *Service) ConsumeClient(raw string) error {
	raw = secret.NormalizePassword(raw)
	err := s.Store.ConsumePairing(raw)
	if err == nil {
		return nil
	}
	if errors.Is(err, store.ErrExpired) {
		return errors.New("password expired")
	}
	return errors.New("invalid password")
}

// Bind stores a client public key. A second bind of the same key is a no-op.
func (s *Service) Bind(pub ssh.PublicKey, comment string) error {
	fp := keys.Fingerprint(pub)
	if _, err := s.Store.FindKeyByFingerprint(fp); err == nil {
		return nil
	}
	line := keys.Line(pub, comment)
	return s.Store.BindKey(line, comment, fp)
}

func (s *Service) HasKey(pub ssh.PublicKey) (bool, error) {
	if pub == nil {
		return false, nil
	}
	fp := keys.Fingerprint(pub)
	_, err := s.Store.FindKeyByFingerprint(fp)
	if errors.Is(err, store.ErrNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}

type KeyView struct {
	Fingerprint string    `json:"fingerprint"`
	Comment     string    `json:"comment"`
	BoundAt     time.Time `json:"bound_at"`
}

func (s *Service) Keys() ([]KeyView, error) {
	list, err := s.Store.ListKeys()
	if err != nil {
		return nil, err
	}
	out := make([]KeyView, 0, len(list))
	for _, k := range list {
		out = append(out, KeyView{Fingerprint: k.Fingerprint, Comment: k.Comment, BoundAt: k.BoundAt})
	}
	return out, nil
}

func (s *Service) RemoveKey(match string) error {
	k, err := s.Store.FindKeyByFingerprint(strings.TrimSpace(match))
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return errors.New("unknown key")
		}
		return err
	}
	if err := s.Store.RemoveKey(k.ID); err != nil {
		return err
	}
	s.PushKeys(context.Background())
	return nil
}

// AuthorizedLines is every bound client key plus the splice public key.
// Lines are raw OpenSSH public keys, with no environment= option.
func (s *Service) AuthorizedLines() ([]string, error) {
	list, err := s.Store.ListKeys()
	if err != nil {
		return nil, err
	}
	var out []string
	for _, k := range list {
		line := strings.TrimSpace(k.Public)
		if line == "" {
			continue
		}
		out = append(out, line)
	}
	if sp := strings.TrimSpace(s.SplicePublic); sp != "" {
		out = append(out, sp)
	}
	return out, nil
}

func (s *Service) PushKeys(ctx context.Context) {
	lines, err := s.AuthorizedLines()
	if err != nil || s.Live == nil {
		return
	}
	req := tunnel.KeysRequest{AuthorizedKeys: lines}
	for _, c := range s.Live.All() {
		_ = call(ctx, c, tunnel.OpKeys, req, nil)
	}
}

func (s *Service) PushEnv(ctx context.Context) {
	if s.Live == nil {
		return
	}
	vars, err := s.Store.EnvAll()
	if err != nil {
		return
	}
	req := tunnel.EnvRequest{Vars: vars}
	for _, c := range s.Live.All() {
		_ = call(ctx, c, tunnel.OpEnv, req, nil)
	}
}

// PushAll sends keys and env to one computer that just said hello.
func (s *Service) PushAll(ctx context.Context, c *tunnel.Conn) {
	if c == nil {
		return
	}
	lines, err := s.AuthorizedLines()
	if err == nil {
		_ = call(ctx, c, tunnel.OpKeys, tunnel.KeysRequest{AuthorizedKeys: lines}, nil)
	}
	vars, err := s.Store.EnvAll()
	if err != nil {
		return
	}
	_ = call(ctx, c, tunnel.OpEnv, tunnel.EnvRequest{Vars: vars}, nil)
}

func call(ctx context.Context, c *tunnel.Conn, op string, req, resp any) error {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	return c.Call(ctx, op, req, resp)
}

func (s *Service) WhoAmI(pub ssh.PublicKey) (string, error) {
	if pub == nil {
		return "", errors.New("no key")
	}
	fp := keys.Fingerprint(pub)
	k, err := s.Store.FindKeyByFingerprint(fp)
	if err != nil {
		return fp, nil
	}
	if k.Comment == "" {
		return fp, nil
	}
	return fp + " " + k.Comment, nil
}

func (s *Service) SetEnv(name, value string) (string, error) {
	if err := ident.Env(name); err != nil {
		return "", err
	}
	if err := s.Store.SetEnv(name, value); err != nil {
		return "", err
	}
	s.PushEnv(context.Background())
	return name + " set. existing sessions keep the old value", nil
}

func (s *Service) EnvNames() ([]string, error) { return s.Store.EnvNames() }

func (s *Service) DeleteEnv(name string) error {
	if err := s.Store.DeleteEnv(name); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return fmt.Errorf("env %s is not set", name)
		}
		return err
	}
	s.PushEnv(context.Background())
	return nil
}

type Status struct {
	Domain    string `json:"domain"`
	Keys      int    `json:"keys"`
	Computers int    `json:"computers"`
}

func (s *Service) Status() (Status, error) {
	st := Status{Domain: s.Domain}
	keys, err := s.Store.ListKeys()
	if err != nil {
		return st, err
	}
	st.Keys = len(keys)
	comps, err := s.Store.ListComputers()
	if err != nil {
		return st, err
	}
	st.Computers = len(comps)
	return st, nil
}

func (s *Service) IsComputer(name string) bool {
	if name == "" || name == "box" || name == "pair" || name == "join" {
		return false
	}
	if strings.HasPrefix(name, "pair+") || strings.HasPrefix(name, "join+") {
		return false
	}
	_, err := s.Store.Computer(name)
	return err == nil
}

type ComputerView struct {
	Name         string   `json:"name"`
	Online       bool     `json:"online"`
	User         string   `json:"user"`
	Address      string   `json:"address"`
	AgentVersion string   `json:"agent_version"`
	Portals      []string `json:"portals"`
}

func (s *Service) ListComputers() ([]ComputerView, error) {
	list, err := s.Store.ListComputers()
	if err != nil {
		return nil, err
	}
	out := make([]ComputerView, 0, len(list))
	for _, c := range list {
		ports, err := s.Store.PortalsByComputer(c.Name)
		if err != nil {
			return nil, err
		}
		labels := make([]string, 0, len(ports))
		for _, p := range ports {
			labels = append(labels, portalLabel(p.Hostname, s.Domain))
		}
		view := ComputerView{
			Name: c.Name, User: c.LoginUser, Portals: labels,
		}
		if conn := s.liveGet(c.Name); conn != nil {
			view.Online = true
			view.AgentVersion = conn.Identity().AgentVersion
			if addr := conn.RemoteAddr(); addr != nil {
				view.Address = addr.String()
			}
		}
		out = append(out, view)
	}
	return out, nil
}

func (s *Service) liveGet(name string) *tunnel.Conn {
	if s.Live == nil {
		return nil
	}
	return s.Live.Get(name)
}

// Remove deletes the computer, revokes its token, and closes a live tunnel.
func (s *Service) Remove(name string) error {
	var conn *tunnel.Conn
	err := s.withLive(func() error {
		if err := s.Store.DeleteComputer(name); err != nil {
			if errors.Is(err, store.ErrNotFound) {
				return fmt.Errorf("computer %s not found", name)
			}
			return err
		}
		if s.Live != nil {
			conn = s.Live.dropLocked(name)
		}
		return nil
	})
	if conn != nil {
		_ = conn.Close()
	}
	return err
}

func (s *Service) Rename(oldName, newName string) error {
	if err := ident.Computer(newName); err != nil {
		return err
	}
	return s.withLive(func() error {
		if _, err := s.Store.Computer(oldName); err != nil {
			if errors.Is(err, store.ErrNotFound) {
				return fmt.Errorf("computer %s not found", oldName)
			}
			return err
		}
		if _, err := s.Store.Computer(newName); err == nil {
			return fmt.Errorf("%s already exists", newName)
		} else if !errors.Is(err, store.ErrNotFound) {
			return err
		}
		if err := s.Store.RenameComputer(oldName, newName); err != nil {
			if errors.Is(err, store.ErrExists) {
				return fmt.Errorf("%s already exists", newName)
			}
			if errors.Is(err, store.ErrNotFound) {
				return fmt.Errorf("computer %s not found", oldName)
			}
			return err
		}
		if s.Live != nil {
			s.Live.renameLocked(oldName, newName)
		}
		return nil
	})
}

func (s *Service) withLive(fn func() error) error {
	if s.Live == nil {
		return fn()
	}
	return s.Live.Do(fn)
}

// Stat asks the live agent. An offline computer is an error.
func (s *Service) Stat(ctx context.Context, name string) (tunnel.StatResponse, error) {
	if _, err := s.needComputer(name); err != nil {
		return tunnel.StatResponse{}, err
	}
	conn := s.liveGet(name)
	if conn == nil {
		return tunnel.StatResponse{}, fmt.Errorf("%s is offline", name)
	}
	var out tunnel.StatResponse
	if err := call(ctx, conn, tunnel.OpStat, tunnel.StatRequest{}, &out); err != nil {
		return tunnel.StatResponse{}, err
	}
	return out, nil
}

type PendingView struct {
	Name      string    `json:"name"`
	Address   string    `json:"address"`
	User      string    `json:"user"`
	ExpiresAt time.Time `json:"expires_at"`
}

func (s *Service) Pending() []PendingView {
	if s.Queue == nil {
		return nil
	}
	list := s.Queue.List()
	out := make([]PendingView, 0, len(list))
	for _, p := range list {
		out = append(out, PendingView{Name: p.Name, Address: p.Addr, User: p.User, ExpiresAt: p.ExpiresAt})
	}
	return out
}

// Approve checks the code and completes the parked join.
// Queue errors are returned as themselves.
func (s *Service) Approve(code, fromAddr string) (string, error) {
	if s.Queue == nil {
		return "", approve.ErrNotFound
	}
	p, err := s.Queue.Approve(strings.TrimSpace(code), fromAddr)
	if err != nil {
		return "", err
	}
	if s.Grant == nil {
		return "", errors.New("no matching join")
	}
	if err := s.Grant(p); err != nil {
		return "", err
	}
	return "approved " + p.Name, nil
}

func (s *Service) CheckPortal(label string) (tunnel.PortalCheckResponse, error) {
	if err := ident.Label(label); err != nil {
		return tunnel.PortalCheckResponse{}, err
	}
	p, err := s.Store.PortalByHost(ident.Hostname(label, s.Domain))
	if errors.Is(err, store.ErrNotFound) {
		return tunnel.PortalCheckResponse{Free: true}, nil
	}
	if err != nil {
		return tunnel.PortalCheckResponse{}, err
	}
	return tunnel.PortalCheckResponse{Free: false, Holder: p.Computer}, nil
}

func (s *Service) AddPortal(computer, label string, port int) (tunnel.PortalAddResponse, error) {
	if err := ident.Label(label); err != nil {
		return tunnel.PortalAddResponse{}, err
	}
	if port < 1 || port > 65535 {
		return tunnel.PortalAddResponse{}, errors.New("invalid port")
	}
	if _, err := s.needComputer(computer); err != nil {
		return tunnel.PortalAddResponse{}, err
	}
	host := ident.Hostname(label, s.Domain)
	err := s.Store.ClaimPortal(host, computer, port)
	var held *store.HeldError
	if errors.As(err, &held) {
		return tunnel.PortalAddResponse{}, fmt.Errorf("%s is held by %s", label, held.Holder)
	}
	if err != nil {
		return tunnel.PortalAddResponse{}, err
	}
	return tunnel.PortalAddResponse{URL: s.portalURL(host), Host: host, Port: port}, nil
}

func (s *Service) RemovePortal(computer, label string) error {
	if err := ident.Label(label); err != nil {
		return err
	}
	host := ident.Hostname(label, s.Domain)
	if err := s.Store.ReleasePortal(host, computer); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return fmt.Errorf("%s is not claimed", label)
		}
		return err
	}
	return nil
}

func (s *Service) ListPortals(computer string) (tunnel.PortalList, error) {
	if _, err := s.needComputer(computer); err != nil {
		return tunnel.PortalList{}, err
	}
	list, err := s.Store.PortalsByComputer(computer)
	if err != nil {
		return tunnel.PortalList{}, err
	}
	out := tunnel.PortalList{Portals: make([]tunnel.Portal, 0, len(list))}
	for _, p := range list {
		out.Portals = append(out.Portals, tunnel.Portal{
			Label: portalLabel(p.Hostname, s.Domain),
			Port:  p.Port,
		})
	}
	return out, nil
}

func (s *Service) portalURL(host string) string {
	p := s.httpPort()
	if p == 80 {
		return "http://" + host
	}
	return fmt.Sprintf("http://%s:%d", host, p)
}

func (s *Service) needComputer(name string) (store.Computer, error) {
	c, err := s.Store.Computer(name)
	if errors.Is(err, store.ErrNotFound) {
		return store.Computer{}, fmt.Errorf("computer %s not found", name)
	}
	return c, err
}

func portalLabel(host, domain string) string {
	suf := "." + domain
	if domain != "" && strings.HasSuffix(host, suf) {
		return strings.TrimSuffix(host, suf)
	}
	return host
}
