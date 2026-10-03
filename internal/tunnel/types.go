// Package tunnel is the QUIC session between a server and a computer.
// The computer dials. One client stream carries newline-JSON control frames.
// The server opens ssh and portal streams.
package tunnel

import "time"

// ProtocolVersion is the only control version this package speaks.
const ProtocolVersion = 1

const alpn = "box"

const (
	OpHello       = "hello"
	OpPortalCheck = "portal_check"
	OpPortalAdd   = "portal_add"
	OpPortalRm    = "portal_rm"
	OpPortalLs    = "portal_ls"
	OpKeys        = "keys"
	OpStat        = "stat"
	OpEventPub    = "event_pub"
	OpEventGet    = "event_get"
)

const (
	KindSSH    = "ssh"
	KindPortal = "portal"
)

// Identity is the computer side of a hello.
type Identity struct {
	Name, Token, User, HostKey, AgentVersion string
}

// HelloResult is the server's reply to a successful hello.
type HelloResult struct {
	Domain string `json:"domain"`
}

// HelloBody is the first control frame, sent by the computer.
type HelloBody struct {
	Version      int    `json:"version"`
	Name         string `json:"name"`
	Token        string `json:"token"`
	User         string `json:"user"`
	HostKey      string `json:"host_key"`
	AgentVersion string `json:"agent_version"`
}

// PortalCheckRequest asks whether a label is free.
type PortalCheckRequest struct {
	Label string `json:"label"`
}

// PortalCheckResponse names the holder when the label is taken.
type PortalCheckResponse struct {
	Free   bool   `json:"free"`
	Holder string `json:"holder"`
}

// PortalAddRequest claims a label for a TCP port on the computer.
// Private requires an access token on later HTTP requests.
type PortalAddRequest struct {
	Label   string `json:"label"`
	Port    int    `json:"port"`
	Private bool   `json:"private,omitempty"`
}

// PortalAddResponse is the public address of a claim.
type PortalAddResponse struct {
	URL  string `json:"url"`
	Host string `json:"host"`
	Port int    `json:"port"`
}

// PortalRmRequest releases a label.
type PortalRmRequest struct {
	Label string `json:"label"`
}

// Portal is one claim.
type Portal struct {
	Label   string `json:"label"`
	Port    int    `json:"port"`
	Private bool   `json:"private,omitempty"`
}

// EventPublish is one event from a computer. Body is any bytes up to
// event.MaxBody; JSON carries it base64. The server sets From to the
// computer's name.
type EventPublish struct {
	Topic string `json:"topic"`
	Body  []byte `json:"body,omitempty"`
	Key   string `json:"key,omitempty"`
}

// EventResult answers a publish. Duplicate is set when a retained key made
// the server return the original event instead of storing a new one.
type EventResult struct {
	ID        int64 `json:"id"`
	Duplicate bool  `json:"duplicate,omitempty"`
}

// EventQuery asks for events. Topics is a union of exact topics and
// "prefix/#" filters; Froms a union of publisher labels; Wait whole seconds.
type EventQuery struct {
	Since  int64    `json:"since"`
	Topics []string `json:"topics,omitempty"`
	Froms  []string `json:"froms,omitempty"`
	Limit  int      `json:"limit,omitempty"`
	Wait   int      `json:"wait,omitempty"`
}

// EventItem is one event on the wire. Body is base64 in JSON.
type EventItem struct {
	ID    int64     `json:"id"`
	Topic string    `json:"topic"`
	Body  []byte    `json:"body,omitempty"`
	From  string    `json:"from"`
	Key   string    `json:"key,omitempty"`
	Time  time.Time `json:"time"`
}

// EventList answers a query. Oldest is the smallest retained id and Latest
// the largest id ever assigned; More is set when Limit cut the result.
type EventList struct {
	Events []EventItem `json:"events"`
	Oldest int64       `json:"oldest"`
	Latest int64       `json:"latest"`
	More   bool        `json:"more"`
}

// PortalList is the computer's claims.
type PortalList struct {
	Portals []Portal `json:"portals"`
}

// KeysRequest replaces the computer's authorized keys.
type KeysRequest struct {
	AuthorizedKeys []string `json:"authorized_keys"`
}

// StatRequest asks the computer for its live load. The body is empty.
type StatRequest struct{}

// StatResponse is the computer's live load.
// CPU is the 1-minute load average. Memory and Disk are used bytes. Uptime is seconds.
type StatResponse struct {
	CPU    float64 `json:"cpu"`
	Memory int64   `json:"memory"`
	Disk   int64   `json:"disk"`
	Uptime int64   `json:"uptime"`
}
