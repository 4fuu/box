package store

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/4fuu/box/internal/event"
)

func mustPublish(t *testing.T, s *Store, from, topic, body, key string) event.Item {
	t.Helper()
	item, dup, err := s.InsertEvent(from, nil, topic, []byte(body), key)
	if err != nil || dup {
		t.Fatalf("publish %q/%q: dup=%v err=%v", topic, key, dup, err)
	}
	return item
}

func TestEventsPersistAcrossReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "box.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	first := mustPublish(t, s, "ssh", "door", "open", "")
	if first.ID != 1 {
		t.Fatalf("first id %d", first.ID)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	second := mustPublish(t, s, "ssh", "door", "shut", "")
	if second.ID != 2 {
		t.Fatalf("id did not continue: %d", second.ID)
	}
	res, err := s.QueryEvents(event.Query{})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Events) != 2 || res.Events[0].Body == nil || string(res.Events[0].Body) != "open" {
		t.Fatalf("reopen lost events: %+v", res.Events)
	}
	if res.Oldest != 1 || res.Latest != 2 {
		t.Fatalf("window oldest=%d latest=%d", res.Oldest, res.Latest)
	}
}

// InsertEvent must keep the body bytes as given: newlines and NULs included.
func TestEventBodyBytesVerbatim(t *testing.T) {
	s := open(t)
	body := []byte("line1\nline2\x00tail")
	item := mustPublish(t, s, "http", "t", string(body), "")
	res, err := s.QueryEvents(event.Query{})
	if err != nil {
		t.Fatal(err)
	}
	if string(res.Events[0].Body) != string(body) || res.Events[0].ID != item.ID {
		t.Fatalf("body not verbatim: %q", res.Events[0].Body)
	}
	if !res.Events[0].Time.Equal(item.Time) {
		t.Fatalf("time %v vs %v", res.Events[0].Time, item.Time)
	}
}

func TestEventDedup(t *testing.T) {
	s := open(t)
	first, dup, err := s.InsertEvent("door-sensor", nil, "door", []byte("open"), "door-1")
	if err != nil || dup {
		t.Fatalf("first: dup=%v err=%v", dup, err)
	}
	again, dup, err := s.InsertEvent("door-sensor", nil, "door", []byte("open again"), "door-1")
	if err != nil || !dup {
		t.Fatalf("second: dup=%v err=%v", dup, err)
	}
	if again.ID != first.ID || string(again.Body) != "open" {
		t.Fatalf("duplicate returned the wrong row: %+v", again)
	}
	// A different publisher with the same key is a new event.
	other, dup, err := s.InsertEvent("window-sensor", nil, "door", []byte("open"), "door-1")
	if err != nil || dup || other.ID == first.ID {
		t.Fatalf("other publisher: dup=%v id=%d err=%v", dup, other.ID, err)
	}
	n, err := s.EventCount()
	if err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Fatalf("stored %d events, want 2", n)
	}
}

func TestEventQueryFilters(t *testing.T) {
	s := open(t)
	mustPublish(t, s, "ssh", "site1", "root", "")
	mustPublish(t, s, "ssh", "site1/a", "a", "")
	mustPublish(t, s, "ssh", "site1/a/b", "ab", "")
	mustPublish(t, s, "ssh", "site10/x", "ten", "")
	mustPublish(t, s, "ssh", "site2", "two", "")
	for _, tc := range []struct {
		topics []string
		want   []string
	}{
		{[]string{"site1/#"}, []string{"site1/a", "site1/a/b"}},
		{[]string{"#"}, []string{"site1", "site1/a", "site1/a/b", "site10/x", "site2"}},
		{[]string{"site1"}, []string{"site1"}},
		{[]string{"site1", "site2"}, []string{"site1", "site2"}},
		{nil, []string{"site1", "site1/a", "site1/a/b", "site10/x", "site2"}},
	} {
		res, err := s.QueryEvents(event.Query{Topics: tc.topics})
		if err != nil {
			t.Fatalf("%v: %v", tc.topics, err)
		}
		got := map[string]bool{}
		for _, e := range res.Events {
			got[e.Topic] = true
		}
		if len(got) != len(tc.want) {
			t.Fatalf("%v matched %v, want %v", tc.topics, got, tc.want)
		}
		for _, w := range tc.want {
			if !got[w] {
				t.Fatalf("%v missing %s", tc.topics, w)
			}
		}
	}
	// from filter
	res, err := s.QueryEvents(event.Query{Froms: []string{"ssh"}})
	if err != nil || len(res.Events) != 5 {
		t.Fatalf("from ssh: %d %v", len(res.Events), err)
	}
	res, err = s.QueryEvents(event.Query{Froms: []string{"nobody"}})
	if err != nil || len(res.Events) != 0 {
		t.Fatalf("from nobody: %d %v", len(res.Events), err)
	}
	// since cursor
	res, err = s.QueryEvents(event.Query{Since: 3})
	if err != nil || len(res.Events) != 2 || res.Events[0].ID != 4 {
		t.Fatalf("since 3: %+v %v", res.Events, err)
	}
	// bad filter values are refused before SQL runs
	if _, err := s.QueryEvents(event.Query{Topics: []string{"bad filter"}}); err == nil {
		t.Fatal("accepted a bad topic filter")
	}
	if _, err := s.QueryEvents(event.Query{Froms: []string{"bad from"}}); err == nil {
		t.Fatal("accepted a bad from filter")
	}
}

func TestEventLimitAndMore(t *testing.T) {
	s := open(t)
	for i := 0; i < 7; i++ {
		mustPublish(t, s, "ssh", "t", "x", "")
	}
	res, err := s.QueryEvents(event.Query{Limit: 3})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Events) != 3 || !res.More || res.Events[2].ID != 3 {
		t.Fatalf("limit cut wrong: n=%d more=%v last=%d", len(res.Events), res.More, res.Events[2].ID)
	}
	res, err = s.QueryEvents(event.Query{Limit: 3, Since: 3})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Events) != 3 || !res.More {
		t.Fatalf("second page wrong: %+v", res)
	}
	res, err = s.QueryEvents(event.Query{Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Events) != 7 || res.More {
		t.Fatalf("full read wrong: n=%d more=%v", len(res.Events), res.More)
	}
	// limit clamps to the max
	res, err = s.QueryEvents(event.Query{Limit: event.MaxLimit + 5})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Events) != 7 {
		t.Fatalf("clamped limit lost rows: %d", len(res.Events))
	}
}

func TestEventWindowAfterPruneAndRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "box.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	s.eventRows = 3
	for i := 0; i < 5; i++ {
		mustPublish(t, s, "ssh", "t", "x", "")
	}
	if err := s.pruneEvents(); err != nil {
		t.Fatal(err)
	}
	oldest, latest, err := s.EventWindow()
	if err != nil {
		t.Fatal(err)
	}
	if oldest != 3 || latest != 5 {
		t.Fatalf("after prune oldest=%d latest=%d, want 3 and 5", oldest, latest)
	}
	n, _ := s.EventCount()
	if n != 3 {
		t.Fatalf("kept %d rows, want 3", n)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	oldest, latest, err = s.EventWindow()
	if err != nil {
		t.Fatal(err)
	}
	if oldest != 3 || latest != 5 {
		t.Fatalf("after restart oldest=%d latest=%d, want 3 and 5", oldest, latest)
	}
	next := mustPublish(t, s, "ssh", "t", "x", "")
	if next.ID != 6 {
		t.Fatalf("id after prune and restart: %d", next.ID)
	}
}

func TestEventRetentionByAge(t *testing.T) {
	s := open(t)
	s.eventAge = time.Hour
	now := time.Date(2026, 10, 3, 12, 0, 0, 0, time.UTC)
	s.SetNow(func() time.Time { return now })
	mustPublish(t, s, "ssh", "t", "old", "")
	now = now.Add(2 * time.Hour)
	mustPublish(t, s, "ssh", "t", "new", "")
	if err := s.pruneEvents(); err != nil {
		t.Fatal(err)
	}
	res, err := s.QueryEvents(event.Query{})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Events) != 1 || string(res.Events[0].Body) != "new" {
		t.Fatalf("age prune kept %+v", res.Events)
	}
	// The dedup key died with its event: the same key now stores again.
	_, dup, err := s.InsertEvent("ssh", nil, "t", []byte("old2"), "gone")
	if err != nil || dup {
		t.Fatalf("dedup key survived its event: dup=%v err=%v", dup, err)
	}
}

func TestEventRetentionAmortisedOnWrite(t *testing.T) {
	s := open(t)
	s.eventRows = 2
	s.eventPruneStep = 4
	for i := 0; i < 3; i++ {
		mustPublish(t, s, "ssh", "t", "x", "")
	}
	if n, _ := s.EventCount(); n != 3 {
		t.Fatalf("pruned before its turn: %d", n)
	}
	mustPublish(t, s, "ssh", "t", "x", "")
	if n, _ := s.EventCount(); n != 2 {
		t.Fatalf("write-path prune kept %d rows, want 2", n)
	}
}

func TestRecentEventsNewestLast(t *testing.T) {
	s := open(t)
	for i := 0; i < 8; i++ {
		mustPublish(t, s, "ssh", "t", "x", "")
	}
	recent, err := s.RecentEvents(3)
	if err != nil {
		t.Fatal(err)
	}
	if len(recent) != 3 || recent[0].ID != 6 || recent[2].ID != 8 {
		t.Fatalf("recent %+v", recent)
	}
}

// A NULL dedup_key must not trip the partial unique index: publishers that
// never set keys store freely.
func TestEventNullKeysDoNotCollide(t *testing.T) {
	s := open(t)
	for i := 0; i < 5; i++ {
		mustPublish(t, s, "ssh", "t", "x", "")
	}
	if n, err := s.EventCount(); err != nil || n != 5 {
		t.Fatalf("null keys collided: n=%d err=%v", n, err)
	}
}
