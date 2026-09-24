// Package keys generates ed25519 keys and prints OpenSSH public lines.
package keys

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/subtle"
	"encoding/pem"
	"errors"
	"os"
	"strings"

	"golang.org/x/crypto/ssh"
)

// Generate writes an ed25519 private key and returns the public line.
// The private key is mode 0600. An existing file is reused.
func Generate(path, comment string) (string, error) {
	if _, err := os.Stat(path); err == nil {
		return PublicLine(path)
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	if err := os.MkdirAll(dirOf(path), 0o700); err != nil {
		return "", err
	}
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return "", err
	}
	block, err := ssh.MarshalPrivateKey(priv, comment)
	if err != nil {
		return "", err
	}
	signer, err := ssh.NewSignerFromKey(priv)
	if err != nil {
		return "", err
	}
	line := Line(signer.PublicKey(), comment) + "\n"
	if err := os.WriteFile(path, pem.EncodeToMemory(block), 0o600); err != nil {
		return "", err
	}
	if err := os.WriteFile(path+".pub", []byte(line), 0o644); err != nil {
		return "", err
	}
	return line, nil
}

// PublicLine reads a private key and returns its OpenSSH public line.
// The line ends with a newline and includes the comment.
func PublicLine(path string) (string, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	signer, err := ssh.ParsePrivateKey(raw)
	if err != nil {
		return "", err
	}
	// ParsePrivateKey drops the comment. Prefer the .pub file when it exists
	// so key copy prints the same line that was generated.
	if pub, err := os.ReadFile(path + ".pub"); err == nil && len(pub) > 0 {
		return ensureNL(string(pub)), nil
	}
	return string(ssh.MarshalAuthorizedKey(signer.PublicKey())), nil
}

// Signer loads a private key.
func Signer(path string) (ssh.Signer, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return ssh.ParsePrivateKey(raw)
}

// Parse splits an authorized_keys line into a key and its comment.
func Parse(line string) (ssh.PublicKey, string, error) {
	line = strings.TrimSpace(line)
	if line == "" {
		return nil, "", errors.New("empty key")
	}
	pub, comment, _, _, err := ssh.ParseAuthorizedKey([]byte(line))
	if err != nil {
		return nil, "", err
	}
	return pub, comment, nil
}

// Line formats a public key the way authorized_keys expects, without a trailing newline.
func Line(pub ssh.PublicKey, comment string) string {
	s := strings.TrimSpace(string(ssh.MarshalAuthorizedKey(pub)))
	if comment == "" {
		return s
	}
	// MarshalAuthorizedKey already appends the comment when the key carries one.
	// Rebuild so the stored comment wins.
	parts := strings.Fields(s)
	if len(parts) < 2 {
		return s
	}
	return parts[0] + " " + parts[1] + " " + comment
}

// Fingerprint is the OpenSSH SHA256 fingerprint.
func Fingerprint(pub ssh.PublicKey) string {
	return ssh.FingerprintSHA256(pub)
}

func ensureNL(s string) string {
	s = strings.TrimRight(s, "\n")
	return s + "\n"
}

func dirOf(path string) string {
	i := strings.LastIndex(path, "/")
	if i <= 0 {
		return "."
	}
	return path[:i]
}

// Same reports whether two public lines are the same key.
func Same(a, b string) (bool, error) {
	ak, _, err := Parse(a)
	if err != nil {
		return false, err
	}
	bk, _, err := Parse(b)
	if err != nil {
		return false, err
	}
	ab, bb := ak.Marshal(), bk.Marshal()
	if len(ab) != len(bb) {
		return false, nil
	}
	return subtle.ConstantTimeCompare(ab, bb) == 1, nil
}

// GenerateSigner creates a key pair in memory for tests and clients.
func GenerateSigner(comment string) (ssh.Signer, string, error) {
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, "", err
	}
	signer, err := ssh.NewSignerFromKey(priv)
	if err != nil {
		return nil, "", err
	}
	return signer, Line(signer.PublicKey(), comment), nil
}
