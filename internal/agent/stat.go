package agent

import (
	"os"
	"strconv"
	"strings"
	"syscall"

	"github.com/4fuu/box/internal/tunnel"
)

func hostKey() string {
	b, err := os.ReadFile("/etc/ssh/ssh_host_ed25519_key.pub")
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

func readStat() tunnel.StatResponse {
	if _, err := os.Stat("/proc"); err != nil {
		return tunnel.StatResponse{}
	}
	var s tunnel.StatResponse
	if b, err := os.ReadFile("/proc/loadavg"); err == nil {
		fields := strings.Fields(string(b))
		if len(fields) > 0 {
			s.CPU, _ = strconv.ParseFloat(fields[0], 64)
		}
	}
	if b, err := os.ReadFile("/proc/meminfo"); err == nil {
		var total, avail int64
		var haveT, haveA bool
		for _, line := range strings.Split(string(b), "\n") {
			switch {
			case strings.HasPrefix(line, "MemTotal:"):
				total, haveT = meminfoKB(line)
			case strings.HasPrefix(line, "MemAvailable:"):
				avail, haveA = meminfoKB(line)
			}
		}
		if haveT && haveA && total >= avail {
			s.Memory = (total - avail) * 1024
		}
	}
	var st syscall.Statfs_t
	if err := syscall.Statfs("/", &st); err == nil && st.Blocks >= st.Bfree {
		unit := st.Frsize
		if unit <= 0 {
			unit = st.Bsize
		}
		if unit > 0 {
			s.Disk = int64(uint64(unit) * (st.Blocks - st.Bfree))
		}
	}
	if b, err := os.ReadFile("/proc/uptime"); err == nil {
		fields := strings.Fields(string(b))
		if len(fields) > 0 {
			if f, err := strconv.ParseFloat(fields[0], 64); err == nil && f >= 0 {
				s.Uptime = int64(f)
			}
		}
	}
	return s
}

func meminfoKB(line string) (int64, bool) {
	fields := strings.Fields(line)
	if len(fields) < 2 {
		return 0, false
	}
	n, err := strconv.ParseInt(fields[1], 10, 64)
	if err != nil {
		return 0, false
	}
	return n, true
}
