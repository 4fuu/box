package ratelimit

import (
	"net/netip"
	"testing"
	"time"
)

func clockAt(t *time.Time) func() time.Time {
	return func() time.Time { return *t }
}

func TestQuietAddressIsNeverBanned(t *testing.T) {
	var now time.Time
	l := New()
	l.SetNow(clockAt(&now))
	addr := netip.MustParseAddr("192.0.2.1")
	for i := 0; i < Burst; i++ {
		if ban, banned := l.Fail(addr); banned || ban != 0 {
			t.Fatalf("failure %d banned for %s", i+1, ban)
		}
	}
	if _, banned := l.Banned(addr); banned {
		t.Fatal("banned inside the burst")
	}
}

func TestFailuresPastBurstBan(t *testing.T) {
	var now time.Time
	l := New()
	l.SetNow(clockAt(&now))
	addr := netip.MustParseAddr("192.0.2.1")
	for i := 0; i < Burst; i++ {
		l.Fail(addr)
	}
	ban, banned := l.Fail(addr)
	if !banned || ban != Window {
		t.Fatalf("ban %s banned %v, want %s", ban, banned, Window)
	}
	if left, banned := l.Banned(addr); !banned || left != Window {
		t.Fatalf("banned %v for %s", banned, left)
	}
	now = now.Add(Window)
	if _, banned := l.Banned(addr); banned {
		t.Fatal("still banned after the ban ended")
	}
}

func TestBansDoubleUpToTheCap(t *testing.T) {
	var now time.Time
	l := New()
	l.SetNow(clockAt(&now))
	addr := netip.MustParseAddr("2001:db8::1")
	want := []time.Duration{Window, 2 * Window, 4 * Window, MaxBan, MaxBan}
	for i, w := range want {
		for j := 0; j < Burst; j++ {
			l.Fail(addr)
		}
		ban, banned := l.Fail(addr)
		if !banned || ban != w {
			t.Fatalf("ban %d: %s banned %v, want %s", i+1, ban, banned, w)
		}
		now = now.Add(w)
	}
	other := netip.MustParseAddr("2001:db8::2")
	if _, banned := l.Fail(other); banned {
		t.Fatal("one address's failures banned another")
	}
}

func TestFailuresSpreadOutNeverBan(t *testing.T) {
	var now time.Time
	l := New()
	l.SetNow(clockAt(&now))
	addr := netip.MustParseAddr("192.0.2.2")
	for i := 0; i < Burst*4; i++ {
		if _, banned := l.Fail(addr); banned {
			t.Fatalf("banned at failure %d", i+1)
		}
		now = now.Add(Window + time.Second)
	}
}

func TestQuietHourDropsTheAddress(t *testing.T) {
	var now time.Time
	l := New()
	l.SetNow(clockAt(&now))
	addr := netip.MustParseAddr("192.0.2.3")
	for i := 0; i < Burst; i++ {
		l.Fail(addr)
	}
	now = now.Add(Quiet + time.Second)
	if _, banned := l.Fail(addr); banned {
		t.Fatal("history survived a quiet hour")
	}
	if n := l.Counts(); n != 1 {
		t.Fatalf("held %d entries", n)
	}
	now = now.Add(Quiet + time.Second)
	l.Fail(netip.MustParseAddr("192.0.2.4"))
	if n := l.Counts(); n != 1 {
		t.Fatalf("sweep held %d entries", n)
	}
}

func TestForgetClearsAnAddress(t *testing.T) {
	var now time.Time
	l := New()
	l.SetNow(clockAt(&now))
	addr := netip.MustParseAddr("192.0.2.5")
	for i := 0; i < Burst; i++ {
		l.Fail(addr)
	}
	l.Fail(addr)
	l.Forget(addr)
	if _, banned := l.Banned(addr); banned {
		t.Fatal("forget did not lift the ban")
	}
}

func TestEntriesAreCapped(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	l := New()
	l.SetNow(clockAt(&now))
	for i := 0; i < MaxEntries+10; i++ {
		now = now.Add(time.Millisecond)
		l.Fail(netip.AddrFrom4([4]byte{10, byte(i >> 16), byte(i >> 8), byte(i)}))
	}
	if n := l.Counts(); n > MaxEntries {
		t.Fatalf("held %d entries", n)
	}
}

func TestAddressKeys(t *testing.T) {
	v4, ok := Address("192.0.2.4:5555")
	if !ok || v4.String() != "192.0.2.4" {
		t.Fatalf("v4 %v %v", v4, ok)
	}
	mapped, ok := Address("[::ffff:192.0.2.4]:5555")
	if !ok || mapped != v4 {
		t.Fatalf("mapped %v %v, want the v4 key", mapped, ok)
	}
	v6, ok := Address("[2001:db8::4]:22")
	if !ok || v6.String() != "2001:db8::4" {
		t.Fatalf("v6 %v %v", v6, ok)
	}
	for _, bad := range []string{"", "nonsense", "192.0.2.4"} {
		if _, ok := Address(bad); ok {
			t.Fatalf("%q parsed", bad)
		}
	}
}
