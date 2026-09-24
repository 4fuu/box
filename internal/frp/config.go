// Package frp writes frpc and frps config.
// The files contain tokens. Callers create them mode 0600 and do not log them.
package frp

import (
	"fmt"
	"strings"
)

// Proxy is one frpc proxy. SSH and HTTP names are per computer because frp names are unique.
// The design's node-<id>-ssh and node-<id>-http are the prefixes.
type Proxy struct {
	Name         string
	Type         string
	LocalIP      string
	LocalPort    int
	Secret       string
	CustomDomain string
}

func RPCName(id string) string { return "node-" + id + "-rpc" }

func SSHName(id, computer string) string { return "node-" + id + "-ssh-" + computer }

func HTTPName(id, label string) string { return "node-" + id + "-http-" + label }

// ServerTOML is the frps config. box serve writes it and does not start frps.
func ServerTOML(bindPort, vhostPort int, token string) string {
	return fmt.Sprintf("bindPort = %d\nauth.method = \"token\"\nauth.token = %q\nvhostHTTPPort = %d\n",
		bindPort, token, vhostPort)
}

// ClientTOML is the node's frpc config.
func ClientTOML(serverAddr string, serverPort int, token string, proxies []Proxy) string {
	var b strings.Builder
	fmt.Fprintf(&b, "serverAddr = %q\nserverPort = %d\nauth.method = \"token\"\nauth.token = %q\n",
		serverAddr, serverPort, token)
	for _, p := range proxies {
		writeProxy(&b, p)
	}
	return b.String()
}

// VisitorTOML is a short-lived frpc the server uses to open an STCP visitor.
func VisitorTOML(serverAddr string, serverPort int, token, serverName, secret string, bindPort int) string {
	var b strings.Builder
	fmt.Fprintf(&b, "serverAddr = %q\nserverPort = %d\nauth.method = \"token\"\nauth.token = %q\n",
		serverAddr, serverPort, token)
	fmt.Fprintf(&b, "\n[[visitors]]\nname = %q\ntype = \"stcp\"\nserverName = %q\nsecretKey = %q\nbindAddr = \"127.0.0.1\"\nbindPort = %d\n",
		"visit-"+serverName, serverName, secret, bindPort)
	return b.String()
}

func writeProxy(b *strings.Builder, p Proxy) {
	fmt.Fprintf(b, "\n[[proxies]]\nname = %q\ntype = %q\nlocalIP = %q\nlocalPort = %d\n",
		p.Name, p.Type, p.LocalIP, p.LocalPort)
	if p.Type == "stcp" {
		fmt.Fprintf(b, "secretKey = %q\n", p.Secret)
	}
	if p.CustomDomain != "" {
		fmt.Fprintf(b, "customDomains = [%q]\n", p.CustomDomain)
	}
}
