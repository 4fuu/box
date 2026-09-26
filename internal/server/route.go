package server

import "strings"

// SSH routing is decided from the username and the auth method together.
// Password auth never opens the REPL: a password alone must not bind a key.

type userClass int

const (
	classREPL userClass = iota
	classPair
	classJoin
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
	return classOther, user
}

// publicKeyDecision is the public-key half of the route table.
// classPair may sign. The one-time password is consumed only after that
// signature has authenticated and the session exists.
func publicKeyDecision(class userClass, bound, livePair, computer bool) (bool, string) {
	switch class {
	case classPair:
		return true, routePair
	case classJoin:
		return false, ""
	case classREPL:
		if bound {
			return true, routeREPL
		}
		if livePair {
			return true, routeBind
		}
		return false, ""
	default:
		if bound && computer {
			return true, routeSplice
		}
		return false, ""
	}
}

// passwordDecision is the password half of the route table.
// hexOK is a 64-character lowercase hash. tokenOK is the computer's token.
func passwordDecision(class userClass, hexOK, tokenOK bool) (bool, string) {
	switch class {
	case classJoin:
		if hexOK {
			return true, routeJoin
		}
		return false, ""
	case classOther:
		if tokenOK {
			return true, routeBoot
		}
		return false, ""
	default:
		return false, ""
	}
}

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
