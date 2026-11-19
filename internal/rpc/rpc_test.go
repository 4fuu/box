package rpc

import (
	"encoding/json"
	"net"
	"testing"
)

func TestRoundTrip(t *testing.T) {
	const op = "round_trip"
	type request struct {
		Name string `json:"name"`
		Ref  string `json:"ref"`
	}
	type response struct {
		Name string `json:"name"`
	}
	a, b := net.Pipe()
	defer a.Close()
	defer b.Close()
	done := make(chan error, 1)
	go func() {
		done <- Serve(b, func(gotOp string, body json.RawMessage) (any, error) {
			if gotOp != op {
				return nil, errString("bad op")
			}
			var req request
			if err := json.Unmarshal(body, &req); err != nil {
				return nil, err
			}
			return response{Name: req.Name}, nil
		})
	}()
	var got response
	if err := Call(a, op, request{Name: "base", Ref: "reg/base:latest"}, &got); err != nil {
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
