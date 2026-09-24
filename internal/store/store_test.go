package store

import (
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
	if _, err := s.NewPairing(KindClient, pw, time.Minute); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), pw) {
		t.Fatal("password was written to sqlite")
	}
	if err := s.ConsumePairing(KindClient, pw); err != nil {
		t.Fatal(err)
	}
	if err := s.ConsumePairing(KindClient, pw); err != ErrUsed {
		t.Fatalf("second consume %v", err)
	}
}

func TestPairingExpiry(t *testing.T) {
	s := open(t)
	start := time.Date(2026, 9, 24, 12, 0, 0, 0, time.UTC)
	s.SetNow(func() time.Time { return start })
	pw := "abcd-efgh-ijkl-mnop-qrst"
	if _, err := s.NewPairing(KindClient, pw, time.Minute); err != nil {
		t.Fatal(err)
	}
	s.SetNow(func() time.Time { return start.Add(2 * time.Minute) })
	if err := s.ConsumePairing(KindClient, pw); err != ErrExpired {
		t.Fatalf("%v", err)
	}
	live, err := s.HasLivePairing(KindClient)
	if err != nil || live {
		t.Fatalf("live %v %v", live, err)
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
	joined := strings.Join(names, "\n")
	if strings.Contains(joined, sentinel) {
		t.Fatal("value leaked from EnvNames")
	}
	all, err := s.EnvAll()
	if err != nil || all["GH_TOKEN"] != sentinel {
		t.Fatalf("%v %v", all, err)
	}
	if err := s.DeleteEnv("GH_TOKEN"); err != nil {
		t.Fatal(err)
	}
}

func TestPortalHoldAndRename(t *testing.T) {
	s := open(t)
	if err := s.CreateNode("n1", "home", "tok-home"); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateNode("n2", "other", "tok-other"); err != nil {
		t.Fatal(err)
	}
	if err := s.AddImage("base", "registry.example/base:latest"); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateComputer(Computer{Name: "web", NodeID: "n1", Image: "base", CPU: 2, Memory: 1, Disk: 1, State: StateRunning}); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateComputer(Computer{Name: "api", NodeID: "n1", Image: "base", CPU: 1, Memory: 1, Disk: 1, State: StateRunning}); err != nil {
		t.Fatal(err)
	}
	p := Portal{Hostname: "web.box.example.com", Label: "web", Container: "web", NodeID: "n1", Port: 3000}
	if err := s.ClaimPortal(p); err != nil {
		t.Fatal(err)
	}
	p.Port = 3001
	if err := s.ClaimPortal(p); err != nil {
		t.Fatal(err)
	}
	got, err := s.PortalByHost(p.Hostname)
	if err != nil || got.Port != 3001 || got.Container != "web" {
		t.Fatalf("%+v %v", got, err)
	}
	err = s.ClaimPortal(Portal{Hostname: p.Hostname, Label: "web", Container: "api", NodeID: "n1", Port: 9})
	held, ok := err.(*HeldError)
	if !ok || held.Holder != "web" {
		t.Fatalf("%v", err)
	}
	if err := s.RenameComputer("web", "site"); err != nil {
		t.Fatal(err)
	}
	got, err = s.PortalByHost(p.Hostname)
	if err != nil || got.Container != "site" || got.Hostname != p.Hostname {
		t.Fatalf("%+v %v", got, err)
	}
	if err := s.DeleteComputer("site"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.PortalByHost(p.Hostname); err != ErrNotFound {
		t.Fatalf("portal remained: %v", err)
	}
}

func TestNodeDeleteRefusedWhileBusy(t *testing.T) {
	s := open(t)
	if err := s.CreateNode("n1", "home", "tok"); err != nil {
		t.Fatal(err)
	}
	if err := s.AddImage("base", "ref"); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateComputer(Computer{Name: "web", NodeID: "n1", Image: "base", CPU: 1, Memory: 1, Disk: 1, State: StateRunning}); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteNode("n1"); err != ErrInUse {
		t.Fatalf("%v", err)
	}
	if err := s.DeleteImage("base"); err != ErrInUse {
		t.Fatalf("image %v", err)
	}
}

func TestNodeTokenLookupDoesNotUseName(t *testing.T) {
	s := open(t)
	if err := s.CreateNode("n1", "home", "tok-1"); err != nil {
		t.Fatal(err)
	}
	n, err := s.NodeByToken("tok-1")
	if err != nil || n.Name != "home" {
		t.Fatalf("%+v %v", n, err)
	}
	if _, err := s.NodeByToken("nope"); err != ErrNotFound {
		t.Fatalf("%v", err)
	}
	tok, err := s.NodeToken("n1")
	if err != nil || tok != "tok-1" {
		t.Fatalf("%s %v", tok, err)
	}
	nodes, err := s.ListNodes()
	if err != nil {
		t.Fatal(err)
	}
	// List path does not select the token column into Node.
	if len(nodes) != 1 || nodes[0].Name != "home" {
		t.Fatalf("%+v", nodes)
	}
}

func TestHeartbeatImages(t *testing.T) {
	s := open(t)
	if err := s.CreateNode("n1", "home", "tok"); err != nil {
		t.Fatal(err)
	}
	if err := s.AddImage("base", "ref"); err != nil {
		t.Fatal(err)
	}
	if err := s.SetDefaultImage("base"); err != nil {
		t.Fatal(err)
	}
	def, err := s.DefaultImage()
	if err != nil || !def.Default || def.Name != "base" {
		t.Fatalf("%+v %v", def, err)
	}
	if err := s.Heartbeat("n1", Heartbeat{CPU: 4, Memory: 8, Disk: 9, Images: []string{"base"}}); err != nil {
		t.Fatal(err)
	}
	n, err := s.NodeByName("home")
	if err != nil {
		t.Fatal(err)
	}
	if n.CPU != 4 || len(n.Images) != 1 || n.Images[0] != "base" || n.LastHeartbeat.IsZero() {
		t.Fatalf("%+v", n)
	}
}

func TestKeyRemove(t *testing.T) {
	s := open(t)
	if err := s.BindKey("ssh-ed25519 AAAA box", "laptop", "SHA256:abc"); err != nil {
		t.Fatal(err)
	}
	k, err := s.FindKeyByFingerprint("laptop")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.RemoveKey(k.ID); err != nil {
		t.Fatal(err)
	}
	keys, err := s.ListKeys()
	if err != nil || len(keys) != 0 {
		t.Fatalf("%v %v", keys, err)
	}
}
