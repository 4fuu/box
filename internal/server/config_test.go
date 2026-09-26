package server

import (
	"path/filepath"
	"testing"

	"github.com/4fuu/box/internal/store"
)

func TestDefaultListenAddrsStick(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "box.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	sshAddr, httpAddr, quicAddr, err := persistedAddrs(st, "", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if sshAddr != ":22" || httpAddr != ":80" || quicAddr != ":7443" {
		t.Fatalf("defaults %s %s %s", sshAddr, httpAddr, quicAddr)
	}
	sshAddr, httpAddr, quicAddr, err = persistedAddrs(st, "127.0.0.1:1", "127.0.0.1:2", "127.0.0.1:3")
	if err != nil {
		t.Fatal(err)
	}
	if sshAddr != ":22" || httpAddr != ":80" || quicAddr != ":7443" {
		t.Fatalf("flags replaced stored addrs: %s %s %s", sshAddr, httpAddr, quicAddr)
	}
	domain, err := persistedDomain(st, "Box.Example.com.")
	if err != nil || domain != "box.example.com" {
		t.Fatalf("domain %s %v", domain, err)
	}
	domain, err = persistedDomain(st, "other.example")
	if err != nil || domain != "box.example.com" {
		t.Fatalf("domain changed to %s %v", domain, err)
	}
}
