package server

import "strings"

// SSH auth is public-key only. The server never offers password auth, so a
// client the server does not know fails with "Permission denied (publickey)"
// and OpenSSH never falls back to a password prompt. Credentials that
// machines present — the join approval-code hash and the computer token —
// travel as the first line of the SSH session instead.

type userClass int

const (
	classREPL userClass = iota
	classPair
	classJoin
	classBoot
	classOther
)

const (
	routeREPL   = "repl"
	routeBind   = "bind"
	routePair   = "pair"
	routeJoin   = "join"
	routeBoot   = "boot"
	routeSplice = "splice"
)

func classifyUser(user string) (userClass, string) {
	switch user {
	case "", "box":
		return classREPL, ""
	}
	if rest, ok := strings.CutPrefix(user, "pair+"); ok {
		return classPair, rest
	}
	if rest, ok := strings.CutPrefix(user, "join+"); ok {
		return classJoin, rest
	}
	if rest, ok := strings.CutPrefix(user, "boot+"); ok {
		return classBoot, rest
	}
	return classOther, user
}

// publicKeyDecision is the whole route table: auth accepts a key or the
// connection dies there. Outside the pairing flows, an unbound key is
// always rejected. A bound key reaches every computer, and with any
// username that is not a registered computer it opens the console.
// classPair may sign. The one-time password is consumed only after that
// signature has authenticated and the session exists.
func publicKeyDecision(class userClass, bound, livePair, computer bool) (bool, string) {
	switch class {
	case classPair:
		return true, routePair
	case classJoin:
		if bound {
			return false, ""
		}
		return true, routeJoin
	case classBoot:
		if bound {
			return false, ""
		}
		return true, routeBoot
	case classREPL:
		if bound {
			return true, routeREPL
		}
		if livePair {
			return true, routeBind
		}
		return false, ""
	default:
		if computer {
			if bound {
				return true, routeSplice
			}
			return false, ""
		}
		if bound {
			return true, routeREPL
		}
		return false, ""
	}
}

// isHex64 is a 64-character lowercase hex string: a sha256 of an
// approval code.
func isHex64(s string) bool {
	if len(s) != 64 {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}
