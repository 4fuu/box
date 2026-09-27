// Package paths are the on-disk locations the design names.
package paths

const (
	// DataDir is the server state directory.
	DataDir = "/var/lib/box"
	// DBFile is the server SQLite file.
	DBFile = DataDir + "/box.db"
	// HostKey is the SSH host key for the server.
	HostKey = DataDir + "/ssh_host_ed25519_key"
	// ServerSocket is the localhost API. The server CLI uses it.
	ServerSocket = DataDir + "/box.sock"
)
