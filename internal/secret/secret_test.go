package secret

import (
	"strings"
	"testing"
)

func TestPasswordShape(t *testing.T) {
	p, err := ClientPassword()
	if err != nil {
		t.Fatal(err)
	}
	if len(p) != 24 || p[4] != '-' {
		t.Fatalf("%s", p)
	}
	if NormalizePassword(strip(p)) != p {
		t.Fatalf("normalize %s -> %s", p, NormalizePassword(strip(p)))
	}
	if Hash(p) == p {
		t.Fatal("hash echoed the secret")
	}
	c, err := NodeCode()
	if err != nil || len(c) != 8 {
		t.Fatalf("%s %v", c, err)
	}
}

func TestApprovalCode(t *testing.T) {
	const n = 8
	var first string
	same := true
	for i := 0; i < n; i++ {
		code, err := ApprovalCode()
		if err != nil {
			t.Fatal(err)
		}
		if len(code) != 6 {
			t.Fatalf("len %d: %s", len(code), code)
		}
		for j := 0; j < len(code); j++ {
			if !strings.ContainsRune(approvalAlphabet, rune(code[j])) {
				t.Fatalf("char %q not in alphabet: %s", code[j], code)
			}
		}
		sum := Hash(code)
		if len(sum) != 64 || !isHex(sum) {
			t.Fatalf("hash %s", sum)
		}
		if i == 0 {
			first = code
			continue
		}
		if code != first {
			same = false
		}
	}
	if same {
		t.Fatal("approval codes were identical")
	}
}

func isHex(s string) bool {
	for i := 0; i < len(s); i++ {
		c := s[i]
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

func strip(s string) string {
	out := make([]byte, 0, len(s))
	for i := 0; i < len(s); i++ {
		if s[i] != '-' {
			out = append(out, s[i])
		}
	}
	return string(out)
}
