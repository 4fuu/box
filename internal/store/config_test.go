package store

import (
	"testing"
	"time"

	"github.com/4fuu/box/internal/secret"
)

func TestConfigSnapshotReplicates(t *testing.T) {
	ctx := t.Context()
	writer, replica := open(t), open(t)
	if err := writer.BindKey("ssh-ed25519 AAAA laptop", "laptop", "fp-1"); err != nil {
		t.Fatal(err)
	}
	if err := writer.CreateComputer("home", secret.Hash("computer-token"), "alice"); err != nil {
		t.Fatal(err)
	}
	if err := writer.ClaimPortal("web.example.com", "home", 3000, false); err != nil {
		t.Fatal(err)
	}
	tok, err := writer.AddToken("access-token", "ci", time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	if err := writer.SetEnv("GH_TOKEN", "secret-value"); err != nil {
		t.Fatal(err)
	}

	rev, err := writer.BumpConfigRevision(ctx)
	if err != nil || rev != 1 {
		t.Fatalf("revision = %d, %v", rev, err)
	}
	snap, err := writer.ConfigSnapshot(ctx, rev)
	if err != nil {
		t.Fatal(err)
	}
	applied, err := replica.ApplyConfigSnapshot(ctx, snap)
	if err != nil || !applied {
		t.Fatalf("apply = %v, %v", applied, err)
	}

	// The replica can authenticate the computer and the access token.
	if ok, err := replica.TokenMatches("home", "computer-token"); err != nil || !ok {
		t.Fatalf("computer token = %v, %v", ok, err)
	}
	if _, ok, err := replica.ValidToken("access-token"); err != nil || !ok {
		t.Fatalf("access token = %v, %v", ok, err)
	}
	if keys, err := replica.ListKeys(); err != nil || len(keys) != 1 {
		t.Fatalf("keys = %d, %v", len(keys), err)
	}
	if env, err := replica.EnvAll(); err != nil || env["GH_TOKEN"] != "secret-value" {
		t.Fatalf("env = %v, %v", env, err)
	}
	if p, err := replica.PortalByHost("web.example.com"); err != nil || p.Port != 3000 {
		t.Fatalf("portal = %+v, %v", p, err)
	}

	// The sshd host key belongs to the node that saw the hello, not the writer.
	if err := replica.SetComputerInfo("home", "", "ssh-ed25519 AAAA host"); err != nil {
		t.Fatal(err)
	}

	// A newer snapshot replaces removals and updates, and keeps that host key.
	if err := writer.DeleteToken(tok.ID); err != nil {
		t.Fatal(err)
	}
	if err := writer.ClaimPortal("web.example.com", "home", 4000, true); err != nil {
		t.Fatal(err)
	}
	if err := writer.DeleteEnv("GH_TOKEN"); err != nil {
		t.Fatal(err)
	}
	rev2, err := writer.BumpConfigRevision(ctx)
	if err != nil {
		t.Fatal(err)
	}
	snap2, err := writer.ConfigSnapshot(ctx, rev2)
	if err != nil {
		t.Fatal(err)
	}
	if applied, err := replica.ApplyConfigSnapshot(ctx, snap2); err != nil || !applied {
		t.Fatalf("apply newer = %v, %v", applied, err)
	}
	if _, ok, err := replica.ValidToken("access-token"); err != nil || ok {
		t.Fatalf("deleted token still valid: %v, %v", ok, err)
	}
	if p, err := replica.PortalByHost("web.example.com"); err != nil || p.Port != 4000 || !p.Private {
		t.Fatalf("updated portal = %+v, %v", p, err)
	}
	if names, err := replica.EnvNames(); err != nil || len(names) != 0 {
		t.Fatalf("env names = %v, %v", names, err)
	}
	if c, err := replica.Computer("home"); err != nil || c.HostKey != "ssh-ed25519 AAAA host" {
		t.Fatalf("host key = %+v, %v", c, err)
	}

	// Replaying an older snapshot must not resurrect the removed rows.
	if applied, err := replica.ApplyConfigSnapshot(ctx, snap); err != nil || applied {
		t.Fatalf("stale apply = %v, %v", applied, err)
	}
	if _, ok, err := replica.ValidToken("access-token"); err != nil || ok {
		t.Fatalf("stale snapshot resurrected a token: %v, %v", ok, err)
	}
	if p, err := replica.PortalByHost("web.example.com"); err != nil || p.Port != 4000 {
		t.Fatalf("stale snapshot reverted a portal: %+v, %v", p, err)
	}
}

func TestConfigSnapshotRejectsInvalid(t *testing.T) {
	ctx := t.Context()
	s := open(t)
	if _, err := s.ApplyConfigSnapshot(ctx, ConfigSnapshot{}); err == nil {
		t.Fatal("revision 0 was accepted")
	}
	bad := ConfigSnapshot{Revision: 1, Portals: []Portal{{Hostname: "web.example.com", Computer: "home", Port: 0}}}
	if _, err := s.ApplyConfigSnapshot(ctx, bad); err == nil {
		t.Fatal("portal with no port was accepted")
	}
}
