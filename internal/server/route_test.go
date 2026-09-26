package server

import "testing"

func TestClassifyUser(t *testing.T) {
	cases := []struct {
		user  string
		class userClass
		rest  string
	}{
		{"", classREPL, ""},
		{"box", classREPL, ""},
		{"pair+abcd-efgh-ijkl-mnop-qrst", classPair, "abcd-efgh-ijkl-mnop-qrst"},
		{"join+home", classJoin, "home"},
		{"home", classOther, "home"},
		{"pair", classOther, "pair"},
		{"join", classOther, "join"},
		{"pairing", classOther, "pairing"},
		{"joinhome", classOther, "joinhome"},
	}
	for _, tc := range cases {
		class, rest := classifyUser(tc.user)
		if class != tc.class || rest != tc.rest {
			t.Fatalf("%q -> %v %q, want %v %q", tc.user, class, rest, tc.class, tc.rest)
		}
	}
}

func TestPublicKeyRoutes(t *testing.T) {
	type row struct {
		name             string
		class            userClass
		bound, live, box bool
		accept           bool
		route            string
	}
	// box here means "name is a computer".
	rows := []row{
		{"repl bound", classREPL, true, false, false, true, routeREPL},
		{"repl live password", classREPL, false, true, false, true, routeBind},
		{"repl unknown", classREPL, false, false, false, false, ""},
		{"pair any key", classPair, false, false, false, true, routePair},
		{"join key rejected", classJoin, true, true, false, false, ""},
		{"computer bound splices", classOther, true, false, true, true, routeSplice},
		{"computer unbound rejected", classOther, false, true, true, false, ""},
		{"unknown name bound rejected", classOther, true, true, false, false, ""},
		{"pair name is not repl", classOther, true, true, false, false, ""},
	}
	for _, r := range rows {
		ok, route := publicKeyDecision(r.class, r.bound, r.live, r.box)
		if ok != r.accept || route != r.route {
			t.Fatalf("%s: got %v %q, want %v %q", r.name, ok, route, r.accept, r.route)
		}
	}
}

func TestPasswordRoutes(t *testing.T) {
	hash := "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	if !isHex64(hash) {
		t.Fatal("hash")
	}
	for _, bad := range []string{"", "abc", hash[:63], hash + "a", "ABCDEF" + hash[6:], hash[:62] + "GG"} {
		if isHex64(bad) {
			t.Fatalf("accepted %q", bad)
		}
	}
	rows := []struct {
		name       string
		class      userClass
		hex, token bool
		accept     bool
		route      string
	}{
		{"join hash", classJoin, true, false, true, routeJoin},
		{"join not hash", classJoin, false, true, false, ""},
		{"bootstrap token", classOther, false, true, true, routeBoot},
		{"bootstrap bad token", classOther, true, false, false, ""},
		{"repl password rejected", classREPL, true, true, false, ""},
		{"pair password rejected", classPair, true, true, false, ""},
	}
	for _, r := range rows {
		ok, route := passwordDecision(r.class, r.hex, r.token)
		if ok != r.accept || route != r.route {
			t.Fatalf("%s: got %v %q, want %v %q", r.name, ok, route, r.accept, r.route)
		}
	}
}
