package ident

import "testing"

func TestComputer(t *testing.T) {
	ok := []string{"web", "a", "web-1", "n123"}
	for _, n := range ok {
		if err := Computer(n); err != nil {
			t.Fatalf("%s: %v", n, err)
		}
	}
	bad := []string{"", "box", "pair", "join", "a+b", "a.b", "Web", "-a", "a-", "has space", "a/b"}
	for _, n := range bad {
		if err := Computer(n); err == nil {
			t.Fatalf("%s should be rejected", n)
		}
	}
	if err := Computer("box"); err == nil || err.Error() != "name box is reserved" {
		t.Fatalf("box: %v", err)
	}
	if err := Computer("join"); err == nil || err.Error() != "name join is reserved" {
		t.Fatalf("join: %v", err)
	}
}

func TestLabelRejectsDot(t *testing.T) {
	if err := Label("web"); err != nil {
		t.Fatal(err)
	}
	if err := Label("web.other"); err == nil {
		t.Fatal("dot should be rejected")
	}
	if err := Label("event"); err == nil || err.Error() != "label event is reserved" {
		t.Fatalf("event: %v", err)
	}
	if err := Label("auth"); err == nil || err.Error() != "label auth is reserved" {
		t.Fatalf("auth: %v", err)
	}
	if err := Computer("event"); err != nil {
		t.Fatal("a computer may be named event")
	}
	if Hostname("web", "box.example.com") != "web.box.example.com" {
		t.Fatal(Hostname("web", "box.example.com"))
	}
}

func TestEnv(t *testing.T) {
	if err := Env("GH_TOKEN"); err != nil {
		t.Fatal(err)
	}
	if err := Env("gh-token"); err == nil {
		t.Fatal("hyphen")
	}
}

func TestDomain(t *testing.T) {
	got, err := Domain("Box.Example.com.")
	if err != nil || got != "box.example.com" {
		t.Fatalf("%s %v", got, err)
	}
	if _, err := Domain("http://box.example.com"); err == nil {
		t.Fatal("scheme")
	}
}
