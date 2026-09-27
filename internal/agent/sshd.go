package agent

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// sshdConfigPath is the sshd config box join edits when it can write it.
var sshdConfigPath = "/etc/ssh/sshd_config"

const (
	boxAuthorizedKeys = ".ssh/box_authorized_keys"
	sshdKeysLine      = "AuthorizedKeysFile .ssh/authorized_keys .ssh/box_authorized_keys"
)

// ConfigureSSHD makes sshd read the managed keys file and apply environment=
// options. It prints every change. Join still succeeds when the file is not writable.
func ConfigureSSHD(out io.Writer) {
	ensureSSHD(out)
}

// ensureSSHD adds the managed keys file and PermitUserEnvironment when sshd_config
// is writable. OpenSSH ignores environment= on a key unless that setting is on.
// Otherwise it tells the operator the lines to add. Join still succeeds.
func ensureSSHD(out io.Writer) {
	if out == nil {
		out = io.Discard
	}
	notes, warning, err := updateSSHD(sshdConfigPath)
	if err != nil {
		if body, rerr := os.ReadFile(sshdConfigPath); rerr == nil {
			_, edit := prepareSSHD(string(body))
			if !edit.changed && edit.warning == "" {
				return
			}
			if !edit.changed && edit.warning != "" {
				fmt.Fprintln(out, edit.warning)
				return
			}
		}
		fmt.Fprintf(out, "sshd_config is not writable. Add these lines, then reload sshd:\n  %s\n  PermitUserEnvironment yes\n  sudo systemctl reload ssh\n", sshdKeysLine)
		return
	}
	if warning != "" {
		fmt.Fprintln(out, warning)
	}
	if len(notes) == 0 {
		return
	}
	fmt.Fprintf(out, "updated %s:\n", sshdConfigPath)
	for _, note := range notes {
		fmt.Fprintf(out, "  %s\n", note)
	}
	if reloadSSHD() {
		fmt.Fprintln(out, "reloaded sshd")
		return
	}
	fmt.Fprintln(out, "reload sshd: sudo systemctl reload ssh")
}

func updateSSHD(path string) ([]string, string, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, "", err
	}
	body, err := os.ReadFile(path)
	if err != nil {
		return nil, "", err
	}
	next, edit := prepareSSHD(string(body))
	if !edit.changed {
		return nil, edit.warning, nil
	}
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".sshd-config-*")
	if err != nil {
		return nil, "", err
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
		return nil, "", err
	}
	if err := tmp.Chmod(info.Mode().Perm()); err != nil {
		_ = tmp.Close()
		return nil, "", err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return nil, "", err
	}
	if err := tmp.Close(); err != nil {
		return nil, "", err
	}
	if err := os.Rename(tmpName, path); err != nil {
		return nil, "", err
	}
	ok = true
	if d, err := os.Open(dir); err == nil {
		_ = d.Sync()
		_ = d.Close()
	}
	return edit.notes, edit.warning, nil
}

type sshdEdit struct {
	notes   []string
	warning string
	changed bool
}

func prepareSSHD(content string) (string, sshdEdit) {
	next, keysChanged := addBoxAuthorizedKeys(content)
	var edit sshdEdit
	if keysChanged {
		edit.notes = append(edit.notes, "AuthorizedKeysFile includes "+boxAuthorizedKeys)
		edit.changed = true
	}
	var envChanged bool
	next, envChanged, edit.warning = setPermitUserEnvironment(next)
	if envChanged {
		edit.notes = append(edit.notes, "PermitUserEnvironment yes")
		edit.changed = true
	}
	return next, edit
}

// setPermitUserEnvironment turns the option on when it is missing or no.
// A pattern the operator already chose is left in place.
func setPermitUserEnvironment(content string) (string, bool, string) {
	lines := strings.Split(content, "\n")
	firstMatch := -1
	target := -1
	args := ""
	for i, line := range lines {
		key, a, ok := sshdKeyword(strings.TrimRight(line, "\r"))
		if !ok {
			continue
		}
		if strings.EqualFold(key, "Match") {
			if firstMatch < 0 {
				firstMatch = i
			}
			continue
		}
		if firstMatch >= 0 || !strings.EqualFold(key, "PermitUserEnvironment") {
			continue
		}
		if target >= 0 {
			continue
		}
		target = i
		args = a
	}
	if target >= 0 {
		switch strings.ToLower(strings.TrimSpace(args)) {
		case "yes", "*":
			return content, false, ""
		case "no", "":
			lines[target] = "PermitUserEnvironment yes"
			return strings.Join(lines, "\n"), true, ""
		default:
			return content, false, "PermitUserEnvironment is already " + args + "; env names must match it"
		}
	}
	line := "PermitUserEnvironment yes"
	if firstMatch >= 0 {
		var b strings.Builder
		for i, l := range lines {
			if i == firstMatch {
				b.WriteString(line)
				b.WriteByte('\n')
			}
			b.WriteString(l)
			if i != len(lines)-1 {
				b.WriteByte('\n')
			}
		}
		return b.String(), true, ""
	}
	base := content
	if base != "" && !strings.HasSuffix(base, "\n") {
		base += "\n"
	}
	return base + line + "\n", true, ""
}

func reloadSSHD() bool {
	for _, name := range []string{"ssh", "sshd"} {
		if exec.Command("systemctl", "reload", name).Run() == nil {
			return true
		}
	}
	return false
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
