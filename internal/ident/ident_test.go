package ident

import "testing"

func TestComputer(t *testing.T) {
	ok := []string{"web", "a", "web-1", "n123"}
	for _, n := range ok {
		if err := Computer(n); err != nil {
			t.Fatalf("%s: %v", n, err)
		}
	}
	bad := []string{"", "box", "pair", "a+b", "a.b", "Web", "-a", "a-", "has space", "a/b"}
	for _, n := range bad {
		if err := Computer(n); err == nil {
			t.Fatalf("%s should be rejected", n)
		}
	}
	if err := Computer("box"); err == nil || err.Error() != "name box is reserved" {
		t.Fatalf("box: %v", err)
	}
}

func TestLabelRejectsDot(t *testing.T) {
	if err := Label("web"); err != nil {
		t.Fatal(err)
	}
	if err := Label("web.other"); err == nil {
		t.Fatal("dot should be rejected")
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
