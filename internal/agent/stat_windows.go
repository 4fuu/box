//go:build windows

package agent

import (
	"os"
	"path/filepath"
	"strings"
	"unsafe"

	"github.com/4fuu/box/internal/tunnel"
	"golang.org/x/sys/windows"
)

// sshHostPub is the sshd host public key reported at hello. Windows keeps
// the host keys under ProgramData. Tests point the var at a key they can
// present; an empty key makes the splice fail closed.
var sshHostPub = filepath.Join(programData, `ssh\ssh_host_ed25519_key.pub`)

// programData is the fixed location for machine-wide state such as the
// OpenSSH server's config and host keys.
var programData = windowsProgramData()

func windowsProgramData() string {
	if d := os.Getenv("ProgramData"); d != "" {
		return d
	}
	return `C:\ProgramData`
}

func hostKey() string {
	b, err := os.ReadFile(sshHostPub)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

// memoryStatusEx mirrors MEMORYSTATUSEX. x/sys/windows ships no wrapper for
// GlobalMemoryStatusEx, so the call is made directly.
type memoryStatusEx struct {
	Length               uint32
	MemoryLoad           uint32
	TotalPhys            uint64
	AvailPhys            uint64
	TotalPageFile        uint64
	AvailPageFile        uint64
	TotalVirtual         uint64
	AvailVirtual         uint64
	AvailExtendedVirtual uint64
}

var globalMemoryStatusEx = windows.NewLazySystemDLL("kernel32.dll").NewProc("GlobalMemoryStatusEx")

// readStat reports memory, disk, and uptime from the Windows APIs. There is
// no load average; CPU stays zero and the dashboard shows a dash.
func readStat() tunnel.StatResponse {
	var s tunnel.StatResponse
	var mem memoryStatusEx
	mem.Length = uint32(unsafe.Sizeof(mem))
	if r, _, _ := globalMemoryStatusEx.Call(uintptr(unsafe.Pointer(&mem))); r != 0 && mem.TotalPhys >= mem.AvailPhys {
		s.Memory = int64(mem.TotalPhys - mem.AvailPhys)
	}
	// The system drive stands in for the root filesystem.
	drive := os.Getenv("SystemDrive")
	if drive == "" {
		drive = `C:`
	}
	if p, err := windows.UTF16PtrFromString(drive + `\`); err == nil {
		var free, total, avail uint64
		if err := windows.GetDiskFreeSpaceEx(p, &free, &total, &avail); err == nil && total >= free {
			s.Disk = int64(total - free)
		}
	}
	s.Uptime = int64(windows.DurationSinceBoot().Seconds())
	return s
}
