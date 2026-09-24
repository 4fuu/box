package secret

import "testing"

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

func strip(s string) string {
	out := make([]byte, 0, len(s))
	for i := 0; i < len(s); i++ {
		if s[i] != '-' {
			out = append(out, s[i])
		}
	}
	return string(out)
}
