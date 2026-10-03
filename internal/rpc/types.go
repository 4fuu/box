package rpc

import "time"

// CreateBody is the server's request to make a computer.
// Env is injected into the container. Do not log this struct.
type CreateBody struct {
	Name           string            `json:"name"`
	ImageRef       string            `json:"image_ref"`
	CPU            float64           `json:"cpu"`
	Memory         int64             `json:"memory"`
	Disk           int64             `json:"disk"`
	Env            map[string]string `json:"env,omitempty"`
	AuthorizedKeys []string          `json:"authorized_keys"`
}

type ResizeBody struct {
	Name     string   `json:"name"`
	CPU      *float64 `json:"cpu,omitempty"`
	Memory   *int64   `json:"memory,omitempty"`
	Disk     *int64   `json:"disk,omitempty"`
	ImageRef string   `json:"image_ref,omitempty"`
}

type NameBody struct {
	Name string `json:"name"`
}

type RenameBody struct {
	Old string `json:"old"`
	New string `json:"new"`
}

type PullBody struct {
	Name string `json:"name"`
	Ref  string `json:"ref"`
}

type KeysBody struct {
	AuthorizedKeys []string `json:"authorized_keys"`
}

type HostKeyBody struct {
	Public string `json:"public"`
}

type StatBody struct {
	CPU     float64 `json:"cpu"`
	Memory  int64   `json:"memory"`
	Disk    int64   `json:"disk"`
	RX      int64   `json:"rx"`
	TX      int64   `json:"tx"`
	HasNet  bool    `json:"has_net"`
	Running bool    `json:"running"`
}

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

const (
	OpCreate   = "create"
	OpDelete   = "delete"
	OpRestart  = "restart"
	OpRename   = "rename"
	OpResize   = "resize"
	OpPull     = "pull"
	OpSyncKeys = "sync_keys"
	OpHostKey  = "hostkey"
	OpStat     = "stat"

	OpDomain      = "domain"
	OpPortalCheck = "portal_check"
	OpPortalAdd   = "portal_add"
	OpPortalLs    = "portal_ls"
	OpPortalRm    = "portal_rm"
	OpEventPub    = "event_pub"
	OpEventGet    = "event_get"
)

// ErrNoCapacity is the text a node returns when a computer does not fit.
const ErrNoCapacity = "no capacity"
