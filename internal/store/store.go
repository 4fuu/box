// Package store is the server's SQLite file.
// Env values and node tokens live here. List methods do not return env values.
package store

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/4fuu/box/internal/secret"
	_ "modernc.org/sqlite"
)

const (
	KindClient = "client"
	KindNode   = "node"

	StateRunning = "running"
	StateStopped = "stopped"
)

var (
	ErrNotFound = errors.New("not found")
	ErrExists   = errors.New("exists")
	ErrInUse    = errors.New("in use")
	ErrExpired  = errors.New("expired")
	ErrUsed     = errors.New("used")
)

// HeldError is a portal label owned by another container.
type HeldError struct {
	Label  string
	Holder string
}

func (e *HeldError) Error() string {
	return e.Label + " is held by " + e.Holder
}

type Store struct {
	db    *sql.DB
	path  string
	nowfn func() time.Time
}

type Key struct {
	ID          int64
	Public      string
	Comment     string
	Fingerprint string
	BoundAt     time.Time
}

type Node struct {
	ID            string
	Name          string
	Tags          map[string]string
	LastHeartbeat time.Time
	CPU           int
	Memory        int64
	Disk          int64
	UsedCPU       float64
	UsedMemory    int64
	UsedDisk      int64
	Images        []string
}

type Image struct {
	Name    string
	Ref     string
	Default bool
}

type Computer struct {
	Name      string
	NodeID    string
	NodeName  string
	Image     string
	CPU       float64
	Memory    int64
	Disk      int64
	State     string
	Heartbeat time.Time
}

type Portal struct {
	Hostname  string
	Label     string
	Container string
	NodeID    string
	Port      int
	ClaimedAt time.Time
}

type Heartbeat struct {
	CPU        int
	Memory     int64
	Disk       int64
	UsedCPU    float64
	UsedMemory int64
	UsedDisk   int64
	Images     []string
	Computers  []ReportedComputer
}

type ReportedComputer struct {
	Name  string
	State string
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

// Now is the clock used for expiry and heartbeats.
func (s *Store) Now() time.Time { return s.now() }

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
  kind TEXT NOT NULL,
  hash TEXT NOT NULL UNIQUE,
  expiry TEXT NOT NULL,
  used_at TEXT
);
CREATE TABLE IF NOT EXISTS nodes (
  id TEXT PRIMARY KEY,
  name TEXT NOT NULL UNIQUE,
  tags TEXT NOT NULL DEFAULT '{}',
  token TEXT NOT NULL,
  token_hash TEXT NOT NULL UNIQUE,
  last_heartbeat TEXT,
  cpu INTEGER NOT NULL DEFAULT 0,
  memory INTEGER NOT NULL DEFAULT 0,
  disk INTEGER NOT NULL DEFAULT 0,
  used_cpu REAL NOT NULL DEFAULT 0,
  used_memory INTEGER NOT NULL DEFAULT 0,
  used_disk INTEGER NOT NULL DEFAULT 0
);
CREATE TABLE IF NOT EXISTS images (
  name TEXT PRIMARY KEY,
  ref TEXT NOT NULL,
  is_default INTEGER NOT NULL DEFAULT 0
);
CREATE TABLE IF NOT EXISTS node_images (
  node_id TEXT NOT NULL REFERENCES nodes(id),
  image TEXT NOT NULL,
  PRIMARY KEY (node_id, image)
);
CREATE TABLE IF NOT EXISTS computers (
  name TEXT PRIMARY KEY,
  node_id TEXT NOT NULL REFERENCES nodes(id),
  image TEXT NOT NULL,
  cpu REAL NOT NULL,
  memory INTEGER NOT NULL,
  disk INTEGER NOT NULL,
  state TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS portals (
  hostname TEXT PRIMARY KEY,
  label TEXT NOT NULL,
  container TEXT NOT NULL,
  node_id TEXT NOT NULL,
  port INTEGER NOT NULL,
  claimed_at TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS env (
  name TEXT PRIMARY KEY,
  value TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS shares (
  computer TEXT NOT NULL,
  key_id INTEGER NOT NULL REFERENCES keys(id),
  web INTEGER NOT NULL DEFAULT 0,
  ssh INTEGER NOT NULL DEFAULT 0,
  PRIMARY KEY (computer, key_id)
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

// NewPairing stores only the hash. The returned secret is shown once.
func (s *Store) NewPairing(kind string, raw string, ttl time.Duration) (time.Time, error) {
	if kind != KindClient && kind != KindNode {
		return time.Time{}, fmt.Errorf("invalid pairing")
	}
	if raw == "" {
		return time.Time{}, fmt.Errorf("invalid pairing")
	}
	exp := s.now().Add(ttl)
	_, err := s.db.Exec(`INSERT INTO pairings(kind, hash, expiry) VALUES(?, ?, ?)`,
		kind, secret.Hash(raw), exp.Format(time.RFC3339Nano))
	if err != nil {
		return time.Time{}, err
	}
	return exp, nil
}

// HasLivePairing reports whether an unused, unexpired pairing of kind exists.
func (s *Store) HasLivePairing(kind string) (bool, error) {
	var n int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM pairings WHERE kind = ? AND used_at IS NULL AND expiry > ?`,
		kind, s.now().Format(time.RFC3339Nano)).Scan(&n)
	return n > 0, err
}

// ConsumePairing marks a pairing used. Wrong, used, and expired secrets fail.
func (s *Store) ConsumePairing(kind, raw string) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var id int64
	var expiry, used sql.NullString
	err = tx.QueryRow(`SELECT id, expiry, used_at FROM pairings WHERE kind = ? AND hash = ?`,
		kind, secret.Hash(raw)).Scan(&id, &expiry, &used)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	if used.Valid {
		return ErrUsed
	}
	exp, err := time.Parse(time.RFC3339Nano, expiry.String)
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

func (s *Store) BindKey(public, comment, fingerprint string) error {
	_, err := s.db.Exec(`INSERT INTO keys(public_key, comment, fingerprint, bound_at) VALUES(?, ?, ?, ?)`,
		public, comment, fingerprint, s.now().Format(time.RFC3339Nano))
	if err != nil {
		return err
	}
	return nil
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
		return Key{}, fmt.Errorf("ambiguous key")
	}
	return Key{}, ErrNotFound
}

func (s *Store) RemoveKey(id int64) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`DELETE FROM shares WHERE key_id = ?`, id); err != nil {
		return err
	}
	res, err := tx.Exec(`DELETE FROM keys WHERE id = ?`, id)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return tx.Commit()
}

func (s *Store) CreateNode(id, name, token string) error {
	_, err := s.db.Exec(`INSERT INTO nodes(id, name, tags, token, token_hash) VALUES(?, ?, '{}', ?, ?)`,
		id, name, token, secret.Hash(token))
	return err
}

func (s *Store) NodeByName(name string) (Node, error) {
	return s.scanNode(`SELECT id, name, tags, last_heartbeat, cpu, memory, disk, used_cpu, used_memory, used_disk FROM nodes WHERE name = ?`, name)
}

func (s *Store) NodeByID(id string) (Node, error) {
	return s.scanNode(`SELECT id, name, tags, last_heartbeat, cpu, memory, disk, used_cpu, used_memory, used_disk FROM nodes WHERE id = ?`, id)
}

func (s *Store) NodeByToken(token string) (Node, error) {
	return s.scanNode(`SELECT id, name, tags, last_heartbeat, cpu, memory, disk, used_cpu, used_memory, used_disk FROM nodes WHERE token_hash = ?`, secret.Hash(token))
}

// NodeToken is the pairing token. Callers use it as the STCP secret. Do not print it.
func (s *Store) NodeToken(id string) (string, error) {
	var tok string
	err := s.db.QueryRow(`SELECT token FROM nodes WHERE id = ?`, id).Scan(&tok)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrNotFound
	}
	return tok, err
}

func (s *Store) scanNode(q string, arg any) (Node, error) {
	var n Node
	var tags, beat sql.NullString
	err := s.db.QueryRow(q, arg).Scan(&n.ID, &n.Name, &tags, &beat, &n.CPU, &n.Memory, &n.Disk, &n.UsedCPU, &n.UsedMemory, &n.UsedDisk)
	if errors.Is(err, sql.ErrNoRows) {
		return Node{}, ErrNotFound
	}
	if err != nil {
		return Node{}, err
	}
	n.Tags = map[string]string{}
	if tags.Valid && tags.String != "" {
		if err := json.Unmarshal([]byte(tags.String), &n.Tags); err != nil {
			return Node{}, err
		}
	}
	if beat.Valid && beat.String != "" {
		n.LastHeartbeat, err = time.Parse(time.RFC3339Nano, beat.String)
		if err != nil {
			return Node{}, err
		}
	}
	imgs, err := s.nodeImages(n.ID)
	if err != nil {
		return Node{}, err
	}
	n.Images = imgs
	return n, nil
}

// AddNodeImage records a pull without waiting for the next heartbeat.
func (s *Store) AddNodeImage(nodeID, image string) error {
	_, err := s.db.Exec(`INSERT INTO node_images(node_id, image) VALUES(?, ?) ON CONFLICT DO NOTHING`, nodeID, image)
	return err
}

func (s *Store) nodeImages(id string) ([]string, error) {
	rows, err := s.db.Query(`SELECT image FROM node_images WHERE node_id = ? ORDER BY image`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		out = append(out, name)
	}
	return out, rows.Err()
}

func (s *Store) ListNodes() ([]Node, error) {
	rows, err := s.db.Query(`SELECT name FROM nodes ORDER BY name`)
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
	var out []Node
	for _, name := range names {
		n, err := s.NodeByName(name)
		if err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, nil
}

func (s *Store) TagNode(name, key, val string) error {
	n, err := s.NodeByName(name)
	if err != nil {
		return err
	}
	if n.Tags == nil {
		n.Tags = map[string]string{}
	}
	n.Tags[key] = val
	raw, err := json.Marshal(n.Tags)
	if err != nil {
		return err
	}
	_, err = s.db.Exec(`UPDATE nodes SET tags = ? WHERE id = ?`, string(raw), n.ID)
	return err
}

func (s *Store) ComputerCount(nodeID string) (int, error) {
	var n int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM computers WHERE node_id = ?`, nodeID).Scan(&n)
	return n, err
}

func (s *Store) DeleteNode(id string) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var n int
	if err := tx.QueryRow(`SELECT COUNT(*) FROM computers WHERE node_id = ?`, id).Scan(&n); err != nil {
		return err
	}
	if n > 0 {
		return ErrInUse
	}
	if _, err := tx.Exec(`DELETE FROM node_images WHERE node_id = ?`, id); err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM nodes WHERE id = ?`, id); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) Heartbeat(id string, hb Heartbeat) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	res, err := tx.Exec(`UPDATE nodes SET last_heartbeat = ?, cpu = ?, memory = ?, disk = ?, used_cpu = ?, used_memory = ?, used_disk = ? WHERE id = ?`,
		s.now().Format(time.RFC3339Nano), hb.CPU, hb.Memory, hb.Disk, hb.UsedCPU, hb.UsedMemory, hb.UsedDisk, id)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	if _, err := tx.Exec(`DELETE FROM node_images WHERE node_id = ?`, id); err != nil {
		return err
	}
	for _, img := range hb.Images {
		if _, err := tx.Exec(`INSERT INTO node_images(node_id, image) VALUES(?, ?)`, id, img); err != nil {
			return err
		}
	}
	for _, c := range hb.Computers {
		if _, err := tx.Exec(`UPDATE computers SET state = ? WHERE name = ? AND node_id = ?`, c.State, c.Name, id); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *Store) AddImage(name, ref string) error {
	_, err := s.db.Exec(`INSERT INTO images(name, ref, is_default) VALUES(?, ?, 0)`, name, ref)
	return err
}

func (s *Store) Image(name string) (Image, error) {
	var img Image
	var def int
	err := s.db.QueryRow(`SELECT name, ref, is_default FROM images WHERE name = ?`, name).Scan(&img.Name, &img.Ref, &def)
	if errors.Is(err, sql.ErrNoRows) {
		return Image{}, ErrNotFound
	}
	img.Default = def == 1
	return img, err
}

func (s *Store) ListImages() ([]Image, error) {
	rows, err := s.db.Query(`SELECT name, ref, is_default FROM images ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Image
	for rows.Next() {
		var img Image
		var def int
		if err := rows.Scan(&img.Name, &img.Ref, &def); err != nil {
			return nil, err
		}
		img.Default = def == 1
		out = append(out, img)
	}
	return out, rows.Err()
}

func (s *Store) DefaultImage() (Image, error) {
	var img Image
	var def int
	err := s.db.QueryRow(`SELECT name, ref, is_default FROM images WHERE is_default = 1`).Scan(&img.Name, &img.Ref, &def)
	if errors.Is(err, sql.ErrNoRows) {
		return Image{}, ErrNotFound
	}
	img.Default = true
	return img, err
}

func (s *Store) SetDefaultImage(name string) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var n int
	if err := tx.QueryRow(`SELECT COUNT(*) FROM images WHERE name = ?`, name).Scan(&n); err != nil {
		return err
	}
	if n == 0 {
		return ErrNotFound
	}
	if _, err := tx.Exec(`UPDATE images SET is_default = 0`); err != nil {
		return err
	}
	if _, err := tx.Exec(`UPDATE images SET is_default = 1 WHERE name = ?`, name); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) ImageUsers(name string) (int, error) {
	var n int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM computers WHERE image = ?`, name).Scan(&n)
	return n, err
}

func (s *Store) DeleteImage(name string) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var n int
	if err := tx.QueryRow(`SELECT COUNT(*) FROM computers WHERE image = ?`, name).Scan(&n); err != nil {
		return err
	}
	if n > 0 {
		return ErrInUse
	}
	if _, err := tx.Exec(`DELETE FROM node_images WHERE image = ?`, name); err != nil {
		return err
	}
	res, err := tx.Exec(`DELETE FROM images WHERE name = ?`, name)
	if err != nil {
		return err
	}
	affected, _ := res.RowsAffected()
	if affected == 0 {
		return ErrNotFound
	}
	return tx.Commit()
}

func (s *Store) NodesWithImage(name string) ([]Node, error) {
	rows, err := s.db.Query(`SELECT n.name FROM node_images i JOIN nodes n ON n.id = i.node_id WHERE i.image = ? ORDER BY n.name`, name)
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
	var out []Node
	for _, name := range names {
		n, err := s.NodeByName(name)
		if err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, nil
}

func (s *Store) CreateComputer(c Computer) error {
	_, err := s.db.Exec(`INSERT INTO computers(name, node_id, image, cpu, memory, disk, state) VALUES(?, ?, ?, ?, ?, ?, ?)`,
		c.Name, c.NodeID, c.Image, c.CPU, c.Memory, c.Disk, c.State)
	return err
}

func (s *Store) Computer(name string) (Computer, error) {
	var c Computer
	err := s.db.QueryRow(`SELECT c.name, c.node_id, n.name, c.image, c.cpu, c.memory, c.disk, c.state, COALESCE(n.last_heartbeat, '')
		FROM computers c JOIN nodes n ON n.id = c.node_id WHERE c.name = ?`, name).
		Scan(&c.Name, &c.NodeID, &c.NodeName, &c.Image, &c.CPU, &c.Memory, &c.Disk, &c.State, new(string))
	if errors.Is(err, sql.ErrNoRows) {
		return Computer{}, ErrNotFound
	}
	if err != nil {
		return Computer{}, err
	}
	n, err := s.NodeByID(c.NodeID)
	if err != nil {
		return Computer{}, err
	}
	c.Heartbeat = n.LastHeartbeat
	return c, nil
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

func (s *Store) UsedOnNode(nodeID string) (cpu float64, memory, disk int64, err error) {
	err = s.db.QueryRow(`SELECT COALESCE(SUM(cpu),0), COALESCE(SUM(memory),0), COALESCE(SUM(disk),0) FROM computers WHERE node_id = ?`, nodeID).
		Scan(&cpu, &memory, &disk)
	return
}

func (s *Store) SetComputerState(name, state string) error {
	res, err := s.db.Exec(`UPDATE computers SET state = ? WHERE name = ?`, state, name)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) ResizeComputer(name string, cpu float64, memory, disk int64) error {
	_, err := s.db.Exec(`UPDATE computers SET cpu = ?, memory = ?, disk = ? WHERE name = ?`, cpu, memory, disk, name)
	return err
}

func (s *Store) RenameComputer(oldName, newName string) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	res, err := tx.Exec(`UPDATE computers SET name = ? WHERE name = ?`, newName, oldName)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	if _, err := tx.Exec(`UPDATE portals SET container = ? WHERE container = ?`, newName, oldName); err != nil {
		return err
	}
	if _, err := tx.Exec(`UPDATE shares SET computer = ? WHERE computer = ?`, newName, oldName); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) DeleteComputer(name string) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`DELETE FROM portals WHERE container = ?`, name); err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM shares WHERE computer = ?`, name); err != nil {
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

func (s *Store) ClaimPortal(p Portal) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var holder string
	err = tx.QueryRow(`SELECT container FROM portals WHERE hostname = ?`, p.Hostname).Scan(&holder)
	if err == nil {
		if holder != p.Container {
			return &HeldError{Label: p.Label, Holder: holder}
		}
		_, err = tx.Exec(`UPDATE portals SET port = ?, node_id = ?, claimed_at = ? WHERE hostname = ?`,
			p.Port, p.NodeID, s.now().Format(time.RFC3339Nano), p.Hostname)
		if err != nil {
			return err
		}
		return tx.Commit()
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	_, err = tx.Exec(`INSERT INTO portals(hostname, label, container, node_id, port, claimed_at) VALUES(?, ?, ?, ?, ?, ?)`,
		p.Hostname, p.Label, p.Container, p.NodeID, p.Port, s.now().Format(time.RFC3339Nano))
	if err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) PortalByHost(host string) (Portal, error) {
	return s.scanPortal(`SELECT hostname, label, container, node_id, port, claimed_at FROM portals WHERE hostname = ?`, host)
}

func (s *Store) scanPortal(q, arg string) (Portal, error) {
	var p Portal
	var at string
	err := s.db.QueryRow(q, arg).Scan(&p.Hostname, &p.Label, &p.Container, &p.NodeID, &p.Port, &at)
	if errors.Is(err, sql.ErrNoRows) {
		return Portal{}, ErrNotFound
	}
	if err != nil {
		return Portal{}, err
	}
	p.ClaimedAt, err = time.Parse(time.RFC3339Nano, at)
	return p, err
}

func (s *Store) PortalsByContainer(name string) ([]Portal, error) {
	rows, err := s.db.Query(`SELECT hostname FROM portals WHERE container = ? ORDER BY label`, name)
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

func (s *Store) DeletePortal(container, label string) error {
	res, err := s.db.Exec(`DELETE FROM portals WHERE container = ? AND label = ?`, container, label)
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

// EnvAll returns values for injection into a new container. Do not print the result.
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
