package keys

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestGenerateReuses(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "k")
	a, err := Generate(path, "box")
	if err != nil {
		t.Fatal(err)
	}
	b, err := Generate(path, "box")
	if err != nil {
		t.Fatal(err)
	}
	same, err := Same(a, b)
	if err != nil || !same {
		t.Fatalf("reused key changed: %v", err)
	}
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Fatalf("mode %o", fi.Mode().Perm())
	}
	if !strings.Contains(a, "ssh-ed25519") || !strings.HasSuffix(a, "\n") {
		t.Fatalf("line %q", a)
	}
	if strings.Count(a, "\n") != 1 {
		t.Fatalf("line %q", a)
	}
}
