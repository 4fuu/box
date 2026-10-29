// Package paths are the on-disk locations the design names.
package paths

import (
	"os"
	"path/filepath"
	"runtime"
)

// defaultDataDir is /var/lib/box on Linux. On Windows a rooted /var path
// would land on the current drive, so the machine-wide ProgramData is used.
func defaultDataDir() string {
	if runtime.GOOS == "windows" {
		if d := os.Getenv("ProgramData"); d != "" {
			return filepath.Join(d, "box")
		}
		return `C:\ProgramData\box`
	}
	return "/var/lib/box"
}

var (
	// DataDir is the server state directory.
	DataDir = defaultDataDir()
	// DBFile is the server SQLite file.
	DBFile = filepath.Join(DataDir, "box.db")
	// HostKey is the SSH host key for the server.
	HostKey = filepath.Join(DataDir, "ssh_host_ed25519_key")
	// ServerSocket is the localhost API. The server CLI uses it.
	ServerSocket = filepath.Join(DataDir, "box.sock")
)
