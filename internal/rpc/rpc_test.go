package rpc

import (
	"encoding/json"
	"net"
	"testing"
)

func TestRoundTrip(t *testing.T) {
	a, b := net.Pipe()
	defer a.Close()
	defer b.Close()
	done := make(chan error, 1)
	go func() {
		done <- Serve(b, func(op string, body json.RawMessage) (any, error) {
			if op != OpPull {
				return nil, errString("bad op")
			}
			var req PullBody
			if err := json.Unmarshal(body, &req); err != nil {
				return nil, err
			}
			return NameBody{Name: req.Name}, nil
		})
	}()
	var got NameBody
	if err := Call(a, OpPull, PullBody{Name: "base", Ref: "reg/base:latest"}, &got); err != nil {
		t.Fatal(err)
	}
	if got.Name != "base" {
		t.Fatalf("%+v", got)
	}
	a.Close()
	<-done
}

type errString string

func (e errString) Error() string { return string(e) }
