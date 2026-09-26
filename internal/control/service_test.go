package control

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/4fuu/box/internal/approve"
	"github.com/4fuu/box/internal/secret"
	"github.com/4fuu/box/internal/store"
	"github.com/4fuu/box/internal/tunnel"
)

func newSvc(t *testing.T) *Service {
	t.Helper()
	st, err := store.Open(t.TempDir() + "/box.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	return &Service{
		Store: st, Domain: "box.example.com", Queue: approve.New(), Live: NewLive(),
		SplicePublic: "ssh-ed25519 AAAASPLICE box-splice",
		HTTPPort:     func() int { return 80 },
	}
}

func TestPairingAndEnvHideValues(t *testing.T) {
	svc := newSvc(t)
	p, err := svc.PairClient()
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(p.Secret, "\n") || p.Secret == "" {
		t.Fatal(p.Secret)
	}
	if err := svc.ConsumeClient(p.Secret); err != nil {
		t.Fatal(err)
	}
	if err := svc.ConsumeClient(p.Secret); err == nil {
		t.Fatal("password worked twice")
	}
	const sentinel = "ghp_secret"
	msg, err := svc.SetEnv("GH_TOKEN", sentinel)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(msg, sentinel) {
		t.Fatal(msg)
	}
	names, err := svc.EnvNames()
	if err != nil || len(names) != 1 || names[0] != "GH_TOKEN" {
		t.Fatalf("%v %v", names, err)
	}
	text, err := FormatEnv(names, false)
	if err != nil || strings.Contains(text, sentinel) {
		t.Fatal(text)
	}
}

func TestAuthorizedLinesIncludeSpliceNotEnv(t *testing.T) {
	svc := newSvc(t)
	if err := svc.Store.BindKey("ssh-ed25519 AAAACLIENT laptop", "laptop", "SHA256:laptop"); err != nil {
		t.Fatal(err)
	}
	lines, err := svc.AuthorizedLines()
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(lines, "\n")
	if !strings.Contains(joined, "AAAACLIENT") || !strings.Contains(joined, "AAAASPLICE") {
		t.Fatalf("%v", lines)
	}
	if strings.Contains(joined, "environment=") {
		t.Fatal(joined)
	}
	keys, err := svc.Keys()
	if err != nil {
		t.Fatal(err)
	}
	for _, k := range keys {
		if strings.Contains(k.Comment, "splice") || strings.Contains(k.Fingerprint, "AAAASPLICE") {
			t.Fatal("splice key listed as a client key")
		}
	}
	if len(keys) != 1 || keys[0].Comment != "laptop" {
		t.Fatalf("%+v", keys)
	}
}

func TestPortalHoldRenameRemove(t *testing.T) {
	svc := newSvc(t)
	if err := svc.Store.CreateComputer("home", secret.Hash("tok"), "alice"); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.AddPortal("home", "web.other", 80); err == nil {
		t.Fatal("dot label")
	}
	res, err := svc.AddPortal("home", "web", 3000)
	if err != nil {
		t.Fatal(err)
	}
	if res.URL != "http://web.box.example.com" || res.Host != "web.box.example.com" || res.Port != 3000 {
		t.Fatalf("%+v", res)
	}
	svc.HTTPPort = func() int { return 8080 }
	res, err = svc.AddPortal("home", "api", 4000)
	if err != nil {
		t.Fatal(err)
	}
	if res.URL != "http://api.box.example.com:8080" {
		t.Fatal(res.URL)
	}
	if err := svc.Store.CreateComputer("other", secret.Hash("tok2"), "bob"); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.AddPortal("other", "web", 1); err == nil || !strings.Contains(err.Error(), "held by home") {
		t.Fatal(err)
	}
	check, err := svc.CheckPortal("web")
	if err != nil || check.Free || check.Holder != "home" {
		t.Fatalf("%+v %v", check, err)
	}
	if err := svc.Rename("home", "house"); err != nil {
		t.Fatal(err)
	}
	check, err = svc.CheckPortal("web")
	if err != nil || check.Holder != "house" {
		t.Fatalf("%+v %v", check, err)
	}
	list, err := svc.ListComputers()
	if err != nil || len(list) != 2 {
		t.Fatal(list, err)
	}
	var house ComputerView
	for _, c := range list {
		if c.Name == "house" {
			house = c
		}
	}
	if house.User != "alice" || house.Online || len(house.Portals) != 2 {
		t.Fatalf("%+v", house)
	}
	if err := svc.Remove("house"); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.CheckPortal("web"); err != nil {
		t.Fatal(err)
	}
	free, err := svc.CheckPortal("web")
	if err != nil || !free.Free {
		t.Fatalf("%+v %v", free, err)
	}
	if _, err := svc.Stat(context.Background(), "house"); err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatal(err)
	}
}

func TestStatOffline(t *testing.T) {
	svc := newSvc(t)
	if err := svc.Store.CreateComputer("home", secret.Hash("tok"), "alice"); err != nil {
		t.Fatal(err)
	}
	_, err := svc.Stat(context.Background(), "home")
	if err == nil || !strings.Contains(err.Error(), "offline") {
		t.Fatal(err)
	}
}

func TestApproveSurfacesQueueErrors(t *testing.T) {
	svc := newSvc(t)
	if _, err := svc.Approve("nope", "a"); !errors.Is(err, approve.ErrNotFound) {
		t.Fatal(err)
	}
	code := "a3Kf9Q"
	if err := svc.Queue.Submit("home", "1.2.3.4:9", "", secret.Hash(code)); err != nil {
		t.Fatal(err)
	}
	var granted approve.Pending
	svc.Grant = func(p approve.Pending) error {
		granted = p
		return nil
	}
	msg, err := svc.Approve(code, "local")
	if err != nil || msg != "approved home" || granted.Name != "home" {
		t.Fatalf("%q %v %+v", msg, err, granted)
	}
	if _, err := svc.Approve(code, "local"); !errors.Is(err, approve.ErrNotFound) {
		t.Fatal(err)
	}
}

func TestReservedNames(t *testing.T) {
	svc := newSvc(t)
	if err := svc.Rename("nope", "box"); err == nil {
		t.Fatal("box")
	}
	if err := svc.Rename("nope", "join"); err == nil {
		t.Fatal("join")
	}
	if svc.IsComputer("box") || svc.IsComputer("join") || svc.IsComputer("pair+x") {
		t.Fatal("reserved looked like a computer")
	}
}

func TestPendingJSONOmitsCode(t *testing.T) {
	svc := newSvc(t)
	code := "a3Kf9Q"
	if err := svc.Queue.Submit("home", "9.9.9.9:1", "alice", secret.Hash(code)); err != nil {
		t.Fatal(err)
	}
	text, err := FormatPending(svc.Pending(), true)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(text, code) || strings.Contains(text, secret.Hash(code)) {
		t.Fatal(text)
	}
	if !strings.Contains(text, "home") || !strings.Contains(text, "alice") {
		t.Fatal(text)
	}
}

func TestFormatStat(t *testing.T) {
	text, err := FormatStat(tunnel.StatResponse{CPU: 0.5, Memory: 10, Disk: 20, Uptime: 30}, false)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(text, "0.5") || !strings.Contains(text, "uptime") {
		t.Fatal(text)
	}
}

func TestRemoveMissing(t *testing.T) {
	svc := newSvc(t)
	err := svc.Remove("nope")
	if err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatal(err)
	}
}
