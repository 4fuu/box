package rpc

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net"
	"strings"
	"testing"
)

// A frame of a megabyte and a half — a 1 MiB event body base64'd in JSON —
// passes the limit and survives a net.Pipe, whose writes are one byte at a
// time per unflushed write.
func TestFrameCarriesBigBody(t *testing.T) {
	a, b := net.Pipe()
	defer a.Close()
	defer b.Close()
	go func() {
		_ = Serve(b, func(op string, body json.RawMessage) (any, error) {
			var req EventPublish
			if err := json.Unmarshal(body, &req); err != nil {
				return nil, err
			}
			return EventResult{ID: 1}, nil
		})
	}()
	body := bytes.Repeat([]byte("x"), 1<<20)
	if err := Call(a, OpEventPub, EventPublish{Topic: "t", Body: body}, &EventResult{}); err != nil {
		t.Fatal(err)
	}
}

func TestFrameLimitRejectsGiantFrame(t *testing.T) {
	// Under the limit passes; one byte over fails.
	r := FrameLimit(strings.NewReader(strings.Repeat("x", 100)), 100)
	if all, err := io.ReadAll(r); err != nil || len(all) != 100 {
		t.Fatalf("at the limit: %d %v", len(all), err)
	}
	r = FrameLimit(strings.NewReader(strings.Repeat("x", 101)), 100)
	if _, err := io.ReadAll(r); !errors.Is(err, errFrameTooLarge) {
		t.Fatalf("over the limit: %v", err)
	}
	// A line longer than the limit fails even with newlines around it.
	r = FrameLimit(strings.NewReader("ok\n"+strings.Repeat("x", 10)+"\nok\n"), 8)
	if all, err := io.ReadAll(r); err == nil || !errors.Is(err, errFrameTooLarge) {
		t.Fatalf("long line accepted: %q %v", all, err)
	}
	// Many small frames pass.
	r = FrameLimit(strings.NewReader("one\ntwo\nfour\n"), 4)
	all, err := io.ReadAll(r)
	if err != nil || string(all) != "one\ntwo\nfour\n" {
		t.Fatalf("small frames: %q %v", all, err)
	}
	// A frame ending exactly at the newline passes whatever chunking the
	// reader below sees: this one arrives in one Read.
	r = FrameLimit(io.LimitReader(strings.NewReader(strings.Repeat("x", 9)+"\n"), 10), 9)
	if all, err := io.ReadAll(r); err != nil || len(all) != 10 {
		t.Fatalf("frame at limit with newline: %q %v", all, err)
	}
}

// Serve must refuse a frame over MaxFrame instead of buffering it forever.
func TestServeRefusesOversizedFrame(t *testing.T) {
	a, b := net.Pipe()
	defer a.Close()
	served := make(chan error, 1)
	go func() {
		err := Serve(b, func(string, json.RawMessage) (any, error) { return nil, nil })
		// Closing the read end unblocks the writer, which may still be
		// pushing the rest of a frame nobody will read.
		b.Close()
		served <- err
	}()
	giant := json.RawMessage(`"` + strings.Repeat("x", MaxFrame+1) + `"`)
	// The write may fail once the reader walks away; that is the point.
	_ = Write(a, Message{Op: "op", Body: giant})
	if err := <-served; err == nil {
		t.Fatal("oversized frame was served")
	}
}
