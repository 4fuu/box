package repl

import (
	"bytes"
	"strings"
	"testing"

	"github.com/4fuu/box/internal/approve"
	"github.com/4fuu/box/internal/control"
	"github.com/4fuu/box/internal/secret"
	"github.com/4fuu/box/internal/store"
)

func TestCRLFWriter(t *testing.T) {
	var buf bytes.Buffer
	w := CRLF(&buf)
	in := []byte("a\nb\r\nc")
	n, err := w.Write(in)
	if err != nil {
		t.Fatal(err)
	}
	if n != len(in) {
		t.Fatalf("Write returned %d, want %d (the caller's byte count)", n, len(in))
	}
	if got := buf.String(); got != "a\r\nb\r\nc" {
		t.Fatalf("CRLF output = %q, want lone \\n doubled and \\r\\n untouched", got)
	}
}

func TestCRLFWriterPassthrough(t *testing.T) {
	var buf bytes.Buffer
	if _, err := CRLF(&buf).Write([]byte("no newlines")); err != nil {
		t.Fatal(err)
	}
	if got := buf.String(); got != "no newlines" {
		t.Fatalf("got %q", got)
	}
}

func TestExecHelp(t *testing.T) {
	var buf bytes.Buffer
	r := &REPL{Out: &buf, Err: &buf}

	if err := r.Exec([]string{"help"}); err != nil {
		t.Fatal(err)
	}
	overview := buf.String()
	for _, want := range []string{"Common commands:", "† marks a command with subcommands.", "help all"} {
		if !strings.Contains(overview, want) {
			t.Fatalf("help overview missing %q:\n%s", want, overview)
		}
	}

	buf.Reset()
	if err := r.Exec([]string{"help", "all"}); err != nil {
		t.Fatal(err)
	}
	all := buf.String()
	for _, want := range []string{"Computers:", "env set <name> <value>", "approve <code>"} {
		if !strings.Contains(all, want) {
			t.Fatalf("help all missing %q:\n%s", want, all)
		}
	}
	if strings.Contains(all, "node pair") || strings.Contains(all, "key copy") {
		t.Fatalf("help all still lists removed commands:\n%s", all)
	}

	buf.Reset()
	if err := r.Exec([]string{"help", "key"}); err != nil {
		t.Fatal(err)
	}
	key := buf.String()
	if !strings.Contains(key, "key ls") || strings.Contains(key, "env set") {
		t.Fatalf("help key printed the wrong set:\n%s", key)
	}

	buf.Reset()
	if err := r.Exec([]string{"bogus"}); err == nil || !strings.Contains(err.Error(), `unknown command "bogus"`) {
		t.Fatalf("bogus: err = %v, want unknown command", err)
	}
	if err := r.Exec([]string{"key"}); err == nil || !strings.Contains(err.Error(), "needs a subcommand") {
		t.Fatalf("bare key: err = %v, want needs-a-subcommand", err)
	}
}

func TestPlainAndJSONLists(t *testing.T) {
	st, err := store.Open(t.TempDir() + "/box.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	svc := &control.Service{
		Store: st, Domain: "box.example.com", Queue: approve.New(), Live: control.NewLive(),
	}
	if err := st.CreateComputer("home", secret.Hash("tok"), "alice"); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.AddPortal("home", "web", 3000); err != nil {
		t.Fatal(err)
	}
	const secretValue = "ghp_secret_value"
	if _, err := svc.SetEnv("GH_TOKEN", secretValue); err != nil {
		t.Fatal(err)
	}
	if err := st.BindKey("ssh-ed25519 AAAACLIENT laptop", "laptop", "SHA256:laptop"); err != nil {
		t.Fatal(err)
	}
	const code = "a3Kf9Q"
	if err := svc.Queue.Submit("laptop", "9.9.9.9:1", "bob", secret.Hash(code)); err != nil {
		t.Fatal(err)
	}

	var buf bytes.Buffer
	r := &REPL{Out: &buf, Err: &buf, Svc: svc}

	check := func(argv []string, want []string, forbid []string) {
		t.Helper()
		buf.Reset()
		if err := r.Exec(argv); err != nil {
			t.Fatal(argv, err)
		}
		got := buf.String()
		if strings.Contains(got, "\x1b") {
			t.Fatalf("%v has color: %q", argv, got)
		}
		for _, s := range want {
			if !strings.Contains(got, s) {
				t.Fatalf("%v missing %q:\n%s", argv, s, got)
			}
		}
		for _, s := range forbid {
			if s != "" && strings.Contains(got, s) {
				t.Fatalf("%v leaked %q:\n%s", argv, s, got)
			}
		}
	}

	check([]string{"ls"}, []string{"NAME", "ONLINE", "USER", "ADDRESS", "AGENT", "PORTALS", "home", "alice", "web"}, []string{secretValue, code})
	check([]string{"ls", "--json"}, []string{`"name":"home"`, `"online":false`, `"user":"alice"`, `"web"`}, []string{secretValue, code, "\x1b"})
	check([]string{"pending"}, []string{"NAME", "ADDRESS", "laptop", "bob"}, []string{code, secret.Hash(code), secretValue})
	check([]string{"--json", "pending"}, []string{`"name":"laptop"`, `"user":"bob"`}, []string{code, secret.Hash(code)})
	check([]string{"env", "ls"}, []string{"GH_TOKEN"}, []string{secretValue})
	check([]string{"env", "ls", "--json"}, []string{`"names"`, "GH_TOKEN"}, []string{secretValue})
	check([]string{"key", "ls"}, []string{"SHA256:laptop", "laptop"}, []string{secretValue, "AAAACLIENT"})
	check([]string{"key", "ls", "--json"}, []string{`"fingerprint":"SHA256:laptop"`, `"comment":"laptop"`}, []string{secretValue, "AAAACLIENT"})
}
