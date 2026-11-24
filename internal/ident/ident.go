// Package ident checks names the design constrains.
package ident

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
)

var labelRe = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?$`)
var envRe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// Computer is an SSH username. box, pair, and join are reserved.
// '+' and '.' are reserved for pair+, join+, and hostnames.
func Computer(name string) error {
	if name == "box" || name == "pair" || name == "join" {
		return fmt.Errorf("name %s is reserved", name)
	}
	if strings.Contains(name, "+") || strings.Contains(name, ".") {
		return errors.New("invalid name")
	}
	if !labelRe.MatchString(name) {
		return errors.New("invalid name")
	}
	return nil
}

// Node is a deploy node name.
func Node(name string) error {
	if !labelRe.MatchString(name) {
		return errors.New("invalid name")
	}
	return nil
}

// Image is a registered image name.
func Image(name string) error {
	if !labelRe.MatchString(name) {
		return errors.New("invalid name")
	}
	return nil
}

// Label is a portal label. It cannot contain a dot, so it cannot escape the parent domain.
// event and auth are reserved for the server's own HTTP hosts.
func Label(name string) error {
	if name == "event" || name == "auth" {
		return fmt.Errorf("label %s is reserved", name)
	}
	if strings.Contains(name, ".") {
		return errors.New("invalid label")
	}
	if !labelRe.MatchString(name) {
		return errors.New("invalid label")
	}
	return nil
}

// Env is an environment variable name.
func Env(name string) error {
	if !envRe.MatchString(name) {
		return errors.New("invalid env name")
	}
	return nil
}

// Domain is one parent domain set when the server starts.
func Domain(d string) (string, error) {
	d = strings.ToLower(strings.TrimSpace(d))
	d = strings.TrimSuffix(d, ".")
	if d == "" || strings.ContainsAny(d, " /\\:@,") || strings.Contains(d, "..") {
		return "", errors.New("invalid domain")
	}
	return d, nil
}

// Domains normalizes parent domains in order. The first is the primary.
// Duplicates are dropped.
func Domains(list []string) ([]string, error) {
	out := make([]string, 0, len(list))
	seen := make(map[string]bool, len(list))
	for _, raw := range list {
		d, err := Domain(raw)
		if err != nil {
			return nil, err
		}
		if seen[d] {
			continue
		}
		seen[d] = true
		out = append(out, d)
	}
	return out, nil
}

// Hostname joins a label to a configured domain.
func Hostname(label, domain string) string {
	return label + "." + domain
}
