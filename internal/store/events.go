package store

import (
	"database/sql"
	"errors"
	"strings"
	"time"

	"github.com/4fuu/box/internal/event"
)

// eventTime is created_at's layout. It is fixed width so the text compares
// in time order; RFC3339Nano drops trailing zeros and does not.
const eventTime = "2006-01-02T15:04:05.000000000Z07:00"

// eventCols is the read shape shared by every event query.
const eventCols = `id, topic, body, from_label, dedup_key, created_at`

// InsertEvent stores one event and returns it with its id. AUTOINCREMENT
// keeps ids increasing and unused across restarts and pruning. When key is
// set and the same from_label already holds that key, the original row comes
// back with duplicate set: the unique index makes the check and the insert
// one transaction, so two racing publishers of the same key both get the
// first row.
func (s *Store) InsertEvent(from string, tokenID *int64, topic string, body []byte, key string) (event.Item, bool, error) {
	now := s.now()
	tx, err := s.db.Begin()
	if err != nil {
		return event.Item{}, false, err
	}
	defer tx.Rollback()
	if key != "" {
		item, found, err := eventByKey(tx, from, key)
		if err != nil {
			return event.Item{}, false, err
		}
		if found {
			return item, true, nil
		}
	}
	var tok any
	if tokenID != nil {
		tok = *tokenID
	}
	var dedup any
	if key != "" {
		dedup = key
	}
	if body == nil {
		body = []byte{} // the column is NOT NULL; an empty body is an empty blob
	}
	res, err := tx.Exec(`INSERT INTO events(topic, body, from_label, token_id, dedup_key, created_at)
		VALUES(?, ?, ?, ?, ?, ?)`, topic, body, from, tok, dedup, now.Format(eventTime))
	if err != nil {
		if key != "" && isUnique(err) {
			if item, found, kerr := eventByKey(tx, from, key); kerr == nil && found {
				return item, true, nil
			}
		}
		return event.Item{}, false, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return event.Item{}, false, err
	}
	if err := tx.Commit(); err != nil {
		return event.Item{}, false, err
	}
	// The event is stored; a failed prune only delays retention.
	_ = s.maybePruneEvents()
	return event.Item{ID: id, Topic: topic, Body: body, From: from, Key: key, Time: now}, false, nil
}

func eventByKey(tx *sql.Tx, from, key string) (event.Item, bool, error) {
	row := tx.QueryRow(`SELECT `+eventCols+` FROM events WHERE from_label = ? AND dedup_key = ?`, from, key)
	item, err := scanEvent(row)
	if errors.Is(err, sql.ErrNoRows) {
		return event.Item{}, false, nil
	}
	if err != nil {
		return event.Item{}, false, err
	}
	return item, true, nil
}

// QueryEvents returns events with id greater than Since, oldest first, at
// most Limit (Default..Max when out of range). Topics is a filter union of
// exact topics and "/#" prefixes; a prefix runs as an index range
// (topic >= prefix AND topic < upper), never LIKE. Every result carries the
// retained window: Oldest is the smallest id still stored, Latest the
// largest id ever assigned. More is set when Limit cut the result.
func (s *Store) QueryEvents(q event.Query) (event.Result, error) {
	filters, err := event.ParseFilters(q.Topics)
	if err != nil {
		return event.Result{}, err
	}
	for _, f := range q.Froms {
		if !event.ValidFrom(f) {
			return event.Result{}, event.ErrInvalidFrom
		}
	}
	limit := q.Limit
	if limit <= 0 {
		limit = event.DefaultLimit
	}
	if limit > event.MaxLimit {
		limit = event.MaxLimit
	}
	where := "id > ?"
	args := []any{q.Since}
	if !filters.Empty() && !filters.All() {
		var arms []string
		for _, t := range filters.Exacts() {
			arms = append(arms, "topic = ?")
			args = append(args, t)
		}
		for _, p := range filters.Prefixes() {
			// "site1/" bounded by "site10": the next byte after the prefix.
			// It matches site1/a and site1/a/b, not site1 or site10/x.
			arms = append(arms, "(topic >= ? AND topic < ?)")
			args = append(args, p, topicPrefixUpper(p))
		}
		where += " AND (" + strings.Join(arms, " OR ") + ")"
	}
	if len(q.Froms) > 0 {
		marks := make([]string, len(q.Froms))
		for i, f := range q.Froms {
			marks[i] = "?"
			args = append(args, f)
		}
		where += " AND from_label IN (" + strings.Join(marks, ",") + ")"
	}
	rows, err := s.db.Query(`SELECT `+eventCols+` FROM events WHERE `+where+`
		ORDER BY id ASC LIMIT ?`, append(args, limit+1)...)
	if err != nil {
		return event.Result{}, err
	}
	defer rows.Close()
	out := event.Result{Events: []event.Item{}}
	for rows.Next() {
		item, err := scanEvent(rows)
		if err != nil {
			return event.Result{}, err
		}
		out.Events = append(out.Events, item)
	}
	if err := rows.Err(); err != nil {
		return event.Result{}, err
	}
	if len(out.Events) > limit {
		out.Events = out.Events[:limit]
		out.More = true
	}
	oldest, latest, err := s.EventWindow()
	if err != nil {
		return event.Result{}, err
	}
	out.Oldest, out.Latest = oldest, latest
	return out, nil
}

// topicPrefixUpper is the exclusive upper bound of a prefix range: the
// prefix with its last byte incremented. A prefix of 0xFF bytes cannot be
// bounded; the empty answer matches everything above it.
func topicPrefixUpper(prefix string) string {
	b := []byte(prefix)
	i := len(b) - 1
	for i >= 0 && b[i] == 0xFF {
		i--
	}
	if i < 0 {
		return ""
	}
	b[i]++
	return string(b[:i+1])
}

// EventWindow returns the smallest retained id (0 when none) and the largest
// id ever assigned (0 before the first publish). Latest comes from
// sqlite_sequence, so it survives pruning and restarts.
func (s *Store) EventWindow() (int64, int64, error) {
	var oldest sql.NullInt64
	if err := s.db.QueryRow(`SELECT MIN(id) FROM events`).Scan(&oldest); err != nil {
		return 0, 0, err
	}
	latest, err := s.eventLatestID()
	if err != nil {
		return 0, 0, err
	}
	return oldest.Int64, latest, nil
}

func (s *Store) eventLatestID() (int64, error) {
	var seq sql.NullInt64
	err := s.db.QueryRow(`SELECT seq FROM sqlite_sequence WHERE name = 'events'`).Scan(&seq)
	if errors.Is(err, sql.ErrNoRows) {
		err = nil // no insert yet
	}
	if err != nil && strings.Contains(err.Error(), "no such table") {
		err = nil // a file from before this table existed
	}
	if err != nil {
		return 0, err
	}
	if seq.Valid {
		return seq.Int64, nil
	}
	var max sql.NullInt64
	if err := s.db.QueryRow(`SELECT MAX(id) FROM events`).Scan(&max); err != nil {
		return 0, err
	}
	return max.Int64, nil
}

// EventCount is how many events are retained.
func (s *Store) EventCount() (int64, error) {
	var n int64
	err := s.db.QueryRow(`SELECT COUNT(*) FROM events`).Scan(&n)
	return n, err
}

// RecentEvents returns the newest n events, oldest first. The summary shows
// these; full reads go through QueryEvents.
func (s *Store) RecentEvents(n int) ([]event.Item, error) {
	if n <= 0 {
		return []event.Item{}, nil
	}
	rows, err := s.db.Query(`SELECT `+eventCols+` FROM events ORDER BY id DESC LIMIT ?`, n)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []event.Item
	for rows.Next() {
		item, err := scanEvent(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return out, nil
}

// maybePruneEvents prunes on the write path, once per eventPruneStep inserts.
func (s *Store) maybePruneEvents() error {
	if s.eventPruneStep <= 0 || s.pruneCount.Add(1)%s.eventPruneStep != 0 {
		return nil
	}
	return s.pruneEvents()
}

// pruneEvents enforces retention with index-driven deletes: by id below
// latest minus eventRows, which keeps at most eventRows rows, and by
// created_at under the age cutoff. Both deletes walk an index.
func (s *Store) pruneEvents() error {
	latest, err := s.eventLatestID()
	if err != nil {
		return err
	}
	if _, err := s.db.Exec(`DELETE FROM events WHERE id <= ?`, latest-int64(s.eventRows)); err != nil {
		return err
	}
	cutoff := s.now().Add(-s.eventAge).Format(eventTime)
	_, err = s.db.Exec(`DELETE FROM events WHERE created_at < ?`, cutoff)
	return err
}

// scanner is a row or a QueryRow.
type scanner interface {
	Scan(dest ...any) error
}

func scanEvent(row scanner) (event.Item, error) {
	var item event.Item
	var body []byte
	var key sql.NullString
	var at string
	if err := row.Scan(&item.ID, &item.Topic, &body, &item.From, &key, &at); err != nil {
		return event.Item{}, err
	}
	item.Body = body
	item.Key = key.String
	var err error
	item.Time, err = time.Parse(time.RFC3339Nano, at)
	if err != nil {
		return event.Item{}, err
	}
	return item, nil
}
