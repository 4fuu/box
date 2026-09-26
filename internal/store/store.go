// Package store is the server's SQLite file.
// Env values and computer tokens live here. List methods do not return env values.
// There is no migration: a new database only.
package store

import (
	"crypto/subtle"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/4fuu/box/internal/secret"
	_ "modernc.org/sqlite"
)

// maxPairingAttempts is how many wrong guesses the sole live password survives.
// Several live passwords are not counted: a miss does not identify which one.
const maxPairingAttempts = 5

var (
	ErrNotFound = errors.New("not found")
	ErrExists   = errors.New("exists")
	ErrExpired  = errors.New("expired")
	ErrUsed     = errors.New("used")
)

// HeldError is a portal hostname owned by another computer.
type HeldError struct {
	Hostname string
	Holder   string
}

func (e *HeldError) Error() string {
	return e.Hostname + " is held by " + e.Holder
}

// Store is one SQLite file. It is safe for concurrent use; the driver is limited to one connection.
type Store struct {
	db    *sql.DB
	path  string
	nowfn func() time.Time
}

// Key is a bound client public key. The splice key is not a row.
type Key struct {
	ID          int64
	Public      string
	Comment     string
	Fingerprint string
	BoundAt     time.Time
}

// Computer is one joined machine. TokenHash is secret.Hash of the raw token.
// Online is not stored: a computer is online while its tunnel connection lives.
type Computer struct {
	Name      string
	TokenHash string
	LoginUser string
	HostKey   string
	JoinedAt  time.Time
}

// Portal is one claimed hostname.
type Portal struct {
	Hostname  string
	Computer  string
	Port      int
	ClaimedAt time.Time
}

func Open(path string) (*Store, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	dsn := "file:" + path + "?_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	s := &Store{db: db, path: path, nowfn: func() time.Time { return time.Now().UTC() }}
	if err := s.migrate(); err != nil {
		db.Close()
		return nil, err
	}
	if err := s.tighten(); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

func (s *Store) Close() error { return s.db.Close() }

func (s *Store) SetNow(fn func() time.Time) {
	if fn != nil {
		s.nowfn = fn
	}
}

func (s *Store) now() time.Time { return s.nowfn().UTC() }

func (s *Store) tighten() error {
	if err := os.Chmod(filepath.Dir(s.path), 0o700); err != nil {
		return err
	}
	for _, suf := range []string{"", "-wal", "-shm"} {
		p := s.path + suf
		if _, err := os.Stat(p); err == nil {
			if err := os.Chmod(p, 0o600); err != nil {
				return err
			}
		}
	}
	return nil
}

func (s *Store) migrate() error {
	_, err := s.db.Exec(schema)
	return err
}

const schema = `
CREATE TABLE IF NOT EXISTS meta (
  key TEXT PRIMARY KEY,
  value TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS keys (
  id INTEGER PRIMARY KEY,
  public_key TEXT NOT NULL UNIQUE,
  comment TEXT NOT NULL DEFAULT '',
  fingerprint TEXT NOT NULL UNIQUE,
  bound_at TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS pairings (
  id INTEGER PRIMARY KEY,
  hash TEXT NOT NULL UNIQUE,
  expiry TEXT NOT NULL,
  used_at TEXT,
  attempts INTEGER NOT NULL DEFAULT 0
);
CREATE TABLE IF NOT EXISTS computers (
  name TEXT PRIMARY KEY,
  token_hash TEXT NOT NULL UNIQUE,
  login_user TEXT NOT NULL DEFAULT '',
  host_key TEXT NOT NULL DEFAULT '',
  joined_at TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS portals (
  hostname TEXT PRIMARY KEY,
  computer TEXT NOT NULL,
  port INTEGER NOT NULL,
  claimed_at TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS env (
  name TEXT PRIMARY KEY,
  value TEXT NOT NULL
);
`

func (s *Store) Meta(key string) (string, bool, error) {
	var v string
	err := s.db.QueryRow(`SELECT value FROM meta WHERE key = ?`, key).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return v, true, nil
}

func (s *Store) SetMeta(key, value string) error {
	_, err := s.db.Exec(`INSERT INTO meta(key, value) VALUES(?, ?)
		ON CONFLICT(key) DO UPDATE SET value = excluded.value`, key, value)
	return err
}

// NewPairing stores only the hash of a client one-time password.
// The returned expiry is when the password stops working.
func (s *Store) NewPairing(raw string, ttl time.Duration) (time.Time, error) {
	if raw == "" {
		return time.Time{}, errors.New("invalid pairing")
	}
	exp := s.now().Add(ttl)
	_, err := s.db.Exec(`INSERT INTO pairings(hash, expiry, attempts) VALUES(?, ?, 0)`,
		secret.Hash(raw), exp.Format(time.RFC3339Nano))
	if err != nil {
		return time.Time{}, err
	}
	return exp, nil
}

// HasLivePairing reports whether an unused, unexpired password still accepts guesses.
func (s *Store) HasLivePairing() (bool, error) {
	var n int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM pairings
		WHERE used_at IS NULL AND attempts < ? AND expiry > ?`,
		maxPairingAttempts, s.now().Format(time.RFC3339Nano)).Scan(&n)
	return n > 0, err
}

// ConsumePairing marks a one-time password used.
// A wrong guess against the only live password counts as an attempt.
// Five of those burn it. A wrong guess against several live passwords burns none.
func (s *Store) ConsumePairing(raw string) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var id int64
	var expiry string
	var used sql.NullString
	var attempts int
	err = tx.QueryRow(`SELECT id, expiry, used_at, attempts FROM pairings WHERE hash = ?`,
		secret.Hash(raw)).Scan(&id, &expiry, &used, &attempts)
	if errors.Is(err, sql.ErrNoRows) {
		if err := s.noteMiss(tx); err != nil {
			return err
		}
		if err := tx.Commit(); err != nil {
			return err
		}
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	if used.Valid || attempts >= maxPairingAttempts {
		return ErrUsed
	}
	exp, err := time.Parse(time.RFC3339Nano, expiry)
	if err != nil {
		return err
	}
	if !s.now().Before(exp) {
		return ErrExpired
	}
	if _, err := tx.Exec(`UPDATE pairings SET used_at = ? WHERE id = ? AND used_at IS NULL`,
		s.now().Format(time.RFC3339Nano), id); err != nil {
		return err
	}
	return tx.Commit()
}

// noteMiss counts a wrong password against the sole live pairing.
// The query is closed before the update: one connection cannot do both at once.
func (s *Store) noteMiss(tx *sql.Tx) error {
	rows, err := tx.Query(`SELECT id, attempts FROM pairings
		WHERE used_at IS NULL AND attempts < ? AND expiry > ?`,
		maxPairingAttempts, s.now().Format(time.RFC3339Nano))
	if err != nil {
		return err
	}
	var id int64
	var attempts int
	n := 0
	scanErr := error(nil)
	for rows.Next() {
		if scanErr = rows.Scan(&id, &attempts); scanErr != nil {
			break
		}
		n++
	}
	if scanErr == nil {
		scanErr = rows.Err()
	}
	rows.Close()
	if scanErr != nil {
		return scanErr
	}
	if n != 1 {
		return nil
	}
	attempts++
	if attempts >= maxPairingAttempts {
		_, err = tx.Exec(`UPDATE pairings SET attempts = ?, used_at = ? WHERE id = ? AND used_at IS NULL`,
			attempts, s.now().Format(time.RFC3339Nano), id)
		return err
	}
	_, err = tx.Exec(`UPDATE pairings SET attempts = ? WHERE id = ? AND used_at IS NULL`, attempts, id)
	return err
}

func (s *Store) BindKey(public, comment, fingerprint string) error {
	_, err := s.db.Exec(`INSERT INTO keys(public_key, comment, fingerprint, bound_at) VALUES(?, ?, ?, ?)`,
		public, comment, fingerprint, s.now().Format(time.RFC3339Nano))
	return err
}

func (s *Store) ListKeys() ([]Key, error) {
	rows, err := s.db.Query(`SELECT id, public_key, comment, fingerprint, bound_at FROM keys ORDER BY bound_at, id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Key
	for rows.Next() {
		var k Key
		var at string
		if err := rows.Scan(&k.ID, &k.Public, &k.Comment, &k.Fingerprint, &at); err != nil {
			return nil, err
		}
		k.BoundAt, err = time.Parse(time.RFC3339Nano, at)
		if err != nil {
			return nil, err
		}
		out = append(out, k)
	}
	return out, rows.Err()
}

// FindKeyByFingerprint looks up a bound key by fingerprint, or by a unique comment.
func (s *Store) FindKeyByFingerprint(fp string) (Key, error) {
	keys, err := s.ListKeys()
	if err != nil {
		return Key{}, err
	}
	var match []Key
	for _, k := range keys {
		if k.Fingerprint == fp {
			return k, nil
		}
	}
	for _, k := range keys {
		if k.Comment == fp && fp != "" {
			match = append(match, k)
		}
	}
	if len(match) == 1 {
		return match[0], nil
	}
	if len(match) > 1 {
		return Key{}, errors.New("ambiguous key")
	}
	return Key{}, ErrNotFound
}

func (s *Store) RemoveKey(id int64) error {
	res, err := s.db.Exec(`DELETE FROM keys WHERE id = ?`, id)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// CreateComputer stores the token hash, never the raw token.
func (s *Store) CreateComputer(name, tokenHash, loginUser string) error {
	if name == "" || tokenHash == "" {
		return errors.New("invalid computer")
	}
	_, err := s.db.Exec(`INSERT INTO computers(name, token_hash, login_user, host_key, joined_at) VALUES(?, ?, ?, '', ?)`,
		name, tokenHash, loginUser, s.now().Format(time.RFC3339Nano))
	if err != nil {
		if isUnique(err) {
			return ErrExists
		}
		return err
	}
	return nil
}

func (s *Store) Computer(name string) (Computer, error) {
	var c Computer
	var at string
	err := s.db.QueryRow(`SELECT name, token_hash, login_user, host_key, joined_at FROM computers WHERE name = ?`, name).
		Scan(&c.Name, &c.TokenHash, &c.LoginUser, &c.HostKey, &at)
	if errors.Is(err, sql.ErrNoRows) {
		return Computer{}, ErrNotFound
	}
	if err != nil {
		return Computer{}, err
	}
	c.JoinedAt, err = time.Parse(time.RFC3339Nano, at)
	return c, err
}

func (s *Store) ListComputers() ([]Computer, error) {
	rows, err := s.db.Query(`SELECT name FROM computers ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var names []string
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			return nil, err
		}
		names = append(names, n)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	var out []Computer
	for _, name := range names {
		c, err := s.Computer(name)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, nil
}

// TokenMatches compares secret.Hash(token) to the stored hash in constant time.
// A missing computer fails and still does a compare, so the name does not change the shape.
func (s *Store) TokenMatches(name, token string) (bool, error) {
	got := secret.Hash(token)
	var hash string
	err := s.db.QueryRow(`SELECT token_hash FROM computers WHERE name = ?`, name).Scan(&hash)
	if errors.Is(err, sql.ErrNoRows) {
		subtle.ConstantTimeCompare([]byte(got), []byte(strings.Repeat("0", len(got))))
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if len(hash) != len(got) {
		return false, nil
	}
	return subtle.ConstantTimeCompare([]byte(hash), []byte(got)) == 1, nil
}

// SetComputerInfo stores the login user and sshd host key reported at hello.
// Empty fields are left as they were.
func (s *Store) SetComputerInfo(name, loginUser, hostKey string) error {
	c, err := s.Computer(name)
	if err != nil {
		return err
	}
	if loginUser != "" {
		c.LoginUser = loginUser
	}
	if hostKey != "" {
		c.HostKey = hostKey
	}
	res, err := s.db.Exec(`UPDATE computers SET login_user = ?, host_key = ? WHERE name = ?`,
		c.LoginUser, c.HostKey, name)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) RenameComputer(oldName, newName string) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	res, err := tx.Exec(`UPDATE computers SET name = ? WHERE name = ?`, newName, oldName)
	if err != nil {
		if isUnique(err) {
			return ErrExists
		}
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	if _, err := tx.Exec(`UPDATE portals SET computer = ? WHERE computer = ?`, newName, oldName); err != nil {
		return err
	}
	return tx.Commit()
}

// DeleteComputer revokes the token and drops the computer's portals.
func (s *Store) DeleteComputer(name string) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`DELETE FROM portals WHERE computer = ?`, name); err != nil {
		return err
	}
	res, err := tx.Exec(`DELETE FROM computers WHERE name = ?`, name)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return tx.Commit()
}

// ClaimPortal claims hostname for computer. The same computer may update the port.
// A different holder returns HeldError.
func (s *Store) ClaimPortal(hostname, computer string, port int) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var holder string
	err = tx.QueryRow(`SELECT computer FROM portals WHERE hostname = ?`, hostname).Scan(&holder)
	if err == nil {
		if holder != computer {
			return &HeldError{Hostname: hostname, Holder: holder}
		}
		_, err = tx.Exec(`UPDATE portals SET port = ?, claimed_at = ? WHERE hostname = ?`,
			port, s.now().Format(time.RFC3339Nano), hostname)
		if err != nil {
			return err
		}
		return tx.Commit()
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	_, err = tx.Exec(`INSERT INTO portals(hostname, computer, port, claimed_at) VALUES(?, ?, ?, ?)`,
		hostname, computer, port, s.now().Format(time.RFC3339Nano))
	if err != nil {
		if isUnique(err) {
			return &HeldError{Hostname: hostname, Holder: "unknown"}
		}
		return err
	}
	return tx.Commit()
}

func (s *Store) PortalByHost(host string) (Portal, error) {
	var p Portal
	var at string
	err := s.db.QueryRow(`SELECT hostname, computer, port, claimed_at FROM portals WHERE hostname = ?`, host).
		Scan(&p.Hostname, &p.Computer, &p.Port, &at)
	if errors.Is(err, sql.ErrNoRows) {
		return Portal{}, ErrNotFound
	}
	if err != nil {
		return Portal{}, err
	}
	p.ClaimedAt, err = time.Parse(time.RFC3339Nano, at)
	return p, err
}

func (s *Store) PortalsByComputer(name string) ([]Portal, error) {
	rows, err := s.db.Query(`SELECT hostname FROM portals WHERE computer = ? ORDER BY hostname`, name)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var hosts []string
	for rows.Next() {
		var h string
		if err := rows.Scan(&h); err != nil {
			return nil, err
		}
		hosts = append(hosts, h)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	var out []Portal
	for _, h := range hosts {
		p, err := s.PortalByHost(h)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, nil
}

// ReleasePortal drops a claim the computer holds. Another computer's claim is left alone.
func (s *Store) ReleasePortal(hostname, computer string) error {
	res, err := s.db.Exec(`DELETE FROM portals WHERE hostname = ? AND computer = ?`, hostname, computer)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) SetEnv(name, value string) error {
	_, err := s.db.Exec(`INSERT INTO env(name, value) VALUES(?, ?)
		ON CONFLICT(name) DO UPDATE SET value = excluded.value`, name, value)
	return err
}

// EnvNames returns names only.
func (s *Store) EnvNames() ([]string, error) {
	rows, err := s.db.Query(`SELECT name FROM env ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, rows.Err()
}

// EnvAll returns values for a push to a computer. Do not print the result.
func (s *Store) EnvAll() (map[string]string, error) {
	rows, err := s.db.Query(`SELECT name, value FROM env ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var n, v string
		if err := rows.Scan(&n, &v); err != nil {
			return nil, err
		}
		out[n] = v
	}
	return out, rows.Err()
}

func (s *Store) DeleteEnv(name string) error {
	res, err := s.db.Exec(`DELETE FROM env WHERE name = ?`, name)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

func isUnique(err error) bool {
	return err != nil && strings.Contains(strings.ToLower(err.Error()), "unique")
}
