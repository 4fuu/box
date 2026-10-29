package store

import (
	"testing"
	"time"
)

func openBlobs(t *testing.T) *Store {
	t.Helper()
	s, err := Open(t.TempDir() + "/box.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestBlobInsertLookupStats(t *testing.T) {
	s := openBlobs(t)
	tok := int64(5)
	m, err := s.InsertBlob("door-sensor", &tok, 11, "image/jpeg", "disk")
	if err != nil {
		t.Fatal(err)
	}
	if m.ID == 0 || m.Size != 11 || m.ContentType != "image/jpeg" || m.Store != "disk" {
		t.Fatalf("insert returned %+v", m)
	}
	got, err := s.Blob(m.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.From != "door-sensor" || got.Size != 11 {
		t.Fatalf("lookup returned %+v", got)
	}
	if _, err := s.Blob(999); err != ErrNotFound {
		t.Fatalf("missing blob: %v", err)
	}
	n, total, err := s.BlobStats()
	if err != nil || n != 1 || total != 11 {
		t.Fatalf("stats %d %d %v", n, total, err)
	}
}

func TestBlobPruneByAge(t *testing.T) {
	s := openBlobs(t)
	if _, err := s.InsertBlob("a", nil, 10, "", "disk"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.InsertBlob("a", nil, 10, "", "disk"); err != nil {
		t.Fatal(err)
	}
	s.SetNow(func() time.Time { return time.Now().UTC().Add(8 * 24 * time.Hour) })
	gone, err := s.PruneBlobs()
	if err != nil {
		t.Fatal(err)
	}
	if len(gone) != 2 {
		t.Fatalf("both blobs aged out, gone has %d", len(gone))
	}
	if n, _, _ := s.BlobStats(); n != 0 {
		t.Fatalf("%d blobs survived the age pass", n)
	}
}

func TestBlobPruneByTotal(t *testing.T) {
	s := openBlobs(t)
	if _, err := s.InsertBlob("a", nil, 60, "", "disk"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.InsertBlob("a", nil, 30, "", "disk"); err != nil {
		t.Fatal(err)
	}
	// The newest blob alone fits; the oldest is dropped to make room.
	if _, err := s.InsertBlob("a", nil, 30, "", "disk"); err != nil {
		t.Fatal(err)
	}
	s.blobTotal = 61
	gone, err := s.PruneBlobs()
	if err != nil {
		t.Fatal(err)
	}
	if len(gone) != 1 || gone[0].ID != 1 || gone[0].Size != 60 {
		t.Fatalf("cap pass dropped %+v", gone)
	}
	n, total, err := s.BlobStats()
	if err != nil || n != 2 || total != 60 {
		t.Fatalf("after prune %d rows %d bytes %v", n, total, err)
	}
	// The dropped row names its backend so the caller can delete the object.
	if gone[0].Store != "disk" {
		t.Fatalf("dropped row lost its backend: %+v", gone[0])
	}
}
