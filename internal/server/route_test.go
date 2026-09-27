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
		{"boot+home", classBoot, "home"},
		{"home", classOther, "home"},
		{"pair", classOther, "pair"},
		{"join", classOther, "join"},
		{"boot", classOther, "boot"},
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
		{"join fresh key", classJoin, false, false, false, true, routeJoin},
		{"join bound key rejected", classJoin, true, false, false, false, ""},
		{"boot fresh key", classBoot, false, false, false, true, routeBoot},
		{"boot bound key rejected", classBoot, true, false, false, false, ""},
		{"computer bound splices", classOther, true, false, true, true, routeSplice},
		{"computer unbound rejected", classOther, false, true, true, false, ""},
		{"other name bound opens repl", classOther, true, true, false, true, routeREPL},
		{"other name unbound rejected", classOther, false, true, false, false, ""},
		{"pair name is not repl", classOther, true, true, false, true, routeREPL},
	}
	for _, r := range rows {
		ok, route := publicKeyDecision(r.class, r.bound, r.live, r.box)
		if ok != r.accept || route != r.route {
			t.Fatalf("%s: got %v %q, want %v %q", r.name, ok, route, r.accept, r.route)
		}
	}
}

func TestIsHex64(t *testing.T) {
	hash := "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	if !isHex64(hash) {
		t.Fatal("hash")
	}
	for _, bad := range []string{"", "abc", hash[:63], hash + "a", "ABCDEF" + hash[6:], hash[:62] + "GG"} {
		if isHex64(bad) {
			t.Fatalf("accepted %q", bad)
		}
	}
}
