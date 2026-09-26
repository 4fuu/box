package tunnel

import "time"

// Backoff is the reconnect delay for attempt, starting at 1s and capped at 30s.
// jitter is in [0, 1). The same jitter always yields the same delay.
// The result never drops below 1s at attempt 0 and never exceeds 30s.
func Backoff(attempt int, jitter float64) time.Duration {
	if attempt < 0 {
		attempt = 0
	}
	if jitter < 0 {
		jitter = 0
	} else if jitter > 1 {
		jitter = 1
	}
	// 1s, 2s, 4s, ... until the 30s cap. Shift stops before the duration overflows.
	shift := attempt
	if shift > 5 {
		shift = 5
	}
	base := time.Second << shift
	if base > 30*time.Second {
		base = 30 * time.Second
	}
	d := time.Duration(float64(base) * (1 + jitter))
	if d < time.Second {
		d = time.Second
	}
	if d > 30*time.Second {
		d = 30 * time.Second
	}
	return d
}
