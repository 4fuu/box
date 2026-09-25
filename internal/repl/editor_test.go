package repl

import (
	"bufio"
	"errors"
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

func TestEditorTabMenuCycle(t *testing.T) {
	// First Tab lists, two more Tabs cycle abc -> abd, Enter confirms.
	ed := newEditor("ab\t\t\t\r", io.Discard)
	ed.complete = func(line string, cur int) []string { return []string{"abc", "abd"} }
	line, err := ed.ReadLine()
	if err != nil {
		t.Fatal(err)
	}
	if line != "abd" {
		t.Fatalf("got %q, want %q", line, "abd")
	}
}

func TestEditorTabMenuBackTab(t *testing.T) {
	// Shift-Tab cycles backward, landing on the last candidate.
	ed := newEditor("ab\t\x1b[Z\r", io.Discard)
	ed.complete = func(line string, cur int) []string { return []string{"abc", "abd"} }
	line, err := ed.ReadLine()
	if err != nil {
		t.Fatal(err)
	}
	if line != "abd" {
		t.Fatalf("got %q, want %q", line, "abd")
	}
}

func TestEditorTabMenuDismissKeepsSelection(t *testing.T) {
	// Typing after the menu dismisses the listing but keeps the selection.
	ed := newEditor("ab\t\tx\r", io.Discard)
	ed.complete = func(line string, cur int) []string { return []string{"abc", "abd"} }
	line, err := ed.ReadLine()
	if err != nil {
		t.Fatal(err)
	}
	if line != "abcx" {
		t.Fatalf("got %q, want %q", line, "abcx")
	}
}

func TestEditorCtrlCInterrupts(t *testing.T) {
	// ^C drops the half-typed line; the editor keeps working afterwards.
	ed := newEditor("par\x03ls\r", io.Discard)
	if _, err := ed.ReadLine(); !errors.Is(err, errInterrupt) {
		t.Fatalf("got err %v, want errInterrupt", err)
	}
	line, err := ed.ReadLine()
	if err != nil {
		t.Fatal(err)
	}
	if line != "ls" {
		t.Fatalf("got %q, want %q", line, "ls")
	}
}

func TestEditorCtrlRSearchExec(t *testing.T) {
	// ^R + query + Enter runs the newest matching history entry.
	ed := newEditor("restart alpha\r\x12alpha\r", io.Discard)
	if _, err := ed.ReadLine(); err != nil {
		t.Fatal(err)
	}
	line, err := ed.ReadLine()
	if err != nil {
		t.Fatal(err)
	}
	if line != "restart alpha" {
		t.Fatalf("got %q, want %q", line, "restart alpha")
	}
}

func TestEditorCtrlRSearchEdit(t *testing.T) {
	// ESC during a search drops the match into the buffer for editing.
	ed := newEditor("restart alpha\r\x12alpha\x1bX\r", io.Discard)
	if _, err := ed.ReadLine(); err != nil {
		t.Fatal(err)
	}
	line, err := ed.ReadLine()
	if err != nil {
		t.Fatal(err)
	}
	if line != "restart alphaX" {
		t.Fatalf("got %q, want %q", line, "restart alphaX")
	}
}

func TestEditorCtrlWordMotion(t *testing.T) {
	// Ctrl+Left (ESC [ 1 ; 5 D) moves by word and must not leak "1;5D".
	ed := newEditor("abc def\x1b[1;5DX\r", io.Discard)
	line, err := ed.ReadLine()
	if err != nil {
		t.Fatal(err)
	}
	if line != "abc Xdef" {
		t.Fatalf("got %q, want %q", line, "abc Xdef")
	}
}

func TestEditorEscBWordLeft(t *testing.T) {
	// readline-style ESC b also moves left by word.
	ed := newEditor("abc def\x1bbX\r", io.Discard)
	line, err := ed.ReadLine()
	if err != nil {
		t.Fatal(err)
	}
	if line != "abc Xdef" {
		t.Fatalf("got %q, want %q", line, "abc Xdef")
	}
}

func TestLayoutMenu(t *testing.T) {
	// Width 7, colw 5: one column, input order preserved.
	rows := layoutMenu([]string{"ab", "cde", "f"}, -1, 7)
	if want := []string{"ab", "cde", "f"}; !reflect.DeepEqual(rows, want) {
		t.Fatalf("got %q, want %q", rows, want)
	}
	// Width 20, colw 5: everything fits one row; selection is highlighted.
	rows = layoutMenu([]string{"ab", "cde", "f"}, 1, 20)
	if len(rows) != 1 {
		t.Fatalf("got %d rows, want 1: %q", len(rows), rows)
	}
	if !strings.Contains(rows[0], "\x1b[7mcde\x1b[0m") {
		t.Fatalf("selection not highlighted: %q", rows[0])
	}
	// Column-major: 4 candidates, width 4 (colw 4, one column) -> 4 rows.
	rows = layoutMenu([]string{"a1", "a2", "a3", "a4"}, -1, 4)
	if len(rows) != 4 || rows[0] != "a1" || rows[3] != "a4" {
		t.Fatalf("bad column layout: %q", rows)
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

func TestCompleteLineFlags(t *testing.T) {
	r := &REPL{}
	if got := r.completeLine("ssh -", 5); !reflect.DeepEqual(got, []string{"--json"}) {
		t.Fatalf(`ssh -: got %q, want ["--json"]`, got)
	}
	if got := r.completeLine("new --i", 7); !reflect.DeepEqual(got, []string{"--image"}) {
		t.Fatalf(`new --i: got %q, want ["--image"]`, got)
	}
}

func TestCompleteLineHelpExcludesHelp(t *testing.T) {
	r := &REPL{}
	got := r.completeLine("help ", 5)
	if contains(got, "help") {
		t.Fatalf("help should not complete to itself: %q", got)
	}
	if !contains(got, "all") || !contains(got, "ls") {
		t.Fatalf("missing expected candidates: %q", got)
	}
	if !isSorted(got) {
		t.Fatalf("candidates not sorted: %q", got)
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
