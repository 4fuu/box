package tunnel

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"sync"
	"sync/atomic"

	"github.com/4fuu/box/internal/rpc"
	"github.com/quic-go/quic-go"
)

var (
	errClosed   = errors.New("connection closed")
	errNotReady = errors.New("control stream not ready")
)

// Handler answers one peer request on the control stream.
// The body is the raw JSON of that request. Return a value to send as the
// response body, or an error to refuse it.
type Handler func(op string, body json.RawMessage) (any, error)

// control is the shared newline-JSON stream. writeMu serializes writes.
type control struct {
	qconn  *quic.Conn
	stream *quic.Stream
	dec    *json.Decoder

	writeMu sync.Mutex
	mu      sync.Mutex
	cond    *sync.Cond

	pending    map[string]chan rpc.Message
	handler    Handler
	handlerSet bool
	err        error
	closed     chan struct{}

	prefix string
	seq    atomic.Uint64
}

func newControl(qconn *quic.Conn, st *quic.Stream, dec *json.Decoder, prefix string) *control {
	c := &control{
		qconn:   qconn,
		stream:  st,
		dec:     dec,
		pending: make(map[string]chan rpc.Message),
		closed:  make(chan struct{}),
		prefix:  prefix,
	}
	c.cond = sync.NewCond(&c.mu)
	return c
}

func (c *control) start() { go c.readLoop() }

func (c *control) Handle(h Handler) {
	c.mu.Lock()
	c.handler = h
	c.handlerSet = true
	c.mu.Unlock()
	c.cond.Broadcast()
}

func (c *control) nextID() string {
	return c.prefix + strconv.FormatUint(c.seq.Add(1), 10)
}

func (c *control) Call(ctx context.Context, op string, req, resp any) error {
	c.mu.Lock()
	if c.err != nil {
		err := c.err
		c.mu.Unlock()
		return err
	}
	c.mu.Unlock()

	var body json.RawMessage
	if req != nil {
		raw, err := json.Marshal(req)
		if err != nil {
			return err
		}
		body = raw
	}
	id := c.nextID()
	ch := make(chan rpc.Message, 1)
	c.mu.Lock()
	if c.err != nil {
		err := c.err
		c.mu.Unlock()
		return err
	}
	c.pending[id] = ch
	c.mu.Unlock()

	if err := c.write(rpc.Message{ID: id, Op: op, Body: body}); err != nil {
		c.forget(id)
		return err
	}

	select {
	case m := <-ch:
		c.forget(id)
		return decodeResp(m, resp)
	case <-ctx.Done():
		go c.forgetWhen(id, ch)
		return ctx.Err()
	case <-c.closed:
		c.forget(id)
		return c.deadErr()
	}
}

func (c *control) forget(id string) {
	c.mu.Lock()
	delete(c.pending, id)
	c.mu.Unlock()
}

// forgetWhen drops the waiter only after the response arrives or the stream dies,
// so a late response is not dispatched as a new request.
func (c *control) forgetWhen(id string, ch <-chan rpc.Message) {
	select {
	case <-ch:
	case <-c.closed:
	}
	c.forget(id)
}

func (c *control) deadErr() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.err != nil {
		return c.err
	}
	return errClosed
}

func (c *control) write(m rpc.Message) error {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	select {
	case <-c.closed:
		return errClosed
	default:
	}
	return rpc.Write(c.stream, m)
}

func (c *control) readLoop() {
	var err error
	defer func() {
		if err == nil {
			err = errClosed
		}
		c.shutdown(err)
	}()
	for {
		var m rpc.Message
		if err = c.dec.Decode(&m); err != nil {
			return
		}
		if c.deliver(m) {
			continue
		}
		go c.dispatch(m)
	}
}

func (c *control) deliver(m rpc.Message) bool {
	if m.ID == "" {
		return false
	}
	c.mu.Lock()
	ch := c.pending[m.ID]
	c.mu.Unlock()
	if ch == nil {
		return false
	}
	select {
	case ch <- m:
	case <-c.closed:
	}
	return true
}

func (c *control) dispatch(m rpc.Message) {
	c.mu.Lock()
	for !c.handlerSet && c.err == nil {
		c.cond.Wait()
	}
	h := c.handler
	dead := c.err != nil && !c.handlerSet
	c.mu.Unlock()

	resp := rpc.Message{Op: m.Op, ID: m.ID, OK: true}
	if dead || h == nil {
		resp.OK = false
		resp.Error = "unsupported"
		if dead {
			resp.Error = "closed"
		}
		_ = c.write(resp)
		return
	}
	out, herr := h(m.Op, m.Body)
	if herr != nil {
		resp.OK = false
		resp.Error = herr.Error()
	} else if out != nil {
		raw, err := json.Marshal(out)
		if err != nil {
			resp.OK = false
			resp.Error = "encode"
		} else {
			resp.Body = raw
		}
	}
	_ = c.write(resp)
}

func (c *control) shutdown(err error) {
	c.mu.Lock()
	if c.err != nil {
		c.mu.Unlock()
		return
	}
	if err == nil {
		err = errClosed
	}
	c.err = err
	close(c.closed)
	c.mu.Unlock()
	c.cond.Broadcast()
}

func (c *control) Close() error {
	c.shutdown(errClosed)
	// Cancel unblocks a Write still holding the stream, which Close must not race.
	c.stream.CancelWrite(0)
	c.stream.CancelRead(0)
	return c.qconn.CloseWithError(0, "closed")
}

func decodeResp(m rpc.Message, resp any) error {
	if !m.OK {
		if m.Error == "" {
			return errors.New("rpc failed")
		}
		return errors.New(m.Error)
	}
	if resp != nil && len(m.Body) > 0 && string(m.Body) != "null" {
		return json.Unmarshal(m.Body, resp)
	}
	return nil
}

func readMsg(ctx context.Context, dec *json.Decoder, st *quic.Stream) (rpc.Message, error) {
	type result struct {
		m   rpc.Message
		err error
	}
	ch := make(chan result, 1)
	go func() {
		var m rpc.Message
		err := dec.Decode(&m)
		ch <- result{m, err}
	}()
	select {
	case <-ctx.Done():
		st.CancelRead(0)
		<-ch
		return rpc.Message{}, ctx.Err()
	case r := <-ch:
		return r.m, r.err
	}
}
