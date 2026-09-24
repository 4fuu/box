// Package rpc is the newline-delimited JSON spoken on the node RPC socket
// and the guest socket. Request bodies are not logged.
package rpc

import (
	"encoding/json"
	"errors"
	"io"
	"net"
)

type Message struct {
	Op    string          `json:"op"`
	OK    bool            `json:"ok"`
	Error string          `json:"error,omitempty"`
	Body  json.RawMessage `json:"body,omitempty"`
}

func Write(w io.Writer, m Message) error {
	raw, err := json.Marshal(m)
	if err != nil {
		return err
	}
	raw = append(raw, '\n')
	_, err = w.Write(raw)
	return err
}

func Read(r io.Reader) (Message, error) {
	dec := json.NewDecoder(r)
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

// Serve reads requests until the connection closes.
func Serve(conn net.Conn, h func(op string, body json.RawMessage) (any, error)) error {
	dec := json.NewDecoder(conn)
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
