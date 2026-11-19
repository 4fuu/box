package rpc

import "time"

type PortalBody struct {
	Label   string `json:"label"`
	Port    int    `json:"port,omitempty"`
	Private bool   `json:"private,omitempty"`
}

type PortalResult struct {
	Free   bool   `json:"free"`
	Holder string `json:"holder,omitempty"`
	URL    string `json:"url,omitempty"`
	Host   string `json:"host,omitempty"`
	Port   int    `json:"port,omitempty"`
}

type PortalList struct {
	Portals []PortalItem `json:"portals"`
}

type PortalItem struct {
	Label   string `json:"label"`
	Port    int    `json:"port"`
	Private bool   `json:"private,omitempty"`
}

// EventPublish is one event from a publisher. Body is any bytes up to
// event.MaxBody; JSON carries it base64. The server assigns From.
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

type DomainBody struct {
	Domain string `json:"domain"`
}

// SSHDEnableResult reports what box sshd enable did. Output is the
// ensureSSHD text for the guest to print. Enabled is false with Error set
// when the probe failed; ssh stays disabled in that case. Reconnecting is
// set when the agent closed its tunnel so the next hello carries the host
// key.
type SSHDEnableResult struct {
	Output       string `json:"output,omitempty"`
	Enabled      bool   `json:"enabled"`
	Reconnecting bool   `json:"reconnecting,omitempty"`
	Error        string `json:"error,omitempty"`
}

const (
	OpDomain      = "domain"
	OpPortalCheck = "portal_check"
	OpPortalAdd   = "portal_add"
	OpPortalLs    = "portal_ls"
	OpPortalRm    = "portal_rm"
	OpEventPub    = "event_pub"
	OpEventGet    = "event_get"
	OpSSHDEnable  = "sshd_enable"
)
