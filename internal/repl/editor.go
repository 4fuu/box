package repl

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"strings"
	"unicode/utf8"
)

// Editor reads one line of input. With a raw PTY and echo on it provides a
// small line editor: cursor movement, up/down history, Tab completion, and
// the common Emacs bindings. Without echo it reads a secret (pairing
// passwords) byte for byte with no redraw and no history. Without a PTY the
// client's terminal does all of it and Editor only splits on '\n'.
//
// One Editor is owned per session so history persists across lines.
type Editor struct {
	in   *bufio.Reader
	out  io.Writer
	raw  bool // input is a PTY in raw mode
	echo bool // typed characters are echoed back

	// prompt is redrawn before the buffer; the cursor is assumed to sit
	// right after it when the line starts.
	prompt string
	// complete returns candidates for the word at the rune index cur in
	// line. A nil func disables completion.
	complete func(line string, cur int) []string

	history []string
	histPos int    // index of the next entry histDown would show
	draft   string // unsubmitted line saved while browsing history
}

// ReadLine reads one line. It returns io.EOF when the caller hangs up or
// presses ^C, or ^D on an empty line.
func (e *Editor) ReadLine() (string, error) {
	if !e.raw {
		line, err := e.in.ReadString('\n')
		return strings.TrimSpace(strings.TrimRight(line, "\r\n")), err
	}
	if !e.echo {
		return e.readSecret()
	}
	return e.edit()
}

// readSecret reads a line without echoing input: the pairing-password
// discipline. Enter ends the line, backspace deletes the last rune, ^C and
// ^D on an empty line end the session, ^D ends a typed line as-is. No
// history, completion, or redraw.
func (e *Editor) readSecret() (string, error) {
	var buf []byte
	for {
		b, err := e.in.ReadByte()
		if err != nil {
			if len(buf) > 0 && err == io.EOF {
				return strings.TrimSpace(string(buf)), nil
			}
			return "", err
		}
		switch {
		case b == '\r' || b == '\n':
			// A real terminal echoes the newline even with ECHO off;
			// without it the next output starts on the prompt's line.
			_, _ = io.WriteString(e.out, "\r\n")
			return strings.TrimSpace(string(buf)), nil
		case b == 0x03, b == 0x04 && len(buf) == 0: // ^C, ^D on an empty line
			if e.echo && b == 0x03 {
				_, _ = io.WriteString(e.out, "^C")
			}
			_, _ = io.WriteString(e.out, "\r\n")
			return "", io.EOF
		case b == 0x04: // ^D ends the line as typed
			_, _ = io.WriteString(e.out, "\r\n")
			return strings.TrimSpace(string(buf)), nil
		case b == 0x08 || b == 0x7f: // backspace
			for len(buf) > 0 {
				last := buf[len(buf)-1]
				buf = buf[:len(buf)-1]
				if last&0xc0 != 0x80 { // dropped a whole rune
					break
				}
			}
			if e.echo {
				fmt.Fprint(e.out, "\b \b")
			}
		case b >= 0x20: // printable; other control bytes are ignored
			buf = append(buf, b)
			if e.echo {
				_, _ = e.out.Write([]byte{b})
			}
		}
	}
}

// edit is the interactive line editor. The terminal is in raw mode, so every
// control byte and escape sequence arrives here.
func (e *Editor) edit() (string, error) {
	var buf []rune
	cur := 0
	e.histPos = len(e.history)
	e.draft = ""

	setLine := func(s string) {
		buf = []rune(s)
		cur = len(buf)
		e.redraw(buf, cur)
	}
	histUp := func() {
		if len(e.history) == 0 {
			return
		}
		if e.histPos == len(e.history) {
			e.draft = string(buf)
		}
		if e.histPos > 0 {
			e.histPos--
			setLine(e.history[e.histPos])
		}
	}
	histDown := func() {
		if e.histPos == len(e.history) {
			return
		}
		e.histPos++
		if e.histPos == len(e.history) {
			setLine(e.draft)
		} else {
			setLine(e.history[e.histPos])
		}
	}
	left := func(n int) {
		if cur >= n && n > 0 {
			cur -= n
			fmt.Fprintf(e.out, "\x1b[%dD", n)
		}
	}
	right := func(n int) {
		if cur+n <= len(buf) && n > 0 {
			cur += n
			fmt.Fprintf(e.out, "\x1b[%dC", n)
		}
	}
	deleteAt := func(i int) { // remove the rune at i
		if i >= 0 && i < len(buf) {
			buf = append(buf[:i], buf[i+1:]...)
			e.redraw(buf, cur)
		}
	}

	for {
		b, err := e.in.ReadByte()
		if err != nil {
			// The caller hung up mid-line: keep what was typed, as a
			// terminal would leave it on screen.
			if len(buf) > 0 && errors.Is(err, io.EOF) {
				line := strings.TrimSpace(string(buf))
				e.commit(line)
				return line, nil
			}
			return "", err
		}
		switch b {
		case '\r', '\n':
			_, _ = io.WriteString(e.out, "\r\n")
			line := strings.TrimSpace(string(buf))
			e.commit(line)
			return line, nil
		case 0x01: // ^A
			left(cur)
		case 0x05: // ^E
			right(len(buf) - cur)
		case 0x02: // ^B
			left(1)
		case 0x06: // ^F
			right(1)
		case 0x10: // ^P
			histUp()
		case 0x0e: // ^N
			histDown()
		case 0x03: // ^C
			_, _ = io.WriteString(e.out, "^C\r\n")
			return "", io.EOF
		case 0x04: // ^D deletes the rune under the cursor; EOF on an empty line
			if len(buf) == 0 {
				_, _ = io.WriteString(e.out, "^D\r\n")
				return "", io.EOF
			}
			deleteAt(cur)
		case 0x08, 0x7f: // backspace
			if cur > 0 {
				buf = append(buf[:cur-1], buf[cur:]...)
				cur--
				e.redraw(buf, cur)
			}
		case 0x0b: // ^K kills to end of line
			if cur < len(buf) {
				buf = buf[:cur]
				e.redraw(buf, cur)
			}
		case 0x15: // ^U kills to start of line
			if cur > 0 {
				buf = buf[cur:]
				cur = 0
				e.redraw(buf, cur)
			}
		case 0x17: // ^W deletes the word behind the cursor
			i := cur
			for i > 0 && buf[i-1] == ' ' {
				i--
			}
			for i > 0 && buf[i-1] != ' ' {
				i--
			}
			if i < cur {
				buf = append(buf[:i], buf[cur:]...)
				cur = i
				e.redraw(buf, cur)
			}
		case 0x0c: // ^L clears the screen and redraws
			_, _ = io.WriteString(e.out, "\x1b[H\x1b[2J")
			e.redraw(buf, cur)
		case '\t':
			e.completeWord(&buf, &cur)
		case 0x1b: // ESC: arrows, Home/End/Delete
			switch e.readEscape() {
			case keyUp:
				histUp()
			case keyDown:
				histDown()
			case keyLeft:
				left(1)
			case keyRight:
				right(1)
			case keyHome:
				left(cur)
			case keyEnd:
				right(len(buf) - cur)
			case keyDelete:
				deleteAt(cur)
			}
		default:
			if b >= 0x20 { // printable; other control bytes are ignored
				r := e.readRune(b)
				buf = append(buf[:cur], append([]rune{r}, buf[cur:]...)...)
				if cur == len(buf)-1 {
					_, _ = io.WriteString(e.out, string(r))
				} else {
					e.redraw(buf, cur)
				}
				cur++
			}
		}
	}
}

// readRune decodes a UTF-8 sequence whose leading byte b was just read,
// pulling continuation bytes from the same reader.
func (e *Editor) readRune(b byte) rune {
	size := 1
	switch {
	case b&0xe0 == 0xc0:
		size = 2
	case b&0xf0 == 0xe0:
		size = 3
	case b&0xf8 == 0xf0:
		size = 4
	}
	if size == 1 {
		return rune(b)
	}
	enc := []byte{b}
	for i := 1; i < size; i++ {
		c, err := e.in.ReadByte()
		if err != nil {
			return utf8.RuneError
		}
		enc = append(enc, c)
	}
	r, _ := utf8.DecodeRune(enc)
	return r
}

// redraw rewrites the prompt and buffer from the start of the line, erases
// the rest of the line, and leaves the cursor at cur.
func (e *Editor) redraw(buf []rune, cur int) {
	var s strings.Builder
	s.WriteString("\r")
	s.WriteString(e.prompt)
	s.WriteString(string(buf))
	s.WriteString("\x1b[K")
	if n := len(buf) - cur; n > 0 {
		fmt.Fprintf(&s, "\x1b[%dD", n)
	}
	_, _ = io.WriteString(e.out, s.String())
}

// commit appends a submitted line to history, skipping blanks and repeats
// of the most recent entry.
func (e *Editor) commit(line string) {
	if line == "" {
		return
	}
	if n := len(e.history); n > 0 && e.history[n-1] == line {
		return
	}
	e.history = append(e.history, line)
}

// completeWord runs Tab completion on the word behind the cursor: a single
// candidate replaces the word, several candidates extend it to their longest
// common prefix, and a second Tab lists them below the prompt.
func (e *Editor) completeWord(buf *[]rune, cur *int) {
	if e.complete == nil {
		return
	}
	cands := e.complete(string(*buf), *cur)
	if len(cands) == 0 {
		return
	}
	start := *cur
	for start > 0 && (*buf)[start-1] != ' ' {
		start--
	}
	prefix := string((*buf)[start:*cur])
	replace := func(word string) {
		w := []rune(word)
		*buf = append((*buf)[:start], append(w, (*buf)[*cur:]...)...)
		*cur = start + len(w)
		e.redraw(*buf, *cur)
	}
	if len(cands) == 1 {
		if cands[0] != prefix {
			replace(cands[0])
		}
		return
	}
	if lcp := longestCommonPrefix(cands); len(lcp) > len(prefix) {
		replace(lcp)
		return
	}
	_, _ = io.WriteString(e.out, "\r\n"+strings.Join(cands, "   ")+"\r\n")
	e.redraw(*buf, *cur)
}

func longestCommonPrefix(cands []string) string {
	runes := make([][]rune, len(cands))
	for i, c := range cands {
		runes[i] = []rune(c)
	}
	prefix := runes[0]
	for _, r := range runes[1:] {
		n := 0
		for n < len(prefix) && n < len(r) && prefix[n] == r[n] {
			n++
		}
		prefix = prefix[:n]
	}
	return string(prefix)
}

type escKey int

const (
	keyNone escKey = iota
	keyUp
	keyDown
	keyLeft
	keyRight
	keyHome
	keyEnd
	keyDelete
)

// readEscape classifies the escape sequence after an ESC byte. A byte that
// does not start a known sequence is left unread so a lone ESC press cannot
// swallow the next character.
func (e *Editor) readEscape() escKey {
	b, err := e.in.Peek(1)
	if err != nil {
		return keyNone
	}
	switch b[0] {
	case '[': // CSI: arrows, Home/End/Delete via "n~"
		_, _ = e.in.ReadByte()
		c, err := e.in.ReadByte()
		if err != nil {
			return keyNone
		}
		switch c {
		case 'A':
			return keyUp
		case 'B':
			return keyDown
		case 'C':
			return keyRight
		case 'D':
			return keyLeft
		case 'H':
			return keyHome
		case 'F':
			return keyEnd
		}
		if c >= '0' && c <= '9' {
			param := string(c)
			for {
				d, err := e.in.ReadByte()
				if err != nil {
					return keyNone
				}
				if d == '~' {
					break
				}
				if d < '0' || d > '9' {
					return keyNone
				}
				param += string(d)
			}
			switch param {
			case "1", "7":
				return keyHome
			case "3":
				return keyDelete
			case "4", "8":
				return keyEnd
			}
		}
		return keyNone
	case 'O': // SS3: some terminals send arrows this way
		_, _ = e.in.ReadByte()
		c, err := e.in.ReadByte()
		if err != nil {
			return keyNone
		}
		switch c {
		case 'A':
			return keyUp
		case 'B':
			return keyDown
		case 'C':
			return keyRight
		case 'D':
			return keyLeft
		case 'H':
			return keyHome
		case 'F':
			return keyEnd
		}
	}
	return keyNone
}
