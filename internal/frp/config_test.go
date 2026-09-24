package frp

import (
	"strings"
	"testing"
)

func TestNamesAndHTTPDomain(t *testing.T) {
	if RPCName("abc") != "node-abc-rpc" {
		t.Fatal(RPCName("abc"))
	}
	proxies := []Proxy{
		{Name: RPCName("abc"), Type: "stcp", LocalIP: "127.0.0.1", LocalPort: 1, Secret: "sek"},
		{Name: SSHName("abc", "web"), Type: "stcp", LocalIP: "127.0.0.1", LocalPort: 2222, Secret: "sek"},
		{Name: HTTPName("abc", "web"), Type: "http", LocalIP: "10.88.0.2", LocalPort: 3000, CustomDomain: "web.box.example.com"},
	}
	got := ClientTOML("box.example.com", 7000, "frp-tok", proxies)
	for _, want := range []string{
		`name = "node-abc-rpc"`,
		`name = "node-abc-ssh-web"`,
		`name = "node-abc-http-web"`,
		`customDomains = ["web.box.example.com"]`,
		`type = "stcp"`,
		`type = "http"`,
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("missing %s\n%s", want, got)
		}
	}
	vis := VisitorTOML("127.0.0.1", 7000, "frp-tok", RPCName("abc"), "sek", 14000)
	if !strings.Contains(vis, `serverName = "node-abc-rpc"`) || !strings.Contains(vis, "bindPort = 14000") {
		t.Fatal(vis)
	}
	srv := ServerTOML(7000, 17080, "frp-tok")
	if !strings.Contains(srv, "vhostHTTPPort = 17080") || !strings.Contains(srv, "bindPort = 7000") {
		t.Fatal(srv)
	}
}
