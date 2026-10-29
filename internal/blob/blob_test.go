package blob

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"
)

func TestDiskRoundtrip(t *testing.T) {
	ctx := context.Background()
	d := NewDisk(filepath.Join(t.TempDir(), "blobs"))
	body := []byte("a screenshot, more or less")
	if err := d.Put(ctx, 7, int64(len(body)), bytes.NewReader(body)); err != nil {
		t.Fatal(err)
	}
	rc, err := d.Open(ctx, 7)
	if err != nil {
		t.Fatal(err)
	}
	got, err := io.ReadAll(rc)
	rc.Close()
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, body) {
		t.Fatalf("roundtrip got %q", got)
	}
	if err := d.Delete(ctx, 7); err != nil {
		t.Fatal(err)
	}
	if err := d.Delete(ctx, 7); err != nil {
		t.Fatalf("deleting a missing blob is not an error: %v", err)
	}
	if _, err := d.Open(ctx, 7); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("open after delete: %v", err)
	}
}

func TestDiskPutSizeMismatch(t *testing.T) {
	ctx := context.Background()
	d := NewDisk(filepath.Join(t.TempDir(), "blobs"))
	if err := d.Put(ctx, 1, 10, bytes.NewReader([]byte("short"))); err == nil {
		t.Fatal("a short body must fail, not store the wrong bytes")
	}
	if _, err := d.Open(ctx, 1); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("a failed put must leave no blob behind")
	}
}
