package agent

import (
	"fmt"
	"io"
	"os"
	"strings"
	"unicode"
)

// sshdConfigPath is the sshd config box join edits when it can write it.
var sshdConfigPath = "/etc/ssh/sshd_config"

const (
	boxAuthorizedKeys = ".ssh/box_authorized_keys"
	sshdKeysLine      = "AuthorizedKeysFile .ssh/authorized_keys .ssh/box_authorized_keys"
)

// ensureSSHD adds the managed keys file when sshd_config is writable.
// Otherwise it tells the operator the one line to add. Join still succeeds.
func ensureSSHD(out io.Writer) {
	if out == nil {
		out = io.Discard
	}
	if err := updateSSHD(sshdConfigPath); err != nil {
		fmt.Fprintf(out, "add to sshd_config: %s\n", sshdKeysLine)
	}
}

func updateSSHD(path string) error {
	f, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		return err
	}
	defer f.Close()
	body, err := io.ReadAll(f)
	if err != nil {
		return err
	}
	next, changed := addBoxAuthorizedKeys(string(body))
	if !changed {
		return nil
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return err
	}
	n, err := f.WriteString(next)
	if err != nil {
		return err
	}
	return f.Truncate(int64(n))
}

// addBoxAuthorizedKeys returns the file contents and whether they changed.
// Only the first global AuthorizedKeysFile counts; OpenSSH ignores later ones.
func addBoxAuthorizedKeys(content string) (string, bool) {
	lines := strings.Split(content, "\n")
	firstMatch := -1
	target := -1
	for i, line := range lines {
		key, args, ok := sshdKeyword(strings.TrimRight(line, "\r"))
		if !ok {
			continue
		}
		if strings.EqualFold(key, "Match") {
			if firstMatch < 0 {
				firstMatch = i
			}
			continue
		}
		if firstMatch >= 0 || !strings.EqualFold(key, "AuthorizedKeysFile") {
			continue
		}
		if target >= 0 {
			continue
		}
		target = i
		for _, field := range strings.Fields(args) {
			if field == boxAuthorizedKeys {
				return content, false
			}
		}
	}
	if target >= 0 {
		lines[target] = strings.TrimRight(lines[target], "\r \t") + " " + boxAuthorizedKeys
		return strings.Join(lines, "\n"), true
	}
	if firstMatch >= 0 {
		var b strings.Builder
		for i, line := range lines {
			if i == firstMatch {
				b.WriteString(sshdKeysLine)
				b.WriteByte('\n')
			}
			b.WriteString(line)
			if i != len(lines)-1 {
				b.WriteByte('\n')
			}
		}
		return b.String(), true
	}
	base := content
	if base != "" && !strings.HasSuffix(base, "\n") {
		base += "\n"
	}
	return base + sshdKeysLine + "\n", true
}

func sshdKeyword(line string) (key, args string, ok bool) {
	s := strings.TrimSpace(line)
	if s == "" || strings.HasPrefix(s, "#") {
		return "", "", false
	}
	i := strings.IndexFunc(s, unicode.IsSpace)
	if i < 0 {
		return s, "", true
	}
	return s[:i], strings.TrimSpace(s[i:]), true
}
