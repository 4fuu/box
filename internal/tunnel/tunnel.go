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
	"sync/atomic"
	"time"

	"github.com/4fuu/box/internal/rpc"
	"github.com/quic-go/quic-go"
)

const appRefused quic.ApplicationErrorCode = 1

// quicConfig keeps an idle computer up. quic-go otherwise uses a 30s idle
// timeout and sends no keep-alive, so a quiet tunnel is closed. The effective
// idle timeout is the minimum of both peers, so Listen and Dial share this.
// Keep-alives are under half the idle timeout; quic-go clamps them to that.
func quicConfig() *quic.Config {
	return &quic.Config{
		MaxIdleTimeout:  2 * time.Minute,
		KeepAlivePeriod: 10 * time.Second,
	}
}

// Server accepts computers that dial in.
type Server struct {
	ln      *quic.Listener
	onHello func(Identity) (HelloResult, error)
	base    context.Context
	cancel  context.CancelFunc

	mu     sync.Mutex
	closed bool
	conns  map[*Conn]struct{}
	early  map[*quic.Conn]struct{} // accepted, hello not finished
}

// Listen serves QUIC on addr. onHello returning an error refuses that computer.
// addr may be "127.0.0.1:0".
func Listen(addr string, cert tls.Certificate, onHello func(Identity) (HelloResult, error)) (*Server, error) {
	if onHello == nil {
		return nil, errors.New("nil hello handler")
	}
	ln, err := quic.ListenAddr(addr, serverTLS(cert), quicConfig())
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithCancel(context.Background())
	return &Server{
		ln:      ln,
		onHello: onHello,
		base:    ctx,
		cancel:  cancel,
		conns:   map[*Conn]struct{}{},
		early:   map[*quic.Conn]struct{}{},
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
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	if stop := context.AfterFunc(s.base, cancel); stop != nil {
		defer stop()
	}

	qconn, err := s.ln.Accept(ctx)
	if err != nil {
		return nil, err
	}
	if err := s.begin(qconn); err != nil {
		_ = qconn.CloseWithError(0, "closed")
		return nil, err
	}
	releaseEarly := true
	defer func() {
		if releaseEarly {
			s.end(qconn)
		}
	}()

	st, err := qconn.AcceptStream(ctx)
	if err != nil {
		_ = qconn.CloseWithError(appRefused, "no control stream")
		return nil, err
	}
	dec := json.NewDecoder(rpc.FrameLimit(st, rpc.MaxFrame))
	m, err := readMsg(ctx, dec, st)
	if err != nil {
		_ = qconn.CloseWithError(appRefused, "hello")
		return nil, err
	}
	if err := s.blocked(ctx); err != nil {
		releaseEarly = false
		s.refuse(qconn, st, m, err)
		return nil, err
	}
	id, result, herr := s.checkHello(m)
	if herr != nil {
		releaseEarly = false
		s.refuse(qconn, st, m, herr)
		return nil, herr
	}
	// Close during onHello must not publish a live Conn.
	if err := s.blocked(ctx); err != nil {
		releaseEarly = false
		s.refuse(qconn, st, m, err)
		return nil, err
	}
	raw, err := json.Marshal(result)
	if err != nil {
		releaseEarly = false
		s.refuse(qconn, st, m, err)
		return nil, err
	}
	if err := rpc.Write(st, rpc.Message{Op: OpHello, ID: m.ID, OK: true, Body: raw}); err != nil {
		_ = qconn.CloseWithError(appRefused, "hello")
		return nil, err
	}
	ctrl := newControl(qconn, st, dec, "s")
	c := &Conn{srv: s, ctrl: ctrl, id: id, since: time.Now()}
	if err := s.track(c); err != nil {
		_ = qconn.CloseWithError(0, "closed")
		return nil, err
	}
	ctrl.start()
	go rejectClientStreams(qconn)
	return c, nil
}

// blocked reports server shutdown or caller cancellation before hello succeeds.
func (s *Server) blocked(ctx context.Context) error {
	if err := s.errIfClosed(); err != nil {
		return err
	}
	return ctx.Err()
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
// q stays in early until then, so Server.Close does not leave it running.
func (s *Server) refuse(qconn *quic.Conn, st *quic.Stream, m rpc.Message, herr error) {
	_ = rpc.Write(st, rpc.Message{Op: m.Op, ID: m.ID, OK: false, Error: herr.Error()})
	_ = st.Close()
	go func() {
		defer func() {
			_ = qconn.CloseWithError(appRefused, herr.Error())
			s.end(qconn)
		}()
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

func (s *Server) begin(q *quic.Conn) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return errClosed
	}
	s.early[q] = struct{}{}
	return nil
}

func (s *Server) end(q *quic.Conn) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.early, q)
}

func (s *Server) errIfClosed() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return errClosed
	}
	return nil
}

// track publishes c. A shutdown that already won closes nothing here;
// the caller closes qconn and Accept returns an error.
func (s *Server) track(c *Conn) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.early, c.ctrl.qconn)
	if s.closed {
		return errClosed
	}
	s.conns[c] = struct{}{}
	return nil
}

func (s *Server) untrack(c *Conn) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.conns, c)
}

// Close stops the listener and every QUIC connection, including hellos still in flight.
func (s *Server) Close() error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	conns := s.conns
	early := s.early
	s.conns = nil
	s.early = nil
	s.mu.Unlock()
	s.cancel()
	err := s.ln.Close()
	for c := range conns {
		_ = c.ctrl.Close()
	}
	for q := range early {
		_ = q.CloseWithError(0, "closed")
	}
	return err
}

// Conn is one computer accepted by the server.
type Conn struct {
	srv   *Server
	ctrl  *control
	id    Identity
	since time.Time

	sshOpen, sshTotal       atomic.Int64
	portalOpen, portalTotal atomic.Int64
}

// ConnStats is one tunnel's transport counters. They start at zero when the
// computer connects; a reconnect is a new Conn.
type ConnStats struct {
	Since         time.Time     `json:"since"`
	RTT           time.Duration `json:"rtt"`
	RTTVar        time.Duration `json:"rtt_var"`
	BytesSent     uint64        `json:"bytes_sent"`
	BytesReceived uint64        `json:"bytes_received"`
	PacketsSent   uint64        `json:"packets_sent"`
	PacketsLost   uint64        `json:"packets_lost"`
	SSHOpen       int64         `json:"ssh_open"`
	SSHTotal      int64         `json:"ssh_total"`
	PortalOpen    int64         `json:"portal_open"`
	PortalTotal   int64         `json:"portal_total"`
}

// Stats reads the QUIC connection's counters and the streams this Conn opened.
func (c *Conn) Stats() ConnStats {
	st := ConnStats{
		Since:       c.since,
		SSHOpen:     c.sshOpen.Load(),
		SSHTotal:    c.sshTotal.Load(),
		PortalOpen:  c.portalOpen.Load(),
		PortalTotal: c.portalTotal.Load(),
	}
	if c.ctrl == nil || c.ctrl.qconn == nil {
		return st
	}
	q := c.ctrl.qconn.ConnectionStats()
	st.RTT = q.SmoothedRTT
	st.RTTVar = q.MeanDeviation
	st.BytesSent = q.BytesSent
	st.BytesReceived = q.BytesReceived
	st.PacketsSent = q.PacketsSent
	st.PacketsLost = q.PacketsLost
	return st
}

func (c *Conn) Name() string       { return c.id.Name }
func (c *Conn) Identity() Identity { return c.id }

// Done closes when the control stream ends. The computer is offline after that.
func (c *Conn) Done() <-chan struct{} {
	if c.ctrl == nil {
		ch := make(chan struct{})
		close(ch)
		return ch
	}
	return c.ctrl.closed
}

// RemoteAddr is the computer's QUIC address. Nil before the handshake finishes.
func (c *Conn) RemoteAddr() net.Addr {
	if c.ctrl == nil || c.ctrl.qconn == nil {
		return nil
	}
	return c.ctrl.qconn.RemoteAddr()
}

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
	if err := writeHeaderCtx(ctx, st, kind, port); err != nil {
		st.CancelRead(0)
		st.CancelWrite(0)
		return nil, err
	}
	open, total := &c.portalOpen, &c.portalTotal
	if kind == KindSSH {
		open, total = &c.sshOpen, &c.sshTotal
	}
	total.Add(1)
	open.Add(1)
	sc := newStreamConn(st, nil, c.ctrl.qconn.LocalAddr(), c.ctrl.qconn.RemoteAddr()).(*streamConn)
	sc.onClose = func() { open.Add(-1) }
	return sc, nil
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
	qconn, err := quic.DialAddr(ctx, addr, clientTLS(fingerprint), quicConfig())
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
	dec := json.NewDecoder(rpc.FrameLimit(st, rpc.MaxFrame))
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
