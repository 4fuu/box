package repl

import (
	"bufio"
	"io"
	"strings"
)

// ReadSecret reads a pairing password. raw is set when the peer allocated a
// PTY: the password is not echoed. The line editor is not used here; the
// interactive session after a successful bind is the TUI.
func ReadSecret(in *bufio.Reader, out io.Writer, raw bool) (string, error) {
	if !raw {
		line, err := in.ReadString('\n')
		return strings.TrimSpace(strings.TrimRight(line, "\r\n")), err
	}
	var buf []byte
	for {
		b, err := in.ReadByte()
		if err != nil {
			if len(buf) > 0 && err == io.EOF {
				return strings.TrimSpace(string(buf)), nil
			}
			return "", err
		}
		switch b {
		case '\r', '\n':
			_, _ = io.WriteString(out, "\r\n")
			return strings.TrimSpace(string(buf)), nil
		case 0x03: // ^C
			_, _ = io.WriteString(out, "\r\n")
			return "", io.EOF
		case 0x04: // ^D
			_, _ = io.WriteString(out, "\r\n")
			if len(buf) == 0 {
				return "", io.EOF
			}
			return strings.TrimSpace(string(buf)), nil
		case 0x08, 0x7f:
			buf = dropLastRune(buf)
		default:
			if b >= 0x20 {
				buf = append(buf, b)
			}
		}
	}
}

func dropLastRune(buf []byte) []byte {
	for len(buf) > 0 {
		last := buf[len(buf)-1]
		buf = buf[:len(buf)-1]
		if last&0xc0 != 0x80 {
			break
		}
	}
	return buf
}
