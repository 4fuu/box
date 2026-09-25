package repl

import (
	"bufio"
	"io"
	"reflect"
	"strings"
	"testing"
)

// newEditor builds a raw, echoing Editor over input.
func newEditor(input string, out io.Writer) *Editor {
	return &Editor{in: bufio.NewReader(strings.NewReader(input)), out: out, raw: true, echo: true}
}

func TestEditorHistory(t *testing.T) {
	in := bufio.NewReader(strings.NewReader("ls\r\x1b[A\r"))
	var out strings.Builder
	ed := newEditor("", &out)
	ed.in = in

	// One shared Editor: the recalled line must equal the committed one.
	var got []string
	for i := 0; i < 2; i++ {
		line, err := ed.ReadLine()
		if err != nil {
			t.Fatalf("read %d: %v", i, err)
		}
		got = append(got, line)
	}
	want := []string{"ls", "ls"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestEditorHistoryDraftRestored(t *testing.T) {
	// Up past history restores the draft typed before recall.
	ed := newEditor("ls\rdr\x1b[A\x1b[B\r", io.Discard)
	if _, err := ed.ReadLine(); err != nil {
		t.Fatal(err)
	}
	line, err := ed.ReadLine()
	if err != nil {
		t.Fatal(err)
	}
	if line != "dr" {
		t.Fatalf("draft not restored: got %q", line)
	}
}

func TestEditorMidLineInsert(t *testing.T) {
	// "abc", left twice, insert X -> aXbc.
	ed := newEditor("abc\x1b[D\x1b[DX\r", io.Discard)
	line, err := ed.ReadLine()
	if err != nil {
		t.Fatal(err)
	}
	if line != "aXbc" {
		t.Fatalf("got %q, want %q", line, "aXbc")
	}
}

func TestEditorHomeDelete(t *testing.T) {
	// "abcd", home, delete twice -> "cd".
	ed := newEditor("abcd\x1b[H\x1b[3~\x1b[3~\r", io.Discard)
	line, err := ed.ReadLine()
	if err != nil {
		t.Fatal(err)
	}
	if line != "cd" {
		t.Fatalf("got %q, want %q", line, "cd")
	}
}

func TestEditorKillWord(t *testing.T) {
	ed := newEditor("hello world\x17\r", io.Discard)
	line, err := ed.ReadLine()
	if err != nil {
		t.Fatal(err)
	}
	if line != "hello" {
		t.Fatalf("got %q, want %q", line, "hello")
	}
}

func TestEditorCtrlAE(t *testing.T) {
	// "xy", ^A moves home, insert Z, ^E moves to end, append !.
	ed := newEditor("xy\x01Z\x05!\r", io.Discard)
	line, err := ed.ReadLine()
	if err != nil {
		t.Fatal(err)
	}
	if line != "Zxy!" {
		t.Fatalf("got %q, want %q", line, "Zxy!")
	}
}

func TestEditorCtrlDDeletesUnderCursor(t *testing.T) {
	// "abcd", home, right, ^D removes 'b' (the char under the cursor).
	ed := newEditor("abcd\x1b[H\x1b[C\x04\r", io.Discard)
	line, err := ed.ReadLine()
	if err != nil {
		t.Fatal(err)
	}
	if line != "acd" {
		t.Fatalf("got %q, want %q", line, "acd")
	}
}

func TestEditorCtrlDEmptyIsEOF(t *testing.T) {
	ed := newEditor("\x04", io.Discard)
	if _, err := ed.ReadLine(); err != io.EOF {
		t.Fatalf("got %v, want io.EOF", err)
	}
}

func TestEditorTabUniqueCandidate(t *testing.T) {
	var out strings.Builder
	ed := newEditor("he\t\r", &out)
	ed.complete = func(line string, cur int) []string { return []string{"help"} }
	line, err := ed.ReadLine()
	if err != nil {
		t.Fatal(err)
	}
	if line != "help" {
		t.Fatalf("got %q, want %q", line, "help")
	}
	if !strings.Contains(out.String(), "help") {
		t.Fatalf("replacement was not echoed: %q", out.String())
	}
}

func TestEditorTabLongestCommonPrefix(t *testing.T) {
	var out strings.Builder
	ed := newEditor("re\t\r", &out)
	ed.complete = func(line string, cur int) []string {
		return []string{"restart", "resize", "rename"}
	}
	line, err := ed.ReadLine()
	if err != nil {
		t.Fatal(err)
	}
	if line != "re" {
		t.Fatalf("got %q, want %q", line, "re")
	}
}

func TestEditorTabListsCandidates(t *testing.T) {
	var out strings.Builder
	ed := newEditor("ab\t\r", &out)
	ed.complete = func(line string, cur int) []string { return []string{"abc", "abd"} }
	line, err := ed.ReadLine()
	if err != nil {
		t.Fatal(err)
	}
	if line != "ab" { // candidates listed, buffer unchanged
		t.Fatalf("got %q, want %q", line, "ab")
	}
	if !strings.Contains(out.String(), "abc") || !strings.Contains(out.String(), "abd") {
		t.Fatalf("candidates not listed: %q", out.String())
	}
}

func TestCompleteLineCommands(t *testing.T) {
	r := &REPL{}
	got := r.completeLine("he", 2)
	if !reflect.DeepEqual(got, []string{"help"}) {
		t.Fatalf("prefix: got %q", got)
	}
	all := r.completeLine("", 0)
	if len(all) == 0 || !isSorted(all) {
		t.Fatalf("all commands not sorted: %q", all)
	}
	if !contains(all, "ssh") || !contains(all, "env") {
		t.Fatalf("missing commands: %q", all)
	}
}

func TestCompleteLineSubcommands(t *testing.T) {
	r := &REPL{}
	got := r.completeLine("node ", 5)
	if len(got) == 0 || !isSorted(got) || !contains(got, "pair") {
		t.Fatalf("node subcommands: %q", got)
	}
	got = r.completeLine("image p", 7)
	if !reflect.DeepEqual(got, []string{"pull"}) {
		t.Fatalf("image p: %q", got)
	}
}

func TestCompleteLineHelpIncludesAll(t *testing.T) {
	r := &REPL{}
	got := r.completeLine("help ", 5)
	if !contains(got, "all") {
		t.Fatalf("help all missing: %q", got)
	}
}

func TestCompleteLineNoFlags(t *testing.T) {
	r := &REPL{}
	if got := r.completeLine("ssh -", 5); got != nil {
		t.Fatalf("flags should not complete: %q", got)
	}
}

func TestCompleteLineObjectsWithoutSvc(t *testing.T) {
	r := &REPL{} // Svc nil: object positions yield nothing, no panic.
	if got := r.completeLine("ssh ", 4); got != nil {
		t.Fatalf("got %q, want nil", got)
	}
}

func isSorted(s []string) bool {
	for i := 1; i < len(s); i++ {
		if s[i-1] > s[i] {
			return false
		}
	}
	return true
}

func contains(s []string, v string) bool {
	for _, x := range s {
		if x == v {
			return true
		}
	}
	return false
}
