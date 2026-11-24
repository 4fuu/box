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
	domains, err := persistedDomains(st, []string{"Box.Example.com.", "second.example"})
	if err != nil || len(domains) != 2 || domains[0] != "box.example.com" || domains[1] != "second.example" {
		t.Fatalf("domains %v %v", domains, err)
	}
	domains, err = persistedDomains(st, []string{"box.example.com", "third.example"})
	if err != nil || len(domains) != 3 || domains[0] != "box.example.com" || domains[2] != "third.example" {
		t.Fatalf("domains changed or were not appended: %v %v", domains, err)
	}
	domains, err = persistedDomains(st, nil)
	if err != nil || len(domains) != 3 {
		t.Fatalf("stored domains not reused: %v %v", domains, err)
	}
}

func TestStoredDomainFallback(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "box.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err := st.SetMeta("domain", "old.example"); err != nil {
		t.Fatal(err)
	}
	domains, err := persistedDomains(st, nil)
	if err != nil || len(domains) != 1 || domains[0] != "old.example" {
		t.Fatalf("legacy domain %v %v", domains, err)
	}
}
