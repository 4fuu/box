package control

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/4fuu/box/internal/rpc"
	"github.com/4fuu/box/internal/store"
)

type fakeNodes struct {
	creates []rpc.CreateBody
	pulls   []string
	deleted []string
	fail    error
}

func (f *fakeNodes) Create(_ context.Context, _ string, body rpc.CreateBody) error {
	if f.fail != nil {
		return f.fail
	}
	f.creates = append(f.creates, body)
	return nil
}
func (f *fakeNodes) Delete(_ context.Context, _, name string) error {
	f.deleted = append(f.deleted, name)
	return nil
}
func (f *fakeNodes) Restart(context.Context, string, rpc.CreateBody) error { return nil }
func (f *fakeNodes) Rename(context.Context, string, string, string) error  { return nil }
func (f *fakeNodes) Resize(context.Context, string, rpc.ResizeBody) error  { return nil }
func (f *fakeNodes) Pull(_ context.Context, _ string, body rpc.PullBody) error {
	f.pulls = append(f.pulls, body.Name)
	return nil
}
func (f *fakeNodes) SyncKeys(context.Context, string, []string) error { return nil }
func (f *fakeNodes) HostKey(context.Context, string, string) (string, error) {
	return "", nil
}
func (f *fakeNodes) Stat(context.Context, string, string) (rpc.StatBody, error) {
	return rpc.StatBody{}, nil
}

func newSvc(t *testing.T) (*Service, *fakeNodes) {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "box.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	nodes := &fakeNodes{}
	return &Service{
		Store: st, Nodes: nodes, Domain: "box.example.com",
		GitHubPublic: "ssh-ed25519 AAAAGH github\n",
	}, nodes
}

func pairNode(t *testing.T, svc *Service, name string) {
	t.Helper()
	p, err := svc.PairNode()
	if err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := svc.JoinNode(name, p.Secret); err != nil {
		t.Fatal(err)
	}
	n, err := svc.Store.NodeByName(name)
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.Store.Heartbeat(n.ID, store.Heartbeat{CPU: 4, Memory: 8 * 1 << 30, Disk: 100 * 1 << 30}); err != nil {
		t.Fatal(err)
	}
}

func TestCreateDoesNotClaimPortal(t *testing.T) {
	svc, nodes := newSvc(t)
	pairNode(t, svc, "home")
	if err := svc.AddImage("base", "registry.example/base:latest"); err != nil {
		t.Fatal(err)
	}
	if err := svc.SetDefaultImage("base"); err != nil {
		t.Fatal(err)
	}
	_, err := svc.Create(context.Background(), CreateInput{Name: "web", Image: "base", Node: "home"})
	if err == nil || !strings.Contains(err.Error(), "image pull base") {
		t.Fatalf("expected pull hint, got %v", err)
	}
	if err := svc.Pull(context.Background(), "base", "home"); err != nil {
		t.Fatal(err)
	}
	created, err := svc.Create(context.Background(), CreateInput{Name: "web", Image: "base", Node: "home", CPU: 2, Memory: 2 << 30, Disk: 20 << 30})
	if err != nil {
		t.Fatal(err)
	}
	if created.SSH != "ssh web@box.example.com" || created.HTTP != "http://web.box.example.com" {
		t.Fatalf("%+v", created)
	}
	if _, err := svc.Store.PortalByHost("web.box.example.com"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("new registered a portal: %v", err)
	}
	if len(nodes.creates) != 1 {
		t.Fatalf("creates %d", len(nodes.creates))
	}
	body := nodes.creates[0]
	joined := strings.Join(body.AuthorizedKeys, "\n")
	if !strings.Contains(joined, "AAAAGH") {
		t.Fatalf("github key missing from authorized keys: %v", body.AuthorizedKeys)
	}
	const secret = "ghp_do_not_print"
	if _, err := svc.SetEnv("GH_TOKEN", secret); err != nil {
		t.Fatal(err)
	}
	names, err := svc.EnvNames()
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(strings.Join(names, " "), secret) {
		t.Fatal("env ls returned a value")
	}
	msg, _ := svc.SetEnv("GH_TOKEN", secret)
	if strings.Contains(msg, secret) || !strings.Contains(msg, "restart") {
		t.Fatalf("msg %q", msg)
	}
}

func TestReservedAndPortalHold(t *testing.T) {
	svc, _ := newSvc(t)
	if _, err := svc.Create(context.Background(), CreateInput{Name: "box"}); err == nil {
		t.Fatal("box should be reserved")
	}
	if _, err := svc.Create(context.Background(), CreateInput{Name: "a+b"}); err == nil {
		t.Fatal("plus")
	}
	pairNode(t, svc, "home")
	if err := svc.AddImage("base", "ref"); err != nil {
		t.Fatal(err)
	}
	if err := svc.Pull(context.Background(), "base", "home"); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Create(context.Background(), CreateInput{Name: "web", Node: "home", Image: "base"}); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Create(context.Background(), CreateInput{Name: "api", Node: "home", Image: "base"}); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.ClaimPortal("web", "app", 3000); err != nil {
		t.Fatal(err)
	}
	_, err := svc.ClaimPortal("api", "app", 3001)
	if err == nil || !strings.Contains(err.Error(), "held by web") {
		t.Fatalf("%v", err)
	}
	free, holder, err := svc.CheckPortal("app")
	if err != nil || free || holder != "web" {
		t.Fatalf("free %v holder %s %v", free, holder, err)
	}
	if err := svc.RemoveNode("home"); err == nil || !strings.Contains(err.Error(), "still has computers") {
		t.Fatalf("%v", err)
	}
}

func TestOfflineAndShrink(t *testing.T) {
	svc, _ := newSvc(t)
	start := time.Date(2026, 9, 24, 12, 0, 0, 0, time.UTC)
	svc.Store.SetNow(func() time.Time { return start })
	pairNode(t, svc, "home")
	if err := svc.AddImage("base", "ref"); err != nil {
		t.Fatal(err)
	}
	if err := svc.Pull(context.Background(), "base", "home"); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Create(context.Background(), CreateInput{Name: "web", Node: "home", Image: "base"}); err != nil {
		t.Fatal(err)
	}
	svc.Store.SetNow(func() time.Time { return start.Add(2 * time.Hour) })
	list, err := svc.ListComputers()
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0].State != "offline" {
		t.Fatalf("%+v", list)
	}
	svc.Store.SetNow(func() time.Time { return start })
	// Refresh heartbeat so resize is allowed, then shrink.
	n, err := svc.Store.NodeByName("home")
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.Store.Heartbeat(n.ID, store.Heartbeat{CPU: 4, Memory: 8 << 30, Disk: 100 << 30, Images: []string{"base"}}); err != nil {
		t.Fatal(err)
	}
	small := int64(1 << 30)
	if err := svc.Resize(context.Background(), ResizeInput{Name: "web", Disk: &small}); err == nil || !strings.Contains(err.Error(), "disk only grows") {
		t.Fatalf("%v", err)
	}
}

func TestKeyCopyIsOnlyThePublicLine(t *testing.T) {
	svc, _ := newSvc(t)
	line, err := svc.KeyCopy()
	if err != nil {
		t.Fatal(err)
	}
	if line != "ssh-ed25519 AAAAGH github\n" {
		t.Fatalf("%q", line)
	}
}
