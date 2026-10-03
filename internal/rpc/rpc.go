// Package rpc is the newline-delimited JSON spoken on the node RPC socket
// and the guest socket. Request bodies are not logged.
package rpc

import (
	"encoding/json"
	"errors"
	"io"
	"net"
)

// MaxFrame is the largest one frame on any newline-JSON stream. A 1 MiB
// event body is about 1.4 MiB base64 inside JSON; 4 MiB leaves headroom.
const MaxFrame = 4 << 20

var errFrameTooLarge = errors.New("frame too large")

type Message struct {
	// ID matches a response to a request when both sides write on one stream.
	// Empty on the node and guest sockets.
	ID    string          `json:"id,omitempty"`
	Op    string          `json:"op"`
	OK    bool            `json:"ok"`
	Error string          `json:"error,omitempty"`
	Body  json.RawMessage `json:"body,omitempty"`
}

// frameLimit bounds one newline-delimited frame without buffering: bytes
// since the last newline are counted, and a frame longer than max fails.
// The bytes that push a frame past the limit are never handed over — a JSON
// decoder would otherwise parse the complete value and report the error only
// on the next read.
type frameLimit struct {
	r    io.Reader
	max  int64
	line int64
}

// FrameLimit wraps r so one frame longer than max bytes returns
// errFrameTooLarge. Newline-JSON readers decode the error as a stream fault.
func FrameLimit(r io.Reader, max int64) io.Reader {
	return &frameLimit{r: r, max: max}
}

func (f *frameLimit) Read(p []byte) (int, error) {
	if f.line > f.max {
		return 0, errFrameTooLarge
	}
	n, err := f.r.Read(p)
	if n <= 0 {
		return n, err
	}
	// Walk the chunk frame by frame: seg is the length of the frame the
	// byte at hand belongs to, carrying f.line in for a frame split over
	// reads. One chunk may close several frames and open the next.
	seg := f.line
	for i := 0; i < n; i++ {
		if p[i] == '\n' {
			if seg > f.max {
				return 0, errFrameTooLarge
			}
			seg = 0
			continue
		}
		seg++
	}
	if seg > f.max {
		return 0, errFrameTooLarge
	}
	f.line = seg
	return n, err
}

// Write marshals one frame and writes it whole. A short write would corrupt
// the stream, so it loops like io.Copy until every byte is out.
func Write(w io.Writer, m Message) error {
	raw, err := json.Marshal(m)
	if err != nil {
		return err
	}
	raw = append(raw, '\n')
	for len(raw) > 0 {
		n, err := w.Write(raw)
		if err != nil {
			return err
		}
		if n == 0 {
			return io.ErrShortWrite
		}
		raw = raw[n:]
	}
	return nil
}

func Read(r io.Reader) (Message, error) {
	dec := json.NewDecoder(FrameLimit(r, MaxFrame))
	var m Message
	if err := dec.Decode(&m); err != nil {
		return Message{}, err
	}
	return m, nil
}

// Call writes one request and reads one response on conn.
func Call(conn net.Conn, op string, req, resp any) error {
	var body json.RawMessage
	if req != nil {
		raw, err := json.Marshal(req)
		if err != nil {
			return err
		}
		body = raw
	}
	if err := Write(conn, Message{Op: op, Body: body}); err != nil {
		return err
	}
	m, err := Read(conn)
	if err != nil {
		return err
	}
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

// Serve reads requests until the connection closes. One frame may be at
// most MaxFrame bytes; a longer frame ends the connection with an error.
func Serve(conn net.Conn, h func(op string, body json.RawMessage) (any, error)) error {
	dec := json.NewDecoder(FrameLimit(conn, MaxFrame))
	enc := json.NewEncoder(conn)
	for {
		var m Message
		if err := dec.Decode(&m); err != nil {
			if errors.Is(err, io.EOF) || errors.Is(err, net.ErrClosed) {
				return nil
			}
			return err
		}
		out, herr := h(m.Op, m.Body)
		resp := Message{Op: m.Op, OK: herr == nil}
		if herr != nil {
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
		if err := enc.Encode(resp); err != nil {
			return err
		}
	}
}
