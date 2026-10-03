package event

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func TestValidTopic(t *testing.T) {
	for _, ok := range []string{"door", "site1/a", "site1/a/b", "a", "A-b_.9", strings.Repeat("x", 255)} {
		if err := ValidTopic(ok); err != nil {
			t.Errorf("rejected %q: %v", ok, err)
		}
	}
	for _, bad := range []string{
		"", "/lead", "trail/", "dou//ble", "has space", "plus+char", "hash#char",
		strings.Repeat("x", 256), "tab\tchar", "new\nline",
	} {
		if err := ValidTopic(bad); err == nil {
			t.Errorf("accepted %q", bad)
		}
	}
}

func TestValidKeyAndFrom(t *testing.T) {
	for _, ok := range []string{"a", strings.Repeat("k", 128), "open!:@", `idem-potency_1.`} {
		if err := ValidKey(ok); err != nil {
			t.Errorf("rejected key %q: %v", ok, err)
		}
	}
	for _, bad := range []string{"", "has space", "tab\t", "nl\n", strings.Repeat("k", 129), "\x01"} {
		if err := ValidKey(bad); err == nil {
			t.Errorf("accepted key %q", bad)
		}
	}
	for _, ok := range []string{"door", "token-1", "a-b_c.d", strings.Repeat("f", 64)} {
		if !ValidFrom(ok) {
			t.Errorf("rejected from %q", ok)
		}
	}
	for _, bad := range []string{"", "has space", strings.Repeat("f", 65), "sl/ash", "+"} {
		if ValidFrom(bad) {
			t.Errorf("accepted from %q", bad)
		}
	}
}

func TestParseFrom(t *testing.T) {
	for _, tc := range []struct {
		comment string
		id      int64
		want    string
	}{
		{"door-sensor", 3, "door-sensor"},
		{"", 3, "token-3"},
		{"my sensor", 12, "token-12"},
		{"sp", 0, "sp"},
	} {
		if got := ParseFrom(tc.comment, tc.id); got != tc.want {
			t.Errorf("ParseFrom(%q,%d) = %q, want %q", tc.comment, tc.id, got, tc.want)
		}
	}
}

func TestFilters(t *testing.T) {
	f, err := ParseFilters([]string{"site1/#"})
	if err != nil {
		t.Fatal(err)
	}
	for _, match := range []string{"site1/a", "site1/a/b"} {
		if !f.Match(match) {
			t.Errorf("site1/# rejected %s", match)
		}
	}
	for _, no := range []string{"site1", "site10/x", "site2"} {
		if f.Match(no) {
			t.Errorf("site1/# matched %s", no)
		}
	}
	f, err = ParseFilters([]string{"door", "window"})
	if err != nil {
		t.Fatal(err)
	}
	if !f.Match("door") || !f.Match("window") || f.Match("wall") {
		t.Fatal("exact union is wrong")
	}
	f, err = ParseFilters([]string{"#"})
	if err != nil {
		t.Fatal(err)
	}
	if !f.Match("anything/at/all") {
		t.Fatal("bare # did not match everything")
	}
	for _, bad := range []string{"site1/#/deep", "/#ok", "has space", "#/x", "site1/#x"} {
		if _, err := ParseFilters([]string{bad}); err == nil {
			t.Errorf("accepted filter %q", bad)
		}
	}
	// An exact topic and its own prefix together dedupe on Match.
	f, _ = ParseFilters([]string{"site1", "site1/#"})
	if !f.Match("site1") || !f.Match("site1/x") {
		t.Fatal("exact+prefix union is wrong")
	}
}

func TestEscapeBody(t *testing.T) {
	if got := EscapeBody([]byte("a\\b\nc\rd\te")); got != `a\\b\nc\rd\te` {
		t.Fatalf("escape %q", got)
	}
	bin := []byte{0xff, 0xfe}
	if got := EscapeBody(bin); got != "base64:"+base64.StdEncoding.EncodeToString(bin) {
		t.Fatalf("binary %q", got)
	}
	if got := EscapeBodyMax([]byte("hello"), 3); got != "he…" {
		t.Fatalf("max %q", got)
	}
	if got := EscapeBodyMax([]byte("hi"), 3); got != "hi" {
		t.Fatalf("max under %q", got)
	}
}

func TestItemJSONBodyRule(t *testing.T) {
	text := Item{ID: 7, Topic: "door", Body: []byte("open"), From: "door-sensor", Key: "k1"}
	raw, err := json.Marshal(text)
	if err != nil {
		t.Fatal(err)
	}
	s := string(raw)
	if !strings.Contains(s, `"body":"open"`) || strings.Contains(s, "body_b64") || !strings.Contains(s, `"key":"k1"`) {
		t.Fatalf("utf-8 body encoded wrong: %s", s)
	}
	var back Item
	if err := json.Unmarshal(raw, &back); err != nil {
		t.Fatal(err)
	}
	if string(back.Body) != "open" || back.Key != "k1" {
		t.Fatalf("round trip %+v", back)
	}
	bin := Item{ID: 8, Topic: "t", Body: []byte{0x00, 0xff}, From: "f"}
	raw, err = json.Marshal(bin)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"body_b64"`) {
		t.Fatalf("binary body not base64: %s", raw)
	}
	back = Item{}
	if err := json.Unmarshal(raw, &back); err != nil || string(back.Body) != "\x00\xff" {
		t.Fatalf("binary round trip %+v %v", back, err)
	}
	empty := Item{ID: 9, Topic: "t", From: "f"}
	raw, _ = json.Marshal(empty)
	if strings.Contains(string(raw), "body") {
		t.Fatalf("empty body carried a field: %s", raw)
	}
	back = Item{}
	if err := json.Unmarshal(raw, &back); err != nil || back.Body != nil {
		t.Fatalf("empty round trip %+v %v", back, err)
	}
}

func TestReadBody(t *testing.T) {
	if b, err := ReadBody([]string{"open", "now"}, nil, true); err != nil || string(b) != "open now" {
		t.Fatalf("args body %q %v", b, err)
	}
	if _, err := ReadBody(nil, nil, true); !errors.Is(err, ErrNoBody) {
		t.Fatalf("terminal stdin: %v", err)
	}
	if _, err := ReadBody(nil, nil, false); !errors.Is(err, ErrNoBody) {
		t.Fatalf("nil stdin: %v", err)
	}
	big := strings.Repeat("x", MaxBody+1)
	if _, err := ReadBody(nil, strings.NewReader(big), false); !errors.Is(err, ErrBodyTooLarge) {
		t.Fatalf("oversize stdin: %v", err)
	}
	if b, err := ReadBody(nil, strings.NewReader("from stdin"), false); err != nil || string(b) != "from stdin" {
		t.Fatalf("stdin body %q %v", b, err)
	}
}

func TestWaiterWakesEveryone(t *testing.T) {
	w := NewWaiter()
	a, b := w.Chan(), w.Chan()
	w.Wake()
	select {
	case <-a:
	default:
		t.Fatal("first channel was not closed")
	}
	select {
	case <-b:
	default:
		t.Fatal("second channel was not closed")
	}
	select {
	case <-w.Chan():
		t.Fatal("fresh channel was already closed")
	default:
	}
}
