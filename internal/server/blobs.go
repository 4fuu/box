package server

import (
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/4fuu/box/internal/blob"
	"github.com/4fuu/box/internal/store"
)

// blobPost answers POST /api/blobs on event.<domain>: the request bytes
// become one blob, spooled to disk first so a chunked upload with no length
// still reaches the backend with one. The stored content type is the
// request's, sanitized.
func (s *Server) blobPost(w http.ResponseWriter, r *http.Request, from string, tokenID int64) {
	if r.ContentLength > blob.MaxSize {
		http.Error(w, "blob over 64 MiB\n", http.StatusRequestEntityTooLarge)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, blob.MaxSize)
	tmp, err := os.CreateTemp(s.spoolDir, "blob-*")
	if err != nil {
		http.Error(w, "unavailable\n", http.StatusServiceUnavailable)
		return
	}
	defer os.Remove(tmp.Name())
	size, err := io.Copy(tmp, r.Body)
	if err != nil {
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			http.Error(w, "blob over 64 MiB\n", http.StatusRequestEntityTooLarge)
			return
		}
		http.Error(w, "bad request\n", http.StatusBadRequest)
		return
	}
	if _, err := tmp.Seek(0, io.SeekStart); err != nil {
		http.Error(w, "unavailable\n", http.StatusServiceUnavailable)
		return
	}

	meta, err := s.store.InsertBlob(from, &tokenID, size, cleanContentType(r.Header.Get("Content-Type")), s.blobs.Name())
	if err != nil {
		http.Error(w, "unavailable\n", http.StatusServiceUnavailable)
		return
	}
	if err := s.blobs.Put(r.Context(), meta.ID, size, tmp); err != nil {
		_ = s.store.DeleteBlob(meta.ID) // the row without bytes answers 404
		http.Error(w, "blob storage failed\n", http.StatusBadGateway)
		return
	}
	writeJSON(w, http.StatusCreated, struct {
		ID   int64 `json:"id"`
		Size int64 `json:"size"`
	}{ID: meta.ID, Size: size})

	// The blob is stored; a failed prune only delays retention.
	gone, err := s.store.PruneBlobs()
	if err != nil {
		return
	}
	for _, m := range gone {
		if m.Store == s.blobs.Name() {
			_ = s.blobs.Delete(r.Context(), m.ID)
		}
	}
}

// cleanContentType keeps one header value out of the answer: no newlines,
// never longer than 255 bytes, empty becomes the generic type.
func cleanContentType(raw string) string {
	raw = strings.TrimSpace(strings.Map(func(c rune) rune {
		if c == '\r' || c == '\n' {
			return -1
		}
		return c
	}, raw))
	if len(raw) > 255 {
		raw = raw[:255]
	}
	if raw == "" {
		return "application/octet-stream"
	}
	return raw
}

// blobGet answers GET and HEAD /api/blobs/<id> on event.<domain> and on
// the bare domain. A row held by another backend answers 410; a row whose
// object is gone answers 404, the same as no row at all.
func (s *Server) blobGet(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(strings.TrimPrefix(r.URL.Path, "/api/blobs/"), 10, 64)
	if err != nil || id <= 0 {
		http.NotFound(w, r)
		return
	}
	meta, err := s.store.Blob(id)
	if errors.Is(err, store.ErrNotFound) {
		http.Error(w, "no such blob\n", http.StatusNotFound)
		return
	} else if err != nil {
		http.Error(w, "unavailable\n", http.StatusServiceUnavailable)
		return
	}
	if meta.Store != s.blobs.Name() {
		http.Error(w, "blob store changed\n", http.StatusGone)
		return
	}
	body, err := s.blobs.Open(r.Context(), id)
	if errors.Is(err, os.ErrNotExist) {
		http.Error(w, "no such blob\n", http.StatusNotFound)
		return
	} else if err != nil {
		http.Error(w, "blob storage failed\n", http.StatusBadGateway)
		return
	}
	defer body.Close()
	h := w.Header()
	ctype := meta.ContentType
	if ctype == "" {
		ctype = "application/octet-stream"
	}
	h.Set("Content-Type", ctype)
	h.Set("Content-Length", strconv.FormatInt(meta.Size, 10))
	h.Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(http.StatusOK)
	_, _ = io.Copy(w, body)
}

// openBlobBackend builds the configured backend and its spool directory.
// S3 credentials come from the environment when the flags are empty, so
// they stay out of process listings.
func openBlobBackend(cfg Config) (blob.Backend, string, error) {
	spool := filepath.Join(cfg.DataDir, "spool")
	if err := os.MkdirAll(spool, 0o700); err != nil {
		return nil, "", err
	}
	switch cfg.Blob.Store {
	case "", "disk":
		dir := cfg.Blob.Dir
		if dir == "" {
			dir = filepath.Join(cfg.DataDir, "blobs")
		}
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return nil, "", err
		}
		return blob.NewDisk(dir), spool, nil
	case "s3":
		if cfg.Blob.S3Endpoint == "" || cfg.Blob.S3Bucket == "" {
			return nil, "", errors.New("s3 blob store needs --blob-s3-endpoint and --blob-s3-bucket")
		}
		key, secret := cfg.Blob.S3Key, cfg.Blob.S3Secret
		if key == "" {
			key = os.Getenv("BOX_BLOB_S3_KEY")
		}
		if secret == "" {
			secret = os.Getenv("BOX_BLOB_S3_SECRET")
		}
		if key == "" || secret == "" {
			return nil, "", errors.New("s3 blob store needs BOX_BLOB_S3_KEY and BOX_BLOB_S3_SECRET")
		}
		return blob.NewS3(cfg.Blob.S3Endpoint, cfg.Blob.S3Bucket, cfg.Blob.S3Region,
			key, secret, cfg.Blob.S3PathStyle), spool, nil
	default:
		return nil, "", errors.New("--blob-store is disk or s3")
	}
}
