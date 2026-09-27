package server

import (
	"context"
	"errors"
	"testing"

	gossh "golang.org/x/crypto/ssh"
)

func TestDialNamedUsesEachComputer(t *testing.T) {
	var got []string
	h := &sshServer{
		dialHook: func(_ context.Context, name string) (*gossh.Client, error) {
			got = append(got, name)
			return nil, errors.New("offline")
		},
	}
	for _, name := range []string{"home", "work", "home"} {
		if _, err := h.dialNamed(context.Background(), name); err == nil {
			t.Fatal("failed dial was cached")
		}
	}
	if len(got) != 3 || got[0] != "home" || got[1] != "work" || got[2] != "home" {
		t.Fatalf("dials %v, want home, work, home with the failure retried", got)
	}
}
