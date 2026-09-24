// Package size parses the cpu, memory, and disk spellings used by new and resize.
package size

import (
	"fmt"
	"math"
	"strconv"
	"strings"
	"unicode"
)

const (
	K int64 = 1024
	M       = 1024 * K
	G       = 1024 * M
	T       = 1024 * G
)

const (
	// Defaults match the sizes in the first-session example.
	DefaultCPU    = 2.0
	DefaultMemory = 2 * G
	DefaultDisk   = 20 * G
)

// ParseBytes accepts 2G, 2GB, 2GiB, 512M, and a bare byte count.
func ParseBytes(s string) (int64, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, fmt.Errorf("invalid size")
	}
	i := 0
	for i < len(s) && (unicode.IsDigit(rune(s[i])) || s[i] == '.') {
		i++
	}
	num := s[:i]
	unit := strings.ToUpper(strings.TrimSpace(s[i:]))
	unit = strings.TrimSuffix(unit, "IB")
	unit = strings.TrimSuffix(unit, "B")
	f, err := strconv.ParseFloat(num, 64)
	if err != nil || f < 0 || math.IsNaN(f) || math.IsInf(f, 0) {
		return 0, fmt.Errorf("invalid size")
	}
	var mul int64 = 1
	switch unit {
	case "":
		mul = 1
	case "K":
		mul = K
	case "M":
		mul = M
	case "G":
		mul = G
	case "T":
		mul = T
	default:
		return 0, fmt.Errorf("invalid size")
	}
	if f*float64(mul) > math.MaxInt64 {
		return 0, fmt.Errorf("invalid size")
	}
	n := int64(math.Round(f * float64(mul)))
	if n <= 0 {
		return 0, fmt.Errorf("invalid size")
	}
	return n, nil
}

// FormatBytes prints a whole number of the largest binary unit.
func FormatBytes(n int64) string {
	switch {
	case n >= T && n%T == 0:
		return fmt.Sprintf("%dT", n/T)
	case n >= G && n%G == 0:
		return fmt.Sprintf("%dG", n/G)
	case n >= M && n%M == 0:
		return fmt.Sprintf("%dM", n/M)
	case n >= K && n%K == 0:
		return fmt.Sprintf("%dK", n/K)
	default:
		return strconv.FormatInt(n, 10)
	}
}

// ParseCPU parses a cpu count. Zero and negative are rejected.
func ParseCPU(s string) (float64, error) {
	f, err := strconv.ParseFloat(strings.TrimSpace(s), 64)
	if err != nil || f <= 0 || math.IsNaN(f) || math.IsInf(f, 0) {
		return 0, fmt.Errorf("invalid cpu")
	}
	return f, nil
}

// FormatCPU prints whole counts as integers.
func FormatCPU(f float64) string {
	if f == math.Trunc(f) {
		return strconv.FormatInt(int64(f), 10)
	}
	return strconv.FormatFloat(f, 'f', -1, 64)
}
