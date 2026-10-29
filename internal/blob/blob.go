// Package blob is the server's store for payloads too big for events:
// screenshots, camera frames, exports. Metadata lives in the SQLite file;
// the bytes live in a Backend. The operator picks the backend at serve time.
package blob

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"time"
)

// Retention. Constants, not flags: a blob over MaxSize is refused, all
// blobs together stay under MaxTotal, and none is older than MaxAge.
// Pruning runs on the write path, oldest first.
const (
	MaxSize  = 64 << 20
	MaxTotal = 4 << 30
	MaxAge   = 7 * 24 * time.Hour
)

// Backend stores and returns one blob's bytes by id. Implementations never
// see metadata: content type and from live in the SQLite file. Open and
// Delete report a missing blob with os.ErrNotExist.
type Backend interface {
	// Name is what a metadata row records: "disk" or "s3".
	Name() string
	Put(ctx context.Context, id int64, size int64, body io.Reader) error
	Open(ctx context.Context, id int64) (io.ReadCloser, error)
	Delete(ctx context.Context, id int64) error
}

// Disk keeps each blob in one file named by id, inside dir. dir must be on
// the server's filesystem; the server creates it.
type Disk struct{ dir string }

// NewDisk returns a disk backend rooted at dir.
func NewDisk(dir string) *Disk { return &Disk{dir: dir} }

func (d *Disk) Name() string { return "disk" }

func (d *Disk) path(id int64) string { return filepath.Join(d.dir, strconv.FormatInt(id, 10)) }

func (d *Disk) Put(ctx context.Context, id int64, size int64, body io.Reader) error {
	if err := os.MkdirAll(d.dir, 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(d.dir, "put-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer os.Remove(name) // a rename removes it on success; a failure cleans up
	n, err := io.Copy(tmp, body)
	cerr := tmp.Close()
	if err != nil {
		return err
	}
	if cerr != nil {
		return cerr
	}
	if n != size {
		return fmt.Errorf("disk blob %d: wrote %d of %d bytes", id, n, size)
	}
	if err := os.Chmod(name, 0o600); err != nil {
		return err
	}
	return os.Rename(name, d.path(id))
}

func (d *Disk) Open(ctx context.Context, id int64) (io.ReadCloser, error) {
	f, err := os.Open(d.path(id))
	if errors.Is(err, os.ErrNotExist) {
		return nil, os.ErrNotExist
	}
	return f, err
}

func (d *Disk) Delete(ctx context.Context, id int64) error {
	err := os.Remove(d.path(id))
	if errors.Is(err, os.ErrNotExist) {
		return nil // already gone: retention only ever moves forward
	}
	return err
}
