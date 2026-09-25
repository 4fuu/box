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
// small line editor: cursor and word movement, up/down history, ^R history
// search, Tab completion with a selectable menu, and the common Emacs
// bindings. Without echo it reads a secret (pairing passwords) byte for byte
// with no redraw and no history. Without a PTY the client's terminal does
// all of it and Editor only splits on '\n'.
//
// One Editor is owned per session so history persists across lines.
type Editor struct {
	in   *bufio.Reader
	out  io.Writer
	raw  bool // input is a PTY in raw mode
	echo bool // typed characters are echoed back
	// width is the peer's terminal width in columns, used to lay out the
	// completion menu; effWidth falls back to 80 when it is zero.
	width int

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
// presses ^D on an empty line, and errInterrupt when ^C aborts the line.
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

	// menu is the live Tab-completion listing; non-nil means it is on
	// screen and must be erased before anything else moves the line.
	var menu *menuState
	keepsMenu := func(b byte) bool {
		switch b {
		case '\t', 0x1b, 0x01, 0x02, 0x05, 0x06: // Tab, ESC, ^A ^B ^E ^F
			return true
		}
		return false
	}
	dropMenu := func() {
		if menu == nil {
			return
		}
		menu = nil
		// Reset any highlight and clear from the cursor down; the cursor
		// sits on the prompt line, so this erases just the listing.
		_, _ = io.WriteString(e.out, "\x1b[0m\x1b[J")
		e.redraw(buf, cur)
	}
	menuCycle := func(step int) {
		n := len(menu.cands)
		if menu.idx == -1 {
			if step > 0 {
				menu.idx = 0
			} else {
				menu.idx = n - 1
			}
		} else {
			menu.idx = (menu.idx + step + n) % n
		}
		// Rebuild the buffer around the highlighted candidate so Enter
		// confirms the selection and typing continues from it.
		cand := []rune(menu.cands[menu.idx])
		buf = append(append([]rune{}, buf[:menu.start]...), cand...)
		buf = append(buf, menu.tail...)
		cur = menu.start + len(cand)
		e.listMenu(buf, cur, menu)
	}
	wordLeft := func() {
		i := cur
		for i > 0 && buf[i-1] == ' ' {
			i--
		}
		for i > 0 && buf[i-1] != ' ' {
			i--
		}
		left(cur - i)
	}
	wordRight := func() {
		i := cur
		for i < len(buf) && buf[i] == ' ' {
			i++
		}
		for i < len(buf) && buf[i] != ' ' {
			i++
		}
		right(i - cur)
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
		if menu != nil && !keepsMenu(b) {
			// Anything but menu navigation dismisses the listing; the
			// buffer keeps the highlighted candidate.
			dropMenu()
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
		case 0x03: // ^C aborts this line only; Loop prints a fresh prompt
			_, _ = io.WriteString(e.out, "^C\r\n")
			return "", errInterrupt
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
		case 0x12: // ^R reverse history search
			line, act := e.searchHistory()
			switch act {
			case searchExec:
				return line, nil
			case searchEdit:
				setLine(line)
			case searchInterrupt:
				return "", errInterrupt
			case searchCancel:
				e.redraw(buf, cur)
			}
		case '\t':
			if menu != nil {
				menuCycle(1)
			} else {
				menu = e.completeWord(&buf, &cur)
			}
		case 0x1b: // ESC: arrows, word motions, Home/End/Delete, Shift-Tab
			switch e.readEscape() {
			case keyUp:
				dropMenu()
				histUp()
			case keyDown:
				dropMenu()
				histDown()
			case keyLeft:
				left(1)
			case keyRight:
				right(1)
			case keyWordLeft:
				wordLeft()
			case keyWordRight:
				wordRight()
			case keyHome:
				left(cur)
			case keyEnd:
				right(len(buf) - cur)
			case keyDelete:
				dropMenu()
				deleteAt(cur)
			case keyBackTab:
				if menu != nil {
					menuCycle(-1)
				}
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

// menuState is a live Tab-completion listing. start and tail bracket the
// completed word in the buffer: buf[:start] is the line before the word and
// tail is everything after it, so cycling rebuilds the buffer as
// buf[:start] + candidate + tail. idx == -1 means nothing is highlighted
// yet; non-nil menuState means the listing is on screen.
type menuState struct {
	cands []string
	idx   int
	start int
	tail  []rune
}

// completeWord runs Tab completion on the word behind the cursor. A single
// candidate replaces the word. Several candidates extend the word to their
// longest common prefix and open a menu listing below the prompt: further
// Tabs cycle the highlight, Enter confirms it, and any other key dismisses
// the listing while keeping the selection in the buffer.
func (e *Editor) completeWord(buf *[]rune, cur *int) *menuState {
	if e.complete == nil {
		return nil
	}
	cands := e.complete(string(*buf), *cur)
	if len(cands) == 0 {
		return nil
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
		return nil
	}
	if lcp := longestCommonPrefix(cands); len(lcp) > len(prefix) {
		replace(lcp)
	}
	m := &menuState{
		cands: cands,
		idx:   -1,
		start: start,
		tail:  append([]rune{}, (*buf)[*cur:]...),
	}
	e.listMenu(*buf, *cur, m)
	return m
}

// listMenu prints the candidate listing below the prompt and redraws the
// line. The rows may scroll the screen, but the cursor stays a fixed number
// of lines below the prompt, so moving back up that many lines always lands
// on the prompt line again.
func (e *Editor) listMenu(buf []rune, cur int, m *menuState) {
	rows := layoutMenu(m.cands, m.idx, e.effWidth())
	var s strings.Builder
	s.WriteString("\r\n")
	for _, row := range rows {
		s.WriteString("\r" + row + "\x1b[K\r\n")
	}
	fmt.Fprintf(&s, "\x1b[%dA", len(rows)+1)
	_, _ = io.WriteString(e.out, s.String())
	e.redraw(buf, cur)
}

// layoutMenu arranges candidates into column-major rows that fit width
// columns, highlighting cands[sel] (sel < 0 highlights nothing). Cells are
// padded by rune count, which is exact for the ASCII names the REPL
// completes.
func layoutMenu(cands []string, sel, width int) []string {
	if len(cands) == 0 {
		return nil
	}
	colw := 0
	for _, c := range cands {
		if n := utf8.RuneCountInString(c); n > colw {
			colw = n
		}
	}
	colw += 2
	cols := width / colw
	if cols < 1 {
		cols = 1
	}
	rows := (len(cands) + cols - 1) / cols
	out := make([]string, rows)
	for row := 0; row < rows; row++ {
		var s strings.Builder
		for col := 0; col < cols; col++ {
			i := col*rows + row
			if i >= len(cands) {
				break
			}
			c := cands[i]
			if i == sel {
				s.WriteString("\x1b[7m" + c + "\x1b[0m")
			} else {
				s.WriteString(c)
			}
			s.WriteString(strings.Repeat(" ", colw-utf8.RuneCountInString(c)))
		}
		out[row] = strings.TrimRight(s.String(), " ")
	}
	return out
}

// effWidth is the terminal width used to lay out the completion menu.
func (e *Editor) effWidth() int {
	if e.width > 0 {
		return e.width
	}
	return 80
}

// searchAction is what a ^R history search ended with.
type searchAction int

const (
	searchCancel    searchAction = iota // redraw the line as it was
	searchExec                          // run the match
	searchEdit                          // put the match in the buffer to edit
	searchInterrupt                     // ^C: abort the line
)

// searchHistory is the incremental reverse search behind ^R: each character
// narrows the newest matching history entry, another ^R steps to the next
// older match, Enter runs the match, ESC drops it into the buffer for
// editing, and ^G cancels. This is the readline behavior shells use.
func (e *Editor) searchHistory() (string, searchAction) {
	var q []rune
	match := ""
	pos := len(e.history) // entries at or above pos are excluded

	find := func() {
		match = ""
		for i := pos - 1; i >= 0; i-- {
			if strings.Contains(e.history[i], string(q)) {
				match = e.history[i]
				pos = i
				return
			}
		}
	}
	render := func() {
		tag := "reverse-i-search"
		if match == "" {
			tag = "failed " + tag
		}
		fmt.Fprintf(e.out, "\r(%s)`%s': %s\x1b[K", tag, string(q), match)
	}

	render()
	for {
		b, err := e.in.ReadByte()
		if err != nil {
			return "", searchInterrupt
		}
		switch {
		case b == 0x12: // ^R: next older match
			find()
			render()
		case b == '\r' || b == '\n':
			_, _ = io.WriteString(e.out, "\r\n")
			if match != "" {
				e.commit(match)
				return match, searchExec
			}
			return "", searchCancel
		case b == 0x07: // ^G cancels, like readline
			return "", searchCancel
		case b == 0x03: // ^C
			_, _ = io.WriteString(e.out, "^C\r\n")
			return "", searchInterrupt
		case b == 0x08 || b == 0x7f: // backspace
			if len(q) > 0 {
				q = q[:len(q)-1]
				pos = len(e.history)
				find()
			}
			render()
		case b == 0x1b: // ESC: a match goes into the buffer for editing
			k := e.readEscape()
			if k == keyNone && match != "" {
				return match, searchEdit
			}
			return "", searchCancel
		case b >= 0x20:
			q = append(q, e.readRune(b))
			pos = len(e.history)
			find()
			render()
		}
	}
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
	keyWordLeft
	keyWordRight
	keyHome
	keyEnd
	keyDelete
	keyBackTab
)

// readEscape classifies the escape sequence after an ESC byte. CSI sequences
// are always consumed whole — parameters, intermediates, and final byte — so
// an unknown or modified sequence (say Ctrl+Left's ESC [ 1 ; 5 D) cannot leak
// its parameter bytes into the line buffer as typed text. A byte that does
// not start a known sequence is left unread so a lone ESC press cannot
// swallow the next character.
func (e *Editor) readEscape() escKey {
	b, err := e.in.Peek(1)
	if err != nil {
		return keyNone
	}
	switch b[0] {
	case '[': // CSI
		_, _ = e.in.ReadByte()
		return e.readCSI()
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
	case 'b': // readline-style ESC b / ESC f word motion
		_, _ = e.in.ReadByte()
		return keyWordLeft
	case 'f':
		_, _ = e.in.ReadByte()
		return keyWordRight
	}
	return keyNone
}

// readCSI consumes the body of a CSI sequence: parameter bytes (0x30–0x3f,
// e.g. digits and ;), intermediate bytes (0x20–0x2f, ignored), then a final
// byte (0x40–0x7e) which classifies the key together with the parameters.
func (e *Editor) readCSI() escKey {
	var param strings.Builder
	for {
		c, err := e.in.ReadByte()
		if err != nil {
			return keyNone
		}
		switch {
		case c >= 0x30 && c <= 0x3f:
			param.WriteByte(c)
		case c >= 0x20 && c <= 0x2f:
		case c >= 0x40 && c <= 0x7e:
			return csiKey(param.String(), c)
		default:
			return keyNone
		}
	}
}

// csiKey maps a CSI parameter and final byte to an editor key. Arrow keys
// with a modifier parameter (1;5 is Ctrl+arrow in xterm) move by word.
func csiKey(param string, final byte) escKey {
	switch final {
	case 'A':
		return keyUp
	case 'B':
		return keyDown
	case 'C':
		if isModified(param) {
			return keyWordRight
		}
		return keyRight
	case 'D':
		if isModified(param) {
			return keyWordLeft
		}
		return keyLeft
	case 'Z': // Shift-Tab
		return keyBackTab
	case 'H':
		return keyHome
	case 'F':
		return keyEnd
	case '~':
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
}

// isModified reports whether a CSI parameter carries a modifier other than
// the plain "1", as in "1;5" for Ctrl+arrow.
func isModified(param string) bool {
	_, mod, ok := strings.Cut(param, ";")
	return ok && mod != "" && mod != "1"
}
