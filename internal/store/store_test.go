package store

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/4fuu/box/internal/secret"
)

func open(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "box.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestPermissionsAndPasswordNotStored(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "box.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Fatalf("db mode %o", fi.Mode().Perm())
	}
	df, err := os.Stat(dir)
	if err != nil {
		t.Fatal(err)
	}
	if df.Mode().Perm() != 0o700 {
		t.Fatalf("dir mode %o", df.Mode().Perm())
	}
	pw, err := secret.ClientPassword()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.NewPairing(pw, time.Minute); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), pw) {
		t.Fatal("password was written to sqlite")
	}
	if strings.Contains(string(raw), "kind") && strings.Contains(string(raw), "nodes") {
		t.Fatal("old schema leaked into a new database")
	}
	if err := s.ConsumePairing(pw); err != nil {
		t.Fatal(err)
	}
	if err := s.ConsumePairing(pw); err != ErrUsed {
		t.Fatalf("second consume %v", err)
	}
}

func TestPairingExpiry(t *testing.T) {
	s := open(t)
	start := time.Date(2026, 9, 24, 12, 0, 0, 0, time.UTC)
	s.SetNow(func() time.Time { return start })
	pw := "abcd-efgh-ijkl-mnop-qrst"
	if _, err := s.NewPairing(pw, time.Minute); err != nil {
		t.Fatal(err)
	}
	s.SetNow(func() time.Time { return start.Add(2 * time.Minute) })
	if err := s.ConsumePairing(pw); err != ErrExpired {
		t.Fatalf("%v", err)
	}
	live, err := s.HasLivePairing()
	if err != nil || live {
		t.Fatalf("live %v %v", live, err)
	}
}

func TestPairingAttemptsBurnTheOnlyPassword(t *testing.T) {
	s := open(t)
	pw := "abcd-efgh-ijkl-mnop-qrst"
	if _, err := s.NewPairing(pw, time.Minute); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 4; i++ {
		if err := s.ConsumePairing("nope-nope-nope-nope-nope"); err != ErrNotFound {
			t.Fatalf("attempt %d: %v", i, err)
		}
		live, err := s.HasLivePairing()
		if err != nil || !live {
			t.Fatalf("still live after %d: %v %v", i, live, err)
		}
	}
	if err := s.ConsumePairing("nope-nope-nope-nope-nope"); err != ErrNotFound {
		t.Fatal(err)
	}
	live, err := s.HasLivePairing()
	if err != nil || live {
		t.Fatalf("burned password still live: %v %v", live, err)
	}
	if err := s.ConsumePairing(pw); err != ErrUsed {
		t.Fatalf("burned password: %v", err)
	}
}

func TestPairingAttemptsDoNotBurnSeveral(t *testing.T) {
	s := open(t)
	a := "abcd-efgh-ijkl-mnop-qrst"
	b := "qrst-mnop-ijkl-efgh-abcd"
	if _, err := s.NewPairing(a, time.Minute); err != nil {
		t.Fatal(err)
	}
	if _, err := s.NewPairing(b, time.Minute); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 6; i++ {
		if err := s.ConsumePairing("xxxx-xxxx-xxxx-xxxx-xxxx"); err != ErrNotFound {
			t.Fatal(err)
		}
	}
	if err := s.ConsumePairing(a); err != nil {
		t.Fatal(err)
	}
	if err := s.ConsumePairing(b); err != nil {
		t.Fatal(err)
	}
}

func TestEnvNamesHideValues(t *testing.T) {
	s := open(t)
	const sentinel = "ghp_super_secret_value"
	if err := s.SetEnv("GH_TOKEN", sentinel); err != nil {
		t.Fatal(err)
	}
	names, err := s.EnvNames()
	if err != nil {
		t.Fatal(err)
	}
	if len(names) != 1 || names[0] != "GH_TOKEN" {
		t.Fatalf("%v", names)
	}
	if strings.Contains(strings.Join(names, "\n"), sentinel) {
		t.Fatal("value leaked from EnvNames")
	}
	all, err := s.EnvAll()
	if err != nil || all["GH_TOKEN"] != sentinel {
		t.Fatalf("env all %v %v", all, err)
	}
	if err := s.DeleteEnv("GH_TOKEN"); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteEnv("GH_TOKEN"); err != ErrNotFound {
		t.Fatalf("%v", err)
	}
}

func TestComputerTokenAndPortals(t *testing.T) {
	s := open(t)
	const token = "raw-token-not-stored"
	if err := s.CreateComputer("home", secret.Hash(token), ""); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(s.path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), token) {
		t.Fatal("raw token was stored")
	}
	ok, err := s.TokenMatches("home", token)
	if err != nil || !ok {
		t.Fatalf("match %v %v", ok, err)
	}
	ok, err = s.TokenMatches("home", "other")
	if err != nil || ok {
		t.Fatalf("bad token %v %v", ok, err)
	}
	ok, err = s.TokenMatches("gone", token)
	if err != nil || ok {
		t.Fatalf("missing computer %v %v", ok, err)
	}
	if err := s.SetComputerInfo("home", "alice", "ssh-ed25519 AAAA"); err != nil {
		t.Fatal(err)
	}
	if err := s.SetComputerInfo("home", "", "ssh-ed25519 BBBB"); err != nil {
		t.Fatal(err)
	}
	c, err := s.Computer("home")
	if err != nil {
		t.Fatal(err)
	}
	if c.LoginUser != "alice" || c.HostKey != "ssh-ed25519 BBBB" {
		t.Fatalf("info %+v", c)
	}
	if err := s.ClaimPortal("web.example.com", "home", 3000); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateComputer("other", secret.Hash("tok2"), "bob"); err != nil {
		t.Fatal(err)
	}
	err = s.ClaimPortal("web.example.com", "other", 9)
	var held *HeldError
	if !errors.As(err, &held) || held.Holder != "home" {
		t.Fatalf("held %v", err)
	}
	if err := s.ClaimPortal("web.example.com", "home", 4000); err != nil {
		t.Fatal(err)
	}
	p, err := s.PortalByHost("web.example.com")
	if err != nil || p.Port != 4000 || p.Computer != "home" {
		t.Fatalf("%+v %v", p, err)
	}
	if err := s.RenameComputer("home", "house"); err != nil {
		t.Fatal(err)
	}
	p, err = s.PortalByHost("web.example.com")
	if err != nil || p.Computer != "house" {
		t.Fatalf("rename portal %+v %v", p, err)
	}
	ok, err = s.TokenMatches("house", token)
	if err != nil || !ok {
		t.Fatal("token should follow the rename")
	}
	if err := s.ReleasePortal("web.example.com", "other"); err != ErrNotFound {
		t.Fatalf("release other %v", err)
	}
	if err := s.DeleteComputer("house"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.PortalByHost("web.example.com"); err != ErrNotFound {
		t.Fatalf("portal survived delete %v", err)
	}
	if _, err := s.Computer("house"); err != ErrNotFound {
		t.Fatal(err)
	}
	ok, err = s.TokenMatches("house", token)
	if err != nil || ok {
		t.Fatal("revoked token still matched")
	}
}

func TestKeyRemove(t *testing.T) {
	s := open(t)
	if err := s.BindKey("ssh-ed25519 AAAA laptop", "laptop", "SHA256:abc"); err != nil {
		t.Fatal(err)
	}
	k, err := s.FindKeyByFingerprint("SHA256:abc")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.RemoveKey(k.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.FindKeyByFingerprint("SHA256:abc"); err != ErrNotFound {
		t.Fatal(err)
	}
	if err := s.RemoveKey(k.ID); err != ErrNotFound {
		t.Fatalf("%v", err)
	}
}

func TestMetaRoundTrip(t *testing.T) {
	s := open(t)
	if _, ok, err := s.Meta("domain"); err != nil || ok {
		t.Fatalf("missing %v %v", ok, err)
	}
	if err := s.SetMeta("ssh_addr", ":22"); err != nil {
		t.Fatal(err)
	}
	if err := s.SetMeta("ssh_addr", "127.0.0.1:22"); err != nil {
		t.Fatal(err)
	}
	v, ok, err := s.Meta("ssh_addr")
	if err != nil || !ok || v != "127.0.0.1:22" {
		t.Fatalf("%q %v %v", v, ok, err)
	}
}
