package agent

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// sshdConfigPath is the sshd config box join edits when it can write it.
var sshdConfigPath = sshdPlatform.config

// sshdBehavior is what differs per OS: where sshd's config lives, how to
// reload it, and what to tell the operator when box cannot do it for them.
type sshdBehavior struct {
	config    string                     // default sshd_config
	manual    string                     // full text when the config is not writable
	reloadTip string                     // printed when a reload attempt fails
	reloaded  string                     // printed after a successful reload
	reload    func() bool                // ask sshd to pick the config up again
	missing   func() string              // full text when the config is absent; "" falls through to manual
	adminNote func(config string) string // extra key-diversion warning; "" for none
}

const (
	boxAuthorizedKeys = ".ssh/box_authorized_keys"
	sshdKeysLine      = "AuthorizedKeysFile .ssh/authorized_keys .ssh/box_authorized_keys"
)

// ConfigureSSHD makes sshd read the managed keys file and accept the env
// requests the server sends with each splice session. It prints every
// change. Join still succeeds when the file is not writable.
func ConfigureSSHD(out io.Writer) {
	ensureSSHD(out)
}

// ensureSSHD adds the managed keys file and AcceptEnv when sshd_config is
// writable. OpenSSH drops per-session env requests unless the name matches
// AcceptEnv. Otherwise it tells the operator the lines to add. Join still
// succeeds.
func ensureSSHD(out io.Writer) {
	if out == nil {
		out = io.Discard
	}
	if hint := sshdPlatform.missing(); hint != "" {
		if _, err := os.Stat(sshdConfigPath); errors.Is(err, os.ErrNotExist) {
			fmt.Fprint(out, hint)
			return
		}
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
		fmt.Fprint(out, sshdPlatform.manual)
		if note := sshdPlatform.adminNote(sshdConfigPath); note != "" {
			fmt.Fprint(out, note)
		}
		return
	}
	if warning != "" {
		fmt.Fprintln(out, warning)
	}
	if note := sshdPlatform.adminNote(sshdConfigPath); note != "" {
		fmt.Fprint(out, note)
	}
	if len(notes) == 0 {
		return
	}
	fmt.Fprintf(out, "updated %s:\n", sshdConfigPath)
	for _, note := range notes {
		fmt.Fprintf(out, "  %s\n", note)
	}
	if sshdPlatform.reload() {
		fmt.Fprintln(out, sshdPlatform.reloaded)
		return
	}
	fmt.Fprintln(out, sshdPlatform.reloadTip)
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
	next, envChanged, edit.warning = ensureAcceptEnv(next)
	if envChanged {
		edit.notes = append(edit.notes, "AcceptEnv *")
		edit.changed = true
	}
	return next, edit
}

// ensureAcceptEnv adds "AcceptEnv *" so sshd accepts the env requests the
// server sends with each splice session. AcceptEnv lines the operator
// already set are left in place; box env names must match them. sshd
// combines every AcceptEnv line, so a "*" anywhere satisfies box.
func ensureAcceptEnv(content string) (string, bool, string) {
	lines := strings.Split(content, "\n")
	firstMatch := -1
	first := -1
	var args []string
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
		if firstMatch >= 0 || !strings.EqualFold(key, "AcceptEnv") {
			continue
		}
		if first < 0 {
			first = i
			args = strings.Fields(a)
		}
		for _, field := range strings.Fields(a) {
			if field == "*" {
				return content, false, ""
			}
		}
	}
	if first >= 0 {
		if len(args) == 0 {
			lines[first] = "AcceptEnv *"
			return strings.Join(lines, "\n"), true, ""
		}
		return content, false, "AcceptEnv is already " + strings.Join(args, " ") + "; box env names must match it"
	}
	line := "AcceptEnv *"
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
