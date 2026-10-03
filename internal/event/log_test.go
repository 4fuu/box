package event_test

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/4fuu/box/internal/event"
	"github.com/4fuu/box/internal/store"
)

func newLog(t *testing.T) (*event.Log, *store.Store) {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "box.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	return event.NewLog(st), st
}

func TestPublishValidatesAndWakesNothing(t *testing.T) {
	log, _ := newLog(t)
	if _, _, err := log.Publish("bad from", nil, "door", []byte("x"), ""); !errors.Is(err, event.ErrInvalidFrom) {
		t.Fatalf("from: %v", err)
	}
	if _, _, err := log.Publish("ssh", nil, "bad topic", []byte("x"), ""); !errors.Is(err, event.ErrInvalidTopic) {
		t.Fatalf("topic: %v", err)
	}
	if _, _, err := log.Publish("ssh", nil, "door", make([]byte, event.MaxBody+1), ""); !errors.Is(err, event.ErrBodyTooLarge) {
		t.Fatalf("body: %v", err)
	}
	if _, _, err := log.Publish("ssh", nil, "door", []byte("x"), "bad key"); !errors.Is(err, event.ErrInvalidKey) {
		t.Fatalf("key: %v", err)
	}
	// An empty body is a valid event.
	if _, _, err := log.Publish("ssh", nil, "door", nil, ""); err != nil {
		t.Fatalf("empty body: %v", err)
	}
}

// A publish while a read waits must wake it with the new event.
func TestGetWaitsForPublish(t *testing.T) {
	log, _ := newLog(t)
	done := make(chan event.Result, 1)
	go func() {
		res, err := log.Get(context.Background(), event.Query{Wait: 5 * time.Second})
		if err != nil {
			t.Error(err)
		}
		done <- res
	}()
	time.Sleep(100 * time.Millisecond)
	if _, _, err := log.Publish("ssh", nil, "door", []byte("open"), ""); err != nil {
		t.Fatal(err)
	}
	select {
	case res := <-done:
		if len(res.Events) != 1 || string(res.Events[0].Body) != "open" {
			t.Fatalf("woke with %+v", res.Events)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("publish did not wake the read")
	}
}

// A publish that does not match the filter wakes the read, which re-queries
// and keeps waiting until the budget runs out.
func TestGetIgnoresNonMatchingWake(t *testing.T) {
	log, _ := newLog(t)
	done := make(chan struct{})
	start := time.Now()
	go func() {
		defer close(done)
		res, err := log.Get(context.Background(), event.Query{Topics: []string{"door"}, Wait: 400 * time.Millisecond})
		if err != nil {
			t.Error(err)
		}
		if len(res.Events) != 0 {
			t.Errorf("matched wrong topic: %+v", res.Events)
		}
	}()
	time.Sleep(100 * time.Millisecond)
	if _, _, err := log.Publish("ssh", nil, "window", []byte("x"), ""); err != nil {
		t.Fatal(err)
	}
	<-done
	if elapsed := time.Since(start); elapsed < 350*time.Millisecond {
		t.Fatalf("a non-matching wake shortened the wait: %v", elapsed)
	}
}

func TestGetTimesOutEmpty(t *testing.T) {
	log, _ := newLog(t)
	res, err := log.Get(context.Background(), event.Query{Wait: 100 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Events) != 0 || res.Oldest != 0 || res.Latest != 0 || res.More {
		t.Fatalf("empty log window wrong: %+v", res)
	}
}

func TestGetCancel(t *testing.T) {
	log, _ := newLog(t)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := log.Get(ctx, event.Query{Wait: 10 * time.Second})
		done <- err
	}()
	time.Sleep(100 * time.Millisecond)
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("cancel returned %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("cancel did not end the wait")
	}
}

func TestGetClamps(t *testing.T) {
	log, _ := newLog(t)
	for i := 0; i < 3; i++ {
		if _, _, err := log.Publish("ssh", nil, "t", []byte("x"), ""); err != nil {
			t.Fatal(err)
		}
	}
	// An oversized wait still returns immediately when events are waiting.
	res, err := log.Get(context.Background(), event.Query{Wait: 10 * time.Minute})
	if err != nil || len(res.Events) != 3 {
		t.Fatalf("wait ignored ready events: %+v %v", res, err)
	}
	if _, err := log.Get(context.Background(), event.Query{Topics: []string{"bad filter"}}); err == nil {
		t.Fatal("bad filter accepted")
	}
	if _, err := log.Get(context.Background(), event.Query{Froms: []string{"bad from"}}); err == nil {
		t.Fatal("bad from accepted")
	}
}

func TestFollow(t *testing.T) {
	log, _ := newLog(t)
	publish := func(body string) {
		t.Helper()
		if _, _, err := log.Publish("ssh", nil, "door", []byte(body), ""); err != nil {
			t.Fatal(err)
		}
	}
	publish("one")
	ctx, cancel := context.WithCancel(context.Background())
	var lines []string
	gaps := 0
	errc := make(chan error, 1)
	go func() {
		errc <- event.Follow(ctx, event.Query{}, 0, true, log.Get, func(res event.Result) error {
			for _, e := range res.Events {
				lines = append(lines, string(e.Body))
			}
			return nil
		}, func(since, oldest int64) { gaps++ })
	}()
	time.Sleep(200 * time.Millisecond)
	publish("two")
	publish("three")
	time.Sleep(300 * time.Millisecond)
	cancel()
	if err := <-errc; !errors.Is(err, context.Canceled) {
		t.Fatalf("follow ended with %v", err)
	}
	if len(lines) != 2 || lines[0] != "two" || lines[1] != "three" {
		t.Fatalf("follow from latest saw %+v", lines)
	}
	if gaps != 0 {
		t.Fatalf("false gap reported %d times", gaps)
	}
}

// A reader whose cursor fell behind retention hears about the gap, then
// keeps following from what is left. The fetch here is a stub: the window it
// reports is what the gap check reads.
func TestFollowReportsGap(t *testing.T) {
	fetch := func(ctx context.Context, q event.Query) (event.Result, error) {
		if q.Wait > 0 {
			select {
			case <-time.After(q.Wait):
			case <-ctx.Done():
				return event.Result{}, ctx.Err()
			}
		}
		if q.Since == 0 {
			return event.Result{Oldest: 10, Latest: 12, Events: []event.Item{{ID: 10, Body: []byte("x")}}}, nil
		}
		return event.Result{Oldest: 10, Latest: 12}, nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	var gaps []int64
	errc := make(chan error, 1)
	go func() {
		errc <- event.Follow(ctx, event.Query{Wait: 40 * time.Millisecond}, 0, false, fetch,
			func(event.Result) error { return nil },
			func(since, oldest int64) { gaps = append(gaps, since, oldest) })
	}()
	time.Sleep(250 * time.Millisecond)
	cancel()
	if err := <-errc; !errors.Is(err, context.Canceled) {
		t.Fatalf("follow ended with %v", err)
	}
	if len(gaps) < 2 || gaps[0] != 0 || gaps[1] != 10 {
		t.Fatalf("gap not reported once per poll: %v", gaps)
	}
}
