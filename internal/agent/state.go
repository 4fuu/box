package agent

import (
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
)

// computer is ~/.box/computer.json. SSH is the bootstrap address: the QUIC
// port can change, and SSH is how the agent learns the new endpoint.
type computer struct {
	Name        string `json:"name"`
	Token       string `json:"token"`
	QUIC        string `json:"quic"`
	Fingerprint string `json:"fingerprint"`
	Domain      string `json:"domain"`
	HTTPPort    int    `json:"http_port"`
	User        string `json:"user"`
	SSH         string `json:"ssh,omitempty"`
}

func (c computer) sshAddress() (string, error) {
	if strings.TrimSpace(c.SSH) != "" {
		return withPort(c.SSH)
	}
	host := c.QUIC
	if h, _, err := net.SplitHostPort(c.QUIC); err == nil {
		host = h
	}
	if host == "" {
		return "", errors.New("no server address")
	}
	return net.JoinHostPort(host, "22"), nil
}

func withPort(addr string) (string, error) {
	addr = strings.TrimSpace(addr)
	if addr == "" {
		return "", errors.New("empty address")
	}
	if _, _, err := net.SplitHostPort(addr); err == nil {
		return addr, nil
	}
	return net.JoinHostPort(addr, "22"), nil
}

func readComputer(dir string) (computer, error) {
	raw, err := os.ReadFile(filepath.Join(dir, "computer.json"))
	if err != nil {
		return computer{}, err
	}
	var c computer
	if err := json.Unmarshal(raw, &c); err != nil {
		return computer{}, err
	}
	if c.Name == "" || c.Token == "" {
		return computer{}, errors.New("computer.json missing name or token")
	}
	return c, nil
}

func writeComputer(dir string, c computer) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		return err
	}
	raw, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	raw = append(raw, '\n')
	final := filepath.Join(dir, "computer.json")
	tmp := final + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return err
	}
	if err := os.Chmod(tmp, 0o600); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, final)
}

// wipeState removes the computer's state. Root is refused so a bad path
// cannot delete the filesystem when a token is revoked.
func wipeState(dir string) error {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return err
	}
	if abs == string(filepath.Separator) {
		return errors.New("refuse to wipe state")
	}
	return os.RemoveAll(abs)
}
