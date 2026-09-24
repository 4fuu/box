// Package paths are the on-disk locations the design names.
package paths

const (
	// DataDir is the server and node state directory.
	DataDir = "/var/lib/box"
	// DBFile is the server SQLite file. The GitHub key sits next to it.
	DBFile = DataDir + "/box.db"
	// GitHubKey is the server ed25519 key the operator adds to GitHub.
	GitHubKey = DataDir + "/github_ed25519"
	// HostKey is the SSH host key for the control REPL.
	HostKey = DataDir + "/ssh_host_ed25519_key"
	// ServerSocket is the localhost API. The server CLI uses it.
	ServerSocket = DataDir + "/box.sock"
	// NodeFile is the paired node token and server address.
	NodeFile = DataDir + "/node.json"
	// FRPSConfig is written for the operator's frps process. box serve does not start frps.
	FRPSConfig = DataDir + "/frps.toml"
	// FRPCConfig is the node's frpc config. It contains the node token.
	FRPCConfig = DataDir + "/frpc.toml"
	// GuestSocket is mounted into each computer. The guest CLI talks only to it.
	GuestSocket = "/run/box/guest.sock"
	// VolumeMount is where a computer's volume is mounted.
	VolumeMount = "/var/lib/box"
	// SSHListen is the server's public SSH entry.
	SSHListen = ":22"
	// HTTPListen is the fixed plain HTTP port.
	HTTPListen = ":80"
	// FRPPort is frps. Nodes dial out to it.
	FRPPort = 7000
	// FRPVHostPort is the localhost port box serve proxies portal HTTP to.
	// frps binds it as vhostHTTPPort. It is not published.
	FRPVHostPort = 17080
	// LoginUser is the user inside a computer. The SSH username does not select it.
	LoginUser = "box"
	// SSHDPort is sshd inside the image.
	SSHDPort = 2222
)
