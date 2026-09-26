package agent

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
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
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	body, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	next, changed := addBoxAuthorizedKeys(string(body))
	if !changed {
		return nil
	}
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".sshd-config-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	ok := false
	defer func() {
		if !ok {
			_ = os.Remove(tmpName)
		}
	}()
	if _, err := tmp.WriteString(next); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Chmod(info.Mode().Perm()); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpName, path); err != nil {
		return err
	}
	ok = true
	if d, err := os.Open(dir); err == nil {
		_ = d.Sync()
		_ = d.Close()
	}
	return nil
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
	i := strings.IndexAny(s, " \t=")
	if i <= 0 {
		if i < 0 {
			return s, "", true
		}
		return "", "", false
	}
	key = s[:i]
	rest := strings.TrimSpace(s[i:])
	if strings.HasPrefix(rest, "=") {
		rest = strings.TrimSpace(rest[1:])
	}
	return key, rest, true
}
