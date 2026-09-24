package runtime

import (
	"strings"
	"testing"

	"github.com/4fuu/box/internal/size"
)

func TestRunArgs(t *testing.T) {
	args := RunArgs(Spec{
		Name:        "web",
		ImageRef:    "registry.example/base:latest",
		CPU:         2,
		Memory:      2 * size.G,
		Volume:      "web",
		Env:         map[string]string{"GH_TOKEN": "ghp_secret"},
		HostKey:     "/var/lib/box/computers/web/ssh_host_ed25519_key",
		SSHDir:      "/var/lib/box/computers/web/ssh",
		GuestSocket: "/var/lib/box/guests/web.sock",
	})
	got := strings.Join(args, " ")
	wants := []string{
		"--systemd=always",
		"--restart=always",
		"--cpus 2",
		"--memory 2G",
		"--volume web:/var/lib/box",
		"--publish 127.0.0.1::2222",
		"--env GH_TOKEN=ghp_secret",
		"readonly",
		"registry.example/base:latest",
		"/run/box/guest.sock",
	}
	for _, w := range wants {
		if !strings.Contains(got, w) {
			t.Fatalf("missing %q\n%s", w, got)
		}
	}
	for _, banned := range []string{"--privileged", "--network=host", "docker.sock", "--publish 3000", "-p 80"} {
		if strings.Contains(got, banned) {
			t.Fatalf("forbidden %s in %s", banned, got)
		}
	}
}

func TestParsePort(t *testing.T) {
	n, err := parsePublishedPort("127.0.0.1:34567\n")
	if err != nil || n != 34567 {
		t.Fatalf("%d %v", n, err)
	}
}
