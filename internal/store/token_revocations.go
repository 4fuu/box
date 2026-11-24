package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"strings"

	"github.com/4fuu/box/internal/secret"
)

// QueueTokenRevocation removes local access and records work for the fixed writer
// to send to its replica. The returned hash, not the reusable row ID, is the
// identity of the operation to retry.
func (s *Store) QueueTokenRevocation(ctx context.Context, id int64) (string, error) {
	var hash string
	err := s.durableTokenTransaction(ctx, func(tx *sql.Tx) error {
		var raw string
		if err := tx.QueryRowContext(ctx, `DELETE FROM tokens WHERE id = ? RETURNING token`, id).Scan(&raw); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return ErrNotFound
			}
			return err
		}
		hash = secret.Hash(raw)
		_, err := tx.ExecContext(ctx, `INSERT INTO token_revocations(hash, pending) VALUES(?, 1)`, hash)
		return err
	})
	if err != nil {
		return "", err
	}
	return hash, nil
}

// PendingTokenRevocations returns the hashes not yet acknowledged by the replica.
func (s *Store) PendingTokenRevocations(ctx context.Context) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT hash FROM token_revocations WHERE pending = 1 ORDER BY hash`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var hashes []string
	for rows.Next() {
		var hash string
		if err := rows.Scan(&hash); err != nil {
			return nil, err
		}
		hashes = append(hashes, hash)
	}
	return hashes, rows.Err()
}

// ApplyTokenRevocation records and applies a replica revocation before the
// sender can acknowledge it. Replaying the hash is safe even if IDs differ.
func (s *Store) ApplyTokenRevocation(ctx context.Context, hash string) error {
	digest, err := hex.DecodeString(hash)
	if err != nil || len(digest) != sha256.Size || hash != strings.ToLower(hash) {
		return errors.New("invalid token hash")
	}
	return s.durableTokenTransaction(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `INSERT INTO token_revocations(hash, pending) VALUES(?, 0) ON CONFLICT DO NOTHING`, hash)
		if err != nil {
			return err
		}
		if n, err := res.RowsAffected(); err != nil || n == 0 {
			return err
		}
		rows, err := tx.QueryContext(ctx, `SELECT token FROM tokens`)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var raw string
			if err := rows.Scan(&raw); err != nil {
				return err
			}
			if secret.Hash(raw) == hash {
				if err := rows.Close(); err != nil {
					return err
				}
				_, err := tx.ExecContext(ctx, `DELETE FROM tokens WHERE token = ?`, raw)
				return err
			}
		}
		return rows.Err()
	})
}

// ConfirmTokenRevocation clears pending only after the replica commits.
// Losing this update on restart causes a safe replay, not a lost revocation.
func (s *Store) ConfirmTokenRevocation(ctx context.Context, hash string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE token_revocations SET pending = 0 WHERE hash = ?`, hash)
	return err
}

func (s *Store) durableTokenTransaction(ctx context.Context, apply func(*sql.Tx) error) error {
	conn, err := s.db.Conn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	if _, err := conn.ExecContext(ctx, `PRAGMA synchronous = FULL`); err != nil {
		return err
	}
	defer conn.ExecContext(context.Background(), `PRAGMA synchronous = NORMAL`)
	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := apply(tx); err != nil {
		return err
	}
	return tx.Commit()
}
