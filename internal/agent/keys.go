package agent

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
)

const accessFileName = "access.json"

func authorizedKeysPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	if home == "" {
		return "", os.ErrNotExist
	}
	return filepath.Join(home, ".ssh", "box_authorized_keys"), nil
}

func writeAuthorizedKeys(keys []string) error {
	path, err := authorizedKeysPath()
	if err != nil {
		return err
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		return err
	}
	var b strings.Builder
	for _, line := range keys {
		formatted := formatKeyLine(line)
		if formatted == "" {
			continue
		}
		b.WriteString(formatted)
		b.WriteByte('\n')
	}
	// Two agents in one process share this path. A fixed temp name lets one
	// unlink the file the other is about to rename.
	f, err := os.CreateTemp(dir, ".box_authorized_keys-*")
	if err != nil {
		return err
	}
	tmpName := f.Name()
	ok := false
	defer func() {
		if !ok {
			_ = os.Remove(tmpName)
		}
	}()
	if err := f.Chmod(0o600); err != nil {
		_ = f.Close()
		return err
	}
	if _, err := f.WriteString(b.String()); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpName, path); err != nil {
		return err
	}
	ok = true
	return nil
}

// formatKeyLine returns the trimmed key line, or empty when the line is
// blank or spans several lines.
func formatKeyLine(line string) string {
	line = strings.TrimSpace(line)
	if line == "" || strings.ContainsAny(line, "\n\r") {
		return ""
	}
	return line
}

// accessFile holds the authorized keys the agent persists for sshd. Env
// values are deliberately absent: they live on the server and arrive per
// splice session, never on the computer's disk. Old files that still carry
// an "env" field lose it on the next save.
type accessFile struct {
	Keys []string `json:"authorized_keys"`
}

func loadAccess(dir string) ([]string, error) {
	raw, err := os.ReadFile(filepath.Join(dir, accessFileName))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var f accessFile
	if err := json.Unmarshal(raw, &f); err != nil {
		return nil, err
	}
	return f.Keys, nil
}

func saveAccess(dir string, keys []string) error {
	if keys == nil {
		keys = []string{}
	}
	raw, err := json.MarshalIndent(accessFile{Keys: keys}, "", "  ")
	if err != nil {
		return err
	}
	raw = append(raw, '\n')
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	final := filepath.Join(dir, accessFileName)
	tmp := final + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return err
	}
	if err := os.Chmod(tmp, 0o600); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	f, err := os.Open(tmp)
	if err != nil {
		_ = os.Remove(tmp)
		return err
	}
	err = f.Sync()
	_ = f.Close()
	if err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, final)
}

// commitAccess rewrites the authorized keys and the state file, then memory.
// A failed write leaves memory matching the previous disk state.
func (a *agent) commitAccess(keys []string) error {
	if keys == nil {
		keys = []string{}
	}
	if err := writeAuthorizedKeys(keys); err != nil {
		return err
	}
	if err := saveAccess(a.dir, keys); err != nil {
		_ = writeAuthorizedKeys(a.keys)
		return err
	}
	a.keys = append([]string(nil), keys...)
	return nil
}

func (a *agent) loadAndRewrite() error {
	keys, err := loadAccess(a.dir)
	if err != nil {
		return err
	}
	if err := writeAuthorizedKeys(keys); err != nil {
		return err
	}
	// Rewrite the state file too, so a leftover "env" field from an older
	// box disappears from disk on first boot after the upgrade.
	if err := saveAccess(a.dir, keys); err != nil {
		return err
	}
	a.keys = append([]string(nil), keys...)
	return nil
}
