package node

import (
	"bufio"
	"os"
	"runtime"
	"strconv"
	"strings"
	"syscall"
)

// HostCapacity reads cpu count, total memory, and free disk on the data directory.
func HostCapacity(dir string) (int, int64, int64, error) {
	mem, err := memTotal()
	if err != nil {
		return 0, 0, 0, err
	}
	var st syscall.Statfs_t
	if err := syscall.Statfs(dir, &st); err != nil {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return 0, 0, 0, err
		}
		if err := syscall.Statfs(dir, &st); err != nil {
			return 0, 0, 0, err
		}
	}
	disk := int64(st.Bavail) * int64(st.Bsize)
	return runtime.NumCPU(), mem, disk, nil
}

func memTotal() (int64, error) {
	f, err := os.Open("/proc/meminfo")
	if err != nil {
		return 0, err
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := sc.Text()
		if !strings.HasPrefix(line, "MemTotal:") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 2 {
			break
		}
		kb, err := strconv.ParseInt(fields[1], 10, 64)
		if err != nil {
			return 0, err
		}
		return kb * 1024, nil
	}
	if err := sc.Err(); err != nil {
		return 0, err
	}
	return 0, os.ErrNotExist
}
