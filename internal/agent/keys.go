package agent

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
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

func writeAuthorizedKeys(keys []string, env map[string]string) error {
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
		formatted := formatKeyLine(line, env)
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

// formatKeyLine prefixes a key with comma-separated environment="K=V" options.
// A space between options ends the field, and a backslash inside the quotes
// consumes the closing quote, so those pairs are skipped.
func formatKeyLine(line string, env map[string]string) string {
	line = strings.TrimSpace(line)
	if line == "" || strings.ContainsAny(line, "\n\r") {
		return ""
	}
	opts := envOptions(env)
	if opts == "" {
		return line
	}
	if startsWithKeyType(line) {
		return opts + " " + line
	}
	return opts + "," + line
}

func envOptions(env map[string]string) string {
	names := make([]string, 0, len(env))
	for k, v := range env {
		if k == "" || strings.ContainsAny(k, "\"\\\n\r") || strings.ContainsAny(v, "\"\\\n\r") {
			continue
		}
		names = append(names, k)
	}
	sort.Strings(names)
	var b strings.Builder
	for i, k := range names {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(`environment="`)
		b.WriteString(k)
		b.WriteByte('=')
		b.WriteString(env[k])
		b.WriteByte('"')
	}
	return b.String()
}

func startsWithKeyType(line string) bool {
	field, _, _ := strings.Cut(line, " ")
	field, _, _ = strings.Cut(field, "\t")
	if field == "" || strings.Contains(field, "=") {
		return false
	}
	return strings.HasPrefix(field, "ssh-") ||
		strings.HasPrefix(field, "ecdsa-") ||
		strings.HasPrefix(field, "sk-")
}

type accessFile struct {
	Keys []string          `json:"authorized_keys"`
	Env  map[string]string `json:"env"`
}

func loadAccess(dir string) ([]string, map[string]string, error) {
	raw, err := os.ReadFile(filepath.Join(dir, accessFileName))
	if errors.Is(err, os.ErrNotExist) {
		return nil, map[string]string{}, nil
	}
	if err != nil {
		return nil, nil, err
	}
	var f accessFile
	if err := json.Unmarshal(raw, &f); err != nil {
		return nil, nil, err
	}
	if f.Env == nil {
		f.Env = map[string]string{}
	}
	return f.Keys, f.Env, nil
}

func saveAccess(dir string, keys []string, env map[string]string) error {
	if keys == nil {
		keys = []string{}
	}
	if env == nil {
		env = map[string]string{}
	}
	raw, err := json.MarshalIndent(accessFile{Keys: keys, Env: env}, "", "  ")
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
func (a *agent) commitAccess(keys []string, env map[string]string) error {
	if keys == nil {
		keys = []string{}
	}
	if env == nil {
		env = map[string]string{}
	}
	if err := writeAuthorizedKeys(keys, env); err != nil {
		return err
	}
	if err := saveAccess(a.dir, keys, env); err != nil {
		_ = writeAuthorizedKeys(a.keys, a.env)
		return err
	}
	a.keys = append([]string(nil), keys...)
	a.env = env
	return nil
}

func (a *agent) loadAndRewrite() error {
	keys, env, err := loadAccess(a.dir)
	if err != nil {
		return err
	}
	if env == nil {
		env = map[string]string{}
	}
	if err := writeAuthorizedKeys(keys, env); err != nil {
		return err
	}
	a.keys = append([]string(nil), keys...)
	a.env = env
	return nil
}
