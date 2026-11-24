package store

import (
	"context"
	"database/sql"
	"errors"
	"strconv"
	"time"
)

// ConfigSnapshot is the configuration a writer replicates to its replica.
// Pairings, events, blobs, and per-node meta stay local.
type ConfigSnapshot struct {
	Revision  int64            `json:"revision"`
	Keys      []Key            `json:"keys"`
	Computers []ConfigComputer `json:"computers"`
	Portals   []Portal         `json:"portals"`
	Tokens    []Token          `json:"tokens"`
	Env       []EnvEntry       `json:"env"`
}

// ConfigComputer is a computer row without the sshd host key. Each node
// learns the host key from the computer's own hello, so it is not copied.
type ConfigComputer struct {
	Name      string    `json:"name"`
	TokenHash string    `json:"token_hash"`
	LoginUser string    `json:"login_user"`
	JoinedAt  time.Time `json:"joined_at"`
}

// EnvEntry is one environment variable, value included. Do not log it.
type EnvEntry struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

// ConfigSnapshot reads every replicated table in one transaction at revision.
func (s *Store) ConfigSnapshot(ctx context.Context, revision int64) (ConfigSnapshot, error) {
	snap := ConfigSnapshot{Revision: revision}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return snap, err
	}
	defer tx.Rollback()

	if snap.Keys, err = snapshotKeys(ctx, tx); err != nil {
		return snap, err
	}
	if snap.Computers, err = snapshotComputers(ctx, tx); err != nil {
		return snap, err
	}
	if snap.Portals, err = snapshotPortals(ctx, tx); err != nil {
		return snap, err
	}
	if snap.Tokens, err = snapshotTokens(ctx, tx); err != nil {
		return snap, err
	}
	if snap.Env, err = snapshotEnv(ctx, tx); err != nil {
		return snap, err
	}
	return snap, tx.Commit()
}

func snapshotKeys(ctx context.Context, tx *sql.Tx) ([]Key, error) {
	rows, err := tx.QueryContext(ctx, `SELECT id, public_key, comment, fingerprint, bound_at FROM keys ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Key{}
	for rows.Next() {
		var k Key
		var at string
		if err := rows.Scan(&k.ID, &k.Public, &k.Comment, &k.Fingerprint, &at); err != nil {
			return nil, err
		}
		if k.BoundAt, err = time.Parse(time.RFC3339Nano, at); err != nil {
			return nil, err
		}
		out = append(out, k)
	}
	return out, rows.Err()
}

func snapshotComputers(ctx context.Context, tx *sql.Tx) ([]ConfigComputer, error) {
	rows, err := tx.QueryContext(ctx, `SELECT name, token_hash, login_user, joined_at FROM computers ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []ConfigComputer{}
	for rows.Next() {
		var c ConfigComputer
		var at string
		if err := rows.Scan(&c.Name, &c.TokenHash, &c.LoginUser, &at); err != nil {
			return nil, err
		}
		if c.JoinedAt, err = time.Parse(time.RFC3339Nano, at); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func snapshotPortals(ctx context.Context, tx *sql.Tx) ([]Portal, error) {
	rows, err := tx.QueryContext(ctx, `SELECT hostname, computer, port, private, claimed_at FROM portals ORDER BY hostname`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Portal{}
	for rows.Next() {
		var p Portal
		var at string
		var private int
		if err := rows.Scan(&p.Hostname, &p.Computer, &p.Port, &private, &at); err != nil {
			return nil, err
		}
		p.Private = private != 0
		if p.ClaimedAt, err = time.Parse(time.RFC3339Nano, at); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

func snapshotTokens(ctx context.Context, tx *sql.Tx) ([]Token, error) {
	rows, err := tx.QueryContext(ctx, `SELECT id, token, comment, expiry, created_at FROM tokens ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Token{}
	for rows.Next() {
		var t Token
		var exp, created string
		if err := rows.Scan(&t.ID, &t.Token, &t.Comment, &exp, &created); err != nil {
			return nil, err
		}
		if exp != "" {
			if t.Expiry, err = time.Parse(time.RFC3339Nano, exp); err != nil {
				return nil, err
			}
		}
		if t.Created, err = time.Parse(time.RFC3339Nano, created); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

func snapshotEnv(ctx context.Context, tx *sql.Tx) ([]EnvEntry, error) {
	rows, err := tx.QueryContext(ctx, `SELECT name, value FROM env ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []EnvEntry{}
	for rows.Next() {
		var e EnvEntry
		if err := rows.Scan(&e.Name, &e.Value); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// ApplyConfigSnapshot replaces the replicated tables when snap is newer than
// the local revision. It reports whether anything was written. Host keys are
// kept for computers that still exist: they describe this node's last hello.
func (s *Store) ApplyConfigSnapshot(ctx context.Context, snap ConfigSnapshot) (bool, error) {
	if snap.Revision < 1 {
		return false, errors.New("invalid configuration revision")
	}
	if err := validateSnapshot(snap); err != nil {
		return false, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()

	var current int64
	err = tx.QueryRowContext(ctx, `SELECT value FROM meta WHERE key = 'config_revision'`).Scan(&current)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return false, err
	}
	if current >= snap.Revision {
		return false, nil
	}
	hostKeys, err := existingHostKeys(ctx, tx)
	if err != nil {
		return false, err
	}
	for _, table := range []string{"keys", "portals", "tokens", "env", "computers"} {
		if _, err := tx.ExecContext(ctx, `DELETE FROM `+table); err != nil {
			return false, err
		}
	}
	for _, k := range snap.Keys {
		if _, err := tx.ExecContext(ctx, `INSERT INTO keys(id, public_key, comment, fingerprint, bound_at) VALUES(?, ?, ?, ?, ?)`,
			k.ID, k.Public, k.Comment, k.Fingerprint, k.BoundAt.UTC().Format(time.RFC3339Nano)); err != nil {
			return false, err
		}
	}
	for _, c := range snap.Computers {
		if _, err := tx.ExecContext(ctx, `INSERT INTO computers(name, token_hash, login_user, host_key, joined_at) VALUES(?, ?, ?, ?, ?)`,
			c.Name, c.TokenHash, c.LoginUser, hostKeys[c.Name], c.JoinedAt.UTC().Format(time.RFC3339Nano)); err != nil {
			return false, err
		}
	}
	for _, p := range snap.Portals {
		if _, err := tx.ExecContext(ctx, `INSERT INTO portals(hostname, computer, port, private, claimed_at) VALUES(?, ?, ?, ?, ?)`,
			p.Hostname, p.Computer, p.Port, boolInt(p.Private), p.ClaimedAt.UTC().Format(time.RFC3339Nano)); err != nil {
			return false, err
		}
	}
	for _, t := range snap.Tokens {
		exp := ""
		if !t.Expiry.IsZero() {
			exp = t.Expiry.UTC().Format(time.RFC3339Nano)
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO tokens(id, token, comment, expiry, created_at) VALUES(?, ?, ?, ?, ?)`,
			t.ID, t.Token, t.Comment, exp, t.Created.UTC().Format(time.RFC3339Nano)); err != nil {
			return false, err
		}
	}
	for _, e := range snap.Env {
		if _, err := tx.ExecContext(ctx, `INSERT INTO env(name, value) VALUES(?, ?)`, e.Name, e.Value); err != nil {
			return false, err
		}
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO meta(key, value) VALUES('config_revision', ?)
		ON CONFLICT(key) DO UPDATE SET value = excluded.value`, strconv.FormatInt(snap.Revision, 10)); err != nil {
		return false, err
	}
	if err := tx.Commit(); err != nil {
		return false, err
	}
	return true, nil
}

func existingHostKeys(ctx context.Context, tx *sql.Tx) (map[string]string, error) {
	rows, err := tx.QueryContext(ctx, `SELECT name, host_key FROM computers`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var name, key string
		if err := rows.Scan(&name, &key); err != nil {
			return nil, err
		}
		out[name] = key
	}
	return out, rows.Err()
}

func validateSnapshot(snap ConfigSnapshot) error {
	for _, k := range snap.Keys {
		if k.Public == "" || k.Fingerprint == "" {
			return errors.New("configuration snapshot has a key without a public key or fingerprint")
		}
	}
	for _, c := range snap.Computers {
		if c.Name == "" || c.TokenHash == "" || len(c.TokenHash) > 256 {
			return errors.New("configuration snapshot has an invalid computer")
		}
	}
	for _, p := range snap.Portals {
		if p.Hostname == "" || p.Computer == "" || p.Port < 1 || p.Port > 65535 {
			return errors.New("configuration snapshot has an invalid portal")
		}
	}
	for _, t := range snap.Tokens {
		if t.ID < 1 || t.Token == "" {
			return errors.New("configuration snapshot has an invalid token")
		}
	}
	for _, e := range snap.Env {
		if e.Name == "" {
			return errors.New("configuration snapshot has an invalid env name")
		}
	}
	return nil
}

// ConfigRevision is the last configuration revision this database holds.
func (s *Store) ConfigRevision(ctx context.Context) (int64, error) {
	var raw string
	err := s.db.QueryRowContext(ctx, `SELECT value FROM meta WHERE key = 'config_revision'`).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	n, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || n < 0 {
		return 0, nil
	}
	return n, nil
}

// BumpConfigRevision reserves the next revision for a writer's change.
func (s *Store) BumpConfigRevision(ctx context.Context) (int64, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	var raw string
	err = tx.QueryRowContext(ctx, `SELECT value FROM meta WHERE key = 'config_revision'`).Scan(&raw)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return 0, err
	}
	current, _ := strconv.ParseInt(raw, 10, 64)
	next := current + 1
	if _, err := tx.ExecContext(ctx, `INSERT INTO meta(key, value) VALUES('config_revision', ?)
		ON CONFLICT(key) DO UPDATE SET value = excluded.value`, strconv.FormatInt(next, 10)); err != nil {
		return 0, err
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return next, nil
}
