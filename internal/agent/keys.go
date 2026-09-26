package agent

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
)

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
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(b.String()), 0o600); err != nil {
		return err
	}
	if err := os.Chmod(tmp, 0o600); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, path)
}

// formatKeyLine prefixes a raw OpenSSH line with environment="K=V" options.
// Names or values with a quote, newline, or CR are skipped so they cannot
// break out of the option.
func formatKeyLine(line string, env map[string]string) string {
	line = strings.TrimRight(line, "\r\n")
	if strings.TrimSpace(line) == "" || strings.ContainsAny(line, "\n\r") {
		return ""
	}
	names := make([]string, 0, len(env))
	for k, v := range env {
		if k == "" || strings.ContainsAny(k, "\"\n\r") || strings.ContainsAny(v, "\"\n\r") {
			continue
		}
		names = append(names, k)
	}
	sort.Strings(names)
	var b strings.Builder
	for _, k := range names {
		if b.Len() > 0 {
			b.WriteByte(' ')
		}
		b.WriteString(`environment="`)
		b.WriteString(k)
		b.WriteByte('=')
		b.WriteString(env[k])
		b.WriteByte('"')
	}
	if b.Len() > 0 {
		b.WriteByte(' ')
	}
	b.WriteString(line)
	return b.String()
}

func copyEnv(in map[string]string) map[string]string {
	out := make(map[string]string, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}
