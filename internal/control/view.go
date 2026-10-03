package control

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/4fuu/box/internal/event"
	"github.com/4fuu/box/internal/tunnel"
)

func FormatPairing(p Pairing, kind string) string {
	if kind == "node" {
		return fmt.Sprintf("code: %s\nexpires: %s\n", p.Secret, p.Expires.UTC().Format(time.RFC3339))
	}
	return fmt.Sprintf("one-time password: %s\nexpires: %s\n", p.Secret, p.Expires.UTC().Format(time.RFC3339))
}

func FormatComputers(list []ComputerView, asJSON bool) (string, error) {
	if asJSON {
		if list == nil {
			list = []ComputerView{}
		}
		return asJSONString(list)
	}
	var b bytes.Buffer
	w := tabwriter.NewWriter(&b, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "NAME\tONLINE\tUSER\tADDRESS\tAGENT\tPORTALS")
	for _, c := range list {
		online := "no"
		if c.Online {
			online = "yes"
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\n", c.Name, online, c.User, c.Address, c.AgentVersion, strings.Join(c.Portals, ","))
	}
	if err := w.Flush(); err != nil {
		return "", err
	}
	return b.String(), nil
}

func FormatPending(list []PendingView, asJSON bool) (string, error) {
	if asJSON {
		if list == nil {
			list = []PendingView{}
		}
		return asJSONString(list)
	}
	var b bytes.Buffer
	w := tabwriter.NewWriter(&b, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "NAME\tADDRESS\tUSER\tEXPIRES")
	for _, p := range list {
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", p.Name, p.Address, p.User, p.ExpiresAt.UTC().Format(time.RFC3339))
	}
	if err := w.Flush(); err != nil {
		return "", err
	}
	return b.String(), nil
}

func FormatKeys(list []KeyView, asJSON bool) (string, error) {
	if asJSON {
		if list == nil {
			list = []KeyView{}
		}
		return asJSONString(list)
	}
	var b strings.Builder
	for _, k := range list {
		fmt.Fprintf(&b, "%s  %s  %s\n", k.Fingerprint, k.Comment, k.BoundAt.UTC().Format(time.RFC3339))
	}
	return b.String(), nil
}

func FormatStat(v tunnel.StatResponse, asJSON bool) (string, error) {
	if asJSON {
		return asJSONString(v)
	}
	return fmt.Sprintf("cpu\t%g\nmemory\t%d\ndisk\t%d\nuptime\t%d\n", v.CPU, v.Memory, v.Disk, v.Uptime), nil
}

func FormatEnv(names []string, asJSON bool) (string, error) {
	if asJSON {
		if names == nil {
			names = []string{}
		}
		return asJSONString(struct {
			Names []string `json:"names"`
		}{Names: names})
	}
	if len(names) == 0 {
		return "", nil
	}
	return strings.Join(names, "\n") + "\n", nil
}

func FormatTokens(list []TokenView, asJSON bool) (string, error) {
	if asJSON {
		if list == nil {
			list = []TokenView{}
		}
		return asJSONString(list)
	}
	var b bytes.Buffer
	w := tabwriter.NewWriter(&b, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "ID\tCOMMENT\tEXPIRES\tTOKEN")
	for _, t := range list {
		fmt.Fprintf(w, "%d\t%s\t%s\t%s\n", t.ID, t.Comment, formatExpiry(t.Expires), t.Token)
	}
	if err := w.Flush(); err != nil {
		return "", err
	}
	return b.String(), nil
}

func FormatEvents(res event.Result, asJSON bool) (string, error) {
	if asJSON {
		if res.Events == nil {
			res.Events = []event.Item{}
		}
		return asJSONString(res)
	}
	var b bytes.Buffer
	w := tabwriter.NewWriter(&b, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "ID\tTIME\tFROM\tTOPIC\tBODY")
	for _, item := range res.Events {
		fmt.Fprintln(w, FormatEventLine(item))
	}
	if err := w.Flush(); err != nil {
		return "", err
	}
	return b.String(), nil
}

// FormatEventLine is one event as one tab-separated line. The body is
// escaped, so a line never wraps: backslash and controls become \-escapes,
// and a body that is not valid UTF-8 prints as base64:<...>.
func FormatEventLine(item event.Item) string {
	return fmt.Sprintf("%d\t%s\t%s\t%s\t%s", item.ID, item.Time.UTC().Format(time.RFC3339), item.From, item.Topic, event.EscapeBody(item.Body))
}

func FormatToken(t TokenView) string {
	return fmt.Sprintf("id: %d\ntoken: %s\nexpires: %s\n", t.ID, t.Token, formatExpiry(t.Expires))
}

func formatExpiry(t time.Time) string {
	if t.IsZero() {
		return "none"
	}
	return t.UTC().Format(time.RFC3339)
}

func FormatStatus(st Status, asJSON bool) (string, error) {
	if asJSON {
		return asJSONString(st)
	}
	return fmt.Sprintf("domain: %s\nkeys: %d\ncomputers: %d\n", st.Domain, st.Keys, st.Computers), nil
}

func asJSONString(v any) (string, error) {
	raw, err := json.Marshal(v)
	if err != nil {
		return "", err
	}
	return string(raw) + "\n", nil
}
