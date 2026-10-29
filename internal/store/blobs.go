package store

import (
	"database/sql"
	"errors"
	"time"
)

// BlobMeta is one blob's row. The bytes live in the backend named by Store.
type BlobMeta struct {
	ID          int64
	Size        int64
	ContentType string
	From        string
	Store       string
	CreatedAt   time.Time
}

const blobCols = `id, size, content_type, from_label, store, created_at`

// InsertBlob records one stored blob. The bytes are the backend's business.
func (s *Store) InsertBlob(from string, tokenID *int64, size int64, contentType, backend string) (BlobMeta, error) {
	now := s.now()
	var tok any
	if tokenID != nil {
		tok = *tokenID
	}
	res, err := s.db.Exec(`INSERT INTO blobs(size, content_type, from_label, token_id, store, created_at)
		VALUES(?, ?, ?, ?, ?, ?)`, size, contentType, from, tok, backend, now.Format(eventTime))
	if err != nil {
		return BlobMeta{}, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return BlobMeta{}, err
	}
	return BlobMeta{ID: id, Size: size, ContentType: contentType, From: from, Store: backend, CreatedAt: now}, nil
}

// Blob returns one blob's metadata.
func (s *Store) Blob(id int64) (BlobMeta, error) {
	row := s.db.QueryRow(`SELECT `+blobCols+` FROM blobs WHERE id = ?`, id)
	var m BlobMeta
	var at string
	if err := row.Scan(&m.ID, &m.Size, &m.ContentType, &m.From, &m.Store, &at); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return BlobMeta{}, ErrNotFound
		}
		return BlobMeta{}, err
	}
	var err error
	if m.CreatedAt, err = time.Parse(time.RFC3339Nano, at); err != nil {
		return BlobMeta{}, err
	}
	return m, nil
}

// BlobStats is how many blobs are retained and the bytes they take together.
func (s *Store) BlobStats() (int64, int64, error) {
	var n, total sql.NullInt64
	err := s.db.QueryRow(`SELECT COUNT(*), SUM(size) FROM blobs`).Scan(&n, &total)
	return n.Int64, total.Int64, err
}

// DeleteBlob drops one blob's row. The backend object is the caller's business.
func (s *Store) DeleteBlob(id int64) error {
	_, err := s.db.Exec(`DELETE FROM blobs WHERE id = ?`, id)
	return err
}

// PruneBlobs enforces retention: nothing older than blobAge, and the total
// under blobTotal by dropping the oldest first. It returns the rows it
// dropped so the caller can delete their backend objects. A failed prune
// only delays retention; insert paths ignore its error.
func (s *Store) PruneBlobs() ([]BlobMeta, error) {
	var gone []BlobMeta
	cutoff := s.now().Add(-s.blobAge).Format(eventTime)
	rows, err := s.db.Query(`SELECT `+blobCols+` FROM blobs WHERE created_at < ? ORDER BY id`, cutoff)
	if err != nil {
		return nil, err
	}
	expired, err := collectBlobs(rows)
	if err != nil {
		return nil, err
	}
	for _, m := range expired {
		if _, err := s.db.Exec(`DELETE FROM blobs WHERE id = ?`, m.ID); err != nil {
			return gone, err
		}
		gone = append(gone, m)
	}

	// The age pass done, the cap pass drops oldest rows until the total
	// fits. It reads every row: a blob store holds thousands at most.
	var total sql.NullInt64
	if err := s.db.QueryRow(`SELECT SUM(size) FROM blobs`).Scan(&total); err != nil {
		return gone, err
	}
	if total.Int64 > s.blobTotal {
		rows, err = s.db.Query(`SELECT ` + blobCols + ` FROM blobs ORDER BY id`)
		if err != nil {
			return gone, err
		}
		over, err := collectBlobs(rows)
		if err != nil {
			return gone, err
		}
		for _, m := range over {
			if total.Int64 <= s.blobTotal {
				break
			}
			if _, err := s.db.Exec(`DELETE FROM blobs WHERE id = ?`, m.ID); err != nil {
				return gone, err
			}
			gone = append(gone, m)
			total.Int64 -= m.Size
		}
	}
	return gone, nil
}

func collectBlobs(rows *sql.Rows) ([]BlobMeta, error) {
	defer rows.Close()
	var out []BlobMeta
	for rows.Next() {
		var m BlobMeta
		var at string
		if err := rows.Scan(&m.ID, &m.Size, &m.ContentType, &m.From, &m.Store, &at); err != nil {
			return nil, err
		}
		t, err := time.Parse(time.RFC3339Nano, at)
		if err != nil {
			return nil, err
		}
		m.CreatedAt = t
		out = append(out, m)
	}
	return out, rows.Err()
}
