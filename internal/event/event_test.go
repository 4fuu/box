package event

import (
	"testing"
	"time"
)

func TestRingDropsOldest(t *testing.T) {
	b := New()
	b.SetNow(func() time.Time { return time.Date(2026, 9, 27, 0, 0, 0, 0, time.UTC) })
	var first Item
	for i := 0; i < MaxEvents; i++ {
		item, err := b.Publish("http", "door", "open")
		if err != nil {
			t.Fatal(err)
		}
		if i == 0 {
			first = item
		}
	}
	if _, err := b.Publish("http", "door", "shut"); err != nil {
		t.Fatal(err)
	}
	got := b.Since(0, "")
	if len(got) != MaxEvents {
		t.Fatalf("len %d", len(got))
	}
	if got[0].ID == first.ID {
		t.Fatal("oldest id was kept")
	}
	if got[len(got)-1].Body != "shut" {
		t.Fatalf("last %q", got[len(got)-1].Body)
	}
	if again := b.Since(got[len(got)-1].ID, "door"); len(again) != 0 {
		t.Fatalf("since last returned %d", len(again))
	}
	if filtered := b.Since(0, "other"); len(filtered) != 0 {
		t.Fatalf("topic filter %d", len(filtered))
	}
}

func TestPublishRejects(t *testing.T) {
	b := New()
	for _, tc := range []struct{ from, topic, body string }{
		{"http", "has space", "x"},
		{"http", "ok", ""},
		{"http", "ok", "a\nb"},
		{"bad from", "ok", "x"},
	} {
		if _, err := b.Publish(tc.from, tc.topic, tc.body); err == nil {
			t.Fatalf("accepted %+v", tc)
		}
	}
}
