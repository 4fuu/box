package repl

import (
	"bufio"
	"bytes"
	"errors"
	"io"
	"strings"
	"testing"
)

func TestCRLFWriter(t *testing.T) {
	var buf bytes.Buffer
	w := CRLF(&buf)
	in := []byte("a\nb\r\nc")
	n, err := w.Write(in)
	if err != nil {
		t.Fatal(err)
	}
	if n != len(in) {
		t.Fatalf("Write returned %d, want %d (the caller's byte count)", n, len(in))
	}
	if got := buf.String(); got != "a\r\nb\r\nc" {
		t.Fatalf("CRLF output = %q, want lone \\n doubled and \\r\\n untouched", got)
	}
}

func TestCRLFWriterPassthrough(t *testing.T) {
	var buf bytes.Buffer
	if _, err := CRLF(&buf).Write([]byte("no newlines")); err != nil {
		t.Fatal(err)
	}
	if got := buf.String(); got != "no newlines" {
		t.Fatalf("got %q", got)
	}
}

func TestReadLineRawEchoesNewline(t *testing.T) {
	var out bytes.Buffer
	line, err := ReadLine(bufio.NewReader(strings.NewReader("ls\r")), &out, true, true)
	if err != nil {
		t.Fatal(err)
	}
	if line != "ls" {
		t.Fatalf("line = %q, want %q", line, "ls")
	}
	if got := out.String(); got != "ls\r\n" {
		t.Fatalf("echo = %q, want %q", got, "ls\r\n")
	}
}

func TestReadLineRawNoEchoStillEndsLine(t *testing.T) {
	var out bytes.Buffer
	line, err := ReadLine(bufio.NewReader(strings.NewReader("secret\r")), &out, true, false)
	if err != nil {
		t.Fatal(err)
	}
	if line != "secret" {
		t.Fatalf("line = %q", line)
	}
	if got := out.String(); got != "\r\n" {
		t.Fatalf("echo = %q, want only the newline", got)
	}
}

func TestReadLineRawCtrlD(t *testing.T) {
	var out bytes.Buffer
	if _, err := ReadLine(bufio.NewReader(strings.NewReader("\x04")), &out, true, true); !errors.Is(err, io.EOF) {
		t.Fatalf("^D on empty line: err = %v, want io.EOF", err)
	}
	line, err := ReadLine(bufio.NewReader(strings.NewReader("partial\x04")), &out, true, true)
	if err != nil {
		t.Fatal(err)
	}
	if line != "partial" {
		t.Fatalf("^D mid-line: line = %q, want %q", line, "partial")
	}
}

func TestExecHelp(t *testing.T) {
	var buf bytes.Buffer
	r := &REPL{Out: &buf, Err: &buf}

	if err := r.Exec([]string{"help"}); err != nil {
		t.Fatal(err)
	}
	overview := buf.String()
	for _, want := range []string{"Common commands:", "† marks a command with subcommands.", "help all"} {
		if !strings.Contains(overview, want) {
			t.Fatalf("help overview missing %q:\n%s", want, overview)
		}
	}

	buf.Reset()
	if err := r.Exec([]string{"help", "all"}); err != nil {
		t.Fatal(err)
	}
	all := buf.String()
	for _, want := range []string{"Computers:", "env set <name> <value>", "node pair"} {
		if !strings.Contains(all, want) {
			t.Fatalf("help all missing %q:\n%s", want, all)
		}
	}

	buf.Reset()
	if err := r.Exec([]string{"help", "node"}); err != nil {
		t.Fatal(err)
	}
	node := buf.String()
	if !strings.Contains(node, "node pair") || strings.Contains(node, "env set") {
		t.Fatalf("help node printed the wrong set:\n%s", node)
	}

	buf.Reset()
	if err := r.Exec([]string{"bogus"}); err == nil || !strings.Contains(err.Error(), `unknown command "bogus"`) {
		t.Fatalf("bogus: err = %v, want unknown command", err)
	}
	if err := r.Exec([]string{"node"}); err == nil || !strings.Contains(err.Error(), "needs a subcommand") {
		t.Fatalf("bare node: err = %v, want needs-a-subcommand", err)
	}
}
