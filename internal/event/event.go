// Package event is the event log's shared vocabulary: field rules, topic
// filters, the one-line display form, and the log itself, which stores lines
// through the server's SQLite file and wakes long polls on publish.
package event

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	// MaxBody is the largest event body, in bytes: 1 MiB.
	MaxBody = 1 << 20
	// MaxTopicLen is the longest topic, in bytes.
	MaxTopicLen = 255
	// MaxKeyLen is the longest dedup key, in bytes.
	MaxKeyLen = 128
	// DefaultLimit is how many events one read returns unless asked otherwise.
	DefaultLimit = 100
	// MaxLimit is the most events one read returns.
	MaxLimit = 1000
	// MaxWait is the longest a read may block. It stays under the agent's
	// 30-second call timeout to the server.
	MaxWait = 25 * time.Second
	// MaxWaitSeconds is MaxWait for wire and HTTP fields that carry seconds.
	MaxWaitSeconds = 25
)

// Validation errors. HTTP turns them into one-line 400 reasons.
var (
	ErrInvalidTopic  = errors.New("invalid topic")
	ErrInvalidKey    = errors.New("invalid key")
	ErrInvalidFrom   = errors.New("invalid from")
	ErrBodyTooLarge  = errors.New("body over 1 MiB")
	ErrInvalidFilter = errors.New("invalid topic filter")
	ErrNoBody        = errors.New("no body: pass text or stdin")
)

// Item is one event. ID is assigned by the server and only increases.
// Time is UTC server time. Key is set when the publisher supplied one.
type Item struct {
	ID    int64
	Topic string
	Body  []byte
	From  string
	Key   string
	Time  time.Time
}

// itemJSON is Item on the HTTP API and the CLI --json form. A body that is
// valid UTF-8 is a JSON string in "body"; any other bytes are base64 in
// "body_b64". An empty body carries neither field.
type itemJSON struct {
	ID      int64     `json:"id"`
	Topic   string    `json:"topic"`
	Body    string    `json:"body,omitempty"`
	BodyB64 string    `json:"body_b64,omitempty"`
	From    string    `json:"from"`
	Key     string    `json:"key,omitempty"`
	Time    time.Time `json:"time"`
}

// MarshalJSON encodes the body rule described on itemJSON.
func (it Item) MarshalJSON() ([]byte, error) {
	j := itemJSON{ID: it.ID, Topic: it.Topic, From: it.From, Key: it.Key, Time: it.Time}
	if utf8.Valid(it.Body) {
		j.Body = string(it.Body)
	} else {
		j.BodyB64 = base64.StdEncoding.EncodeToString(it.Body)
	}
	return json.Marshal(j)
}

// UnmarshalJSON reads both body fields back. "body" wins when both appear.
func (it *Item) UnmarshalJSON(raw []byte) error {
	var j itemJSON
	if err := json.Unmarshal(raw, &j); err != nil {
		return err
	}
	it.ID, it.Topic, it.From, it.Key, it.Time = j.ID, j.Topic, j.From, j.Key, j.Time
	switch {
	case j.Body != "":
		it.Body = []byte(j.Body)
	case j.BodyB64 != "":
		b, err := base64.StdEncoding.DecodeString(j.BodyB64)
		if err != nil {
			return err
		}
		it.Body = b
	default:
		it.Body = nil
	}
	return nil
}

// Query is one read: events with id greater than Since, matching every
// filter, oldest first, at most Limit. Wait blocks the read when nothing
// matches yet.
type Query struct {
	Since  int64
	Topics []string
	Froms  []string
	Limit  int
	Wait   time.Duration
}

// Result is one read's answer. Oldest is the smallest id still retained and
// Latest the largest id ever assigned, so a client whose Since is below
// Oldest-1 knows it missed events, and one starting out can jump to Latest.
// More is set when Limit cut the result.
type Result struct {
	Events []Item `json:"events"`
	Oldest int64  `json:"oldest"`
	Latest int64  `json:"latest"`
	More   bool   `json:"more"`
}

// ValidTopic accepts 1–255 bytes of segments separated by "/". A segment is
// one or more of [A-Za-z0-9_.-]; no empty segments, so no leading or
// trailing "/" and none doubled. "#", "+", and whitespace cannot appear.
func ValidTopic(topic string) error {
	if len(topic) == 0 || len(topic) > MaxTopicLen {
		return ErrInvalidTopic
	}
	start := 0
	for i := 0; i <= len(topic); i++ {
		if i < len(topic) && topic[i] != '/' {
			continue
		}
		seg := topic[start:i]
		if len(seg) == 0 {
			return ErrInvalidTopic
		}
		for j := 0; j < len(seg); j++ {
			if !segByte(seg[j]) {
				return ErrInvalidTopic
			}
		}
		start = i + 1
	}
	return nil
}

func segByte(c byte) bool {
	switch {
	case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9':
		return true
	case c == '_' || c == '.' || c == '-':
		return true
	}
	return false
}

// ValidKey accepts 1–128 bytes of printable ASCII without spaces.
func ValidKey(key string) error {
	if len(key) == 0 || len(key) > MaxKeyLen {
		return ErrInvalidKey
	}
	for i := 0; i < len(key); i++ {
		if key[i] <= ' ' || key[i] >= 0x7f {
			return ErrInvalidKey
		}
	}
	return nil
}

// ValidFrom reports a publisher label: a token comment or fallback shape.
func ValidFrom(from string) bool {
	if len(from) == 0 || len(from) > 64 {
		return false
	}
	for i := 0; i < len(from); i++ {
		c := from[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9':
		case c == '_' || c == '.' || c == '-':
		default:
			return false
		}
	}
	return true
}

// ParseFrom sanitizes a token comment into a from label: the comment when it
// is one, else token-<id>.
func ParseFrom(comment string, id int64) string {
	if ValidFrom(comment) {
		return comment
	}
	return "token-" + strconv.FormatInt(id, 10)
}

// Filters is a compiled topic filter list: a union of exact topics and
// "/#" prefixes. A bare "#" matches everything.
type Filters struct {
	exact    []string
	prefixes []string // each ends with "/"
	all      bool
}

// ParseFilters validates and compiles filter values. Each is an exact topic,
// a "<topic>/#" prefix, or "#".
func ParseFilters(topics []string) (Filters, error) {
	var f Filters
	for _, t := range topics {
		switch {
		case t == "#":
			f.all = true
		case strings.HasSuffix(t, "/#"):
			stem := strings.TrimSuffix(t, "/#")
			if err := ValidTopic(stem); err != nil {
				return Filters{}, ErrInvalidFilter
			}
			f.prefixes = append(f.prefixes, stem+"/")
		default:
			if err := ValidTopic(t); err != nil {
				return Filters{}, ErrInvalidFilter
			}
			f.exact = append(f.exact, t)
		}
	}
	return f, nil
}

// Empty reports no constraint: every topic matches.
func (f Filters) Empty() bool { return !f.all && len(f.exact) == 0 && len(f.prefixes) == 0 }

// All reports a bare "#" among the values.
func (f Filters) All() bool { return f.all }

// Exacts lists the exact-match topics.
func (f Filters) Exacts() []string { return f.exact }

// Prefixes lists the prefix strings, each ending with "/".
func (f Filters) Prefixes() []string { return f.prefixes }

// Match reports whether one topic passes the filter.
func (f Filters) Match(topic string) bool {
	if f.Empty() || f.all {
		return true
	}
	for _, e := range f.exact {
		if topic == e {
			return true
		}
	}
	for _, p := range f.prefixes {
		if strings.HasPrefix(topic, p) {
			return true
		}
	}
	return false
}

// EscapeBody renders a body as one line: backslash, newline, carriage
// return, and tab become escapes. A body that is not valid UTF-8 becomes
// "base64:<...>" instead, so binary bodies stay one line.
func EscapeBody(body []byte) string {
	if !utf8.Valid(body) {
		return "base64:" + base64.StdEncoding.EncodeToString(body)
	}
	var b strings.Builder
	for i := 0; i < len(body); i++ {
		switch body[i] {
		case '\\':
			b.WriteString(`\\`)
		case '\n':
			b.WriteString(`\n`)
		case '\r':
			b.WriteString(`\r`)
		case '\t':
			b.WriteString(`\t`)
		default:
			b.WriteByte(body[i])
		}
	}
	return b.String()
}

// EscapeBodyMax is EscapeBody with the result cut to max runes, ending in
// "…" when anything was dropped. max <= 0 means no limit. The TUI uses it so
// a 1 MiB body cannot flood the summary.
func EscapeBodyMax(body []byte, max int) string {
	s := EscapeBody(body)
	if max <= 0 {
		return s
	}
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	if max <= 1 {
		return "…"
	}
	return string(r[:max-1]) + "…"
}

// ReadBody picks a publish body: the text arguments joined by spaces, or
// stdin when there are none and it is not a terminal.
func ReadBody(args []string, stdin io.Reader, stdinTTY bool) ([]byte, error) {
	if len(args) > 0 {
		return []byte(strings.Join(args, " ")), nil
	}
	if stdin == nil || stdinTTY {
		return nil, ErrNoBody
	}
	body, err := io.ReadAll(io.LimitReader(stdin, MaxBody+1))
	if err != nil {
		return nil, err
	}
	if len(body) > MaxBody {
		return nil, ErrBodyTooLarge
	}
	return body, nil
}
