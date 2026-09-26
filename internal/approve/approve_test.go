package approve

import (
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/4fuu/box/internal/secret"
)

func TestSubmitListApprove(t *testing.T) {
	start := time.Date(2026, 9, 26, 15, 4, 0, 0, time.UTC)
	q := New()
	q.SetNow(func() time.Time { return start })

	p, err := q.Approve("a3Kf9Q", "ops")
	if !errors.Is(err, ErrNotFound) || p != (Pending{}) {
		t.Fatalf("empty: %+v %v", p, err)
	}

	const code = "a3Kf9Q"
	if err := q.Submit("home", "203.0.113.5", "alice", secret.Hash(code)); err != nil {
		t.Fatal(err)
	}
	if err := q.Submit("home", "203.0.113.9", "carol", secret.Hash("other1")); !errors.Is(err, ErrExists) {
		t.Fatalf("exists: %v", err)
	}
	list := q.List()
	if len(list) != 1 {
		t.Fatalf("%+v", list)
	}
	if list[0] != (Pending{Name: "home", Addr: "203.0.113.5", User: "alice", ExpiresAt: start.Add(10 * time.Minute)}) {
		t.Fatalf("%+v", list[0])
	}
	list[0].Name = "changed"
	if q.List()[0].Name != "home" {
		t.Fatal("list aliased internal state")
	}

	if err := q.Submit("office", "203.0.113.6", "bob", secret.Hash("office1")); err != nil {
		t.Fatal(err)
	}
	got, err := q.Approve(code, "ops")
	if err != nil {
		t.Fatal(err)
	}
	if got != (Pending{Name: "home", Addr: "203.0.113.5", User: "alice", ExpiresAt: start.Add(10 * time.Minute)}) {
		t.Fatalf("%+v", got)
	}
	list = q.List()
	if len(list) != 1 || list[0].Name != "office" || list[0].User != "bob" {
		t.Fatalf("%+v", list)
	}
	if _, err := q.Approve(code, "ops"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("replay: %v", err)
	}
}

func TestWrongCodeBurnsSingleJoin(t *testing.T) {
	t.Run("fourth keeps it", func(t *testing.T) {
		q := New()
		mustSubmit(t, q, "home", "1.1.1.1", "alice", "a3Kf9Q")
		for i := 0; i < 4; i++ {
			p, err := q.Approve("wrong1", "ops")
			if !errors.Is(err, ErrNotFound) || p != (Pending{}) {
				t.Fatalf("%d: %+v %v", i, p, err)
			}
			if len(q.List()) != 1 {
				t.Fatalf("dropped on attempt %d", i+1)
			}
		}
		got, err := q.Approve("a3Kf9Q", "ops")
		if err != nil || got.Name != "home" {
			t.Fatalf("%+v %v", got, err)
		}
	})
	t.Run("fifth drops it", func(t *testing.T) {
		q := New()
		mustSubmit(t, q, "home", "1.1.1.1", "alice", "a3Kf9Q")
		for i := 0; i < 4; i++ {
			if _, err := q.Approve("wrong1", "ops"); !errors.Is(err, ErrNotFound) {
				t.Fatalf("%d: %v", i, err)
			}
		}
		p, err := q.Approve("wrong1", "ops")
		if !errors.Is(err, ErrAttempts) || p != (Pending{}) {
			t.Fatalf("%+v %v", p, err)
		}
		if len(q.List()) != 0 {
			t.Fatalf("still listed: %+v", q.List())
		}
		if _, err := q.Approve("a3Kf9Q", "ops"); !errors.Is(err, ErrRate) {
			t.Fatalf("same addr after discard: %v", err)
		}
		if _, err := q.Approve("a3Kf9Q", "other"); !errors.Is(err, ErrNotFound) {
			t.Fatalf("other addr: %v", err)
		}
	})
}

func TestWrongCodeTwoJoins(t *testing.T) {
	q := New()
	mustSubmit(t, q, "home", "1", "alice", "codeAA")
	mustSubmit(t, q, "office", "2", "bob", "codeBB")
	for i := 0; i < 5; i++ {
		p, err := q.Approve("nope00", "ops")
		if !errors.Is(err, ErrNotFound) || p != (Pending{}) {
			t.Fatalf("%d: %+v %v", i, p, err)
		}
	}
	if names(q.List()) != "home,office" {
		t.Fatalf("dropped: %+v", q.List())
	}
	got, err := q.Approve("codeAA", "ops")
	if err != nil || got.Name != "home" {
		t.Fatalf("%+v %v", got, err)
	}
	if names(q.List()) != "office" {
		t.Fatalf("%+v", q.List())
	}
}

func TestExpiry(t *testing.T) {
	start := time.Date(2026, 9, 26, 15, 4, 0, 0, time.UTC)

	t.Run("valid until the deadline", func(t *testing.T) {
		now := start
		q := New()
		q.SetNow(func() time.Time { return now })
		mustSubmit(t, q, "home", "203.0.113.5", "alice", "a3Kf9Q")
		now = start.Add(10*time.Minute - time.Nanosecond)
		list := q.List()
		if len(list) != 1 || !list[0].ExpiresAt.Equal(start.Add(10*time.Minute)) {
			t.Fatalf("%+v", list)
		}
		got, err := q.Approve("a3Kf9Q", "ops")
		if err != nil || got.Name != "home" {
			t.Fatalf("%+v %v", got, err)
		}
	})

	t.Run("expired", func(t *testing.T) {
		now := start
		q := New()
		q.SetNow(func() time.Time { return now })
		mustSubmit(t, q, "home", "203.0.113.5", "alice", "a3Kf9Q")
		if err := q.Submit("home", "203.0.113.5", "alice", secret.Hash("newCod")); !errors.Is(err, ErrExists) {
			t.Fatalf("live exists: %v", err)
		}
		now = start.Add(10 * time.Minute)
		if len(q.List()) != 0 {
			t.Fatal("expired join still listed")
		}
		p, err := q.Approve("wrong1", "ops")
		if !errors.Is(err, ErrNotFound) || p != (Pending{}) {
			t.Fatalf("wrong on expired: %+v %v", p, err)
		}
		p, err = q.Approve("a3Kf9Q", "ops")
		if !errors.Is(err, ErrExpired) || p != (Pending{}) {
			t.Fatalf("expired: %+v %v", p, err)
		}
		if _, err := q.Approve("a3Kf9Q", "ops"); !errors.Is(err, ErrNotFound) {
			t.Fatalf("second: %v", err)
		}
		if err := q.Submit("home", "203.0.113.5", "alice", secret.Hash("newCod")); err != nil {
			t.Fatal(err)
		}
		got, err := q.Approve("newCod", "ops")
		if err != nil || got.Name != "home" || !got.ExpiresAt.Equal(now.Add(10*time.Minute)) {
			t.Fatalf("%+v %v", got, err)
		}
	})
}

func TestIllegalNames(t *testing.T) {
	q := New()
	bad := []string{"box", "pair", "join", "a+b", "a.b", "", "a b", "a/b", "a\tb", "a\nb", " join", "box ", ".", "+", "a\u00a0b"}
	for _, name := range bad {
		err := q.Submit(name, "addr", "alice", secret.Hash("a3Kf9Q"))
		if !errors.Is(err, ErrName) {
			t.Fatalf("%q: %v", name, err)
		}
	}
	if len(q.List()) != 0 {
		t.Fatal("parked an illegal name")
	}
	if err := q.Submit("ok", "addr", "alice", secret.Hash("a3Kf9Q")); err != nil {
		t.Fatalf("illegal names consumed the rate budget: %v", err)
	}

	ok := []string{"home", "Home", "Box", "Pair", "Join", "JOIN", "joining", "boxer", "repair", "a-b", "a_b", "n1"}
	q2 := New()
	for i, name := range ok {
		err := q2.Submit(name, fmt.Sprintf("ok-%d", i), "alice", secret.Hash("a3Kf9Q"))
		if err != nil {
			t.Fatalf("%q: %v", name, err)
		}
	}
}

func TestSubmitRateLimit(t *testing.T) {
	start := time.Date(2026, 9, 26, 15, 4, 0, 0, time.UTC)
	now := start
	q := New()
	q.SetNow(func() time.Time { return now })

	for i := 0; i < 5; i++ {
		name := fmt.Sprintf("c%d", i)
		if err := q.Submit(name, "1.1.1.1", "alice", secret.Hash("code"+name)); err != nil {
			t.Fatal(err)
		}
	}
	if err := q.Submit("c5", "1.1.1.1", "alice", secret.Hash("nope")); !errors.Is(err, ErrRate) {
		t.Fatalf("sixth: %v", err)
	}
	if strings.Contains(names(q.List()), "c5") || len(q.List()) != 5 {
		t.Fatalf("parked over limit: %+v", q.List())
	}
	if err := q.Submit("other", "2.2.2.2", "alice", secret.Hash("other")); err != nil {
		t.Fatalf("other addr: %v", err)
	}
	now = start.Add(10*time.Minute - time.Nanosecond)
	if err := q.Submit("c6", "1.1.1.1", "alice", secret.Hash("later")); !errors.Is(err, ErrRate) {
		t.Fatalf("inside window: %v", err)
	}
	now = start.Add(10 * time.Minute)
	if err := q.Submit("c6", "1.1.1.1", "alice", secret.Hash("later")); err != nil {
		t.Fatalf("window open: %v", err)
	}

	t.Run("exists counts", func(t *testing.T) {
		q := New()
		mustSubmit(t, q, "home", "9.9.9.9", "alice", "a3Kf9Q")
		for i := 0; i < 4; i++ {
			if err := q.Submit("home", "9.9.9.9", "alice", secret.Hash("a3Kf9Q")); !errors.Is(err, ErrExists) {
				t.Fatalf("%d: %v", i, err)
			}
		}
		if err := q.Submit("other", "9.9.9.9", "alice", secret.Hash("other")); !errors.Is(err, ErrRate) {
			t.Fatalf("after exists: %v", err)
		}
		if names(q.List()) != "home" {
			t.Fatalf("%+v", q.List())
		}
	})
}

func TestApproveRateLimit(t *testing.T) {
	start := time.Date(2026, 9, 26, 15, 4, 0, 0, time.UTC)
	now := start
	q := New()
	q.SetNow(func() time.Time { return now })
	mustSubmit(t, q, "home", "1", "alice", "codeAA")
	mustSubmit(t, q, "office", "2", "bob", "codeBB")

	for i := 0; i < 5; i++ {
		p, err := q.Approve("bad", "attacker")
		if !errors.Is(err, ErrNotFound) || p != (Pending{}) {
			t.Fatalf("fail %d: %+v %v", i, p, err)
		}
	}
	if _, err := q.Approve("bad", "attacker"); !errors.Is(err, ErrRate) {
		t.Fatalf("sixth: %v", err)
	}
	if _, err := q.Approve("bad", "operator"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("other addr: %v", err)
	}
	got, err := q.Approve("codeAA", "attacker")
	if err != nil || got.Name != "home" {
		t.Fatalf("right code during lockout: %+v %v", got, err)
	}
	if _, err := q.Approve("bad", "attacker"); !errors.Is(err, ErrRate) {
		t.Fatalf("after success: %v", err)
	}
	if names(q.List()) != "office" {
		t.Fatalf("office dropped: %+v", q.List())
	}
	now = start.Add(time.Minute - time.Nanosecond)
	if _, err := q.Approve("bad", "attacker"); !errors.Is(err, ErrRate) {
		t.Fatalf("inside minute: %v", err)
	}
	if len(q.List()) != 1 {
		t.Fatal("rate limit dropped the join")
	}
	now = start.Add(time.Minute)
	if _, err := q.Approve("bad", "attacker"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("window open: %v", err)
	}
	if names(q.List()) != "office" {
		t.Fatalf("%+v", q.List())
	}
}

func TestListHasNoHash(t *testing.T) {
	const code = "a3Kf9Q"
	hash := secret.Hash(code)
	q := New()
	if err := q.Submit("home", "203.0.113.5", "alice", hash); err != nil {
		t.Fatal(err)
	}
	typ := reflect.TypeOf(Pending{})
	want := []string{"Name", "Addr", "User", "ExpiresAt"}
	if typ.NumField() != len(want) {
		t.Fatalf("Pending has %d fields", typ.NumField())
	}
	for i, name := range want {
		f := typ.Field(i)
		if f.Name != name || !f.IsExported() {
			t.Fatalf("field %d = %s", i, f.Name)
		}
		lower := strings.ToLower(f.Name)
		if strings.Contains(lower, "hash") || strings.Contains(lower, "code") || strings.Contains(lower, "attempt") {
			t.Fatalf("secret field %s", f.Name)
		}
	}
	for _, p := range q.List() {
		blob := fmt.Sprintf("%#v", p)
		if strings.Contains(blob, hash) || strings.Contains(blob, code) {
			t.Fatalf("list leaked secret: %s", blob)
		}
	}
	got, err := q.Approve(code, "ops")
	if err != nil {
		t.Fatal(err)
	}
	blob := fmt.Sprintf("%#v", got)
	if strings.Contains(blob, hash) || strings.Contains(blob, code) {
		t.Fatalf("approve leaked secret: %s", blob)
	}
}

func TestDrop(t *testing.T) {
	q := New()
	mustSubmit(t, q, "home", "1", "alice", "codeAA")
	mustSubmit(t, q, "office", "2", "bob", "codeBB")
	q.Drop("home")
	q.Drop("home")
	q.Drop("missing")
	if names(q.List()) != "office" {
		t.Fatalf("%+v", q.List())
	}
	if _, err := q.Approve("codeAA", "ops"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("%v", err)
	}
}

func TestConcurrentSubmitApprove(t *testing.T) {
	q := New()
	if err := q.Submit("home", "anchor-a", "alice", secret.Hash("anchor-home")); err != nil {
		t.Fatal(err)
	}
	if err := q.Submit("office", "anchor-b", "bob", secret.Hash("anchor-office")); err != nil {
		t.Fatal(err)
	}

	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			for n := 0; n < 20; n++ {
				name := fmt.Sprintf("g%d-%d", i, n)
				addr := fmt.Sprintf("addr-%d", i)
				code := fmt.Sprintf("code-%d-%d", i, n)
				from := fmt.Sprintf("op-%d", i)
				err := q.Submit(name, addr, "alice", secret.Hash(code))
				if errors.Is(err, ErrRate) {
					if _, aerr := q.Approve(code, from); !errors.Is(aerr, ErrNotFound) && !errors.Is(aerr, ErrRate) {
						t.Errorf("approve after rate: %v", aerr)
					}
				} else if err != nil {
					t.Errorf("submit %s: %v", name, err)
				} else if got, aerr := q.Approve(code, from); aerr != nil || got.Name != name {
					t.Errorf("approve %s: %+v %v", name, got, aerr)
				}
				_, _ = q.Approve("wrong-code", from)
				q.Drop(name)
				_ = q.List()
			}
		}(i)
	}
	wg.Wait()

	got, err := q.Approve("anchor-home", "final")
	if err != nil || got.Name != "home" {
		t.Fatalf("home: %+v %v", got, err)
	}
	got, err = q.Approve("anchor-office", "final")
	if err != nil || got.Name != "office" {
		t.Fatalf("office: %+v %v", got, err)
	}
}

func TestConcurrentSameName(t *testing.T) {
	q := New()
	const n = 16
	var wg sync.WaitGroup
	errs := make(chan error, n)
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func(i int) {
			defer wg.Done()
			errs <- q.Submit("same", fmt.Sprintf("a%d", i), "alice", secret.Hash("code1"))
		}(i)
	}
	wg.Wait()
	close(errs)
	ok := 0
	for err := range errs {
		if err == nil {
			ok++
			continue
		}
		if !errors.Is(err, ErrExists) {
			t.Fatal(err)
		}
	}
	if ok != 1 {
		t.Fatalf("successes %d", ok)
	}
	if names(q.List()) != "same" {
		t.Fatalf("%+v", q.List())
	}

	wg.Add(8)
	for i := 0; i < 8; i++ {
		go func() {
			defer wg.Done()
			_, _ = q.Approve("code1", "x")
			q.Drop("same")
			_ = q.List()
		}()
	}
	wg.Wait()
	if len(q.List()) != 0 {
		t.Fatalf("still pending: %+v", q.List())
	}
}

func mustSubmit(t *testing.T, q *Queue, name, addr, user, code string) {
	t.Helper()
	if err := q.Submit(name, addr, user, secret.Hash(code)); err != nil {
		t.Fatalf("submit %s: %v", name, err)
	}
}

func names(list []Pending) string {
	out := make([]string, len(list))
	for i, p := range list {
		out[i] = p.Name
	}
	return strings.Join(out, ",")
}
