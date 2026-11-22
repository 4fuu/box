package tunnel

import "time"

// Lived is how long a tunnel must stay up before the agent counts it as a
// reconnect and starts its backoff over. A server that accepts and drops at
// once keeps the delay growing instead of becoming a tight loop. It is a var
// so a test can watch the reset without waiting out five seconds.
var Lived = 5 * time.Second

// Backoff is the reconnect delay for attempt, starting at 1s and capped at 30s.
// jitter is in [0, 1). The same jitter always yields the same delay.
// The result never drops below 1s at attempt 0 and never exceeds 30s.
func Backoff(attempt int, jitter float64) time.Duration {
	return BackoffFor(attempt, jitter, time.Second)
}

// BackoffFor is Backoff with the starting delay as a parameter, so a test
// can watch the delay grow without waiting a second for it.
func BackoffFor(attempt int, jitter float64, base time.Duration) time.Duration {
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
	delay := base << shift
	if delay > 30*time.Second {
		delay = 30 * time.Second
	}
	d := time.Duration(float64(delay) * (1 + jitter))
	if d > 30*time.Second {
		d = 30 * time.Second
	}
	return d
}
