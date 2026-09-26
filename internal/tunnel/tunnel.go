package tunnel

import (
	"context"
	"crypto/subtle"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"net"
	"strings"
	"sync"
	"time"

	"github.com/4fuu/box/internal/rpc"
	"github.com/quic-go/quic-go"
)

const appRefused quic.ApplicationErrorCode = 1

// Server accepts computers that dial in.
type Server struct {
	ln      *quic.Listener
	onHello func(Identity) (HelloResult, error)

	mu    sync.Mutex
	conns map[*Conn]struct{}
}

// Listen serves QUIC on addr. onHello returning an error refuses that computer.
// addr may be "127.0.0.1:0".
func Listen(addr string, cert tls.Certificate, onHello func(Identity) (HelloResult, error)) (*Server, error) {
	if onHello == nil {
		return nil, errors.New("nil hello handler")
	}
	ln, err := quic.ListenAddr(addr, serverTLS(cert), nil)
	if err != nil {
		return nil, err
	}
	return &Server{
		ln:      ln,
		onHello: onHello,
		conns:   map[*Conn]struct{}{},
	}, nil
}

func serverTLS(cert tls.Certificate) *tls.Config {
	return &tls.Config{
		Certificates: []tls.Certificate{cert},
		MinVersion:   tls.VersionTLS13,
		NextProtos:   []string{alpn},
	}
}

// Addr is the host:port actually bound.
func (s *Server) Addr() string { return s.ln.Addr().String() }

// Accept waits for one computer, completes hello, and returns its connection.
// The QUIC connection is closed when hello is refused.
func (s *Server) Accept(ctx context.Context) (*Conn, error) {
	qconn, err := s.ln.Accept(ctx)
	if err != nil {
		return nil, err
	}
	st, err := qconn.AcceptStream(ctx)
	if err != nil {
		_ = qconn.CloseWithError(appRefused, "no control stream")
		return nil, err
	}
	dec := json.NewDecoder(st)
	m, err := readMsg(ctx, dec, st)
	if err != nil {
		_ = qconn.CloseWithError(appRefused, "hello")
		return nil, err
	}
	id, result, herr := s.checkHello(m)
	if herr != nil {
		refuse(qconn, st, m, herr)
		return nil, herr
	}
	if err := ctx.Err(); err != nil {
		refuse(qconn, st, m, err)
		return nil, err
	}
	raw, err := json.Marshal(result)
	if err != nil {
		refuse(qconn, st, m, err)
		return nil, err
	}
	if err := rpc.Write(st, rpc.Message{Op: OpHello, ID: m.ID, OK: true, Body: raw}); err != nil {
		_ = qconn.CloseWithError(appRefused, "hello")
		return nil, err
	}
	ctrl := newControl(qconn, st, dec, "s")
	ctrl.start()
	go rejectClientStreams(qconn)
	c := &Conn{srv: s, ctrl: ctrl, id: id}
	s.track(c)
	return c, nil
}

func (s *Server) checkHello(m rpc.Message) (Identity, HelloResult, error) {
	if m.Op != OpHello {
		return Identity{}, HelloResult{}, errors.New("expected hello")
	}
	var body HelloBody
	if len(m.Body) == 0 || json.Unmarshal(m.Body, &body) != nil {
		return Identity{}, HelloResult{}, errors.New("expected hello")
	}
	if body.Version != ProtocolVersion {
		return Identity{}, HelloResult{}, errors.New("unsupported version")
	}
	id := Identity{
		Name:         body.Name,
		Token:        body.Token,
		User:         body.User,
		HostKey:      body.HostKey,
		AgentVersion: body.AgentVersion,
	}
	res, err := s.onHello(id)
	if err != nil {
		return Identity{}, HelloResult{}, err
	}
	return id, res, nil
}

// refuse writes the error and closes the QUIC connection once the peer can read it.
func refuse(qconn *quic.Conn, st *quic.Stream, m rpc.Message, herr error) {
	_ = rpc.Write(st, rpc.Message{Op: m.Op, ID: m.ID, OK: false, Error: herr.Error()})
	_ = st.Close()
	go func() {
		defer func() { _ = qconn.CloseWithError(appRefused, herr.Error()) }()
		_ = st.SetReadDeadline(time.Now().Add(5 * time.Second))
		var buf [1]byte
		_, _ = st.Read(buf[:])
	}()
}

func rejectClientStreams(qconn *quic.Conn) {
	for {
		st, err := qconn.AcceptStream(context.Background())
		if err != nil {
			return
		}
		st.CancelRead(0)
		st.CancelWrite(0)
	}
}

func (s *Server) track(c *Conn) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.conns == nil {
		s.conns = map[*Conn]struct{}{}
	}
	s.conns[c] = struct{}{}
}

func (s *Server) untrack(c *Conn) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.conns, c)
}

// Close stops the listener and the computers accepted on it.
func (s *Server) Close() error {
	s.mu.Lock()
	conns := s.conns
	s.conns = nil
	s.mu.Unlock()
	err := s.ln.Close()
	for c := range conns {
		_ = c.ctrl.Close()
	}
	return err
}

// Conn is one computer accepted by the server.
type Conn struct {
	srv  *Server
	ctrl *control
	id   Identity
}

func (c *Conn) Name() string       { return c.id.Name }
func (c *Conn) Identity() Identity { return c.id }

// Handle sets the function called for computer requests on the control stream.
func (c *Conn) Handle(h Handler) { c.ctrl.Handle(h) }

func (c *Conn) Call(ctx context.Context, op string, req, resp any) error {
	return c.ctrl.Call(ctx, op, req, resp)
}

func (c *Conn) OpenSSH(ctx context.Context) (net.Conn, error) {
	return c.open(ctx, KindSSH, 0)
}

func (c *Conn) OpenPortal(ctx context.Context, port int) (net.Conn, error) {
	return c.open(ctx, KindPortal, port)
}

func (c *Conn) open(ctx context.Context, kind string, port int) (net.Conn, error) {
	st, err := c.ctrl.qconn.OpenStreamSync(ctx)
	if err != nil {
		return nil, err
	}
	if err := writeHeader(st, kind, port); err != nil {
		st.CancelRead(0)
		st.CancelWrite(0)
		return nil, err
	}
	return newStreamConn(st, nil, c.ctrl.qconn.LocalAddr(), c.ctrl.qconn.RemoteAddr()), nil
}

func (c *Conn) Close() error {
	if c.srv != nil {
		c.srv.untrack(c)
	}
	return c.ctrl.Close()
}

// Session is a computer's QUIC connection to the server.
type Session struct {
	qconn *quic.Conn

	mu           sync.Mutex
	ctrl         *control
	handler      Handler
	handlerSet   bool
	helloStarted bool
}

// Dial checks the server leaf against fingerprint and returns before hello.
// A wrong pin fails the handshake.
func Dial(ctx context.Context, addr, fingerprint string) (*Session, error) {
	qconn, err := quic.DialAddr(ctx, addr, clientTLS(fingerprint), nil)
	if err != nil {
		return nil, err
	}
	return &Session{qconn: qconn}, nil
}

func clientTLS(fingerprint string) *tls.Config {
	want := strings.ToLower(strings.TrimSpace(fingerprint))
	return &tls.Config{
		MinVersion: tls.VersionTLS13,
		NextProtos: []string{alpn},
		// The certificate is self-signed. The pin is the only check.
		InsecureSkipVerify: true,
		VerifyPeerCertificate: func(rawCerts [][]byte, _ [][]*x509.Certificate) error {
			if len(rawCerts) == 0 {
				return errors.New("no peer certificate")
			}
			got := Fingerprint(rawCerts[0])
			if len(got) != len(want) || subtle.ConstantTimeCompare([]byte(got), []byte(want)) != 1 {
				return errors.New("certificate fingerprint mismatch")
			}
			return nil
		},
	}
}

// Hello opens the control stream, sends version 1, and waits for the server.
// A refused hello closes the QUIC connection.
func (s *Session) Hello(ctx context.Context, id Identity) (HelloResult, error) {
	return s.hello(ctx, id, ProtocolVersion)
}

func (s *Session) hello(ctx context.Context, id Identity, version int) (HelloResult, error) {
	s.mu.Lock()
	if s.helloStarted {
		s.mu.Unlock()
		return HelloResult{}, errors.New("hello already sent")
	}
	s.helloStarted = true
	s.mu.Unlock()

	st, err := s.qconn.OpenStreamSync(ctx)
	if err != nil {
		_ = s.qconn.CloseWithError(appRefused, "hello")
		return HelloResult{}, err
	}
	raw, err := json.Marshal(HelloBody{
		Version:      version,
		Name:         id.Name,
		Token:        id.Token,
		User:         id.User,
		HostKey:      id.HostKey,
		AgentVersion: id.AgentVersion,
	})
	if err != nil {
		_ = s.qconn.CloseWithError(appRefused, "hello")
		return HelloResult{}, err
	}
	if err := rpc.Write(st, rpc.Message{Op: OpHello, ID: "c0", Body: raw}); err != nil {
		_ = s.qconn.CloseWithError(appRefused, "hello")
		return HelloResult{}, err
	}
	dec := json.NewDecoder(st)
	m, err := readMsg(ctx, dec, st)
	if err != nil {
		_ = s.qconn.CloseWithError(appRefused, "hello")
		return HelloResult{}, helloFailure(s.qconn, err)
	}
	if !m.OK {
		msg := m.Error
		if msg == "" {
			msg = "refused"
		}
		_ = s.qconn.CloseWithError(appRefused, msg)
		return HelloResult{}, errors.New(msg)
	}
	var res HelloResult
	if len(m.Body) > 0 && string(m.Body) != "null" {
		if err := json.Unmarshal(m.Body, &res); err != nil {
			_ = s.qconn.CloseWithError(appRefused, "hello")
			return HelloResult{}, err
		}
	}
	ctrl := newControl(s.qconn, st, dec, "c")
	s.bind(ctrl)
	return res, nil
}

func helloFailure(qconn *quic.Conn, err error) error {
	var app *quic.ApplicationError
	if errors.As(err, &app) && app.ErrorMessage != "" {
		return errors.New(app.ErrorMessage)
	}
	if cause := context.Cause(qconn.Context()); cause != nil && !errors.Is(cause, err) {
		if errors.As(cause, &app) && app.ErrorMessage != "" {
			return errors.New(app.ErrorMessage)
		}
	}
	return err
}

func (s *Session) bind(ctrl *control) {
	s.mu.Lock()
	s.ctrl = ctrl
	if s.handlerSet {
		ctrl.Handle(s.handler)
	}
	s.mu.Unlock()
	ctrl.start()
}

// Handle sets the function called for server requests on the control stream.
func (s *Session) Handle(h Handler) {
	s.mu.Lock()
	s.handler = h
	s.handlerSet = true
	ctrl := s.ctrl
	s.mu.Unlock()
	if ctrl != nil {
		ctrl.Handle(h)
	}
}

func (s *Session) Call(ctx context.Context, op string, req, resp any) error {
	s.mu.Lock()
	ctrl := s.ctrl
	s.mu.Unlock()
	if ctrl == nil {
		return errNotReady
	}
	return ctrl.Call(ctx, op, req, resp)
}

// Accept returns the next server-opened stream after its header is consumed.
// kind is ssh (port 0) or portal. A bad header closes that stream.
func (s *Session) Accept(ctx context.Context) (string, int, net.Conn, error) {
	st, err := s.qconn.AcceptStream(ctx)
	if err != nil {
		return "", 0, nil, err
	}
	kind, port, r, err := acceptHeader(ctx, st)
	if err != nil {
		return "", 0, nil, err
	}
	return kind, port, newStreamConn(st, r, s.qconn.LocalAddr(), s.qconn.RemoteAddr()), nil
}

func (s *Session) Close() error {
	s.mu.Lock()
	ctrl := s.ctrl
	s.mu.Unlock()
	if ctrl != nil {
		return ctrl.Close()
	}
	return s.qconn.CloseWithError(0, "closed")
}
